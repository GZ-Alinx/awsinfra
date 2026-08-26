package gitlab

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/GZ-Alinx/awsinfra/internal/appconfig"
	"github.com/GZ-Alinx/awsinfra/internal/cicd"
)

var (
	keyPattern                     = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)
	groupPattern                   = regexp.MustCompile(`^[A-Za-z0-9_.-]+(?:/[A-Za-z0-9_.-]+)*$`)
	credentialPattern              = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,127}$`)
	resourcePattern                = regexp.MustCompile(`^[0-9]+(?:m)?$`)
	memoryPattern                  = regexp.MustCompile(`^[0-9]+(?:Ki|Mi|Gi|Ti)$`)
	relativePathPattern            = regexp.MustCompile(`^(?:\.|[A-Za-z0-9_][A-Za-z0-9._/-]*)$`)
	imageRepositoryPattern         = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:/-]{0,499}$`)
	dockerTargetPattern            = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,127}$`)
	etcdHostPattern                = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9.-]*:[0-9]{1,5}$`)
	configFilePattern              = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,127}\.ya?ml$`)
	healthPathPattern              = regexp.MustCompile(`^/[A-Za-z0-9._~!$&()*+,=:@%/-]*$`)
	environmentKeyPattern          = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,127}$`)
	secretDataKeyPattern           = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,252}$`)
	sensitiveEnvironmentKeyPattern = regexp.MustCompile(`(?i)(PASSWORD|PASSWD|SECRET|TOKEN|PRIVATE_KEY|ACCESS_KEY)`)
	sensitiveJavaOptionPattern     = regexp.MustCompile(`(?i)(?:password|passwd|secret|token|private[_-]?key|access[_-]?key)\s*=`)
)

type Service struct {
	store Store
	aead  cipher.AEAD
}

func New(config *appconfig.Config, store Store) (*Service, error) {
	encoded := strings.TrimSpace(config.CredentialKey())
	if encoded == "" {
		return nil, fmt.Errorf("%s is not set", config.Security.CredentialKeyEnv)
	}
	key, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil || len(key) != 32 {
		return nil, fmt.Errorf("%s must contain a base64-encoded 32-byte key", config.Security.CredentialKeyEnv)
	}
	block, err := aes.NewCipher(key)
	clear(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &Service{store: store, aead: aead}, nil
}

func (s *Service) ListServers(ctx context.Context) ([]Server, error) {
	items, err := s.store.ListGitLabServers(ctx)
	if err != nil {
		return nil, err
	}
	result := make([]Server, 0, len(items))
	for _, item := range items {
		item.Server.Configured = item.TokenCipher != ""
		result = append(result, item.Server)
	}
	return result, nil
}

func (s *Service) SaveServer(ctx context.Context, key string, input ServerInput) (Server, error) {
	key = strings.ToLower(strings.TrimSpace(key))
	if key == "" {
		key = strings.ToLower(strings.TrimSpace(input.Key))
	}
	if !keyPattern.MatchString(key) || strings.TrimSpace(input.DisplayName) == "" {
		return Server{}, fmt.Errorf("%w：服务器标识或名称不正确", ErrInvalid)
	}
	baseURL, err := validateBaseURL(input.BaseURL, input.AllowInsecureHTTP)
	if err != nil {
		return Server{}, fmt.Errorf("%w：%v", ErrInvalid, err)
	}
	rootGroups, err := normalizeRootGroups(input.RootGroup, input.RootGroups)
	if err != nil {
		return Server{}, err
	}
	rootGroup := rootGroups[0]
	branch := strings.TrimSpace(input.DefaultBranch)
	if branch == "" {
		branch = "main"
	}
	if len(branch) > 255 || strings.ContainsAny(branch, " ~^:?*[\\\n\r\t") {
		return Server{}, fmt.Errorf("%w：默认分支不正确", ErrInvalid)
	}
	visibility := strings.ToLower(strings.TrimSpace(input.Visibility))
	if visibility == "" {
		visibility = "private"
	}
	if visibility != "private" && visibility != "internal" {
		return Server{}, fmt.Errorf("%w：交付仓库只允许私有或内部可见", ErrInvalid)
	}
	record := StoredServer{Server: Server{Key: key, DisplayName: limit(input.DisplayName, 128), BaseURL: baseURL, RootGroup: rootGroup, RootGroups: rootGroups, DefaultBranch: branch, Visibility: visibility, AllowInsecureHTTP: input.AllowInsecureHTTP}}
	if existing, getErr := s.store.GetGitLabServer(ctx, key); getErr == nil {
		bindings, countErr := s.store.GitLabServerBindingCount(ctx, key)
		if countErr != nil {
			return Server{}, countErr
		}
		if bindings > 0 && existing.BaseURL != record.BaseURL {
			return Server{}, fmt.Errorf("%w：该 GitLab 已被 %d 个项目绑定，不能修改服务器地址", ErrConflict, bindings)
		}
		boundGroups, boundErr := s.store.GitLabServerBindingRootGroups(ctx, key)
		if boundErr != nil {
			return Server{}, boundErr
		}
		for _, boundGroup := range boundGroups {
			if !containsRootGroup(rootGroups, boundGroup) {
				return Server{}, fmt.Errorf("%w：授权根组 %s 已被项目使用，不能移除", ErrConflict, boundGroup)
			}
		}
		boundRepositories, repositoryErr := s.store.GitLabServerSourceRepositories(ctx, key)
		if repositoryErr != nil {
			return Server{}, repositoryErr
		}
		for _, repository := range boundRepositories {
			if _, ok := repositoryRootGroup(repository, record.BaseURL, rootGroups); !ok {
				return Server{}, fmt.Errorf("%w：授权根组仍被业务源码仓库 %s 使用，不能移除", ErrConflict, repository)
			}
		}
		record.TokenCipher = existing.TokenCipher
		record.CreatedAt = existing.CreatedAt
		record.LastCheckStatus, record.LastCheckError, record.LastCheckedAt = existing.LastCheckStatus, existing.LastCheckError, existing.LastCheckedAt
	} else if !errors.Is(getErr, os.ErrNotExist) {
		return Server{}, getErr
	}
	if token := strings.TrimSpace(input.AccessToken); token != "" {
		if len(token) > 4096 {
			return Server{}, fmt.Errorf("%w：Access Token 过长", ErrInvalid)
		}
		record.TokenCipher, err = s.encrypt(serverAAD(key), []byte(token))
		input.AccessToken = ""
		if err != nil {
			return Server{}, err
		}
	}
	if record.TokenCipher == "" {
		return Server{}, fmt.Errorf("%w：Access Token 不能为空", ErrInvalid)
	}
	if err := s.store.SaveGitLabServer(ctx, record); err != nil {
		return Server{}, err
	}
	stored, err := s.store.GetGitLabServer(ctx, key)
	if err != nil {
		return Server{}, err
	}
	stored.Server.Configured = true
	return stored.Server, nil
}

func (s *Service) DeleteServer(ctx context.Context, key string) error {
	return s.store.DeleteGitLabServer(ctx, strings.ToLower(strings.TrimSpace(key)))
}

func (s *Service) TestServer(ctx context.Context, key string) (Server, error) {
	record, client, err := s.client(ctx, key)
	if err != nil {
		return Server{}, err
	}
	checkErr := client.currentUser(ctx)
	if checkErr == nil {
		for _, rootGroup := range serverRootGroups(record.Server) {
			group, groupErr := client.group(ctx, rootGroup)
			if groupErr != nil {
				checkErr = fmt.Errorf("无法访问授权根组 %s: %w", rootGroup, groupErr)
				break
			}
			if group.ID == 0 || !strings.EqualFold(group.FullPath, rootGroup) {
				checkErr = fmt.Errorf("无法访问授权根组 %s：GitLab 返回的组不匹配", rootGroup)
				break
			}
		}
	}
	record.LastCheckedAt = time.Now().UTC()
	if checkErr != nil {
		record.LastCheckStatus, record.LastCheckError = "failed", limit(checkErr.Error(), 1000)
	} else {
		record.LastCheckStatus, record.LastCheckError = "healthy", ""
	}
	if saveErr := s.store.SaveGitLabServer(ctx, record); saveErr != nil {
		return Server{}, saveErr
	}
	record.Server.Configured = true
	return record.Server, checkErr
}

func (s *Service) GetDelivery(ctx context.Context, project string) (ProjectDelivery, error) {
	item, err := s.store.GetProjectGitLabDelivery(ctx, strings.TrimSpace(project))
	if errors.Is(err, os.ErrNotExist) {
		return ProjectDelivery{ProjectKey: strings.TrimSpace(project), Services: []ServiceSpec{}}, nil
	}
	return item, err
}

func (s *Service) ProjectDeletionBlocker(ctx context.Context, project string) (string, error) {
	item, err := s.store.GetProjectGitLabDelivery(ctx, strings.TrimSpace(project))
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	if item.JenkinsfilesProjectID != 0 || item.ManifestsProjectID != 0 || item.JenkinsfilesCloneURL != "" || item.ManifestsCloneURL != "" {
		return "项目仍绑定 GitLab 部署流水线仓库和部署清单仓库；请先在“CICD / 项目接入”中解除绑定（不会删除 GitLab 远程仓库）", nil
	}
	return "", nil
}

func (s *Service) DetachDelivery(ctx context.Context, project string) error {
	err := s.store.DetachProjectGitLabDelivery(ctx, strings.TrimSpace(project))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

func (s *Service) SaveDelivery(ctx context.Context, project string, input ProjectDelivery) (ProjectDelivery, error) {
	project = strings.TrimSpace(project)
	serverKey := strings.ToLower(strings.TrimSpace(input.ServerKey))
	if !keyPattern.MatchString(project) || serverKey == "" {
		return ProjectDelivery{}, fmt.Errorf("%w：项目和 GitLab 服务器不能为空", ErrInvalid)
	}
	server, err := s.store.GetGitLabServer(ctx, serverKey)
	if err != nil || server.TokenCipher == "" {
		return ProjectDelivery{}, fmt.Errorf("%w：选择的 GitLab 服务器不可用", ErrInvalid)
	}
	existing, existingErr := s.store.GetProjectGitLabDelivery(ctx, project)
	if existingErr != nil && !errors.Is(existingErr, os.ErrNotExist) {
		return ProjectDelivery{}, existingErr
	}
	rootGroup := strings.Trim(strings.TrimSpace(input.RootGroup), "/")
	if rootGroup == "" && existingErr == nil && existing.ServerKey == serverKey {
		rootGroup = existing.RootGroup
	}
	if rootGroup == "" {
		rootGroup = server.RootGroup
	}
	if !containsRootGroup(serverRootGroups(server.Server), rootGroup) {
		return ProjectDelivery{}, fmt.Errorf("%w：交付根组 %s 不在 GitLab 授权范围内", ErrInvalid, rootGroup)
	}
	sourceServerKey := strings.ToLower(strings.TrimSpace(input.SourceServerKey))
	if sourceServerKey == "" && existingErr == nil {
		sourceServerKey = existing.SourceServerKey
	}
	var sourceServer StoredServer
	if sourceServerKey != "" {
		sourceServer, err = s.store.GetGitLabServer(ctx, sourceServerKey)
		if err != nil || sourceServer.TokenCipher == "" {
			return ProjectDelivery{}, fmt.Errorf("%w：选择的业务源码 GitLab 服务器不可用", ErrInvalid)
		}
	}
	sourceRootGroup := strings.Trim(strings.TrimSpace(input.SourceRootGroup), "/")
	if sourceServerKey != "" {
		if sourceRootGroup == "" && existingErr == nil && existing.SourceServerKey == sourceServerKey {
			sourceRootGroup = existing.SourceRootGroup
		}
		if sourceRootGroup == "" {
			sourceRootGroup = sourceServer.RootGroup
		}
		if !containsRootGroup(serverRootGroups(sourceServer.Server), sourceRootGroup) {
			return ProjectDelivery{}, fmt.Errorf("%w：业务源码根组 %s 不在 GitLab 授权范围内", ErrInvalid, sourceRootGroup)
		}
	}
	services, err := normalizeServices(input.Services, project)
	if err != nil {
		return ProjectDelivery{}, err
	}
	if sourceServerKey != "" {
		for _, service := range services {
			if _, ok := repositoryRootGroup(service.SourceRepository, sourceServer.BaseURL, serverRootGroups(sourceServer.Server)); !ok {
				return ProjectDelivery{}, fmt.Errorf("%w：服务 %s 的源码仓库不属于业务源码 GitLab 的任一授权根组", ErrInvalid, service.Key)
			}
		}
	}
	item := ProjectDelivery{
		ProjectKey: project, ServerKey: serverKey, RootGroup: rootGroup, SourceServerKey: sourceServerKey, SourceRootGroup: sourceRootGroup, DefaultBranch: server.DefaultBranch,
		JenkinsfilesProjectPath: rootGroup + "/" + project + "-ops-delivery",
		ManifestsProjectPath:    rootGroup + "/" + project + "-ops-delivery",
		Services:                services,
	}
	if existingErr == nil {
		if existing.JenkinsfilesProjectPath != "" {
			item.JenkinsfilesProjectPath = rootGroup + "/" + path.Base(existing.JenkinsfilesProjectPath)
		}
		if existing.ManifestsProjectPath != "" {
			item.ManifestsProjectPath = rootGroup + "/" + path.Base(existing.ManifestsProjectPath)
		}
		deliveryBindingChanged := existing.ServerKey != serverKey || existing.RootGroup != rootGroup
		hasDeliveryRepositories := existing.JenkinsfilesProjectID != 0 || existing.ManifestsProjectID != 0 ||
			existing.JenkinsfilesCloneURL != "" || existing.ManifestsCloneURL != ""
		if deliveryBindingChanged && hasDeliveryRepositories {
			jenkinsfiles, manifests, verifyErr := s.verifyDeliveryRebind(ctx, project, item, existing)
			if verifyErr != nil {
				return ProjectDelivery{}, verifyErr
			}
			item.JenkinsfilesProjectID, item.JenkinsfilesCloneURL = jenkinsfiles.ID, jenkinsfiles.HTTPURLToRepo
			item.ManifestsProjectID, item.ManifestsCloneURL = manifests.ID, manifests.HTTPURLToRepo
		} else {
			item.JenkinsfilesProjectID, item.JenkinsfilesCloneURL = existing.JenkinsfilesProjectID, existing.JenkinsfilesCloneURL
			item.ManifestsProjectID, item.ManifestsCloneURL = existing.ManifestsProjectID, existing.ManifestsCloneURL
		}
		item.ProvisionStatus, item.ProvisionError, item.LastProvisionedAt, item.CreatedAt = existing.ProvisionStatus, existing.ProvisionError, existing.LastProvisionedAt, existing.CreatedAt
	}
	if err := s.store.SaveProjectGitLabDelivery(ctx, item); err != nil {
		return ProjectDelivery{}, err
	}
	if sourceServerKey != "" {
		if err := s.registerSourceRepositories(ctx, item); err != nil {
			return ProjectDelivery{}, err
		}
	}
	return s.store.GetProjectGitLabDelivery(ctx, project)
}

// Activate saves the project bindings and provisions the delivery repository
// in one API operation. A failed GitLab write intentionally keeps
// the validated binding and failed status so the same request can be retried
// without re-entering configuration.
func (s *Service) Activate(ctx context.Context, project string, input ProjectDelivery) (ProvisionResult, error) {
	if _, err := s.SaveDelivery(ctx, project, input); err != nil {
		return ProvisionResult{}, err
	}
	return s.Provision(ctx, project)
}

// verifyDeliveryRebind permits changing a GitLab connection or root group only
// when both target paths resolve to the exact repositories already owned by
// this project.
func (s *Service) verifyDeliveryRebind(ctx context.Context, project string, desired, existing ProjectDelivery) (gitLabProject, gitLabProject, error) {
	_, client, err := s.client(ctx, desired.ServerKey)
	if err != nil {
		return gitLabProject{}, gitLabProject{}, err
	}
	defer func() { client.token = "" }()
	verify := func(path, kind string, existingID int64) (gitLabProject, error) {
		repository, exists, requestErr := client.project(ctx, path)
		if requestErr != nil {
			return gitLabProject{}, requestErr
		}
		if !exists {
			return gitLabProject{}, fmt.Errorf("%w：目标 GitLab 中找不到原交付仓库 %s；为避免误绑，已拒绝切换", ErrConflict, path)
		}
		if existingID != 0 && repository.ID != existingID {
			return gitLabProject{}, fmt.Errorf("%w：目标仓库 %s 与当前绑定的仓库 ID 不一致；为避免误绑，已拒绝切换", ErrConflict, path)
		}
		managed, markerErr := validManagedMarker(ctx, client, repository.ID, desired.DefaultBranch, project, kind)
		if markerErr != nil {
			return gitLabProject{}, markerErr
		}
		if !managed {
			return gitLabProject{}, fmt.Errorf("%w：目标仓库 %s 缺少当前项目的平台管理标记；为避免覆盖已有仓库，已拒绝切换", ErrConflict, path)
		}
		return repository, nil
	}
	if unifiedDelivery(existing) {
		delivery, err := verify(desired.JenkinsfilesProjectPath, "delivery", existing.JenkinsfilesProjectID)
		return delivery, delivery, err
	}
	jenkinsfiles, err := verify(desired.JenkinsfilesProjectPath, "jenkinsfiles", existing.JenkinsfilesProjectID)
	if err != nil {
		return gitLabProject{}, gitLabProject{}, err
	}
	manifests, err := verify(desired.ManifestsProjectPath, "manifests", existing.ManifestsProjectID)
	if err != nil {
		return gitLabProject{}, gitLabProject{}, err
	}
	return jenkinsfiles, manifests, nil
}

func normalizeRootGroups(primary string, values []string) ([]string, error) {
	candidates := append([]string{}, values...)
	if primary = strings.Trim(strings.TrimSpace(primary), "/"); primary != "" {
		candidates = append([]string{primary}, candidates...)
	}
	result := make([]string, 0, len(candidates))
	seen := map[string]bool{}
	for _, candidate := range candidates {
		candidate = strings.Trim(strings.TrimSpace(candidate), "/")
		if candidate == "" {
			continue
		}
		if !groupPattern.MatchString(candidate) || len(candidate) > 500 {
			return nil, fmt.Errorf("%w：授权根组路径 %q 不正确", ErrInvalid, candidate)
		}
		identity := strings.ToLower(candidate)
		if seen[identity] {
			continue
		}
		seen[identity] = true
		result = append(result, candidate)
		if len(result) > 20 {
			return nil, fmt.Errorf("%w：单个 GitLab 最多配置 20 个授权根组", ErrInvalid)
		}
	}
	if len(result) == 0 {
		return nil, fmt.Errorf("%w：至少需要一个授权根组", ErrInvalid)
	}
	return result, nil
}

func serverRootGroups(server Server) []string {
	groups, err := normalizeRootGroups(server.RootGroup, server.RootGroups)
	if err != nil {
		return []string{strings.Trim(strings.TrimSpace(server.RootGroup), "/")}
	}
	return groups
}

func containsRootGroup(groups []string, target string) bool {
	target = strings.Trim(strings.TrimSpace(target), "/")
	for _, group := range groups {
		if strings.EqualFold(strings.Trim(group, "/"), target) {
			return true
		}
	}
	return false
}

func validateRepositoryServer(repositoryURL, serverURL, rootGroup string) error {
	repository, err := url.Parse(strings.TrimSpace(repositoryURL))
	if err != nil || repository.Host == "" {
		return ErrInvalid
	}
	server, err := url.Parse(strings.TrimSpace(serverURL))
	if err != nil || server.Host == "" || !strings.EqualFold(repository.Scheme, server.Scheme) || !strings.EqualFold(repository.Host, server.Host) {
		return ErrInvalid
	}
	serverPath := strings.Trim(server.Path, "/")
	repositoryPath := strings.Trim(repository.Path, "/")
	if serverPath != "" {
		if repositoryPath == serverPath || !strings.HasPrefix(repositoryPath, serverPath+"/") {
			return ErrInvalid
		}
		repositoryPath = strings.TrimPrefix(repositoryPath, serverPath+"/")
	}
	repositoryPath, err = url.PathUnescape(strings.TrimSuffix(repositoryPath, ".git"))
	rootGroup = strings.Trim(strings.TrimSpace(rootGroup), "/")
	if err != nil || rootGroup == "" || (repositoryPath != rootGroup && !strings.HasPrefix(repositoryPath, rootGroup+"/")) {
		return ErrInvalid
	}
	return nil
}

// repositoryRootGroup returns the most specific configured root group that
// owns a repository. A GitLab connection may authorize multiple sibling or
// nested groups; matching the longest path keeps generated credentials scoped
// to the narrowest available group.
func repositoryRootGroup(repositoryURL, serverURL string, rootGroups []string) (string, bool) {
	matched := ""
	for _, rootGroup := range rootGroups {
		if validateRepositoryServer(repositoryURL, serverURL, rootGroup) == nil && len(rootGroup) > len(matched) {
			matched = strings.Trim(rootGroup, "/")
		}
	}
	return matched, matched != ""
}

func (s *Service) Preview(ctx context.Context, project string) (Preview, error) {
	delivery, server, client, err := s.deliveryClient(ctx, project)
	if err != nil {
		return Preview{}, err
	}
	files := generateDeliveryFiles(project, delivery, server.Server)
	plans, err := s.repositoryPlans(ctx, client, delivery, files)
	if err != nil {
		return Preview{}, err
	}
	return Preview{ProjectKey: project, Repositories: plans, Files: files}, nil
}

func (s *Service) Provision(ctx context.Context, project string) (result ProvisionResult, resultErr error) {
	delivery, server, client, err := s.deliveryClient(ctx, project)
	if err != nil {
		return result, err
	}
	delivery.ProvisionStatus, delivery.ProvisionError = "running", ""
	_ = s.store.SaveProjectGitLabDelivery(ctx, delivery)
	defer func() {
		if resultErr != nil {
			delivery.ProvisionStatus, delivery.ProvisionError = "failed", limit(resultErr.Error(), 1000)
			_ = s.store.SaveProjectGitLabDelivery(context.WithoutCancel(ctx), delivery)
		}
	}()
	files := generateDeliveryFiles(project, delivery, server.Server)
	group, err := client.group(ctx, delivery.RootGroup)
	if err != nil {
		return result, err
	}
	repositories := []struct {
		kind, name, path string
		files            []GeneratedFile
	}{
		{kind: "jenkinsfiles", name: project + " Jenkinsfiles", path: project + "-jenkinsfiles"},
		{kind: "manifests", name: project + " Deploy Manifests", path: project + "-deploy-manifests"},
	}
	if unifiedDelivery(delivery) {
		repositories = repositories[:0]
		repositories = append(repositories, struct {
			kind, name, path string
			files            []GeneratedFile
		}{kind: "delivery", name: project + " Ops Delivery", path: path.Base(delivery.JenkinsfilesProjectPath)})
	}
	for index := range repositories {
		for _, file := range files {
			if file.Repository == repositories[index].kind {
				repositories[index].files = append(repositories[index].files, file)
			}
		}
	}
	result.Repositories = make([]RepositoryPlan, 0, len(repositories))
	for _, target := range repositories {
		fullPath := delivery.RootGroup + "/" + target.path
		projectInfo, exists, lookupErr := client.project(ctx, fullPath)
		if lookupErr != nil {
			return result, lookupErr
		}
		createdNow := false
		if !exists {
			projectInfo, lookupErr = client.createProject(ctx, group.ID, target.name, target.path, delivery.DefaultBranch, server.Visibility)
			if lookupErr != nil {
				createErr := lookupErr
				// A concurrent retry may have created the same exact repository.
				projectInfo, exists, lookupErr = client.project(ctx, fullPath)
				if lookupErr != nil {
					return result, lookupErr
				}
				if !exists {
					return result, createErr
				}
			} else {
				createdNow = true
			}
		} else if projectInfo.PathWithNamespace != fullPath {
			return result, fmt.Errorf("%w：返回的仓库路径与项目绑定不一致", ErrConflict)
		}
		if projectInfo.PathWithNamespace != fullPath {
			return result, fmt.Errorf("%w：返回的仓库路径与项目绑定不一致", ErrConflict)
		}
		boundProjectID := delivery.JenkinsfilesProjectID
		if target.kind == "manifests" {
			boundProjectID = delivery.ManifestsProjectID
		} else if target.kind == "delivery" && boundProjectID == 0 {
			boundProjectID = delivery.ManifestsProjectID
		}
		if createdNow {
			if target.kind == "delivery" {
				delivery.JenkinsfilesProjectID, delivery.JenkinsfilesCloneURL = projectInfo.ID, projectInfo.HTTPURLToRepo
				delivery.ManifestsProjectID, delivery.ManifestsCloneURL = projectInfo.ID, projectInfo.HTTPURLToRepo
			} else if target.kind == "jenkinsfiles" {
				delivery.JenkinsfilesProjectID, delivery.JenkinsfilesCloneURL = projectInfo.ID, projectInfo.HTTPURLToRepo
			} else {
				delivery.ManifestsProjectID, delivery.ManifestsCloneURL = projectInfo.ID, projectInfo.HTTPURLToRepo
			}
			// Persist ownership before the first commit so a failed request can be retried
			// without claiming an unrelated empty repository with the same path.
			if saveErr := s.store.SaveProjectGitLabDelivery(ctx, delivery); saveErr != nil {
				return result, saveErr
			}
			boundProjectID = projectInfo.ID
		}
		managed, lookupErr := validManagedMarker(ctx, client, projectInfo.ID, delivery.DefaultBranch, project, target.kind)
		if lookupErr != nil {
			return result, lookupErr
		}
		if !managed {
			if boundProjectID != projectInfo.ID {
				return result, fmt.Errorf("%w：仓库 %s 已存在但不属于当前项目的平台绑定，已拒绝接管", ErrConflict, fullPath)
			}
			hasFiles, treeErr := client.repositoryHasFiles(ctx, projectInfo.ID, delivery.DefaultBranch)
			if treeErr != nil {
				return result, treeErr
			}
			if hasFiles {
				return result, fmt.Errorf("%w：仓库 %s 已有内容且不是平台管理的仓库，已拒绝覆盖", ErrConflict, fullPath)
			}
		}
		for start := 0; start < len(target.files); start += 50 {
			end := start + 50
			if end > len(target.files) {
				end = len(target.files)
			}
			created, updated, commitErr := client.commitFiles(ctx, projectInfo.ID, delivery.DefaultBranch, fmt.Sprintf("chore: sync %s delivery files", project), target.files[start:end])
			if commitErr != nil {
				return result, commitErr
			}
			result.CreatedFiles, result.UpdatedFiles = result.CreatedFiles+created, result.UpdatedFiles+updated
		}
		if target.kind == "manifests" || target.kind == "delivery" {
			deletePaths, cleanupErr := s.verifiedLegacyKustomizationPaths(ctx, client, projectInfo.ID, delivery)
			if cleanupErr != nil {
				return result, cleanupErr
			}
			if len(deletePaths) > 0 {
				if _, _, cleanupErr = client.commitFilesWithDeletes(ctx, projectInfo.ID, delivery.DefaultBranch, fmt.Sprintf("chore: remove obsolete %s kustomization files", project), nil, deletePaths); cleanupErr != nil {
					return result, cleanupErr
				}
				result.DeletedFiles += len(deletePaths)
			}
		}
		plan := RepositoryPlan{Kind: target.kind, Name: target.name, ProjectPath: fullPath, CloneURL: projectInfo.HTTPURLToRepo, Exists: exists, Managed: true}
		result.Repositories = append(result.Repositories, plan)
		if target.kind == "delivery" {
			delivery.JenkinsfilesProjectID, delivery.JenkinsfilesCloneURL = projectInfo.ID, projectInfo.HTTPURLToRepo
			delivery.ManifestsProjectID, delivery.ManifestsCloneURL = projectInfo.ID, projectInfo.HTTPURLToRepo
		} else if target.kind == "jenkinsfiles" {
			delivery.JenkinsfilesProjectID, delivery.JenkinsfilesCloneURL = projectInfo.ID, projectInfo.HTTPURLToRepo
		} else {
			delivery.ManifestsProjectID, delivery.ManifestsCloneURL = projectInfo.ID, projectInfo.HTTPURLToRepo
		}
	}
	if err := s.registerRepositories(ctx, delivery); err != nil {
		return result, err
	}
	delivery.ProvisionStatus, delivery.ProvisionError, delivery.LastProvisionedAt = "ready", "", time.Now().UTC()
	if err := s.store.SaveProjectGitLabDelivery(ctx, delivery); err != nil {
		return result, err
	}
	result.Delivery, err = s.store.GetProjectGitLabDelivery(ctx, project)
	return result, err
}

func (s *Service) verifiedLegacyKustomizationPaths(ctx context.Context, client *gitLabClient, projectID int64, delivery ProjectDelivery) ([]string, error) {
	paths := make([]string, 0)
	for _, legacy := range legacyKustomizationFiles(delivery) {
		content, exists, err := client.fileContent(ctx, projectID, delivery.DefaultBranch, legacy.Path)
		if err != nil {
			return nil, err
		}
		if exists && bytes.Equal(content, []byte(legacy.Content)) {
			paths = append(paths, legacy.Path)
		}
	}
	return paths, nil
}

// SyncJobJenkinsfile writes the generated pipeline only for the selected Job.
// Deployment manifests remain owned by the project service catalog and are
// synchronized separately, so a Job may safely reference multiple services.
func (s *Service) SyncJobJenkinsfile(ctx context.Context, project string, job cicd.Job) (created, updated int, err error) {
	if job.JenkinsfileMode != "generated" {
		return 0, 0, nil
	}
	if job.JenkinsfileCredential == "" || job.ManifestCredential == "" {
		return 0, 0, fmt.Errorf("%w：平台生成 Jenkinsfile 需要选择流水线仓库凭据和部署清单仓库凭据", ErrInvalid)
	}
	delivery, _, client, err := s.deliveryClient(ctx, project)
	if err != nil {
		return 0, 0, err
	}
	if delivery.JenkinsfilesProjectID == 0 || delivery.JenkinsfilesCloneURL == "" || delivery.ManifestsCloneURL == "" {
		return 0, 0, fmt.Errorf("%w：请先在“项目接入”创建项目交付仓库", ErrConflict)
	}
	targetProjectID := delivery.JenkinsfilesProjectID
	targetKind := "jenkinsfiles"
	jobRepository := strings.TrimSuffix(strings.TrimSpace(job.JenkinsfileRepo), "/")
	if jobRepository == strings.TrimSuffix(strings.TrimSpace(delivery.ManifestsCloneURL), "/") &&
		jobRepository != strings.TrimSuffix(strings.TrimSpace(delivery.JenkinsfilesCloneURL), "/") {
		targetProjectID = delivery.ManifestsProjectID
		targetKind = "manifests"
	}
	if delivery.JenkinsfilesProjectID == delivery.ManifestsProjectID && delivery.JenkinsfilesProjectID != 0 {
		targetProjectID = delivery.JenkinsfilesProjectID
		targetKind = "delivery"
	}
	managed, err := validManagedMarker(ctx, client, targetProjectID, delivery.DefaultBranch, project, targetKind)
	if err != nil {
		return 0, 0, err
	}
	if !managed {
		return 0, 0, fmt.Errorf("%w：流水线仓库缺少平台管理标记，已拒绝写入", ErrConflict)
	}
	byKey := make(map[string]ServiceSpec, len(delivery.Services))
	for _, service := range delivery.Services {
		byKey[service.Key] = service
	}
	services := make([]ServiceSpec, 0, len(job.ServiceKeys))
	for _, key := range job.ServiceKeys {
		service, ok := byKey[key]
		if !ok {
			return 0, 0, fmt.Errorf("%w：Job 引用的服务 %s 尚未在“服务与清单”登记", ErrInvalid, key)
		}
		services = append(services, service)
	}
	if len(services) == 0 {
		return 0, 0, fmt.Errorf("%w：Job 至少需要关联一个服务", ErrInvalid)
	}
	if delivery.SourceServerKey != "" {
		sourceServer, sourceErr := s.store.GetGitLabServer(ctx, delivery.SourceServerKey)
		for index := range services {
			if sourceErr != nil {
				// Preserve legacy deliveries that predate source-server records. New
				// and edited deliveries are validated by SaveDelivery.
				services[index].SourceCredentialID = sourceCredentialExternalID(project)
				continue
			}
			rootGroup, ok := repositoryRootGroup(services[index].SourceRepository, sourceServer.BaseURL, serverRootGroups(sourceServer.Server))
			if !ok {
				return 0, 0, fmt.Errorf("%w：服务 %s 的源码仓库不属于业务 GitLab 的授权根组", ErrConflict, services[index].Key)
			}
			services[index].SourceCredentialID = sourceCredentialExternalIDForRoot(project, rootGroup, delivery.SourceRootGroup)
		}
	}
	manifestURL := strings.TrimSpace(job.ManifestRepo)
	manifestBranch := strings.TrimSpace(job.ManifestBranch)
	if manifestURL == "" {
		manifestURL = delivery.ManifestsCloneURL
	}
	if manifestBranch == "" {
		manifestBranch = delivery.DefaultBranch
	}
	files := generateJobFiles(job, services, manifestURL, manifestBranch)
	legacyPaths := []string{path.Join(path.Dir(job.JenkinsfilePath), "job.yaml")}
	oldJobPath := "jobs/" + job.Key + "/job.yaml"
	if legacyPaths[0] != oldJobPath {
		legacyPaths = append(legacyPaths, oldJobPath)
	}
	return client.commitFilesWithDeletes(ctx, targetProjectID, delivery.DefaultBranch, fmt.Sprintf("ci: sync %s Jenkinsfile", job.Key), files, legacyPaths)
}

func (s *Service) registerRepositories(ctx context.Context, delivery ProjectDelivery) error {
	items := []cicd.Repository{
		{Key: "ops-delivery-jenkinsfiles", ProjectKey: delivery.ProjectKey, DisplayName: "项目部署流水线", Provider: "gitlab", Purpose: "jenkinsfile", CloneURL: delivery.JenkinsfilesCloneURL, DefaultBranch: delivery.DefaultBranch, DefaultPath: "environments", Description: "按环境保存 Jenkinsfile、services.groovy 和平台托管 Dockerfile"},
		{Key: "ops-delivery-manifests", ProjectKey: delivery.ProjectKey, DisplayName: "项目部署清单", Provider: "gitlab", Purpose: "manifest", CloneURL: delivery.ManifestsCloneURL, DefaultBranch: delivery.DefaultBranch, DefaultPath: "environments", Description: "由 AWS 部署平台创建，仅属于当前项目"},
	}
	if unifiedDelivery(delivery) {
		items = append([]cicd.Repository{{
			Key: "ops-delivery", ProjectKey: delivery.ProjectKey, DisplayName: "项目运维交付仓库", Provider: "gitlab", Purpose: "general",
			CloneURL: delivery.JenkinsfilesCloneURL, DefaultBranch: delivery.DefaultBranch, DefaultPath: "environments",
			Description: "同一仓库按环境路径管理 Jenkinsfile、部署清单和平台托管 Dockerfile",
		}}, items...)
	}
	for _, item := range items {
		if existing, err := s.store.GetCICDRepository(ctx, delivery.ProjectKey, item.Key); err == nil {
			if existing.CloneURL != "" && existing.CloneURL != item.CloneURL {
				return fmt.Errorf("%w：保留的仓库标识 %s 已被其他地址占用", ErrConflict, item.Key)
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		if err := s.store.SaveCICDRepository(ctx, item); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) registerSourceRepositories(ctx context.Context, delivery ProjectDelivery) error {
	for _, service := range delivery.Services {
		item := cicd.Repository{
			Key: sourceRepositoryKey(service.Key), ProjectKey: delivery.ProjectKey,
			DisplayName: service.DisplayName + " 业务源码", Provider: "gitlab", Purpose: "source",
			CloneURL: service.SourceRepository, DefaultBranch: service.SourceBranch,
			Description: "来自项目绑定的业务源码 GitLab，只允许当前项目流水线读取",
		}
		if existing, err := s.store.GetCICDRepository(ctx, delivery.ProjectKey, item.Key); err == nil {
			if existing.Purpose != "source" {
				return fmt.Errorf("%w：仓库标识 %s 已被非源码仓库占用", ErrConflict, item.Key)
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		if err := s.store.SaveCICDRepository(ctx, item); err != nil {
			return err
		}
	}
	return nil
}

func sourceRepositoryKey(serviceKey string) string {
	key := "source-" + strings.ToLower(strings.TrimSpace(serviceKey))
	if len(key) > 63 {
		key = key[:63]
	}
	return key
}

func (s *Service) repositoryPlans(ctx context.Context, client *gitLabClient, delivery ProjectDelivery, files []GeneratedFile) ([]RepositoryPlan, error) {
	targets := []RepositoryPlan{
		{Kind: "jenkinsfiles", Name: delivery.ProjectKey + " Jenkinsfiles", ProjectPath: delivery.JenkinsfilesProjectPath},
		{Kind: "manifests", Name: delivery.ProjectKey + " Deploy Manifests", ProjectPath: delivery.ManifestsProjectPath},
	}
	if unifiedDelivery(delivery) {
		targets = []RepositoryPlan{{Kind: "delivery", Name: delivery.ProjectKey + " Ops Delivery", ProjectPath: delivery.JenkinsfilesProjectPath}}
	}
	for index := range targets {
		project, exists, err := client.project(ctx, targets[index].ProjectPath)
		if err != nil {
			return nil, err
		}
		targets[index].Exists, targets[index].CloneURL = exists, project.HTTPURLToRepo
		if exists {
			targets[index].Managed, err = validManagedMarker(ctx, client, project.ID, delivery.DefaultBranch, delivery.ProjectKey, targets[index].Kind)
			if err != nil {
				return nil, err
			}
		}
	}
	return targets, nil
}

func validManagedMarker(ctx context.Context, client *gitLabClient, projectID int64, branch, project, kind string) (bool, error) {
	content, exists, err := client.fileContent(ctx, projectID, branch, ".ops-deploy/managed.json")
	if err != nil || !exists {
		return false, err
	}
	var marker struct {
		ManagedBy      string `json:"managed_by"`
		Project        string `json:"project"`
		RepositoryKind string `json:"repository_kind"`
	}
	if err := json.Unmarshal(content, &marker); err != nil {
		return false, nil
	}
	expectedKind := kind
	if kind == "manifests" {
		expectedKind = "deploy-manifests"
	}
	return marker.ManagedBy == "ops-deploy-platform" && marker.Project == project && marker.RepositoryKind == expectedKind, nil
}

func (s *Service) deliveryClient(ctx context.Context, project string) (ProjectDelivery, StoredServer, *gitLabClient, error) {
	delivery, err := s.store.GetProjectGitLabDelivery(ctx, strings.TrimSpace(project))
	if err != nil {
		return delivery, StoredServer{}, nil, fmt.Errorf("%w：请先保存项目交付配置", ErrInvalid)
	}
	server, client, err := s.client(ctx, delivery.ServerKey)
	return delivery, server, client, err
}

// CheckServerAlias verifies that an alternate public hostname reaches the
// same GitLab repositories using the already encrypted server token. It does
// not persist the alias or expose the token.
func (s *Service) CheckServerAlias(ctx context.Context, project, rawBaseURL string) (ServerAliasCheck, error) {
	delivery, _, current, err := s.deliveryClient(ctx, project)
	if err != nil {
		return ServerAliasCheck{}, err
	}
	result := ServerAliasCheck{BaseURL: strings.TrimSuffix(strings.TrimSpace(rawBaseURL), "/")}
	parsed, err := url.Parse(result.BaseURL)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return result, fmt.Errorf("%w：GitLab 别名必须是 HTTPS 根地址", ErrInvalid)
	}
	alias, err := newGitLabClient(result.BaseURL, current.token)
	if err != nil {
		return result, err
	}
	defer func() { current.token, alias.token = "", "" }()
	if err := alias.currentUser(ctx); err != nil {
		return result, err
	}
	result.Authenticated = true
	jenkinsfiles, exists, err := alias.project(ctx, delivery.JenkinsfilesProjectPath)
	if err != nil || !exists {
		if err == nil {
			err = fmt.Errorf("%w：别名下找不到 %s", ErrNotFound, delivery.JenkinsfilesProjectPath)
		}
		return result, err
	}
	manifests, exists, err := alias.project(ctx, delivery.ManifestsProjectPath)
	if err != nil || !exists {
		if err == nil {
			err = fmt.Errorf("%w：别名下找不到 %s", ErrNotFound, delivery.ManifestsProjectPath)
		}
		return result, err
	}
	result.JenkinsfilesProjectID, result.JenkinsfilesCloneURL = jenkinsfiles.ID, jenkinsfiles.HTTPURLToRepo
	result.ManifestsProjectID, result.ManifestsCloneURL = manifests.ID, manifests.HTTPURLToRepo
	result.MatchesCurrentProjects = jenkinsfiles.ID == delivery.JenkinsfilesProjectID && manifests.ID == delivery.ManifestsProjectID
	return result, nil
}

// DeliveryRepositoryEndpoints returns clone endpoints reported by GitLab for
// the two project-owned delivery repositories. It contains no credentials.
func (s *Service) DeliveryRepositoryEndpoints(ctx context.Context, project string) ([]RepositoryEndpoints, error) {
	delivery, _, client, err := s.deliveryClient(ctx, project)
	if err != nil {
		return nil, err
	}
	defer func() { client.token = "" }()
	paths := []string{delivery.JenkinsfilesProjectPath, delivery.ManifestsProjectPath}
	result := make([]RepositoryEndpoints, 0, len(paths))
	for _, path := range paths {
		repository, exists, requestErr := client.project(ctx, path)
		if requestErr != nil {
			return nil, requestErr
		}
		if !exists {
			return nil, fmt.Errorf("%w：%s", ErrNotFound, path)
		}
		result = append(result, RepositoryEndpoints{ProjectID: repository.ID, ProjectPath: repository.PathWithNamespace, HTTPS: repository.HTTPURLToRepo, SSH: repository.SSHURLToRepo})
	}
	return result, nil
}

// RelayCloneTarget resolves only repositories already owned or registered by
// the selected project. It never accepts an arbitrary upstream URL.
func (s *Service) RelayCloneTarget(ctx context.Context, project, kind, serviceKey string) (string, error) {
	delivery, server, _, err := s.deliveryClient(ctx, project)
	if err != nil {
		return "", err
	}
	var cloneURL string
	switch strings.ToLower(strings.TrimSpace(kind)) {
	case "jenkinsfiles":
		cloneURL = delivery.JenkinsfilesCloneURL
	case "manifests":
		cloneURL = delivery.ManifestsCloneURL
	case "source":
		serviceKey = strings.ToLower(strings.TrimSpace(serviceKey))
		if !keyPattern.MatchString(serviceKey) {
			return "", ErrInvalid
		}
		repository, getErr := s.store.GetCICDRepository(ctx, strings.TrimSpace(project), "source-"+serviceKey)
		if getErr != nil {
			return "", getErr
		}
		if repository.Provider != "gitlab" || repository.Purpose != "source" {
			return "", ErrConflict
		}
		cloneURL = repository.CloneURL
	default:
		return "", ErrInvalid
	}
	clone, err := url.Parse(strings.TrimSpace(cloneURL))
	if err != nil || clone.Scheme != "https" || clone.Host == "" || clone.User != nil || clone.RawQuery != "" || clone.Fragment != "" {
		return "", ErrInvalid
	}
	base, err := url.Parse(strings.TrimSpace(server.BaseURL))
	if err != nil || !strings.EqualFold(clone.Scheme, base.Scheme) || !strings.EqualFold(clone.Host, base.Host) {
		return "", ErrConflict
	}
	return strings.TrimSuffix(clone.String(), "/"), nil
}

func (s *Service) client(ctx context.Context, key string) (StoredServer, *gitLabClient, error) {
	record, err := s.store.GetGitLabServer(ctx, strings.ToLower(strings.TrimSpace(key)))
	if err != nil {
		return record, nil, err
	}
	plain, err := s.decrypt(serverAAD(record.Key), record.TokenCipher)
	if err != nil {
		return record, nil, err
	}
	defer clear(plain)
	client, err := newGitLabClient(record.BaseURL, string(plain))
	return record, client, err
}

func normalizeServices(input []ServiceSpec, project string) ([]ServiceSpec, error) {
	if len(input) > 50 {
		return nil, fmt.Errorf("%w：单个项目最多配置 50 个服务", ErrInvalid)
	}
	seen := map[string]bool{}
	result := make([]ServiceSpec, 0, len(input))
	for _, item := range input {
		item.Key = strings.ToLower(strings.TrimSpace(item.Key))
		if !keyPattern.MatchString(item.Key) || seen[item.Key] {
			return nil, fmt.Errorf("%w：服务标识 %q 不正确或重复", ErrInvalid, item.Key)
		}
		seen[item.Key] = true
		item.DisplayName = limit(strings.TrimSpace(item.DisplayName), 128)
		if item.DisplayName == "" {
			item.DisplayName = item.Key
		}
		item.WorkloadType = strings.ToLower(strings.TrimSpace(item.WorkloadType))
		if item.WorkloadType == "" {
			item.WorkloadType = "backend"
		}
		if item.WorkloadType != "backend" && item.WorkloadType != "frontend" {
			return nil, fmt.Errorf("%w：服务 %s 部署类型只支持后端或前端", ErrInvalid, item.Key)
		}
		item.Language = strings.ToLower(strings.TrimSpace(item.Language))
		if item.Language != "go" && item.Language != "java" && item.Language != "node" {
			return nil, fmt.Errorf("%w：服务 %s 只支持 Go、Java 或 Node.js", ErrInvalid, item.Key)
		}
		item.RuntimeVersion = strings.TrimSpace(item.RuntimeVersion)
		if item.RuntimeVersion == "" {
			if item.Language == "go" {
				item.RuntimeVersion = "1.24"
			} else if item.Language == "java" {
				item.RuntimeVersion = "21"
			} else {
				item.RuntimeVersion = "20"
			}
		}
		if len(item.RuntimeVersion) > 32 || !regexp.MustCompile(`^[A-Za-z0-9_.-]+$`).MatchString(item.RuntimeVersion) {
			return nil, fmt.Errorf("%w：服务 %s 运行时版本不正确", ErrInvalid, item.Key)
		}
		if err := validateCloneURL(item.SourceRepository); err != nil {
			return nil, fmt.Errorf("%w：服务 %s 源码仓库：%v", ErrInvalid, item.Key, err)
		}
		item.SourceBranch = strings.TrimSpace(item.SourceBranch)
		if item.SourceBranch == "" {
			item.SourceBranch = "main"
		}
		for _, credential := range []string{item.SourceCredentialID, item.ManifestCredentialID} {
			if credential != "" && !credentialPattern.MatchString(credential) {
				return nil, fmt.Errorf("%w：服务 %s 的 Jenkins Credential ID 不正确", ErrInvalid, item.Key)
			}
		}
		item.BuildContext = cleanRelativePath(item.BuildContext, ".")
		item.DockerfileSource = strings.ToLower(strings.TrimSpace(item.DockerfileSource))
		if item.DockerfileSource == "" {
			item.DockerfileSource = "platform"
		}
		if item.DockerfileSource != "platform" && item.DockerfileSource != "source" {
			return nil, fmt.Errorf("%w：服务 %s 的 Dockerfile 来源只支持 platform 或 source", ErrInvalid, item.Key)
		}
		if item.DockerfileSource == "source" {
			item.Dockerfile = cleanRelativePath(item.Dockerfile, "Dockerfile")
		} else {
			// The concrete path is selected from the Job environment when the
			// pipeline is generated. Keep this legacy value only as a harmless
			// catalog fallback for older clients.
			item.Dockerfile = "dockerfiles/" + item.Key + "/Dockerfile"
		}
		if item.BuildContext == "" || item.Dockerfile == "" {
			return nil, fmt.Errorf("%w：服务 %s 构建路径不正确", ErrInvalid, item.Key)
		}
		item.DockerfileContent = strings.TrimSpace(item.DockerfileContent)
		if item.DockerfileSource == "source" {
			// The business repository is the only Dockerfile source in this mode.
			item.DockerfileContent = ""
			item.DockerfileContents = nil
		} else {
			if item.DockerfileContent == "" {
				item.DockerfileContent = defaultDockerfile(item)
			}
			if len(item.DockerfileContent) > 64*1024 || strings.IndexByte(item.DockerfileContent, 0) >= 0 || !regexp.MustCompile(`(?mi)^\s*FROM\s+`).MatchString(item.DockerfileContent) {
				return nil, fmt.Errorf("%w：服务 %s 的 Dockerfile 内容为空、过大或缺少 FROM 指令", ErrInvalid, item.Key)
			}
			item.DockerfileContent = strings.TrimSpace(item.DockerfileContent) + "\n"
			contents := make(map[string]string, len(item.DockerfileContents))
			for environment, content := range item.DockerfileContents {
				environment = strings.ToLower(strings.TrimSpace(environment))
				content = strings.TrimSpace(content)
				if !validDeliveryEnvironment(environment) {
					return nil, fmt.Errorf("%w：服务 %s 的 Dockerfile 环境 %s 不正确", ErrInvalid, item.Key, environment)
				}
				if len(content) > 64*1024 || strings.IndexByte(content, 0) >= 0 || !regexp.MustCompile(`(?mi)^\s*FROM\s+`).MatchString(content) {
					return nil, fmt.Errorf("%w：服务 %s 的 %s Dockerfile 内容为空、过大或缺少 FROM 指令", ErrInvalid, item.Key, environment)
				}
				contents[environment] = content + "\n"
			}
			item.DockerfileContents = contents
		}
		item.ManifestMode = strings.ToLower(strings.TrimSpace(item.ManifestMode))
		if item.ManifestMode == "" {
			item.ManifestMode = "platform"
		}
		if item.ManifestMode != "platform" && item.ManifestMode != "repository" {
			return nil, fmt.Errorf("%w：服务 %s 的部署清单来源只支持 platform 或 repository", ErrInvalid, item.Key)
		}
		// Jenkins 只负责源码检出、镜像构建与 Kubernetes 部署。
		// 依赖安装、编译和打包统一由 Dockerfile 完成。
		item.BuildCommand = ""
		if len(item.BuildCommand) > 1000 || strings.ContainsAny(item.BuildCommand, "\x00\r\n") {
			return nil, fmt.Errorf("%w：服务 %s 构建命令不正确", ErrInvalid, item.Key)
		}
		item.DockerTarget = strings.TrimSpace(item.DockerTarget)
		if item.DockerTarget != "" && !dockerTargetPattern.MatchString(item.DockerTarget) {
			return nil, fmt.Errorf("%w：服务 %s Docker target 不正确", ErrInvalid, item.Key)
		}
		item.RunEnvironment = strings.TrimSpace(item.RunEnvironment)
		if item.RunEnvironment != "" && !dockerTargetPattern.MatchString(item.RunEnvironment) {
			return nil, fmt.Errorf("%w：服务 %s RUN_ENV 不正确", ErrInvalid, item.Key)
		}
		item.ImageRepository = strings.TrimSpace(item.ImageRepository)
		if item.ImageRepository == "" {
			item.ImageRepository = project + "/" + item.Key
		}
		if !imageRepositoryPattern.MatchString(item.ImageRepository) {
			return nil, fmt.Errorf("%w：服务 %s 镜像仓库不正确", ErrInvalid, item.Key)
		}
		pullSecrets := make([]string, 0, len(item.ImagePullSecrets))
		pullSecretSeen := map[string]bool{}
		for _, secret := range item.ImagePullSecrets {
			secret = strings.ToLower(strings.TrimSpace(secret))
			if !keyPattern.MatchString(secret) {
				return nil, fmt.Errorf("%w：服务 %s 的镜像拉取 Secret 名称不正确", ErrInvalid, item.Key)
			}
			if !pullSecretSeen[secret] {
				pullSecrets = append(pullSecrets, secret)
				pullSecretSeen[secret] = true
			}
			if len(pullSecrets) > 10 {
				return nil, fmt.Errorf("%w：服务 %s 最多配置 10 个镜像拉取 Secret", ErrInvalid, item.Key)
			}
		}
		item.ImagePullSecrets = pullSecrets
		item.ImagePullPolicy = defaultValue(item.ImagePullPolicy, "Always")
		if item.ImagePullPolicy != "Always" && item.ImagePullPolicy != "IfNotPresent" && item.ImagePullPolicy != "Never" {
			return nil, fmt.Errorf("%w：服务 %s 镜像拉取策略不正确", ErrInvalid, item.Key)
		}
		item.Namespace = strings.ToLower(strings.TrimSpace(item.Namespace))
		if item.Namespace == "" {
			item.Namespace = project
		}
		if !keyPattern.MatchString(item.Namespace) {
			return nil, fmt.Errorf("%w：服务 %s Namespace 不正确", ErrInvalid, item.Key)
		}
		item.WorkloadClass = strings.ToLower(strings.TrimSpace(item.WorkloadClass))
		if item.WorkloadClass != "" && item.WorkloadClass != "application" && item.WorkloadClass != "platform" && item.WorkloadClass != "stateful" && item.WorkloadClass != "general" {
			return nil, fmt.Errorf("%w：服务 %s 的节点调度用途不正确", ErrInvalid, item.Key)
		}
		if item.ContainerPort == 0 {
			item.ContainerPort = 8080
		}
		if item.ContainerPort < 1 || item.ContainerPort > 65535 {
			return nil, fmt.Errorf("%w：服务 %s 端口不正确", ErrInvalid, item.Key)
		}
		if item.Replicas == 0 {
			item.Replicas = 1
		}
		if item.Replicas < 1 || item.Replicas > 50 {
			return nil, fmt.Errorf("%w：服务 %s 副本数不正确", ErrInvalid, item.Key)
		}
		if item.RevisionHistoryLimit == 0 {
			item.RevisionHistoryLimit = 5
		}
		if item.RevisionHistoryLimit < 1 || item.RevisionHistoryLimit > 20 {
			return nil, fmt.Errorf("%w：服务 %s 历史版本保留数不正确", ErrInvalid, item.Key)
		}
		item.Timezone = defaultValue(item.Timezone, "Asia/Shanghai")
		if len(item.Timezone) > 64 || strings.ContainsAny(item.Timezone, "\x00\r\n\t") {
			return nil, fmt.Errorf("%w：服务 %s 时区不正确", ErrInvalid, item.Key)
		}
		if item.Language != "java" {
			if len(item.JavaOptions) > 0 {
				return nil, fmt.Errorf("%w：服务 %s 只有 Java 服务可以配置 JVM 启动参数", ErrInvalid, item.Key)
			}
		} else {
			if item.WorkloadType != "backend" && len(item.JavaOptions) > 0 {
				return nil, fmt.Errorf("%w：服务 %s 只有 Java 后端服务可以配置 JVM 启动参数", ErrInvalid, item.Key)
			}
			// Older platform versions allowed the managed Java launcher suffix to be
			// stored together with JVM options. Strip that suffix on every save so a
			// later manifest render cannot append a second "-jar app.jar" pair.
			item.JavaOptions = normalizeJavaJVMOptions(item.JavaOptions)
			if len(item.JavaOptions) > 32 {
				return nil, fmt.Errorf("%w：服务 %s 最多配置 32 个 JVM 启动参数", ErrInvalid, item.Key)
			}
			javaOptions := make([]string, 0, len(item.JavaOptions))
			for _, option := range item.JavaOptions {
				option = strings.TrimSpace(option)
				if option == "" {
					continue
				}
				lowerOption := strings.ToLower(option)
				if len(option) > 512 || strings.ContainsAny(option, "\x00\r\n\t") || strings.Contains(option, ": ") || strings.Contains(option, " #") || strings.HasSuffix(option, ":") || !strings.HasPrefix(option, "-") || lowerOption == "-jar" || lowerOption == "-cp" || lowerOption == "-classpath" {
					return nil, fmt.Errorf("%w：服务 %s JVM 启动参数 %q 不正确；-jar 和 classpath 由平台管理", ErrInvalid, item.Key, option)
				}
				if sensitiveJavaOptionPattern.MatchString(option) {
					return nil, fmt.Errorf("%w：服务 %s JVM 启动参数 %q 疑似包含敏感信息；请改用 Kubernetes Secret 环境变量引用", ErrInvalid, item.Key, option)
				}
				javaOptions = append(javaOptions, option)
			}
			item.JavaOptions = javaOptions
		}
		if len(item.EnvironmentVariables) > 50 {
			return nil, fmt.Errorf("%w：服务 %s 最多配置 50 个非敏感环境变量", ErrInvalid, item.Key)
		}
		environmentVariables := make(map[string]string, len(item.EnvironmentVariables))
		for key, value := range item.EnvironmentVariables {
			key = strings.TrimSpace(key)
			if !environmentKeyPattern.MatchString(key) || sensitiveEnvironmentKeyPattern.MatchString(key) || len(value) > 4096 || strings.IndexByte(value, 0) >= 0 {
				return nil, fmt.Errorf("%w：服务 %s 环境变量 %q 不正确", ErrInvalid, item.Key, key)
			}
			environmentVariables[key] = value
		}
		item.EnvironmentVariables = environmentVariables
		if len(item.SecretEnvironmentVariables) > 50 {
			return nil, fmt.Errorf("%w：服务 %s 最多配置 50 个 Secret 环境变量引用", ErrInvalid, item.Key)
		}
		secretEnvironmentVariables := make(map[string]SecretKeyReference, len(item.SecretEnvironmentVariables))
		for key, reference := range item.SecretEnvironmentVariables {
			key = strings.TrimSpace(key)
			reference.SecretName = strings.ToLower(strings.TrimSpace(reference.SecretName))
			reference.SecretKey = strings.TrimSpace(reference.SecretKey)
			if !environmentKeyPattern.MatchString(key) || !keyPattern.MatchString(reference.SecretName) || !secretDataKeyPattern.MatchString(reference.SecretKey) {
				return nil, fmt.Errorf("%w：服务 %s Secret 环境变量 %q 不正确", ErrInvalid, item.Key, key)
			}
			if _, exists := environmentVariables[key]; exists {
				return nil, fmt.Errorf("%w：服务 %s 环境变量 %q 同时配置了明文值和 Secret 引用", ErrInvalid, item.Key, key)
			}
			secretEnvironmentVariables[key] = reference
		}
		item.SecretEnvironmentVariables = secretEnvironmentVariables
		if item.WorkloadType == "frontend" {
			item.CPURequest = defaultValue(item.CPURequest, "50m")
			item.MemoryRequest = defaultValue(item.MemoryRequest, "64Mi")
			item.CPULimit = defaultValue(item.CPULimit, "500m")
			item.MemoryLimit = defaultValue(item.MemoryLimit, "256Mi")
		} else {
			item.CPURequest = defaultValue(item.CPURequest, "100m")
			item.MemoryRequest = defaultValue(item.MemoryRequest, "128Mi")
			item.CPULimit = defaultValue(item.CPULimit, "1")
			item.MemoryLimit = defaultValue(item.MemoryLimit, "512Mi")
		}
		if !resourcePattern.MatchString(item.CPURequest) || !resourcePattern.MatchString(item.CPULimit) || !memoryPattern.MatchString(item.MemoryRequest) || !memoryPattern.MatchString(item.MemoryLimit) {
			return nil, fmt.Errorf("%w：服务 %s 资源规格不正确", ErrInvalid, item.Key)
		}
		item.HealthPath = strings.TrimSpace(item.HealthPath)
		if item.WorkloadType == "frontend" && item.HealthPath == "" {
			item.HealthPath = "/healthz"
		}
		if item.HealthPath != "" && (len(item.HealthPath) > 255 || !healthPathPattern.MatchString(item.HealthPath)) {
			return nil, fmt.Errorf("%w：服务 %s 健康检查路径不正确", ErrInvalid, item.Key)
		}
		if item.EtcdConfigEnabled {
			if item.WorkloadType != "backend" {
				return nil, fmt.Errorf("%w：服务 %s 只有后端服务可以生成 etcd 配置", ErrInvalid, item.Key)
			}
			if len(item.EtcdHosts) == 0 {
				item.EtcdHosts = []string{"etcd." + item.Namespace + ".svc.cluster.local:2379"}
			}
			hosts := make([]string, 0, len(item.EtcdHosts))
			seenHosts := map[string]bool{}
			for _, host := range item.EtcdHosts {
				host = strings.TrimSpace(host)
				if !etcdHostPattern.MatchString(host) {
					return nil, fmt.Errorf("%w：服务 %s etcd 地址 %q 不正确", ErrInvalid, item.Key, host)
				}
				port, _ := strconv.Atoi(host[strings.LastIndex(host, ":")+1:])
				if port < 1 || port > 65535 {
					return nil, fmt.Errorf("%w：服务 %s etcd 地址 %q 端口不正确", ErrInvalid, item.Key, host)
				}
				if !seenHosts[host] {
					hosts = append(hosts, host)
					seenHosts[host] = true
				}
				if len(hosts) > 10 {
					return nil, fmt.Errorf("%w：服务 %s 最多配置 10 个 etcd 地址", ErrInvalid, item.Key)
				}
			}
			item.EtcdHosts = hosts
			item.EtcdConfigKey = defaultValue(item.EtcdConfigKey, "/config-center/"+item.Key)
			if !strings.HasPrefix(item.EtcdConfigKey, "/") || len(item.EtcdConfigKey) > 500 || strings.ContainsAny(item.EtcdConfigKey, "\x00\r\n\t") {
				return nil, fmt.Errorf("%w：服务 %s etcd Key 不正确", ErrInvalid, item.Key)
			}
			item.EtcdUsername = defaultValue(item.EtcdUsername, "admin")
			if len(item.EtcdUsername) > 128 || strings.ContainsAny(item.EtcdUsername, "\x00\r\n\t") {
				return nil, fmt.Errorf("%w：服务 %s etcd 用户名不正确", ErrInvalid, item.Key)
			}
			item.EtcdPasswordCredentialID = strings.TrimSpace(item.EtcdPasswordCredentialID)
			if !credentialPattern.MatchString(item.EtcdPasswordCredentialID) {
				return nil, fmt.Errorf("%w：服务 %s 需要有效的 etcd 密码 Jenkins Secret Text Credential ID", ErrInvalid, item.Key)
			}
			item.EtcdConfigFile = defaultValue(item.EtcdConfigFile, "config.yaml")
			if !configFilePattern.MatchString(item.EtcdConfigFile) {
				return nil, fmt.Errorf("%w：服务 %s etcd 配置文件名不正确", ErrInvalid, item.Key)
			}
			item.EtcdMountPath = defaultValue(item.EtcdMountPath, "/app/apps/api/etc/"+item.EtcdConfigFile)
			if !strings.HasPrefix(item.EtcdMountPath, "/") || len(item.EtcdMountPath) > 500 || strings.Contains(item.EtcdMountPath, "..") || strings.ContainsAny(item.EtcdMountPath, "\x00\r\n\t") {
				return nil, fmt.Errorf("%w：服务 %s etcd 配置挂载路径不正确", ErrInvalid, item.Key)
			}
		} else {
			item.EtcdHosts = nil
			item.EtcdConfigKey, item.EtcdUsername, item.EtcdPasswordCredentialID, item.EtcdConfigFile, item.EtcdMountPath = "", "", "", "", ""
		}
		// Runtime configuration belongs to the image. Historical Nginx
		// ConfigMap content is deliberately discarded so repository syncs
		// cannot reintroduce a manifest-side configuration override.
		item.NginxServerConfig = ""
		result = append(result, item)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Key < result[j].Key })
	return result, nil
}

func validateBaseURL(raw string, allowHTTP bool) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", errors.New("GitLab 地址必须是不含用户信息、查询参数和片段的完整 URL")
	}
	if parsed.Scheme != "https" && !(parsed.Scheme == "http" && allowHTTP) {
		return "", errors.New("默认必须使用 HTTPS；只有明确确认内网风险后才可使用 HTTP")
	}
	return strings.TrimSuffix(parsed.String(), "/"), nil
}

func validateCloneURL(raw string) error {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Host == "" || parsed.User != nil || (parsed.Scheme != "https" && parsed.Scheme != "http") || parsed.RawQuery != "" || parsed.Fragment != "" {
		return errors.New("请填写不含账号密码的 HTTP(S) 仓库地址")
	}
	return nil
}

func cleanRelativePath(value, fallback string) string {
	value = strings.Trim(strings.TrimSpace(value), "/")
	if value == "" {
		value = fallback
	}
	if strings.Contains(value, "..") || len(value) > 500 || !relativePathPattern.MatchString(value) {
		return ""
	}
	return value
}

func (s *Service) encrypt(aad string, plain []byte) (string, error) {
	nonce := make([]byte, s.aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", err
	}
	sealed := s.aead.Seal(nil, nonce, plain, []byte(aad))
	payload := append(nonce, sealed...)
	return base64.RawStdEncoding.EncodeToString(payload), nil
}

func (s *Service) decrypt(aad, encoded string) ([]byte, error) {
	payload, err := base64.RawStdEncoding.DecodeString(encoded)
	if err != nil || len(payload) < s.aead.NonceSize() {
		return nil, errors.New("decrypt GitLab token: invalid ciphertext")
	}
	plain, err := s.aead.Open(nil, payload[:s.aead.NonceSize()], payload[s.aead.NonceSize():], []byte(aad))
	if err != nil {
		return nil, fmt.Errorf("decrypt GitLab token: %w", err)
	}
	return plain, nil
}

func serverAAD(key string) string { return "gitlab-server:" + key }
func limit(value string, length int) string {
	value = strings.TrimSpace(value)
	if len(value) > length {
		return value[:length]
	}
	return value
}
func defaultValue(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return strings.TrimSpace(value)
}

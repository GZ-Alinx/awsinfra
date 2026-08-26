package gitlab

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"ops-deploy-platform/internal/cicd"
)

var sourcePathPattern = regexp.MustCompile(`^[A-Za-z0-9._-]+(?:/[A-Za-z0-9._-]+)*$`)

type jenkinsCredentialSyncer interface {
	ListCredentials(context.Context, string) ([]cicd.Credential, error)
	SaveCredential(context.Context, string, string, cicd.CredentialInput) (cicd.Credential, error)
	SyncCredential(context.Context, string, string) (cicd.Credential, error)
	ValidateGitCredential(context.Context, string, string, string) (cicd.GitCredentialValidation, error)
}

func (s *Service) ListSourceRepositories(ctx context.Context, project, search string) ([]SourceRepositoryOption, error) {
	_, server, client, err := s.sourceClient(ctx, project)
	if err != nil {
		return nil, err
	}
	defer func() { client.token = "" }()
	result := []SourceRepositoryOption{}
	seen := map[int64]int{}
	for _, rootGroup := range serverRootGroups(server.Server) {
		group, groupErr := client.group(ctx, rootGroup)
		if groupErr != nil {
			return nil, fmt.Errorf("读取授权根组 %s 失败：%w", rootGroup, groupErr)
		}
		projects, projectErr := client.groupProjects(ctx, group.ID, search)
		if projectErr != nil {
			return nil, fmt.Errorf("读取授权根组 %s 的仓库失败：%w", rootGroup, projectErr)
		}
		for _, item := range projects {
			if item.HTTPURLToRepo == "" {
				continue
			}
			branch := strings.TrimSpace(item.DefaultBranch)
			if branch == "" {
				branch = server.DefaultBranch
			}
			if index, exists := seen[item.ID]; exists {
				if len(rootGroup) > len(result[index].RootGroup) {
					result[index].RootGroup = rootGroup
				}
				continue
			}
			seen[item.ID] = len(result)
			result = append(result, SourceRepositoryOption{ProjectID: item.ID, Name: item.Name, Path: item.PathWithNamespace, RootGroup: rootGroup, CloneURL: item.HTTPURLToRepo, DefaultBranch: branch, SourceServerKey: server.Key})
		}
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].RootGroup != result[j].RootGroup {
			return result[i].RootGroup < result[j].RootGroup
		}
		return result[i].Path < result[j].Path
	})
	return result, nil
}

func (s *Service) ListSourceRepositoryBranches(ctx context.Context, project string, projectID int64) ([]SourceBranchOption, error) {
	if projectID <= 0 {
		return nil, fmt.Errorf("%w：业务源码仓库 ID 不正确", ErrInvalid)
	}
	_, server, client, err := s.sourceClient(ctx, project)
	if err != nil {
		return nil, err
	}
	defer func() { client.token = "" }()
	owned, err := authorizedSourceProject(ctx, client, server.Server, projectID)
	if err != nil {
		return nil, err
	}
	if !owned {
		return nil, fmt.Errorf("%w：业务源码仓库不属于当前 GitLab 的任一授权根组", ErrConflict)
	}
	branches, err := client.projectBranches(ctx, projectID)
	if err != nil {
		return nil, err
	}
	result := make([]SourceBranchOption, 0, len(branches))
	for _, item := range branches {
		if name := strings.TrimSpace(item.Name); name != "" {
			result = append(result, SourceBranchOption{Name: name, Default: item.Default, Protected: item.Protected})
		}
	}
	sort.SliceStable(result, func(i, j int) bool {
		if result[i].Default != result[j].Default {
			return result[i].Default
		}
		return result[i].Name < result[j].Name
	})
	return result, nil
}

func (s *Service) CheckSourceRepositoryFile(ctx context.Context, project string, projectID int64, branch, path string) (SourceFileCheck, error) {
	result := SourceFileCheck{ProjectID: projectID, Branch: strings.TrimSpace(branch)}
	if projectID <= 0 {
		return result, fmt.Errorf("%w：业务源码仓库 ID 不正确", ErrInvalid)
	}
	if result.Branch == "" || len(result.Branch) > 255 || strings.ContainsAny(result.Branch, "\x00\r\n") {
		return result, fmt.Errorf("%w：业务源码分支不正确", ErrInvalid)
	}
	result.Path = cleanRelativePath(path, "Dockerfile")
	if result.Path == "" {
		return result, fmt.Errorf("%w：Dockerfile 路径不正确", ErrInvalid)
	}
	_, server, client, err := s.sourceClient(ctx, project)
	if err != nil {
		return result, err
	}
	defer func() { client.token = "" }()
	owned, err := authorizedSourceProject(ctx, client, server.Server, projectID)
	if err != nil {
		return result, err
	}
	if !owned {
		return result, fmt.Errorf("%w：业务源码仓库不属于当前 GitLab 的任一授权根组", ErrConflict)
	}
	result.Exists, err = client.fileExists(ctx, projectID, result.Branch, result.Path)
	return result, err
}

func authorizedSourceProject(ctx context.Context, client *gitLabClient, server Server, projectID int64) (bool, error) {
	for _, rootGroup := range serverRootGroups(server) {
		group, err := client.group(ctx, rootGroup)
		if err != nil {
			return false, fmt.Errorf("校验授权根组 %s 失败：%w", rootGroup, err)
		}
		projects, err := client.groupProjects(ctx, group.ID, "")
		if err != nil {
			return false, fmt.Errorf("校验授权根组 %s 仓库失败：%w", rootGroup, err)
		}
		for _, item := range projects {
			if item.ID == projectID {
				return true, nil
			}
		}
	}
	return false, nil
}

func (s *Service) ValidateSourceRepository(ctx context.Context, project, serviceKey string) error {
	_, server, client, err := s.sourceClient(ctx, project)
	if err != nil {
		return err
	}
	defer func() { client.token = "" }()
	repository, err := s.store.GetCICDRepository(ctx, strings.TrimSpace(project), sourceRepositoryKey(serviceKey))
	if err != nil {
		return err
	}
	if _, ok := repositoryRootGroup(repository.CloneURL, client.baseURL.String(), serverRootGroups(server.Server)); !ok {
		return fmt.Errorf("%w：服务 %s 的源码仓库不属于业务源码 GitLab 的任一授权根组", ErrConflict, serviceKey)
	}
	if !client.gitHTTPAccess(ctx, repository.CloneURL, "oauth2", client.token) {
		return fmt.Errorf("%w：业务源码 GitLab Token 无法读取服务 %s 的仓库，请检查 read_repository 权限", ErrRequest, serviceKey)
	}
	return nil
}

// SourceRelayTarget returns the project-registered source repository and an
// in-memory upstream token. Jenkins only sees the project-scoped relay
// credential; the business GitLab API token is never copied to Jenkins.
func (s *Service) SourceRelayTarget(ctx context.Context, project, serviceKey string) (string, string, []byte, error) {
	_, server, client, err := s.sourceClient(ctx, project)
	if err != nil {
		return "", "", nil, err
	}
	// SourceRelayTarget returns a separately decrypted byte slice below. Drop
	// the temporary API client copy before leaving this method.
	client.token = ""
	repository, err := s.store.GetCICDRepository(ctx, strings.TrimSpace(project), sourceRepositoryKey(serviceKey))
	if err != nil {
		return "", "", nil, err
	}
	if _, ok := repositoryRootGroup(repository.CloneURL, server.BaseURL, serverRootGroups(server.Server)); !ok {
		return "", "", nil, fmt.Errorf("%w：源码仓库不属于项目业务 GitLab 的任一授权根组", ErrConflict)
	}
	token, err := s.decrypt(serverAAD(server.Key), server.TokenCipher)
	if err != nil {
		return "", "", nil, err
	}
	return strings.TrimSuffix(repository.CloneURL, "/"), "oauth2", token, nil
}

func (s *Service) sourceClient(ctx context.Context, project string) (ProjectDelivery, StoredServer, *gitLabClient, error) {
	delivery, err := s.store.GetProjectGitLabDelivery(ctx, strings.TrimSpace(project))
	if err != nil {
		return ProjectDelivery{}, StoredServer{}, nil, err
	}
	if delivery.SourceServerKey == "" {
		return delivery, StoredServer{}, nil, fmt.Errorf("%w：请先在“项目接入”选择业务源码 GitLab", ErrInvalid)
	}
	server, client, err := s.client(ctx, delivery.SourceServerKey)
	return delivery, server, client, err
}

// SyncSourceCredential issues a project/connection-scoped read_repository
// deploy token and synchronizes it to the Jenkins used by the Job. When a
// GitLab gateway blocks deploy-token creation, a previously validated
// compatibility credential remains usable and is retried for least-privilege
// rotation on subsequent explicit synchronizations.
func (s *Service) SyncSourceCredential(ctx context.Context, project, connectionKey string, serviceKeys []string, cicdService jenkinsCredentialSyncer) (cicd.Credential, error) {
	if cicdService == nil {
		return cicd.Credential{}, fmt.Errorf("%w：CI/CD 服务不可用", ErrInvalid)
	}
	project, connectionKey = strings.ToLower(strings.TrimSpace(project)), strings.ToLower(strings.TrimSpace(connectionKey))
	delivery, server, client, err := s.sourceClient(ctx, project)
	if err != nil {
		return cicd.Credential{}, err
	}
	defer func() { client.token = "" }()
	services := make(map[string]ServiceSpec, len(delivery.Services))
	for _, service := range delivery.Services {
		services[service.Key] = service
	}
	selectedByRoot := map[string][]ServiceSpec{}
	for _, key := range serviceKeys {
		service, ok := services[strings.ToLower(strings.TrimSpace(key))]
		if !ok {
			return cicd.Credential{}, fmt.Errorf("%w：Job 引用的服务 %s 尚未登记", ErrInvalid, key)
		}
		rootGroup, matched := repositoryRootGroup(service.SourceRepository, server.BaseURL, serverRootGroups(server.Server))
		if !matched {
			return cicd.Credential{}, fmt.Errorf("%w：服务 %s 的源码仓库不属于业务 GitLab 的任一授权根组", ErrConflict, service.Key)
		}
		selectedByRoot[rootGroup] = append(selectedByRoot[rootGroup], service)
	}
	if len(selectedByRoot) == 0 {
		return cicd.Credential{}, fmt.Errorf("%w：Job 至少需要一个业务服务", ErrInvalid)
	}
	existing, _ := cicdService.ListCredentials(ctx, project)
	var first cicd.Credential
	for _, rootGroup := range serverRootGroups(server.Server) {
		selected := selectedByRoot[rootGroup]
		if len(selected) == 0 {
			continue
		}
		credential, syncErr := s.syncSourceRootCredential(ctx, project, connectionKey, rootGroup, delivery.SourceRootGroup, selected, existing, client, cicdService)
		if syncErr != nil {
			return cicd.Credential{}, syncErr
		}
		if first.Key == "" {
			first = credential
		}
	}
	if first.Key == "" {
		return cicd.Credential{}, fmt.Errorf("%w：没有可同步的业务源码根组", ErrInvalid)
	}
	return first, nil
}

func (s *Service) syncSourceRootCredential(ctx context.Context, project, connectionKey, rootGroup, primaryRoot string, selected []ServiceSpec, existing []cicd.Credential, client *gitLabClient, cicdService jenkinsCredentialSyncer) (cicd.Credential, error) {
	credentialKey := sourceCredentialKeyForRoot(connectionKey, rootGroup, primaryRoot)
	externalID := sourceCredentialExternalIDForRoot(project, rootGroup, primaryRoot)
	var existingCredential *cicd.Credential
	existingCompatibilityCredential := false
	for index := range existing {
		credential := &existing[index]
		if credential.Key != credentialKey {
			continue
		}
		description := strings.TrimSpace(credential.Description)
		compatibleDescription := strings.HasPrefix(description, "业务 GitLab Group Deploy Token") || strings.HasPrefix(description, "业务 GitLab 源码只读凭据")
		if credential.ConnectionKey != connectionKey || credential.ExternalID != externalID || credential.Kind != "gitlab_token" || !compatibleDescription {
			return cicd.Credential{}, fmt.Errorf("%w：Jenkins 业务源码凭据标识已被其他配置占用", ErrConflict)
		}
		existingCredential = credential
		existingCompatibilityCredential = strings.HasPrefix(description, "业务 GitLab 源码只读凭据")
		break
	}
	existingCredentialValid := false
	if existingCredential != nil {
		valid := true
		for _, service := range selected {
			inspection, validateErr := cicdService.ValidateGitCredential(ctx, project, existingCredential.Key, service.SourceRepository)
			if validateErr != nil || !inspection.SmartHTTP {
				valid = false
				break
			}
		}
		if valid {
			existingCredentialValid = true
			if !existingCompatibilityCredential {
				return cicdService.SyncCredential(ctx, project, existingCredential.Key)
			}
		}
	}
	group, err := client.group(ctx, rootGroup)
	if err != nil {
		return cicd.Credential{}, err
	}
	rootSuffix := shortRootDigest(rootGroup)
	tokenName := limit("ops-deploy-"+project+"-"+connectionKey+"-"+rootSuffix+"-source-read", 128)
	oldTokens, _ := client.listGroupDeployTokens(ctx, group.ID)
	deployToken, err := client.createGroupDeployToken(ctx, group.ID, tokenName)
	if err != nil {
		// Some GitLab installations protect deploy-token API paths with a WAF.
		// Keep a previously validated compatibility credential usable, and try
		// the least-privilege rotation again on the next explicit synchronization.
		if existingCredentialValid {
			return cicdService.SyncCredential(ctx, project, existingCredential.Key)
		}
		// A read_repository/read_api token can clone source repositories but is
		// intentionally unable to administer Group Deploy Tokens. GitLab reports
		// that case as insufficient_scope. After proving the configured token can
		// read every selected repository, synchronize it as an environment-scoped
		// compatibility credential instead of blocking the whole Jenkins Job.
		// Other 403 responses (role, root-group or WAF failures) are not bypassed.
		if gitLabInsufficientScope(err) {
			for _, service := range selected {
				if validateErr := validateRepositoryServer(service.SourceRepository, client.baseURL.String(), rootGroup); validateErr != nil ||
					!client.gitHTTPAccess(ctx, service.SourceRepository, "oauth2", client.token) {
					return cicd.Credential{}, fmt.Errorf("%w：GitLab Token 无权创建 Group Deploy Token，且不能只读拉取服务 %s 的源码仓库；请给 Token 增加 read_repository，或使用具有 Owner + api 权限的管理 Token", ErrRequest, service.Key)
				}
			}
			input := cicd.CredentialInput{
				ConnectionKey: connectionKey,
				DisplayName:   project + " 业务源码兼容凭据 · " + rootGroup,
				Kind:          "gitlab_token",
				ExternalID:    externalID,
				Description:   fmt.Sprintf("业务 GitLab 源码只读凭据（兼容模式，根组 %s；Token 已验证可读取所选仓库，但无 Group Deploy Token 管理权限）", rootGroup),
				Username:      "oauth2",
				Password:      client.token,
			}
			credential, saveErr := cicdService.SaveCredential(ctx, project, credentialKey, input)
			input.Password = ""
			if saveErr != nil {
				return cicd.Credential{}, saveErr
			}
			return cicdService.SyncCredential(ctx, project, credential.Key)
		}
		return cicd.Credential{}, fmt.Errorf("%w：创建业务源码只读 Group Deploy Token 失败，请确认 GitLab Token 具有 api 权限：%v", ErrRequest, err)
	}
	for _, service := range selected {
		if err := validateRepositoryServer(service.SourceRepository, client.baseURL.String(), rootGroup); err != nil || !client.gitHTTPAccess(ctx, service.SourceRepository, deployToken.Username, deployToken.Token) {
			_ = client.deleteGroupDeployToken(ctx, group.ID, deployToken.ID)
			deployToken.Token = ""
			return cicd.Credential{}, fmt.Errorf("%w：新建的项目只读凭据无法读取服务 %s 源码仓库", ErrRequest, service.Key)
		}
	}
	input := cicd.CredentialInput{
		ConnectionKey: connectionKey,
		DisplayName:   project + " 业务源码只读凭据 · " + rootGroup,
		Kind:          "gitlab_token",
		ExternalID:    externalID,
		Description:   fmt.Sprintf("业务 GitLab Group Deploy Token %d（根组 %s，仅 read_repository），由平台管理", deployToken.ID, rootGroup),
		Username:      deployToken.Username,
		Password:      deployToken.Token,
	}
	credential, err := cicdService.SaveCredential(ctx, project, credentialKey, input)
	input.Password, deployToken.Token = "", ""
	if err != nil {
		_ = client.deleteGroupDeployToken(ctx, group.ID, deployToken.ID)
		return cicd.Credential{}, err
	}
	credential, err = cicdService.SyncCredential(ctx, project, credential.Key)
	if err != nil {
		return cicd.Credential{}, err
	}
	for _, old := range oldTokens {
		if old.Name == tokenName && old.ID != deployToken.ID {
			_ = client.deleteGroupDeployToken(ctx, group.ID, old.ID)
		}
	}
	return credential, nil
}

func gitLabInsufficientScope(err error) bool {
	return err != nil && strings.Contains(strings.ToLower(err.Error()), "insufficient_scope")
}

func sourceCredentialExternalID(project string) string {
	return limit("ops-"+strings.ToLower(strings.TrimSpace(project))+"-source-read", 128)
}

func sourceCredentialExternalIDForRoot(project, rootGroup, primaryRoot string) string {
	if strings.EqualFold(strings.Trim(rootGroup, "/"), strings.Trim(primaryRoot, "/")) {
		return sourceCredentialExternalID(project)
	}
	return limit(sourceCredentialExternalID(project)+"-"+shortRootDigest(rootGroup), 128)
}

func sourceCredentialKey(connectionKey string) string {
	key := "gitlab-source-read-" + strings.ToLower(strings.TrimSpace(connectionKey))
	if len(key) <= 63 {
		return key
	}
	digest := fmt.Sprintf("%x", sha256.Sum256([]byte(key)))[:8]
	return key[:54] + "-" + digest
}

// deliveryCredentialKey keeps the delivery credential isolated per Jenkins
// connection. A project can have dev/test/uat/prod Jenkins instances at the
// same time, while CICD credential keys are unique inside the whole project.
// Reusing the legacy fixed key here would make the second environment collide
// with (or overwrite) the first environment's credential.
func deliveryCredentialKey(connectionKey string) string {
	key := "gitlab-delivery-read-" + strings.ToLower(strings.TrimSpace(connectionKey))
	if len(key) <= 63 {
		return key
	}
	digest := fmt.Sprintf("%x", sha256.Sum256([]byte(key)))[:8]
	return key[:54] + "-" + digest
}

func deliveryCredentialKeyForEnvironment(connectionKey, environment string) string {
	return deliveryCredentialKey(connectionKey + "-" + strings.ToLower(strings.TrimSpace(environment)))
}

func sourceCredentialKeyForRoot(connectionKey, rootGroup, primaryRoot string) string {
	if strings.EqualFold(strings.Trim(rootGroup, "/"), strings.Trim(primaryRoot, "/")) {
		return sourceCredentialKey(connectionKey)
	}
	return sourceCredentialKey(connectionKey + "-" + shortRootDigest(rootGroup))
}

func shortRootDigest(rootGroup string) string {
	return fmt.Sprintf("%x", sha256.Sum256([]byte(strings.ToLower(strings.Trim(rootGroup, "/")))))[:8]
}

// EnsureSourceRepository creates or updates one platform-owned source
// repository without ever taking over an unrelated GitLab project. Existing
// repositories must contain a matching ownership marker before files can be
// changed.
func (s *Service) EnsureSourceRepository(ctx context.Context, project, serviceKey string, files []SourceFile) (SourceRepositoryResult, error) {
	project = strings.ToLower(strings.TrimSpace(project))
	serviceKey = strings.ToLower(strings.TrimSpace(serviceKey))
	if !keyPattern.MatchString(project) || !keyPattern.MatchString(serviceKey) || len(files) == 0 || len(files) > 50 {
		return SourceRepositoryResult{}, fmt.Errorf("%w：源码仓库参数不正确", ErrInvalid)
	}
	delivery, server, client, err := s.deliveryClient(ctx, project)
	if err != nil {
		return SourceRepositoryResult{}, err
	}
	group, err := client.group(ctx, delivery.RootGroup)
	if err != nil {
		return SourceRepositoryResult{}, err
	}
	repositoryPath := project + "-" + serviceKey
	fullPath := delivery.RootGroup + "/" + repositoryPath
	repositoryKey := "source-" + serviceKey
	if len(repositoryKey) > 63 {
		repositoryKey = repositoryKey[:63]
	}
	repository, exists, err := client.project(ctx, fullPath)
	if err != nil {
		return SourceRepositoryResult{}, err
	}
	created := false
	if !exists {
		repository, err = client.createProject(ctx, group.ID, project+" "+serviceKey+" Source", repositoryPath, delivery.DefaultBranch, server.Visibility)
		if err != nil {
			return SourceRepositoryResult{}, err
		}
		created = true
	} else {
		marker, markerExists, markerErr := client.fileContent(ctx, repository.ID, delivery.DefaultBranch, ".ops-deploy/source.json")
		if markerErr != nil {
			return SourceRepositoryResult{}, markerErr
		}
		var ownership struct {
			ManagedBy string `json:"managed_by"`
			Project   string `json:"project"`
			Service   string `json:"service"`
		}
		owned := markerExists && json.Unmarshal(marker, &ownership) == nil && ownership.ManagedBy == "ops-deploy-platform" && ownership.Project == project && ownership.Service == serviceKey
		if !owned {
			registered, registeredErr := s.store.GetCICDRepository(ctx, project, repositoryKey)
			owned = registeredErr == nil && registered.Purpose == "source" && registered.CloneURL == repository.HTTPURLToRepo
		}
		if !owned {
			return SourceRepositoryResult{}, fmt.Errorf("%w：源码仓库 %s 已存在但不属于当前项目服务，已拒绝覆盖", ErrConflict, fullPath)
		}
	}
	registered := cicd.Repository{Key: repositoryKey, ProjectKey: project, DisplayName: serviceKey + " 业务源码", Provider: "gitlab", Purpose: "source", CloneURL: repository.HTTPURLToRepo, DefaultBranch: delivery.DefaultBranch, Description: "由运维自动部署平台创建，仅属于当前项目服务"}
	if err := s.store.SaveCICDRepository(ctx, registered); err != nil {
		return SourceRepositoryResult{}, err
	}
	marker, _ := json.MarshalIndent(map[string]any{"schema": 1, "managed_by": "ops-deploy-platform", "project": project, "service": serviceKey}, "", "  ")
	generated := []GeneratedFile{{Path: ".ops-deploy/source.json", Content: string(marker) + "\n"}}
	seen := map[string]bool{".ops-deploy/source.json": true}
	for _, file := range files {
		file.Path = strings.Trim(strings.TrimSpace(file.Path), "/")
		if !sourcePathPattern.MatchString(file.Path) || strings.Contains(file.Path, "..") || seen[file.Path] || len(file.Content) > 1<<20 {
			return SourceRepositoryResult{}, fmt.Errorf("%w：源码文件路径不正确、重复或内容过大", ErrInvalid)
		}
		seen[file.Path] = true
		generated = append(generated, GeneratedFile{Path: file.Path, Content: file.Content})
	}
	if _, _, err := client.commitFiles(ctx, repository.ID, delivery.DefaultBranch, "feat: initialize "+serviceKey+" smoke service", generated); err != nil {
		return SourceRepositoryResult{}, err
	}
	return SourceRepositoryResult{ProjectID: repository.ID, FullPath: repository.PathWithNamespace, CloneURL: repository.HTTPURLToRepo, Created: created, Files: len(generated)}, nil
}

// SyncDeliveryCredential creates a group-scoped, read_repository-only deploy
// token and writes it directly into the selected Jenkins credential store. The
// higher-privilege GitLab API token is never copied to Jenkins.
func (s *Service) SyncDeliveryCredential(ctx context.Context, project, connectionKey string, cicdService *cicd.Service) (cicd.Credential, error) {
	if cicdService == nil {
		return cicd.Credential{}, fmt.Errorf("%w：CI/CD 服务不可用", ErrInvalid)
	}
	project = strings.ToLower(strings.TrimSpace(project))
	connectionKey = strings.ToLower(strings.TrimSpace(connectionKey))
	connection, err := cicdService.GetConnection(ctx, project, connectionKey)
	if err != nil {
		return cicd.Credential{}, err
	}
	environment := strings.ToLower(strings.TrimSpace(connection.EnvironmentKey))
	if environment == "" {
		return cicd.Credential{}, fmt.Errorf("%w：目标 Jenkins 尚未绑定环境，不能生成生产/测试共用凭据", ErrConflict)
	}
	delivery, _, client, err := s.deliveryClient(ctx, project)
	if err != nil {
		return cicd.Credential{}, err
	}
	defer func() { client.token = "" }()
	credentialKey := deliveryCredentialKeyForEnvironment(connectionKey, environment)
	externalID := limit("ops-"+project+"-"+environment+"-gitlab-read", 128)
	if existing, listErr := cicdService.ListCredentials(ctx, project); listErr == nil {
		for _, credential := range existing {
			// Reuse both the new connection-scoped key and the legacy fixed key,
			// but only when it belongs to this exact Jenkins connection.
			if credential.ConnectionKey == connectionKey && credential.Kind == "gitlab_token" && credential.ExternalID == externalID && strings.HasPrefix(credential.Description, "GitLab Group Deploy Token") {
				inspection, validateErr := cicdService.ValidateGitCredential(ctx, project, credential.Key, delivery.JenkinsfilesCloneURL)
				if validateErr == nil && inspection.SmartHTTP {
					return cicdService.SyncCredential(ctx, project, credential.Key)
				}
			}
			if credential.Key == credentialKey && credential.ConnectionKey != connectionKey {
				return cicd.Credential{}, fmt.Errorf("%w：Jenkins 交付凭据标识已被其他连接占用", ErrConflict)
			}
		}
	}
	group, err := client.group(ctx, delivery.RootGroup)
	if err != nil {
		return cicd.Credential{}, err
	}
	// Token names are connection-scoped too. Otherwise rotating production
	// credentials would revoke the token still used by test Jenkins.
	name := limit("ops-deploy-"+project+"-"+connectionKey+"-jenkins-read", 128)
	oldDeployTokens, _ := client.listGroupDeployTokens(ctx, group.ID)
	deployToken, err := client.createGroupDeployToken(ctx, group.ID, name)
	if err != nil {
		return cicd.Credential{}, fmt.Errorf("%w：创建只读 Group Deploy Token 失败：%v", ErrRequest, err)
	}
	if deployToken.Username == "" || deployToken.Token == "" || !client.gitHTTPAccess(ctx, delivery.JenkinsfilesCloneURL, deployToken.Username, deployToken.Token) {
		_ = client.deleteGroupDeployToken(ctx, group.ID, deployToken.ID)
		return cicd.Credential{}, fmt.Errorf("%w：新建的只读 Group Deploy Token 无法读取项目交付仓库", ErrRequest)
	}
	input := cicd.CredentialInput{
		EnvironmentKey: environment,
		ConnectionKey:  connectionKey,
		DisplayName:    "项目 GitLab 只读交付凭据",
		Kind:           "gitlab_token",
		ExternalID:     externalID,
		Description:    fmt.Sprintf("GitLab Group Deploy Token %d（仅 read_repository），由平台管理", deployToken.ID),
		Username:       deployToken.Username,
		Password:       deployToken.Token,
	}
	credential, err := cicdService.SaveCredential(ctx, project, credentialKey, input)
	input.Password, deployToken.Token = "", ""
	if err != nil {
		_ = client.deleteGroupDeployToken(ctx, group.ID, deployToken.ID)
		return cicd.Credential{}, err
	}
	credential, err = cicdService.SyncCredential(ctx, project, credential.Key)
	if err != nil {
		return cicd.Credential{}, err
	}
	for _, old := range oldDeployTokens {
		if old.Name == name && old.ID != deployToken.ID {
			_ = client.deleteGroupDeployToken(ctx, group.ID, old.ID)
		}
	}
	return credential, nil
}

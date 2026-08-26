package gitlab

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const maxGitLabResponse = 4 << 20

type gitLabClient struct {
	baseURL *url.URL
	token   string
	http    *http.Client
}

type gitLabGroup struct {
	ID       int64  `json:"id"`
	FullPath string `json:"full_path"`
}

type gitLabProject struct {
	ID                int64  `json:"id"`
	Name              string `json:"name"`
	Path              string `json:"path"`
	PathWithNamespace string `json:"path_with_namespace"`
	HTTPURLToRepo     string `json:"http_url_to_repo"`
	SSHURLToRepo      string `json:"ssh_url_to_repo"`
	DefaultBranch     string `json:"default_branch"`
}

type gitLabUser struct {
	ID       int64  `json:"id"`
	Username string `json:"username"`
}

type gitLabDeployToken struct {
	ID       int64    `json:"id"`
	Name     string   `json:"name"`
	Username string   `json:"username"`
	Token    string   `json:"token"`
	Scopes   []string `json:"scopes"`
}

type gitLabBranch struct {
	Name      string `json:"name"`
	Default   bool   `json:"default"`
	Protected bool   `json:"protected"`
}

func newGitLabClient(rawURL, token string) (*gitLabClient, error) {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return nil, err
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	client := &http.Client{Timeout: 20 * time.Second, Transport: transport}
	originHost, originScheme := parsed.Host, parsed.Scheme
	client.CheckRedirect = func(request *http.Request, via []*http.Request) error {
		if len(via) >= 3 || !strings.EqualFold(request.URL.Host, originHost) || !strings.EqualFold(request.URL.Scheme, originScheme) {
			return errors.New("GitLab redirect was rejected")
		}
		return nil
	}
	return &gitLabClient{baseURL: parsed, token: token, http: client}, nil
}

func (c *gitLabClient) endpoint(path string) string {
	return strings.TrimSuffix(c.baseURL.String(), "/") + "/api/v4/" + strings.TrimPrefix(path, "/")
}

func (c *gitLabClient) request(ctx context.Context, method, path string, input, output any) (int, error) {
	var body io.Reader
	if input != nil {
		payload, err := json.Marshal(input)
		if err != nil {
			return 0, err
		}
		body = bytes.NewReader(payload)
	}
	request, err := http.NewRequestWithContext(ctx, method, c.endpoint(path), body)
	if err != nil {
		return 0, err
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("User-Agent", "OpsDeployPlatform/1.7")
	request.Header.Set("PRIVATE-TOKEN", c.token)
	if input != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := c.http.Do(request)
	if err != nil {
		return 0, fmt.Errorf("%w: %v", ErrRequest, err)
	}
	defer response.Body.Close()
	data, err := io.ReadAll(io.LimitReader(response.Body, maxGitLabResponse+1))
	if err != nil {
		return response.StatusCode, fmt.Errorf("%w: %v", ErrRequest, err)
	}
	if len(data) > maxGitLabResponse {
		return response.StatusCode, fmt.Errorf("%w: GitLab response is too large", ErrRequest)
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		message := gitLabErrorMessage(response, data)
		var envelope struct {
			Message any `json:"message"`
		}
		if json.Unmarshal(data, &envelope) == nil && envelope.Message != nil {
			if encoded, marshalErr := json.Marshal(envelope.Message); marshalErr == nil {
				message = strings.Trim(string(encoded), `"`)
			}
		}
		if len(message) > 500 {
			message = message[:500]
		}
		return response.StatusCode, fmt.Errorf("%w: HTTP %d: %s", ErrRequest, response.StatusCode, message)
	}
	if output != nil && len(data) > 0 {
		if err := json.Unmarshal(data, output); err != nil {
			return response.StatusCode, fmt.Errorf("%w: decode response: %v", ErrRequest, err)
		}
	}
	return response.StatusCode, nil
}

func gitLabErrorMessage(response *http.Response, data []byte) string {
	message := strings.TrimSpace(string(data))
	server := strings.ToLower(response.Header.Get("Server"))
	contentType := strings.ToLower(response.Header.Get("Content-Type"))
	lowerData := bytes.ToLower(data)
	isHTML := strings.Contains(contentType, "text/html") || bytes.HasPrefix(bytes.TrimSpace(lowerData), []byte("<!doctype html")) || bytes.HasPrefix(bytes.TrimSpace(lowerData), []byte("<html"))
	cloudflareChallenge := bytes.Contains(lowerData, []byte("cloudflare")) || bytes.Contains(lowerData, []byte("attention required"))
	// A GitLab JSON 403 is commonly proxied with `Server: cloudflare`. The
	// proxy header alone is not evidence of a WAF rejection; otherwise a normal
	// GitLab role/scope error is hidden behind a misleading IP allow-list hint.
	if response.StatusCode == http.StatusForbidden && cloudflareChallenge && (isHTML || strings.Contains(server, "cloudflare")) {
		rayID := strings.TrimSpace(response.Header.Get("CF-Ray"))
		if rayID != "" {
			return "Cloudflare/WAF 拒绝平台出口访问（Ray ID " + rayID + "）；请放行平台出口 IP，或对受 Token 保护的 /api/v4/* 配置受控放行"
		}
		return "Cloudflare/WAF 拒绝平台出口访问；请放行平台出口 IP，或对受 Token 保护的 /api/v4/* 配置受控放行"
	}
	if isHTML {
		return "GitLab 地址返回 HTML 页面而不是 API JSON；请检查反向代理、SSO 或 WAF 是否放行 /api/v4/*"
	}
	if message == "" {
		return http.StatusText(response.StatusCode)
	}
	return message
}

func (c *gitLabClient) currentUser(ctx context.Context) error {
	result, err := c.user(ctx)
	if err == nil && result.ID == 0 {
		return fmt.Errorf("%w: token did not resolve a GitLab user", ErrRequest)
	}
	return err
}

func (c *gitLabClient) user(ctx context.Context) (gitLabUser, error) {
	var result gitLabUser
	_, err := c.request(ctx, http.MethodGet, "user", nil, &result)
	return result, err
}

func (c *gitLabClient) gitHTTPAccess(ctx context.Context, cloneURL, username, password string) bool {
	parsed, err := url.Parse(strings.TrimSuffix(strings.TrimSpace(cloneURL), "/") + "/info/refs?service=git-upload-pack")
	if err != nil || parsed.Host == "" || !strings.EqualFold(parsed.Host, c.baseURL.Host) || parsed.Scheme != c.baseURL.Scheme {
		return false
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, parsed.String(), nil)
	if err != nil {
		return false
	}
	request.SetBasicAuth(username, password)
	request.Header.Set("Accept", "application/x-git-upload-pack-advertisement")
	response, err := c.http.Do(request)
	if err != nil {
		return false
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, 4096))
	if err != nil || response.StatusCode != http.StatusOK {
		return false
	}
	// A reverse proxy, SSO page or WAF can return an HTML 200 for this URL.
	// Only Git smart-HTTP's upload-pack advertisement proves the credential can
	// actually be used by Jenkins checkout.
	contentType := strings.ToLower(response.Header.Get("Content-Type"))
	return strings.Contains(contentType, "application/x-git-upload-pack-advertisement") &&
		bytes.Contains(body, []byte("# service=git-upload-pack"))
}

func (c *gitLabClient) listGroupDeployTokens(ctx context.Context, groupID int64) ([]gitLabDeployToken, error) {
	var result []gitLabDeployToken
	_, err := c.request(ctx, http.MethodGet, "groups/"+strconv.FormatInt(groupID, 10)+"/deploy_tokens", nil, &result)
	return result, err
}

func (c *gitLabClient) createGroupDeployToken(ctx context.Context, groupID int64, name string) (gitLabDeployToken, error) {
	var result gitLabDeployToken
	_, err := c.request(ctx, http.MethodPost, "groups/"+strconv.FormatInt(groupID, 10)+"/deploy_tokens", map[string]any{"name": name, "scopes": []string{"read_repository"}}, &result)
	return result, err
}

func (c *gitLabClient) deleteGroupDeployToken(ctx context.Context, groupID, tokenID int64) error {
	_, err := c.request(ctx, http.MethodDelete, "groups/"+strconv.FormatInt(groupID, 10)+"/deploy_tokens/"+strconv.FormatInt(tokenID, 10), nil, nil)
	return err
}

func (c *gitLabClient) group(ctx context.Context, path string) (gitLabGroup, error) {
	var result gitLabGroup
	status, err := c.request(ctx, http.MethodGet, "groups/"+url.PathEscape(path), nil, &result)
	if err == nil || status < http.StatusInternalServerError {
		return result, err
	}
	// Some self-managed GitLab versions return 500 for GET /groups/:id even
	// though the token can list the same group. Fall back to an exact full_path
	// match so a GitLab server-side detail endpoint defect does not block the
	// entire integration. Authentication and authorization errors are never
	// bypassed by this compatibility path.
	var candidates []gitLabGroup
	searchPath := "groups?search=" + url.QueryEscape(path) + "&per_page=100"
	if _, searchErr := c.request(ctx, http.MethodGet, searchPath, nil, &candidates); searchErr != nil {
		return result, err
	}
	for _, candidate := range candidates {
		if candidate.ID > 0 && strings.EqualFold(strings.Trim(candidate.FullPath, "/"), strings.Trim(path, "/")) {
			return candidate, nil
		}
	}
	return result, err
}

func (c *gitLabClient) groupProjects(ctx context.Context, groupID int64, search string) ([]gitLabProject, error) {
	query := "include_subgroups=true&with_shared=false&archived=false&simple=true&per_page=100&order_by=path&sort=asc"
	if search = strings.TrimSpace(search); search != "" {
		query += "&search=" + url.QueryEscape(search)
	}
	result := make([]gitLabProject, 0, 100)
	for page := 1; page <= 10; page++ {
		var batch []gitLabProject
		_, err := c.request(ctx, http.MethodGet, "groups/"+strconv.FormatInt(groupID, 10)+"/projects?"+query+"&page="+strconv.Itoa(page), nil, &batch)
		if err != nil {
			return nil, err
		}
		result = append(result, batch...)
		if len(batch) < 100 {
			return result, nil
		}
	}
	return result, fmt.Errorf("%w: 业务源码 GitLab 根组下的仓库超过 1000 个，请使用搜索缩小范围", ErrRequest)
}

func (c *gitLabClient) project(ctx context.Context, path string) (gitLabProject, bool, error) {
	var result gitLabProject
	status, err := c.request(ctx, http.MethodGet, "projects/"+url.PathEscape(path), nil, &result)
	if status == http.StatusNotFound {
		return result, false, nil
	}
	return result, err == nil, err
}

func (c *gitLabClient) projectBranches(ctx context.Context, projectID int64) ([]gitLabBranch, error) {
	result := make([]gitLabBranch, 0, 100)
	for page := 1; page <= 10; page++ {
		var batch []gitLabBranch
		_, err := c.request(ctx, http.MethodGet, "projects/"+strconv.FormatInt(projectID, 10)+"/repository/branches?per_page=100&page="+strconv.Itoa(page), nil, &batch)
		if err != nil {
			return nil, err
		}
		result = append(result, batch...)
		if len(batch) < 100 {
			return result, nil
		}
	}
	return nil, fmt.Errorf("%w: GitLab 仓库分支超过 1000 个，请清理长期无用分支", ErrRequest)
}

func (c *gitLabClient) createProject(ctx context.Context, groupID int64, name, path, branch, visibility string) (gitLabProject, error) {
	var result gitLabProject
	_, err := c.request(ctx, http.MethodPost, "projects", map[string]any{
		"namespace_id": groupID, "name": name, "path": path, "default_branch": branch,
		"visibility": visibility, "initialize_with_readme": false,
	}, &result)
	return result, err
}

func (c *gitLabClient) fileExists(ctx context.Context, projectID int64, branch, path string) (bool, error) {
	status, err := c.request(ctx, http.MethodGet, "projects/"+strconv.FormatInt(projectID, 10)+"/repository/files/"+url.PathEscape(path)+"?ref="+url.QueryEscape(branch), nil, nil)
	if status == http.StatusNotFound {
		return false, nil
	}
	return err == nil, err
}

func (c *gitLabClient) fileContent(ctx context.Context, projectID int64, branch, path string) ([]byte, bool, error) {
	var result struct {
		Content  string `json:"content"`
		Encoding string `json:"encoding"`
	}
	status, err := c.request(ctx, http.MethodGet, "projects/"+strconv.FormatInt(projectID, 10)+"/repository/files/"+url.PathEscape(path)+"?ref="+url.QueryEscape(branch), nil, &result)
	if status == http.StatusNotFound {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	if result.Encoding != "base64" {
		return nil, false, fmt.Errorf("%w: unsupported GitLab file encoding", ErrRequest)
	}
	content, err := base64.StdEncoding.DecodeString(strings.ReplaceAll(result.Content, "\n", ""))
	if err != nil {
		return nil, false, fmt.Errorf("%w: decode GitLab file: %v", ErrRequest, err)
	}
	return content, true, nil
}

func (c *gitLabClient) repositoryHasFiles(ctx context.Context, projectID int64, branch string) (bool, error) {
	var result []json.RawMessage
	status, err := c.request(ctx, http.MethodGet, "projects/"+strconv.FormatInt(projectID, 10)+"/repository/tree?ref="+url.QueryEscape(branch)+"&per_page=1", nil, &result)
	if status == http.StatusNotFound {
		return false, nil
	}
	return len(result) > 0, err
}

func (c *gitLabClient) commitFiles(ctx context.Context, projectID int64, branch, message string, files []GeneratedFile) (created, updated int, err error) {
	return c.commitFilesWithDeletes(ctx, projectID, branch, message, files, nil)
}

func (c *gitLabClient) commitFilesWithDeletes(ctx context.Context, projectID int64, branch, message string, files []GeneratedFile, deletePaths []string) (created, updated int, err error) {
	actions := make([]map[string]string, 0, len(files)+len(deletePaths))
	written := make(map[string]bool, len(files))
	for _, file := range files {
		written[file.Path] = true
		current, exists, lookupErr := c.fileContent(ctx, projectID, branch, file.Path)
		if lookupErr != nil {
			return 0, 0, lookupErr
		}
		if exists && bytes.Equal(current, []byte(file.Content)) {
			continue
		}
		action := "create"
		if exists {
			action = "update"
			updated++
		} else {
			created++
		}
		actions = append(actions, map[string]string{"action": action, "file_path": file.Path, "content": file.Content})
	}
	for _, path := range deletePaths {
		path = strings.TrimSpace(path)
		if path == "" || written[path] {
			continue
		}
		_, exists, lookupErr := c.fileContent(ctx, projectID, branch, path)
		if lookupErr != nil {
			return 0, 0, lookupErr
		}
		if exists {
			actions = append(actions, map[string]string{"action": "delete", "file_path": path})
		}
	}
	if len(actions) == 0 {
		return 0, 0, nil
	}
	_, err = c.request(ctx, http.MethodPost, "projects/"+strconv.FormatInt(projectID, 10)+"/repository/commits", map[string]any{
		"branch": branch, "commit_message": message, "actions": actions,
	}, nil)
	if err != nil {
		return created, updated, err
	}
	// GitLab is the source of truth for generated delivery files. Read every
	// changed path back before reporting success so the platform never marks a
	// manifest or Jenkinsfile as synchronized based only on a successful POST.
	for _, action := range actions {
		path := action["file_path"]
		content, exists, verifyErr := c.fileContent(ctx, projectID, branch, path)
		if verifyErr != nil {
			return created, updated, fmt.Errorf("%w: verify %s: %v", ErrRequest, path, verifyErr)
		}
		if action["action"] == "delete" {
			if exists {
				return created, updated, fmt.Errorf("%w: GitLab file %s still exists after deletion", ErrRequest, path)
			}
			continue
		}
		if !exists || !bytes.Equal(content, []byte(action["content"])) {
			return created, updated, fmt.Errorf("%w: GitLab file %s did not match after commit", ErrRequest, path)
		}
	}
	return created, updated, nil
}

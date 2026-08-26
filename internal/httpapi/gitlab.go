package httpapi

import (
	"errors"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"

	"ops-deploy-platform/internal/access"
	"ops-deploy-platform/internal/gitlab"
	"ops-deploy-platform/internal/sensitive"
)

func (s *Server) listGitLabServers(w http.ResponseWriter, r *http.Request) {
	if !requirePlatformPermission(w, r, platformManageCredentials) {
		return
	}
	if s.gitlab == nil {
		writeError(w, http.StatusServiceUnavailable, errors.New("GitLab service is unavailable"))
		return
	}
	items, err := s.gitlab.ListServers(r.Context())
	if err != nil {
		writeGitLabError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, items)
}

func (s *Server) createGitLabServer(w http.ResponseWriter, r *http.Request) {
	if !requirePlatformPermission(w, r, platformManageCredentials) {
		return
	}
	s.saveGitLabServer(w, r, "")
}

func (s *Server) updateGitLabServer(w http.ResponseWriter, r *http.Request) {
	if !requirePlatformPermission(w, r, platformManageCredentials) {
		return
	}
	s.saveGitLabServer(w, r, r.PathValue("server"))
}

func (s *Server) saveGitLabServer(w http.ResponseWriter, r *http.Request, key string) {
	if s.gitlab == nil {
		writeError(w, http.StatusServiceUnavailable, errors.New("GitLab service is unavailable"))
		return
	}
	var input gitlab.ServerInput
	if err := decodeJSON(w, r, &input); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	item, err := s.gitlab.SaveServer(r.Context(), key, input)
	if err != nil {
		writeGitLabError(w, err)
		return
	}
	status := http.StatusOK
	if key == "" {
		status = http.StatusCreated
	}
	writeJSON(w, status, item)
}

func (s *Server) deleteGitLabServer(w http.ResponseWriter, r *http.Request) {
	if !requirePlatformPermission(w, r, platformManageCredentials) {
		return
	}
	if s.gitlab == nil {
		writeError(w, http.StatusServiceUnavailable, errors.New("GitLab service is unavailable"))
		return
	}
	if err := s.gitlab.DeleteServer(r.Context(), r.PathValue("server")); err != nil {
		writeGitLabError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) testGitLabServer(w http.ResponseWriter, r *http.Request) {
	if !requirePlatformPermission(w, r, platformManageCredentials) {
		return
	}
	if s.gitlab == nil {
		writeError(w, http.StatusServiceUnavailable, errors.New("GitLab service is unavailable"))
		return
	}
	item, err := s.gitlab.TestServer(r.Context(), r.PathValue("server"))
	if err != nil {
		writeGitLabError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, item)
}

func (s *Server) listProjectGitLabServers(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("project")
	if _, err := s.requireProjectView(r, project); err != nil {
		writeAccessError(w, err)
		return
	}
	if s.gitlab == nil {
		writeError(w, http.StatusServiceUnavailable, errors.New("GitLab service is unavailable"))
		return
	}
	items, err := s.gitlab.ListServers(r.Context())
	if err != nil {
		writeGitLabError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, items)
}

func (s *Server) getProjectGitLabDelivery(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("project")
	if _, err := s.requireProjectView(r, project); err != nil {
		writeAccessError(w, err)
		return
	}
	if s.gitlab == nil {
		writeError(w, http.StatusServiceUnavailable, errors.New("GitLab service is unavailable"))
		return
	}
	item, err := s.gitlab.GetDelivery(r.Context(), project)
	if err != nil {
		writeGitLabError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, item)
}

func (s *Server) listProjectSourceRepositories(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("project")
	if _, err := s.requireProjectView(r, project); err != nil {
		writeAccessError(w, err)
		return
	}
	if s.gitlab == nil {
		writeError(w, http.StatusServiceUnavailable, errors.New("GitLab service is unavailable"))
		return
	}
	items, err := s.gitlab.ListSourceRepositories(r.Context(), project, r.URL.Query().Get("search"))
	if err != nil {
		writeGitLabError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"repositories": items})
}

func (s *Server) listProjectSourceRepositoryBranches(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("project")
	if _, err := s.requireProjectView(r, project); err != nil {
		writeAccessError(w, err)
		return
	}
	if s.gitlab == nil {
		writeError(w, http.StatusServiceUnavailable, errors.New("GitLab service is unavailable"))
		return
	}
	projectID, err := strconv.ParseInt(strings.TrimSpace(r.PathValue("repository")), 10, 64)
	if err != nil || projectID <= 0 {
		writeError(w, http.StatusBadRequest, errors.New("业务源码仓库 ID 不正确"))
		return
	}
	items, err := s.gitlab.ListSourceRepositoryBranches(r.Context(), project, projectID)
	if err != nil {
		writeGitLabError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"branches": items})
}

func (s *Server) checkProjectSourceRepositoryFile(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("project")
	if _, err := s.requireProjectView(r, project); err != nil {
		writeAccessError(w, err)
		return
	}
	if s.gitlab == nil {
		writeError(w, http.StatusServiceUnavailable, errors.New("GitLab service is unavailable"))
		return
	}
	projectID, err := strconv.ParseInt(strings.TrimSpace(r.PathValue("repository")), 10, 64)
	if err != nil || projectID <= 0 {
		writeError(w, http.StatusBadRequest, errors.New("业务源码仓库 ID 不正确"))
		return
	}
	result, err := s.gitlab.CheckSourceRepositoryFile(r.Context(), project, projectID, r.URL.Query().Get("branch"), r.URL.Query().Get("path"))
	if err != nil {
		writeGitLabError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) saveProjectGitLabDelivery(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("project")
	if err := s.requireProjectConfigure(r, project); err != nil {
		writeAccessError(w, err)
		return
	}
	if s.gitlab == nil {
		writeError(w, http.StatusServiceUnavailable, errors.New("GitLab service is unavailable"))
		return
	}
	var input gitlab.ProjectDelivery
	if err := decodeJSON(w, r, &input); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	item, err := s.gitlab.SaveDelivery(r.Context(), project, input)
	if err != nil {
		writeGitLabError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, item)
}

func (s *Server) activateProjectGitLabDelivery(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("project")
	if err := s.requireProjectConfigure(r, project); err != nil {
		writeAccessError(w, err)
		return
	}
	if s.gitlab == nil {
		writeError(w, http.StatusServiceUnavailable, errors.New("GitLab service is unavailable"))
		return
	}
	var input gitlab.ProjectDelivery
	if err := decodeJSON(w, r, &input); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	item, err := s.gitlab.Activate(r.Context(), project, input)
	if err != nil {
		writeGitLabError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, item)
}

func (s *Server) detachProjectGitLabDelivery(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("project")
	if err := s.requireProjectConfigure(r, project); err != nil {
		writeAccessError(w, err)
		return
	}
	if s.gitlab == nil {
		writeError(w, http.StatusServiceUnavailable, errors.New("GitLab service is unavailable"))
		return
	}
	var input struct {
		Confirm string `json:"confirm"`
	}
	if err := decodeJSON(w, r, &input); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if input.Confirm != project {
		writeError(w, http.StatusBadRequest, errors.New("项目确认标识不匹配"))
		return
	}
	if err := s.gitlab.DetachDelivery(r.Context(), project); err != nil {
		writeGitLabError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) previewProjectGitLabDelivery(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("project")
	if _, err := s.requireProjectView(r, project); err != nil {
		writeAccessError(w, err)
		return
	}
	if s.gitlab == nil {
		writeError(w, http.StatusServiceUnavailable, errors.New("GitLab service is unavailable"))
		return
	}
	item, err := s.gitlab.Preview(r.Context(), project)
	if err != nil {
		writeGitLabError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, item)
}

func (s *Server) provisionProjectGitLabDelivery(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("project")
	if err := s.requireProjectConfigure(r, project); err != nil {
		writeAccessError(w, err)
		return
	}
	if s.gitlab == nil {
		writeError(w, http.StatusServiceUnavailable, errors.New("GitLab service is unavailable"))
		return
	}
	item, err := s.gitlab.Provision(r.Context(), project)
	if err != nil {
		writeGitLabError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, item)
}

func writeGitLabError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, gitlab.ErrInvalid):
		writeError(w, http.StatusBadRequest, err)
	case errors.Is(err, gitlab.ErrConflict):
		writeError(w, http.StatusConflict, err)
	case errors.Is(err, gitlab.ErrRequest):
		writeGitLabRequestError(w, err)
	case errors.Is(err, os.ErrNotExist), errors.Is(err, gitlab.ErrNotFound):
		writeError(w, http.StatusNotFound, err)
	case errors.Is(err, access.ErrForbidden):
		writeError(w, http.StatusForbidden, err)
	default:
		writeError(w, http.StatusInternalServerError, err)
	}
}

// writeGitLabRequestError keeps arbitrary upstream response bodies out of the
// browser while returning a useful, operation-specific diagnosis. GitLab API
// authentication uses PRIVATE-TOKEN and therefore never requires a username;
// a username is only needed later for Git smart-HTTP credentials.
func writeGitLabRequestError(w http.ResponseWriter, err error) {
	status := http.StatusFailedDependency
	log.Printf("request failed status=%d dependency=gitlab error=%s", status, safeLogField(sensitive.RedactText(err.Error()))) // #nosec G706 -- the value is redacted, control-character stripped and length-bounded.

	detail := strings.ToLower(err.Error())
	message := "GitLab 上游请求失败；请先在 GitLab 服务器页面执行连接测试，并检查 Token 的 api 权限、授权根组和网络访问策略。GitLab API Token 不需要用户名。"
	switch {
	case strings.Contains(detail, "不能只读拉取") && strings.Contains(detail, "read_repository"):
		message = "业务源码凭据同步失败：当前 GitLab Token 不能创建 Deploy Token，也不能读取所选源码仓库。请为 Token 增加 read_repository，或改用对授权根组具有 Owner 角色且包含 api 权限的 Token。"
	case strings.Contains(detail, "创建业务源码只读 group deploy token") && strings.Contains(detail, "http 403"):
		message = "业务源码凭据创建失败：GitLab Token 必须对业务源码授权根组具有 Owner 角色并包含 api 权限。当前请求已到达 GitLab，但被拒绝；请更新业务源码 GitLab Token 后重新同步。"
	case strings.Contains(detail, "创建只读 group deploy token") && strings.Contains(detail, "http 403"):
		message = "项目交付凭据创建失败：GitLab Token 必须对交付授权根组具有 Owner 角色并包含 api 权限。当前请求已到达 GitLab，但被拒绝；请更新交付 GitLab Token 后重新同步。"
	case strings.Contains(detail, "cloudflare/waf") || strings.Contains(detail, "attention required"):
		message = "GitLab 请求被 Cloudflare/WAF 拒绝；请放行平台出口 IP 对该 GitLab 的 /api/v4/* 访问后重试。此问题与用户名无关，GitLab API Token 不需要用户名。"
	case strings.Contains(detail, "html 页面") || strings.Contains(detail, "html page"):
		message = "GitLab API 返回了 HTML 页面；请检查反向代理、SSO 或 WAF 是否允许平台访问 /api/v4/*。GitLab API Token 不需要用户名。"
	case strings.Contains(detail, "http 401"):
		message = "GitLab Token 无效或已过期；请更新具有 api 权限的 Token 后重试。"
	case strings.Contains(detail, "http 403"):
		message = "GitLab 拒绝访问；请检查 Token 的 api 权限、根组权限以及 WAF/IP 白名单。"
	case strings.Contains(detail, "timeout") || strings.Contains(detail, "deadline exceeded"):
		message = "连接 GitLab 超时；请检查平台到 GitLab 的 DNS、路由、防火墙和代理配置。"
	}
	writeJSON(w, status, map[string]any{"error": message, "code": "gitlab_dependency_failed", "dependency": "gitlab", "retryable": true})
}

package httpapi

import (
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"

	"ops-deploy-platform/internal/sensitive"
	statusservice "ops-deploy-platform/internal/status"
)

type ingressWriteRequest struct {
	YAML            string `json:"yaml"`
	ResourceVersion string `json:"resource_version,omitempty"`
}

func (s *Server) listEnvironmentIngresses(w http.ResponseWriter, r *http.Request) {
	project, environmentKey := r.PathValue("project"), r.PathValue("environment")
	targetName, ok := s.ingressEnvironmentTarget(w, r, project, environmentKey, false)
	if !ok {
		return
	}
	ctx, cancel := contextWithTimeout(r, 90*time.Second)
	defer cancel()
	items, err := s.status.ListIngresses(ctx, targetName)
	if err != nil {
		writeError(w, http.StatusFailedDependency, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, map[string]any{
		"project": project, "environment": environmentKey, "observed_at": time.Now().UTC(), "ingresses": items,
	})
}

func (s *Server) syncEnvironmentIngressConfig(w http.ResponseWriter, r *http.Request) {
	project, environmentKey := r.PathValue("project"), r.PathValue("environment")
	targetName, ok := s.ingressEnvironmentTarget(w, r, project, environmentKey, true)
	if !ok {
		return
	}
	ctx, cancel := contextWithTimeout(r, 120*time.Second)
	defer cancel()
	items, err := s.status.ListIngresses(ctx, targetName)
	if err != nil {
		writeError(w, http.StatusFailedDependency, err)
		return
	}
	doc, err := s.environments.Load(targetName)
	if err != nil {
		writeError(w, http.StatusNotFound, err)
		return
	}
	report := statusservice.SyncIngressesToDomainConfigFromCluster(doc, items)
	preview := strings.EqualFold(strings.TrimSpace(r.URL.Query().Get("preview")), "true")
	if !preview {
		if err := s.environments.Save(targetName, doc); err != nil {
			writeError(w, http.StatusConflict, err)
			return
		}
		s.status.Invalidate(r.Context(), targetName)
	}
	sensitive.Sanitize(map[string]any(doc))
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, map[string]any{
		"project": project, "environment": environmentKey, "preview": preview, "config": doc, "report": report,
	})
}

func (s *Server) getEnvironmentIngress(w http.ResponseWriter, r *http.Request) {
	project, environmentKey := r.PathValue("project"), r.PathValue("environment")
	targetName, ok := s.ingressEnvironmentTarget(w, r, project, environmentKey, false)
	if !ok {
		return
	}
	namespace, name, err := ingressPathIdentity(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	ctx, cancel := contextWithTimeout(r, 90*time.Second)
	defer cancel()
	document, err := s.status.GetIngress(ctx, targetName, namespace, name)
	if err != nil {
		writeError(w, http.StatusFailedDependency, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, document)
}

func (s *Server) validateEnvironmentIngress(w http.ResponseWriter, r *http.Request) {
	project, environmentKey := r.PathValue("project"), r.PathValue("environment")
	targetName, ok := s.ingressEnvironmentTarget(w, r, project, environmentKey, true)
	if !ok {
		return
	}
	var request ingressWriteRequest
	if err := decodeJSON(w, r, &request); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	ctx, cancel := contextWithTimeout(r, 90*time.Second)
	defer cancel()
	result, err := s.status.ValidateIngress(ctx, targetName, []byte(request.YAML))
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) createEnvironmentIngress(w http.ResponseWriter, r *http.Request) {
	project, environmentKey := r.PathValue("project"), r.PathValue("environment")
	targetName, ok := s.ingressEnvironmentTarget(w, r, project, environmentKey, true)
	if !ok {
		return
	}
	var request ingressWriteRequest
	if err := decodeJSON(w, r, &request); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	ctx, cancel := contextWithTimeout(r, 120*time.Second)
	defer cancel()
	document, err := s.status.ApplyIngress(ctx, targetName, []byte(request.YAML), "", "", "")
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusCreated, document)
}

func (s *Server) updateEnvironmentIngress(w http.ResponseWriter, r *http.Request) {
	project, environmentKey := r.PathValue("project"), r.PathValue("environment")
	targetName, ok := s.ingressEnvironmentTarget(w, r, project, environmentKey, true)
	if !ok {
		return
	}
	namespace, name, err := ingressPathIdentity(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	var request ingressWriteRequest
	if err := decodeJSON(w, r, &request); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	ctx, cancel := contextWithTimeout(r, 120*time.Second)
	defer cancel()
	document, err := s.status.ApplyIngress(ctx, targetName, []byte(request.YAML), namespace, name, request.ResourceVersion)
	if err != nil {
		writeError(w, http.StatusConflict, err)
		return
	}
	writeJSON(w, http.StatusOK, document)
}

func (s *Server) deleteEnvironmentIngress(w http.ResponseWriter, r *http.Request) {
	project, environmentKey := r.PathValue("project"), r.PathValue("environment")
	targetName, ok := s.ingressEnvironmentTarget(w, r, project, environmentKey, true)
	if !ok {
		return
	}
	namespace, name, err := ingressPathIdentity(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	ctx, cancel := contextWithTimeout(r, 90*time.Second)
	defer cancel()
	if err := s.status.DeleteIngress(ctx, targetName, namespace, name, r.URL.Query().Get("resource_version")); err != nil {
		writeError(w, http.StatusConflict, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) ingressEnvironmentTarget(w http.ResponseWriter, r *http.Request, project, environmentKey string, mutate bool) (string, bool) {
	if _, err := s.requireProjectView(r, project); err != nil {
		writeAccessError(w, err)
		return "", false
	}
	if mutate {
		if err := s.requireProjectConfigure(r, project); err != nil {
			writeAccessError(w, err)
			return "", false
		}
		if err := s.requireProjectDeploy(r, project); err != nil {
			writeAccessError(w, err)
			return "", false
		}
	}
	if err := s.requireBoundProjectAWSCredential(r.Context(), project); err != nil {
		writeProjectAWSCredentialError(w, err)
		return "", false
	}
	if s.status == nil {
		writeError(w, http.StatusServiceUnavailable, errors.New("EKS Ingress 管理服务不可用"))
		return "", false
	}
	item, err := s.accessControl.Environment(r.Context(), project, environmentKey)
	if err != nil {
		writeAccessError(w, err)
		return "", false
	}
	return item.TargetName, true
}

func ingressPathIdentity(r *http.Request) (string, string, error) {
	namespace, err := url.PathUnescape(r.PathValue("namespace"))
	if err != nil {
		return "", "", errors.New("Ingress Namespace 无效")
	}
	name, err := url.PathUnescape(r.PathValue("ingress"))
	if err != nil {
		return "", "", errors.New("Ingress 名称无效")
	}
	namespace, name = strings.TrimSpace(namespace), strings.TrimSpace(name)
	if namespace == "" || name == "" {
		return "", "", errors.New("Ingress Namespace 和名称不能为空")
	}
	return namespace, name, nil
}

package httpapi

import (
	"errors"
	"net/http"
	"time"

	"ops-deploy-platform/internal/access"
)

func (s *Server) projectEnvironmentApplicationTopology(w http.ResponseWriter, r *http.Request) {
	projectKey, environmentKey := r.PathValue("project"), r.PathValue("environment")
	if _, err := s.requireProjectView(r, projectKey); err != nil {
		writeAccessError(w, err)
		return
	}
	if err := s.requireBoundProjectAWSCredential(r.Context(), projectKey); err != nil {
		writeProjectAWSCredentialError(w, err)
		return
	}
	if s.status == nil {
		writeError(w, http.StatusServiceUnavailable, errors.New("应用观测服务不可用"))
		return
	}
	item, err := s.accessControl.Environment(r.Context(), projectKey, environmentKey)
	if err != nil {
		writeAccessError(w, err)
		return
	}
	ctx, cancel := contextWithTimeout(r, 60*time.Second)
	defer cancel()
	topology, err := s.status.ApplicationTopology(ctx, item.TargetName, r.URL.Query().Get("fresh") == "true")
	if err != nil {
		writeError(w, http.StatusFailedDependency, err)
		return
	}
	w.Header().Set("Cache-Control", "private, no-store")
	writeJSON(w, http.StatusOK, map[string]any{
		"project": projectKey, "environment": environmentKey,
		"environment_name": access.EnvironmentName(environmentKey),
		"target_name":      item.TargetName,
		"observed_at":      topology.ObservedAt,
		"source":           topology.Source,
		"summary":          topology.Summary,
		"nodes":            topology.Nodes,
		"edges":            topology.Edges,
		"alerts":           topology.Alerts,
		"warnings":         topology.Warnings,
	})
}

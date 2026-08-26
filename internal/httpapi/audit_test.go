package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"ops-deploy-platform/internal/access"
	"ops-deploy-platform/internal/auditlog"
	"ops-deploy-platform/internal/auth"
)

func TestListAuditEventsRequiresDedicatedPermissionAndParsesFilters(t *testing.T) {
	store := &recordingAuditStore{}
	server := &Server{auditStore: store}

	forbiddenRequest := httptest.NewRequest(http.MethodGet, "/api/audit-events", nil)
	forbiddenRequest = forbiddenRequest.WithContext(auth.WithSession(forbiddenRequest.Context(), auth.Session{Username: "viewer"}))
	forbidden := httptest.NewRecorder()
	server.listAuditEvents(forbidden, forbiddenRequest)
	if forbidden.Code != http.StatusForbidden {
		t.Fatalf("audit endpoint without permission status = %d", forbidden.Code)
	}

	allowedRequest := httptest.NewRequest(http.MethodGet, "/api/audit-events?username=operator&method=DELETE&result=failed&keyword=game-admin&include_system=true&page=2&page_size=50", nil)
	allowedRequest = allowedRequest.WithContext(auth.WithSession(allowedRequest.Context(), auth.Session{
		Username: "auditor",
		PlatformPermissions: access.PlatformPermission{
			CanViewAudit: true,
		},
	}))
	allowed := httptest.NewRecorder()
	server.listAuditEvents(allowed, allowedRequest)
	if allowed.Code != http.StatusOK {
		t.Fatalf("audit endpoint status = %d body=%s", allowed.Code, allowed.Body.String())
	}
	if store.query.Username != "operator" || store.query.Method != http.MethodDelete ||
		store.query.Result != "failed" || store.query.Keyword != "game-admin" ||
		!store.query.IncludeSystem || store.query.Page != 2 || store.query.PageSize != 50 {
		t.Fatalf("unexpected audit query: %#v", store.query)
	}
	var response auditlog.Page
	if err := json.Unmarshal(allowed.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.Items == nil {
		t.Fatal("audit API returned a null items collection")
	}
}

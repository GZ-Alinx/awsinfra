package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/GZ-Alinx/awsinfra/internal/access"
	"github.com/GZ-Alinx/awsinfra/internal/appconfig"
	"github.com/GZ-Alinx/awsinfra/internal/auditlog"
	"github.com/GZ-Alinx/awsinfra/internal/auth"
	"github.com/GZ-Alinx/awsinfra/internal/awscatalog"
	"github.com/GZ-Alinx/awsinfra/internal/cicd"
	"github.com/GZ-Alinx/awsinfra/internal/environment"
	"github.com/GZ-Alinx/awsinfra/internal/jobs"
	statusservice "github.com/GZ-Alinx/awsinfra/internal/status"
)

type noOpRunner struct{}

func (noOpRunner) Run(context.Context, string, jobs.Action, string, io.Writer) error { return nil }

type recordedAuditEvent struct {
	method, path, username, remote string
	status                         int
}

type recordingAuditStore struct {
	events []recordedAuditEvent
	query  auditlog.Query
}

func (s *recordingAuditStore) RecordAudit(_ context.Context, method, path string, status int, username, remote string, _ time.Duration) error {
	s.events = append(s.events, recordedAuditEvent{method: method, path: path, status: status, username: username, remote: remote})
	return nil
}

func (s *recordingAuditStore) ListAuditEvents(_ context.Context, query auditlog.Query) (auditlog.Page, error) {
	s.query = query
	return auditlog.Page{Items: []auditlog.Event{}, Total: 0, Page: query.Page, PageSize: query.PageSize}, nil
}

func TestAuditUsesSystemActorsAndTrustedForwardedClientIP(t *testing.T) {
	store := &recordingAuditStore{}
	server := &Server{auditStore: store}
	handler := server.audit(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusAccepted) }))

	request := httptest.NewRequest(http.MethodPost, "/api/internal/alerting/relay/demo-test", nil)
	request.RemoteAddr = "172.31.20.10:49152"
	request.Header.Set("X-Forwarded-For", "203.0.113.42, 172.31.20.10")
	handler.ServeHTTP(httptest.NewRecorder(), request)
	if len(store.events) != 1 || store.events[0].username != "system:alerting" || store.events[0].remote != "203.0.113.42" {
		t.Fatalf("unexpected system audit event: %#v", store.events)
	}

	store.events = nil
	request = httptest.NewRequest(http.MethodPost, "/api/jobs", nil)
	request.RemoteAddr = "198.51.100.9:443"
	request.Header.Set("X-Forwarded-For", "203.0.113.99")
	handler.ServeHTTP(httptest.NewRecorder(), request)
	if len(store.events) != 1 || store.events[0].remote != "198.51.100.9" {
		t.Fatalf("untrusted public peer overrode audit address: %#v", store.events)
	}
}

func TestAPIAuthenticationSessionAndCSRF(t *testing.T) {
	passwordHash, err := auth.HashPassword([]byte("correct horse battery staple"))
	if err != nil {
		t.Fatal(err)
	}
	auditStore := &recordingAuditStore{}
	security := appconfig.SecurityConfig{
		AdminUsername: "admin", PasswordHashEnv: "TEST_HASH", SessionTTL: time.Hour,
		LoginMaxAttempts: 5, LoginWindow: time.Minute, LoginLockout: time.Minute,
		ExternalOrigin: "https://ops.example.com",
	}
	authentication, err := auth.NewService(security, passwordHash)
	if err != nil {
		t.Fatal(err)
	}
	config := &appconfig.Config{
		Security: security,
		Jobs:     appconfig.JobsConfig{MaxParallel: 1, HistoryLimit: 10, Timeout: time.Minute},
	}
	repository, err := environment.NewRepository(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	manager, err := jobs.NewManager(t.TempDir(), 1, 10, time.Minute, noOpRunner{})
	if err != nil {
		t.Fatal(err)
	}
	server, err := New(config, repository, manager, statusservice.NewService(config, repository), authentication)
	if err != nil {
		t.Fatal(err)
	}
	server.SetDataServices(auditStore, nil)

	index := httptest.NewRecorder()
	server.Handler().ServeHTTP(index, httptest.NewRequest(http.MethodGet, "/", nil))
	if index.Code != http.StatusOK || !strings.Contains(index.Header().Get("Cache-Control"), "no-store") {
		t.Fatalf("index cache policy = status %d, %q", index.Code, index.Header().Get("Cache-Control"))
	}
	assetStart := strings.Index(index.Body.String(), "/assets/")
	if assetStart < 0 {
		t.Fatal("index does not reference a content-hashed asset")
	}
	assetEnd := strings.IndexAny(index.Body.String()[assetStart:], "\"'")
	if assetEnd < 0 {
		t.Fatal("index contains an invalid asset reference")
	}
	assetPath := index.Body.String()[assetStart : assetStart+assetEnd]
	asset := httptest.NewRecorder()
	server.Handler().ServeHTTP(asset, httptest.NewRequest(http.MethodGet, assetPath, nil))
	if asset.Code != http.StatusOK {
		t.Fatalf("asset status = %d for %s", asset.Code, assetPath)
	}
	if !strings.Contains(asset.Header().Get("Cache-Control"), "immutable") {
		t.Fatalf("asset cache policy = %q", asset.Header().Get("Cache-Control"))
	}

	health := httptest.NewRecorder()
	server.Handler().ServeHTTP(health, httptest.NewRequest(http.MethodGet, "/api/health", nil))
	if health.Code != http.StatusOK {
		t.Fatalf("health status = %d", health.Code)
	}
	if health.Header().Get("Strict-Transport-Security") == "" {
		t.Fatal("HTTPS external origin behind a reverse proxy must enable HSTS")
	}

	unauthorized := httptest.NewRecorder()
	server.Handler().ServeHTTP(unauthorized, httptest.NewRequest(http.MethodGet, "/api/platform", nil))
	if unauthorized.Code != http.StatusUnauthorized {
		t.Fatalf("unauthorized status = %d", unauthorized.Code)
	}

	crossOriginRequest := httptest.NewRequest(http.MethodPost, "/api/auth/login", bytes.NewBufferString(`{"username":"admin","password":"correct horse battery staple"}`))
	crossOriginRequest.Header.Set("Origin", "https://evil.example")
	crossOrigin := httptest.NewRecorder()
	server.Handler().ServeHTTP(crossOrigin, crossOriginRequest)
	if crossOrigin.Code != http.StatusForbidden {
		t.Fatalf("cross-origin login status = %d", crossOrigin.Code)
	}

	loginBody := bytes.NewBufferString(`{"username":"admin","password":"correct horse battery staple"}`)
	loginRequest := httptest.NewRequest(http.MethodPost, "/api/auth/login", loginBody)
	login := httptest.NewRecorder()
	server.Handler().ServeHTTP(login, loginRequest)
	if login.Code != http.StatusOK {
		t.Fatalf("login status = %d body=%s", login.Code, login.Body.String())
	}
	var session auth.Session
	if err := json.Unmarshal(login.Body.Bytes(), &session); err != nil {
		t.Fatal(err)
	}
	cookies := login.Result().Cookies()
	if len(cookies) != 1 || !cookies[0].HttpOnly || cookies[0].SameSite != http.SameSiteStrictMode {
		t.Fatalf("unexpected session cookie: %#v", cookies)
	}

	authorizedRequest := httptest.NewRequest(http.MethodGet, "/api/platform", nil)
	authorizedRequest.AddCookie(cookies[0])
	authorized := httptest.NewRecorder()
	server.Handler().ServeHTTP(authorized, authorizedRequest)
	if authorized.Code != http.StatusOK {
		t.Fatalf("authorized status = %d", authorized.Code)
	}

	logoutWithoutCSRF := httptest.NewRequest(http.MethodPost, "/api/auth/logout", nil)
	logoutWithoutCSRF.AddCookie(cookies[0])
	forbidden := httptest.NewRecorder()
	server.Handler().ServeHTTP(forbidden, logoutWithoutCSRF)
	if forbidden.Code != http.StatusForbidden {
		t.Fatalf("missing CSRF status = %d", forbidden.Code)
	}
	foundRejectedLogout := false
	for _, event := range auditStore.events {
		if event.method == http.MethodPost && event.path == "/api/auth/logout" && event.status == http.StatusForbidden && event.username == "admin" {
			foundRejectedLogout = true
		}
	}
	if !foundRejectedLogout {
		t.Fatalf("authenticated CSRF rejection was not audited: %#v", auditStore.events)
	}

	logoutRequest := httptest.NewRequest(http.MethodPost, "/api/auth/logout", nil)
	logoutRequest.AddCookie(cookies[0])
	logoutRequest.Header.Set("X-CSRF-Token", session.CSRFToken)
	logout := httptest.NewRecorder()
	server.Handler().ServeHTTP(logout, logoutRequest)
	if logout.Code != http.StatusNoContent {
		t.Fatalf("logout status = %d", logout.Code)
	}
}

func TestDecodeJSONRejectsTrailingValues(t *testing.T) {
	request := httptest.NewRequest(http.MethodPost, "/", bytes.NewBufferString(`{"name":"first"}{"name":"second"}`))
	var target struct {
		Name string `json:"name"`
	}
	if err := decodeJSON(httptest.NewRecorder(), request, &target); err == nil {
		t.Fatal("trailing JSON value was accepted")
	}
}

func TestCICDJobInputJSONContract(t *testing.T) {
	valid := `{
		"key":"game-release","environment_key":"test","display_name":"Game Release","service_name":"gateway","service_keys":["gateway"],
		"language":"go","jenkinsfile_mode":"generated","execution_mode":"serial","failure_policy":"stop",
		"connection_key":"test-jenkins","jenkins_job_name":"game-release","enabled":true,
		"jenkinsfile_repository":"ops-delivery-jenkinsfiles","jenkinsfile_repo":"https://git.example/jenkinsfiles.git",
		"jenkinsfile_branch":"main","jenkinsfile_path":"jobs/game-release/Jenkinsfile","jenkinsfile_credential":"gitlab-read",
		"manifest_repository":"ops-delivery-manifests","manifest_repo":"https://git.example/manifests.git",
		"manifest_branch":"main","manifest_path":"environments","manifest_credential":"gitlab-read",
		"environment_paths":{"test":"environments/test"},"parameters":{"JENKINS_AGENT_MODE":"kubernetes"},
		"parameter_definitions":[{"name":"RELEASE_KIND","type":"choice","default_value":"full","choices":["full","config-only"],"description":"发布类型","required":true}]
	}`
	request := httptest.NewRequest(http.MethodPost, "/", bytes.NewBufferString(valid))
	var input cicd.JobInput
	if err := decodeJSON(httptest.NewRecorder(), request, &input); err != nil {
		t.Fatalf("valid CI/CD Job payload was rejected: %v", err)
	}
	job := input.Job()
	if job.Key != "game-release" || job.EnvironmentKey != "test" || len(job.ParameterDefinitions) != 1 || job.ParameterDefinitions[0].Name != "RELEASE_KIND" {
		t.Fatalf("unexpected decoded CI/CD Job: %#v", job)
	}

	for _, unknown := range []string{
		`{"key":"game-release","parameter_definitions":[{"_id":"ui-row","name":"RELEASE_KIND","type":"string"}]}`,
		`{"key":"game-release","sync_status":"ready"}`,
		`{"key":"game-release","project_key":"another-project"}`,
	} {
		request = httptest.NewRequest(http.MethodPost, "/", bytes.NewBufferString(unknown))
		if err := decodeJSON(httptest.NewRecorder(), request, &input); err == nil {
			t.Fatalf("unknown or server-owned field was accepted: %s", unknown)
		} else if !strings.Contains(err.Error(), "当前接口不支持的字段") {
			t.Fatalf("unknown field error is not actionable: %v", err)
		}
	}
}

func TestInternalErrorResponseDoesNotLeakDetails(t *testing.T) {
	response := httptest.NewRecorder()
	writeError(response, http.StatusInternalServerError, errors.New("database password=must-not-leak"))
	if strings.Contains(response.Body.String(), "must-not-leak") || !strings.Contains(response.Body.String(), "internal server error") {
		t.Fatalf("unsafe error response: %s", response.Body.String())
	}
}

func TestAWSCatalogErrorsRemainActionable(t *testing.T) {
	missing := httptest.NewRecorder()
	writeAWSCatalogError(missing, awscatalog.ErrCredentialUnavailable, "EKS Kubernetes 版本", "eks:DescribeClusterVersions")
	if missing.Code != http.StatusConflict || !strings.Contains(missing.Body.String(), "AWS 凭据池") || strings.Contains(missing.Body.String(), "upstream") {
		t.Fatalf("unexpected missing credential response: status=%d body=%s", missing.Code, missing.Body.String())
	}

	denied := httptest.NewRecorder()
	writeAWSCatalogError(denied, awscatalog.ErrAccessDenied, "EKS Kubernetes 版本", "eks:DescribeClusterVersions")
	if denied.Code != http.StatusFailedDependency || !strings.Contains(denied.Body.String(), "eks:DescribeClusterVersions") {
		t.Fatalf("unexpected access denied response: status=%d body=%s", denied.Code, denied.Body.String())
	}
}

func TestPermissionSubsetPreventsPrivilegeGrant(t *testing.T) {
	callerPlatform := access.PlatformPermission{CanManageUsers: true}
	if platformPermissionSubset(access.PlatformPermission{CanManageProjects: true}, callerPlatform) {
		t.Fatal("user manager was allowed to grant project management")
	}
	if !platformPermissionSubset(access.PlatformPermission{CanManageUsers: true}, callerPlatform) {
		t.Fatal("user manager could not delegate a permission they hold")
	}
	callerProject := access.Permission{CanView: true, CanConfigure: true}
	if projectPermissionSubset(access.Permission{CanView: true, CanDeploy: true}, callerProject) {
		t.Fatal("project configurator was allowed to grant deploy permission they do not hold")
	}
	if !projectPermissionSubset(access.Permission{CanView: true, CanConfigure: true}, callerProject) {
		t.Fatal("project configurator could not delegate permissions they hold")
	}
}

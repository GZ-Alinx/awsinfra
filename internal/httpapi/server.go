package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"ops-deploy-platform/internal/access"
	"ops-deploy-platform/internal/appconfig"
	"ops-deploy-platform/internal/auditlog"
	"ops-deploy-platform/internal/auth"
	"ops-deploy-platform/internal/awscatalog"
	"ops-deploy-platform/internal/awscredentials"
	"ops-deploy-platform/internal/cicd"
	"ops-deploy-platform/internal/componentcatalog"
	"ops-deploy-platform/internal/dataservicecredentials"
	"ops-deploy-platform/internal/environment"
	"ops-deploy-platform/internal/gitlab"
	"ops-deploy-platform/internal/jobs"
	"ops-deploy-platform/internal/resourcecenter"
	"ops-deploy-platform/internal/sensitive"
	"ops-deploy-platform/internal/statebackend"
	"ops-deploy-platform/internal/staticcdn"
	statusservice "ops-deploy-platform/internal/status"
	"ops-deploy-platform/internal/tlscertificates"
	"ops-deploy-platform/web"
)

const Version = "1.20.3"

type Server struct {
	config                 *appconfig.Config
	environments           *environment.Repository
	jobs                   *jobs.Manager
	status                 *statusservice.Service
	authentication         *auth.Service
	accessControl          *access.Service
	handler                http.Handler
	auditStore             AuditStore
	healthChecker          HealthChecker
	resources              *resourcecenter.Service
	awsCredentials         *awscredentials.Service
	awsCatalog             AWSCatalog
	componentCatalog       *componentcatalog.Service
	tlsCertificates        *tlscertificates.Service
	dataServiceCredentials *dataservicecredentials.Service
	cicd                   *cicd.Service
	gitlab                 *gitlab.Service
	stateBackend           *statebackend.Service
	staticCDN              *staticcdn.Service
}

type AWSCatalog interface {
	EKSVersions(context.Context, string, string) ([]awscatalog.EKSVersion, error)
	EKSClusters(context.Context, string, string) ([]awscatalog.EKSCluster, error)
	VPCs(context.Context, string, string) ([]awscatalog.VPC, error)
	SecurityGroups(context.Context, string, string, string) ([]awscatalog.SecurityGroup, error)
	InstanceTypes(context.Context, string, string, string) ([]awscatalog.InstanceType, error)
	ServiceInstanceTypes(context.Context, string, string, string, string) ([]awscatalog.ServiceInstanceOption, error)
	EngineVersions(context.Context, string, string, string, string) ([]string, error)
	EnsureECRRepository(context.Context, string, string, string) (awscatalog.ECRRepository, error)
}

type AuditStore interface {
	RecordAudit(context.Context, string, string, int, string, string, time.Duration) error
	ListAuditEvents(context.Context, auditlog.Query) (auditlog.Page, error)
}

type HealthChecker interface {
	Health(context.Context) (map[string]string, error)
}

func New(config *appconfig.Config, environments *environment.Repository, jobManager *jobs.Manager, status *statusservice.Service, authentication *auth.Service, accessServices ...*access.Service) (*Server, error) {
	if authentication == nil {
		return nil, errors.New("authentication service is required")
	}
	server := &Server{config: config, environments: environments, jobs: jobManager, status: status, authentication: authentication}
	if len(accessServices) > 0 {
		server.accessControl = accessServices[0]
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/health", server.health)
	mux.HandleFunc("POST /api/auth/login", server.login)
	mux.HandleFunc("GET /git-relay/{project}/{repository...}", server.gitRelay)
	mux.HandleFunc("POST /git-relay/{project}/{repository...}", server.gitRelay)
	mux.HandleFunc("POST /api/internal/alerting/relay/{target}", server.relayAlertmanager)
	mux.HandleFunc("GET /api/auth/session", server.currentSession)
	mux.HandleFunc("POST /api/auth/logout", server.logout)
	mux.HandleFunc("GET /api/me", server.currentProfile)
	mux.HandleFunc("PUT /api/me/profile", server.updateCurrentProfile)
	mux.HandleFunc("PUT /api/me/password", server.updateCurrentPassword)
	mux.HandleFunc("GET /api/platform", server.platform)
	mux.HandleFunc("GET /api/projects", server.listProjects)
	mux.HandleFunc("POST /api/projects", server.createProject)
	mux.HandleFunc("GET /api/projects/{project}", server.getProject)
	mux.HandleFunc("PUT /api/projects/{project}", server.updateProject)
	mux.HandleFunc("DELETE /api/projects/{project}", server.deleteProject)
	mux.HandleFunc("GET /api/projects/{project}/aws-credentials", server.getProjectAWSCredentials)
	mux.HandleFunc("PUT /api/projects/{project}/aws-credentials", server.saveProjectAWSCredentials)
	mux.HandleFunc("DELETE /api/projects/{project}/aws-credentials", server.deleteProjectAWSCredentials)
	mux.HandleFunc("GET /api/aws-credentials", server.listAWSCredentials)
	mux.HandleFunc("POST /api/aws-credentials", server.createAWSCredential)
	mux.HandleFunc("DELETE /api/aws-credentials/{credential}", server.deleteAWSCredential)
	mux.HandleFunc("GET /api/terraform-state", server.getTerraformStateCenter)
	mux.HandleFunc("PUT /api/terraform-state", server.saveTerraformStateCenter)
	mux.HandleFunc("GET /api/platform/gitlab/servers", server.listGitLabServers)
	mux.HandleFunc("POST /api/platform/gitlab/servers", server.createGitLabServer)
	mux.HandleFunc("PUT /api/platform/gitlab/servers/{server}", server.updateGitLabServer)
	mux.HandleFunc("DELETE /api/platform/gitlab/servers/{server}", server.deleteGitLabServer)
	mux.HandleFunc("POST /api/platform/gitlab/servers/{server}/test", server.testGitLabServer)
	mux.HandleFunc("PUT /api/projects/{project}/aws-credential-selection", server.selectProjectAWSCredential)
	mux.HandleFunc("GET /api/component-catalog", server.listHelmComponents)
	mux.HandleFunc("POST /api/component-catalog", server.createHelmComponent)
	mux.HandleFunc("PUT /api/component-catalog/{component}", server.updateHelmComponent)
	mux.HandleFunc("DELETE /api/component-catalog/{component}", server.deleteHelmComponent)
	mux.HandleFunc("POST /api/component-catalog/inspect", server.inspectHelmComponent)
	mux.HandleFunc("POST /api/component-catalog/versions", server.listHelmComponentVersions)
	mux.HandleFunc("GET /api/projects/{project}/aws-catalog/eks-versions", server.getEKSVersions)
	mux.HandleFunc("GET /api/projects/{project}/aws-catalog/eks-clusters", server.getEKSClusters)
	mux.HandleFunc("GET /api/projects/{project}/aws-catalog/vpcs", server.getVPCs)
	mux.HandleFunc("GET /api/projects/{project}/aws-catalog/security-groups", server.getSecurityGroups)
	mux.HandleFunc("GET /api/projects/{project}/aws-catalog/instance-types", server.getEC2InstanceTypes)
	mux.HandleFunc("GET /api/projects/{project}/aws-catalog/service-instance-types", server.getServiceInstanceTypes)
	mux.HandleFunc("GET /api/projects/{project}/aws-catalog/engine-versions", server.getEngineVersions)
	mux.HandleFunc("POST /api/projects/{project}/cicd/ecr/repositories", server.ensureCICDECRRepository)
	mux.HandleFunc("POST /api/projects/{project}/environments", server.createProjectEnvironment)
	mux.HandleFunc("GET /api/projects/{project}/environments/{environment}", server.getProjectEnvironment)
	mux.HandleFunc("PUT /api/projects/{project}/environments/{environment}", server.saveProjectEnvironment)
	mux.HandleFunc("DELETE /api/projects/{project}/environments/{environment}", server.deleteProjectEnvironment)
	mux.HandleFunc("GET /api/projects/{project}/environments/{environment}/tls-certificates", server.listEnvironmentTLSCertificates)
	mux.HandleFunc("PUT /api/projects/{project}/environments/{environment}/tls-certificates/{certificate}", server.saveEnvironmentTLSCertificate)
	mux.HandleFunc("DELETE /api/projects/{project}/environments/{environment}/tls-certificates/{certificate}", server.deleteEnvironmentTLSCertificate)
	mux.HandleFunc("GET /api/projects/{project}/environments/{environment}/data-service-credentials", server.listEnvironmentDataServiceCredentials)
	mux.HandleFunc("PUT /api/projects/{project}/environments/{environment}/data-service-credentials/{service}", server.saveEnvironmentDataServiceCredential)
	mux.HandleFunc("DELETE /api/projects/{project}/environments/{environment}/data-service-credentials/{service}", server.deleteEnvironmentDataServiceCredential)
	mux.HandleFunc("GET /api/projects/{project}/environments/{environment}/status", server.projectEnvironmentStatus)
	mux.HandleFunc("GET /api/projects/{project}/environments/{environment}/observability/topology", server.projectEnvironmentApplicationTopology)
	mux.HandleFunc("GET /api/projects/{project}/environments/{environment}/kubernetes/services", server.projectEnvironmentKubernetesServices)
	mux.HandleFunc("GET /api/projects/{project}/environments/{environment}/components/clickvisual-stack/storage", server.listClickVisualStackStorage)
	mux.HandleFunc("POST /api/projects/{project}/environments/{environment}/components/clickvisual-stack/storage/resize", server.resizeClickVisualStackStorage)
	mux.HandleFunc("GET /api/projects/{project}/environments/{environment}/components/opentelemetry-collector/storage", server.listOpenTelemetryCollectorStorage)
	mux.HandleFunc("POST /api/projects/{project}/environments/{environment}/components/opentelemetry-collector/storage/expand", server.expandOpenTelemetryCollectorStorage)
	mux.HandleFunc("GET /api/projects/{project}/environments/{environment}/kubernetes/ingresses", server.listEnvironmentIngresses)
	mux.HandleFunc("POST /api/projects/{project}/environments/{environment}/kubernetes/ingresses", server.createEnvironmentIngress)
	mux.HandleFunc("POST /api/projects/{project}/environments/{environment}/kubernetes/ingresses/sync-config", server.syncEnvironmentIngressConfig)
	mux.HandleFunc("POST /api/projects/{project}/environments/{environment}/kubernetes/ingresses/validate", server.validateEnvironmentIngress)
	mux.HandleFunc("GET /api/projects/{project}/environments/{environment}/kubernetes/ingresses/{namespace}/{ingress}", server.getEnvironmentIngress)
	mux.HandleFunc("PUT /api/projects/{project}/environments/{environment}/kubernetes/ingresses/{namespace}/{ingress}", server.updateEnvironmentIngress)
	mux.HandleFunc("DELETE /api/projects/{project}/environments/{environment}/kubernetes/ingresses/{namespace}/{ingress}", server.deleteEnvironmentIngress)
	mux.HandleFunc("POST /api/projects/{project}/environments/{environment}/alerting/channels/{channel}/test", server.testEnvironmentAlertChannel)
	mux.HandleFunc("POST /api/projects/{project}/environments/{environment}/alerting/scenarios/{scenario}/test", server.testEnvironmentAlertScenario)
	mux.HandleFunc("GET /api/projects/{project}/environments/{environment}/resources", server.environmentResources)
	mux.HandleFunc("POST /api/projects/{project}/environments/{environment}/resources/sync-aws", server.syncEnvironmentAWSConfiguration)
	mux.HandleFunc("GET /api/projects/{project}/environments/{environment}/static-cdns", server.listStaticCDNs)
	mux.HandleFunc("POST /api/projects/{project}/environments/{environment}/static-cdns", server.createStaticCDN)
	mux.HandleFunc("POST /api/projects/{project}/environments/{environment}/static-cdns/{bucket}/refresh", server.refreshStaticCDN)
	mux.HandleFunc("GET /api/projects/{project}/environments/{environment}/static-cdns/{bucket}/objects", server.listStaticCDNObjects)
	mux.HandleFunc("POST /api/projects/{project}/environments/{environment}/static-cdns/{bucket}/upload-url", server.authorizeStaticCDNUpload)
	mux.HandleFunc("PUT /api/projects/{project}/environments/{environment}/static-cdns/{bucket}/objects/{key...}", server.uploadStaticCDNObject)
	mux.HandleFunc("DELETE /api/projects/{project}/environments/{environment}/static-cdns/{bucket}/objects/{key...}", server.deleteStaticCDNObject)
	mux.HandleFunc("POST /api/projects/{project}/environments/{environment}/static-cdns/{bucket}/invalidate", server.invalidateStaticCDN)
	mux.HandleFunc("DELETE /api/projects/{project}/environments/{environment}/static-cdns/{bucket}", server.deleteStaticCDN)
	mux.HandleFunc("GET /api/projects/{project}/environments/{environment}/cicd/jenkins", server.environmentJenkinsIntegration)
	mux.HandleFunc("POST /api/projects/{project}/environments/{environment}/cicd/jenkins/connect", server.connectEnvironmentJenkins)
	mux.HandleFunc("GET /api/projects/{project}/environments/{environment}/cicd/connections", server.listCICDConnections)
	mux.HandleFunc("POST /api/projects/{project}/environments/{environment}/cicd/connections", server.createCICDConnection)
	mux.HandleFunc("PUT /api/projects/{project}/environments/{environment}/cicd/connections/{connection}", server.updateCICDConnection)
	mux.HandleFunc("DELETE /api/projects/{project}/environments/{environment}/cicd/connections/{connection}", server.deleteCICDConnection)
	mux.HandleFunc("POST /api/projects/{project}/environments/{environment}/cicd/connections/{connection}/test", server.testCICDConnection)
	mux.HandleFunc("GET /api/projects/{project}/environments/{environment}/cicd/credentials", server.listCICDCredentials)
	mux.HandleFunc("POST /api/projects/{project}/environments/{environment}/cicd/credentials", server.createCICDCredential)
	mux.HandleFunc("PUT /api/projects/{project}/environments/{environment}/cicd/credentials/{credential}", server.updateCICDCredential)
	mux.HandleFunc("DELETE /api/projects/{project}/environments/{environment}/cicd/credentials/{credential}", server.deleteCICDCredential)
	mux.HandleFunc("POST /api/projects/{project}/environments/{environment}/cicd/credentials/{credential}/sync", server.syncCICDCredential)
	// Keep the project-scoped query routes for backward compatibility with
	// existing clients. The web UI uses the environment-scoped routes above so
	// the isolation boundary cannot be lost while the selected scope changes.
	mux.HandleFunc("GET /api/projects/{project}/cicd/connections", server.listCICDConnections)
	mux.HandleFunc("POST /api/projects/{project}/cicd/connections", server.createCICDConnection)
	mux.HandleFunc("PUT /api/projects/{project}/cicd/connections/{connection}", server.updateCICDConnection)
	mux.HandleFunc("DELETE /api/projects/{project}/cicd/connections/{connection}", server.deleteCICDConnection)
	mux.HandleFunc("POST /api/projects/{project}/cicd/connections/{connection}/test", server.testCICDConnection)
	mux.HandleFunc("GET /api/projects/{project}/cicd/credentials", server.listCICDCredentials)
	mux.HandleFunc("POST /api/projects/{project}/cicd/credentials", server.createCICDCredential)
	mux.HandleFunc("PUT /api/projects/{project}/cicd/credentials/{credential}", server.updateCICDCredential)
	mux.HandleFunc("DELETE /api/projects/{project}/cicd/credentials/{credential}", server.deleteCICDCredential)
	mux.HandleFunc("POST /api/projects/{project}/cicd/credentials/{credential}/sync", server.syncCICDCredential)
	mux.HandleFunc("GET /api/projects/{project}/cicd/notification-channels", server.listCICDNotificationChannels)
	mux.HandleFunc("GET /api/projects/{project}/cicd/repositories", server.listCICDRepositories)
	mux.HandleFunc("POST /api/projects/{project}/cicd/repositories", server.createCICDRepository)
	mux.HandleFunc("PUT /api/projects/{project}/cicd/repositories/{repository}", server.updateCICDRepository)
	mux.HandleFunc("DELETE /api/projects/{project}/cicd/repositories/{repository}", server.deleteCICDRepository)
	mux.HandleFunc("POST /api/projects/{project}/cicd/jenkinsfile/analyze", server.analyzeCICDJenkinsfile)
	mux.HandleFunc("GET /api/projects/{project}/cicd/gitlab-servers", server.listProjectGitLabServers)
	mux.HandleFunc("GET /api/projects/{project}/cicd/delivery", server.getProjectGitLabDelivery)
	mux.HandleFunc("GET /api/projects/{project}/cicd/source-repositories", server.listProjectSourceRepositories)
	mux.HandleFunc("GET /api/projects/{project}/cicd/source-repositories/{repository}/branches", server.listProjectSourceRepositoryBranches)
	mux.HandleFunc("GET /api/projects/{project}/cicd/source-repositories/{repository}/files/check", server.checkProjectSourceRepositoryFile)
	mux.HandleFunc("PUT /api/projects/{project}/cicd/delivery", server.saveProjectGitLabDelivery)
	mux.HandleFunc("DELETE /api/projects/{project}/cicd/delivery", server.detachProjectGitLabDelivery)
	mux.HandleFunc("POST /api/projects/{project}/cicd/delivery/activate", server.activateProjectGitLabDelivery)
	mux.HandleFunc("POST /api/projects/{project}/cicd/delivery/preview", server.previewProjectGitLabDelivery)
	mux.HandleFunc("POST /api/projects/{project}/cicd/delivery/provision", server.provisionProjectGitLabDelivery)
	mux.HandleFunc("GET /api/projects/{project}/cicd/jobs", server.listCICDJobs)
	mux.HandleFunc("POST /api/projects/{project}/cicd/jobs", server.createCICDJob)
	mux.HandleFunc("PUT /api/projects/{project}/cicd/jobs/{job}", server.updateCICDJob)
	mux.HandleFunc("GET /api/projects/{project}/cicd/jobs/{job}/usage", server.cicdJobUsage)
	mux.HandleFunc("DELETE /api/projects/{project}/cicd/jobs/{job}", server.deleteCICDJob)
	mux.HandleFunc("POST /api/projects/{project}/cicd/jobs/{job}/sync", server.syncCICDJob)
	mux.HandleFunc("POST /api/projects/{project}/cicd/jobs/{job}/webhook/rotate", server.rotateCICDJobWebhookSecret)
	mux.HandleFunc("POST /api/projects/{project}/cicd/jobs/{job}/builds", server.triggerCICDBuild)
	mux.HandleFunc("POST /api/cicd/webhooks/gitlab/{project}/{job}", server.receiveCICDGitLabWebhook)
	mux.HandleFunc("GET /api/projects/{project}/cicd/builds", server.listCICDBuilds)
	mux.HandleFunc("POST /api/projects/{project}/cicd/builds/{build}/retry", server.retryCICDBuild)
	mux.HandleFunc("POST /api/projects/{project}/cicd/builds/{build}/cancel", server.cancelCICDBuild)
	mux.HandleFunc("GET /api/projects/{project}/cicd/builds/{build}/logs", server.cicdBuildLogs)
	mux.HandleFunc("GET /api/projects/{project}/cicd/builds/{build}/deployment-logs", server.cicdBuildDeploymentLogs)
	mux.HandleFunc("POST /api/projects/{project}/environments/{environment}/credentials/{credential}/reveal", server.revealEnvironmentCredential)
	mux.HandleFunc("GET /api/users", server.listUsers)
	mux.HandleFunc("GET /api/audit-events", server.listAuditEvents)
	mux.HandleFunc("POST /api/users", server.createUser)
	mux.HandleFunc("PUT /api/users/{username}", server.updateUser)
	mux.HandleFunc("DELETE /api/users/{username}", server.deleteUser)
	mux.HandleFunc("PUT /api/projects/{project}/members/{username}", server.saveProjectMember)
	mux.HandleFunc("DELETE /api/projects/{project}/members/{username}", server.deleteProjectMember)
	mux.HandleFunc("GET /api/jobs", server.listJobs)
	mux.HandleFunc("DELETE /api/jobs", server.deleteJobHistory)
	mux.HandleFunc("POST /api/jobs", server.createJob)
	mux.HandleFunc("GET /api/jobs/{id}", server.getJob)
	mux.HandleFunc("DELETE /api/jobs/{id}", server.cancelJob)
	mux.HandleFunc("POST /api/jobs/{id}/retry", server.retryJob)
	mux.HandleFunc("POST /api/jobs/{id}/ignore", server.ignoreJobFailure)
	mux.HandleFunc("GET /api/jobs/{id}/logs", server.jobLogs)

	webFS, err := fs.Sub(web.FS(), "dist")
	if err != nil {
		return nil, err
	}
	staticFiles := http.FileServer(http.FS(webFS))
	mux.Handle("/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// index.html points at content-hashed lazy chunks. It must be revalidated
		// after every platform rollout; otherwise a long-lived browser tab can
		// request chunks from the previous image and make navigation appear dead.
		if r.URL.Path == "/" || r.URL.Path == "/index.html" {
			w.Header().Set("Cache-Control", "no-store, max-age=0")
		} else if strings.HasPrefix(r.URL.Path, "/assets/") {
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		}
		staticFiles.ServeHTTP(w, r)
	}))
	server.handler = server.observe(compressResponse(server.securityHeaders(server.audit(server.auth(mux)))))
	if jobManager != nil {
		jobManager.SetCompletionHandler(server.completeJob)
	}
	return server, nil
}

func (s *Server) SetStateBackendService(service *statebackend.Service) {
	s.stateBackend = service
}

func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	if !s.sameOrigin(r) {
		writeError(w, http.StatusForbidden, errors.New("cross-origin request rejected"))
		return
	}
	var request struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := decodeJSON(w, r, &request); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if auditWriter, ok := w.(*statusWriter); ok {
		auditWriter.username = strings.TrimSpace(request.Username)
	}
	session, err := s.authentication.Login(w, r, request.Username, request.Password)
	request.Password = ""
	if errors.Is(err, auth.ErrRateLimited) {
		writeError(w, http.StatusTooManyRequests, err)
		return
	}
	if err != nil {
		writeError(w, http.StatusUnauthorized, auth.ErrInvalidCredentials)
		return
	}
	if auditWriter, ok := w.(*statusWriter); ok {
		auditWriter.username = session.Username
	}
	writeJSON(w, http.StatusOK, session)
}

func (s *Server) currentSession(w http.ResponseWriter, r *http.Request) {
	session, ok := auth.SessionFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, auth.ErrUnauthenticated)
		return
	}
	writeJSON(w, http.StatusOK, session)
}

func (s *Server) currentProfile(w http.ResponseWriter, r *http.Request) {
	session, ok := auth.SessionFromContext(r.Context())
	if !ok || s.accessControl == nil {
		writeError(w, http.StatusUnauthorized, auth.ErrUnauthenticated)
		return
	}
	user, err := s.accessControl.GetUser(r.Context(), session.Username)
	if err != nil {
		writeAccessError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, user)
}

func (s *Server) updateCurrentProfile(w http.ResponseWriter, r *http.Request) {
	session, ok := auth.SessionFromContext(r.Context())
	if !ok || s.accessControl == nil {
		writeError(w, http.StatusUnauthorized, auth.ErrUnauthenticated)
		return
	}
	var request struct {
		DisplayName string `json:"display_name"`
	}
	if err := decodeJSON(w, r, &request); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	request.DisplayName = strings.TrimSpace(request.DisplayName)
	if request.DisplayName == "" || len([]rune(request.DisplayName)) > 128 {
		writeError(w, http.StatusBadRequest, errors.New("display name must contain 1 to 128 characters"))
		return
	}
	user, err := s.accessControl.GetUser(r.Context(), session.Username)
	if err != nil {
		writeAccessError(w, err)
		return
	}
	user.DisplayName = request.DisplayName
	if err := s.accessControl.SaveUser(r.Context(), user); err != nil {
		writeAccessError(w, err)
		return
	}
	user, err = s.accessControl.GetUser(r.Context(), session.Username)
	if err != nil {
		writeAccessError(w, err)
		return
	}
	refreshed, err := s.authentication.RefreshCurrentSession(r, user)
	if err != nil {
		writeError(w, http.StatusUnauthorized, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"user": user, "session": refreshed})
}

func (s *Server) updateCurrentPassword(w http.ResponseWriter, r *http.Request) {
	session, ok := auth.SessionFromContext(r.Context())
	if !ok || s.accessControl == nil {
		writeError(w, http.StatusUnauthorized, auth.ErrUnauthenticated)
		return
	}
	var request struct {
		CurrentPassword string `json:"current_password"`
		NewPassword     string `json:"new_password"`
	}
	if err := decodeJSON(w, r, &request); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if len(request.NewPassword) < 12 || len(request.NewPassword) > 256 || request.NewPassword == request.CurrentPassword {
		request.CurrentPassword, request.NewPassword = "", ""
		writeError(w, http.StatusBadRequest, errors.New("new password must contain 12 to 256 characters and differ from the current password"))
		return
	}
	if err := s.authentication.ReauthenticateRequest(r, session.Username, request.CurrentPassword); err != nil {
		request.CurrentPassword, request.NewPassword = "", ""
		if errors.Is(err, auth.ErrRateLimited) {
			writeError(w, http.StatusTooManyRequests, err)
			return
		}
		writeError(w, http.StatusUnauthorized, auth.ErrInvalidCredentials)
		return
	}
	hash, err := auth.HashPassword([]byte(request.NewPassword))
	request.CurrentPassword, request.NewPassword = "", ""
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	user, err := s.accessControl.GetUser(r.Context(), session.Username)
	if err != nil {
		writeAccessError(w, err)
		return
	}
	user.PasswordHash = hash
	if err := s.accessControl.SaveUser(r.Context(), user); err != nil {
		writeAccessError(w, err)
		return
	}
	s.authentication.Logout(w, r)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	s.authentication.Logout(w, r)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) Handler() http.Handler { return s.handler }

func (s *Server) SetDataServices(auditStore AuditStore, healthChecker HealthChecker) {
	s.auditStore = auditStore
	s.healthChecker = healthChecker
}

func (s *Server) SetResourceService(resources *resourcecenter.Service) { s.resources = resources }

func (s *Server) SetStaticCDNService(service *staticcdn.Service) { s.staticCDN = service }

func (s *Server) SetAWSCredentialService(credentials *awscredentials.Service) {
	s.awsCredentials = credentials
}

func (s *Server) SetAWSCatalog(catalog AWSCatalog) { s.awsCatalog = catalog }

func (s *Server) SetComponentCatalog(catalog *componentcatalog.Service) { s.componentCatalog = catalog }

func (s *Server) SetTLSCertificateService(certificates *tlscertificates.Service) {
	s.tlsCertificates = certificates
}

func (s *Server) SetDataServiceCredentialService(credentials *dataservicecredentials.Service) {
	s.dataServiceCredentials = credentials
}

func (s *Server) SetCICDService(service *cicd.Service) {
	s.cicd = service
	s.configureCICDBuildPreflight()
}

func (s *Server) SetGitLabService(service *gitlab.Service) {
	s.gitlab = service
	s.configureCICDBuildPreflight()
}

func (s *Server) configureCICDBuildPreflight() {
	if s.cicd != nil && s.gitlab != nil {
		s.cicd.SetBuildSourcePreflight(s.validateCICDBuildSources)
	}
}

func (s *Server) requireBoundProjectAWSCredential(ctx context.Context, project string) error {
	if s.awsCredentials == nil {
		return awscredentials.ErrCredentialNotBound
	}
	info, err := s.awsCredentials.Info(ctx, project)
	if err != nil {
		return err
	}
	if !info.Configured || !info.Selected || info.ProjectKey != project {
		return awscredentials.ErrCredentialNotBound
	}
	return nil
}

func writeProjectAWSCredentialError(w http.ResponseWriter, err error) {
	if errors.Is(err, awscredentials.ErrCredentialNotBound) || errors.Is(err, awscredentials.ErrCredentialMismatch) {
		writeError(w, http.StatusConflict, errors.New("当前项目未绑定并选中自己的 AWS 凭据；平台已拒绝操作，且不会使用其他项目凭据、AWS Profile、IAM Role 或默认凭据链"))
		return
	}
	writeError(w, http.StatusInternalServerError, err)
}

func (s *Server) getEKSVersions(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("project")
	if _, err := s.requireProjectView(r, project); err != nil {
		writeAccessError(w, err)
		return
	}
	if s.awsCatalog == nil {
		writeError(w, http.StatusServiceUnavailable, errors.New("AWS catalog service is unavailable"))
		return
	}
	versions, err := s.awsCatalog.EKSVersions(r.Context(), project, r.URL.Query().Get("region"))
	if errors.Is(err, awscatalog.ErrInvalidRegion) {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if err != nil {
		writeAWSCatalogError(w, err, "EKS Kubernetes 版本", "eks:DescribeClusterVersions")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"region": r.URL.Query().Get("region"), "versions": versions, "source": "aws-live"})
}

func (s *Server) getEKSClusters(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("project")
	if _, err := s.requireProjectView(r, project); err != nil {
		writeAccessError(w, err)
		return
	}
	if s.awsCatalog == nil {
		writeError(w, http.StatusServiceUnavailable, errors.New("AWS catalog service is unavailable"))
		return
	}
	items, err := s.awsCatalog.EKSClusters(r.Context(), project, r.URL.Query().Get("region"))
	if errors.Is(err, awscatalog.ErrInvalidRegion) {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if err != nil {
		writeAWSCatalogError(w, err, "EKS 集群列表", "eks:ListClusters")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"region": r.URL.Query().Get("region"), "clusters": items, "source": "aws-live"})
}

func (s *Server) getVPCs(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("project")
	if _, err := s.requireProjectView(r, project); err != nil {
		writeAccessError(w, err)
		return
	}
	if s.awsCatalog == nil {
		writeError(w, http.StatusServiceUnavailable, errors.New("AWS catalog service is unavailable"))
		return
	}
	items, err := s.awsCatalog.VPCs(r.Context(), project, r.URL.Query().Get("region"))
	if errors.Is(err, awscatalog.ErrInvalidRegion) {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if err != nil {
		writeAWSCatalogError(w, err, "VPC 与子网列表", "ec2:DescribeVpcs + ec2:DescribeSubnets")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"region": r.URL.Query().Get("region"), "vpcs": items, "source": "aws-live"})
}

func (s *Server) getSecurityGroups(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("project")
	// Security-group rules expose network boundaries and are only needed while
	// editing deploy configuration. Do not disclose them to project viewers
	// who do not hold configuration permission.
	if err := s.requireProjectConfigure(r, project); err != nil {
		writeAccessError(w, err)
		return
	}
	if s.awsCatalog == nil {
		writeError(w, http.StatusServiceUnavailable, errors.New("AWS catalog service is unavailable"))
		return
	}
	items, err := s.awsCatalog.SecurityGroups(r.Context(), project, r.URL.Query().Get("region"), r.URL.Query().Get("vpc_id"))
	if errors.Is(err, awscatalog.ErrInvalidRegion) || errors.Is(err, awscatalog.ErrInvalidQuery) {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if err != nil {
		writeAWSCatalogError(w, err, "VPC 安全组列表", "ec2:DescribeSecurityGroups")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"region": r.URL.Query().Get("region"), "vpc_id": r.URL.Query().Get("vpc_id"), "security_groups": items, "source": "aws-live",
	})
}

func (s *Server) getEC2InstanceTypes(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("project")
	if _, err := s.requireProjectView(r, project); err != nil {
		writeAccessError(w, err)
		return
	}
	if s.awsCatalog == nil {
		writeError(w, http.StatusServiceUnavailable, errors.New("AWS catalog service is unavailable"))
		return
	}
	items, err := s.awsCatalog.InstanceTypes(r.Context(), project, r.URL.Query().Get("region"), r.URL.Query().Get("query"))
	if errors.Is(err, awscatalog.ErrInvalidRegion) || errors.Is(err, awscatalog.ErrInvalidQuery) {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if err != nil {
		writeAWSCatalogError(w, err, "EC2 实例规格", "ec2:DescribeInstanceTypes")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"region": r.URL.Query().Get("region"), "query": r.URL.Query().Get("query"), "instance_types": items, "source": "aws-live"})
}

func (s *Server) getServiceInstanceTypes(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("project")
	if _, err := s.requireProjectView(r, project); err != nil {
		writeAccessError(w, err)
		return
	}
	if s.awsCatalog == nil {
		writeError(w, http.StatusServiceUnavailable, errors.New("AWS catalog service is unavailable"))
		return
	}
	service := r.URL.Query().Get("service")
	items, err := s.awsCatalog.ServiceInstanceTypes(r.Context(), project, r.URL.Query().Get("region"), service, r.URL.Query().Get("engine_version"))
	if errors.Is(err, awscatalog.ErrInvalidRegion) || errors.Is(err, awscatalog.ErrInvalidQuery) {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if errors.Is(err, awscatalog.ErrEngineVersionMissing) {
		writeError(w, http.StatusBadRequest, fmt.Errorf("AWS 在当前 Region 未提供引擎版本 %q", r.URL.Query().Get("engine_version")))
		return
	}
	if err != nil {
		permission := map[string]string{
			"rds-mysql": "rds:DescribeDBEngineVersions + rds:DescribeOrderableDBInstanceOptions", "rds-postgres": "rds:DescribeDBEngineVersions + rds:DescribeOrderableDBInstanceOptions",
			"documentdb": "rds:DescribeDBEngineVersions + rds:DescribeOrderableDBInstanceOptions", "amazon-mq": "mq:DescribeBrokerInstanceOptions",
			"elasticache": "pricing:GetProducts", "msk": "pricing:GetProducts",
		}[service]
		writeAWSCatalogError(w, err, "云服务实例规格", permission)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"region": r.URL.Query().Get("region"), "service": service, "instance_types": items, "source": "aws-live",
	})
}

func (s *Server) getEngineVersions(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("project")
	if _, err := s.requireProjectView(r, project); err != nil {
		writeAccessError(w, err)
		return
	}
	if s.awsCatalog == nil {
		writeError(w, http.StatusServiceUnavailable, errors.New("AWS catalog service is unavailable"))
		return
	}
	service := r.URL.Query().Get("service")
	versions, err := s.awsCatalog.EngineVersions(r.Context(), project, r.URL.Query().Get("region"), service, r.URL.Query().Get("engine"))
	if errors.Is(err, awscatalog.ErrInvalidRegion) || errors.Is(err, awscatalog.ErrInvalidQuery) {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if err != nil {
		permission := map[string]string{
			"rds-mysql": "rds:DescribeDBEngineVersions", "rds-postgres": "rds:DescribeDBEngineVersions", "aurora-mysql": "rds:DescribeDBEngineVersions",
			"documentdb": "rds:DescribeDBEngineVersions", "elasticache": "elasticache:DescribeCacheEngineVersions",
			"msk": "kafka:ListKafkaVersions", "amazon-mq": "mq:DescribeBrokerInstanceOptions",
		}[service]
		writeAWSCatalogError(w, err, "云服务引擎版本", permission)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"region": r.URL.Query().Get("region"), "service": service, "engine": r.URL.Query().Get("engine"), "versions": versions, "source": "aws-live"})
}

func (s *Server) getProjectAWSCredentials(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("project")
	if _, err := s.requireProjectView(r, project); err != nil {
		writeAccessError(w, err)
		return
	}
	if s.awsCredentials == nil {
		writeError(w, http.StatusServiceUnavailable, errors.New("AWS credential service is unavailable"))
		return
	}
	info, err := s.awsCredentials.Info(r.Context(), project)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, info)
}

func (s *Server) saveProjectAWSCredentials(w http.ResponseWriter, r *http.Request) {
	if !requirePlatformPermission(w, r, platformManageCredentials) {
		return
	}
	project := r.PathValue("project")
	if err := s.requireProjectConfigure(r, project); err != nil {
		writeAccessError(w, err)
		return
	}
	if s.awsCredentials == nil {
		writeError(w, http.StatusServiceUnavailable, errors.New("AWS credential service is unavailable"))
		return
	}
	var request struct {
		AccessKeyID     string `json:"access_key_id"`
		SecretAccessKey string `json:"secret_access_key"`
		SessionToken    string `json:"session_token"`
		Region          string `json:"region"`
		Password        string `json:"password"`
	}
	if err := decodeJSON(w, r, &request); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	session, _ := auth.SessionFromContext(r.Context())
	if strings.TrimSpace(request.Password) != "" {
		if err := s.authentication.ReauthenticateRequest(r, session.Username, request.Password); err != nil {
			request.SecretAccessKey, request.SessionToken, request.Password = "", "", ""
			if errors.Is(err, auth.ErrRateLimited) {
				writeError(w, http.StatusTooManyRequests, err)
				return
			}
			writeError(w, http.StatusUnauthorized, auth.ErrInvalidCredentials)
			return
		}
	}
	info, err := s.awsCredentials.SaveAndVerify(r.Context(), project, session.Username, awscredentials.Input{
		AccessKeyID: request.AccessKeyID, SecretAccessKey: request.SecretAccessKey,
		SessionToken: request.SessionToken, Region: request.Region,
	})
	request.SecretAccessKey, request.SessionToken, request.Password = "", "", ""
	if errors.Is(err, awscredentials.ErrInvalidCredential) || errors.Is(err, awscredentials.ErrVerificationFailed) {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, info)
}

func (s *Server) deleteProjectAWSCredentials(w http.ResponseWriter, r *http.Request) {
	if !requirePlatformPermission(w, r, platformManageCredentials) {
		return
	}
	project := r.PathValue("project")
	if err := s.requireProjectConfigure(r, project); err != nil {
		writeAccessError(w, err)
		return
	}
	if s.awsCredentials == nil {
		writeError(w, http.StatusServiceUnavailable, errors.New("AWS credential service is unavailable"))
		return
	}
	var request struct {
		Password string `json:"password"`
	}
	if err := decodeJSON(w, r, &request); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	session, _ := auth.SessionFromContext(r.Context())
	if strings.TrimSpace(request.Password) != "" {
		if err := s.authentication.ReauthenticateRequest(r, session.Username, request.Password); err != nil {
			request.Password = ""
			if errors.Is(err, auth.ErrRateLimited) {
				writeError(w, http.StatusTooManyRequests, err)
				return
			}
			writeError(w, http.StatusUnauthorized, auth.ErrInvalidCredentials)
			return
		}
	}
	request.Password = ""
	if err := s.awsCredentials.Delete(r.Context(), project); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) listAWSCredentials(w http.ResponseWriter, r *http.Request) {
	if s.awsCredentials == nil {
		writeError(w, http.StatusServiceUnavailable, errors.New("AWS credential service is unavailable"))
		return
	}
	items, err := s.awsCredentials.List(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	session, _ := auth.SessionFromContext(r.Context())
	projects, listErr := s.accessControl.ListProjects(r.Context(), session.Username, false)
	if listErr != nil {
		writeAccessError(w, listErr)
		return
	}
	items = visibleAWSCredentials(items, projects, session.IsAdmin || session.PlatformPermissions.CanManageCredentials)
	writeJSON(w, http.StatusOK, items)
}

func visibleAWSCredentials(items []awscredentials.PublicInfo, projects []access.Project, canManageAll bool) []awscredentials.PublicInfo {
	if canManageAll {
		return items
	}
	visible := make(map[string]bool, len(projects))
	for _, project := range projects {
		visible[project.Key] = true
	}
	filtered := make([]awscredentials.PublicInfo, 0, len(items))
	for _, item := range items {
		if visible[item.ProjectKey] && !item.ProjectArchived {
			filtered = append(filtered, item)
		}
	}
	return filtered
}

func (s *Server) createAWSCredential(w http.ResponseWriter, r *http.Request) {
	if !requirePlatformPermission(w, r, platformManageCredentials) {
		return
	}
	if s.awsCredentials == nil {
		writeError(w, http.StatusServiceUnavailable, errors.New("AWS credential service is unavailable"))
		return
	}
	var request struct {
		Key             string `json:"key"`
		DisplayName     string `json:"display_name"`
		ProjectKey      string `json:"project_key"`
		AccessKeyID     string `json:"access_key_id"`
		SecretAccessKey string `json:"secret_access_key"`
		SessionToken    string `json:"session_token"`
		Region          string `json:"region"`
		Password        string `json:"password"`
	}
	if err := decodeJSON(w, r, &request); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if err := s.requireProjectConfigure(r, request.ProjectKey); err != nil {
		request.SecretAccessKey, request.SessionToken, request.Password = "", "", ""
		writeAccessError(w, err)
		return
	}
	session, _ := auth.SessionFromContext(r.Context())
	if strings.TrimSpace(request.Password) != "" {
		if err := s.authentication.ReauthenticateRequest(r, session.Username, request.Password); err != nil {
			request.SecretAccessKey, request.SessionToken, request.Password = "", "", ""
			if errors.Is(err, auth.ErrRateLimited) {
				writeError(w, http.StatusTooManyRequests, err)
				return
			}
			writeError(w, http.StatusUnauthorized, auth.ErrInvalidCredentials)
			return
		}
	}
	info, err := s.awsCredentials.SaveNamedAndVerify(r.Context(), request.Key, request.DisplayName, request.ProjectKey, session.Username, awscredentials.Input{
		AccessKeyID: request.AccessKeyID, SecretAccessKey: request.SecretAccessKey,
		SessionToken: request.SessionToken, Region: request.Region,
	})
	request.SecretAccessKey, request.SessionToken, request.Password = "", "", ""
	if errors.Is(err, awscredentials.ErrInvalidCredential) || errors.Is(err, awscredentials.ErrCredentialMismatch) {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if errors.Is(err, awscredentials.ErrVerificationFailed) {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusCreated, info)
}

func (s *Server) deleteAWSCredential(w http.ResponseWriter, r *http.Request) {
	if !requirePlatformPermission(w, r, platformManageCredentials) {
		return
	}
	if s.awsCredentials == nil {
		writeError(w, http.StatusServiceUnavailable, errors.New("AWS credential service is unavailable"))
		return
	}
	credentialKey := r.PathValue("credential")
	items, err := s.awsCredentials.List(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	ownerProject := ""
	ownerArchived := false
	for _, item := range items {
		if item.Key == credentialKey {
			ownerProject = item.ProjectKey
			ownerArchived = item.ProjectArchived
			break
		}
	}
	if ownerProject == "" {
		writeError(w, http.StatusNotFound, os.ErrNotExist)
		return
	}
	if !ownerArchived {
		if err := s.requireProjectConfigure(r, ownerProject); err != nil {
			writeAccessError(w, err)
			return
		}
	}
	var request struct {
		Password string `json:"password"`
	}
	if err := decodeJSON(w, r, &request); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	session, _ := auth.SessionFromContext(r.Context())
	if strings.TrimSpace(request.Password) != "" {
		if err := s.authentication.ReauthenticateRequest(r, session.Username, request.Password); err != nil {
			request.Password = ""
			if errors.Is(err, auth.ErrRateLimited) {
				writeError(w, http.StatusTooManyRequests, err)
				return
			}
			writeError(w, http.StatusUnauthorized, auth.ErrInvalidCredentials)
			return
		}
	}
	request.Password = ""
	if err := s.awsCredentials.DeleteNamed(r.Context(), credentialKey); err != nil {
		if errors.Is(err, awscredentials.ErrInvalidCredential) {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) selectProjectAWSCredential(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("project")
	if err := s.requireProjectConfigure(r, project); err != nil {
		writeAccessError(w, err)
		return
	}
	if s.awsCredentials == nil {
		writeError(w, http.StatusServiceUnavailable, errors.New("AWS credential service is unavailable"))
		return
	}
	var request struct {
		CredentialKey string `json:"credential_key"`
	}
	if err := decodeJSON(w, r, &request); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	info, err := s.awsCredentials.Select(r.Context(), project, request.CredentialKey)
	if errors.Is(err, os.ErrNotExist) {
		writeError(w, http.StatusNotFound, err)
		return
	}
	if errors.Is(err, awscredentials.ErrCredentialMismatch) {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, info)
}

func (s *Server) listHelmComponents(w http.ResponseWriter, r *http.Request) {
	if s.componentCatalog == nil {
		writeError(w, http.StatusServiceUnavailable, errors.New("component catalog service is unavailable"))
		return
	}
	items, err := s.componentCatalog.List(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, items)
}

func (s *Server) createHelmComponent(w http.ResponseWriter, r *http.Request) {
	s.saveHelmComponent(w, r, "")
}

func (s *Server) updateHelmComponent(w http.ResponseWriter, r *http.Request) {
	s.saveHelmComponent(w, r, r.PathValue("component"))
}

func (s *Server) saveHelmComponent(w http.ResponseWriter, r *http.Request, pathKey string) {
	if !requirePlatformPermission(w, r, platformManageComponents) {
		return
	}
	if s.componentCatalog == nil {
		writeError(w, http.StatusServiceUnavailable, errors.New("component catalog service is unavailable"))
		return
	}
	var component componentcatalog.Component
	if err := decodeJSON(w, r, &component); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if pathKey != "" && component.Key != pathKey {
		writeError(w, http.StatusBadRequest, errors.New("component key cannot be changed"))
		return
	}
	session, _ := auth.SessionFromContext(r.Context())
	component.CreatedBy = session.Username
	item, err := s.componentCatalog.Save(r.Context(), component)
	if errors.Is(err, componentcatalog.ErrInvalidComponent) || errors.Is(err, componentcatalog.ErrInlineSecret) {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if errors.Is(err, componentcatalog.ErrReservedComponent) {
		writeError(w, http.StatusConflict, err)
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	status := http.StatusOK
	if pathKey == "" {
		status = http.StatusCreated
	}
	writeJSON(w, status, item)
}

func (s *Server) deleteHelmComponent(w http.ResponseWriter, r *http.Request) {
	if !requirePlatformPermission(w, r, platformManageComponents) {
		return
	}
	if s.componentCatalog == nil {
		writeError(w, http.StatusServiceUnavailable, errors.New("component catalog service is unavailable"))
		return
	}
	if err := s.componentCatalog.Delete(r.Context(), r.PathValue("component")); err != nil {
		if errors.Is(err, componentcatalog.ErrInvalidComponent) {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		if errors.Is(err, componentcatalog.ErrReservedComponent) {
			writeError(w, http.StatusConflict, err)
			return
		}
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) inspectHelmComponent(w http.ResponseWriter, r *http.Request) {
	if !requirePlatformPermission(w, r, platformManageComponents) {
		return
	}
	if s.componentCatalog == nil {
		writeError(w, http.StatusServiceUnavailable, errors.New("component catalog service is unavailable"))
		return
	}
	var request componentcatalog.InspectRequest
	if err := decodeJSON(w, r, &request); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	result, err := s.componentCatalog.Inspect(r.Context(), request)
	if errors.Is(err, componentcatalog.ErrInvalidComponent) {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if errors.Is(err, componentcatalog.ErrHelmInspect) {
		writeError(w, http.StatusFailedDependency, errors.New("Helm Chart 查询失败，请检查仓库地址、Chart 名称、版本和本机网络后重试"))
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) listHelmComponentVersions(w http.ResponseWriter, r *http.Request) {
	if s.componentCatalog == nil {
		writeError(w, http.StatusServiceUnavailable, errors.New("component catalog service is unavailable"))
		return
	}
	var request componentcatalog.InspectRequest
	if err := decodeJSON(w, r, &request); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	result, err := s.componentCatalog.Versions(r.Context(), request)
	if errors.Is(err, componentcatalog.ErrInvalidComponent) {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if errors.Is(err, componentcatalog.ErrHelmInspect) {
		writeError(w, http.StatusFailedDependency, errors.New("Helm 版本查询失败，请检查仓库地址、Chart 名称和平台网络后重试"))
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) health(w http.ResponseWriter, r *http.Request) {
	dependencies := map[string]string{}
	status := http.StatusOK
	state := "ok"
	if s.healthChecker != nil {
		ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
		defer cancel()
		var err error
		dependencies, err = s.healthChecker.Health(ctx)
		if err != nil {
			status = http.StatusServiceUnavailable
			state = "degraded"
		}
	}
	if s.jobs != nil {
		if err := s.jobs.CheckStorage(); err != nil {
			dependencies["job_log_storage"] = "down"
			status = http.StatusServiceUnavailable
			state = "degraded"
		} else {
			dependencies["job_log_storage"] = "up"
		}
	}
	writeJSON(w, status, map[string]any{
		"status": state, "version": Version, "time": time.Now().UTC(), "dependencies": dependencies,
	})
}

func (s *Server) platform(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"version":                 Version,
		"components":              s.config.Components,
		"environment_definitions": access.EnvironmentDefinitions,
		"auth_required":           true,
		"aws_profile":             "",
		"max_parallel":            s.config.Jobs.MaxParallel,
		"aws_regions":             awsRegions,
	})
}

var awsRegions = []map[string]any{
	{"code": "ap-south-1", "name": "亚太地区（孟买）", "availability_zones": 3, "opt_in": false},
	{"code": "ap-south-2", "name": "亚太地区（海得拉巴）", "availability_zones": 3, "opt_in": true},
	{"code": "ap-northeast-1", "name": "亚太地区（东京）", "availability_zones": 4, "opt_in": false},
	{"code": "ap-northeast-2", "name": "亚太地区（首尔）", "availability_zones": 4, "opt_in": false},
	{"code": "ap-northeast-3", "name": "亚太地区（大阪）", "availability_zones": 3, "opt_in": false},
	{"code": "ap-southeast-1", "name": "亚太地区（新加坡）", "availability_zones": 3, "opt_in": false},
	{"code": "ap-southeast-2", "name": "亚太地区（悉尼）", "availability_zones": 3, "opt_in": false},
	{"code": "ap-southeast-3", "name": "亚太地区（雅加达）", "availability_zones": 3, "opt_in": true},
	{"code": "ap-southeast-4", "name": "亚太地区（墨尔本）", "availability_zones": 3, "opt_in": true},
	{"code": "ap-southeast-5", "name": "亚太地区（马来西亚）", "availability_zones": 3, "opt_in": true},
	{"code": "ap-southeast-6", "name": "亚太地区（新西兰）", "availability_zones": 3, "opt_in": true},
	{"code": "ap-southeast-7", "name": "亚太地区（泰国）", "availability_zones": 3, "opt_in": true},
	{"code": "ap-east-1", "name": "亚太地区（香港）", "availability_zones": 3, "opt_in": true},
	{"code": "ap-east-2", "name": "亚太地区（台北）", "availability_zones": 3, "opt_in": true},
	{"code": "us-east-1", "name": "美国东部（弗吉尼亚北部）", "availability_zones": 6, "opt_in": false},
	{"code": "us-east-2", "name": "美国东部（俄亥俄）", "availability_zones": 3, "opt_in": false},
	{"code": "us-west-1", "name": "美国西部（加利福尼亚北部）", "availability_zones": 3, "opt_in": false},
	{"code": "us-west-2", "name": "美国西部（俄勒冈）", "availability_zones": 4, "opt_in": false},
	{"code": "eu-central-1", "name": "欧洲（法兰克福）", "availability_zones": 3, "opt_in": false},
	{"code": "eu-west-1", "name": "欧洲（爱尔兰）", "availability_zones": 3, "opt_in": false},
	{"code": "eu-west-2", "name": "欧洲（伦敦）", "availability_zones": 3, "opt_in": false},
	{"code": "eu-west-3", "name": "欧洲（巴黎）", "availability_zones": 3, "opt_in": false},
	{"code": "eu-north-1", "name": "欧洲（斯德哥尔摩）", "availability_zones": 3, "opt_in": false},
	{"code": "ca-central-1", "name": "加拿大（中部）", "availability_zones": 3, "opt_in": false},
	{"code": "sa-east-1", "name": "南美洲（圣保罗）", "availability_zones": 3, "opt_in": false},
}

func (s *Server) listProjects(w http.ResponseWriter, r *http.Request) {
	session, _ := auth.SessionFromContext(r.Context())
	projects, err := s.accessControl.ListProjects(r.Context(), session.Username, session.IsAdmin)
	if err != nil {
		writeAccessError(w, err)
		return
	}
	s.enrichProjectEnvironmentLifecycles(r.Context(), projects)
	writeJSON(w, http.StatusOK, projects)
}

func (s *Server) createProject(w http.ResponseWriter, r *http.Request) {
	if !requirePlatformPermission(w, r, platformManageProjects) {
		return
	}
	var project access.Project
	if err := decodeJSON(w, r, &project); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	keySource := project.Key
	if strings.TrimSpace(keySource) == "" {
		keySource = project.DisplayName
	}
	project.Key = access.NormalizeProjectKey(keySource)
	project.Environments = nil
	project.Permission = access.Permission{}
	if _, err := s.accessControl.GetProject(r.Context(), project.Key); err == nil {
		writeError(w, http.StatusConflict, errors.New("project already exists"))
		return
	} else if !errors.Is(err, os.ErrNotExist) {
		writeAccessError(w, err)
		return
	}
	if err := s.accessControl.SaveProject(r.Context(), project); err != nil {
		writeAccessError(w, err)
		return
	}
	session, _ := auth.SessionFromContext(r.Context())
	if err := s.accessControl.SavePermission(r.Context(), access.Permission{
		ProjectKey: project.Key, Username: session.Username, CanView: true,
		CanDeploy: true, CanConfigure: true, CanViewSecrets: true,
	}); err != nil {
		_ = s.accessControl.DeleteProject(r.Context(), project.Key)
		writeAccessError(w, err)
		return
	}
	created, _ := s.accessControl.GetProject(r.Context(), project.Key)
	created.Permission, _ = s.accessControl.Permission(r.Context(), session.Username, false, project.Key)
	writeJSON(w, http.StatusCreated, created)
}

func (s *Server) getProject(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("project")
	if _, err := s.requireProjectView(r, project); err != nil {
		writeAccessError(w, err)
		return
	}
	item, err := s.accessControl.GetProject(r.Context(), project)
	if err != nil {
		writeAccessError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, item)
}

func (s *Server) updateProject(w http.ResponseWriter, r *http.Request) {
	if !requirePlatformPermission(w, r, platformManageProjects) {
		return
	}
	if err := s.requireProjectConfigure(r, r.PathValue("project")); err != nil {
		writeAccessError(w, err)
		return
	}
	var project access.Project
	if err := decodeJSON(w, r, &project); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	project.Key = r.PathValue("project")
	project.Environments = nil
	project.Permission = access.Permission{}
	if err := s.accessControl.SaveProject(r.Context(), project); err != nil {
		writeAccessError(w, err)
		return
	}
	updated, err := s.accessControl.GetProject(r.Context(), project.Key)
	if err != nil {
		writeAccessError(w, err)
		return
	}
	session, _ := auth.SessionFromContext(r.Context())
	updated.Permission, _ = s.accessControl.Permission(r.Context(), session.Username, session.IsAdmin, project.Key)
	writeJSON(w, http.StatusOK, updated)
}

func (s *Server) deleteProject(w http.ResponseWriter, r *http.Request) {
	if !requirePlatformPermission(w, r, platformManageProjects) {
		return
	}
	projectKey := r.PathValue("project")
	if err := s.requireProjectConfigure(r, projectKey); err != nil {
		writeAccessError(w, err)
		return
	}
	var request struct {
		Confirm string `json:"confirm"`
	}
	if err := decodeJSON(w, r, &request); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if request.Confirm != projectKey {
		writeError(w, http.StatusBadRequest, errors.New("project confirmation does not match"))
		return
	}
	if s.jobs.HasActiveProject(projectKey) {
		writeError(w, http.StatusConflict, errors.New("project has an active deployment job"))
		return
	}
	if s.cicd != nil {
		active, err := s.cicd.HasActiveBuilds(r.Context(), projectKey)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		if active {
			writeError(w, http.StatusConflict, errors.New("项目仍有执行中的 Jenkins 构建，请先等待完成或停止构建"))
			return
		}
	}
	project, err := s.accessControl.GetProject(r.Context(), projectKey)
	if err != nil {
		writeAccessError(w, err)
		return
	}
	if blockers := s.projectDeletionBlockers(r.Context(), project); len(blockers) > 0 {
		writeError(w, http.StatusConflict, errors.New("项目仍有未清理的环境资源，不能删除："+strings.Join(blockers, "；")))
		return
	}
	if s.gitlab != nil {
		blocker, err := s.gitlab.ProjectDeletionBlocker(r.Context(), projectKey)
		if err != nil {
			writeGitLabError(w, err)
			return
		}
		if blocker != "" {
			writeError(w, http.StatusConflict, errors.New(blocker))
			return
		}
		// A saved configuration that never created remote repositories is safe to
		// detach automatically; no external GitLab resource is touched.
		if err := s.gitlab.DetachDelivery(r.Context(), projectKey); err != nil {
			writeGitLabError(w, err)
			return
		}
	}
	for _, item := range project.Environments {
		if err := s.environments.Delete(item.TargetName); err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		s.status.Invalidate(r.Context(), item.TargetName)
	}
	// Remove terminal jobs before the project permission boundary disappears;
	// otherwise their database rows, Redis cache entries and local logs become
	// inaccessible orphaned history after project deletion.
	if _, err := s.jobs.DeleteHistory(projectKey, ""); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if s.cicd != nil {
		if err := s.cicd.DeleteProjectData(r.Context(), projectKey); err != nil {
			writeCICDError(w, err)
			return
		}
	}
	if err := s.accessControl.DeleteProject(r.Context(), projectKey); err != nil {
		writeAccessError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) createProjectEnvironment(w http.ResponseWriter, r *http.Request) {
	projectKey := r.PathValue("project")
	if err := s.requireProjectConfigure(r, projectKey); err != nil {
		writeAccessError(w, err)
		return
	}
	var request struct {
		Environment       string `json:"environment"`
		SourceProject     string `json:"source_project"`
		SourceEnvironment string `json:"source_environment"`
		TargetType        string `json:"target_type"`
		ExistingCluster   string `json:"existing_cluster_name"`
		Region            string `json:"region"`
	}
	if err := decodeJSON(w, r, &request); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if !access.ValidEnvironment(request.Environment) {
		writeError(w, http.StatusBadRequest, access.ErrInvalidEnvironment)
		return
	}
	if strings.TrimSpace(request.TargetType) == environment.TargetExistingEKS {
		if err := s.requireBoundProjectAWSCredential(r.Context(), projectKey); err != nil {
			writeProjectAWSCredentialError(w, err)
			return
		}
	}
	if _, err := s.accessControl.Environment(r.Context(), projectKey, request.Environment); err == nil {
		writeError(w, http.StatusConflict, errors.New("project environment already exists"))
		return
	}
	targetName := projectKey + "-" + request.Environment
	var doc environment.Document
	var err error
	if request.SourceProject == "" && request.SourceEnvironment == "" {
		doc = environment.DefaultDocument(projectKey, request.Environment)
		if strings.TrimSpace(request.Region) != "" {
			err = environment.ConfigureRegion(doc, request.Region)
		}
		if err == nil {
			err = s.normalizeComponentSources(r.Context(), doc, projectKey, request.Environment)
		}
		if err == nil {
			err = environment.ConfigureTarget(doc, request.TargetType)
		}
		if err == nil {
			environment.ConfigureNewEnvironmentScheduling(doc)
		}
		if err == nil && environment.IsExistingEKS(doc) {
			doc["deployment_target"].(map[string]any)["cluster_name"] = strings.TrimSpace(request.ExistingCluster)
		}
		if err == nil {
			err = s.environments.Save(targetName, doc)
		}
	} else {
		if request.SourceProject == "" || request.SourceEnvironment == "" {
			writeError(w, http.StatusBadRequest, errors.New("source project and environment must be provided together"))
			return
		}
		if _, err = s.requireProjectView(r, request.SourceProject); err != nil {
			writeAccessError(w, err)
			return
		}
		var source access.ProjectEnvironment
		source, err = s.accessControl.Environment(r.Context(), request.SourceProject, request.SourceEnvironment)
		if err == nil {
			doc, err = s.environments.Load(source.TargetName)
		}
		if err == nil {
			doc = environment.ApplyDefaults(doc, projectKey, request.Environment)
			doc["project"] = projectKey
			doc["environment"] = request.Environment
			if strings.TrimSpace(request.Region) != "" {
				err = environment.ConfigureRegion(doc, request.Region)
			}
		}
		if err == nil {
			sensitive.Sanitize(map[string]any(doc))
			err = s.normalizeComponentSources(r.Context(), doc, projectKey, request.Environment)
		}
		if err == nil {
			err = environment.ConfigureTarget(doc, request.TargetType)
		}
		if err == nil {
			environment.ConfigureNewEnvironmentScheduling(doc)
		}
		if err == nil && environment.IsExistingEKS(doc) {
			doc["deployment_target"].(map[string]any)["cluster_name"] = strings.TrimSpace(request.ExistingCluster)
		}
		if err == nil {
			err = s.environments.Save(targetName, doc)
		}
	}
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	item := access.ProjectEnvironment{
		ProjectKey: projectKey, Environment: request.Environment, TargetName: targetName,
		Region: documentString(doc, "region"),
	}
	if err := s.accessControl.SaveEnvironment(r.Context(), item); err != nil {
		_ = s.environments.Delete(targetName)
		writeAccessError(w, err)
		return
	}
	created, _ := s.accessControl.Environment(r.Context(), projectKey, request.Environment)
	writeJSON(w, http.StatusCreated, created)
}

func (s *Server) getProjectEnvironment(w http.ResponseWriter, r *http.Request) {
	projectKey, environmentKey := r.PathValue("project"), r.PathValue("environment")
	if _, err := s.requireProjectView(r, projectKey); err != nil {
		writeAccessError(w, err)
		return
	}
	item, err := s.accessControl.Environment(r.Context(), projectKey, environmentKey)
	if err != nil {
		writeAccessError(w, err)
		return
	}
	doc, err := s.environments.Load(item.TargetName)
	if err != nil {
		writeAccessError(w, err)
		return
	}
	doc = environment.ApplyDefaults(doc, projectKey, environmentKey)
	sensitive.Sanitize(map[string]any(doc))
	if err := s.normalizeComponentSources(r.Context(), doc, projectKey, environmentKey); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, doc)
}

func (s *Server) listEnvironmentTLSCertificates(w http.ResponseWriter, r *http.Request) {
	projectKey, environmentKey := r.PathValue("project"), r.PathValue("environment")
	if _, err := s.requireProjectView(r, projectKey); err != nil {
		writeAccessError(w, err)
		return
	}
	if s.tlsCertificates == nil {
		writeError(w, http.StatusServiceUnavailable, errors.New("TLS certificate service is unavailable"))
		return
	}
	if _, err := s.accessControl.Environment(r.Context(), projectKey, environmentKey); err != nil {
		writeAccessError(w, err)
		return
	}
	items, err := s.tlsCertificates.List(r.Context(), projectKey, environmentKey)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, items)
}

func (s *Server) saveEnvironmentTLSCertificate(w http.ResponseWriter, r *http.Request) {
	projectKey, environmentKey, certificateKey := r.PathValue("project"), r.PathValue("environment"), r.PathValue("certificate")
	if err := s.requireProjectConfigure(r, projectKey); err != nil {
		writeAccessError(w, err)
		return
	}
	if s.tlsCertificates == nil {
		writeError(w, http.StatusServiceUnavailable, errors.New("TLS certificate service is unavailable"))
		return
	}
	if _, err := s.accessControl.Environment(r.Context(), projectKey, environmentKey); err != nil {
		writeAccessError(w, err)
		return
	}
	var input tlscertificates.Input
	if err := decodeJSON(w, r, &input); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	session, ok := auth.SessionFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, auth.ErrUnauthenticated)
		return
	}
	info, err := s.tlsCertificates.Save(r.Context(), projectKey, environmentKey, certificateKey, session.Username, input)
	input.CertificatePEM, input.PrivateKeyPEM = "", ""
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, info)
}

func (s *Server) deleteEnvironmentTLSCertificate(w http.ResponseWriter, r *http.Request) {
	projectKey, environmentKey, certificateKey := r.PathValue("project"), r.PathValue("environment"), r.PathValue("certificate")
	if err := s.requireProjectConfigure(r, projectKey); err != nil {
		writeAccessError(w, err)
		return
	}
	if s.tlsCertificates == nil {
		writeError(w, http.StatusServiceUnavailable, errors.New("TLS certificate service is unavailable"))
		return
	}
	if _, err := s.accessControl.Environment(r.Context(), projectKey, environmentKey); err != nil {
		writeAccessError(w, err)
		return
	}
	if err := s.tlsCertificates.Delete(r.Context(), projectKey, environmentKey, certificateKey); errors.Is(err, os.ErrNotExist) {
		writeError(w, http.StatusNotFound, err)
		return
	} else if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) listEnvironmentDataServiceCredentials(w http.ResponseWriter, r *http.Request) {
	projectKey, environmentKey := r.PathValue("project"), r.PathValue("environment")
	if _, err := s.requireProjectView(r, projectKey); err != nil {
		writeAccessError(w, err)
		return
	}
	if s.dataServiceCredentials == nil {
		writeError(w, http.StatusServiceUnavailable, errors.New("data service credential service is unavailable"))
		return
	}
	if _, err := s.accessControl.Environment(r.Context(), projectKey, environmentKey); err != nil {
		writeAccessError(w, err)
		return
	}
	items, err := s.dataServiceCredentials.List(r.Context(), projectKey, environmentKey)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, items)
}

func (s *Server) saveEnvironmentDataServiceCredential(w http.ResponseWriter, r *http.Request) {
	projectKey, environmentKey, serviceKey := r.PathValue("project"), r.PathValue("environment"), r.PathValue("service")
	if err := s.requireProjectConfigure(r, projectKey); err != nil {
		writeAccessError(w, err)
		return
	}
	if s.dataServiceCredentials == nil {
		writeError(w, http.StatusServiceUnavailable, errors.New("data service credential service is unavailable"))
		return
	}
	if _, err := s.accessControl.Environment(r.Context(), projectKey, environmentKey); err != nil {
		writeAccessError(w, err)
		return
	}
	var input dataservicecredentials.Input
	if err := decodeJSON(w, r, &input); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	session, ok := auth.SessionFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, auth.ErrUnauthenticated)
		return
	}
	info, err := s.dataServiceCredentials.Save(r.Context(), projectKey, environmentKey, serviceKey, session.Username, input)
	input.Password = ""
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, info)
}

func (s *Server) deleteEnvironmentDataServiceCredential(w http.ResponseWriter, r *http.Request) {
	projectKey, environmentKey, serviceKey := r.PathValue("project"), r.PathValue("environment"), r.PathValue("service")
	if err := s.requireProjectConfigure(r, projectKey); err != nil {
		writeAccessError(w, err)
		return
	}
	if s.dataServiceCredentials == nil {
		writeError(w, http.StatusServiceUnavailable, errors.New("data service credential service is unavailable"))
		return
	}
	if _, err := s.accessControl.Environment(r.Context(), projectKey, environmentKey); err != nil {
		writeAccessError(w, err)
		return
	}
	if err := s.dataServiceCredentials.Delete(r.Context(), projectKey, environmentKey, serviceKey); errors.Is(err, os.ErrNotExist) {
		writeError(w, http.StatusNotFound, err)
		return
	} else if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) saveProjectEnvironment(w http.ResponseWriter, r *http.Request) {
	projectKey, environmentKey := r.PathValue("project"), r.PathValue("environment")
	if err := s.requireProjectConfigure(r, projectKey); err != nil {
		writeAccessError(w, err)
		return
	}
	item, err := s.accessControl.Environment(r.Context(), projectKey, environmentKey)
	if err != nil {
		writeAccessError(w, err)
		return
	}
	currentDoc, err := s.environments.Load(item.TargetName)
	if err != nil {
		writeAccessError(w, err)
		return
	}
	var doc environment.Document
	if err := decodeJSON(w, r, &doc); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	doc["project"] = projectKey
	doc["environment"] = environmentKey
	doc = environment.ApplyDefaults(doc, projectKey, environmentKey)
	currentDoc = environment.ApplyDefaults(currentDoc, projectKey, environmentKey)
	if environment.TargetType(doc) != environment.TargetType(currentDoc) {
		writeError(w, http.StatusConflict, errors.New("环境部署目标创建后不可切换；请先卸载资源并重新创建环境"))
		return
	}
	if environment.IsExistingEKS(currentDoc) && (environment.ClusterName(doc) != environment.ClusterName(currentDoc) || documentString(doc, "region") != documentString(currentDoc, "region")) {
		writeError(w, http.StatusConflict, errors.New("已有 EKS 的 Region 和集群名称创建后不可修改；如选择错误，请删除未部署的环境配置后重新创建"))
		return
	}
	if err := validateNodeGroupPlanningChange(
		currentDoc,
		doc,
		s.phaseOneAlreadyDeployed(r.Context(), projectKey, environmentKey, item.TargetName, currentDoc),
	); err != nil {
		writeError(w, http.StatusConflict, err)
		return
	}
	removedNamespaces := removedNamespaceNames(currentDoc, doc)
	if len(removedNamespaces) > 0 {
		writeError(w, http.StatusConflict, namespaceRemovalError(removedNamespaces))
		return
	}
	if err := s.normalizeComponentSources(r.Context(), doc, projectKey, environmentKey); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if err := s.environments.Save(item.TargetName, doc); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	// Remove pasted certificate material only after the environment document is
	// committed. This keeps editing an unsaved draft from breaking the currently
	// deployed configuration while still preventing orphaned encrypted keys.
	if err := s.cleanupUnusedTLSMaterials(r.Context(), projectKey, environmentKey, doc); err != nil {
		log.Printf("cleanup unused TLS material for %s/%s: %v", projectKey, environmentKey, err)
	}
	item.Region = documentString(doc, "region")
	_ = s.accessControl.SaveEnvironment(r.Context(), item)
	s.status.Invalidate(r.Context(), item.TargetName)
	writeJSON(w, http.StatusOK, doc)
}

func (s *Server) cleanupUnusedTLSMaterials(ctx context.Context, projectKey, environmentKey string, doc environment.Document) error {
	if s.tlsCertificates == nil {
		return nil
	}
	wanted := make(map[string]struct{})
	tlsConfig, _ := doc["tls"].(map[string]any)
	certificates, _ := tlsConfig["certificates"].([]any)
	for _, raw := range certificates {
		certificate, _ := raw.(map[string]any)
		if strings.TrimSpace(fmt.Sprint(certificate["mode"])) != "uploaded-pem" {
			continue
		}
		key := strings.TrimSpace(fmt.Sprint(certificate["material_ref"]))
		if key == "" {
			key = strings.TrimSpace(fmt.Sprint(certificate["key"]))
		}
		if key != "" {
			wanted[key] = struct{}{}
		}
	}
	stored, err := s.tlsCertificates.List(ctx, projectKey, environmentKey)
	if err != nil {
		return err
	}
	for _, certificate := range stored {
		if _, ok := wanted[certificate.Key]; ok {
			continue
		}
		if err := s.tlsCertificates.Delete(ctx, projectKey, environmentKey, certificate.Key); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	return nil
}

func (s *Server) deleteProjectEnvironment(w http.ResponseWriter, r *http.Request) {
	projectKey, environmentKey := r.PathValue("project"), r.PathValue("environment")
	if err := s.requireProjectConfigure(r, projectKey); err != nil {
		writeAccessError(w, err)
		return
	}
	var request struct {
		Confirm          string `json:"confirm"`
		DestroyResources bool   `json:"destroy_resources"`
		DestroyConfirm   string `json:"destroy_confirm"`
		Password         string `json:"password"`
	}
	if err := decodeJSON(w, r, &request); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if request.Confirm != projectKey+"/"+environmentKey {
		writeError(w, http.StatusBadRequest, errors.New("project environment confirmation does not match"))
		return
	}
	if s.jobs.HasActiveEnvironment(projectKey, environmentKey) {
		writeError(w, http.StatusConflict, errors.New("environment has an active deployment job"))
		return
	}
	item, err := s.accessControl.Environment(r.Context(), projectKey, environmentKey)
	if err != nil {
		writeAccessError(w, err)
		return
	}
	if request.DestroyResources {
		if err := s.requireProjectDeploy(r, projectKey); err != nil {
			request.Password = ""
			writeAccessError(w, err)
			return
		}
		if err := s.requireBoundProjectAWSCredential(r.Context(), projectKey); err != nil {
			request.Password = ""
			writeProjectAWSCredentialError(w, err)
			return
		}
		if request.DestroyConfirm != "destroy:"+projectKey+"/"+environmentKey {
			request.Password = ""
			writeError(w, http.StatusBadRequest, errors.New("销毁确认内容不匹配"))
			return
		}
		session, _ := auth.SessionFromContext(r.Context())
		err := s.authentication.ReauthenticateRequest(r, session.Username, request.Password)
		request.Password = ""
		if errors.Is(err, auth.ErrRateLimited) {
			writeError(w, http.StatusTooManyRequests, err)
			return
		}
		if err != nil {
			writeError(w, http.StatusUnauthorized, auth.ErrInvalidCredentials)
			return
		}
		job, err := s.jobs.SubmitWithCompletion(projectKey, environmentKey, item.TargetName, session.Username, jobs.ActionDestroy, jobs.CompletionDeleteEnvironment)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		writeJSON(w, http.StatusAccepted, job)
		return
	}
	request.Password = ""
	if blockers := s.projectDeletionBlockers(r.Context(), access.Project{Key: projectKey, Environments: []access.ProjectEnvironment{item}}); len(blockers) > 0 {
		writeError(w, http.StatusConflict, errors.New("环境仍有未清理资源，不能直接删除配置；请勾选“先销毁已部署资源”后再操作："+strings.Join(blockers, "；")))
		return
	}
	if err := s.removeEnvironmentConfiguration(r.Context(), item); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) completeJob(ctx context.Context, job jobs.Job) error {
	if job.CompletionAction != jobs.CompletionDeleteEnvironment {
		return nil
	}
	if job.Action != jobs.ActionDestroy {
		return errors.New("只有销毁任务可以在成功后自动删除环境")
	}
	item, err := s.accessControl.Environment(ctx, job.Project, job.Environment)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if blockers := s.environmentStateBlockers(job.Project, item); len(blockers) > 0 {
		return errors.New(strings.Join(blockers, "；"))
	}
	return s.removeEnvironmentConfiguration(ctx, item)
}

func (s *Server) removeEnvironmentConfiguration(ctx context.Context, item access.ProjectEnvironment) error {
	doc, err := s.environments.Load(item.TargetName)
	if err != nil {
		return fmt.Errorf("读取待删除环境配置: %w", err)
	}
	if err := s.environments.Delete(item.TargetName); err != nil {
		return fmt.Errorf("删除环境部署配置: %w", err)
	}
	if err := s.accessControl.DeleteEnvironment(ctx, item.ProjectKey, item.Environment); err != nil {
		if rollbackErr := s.environments.Save(item.TargetName, doc); rollbackErr != nil {
			return errors.Join(fmt.Errorf("删除环境记录: %w", err), fmt.Errorf("回滚环境配置: %w", rollbackErr))
		}
		return fmt.Errorf("删除环境记录: %w", err)
	}
	if s.status != nil {
		s.status.Invalidate(ctx, item.TargetName)
	}
	return nil
}

func (s *Server) listJobs(w http.ResponseWriter, r *http.Request) {
	projectKey := r.URL.Query().Get("project")
	environmentKey := r.URL.Query().Get("environment")
	if projectKey == "" {
		writeError(w, http.StatusBadRequest, errors.New("project query parameter is required"))
		return
	}
	if _, err := s.requireProjectView(r, projectKey); err != nil {
		writeAccessError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, s.jobs.List(projectKey, environmentKey))
}

func (s *Server) deleteJobHistory(w http.ResponseWriter, r *http.Request) {
	projectKey := r.URL.Query().Get("project")
	environmentKey := r.URL.Query().Get("environment")
	if projectKey == "" {
		writeError(w, http.StatusBadRequest, errors.New("project query parameter is required"))
		return
	}
	if err := s.requireProjectDeploy(r, projectKey); err != nil {
		writeAccessError(w, err)
		return
	}
	if environmentKey != "" {
		if _, err := s.accessControl.Environment(r.Context(), projectKey, environmentKey); err != nil {
			writeAccessError(w, err)
			return
		}
	}
	deleted, err := s.jobs.DeleteHistory(projectKey, environmentKey)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]int{"deleted": deleted})
}

func (s *Server) createJob(w http.ResponseWriter, r *http.Request) {
	var request struct {
		Project     string      `json:"project"`
		Environment string      `json:"environment"`
		Action      jobs.Action `json:"action"`
		Confirm     string      `json:"confirm"`
		Password    string      `json:"password"`
	}
	if err := decodeJSON(w, r, &request); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if request.Action == jobs.ActionStorageExpand || request.Action == jobs.ActionStorageShrink {
		writeError(w, http.StatusBadRequest, errors.New("存储扩缩容必须从日志平台存储管理页面发起"))
		return
	}
	if err := s.requireProjectDeploy(r, request.Project); err != nil {
		writeAccessError(w, err)
		return
	}
	item, err := s.accessControl.Environment(r.Context(), request.Project, request.Environment)
	if err != nil {
		writeAccessError(w, err)
		return
	}
	if err := s.requireBoundProjectAWSCredential(r.Context(), request.Project); err != nil {
		writeProjectAWSCredentialError(w, err)
		return
	}
	doc, err := s.environments.Load(item.TargetName)
	if err != nil {
		writeError(w, http.StatusBadRequest, errors.New("环境部署配置不存在或无法读取"))
		return
	}
	if environment.IsExistingEKS(doc) && (request.Action == jobs.ActionPlan || request.Action == jobs.ActionDeploy) {
		writeError(w, http.StatusConflict, errors.New("该环境接入已有 EKS，不执行阶段 1；请保存配置并执行组件与接入部署"))
		return
	}
	if request.Action == jobs.ActionDeploy {
		if s.resources == nil {
			writeError(w, http.StatusServiceUnavailable, errors.New("AWS 实际配置同步服务不可用，平台已停止阶段1部署"))
			return
		}
		if resetErr := s.resetMissingCloudBaselineAfterDestroy(r.Context(), request.Project, request.Environment); resetErr != nil {
			writeError(w, http.StatusFailedDependency, fmt.Errorf("无法重置已销毁环境的 AWS 配置基线: %w", resetErr))
			return
		}
		preflightCtx, cancel := contextWithTimeout(r, 90*time.Second)
		snapshot, refreshErr := s.resources.RefreshCloudConfiguration(preflightCtx, request.Project, request.Environment, item.TargetName)
		cancel()
		if refreshErr != nil {
			writeError(w, http.StatusFailedDependency, fmt.Errorf("部署前读取 AWS 实际配置失败，平台不会使用过期参数继续执行: %w", refreshErr))
			return
		}
		if driftErr := snapshot.CloudDeploymentPreflightError(); driftErr != nil {
			writeError(w, http.StatusConflict, driftErr)
			return
		}
	}
	requiredConfirmation := ""
	switch request.Action {
	case jobs.ActionDeploy, jobs.ActionPlatform, jobs.ActionAccess, jobs.ActionTLS:
		requiredConfirmation = request.Project + "/" + request.Environment
	case jobs.ActionDestroy:
		requiredConfirmation = "destroy:" + request.Project + "/" + request.Environment
	}
	if requiredConfirmation != "" && request.Confirm != requiredConfirmation {
		writeError(w, http.StatusBadRequest, errors.New("deployment confirmation does not match"))
		return
	}
	if request.Action == jobs.ActionDestroy {
		session, _ := auth.SessionFromContext(r.Context())
		err := s.authentication.ReauthenticateRequest(r, session.Username, request.Password)
		request.Password = ""
		if errors.Is(err, auth.ErrRateLimited) {
			writeError(w, http.StatusTooManyRequests, err)
			return
		}
		if err != nil {
			writeError(w, http.StatusUnauthorized, auth.ErrInvalidCredentials)
			return
		}
	}
	if request.Action == jobs.ActionPlatform || request.Action == jobs.ActionAccess || request.Action == jobs.ActionTLS {
		report, statusErr := s.status.CollectFresh(r.Context(), item.TargetName)
		if statusErr != nil {
			writeError(w, http.StatusFailedDependency, errors.New("无法验证 EKS 状态，请检查项目 AWS 凭据、Region 和集群 API 访问配置"))
			return
		}
		if !report.Cluster.Reachable {
			if environment.IsExistingEKS(doc) {
				writeError(w, http.StatusConflict, errors.New("已有 EKS 接入检查未通过：请确认集群为 ACTIVE，并为当前 AWS 身份配置 EKS Access Entry 或 aws-auth 集群管理权限"))
			} else {
				writeError(w, http.StatusConflict, errors.New("阶段 2 需要 EKS 已正常运行，请先完成阶段 1"))
			}
			return
		}
	}
	session, _ := auth.SessionFromContext(r.Context())
	job, err := s.jobs.Submit(request.Project, request.Environment, item.TargetName, session.Username, request.Action)
	if err != nil {
		writeJobSubmitError(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, job)
}

func (s *Server) retryJob(w http.ResponseWriter, r *http.Request) {
	original, ok := s.jobs.Get(r.PathValue("id"))
	if !ok {
		writeError(w, http.StatusNotFound, os.ErrNotExist)
		return
	}
	if !jobs.Retryable(original.Status) {
		writeError(w, http.StatusConflict, errors.New("only failed, canceled, or ignored jobs can be retried"))
		return
	}
	if err := s.requireProjectDeploy(r, original.Project); err != nil {
		writeAccessError(w, err)
		return
	}
	if err := s.requireBoundProjectAWSCredential(r.Context(), original.Project); err != nil {
		writeProjectAWSCredentialError(w, err)
		return
	}
	var request struct {
		Confirm  string `json:"confirm"`
		Password string `json:"password"`
	}
	if err := decodeJSON(w, r, &request); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	requiredConfirmation := ""
	switch original.Action {
	case jobs.ActionDeploy, jobs.ActionPlatform, jobs.ActionAccess, jobs.ActionTLS:
		requiredConfirmation = original.Project + "/" + original.Environment
	case jobs.ActionDestroy:
		requiredConfirmation = "destroy:" + original.Project + "/" + original.Environment
	}
	if requiredConfirmation != "" && request.Confirm != requiredConfirmation {
		writeError(w, http.StatusBadRequest, errors.New("retry confirmation does not match"))
		return
	}
	if original.Action == jobs.ActionDestroy {
		session, _ := auth.SessionFromContext(r.Context())
		err := s.authentication.ReauthenticateRequest(r, session.Username, request.Password)
		request.Password = ""
		if errors.Is(err, auth.ErrRateLimited) {
			writeError(w, http.StatusTooManyRequests, err)
			return
		}
		if err != nil {
			writeError(w, http.StatusUnauthorized, auth.ErrInvalidCredentials)
			return
		}
	}
	item, err := s.accessControl.Environment(r.Context(), original.Project, original.Environment)
	if err != nil {
		writeAccessError(w, err)
		return
	}
	doc, err := s.environments.Load(item.TargetName)
	if err != nil {
		writeError(w, http.StatusBadRequest, errors.New("环境部署配置不存在或无法读取"))
		return
	}
	if environment.IsExistingEKS(doc) && (original.Action == jobs.ActionPlan || original.Action == jobs.ActionDeploy) {
		writeError(w, http.StatusConflict, errors.New("该环境接入已有 EKS，不能重试阶段 1；请执行组件与接入部署"))
		return
	}
	if original.Action == jobs.ActionDeploy {
		if s.resources == nil {
			writeError(w, http.StatusServiceUnavailable, errors.New("AWS 实际配置同步服务不可用，平台已停止阶段1重试"))
			return
		}
		if resetErr := s.resetMissingCloudBaselineAfterDestroy(r.Context(), original.Project, original.Environment); resetErr != nil {
			writeError(w, http.StatusFailedDependency, fmt.Errorf("无法重置已销毁环境的 AWS 配置基线: %w", resetErr))
			return
		}
		preflightCtx, cancel := contextWithTimeout(r, 90*time.Second)
		snapshot, refreshErr := s.resources.RefreshCloudConfiguration(preflightCtx, original.Project, original.Environment, item.TargetName)
		cancel()
		if refreshErr != nil {
			writeError(w, http.StatusFailedDependency, fmt.Errorf("重试前读取 AWS 实际配置失败，平台不会使用过期参数继续执行: %w", refreshErr))
			return
		}
		if driftErr := snapshot.CloudDeploymentPreflightError(); driftErr != nil {
			writeError(w, http.StatusConflict, driftErr)
			return
		}
	}
	// A platform retry must become an observable job instead of being rejected
	// by a second, short-lived HTTP status probe. The runner's first steps update
	// kubeconfig and perform the full EKS permission preflight before Terraform
	// can change anything. A genuine access problem is therefore recorded with
	// its step and deployment log, while a transient probe failure cannot strand
	// the user on the old canceled job with only an HTTP 409.
	session, _ := auth.SessionFromContext(r.Context())
	var retry *jobs.Job
	if original.Action == jobs.ActionStorageExpand || original.Action == jobs.ActionStorageShrink {
		retry, err = s.jobs.SubmitWithParameters(original.Project, original.Environment, item.TargetName, session.Username, original.Action, original.Parameters)
	} else {
		retry, err = s.jobs.SubmitWithCompletion(original.Project, original.Environment, item.TargetName, session.Username, original.Action, original.CompletionAction)
	}
	if err != nil {
		writeJobSubmitError(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, retry)
}

func (s *Server) ignoreJobFailure(w http.ResponseWriter, r *http.Request) {
	original, ok := s.jobs.Get(r.PathValue("id"))
	if !ok {
		writeError(w, http.StatusNotFound, os.ErrNotExist)
		return
	}
	if err := s.requireProjectDeploy(r, original.Project); err != nil {
		writeAccessError(w, err)
		return
	}
	var request struct {
		Confirm string `json:"confirm"`
		Reason  string `json:"reason"`
	}
	if err := decodeJSON(w, r, &request); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if request.Confirm != original.Project+"/"+original.Environment {
		writeError(w, http.StatusBadRequest, errors.New("ignore confirmation does not match"))
		return
	}
	session, _ := auth.SessionFromContext(r.Context())
	ignored, err := s.jobs.Ignore(original.ID, session.Username, request.Reason)
	switch {
	case errors.Is(err, jobs.ErrInvalidIgnoreReason):
		writeError(w, http.StatusBadRequest, errors.New("请填写 3 到 500 个字符的忽略原因"))
		return
	case errors.Is(err, jobs.ErrDestroyNotIgnorable):
		writeError(w, http.StatusConflict, errors.New("销毁失败不能忽略，必须继续清理剩余资源"))
		return
	case errors.Is(err, jobs.ErrNotIgnorable):
		writeError(w, http.StatusConflict, errors.New("只有失败或已取消的任务可以标记为已忽略"))
		return
	case errors.Is(err, jobs.ErrEnvironmentBusy):
		writeError(w, http.StatusConflict, errors.New("当前环境有任务正在执行，结束后才能忽略旧失败记录"))
		return
	case err != nil:
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, ignored)
}

func (s *Server) getJob(w http.ResponseWriter, r *http.Request) {
	job, ok := s.jobs.Get(r.PathValue("id"))
	if !ok {
		writeError(w, http.StatusNotFound, os.ErrNotExist)
		return
	}
	if _, err := s.requireProjectView(r, job.Project); err != nil {
		writeAccessError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, job)
}

func (s *Server) cancelJob(w http.ResponseWriter, r *http.Request) {
	job, ok := s.jobs.Get(r.PathValue("id"))
	if !ok {
		writeError(w, http.StatusNotFound, os.ErrNotExist)
		return
	}
	if err := s.requireProjectDeploy(r, job.Project); err != nil {
		writeAccessError(w, err)
		return
	}
	if err := s.jobs.Cancel(job.ID); err != nil {
		writeError(w, http.StatusConflict, err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]bool{"canceling": true})
}

func (s *Server) jobLogs(w http.ResponseWriter, r *http.Request) {
	job, ok := s.jobs.Get(r.PathValue("id"))
	if !ok {
		writeError(w, http.StatusNotFound, os.ErrNotExist)
		return
	}
	if _, err := s.requireProjectView(r, job.Project); err != nil {
		writeAccessError(w, err)
		return
	}
	offset, _ := strconv.ParseInt(r.URL.Query().Get("offset"), 10, 64)
	data, next, complete, err := s.jobs.ReadLog(job.ID, offset, 256*1024)
	if errors.Is(err, os.ErrNotExist) {
		writeError(w, http.StatusNotFound, err)
		return
	}
	if err != nil {
		if errors.Is(err, jobs.ErrLogStorageUnavailable) || errors.Is(err, os.ErrPermission) {
			writeJobStorageError(w, err)
			return
		}
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"data": string(data), "next_offset": next, "complete": complete,
	})
}

func writeJobSubmitError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, jobs.ErrEnvironmentBusy):
		writeError(w, http.StatusConflict, errors.New("当前环境已有部署任务正在执行，请等待任务结束后再提交"))
	case errors.Is(err, jobs.ErrLogStorageUnavailable), errors.Is(err, os.ErrPermission):
		writeJobStorageError(w, err)
	default:
		writeError(w, http.StatusBadRequest, err)
	}
}

func writeJobStorageError(w http.ResponseWriter, err error) {
	log.Printf("job log storage unavailable: %s", safeLogField(sensitive.RedactText(err.Error()))) // #nosec G706 -- safeLogField strips control characters and bounds length.
	writeJSON(w, http.StatusServiceUnavailable, map[string]string{
		"error": "任务日志存储暂时不可写，平台已阻止启动部署；请检查运行卷权限，恢复后直接重试",
		"code":  "job_log_storage_unwritable",
	})
}

func (s *Server) projectEnvironmentStatus(w http.ResponseWriter, r *http.Request) {
	projectKey, environmentKey := r.PathValue("project"), r.PathValue("environment")
	if _, err := s.requireProjectView(r, projectKey); err != nil {
		writeAccessError(w, err)
		return
	}
	if err := s.requireBoundProjectAWSCredential(r.Context(), projectKey); err != nil {
		writeProjectAWSCredentialError(w, err)
		return
	}
	item, err := s.accessControl.Environment(r.Context(), projectKey, environmentKey)
	if err != nil {
		writeAccessError(w, err)
		return
	}
	ctx, cancel := contextWithTimeout(r, 90*time.Second)
	defer cancel()
	var report *statusservice.Report
	err = nil
	if r.URL.Query().Get("fresh") == "true" {
		report, err = s.status.CollectFresh(ctx, item.TargetName)
	} else {
		report, err = s.status.Collect(ctx, item.TargetName)
	}
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	report.Project = projectKey
	report.Environment = environmentKey
	report.EnvironmentName = access.EnvironmentName(environmentKey)
	report.TargetName = item.TargetName
	writeJSON(w, http.StatusOK, report)
}

func (s *Server) projectEnvironmentKubernetesServices(w http.ResponseWriter, r *http.Request) {
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
		writeError(w, http.StatusServiceUnavailable, errors.New("EKS 状态服务不可用"))
		return
	}
	item, err := s.accessControl.Environment(r.Context(), projectKey, environmentKey)
	if err != nil {
		writeAccessError(w, err)
		return
	}
	ctx, cancel := contextWithTimeout(r, 60*time.Second)
	defer cancel()
	services, err := s.status.ListServices(ctx, item.TargetName)
	if err != nil {
		writeError(w, http.StatusFailedDependency, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, map[string]any{
		"project": projectKey, "environment": environmentKey,
		"observed_at": time.Now().UTC(), "services": services,
	})
}

func (s *Server) environmentResources(w http.ResponseWriter, r *http.Request) {
	projectKey, environmentKey := r.PathValue("project"), r.PathValue("environment")
	if _, err := s.requireProjectView(r, projectKey); err != nil {
		writeAccessError(w, err)
		return
	}
	if err := s.requireBoundProjectAWSCredential(r.Context(), projectKey); err != nil {
		writeProjectAWSCredentialError(w, err)
		return
	}
	if s.resources == nil {
		writeError(w, http.StatusServiceUnavailable, errors.New("resource center is not configured"))
		return
	}
	item, err := s.accessControl.Environment(r.Context(), projectKey, environmentKey)
	if err != nil {
		writeAccessError(w, err)
		return
	}
	if resetErr := s.resetMissingCloudBaselineAfterDestroy(r.Context(), projectKey, environmentKey); resetErr != nil {
		writeError(w, http.StatusFailedDependency, fmt.Errorf("无法重置已销毁环境的 AWS 配置基线: %w", resetErr))
		return
	}
	var snapshot resourcecenter.Snapshot
	if r.URL.Query().Get("cloud_only") == "true" {
		snapshot, err = s.resources.RefreshCloudConfiguration(r.Context(), projectKey, environmentKey, item.TargetName)
	} else if r.URL.Query().Get("fresh") == "true" {
		snapshot, err = s.resources.Refresh(r.Context(), projectKey, environmentKey, item.TargetName)
	} else {
		snapshot, err = s.resources.Load(r.Context(), projectKey, environmentKey)
		if errors.Is(err, os.ErrNotExist) || (err == nil && snapshot.SchemaVersion < 4) {
			snapshot, err = s.resources.Refresh(r.Context(), projectKey, environmentKey, item.TargetName)
		}
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	w.Header().Set("Cache-Control", "private, no-store")
	writeJSON(w, http.StatusOK, snapshot.Public())
}

func (s *Server) syncEnvironmentAWSConfiguration(w http.ResponseWriter, r *http.Request) {
	projectKey, environmentKey := r.PathValue("project"), r.PathValue("environment")
	if err := s.requireProjectConfigure(r, projectKey); err != nil {
		writeAccessError(w, err)
		return
	}
	if err := s.requireBoundProjectAWSCredential(r.Context(), projectKey); err != nil {
		writeProjectAWSCredentialError(w, err)
		return
	}
	if s.resources == nil {
		writeError(w, http.StatusServiceUnavailable, errors.New("resource center is not configured"))
		return
	}
	if s.jobs.HasActiveEnvironment(projectKey, environmentKey) {
		writeError(w, http.StatusConflict, errors.New("当前环境有任务执行中，不能在部署过程中改写配置基线"))
		return
	}
	item, err := s.accessControl.Environment(r.Context(), projectKey, environmentKey)
	if err != nil {
		writeAccessError(w, err)
		return
	}
	if resetErr := s.resetMissingCloudBaselineAfterDestroy(r.Context(), projectKey, environmentKey); resetErr != nil {
		writeError(w, http.StatusFailedDependency, fmt.Errorf("无法重置已销毁环境的 AWS 配置基线: %w", resetErr))
		return
	}
	ctx, cancel := contextWithTimeout(r, 90*time.Second)
	defer cancel()
	doc, snapshot, err := s.resources.SyncDesiredFromAWS(ctx, projectKey, environmentKey, item.TargetName)
	if err != nil {
		writeError(w, http.StatusFailedDependency, err)
		return
	}
	w.Header().Set("Cache-Control", "private, no-store")
	writeJSON(w, http.StatusOK, map[string]any{"config": doc, "resources": snapshot.Public()})
}

func (s *Server) revealEnvironmentCredential(w http.ResponseWriter, r *http.Request) {
	projectKey, environmentKey := r.PathValue("project"), r.PathValue("environment")
	session, _ := auth.SessionFromContext(r.Context())
	if s.accessControl == nil {
		writeError(w, http.StatusServiceUnavailable, errors.New("access control is unavailable"))
		return
	}
	if err := s.accessControl.RequireViewSecrets(r.Context(), session.Username, session.IsAdmin, projectKey); err != nil {
		writeAccessError(w, err)
		return
	}
	item, err := s.accessControl.Environment(r.Context(), projectKey, environmentKey)
	if err != nil {
		writeAccessError(w, err)
		return
	}
	if err := s.requireBoundProjectAWSCredential(r.Context(), projectKey); err != nil {
		writeProjectAWSCredentialError(w, err)
		return
	}
	if s.resources == nil {
		writeError(w, http.StatusServiceUnavailable, errors.New("resource center is not configured"))
		return
	}
	credentialID := r.PathValue("credential")
	values, err := s.resources.Reveal(r.Context(), projectKey, environmentKey, credentialID)
	if errors.Is(err, os.ErrNotExist) {
		// A resource snapshot can outlive a Helm uninstall or Secret rotation.
		// Refresh once on this exceptional path, then retry against the current
		// resource catalog so stale cards disappear without slowing normal reads.
		refreshCtx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
		_, _ = s.resources.Refresh(refreshCtx, projectKey, environmentKey, item.TargetName)
		cancel()
		values, err = s.resources.Reveal(r.Context(), projectKey, environmentKey, credentialID)
	}
	if errors.Is(err, os.ErrNotExist) {
		writeError(w, http.StatusNotFound, errors.New("凭据来源不存在或尚未部署，资源目录已重新同步，请确认组件和 Secret 已就绪"))
		return
	}
	if err != nil {
		log.Printf("resource credential reveal failed: user=%s project=%s environment=%s credential=%s error=%v", session.Username, projectKey, environmentKey, credentialID, err)
		writeError(w, http.StatusFailedDependency, errors.New("无法读取资源凭据，请检查 AWS/Kubernetes 访问权限、资源状态和 Secret 引用"))
		return
	}
	log.Printf("resource credential revealed: user=%s project=%s environment=%s credential=%s", session.Username, projectKey, environmentKey, credentialID)
	w.Header().Set("Cache-Control", "no-store, max-age=0")
	w.Header().Set("Pragma", "no-cache")
	writeJSON(w, http.StatusOK, map[string]any{"values": values})
}

func (s *Server) listUsers(w http.ResponseWriter, r *http.Request) {
	if !requirePlatformPermission(w, r, platformManageUsers) {
		return
	}
	users, err := s.accessControl.ListUsers(r.Context())
	if err != nil {
		writeAccessError(w, err)
		return
	}
	session, _ := auth.SessionFromContext(r.Context())
	projects, err := s.accessControl.ListProjects(r.Context(), session.Username, false)
	if err != nil {
		writeAccessError(w, err)
		return
	}
	visibleProjects := make(map[string]bool, len(projects))
	for _, project := range projects {
		visibleProjects[project.Key] = true
	}
	for index := range users {
		filtered := users[index].Permissions[:0]
		for _, permission := range users[index].Permissions {
			if visibleProjects[permission.ProjectKey] {
				filtered = append(filtered, permission)
			}
		}
		users[index].Permissions = filtered
	}
	writeJSON(w, http.StatusOK, users)
}

func (s *Server) createUser(w http.ResponseWriter, r *http.Request) {
	if !requirePlatformPermission(w, r, platformManageUsers) {
		return
	}
	var request struct {
		Username            string                    `json:"username"`
		DisplayName         string                    `json:"display_name"`
		Password            string                    `json:"password"`
		IsAdmin             bool                      `json:"is_admin"`
		Active              bool                      `json:"active"`
		PlatformPermissions access.PlatformPermission `json:"platform_permissions"`
	}
	if err := decodeJSON(w, r, &request); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if len(request.Password) < 12 || len(request.Password) > 256 {
		writeError(w, http.StatusBadRequest, errors.New("password must contain 12 to 256 characters"))
		return
	}
	session, _ := auth.SessionFromContext(r.Context())
	if request.IsAdmin && !session.IsAdmin {
		writeError(w, http.StatusForbidden, access.ErrForbidden)
		return
	}
	if request.IsAdmin {
		request.PlatformPermissions = access.FullPlatformPermission()
	}
	if !platformPermissionSubset(request.PlatformPermissions, session.PlatformPermissions) && !session.IsAdmin {
		writeError(w, http.StatusForbidden, errors.New("cannot grant a platform permission you do not hold"))
		return
	}
	if _, err := s.accessControl.GetUser(r.Context(), strings.ToLower(request.Username)); err == nil {
		writeError(w, http.StatusConflict, errors.New("username already exists"))
		return
	}
	hash, err := auth.HashPassword([]byte(request.Password))
	request.Password = ""
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	user := access.User{
		Username: request.Username, DisplayName: request.DisplayName, PasswordHash: hash,
		IsAdmin: request.IsAdmin, Active: request.Active, PlatformPermissions: request.PlatformPermissions,
	}
	if err := s.accessControl.SaveUser(r.Context(), user); err != nil {
		writeAccessError(w, err)
		return
	}
	created, _ := s.accessControl.GetUser(r.Context(), strings.ToLower(request.Username))
	writeJSON(w, http.StatusCreated, created)
}

func (s *Server) updateUser(w http.ResponseWriter, r *http.Request) {
	if !requirePlatformPermission(w, r, platformManageUsers) {
		return
	}
	username := strings.ToLower(r.PathValue("username"))
	current, err := s.accessControl.GetUser(r.Context(), username)
	if err != nil {
		writeAccessError(w, err)
		return
	}
	var request struct {
		DisplayName         string                    `json:"display_name"`
		Password            string                    `json:"password"`
		IsAdmin             bool                      `json:"is_admin"`
		Active              bool                      `json:"active"`
		PlatformPermissions access.PlatformPermission `json:"platform_permissions"`
	}
	if err := decodeJSON(w, r, &request); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	session, _ := auth.SessionFromContext(r.Context())
	if err := s.requireUserManagementScope(r.Context(), session, current); err != nil {
		writeAccessError(w, err)
		return
	}
	if request.IsAdmin != current.IsAdmin && !session.IsAdmin {
		writeError(w, http.StatusForbidden, access.ErrForbidden)
		return
	}
	if request.IsAdmin {
		request.PlatformPermissions = access.FullPlatformPermission()
	}
	if !platformPermissionSubset(request.PlatformPermissions, session.PlatformPermissions) && !session.IsAdmin {
		writeError(w, http.StatusForbidden, errors.New("cannot grant a platform permission you do not hold"))
		return
	}
	if username == session.Username && (!request.Active || request.IsAdmin != current.IsAdmin ||
		request.PlatformPermissions != current.PlatformPermissions || request.Password != "") {
		writeError(w, http.StatusBadRequest, errors.New("use the personal account menu to change your own password; you cannot change your own role, status, or platform permissions"))
		return
	}
	current.DisplayName = request.DisplayName
	current.IsAdmin = request.IsAdmin
	current.Active = request.Active
	current.PlatformPermissions = request.PlatformPermissions
	if request.Password != "" {
		if len(request.Password) < 12 || len(request.Password) > 256 {
			writeError(w, http.StatusBadRequest, errors.New("password must contain 12 to 256 characters"))
			return
		}
		current.PasswordHash, err = auth.HashPassword([]byte(request.Password))
		request.Password = ""
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
	}
	if err := s.accessControl.SaveUser(r.Context(), current); err != nil {
		writeAccessError(w, err)
		return
	}
	updated, _ := s.accessControl.GetUser(r.Context(), username)
	if username == session.Username {
		if refreshed, refreshErr := s.authentication.RefreshCurrentSession(r, updated); refreshErr == nil {
			_ = refreshed
		}
	}
	writeJSON(w, http.StatusOK, updated)
}

func (s *Server) deleteUser(w http.ResponseWriter, r *http.Request) {
	if !requirePlatformPermission(w, r, platformManageUsers) {
		return
	}
	session, _ := auth.SessionFromContext(r.Context())
	username := strings.ToLower(r.PathValue("username"))
	if username == session.Username {
		writeError(w, http.StatusBadRequest, errors.New("you cannot delete your own account"))
		return
	}
	target, err := s.accessControl.GetUser(r.Context(), username)
	if err != nil {
		writeAccessError(w, err)
		return
	}
	if target.IsAdmin && !session.IsAdmin {
		writeError(w, http.StatusForbidden, access.ErrForbidden)
		return
	}
	if err := s.requireUserManagementScope(r.Context(), session, target); err != nil {
		writeAccessError(w, err)
		return
	}
	if err := s.accessControl.DeleteUser(r.Context(), username); err != nil {
		writeAccessError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) saveProjectMember(w http.ResponseWriter, r *http.Request) {
	if !requirePlatformPermission(w, r, platformManageUsers) {
		return
	}
	projectKey, username := r.PathValue("project"), strings.ToLower(r.PathValue("username"))
	if err := s.requireProjectConfigure(r, projectKey); err != nil {
		writeAccessError(w, err)
		return
	}
	if _, err := s.accessControl.GetUser(r.Context(), username); err != nil {
		writeAccessError(w, err)
		return
	}
	var permission access.Permission
	if err := decodeJSON(w, r, &permission); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	permission.ProjectKey = projectKey
	permission.Username = username
	if permission.CanDeploy || permission.CanConfigure || permission.CanViewSecrets {
		permission.CanView = true
	}
	session, _ := auth.SessionFromContext(r.Context())
	callerPermission, err := s.accessControl.Permission(r.Context(), session.Username, false, projectKey)
	if err != nil {
		writeAccessError(w, err)
		return
	}
	if !projectPermissionSubset(permission, callerPermission) {
		writeError(w, http.StatusForbidden, errors.New("cannot grant a project permission you do not hold"))
		return
	}
	if err := s.accessControl.SavePermission(r.Context(), permission); err != nil {
		writeAccessError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, permission)
}

func (s *Server) deleteProjectMember(w http.ResponseWriter, r *http.Request) {
	if !requirePlatformPermission(w, r, platformManageUsers) {
		return
	}
	if err := s.requireProjectConfigure(r, r.PathValue("project")); err != nil {
		writeAccessError(w, err)
		return
	}
	if err := s.accessControl.DeletePermission(r.Context(), r.PathValue("project"), strings.ToLower(r.PathValue("username"))); err != nil {
		writeAccessError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) requireProjectView(r *http.Request, project string) (access.Permission, error) {
	if s.accessControl == nil {
		return access.Permission{}, errors.New("project access service is unavailable")
	}
	session, ok := auth.SessionFromContext(r.Context())
	if !ok {
		return access.Permission{}, auth.ErrUnauthenticated
	}
	return s.accessControl.RequireView(r.Context(), session.Username, session.IsAdmin, project)
}

func (s *Server) requireUserManagementScope(ctx context.Context, session auth.Session, target access.User) error {
	if session.IsAdmin {
		return nil
	}
	if target.IsAdmin || !platformPermissionSubset(target.PlatformPermissions, session.PlatformPermissions) {
		return access.ErrForbidden
	}
	for _, targetPermission := range target.Permissions {
		callerPermission, err := s.accessControl.Permission(ctx, session.Username, false, targetPermission.ProjectKey)
		if err != nil || !callerPermission.CanConfigure || !projectPermissionSubset(targetPermission, callerPermission) {
			return access.ErrForbidden
		}
	}
	return nil
}

func (s *Server) requireProjectDeploy(r *http.Request, project string) error {
	if s.accessControl == nil {
		return errors.New("project access service is unavailable")
	}
	session, ok := auth.SessionFromContext(r.Context())
	if !ok {
		return auth.ErrUnauthenticated
	}
	return s.accessControl.RequireDeploy(r.Context(), session.Username, session.IsAdmin, project)
}

func (s *Server) requireProjectConfigure(r *http.Request, project string) error {
	if s.accessControl == nil {
		return errors.New("project access service is unavailable")
	}
	session, ok := auth.SessionFromContext(r.Context())
	if !ok {
		return auth.ErrUnauthenticated
	}
	return s.accessControl.RequireConfigure(r.Context(), session.Username, session.IsAdmin, project)
}

type platformCapability string

const (
	platformManageProjects    platformCapability = "manage_projects"
	platformManageUsers       platformCapability = "manage_users"
	platformManageCredentials platformCapability = "manage_credentials"
	platformManageComponents  platformCapability = "manage_components"
	platformViewAudit         platformCapability = "view_audit"
)

func requirePlatformPermission(w http.ResponseWriter, r *http.Request, capability platformCapability) bool {
	session, ok := auth.SessionFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusForbidden, access.ErrForbidden)
		return false
	}
	if session.IsAdmin {
		return true
	}
	permissions := session.PlatformPermissions
	allowed := map[platformCapability]bool{
		platformManageProjects:    permissions.CanManageProjects,
		platformManageUsers:       permissions.CanManageUsers,
		platformManageCredentials: permissions.CanManageCredentials,
		platformManageComponents:  permissions.CanManageComponents,
		platformViewAudit:         permissions.CanViewAudit,
	}[capability]
	if !allowed {
		writeError(w, http.StatusForbidden, access.ErrForbidden)
	}
	return allowed
}

func platformPermissionSubset(requested, caller access.PlatformPermission) bool {
	return (!requested.CanManageProjects || caller.CanManageProjects) &&
		(!requested.CanManageUsers || caller.CanManageUsers) &&
		(!requested.CanManageCredentials || caller.CanManageCredentials) &&
		(!requested.CanManageComponents || caller.CanManageComponents) &&
		(!requested.CanViewAudit || caller.CanViewAudit)
}

func projectPermissionSubset(requested, caller access.Permission) bool {
	return (!requested.CanView || caller.CanView) &&
		(!requested.CanDeploy || caller.CanDeploy) &&
		(!requested.CanConfigure || caller.CanConfigure) &&
		(!requested.CanViewSecrets || caller.CanViewSecrets)
}

func writeAccessError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, access.ErrForbidden):
		writeError(w, http.StatusForbidden, err)
	case errors.Is(err, os.ErrNotExist):
		writeError(w, http.StatusNotFound, err)
	case errors.Is(err, access.ErrInvalidProject), errors.Is(err, access.ErrInvalidProjectDisplayName),
		errors.Is(err, access.ErrProjectDescriptionTooLong), errors.Is(err, access.ErrInvalidEnvironment),
		errors.Is(err, access.ErrInvalidUsername), errors.Is(err, access.ErrInvalidPermission):
		writeError(w, http.StatusBadRequest, err)
	default:
		writeError(w, http.StatusInternalServerError, err)
	}
}

func documentString(doc environment.Document, key string) string {
	value, _ := doc[key].(string)
	return strings.TrimSpace(value)
}

func (s *Server) auth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/api/") || r.URL.Path == "/api/health" || r.URL.Path == "/api/auth/login" ||
			(r.Method == http.MethodPost && (strings.HasPrefix(r.URL.Path, "/api/internal/alerting/relay/") || strings.HasPrefix(r.URL.Path, "/api/cicd/webhooks/gitlab/"))) {
			next.ServeHTTP(w, r)
			return
		}
		session, err := s.authentication.Authenticate(r)
		if err != nil {
			if errors.Is(err, context.Canceled) {
				return
			}
			if errors.Is(err, auth.ErrUnauthenticated) {
				writeError(w, http.StatusUnauthorized, auth.ErrUnauthenticated)
			} else {
				writeError(w, http.StatusServiceUnavailable, errors.New("登录会话校验服务暂时不可用，请稍后重试"))
			}
			return
		}
		if auditWriter, ok := w.(*statusWriter); ok {
			auditWriter.username = session.Username
		}
		if mutatingMethod(r.Method) {
			if !s.sameOrigin(r) {
				writeError(w, http.StatusForbidden, errors.New("cross-origin request rejected"))
				return
			}
			if err := s.authentication.ValidateCSRF(r, session); err != nil {
				writeError(w, http.StatusForbidden, err)
				return
			}
		}
		next.ServeHTTP(w, r.WithContext(auth.WithSession(r.Context(), session)))
	})
}

func (s *Server) securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("X-Permitted-Cross-Domain-Policies", "none")
		w.Header().Set("Referrer-Policy", "same-origin")
		w.Header().Set("Cross-Origin-Opener-Policy", "same-origin")
		w.Header().Set("Cross-Origin-Resource-Policy", "same-origin")
		w.Header().Set("Permissions-Policy", "camera=(), microphone=(), geolocation=()")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; base-uri 'self'; form-action 'self'; frame-ancestors 'none'; object-src 'none'; worker-src 'self'; manifest-src 'self'; style-src 'self' 'unsafe-inline'; script-src 'self'; connect-src 'self'; img-src 'self' data:; font-src 'self' data:")
		if strings.HasPrefix(r.URL.Path, "/api/") {
			w.Header().Set("Cache-Control", "no-store")
		}
		// TLS commonly terminates at Cloudflare, Higress, ALB, or another trusted
		// reverse proxy. In that deployment mode r.TLS is nil, while the validated
		// external origin still guarantees that browsers use HTTPS.
		if r.TLS != nil || strings.HasPrefix(strings.ToLower(strings.TrimSpace(s.config.Security.ExternalOrigin)), "https://") {
			w.Header().Set("Strict-Transport-Security", "max-age=31536000; includeSubDomains")
		}
		next.ServeHTTP(w, r)
	})
}

type statusWriter struct {
	http.ResponseWriter
	status   int
	username string
}

func (w *statusWriter) WriteHeader(status int) {
	w.status = status
	w.ResponseWriter.WriteHeader(status)
}

func (s *Server) audit(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !mutatingMethod(r.Method) {
			next.ServeHTTP(w, r)
			return
		}
		wrapped := &statusWriter{ResponseWriter: w, status: http.StatusOK}
		started := time.Now()
		next.ServeHTTP(wrapped, r)
		session, _ := auth.SessionFromContext(r.Context())
		username := wrapped.username
		if username == "" {
			username = session.Username
		}
		username = auditActor(username, r.URL.Path)
		remote := auditRemoteAddress(r)
		log.Printf("audit method=%s path=%s status=%d user=%s remote=%s duration=%s", r.Method, safeLogField(r.URL.Path), wrapped.status, safeLogField(username), safeLogField(remote), time.Since(started).Round(time.Millisecond)) // #nosec G706 -- all attacker-controlled fields are stripped of control characters.
		if s.auditStore != nil {
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			if err := s.auditStore.RecordAudit(ctx, r.Method, r.URL.Path, wrapped.status, username, remote, time.Since(started)); err != nil {
				log.Printf("audit persistence failed: %v", err)
			}
			cancel()
		}
	})
}

func auditActor(username, requestPath string) string {
	if username = strings.TrimSpace(username); username != "" {
		return username
	}
	switch {
	case strings.HasPrefix(requestPath, "/api/internal/alerting/relay/"):
		return "system:alerting"
	case strings.HasPrefix(requestPath, "/api/cicd/webhooks/gitlab/"):
		return "system:gitlab-webhook"
	default:
		return "anonymous"
	}
}

func auditRemoteAddress(r *http.Request) string {
	direct := requestIPAddress(r.RemoteAddr)
	ip := net.ParseIP(direct)
	if ip == nil || (!ip.IsPrivate() && !ip.IsLoopback()) {
		return direct
	}
	forwardedFor := r.Header.Get("X-Forwarded-For")
	if comma := strings.IndexByte(forwardedFor, ','); comma >= 0 {
		forwardedFor = forwardedFor[:comma]
	}
	for _, candidate := range []string{r.Header.Get("CF-Connecting-IP"), forwardedFor, r.Header.Get("X-Real-IP")} {
		if forwarded := requestIPAddress(candidate); net.ParseIP(forwarded) != nil {
			return forwarded
		}
	}
	return direct
}

func requestIPAddress(value string) string {
	value = strings.TrimSpace(value)
	if host, _, err := net.SplitHostPort(value); err == nil {
		return strings.Trim(host, "[]")
	}
	return strings.Trim(value, "[]")
}

func mutatingMethod(method string) bool {
	return method == http.MethodPost || method == http.MethodPut || method == http.MethodPatch || method == http.MethodDelete
}

func (s *Server) sameOrigin(r *http.Request) bool {
	origin := strings.TrimSpace(r.Header.Get("Origin"))
	if origin == "" {
		return true
	}
	parsed, err := url.Parse(origin)
	if err != nil {
		return false
	}
	if configured := strings.TrimSpace(s.config.Security.ExternalOrigin); configured != "" {
		expected, parseErr := url.Parse(configured)
		return parseErr == nil && strings.EqualFold(parsed.Host, expected.Host) && parsed.Scheme == expected.Scheme
	}
	expectedScheme := "http"
	if r.TLS != nil {
		expectedScheme = "https"
	}
	return strings.EqualFold(parsed.Host, r.Host) && parsed.Scheme == expectedScheme
}

func (s *Server) componentPaths() map[string]string {
	result := make(map[string]string, len(s.config.Components))
	for _, component := range s.config.Components {
		result[component.Key] = component.ConfigPath
	}
	return result
}

// normalizeComponentSources keeps supply-chain settings platform-owned. An
// environment may choose and configure a component, but cannot swap the chart
// or redirect a credential reference through a crafted API request.
func (s *Server) normalizeComponentSources(ctx context.Context, doc environment.Document, project, environmentName string) error {
	components, ok := doc["components"].(map[string]any)
	if !ok {
		return errors.New("components must be an object")
	}
	catalog, ok := components["catalog"].(map[string]any)
	if !ok {
		return errors.New("components.catalog must be an object")
	}
	defaults := environment.DefaultDocument(project, environmentName)
	defaultComponents, _ := defaults["components"].(map[string]any)
	builtins, _ := defaultComponents["catalog"].(map[string]any)
	authoritative := make(map[string]map[string]any, len(builtins))
	for key, raw := range builtins {
		if definition, ok := raw.(map[string]any); ok {
			authoritative[key] = definition
		}
	}
	if s.componentCatalog != nil {
		extensions, err := s.componentCatalog.List(ctx)
		if err != nil {
			return fmt.Errorf("load component catalog: %w", err)
		}
		for _, item := range extensions {
			authoritative[item.Key] = map[string]any{
				"display_name": item.DisplayName, "category": item.Category,
				"repository": item.Repository, "chart": item.Chart, "chart_version": item.ChartVersion,
				"release_name": item.Key, "service_name": "", "service_port": 80,
				"protocol": "http", "username": "", "secret_name": "", "secret_key": "",
				"_default_namespace": item.DefaultNamespace, "_default_values": item.Values,
			}
		}
	}
	locked := []string{
		"display_name", "category", "repository", "chart", "chart_version", "builtin_chart",
		"release_name", "service_name", "service_port", "protocol", "username", "secret_name", "secret_key",
	}
	for key, raw := range catalog {
		entry, ok := raw.(map[string]any)
		if !ok {
			catalog[key] = map[string]any{"enabled": false}
			continue
		}
		definition, supported := authoritative[key]
		if !supported {
			entry["enabled"] = false
			for _, field := range locked {
				delete(entry, field)
			}
			continue
		}
		for _, field := range locked {
			if value, exists := definition[field]; exists {
				entry[field] = value
			} else {
				delete(entry, field)
			}
		}
	}
	for key, definition := range authoritative {
		if _, exists := catalog[key]; exists {
			continue
		}
		entry := map[string]any{
			"enabled": false, "namespace": "platform-server", "domain": "", "tls": false,
			"timeout": 1200, "values": map[string]any{},
		}
		if namespace, ok := definition["_default_namespace"].(string); ok && namespace != "" {
			entry["namespace"] = namespace
		}
		if values, ok := definition["_default_values"].(map[string]any); ok {
			entry["values"] = values
		}
		for _, field := range locked {
			if value, exists := definition[field]; exists {
				entry[field] = value
			}
		}
		catalog[key] = entry
	}
	return nil
}

func decodeJSON(w http.ResponseWriter, r *http.Request, target any) error {
	r.Body = http.MaxBytesReader(w, r.Body, 2<<20)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		if strings.HasPrefix(err.Error(), "json: unknown field ") {
			field := strings.TrimPrefix(err.Error(), "json: unknown field ")
			return fmt.Errorf("请求包含当前接口不支持的字段 %s；请刷新页面后重试", field)
		}
		return fmt.Errorf("invalid JSON body: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return errors.New("invalid JSON body: body must contain exactly one JSON value")
	}
	return nil
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writeError(w http.ResponseWriter, status int, err error) {
	message := err.Error()
	if status >= 500 {
		log.Printf("request failed status=%d error=%s", status, safeLogField(sensitive.RedactText(message))) // #nosec G706 -- safeLogField strips log control characters and length-bounds the value.
		switch status {
		case http.StatusBadGateway:
			message = "upstream service request failed"
		case http.StatusServiceUnavailable:
			message = "service unavailable"
		case http.StatusGatewayTimeout:
			message = "upstream service timed out"
		default:
			message = "internal server error"
		}
	}
	writeJSON(w, status, map[string]string{"error": message})
}

func writeAWSCatalogError(w http.ResponseWriter, err error, subject, permission string) {
	const status = http.StatusFailedDependency
	switch {
	case errors.Is(err, awscatalog.ErrInvalidRegion), errors.Is(err, awscatalog.ErrInvalidQuery), errors.Is(err, awscatalog.ErrInvalidECRRepository):
		writeError(w, http.StatusBadRequest, err)
	case errors.Is(err, awscatalog.ErrCredentialUnavailable):
		writeError(w, http.StatusConflict, errors.New("当前项目未绑定并选中自己的 AWS 凭据；请先在 AWS 凭据池配置项目权限入口，平台不会使用其他项目凭据、AWS Profile、IAM Role 或默认凭据链"))
	case errors.Is(err, awscatalog.ErrCredentialRejected):
		writeError(w, status, errors.New("当前项目选择的 AWS 凭据已失效，请在 AWS 凭据池重新验证或更换该项目自己的凭据"))
	case errors.Is(err, awscatalog.ErrAccessDenied):
		writeError(w, status, fmt.Errorf("AWS 身份缺少 %s 权限，无法查询%s", permission, subject))
	case errors.Is(err, awscatalog.ErrUnsupportedCLI):
		writeError(w, status, errors.New("当前 AWS CLI 不支持该查询，请升级 AWS CLI v2 后重试"))
	case errors.Is(err, awscatalog.ErrNetworkUnavailable):
		writeError(w, status, errors.New("无法连接 AWS API，请检查平台网络、代理、DNS 和目标 Region 端点"))
	case errors.Is(err, awscatalog.ErrQueryTimedOut):
		writeError(w, status, fmt.Errorf("AWS %s查询超时，请检查网络后重试", subject))
	default:
		writeError(w, status, fmt.Errorf("AWS %s查询失败，请检查项目 AWS 身份、Region 和平台日志", subject))
	}
}

func contextWithTimeout(r *http.Request, timeout time.Duration) (context.Context, context.CancelFunc) {
	return context.WithTimeout(r.Context(), timeout)
}

func safeLogField(value string) string {
	value = strings.Map(func(r rune) rune {
		if r == '\n' || r == '\r' || r < 0x20 || r == 0x7f {
			return ' '
		}
		return r
	}, value)
	if len(value) > 2048 {
		return value[:2048]
	}
	return value
}

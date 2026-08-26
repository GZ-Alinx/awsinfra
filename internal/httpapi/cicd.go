package httpapi

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"

	"github.com/GZ-Alinx/awsinfra/internal/access"
	"github.com/GZ-Alinx/awsinfra/internal/auth"
	"github.com/GZ-Alinx/awsinfra/internal/awscatalog"
	"github.com/GZ-Alinx/awsinfra/internal/cicd"
	"github.com/GZ-Alinx/awsinfra/internal/environment"
	"github.com/GZ-Alinx/awsinfra/internal/gitlab"
	statusservice "github.com/GZ-Alinx/awsinfra/internal/status"
)

func (s *Server) ensureCICDECRRepository(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("project")
	if err := s.requireProjectConfigure(r, project); err != nil {
		writeAccessError(w, err)
		return
	}
	if err := s.requireBoundProjectAWSCredential(r.Context(), project); err != nil {
		writeProjectAWSCredentialError(w, err)
		return
	}
	if s.awsCatalog == nil {
		writeError(w, http.StatusServiceUnavailable, errors.New("AWS catalog service is unavailable"))
		return
	}
	var request struct {
		Region     string `json:"region"`
		Repository string `json:"repository"`
	}
	if err := decodeJSON(w, r, &request); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	item, err := s.awsCatalog.EnsureECRRepository(r.Context(), project, request.Region, request.Repository)
	if errors.Is(err, awscatalog.ErrInvalidRegion) || errors.Is(err, awscatalog.ErrInvalidECRRepository) {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if err != nil {
		writeAWSCatalogError(w, err, "ECR 镜像仓库", "ecr:DescribeRepositories + ecr:CreateRepository + ecr:TagResource")
		return
	}
	status := http.StatusOK
	if item.Created {
		status = http.StatusCreated
	}
	writeJSON(w, status, item)
}

type environmentJenkinsInfo struct {
	Project           string `json:"project"`
	Environment       string `json:"environment"`
	Enabled           bool   `json:"enabled"`
	DeploymentStatus  string `json:"deployment_status"`
	DeploymentDetail  string `json:"deployment_detail,omitempty"`
	Namespace         string `json:"namespace,omitempty"`
	ServiceName       string `json:"service_name,omitempty"`
	ServicePort       int    `json:"service_port,omitempty"`
	InternalURL       string `json:"internal_url,omitempty"`
	ExternalURL       string `json:"external_url,omitempty"`
	ConnectionMode    string `json:"connection_mode,omitempty"`
	ConnectionKey     string `json:"connection_key,omitempty"`
	Connected         bool   `json:"connected"`
	ConnectionHealthy bool   `json:"connection_healthy"`
	CanConnect        bool   `json:"can_connect"`
	Reason            string `json:"reason,omitempty"`

	TargetName  string `json:"-"`
	Region      string `json:"-"`
	ClusterName string `json:"-"`
	Username    string `json:"-"`
	SecretName  string `json:"-"`
	SecretKey   string `json:"-"`
}

func (s *Server) environmentJenkinsIntegration(w http.ResponseWriter, r *http.Request) {
	project, environmentKey := r.PathValue("project"), r.PathValue("environment")
	if _, err := s.requireProjectView(r, project); err != nil {
		writeAccessError(w, err)
		return
	}
	if !s.cicdAvailable(w) {
		return
	}
	info, err := s.discoverEnvironmentJenkins(r, project, environmentKey, false)
	if err != nil {
		writeCICDError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, info)
}

func (s *Server) connectEnvironmentJenkins(w http.ResponseWriter, r *http.Request) {
	project, environmentKey := r.PathValue("project"), r.PathValue("environment")
	if err := s.requireProjectConfigure(r, project); err != nil {
		writeAccessError(w, err)
		return
	}
	if !s.cicdAvailable(w) {
		return
	}
	session, ok := auth.SessionFromContext(r.Context())
	if !ok || s.accessControl == nil {
		writeError(w, http.StatusUnauthorized, auth.ErrUnauthenticated)
		return
	}
	if err := s.accessControl.RequireViewSecrets(r.Context(), session.Username, session.IsAdmin, project); err != nil {
		writeAccessError(w, err)
		return
	}
	if err := s.requireBoundProjectAWSCredential(r.Context(), project); err != nil {
		writeProjectAWSCredentialError(w, err)
		return
	}
	var request struct {
		Password string `json:"password"`
	}
	if err := decodeJSON(w, r, &request); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if err := s.authentication.ReauthenticateRequest(r, session.Username, request.Password); err != nil {
		request.Password = ""
		if errors.Is(err, auth.ErrRateLimited) {
			writeError(w, http.StatusTooManyRequests, err)
		} else {
			writeError(w, http.StatusUnauthorized, auth.ErrInvalidCredentials)
		}
		return
	}
	request.Password = ""
	info, err := s.discoverEnvironmentJenkins(r, project, environmentKey, true)
	if err != nil {
		writeCICDError(w, err)
		return
	}
	if !info.CanConnect {
		writeError(w, http.StatusConflict, errors.New(info.Reason))
		return
	}
	endpoint := cicd.ManagedEndpoint{ProjectKey: project, EnvironmentKey: environmentKey, TargetName: info.TargetName, Region: info.Region, ClusterName: info.ClusterName, Namespace: info.Namespace, ServiceName: info.ServiceName, ServicePort: info.ServicePort}
	if _, err := s.cicd.PrepareManagedEndpoint(r.Context(), endpoint); err != nil {
		writeError(w, http.StatusFailedDependency, fmt.Errorf("无法建立到当前环境 Jenkins 的 EKS 安全隧道: %w", err))
		return
	}
	if s.resources == nil {
		writeError(w, http.StatusServiceUnavailable, errors.New("resource center is not configured"))
		return
	}
	secretRef := strings.Join([]string{info.Namespace, info.SecretName, info.SecretKey}, "/")
	values, err := s.resources.RevealKubernetesSecretAt(r.Context(), project, info.TargetName, secretRef)
	if err != nil {
		writeError(w, http.StatusFailedDependency, errors.New("无法读取 Jenkins 管理凭据，请检查 EKS Access Entry、Namespace 和 Jenkins Secret"))
		return
	}
	jenkinsPassword := strings.TrimSpace(values[info.SecretKey])
	if jenkinsPassword == "" {
		jenkinsPassword = strings.TrimSpace(values["password"])
	}
	for key := range values {
		values[key] = ""
		delete(values, key)
	}
	if jenkinsPassword == "" {
		writeError(w, http.StatusFailedDependency, errors.New("Jenkins Secret 中没有可用的管理密码"))
		return
	}
	connection, err := s.cicd.SaveManagedConnection(r.Context(), cicd.ManagedConnectionInput{
		Key: info.ConnectionKey, ProjectKey: project, EnvironmentKey: environmentKey, TargetName: info.TargetName,
		DisplayName: access.EnvironmentName(environmentKey) + " Jenkins", Region: info.Region, ClusterName: info.ClusterName,
		Namespace: info.Namespace, ServiceName: info.ServiceName, ServicePort: info.ServicePort, Username: info.Username, Password: jenkinsPassword,
	})
	jenkinsPassword = ""
	if err != nil {
		writeCICDError(w, err)
		return
	}
	connection, err = s.cicd.TestConnection(r.Context(), project, connection.Key)
	if err != nil {
		writeCICDError(w, fmt.Errorf("已保存环境 Jenkins 连接，但 API 健康检查失败: %w", err))
		return
	}
	info.Connected, info.ConnectionHealthy = true, true
	writeJSON(w, http.StatusOK, map[string]any{"connection": connection, "jenkins": info})
}

func (s *Server) discoverEnvironmentJenkins(r *http.Request, project, environmentKey string, fresh bool) (environmentJenkinsInfo, error) {
	info := environmentJenkinsInfo{Project: project, Environment: environmentKey, DeploymentStatus: "unknown", ConnectionMode: "eks_port_forward", ConnectionKey: environmentKey + "-jenkins"}
	if s.accessControl == nil || s.environments == nil {
		return info, errors.New("project environment service is unavailable")
	}
	target, err := s.accessControl.Environment(r.Context(), project, environmentKey)
	if err != nil {
		return info, err
	}
	doc, err := s.environments.Load(target.TargetName)
	if err != nil {
		return info, err
	}
	doc = environment.ApplyDefaults(doc, project, environmentKey)
	raw, ok := environment.GetPath(doc, "components.catalog.jenkins")
	config, ok := raw.(map[string]any)
	if !ok {
		info.Reason = "当前环境没有 Jenkins 组件配置"
		return info, nil
	}
	info.Enabled = cicdBool(config["enabled"])
	info.TargetName, info.Region, info.ClusterName = target.TargetName, cicdString(doc["region"]), environment.ClusterName(doc)
	info.Namespace = cicdDefault(cicdString(config["namespace"]), "platform-server")
	info.ServiceName = cicdDefault(cicdString(config["service_name"]), "jenkins")
	info.ServicePort = cicdInt(config["service_port"])
	if info.ServicePort == 0 {
		info.ServicePort = 8080
	}
	info.Username = cicdDefault(cicdString(config["username"]), "admin")
	info.SecretName = cicdDefault(cicdString(config["secret_name"]), "jenkins")
	info.SecretKey = cicdDefault(cicdString(config["secret_key"]), "jenkins-admin-password")
	info.InternalURL = fmt.Sprintf("http://%s.%s.svc.cluster.local:%d", info.ServiceName, info.Namespace, info.ServicePort)
	if domain := cicdString(config["domain"]); domain != "" {
		scheme := "http"
		if cicdBool(config["tls"]) {
			scheme = "https"
		}
		info.ExternalURL = scheme + "://" + domain
	}
	if !info.Enabled {
		info.DeploymentStatus = "disabled"
		info.Reason = "当前环境尚未启用 Jenkins，请先在阶段2组件中启用并部署"
	}
	if s.status != nil && info.Enabled {
		var reportErr error
		var report *statusservice.Report
		if fresh {
			report, reportErr = s.status.CollectFresh(r.Context(), target.TargetName)
		} else {
			report, reportErr = s.status.Collect(r.Context(), target.TargetName)
		}
		if reportErr != nil {
			info.Reason = "无法确认 Jenkins 部署状态：" + reportErr.Error()
		} else if report != nil {
			for _, component := range report.Components {
				if component.Key == "jenkins" {
					info.DeploymentStatus = component.Status
					info.DeploymentDetail = component.Detail
					if component.Actual {
						info.CanConnect = true
					}
					break
				}
			}
		}
	}
	connections, listErr := s.cicd.ListConnections(r.Context(), project)
	if listErr != nil {
		return info, listErr
	}
	for _, connection := range connections {
		if connection.Key == info.ConnectionKey && connection.EnvironmentKey == environmentKey {
			info.Connected = true
			info.ConnectionHealthy = connection.LastCheckStatus == "healthy"
			break
		}
	}
	if info.Enabled && !info.CanConnect && info.Reason == "" {
		info.Reason = "Jenkins 尚未健康部署，请先完成阶段2部署或修复 Helm Release"
	}
	return info, nil
}

func cicdString(value any) string { result, _ := value.(string); return strings.TrimSpace(result) }
func cicdBool(value any) bool     { result, _ := value.(bool); return result }
func cicdInt(value any) int {
	switch typed := value.(type) {
	case int:
		return typed
	case int64:
		return int(typed)
	case float64:
		return int(typed)
	default:
		return 0
	}
}
func cicdDefault(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}

func (s *Server) cicdAvailable(w http.ResponseWriter) bool {
	if s.cicd == nil {
		writeError(w, http.StatusServiceUnavailable, errors.New("CI/CD service is unavailable"))
		return false
	}
	return true
}

func cicdRequestEnvironment(r *http.Request) (string, error) {
	pathEnvironment := strings.ToLower(strings.TrimSpace(r.PathValue("environment")))
	queryEnvironment := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("environment")))
	if pathEnvironment != "" && queryEnvironment != "" && pathEnvironment != queryEnvironment {
		return "", fmt.Errorf("%w：CI/CD 请求路径环境与查询环境不一致", cicd.ErrConflict)
	}
	environment := pathEnvironment
	if environment == "" {
		environment = queryEnvironment
	}
	switch environment {
	case "dev", "test", "uat", "prod":
		return environment, nil
	default:
		return "", fmt.Errorf("%w：CI/CD Jenkins 与凭据按环境隔离，请先选择 dev、test、uat 或 prod 环境", cicd.ErrInvalid)
	}
}

func cicdRequestEnvironmentMissing(r *http.Request) bool {
	return strings.TrimSpace(r.PathValue("environment")) == "" &&
		strings.TrimSpace(r.URL.Query().Get("environment")) == ""
}

func (s *Server) listCICDConnections(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("project")
	if _, err := s.requireProjectView(r, project); err != nil {
		writeAccessError(w, err)
		return
	}
	if !s.cicdAvailable(w) {
		return
	}
	environment, err := cicdRequestEnvironment(r)
	if err != nil {
		// Compatibility for a page-load race in older/open frontend sessions: an
		// unscoped read must never fall back to another environment, but it also
		// does not need to surface a user-facing configuration error. Returning an
		// empty list keeps environment isolation intact until the scoped request
		// follows after the project environment has initialized.
		if cicdRequestEnvironmentMissing(r) {
			writeJSON(w, http.StatusOK, map[string]any{"connections": []any{}})
			return
		}
		writeCICDError(w, err)
		return
	}
	items, err := s.cicd.ListConnectionsForEnvironment(r.Context(), project, environment)
	if err != nil {
		writeCICDError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"connections": items})
}
func (s *Server) createCICDConnection(w http.ResponseWriter, r *http.Request) {
	s.saveCICDConnection(w, r, "")
}
func (s *Server) updateCICDConnection(w http.ResponseWriter, r *http.Request) {
	s.saveCICDConnection(w, r, r.PathValue("connection"))
}
func (s *Server) saveCICDConnection(w http.ResponseWriter, r *http.Request, key string) {
	project := r.PathValue("project")
	if err := s.requireProjectConfigure(r, project); err != nil {
		writeAccessError(w, err)
		return
	}
	if !s.cicdAvailable(w) {
		return
	}
	var input cicd.ConnectionInput
	if err := decodeJSON(w, r, &input); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	environment, err := cicdRequestEnvironment(r)
	if err != nil {
		writeCICDError(w, err)
		return
	}
	if input.EnvironmentKey != "" && !strings.EqualFold(input.EnvironmentKey, environment) {
		writeCICDError(w, fmt.Errorf("%w：请求环境与 Jenkins 连接环境不一致", cicd.ErrConflict))
		return
	}
	input.EnvironmentKey = environment
	item, err := s.cicd.SaveConnection(r.Context(), project, key, input)
	input.APIToken = ""
	if err != nil {
		writeCICDError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, item)
}
func (s *Server) deleteCICDConnection(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("project")
	if err := s.requireProjectConfigure(r, project); err != nil {
		writeAccessError(w, err)
		return
	}
	if !s.cicdAvailable(w) {
		return
	}
	environment, err := cicdRequestEnvironment(r)
	if err != nil {
		writeCICDError(w, err)
		return
	}
	if _, err := s.cicd.GetConnectionForEnvironment(r.Context(), project, environment, r.PathValue("connection")); err != nil {
		writeCICDError(w, err)
		return
	}
	if err := s.cicd.DeleteConnection(r.Context(), project, r.PathValue("connection")); err != nil {
		writeCICDError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
func (s *Server) testCICDConnection(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("project")
	if err := s.requireProjectConfigure(r, project); err != nil {
		writeAccessError(w, err)
		return
	}
	if !s.cicdAvailable(w) {
		return
	}
	environment, err := cicdRequestEnvironment(r)
	if err != nil {
		writeCICDError(w, err)
		return
	}
	if _, err := s.cicd.GetConnectionForEnvironment(r.Context(), project, environment, r.PathValue("connection")); err != nil {
		writeCICDError(w, err)
		return
	}
	item, err := s.cicd.TestConnection(r.Context(), project, r.PathValue("connection"))
	if err != nil {
		writeCICDError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, item)
}

func (s *Server) listCICDCredentials(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("project")
	if _, err := s.requireProjectView(r, project); err != nil {
		writeAccessError(w, err)
		return
	}
	if !s.cicdAvailable(w) {
		return
	}
	environment, err := cicdRequestEnvironment(r)
	if err != nil {
		if cicdRequestEnvironmentMissing(r) {
			writeJSON(w, http.StatusOK, map[string]any{"credentials": []any{}})
			return
		}
		writeCICDError(w, err)
		return
	}
	items, err := s.cicd.ListCredentialsForEnvironment(r.Context(), project, environment)
	if err != nil {
		writeCICDError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"credentials": items})
}
func (s *Server) createCICDCredential(w http.ResponseWriter, r *http.Request) {
	s.saveCICDCredential(w, r, "")
}
func (s *Server) updateCICDCredential(w http.ResponseWriter, r *http.Request) {
	s.saveCICDCredential(w, r, r.PathValue("credential"))
}
func (s *Server) saveCICDCredential(w http.ResponseWriter, r *http.Request, key string) {
	project := r.PathValue("project")
	if err := s.requireProjectConfigure(r, project); err != nil {
		writeAccessError(w, err)
		return
	}
	if !s.cicdAvailable(w) {
		return
	}
	var input cicd.CredentialInput
	if err := decodeJSON(w, r, &input); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	environment, err := cicdRequestEnvironment(r)
	if err != nil {
		writeCICDError(w, err)
		return
	}
	if input.EnvironmentKey != "" && !strings.EqualFold(input.EnvironmentKey, environment) {
		writeCICDError(w, fmt.Errorf("%w：请求环境与 Jenkins 凭据环境不一致", cicd.ErrConflict))
		return
	}
	input.EnvironmentKey = environment
	item, err := s.cicd.SaveCredential(r.Context(), project, key, input)
	input.Password, input.SecretText, input.PrivateKey, input.Passphrase = "", "", "", ""
	if err != nil {
		writeCICDError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, item)
}
func (s *Server) deleteCICDCredential(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("project")
	if err := s.requireProjectConfigure(r, project); err != nil {
		writeAccessError(w, err)
		return
	}
	if !s.cicdAvailable(w) {
		return
	}
	environment, err := cicdRequestEnvironment(r)
	if err != nil {
		writeCICDError(w, err)
		return
	}
	if _, err := s.cicd.GetCredentialForEnvironment(r.Context(), project, environment, r.PathValue("credential")); err != nil {
		writeCICDError(w, err)
		return
	}
	if err := s.cicd.DeleteCredential(r.Context(), project, r.PathValue("credential")); err != nil {
		writeCICDError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
func (s *Server) syncCICDCredential(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("project")
	if err := s.requireProjectConfigure(r, project); err != nil {
		writeAccessError(w, err)
		return
	}
	if !s.cicdAvailable(w) {
		return
	}
	environment, err := cicdRequestEnvironment(r)
	if err != nil {
		writeCICDError(w, err)
		return
	}
	item, err := s.cicd.SyncCredentialForEnvironment(r.Context(), project, environment, r.PathValue("credential"))
	if err != nil {
		writeCICDError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, item)
}

func (s *Server) listCICDRepositories(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("project")
	if _, err := s.requireProjectView(r, project); err != nil {
		writeAccessError(w, err)
		return
	}
	if !s.cicdAvailable(w) {
		return
	}
	items, err := s.cicd.ListRepositories(r.Context(), project)
	if err != nil {
		writeCICDError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"repositories": items})
}

func (s *Server) createCICDRepository(w http.ResponseWriter, r *http.Request) {
	s.saveCICDRepository(w, r, "")
}

func (s *Server) updateCICDRepository(w http.ResponseWriter, r *http.Request) {
	s.saveCICDRepository(w, r, r.PathValue("repository"))
}

func (s *Server) saveCICDRepository(w http.ResponseWriter, r *http.Request, key string) {
	project := r.PathValue("project")
	if err := s.requireProjectConfigure(r, project); err != nil {
		writeAccessError(w, err)
		return
	}
	if !s.cicdAvailable(w) {
		return
	}
	var input cicd.Repository
	if err := decodeJSON(w, r, &input); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	item, err := s.cicd.SaveRepository(r.Context(), project, key, input)
	if err != nil {
		writeCICDError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, item)
}

func (s *Server) deleteCICDRepository(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("project")
	if err := s.requireProjectConfigure(r, project); err != nil {
		writeAccessError(w, err)
		return
	}
	if !s.cicdAvailable(w) {
		return
	}
	if err := s.cicd.DeleteRepository(r.Context(), project, r.PathValue("repository")); err != nil {
		writeCICDError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) analyzeCICDJenkinsfile(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("project")
	if err := s.requireProjectConfigure(r, project); err != nil {
		writeAccessError(w, err)
		return
	}
	if !s.cicdAvailable(w) {
		return
	}
	var input struct {
		Content string `json:"content"`
	}
	if err := decodeJSON(w, r, &input); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	analysis, err := s.cicd.AnalyzeJenkinsfile(input.Content)
	input.Content = ""
	if err != nil {
		writeCICDError(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, analysis)
}

func (s *Server) listCICDJobs(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("project")
	if _, err := s.requireProjectView(r, project); err != nil {
		writeAccessError(w, err)
		return
	}
	if !s.cicdAvailable(w) {
		return
	}
	items, err := s.cicd.ListJobs(r.Context(), project)
	if err != nil {
		writeCICDError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"jobs": items})
}
func (s *Server) createCICDJob(w http.ResponseWriter, r *http.Request) { s.saveCICDJob(w, r, "") }
func (s *Server) updateCICDJob(w http.ResponseWriter, r *http.Request) {
	s.saveCICDJob(w, r, r.PathValue("job"))
}
func (s *Server) saveCICDJob(w http.ResponseWriter, r *http.Request, key string) {
	project := r.PathValue("project")
	if err := s.requireProjectConfigure(r, project); err != nil {
		writeAccessError(w, err)
		return
	}
	if !s.cicdAvailable(w) {
		return
	}
	var input cicd.JobInput
	if err := decodeJSON(w, r, &input); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	job := input.Job()
	if strings.EqualFold(strings.TrimSpace(job.JenkinsfileMode), "generated") && (strings.TrimSpace(job.JenkinsfileCredential) == "" || strings.TrimSpace(job.ManifestCredential) == "") {
		if s.gitlab == nil {
			writeError(w, http.StatusServiceUnavailable, errors.New("GitLab integration service is unavailable"))
			return
		}
		credential, err := s.gitlab.SyncDeliveryCredential(r.Context(), project, job.ConnectionKey, s.cicd)
		if err != nil {
			writeGitLabError(w, err)
			return
		}
		if strings.TrimSpace(job.JenkinsfileCredential) == "" {
			job.JenkinsfileCredential = credential.Key
		}
		if strings.TrimSpace(job.ManifestCredential) == "" {
			job.ManifestCredential = credential.Key
		}
	}
	item, err := s.cicd.SaveJob(r.Context(), project, key, job)
	if err != nil {
		writeCICDError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, item)
}
func (s *Server) deleteCICDJob(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("project")
	if err := s.requireProjectConfigure(r, project); err != nil {
		writeAccessError(w, err)
		return
	}
	if !s.cicdAvailable(w) {
		return
	}
	deleteRemote := r.URL.Query().Get("delete_remote") == "true"
	result, err := s.cicd.DeleteJob(r.Context(), project, r.PathValue("job"), deleteRemote)
	if err != nil {
		writeCICDError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}
func (s *Server) cicdJobUsage(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("project")
	if _, err := s.requireProjectView(r, project); err != nil {
		writeAccessError(w, err)
		return
	}
	if !s.cicdAvailable(w) {
		return
	}
	usage, err := s.cicd.JobBuildUsage(r.Context(), project, r.PathValue("job"))
	if err != nil {
		writeCICDError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, usage)
}
func (s *Server) syncCICDJob(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("project")
	if err := s.requireProjectConfigure(r, project); err != nil {
		writeAccessError(w, err)
		return
	}
	if !s.cicdAvailable(w) {
		return
	}
	job, err := s.cicd.GetJob(r.Context(), project, r.PathValue("job"))
	if err != nil {
		writeCICDError(w, err)
		return
	}
	if s.gitlab != nil {
		delivery, deliveryErr := s.gitlab.GetDelivery(r.Context(), project)
		if deliveryErr != nil {
			_, _ = s.cicd.RecordJobSyncStageFailure(r.Context(), project, job.Key, "读取项目 GitLab 接入配置", deliveryErr)
			writeGitLabError(w, deliveryErr)
			return
		}
		if delivery.SourceServerKey != "" && len(job.ServiceKeys) > 0 {
			if _, err := s.gitlab.SyncSourceCredential(r.Context(), project, job.ConnectionKey, job.ServiceKeys, s.cicd); err != nil {
				_, _ = s.cicd.RecordJobSyncStageFailure(r.Context(), project, job.Key, "同步业务源码只读凭据", err)
				writeGitLabError(w, err)
				return
			}
		}
	}
	job, err = s.syncCICDJobCredentials(r.Context(), project, job)
	if err != nil {
		_, _ = s.cicd.RecordJobSyncStageFailure(r.Context(), project, job.Key, "同步 Jenkins 凭据", err)
		writeCICDError(w, err)
		return
	}
	job, err = s.syncCICDLarkCredential(r.Context(), project, job)
	if err != nil {
		_, _ = s.cicd.RecordJobSyncStageFailure(r.Context(), project, job.Key, "同步通知凭据", err)
		writeCICDError(w, err)
		return
	}
	if job.JenkinsfileMode == "generated" {
		if s.gitlab == nil {
			writeError(w, http.StatusServiceUnavailable, errors.New("GitLab integration service is unavailable"))
			return
		}
		needsManagedDeliveryCredential := job.JenkinsfileCredential == "" || job.JenkinsfileCredential == "gitlab-delivery-read" ||
			job.ManifestCredential == "" || job.ManifestCredential == "gitlab-delivery-read"
		if !needsManagedDeliveryCredential {
			expectedID := "ops-" + strings.ToLower(strings.TrimSpace(project)) + "-" + job.EnvironmentKey + "-gitlab-read"
			for _, credentialKey := range []string{job.JenkinsfileCredential, job.ManifestCredential} {
				credential, lookupErr := s.cicd.GetCredentialForEnvironment(r.Context(), project, job.EnvironmentKey, credentialKey)
				if lookupErr != nil || credential.ConnectionKey != job.ConnectionKey {
					needsManagedDeliveryCredential = true
					break
				}
				if strings.HasPrefix(credential.Description, "GitLab Group Deploy Token") && credential.ExternalID != expectedID {
					needsManagedDeliveryCredential = true
					break
				}
			}
		}
		if needsManagedDeliveryCredential {
			var managedCredential cicd.Credential
			managedCredential, err = s.gitlab.SyncDeliveryCredential(r.Context(), project, job.ConnectionKey, s.cicd)
			if err == nil {
				job.JenkinsfileCredential = managedCredential.Key
				job.ManifestCredential = managedCredential.Key
				job, err = s.cicd.SaveJob(r.Context(), project, job.Key, job)
			}
		}
		if err != nil {
			_, _ = s.cicd.RecordJobSyncStageFailure(r.Context(), project, job.Key, "同步项目交付只读凭据", err)
			writeGitLabError(w, err)
			return
		}
		if _, err = s.gitlab.Provision(r.Context(), project); err != nil {
			_, _ = s.cicd.RecordJobSyncStageFailure(r.Context(), project, job.Key, "准备项目交付仓库", err)
			writeGitLabError(w, err)
			return
		}
		job, err = s.cicd.PrepareGeneratedJobRepositories(r.Context(), project, job.Key)
		if err != nil {
			writeCICDError(w, err)
			return
		}
		if _, _, err := s.gitlab.SyncJobJenkinsfile(r.Context(), project, job); err != nil {
			_, _ = s.cicd.RecordJobSyncStageFailure(r.Context(), project, job.Key, "同步 GitLab Jenkinsfile", err)
			writeGitLabError(w, err)
			return
		}
	}
	item, err := s.cicd.SyncJob(r.Context(), project, r.PathValue("job"))
	if err != nil {
		writeCICDError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, item)
}

func (s *Server) rotateCICDJobWebhookSecret(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("project")
	if err := s.requireProjectConfigure(r, project); err != nil {
		writeAccessError(w, err)
		return
	}
	if !s.cicdAvailable(w) {
		return
	}
	secret, err := s.cicd.RotateWebhookSecret(r.Context(), project, r.PathValue("job"))
	if err != nil {
		writeCICDError(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, secret)
}

type gitLabPushWebhook struct {
	ObjectKind   string `json:"object_kind"`
	Ref          string `json:"ref"`
	After        string `json:"after"`
	CheckoutSHA  string `json:"checkout_sha"`
	UserUsername string `json:"user_username"`
	Project      struct {
		WebURL     string `json:"web_url"`
		GitHTTPURL string `json:"git_http_url"`
	} `json:"project"`
	Repository struct {
		GitHTTPURL string `json:"git_http_url"`
		Homepage   string `json:"homepage"`
	} `json:"repository"`
}

// receiveCICDGitLabWebhook deliberately bypasses the platform login session.
// Authentication is provided by GitLab's X-Gitlab-Token header, whose digest
// is stored with the Job. The token is never accepted in the URL or body.
func (s *Server) receiveCICDGitLabWebhook(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if s.cicd == nil {
		writeError(w, http.StatusServiceUnavailable, errors.New("CI/CD service is unavailable"))
		return
	}
	project, jobKey := r.PathValue("project"), r.PathValue("job")
	token := strings.TrimSpace(r.Header.Get("X-Gitlab-Token"))
	if len(token) > 4096 {
		writeError(w, http.StatusUnauthorized, cicd.ErrWebhookUnauthorized)
		return
	}
	job, err := s.cicd.AuthenticateGitLabWebhook(r.Context(), project, jobKey, token)
	if err != nil {
		if errors.Is(err, cicd.ErrWebhookUnauthorized) {
			writeError(w, http.StatusUnauthorized, cicd.ErrWebhookUnauthorized)
		} else {
			writeCICDError(w, err)
		}
		return
	}
	if !strings.EqualFold(strings.TrimSpace(r.Header.Get("X-Gitlab-Event")), "Push Hook") {
		writeJSON(w, http.StatusAccepted, map[string]any{"status": "ignored", "reason": "only Push Hook events are enabled"})
		return
	}

	defer r.Body.Close()
	var payload gitLabPushWebhook
	r.Body = http.MaxBytesReader(w, r.Body, 2<<20)
	decoder := json.NewDecoder(r.Body)
	if err := decoder.Decode(&payload); err != nil {
		writeError(w, http.StatusBadRequest, errors.New("invalid GitLab webhook payload"))
		return
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		writeError(w, http.StatusBadRequest, errors.New("invalid GitLab webhook payload"))
		return
	}
	if payload.ObjectKind != "" && !strings.EqualFold(payload.ObjectKind, "push") {
		writeJSON(w, http.StatusAccepted, map[string]any{"status": "ignored", "reason": "event payload is not a push"})
		return
	}
	branch, ok := strings.CutPrefix(strings.TrimSpace(payload.Ref), "refs/heads/")
	if !ok || branch == "" || branch != job.TriggerBranch {
		writeJSON(w, http.StatusAccepted, map[string]any{"status": "ignored", "reason": "branch does not match", "branch": branch})
		return
	}
	if allZeroGitSHA(payload.After) {
		writeJSON(w, http.StatusAccepted, map[string]any{"status": "ignored", "reason": "branch deletion does not trigger a build"})
		return
	}
	if s.gitlab == nil {
		writeError(w, http.StatusServiceUnavailable, errors.New("GitLab integration service is unavailable"))
		return
	}
	delivery, err := s.gitlab.GetDelivery(r.Context(), project)
	if err != nil {
		writeError(w, http.StatusFailedDependency, errors.New("unable to load the project delivery catalog"))
		return
	}
	repositoryURLs := []string{payload.Project.GitHTTPURL, payload.Repository.GitHTTPURL, payload.Project.WebURL, payload.Repository.Homepage}
	services := gitLabWebhookServices(job, delivery.Services, repositoryURLs)
	if len(services) == 0 {
		writeJSON(w, http.StatusAccepted, map[string]any{"status": "ignored", "reason": "repository is not registered in this Job"})
		return
	}
	environmentKey := strings.ToLower(strings.TrimSpace(job.EnvironmentKey))
	if environmentKey == "" {
		connection, err := s.cicd.GetConnection(r.Context(), project, job.ConnectionKey)
		if err != nil {
			writeError(w, http.StatusFailedDependency, errors.New("unable to resolve the Jenkins environment for this Job"))
			return
		}
		environmentKey = strings.TrimSpace(connection.EnvironmentKey)
		if environmentKey == "" {
			environmentKey = strings.ToLower(strings.TrimSpace(job.Parameters["DEPLOY_ENV"]))
		}
	}
	if environmentKey == "" {
		writeError(w, http.StatusConflict, errors.New("the Job is not bound to a deployment environment"))
		return
	}
	deliveryID := gitLabWebhookDeliveryID(r, payload, repositoryURLs)
	requestedBy := "gitlab-webhook"
	if username := strings.TrimSpace(payload.UserUsername); username != "" {
		requestedBy = "gitlab:" + username
		if len(requestedBy) > 64 {
			requestedBy = requestedBy[:64]
		}
	}
	builds := make([]cicd.Build, 0, len(services))
	for _, serviceKey := range services {
		build, triggerErr := s.cicd.TriggerBuild(r.Context(), project, job.Key, requestedBy, cicd.BuildInput{
			Environment: environmentKey,
			Branch:      branch,
			Services:    []string{serviceKey},
			Parameters:  map[string]string{cicd.WebhookDeliveryParameter: deliveryID + ":" + serviceKey},
		})
		if triggerErr != nil {
			writeCICDError(w, triggerErr)
			return
		}
		builds = append(builds, build)
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"status": "accepted", "branch": branch, "services": services, "builds": builds})
}

func gitLabWebhookServices(job cicd.Job, catalog []gitlab.ServiceSpec, repositoryURLs []string) []string {
	incoming := map[string]bool{}
	for _, raw := range repositoryURLs {
		if normalized := normalizeGitRepository(raw); normalized != "" {
			incoming[normalized] = true
		}
	}
	allowed := map[string]bool{}
	for _, key := range job.ServiceKeys {
		allowed[key] = true
	}
	result := make([]string, 0, len(job.ServiceKeys))
	seen := map[string]bool{}
	for _, service := range catalog {
		if !allowed[service.Key] || seen[service.Key] || !incoming[normalizeGitRepository(service.SourceRepository)] {
			continue
		}
		seen[service.Key] = true
		result = append(result, service.Key)
	}
	if len(result) == 0 && incoming[normalizeGitRepository(job.SourceRepo)] && len(job.ServiceKeys) == 1 {
		result = append(result, job.ServiceKeys[0])
	}
	return result
}

func normalizeGitRepository(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Hostname() == "" || parsed.User != nil {
		return ""
	}
	path := strings.TrimSuffix(strings.TrimSuffix(parsed.EscapedPath(), "/"), ".git")
	decoded, err := url.PathUnescape(path)
	if err != nil {
		return ""
	}
	return strings.ToLower(parsed.Host + "/" + strings.TrimPrefix(decoded, "/"))
}

func allZeroGitSHA(value string) bool {
	value = strings.TrimSpace(value)
	return len(value) >= 40 && strings.Trim(value, "0") == ""
}

func gitLabWebhookDeliveryID(r *http.Request, payload gitLabPushWebhook, repositoryURLs []string) string {
	for _, header := range []string{"X-Gitlab-Event-UUID", "X-Gitlab-Webhook-UUID"} {
		if value := strings.TrimSpace(r.Header.Get(header)); value != "" {
			if len(value) <= 64 {
				return value
			}
			digest := sha256.Sum256([]byte(value))
			return hex.EncodeToString(digest[:])
		}
	}
	commit := strings.TrimSpace(payload.After)
	if commit == "" {
		commit = strings.TrimSpace(payload.CheckoutSHA)
	}
	repository := ""
	for _, candidate := range repositoryURLs {
		if repository = normalizeGitRepository(candidate); repository != "" {
			break
		}
	}
	digest := sha256.Sum256([]byte(strings.Join([]string{payload.Ref, commit, repository}, "\x00")))
	return hex.EncodeToString(digest[:])
}

type cicdNotificationChannel struct {
	Name        string `json:"name"`
	Type        string `json:"type"`
	Environment string `json:"environment"`
	Configured  bool   `json:"configured"`
}

func (s *Server) listCICDNotificationChannels(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("project")
	if _, err := s.requireProjectView(r, project); err != nil {
		writeAccessError(w, err)
		return
	}
	if !s.cicdAvailable(w) {
		return
	}
	environmentKey := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("environment")))
	if connectionKey := strings.TrimSpace(r.URL.Query().Get("connection")); connectionKey != "" {
		connection, err := s.cicd.GetConnection(r.Context(), project, connectionKey)
		if err != nil {
			writeCICDError(w, err)
			return
		}
		if connection.EnvironmentKey != "" {
			environmentKey = connection.EnvironmentKey
		}
	}
	channels, err := s.cicDLarkChannels(r.Context(), project, environmentKey)
	if err != nil {
		writeCICDError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"channels": channels})
}

func (s *Server) cicDLarkChannels(ctx context.Context, project, environmentKey string) ([]cicdNotificationChannel, error) {
	if s.accessControl == nil || s.environments == nil {
		return nil, errors.New("project environment service is unavailable")
	}
	environmentKey = strings.ToLower(strings.TrimSpace(environmentKey))
	if environmentKey == "" {
		return nil, fmt.Errorf("%w：请先选择项目环境，告警通道按环境隔离", cicd.ErrInvalid)
	}
	target, err := s.accessControl.Environment(ctx, project, environmentKey)
	if err != nil {
		return nil, err
	}
	doc, err := s.environments.Load(target.TargetName)
	if err != nil {
		return nil, err
	}
	doc = environment.ApplyDefaults(doc, project, environmentKey)
	alerting, _ := doc["alerting"].(map[string]any)
	configured := configuredAlertChannels(alerting)
	result := make([]cicdNotificationChannel, 0, len(configured))
	for _, channel := range configured {
		if normalizedAlertProvider(channel.Type, channel.Address) != "lark" {
			continue
		}
		result = append(result, cicdNotificationChannel{Name: channel.Name, Type: "lark", Environment: environmentKey, Configured: channel.Address != ""})
	}
	return result, nil
}

func (s *Server) syncCICDJobCredentials(ctx context.Context, project string, job cicd.Job) (cicd.Job, error) {
	seen := map[string]bool{}
	for _, key := range []string{job.JenkinsfileCredential, job.ManifestCredential} {
		key = strings.ToLower(strings.TrimSpace(key))
		if key == "" || key == "gitlab-delivery-read" || seen[key] {
			continue
		}
		seen[key] = true
		if _, err := s.cicd.SyncCredentialForEnvironment(ctx, project, job.EnvironmentKey, key); err != nil {
			return job, fmt.Errorf("同步 GitLab 仓库凭据 %s 到 Jenkins 失败：%w", key, err)
		}
	}
	return s.cicd.GetJob(ctx, project, job.Key)
}

func (s *Server) syncCICDLarkCredential(ctx context.Context, project string, job cicd.Job) (cicd.Job, error) {
	channelName := strings.TrimSpace(job.Parameters["LARK_ALERT_CHANNEL"])
	if channelName == "" {
		return s.cicd.SetManagedJobParameter(ctx, project, job.Key, "LARK_CREDENTIALS_ID", "")
	}
	if s.accessControl == nil || s.environments == nil {
		return job, errors.New("project environment service is unavailable")
	}
	connection, err := s.cicd.GetConnection(ctx, project, job.ConnectionKey)
	if err != nil {
		return job, err
	}
	environmentKey := strings.ToLower(strings.TrimSpace(connection.EnvironmentKey))
	if environmentKey == "" {
		environmentKey = strings.ToLower(strings.TrimSpace(job.Parameters["LARK_ALERT_ENVIRONMENT"]))
	}
	if environmentKey == "" {
		return job, fmt.Errorf("%w：当前 Jenkins 未绑定环境，无法确定 Lark 告警通道；请重新选择当前项目环境", cicd.ErrInvalid)
	}
	target, err := s.accessControl.Environment(ctx, project, environmentKey)
	if err != nil {
		return job, err
	}
	doc, err := s.environments.Load(target.TargetName)
	if err != nil {
		return job, err
	}
	doc = environment.ApplyDefaults(doc, project, environmentKey)
	channel, err := findAlertChannel(doc, channelName)
	if err != nil {
		return job, fmt.Errorf("%w：Lark 告警通道 %q 不存在，请先在告警管理中保存", cicd.ErrInvalid, channelName)
	}
	if normalizedAlertProvider(channel.Type, channel.Address) != "lark" {
		return job, fmt.Errorf("%w：告警通道 %q 不是 Lark / 飞书类型", cicd.ErrInvalid, channelName)
	}
	if channel.Address == "" {
		return job, fmt.Errorf("%w：Lark 告警通道 %q 没有可同步的 Webhook 地址；外部密钥引用暂不能同步到 Jenkins", cicd.ErrInvalid, channelName)
	}
	credentialKey, externalID := managedLarkCredentialIdentity(project, job.ConnectionKey, environmentKey, channelName)
	credential, err := s.cicd.SaveCredential(ctx, project, credentialKey, cicd.CredentialInput{
		Key: credentialKey, ConnectionKey: job.ConnectionKey,
		DisplayName: fmt.Sprintf("Lark 告警 · %s / %s", environmentKey, channelName), Kind: "secret_text", ExternalID: externalID,
		Description: "由环境告警通道自动同步，Webhook 仅加密保存在平台和 Jenkins Credential 中", SecretText: channel.Address,
	})
	channel.Address = ""
	if err != nil {
		return job, err
	}
	if _, err := s.cicd.SyncCredentialForEnvironment(ctx, project, environmentKey, credential.Key); err != nil {
		return job, fmt.Errorf("同步 Lark Webhook 到 Jenkins 失败：%w", err)
	}
	if _, err := s.cicd.SetManagedJobParameter(ctx, project, job.Key, "LARK_ALERT_ENVIRONMENT", environmentKey); err != nil {
		return job, err
	}
	return s.cicd.SetManagedJobParameter(ctx, project, job.Key, "LARK_CREDENTIALS_ID", credential.ExternalID)
}

func managedLarkCredentialIdentity(project, connection, environmentKey, channel string) (string, string) {
	digest := sha256.Sum256([]byte(strings.Join([]string{strings.TrimSpace(project), strings.TrimSpace(connection), strings.TrimSpace(environmentKey), strings.TrimSpace(channel)}, "\x00")))
	suffix := fmt.Sprintf("%x", digest[:8])
	return "alert-lark-" + suffix, "ops-lark-" + suffix
}

func (s *Server) validateCICDBuildSources(ctx context.Context, project string, job cicd.Job, serviceKeys []string) error {
	if s.gitlab == nil || s.cicd == nil {
		return fmt.Errorf("%w: CI/CD 源码仓库预检服务不可用", cicd.ErrConflict)
	}
	delivery, err := s.gitlab.GetDelivery(ctx, project)
	if err != nil {
		return fmt.Errorf("%w: 无法读取项目服务目录：%v", cicd.ErrInvalid, err)
	}
	services := make(map[string]gitlab.ServiceSpec, len(delivery.Services))
	for _, service := range delivery.Services {
		services[strings.ToLower(strings.TrimSpace(service.Key))] = service
	}
	credentials, err := s.cicd.ListCredentials(ctx, project)
	if err != nil {
		return err
	}
	for _, key := range serviceKeys {
		service, ok := services[strings.ToLower(strings.TrimSpace(key))]
		if !ok {
			return fmt.Errorf("%w: 服务 %s 尚未在“服务与清单”登记", cicd.ErrInvalid, key)
		}
		if delivery.SourceServerKey != "" {
			if err := s.gitlab.ValidateSourceRepository(ctx, project, service.Key); err != nil {
				return fmt.Errorf("%w: 服务 %s 的业务源码 GitLab 预检失败：%v", cicd.ErrInvalid, service.Key, err)
			}
			continue
		}
		var validation cicd.GitCredentialValidation
		if credentialID := strings.TrimSpace(service.SourceCredentialID); credentialID != "" {
			credentialKey := ""
			for _, credential := range credentials {
				if credential.ConnectionKey == job.ConnectionKey && credential.EnvironmentKey == job.EnvironmentKey && credential.ExternalID == credentialID && credential.SyncStatus == "ready" {
					credentialKey = credential.Key
					break
				}
			}
			if credentialKey == "" {
				return fmt.Errorf("%w: 服务 %s 引用的源码 Credential ID %s 未同步到当前 Jenkins；请在“Jenkins 设置”添加或重新同步凭据", cicd.ErrInvalid, service.Key, credentialID)
			}
			validation, err = s.cicd.ValidateGitCredential(ctx, project, credentialKey, service.SourceRepository)
		} else {
			validation, err = s.cicd.ValidateGitRepository(ctx, service.SourceRepository)
		}
		if err != nil {
			return fmt.Errorf("%w: 服务 %s 源码仓库预检失败：%v", cicd.ErrInvalid, service.Key, err)
		}
		if !validation.SmartHTTP {
			if strings.TrimSpace(service.SourceCredentialID) == "" {
				return fmt.Errorf("%w: 服务 %s 的源码仓库无法匿名读取（HTTP %d）；请在“Jenkins 设置”添加该 GitLab 的 HTTPS Token，再到“服务与清单”选择源码 Credential ID", cicd.ErrInvalid, service.Key, validation.HTTPStatus)
			}
			return fmt.Errorf("%w: 服务 %s 的源码凭据 %s 无权读取仓库（HTTP %d）；请检查 Token 的 read_repository 权限和项目访问范围", cicd.ErrInvalid, service.Key, service.SourceCredentialID, validation.HTTPStatus)
		}
	}
	return nil
}

func (s *Server) triggerCICDBuild(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("project")
	if err := s.requireProjectDeploy(r, project); err != nil {
		writeAccessError(w, err)
		return
	}
	if !s.cicdAvailable(w) {
		return
	}
	var input cicd.BuildInput
	if err := decodeJSON(w, r, &input); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if s.accessControl == nil {
		writeError(w, http.StatusServiceUnavailable, errors.New("project access service is unavailable"))
		return
	}
	if _, err := s.accessControl.Environment(r.Context(), project, strings.ToLower(strings.TrimSpace(input.Environment))); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			writeError(w, http.StatusConflict, errors.New("所选项目环境尚未创建；请先在“项目与环境”中创建 dev、test、uat 或 prod 环境，再触发 CI/CD 构建"))
			return
		}
		writeAccessError(w, err)
		return
	}
	session, _ := auth.SessionFromContext(r.Context())
	item, err := s.cicd.TriggerBuild(r.Context(), project, r.PathValue("job"), session.Username, input)
	if err != nil {
		writeCICDError(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, item)
}
func (s *Server) listCICDBuilds(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("project")
	if _, err := s.requireProjectView(r, project); err != nil {
		writeAccessError(w, err)
		return
	}
	if !s.cicdAvailable(w) {
		return
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	items, err := s.cicd.ListBuilds(r.Context(), project, strings.ToLower(strings.TrimSpace(r.URL.Query().Get("environment"))), limit)
	if err != nil {
		writeCICDError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"builds": items})
}
func (s *Server) retryCICDBuild(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("project")
	if err := s.requireProjectDeploy(r, project); err != nil {
		writeAccessError(w, err)
		return
	}
	if !s.cicdAvailable(w) {
		return
	}
	session, _ := auth.SessionFromContext(r.Context())
	item, err := s.cicd.RetryBuild(r.Context(), project, r.PathValue("build"), session.Username)
	if err != nil {
		writeCICDError(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, item)
}
func (s *Server) cancelCICDBuild(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("project")
	if err := s.requireProjectDeploy(r, project); err != nil {
		writeAccessError(w, err)
		return
	}
	if !s.cicdAvailable(w) {
		return
	}
	item, err := s.cicd.CancelBuild(r.Context(), project, r.PathValue("build"))
	if err != nil {
		writeCICDError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, item)
}
func (s *Server) cicdBuildLogs(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("project")
	if _, err := s.requireProjectView(r, project); err != nil {
		writeAccessError(w, err)
		return
	}
	if !s.cicdAvailable(w) {
		return
	}
	offset, _ := strconv.ParseInt(r.URL.Query().Get("offset"), 10, 64)
	item, err := s.cicd.BuildLogs(r.Context(), project, r.PathValue("build"), offset)
	if err != nil {
		writeCICDError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, item)
}

func (s *Server) cicdBuildDeploymentLogs(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("project")
	if _, err := s.requireProjectView(r, project); err != nil {
		writeAccessError(w, err)
		return
	}
	if !s.cicdAvailable(w) {
		return
	}
	item, err := s.cicd.BuildDeploymentLogs(r.Context(), project, r.PathValue("build"))
	if err != nil {
		writeCICDError(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, item)
}

func writeCICDError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, os.ErrNotExist), errors.Is(err, cicd.ErrNotFound):
		writeError(w, http.StatusNotFound, err)
	case errors.Is(err, cicd.ErrInvalid), errors.Is(err, cicd.ErrCredentialSecret):
		writeError(w, http.StatusBadRequest, err)
	case errors.Is(err, cicd.ErrConflict):
		writeError(w, http.StatusConflict, err)
	case errors.Is(err, access.ErrForbidden):
		writeError(w, http.StatusForbidden, err)
	case errors.Is(err, cicd.ErrJenkins):
		writeError(w, http.StatusBadGateway, err)
	default:
		writeError(w, http.StatusInternalServerError, err)
	}
}

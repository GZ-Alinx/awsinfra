package gitlab

import (
	"context"
	"errors"
	"time"

	"ops-deploy-platform/internal/cicd"
)

var (
	ErrInvalid  = errors.New("GitLab 配置不合法")
	ErrNotFound = errors.New("GitLab 配置不存在")
	ErrConflict = errors.New("GitLab 交付仓库冲突")
	ErrRequest  = errors.New("GitLab 请求失败")
)

type Server struct {
	Key               string    `json:"key"`
	DisplayName       string    `json:"display_name"`
	BaseURL           string    `json:"base_url"`
	RootGroup         string    `json:"root_group"`
	RootGroups        []string  `json:"root_groups"`
	DefaultBranch     string    `json:"default_branch"`
	Visibility        string    `json:"visibility"`
	AllowInsecureHTTP bool      `json:"allow_insecure_http"`
	Configured        bool      `json:"configured"`
	LastCheckStatus   string    `json:"last_check_status,omitempty"`
	LastCheckError    string    `json:"last_check_error,omitempty"`
	LastCheckedAt     time.Time `json:"last_checked_at,omitempty"`
	CreatedAt         time.Time `json:"created_at"`
	UpdatedAt         time.Time `json:"updated_at"`
}

type StoredServer struct {
	Server
	TokenCipher string
}

type ServerInput struct {
	Key               string   `json:"key"`
	DisplayName       string   `json:"display_name"`
	BaseURL           string   `json:"base_url"`
	AccessToken       string   `json:"access_token"`
	RootGroup         string   `json:"root_group"`
	RootGroups        []string `json:"root_groups"`
	DefaultBranch     string   `json:"default_branch"`
	Visibility        string   `json:"visibility"`
	AllowInsecureHTTP bool     `json:"allow_insecure_http"`
}

// SecretKeyReference points at an existing Kubernetes Secret. The platform
// persists only this reference; the secret value never enters MySQL, GitLab,
// Jenkins parameters, generated files, or build logs.
type SecretKeyReference struct {
	SecretName string `json:"secret_name"`
	SecretKey  string `json:"secret_key"`
}

type ServiceSpec struct {
	Key                        string                        `json:"key"`
	DisplayName                string                        `json:"display_name"`
	WorkloadType               string                        `json:"workload_type"`
	Language                   string                        `json:"language"`
	RuntimeVersion             string                        `json:"runtime_version"`
	SourceRepository           string                        `json:"source_repository"`
	SourceBranch               string                        `json:"source_branch"`
	SourceCredentialID         string                        `json:"source_credential_id,omitempty"`
	ManifestCredentialID       string                        `json:"manifest_credential_id,omitempty"`
	BuildCommand               string                        `json:"build_command,omitempty"`
	BuildContext               string                        `json:"build_context"`
	DockerfileSource           string                        `json:"dockerfile_source,omitempty"`
	Dockerfile                 string                        `json:"dockerfile"`
	DockerfileContent          string                        `json:"dockerfile_content,omitempty"`
	DockerfileContents         map[string]string             `json:"dockerfile_contents,omitempty"`
	ManifestMode               string                        `json:"manifest_mode,omitempty"`
	DockerTarget               string                        `json:"docker_target,omitempty"`
	RunEnvironment             string                        `json:"run_environment,omitempty"`
	ImageRepository            string                        `json:"image_repository"`
	ImagePullSecrets           []string                      `json:"image_pull_secrets,omitempty"`
	ImagePullPolicy            string                        `json:"image_pull_policy"`
	Namespace                  string                        `json:"namespace"`
	WorkloadClass              string                        `json:"workload_class,omitempty"`
	ContainerPort              int                           `json:"container_port"`
	Replicas                   int                           `json:"replicas"`
	RevisionHistoryLimit       int                           `json:"revision_history_limit"`
	Timezone                   string                        `json:"timezone"`
	JavaOptions                []string                      `json:"java_options,omitempty"`
	EnvironmentVariables       map[string]string             `json:"environment_variables,omitempty"`
	SecretEnvironmentVariables map[string]SecretKeyReference `json:"secret_environment_variables,omitempty"`
	CPURequest                 string                        `json:"cpu_request"`
	MemoryRequest              string                        `json:"memory_request"`
	CPULimit                   string                        `json:"cpu_limit"`
	MemoryLimit                string                        `json:"memory_limit"`
	HealthPath                 string                        `json:"health_path,omitempty"`
	EtcdConfigEnabled          bool                          `json:"etcd_config_enabled,omitempty"`
	EtcdHosts                  []string                      `json:"etcd_hosts,omitempty"`
	EtcdConfigKey              string                        `json:"etcd_config_key,omitempty"`
	EtcdUsername               string                        `json:"etcd_username,omitempty"`
	EtcdPasswordCredentialID   string                        `json:"etcd_password_credential_id,omitempty"`
	EtcdConfigFile             string                        `json:"etcd_config_file,omitempty"`
	EtcdMountPath              string                        `json:"etcd_mount_path,omitempty"`
	NginxServerConfig          string                        `json:"nginx_server_config,omitempty"`
}

type ProjectDelivery struct {
	ProjectKey              string        `json:"project_key"`
	ServerKey               string        `json:"server_key"`
	RootGroup               string        `json:"root_group"`
	SourceServerKey         string        `json:"source_server_key,omitempty"`
	SourceRootGroup         string        `json:"source_root_group,omitempty"`
	DefaultBranch           string        `json:"default_branch"`
	JenkinsfilesProjectID   int64         `json:"jenkinsfiles_project_id,omitempty"`
	JenkinsfilesProjectPath string        `json:"jenkinsfiles_project_path"`
	JenkinsfilesCloneURL    string        `json:"jenkinsfiles_clone_url,omitempty"`
	ManifestsProjectID      int64         `json:"manifests_project_id,omitempty"`
	ManifestsProjectPath    string        `json:"manifests_project_path"`
	ManifestsCloneURL       string        `json:"manifests_clone_url,omitempty"`
	Services                []ServiceSpec `json:"services"`
	ProvisionStatus         string        `json:"provision_status,omitempty"`
	ProvisionError          string        `json:"provision_error,omitempty"`
	LastProvisionedAt       time.Time     `json:"last_provisioned_at,omitempty"`
	CreatedAt               time.Time     `json:"created_at"`
	UpdatedAt               time.Time     `json:"updated_at"`
}

type SourceRepositoryOption struct {
	ProjectID       int64  `json:"project_id"`
	Name            string `json:"name"`
	Path            string `json:"path"`
	RootGroup       string `json:"root_group"`
	CloneURL        string `json:"clone_url"`
	DefaultBranch   string `json:"default_branch"`
	SourceServerKey string `json:"source_server_key"`
}

type SourceBranchOption struct {
	Name      string `json:"name"`
	Default   bool   `json:"default"`
	Protected bool   `json:"protected"`
}

type SourceFileCheck struct {
	ProjectID int64  `json:"project_id"`
	Branch    string `json:"branch"`
	Path      string `json:"path"`
	Exists    bool   `json:"exists"`
}

type GeneratedFile struct {
	Repository string `json:"repository"`
	Path       string `json:"path"`
	Content    string `json:"content"`
}

type RepositoryPlan struct {
	Kind        string `json:"kind"`
	Name        string `json:"name"`
	ProjectPath string `json:"project_path"`
	CloneURL    string `json:"clone_url,omitempty"`
	Exists      bool   `json:"exists"`
	Managed     bool   `json:"managed"`
}

type Preview struct {
	ProjectKey   string           `json:"project_key"`
	Repositories []RepositoryPlan `json:"repositories"`
	Files        []GeneratedFile  `json:"files"`
}

type ProvisionResult struct {
	Delivery     ProjectDelivery  `json:"delivery"`
	Repositories []RepositoryPlan `json:"repositories"`
	CreatedFiles int              `json:"created_files"`
	UpdatedFiles int              `json:"updated_files"`
	DeletedFiles int              `json:"deleted_files"`
}

type SourceFile struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}

type SourceRepositoryResult struct {
	ProjectID int64  `json:"project_id"`
	FullPath  string `json:"full_path"`
	CloneURL  string `json:"clone_url"`
	Created   bool   `json:"created"`
	Files     int    `json:"files"`
}

type ServerAliasCheck struct {
	BaseURL                string `json:"base_url"`
	Authenticated          bool   `json:"authenticated"`
	JenkinsfilesProjectID  int64  `json:"jenkinsfiles_project_id,omitempty"`
	JenkinsfilesCloneURL   string `json:"jenkinsfiles_clone_url,omitempty"`
	ManifestsProjectID     int64  `json:"manifests_project_id,omitempty"`
	ManifestsCloneURL      string `json:"manifests_clone_url,omitempty"`
	MatchesCurrentProjects bool   `json:"matches_current_projects"`
}

type RepositoryEndpoints struct {
	ProjectID   int64  `json:"project_id"`
	ProjectPath string `json:"project_path"`
	HTTPS       string `json:"https"`
	SSH         string `json:"ssh"`
}

type Store interface {
	ListGitLabServers(context.Context) ([]StoredServer, error)
	GetGitLabServer(context.Context, string) (StoredServer, error)
	SaveGitLabServer(context.Context, StoredServer) error
	DeleteGitLabServer(context.Context, string) error
	GitLabServerBindingCount(context.Context, string) (int, error)
	GitLabServerBindingRootGroups(context.Context, string) ([]string, error)
	GitLabServerSourceRepositories(context.Context, string) ([]string, error)
	GetProjectGitLabDelivery(context.Context, string) (ProjectDelivery, error)
	SaveProjectGitLabDelivery(context.Context, ProjectDelivery) error
	DetachProjectGitLabDelivery(context.Context, string) error
	GetCICDRepository(context.Context, string, string) (cicd.Repository, error)
	SaveCICDRepository(context.Context, cicd.Repository) error
}

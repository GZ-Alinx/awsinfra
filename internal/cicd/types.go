package cicd

import (
	"context"
	"errors"
	"time"
)

var (
	ErrInvalid             = errors.New("CI/CD 配置不合法")
	ErrNotFound            = errors.New("CI/CD resource was not found")
	ErrConflict            = errors.New("CI/CD resource is in use")
	ErrJenkins             = errors.New("Jenkins request failed")
	ErrCredentialSecret    = errors.New("credential secret is required")
	ErrWebhookUnauthorized = errors.New("webhook authentication failed")
)

type Connection struct {
	Key             string    `json:"key"`
	ProjectKey      string    `json:"project_key"`
	DisplayName     string    `json:"display_name"`
	BaseURL         string    `json:"base_url"`
	Username        string    `json:"username"`
	ConnectionMode  string    `json:"connection_mode"`
	EnvironmentKey  string    `json:"environment_key,omitempty"`
	TargetName      string    `json:"target_name,omitempty"`
	Region          string    `json:"region,omitempty"`
	ClusterName     string    `json:"cluster_name,omitempty"`
	Namespace       string    `json:"namespace,omitempty"`
	ServiceName     string    `json:"service_name,omitempty"`
	ServicePort     int       `json:"service_port,omitempty"`
	Configured      bool      `json:"configured"`
	JenkinsVersion  string    `json:"jenkins_version,omitempty"`
	LastCheckStatus string    `json:"last_check_status,omitempty"`
	LastCheckError  string    `json:"last_check_error,omitempty"`
	LastCheckedAt   time.Time `json:"last_checked_at,omitempty"`
	CreatedAt       time.Time `json:"created_at"`
	UpdatedAt       time.Time `json:"updated_at"`
}

type StoredConnection struct {
	Connection
	TokenCipher string
}

type ConnectionInput struct {
	Key               string `json:"key"`
	EnvironmentKey    string `json:"environment_key"`
	DisplayName       string `json:"display_name"`
	BaseURL           string `json:"base_url"`
	Username          string `json:"username"`
	APIToken          string `json:"api_token"`
	AllowInsecureHTTP bool   `json:"allow_insecure_http"`
}

type ManagedConnectionInput struct {
	Key            string
	ProjectKey     string
	EnvironmentKey string
	TargetName     string
	DisplayName    string
	Region         string
	ClusterName    string
	Namespace      string
	ServiceName    string
	ServicePort    int
	Username       string
	Password       string
}

type ManagedEndpoint struct {
	ProjectKey     string
	EnvironmentKey string
	TargetName     string
	Region         string
	ClusterName    string
	Namespace      string
	ServiceName    string
	ServicePort    int
}

type TunnelProvider interface {
	Ensure(context.Context, ManagedEndpoint) (string, error)
}

type Credential struct {
	Key            string    `json:"key"`
	ProjectKey     string    `json:"project_key"`
	EnvironmentKey string    `json:"environment_key"`
	ConnectionKey  string    `json:"connection_key"`
	DisplayName    string    `json:"display_name"`
	Kind           string    `json:"kind"`
	ExternalID     string    `json:"external_id"`
	Description    string    `json:"description,omitempty"`
	Configured     bool      `json:"configured"`
	SyncStatus     string    `json:"sync_status,omitempty"`
	SyncError      string    `json:"sync_error,omitempty"`
	LastSyncedAt   time.Time `json:"last_synced_at,omitempty"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
}

type StoredCredential struct {
	Credential
	SecretCipher string
}

type CredentialInput struct {
	Key            string `json:"key"`
	EnvironmentKey string `json:"environment_key,omitempty"`
	ConnectionKey  string `json:"connection_key"`
	DisplayName    string `json:"display_name"`
	Kind           string `json:"kind"`
	ExternalID     string `json:"external_id"`
	Description    string `json:"description"`
	Username       string `json:"username,omitempty"`
	Password       string `json:"password,omitempty"`
	SecretText     string `json:"secret_text,omitempty"`
	PrivateKey     string `json:"private_key,omitempty"`
	Passphrase     string `json:"passphrase,omitempty"`
}

type CredentialSecret struct {
	Username   string `json:"username,omitempty"`
	Password   string `json:"password,omitempty"`
	SecretText string `json:"secret_text,omitempty"`
	PrivateKey string `json:"private_key,omitempty"`
	Passphrase string `json:"passphrase,omitempty"`
}

type CredentialInspection struct {
	ExternalID        string `json:"external_id"`
	Username          string `json:"username,omitempty"`
	PasswordPresent   bool   `json:"password_present"`
	PasswordEncrypted bool   `json:"password_encrypted"`
}

type GitCredentialValidation struct {
	CredentialKey string `json:"credential_key"`
	ExternalID    string `json:"external_id"`
	RepositoryURL string `json:"repository_url"`
	HTTPStatus    int    `json:"http_status"`
	ContentType   string `json:"content_type,omitempty"`
	SmartHTTP     bool   `json:"smart_http"`
}

type Repository struct {
	Key           string    `json:"key"`
	ProjectKey    string    `json:"project_key"`
	DisplayName   string    `json:"display_name"`
	Provider      string    `json:"provider"`
	Purpose       string    `json:"purpose"`
	CloneURL      string    `json:"clone_url"`
	DefaultBranch string    `json:"default_branch"`
	DefaultPath   string    `json:"default_path,omitempty"`
	Description   string    `json:"description,omitempty"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
}

type Job struct {
	Key                   string                `json:"key"`
	ProjectKey            string                `json:"project_key"`
	EnvironmentKey        string                `json:"environment_key"`
	DisplayName           string                `json:"display_name"`
	ServiceName           string                `json:"service_name"`
	ServiceKeys           []string              `json:"service_keys,omitempty"`
	Language              string                `json:"language"`
	JenkinsfileMode       string                `json:"jenkinsfile_mode,omitempty"`
	ExecutionMode         string                `json:"execution_mode,omitempty"`
	FailurePolicy         string                `json:"failure_policy,omitempty"`
	CompactParameters     bool                  `json:"compact_parameters,omitempty"`
	ConnectionKey         string                `json:"connection_key"`
	JenkinsJobName        string                `json:"jenkins_job_name"`
	Enabled               bool                  `json:"enabled"`
	TriggerMode           string                `json:"trigger_mode,omitempty"`
	TriggerBranch         string                `json:"trigger_branch,omitempty"`
	WebhookConfigured     bool                  `json:"webhook_configured"`
	WebhookSecretHash     string                `json:"-"`
	JenkinsfileRepository string                `json:"jenkinsfile_repository,omitempty"`
	JenkinsfileRepo       string                `json:"jenkinsfile_repo"`
	JenkinsfileBranch     string                `json:"jenkinsfile_branch"`
	JenkinsfilePath       string                `json:"jenkinsfile_path"`
	JenkinsfileContent    string                `json:"jenkinsfile_content,omitempty"`
	JenkinsfileCredential string                `json:"jenkinsfile_credential,omitempty"`
	SourceRepository      string                `json:"source_repository,omitempty"`
	SourceRepo            string                `json:"source_repo,omitempty"`
	ManifestRepository    string                `json:"manifest_repository,omitempty"`
	ManifestRepo          string                `json:"manifest_repo"`
	ManifestBranch        string                `json:"manifest_branch"`
	ManifestPath          string                `json:"manifest_path"`
	ManifestCredential    string                `json:"manifest_credential,omitempty"`
	EnvironmentPaths      map[string]string     `json:"environment_paths,omitempty"`
	BuildCommand          string                `json:"build_command,omitempty"`
	RuntimeVersion        string                `json:"runtime_version,omitempty"`
	Parameters            map[string]string     `json:"parameters,omitempty"`
	ParameterDefinitions  []ParameterDefinition `json:"parameter_definitions,omitempty"`
	SyncStatus            string                `json:"sync_status,omitempty"`
	SyncError             string                `json:"sync_error,omitempty"`
	LastSyncedAt          time.Time             `json:"last_synced_at,omitempty"`
	CreatedAt             time.Time             `json:"created_at"`
	UpdatedAt             time.Time             `json:"updated_at"`
}

// JobInput is the writable HTTP contract for a Jenkins Job. Keeping it
// separate from Job prevents clients from submitting server-owned project,
// synchronization and timestamp fields when an editing form is saved.
type JobInput struct {
	Key                   string                `json:"key"`
	EnvironmentKey        string                `json:"environment_key"`
	DisplayName           string                `json:"display_name"`
	ServiceName           string                `json:"service_name"`
	ServiceKeys           []string              `json:"service_keys,omitempty"`
	Language              string                `json:"language"`
	JenkinsfileMode       string                `json:"jenkinsfile_mode,omitempty"`
	ExecutionMode         string                `json:"execution_mode,omitempty"`
	FailurePolicy         string                `json:"failure_policy,omitempty"`
	CompactParameters     bool                  `json:"compact_parameters,omitempty"`
	ConnectionKey         string                `json:"connection_key"`
	JenkinsJobName        string                `json:"jenkins_job_name"`
	Enabled               bool                  `json:"enabled"`
	TriggerMode           string                `json:"trigger_mode,omitempty"`
	TriggerBranch         string                `json:"trigger_branch,omitempty"`
	JenkinsfileRepository string                `json:"jenkinsfile_repository,omitempty"`
	JenkinsfileRepo       string                `json:"jenkinsfile_repo"`
	JenkinsfileBranch     string                `json:"jenkinsfile_branch"`
	JenkinsfilePath       string                `json:"jenkinsfile_path"`
	JenkinsfileContent    string                `json:"jenkinsfile_content,omitempty"`
	JenkinsfileCredential string                `json:"jenkinsfile_credential,omitempty"`
	SourceRepository      string                `json:"source_repository,omitempty"`
	SourceRepo            string                `json:"source_repo,omitempty"`
	ManifestRepository    string                `json:"manifest_repository,omitempty"`
	ManifestRepo          string                `json:"manifest_repo"`
	ManifestBranch        string                `json:"manifest_branch"`
	ManifestPath          string                `json:"manifest_path"`
	ManifestCredential    string                `json:"manifest_credential,omitempty"`
	EnvironmentPaths      map[string]string     `json:"environment_paths,omitempty"`
	BuildCommand          string                `json:"build_command,omitempty"`
	RuntimeVersion        string                `json:"runtime_version,omitempty"`
	Parameters            map[string]string     `json:"parameters,omitempty"`
	ParameterDefinitions  []ParameterDefinition `json:"parameter_definitions,omitempty"`
}

type JobBuildUsage struct {
	TotalBuilds      int `json:"total_builds"`
	ActiveBuilds     int `json:"active_builds"`
	HistoricalBuilds int `json:"historical_builds"`
}

type JobDeletionResult struct {
	JobKey                   string `json:"job_key"`
	JenkinsJobName           string `json:"jenkins_job_name"`
	RemoteDeletionRequested  bool   `json:"remote_deletion_requested"`
	RemoteDeleted            bool   `json:"remote_deleted"`
	RemoteAlreadyMissing     bool   `json:"remote_already_missing"`
	HistoricalBuildsRetained int    `json:"historical_builds_retained"`
}

func (input JobInput) Job() Job {
	return Job{
		Key: input.Key, EnvironmentKey: input.EnvironmentKey, DisplayName: input.DisplayName, ServiceName: input.ServiceName, ServiceKeys: input.ServiceKeys,
		Language: input.Language, JenkinsfileMode: input.JenkinsfileMode, ExecutionMode: input.ExecutionMode, FailurePolicy: input.FailurePolicy, CompactParameters: input.CompactParameters,
		ConnectionKey: input.ConnectionKey, JenkinsJobName: input.JenkinsJobName, Enabled: input.Enabled,
		TriggerMode: input.TriggerMode, TriggerBranch: input.TriggerBranch,
		JenkinsfileRepository: input.JenkinsfileRepository, JenkinsfileRepo: input.JenkinsfileRepo, JenkinsfileBranch: input.JenkinsfileBranch,
		JenkinsfilePath: input.JenkinsfilePath, JenkinsfileContent: input.JenkinsfileContent, JenkinsfileCredential: input.JenkinsfileCredential,
		SourceRepository: input.SourceRepository, SourceRepo: input.SourceRepo,
		ManifestRepository: input.ManifestRepository, ManifestRepo: input.ManifestRepo, ManifestBranch: input.ManifestBranch,
		ManifestPath: input.ManifestPath, ManifestCredential: input.ManifestCredential,
		EnvironmentPaths: input.EnvironmentPaths, BuildCommand: input.BuildCommand, RuntimeVersion: input.RuntimeVersion,
		Parameters: input.Parameters, ParameterDefinitions: input.ParameterDefinitions,
	}
}

type WebhookSecret struct {
	SecretToken string `json:"secret_token"`
}

// ParameterDefinition describes a non-secret Jenkins build parameter that the
// platform can render as a typed control and validate before triggering a job.
type ParameterDefinition struct {
	Name         string   `json:"name"`
	Type         string   `json:"type"`
	DefaultValue string   `json:"default_value,omitempty"`
	Choices      []string `json:"choices,omitempty"`
	Description  string   `json:"description,omitempty"`
	Required     bool     `json:"required,omitempty"`
}

// JenkinsfileAnalysis is a redacted, read-only description of a declarative
// Jenkinsfile. The source text and detected secret values are never persisted
// or returned by the API.
type JenkinsfileAnalysis struct {
	Language             string                           `json:"language,omitempty"`
	RuntimeVersion       string                           `json:"runtime_version,omitempty"`
	ServiceParameter     string                           `json:"service_parameter,omitempty"`
	Services             []string                         `json:"services,omitempty"`
	Parameters           []ParameterDefinition            `json:"parameters,omitempty"`
	Stages               []string                         `json:"stages,omitempty"`
	Repositories         []JenkinsfileRepositoryHint      `json:"repositories,omitempty"`
	CredentialReferences []JenkinsfileCredentialReference `json:"credential_references,omitempty"`
	Settings             map[string]string                `json:"settings,omitempty"`
	SensitiveVariables   []string                         `json:"sensitive_variables,omitempty"`
	Warnings             []string                         `json:"warnings,omitempty"`
	Suggestion           JenkinsfileJobSuggestion         `json:"suggestion"`
}

type JenkinsfileRepositoryHint struct {
	Role   string `json:"role"`
	URL    string `json:"url"`
	Branch string `json:"branch,omitempty"`
	Path   string `json:"path,omitempty"`
}

type JenkinsfileCredentialReference struct {
	Variable      string `json:"variable"`
	ExternalID    string `json:"external_id,omitempty"`
	SuggestedKind string `json:"suggested_kind"`
	Usage         string `json:"usage"`
	Hardcoded     bool   `json:"hardcoded"`
}

type JenkinsfileJobSuggestion struct {
	DisplayName    string `json:"display_name,omitempty"`
	ServiceName    string `json:"service_name,omitempty"`
	Language       string `json:"language,omitempty"`
	RuntimeVersion string `json:"runtime_version,omitempty"`
	ManifestRepo   string `json:"manifest_repo,omitempty"`
	ManifestBranch string `json:"manifest_branch,omitempty"`
	ManifestPath   string `json:"manifest_path,omitempty"`
}

type Build struct {
	ID           string            `json:"id"`
	ProjectKey   string            `json:"project_key"`
	JobKey       string            `json:"job_key"`
	Environment  string            `json:"environment"`
	RequestedBy  string            `json:"requested_by"`
	Status       string            `json:"status"`
	Result       string            `json:"result,omitempty"`
	QueueURL     string            `json:"queue_url,omitempty"`
	BuildNumber  int64             `json:"build_number,omitempty"`
	BuildURL     string            `json:"build_url,omitempty"`
	Parameters   map[string]string `json:"parameters,omitempty"`
	Services     []string          `json:"services,omitempty"`
	Progress     int               `json:"progress"`
	CurrentStage string            `json:"current_stage,omitempty"`
	Stages       []BuildStage      `json:"stages,omitempty"`
	Error        string            `json:"error,omitempty"`
	CreatedAt    time.Time         `json:"created_at"`
	StartedAt    time.Time         `json:"started_at,omitempty"`
	FinishedAt   time.Time         `json:"finished_at,omitempty"`
	UpdatedAt    time.Time         `json:"updated_at"`
}

type BuildInput struct {
	Environment string            `json:"environment"`
	Branch      string            `json:"branch,omitempty"`
	ImageTag    string            `json:"image_tag,omitempty"`
	Services    []string          `json:"services,omitempty"`
	Parameters  map[string]string `json:"parameters,omitempty"`
}

const WebhookDeliveryParameter = "_OPS_WEBHOOK_DELIVERY_ID"

// BuildStage is a normalized Jenkins Pipeline stage. Service is populated for
// generated multi-service pipelines whose stage name starts with "service /".
type BuildStage struct {
	Name           string `json:"name"`
	Service        string `json:"service,omitempty"`
	Kind           string `json:"kind,omitempty"`
	Status         string `json:"status"`
	DurationMillis int64  `json:"duration_millis,omitempty"`
}

type LogChunk struct {
	Text       string `json:"text"`
	NextOffset int64  `json:"next_offset"`
	More       bool   `json:"more"`
}

type DeploymentLog struct {
	Text string `json:"text"`
}

type Store interface {
	ListCICDConnections(context.Context, string) ([]StoredConnection, error)
	GetCICDConnection(context.Context, string, string) (StoredConnection, error)
	SaveCICDConnection(context.Context, StoredConnection) error
	DeleteCICDConnection(context.Context, string, string) error

	ListCICDCredentials(context.Context, string) ([]StoredCredential, error)
	GetCICDCredential(context.Context, string, string) (StoredCredential, error)
	SaveCICDCredential(context.Context, StoredCredential) error
	DeleteCICDCredential(context.Context, string, string) error

	ListCICDRepositories(context.Context, string) ([]Repository, error)
	GetCICDRepository(context.Context, string, string) (Repository, error)
	SaveCICDRepository(context.Context, Repository) error
	DeleteCICDRepository(context.Context, string, string) error

	ListCICDJobs(context.Context, string) ([]Job, error)
	GetCICDJob(context.Context, string, string) (Job, error)
	SaveCICDJob(context.Context, Job) error
	DeleteCICDJob(context.Context, string, string) error
	GetCICDJobBuildUsage(context.Context, string, string) (JobBuildUsage, error)

	ListCICDBuilds(context.Context, string, string, int) ([]Build, error)
	GetCICDBuild(context.Context, string, string) (Build, error)
	SaveCICDBuild(context.Context, Build) error
	HasActiveCICDBuilds(context.Context, string) (bool, error)
	DeleteCICDProject(context.Context, string) error
}

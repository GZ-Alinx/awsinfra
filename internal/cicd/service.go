package cicd

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"ops-deploy-platform/internal/appconfig"
)

var (
	keyPattern           = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)
	jenkinsNamePattern   = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,127}$`)
	credentialIDPattern  = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,127}$`)
	parameterNamePattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,127}$`)
	jenkinsSecretPattern = regexp.MustCompile(`(?i)(glpat-[a-z0-9_.-]{16,}|AKIA[0-9A-Z]{16}|-----BEGIN [A-Z ]*PRIVATE KEY-----)`)
)

const maxSecretBytes = 1 << 20

type Service struct {
	store                Store
	aead                 cipher.AEAD
	newClient            func(StoredConnection, string) (*jenkinsClient, error)
	tunnels              TunnelProvider
	buildSourcePreflight func(context.Context, string, Job, []string) error
}

func (s *Service) SetTunnelProvider(provider TunnelProvider) { s.tunnels = provider }

func (s *Service) SetBuildSourcePreflight(preflight func(context.Context, string, Job, []string) error) {
	s.buildSourcePreflight = preflight
}

func (s *Service) PrepareManagedEndpoint(ctx context.Context, endpoint ManagedEndpoint) (string, error) {
	if s.tunnels == nil {
		return "", errors.New("EKS Jenkins tunnel service is unavailable")
	}
	return s.tunnels.Ensure(ctx, endpoint)
}

func New(config *appconfig.Config, store Store) (*Service, error) {
	encoded := strings.TrimSpace(config.CredentialKey())
	if encoded == "" {
		return nil, fmt.Errorf("%s is not set", config.Security.CredentialKeyEnv)
	}
	key, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil || len(key) != 32 {
		return nil, fmt.Errorf("%s must contain a base64-encoded 32-byte key", config.Security.CredentialKeyEnv)
	}
	block, err := aes.NewCipher(key)
	clear(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	s := &Service{store: store, aead: aead}
	s.newClient = func(c StoredConnection, token string) (*jenkinsClient, error) {
		return newJenkinsClient(c.BaseURL, c.Username, token)
	}
	return s, nil
}

func (s *Service) ListConnections(ctx context.Context, project string) ([]Connection, error) {
	records, err := s.store.ListCICDConnections(ctx, project)
	if err != nil {
		return nil, err
	}
	result := make([]Connection, 0, len(records))
	for _, record := range records {
		record.Connection.Configured = record.TokenCipher != ""
		result = append(result, record.Connection)
	}
	return result, nil
}

// ListConnectionsForEnvironment returns only Jenkins connections explicitly
// bound to the requested environment. Legacy project-wide connections are
// deliberately excluded so they cannot leak from test into production.
func (s *Service) ListConnectionsForEnvironment(ctx context.Context, project, environment string) ([]Connection, error) {
	environment = strings.ToLower(strings.TrimSpace(environment))
	if !validDeploymentEnvironment(environment) {
		return nil, fmt.Errorf("%w：请先选择 dev、test、uat 或 prod 环境", ErrInvalid)
	}
	items, err := s.ListConnections(ctx, project)
	if err != nil {
		return nil, err
	}
	result := make([]Connection, 0, len(items))
	for _, item := range items {
		if strings.EqualFold(strings.TrimSpace(item.EnvironmentKey), environment) {
			result = append(result, item)
		}
	}
	return result, nil
}

func (s *Service) GetConnectionForEnvironment(ctx context.Context, project, environment, key string) (Connection, error) {
	item, err := s.GetConnection(ctx, project, key)
	if err != nil {
		return Connection{}, err
	}
	if err := requireEnvironmentBinding("Jenkins 连接", item.EnvironmentKey, environment); err != nil {
		return Connection{}, err
	}
	return item, nil
}

func (s *Service) GetConnection(ctx context.Context, project, key string) (Connection, error) {
	record, err := s.store.GetCICDConnection(ctx, strings.TrimSpace(project), strings.ToLower(strings.TrimSpace(key)))
	if err != nil {
		return Connection{}, err
	}
	record.Configured = record.TokenCipher != ""
	return record.Connection, nil
}

func (s *Service) SaveConnection(ctx context.Context, project, key string, input ConnectionInput) (Connection, error) {
	project, key = strings.TrimSpace(project), strings.ToLower(strings.TrimSpace(key))
	input.EnvironmentKey = strings.ToLower(strings.TrimSpace(input.EnvironmentKey))
	if key == "" {
		key = strings.ToLower(strings.TrimSpace(input.Key))
	}
	if project == "" {
		return Connection{}, fmt.Errorf("%w：尚未选择项目", ErrInvalid)
	}
	if !validDeploymentEnvironment(input.EnvironmentKey) {
		return Connection{}, fmt.Errorf("%w：Jenkins 连接必须固定绑定 dev、test、uat 或 prod 环境", ErrInvalid)
	}
	if !keyPattern.MatchString(key) {
		return Connection{}, fmt.Errorf("%w：Jenkins 连接标识只能使用小写字母、数字和连字符，最长 63 位", ErrInvalid)
	}
	if strings.TrimSpace(input.DisplayName) == "" {
		return Connection{}, fmt.Errorf("%w：Jenkins 显示名称不能为空", ErrInvalid)
	}
	baseURL, err := validateJenkinsURL(input.BaseURL, input.AllowInsecureHTTP)
	if err != nil {
		return Connection{}, fmt.Errorf("%w：%v", ErrInvalid, err)
	}
	if strings.TrimSpace(input.Username) == "" {
		return Connection{}, fmt.Errorf("%w：Jenkins 用户名不能为空", ErrInvalid)
	}
	record := StoredConnection{Connection: Connection{Key: key, ProjectKey: project, EnvironmentKey: input.EnvironmentKey, DisplayName: limit(input.DisplayName, 128), BaseURL: baseURL, Username: limit(input.Username, 128), ConnectionMode: "direct"}}
	if existing, getErr := s.store.GetCICDConnection(ctx, project, key); getErr == nil {
		if existing.EnvironmentKey != "" && existing.EnvironmentKey != input.EnvironmentKey {
			return Connection{}, fmt.Errorf("%w：Jenkins 连接已绑定 %s 环境，不能改为 %s；请为新环境添加独立连接", ErrConflict, existing.EnvironmentKey, input.EnvironmentKey)
		}
		if existing.EnvironmentKey == "" {
			jobs, listErr := s.store.ListCICDJobs(ctx, project)
			if listErr != nil {
				return Connection{}, listErr
			}
			for _, job := range jobs {
				if job.ConnectionKey == key && job.EnvironmentKey != "" && job.EnvironmentKey != input.EnvironmentKey {
					return Connection{}, fmt.Errorf("%w：旧 Jenkins 连接同时被 %s 环境 Job 引用，不能直接绑定到 %s；请分别新增环境独立连接", ErrConflict, job.EnvironmentKey, input.EnvironmentKey)
				}
			}
		}
		record.TokenCipher = existing.TokenCipher
		record.CreatedAt = existing.CreatedAt
		record.JenkinsVersion, record.LastCheckStatus, record.LastCheckError, record.LastCheckedAt = existing.JenkinsVersion, existing.LastCheckStatus, existing.LastCheckError, existing.LastCheckedAt
	} else if !errors.Is(getErr, os.ErrNotExist) {
		return Connection{}, getErr
	}
	if token := strings.TrimSpace(input.APIToken); token != "" {
		record.TokenCipher, err = s.encrypt(connectionAAD(project, key), []byte(token))
		input.APIToken = ""
		if err != nil {
			return Connection{}, err
		}
	}
	if record.TokenCipher == "" {
		return Connection{}, ErrCredentialSecret
	}
	if err := s.store.SaveCICDConnection(ctx, record); err != nil {
		return Connection{}, err
	}
	stored, err := s.store.GetCICDConnection(ctx, project, key)
	if err != nil {
		return Connection{}, err
	}
	stored.Connection.Configured = true
	return stored.Connection, nil
}

func (s *Service) SaveManagedConnection(ctx context.Context, input ManagedConnectionInput) (Connection, error) {
	input.Key = strings.ToLower(strings.TrimSpace(input.Key))
	input.ProjectKey = strings.TrimSpace(input.ProjectKey)
	input.EnvironmentKey = strings.ToLower(strings.TrimSpace(input.EnvironmentKey))
	input.TargetName = strings.TrimSpace(input.TargetName)
	input.Region = strings.TrimSpace(input.Region)
	input.ClusterName = strings.TrimSpace(input.ClusterName)
	input.Namespace = strings.ToLower(strings.TrimSpace(input.Namespace))
	input.ServiceName = strings.ToLower(strings.TrimSpace(input.ServiceName))
	input.Username = strings.TrimSpace(input.Username)
	if input.ProjectKey == "" || !keyPattern.MatchString(input.Key) || input.EnvironmentKey == "" || input.TargetName == "" || input.Region == "" || input.ClusterName == "" ||
		!keyPattern.MatchString(input.Namespace) || !keyPattern.MatchString(input.ServiceName) || input.ServicePort < 1 || input.ServicePort > 65535 || input.Username == "" || input.Password == "" {
		input.Password = ""
		return Connection{}, fmt.Errorf("%w: managed Jenkins connection fields are incomplete", ErrInvalid)
	}
	host := input.ServiceName + "." + input.Namespace + ".svc.cluster.local"
	record := StoredConnection{Connection: Connection{
		Key: input.Key, ProjectKey: input.ProjectKey, DisplayName: limit(input.DisplayName, 128),
		BaseURL: "http://" + host + ":" + fmt.Sprint(input.ServicePort), Username: limit(input.Username, 128),
		ConnectionMode: "eks_port_forward", EnvironmentKey: input.EnvironmentKey, TargetName: input.TargetName,
		Region: input.Region, ClusterName: input.ClusterName, Namespace: input.Namespace,
		ServiceName: input.ServiceName, ServicePort: input.ServicePort,
	}}
	if record.DisplayName == "" {
		record.DisplayName = input.EnvironmentKey + " 环境 Jenkins"
	}
	if existing, err := s.store.GetCICDConnection(ctx, input.ProjectKey, input.Key); err == nil {
		if existing.EnvironmentKey != "" && existing.EnvironmentKey != input.EnvironmentKey {
			input.Password = ""
			return Connection{}, fmt.Errorf("%w: 当前 Jenkins 连接已绑定 %s 环境，不能覆盖为 %s", ErrConflict, existing.EnvironmentKey, input.EnvironmentKey)
		}
		record.CreatedAt = existing.CreatedAt
	} else if !errors.Is(err, os.ErrNotExist) {
		input.Password = ""
		return Connection{}, err
	}
	var err error
	record.TokenCipher, err = s.encrypt(connectionAAD(input.ProjectKey, input.Key), []byte(input.Password))
	input.Password = ""
	if err != nil {
		return Connection{}, err
	}
	if err := s.store.SaveCICDConnection(ctx, record); err != nil {
		return Connection{}, err
	}
	stored, err := s.store.GetCICDConnection(ctx, input.ProjectKey, input.Key)
	if err != nil {
		return Connection{}, err
	}
	stored.Configured = true
	return stored.Connection, nil
}

func (s *Service) DeleteConnection(ctx context.Context, project, key string) error {
	return s.store.DeleteCICDConnection(ctx, strings.TrimSpace(project), strings.ToLower(strings.TrimSpace(key)))
}

func (s *Service) TestConnection(ctx context.Context, project, key string) (Connection, error) {
	record, client, err := s.client(ctx, project, key)
	if err != nil {
		return Connection{}, err
	}
	version, checkErr := client.test(ctx)
	record.LastCheckedAt = time.Now().UTC()
	if checkErr != nil {
		record.LastCheckStatus, record.LastCheckError = "failed", friendlyJenkinsError(checkErr)
	} else {
		record.LastCheckStatus, record.LastCheckError, record.JenkinsVersion = "healthy", "", version
	}
	if saveErr := s.store.SaveCICDConnection(ctx, record); saveErr != nil {
		return Connection{}, saveErr
	}
	record.Configured = true
	if checkErr != nil {
		return record.Connection, checkErr
	}
	return record.Connection, nil
}

func (s *Service) ListCredentials(ctx context.Context, project string) ([]Credential, error) {
	records, err := s.store.ListCICDCredentials(ctx, project)
	if err != nil {
		return nil, err
	}
	result := make([]Credential, 0, len(records))
	for _, record := range records {
		record.Credential.Configured = record.Kind == "existing" || record.SecretCipher != ""
		result = append(result, record.Credential)
	}
	return result, nil
}

func (s *Service) ListCredentialsForEnvironment(ctx context.Context, project, environment string) ([]Credential, error) {
	environment = strings.ToLower(strings.TrimSpace(environment))
	if !validDeploymentEnvironment(environment) {
		return nil, fmt.Errorf("%w：请先选择 dev、test、uat 或 prod 环境", ErrInvalid)
	}
	items, err := s.ListCredentials(ctx, project)
	if err != nil {
		return nil, err
	}
	result := make([]Credential, 0, len(items))
	for _, item := range items {
		if strings.EqualFold(strings.TrimSpace(item.EnvironmentKey), environment) {
			result = append(result, item)
		}
	}
	return result, nil
}

func (s *Service) GetCredentialForEnvironment(ctx context.Context, project, environment, key string) (Credential, error) {
	record, err := s.store.GetCICDCredential(ctx, strings.TrimSpace(project), strings.ToLower(strings.TrimSpace(key)))
	if err != nil {
		return Credential{}, err
	}
	if err := requireEnvironmentBinding("Jenkins 凭据", record.EnvironmentKey, environment); err != nil {
		return Credential{}, err
	}
	record.Configured = record.Kind == "existing" || record.SecretCipher != ""
	return record.Credential, nil
}

func (s *Service) SaveCredential(ctx context.Context, project, key string, input CredentialInput) (Credential, error) {
	project, key = strings.TrimSpace(project), strings.ToLower(strings.TrimSpace(key))
	if key == "" {
		key = strings.ToLower(strings.TrimSpace(input.Key))
	}
	kind := strings.ToLower(strings.TrimSpace(input.Kind))
	input.EnvironmentKey = strings.ToLower(strings.TrimSpace(input.EnvironmentKey))
	input.ConnectionKey = strings.ToLower(strings.TrimSpace(input.ConnectionKey))
	connection, err := s.store.GetCICDConnection(ctx, project, input.ConnectionKey)
	if err != nil {
		return Credential{}, fmt.Errorf("connection: %w", err)
	}
	if input.EnvironmentKey == "" {
		input.EnvironmentKey = strings.ToLower(strings.TrimSpace(connection.EnvironmentKey))
	}
	if err := requireEnvironmentBinding("目标 Jenkins", connection.EnvironmentKey, input.EnvironmentKey); err != nil {
		return Credential{}, err
	}
	input.ExternalID = strings.TrimSpace(input.ExternalID)
	if input.ExternalID == "" {
		externalKey := strings.TrimPrefix(key, input.EnvironmentKey+"-")
		input.ExternalID = limit("ops-"+project+"-"+input.EnvironmentKey+"-"+externalKey, 128)
	}
	if project == "" || !keyPattern.MatchString(key) || !keyPattern.MatchString(input.ConnectionKey) || strings.TrimSpace(input.DisplayName) == "" || !credentialIDPattern.MatchString(input.ExternalID) || !validCredentialKind(kind) {
		return Credential{}, fmt.Errorf("%w: credential fields are incomplete", ErrInvalid)
	}
	record := StoredCredential{Credential: Credential{Key: key, ProjectKey: project, EnvironmentKey: input.EnvironmentKey, ConnectionKey: input.ConnectionKey, DisplayName: limit(input.DisplayName, 128), Kind: kind, ExternalID: limit(input.ExternalID, 128), Description: limit(input.Description, 500)}}
	if existing, err := s.store.GetCICDCredential(ctx, project, key); err == nil {
		if existing.EnvironmentKey != "" && existing.EnvironmentKey != record.EnvironmentKey {
			return Credential{}, fmt.Errorf("%w: 凭据已绑定 %s 环境，不能给 %s 环境复用；请创建新的环境凭据", ErrConflict, existing.EnvironmentKey, record.EnvironmentKey)
		}
		if existing.ConnectionKey != record.ConnectionKey || existing.ExternalID != record.ExternalID || existing.Kind != record.Kind {
			return Credential{}, fmt.Errorf("%w: 已创建凭据不能修改类型、目标 Jenkins 或 Credential ID，请新建凭据后再替换 Job 引用", ErrConflict)
		}
		record.SecretCipher, record.CreatedAt = existing.SecretCipher, existing.CreatedAt
		record.SyncStatus, record.SyncError, record.LastSyncedAt = existing.SyncStatus, existing.SyncError, existing.LastSyncedAt
	} else if !errors.Is(err, os.ErrNotExist) {
		return Credential{}, err
	}
	secret := CredentialSecret{Username: input.Username, Password: input.Password, SecretText: input.SecretText, PrivateKey: input.PrivateKey, Passphrase: input.Passphrase}
	payload, supplied, err := validateCredentialSecret(kind, secret)
	input.Password, input.SecretText, input.PrivateKey, input.Passphrase = "", "", "", ""
	if err != nil {
		return Credential{}, err
	}
	if supplied {
		record.SecretCipher, err = s.encrypt(credentialAAD(project, key), payload)
		clear(payload)
		if err != nil {
			return Credential{}, err
		}
	}
	if kind != "existing" && record.SecretCipher == "" {
		return Credential{}, ErrCredentialSecret
	}
	if kind == "existing" {
		record.SecretCipher = ""
		record.SyncStatus, record.SyncError, record.LastSyncedAt = "ready", "", time.Now().UTC()
	}
	if err := s.store.SaveCICDCredential(ctx, record); err != nil {
		return Credential{}, err
	}
	stored, err := s.store.GetCICDCredential(ctx, project, key)
	if err != nil {
		return Credential{}, err
	}
	stored.Configured = stored.Kind == "existing" || stored.SecretCipher != ""
	return stored.Credential, nil
}

func (s *Service) DeleteCredential(ctx context.Context, project, key string) error {
	return s.store.DeleteCICDCredential(ctx, project, strings.ToLower(strings.TrimSpace(key)))
}

// AuthorizeGitRelay compares a relay request with the project-owned encrypted
// Git credential without returning either stored value. Relay access is denied
// for existing/external Jenkins credentials because the platform cannot prove
// their contents.
func (s *Service) AuthorizeGitRelay(ctx context.Context, project, credentialKey, username, password string) (bool, error) {
	record, err := s.store.GetCICDCredential(ctx, strings.TrimSpace(project), strings.ToLower(strings.TrimSpace(credentialKey)))
	if err != nil {
		return false, err
	}
	if record.Kind != "gitlab_token" && record.Kind != "username_password" {
		return false, nil
	}
	payload, err := s.decrypt(credentialAAD(record.ProjectKey, record.Key), record.SecretCipher)
	if err != nil {
		return false, err
	}
	var secret CredentialSecret
	if err := json.Unmarshal(payload, &secret); err != nil {
		clear(payload)
		return false, err
	}
	clear(payload)
	userMatch := subtle.ConstantTimeCompare([]byte(secret.Username), []byte(username))
	passwordMatch := subtle.ConstantTimeCompare([]byte(secret.Password), []byte(password))
	secret.Username, secret.Password = "", ""
	return userMatch == 1 && passwordMatch == 1, nil
}

func (s *Service) SyncCredential(ctx context.Context, project, key string) (Credential, error) {
	record, err := s.store.GetCICDCredential(ctx, project, key)
	if err != nil {
		return Credential{}, err
	}
	if record.Kind == "existing" {
		record.SyncStatus, record.SyncError, record.LastSyncedAt = "ready", "", time.Now().UTC()
	} else {
		_, client, clientErr := s.client(ctx, project, record.ConnectionKey)
		var secret CredentialSecret
		if clientErr == nil {
			payload, decryptErr := s.decrypt(credentialAAD(project, key), record.SecretCipher)
			if decryptErr != nil {
				clientErr = decryptErr
			} else {
				clientErr = json.Unmarshal(payload, &secret)
				clear(payload)
			}
		}
		if clientErr == nil {
			clientErr = client.upsertCredential(ctx, record, secret)
		}
		secret.Password, secret.SecretText, secret.PrivateKey, secret.Passphrase = "", "", "", ""
		record.LastSyncedAt = time.Now().UTC()
		if clientErr != nil {
			record.SyncStatus, record.SyncError = "failed", friendlyJenkinsError(clientErr)
		} else {
			record.SyncStatus, record.SyncError = "ready", ""
		}
	}
	if err := s.store.SaveCICDCredential(ctx, record); err != nil {
		return Credential{}, err
	}
	record.Configured = record.Kind == "existing" || record.SecretCipher != ""
	if record.SyncStatus == "failed" {
		return record.Credential, fmt.Errorf("%w: %s", ErrJenkins, record.SyncError)
	}
	return record.Credential, nil
}

func (s *Service) SyncCredentialForEnvironment(ctx context.Context, project, environment, key string) (Credential, error) {
	if _, err := s.GetCredentialForEnvironment(ctx, project, environment, key); err != nil {
		return Credential{}, err
	}
	return s.SyncCredential(ctx, project, key)
}

func (s *Service) InspectCredential(ctx context.Context, project, key string) (CredentialInspection, error) {
	record, err := s.store.GetCICDCredential(ctx, project, key)
	if err != nil {
		return CredentialInspection{}, err
	}
	_, client, err := s.client(ctx, project, record.ConnectionKey)
	if err != nil {
		return CredentialInspection{}, err
	}
	return client.inspectCredential(ctx, record.ExternalID)
}

func (s *Service) ListJobs(ctx context.Context, project string) ([]Job, error) {
	return s.store.ListCICDJobs(ctx, project)
}

func (s *Service) GetJob(ctx context.Context, project, key string) (Job, error) {
	return s.store.GetCICDJob(ctx, strings.TrimSpace(project), strings.ToLower(strings.TrimSpace(key)))
}

func (s *Service) ListRepositories(ctx context.Context, project string) ([]Repository, error) {
	return s.store.ListCICDRepositories(ctx, strings.TrimSpace(project))
}

func (s *Service) SaveRepository(ctx context.Context, project, key string, input Repository) (Repository, error) {
	project, key = strings.TrimSpace(project), strings.ToLower(strings.TrimSpace(key))
	if key == "" {
		key = strings.ToLower(strings.TrimSpace(input.Key))
	}
	input.Key, input.ProjectKey = key, project
	input.Provider = strings.ToLower(strings.TrimSpace(input.Provider))
	input.Purpose = strings.ToLower(strings.TrimSpace(input.Purpose))
	input.CloneURL = strings.TrimSpace(input.CloneURL)
	input.DefaultBranch = defaultValue(input.DefaultBranch, "main")
	parsed, err := url.Parse(input.CloneURL)
	if project == "" || !keyPattern.MatchString(key) || strings.TrimSpace(input.DisplayName) == "" || !validRepositoryProvider(input.Provider) || !validRepositoryPurpose(input.Purpose) || err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil {
		return Repository{}, fmt.Errorf("%w: 仓库标识、用途和 HTTPS Clone URL 不完整", ErrInvalid)
	}
	input.DisplayName, input.Description = limit(input.DisplayName, 128), limit(input.Description, 500)
	input.DefaultBranch, input.DefaultPath = limit(input.DefaultBranch, 255), limit(input.DefaultPath, 500)
	if existing, getErr := s.store.GetCICDRepository(ctx, project, key); getErr == nil {
		input.CreatedAt = existing.CreatedAt
	} else if !errors.Is(getErr, os.ErrNotExist) {
		return Repository{}, getErr
	}
	if err := s.store.SaveCICDRepository(ctx, input); err != nil {
		return Repository{}, err
	}
	return s.store.GetCICDRepository(ctx, project, key)
}

func (s *Service) DeleteRepository(ctx context.Context, project, key string) error {
	return s.store.DeleteCICDRepository(ctx, strings.TrimSpace(project), strings.ToLower(strings.TrimSpace(key)))
}

func (s *Service) SaveJob(ctx context.Context, project, key string, input Job) (Job, error) {
	project, key = strings.TrimSpace(project), strings.ToLower(strings.TrimSpace(key))
	if key == "" {
		key = strings.ToLower(strings.TrimSpace(input.Key))
	}
	input.Key, input.ProjectKey = key, project
	input.EnvironmentKey = strings.ToLower(strings.TrimSpace(input.EnvironmentKey))
	input.ConnectionKey = strings.ToLower(strings.TrimSpace(input.ConnectionKey))
	input.Language = strings.ToLower(strings.TrimSpace(input.Language))
	input.JenkinsfileMode = strings.ToLower(strings.TrimSpace(input.JenkinsfileMode))
	input.ExecutionMode = strings.ToLower(strings.TrimSpace(input.ExecutionMode))
	input.FailurePolicy = strings.ToLower(strings.TrimSpace(input.FailurePolicy))
	input.TriggerMode = strings.ToLower(strings.TrimSpace(input.TriggerMode))
	input.TriggerBranch = strings.TrimSpace(input.TriggerBranch)
	input.ServiceKeys = normalizeServiceKeys(input.ServiceKeys)
	if len(input.ServiceKeys) == 0 && strings.TrimSpace(input.ServiceName) != "" {
		input.ServiceKeys = normalizeServiceKeys([]string{input.ServiceName})
	}
	if input.JenkinsfileMode == "" {
		input.JenkinsfileMode = "existing"
	}
	if input.ExecutionMode == "" {
		input.ExecutionMode = "serial"
	}
	if input.FailurePolicy == "" {
		input.FailurePolicy = "stop"
	}
	if input.TriggerMode == "" {
		input.TriggerMode = "manual"
	}
	if input.TriggerMode == "gitlab_push" && input.TriggerBranch == "" {
		input.TriggerBranch = "main"
	}
	connection, err := s.store.GetCICDConnection(ctx, project, input.ConnectionKey)
	if err != nil {
		return Job{}, fmt.Errorf("connection: %w", err)
	}
	existing, existingErr := s.store.GetCICDJob(ctx, project, key)
	if existingErr != nil && !errors.Is(existingErr, os.ErrNotExist) {
		return Job{}, existingErr
	}
	if input.EnvironmentKey == "" && existingErr == nil {
		input.EnvironmentKey = strings.ToLower(strings.TrimSpace(existing.EnvironmentKey))
	}
	if input.EnvironmentKey == "" {
		input.EnvironmentKey = generatedJobEnvironment(connection.EnvironmentKey, input.Parameters["DEPLOY_ENV"])
	}
	if !validDeploymentEnvironment(input.EnvironmentKey) {
		return Job{}, fmt.Errorf("%w: Job 必须固定绑定 dev、test、uat 或 prod 环境", ErrInvalid)
	}
	if existingErr == nil && existing.EnvironmentKey != "" && existing.EnvironmentKey != input.EnvironmentKey {
		return Job{}, fmt.Errorf("%w: Job 已绑定 %s 环境，不能改为 %s；请为新环境创建独立 Job", ErrConflict, existing.EnvironmentKey, input.EnvironmentKey)
	}
	if err := requireEnvironmentBinding("目标 Jenkins", connection.EnvironmentKey, input.EnvironmentKey); err != nil {
		return Job{}, err
	}
	if input.JenkinsfileMode == "generated" {
		input.JenkinsfileRepository = defaultValue(input.JenkinsfileRepository, "ops-delivery-jenkinsfiles")
		input.ManifestRepository = defaultValue(input.ManifestRepository, "ops-delivery-manifests")
		input.JenkinsfilePath = defaultValue(input.JenkinsfilePath, generatedJenkinsfilePath(input.EnvironmentKey, key))
		input.ManifestPath = defaultValue(input.ManifestPath, "environments/"+input.EnvironmentKey)
		if input.ServiceName == "" {
			input.ServiceName = key
		}
		if len(input.ServiceKeys) > 1 {
			input.Language = "mixed"
		}
	}
	input.JenkinsJobName = strings.TrimSpace(input.JenkinsJobName)
	input.JenkinsfileRepository = strings.ToLower(strings.TrimSpace(input.JenkinsfileRepository))
	input.SourceRepository = strings.ToLower(strings.TrimSpace(input.SourceRepository))
	input.ManifestRepository = strings.ToLower(strings.TrimSpace(input.ManifestRepository))
	if err := s.resolveJobRepositories(ctx, project, &input); err != nil {
		return Job{}, err
	}
	if input.JenkinsfileMode == "generated" {
		allowed := map[string]bool{"ops-delivery": true, "ops-delivery-jenkinsfiles": true, "ops-delivery-manifests": true}
		if !allowed[input.JenkinsfileRepository] || !allowed[input.ManifestRepository] {
			return Job{}, fmt.Errorf("%w: 平台生成文件只能写入“项目接入”创建并验证过的运维交付仓库", ErrInvalid)
		}
		if input.JenkinsfileRepository == input.ManifestRepository && input.JenkinsfileRepository == "ops-delivery-jenkinsfiles" {
			manifestRepository, lookupErr := s.store.GetCICDRepository(ctx, project, "ops-delivery-manifests")
			if lookupErr == nil && canonicalRepositoryURL(manifestRepository.CloneURL) != canonicalRepositoryURL(input.JenkinsfileRepo) {
				return Job{}, fmt.Errorf("%w: 旧项目统一仓库模式应选择部署清单仓库，确保环境清单已经存在", ErrInvalid)
			}
		}
	}
	if input.JenkinsfileMode == "generated" && (input.JenkinsfileCredential == "" || input.ManifestCredential == "") {
		return Job{}, fmt.Errorf("%w: 平台生成流水线必须选择 Jenkinsfile 仓库凭据和部署清单仓库凭据", ErrInvalid)
	}
	if !validJob(input) {
		return Job{}, fmt.Errorf("%w: Job 的名称、Jenkins 连接、Jenkinsfile 仓库和部署清单仓库不完整", ErrInvalid)
	}
	manifestCredentialID := ""
	for _, credentialKey := range []string{input.JenkinsfileCredential, input.ManifestCredential} {
		if credentialKey == "" {
			continue
		}
		credential, err := s.store.GetCICDCredential(ctx, project, credentialKey)
		if err != nil || credential.ConnectionKey != input.ConnectionKey || credential.EnvironmentKey != input.EnvironmentKey {
			return Job{}, fmt.Errorf("%w: Job 引用了无效或属于其他 Jenkins 连接的凭据", ErrInvalid)
		}
		if credentialKey == input.ManifestCredential {
			manifestCredentialID = credential.ExternalID
		}
	}
	input.DisplayName, input.ServiceName = limit(input.DisplayName, 128), limit(input.ServiceName, 128)
	input.JenkinsfileBranch = defaultValue(input.JenkinsfileBranch, "main")
	input.JenkinsfilePath = defaultValue(input.JenkinsfilePath, "Jenkinsfile")
	input.JenkinsfileContent = strings.TrimSpace(input.JenkinsfileContent)
	if input.JenkinsfileContent != "" {
		if input.JenkinsfileMode != "generated" {
			return Job{}, fmt.Errorf("%w: 在线维护 Jenkinsfile 仅适用于平台生成模式", ErrInvalid)
		}
		if len(input.JenkinsfileContent) > 256*1024 || strings.IndexByte(input.JenkinsfileContent, 0) >= 0 || !regexp.MustCompile(`(?m)^\s*pipeline\s*\{`).MatchString(input.JenkinsfileContent) {
			return Job{}, fmt.Errorf("%w: Jenkinsfile 内容为空、过大或不是 Declarative Pipeline", ErrInvalid)
		}
		if jenkinsSecretPattern.MatchString(input.JenkinsfileContent) {
			return Job{}, fmt.Errorf("%w: Jenkinsfile 疑似包含 Token、Access Key 或私钥明文；请改用 Jenkins Credential ID", ErrInvalid)
		}
		input.JenkinsfileContent += "\n"
	}
	input.ManifestBranch = defaultValue(input.ManifestBranch, "main")
	if err := validateRepositoryPath(input.JenkinsfilePath, false); err != nil {
		return Job{}, fmt.Errorf("%w: Jenkinsfile 路径不合法：%v", ErrInvalid, err)
	}
	if err := validateRepositoryPath(input.ManifestPath, true); err != nil {
		return Job{}, fmt.Errorf("%w: 部署清单目录不合法：%v", ErrInvalid, err)
	}
	input.EnvironmentPaths = cleanMap(input.EnvironmentPaths, 32)
	if input.JenkinsfileMode == "generated" {
		input.EnvironmentPaths = map[string]string{input.EnvironmentKey: input.ManifestPath}
	}
	input.Parameters = cleanMap(input.Parameters, 64)
	parameterDefinitions, err := cleanParameterDefinitions(input.ParameterDefinitions)
	if err != nil {
		return Job{}, err
	}
	input.ParameterDefinitions = parameterDefinitions
	if input.JenkinsfileMode == "generated" {
		input.Parameters = generatedJobConfiguration(input.Parameters, input.EnvironmentKey, manifestCredentialID)
		// 平台生成的 Jenkinsfile 有固定的两个构建入口，不接收
		// 可变参数定义。所有其他值在生成时写入 Jenkinsfile。
		input.ParameterDefinitions = nil
	}
	if existingErr == nil {
		input.CreatedAt = existing.CreatedAt
		input.WebhookSecretHash = existing.WebhookSecretHash
		input.WebhookConfigured = existing.WebhookSecretHash != ""
	}
	if err := s.validateJobPathOwnership(ctx, input); err != nil {
		return Job{}, err
	}
	// A saved definition is not deployable until the corresponding GitLab
	// Jenkinsfile and Jenkins Job have both been synchronized again. Marking it
	// pending prevents a network interruption between the two HTTP requests from
	// leaving an edited Job falsely shown as ready.
	input.SyncStatus, input.SyncError, input.LastSyncedAt = "pending", "", time.Time{}
	if err := s.store.SaveCICDJob(ctx, input); err != nil {
		return Job{}, err
	}
	if input.JenkinsfileMode == "generated" {
		return s.PrepareGeneratedJobRepositories(ctx, project, key)
	}
	return s.store.GetCICDJob(ctx, project, key)
}

// RotateWebhookSecret issues a high-entropy GitLab webhook secret. Only the
// SHA-256 digest is persisted; the plaintext is returned once to the operator.
func (s *Service) RotateWebhookSecret(ctx context.Context, project, key string) (WebhookSecret, error) {
	job, err := s.store.GetCICDJob(ctx, strings.TrimSpace(project), strings.ToLower(strings.TrimSpace(key)))
	if err != nil {
		return WebhookSecret{}, err
	}
	if job.TriggerMode != "gitlab_push" {
		return WebhookSecret{}, fmt.Errorf("%w: 请先把 Job 触发方式设置为 GitLab Push", ErrConflict)
	}
	raw := make([]byte, 32)
	if _, err := io.ReadFull(rand.Reader, raw); err != nil {
		return WebhookSecret{}, err
	}
	token := base64.RawURLEncoding.EncodeToString(raw)
	clear(raw)
	job.WebhookSecretHash = webhookSecretHash(token)
	job.WebhookConfigured = true
	if err := s.store.SaveCICDJob(ctx, job); err != nil {
		return WebhookSecret{}, err
	}
	return WebhookSecret{SecretToken: token}, nil
}

// AuthenticateGitLabWebhook validates the shared secret without exposing
// whether a project or Job exists to an unauthenticated caller.
func (s *Service) AuthenticateGitLabWebhook(ctx context.Context, project, key, token string) (Job, error) {
	job, err := s.store.GetCICDJob(ctx, strings.TrimSpace(project), strings.ToLower(strings.TrimSpace(key)))
	if err != nil {
		_ = subtle.ConstantTimeCompare([]byte(webhookSecretHash(token)), []byte(webhookSecretHash("invalid")))
		return Job{}, ErrWebhookUnauthorized
	}
	expected, actual := strings.TrimSpace(job.WebhookSecretHash), webhookSecretHash(strings.TrimSpace(token))
	if expected == "" || token == "" || subtle.ConstantTimeCompare([]byte(expected), []byte(actual)) != 1 {
		return Job{}, ErrWebhookUnauthorized
	}
	if job.TriggerMode != "gitlab_push" || !job.Enabled {
		return Job{}, fmt.Errorf("%w: 当前 Job 未启用 GitLab Push 自动触发", ErrConflict)
	}
	return job, nil
}

func webhookSecretHash(token string) string {
	digest := sha256.Sum256([]byte(token))
	return base64.RawURLEncoding.EncodeToString(digest[:])
}

// PrepareGeneratedJobRepositories pins platform-generated Jobs to the exact
// project-owned GitLab repositories displayed in the UI. The read-only deploy
// credential is synchronized to Jenkins separately; repository URLs must not
// be silently replaced with relay aliases after the user has reviewed them.
func (s *Service) PrepareGeneratedJobRepositories(ctx context.Context, project, key string) (Job, error) {
	job, err := s.store.GetCICDJob(ctx, strings.TrimSpace(project), strings.ToLower(strings.TrimSpace(key)))
	if err != nil || job.JenkinsfileMode != "generated" {
		return job, err
	}
	pipelineKey := defaultValue(job.JenkinsfileRepository, "ops-delivery-jenkinsfiles")
	manifestKey := defaultValue(job.ManifestRepository, "ops-delivery-manifests")
	// Compatibility repair for legacy two-repository projects. Older UI builds
	// could incorrectly treat the manifests repository as a unified delivery
	// repository, which placed a production Jenkinsfile beside manifests while
	// test still used the dedicated Jenkinsfile repository.
	if pipelineKey == "ops-delivery-manifests" && manifestKey == "ops-delivery-manifests" {
		if _, unifiedErr := s.store.GetCICDRepository(ctx, project, "ops-delivery"); errors.Is(unifiedErr, os.ErrNotExist) {
			if _, pipelineLookupErr := s.store.GetCICDRepository(ctx, project, "ops-delivery-jenkinsfiles"); pipelineLookupErr == nil {
				pipelineKey = "ops-delivery-jenkinsfiles"
			}
		}
	}
	pipeline, pipelineErr := s.store.GetCICDRepository(ctx, project, pipelineKey)
	manifests, manifestErr := s.store.GetCICDRepository(ctx, project, manifestKey)
	jenkinsfileCredentialKey := strings.TrimSpace(job.JenkinsfileCredential)
	if jenkinsfileCredentialKey == "" {
		jenkinsfileCredentialKey = "gitlab-delivery-read"
	}
	manifestCredentialKey := strings.TrimSpace(job.ManifestCredential)
	if manifestCredentialKey == "" {
		manifestCredentialKey = jenkinsfileCredentialKey
	}
	jenkinsfileCredential, jenkinsfileCredentialErr := s.store.GetCICDCredential(ctx, project, jenkinsfileCredentialKey)
	manifestCredential, manifestCredentialErr := s.store.GetCICDCredential(ctx, project, manifestCredentialKey)
	// Old Jobs may still contain a removed or legacy credential key. Keep a
	// valid operator-selected credential, but fall back to the platform managed
	// delivery credential when the selected record no longer exists.
	if errors.Is(jenkinsfileCredentialErr, os.ErrNotExist) && jenkinsfileCredentialKey != "gitlab-delivery-read" {
		jenkinsfileCredentialKey = "gitlab-delivery-read"
		jenkinsfileCredential, jenkinsfileCredentialErr = s.store.GetCICDCredential(ctx, project, jenkinsfileCredentialKey)
	}
	if errors.Is(manifestCredentialErr, os.ErrNotExist) && manifestCredentialKey != "gitlab-delivery-read" {
		manifestCredentialKey = "gitlab-delivery-read"
		manifestCredential, manifestCredentialErr = s.store.GetCICDCredential(ctx, project, manifestCredentialKey)
	}
	for _, lookupErr := range []error{pipelineErr, manifestErr, jenkinsfileCredentialErr, manifestCredentialErr} {
		if lookupErr != nil && !errors.Is(lookupErr, os.ErrNotExist) {
			return Job{}, lookupErr
		}
	}
	credentialID := ""
	if manifestCredentialErr == nil && manifestCredential.ConnectionKey == job.ConnectionKey && manifestCredential.EnvironmentKey == job.EnvironmentKey {
		credentialID = manifestCredential.ExternalID
	}
	job.Parameters = generatedJobConfiguration(job.Parameters, generatedJobEnvironment(job.EnvironmentKey, job.Parameters["DEPLOY_ENV"]), credentialID)
	job.ParameterDefinitions = nil
	if pipelineErr != nil || manifestErr != nil || jenkinsfileCredentialErr != nil || manifestCredentialErr != nil ||
		jenkinsfileCredential.SyncStatus != "ready" || manifestCredential.SyncStatus != "ready" ||
		jenkinsfileCredential.ConnectionKey != job.ConnectionKey || manifestCredential.ConnectionKey != job.ConnectionKey ||
		jenkinsfileCredential.EnvironmentKey != job.EnvironmentKey || manifestCredential.EnvironmentKey != job.EnvironmentKey {
		if err := s.store.SaveCICDJob(ctx, job); err != nil {
			return Job{}, err
		}
		return job, nil
	}
	job.JenkinsfileRepository = pipeline.Key
	job.JenkinsfileRepo = pipeline.CloneURL
	job.JenkinsfileBranch = defaultValue(job.JenkinsfileBranch, defaultValue(pipeline.DefaultBranch, "main"))
	job.JenkinsfilePath = defaultValue(job.JenkinsfilePath, generatedJenkinsfilePath(job.EnvironmentKey, job.Key))
	job.JenkinsfileCredential = jenkinsfileCredential.Key
	job.ManifestRepository = manifests.Key
	job.ManifestRepo = manifests.CloneURL
	job.ManifestBranch = defaultValue(job.ManifestBranch, defaultValue(manifests.DefaultBranch, "main"))
	job.ManifestPath = defaultValue(job.ManifestPath, "environments/"+job.EnvironmentKey)
	job.EnvironmentPaths = map[string]string{job.EnvironmentKey: job.ManifestPath}
	job.ManifestCredential = manifestCredential.Key
	job.SyncStatus, job.SyncError, job.LastSyncedAt = "pending", "", time.Time{}
	if err := s.store.SaveCICDJob(ctx, job); err != nil {
		return Job{}, err
	}
	return s.store.GetCICDJob(ctx, project, key)
}

// SetManagedJobParameter updates a platform-owned, non-secret runtime
// reference without re-running the writable Job validation path. The value is
// a Jenkins Credential ID or channel identity, never the credential secret.
func (s *Service) SetManagedJobParameter(ctx context.Context, project, key, name, value string) (Job, error) {
	allowed := map[string]bool{"LARK_ALERT_CHANNEL": true, "LARK_ALERT_ENVIRONMENT": true, "LARK_CREDENTIALS_ID": true}
	if !allowed[name] {
		return Job{}, fmt.Errorf("%w: unsupported managed Job parameter", ErrInvalid)
	}
	job, err := s.store.GetCICDJob(ctx, strings.TrimSpace(project), strings.ToLower(strings.TrimSpace(key)))
	if err != nil {
		return Job{}, err
	}
	if job.Parameters == nil {
		job.Parameters = map[string]string{}
	}
	value = strings.TrimSpace(value)
	if value == "" {
		delete(job.Parameters, name)
	} else {
		job.Parameters[name] = limit(value, 500)
	}
	if err := s.store.SaveCICDJob(ctx, job); err != nil {
		return Job{}, err
	}
	return s.store.GetCICDJob(ctx, project, key)
}

func (s *Service) RecordJobSyncFailure(ctx context.Context, project, key string, cause error) (Job, error) {
	return s.RecordJobSyncStageFailure(ctx, project, key, "GitLab Jenkinsfile 同步", cause)
}

func (s *Service) RecordJobSyncStageFailure(ctx context.Context, project, key, stage string, cause error) (Job, error) {
	job, err := s.store.GetCICDJob(ctx, project, key)
	if err != nil {
		return Job{}, err
	}
	stage = strings.TrimSpace(stage)
	if stage == "" {
		stage = "CI/CD 同步"
	}
	message := stage + "失败"
	if cause != nil && strings.TrimSpace(cause.Error()) != "" {
		message += "：" + limit(cause.Error(), 800)
	}
	job.SyncStatus, job.SyncError, job.LastSyncedAt = "failed", message, time.Now().UTC()
	if err := s.store.SaveCICDJob(ctx, job); err != nil {
		return Job{}, err
	}
	return job, nil
}

func (s *Service) JobBuildUsage(ctx context.Context, project, key string) (JobBuildUsage, error) {
	project, key = strings.TrimSpace(project), strings.ToLower(strings.TrimSpace(key))
	if _, err := s.store.GetCICDJob(ctx, project, key); err != nil {
		return JobBuildUsage{}, err
	}
	return s.store.GetCICDJobBuildUsage(ctx, project, key)
}

func (s *Service) DeleteJob(ctx context.Context, project, key string, deleteRemote bool) (JobDeletionResult, error) {
	project, key = strings.TrimSpace(project), strings.ToLower(strings.TrimSpace(key))
	job, err := s.store.GetCICDJob(ctx, project, key)
	if err != nil {
		return JobDeletionResult{}, err
	}
	usage, err := s.store.GetCICDJobBuildUsage(ctx, project, key)
	if err != nil {
		return JobDeletionResult{}, err
	}
	if usage.ActiveBuilds > 0 {
		return JobDeletionResult{}, fmt.Errorf("%w：该 Job 仍有 %d 个排队中或运行中的构建，请先停止后再删除", ErrConflict, usage.ActiveBuilds)
	}
	result := JobDeletionResult{JobKey: job.Key, JenkinsJobName: job.JenkinsJobName, RemoteDeletionRequested: deleteRemote, HistoricalBuildsRetained: usage.HistoricalBuilds}
	if deleteRemote {
		_, client, clientErr := s.clientForJob(ctx, project, job, job.EnvironmentKey)
		if clientErr != nil {
			return JobDeletionResult{}, fmt.Errorf("%w: 无法连接 Jenkins，未删除平台 Job：%s", ErrJenkins, friendlyJenkinsError(clientErr))
		}
		result.RemoteDeleted, result.RemoteAlreadyMissing, clientErr = client.deleteJob(ctx, job.JenkinsJobName)
		if clientErr != nil {
			return JobDeletionResult{}, fmt.Errorf("%w: 删除 Jenkins Job 失败，平台记录已保留：%s", ErrJenkins, friendlyJenkinsError(clientErr))
		}
	}
	if err := s.store.DeleteCICDJob(ctx, project, key); err != nil {
		return JobDeletionResult{}, err
	}
	return result, nil
}

func (s *Service) SyncJob(ctx context.Context, project, key string) (Job, error) {
	job, err := s.PrepareGeneratedJobRepositories(ctx, project, key)
	if err != nil {
		return Job{}, err
	}
	_, client, bindingErr := s.clientForJob(ctx, project, job, job.EnvironmentKey)
	if bindingErr != nil {
		job.SyncStatus, job.SyncError, job.LastSyncedAt = "failed", friendlyJenkinsError(bindingErr), time.Now().UTC()
		_ = s.store.SaveCICDJob(ctx, job)
		return job, bindingErr
	}
	credentials, credentialErr := s.credentialIDs(ctx, project, job)
	if credentialErr != nil {
		err = credentialErr
	} else {
		err = client.upsertJob(ctx, job, credentials)
	}
	job.LastSyncedAt = time.Now().UTC()
	if err != nil {
		job.SyncStatus, job.SyncError = "failed", friendlyJenkinsError(err)
	} else {
		job.SyncStatus, job.SyncError = "ready", ""
	}
	if saveErr := s.store.SaveCICDJob(ctx, job); saveErr != nil {
		return Job{}, saveErr
	}
	if err != nil {
		return job, fmt.Errorf("%w: %s", ErrJenkins, job.SyncError)
	}
	return job, nil
}

func (s *Service) ListBuilds(ctx context.Context, project, environment string, limit int) ([]Build, error) {
	if limit <= 0 || limit > 200 {
		limit = 100
	}
	builds, err := s.store.ListCICDBuilds(ctx, project, environment, limit)
	if err != nil {
		return nil, err
	}
	for index := range builds {
		if builds[index].Status == "queued" || builds[index].Status == "running" {
			if refreshed, refreshErr := s.refreshBuild(ctx, builds[index]); refreshErr == nil {
				builds[index] = refreshed
			}
		}
	}
	return builds, nil
}

func (s *Service) HasActiveBuilds(ctx context.Context, project string) (bool, error) {
	return s.store.HasActiveCICDBuilds(ctx, strings.TrimSpace(project))
}

func (s *Service) DeleteProjectData(ctx context.Context, project string) error {
	active, err := s.store.HasActiveCICDBuilds(ctx, strings.TrimSpace(project))
	if err != nil {
		return err
	}
	if active {
		return fmt.Errorf("%w: project has an active Jenkins build", ErrConflict)
	}
	return s.store.DeleteCICDProject(ctx, strings.TrimSpace(project))
}

func (s *Service) TriggerBuild(ctx context.Context, project, jobKey, requestedBy string, input BuildInput) (Build, error) {
	job, err := s.store.GetCICDJob(ctx, project, jobKey)
	if err != nil {
		return Build{}, err
	}
	if !job.Enabled {
		return Build{}, fmt.Errorf("%w: Job 已停用", ErrConflict)
	}
	if job.SyncStatus != "ready" {
		return Build{}, fmt.Errorf("%w: 请先同步 Jenkins Job", ErrConflict)
	}
	connection, err := s.store.GetCICDConnection(ctx, project, job.ConnectionKey)
	if err != nil {
		return Build{}, err
	}
	requestedEnvironment := strings.ToLower(strings.TrimSpace(input.Environment))
	boundEnvironment := strings.ToLower(strings.TrimSpace(job.EnvironmentKey))
	if err := requireEnvironmentBinding("目标 Jenkins", connection.EnvironmentKey, boundEnvironment); err != nil {
		return Build{}, err
	}
	if requestedEnvironment == "" && boundEnvironment != "" {
		requestedEnvironment = boundEnvironment
		input.Environment = boundEnvironment
	}
	if boundEnvironment != "" && boundEnvironment != requestedEnvironment {
		return Build{}, fmt.Errorf("%w: 当前 Job 固定绑定 %s 环境，不能用于 %s 环境构建；请切换环境并选择对应 Job", ErrConflict, boundEnvironment, requestedEnvironment)
	}
	if len(input.Services) > 0 {
		selected := normalizeServiceKeys(input.Services)
		if len(selected) != len(input.Services) {
			return Build{}, fmt.Errorf("%w: 构建服务列表包含不合法或重复的服务标识", ErrInvalid)
		}
		if len(selected) > 1 {
			return Build{}, fmt.Errorf("%w: Jenkins 每次只能构建一个服务；平台多选会自动拆分为多个独立构建", ErrInvalid)
		}
		allowed := map[string]bool{}
		for _, service := range job.ServiceKeys {
			allowed[service] = true
		}
		for _, service := range selected {
			if !allowed[service] {
				return Build{}, fmt.Errorf("%w: 服务 %s 不属于当前 Job", ErrInvalid, service)
			}
		}
		input.Services = selected
	}
	services := normalizeServiceKeys(input.Services)
	if len(services) == 0 {
		services = []string{job.ServiceKeys[0]}
		input.Services = append([]string(nil), services...)
	}
	deliveryID := limit(strings.TrimSpace(input.Parameters[WebhookDeliveryParameter]), 128)
	if deliveryID != "" {
		if recent, listErr := s.store.ListCICDBuilds(ctx, project, requestedEnvironment, 200); listErr == nil {
			for _, existing := range recent {
				if existing.JobKey == job.Key && existing.Parameters[WebhookDeliveryParameter] == deliveryID {
					return existing, nil
				}
			}
		}
	}
	if (job.JenkinsfileMode == "generated" || job.CompactParameters) && s.buildSourcePreflight != nil {
		if err := s.buildSourcePreflight(ctx, project, job, services); err != nil {
			return Build{}, err
		}
	}
	_, client, err := s.clientForJob(ctx, project, job, requestedEnvironment)
	if err != nil {
		return Build{}, err
	}
	parameters := buildParameters(job, input)
	if job.JenkinsfileMode != "generated" && !job.CompactParameters {
		if err := validateBuildParameters(job.ParameterDefinitions, parameters); err != nil {
			return Build{}, err
		}
	}
	if job.JenkinsfileMode != "generated" && !job.CompactParameters {
		credentialIDs, err := s.credentialIDs(ctx, project, job)
		if err != nil {
			return Build{}, err
		}
		parameters["JENKINSFILE_CREDENTIAL_ID"] = credentialIDs["jenkinsfile"]
		parameters["MANIFEST_CREDENTIAL_ID"] = credentialIDs["manifest"]
	}
	queueURL, err := client.trigger(ctx, job.JenkinsJobName, parameters)
	if err != nil {
		return Build{}, err
	}
	if deliveryID != "" {
		parameters[WebhookDeliveryParameter] = deliveryID
	}
	// Jenkins can coalesce identical requests that are already queued. In that
	// case it returns the existing queue item, so re-use the platform record as
	// well instead of showing duplicate builds for one Jenkins execution.
	if active, listErr := s.store.ListCICDBuilds(ctx, project, requestedEnvironment, 200); listErr == nil {
		for _, existing := range active {
			if existing.JobKey == job.Key && existing.QueueURL == queueURL && (existing.Status == "queued" || existing.Status == "running") {
				return existing, nil
			}
		}
	}
	now := time.Now().UTC()
	build := Build{ID: fmt.Sprintf("cicd-%d-%s", now.UnixMilli(), randomSuffix()), ProjectKey: project, JobKey: job.Key, Environment: requestedEnvironment, RequestedBy: strings.TrimSpace(requestedBy), Status: "queued", QueueURL: queueURL, Parameters: parameters, Services: services, Progress: 5, CreatedAt: now, UpdatedAt: now}
	if err := s.store.SaveCICDBuild(ctx, build); err != nil {
		return Build{}, err
	}
	return build, nil
}

func (s *Service) RetryBuild(ctx context.Context, project, buildID, requestedBy string) (Build, error) {
	old, err := s.store.GetCICDBuild(ctx, project, buildID)
	if err != nil {
		return Build{}, err
	}
	services := []string(nil)
	if raw := strings.TrimSpace(old.Parameters["TARGET_SERVICES"]); raw != "" {
		services = strings.Split(raw, ",")
	}
	return s.TriggerBuild(ctx, project, old.JobKey, requestedBy, BuildInput{Environment: old.Environment, Services: services, Parameters: old.Parameters})
}

func (s *Service) CancelBuild(ctx context.Context, project, buildID string) (Build, error) {
	build, err := s.store.GetCICDBuild(ctx, project, buildID)
	if err != nil {
		return Build{}, err
	}
	job, err := s.store.GetCICDJob(ctx, project, build.JobKey)
	if err != nil {
		return Build{}, err
	}
	_, client, err := s.clientForJob(ctx, project, job, build.Environment)
	if err != nil {
		return Build{}, err
	}
	if err = client.cancel(ctx, build); err != nil {
		return Build{}, err
	}
	build.Status, build.Result, build.FinishedAt, build.UpdatedAt = "canceled", "ABORTED", time.Now().UTC(), time.Now().UTC()
	if err := s.store.SaveCICDBuild(ctx, build); err != nil {
		return Build{}, err
	}
	return build, nil
}

func (s *Service) BuildLogs(ctx context.Context, project, buildID string, offset int64) (LogChunk, error) {
	build, err := s.store.GetCICDBuild(ctx, project, buildID)
	if err != nil {
		return LogChunk{}, err
	}
	if build.BuildNumber == 0 {
		build, err = s.refreshBuild(ctx, build)
		if err != nil || build.BuildNumber == 0 {
			return LogChunk{}, fmt.Errorf("%w: 构建仍在 Jenkins 队列中，暂无日志", ErrConflict)
		}
	}
	job, err := s.store.GetCICDJob(ctx, project, build.JobKey)
	if err != nil {
		return LogChunk{}, err
	}
	_, client, err := s.clientForJob(ctx, project, job, build.Environment)
	if err != nil {
		return LogChunk{}, err
	}
	return client.logs(ctx, job.JenkinsJobName, build.BuildNumber, offset)
}

func (s *Service) BuildDeploymentLogs(ctx context.Context, project, buildID string) (DeploymentLog, error) {
	build, err := s.store.GetCICDBuild(ctx, project, buildID)
	if err != nil {
		return DeploymentLog{}, err
	}
	if build.BuildNumber == 0 {
		build, err = s.refreshBuild(ctx, build)
		if err != nil || build.BuildNumber == 0 {
			return DeploymentLog{}, fmt.Errorf("%w: 构建仍在 Jenkins 队列中，暂无部署日志", ErrConflict)
		}
	}
	job, err := s.store.GetCICDJob(ctx, project, build.JobKey)
	if err != nil {
		return DeploymentLog{}, err
	}
	_, client, err := s.clientForJob(ctx, project, job, build.Environment)
	if err != nil {
		return DeploymentLog{}, err
	}
	text, err := client.fullLogs(ctx, job.JenkinsJobName, build.BuildNumber)
	if err != nil {
		return DeploymentLog{}, err
	}
	return DeploymentLog{Text: extractDeploymentLog(text)}, nil
}

func (s *Service) refreshBuild(ctx context.Context, build Build) (Build, error) {
	job, err := s.store.GetCICDJob(ctx, build.ProjectKey, build.JobKey)
	if err != nil {
		return build, err
	}
	_, client, err := s.clientForJob(ctx, build.ProjectKey, job, build.Environment)
	if err != nil {
		return build, err
	}
	updated, err := client.refresh(ctx, job.JenkinsJobName, build)
	if err != nil {
		build.Error, build.UpdatedAt = friendlyJenkinsError(err), time.Now().UTC()
	} else {
		build = updated
		build.Error = ""
		if build.BuildNumber > 0 {
			if stages, progress, current, stageErr := client.stageProgress(ctx, job.JenkinsJobName, build.BuildNumber, build.Status); stageErr == nil {
				build.Stages, build.Progress, build.CurrentStage = filterBuildStages(stages, build.Services, build.Status, progress, current)
			} else if logs, logErr := client.fullLogs(ctx, job.JenkinsJobName, build.BuildNumber); logErr == nil {
				stages, progress, current := markerProgress(logs, job, build.Status)
				build.Stages, build.Progress, build.CurrentStage = filterBuildStages(stages, build.Services, build.Status, progress, current)
			}
		}
		if build.Status == "succeeded" {
			build.Progress = 100
		}
	}
	if saveErr := s.store.SaveCICDBuild(ctx, build); saveErr != nil {
		return build, saveErr
	}
	return build, err
}

func filterBuildStages(stages []BuildStage, services []string, buildStatus string, fallbackProgress int, fallbackCurrent string) ([]BuildStage, int, string) {
	allowed := map[string]bool{}
	for _, service := range normalizeServiceKeys(services) {
		allowed[service] = true
	}
	if len(allowed) == 0 {
		return stages, fallbackProgress, fallbackCurrent
	}
	filtered := make([]BuildStage, 0, len(stages))
	completed, current := 0.0, ""
	for _, stage := range stages {
		if stage.Service != "" && !allowed[strings.ToLower(strings.TrimSpace(stage.Service))] {
			continue
		}
		filtered = append(filtered, stage)
		switch stage.Status {
		case "succeeded", "failed", "canceled", "skipped":
			completed++
		case "running":
			completed += 0.35
			if current == "" {
				if stage.Service == "" {
					current = stage.Name
				} else {
					current = stage.Service + " / " + stage.Name
				}
			}
		}
	}
	if len(filtered) == 0 {
		return filtered, fallbackProgress, ""
	}
	progress := int(completed * 100 / float64(len(filtered)))
	if buildStatus == "running" && progress >= 100 {
		progress = 95
	}
	if buildStatus == "succeeded" {
		progress, current = 100, ""
	}
	if progress < 0 {
		progress = 0
	}
	if progress > 100 {
		progress = 100
	}
	return filtered, progress, current
}

func (s *Service) client(ctx context.Context, project, key string) (StoredConnection, *jenkinsClient, error) {
	record, err := s.store.GetCICDConnection(ctx, project, key)
	if err != nil {
		return StoredConnection{}, nil, err
	}
	clientRecord := record
	if record.ConnectionMode == "eks_port_forward" {
		if s.tunnels == nil {
			return StoredConnection{}, nil, errors.New("EKS Jenkins tunnel service is unavailable")
		}
		localURL, tunnelErr := s.tunnels.Ensure(ctx, ManagedEndpoint{
			ProjectKey: record.ProjectKey, EnvironmentKey: record.EnvironmentKey, TargetName: record.TargetName,
			Region: record.Region, ClusterName: record.ClusterName, Namespace: record.Namespace,
			ServiceName: record.ServiceName, ServicePort: record.ServicePort,
		})
		if tunnelErr != nil {
			return StoredConnection{}, nil, fmt.Errorf("open EKS Jenkins tunnel: %w", tunnelErr)
		}
		clientRecord.BaseURL = localURL
	}
	token, err := s.decrypt(connectionAAD(project, key), record.TokenCipher)
	if err != nil {
		return StoredConnection{}, nil, err
	}
	defer clear(token)
	client, err := s.newClient(clientRecord, string(token))
	return record, client, err
}

func (s *Service) clientForJob(ctx context.Context, project string, job Job, requestedEnvironment string) (StoredConnection, *jenkinsClient, error) {
	expected := strings.ToLower(strings.TrimSpace(job.EnvironmentKey))
	requestedEnvironment = strings.ToLower(strings.TrimSpace(requestedEnvironment))
	if requestedEnvironment != "" && expected != requestedEnvironment {
		return StoredConnection{}, nil, fmt.Errorf("%w：Job 属于 %s 环境，当前操作请求的是 %s，禁止跨环境访问 Jenkins", ErrConflict, expected, requestedEnvironment)
	}
	record, err := s.store.GetCICDConnection(ctx, project, job.ConnectionKey)
	if err != nil {
		return StoredConnection{}, nil, err
	}
	if err := requireEnvironmentBinding("目标 Jenkins", record.EnvironmentKey, expected); err != nil {
		return StoredConnection{}, nil, err
	}
	return s.client(ctx, project, job.ConnectionKey)
}

func (s *Service) credentialIDs(ctx context.Context, project string, job Job) (map[string]string, error) {
	result := map[string]string{}
	for name, key := range map[string]string{"jenkinsfile": job.JenkinsfileCredential, "manifest": job.ManifestCredential} {
		if key == "" {
			continue
		}
		record, err := s.store.GetCICDCredential(ctx, project, key)
		if err != nil {
			return nil, err
		}
		if record.Kind != "existing" && record.SyncStatus != "ready" {
			return nil, fmt.Errorf("%w: 凭据 %s 尚未同步到 Jenkins", ErrConflict, record.DisplayName)
		}
		if record.ConnectionKey != job.ConnectionKey || record.EnvironmentKey != job.EnvironmentKey {
			return nil, fmt.Errorf("%w: 凭据 %s 不属于当前 %s 环境 Jenkins", ErrConflict, record.DisplayName, job.EnvironmentKey)
		}
		result[name] = record.ExternalID
	}
	return result, nil
}

func (s *Service) encrypt(aad, plaintext []byte) (string, error) {
	if len(plaintext) == 0 || len(plaintext) > maxSecretBytes {
		return "", ErrCredentialSecret
	}
	nonce := make([]byte, s.aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", err
	}
	sealed := s.aead.Seal(nil, nonce, plaintext, aad)
	return base64.RawStdEncoding.EncodeToString(append(nonce, sealed...)), nil
}

func (s *Service) decrypt(aad []byte, encoded string) ([]byte, error) {
	payload, err := base64.RawStdEncoding.DecodeString(encoded)
	if err != nil || len(payload) <= s.aead.NonceSize() {
		return nil, errors.New("encrypted CI/CD secret is invalid")
	}
	nonce, ciphertext := payload[:s.aead.NonceSize()], payload[s.aead.NonceSize():]
	plaintext, err := s.aead.Open(nil, nonce, ciphertext, aad)
	clear(payload)
	return plaintext, err
}

func connectionAAD(project, key string) []byte {
	return []byte("ops-deploy/cicd/connection/" + project + "/" + key)
}
func credentialAAD(project, key string) []byte {
	return []byte("ops-deploy/cicd/credential/" + project + "/" + key)
}

func validateJenkinsURL(raw string, allowInsecureHTTP bool) (string, error) {
	raw = strings.TrimSpace(raw)
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Host == "" || (parsed.Scheme != "https" && parsed.Scheme != "http") {
		return "", errors.New("Jenkins 地址必须是包含 http:// 或 https:// 的完整 URL")
	}
	if parsed.User != nil {
		return "", errors.New("Jenkins 地址不能内嵌用户名或密码")
	}
	if parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", errors.New("Jenkins 地址不能包含查询参数或锚点")
	}
	if parsed.Scheme == "http" && parsed.Hostname() != "localhost" && parsed.Hostname() != "127.0.0.1" && parsed.Hostname() != "::1" && !allowInsecureHTTP {
		return "", errors.New("外部 HTTP Jenkins 需要明确确认允许明文连接")
	}
	parsed.Path = strings.TrimSuffix(parsed.EscapedPath(), "/")
	return strings.TrimSuffix(parsed.String(), "/"), nil
}

func validateCredentialSecret(kind string, secret CredentialSecret) ([]byte, bool, error) {
	supplied := strings.TrimSpace(secret.Username+secret.Password+secret.SecretText+secret.PrivateKey+secret.Passphrase) != ""
	if kind == "existing" {
		return nil, false, nil
	}
	if !supplied {
		return nil, false, nil
	}
	valid := ((kind == "username_password" || kind == "gitlab_token") && secret.Username != "" && secret.Password != "") || (kind == "secret_text" && secret.SecretText != "") || (kind == "ssh_private_key" && secret.Username != "" && strings.Contains(secret.PrivateKey, "PRIVATE KEY"))
	if !valid {
		return nil, false, fmt.Errorf("%w: 凭据秘密内容不完整", ErrInvalid)
	}
	payload, err := json.Marshal(secret)
	if err != nil || len(payload) > maxSecretBytes {
		return nil, false, ErrInvalid
	}
	return payload, true, nil
}

func validCredentialKind(kind string) bool {
	return kind == "existing" || kind == "gitlab_token" || kind == "username_password" || kind == "secret_text" || kind == "ssh_private_key"
}

func validRepositoryProvider(value string) bool {
	return value == "gitlab" || value == "generic_git"
}

func validRepositoryPurpose(value string) bool {
	return value == "jenkinsfile" || value == "manifest" || value == "source" || value == "general"
}

func (s *Service) resolveJobRepositories(ctx context.Context, project string, job *Job) error {
	sharedDeliveryRepository := job.JenkinsfileRepository != "" && job.JenkinsfileRepository == job.ManifestRepository
	items := []struct {
		key     string
		purpose string
		apply   func(Repository)
	}{
		{job.JenkinsfileRepository, "jenkinsfile", func(repo Repository) {
			job.JenkinsfileRepo = repo.CloneURL
			if strings.TrimSpace(job.JenkinsfileBranch) == "" {
				job.JenkinsfileBranch = repo.DefaultBranch
			}
			if strings.TrimSpace(job.JenkinsfilePath) == "" {
				job.JenkinsfilePath = repo.DefaultPath
			}
		}},
		{job.SourceRepository, "source", func(repo Repository) { job.SourceRepo = repo.CloneURL }},
		{job.ManifestRepository, "manifest", func(repo Repository) {
			job.ManifestRepo = repo.CloneURL
			if strings.TrimSpace(job.ManifestBranch) == "" {
				job.ManifestBranch = repo.DefaultBranch
			}
			if strings.TrimSpace(job.ManifestPath) == "" {
				job.ManifestPath = repo.DefaultPath
			}
		}},
	}
	for _, item := range items {
		if item.key == "" {
			continue
		}
		repository, err := s.store.GetCICDRepository(ctx, project, item.key)
		if err != nil {
			return fmt.Errorf("%w: Job 引用了不存在的项目仓库 %s", ErrInvalid, item.key)
		}
		sharedPurpose := sharedDeliveryRepository && (repository.Purpose == "jenkinsfile" || repository.Purpose == "manifest")
		if repository.Purpose != item.purpose && repository.Purpose != "general" && !sharedPurpose {
			return fmt.Errorf("%w: 仓库 %s 的用途不适用于 %s", ErrInvalid, repository.DisplayName, item.purpose)
		}
		item.apply(repository)
	}
	return nil
}

func validJob(job Job) bool {
	if job.ProjectKey == "" || !keyPattern.MatchString(job.Key) || !validDeploymentEnvironment(job.EnvironmentKey) || !keyPattern.MatchString(job.ConnectionKey) || strings.TrimSpace(job.DisplayName) == "" || len(job.ServiceKeys) == 0 || !jenkinsNamePattern.MatchString(job.JenkinsJobName) {
		return false
	}
	if job.Language != "java" && job.Language != "go" && job.Language != "node" && job.Language != "mixed" {
		return false
	}
	if job.JenkinsfileMode != "existing" && job.JenkinsfileMode != "generated" {
		return false
	}
	if job.ExecutionMode != "serial" && job.ExecutionMode != "parallel" {
		return false
	}
	if job.FailurePolicy != "stop" && job.FailurePolicy != "continue" {
		return false
	}
	if job.TriggerMode != "manual" && job.TriggerMode != "gitlab_push" {
		return false
	}
	if job.TriggerMode == "gitlab_push" && !validGitBranch(job.TriggerBranch) {
		return false
	}
	for _, raw := range []string{job.JenkinsfileRepo, job.ManifestRepo} {
		parsed, err := url.Parse(strings.TrimSpace(raw))
		if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil {
			return false
		}
	}
	return true
}

func validDeploymentEnvironment(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "dev", "test", "uat", "prod":
		return true
	default:
		return false
	}
}

func requireEnvironmentBinding(resource, actual, expected string) error {
	actual = strings.ToLower(strings.TrimSpace(actual))
	expected = strings.ToLower(strings.TrimSpace(expected))
	if !validDeploymentEnvironment(expected) {
		return fmt.Errorf("%w：请先选择 dev、test、uat 或 prod 环境", ErrInvalid)
	}
	if actual == "" {
		return fmt.Errorf("%w：%s 是旧的项目级配置，尚未绑定环境；为防止测试与生产串用，请在 %s 环境重新创建独立配置", ErrConflict, resource, expected)
	}
	if actual != expected {
		return fmt.Errorf("%w：%s 属于 %s 环境，当前选择的是 %s，禁止跨环境使用", ErrConflict, resource, actual, expected)
	}
	return nil
}

func generatedJenkinsfilePath(environment, key string) string {
	return "environments/" + environment + "/pipelines/" + key + "/Jenkinsfile"
}

func validateRepositoryPath(value string, allowDirectory bool) error {
	value = strings.TrimSpace(value)
	if strings.Contains(value, "\\") {
		return errors.New("路径分隔符必须使用 /")
	}
	if value == "" || len(value) > 500 || strings.HasPrefix(value, "/") || strings.ContainsAny(value, "\x00\r\n") {
		return errors.New("必须填写仓库内相对路径")
	}
	cleaned := path.Clean(value)
	if cleaned == "." || cleaned == ".." || strings.HasPrefix(cleaned, "../") || cleaned != strings.TrimSuffix(value, "/") {
		return errors.New("不能包含重复分隔符、当前目录或上级目录")
	}
	if !allowDirectory && strings.HasSuffix(value, "/") {
		return errors.New("必须指向具体文件")
	}
	return nil
}

func canonicalRepositoryURL(value string) string {
	return strings.TrimSuffix(strings.TrimSpace(value), "/")
}

func (s *Service) validateJobPathOwnership(ctx context.Context, input Job) error {
	jobs, err := s.store.ListCICDJobs(ctx, input.ProjectKey)
	if err != nil {
		return err
	}
	for _, existing := range jobs {
		if existing.Key == input.Key {
			continue
		}
		if canonicalRepositoryURL(existing.JenkinsfileRepo) == canonicalRepositoryURL(input.JenkinsfileRepo) &&
			existing.JenkinsfileBranch == input.JenkinsfileBranch && path.Clean(existing.JenkinsfilePath) == path.Clean(input.JenkinsfilePath) {
			return fmt.Errorf("%w: Jenkinsfile 路径已被 %s 环境的 Job %s 使用，请修改环境目录或 Job 标识", ErrConflict, existing.EnvironmentKey, existing.Key)
		}
	}
	return nil
}

func validGitBranch(branch string) bool {
	branch = strings.TrimSpace(branch)
	return branch != "" && len(branch) <= 255 && !strings.ContainsAny(branch, " ~^:?*[\\\n\r\t") &&
		!strings.HasPrefix(branch, "/") && !strings.HasSuffix(branch, "/") && !strings.Contains(branch, "..") && !strings.Contains(branch, "//")
}

func buildParameters(job Job, input BuildInput) map[string]string {
	if job.JenkinsfileMode == "generated" || job.CompactParameters {
		selected := selectedBuildServices(job, input.Services)
		branch := strings.TrimSpace(input.Branch)
		if branch == "" {
			branch = strings.TrimSpace(input.Parameters["GIT_BRANCH"])
		}
		return map[string]string{
			"GIT_BRANCH":      limit(branch, 255),
			"TARGET_SERVICES": strings.Join(selected, ","),
		}
	}
	result := cleanMap(job.Parameters, 64)
	if _, exists := result["GIT_BRANCH"]; !exists {
		result["GIT_BRANCH"] = "main"
	}
	if _, exists := result["IMAGE_TAG"]; !exists {
		result["IMAGE_TAG"] = ""
	}
	for _, parameter := range job.ParameterDefinitions {
		if _, exists := result[parameter.Name]; !exists {
			result[parameter.Name] = parameter.DefaultValue
		}
	}
	for key, value := range cleanMap(input.Parameters, 64) {
		if _, reserved := map[string]bool{"SERVICE_NAME": true, "TARGET_SERVICES": true, "SERVICE_LANGUAGE": true, "SOURCE_REPO_URL": true, "MANIFEST_REPO_URL": true, "MANIFEST_BRANCH": true, "MANIFEST_PATH": true}[key]; !reserved {
			result[key] = value
		}
	}
	selected := normalizeServiceKeys(input.Services)
	if len(selected) == 0 {
		selected = append([]string(nil), job.ServiceKeys...)
	}
	allowed := map[string]bool{}
	for _, service := range job.ServiceKeys {
		allowed[service] = true
	}
	for _, service := range selected {
		if !allowed[service] {
			continue
		}
	}
	filtered := selected[:0]
	for _, service := range selected {
		if allowed[service] {
			filtered = append(filtered, service)
		}
	}
	selected = filtered
	if len(selected) == 0 {
		selected = append([]string(nil), job.ServiceKeys...)
	}
	environment := strings.ToLower(strings.TrimSpace(input.Environment))
	result["DEPLOY_ENV"], result["SERVICE_NAME"], result["SERVICE_LANGUAGE"] = environment, job.ServiceName, job.Language
	result["TARGET_SERVICES"] = strings.Join(selected, ",")
	result["SOURCE_REPO_URL"], result["MANIFEST_REPO_URL"], result["MANIFEST_BRANCH"] = job.SourceRepo, job.ManifestRepo, job.ManifestBranch
	result["MANIFEST_PATH"] = job.ManifestPath
	if path := job.EnvironmentPaths[environment]; path != "" {
		result["MANIFEST_PATH"] = path
	}
	if input.Branch != "" {
		result["GIT_BRANCH"] = limit(input.Branch, 255)
	}
	if input.ImageTag != "" {
		result["IMAGE_TAG"] = limit(input.ImageTag, 255)
	}
	if job.BuildCommand != "" {
		result["BUILD_COMMAND"] = job.BuildCommand
	}
	if job.Language == "java" {
		result["JDK_VERSION"] = job.RuntimeVersion
	} else {
		result["GO_VERSION"] = job.RuntimeVersion
	}
	return result
}

func selectedBuildServices(job Job, requested []string) []string {
	selected := normalizeServiceKeys(requested)
	if len(selected) == 0 {
		return []string{job.ServiceKeys[0]}
	}
	allowed := make(map[string]bool, len(job.ServiceKeys))
	for _, service := range job.ServiceKeys {
		allowed[service] = true
	}
	filtered := make([]string, 0, len(selected))
	for _, service := range selected {
		if allowed[service] {
			filtered = append(filtered, service)
		}
	}
	if len(filtered) == 0 {
		return []string{job.ServiceKeys[0]}
	}
	return filtered[:1]
}

func generatedJobEnvironment(connectionEnvironment, configured string) string {
	for _, value := range []string{connectionEnvironment, configured, "dev"} {
		value = strings.ToLower(strings.TrimSpace(value))
		if value == "dev" || value == "test" || value == "uat" || value == "prod" {
			return value
		}
	}
	return "dev"
}

func generatedJobConfiguration(input map[string]string, environment, manifestCredentialID string) map[string]string {
	allowed := map[string]bool{
		"JENKINS_AGENT_MODE": true, "JENKINS_AGENT_LABEL": true,
		"JENKINS_KUBERNETES_SERVICE_ACCOUNT": true, "AWS_PROFILE": true,
		"PIPELINE_TIMEOUT_MINUTES": true, "TELEGRAM_CREDENTIALS_ID": true,
		"LARK_CREDENTIALS_ID": true, "LARK_ALERT_CHANNEL": true, "LARK_ALERT_ENVIRONMENT": true, "DEPLOY_ENV": true,
		"MANIFEST_CREDENTIAL_ID": true, "DEPLOY_VERIFY_MODE": true,
		"ROLLOUT_TIMEOUT_MINUTES": true, "ROLLBACK_ON_FAILURE": true,
	}
	result := make(map[string]string, len(allowed))
	for key, value := range cleanMap(input, 64) {
		if allowed[key] {
			result[key] = value
		}
	}
	result["DEPLOY_ENV"] = environment
	verifyMode := strings.ToLower(strings.TrimSpace(result["DEPLOY_VERIFY_MODE"]))
	if verifyMode != "apply" && verifyMode != "rollout" {
		verifyMode = "rollout"
	}
	result["DEPLOY_VERIFY_MODE"] = verifyMode
	rolloutTimeout := 5
	if value, err := strconv.Atoi(strings.TrimSpace(result["ROLLOUT_TIMEOUT_MINUTES"])); err == nil && value >= 1 && value <= 30 {
		rolloutTimeout = value
	}
	result["ROLLOUT_TIMEOUT_MINUTES"] = strconv.Itoa(rolloutTimeout)
	result["ROLLBACK_ON_FAILURE"] = strconv.FormatBool(strings.EqualFold(strings.TrimSpace(result["ROLLBACK_ON_FAILURE"]), "true"))
	if value := strings.TrimSpace(manifestCredentialID); value != "" {
		result["MANIFEST_CREDENTIAL_ID"] = limit(value, 128)
	}
	return result
}

func normalizeServiceKeys(input []string) []string {
	result := make([]string, 0, len(input))
	seen := map[string]bool{}
	for _, value := range input {
		value = strings.ToLower(strings.TrimSpace(value))
		if keyPattern.MatchString(value) && !seen[value] {
			result = append(result, value)
			seen[value] = true
		}
		if len(result) == 50 {
			break
		}
	}
	sort.Strings(result)
	return result
}

func extractDeploymentLog(input string) string {
	lines := strings.Split(input, "\n")
	result := make([]string, 0, len(lines))
	inSection := false
	for _, line := range lines {
		if strings.Contains(line, "@@OPS_DEPLOY_BEGIN") {
			inSection = true
			result = append(result, line)
			continue
		}
		if strings.Contains(line, "@@OPS_DEPLOY_END") {
			result = append(result, line)
			inSection = false
			continue
		}
		lower := strings.ToLower(line)
		if inSection || strings.Contains(lower, "kubectl ") || strings.Contains(lower, "helm ") || strings.Contains(lower, "rollout") || strings.Contains(lower, "deploy(") || strings.Contains(lower, "deployment") {
			result = append(result, line)
		}
	}
	if len(result) == 0 {
		return "当前 Jenkinsfile 没有输出可识别的部署阶段日志。平台生成的 Jenkinsfile 会自动标记部署日志。\n"
	}
	if len(result) > 1200 {
		result = result[len(result)-1200:]
	}
	return strings.Join(result, "\n") + "\n"
}

func markerProgress(logs string, job Job, buildStatus string) ([]BuildStage, int, string) {
	type stageState struct {
		stage BuildStage
		order int
	}
	states := map[string]stageState{}
	order := 0
	for _, line := range strings.Split(logs, "\n") {
		index := strings.Index(line, "@@OPS_STAGE|")
		if index < 0 {
			continue
		}
		parts := strings.Split(strings.TrimSpace(line[index:]), "|")
		if len(parts) < 4 {
			continue
		}
		service, kind := strings.ToLower(strings.TrimSpace(parts[1])), strings.ToLower(strings.TrimSpace(parts[2]))
		if !keyPattern.MatchString(service) {
			continue
		}
		key := service + "/" + kind
		state, exists := states[key]
		if !exists {
			state.order, order = order, order+1
		}
		state.stage = BuildStage{Name: markerStageName(kind), Service: service, Kind: kind, Status: normalizeMarkerStatus(parts[3])}
		states[key] = state
	}
	if strings.Contains(logs, "@@OPS_DEPLOY_BEGIN") {
		for _, service := range job.ServiceKeys {
			_, serviceRan := states[service+"/checkout"]
			if !serviceRan && !strings.Contains(logs, "@@OPS_DEPLOY|"+service+"|") {
				continue
			}
			key := service + "/deploy"
			state, exists := states[key]
			if !exists {
				state.order, order = order, order+1
				state.stage = BuildStage{Name: "更新部署清单", Service: service, Kind: "deploy", Status: "running"}
				states[key] = state
			}
		}
	}
	ordered := make([]stageState, 0, len(states))
	for _, state := range states {
		ordered = append(ordered, state)
	}
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].order < ordered[j].order })
	stages := make([]BuildStage, 0, len(ordered))
	completed, current := 0.0, ""
	for _, state := range ordered {
		stage := state.stage
		if stage.Status == "running" {
			switch buildStatus {
			case "failed":
				stage.Status = "failed"
			case "canceled":
				stage.Status = "canceled"
			}
		}
		stages = append(stages, stage)
		switch stage.Status {
		case "succeeded", "failed", "canceled", "skipped":
			completed++
		case "running":
			completed += 0.35
			if current == "" {
				current = stage.Service + " / " + stage.Name
			}
		}
	}
	// 平台生成流水线固定为：源码检出、Docker 镜像、Kubernetes 部署。
	// 业务编译已经收敛到 Dockerfile，不再单独计算 Jenkins 编译阶段。
	expected := len(job.ServiceKeys) * 3
	if expected < len(stages) {
		expected = len(stages)
	}
	progress := 10
	if expected > 0 {
		progress = 5 + int(completed*90/float64(expected))
	}
	if buildStatus == "succeeded" {
		progress = 100
	} else if progress > 95 {
		progress = 95
	}
	return stages, progress, current
}

func markerStageName(kind string) string {
	switch kind {
	case "checkout":
		return "检出源码"
	case "build":
		return "编译与测试"
	case "image":
		return "构建镜像"
	case "deploy":
		return "部署到 Kubernetes"
	default:
		return kind
	}
}

func normalizeMarkerStatus(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "success", "succeeded":
		return "succeeded"
	case "failure", "failed":
		return "failed"
	case "canceled", "cancelled", "aborted":
		return "canceled"
	default:
		return "running"
	}
}

func cleanParameterDefinitions(input []ParameterDefinition) ([]ParameterDefinition, error) {
	if len(input) > 64 {
		return nil, fmt.Errorf("%w: Jenkins 构建参数最多 64 个", ErrInvalid)
	}
	result := make([]ParameterDefinition, 0, len(input))
	seen := map[string]bool{}
	for _, parameter := range input {
		parameter.Name = strings.TrimSpace(parameter.Name)
		parameter.Type = strings.ToLower(strings.TrimSpace(parameter.Type))
		parameter.DefaultValue = strings.TrimSpace(parameter.DefaultValue)
		parameter.Description = limit(parameter.Description, 500)
		if !parameterNamePattern.MatchString(parameter.Name) || seen[parameter.Name] {
			return nil, fmt.Errorf("%w: Jenkins 参数名 %q 不合法或重复", ErrInvalid, parameter.Name)
		}
		seen[parameter.Name] = true
		switch parameter.Type {
		case "string":
			parameter.Choices = nil
		case "choice":
			choices := make([]string, 0, len(parameter.Choices))
			choiceSeen := map[string]bool{}
			for _, choice := range parameter.Choices {
				choice = limit(choice, 255)
				if choice != "" && !choiceSeen[choice] {
					choices = append(choices, choice)
					choiceSeen[choice] = true
				}
				if len(choices) == 100 {
					break
				}
			}
			if len(choices) == 0 {
				return nil, fmt.Errorf("%w: 选项参数 %s 至少需要一个可选值", ErrInvalid, parameter.Name)
			}
			parameter.Choices = choices
			if !choiceSeen[parameter.DefaultValue] {
				parameter.DefaultValue = choices[0]
			} else if parameter.DefaultValue != choices[0] {
				ordered := []string{parameter.DefaultValue}
				for _, choice := range choices {
					if choice != parameter.DefaultValue {
						ordered = append(ordered, choice)
					}
				}
				parameter.Choices = ordered
			}
		case "number":
			parameter.Choices = nil
			if parameter.DefaultValue != "" {
				if _, err := strconv.ParseFloat(parameter.DefaultValue, 64); err != nil {
					return nil, fmt.Errorf("%w: 参数 %s 的默认值必须是数字", ErrInvalid, parameter.Name)
				}
			}
		case "boolean":
			parameter.Choices = nil
			if !strings.EqualFold(parameter.DefaultValue, "true") {
				parameter.DefaultValue = "false"
			} else {
				parameter.DefaultValue = "true"
			}
		default:
			return nil, fmt.Errorf("%w: 参数 %s 的类型不受支持", ErrInvalid, parameter.Name)
		}
		if len(parameter.DefaultValue) > 4096 {
			return nil, fmt.Errorf("%w: 参数 %s 的默认值过长", ErrInvalid, parameter.Name)
		}
		result = append(result, parameter)
	}
	return result, nil
}

func validateBuildParameters(definitions []ParameterDefinition, values map[string]string) error {
	for _, parameter := range definitions {
		value := strings.TrimSpace(values[parameter.Name])
		if parameter.Required && value == "" {
			return fmt.Errorf("%w: 构建参数 %s 不能为空", ErrInvalid, parameter.Name)
		}
		switch parameter.Type {
		case "choice":
			valid := false
			for _, choice := range parameter.Choices {
				if value == choice {
					valid = true
					break
				}
			}
			if !valid {
				return fmt.Errorf("%w: 构建参数 %s 不在允许的选项中", ErrInvalid, parameter.Name)
			}
		case "number":
			if value != "" {
				if _, err := strconv.ParseFloat(value, 64); err != nil {
					return fmt.Errorf("%w: 构建参数 %s 必须是数字", ErrInvalid, parameter.Name)
				}
			}
		case "boolean":
			if value != "true" && value != "false" {
				return fmt.Errorf("%w: 构建参数 %s 必须是布尔值", ErrInvalid, parameter.Name)
			}
		}
	}
	return nil
}

func cleanMap(input map[string]string, max int) map[string]string {
	result := map[string]string{}
	keys := make([]string, 0, len(input))
	for key := range input {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		if len(result) >= max {
			break
		}
		key = strings.TrimSpace(key)
		value := strings.TrimSpace(input[key])
		if key != "" && len(key) <= 128 && len(value) <= 4096 {
			result[key] = value
		}
	}
	return result
}

func randomSuffix() string {
	b := make([]byte, 4)
	if _, err := rand.Read(b); err != nil {
		return "00000000"
	}
	return fmt.Sprintf("%x", b)
}
func limit(value string, max int) string {
	value = strings.TrimSpace(value)
	if len(value) > max {
		return value[:max]
	}
	return value
}
func defaultValue(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return strings.TrimSpace(value)
}
func friendlyJenkinsError(err error) string {
	if err == nil {
		return ""
	}
	value := strings.TrimSpace(err.Error())
	if len(value) > 1000 {
		value = value[:1000]
	}
	return value
}

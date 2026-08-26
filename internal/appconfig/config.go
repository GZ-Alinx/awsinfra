package appconfig

import (
	"bytes"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

type Config struct {
	EnvironmentFile string               `yaml:"environment_file" json:"-"`
	Server          ServerConfig         `yaml:"server" json:"server"`
	Security        SecurityConfig       `yaml:"security" json:"-"`
	Paths           PathsConfig          `yaml:"paths" json:"paths"`
	Tools           ToolsConfig          `yaml:"tools" json:"tools"`
	AWS             AWSConfig            `yaml:"aws" json:"aws"`
	TerraformState  TerraformStateConfig `yaml:"terraform_state" json:"terraform_state"`
	DataStore       DataStoreConfig      `yaml:"datastore" json:"-"`
	Jobs            JobsConfig           `yaml:"jobs" json:"jobs"`
	Components      []ComponentConfig    `yaml:"components" json:"components"`
}

type ServerConfig struct {
	ListenAddress string        `yaml:"listen_address" json:"listen_address"`
	TLSCertFile   string        `yaml:"tls_cert_file" json:"-"`
	TLSKeyFile    string        `yaml:"tls_key_file" json:"-"`
	ReadTimeout   time.Duration `yaml:"-" json:"-"`
	WriteTimeout  time.Duration `yaml:"-" json:"-"`
	ReadText      string        `yaml:"read_timeout" json:"read_timeout"`
	WriteText     string        `yaml:"write_timeout" json:"write_timeout"`
}

type SecurityConfig struct {
	AdminUsername                string        `yaml:"admin_username"`
	SessionCookieName            string        `yaml:"session_cookie_name"`
	PasswordHashEnv              string        `yaml:"password_hash_env"`
	CredentialKeyEnv             string        `yaml:"credential_key_env"`
	CookieSecure                 bool          `yaml:"cookie_secure"`
	SessionTTL                   time.Duration `yaml:"-"`
	SessionTTLText               string        `yaml:"session_ttl"`
	LoginMaxAttempts             int           `yaml:"login_max_attempts"`
	LoginWindow                  time.Duration `yaml:"-"`
	LoginWindowText              string        `yaml:"login_window"`
	LoginLockout                 time.Duration `yaml:"-"`
	LoginLockoutText             string        `yaml:"login_lockout"`
	AllowPrivateHelmRepositories bool          `yaml:"allow_private_helm_repositories"`
	ExternalOrigin               string        `yaml:"external_origin"`
}

type PathsConfig struct {
	RepositoryRoot       string `yaml:"repository_root" json:"repository_root"`
	EnvironmentsDir      string `yaml:"environments_dir" json:"environments_dir"`
	DataDir              string `yaml:"data_dir" json:"data_dir"`
	TerraformInfraDir    string `yaml:"terraform_infra_dir" json:"terraform_infra_dir"`
	TerraformPlatformDir string `yaml:"terraform_platform_dir" json:"terraform_platform_dir"`
	EtcdChartDir         string `yaml:"etcd_chart_dir" json:"etcd_chart_dir"`
}

type ToolsConfig struct {
	Terraform string `yaml:"terraform" json:"terraform"`
	AWS       string `yaml:"aws" json:"aws"`
	Kubectl   string `yaml:"kubectl" json:"kubectl"`
	Helm      string `yaml:"helm" json:"helm"`
}

type AWSConfig struct {
	Profile string `yaml:"profile" json:"profile"`
}

// TerraformStateConfig enables remote state and retains the previous
// per-project layout solely for safe discovery during central migration. The
// live central bucket and credential are encrypted platform settings in MySQL.
type TerraformStateConfig struct {
	Enabled      bool   `yaml:"enabled" json:"enabled"`
	Region       string `yaml:"region" json:"region"`
	BucketPrefix string `yaml:"bucket_prefix" json:"bucket_prefix"`
	KeyPrefix    string `yaml:"key_prefix" json:"key_prefix"`
	AutoCreate   bool   `yaml:"auto_create" json:"auto_create"`
}

type DataStoreConfig struct {
	MySQL MySQLConfig `yaml:"mysql"`
	Redis RedisConfig `yaml:"redis"`
}

type MySQLConfig struct {
	DSNEnv       string        `yaml:"dsn_env"`
	MaxOpenConns int           `yaml:"max_open_conns"`
	MaxIdleConns int           `yaml:"max_idle_conns"`
	ConnMaxLife  time.Duration `yaml:"-"`
	ConnMaxText  string        `yaml:"conn_max_lifetime"`
}

type RedisConfig struct {
	Address         string        `yaml:"address"`
	PasswordEnv     string        `yaml:"password_env"`
	Database        int           `yaml:"database"`
	KeyPrefix       string        `yaml:"key_prefix"`
	StatusTTL       time.Duration `yaml:"-"`
	StatusTTLText   string        `yaml:"status_ttl"`
	JobStateTTL     time.Duration `yaml:"-"`
	JobStateTTLText string        `yaml:"job_state_ttl"`
}

type JobsConfig struct {
	MaxParallel  int           `yaml:"max_parallel" json:"max_parallel"`
	Timeout      time.Duration `yaml:"-" json:"-"`
	TimeoutText  string        `yaml:"timeout" json:"timeout"`
	HistoryLimit int           `yaml:"history_limit" json:"history_limit"`
}

type ComponentConfig struct {
	Key         string `yaml:"key" json:"key"`
	DisplayName string `yaml:"display_name" json:"display_name"`
	Category    string `yaml:"category" json:"category"`
	Description string `yaml:"description" json:"description"`
	ConfigPath  string `yaml:"config_path" json:"config_path"`
	Stage       string `yaml:"stage" json:"stage"`
	StatusType  string `yaml:"status_type" json:"status_type"`
	StatusName  string `yaml:"status_name" json:"status_name"`
	Hidden      bool   `yaml:"hidden" json:"hidden"`
	Kind        string `yaml:"kind" json:"kind"`
}

func Load(path string) (*Config, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("resolve config path: %w", err)
	}
	b, err := loadConfigWithBase(abs, map[string]bool{})
	if err != nil {
		return nil, err
	}

	var cfg Config
	decoder := yaml.NewDecoder(bytes.NewReader(b))
	decoder.KnownFields(true)
	if err := decoder.Decode(&cfg); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}
	if err := cfg.applyDefaults(filepath.Dir(abs)); err != nil {
		return nil, err
	}
	if err := loadEnvironmentFile(cfg.EnvironmentFile); err != nil {
		return nil, err
	}
	if err := cfg.applyRuntimeEnvironment(); err != nil {
		return nil, err
	}
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return &cfg, nil
}

// applyRuntimeEnvironment keeps the checked-in YAML usable in local Docker,
// systemd and Kubernetes without generating a second copy of the component
// catalog. Only settings that must change with the runtime network boundary
// are overridable; secrets continue to use their dedicated environment names.
func (c *Config) applyRuntimeEnvironment() error {
	if value := strings.TrimSpace(os.Getenv("OPS_DEPLOY_LISTEN_ADDRESS")); value != "" {
		c.Server.ListenAddress = value
	}
	if value := strings.TrimSpace(os.Getenv("OPS_DEPLOY_REDIS_ADDRESS")); value != "" {
		c.DataStore.Redis.Address = value
	}
	if value, exists := os.LookupEnv("OPS_DEPLOY_COOKIE_SECURE"); exists {
		parsed, err := strconv.ParseBool(strings.TrimSpace(value))
		if err != nil {
			return fmt.Errorf("invalid OPS_DEPLOY_COOKIE_SECURE: %w", err)
		}
		c.Security.CookieSecure = parsed
	}
	if value, exists := os.LookupEnv("OPS_DEPLOY_EXTERNAL_ORIGIN"); exists {
		c.Security.ExternalOrigin = strings.TrimSpace(value)
	}
	return nil
}

// loadConfigWithBase lets a test or upgrade instance inherit the full catalog
// from the production-like configuration while overriding only its listener,
// storage and runtime paths. The resulting document is still decoded with
// KnownFields, so misspelled settings remain fatal.
func loadConfigWithBase(path string, seen map[string]bool) ([]byte, error) {
	path = filepath.Clean(path)
	if seen[path] {
		return nil, fmt.Errorf("configuration extends cycle at %s", path)
	}
	seen[path] = true
	defer delete(seen, path)
	payload, err := os.ReadFile(path) // #nosec G304 -- the config path is explicitly selected by the local administrator.
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}
	var overlay map[string]any
	if err := yaml.Unmarshal(payload, &overlay); err != nil {
		return nil, fmt.Errorf("parse config overlay: %w", err)
	}
	rawBase, hasBase := overlay["extends"]
	delete(overlay, "extends")
	if !hasBase {
		return yaml.Marshal(overlay)
	}
	baseName, ok := rawBase.(string)
	if !ok || strings.TrimSpace(baseName) == "" {
		return nil, errors.New("config extends must be a non-empty file path")
	}
	basePath := strings.TrimSpace(baseName)
	if !filepath.IsAbs(basePath) {
		basePath = filepath.Join(filepath.Dir(path), basePath)
	}
	basePayload, err := loadConfigWithBase(basePath, seen)
	if err != nil {
		return nil, err
	}
	var base map[string]any
	if err := yaml.Unmarshal(basePayload, &base); err != nil {
		return nil, err
	}
	mergeConfigMaps(base, overlay)
	return yaml.Marshal(base)
}

func mergeConfigMaps(target, overlay map[string]any) {
	for key, value := range overlay {
		child, childOK := value.(map[string]any)
		parent, parentOK := target[key].(map[string]any)
		if childOK && parentOK {
			mergeConfigMaps(parent, child)
			continue
		}
		target[key] = value
	}
}

func (c *Config) applyDefaults(base string) error {
	if c.EnvironmentFile == "" {
		c.EnvironmentFile = ".env"
	}
	if c.Server.ListenAddress == "" {
		c.Server.ListenAddress = "127.0.0.1:8080"
	}
	if c.Server.ReadText == "" {
		c.Server.ReadText = "30s"
	}
	if c.Server.WriteText == "" {
		c.Server.WriteText = "30s"
	}
	if c.Jobs.TimeoutText == "" {
		c.Jobs.TimeoutText = "3h"
	}
	if c.Security.AdminUsername == "" {
		c.Security.AdminUsername = "admin"
	}
	if c.Security.SessionCookieName == "" {
		c.Security.SessionCookieName = "ops_deploy_session"
	}
	if c.Security.PasswordHashEnv == "" {
		c.Security.PasswordHashEnv = "OPS_DEPLOY_PASSWORD_HASH"
	}
	if c.Security.CredentialKeyEnv == "" {
		c.Security.CredentialKeyEnv = "OPS_DEPLOY_CREDENTIAL_KEY"
	}
	if c.Security.SessionTTLText == "" {
		c.Security.SessionTTLText = "8h"
	}
	if c.Security.LoginWindowText == "" {
		c.Security.LoginWindowText = "15m"
	}
	if c.Security.LoginLockoutText == "" {
		c.Security.LoginLockoutText = "15m"
	}
	if c.Security.LoginMaxAttempts <= 0 {
		c.Security.LoginMaxAttempts = 5
	}
	if c.DataStore.MySQL.DSNEnv == "" {
		c.DataStore.MySQL.DSNEnv = "OPS_MYSQL_DSN"
	}
	if c.DataStore.MySQL.MaxOpenConns <= 0 {
		c.DataStore.MySQL.MaxOpenConns = 10
	}
	if c.DataStore.MySQL.MaxIdleConns <= 0 {
		c.DataStore.MySQL.MaxIdleConns = 5
	}
	if c.DataStore.MySQL.ConnMaxText == "" {
		c.DataStore.MySQL.ConnMaxText = "30m"
	}
	if c.DataStore.Redis.Address == "" {
		c.DataStore.Redis.Address = "127.0.0.1:6379"
	}
	if c.DataStore.Redis.PasswordEnv == "" {
		c.DataStore.Redis.PasswordEnv = "OPS_REDIS_PASSWORD"
	}
	if c.DataStore.Redis.KeyPrefix == "" {
		c.DataStore.Redis.KeyPrefix = "ops:manager"
	}
	if c.DataStore.Redis.StatusTTLText == "" {
		c.DataStore.Redis.StatusTTLText = "5s"
	}
	if c.DataStore.Redis.JobStateTTLText == "" {
		c.DataStore.Redis.JobStateTTLText = "24h"
	}
	if c.TerraformState.Region == "" {
		c.TerraformState.Region = "ap-south-1"
	}
	if c.TerraformState.BucketPrefix == "" {
		c.TerraformState.BucketPrefix = "ops-tfstate"
	}
	if c.TerraformState.KeyPrefix == "" {
		c.TerraformState.KeyPrefix = "ops-deploy"
	}
	var err error
	if c.Server.ReadTimeout, err = time.ParseDuration(c.Server.ReadText); err != nil {
		return fmt.Errorf("invalid server.read_timeout: %w", err)
	}
	if c.Server.WriteTimeout, err = time.ParseDuration(c.Server.WriteText); err != nil {
		return fmt.Errorf("invalid server.write_timeout: %w", err)
	}
	if c.Jobs.Timeout, err = time.ParseDuration(c.Jobs.TimeoutText); err != nil {
		return fmt.Errorf("invalid jobs.timeout: %w", err)
	}
	if c.Security.SessionTTL, err = time.ParseDuration(c.Security.SessionTTLText); err != nil {
		return fmt.Errorf("invalid security.session_ttl: %w", err)
	}
	if c.Security.LoginWindow, err = time.ParseDuration(c.Security.LoginWindowText); err != nil {
		return fmt.Errorf("invalid security.login_window: %w", err)
	}
	if c.Security.LoginLockout, err = time.ParseDuration(c.Security.LoginLockoutText); err != nil {
		return fmt.Errorf("invalid security.login_lockout: %w", err)
	}
	if c.DataStore.MySQL.ConnMaxLife, err = time.ParseDuration(c.DataStore.MySQL.ConnMaxText); err != nil {
		return fmt.Errorf("invalid datastore.mysql.conn_max_lifetime: %w", err)
	}
	if c.DataStore.Redis.StatusTTL, err = time.ParseDuration(c.DataStore.Redis.StatusTTLText); err != nil {
		return fmt.Errorf("invalid datastore.redis.status_ttl: %w", err)
	}
	if c.DataStore.Redis.JobStateTTL, err = time.ParseDuration(c.DataStore.Redis.JobStateTTLText); err != nil {
		return fmt.Errorf("invalid datastore.redis.job_state_ttl: %w", err)
	}
	if c.Jobs.MaxParallel <= 0 {
		c.Jobs.MaxParallel = 1
	}
	if c.Jobs.HistoryLimit <= 0 {
		c.Jobs.HistoryLimit = 100
	}
	if c.Paths.RepositoryRoot == "" {
		c.Paths.RepositoryRoot = "."
	}
	c.Paths.RepositoryRoot = resolve(base, c.Paths.RepositoryRoot)
	c.EnvironmentFile = resolve(c.Paths.RepositoryRoot, c.EnvironmentFile)
	c.Paths.EnvironmentsDir = resolve(c.Paths.RepositoryRoot, defaultString(c.Paths.EnvironmentsDir, "environments"))
	c.Paths.DataDir = resolve(c.Paths.RepositoryRoot, defaultString(c.Paths.DataDir, "data"))
	c.Paths.TerraformInfraDir = resolve(c.Paths.RepositoryRoot, defaultString(c.Paths.TerraformInfraDir, "terraform/infra"))
	c.Paths.TerraformPlatformDir = resolve(c.Paths.RepositoryRoot, defaultString(c.Paths.TerraformPlatformDir, "terraform/platform"))
	c.Paths.EtcdChartDir = resolve(c.Paths.RepositoryRoot, defaultString(c.Paths.EtcdChartDir, "terraform/platform/charts/etcd"))
	if c.Server.TLSCertFile != "" {
		c.Server.TLSCertFile = resolve(c.Paths.RepositoryRoot, c.Server.TLSCertFile)
	}
	if c.Server.TLSKeyFile != "" {
		c.Server.TLSKeyFile = resolve(c.Paths.RepositoryRoot, c.Server.TLSKeyFile)
	}
	c.Tools.Terraform = defaultString(c.Tools.Terraform, "terraform")
	c.Tools.AWS = defaultString(c.Tools.AWS, "aws")
	c.Tools.Kubectl = defaultString(c.Tools.Kubectl, "kubectl")
	c.Tools.Helm = defaultString(c.Tools.Helm, "helm")
	return nil
}

func (c *Config) Validate() error {
	host, _, err := net.SplitHostPort(c.Server.ListenAddress)
	if err != nil {
		return fmt.Errorf("invalid server.listen_address: %w", err)
	}
	if c.Security.AdminUsername == "" || c.Security.PasswordHashEnv == "" || c.Security.CredentialKeyEnv == "" {
		return errors.New("security.admin_username, password_hash_env and credential_key_env are required")
	}
	if len(c.Security.SessionCookieName) > 64 || strings.Trim(c.Security.SessionCookieName, "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789_-") != "" {
		return errors.New("security.session_cookie_name must contain only letters, digits, underscores, or hyphens")
	}
	if (c.Server.TLSCertFile == "") != (c.Server.TLSKeyFile == "") {
		return errors.New("server.tls_cert_file and server.tls_key_file must be set together")
	}
	if c.Security.SessionTTL <= 0 || c.Security.LoginWindow <= 0 || c.Security.LoginLockout <= 0 {
		return errors.New("security durations must be positive")
	}
	if c.DataStore.MySQL.DSNEnv == "" || c.DataStore.Redis.PasswordEnv == "" || c.DataStore.Redis.Address == "" {
		return errors.New("MySQL DSN environment and Redis connection settings are required")
	}
	if !isLoopback(host) && !c.Security.CookieSecure {
		return errors.New("security.cookie_secure must be true for a non-loopback listener")
	}
	if c.Security.ExternalOrigin != "" {
		origin, err := url.Parse(c.Security.ExternalOrigin)
		if err != nil || origin.Scheme != "https" || origin.Host == "" || origin.User != nil || origin.Path != "" || origin.RawQuery != "" || origin.Fragment != "" {
			return errors.New("security.external_origin must be an HTTPS origin without a path, query or fragment")
		}
	}
	if c.TerraformState.Enabled {
		if !validS3BucketPrefix(c.TerraformState.BucketPrefix) {
			return errors.New("terraform_state.bucket_prefix must contain 3 to 20 lowercase letters, digits, or hyphens")
		}
		if !validStateKeyPrefix(c.TerraformState.KeyPrefix) {
			return errors.New("terraform_state.key_prefix must contain only letters, digits, dots, underscores, slashes, or hyphens")
		}
		if strings.TrimSpace(c.TerraformState.Region) == "" {
			return errors.New("terraform_state.region is required when remote state is enabled")
		}
	}
	seen := make(map[string]struct{}, len(c.Components))
	for _, component := range c.Components {
		if component.Key == "" || component.ConfigPath == "" {
			return errors.New("every component requires key and config_path")
		}
		if _, ok := seen[component.Key]; ok {
			return fmt.Errorf("duplicate component key %q", component.Key)
		}
		seen[component.Key] = struct{}{}
	}
	return nil
}

func (c *Config) PasswordHash() string {
	return os.Getenv(c.Security.PasswordHashEnv)
}

func (c *Config) CredentialKey() string { return os.Getenv(c.Security.CredentialKeyEnv) }

func (c *Config) MySQLDSN() string { return os.Getenv(c.DataStore.MySQL.DSNEnv) }

func (c *Config) RedisPassword() string { return os.Getenv(c.DataStore.Redis.PasswordEnv) }

func (c *Config) IsLocalOnly() bool {
	host, _, err := net.SplitHostPort(c.Server.ListenAddress)
	return err == nil && isLoopback(host)
}

func isLoopback(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func validS3BucketPrefix(value string) bool {
	if len(value) < 3 || len(value) > 20 || strings.HasPrefix(value, "-") || strings.HasSuffix(value, "-") {
		return false
	}
	return strings.Trim(value, "abcdefghijklmnopqrstuvwxyz0123456789-") == ""
}

func validStateKeyPrefix(value string) bool {
	value = strings.Trim(strings.TrimSpace(value), "/")
	return value != "" && len(value) <= 128 && strings.Trim(value, "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789._/-") == ""
}

func resolve(base, path string) string {
	if filepath.IsAbs(path) {
		return filepath.Clean(path)
	}
	return filepath.Clean(filepath.Join(base, path))
}

func defaultString(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}

func loadEnvironmentFile(path string) error {
	payload, err := os.ReadFile(path) // #nosec G304 -- path is resolved beneath the administrator-selected repository root.
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read environment file: %w", err)
	}
	for index, rawLine := range strings.Split(string(payload), "\n") {
		line := strings.TrimSpace(rawLine)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, found := strings.Cut(line, "=")
		key = strings.TrimSpace(key)
		if !found || key == "" || strings.ContainsAny(key, " \t") {
			return fmt.Errorf("invalid environment file line %d", index+1)
		}
		value = strings.TrimSpace(value)
		if len(value) >= 2 && ((value[0] == '\'' && value[len(value)-1] == '\'') || (value[0] == '"' && value[len(value)-1] == '"')) {
			value = value[1 : len(value)-1]
		}
		if _, exists := os.LookupEnv(key); !exists {
			if err := os.Setenv(key, value); err != nil {
				return fmt.Errorf("set %s from environment file: %w", key, err)
			}
		}
	}
	return nil
}

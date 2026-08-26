package selfdeploy

import (
	"bytes"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
)

var (
	awsRegionPattern   = regexp.MustCompile(`^[a-z]{2}(?:-gov)?-[a-z]+-[0-9]$`)
	clusterNamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,99}$`)
	kubeNamePattern    = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`)
	repositoryPattern  = regexp.MustCompile(`^(?:[a-z0-9]+(?:[._-][a-z0-9]+)*/)*[a-z0-9]+(?:[._-][a-z0-9]+)*$`)
	quantityPattern    = regexp.MustCompile(`^[1-9][0-9]*(?:Mi|Gi|Ti)$`)
	hostPattern        = regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9.-]{0,251}[A-Za-z0-9])?$`)
)

type Config struct {
	Cluster    ClusterConfig    `yaml:"cluster"`
	Registry   RegistryConfig   `yaml:"registry"`
	Build      BuildConfig      `yaml:"build"`
	Kubernetes KubernetesConfig `yaml:"kubernetes"`
}

type ClusterConfig struct {
	Name         string `yaml:"name"`
	Region       string `yaml:"region"`
	Profile      string `yaml:"profile"`
	Kubeconfig   string `yaml:"kubeconfig"`
	ContextAlias string `yaml:"context_alias"`
}

type RegistryConfig struct {
	Repository       string `yaml:"repository"`
	CreateRepository bool   `yaml:"create_repository"`
	Tag              string `yaml:"tag"`
}

type BuildConfig struct {
	Context    string `yaml:"context"`
	Dockerfile string `yaml:"dockerfile"`
	Platform   string `yaml:"platform"`
}

type KubernetesConfig struct {
	Namespace        string        `yaml:"namespace"`
	ManifestTemplate string        `yaml:"manifest_template"`
	SecretsFile      string        `yaml:"secrets_file"`
	StorageClass     string        `yaml:"storage_class"`
	PlatformStorage  string        `yaml:"platform_storage"`
	MySQLStorage     string        `yaml:"mysql_storage"`
	RedisStorage     string        `yaml:"redis_storage"`
	ServiceType      string        `yaml:"service_type"`
	ExternalOrigin   string        `yaml:"external_origin"`
	CookieSecure     bool          `yaml:"cookie_secure"`
	Ingress          IngressConfig `yaml:"ingress"`
}

type IngressConfig struct {
	Enabled                  bool   `yaml:"enabled"`
	ClassName                string `yaml:"class_name"`
	Host                     string `yaml:"host"`
	TLSSecretName            string `yaml:"tls_secret_name"`
	TLSSecretSourceNamespace string `yaml:"tls_secret_source_namespace"`
	GatewayServiceNamespace  string `yaml:"gateway_service_namespace"`
	GatewayServiceName       string `yaml:"gateway_service_name"`
}

func LoadConfig(path string) (*Config, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("resolve deployment config: %w", err)
	}
	payload, err := os.ReadFile(abs) // #nosec G304 -- selected by the local operator.
	if err != nil {
		return nil, fmt.Errorf("read deployment config: %w", err)
	}
	var config Config
	decoder := yaml.NewDecoder(bytes.NewReader(payload))
	decoder.KnownFields(true)
	if err := decoder.Decode(&config); err != nil {
		return nil, fmt.Errorf("parse deployment config: %w", err)
	}
	config.applyDefaults(filepath.Dir(abs))
	if err := config.Validate(); err != nil {
		return nil, err
	}
	return &config, nil
}

func (c *Config) applyDefaults(base string) {
	c.Cluster.Name = strings.TrimSpace(c.Cluster.Name)
	c.Cluster.Region = strings.TrimSpace(c.Cluster.Region)
	c.Cluster.Profile = strings.TrimSpace(c.Cluster.Profile)
	if strings.TrimSpace(c.Cluster.Kubeconfig) == "" {
		c.Cluster.Kubeconfig = filepath.Join(base, "kubeconfig")
	}
	c.Cluster.Kubeconfig = resolvePath(base, c.Cluster.Kubeconfig)
	if strings.TrimSpace(c.Cluster.ContextAlias) == "" {
		c.Cluster.ContextAlias = "ops-deploy-platform"
	}
	if strings.TrimSpace(c.Build.Context) == "" {
		c.Build.Context = "../.."
	}
	c.Build.Context = resolvePath(base, c.Build.Context)
	if strings.TrimSpace(c.Build.Dockerfile) == "" {
		c.Build.Dockerfile = "Dockerfile"
	}
	c.Build.Dockerfile = resolvePath(base, c.Build.Dockerfile)
	if strings.TrimSpace(c.Build.Platform) == "" {
		c.Build.Platform = "linux/amd64"
	}
	if strings.TrimSpace(c.Kubernetes.ManifestTemplate) == "" {
		c.Kubernetes.ManifestTemplate = "ops-deploy.yaml.tmpl"
	}
	c.Kubernetes.ManifestTemplate = resolvePath(base, c.Kubernetes.ManifestTemplate)
	if strings.TrimSpace(c.Kubernetes.SecretsFile) == "" {
		c.Kubernetes.SecretsFile = "secrets.env"
	}
	c.Kubernetes.SecretsFile = resolvePath(base, c.Kubernetes.SecretsFile)
	if c.Kubernetes.ServiceType == "" {
		c.Kubernetes.ServiceType = "ClusterIP"
	}
	c.Kubernetes.ExternalOrigin = strings.TrimSpace(c.Kubernetes.ExternalOrigin)
	c.Kubernetes.Ingress.ClassName = strings.TrimSpace(c.Kubernetes.Ingress.ClassName)
	c.Kubernetes.Ingress.Host = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(c.Kubernetes.Ingress.Host), "."))
	c.Kubernetes.Ingress.TLSSecretName = strings.TrimSpace(c.Kubernetes.Ingress.TLSSecretName)
	c.Kubernetes.Ingress.TLSSecretSourceNamespace = strings.TrimSpace(c.Kubernetes.Ingress.TLSSecretSourceNamespace)
	c.Kubernetes.Ingress.GatewayServiceNamespace = strings.TrimSpace(c.Kubernetes.Ingress.GatewayServiceNamespace)
	c.Kubernetes.Ingress.GatewayServiceName = strings.TrimSpace(c.Kubernetes.Ingress.GatewayServiceName)
	if c.Kubernetes.Ingress.Enabled && c.Kubernetes.Ingress.GatewayServiceNamespace == "" {
		c.Kubernetes.Ingress.GatewayServiceNamespace = c.Kubernetes.Ingress.TLSSecretSourceNamespace
	}
	if c.Kubernetes.Ingress.Enabled && c.Kubernetes.Ingress.GatewayServiceName == "" && c.Kubernetes.Ingress.ClassName == "higress" {
		c.Kubernetes.Ingress.GatewayServiceName = "higress-gateway"
	}
	if c.Kubernetes.Ingress.Enabled && c.Kubernetes.ExternalOrigin == "" {
		c.Kubernetes.ExternalOrigin = "https://" + strings.TrimSpace(c.Kubernetes.Ingress.Host)
	}
}

func (c *Config) Validate() error {
	if !clusterNamePattern.MatchString(c.Cluster.Name) {
		return errors.New("cluster.name must be a valid EKS cluster name")
	}
	if !awsRegionPattern.MatchString(c.Cluster.Region) {
		return errors.New("cluster.region must be a valid AWS region")
	}
	if !kubeNamePattern.MatchString(c.Cluster.ContextAlias) || len(c.Cluster.ContextAlias) > 63 {
		return errors.New("cluster.context_alias must be a Kubernetes-style name up to 63 characters")
	}
	if !repositoryPattern.MatchString(c.Registry.Repository) || len(c.Registry.Repository) > 256 {
		return errors.New("registry.repository must be a valid private ECR repository name")
	}
	if c.Build.Platform != "linux/amd64" && c.Build.Platform != "linux/arm64" {
		return errors.New("build.platform must be linux/amd64 or linux/arm64")
	}
	if !kubeNamePattern.MatchString(c.Kubernetes.Namespace) || len(c.Kubernetes.Namespace) > 63 {
		return errors.New("kubernetes.namespace must be a valid Kubernetes namespace")
	}
	if !kubeNamePattern.MatchString(c.Kubernetes.StorageClass) || len(c.Kubernetes.StorageClass) > 63 {
		return errors.New("kubernetes.storage_class must be a valid StorageClass name")
	}
	for name, value := range map[string]string{
		"platform_storage": c.Kubernetes.PlatformStorage,
		"mysql_storage":    c.Kubernetes.MySQLStorage,
		"redis_storage":    c.Kubernetes.RedisStorage,
	} {
		if !quantityPattern.MatchString(value) {
			return fmt.Errorf("kubernetes.%s must be a positive Mi/Gi/Ti quantity", name)
		}
	}
	if c.Kubernetes.ServiceType != "ClusterIP" && c.Kubernetes.ServiceType != "LoadBalancer" {
		return errors.New("kubernetes.service_type must be ClusterIP or LoadBalancer")
	}
	if c.Kubernetes.ExternalOrigin != "" {
		origin, err := url.Parse(c.Kubernetes.ExternalOrigin)
		if err != nil || origin.Scheme != "https" || origin.Host == "" || origin.Path != "" || origin.RawQuery != "" || origin.Fragment != "" {
			return errors.New("kubernetes.external_origin must be an HTTPS origin without a path")
		}
		if !c.Kubernetes.CookieSecure {
			return errors.New("kubernetes.cookie_secure must be true when external_origin is configured")
		}
	}
	if c.Kubernetes.Ingress.Enabled {
		if !kubeNamePattern.MatchString(c.Kubernetes.Ingress.ClassName) || !kubeNamePattern.MatchString(c.Kubernetes.Ingress.TLSSecretName) {
			return errors.New("enabled ingress requires valid class_name and tls_secret_name")
		}
		if !kubeNamePattern.MatchString(c.Kubernetes.Ingress.TLSSecretSourceNamespace) || len(c.Kubernetes.Ingress.TLSSecretSourceNamespace) > 63 {
			return errors.New("enabled ingress requires a valid tls_secret_source_namespace")
		}
		if !hostPattern.MatchString(c.Kubernetes.Ingress.Host) || !strings.Contains(c.Kubernetes.Ingress.Host, ".") {
			return errors.New("enabled ingress requires a valid DNS host")
		}
		if !kubeNamePattern.MatchString(c.Kubernetes.Ingress.GatewayServiceNamespace) || len(c.Kubernetes.Ingress.GatewayServiceNamespace) > 63 ||
			!kubeNamePattern.MatchString(c.Kubernetes.Ingress.GatewayServiceName) || len(c.Kubernetes.Ingress.GatewayServiceName) > 63 {
			return errors.New("enabled ingress requires valid gateway_service_namespace and gateway_service_name")
		}
		origin, _ := url.Parse(c.Kubernetes.ExternalOrigin)
		if origin == nil || !strings.EqualFold(origin.Hostname(), c.Kubernetes.Ingress.Host) || origin.Port() != "" {
			return fmt.Errorf("kubernetes.external_origin must exactly match enabled ingress host: https://%s", c.Kubernetes.Ingress.Host)
		}
	}
	for label, path := range map[string]string{
		"build.context":                c.Build.Context,
		"build.dockerfile":             c.Build.Dockerfile,
		"kubernetes.manifest_template": c.Kubernetes.ManifestTemplate,
	} {
		if _, err := os.Stat(path); err != nil {
			return fmt.Errorf("%s is unavailable: %w", label, err)
		}
	}
	return nil
}

func resolvePath(base, value string) string {
	value = strings.TrimSpace(value)
	if value == "~" || strings.HasPrefix(value, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			value = filepath.Join(home, strings.TrimPrefix(value, "~/"))
		}
	}
	if !filepath.IsAbs(value) {
		value = filepath.Join(base, value)
	}
	return filepath.Clean(value)
}

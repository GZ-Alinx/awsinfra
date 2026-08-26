package resourcecenter

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"ops-deploy-platform/internal/appconfig"
	"ops-deploy-platform/internal/environment"
	statusservice "ops-deploy-platform/internal/status"
)

var (
	kubernetesNamePattern = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`)
	kubernetesKeyPattern  = regexp.MustCompile(`^[A-Za-z0-9._-]{1,253}$`)
)

type Store interface {
	LoadResourceSnapshot(context.Context, string, string) ([]byte, error)
	SaveResourceSnapshot(context.Context, string, string, []byte) error
}

type Snapshot struct {
	SchemaVersion int             `json:"schema_version"`
	Project       string          `json:"project"`
	Environment   string          `json:"environment"`
	ObservedAt    time.Time       `json:"observed_at"`
	CloudSync     CloudSync       `json:"cloud_sync"`
	Info          EnvironmentInfo `json:"info"`
	Resources     []Resource      `json:"resources"`
	Warnings      []string        `json:"warnings"`
}

// CloudSync summarizes the three-way comparison used by the platform:
// Terraform's last known state is the baseline, the environment document is
// the desired state, and AWS APIs provide the observed state.  Keeping these
// values separate prevents an out-of-band console edit from being silently
// overwritten by the next deployment.
type CloudSync struct {
	Status          string    `json:"status"`
	ObservedAt      time.Time `json:"observed_at,omitempty"`
	SyncedFields    int       `json:"synced_fields"`
	PendingFields   int       `json:"pending_fields"`
	DriftedFields   int       `json:"drifted_fields"`
	ConflictFields  int       `json:"conflict_fields"`
	Unavailable     int       `json:"unavailable_resources"`
	BlockingChanges bool      `json:"blocking_changes"`
}

type ConfigurationField struct {
	Path     string `json:"path"`
	Label    string `json:"label"`
	Desired  any    `json:"desired,omitempty"`
	Actual   any    `json:"actual,omitempty"`
	State    string `json:"state"`
	Syncable bool   `json:"syncable"`
}

type EnvironmentInfo struct {
	AWSAccountID      string            `json:"aws_account_id,omitempty"`
	Region            string            `json:"region"`
	AvailabilityZones []string          `json:"availability_zones"`
	VPCID             string            `json:"vpc_id,omitempty"`
	VPCCIDR           string            `json:"vpc_cidr"`
	ClusterName       string            `json:"cluster_name"`
	ClusterEndpoint   string            `json:"cluster_endpoint,omitempty"`
	Namespaces        []string          `json:"namespaces"`
	PublicSubnets     map[string]string `json:"public_subnets"`
	PrivateSubnets    map[string]string `json:"private_subnets"`
	NetworkMode       string            `json:"network_mode"`
	NATGatewayMode    string            `json:"nat_gateway_mode"`
	NATGatewayIPs     map[string]string `json:"nat_gateway_ips"`
}

type Resource struct {
	Key           string               `json:"key"`
	DisplayName   string               `json:"display_name"`
	Category      string               `json:"category"`
	Source        string               `json:"source"`
	Provider      string               `json:"provider"`
	Status        string               `json:"status"`
	Version       string               `json:"version,omitempty"`
	Specification string               `json:"specification,omitempty"`
	Namespace     string               `json:"namespace,omitempty"`
	AccessPoints  []AccessPoint        `json:"access_points"`
	Credentials   []Credential         `json:"credentials"`
	Metadata      map[string]any       `json:"metadata"`
	Configuration []ConfigurationField `json:"configuration,omitempty"`
	// Baseline is persisted only inside the encrypted/private platform data
	// store. Public() intentionally excludes it from API responses.
	Baseline map[string]any `json:"baseline,omitempty"`
}

type AccessPoint struct {
	Name        string `json:"name"`
	Type        string `json:"type"`
	Visibility  string `json:"visibility"`
	Protocol    string `json:"protocol"`
	Host        string `json:"host,omitempty"`
	Port        int    `json:"port,omitempty"`
	URL         string `json:"url,omitempty"`
	Description string `json:"description,omitempty"`
}

type Credential struct {
	ID        string `json:"id"`
	Label     string `json:"label"`
	Username  string `json:"username,omitempty"`
	Provider  string `json:"provider"`
	SecretRef string `json:"secret_ref"`
	Available bool   `json:"available"`
}

type PublicSnapshot struct {
	SchemaVersion int              `json:"schema_version"`
	Project       string           `json:"project"`
	Environment   string           `json:"environment"`
	ObservedAt    time.Time        `json:"observed_at"`
	CloudSync     CloudSync        `json:"cloud_sync"`
	Info          EnvironmentInfo  `json:"info"`
	Resources     []PublicResource `json:"resources"`
	Warnings      []string         `json:"warnings"`
}

type PublicResource struct {
	Key           string               `json:"key"`
	DisplayName   string               `json:"display_name"`
	Category      string               `json:"category"`
	Source        string               `json:"source"`
	Provider      string               `json:"provider"`
	Status        string               `json:"status"`
	Version       string               `json:"version,omitempty"`
	Specification string               `json:"specification,omitempty"`
	Namespace     string               `json:"namespace,omitempty"`
	AccessPoints  []AccessPoint        `json:"access_points"`
	Credentials   []PublicCredential   `json:"credentials"`
	Metadata      map[string]any       `json:"metadata"`
	Configuration []ConfigurationField `json:"configuration,omitempty"`
}

type PublicCredential struct {
	ID        string `json:"id"`
	Label     string `json:"label"`
	Username  string `json:"username,omitempty"`
	Provider  string `json:"provider"`
	Available bool   `json:"available"`
}

func (snapshot Snapshot) Public() PublicSnapshot {
	result := PublicSnapshot{
		SchemaVersion: snapshot.SchemaVersion,
		Project:       snapshot.Project, Environment: snapshot.Environment, ObservedAt: snapshot.ObservedAt,
		CloudSync: snapshot.CloudSync, Info: snapshot.Info, Warnings: append([]string(nil), snapshot.Warnings...),
		Resources: make([]PublicResource, 0, len(snapshot.Resources)),
	}
	for _, resource := range snapshot.Resources {
		item := PublicResource{
			Key: resource.Key, DisplayName: resource.DisplayName, Category: resource.Category,
			Source: resource.Source, Provider: resource.Provider, Status: resource.Status,
			Version: resource.Version, Specification: resource.Specification, Namespace: resource.Namespace,
			AccessPoints: append(make([]AccessPoint, 0, len(resource.AccessPoints)), resource.AccessPoints...), Metadata: resource.Metadata,
			Configuration: append(make([]ConfigurationField, 0, len(resource.Configuration)), resource.Configuration...),
			Credentials:   make([]PublicCredential, 0, len(resource.Credentials)),
		}
		for _, credential := range resource.Credentials {
			item.Credentials = append(item.Credentials, PublicCredential{
				ID: credential.ID, Label: credential.Label, Username: credential.Username,
				Provider: credential.Provider, Available: credential.Available,
			})
		}
		result.Resources = append(result.Resources, item)
	}
	return result
}

type Service struct {
	config         *appconfig.Config
	environments   *environment.Repository
	status         *statusservice.Service
	store          Store
	awsProvider    AWSCredentialProvider
	outputProvider TerraformOutputProvider
}

type AWSCredentialProvider interface {
	Environment(context.Context, string) ([]string, error)
}

type TerraformOutputProvider interface {
	StateOutputs(context.Context, string, string, string) (map[string]any, error)
}

func New(config *appconfig.Config, environments *environment.Repository, status *statusservice.Service, store Store) *Service {
	return &Service{config: config, environments: environments, status: status, store: store}
}

func (s *Service) SetAWSCredentialProvider(provider AWSCredentialProvider) {
	s.awsProvider = provider
}

func (s *Service) SetTerraformOutputProvider(provider TerraformOutputProvider) {
	s.outputProvider = provider
}

func (s *Service) Load(ctx context.Context, project, environmentName string) (Snapshot, error) {
	if s.store == nil {
		return Snapshot{}, os.ErrNotExist
	}
	payload, err := s.store.LoadResourceSnapshot(ctx, project, environmentName)
	if err != nil {
		return Snapshot{}, err
	}
	var snapshot Snapshot
	if err := json.Unmarshal(payload, &snapshot); err != nil {
		return Snapshot{}, err
	}
	return snapshot, nil
}

func (s *Service) Refresh(ctx context.Context, project, environmentName, targetName string) (Snapshot, error) {
	doc, err := s.environments.Load(targetName)
	if err != nil {
		return Snapshot{}, err
	}
	doc = environment.ApplyDefaults(doc, project, environmentName)
	previous, _ := s.Load(ctx, project, environmentName)
	var report *statusservice.Report
	var statusErr error
	var outputs map[string]any
	var outputErr error
	var actual map[string]actualCloudResource
	var cloudWarnings []string
	var wait sync.WaitGroup
	wait.Add(3)
	go func() {
		defer wait.Done()
		report, statusErr = s.status.CollectFresh(ctx, targetName)
	}()
	go func() {
		defer wait.Done()
		outputs, outputErr = s.terraformOutputs(ctx, project, targetName)
	}()
	go func() {
		defer wait.Done()
		actual, cloudWarnings = s.collectCloudConfiguration(ctx, project, doc, previous)
	}()
	wait.Wait()
	snapshot := s.build(project, environmentName, targetName, doc, report, outputs)
	snapshot.SchemaVersion = cloudConfigurationSchemaVersion
	s.attachCloudConfiguration(&snapshot, doc, actual, previous)
	if statusErr != nil {
		snapshot.Warnings = append(snapshot.Warnings, "集群状态采集失败："+statusErr.Error())
	}
	if outputErr != nil {
		snapshot.Warnings = append(snapshot.Warnings, "Terraform 输出尚不可用，资源部署完成后将自动补齐访问地址")
	}
	snapshot.Warnings = append(snapshot.Warnings, cloudWarnings...)
	if err := s.persistSnapshot(ctx, snapshot); err != nil {
		return Snapshot{}, err
	}
	return snapshot, nil
}

func (s *Service) Reveal(ctx context.Context, project, environmentName, credentialID string) (map[string]string, error) {
	snapshot, err := s.Load(ctx, project, environmentName)
	if err != nil {
		return nil, err
	}
	for _, resource := range snapshot.Resources {
		for _, credential := range resource.Credentials {
			if credential.ID != credentialID || credential.SecretRef == "" {
				continue
			}
			switch credential.Provider {
			case "aws-secrets-manager":
				return s.revealAWSSecret(ctx, project, snapshot.Info.Region, credential.SecretRef)
			case "kubernetes-secret":
				return s.revealKubernetesSecret(ctx, project, environmentName, credential.SecretRef)
			default:
				return nil, errors.New("unsupported credential provider")
			}
		}
	}
	return nil, os.ErrNotExist
}

// RevealKubernetesSecretAt is used only after a trusted environment document
// has supplied the namespace, Secret name and key. It keeps the same strict
// reference validation as the user-facing resource reveal flow.
func (s *Service) RevealKubernetesSecretAt(ctx context.Context, project, targetName, ref string) (map[string]string, error) {
	if !environment.ValidName(targetName) {
		return nil, errors.New("invalid environment target")
	}
	return s.revealKubernetesSecretWithPath(ctx, project, filepath.Join(s.config.Paths.DataDir, "kubeconfigs", targetName), ref)
}

func (s *Service) terraformOutputs(ctx context.Context, project, targetName string) (map[string]any, error) {
	if s.outputProvider != nil {
		if outputs, err := s.outputProvider.StateOutputs(ctx, project, targetName, "infra"); err == nil {
			return outputs, nil
		}
	}
	stage := filepath.Base(s.config.Paths.TerraformInfraDir)
	dataDir := filepath.Join(s.config.Paths.DataDir, "terraform", targetName, stage)
	commandCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(commandCtx, s.config.Tools.Terraform, "output", "-json", "-no-color") // #nosec G204 -- tool path is administrator-owned and arguments are constant.
	cmd.Dir = s.config.Paths.TerraformInfraDir
	commandEnvironment, err := s.projectCommandEnvironment(ctx, project)
	if err != nil {
		return nil, err
	}
	cmd.Env = commandEnvironment
	cmd.Env = append(cmd.Env, "TF_DATA_DIR="+dataDir, "TF_WORKSPACE="+targetName)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("terraform output: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	var raw map[string]struct {
		Value any `json:"value"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &raw); err != nil {
		return nil, err
	}
	result := make(map[string]any, len(raw))
	for key, value := range raw {
		result[key] = value.Value
	}
	return result, nil
}

func (s *Service) build(project, environmentName, targetName string, doc environment.Document, report *statusservice.Report, outputs map[string]any) Snapshot {
	region := stringPath(doc, "region")
	clusterName := environment.ClusterName(doc)
	if value := outputString(outputs, "cluster_name"); value != "" {
		clusterName = value
	}
	info := EnvironmentInfo{
		AWSAccountID: outputString(outputs, "aws_account_id"), Region: region,
		AvailabilityZones: stringSlicePath(doc, "network.availability_zones"),
		VPCID:             outputString(outputs, "vpc_id"), VPCCIDR: stringPath(doc, "network.vpc_cidr"),
		ClusterName: clusterName, ClusterEndpoint: outputString(outputs, "cluster_endpoint"),
		Namespaces:     sortedMapKeys(valuePath(doc, "namespaces")),
		PublicSubnets:  stringMapPath(doc, "network.public_subnets"),
		PrivateSubnets: stringMapPath(doc, "network.private_subnets"),
		NetworkMode:    defaultString(stringPath(doc, "network.workload_subnet_type"), "public"),
		NATGatewayMode: defaultString(outputString(outputs, "nat_gateway_mode"), stringPath(doc, "network.nat_gateway_mode")),
		NATGatewayIPs:  outputStringMap(outputs, "nat_gateway_public_ips"),
	}
	if environment.IsExistingEKS(doc) {
		info.AvailabilityZones = []string{}
		info.VPCCIDR = ""
		info.PublicSubnets = map[string]string{}
		info.PrivateSubnets = map[string]string{}
		info.NetworkMode = "existing"
		info.NATGatewayMode = "external"
		info.NATGatewayIPs = map[string]string{}
	}
	if report != nil && report.Cluster.Endpoint != "" {
		info.ClusterEndpoint = report.Cluster.Endpoint
	}
	snapshot := Snapshot{
		SchemaVersion: cloudConfigurationSchemaVersion,
		Project:       project, Environment: environmentName, ObservedAt: time.Now().UTC(), Info: info,
		Resources: make([]Resource, 0), Warnings: make([]string, 0),
	}
	componentStatus := make(map[string]string)
	if report != nil {
		for _, component := range report.Components {
			componentStatus[component.Key] = component.Status
		}
	}
	eksStatus := ""
	if report != nil && report.Cluster.Reachable {
		eksStatus = "healthy"
	}
	if eksStatus != "" {
		version := stringPath(doc, "eks.kubernetes_version")
		specification := fmt.Sprintf("%d 个节点组", len(mapPath(doc, "eks.node_groups")))
		if report != nil && report.Cluster.Version != "" {
			version = report.Cluster.Version
		}
		if environment.IsExistingEKS(doc) {
			specification = "接入已有 EKS（基础设施不由本平台托管）"
		}
		snapshot.Resources = append(snapshot.Resources, Resource{
			Key: "eks", DisplayName: "Amazon EKS", Category: "容器平台", Source: "cloud", Provider: "AWS",
			Status: eksStatus, Version: version,
			Specification: specification,
			AccessPoints:  compactAccess([]AccessPoint{{Name: "Kubernetes API", Type: "api", Visibility: info.NetworkMode, Protocol: "https", URL: info.ClusterEndpoint}}),
			Credentials:   make([]Credential, 0), Metadata: map[string]any{"cluster_name": clusterName},
		})
	}
	s.appendManagedResources(&snapshot, doc, outputs, componentStatus)
	services := make(map[string]statusservice.KubernetesService)
	if report != nil {
		for _, service := range report.Services {
			services[service.Namespace+"/"+service.Name] = service
		}
	}
	s.appendSelfHostedResources(&snapshot, doc, componentStatus, services)
	s.appendDomainResources(&snapshot, doc, componentStatus, services)
	sort.Slice(snapshot.Resources, func(i, j int) bool {
		if snapshot.Resources[i].Category == snapshot.Resources[j].Category {
			return snapshot.Resources[i].DisplayName < snapshot.Resources[j].DisplayName
		}
		return snapshot.Resources[i].Category < snapshot.Resources[j].Category
	})
	_ = targetName
	return snapshot
}

func (s *Service) appendManagedResources(snapshot *Snapshot, doc environment.Document, outputs map[string]any, statuses map[string]string) {
	definitions := []struct {
		Key, Name, Category, ConfigPath, EndpointKey, ReaderKey, SecretKey string
	}{
		{"rds", "RDS 管理数据库", "中间件与数据库", "data_services.rds", "rds_endpoint", "", "rds_master_secret_arn"},
		{"aurora", "Aurora 游戏数据库", "中间件与数据库", "data_services.aurora", "aurora_writer_endpoint", "aurora_reader_endpoint", "aurora_master_secret_arn"},
		{"postgres", "RDS PostgreSQL", "中间件与数据库", "data_services.postgres", "postgres_endpoint", "", "postgres_master_secret_arn"},
		{"documentdb", "Amazon DocumentDB（MongoDB 兼容）", "中间件与数据库", "data_services.documentdb", "documentdb_endpoint", "documentdb_reader_endpoint", "documentdb_master_secret_arn"},
		{"elasticache", "ElastiCache Redis/Valkey", "中间件与数据库", "data_services.elasticache", "elasticache_configuration_endpoint", "elasticache_reader_endpoint", "elasticache_secret_arn"},
		{"msk", "Amazon MSK Kafka", "中间件与数据库", "data_services.msk", "msk_bootstrap_brokers", "", ""},
		{"amazon_mq", "Amazon MQ RabbitMQ", "中间件与数据库", "data_services.amazon_mq", "amazon_mq_endpoint", "amazon_mq_console_url", "amazon_mq_secret_arn"},
	}
	for _, definition := range definitions {
		config := mapPath(doc, definition.ConfigPath)
		if !boolFrom(config["enabled"]) {
			continue
		}
		host := outputString(outputs, definition.EndpointKey)
		if host == "" {
			continue
		}
		port := intFrom(config["port"])
		protocol := protocolFor(definition.Key, boolFrom(config["tls_enabled"]))
		// These managed services are explicitly created without public access.
		// Placement in a public subnet only selects its route table; it does not
		// make an RDS, DocumentDB, ElastiCache, MSK or Amazon MQ endpoint public.
		visibility := "private"
		primary := AccessPoint{Name: "主连接地址", Type: "connection", Visibility: visibility, Protocol: protocol, Host: host, Port: port}
		// MSK 返回由逗号分隔、且自带端口的 broker 列表；Amazon MQ 返回完整 URI。
		// 保留 AWS 的原始值，避免前端拼出重复的协议或端口。
		if definition.Key == "msk" {
			primary.Port = 0
		}
		if strings.Contains(host, "://") {
			primary.URL, primary.Host, primary.Port = host, "", 0
		}
		points := []AccessPoint{primary}
		if reader := outputString(outputs, definition.ReaderKey); definition.Key != "amazon_mq" && reader != "" {
			points = append(points, AccessPoint{Name: "只读地址", Type: "connection", Visibility: visibility, Protocol: protocol, Host: reader, Port: port})
		}
		if console := outputString(outputs, definition.ReaderKey); definition.Key == "amazon_mq" && console != "" {
			points = append(points, AccessPoint{Name: "管理控制台", Type: "console", Visibility: visibility, Protocol: "https", URL: console})
		}
		credentials := make([]Credential, 0)
		secretRef := outputString(outputs, definition.SecretKey)
		if definition.SecretKey != "" {
			credentials = append(credentials, newCredential(definition.Key, "管理员凭据", stringFrom(config["master_username"]), "aws-secrets-manager", secretRef))
			if secretRef == "" {
				snapshot.Warnings = append(snapshot.Warnings, fmt.Sprintf(
					"%s 已创建并可访问，但 AWS/平台未返回管理员凭据 Secret。若该服务原本使用 AWS 托管密码，请先确认 Secret 是否被删除，再由运维执行密码轮换；平台不会自动更改生产数据库密码。",
					definition.Name,
				))
			}
		}
		status := defaultString(statuses[definition.Key], "healthy")
		snapshot.Resources = append(snapshot.Resources, Resource{
			Key: definition.Key, DisplayName: definition.Name, Category: definition.Category,
			Source: "cloud", Provider: "AWS", Status: status,
			Version: stringFrom(config["engine_version"]), Specification: managedSpecification(definition.Key, config),
			AccessPoints: compactAccess(points), Credentials: credentials,
			Metadata: map[string]any{"engine": config["engine"], "mode": config["mode"], "database": config["database_name"], "credential_available": definition.SecretKey == "" || secretRef != ""},
		})
	}
	if ecr := mapPath(doc, "ecr"); boolFrom(ecr["enabled"]) {
		urls, _ := outputs["ecr_repository_urls"].(map[string]any)
		points := make([]AccessPoint, 0, len(urls))
		for name, value := range urls {
			points = append(points, AccessPoint{Name: name, Type: "registry", Visibility: "public", Protocol: "https", URL: stringFrom(value)})
		}
		if len(points) == 0 {
			return
		}
		snapshot.Resources = append(snapshot.Resources, Resource{
			Key: "ecr", DisplayName: "Amazon ECR", Category: "镜像仓库", Source: "cloud", Provider: "AWS",
			Status: "healthy", Specification: fmt.Sprintf("%d 个仓库", len(sliceFrom(ecr["repositories"]))),
			AccessPoints: points, Credentials: make([]Credential, 0), Metadata: map[string]any{"immutable": true},
		})
	}
}

func (s *Service) appendSelfHostedResources(snapshot *Snapshot, doc environment.Document, statuses map[string]string, services map[string]statusservice.KubernetesService) {
	configs := mapPath(doc, "components.catalog")
	for key, raw := range configs {
		config, ok := raw.(map[string]any)
		if !ok || !boolFrom(config["enabled"]) {
			continue
		}
		status := statuses[key]
		if status != "healthy" && status != "drift" {
			continue
		}
		namespace := defaultString(stringFrom(config["namespace"]), "platform-server")
		serviceName := stringFrom(config["service_name"])
		port := intFrom(config["service_port"])
		protocol := defaultString(stringFrom(config["protocol"]), "http")
		points := make([]AccessPoint, 0, 2)
		if serviceName != "" && port > 0 {
			host := fmt.Sprintf("%s.%s.svc.cluster.local", serviceName, namespace)
			name := "集群内地址"
			if key == "opentelemetry_collector" {
				name = "OTLP gRPC（集群内）"
			}
			points = append(points, AccessPoint{Name: name, Type: "service", Visibility: "private", Protocol: protocol, Host: host, Port: port})
			if key == "opentelemetry_collector" {
				points = append(points, AccessPoint{Name: "OTLP HTTP（集群内）", Type: "service", Visibility: "private", Protocol: "http", Host: host, Port: 4318})
			}
		}
		consoleServiceName := stringFrom(config["console_service_name"])
		consolePort := intFrom(config["console_service_port"])
		if consoleServiceName != "" && consolePort > 0 {
			consoleProtocol := defaultString(stringFrom(config["console_protocol"]), "http")
			points = append(points, AccessPoint{
				Name: "Web 管理页", Type: "console", Visibility: "private", Protocol: consoleProtocol,
				Host: fmt.Sprintf("%s.%s.svc.cluster.local", consoleServiceName, namespace), Port: consolePort,
			})
		}
		if domain := stringFrom(config["domain"]); domain != "" {
			points = append(points, AccessPoint{Name: "域名入口", Type: "console", Visibility: "public", Protocol: ternary(boolFrom(config["tls"]), "https", "http"), URL: protocolURL(domain, boolFrom(config["tls"]))})
		}
		publicServiceName := stringFrom(config["public_service_name"])
		if publicServiceName != "" {
			if service, ok := services[namespace+"/"+publicServiceName]; ok {
				points = append(points, loadBalancerAccessPoints(service, protocol)...)
			}
		}
		credentials := make([]Credential, 0)
		if secretName := stringFrom(config["secret_name"]); secretName != "" {
			secretKey := defaultString(stringFrom(config["secret_key"]), "password")
			secretRef := strings.Join([]string{namespace, secretName, secretKey}, "/")
			credentials = append(credentials, newCredential(key, "管理凭据", stringFrom(config["username"]), "kubernetes-secret", secretRef))
		}
		snapshot.Resources = append(snapshot.Resources, Resource{
			Key: key, DisplayName: defaultString(stringFrom(config["display_name"]), key),
			Category: defaultString(stringFrom(config["category"]), "自建组件"), Source: "self-hosted", Provider: "Kubernetes/Helm",
			Status: status, Version: defaultString(stringFrom(config["app_version"]), stringFrom(config["chart_version"])), Specification: componentSpecification(config), Namespace: namespace,
			AccessPoints: compactAccess(points), Credentials: credentials,
			Metadata: map[string]any{"release_name": defaultString(stringFrom(config["release_name"]), key), "chart": config["chart"]},
		})
		if key == "opentelemetry_collector" {
			values := mapFrom(config["values"])
			elasticsearch := mapFrom(values["elasticsearch"])
			if boolFrom(elasticsearch["enabled"]) {
				esStatus := status
				if services != nil {
					service, exists := services[namespace+"/otel-elasticsearch"]
					if !exists || (service.EndpointHealthKnown && service.ReadyEndpoints == 0) {
						esStatus = "missing"
					}
				}
				image := mapFrom(elasticsearch["image"])
				storage := mapFrom(elasticsearch["storage"])
				storageSize := defaultString(stringFrom(storage["expandedSize"]), stringFrom(storage["initialSize"]))
				mode := defaultString(stringFrom(elasticsearch["mode"]), "standalone")
				nodes := intFrom(elasticsearch["replicas"])
				if mode == "standalone" || nodes < 1 {
					nodes = 1
				}
				snapshot.Resources = append(snapshot.Resources, Resource{
					Key: "otel_elasticsearch", DisplayName: "OpenTelemetry Elasticsearch", Category: "监控", Source: "self-hosted", Provider: "Kubernetes/Helm",
					Status: esStatus, Version: stringFrom(image["tag"]), Specification: fmt.Sprintf("%s · %d 节点 · %s/节点", mode, nodes, storageSize), Namespace: namespace,
					AccessPoints: []AccessPoint{{Name: "Elasticsearch API（集群内）", Type: "service", Visibility: "private", Protocol: "http", Host: fmt.Sprintf("otel-elasticsearch.%s.svc.cluster.local", namespace), Port: 9200}},
					Credentials:  []Credential{newCredential("otel_elasticsearch", "Elasticsearch 管理凭据", "elastic", "kubernetes-secret", strings.Join([]string{namespace, "otel-elasticsearch-access", "password"}, "/"))},
					Metadata:     map[string]any{"release_name": "otel-elasticsearch", "mode": mode, "nodes": nodes, "storage_per_node": storageSize},
				})
			}
		}
	}
	for _, key := range []string{"consul", "etcd"} {
		config := mapPath(doc, "components."+key)
		if !boolFrom(config["enabled"]) {
			continue
		}
		status := statuses[key]
		if status != "healthy" && status != "drift" {
			continue
		}
		namespace := defaultString(stringFrom(config["namespace"]), "platform-server")
		points := make([]AccessPoint, 0, 2)
		credentials := make([]Credential, 0, 1)
		if key == "consul" {
			points = append(points,
				AccessPoint{Name: "Consul API（集群内）", Type: "service", Visibility: "private", Protocol: "http", Host: fmt.Sprintf("consul-http.%s.svc.cluster.local", namespace), Port: 8500},
				AccessPoint{Name: "Consul Web 管理页", Type: "console", Visibility: "private", Protocol: "https", Host: fmt.Sprintf("consul-ui.%s.svc.cluster.local", namespace), Port: 443},
			)
			credentials = append(credentials, newCredential(key, "Consul ACL Token", "", "kubernetes-secret", strings.Join([]string{namespace, "consul-bootstrap-acl-token", "token"}, "/")))
		} else {
			protocol := "https"
			if config["tls_enabled"] != nil && !boolFrom(config["tls_enabled"]) {
				protocol = "http"
			}
			points = append(points, AccessPoint{Name: "etcd API（集群内）", Type: "service", Visibility: "private", Protocol: protocol, Host: fmt.Sprintf("etcd.%s.svc.cluster.local", namespace), Port: 2379})
			if boolFrom(mapFrom(config["web_ui"])["enabled"]) || config["web_ui"] == nil {
				points = append(points, AccessPoint{Name: "etcd Web 管理页", Type: "console", Visibility: "private", Protocol: "http", Host: fmt.Sprintf("etcd-web.%s.svc.cluster.local", namespace), Port: 80})
				username := defaultString(stringFrom(mapFrom(config["web_ui"])["username"]), "admin")
				credentials = append(credentials, newCredential(key, "etcd Web 登录密码", username, "kubernetes-secret", strings.Join([]string{namespace, "etcd-web-auth", "password"}, "/")))
			}
		}
		snapshot.Resources = append(snapshot.Resources, Resource{
			Key: key, DisplayName: strings.ToUpper(key), Category: "配置与注册中心", Source: "self-hosted", Provider: "Kubernetes/Helm",
			Status: status, Version: stringFrom(config["image"]), Namespace: namespace,
			AccessPoints: points, Credentials: credentials, Metadata: map[string]any{"replicas": config["replicas"]},
		})
	}
}

func componentSpecification(config map[string]any) string {
	mode := defaultString(stringFrom(config["deployment_mode"]), "standalone")
	if mode == "cluster" {
		if len(sliceFrom(config["replica_paths"])) == 0 {
			return "Helm · 集群工作负载模式 · 控制面单副本"
		}
		return fmt.Sprintf("Helm · 集群模式 · %d 副本", intFrom(config["replicas"]))
	}
	return "Helm · 单机模式"
}

func loadBalancerAccessPoints(service statusservice.KubernetesService, fallbackProtocol string) []AccessPoint {
	if !strings.EqualFold(service.Type, "LoadBalancer") || len(service.LoadBalancerHosts) == 0 {
		return nil
	}
	points := make([]AccessPoint, 0, len(service.LoadBalancerHosts)*len(service.Ports))
	for _, host := range service.LoadBalancerHosts {
		for _, port := range service.Ports {
			protocol := fallbackProtocol
			if port.Port == 443 || strings.Contains(strings.ToLower(port.Name), "https") || strings.Contains(strings.ToLower(port.Name), "tls") {
				protocol = "https"
			} else if port.Port == 80 || strings.Contains(strings.ToLower(port.Name), "http") {
				protocol = "http"
			}
			points = append(points, AccessPoint{
				Name: "公网负载均衡入口", Type: "load-balancer", Visibility: "public",
				Protocol: protocol, Host: host, Port: port.Port,
			})
		}
	}
	return points
}

func (s *Service) appendDomainResources(snapshot *Snapshot, doc environment.Document, statuses map[string]string, services map[string]statusservice.KubernetesService) {
	for index, raw := range sliceFrom(valuePath(doc, "domains")) {
		config, ok := raw.(map[string]any)
		if !ok || !boolFromDefault(config["enabled"], true) {
			continue
		}
		domain := stringFrom(config["domain"])
		accessType := defaultString(stringFrom(config["access_type"]), "domain")
		protocol := strings.ToLower(defaultString(stringFrom(config["protocol"]), ternary(boolFrom(config["tls_enabled"]), "https", "http")))
		if protocol == "tcp" {
			namespace := defaultString(stringFrom(config["namespace"]), "platform-server")
			serviceName := stringFrom(config["name"])
			if serviceName == "" {
				serviceName = fmt.Sprintf("tcp-%s-%d", stringFrom(config["service"]), index)
				if len(serviceName) > 63 {
					serviceName = serviceName[:63]
				}
			}
			service, deployed := services[namespace+"/"+serviceName]
			if !deployed {
				continue
			}
			scheme := defaultString(stringFrom(config["tcp_scheme"]), "internet-facing")
			visibility := ternary(scheme == "internal", "private", "public")
			points := loadBalancerAccessPoints(service, "tcp")
			for pointIndex := range points {
				points[pointIndex].Name = ternary(scheme == "internal", "VPC 内网 NLB 入口", "公网 TCP NLB 入口")
				points[pointIndex].Visibility = visibility
				points[pointIndex].Protocol = "tcp"
			}
			externalPort := intFrom(config["external_port"])
			if externalPort == 0 {
				externalPort = intFrom(config["service_port"])
			}
			if accessType == "domain" && domain != "" {
				points = append([]AccessPoint{{Name: "TCP 域名入口", Type: "domain", Visibility: visibility, Protocol: "tcp", Host: domain, Port: externalPort}}, points...)
			}
			status := "pending"
			if len(service.LoadBalancerHosts) > 0 {
				status = "healthy"
			}
			displayName := domain
			if displayName == "" {
				displayName = fmt.Sprintf("%s/%s:%d", namespace, stringFrom(config["service"]), externalPort)
			}
			snapshot.Resources = append(snapshot.Resources, Resource{
				Key: fmt.Sprintf("domain-%d", index), DisplayName: displayName, Category: "TCP 访问与负载均衡", Source: "self-hosted", Provider: "AWS NLB",
				Status: status, Namespace: namespace, AccessPoints: compactAccess(points), Credentials: make([]Credential, 0),
				Metadata: map[string]any{"protocol": "tcp", "service": config["service"], "service_port": config["service_port"], "external_port": externalPort, "tcp_scheme": scheme, "allowed_cidrs": config["allowed_cidrs"]},
			})
			continue
		}
		gateway := defaultString(stringFrom(config["gateway"]), "higress")
		statusKey := gateway
		if gateway == "nginx" {
			statusKey = "nginx_ingress"
		}
		gatewayStatus := statuses[statusKey]
		if gatewayStatus != "healthy" && gatewayStatus != "drift" {
			continue
		}
		tls := boolFrom(config["tls_enabled"])
		displayName := domain
		points := make([]AccessPoint, 0, 2)
		if accessType == "ip" {
			displayName = "网关公网 IP / LoadBalancer 默认路由"
			catalogKey := gateway
			if gateway == "nginx" {
				catalogKey = "nginx_ingress"
			}
			gatewayConfig := mapPath(doc, "components.catalog."+catalogKey)
			namespace := defaultString(stringFrom(gatewayConfig["namespace"]), "platform-server")
			if service, ok := services[namespace+"/"+stringFrom(gatewayConfig["public_service_name"])]; ok {
				points = append(points, loadBalancerAccessPoints(service, "http")...)
			}
		} else if domain != "" {
			points = append(points, AccessPoint{Name: "域名访问", Type: "domain", Visibility: "public", Protocol: ternary(tls, "https", "http"), URL: protocolURL(domain, tls)})
		}
		snapshot.Resources = append(snapshot.Resources, Resource{
			Key: fmt.Sprintf("domain-%d", index), DisplayName: displayName, Category: "网关、域名与 TLS", Source: "self-hosted", Provider: gateway,
			Status: gatewayStatus, AccessPoints: compactAccess(points),
			Credentials: make([]Credential, 0), Metadata: map[string]any{
				"gateway": gateway, "service": config["service"], "service_port": config["service_port"], "routes": config["routes"],
				"access_type": accessType, "protocol": protocol, "tls_enabled": tls, "certificate_ref": config["certificate_ref"],
			},
		})
	}
}

func (s *Service) revealAWSSecret(ctx context.Context, project, region, ref string) (map[string]string, error) {
	commandCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	args := []string{"secretsmanager", "get-secret-value", "--region", region, "--secret-id", ref, "--query", "SecretString", "--output", "text"}
	cmd := exec.CommandContext(commandCtx, s.config.Tools.AWS, args...) // #nosec G204 -- exec does not invoke a shell; region and secret reference are validated/scoped arguments.
	commandEnvironment, err := s.projectCommandEnvironment(ctx, project)
	if err != nil {
		return nil, err
	}
	cmd.Env = commandEnvironment
	payload, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("read AWS secret: %w", err)
	}
	return parseSecretPayload(payload), nil
}

func removeEnvironmentKeys(source []string, keys ...string) []string {
	blocked := make(map[string]bool, len(keys))
	for _, key := range keys {
		blocked[key] = true
	}
	result := make([]string, 0, len(source))
	for _, item := range source {
		key, _, _ := strings.Cut(item, "=")
		if !blocked[key] {
			result = append(result, item)
		}
	}
	return result
}

func (s *Service) projectCommandEnvironment(ctx context.Context, project string) ([]string, error) {
	if s.awsProvider == nil {
		return nil, errors.New("当前项目未绑定 AWS 凭据，平台已拒绝访问资源")
	}
	awsEnvironment, err := s.awsProvider.Environment(ctx, project)
	if err != nil {
		return nil, fmt.Errorf("当前项目未绑定可用的 AWS 凭据，平台不会使用其他项目或默认凭据链: %w", err)
	}
	if len(awsEnvironment) == 0 {
		return nil, errors.New("当前项目未绑定可用的 AWS 凭据，平台不会使用其他项目或默认凭据链")
	}
	environment := removeEnvironmentKeys(os.Environ(), "AWS_ACCESS_KEY_ID", "AWS_SECRET_ACCESS_KEY", "AWS_SESSION_TOKEN", "AWS_PROFILE", "AWS_DEFAULT_PROFILE")
	return append(environment, awsEnvironment...), nil
}

func (s *Service) revealKubernetesSecret(ctx context.Context, project, environmentName, ref string) (map[string]string, error) {
	return s.revealKubernetesSecretWithPath(ctx, project, filepath.Join(s.config.Paths.DataDir, "kubeconfigs", project+"-"+environmentName), ref)
}

func (s *Service) revealKubernetesSecretWithPath(ctx context.Context, project, kubeconfig, ref string) (map[string]string, error) {
	parts := strings.Split(ref, "/")
	if len(parts) != 3 || len(parts[0]) > 63 || len(parts[1]) > 253 ||
		!kubernetesNamePattern.MatchString(parts[0]) || !kubernetesNamePattern.MatchString(parts[1]) ||
		!kubernetesKeyPattern.MatchString(parts[2]) {
		return nil, errors.New("invalid Kubernetes secret reference")
	}
	if err := os.Chmod(kubeconfig, 0o600); err != nil {
		return nil, fmt.Errorf("secure kubeconfig: %w", err)
	}
	commandCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(commandCtx, s.config.Tools.Kubectl, "--kubeconfig", kubeconfig, "get", "secret", parts[1], "-n", parts[0], "-o", "json") // #nosec G204 -- Kubernetes identifiers are allowlist-validated and no shell is used.
	commandEnvironment, err := s.projectCommandEnvironment(ctx, project)
	if err != nil {
		return nil, err
	}
	cmd.Env = commandEnvironment
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err = cmd.Run()
	if err != nil {
		if kubernetesResourceNotFound(stderr.String()) {
			return nil, os.ErrNotExist
		}
		return nil, fmt.Errorf("read Kubernetes secret: %w", err)
	}
	payload := stdout.Bytes()
	var object struct {
		Data map[string]string `json:"data"`
	}
	if err := json.Unmarshal(payload, &object); err != nil {
		return nil, err
	}
	result := make(map[string]string)
	keys := []string{parts[2], "username", "admin-user", "password", "admin-password", "token"}
	for _, key := range keys {
		encoded, ok := object.Data[key]
		if !ok {
			continue
		}
		decoded, err := base64.StdEncoding.DecodeString(encoded)
		if err == nil {
			result[key] = string(decoded)
		}
	}
	if len(result) == 0 {
		return nil, os.ErrNotExist
	}
	return result, nil
}

func kubernetesResourceNotFound(stderr string) bool {
	value := strings.ToLower(strings.TrimSpace(stderr))
	return strings.Contains(value, "notfound") || strings.Contains(value, "not found") || strings.Contains(value, "could not find")
}

func parseSecretPayload(payload []byte) map[string]string {
	var raw map[string]any
	if json.Unmarshal(bytes.TrimSpace(payload), &raw) == nil {
		result := make(map[string]string, len(raw))
		for key, value := range raw {
			result[key] = fmt.Sprint(value)
		}
		return result
	}
	return map[string]string{"value": strings.TrimSpace(string(payload))}
}

func newCredential(resourceKey, label, username, provider, ref string) Credential {
	digest := sha256.Sum256([]byte(resourceKey + "\x00" + provider + "\x00" + ref))
	return Credential{ID: hex.EncodeToString(digest[:12]), Label: label, Username: username, Provider: provider, SecretRef: ref, Available: ref != ""}
}

func managedSpecification(key string, config map[string]any) string {
	switch key {
	case "rds", "postgres":
		return defaultString(stringFrom(config["instance_class"]), "RDS")
	case "documentdb":
		return fmt.Sprintf("%s · %v 节点", defaultString(stringFrom(config["instance_class"]), "DocumentDB"), config["instance_count"])
	case "aurora":
		return fmt.Sprintf("%s · %v-%v ACU", defaultString(stringFrom(config["mode"]), "serverless-v2"), config["min_acu"], config["max_acu"])
	case "elasticache":
		mode := defaultString(stringFrom(config["mode"]), "cluster")
		if mode == "serverless" {
			return mode
		}
		shards := intFrom(config["num_node_groups"])
		nodesPerShard := intFrom(config["nodes_per_shard"])
		if nodesPerShard < 1 {
			nodesPerShard = intFrom(config["replicas_per_node_group"]) + 1
		}
		return fmt.Sprintf("%s · %s · %d 分片 × %d 节点 = %d 总节点", mode, stringFrom(config["node_type"]), shards, nodesPerShard, shards*nodesPerShard)
	case "msk":
		return defaultString(stringFrom(config["mode"]), "serverless")
	case "amazon_mq":
		return fmt.Sprintf("%s · %s", defaultString(stringFrom(config["deployment_mode"]), "SINGLE_INSTANCE"), stringFrom(config["host_instance_type"]))
	default:
		return ""
	}
}

func protocolFor(key string, tls bool) string {
	switch key {
	case "rds", "aurora":
		return "mysql"
	case "postgres":
		return "postgresql"
	case "documentdb":
		return "mongodb+tls"
	case "elasticache":
		return ternary(tls, "rediss", "redis")
	case "msk":
		return "kafka+iam"
	case "amazon_mq":
		return "amqps"
	default:
		return "tcp"
	}
}

func compactAccess(items []AccessPoint) []AccessPoint {
	result := make([]AccessPoint, 0, len(items))
	for _, item := range items {
		if item.Host != "" || item.URL != "" {
			result = append(result, item)
		}
	}
	return result
}

func protocolURL(domain string, tls bool) string {
	if strings.Contains(domain, "://") {
		return domain
	}
	return ternary(tls, "https://", "http://") + domain
}

func valuePath(doc environment.Document, path string) any {
	value, _ := environment.GetPath(doc, path)
	return value
}

func stringPath(doc environment.Document, path string) string {
	return stringFrom(valuePath(doc, path))
}

func mapPath(doc environment.Document, path string) map[string]any {
	value, _ := valuePath(doc, path).(map[string]any)
	if value == nil {
		return map[string]any{}
	}
	return value
}

func stringSlicePath(doc environment.Document, path string) []string {
	values := sliceFrom(valuePath(doc, path))
	result := make([]string, 0, len(values))
	for _, value := range values {
		result = append(result, stringFrom(value))
	}
	return result
}

func stringMapPath(doc environment.Document, path string) map[string]string {
	values := mapPath(doc, path)
	result := make(map[string]string, len(values))
	for key, value := range values {
		result[key] = stringFrom(value)
	}
	return result
}

func sortedMapKeys(value any) []string {
	items, _ := value.(map[string]any)
	result := make([]string, 0, len(items))
	for key := range items {
		result = append(result, key)
	}
	sort.Strings(result)
	return result
}

func outputString(outputs map[string]any, key string) string {
	if key == "" {
		return ""
	}
	return stringFrom(outputs[key])
}

func outputStringMap(outputs map[string]any, key string) map[string]string {
	values, _ := outputs[key].(map[string]any)
	result := make(map[string]string, len(values))
	for itemKey, value := range values {
		result[itemKey] = stringFrom(value)
	}
	return result
}

func stringFrom(value any) string {
	switch typed := value.(type) {
	case string:
		return strings.TrimSpace(typed)
	case json.Number:
		return typed.String()
	case nil:
		return ""
	default:
		return strings.TrimSpace(fmt.Sprint(value))
	}
}

func intFrom(value any) int {
	switch typed := value.(type) {
	case int:
		return typed
	case float64:
		return int(typed)
	case json.Number:
		result, _ := strconv.Atoi(typed.String())
		return result
	default:
		result, _ := strconv.Atoi(stringFrom(value))
		return result
	}
}

func boolFrom(value any) bool { result, _ := value.(bool); return result }
func boolFromDefault(value any, fallback bool) bool {
	if value == nil {
		return fallback
	}
	return boolFrom(value)
}
func sliceFrom(value any) []any { result, _ := value.([]any); return result }
func mapFrom(value any) map[string]any {
	result, _ := value.(map[string]any)
	if result == nil {
		return map[string]any{}
	}
	return result
}
func defaultString(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}
func ternary[T any](condition bool, yes, no T) T {
	if condition {
		return yes
	}
	return no
}

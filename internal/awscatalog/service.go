package awscatalog

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"ops-deploy-platform/internal/appconfig"
	"ops-deploy-platform/internal/awscredentials"
)

var (
	ErrInvalidRegion         = errors.New("invalid AWS region")
	ErrInvalidQuery          = errors.New("invalid EC2 instance type query")
	ErrCredentialUnavailable = errors.New("AWS credential is unavailable")
	ErrCredentialRejected    = errors.New("AWS credential was rejected")
	ErrAccessDenied          = errors.New("AWS access was denied")
	ErrUnsupportedCLI        = errors.New("AWS CLI does not support the requested operation")
	ErrNetworkUnavailable    = errors.New("AWS endpoint is unavailable")
	ErrQueryTimedOut         = errors.New("AWS catalog query timed out")
	ErrQueryFailed           = errors.New("AWS catalog query failed")
	ErrEngineVersionMissing  = errors.New("AWS engine version is unavailable in the selected region")
	ErrInvalidECRRepository  = errors.New("invalid ECR repository")
	regionPattern            = regexp.MustCompile(`^[a-z]{2}(?:-gov)?-[a-z]+-\d$`)
	queryPattern             = regexp.MustCompile(`^[a-z0-9][a-z0-9.-]{0,31}$`)
	ecrRepositoryPattern     = regexp.MustCompile(`^(?:[a-z0-9]+(?:[._-][a-z0-9]+)*/)*[a-z0-9]+(?:[._-][a-z0-9]+)*$`)
	ecrRegistryPattern       = regexp.MustCompile(`^([0-9]{12})\.dkr\.ecr\.([a-z0-9-]+)\.amazonaws\.com(?:\.cn)?$`)
	vpcIDPattern             = regexp.MustCompile(`^vpc-[0-9a-f]{8,17}$`)
)

type CredentialProvider interface {
	Environment(context.Context, string) ([]string, error)
}

type Service struct {
	tool     string
	provider CredentialProvider
	slots    chan struct{}
}

type EKSVersion struct {
	Version                  string `json:"version"`
	PatchVersion             string `json:"patch_version,omitempty"`
	Default                  bool   `json:"default"`
	Status                   string `json:"status"`
	DefaultPlatformVersion   string `json:"default_platform_version,omitempty"`
	ReleaseDate              string `json:"release_date,omitempty"`
	EndOfStandardSupportDate string `json:"end_of_standard_support_date,omitempty"`
	EndOfExtendedSupportDate string `json:"end_of_extended_support_date,omitempty"`
}

type EKSCluster struct {
	Name string `json:"name"`
}

type VPCSubnet struct {
	ID                  string `json:"id"`
	Name                string `json:"name,omitempty"`
	CIDR                string `json:"cidr"`
	AvailabilityZone    string `json:"availability_zone"`
	AvailableIPCount    int    `json:"available_ip_count"`
	MapPublicIPOnLaunch bool   `json:"map_public_ip_on_launch"`
}

type VPC struct {
	ID      string      `json:"id"`
	Name    string      `json:"name,omitempty"`
	CIDR    string      `json:"cidr"`
	Default bool        `json:"default"`
	State   string      `json:"state"`
	Subnets []VPCSubnet `json:"subnets"`
}

type SecurityGroup struct {
	ID                   string `json:"id"`
	Name                 string `json:"name"`
	DisplayName          string `json:"display_name,omitempty"`
	VPCID                string `json:"vpc_id"`
	Description          string `json:"description,omitempty"`
	IngressSourceCount   int    `json:"ingress_source_count"`
	AllowsHTTP           bool   `json:"allows_http"`
	AllowsHTTPS          bool   `json:"allows_https"`
	PublicHTTP           bool   `json:"public_http"`
	PublicHTTPS          bool   `json:"public_https"`
	Selectable           bool   `json:"selectable"`
	BlockedReason        string `json:"blocked_reason,omitempty"`
	PlatformManagedGuard bool   `json:"platform_managed_guard"`
}

type InstanceType struct {
	Name                     string   `json:"name"`
	CurrentGeneration        bool     `json:"current_generation"`
	VCpu                     int      `json:"vcpu"`
	MemoryMiB                int      `json:"memory_mib"`
	Architectures            []string `json:"architectures"`
	NetworkPerformance       string   `json:"network_performance"`
	MaximumNetworkInterfaces int      `json:"maximum_network_interfaces"`
	EBSOptimizedSupport      string   `json:"ebs_optimized_support"`
	InstanceStorageSupported bool     `json:"instance_storage_supported"`
	Burstable                bool     `json:"burstable"`
	UsageClasses             []string `json:"usage_classes"`
}

type ServiceInstanceOption struct {
	Value             string   `json:"value"`
	EngineVersions    []string `json:"engine_versions,omitempty"`
	DeploymentModes   []string `json:"deployment_modes,omitempty"`
	AvailabilityZones []string `json:"availability_zones,omitempty"`
	MultiAZCapable    bool     `json:"multi_az_capable,omitempty"`
	StorageTypes      []string `json:"storage_types,omitempty"`
}

type ECRRepository struct {
	Name    string `json:"name"`
	URI     string `json:"uri"`
	Region  string `json:"region"`
	Created bool   `json:"created"`
}

func New(config *appconfig.Config, provider CredentialProvider) *Service {
	return &Service{tool: config.Tools.AWS, provider: provider, slots: make(chan struct{}, 4)}
}

func (s *Service) EKSVersions(ctx context.Context, project, region string) ([]EKSVersion, error) {
	region = strings.TrimSpace(region)
	if !regionPattern.MatchString(region) {
		return nil, ErrInvalidRegion
	}
	payload, err := s.run(ctx, project,
		"eks", "describe-cluster-versions", "--region", region,
		"--output", "json", "--no-cli-pager",
	)
	if err != nil {
		return nil, err
	}
	return parseEKSVersions(payload)
}

func (s *Service) EKSClusters(ctx context.Context, project, region string) ([]EKSCluster, error) {
	region = strings.TrimSpace(region)
	if !regionPattern.MatchString(region) {
		return nil, ErrInvalidRegion
	}
	payload, err := s.run(ctx, project,
		"eks", "list-clusters", "--region", region, "--max-items", "100",
		"--output", "json", "--no-cli-pager",
	)
	if err != nil {
		return nil, err
	}
	return parseEKSClusters(payload)
}

func (s *Service) VPCs(ctx context.Context, project, region string) ([]VPC, error) {
	region = strings.TrimSpace(region)
	if !regionPattern.MatchString(region) {
		return nil, ErrInvalidRegion
	}
	vpcPayload, err := s.run(ctx, project,
		"ec2", "describe-vpcs", "--region", region, "--output", "json", "--no-cli-pager",
	)
	if err != nil {
		return nil, err
	}
	subnetPayload, err := s.run(ctx, project,
		"ec2", "describe-subnets", "--region", region, "--output", "json", "--no-cli-pager",
	)
	if err != nil {
		return nil, err
	}
	return parseVPCs(vpcPayload, subnetPayload)
}

func (s *Service) SecurityGroups(ctx context.Context, project, region, vpcID string) ([]SecurityGroup, error) {
	region = strings.TrimSpace(region)
	if !regionPattern.MatchString(region) {
		return nil, ErrInvalidRegion
	}
	vpcID = strings.ToLower(strings.TrimSpace(vpcID))
	if vpcID != "" && !vpcIDPattern.MatchString(vpcID) {
		return nil, ErrInvalidQuery
	}
	args := []string{"ec2", "describe-security-groups", "--region", region, "--output", "json", "--no-cli-pager"}
	if vpcID != "" {
		args = append(args, "--filters", "Name=vpc-id,Values="+vpcID)
	}
	payload, err := s.run(ctx, project, args...)
	if err != nil {
		return nil, err
	}
	return parseSecurityGroups(payload)
}

func (s *Service) InstanceTypes(ctx context.Context, project, region, query string) ([]InstanceType, error) {
	region = strings.TrimSpace(region)
	if !regionPattern.MatchString(region) {
		return nil, ErrInvalidRegion
	}
	query = strings.ToLower(strings.TrimSpace(query))
	if !queryPattern.MatchString(query) {
		return nil, ErrInvalidQuery
	}
	payload, err := s.run(ctx, project,
		"ec2", "describe-instance-types", "--region", region,
		"--filters", "Name=instance-type,Values="+query+"*",
		"--max-items", "100", "--output", "json", "--no-cli-pager",
	)
	if err != nil {
		return nil, err
	}
	return parseInstanceTypes(payload)
}

func (s *Service) ServiceInstanceTypes(ctx context.Context, project, region, service, engineVersion string) ([]ServiceInstanceOption, error) {
	region = strings.TrimSpace(region)
	if !regionPattern.MatchString(region) {
		return nil, ErrInvalidRegion
	}
	service = strings.ToLower(strings.TrimSpace(service))
	engineVersion = strings.TrimSpace(engineVersion)
	if engineVersion != "" && !queryPattern.MatchString(strings.ToLower(engineVersion)) {
		return nil, ErrInvalidQuery
	}
	var args []string
	switch service {
	case "rds-mysql":
		args = []string{"rds", "describe-orderable-db-instance-options", "--engine", "mysql"}
	case "rds-postgres":
		args = []string{"rds", "describe-orderable-db-instance-options", "--engine", "postgres"}
	case "documentdb":
		args = []string{"docdb", "describe-orderable-db-instance-options", "--engine", "docdb"}
	case "amazon-mq":
		args = []string{"mq", "describe-broker-instance-options", "--engine-type", "RABBITMQ", "--region", region, "--max-results", "100", "--output", "json", "--no-cli-pager"}
	case "elasticache":
		args = pricingArguments("AmazonElastiCache", region)
	case "msk":
		args = pricingArguments("AmazonMSK", region)
	default:
		return nil, ErrInvalidQuery
	}
	if engineVersion != "" && service != "amazon-mq" && service != "elasticache" && service != "msk" {
		resolvedVersion, err := s.resolveEngineVersion(ctx, project, region, service, engineVersion)
		if err != nil {
			return nil, err
		}
		args = append(args, "--engine-version", resolvedVersion)
	}
	if service == "rds-mysql" || service == "rds-postgres" || service == "documentdb" {
		args = append(args, "--region", region, "--max-items", "1000", "--output", "json", "--no-cli-pager")
	}
	payload, err := s.run(ctx, project, args...)
	if err != nil {
		return nil, err
	}
	switch service {
	case "amazon-mq":
		return parseMQInstanceOptions(payload)
	case "elasticache":
		return parsePricingInstanceOptions(payload, "cache.")
	case "msk":
		return parsePricingInstanceOptions(payload, "kafka.")
	default:
		return parseOrderableDBInstanceOptions(payload)
	}
}

func (s *Service) EngineVersions(ctx context.Context, project, region, service, engine string) ([]string, error) {
	region = strings.TrimSpace(region)
	if !regionPattern.MatchString(region) {
		return nil, ErrInvalidRegion
	}
	service = strings.ToLower(strings.TrimSpace(service))
	engine = strings.ToLower(strings.TrimSpace(engine))
	if engine != "" && !queryPattern.MatchString(engine) {
		return nil, ErrInvalidQuery
	}
	var args []string
	switch service {
	case "rds-mysql":
		args = []string{"rds", "describe-db-engine-versions", "--engine", "mysql"}
	case "rds-postgres":
		args = []string{"rds", "describe-db-engine-versions", "--engine", "postgres"}
	case "aurora-mysql":
		args = []string{"rds", "describe-db-engine-versions", "--engine", "aurora-mysql"}
	case "documentdb":
		args = []string{"docdb", "describe-db-engine-versions", "--engine", "docdb"}
	case "elasticache":
		if engine != "redis" && engine != "valkey" {
			return nil, ErrInvalidQuery
		}
		args = []string{"elasticache", "describe-cache-engine-versions", "--engine", engine}
	case "msk":
		args = []string{"kafka", "list-kafka-versions"}
	case "amazon-mq":
		args = []string{"mq", "describe-broker-instance-options", "--engine-type", "RABBITMQ"}
	default:
		return nil, ErrInvalidQuery
	}
	args = append(args, "--region", region, "--output", "json", "--no-cli-pager")
	payload, err := s.run(ctx, project, args...)
	if err != nil {
		return nil, err
	}
	return parseEngineVersions(payload, service)
}

func (s *Service) EnsureECRRepository(ctx context.Context, project, region, value string) (ECRRepository, error) {
	region = strings.TrimSpace(region)
	if !regionPattern.MatchString(region) {
		return ECRRepository{}, ErrInvalidRegion
	}
	name, expectedRegistry, err := normalizeECRRepository(value, region)
	if err != nil {
		return ECRRepository{}, err
	}
	payload, stderr, err := s.runDetailed(ctx, project,
		"ecr", "describe-repositories", "--region", region, "--repository-names", name,
		"--output", "json", "--no-cli-pager",
	)
	created := false
	if err != nil {
		if !strings.Contains(strings.ToLower(stderr), "repositorynotfoundexception") {
			return ECRRepository{}, err
		}
		payload, _, err = s.runDetailed(ctx, project,
			"ecr", "create-repository", "--region", region, "--repository-name", name,
			"--image-tag-mutability", "IMMUTABLE",
			"--image-scanning-configuration", "scanOnPush=true",
			"--encryption-configuration", "encryptionType=AES256",
			"--tags", "Key=ManagedBy,Value=OpsDeployPlatform", "Key=Project,Value="+strings.ToLower(strings.TrimSpace(project)),
			"--output", "json", "--no-cli-pager",
		)
		if err != nil {
			return ECRRepository{}, err
		}
		created = true
	}
	repository, err := parseECRRepository(payload, created)
	if err != nil {
		return ECRRepository{}, err
	}
	repository.Region = region
	if expectedRegistry != "" {
		parts := strings.SplitN(repository.URI, "/", 2)
		if len(parts) != 2 || !strings.EqualFold(parts[0], expectedRegistry) {
			return ECRRepository{}, fmt.Errorf("%w: 填写的 ECR Registry 不属于当前项目绑定的 AWS 账号或 Region", ErrInvalidECRRepository)
		}
	}
	return repository, nil
}

func (s *Service) resolveEngineVersion(ctx context.Context, project, region, service, requested string) (string, error) {
	tool, engine := "rds", ""
	switch service {
	case "rds-mysql":
		engine = "mysql"
	case "rds-postgres":
		engine = "postgres"
	case "documentdb":
		tool, engine = "docdb", "docdb"
	default:
		return "", ErrInvalidQuery
	}
	payload, err := s.run(ctx, project, tool, "describe-db-engine-versions", "--engine", engine, "--region", region, "--output", "json", "--no-cli-pager")
	if err != nil {
		return "", err
	}
	return selectLatestEngineVersion(payload, requested)
}

func pricingArguments(serviceCode, region string) []string {
	return []string{
		"pricing", "get-products", "--service-code", serviceCode,
		"--filters", "Type=TERM_MATCH,Field=regionCode,Value=" + region,
		"--region", "us-east-1", "--max-results", "100", "--output", "json", "--no-cli-pager",
	}
}

func (s *Service) run(ctx context.Context, project string, args ...string) ([]byte, error) {
	stdout, _, err := s.runDetailed(ctx, project, args...)
	return stdout, err
}

func (s *Service) runDetailed(ctx context.Context, project string, args ...string) ([]byte, string, error) {
	if s.provider == nil {
		return nil, "", ErrCredentialUnavailable
	}
	projectEnvironment, err := s.provider.Environment(ctx, project)
	if errors.Is(err, awscredentials.ErrCredentialNotBound) || errors.Is(err, awscredentials.ErrCredentialMismatch) {
		return nil, "", ErrCredentialUnavailable
	}
	if err != nil {
		return nil, "", fmt.Errorf("load project AWS credential: %w", err)
	}
	if len(projectEnvironment) == 0 {
		return nil, "", ErrCredentialUnavailable
	}
	if s.slots != nil {
		select {
		case s.slots <- struct{}{}:
			defer func() { <-s.slots }()
		case <-ctx.Done():
			return nil, "", classifyCommandError(ctx.Err(), "")
		}
	}
	commandCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(commandCtx, s.tool, args...) // #nosec G204 -- AWS tool is administrator-configured and all user-derived arguments are allowlist-validated.
	environment := removeEnvironmentKeys(os.Environ(), "AWS_ACCESS_KEY_ID", "AWS_SECRET_ACCESS_KEY", "AWS_SESSION_TOKEN", "AWS_PROFILE", "AWS_DEFAULT_PROFILE")
	environment = append(environment, projectEnvironment...)
	cmd.Env = environment
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		return nil, stderr.String(), classifyCommandError(commandCtx.Err(), stderr.String())
	}
	return stdout.Bytes(), stderr.String(), nil
}

func normalizeECRRepository(value, region string) (name, registry string, err error) {
	value = strings.TrimSpace(strings.TrimSuffix(value, "/"))
	if value == "" || len(value) > 512 || strings.ContainsAny(value, "?#@") {
		return "", "", ErrInvalidECRRepository
	}
	if strings.Contains(value, "/") {
		parts := strings.SplitN(value, "/", 2)
		if matches := ecrRegistryPattern.FindStringSubmatch(strings.ToLower(parts[0])); len(matches) == 3 {
			if matches[2] != region {
				return "", "", fmt.Errorf("%w: ECR 地址 Region 与当前环境不一致", ErrInvalidECRRepository)
			}
			registry, value = parts[0], parts[1]
		} else if strings.Contains(parts[0], ".") || strings.Contains(parts[0], ":") {
			return "", "", fmt.Errorf("%w: 只允许当前项目 AWS 账号的 ECR 地址或仓库名称", ErrInvalidECRRepository)
		}
	}
	if len(value) < 2 || len(value) > 256 || !ecrRepositoryPattern.MatchString(value) {
		return "", "", ErrInvalidECRRepository
	}
	return value, registry, nil
}

func parseECRRepository(payload []byte, created bool) (ECRRepository, error) {
	var response struct {
		Repositories []struct {
			Name string `json:"repositoryName"`
			URI  string `json:"repositoryUri"`
		} `json:"repositories"`
		Repository struct {
			Name string `json:"repositoryName"`
			URI  string `json:"repositoryUri"`
		} `json:"repository"`
	}
	if err := json.Unmarshal(payload, &response); err != nil {
		return ECRRepository{}, fmt.Errorf("parse AWS ECR repository: %w", err)
	}
	name, uri := response.Repository.Name, response.Repository.URI
	if len(response.Repositories) > 0 {
		name, uri = response.Repositories[0].Name, response.Repositories[0].URI
	}
	if strings.TrimSpace(name) == "" || strings.TrimSpace(uri) == "" {
		return ECRRepository{}, errors.New("AWS ECR 返回的仓库信息不完整")
	}
	return ECRRepository{Name: name, URI: uri, Created: created}, nil
}

func classifyCommandError(contextErr error, stderr string) error {
	if errors.Is(contextErr, context.DeadlineExceeded) {
		return ErrQueryTimedOut
	}
	message := strings.ToLower(stderr)
	switch {
	case strings.Contains(message, "unable to locate credentials"),
		strings.Contains(message, "could not load credentials"),
		strings.Contains(message, "no credentials"),
		strings.Contains(message, "config profile") && strings.Contains(message, "could not be found"):
		return ErrCredentialUnavailable
	case strings.Contains(message, "expiredtoken"),
		strings.Contains(message, "invalidclienttokenid"),
		strings.Contains(message, "unrecognizedclient"),
		strings.Contains(message, "signaturedoesnotmatch"),
		strings.Contains(message, "security token included in the request is invalid"),
		strings.Contains(message, "session has expired"),
		strings.Contains(message, "reauthenticate using 'aws login'"),
		strings.Contains(message, "sso session") && strings.Contains(message, "expired"),
		strings.Contains(message, "token has expired and refresh failed"):
		return ErrCredentialRejected
	case strings.Contains(message, "accessdenied"),
		strings.Contains(message, "unauthorizedoperation"),
		strings.Contains(message, "not authorized to perform"):
		return ErrAccessDenied
	case strings.Contains(message, "invalid choice") && strings.Contains(message, "describe-cluster-versions"),
		strings.Contains(message, "unknown operation"):
		return ErrUnsupportedCLI
	case strings.Contains(message, "could not connect to the endpoint url"),
		strings.Contains(message, "endpointconnectionerror"),
		strings.Contains(message, "proxy connection"),
		strings.Contains(message, "connection timed out"),
		strings.Contains(message, "ssl validation failed"):
		return ErrNetworkUnavailable
	default:
		return ErrQueryFailed
	}
}

func parseEKSVersions(payload []byte) ([]EKSVersion, error) {
	var response struct {
		Versions []struct {
			Version                  string `json:"clusterVersion"`
			PatchVersion             string `json:"kubernetesPatchVersion"`
			Default                  bool   `json:"defaultVersion"`
			Status                   string `json:"versionStatus"`
			LegacyStatus             string `json:"status"`
			DefaultPlatformVersion   string `json:"defaultPlatformVersion"`
			ReleaseDate              string `json:"releaseDate"`
			EndOfStandardSupportDate string `json:"endOfStandardSupportDate"`
			EndOfExtendedSupportDate string `json:"endOfExtendedSupportDate"`
		} `json:"clusterVersions"`
	}
	if err := json.Unmarshal(payload, &response); err != nil {
		return nil, fmt.Errorf("parse AWS EKS versions: %w", err)
	}
	byVersion := make(map[string]EKSVersion, len(response.Versions))
	for _, item := range response.Versions {
		if item.Version == "" {
			continue
		}
		status := item.Status
		if status == "" {
			status = strings.ToUpper(strings.ReplaceAll(item.LegacyStatus, "-", "_"))
		}
		version := EKSVersion{
			Version: item.Version, PatchVersion: item.PatchVersion, Default: item.Default, Status: status,
			DefaultPlatformVersion: item.DefaultPlatformVersion, ReleaseDate: item.ReleaseDate,
			EndOfStandardSupportDate: item.EndOfStandardSupportDate, EndOfExtendedSupportDate: item.EndOfExtendedSupportDate,
		}
		current, exists := byVersion[item.Version]
		if !exists || version.Default || current.Status == "" {
			byVersion[item.Version] = version
		}
	}
	result := make([]EKSVersion, 0, len(byVersion))
	for _, version := range byVersion {
		result = append(result, version)
	}
	sort.SliceStable(result, func(i, j int) bool { return compareVersion(result[i].Version, result[j].Version) > 0 })
	return result, nil
}

func parseEKSClusters(payload []byte) ([]EKSCluster, error) {
	var response struct {
		Clusters []string `json:"clusters"`
	}
	if err := json.Unmarshal(payload, &response); err != nil {
		return nil, fmt.Errorf("parse AWS EKS clusters: %w", err)
	}
	seen := make(map[string]bool, len(response.Clusters))
	result := make([]EKSCluster, 0, len(response.Clusters))
	for _, name := range response.Clusters {
		name = strings.TrimSpace(name)
		if name == "" || seen[name] {
			continue
		}
		seen[name] = true
		result = append(result, EKSCluster{Name: name})
	}
	sort.Slice(result, func(i, j int) bool { return naturalLess(result[i].Name, result[j].Name) })
	return result, nil
}

func parseVPCs(vpcPayload, subnetPayload []byte) ([]VPC, error) {
	type tag struct {
		Key   string `json:"Key"`
		Value string `json:"Value"`
	}
	var vpcResponse struct {
		VPCs []struct {
			ID      string `json:"VpcId"`
			CIDR    string `json:"CidrBlock"`
			Default bool   `json:"IsDefault"`
			State   string `json:"State"`
			Tags    []tag  `json:"Tags"`
		} `json:"Vpcs"`
	}
	if err := json.Unmarshal(vpcPayload, &vpcResponse); err != nil {
		return nil, fmt.Errorf("parse AWS VPCs: %w", err)
	}
	var subnetResponse struct {
		Subnets []struct {
			ID                  string `json:"SubnetId"`
			VPCID               string `json:"VpcId"`
			CIDR                string `json:"CidrBlock"`
			AvailabilityZone    string `json:"AvailabilityZone"`
			AvailableIPCount    int    `json:"AvailableIpAddressCount"`
			MapPublicIPOnLaunch bool   `json:"MapPublicIpOnLaunch"`
			Tags                []tag  `json:"Tags"`
		} `json:"Subnets"`
	}
	if err := json.Unmarshal(subnetPayload, &subnetResponse); err != nil {
		return nil, fmt.Errorf("parse AWS VPC subnets: %w", err)
	}
	nameOf := func(tags []tag) string {
		for _, item := range tags {
			if item.Key == "Name" {
				return strings.TrimSpace(item.Value)
			}
		}
		return ""
	}
	byVPC := make(map[string][]VPCSubnet)
	for _, subnet := range subnetResponse.Subnets {
		if subnet.ID == "" || subnet.VPCID == "" || subnet.AvailabilityZone == "" {
			continue
		}
		byVPC[subnet.VPCID] = append(byVPC[subnet.VPCID], VPCSubnet{
			ID: subnet.ID, Name: nameOf(subnet.Tags), CIDR: subnet.CIDR, AvailabilityZone: subnet.AvailabilityZone,
			AvailableIPCount: subnet.AvailableIPCount, MapPublicIPOnLaunch: subnet.MapPublicIPOnLaunch,
		})
	}
	result := make([]VPC, 0, len(vpcResponse.VPCs))
	for _, item := range vpcResponse.VPCs {
		if item.ID == "" || item.State != "available" {
			continue
		}
		subnets := byVPC[item.ID]
		sort.Slice(subnets, func(i, j int) bool {
			if subnets[i].AvailabilityZone != subnets[j].AvailabilityZone {
				return subnets[i].AvailabilityZone < subnets[j].AvailabilityZone
			}
			return naturalLess(subnets[i].ID, subnets[j].ID)
		})
		result = append(result, VPC{ID: item.ID, Name: nameOf(item.Tags), CIDR: item.CIDR, Default: item.Default, State: item.State, Subnets: subnets})
	}
	sort.Slice(result, func(i, j int) bool {
		left, right := result[i].Name, result[j].Name
		if left == "" {
			left = result[i].ID
		}
		if right == "" {
			right = result[j].ID
		}
		return naturalLess(left, right)
	})
	return result, nil
}

func parseSecurityGroups(payload []byte) ([]SecurityGroup, error) {
	type tag struct {
		Key   string `json:"Key"`
		Value string `json:"Value"`
	}
	var response struct {
		SecurityGroups []struct {
			ID          string `json:"GroupId"`
			Name        string `json:"GroupName"`
			VPCID       string `json:"VpcId"`
			Description string `json:"Description"`
			Tags        []tag  `json:"Tags"`
			Permissions []struct {
				Protocol string `json:"IpProtocol"`
				FromPort *int   `json:"FromPort"`
				ToPort   *int   `json:"ToPort"`
				IPRanges []struct {
					CIDR string `json:"CidrIp"`
				} `json:"IpRanges"`
				IPv6Ranges []struct {
					CIDR string `json:"CidrIpv6"`
				} `json:"Ipv6Ranges"`
				PrefixLists  []json.RawMessage `json:"PrefixListIds"`
				SourceGroups []json.RawMessage `json:"UserIdGroupPairs"`
			} `json:"IpPermissions"`
		} `json:"SecurityGroups"`
	}
	if err := json.Unmarshal(payload, &response); err != nil {
		return nil, fmt.Errorf("parse AWS security groups: %w", err)
	}
	result := make([]SecurityGroup, 0, len(response.SecurityGroups))
	for _, item := range response.SecurityGroups {
		if !strings.HasPrefix(item.ID, "sg-") || !strings.HasPrefix(item.VPCID, "vpc-") {
			continue
		}
		displayName := ""
		platformManagedGuard := false
		for _, itemTag := range item.Tags {
			if itemTag.Key == "Name" {
				displayName = strings.TrimSpace(itemTag.Value)
			}
			if itemTag.Key == "ops-deploy.io/resource" && itemTag.Value == "nlb-frontend-security-group" {
				platformManagedGuard = true
			}
		}
		selectable, blockedReason := true, ""
		switch {
		case item.Name == "default":
			selectable, blockedReason = false, "VPC 默认安全组不能作为 NLB 专用入口安全组"
		case strings.HasPrefix(item.Name, "eks-cluster-sg-"):
			selectable, blockedReason = false, "EKS 集群安全组不能复用为公网 NLB 入口安全组"
		case platformManagedGuard:
			selectable, blockedReason = false, "平台守护安全组由对应环境自动维护，无需重复选择"
		}
		allowsHTTP, publicHTTP := securityGroupAllowsPort(item.Permissions, 80)
		allowsHTTPS, publicHTTPS := securityGroupAllowsPort(item.Permissions, 443)
		sourceCount := 0
		for _, permission := range item.Permissions {
			sourceCount += len(permission.IPRanges) + len(permission.IPv6Ranges) + len(permission.PrefixLists) + len(permission.SourceGroups)
		}
		result = append(result, SecurityGroup{
			ID: item.ID, Name: item.Name, DisplayName: displayName, VPCID: item.VPCID, Description: item.Description,
			IngressSourceCount: sourceCount, AllowsHTTP: allowsHTTP, AllowsHTTPS: allowsHTTPS,
			PublicHTTP: publicHTTP, PublicHTTPS: publicHTTPS, Selectable: selectable,
			BlockedReason: blockedReason, PlatformManagedGuard: platformManagedGuard,
		})
	}
	sort.Slice(result, func(i, j int) bool {
		left, right := result[i].DisplayName, result[j].DisplayName
		if left == "" {
			left = result[i].Name
		}
		if right == "" {
			right = result[j].Name
		}
		if left == right {
			return naturalLess(result[i].ID, result[j].ID)
		}
		return naturalLess(left, right)
	})
	return result, nil
}

func securityGroupAllowsPort(permissions []struct {
	Protocol string `json:"IpProtocol"`
	FromPort *int   `json:"FromPort"`
	ToPort   *int   `json:"ToPort"`
	IPRanges []struct {
		CIDR string `json:"CidrIp"`
	} `json:"IpRanges"`
	IPv6Ranges []struct {
		CIDR string `json:"CidrIpv6"`
	} `json:"Ipv6Ranges"`
	PrefixLists  []json.RawMessage `json:"PrefixListIds"`
	SourceGroups []json.RawMessage `json:"UserIdGroupPairs"`
}, port int) (bool, bool) {
	allowed, public := false, false
	for _, permission := range permissions {
		portMatches := permission.Protocol == "-1" || ((permission.Protocol == "tcp" || permission.Protocol == "6") && permission.FromPort != nil && permission.ToPort != nil && *permission.FromPort <= port && *permission.ToPort >= port)
		if !portMatches {
			continue
		}
		allowed = allowed || len(permission.IPRanges)+len(permission.IPv6Ranges)+len(permission.PrefixLists)+len(permission.SourceGroups) > 0
		for _, source := range permission.IPRanges {
			public = public || source.CIDR == "0.0.0.0/0"
		}
		for _, source := range permission.IPv6Ranges {
			public = public || source.CIDR == "::/0"
		}
	}
	return allowed, public
}

func parseInstanceTypes(payload []byte) ([]InstanceType, error) {
	var response struct {
		Items []struct {
			Name              string `json:"InstanceType"`
			CurrentGeneration bool   `json:"CurrentGeneration"`
			VCpuInfo          struct {
				DefaultVCpus int `json:"DefaultVCpus"`
			} `json:"VCpuInfo"`
			MemoryInfo struct {
				SizeInMiB int `json:"SizeInMiB"`
			} `json:"MemoryInfo"`
			ProcessorInfo struct {
				Architectures []string `json:"SupportedArchitectures"`
			} `json:"ProcessorInfo"`
			NetworkInfo struct {
				Performance              string `json:"NetworkPerformance"`
				MaximumNetworkInterfaces int    `json:"MaximumNetworkInterfaces"`
			} `json:"NetworkInfo"`
			EBSInfo struct {
				OptimizedSupport string `json:"EbsOptimizedSupport"`
			} `json:"EbsInfo"`
			InstanceStorageSupported bool     `json:"InstanceStorageSupported"`
			Burstable                bool     `json:"BurstablePerformanceSupported"`
			UsageClasses             []string `json:"SupportedUsageClasses"`
		} `json:"InstanceTypes"`
	}
	if err := json.Unmarshal(payload, &response); err != nil {
		return nil, fmt.Errorf("parse AWS EC2 instance types: %w", err)
	}
	result := make([]InstanceType, 0, len(response.Items))
	for _, item := range response.Items {
		if item.Name == "" {
			continue
		}
		result = append(result, InstanceType{
			Name: item.Name, CurrentGeneration: item.CurrentGeneration, VCpu: item.VCpuInfo.DefaultVCpus,
			MemoryMiB: item.MemoryInfo.SizeInMiB, Architectures: item.ProcessorInfo.Architectures,
			NetworkPerformance: item.NetworkInfo.Performance, MaximumNetworkInterfaces: item.NetworkInfo.MaximumNetworkInterfaces,
			EBSOptimizedSupport: item.EBSInfo.OptimizedSupport, InstanceStorageSupported: item.InstanceStorageSupported,
			Burstable: item.Burstable, UsageClasses: item.UsageClasses,
		})
	}
	sort.Slice(result, func(i, j int) bool { return naturalLess(result[i].Name, result[j].Name) })
	return result, nil
}

func parseOrderableDBInstanceOptions(payload []byte) ([]ServiceInstanceOption, error) {
	var response struct {
		Items []struct {
			Value             string `json:"DBInstanceClass"`
			EngineVersion     string `json:"EngineVersion"`
			MultiAZCapable    bool   `json:"MultiAZCapable"`
			StorageType       string `json:"StorageType"`
			AvailabilityZones []struct {
				Name string `json:"Name"`
			} `json:"AvailabilityZones"`
		} `json:"OrderableDBInstanceOptions"`
	}
	if err := json.Unmarshal(payload, &response); err != nil {
		return nil, fmt.Errorf("parse AWS database instance options: %w", err)
	}
	byValue := make(map[string]*ServiceInstanceOption)
	for _, item := range response.Items {
		if item.Value == "" {
			continue
		}
		option := ensureServiceOption(byValue, item.Value)
		option.MultiAZCapable = option.MultiAZCapable || item.MultiAZCapable
		appendUnique(&option.EngineVersions, item.EngineVersion)
		appendUnique(&option.StorageTypes, item.StorageType)
		for _, zone := range item.AvailabilityZones {
			appendUnique(&option.AvailabilityZones, zone.Name)
		}
	}
	return sortedServiceOptions(byValue), nil
}

func parseMQInstanceOptions(payload []byte) ([]ServiceInstanceOption, error) {
	var response struct {
		Items []struct {
			Value             string   `json:"HostInstanceType"`
			EngineVersions    []string `json:"SupportedEngineVersions"`
			DeploymentModes   []string `json:"SupportedDeploymentModes"`
			StorageType       string   `json:"StorageType"`
			AvailabilityZones []struct {
				Name string `json:"Name"`
			} `json:"AvailabilityZones"`
		} `json:"BrokerInstanceOptions"`
	}
	if err := json.Unmarshal(payload, &response); err != nil {
		return nil, fmt.Errorf("parse AWS MQ instance options: %w", err)
	}
	byValue := make(map[string]*ServiceInstanceOption)
	for _, item := range response.Items {
		if item.Value == "" {
			continue
		}
		option := ensureServiceOption(byValue, item.Value)
		for _, version := range item.EngineVersions {
			appendUnique(&option.EngineVersions, version)
		}
		for _, mode := range item.DeploymentModes {
			appendUnique(&option.DeploymentModes, mode)
		}
		appendUnique(&option.StorageTypes, item.StorageType)
		for _, zone := range item.AvailabilityZones {
			appendUnique(&option.AvailabilityZones, zone.Name)
		}
	}
	return sortedServiceOptions(byValue), nil
}

func parsePricingInstanceOptions(payload []byte, prefix string) ([]ServiceInstanceOption, error) {
	var response struct {
		PriceList []string `json:"PriceList"`
	}
	if err := json.Unmarshal(payload, &response); err != nil {
		return nil, fmt.Errorf("parse AWS Pricing response: %w", err)
	}
	byValue := make(map[string]*ServiceInstanceOption)
	for _, encoded := range response.PriceList {
		var product struct {
			Product struct {
				Attributes map[string]string `json:"attributes"`
			} `json:"product"`
		}
		if json.Unmarshal([]byte(encoded), &product) != nil {
			continue
		}
		value := ""
		for _, key := range []string{"instanceType", "cacheNodeType", "brokerType", "computeFamily"} {
			candidate := product.Product.Attributes[key]
			if strings.HasPrefix(candidate, prefix) {
				value = candidate
				break
			}
			if prefix == "kafka." && validMSKBrokerType(candidate) {
				value = prefix + candidate
				break
			}
		}
		if value == "" {
			for _, candidate := range product.Product.Attributes {
				if strings.HasPrefix(candidate, prefix) {
					value = candidate
					break
				}
			}
		}
		if value != "" {
			ensureServiceOption(byValue, value)
		}
	}
	return sortedServiceOptions(byValue), nil
}

func selectLatestEngineVersion(payload []byte, requested string) (string, error) {
	var response struct {
		Items []struct {
			Version string `json:"EngineVersion"`
		} `json:"DBEngineVersions"`
	}
	if err := json.Unmarshal(payload, &response); err != nil {
		return "", fmt.Errorf("parse AWS database engine versions: %w", err)
	}
	requested = strings.TrimSpace(requested)
	versions := make([]string, 0, len(response.Items))
	for _, item := range response.Items {
		if item.Version == requested || strings.HasPrefix(item.Version, requested+".") || strings.HasPrefix(item.Version, requested+"-") {
			versions = append(versions, item.Version)
		}
	}
	if len(versions) == 0 {
		return "", ErrEngineVersionMissing
	}
	sort.SliceStable(versions, func(i, j int) bool { return compareVersion(versions[i], versions[j]) > 0 })
	return versions[0], nil
}

func parseEngineVersions(payload []byte, service string) ([]string, error) {
	versions := []string{}
	switch service {
	case "rds-mysql", "rds-postgres", "aurora-mysql", "documentdb":
		var response struct {
			Items []struct {
				Version string `json:"EngineVersion"`
			} `json:"DBEngineVersions"`
		}
		if err := json.Unmarshal(payload, &response); err != nil {
			return nil, fmt.Errorf("parse AWS database engine versions: %w", err)
		}
		for _, item := range response.Items {
			appendUnique(&versions, item.Version)
		}
	case "elasticache":
		var response struct {
			Items []struct {
				Version string `json:"EngineVersion"`
			} `json:"CacheEngineVersions"`
		}
		if err := json.Unmarshal(payload, &response); err != nil {
			return nil, fmt.Errorf("parse AWS cache engine versions: %w", err)
		}
		for _, item := range response.Items {
			appendUnique(&versions, item.Version)
		}
	case "msk":
		var response struct {
			Items []struct{ Version, Status string } `json:"KafkaVersions"`
		}
		if err := json.Unmarshal(payload, &response); err != nil {
			return nil, fmt.Errorf("parse AWS Kafka versions: %w", err)
		}
		for _, item := range response.Items {
			if item.Status == "" || strings.EqualFold(item.Status, "ACTIVE") {
				appendUnique(&versions, item.Version)
			}
		}
	case "amazon-mq":
		var response struct {
			Items []struct {
				Versions []string `json:"SupportedEngineVersions"`
			} `json:"BrokerInstanceOptions"`
		}
		if err := json.Unmarshal(payload, &response); err != nil {
			return nil, fmt.Errorf("parse Amazon MQ engine versions: %w", err)
		}
		for _, option := range response.Items {
			for _, version := range option.Versions {
				appendUnique(&versions, version)
			}
		}
	default:
		return nil, ErrInvalidQuery
	}
	versions = slices.DeleteFunc(versions, func(version string) bool { return strings.TrimSpace(version) == "" })
	sort.SliceStable(versions, func(i, j int) bool { return compareVersion(versions[i], versions[j]) > 0 })
	return versions, nil
}

func validMSKBrokerType(value string) bool {
	if value == "" || strings.HasPrefix(value, "kafka.") {
		return false
	}
	parts := strings.Split(value, ".")
	if len(parts) != 2 || len(parts[0]) < 2 {
		return false
	}
	for _, char := range parts[0] {
		if (char < 'a' || char > 'z') && (char < '0' || char > '9') {
			return false
		}
	}
	return parts[1] == "large" || strings.HasSuffix(parts[1], "xlarge")
}

func ensureServiceOption(items map[string]*ServiceInstanceOption, value string) *ServiceInstanceOption {
	if items[value] == nil {
		items[value] = &ServiceInstanceOption{Value: value}
	}
	return items[value]
}

func appendUnique(values *[]string, value string) {
	if value == "" {
		return
	}
	for _, current := range *values {
		if current == value {
			return
		}
	}
	*values = append(*values, value)
	sort.Strings(*values)
}

func sortedServiceOptions(items map[string]*ServiceInstanceOption) []ServiceInstanceOption {
	result := make([]ServiceInstanceOption, 0, len(items))
	for _, item := range items {
		result = append(result, *item)
	}
	sort.Slice(result, func(i, j int) bool { return naturalLess(result[i].Value, result[j].Value) })
	return result
}

func compareVersion(left, right string) int {
	leftParts, rightParts := strings.Split(left, "."), strings.Split(right, ".")
	for index := 0; index < len(leftParts) || index < len(rightParts); index++ {
		leftValue, rightValue := 0, 0
		if index < len(leftParts) {
			leftValue = numericPrefix(leftParts[index])
		}
		if index < len(rightParts) {
			rightValue = numericPrefix(rightParts[index])
		}
		if leftValue > rightValue {
			return 1
		}
		if leftValue < rightValue {
			return -1
		}
	}
	return 0
}

func numericPrefix(value string) int {
	end := 0
	for end < len(value) && value[end] >= '0' && value[end] <= '9' {
		end++
	}
	if end == 0 {
		return 0
	}
	result, _ := strconv.Atoi(value[:end])
	return result
}

func naturalLess(left, right string) bool {
	leftPrefix, leftFamily, leftSize := instanceSortParts(left)
	rightPrefix, rightFamily, rightSize := instanceSortParts(right)
	if leftPrefix != rightPrefix {
		return leftPrefix < rightPrefix
	}
	if leftFamily != rightFamily {
		return leftFamily < rightFamily
	}
	order := map[string]int{"nano": 1, "micro": 2, "small": 3, "medium": 4, "large": 5, "xlarge": 6, "2xlarge": 7, "3xlarge": 8, "4xlarge": 9, "6xlarge": 10, "8xlarge": 11, "9xlarge": 12, "10xlarge": 13, "12xlarge": 14, "16xlarge": 15, "18xlarge": 16, "24xlarge": 17, "32xlarge": 18, "48xlarge": 19, "metal": 99}
	leftOrder, leftKnown := order[leftSize]
	rightOrder, rightKnown := order[rightSize]
	if leftKnown && rightKnown && leftOrder != rightOrder {
		return leftOrder < rightOrder
	}
	return leftSize < rightSize
}

func instanceSortParts(value string) (prefix, family, size string) {
	parts := strings.Split(value, ".")
	if len(parts) >= 3 {
		return parts[0], parts[1], strings.Join(parts[2:], ".")
	}
	if len(parts) == 2 {
		return "", parts[0], parts[1]
	}
	return "", value, ""
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

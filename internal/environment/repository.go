package environment

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/mail"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/GZ-Alinx/awsinfra/internal/sensitive"
)

var (
	namePattern             = regexp.MustCompile(`^[a-z][a-z0-9-]{0,62}$`)
	kubernetesNamePattern   = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`)
	regionPattern           = regexp.MustCompile(`^[a-z]{2}(?:-gov)?-[a-z]+-[0-9]$`)
	vpcIDPattern            = regexp.MustCompile(`^vpc-[0-9a-f]{8,17}$`)
	subnetIDPattern         = regexp.MustCompile(`^subnet-[0-9a-f]{8,17}$`)
	amazonMQInstancePattern = regexp.MustCompile(`^mq\.[a-z0-9]+\.(?:medium|large|xlarge|[0-9]+xlarge)$`)
	storageQuantityPattern  = regexp.MustCompile(`^([1-9][0-9]*)Gi$`)
	lokiRetentionPattern    = regexp.MustCompile(`^[1-9][0-9]{0,5}h$`)
	javaHeapOptionPattern   = regexp.MustCompile(`(?i)-Xm([sx])([1-9][0-9]*)([kmgt]?)\b`)
	memoryQuantityPattern   = regexp.MustCompile(`(?i)^([1-9][0-9]*)([kmgt]i?)$`)
)

type Document map[string]any

type Summary struct {
	Name        string   `json:"target_name"`
	Project     string   `json:"project"`
	Environment string   `json:"environment"`
	Region      string   `json:"region"`
	ClusterName string   `json:"cluster_name"`
	Components  []string `json:"components"`
}

type Repository struct {
	dir   string
	store Store
	mu    sync.RWMutex
}

type Store interface {
	LoadEnvironments(context.Context) (map[string][]byte, error)
	GetEnvironment(context.Context, string) ([]byte, error)
	SaveEnvironment(context.Context, string, []byte) error
	DeleteEnvironment(context.Context, string) error
}

func NewRepository(dir string) (*Repository, error) {
	return NewRepositoryWithStore(dir, nil)
}

func NewRepositoryWithStore(dir string, store Store) (*Repository, error) {
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return nil, fmt.Errorf("create environments directory: %w", err)
	}
	repository := &Repository{dir: dir, store: store}
	if store != nil {
		if err := repository.bootstrap(); err != nil {
			return nil, fmt.Errorf("bootstrap environments: %w", err)
		}
	}
	return repository, nil
}

func (r *Repository) List(componentPaths map[string]string) ([]Summary, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	documents, err := r.loadAllUnlocked()
	if err != nil {
		return nil, err
	}
	result := make([]Summary, 0, len(documents))
	for name, doc := range documents {
		result = append(result, summarize(name, doc, componentPaths))
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Name < result[j].Name })
	return result, nil
}

func (r *Repository) Load(name string) (Document, error) {
	if !ValidName(name) {
		return nil, ErrInvalidName
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	if r.store != nil {
		ctx, cancel := storageContext()
		defer cancel()
		payload, err := r.store.GetEnvironment(ctx, name)
		if err != nil {
			return nil, err
		}
		return decodeDocument(payload)
	}
	return r.loadUnlocked(name)
}

func (r *Repository) Save(name string, doc Document) error {
	if !ValidName(name) {
		return ErrInvalidName
	}
	NormalizeDomainRoutes(doc)
	NormalizeDomainBackendProtocols(doc)
	if err := Validate(doc); err != nil {
		return err
	}
	if stringValue(doc["environment"]) == "" {
		doc["environment"] = name
	}
	jsonPayload, err := json.Marshal(doc)
	if err != nil {
		return fmt.Errorf("encode environment JSON: %w", err)
	}
	b, err := yaml.Marshal(doc)
	if err != nil {
		return fmt.Errorf("encode environment: %w", err)
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	if r.store != nil {
		ctx, cancel := storageContext()
		defer cancel()
		if err := r.store.SaveEnvironment(ctx, name, jsonPayload); err != nil {
			return fmt.Errorf("save environment to MySQL: %w", err)
		}
	}
	return r.writeFileUnlocked(name, b)
}

// NormalizeDomainRoutes upgrades the legacy one-domain/one-backend structure
// to the domain-level routes collection used by the UI and Terraform. The
// legacy fields are deliberately retained as a first-route mirror so an older
// platform binary can still read a rolled-back configuration.
func NormalizeDomainRoutes(doc Document) {
	domains, ok := doc["domains"].([]any)
	if !ok {
		return
	}
	for _, raw := range domains {
		domain, ok := mapValue(raw)
		if !ok {
			continue
		}
		protocol := strings.ToLower(strings.TrimSpace(stringValue(domain["protocol"])))
		if protocol == "" {
			if boolValue(domain["tls_enabled"]) {
				protocol = "https"
			} else {
				protocol = "http"
			}
		}
		if protocol == "tcp" {
			continue
		}
		routes, exists := domain["routes"].([]any)
		if !exists || len(routes) == 0 {
			routes = []any{map[string]any{
				"path":         defaultDomainRouteString(domain["path"], "/"),
				"path_type":    defaultDomainRouteString(domain["path_type"], "Prefix"),
				"service":      stringValue(domain["service"]),
				"service_port": domain["service_port"],
			}}
			domain["routes"] = routes
		}
		for _, rawRoute := range routes {
			route, valid := mapValue(rawRoute)
			if !valid {
				continue
			}
			route["path"] = defaultDomainRouteString(route["path"], "/")
			route["path_type"] = defaultDomainRouteString(route["path_type"], "Prefix")
		}
		if first, valid := mapValue(routes[0]); valid {
			domain["path"] = first["path"]
			domain["path_type"] = first["path_type"]
			domain["service"] = first["service"]
			domain["service_port"] = first["service_port"]
		}
	}
}

func defaultDomainRouteString(value any, fallback string) string {
	if configured := strings.TrimSpace(stringValue(value)); configured != "" {
		return configured
	}
	return fallback
}

func httpDomainRoutes(domain map[string]any) ([]any, bool) {
	if raw, exists := domain["routes"]; exists {
		routes, ok := raw.([]any)
		return routes, ok
	}
	return []any{map[string]any{
		"path":         defaultDomainRouteString(domain["path"], "/"),
		"path_type":    defaultDomainRouteString(domain["path_type"], "Prefix"),
		"service":      stringValue(domain["service"]),
		"service_port": domain["service_port"],
	}}, true
}

// NormalizeDomainBackendProtocols keeps the client-to-gateway protocol
// separate from the gateway-to-Service protocol. HTTPS/WSS/GRPCS describes
// edge TLS; the upstream can independently be HTTP, HTTPS, gRPC or gRPCS.
// Older UI versions inferred HTTPS solely from Service port 443, which is
// unsafe because Kubernetes commonly maps port 443 to a plaintext named port.
func NormalizeDomainBackendProtocols(doc Document) {
	domains, ok := doc["domains"].([]any)
	if !ok {
		return
	}
	for _, raw := range domains {
		domain, ok := mapValue(raw)
		if !ok {
			continue
		}
		protocol := strings.ToLower(strings.TrimSpace(stringValue(domain["protocol"])))
		if protocol == "" {
			if boolValue(domain["tls_enabled"]) {
				protocol = "https"
			} else {
				protocol = "http"
			}
		}
		annotations, _ := mapValue(domain["annotations"])
		if annotations == nil {
			annotations = map[string]any{}
		}
		legacyBackend := strings.ToUpper(strings.TrimSpace(stringValue(annotations["higress.io/backend-protocol"])))
		if legacyBackend == "" {
			legacyBackend = strings.ToUpper(strings.TrimSpace(stringValue(annotations["nginx.ingress.kubernetes.io/backend-protocol"])))
		}
		delete(annotations, "higress.io/backend-protocol")
		delete(annotations, "nginx.ingress.kubernetes.io/backend-protocol")
		if protocol == "tcp" {
			delete(domain, "backend_protocol")
			domain["annotations"] = annotations
			continue
		}

		backendProtocol := strings.ToLower(strings.TrimSpace(stringValue(domain["backend_protocol"])))
		if backendProtocol == "" {
			// A legacy WSS rule with no explicit backend_protocol came from the
			// old port-443 heuristic. Default it to plaintext WebSocket; operators
			// can explicitly select HTTPS when the Pod truly terminates TLS.
			if protocol == "grpc" || protocol == "grpcs" {
				if legacyBackend == "GRPCS" {
					backendProtocol = "grpcs"
				} else {
					backendProtocol = "grpc"
				}
			} else if protocol == "wss" {
				backendProtocol = "http"
			} else {
				if legacyBackend == "HTTPS" {
					backendProtocol = "https"
				} else {
					backendProtocol = "http"
				}
			}
		}
		domain["backend_protocol"] = backendProtocol
		if backendProtocol == "https" || backendProtocol == "grpc" || backendProtocol == "grpcs" {
			if strings.EqualFold(stringValue(domain["gateway"]), "nginx") {
				annotations["nginx.ingress.kubernetes.io/backend-protocol"] = strings.ToUpper(backendProtocol)
			} else {
				annotations["higress.io/backend-protocol"] = strings.ToUpper(backendProtocol)
			}
		}
		domain["annotations"] = annotations
	}
}

func (r *Repository) writeFileUnlocked(name string, b []byte) error {
	tmp, err := os.CreateTemp(r.dir, ".environment-*.yaml")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(0o640); err != nil {
		return errors.Join(err, tmp.Close())
	}
	if _, err := tmp.Write(b); err != nil {
		return errors.Join(err, tmp.Close())
	}
	if err := tmp.Sync(); err != nil {
		return errors.Join(err, tmp.Close())
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, r.Path(name))
}

func (r *Repository) Clone(name, source string) (Document, error) {
	return r.CloneForProject(name, source, "", name)
}

func (r *Repository) CloneForProject(name, source, project, environment string) (Document, error) {
	if !ValidName(name) || !ValidName(source) {
		return nil, ErrInvalidName
	}
	if _, err := r.Load(name); err == nil {
		return nil, ErrAlreadyExists
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	doc, err := r.Load(source)
	if err != nil {
		return nil, err
	}
	if project != "" {
		doc["project"] = project
	}
	doc["environment"] = environment
	if err := r.Save(name, doc); err != nil {
		return nil, err
	}
	return doc, nil
}

func (r *Repository) Delete(name string) error {
	if !ValidName(name) {
		return ErrInvalidName
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.store != nil {
		ctx, cancel := storageContext()
		defer cancel()
		if err := r.store.DeleteEnvironment(ctx, name); err != nil {
			return err
		}
	}
	if err := os.Remove(r.Path(name)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

func (r *Repository) Path(name string) string {
	return filepath.Join(r.dir, name+".yaml")
}

func (r *Repository) loadUnlocked(name string) (Document, error) {
	b, err := os.ReadFile(r.Path(name))
	if err != nil {
		return nil, err
	}
	var doc Document
	if err := yaml.Unmarshal(b, &doc); err != nil {
		return nil, err
	}
	return doc, nil
}

func (r *Repository) loadAllUnlocked() (map[string]Document, error) {
	if r.store != nil {
		ctx, cancel := storageContext()
		defer cancel()
		records, err := r.store.LoadEnvironments(ctx)
		if err != nil {
			return nil, err
		}
		result := make(map[string]Document, len(records))
		for name, payload := range records {
			doc, err := decodeDocument(payload)
			if err != nil {
				return nil, fmt.Errorf("decode environment %s from MySQL: %w", name, err)
			}
			result[name] = doc
		}
		return result, nil
	}
	entries, err := os.ReadDir(r.dir)
	if err != nil {
		return nil, err
	}
	result := make(map[string]Document)
	for _, entry := range entries {
		if entry.IsDir() || (filepath.Ext(entry.Name()) != ".yaml" && filepath.Ext(entry.Name()) != ".yml") {
			continue
		}
		name := strings.TrimSuffix(strings.TrimSuffix(entry.Name(), ".yaml"), ".yml")
		if !ValidName(name) {
			continue
		}
		doc, err := r.loadUnlocked(name)
		if err != nil {
			return nil, fmt.Errorf("load environment %s: %w", name, err)
		}
		result[name] = doc
	}
	return result, nil
}

func (r *Repository) bootstrap() error {
	ctx, cancel := storageContext()
	defer cancel()
	records, err := r.store.LoadEnvironments(ctx)
	if err != nil {
		return err
	}
	if len(records) == 0 {
		destination := r.store
		r.store = nil
		files, loadErr := r.loadAllUnlocked()
		r.store = destination
		if loadErr != nil {
			return loadErr
		}
		for name, doc := range files {
			if stringValue(doc["environment"]) == "" {
				doc["environment"] = name
			}
			payload, err := json.Marshal(doc)
			if err != nil {
				return err
			}
			if err := destination.SaveEnvironment(ctx, name, payload); err != nil {
				return err
			}
		}
		return nil
	}
	for name, payload := range records {
		doc, err := decodeDocument(payload)
		if err != nil {
			return err
		}
		if stringValue(doc["environment"]) == "" {
			doc["environment"] = name
		}
		encoded, err := yaml.Marshal(doc)
		if err != nil {
			return err
		}
		if err := r.writeFileUnlocked(name, encoded); err != nil {
			return err
		}
	}
	return nil
}

func decodeDocument(payload []byte) (Document, error) {
	var doc Document
	if err := json.Unmarshal(payload, &doc); err != nil {
		return nil, err
	}
	return doc, nil
}

func storageContext() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), 5*time.Second)
}

func Validate(doc Document) error {
	if sensitive.Has(map[string]any(doc)) {
		return errors.New("inline secrets are not allowed; use a Kubernetes or AWS Secrets Manager reference")
	}
	required := []string{"project", "environment", "region", "network", "eks", "components"}
	for _, field := range required {
		if _, ok := doc[field]; !ok {
			return fmt.Errorf("missing required field %q", field)
		}
	}
	project := stringValue(doc["project"])
	environmentName := stringValue(doc["environment"])
	if !namePattern.MatchString(project) || !namePattern.MatchString(environmentName) {
		return errors.New("project and environment must use lowercase letters, numbers and hyphens")
	}
	region := stringValue(doc["region"])
	if !regionPattern.MatchString(region) {
		return errors.New("region must be a valid AWS region code")
	}
	if err := ValidateTarget(doc); err != nil {
		return err
	}
	network, ok := mapValue(doc["network"])
	if !ok {
		return errors.New("network must be an object")
	}
	for _, field := range []string{"vpc_cidr", "service_ipv4_cidr", "availability_zones", "public_subnets", "private_subnets"} {
		if _, ok := network[field]; !ok {
			return fmt.Errorf("network.%s is required", field)
		}
	}
	if err := validateNetwork(network, region); err != nil {
		return err
	}
	eks, ok := mapValue(doc["eks"])
	if !ok {
		return errors.New("eks must be an object")
	}
	if !boolValue(eks["endpoint_private_access"]) && !boolValue(eks["endpoint_public_access"]) {
		return errors.New("at least one EKS API endpoint must be enabled")
	}
	if boolValue(eks["endpoint_public_access"]) {
		cidrs, ok := eks["public_access_cidrs"].([]any)
		if !ok || len(cidrs) == 0 {
			return errors.New("eks.public_access_cidrs is required when the public endpoint is enabled")
		}
		for _, raw := range cidrs {
			cidr := stringValue(raw)
			if _, _, err := net.ParseCIDR(cidr); err != nil {
				return fmt.Errorf("invalid EKS public access CIDR %q", cidr)
			}
			if environmentName == "prod" && (cidr == "0.0.0.0/0" || cidr == "::/0") {
				return errors.New("production EKS public access cannot be open to the entire internet")
			}
		}
	}
	nodeGroups, ok := mapValue(eks["node_groups"])
	if !ok || len(nodeGroups) == 0 {
		return errors.New("eks.node_groups must contain at least one node group")
	}
	workloadScheduling := WorkloadSchedulingEnabled(doc)
	roleCounts := map[string]int{}
	for name, raw := range nodeGroups {
		group, valid := mapValue(raw)
		if !namePattern.MatchString(name) || !valid {
			return fmt.Errorf("invalid EKS node group %q", name)
		}
		minimum, desired, maximum := intValue(group["min_size"]), intValue(group["desired_size"]), intValue(group["max_size"])
		if minimum < 0 || minimum > desired || desired > maximum || maximum < 1 {
			return fmt.Errorf("EKS node group %q requires 0 <= min <= desired <= max", name)
		}
		if deferred, exists := group["capacity_deferred"]; exists {
			if _, valid := deferred.(bool); !valid {
				return fmt.Errorf("EKS node group %q capacity_deferred must be boolean", name)
			}
		}
		subnetType := strings.TrimSpace(stringValue(group["subnet_type"]))
		if subnetType != "" && subnetType != "public" && subnetType != "private" {
			return fmt.Errorf("EKS node group %q subnet_type must be public or private", name)
		}
		instanceTypes, ok := group["instance_types"].([]any)
		if !ok || len(instanceTypes) == 0 || len(instanceTypes) > 20 {
			return fmt.Errorf("EKS 节点组 %q 的实例类型必须选择 1 到 20 个", name)
		}
		for _, rawInstanceType := range instanceTypes {
			if strings.TrimSpace(stringValue(rawInstanceType)) == "" {
				return fmt.Errorf("EKS 节点组 %q 包含空的实例类型", name)
			}
		}
		if labels, valid := mapValue(group["labels"]); valid {
			role := strings.TrimSpace(stringValue(labels["workload-class"]))
			if role != "" {
				if role != "gateway" && role != "application" && role != "platform" && role != "stateful" && role != "general" {
					return fmt.Errorf("EKS 节点组 %q 的用途 %q 不受支持", name, role)
				}
				roleCounts[role]++
			}
		}
		if taints, exists := group["taints"]; exists {
			items, valid := taints.([]any)
			if !valid {
				return fmt.Errorf("EKS 节点组 %q 的 taints 必须是数组", name)
			}
			for index, raw := range items {
				taint, valid := mapValue(raw)
				if !valid || strings.TrimSpace(stringValue(taint["key"])) == "" {
					return fmt.Errorf("EKS 节点组 %q 的 taints[%d] 缺少 key", name, index)
				}
				effect := strings.ToUpper(strings.TrimSpace(stringValue(taint["effect"])))
				if effect != "NO_SCHEDULE" && effect != "PREFER_NO_SCHEDULE" && effect != "NO_EXECUTE" {
					return fmt.Errorf("EKS 节点组 %q 的 taints[%d].effect 必须是 NO_SCHEDULE、PREFER_NO_SCHEDULE 或 NO_EXECUTE", name, index)
				}
			}
		}
		groupZones, ok := group["availability_zones"].([]any)
		if !ok || len(groupZones) == 0 {
			return fmt.Errorf("EKS node group %q requires at least one availability zone", name)
		}
		seenZones := make(map[string]bool, len(groupZones))
		for _, rawZone := range groupZones {
			zone := stringValue(rawZone)
			if seenZones[zone] || !containsStringValue(network["availability_zones"], zone) {
				return fmt.Errorf("EKS node group %q contains unavailable or duplicate availability zone %q", name, zone)
			}
			seenZones[zone] = true
		}
	}
	if workloadScheduling {
		if roleCounts["application"] == 0 || roleCounts["platform"] == 0 {
			return errors.New("启用工作负载调度规划时，至少需要一个业务服务节点组和一个运维组件节点组")
		}
	}
	namespaces, ok := mapValue(doc["namespaces"])
	if !ok {
		return errors.New("namespaces must be an object")
	}
	for name := range namespaces {
		if len(name) > 63 || !kubernetesNamePattern.MatchString(name) {
			return fmt.Errorf("invalid Kubernetes namespace %q", name)
		}
	}
	if dataServices, exists := doc["data_services"]; exists {
		services, ok := mapValue(dataServices)
		if !ok {
			return errors.New("data_services must be an object")
		}
		for _, key := range []string{"rds", "aurora"} {
			if raw, exists := services[key]; exists {
				service, valid := mapValue(raw)
				if !valid {
					return fmt.Errorf("data_services.%s must be an object", key)
				}
				if boolValue(service["enabled"]) {
					mode := strings.TrimSpace(stringValue(service["credential_management"]))
					if mode != "self-managed" && mode != "aws-managed" {
						return fmt.Errorf("data_services.%s.credential_management must be self-managed or aws-managed", key)
					}
					if strings.TrimSpace(stringValue(service["master_username"])) == "" {
						return fmt.Errorf("data_services.%s.master_username is required", key)
					}
				}
			}
		}
		for _, key := range []string{"rds", "postgres"} {
			service, valid := mapValue(services[key])
			if !valid || !boolValue(service["enabled"]) {
				continue
			}
			allocated := intValue(service["allocated_storage"])
			maximum := intValue(service["max_allocated_storage"])
			if allocated < 20 {
				return fmt.Errorf("data_services.%s.allocated_storage must be at least 20 GiB", key)
			}
			if maximum < allocated {
				return fmt.Errorf("data_services.%s.max_allocated_storage must be greater than or equal to allocated_storage", key)
			}
		}
		if raw, exists := services["aurora"]; exists {
			aurora, ok := mapValue(raw)
			if !ok {
				return errors.New("data_services.aurora must be an object")
			}
			if boolValue(aurora["enabled"]) {
				instances := intValue(aurora["instance_count"])
				minimum, minimumOK := floatValue(aurora["min_acu"])
				maximum, maximumOK := floatValue(aurora["max_acu"])
				if instances < 1 || instances > 15 {
					return errors.New("data_services.aurora.instance_count must be between 1 and 15")
				}
				if !minimumOK || !maximumOK || minimum < 0 || maximum <= 0 || minimum > maximum {
					return errors.New("data_services.aurora requires 0 <= min_acu <= max_acu and max_acu > 0")
				}
				if boolValue(aurora["backtrack_enabled"]) {
					hours := intValue(aurora["backtrack_window_hours"])
					if stringValue(aurora["engine"]) != "aurora-mysql" {
						return errors.New("data_services.aurora backtrack is supported only for the aurora-mysql engine")
					}
					if hours < 1 || hours > 72 {
						return errors.New("data_services.aurora.backtrack_window_hours must be between 1 and 72")
					}
				}
			}
		}
		if raw, exists := services["documentdb"]; exists {
			documentdb, ok := mapValue(raw)
			if !ok {
				return errors.New("data_services.documentdb must be an object")
			}
			if boolValue(documentdb["enabled"]) {
				instances := intValue(documentdb["instance_count"])
				if instances < 1 || instances > 16 {
					return errors.New("data_services.documentdb.instance_count must be between 1 and 16")
				}
				if stringValue(documentdb["instance_class"]) == "" || stringValue(documentdb["master_username"]) == "" {
					return errors.New("data_services.documentdb requires instance_class and master_username")
				}
				if storageType := stringValue(documentdb["storage_type"]); storageType != "standard" && storageType != "iopt1" {
					return errors.New("data_services.documentdb.storage_type must be standard or iopt1")
				}
			}
		}
		if raw, exists := services["elasticache"]; exists {
			elasticache, ok := mapValue(raw)
			if !ok {
				return errors.New("data_services.elasticache must be an object")
			}
			if boolValue(elasticache["enabled"]) {
				engine := strings.ToLower(strings.TrimSpace(stringValue(elasticache["engine"])))
				version := strings.TrimSpace(stringValue(elasticache["engine_version"]))
				if engine != "redis" && engine != "valkey" {
					return errors.New("data_services.elasticache.engine must be redis or valkey")
				}
				if version == "" {
					return errors.New("data_services.elasticache.engine_version is required")
				}
				if mode := strings.TrimSpace(stringValue(elasticache["mode"])); mode != "cluster" && mode != "serverless" {
					return errors.New("data_services.elasticache.mode must be cluster or serverless")
				}
				if strings.TrimSpace(stringValue(elasticache["mode"])) == "cluster" {
					shards := intValue(elasticache["num_node_groups"])
					nodesPerShard := intValue(elasticache["nodes_per_shard"])
					if shards < 1 || shards > 500 {
						return errors.New("data_services.elasticache.num_node_groups must be between 1 and 500")
					}
					if nodesPerShard < 1 || nodesPerShard > 6 {
						return errors.New("data_services.elasticache.nodes_per_shard must be between 1 and 6, including the primary node")
					}
					if shards*nodesPerShard > 500 {
						return errors.New("data_services.elasticache total node count must not exceed 500")
					}
					if replicas := intValue(elasticache["replicas_per_node_group"]); replicas != nodesPerShard-1 {
						return errors.New("data_services.elasticache.replicas_per_node_group must equal nodes_per_shard - 1")
					}
				}
				configured := strings.TrimSpace(stringValue(elasticache["parameter_group_name"]))
				if strings.HasPrefix(configured, "default.") {
					expected := elasticacheDefaultParameterGroup(engine, version)
					if configured != expected {
						return fmt.Errorf("data_services.elasticache.parameter_group_name %q is incompatible with %s %s; expected %q", configured, engine, version, expected)
					}
				}
			}
		}
		if raw, exists := services["amazon_mq"]; exists {
			amazonMQ, ok := mapValue(raw)
			if !ok {
				return errors.New("data_services.amazon_mq must be an object")
			}
			if boolValue(amazonMQ["enabled"]) {
				mode := stringValue(amazonMQ["deployment_mode"])
				if mode != "SINGLE_INSTANCE" && mode != "CLUSTER_MULTI_AZ" {
					return errors.New("data_services.amazon_mq.deployment_mode must be SINGLE_INSTANCE or CLUSTER_MULTI_AZ")
				}
				instanceType := stringValue(amazonMQ["host_instance_type"])
				if instanceType == "mq.t3.micro" {
					return errors.New("data_services.amazon_mq.host_instance_type mq.t3.micro is deprecated and unavailable for new RabbitMQ brokers; use mq.m7g.medium or larger")
				}
				if !amazonMQInstancePattern.MatchString(instanceType) {
					return errors.New("data_services.amazon_mq.host_instance_type is invalid")
				}
				if stringValue(amazonMQ["master_username"]) == "" {
					return errors.New("data_services.amazon_mq.master_username is required")
				}
			}
		}
		if raw, exists := services["msk"]; exists {
			msk, ok := mapValue(raw)
			if !ok {
				return errors.New("data_services.msk must be an object")
			}
			if boolValue(msk["enabled"]) {
				mode := stringValue(msk["mode"])
				if mode != "serverless" && mode != "provisioned" {
					return errors.New("data_services.msk.mode must be serverless or provisioned")
				}
				if mode == "provisioned" {
					zones := selectedDataSubnetCount(network)
					brokers := intValue(msk["broker_count"])
					if zones < 2 || brokers < zones || brokers%zones != 0 {
						return fmt.Errorf("data_services.msk.broker_count must be a positive multiple of the %d selected data availability zones", zones)
					}
					if intValue(msk["volume_size"]) < 1 {
						return errors.New("data_services.msk.volume_size must be at least 1 GiB")
					}
				}
			}
		}
	}
	if rawECR, exists := doc["ecr"]; exists {
		ecr, ok := mapValue(rawECR)
		if !ok {
			return errors.New("ecr must be an object")
		}
		if boolValue(ecr["enabled"]) {
			if intValue(ecr["keep_image_count"]) < 1 {
				return errors.New("ecr.keep_image_count must be at least 1")
			}
			if intValue(ecr["untagged_expire_days"]) < 1 {
				return errors.New("ecr.untagged_expire_days must be at least 1")
			}
		}
	}
	if components, ok := mapValue(doc["components"]); ok {
		for _, key := range []string{"consul", "etcd"} {
			if config, valid := mapValue(components[key]); valid {
				if err := validateDeploymentMode("components."+key, config); err != nil {
					return err
				}
				if boolValue(config["enabled"]) {
					if err := validateStatefulService("components."+key, config); err != nil {
						return err
					}
				}
			}
		}
		if err := validateStatefulPlatformCapacity(doc, components, nodeGroups); err != nil {
			return err
		}
		if catalog, valid := mapValue(components["catalog"]); valid {
			for key, raw := range catalog {
				config, valid := mapValue(raw)
				if !valid {
					return fmt.Errorf("components.catalog.%s must be an object", key)
				}
				if err := validateDeploymentMode("components.catalog."+key, config); err != nil {
					return err
				}
				if boolValue(config["enabled"]) && boolValue(config["standalone_only"]) && stringValue(config["deployment_mode"]) != "standalone" {
					return fmt.Errorf("components.catalog.%s supports standalone mode only", key)
				}
				if timeout := intValue(config["timeout"]); timeout < 60 || timeout > 7200 {
					return fmt.Errorf("components.catalog.%s.timeout must be between 60 and 7200 seconds", key)
				}
				if values, exists := config["values"]; exists {
					payload, err := json.Marshal(values)
					if err != nil || len(payload) > 1<<20 {
						return fmt.Errorf("components.catalog.%s.values must be valid and no larger than 1 MiB", key)
					}
				}
				if boolValue(config["enabled"]) && (key == "mysql" || key == "redis" || key == "activemq" || key == "mongodb") {
					if err := validateDataServiceComponent(key, config); err != nil {
						return err
					}
				}
				if boolValue(config["enabled"]) && (key == "bytebase" || key == "redisinsight" || key == "etcd_workbench") {
					if err := validateDatabaseManagementComponent(key, config); err != nil {
						return err
					}
				}
				if boolValue(config["enabled"]) && key == "clickvisual_stack" {
					if err := validateClickVisualStack(config, doc); err != nil {
						return err
					}
				}
				if boolValue(config["enabled"]) && key == "efk_stack" {
					if err := validateEFKStack(config, doc); err != nil {
						return err
					}
				}
				if boolValue(config["enabled"]) && key == "opentelemetry_collector" {
					if err := validateOpenTelemetryCollectorStorage(config, components); err != nil {
						return err
					}
				}
				if boolValue(config["enabled"]) && key == "jaeger" {
					if err := validateJaegerStack(config, components); err != nil {
						return err
					}
				}
				if boolValue(config["enabled"]) && key == "tempo" {
					if err := validateTempoStorage(config, components); err != nil {
						return err
					}
				}
				if boolValue(config["enabled"]) && key == "higress" {
					if err := validateHigressNLB(config, doc); err != nil {
						return err
					}
				}
			}
			if raw, exists := catalog["loki"]; exists {
				config, valid := mapValue(raw)
				if valid && boolValue(config["enabled"]) {
					if err := validateLokiStorage(config, components); err != nil {
						return err
					}
				}
			}
			if enabledCatalogComponent(catalog, "bytebase") && !enabledCatalogComponent(catalog, "mysql") {
				return errors.New("Bytebase 自动接入需要同时启用 MySQL（自建）组件")
			}
			if enabledCatalogComponent(catalog, "redisinsight") && !enabledCatalogComponent(catalog, "redis") {
				return errors.New("RedisInsight 自动接入需要同时启用 Redis（自建）组件")
			}
			if enabledCatalogComponent(catalog, "etcd_workbench") {
				etcd, _ := mapValue(components["etcd"])
				if !boolValue(etcd["enabled"]) {
					return errors.New("Etcd Workbench 需要同时启用阶段1的 etcd 基础服务")
				}
			}
			deploymentTarget, _ := mapValue(doc["deployment_target"])
			if enabledCatalogComponent(catalog, "higress") && stringValue(deploymentTarget["type"]) != "existing_eks" {
				controller, _ := mapValue(components["aws_load_balancer_controller"])
				if !boolValue(controller["enabled"]) {
					return errors.New("Higress on a platform-managed EKS requires components.aws_load_balancer_controller.enabled=true")
				}
			}
		}
	}
	certificateKeys := make(map[string]struct{})
	if tlsValue, exists := doc["tls"]; exists {
		tlsConfig, ok := mapValue(tlsValue)
		if !ok {
			return errors.New("tls must be an object")
		}
		certificates, ok := tlsConfig["certificates"].([]any)
		if !ok {
			return errors.New("tls.certificates must be an array")
		}
		for index, raw := range certificates {
			certificate, ok := mapValue(raw)
			if !ok {
				return fmt.Errorf("tls.certificates[%d] must be an object", index)
			}
			key := stringValue(certificate["key"])
			mode := stringValue(certificate["mode"])
			secretName := stringValue(certificate["tls_secret_name"])
			namespace := stringValue(certificate["namespace"])
			if !namePattern.MatchString(key) || len(key) > 63 ||
				(mode != "cert-manager" && mode != "existing-secret" && mode != "uploaded-pem") ||
				!namePattern.MatchString(secretName) || len(secretName) > 63 ||
				!namePattern.MatchString(namespace) || len(namespace) > 63 {
				return fmt.Errorf("tls.certificates[%d] has invalid key, mode or tls_secret_name", index)
			}
			if _, duplicate := certificateKeys[key]; duplicate {
				return fmt.Errorf("duplicate TLS certificate key %q", key)
			}
			certificateKeys[key] = struct{}{}
			if mode == "cert-manager" {
				domains, validDomains := certificate["domains"].([]any)
				if !validDomains || len(domains) == 0 {
					return fmt.Errorf("tls.certificates[%d].domains is required for cert-manager", index)
				}
			}
			if mode == "uploaded-pem" && stringValue(certificate["material_ref"]) != key {
				return fmt.Errorf("tls.certificates[%d].material_ref must match its key for uploaded PEM material", index)
			}
		}
	}
	if domains, exists := doc["domains"].([]any); exists {
		for index, raw := range domains {
			domain, ok := mapValue(raw)
			if !ok {
				return fmt.Errorf("domains[%d] must be an object", index)
			}
			protocol := strings.ToLower(strings.TrimSpace(stringValue(domain["protocol"])))
			if protocol == "" {
				if boolValue(domain["tls_enabled"]) {
					protocol = "https"
				} else {
					protocol = "http"
				}
			}
			if protocol != "http" && protocol != "https" && protocol != "ws" && protocol != "wss" &&
				protocol != "grpc" && protocol != "grpcs" && protocol != "tcp" {
				return fmt.Errorf("domains[%d].protocol must be http, https, ws, wss, grpc, grpcs or tcp", index)
			}
			accessType := stringValue(domain["access_type"])
			if accessType == "" {
				accessType = "domain"
			}
			if accessType != "domain" && accessType != "ip" {
				return fmt.Errorf("domains[%d].access_type must be domain or ip", index)
			}
			if accessType == "domain" && strings.TrimSpace(stringValue(domain["domain"])) == "" {
				return fmt.Errorf("domains[%d].domain is required for domain access", index)
			}
			namespace := strings.TrimSpace(stringValue(domain["namespace"]))
			if !kubernetesNamePattern.MatchString(namespace) {
				return fmt.Errorf("domains[%d].namespace must be a valid Kubernetes namespace", index)
			}
			if protocol == "tcp" {
				serviceName := strings.TrimSpace(stringValue(domain["service"]))
				if !kubernetesNamePattern.MatchString(serviceName) {
					return fmt.Errorf("domains[%d].service must be a valid Kubernetes service", index)
				}
				servicePort := intValue(domain["service_port"])
				if servicePort < 1 || servicePort > 65535 {
					return fmt.Errorf("domains[%d].service_port must be between 1 and 65535", index)
				}
				if routes, configured := domain["routes"]; configured {
					if list, valid := routes.([]any); !valid || len(list) > 0 {
						return fmt.Errorf("domains[%d].routes is not supported for a raw TCP load balancer", index)
					}
				}
				if boolValue(domain["tls_enabled"]) || strings.TrimSpace(stringValue(domain["certificate_ref"])) != "" {
					return fmt.Errorf("domains[%d] TCP passthrough cannot use an HTTP TLS certificate", index)
				}
				externalPort := intValue(domain["external_port"])
				if externalPort == -1 {
					externalPort = servicePort
				}
				if externalPort < 1 || externalPort > 65535 {
					return fmt.Errorf("domains[%d].external_port must be between 1 and 65535", index)
				}
				scheme := strings.TrimSpace(stringValue(domain["tcp_scheme"]))
				if scheme == "" {
					scheme = "internet-facing"
				}
				if scheme != "internet-facing" && scheme != "internal" {
					return fmt.Errorf("domains[%d].tcp_scheme must be internet-facing or internal", index)
				}
				cidrs, valid := domain["allowed_cidrs"].([]any)
				if scheme == "internet-facing" && (!valid || len(cidrs) == 0) {
					return fmt.Errorf("domains[%d].allowed_cidrs is required for a public TCP load balancer", index)
				}
				for _, rawCIDR := range cidrs {
					cidr := strings.TrimSpace(stringValue(rawCIDR))
					if _, _, err := net.ParseCIDR(cidr); err != nil {
						return fmt.Errorf("domains[%d] contains invalid TCP source CIDR %q", index, cidr)
					}
					if cidr == "0.0.0.0/0" || cidr == "::/0" {
						return fmt.Errorf("domains[%d] TCP source CIDR cannot expose the service to the entire internet", index)
					}
				}
				continue
			}
			gateway := strings.ToLower(strings.TrimSpace(stringValue(domain["gateway"])))
			if gateway != "higress" && gateway != "nginx" {
				return fmt.Errorf("domains[%d].gateway must be higress or nginx for HTTP/WebSocket/gRPC routes", index)
			}
			grpcProtocol := protocol == "grpc" || protocol == "grpcs"
			knownTCPPorts := map[int]bool{2379: true, 2380: true, 3306: true, 5432: true, 5672: true, 6379: true, 9092: true, 27017: true, 61616: true}
			secureProtocol := protocol == "https" || protocol == "wss" || protocol == "grpcs"
			routes, validRoutes := httpDomainRoutes(domain)
			if !validRoutes || len(routes) == 0 {
				return fmt.Errorf("domains[%d].routes must contain at least one HTTP route", index)
			}
			if len(routes) > 64 {
				return fmt.Errorf("domains[%d].routes cannot contain more than 64 routes", index)
			}
			seenPaths := make(map[string]struct{}, len(routes))
			for routeIndex, rawRoute := range routes {
				route, valid := mapValue(rawRoute)
				if !valid {
					return fmt.Errorf("domains[%d].routes[%d] must be an object", index, routeIndex)
				}
				serviceName := strings.TrimSpace(stringValue(route["service"]))
				if !kubernetesNamePattern.MatchString(serviceName) {
					return fmt.Errorf("domains[%d].routes[%d].service must be a valid Kubernetes service", index, routeIndex)
				}
				servicePort := intValue(route["service_port"])
				if servicePort < 1 || servicePort > 65535 {
					return fmt.Errorf("domains[%d].routes[%d].service_port must be between 1 and 65535", index, routeIndex)
				}
				path := defaultDomainRouteString(route["path"], "/")
				if !strings.HasPrefix(path, "/") || strings.ContainsAny(path, "\r\n") {
					return fmt.Errorf("domains[%d].routes[%d].path must start with / and stay on one line", index, routeIndex)
				}
				pathType := defaultDomainRouteString(route["path_type"], "Prefix")
				if pathType != "Prefix" && pathType != "Exact" && pathType != "ImplementationSpecific" {
					return fmt.Errorf("domains[%d].routes[%d].path_type must be Prefix, Exact or ImplementationSpecific", index, routeIndex)
				}
				if _, duplicate := seenPaths[path]; duplicate {
					return fmt.Errorf("domains[%d] contains duplicate route path %q", index, path)
				}
				seenPaths[path] = struct{}{}
				if knownTCPPorts[servicePort] && !grpcProtocol {
					return fmt.Errorf("domains[%d].routes[%d] service port %d is a raw TCP port and cannot use HTTP/WebSocket ingress; choose TCP or gRPC when appropriate", index, routeIndex, servicePort)
				}
				sensitiveConsole := (serviceName == "bytebase" && servicePort == 8080) ||
					(serviceName == "redisinsight" && servicePort == 80) ||
					(serviceName == "etcd-workbench" && servicePort == 8002) ||
					(serviceName == "rabbitmq" && servicePort == 15672)
				if sensitiveConsole && protocol != "https" {
					return fmt.Errorf("domains[%d].routes[%d] database/message management console must use HTTPS and a TLS certificate", index, routeIndex)
				}
			}
			if accessType == "ip" && secureProtocol {
				return fmt.Errorf("domains[%d] load balancer address access only supports HTTP, WebSocket or gRPC without edge TLS", index)
			}
			if boolValue(domain["tls_enabled"]) != secureProtocol {
				return fmt.Errorf("domains[%d].tls_enabled must match its protocol", index)
			}
			backendProtocol := strings.ToLower(strings.TrimSpace(stringValue(domain["backend_protocol"])))
			if grpcProtocol {
				if backendProtocol != "grpc" && backendProtocol != "grpcs" {
					return fmt.Errorf("domains[%d].backend_protocol must be grpc or grpcs for a gRPC route", index)
				}
			} else if backendProtocol != "" && backendProtocol != "http" && backendProtocol != "https" {
				return fmt.Errorf("domains[%d].backend_protocol must be http or https", index)
			}
			if !secureProtocol {
				continue
			}
			certificateRef := stringValue(domain["certificate_ref"])
			if _, found := certificateKeys[certificateRef]; !found {
				return fmt.Errorf("domains[%d] references unknown TLS certificate %q", index, certificateRef)
			}
		}
	}
	if alerting, valid := mapValue(doc["alerting"]); valid {
		deliveryPolicy := strings.ToLower(strings.TrimSpace(stringValue(alerting["delivery_policy"])))
		if deliveryPolicy == "" {
			deliveryPolicy = "core"
		}
		if deliveryPolicy != "core" && deliveryPolicy != "all" {
			return errors.New("alerting.delivery_policy must be core or all")
		}
		channels, ok := alerting["channels"].([]any)
		if !ok {
			return errors.New("alerting.channels must be an array")
		}
		allowedTypes := map[string]bool{"slack": true, "email": true, "webhook": true, "telegram": true, "dingtalk": true, "feishu": true, "lark": true, "wecom": true}
		for index, raw := range channels {
			channel, valid := mapValue(raw)
			if !valid || strings.TrimSpace(stringValue(channel["name"])) == "" || !allowedTypes[stringValue(channel["type"])] {
				return fmt.Errorf("alerting.channels[%d] has an invalid name or type", index)
			}
			address := strings.TrimSpace(stringValue(channel["address"]))
			// Backward compatibility for channels created before direct address
			// input was introduced. Their referenced Secret remains valid.
			if address == "" && strings.TrimSpace(stringValue(channel["secret_ref"])) != "" {
				continue
			}
			if stringValue(channel["type"]) == "email" {
				if parsed, err := mail.ParseAddress(address); err != nil || parsed.Address != address {
					return fmt.Errorf("alerting.channels[%d].address must be a valid email address", index)
				}
				continue
			}
			parsed, err := url.ParseRequestURI(address)
			if err != nil || (parsed.Scheme != "https" && parsed.Scheme != "http") || parsed.Host == "" {
				return fmt.Errorf("alerting.channels[%d].address must be an HTTP(S) URL", index)
			}
		}
	}
	return nil
}

func validateClickVisualStack(config map[string]any, doc Document) error {
	values, ok := mapValue(config["values"])
	if !ok {
		return errors.New("components.catalog.clickvisual_stack.values must be an object")
	}
	namespace := strings.TrimSpace(stringValue(values["namespace"]))
	if namespace == "" || len(namespace) > 63 || !kubernetesNamePattern.MatchString(namespace) {
		return errors.New("components.catalog.clickvisual_stack.values.namespace is invalid")
	}
	if configuredNamespace := strings.TrimSpace(stringValue(config["namespace"])); configuredNamespace != namespace {
		return errors.New("ClickVisual 日志平台 Helm Namespace 与组件 Namespace 必须一致")
	}
	configuredNamespaces, _ := mapValue(doc["namespaces"])
	if _, exists := configuredNamespaces[namespace]; !exists {
		return fmt.Errorf("ClickVisual 日志平台 Namespace %q 必须先加入环境 Namespaces 配置", namespace)
	}
	if err := validateClickVisualCollection(values); err != nil {
		return err
	}
	for _, key := range []string{"kafka", "clickhouse", "mysql"} {
		subcomponent, valid := mapValue(values[key])
		if !valid {
			return fmt.Errorf("components.catalog.clickvisual_stack.values.%s is required", key)
		}
		storage, valid := mapValue(subcomponent["storage"])
		if !valid {
			return fmt.Errorf("components.catalog.clickvisual_stack.values.%s.storage is required", key)
		}
		className := strings.TrimSpace(stringValue(storage["className"]))
		if className == "" || len(className) > 63 || !kubernetesNamePattern.MatchString(className) {
			return fmt.Errorf("components.catalog.clickvisual_stack.values.%s.storage.className is invalid", key)
		}
		match := storageQuantityPattern.FindStringSubmatch(stringValue(storage["size"]))
		if len(match) != 2 {
			return fmt.Errorf("components.catalog.clickvisual_stack.values.%s.storage.size must use Gi units", key)
		}
		size, err := strconv.Atoi(match[1])
		if err != nil || size < 1 || size > 16384 {
			return fmt.Errorf("components.catalog.clickvisual_stack.values.%s.storage.size must be between 1Gi and 16384Gi", key)
		}
		if key == "kafka" {
			if rawClaims, exists := storage["activeClaims"]; exists {
				claims, valid := rawClaims.([]any)
				if !valid {
					return errors.New("components.catalog.clickvisual_stack.values.kafka.storage.activeClaims must be an array")
				}
				for index, rawClaim := range claims {
					claim := strings.TrimSpace(stringValue(rawClaim))
					if claim != "" && (len(claim) > 63 || !kubernetesNamePattern.MatchString(claim)) {
						return fmt.Errorf("components.catalog.clickvisual_stack.values.kafka.storage.activeClaims[%d] is invalid", index)
					}
				}
			}
			continue
		}
		activeClaim := strings.TrimSpace(stringValue(storage["activeClaim"]))
		if activeClaim != "" && (len(activeClaim) > 63 || !kubernetesNamePattern.MatchString(activeClaim)) {
			return fmt.Errorf("components.catalog.clickvisual_stack.values.%s.storage.activeClaim is invalid", key)
		}
	}
	kafka, _ := mapValue(values["kafka"])
	replicas := intValue(kafka["replicas"])
	if replicas != 1 && replicas != 3 {
		return errors.New("ClickVisual Kafka replicas must be 1 or 3")
	}
	if kafkaStorage, valid := mapValue(kafka["storage"]); valid {
		if claims, valid := kafkaStorage["activeClaims"].([]any); valid && len(claims) > replicas {
			return errors.New("ClickVisual Kafka activeClaims cannot exceed replicas")
		}
	}
	clickhouse, _ := mapValue(values["clickhouse"])
	retention := intValue(clickhouse["retentionDays"])
	if retention < 1 || retention > 3650 {
		return errors.New("ClickVisual ClickHouse retentionDays must be between 1 and 3650")
	}
	storage, _ := mapValue(values["storage"])
	safety := intValue(storage["shrinkSafetyPercent"])
	if safety < 10 || safety > 100 {
		return errors.New("ClickVisual storage shrinkSafetyPercent must be between 10 and 100")
	}
	components, _ := mapValue(doc["components"])
	addons, _ := mapValue(components["eks_addons"])
	if !boolValue(addons["ebs_csi_driver"]) {
		return errors.New("ClickVisual 日志平台持久化需要 EBS CSI EKS Add-on")
	}
	return nil
}

func validateClickVisualCollection(values map[string]any) error {
	return validateLogCollection("clickvisual_stack", values)
}

func validateLogCollection(componentKey string, values map[string]any) error {
	collection, valid := mapValue(values["collection"])
	if !valid {
		return fmt.Errorf("components.catalog.%s.values.collection must be an object", componentKey)
	}
	for _, field := range []string{"includeNamespaces", "excludeNamespaces", "includeServices", "excludeServices"} {
		raw, exists := collection[field]
		if !exists {
			continue
		}
		items, valid := raw.([]any)
		if !valid {
			return fmt.Errorf("components.catalog.%s.values.collection.%s must be an array", componentKey, field)
		}
		if len(items) > 100 {
			return fmt.Errorf("components.catalog.%s.values.collection.%s cannot contain more than 100 entries", componentKey, field)
		}
		for index, item := range items {
			name, valid := item.(string)
			name = strings.TrimSpace(name)
			if !valid || name == "" || len(name) > 63 || !kubernetesNamePattern.MatchString(name) {
				return fmt.Errorf("components.catalog.%s.values.collection.%s[%d] must be a Kubernetes name", componentKey, field, index)
			}
		}
	}
	return nil
}

func validateEFKStack(config map[string]any, doc Document) error {
	values, ok := mapValue(config["values"])
	if !ok {
		return errors.New("components.catalog.efk_stack.values must be an object")
	}
	namespace := strings.TrimSpace(stringValue(values["namespace"]))
	if namespace == "" || len(namespace) > 63 || !kubernetesNamePattern.MatchString(namespace) {
		return errors.New("components.catalog.efk_stack.values.namespace is invalid")
	}
	if configuredNamespace := strings.TrimSpace(stringValue(config["namespace"])); configuredNamespace != namespace {
		return errors.New("EFK 日志系统 Helm Namespace 与组件 Namespace 必须一致")
	}
	configuredNamespaces, _ := mapValue(doc["namespaces"])
	if _, exists := configuredNamespaces[namespace]; !exists {
		return fmt.Errorf("EFK 日志系统 Namespace %q 必须先加入环境 Namespaces 配置", namespace)
	}
	if err := validateLogCollection("efk_stack", values); err != nil {
		return err
	}
	elasticsearch, valid := mapValue(values["elasticsearch"])
	if !valid {
		return errors.New("components.catalog.efk_stack.values.elasticsearch is required")
	}
	storage, valid := mapValue(elasticsearch["storage"])
	if !valid {
		return errors.New("components.catalog.efk_stack.values.elasticsearch.storage is required")
	}
	className := strings.TrimSpace(stringValue(storage["className"]))
	if className == "" || len(className) > 63 || !kubernetesNamePattern.MatchString(className) {
		return errors.New("components.catalog.efk_stack.values.elasticsearch.storage.className is invalid")
	}
	match := storageQuantityPattern.FindStringSubmatch(stringValue(storage["size"]))
	if len(match) != 2 {
		return errors.New("components.catalog.efk_stack.values.elasticsearch.storage.size must use Gi units")
	}
	size, err := strconv.Atoi(match[1])
	if err != nil || size < 10 || size > 16384 {
		return errors.New("components.catalog.efk_stack.values.elasticsearch.storage.size must be between 10Gi and 16384Gi")
	}
	retention := intValue(elasticsearch["retentionDays"])
	if retention < 1 || retention > 3650 {
		return errors.New("EFK Elasticsearch retentionDays must be between 1 and 3650")
	}
	javaOpts := strings.TrimSpace(stringValue(elasticsearch["javaOpts"]))
	if javaOpts == "" || len(javaOpts) > 100 || strings.ContainsAny(javaOpts, "\n\r") {
		return errors.New("components.catalog.efk_stack.values.elasticsearch.javaOpts is invalid")
	}
	xms, xmx, err := parseJavaHeapBytes(javaOpts)
	if err != nil {
		return fmt.Errorf("EFK Elasticsearch JVM Heap 参数无效: %w", err)
	}
	if xms != xmx {
		return errors.New("EFK Elasticsearch JVM Heap 的 -Xms 与 -Xmx 必须相同")
	}
	resources, valid := mapValue(elasticsearch["resources"])
	if !valid {
		return errors.New("components.catalog.efk_stack.values.elasticsearch.resources is required")
	}
	limits, valid := mapValue(resources["limits"])
	if !valid {
		return errors.New("components.catalog.efk_stack.values.elasticsearch.resources.limits is required")
	}
	memoryLimit, err := parseMemoryQuantityBytes(strings.TrimSpace(stringValue(limits["memory"])))
	if err != nil {
		return fmt.Errorf("EFK Elasticsearch 容器内存上限无效: %w", err)
	}
	if xmx > memoryLimit/2 {
		return fmt.Errorf("EFK Elasticsearch JVM Heap 不能超过容器内存上限的 50%%（当前 Heap %s，容器内存 %s）", formatJavaHeap(xmx), stringValue(limits["memory"]))
	}
	components, _ := mapValue(doc["components"])
	addons, _ := mapValue(components["eks_addons"])
	if !boolValue(addons["ebs_csi_driver"]) {
		return errors.New("EFK 日志系统持久化需要 EBS CSI EKS Add-on")
	}
	return nil
}

func parseJavaHeapBytes(value string) (int64, int64, error) {
	matches := javaHeapOptionPattern.FindAllStringSubmatch(value, -1)
	var xms, xmx int64
	for _, match := range matches {
		if len(match) != 4 {
			continue
		}
		amount, err := strconv.ParseInt(match[2], 10, 64)
		if err != nil {
			return 0, 0, errors.New("Heap 数值超出范围")
		}
		multiplier := int64(1)
		switch strings.ToLower(match[3]) {
		case "k":
			multiplier = 1 << 10
		case "m":
			multiplier = 1 << 20
		case "g":
			multiplier = 1 << 30
		case "t":
			multiplier = 1 << 40
		}
		if amount > (int64(^uint64(0)>>1))/multiplier {
			return 0, 0, errors.New("Heap 数值超出范围")
		}
		bytes := amount * multiplier
		switch strings.ToLower(match[1]) {
		case "s":
			if xms != 0 {
				return 0, 0, errors.New("-Xms 只能配置一次")
			}
			xms = bytes
		case "x":
			if xmx != 0 {
				return 0, 0, errors.New("-Xmx 只能配置一次")
			}
			xmx = bytes
		}
	}
	if xms == 0 || xmx == 0 {
		return 0, 0, errors.New("必须同时包含 -Xms 和 -Xmx，例如 -Xms2g -Xmx2g")
	}
	return xms, xmx, nil
}

func parseMemoryQuantityBytes(value string) (int64, error) {
	match := memoryQuantityPattern.FindStringSubmatch(value)
	if len(match) != 3 {
		return 0, errors.New("必须使用 Mi 或 Gi 等 Kubernetes 内存单位")
	}
	amount, err := strconv.ParseInt(match[1], 10, 64)
	if err != nil {
		return 0, errors.New("内存数值超出范围")
	}
	unit := strings.ToLower(match[2])
	multiplier := int64(1)
	switch unit {
	case "k", "ki":
		multiplier = 1 << 10
	case "m", "mi":
		multiplier = 1 << 20
	case "g", "gi":
		multiplier = 1 << 30
	case "t", "ti":
		multiplier = 1 << 40
	}
	if amount > (int64(^uint64(0)>>1))/multiplier {
		return 0, errors.New("内存数值超出范围")
	}
	return amount * multiplier, nil
}

func formatJavaHeap(bytes int64) string {
	if bytes%(1<<30) == 0 {
		return fmt.Sprintf("%dg", bytes/(1<<30))
	}
	return fmt.Sprintf("%dm", bytes/(1<<20))
}

func normalizeLegacyEFKHeap(values map[string]any) {
	elasticsearch, valid := mapValue(values["elasticsearch"])
	if !valid {
		return
	}
	resources, valid := mapValue(elasticsearch["resources"])
	if !valid {
		return
	}
	limits, valid := mapValue(resources["limits"])
	if !valid {
		return
	}
	memoryLimit, err := parseMemoryQuantityBytes(strings.TrimSpace(stringValue(limits["memory"])))
	if err != nil || memoryLimit < 2*(1<<20) {
		return
	}
	javaOpts := strings.TrimSpace(stringValue(elasticsearch["javaOpts"]))
	xms, xmx, err := parseJavaHeapBytes(javaOpts)
	if err != nil || (xms == xmx && xmx <= memoryLimit/2) {
		return
	}
	safeHeap := formatJavaHeap(memoryLimit / 2)
	elasticsearch["javaOpts"] = javaHeapOptionPattern.ReplaceAllStringFunc(javaOpts, func(option string) string {
		if strings.HasPrefix(strings.ToLower(option), "-xms") {
			return "-Xms" + safeHeap
		}
		return "-Xmx" + safeHeap
	})
}

func enabledCatalogComponent(catalog map[string]any, key string) bool {
	config, ok := mapValue(catalog[key])
	return ok && boolValue(config["enabled"])
}

func validateHigressNLB(config map[string]any, doc Document) error {
	nlb, valid := mapValue(config["nlb"])
	if !valid {
		return errors.New("components.catalog.higress.nlb must be an object")
	}
	securityGroupMode := strings.TrimSpace(stringValue(nlb["security_group_mode"]))
	if securityGroupMode != "managed" && securityGroupMode != "custom" && securityGroupMode != "managed_plus_custom" {
		return errors.New("components.catalog.higress.nlb.security_group_mode must be managed, custom, or managed_plus_custom")
	}
	deploymentTarget, _ := mapValue(doc["deployment_target"])
	if stringValue(deploymentTarget["type"]) == "existing_eks" && securityGroupMode != "managed" {
		return errors.New("custom Higress NLB security groups require a platform-managed EKS environment")
	}
	networkConfig, _ := mapValue(doc["network"])
	if securityGroupMode != "managed" && stringValue(networkConfig["mode"]) != "existing" {
		return errors.New("custom Higress NLB security groups require network.mode=existing because AWS security groups cannot be moved into a newly created VPC")
	}
	if _, valid := nlb["manage_backend_security_group_rules"].(bool); !valid {
		return errors.New("components.catalog.higress.nlb.manage_backend_security_group_rules must be a boolean")
	}
	scheme := strings.TrimSpace(stringValue(nlb["scheme"]))
	if scheme != "internet-facing" && scheme != "internal" {
		return errors.New("components.catalog.higress.nlb.scheme must be internet-facing or internal")
	}
	allowedPorts, valid := nlb["allowed_ports"].([]any)
	if !valid || len(allowedPorts) == 0 || len(allowedPorts) > 2 {
		return errors.New("components.catalog.higress.nlb.allowed_ports must contain port 80, port 443, or both")
	}
	seenPorts := make(map[int]struct{}, len(allowedPorts))
	for index, raw := range allowedPorts {
		port := intValue(raw)
		if port != 80 && port != 443 {
			return fmt.Errorf("components.catalog.higress.nlb.allowed_ports[%d] must be 80 or 443", index)
		}
		if _, duplicate := seenPorts[port]; duplicate {
			return fmt.Errorf("components.catalog.higress.nlb.allowed_ports contains duplicate port %d", port)
		}
		seenPorts[port] = struct{}{}
	}
	securityGroupIDs, valid := nlb["security_group_ids"].([]any)
	if !valid {
		return errors.New("components.catalog.higress.nlb.security_group_ids must be a list")
	}
	if len(securityGroupIDs) > 4 {
		return errors.New("components.catalog.higress.nlb.security_group_ids cannot contain more than 4 entries")
	}
	securityGroupPattern := regexp.MustCompile(`^sg-[0-9a-f]{8,17}$`)
	seenSecurityGroups := make(map[string]struct{}, len(securityGroupIDs))
	for index, raw := range securityGroupIDs {
		securityGroupID := strings.ToLower(strings.TrimSpace(stringValue(raw)))
		if !securityGroupPattern.MatchString(securityGroupID) {
			return fmt.Errorf("components.catalog.higress.nlb.security_group_ids[%d] is invalid", index)
		}
		if _, duplicate := seenSecurityGroups[securityGroupID]; duplicate {
			return fmt.Errorf("components.catalog.higress.nlb.security_group_ids contains duplicate %q", securityGroupID)
		}
		seenSecurityGroups[securityGroupID] = struct{}{}
	}
	if securityGroupMode != "managed" && len(securityGroupIDs) == 0 {
		return errors.New("components.catalog.higress.nlb.security_group_ids must contain at least one security group in custom mode")
	}
	cidrs, valid := nlb["allowed_cidrs"].([]any)
	if !valid || (securityGroupMode != "custom" && len(cidrs) == 0) {
		return errors.New("components.catalog.higress.nlb.allowed_cidrs must contain at least one source CIDR")
	}
	// Each source creates one rule for 80 and one for 443. Keep the default AWS
	// security-group quota usable without requiring an account quota increase.
	if len(cidrs) > 30 {
		return errors.New("components.catalog.higress.nlb.allowed_cidrs cannot contain more than 30 entries")
	}
	seen := make(map[string]struct{}, len(cidrs))
	for index, raw := range cidrs {
		cidr := strings.TrimSpace(stringValue(raw))
		ip, cidrNetwork, err := net.ParseCIDR(cidr)
		if err != nil || ip.To4() == nil || !ip.Equal(cidrNetwork.IP) {
			return fmt.Errorf("components.catalog.higress.nlb.allowed_cidrs[%d] is invalid", index)
		}
		if _, duplicate := seen[cidr]; duplicate {
			return fmt.Errorf("components.catalog.higress.nlb.allowed_cidrs contains duplicate %q", cidr)
		}
		seen[cidr] = struct{}{}
	}
	policy := strings.TrimSpace(stringValue(nlb["external_traffic_policy"]))
	if policy != "Local" && policy != "Cluster" {
		return errors.New("components.catalog.higress.nlb.external_traffic_policy must be Local or Cluster")
	}
	idleTimeout := intValue(nlb["idle_timeout_seconds"])
	if idleTimeout < 60 || idleTimeout > 6000 {
		return errors.New("components.catalog.higress.nlb.idle_timeout_seconds must be between 60 and 6000")
	}
	return nil
}

func validateDatabaseManagementComponent(key string, config map[string]any) error {
	values, ok := mapValue(config["values"])
	if !ok {
		return fmt.Errorf("components.catalog.%s.values must be an object", key)
	}
	image, ok := mapValue(values["image"])
	if !ok || strings.TrimSpace(stringValue(image["repository"])) == "" || strings.TrimSpace(stringValue(image["tag"])) == "" {
		return fmt.Errorf("components.catalog.%s image repository and tag are required", key)
	}
	persistence, ok := mapValue(values["persistence"])
	if !ok || !boolValue(persistence["enabled"]) {
		return fmt.Errorf("components.catalog.%s requires persistent storage", key)
	}
	className := strings.TrimSpace(stringValue(persistence["storageClass"]))
	if className == "" || !kubernetesNamePattern.MatchString(className) || len(className) > 63 {
		return fmt.Errorf("components.catalog.%s persistence.storageClass is invalid", key)
	}
	if match := storageQuantityPattern.FindStringSubmatch(stringValue(persistence["size"])); len(match) != 2 {
		return fmt.Errorf("components.catalog.%s persistence.size must use Gi units, for example 20Gi", key)
	}
	if key == "bytebase" {
		admin, ok := mapValue(values["admin"])
		email := strings.TrimSpace(stringValue(admin["email"]))
		if !ok || email == "" || !strings.Contains(email, "@") {
			return errors.New("components.catalog.bytebase.values.admin.email must be a valid email address")
		}
	} else {
		auth, ok := mapValue(values["basicAuth"])
		username := strings.TrimSpace(stringValue(auth["username"]))
		if !ok || username == "" {
			return fmt.Errorf("components.catalog.%s.values.basicAuth.username is required", key)
		}
		if strings.ContainsAny(username, ":\r\n") {
			return fmt.Errorf("components.catalog.%s.values.basicAuth.username cannot contain colon or line breaks", key)
		}
		if key == "etcd_workbench" {
			settings, ok := mapValue(values["settings"])
			timeout := intValue(settings["etcdExecuteTimeoutMillis"])
			if !ok || timeout < 1000 || timeout > 60000 {
				return errors.New("components.catalog.etcd_workbench.values.settings.etcdExecuteTimeoutMillis must be between 1000 and 60000")
			}
		}
	}
	return nil
}

func validateDataServiceComponent(key string, config map[string]any) error {
	values, ok := mapValue(config["values"])
	if !ok || stringValue(values["engine"]) != key {
		return fmt.Errorf("components.catalog.%s.values.engine must be %s", key, key)
	}
	image, ok := mapValue(values["image"])
	if !ok || strings.TrimSpace(stringValue(image["repository"])) == "" || strings.TrimSpace(stringValue(image["tag"])) == "" {
		return fmt.Errorf("components.catalog.%s image repository and tag are required", key)
	}
	auth, ok := mapValue(values["auth"])
	if !ok || strings.TrimSpace(stringValue(auth["username"])) == "" {
		return fmt.Errorf("components.catalog.%s auth.username is required", key)
	}
	storage, ok := mapValue(values["storage"])
	if !ok || !boolValue(storage["enabled"]) {
		return fmt.Errorf("components.catalog.%s requires persistent storage", key)
	}
	className := stringValue(storage["className"])
	if className == "" || !kubernetesNamePattern.MatchString(className) || len(className) > 63 {
		return fmt.Errorf("components.catalog.%s storage.className is invalid", key)
	}
	if match := storageQuantityPattern.FindStringSubmatch(stringValue(storage["size"])); len(match) != 2 {
		return fmt.Errorf("components.catalog.%s storage.size must use Gi units, for example 20Gi", key)
	}
	return nil
}

func validateLokiStorage(config, components map[string]any) error {
	values, ok := mapValue(config["values"])
	if !ok {
		return errors.New("components.catalog.loki.values must be an object")
	}
	loki, ok := mapValue(values["loki"])
	if !ok {
		return errors.New("components.catalog.loki.values.loki is required")
	}
	storage, ok := mapValue(loki["storage"])
	if !ok || stringValue(storage["type"]) == "" {
		return errors.New("components.catalog.loki.values.loki.storage.type is required")
	}
	if stringValue(storage["type"]) != "filesystem" {
		return nil
	}
	singleBinary, ok := mapValue(values["singleBinary"])
	if !ok {
		return errors.New("Loki filesystem storage requires values.singleBinary")
	}
	persistence, ok := mapValue(singleBinary["persistence"])
	if !ok || !boolValue(persistence["enabled"]) {
		return errors.New("Loki filesystem storage requires an EBS persistent volume; enable singleBinary.persistence.enabled")
	}
	storageClass := stringValue(persistence["storageClass"])
	if storageClass == "" || !kubernetesNamePattern.MatchString(storageClass) || len(storageClass) > 63 {
		return errors.New("Loki persistence storageClass must be a valid Kubernetes StorageClass name")
	}
	size := stringValue(persistence["size"])
	match := storageQuantityPattern.FindStringSubmatch(size)
	if len(match) != 2 {
		return errors.New("Loki persistence size must use Gi units, for example 20Gi")
	}
	quantity, err := strconv.Atoi(match[1])
	if err != nil || quantity < 1 || quantity > 16384 {
		return errors.New("Loki persistence size must be between 1Gi and 16384Gi")
	}
	limits, ok := mapValue(loki["limits_config"])
	if !ok || !lokiRetentionPattern.MatchString(stringValue(limits["retention_period"])) {
		return errors.New("Loki retention_period must use hours, for example 168h")
	}
	addons, ok := mapValue(components["eks_addons"])
	if !ok || !boolValue(addons["ebs_csi_driver"]) {
		return errors.New("Loki EBS persistence requires the EBS CSI EKS add-on")
	}
	return nil
}

func validateOpenTelemetryCollectorStorage(config, components map[string]any) error {
	values, ok := mapValue(config["values"])
	if !ok {
		return errors.New("OpenTelemetry Collector values must be an object")
	}
	storage, ok := mapValue(values["storage"])
	if !ok || !boolValue(storage["enabled"]) {
		return errors.New("OpenTelemetry Collector persistent queue storage must be enabled")
	}
	className := strings.TrimSpace(stringValue(storage["className"]))
	if className == "" || !kubernetesNamePattern.MatchString(className) || len(className) > 63 {
		return errors.New("OpenTelemetry Collector storage.className must be a valid Kubernetes StorageClass name")
	}
	initialSize, err := storageQuantityGiValue(stringValue(storage["initialSize"]))
	if err != nil || initialSize < 1 || initialSize > 16384 {
		return errors.New("OpenTelemetry Collector storage.initialSize must be between 1Gi and 16384Gi")
	}
	if expanded := strings.TrimSpace(stringValue(storage["expandedSize"])); expanded != "" {
		expandedSize, expandedErr := storageQuantityGiValue(expanded)
		if expandedErr != nil || expandedSize < initialSize || expandedSize > 16384 {
			return errors.New("OpenTelemetry Collector storage.expandedSize must use Gi units and cannot be smaller than initialSize")
		}
	}
	queueSize := intValue(storage["queueSize"])
	if queueSize < 1 || queueSize > 1000000 {
		return errors.New("OpenTelemetry Collector storage.queueSize must be between 1 and 1000000 batches")
	}
	addons, ok := mapValue(components["eks_addons"])
	if !ok || !boolValue(addons["ebs_csi_driver"]) {
		return errors.New("OpenTelemetry Collector persistent queue storage requires the EBS CSI EKS add-on")
	}
	catalog, _ := mapValue(components["catalog"])
	destinations, ok := mapValue(values["destinations"])
	if !ok {
		return errors.New("OpenTelemetry Collector destinations must be an object")
	}
	for destination, dependency := range map[string]string{"jaeger": "jaeger", "tempo": "tempo", "prometheus": "prometheus", "loki": "loki"} {
		target, valid := mapValue(destinations[destination])
		if valid && boolValue(target["enabled"]) && !enabledCatalogComponent(catalog, dependency) {
			return fmt.Errorf("OpenTelemetry Collector %s exporter requires the %s component", destination, dependency)
		}
	}
	elasticsearchDestination, _ := mapValue(destinations["elasticsearch"])
	elasticsearch, valid := mapValue(values["elasticsearch"])
	if !valid {
		return errors.New("OpenTelemetry Collector elasticsearch configuration is required")
	}
	if boolValue(elasticsearchDestination["enabled"]) && !boolValue(elasticsearch["enabled"]) {
		return errors.New("OpenTelemetry Collector Elasticsearch exporter requires its dedicated Elasticsearch storage")
	}
	if boolValue(elasticsearch["enabled"]) {
		if err := validateOpenTelemetryElasticsearch(elasticsearch); err != nil {
			return err
		}
	}
	jaegerDestination, _ := mapValue(destinations["jaeger"])
	tempoDestination, _ := mapValue(destinations["tempo"])
	if !boolValue(jaegerDestination["enabled"]) && !boolValue(tempoDestination["enabled"]) {
		return errors.New("OpenTelemetry Collector requires at least one trace backend: Jaeger or Tempo")
	}
	agent, ok := mapValue(values["agent"])
	if !ok {
		return errors.New("OpenTelemetry Collector agent configuration is required")
	}
	if boolValue(agent["enabled"]) {
		logs, valid := mapValue(agent["logs"])
		if !valid {
			return errors.New("OpenTelemetry Collector agent.logs must be an object")
		}
		for _, field := range []string{"includeNamespaces", "excludeNamespaces", "includeServices", "excludeServices"} {
			items, valid := logs[field].([]any)
			if !valid {
				return fmt.Errorf("OpenTelemetry Collector agent.logs.%s must be an array", field)
			}
			if len(items) > 100 {
				return fmt.Errorf("OpenTelemetry Collector agent.logs.%s cannot contain more than 100 entries", field)
			}
			for index, item := range items {
				name, valid := item.(string)
				name = strings.TrimSpace(name)
				if !valid || name == "" || len(name) > 63 || !kubernetesNamePattern.MatchString(name) {
					return fmt.Errorf("OpenTelemetry Collector agent.logs.%s[%d] must be a Kubernetes name", field, index)
				}
			}
		}
	}
	return nil
}

func validateOpenTelemetryElasticsearch(config map[string]any) error {
	mode := strings.ToLower(strings.TrimSpace(stringValue(config["mode"])))
	replicas := intValue(config["replicas"])
	if mode != "standalone" && mode != "cluster" {
		return errors.New("OpenTelemetry Elasticsearch mode must be standalone or cluster")
	}
	if mode == "standalone" && replicas != 1 {
		return errors.New("OpenTelemetry Elasticsearch standalone mode requires exactly 1 node")
	}
	if mode == "cluster" && (replicas < 3 || replicas > 9 || replicas%2 == 0) {
		return errors.New("OpenTelemetry Elasticsearch cluster mode requires 3, 5, 7 or 9 nodes")
	}
	image, ok := mapValue(config["image"])
	if !ok || strings.TrimSpace(stringValue(image["repository"])) == "" || strings.TrimSpace(stringValue(image["tag"])) == "" {
		return errors.New("OpenTelemetry Elasticsearch image repository and tag are required")
	}
	javaOpts := strings.TrimSpace(stringValue(config["javaOpts"]))
	if javaOpts == "" || len(javaOpts) > 100 || strings.ContainsAny(javaOpts, "\n\r") {
		return errors.New("OpenTelemetry Elasticsearch javaOpts is invalid")
	}
	xms, xmx, heapErr := parseJavaHeapBytes(javaOpts)
	if heapErr != nil {
		return fmt.Errorf("OpenTelemetry Elasticsearch JVM Heap is invalid: %w", heapErr)
	}
	if xms != xmx {
		return errors.New("OpenTelemetry Elasticsearch JVM Heap requires equal -Xms and -Xmx values")
	}
	resources, valid := mapValue(config["resources"])
	if !valid {
		return errors.New("OpenTelemetry Elasticsearch resources are required")
	}
	limits, valid := mapValue(resources["limits"])
	if !valid {
		return errors.New("OpenTelemetry Elasticsearch resources.limits are required")
	}
	memoryLimit, memoryErr := parseMemoryQuantityBytes(strings.TrimSpace(stringValue(limits["memory"])))
	if memoryErr != nil {
		return fmt.Errorf("OpenTelemetry Elasticsearch memory limit is invalid: %w", memoryErr)
	}
	if xmx > memoryLimit/2 {
		return fmt.Errorf("OpenTelemetry Elasticsearch JVM Heap cannot exceed 50%% of the memory limit (Heap %s, memory %s)", formatJavaHeap(xmx), stringValue(limits["memory"]))
	}
	storage, ok := mapValue(config["storage"])
	if !ok {
		return errors.New("OpenTelemetry Elasticsearch storage configuration is required")
	}
	className := strings.TrimSpace(stringValue(storage["className"]))
	if className == "" || !kubernetesNamePattern.MatchString(className) || len(className) > 63 {
		return errors.New("OpenTelemetry Elasticsearch storage.className must be a valid Kubernetes StorageClass name")
	}
	initialSize, err := storageQuantityGiValue(stringValue(storage["initialSize"]))
	if err != nil || initialSize < 10 || initialSize > 16384 {
		return errors.New("OpenTelemetry Elasticsearch storage.initialSize must be between 10Gi and 16384Gi")
	}
	expandedSize := strings.TrimSpace(stringValue(storage["expandedSize"]))
	if expandedSize != "" {
		expandedGi, expandedErr := storageQuantityGiValue(expandedSize)
		if expandedErr != nil || expandedGi < initialSize || expandedGi > 16384 {
			return errors.New("OpenTelemetry Elasticsearch storage.expandedSize must be empty or between initialSize and 16384Gi")
		}
	}
	return nil
}

func validateJaegerStack(config, components map[string]any) error {
	values, ok := mapValue(config["values"])
	if !ok {
		return errors.New("Jaeger values must be an object")
	}
	auth, ok := mapValue(values["basicAuth"])
	if !ok || !boolValue(auth["enabled"]) || strings.TrimSpace(stringValue(auth["username"])) == "" {
		return errors.New("Jaeger Query UI basic authentication must be enabled with a username")
	}
	storage, ok := mapValue(values["storage"])
	if !ok {
		return errors.New("Jaeger storage configuration is required")
	}
	backend := strings.ToLower(strings.TrimSpace(stringValue(storage["backend"])))
	if backend != "badger" && backend != "elasticsearch" {
		return errors.New("Jaeger storage.backend must be badger or elasticsearch")
	}
	catalog, _ := mapValue(components["catalog"])
	if !enabledCatalogComponent(catalog, "prometheus") {
		return errors.New("Jaeger requires Prometheus + Grafana for component health metrics")
	}
	if backend == "elasticsearch" {
		collector, valid := mapValue(catalog["opentelemetry_collector"])
		if !valid || !boolValue(collector["enabled"]) {
			return errors.New("Jaeger Elasticsearch storage requires OpenTelemetry Collector")
		}
		collectorValues, valid := mapValue(collector["values"])
		if !valid {
			return errors.New("Jaeger Elasticsearch storage requires OpenTelemetry Collector values")
		}
		collectorElasticsearch, valid := mapValue(collectorValues["elasticsearch"])
		if !valid || !boolValue(collectorElasticsearch["enabled"]) {
			return errors.New("Jaeger Elasticsearch storage requires the dedicated OpenTelemetry Elasticsearch instance")
		}
		elasticsearch, valid := mapValue(storage["elasticsearch"])
		if !valid || strings.TrimSpace(stringValue(elasticsearch["endpoint"])) == "" {
			return errors.New("Jaeger Elasticsearch endpoint is required")
		}
		retentionDays := intValue(elasticsearch["retentionDays"])
		if retentionDays < 1 || retentionDays > 3650 {
			return errors.New("Jaeger Elasticsearch retentionDays must be between 1 and 3650")
		}
		indexCleaner, valid := mapValue(elasticsearch["indexCleaner"])
		if !valid || !boolValue(indexCleaner["enabled"]) || strings.TrimSpace(stringValue(indexCleaner["schedule"])) == "" {
			return errors.New("Jaeger Elasticsearch index cleaner must be enabled with a schedule")
		}
		return nil
	}
	if stringValue(config["deployment_mode"]) != "standalone" || intValue(config["replicas"]) != 1 {
		return errors.New("Jaeger Badger storage supports standalone mode only; select Elasticsearch before using cluster mode")
	}
	className := strings.TrimSpace(stringValue(storage["className"]))
	if className == "" || !kubernetesNamePattern.MatchString(className) || len(className) > 63 {
		return errors.New("Jaeger storage.className must be a valid Kubernetes StorageClass name")
	}
	initialSize, err := storageQuantityGiValue(stringValue(storage["initialSize"]))
	if err != nil || initialSize < 1 || initialSize > 16384 {
		return errors.New("Jaeger storage.initialSize must be between 1Gi and 16384Gi")
	}
	if !lokiRetentionPattern.MatchString(stringValue(storage["retention"])) {
		return errors.New("Jaeger Badger retention must use hours, for example 168h")
	}
	addons, ok := mapValue(components["eks_addons"])
	if !ok || !boolValue(addons["ebs_csi_driver"]) {
		return errors.New("Jaeger Badger persistence requires the EBS CSI EKS add-on")
	}
	return nil
}

func validateTempoStorage(config, components map[string]any) error {
	values, ok := mapValue(config["values"])
	if !ok {
		return errors.New("Tempo values must be an object")
	}
	persistence, ok := mapValue(values["persistence"])
	if !ok || !boolValue(persistence["enabled"]) {
		return errors.New("Tempo requires persistent storage")
	}
	className := strings.TrimSpace(stringValue(persistence["storageClassName"]))
	if className == "" || !kubernetesNamePattern.MatchString(className) || len(className) > 63 {
		return errors.New("Tempo persistence.storageClassName must be a valid Kubernetes StorageClass name")
	}
	size, err := storageQuantityGiValue(stringValue(persistence["size"]))
	if err != nil || size < 1 || size > 16384 {
		return errors.New("Tempo persistence.size must be between 1Gi and 16384Gi")
	}
	tempo, ok := mapValue(values["tempo"])
	if !ok || !lokiRetentionPattern.MatchString(stringValue(tempo["retention"])) {
		return errors.New("Tempo retention must use hours, for example 168h")
	}
	addons, ok := mapValue(components["eks_addons"])
	if !ok || !boolValue(addons["ebs_csi_driver"]) {
		return errors.New("Tempo persistence requires the EBS CSI EKS add-on")
	}
	return nil
}

func storageQuantityGiValue(value string) (int, error) {
	match := storageQuantityPattern.FindStringSubmatch(strings.TrimSpace(value))
	if len(match) != 2 {
		return 0, errors.New("storage size must use Gi units")
	}
	return strconv.Atoi(match[1])
}

func validateStatefulService(path string, config map[string]any) error {
	if strings.TrimSpace(stringValue(config["image"])) == "" {
		return fmt.Errorf("%s.image is required", path)
	}
	storageClass := stringValue(config["storage_class"])
	if storageClass == "" || !kubernetesNamePattern.MatchString(storageClass) || len(storageClass) > 63 {
		return fmt.Errorf("%s.storage_class must be a valid Kubernetes StorageClass name", path)
	}
	match := storageQuantityPattern.FindStringSubmatch(stringValue(config["storage_size"]))
	if len(match) != 2 {
		return fmt.Errorf("%s.storage_size must use Gi units, for example 20Gi", path)
	}
	quantity, err := strconv.Atoi(match[1])
	if err != nil || quantity < 1 || quantity > 16384 {
		return fmt.Errorf("%s.storage_size must be between 1Gi and 16384Gi", path)
	}
	if stringValue(config["deployment_mode"]) == "cluster" {
		replicas := intValue(config["replicas"])
		if replicas < 3 || replicas%2 == 0 {
			return fmt.Errorf("%s cluster mode requires an odd replica count of at least 3", path)
		}
	}
	if backup, ok := mapValue(config["backup"]); ok && boolValue(backup["enabled"]) {
		if strings.TrimSpace(stringValue(backup["schedule"])) == "" {
			return fmt.Errorf("%s.backup.schedule is required", path)
		}
		retention := intValue(backup["retention_days"])
		if retention < 1 || retention > 3650 {
			return fmt.Errorf("%s.backup.retention_days must be between 1 and 3650", path)
		}
	}
	return nil
}

func validateStatefulPlatformCapacity(doc Document, components, nodeGroups map[string]any) error {
	if IsExistingEKS(doc) {
		return nil
	}
	requiredReplicas := 0
	for _, key := range []string{"consul", "etcd"} {
		config, ok := mapValue(components[key])
		if !ok || !boolValue(config["enabled"]) || stringValue(config["deployment_mode"]) != "cluster" {
			continue
		}
		if replicas := intValue(config["replicas"]); replicas > requiredReplicas {
			requiredReplicas = replicas
		}
	}
	if requiredReplicas == 0 {
		return nil
	}

	minimumCapacity, desiredCapacity := 0, 0
	zones := make(map[string]bool)
	workloadScheduling := WorkloadSchedulingEnabled(doc)
	for _, raw := range nodeGroups {
		group, ok := mapValue(raw)
		if !ok {
			continue
		}
		if workloadScheduling {
			labels, _ := mapValue(group["labels"])
			if stringValue(labels["workload-class"]) != "platform" {
				continue
			}
		}
		minimumCapacity += intValue(group["min_size"])
		desiredCapacity += intValue(group["desired_size"])
		if groupZones, ok := group["availability_zones"].([]any); ok {
			for _, rawZone := range groupZones {
				zones[stringValue(rawZone)] = true
			}
		}
	}
	if minimumCapacity < requiredReplicas || desiredCapacity < requiredReplicas {
		return fmt.Errorf("Consul/etcd 集群最多需要 %d 个副本；承载运维组件的节点组 min_size 和 desired_size 均不得小于 %d（当前 min=%d, desired=%d）", requiredReplicas, requiredReplicas, minimumCapacity, desiredCapacity)
	}
	requiredZones := requiredReplicas
	if requiredZones > 3 {
		requiredZones = 3
	}
	if len(zones) < requiredZones {
		return fmt.Errorf("Consul/etcd 高可用集群需要至少 %d 个可用区，当前承载运维组件的节点组仅覆盖 %d 个", requiredZones, len(zones))
	}
	return nil
}

func validateDeploymentMode(path string, config map[string]any) error {
	mode := stringValue(config["deployment_mode"])
	if mode == "" {
		mode = "standalone"
	}
	if mode != "standalone" && mode != "cluster" {
		return fmt.Errorf("%s.deployment_mode must be standalone or cluster", path)
	}
	replicas := intValue(config["replicas"])
	if mode == "standalone" && replicas != 0 && replicas != 1 {
		return fmt.Errorf("%s standalone mode requires replicas=1", path)
	}
	if mode == "cluster" && (replicas < 2 || replicas > 20) {
		return fmt.Errorf("%s cluster mode requires replicas between 2 and 20", path)
	}
	return nil
}

func validateNetwork(network map[string]any, region string) error {
	mode := stringValue(network["mode"])
	if mode == "" {
		mode = "create"
	}
	if mode != "create" && mode != "existing" {
		return errors.New("network.mode must be create or existing")
	}
	vpcCIDR := stringValue(network["vpc_cidr"])
	if mode == "existing" {
		vpcCIDR = stringValue(network["existing_vpc_cidr"])
		if !vpcIDPattern.MatchString(stringValue(network["existing_vpc_id"])) {
			return errors.New("network.existing_vpc_id must be a valid VPC ID")
		}
		for _, key := range []string{"existing_workload_subnet_ids", "existing_data_subnet_ids"} {
			selected, ok := network[key].([]any)
			if !ok || len(selected) < 2 || len(selected) > 3 {
				return fmt.Errorf("network.%s must contain two or three subnet IDs", key)
			}
			seen := make(map[string]bool, len(selected))
			for _, raw := range selected {
				id := stringValue(raw)
				if !subnetIDPattern.MatchString(id) || seen[id] {
					return fmt.Errorf("network.%s contains invalid or duplicate subnet ID %q", key, id)
				}
				seen[id] = true
			}
		}
	}
	vpcIP, vpc, err := net.ParseCIDR(vpcCIDR)
	if err != nil || vpcIP.To4() == nil || !vpcIP.Equal(vpc.IP) {
		if mode == "existing" {
			return errors.New("network.existing_vpc_cidr must be a canonical CIDR")
		}
		return errors.New("network.vpc_cidr must be a canonical CIDR")
	}
	serviceIP, service, err := net.ParseCIDR(stringValue(network["service_ipv4_cidr"]))
	if err != nil || serviceIP.To4() == nil || !serviceIP.Equal(service.IP) {
		return errors.New("network.service_ipv4_cidr must be a canonical CIDR")
	}
	if vpc.Contains(service.IP) || service.Contains(vpc.IP) {
		return errors.New("VPC and Kubernetes service CIDRs must not overlap")
	}
	zones, ok := network["availability_zones"].([]any)
	if !ok || (mode == "create" && len(zones) != 3) || (mode == "existing" && (len(zones) < 2 || len(zones) > 3)) {
		return errors.New("network.availability_zones must contain three zones for a new VPC or two to three zones for an existing VPC")
	}
	zoneSet := make(map[string]bool, 3)
	for _, raw := range zones {
		zone := stringValue(raw)
		if len(zone) != len(region)+1 || !strings.HasPrefix(zone, region) || zone[len(zone)-1] < 'a' || zone[len(zone)-1] > 'z' || zoneSet[zone] {
			return fmt.Errorf("invalid or duplicate availability zone %q", zone)
		}
		zoneSet[zone] = true
	}
	if mode == "existing" {
		return nil
	}
	natGatewayMode := stringValue(network["nat_gateway_mode"])
	if natGatewayMode == "" {
		natGatewayMode = "when-private"
	}
	if natGatewayMode != "when-private" && natGatewayMode != "always" && natGatewayMode != "disabled" {
		return errors.New("network.nat_gateway_mode must be when-private, always or disabled")
	}
	for _, key := range []string{"workload_subnet_type", "data_subnet_type"} {
		if value := stringValue(network[key]); value != "public" && value != "private" {
			return fmt.Errorf("network.%s must be public or private", key)
		}
	}
	for _, key := range []string{"workload_subnet_zones", "data_subnet_zones"} {
		selected, ok := network[key].([]any)
		if !ok || len(selected) < 2 || len(selected) > 3 {
			return fmt.Errorf("network.%s must select two or three availability zones", key)
		}
		selectedSet := make(map[string]bool, len(selected))
		for _, raw := range selected {
			zone := stringValue(raw)
			if !zoneSet[zone] || selectedSet[zone] {
				return fmt.Errorf("network.%s contains invalid or duplicate zone %q", key, zone)
			}
			selectedSet[zone] = true
		}
	}
	subnets := make([]*net.IPNet, 0, 6)
	for _, groupName := range []string{"public_subnets", "private_subnets"} {
		group, ok := mapValue(network[groupName])
		if !ok || len(group) != 3 {
			return fmt.Errorf("network.%s must contain exactly three subnets", groupName)
		}
		for zone := range zoneSet {
			rawCIDR, exists := group[zone]
			ip, subnet, parseErr := net.ParseCIDR(stringValue(rawCIDR))
			vpcPrefix, vpcBits := vpc.Mask.Size()
			subnetPrefix, subnetBits := 0, 0
			if subnet != nil {
				subnetPrefix, subnetBits = subnet.Mask.Size()
			}
			if !exists || parseErr != nil || ip.To4() == nil || !ip.Equal(subnet.IP) || !vpc.Contains(subnet.IP) || subnetBits != vpcBits || subnetPrefix < vpcPrefix {
				return fmt.Errorf("network.%s.%s must be a canonical subnet inside the VPC", groupName, zone)
			}
			for _, existing := range subnets {
				if existing.Contains(subnet.IP) || subnet.Contains(existing.IP) {
					return fmt.Errorf("network subnet %s overlaps another subnet", subnet.String())
				}
			}
			subnets = append(subnets, subnet)
		}
	}
	return nil
}

func intValue(value any) int {
	switch number := value.(type) {
	case int:
		return number
	case int64:
		return int(number)
	case float64:
		converted := int(number)
		if number != float64(converted) {
			return -1
		}
		return converted
	default:
		return -1
	}
}

func floatValue(value any) (float64, bool) {
	switch number := value.(type) {
	case int:
		return float64(number), true
	case int64:
		return float64(number), true
	case float64:
		return number, true
	default:
		return 0, false
	}
}

func selectedDataSubnetCount(network map[string]any) int {
	key := "data_subnet_zones"
	if stringValue(network["mode"]) == "existing" {
		key = "existing_data_subnet_ids"
	}
	items, _ := network[key].([]any)
	if len(items) > 0 {
		return len(items)
	}
	zones, _ := network["availability_zones"].([]any)
	return len(zones)
}

func containsStringValue(value any, target string) bool {
	items, ok := value.([]any)
	if !ok {
		return false
	}
	for _, item := range items {
		if stringValue(item) == target {
			return true
		}
	}
	return false
}

func ValidName(name string) bool {
	return namePattern.MatchString(name)
}

func GetPath(doc Document, path string) (any, bool) {
	var current any = map[string]any(doc)
	for _, part := range strings.Split(path, ".") {
		m, ok := mapValue(current)
		if !ok {
			return nil, false
		}
		current, ok = m[part]
		if !ok {
			return nil, false
		}
	}
	return current, true
}

func SetPath(doc Document, path string, value any) {
	parts := strings.Split(path, ".")
	current := map[string]any(doc)
	for _, part := range parts[:len(parts)-1] {
		next, ok := mapValue(current[part])
		if !ok {
			next = make(map[string]any)
			current[part] = next
		}
		current = next
	}
	current[parts[len(parts)-1]] = value
}

func summarize(name string, doc Document, componentPaths map[string]string) Summary {
	project := stringValue(doc["project"])
	environment := stringValue(doc["environment"])
	region := stringValue(doc["region"])
	components := make([]string, 0)
	for key, path := range componentPaths {
		if value, ok := GetPath(doc, path); ok && boolValue(value) {
			components = append(components, key)
		}
	}
	sort.Strings(components)
	return Summary{
		Name:        name,
		Project:     project,
		Environment: environment,
		Region:      region,
		ClusterName: ClusterName(doc),
		Components:  components,
	}
}

func mapValue(value any) (map[string]any, bool) {
	switch v := value.(type) {
	case map[string]any:
		return v, true
	case Document:
		return map[string]any(v), true
	default:
		return nil, false
	}
}

func stringValue(value any) string {
	valueString, _ := value.(string)
	return strings.TrimSpace(valueString)
}

func boolValue(value any) bool {
	valueBool, _ := value.(bool)
	return valueBool
}

var (
	ErrInvalidName   = errors.New("invalid environment name")
	ErrAlreadyExists = errors.New("environment already exists")
)

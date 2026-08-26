package resourcecenter

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"ops-deploy-platform/internal/appconfig"
	"ops-deploy-platform/internal/environment"
	statusservice "ops-deploy-platform/internal/status"
)

type memoryResourceStore struct {
	payload []byte
}

func (store *memoryResourceStore) LoadResourceSnapshot(context.Context, string, string) ([]byte, error) {
	return append([]byte(nil), store.payload...), nil
}

func (store *memoryResourceStore) SaveResourceSnapshot(_ context.Context, _, _ string, payload []byte) error {
	store.payload = append([]byte(nil), payload...)
	return nil
}

func TestPublicSnapshotNeverContainsSecretReferences(t *testing.T) {
	snapshot := Snapshot{
		Project: "demo", Environment: "test", ObservedAt: time.Now(),
		Resources: []Resource{{
			Key: "rds", DisplayName: "RDS", Credentials: []Credential{{
				ID: "opaque-id", Label: "管理员凭据", Username: "admin",
				Provider: "aws-secrets-manager", SecretRef: "arn:aws:secretsmanager:ap-south-1:123:secret:demo", Available: true,
			}},
		}},
	}

	payload, err := json.Marshal(snapshot.Public())
	if err != nil {
		t.Fatal(err)
	}
	serialized := string(payload)
	if strings.Contains(serialized, "SecretRef") || strings.Contains(serialized, "secret_ref") || strings.Contains(serialized, "arn:aws:") {
		t.Fatalf("public snapshot leaked secret material: %s", serialized)
	}
	if !strings.Contains(serialized, "opaque-id") || !strings.Contains(serialized, "管理员凭据") {
		t.Fatalf("public snapshot lost safe credential metadata: %s", serialized)
	}
}

func TestKubernetesResourceNotFoundRecognition(t *testing.T) {
	for _, message := range []string{
		`Error from server (NotFound): secrets "missing" not found`,
		`error: the server could not find the requested resource`,
	} {
		if !kubernetesResourceNotFound(message) {
			t.Fatalf("missing Kubernetes resource was not recognized: %q", message)
		}
	}
	if kubernetesResourceNotFound("Error from server (Forbidden): secrets is forbidden") {
		t.Fatal("RBAC failure was misclassified as a missing credential")
	}
}

func TestPublicSnapshotDoesNotExposeCloudBaseline(t *testing.T) {
	snapshot := Snapshot{Resources: []Resource{{
		Key:      "elasticache",
		Baseline: map[string]any{"data_services.elasticache.nodes_per_shard": 4},
		Configuration: []ConfigurationField{{
			Path: "data_services.elasticache.nodes_per_shard", Label: "每分片总节点数",
			Desired: 1, Actual: 4, State: "drifted", Syncable: true,
		}},
	}}}
	payload, err := json.Marshal(snapshot.Public())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(payload), "baseline") {
		t.Fatalf("public cloud configuration leaked its private comparison baseline: %s", payload)
	}
	if !strings.Contains(string(payload), "drifted") || !strings.Contains(string(payload), "每分片总节点数") {
		t.Fatalf("public cloud configuration lost safe drift details: %s", payload)
	}
}

func TestCloudFieldStateDistinguishesPendingDriftAndConflict(t *testing.T) {
	tests := []struct {
		name            string
		desired, actual any
		baseline        any
		expected        string
	}{
		{"synced", 2, 2, 1, "synced"},
		{"platform pending", 2, 1, 1, "pending"},
		{"aws drift", 1, 2, 1, "drifted"},
		{"conflict", 3, 2, 1, "conflict"},
	}
	for _, item := range tests {
		t.Run(item.name, func(t *testing.T) {
			if got := cloudFieldState(item.desired, item.actual, item.baseline); got != item.expected {
				t.Fatalf("state = %s, want %s", got, item.expected)
			}
		})
	}
}

func TestEKSPublicAccessCIDRsAreComparedAsAnAdditiveSet(t *testing.T) {
	if got := additiveStringSetState(
		[]any{"203.0.113.10/32"},
		[]string{"198.51.100.4/32", "203.0.113.10/32"},
	); got != "synced" {
		t.Fatalf("AWS-only CIDRs must not be reported as drift: got %s", got)
	}
	if got := additiveStringSetState(
		[]any{"192.0.2.8/32", "203.0.113.10/32"},
		[]string{"203.0.113.10/32"},
	); got != "pending" {
		t.Fatalf("a missing platform CIDR must remain pending: got %s", got)
	}
	doc := environment.Document{}
	for _, spec := range cloudFieldSpecs("eks", doc) {
		if spec.Path == "eks.public_access_cidrs" {
			if spec.Syncable {
				t.Fatal("additive EKS CIDRs must never be overwritten by AWS-to-platform synchronization")
			}
			return
		}
	}
	t.Fatal("EKS public access CIDR field specification is missing")
}

func TestECRRepositoriesAreAdditiveAndNeverSyncedOverAWS(t *testing.T) {
	if got := additiveStringSetState(
		[]any{"gateway", "game-admin"},
		[]string{"gateway", "game-admin", "manually-created"},
	); got != "synced" {
		t.Fatalf("an AWS-only ECR repository must be preserved without drift: got %s", got)
	}
	for _, spec := range cloudFieldSpecs("ecr", environment.Document{}) {
		if spec.Path == "ecr.repositories" {
			if spec.Syncable || !additiveCloudFieldPath(spec.Path) {
				t.Fatalf("ECR repositories must use a non-overwriting additive policy: %#v", spec)
			}
			return
		}
	}
	t.Fatal("ECR repository field specification is missing")
}

func TestCloudDocumentValueNormalizesTypedStringSlices(t *testing.T) {
	normalized := cloudDocumentValue([]string{"3.1.174.139/32", "10.0.0.0/8"})
	items, ok := normalized.([]any)
	if !ok || len(items) != 2 {
		t.Fatalf("typed string slice was not normalized for an environment document: %#v", normalized)
	}
	if items[0] != "3.1.174.139/32" || items[1] != "10.0.0.0/8" {
		t.Fatalf("normalized CIDRs changed unexpectedly: %#v", items)
	}
}

func TestConfiguredECRRepositoriesAreEnvironmentScoped(t *testing.T) {
	doc := environment.Document{"ecr": map[string]any{"repositories": []any{
		"prod-gateway", "kbp/prod-online", "", "prod-gateway",
	}}}
	configured := configuredECRRepositories("kbp", doc)
	for _, name := range []string{"kbp/prod-gateway", "kbp/prod-online"} {
		if _, ok := configured[name]; !ok {
			t.Fatalf("configured repository %s was not normalized: %#v", name, configured)
		}
	}
	if len(configured) != 2 {
		t.Fatalf("environment ECR scope contains unexpected repositories: %#v", configured)
	}
	if _, leaked := configured["kbp/gateway"]; leaked {
		t.Fatalf("test repository leaked into production ECR scope: %#v", configured)
	}
}

func TestCloudDeploymentPreflightBlocksExternalDriftButAllowsPendingChange(t *testing.T) {
	pending := Snapshot{CloudSync: CloudSync{Status: "pending", PendingFields: 1}}
	if err := pending.CloudDeploymentPreflightError(); err != nil {
		t.Fatalf("a deliberate platform change must be deployable: %v", err)
	}
	drifted := Snapshot{CloudSync: CloudSync{Status: "drifted", DriftedFields: 1, BlockingChanges: true}}
	if err := drifted.CloudDeploymentPreflightError(); err == nil || !strings.Contains(err.Error(), "AWS 控制台变更") {
		t.Fatalf("external AWS drift must block deployment with a clear error: %v", err)
	}
	modifying := Snapshot{Resources: []Resource{{
		DisplayName: "ElastiCache Redis/Valkey", Metadata: map[string]any{"aws_status": "modifying"},
		Configuration: []ConfigurationField{{Path: "data_services.elasticache.nodes_per_shard", State: "synced"}},
	}}}
	if err := modifying.CloudDeploymentPreflightError(); err == nil || !strings.Contains(err.Error(), "仍在变更中") {
		t.Fatalf("an in-flight AWS resize must block another deployment: %v", err)
	}
}

func TestCloudDeploymentPreflightRejectsUnsupportedCapacityShrink(t *testing.T) {
	rds := Snapshot{Resources: []Resource{{
		Key: "rds", DisplayName: "RDS 管理数据库", Metadata: map[string]any{"aws_status": "available"},
		Configuration: []ConfigurationField{
			{Path: "data_services.rds.allocated_storage", Desired: 80, Actual: 100, State: "pending"},
			{Path: "data_services.rds.max_allocated_storage", Desired: 500, Actual: 500, State: "synced"},
		},
	}}}
	if err := rds.CloudDeploymentPreflightError(); err == nil || !strings.Contains(err.Error(), "不支持缩容") {
		t.Fatalf("RDS storage shrink must be rejected before Terraform: %v", err)
	}

	msk := Snapshot{Resources: []Resource{{
		Key: "msk", DisplayName: "Amazon MSK Kafka", Metadata: map[string]any{"aws_status": "ACTIVE"},
		Configuration: []ConfigurationField{{
			Path: "data_services.msk.broker_count", Desired: 3, Actual: 6, State: "pending",
		}},
	}}}
	if err := msk.CloudDeploymentPreflightError(); err == nil || !strings.Contains(err.Error(), "只支持扩容") {
		t.Fatalf("MSK broker shrink must be rejected before Terraform: %v", err)
	}

	expand := Snapshot{Resources: []Resource{{
		Key: "msk", DisplayName: "Amazon MSK Kafka", Metadata: map[string]any{"aws_status": "ACTIVE"},
		Configuration: []ConfigurationField{{
			Path: "data_services.msk.broker_count", Desired: 6, Actual: 3, State: "pending",
		}},
	}}}
	if err := expand.CloudDeploymentPreflightError(); err != nil {
		t.Fatalf("supported MSK expansion must remain deployable: %v", err)
	}

	modeSwitch := Snapshot{Resources: []Resource{{
		Key: "elasticache", DisplayName: "ElastiCache Redis/Valkey", Metadata: map[string]any{"aws_status": "available"},
		Configuration: []ConfigurationField{{
			Path: "data_services.elasticache.mode", Desired: "serverless", Actual: "cluster", State: "pending",
		}},
	}}}
	if err := modeSwitch.CloudDeploymentPreflightError(); err == nil || !strings.Contains(err.Error(), "不支持对现有资源原地切换") {
		t.Fatalf("an in-place managed-service mode replacement must be rejected: %v", err)
	}

	auroraBacktrack := Snapshot{Resources: []Resource{{
		Key: "aurora", DisplayName: "Aurora MySQL", Metadata: map[string]any{"aws_status": "available"},
		Configuration: []ConfigurationField{{
			Path: "data_services.aurora.backtrack_enabled", Desired: true, Actual: false, State: "pending",
		}},
	}}}
	if err := auroraBacktrack.CloudDeploymentPreflightError(); err == nil || !strings.Contains(err.Error(), "创建时未启用") {
		t.Fatalf("enabling backtrack on an incompatible existing cluster must fail before Terraform: %v", err)
	}
}

func TestAttachCloudConfigurationTracksDisableAndCompletedDeletion(t *testing.T) {
	service := &Service{}
	doc := environment.Document{"data_services": map[string]any{"rds": map[string]any{"enabled": false}}}
	path := "data_services.rds.enabled"
	previous := Snapshot{Resources: []Resource{{
		Key: "rds", DisplayName: "RDS 管理数据库", Metadata: map[string]any{},
		Baseline: map[string]any{path: true}, Configuration: []ConfigurationField{{Path: path, Desired: true, Actual: true, State: "synced"}},
	}}}

	pending := Snapshot{ObservedAt: time.Now(), Resources: append([]Resource(nil), previous.Resources...)}
	service.attachCloudConfiguration(&pending, doc, map[string]actualCloudResource{
		"rds": {Key: "rds", Exists: true, Status: "available", Fields: map[string]any{path: true}},
	}, previous)
	if pending.CloudSync.PendingFields != 1 || pending.CloudSync.BlockingChanges {
		t.Fatalf("an explicit platform disable must be pending and deployable: %#v", pending.CloudSync)
	}

	deleted := Snapshot{ObservedAt: time.Now(), Resources: append([]Resource(nil), previous.Resources...)}
	service.attachCloudConfiguration(&deleted, doc, map[string]actualCloudResource{
		"rds": {Key: "rds", Exists: false, Fields: map[string]any{}},
	}, previous)
	if deleted.CloudSync.DriftedFields != 0 || deleted.CloudSync.BlockingChanges || len(deleted.Resources[0].Configuration) != 0 {
		t.Fatalf("a completed intentional deletion must not leave permanent drift: %#v", deleted)
	}
}

func TestResetMissingCloudConfigurationAfterDestroyPreservesExistingResources(t *testing.T) {
	store := &memoryResourceStore{}
	service := &Service{store: store}
	snapshot := Snapshot{
		Project: "demo", Environment: "uat", ObservedAt: time.Now(),
		CloudSync: CloudSync{Status: "drifted", DriftedFields: 25, BlockingChanges: true},
		Resources: []Resource{
			{
				Key: "eks", Source: "cloud", Status: "missing", Metadata: map[string]any{"aws_status": "NOT_FOUND"},
				Baseline:      map[string]any{"eks.kubernetes_version": "1.34"},
				Configuration: []ConfigurationField{{Path: "eks.kubernetes_version", State: "drifted"}},
			},
			{
				Key: "ecr", Source: "cloud", Status: "healthy", Metadata: map[string]any{"aws_status": "ACTIVE"},
				Baseline:      map[string]any{"ecr.enabled": true},
				Configuration: []ConfigurationField{{Path: "ecr.enabled", State: "synced"}},
			},
			{Key: "grafana", Source: "self-hosted", Status: "healthy"},
		},
	}
	payload, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	store.payload = payload

	if err := service.ResetMissingCloudConfigurationAfterDestroy(t.Context(), "demo", "uat"); err != nil {
		t.Fatal(err)
	}
	var saved Snapshot
	if err := json.Unmarshal(store.payload, &saved); err != nil {
		t.Fatal(err)
	}
	if len(saved.Resources) != 2 || saved.Resources[0].Key != "ecr" || saved.Resources[1].Key != "grafana" {
		t.Fatalf("reset removed resources that still exist: %#v", saved.Resources)
	}
	if saved.CloudSync.BlockingChanges || saved.CloudSync.DriftedFields != 0 || saved.CloudSync.SyncedFields != 1 {
		t.Fatalf("stale destroyed-resource drift survived reset: %#v", saved.CloudSync)
	}
}

func TestAttachCloudConfigurationDetectsElastiCacheConsoleResize(t *testing.T) {
	service := &Service{}
	doc := environment.Document{
		"data_services": map[string]any{"elasticache": map[string]any{
			"enabled": true, "mode": "cluster", "engine": "redis", "engine_version": "7.1",
			"node_type": "cache.m4.xlarge", "num_node_groups": 5, "nodes_per_shard": 1,
		}},
	}
	path := "data_services.elasticache.nodes_per_shard"
	previous := Snapshot{Resources: []Resource{{Key: "elasticache", Baseline: map[string]any{path: int64(1)}}}}
	snapshot := Snapshot{ObservedAt: time.Now(), Resources: []Resource{{
		Key: "elasticache", DisplayName: "ElastiCache Redis/Valkey", Metadata: map[string]any{},
	}}}
	service.attachCloudConfiguration(&snapshot, doc, map[string]actualCloudResource{
		"elasticache": {
			Key: "elasticache", Exists: true, Status: "available",
			Fields: map[string]any{
				"data_services.elasticache.mode": "cluster", "data_services.elasticache.engine": "redis",
				"data_services.elasticache.engine_version": "7.1", "data_services.elasticache.node_type": "cache.m4.xlarge",
				"data_services.elasticache.num_node_groups": 5, path: 4,
			},
		},
	}, previous)
	if !snapshot.CloudSync.BlockingChanges || snapshot.CloudSync.DriftedFields != 1 {
		t.Fatalf("console resize was not classified as external drift: %#v", snapshot.CloudSync)
	}
	var capacity ConfigurationField
	for _, field := range snapshot.Resources[0].Configuration {
		if field.Path == path {
			capacity = field
		}
	}
	if capacity.State != "drifted" || capacity.Desired != int64(1) || capacity.Actual != int64(4) {
		t.Fatalf("unexpected ElastiCache capacity comparison: %#v", capacity)
	}
}

func TestBuildOnlyIncludesActuallyCreatedResources(t *testing.T) {
	service := &Service{config: &appconfig.Config{}}
	doc := environment.Document{
		"project": "demo", "environment": "test", "region": "ap-south-1",
		"network": map[string]any{"vpc_cidr": "10.40.0.0/16", "workload_subnet_type": "public"},
		"eks":     map[string]any{"kubernetes_version": "1.31", "node_groups": map[string]any{"default": map[string]any{}}},
		"data_services": map[string]any{
			"rds":         map[string]any{"enabled": true},
			"elasticache": map[string]any{"enabled": true},
		},
		"components": map[string]any{"consul": map[string]any{"enabled": true}},
	}
	report := &statusservice.Report{Cluster: statusservice.Cluster{Name: "demo-test-eks", Status: "NOT_FOUND", Reachable: false}}
	snapshot := service.build("demo", "test", "demo-test", doc, report, map[string]any{})
	if len(snapshot.Resources) != 0 {
		t.Fatalf("configured but undeployed resources must stay hidden, got %#v", snapshot.Resources)
	}
}

func TestBuildCollectsNATGatewayAddresses(t *testing.T) {
	service := &Service{config: &appconfig.Config{}}
	doc := environment.Document{
		"project": "demo", "environment": "test", "region": "ap-south-1",
		"network": map[string]any{"vpc_cidr": "10.40.0.0/16", "workload_subnet_type": "public", "nat_gateway_mode": "always"},
		"eks":     map[string]any{"kubernetes_version": "1.31", "node_groups": map[string]any{}},
	}
	snapshot := service.build("demo", "test", "demo-test", doc, nil, map[string]any{
		"nat_gateway_mode":       "always",
		"nat_gateway_public_ips": map[string]any{"ap-south-1a": "203.0.113.10"},
	})
	if snapshot.Info.NATGatewayMode != "always" || snapshot.Info.NATGatewayIPs["ap-south-1a"] != "203.0.113.10" {
		t.Fatalf("NAT Gateway outputs were not collected: %#v", snapshot.Info)
	}
}

func TestCompactAccessDropsUnavailableEndpoints(t *testing.T) {
	items := compactAccess([]AccessPoint{
		{Name: "pending"},
		{Name: "host", Host: "db.internal", Port: 3306},
		{Name: "url", URL: "https://console.example.com"},
	})
	if len(items) != 2 {
		t.Fatalf("expected two usable endpoints, got %d", len(items))
	}
}

func TestManagedDataEndpointStaysPrivateInPublicSubnet(t *testing.T) {
	service := &Service{config: &appconfig.Config{}}
	snapshot := Snapshot{Info: EnvironmentInfo{NetworkMode: "public"}}
	doc := environment.Document{
		"data_services": map[string]any{
			"documentdb": map[string]any{
				"enabled": true, "port": 27017, "engine": "docdb",
				"instance_class": "db.t3.medium", "instance_count": 1,
			},
		},
	}
	service.appendManagedResources(&snapshot, doc, map[string]any{
		"documentdb_endpoint": "docdb.internal.example",
	}, map[string]string{})
	if len(snapshot.Resources) != 1 || len(snapshot.Resources[0].AccessPoints) != 1 {
		t.Fatalf("expected one DocumentDB endpoint, got %#v", snapshot.Resources)
	}
	if visibility := snapshot.Resources[0].AccessPoints[0].Visibility; visibility != "private" {
		t.Fatalf("managed endpoint in a public subnet must remain private, got %q", visibility)
	}
	if len(snapshot.Resources[0].Credentials) != 1 || snapshot.Resources[0].Credentials[0].Available {
		t.Fatalf("missing AWS managed secret must remain visible as unavailable: %#v", snapshot.Resources[0].Credentials)
	}
	if len(snapshot.Warnings) != 1 || !strings.Contains(snapshot.Warnings[0], "不会自动更改生产数据库密码") {
		t.Fatalf("missing database credential warning is incomplete: %#v", snapshot.Warnings)
	}
}

func TestSelfHostedComponentWithoutServiceDoesNotInventEndpoint(t *testing.T) {
	service := &Service{config: &appconfig.Config{}}
	snapshot := Snapshot{}
	doc := environment.Document{
		"components": map[string]any{"catalog": map[string]any{
			"tekton": map[string]any{
				"enabled": true, "display_name": "Tekton Pipelines", "namespace": "platform-server",
				"release_name": "tekton", "service_name": "", "service_port": 0,
			},
		}},
	}
	service.appendSelfHostedResources(&snapshot, doc, map[string]string{"tekton": "healthy"}, nil)
	if len(snapshot.Resources) != 1 {
		t.Fatalf("expected the deployed component to remain visible, got %#v", snapshot.Resources)
	}
	if len(snapshot.Resources[0].AccessPoints) != 0 {
		t.Fatalf("component without a service must not publish a fake endpoint: %#v", snapshot.Resources[0].AccessPoints)
	}
}

func TestSelfHostedGatewayIncludesProvisionedLoadBalancerEndpoints(t *testing.T) {
	service := &Service{config: &appconfig.Config{}}
	snapshot := Snapshot{}
	doc := environment.Document{
		"components": map[string]any{"catalog": map[string]any{
			"higress": map[string]any{
				"enabled": true, "namespace": "platform-server", "service_name": "higress-console",
				"service_port": 8080, "protocol": "http", "public_service_name": "higress-gateway",
			},
		}},
	}
	services := map[string]statusservice.KubernetesService{
		"platform-server/higress-gateway": {
			Name: "higress-gateway", Namespace: "platform-server", Type: "LoadBalancer",
			Ports:             []statusservice.ServicePort{{Name: "http", Port: 80}, {Name: "https", Port: 443}},
			LoadBalancerHosts: []string{"gateway.example.elb.amazonaws.com"},
		},
	}
	service.appendSelfHostedResources(&snapshot, doc, map[string]string{"higress": "healthy"}, services)
	if len(snapshot.Resources) != 1 || len(snapshot.Resources[0].AccessPoints) != 3 {
		t.Fatalf("expected console and two public gateway endpoints, got %#v", snapshot.Resources)
	}
	if point := snapshot.Resources[0].AccessPoints[2]; point.Protocol != "https" || point.Port != 443 || point.Visibility != "public" {
		t.Fatalf("unexpected TLS load balancer endpoint: %#v", point)
	}
}

func TestRabbitMQIncludesAMQPAndManagementConsole(t *testing.T) {
	service := &Service{config: &appconfig.Config{}}
	snapshot := Snapshot{}
	doc := environment.Document{
		"components": map[string]any{"catalog": map[string]any{
			"rabbitmq": map[string]any{
				"enabled": true, "display_name": "RabbitMQ（自建）", "namespace": "platform-server",
				"service_name": "rabbitmq", "service_port": 5672, "protocol": "amqp",
				"console_service_name": "rabbitmq", "console_service_port": 15672, "console_protocol": "http",
				"username": "user", "secret_name": "rabbitmq", "secret_key": "rabbitmq-password",
			},
		}},
	}
	service.appendSelfHostedResources(&snapshot, doc, map[string]string{"rabbitmq": "healthy"}, nil)
	if len(snapshot.Resources) != 1 || len(snapshot.Resources[0].AccessPoints) != 2 {
		t.Fatalf("expected AMQP and management access points, got %#v", snapshot.Resources)
	}
	if point := snapshot.Resources[0].AccessPoints[1]; point.Name != "Web 管理页" || point.Port != 15672 || point.Protocol != "http" {
		t.Fatalf("unexpected RabbitMQ management endpoint: %#v", point)
	}
	if len(snapshot.Resources[0].Credentials) != 1 || snapshot.Resources[0].Credentials[0].Username != "user" {
		t.Fatalf("RabbitMQ management credential is missing: %#v", snapshot.Resources[0].Credentials)
	}
}

func TestOpenTelemetryCollectorIncludesBothOTLPEndpoints(t *testing.T) {
	service := &Service{config: &appconfig.Config{}}
	snapshot := Snapshot{}
	doc := environment.Document{
		"components": map[string]any{"catalog": map[string]any{
			"opentelemetry_collector": map[string]any{
				"enabled": true, "display_name": "OpenTelemetry Collector", "namespace": "monitoring",
				"service_name": "opentelemetry-collector", "service_port": 4317, "protocol": "grpc",
			},
		}},
	}
	service.appendSelfHostedResources(&snapshot, doc, map[string]string{"opentelemetry_collector": "healthy"}, nil)
	if len(snapshot.Resources) != 1 || len(snapshot.Resources[0].AccessPoints) != 2 {
		t.Fatalf("expected OTLP gRPC and HTTP endpoints, got %#v", snapshot.Resources)
	}
	grpc := snapshot.Resources[0].AccessPoints[0]
	http := snapshot.Resources[0].AccessPoints[1]
	if grpc.Name != "OTLP gRPC（集群内）" || grpc.Protocol != "grpc" || grpc.Port != 4317 {
		t.Fatalf("unexpected OTLP gRPC endpoint: %#v", grpc)
	}
	if http.Name != "OTLP HTTP（集群内）" || http.Protocol != "http" || http.Port != 4318 || http.Host != grpc.Host {
		t.Fatalf("unexpected OTLP HTTP endpoint: %#v", http)
	}
}

func TestOpenTelemetryDedicatedElasticsearchHasIndependentAccessResource(t *testing.T) {
	service := &Service{config: &appconfig.Config{}}
	snapshot := Snapshot{}
	doc := environment.Document{
		"components": map[string]any{"catalog": map[string]any{
			"opentelemetry_collector": map[string]any{
				"enabled": true, "display_name": "OpenTelemetry Collector", "namespace": "monitoring",
				"service_name": "opentelemetry-collector", "service_port": 4317, "protocol": "grpc",
				"values": map[string]any{"elasticsearch": map[string]any{
					"enabled": true, "mode": "cluster", "replicas": 3,
					"image":   map[string]any{"tag": "8.19.17"},
					"storage": map[string]any{"initialSize": "50Gi", "expandedSize": "100Gi"},
				}},
			},
		}},
	}
	service.appendSelfHostedResources(&snapshot, doc, map[string]string{"opentelemetry_collector": "healthy"}, nil)
	if len(snapshot.Resources) != 2 {
		t.Fatalf("expected Collector and its dedicated Elasticsearch resource, got %#v", snapshot.Resources)
	}
	elasticsearch := snapshot.Resources[1]
	if elasticsearch.Key != "otel_elasticsearch" || elasticsearch.Namespace != "monitoring" || len(elasticsearch.AccessPoints) != 1 || elasticsearch.AccessPoints[0].Port != 9200 {
		t.Fatalf("unexpected dedicated Elasticsearch access resource: %#v", elasticsearch)
	}
	if elasticsearch.Metadata["storage_per_node"] != "100Gi" || len(elasticsearch.Credentials) != 1 || elasticsearch.Credentials[0].Provider != "kubernetes-secret" {
		t.Fatalf("dedicated Elasticsearch storage or credential metadata is missing: %#v", elasticsearch)
	}
}

func TestTCPRouteUsesDedicatedNLBAccessPoint(t *testing.T) {
	service := &Service{config: &appconfig.Config{}}
	snapshot := Snapshot{}
	doc := environment.Document{"domains": []any{map[string]any{
		"enabled": true, "protocol": "tcp", "access_type": "domain", "domain": "mysql.example.com",
		"namespace": "apps", "service": "mysql", "service_port": 3306, "external_port": 3306,
		"tcp_scheme": "internet-facing", "allowed_cidrs": []any{"203.0.113.10/32"},
	}}}
	services := map[string]statusservice.KubernetesService{
		"apps/tcp-mysql-0": {
			Name: "tcp-mysql-0", Namespace: "apps", Type: "LoadBalancer",
			Ports: []statusservice.ServicePort{{Name: "tcp-3306", Port: 3306}}, LoadBalancerHosts: []string{"mysql.example.elb.amazonaws.com"},
		},
	}
	service.appendDomainResources(&snapshot, doc, nil, services)
	if len(snapshot.Resources) != 1 || snapshot.Resources[0].Status != "healthy" || len(snapshot.Resources[0].AccessPoints) != 2 {
		t.Fatalf("unexpected TCP route resource: %#v", snapshot.Resources)
	}
	if point := snapshot.Resources[0].AccessPoints[1]; point.Protocol != "tcp" || point.Host != "mysql.example.elb.amazonaws.com" || point.Port != 3306 {
		t.Fatalf("unexpected NLB access point: %#v", point)
	}
}

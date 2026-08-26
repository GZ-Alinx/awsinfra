package environment

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"strings"
	"testing"
)

type memoryEnvironmentStore struct {
	records map[string][]byte
}

func (s *memoryEnvironmentStore) LoadEnvironments(context.Context) (map[string][]byte, error) {
	result := make(map[string][]byte, len(s.records))
	for name, payload := range s.records {
		result[name] = append([]byte(nil), payload...)
	}
	return result, nil
}

func (s *memoryEnvironmentStore) GetEnvironment(_ context.Context, name string) ([]byte, error) {
	payload, ok := s.records[name]
	if !ok {
		return nil, os.ErrNotExist
	}
	return append([]byte(nil), payload...), nil
}

func (s *memoryEnvironmentStore) SaveEnvironment(_ context.Context, name string, payload []byte) error {
	s.records[name] = append([]byte(nil), payload...)
	return nil
}

func (s *memoryEnvironmentStore) DeleteEnvironment(_ context.Context, name string) error {
	delete(s.records, name)
	return nil
}

func validDocument() Document {
	return DefaultDocument("ops", "test")
}

func TestDefaultDocumentCreatesIsolatedNodeGroups(t *testing.T) {
	doc := DefaultDocument("ops", "test")
	if !WorkloadSchedulingEnabled(doc) {
		t.Fatal("new managed environment should enable workload scheduling")
	}
	groups := doc["eks"].(map[string]any)["node_groups"].(map[string]any)
	for name, role := range map[string]string{"ingress-gateway": "gateway", "business-workload": "application", "platform-ops": "platform"} {
		group, ok := groups[name].(map[string]any)
		if !ok {
			t.Fatalf("default node group %s is missing", name)
		}
		labels, _ := group["labels"].(map[string]any)
		if labels["workload-class"] != role {
			t.Fatalf("node group %s role=%#v, want %s", name, labels["workload-class"], role)
		}
		taints, _ := group["taints"].([]any)
		if role == "application" && len(taints) != 1 {
			t.Fatalf("business node group must have exactly one taint, got %#v", taints)
		}
		if role != "application" && len(taints) != 0 {
			t.Fatalf("non-business node group %s must not have taints, got %#v", name, taints)
		}
	}
	if err := Validate(doc); err != nil {
		t.Fatalf("new scheduling defaults are invalid: %v", err)
	}
}

func TestApplyDefaultsKeepsOnlyBusinessNodeGroupTaint(t *testing.T) {
	doc := DefaultDocument("ops", "prod")
	groups := doc["eks"].(map[string]any)["node_groups"].(map[string]any)
	groups["ingress-gateway"].(map[string]any)["taints"] = []any{map[string]any{
		"key": "workload-class", "value": "gateway", "effect": "NO_SCHEDULE",
	}}
	groups["platform-ops"].(map[string]any)["taints"] = []any{map[string]any{
		"key": "workload-class", "value": "platform", "effect": "NO_SCHEDULE",
	}}
	groups["business-workload"].(map[string]any)["taints"] = []any{}

	upgraded := ApplyDefaults(doc, "ops", "prod")
	groups = upgraded["eks"].(map[string]any)["node_groups"].(map[string]any)
	for _, name := range []string{"ingress-gateway", "platform-ops"} {
		if taints := groups[name].(map[string]any)["taints"].([]any); len(taints) != 0 {
			t.Fatalf("%s taints were not removed: %#v", name, taints)
		}
	}
	taints := groups["business-workload"].(map[string]any)["taints"].([]any)
	if len(taints) != 1 {
		t.Fatalf("business taint was not restored: %#v", taints)
	}
	taint := taints[0].(map[string]any)
	if taint["key"] != "workload-class" || taint["value"] != "application" || taint["effect"] != "NO_SCHEDULE" {
		t.Fatalf("business taint is invalid: %#v", taint)
	}
}

func TestValidateRequiresPlatformCapacityForStatefulCluster(t *testing.T) {
	doc := DefaultDocument("ops", "test")
	components := doc["components"].(map[string]any)
	components["consul"].(map[string]any)["enabled"] = true

	err := Validate(doc)
	if err == nil || !strings.Contains(err.Error(), "min_size") {
		t.Fatalf("undersized platform node group was accepted: %v", err)
	}

	platform := doc["eks"].(map[string]any)["node_groups"].(map[string]any)["platform-ops"].(map[string]any)
	platform["min_size"] = 3
	platform["desired_size"] = 3
	if err := Validate(doc); err != nil {
		t.Fatalf("three-node, three-zone platform capacity was rejected: %v", err)
	}
}

func TestApplyDefaultsDoesNotEnableSchedulingForExistingEnvironment(t *testing.T) {
	doc := DefaultDocument("ops", "test")
	eks := doc["eks"].(map[string]any)
	delete(eks, "workload_scheduling")
	delete(eks["node_groups"].(map[string]any), "platform-ops")

	upgraded := ApplyDefaults(doc, "ops", "test")
	if WorkloadSchedulingEnabled(upgraded) {
		t.Fatal("existing environment unexpectedly enabled workload scheduling")
	}
	if _, exists := upgraded["eks"].(map[string]any)["node_groups"].(map[string]any)["platform-ops"]; exists {
		t.Fatal("existing environment unexpectedly gained a platform node group")
	}
}

func TestConfigureNewEnvironmentSchedulingUpgradesClonedLegacyLayout(t *testing.T) {
	doc := DefaultDocument("ops", "test")
	eks := doc["eks"].(map[string]any)
	delete(eks, "workload_scheduling")
	delete(eks["node_groups"].(map[string]any), "platform-ops")
	ConfigureNewEnvironmentScheduling(doc)
	if !WorkloadSchedulingEnabled(doc) {
		t.Fatal("new cloned environment did not enable workload scheduling")
	}
	groups := eks["node_groups"].(map[string]any)
	if _, exists := groups["platform-ops"]; !exists {
		t.Fatal("new cloned environment did not receive a platform node group")
	}
	if err := Validate(doc); err != nil {
		t.Fatalf("upgraded cloned environment is invalid: %v", err)
	}

	if err := ConfigureTarget(doc, TargetExistingEKS); err != nil {
		t.Fatal(err)
	}
	ConfigureNewEnvironmentScheduling(doc)
	if WorkloadSchedulingEnabled(doc) {
		t.Fatal("existing EKS target must not enable managed node scheduling")
	}
}

func TestSchedulingPlanLocksPlacementButAllowsCapacityChanges(t *testing.T) {
	current := DefaultDocument("ops", "test")
	next := ApplyDefaults(cloneDocumentForTest(t, current), "ops", "test")
	group := next["eks"].(map[string]any)["node_groups"].(map[string]any)["business-workload"].(map[string]any)
	group["min_size"], group["desired_size"], group["max_size"] = 2, 3, 10
	if !SameSchedulingPlan(current, next) {
		t.Fatal("capacity-only update changed the immutable scheduling plan")
	}
	group["instance_types"] = []any{"m7i.xlarge"}
	if SameSchedulingPlan(current, next) {
		t.Fatal("instance type update was not detected as a scheduling plan change")
	}
}

func cloneDocumentForTest(t *testing.T, doc Document) Document {
	t.Helper()
	payload, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	var cloned Document
	if err := json.Unmarshal(payload, &cloned); err != nil {
		t.Fatal(err)
	}
	return cloned
}

func TestApplyDefaultsMigratesLegacySubnetGroups(t *testing.T) {
	doc := validDocument()
	network := doc["network"].(map[string]any)
	delete(network, "workload_subnet_zones")
	delete(network, "data_subnet_zones")
	delete(network, "workload_subnet_type")
	delete(network, "data_subnet_type")
	delete(doc["eks"].(map[string]any)["node_groups"].(map[string]any)["business-workload"].(map[string]any), "subnet_type")
	network["database_subnets"] = map[string]any{"ap-south-1a": "10.40.112.0/24"}
	network["elasticache_subnets"] = map[string]any{"ap-south-1a": "10.40.120.0/24"}
	alerting := doc["alerting"].(map[string]any)
	delete(alerting, "template_preset_version")
	alerting["templates"] = []any{}

	upgraded := ApplyDefaults(doc, "ops", "test")
	upgradedNetwork := upgraded["network"].(map[string]any)
	if _, exists := upgradedNetwork["database_subnets"]; exists {
		t.Fatal("legacy database_subnets should be removed")
	}
	if _, exists := upgradedNetwork["elasticache_subnets"]; exists {
		t.Fatal("legacy elasticache_subnets should be removed")
	}
	if upgradedNetwork["workload_subnet_type"] != "public" || upgradedNetwork["data_subnet_type"] != "public" {
		t.Fatal("network switches should be added with public defaults")
	}
	if len(upgradedNetwork["workload_subnet_zones"].([]any)) != 3 || len(upgradedNetwork["data_subnet_zones"].([]any)) != 3 {
		t.Fatal("network zone selections should be added with all three AZs")
	}
	if templates := upgraded["alerting"].(map[string]any)["templates"].([]any); len(templates) < 6 {
		t.Fatalf("legacy alert configuration should receive presets, got %d", len(templates))
	}
}

func TestApplyDefaultsDoesNotRestoreDeletedNodeGroup(t *testing.T) {
	doc := validDocument()
	eks := doc["eks"].(map[string]any)
	eks["node_groups"] = map[string]any{
		"application-private": map[string]any{
			"subnet_type":        "private",
			"instance_types":     []any{"m7i.xlarge"},
			"capacity_type":      "ON_DEMAND",
			"min_size":           3,
			"desired_size":       3,
			"max_size":           6,
			"disk_size":          80,
			"availability_zones": []any{"ap-south-1a", "ap-south-1b", "ap-south-1c"},
		},
	}

	upgraded := ApplyDefaults(doc, "ops", "test")
	groups := upgraded["eks"].(map[string]any)["node_groups"].(map[string]any)
	if _, exists := groups["business-workload"]; exists {
		t.Fatal("ApplyDefaults restored an explicitly deleted node group")
	}
	if _, exists := groups["application-private"]; !exists {
		t.Fatal("ApplyDefaults removed the configured private node group")
	}
}

func TestValidateAlertChannelDirectAddress(t *testing.T) {
	doc := DefaultDocument("ops", "test")
	alerting := doc["alerting"].(map[string]any)
	alerting["channels"] = []any{map[string]any{"name": "ops-webhook", "type": "webhook", "address": "https://alerts.example.com/hook"}}
	if err := Validate(doc); err != nil {
		t.Fatalf("valid direct webhook address was rejected: %v", err)
	}
	alerting["channels"] = []any{map[string]any{"name": "broken", "type": "webhook", "address": "javascript:alert(1)"}}
	if err := Validate(doc); err == nil {
		t.Fatal("unsafe alert channel address was accepted")
	}
}

func TestAlertDeliveryPolicyDefaultsToCoreAndRejectsUnknownValue(t *testing.T) {
	doc := DefaultDocument("ops", "test")
	alerting := doc["alerting"].(map[string]any)
	if alerting["delivery_policy"] != "core" {
		t.Fatalf("new environments must default to core alert delivery, got %#v", alerting["delivery_policy"])
	}
	delete(alerting, "delivery_policy")
	upgraded := ApplyDefaults(doc, "ops", "test")
	if upgraded["alerting"].(map[string]any)["delivery_policy"] != "core" {
		t.Fatal("legacy alert settings did not receive the core delivery policy")
	}
	upgraded["alerting"].(map[string]any)["delivery_policy"] = "unknown"
	if err := Validate(upgraded); err == nil || !strings.Contains(err.Error(), "delivery_policy") {
		t.Fatalf("unknown alert delivery policy was accepted: %v", err)
	}
}

func TestValidateUploadedTLSCertificateReference(t *testing.T) {
	doc := validDocument()
	doc["tls"].(map[string]any)["certificates"] = []any{map[string]any{
		"enabled": true, "key": "web-tls", "mode": "uploaded-pem", "material_ref": "web-tls",
		"namespace": "platform-server", "tls_secret_name": "web-tls",
	}}
	if err := Validate(doc); err != nil {
		t.Fatalf("valid uploaded TLS reference was rejected: %v", err)
	}
	doc["tls"].(map[string]any)["certificates"].([]any)[0].(map[string]any)["material_ref"] = "other-tls"
	if err := Validate(doc); err == nil || !strings.Contains(err.Error(), "material_ref") {
		t.Fatalf("mismatched uploaded TLS material reference was accepted: %v", err)
	}
}

func TestValidateProtocolAwareRoutes(t *testing.T) {
	doc := validDocument()
	doc["domains"] = []any{map[string]any{
		"enabled": true, "protocol": "http", "access_type": "domain", "domain": "mysql.example.com",
		"gateway": "higress", "namespace": "apps", "service": "mysql", "service_port": 3306,
		"tls_enabled": false,
	}}
	if err := Validate(doc); err == nil || !strings.Contains(err.Error(), "raw TCP") {
		t.Fatalf("HTTP ingress accepted MySQL port: %v", err)
	}

	route := map[string]any{
		"enabled": true, "protocol": "tcp", "access_type": "domain", "domain": "mysql.example.com",
		"gateway": "nlb", "namespace": "apps", "service": "mysql", "service_port": 3306,
		"external_port": 3306, "tcp_scheme": "internet-facing", "tls_enabled": false,
	}
	doc["domains"] = []any{route}
	if err := Validate(doc); err == nil || !strings.Contains(err.Error(), "allowed_cidrs") {
		t.Fatalf("public TCP route without an allowlist was accepted: %v", err)
	}
	route["allowed_cidrs"] = []any{"0.0.0.0/0"}
	if err := Validate(doc); err == nil || !strings.Contains(err.Error(), "entire internet") {
		t.Fatalf("public TCP route with a wildcard allowlist was accepted: %v", err)
	}
	route["allowed_cidrs"] = []any{"203.0.113.10/32"}
	if err := Validate(doc); err != nil {
		t.Fatalf("valid allowlisted TCP route was rejected: %v", err)
	}
}

func TestNormalizeAndValidateMultipleDomainRoutes(t *testing.T) {
	doc := validDocument()
	domain := map[string]any{
		"enabled": true, "protocol": "https", "access_type": "domain", "domain": "app.example.com",
		"gateway": "higress", "namespace": "apps", "tls_enabled": true, "certificate_ref": "web-tls",
		"backend_protocol": "http",
		"routes": []any{
			map[string]any{"path": "/", "path_type": "Prefix", "service": "web", "service_port": 80},
			map[string]any{"path": "/api", "path_type": "Prefix", "service": "api", "service_port": 8080},
		},
	}
	doc["tls"].(map[string]any)["certificates"] = []any{map[string]any{
		"enabled": true, "key": "web-tls", "mode": "existing-secret",
		"namespace": "apps", "tls_secret_name": "web-tls",
	}}
	doc["domains"] = []any{domain}

	NormalizeDomainRoutes(doc)
	NormalizeDomainBackendProtocols(doc)

	if domain["service"] != "web" || intValue(domain["service_port"]) != 80 || domain["path"] != "/" {
		t.Fatalf("legacy first-route mirror was not generated: %#v", domain)
	}
	if err := Validate(doc); err != nil {
		t.Fatalf("valid multi-path domain was rejected: %v", err)
	}
}

func TestNormalizeLegacyDomainCreatesRouteCollection(t *testing.T) {
	doc := validDocument()
	domain := map[string]any{
		"enabled": true, "protocol": "http", "access_type": "domain", "domain": "legacy.example.com",
		"gateway": "nginx", "namespace": "apps", "service": "legacy-api", "service_port": 8080,
		"path": "/api", "path_type": "Exact", "tls_enabled": false, "backend_protocol": "http",
	}
	doc["domains"] = []any{domain}

	NormalizeDomainRoutes(doc)

	routes, ok := domain["routes"].([]any)
	if !ok || len(routes) != 1 {
		t.Fatalf("legacy route was not normalized: %#v", domain["routes"])
	}
	route := routes[0].(map[string]any)
	if route["path"] != "/api" || route["path_type"] != "Exact" || route["service"] != "legacy-api" || intValue(route["service_port"]) != 8080 {
		t.Fatalf("legacy route fields changed during normalization: %#v", route)
	}
	if err := Validate(doc); err != nil {
		t.Fatalf("normalized legacy domain was rejected: %v", err)
	}
}

func TestValidateMultipleDomainRoutesRejectsAmbiguousOrTCPConfiguration(t *testing.T) {
	doc := validDocument()
	domain := map[string]any{
		"enabled": true, "protocol": "http", "access_type": "domain", "domain": "app.example.com",
		"gateway": "higress", "namespace": "apps", "tls_enabled": false, "backend_protocol": "http",
		"routes": []any{
			map[string]any{"path": "/api", "path_type": "Prefix", "service": "api-v1", "service_port": 8080},
			map[string]any{"path": "/api", "path_type": "Prefix", "service": "api-v2", "service_port": 8080},
		},
	}
	doc["domains"] = []any{domain}
	if err := Validate(doc); err == nil || !strings.Contains(err.Error(), "duplicate route path") {
		t.Fatalf("duplicate route path was accepted: %v", err)
	}

	domain["protocol"] = "tcp"
	domain["service"] = "mysql"
	domain["service_port"] = 3306
	domain["external_port"] = 3306
	domain["tcp_scheme"] = "internal"
	delete(domain, "backend_protocol")
	if err := Validate(doc); err == nil || !strings.Contains(err.Error(), "not supported for a raw TCP") {
		t.Fatalf("TCP route collection was accepted: %v", err)
	}
}

func TestNormalizeWSSPort443DefaultsToPlaintextBackend(t *testing.T) {
	doc := validDocument()
	route := map[string]any{
		"enabled": true, "protocol": "wss", "access_type": "domain", "domain": "wss.example.com",
		"gateway": "higress", "namespace": "apps", "service": "gateway", "service_port": 443,
		"tls_enabled": true, "certificate_ref": "web-tls",
		"annotations": map[string]any{"higress.io/backend-protocol": "HTTPS"},
	}
	doc["tls"].(map[string]any)["certificates"] = []any{map[string]any{
		"enabled": true, "key": "web-tls", "mode": "existing-secret", "namespace": "apps", "tls_secret_name": "web-tls",
	}}
	doc["domains"] = []any{route}
	NormalizeDomainBackendProtocols(doc)
	if route["backend_protocol"] != "http" {
		t.Fatalf("WSS backend protocol = %#v, want http", route["backend_protocol"])
	}
	annotations := route["annotations"].(map[string]any)
	if _, exists := annotations["higress.io/backend-protocol"]; exists {
		t.Fatalf("legacy HTTPS backend annotation was retained: %#v", annotations)
	}
	if err := Validate(doc); err != nil {
		t.Fatalf("normalized WSS route is invalid: %v", err)
	}

	route["backend_protocol"] = "https"
	NormalizeDomainBackendProtocols(doc)
	if route["annotations"].(map[string]any)["higress.io/backend-protocol"] != "HTTPS" {
		t.Fatalf("explicit TLS backend was not preserved: %#v", route)
	}
}

func TestNormalizeLegacyHTTPSBackendPreservesExplicitTLSUpstream(t *testing.T) {
	doc := validDocument()
	route := map[string]any{
		"enabled": true, "protocol": "https", "access_type": "domain", "domain": "secure.example.com",
		"gateway": "nginx", "namespace": "apps", "service": "secure-api", "service_port": 8443,
		"tls_enabled": true, "certificate_ref": "web-tls",
		"annotations": map[string]any{"nginx.ingress.kubernetes.io/backend-protocol": "HTTPS"},
	}
	doc["tls"].(map[string]any)["certificates"] = []any{map[string]any{
		"enabled": true, "key": "web-tls", "mode": "existing-secret", "namespace": "apps", "tls_secret_name": "web-tls",
	}}
	doc["domains"] = []any{route}

	NormalizeDomainBackendProtocols(doc)

	if route["backend_protocol"] != "https" {
		t.Fatalf("legacy HTTPS upstream was changed: %#v", route)
	}
	if route["annotations"].(map[string]any)["nginx.ingress.kubernetes.io/backend-protocol"] != "HTTPS" {
		t.Fatalf("NGINX HTTPS backend annotation was not restored: %#v", route)
	}
}

func TestValidateAndNormalizeGRPCRoutes(t *testing.T) {
	doc := validDocument()
	route := map[string]any{
		"enabled": true, "protocol": "grpc", "access_type": "domain", "domain": "etcd-grpc.example.com",
		"gateway": "higress", "namespace": "platform-server", "service": "etcd", "service_port": 2379,
		"tls_enabled": false,
	}
	doc["domains"] = []any{route}

	NormalizeDomainBackendProtocols(doc)

	if route["backend_protocol"] != "grpc" {
		t.Fatalf("gRPC backend protocol = %#v, want grpc", route["backend_protocol"])
	}
	if route["annotations"].(map[string]any)["higress.io/backend-protocol"] != "GRPC" {
		t.Fatalf("Higress gRPC annotation was not generated: %#v", route)
	}
	if err := Validate(doc); err != nil {
		t.Fatalf("valid gRPC route on port 2379 was rejected: %v", err)
	}
}

func TestValidateAndNormalizeGRPCSRoute(t *testing.T) {
	doc := validDocument()
	doc["tls"].(map[string]any)["certificates"] = []any{map[string]any{
		"enabled": true, "key": "grpc-tls", "mode": "existing-secret",
		"namespace": "apps", "tls_secret_name": "grpc-tls",
	}}
	route := map[string]any{
		"enabled": true, "protocol": "grpcs", "access_type": "domain", "domain": "rpc.example.com",
		"gateway": "nginx", "namespace": "apps", "service": "rpc-server", "service_port": 9000,
		"tls_enabled": true, "certificate_ref": "grpc-tls", "backend_protocol": "grpcs",
	}
	doc["domains"] = []any{route}

	NormalizeDomainBackendProtocols(doc)

	if route["annotations"].(map[string]any)["nginx.ingress.kubernetes.io/backend-protocol"] != "GRPCS" {
		t.Fatalf("NGINX gRPCS annotation was not generated: %#v", route)
	}
	if err := Validate(doc); err != nil {
		t.Fatalf("valid gRPCS route was rejected: %v", err)
	}

	route["certificate_ref"] = ""
	if err := Validate(doc); err == nil || !strings.Contains(err.Error(), "unknown TLS certificate") {
		t.Fatalf("gRPCS route without a certificate was accepted: %v", err)
	}
}

func TestDefaultDocumentStartsWithoutApplicationNamespaces(t *testing.T) {
	doc := validDocument()
	namespaces := doc["namespaces"].(map[string]any)
	if len(namespaces) != 0 {
		t.Fatalf("default namespaces should be empty, got %#v", namespaces)
	}
	if err := Validate(doc); err != nil {
		t.Fatalf("empty namespace set should be valid: %v", err)
	}
	templates := doc["alerting"].(map[string]any)["templates"].([]any)
	if len(templates) < 6 {
		t.Fatalf("expected reusable alert templates, got %d", len(templates))
	}
	if intValue(doc["alerting"].(map[string]any)["template_preset_version"]) != alertTemplatePresetVersion {
		t.Fatal("new environments should use the latest alert template preset")
	}
	for _, raw := range templates {
		body := stringValue(raw.(map[string]any)["body"])
		if strings.Contains(body, "\n\n") || strings.Contains(body, "\n>") {
			t.Fatalf("default alert template should use compact Markdown spacing: %q", body)
		}
	}
}

func TestPrometheusDefaultsAreEKSAwareAndCarryEnvironmentLabels(t *testing.T) {
	doc := validDocument()
	// Simulate an existing environment saved before the EKS monitoring fix.
	values := doc["components"].(map[string]any)["catalog"].(map[string]any)["prometheus"].(map[string]any)["values"].(map[string]any)
	delete(values, "kubeControllerManager")
	delete(values, "kubeScheduler")
	delete(values, "kubeEtcd")
	delete(values, "prometheus")

	upgraded := ApplyDefaults(doc, "kbp", "test")
	values = upgraded["components"].(map[string]any)["catalog"].(map[string]any)["prometheus"].(map[string]any)["values"].(map[string]any)
	for _, key := range []string{"kubeControllerManager", "kubeScheduler", "kubeEtcd"} {
		monitor, ok := values[key].(map[string]any)
		if !ok || boolValue(monitor["enabled"]) {
			t.Fatalf("EKS-inaccessible monitor %s must default to disabled: %#v", key, values[key])
		}
	}
	labels := values["prometheus"].(map[string]any)["prometheusSpec"].(map[string]any)["externalLabels"].(map[string]any)
	for key, expected := range map[string]string{"project": "kbp", "environment": "test", "cluster": "kbp-test-eks"} {
		if labels[key] != expected {
			t.Fatalf("Prometheus external label %s=%#v, want %q", key, labels[key], expected)
		}
	}
}

func TestApplyDefaultsUpgradesBundledAlertTemplatesAndPreservesCustomTemplates(t *testing.T) {
	doc := validDocument()
	alerting := doc["alerting"].(map[string]any)
	alerting["template_preset_version"] = 1
	alerting["templates"] = []any{
		map[string]any{"name": "kubernetes-workload-critical", "event_type": "kubernetes-workload", "severity": "critical", "title": "old preset", "body": "old"},
		map[string]any{"name": "team-custom", "event_type": "custom", "severity": "warning", "title": "keep me", "body": "custom body"},
	}

	upgraded := ApplyDefaults(doc, "ops", "test")
	upgradedAlerting := upgraded["alerting"].(map[string]any)
	if intValue(upgradedAlerting["template_preset_version"]) != alertTemplatePresetVersion {
		t.Fatalf("alert preset version was not upgraded: %#v", upgradedAlerting["template_preset_version"])
	}
	templates := upgradedAlerting["templates"].([]any)
	if len(templates) != 7 {
		t.Fatalf("expected six bundled templates and one custom template, got %d", len(templates))
	}
	foundPreset, foundCustom := false, false
	for _, raw := range templates {
		item := raw.(map[string]any)
		switch stringValue(item["name"]) {
		case "kubernetes-workload-critical":
			foundPreset = strings.Contains(stringValue(item["title"]), "StatusText") && stringValue(item["format"]) == "markdown"
		case "team-custom":
			foundCustom = stringValue(item["title"]) == "keep me"
		}
	}
	if !foundPreset || !foundCustom {
		t.Fatalf("preset/custom upgrade result is incorrect: %#v", templates)
	}
}

func TestRDSCredentialDefaultsDoNotRotateRunningLegacyDatabases(t *testing.T) {
	newDocument := validDocument()
	for _, key := range []string{"rds", "aurora"} {
		service := newDocument["data_services"].(map[string]any)[key].(map[string]any)
		if service["credential_management"] != "self-managed" {
			t.Fatalf("new %s should default to self-managed credentials: %#v", key, service)
		}
	}
	if aurora := newDocument["data_services"].(map[string]any)["aurora"].(map[string]any); boolValue(aurora["tls_enabled"]) {
		t.Fatalf("new Aurora should keep mandatory client TLS disabled until explicitly enabled: %#v", aurora)
	}
	auroraDefaults := newDocument["data_services"].(map[string]any)["aurora"].(map[string]any)
	if boolValue(auroraDefaults["backtrack_enabled"]) || intValue(auroraDefaults["backtrack_window_hours"]) != 72 {
		t.Fatalf("Aurora backtrack must be opt-in with a 72-hour default window: %#v", auroraDefaults)
	}

	legacy := validDocument()
	rds := legacy["data_services"].(map[string]any)["rds"].(map[string]any)
	rds["enabled"] = true
	delete(rds, "credential_management")
	ApplyDefaults(legacy, "ops", "test")
	if rds["credential_management"] != "aws-managed" {
		t.Fatalf("running legacy RDS must retain AWS-managed credentials: %#v", rds)
	}

	legacyDisabled := validDocument()
	disabledRDS := legacyDisabled["data_services"].(map[string]any)["rds"].(map[string]any)
	delete(disabledRDS, "credential_management")
	ApplyDefaults(legacyDisabled, "ops", "test")
	if disabledRDS["credential_management"] != "self-managed" {
		t.Fatalf("unused legacy RDS should adopt the new-create default: %#v", disabledRDS)
	}
}

func TestValidateExistingVPCSelection(t *testing.T) {
	doc := validDocument()
	network := doc["network"].(map[string]any)
	network["mode"] = "existing"
	network["existing_vpc_id"] = "vpc-0123456789abcdef0"
	network["existing_vpc_cidr"] = "10.80.0.0/16"
	network["existing_workload_subnet_ids"] = []any{"subnet-0123456789abcdef0", "subnet-1123456789abcdef0"}
	network["existing_data_subnet_ids"] = []any{"subnet-2123456789abcdef0", "subnet-3123456789abcdef0"}
	network["availability_zones"] = []any{"ap-south-1a", "ap-south-1b"}
	for _, raw := range doc["eks"].(map[string]any)["node_groups"].(map[string]any) {
		raw.(map[string]any)["availability_zones"] = []any{"ap-south-1a", "ap-south-1b"}
	}
	if err := Validate(doc); err != nil {
		t.Fatalf("valid existing VPC selection was rejected: %v", err)
	}
	network["existing_data_subnet_ids"] = []any{"subnet-2123456789abcdef0", "subnet-2123456789abcdef0"}
	if err := Validate(doc); err == nil || !strings.Contains(err.Error(), "duplicate subnet ID") {
		t.Fatalf("duplicate existing subnet was accepted: %v", err)
	}
}

func TestValidateNATGatewayMode(t *testing.T) {
	doc := validDocument()
	network := doc["network"].(map[string]any)
	for _, mode := range []string{"when-private", "always", "disabled"} {
		network["nat_gateway_mode"] = mode
		if err := Validate(doc); err != nil {
			t.Fatalf("valid NAT Gateway mode %q was rejected: %v", mode, err)
		}
	}
	network["nat_gateway_mode"] = "sometimes"
	if err := Validate(doc); err == nil || !strings.Contains(err.Error(), "nat_gateway_mode") {
		t.Fatalf("invalid NAT Gateway mode was accepted: %v", err)
	}
}

func TestValidateNodeGroupSubnetType(t *testing.T) {
	doc := validDocument()
	group := doc["eks"].(map[string]any)["node_groups"].(map[string]any)["business-workload"].(map[string]any)
	for _, subnetType := range []string{"public", "private"} {
		group["subnet_type"] = subnetType
		if err := Validate(doc); err != nil {
			t.Fatalf("valid node group subnet type %q was rejected: %v", subnetType, err)
		}
	}
	group["subnet_type"] = "isolated"
	if err := Validate(doc); err == nil || !strings.Contains(err.Error(), "subnet_type") {
		t.Fatalf("invalid node group subnet type was accepted: %v", err)
	}
}

func TestValidateNodeGroupRequiresInstanceTypes(t *testing.T) {
	doc := validDocument()
	group := doc["eks"].(map[string]any)["node_groups"].(map[string]any)["business-workload"].(map[string]any)
	group["instance_types"] = []any{}
	if err := Validate(doc); err == nil || !strings.Contains(err.Error(), "必须选择 1 到 20 个") {
		t.Fatalf("empty node group instance types returned an unclear error: %v", err)
	}

	group["instance_types"] = []any{"m7i.large", ""}
	if err := Validate(doc); err == nil || !strings.Contains(err.Error(), "包含空的实例类型") {
		t.Fatalf("blank node group instance type was accepted: %v", err)
	}
}

func TestValidateNodeGroupZoneMustExistInSelectedNetwork(t *testing.T) {
	doc := validDocument()
	group := doc["eks"].(map[string]any)["node_groups"].(map[string]any)["business-workload"].(map[string]any)
	group["availability_zones"] = []any{"ap-south-1z"}
	if err := Validate(doc); err == nil || !strings.Contains(err.Error(), "unavailable") {
		t.Fatalf("unselected node group zone was accepted: %v", err)
	}
}

func TestDocumentDBCapacityValidation(t *testing.T) {
	doc := validDocument()
	documentdb := doc["data_services"].(map[string]any)["documentdb"].(map[string]any)
	documentdb["enabled"] = true
	documentdb["instance_count"] = 0
	if err := Validate(doc); err == nil || !strings.Contains(err.Error(), "documentdb.instance_count") {
		t.Fatalf("unexpected validation error: %v", err)
	}
}

func TestApplyDefaultsMigratesDeprecatedAmazonMQInstance(t *testing.T) {
	doc := validDocument()
	amazonMQ := doc["data_services"].(map[string]any)["amazon_mq"].(map[string]any)
	amazonMQ["enabled"] = true
	amazonMQ["host_instance_type"] = "mq.t3.micro"

	upgraded := ApplyDefaults(doc, "ops", "test")
	got := upgraded["data_services"].(map[string]any)["amazon_mq"].(map[string]any)["host_instance_type"]
	if got != "mq.m7g.medium" {
		t.Fatalf("deprecated Amazon MQ instance was not migrated: %v", got)
	}
	if err := Validate(upgraded); err != nil {
		t.Fatalf("migrated Amazon MQ configuration is invalid: %v", err)
	}
}

func TestApplyDefaultsDisablesTLSOnlyForUnusedLegacyCache(t *testing.T) {
	doc := validDocument()
	delete(doc, "data_service_defaults_version")
	elasticache := doc["data_services"].(map[string]any)["elasticache"].(map[string]any)
	elasticache["tls_enabled"] = true
	ApplyDefaults(doc, "ops", "test")
	if boolValue(elasticache["tls_enabled"]) {
		t.Fatal("disabled legacy cache should adopt the new TLS-off default")
	}

	doc = validDocument()
	delete(doc, "data_service_defaults_version")
	elasticache = doc["data_services"].(map[string]any)["elasticache"].(map[string]any)
	elasticache["enabled"] = true
	elasticache["tls_enabled"] = true
	ApplyDefaults(doc, "ops", "test")
	if !boolValue(elasticache["tls_enabled"]) {
		t.Fatal("a possibly running legacy cache must not have TLS changed silently")
	}
}

func TestApplyDefaultsAlignsElastiCacheDefaultParameterGroup(t *testing.T) {
	tests := []struct {
		engine, version, expected string
	}{
		{"redis", "7.1", "default.redis7.cluster.on"},
		{"redis", "6.2", "default.redis6.x.cluster.on"},
		{"redis", "5.0.6", "default.redis5.0.cluster.on"},
		{"valkey", "8.2", "default.valkey8.cluster.on"},
		{"valkey", "9.1", "default.valkey9.cluster.on"},
	}
	for _, item := range tests {
		doc := validDocument()
		elasticache := doc["data_services"].(map[string]any)["elasticache"].(map[string]any)
		elasticache["enabled"] = true
		elasticache["engine"] = item.engine
		elasticache["engine_version"] = item.version
		elasticache["parameter_group_name"] = "default.valkey8.cluster.on"
		ApplyDefaults(doc, "ops", "test")
		if got := elasticache["parameter_group_name"]; got != item.expected {
			t.Fatalf("%s %s parameter group = %v, want %s", item.engine, item.version, got, item.expected)
		}
		if err := Validate(doc); err != nil {
			t.Fatalf("normalized %s %s configuration is invalid: %v", item.engine, item.version, err)
		}
	}

	doc := validDocument()
	elasticache := doc["data_services"].(map[string]any)["elasticache"].(map[string]any)
	elasticache["enabled"] = true
	elasticache["engine"] = "redis"
	elasticache["engine_version"] = "7.1"
	elasticache["parameter_group_name"] = "company-redis7-cluster"
	ApplyDefaults(doc, "ops", "test")
	if got := elasticache["parameter_group_name"]; got != "company-redis7-cluster" {
		t.Fatalf("customer parameter group must be preserved, got %v", got)
	}
}

func TestApplyDefaultsMigratesElastiCacheCapacityWithoutChangingTotalNodes(t *testing.T) {
	doc := validDocument()
	doc["data_service_defaults_version"] = 2
	elasticache := doc["data_services"].(map[string]any)["elasticache"].(map[string]any)
	delete(elasticache, "nodes_per_shard")
	elasticache["num_node_groups"] = 5
	elasticache["replicas_per_node_group"] = 3

	ApplyDefaults(doc, "ops", "test")
	if got := intValue(elasticache["nodes_per_shard"]); got != 4 {
		t.Fatalf("nodes_per_shard = %d, want 4 to preserve one primary plus three replicas", got)
	}
	if got := intValue(elasticache["replicas_per_node_group"]); got != 3 {
		t.Fatalf("replicas_per_node_group = %d, want 3", got)
	}
	if got := intValue(doc["data_service_defaults_version"]); got != 3 {
		t.Fatalf("data_service_defaults_version = %d, want 3", got)
	}

	// The user-facing value is the exact total nodes per shard. Setting it to
	// one must derive zero replicas, so five shards converge to five nodes.
	elasticache["nodes_per_shard"] = 1
	ApplyDefaults(doc, "ops", "test")
	if got := intValue(elasticache["replicas_per_node_group"]); got != 0 {
		t.Fatalf("replicas_per_node_group = %d, want 0", got)
	}
}

func TestValidateElastiCacheExactCapacity(t *testing.T) {
	doc := validDocument()
	elasticache := doc["data_services"].(map[string]any)["elasticache"].(map[string]any)
	elasticache["enabled"] = true
	elasticache["num_node_groups"] = 5
	elasticache["nodes_per_shard"] = 1
	elasticache["replicas_per_node_group"] = 0
	if err := Validate(doc); err != nil {
		t.Fatalf("valid five-node cache was rejected: %v", err)
	}

	elasticache["replicas_per_node_group"] = 1
	if err := Validate(doc); err == nil || !strings.Contains(err.Error(), "nodes_per_shard - 1") {
		t.Fatalf("inconsistent cache capacity was accepted: %v", err)
	}

	elasticache["replicas_per_node_group"] = 0
	elasticache["nodes_per_shard"] = 6
	elasticache["num_node_groups"] = 100
	if err := Validate(doc); err == nil || !strings.Contains(err.Error(), "total node count") {
		t.Fatalf("oversized cache capacity was accepted: %v", err)
	}
}

func TestValidateRejectsMismatchedElastiCacheDefaultParameterGroup(t *testing.T) {
	doc := validDocument()
	elasticache := doc["data_services"].(map[string]any)["elasticache"].(map[string]any)
	elasticache["enabled"] = true
	elasticache["engine"] = "redis"
	elasticache["engine_version"] = "7.1"
	elasticache["parameter_group_name"] = "default.valkey8.cluster.on"
	if err := Validate(doc); err == nil || !strings.Contains(err.Error(), "incompatible") {
		t.Fatalf("mismatched default parameter group was accepted: %v", err)
	}
}

func TestApplyDefaultsRepairsFormerJenkinsAndLokiDefaults(t *testing.T) {
	doc := validDocument()
	catalog := doc["components"].(map[string]any)["catalog"].(map[string]any)
	jenkins := catalog["jenkins"].(map[string]any)
	jenkins["chart_version"] = "5.8.120"
	loki := catalog["loki"].(map[string]any)
	loki["chart_version"] = "6.37.0"
	loki["values"] = map[string]any{}

	upgraded := ApplyDefaults(doc, "ops", "test")
	upgradedCatalog := upgraded["components"].(map[string]any)["catalog"].(map[string]any)
	if got := upgradedCatalog["jenkins"].(map[string]any)["chart_version"]; got != "5.9.34" {
		t.Fatalf("Jenkins chart default was not migrated: %v", got)
	}
	if got := upgradedCatalog["loki"].(map[string]any)["chart_version"]; got != lokiChartVersion {
		t.Fatalf("Loki chart default was not migrated: %v", got)
	}
	values := upgradedCatalog["loki"].(map[string]any)["values"].(map[string]any)
	if values["deploymentMode"] != "SingleBinary" || values["loki"].(map[string]any)["storage"].(map[string]any)["type"] != "filesystem" {
		t.Fatalf("Loki standalone defaults were not repaired: %#v", values)
	}
	persistence := values["singleBinary"].(map[string]any)["persistence"].(map[string]any)
	if persistence["storageClass"] != "gp3" || persistence["size"] != "20Gi" || persistence["enabled"] != true {
		t.Fatalf("Loki EBS persistence defaults were not repaired: %#v", persistence)
	}
	if got := values["loki"].(map[string]any)["limits_config"].(map[string]any)["retention_period"]; got != "168h" {
		t.Fatalf("Loki retention default was not repaired: %v", got)
	}
	limits := values["loki"].(map[string]any)["limits_config"].(map[string]any)
	if limits["ingestion_rate_mb"] != 16 || limits["ingestion_burst_size_mb"] != 32 {
		t.Fatalf("Loki ingestion limits were not repaired: %#v", limits)
	}
	ingester := values["loki"].(map[string]any)["ingester"].(map[string]any)
	if ingester["concurrent_flushes"] != 4 || ingester["wal"].(map[string]any)["replay_memory_ceiling"] != "256MB" {
		t.Fatalf("Loki WAL recovery defaults were not repaired: %#v", ingester)
	}
	resources := values["singleBinary"].(map[string]any)["resources"].(map[string]any)
	if resources["limits"].(map[string]any)["memory"] != "5Gi" {
		t.Fatalf("Loki memory safety defaults were not repaired: %#v", resources)
	}
	memberlist := values["memberlist"].(map[string]any)["service"].(map[string]any)
	if memberlist["publishNotReadyAddresses"] != true {
		t.Fatalf("Loki memberlist startup defaults were not repaired: %#v", memberlist)
	}
}

func TestOpenTelemetryCollectorSupportsStandaloneAndClusterModes(t *testing.T) {
	doc := DefaultDocument("ops", "test")
	catalog := doc["components"].(map[string]any)["catalog"].(map[string]any)
	collector, ok := catalog["opentelemetry_collector"].(map[string]any)
	if !ok {
		t.Fatal("OpenTelemetry Collector is missing from the built-in catalog")
	}
	if collector["chart_version"] != observabilityOTelChartVersion || collector["builtin_chart"] != "observability-otel" {
		t.Fatalf("unexpected Collector chart version: %v", collector["chart_version"])
	}
	if got := collector["replica_paths"].([]any); len(got) != 1 || got[0] != "replicaCount" {
		t.Fatalf("Collector replica path is not wired to Helm: %#v", got)
	}
	values := collector["values"].(map[string]any)
	if values["fullnameOverride"] != "opentelemetry-collector" || values["project"] != "ops" || values["environment"] != "test" {
		t.Fatalf("Collector gateway defaults are invalid: %#v", values)
	}
	storage := values["storage"].(map[string]any)
	if storage["className"] != "gp3" || storage["initialSize"] != "10Gi" || storage["queueSize"] != 1000 || storage["retainOnDelete"] != true {
		t.Fatalf("Collector persistent queue defaults are invalid: %#v", storage)
	}
	agent := values["agent"].(map[string]any)
	destinations := values["destinations"].(map[string]any)
	if agent["enabled"] != true || destinations["jaeger"].(map[string]any)["enabled"] != true || destinations["tempo"].(map[string]any)["enabled"] != false || destinations["loki"].(map[string]any)["enabled"] != true {
		t.Fatalf("Collector unified agent/exporter defaults are invalid: agent=%#v destinations=%#v", agent, destinations)
	}
	elasticsearch := values["elasticsearch"].(map[string]any)
	elasticsearchStorage := elasticsearch["storage"].(map[string]any)
	if elasticsearch["enabled"] != false || elasticsearch["mode"] != "standalone" || elasticsearch["replicas"] != 1 || elasticsearchStorage["initialSize"] != "50Gi" || elasticsearchStorage["expandedSize"] != "" {
		t.Fatalf("dedicated OpenTelemetry Elasticsearch defaults are invalid: %#v", elasticsearch)
	}

	collector["enabled"] = true
	for _, dependency := range []string{"prometheus", "loki", "jaeger"} {
		catalog[dependency].(map[string]any)["enabled"] = true
	}
	collector["deployment_mode"] = "standalone"
	collector["replicas"] = 1
	if err := Validate(doc); err != nil {
		t.Fatalf("valid standalone Collector was rejected: %v", err)
	}
	collector["deployment_mode"] = "cluster"
	collector["replicas"] = 3
	if err := Validate(doc); err != nil {
		t.Fatalf("valid clustered Collector was rejected: %v", err)
	}
	storage["initialSize"] = "10"
	if err := Validate(doc); err == nil || !strings.Contains(err.Error(), "initialSize") {
		t.Fatalf("invalid Collector disk size was accepted: %v", err)
	}
	storage["initialSize"] = "10Gi"
	storage["expandedSize"] = "5Gi"
	if err := Validate(doc); err == nil || !strings.Contains(err.Error(), "expandedSize") {
		t.Fatalf("Collector expanded size smaller than its initial disk was accepted: %v", err)
	}
	storage["expandedSize"] = "20Gi"
	doc["components"].(map[string]any)["eks_addons"].(map[string]any)["ebs_csi_driver"] = false
	if err := Validate(doc); err == nil || !strings.Contains(err.Error(), "EBS CSI") {
		t.Fatalf("Collector persistence without EBS CSI was accepted: %v", err)
	}
}

func TestOpenTelemetryDedicatedElasticsearchValidation(t *testing.T) {
	doc := DefaultDocument("ops", "test")
	catalog := doc["components"].(map[string]any)["catalog"].(map[string]any)
	collector := catalog["opentelemetry_collector"].(map[string]any)
	collector["enabled"] = true
	for _, dependency := range []string{"prometheus", "loki", "jaeger"} {
		catalog[dependency].(map[string]any)["enabled"] = true
	}
	values := collector["values"].(map[string]any)
	elasticsearch := values["elasticsearch"].(map[string]any)
	elasticsearch["enabled"] = true
	values["destinations"].(map[string]any)["elasticsearch"].(map[string]any)["enabled"] = true
	elasticsearch["mode"] = "cluster"
	elasticsearch["replicas"] = 3
	if err := Validate(doc); err != nil {
		t.Fatalf("valid dedicated Elasticsearch cluster was rejected: %v", err)
	}
	elasticsearch["replicas"] = 2
	if err := Validate(doc); err == nil || !strings.Contains(err.Error(), "3, 5, 7 or 9") {
		t.Fatalf("invalid dedicated Elasticsearch node count was accepted: %v", err)
	}
	elasticsearch["replicas"] = 3
	storage := elasticsearch["storage"].(map[string]any)
	storage["expandedSize"] = "20Gi"
	if err := Validate(doc); err == nil || !strings.Contains(err.Error(), "expandedSize") {
		t.Fatalf("dedicated Elasticsearch shrink target was accepted: %v", err)
	}
	storage["expandedSize"] = ""
	elasticsearch["javaOpts"] = "-Xms1g -Xmx3g"
	if err := Validate(doc); err == nil || !strings.Contains(err.Error(), "equal") {
		t.Fatalf("mismatched dedicated Elasticsearch JVM Heap was accepted: %v", err)
	}
}

func TestJaegerDefaultsProvidePersistentAuthenticatedTraceUI(t *testing.T) {
	doc := DefaultDocument("ops", "test")
	catalog := doc["components"].(map[string]any)["catalog"].(map[string]any)
	jaeger := catalog["jaeger"].(map[string]any)
	if jaeger["builtin_chart"] != "jaeger-stack" || jaeger["chart_version"] != jaegerStackChartVersion {
		t.Fatalf("Jaeger built-in chart metadata is invalid: %#v", jaeger)
	}
	values := jaeger["values"].(map[string]any)
	storage := values["storage"].(map[string]any)
	if storage["backend"] != "badger" || storage["initialSize"] != "20Gi" || storage["retention"] != "168h" {
		t.Fatalf("Jaeger test storage defaults are invalid: %#v", storage)
	}
	auth := values["basicAuth"].(map[string]any)
	if auth["enabled"] != true || auth["username"] != "admin" || jaeger["secret_name"] != "jaeger-access" {
		t.Fatalf("Jaeger UI authentication defaults are invalid: component=%#v auth=%#v", jaeger, auth)
	}
	jaeger["enabled"] = true
	catalog["prometheus"].(map[string]any)["enabled"] = true
	if err := Validate(doc); err != nil {
		t.Fatalf("valid Badger-backed Jaeger was rejected: %v", err)
	}
	jaeger["deployment_mode"] = "cluster"
	jaeger["replicas"] = 3
	if err := Validate(doc); err == nil || !strings.Contains(err.Error(), "Badger") {
		t.Fatalf("clustered Badger Jaeger was accepted: %v", err)
	}
	storage["backend"] = "elasticsearch"
	if err := Validate(doc); err == nil || !strings.Contains(err.Error(), "OpenTelemetry Collector") {
		t.Fatalf("Jaeger Elasticsearch without OpenTelemetry storage was accepted: %v", err)
	}
	collector := catalog["opentelemetry_collector"].(map[string]any)
	collector["enabled"] = true
	catalog["loki"].(map[string]any)["enabled"] = true
	collector["values"].(map[string]any)["elasticsearch"].(map[string]any)["enabled"] = true
	if err := Validate(doc); err != nil {
		t.Fatalf("valid Elasticsearch-backed Jaeger was rejected: %v", err)
	}
}

func TestApplyDefaultsMigratesOpenTelemetryCollectorToPersistentWAL(t *testing.T) {
	doc := validDocument()
	collector := doc["components"].(map[string]any)["catalog"].(map[string]any)["opentelemetry_collector"].(map[string]any)
	collector["values"] = map[string]any{
		"storage": map[string]any{"initialSize": "40Gi", "className": "gp3", "queueSize": 2500},
	}

	upgraded := ApplyDefaults(doc, "payments", "prod")
	values := upgraded["components"].(map[string]any)["catalog"].(map[string]any)["opentelemetry_collector"].(map[string]any)["values"].(map[string]any)
	if values["project"] != "payments" || values["environment"] != "prod" {
		t.Fatalf("Collector resource attributes were not migrated: %#v", values)
	}
	storage := values["storage"].(map[string]any)
	if storage["initialSize"] != "40Gi" || storage["queueSize"] != 2500 {
		t.Fatalf("Collector WAL settings were not preserved: %#v", storage)
	}
	for _, obsolete := range []string{"mode", "statefulset", "config", "presets"} {
		if _, exists := values[obsolete]; exists {
			t.Fatalf("obsolete upstream Collector value %q survived migration", obsolete)
		}
	}
}

func TestApplyDefaultsMigratesDedicatedElasticsearchLegacySize(t *testing.T) {
	doc := validDocument()
	collector := doc["components"].(map[string]any)["catalog"].(map[string]any)["opentelemetry_collector"].(map[string]any)
	values := collector["values"].(map[string]any)
	values["elasticsearch"] = map[string]any{
		"enabled": false,
		"storage": map[string]any{"className": "gp3", "size": "75Gi", "retainOnDelete": true},
	}
	upgraded := ApplyDefaults(doc, "ops", "test")
	storage := upgraded["components"].(map[string]any)["catalog"].(map[string]any)["opentelemetry_collector"].(map[string]any)["values"].(map[string]any)["elasticsearch"].(map[string]any)["storage"].(map[string]any)
	if storage["initialSize"] != "75Gi" || storage["expandedSize"] != "" {
		t.Fatalf("legacy dedicated Elasticsearch disk was not migrated: %#v", storage)
	}
	if _, exists := storage["size"]; exists {
		t.Fatalf("legacy mutable Elasticsearch size survived migration: %#v", storage)
	}
}

func TestDatabaseManagementComponentsHaveSafeDefaults(t *testing.T) {
	doc := DefaultDocument("ops", "test")
	catalog := doc["components"].(map[string]any)["catalog"].(map[string]any)
	for _, key := range []string{"bytebase", "redisinsight", "etcd_workbench"} {
		component, ok := catalog[key].(map[string]any)
		expectedChart := map[string]string{"etcd_workbench": "etcd-workbench"}[key]
		if expectedChart == "" {
			expectedChart = key
		}
		if !ok || component["builtin_chart"] != expectedChart || component["standalone_only"] != true {
			t.Fatalf("%s safe built-in defaults are missing: %#v", key, component)
		}
		if component["enabled"] != false {
			t.Fatalf("%s must remain opt-in", key)
		}
		if key == "etcd_workbench" {
			values := component["values"].(map[string]any)
			image := values["image"].(map[string]any)
			persistence := values["persistence"].(map[string]any)
			if !strings.Contains(image["tag"].(string), "@sha256:") || persistence["retainOnDelete"] != true {
				t.Fatalf("Etcd Workbench image pinning or PVC retention is unsafe: %#v", values)
			}
		}
	}
}

func TestDatabaseManagementComponentsRequireTheirTargetService(t *testing.T) {
	doc := DefaultDocument("ops", "test")
	catalog := doc["components"].(map[string]any)["catalog"].(map[string]any)
	catalog["bytebase"].(map[string]any)["enabled"] = true
	if err := Validate(doc); err == nil || !strings.Contains(err.Error(), "MySQL") {
		t.Fatalf("Bytebase without MySQL was accepted: %v", err)
	}
	catalog["bytebase"].(map[string]any)["enabled"] = false
	catalog["redisinsight"].(map[string]any)["enabled"] = true
	if err := Validate(doc); err == nil || !strings.Contains(err.Error(), "Redis") {
		t.Fatalf("RedisInsight without Redis was accepted: %v", err)
	}
	catalog["redisinsight"].(map[string]any)["enabled"] = false
	catalog["etcd_workbench"].(map[string]any)["enabled"] = true
	if err := Validate(doc); err == nil || !strings.Contains(err.Error(), "etcd") {
		t.Fatalf("Etcd Workbench without etcd was accepted: %v", err)
	}
}

func TestDatabaseManagementConsoleRouteRequiresTLS(t *testing.T) {
	doc := DefaultDocument("ops", "test")
	doc["domains"] = []any{map[string]any{
		"enabled": true, "protocol": "http", "access_type": "domain", "domain": "redis.example.com",
		"namespace": "platform-server", "service": "redisinsight", "service_port": 80, "gateway": "higress", "tls_enabled": false,
	}}
	if err := Validate(doc); err == nil || !strings.Contains(err.Error(), "must use HTTPS") {
		t.Fatalf("insecure RedisInsight route was accepted: %v", err)
	}
}

func TestEtcdWorkbenchRouteRequiresTLS(t *testing.T) {
	doc := DefaultDocument("ops", "test")
	doc["domains"] = []any{map[string]any{
		"enabled": true, "protocol": "http", "access_type": "domain", "domain": "etcd.example.com",
		"namespace": "platform-server", "service": "etcd-workbench", "service_port": 8002, "gateway": "higress", "tls_enabled": false,
	}}
	if err := Validate(doc); err == nil || !strings.Contains(err.Error(), "must use HTTPS") {
		t.Fatalf("insecure Etcd Workbench route was accepted: %v", err)
	}
}

func TestApplyDefaultsPreservesLegacyEtcdWebUI(t *testing.T) {
	doc := DefaultDocument("ops", "test")
	etcd := doc["components"].(map[string]any)["etcd"].(map[string]any)
	etcd["web_ui"] = map[string]any{"enabled": true, "username": "legacy-admin", "frontend_image": "legacy/image:v1"}

	upgraded := ApplyDefaults(doc, "ops", "test")
	webUI := upgraded["components"].(map[string]any)["etcd"].(map[string]any)["web_ui"].(map[string]any)
	if webUI["username"] != "legacy-admin" || webUI["frontend_image"] != "legacy/image:v1" {
		t.Fatalf("existing etcd WebUI settings were overwritten: %#v", webUI)
	}
	workbench := upgraded["components"].(map[string]any)["catalog"].(map[string]any)["etcd_workbench"].(map[string]any)
	if workbench["enabled"] != false || workbench["builtin_chart"] != "etcd-workbench" {
		t.Fatalf("Etcd Workbench must be added independently and disabled: %#v", workbench)
	}
}

func TestApplyDefaultsTurnsEnabledLokiIntoCompleteLoggingStack(t *testing.T) {
	doc := DefaultDocument("ops", "test")
	catalog := doc["components"].(map[string]any)["catalog"].(map[string]any)
	catalog["loki"].(map[string]any)["enabled"] = true
	catalog["prometheus"].(map[string]any)["enabled"] = false
	delete(doc["namespaces"].(map[string]any), "monitoring")

	upgraded := ApplyDefaults(doc, "ops", "test")
	upgradedCatalog := upgraded["components"].(map[string]any)["catalog"].(map[string]any)
	if !boolValue(upgradedCatalog["prometheus"].(map[string]any)["enabled"]) {
		t.Fatal("enabled Loki did not automatically enable Prometheus + Grafana")
	}
	if _, found := upgraded["namespaces"].(map[string]any)["monitoring"]; !found {
		t.Fatal("complete logging stack did not ensure its monitoring namespace")
	}
	if err := Validate(upgraded); err != nil {
		t.Fatalf("complete logging stack defaults are invalid: %v", err)
	}
}

func TestApplyDefaultsEnablesManagedGrafanaDashboardDiscovery(t *testing.T) {
	doc := DefaultDocument("ops", "test")
	prometheus := doc["components"].(map[string]any)["catalog"].(map[string]any)["prometheus"].(map[string]any)
	prometheus["values"] = map[string]any{}

	upgraded := ApplyDefaults(doc, "ops", "test")
	values := upgraded["components"].(map[string]any)["catalog"].(map[string]any)["prometheus"].(map[string]any)["values"].(map[string]any)
	grafana := values["grafana"].(map[string]any)
	sidecar := grafana["sidecar"].(map[string]any)
	dashboards := sidecar["dashboards"].(map[string]any)
	datasources := sidecar["datasources"].(map[string]any)
	if dashboards["enabled"] != true || dashboards["label"] != "grafana_dashboard" || dashboards["searchNamespace"] != "ALL" {
		t.Fatalf("Grafana dashboard discovery defaults were not repaired: %#v", dashboards)
	}
	if datasources["uid"] != "prometheus" || datasources["defaultDatasourceEnabled"] != true {
		t.Fatalf("Prometheus datasource defaults were not repaired: %#v", datasources)
	}
}

func TestApplyDefaultsMigratesBuiltInDataServiceChart(t *testing.T) {
	doc := validDocument()
	catalog := doc["components"].(map[string]any)["catalog"].(map[string]any)
	catalog["mysql"].(map[string]any)["chart_version"] = "0.1.0"

	upgraded := ApplyDefaults(doc, "ops", "test")
	got := upgraded["components"].(map[string]any)["catalog"].(map[string]any)["mysql"].(map[string]any)["chart_version"]
	if got != dataServiceChartVersion {
		t.Fatalf("built-in MySQL chart was not migrated: %v", got)
	}
}

func TestApplyDefaultsPreservesLokiStorageOverrides(t *testing.T) {
	doc := validDocument()
	loki := doc["components"].(map[string]any)["catalog"].(map[string]any)["loki"].(map[string]any)
	loki["values"] = map[string]any{
		"singleBinary": map[string]any{"persistence": map[string]any{"size": "80Gi"}},
	}

	upgraded := ApplyDefaults(doc, "ops", "test")
	values := upgraded["components"].(map[string]any)["catalog"].(map[string]any)["loki"].(map[string]any)["values"].(map[string]any)
	persistence := values["singleBinary"].(map[string]any)["persistence"].(map[string]any)
	if persistence["size"] != "80Gi" || persistence["storageClass"] != "gp3" || persistence["enabled"] != true {
		t.Fatalf("Loki custom disk size was overwritten or defaults were not merged: %#v", persistence)
	}
}

func TestValidateLokiPersistentStorage(t *testing.T) {
	doc := DefaultDocument("ops", "test")
	components := doc["components"].(map[string]any)
	loki := components["catalog"].(map[string]any)["loki"].(map[string]any)
	loki["enabled"] = true
	if err := Validate(doc); err != nil {
		t.Fatalf("valid Loki EBS persistence was rejected: %v", err)
	}
	persistence := loki["values"].(map[string]any)["singleBinary"].(map[string]any)["persistence"].(map[string]any)
	persistence["size"] = "20"
	if err := Validate(doc); err == nil || !strings.Contains(err.Error(), "20Gi") {
		t.Fatalf("invalid Loki disk size was accepted: %v", err)
	}
	persistence["size"] = "20Gi"
	persistence["enabled"] = false
	if err := Validate(doc); err == nil || !strings.Contains(err.Error(), "persistent volume") {
		t.Fatalf("ephemeral Loki filesystem storage was accepted: %v", err)
	}
	persistence["enabled"] = true
	components["eks_addons"].(map[string]any)["ebs_csi_driver"] = false
	if err := Validate(doc); err == nil || !strings.Contains(err.Error(), "EBS CSI") {
		t.Fatalf("Loki persistence without EBS CSI was accepted: %v", err)
	}
}

func TestValidateClickVisualManagedStorageClaims(t *testing.T) {
	doc := DefaultDocument("ops", "test")
	stack := doc["components"].(map[string]any)["catalog"].(map[string]any)["clickvisual_stack"].(map[string]any)
	stack["enabled"] = true
	doc = ApplyDefaults(doc, "ops", "test")
	if err := Validate(doc); err != nil {
		t.Fatalf("valid ClickVisual logging stack was rejected: %v", err)
	}

	values := stack["values"].(map[string]any)
	kafkaStorage := values["kafka"].(map[string]any)["storage"].(map[string]any)
	kafkaStorage["activeClaims"] = []any{"valid-kafka-pvc", "../../other-namespace"}
	if err := Validate(doc); err == nil || !strings.Contains(err.Error(), "activeClaims") {
		t.Fatalf("unsafe Kafka replacement PVC name was accepted: %v", err)
	}

	kafkaStorage["activeClaims"] = []any{}
	clickhouseStorage := values["clickhouse"].(map[string]any)["storage"].(map[string]any)
	clickhouseStorage["activeClaim"] = "invalid/claim"
	if err := Validate(doc); err == nil || !strings.Contains(err.Error(), "activeClaim") {
		t.Fatalf("unsafe ClickHouse replacement PVC name was accepted: %v", err)
	}
}

func TestValidateClickVisualCollectionScope(t *testing.T) {
	doc := DefaultDocument("ops", "test")
	stack := doc["components"].(map[string]any)["catalog"].(map[string]any)["clickvisual_stack"].(map[string]any)
	stack["enabled"] = true
	doc = ApplyDefaults(doc, "ops", "test")
	collection := stack["values"].(map[string]any)["collection"].(map[string]any)
	collection["includeNamespaces"] = []any{"ops-test", "platform-server"}
	collection["excludeNamespaces"] = []any{"ops-test-logs-system"}
	collection["includeServices"] = []any{"gateway", "platform-external"}
	collection["excludeServices"] = []any{"health-probe"}
	if err := Validate(doc); err != nil {
		t.Fatalf("valid ClickVisual collection scope was rejected: %v", err)
	}

	collection["includeServices"] = []any{"gateway|.*"}
	if err := Validate(doc); err == nil || !strings.Contains(err.Error(), "includeServices") {
		t.Fatalf("unsafe service filter was accepted: %v", err)
	}

	collection["includeServices"] = "gateway"
	if err := Validate(doc); err == nil || !strings.Contains(err.Error(), "must be an array") {
		t.Fatalf("non-array service filter was accepted: %v", err)
	}
}

func TestValidateEFKStackAndCollectionScope(t *testing.T) {
	doc := DefaultDocument("ops", "test")
	stack := doc["components"].(map[string]any)["catalog"].(map[string]any)["efk_stack"].(map[string]any)
	stack["enabled"] = true
	doc = ApplyDefaults(doc, "ops", "test")
	values := stack["values"].(map[string]any)
	collection := values["collection"].(map[string]any)
	collection["includeNamespaces"] = []any{"ops-test", "platform-server"}
	collection["excludeNamespaces"] = []any{"kube-system"}
	collection["includeServices"] = []any{"gateway", "platform-external"}
	collection["excludeServices"] = []any{"health-probe"}
	if err := Validate(doc); err != nil {
		t.Fatalf("valid EFK logging stack was rejected: %v", err)
	}

	collection["includeServices"] = []any{"gateway|.*"}
	if err := Validate(doc); err == nil || !strings.Contains(err.Error(), "includeServices") {
		t.Fatalf("unsafe EFK service filter was accepted: %v", err)
	}
	collection["includeServices"] = []any{}
	storage := values["elasticsearch"].(map[string]any)["storage"].(map[string]any)
	storage["size"] = "5Gi"
	if err := Validate(doc); err == nil || !strings.Contains(err.Error(), "10Gi") {
		t.Fatalf("undersized EFK disk was accepted: %v", err)
	}
}

func TestApplyDefaultsAddsAndPreservesEnabledComponentNamespaces(t *testing.T) {
	doc := DefaultDocument("ops", "prod")
	catalog := doc["components"].(map[string]any)["catalog"].(map[string]any)
	catalog["jenkins"].(map[string]any)["enabled"] = true
	catalog["higress"].(map[string]any)["enabled"] = true

	doc = ApplyDefaults(doc, "ops", "prod")
	namespaces := doc["namespaces"].(map[string]any)
	if _, exists := namespaces["platform-server"]; !exists {
		t.Fatal("enabled component Namespace was not added to the environment")
	}
	if _, exists := namespaces["higress-system"]; !exists {
		t.Fatal("enabled Higress Namespace was not added to the environment")
	}
	if namespace := catalog["higress"].(map[string]any)["namespace"]; namespace != "higress-system" {
		t.Fatalf("Higress default namespace = %v, want higress-system", namespace)
	}

	catalog["jenkins"].(map[string]any)["enabled"] = false
	catalog["higress"].(map[string]any)["enabled"] = false
	doc = ApplyDefaults(doc, "ops", "prod")
	if _, exists := doc["namespaces"].(map[string]any)["platform-server"]; !exists {
		t.Fatal("disabling components removed a previously managed Namespace")
	}
}

func TestApplyDefaultsRepairsLegacyEFKHeapAboveMemoryLimit(t *testing.T) {
	doc := DefaultDocument("ops", "prod")
	delete(doc, "efk_stack_defaults_version")
	stack := doc["components"].(map[string]any)["catalog"].(map[string]any)["efk_stack"].(map[string]any)
	stack["enabled"] = true
	elasticsearch := stack["values"].(map[string]any)["elasticsearch"].(map[string]any)
	elasticsearch["javaOpts"] = "-Xms8g -Xmx8g"

	doc = ApplyDefaults(doc, "ops", "prod")
	if got := elasticsearch["javaOpts"]; got != "-Xms2g -Xmx2g" {
		t.Fatalf("legacy EFK Heap was not clamped safely: %v", got)
	}
	if err := Validate(doc); err != nil {
		t.Fatalf("repaired legacy EFK configuration was rejected: %v", err)
	}
}

func TestValidateEFKHeapAgainstContainerMemory(t *testing.T) {
	doc := DefaultDocument("ops", "prod")
	stack := doc["components"].(map[string]any)["catalog"].(map[string]any)["efk_stack"].(map[string]any)
	stack["enabled"] = true
	doc = ApplyDefaults(doc, "ops", "prod")
	elasticsearch := stack["values"].(map[string]any)["elasticsearch"].(map[string]any)
	elasticsearch["javaOpts"] = "-Xms8g -Xmx8g"
	if err := Validate(doc); err == nil || !strings.Contains(err.Error(), "50%") {
		t.Fatalf("unsafe EFK Heap was accepted: %v", err)
	}

	elasticsearch["javaOpts"] = "-Xms1g -Xmx2g"
	if err := Validate(doc); err == nil || !strings.Contains(err.Error(), "必须相同") {
		t.Fatalf("mismatched EFK Heap settings were accepted: %v", err)
	}
}

func TestApplyDefaultsAddsClickVisualServiceFiltersWithoutChangingNamespaceScope(t *testing.T) {
	doc := DefaultDocument("ops", "test")
	stack := doc["components"].(map[string]any)["catalog"].(map[string]any)["clickvisual_stack"].(map[string]any)
	collection := stack["values"].(map[string]any)["collection"].(map[string]any)
	collection["includeNamespaces"] = []any{"ops-test"}
	collection["excludeNamespaces"] = []any{"kube-system"}
	delete(collection, "includeServices")
	delete(collection, "excludeServices")

	doc = ApplyDefaults(doc, "ops", "test")
	collection = stack["values"].(map[string]any)["collection"].(map[string]any)
	if values, ok := collection["includeNamespaces"].([]any); !ok || len(values) != 1 || values[0] != "ops-test" {
		t.Fatalf("existing namespace include scope changed: %#v", collection["includeNamespaces"])
	}
	if values, ok := collection["excludeNamespaces"].([]any); !ok || len(values) != 1 || values[0] != "kube-system" {
		t.Fatalf("existing namespace exclude scope changed: %#v", collection["excludeNamespaces"])
	}
	for _, field := range []string{"includeServices", "excludeServices"} {
		values, ok := collection[field].([]any)
		if !ok || len(values) != 0 {
			t.Fatalf("new service filter %s was not safely defaulted: %#v", field, collection[field])
		}
	}
}

func TestClickVisualStackUsesOneNamespace(t *testing.T) {
	doc := DefaultDocument("ops", "test")
	stack := doc["components"].(map[string]any)["catalog"].(map[string]any)["clickvisual_stack"].(map[string]any)
	stack["enabled"] = true

	doc = ApplyDefaults(doc, "ops", "test")
	values := stack["values"].(map[string]any)
	namespace := stringValue(values["namespace"])
	if namespace != "ops-test-logs-system" {
		t.Fatalf("unexpected logging namespace: %q", namespace)
	}
	if stringValue(stack["namespace"]) != namespace {
		t.Fatalf("component and chart namespaces differ: %#v", stack)
	}
	if _, exists := values["namespaces"]; exists {
		t.Fatalf("legacy per-component namespaces survived migration: %#v", values["namespaces"])
	}
	namespaces := doc["namespaces"].(map[string]any)
	if _, exists := namespaces[namespace]; !exists {
		t.Fatalf("logging namespace was not registered for Terraform creation: %#v", namespaces)
	}
	if err := Validate(doc); err != nil {
		t.Fatalf("single-namespace ClickVisual stack was rejected: %v", err)
	}
}

func TestApplyDefaultsMigratesBrokenClickVisualImageOnly(t *testing.T) {
	doc := DefaultDocument("ops", "test")
	stack := doc["components"].(map[string]any)["catalog"].(map[string]any)["clickvisual_stack"].(map[string]any)
	stack["chart_version"] = "0.3.0"
	images := stack["values"].(map[string]any)["images"].(map[string]any)
	images["clickvisual"] = "clickvisual/clickvisual:v1.0.6"

	doc = ApplyDefaults(doc, "ops", "test")
	if got := stringValue(images["clickvisual"]); got != clickVisualImage {
		t.Fatalf("broken ClickVisual image was not migrated: %q", got)
	}
	if got := stringValue(stack["chart_version"]); got != clickVisualStackChartVersion {
		t.Fatalf("ClickVisual chart version was not upgraded: %q", got)
	}

	images["clickvisual"] = "registry.example.com/clickvisual:custom"
	doc = ApplyDefaults(doc, "ops", "test")
	if got := stringValue(images["clickvisual"]); got != "registry.example.com/clickvisual:custom" {
		t.Fatalf("custom ClickVisual image was overwritten: %q", got)
	}
}

func TestApplyDefaultsMigratesLegacyClickVisualNamespacesSafely(t *testing.T) {
	doc := DefaultDocument("ops", "test")
	stack := doc["components"].(map[string]any)["catalog"].(map[string]any)["clickvisual_stack"].(map[string]any)
	stack["enabled"] = true
	stack["namespace"] = "ops-test-log-clickvisual"
	values := stack["values"].(map[string]any)
	delete(values, "namespace")
	values["namespaces"] = map[string]any{
		"fluentBit":   "ops-test-log-fluent-bit",
		"kafka":       "ops-test-log-kafka",
		"clickhouse":  "ops-test-log-clickhouse",
		"clickvisual": "ops-test-log-clickvisual",
		"mysql":       "ops-test-log-mysql",
	}
	namespaces := doc["namespaces"].(map[string]any)
	for _, namespace := range values["namespaces"].(map[string]any) {
		namespaces[stringValue(namespace)] = map[string]any{}
	}

	doc = ApplyDefaults(doc, "ops", "test")
	values = stack["values"].(map[string]any)
	if stringValue(values["namespace"]) != "ops-test-logs-system" {
		t.Fatalf("legacy stack did not migrate to the unified namespace: %#v", values)
	}
	if _, exists := values["namespaces"]; exists {
		t.Fatal("legacy per-component namespace map was not removed")
	}
	if _, exists := namespaces["ops-test-log-kafka"]; !exists {
		t.Fatal("legacy top-level namespace was removed and could destroy running resources")
	}
}

func TestValidateStatefulServiceStorage(t *testing.T) {
	doc := DefaultDocument("ops", "test")
	consul := doc["components"].(map[string]any)["consul"].(map[string]any)
	consul["enabled"] = true
	platform := doc["eks"].(map[string]any)["node_groups"].(map[string]any)["platform-ops"].(map[string]any)
	platform["min_size"] = 3
	platform["desired_size"] = 3
	if err := Validate(doc); err != nil {
		t.Fatalf("valid Consul persistence was rejected: %v", err)
	}
	consul["storage_size"] = "20"
	if err := Validate(doc); err == nil || !strings.Contains(err.Error(), "20Gi") {
		t.Fatalf("invalid Consul disk size was accepted: %v", err)
	}
	consul["storage_size"] = "20Gi"
	consul["replicas"] = 2
	if err := Validate(doc); err == nil || !strings.Contains(err.Error(), "odd replica") {
		t.Fatalf("even Consul quorum was accepted: %v", err)
	}
}

func TestAmazonMQValidationRejectsUnsupportedConfiguration(t *testing.T) {
	doc := validDocument()
	amazonMQ := doc["data_services"].(map[string]any)["amazon_mq"].(map[string]any)
	amazonMQ["enabled"] = true
	amazonMQ["host_instance_type"] = "mq.t3.micro"
	if err := Validate(doc); err == nil || !strings.Contains(err.Error(), "mq.t3.micro") {
		t.Fatalf("deprecated Amazon MQ instance was accepted: %v", err)
	}
	amazonMQ["host_instance_type"] = "mq.m7g.medium"
	amazonMQ["deployment_mode"] = "BROKEN"
	if err := Validate(doc); err == nil || !strings.Contains(err.Error(), "deployment_mode") {
		t.Fatalf("invalid Amazon MQ deployment mode was accepted: %v", err)
	}
}

func TestValidateRejectsInvalidManagedServiceCapacity(t *testing.T) {
	t.Run("RDS autoscaling ceiling below allocated storage", func(t *testing.T) {
		doc := DefaultDocument("demo", "test")
		rds := doc["data_services"].(map[string]any)["rds"].(map[string]any)
		rds["enabled"] = true
		rds["allocated_storage"] = 100
		rds["max_allocated_storage"] = 80
		if err := Validate(doc); err == nil || !strings.Contains(err.Error(), "max_allocated_storage") {
			t.Fatalf("invalid RDS storage limits were accepted: %v", err)
		}
	})

	t.Run("Aurora ACU range", func(t *testing.T) {
		doc := DefaultDocument("demo", "test")
		aurora := doc["data_services"].(map[string]any)["aurora"].(map[string]any)
		aurora["enabled"] = true
		aurora["min_acu"] = 8.0
		aurora["max_acu"] = 2.0
		if err := Validate(doc); err == nil || !strings.Contains(err.Error(), "min_acu") {
			t.Fatalf("invalid Aurora ACU range was accepted: %v", err)
		}
	})

	t.Run("Aurora backtrack window", func(t *testing.T) {
		doc := DefaultDocument("demo", "test")
		aurora := doc["data_services"].(map[string]any)["aurora"].(map[string]any)
		aurora["enabled"] = true
		aurora["backtrack_enabled"] = true
		aurora["backtrack_window_hours"] = 73
		if err := Validate(doc); err == nil || !strings.Contains(err.Error(), "backtrack_window_hours") {
			t.Fatalf("invalid Aurora backtrack window was accepted: %v", err)
		}
	})

	t.Run("MSK brokers must match availability zones", func(t *testing.T) {
		doc := DefaultDocument("demo", "test")
		msk := doc["data_services"].(map[string]any)["msk"].(map[string]any)
		msk["enabled"] = true
		msk["mode"] = "provisioned"
		msk["broker_count"] = 4
		if err := Validate(doc); err == nil || !strings.Contains(err.Error(), "broker_count") {
			t.Fatalf("MSK broker count incompatible with three AZs was accepted: %v", err)
		}
	})
}

func TestRepositorySaveLoadCloneDelete(t *testing.T) {
	repository, err := NewRepository(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := repository.Save("test", validDocument()); err != nil {
		t.Fatal(err)
	}
	loaded, err := repository.Load("test")
	if err != nil {
		t.Fatal(err)
	}
	if loaded["environment"] != "test" {
		t.Fatalf("environment name was not normalized: %#v", loaded)
	}
	if _, err := repository.Clone("staging", "test"); err != nil {
		t.Fatal(err)
	}
	if err := repository.Delete("staging"); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.Load("staging"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected deleted file to be missing, got %v", err)
	}
}

func TestHigressDefaultRequestFitsTwoVCPUNode(t *testing.T) {
	doc := DefaultDocument("demo", "uat")
	request, ok := GetPath(doc, "components.catalog.higress.values.higress-core.gateway.resources.requests.cpu")
	if !ok || request != "1" {
		t.Fatalf("unexpected Higress gateway CPU request: %#v", request)
	}

	legacy := DefaultDocument("demo", "uat")
	higress := legacy["components"].(map[string]any)["catalog"].(map[string]any)["higress"].(map[string]any)
	higress["values"] = map[string]any{}
	upgraded := ApplyDefaults(legacy, "demo", "uat")
	request, ok = GetPath(upgraded, "components.catalog.higress.values.higress-core.gateway.resources.requests.cpu")
	if !ok || request != "1" {
		t.Fatalf("legacy empty Higress values were not upgraded safely: %#v", request)
	}
}

func TestHigressNLBDefaultsAreSafeToManageFromFirstCreation(t *testing.T) {
	doc := DefaultDocument("demo", "prod")
	for path, want := range map[string]any{
		"components.catalog.higress.nlb.security_group_mode":                 "managed",
		"components.catalog.higress.nlb.manage_backend_security_group_rules": true,
		"components.catalog.higress.nlb.scheme":                              "internet-facing",
		"components.catalog.higress.nlb.external_traffic_policy":             "Local",
		"components.catalog.higress.nlb.idle_timeout_seconds":                600,
	} {
		got, ok := GetPath(doc, path)
		if !ok || got != want {
			t.Fatalf("%s = %#v, want %#v", path, got, want)
		}
	}
	ports, ok := GetPath(doc, "components.catalog.higress.nlb.allowed_ports")
	if !ok || !reflect.DeepEqual(ports, []any{80, 443}) {
		t.Fatalf("unexpected Higress NLB default ports: %#v", ports)
	}
	cidrs, ok := GetPath(doc, "components.catalog.higress.nlb.allowed_cidrs")
	if !ok || len(cidrs.([]any)) != 1 || cidrs.([]any)[0] != "0.0.0.0/0" {
		t.Fatalf("unexpected Higress NLB default CIDRs: %#v", cidrs)
	}

	legacy := DefaultDocument("demo", "prod")
	higress := legacy["components"].(map[string]any)["catalog"].(map[string]any)["higress"].(map[string]any)
	delete(higress, "nlb")
	upgraded := ApplyDefaults(legacy, "demo", "prod")
	if _, ok := GetPath(upgraded, "components.catalog.higress.nlb.allowed_cidrs"); !ok {
		t.Fatal("legacy Higress configuration was not upgraded with managed NLB defaults")
	}
	if mode, ok := GetPath(upgraded, "components.catalog.higress.nlb.security_group_mode"); !ok || mode != "managed" {
		t.Fatalf("legacy Higress configuration did not migrate to managed security groups: %#v", mode)
	}
}

func TestValidateHigressNLBSettings(t *testing.T) {
	valid := DefaultDocument("demo", "prod")
	SetPath(valid, "components.catalog.higress.enabled", true)
	SetPath(valid, "components.catalog.higress.nlb.allowed_cidrs", []any{"103.21.244.0/22", "104.16.0.0/13"})
	if err := Validate(valid); err != nil {
		t.Fatalf("valid Higress NLB settings were rejected: %v", err)
	}

	tests := []struct {
		name  string
		path  string
		value any
		want  string
	}{
		{name: "empty CIDRs", path: "components.catalog.higress.nlb.allowed_cidrs", value: []any{}, want: "at least one source CIDR"},
		{name: "invalid CIDR", path: "components.catalog.higress.nlb.allowed_cidrs", value: []any{"not-a-cidr"}, want: "is invalid"},
		{name: "duplicate CIDR", path: "components.catalog.higress.nlb.allowed_cidrs", value: []any{"103.21.244.0/22", "103.21.244.0/22"}, want: "contains duplicate"},
		{name: "non canonical CIDR", path: "components.catalog.higress.nlb.allowed_cidrs", value: []any{"103.21.244.1/22"}, want: "is invalid"},
		{name: "IPv6 CIDR on IPv4 NLB", path: "components.catalog.higress.nlb.allowed_cidrs", value: []any{"2001:db8::/64"}, want: "is invalid"},
		{name: "invalid security group mode", path: "components.catalog.higress.nlb.security_group_mode", value: "automatic", want: "must be managed, custom, or managed_plus_custom"},
		{name: "invalid scheme", path: "components.catalog.higress.nlb.scheme", value: "public", want: "must be internet-facing or internal"},
		{name: "empty allowed ports", path: "components.catalog.higress.nlb.allowed_ports", value: []any{}, want: "must contain port 80"},
		{name: "invalid allowed port", path: "components.catalog.higress.nlb.allowed_ports", value: []any{8443}, want: "must be 80 or 443"},
		{name: "duplicate allowed port", path: "components.catalog.higress.nlb.allowed_ports", value: []any{443, 443}, want: "duplicate port 443"},
		{name: "invalid security group ID", path: "components.catalog.higress.nlb.security_group_ids", value: []any{"sg-not-valid"}, want: "is invalid"},
		{name: "too many security groups", path: "components.catalog.higress.nlb.security_group_ids", value: []any{"sg-00000001", "sg-00000002", "sg-00000003", "sg-00000004", "sg-00000005"}, want: "cannot contain more than 4"},
		{name: "invalid backend rule management", path: "components.catalog.higress.nlb.manage_backend_security_group_rules", value: "yes", want: "must be a boolean"},
		{name: "invalid source policy", path: "components.catalog.higress.nlb.external_traffic_policy", value: "Other", want: "must be Local or Cluster"},
		{name: "idle timeout too small", path: "components.catalog.higress.nlb.idle_timeout_seconds", value: 30, want: "between 60 and 6000"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			doc := DefaultDocument("demo", "prod")
			SetPath(doc, "components.catalog.higress.enabled", true)
			SetPath(doc, tt.path, tt.value)
			if err := Validate(doc); err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("Validate() error = %v, want substring %q", err, tt.want)
			}
		})
	}

	custom := DefaultDocument("demo", "prod")
	SetPath(custom, "network.mode", "existing")
	SetPath(custom, "network.existing_vpc_id", "vpc-0123456789abcdef0")
	SetPath(custom, "network.existing_vpc_cidr", "10.40.0.0/16")
	SetPath(custom, "network.existing_workload_subnet_ids", []any{"subnet-0123456789abcdef0", "subnet-1123456789abcdef0"})
	SetPath(custom, "network.existing_data_subnet_ids", []any{"subnet-2123456789abcdef0", "subnet-3123456789abcdef0"})
	SetPath(custom, "components.catalog.higress.enabled", true)
	SetPath(custom, "components.catalog.higress.nlb.security_group_mode", "custom")
	SetPath(custom, "components.catalog.higress.nlb.security_group_ids", []any{"sg-0123456789abcdef0"})
	SetPath(custom, "components.catalog.higress.nlb.allowed_cidrs", []any{})
	if err := Validate(custom); err != nil {
		t.Fatalf("custom-only security group configuration was rejected: %v", err)
	}
	SetPath(custom, "components.catalog.higress.nlb.security_group_ids", []any{})
	if err := Validate(custom); err == nil || !strings.Contains(err.Error(), "at least one security group") {
		t.Fatalf("empty custom security group list error = %v", err)
	}

	newVPC := DefaultDocument("demo", "prod")
	SetPath(newVPC, "components.catalog.higress.enabled", true)
	SetPath(newVPC, "components.catalog.higress.nlb.security_group_mode", "custom")
	SetPath(newVPC, "components.catalog.higress.nlb.security_group_ids", []any{"sg-0123456789abcdef0"})
	SetPath(newVPC, "components.catalog.higress.nlb.allowed_cidrs", []any{})
	if err := Validate(newVPC); err == nil || !strings.Contains(err.Error(), "network.mode=existing") {
		t.Fatalf("new VPC custom security group error = %v", err)
	}

	withoutController := DefaultDocument("demo", "prod")
	SetPath(withoutController, "components.catalog.higress.enabled", true)
	SetPath(withoutController, "components.aws_load_balancer_controller.enabled", false)
	if err := Validate(withoutController); err == nil || !strings.Contains(err.Error(), "aws_load_balancer_controller.enabled=true") {
		t.Fatalf("missing load balancer controller error = %v", err)
	}

	existing := DefaultDocument("demo", "prod")
	SetPath(existing, "deployment_target.type", "existing_eks")
	SetPath(existing, "deployment_target.cluster_name", "shared-prod")
	SetPath(existing, "components.catalog.higress.enabled", true)
	SetPath(existing, "components.catalog.higress.nlb.security_group_mode", "custom")
	SetPath(existing, "components.catalog.higress.nlb.security_group_ids", []any{"sg-0123456789abcdef0"})
	SetPath(existing, "components.catalog.higress.nlb.allowed_cidrs", []any{})
	if err := Validate(existing); err == nil || !strings.Contains(err.Error(), "platform-managed EKS") {
		t.Fatalf("existing EKS custom security group error = %v", err)
	}
}

func TestRepositoryRejectsTraversal(t *testing.T) {
	repository, err := NewRepository(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := repository.Save("../escape", validDocument()); !errors.Is(err, ErrInvalidName) {
		t.Fatalf("expected ErrInvalidName, got %v", err)
	}
}

func TestRepositoryBootstrapsStoreAndExportsRuntimeYAML(t *testing.T) {
	dir := t.TempDir()
	files, err := NewRepository(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := files.Save("test", validDocument()); err != nil {
		t.Fatal(err)
	}
	store := &memoryEnvironmentStore{records: make(map[string][]byte)}
	if _, err := NewRepositoryWithStore(dir, store); err != nil {
		t.Fatal(err)
	}
	if !json.Valid(store.records["test"]) {
		t.Fatalf("file bootstrap did not persist valid JSON: %q", store.records["test"])
	}

	exportDir := t.TempDir()
	repository, err := NewRepositoryWithStore(exportDir, store)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(repository.Path("test")); err != nil {
		t.Fatalf("MySQL-backed configuration was not exported to YAML: %v", err)
	}
	if err := repository.Save("staging", validDocument()); err != nil {
		t.Fatal(err)
	}
	if _, ok := store.records["staging"]; !ok {
		t.Fatal("save did not reach the backing store")
	}
	if err := repository.Delete("staging"); err != nil {
		t.Fatal(err)
	}
	if _, ok := store.records["staging"]; ok {
		t.Fatal("delete did not reach the backing store")
	}
}

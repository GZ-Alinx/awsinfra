package runner

import (
	"strings"
	"testing"

	"ops-deploy-platform/internal/environment"
)

func TestKubernetesStorageBytes(t *testing.T) {
	cases := map[string]int64{
		"1Gi":  1024 * 1024 * 1024,
		"2Ti":  2 * 1024 * 1024 * 1024 * 1024,
		"64Mi": 64 * 1024 * 1024,
	}
	for value, expected := range cases {
		actual, ok := kubernetesStorageBytes(value)
		if !ok || actual != expected {
			t.Fatalf("kubernetesStorageBytes(%q) = %d, %v; want %d, true", value, actual, ok, expected)
		}
	}
	for _, value := range []string{"", "0Gi", "20GB", "-1Gi"} {
		if _, ok := kubernetesStorageBytes(value); ok {
			t.Fatalf("expected %q to be rejected", value)
		}
	}
}

func TestStorageReplacementPVCNameIsDNSLabel(t *testing.T) {
	name := storageReplacementPVCName("clickvisual-clickhouse-data-with-a-very-long-environment-prefix-that-exceeds-dns", "abcdef12-0")
	if len(name) > 63 {
		t.Fatalf("PVC name exceeds DNS label limit: %d %q", len(name), name)
	}
	if !strings.HasSuffix(name, "-resize-abcdef12-0") {
		t.Fatalf("PVC name lost its unique resize suffix: %q", name)
	}
}

func TestSetClickVisualActiveClaim(t *testing.T) {
	doc := environment.Document{}
	environment.SetPath(doc, "components.catalog.clickvisual_stack.values.kafka.replicas", 3)
	environment.SetPath(doc, "components.catalog.clickvisual_stack.values.kafka.storage.activeClaims", []any{})
	setClickVisualActiveClaim(doc, "kafka", "clickvisual-kafka-1", "replacement-1")
	raw, ok := environment.GetPath(doc, "components.catalog.clickvisual_stack.values.kafka.storage.activeClaims")
	if !ok {
		t.Fatal("active claims not saved")
	}
	claims, ok := raw.([]any)
	if !ok || len(claims) != 3 {
		t.Fatalf("unexpected claims: %#v", raw)
	}
	if claims[0] != "clickvisual-kafka-data-0" || claims[1] != "replacement-1" || claims[2] != "clickvisual-kafka-data-2" {
		t.Fatalf("unexpected claims: %#v", claims)
	}

	setClickVisualActiveClaim(doc, "clickhouse", "clickvisual-clickhouse", "replacement-clickhouse")
	value, _ := environment.GetPath(doc, "components.catalog.clickvisual_stack.values.clickhouse.storage.activeClaim")
	if value != "replacement-clickhouse" {
		t.Fatalf("unexpected clickhouse claim: %#v", value)
	}
}

func TestOpenTelemetryStorageUsesExpandedSizeWithoutMutatingClaimTemplate(t *testing.T) {
	if got := managedStorageSizePath("opentelemetry_collector"); got != "components.catalog.opentelemetry_collector.values.storage.expandedSize" {
		t.Fatalf("unexpected Collector expansion path: %s", got)
	}
	if got := managedStorageSizePath("otel-elasticsearch"); got != "components.catalog.opentelemetry_collector.values.elasticsearch.storage.expandedSize" {
		t.Fatalf("unexpected dedicated Elasticsearch expansion path: %s", got)
	}
	pvcs := []byte(`{"items":[{"metadata":{"name":"collector-storage-opentelemetry-collector-0","namespace":"monitoring","labels":{"ops-deploy.io/component":"opentelemetry_collector","ops-deploy.io/project":"demo","ops-deploy.io/environment":"test","ops-deploy.io/workload-kind":"statefulset","ops-deploy.io/workload-name":"opentelemetry-collector","ops-deploy.io/volume-name":"collector-storage"}},"spec":{"storageClassName":"gp3","accessModes":["ReadWriteOnce"],"resources":{"requests":{"storage":"10Gi"}}},"status":{"capacity":{"storage":"10Gi"}}}]}`)
	classes := []byte(`{"items":[{"metadata":{"name":"gp3"},"allowVolumeExpansion":true}]}`)
	workloads := []byte(`{"items":[{"metadata":{"name":"opentelemetry-collector","namespace":"monitoring"},"spec":{"replicas":1,"selector":{"matchLabels":{"app":"otel"}},"volumeClaimTemplates":[{"metadata":{"name":"collector-storage"}}],"template":{"spec":{"volumes":[]}}},"status":{"readyReplicas":1}}]}`)

	items, managedWorkloads, err := decodeRunnerManagedStorage(pvcs, classes, workloads)
	if err != nil {
		t.Fatalf("decodeRunnerManagedStorage: %v", err)
	}
	if len(items) != 1 || !items[0].Active || !items[0].AllowExpansion {
		t.Fatalf("Collector claim was not recognized as active and expandable: %#v", items)
	}
	if workload := managedWorkloads["monitoring\x00opentelemetry-collector"]; workload.Ready != 1 || workload.Replicas != 1 {
		t.Fatalf("Collector StatefulSet status was not decoded: %#v", workload)
	}
}

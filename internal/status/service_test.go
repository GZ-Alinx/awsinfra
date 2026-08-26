package status

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/GZ-Alinx/awsinfra/internal/appconfig"
	"github.com/GZ-Alinx/awsinfra/internal/environment"
)

func TestDecodeKubernetesServicesSortsAndPreservesPorts(t *testing.T) {
	payload := []byte(`{"items":[
	  {"metadata":{"name":"api","namespace":"team-b"},"spec":{"type":"ClusterIP","ports":[{"name":"http","port":8080,"appProtocol":"http"},{"name":"metrics","port":9090}]}},
      {"metadata":{"name":"web","namespace":"team-a"},"spec":{"type":"LoadBalancer","ports":[{"name":"https","port":443}]},"status":{"loadBalancer":{"ingress":[{"hostname":"web.example.elb.amazonaws.com"}]}}},
      {"metadata":{"name":"","namespace":"ignored"},"spec":{"ports":[{"port":80}]}}
    ]}`)
	services, err := decodeKubernetesServices(payload)
	if err != nil {
		t.Fatal(err)
	}
	if len(services) != 2 || services[0].Namespace != "team-a" || services[1].Name != "api" {
		t.Fatalf("services were not filtered and sorted: %#v", services)
	}
	if !reflect.DeepEqual(services[1].Ports, []ServicePort{{Name: "http", Port: 8080, AppProtocol: "http"}, {Name: "metrics", Port: 9090}}) {
		t.Fatalf("service ports = %#v", services[1].Ports)
	}
	if !reflect.DeepEqual(services[0].LoadBalancerHosts, []string{"web.example.elb.amazonaws.com"}) {
		t.Fatalf("load balancer hosts = %#v", services[0].LoadBalancerHosts)
	}
	if _, err := decodeKubernetesServices([]byte(`{"items":`)); err == nil {
		t.Fatal("invalid Kubernetes service JSON was accepted")
	}
}

func TestDecodeKubernetesEndpointHealth(t *testing.T) {
	payload := []byte(`{"items":[
	  {"metadata":{"namespace":"team-a","labels":{"kubernetes.io/service-name":"api"}},"endpoints":[
	    {"addresses":["10.0.0.1"],"conditions":{"ready":true}},
	    {"addresses":["10.0.0.2"],"conditions":{"ready":false}},
	    {"addresses":["10.0.0.3"],"conditions":{"ready":true,"terminating":true}}
	  ]},
	  {"metadata":{"namespace":"team-a","labels":{"kubernetes.io/service-name":"web"}},"endpoints":[
	    {"addresses":["10.0.0.4"],"conditions":{}}
	  ]}
	]}`)
	health, err := decodeKubernetesEndpointHealth(payload)
	if err != nil {
		t.Fatal(err)
	}
	if got := health["team-a/api"]; got.Ready != 1 || got.Total != 3 {
		t.Fatalf("api endpoint health = %#v", got)
	}
	if got := health["team-a/web"]; got.Ready != 1 || got.Total != 1 {
		t.Fatalf("web endpoint health = %#v", got)
	}
	services := []KubernetesService{{Name: "api", Namespace: "team-a"}, {Name: "missing", Namespace: "team-a"}}
	applyKubernetesEndpointHealth(services, health)
	if !services[0].EndpointHealthKnown || services[0].ReadyEndpoints != 1 || services[1].ReadyEndpoints != 0 {
		t.Fatalf("service endpoint health was not applied: %#v", services)
	}
	if _, err := decodeKubernetesEndpointHealth([]byte(`{"items":`)); err == nil {
		t.Fatal("invalid EndpointSlice JSON was accepted")
	}
}

func TestComponentStatusUsesCatalogReleaseName(t *testing.T) {
	config := &appconfig.Config{Components: []appconfig.ComponentConfig{{
		Key: "prometheus", DisplayName: "Prometheus", Category: "监控",
		ConfigPath: "components.catalog.prometheus.enabled", StatusType: "helm", StatusName: "prometheus",
	}}}
	doc := environment.Document{"components": map[string]any{"catalog": map[string]any{
		"prometheus": map[string]any{"enabled": true, "release_name": "prometheus-grafana"},
	}}}
	service := NewService(config, nil)
	components := service.componentStatuses(doc, nil, map[string]HelmRelease{
		"prometheus-grafana": {Name: "prometheus-grafana", Status: "deployed", Namespace: "monitoring", Chart: "kube-prometheus-stack"},
	}, nil, nil)
	if len(components) != 1 || !components[0].Actual || components[0].Status != "healthy" {
		t.Fatalf("configured Helm release was not recognized: %#v", components)
	}
}

func TestComponentStatusIncludesDynamicCatalogRelease(t *testing.T) {
	config := &appconfig.Config{}
	doc := environment.Document{"components": map[string]any{"catalog": map[string]any{
		"victoria-metrics": map[string]any{
			"enabled": true, "display_name": "VictoriaMetrics", "category": "监控",
			"release_name": "vm-stack",
		},
	}}}
	service := NewService(config, nil)
	components := service.componentStatuses(doc, nil, map[string]HelmRelease{
		"vm-stack": {Name: "vm-stack", Status: "deployed", Namespace: "monitoring", Chart: "victoria-metrics-k8s-stack-0.31.0"},
	}, nil, nil)
	if len(components) != 1 || components[0].Key != "victoria-metrics" || components[0].DisplayName != "VictoriaMetrics" || components[0].Status != "healthy" || !components[0].Actual {
		t.Fatalf("dynamic catalog release was not recognized: %#v", components)
	}
}

func TestFailedHelmReleaseWithReadyServiceIsDegradedNotMissing(t *testing.T) {
	config := &appconfig.Config{Components: []appconfig.ComponentConfig{{
		Key: "rabbitmq", DisplayName: "RabbitMQ", Category: "消息队列",
		ConfigPath: "components.catalog.rabbitmq.enabled", StatusType: "helm", StatusName: "rabbitmq",
	}}}
	doc := environment.Document{"components": map[string]any{"catalog": map[string]any{
		"rabbitmq": map[string]any{
			"enabled": true, "release_name": "rabbitmq", "namespace": "platform-server", "service_name": "rabbitmq",
		},
	}}}
	service := NewService(config, nil)
	components := service.componentStatuses(doc, nil, map[string]HelmRelease{
		"rabbitmq": {Name: "rabbitmq", Status: "failed", Namespace: "platform-server", Chart: "rabbitmq-0.1.0"},
	}, []KubernetesService{{
		Name: "rabbitmq", Namespace: "platform-server", EndpointHealthKnown: true, ReadyEndpoints: 1, TotalEndpoints: 1,
	}}, nil)
	if len(components) != 1 || !components[0].Actual || components[0].Status != "degraded" {
		t.Fatalf("running workload behind a failed Helm revision must be degraded: %#v", components)
	}
	if !strings.Contains(components[0].Detail, "Helm failed") || !strings.Contains(components[0].Detail, "Service 1/1") {
		t.Fatalf("degraded component detail is not actionable: %#v", components[0])
	}
}

func TestFailedHelmReleaseWithoutReadyServiceRemainsMissing(t *testing.T) {
	config := &appconfig.Config{Components: []appconfig.ComponentConfig{{
		Key: "rabbitmq", DisplayName: "RabbitMQ", Category: "消息队列",
		ConfigPath: "components.catalog.rabbitmq.enabled", StatusType: "helm", StatusName: "rabbitmq",
	}}}
	doc := environment.Document{"components": map[string]any{"catalog": map[string]any{
		"rabbitmq": map[string]any{
			"enabled": true, "release_name": "rabbitmq", "namespace": "platform-server", "service_name": "rabbitmq",
		},
	}}}
	service := NewService(config, nil)
	components := service.componentStatuses(doc, nil, map[string]HelmRelease{
		"rabbitmq": {Name: "rabbitmq", Status: "failed", Namespace: "platform-server", Chart: "rabbitmq-0.1.0"},
	}, []KubernetesService{{
		Name: "rabbitmq", Namespace: "platform-server", EndpointHealthKnown: true, ReadyEndpoints: 0, TotalEndpoints: 1,
	}}, nil)
	if len(components) != 1 || components[0].Actual || components[0].Status != "missing" {
		t.Fatalf("failed release without a ready endpoint must remain missing: %#v", components)
	}
}

type memoryStatusCache struct {
	payload []byte
	reads   int
	writes  int
}

type batchStatusCache struct {
	memoryStatusCache
	payloads   map[string][]byte
	batchReads int
}

func (c *batchStatusCache) GetStatuses(_ context.Context, names []string) (map[string][]byte, error) {
	c.batchReads++
	result := make(map[string][]byte)
	for _, name := range names {
		if payload, exists := c.payloads[name]; exists {
			result[name] = append([]byte(nil), payload...)
		}
	}
	return result, nil
}

type fixedCredentialProvider struct{}

func (fixedCredentialProvider) Environment(context.Context, string) ([]string, error) {
	return []string{"AWS_ACCESS_KEY_ID=TEST", "AWS_SECRET_ACCESS_KEY=TEST"}, nil
}

func (c *memoryStatusCache) GetStatus(context.Context, string) ([]byte, bool, error) {
	c.reads++
	return append([]byte(nil), c.payload...), len(c.payload) > 0, nil
}

func (c *memoryStatusCache) SetStatus(_ context.Context, _ string, payload []byte) error {
	c.writes++
	c.payload = append([]byte(nil), payload...)
	return nil
}

func (c *memoryStatusCache) DeleteStatus(context.Context, string) error {
	c.payload = nil
	return nil
}

func TestCachedManyUsesOneBatchReadAndSkipsInvalidEntries(t *testing.T) {
	cache := &batchStatusCache{payloads: map[string][]byte{
		"alpha-test": []byte(`{"cluster":{"name":"alpha","status":"ACTIVE"}}`),
		"broken":     []byte(`{"cluster":`),
	}}
	service := NewServiceWithCache(&appconfig.Config{}, nil, cache)
	reports := service.CachedMany(context.Background(), []string{"alpha-test", "missing", "broken"})
	if cache.batchReads != 1 {
		t.Fatalf("batch cache reads = %d, want 1", cache.batchReads)
	}
	if len(reports) != 1 || reports["alpha-test"] == nil || reports["alpha-test"].Cluster.Status != "ACTIVE" {
		t.Fatalf("unexpected cached reports: %#v", reports)
	}
}

func TestCollectUsesCacheAndFreshBypassesIt(t *testing.T) {
	dir := t.TempDir()
	repository, err := environment.NewRepository(filepath.Join(dir, "environments"))
	if err != nil {
		t.Fatal(err)
	}
	doc := environment.DefaultDocument("ops", "test")
	if err := repository.Save("test", doc); err != nil {
		t.Fatal(err)
	}
	falseTool := "/usr/bin/false"
	if _, err := os.Stat(falseTool); err != nil {
		t.Skip("test requires /usr/bin/false")
	}
	config := &appconfig.Config{
		Paths: appconfig.PathsConfig{
			DataDir: dir, TerraformInfraDir: dir,
		},
		Tools: appconfig.ToolsConfig{Terraform: falseTool, AWS: falseTool},
	}
	cache := &memoryStatusCache{}
	service := NewServiceWithCache(config, repository, cache)
	service.SetAWSCredentialProvider(fixedCredentialProvider{})
	first, err := service.Collect(context.Background(), "test")
	if err != nil {
		t.Fatal(err)
	}
	second, err := service.Collect(context.Background(), "test")
	if err != nil {
		t.Fatal(err)
	}
	if first.ObservedAt != second.ObservedAt || cache.reads != 2 || cache.writes != 1 {
		t.Fatalf("cache was not used: reads=%d writes=%d first=%s second=%s", cache.reads, cache.writes, first.ObservedAt, second.ObservedAt)
	}
	if _, err := service.CollectFresh(context.Background(), "test"); err != nil {
		t.Fatal(err)
	}
	if cache.writes != 2 {
		t.Fatalf("fresh collection did not update cache: writes=%d", cache.writes)
	}
}

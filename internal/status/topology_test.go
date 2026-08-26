package status

import (
	"encoding/json"
	"testing"
)

func TestDecodeApplicationTopologyBuildsIngressWorkloadRelation(t *testing.T) {
	payload := []byte(`{
	  "items": [
	    {
	      "apiVersion": "apps/v1", "kind": "Deployment",
	      "metadata": {"name": "gateway", "namespace": "demo-test", "labels": {"app": "gateway"}},
	      "spec": {"replicas": 2, "selector": {"matchLabels": {"app": "gateway"}}},
	      "status": {"replicas": 2, "readyReplicas": 1}
	    },
	    {
	      "apiVersion": "apps/v1", "kind": "ReplicaSet",
	      "metadata": {"name": "gateway-7d9", "namespace": "demo-test", "ownerReferences": [{"kind": "Deployment", "name": "gateway", "controller": true}]}
	    },
	    {
	      "apiVersion": "v1", "kind": "Pod",
	      "metadata": {"name": "gateway-7d9-abc", "namespace": "demo-test", "ownerReferences": [{"kind": "ReplicaSet", "name": "gateway-7d9", "controller": true}]},
	      "status": {"phase": "Running", "containerStatuses": [{"name": "gateway", "ready": true, "restartCount": 0}]}
	    },
	    {
	      "apiVersion": "v1", "kind": "Service",
	      "metadata": {"name": "gateway", "namespace": "demo-test"},
	      "spec": {"selector": {"app": "gateway"}, "ports": [{"name": "http", "port": 8080, "appProtocol": "http"}]}
	    },
	    {
	      "apiVersion": "networking.k8s.io/v1", "kind": "Ingress",
	      "metadata": {"name": "gateway", "namespace": "demo-test"},
	      "spec": {"rules": [{"host": "gateway.example.com", "http": {"paths": [{"path": "/api", "backend": {"service": {"name": "gateway", "port": {"name": "http"}}}}]}}]}
	    },
	    {
	      "apiVersion": "apps/v1", "kind": "Deployment",
	      "metadata": {"name": "coredns", "namespace": "kube-system"},
	      "spec": {"replicas": 2, "selector": {"matchLabels": {"k8s-app": "kube-dns"}}},
	      "status": {"readyReplicas": 2}
	    }
	  ]
	}`)
	topology, services, podOwners, err := decodeApplicationTopology(payload, map[string]struct{}{"demo-test": {}})
	if err != nil {
		t.Fatalf("decode topology: %v", err)
	}
	if len(topology.Nodes) != 4 || len(topology.Edges) != 3 {
		t.Fatalf("nodes=%d edges=%d, want 4 and 3", len(topology.Nodes), len(topology.Edges))
	}
	if len(services) != 1 {
		t.Fatalf("services=%d, want 1", len(services))
	}
	workloadID := "deployment:demo-test:gateway"
	if podOwners["demo-test/gateway-7d9-abc"] != workloadID {
		t.Fatalf("pod owner=%q", podOwners["demo-test/gateway-7d9-abc"])
	}
	var workload *ApplicationNode
	for index := range topology.Nodes {
		if topology.Nodes[index].ID == workloadID {
			workload = &topology.Nodes[index]
			break
		}
	}
	if workload == nil {
		t.Fatal("gateway workload not found")
	}
	if workload.State != "warning" || workload.ReadyReplicas != 1 || workload.DesiredReplicas != 2 {
		t.Fatalf("workload state=%s replicas=%d/%d", workload.State, workload.ReadyReplicas, workload.DesiredReplicas)
	}
	if len(workload.Services) != 1 || workload.Services[0] != "gateway" || len(workload.Hosts) != 1 {
		t.Fatalf("workload relation fields=%#v hosts=%#v", workload.Services, workload.Hosts)
	}
	serviceID := "service:demo-test:gateway"
	var gatewayDomain, ingressRoute, selectorRelation bool
	for _, edge := range topology.Edges {
		if edge.Source == "gateway:demo-test:ingress-gateway" && edge.Target == "domain:demo-test:gateway.example.com" &&
			edge.Relation == "exposes_domain" && edge.Verified {
			gatewayDomain = true
		}
		if edge.Source == "domain:demo-test:gateway.example.com" && edge.Target == serviceID &&
			edge.Relation == "ingress_route" && edge.Verified {
			ingressRoute = true
		}
		if edge.Source == serviceID && edge.Target == workloadID && edge.Relation == "service_selector" && !edge.Verified {
			selectorRelation = true
		}
	}
	if !gatewayDomain || !ingressRoute || !selectorRelation {
		t.Fatalf("expected real resource chain was not built: %#v", topology.Edges)
	}
}

func TestTopologyEndpointRelationsReplaceSelectorWithObservedEndpoint(t *testing.T) {
	topology := &ApplicationTopology{
		Nodes: []ApplicationNode{
			{ID: "service:demo-test:gateway", Name: "gateway", Namespace: "demo-test", Kind: "Service", State: "normal"},
			{ID: "deployment:demo-test:gateway", Name: "gateway", Namespace: "demo-test", Kind: "Deployment", State: "normal"},
		},
		Edges: []ApplicationEdge{{
			ID: "selector", Source: "service:demo-test:gateway", Target: "deployment:demo-test:gateway",
			Relation: "service_selector", Evidence: "Kubernetes Service selector", State: "normal",
		}},
	}
	payload := []byte(`{
	  "items": [{
	    "kind": "EndpointSlice",
	    "metadata": {"namespace": "demo-test", "labels": {"kubernetes.io/service-name": "gateway"}},
	    "endpoints": [
	      {"addresses": ["10.0.1.3"], "conditions": {"ready": true}, "targetRef": {"kind": "Pod", "namespace": "demo-test", "name": "gateway-a"}},
	      {"addresses": ["10.0.1.4"], "conditions": {"ready": false}, "targetRef": {"kind": "Pod", "namespace": "demo-test", "name": "gateway-b"}}
	    ]
	  }]
	}`)
	relations, err := decodeTopologyEndpointRelations(payload, map[string]string{
		"demo-test/gateway-a": "deployment:demo-test:gateway",
		"demo-test/gateway-b": "deployment:demo-test:gateway",
	})
	if err != nil {
		t.Fatal(err)
	}
	applyTopologyEndpointRelations(topology, []topologyService{{name: "gateway", namespace: "demo-test"}}, relations)
	if len(topology.Edges) != 1 || topology.Edges[0].Relation != "endpoint" || !topology.Edges[0].Verified {
		t.Fatalf("endpoint edge=%#v", topology.Edges)
	}
	if topology.Edges[0].ReadyEndpoints != 1 || topology.Edges[0].TotalEndpoints != 2 || topology.Edges[0].State != "warning" {
		t.Fatalf("endpoint health=%#v", topology.Edges[0])
	}
	if topology.Nodes[0].ReadyEndpoints != 1 || topology.Nodes[0].TotalEndpoints != 2 || topology.Nodes[0].State != "warning" {
		t.Fatalf("service health=%#v", topology.Nodes[0])
	}
}

func TestDecodeApplicationTopologyKeepsSelectorlessServiceVisible(t *testing.T) {
	payload := []byte(`{
	  "items": [
	    {
	      "apiVersion": "v1", "kind": "Service",
	      "metadata": {"name": "external-api", "namespace": "demo-test"},
	      "spec": {"ports": [{"name": "http", "port": 8080, "appProtocol": "http"}]}
	    },
	    {
	      "apiVersion": "networking.k8s.io/v1", "kind": "Ingress",
	      "metadata": {"name": "external-api", "namespace": "demo-test"},
	      "spec": {"rules": [{"host": "api.example.com", "http": {"paths": [{"path": "/", "backend": {"service": {"name": "external-api", "port": {"name": "http"}}}}]}}]}
	    }
	  ]
	}`)
	topology, _, _, err := decodeApplicationTopology(payload, map[string]struct{}{"demo-test": {}})
	if err != nil {
		t.Fatalf("decode topology: %v", err)
	}
	if len(topology.Nodes) != 3 || len(topology.Edges) != 2 {
		t.Fatalf("nodes=%d edges=%d, want 3 and 2", len(topology.Nodes), len(topology.Edges))
	}
	kinds := make(map[string]bool)
	for _, node := range topology.Nodes {
		kinds[node.Kind] = true
	}
	if !kinds["Gateway"] || !kinds["Domain"] || !kinds["Service"] {
		t.Fatalf("node kinds=%#v", kinds)
	}
	var hasGatewayDomain, hasDomainService bool
	for _, edge := range topology.Edges {
		hasGatewayDomain = hasGatewayDomain || edge.Relation == "exposes_domain"
		hasDomainService = hasDomainService || edge.Relation == "ingress_route"
	}
	if !hasGatewayDomain || !hasDomainService {
		t.Fatalf("edges=%#v", topology.Edges)
	}
}

func TestDecodeApplicationTopologyFindsDeclaredDataServiceDependencyWithoutReadingSecrets(t *testing.T) {
	payload := []byte(`{
	  "items": [
	    {
	      "apiVersion": "apps/v1", "kind": "Deployment",
	      "metadata": {"name": "api", "namespace": "demo-test"},
	      "spec": {
	        "replicas": 1,
	        "selector": {"matchLabels": {"app": "api"}},
	        "template": {"spec": {"containers": [{
	          "name": "api",
	          "env": [
	            {"name": "SPRING_DATA_REDIS_PASSWORD", "valueFrom": {"secretKeyRef": {"name": "redis-auth", "key": "password"}}}
	          ],
	          "envFrom": [{"configMapRef": {"name": "api-config"}}]
	        }]}}
	      },
	      "status": {"readyReplicas": 1}
	    },
	    {
	      "apiVersion": "v1", "kind": "ConfigMap",
	      "metadata": {"name": "api-config", "namespace": "demo-test"},
	      "data": {"application.yaml": "datasource: jdbc:mysql://mysql.demo-test.svc.cluster.local:3306/app"}
	    },
	    {"apiVersion": "v1", "kind": "Service", "metadata": {"name": "redis", "namespace": "demo-test"}, "spec": {"ports": [{"name": "redis", "port": 6379}]}},
	    {"apiVersion": "v1", "kind": "Service", "metadata": {"name": "mysql", "namespace": "demo-test"}, "spec": {"ports": [{"name": "mysql", "port": 3306}]}}
	  ]
	}`)
	topology, _, _, err := decodeApplicationTopology(payload, map[string]struct{}{"demo-test": {}})
	if err != nil {
		t.Fatal(err)
	}
	dependencies := make(map[string]bool)
	for _, edge := range topology.Edges {
		if edge.Relation == "declared_dependency" && edge.Source == "deployment:demo-test:api" {
			dependencies[edge.Target] = true
			if edge.Verified {
				t.Fatalf("declared dependency must not be reported as observed traffic: %#v", edge)
			}
		}
	}
	if !dependencies["service:demo-test:redis"] || !dependencies["service:demo-test:mysql"] {
		t.Fatalf("dependencies=%#v edges=%#v", dependencies, topology.Edges)
	}
}

func TestTopologyHealthUsesThreeExplicitStates(t *testing.T) {
	topology := &ApplicationTopology{Nodes: []ApplicationNode{
		{ID: "a", State: "normal"},
		{ID: "b", State: "warning"},
		{ID: "c", State: "abnormal"},
	}}
	summarizeTopology(topology)
	if topology.Summary != (TopologySummary{Normal: 1, Warning: 1, Abnormal: 1, Total: 3}) {
		t.Fatalf("summary=%#v", topology.Summary)
	}
	if worseState("warning", "normal") != "warning" || worseState("warning", "abnormal") != "abnormal" {
		t.Fatal("state ordering is incorrect")
	}
}

func TestDecodeApplicationAlertsFiltersManagedEKSControlPlaneNoise(t *testing.T) {
	var vector prometheusVector
	payload := []byte(`{
	  "status": "success",
	  "data": {"resultType": "vector", "result": [
	    {"metric": {"alertname": "KubeControllerManagerDown", "severity": "critical"}, "value": [1, "1"]},
	    {"metric": {"alertname": "PodCrashLooping", "severity": "critical", "namespace": "demo-test", "pod": "gateway-abc"}, "value": [1, "1"]},
	    {"metric": {"alertname": "OtherProject", "severity": "warning", "namespace": "other"}, "value": [1, "1"]}
	  ]}
	}`)
	if err := json.Unmarshal(payload, &vector); err != nil {
		t.Fatal(err)
	}
	alerts := decodeApplicationAlerts(vector, map[string]struct{}{"demo-test": {}})
	if len(alerts) != 1 || alerts[0].Name != "PodCrashLooping" || alerts[0].State != "abnormal" {
		t.Fatalf("alerts=%#v", alerts)
	}
}

func TestRuntimeTopologyConnectionsOnlyCreateObservedResolvableTraffic(t *testing.T) {
	var vector prometheusVector
	payload := []byte(`{
	  "status": "success",
	  "data": {"resultType": "vector", "result": [
	    {"metric": {
	      "source_workload": "gateway", "source_workload_namespace": "demo-test",
	      "destination_workload": "orders", "destination_workload_namespace": "demo-test",
	      "request_protocol": "http"
	    }, "value": [1, "3.25"]},
	    {"metric": {
	      "source_workload": "gateway", "source_workload_namespace": "other",
	      "destination_workload": "orders", "destination_workload_namespace": "other"
	    }, "value": [1, "99"]}
	  ]}
	}`)
	if err := json.Unmarshal(payload, &vector); err != nil {
		t.Fatal(err)
	}
	connections := decodeRuntimeTopologyConnections("istio_graph", vector, map[string]struct{}{"demo-test": {}})
	if len(connections) != 1 || connections[0].requestRate != 3.25 {
		t.Fatalf("connections=%#v", connections)
	}
	topology := &ApplicationTopology{Nodes: []ApplicationNode{
		{ID: "deployment:demo-test:gateway", Name: "gateway", Namespace: "demo-test", Kind: "Deployment", State: "normal"},
		{ID: "deployment:demo-test:orders", Name: "orders", Namespace: "demo-test", Kind: "Deployment", State: "normal"},
	}}
	if count := applyRuntimeTopologyConnections(topology, connections); count != 1 {
		t.Fatalf("runtime edge count=%d", count)
	}
	if len(topology.Edges) != 1 || topology.Edges[0].Relation != "runtime_request" ||
		!topology.Edges[0].Verified || topology.Edges[0].RequestRate != 3.25 {
		t.Fatalf("runtime edge=%#v", topology.Edges)
	}
}

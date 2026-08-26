package status

import (
	"strings"
	"testing"

	"github.com/GZ-Alinx/awsinfra/internal/environment"
)

func ingressTestDocument() environment.Document {
	return environment.Document{
		"project":     "kbp",
		"environment": "test",
		"namespaces": map[string]any{
			"kbp-game":        map[string]any{},
			"platform-server": map[string]any{},
		},
	}
}

func TestNormalizeIngressYAMLRestrictsScopeAndAddsOwnershipLabels(t *testing.T) {
	source := []byte(`
apiVersion: networking.k8s.io/v1
kind: Ingress
metadata:
  name: game-api
  namespace: kbp-game
  resourceVersion: "123"
  status: ignored
spec:
  ingressClassName: higress
  tls:
    - hosts: [api.example.com]
      secretName: example-tls
  rules:
    - host: api.example.com
      http:
        paths:
          - path: /api
            pathType: Prefix
            backend:
              service:
                name: game-admin
                port:
                  number: 8080
`)
	normalized, summary, warnings, err := normalizeIngressYAML(ingressTestDocument(), source)
	if err != nil {
		t.Fatalf("normalize ingress: %v", err)
	}
	text := string(normalized)
	for _, required := range []string{
		"ops-deploy.io/project: kbp",
		"ops-deploy.io/environment: test",
		"ops-deploy.io/managed-by: ingress-editor",
	} {
		if !strings.Contains(text, required) {
			t.Fatalf("normalized YAML missing %q:\n%s", required, text)
		}
	}
	if strings.Contains(text, "resourceVersion:") || strings.Contains(text, "\nstatus:") {
		t.Fatalf("server-owned fields leaked into normalized YAML:\n%s", text)
	}
	if summary.Namespace != "kbp-game" || summary.Name != "game-api" || summary.ClassName != "higress" {
		t.Fatalf("unexpected summary: %#v", summary)
	}
	if len(summary.Paths) != 1 || summary.Paths[0].ServiceName != "game-admin" || summary.Paths[0].ServicePort != "8080" {
		t.Fatalf("unexpected backend summary: %#v", summary.Paths)
	}
	if len(warnings) != 0 {
		t.Fatalf("unexpected warnings: %#v", warnings)
	}
}

func TestNormalizeIngressYAMLRejectsForeignNamespaceAndDangerousAnnotations(t *testing.T) {
	foreign := []byte(`
apiVersion: networking.k8s.io/v1
kind: Ingress
metadata:
  name: foreign
  namespace: kube-system
spec:
  ingressClassName: nginx
  rules:
    - http:
        paths:
          - path: /
            pathType: Prefix
            backend:
              service:
                name: dashboard
                port: {number: 80}
`)
	if _, _, _, err := normalizeIngressYAML(ingressTestDocument(), foreign); err == nil || !strings.Contains(err.Error(), "不属于当前项目环境") {
		t.Fatalf("foreign namespace was not rejected: %v", err)
	}

	dangerous := strings.Replace(string(foreign), "namespace: kube-system", "namespace: kbp-game\n  annotations:\n    nginx.ingress.kubernetes.io/server-snippet: return 200;", 1)
	if _, _, _, err := normalizeIngressYAML(ingressTestDocument(), []byte(dangerous)); err == nil || !strings.Contains(err.Error(), "高风险") {
		t.Fatalf("dangerous annotation was not rejected: %v", err)
	}
}

func TestNormalizeIngressYAMLRejectsMultipleDocumentsAndOtherKinds(t *testing.T) {
	multiple := []byte(`
apiVersion: networking.k8s.io/v1
kind: Ingress
metadata: {name: one, namespace: kbp-game}
spec: {ingressClassName: higress, defaultBackend: {service: {name: web, port: {number: 80}}}}
---
apiVersion: v1
kind: Secret
metadata: {name: hidden, namespace: kbp-game}
`)
	if _, _, _, err := normalizeIngressYAML(ingressTestDocument(), multiple); err == nil || !strings.Contains(err.Error(), "一次只能编辑一个") {
		t.Fatalf("multiple YAML documents were not rejected: %v", err)
	}
	secret := []byte(`apiVersion: v1
kind: Secret
metadata: {name: hidden, namespace: kbp-game}
`)
	if _, _, _, err := normalizeIngressYAML(ingressTestDocument(), secret); err == nil || !strings.Contains(err.Error(), "只允许") {
		t.Fatalf("non-Ingress kind was not rejected: %v", err)
	}

	aliases := []byte(`
apiVersion: networking.k8s.io/v1
kind: Ingress
metadata: &metadata
  name: aliased
  namespace: kbp-game
spec:
  ingressClassName: higress
  defaultBackend:
    service:
      name: web
      port: {number: 80}
`)
	if _, _, _, err := normalizeIngressYAML(ingressTestDocument(), aliases); err == nil || !strings.Contains(err.Error(), "锚点或别名") {
		t.Fatalf("YAML anchors were not rejected: %v", err)
	}
}

func TestEditableIngressDocumentPreservesResourceVersionForOptimisticLock(t *testing.T) {
	payload := []byte(`{
	  "apiVersion":"networking.k8s.io/v1",
	  "kind":"Ingress",
	  "metadata":{"name":"web","namespace":"kbp-game","resourceVersion":"456","uid":"server-owned"},
	  "spec":{"ingressClassName":"higress","rules":[{"http":{"paths":[{"path":"/","pathType":"Prefix","backend":{"service":{"name":"web","port":{"number":80}}}}]}}]},
	  "status":{"loadBalancer":{"ingress":[{"hostname":"lb.example.com"}]}}
	}`)
	document, err := editableIngressDocument(payload)
	if err != nil {
		t.Fatalf("editable ingress: %v", err)
	}
	if document.Ingress.ResourceVersion != "456" || !strings.Contains(document.YAML, `resourceVersion: "456"`) {
		t.Fatalf("resourceVersion was not preserved: %#v\n%s", document.Ingress, document.YAML)
	}
	if strings.Contains(document.YAML, "uid:") || strings.Contains(document.YAML, "status:") {
		t.Fatalf("server-owned fields leaked into editor YAML:\n%s", document.YAML)
	}
}

func TestReconcileConfiguredIngressesReportsSyncedPendingDriftAndClusterOnly(t *testing.T) {
	doc := ingressTestDocument()
	doc["tls"] = map[string]any{"certificates": []any{map[string]any{
		"key": "web-tls", "tls_secret_name": "web-tls-secret",
	}}}
	doc["domains"] = []any{
		map[string]any{
			"enabled": true, "protocol": "https", "domain": "web.example.com",
			"namespace": "kbp-game", "gateway": "higress", "tls_enabled": true,
			"certificate_ref": "web-tls", "backend_protocol": "http",
			"routes": []any{map[string]any{
				"path": "/", "path_type": "Prefix", "service": "web", "service_port": 80,
			}},
		},
		map[string]any{
			"enabled": true, "protocol": "http", "domain": "api.example.com",
			"namespace": "kbp-game", "gateway": "higress", "tls_enabled": false,
			"backend_protocol": "http",
			"routes": []any{map[string]any{
				"path": "/api", "path_type": "Prefix", "service": "api", "service_port": 8080,
			}},
		},
		map[string]any{
			"enabled": true, "protocol": "http", "domain": "drift.example.com",
			"namespace": "kbp-game", "gateway": "higress", "tls_enabled": false,
			"backend_protocol": "http",
			"routes": []any{map[string]any{
				"path": "/", "path_type": "Prefix", "service": "desired-api", "service_port": 8080,
			}},
		},
	}
	actual := []KubernetesIngress{
		{
			Name: "web-example-com", Namespace: "kbp-game", ClassName: "higress",
			Hosts: []string{"web.example.com"}, TLSSecrets: []string{"web-tls-secret"},
			Paths: []KubernetesIngressPath{{Host: "web.example.com", Path: "/", PathType: "Prefix", ServiceName: "web", ServicePort: "80"}},
		},
		{
			Name: "drift-example-com", Namespace: "kbp-game", ClassName: "higress",
			Hosts: []string{"drift.example.com"},
			Paths: []KubernetesIngressPath{{Host: "drift.example.com", Path: "/", PathType: "Prefix", ServiceName: "wrong-api", ServicePort: "8080"}},
		},
		{Name: "manual", Namespace: "kbp-game", ClassName: "higress"},
	}

	items := reconcileConfiguredIngresses(doc, actual)
	statuses := make(map[string]string, len(items))
	for _, item := range items {
		statuses[item.Name] = item.SyncStatus
	}
	for name, expected := range map[string]string{
		"web-example-com": "synced", "api-example-com": "pending",
		"drift-example-com": "drifted", "manual": "cluster-only",
	} {
		if statuses[name] != expected {
			t.Fatalf("Ingress %s status = %q, want %q; all=%#v", name, statuses[name], expected, statuses)
		}
	}
}

func TestConfiguredIngressIndexMatchesTerraformIdentity(t *testing.T) {
	doc := ingressTestDocument()
	doc["domains"] = []any{
		map[string]any{"enabled": false, "protocol": "http", "domain": "disabled.example.com", "namespace": "kbp-game"},
		map[string]any{"enabled": true, "protocol": "http", "domain": "api.example.com", "namespace": "kbp-game"},
		map[string]any{"enabled": true, "protocol": "http", "access_type": "ip", "name": "custom", "namespace": "platform-server"},
	}
	if index, ok := configuredIngressIndex(doc, "kbp-game", "api-example-com"); !ok || index != 1 {
		t.Fatalf("configured domain identity did not resolve: index=%d ok=%v", index, ok)
	}
	if index, ok := configuredIngressIndex(doc, "platform-server", "custom"); !ok || index != 2 {
		t.Fatalf("explicit Ingress name did not resolve: index=%d ok=%v", index, ok)
	}
	if _, ok := configuredIngressIndex(doc, "kbp-game", "disabled-example-com"); ok {
		t.Fatal("disabled domain was treated as a managed Ingress")
	}
}

func TestReconcileConfiguredIngressesReportsDuplicateDesiredIdentity(t *testing.T) {
	doc := ingressTestDocument()
	doc["domains"] = []any{
		map[string]any{
			"enabled": true, "protocol": "http", "domain": "api.example.com",
			"namespace": "kbp-game", "gateway": "higress",
			"service": "api", "service_port": 8080, "path": "/api",
		},
		map[string]any{
			"enabled": true, "protocol": "http", "domain": "api.example.com",
			"namespace": "kbp-game", "gateway": "higress",
			"service": "web", "service_port": 80, "path": "/",
		},
	}
	items := reconcileConfiguredIngresses(doc, nil)
	if len(items) != 1 || items[0].SyncStatus != "conflict" {
		t.Fatalf("duplicate desired Ingress identity was not surfaced as one conflict: %#v", items)
	}
}

func TestDecodeHigressResourceBackendUsesDestinationAnnotation(t *testing.T) {
	payload := []byte(`{
	  "metadata": {
	    "name": "game-admin-api",
	    "namespace": "platform-server",
	    "resourceVersion": "42",
	    "annotations": {
	      "higress.io/destination": "game-admin.kbp-game.svc.cluster.local:8080"
	    }
	  },
	  "spec": {
	    "ingressClassName": "higress",
	    "rules": [{
	      "host": "admin.example.com",
	      "http": {"paths": [{
	        "path": "/api",
	        "pathType": "Prefix",
	        "backend": {"resource": {"apiGroup": "networking.higress.io", "kind": "McpBridge", "name": "default"}}
	      }]}
	    }]
	  }
	}`)
	item, err := decodeKubernetesIngress(payload)
	if err != nil {
		t.Fatalf("decode Higress ingress: %v", err)
	}
	if len(item.Paths) != 1 {
		t.Fatalf("unexpected paths: %#v", item.Paths)
	}
	path := item.Paths[0]
	if path.ServiceName != "game-admin" || path.ServiceNamespace != "kbp-game" || path.ServicePort != "8080" {
		t.Fatalf("Higress destination was not decoded: %#v", path)
	}
}

func TestSyncIngressesToDomainConfigImportsMoreClusterRoutesByHost(t *testing.T) {
	doc := ingressTestDocument()
	doc["domains"] = []any{map[string]any{
		"enabled": true, "protocol": "https", "access_type": "domain",
		"domain": "admin.example.com", "gateway": "higress", "namespace": "kbp-game",
		"service": "frontend", "service_port": 8080, "path": "/", "path_type": "Prefix",
		"tls_enabled": true, "certificate_ref": "admin-tls", "tls_secret_name": "admin-tls",
		"backend_protocol": "http", "annotations": map[string]any{},
	}}
	inventory := []KubernetesIngress{
		{
			Name: "admin-ui", Namespace: "platform-server", ClassName: "higress", ResourceVersion: "1",
			Hosts: []string{"admin.example.com"},
			Paths: []KubernetesIngressPath{{
				Host: "admin.example.com", Path: "/", PathType: "Prefix",
				ServiceName: "frontend", ServiceNamespace: "kbp-game", ServicePort: "8080",
			}},
		},
		{
			Name: "admin-api", Namespace: "platform-server", ClassName: "higress", ResourceVersion: "2",
			Hosts: []string{"admin.example.com"},
			Paths: []KubernetesIngressPath{{
				Host: "admin.example.com", Path: "/api", PathType: "Prefix",
				ServiceName: "api", ServiceNamespace: "kbp-game", ServicePort: "8080",
			}},
		},
	}
	report := SyncIngressesToDomainConfig(doc, inventory)
	if report.UpdatedDomains != 1 || report.ImportedRoutes != 1 {
		t.Fatalf("unexpected sync report: %#v", report)
	}
	domains := doc["domains"].([]any)
	domain := domains[0].(map[string]any)
	routes := domain["routes"].([]any)
	if len(routes) != 2 {
		t.Fatalf("expected two imported routes: %#v", routes)
	}
	first := routes[0].(map[string]any)
	if first["path"] != "/api" || first["service"] != "api" {
		t.Fatalf("specific path should be kept before root: %#v", routes)
	}
	if domain["path"] != "/api" || domain["service"] != "api" {
		t.Fatalf("legacy first-route mirror was not updated: %#v", domain)
	}
}

func TestSyncIngressesToDomainConfigPreservesMoreCompleteDeploymentConfig(t *testing.T) {
	doc := ingressTestDocument()
	doc["domains"] = []any{map[string]any{
		"enabled": true, "protocol": "http", "access_type": "domain",
		"domain": "api.example.com", "gateway": "higress", "namespace": "kbp-game",
		"tls_enabled": false, "backend_protocol": "http", "annotations": map[string]any{},
		"routes": []any{
			map[string]any{"path": "/", "path_type": "Prefix", "service": "web", "service_port": 80},
			map[string]any{"path": "/api", "path_type": "Prefix", "service": "api", "service_port": 8080},
		},
	}}
	inventory := []KubernetesIngress{{
		Name: "api", Namespace: "kbp-game", ClassName: "higress", ResourceVersion: "1",
		Hosts: []string{"api.example.com"},
		Paths: []KubernetesIngressPath{{
			Host: "api.example.com", Path: "/", PathType: "Prefix",
			ServiceName: "web", ServiceNamespace: "kbp-game", ServicePort: "80",
		}},
	}}
	report := SyncIngressesToDomainConfig(doc, inventory)
	if report.UpdatedDomains != 0 || report.PreservedDomains != 1 {
		t.Fatalf("deployment config should win: %#v", report)
	}
	routes := doc["domains"].([]any)[0].(map[string]any)["routes"].([]any)
	if len(routes) != 2 {
		t.Fatalf("deployment routes changed unexpectedly: %#v", routes)
	}
}

func TestSyncIngressesToDomainConfigFromClusterReplacesEqualCountDrift(t *testing.T) {
	doc := ingressTestDocument()
	doc["domains"] = []any{map[string]any{
		"enabled": true, "protocol": "https", "access_type": "domain",
		"domain": "client.example.com", "gateway": "higress", "namespace": "kbp-game",
		"service": "game-admin", "service_port": 8080, "path": "/api", "path_type": "Prefix",
		"tls_enabled": true, "certificate_ref": "client-tls", "tls_secret_name": "client-tls",
		"backend_protocol": "http", "annotations": map[string]any{},
	}}
	inventory := []KubernetesIngress{{
		Name: "external-api", Namespace: "platform-server", ClassName: "higress", ResourceVersion: "1",
		Hosts: []string{"client.example.com"},
		Paths: []KubernetesIngressPath{{
			Host: "client.example.com", Path: "/openapi", PathType: "Prefix",
			ServiceName: "platform-external", ServiceNamespace: "platform-server", ServicePort: "8888",
		}},
	}}
	report := SyncIngressesToDomainConfigFromCluster(doc, inventory)
	if report.UpdatedDomains != 1 || len(report.Skipped) != 0 {
		t.Fatalf("cluster-authoritative sync did not update drift: %#v", report)
	}
	domain := doc["domains"].([]any)[0].(map[string]any)
	if domain["namespace"] != "platform-server" || domain["path"] != "/openapi" ||
		domain["service"] != "platform-external" || domain["service_port"] != 8888 {
		t.Fatalf("cluster route was not authoritative: %#v", domain)
	}
}

func TestSyncIngressesToDomainConfigFromClusterImportsNewProjectIngress(t *testing.T) {
	doc := ingressTestDocument()
	doc["domains"] = []any{}
	doc["tls"] = map[string]any{"certificates": []any{map[string]any{
		"key": "apps-tls", "tls_secret_name": "apps-tls",
	}}}
	inventory := []KubernetesIngress{{
		Name: "new-api", Namespace: "kbp-game", ClassName: "higress", ResourceVersion: "10",
		Hosts: []string{"new.example.com"}, TLSSecrets: []string{"apps-tls"},
		Paths: []KubernetesIngressPath{{
			Host: "new.example.com", Path: "/api", PathType: "Prefix",
			ServiceName: "new-api", ServiceNamespace: "kbp-game", ServicePort: "8080",
		}},
	}}
	report := SyncIngressesToDomainConfigFromCluster(doc, inventory)
	if report.ImportedDomains != 1 || report.ImportedRoutes != 1 || report.UpdatedDomains != 1 || len(report.Skipped) != 0 {
		t.Fatalf("unexpected import report: %#v", report)
	}
	domains := doc["domains"].([]any)
	if len(domains) != 1 {
		t.Fatalf("unexpected imported domains: %#v", domains)
	}
	domain := domains[0].(map[string]any)
	if domain["domain"] != "new.example.com" || domain["name"] != "new-api" ||
		domain["namespace"] != "kbp-game" || domain["protocol"] != "https" ||
		domain["certificate_ref"] != "apps-tls" {
		t.Fatalf("unexpected imported domain: %#v", domain)
	}
}

func TestSyncIngressesToDomainConfigFromClusterDoesNotImportForeignNamespace(t *testing.T) {
	doc := ingressTestDocument()
	doc["domains"] = []any{}
	inventory := []KubernetesIngress{{
		Name: "foreign", Namespace: "other-project", ClassName: "higress", ResourceVersion: "11",
		Hosts: []string{"foreign.example.com"},
		Paths: []KubernetesIngressPath{{
			Host: "foreign.example.com", Path: "/", PathType: "Prefix",
			ServiceName: "web", ServiceNamespace: "other-project", ServicePort: "80",
		}},
	}}
	report := SyncIngressesToDomainConfigFromCluster(doc, inventory)
	if report.ImportedDomains != 0 || len(doc["domains"].([]any)) != 0 {
		t.Fatalf("foreign ingress was imported: report=%#v domains=%#v", report, doc["domains"])
	}
}

func TestSyncIngressesToDomainConfigFromClusterRequiresRegisteredTLSSecret(t *testing.T) {
	doc := ingressTestDocument()
	doc["domains"] = []any{}
	inventory := []KubernetesIngress{{
		Name: "secure", Namespace: "kbp-game", ClassName: "higress", ResourceVersion: "12",
		Hosts: []string{"secure.example.com"}, TLSSecrets: []string{"unknown-tls"},
		Paths: []KubernetesIngressPath{{
			Host: "secure.example.com", Path: "/", PathType: "Prefix",
			ServiceName: "secure", ServiceNamespace: "kbp-game", ServicePort: "8080",
		}},
	}}
	report := SyncIngressesToDomainConfigFromCluster(doc, inventory)
	if report.ImportedDomains != 0 || len(report.Skipped) != 1 {
		t.Fatalf("unregistered TLS secret should be skipped: %#v", report)
	}
}

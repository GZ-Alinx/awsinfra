package runner

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func TestClusterAutoscalerAllowsDrainOfDisposableLocalStorage(t *testing.T) {
	payload, err := os.ReadFile(filepath.Join("..", "..", "terraform", "platform", "helm-addons.tf"))
	if err != nil {
		t.Fatal(err)
	}
	configuration := string(payload)
	for _, expected := range []string{
		"skip-nodes-with-local-storage",
		"skip-nodes-with-system-pods",
	} {
		if !strings.Contains(configuration, expected) {
			t.Fatalf("cluster autoscaler configuration is missing %q", expected)
		}
	}
	for _, key := range []string{"skip-nodes-with-local-storage", "skip-nodes-with-system-pods"} {
		if !regexp.MustCompile(regexp.QuoteMeta(key) + `\s*=\s*false`).MatchString(configuration) {
			t.Fatalf("cluster autoscaler flag %q must be explicitly false", key)
		}
	}
}

func TestClusterAutoscalerCanReadDynamicResourceAllocationObjects(t *testing.T) {
	payload, err := os.ReadFile(filepath.Join("..", "..", "terraform", "platform", "helm-addons.tf"))
	if err != nil {
		t.Fatal(err)
	}
	configuration := string(payload)
	for _, expected := range []string{
		"additionalRules",
		"resource.k8s.io",
		"deviceclasses",
		"resourceclaims",
		"resourceclaimtemplates",
		"resourceslices",
	} {
		if !strings.Contains(configuration, expected) {
			t.Fatalf("cluster autoscaler RBAC is missing %q", expected)
		}
	}
}

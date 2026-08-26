package environment

import (
	"strings"
	"testing"
)

func TestExistingEKSTargetUsesSelectedClusterAndIsolatedNamespaces(t *testing.T) {
	doc := DefaultDocument("demo", "test")
	if err := ConfigureTarget(doc, TargetExistingEKS); err != nil {
		t.Fatal(err)
	}
	target := doc["deployment_target"].(map[string]any)
	target["cluster_name"] = "shared-eks"
	if ClusterName(doc) != "shared-eks" || ManageClusterAddons(doc) {
		t.Fatalf("unexpected existing EKS target: %#v", target)
	}
	if err := Validate(doc); err != nil {
		t.Fatalf("valid existing EKS target was rejected: %v", err)
	}
	catalog := doc["components"].(map[string]any)["catalog"].(map[string]any)
	for key, raw := range catalog {
		namespace := raw.(map[string]any)["namespace"].(string)
		if !strings.HasPrefix(namespace, "demo-test-") {
			t.Fatalf("component %s was not isolated: %s", key, namespace)
		}
	}
}

func TestExistingEKSTargetRequiresClusterName(t *testing.T) {
	doc := DefaultDocument("demo", "test")
	if err := ConfigureTarget(doc, TargetExistingEKS); err != nil {
		t.Fatal(err)
	}
	if err := Validate(doc); err == nil || !strings.Contains(err.Error(), "cluster_name") {
		t.Fatalf("missing existing cluster name was accepted: %v", err)
	}
}

func TestManagedTargetKeepsDerivedClusterName(t *testing.T) {
	doc := DefaultDocument("demo", "uat")
	if ClusterName(doc) != "demo-uat-eks" || !ManageClusterAddons(doc) {
		t.Fatalf("unexpected managed target: %#v", doc["deployment_target"])
	}
}

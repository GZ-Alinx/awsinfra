package environment

import "testing"

func TestConfigureRegionRemapsZoneReferences(t *testing.T) {
	doc := DefaultDocument("demo", "test")
	if err := ConfigureRegion(doc, "us-east-1"); err != nil {
		t.Fatal(err)
	}
	if err := ConfigureTarget(doc, TargetExistingEKS); err != nil {
		t.Fatal(err)
	}
	doc["deployment_target"].(map[string]any)["cluster_name"] = "shared-eks"
	if err := Validate(doc); err != nil {
		t.Fatalf("non-default Region target was invalid: %v", err)
	}
	network := doc["network"].(map[string]any)
	for _, zone := range []string{"us-east-1a", "us-east-1b", "us-east-1c"} {
		if _, ok := network["public_subnets"].(map[string]any)[zone]; !ok {
			t.Fatalf("missing remapped public subnet for %s", zone)
		}
	}
}

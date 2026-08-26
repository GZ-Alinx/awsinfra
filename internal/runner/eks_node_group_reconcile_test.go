package runner

import (
	"reflect"
	"testing"

	"ops-deploy-platform/internal/environment"
)

func TestDesiredEKSNodeGroupNamesAreStable(t *testing.T) {
	document := environment.Document{"eks": map[string]any{"node_groups": map[string]any{
		"platform-ops": map[string]any{}, "business-workload": map[string]any{}, "ingress-gateway": map[string]any{},
	}}}
	want := []string{"business-workload", "ingress-gateway", "platform-ops"}
	if got := desiredEKSNodeGroupNames(document); !reflect.DeepEqual(got, want) {
		t.Fatalf("unexpected group order: got %#v want %#v", got, want)
	}
}

func TestTerraformEKSNodeGroupAddresses(t *testing.T) {
	if got := terraformEKSNodeGroupAddress("business-workload"); got != `aws_eks_node_group.this["business-workload"]` {
		t.Fatalf("unexpected node group address %q", got)
	}
	if got := terraformEKSLaunchTemplateAddress("business-workload"); got != `aws_launch_template.node["business-workload"]` {
		t.Fatalf("unexpected launch template address %q", got)
	}
}

func TestDesiredNodeGroupCapacityReadsYAMLValues(t *testing.T) {
	desired, instanceTypes := desiredNodeGroupCapacity(map[string]any{
		"desired_size":   8,
		"instance_types": []any{"m7i.2xlarge", "m7i.4xlarge"},
	})
	if desired != 8 {
		t.Fatalf("unexpected desired capacity %d", desired)
	}
	if want := []string{"m7i.2xlarge", "m7i.4xlarge"}; !reflect.DeepEqual(instanceTypes, want) {
		t.Fatalf("unexpected instance types: got %#v want %#v", instanceTypes, want)
	}
}

func TestDesiredNodeGroupCapacityDefaultsToZero(t *testing.T) {
	desired, instanceTypes := desiredNodeGroupCapacity(map[string]any{})
	if desired != 0 || len(instanceTypes) != 0 {
		t.Fatalf("unexpected defaults: desired=%d types=%#v", desired, instanceTypes)
	}
}

func TestDeferredGatewayCapacityBlocksPhaseTwo(t *testing.T) {
	document := environment.Document{
		"components": map[string]any{"catalog": map[string]any{"higress": map[string]any{"enabled": true}}},
		"eks": map[string]any{"node_groups": map[string]any{
			"ingress-gateway": map[string]any{
				"capacity_deferred": true,
				"labels":            map[string]any{"workload-class": "gateway"},
			},
		}},
	}
	if err := validateDeferredComponentCapacity(document); err == nil {
		t.Fatal("expected deferred gateway capacity to block phase two")
	}
	document["eks"].(map[string]any)["node_groups"].(map[string]any)["ingress-gateway"].(map[string]any)["capacity_deferred"] = false
	if err := validateDeferredComponentCapacity(document); err != nil {
		t.Fatalf("active gateway capacity must allow phase two: %v", err)
	}
}

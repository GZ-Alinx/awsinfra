package main

import (
	"testing"

	"github.com/GZ-Alinx/awsinfra/internal/environment"
)

func TestDeferredCapacityKeepsScaleOutCeilings(t *testing.T) {
	document := environment.Document{
		"region": "ap-south-1",
		"network": map[string]any{
			"availability_zones": []any{"ap-south-1a", "ap-south-1b", "ap-south-1c"},
		},
	}
	if err := applyIsolatedProduction(document); err != nil {
		t.Fatal(err)
	}
	deferIsolatedNodeGroupCapacity(document)
	groups := object(object(document, "eks"), "node_groups")
	for name, maximum := range map[string]int{"ingress-gateway": 12, "business-workload": 20, "platform-ops": 6} {
		group := groups[name].(map[string]any)
		if group["capacity_deferred"] != true {
			t.Fatalf("%s capacity was not marked deferred: %#v", name, group)
		}
		if group["max_size"] != maximum {
			t.Fatalf("%s max_size changed: got %v want %d", name, group["max_size"], maximum)
		}
	}
}

func TestProfileRestoresStagedCapacityAfterQuotaApproval(t *testing.T) {
	document := environment.Document{
		"region": "ap-south-1",
		"network": map[string]any{
			"availability_zones": []any{"ap-south-1a", "ap-south-1b", "ap-south-1c"},
		},
	}
	if err := applyIsolatedProduction(document); err != nil {
		t.Fatal(err)
	}
	deferIsolatedNodeGroupCapacity(document)
	if err := applyIsolatedProduction(document); err != nil {
		t.Fatal(err)
	}
	groups := object(object(document, "eks"), "node_groups")
	if groups["ingress-gateway"].(map[string]any)["desired_size"] != 2 {
		t.Fatal("profile did not restore gateway desired capacity")
	}
	if groups["business-workload"].(map[string]any)["desired_size"] != 0 {
		t.Fatal("profile did not restore business desired capacity")
	}
	if groups["platform-ops"].(map[string]any)["desired_size"] != 1 {
		t.Fatal("profile did not restore platform desired capacity")
	}
	for name, raw := range groups {
		if raw.(map[string]any)["capacity_deferred"] != false {
			t.Fatalf("profile did not activate %s after quota approval", name)
		}
	}
}

func TestIsolatedProfileTaintsOnlyBusinessNodes(t *testing.T) {
	document := environment.Document{
		"region": "ap-south-1",
		"network": map[string]any{
			"availability_zones": []any{"ap-south-1a", "ap-south-1b", "ap-south-1c"},
		},
	}
	if err := applyIsolatedProduction(document); err != nil {
		t.Fatal(err)
	}
	groups := object(object(document, "eks"), "node_groups")
	for _, name := range []string{"ingress-gateway", "platform-ops"} {
		if taints := groups[name].(map[string]any)["taints"].([]any); len(taints) != 0 {
			t.Fatalf("%s must not have a taint: %#v", name, taints)
		}
	}
	if taints := groups["business-workload"].(map[string]any)["taints"].([]any); len(taints) != 1 {
		t.Fatalf("business-workload must have one taint: %#v", taints)
	}
}

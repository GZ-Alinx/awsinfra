package httpapi

import (
	"errors"
	"testing"

	"github.com/GZ-Alinx/awsinfra/internal/environment"
)

func TestNodeGroupPlanningLock(t *testing.T) {
	current := environment.DefaultDocument("demo", "test")
	next := environment.DefaultDocument("demo", "test")
	group := next["eks"].(map[string]any)["node_groups"].(map[string]any)["business-workload"].(map[string]any)
	group["max_size"] = 12
	if err := validateNodeGroupPlanningChange(current, next, true); err != nil {
		t.Fatalf("capacity-only update was blocked: %v", err)
	}

	group["labels"].(map[string]any)["workload-class"] = "platform"
	if err := validateNodeGroupPlanningChange(current, next, true); !errors.Is(err, errNodeGroupPlanningLocked) {
		t.Fatalf("placement update was not blocked: %v", err)
	}
	if err := validateNodeGroupPlanningChange(current, next, false); err != nil {
		t.Fatalf("pre-deployment planning update was blocked: %v", err)
	}
}

func TestNodeGroupPlanningLockAllowsAppendOnlyGroups(t *testing.T) {
	current := environment.DefaultDocument("demo", "test")
	next := environment.DefaultDocument("demo", "test")
	groups := next["eks"].(map[string]any)["node_groups"].(map[string]any)
	groups["batch"] = map[string]any{
		"availability_zones": []any{"ap-south-1a", "ap-south-1b", "ap-south-1c"},
		"instance_types":     []any{"m7i.large"},
		"capacity_type":      "SPOT",
		"subnet_type":        "private",
		"min_size":           0,
		"desired_size":       0,
		"max_size":           3,
		"disk_size":          80,
		"labels":             map[string]any{"workload-class": "general"},
	}
	if err := validateNodeGroupPlanningChange(current, next, true); err != nil {
		t.Fatalf("append-only node group addition was blocked: %v", err)
	}

	delete(groups, "business-workload")
	if err := validateNodeGroupPlanningChange(current, next, true); !errors.Is(err, errNodeGroupPlanningLocked) {
		t.Fatalf("existing node group deletion was not blocked: %v", err)
	}
}

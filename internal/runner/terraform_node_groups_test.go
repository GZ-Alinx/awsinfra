package runner

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestInfraNodeGroupsKeepHeterogeneousObjectShape(t *testing.T) {
	payload, err := os.ReadFile(filepath.Join("..", "..", "terraform", "infra", "locals.tf"))
	if err != nil {
		t.Fatal(err)
	}
	configuration := string(payload)
	if strings.Contains(configuration, "node_groups = tomap(") {
		t.Fatal("node_groups must not use tomap: groups with different optional fields cannot be coerced to one type")
	}
	if !strings.Contains(configuration, "node_groups = try(local.config.eks.node_groups, {})") {
		t.Fatal("node_groups must preserve the native yamldecode object for append-only heterogeneous groups")
	}
}

func TestPlatformNodeGroupsAvoidIncompatibleConditionalObjectTypes(t *testing.T) {
	payload, err := os.ReadFile(filepath.Join("..", "..", "terraform", "platform", "locals.tf"))
	if err != nil {
		t.Fatal(err)
	}
	configuration := string(payload)
	if strings.Contains(configuration, "node_groups                 = local.managed_target ?") {
		t.Fatal("platform node_groups must not conditionally unify a heterogeneous object with an empty object")
	}
	if !strings.Contains(configuration, "node_groups                 = try(local.config.eks.node_groups, {})") {
		t.Fatal("platform node_groups must preserve the native yamldecode object")
	}
	if !strings.Contains(configuration, "node_group_names            = local.managed_target ? sort(keys(local.node_groups)) : []") {
		t.Fatal("existing EKS targets must be gated at the derived node-group name list")
	}
}

func TestComponentDisableKeepsConfiguredBackupBucket(t *testing.T) {
	payload, err := os.ReadFile(filepath.Join("..", "..", "terraform", "infra", "backups.tf"))
	if err != nil {
		t.Fatal(err)
	}
	configuration := string(payload)
	if strings.Contains(configuration, "local.consul_backup_enabled || local.etcd_backup_enabled") {
		t.Fatal("backup bucket lifecycle must not be tied to runtime component enablement")
	}
	for _, path := range []string{
		"local.config.components.consul.backup.enabled",
		"local.config.components.etcd.backup.enabled",
	} {
		if !strings.Contains(configuration, path) {
			t.Fatalf("backup bucket retention is missing configured policy %s", path)
		}
	}
}

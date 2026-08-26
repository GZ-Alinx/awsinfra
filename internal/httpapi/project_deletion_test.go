package httpapi

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/GZ-Alinx/awsinfra/internal/access"
	"github.com/GZ-Alinx/awsinfra/internal/appconfig"
	"github.com/GZ-Alinx/awsinfra/internal/environment"
	"github.com/GZ-Alinx/awsinfra/internal/jobs"
)

func TestManagedTerraformResourceCount(t *testing.T) {
	path := filepath.Join(t.TempDir(), "terraform.tfstate")
	payload := []byte(`{"resources":[{"mode":"data","instances":[{}]},{"mode":"managed","instances":[{},{}]},{"mode":"managed","instances":[]}]}`)
	if err := os.WriteFile(path, payload, 0o600); err != nil {
		t.Fatal(err)
	}
	count, found, err := managedTerraformResourceCount(path)
	if err != nil || !found || count != 2 {
		t.Fatalf("count=%d found=%v err=%v", count, found, err)
	}
	if count, found, err = managedTerraformResourceCount(filepath.Join(t.TempDir(), "missing")); err != nil || found || count != 0 {
		t.Fatalf("missing state: count=%d found=%v err=%v", count, found, err)
	}
}

func TestRemoteStateMetadataSupersedesLegacyState(t *testing.T) {
	root := t.TempDir()
	metadataPath := filepath.Join(root, "platform.json")
	legacyPath := filepath.Join(root, "terraform.tfstate")
	if err := os.WriteFile(legacyPath, []byte(`{"resources":[{"mode":"managed","instances":[{},{}]}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(metadataPath, []byte(`{"backend":"s3","managed_resources":5}`), 0o600); err != nil {
		t.Fatal(err)
	}
	count, found, err := managedTerraformResourceCountWithMetadata(metadataPath, legacyPath)
	if err != nil || !found || count != 5 {
		t.Fatalf("remote state metadata was not authoritative: count=%d found=%v err=%v", count, found, err)
	}
}

func TestEnvironmentStateBlockersRequireBothStagesToBeEmpty(t *testing.T) {
	root := t.TempDir()
	server := &Server{config: &appconfig.Config{Paths: appconfig.PathsConfig{
		DataDir: root, TerraformInfraDir: filepath.Join(root, "infra"), TerraformPlatformDir: filepath.Join(root, "platform"),
	}}}
	item := access.ProjectEnvironment{ProjectKey: "demo", Environment: "test", TargetName: "demo-test"}
	metadataDir := filepath.Join(root, "state-metadata", "demo", "demo-test")
	if err := os.MkdirAll(metadataDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(metadataDir, "infra.json"), []byte(`{"backend":"s3","managed_resources":0}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(metadataDir, "platform.json"), []byte(`{"backend":"s3","managed_resources":2}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if blockers := server.environmentStateBlockers("demo", item); len(blockers) != 1 {
		t.Fatalf("remaining platform resources did not preserve environment configuration: %#v", blockers)
	}
	if err := os.WriteFile(filepath.Join(metadataDir, "platform.json"), []byte(`{"backend":"s3","managed_resources":0}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if blockers := server.environmentStateBlockers("demo", item); len(blockers) != 0 {
		t.Fatalf("empty destroy state blocked environment deletion: %#v", blockers)
	}
}

func TestProjectDeletionBlockedByManagedTerraformState(t *testing.T) {
	root := t.TempDir()
	repository, err := environment.NewRepository(filepath.Join(root, "environments"))
	if err != nil {
		t.Fatal(err)
	}
	if err := repository.Save("demo-test", environment.DefaultDocument("demo", "test")); err != nil {
		t.Fatal(err)
	}
	infraDir := filepath.Join(root, "terraform", "infra")
	platformDir := filepath.Join(root, "terraform", "platform")
	statePath := terraformWorkspaceStatePath(infraDir, "demo-test")
	if err := os.MkdirAll(filepath.Dir(statePath), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(statePath, []byte(`{"resources":[{"mode":"managed","instances":[{}]}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	server := &Server{
		config:       &appconfig.Config{Paths: appconfig.PathsConfig{TerraformInfraDir: infraDir, TerraformPlatformDir: platformDir}},
		environments: repository,
	}
	project := access.Project{Key: "demo", Environments: []access.ProjectEnvironment{{
		ProjectKey: "demo", Environment: "test", TargetName: "demo-test",
	}}}
	if blockers := server.projectDeletionBlockers(context.Background(), project); len(blockers) != 1 {
		t.Fatalf("managed Terraform resource did not block deletion: %#v", blockers)
	}
	if err := os.WriteFile(statePath, []byte(`{"resources":[{"mode":"data","instances":[{}]}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if blockers := server.projectDeletionBlockers(context.Background(), project); len(blockers) != 0 {
		t.Fatalf("data-only destroyed state blocked deletion: %#v", blockers)
	}
}

func TestLatestResourceMutationRequiresSuccessfulDestroy(t *testing.T) {
	now := time.Now()
	items := []jobs.Job{
		{ID: "validate", Action: jobs.ActionValidate, Status: jobs.StatusSucceeded, CreatedAt: now.Add(2 * time.Minute)},
		{ID: "destroy", Action: jobs.ActionDestroy, Status: jobs.StatusSucceeded, CreatedAt: now.Add(time.Minute)},
		{ID: "deploy", Action: jobs.ActionDeploy, Status: jobs.StatusSucceeded, CreatedAt: now},
	}
	mutation, found := latestResourceMutation(items)
	if !found || mutation.ID != "destroy" || mutation.Status != jobs.StatusSucceeded {
		t.Fatalf("unexpected latest mutation: %#v found=%v", mutation, found)
	}
	items[1].Status = jobs.StatusFailed
	mutation, _ = latestResourceMutation(items)
	if mutation.Status != jobs.StatusFailed {
		t.Fatalf("failed destroy was not retained: %#v", mutation)
	}
}

func TestExistingEKSWithoutOwnedResourcesDoesNotBlockProjectDeletion(t *testing.T) {
	root := t.TempDir()
	repository, err := environment.NewRepository(filepath.Join(root, "environments"))
	if err != nil {
		t.Fatal(err)
	}
	doc := environment.DefaultDocument("demo", "test")
	if err := environment.ConfigureTarget(doc, environment.TargetExistingEKS); err != nil {
		t.Fatal(err)
	}
	doc["deployment_target"].(map[string]any)["cluster_name"] = "shared-eks"
	if err := repository.Save("demo-test", doc); err != nil {
		t.Fatal(err)
	}
	server := &Server{
		config: &appconfig.Config{Paths: appconfig.PathsConfig{
			TerraformInfraDir:    filepath.Join(root, "terraform", "infra"),
			TerraformPlatformDir: filepath.Join(root, "terraform", "platform"),
		}},
		environments: repository,
	}
	project := access.Project{Key: "demo", Environments: []access.ProjectEnvironment{{
		ProjectKey: "demo", Environment: "test", TargetName: "demo-test",
	}}}
	if blockers := server.projectDeletionBlockers(context.Background(), project); len(blockers) != 0 {
		t.Fatalf("empty existing-EKS environment blocked deletion: %#v", blockers)
	}
}

package runner

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/GZ-Alinx/awsinfra/internal/appconfig"
	"github.com/GZ-Alinx/awsinfra/internal/environment"
	"github.com/GZ-Alinx/awsinfra/internal/jobs"
	"github.com/GZ-Alinx/awsinfra/internal/statebackend"
)

func TestTerraformInitUsesCommittedLockFileReadOnly(t *testing.T) {
	args := terraformInitArgs()
	if !slices.Contains(args, "-lockfile=readonly") {
		t.Fatalf("terraform init must keep the image module immutable: %#v", args)
	}
}

type terraformLockExecutor struct {
	payload  string
	err      error
	commands []Command
}

type terraformLockSequenceExecutor struct {
	payloads []string
	errors   []error
	commands []Command
}

func (e *terraformLockSequenceExecutor) Run(_ context.Context, command Command, output io.Writer) error {
	index := len(e.commands)
	e.commands = append(e.commands, command)
	if index < len(e.payloads) {
		_, _ = io.WriteString(output, e.payloads[index])
	}
	if index < len(e.errors) {
		return e.errors[index]
	}
	return nil
}

type eksPublicAccessCIDRExecutor struct {
	payload  string
	err      error
	commands []Command
}

func (e *eksPublicAccessCIDRExecutor) Run(_ context.Context, command Command, output io.Writer) error {
	e.commands = append(e.commands, command)
	_, _ = io.WriteString(output, e.payload)
	return e.err
}

func TestEKSPublicAccessCIDRsMergeAWSAndPlatformWithoutDeleting(t *testing.T) {
	executor := &eksPublicAccessCIDRExecutor{payload: `["203.0.113.10/32","198.51.100.4/32"]`}
	repository, err := environment.NewRepository(filepath.Join(t.TempDir(), "environments"))
	if err != nil {
		t.Fatal(err)
	}
	deployment := &Deployment{
		config: &appconfig.Config{
			Tools: appconfig.ToolsConfig{AWS: "aws", Terraform: "terraform"},
			Paths: appconfig.PathsConfig{RepositoryRoot: "/workspace", TerraformInfraDir: "/workspace/terraform/infra"},
		},
		environments: repository,
		executor:     executor,
	}
	doc := environment.Document{
		"project": "demo", "environment": "test", "region": "ap-south-1",
		"eks": map[string]any{"public_access_cidrs": []any{"198.51.100.4/32", "192.0.2.8/32"}},
	}
	ctx := context.WithValue(context.Background(), awsEnvironmentContextKey{}, []string{"AWS_ACCESS_KEY_ID=PROJECT"})
	var log bytes.Buffer
	mergedContext, err := deployment.withMergedEKSPublicAccessCIDRs(ctx, doc, &log)
	if err != nil {
		t.Fatal(err)
	}
	merged, _ := mergedContext.Value(eksPublicAccessCIDRsContextKey{}).([]string)
	want := []string{"192.0.2.8/32", "198.51.100.4/32", "203.0.113.10/32"}
	if !slices.Equal(merged, want) {
		t.Fatalf("merged CIDRs = %#v, want %#v", merged, want)
	}
	if !strings.Contains(log.String(), "AWS 当前 2 条") || !strings.Contains(log.String(), "不会删除 AWS 已有地址") {
		t.Fatalf("operator log does not explain additive behavior: %q", log.String())
	}
	if len(executor.commands) != 1 || !slices.Contains(executor.commands[0].Args, "--no-cli-pager") {
		t.Fatalf("unexpected AWS lookup: %#v", executor.commands)
	}

	if err := deployment.terraformPlanToFile(mergedContext, "/workspace/terraform/infra", "demo-test", "", "/tmp/demo.tfplan", stepPlanInfra, io.Discard); err != nil {
		t.Fatal(err)
	}
	planArgs := executor.commands[len(executor.commands)-1].Args
	if !slices.Contains(planArgs, `-var=eks_public_access_cidrs_override=["192.0.2.8/32","198.51.100.4/32","203.0.113.10/32"]`) {
		t.Fatalf("Terraform plan did not receive the merged whitelist: %#v", planArgs)
	}
}

func TestEKSPublicAccessCIDRsUseConfiguredValuesForANewCluster(t *testing.T) {
	executor := &eksPublicAccessCIDRExecutor{payload: "ResourceNotFoundException: No cluster found", err: errors.New("aws exited with an error")}
	deployment := &Deployment{
		config:   &appconfig.Config{Tools: appconfig.ToolsConfig{AWS: "aws"}, Paths: appconfig.PathsConfig{RepositoryRoot: "/workspace"}},
		executor: executor,
	}
	doc := environment.Document{
		"project": "demo", "environment": "dev", "region": "ap-south-1",
		"eks": map[string]any{"public_access_cidrs": []any{"203.0.113.10/32"}},
	}
	mergedContext, err := deployment.withMergedEKSPublicAccessCIDRs(context.Background(), doc, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	merged, _ := mergedContext.Value(eksPublicAccessCIDRsContextKey{}).([]string)
	if !slices.Equal(merged, []string{"203.0.113.10/32"}) {
		t.Fatalf("new cluster CIDRs = %#v", merged)
	}
}

func TestEKSPublicAccessCIDRsFailClosedWhenAWSCannotBeRead(t *testing.T) {
	executor := &eksPublicAccessCIDRExecutor{payload: "AccessDeniedException", err: errors.New("aws exited with an error")}
	deployment := &Deployment{
		config:   &appconfig.Config{Tools: appconfig.ToolsConfig{AWS: "aws"}, Paths: appconfig.PathsConfig{RepositoryRoot: "/workspace"}},
		executor: executor,
	}
	doc := environment.Document{
		"project": "demo", "environment": "prod", "region": "ap-south-1",
		"eks": map[string]any{"public_access_cidrs": []any{"203.0.113.10/32"}},
	}
	_, err := deployment.withMergedEKSPublicAccessCIDRs(context.Background(), doc, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "已安全停止") || !strings.Contains(err.Error(), "避免覆盖") {
		t.Fatalf("AWS read failure must fail closed, got %v", err)
	}
}

func (e *terraformLockExecutor) Run(_ context.Context, command Command, output io.Writer) error {
	e.commands = append(e.commands, command)
	_, _ = io.WriteString(output, e.payload)
	return e.err
}

func TestTerraformStateLockPreflightAllowsMissingLock(t *testing.T) {
	executor := &terraformLockExecutor{payload: "fatal error: An error occurred (404) when calling the HeadObject operation: Key does not exist", err: errors.New("aws exited with an error")}
	deployment := &Deployment{
		config: &appconfig.Config{
			TerraformState: appconfig.TerraformStateConfig{Enabled: true},
			Tools:          appconfig.ToolsConfig{AWS: "aws"},
			Paths:          appconfig.PathsConfig{RepositoryRoot: "/workspace"},
		},
		executor: executor,
	}
	ctx := context.WithValue(context.Background(), stateProjectContextKey{}, "demo")
	ctx = context.WithValue(ctx, stateBackendContextKey{}, statebackend.Runtime{
		Bucket: "state-bucket", Region: "ap-south-1", KeyPrefix: "ops", AccessKeyID: "BACKENDKEY", SecretAccessKey: "backend-secret",
	})
	ctx = context.WithValue(ctx, awsEnvironmentContextKey{}, []string{"AWS_ACCESS_KEY_ID=PROJECTKEY", "AWS_SECRET_ACCESS_KEY=project-secret"})
	var log bytes.Buffer
	if err := deployment.checkTerraformStateLock(ctx, "demo-test", "platform", &log); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(log.String(), "未发现活动锁") {
		t.Fatalf("safe lock result is missing from operator log: %q", log.String())
	}
	if len(executor.commands) != 1 || !strings.Contains(strings.Join(executor.commands[0].Args, " "), "ops/projects/demo/demo-test/platform/terraform.tfstate.tflock") {
		t.Fatalf("unexpected lock lookup command: %#v", executor.commands)
	}
	merged := mergeEnvironment(nil, executor.commands[0].Env)
	if !slices.Contains(merged, "AWS_ACCESS_KEY_ID=BACKENDKEY") || slices.Contains(merged, "AWS_ACCESS_KEY_ID=PROJECTKEY") {
		t.Fatalf("state lock lookup did not use backend credentials: %#v", merged)
	}
}

func TestTerraformStateLockPreflightBlocksBeforeComponentMutation(t *testing.T) {
	executor := &terraformLockExecutor{payload: `{"ID":"lock-123","Operation":"OperationTypeApply","Who":"old-platform-pod","Created":"2026-08-10T09:00:20Z"}`}
	deployment := &Deployment{
		config: &appconfig.Config{
			TerraformState: appconfig.TerraformStateConfig{Enabled: true},
			Tools:          appconfig.ToolsConfig{AWS: "aws"},
			Paths:          appconfig.PathsConfig{RepositoryRoot: "/workspace"},
		},
		executor: executor,
	}
	ctx := context.WithValue(context.Background(), stateProjectContextKey{}, "demo")
	ctx = context.WithValue(ctx, stateBackendContextKey{}, statebackend.Runtime{
		Bucket: "state-bucket", Region: "ap-south-1", KeyPrefix: "ops", AccessKeyID: "BACKENDKEY", SecretAccessKey: "backend-secret",
	})
	err := deployment.checkTerraformStateLock(ctx, "demo-test", "platform", io.Discard)
	if err == nil || !strings.Contains(err.Error(), "lock-123") || !strings.Contains(err.Error(), "修改任何 Helm 组件前安全停止") {
		t.Fatalf("active state lock must fail closed with actionable details, got %v", err)
	}
	steps := deploymentSteps(jobs.ActionPlatform, false)
	lockIndex := slices.Index(steps, stepCheckPlatformStateLock)
	reconcileIndex := slices.Index(steps, stepReconcileReleases)
	if lockIndex < 0 || reconcileIndex < 0 || lockIndex >= reconcileIndex {
		t.Fatalf("state lock preflight must precede component reconciliation: %#v", steps)
	}
}

func TestTerraformStateLockPreflightAutoUnlocksOldPlatformPodLock(t *testing.T) {
	created := time.Now().Add(-stalePlatformTerraformLockAge - time.Minute).UTC().Format(time.RFC3339Nano)
	payload := fmt.Sprintf(`{"ID":"lock-stale","Operation":"OperationTypeApply","Who":"runner@ops-deploy-platform-deadbeef","Created":%q}`, created)
	executor := &terraformLockSequenceExecutor{payloads: []string{payload, payload, `{}`}}
	deployment := &Deployment{
		config: &appconfig.Config{
			TerraformState: appconfig.TerraformStateConfig{Enabled: true},
			Tools:          appconfig.ToolsConfig{AWS: "aws"},
			Paths:          appconfig.PathsConfig{RepositoryRoot: "/workspace"},
		},
		executor: executor,
	}
	ctx := context.WithValue(context.Background(), stateProjectContextKey{}, "demo")
	ctx = context.WithValue(ctx, stateBackendContextKey{}, statebackend.Runtime{
		Bucket: "state-bucket", Region: "ap-south-1", KeyPrefix: "ops", AccessKeyID: "BACKENDKEY", SecretAccessKey: "backend-secret",
	})
	var log bytes.Buffer
	if err := deployment.checkTerraformStateLock(ctx, "demo-test", "platform", &log); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(log.String(), "[自动恢复]") || !strings.Contains(log.String(), "lock-stale") {
		t.Fatalf("automatic stale-lock recovery is missing from operator log: %q", log.String())
	}
	if len(executor.commands) != 3 || !slices.Contains(executor.commands[2].Args, "delete-object") {
		t.Fatalf("stale lock was not read twice and then deleted: %#v", executor.commands)
	}
}

func TestTerraformStateLockPreflightNeverUnlocksRecentPlatformLock(t *testing.T) {
	created := time.Now().Add(-time.Minute).UTC().Format(time.RFC3339Nano)
	payload := fmt.Sprintf(`{"ID":"lock-active","Operation":"OperationTypeApply","Who":"runner@ops-deploy-platform-active","Created":%q}`, created)
	executor := &terraformLockSequenceExecutor{payloads: []string{payload}}
	deployment := &Deployment{
		config: &appconfig.Config{
			TerraformState: appconfig.TerraformStateConfig{Enabled: true},
			Tools:          appconfig.ToolsConfig{AWS: "aws"},
			Paths:          appconfig.PathsConfig{RepositoryRoot: "/workspace"},
		},
		executor: executor,
	}
	ctx := context.WithValue(context.Background(), stateProjectContextKey{}, "demo")
	ctx = context.WithValue(ctx, stateBackendContextKey{}, statebackend.Runtime{
		Bucket: "state-bucket", Region: "ap-south-1", KeyPrefix: "ops", AccessKeyID: "BACKENDKEY", SecretAccessKey: "backend-secret",
	})
	err := deployment.checkTerraformStateLock(ctx, "demo-test", "platform", io.Discard)
	if err == nil || !strings.Contains(err.Error(), "lock-active") {
		t.Fatalf("recent platform lock must remain protected, got %v", err)
	}
	if len(executor.commands) != 1 {
		t.Fatalf("recent platform lock must not be re-read or deleted: %#v", executor.commands)
	}
}

func TestTerraformStateBucketNameIsStableAndBounded(t *testing.T) {
	name := terraformStateBucketName("ops-tfstate", "123456789012", "project-with-a-name-longer-than-twenty-four-characters")
	if len(name) > 63 {
		t.Fatalf("state bucket name exceeds S3 limit: %q", name)
	}
	if name != terraformStateBucketName("ops-tfstate", "123456789012", "project-with-a-name-longer-than-twenty-four-characters") {
		t.Fatalf("state bucket name is not deterministic: %q", name)
	}
	other := terraformStateBucketName("ops-tfstate", "123456789012", "project-with-a-name-longer-than-twenty-four-different")
	if name == other {
		t.Fatalf("truncated project names collided: %q", name)
	}
}

func TestDecodeTerraformStateRetainsLineageAndResources(t *testing.T) {
	state, err := decodeTerraformState([]byte(`{"lineage":"abc","serial":7,"resources":[{},{}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if state.Lineage != "abc" || state.Serial != 7 || len(state.Resources) != 2 {
		t.Fatalf("unexpected state metadata: %#v", state)
	}
}

func TestTerraformStateMigrationNeverRestoresDestroyedResources(t *testing.T) {
	managed := []json.RawMessage{json.RawMessage(`{}`)}
	staleLocal := terraformStateSnapshot{Lineage: "same", Serial: 7, Resources: []terraformStateResource{{Mode: "managed", Instances: managed}}}
	destroyedRemote := terraformStateSnapshot{Lineage: "same", Serial: 9}
	if got := decideTerraformStateMigration(staleLocal, destroyedRemote); got != terraformStateArchiveLocal {
		t.Fatalf("destroyed remote state must supersede stale local resources: got %v", got)
	}
	newRemoteWorkspace := terraformStateSnapshot{Lineage: "new-workspace", Serial: 1}
	if got := decideTerraformStateMigration(staleLocal, newRemoteWorkspace); got != terraformStatePushLocal {
		t.Fatalf("brand-new remote workspace should receive the legacy state: got %v", got)
	}
	destroyedLegacy := terraformStateSnapshot{Lineage: "same", Serial: 12}
	if got := decideTerraformStateMigration(destroyedLegacy, newRemoteWorkspace); got != terraformStateArchiveLocal {
		t.Fatalf("legacy state without managed instances should only be archived: got %v", got)
	}
}

func TestTerraformStateMigrationRejectsConflicts(t *testing.T) {
	managed := []json.RawMessage{json.RawMessage(`{}`)}
	local := terraformStateSnapshot{Lineage: "local", Serial: 7, Resources: []terraformStateResource{{Mode: "managed", Instances: managed}}}
	remote := terraformStateSnapshot{Lineage: "remote", Serial: 8, Resources: []terraformStateResource{{Mode: "managed", Instances: managed}}}
	if got := decideTerraformStateMigration(local, remote); got != terraformStateLineageConflict {
		t.Fatalf("different lineages must be blocked: got %v", got)
	}
	remote = terraformStateSnapshot{Lineage: "local", Serial: 6, Resources: []terraformStateResource{{Mode: "managed", Instances: managed}}}
	if got := decideTerraformStateMigration(local, remote); got != terraformStateLocalNewer {
		t.Fatalf("newer local state needs manual recovery: got %v", got)
	}
}

func TestTerraformStateMigrationArchivesDataOnlyLegacyState(t *testing.T) {
	local := terraformStateSnapshot{
		Lineage: "legacy", Serial: 18,
		Resources: []terraformStateResource{{Mode: "data", Instances: []json.RawMessage{json.RawMessage(`{}`), json.RawMessage(`{}`)}}},
	}
	remote := terraformStateSnapshot{Lineage: "central", Serial: 1}
	if got := decideTerraformStateMigration(local, remote); got != terraformStateArchiveLocal {
		t.Fatalf("data-only legacy state must not replace central lineage: got %v", got)
	}
}

func TestLegacyStatesAreStagedByOwningProject(t *testing.T) {
	root := t.TempDir()
	repository, err := environment.NewRepository(filepath.Join(root, "environments"))
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range []struct{ project, target string }{{"alpha", "alpha-test"}, {"beta", "beta-test"}} {
		if err := repository.Save(item.target, environment.DefaultDocument(item.project, "test")); err != nil {
			t.Fatal(err)
		}
		source := filepath.Join(root, "terraform", "platform", "terraform.tfstate.d", item.target, "terraform.tfstate")
		if err := os.MkdirAll(filepath.Dir(source), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(source, []byte(`{"lineage":"`+item.project+`","resources":[{}]}`), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	deployment := &Deployment{
		config:       &appconfig.Config{Paths: appconfig.PathsConfig{DataDir: filepath.Join(root, "data")}},
		environments: repository,
	}
	dir := filepath.Join(root, "terraform", "platform")
	if err := deployment.stageAllLegacyLocalStates(dir, os.Stderr); err != nil {
		t.Fatal(err)
	}
	for _, item := range []struct{ project, target string }{{"alpha", "alpha-test"}, {"beta", "beta-test"}} {
		staged := deployment.stagedLocalStatePath(item.project, item.target, "platform")
		if _, err := os.Stat(staged); err != nil {
			t.Fatalf("state was not staged for %s: %v", item.project, err)
		}
		legacy := filepath.Join(dir, "terraform.tfstate.d", item.target, "terraform.tfstate")
		if _, err := os.Stat(legacy); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("legacy state remained active for %s: %v", item.project, err)
		}
	}
}

func TestOrphanedLegacyStateDoesNotBlockAnotherProject(t *testing.T) {
	root := t.TempDir()
	repository, err := environment.NewRepository(filepath.Join(root, "environments"))
	if err != nil {
		t.Fatal(err)
	}
	if err := repository.Save("active-test", environment.DefaultDocument("active", "test")); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(root, "terraform", "platform")
	for _, target := range []string{"deleted-test", "active-test"} {
		source := filepath.Join(dir, "terraform.tfstate.d", target, "terraform.tfstate")
		if err := os.MkdirAll(filepath.Dir(source), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(source, []byte(`{"lineage":"`+target+`","resources":[]}`), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	deployment := &Deployment{
		config:       &appconfig.Config{Paths: appconfig.PathsConfig{DataDir: filepath.Join(root, "data")}},
		environments: repository,
	}
	var log bytes.Buffer
	if err := deployment.stageAllLegacyLocalStates(dir, &log); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "terraform.tfstate.d", "deleted-test", "terraform.tfstate")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("orphaned state remained in Terraform's active workspace: %v", err)
	}
	if _, err := os.Stat(deployment.orphanedLocalStatePath("deleted-test", "platform")); err != nil {
		t.Fatalf("orphaned state was not preserved in quarantine: %v", err)
	}
	if _, err := os.Stat(deployment.stagedLocalStatePath("active", "active-test", "platform")); err != nil {
		t.Fatalf("owned state was not staged: %v", err)
	}
	if !strings.Contains(log.String(), "已隔离无法确认归属") || !strings.Contains(log.String(), "不会迁移、覆盖或删除") {
		t.Fatalf("operator-safe orphan message missing: %q", log.String())
	}
}

func TestValidAWSResourceID(t *testing.T) {
	for _, test := range []struct {
		value  string
		prefix string
		valid  bool
	}{
		{value: "vpc-0a36439e5b2c31040", prefix: "vpc-", valid: true},
		{value: "eni-03838ce0ad827bb06", prefix: "eni-", valid: true},
		{value: "sg-0ef5e20b72e342f81", prefix: "sg-", valid: true},
		{value: "eni-03838ce0;rm", prefix: "eni-", valid: false},
		{value: "subnet-03838ce0", prefix: "eni-", valid: false},
		{value: "eni-", prefix: "eni-", valid: false},
	} {
		if got := validAWSResourceID(test.value, test.prefix); got != test.valid {
			t.Errorf("validAWSResourceID(%q, %q)=%v, want %v", test.value, test.prefix, got, test.valid)
		}
	}
}

func TestExistingEKSDeploymentStepsSkipInfrastructure(t *testing.T) {
	steps := deploymentSteps(jobs.ActionPlatform, true)
	for _, forbidden := range []string{stepInitializeInfra, stepPrepareInfra, stepApplyInfra, stepDestroyInfra} {
		if slices.Contains(steps, forbidden) {
			t.Fatalf("existing EKS component deployment contains infrastructure step %q: %#v", forbidden, steps)
		}
	}
	for _, required := range []string{stepUpdateKubeconfig, stepCheckExistingEKS, stepApplyComponents, stepSyncGatewayAddress} {
		if !slices.Contains(steps, required) {
			t.Fatalf("existing EKS component deployment is missing %q: %#v", required, steps)
		}
	}

	destroySteps := deploymentSteps(jobs.ActionDestroy, true)
	if slices.Contains(destroySteps, stepDestroyInfra) || !slices.Contains(destroySteps, stepDestroyPlatform) {
		t.Fatalf("existing EKS destroy ownership boundary is wrong: %#v", destroySteps)
	}
}

func TestManagedDestroyStagesComputeAndNetworkCleanupBeforeVPCDestroy(t *testing.T) {
	steps := deploymentSteps(jobs.ActionDestroy, false)
	computeIndex := slices.Index(steps, stepDestroyEKSCompute)
	cleanupIndex := slices.Index(steps, stepCleanupOrphanedNetwork)
	infraIndex := slices.Index(steps, stepDestroyInfra)
	if computeIndex < 0 || cleanupIndex < 0 || infraIndex < 0 {
		t.Fatalf("managed destroy is missing staged cleanup steps: %#v", steps)
	}
	if !(computeIndex < cleanupIndex && cleanupIndex < infraIndex) {
		t.Fatalf("managed destroy order must be compute -> ENI cleanup -> VPC: %#v", steps)
	}
}

type managedVPCRecoveryExecutor struct {
	commands []Command
	vpcsJSON string
}

func (e *managedVPCRecoveryExecutor) Run(_ context.Context, command Command, output io.Writer) error {
	e.commands = append(e.commands, command)
	if command.Name == "terraform" {
		_, _ = io.WriteString(output, "\x1b[33mWarning: No outputs found\x1b[0m\n")
		return nil
	}
	_, _ = io.WriteString(output, e.vpcsJSON)
	return nil
}

func TestManagedVPCCleanupRecoversFromClearedTerraformOutputs(t *testing.T) {
	executor := &managedVPCRecoveryExecutor{vpcsJSON: `{"Vpcs":[{"VpcId":"vpc-091ff32b06318e8cb"}]}`}
	deployment := &Deployment{
		config: &appconfig.Config{
			Tools: appconfig.ToolsConfig{AWS: "aws", Terraform: "terraform"},
			Paths: appconfig.PathsConfig{RepositoryRoot: "/workspace", TerraformInfraDir: "/workspace/terraform/infra"},
		},
		executor: executor,
	}
	doc := environment.Document{"project": "project-17be2ac0", "environment": "uat", "region": "ap-south-1"}
	var log bytes.Buffer
	vpcID, found, err := deployment.resolveManagedVPCIDForCleanup(context.Background(), "project-17be2ac0-uat", doc, &log)
	if err != nil {
		t.Fatal(err)
	}
	if !found || vpcID != "vpc-091ff32b06318e8cb" {
		t.Fatalf("resolved VPC = %q, found=%v", vpcID, found)
	}
	if !strings.Contains(log.String(), "按项目/环境归属标签") || !strings.Contains(log.String(), vpcID) {
		t.Fatalf("recovery log is not operator-friendly: %q", log.String())
	}
	if len(executor.commands) != 2 || !slices.Contains(executor.commands[1].Args, "Name=tag:Project,Values=project-17be2ac0") {
		t.Fatalf("unexpected VPC ownership lookup: %#v", executor.commands)
	}
}

func TestManagedVPCCleanupFailsClosedForAmbiguousOwnership(t *testing.T) {
	executor := &managedVPCRecoveryExecutor{vpcsJSON: `{"Vpcs":[{"VpcId":"vpc-091ff32b06318e8cb"},{"VpcId":"vpc-0a36439e5b2c31040"}]}`}
	deployment := &Deployment{
		config: &appconfig.Config{
			Tools: appconfig.ToolsConfig{AWS: "aws", Terraform: "terraform"},
			Paths: appconfig.PathsConfig{RepositoryRoot: "/workspace", TerraformInfraDir: "/workspace/terraform/infra"},
		},
		executor: executor,
	}
	doc := environment.Document{"project": "project-17be2ac0", "environment": "uat", "region": "ap-south-1"}
	_, _, err := deployment.resolveManagedVPCIDForCleanup(context.Background(), "project-17be2ac0-uat", doc, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "拒绝模糊清理") {
		t.Fatalf("ambiguous VPC ownership must fail closed, got %v", err)
	}
}

func TestManagedPhaseOneDeploymentIncludesValidationAndSavedPlans(t *testing.T) {
	steps := deploymentSteps(jobs.ActionDeploy, false)
	for _, required := range []string{stepCheckFormatting, stepValidateInfra, stepValidatePlatform, stepPlanInfra, stepApplyInfra, stepCheckPlatformStateLock, stepReconcileNamespaces, stepPlanBase, stepApplyBase} {
		if !slices.Contains(steps, required) {
			t.Fatalf("unified phase-one deployment is missing %q: %#v", required, steps)
		}
	}
	if slices.Index(steps, stepReconcileNamespaces) >= slices.Index(steps, stepPlanBase) {
		t.Fatalf("Namespace reconciliation must happen before the phase-one platform plan: %#v", steps)
	}
	seen := make(map[string]bool, len(steps))
	for _, step := range steps {
		if seen[step] {
			t.Fatalf("unified phase-one deployment contains duplicate step %q: %#v", step, steps)
		}
		seen[step] = true
	}
}

func TestPhaseTargetsKeepBaseServicesOutOfComponentUpdates(t *testing.T) {
	base := phaseOneBaseTargets()
	components := phaseTwoComponentTargets()
	for _, required := range []string{
		"helm_release.consul",
		"kubernetes_service_v1.consul_http",
		"helm_release.etcd",
		"kubernetes_secret_v1.etcd_tls",
		"random_password.etcd_web",
		"kubernetes_cron_job_v1.consul_backup",
		"kubernetes_cron_job_v1.etcd_backup",
		"aws_iam_role.platform_backup",
	} {
		if !slices.Contains(base, required) {
			t.Fatalf("phase one is missing base target %q: %#v", required, base)
		}
		if slices.Contains(components, required) {
			t.Fatalf("phase two must not upgrade base target %q: %#v", required, components)
		}
	}
	for _, required := range []string{
		"helm_release.catalog",
		"aws_security_group.higress_nlb",
		"aws_vpc_security_group_ingress_rule.higress_nlb_ipv4",
		"aws_vpc_security_group_ingress_rule.higress_nlb_ipv6",
		"aws_vpc_security_group_egress_rule.higress_nlb",
		"kubernetes_ingress_v1.domain",
		"kubernetes_service_v1.tcp_route",
		"kubernetes_config_map_v1.alerting",
		"random_password.clickvisual_mysql",
		"random_password.clickvisual_clickhouse",
		"random_password.clickvisual_admin",
		"random_password.clickvisual_proxy_token",
		"random_password.clickvisual_secret_key",
		"random_password.clickvisual_encryption_key",
		"random_id.clickvisual_kafka_cluster",
		"random_password.efk_elastic",
		"random_password.efk_kibana_system",
		"random_password.efk_fluentd",
		"random_password.efk_security_key",
		"random_password.efk_saved_objects_key",
		"random_password.efk_reporting_key",
		"random_password.etcd_workbench_admin",
		"random_password.etcd_workbench_encryption",
	} {
		if !slices.Contains(components, required) {
			t.Fatalf("phase two is missing component target %q: %#v", required, components)
		}
	}
	seen := make(map[string]bool, len(components))
	for _, target := range components {
		if seen[target] {
			t.Fatalf("phase two contains duplicate target %q: %#v", target, components)
		}
		seen[target] = true
	}
}

func TestComponentLifecyclePlanCoversInstallRepairUpdateAndUninstall(t *testing.T) {
	doc := environment.DefaultDocument("demo", "test")
	components := doc["components"].(map[string]any)
	components["etcd"].(map[string]any)["enabled"] = true
	catalog := components["catalog"].(map[string]any)
	for _, key := range []string{"mysql", "prometheus", "loki"} {
		catalog[key].(map[string]any)["enabled"] = true
	}
	releases := []helmListRelease{
		{Name: "consul", Namespace: "platform-server", Status: "deployed"},
		{Name: "prometheus", Namespace: "monitoring", Status: "deployed"},
		{Name: "loki", Namespace: "monitoring", Status: "failed"},
		{Name: "efk-stack", Namespace: "demo-test-efk-system", Status: "deployed"},
	}

	base := componentLifecyclePlan(doc, "base", releases)
	componentPlan := componentLifecyclePlan(doc, "components", releases)
	actions := make(map[string]string)
	for _, item := range append(base, componentPlan...) {
		actions[item.Key] = item.Action
	}
	for key, expected := range map[string]string{
		"consul": "卸载", "etcd": "安装", "mysql": "安装", "prometheus": "更新/对账", "loki": "修复", "efk_stack": "卸载",
	} {
		if actions[key] != expected {
			t.Fatalf("component %s action=%q, want %q; all=%#v", key, actions[key], expected, actions)
		}
	}
	var output bytes.Buffer
	writeComponentLifecyclePlan(&output, "components", componentPlan)
	for _, expected := range []string{"[安装] MySQL（自建）", "[修复] Loki", "[卸载] EFK 日志系统", "Namespace 永久保留", "计划汇总"} {
		if !strings.Contains(output.String(), expected) {
			t.Fatalf("lifecycle log is missing %q:\n%s", expected, output.String())
		}
	}
}

func TestEFKCredentialsFollowEnabledLifecycle(t *testing.T) {
	payload, err := os.ReadFile(filepath.Join("..", "..", "terraform", "platform", "catalog-components.tf"))
	if err != nil {
		t.Fatal(err)
	}
	source := string(payload)
	if strings.Contains(source, `local.phase_two_enabled && contains(keys(local.catalog_components), "efk_stack")`) {
		t.Fatal("EFK credentials would survive after the component is disabled")
	}
	if count := strings.Count(source, `contains(keys(local.enabled_catalog_components), "efk_stack") ? 1 : 0`); count < 6 {
		t.Fatalf("only %d EFK credential resources follow the enabled lifecycle, want at least 6", count)
	}
}

func TestHigressNLBSupportsManagedAndCustomFrontendSecurityGroups(t *testing.T) {
	networkPayload, err := os.ReadFile(filepath.Join("..", "..", "terraform", "platform", "higress-network.tf"))
	if err != nil {
		t.Fatal(err)
	}
	catalogPayload, err := os.ReadFile(filepath.Join("..", "..", "terraform", "platform", "catalog-components.tf"))
	if err != nil {
		t.Fatal(err)
	}
	addonsPayload, err := os.ReadFile(filepath.Join("..", "..", "terraform", "platform", "helm-addons.tf"))
	if err != nil {
		t.Fatal(err)
	}
	network := string(networkPayload)
	catalog := string(catalogPayload)
	addons := string(addonsPayload)
	for _, required := range []string{
		`higress_nlb_security_group_mode`,
		`higress_nlb_allowed_ports`,
		`data "aws_security_group" "higress_nlb_custom"`,
		`higress_nlb_custom_security_groups_use_cluster_vpc`,
		`higress_nlb_custom_security_groups_are_safe_frontends`,
		`security_group.vpc_id == local.vpc_id`,
		`local.higress_nlb_security_group_mode != "custom"`,
	} {
		if !strings.Contains(network, required) {
			t.Fatalf("Higress NLB security group Terraform is missing %q", required)
		}
	}
	for _, required := range []string{
		`join(",", local.higress_nlb_frontend_security_group_ids)`,
		`manage_backend_security_group_rules`,
		`aws-load-balancer-ip-address-type`,
		`aws-load-balancer-additional-resource-tags`,
	} {
		if !strings.Contains(catalog, required) {
			t.Fatalf("Higress Helm annotations are missing %q", required)
		}
	}
	for _, required := range []string{
		`enableBackendSecurityGroup          = true`,
		`disableRestrictedSecurityGroupRules = false`,
	} {
		if !strings.Contains(addons, required) {
			t.Fatalf("AWS Load Balancer Controller security settings are missing %q", required)
		}
	}
}

func TestInspectHigressCustomSecurityGroupsRejectsUnsafeGroupsAndSummarizesPorts(t *testing.T) {
	from80, to443 := 80, 443
	groups := []higressNLBSecurityGroup{{
		ID: "sg-0123456789abcdef0", Name: "nlb-frontend", VPCID: "vpc-0123456789abcdef0",
		Permissions: []higressNLBPermission{{
			Protocol: "tcp", FromPort: &from80, ToPort: &to443,
			IPRanges: []struct {
				CIDR string `json:"CidrIp"`
			}{{CIDR: "0.0.0.0/0"}},
		}},
	}}
	http, https, publicHTTP, publicHTTPS, err := inspectHigressCustomSecurityGroups(groups, "vpc-0123456789abcdef0")
	if err != nil || !http || !https || !publicHTTP || !publicHTTPS {
		t.Fatalf("unexpected custom security group inspection: http=%t https=%t publicHTTP=%t publicHTTPS=%t err=%v", http, https, publicHTTP, publicHTTPS, err)
	}

	groups[0].Name = "default"
	if _, _, _, _, err := inspectHigressCustomSecurityGroups(groups, "vpc-0123456789abcdef0"); err == nil || !strings.Contains(err.Error(), "默认安全组") {
		t.Fatalf("default security group error = %v", err)
	}
	groups[0].Name = "nlb-frontend"
	groups[0].VPCID = "vpc-1123456789abcdef0"
	if _, _, _, _, err := inspectHigressCustomSecurityGroups(groups, "vpc-0123456789abcdef0"); err == nil || !strings.Contains(err.Error(), "跨 VPC") {
		t.Fatalf("cross-VPC security group error = %v", err)
	}
}

func TestCatalogPoolSelectorHonorsCapacityDeferredMigration(t *testing.T) {
	payload, err := os.ReadFile(filepath.Join("..", "..", "terraform", "platform", "catalog-components.tf"))
	if err != nil {
		t.Fatal(err)
	}
	localsPayload, err := os.ReadFile(filepath.Join("..", "..", "terraform", "platform", "locals.tf"))
	if err != nil {
		t.Fatal(err)
	}
	source := string(payload)
	if !strings.Contains(source, `contains(keys(local.platform_node_selector), "ops-deploy.io/pool")`) {
		t.Fatal("catalog components must only receive the exact platform pool selector when locals expose it")
	}
	if strings.Contains(source, `try(local.platform_node_selector["ops-deploy.io/pool"], "platform-ops")`) {
		t.Fatal("capacity-deferred migrations must not recreate an intentionally omitted platform pool selector")
	}
	if !strings.Contains(source, `!contains(local.catalog_zonal_storage_components, each.key)`) {
		t.Fatal("zonal-PVC catalog components must not be pinned to one exact platform node pool")
	}
	for _, component := range []string{"rabbitmq", "prometheus", "bytebase", "jenkins", "loki", "efk_stack"} {
		if !strings.Contains(string(localsPayload), `"`+component+`"`) {
			t.Fatalf("zonal storage component %s is missing from scheduling protection", component)
		}
	}
}

func TestManagedEKSAddonsTolerateIsolatedNodePools(t *testing.T) {
	payload, err := os.ReadFile(filepath.Join("..", "..", "terraform", "platform", "addons.tf"))
	if err != nil {
		t.Fatal(err)
	}
	source := string(payload)
	for _, expected := range []string{
		`each.key == "coredns"`,
		`nodeSelector = local.platform_node_selector`,
		`local.platform_tolerations`,
		`node-role.kubernetes.io/control-plane`,
		`tolerateAllTaints = true`,
		`create = "30m"`,
		`update = "30m"`,
	} {
		if !strings.Contains(source, expected) {
			t.Fatalf("managed EKS add-on scheduling is missing %q", expected)
		}
	}
}

func TestPrometheusAdmissionHooksToleratePlatformNodePool(t *testing.T) {
	payload, err := os.ReadFile(filepath.Join("..", "..", "terraform", "platform", "locals.tf"))
	if err != nil {
		t.Fatal(err)
	}
	source := string(payload)
	for _, expected := range []string{
		`prometheusOperator.admissionWebhooks.patch.nodeSelector.workload-class`,
		`prometheusOperator.admissionWebhooks.patch.tolerations`,
	} {
		if !strings.Contains(source, expected) {
			t.Fatalf("Prometheus pre-install hook scheduling is missing %q", expected)
		}
	}
}

func TestOpenTelemetryCollectorClusterModeControlsReplicasAndDisruptionBudget(t *testing.T) {
	terraformPayload, err := os.ReadFile(filepath.Join("..", "..", "terraform", "platform", "catalog-components.tf"))
	if err != nil {
		t.Fatal(err)
	}
	chartPayload, err := os.ReadFile(filepath.Join("..", "..", "terraform", "platform", "charts", "observability-otel", "templates", "gateway.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	source := string(terraformPayload) + string(chartPayload)
	for _, expected := range []string{
		`each.key == "opentelemetry_collector"`,
		`.Values.replicaCount`,
		`kind: StatefulSet`,
		`kind: PodDisruptionBudget`,
		`minAvailable: 1`,
	} {
		if !strings.Contains(source, expected) {
			t.Fatalf("OpenTelemetry Collector Terraform wiring is missing %q", expected)
		}
	}
}

func TestOpenTelemetryElasticsearchIsIndependentFromEFK(t *testing.T) {
	terraformPayload, err := os.ReadFile(filepath.Join("..", "..", "terraform", "platform", "catalog-components.tf"))
	if err != nil {
		t.Fatal(err)
	}
	chartPayload, err := os.ReadFile(filepath.Join("..", "..", "terraform", "platform", "charts", "otel-elasticsearch", "templates", "elasticsearch.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	helperPayload, err := os.ReadFile(filepath.Join("..", "..", "terraform", "platform", "charts", "otel-elasticsearch", "templates", "_helpers.tpl"))
	if err != nil {
		t.Fatal(err)
	}
	source := string(terraformPayload) + string(chartPayload) + string(helperPayload)
	for _, expected := range []string{
		`resource "helm_release" "otel_elasticsearch"`,
		`name             = "otel-elasticsearch"`,
		`ops-deploy.io/component: otel-elasticsearch`,
		`storage: {{ .Values.storage.initialSize | quote }}`,
		`kind: StatefulSet`,
	} {
		if !strings.Contains(source, expected) {
			t.Fatalf("dedicated OpenTelemetry Elasticsearch wiring is missing %q", expected)
		}
	}
	if strings.Contains(source, `storage.elasticsearch.endpoint" = local.efk_elasticsearch_url`) || strings.Contains(source, `destinations.elasticsearch.endpoint" = local.efk_elasticsearch_url`) {
		t.Fatal("OpenTelemetry or Jaeger still points at the EFK Elasticsearch endpoint")
	}
}

func TestCloudServiceLifecyclePlanUsesDesiredConfigAndTerraformState(t *testing.T) {
	doc := environment.DefaultDocument("demo", "test")
	services := doc["data_services"].(map[string]any)
	services["rds"].(map[string]any)["enabled"] = true
	services["aurora"].(map[string]any)["enabled"] = true
	services["aurora"].(map[string]any)["deletion_protection"] = true
	services["elasticache"].(map[string]any)["enabled"] = false
	state := strings.Join([]string{
		"aws_db_instance.admin[0]",
		"aws_elasticache_replication_group.game[0]",
		"aws_mq_broker.rabbitmq[0]",
	}, "\n")
	plan := cloudServiceLifecyclePlan(doc, state)
	actions := make(map[string]cloudServiceLifecycleItem)
	for _, item := range plan {
		actions[item.Key] = item
	}
	if actions["rds"].Action != "更新/对账" || actions["aurora"].Action != "创建" || actions["elasticache"].Action != "删除" || actions["amazon_mq"].Action != "删除" {
		t.Fatalf("unexpected cloud lifecycle plan: %#v", actions)
	}
	if !strings.Contains(actions["aurora"].DataPolicy, "删除保护已开启") {
		t.Fatalf("Aurora deletion protection is missing from lifecycle plan: %#v", actions["aurora"])
	}
	if actions["ecr"].Action != "复用/对账" {
		t.Fatalf("ECR must stay shared and non-destructive: %#v", actions["ecr"])
	}
}

func TestAccessTargetsNeverReconcileComponentHelmReleases(t *testing.T) {
	access := phaseTwoAccessTargets()
	for _, required := range []string{
		"kubernetes_manifest.tls_certificate",
		"kubernetes_ingress_v1.domain",
		"kubernetes_service_v1.tcp_route",
		"kubernetes_config_map_v1.alerting",
		"kubernetes_secret_v1.alerting_channels",
	} {
		if !slices.Contains(access, required) {
			t.Fatalf("access update is missing target %q: %#v", required, access)
		}
	}
	for _, forbidden := range []string{"helm_release.catalog", "helm_release.loki_collector", "helm_release.consul", "helm_release.etcd"} {
		if slices.Contains(access, forbidden) {
			t.Fatalf("access update must not reconcile component target %q: %#v", forbidden, access)
		}
	}
	for _, existingEKS := range []bool{false, true} {
		steps := deploymentSteps(jobs.ActionAccess, existingEKS)
		if !slices.Contains(steps, stepApplyAccess) || slices.Contains(steps, stepApplyComponents) || slices.Contains(steps, stepReconcileReleases) {
			t.Fatalf("access-only steps for existingEKS=%v include component work: %#v", existingEKS, steps)
		}
	}
}

func TestLatestStableHelmRevisionSkipsPendingAndFailedRevisions(t *testing.T) {
	payload := []byte(`[
		{"revision":1,"status":"superseded"},
		{"revision":2,"status":"failed"},
		{"revision":3,"status":"pending-upgrade"}
	]`)
	revision, ok := latestStableHelmRevision(payload)
	if !ok || revision != 1 {
		t.Fatalf("revision=%d ok=%v, want revision 1", revision, ok)
	}
	if _, ok := latestStableHelmRevision([]byte(`[{"revision":1,"status":"pending-install"}]`)); ok {
		t.Fatal("a release without a stable revision must not be rolled back automatically")
	}
}

type interruptedLogStackExecutor struct {
	commands       []Command
	unsafePVC      bool
	pendingInstall bool
}

func (e *interruptedLogStackExecutor) Run(_ context.Context, command Command, output io.Writer) error {
	e.commands = append(e.commands, command)
	joined := strings.Join(command.Args, " ")
	switch {
	case joined == "state list":
		_, _ = io.WriteString(output, `helm_release.catalog["clickvisual_stack"]`)
	case strings.HasPrefix(joined, "list "):
		if e.pendingInstall {
			_, _ = io.WriteString(output, `[{"name":"clickvisual-stack","namespace":"demo-test-logs-system","status":"pending-install"}]`)
		} else {
			_, _ = io.WriteString(output, `[{"name":"clickvisual-stack","namespace":"demo-test-logs-system","status":"failed"}]`)
		}
	case joined == "history clickvisual-stack --namespace demo-test-logs-system --output json":
		if e.pendingInstall {
			_, _ = io.WriteString(output, `[{"revision":1,"status":"pending-install"}]`)
		} else {
			_, _ = io.WriteString(output, `[{"revision":1,"status":"failed"}]`)
		}
	case joined == "get persistentvolumeclaim --namespace demo-test-logs-system --selector ops-deploy.io/stack=clickvisual --output json":
		if e.unsafePVC {
			_, _ = io.WriteString(output, `{"items":[{"metadata":{"name":"clickvisual-clickhouse-data"}}]}`)
		} else {
			_, _ = io.WriteString(output, `{"items":[{"metadata":{"name":"clickvisual-clickhouse-data","annotations":{"helm.sh/resource-policy":"keep"}}}]}`)
		}
	}
	return nil
}

func TestTrackedFailedBundledLogStackWithoutStableRevisionIsUpgradedInPlace(t *testing.T) {
	doc := environment.DefaultDocument("demo", "test")
	environment.SetPath(doc, "components.catalog.clickvisual_stack.enabled", true)
	executor := &interruptedLogStackExecutor{}
	deployment := &Deployment{
		config: &appconfig.Config{
			Tools: appconfig.ToolsConfig{Terraform: "terraform", Helm: "helm", Kubectl: "kubectl"},
			Paths: appconfig.PathsConfig{RepositoryRoot: "/workspace", TerraformPlatformDir: "/workspace/terraform/platform"},
		},
		executor: executor,
	}
	var log bytes.Buffer
	if err := deployment.reconcileInterruptedDataServices(context.Background(), "demo-test", doc, "/tmp/kubeconfig", &log); err != nil {
		t.Fatal(err)
	}
	joined := ""
	for _, command := range executor.commands {
		joined += command.Name + " " + strings.Join(command.Args, " ") + "\n"
	}
	if strings.Contains(joined, "helm uninstall") || strings.Contains(joined, "get persistentvolumeclaim") {
		t.Fatalf("a failed release must be upgraded in place without destructive recovery:\n%s", joined)
	}
	if !strings.Contains(log.String(), "原地增量更新") || !strings.Contains(log.String(), "不卸载 Release") {
		t.Fatalf("operator log does not explain the recovery safety boundary:\n%s", log.String())
	}
}

type interruptedHigressExecutor struct{ commands []Command }

func (e *interruptedHigressExecutor) Run(_ context.Context, command Command, output io.Writer) error {
	e.commands = append(e.commands, command)
	joined := strings.Join(command.Args, " ")
	switch {
	case joined == "state list":
		_, _ = io.WriteString(output, `helm_release.catalog["higress"]`)
	case strings.HasPrefix(joined, "list "):
		_, _ = io.WriteString(output, `[{"name":"higress","namespace":"higress-system","status":"failed"}]`)
	case joined == "history higress --namespace higress-system --output json":
		_, _ = io.WriteString(output, `[{"revision":1,"status":"failed"}]`)
	}
	return nil
}

func TestTrackedFailedFirstInstallHigressIsUpgradedInPlace(t *testing.T) {
	doc := environment.DefaultDocument("demo", "test")
	environment.SetPath(doc, "components.catalog.higress.enabled", true)
	executor := &interruptedHigressExecutor{}
	deployment := &Deployment{
		config: &appconfig.Config{
			Tools: appconfig.ToolsConfig{Terraform: "terraform", Helm: "helm", Kubectl: "kubectl"},
			Paths: appconfig.PathsConfig{RepositoryRoot: "/workspace", TerraformPlatformDir: "/workspace/terraform/platform"},
		},
		executor: executor,
	}
	var log bytes.Buffer
	if err := deployment.reconcileInterruptedDataServices(context.Background(), "demo-test", doc, "/tmp/kubeconfig", &log); err != nil {
		t.Fatal(err)
	}
	for _, command := range executor.commands {
		joined := command.Name + " " + strings.Join(command.Args, " ")
		if strings.Contains(joined, "uninstall") || strings.Contains(joined, "persistentvolumeclaim") {
			t.Fatalf("failed first-install Higress must converge in place: %s", joined)
		}
	}
	if !strings.Contains(log.String(), "higress-system/higress 当前为 failed") || !strings.Contains(log.String(), "原地增量更新") {
		t.Fatalf("Higress recovery log is incomplete:\n%s", log.String())
	}
}

func TestTrackedFailedBundledLogStackWithoutLivePVCProtectionFailsClosed(t *testing.T) {
	doc := environment.DefaultDocument("demo", "test")
	environment.SetPath(doc, "components.catalog.clickvisual_stack.enabled", true)
	executor := &interruptedLogStackExecutor{unsafePVC: true, pendingInstall: true}
	deployment := &Deployment{
		config: &appconfig.Config{
			Tools: appconfig.ToolsConfig{Terraform: "terraform", Helm: "helm", Kubectl: "kubectl"},
			Paths: appconfig.PathsConfig{RepositoryRoot: "/workspace", TerraformPlatformDir: "/workspace/terraform/platform"},
		},
		executor: executor,
	}
	err := deployment.reconcileInterruptedDataServices(context.Background(), "demo-test", doc, "/tmp/kubeconfig", io.Discard)
	if err == nil || !strings.Contains(err.Error(), "does not have helm.sh/resource-policy=keep") {
		t.Fatalf("unsafe PVC must block automatic recovery, got %v", err)
	}
	for _, command := range executor.commands {
		if command.Name == "helm" && len(command.Args) > 0 && command.Args[0] == "uninstall" {
			t.Fatalf("unsafe recovery unexpectedly uninstalled the release: %#v", command)
		}
	}
}

type loggingStackExecutor struct{ commands []Command }

func (e *loggingStackExecutor) Run(_ context.Context, command Command, output io.Writer) error {
	e.commands = append(e.commands, command)
	if slices.Contains(command.Args, "--raw") {
		_, _ = io.WriteString(output, `{"status":"success","data":["test"]}`)
	}
	return nil
}

func TestVerifyLoggingStackChecksCollectorIngestionAndGrafanaProvisioning(t *testing.T) {
	doc := environment.DefaultDocument("demo", "test")
	catalog := doc["components"].(map[string]any)["catalog"].(map[string]any)
	catalog["loki"].(map[string]any)["enabled"] = true
	catalog["prometheus"].(map[string]any)["enabled"] = true
	executor := &loggingStackExecutor{}
	deployment := &Deployment{
		config: &appconfig.Config{
			Tools: appconfig.ToolsConfig{Kubectl: "kubectl"},
			Paths: appconfig.PathsConfig{RepositoryRoot: "/workspace"},
		},
		executor: executor,
	}
	var log bytes.Buffer
	if err := deployment.verifyLoggingStack(context.Background(), "/tmp/kubeconfig", doc, &log); err != nil {
		t.Fatal(err)
	}
	commands := make([]string, 0, len(executor.commands))
	for _, command := range executor.commands {
		commands = append(commands, strings.Join(command.Args, " "))
	}
	joined := strings.Join(commands, "\n")
	for _, expected := range []string{
		"rollout status deployment/loki-alloy",
		"/loki/api/v1/label/environment/values",
		"deployment/prometheus-grafana -c grafana",
		"loki-datasource.yaml",
	} {
		if !strings.Contains(joined, expected) {
			t.Fatalf("logging verification did not execute %q: %s", expected, joined)
		}
	}
	if !strings.Contains(log.String(), "Loki 已收到当前环境 test 的集群日志") {
		t.Fatalf("operator-facing ingestion result missing: %q", log.String())
	}
}

func TestVerifyGrafanaDashboardsChecksMonitoringAndLoggingViews(t *testing.T) {
	doc := environment.DefaultDocument("demo", "test")
	catalog := doc["components"].(map[string]any)["catalog"].(map[string]any)
	catalog["prometheus"].(map[string]any)["enabled"] = true
	catalog["loki"].(map[string]any)["enabled"] = true
	executor := &loggingStackExecutor{}
	deployment := &Deployment{
		config: &appconfig.Config{
			Tools: appconfig.ToolsConfig{Kubectl: "kubectl"},
			Paths: appconfig.PathsConfig{RepositoryRoot: "/workspace"},
		},
		executor: executor,
	}
	if err := deployment.verifyGrafanaDashboards(context.Background(), "/tmp/kubeconfig", doc, io.Discard); err != nil {
		t.Fatal(err)
	}
	if len(executor.commands) != 1 {
		t.Fatalf("expected one Grafana dashboard verification command, got %d", len(executor.commands))
	}
	joined := strings.Join(executor.commands[0].Args, " ")
	for _, expected := range []string{
		"deployment/prometheus-grafana -c grafana",
		"ops-deploy-eks-core.json",
		"ops-deploy-cluster-logs.json",
		"ops-eks-core",
		"ops-cluster-logs",
	} {
		if !strings.Contains(joined, expected) {
			t.Fatalf("Grafana dashboard verification did not include %q: %s", expected, joined)
		}
	}
}

func TestAlertingDiffLogNamesChangesWithoutLeakingWebhook(t *testing.T) {
	desired := namedAlertingConfigs([]any{
		map[string]any{"name": "lark", "type": "lark", "address": "https://open.larksuite.com/secret-token"},
		map[string]any{"name": "ops", "type": "slack", "address": "https://hooks.slack.com/new-secret"},
	}, true)
	current := namedAlertingConfigs([]any{
		map[string]any{"name": "ops", "type": "slack", "address": "https://hooks.slack.com/old-secret"},
		map[string]any{"name": "removed", "type": "webhook", "address": "https://example.com/removed-secret"},
	}, true)
	var output bytes.Buffer
	writeAlertingDiff(&output, "通道", desired, current)
	log := output.String()
	for _, required := range []string{"[新增通道] lark（lark）", "[更新通道] ops（slack）", "[删除通道] removed（webhook）"} {
		if !strings.Contains(log, required) {
			t.Fatalf("missing change summary %q in %q", required, log)
		}
	}
	for _, secret := range []string{"secret-token", "new-secret", "old-secret", "removed-secret"} {
		if strings.Contains(log, secret) {
			t.Fatalf("deployment log leaked webhook material %q: %q", secret, log)
		}
	}
}

type higressStatusExecutor struct{ patches []Command }

func (e *higressStatusExecutor) Run(_ context.Context, command Command, output io.Writer) error {
	joined := strings.Join(command.Args, " ")
	switch {
	case strings.Contains(joined, "get service higress-gateway"):
		_, _ = io.WriteString(output, `{"status":{"loadBalancer":{"ingress":[{"hostname":"gateway.elb.amazonaws.com"}]}}}`)
	case strings.Contains(joined, "get ingress -A"):
		_, _ = io.WriteString(output, `{"items":[{"metadata":{"name":"default","namespace":"higress-system"},"spec":{"ingressClassName":"higress"}},{"metadata":{"name":"other","namespace":"unrelated"},"spec":{"ingressClassName":"higress"}}]}`)
	case strings.Contains(joined, "patch ingress"):
		e.patches = append(e.patches, command)
		_, _ = io.WriteString(output, "patched\n")
	default:
		return fmt.Errorf("unexpected command: %#v", command.Args)
	}
	return nil
}

type staticHostResolver struct {
	addresses map[string][]string
}

func (r staticHostResolver) LookupHost(_ context.Context, host string) ([]string, error) {
	if addresses := r.addresses[host]; len(addresses) > 0 {
		return addresses, nil
	}
	return nil, errors.New("no such host")
}

type alertmanagerRelayExecutor struct {
	manifest []byte
}

func (e *alertmanagerRelayExecutor) Run(_ context.Context, command Command, output io.Writer) error {
	joined := strings.Join(command.Args, " ")
	switch {
	case strings.Contains(joined, "get crd alertmanagerconfigs.monitoring.coreos.com"):
		_, _ = io.WriteString(output, "customresourcedefinition.apiextensions.k8s.io/alertmanagerconfigs.monitoring.coreos.com\n")
		return nil
	case strings.Contains(joined, "apply -f -"):
		payload, err := io.ReadAll(command.Stdin)
		if err != nil {
			return err
		}
		e.manifest = payload
		_, _ = io.WriteString(output, "secret/platform-alert-relay configured\nalertmanagerconfig.monitoring.coreos.com/platform-alert-relay configured\n")
		return nil
	default:
		return fmt.Errorf("unexpected command: %#v", command.Args)
	}
}

func TestAlertmanagerRelayManifestUsesScopedSecretWithoutChannelURL(t *testing.T) {
	doc := environment.DefaultDocument("demo", "test")
	alerting := doc["alerting"].(map[string]any)
	alerting["enabled"] = true
	alerting["channels"] = []any{map[string]any{
		"name": "lark", "type": "lark", "address": "https://open.larksuite.com/open-apis/bot/v2/hook/sensitive-token",
	}}
	prometheus := doc["components"].(map[string]any)["catalog"].(map[string]any)["prometheus"].(map[string]any)
	prometheus["enabled"] = true
	prometheus["namespace"] = "monitoring"
	executor := &alertmanagerRelayExecutor{}
	deployment := &Deployment{
		config: &appconfig.Config{
			Security: appconfig.SecurityConfig{ExternalOrigin: "https://ops.example.com", CredentialKeyEnv: "TEST_ALERT_RELAY_KEY"},
			Tools:    appconfig.ToolsConfig{Kubectl: "kubectl"},
			Paths:    appconfig.PathsConfig{RepositoryRoot: t.TempDir()},
		},
		executor: executor,
	}
	t.Setenv("TEST_ALERT_RELAY_KEY", base64.StdEncoding.EncodeToString([]byte("01234567890123456789012345678901")))
	var output bytes.Buffer
	if err := deployment.applyAlertmanagerRelay(context.Background(), "/tmp/kubeconfig", "demo-test", doc, &output); err != nil {
		t.Fatal(err)
	}
	if len(executor.manifest) == 0 {
		t.Fatal("relay manifest was not applied")
	}
	manifest := string(executor.manifest)
	if !strings.Contains(manifest, "https://ops.example.com/api/internal/alerting/relay/demo-test") || !strings.Contains(manifest, "platform-alert-relay") {
		t.Fatalf("relay manifest is incomplete: %s", manifest)
	}
	if !strings.Contains(manifest, `"name":"alertname"`) || !strings.Contains(manifest, `"value":"^(Watchdog|AlertmanagerFailedToSendAlerts)$"`) || !strings.Contains(manifest, `"matchType":"!~"`) {
		t.Fatalf("internal alert-pipeline signals must be excluded from the business alert relay: %s", manifest)
	}
	if strings.Contains(manifest, "sensitive-token") || strings.Contains(output.String(), "sensitive-token") {
		t.Fatalf("channel webhook leaked into relay manifest or deployment log")
	}
	if !strings.Contains(output.String(), "Alertmanager 自动告警路由已生效") {
		t.Fatalf("operator-facing success message missing: %s", output.String())
	}
}

func TestHigressGatewayAddressIsCopiedFromServiceToIngressStatus(t *testing.T) {
	doc := environment.DefaultDocument("demo", "test")
	higress := doc["components"].(map[string]any)["catalog"].(map[string]any)["higress"].(map[string]any)
	higress["enabled"] = true
	executor := &higressStatusExecutor{}
	deployment := &Deployment{
		config: &appconfig.Config{
			Tools: appconfig.ToolsConfig{Kubectl: "kubectl"},
			Paths: appconfig.PathsConfig{RepositoryRoot: t.TempDir()},
		},
		executor: executor,
	}
	var log bytes.Buffer
	if err := deployment.syncHigressGatewayAddress(context.Background(), "/tmp/kubeconfig", doc, &log); err != nil {
		t.Fatal(err)
	}
	if len(executor.patches) != 1 {
		t.Fatalf("only the environment-owned Higress Ingress should be patched: %#v", executor.patches)
	}
	patchCommand := strings.Join(executor.patches[0].Args, " ")
	if !strings.Contains(patchCommand, "--subresource=status") || !strings.Contains(patchCommand, "gateway.elb.amazonaws.com") {
		t.Fatalf("Ingress status patch is incomplete: %s", patchCommand)
	}
	if !strings.Contains(log.String(), "AWS LoadBalancer 已就绪") {
		t.Fatalf("operator-facing gateway log is missing: %s", log.String())
	}
}

func TestHigressGatewayLogDistinguishesMissingPublicDNSFromRouteFailure(t *testing.T) {
	doc := environment.DefaultDocument("demo", "test")
	doc["domains"] = []any{
		map[string]any{"enabled": true, "gateway": "higress", "domain": "ready.example.com"},
		map[string]any{"enabled": true, "gateway": "higress", "domain": "missing.example.com"},
	}
	higress := doc["components"].(map[string]any)["catalog"].(map[string]any)["higress"].(map[string]any)
	higress["enabled"] = true
	executor := &higressStatusExecutor{}
	deployment := &Deployment{
		config: &appconfig.Config{
			Tools: appconfig.ToolsConfig{Kubectl: "kubectl"},
			Paths: appconfig.PathsConfig{RepositoryRoot: t.TempDir()},
		},
		executor:    executor,
		dnsResolver: staticHostResolver{addresses: map[string][]string{"ready.example.com": {"203.0.113.10"}}},
	}
	var log bytes.Buffer
	if err := deployment.syncHigressGatewayAddress(context.Background(), "/tmp/kubeconfig", doc, &log); err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{
		"[DNS 已解析] ready.example.com -> 203.0.113.10",
		"[DNS 未就绪] missing.example.com",
		"missing.example.com -> gateway.elb.amazonaws.com",
	} {
		if !strings.Contains(log.String(), expected) {
			t.Fatalf("gateway DNS status log is missing %q: %s", expected, log.String())
		}
	}
}

func TestConfiguredDomainBackendsAndReadyEndpointCounts(t *testing.T) {
	doc := environment.DefaultDocument("demo", "test")
	doc["domains"] = []any{
		map[string]any{
			"enabled": true, "domain": "api.example.com", "namespace": "apps",
			"routes": []any{
				map[string]any{"path": "/api", "service": "api", "service_port": 8080},
				map[string]any{"path": "/", "service": "web", "service_port": 80},
			},
		},
		map[string]any{"enabled": true, "domain": "api-alt.example.com", "namespace": "apps", "service": "api"},
		map[string]any{"enabled": false, "domain": "disabled.example.com", "namespace": "apps", "service": "disabled"},
	}
	backends := configuredDomainBackends(doc)
	if len(backends) != 2 ||
		backends[0].Host != "api.example.com/api" || backends[0].Namespace != "apps" || backends[0].Service != "api" ||
		backends[1].Host != "api.example.com" || backends[1].Namespace != "apps" || backends[1].Service != "web" {
		t.Fatalf("configured domain backends = %#v", backends)
	}
	payload := []byte(`{"items":[{"metadata":{"namespace":"apps","labels":{"kubernetes.io/service-name":"api"}},"endpoints":[{"addresses":["10.0.0.1"],"conditions":{"ready":true}},{"addresses":["10.0.0.2"],"conditions":{"ready":false}},{"addresses":["10.0.0.3"],"conditions":{"ready":true,"terminating":true}}]},{"metadata":{"namespace":"apps","labels":{"kubernetes.io/service-name":"web"}},"endpoints":[{"addresses":["10.0.0.4"],"conditions":{"ready":true}}]}]}`)
	counts, err := decodeReadyEndpointCounts(payload)
	if err != nil {
		t.Fatal(err)
	}
	if counts["apps/api"] != 1 || counts["apps/web"] != 1 {
		t.Fatalf("ready endpoint counts = %#v", counts)
	}
	if _, err := decodeReadyEndpointCounts([]byte(`{"items":`)); err == nil {
		t.Fatal("invalid EndpointSlice JSON was accepted")
	}
}

func TestTLSOnlyDeploymentStepsNeverInstallComponents(t *testing.T) {
	for _, existingEKS := range []bool{false, true} {
		steps := deploymentSteps(jobs.ActionTLS, existingEKS)
		for _, required := range []string{stepUpdateKubeconfig, stepApplyTLSCertificates} {
			if !slices.Contains(steps, required) {
				t.Fatalf("TLS-only steps for existingEKS=%v are missing %q: %#v", existingEKS, required, steps)
			}
		}
		for _, forbidden := range []string{
			stepInitializeInfra, stepPrepareInfra, stepApplyInfra,
			stepReconcileReleases, stepApplyBase, stepApplyComponents, stepVerifyPods,
		} {
			if slices.Contains(steps, forbidden) {
				t.Fatalf("TLS-only steps for existingEKS=%v contain unrelated step %q: %#v", existingEKS, forbidden, steps)
			}
		}
		if existingEKS != slices.Contains(steps, stepCheckExistingEKS) {
			t.Fatalf("TLS-only existing EKS preflight mismatch for existingEKS=%v: %#v", existingEKS, steps)
		}
		if !slices.Contains(steps, stepEnsureTLSNamespaces) {
			t.Fatalf("TLS-only steps must prepare the target namespace safely: %#v", steps)
		}
	}
}

type clickVisualStoragePreflightExecutor struct{}

func (clickVisualStoragePreflightExecutor) Run(_ context.Context, command Command, output io.Writer) error {
	joined := strings.Join(command.Args, " ")
	switch {
	case strings.Contains(joined, "get pods"):
		_, _ = io.WriteString(output, `{"items":[{"metadata":{"name":"clickvisual-kafka-0-0","namespace":"demo-test-logs-system"},"status":{"containerStatuses":[{"name":"kafka","ready":false,"state":{"waiting":{"reason":"CrashLoopBackOff"}}}]}}]}`)
	case strings.HasPrefix(joined, "logs "):
		_, _ = io.WriteString(output, "java.io.IOException: No space left on device\n")
	case strings.Contains(joined, "get persistentvolumeclaim"):
		_, _ = io.WriteString(output, `{"items":[{"metadata":{"name":"clickvisual-kafka-data-0"},"spec":{"resources":{"requests":{"storage":"50Gi"}}},"status":{"capacity":{"storage":"50Gi"}}}]}`)
	default:
		return fmt.Errorf("unexpected command: %s", joined)
	}
	return nil
}

type higressSchedulingPreflightExecutor struct {
	payload string
}

func (e higressSchedulingPreflightExecutor) Run(_ context.Context, command Command, output io.Writer) error {
	if strings.Join(command.Args, " ") != "get nodes -l workload-class=gateway -o json" {
		return fmt.Errorf("unexpected command: %#v", command.Args)
	}
	_, _ = io.WriteString(output, e.payload)
	return nil
}

func TestHigressSchedulingPreflightRejectsImpossibleCPURequest(t *testing.T) {
	doc := environment.DefaultDocument("demo", "uat")
	environment.SetPath(doc, "components.catalog.higress.enabled", true)
	environment.SetPath(doc, "eks.workload_scheduling.enabled", true)
	environment.SetPath(doc, "components.catalog.higress.values.higress-core.gateway.resources.requests.cpu", "2")
	deployment := &Deployment{
		config: &appconfig.Config{
			Paths: appconfig.PathsConfig{RepositoryRoot: "."},
			Tools: appconfig.ToolsConfig{Kubectl: "kubectl"},
		},
		executor: higressSchedulingPreflightExecutor{payload: `{"items":[{"metadata":{"name":"gateway-1"},"status":{"allocatable":{"cpu":"1930m"},"conditions":[{"type":"Ready","status":"True"}]}}]}`},
	}
	failure, err := deployment.higressGatewaySchedulingFailure(context.Background(), "/tmp/kubeconfig", doc)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(failure, "2000m") || !strings.Contains(failure, "1930m") || !strings.Contains(failure, "避免等待 20 分钟") {
		t.Fatalf("impossible gateway request was not explained directly: %q", failure)
	}
}

func TestHigressSchedulingPreflightAllowsSafeDefault(t *testing.T) {
	doc := environment.DefaultDocument("demo", "uat")
	environment.SetPath(doc, "components.catalog.higress.enabled", true)
	environment.SetPath(doc, "eks.workload_scheduling.enabled", true)
	deployment := &Deployment{
		config: &appconfig.Config{
			Paths: appconfig.PathsConfig{RepositoryRoot: "."},
			Tools: appconfig.ToolsConfig{Kubectl: "kubectl"},
		},
		executor: higressSchedulingPreflightExecutor{payload: `{"items":[{"metadata":{"name":"gateway-1"},"status":{"allocatable":{"cpu":"1930m"},"conditions":[{"type":"Ready","status":"True"}]}}]}`},
	}
	failure, err := deployment.higressGatewaySchedulingFailure(context.Background(), "/tmp/kubeconfig", doc)
	if err != nil || failure != "" {
		t.Fatalf("safe default must pass: failure=%q err=%v", failure, err)
	}
}

func TestClickVisualStoragePreflightFailsImmediatelyWhenKafkaVolumeIsFull(t *testing.T) {
	doc := environment.DefaultDocument("demo", "test")
	environment.SetPath(doc, "components.catalog.clickvisual_stack.enabled", true)
	deployment := &Deployment{
		config:   &appconfig.Config{Tools: appconfig.ToolsConfig{Kubectl: "kubectl"}},
		executor: clickVisualStoragePreflightExecutor{},
	}
	failure, err := deployment.clickVisualKafkaStorageFailure(context.Background(), "", doc, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(failure, "No space left on device") || !strings.Contains(failure, "clickvisual-kafka-data-0=50Gi") || !strings.Contains(failure, "先在“ClickVisual 磁盘与容量”扩容") {
		t.Fatalf("full Kafka volume was not explained directly: %q", failure)
	}
}

func TestClickVisualStoragePreflightAllowsSavedCapacityIncrease(t *testing.T) {
	doc := environment.DefaultDocument("demo", "test")
	environment.SetPath(doc, "components.catalog.clickvisual_stack.enabled", true)
	environment.SetPath(doc, "components.catalog.clickvisual_stack.values.kafka.storage.size", "100Gi")
	deployment := &Deployment{
		config:   &appconfig.Config{Tools: appconfig.ToolsConfig{Kubectl: "kubectl"}},
		executor: clickVisualStoragePreflightExecutor{},
	}
	var log bytes.Buffer
	failure, err := deployment.clickVisualKafkaStorageFailure(context.Background(), "", doc, &log)
	if err != nil {
		t.Fatal(err)
	}
	if failure != "" || !strings.Contains(log.String(), "目标容量 100Gi 大于当前 PVC 容量") {
		t.Fatalf("a pending capacity increase must be allowed: failure=%q log=%q", failure, log.String())
	}
}

type tlsNamespaceExecutor struct {
	existing map[string]bool
	seen     []string
}

func (e *tlsNamespaceExecutor) Run(_ context.Context, command Command, output io.Writer) error {
	if len(command.Args) < 3 || command.Args[0] != "get" || command.Args[1] != "namespace" {
		return fmt.Errorf("unexpected command: %#v", command.Args)
	}
	namespace := command.Args[2]
	e.seen = append(e.seen, namespace)
	if e.existing[namespace] {
		_, _ = fmt.Fprintf(output, "namespace/%s\n", namespace)
	}
	return nil
}

func TestMissingTLSNamespacesAreUniqueAndSorted(t *testing.T) {
	executor := &tlsNamespaceExecutor{existing: map[string]bool{"present": true}}
	deployment := &Deployment{
		config: &appconfig.Config{
			Tools: appconfig.ToolsConfig{Kubectl: "kubectl"},
			Paths: appconfig.PathsConfig{RepositoryRoot: t.TempDir()},
		},
		executor: executor,
	}
	missing, err := deployment.missingTLSNamespaces(context.Background(), "/tmp/kubeconfig", []uploadedTLSMaterial{
		{Namespace: "z-missing"}, {Namespace: "present"}, {Namespace: "a-missing"}, {Namespace: "z-missing"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(executor.seen, []string{"a-missing", "present", "z-missing"}) {
		t.Fatalf("namespace preflight was not stable and unique: %#v", executor.seen)
	}
	if !slices.Equal(missing, []string{"a-missing", "z-missing"}) {
		t.Fatalf("unexpected missing namespaces: %#v", missing)
	}
}

func TestTerraformNamespaceAddressCannotBroadenTarget(t *testing.T) {
	if got := terraformNamespaceAddress("test-hichat"); got != `kubernetes_namespace_v1.this["test-hichat"]` {
		t.Fatalf("unexpected namespace target: %q", got)
	}
}

type platformNamespaceReconcileExecutor struct {
	state      string
	namespaces map[string]map[string]string
	imports    []Command
}

func (e *platformNamespaceReconcileExecutor) Run(_ context.Context, command Command, output io.Writer) error {
	if command.Name == "terraform" && slices.Equal(command.Args, []string{"state", "list"}) {
		_, _ = io.WriteString(output, e.state)
		return nil
	}
	if command.Name == "kubectl" && len(command.Args) >= 3 && command.Args[0] == "get" && command.Args[1] == "namespace" {
		namespace := command.Args[2]
		labels, exists := e.namespaces[namespace]
		if !exists {
			return nil
		}
		payload, _ := json.Marshal(map[string]any{"metadata": map[string]any{"name": namespace, "labels": labels}})
		_, _ = output.Write(payload)
		return nil
	}
	if command.Name == "terraform" && len(command.Args) > 0 && command.Args[0] == "import" {
		e.imports = append(e.imports, command)
		_, _ = io.WriteString(output, "Import successful!\n")
		return nil
	}
	return fmt.Errorf("unexpected command: %s %#v", command.Name, command.Args)
}

func namespaceReconcileDeployment(t *testing.T, executor commandExecutor) *Deployment {
	t.Helper()
	root := t.TempDir()
	repository, err := environment.NewRepository(filepath.Join(root, "environments"))
	if err != nil {
		t.Fatal(err)
	}
	return &Deployment{
		config: &appconfig.Config{
			Tools: appconfig.ToolsConfig{Terraform: "terraform", Kubectl: "kubectl"},
			Paths: appconfig.PathsConfig{RepositoryRoot: root, TerraformPlatformDir: filepath.Join(root, "platform")},
		},
		environments: repository,
		executor:     executor,
	}
}

func TestDesiredPlatformNamespacesIncludesEnabledComponentTargets(t *testing.T) {
	doc := environment.DefaultDocument("demo", "test")
	doc["namespaces"] = map[string]any{"test-demo": map[string]any{}}
	components := doc["components"].(map[string]any)
	components["consul"] = map[string]any{"enabled": true, "namespace": "platform-server"}
	components["catalog"] = map[string]any{
		"prometheus": map[string]any{"enabled": true, "namespace": "monitoring"},
		"disabled":   map[string]any{"enabled": false, "namespace": "ignored"},
	}
	if got := desiredPlatformNamespaces(doc); !slices.Equal(got, []string{"monitoring", "platform-server", "test-demo"}) {
		t.Fatalf("unexpected desired namespaces: %#v", got)
	}
}

func TestReconcileExistingNamespacesImportsSafeUntrackedNamespace(t *testing.T) {
	doc := environment.DefaultDocument("demo", "test")
	doc["namespaces"] = map[string]any{"test-demo": map[string]any{}}
	doc["components"].(map[string]any)["catalog"] = map[string]any{
		"prometheus": map[string]any{"enabled": true, "namespace": "monitoring"},
	}
	executor := &platformNamespaceReconcileExecutor{
		state: `kubernetes_namespace_v1.this["test-demo"]` + "\n",
		namespaces: map[string]map[string]string{
			"monitoring": {},
			"test-demo":  {"app.kubernetes.io/part-of": "demo", "environment": "test"},
		},
	}
	deployment := namespaceReconcileDeployment(t, executor)
	var log bytes.Buffer
	if err := deployment.reconcileExistingNamespaces(context.Background(), "demo-test", doc, "/tmp/kubeconfig", &log); err != nil {
		t.Fatal(err)
	}
	if len(executor.imports) != 1 {
		t.Fatalf("expected one safe import, got %#v", executor.imports)
	}
	args := executor.imports[0].Args
	if !slices.Contains(args, terraformNamespaceAddress("monitoring")) || args[len(args)-1] != "monitoring" {
		t.Fatalf("unexpected import args: %#v", args)
	}
	if !strings.Contains(log.String(), "已安全复用并纳入当前环境 State") || !strings.Contains(log.String(), "不会删除任何 Namespace") {
		t.Fatalf("missing clear adoption log: %s", log.String())
	}
}

func TestReconcileExistingNamespacesRejectsForeignOwnership(t *testing.T) {
	doc := environment.DefaultDocument("demo", "test")
	doc["namespaces"] = map[string]any{"monitoring": map[string]any{}}
	executor := &platformNamespaceReconcileExecutor{namespaces: map[string]map[string]string{
		"monitoring": {"app.kubernetes.io/part-of": "another-project", "environment": "prod"},
	}}
	deployment := namespaceReconcileDeployment(t, executor)
	err := deployment.reconcileExistingNamespaces(context.Background(), "demo-test", doc, "/tmp/kubeconfig", io.Discard)
	if err == nil || !strings.Contains(err.Error(), "归属于其他项目或环境") {
		t.Fatalf("expected ownership conflict, got %v", err)
	}
	if len(executor.imports) != 0 {
		t.Fatalf("foreign Namespace must never be imported: %#v", executor.imports)
	}
}

func TestExistingEKSComponentDestroyNeverTargetsNamespaces(t *testing.T) {
	if slices.Contains(existingEKSComponentDestroyTargets(), "kubernetes_namespace_v1.this") {
		t.Fatal("existing EKS component uninstall must never target Namespace deletion")
	}
	if !slices.Contains(protectedExistingEKSStatePrefixes(), "kubernetes_namespace_v1.this") {
		t.Fatal("existing EKS cleanup must detach Namespace state while preserving the real Namespace")
	}
}

func TestTerraformNamespaceHasPermanentDestroyProtection(t *testing.T) {
	payload, err := os.ReadFile("../../terraform/platform/namespaces.tf")
	if err != nil {
		t.Fatal(err)
	}
	source := string(payload)
	if !strings.Contains(source, "prevent_destroy = true") {
		t.Fatal("Terraform Namespace resources must reject accidental destroy plans")
	}
}

func TestEtcdServerSelectorsExcludeWebUI(t *testing.T) {
	for _, path := range []string{
		"../../terraform/platform/charts/etcd/templates/statefulset.yaml",
		"../../terraform/platform/charts/etcd/templates/service.yaml",
		"../../terraform/platform/charts/etcd/templates/pdb.yaml",
	} {
		payload, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(payload), "app.kubernetes.io/component: server") {
			t.Fatalf("etcd server selector in %s can match the web UI pod", path)
		}
	}
}

func TestExistingEKSStorageClasses(t *testing.T) {
	doc := environment.DefaultDocument("demo", "test")
	if err := environment.ConfigureTarget(doc, environment.TargetExistingEKS); err != nil {
		t.Fatal(err)
	}
	target := doc["deployment_target"].(map[string]any)
	target["cluster_name"] = "shared-cluster"
	consul := doc["components"].(map[string]any)["consul"].(map[string]any)
	consul["enabled"] = true
	consul["storage_class"] = "gp3"
	classes := existingEKSStorageClasses(doc)
	if !slices.Contains(classes, "gp3") {
		t.Fatalf("stateful preflight did not require configured storage class: %#v", classes)
	}
}

func TestTLSSecretManifestUsesKubernetesTLSData(t *testing.T) {
	certificate := []byte("-----BEGIN CERTIFICATE-----\ntest\n-----END CERTIFICATE-----\n")
	privateKey := []byte("-----BEGIN PRIVATE KEY-----\ntest\n-----END PRIVATE KEY-----\n")
	payload, err := tlsSecretManifest(uploadedTLSMaterial{
		Project: "demo", Environment: "test",
		Key: "web-tls", Namespace: "platform-server", SecretName: "web-tls",
		Certificate: certificate, PrivateKey: privateKey,
	})
	if err != nil {
		t.Fatal(err)
	}
	var manifest struct {
		Kind     string            `json:"kind"`
		Type     string            `json:"type"`
		Data     map[string]string `json:"data"`
		Metadata struct {
			Name      string            `json:"name"`
			Namespace string            `json:"namespace"`
			Labels    map[string]string `json:"labels"`
		} `json:"metadata"`
	}
	if err := json.Unmarshal(payload, &manifest); err != nil {
		t.Fatal(err)
	}
	if manifest.Kind != "Secret" || manifest.Type != "kubernetes.io/tls" || manifest.Metadata.Name != "web-tls" || manifest.Metadata.Namespace != "platform-server" {
		t.Fatalf("unexpected TLS Secret manifest: %#v", manifest)
	}
	decodedCertificate, err := base64.StdEncoding.DecodeString(manifest.Data["tls.crt"])
	if err != nil || string(decodedCertificate) != string(certificate) {
		t.Fatalf("tls.crt was not encoded correctly: %v", err)
	}
	decodedPrivateKey, err := base64.StdEncoding.DecodeString(manifest.Data["tls.key"])
	if err != nil || string(decodedPrivateKey) != string(privateKey) {
		t.Fatalf("tls.key was not encoded correctly: %v", err)
	}
	if manifest.Metadata.Labels["app.kubernetes.io/managed-by"] != "ops-deploy-platform" {
		t.Fatalf("managed-by label missing: %#v", manifest.Metadata.Labels)
	}
	if manifest.Metadata.Labels["ops-deploy.io/project"] != "demo" || manifest.Metadata.Labels["ops-deploy.io/environment"] != "test" {
		t.Fatalf("scope labels missing: %#v", manifest.Metadata.Labels)
	}
	if manifest.Metadata.Labels["ops-deploy.io/certificate-key"] != "web-tls" {
		t.Fatalf("certificate inventory label missing: %#v", manifest.Metadata.Labels)
	}
	selector := uploadedTLSSecretSelector("demo", "test")
	if !strings.Contains(selector, "ops-deploy.io/certificate-key") {
		t.Fatalf("TLS prune selector could match unrelated platform Secrets: %q", selector)
	}
}

func TestConsulClientCANamespacesExcludesServerAndDisabledNamespaces(t *testing.T) {
	doc := environment.DefaultDocument("demo", "test")
	consul := doc["components"].(map[string]any)["consul"].(map[string]any)
	consul["enabled"] = true
	consul["namespace"] = "platform-server"
	doc["namespaces"] = map[string]any{
		"platform-server": map[string]any{"enabled": true},
		"game-test":       map[string]any{"enabled": true},
		"monitoring":      map[string]any{"enabled": true},
		"old":             map[string]any{"enabled": false},
	}
	targets, source := consulClientCANamespaces(doc)
	if source != "platform-server" || !slices.Equal(targets, []string{"game-test", "monitoring"}) {
		t.Fatalf("consul client CA targets=%v source=%q", targets, source)
	}
	consul["enabled"] = false
	if targets, _ := consulClientCANamespaces(doc); len(targets) != 0 {
		t.Fatalf("disabled Consul unexpectedly produced CA targets: %v", targets)
	}
}

func TestUploadedTLSMaterialIsMirroredIntoEveryIngressNamespace(t *testing.T) {
	doc := environment.DefaultDocument("demo", "test")
	doc["domains"] = []any{
		map[string]any{"enabled": true, "access_type": "domain", "tls_enabled": true, "certificate_ref": "web-tls", "namespace": "monitoring"},
		map[string]any{"enabled": true, "access_type": "domain", "tls_enabled": true, "certificate_ref": "web-tls", "namespace": "monitoring"},
		map[string]any{"enabled": false, "access_type": "domain", "tls_enabled": true, "certificate_ref": "web-tls", "namespace": "disabled"},
		map[string]any{"enabled": true, "access_type": "ip", "tls_enabled": false, "certificate_ref": "web-tls", "namespace": "ip-only"},
		map[string]any{"enabled": true, "access_type": "domain", "tls_enabled": true, "certificate_ref": "other-tls", "namespace": "other"},
	}
	certificate := []byte("certificate")
	privateKey := []byte("private-key")
	result := expandUploadedTLSMaterials(doc, []uploadedTLSMaterial{{
		Project: "demo", Environment: "test", Key: "web-tls", Namespace: "platform-server", SecretName: "web-tls",
		Certificate: certificate, PrivateKey: privateKey,
	}})
	if len(result) != 2 || result[0].Namespace != "monitoring" || result[1].Namespace != "platform-server" {
		t.Fatalf("TLS material was not mirrored into the HTTPS Ingress Namespace: %#v", result)
	}
	for _, material := range result {
		if material.SecretName != "web-tls" || string(material.Certificate) != "certificate" || string(material.PrivateKey) != "private-key" {
			t.Fatalf("mirrored TLS material changed unexpectedly: %#v", material)
		}
	}
}

type elastiCacheScaleDownExecutor struct {
	commands []Command
}

func (e *elastiCacheScaleDownExecutor) Run(_ context.Context, command Command, output io.Writer) error {
	e.commands = append(e.commands, command)
	if slices.Contains(command.Args, "describe-replication-groups") {
		_, _ = io.WriteString(output, `{"ReplicationGroups":[{"Status":"available","MultiAZ":"enabled","AutomaticFailover":"enabled","NodeGroups":[{"NodeGroupMembers":[{},{}]}]}]}`)
	}
	return nil
}

func TestPrepareElastiCacheReplicaScaleDownDisablesHABeforeReplicaRemoval(t *testing.T) {
	doc := environment.DefaultDocument("demo", "test")
	cache := doc["data_services"].(map[string]any)["elasticache"].(map[string]any)
	cache["enabled"] = true
	cache["mode"] = "cluster"
	cache["nodes_per_shard"] = 1
	cache["replicas_per_node_group"] = 0
	executor := &elastiCacheScaleDownExecutor{}
	deployment := &Deployment{
		config:   &appconfig.Config{Paths: appconfig.PathsConfig{RepositoryRoot: "."}, Tools: appconfig.ToolsConfig{AWS: "aws"}},
		executor: executor,
	}
	var output bytes.Buffer
	if err := deployment.prepareElastiCacheReplicaScaleDown(context.Background(), doc, &output); err != nil {
		t.Fatal(err)
	}
	if len(executor.commands) != 3 {
		t.Fatalf("expected describe, modify and wait commands, got %#v", executor.commands)
	}
	modify := executor.commands[1].Args
	if !slices.Contains(modify, "--no-multi-az-enabled") || !slices.Contains(modify, "--no-automatic-failover-enabled") {
		t.Fatalf("HA disable flags missing before replica decrease: %#v", modify)
	}
	wait := executor.commands[2].Args
	if !slices.Contains(wait, "replication-group-available") {
		t.Fatalf("replication group availability wait missing: %#v", wait)
	}
	if !strings.Contains(output.String(), "继续执行 Terraform 副本缩容") {
		t.Fatalf("operator-facing staged resize log is missing: %s", output.String())
	}
}

func TestElastiCacheReplicaTargetUsesTotalNodesPerShard(t *testing.T) {
	doc := environment.DefaultDocument("demo", "test")
	cache := doc["data_services"].(map[string]any)["elasticache"].(map[string]any)
	cache["enabled"] = true
	cache["mode"] = "cluster"
	cache["nodes_per_shard"] = 4
	cache["replicas_per_node_group"] = 0
	if target := elastiCacheReplicaTarget(doc); target != 3 {
		t.Fatalf("replica target = %d, want 3 derived from four total nodes per shard", target)
	}
}

package jobs

import (
	"context"
	"errors"
	"io"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

type memoryJobStore struct {
	mu       sync.Mutex
	jobs     map[string]Job
	cached   map[string]Job
	cacheOps int
}

func (s *memoryJobStore) LoadJobs(context.Context) ([]Job, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	result := make([]Job, 0, len(s.jobs))
	for _, job := range s.jobs {
		result = append(result, job)
	}
	return result, nil
}

func (s *memoryJobStore) SaveJob(_ context.Context, job *Job) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.jobs[job.ID] = *job
	return nil
}

func (s *memoryJobStore) DeleteJob(_ context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.jobs, id)
	return nil
}

func (s *memoryJobStore) CacheJob(_ context.Context, job *Job) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cached[job.ID] = *job
	s.cacheOps++
	return nil
}

func (s *memoryJobStore) DeleteCachedJob(_ context.Context, id, _, _ string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.cached, id)
	return nil
}

type testRunner struct {
	block bool
	err   error
}

func (r testRunner) Run(ctx context.Context, environment string, action Action, _ string, output io.Writer) error {
	_, _ = io.WriteString(output, "running "+string(action)+" for "+environment+"\n")
	if r.block {
		<-ctx.Done()
		return ctx.Err()
	}
	return r.err
}

func TestSubmitWithParametersKeepsImmutableCopy(t *testing.T) {
	manager, err := NewManager(t.TempDir(), 1, 10, time.Minute, testRunner{block: true})
	if err != nil {
		t.Fatal(err)
	}
	input := map[string]string{
		"component":      "clickhouse",
		"target_size_gi": "200",
	}
	job, err := manager.SubmitWithParameters("ops", "test", "ops-test", "admin", ActionStorageExpand, input)
	if err != nil {
		t.Fatal(err)
	}
	input["target_size_gi"] = "1"
	job.Parameters["component"] = "mysql"

	stored, found := manager.Get(job.ID)
	if !found {
		t.Fatal("submitted task not found")
	}
	if stored.Parameters["component"] != "clickhouse" || stored.Parameters["target_size_gi"] != "200" {
		t.Fatalf("task parameters were mutated through caller-owned data: %#v", stored.Parameters)
	}
	if err := manager.Cancel(job.ID); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := manager.Wait(ctx, job.ID); err != nil {
		t.Fatal(err)
	}
}

func TestSnapshotPreservesHistoryLimitPerEnvironment(t *testing.T) {
	base := time.Now().UTC()
	manager := &Manager{
		historyLimit: 1,
		jobs: map[string]*Job{
			"alpha-old": {ID: "alpha-old", Project: "alpha", Environment: "test", CreatedAt: base.Add(-time.Hour)},
			"alpha-new": {ID: "alpha-new", Project: "alpha", Environment: "test", CreatedAt: base},
			"beta-new":  {ID: "beta-new", Project: "beta", Environment: "prod", CreatedAt: base.Add(-time.Minute)},
		},
	}
	snapshot := manager.Snapshot()
	if len(snapshot) != 2 {
		t.Fatalf("snapshot length = %d, want one item per environment", len(snapshot))
	}
	if snapshot[0].ID != "alpha-new" || snapshot[1].ID != "beta-new" {
		t.Fatalf("snapshot did not preserve newest per-scope tasks: %#v", snapshot)
	}
}

func TestManagerAddsFailureHintAndDeletesTerminalHistory(t *testing.T) {
	store := &memoryJobStore{jobs: make(map[string]Job), cached: make(map[string]Job)}
	manager, err := NewManagerWithStores(t.TempDir(), 1, 10, time.Minute, testRunner{err: errors.New("AccessDenied: eks DescribeCluster")}, store, store)
	if err != nil {
		t.Fatal(err)
	}
	job, err := manager.Submit("ops", "test", "ops-test", "admin", ActionDeploy)
	if err != nil {
		t.Fatal(err)
	}
	completed, err := manager.Wait(context.Background(), job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if completed.Status != StatusFailed || completed.Diagnosis == nil || completed.Diagnosis.Code != "aws_permission_denied" || !strings.Contains(completed.FailureHint, "IAM") || !Retryable(completed.Status) {
		t.Fatalf("failure was not diagnosed as retryable: %#v", completed)
	}
	logData, _, _, err := manager.ReadLog(job.ID, 0, 256*1024)
	if err != nil || !strings.Contains(string(logData), "部署失败诊断") || !strings.Contains(string(logData), "处理建议") {
		t.Fatalf("structured diagnosis was not appended to log: err=%v log=%q", err, logData)
	}
	removed, err := manager.DeleteHistory("ops", "test")
	if err != nil || removed != 1 {
		t.Fatalf("history cleanup failed: removed=%d err=%v", removed, err)
	}
	if _, found := manager.Get(job.ID); found {
		t.Fatal("terminal job remained after cleanup")
	}
}

func TestManagerRunsPersistedCompletionActionAfterSuccessfulDestroy(t *testing.T) {
	store := &memoryJobStore{jobs: make(map[string]Job), cached: make(map[string]Job)}
	manager, err := NewManagerWithStores(t.TempDir(), 1, 10, time.Minute, testRunner{}, store, store)
	if err != nil {
		t.Fatal(err)
	}
	var completed Job
	manager.SetCompletionHandler(func(_ context.Context, job Job) error {
		completed = job
		return nil
	})
	job, err := manager.SubmitWithCompletion("ops", "test", "ops-test", "admin", ActionDestroy, CompletionDeleteEnvironment)
	if err != nil {
		t.Fatal(err)
	}
	result, err := manager.Wait(context.Background(), job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != StatusSucceeded || completed.ID != job.ID || result.CompletionAction != CompletionDeleteEnvironment {
		t.Fatalf("completion action was not retained and executed: result=%#v completed=%#v", result, completed)
	}
	store.mu.Lock()
	persisted := store.jobs[job.ID]
	store.mu.Unlock()
	if persisted.CompletionAction != CompletionDeleteEnvironment {
		t.Fatalf("completion action was not persisted: %#v", persisted)
	}
	logData, _, _, err := manager.ReadLog(job.ID, 0, 256*1024)
	if err != nil || !strings.Contains(string(logData), "环境配置已自动删除") {
		t.Fatalf("completion result was not written to the task log: err=%v log=%q", err, logData)
	}
}

func TestManagerKeepsEnvironmentWhenCompletionActionFails(t *testing.T) {
	manager, err := NewManager(t.TempDir(), 1, 10, time.Minute, testRunner{})
	if err != nil {
		t.Fatal(err)
	}
	manager.SetCompletionHandler(func(context.Context, Job) error { return errors.New("Terraform 仍跟踪 2 项资源") })
	job, err := manager.SubmitWithCompletion("ops", "test", "ops-test", "admin", ActionDestroy, CompletionDeleteEnvironment)
	if err != nil {
		t.Fatal(err)
	}
	result, err := manager.Wait(context.Background(), job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != StatusFailed || result.Diagnosis == nil || result.Diagnosis.Code != "environment_cleanup_after_destroy_failed" {
		t.Fatalf("failed completion was not surfaced safely: %#v", result)
	}
	if !strings.Contains(result.Error, "删除环境配置失败") || !Retryable(result.Status) {
		t.Fatalf("failed completion lost retry guidance: %#v", result)
	}
}

func TestDeleteEnvironmentCompletionOnlyAcceptsDestroy(t *testing.T) {
	manager, err := NewManager(t.TempDir(), 1, 10, time.Minute, testRunner{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.SubmitWithCompletion("ops", "test", "ops-test", "admin", ActionDeploy, CompletionDeleteEnvironment); !errors.Is(err, ErrInvalidCompletionAction) {
		t.Fatalf("non-destroy job accepted environment deletion completion: %v", err)
	}
}

func TestFailureDiagnosisExplainsHelmStateConflict(t *testing.T) {
	job := &Job{
		Action: ActionDeploy,
		Steps:  []Step{{Name: "Apply phase 1 base services", Status: StepFailed}},
	}
	diagnosis := failureDiagnosis(job, `
Error: cannot re-use a name that is still in use
  with helm_release.consul[0]
Error: cannot re-use a name that is still in use
  with helm_release.etcd[0]
`)
	if diagnosis.Code != "helm_release_state_conflict" || !strings.Contains(diagnosis.Cause, "consul、etcd") {
		t.Fatalf("Helm conflict was not explained precisely: %#v", diagnosis)
	}
	if !strings.Contains(diagnosis.Suggestion, "导入 Terraform State") || !strings.Contains(diagnosis.Retry, "不要直接重试") {
		t.Fatalf("Helm conflict did not include safe recovery guidance: %#v", diagnosis)
	}
}

func TestFailureDiagnosisExplainsPlatformHelmCompatibility(t *testing.T) {
	job := &Job{Action: ActionPlatform, Steps: []Step{{Name: "对账并修复上次中断的组件", Status: StepFailed}}}
	diagnosis := failureDiagnosis(job, "cannot inspect Helm releases before component retry: Error: unknown flag: --all")
	if diagnosis.Code != "platform_tool_compatibility" || !strings.Contains(diagnosis.Impact, "均未") || !strings.Contains(diagnosis.Retry, "直接重试") {
		t.Fatalf("unexpected diagnosis: %#v", diagnosis)
	}
}

func TestFailureDiagnosisExplainsUnwritableJobLogStorage(t *testing.T) {
	job := &Job{Action: ActionPlatform}
	diagnosis := failureDiagnosis(job, "mkdir /app/data/jobs/kbp: permission denied")
	if diagnosis.Code != "job_log_storage_unwritable" || !strings.Contains(diagnosis.Impact, "不会创建、修改或删除") {
		t.Fatalf("unexpected task log storage diagnosis: %#v", diagnosis)
	}
	if !strings.Contains(diagnosis.Suggestion, "10001:10001") || !strings.Contains(diagnosis.Retry, "不需要清理") {
		t.Fatalf("task log storage recovery guidance is incomplete: %#v", diagnosis)
	}
}

func TestFailureDiagnosisExplainsAccessPhaseContractMismatch(t *testing.T) {
	job := &Job{Action: ActionAccess, Steps: []Step{{Name: "阶段2 · 应用域名、TLS 与告警接入配置", Status: StepFailed}}}
	diagnosis := failureDiagnosis(job, `Error: Invalid value for variable
deployment_phase must be base or components.`)
	if diagnosis.Code != "platform_access_phase_contract_mismatch" || !strings.Contains(diagnosis.Impact, "不会创建、修改或删除") {
		t.Fatalf("unexpected access phase diagnosis: %#v", diagnosis)
	}
	if !strings.Contains(diagnosis.Retry, "仅应用接入配置") || !strings.Contains(diagnosis.Retry, "不需要清理") {
		t.Fatalf("access phase recovery guidance is incomplete: %#v", diagnosis)
	}
}

func TestManagerChecksKnownJobLogDirectories(t *testing.T) {
	manager, err := NewManager(t.TempDir(), 1, 10, time.Minute, testRunner{})
	if err != nil {
		t.Fatal(err)
	}
	job, err := manager.Submit("ops", "test", "ops-test", "admin", ActionValidate)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := manager.Wait(ctx, job.ID); err != nil {
		t.Fatal(err)
	}
	if err := manager.CheckStorage(); err != nil {
		t.Fatalf("healthy task log storage was rejected: %v", err)
	}
	info, err := os.Stat(manager.jobDir(job))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o700 {
		t.Fatalf("task log directory mode = %o, want 700", info.Mode().Perm())
	}
}

func TestFailureDiagnosisExplainsReadOnlyTerraformLockFile(t *testing.T) {
	job := &Job{Action: ActionPlatform, Steps: []Step{{Name: "初始化 EKS 平台组件 Terraform", Status: StepFailed}}}
	diagnosis := failureDiagnosis(job, "Error: Failed to update dependency lock file: open ./.terraform.lock.hcl123: read-only file system")
	if diagnosis.Code != "terraform_lockfile_readonly" || !strings.Contains(diagnosis.Impact, "尚未执行") || !strings.Contains(diagnosis.Retry, "不需要删除") {
		t.Fatalf("unexpected lockfile diagnosis: %#v", diagnosis)
	}
}

func TestFailureDiagnosisExplainsPermanentNamespaceProtection(t *testing.T) {
	job := &Job{Action: ActionPlatform, Steps: []Step{{Name: "Apply phase 2 components", Status: StepFailed}}}
	diagnosis := failureDiagnosis(job, `Error: Instance cannot be destroyed
Resource kubernetes_namespace_v1.this["platform-server"] has lifecycle.prevent_destroy set`)
	if diagnosis.Code != "namespace_deletion_protected" {
		t.Fatalf("namespace deletion protection was not diagnosed precisely: %#v", diagnosis)
	}
	if !strings.Contains(diagnosis.Impact, "均未") || !strings.Contains(diagnosis.Suggestion, "只关闭") || !strings.Contains(diagnosis.Retry, "不要移除 prevent_destroy") {
		t.Fatalf("namespace protection guidance is incomplete: %#v", diagnosis)
	}
}

func TestFailureDiagnosisExplainsExistingNamespaceOutsideState(t *testing.T) {
	job := &Job{Action: ActionPlatform, Steps: []Step{{Name: "阶段2 · 安装组件并应用接入配置", Status: StepFailed}}}
	diagnosis := failureDiagnosis(job, `Error: namespaces "monitoring" already exists`)
	if diagnosis.Code != "namespace_exists_outside_state" {
		t.Fatalf("existing Namespace was not diagnosed precisely: %#v", diagnosis)
	}
	if !strings.Contains(diagnosis.Cause, "Terraform State") || !strings.Contains(diagnosis.Suggestion, "自动复用") || !strings.Contains(diagnosis.Retry, "无需手工删除") {
		t.Fatalf("existing Namespace recovery guidance is incomplete: %#v", diagnosis)
	}
}

func TestFailureDiagnosisExplainsImmutableStatefulSetUpgrade(t *testing.T) {
	job := &Job{Action: ActionPlatform, Steps: []Step{{Name: "阶段2 · 安装组件并应用接入配置", Status: StepFailed}}}
	diagnosis := failureDiagnosis(job, `Error: cannot patch "redis" with kind StatefulSet: StatefulSet.apps "redis" is invalid: spec: Forbidden: updates to statefulset spec for fields other than replicas are forbidden`)
	if diagnosis.Code != "statefulset_immutable_upgrade" || !strings.Contains(diagnosis.Impact, "PVC 未被删除") || !strings.Contains(diagnosis.Retry, "原地重试") {
		t.Fatalf("unexpected diagnosis: %#v", diagnosis)
	}
}

func TestFailureDiagnosisExplainsUnsupportedAmazonMQInstance(t *testing.T) {
	job := &Job{Action: ActionDeploy, Steps: []Step{{Name: "Apply infra Terraform", Status: StepFailed}}}
	diagnosis := failureDiagnosis(job, `Error: creating MQ Broker (demo): BadRequestException: Broker engine type [RabbitMQ] does not support host instance type [mq.t3.micro].`)
	if diagnosis.Code != "amazon_mq_instance_unsupported" || !strings.Contains(diagnosis.Cause, "mq.t3.micro") {
		t.Fatalf("Amazon MQ failure was not diagnosed precisely: %#v", diagnosis)
	}
	if !strings.Contains(diagnosis.Impact, "不需要手工删除") || !strings.Contains(diagnosis.Retry, "原地重试") {
		t.Fatalf("Amazon MQ recovery guidance is incomplete: %#v", diagnosis)
	}
}

func TestFailureDiagnosisExplainsElastiCacheParameterGroupMismatch(t *testing.T) {
	job := &Job{Action: ActionDeploy, Steps: []Step{{Name: "Apply infra Terraform", Status: StepFailed}}}
	diagnosis := failureDiagnosis(job, `Error: creating ElastiCache Replication Group (kbp-test-game): InvalidParameterCombination: valkey parameter group is not applicable to engine redis`)
	if diagnosis.Code != "elasticache_parameter_group_mismatch" || !strings.Contains(diagnosis.Cause, "Redis OSS/Valkey") {
		t.Fatalf("ElastiCache mismatch was not diagnosed precisely: %#v", diagnosis)
	}
	if !strings.Contains(diagnosis.Impact, "不需要删除或重建") || !strings.Contains(diagnosis.Retry, "原地重试") {
		t.Fatalf("ElastiCache recovery guidance is incomplete: %#v", diagnosis)
	}
}

func TestFailureDiagnosisExplainsCloudDeletionProtectionTwoStepFlow(t *testing.T) {
	job := &Job{Action: ActionDeploy, Steps: []Step{{Name: "Apply infra Terraform", Status: StepFailed}}}
	diagnosis := failureDiagnosis(job, `Error: deleting RDS Cluster (demo): InvalidParameterCombination: Cannot delete protected Cluster, please disable deletion protection`)
	if diagnosis.Code != "cloud_resource_deletion_protected" {
		t.Fatalf("cloud deletion protection was not diagnosed precisely: %#v", diagnosis)
	}
	if !strings.Contains(diagnosis.Suggestion, "先重新开启") || !strings.Contains(diagnosis.Suggestion, "第二次更新部署") || !strings.Contains(diagnosis.Retry, "不要直接重复") {
		t.Fatalf("cloud deletion protection recovery flow is incomplete: %#v", diagnosis)
	}
}

func TestFailureDiagnosisExplainsFinalSnapshotNameConflict(t *testing.T) {
	job := &Job{Action: ActionDeploy, Steps: []Step{{Name: "Apply infra Terraform", Status: StepFailed}}}
	diagnosis := failureDiagnosis(job, `Error: deleting DB Instance: DB Snapshot already exists: demo-final snapshot`)
	if diagnosis.Code != "cloud_final_snapshot_name_conflict" || !strings.Contains(diagnosis.Impact, "均未删除") {
		t.Fatalf("final snapshot conflict was not diagnosed precisely: %#v", diagnosis)
	}
	if !strings.Contains(diagnosis.Suggestion, "最终快照名称") || !strings.Contains(diagnosis.Retry, "不要删除 Terraform State") {
		t.Fatalf("final snapshot recovery guidance is incomplete: %#v", diagnosis)
	}
}

func TestFailureDiagnosisExplainsMissingCentralState(t *testing.T) {
	job := &Job{Action: ActionDeploy}
	diagnosis := failureDiagnosis(job, "统一 Terraform 状态中心不可用: 统一 Terraform 状态中心尚未配置")
	if diagnosis.Code != "terraform_state_center_not_configured" || !strings.Contains(diagnosis.Suggestion, "Terraform 状态中心") {
		t.Fatalf("unexpected central state diagnosis: %#v", diagnosis)
	}
}

func TestFailureDiagnosisExplainsTLSNamespacePreparation(t *testing.T) {
	job := &Job{Action: ActionTLS, Steps: []Step{{Name: "确保 TLS 目标 Namespace 可用", Status: StepFailed}}}
	diagnosis := failureDiagnosis(job, "自动创建 TLS 目标 Namespace 失败: forbidden")
	if diagnosis.Code != "tls_namespace_prepare_failed" || !strings.Contains(diagnosis.Impact, "不会留下未纳入 State") {
		t.Fatalf("unexpected TLS namespace diagnosis: %#v", diagnosis)
	}
}

func TestFailureDiagnosisExplainsMissingLokiStorage(t *testing.T) {
	job := &Job{Action: ActionPlatform, Steps: []Step{{Name: "Apply phase 2 components and access configuration", Status: StepFailed}}}
	diagnosis := failureDiagnosis(job, `Error: template: loki/templates/config.yaml: error calling include: executing "loki.commonStorageConfig" at <.Values.loki.storage.bucketNames.chunks>: nil pointer evaluating interface {}.chunks; http_server_read_timeout: 600s`)
	if diagnosis.Code != "loki_storage_config_missing" || !strings.Contains(diagnosis.Suggestion, "SingleBinary") {
		t.Fatalf("Loki failure was not diagnosed precisely: %#v", diagnosis)
	}
	if diagnosis.Code == "operation_timeout" {
		t.Fatal("a timeout value inside Helm YAML was mistaken for an operation timeout")
	}
}

func TestFailureDiagnosisExplainsSelfManagedDatabaseSecretOutput(t *testing.T) {
	job := &Job{Action: ActionDeploy, Steps: []Step{{Name: "生成 AWS 基础资源执行计划", Status: StepFailed}}}
	message := `Earlier check: Helm charts passed.
Error: Invalid index
  on outputs.tf line 78, in output "aurora_master_secret_arn":
  aws_rds_cluster.game[0].master_user_secret is empty list of object
terraform exited with an error`
	diagnosis := failureDiagnosis(job, message)
	if diagnosis.Code != "database_secret_output_mode_mismatch" {
		t.Fatalf("database output mismatch was misclassified: %#v", diagnosis)
	}
	if !strings.Contains(diagnosis.Impact, "尚未执行 Apply") || !strings.Contains(diagnosis.Retry, "不需要修改或删除数据库") {
		t.Fatalf("database output recovery guidance is incomplete: %#v", diagnosis)
	}
}

func TestFailureDiagnosisExplainsDatabaseCredentialArgumentConflict(t *testing.T) {
	job := &Job{Action: ActionDeploy, Steps: []Step{{Name: "生成 AWS 基础资源执行计划", Status: StepFailed}}}
	message := `Error: Conflicting configuration arguments
"manage_master_user_password": conflicts with master_password`
	diagnosis := failureDiagnosis(job, message)
	if diagnosis.Code != "database_credential_arguments_conflict" {
		t.Fatalf("database credential conflict was misclassified: %#v", diagnosis)
	}
	if !strings.Contains(diagnosis.Impact, "尚未执行 Apply") || !strings.Contains(diagnosis.Retry, "不需要删除 State") {
		t.Fatalf("database credential conflict guidance is incomplete: %#v", diagnosis)
	}
}

func TestFailureDiagnosisExplainsHigressLoadBalancerTimeout(t *testing.T) {
	job := &Job{Action: ActionPlatform, Steps: []Step{{Name: "阶段2 · 安装组件并应用接入配置", Status: StepFailed}}}
	diagnosis := failureDiagnosis(job, `Error: context deadline exceeded
  with helm_release.catalog["higress"],
  on catalog-components.tf line 1, in resource "helm_release" "catalog"`)
	if diagnosis.Code != "higress_load_balancer_timeout" || !strings.Contains(diagnosis.Cause, "AWS Load Balancer Controller") {
		t.Fatalf("Higress timeout was not diagnosed precisely: %#v", diagnosis)
	}
	if !strings.Contains(diagnosis.Suggestion, "NLB instance") || !strings.Contains(diagnosis.Retry, "原地重试") {
		t.Fatalf("Higress recovery guidance is incomplete: %#v", diagnosis)
	}
}

func TestFailureDiagnosisExplainsHigressGatewayCPUCapacity(t *testing.T) {
	job := Job{Action: ActionPlatform}
	diagnosis := failureDiagnosis(&job, `Error: context deadline exceeded
with helm_release.catalog["higress"]
pod/higress-gateway-abc: 0/1 nodes are available: 1 Insufficient cpu`)
	if diagnosis.Code != "higress_gateway_cpu_unschedulable" || !strings.Contains(diagnosis.Cause, "Allocatable") {
		t.Fatalf("Higress CPU scheduling failure was not diagnosed precisely: %#v", diagnosis)
	}
	if !strings.Contains(diagnosis.Suggestion, "部署前直接比较") || !strings.Contains(diagnosis.Retry, "无需删除") {
		t.Fatalf("Higress CPU recovery guidance is incomplete: %#v", diagnosis)
	}
}

func TestFailureDiagnosisExplainsHigressNLBSecurityPreflight(t *testing.T) {
	job := &Job{Action: ActionPlatform, Steps: []Step{{Name: "部署前检查组件健康与存储", Status: StepFailed}}}
	diagnosis := failureDiagnosis(job, `[HIGRESS_NLB_PREFLIGHT] Higress NLB 安全组 sg-1234 属于 vpc-old，但 EKS 位于 vpc-current；AWS 不允许跨 VPC 绑定安全组`)
	if diagnosis.Code != "higress_nlb_preflight_failed" || !strings.Contains(diagnosis.Cause, "VPC 归属") {
		t.Fatalf("Higress NLB preflight failure was not diagnosed precisely: %#v", diagnosis)
	}
	if !strings.Contains(diagnosis.Impact, "变更前停止") || !strings.Contains(diagnosis.Retry, "直接重试阶段2") {
		t.Fatalf("Higress NLB preflight recovery guidance is incomplete: %#v", diagnosis)
	}
}

func TestFailureDiagnosisExplainsClusterAutoscalerDynamicResourceRBAC(t *testing.T) {
	job := &Job{Action: ActionPlatform, Steps: []Step{{Name: "阶段2 · 安装组件并应用接入配置", Status: StepFailed}}}
	diagnosis := failureDiagnosis(job, `Error: context deadline exceeded
autoscalerStatus: Initializing
resourceclaims.resource.k8s.io is forbidden
0/3 nodes are available: 1 Insufficient cpu, 1 node(s) had untolerated taint(s)`)
	if diagnosis.Code != "cluster_autoscaler_dra_rbac_missing" {
		t.Fatalf("autoscaler DRA RBAC failure was not diagnosed precisely: %#v", diagnosis)
	}
	if !strings.Contains(diagnosis.Cause, "业务节点隔离正常") || !strings.Contains(diagnosis.Retry, "无需删除") {
		t.Fatalf("autoscaler DRA RBAC guidance is incomplete: %#v", diagnosis)
	}
}

func TestFailureDiagnosisExplainsPrometheusPreInstallHookTimeout(t *testing.T) {
	job := &Job{Action: ActionPlatform, Steps: []Step{{Name: "阶段2 · 安装组件并应用接入配置", Status: StepFailed}}}
	diagnosis := failureDiagnosis(job, `Error: failed pre-install: 1 error occurred:
	* timed out waiting for the condition

  with helm_release.catalog["prometheus"]`)
	if diagnosis.Code != "prometheus_preinstall_hook_unschedulable" || !strings.Contains(diagnosis.Cause, "admission webhook") {
		t.Fatalf("Prometheus pre-install timeout was not diagnosed precisely: %#v", diagnosis)
	}
	if !strings.Contains(diagnosis.Suggestion, "workload-class=platform") || !strings.Contains(diagnosis.Retry, "直接重试阶段2") {
		t.Fatalf("Prometheus Hook recovery guidance is incomplete: %#v", diagnosis)
	}
}

func TestFailureDiagnosisExplainsNewEKSRemainingQuotaShortage(t *testing.T) {
	job := &Job{Action: ActionDeploy, Steps: []Step{{Name: "校验 EKS 创建前 AWS 可用配额", Status: StepFailed}}}
	diagnosis := failureDiagnosis(job, `[AWS_CREATE_QUOTA_INSUFFICIENT] 新建 EKS 的区域剩余 EIP不足：总配额 8、当前已分配 4、实际剩余 4、最低需要 5`)
	if diagnosis.Code != "aws_create_quota_insufficient" || !strings.Contains(diagnosis.Cause, "总配额减去当前已使用量") {
		t.Fatalf("remaining AWS quota shortage was not diagnosed clearly: %#v", diagnosis)
	}
	if !strings.Contains(diagnosis.Impact, "Terraform") || !strings.Contains(diagnosis.Retry, "提额审批") {
		t.Fatalf("remaining AWS quota diagnosis lacks a safe retry contract: %#v", diagnosis)
	}
}

func TestFailureDiagnosisExplainsClickVisualKafkaFullDiskBeforeGenericTimeout(t *testing.T) {
	job := &Job{Action: ActionPlatform, Steps: []Step{{Name: "阶段2 · 安装组件并应用接入配置", Status: StepFailed}}}
	diagnosis := failureDiagnosis(job, `Error: context deadline exceeded
ClickVisual Kafka 容器 logs-system/clickvisual-kafka-0-0 因数据盘写满无法启动（No space left on device）`)
	if diagnosis.Code != "clickvisual_kafka_storage_full" || !strings.Contains(diagnosis.Suggestion, "ClickVisual 磁盘与容量") {
		t.Fatalf("Kafka full disk must take precedence over the generic timeout: %#v", diagnosis)
	}
	if !strings.Contains(diagnosis.Retry, "不需要删除 Namespace") {
		t.Fatalf("Kafka recovery guidance is incomplete: %#v", diagnosis)
	}
}

func TestFailureDiagnosisExplainsStatefulPlatformPVCTopologyConflict(t *testing.T) {
	job := &Job{Action: ActionDeploy, Steps: []Step{{Name: "阶段1 · 安装 EKS 基础组件与基础服务", Status: StepFailed}}}
	diagnosis := failureDiagnosis(job, `terraform exited with an error: signal: broken pipe
helm_release.etcd[0]: Still modifying... [id=etcd, 10m00s elapsed]
helm_release.consul[0]: Still modifying... [id=consul, 19m50s elapsed]`)
	if diagnosis.Code != "stateful_platform_pvc_topology_conflict" {
		t.Fatalf("stateful PVC topology conflict was not diagnosed precisely: %#v", diagnosis)
	}
	if !strings.Contains(diagnosis.Cause, "EBS PVC") || !strings.Contains(diagnosis.Suggestion, "workload-class=platform") {
		t.Fatalf("stateful PVC topology guidance is incomplete: %#v", diagnosis)
	}
	if !strings.Contains(diagnosis.Retry, "不需要重建") {
		t.Fatalf("stateful PVC topology retry guidance is incomplete: %#v", diagnosis)
	}
}

func TestFailureDiagnosisPrioritizesMissingNamespaceOverHigressTimeout(t *testing.T) {
	job := &Job{Action: ActionPlatform, Steps: []Step{{Name: "阶段2 · 安装组件并应用接入配置", Status: StepFailed}}}
	diagnosis := failureDiagnosis(job, `Error: context deadline exceeded
  with helm_release.catalog["efk_stack"]
Error: create: failed to create: namespaces "platform-server" not found
  with helm_release.catalog["jenkins"]
Error: create: failed to create: namespaces "platform-server" not found
  with helm_release.catalog["higress"]`)
	if diagnosis.Code != "component_namespace_missing" {
		t.Fatalf("missing Namespace was obscured by a timeout: %#v", diagnosis)
	}
	if !strings.Contains(diagnosis.Cause, "不是 Higress") || !strings.Contains(diagnosis.Retry, "直接重试阶段2") {
		t.Fatalf("missing Namespace recovery guidance is incomplete: %#v", diagnosis)
	}
}

func TestFailureDiagnosisPrioritizesHelmOwnershipConflictOverTimeout(t *testing.T) {
	job := &Job{Action: ActionPlatform, Steps: []Step{{Name: "阶段2 · 安装组件并应用接入配置", Status: StepFailed}}}
	diagnosis := failureDiagnosis(job, `Error: Unable to continue with install: ClusterRole "loki-clusterrole" exists and cannot be imported into the current release: invalid ownership metadata; annotation validation error: key "meta.helm.sh/release-namespace" must equal "hichat-test-monitoring": current value is "monitoring"
Error: context deadline exceeded
  with helm_release.catalog["prometheus"]`)
	if diagnosis.Code != "helm_cluster_resource_ownership_conflict" {
		t.Fatalf("ownership conflict was obscured by the later timeout: %#v", diagnosis)
	}
	if !strings.Contains(diagnosis.Impact, "不会删除旧监控栈") || !strings.Contains(diagnosis.Retry, "直接重试阶段2") {
		t.Fatalf("ownership conflict recovery guidance is incomplete: %#v", diagnosis)
	}
}

func TestTerraformStepIsNotMisclassifiedByEarlierHelmLog(t *testing.T) {
	job := &Job{Action: ActionDeploy, Steps: []Step{{Name: "校验 AWS 基础资源 Terraform", Status: StepFailed}}}
	diagnosis := failureDiagnosis(job, "Helm chart lint passed earlier; Terraform validate failed")
	if diagnosis.Code != "terraform_operation_failed" {
		t.Fatalf("Terraform stage was misclassified because an earlier Helm log remained in context: %#v", diagnosis)
	}
}

func TestFailureHintIdentifiesDestroyNetworkDependencies(t *testing.T) {
	job := &Job{
		Action: ActionDestroy,
		Steps:  []Step{{Name: "Destroy infra Terraform", Status: StepFailed}},
	}
	hint := failureHint(job, "terraform exited with an error: exit status 1")
	if !strings.Contains(hint, "子网") || !strings.Contains(hint, "ENI") || !strings.Contains(hint, "重试") {
		t.Fatalf("destroy hint did not explain the likely network dependency: %q", hint)
	}
}

func TestSuccessfulJobNeverRetainsFailedStep(t *testing.T) {
	now := time.Now().UTC()
	job := &Job{
		Status: StatusSucceeded,
		Steps: []Step{
			{Name: "safe no-op", Status: StepFailed, Error: "resource already absent"},
			{Name: "destroy remaining resources", Status: StepSucceeded},
		},
	}
	normalizeSucceededSteps(job, now)
	if job.FailedSteps != 0 || job.SuccessSteps != 2 || job.Progress != 100 {
		t.Fatalf("successful job retained a failed result: %#v", job)
	}
}

func TestManagerPersistsLogsAndCompletes(t *testing.T) {
	manager, err := NewManager(t.TempDir(), 1, 10, time.Minute, testRunner{})
	if err != nil {
		t.Fatal(err)
	}
	job, err := manager.Submit("ops", "test", "test", "admin", ActionValidate)
	if err != nil {
		t.Fatal(err)
	}
	result, err := manager.Wait(context.Background(), job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != StatusSucceeded {
		t.Fatalf("unexpected status: %#v", result)
	}
	data, _, complete, err := manager.ReadLog(job.ID, 0, 256*1024)
	if err != nil || !complete || !strings.Contains(string(data), "running validate for test") {
		t.Fatalf("unexpected log result: complete=%v err=%v data=%q", complete, err, data)
	}
}

func TestManagerCanAcknowledgeFailureWithoutMarkingItSuccessful(t *testing.T) {
	manager, err := NewManager(t.TempDir(), 1, 10, time.Minute, testRunner{err: errors.New("optional verification failed")})
	if err != nil {
		t.Fatal(err)
	}
	job, err := manager.Submit("ops", "test", "ops-test", "admin", ActionPlatform)
	if err != nil {
		t.Fatal(err)
	}
	failed, err := manager.Wait(context.Background(), job.ID)
	if err != nil || failed.Status != StatusFailed {
		t.Fatalf("job did not fail as expected: job=%#v err=%v", failed, err)
	}
	ignored, err := manager.Ignore(job.ID, "operator", "已人工核对非关键检查")
	if err != nil {
		t.Fatal(err)
	}
	if ignored.Status != StatusIgnored || ignored.IgnoreReason == "" || ignored.IgnoredAt == nil || ignored.Error == "" || !Retryable(ignored.Status) {
		t.Fatalf("ignored failure lost audit or retry information: %#v", ignored)
	}
	logData, _, complete, err := manager.ReadLog(job.ID, 0, 256*1024)
	if err != nil || !complete || !strings.Contains(string(logData), "failure acknowledged") {
		t.Fatalf("ignore acknowledgement was not audited: complete=%t err=%v log=%q", complete, err, logData)
	}
}

func TestDestroyFailureCannotBeIgnored(t *testing.T) {
	manager, err := NewManager(t.TempDir(), 1, 10, time.Minute, testRunner{err: errors.New("remaining resources")})
	if err != nil {
		t.Fatal(err)
	}
	job, err := manager.Submit("ops", "test", "ops-test", "admin", ActionDestroy)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Wait(context.Background(), job.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Ignore(job.ID, "operator", "暂时不处理"); !errors.Is(err, ErrDestroyNotIgnorable) {
		t.Fatalf("destroy failure was allowed to be ignored: %v", err)
	}
}

func TestIgnoreReasonLimitCountsCharacters(t *testing.T) {
	manager, err := NewManager(t.TempDir(), 1, 10, time.Minute, testRunner{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Ignore("missing", "operator", strings.Repeat("中", 500)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("500 Unicode characters should pass reason validation: %v", err)
	}
	if _, err := manager.Ignore("missing", "operator", strings.Repeat("中", 501)); !errors.Is(err, ErrInvalidIgnoreReason) {
		t.Fatalf("501 Unicode characters should be rejected: %v", err)
	}
}

func TestManagerAcceptsTLSOnlyAction(t *testing.T) {
	manager, err := NewManager(t.TempDir(), 1, 10, time.Minute, testRunner{})
	if err != nil {
		t.Fatal(err)
	}
	job, err := manager.Submit("ops", "test", "test", "admin", ActionTLS)
	if err != nil {
		t.Fatal(err)
	}
	result, err := manager.Wait(context.Background(), job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != StatusSucceeded || result.Action != ActionTLS {
		t.Fatalf("unexpected TLS-only job result: %#v", result)
	}
}

func TestFailureDiagnosisExplainsSharedECRRepositoryReuse(t *testing.T) {
	job := Job{Action: ActionDeploy}
	diagnosis := failureDiagnosis(&job, `Error: creating ECR Repository (kbp/gateway): RepositoryAlreadyExistsException: repository already exists`)
	if diagnosis.Code != "ecr_shared_repository_state_conflict" {
		t.Fatalf("unexpected diagnosis: %#v", diagnosis)
	}
	for _, expected := range []string{"项目级共享", "不需要手工删除", "prod-<版本>"} {
		combined := diagnosis.Suggestion + diagnosis.Retry
		if !strings.Contains(combined, expected) {
			t.Fatalf("ECR recovery guidance missing %q: %#v", expected, diagnosis)
		}
	}
}

func TestFailureDiagnosisExplainsElastiCacheServerlessDefaultUser(t *testing.T) {
	job := Job{Action: ActionDeploy}
	diagnosis := failureDiagnosis(&job, `Error: creating ElastiCache User Group: DefaultUserRequired: Redis user group needs to contain a user with the user name default.`)
	if diagnosis.Code != "elasticache_serverless_default_user_missing" {
		t.Fatalf("unexpected diagnosis: %#v", diagnosis)
	}
	if !strings.Contains(diagnosis.Suggestion, "nopass") || !strings.Contains(diagnosis.Retry, "无需删除") {
		t.Fatalf("ElastiCache recovery guidance is incomplete: %#v", diagnosis)
	}
}

func TestFailureDiagnosisExplainsUnreachableEKSEndpoint(t *testing.T) {
	job := Job{Action: ActionDeploy}
	diagnosis := failureDiagnosis(&job, `Error: kubernetes cluster unreachable: Get "https://example.eks.amazonaws.com/version": dial tcp 10.82.48.251:443: i/o timeout`)
	if diagnosis.Code != "eks_api_endpoint_unreachable" {
		t.Fatalf("unexpected diagnosis: %#v", diagnosis)
	}
	if !strings.Contains(diagnosis.Suggestion, "/32") || !strings.Contains(diagnosis.Suggestion, "0.0.0.0/0") || !strings.Contains(diagnosis.Retry, "0 删除") {
		t.Fatalf("EKS endpoint recovery guidance is incomplete: %#v", diagnosis)
	}
}

func TestManagerSerializesJobsPerEnvironment(t *testing.T) {
	manager, err := NewManager(t.TempDir(), 2, 10, time.Minute, testRunner{block: true})
	if err != nil {
		t.Fatal(err)
	}
	first, err := manager.Submit("ops", "test", "test", "admin", ActionDeploy)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Submit("ops", "test", "test", "admin", ActionPlan); !errors.Is(err, ErrEnvironmentBusy) {
		t.Fatalf("expected ErrEnvironmentBusy, got %v", err)
	}
	if err := manager.Cancel(first.ID); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if _, err := manager.Wait(ctx, first.ID); err != nil {
		t.Fatal(err)
	}
}

func TestManagerPersistsAndRestoresThroughStores(t *testing.T) {
	store := &memoryJobStore{jobs: make(map[string]Job), cached: make(map[string]Job)}
	manager, err := NewManagerWithStores(t.TempDir(), 1, 10, time.Minute, testRunner{}, store, store)
	if err != nil {
		t.Fatal(err)
	}
	job, err := manager.Submit("ops", "test", "test", "admin", ActionValidate)
	if err != nil {
		t.Fatal(err)
	}
	_, err = manager.Wait(context.Background(), job.ID)
	if err != nil {
		t.Fatal(err)
	}
	store.mu.Lock()
	persisted := store.jobs[job.ID]
	cached := store.cached[job.ID]
	cacheOps := store.cacheOps
	store.mu.Unlock()
	if persisted.Status != StatusSucceeded || cached.Status != StatusSucceeded || cacheOps < 2 {
		t.Fatalf("unexpected persisted state: mysql=%s redis=%s cacheOps=%d", persisted.Status, cached.Status, cacheOps)
	}

	restored, err := NewManagerWithStores(t.TempDir(), 1, 10, time.Minute, testRunner{}, store, store)
	if err != nil {
		t.Fatal(err)
	}
	loaded, ok := restored.Get(job.ID)
	if !ok || loaded.Status != StatusSucceeded {
		t.Fatalf("persisted job was not restored: %#v, found=%v", loaded, ok)
	}
}

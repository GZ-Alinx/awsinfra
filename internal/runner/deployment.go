package runner

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"ops-deploy-platform/internal/alertingrelay"
	"ops-deploy-platform/internal/appconfig"
	"ops-deploy-platform/internal/dataservicecredentials"
	"ops-deploy-platform/internal/environment"
	"ops-deploy-platform/internal/jobs"
	"ops-deploy-platform/internal/statebackend"
	"ops-deploy-platform/internal/tlscertificates"
)

type Deployment struct {
	config                        *appconfig.Config
	environments                  *environment.Repository
	executor                      commandExecutor
	awsProvider                   AWSCredentialProvider
	stateProvider                 StateBackendProvider
	tlsProvider                   TLSCertificateProvider
	dataServiceCredentialProvider DataServiceCredentialProvider
	dnsResolver                   hostResolver
}

type hostResolver interface {
	LookupHost(context.Context, string) ([]string, error)
}

type commandExecutor interface {
	Run(context.Context, Command, io.Writer) error
}

type AWSCredentialProvider interface {
	Environment(context.Context, string) ([]string, error)
}

type StateBackendProvider interface {
	Runtime(context.Context) (statebackend.Runtime, error)
}

type TLSCertificateProvider interface {
	Material(context.Context, string, string, string) (tlscertificates.Material, error)
}

type DataServiceCredentialProvider interface {
	Materials(context.Context, string, string) (map[string]dataservicecredentials.Material, error)
}

type awsEnvironmentContextKey struct{}
type stateProjectContextKey struct{}
type stateBackendContextKey struct{}
type stateRuntimeDirectoryContextKey struct{}
type dataServicePasswordsContextKey struct{}
type jobParametersContextKey struct{}
type eksPublicAccessCIDRsContextKey struct{}

const (
	stalePlatformTerraformLockAge = 15 * time.Minute

	stepInitializeInfra          = "初始化 AWS 基础资源 Terraform"
	stepInitializePlatform       = "初始化 EKS 平台组件 Terraform"
	stepPrepareInfra             = "选择 AWS 基础资源状态空间"
	stepPreparePlatform          = "选择 EKS 平台组件状态空间"
	stepCheckFormatting          = "检查 Terraform 格式"
	stepValidateInfra            = "校验 AWS 基础资源 Terraform"
	stepValidatePlatform         = "校验 EKS 平台组件 Terraform"
	stepLintEtcd                 = "检查 etcd Helm Chart"
	stepLintXXLJob               = "检查 XXL-JOB Helm Chart"
	stepLintNacos                = "检查 Nacos Helm Chart"
	stepPlanInfra                = "生成 AWS 基础资源执行计划"
	stepReconcileEKSNodeGroups   = "对账并复用已有 EKS 节点组"
	stepCheckEKSNodeGroupQuota   = "校验 EKS 创建前 AWS 可用配额"
	stepEnsureSharedECR          = "检查并复用项目共享 ECR 仓库"
	stepPrepareElastiCache       = "准备 ElastiCache 单节点缩容"
	stepPlanBase                 = "生成 EKS 基础服务执行计划"
	stepApplyInfra               = "创建或更新 AWS 基础资源"
	stepUpdateKubeconfig         = "更新当前环境 EKS 访问配置"
	stepApplyBase                = "阶段1 · 安装 EKS 基础组件与基础服务"
	stepVerifyNodes              = "验证 EKS 节点是否可用"
	stepReconcileBaseReleases    = "对账并修复中断的 EKS 基础服务"
	stepPreflightComponentHealth = "部署前检查组件健康与存储"
	stepCheckPlatformStateLock   = "检查 EKS 平台组件 Terraform 状态锁"
	stepReconcileNamespaces      = "对账并复用已有 Namespace"
	stepReconcileReleases        = "对账并修复上次中断的组件"
	stepApplyComponents          = "阶段2 · 安装组件并应用接入配置"
	stepReconcileCollectorWAL    = "对账 OpenTelemetry 持久化存储"
	stepApplyAccess              = "阶段2 · 应用域名、TLS 与告警接入配置"
	stepSyncConsulClientCA       = "同步 Consul 客户端 CA"
	stepConfigureAlerting        = "配置 Alertmanager 告警路由"
	stepSyncGatewayAddress       = "同步网关负载均衡地址"
	stepEnsureTLSNamespaces      = "确保 TLS 目标 Namespace 可用"
	stepApplyTLSCertificates     = "创建或更新 Kubernetes TLS Secret"
	stepVerifyDomainBackends     = "验证域名转发后端健康"
	stepVerifyPods               = "验证平台组件 Pod 是否健康"
	stepVerifyLogCollector       = "验证集群日志采集器"
	stepVerifyLokiIngestion      = "验证 Loki 日志写入与查询"
	stepVerifyGrafanaLoki        = "验证 Grafana Loki 数据源"
	stepVerifyGrafanaDashboards  = "验证 Grafana 默认中文 Dashboard"
	stepCheckExistingEKS         = "检查已有 EKS 接入条件"
	stepDeleteNamespaces         = "删除应用 Namespace 并等待网络资源释放"
	stepDestroyPlatform          = "销毁 EKS 平台组件资源"
	stepDestroyEKSCompute        = "先销毁 EKS 集群与节点组"
	stepDetachExistingEKS        = "保留并解除共享 EKS 基础资源状态"
	stepDestroyInfra             = "销毁 AWS 基础资源"
	stepRetryDestroyInfra        = "重试销毁剩余 AWS 基础资源"
	stepCleanupOrphanedNetwork   = "清理 EKS 遗留网卡和安全组"
	stepInspectManagedStorage    = "检查受管组件存储和工作负载"
	stepExpandManagedStorage     = "在线扩容受管组件 PVC"
	stepStopStorageWorkload      = "停止目标子组件并确认数据静止"
	stepMigrateManagedStorage    = "迁移数据到新的小容量 PVC"
	stepSwitchManagedStorage     = "切换工作负载到新 PVC"
	stepVerifyManagedStorage     = "恢复服务并验证存储切换"
)

const (
	// These are account-and-region headroom requirements, not total quota
	// requirements. They protect a new managed EKS environment from consuming
	// the last capacity needed by projects that are already running.
	minimumNewEKSAvailableVCPUs = 96
	minimumNewEKSAvailableEIPs  = 5
)

func NewDeployment(config *appconfig.Config, environments *environment.Repository) *Deployment {
	return &Deployment{config: config, environments: environments, executor: Executor{}}
}

func (d *Deployment) SetAWSCredentialProvider(provider AWSCredentialProvider) {
	d.awsProvider = provider
}

func (d *Deployment) SetStateBackendProvider(provider StateBackendProvider) {
	d.stateProvider = provider
}

func (d *Deployment) SetTLSCertificateProvider(provider TLSCertificateProvider) {
	d.tlsProvider = provider
}

func (d *Deployment) SetDataServiceCredentialProvider(provider DataServiceCredentialProvider) {
	d.dataServiceCredentialProvider = provider
}

// RunJob receives operation parameters from the task manager. Ordinary
// deployment jobs continue through Run unchanged; scoped storage jobs read
// only the allowlisted parameters placed in this private context value.
func (d *Deployment) RunJob(ctx context.Context, job jobs.Job, output io.Writer) error {
	ctx = context.WithValue(ctx, jobParametersContextKey{}, cloneJobParameters(job.Parameters))
	return d.Run(ctx, job.TargetName, job.Action, job.ID, output)
}

func (d *Deployment) Run(ctx context.Context, environmentName string, action jobs.Action, jobID string, output io.Writer) error {
	if !environment.ValidName(environmentName) {
		return environment.ErrInvalidName
	}
	doc, err := d.environments.Load(environmentName)
	if err != nil {
		return err
	}
	doc = environment.ApplyDefaults(doc, documentString(doc, "project"), documentString(doc, "environment"))
	if err := d.environments.Save(environmentName, doc); err != nil {
		return fmt.Errorf("persist environment defaults: %w", err)
	}
	if err := environment.Validate(doc); err != nil {
		return fmt.Errorf("environment configuration is invalid: %w", err)
	}
	if !environment.IsExistingEKS(doc) && actionUsesInfrastructure(action) {
		passwords, credentialErr := d.loadDataServicePasswords(ctx, doc)
		if credentialErr != nil {
			return credentialErr
		}
		defer clearPasswordMap(passwords)
		ctx = context.WithValue(ctx, dataServicePasswordsContextKey{}, passwords)
	}
	if d.awsProvider == nil {
		return errors.New("当前项目未绑定 AWS 凭据，平台已拒绝执行；请先在 AWS 凭据池中为该项目配置并选择权限入口")
	}
	awsEnvironment, credentialErr := d.awsProvider.Environment(ctx, documentString(doc, "project"))
	if credentialErr != nil {
		return fmt.Errorf("当前项目未绑定可用的 AWS 凭据，平台不会回退使用其他项目或默认凭据链: %w", credentialErr)
	}
	if len(awsEnvironment) == 0 {
		return errors.New("当前项目未绑定可用的 AWS 凭据，平台不会回退使用其他项目或默认凭据链")
	}
	ctx = context.WithValue(ctx, awsEnvironmentContextKey{}, awsEnvironment)
	ctx = context.WithValue(ctx, stateProjectContextKey{}, documentString(doc, "project"))
	if d.config.TerraformState.Enabled && action != jobs.ActionTLS && action != jobs.ActionStorageExpand && action != jobs.ActionStorageShrink {
		if d.stateProvider == nil {
			return errors.New("统一 Terraform 状态中心尚未配置；请由平台管理员先在“平台管理 / Terraform 状态中心”完成配置")
		}
		stateRuntime, stateErr := d.stateProvider.Runtime(ctx)
		if stateErr != nil {
			return fmt.Errorf("统一 Terraform 状态中心不可用: %w", stateErr)
		}
		runtimeDirectory := filepath.Join(d.config.Paths.DataDir, "terraform-state-runtime", jobID)
		ctx = context.WithValue(ctx, stateBackendContextKey{}, stateRuntime)
		ctx = context.WithValue(ctx, stateRuntimeDirectoryContextKey{}, runtimeDirectory)
		defer d.cleanupStateBackendRuntime(environmentName, runtimeDirectory)
	}
	redacted := NewRedactingWriter(output)
	defer func() { _ = redacted.Flush() }()
	steps := deploymentSteps(action, environment.IsExistingEKS(doc))
	if action == jobs.ActionPlatform {
		if enabledPath(doc, "components.catalog.loki.enabled") {
			steps = append(steps, stepVerifyLogCollector, stepVerifyLokiIngestion, stepVerifyGrafanaLoki)
		}
		if enabledPath(doc, "components.catalog.prometheus.enabled") {
			steps = append(steps, stepVerifyGrafanaDashboards)
		}
	}
	if action == jobs.ActionDeploy && elastiCacheReplicaTarget(doc) == 0 {
		steps = insertStepBefore(steps, stepApplyInfra, stepPrepareElastiCache)
	}
	jobs.SetSteps(ctx, steps)
	requiredTools := []string{d.config.Tools.Terraform, d.config.Tools.AWS, d.config.Tools.Kubectl, d.config.Tools.Helm}
	if action == jobs.ActionTLS {
		requiredTools = []string{d.config.Tools.AWS, d.config.Tools.Kubectl}
	} else if action == jobs.ActionStorageExpand || action == jobs.ActionStorageShrink {
		requiredTools = []string{d.config.Tools.AWS, d.config.Tools.Kubectl}
	} else if action == jobs.ActionAccess {
		requiredTools = []string{d.config.Tools.Terraform, d.config.Tools.AWS, d.config.Tools.Kubectl}
	}
	if err := CheckTools(requiredTools...); err != nil {
		return err
	}
	if !environment.IsExistingEKS(doc) && (action == jobs.ActionPlan || action == jobs.ActionDeploy) {
		ctx, err = d.withMergedEKSPublicAccessCIDRs(ctx, doc, redacted)
		if err != nil {
			return err
		}
	}

	switch action {
	case jobs.ActionValidate:
		return d.validate(ctx, environmentName, doc, redacted)
	case jobs.ActionPlan:
		if environment.IsExistingEKS(doc) {
			return fmt.Errorf("existing EKS environments do not create an infrastructure plan; run component deployment instead")
		}
		return d.plan(ctx, environmentName, jobID, doc, redacted)
	case jobs.ActionDeploy:
		if environment.IsExistingEKS(doc) {
			return fmt.Errorf("existing EKS environments skip phase 1; run component deployment instead")
		}
		return d.deploy(ctx, environmentName, jobID, doc, redacted)
	case jobs.ActionPlatform:
		return d.platform(ctx, environmentName, doc, redacted)
	case jobs.ActionAccess:
		return d.access(ctx, environmentName, doc, redacted)
	case jobs.ActionTLS:
		return d.applyTLSOnly(ctx, environmentName, jobID, doc, redacted)
	case jobs.ActionStorageExpand, jobs.ActionStorageShrink:
		return d.resizeClickVisualStorage(ctx, environmentName, jobID, action, doc, redacted)
	case jobs.ActionDestroy:
		return d.destroy(ctx, environmentName, doc, redacted)
	default:
		return jobs.ErrInvalidAction
	}
}

func actionUsesInfrastructure(action jobs.Action) bool {
	return action == jobs.ActionValidate || action == jobs.ActionPlan || action == jobs.ActionDeploy || action == jobs.ActionDestroy
}

func (d *Deployment) loadDataServicePasswords(ctx context.Context, doc environment.Document) (map[string]string, error) {
	required := make(map[string]string)
	for _, service := range []string{"rds", "aurora"} {
		configValue, ok := environment.GetPath(doc, "data_services."+service)
		config, valid := configValue.(map[string]any)
		if !ok || !valid || !documentMapBoolDefault(config, "enabled", false) || documentMapString(config, "credential_management") != "self-managed" {
			continue
		}
		required[service] = strings.TrimSpace(documentMapString(config, "master_username"))
	}
	if len(required) == 0 {
		return map[string]string{}, nil
	}
	if d.dataServiceCredentialProvider == nil {
		return nil, errors.New("RDS/Aurora 已选择自我管理凭证，但数据库凭证服务不可用")
	}
	project, environmentName := documentString(doc, "project"), documentString(doc, "environment")
	materials, err := d.dataServiceCredentialProvider.Materials(ctx, project, environmentName)
	if err != nil {
		return nil, fmt.Errorf("读取 RDS/Aurora 自管凭证: %w", err)
	}
	passwords := make(map[string]string, len(required))
	for service, username := range required {
		material, exists := materials[service]
		if !exists || material.Password == "" {
			clearDataServiceMaterials(materials)
			return nil, fmt.Errorf("%s 已选择“自我管理凭证”，但尚未保存管理员密码；请在阶段1云数据库参数中填写后保存", strings.ToUpper(service))
		}
		if !strings.EqualFold(strings.TrimSpace(material.Username), username) {
			clearDataServiceMaterials(materials)
			return nil, fmt.Errorf("%s 配置的管理员用户名 %q 与已保存凭证用户名 %q 不一致；请重新输入密码并保存", strings.ToUpper(service), username, material.Username)
		}
		passwords[service] = material.Password
	}
	clearDataServiceMaterials(materials)
	return passwords, nil
}

func clearDataServiceMaterials(materials map[string]dataservicecredentials.Material) {
	for key, material := range materials {
		material.Password = ""
		materials[key] = material
	}
}

func clearPasswordMap(passwords map[string]string) {
	for key := range passwords {
		passwords[key] = ""
		delete(passwords, key)
	}
}

func deploymentSteps(action jobs.Action, existingEKS bool) []string {
	if existingEKS {
		switch action {
		case jobs.ActionValidate:
			return []string{stepUpdateKubeconfig, stepCheckExistingEKS, stepInitializePlatform, stepPreparePlatform, stepCheckFormatting, stepValidatePlatform, stepLintEtcd, stepLintXXLJob, stepLintNacos}
		case jobs.ActionPlatform:
			return []string{stepUpdateKubeconfig, stepCheckExistingEKS, stepPreflightComponentHealth, stepInitializePlatform, stepPreparePlatform, stepCheckPlatformStateLock, stepReconcileNamespaces, stepReconcileReleases, stepApplyComponents, stepReconcileCollectorWAL, stepSyncConsulClientCA, stepConfigureAlerting, stepSyncGatewayAddress, stepApplyTLSCertificates, stepVerifyDomainBackends, stepVerifyPods}
		case jobs.ActionAccess:
			return []string{stepUpdateKubeconfig, stepCheckExistingEKS, stepInitializePlatform, stepPreparePlatform, stepApplyAccess, stepConfigureAlerting, stepSyncGatewayAddress, stepApplyTLSCertificates, stepVerifyDomainBackends}
		case jobs.ActionTLS:
			return []string{stepUpdateKubeconfig, stepCheckExistingEKS, stepInitializePlatform, stepPreparePlatform, stepEnsureTLSNamespaces, stepApplyTLSCertificates}
		case jobs.ActionStorageExpand:
			return []string{stepUpdateKubeconfig, stepInspectManagedStorage, stepExpandManagedStorage, stepVerifyManagedStorage}
		case jobs.ActionStorageShrink:
			return []string{stepUpdateKubeconfig, stepInspectManagedStorage, stepStopStorageWorkload, stepMigrateManagedStorage, stepSwitchManagedStorage, stepVerifyManagedStorage}
		case jobs.ActionDestroy:
			return []string{stepInitializePlatform, stepPreparePlatform, stepUpdateKubeconfig, stepDestroyPlatform, stepDetachExistingEKS}
		}
	}
	switch action {
	case jobs.ActionValidate:
		return []string{
			stepInitializeInfra, stepPrepareInfra, stepInitializePlatform, stepPreparePlatform, stepCheckFormatting, stepValidateInfra,
			stepValidatePlatform, stepLintEtcd, stepLintXXLJob, stepLintNacos,
		}
	case jobs.ActionPlan:
		return []string{stepInitializeInfra, stepPrepareInfra, stepReconcileEKSNodeGroups, stepCheckEKSNodeGroupQuota, stepPlanInfra}
	case jobs.ActionDeploy:
		return []string{
			stepInitializeInfra, stepPrepareInfra, stepInitializePlatform, stepPreparePlatform,
			stepCheckFormatting, stepValidateInfra, stepValidatePlatform, stepLintEtcd, stepLintXXLJob, stepLintNacos,
			stepEnsureSharedECR, stepReconcileEKSNodeGroups, stepCheckEKSNodeGroupQuota, stepPlanInfra, stepApplyInfra, stepUpdateKubeconfig, stepReconcileBaseReleases,
			stepCheckPlatformStateLock, stepReconcileNamespaces, stepPlanBase, stepApplyBase, stepVerifyNodes,
		}
	case jobs.ActionPlatform:
		return []string{
			stepUpdateKubeconfig, stepPreflightComponentHealth, stepInitializePlatform, stepPreparePlatform, stepCheckPlatformStateLock, stepReconcileNamespaces, stepReconcileReleases, stepApplyComponents, stepReconcileCollectorWAL, stepSyncConsulClientCA, stepConfigureAlerting, stepSyncGatewayAddress, stepApplyTLSCertificates, stepVerifyDomainBackends, stepVerifyPods,
		}
	case jobs.ActionAccess:
		return []string{stepUpdateKubeconfig, stepInitializePlatform, stepPreparePlatform, stepApplyAccess, stepConfigureAlerting, stepSyncGatewayAddress, stepApplyTLSCertificates, stepVerifyDomainBackends}
	case jobs.ActionTLS:
		return []string{stepUpdateKubeconfig, stepInitializePlatform, stepPreparePlatform, stepEnsureTLSNamespaces, stepApplyTLSCertificates}
	case jobs.ActionStorageExpand:
		return []string{stepUpdateKubeconfig, stepInspectManagedStorage, stepExpandManagedStorage, stepVerifyManagedStorage}
	case jobs.ActionStorageShrink:
		return []string{stepUpdateKubeconfig, stepInspectManagedStorage, stepStopStorageWorkload, stepMigrateManagedStorage, stepSwitchManagedStorage, stepVerifyManagedStorage}
	case jobs.ActionDestroy:
		return []string{
			stepInitializeInfra, stepPrepareInfra, stepInitializePlatform, stepPreparePlatform,
			stepUpdateKubeconfig, stepDeleteNamespaces, stepDestroyPlatform, stepDestroyEKSCompute,
			stepCleanupOrphanedNetwork, stepDestroyInfra,
		}
	default:
		return nil
	}
}

func (d *Deployment) validate(ctx context.Context, name string, doc environment.Document, output io.Writer) error {
	materials, err := d.loadUploadedTLSMaterials(ctx, doc)
	if err != nil {
		return err
	}
	clearUploadedTLSMaterials(materials)
	if environment.IsExistingEKS(doc) {
		kubeconfig, err := d.updateKubeconfig(ctx, name, doc, output)
		if err != nil {
			return err
		}
		if err := d.checkExistingEKS(ctx, doc, kubeconfig, output); err != nil {
			return err
		}
		if err := d.terraformInit(ctx, d.config.Paths.TerraformPlatformDir, name, output); err != nil {
			return err
		}
		if err := d.terraformWorkspace(ctx, d.config.Paths.TerraformPlatformDir, name, output); err != nil {
			return err
		}
		if err := d.step(ctx, output, stepCheckFormatting, Command{
			Name: d.config.Tools.Terraform, Args: []string{"fmt", "-check", "-recursive", filepath.Join(d.config.Paths.RepositoryRoot, "terraform")},
			Dir: d.config.Paths.RepositoryRoot, Env: d.commandEnv(ctx, ""),
		}); err != nil {
			return err
		}
		if err := d.step(ctx, output, stepValidatePlatform, Command{
			Name: d.config.Tools.Terraform, Args: []string{"validate", "-no-color"},
			Dir: d.config.Paths.TerraformPlatformDir, Env: d.terraformDataEnv(ctx, name, d.config.Paths.TerraformPlatformDir),
		}); err != nil {
			return err
		}
		return d.lintLocalCharts(ctx, output)
	}
	if err := d.terraformInit(ctx, d.config.Paths.TerraformInfraDir, name, output); err != nil {
		return err
	}
	if err := d.terraformWorkspace(ctx, d.config.Paths.TerraformInfraDir, name, output); err != nil {
		return err
	}
	if err := d.terraformInit(ctx, d.config.Paths.TerraformPlatformDir, name, output); err != nil {
		return err
	}
	if err := d.terraformWorkspace(ctx, d.config.Paths.TerraformPlatformDir, name, output); err != nil {
		return err
	}
	if err := d.step(ctx, output, stepCheckFormatting, Command{
		Name: d.config.Tools.Terraform,
		Args: []string{"fmt", "-check", "-recursive", filepath.Join(d.config.Paths.RepositoryRoot, "terraform")},
		Dir:  d.config.Paths.RepositoryRoot,
		Env:  d.commandEnv(ctx, ""),
	}); err != nil {
		return err
	}
	for _, item := range []struct{ dir, label string }{{d.config.Paths.TerraformInfraDir, stepValidateInfra}, {d.config.Paths.TerraformPlatformDir, stepValidatePlatform}} {
		if err := d.step(ctx, output, item.label, Command{
			Name: d.config.Tools.Terraform,
			Args: []string{"validate", "-no-color"},
			Dir:  item.dir,
			Env:  d.terraformDataEnv(ctx, name, item.dir),
		}); err != nil {
			return err
		}
	}
	return d.lintLocalCharts(ctx, output)
}

func (d *Deployment) lintLocalCharts(ctx context.Context, output io.Writer) error {
	if err := d.step(ctx, output, stepLintEtcd, Command{
		Name: d.config.Tools.Helm,
		Args: []string{"lint", d.config.Paths.EtcdChartDir},
		Dir:  d.config.Paths.RepositoryRoot,
		Env:  d.commandEnv(ctx, ""),
	}); err != nil {
		return err
	}
	xxlJobChart := filepath.Join(d.config.Paths.TerraformPlatformDir, "charts", "xxl-job")
	if err := d.step(ctx, output, stepLintXXLJob, Command{
		Name: d.config.Tools.Helm,
		Args: []string{
			"lint", xxlJobChart,
			"--set-string", "admin.password=validation-only",
			"--set-string", "mysql.password=validation-only",
		},
		Dir: d.config.Paths.RepositoryRoot,
		Env: d.commandEnv(ctx, ""),
	}); err != nil {
		return err
	}
	nacosChart := filepath.Join(d.config.Paths.TerraformPlatformDir, "charts", "nacos")
	return d.step(ctx, output, stepLintNacos, Command{
		Name: d.config.Tools.Helm,
		Args: []string{
			"lint", nacosChart,
			"--set-string", "auth.token=validation-only",
			"--set-string", "auth.identityValue=validation-only",
		},
		Dir: d.config.Paths.RepositoryRoot,
		Env: d.commandEnv(ctx, ""),
	})
}

func (d *Deployment) plan(ctx context.Context, name, jobID string, doc environment.Document, output io.Writer) error {
	if err := d.terraformInit(ctx, d.config.Paths.TerraformInfraDir, name, output); err != nil {
		return err
	}
	if err := d.terraformWorkspace(ctx, d.config.Paths.TerraformInfraDir, name, output); err != nil {
		return err
	}
	if err := d.reconcileExistingEKSNodeGroups(ctx, name, doc, output); err != nil {
		return err
	}
	if err := d.checkEKSNodeGroupVCPUQuota(ctx, doc, output); err != nil {
		return err
	}
	planPath, err := d.deploymentPlanPath(jobID, "infra")
	if err != nil {
		return err
	}
	defer os.Remove(planPath)
	return d.terraformPlanToFile(ctx, d.config.Paths.TerraformInfraDir, name, "", planPath, stepPlanInfra, output)
}

func (d *Deployment) deploy(ctx context.Context, name, jobID string, doc environment.Document, output io.Writer) error {
	// A deployment is intentionally self-contained: validation, an explicit
	// saved Terraform plan and the matching apply all appear in one task. This
	// keeps the UI to one button without sacrificing the plan or preflight.
	if err := d.validate(ctx, name, doc, output); err != nil {
		return err
	}
	if err := d.ensureSharedECRRepositories(ctx, doc, output); err != nil {
		return err
	}
	if err := d.logCloudServiceLifecyclePlan(ctx, name, doc, output); err != nil {
		return err
	}
	if err := d.reconcileExistingEKSNodeGroups(ctx, name, doc, output); err != nil {
		return err
	}
	if err := d.checkEKSNodeGroupVCPUQuota(ctx, doc, output); err != nil {
		return err
	}
	infraPlanPath, err := d.deploymentPlanPath(jobID, "infra")
	if err != nil {
		return err
	}
	defer os.Remove(infraPlanPath)
	if err := d.terraformPlanToFile(ctx, d.config.Paths.TerraformInfraDir, name, "", infraPlanPath, stepPlanInfra, output); err != nil {
		return err
	}
	if err := d.prepareElastiCacheReplicaScaleDown(ctx, doc, output); err != nil {
		return err
	}
	if err := d.terraformApplyPlan(ctx, d.config.Paths.TerraformInfraDir, name, infraPlanPath, stepApplyInfra, output); err != nil {
		return err
	}
	kubeconfig, err := d.updateKubeconfig(ctx, name, doc, output)
	if err != nil {
		return err
	}
	if err := d.reconcileInterruptedBaseServices(ctx, doc, kubeconfig, output); err != nil {
		return err
	}
	// Phase one also manages Namespace-scoped base services (Consul and etcd).
	// Reuse safe pre-existing Namespaces before planning so migrations or manual
	// Namespace creation cannot make Terraform fail with AlreadyExists.
	if err := d.checkTerraformStateLock(ctx, name, "platform", output); err != nil {
		return err
	}
	if err := d.reconcileExistingNamespaces(ctx, name, doc, kubeconfig, output); err != nil {
		return err
	}
	basePlanPath, err := d.deploymentPlanPath(jobID, "base")
	if err != nil {
		return err
	}
	defer os.Remove(basePlanPath)
	if err := d.terraformPlanToFile(ctx, d.config.Paths.TerraformPlatformDir, name, "base", basePlanPath, stepPlanBase, output); err != nil {
		return err
	}
	if err := d.terraformApplyPlan(ctx, d.config.Paths.TerraformPlatformDir, name, basePlanPath, stepApplyBase, output); err != nil {
		return err
	}
	if !enabledPath(doc, "components.consul.enabled") {
		_, _ = fmt.Fprintln(output, "\n==> 清理 Consul 卸载后的跨 Namespace CA")
		d.cleanupConsulClientCA(ctx, kubeconfig, doc, output)
	}
	return d.step(ctx, output, stepVerifyNodes, Command{
		Name: d.config.Tools.Kubectl,
		Args: []string{"get", "nodes", "-o", "wide"},
		Dir:  d.config.Paths.RepositoryRoot,
		Env:  d.commandEnv(ctx, kubeconfig),
	})
}

type sharedECRConfiguration struct {
	Enabled            bool
	Region             string
	Project            string
	Repositories       []string
	ImageTagMutability string
	ScanOnPush         bool
	UntaggedExpireDays int
	KeepImageCount     int
}

type cloudServiceLifecycleItem struct {
	Key, Name, Action, DataPolicy string
}

func cloudServiceLifecyclePlan(doc environment.Document, terraformState string) []cloudServiceLifecycleItem {
	type definition struct {
		key, name string
		addresses []string
	}
	definitions := []definition{
		{"rds", "RDS MySQL", []string{"aws_db_instance.admin"}},
		{"aurora", "Aurora MySQL", []string{"aws_rds_cluster.game"}},
		{"postgres", "RDS PostgreSQL", []string{"aws_db_instance.postgres"}},
		{"documentdb", "Amazon DocumentDB", []string{"aws_docdb_cluster.this"}},
		{"elasticache", "AWS ElastiCache", []string{"aws_elasticache_replication_group.game", "aws_elasticache_serverless_cache.game"}},
		{"msk", "Amazon MSK Kafka", []string{"aws_msk_cluster.this", "aws_msk_serverless_cluster.this"}},
		{"amazon_mq", "Amazon MQ RabbitMQ", []string{"aws_mq_broker.rabbitmq"}},
	}
	result := make([]cloudServiceLifecycleItem, 0, len(definitions)+1)
	for _, item := range definitions {
		prefix := "data_services." + item.key
		desired := enabledPath(doc, prefix+".enabled")
		actual := false
		for _, address := range item.addresses {
			if strings.Contains(terraformState, address) {
				actual = true
				break
			}
		}
		if !desired && !actual {
			continue
		}
		action := "创建"
		if desired && actual {
			action = "更新/对账"
		} else if !desired {
			action = "删除"
		}
		result = append(result, cloudServiceLifecycleItem{Key: item.key, Name: item.name, Action: action, DataPolicy: cloudServiceDataPolicy(doc, item.key)})
	}
	if enabledPath(doc, "ecr.enabled") {
		result = append(result, cloudServiceLifecycleItem{Key: "ecr", Name: "Amazon ECR", Action: "复用/对账", DataPolicy: "项目共享仓库与镜像保留，环境删除不删仓库"})
	}
	return result
}

func cloudServiceDataPolicy(doc environment.Document, key string) string {
	prefix := "data_services." + key
	if protection, found := environment.GetPath(doc, prefix+".deletion_protection"); found {
		if enabled, valid := protection.(bool); valid && enabled {
			return "删除保护已开启，AWS 会阻止删除"
		}
	}
	if key == "rds" || key == "aurora" || key == "postgres" || key == "documentdb" {
		if skip, found := environment.GetPath(doc, prefix+".skip_final_snapshot"); found {
			if value, valid := skip.(bool); valid && !value {
				return "删除前创建最终快照"
			}
		}
		return "跳过最终快照，删除时数据不保留"
	}
	switch key {
	case "elasticache":
		return "删除在线缓存，已有手工快照按 AWS 策略保留"
	case "msk":
		return "删除 Broker 存储和主题数据"
	case "amazon_mq":
		return "删除 Broker、队列和未消费消息"
	default:
		return "按 Terraform 和 AWS 资源策略处理"
	}
}

func (d *Deployment) logCloudServiceLifecyclePlan(ctx context.Context, name string, doc environment.Document, output io.Writer) error {
	var state bytes.Buffer
	command := Command{
		Name: d.config.Tools.Terraform, Args: []string{"state", "list"},
		Dir: d.config.Paths.TerraformInfraDir, Env: d.terraformEnv(ctx, name, d.config.Paths.TerraformInfraDir),
	}
	if err := d.executor.Run(ctx, command, &state); err != nil {
		_, _ = fmt.Fprintf(output, "Terraform State 尚无可读取资源，按首次部署生成云服务计划：%v\n", err)
		state.Reset()
	}
	items := cloudServiceLifecyclePlan(doc, state.String())
	_, _ = fmt.Fprintln(output, "\n==> 阶段1 AWS 云服务生命周期计划")
	if len(items) == 0 {
		_, _ = fmt.Fprintln(output, "未发现需要创建、更新或删除的 AWS 云服务。")
		return nil
	}
	counts := map[string]int{}
	for _, item := range items {
		counts[item.Action]++
		_, _ = fmt.Fprintf(output, "[%s] %s；数据策略：%s\n", item.Action, item.Name, item.DataPolicy)
	}
	_, _ = fmt.Fprintf(output, "计划汇总：创建 %d，更新/对账 %d，删除 %d，共享复用 %d。\n", counts["创建"], counts["更新/对账"], counts["删除"], counts["复用/对账"])
	return nil
}

func sharedECRConfigurationFromDocument(doc environment.Document) (sharedECRConfiguration, error) {
	configuration := sharedECRConfiguration{
		Region:             documentString(doc, "region"),
		Project:            documentString(doc, "project"),
		ImageTagMutability: "IMMUTABLE",
		ScanOnPush:         true,
		UntaggedExpireDays: 7,
		KeepImageCount:     30,
	}
	raw, exists := environment.GetPath(doc, "ecr")
	ecr, valid := raw.(map[string]any)
	if !exists || !valid || !documentMapBoolDefault(ecr, "enabled", false) {
		return configuration, nil
	}
	configuration.Enabled = true
	configuration.ImageTagMutability = strings.ToUpper(defaultString(documentMapString(ecr, "image_tag_mutability"), "IMMUTABLE"))
	if configuration.ImageTagMutability != "IMMUTABLE" && configuration.ImageTagMutability != "MUTABLE" {
		return configuration, errors.New("ECR 镜像标签策略必须是 IMMUTABLE 或 MUTABLE")
	}
	configuration.ScanOnPush = documentMapBoolDefault(ecr, "scan_on_push", true)
	configuration.UntaggedExpireDays = documentMapIntDefault(ecr, "untagged_expire_days", 7)
	configuration.KeepImageCount = documentMapIntDefault(ecr, "keep_image_count", 30)

	seen := make(map[string]struct{})
	appendRepository := func(value string) error {
		value = strings.Trim(strings.TrimSpace(value), "/")
		if value == "" {
			return nil
		}
		if strings.Contains(value, "://") || strings.ContainsAny(value, " @:") {
			return fmt.Errorf("ECR 仓库名称 %q 不合法；这里只填写仓库路径，不填写 Registry 地址或 Tag", value)
		}
		name := value
		if !strings.HasPrefix(name, configuration.Project+"/") {
			name = configuration.Project + "/" + name
		}
		if _, duplicate := seen[name]; duplicate {
			return nil
		}
		seen[name] = struct{}{}
		configuration.Repositories = append(configuration.Repositories, name)
		return nil
	}
	switch repositories := ecr["repositories"].(type) {
	case []any:
		for _, item := range repositories {
			value, ok := item.(string)
			if !ok {
				return configuration, errors.New("ECR 仓库列表只能包含仓库名称")
			}
			if err := appendRepository(value); err != nil {
				return configuration, err
			}
		}
	case []string:
		for _, value := range repositories {
			if err := appendRepository(value); err != nil {
				return configuration, err
			}
		}
	case nil:
	default:
		return configuration, errors.New("ECR 仓库列表格式不正确")
	}
	sort.Strings(configuration.Repositories)
	return configuration, nil
}

func sharedECRLifecyclePolicy(configuration sharedECRConfiguration) (string, error) {
	policy := map[string]any{"rules": []any{
		map[string]any{
			"rulePriority": 1,
			"description":  "Remove untagged images after the configured retention period",
			"selection": map[string]any{
				"tagStatus": "untagged", "countType": "sinceImagePushed", "countUnit": "days", "countNumber": configuration.UntaggedExpireDays,
			},
			"action": map[string]any{"type": "expire"},
		},
		map[string]any{
			"rulePriority": 2,
			"description":  "Keep the most recent release images",
			"selection": map[string]any{
				"tagStatus": "any", "countType": "imageCountMoreThan", "countNumber": configuration.KeepImageCount,
			},
			"action": map[string]any{"type": "expire"},
		},
	}}
	payload, err := json.Marshal(policy)
	if err != nil {
		return "", fmt.Errorf("生成 ECR 生命周期策略: %w", err)
	}
	return string(payload), nil
}

// ensureSharedECRRepositories keeps ECR ownership outside an environment's
// Terraform state. Each environment may use a distinct repository list (for
// example demo/gateway for test and demo/prod-gateway for production), while the
// runner creates and reconciles only the repositories named by that environment.
func (d *Deployment) ensureSharedECRRepositories(ctx context.Context, doc environment.Document, output io.Writer) error {
	label := stepEnsureSharedECR
	jobs.StepStarted(ctx, label)
	_, _ = fmt.Fprintf(output, "\n==> %s\n", label)
	fail := func(err error) error {
		jobs.StepFinished(ctx, label, err)
		return fmt.Errorf("%s: %w", label, err)
	}
	configuration, err := sharedECRConfigurationFromDocument(doc)
	if err != nil {
		return fail(err)
	}
	if !configuration.Enabled || len(configuration.Repositories) == 0 {
		_, _ = fmt.Fprintln(output, "当前环境未启用 ECR 或未配置仓库，无需处理项目共享仓库。")
		jobs.StepFinished(ctx, label, nil)
		return nil
	}
	policy, err := sharedECRLifecyclePolicy(configuration)
	if err != nil {
		return fail(err)
	}
	common := []string{"--region", configuration.Region, "--no-cli-pager"}
	run := func(args []string) (string, error) {
		var result bytes.Buffer
		command := Command{Name: d.config.Tools.AWS, Args: append(args, common...), Dir: d.config.Paths.RepositoryRoot, Env: d.commandEnv(ctx, "")}
		if err := d.executor.Run(ctx, command, &result); err != nil {
			return result.String(), fmt.Errorf("%w: %s", err, strings.TrimSpace(result.String()))
		}
		return result.String(), nil
	}
	for _, repository := range configuration.Repositories {
		_, describeErr := run([]string{"ecr", "describe-repositories", "--repository-names", repository, "--output", "json"})
		created := false
		if describeErr != nil {
			message := strings.ToLower(describeErr.Error())
			if !strings.Contains(message, "repositorynotfoundexception") {
				return fail(fmt.Errorf("读取共享 ECR 仓库 %s: %w", repository, describeErr))
			}
			_, createErr := run([]string{
				"ecr", "create-repository", "--repository-name", repository,
				"--image-tag-mutability", configuration.ImageTagMutability,
				"--image-scanning-configuration", "scanOnPush=" + strconv.FormatBool(configuration.ScanOnPush),
				"--encryption-configuration", "encryptionType=AES256",
				"--tags", "Key=ManagedBy,Value=OpsDeployPlatform", "Key=Project,Value=" + configuration.Project,
				"--output", "json",
			})
			if createErr != nil && !strings.Contains(strings.ToLower(createErr.Error()), "repositoryalreadyexistsexception") {
				return fail(fmt.Errorf("创建共享 ECR 仓库 %s: %w", repository, createErr))
			}
			created = createErr == nil
		}
		if _, err := run([]string{"ecr", "put-image-tag-mutability", "--repository-name", repository, "--image-tag-mutability", configuration.ImageTagMutability}); err != nil {
			return fail(fmt.Errorf("同步共享 ECR 仓库 %s 的镜像标签策略: %w", repository, err))
		}
		if _, err := run([]string{"ecr", "put-image-scanning-configuration", "--repository-name", repository, "--image-scanning-configuration", "scanOnPush=" + strconv.FormatBool(configuration.ScanOnPush)}); err != nil {
			return fail(fmt.Errorf("同步共享 ECR 仓库 %s 的扫描策略: %w", repository, err))
		}
		if _, err := run([]string{"ecr", "put-lifecycle-policy", "--repository-name", repository, "--lifecycle-policy-text", policy}); err != nil {
			return fail(fmt.Errorf("同步共享 ECR 仓库 %s 的保留策略: %w", repository, err))
		}
		if created {
			_, _ = fmt.Fprintf(output, "[已创建] %s（项目共享，环境销毁不会删除）\n", repository)
		} else {
			_, _ = fmt.Fprintf(output, "[已复用] %s（已存在，不重复创建）\n", repository)
		}
	}
	_, _ = fmt.Fprintln(output, "ECR 仓库已按当前环境配置完成对接；其他环境的仓库不会被修改。")
	jobs.StepFinished(ctx, label, nil)
	return nil
}

type elastiCacheReplicationGroupDescription struct {
	ReplicationGroups []struct {
		Status            string `json:"Status"`
		MultiAZ           string `json:"MultiAZ"`
		AutomaticFailover string `json:"AutomaticFailover"`
		NodeGroups        []struct {
			NodeGroupMembers []json.RawMessage `json:"NodeGroupMembers"`
		} `json:"NodeGroups"`
	} `json:"ReplicationGroups"`
}

func elastiCacheReplicaTarget(doc environment.Document) int {
	raw, ok := environment.GetPath(doc, "data_services.elasticache")
	config, valid := raw.(map[string]any)
	if !ok || !valid || !documentMapBoolDefault(config, "enabled", false) || documentMapString(config, "mode") == "serverless" {
		return -1
	}
	nodesPerShard := documentMapIntDefault(config, "nodes_per_shard", documentMapIntDefault(config, "replicas_per_node_group", 1)+1)
	return max(0, nodesPerShard-1)
}

func insertStepBefore(steps []string, before, inserted string) []string {
	for index, step := range steps {
		if step != before {
			continue
		}
		result := make([]string, 0, len(steps)+1)
		result = append(result, steps[:index]...)
		result = append(result, inserted)
		result = append(result, steps[index:]...)
		return result
	}
	return append(steps, inserted)
}

// prepareElastiCacheReplicaScaleDown handles an AWS API ordering constraint.
// When the target has no replicas, Multi-AZ and automatic failover must be
// disabled and reach AVAILABLE before Terraform calls DecreaseReplicaCount.
// Keeping this as an explicit job step makes retries idempotent and prevents a
// valid six-node-to-three-node resize from failing after a long provider wait.
func (d *Deployment) prepareElastiCacheReplicaScaleDown(ctx context.Context, doc environment.Document, output io.Writer) error {
	if elastiCacheReplicaTarget(doc) != 0 {
		return nil
	}
	label := stepPrepareElastiCache
	jobs.StepStarted(ctx, label)
	_, _ = fmt.Fprintf(output, "\n==> %s\n", label)
	fail := func(err error) error {
		jobs.StepFinished(ctx, label, err)
		return fmt.Errorf("%s: %w", label, err)
	}

	identifier := documentString(doc, "project") + "-" + documentString(doc, "environment") + "-game"
	region := documentString(doc, "region")
	commonArgs := []string{"--replication-group-id", identifier, "--region", region, "--no-cli-pager"}
	var descriptionOutput bytes.Buffer
	describeArgs := append([]string{"elasticache", "describe-replication-groups"}, commonArgs...)
	if err := d.executor.Run(ctx, Command{
		Name: d.config.Tools.AWS, Args: describeArgs, Dir: d.config.Paths.RepositoryRoot, Env: d.commandEnv(ctx, ""),
	}, &descriptionOutput); err != nil {
		message := strings.ToLower(descriptionOutput.String() + " " + err.Error())
		if strings.Contains(message, "replicationgroupnotfound") || strings.Contains(message, "not found") {
			_, _ = fmt.Fprintln(output, "ElastiCache 尚未创建，无需执行缩容前置步骤。")
			jobs.StepFinished(ctx, label, nil)
			return nil
		}
		_, _ = io.Copy(output, &descriptionOutput)
		return fail(fmt.Errorf("读取 ElastiCache 当前高可用状态: %w", err))
	}
	var description elastiCacheReplicationGroupDescription
	if err := json.Unmarshal(descriptionOutput.Bytes(), &description); err != nil {
		return fail(fmt.Errorf("解析 ElastiCache 当前状态: %w", err))
	}
	if len(description.ReplicationGroups) == 0 {
		_, _ = fmt.Fprintln(output, "ElastiCache 尚未创建，无需执行缩容前置步骤。")
		jobs.StepFinished(ctx, label, nil)
		return nil
	}
	group := description.ReplicationGroups[0]
	if !strings.EqualFold(group.Status, "available") {
		return fail(fmt.Errorf("ElastiCache %s 当前状态为 %s；请等待变更完成后重试", identifier, group.Status))
	}
	multiAZEnabled := strings.EqualFold(group.MultiAZ, "enabled")
	failoverEnabled := strings.EqualFold(group.AutomaticFailover, "enabled") || strings.EqualFold(group.AutomaticFailover, "enabling")
	if !multiAZEnabled && !failoverEnabled {
		_, _ = fmt.Fprintln(output, "ElastiCache Multi-AZ 与自动故障转移已关闭，可以安全减少到每分片 0 个副本。")
		jobs.StepFinished(ctx, label, nil)
		return nil
	}

	_, _ = fmt.Fprintf(output, "目标为每分片仅保留主节点；先关闭 %s 的 Multi-AZ 与自动故障转移，等待 AVAILABLE 后再减少副本。\n", identifier)
	modifyArgs := append([]string{"elasticache", "modify-replication-group"}, commonArgs...)
	modifyArgs = append(modifyArgs, "--no-multi-az-enabled", "--no-automatic-failover-enabled", "--apply-immediately", "--output", "json")
	if err := d.executor.Run(ctx, Command{
		Name: d.config.Tools.AWS, Args: modifyArgs, Dir: d.config.Paths.RepositoryRoot, Env: d.commandEnv(ctx, ""),
	}, output); err != nil {
		return fail(fmt.Errorf("关闭 ElastiCache Multi-AZ 与自动故障转移: %w", err))
	}
	waitArgs := append([]string{"elasticache", "wait", "replication-group-available"}, commonArgs...)
	if err := d.executor.Run(ctx, Command{
		Name: d.config.Tools.AWS, Args: waitArgs, Dir: d.config.Paths.RepositoryRoot, Env: d.commandEnv(ctx, ""),
	}, output); err != nil {
		return fail(fmt.Errorf("等待 ElastiCache 高可用设置更新完成: %w", err))
	}
	_, _ = fmt.Fprintln(output, "ElastiCache 高可用前置状态已完成，继续执行 Terraform 副本缩容。")
	jobs.StepFinished(ctx, label, nil)
	return nil
}

func (d *Deployment) platform(ctx context.Context, name string, doc environment.Document, output io.Writer) error {
	if err := validateDeferredComponentCapacity(doc); err != nil {
		return err
	}
	materials, err := d.loadUploadedTLSMaterials(ctx, doc)
	if err != nil {
		return err
	}
	defer clearUploadedTLSMaterials(materials)
	kubeconfig, err := d.updateKubeconfig(ctx, name, doc, output)
	if err != nil {
		return err
	}
	if environment.IsExistingEKS(doc) {
		if err := d.checkExistingEKS(ctx, doc, kubeconfig, output); err != nil {
			return err
		}
	}
	if err := d.preflightComponentHealth(ctx, kubeconfig, doc, output); err != nil {
		return err
	}
	if err := d.terraformInit(ctx, d.config.Paths.TerraformPlatformDir, name, output); err != nil {
		return err
	}
	if err := d.terraformWorkspace(ctx, d.config.Paths.TerraformPlatformDir, name, output); err != nil {
		return err
	}
	// The release reconciliation below may roll back or uninstall an interrupted
	// Helm release. Verify the centralized Terraform lock first so a stale or
	// concurrent writer can never cause cluster mutations before apply obtains
	// the same lock.
	if err := d.checkTerraformStateLock(ctx, name, "platform", output); err != nil {
		return err
	}
	if err := d.reconcileExistingNamespaces(ctx, name, doc, kubeconfig, output); err != nil {
		return err
	}
	if err := d.reconcileInterruptedDataServices(ctx, name, doc, kubeconfig, output); err != nil {
		return err
	}
	d.logAlertingChangeSummary(ctx, kubeconfig, doc, output)
	if err := d.terraformApply(ctx, d.config.Paths.TerraformPlatformDir, name, false, "components", output); err != nil {
		if storageFailure := d.emitComponentFailureDiagnostics(ctx, kubeconfig, doc, output); storageFailure != "" {
			return fmt.Errorf("%w\n%s", err, storageFailure)
		}
		return err
	}
	if err := d.reconcileOpenTelemetryStorage(ctx, kubeconfig, doc, output); err != nil {
		return err
	}
	if err := d.syncConsulClientCA(ctx, kubeconfig, doc, output); err != nil {
		return err
	}
	if err := d.applyAlertmanagerRelay(ctx, kubeconfig, name, doc, output); err != nil {
		return err
	}
	if err := d.syncHigressGatewayAddress(ctx, kubeconfig, doc, output); err != nil {
		return err
	}
	if err := d.applyUploadedTLSMaterials(ctx, kubeconfig, doc, materials, output); err != nil {
		return err
	}
	if err := d.verifyDomainBackends(ctx, kubeconfig, doc, output); err != nil {
		return err
	}
	if err := d.step(ctx, output, stepVerifyPods, Command{
		Name: d.config.Tools.Kubectl,
		Args: []string{"get", "pods", "-A"},
		Dir:  d.config.Paths.RepositoryRoot,
		Env:  d.commandEnv(ctx, kubeconfig),
	}); err != nil {
		return err
	}
	if err := d.verifyLoggingStack(ctx, kubeconfig, doc, output); err != nil {
		return err
	}
	return d.verifyGrafanaDashboards(ctx, kubeconfig, doc, output)
}

func validateDeferredComponentCapacity(doc environment.Document) error {
	if environment.IsExistingEKS(doc) || !enabledPath(doc, "components.catalog.higress.enabled") {
		return nil
	}
	eks, _ := doc["eks"].(map[string]any)
	groups, _ := eks["node_groups"].(map[string]any)
	for name, raw := range groups {
		group, _ := raw.(map[string]any)
		labels, _ := group["labels"].(map[string]any)
		if documentMapString(labels, "workload-class") != "gateway" {
			continue
		}
		if documentMapBoolDefault(group, "capacity_deferred", false) {
			return fmt.Errorf("Higress 专用节点组 %s 的容量仍处于暂缓状态；为避免网关 Pod 全部 Pending，阶段 2 尚未执行。请先完成 EC2 vCPU 配额审批，在 EKS 节点页面把“容量执行”切换为启用并完成阶段 1，再重试阶段 2", name)
		}
		return nil
	}
	return errors.New("Higress 已启用，但没有配置 workload-class=gateway 的专用节点组")
}

// access applies only resources that expose or configure already-installed
// workloads. It intentionally excludes helm_release.catalog so a domain, TLS,
// TCP route, or alerting edit cannot upgrade an unrelated StatefulSet.
func (d *Deployment) access(ctx context.Context, name string, doc environment.Document, output io.Writer) error {
	materials, err := d.loadUploadedTLSMaterials(ctx, doc)
	if err != nil {
		return err
	}
	defer clearUploadedTLSMaterials(materials)
	kubeconfig, err := d.updateKubeconfig(ctx, name, doc, output)
	if err != nil {
		return err
	}
	if environment.IsExistingEKS(doc) {
		if err := d.checkExistingEKS(ctx, doc, kubeconfig, output); err != nil {
			return err
		}
	}
	if err := d.terraformInit(ctx, d.config.Paths.TerraformPlatformDir, name, output); err != nil {
		return err
	}
	if err := d.terraformWorkspace(ctx, d.config.Paths.TerraformPlatformDir, name, output); err != nil {
		return err
	}
	d.logAlertingChangeSummary(ctx, kubeconfig, doc, output)
	if err := d.terraformApply(ctx, d.config.Paths.TerraformPlatformDir, name, false, "access", output); err != nil {
		return err
	}
	if err := d.applyAlertmanagerRelay(ctx, kubeconfig, name, doc, output); err != nil {
		return err
	}
	if err := d.syncHigressGatewayAddress(ctx, kubeconfig, doc, output); err != nil {
		return err
	}
	if err := d.applyUploadedTLSMaterials(ctx, kubeconfig, doc, materials, output); err != nil {
		return err
	}
	return d.verifyDomainBackends(ctx, kubeconfig, doc, output)
}

type domainBackend struct {
	Host      string
	Namespace string
	Service   string
}

func configuredDomainBackends(doc environment.Document) []domainBackend {
	rawDomains, _ := doc["domains"].([]any)
	result := make([]domainBackend, 0, len(rawDomains))
	seen := make(map[string]struct{})
	for _, raw := range rawDomains {
		domain, ok := raw.(map[string]any)
		if !ok || !documentMapBoolDefault(domain, "enabled", true) {
			continue
		}
		namespace := strings.TrimSpace(documentMapString(domain, "namespace"))
		if namespace == "" {
			continue
		}
		host := strings.TrimSpace(documentMapString(domain, "domain"))
		if host == "" {
			host = "IP 路由"
		}
		protocol := strings.ToLower(strings.TrimSpace(documentMapString(domain, "protocol")))
		rawRoutes, hasRoutes := domain["routes"].([]any)
		if protocol == "tcp" || !hasRoutes || len(rawRoutes) == 0 {
			rawRoutes = []any{domain}
		}
		for _, rawRoute := range rawRoutes {
			route, valid := rawRoute.(map[string]any)
			if !valid {
				continue
			}
			service := strings.TrimSpace(documentMapString(route, "service"))
			if service == "" {
				continue
			}
			key := namespace + "/" + service
			if _, exists := seen[key]; exists {
				continue
			}
			seen[key] = struct{}{}
			routeHost := host
			if path := strings.TrimSpace(documentMapString(route, "path")); path != "" && path != "/" {
				routeHost += path
			}
			result = append(result, domainBackend{Host: routeHost, Namespace: namespace, Service: service})
		}
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].Namespace == result[j].Namespace {
			return result[i].Service < result[j].Service
		}
		return result[i].Namespace < result[j].Namespace
	})
	return result
}

func decodeReadyEndpointCounts(payload []byte) (map[string]int, error) {
	var response struct {
		Items []struct {
			Metadata struct {
				Namespace string            `json:"namespace"`
				Labels    map[string]string `json:"labels"`
			} `json:"metadata"`
			Endpoints []struct {
				Addresses  []string `json:"addresses"`
				Conditions struct {
					Ready       *bool `json:"ready"`
					Terminating *bool `json:"terminating"`
				} `json:"conditions"`
			} `json:"endpoints"`
		} `json:"items"`
	}
	if err := json.Unmarshal(payload, &response); err != nil {
		return nil, err
	}
	result := make(map[string]int)
	for _, item := range response.Items {
		service := strings.TrimSpace(item.Metadata.Labels["kubernetes.io/service-name"])
		namespace := strings.TrimSpace(item.Metadata.Namespace)
		if namespace == "" || service == "" {
			continue
		}
		key := namespace + "/" + service
		for _, endpoint := range item.Endpoints {
			ready := endpoint.Conditions.Ready == nil || *endpoint.Conditions.Ready
			terminating := endpoint.Conditions.Terminating != nil && *endpoint.Conditions.Terminating
			if len(endpoint.Addresses) > 0 && ready && !terminating {
				result[key]++
			}
		}
	}
	return result, nil
}

func (d *Deployment) verifyDomainBackends(ctx context.Context, kubeconfig string, doc environment.Document, output io.Writer) error {
	jobs.StepStarted(ctx, stepVerifyDomainBackends)
	_, _ = fmt.Fprintf(output, "\n==> %s\n", stepVerifyDomainBackends)
	backends := configuredDomainBackends(doc)
	if len(backends) == 0 {
		_, _ = fmt.Fprintln(output, "当前环境没有启用的域名转发规则，无需检查后端 Endpoint。")
		jobs.StepFinished(ctx, stepVerifyDomainBackends, nil)
		return nil
	}

	var failures []domainBackend
	for attempt := 1; attempt <= 6; attempt++ {
		var endpointOutput bytes.Buffer
		err := d.executor.Run(ctx, Command{
			Name: d.config.Tools.Kubectl,
			Args: []string{"get", "endpointslices.discovery.k8s.io", "-A", "-o", "json"},
			Dir:  d.config.Paths.RepositoryRoot,
			Env:  d.commandEnv(ctx, kubeconfig),
		}, &endpointOutput)
		if err != nil {
			wrapped := fmt.Errorf("无法读取 EndpointSlice；请检查 EKS RBAC 是否允许 list endpointslices.discovery.k8s.io: %w", err)
			jobs.StepFinished(ctx, stepVerifyDomainBackends, wrapped)
			return wrapped
		}
		ready, decodeErr := decodeReadyEndpointCounts(endpointOutput.Bytes())
		if decodeErr != nil {
			wrapped := fmt.Errorf("解析 EKS EndpointSlice 失败: %w", decodeErr)
			jobs.StepFinished(ctx, stepVerifyDomainBackends, wrapped)
			return wrapped
		}
		failures = failures[:0]
		for _, backend := range backends {
			if ready[backend.Namespace+"/"+backend.Service] == 0 {
				failures = append(failures, backend)
			}
		}
		if len(failures) == 0 {
			for _, backend := range backends {
				_, _ = fmt.Fprintf(output, "[后端正常] %s -> %s/%s（Ready Endpoint: %d）\n", backend.Host, backend.Namespace, backend.Service, ready[backend.Namespace+"/"+backend.Service])
			}
			jobs.StepFinished(ctx, stepVerifyDomainBackends, nil)
			return nil
		}
		if attempt < 6 {
			_, _ = fmt.Fprintf(output, "仍有 %d 个域名后端没有 Ready Endpoint，5 秒后复查（%d/6）…\n", len(failures), attempt)
			timer := time.NewTimer(5 * time.Second)
			select {
			case <-ctx.Done():
				timer.Stop()
				jobs.StepFinished(ctx, stepVerifyDomainBackends, ctx.Err())
				return ctx.Err()
			case <-timer.C:
			}
		}
	}

	details := make([]string, 0, len(failures))
	for _, backend := range failures {
		details = append(details, fmt.Sprintf("%s -> %s/%s", backend.Host, backend.Namespace, backend.Service))
	}
	err := fmt.Errorf("域名转发已创建，但后端 Service 没有 Ready Endpoint：%s。访问会返回 no healthy upstream；请检查 Deployment/StatefulSet 副本数、Pod Ready 状态以及 Service selector，修复后可重试阶段2", strings.Join(details, "；"))
	jobs.StepFinished(ctx, stepVerifyDomainBackends, err)
	return err
}

type kubernetesSecretData struct {
	Data map[string]string `json:"data"`
}

func (d *Deployment) syncConsulClientCA(ctx context.Context, kubeconfig string, doc environment.Document, output io.Writer) error {
	jobs.StepStarted(ctx, stepSyncConsulClientCA)
	_, _ = fmt.Fprintf(output, "\n==> %s\n", stepSyncConsulClientCA)
	targets, sourceNamespace := consulClientCANamespaces(doc)
	if len(targets) == 0 {
		if !enabledPath(doc, "components.consul.enabled") {
			d.cleanupConsulClientCA(ctx, kubeconfig, doc, output)
			_, _ = fmt.Fprintln(output, "Consul 已关闭；已清理平台同步到业务 Namespace 的遗留客户端 CA Secret。")
		} else {
			_, _ = fmt.Fprintln(output, "Consul 没有跨 Namespace 客户端，本步骤无需变更。")
		}
		jobs.StepFinished(ctx, stepSyncConsulClientCA, nil)
		return nil
	}
	var source bytes.Buffer
	command := Command{
		Name: d.config.Tools.Kubectl,
		Args: []string{"get", "secret", "consul-ca-cert", "-n", sourceNamespace, "-o", "json"},
		Dir:  d.config.Paths.RepositoryRoot,
		Env:  d.commandEnv(ctx, kubeconfig),
	}
	_, _ = fmt.Fprintf(output, "$ %s get secret consul-ca-cert -n %s（只读取并转发 CA，不输出正文）\n", command.Name, sourceNamespace)
	if err := d.executor.Run(ctx, command, &source); err != nil {
		wrapped := fmt.Errorf("读取 Consul 客户端 CA 失败: %w", err)
		jobs.StepFinished(ctx, stepSyncConsulClientCA, wrapped)
		return wrapped
	}
	var secret kubernetesSecretData
	if err := json.Unmarshal(source.Bytes(), &secret); err != nil || strings.TrimSpace(secret.Data["tls.crt"]) == "" {
		wrapped := errors.New("读取 Consul 客户端 CA 失败: consul-ca-cert 缺少 tls.crt")
		jobs.StepFinished(ctx, stepSyncConsulClientCA, wrapped)
		return wrapped
	}
	project, environmentName := documentString(doc, "project"), documentString(doc, "environment")
	for _, namespace := range targets {
		payload, err := json.Marshal(map[string]any{
			"apiVersion": "v1", "kind": "Secret", "type": "Opaque",
			"metadata": map[string]any{
				"name": "consul-client-ca", "namespace": namespace,
				"labels": map[string]string{
					"app.kubernetes.io/managed-by": "ops-deploy-platform", "ops-deploy.io/kind": "consul-client-ca",
					"ops-deploy.io/project": project, "ops-deploy.io/environment": environmentName,
				},
			},
			"data": map[string]string{"tls.crt": secret.Data["tls.crt"]},
		})
		if err != nil {
			jobs.StepFinished(ctx, stepSyncConsulClientCA, err)
			return err
		}
		apply := Command{
			Name: d.config.Tools.Kubectl, Args: []string{"apply", "-f", "-"},
			Dir: d.config.Paths.RepositoryRoot, Env: d.commandEnv(ctx, kubeconfig), Stdin: bytes.NewReader(payload),
		}
		_, _ = fmt.Fprintf(output, "正在同步 Consul CA -> %s/consul-client-ca（CA 正文不会写入日志）\n", namespace)
		runErr := d.executor.Run(ctx, apply, output)
		clear(payload)
		if runErr != nil {
			wrapped := fmt.Errorf("同步 Consul CA 到 Namespace %s 失败: %w", namespace, runErr)
			jobs.StepFinished(ctx, stepSyncConsulClientCA, wrapped)
			return wrapped
		}
	}
	secret.Data["tls.crt"] = ""
	jobs.StepFinished(ctx, stepSyncConsulClientCA, nil)
	return nil
}

func (d *Deployment) cleanupConsulClientCA(ctx context.Context, kubeconfig string, doc environment.Document, output io.Writer) {
	configured, _ := doc["namespaces"].(map[string]any)
	namespaces := make([]string, 0, len(configured))
	for namespace, raw := range configured {
		namespace = strings.TrimSpace(namespace)
		if namespace == "" {
			continue
		}
		if config, ok := raw.(map[string]any); ok && !documentMapBoolDefault(config, "enabled", true) {
			continue
		}
		namespaces = append(namespaces, namespace)
	}
	sort.Strings(namespaces)
	for _, namespace := range namespaces {
		command := Command{
			Name: d.config.Tools.Kubectl,
			Args: []string{"delete", "secret", "consul-client-ca", "--namespace", namespace, "--ignore-not-found=true", "--wait=false"},
			Dir:  d.config.Paths.RepositoryRoot, Env: d.commandEnv(ctx, kubeconfig),
		}
		_, _ = fmt.Fprintf(output, "$ %s delete secret consul-client-ca --namespace %s --ignore-not-found=true --wait=false\n", command.Name, namespace)
		if err := d.executor.Run(ctx, command, output); err != nil {
			// A namespace can disappear in the same update. Stale CA cleanup
			// must not turn an otherwise successful Consul uninstall into a
			// failed deployment.
			_, _ = fmt.Fprintf(output, "清理 %s/consul-client-ca 时 Namespace 已不可用，已忽略：%v\n", namespace, err)
		}
	}
}

func consulClientCANamespaces(doc environment.Document) ([]string, string) {
	consulValue, found := environment.GetPath(doc, "components.consul")
	consul, valid := consulValue.(map[string]any)
	if !found || !valid || !documentMapBoolDefault(consul, "enabled", false) {
		return nil, ""
	}
	source := defaultString(documentMapString(consul, "namespace"), "platform-server")
	configured, _ := doc["namespaces"].(map[string]any)
	targetSet := make(map[string]struct{})
	for namespace, raw := range configured {
		if namespace = strings.TrimSpace(namespace); namespace == "" || namespace == source {
			continue
		}
		if config, ok := raw.(map[string]any); ok && !documentMapBoolDefault(config, "enabled", true) {
			continue
		}
		targetSet[namespace] = struct{}{}
	}
	targets := make([]string, 0, len(targetSet))
	for namespace := range targetSet {
		targets = append(targets, namespace)
	}
	sort.Strings(targets)
	return targets, source
}

type componentPodList struct {
	Items []struct {
		Metadata struct {
			Name      string `json:"name"`
			Namespace string `json:"namespace"`
		} `json:"metadata"`
		Status struct {
			ContainerStatuses []struct {
				Name  string `json:"name"`
				Ready bool   `json:"ready"`
				State struct {
					Waiting *struct {
						Reason string `json:"reason"`
					} `json:"waiting"`
				} `json:"state"`
			} `json:"containerStatuses"`
		} `json:"status"`
	} `json:"items"`
}

type componentPVCList struct {
	Items []struct {
		Metadata struct {
			Name string `json:"name"`
		} `json:"metadata"`
		Spec struct {
			Resources struct {
				Requests map[string]string `json:"requests"`
			} `json:"resources"`
		} `json:"spec"`
		Status struct {
			Capacity map[string]string `json:"capacity"`
		} `json:"status"`
	} `json:"items"`
}

// preflightComponentHealth catches a retained ClickVisual Kafka volume that is
// already full before Terraform starts a long Helm wait. The check is read-only
// and intentionally allows a first install, an unavailable namespace, and a
// saved capacity increase that the upcoming apply can perform.
func (d *Deployment) preflightComponentHealth(ctx context.Context, kubeconfig string, doc environment.Document, output io.Writer) error {
	jobs.StepStarted(ctx, stepPreflightComponentHealth)
	_, _ = fmt.Fprintf(output, "\n==> %s\n", stepPreflightComponentHealth)
	if err := d.preflightHigressNLB(ctx, kubeconfig, doc, output); err != nil {
		wrapped := fmt.Errorf("[HIGRESS_NLB_PREFLIGHT] %w", err)
		jobs.StepFinished(ctx, stepPreflightComponentHealth, wrapped)
		return wrapped
	}
	if failure, err := d.higressGatewaySchedulingFailure(ctx, kubeconfig, doc); err != nil {
		// This is an optimization rather than the source of truth. A transient
		// API/RBAC failure must not reject an otherwise valid deployment; Helm
		// will still perform its normal readiness checks.
		_, _ = fmt.Fprintf(output, "无法完成 Higress 节点容量预检，将继续部署并由 Helm 验证：%v\n", err)
	} else if failure != "" {
		err := errors.New(failure)
		jobs.StepFinished(ctx, stepPreflightComponentHealth, err)
		return err
	}
	if !enabledPath(doc, "components.catalog.clickvisual_stack.enabled") {
		_, _ = fmt.Fprintln(output, "组件调度容量检查通过；ClickVisual 未启用，无需检查 Kafka 持久化存储。")
		jobs.StepFinished(ctx, stepPreflightComponentHealth, nil)
		return nil
	}

	failure, err := d.clickVisualKafkaStorageFailure(ctx, kubeconfig, doc, output)
	if err != nil {
		// A namespace does not exist on a first install, and RBAC/API failures are
		// diagnosed by the normal Terraform path. This optional read-only check
		// must not reject an otherwise valid initial deployment.
		_, _ = fmt.Fprintf(output, "无法完成 ClickVisual 存储预检，将继续部署并由 Helm 验证就绪状态：%v\n", err)
		jobs.StepFinished(ctx, stepPreflightComponentHealth, nil)
		return nil
	}
	if failure != "" {
		err := errors.New(failure)
		jobs.StepFinished(ctx, stepPreflightComponentHealth, err)
		return err
	}
	_, _ = fmt.Fprintln(output, "ClickVisual Kafka 未发现已知的磁盘写满故障，可继续增量部署。")
	jobs.StepFinished(ctx, stepPreflightComponentHealth, nil)
	return nil
}

type higressNLBPermission struct {
	Protocol string `json:"IpProtocol"`
	FromPort *int   `json:"FromPort"`
	ToPort   *int   `json:"ToPort"`
	IPRanges []struct {
		CIDR string `json:"CidrIp"`
	} `json:"IpRanges"`
	IPv6Ranges []struct {
		CIDR string `json:"CidrIpv6"`
	} `json:"Ipv6Ranges"`
	PrefixLists  []json.RawMessage `json:"PrefixListIds"`
	SourceGroups []json.RawMessage `json:"UserIdGroupPairs"`
}

type higressNLBSecurityGroup struct {
	ID          string                 `json:"GroupId"`
	Name        string                 `json:"GroupName"`
	VPCID       string                 `json:"VpcId"`
	Permissions []higressNLBPermission `json:"IpPermissions"`
	Tags        []struct {
		Key   string `json:"Key"`
		Value string `json:"Value"`
	} `json:"Tags"`
}

func higressPermissionAllowsPort(permission higressNLBPermission, port int) (bool, bool) {
	portMatches := permission.Protocol == "-1" || ((permission.Protocol == "tcp" || permission.Protocol == "6") && permission.FromPort != nil && permission.ToPort != nil && *permission.FromPort <= port && *permission.ToPort >= port)
	if !portMatches {
		return false, false
	}
	allowed := len(permission.IPRanges)+len(permission.IPv6Ranges)+len(permission.PrefixLists)+len(permission.SourceGroups) > 0
	public := false
	for _, source := range permission.IPRanges {
		public = public || source.CIDR == "0.0.0.0/0"
	}
	for _, source := range permission.IPv6Ranges {
		public = public || source.CIDR == "::/0"
	}
	return allowed, public
}

func inspectHigressCustomSecurityGroups(groups []higressNLBSecurityGroup, vpcID string) (allowsHTTP, allowsHTTPS, publicHTTP, publicHTTPS bool, err error) {
	for _, group := range groups {
		if group.VPCID != vpcID {
			return false, false, false, false, fmt.Errorf("Higress NLB 安全组 %s 属于 %s，但 EKS 位于 %s；AWS 不允许跨 VPC 绑定安全组", group.ID, group.VPCID, vpcID)
		}
		platformGuard := false
		for _, tag := range group.Tags {
			platformGuard = platformGuard || (tag.Key == "ops-deploy.io/resource" && tag.Value == "nlb-frontend-security-group")
		}
		switch {
		case group.Name == "default":
			return false, false, false, false, fmt.Errorf("Higress NLB 不能使用 VPC 默认安全组 %s；请创建用途独立的入口安全组", group.ID)
		case strings.HasPrefix(group.Name, "eks-cluster-sg-"):
			return false, false, false, false, fmt.Errorf("Higress NLB 不能复用 EKS 集群安全组 %s；否则入口与集群控制面安全边界耦合", group.ID)
		case platformGuard:
			return false, false, false, false, fmt.Errorf("Higress NLB 不能手动复用平台守护安全组 %s；当前环境会自动创建并维护自己的守护安全组", group.ID)
		}
		for _, permission := range group.Permissions {
			if allowed, public := higressPermissionAllowsPort(permission, 80); allowed {
				allowsHTTP, publicHTTP = true, publicHTTP || public
			}
			if allowed, public := higressPermissionAllowsPort(permission, 443); allowed {
				allowsHTTPS, publicHTTPS = true, publicHTTPS || public
			}
		}
	}
	return allowsHTTP, allowsHTTPS, publicHTTP, publicHTTPS, nil
}

// preflightHigressNLB fails before Terraform/Helm mutates the cluster when the
// controller or selected AWS security groups cannot produce a reachable and
// isolated NLB. Terraform repeats the ownership checks as the final authority.
func (d *Deployment) preflightHigressNLB(ctx context.Context, kubeconfig string, doc environment.Document, output io.Writer) error {
	if !enabledPath(doc, "components.catalog.higress.enabled") {
		return nil
	}
	if environment.IsExistingEKS(doc) {
		_, _ = fmt.Fprintln(output, "Higress 使用接入集群现有的 LoadBalancer 控制器；平台不会修改共享集群的 NLB 安全组。")
		return nil
	}

	var controllerOutput bytes.Buffer
	controllerCommand := Command{
		Name: d.config.Tools.Kubectl,
		Args: []string{"-n", "kube-system", "get", "deployment", "aws-load-balancer-controller", "-o", "json"},
		Dir:  d.config.Paths.RepositoryRoot,
		Env:  d.commandEnv(ctx, kubeconfig),
	}
	if err := d.executor.Run(ctx, controllerCommand, &controllerOutput); err != nil {
		return fmt.Errorf("Higress NLB 部署前检查失败：AWS Load Balancer Controller 不存在或不可读取；请先完成阶段1基础组件部署: %w", err)
	}
	var controller struct {
		Spec struct {
			Replicas int `json:"replicas"`
		} `json:"spec"`
		Status struct {
			AvailableReplicas int `json:"availableReplicas"`
		} `json:"status"`
	}
	if err := json.Unmarshal(controllerOutput.Bytes(), &controller); err != nil {
		return fmt.Errorf("解析 AWS Load Balancer Controller 状态失败: %w", err)
	}
	if controller.Spec.Replicas < 1 || controller.Status.AvailableReplicas < 1 {
		return fmt.Errorf("Higress NLB 部署前检查失败：AWS Load Balancer Controller 可用副本为 %d/%d；请先恢复控制器后重试", controller.Status.AvailableReplicas, controller.Spec.Replicas)
	}

	mode := documentString(doc, "components.catalog.higress.nlb.security_group_mode")
	scheme := documentString(doc, "components.catalog.higress.nlb.scheme")
	if mode == "managed" {
		ports, _ := environment.GetPath(doc, "components.catalog.higress.nlb.allowed_ports")
		cidrs := documentStringList(doc, "components.catalog.higress.nlb.allowed_cidrs")
		_, _ = fmt.Fprintf(output, "Higress NLB 预检通过：%s，平台管理入口安全组，开放端口 %v，来源 CIDR %d 条；Load Balancer Controller 可用 %d/%d。\n", scheme, ports, len(cidrs), controller.Status.AvailableReplicas, controller.Spec.Replicas)
		return nil
	}

	securityGroupIDs := documentStringList(doc, "components.catalog.higress.nlb.security_group_ids")
	var clusterPayload struct {
		Cluster struct {
			ResourcesVPCConfig struct {
				VPCID string `json:"vpcId"`
			} `json:"resourcesVpcConfig"`
		} `json:"cluster"`
	}
	if err := d.awsJSON(ctx, doc, &clusterPayload, "eks", "describe-cluster", "--name", environment.ClusterName(doc)); err != nil {
		return fmt.Errorf("Higress NLB 部署前无法读取 EKS VPC: %w", err)
	}
	args := []string{"ec2", "describe-security-groups", "--group-ids"}
	args = append(args, securityGroupIDs...)
	var groupsPayload struct {
		Groups []higressNLBSecurityGroup `json:"SecurityGroups"`
	}
	if err := d.awsJSON(ctx, doc, &groupsPayload, args...); err != nil {
		return fmt.Errorf("Higress NLB 自定义安全组读取失败；请确认 ID、Region 和 ec2:DescribeSecurityGroups 权限: %w", err)
	}
	if len(groupsPayload.Groups) != len(securityGroupIDs) {
		return fmt.Errorf("Higress NLB 配置了 %d 个安全组，但 AWS 只返回 %d 个；本次未执行 Terraform", len(securityGroupIDs), len(groupsPayload.Groups))
	}
	allowsHTTP, allowsHTTPS, publicHTTP, publicHTTPS, err := inspectHigressCustomSecurityGroups(groupsPayload.Groups, clusterPayload.Cluster.ResourcesVPCConfig.VPCID)
	if err != nil {
		return err
	}
	if mode == "custom" && !allowsHTTP && !allowsHTTPS {
		return errors.New("Higress NLB 自定义安全组没有允许 TCP 80 或 443 的入站来源；请先补充至少一个入口规则，本次未执行 Terraform")
	}
	if publicHTTP || publicHTTPS {
		if scheme == "internal" {
			_, _ = fmt.Fprintf(output, "[安全提醒] 自定义安全组来源为 0.0.0.0/0：HTTP=%t，HTTPS=%t；内网 NLB 仍仅在其网络可达范围内生效，多个安全组的权限会取并集。\n", publicHTTP, publicHTTPS)
		} else {
			_, _ = fmt.Fprintf(output, "[安全提醒] 自定义安全组允许全互联网访问：HTTP=%t，HTTPS=%t；多个安全组的权限会取并集。\n", publicHTTP, publicHTTPS)
		}
	}
	manageBackend, _ := environment.GetPath(doc, "components.catalog.higress.nlb.manage_backend_security_group_rules")
	_, _ = fmt.Fprintf(output, "Higress NLB 自定义安全组预检通过：%s，%d 个安全组均属于 %s，HTTP=%t，HTTPS=%t，自动维护后端规则=%t。\n", scheme, len(groupsPayload.Groups), clusterPayload.Cluster.ResourcesVPCConfig.VPCID, allowsHTTP, allowsHTTPS, manageBackend == true)
	return nil
}

type higressGatewayNodeList struct {
	Items []struct {
		Metadata struct {
			Name string `json:"name"`
		} `json:"metadata"`
		Status struct {
			Allocatable map[string]string `json:"allocatable"`
			Conditions  []struct {
				Type   string `json:"type"`
				Status string `json:"status"`
			} `json:"conditions"`
		} `json:"status"`
	} `json:"items"`
}

// higressGatewaySchedulingFailure prevents Helm from waiting its full timeout
// for a Pod that can never fit on the dedicated gateway pool. EKS reserves a
// small part of every node, therefore a 2-core instance exposes less than
// 2000m allocatable CPU and cannot accept a Pod requesting two full cores.
func (d *Deployment) higressGatewaySchedulingFailure(ctx context.Context, kubeconfig string, doc environment.Document) (string, error) {
	if environment.IsExistingEKS(doc) || !enabledPath(doc, "components.catalog.higress.enabled") || !enabledPath(doc, "eks.workload_scheduling.enabled") {
		return "", nil
	}
	request := documentString(doc, "components.catalog.higress.values.higress-core.gateway.resources.requests.cpu")
	if request == "" {
		return "", nil
	}
	requestMilli, err := cpuMilliValue(request)
	if err != nil {
		return "", fmt.Errorf("Higress Gateway 请求 CPU %q 不合法: %w", request, err)
	}
	var payload bytes.Buffer
	if err := d.executor.Run(ctx, Command{
		Name: d.config.Tools.Kubectl,
		Args: []string{"get", "nodes", "-l", "workload-class=gateway", "-o", "json"},
		Dir:  d.config.Paths.RepositoryRoot,
		Env:  d.commandEnv(ctx, kubeconfig),
	}, &payload); err != nil {
		return "", err
	}
	var nodes higressGatewayNodeList
	if err := json.Unmarshal(payload.Bytes(), &nodes); err != nil {
		return "", fmt.Errorf("解析 Higress 专用节点容量: %w", err)
	}
	maxAllocatable := int64(0)
	readyNodes := 0
	for _, node := range nodes.Items {
		ready := false
		for _, condition := range node.Status.Conditions {
			if condition.Type == "Ready" && strings.EqualFold(condition.Status, "True") {
				ready = true
				break
			}
		}
		if !ready {
			continue
		}
		readyNodes++
		value, parseErr := cpuMilliValue(node.Status.Allocatable["cpu"])
		if parseErr != nil {
			return "", fmt.Errorf("解析节点 %s 可分配 CPU: %w", node.Metadata.Name, parseErr)
		}
		if value > maxAllocatable {
			maxAllocatable = value
		}
	}
	if readyNodes == 0 {
		return "Higress 已启用，但 workload-class=gateway 专用节点组没有 Ready 节点；平台已在 Helm 执行前停止，避免等待 20 分钟。请先完成阶段1节点容量部署后重试阶段2", nil
	}
	if requestMilli > maxAllocatable {
		return fmt.Sprintf("Higress Gateway 请求 CPU 为 %dm，但专用节点单节点最多仅可分配 %dm；该 Pod 永远无法调度，平台已在 Helm 执行前停止，避免等待 20 分钟。请降低 Higress 请求 CPU，或把 ingress-gateway 节点规格升级为更多 vCPU 后重试阶段2", requestMilli, maxAllocatable), nil
	}
	return "", nil
}

func cpuMilliValue(value string) (int64, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0, errors.New("CPU 数量为空")
	}
	if strings.HasSuffix(value, "m") {
		parsed, err := strconv.ParseInt(strings.TrimSuffix(value, "m"), 10, 64)
		if err != nil || parsed <= 0 {
			return 0, errors.New("必须是正整数 millicores")
		}
		return parsed, nil
	}
	parsed, err := strconv.ParseFloat(value, 64)
	if err != nil || parsed <= 0 {
		return 0, errors.New("必须是正数 CPU cores")
	}
	return int64(parsed * 1000), nil
}

// clickVisualKafkaStorageFailure returns a direct operator-facing error only
// when a failing Kafka container contains the definitive filesystem message.
// Generic CrashLoopBackOff states are left to Helm because they may recover.
func (d *Deployment) clickVisualKafkaStorageFailure(ctx context.Context, kubeconfig string, doc environment.Document, output io.Writer) (string, error) {
	namespace := documentString(doc, "components.catalog.clickvisual_stack.values.namespace")
	if namespace == "" {
		namespace = documentString(doc, "components.catalog.clickvisual_stack.namespace")
	}
	if namespace == "" {
		return "", nil
	}

	var podsPayload bytes.Buffer
	podsCommand := Command{
		Name: d.config.Tools.Kubectl,
		Args: []string{"get", "pods", "--namespace", namespace, "--selector", "ops-deploy.io/stack=clickvisual,ops-deploy.io/component=kafka", "--output", "json"},
		Dir:  d.config.Paths.RepositoryRoot,
		Env:  d.commandEnv(ctx, kubeconfig),
	}
	if err := d.executor.Run(ctx, podsCommand, &podsPayload); err != nil {
		return "", err
	}
	var pods componentPodList
	if err := json.Unmarshal(podsPayload.Bytes(), &pods); err != nil {
		return "", fmt.Errorf("解析 ClickVisual Kafka Pod 状态: %w", err)
	}

	failingPod := ""
	for _, pod := range pods.Items {
		for _, container := range pod.Status.ContainerStatuses {
			if container.Ready || container.State.Waiting == nil {
				continue
			}
			reason := strings.ToLower(strings.TrimSpace(container.State.Waiting.Reason))
			if reason != "crashloopbackoff" && reason != "error" {
				continue
			}
			for _, previous := range []bool{true, false} {
				args := []string{"logs", pod.Metadata.Name, "--namespace", namespace, "--container", container.Name, "--tail=120"}
				if previous {
					args = append(args, "--previous")
				}
				var logs bytes.Buffer
				if err := d.executor.Run(ctx, Command{Name: d.config.Tools.Kubectl, Args: args, Dir: d.config.Paths.RepositoryRoot, Env: d.commandEnv(ctx, kubeconfig)}, &logs); err != nil {
					continue
				}
				if strings.Contains(strings.ToLower(logs.String()), "no space left on device") {
					failingPod = pod.Metadata.Name
					break
				}
			}
			if failingPod != "" {
				break
			}
		}
		if failingPod != "" {
			break
		}
	}
	if failingPod == "" {
		return "", nil
	}

	var pvcPayload bytes.Buffer
	pvcCommand := Command{
		Name: d.config.Tools.Kubectl,
		Args: []string{"get", "persistentvolumeclaim", "--namespace", namespace, "--selector", "ops-deploy.io/stack=clickvisual,ops-deploy.io/component=kafka", "--output", "json"},
		Dir:  d.config.Paths.RepositoryRoot,
		Env:  d.commandEnv(ctx, kubeconfig),
	}
	if err := d.executor.Run(ctx, pvcCommand, &pvcPayload); err != nil {
		return fmt.Sprintf("ClickVisual Kafka 容器 %s/%s 因数据盘写满无法启动（No space left on device）；请先在“ClickVisual 磁盘与容量”扩容 Kafka PVC，再重试阶段2", namespace, failingPod), nil
	}
	var claims componentPVCList
	if err := json.Unmarshal(pvcPayload.Bytes(), &claims); err != nil {
		return "", fmt.Errorf("解析 ClickVisual Kafka PVC 状态: %w", err)
	}
	desired := documentString(doc, "components.catalog.clickvisual_stack.values.kafka.storage.size")
	desiredBytes, desiredValid := kubernetesStorageBytes(desired)
	maxCapacityBytes := int64(0)
	claimSummaries := make([]string, 0, len(claims.Items))
	for _, claim := range claims.Items {
		capacity := claim.Status.Capacity["storage"]
		capacityBytes, _ := kubernetesStorageBytes(capacity)
		if capacityBytes > maxCapacityBytes {
			maxCapacityBytes = capacityBytes
		}
		claimSummaries = append(claimSummaries, fmt.Sprintf("%s=%s", claim.Metadata.Name, defaultString(capacity, claim.Spec.Resources.Requests["storage"])))
	}
	if desiredValid && desiredBytes > maxCapacityBytes {
		_, _ = fmt.Fprintf(output, "检测到 Kafka 旧 Pod %s/%s 曾因磁盘写满退出，但已保存的目标容量 %s 大于当前 PVC 容量；本次 Apply 将先扩容并继续就绪验证。\n", namespace, failingPod, desired)
		return "", nil
	}
	return fmt.Sprintf("ClickVisual Kafka 容器 %s/%s 因数据盘写满无法启动（No space left on device）；Kafka PVC：%s，平台配置：%s。请先在“ClickVisual 磁盘与容量”扩容 Kafka PVC，再重试阶段2", namespace, failingPod, strings.Join(claimSummaries, "、"), defaultString(desired, "未填写")), nil
}

// emitComponentFailureDiagnostics preserves the most useful Kubernetes
// evidence in the job log while the failure is still fresh. Terraform's Helm
// provider normally returns only "context deadline exceeded" after waiting;
// Pod readiness and Warning events explain whether the real cause is image
// pull, scheduling, PVC binding, CrashLoopBackOff or a failed probe.
func (d *Deployment) emitComponentFailureDiagnostics(ctx context.Context, kubeconfig string, doc environment.Document, output io.Writer) string {
	_, _ = fmt.Fprintln(output, "\n==> 组件失败现场诊断（只读）")
	storageFailure, storageErr := d.clickVisualKafkaStorageFailure(ctx, kubeconfig, doc, output)
	if storageErr != nil {
		_, _ = fmt.Fprintf(output, "ClickVisual Kafka 存储诊断失败（不覆盖原始错误）：%v\n", storageErr)
	} else if storageFailure != "" {
		_, _ = fmt.Fprintf(output, "[明确原因] %s\n", storageFailure)
	}
	commands := []Command{
		{
			Name: d.config.Tools.Kubectl,
			Args: []string{"get", "pods", "-A", "-o", "wide"},
			Dir:  d.config.Paths.RepositoryRoot,
			Env:  d.commandEnv(ctx, kubeconfig),
		},
		{
			Name: d.config.Tools.Kubectl,
			Args: []string{"get", "events", "-A", "--field-selector=type=Warning", "--sort-by=.lastTimestamp"},
			Dir:  d.config.Paths.RepositoryRoot,
			Env:  d.commandEnv(ctx, kubeconfig),
		},
		{
			Name: d.config.Tools.Kubectl,
			Args: []string{"get", "configmap", "cluster-autoscaler-status", "--namespace", "kube-system", "--output", "yaml"},
			Dir:  d.config.Paths.RepositoryRoot,
			Env:  d.commandEnv(ctx, kubeconfig),
		},
		{
			Name: d.config.Tools.Kubectl,
			Args: []string{"logs", "--namespace", "kube-system", "--selector", "app.kubernetes.io/instance=cluster-autoscaler", "--tail", "100", "--prefix=true"},
			Dir:  d.config.Paths.RepositoryRoot,
			Env:  d.commandEnv(ctx, kubeconfig),
		},
	}
	for _, command := range commands {
		_, _ = fmt.Fprintf(output, "$ %s %s\n", command.Name, strings.Join(command.Args, " "))
		if err := d.executor.Run(ctx, command, output); err != nil {
			_, _ = fmt.Fprintf(output, "现场诊断命令执行失败（不覆盖原始错误）：%v\n", err)
		}
	}
	return storageFailure
}

type lokiLabelValuesResponse struct {
	Status string   `json:"status"`
	Data   []string `json:"data"`
}

func (d *Deployment) verifyLoggingStack(ctx context.Context, kubeconfig string, doc environment.Document, output io.Writer) error {
	lokiValue, lokiFound := environment.GetPath(doc, "components.catalog.loki")
	loki, lokiValid := lokiValue.(map[string]any)
	if !lokiFound || !lokiValid || !documentMapBoolDefault(loki, "enabled", false) {
		return nil
	}
	prometheusValue, prometheusFound := environment.GetPath(doc, "components.catalog.prometheus")
	prometheus, prometheusValid := prometheusValue.(map[string]any)
	if !prometheusFound || !prometheusValid || !documentMapBoolDefault(prometheus, "enabled", false) {
		return errors.New("Loki 已启用但 Prometheus + Grafana 未启用，无法验证日志查询链路")
	}

	lokiNamespace := defaultString(documentMapString(loki, "namespace"), "monitoring")
	lokiService := defaultString(documentMapString(loki, "service_name"), "loki-gateway")
	lokiPort := documentMapIntDefault(loki, "service_port", 80)
	grafanaNamespace := defaultString(documentMapString(prometheus, "namespace"), "monitoring")
	grafanaWorkload := defaultString(documentMapString(prometheus, "service_name"), "prometheus-grafana")

	if err := d.step(ctx, output, stepVerifyLogCollector, Command{
		Name: d.config.Tools.Kubectl,
		Args: []string{"-n", lokiNamespace, "rollout", "status", "deployment/loki-alloy", "--timeout=180s"},
		Dir:  d.config.Paths.RepositoryRoot,
		Env:  d.commandEnv(ctx, kubeconfig),
	}); err != nil {
		return err
	}
	if err := d.waitForLokiEnvironmentLabel(ctx, kubeconfig, lokiNamespace, lokiService, lokiPort, documentString(doc, "environment"), output); err != nil {
		return err
	}
	return d.step(ctx, output, stepVerifyGrafanaLoki, Command{
		Name: d.config.Tools.Kubectl,
		Args: []string{
			"-n", grafanaNamespace, "exec", "deployment/" + grafanaWorkload, "-c", "grafana", "--",
			"sh", "-ec", `test -s /etc/grafana/provisioning/datasources/loki-datasource.yaml && grep -q 'uid.*loki' /etc/grafana/provisioning/datasources/loki-datasource.yaml`,
		},
		Dir: d.config.Paths.RepositoryRoot,
		Env: d.commandEnv(ctx, kubeconfig),
	})
}

func (d *Deployment) verifyGrafanaDashboards(ctx context.Context, kubeconfig string, doc environment.Document, output io.Writer) error {
	prometheusValue, prometheusFound := environment.GetPath(doc, "components.catalog.prometheus")
	prometheus, prometheusValid := prometheusValue.(map[string]any)
	if !prometheusFound || !prometheusValid || !documentMapBoolDefault(prometheus, "enabled", false) {
		return nil
	}

	grafanaNamespace := defaultString(documentMapString(prometheus, "namespace"), "monitoring")
	grafanaWorkload := defaultString(documentMapString(prometheus, "service_name"), "prometheus-grafana")
	checks := []string{
		`test -s /tmp/dashboards/ops-deploy-eks-core.json`,
		`grep -Fq 'ops-eks-core' /tmp/dashboards/ops-deploy-eks-core.json`,
	}
	if enabledPath(doc, "components.catalog.loki.enabled") {
		checks = append(checks,
			`test -s /tmp/dashboards/ops-deploy-cluster-logs.json`,
			`grep -Fq 'ops-cluster-logs' /tmp/dashboards/ops-deploy-cluster-logs.json`,
		)
	}
	condition := strings.Join(checks, " && ")
	verifyScript := fmt.Sprintf(`attempt=0; until %s; do attempt=$((attempt + 1)); if [ "$attempt" -ge 30 ]; then echo "Grafana Dashboard sidecar 在 60 秒内未完成配置同步" >&2; exit 1; fi; sleep 2; done`, condition)
	return d.step(ctx, output, stepVerifyGrafanaDashboards, Command{
		Name: d.config.Tools.Kubectl,
		Args: []string{
			"-n", grafanaNamespace, "exec", "deployment/" + grafanaWorkload, "-c", "grafana", "--",
			"sh", "-ec", verifyScript,
		},
		Dir: d.config.Paths.RepositoryRoot,
		Env: d.commandEnv(ctx, kubeconfig),
	})
}

func (d *Deployment) waitForLokiEnvironmentLabel(ctx context.Context, kubeconfig, namespace, service string, port int, environmentName string, output io.Writer) error {
	const attempts = 12
	labelURL := fmt.Sprintf("/api/v1/namespaces/%s/services/http:%s:%d/proxy/loki/api/v1/label/environment/values", namespace, service, port)
	jobs.StepStarted(ctx, stepVerifyLokiIngestion)
	_, _ = fmt.Fprintf(output, "\n==> %s\n", stepVerifyLokiIngestion)
	_, _ = fmt.Fprintf(output, "$ %s get --raw %s（最多等待 60 秒，不输出业务日志正文）\n", d.config.Tools.Kubectl, labelURL)
	var lastErr error
	for attempt := 1; attempt <= attempts; attempt++ {
		var response bytes.Buffer
		lastErr = d.executor.Run(ctx, Command{
			Name: d.config.Tools.Kubectl,
			Args: []string{"get", "--raw", labelURL},
			Dir:  d.config.Paths.RepositoryRoot,
			Env:  d.commandEnv(ctx, kubeconfig),
		}, &response)
		if lastErr == nil {
			var payload lokiLabelValuesResponse
			if err := json.Unmarshal(response.Bytes(), &payload); err != nil {
				lastErr = fmt.Errorf("Loki 查询返回无法解析: %w", err)
			} else if payload.Status != "success" {
				lastErr = fmt.Errorf("Loki 查询状态为 %q", payload.Status)
			} else if slicesContain(payload.Data, environmentName) {
				_, _ = fmt.Fprintf(output, "Loki 已收到当前环境 %s 的集群日志，写入与查询链路正常。\n", environmentName)
				jobs.StepFinished(ctx, stepVerifyLokiIngestion, nil)
				return nil
			} else {
				lastErr = fmt.Errorf("Loki 尚未出现 environment=%s 标签", environmentName)
			}
		}
		if attempt < attempts {
			_, _ = fmt.Fprintf(output, "等待 Alloy 首批日志写入 Loki（%d/%d）…\n", attempt, attempts)
			select {
			case <-ctx.Done():
				lastErr = ctx.Err()
				attempt = attempts
			case <-time.After(5 * time.Second):
			}
		}
	}
	err := fmt.Errorf("%s: %w；请检查 loki-alloy 日志、loki-gateway Service 和网络策略", stepVerifyLokiIngestion, lastErr)
	jobs.StepFinished(ctx, stepVerifyLokiIngestion, err)
	return err
}

func slicesContain(items []string, wanted string) bool {
	for _, item := range items {
		if item == wanted {
			return true
		}
	}
	return false
}

type deployedAlertingObjects struct {
	Channels  []any
	Templates []any
}

type namedAlertingConfig struct {
	Name      string
	Type      string
	Signature string
}

// logAlertingChangeSummary makes stage-2 changes explicit without printing
// webhook URLs, credential references or template bodies into deployment logs.
// This is diagnostic-only: an unavailable Kubernetes API must not block the
// Terraform apply that can create the namespace and alerting objects.
func (d *Deployment) logAlertingChangeSummary(ctx context.Context, kubeconfig string, doc environment.Document, output io.Writer) {
	alerting, ok := doc["alerting"].(map[string]any)
	if !ok {
		return
	}
	enabled := documentMapBoolDefault(alerting, "enabled", false)
	namespace := strings.TrimSpace(documentMapString(alerting, "namespace"))
	if namespace == "" {
		namespace = "monitoring"
	}
	desiredChannels := namedAlertingConfigs(alerting["channels"], true)
	desiredTemplates := namedAlertingConfigs(alerting["templates"], false)
	current := d.readDeployedAlertingObjects(ctx, kubeconfig, namespace)
	currentChannels := namedAlertingConfigs(current.Channels, true)
	currentTemplates := namedAlertingConfigs(current.Templates, false)

	_, _ = fmt.Fprintln(output, "\n==> 告警配置变更摘要")
	if !enabled {
		_, _ = fmt.Fprintln(output, "[告警中心] 已关闭，本次不会启用通道或模板。")
		if len(currentChannels) > 0 || len(currentTemplates) > 0 {
			_, _ = fmt.Fprintln(output, "[本次待删除] 集群内已部署的告警通道 Secret 与模板 ConfigMap。")
		}
		if len(desiredChannels) > 0 {
			_, _ = fmt.Fprintf(output, "[尚未生效] 已保存 %d 个通道；开启告警中心后才会部署。\n", len(desiredChannels))
		}
		return
	}

	_, _ = fmt.Fprintf(output, "[目标位置] Namespace %s\n", namespace)
	writeAlertingDiff(output, "通道", desiredChannels, currentChannels)
	writeAlertingDiff(output, "模板", desiredTemplates, currentTemplates)
}

func (d *Deployment) readDeployedAlertingObjects(ctx context.Context, kubeconfig, namespace string) deployedAlertingObjects {
	result := deployedAlertingObjects{}
	var secretOutput bytes.Buffer
	if err := d.executor.Run(ctx, Command{
		Name: d.config.Tools.Kubectl,
		Args: []string{"-n", namespace, "get", "secret", "platform-alerting-channels", "-o", "json"},
		Dir:  d.config.Paths.RepositoryRoot,
		Env:  d.commandEnv(ctx, kubeconfig),
	}, &secretOutput); err == nil {
		var secret struct {
			Data map[string]string `json:"data"`
		}
		if json.Unmarshal(secretOutput.Bytes(), &secret) == nil {
			if raw, err := base64.StdEncoding.DecodeString(secret.Data["channels.json"]); err == nil {
				_ = json.Unmarshal(raw, &result.Channels)
			}
		}
	}

	var configMapOutput bytes.Buffer
	if err := d.executor.Run(ctx, Command{
		Name: d.config.Tools.Kubectl,
		Args: []string{"-n", namespace, "get", "configmap", "platform-alerting-catalog", "-o", "json"},
		Dir:  d.config.Paths.RepositoryRoot,
		Env:  d.commandEnv(ctx, kubeconfig),
	}, &configMapOutput); err == nil {
		var configMap struct {
			Data map[string]string `json:"data"`
		}
		if json.Unmarshal(configMapOutput.Bytes(), &configMap) == nil {
			_ = json.Unmarshal([]byte(configMap.Data["templates.json"]), &result.Templates)
		}
	}
	return result
}

func namedAlertingConfigs(raw any, includeType bool) map[string]namedAlertingConfig {
	items, ok := raw.([]any)
	if !ok {
		return map[string]namedAlertingConfig{}
	}
	result := make(map[string]namedAlertingConfig, len(items))
	for _, item := range items {
		value, ok := item.(map[string]any)
		if !ok {
			continue
		}
		name := strings.TrimSpace(documentMapString(value, "name"))
		if name == "" {
			continue
		}
		encoded, err := json.Marshal(value)
		if err != nil {
			continue
		}
		kind := ""
		if includeType {
			kind = strings.TrimSpace(documentMapString(value, "type"))
		}
		result[name] = namedAlertingConfig{Name: name, Type: kind, Signature: fmt.Sprintf("%x", sha256.Sum256(encoded))}
	}
	return result
}

func writeAlertingDiff(output io.Writer, label string, desired, current map[string]namedAlertingConfig) {
	added, updated, unchanged, removed := []string{}, []string{}, []string{}, []string{}
	for name, desiredItem := range desired {
		currentItem, found := current[name]
		display := alertingConfigDisplay(desiredItem)
		switch {
		case !found:
			added = append(added, display)
		case currentItem.Signature != desiredItem.Signature:
			updated = append(updated, display)
		default:
			unchanged = append(unchanged, display)
		}
	}
	for name, currentItem := range current {
		if _, found := desired[name]; !found {
			removed = append(removed, alertingConfigDisplay(currentItem))
		}
	}
	for _, values := range [][]string{added, updated, unchanged, removed} {
		sort.Strings(values)
	}
	if len(added) > 0 {
		_, _ = fmt.Fprintf(output, "[新增%s] %s\n", label, strings.Join(added, "、"))
	}
	if len(updated) > 0 {
		_, _ = fmt.Fprintf(output, "[更新%s] %s\n", label, strings.Join(updated, "、"))
	}
	if len(removed) > 0 {
		_, _ = fmt.Fprintf(output, "[删除%s] %s\n", label, strings.Join(removed, "、"))
	}
	if len(unchanged) > 0 {
		_, _ = fmt.Fprintf(output, "[未变更%s] %d 项\n", label, len(unchanged))
	}
	if len(added)+len(updated)+len(unchanged)+len(removed) == 0 {
		_, _ = fmt.Fprintf(output, "[%s] 未配置\n", label)
	}
}

func alertingConfigDisplay(value namedAlertingConfig) string {
	if value.Type == "" {
		return value.Name
	}
	return fmt.Sprintf("%s（%s）", value.Name, value.Type)
}

func (d *Deployment) applyAlertmanagerRelay(ctx context.Context, kubeconfig, target string, doc environment.Document, output io.Writer) error {
	jobs.StepStarted(ctx, stepConfigureAlerting)
	_, _ = fmt.Fprintf(output, "\n==> %s\n", stepConfigureAlerting)
	alerting, _ := doc["alerting"].(map[string]any)
	prometheus := prometheusComponentConfig(doc)
	namespace := strings.TrimSpace(documentMapString(prometheus, "namespace"))
	if namespace == "" {
		namespace = strings.TrimSpace(documentMapString(alerting, "namespace"))
	}
	if namespace == "" {
		namespace = "monitoring"
	}
	alertingEnabled := documentMapBoolDefault(alerting, "enabled", false)
	prometheusEnabled := documentMapBoolDefault(prometheus, "enabled", false)
	if !alertingEnabled || !prometheusEnabled {
		d.cleanupAlertmanagerRelay(ctx, kubeconfig, namespace, output)
		reason := "告警中心未启用"
		if alertingEnabled && !prometheusEnabled {
			reason = "Prometheus + Alertmanager 组件未启用"
		}
		_, _ = fmt.Fprintf(output, "%s，不创建自动告警路由；已清理该环境可能遗留的路由凭据。\n", reason)
		jobs.StepFinished(ctx, stepConfigureAlerting, nil)
		return nil
	}
	if len(namedAlertingConfigs(alerting["channels"], true)) == 0 {
		err := errors.New("告警中心已启用，但没有配置任何告警通道")
		jobs.StepFinished(ctx, stepConfigureAlerting, err)
		return err
	}
	externalOrigin := strings.TrimSuffix(strings.TrimSpace(d.config.Security.ExternalOrigin), "/")
	if externalOrigin == "" || !strings.HasPrefix(strings.ToLower(externalOrigin), "https://") {
		err := errors.New("告警中心自动路由需要平台配置 security.external_origin HTTPS 地址")
		jobs.StepFinished(ctx, stepConfigureAlerting, err)
		return err
	}
	if !d.alertmanagerConfigCRDAvailable(ctx, kubeconfig) {
		err := errors.New("Prometheus 已启用，但集群缺少 AlertmanagerConfig CRD；请检查 kube-prometheus-stack 是否安装成功")
		jobs.StepFinished(ctx, stepConfigureAlerting, err)
		return err
	}
	token, err := alertingrelay.DeriveToken(d.config.CredentialKey(), target)
	if err != nil {
		wrapped := fmt.Errorf("生成环境级告警路由凭据: %w", err)
		jobs.StepFinished(ctx, stepConfigureAlerting, wrapped)
		return wrapped
	}
	route := map[string]any{
		"receiver": "ops-deploy-platform-relay", "groupBy": []string{"alertname", "namespace", "severity"},
		"groupWait": "30s", "groupInterval": "5m", "repeatInterval": "4h",
	}
	// Watchdog is the kube-prometheus-stack dead-man-switch and
	// AlertmanagerFailedToSendAlerts is an alerting-pipeline health signal.
	// Both must remain visible in Alertmanager for operators, but forwarding
	// either to ordinary business channels creates noise (or a feedback loop).
	// Managed relay routes explicitly exclude them because they use
	// continue=true when the Operator merges AlertmanagerConfig objects.
	matchers := []any{map[string]string{
		"name": "alertname", "matchType": "!~", "value": "^(Watchdog|AlertmanagerFailedToSendAlerts)$",
	}}
	// A managed EKS cluster belongs to one environment, so cluster-level alerts
	// are intentionally included. Existing/shared EKS environments are limited
	// to their platform-generated namespace prefix to avoid cross-project alert
	// disclosure.
	if environment.IsExistingEKS(doc) {
		deploymentTarget, _ := doc["deployment_target"].(map[string]any)
		if prefix := strings.TrimSpace(documentMapString(deploymentTarget, "namespace_prefix")); prefix != "" {
			matchers = append(matchers, map[string]string{
				"name": "namespace", "matchType": "=~", "value": "^" + prefix + "-.*$",
			})
		}
	}
	route["matchers"] = matchers
	manifest := map[string]any{
		"apiVersion": "v1", "kind": "List",
		"items": []any{
			map[string]any{
				"apiVersion": "v1", "kind": "Secret",
				"metadata": relayObjectMetadata(namespace, doc),
				"type":     "Opaque",
				"stringData": map[string]string{
					"token": token,
				},
			},
			map[string]any{
				"apiVersion": "monitoring.coreos.com/v1alpha1", "kind": "AlertmanagerConfig",
				"metadata": relayObjectMetadata(namespace, doc),
				"spec": map[string]any{
					"route": route,
					"receivers": []any{map[string]any{
						"name": "ops-deploy-platform-relay",
						"webhookConfigs": []any{map[string]any{
							"url": externalOrigin + "/api/internal/alerting/relay/" + target, "sendResolved": true,
							"httpConfig": map[string]any{"authorization": map[string]any{
								"type": "Bearer", "credentials": map[string]string{"name": "platform-alert-relay", "key": "token"},
							}},
						}},
					}},
				},
			},
		},
	}
	payload, err := json.Marshal(manifest)
	token = ""
	if err != nil {
		jobs.StepFinished(ctx, stepConfigureAlerting, err)
		return err
	}
	_, _ = fmt.Fprintf(output, "正在连接 Alertmanager -> 平台安全中继 -> %d 个告警通道（路由令牌和通道地址不会写入日志）。\n", len(namedAlertingConfigs(alerting["channels"], true)))
	command := Command{
		Name: d.config.Tools.Kubectl, Args: []string{"apply", "-f", "-"},
		Dir: d.config.Paths.RepositoryRoot, Env: d.commandEnv(ctx, kubeconfig), Stdin: bytes.NewReader(payload),
	}
	_, _ = fmt.Fprintf(output, "$ %s %s\n", command.Name, strings.Join(command.Args, " "))
	runErr := d.executor.Run(ctx, command, output)
	clear(payload)
	if runErr != nil {
		wrapped := fmt.Errorf("应用 Alertmanager 告警路由失败: %w", runErr)
		jobs.StepFinished(ctx, stepConfigureAlerting, wrapped)
		return wrapped
	}
	_, _ = fmt.Fprintln(output, "Alertmanager 自动告警路由已生效，触发与恢复消息都会发送到平台配置的通道。")
	jobs.StepFinished(ctx, stepConfigureAlerting, nil)
	return nil
}

func prometheusComponentConfig(doc environment.Document) map[string]any {
	components, _ := doc["components"].(map[string]any)
	catalog, _ := components["catalog"].(map[string]any)
	prometheus, _ := catalog["prometheus"].(map[string]any)
	return prometheus
}

func relayObjectMetadata(namespace string, doc environment.Document) map[string]any {
	return map[string]any{
		"name": "platform-alert-relay", "namespace": namespace,
		"labels": map[string]string{
			"app.kubernetes.io/managed-by": "ops-deploy-platform",
			"ops-deploy.io/managed":        "true",
			"ops-deploy.io/project":        documentString(doc, "project"),
			"ops-deploy.io/environment":    documentString(doc, "environment"),
		},
	}
}

func (d *Deployment) alertmanagerConfigCRDAvailable(ctx context.Context, kubeconfig string) bool {
	var result bytes.Buffer
	err := d.executor.Run(ctx, Command{
		Name: d.config.Tools.Kubectl, Args: []string{"get", "crd", "alertmanagerconfigs.monitoring.coreos.com", "-o", "name"},
		Dir: d.config.Paths.RepositoryRoot, Env: d.commandEnv(ctx, kubeconfig),
	}, &result)
	return err == nil && strings.TrimSpace(result.String()) != ""
}

func (d *Deployment) cleanupAlertmanagerRelay(ctx context.Context, kubeconfig, namespace string, output io.Writer) {
	commands := [][]string{{"-n", namespace, "delete", "secret", "platform-alert-relay", "--ignore-not-found=true"}}
	if d.alertmanagerConfigCRDAvailable(ctx, kubeconfig) {
		commands = append(commands, []string{"-n", namespace, "delete", "alertmanagerconfig", "platform-alert-relay", "--ignore-not-found=true"})
	}
	for _, args := range commands {
		var result bytes.Buffer
		if err := d.executor.Run(ctx, Command{
			Name: d.config.Tools.Kubectl, Args: args,
			Dir: d.config.Paths.RepositoryRoot, Env: d.commandEnv(ctx, kubeconfig),
		}, &result); err == nil && strings.TrimSpace(result.String()) != "" {
			_, _ = io.Copy(output, &result)
		}
	}
}

type kubernetesLoadBalancerService struct {
	Status struct {
		LoadBalancer struct {
			Ingress []struct {
				Hostname string `json:"hostname"`
				IP       string `json:"ip"`
			} `json:"ingress"`
		} `json:"loadBalancer"`
	} `json:"status"`
}

type kubernetesIngressList struct {
	Items []struct {
		Metadata struct {
			Name        string            `json:"name"`
			Namespace   string            `json:"namespace"`
			Annotations map[string]string `json:"annotations"`
		} `json:"metadata"`
		Spec struct {
			IngressClassName string `json:"ingressClassName"`
		} `json:"spec"`
	} `json:"items"`
}

// syncHigressGatewayAddress closes a gap in Higress' status reconciliation on
// AWS: the gateway Service can already own a healthy NLB hostname while the
// Ingress status remains empty. Kubernetes clients and this platform use the
// Ingress status as the canonical address, so phase 2 waits for the Service
// allocation and copies it through the status subresource.
func (d *Deployment) syncHigressGatewayAddress(ctx context.Context, kubeconfig string, doc environment.Document, output io.Writer) error {
	jobs.StepStarted(ctx, stepSyncGatewayAddress)
	_, _ = fmt.Fprintf(output, "\n==> %s\n", stepSyncGatewayAddress)
	higressValue, found := environment.GetPath(doc, "components.catalog.higress")
	higress, valid := higressValue.(map[string]any)
	if !found || !valid || !documentMapBoolDefault(higress, "enabled", false) {
		_, _ = fmt.Fprintln(output, "Higress 未启用，无需同步 LoadBalancer 地址。")
		jobs.StepFinished(ctx, stepSyncGatewayAddress, nil)
		return nil
	}
	namespace := strings.TrimSpace(documentMapString(higress, "namespace"))
	if namespace == "" {
		namespace = "higress-system"
	}
	serviceName := strings.TrimSpace(documentMapString(higress, "public_service_name"))
	if serviceName == "" {
		serviceName = "higress-gateway"
	}
	allowedNamespaces := map[string]bool{namespace: true}
	if rawDomains, ok := doc["domains"].([]any); ok {
		for _, rawDomain := range rawDomains {
			domain, ok := rawDomain.(map[string]any)
			if !ok || !documentMapBoolDefault(domain, "enabled", true) || !strings.EqualFold(strings.TrimSpace(documentMapString(domain, "gateway")), "higress") {
				continue
			}
			if value := strings.TrimSpace(documentMapString(domain, "namespace")); value != "" {
				allowedNamespaces[value] = true
			}
		}
	}

	waitCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	var address, addressKind string
	for {
		payload, err := d.captureCommand(waitCtx, Command{
			Name: d.config.Tools.Kubectl,
			Args: []string{"-n", namespace, "get", "service", serviceName, "-o", "json"},
			Dir:  d.config.Paths.RepositoryRoot, Env: d.commandEnv(ctx, kubeconfig),
		})
		if err == nil {
			var service kubernetesLoadBalancerService
			if json.Unmarshal(payload, &service) == nil && len(service.Status.LoadBalancer.Ingress) > 0 {
				candidate := service.Status.LoadBalancer.Ingress[0]
				if address = strings.TrimSpace(candidate.Hostname); address != "" {
					addressKind = "hostname"
				} else if address = strings.TrimSpace(candidate.IP); address != "" {
					addressKind = "ip"
				}
			}
		}
		if address != "" {
			break
		}
		select {
		case <-waitCtx.Done():
			err := fmt.Errorf("Higress 网关 Service %s/%s 在 5 分钟内未获得 AWS LoadBalancer 地址；请检查 AWS Load Balancer Controller、Service event、子网标签和 IAM 权限", namespace, serviceName)
			jobs.StepFinished(ctx, stepSyncGatewayAddress, err)
			return err
		case <-time.After(5 * time.Second):
		}
	}
	_, _ = fmt.Fprintf(output, "AWS LoadBalancer 已就绪：%s\n", address)

	ingressPayload, err := d.captureCommand(ctx, Command{
		Name: d.config.Tools.Kubectl, Args: []string{"get", "ingress", "-A", "-o", "json"},
		Dir: d.config.Paths.RepositoryRoot, Env: d.commandEnv(ctx, kubeconfig),
	})
	if err != nil {
		jobs.StepFinished(ctx, stepSyncGatewayAddress, err)
		return fmt.Errorf("读取 Higress Ingress 失败: %w", err)
	}
	var ingresses kubernetesIngressList
	if err := json.Unmarshal(ingressPayload, &ingresses); err != nil {
		jobs.StepFinished(ctx, stepSyncGatewayAddress, err)
		return fmt.Errorf("解析 Higress Ingress 失败: %w", err)
	}
	patched := 0
	statusEntry := map[string]string{addressKind: address}
	statusPatch, _ := json.Marshal(map[string]any{"status": map[string]any{"loadBalancer": map[string]any{"ingress": []any{statusEntry}}}})
	for _, ingress := range ingresses.Items {
		className := strings.TrimSpace(ingress.Spec.IngressClassName)
		if className == "" {
			className = strings.TrimSpace(ingress.Metadata.Annotations["kubernetes.io/ingress.class"])
		}
		if !allowedNamespaces[ingress.Metadata.Namespace] || !strings.EqualFold(className, "higress") {
			continue
		}
		command := Command{
			Name: d.config.Tools.Kubectl,
			Args: []string{"-n", ingress.Metadata.Namespace, "patch", "ingress", ingress.Metadata.Name, "--subresource=status", "--type=merge", "-p", string(statusPatch)},
			Dir:  d.config.Paths.RepositoryRoot, Env: d.commandEnv(ctx, kubeconfig),
		}
		if err := d.executor.Run(ctx, command, output); err != nil {
			jobs.StepFinished(ctx, stepSyncGatewayAddress, err)
			return fmt.Errorf("回写 Ingress %s/%s 的 LoadBalancer 地址失败: %w", ingress.Metadata.Namespace, ingress.Metadata.Name, err)
		}
		patched++
	}
	_, _ = fmt.Fprintf(output, "已将 %s 同步到 %d 个 Higress Ingress；现在 kubectl get ingress 的 ADDRESS 将正常显示。\n", address, patched)
	d.writeHigressDNSStatus(ctx, doc, address, output)
	jobs.StepFinished(ctx, stepSyncGatewayAddress, nil)
	return nil
}

// writeHigressDNSStatus makes the separation between an in-cluster route and
// public DNS explicit. Saving an Ingress cannot create records in an external
// DNS provider such as Cloudflare, and an NXDOMAIN would otherwise look like a
// broken gateway even though the Service and route are healthy.
func (d *Deployment) writeHigressDNSStatus(ctx context.Context, doc environment.Document, gatewayAddress string, output io.Writer) {
	resolver := d.dnsResolver
	if resolver == nil {
		resolver = net.DefaultResolver
	}
	rawDomains, _ := doc["domains"].([]any)
	for _, rawDomain := range rawDomains {
		domain, ok := rawDomain.(map[string]any)
		if !ok || !documentMapBoolDefault(domain, "enabled", true) || !strings.EqualFold(strings.TrimSpace(documentMapString(domain, "gateway")), "higress") {
			continue
		}
		host := strings.TrimSpace(documentMapString(domain, "domain"))
		if host == "" {
			continue
		}
		lookupCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
		addresses, err := resolver.LookupHost(lookupCtx, host)
		cancel()
		if err != nil || len(addresses) == 0 {
			_, _ = fmt.Fprintf(output, "[DNS 未就绪] %s 当前无法公网解析；Higress 路由已创建，但公网访问仍会失败。请在 DNS 服务商创建 CNAME：%s -> %s\n", host, host, gatewayAddress)
			continue
		}
		sort.Strings(addresses)
		_, _ = fmt.Fprintf(output, "[DNS 已解析] %s -> %s\n", host, strings.Join(addresses, "、"))
	}
}

// applyTLSOnly always bypasses component releases. It normally reconciles only
// platform-managed TLS Secrets; when a configured target Namespace is missing,
// it uses a narrow Terraform target first so ownership is recorded in state and
// a later phase-2 apply cannot collide with an untracked Namespace.
func (d *Deployment) applyTLSOnly(ctx context.Context, name, jobID string, doc environment.Document, output io.Writer) error {
	materials, err := d.loadUploadedTLSMaterials(ctx, doc)
	if err != nil {
		return err
	}
	defer clearUploadedTLSMaterials(materials)

	kubeconfig, err := d.updateKubeconfig(ctx, name, doc, output)
	if err != nil {
		return err
	}
	if environment.IsExistingEKS(doc) {
		if err := d.checkExistingEKS(ctx, doc, kubeconfig, output); err != nil {
			return err
		}
	}
	missingNamespaces, err := d.missingTLSNamespaces(ctx, kubeconfig, materials)
	if err != nil {
		return err
	}
	if len(missingNamespaces) == 0 {
		for _, step := range []string{stepInitializePlatform, stepPreparePlatform, stepEnsureTLSNamespaces} {
			jobs.StepStarted(ctx, step)
			jobs.StepFinished(ctx, step, nil)
		}
		_, _ = fmt.Fprintln(output, "TLS 目标 Namespace 均已存在，无需初始化 Terraform 或修改 Namespace。")
	} else {
		stateContext, cleanup, err := d.tlsStateContext(ctx, name, jobID)
		if err != nil {
			return err
		}
		defer cleanup()
		ctx = stateContext
		if err := d.terraformInit(ctx, d.config.Paths.TerraformPlatformDir, name, output); err != nil {
			return err
		}
		if err := d.terraformWorkspace(ctx, d.config.Paths.TerraformPlatformDir, name, output); err != nil {
			return err
		}
		if err := d.terraformApplyTLSNamespaces(ctx, name, doc, missingNamespaces, output); err != nil {
			return err
		}
	}
	return d.applyUploadedTLSMaterials(ctx, kubeconfig, doc, materials, output)
}

// missingTLSNamespaces performs a read-only preflight before any Secret is
// applied. A missing Namespace is not handled with kubectl create: namespaces
// configured by the platform belong in Terraform state so a later phase-2
// deployment cannot collide with an untracked Kubernetes object.
func (d *Deployment) missingTLSNamespaces(ctx context.Context, kubeconfig string, materials []uploadedTLSMaterial) ([]string, error) {
	namespaceSet := make(map[string]struct{}, len(materials))
	for _, material := range materials {
		if material.Namespace != "" {
			namespaceSet[material.Namespace] = struct{}{}
		}
	}
	namespaces := make([]string, 0, len(namespaceSet))
	for namespace := range namespaceSet {
		namespaces = append(namespaces, namespace)
	}
	sort.Strings(namespaces)

	missing := make([]string, 0)
	for _, namespace := range namespaces {
		var result bytes.Buffer
		command := Command{
			Name: d.config.Tools.Kubectl,
			Args: []string{"get", "namespace", namespace, "--ignore-not-found=true", "-o", "name"},
			Dir:  d.config.Paths.RepositoryRoot,
			Env:  d.commandEnv(ctx, kubeconfig),
		}
		if err := d.executor.Run(ctx, command, &result); err != nil {
			return nil, fmt.Errorf("检查 TLS 目标 Namespace %s 失败: %w", namespace, err)
		}
		if strings.TrimSpace(result.String()) == "" {
			missing = append(missing, namespace)
		}
	}
	return missing, nil
}

func (d *Deployment) tlsStateContext(ctx context.Context, name, jobID string) (context.Context, func(), error) {
	if !d.config.TerraformState.Enabled {
		return ctx, func() {}, nil
	}
	if d.stateProvider == nil {
		return nil, func() {}, errors.New("TLS 目标 Namespace 尚未创建，且统一 Terraform 状态中心未配置；平台已停止，避免创建未纳入 State 的 Namespace")
	}
	runtime, err := d.stateProvider.Runtime(ctx)
	if err != nil {
		return nil, func() {}, fmt.Errorf("TLS 目标 Namespace 尚未创建，统一 Terraform 状态中心不可用: %w", err)
	}
	runtimeDirectory := filepath.Join(d.config.Paths.DataDir, "terraform-state-runtime", jobID)
	ctx = context.WithValue(ctx, stateBackendContextKey{}, runtime)
	ctx = context.WithValue(ctx, stateRuntimeDirectoryContextKey{}, runtimeDirectory)
	return ctx, func() { d.cleanupStateBackendRuntime(name, runtimeDirectory) }, nil
}

func (d *Deployment) terraformApplyTLSNamespaces(ctx context.Context, name string, doc environment.Document, namespaces []string, output io.Writer) error {
	configured, _ := doc["namespaces"].(map[string]any)
	for _, namespace := range namespaces {
		if _, ok := configured[namespace]; !ok {
			err := fmt.Errorf("TLS 目标 Namespace %s 不在当前环境的 Namespaces 配置中；请先添加并保存该 Namespace", namespace)
			jobs.StepStarted(ctx, stepEnsureTLSNamespaces)
			jobs.StepFinished(ctx, stepEnsureTLSNamespaces, err)
			return err
		}
	}
	args := []string{
		"apply", "-input=false", "-auto-approve", "-no-color",
		"-var=config_file=" + d.environments.Path(name),
		"-var=deployment_phase=components",
	}
	for _, namespace := range namespaces {
		args = append(args, "-target="+terraformNamespaceAddress(namespace))
	}
	_, _ = fmt.Fprintf(output, "检测到 TLS 目标 Namespace 尚未创建：%s；平台将只创建这些 Namespace 并写入统一 Terraform State，不会安装或修改其他组件。\n", strings.Join(namespaces, "、"))
	err := d.step(ctx, output, stepEnsureTLSNamespaces, Command{
		Name: d.config.Tools.Terraform,
		Args: args,
		Dir:  d.config.Paths.TerraformPlatformDir,
		Env:  d.terraformEnv(ctx, name, d.config.Paths.TerraformPlatformDir),
	})
	metadataErr := d.persistRemoteStateMetadata(ctx, d.config.Paths.TerraformPlatformDir, name)
	if err != nil {
		return fmt.Errorf("自动创建 TLS 目标 Namespace 失败: %w", err)
	}
	if metadataErr != nil {
		return fmt.Errorf("TLS 目标 Namespace 已创建，但保存 Terraform State 元数据失败: %w", metadataErr)
	}
	return nil
}

func terraformNamespaceAddress(namespace string) string {
	return `kubernetes_namespace_v1.this["` + namespace + `"]`
}

type kubernetesNamespace struct {
	Metadata struct {
		Name   string            `json:"name"`
		Labels map[string]string `json:"labels"`
	} `json:"metadata"`
}

type eksNodeGroupDescription struct {
	Nodegroup struct {
		Status         string `json:"status"`
		LaunchTemplate struct {
			ID string `json:"id"`
		} `json:"launchTemplate"`
	} `json:"nodegroup"`
}

func desiredEKSNodeGroupNames(doc environment.Document) []string {
	eks, _ := doc["eks"].(map[string]any)
	groups, _ := eks["node_groups"].(map[string]any)
	result := make([]string, 0, len(groups))
	for name := range groups {
		if name = strings.TrimSpace(name); name != "" {
			result = append(result, name)
		}
	}
	sort.Strings(result)
	return result
}

func terraformEKSNodeGroupAddress(name string) string {
	return fmt.Sprintf(`aws_eks_node_group.this[%q]`, name)
}

func terraformEKSLaunchTemplateAddress(name string) string {
	return fmt.Sprintf(`aws_launch_template.node[%q]`, name)
}

// reconcileExistingEKSNodeGroups safely adopts a desired node group when an
// earlier Terraform process was interrupted after AWS accepted the create
// request but before the remote state recorded completion. This is especially
// important for long-running EKS creates: a blind retry would otherwise fail
// with ResourceInUse/AlreadyExists even though the requested group is healthy.
func (d *Deployment) reconcileExistingEKSNodeGroups(ctx context.Context, name string, doc environment.Document, output io.Writer) error {
	jobs.StepStarted(ctx, stepReconcileEKSNodeGroups)
	_, _ = fmt.Fprintf(output, "\n==> %s\n", stepReconcileEKSNodeGroups)
	fail := func(err error) error {
		jobs.StepFinished(ctx, stepReconcileEKSNodeGroups, err)
		return err
	}

	var stateOutput bytes.Buffer
	stateCommand := Command{
		Name: d.config.Tools.Terraform,
		Args: []string{"state", "list"},
		Dir:  d.config.Paths.TerraformInfraDir,
		Env:  d.terraformEnv(ctx, name, d.config.Paths.TerraformInfraDir),
	}
	if err := d.executor.Run(ctx, stateCommand, &stateOutput); err != nil {
		return fail(fmt.Errorf("读取 EKS 节点组 Terraform State 失败: %w", err))
	}
	tracked := make(map[string]struct{})
	for _, address := range strings.Fields(stateOutput.String()) {
		tracked[address] = struct{}{}
	}

	region := strings.TrimSpace(documentString(doc, "region"))
	cluster := strings.TrimSpace(environment.ClusterName(doc))
	if region == "" || cluster == "" {
		return fail(errors.New("EKS region or cluster name is missing"))
	}
	imported := 0
	missing := 0
	for _, groupName := range desiredEKSNodeGroupNames(doc) {
		nodeAddress := terraformEKSNodeGroupAddress(groupName)
		launchAddress := terraformEKSLaunchTemplateAddress(groupName)
		_, nodeTracked := tracked[nodeAddress]
		_, launchTracked := tracked[launchAddress]
		if nodeTracked && launchTracked {
			_, _ = fmt.Fprintf(output, "节点组 %s 已由当前 Terraform State 管理，无需重复处理。\n", groupName)
			continue
		}

		var payload bytes.Buffer
		describeCommand := Command{
			Name: d.config.Tools.AWS,
			Args: []string{
				"eks", "describe-nodegroup", "--region", region, "--cluster-name", cluster,
				"--nodegroup-name", groupName, "--output", "json", "--no-cli-pager",
			},
			Dir: d.config.Paths.RepositoryRoot,
			Env: d.commandEnv(ctx, ""),
		}
		if err := d.executor.Run(ctx, describeCommand, &payload); err != nil {
			details := strings.ToLower(payload.String() + "\n" + err.Error())
			if strings.Contains(details, "resourcenotfoundexception") || strings.Contains(details, "no node group found") {
				_, _ = fmt.Fprintf(output, "节点组 %s 尚不存在，将由本次 Terraform 正常创建。\n", groupName)
				missing++
				continue
			}
			return fail(fmt.Errorf("读取已有 EKS 节点组 %s 失败: %w", groupName, err))
		}
		var described eksNodeGroupDescription
		if err := json.Unmarshal(payload.Bytes(), &described); err != nil {
			return fail(fmt.Errorf("解析已有 EKS 节点组 %s 失败: %w", groupName, err))
		}
		status := strings.ToUpper(strings.TrimSpace(described.Nodegroup.Status))
		if status != "ACTIVE" {
			return fail(fmt.Errorf("EKS 节点组 %s 已存在但状态为 %s；平台已停止重建，请等待其变为 ACTIVE 后直接重试，若最终为 CREATE_FAILED 请先按日志清理失败节点组", groupName, defaultString(status, "UNKNOWN")))
		}
		launchTemplateID := strings.TrimSpace(described.Nodegroup.LaunchTemplate.ID)
		if !launchTracked {
			if launchTemplateID == "" {
				return fail(fmt.Errorf("EKS 节点组 %s 已存在但未返回启动模板 ID，平台拒绝不完整接管", groupName))
			}
			_, _ = fmt.Fprintf(output, "节点组 %s 的启动模板已存在但 State 缺失，正在安全导入 %s。\n", groupName, launchTemplateID)
			if err := d.terraformImportInfra(ctx, name, launchAddress, launchTemplateID, output); err != nil {
				return fail(fmt.Errorf("导入节点组 %s 启动模板失败: %w", groupName, err))
			}
			tracked[launchAddress] = struct{}{}
			imported++
		}
		if !nodeTracked {
			_, _ = fmt.Fprintf(output, "EKS 节点组 %s 已为 ACTIVE 但 State 缺失，正在安全复用并导入。\n", groupName)
			if err := d.terraformImportInfra(ctx, name, nodeAddress, cluster+":"+groupName, output); err != nil {
				return fail(fmt.Errorf("导入 EKS 节点组 %s 失败: %w", groupName, err))
			}
			tracked[nodeAddress] = struct{}{}
			imported++
		}
	}
	if imported > 0 {
		if err := d.persistRemoteStateMetadata(ctx, d.config.Paths.TerraformInfraDir, name); err != nil {
			return fail(fmt.Errorf("保存节点组对账后的 Terraform State 元数据: %w", err))
		}
	}
	_, _ = fmt.Fprintf(output, "EKS 节点组对账完成：导入 %d 项，等待创建 %d 个节点组；不会删除或重复创建已有节点组。\n", imported, missing)
	jobs.StepFinished(ctx, stepReconcileEKSNodeGroups, nil)
	return nil
}

type ec2QuotaInstance struct {
	InstanceType      string `json:"InstanceType"`
	InstanceLifecycle string `json:"InstanceLifecycle"`
}

type ec2QuotaInstanceType struct {
	InstanceType string `json:"InstanceType"`
	VCpuInfo     struct {
		DefaultVCpus int `json:"DefaultVCpus"`
	} `json:"VCpuInfo"`
}

func desiredNodeGroupCapacity(raw any) (int, []string) {
	group, _ := raw.(map[string]any)
	desired := documentMapIntDefault(group, "desired_size", 0)
	if documentMapBoolDefault(group, "capacity_deferred", false) {
		desired = 0
	}
	types := make([]string, 0)
	switch values := group["instance_types"].(type) {
	case []any:
		for _, value := range values {
			if item, ok := value.(string); ok && strings.TrimSpace(item) != "" {
				types = append(types, strings.TrimSpace(item))
			}
		}
	case []string:
		for _, item := range values {
			if strings.TrimSpace(item) != "" {
				types = append(types, strings.TrimSpace(item))
			}
		}
	}
	return desired, types
}

func (d *Deployment) awsJSON(ctx context.Context, doc environment.Document, target any, args ...string) error {
	region := strings.TrimSpace(documentString(doc, "region"))
	args = append(args, "--region", region, "--output", "json", "--no-cli-pager")
	var payload bytes.Buffer
	if err := d.executor.Run(ctx, Command{
		Name: d.config.Tools.AWS,
		Args: args,
		Dir:  d.config.Paths.RepositoryRoot,
		Env:  d.commandEnv(ctx, ""),
	}, &payload); err != nil {
		return fmt.Errorf("%w: %s", err, strings.TrimSpace(payload.String()))
	}
	if err := json.Unmarshal(payload.Bytes(), target); err != nil {
		return fmt.Errorf("解析 AWS 返回数据失败: %w", err)
	}
	return nil
}

// checkEKSNodeGroupVCPUQuota blocks an apply before Terraform when the desired
// initial capacity cannot fit into the regional Standard On-Demand vCPU
// quota. For a new platform-managed cluster it also enforces an account-level
// safety reserve of available (quota minus current use) vCPU and EIP capacity.
// Existing ACTIVE clusters keep their normal incremental capacity check and
// are never blocked by the new-cluster reserve policy.
func (d *Deployment) checkEKSNodeGroupVCPUQuota(ctx context.Context, doc environment.Document, output io.Writer) error {
	jobs.StepStarted(ctx, stepCheckEKSNodeGroupQuota)
	_, _ = fmt.Fprintf(output, "\n==> %s\n", stepCheckEKSNodeGroupQuota)
	fail := func(err error) error {
		jobs.StepFinished(ctx, stepCheckEKSNodeGroupQuota, err)
		return err
	}

	eks, _ := doc["eks"].(map[string]any)
	groups, _ := eks["node_groups"].(map[string]any)
	if len(groups) == 0 {
		_, _ = fmt.Fprintln(output, "未配置 EKS 节点组，跳过配额校验。")
		jobs.StepFinished(ctx, stepCheckEKSNodeGroupQuota, nil)
		return nil
	}

	configuredTypes := make(map[string]struct{})
	for _, raw := range groups {
		_, types := desiredNodeGroupCapacity(raw)
		for _, instanceType := range types {
			configuredTypes[instanceType] = struct{}{}
		}
	}
	if len(configuredTypes) == 0 {
		return fail(errors.New("EKS 节点组未配置有效实例类型"))
	}
	typeNames := make([]string, 0, len(configuredTypes))
	for instanceType := range configuredTypes {
		typeNames = append(typeNames, instanceType)
	}
	sort.Strings(typeNames)

	var typePayload struct {
		InstanceTypes []ec2QuotaInstanceType `json:"InstanceTypes"`
	}
	typeArgs := []string{"ec2", "describe-instance-types", "--instance-types"}
	typeArgs = append(typeArgs, typeNames...)
	if err := d.awsJSON(ctx, doc, &typePayload, typeArgs...); err != nil {
		return fail(fmt.Errorf("查询 EC2 实例 vCPU 规格失败: %w", err))
	}
	vcpus := make(map[string]int, len(typePayload.InstanceTypes))
	for _, item := range typePayload.InstanceTypes {
		vcpus[item.InstanceType] = item.VCpuInfo.DefaultVCpus
	}

	cluster := strings.TrimSpace(environment.ClusterName(doc))
	requireNewClusterReserve := false
	var clusterPayload struct {
		Cluster struct {
			Status string `json:"status"`
		} `json:"cluster"`
	}
	if err := d.awsJSON(ctx, doc, &clusterPayload, "eks", "describe-cluster", "--name", cluster); err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "resourcenotfoundexception") {
			requireNewClusterReserve = true
			_, _ = fmt.Fprintln(output, "未发现现有 EKS 集群，本次按新建环境执行账户区域剩余配额保护。")
		} else {
			return fail(fmt.Errorf("判断 EKS 集群是否已存在失败: %w", err))
		}
	} else if !strings.EqualFold(strings.TrimSpace(clusterPayload.Cluster.Status), "ACTIVE") {
		requireNewClusterReserve = true
		_, _ = fmt.Fprintf(output, "EKS 集群当前状态为 %s，仍按新建环境执行账户区域剩余配额保护。\n", clusterPayload.Cluster.Status)
	} else {
		_, _ = fmt.Fprintln(output, "EKS 集群已存在且为 ACTIVE；仅校验本次节点扩容所需 vCPU，不套用新建环境保留阈值。")
	}

	additionalVCPUs := 0
	for groupName, raw := range groups {
		desired, types := desiredNodeGroupCapacity(raw)
		if desired <= 0 {
			continue
		}
		actualDesired := 0
		var described struct {
			Nodegroup struct {
				Scaling struct {
					DesiredSize int `json:"desiredSize"`
				} `json:"scalingConfig"`
			} `json:"nodegroup"`
		}
		err := d.awsJSON(ctx, doc, &described, "eks", "describe-nodegroup", "--cluster-name", cluster, "--nodegroup-name", groupName)
		if err == nil {
			actualDesired = described.Nodegroup.Scaling.DesiredSize
		} else if !strings.Contains(strings.ToLower(err.Error()), "resourcenotfoundexception") {
			return fail(fmt.Errorf("读取 EKS 节点组 %s 当前容量失败: %w", groupName, err))
		}
		increase := desired - actualDesired
		if increase <= 0 {
			continue
		}
		maxVCPU := 0
		for _, instanceType := range types {
			if vcpus[instanceType] > maxVCPU {
				maxVCPU = vcpus[instanceType]
			}
		}
		if maxVCPU == 0 {
			return fail(fmt.Errorf("无法识别节点组 %s 的实例 vCPU", groupName))
		}
		additionalVCPUs += increase * maxVCPU
	}
	if additionalVCPUs == 0 && !requireNewClusterReserve {
		_, _ = fmt.Fprintln(output, "节点组没有新增初始容量，本次不会额外占用 EC2 vCPU 配额。")
		jobs.StepFinished(ctx, stepCheckEKSNodeGroupQuota, nil)
		return nil
	}

	var quotaPayload struct {
		Quota struct {
			Value float64 `json:"Value"`
		} `json:"Quota"`
	}
	if err := d.awsJSON(ctx, doc, &quotaPayload, "service-quotas", "get-service-quota", "--service-code", "ec2", "--quota-code", "L-1216C47A"); err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "accessdenied") {
			if requireNewClusterReserve {
				return fail(fmt.Errorf("[AWS_CREATE_QUOTA_CHECK_UNAVAILABLE] 新建 EKS 前必须读取区域剩余 vCPU 配额；请为当前项目 AWS 凭据补充 servicequotas:GetServiceQuota 权限后重试，本次未执行 Terraform"))
			}
			_, _ = fmt.Fprintln(output, "当前 AWS 凭据无读取 EC2 vCPU 配额权限，已跳过自动配额判断；建议补充 servicequotas:GetServiceQuota 权限。")
			jobs.StepFinished(ctx, stepCheckEKSNodeGroupQuota, nil)
			return nil
		}
		return fail(fmt.Errorf("查询 EC2 vCPU 配额失败: %w", err))
	}

	var instancesPayload struct {
		Reservations []struct {
			Instances []ec2QuotaInstance `json:"Instances"`
		} `json:"Reservations"`
	}
	if err := d.awsJSON(ctx, doc, &instancesPayload, "ec2", "describe-instances", "--filters", "Name=instance-state-name,Values=pending,running"); err != nil {
		return fail(fmt.Errorf("统计当前 EC2 vCPU 使用量失败: %w", err))
	}
	usedTypes := make(map[string]struct{})
	for _, reservation := range instancesPayload.Reservations {
		for _, instance := range reservation.Instances {
			usedTypes[instance.InstanceType] = struct{}{}
		}
	}
	missingUsedTypes := make([]string, 0)
	for instanceType := range usedTypes {
		if _, found := vcpus[instanceType]; !found {
			missingUsedTypes = append(missingUsedTypes, instanceType)
		}
	}
	if len(missingUsedTypes) > 0 {
		sort.Strings(missingUsedTypes)
		var usedTypePayload struct {
			InstanceTypes []ec2QuotaInstanceType `json:"InstanceTypes"`
		}
		args := []string{"ec2", "describe-instance-types", "--instance-types"}
		args = append(args, missingUsedTypes...)
		if err := d.awsJSON(ctx, doc, &usedTypePayload, args...); err != nil {
			return fail(fmt.Errorf("查询现有 EC2 实例 vCPU 规格失败: %w", err))
		}
		for _, item := range usedTypePayload.InstanceTypes {
			vcpus[item.InstanceType] = item.VCpuInfo.DefaultVCpus
		}
	}
	usedVCPUs := 0
	for _, reservation := range instancesPayload.Reservations {
		for _, instance := range reservation.Instances {
			// InstanceLifecycle is empty for On-Demand instances. Spot capacity has
			// a separate quota and must not reduce the Standard On-Demand reserve.
			if strings.TrimSpace(instance.InstanceLifecycle) != "" {
				continue
			}
			usedVCPUs += vcpus[instance.InstanceType]
		}
	}
	quota := int(quotaPayload.Quota.Value)
	availableVCPUs := quota - usedVCPUs
	if availableVCPUs < 0 {
		availableVCPUs = 0
	}
	requiredAvailableVCPUs := additionalVCPUs
	if requireNewClusterReserve && requiredAvailableVCPUs < minimumNewEKSAvailableVCPUs {
		requiredAvailableVCPUs = minimumNewEKSAvailableVCPUs
	}
	_, _ = fmt.Fprintf(output, "EC2 Standard On-Demand vCPU：总配额 %d，当前约已用 %d，实际剩余 %d；本次节点计划新增 %d，新建环境最低剩余要求 %d。\n", quota, usedVCPUs, availableVCPUs, additionalVCPUs, minimumNewEKSAvailableVCPUs)
	if availableVCPUs < requiredAvailableVCPUs {
		shortage := requiredAvailableVCPUs - availableVCPUs
		if requireNewClusterReserve {
			return fail(fmt.Errorf("[AWS_CREATE_QUOTA_INSUFFICIENT] 新建 EKS 的区域剩余 vCPU 不足：总配额 %d、当前约已用 %d、实际剩余 %d、最低需要 %d、缺口 %d、建议申请总配额至少 %d；请在 AWS Service Quotas 提高 EC2 Standard On-Demand vCPU（L-1216C47A），审批生效后直接重试，本次未执行 Terraform，也未修改其他项目资源", quota, usedVCPUs, availableVCPUs, requiredAvailableVCPUs, shortage, usedVCPUs+requiredAvailableVCPUs))
		}
		return fail(fmt.Errorf("EKS 节点组本次计划新增 %d vCPU，但区域实际只剩 %d vCPU（总配额 %d、当前约已用 %d）；请提高 EC2 配额 L-1216C47A 或降低节点组容量后重试，本次未执行 Terraform", additionalVCPUs, availableVCPUs, quota, usedVCPUs))
	}

	if requireNewClusterReserve {
		var eipQuotaPayload struct {
			Quota struct {
				Value float64 `json:"Value"`
			} `json:"Quota"`
		}
		if err := d.awsJSON(ctx, doc, &eipQuotaPayload, "service-quotas", "get-service-quota", "--service-code", "ec2", "--quota-code", "L-0263D0A3"); err != nil {
			if strings.Contains(strings.ToLower(err.Error()), "accessdenied") {
				return fail(fmt.Errorf("[AWS_CREATE_QUOTA_CHECK_UNAVAILABLE] 新建 EKS 前必须读取区域 EIP 剩余配额；请为当前项目 AWS 凭据补充 servicequotas:GetServiceQuota 权限后重试，本次未执行 Terraform"))
			}
			return fail(fmt.Errorf("查询 EC2-VPC Elastic IP 配额失败: %w", err))
		}
		var addressesPayload struct {
			Addresses []json.RawMessage `json:"Addresses"`
		}
		if err := d.awsJSON(ctx, doc, &addressesPayload, "ec2", "describe-addresses"); err != nil {
			return fail(fmt.Errorf("统计当前区域 EIP 使用量失败: %w", err))
		}
		eipQuota := int(eipQuotaPayload.Quota.Value)
		usedEIPs := len(addressesPayload.Addresses)
		availableEIPs := eipQuota - usedEIPs
		if availableEIPs < 0 {
			availableEIPs = 0
		}
		_, _ = fmt.Fprintf(output, "EC2-VPC Elastic IP：总配额 %d，当前已分配 %d，实际剩余 %d；新建环境最低剩余要求 %d。\n", eipQuota, usedEIPs, availableEIPs, minimumNewEKSAvailableEIPs)
		if availableEIPs < minimumNewEKSAvailableEIPs {
			return fail(fmt.Errorf("[AWS_CREATE_QUOTA_INSUFFICIENT] 新建 EKS 的区域剩余 EIP 不足：总配额 %d、当前已分配 %d、实际剩余 %d、最低需要 %d、缺口 %d、建议申请总配额至少 %d；请在 AWS Service Quotas 提高 EC2-VPC Elastic IP（L-0263D0A3），审批生效后直接重试，本次未执行 Terraform，也未修改其他项目资源", eipQuota, usedEIPs, availableEIPs, minimumNewEKSAvailableEIPs, minimumNewEKSAvailableEIPs-availableEIPs, usedEIPs+minimumNewEKSAvailableEIPs))
		}
		_, _ = fmt.Fprintf(output, "新建 EKS 配额预检通过：区域实际剩余 vCPU %d（要求 ≥%d），EIP %d（要求 ≥%d）。该检查只读，不会占用额度或修改其他项目。\n", availableVCPUs, minimumNewEKSAvailableVCPUs, availableEIPs, minimumNewEKSAvailableEIPs)
	}
	jobs.StepFinished(ctx, stepCheckEKSNodeGroupQuota, nil)
	return nil
}

func (d *Deployment) terraformImportInfra(ctx context.Context, name, address, id string, output io.Writer) error {
	return d.executor.Run(ctx, Command{
		Name: d.config.Tools.Terraform,
		Args: []string{
			"import", "-input=false", "-no-color",
			"-var=config_file=" + d.environments.Path(name), address, id,
		},
		Dir: d.config.Paths.TerraformInfraDir,
		Env: d.terraformEnv(ctx, name, d.config.Paths.TerraformInfraDir),
	}, output)
}

// desiredPlatformNamespaces mirrors terraform/platform/locals.tf. Keeping the
// calculation here lets the runner adopt an existing, untracked Namespace
// before Terraform attempts to create it. Namespace adoption is deliberately
// limited to objects with no conflicting project/environment ownership.
func desiredPlatformNamespaces(doc environment.Document) []string {
	namespaces := make(map[string]struct{})
	if configured, ok := doc["namespaces"].(map[string]any); ok {
		for namespace := range configured {
			if namespace = strings.TrimSpace(namespace); namespace != "" {
				namespaces[namespace] = struct{}{}
			}
		}
	}
	components, _ := doc["components"].(map[string]any)
	for _, key := range []string{"consul", "etcd"} {
		component, _ := components[key].(map[string]any)
		if !documentMapBoolDefault(component, "enabled", false) {
			continue
		}
		namespace := strings.TrimSpace(documentMapString(component, "namespace"))
		if namespace == "" {
			namespace = "platform-server"
		}
		namespaces[namespace] = struct{}{}
	}
	if catalog, ok := components["catalog"].(map[string]any); ok {
		for _, raw := range catalog {
			component, _ := raw.(map[string]any)
			if !documentMapBoolDefault(component, "enabled", false) {
				continue
			}
			namespace := strings.TrimSpace(documentMapString(component, "namespace"))
			if namespace == "" {
				namespace = "platform-server"
			}
			namespaces[namespace] = struct{}{}
		}
	}
	result := make([]string, 0, len(namespaces))
	for namespace := range namespaces {
		result = append(result, namespace)
	}
	sort.Strings(result)
	return result
}

func (d *Deployment) reconcileExistingNamespaces(ctx context.Context, name string, doc environment.Document, kubeconfig string, output io.Writer) error {
	jobs.StepStarted(ctx, stepReconcileNamespaces)
	_, _ = fmt.Fprintf(output, "\n==> %s\n", stepReconcileNamespaces)
	fail := func(err error) error {
		jobs.StepFinished(ctx, stepReconcileNamespaces, err)
		return err
	}

	var stateOutput bytes.Buffer
	stateCommand := Command{
		Name: d.config.Tools.Terraform,
		Args: []string{"state", "list"},
		Dir:  d.config.Paths.TerraformPlatformDir,
		Env:  d.terraformEnv(ctx, name, d.config.Paths.TerraformPlatformDir),
	}
	if err := d.executor.Run(ctx, stateCommand, &stateOutput); err != nil {
		return fail(fmt.Errorf("读取 Namespace Terraform State 失败: %w", err))
	}
	tracked := make(map[string]struct{})
	for _, address := range strings.Fields(stateOutput.String()) {
		tracked[address] = struct{}{}
	}

	project := strings.TrimSpace(documentString(doc, "project"))
	environmentName := strings.TrimSpace(documentString(doc, "environment"))
	imported := 0
	missing := 0
	for _, namespace := range desiredPlatformNamespaces(doc) {
		address := terraformNamespaceAddress(namespace)
		if _, ok := tracked[address]; ok {
			_, _ = fmt.Fprintf(output, "Namespace %s 已由当前环境 Terraform State 管理，无需重复处理。\n", namespace)
			continue
		}

		var namespaceOutput bytes.Buffer
		getCommand := Command{
			Name: d.config.Tools.Kubectl,
			Args: []string{"get", "namespace", namespace, "--ignore-not-found=true", "-o", "json"},
			Dir:  d.config.Paths.RepositoryRoot,
			Env:  d.commandEnv(ctx, kubeconfig),
		}
		if err := d.executor.Run(ctx, getCommand, &namespaceOutput); err != nil {
			return fail(fmt.Errorf("检查已有 Namespace %s 失败: %w", namespace, err))
		}
		if strings.TrimSpace(namespaceOutput.String()) == "" {
			_, _ = fmt.Fprintf(output, "Namespace %s 尚不存在，将由本次 Terraform 正常创建。\n", namespace)
			missing++
			continue
		}

		var existing kubernetesNamespace
		if err := json.Unmarshal(namespaceOutput.Bytes(), &existing); err != nil {
			return fail(fmt.Errorf("解析已有 Namespace %s 元数据失败: %w", namespace, err))
		}
		ownerProject := strings.TrimSpace(existing.Metadata.Labels["app.kubernetes.io/part-of"])
		ownerEnvironment := strings.TrimSpace(existing.Metadata.Labels["environment"])
		if (ownerProject != "" && ownerProject != project) || (ownerEnvironment != "" && ownerEnvironment != environmentName) {
			return fail(fmt.Errorf("Namespace %s 已存在但归属于其他项目或环境（project=%s, environment=%s）；平台已拒绝自动接管，请改用当前环境独立 Namespace", namespace, defaultString(ownerProject, "未标记"), defaultString(ownerEnvironment, "未标记")))
		}

		_, _ = fmt.Fprintf(output, "Namespace %s 已存在但尚未进入当前 Terraform State；归属校验通过，正在安全复用并跳过重复创建。\n", namespace)
		importCommand := Command{
			Name: d.config.Tools.Terraform,
			Args: []string{
				"import", "-input=false", "-no-color",
				"-var=config_file=" + d.environments.Path(name),
				"-var=deployment_phase=components",
				address, namespace,
			},
			Dir: d.config.Paths.TerraformPlatformDir,
			Env: d.terraformEnv(ctx, name, d.config.Paths.TerraformPlatformDir),
		}
		if err := d.executor.Run(ctx, importCommand, output); err != nil {
			return fail(fmt.Errorf("复用已有 Namespace %s 并导入 Terraform State 失败: %w", namespace, err))
		}
		_, _ = fmt.Fprintf(output, "Namespace %s 已安全复用并纳入当前环境 State；后续部署不会再次尝试创建。\n", namespace)
		imported++
	}

	_, _ = fmt.Fprintf(output, "Namespace 对账完成：复用 %d 个，等待创建 %d 个；不会删除任何 Namespace。\n", imported, missing)
	jobs.StepFinished(ctx, stepReconcileNamespaces, nil)
	return nil
}

type uploadedTLSMaterial struct {
	Project     string
	Environment string
	Key         string
	Namespace   string
	SecretName  string
	Certificate []byte
	PrivateKey  []byte
}

func (d *Deployment) loadUploadedTLSMaterials(ctx context.Context, doc environment.Document) ([]uploadedTLSMaterial, error) {
	raw, _ := environment.GetPath(doc, "tls.certificates")
	certificates, _ := raw.([]any)
	project, environmentName := documentString(doc, "project"), documentString(doc, "environment")
	configs := make([]uploadedTLSMaterial, 0)
	for _, item := range certificates {
		certificate, ok := item.(map[string]any)
		if !ok || strings.TrimSpace(documentMapString(certificate, "mode")) != "uploaded-pem" || !documentMapBoolDefault(certificate, "enabled", true) {
			continue
		}
		configs = append(configs, uploadedTLSMaterial{
			Project:     project,
			Environment: environmentName,
			Key:         strings.TrimSpace(documentMapString(certificate, "material_ref")),
			Namespace:   strings.TrimSpace(documentMapString(certificate, "namespace")),
			SecretName:  strings.TrimSpace(documentMapString(certificate, "tls_secret_name")),
		})
	}
	sort.Slice(configs, func(i, j int) bool { return configs[i].Key < configs[j].Key })
	if len(configs) == 0 {
		return configs, nil
	}
	if d.tlsProvider == nil {
		return nil, errors.New("粘贴证书服务未配置，无法读取环境级加密证书材料")
	}
	for index := range configs {
		material, err := d.tlsProvider.Material(ctx, project, environmentName, configs[index].Key)
		if err != nil {
			clearUploadedTLSMaterials(configs)
			return nil, fmt.Errorf("TLS 证书 %s 的加密材料不可用，请在部署配置的 TLS 证书页面重新粘贴证书链和私钥: %w", configs[index].Key, err)
		}
		configs[index].Certificate = material.CertificatePEM
		configs[index].PrivateKey = material.PrivateKeyPEM
	}
	return expandUploadedTLSMaterials(doc, configs), nil
}

// expandUploadedTLSMaterials mirrors an uploaded certificate into every
// Namespace containing an HTTPS Ingress that references it. Kubernetes Secrets
// are namespaced and an Ingress cannot read a TLS Secret from another
// Namespace, even when the Secret has the same name.
func expandUploadedTLSMaterials(doc environment.Document, materials []uploadedTLSMaterial) []uploadedTLSMaterial {
	targets := make(map[string]map[string]struct{})
	domains, _ := doc["domains"].([]any)
	for _, raw := range domains {
		domain, ok := raw.(map[string]any)
		if !ok || !documentMapBoolDefault(domain, "enabled", true) || !documentMapBoolDefault(domain, "tls_enabled", false) || strings.TrimSpace(documentMapString(domain, "access_type")) == "ip" {
			continue
		}
		certificateKey := strings.TrimSpace(documentMapString(domain, "certificate_ref"))
		namespace := strings.TrimSpace(documentMapString(domain, "namespace"))
		if certificateKey == "" || namespace == "" {
			continue
		}
		if targets[certificateKey] == nil {
			targets[certificateKey] = make(map[string]struct{})
		}
		targets[certificateKey][namespace] = struct{}{}
	}

	result := make([]uploadedTLSMaterial, 0, len(materials))
	for _, material := range materials {
		namespaces := map[string]struct{}{material.Namespace: {}}
		for namespace := range targets[material.Key] {
			namespaces[namespace] = struct{}{}
		}
		ordered := make([]string, 0, len(namespaces))
		for namespace := range namespaces {
			if namespace != "" {
				ordered = append(ordered, namespace)
			}
		}
		sort.Strings(ordered)
		for _, namespace := range ordered {
			copy := material
			copy.Namespace = namespace
			result = append(result, copy)
		}
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].Key == result[j].Key {
			return result[i].Namespace < result[j].Namespace
		}
		return result[i].Key < result[j].Key
	})
	return result
}

func (d *Deployment) applyUploadedTLSMaterials(ctx context.Context, kubeconfig string, doc environment.Document, materials []uploadedTLSMaterial, output io.Writer) error {
	jobs.StepStarted(ctx, stepApplyTLSCertificates)
	_, _ = fmt.Fprintf(output, "\n==> %s\n", stepApplyTLSCertificates)
	if len(materials) == 0 {
		_, _ = fmt.Fprintln(output, "当前环境没有需要创建或更新的粘贴证书 TLS Secret，将检查并清理已移除的旧证书。")
	}
	for _, material := range materials {
		payload, err := tlsSecretManifest(material)
		if err != nil {
			jobs.StepFinished(ctx, stepApplyTLSCertificates, err)
			return err
		}
		_, _ = fmt.Fprintf(output, "正在应用证书配置 %s -> %s/%s（证书正文和私钥不会写入日志）\n", material.Key, material.Namespace, material.SecretName)
		command := Command{
			Name: d.config.Tools.Kubectl, Args: []string{"apply", "-f", "-"},
			Dir: d.config.Paths.RepositoryRoot, Env: d.commandEnv(ctx, kubeconfig), Stdin: bytes.NewReader(payload),
		}
		_, _ = fmt.Fprintf(output, "$ %s %s\n", command.Name, strings.Join(command.Args, " "))
		runErr := d.executor.Run(ctx, command, output)
		clear(payload)
		if runErr != nil {
			wrapped := fmt.Errorf("应用 TLS Secret %s/%s 失败: %w", material.Namespace, material.SecretName, runErr)
			jobs.StepFinished(ctx, stepApplyTLSCertificates, wrapped)
			return wrapped
		}
	}
	if err := d.pruneUploadedTLSSecrets(ctx, kubeconfig, documentString(doc, "project"), documentString(doc, "environment"), materials, output); err != nil {
		jobs.StepFinished(ctx, stepApplyTLSCertificates, err)
		return err
	}
	jobs.StepFinished(ctx, stepApplyTLSCertificates, nil)
	return nil
}

func (d *Deployment) pruneUploadedTLSSecrets(ctx context.Context, kubeconfig, project, environmentName string, materials []uploadedTLSMaterial, output io.Writer) error {
	wanted := make(map[string]struct{}, len(materials))
	for _, material := range materials {
		wanted[material.Namespace+"\x00"+material.SecretName] = struct{}{}
	}
	selector := uploadedTLSSecretSelector(project, environmentName)
	command := Command{
		Name: d.config.Tools.Kubectl,
		Args: []string{"get", "secret", "-A", "-l", selector, "-o", `jsonpath={range .items[*]}{.metadata.namespace}{"\t"}{.metadata.name}{"\n"}{end}`},
		Dir:  d.config.Paths.RepositoryRoot,
		Env:  d.commandEnv(ctx, kubeconfig),
	}
	_, _ = fmt.Fprintf(output, "$ %s get secret -A -l %s（只读取名称，不读取证书正文）\n", command.Name, selector)
	var inventory bytes.Buffer
	if err := d.executor.Run(ctx, command, &inventory); err != nil {
		return fmt.Errorf("检查环境级 TLS Secret 失败: %w", err)
	}
	for _, line := range strings.Split(strings.TrimSpace(inventory.String()), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) != 2 {
			return errors.New("检查环境级 TLS Secret 失败: kubectl 返回了无法识别的资源名称")
		}
		if _, keep := wanted[fields[0]+"\x00"+fields[1]]; keep {
			continue
		}
		_, _ = fmt.Fprintf(output, "正在删除配置中已移除的旧 TLS Secret %s/%s\n", fields[0], fields[1])
		deleteCommand := Command{
			Name: d.config.Tools.Kubectl, Args: []string{"delete", "secret", fields[1], "-n", fields[0], "--ignore-not-found=true"},
			Dir: d.config.Paths.RepositoryRoot, Env: d.commandEnv(ctx, kubeconfig),
		}
		if err := d.executor.Run(ctx, deleteCommand, output); err != nil {
			return fmt.Errorf("删除旧 TLS Secret %s/%s 失败: %w", fields[0], fields[1], err)
		}
	}
	return nil
}

// uploadedTLSSecretSelector deliberately requires the certificate-key label.
// Other environment-scoped Secrets (for example the Alertmanager relay token)
// share the project/environment labels and must never be pruned as TLS
// inventory.
func uploadedTLSSecretSelector(project, environmentName string) string {
	return "app.kubernetes.io/managed-by=ops-deploy-platform,ops-deploy.io/project=" + project +
		",ops-deploy.io/environment=" + environmentName + ",ops-deploy.io/certificate-key"
}

func tlsSecretManifest(material uploadedTLSMaterial) ([]byte, error) {
	return json.Marshal(map[string]any{
		"apiVersion": "v1",
		"kind":       "Secret",
		"metadata": map[string]any{
			"name":      material.SecretName,
			"namespace": material.Namespace,
			"labels": map[string]string{
				"app.kubernetes.io/managed-by":  "ops-deploy-platform",
				"ops-deploy.io/certificate-key": material.Key,
				"ops-deploy.io/project":         material.Project,
				"ops-deploy.io/environment":     material.Environment,
			},
		},
		"type": "kubernetes.io/tls",
		"data": map[string]string{
			"tls.crt": base64.StdEncoding.EncodeToString(material.Certificate),
			"tls.key": base64.StdEncoding.EncodeToString(material.PrivateKey),
		},
	})
}

func clearUploadedTLSMaterials(materials []uploadedTLSMaterial) {
	for index := range materials {
		clear(materials[index].Certificate)
		clear(materials[index].PrivateKey)
		materials[index].Certificate = nil
		materials[index].PrivateKey = nil
	}
}

func documentMapString(value map[string]any, key string) string {
	result, _ := value[key].(string)
	return result
}

func documentMapBoolDefault(value map[string]any, key string, fallback bool) bool {
	result, ok := value[key].(bool)
	if !ok {
		return fallback
	}
	return result
}

func documentMapIntDefault(value map[string]any, key string, fallback int) int {
	switch result := value[key].(type) {
	case int:
		return result
	case int64:
		return int(result)
	case float64:
		return int(result)
	default:
		return fallback
	}
}

func defaultString(value, fallback string) string {
	if value = strings.TrimSpace(value); value != "" {
		return value
	}
	return fallback
}

type helmListRelease struct {
	Name      string `json:"name"`
	Namespace string `json:"namespace"`
	Status    string `json:"status"`
}

type componentLifecycleItem struct {
	Key, Name, Action, Namespace, Release, Status, Retention, Related string
}

func componentLifecyclePlan(doc environment.Document, phase string, releases []helmListRelease) []componentLifecycleItem {
	installed := make(map[string]helmListRelease, len(releases))
	for _, release := range releases {
		installed[release.Namespace+"/"+release.Name] = release
	}
	result := make([]componentLifecycleItem, 0)
	appendItem := func(key, name, namespace, release, prefix string) {
		desired := enabledPath(doc, prefix+".enabled")
		actual, exists := installed[namespace+"/"+release]
		if !desired && !exists {
			return
		}
		action := "安装"
		switch {
		case desired && exists && strings.EqualFold(actual.Status, "deployed"):
			action = "更新/对账"
		case desired && exists:
			action = "修复"
		case !desired && exists:
			action = "卸载"
		}
		result = append(result, componentLifecycleItem{
			Key: key, Name: name, Action: action, Namespace: namespace, Release: release,
			Status: actual.Status, Retention: componentLifecycleRetention(doc, key, prefix), Related: componentLifecycleRelated(key),
		})
	}

	if phase == "base" {
		for _, item := range []struct{ key, name string }{{"consul", "Consul"}, {"etcd", "etcd"}} {
			prefix := "components." + item.key
			appendItem(item.key, item.name, defaultString(documentString(doc, prefix+".namespace"), "platform-server"), item.key, prefix)
		}
		return result
	}

	catalogValue, _ := environment.GetPath(doc, "components.catalog")
	catalog, _ := catalogValue.(map[string]any)
	keys := make([]string, 0, len(catalog))
	for key := range catalog {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		config, ok := catalog[key].(map[string]any)
		if !ok {
			continue
		}
		name := defaultString(documentMapString(config, "display_name"), key)
		namespace := defaultString(documentMapString(config, "namespace"), "platform-server")
		release := defaultString(documentMapString(config, "release_name"), key)
		appendItem(key, name, namespace, release, "components.catalog."+key)
	}
	return result
}

func componentLifecycleRetention(doc environment.Document, key, prefix string) string {
	paths := []string{prefix + ".values.storage.retainOnDelete", prefix + ".values.persistence.retainOnDelete", prefix + ".values.elasticsearch.storage.retainOnDelete"}
	if key == "consul" || key == "etcd" {
		paths = []string{prefix + ".retain_pvc_on_delete"}
	}
	for _, path := range paths {
		if value, found := environment.GetPath(doc, path); found {
			if retain, valid := value.(bool); valid {
				if retain {
					return "PVC 保留"
				}
				return "PVC 删除"
			}
		}
	}
	return "数据卷按 Chart/Kubernetes 默认策略"
}

func componentLifecycleRelated(key string) string {
	items := map[string]string{
		"consul":            "Helm、Service、TLS/ACL 和备份任务（S3 已有备份保留）",
		"etcd":              "Helm、Service、WebUI、TLS/Secret 和备份任务（S3 已有备份保留）",
		"loki":              "Helm、Alloy 采集器、Grafana 数据源和日志 Dashboard",
		"prometheus":        "Helm、Grafana Dashboard 和 Alertmanager 路由",
		"clickvisual_stack": "Fluent Bit、Kafka、ClickHouse、ClickVisual、MySQL 和随机凭据",
		"efk_stack":         "Elasticsearch、Fluentd、Kibana 和随机凭据",
	}
	if value := items[key]; value != "" {
		return value
	}
	return "Helm Release 与平台生成的关联凭据"
}

func writeComponentLifecyclePlan(output io.Writer, phase string, items []componentLifecycleItem) {
	label := "阶段2"
	if phase == "base" {
		label = "阶段1"
	}
	_, _ = fmt.Fprintf(output, "\n==> %s 组件生命周期计划\n", label)
	if len(items) == 0 {
		_, _ = fmt.Fprintln(output, "未发现需要安装、更新或卸载的组件。")
		return
	}
	counts := map[string]int{}
	for _, item := range items {
		counts[item.Action]++
		status := ""
		if item.Status != "" {
			status = "，当前=" + item.Status
		}
		_, _ = fmt.Fprintf(output, "[%s] %s（%s/%s%s）；Namespace 永久保留；%s；关联资源：%s\n", item.Action, item.Name, item.Namespace, item.Release, status, item.Retention, item.Related)
	}
	_, _ = fmt.Fprintf(output, "计划汇总：安装 %d，更新/对账 %d，修复 %d，卸载 %d。\n", counts["安装"], counts["更新/对账"], counts["修复"], counts["卸载"])
}

type helmReleaseRevision struct {
	Revision int    `json:"revision"`
	Status   string `json:"status"`
}

// reconcileInterruptedBaseServices clears Helm's pending-operation lock for
// tracked Consul/etcd releases after an interrupted upgrade. It always rolls
// back to the latest known successful revision first, preserving StatefulSet
// PVCs and data, and lets the subsequent Terraform saved plan perform the
// intended upgrade from a stable release state.
func (d *Deployment) reconcileInterruptedBaseServices(ctx context.Context, doc environment.Document, kubeconfig string, output io.Writer) error {
	jobs.StepStarted(ctx, stepReconcileBaseReleases)
	_, _ = fmt.Fprintf(output, "\n==> %s\n", stepReconcileBaseReleases)

	var inventory bytes.Buffer
	listCommand := Command{
		Name: d.config.Tools.Helm, Args: helmInventoryArgs(),
		Dir: d.config.Paths.RepositoryRoot, Env: d.commandEnv(ctx, kubeconfig),
	}
	_, _ = fmt.Fprintf(output, "$ %s %s\n", listCommand.Name, strings.Join(listCommand.Args, " "))
	if err := d.executor.Run(ctx, listCommand, &inventory); err != nil {
		jobs.StepFinished(ctx, stepReconcileBaseReleases, err)
		return fmt.Errorf("cannot inspect Helm releases before base-service update: %w", err)
	}
	var releases []helmListRelease
	if err := json.Unmarshal(inventory.Bytes(), &releases); err != nil {
		jobs.StepFinished(ctx, stepReconcileBaseReleases, err)
		return fmt.Errorf("cannot parse Helm release inventory before base-service update: %w", err)
	}
	writeComponentLifecyclePlan(output, "base", componentLifecyclePlan(doc, "base", releases))
	releaseByScope := make(map[string]helmListRelease, len(releases))
	for _, release := range releases {
		releaseByScope[release.Namespace+"/"+release.Name] = release
	}

	repaired := 0
	for _, component := range []string{"consul", "etcd"} {
		prefix := "components." + component
		if !enabledPath(doc, prefix+".enabled") {
			continue
		}
		namespace := documentString(doc, prefix+".namespace")
		release, exists := releaseByScope[namespace+"/"+component]
		if !exists || !isPendingHelmStatus(release.Status) {
			continue
		}

		historyCommand := Command{
			Name: d.config.Tools.Helm,
			Args: []string{"history", component, "--namespace", namespace, "--output", "json"},
			Dir:  d.config.Paths.RepositoryRoot, Env: d.commandEnv(ctx, kubeconfig),
		}
		_, _ = fmt.Fprintf(output, "检测到 %s/%s 处于 %s；正在查找最近成功 revision。\n", namespace, component, release.Status)
		_, _ = fmt.Fprintf(output, "$ %s %s\n", historyCommand.Name, strings.Join(historyCommand.Args, " "))
		var history bytes.Buffer
		if err := d.executor.Run(ctx, historyCommand, &history); err != nil {
			jobs.StepFinished(ctx, stepReconcileBaseReleases, err)
			return fmt.Errorf("cannot inspect interrupted base release %s/%s history: %w", namespace, component, err)
		}
		revision, ok := latestStableHelmRevision(history.Bytes())
		if !ok {
			err := fmt.Errorf("base release %s/%s is %s but has no prior successful revision; automatic rollback was blocked to protect its PVC data", namespace, component, release.Status)
			jobs.StepFinished(ctx, stepReconcileBaseReleases, err)
			return err
		}
		rollbackCommand := Command{
			Name: d.config.Tools.Helm,
			Args: []string{"rollback", component, strconv.Itoa(revision), "--namespace", namespace, "--wait", "--timeout", "5m", "--cleanup-on-fail"},
			Dir:  d.config.Paths.RepositoryRoot, Env: d.commandEnv(ctx, kubeconfig),
		}
		_, _ = fmt.Fprintf(output, "$ %s rollback %s %d --namespace %s --wait --timeout 5m --cleanup-on-fail\n", rollbackCommand.Name, component, revision, namespace)
		if err := d.executor.Run(ctx, rollbackCommand, output); err != nil {
			jobs.StepFinished(ctx, stepReconcileBaseReleases, err)
			return fmt.Errorf("failed to roll back interrupted base release %s/%s to revision %d: %w", namespace, component, revision, err)
		}
		_, _ = fmt.Fprintf(output, "%s/%s 已恢复到成功 revision %d，接下来由 Terraform 继续增量升级。\n", namespace, component, revision)
		repaired++
	}
	if repaired == 0 {
		_, _ = fmt.Fprintln(output, "Consul/etcd 没有 pending Helm 操作，可以继续增量部署。")
	}
	jobs.StepFinished(ctx, stepReconcileBaseReleases, nil)
	return nil
}

func isPendingHelmStatus(status string) bool {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "pending-install", "pending-upgrade", "pending-rollback":
		return true
	default:
		return false
	}
}

func latestStableHelmRevision(payload []byte) (int, bool) {
	var history []helmReleaseRevision
	if err := json.Unmarshal(payload, &history); err != nil {
		return 0, false
	}
	revision := 0
	for _, item := range history {
		status := strings.ToLower(strings.TrimSpace(item.Status))
		if (status == "deployed" || status == "superseded") && item.Revision > revision {
			revision = item.Revision
		}
	}
	return revision, revision > 0
}

type interruptedDataService struct {
	Key          string
	Release      string
	Namespace    string
	Status       string
	FreshInstall bool
}

type kubernetesPVCList struct {
	Items []struct {
		Metadata struct {
			Name        string            `json:"name"`
			Annotations map[string]string `json:"annotations"`
		} `json:"metadata"`
	} `json:"items"`
}

type kubernetesStatefulSetRetention struct {
	Spec struct {
		PersistentVolumeClaimRetentionPolicy struct {
			WhenDeleted string `json:"whenDeleted"`
		} `json:"persistentVolumeClaimRetentionPolicy"`
	} `json:"spec"`
}

func (d *Deployment) reconcileInterruptedDataServices(ctx context.Context, name string, doc environment.Document, kubeconfig string, output io.Writer) error {
	jobs.StepStarted(ctx, stepReconcileReleases)
	_, _ = fmt.Fprintf(output, "\n==> %s\n", stepReconcileReleases)

	var stateOutput bytes.Buffer
	stateCommand := Command{
		Name: d.config.Tools.Terraform, Args: []string{"state", "list"},
		Dir: d.config.Paths.TerraformPlatformDir, Env: d.terraformEnv(ctx, name, d.config.Paths.TerraformPlatformDir),
	}
	_, _ = fmt.Fprintf(output, "$ %s %s\n", stateCommand.Name, strings.Join(stateCommand.Args, " "))
	if err := d.executor.Run(ctx, stateCommand, &stateOutput); err != nil {
		jobs.StepFinished(ctx, stepReconcileReleases, err)
		return fmt.Errorf("cannot inspect Terraform state before component retry: %w", err)
	}

	var helmOutput bytes.Buffer
	helmCommand := Command{
		Name: d.config.Tools.Helm, Args: helmInventoryArgs(),
		Dir: d.config.Paths.RepositoryRoot, Env: d.commandEnv(ctx, kubeconfig),
	}
	_, _ = fmt.Fprintf(output, "$ %s %s\n", helmCommand.Name, strings.Join(helmCommand.Args, " "))
	if err := d.executor.Run(ctx, helmCommand, &helmOutput); err != nil {
		jobs.StepFinished(ctx, stepReconcileReleases, err)
		return fmt.Errorf("cannot inspect Helm releases before component retry: %w", err)
	}
	var releases []helmListRelease
	if err := json.Unmarshal(helmOutput.Bytes(), &releases); err != nil {
		jobs.StepFinished(ctx, stepReconcileReleases, err)
		return fmt.Errorf("cannot parse Helm release inventory: %w", err)
	}
	writeComponentLifecyclePlan(output, "components", componentLifecyclePlan(doc, "components", releases))
	for _, repair := range trackedInterruptedCatalogRepairs(doc, stateOutput.String(), releases) {
		_, _ = fmt.Fprintf(output, "检测到 Terraform 已管理的组件 %s/%s 处于 %s；先回滚到最近成功 revision，再由本次 Terraform 增量更新。不会卸载组件或删除 PVC。\n", repair.Namespace, repair.Release, repair.Status)
		historyCommand := Command{
			Name: d.config.Tools.Helm,
			Args: []string{"history", repair.Release, "--namespace", repair.Namespace, "--output", "json"},
			Dir:  d.config.Paths.RepositoryRoot, Env: d.commandEnv(ctx, kubeconfig),
		}
		var history bytes.Buffer
		if err := d.executor.Run(ctx, historyCommand, &history); err != nil {
			jobs.StepFinished(ctx, stepReconcileReleases, err)
			return fmt.Errorf("cannot inspect interrupted component %s/%s history: %w", repair.Namespace, repair.Release, err)
		}
		revision, ok := latestStableHelmRevision(history.Bytes())
		// A failed Helm revision does not hold an operation lock, including a
		// failed first install with no deployed history. Helm upgrade can converge
		// that release in place. Do this before the no-history recovery branch so
		// stateless charts such as Higress are never rejected merely because they
		// do not have a PVC retention policy, and stateful charts keep their bound
		// PVCs without an uninstall/rollback cycle.
		if strings.EqualFold(strings.TrimSpace(repair.Status), "failed") {
			_, _ = fmt.Fprintf(output, "%s/%s 当前为 failed，未持有 Helm 操作锁；直接由本次 Terraform 原地增量更新收敛，不卸载 Release、不修改或替换 PVC。\n", repair.Namespace, repair.Release)
			continue
		}
		if !ok {
			if !trackedCatalogReleaseSupportsPVCPreservingReinstall(doc, repair) {
				err := fmt.Errorf("managed component %s/%s is %s but has no prior successful Helm revision; automatic replacement was blocked because PVC retention cannot be verified", repair.Namespace, repair.Release, repair.Status)
				jobs.StepFinished(ctx, stepReconcileReleases, err)
				return err
			}
			if err := d.verifyTrackedCatalogPVCProtection(ctx, repair, kubeconfig, output); err != nil {
				jobs.StepFinished(ctx, stepReconcileReleases, err)
				return err
			}
			_, _ = fmt.Fprintf(output, "%s/%s 没有可回滚的成功 revision；已确认这是平台内置组件且 PVC 保留策略已开启。将只清理失败的 Helm Release 和工作负载，保留全部 PVC，再由 Terraform 原地重建。不会删除 Namespace。\n", repair.Namespace, repair.Release)
			uninstallCommand := Command{
				Name: d.config.Tools.Helm,
				Args: []string{"uninstall", repair.Release, "--namespace", repair.Namespace, "--wait", "--timeout", "5m"},
				Dir:  d.config.Paths.RepositoryRoot, Env: d.commandEnv(ctx, kubeconfig),
			}
			_, _ = fmt.Fprintf(output, "$ %s uninstall %s --namespace %s --wait --timeout 5m\n", uninstallCommand.Name, repair.Release, repair.Namespace)
			if err := d.executor.Run(ctx, uninstallCommand, output); err != nil {
				jobs.StepFinished(ctx, stepReconcileReleases, err)
				return fmt.Errorf("failed to remove interrupted Helm metadata for managed component %s/%s while preserving PVCs: %w", repair.Namespace, repair.Release, err)
			}
			_, _ = fmt.Fprintf(output, "%s/%s 的失败 Release 已清理，PVC 已保留；本次 Terraform 将重新创建工作负载并复用原数据卷。\n", repair.Namespace, repair.Release)
			continue
		}
		rollbackCommand := Command{
			Name: d.config.Tools.Helm,
			Args: []string{"rollback", repair.Release, strconv.Itoa(revision), "--namespace", repair.Namespace, "--wait", "--timeout", "5m", "--cleanup-on-fail"},
			Dir:  d.config.Paths.RepositoryRoot, Env: d.commandEnv(ctx, kubeconfig),
		}
		_, _ = fmt.Fprintf(output, "$ %s rollback %s %d --namespace %s --wait --timeout 5m --cleanup-on-fail\n", rollbackCommand.Name, repair.Release, revision, repair.Namespace)
		if err := d.executor.Run(ctx, rollbackCommand, output); err != nil {
			jobs.StepFinished(ctx, stepReconcileReleases, err)
			return fmt.Errorf("failed to roll back managed component %s/%s to revision %d: %w", repair.Namespace, repair.Release, revision, err)
		}
		_, _ = fmt.Fprintf(output, "%s/%s 已恢复到成功 revision %d，现有工作负载和 PVC 均保留。\n", repair.Namespace, repair.Release, revision)
	}

	repairs, conflicts := interruptedDataServiceRepairs(doc, stateOutput.String(), releases)
	if len(conflicts) > 0 {
		err := fmt.Errorf("healthy Helm releases are not recorded in Terraform state: %s; import them before retry so the platform never deletes a running service", strings.Join(conflicts, ", "))
		jobs.StepFinished(ctx, stepReconcileReleases, err)
		return err
	}
	if len(repairs) == 0 {
		_, _ = fmt.Fprintln(output, "未发现需要修复的中断组件，可以继续部署。")
		jobs.StepFinished(ctx, stepReconcileReleases, nil)
		return nil
	}
	for _, repair := range repairs {
		pvcNames := make([]string, 0)
		if repair.FreshInstall {
			var err error
			pvcNames, err = d.freshInstallPVCNames(ctx, repair, kubeconfig, output)
			if err != nil {
				jobs.StepFinished(ctx, stepReconcileReleases, err)
				return err
			}
			_, _ = fmt.Fprintf(output, "检测到 %s/%s 处于 pending-install 且未进入 Terraform State，判定为首次安装中断；将重建该组件并清理它自己的新建 PVC。\n", repair.Namespace, repair.Release)
		} else {
			_, _ = fmt.Fprintf(output, "检测到 %s/%s 处于 %s 且未进入 Terraform State；正在清理失败的 Helm Release 并保留 PVC 数据卷。\n", repair.Namespace, repair.Release, repair.Status)
		}
		command := Command{
			Name: d.config.Tools.Helm,
			Args: []string{"uninstall", repair.Release, "--namespace", repair.Namespace, "--wait", "--timeout", "5m"},
			Dir:  d.config.Paths.RepositoryRoot, Env: d.commandEnv(ctx, kubeconfig),
		}
		_, _ = fmt.Fprintf(output, "$ %s uninstall %s --namespace %s --wait --timeout 5m\n", command.Name, repair.Release, repair.Namespace)
		if err := d.executor.Run(ctx, command, output); err != nil {
			jobs.StepFinished(ctx, stepReconcileReleases, err)
			return fmt.Errorf("failed to clean interrupted built-in component %s: %w", repair.Key, err)
		}
		if len(pvcNames) > 0 {
			_, _ = fmt.Fprintf(output, "正在删除首次安装中断留下的 PVC：%s\n", strings.Join(pvcNames, "、"))
			args := []string{"delete", "persistentvolumeclaim"}
			args = append(args, pvcNames...)
			args = append(args, "--namespace", repair.Namespace, "--ignore-not-found=true", "--wait=true", "--timeout=5m")
			deleteCommand := Command{
				Name: d.config.Tools.Kubectl, Args: args,
				Dir: d.config.Paths.RepositoryRoot, Env: d.commandEnv(ctx, kubeconfig),
			}
			_, _ = fmt.Fprintf(output, "$ %s delete persistentvolumeclaim %s --namespace %s --ignore-not-found=true --wait=true --timeout=5m\n", deleteCommand.Name, strings.Join(pvcNames, " "), repair.Namespace)
			if err := d.executor.Run(ctx, deleteCommand, output); err != nil {
				jobs.StepFinished(ctx, stepReconcileReleases, err)
				return fmt.Errorf("清理首次安装中断的 PVC 失败（%s/%s）: %w", repair.Namespace, repair.Release, err)
			}
		} else if repair.FreshInstall {
			_, _ = fmt.Fprintln(output, "该首次安装没有留下组件 PVC，无需删除。")
		}
	}
	jobs.StepFinished(ctx, stepReconcileReleases, nil)
	return nil
}

func (d *Deployment) freshInstallPVCNames(ctx context.Context, repair interruptedDataService, kubeconfig string, output io.Writer) ([]string, error) {
	command := Command{
		Name: d.config.Tools.Kubectl,
		Args: []string{"get", "persistentvolumeclaim", "--namespace", repair.Namespace, "--output", "json"},
		Dir:  d.config.Paths.RepositoryRoot,
		Env:  d.commandEnv(ctx, kubeconfig),
	}
	_, _ = fmt.Fprintf(output, "$ %s get persistentvolumeclaim --namespace %s --output json\n", command.Name, repair.Namespace)
	var payload bytes.Buffer
	if err := d.executor.Run(ctx, command, &payload); err != nil {
		return nil, fmt.Errorf("无法在首次安装重试前读取 PVC 清单（%s/%s）: %w", repair.Namespace, repair.Release, err)
	}
	return freshInstallPVCNames(repair.Release, payload.Bytes())
}

func freshInstallPVCNames(release string, payload []byte) ([]string, error) {
	var list kubernetesPVCList
	if err := json.Unmarshal(payload, &list); err != nil {
		return nil, fmt.Errorf("无法解析首次安装的 PVC 清单: %w", err)
	}
	prefix := "data-" + release + "-"
	result := make([]string, 0)
	for _, item := range list.Items {
		if !strings.HasPrefix(item.Metadata.Name, prefix) {
			continue
		}
		ordinal := strings.TrimPrefix(item.Metadata.Name, prefix)
		if _, err := strconv.ParseUint(ordinal, 10, 32); err == nil {
			result = append(result, item.Metadata.Name)
		}
	}
	sort.Strings(result)
	return result, nil
}

func helmInventoryArgs() []string {
	// Helm 4 removed the Helm 3 --all shortcut. Explicit status filters work in
	// both versions and include every state relevant to safe reconciliation.
	return []string{"list", "--all-namespaces", "--deployed", "--failed", "--pending", "--output", "json"}
}

func interruptedDataServiceRepairs(doc environment.Document, terraformState string, releases []helmListRelease) ([]interruptedDataService, []string) {
	releaseByScope := make(map[string]helmListRelease, len(releases))
	for _, release := range releases {
		releaseByScope[release.Namespace+"/"+release.Name] = release
	}
	var repairs []interruptedDataService
	var conflicts []string
	dataServices := map[string]bool{
		"mysql": true, "redis": true, "activemq": true, "mongodb": true, "rabbitmq": true, "bytebase": true, "redisinsight": true, "etcd_workbench": true,
	}
	for _, key := range []string{"mysql", "redis", "activemq", "mongodb", "rabbitmq", "bytebase", "redisinsight", "etcd_workbench", "prometheus", "loki", "clickvisual_stack", "efk_stack"} {
		prefix := "components.catalog." + key
		builtinChart := documentString(doc, prefix+".builtin_chart")
		if !enabledPath(doc, prefix+".enabled") {
			continue
		}
		if dataServices[key] {
			expectedChart := map[string]string{
				"mysql": "data-service", "redis": "data-service", "activemq": "data-service", "mongodb": "data-service",
				"rabbitmq": "rabbitmq", "bytebase": "bytebase", "redisinsight": "redisinsight", "etcd_workbench": "etcd-workbench",
			}[key]
			if builtinChart != expectedChart {
				continue
			}
			storageEnabled := boolAt(doc, prefix+".values.storage.enabled") || boolAt(doc, prefix+".values.persistence.enabled")
			retainOnDelete := boolAt(doc, prefix+".values.storage.retainOnDelete") || boolAt(doc, prefix+".values.persistence.retainOnDelete")
			if storageEnabled && !retainOnDelete {
				continue
			}
		} else {
			// Only reconcile platform-supported observability charts. A custom
			// chart using the same catalog key is never removed implicitly. EFK
			// and ClickVisual are bundled charts; their failed first install is
			// removed before retry while every PVC is deliberately retained.
			if expectedBuiltin := map[string]string{"clickvisual_stack": "clickvisual-stack", "efk_stack": "efk-stack"}[key]; expectedBuiltin != "" {
				if builtinChart != expectedBuiltin {
					continue
				}
			} else {
				expectedChart := map[string]string{"prometheus": "kube-prometheus-stack", "loki": "loki"}[key]
				if builtinChart != "" || documentString(doc, prefix+".chart") != expectedChart {
					continue
				}
			}
		}
		releaseName := documentString(doc, prefix+".release_name")
		namespace := documentString(doc, prefix+".namespace")
		if releaseName == "" || namespace == "" || strings.Contains(terraformState, fmt.Sprintf(`helm_release.catalog["%s"]`, key)) {
			continue
		}
		release, exists := releaseByScope[namespace+"/"+releaseName]
		if !exists {
			continue
		}
		status := strings.ToLower(strings.TrimSpace(release.Status))
		switch status {
		case "pending-install", "pending-upgrade", "pending-rollback", "failed":
			repairs = append(repairs, interruptedDataService{
				Key: key, Release: releaseName, Namespace: namespace, Status: status,
				// Data-service PVCs created by a first interrupted install may be
				// safely recreated. Monitoring PVCs are always retained because
				// they can contain metrics/log history from a prior manual install.
				FreshInstall: dataServices[key] && status == "pending-install",
			})
		case "deployed":
			conflicts = append(conflicts, namespace+"/"+releaseName)
		}
	}
	if enabledPath(doc, "components.catalog.opentelemetry_collector.enabled") && boolAt(doc, "components.catalog.opentelemetry_collector.values.elasticsearch.enabled") && !strings.Contains(terraformState, "helm_release.otel_elasticsearch") {
		namespace := documentString(doc, "components.catalog.opentelemetry_collector.namespace")
		if namespace == "" {
			namespace = "monitoring"
		}
		if release, exists := releaseByScope[namespace+"/otel-elasticsearch"]; exists {
			status := strings.ToLower(strings.TrimSpace(release.Status))
			switch status {
			case "pending-install", "pending-upgrade", "pending-rollback", "failed":
				repairs = append(repairs, interruptedDataService{Key: "otel_elasticsearch", Release: "otel-elasticsearch", Namespace: namespace, Status: status})
			case "deployed":
				conflicts = append(conflicts, namespace+"/otel-elasticsearch")
			}
		}
	}
	return repairs, conflicts
}

func trackedInterruptedCatalogRepairs(doc environment.Document, terraformState string, releases []helmListRelease) []interruptedDataService {
	releaseByScope := make(map[string]helmListRelease, len(releases))
	for _, release := range releases {
		releaseByScope[release.Namespace+"/"+release.Name] = release
	}
	catalogValue, _ := environment.GetPath(doc, "components.catalog")
	catalog, _ := catalogValue.(map[string]any)
	keys := make([]string, 0, len(catalog))
	for key := range catalog {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	result := make([]interruptedDataService, 0)
	for _, key := range keys {
		component, ok := catalog[key].(map[string]any)
		if !ok {
			continue
		}
		enabled, _ := component["enabled"].(bool)
		if !enabled || !strings.Contains(terraformState, fmt.Sprintf(`helm_release.catalog["%s"]`, key)) {
			continue
		}
		releaseName, _ := component["release_name"].(string)
		releaseName = strings.TrimSpace(releaseName)
		if releaseName == "" {
			releaseName = key
		}
		namespace, _ := component["namespace"].(string)
		namespace = strings.TrimSpace(namespace)
		if namespace == "" {
			continue
		}
		release, exists := releaseByScope[namespace+"/"+releaseName]
		if !exists {
			continue
		}
		status := strings.ToLower(strings.TrimSpace(release.Status))
		switch status {
		case "failed", "pending-install", "pending-upgrade", "pending-rollback":
			result = append(result, interruptedDataService{Key: key, Release: releaseName, Namespace: namespace, Status: status})
		}
	}
	return result
}

// trackedCatalogReleaseSupportsPVCPreservingReinstall deliberately permits
// automatic first-install recovery only for bundled charts whose PVC lifetime
// is controlled by the platform and whose saved configuration explicitly
// retains those PVCs. A custom chart, a renamed built-in chart, or a disabled
// retention policy must continue to fail closed: uninstalling an arbitrary
// failed Helm release is not a safe generic recovery mechanism.
func trackedCatalogReleaseSupportsPVCPreservingReinstall(doc environment.Document, repair interruptedDataService) bool {
	prefix := "components.catalog." + repair.Key
	switch repair.Key {
	case "bytebase":
		return documentString(doc, prefix+".builtin_chart") == "bytebase" &&
			boolAt(doc, prefix+".values.persistence.retainOnDelete")
	case "clickvisual_stack":
		return documentString(doc, prefix+".builtin_chart") == "clickvisual-stack" &&
			boolAt(doc, prefix+".values.storage.retainOnDelete")
	case "efk_stack":
		return documentString(doc, prefix+".builtin_chart") == "efk-stack" &&
			boolAt(doc, prefix+".values.elasticsearch.storage.retainOnDelete")
	default:
		return false
	}
}

func (d *Deployment) verifyTrackedCatalogPVCProtection(ctx context.Context, repair interruptedDataService, kubeconfig string, output io.Writer) error {
	if repair.Key == "bytebase" {
		statefulSetCommand := Command{
			Name: d.config.Tools.Kubectl,
			Args: []string{"get", "statefulset", repair.Release, "--namespace", repair.Namespace, "--output", "json"},
			Dir:  d.config.Paths.RepositoryRoot, Env: d.commandEnv(ctx, kubeconfig),
		}
		_, _ = fmt.Fprintf(output, "$ %s get statefulset %s --namespace %s --output json\n", statefulSetCommand.Name, repair.Release, repair.Namespace)
		var statefulSetPayload bytes.Buffer
		if err := d.executor.Run(ctx, statefulSetCommand, &statefulSetPayload); err != nil {
			return fmt.Errorf("cannot verify Bytebase StatefulSet PVC retention before recovering %s/%s: %w", repair.Namespace, repair.Release, err)
		}
		var statefulSet kubernetesStatefulSetRetention
		if err := json.Unmarshal(statefulSetPayload.Bytes(), &statefulSet); err != nil {
			return fmt.Errorf("cannot parse Bytebase StatefulSet PVC retention before recovering %s/%s: %w", repair.Namespace, repair.Release, err)
		}
		if !strings.EqualFold(strings.TrimSpace(statefulSet.Spec.PersistentVolumeClaimRetentionPolicy.WhenDeleted), "Retain") {
			return fmt.Errorf("StatefulSet %s/%s does not retain PVCs when deleted; automatic recovery was blocked", repair.Namespace, repair.Release)
		}
		_, _ = fmt.Fprintf(output, "已验证 Bytebase StatefulSet %s/%s 在失败 Release 清理时保留 PVC。\n", repair.Namespace, repair.Release)
		return nil
	}

	stack := map[string]string{"clickvisual_stack": "clickvisual", "efk_stack": "efk"}[repair.Key]
	if stack == "" {
		return fmt.Errorf("cannot verify PVC protection for unsupported managed component %s/%s", repair.Namespace, repair.Release)
	}
	command := Command{
		Name: d.config.Tools.Kubectl,
		Args: []string{"get", "persistentvolumeclaim", "--namespace", repair.Namespace, "--selector", "ops-deploy.io/stack=" + stack, "--output", "json"},
		Dir:  d.config.Paths.RepositoryRoot, Env: d.commandEnv(ctx, kubeconfig),
	}
	_, _ = fmt.Fprintf(output, "$ %s get persistentvolumeclaim --namespace %s --selector ops-deploy.io/stack=%s --output json\n", command.Name, repair.Namespace, stack)
	var payload bytes.Buffer
	if err := d.executor.Run(ctx, command, &payload); err != nil {
		return fmt.Errorf("cannot verify retained PVCs before recovering %s/%s: %w", repair.Namespace, repair.Release, err)
	}
	var claims kubernetesPVCList
	if err := json.Unmarshal(payload.Bytes(), &claims); err != nil {
		return fmt.Errorf("cannot parse retained PVC inventory before recovering %s/%s: %w", repair.Namespace, repair.Release, err)
	}
	if len(claims.Items) == 0 {
		_, _ = fmt.Fprintln(output, "失败安装没有留下平台日志栈 PVC，可以安全重建 Helm Release。")
		return nil
	}

	if repair.Key == "clickvisual_stack" {
		for _, claim := range claims.Items {
			if claim.Metadata.Annotations["helm.sh/resource-policy"] != "keep" {
				return fmt.Errorf("PVC %s/%s does not have helm.sh/resource-policy=keep; automatic recovery was blocked", repair.Namespace, claim.Metadata.Name)
			}
		}
		_, _ = fmt.Fprintf(output, "已验证 %d 个 ClickVisual PVC 均带有 Helm keep 保护。\n", len(claims.Items))
		return nil
	}

	statefulSetCommand := Command{
		Name: d.config.Tools.Kubectl,
		Args: []string{"get", "statefulset", "efk-elasticsearch", "--namespace", repair.Namespace, "--output", "json"},
		Dir:  d.config.Paths.RepositoryRoot, Env: d.commandEnv(ctx, kubeconfig),
	}
	var statefulSetPayload bytes.Buffer
	if err := d.executor.Run(ctx, statefulSetCommand, &statefulSetPayload); err != nil {
		return fmt.Errorf("cannot verify EFK StatefulSet PVC retention before recovering %s/%s: %w", repair.Namespace, repair.Release, err)
	}
	var statefulSet kubernetesStatefulSetRetention
	if err := json.Unmarshal(statefulSetPayload.Bytes(), &statefulSet); err != nil {
		return fmt.Errorf("cannot parse EFK StatefulSet PVC retention before recovering %s/%s: %w", repair.Namespace, repair.Release, err)
	}
	if !strings.EqualFold(strings.TrimSpace(statefulSet.Spec.PersistentVolumeClaimRetentionPolicy.WhenDeleted), "Retain") {
		return fmt.Errorf("StatefulSet %s/efk-elasticsearch does not retain PVCs when deleted; automatic recovery was blocked", repair.Namespace)
	}
	_, _ = fmt.Fprintf(output, "已验证 EFK StatefulSet 会保留 %d 个 Elasticsearch PVC。\n", len(claims.Items))
	return nil
}

func boolAt(doc environment.Document, path string) bool {
	value, _ := environment.GetPath(doc, path)
	result, _ := value.(bool)
	return result
}

func (d *Deployment) destroy(ctx context.Context, name string, doc environment.Document, output io.Writer) error {
	if environment.IsExistingEKS(doc) {
		if err := d.terraformInit(ctx, d.config.Paths.TerraformPlatformDir, name, output); err != nil {
			return err
		}
		if err := d.terraformWorkspace(ctx, d.config.Paths.TerraformPlatformDir, name, output); err != nil {
			return err
		}
		if _, err := d.updateKubeconfig(ctx, name, doc, output); err != nil {
			return fmt.Errorf("cannot safely uninstall components from existing EKS: %w", err)
		}
		_, _ = fmt.Fprintln(output, "检测到已有 EKS：只卸载平台管理的组件与接入资源；EKS、Namespace、VPC 和节点组全部保留。")
		if err := d.destroyExistingEKSComponents(ctx, name, output); err != nil {
			return err
		}
		if err := d.detachProtectedExistingEKSState(ctx, name, output); err != nil {
			return err
		}
		return d.persistRemoteStateMetadata(ctx, d.config.Paths.TerraformPlatformDir, name)
	}
	if err := d.terraformInit(ctx, d.config.Paths.TerraformInfraDir, name, output); err != nil {
		return err
	}
	if err := d.terraformWorkspace(ctx, d.config.Paths.TerraformInfraDir, name, output); err != nil {
		return err
	}
	if err := d.terraformInit(ctx, d.config.Paths.TerraformPlatformDir, name, output); err != nil {
		return err
	}
	if err := d.terraformWorkspace(ctx, d.config.Paths.TerraformPlatformDir, name, output); err != nil {
		return err
	}
	kubeconfig, kubeErr := d.updateKubeconfig(ctx, name, doc, output)
	if kubeErr == nil {
		project := documentString(doc, "project")
		if err := d.step(ctx, output, stepDeleteNamespaces, Command{
			Name: d.config.Tools.Kubectl,
			Args: []string{
				"delete", "namespace",
				"-l", "app.kubernetes.io/part-of=" + project + ",environment=" + documentString(doc, "environment"),
				"--ignore-not-found=true", "--wait=true", "--timeout=15m",
			},
			Dir: d.config.Paths.RepositoryRoot,
			Env: d.commandEnv(ctx, kubeconfig),
		}); err != nil {
			return err
		}
	} else {
		_, _ = fmt.Fprintf(output, "Kubernetes cleanup skipped because kubeconfig could not be updated: %v\n", kubeErr)
		// A destroy retry commonly runs after the EKS control plane was removed
		// by the previous attempt. In that state Kubernetes cleanup is a safe
		// no-op, not a deployment failure.
		jobs.StepFinished(ctx, stepUpdateKubeconfig, nil)
		jobs.StepStarted(ctx, stepDeleteNamespaces)
		_, _ = fmt.Fprintln(output, "Kubernetes namespace cleanup is not required because the EKS cluster is already unavailable.")
		jobs.StepFinished(ctx, stepDeleteNamespaces, nil)
	}
	if err := d.terraformApply(ctx, d.config.Paths.TerraformPlatformDir, name, true, "components", output); err != nil {
		return err
	}
	// Delete EKS compute before the VPC. The VPC CNI can leave secondary ENIs
	// behind for a short time after worker nodes disappear. A monolithic
	// terraform destroy races that asynchronous cleanup, waits for every subnet
	// deletion timeout and only then gets a chance to recover. Staging the
	// cluster/node-group deletion lets us clean those ENIs while the VPC is
	// still tracked and then remove the remaining network in one pass.
	if err := d.destroyManagedEKSCompute(ctx, name, output); err != nil {
		return err
	}
	if err := d.cleanupOrphanedEKSNetworkResources(ctx, name, doc, output); err != nil {
		return err
	}
	if err := d.terraformApply(ctx, d.config.Paths.TerraformInfraDir, name, true, "", output); err == nil {
		return nil
	} else {
		_, _ = fmt.Fprintf(output, "Infrastructure destroy needs one recovery pass: %v\n", err)
	}
	if err := d.cleanupOrphanedEKSNetworkResources(ctx, name, doc, output); err != nil {
		return err
	}
	return d.terraformApplyNamed(ctx, d.config.Paths.TerraformInfraDir, name, true, "", stepRetryDestroyInfra, output)
}

func (d *Deployment) destroyManagedEKSCompute(ctx context.Context, name string, output io.Writer) error {
	args := []string{
		"destroy", "-input=false", "-auto-approve", "-no-color",
		"-var=config_file=" + d.environments.Path(name),
		"-target=aws_eks_node_group.this",
		"-target=aws_eks_cluster.this",
	}
	return d.step(ctx, output, stepDestroyEKSCompute, Command{
		Name: d.config.Tools.Terraform,
		Args: args,
		Dir:  d.config.Paths.TerraformInfraDir,
		Env:  d.terraformEnv(ctx, name, d.config.Paths.TerraformInfraDir),
	})
}

func (d *Deployment) destroyExistingEKSComponents(ctx context.Context, name string, output io.Writer) error {
	targets := existingEKSComponentDestroyTargets()
	args := []string{
		"destroy", "-input=false", "-auto-approve", "-no-color",
		"-var=config_file=" + d.environments.Path(name), "-var=deployment_phase=components",
	}
	for _, target := range targets {
		args = append(args, "-target="+target)
	}
	_, _ = fmt.Fprintln(output, "Namespace 永久删除保护：本次只卸载平台组件与关联资源，所有 Namespace 均保留。")
	return d.step(ctx, output, stepDestroyPlatform, Command{
		Name: d.config.Tools.Terraform, Args: args, Dir: d.config.Paths.TerraformPlatformDir,
		Env: d.terraformEnv(ctx, name, d.config.Paths.TerraformPlatformDir),
	})
}

func existingEKSComponentDestroyTargets() []string {
	return []string{
		"helm_release.catalog", "helm_release.otel_elasticsearch", "helm_release.consul", "helm_release.etcd",
		"helm_release.loki_collector",
		"kubernetes_service_v1.consul_http", "random_password.etcd_web",
		"kubernetes_cron_job_v1.consul_backup", "kubernetes_cron_job_v1.etcd_backup",
		"kubernetes_service_account_v1.platform_backup", "aws_eks_pod_identity_association.platform_backup",
		"aws_iam_role_policy_attachment.platform_backup", "aws_iam_policy.platform_backup", "aws_iam_role.platform_backup",
		"tls_private_key.etcd_ca", "tls_self_signed_cert.etcd_ca", "tls_private_key.etcd",
		"tls_cert_request.etcd", "tls_locally_signed_cert.etcd", "kubernetes_secret_v1.etcd_tls",
		"random_password.xxl_job_mysql", "random_password.xxl_job_admin", "random_password.nacos_auth_token",
		"random_password.nacos_identity_value", "random_password.higress_admin",
		"random_password.data_service", "random_password.bytebase_admin", "random_password.redisinsight_admin",
		"random_password.redisinsight_encryption", "random_password.etcd_workbench_admin",
		"random_password.etcd_workbench_encryption",
		"random_password.clickvisual_mysql", "random_password.clickvisual_clickhouse", "random_password.clickvisual_admin",
		"random_password.clickvisual_proxy_token", "random_password.clickvisual_secret_key", "random_password.clickvisual_encryption_key",
		"random_id.clickvisual_kafka_cluster",
		"random_password.efk_elastic", "random_password.efk_kibana_system", "random_password.efk_fluentd",
		"random_password.efk_security_key", "random_password.efk_saved_objects_key", "random_password.efk_reporting_key",
		"random_password.jaeger_ui",
		"aws_security_group.higress_nlb", "aws_vpc_security_group_ingress_rule.higress_nlb_ipv4",
		"aws_vpc_security_group_ingress_rule.higress_nlb_ipv6", "aws_vpc_security_group_egress_rule.higress_nlb",
		"kubernetes_config_map_v1.grafana_loki_datasource", "kubernetes_config_map_v1.grafana_eks_dashboard",
		"kubernetes_config_map_v1.grafana_tempo_datasource", "kubernetes_secret_v1.grafana_elasticsearch_datasource",
		"kubernetes_secret_v1.grafana_otel_elasticsearch_datasource",
		"kubernetes_config_map_v1.grafana_logs_dashboard", "kubernetes_config_map_v1.grafana_traces_dashboard",
		"kubernetes_manifest.tls_certificate", "kubernetes_ingress_v1.domain", "kubernetes_service_v1.tcp_route",
		"kubernetes_config_map_v1.alerting", "kubernetes_secret_v1.alerting_channels",
	}
}

func (d *Deployment) detachProtectedExistingEKSState(ctx context.Context, name string, output io.Writer) error {
	jobs.StepStarted(ctx, stepDetachExistingEKS)
	_, _ = fmt.Fprintf(output, "\n==> %s\n", stepDetachExistingEKS)
	var stateList bytes.Buffer
	list := Command{
		Name: d.config.Tools.Terraform, Args: []string{"state", "list"}, Dir: d.config.Paths.TerraformPlatformDir,
		Env: d.terraformEnv(ctx, name, d.config.Paths.TerraformPlatformDir),
	}
	if err := d.executor.Run(ctx, list, &stateList); err != nil {
		jobs.StepFinished(ctx, stepDetachExistingEKS, err)
		return fmt.Errorf("list existing EKS Terraform state: %w", err)
	}
	protectedPrefixes := protectedExistingEKSStatePrefixes()
	addresses := make([]string, 0)
	for _, address := range strings.Fields(stateList.String()) {
		for _, prefix := range protectedPrefixes {
			if address == prefix || strings.HasPrefix(address, prefix+"[") {
				addresses = append(addresses, address)
				break
			}
		}
	}
	if len(addresses) == 0 {
		_, _ = fmt.Fprintln(output, "No shared EKS infrastructure or protected Namespace is tracked by this environment state.")
		jobs.StepFinished(ctx, stepDetachExistingEKS, nil)
		return nil
	}
	args := append([]string{"state", "rm"}, addresses...)
	_, _ = fmt.Fprintf(output, "Preserving %d shared EKS infrastructure/Namespace state entries without deleting the real resources.\n", len(addresses))
	err := d.executor.Run(ctx, Command{
		Name: d.config.Tools.Terraform, Args: args, Dir: d.config.Paths.TerraformPlatformDir,
		Env: d.terraformEnv(ctx, name, d.config.Paths.TerraformPlatformDir),
	}, output)
	jobs.StepFinished(ctx, stepDetachExistingEKS, err)
	if err != nil {
		return fmt.Errorf("detach protected existing EKS state: %w", err)
	}
	return nil
}

func protectedExistingEKSStatePrefixes() []string {
	return []string{
		"kubernetes_namespace_v1.this",
		"aws_eks_addon.base", "aws_eks_addon.ebs_csi", "aws_iam_role.ebs_csi",
		"aws_iam_role_policy_attachment.ebs_csi", "aws_eks_pod_identity_association.ebs_csi",
		"kubernetes_storage_class_v1.gp3", "helm_release.load_balancer_controller", "helm_release.metrics_server",
		"helm_release.cluster_autoscaler", "helm_release.external_dns", "helm_release.cert_manager",
		"aws_iam_role.load_balancer_controller", "aws_iam_policy.load_balancer_controller",
		"aws_iam_role_policy_attachment.load_balancer_controller", "aws_eks_pod_identity_association.load_balancer_controller",
		"aws_iam_role.cluster_autoscaler", "aws_iam_policy.cluster_autoscaler",
		"aws_iam_role_policy_attachment.cluster_autoscaler", "aws_eks_pod_identity_association.cluster_autoscaler",
		"aws_iam_role.external_dns", "aws_iam_policy.external_dns",
		"aws_iam_role_policy_attachment.external_dns", "aws_eks_pod_identity_association.external_dns",
	}
}

func (d *Deployment) terraformInit(ctx context.Context, dir, name string, output io.Writer) error {
	label := stepInitializePlatform
	if filepath.Base(dir) == filepath.Base(d.config.Paths.TerraformInfraDir) {
		label = stepInitializeInfra
	}
	args := terraformInitArgs()
	if d.config.TerraformState.Enabled {
		backendArgs, err := d.prepareRemoteStateBackend(ctx, dir, name, output)
		if err != nil {
			return fmt.Errorf("%s: prepare S3 remote state: %w", label, err)
		}
		args = append(args, backendArgs...)
	}
	return d.step(ctx, output, label, Command{
		Name: d.config.Tools.Terraform,
		// Production containers intentionally use a read-only root filesystem.
		// Provider versions and checksums are reviewed and committed at image
		// build time, so runtime initialization must never rewrite the module lock.
		Args: args,
		Dir:  dir,
		Env:  d.terraformDataEnv(ctx, name, dir),
	})
}

func terraformInitArgs() []string {
	return []string{"init", "-input=false", "-no-color", "-lockfile=readonly"}
}

type terraformStateSnapshot struct {
	Lineage   string                   `json:"lineage"`
	Serial    int64                    `json:"serial"`
	Resources []terraformStateResource `json:"resources"`
}

type terraformStateResource struct {
	Mode      string            `json:"mode"`
	Instances []json.RawMessage `json:"instances"`
}

type terraformStateLock struct {
	ID        string `json:"ID"`
	Operation string `json:"Operation"`
	Who       string `json:"Who"`
	Version   string `json:"Version"`
	Created   string `json:"Created"`
	Info      string `json:"Info"`
}

func (d *Deployment) checkTerraformStateLock(ctx context.Context, name, stage string, output io.Writer) error {
	jobs.StepStarted(ctx, stepCheckPlatformStateLock)
	_, _ = fmt.Fprintf(output, "\n==> %s\n", stepCheckPlatformStateLock)
	fail := func(err error) error {
		jobs.StepFinished(ctx, stepCheckPlatformStateLock, err)
		return err
	}
	if !d.config.TerraformState.Enabled {
		_, _ = fmt.Fprintln(output, "未启用统一 Terraform 状态中心，跳过远端状态锁检查。")
		jobs.StepFinished(ctx, stepCheckPlatformStateLock, nil)
		return nil
	}
	project, _ := ctx.Value(stateProjectContextKey{}).(string)
	project = strings.TrimSpace(strings.ToLower(project))
	name = strings.TrimSpace(strings.ToLower(name))
	stage = strings.TrimSpace(strings.ToLower(stage))
	runtime, ok := ctx.Value(stateBackendContextKey{}).(statebackend.Runtime)
	if !ok || runtime.Bucket == "" || runtime.Region == "" || runtime.KeyPrefix == "" {
		return fail(statebackend.ErrNotConfigured)
	}
	if project == "" || name == "" || (stage != "infra" && stage != "platform") {
		return fail(errors.New("invalid Terraform state lock scope"))
	}
	lockKey := strings.Trim(runtime.KeyPrefix, "/") + "/projects/" + project + "/" + name + "/" + stage + "/terraform.tfstate.tflock"
	var payload bytes.Buffer
	err := d.executor.Run(ctx, Command{
		Name: d.config.Tools.AWS,
		Args: []string{"s3", "cp", "s3://" + runtime.Bucket + "/" + lockKey, "-", "--region", runtime.Region, "--only-show-errors", "--no-progress"},
		Dir:  d.config.Paths.RepositoryRoot,
		Env:  d.stateBackendCommandEnv(ctx, runtime),
	}, &payload)
	if err != nil {
		if terraformStateLockNotFound(err, payload.String()) {
			_, _ = fmt.Fprintln(output, "统一状态中心未发现活动锁，可以安全继续组件对账与部署。")
			jobs.StepFinished(ctx, stepCheckPlatformStateLock, nil)
			return nil
		}
		return fail(fmt.Errorf("读取统一 Terraform 状态锁失败，已在修改集群前停止: %w", err))
	}
	var lock terraformStateLock
	if err := json.Unmarshal(payload.Bytes(), &lock); err != nil {
		return fail(fmt.Errorf("统一 Terraform 状态锁内容无法识别，已在修改集群前停止: %w", err))
	}
	lock.ID = strings.TrimSpace(lock.ID)
	if lock.ID == "" {
		return fail(errors.New("统一 Terraform 状态锁缺少 Lock ID，已在修改集群前停止"))
	}
	owner := strings.TrimSpace(lock.Who)
	if owner == "" {
		owner = "未知执行端"
	}
	created := strings.TrimSpace(lock.Created)
	if created == "" {
		created = "未知时间"
	}
	autoUnlocked, err := d.autoUnlockStalePlatformStateLock(ctx, runtime, lockKey, lock, output)
	if err != nil {
		return fail(err)
	}
	if autoUnlocked {
		jobs.StepFinished(ctx, stepCheckPlatformStateLock, nil)
		return nil
	}
	return fail(fmt.Errorf("Terraform State 被锁定，已在修改任何 Helm 组件前安全停止；Lock ID=%s，持有者=%s，创建时间=%s。请确认持有任务已结束后使用受控解锁或重试（Error acquiring the state lock）", lock.ID, owner, created))
}

// autoUnlockStalePlatformStateLock only removes locks that can be attributed to
// an old ops-deploy-platform Pod. Terraform locks from operators or any other
// automation remain fail-closed. The object is read again immediately before
// deletion so a newly acquired lock can never be deleted using stale data.
func (d *Deployment) autoUnlockStalePlatformStateLock(ctx context.Context, runtime statebackend.Runtime, lockKey string, observed terraformStateLock, output io.Writer) (bool, error) {
	lockHost, createdAt, eligible := stalePlatformTerraformLock(observed, time.Now())
	if !eligible {
		return false, nil
	}
	currentHost, _ := os.Hostname()
	if strings.EqualFold(strings.TrimSpace(currentHost), lockHost) {
		return false, nil
	}

	var latestPayload bytes.Buffer
	readErr := d.executor.Run(ctx, Command{
		Name: d.config.Tools.AWS,
		Args: []string{"s3", "cp", "s3://" + runtime.Bucket + "/" + lockKey, "-", "--region", runtime.Region, "--only-show-errors", "--no-progress"},
		Dir:  d.config.Paths.RepositoryRoot,
		Env:  d.stateBackendCommandEnv(ctx, runtime),
	}, &latestPayload)
	if readErr != nil {
		if terraformStateLockNotFound(readErr, latestPayload.String()) {
			_, _ = fmt.Fprintln(output, "[自动恢复] 状态锁已由其他执行端释放，可以安全继续。")
			return true, nil
		}
		return false, fmt.Errorf("二次核对 Terraform 状态锁失败，已安全停止: %w", readErr)
	}
	var latest terraformStateLock
	if err := json.Unmarshal(latestPayload.Bytes(), &latest); err != nil {
		return false, fmt.Errorf("二次核对 Terraform 状态锁内容失败，已安全停止: %w", err)
	}
	latest.ID = strings.TrimSpace(latest.ID)
	latestHost, _, stillEligible := stalePlatformTerraformLock(latest, time.Now())
	if latest.ID == "" || latest.ID != strings.TrimSpace(observed.ID) || !stillEligible || !strings.EqualFold(latestHost, lockHost) {
		return false, errors.New("Terraform 状态锁在二次核对期间已变化，已拒绝自动解锁并安全停止")
	}

	var deleteOutput bytes.Buffer
	if err := d.executor.Run(ctx, Command{
		Name: d.config.Tools.AWS,
		Args: []string{"s3api", "delete-object", "--bucket", runtime.Bucket, "--key", lockKey, "--region", runtime.Region, "--no-cli-pager"},
		Dir:  d.config.Paths.RepositoryRoot,
		Env:  d.stateBackendCommandEnv(ctx, runtime),
	}, &deleteOutput); err != nil {
		return false, fmt.Errorf("清理旧平台 Pod 遗留的 Terraform 状态锁失败，已安全停止: %w", err)
	}
	_, _ = fmt.Fprintf(output, "[自动恢复] 已清理旧平台 Pod %s 遗留的 Terraform 状态锁（Lock ID=%s，创建于 %s），可以安全继续。\n", lockHost, latest.ID, createdAt.Format(time.RFC3339))
	return true, nil
}

func stalePlatformTerraformLock(lock terraformStateLock, now time.Time) (string, time.Time, bool) {
	owner := strings.TrimSpace(lock.Who)
	at := strings.LastIndex(owner, "@")
	if at < 0 || at == len(owner)-1 {
		return "", time.Time{}, false
	}
	host := strings.TrimSpace(strings.SplitN(owner[at+1:], ".", 2)[0])
	if !strings.HasPrefix(strings.ToLower(host), "ops-deploy-platform-") {
		return "", time.Time{}, false
	}
	createdAt, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(lock.Created))
	if err != nil || createdAt.After(now) || now.Sub(createdAt) < stalePlatformTerraformLockAge {
		return "", time.Time{}, false
	}
	return host, createdAt, true
}

func terraformStateLockNotFound(err error, output string) bool {
	value := strings.ToLower(strings.TrimSpace(output + "\n" + err.Error()))
	return strings.Contains(value, "nosuchkey") ||
		strings.Contains(value, "status code: 404") ||
		strings.Contains(value, "(404)") ||
		strings.Contains(value, "not found")
}

func (d *Deployment) prepareRemoteStateBackend(ctx context.Context, dir, name string, output io.Writer) ([]string, error) {
	project, _ := ctx.Value(stateProjectContextKey{}).(string)
	project = strings.TrimSpace(project)
	if project == "" {
		return nil, errors.New("project scope is missing for Terraform state")
	}
	runtime, ok := ctx.Value(stateBackendContextKey{}).(statebackend.Runtime)
	if !ok || runtime.Bucket == "" || runtime.Region == "" || runtime.KeyPrefix == "" {
		return nil, statebackend.ErrNotConfigured
	}
	if err := d.stageAllLegacyLocalStates(dir, output); err != nil {
		return nil, err
	}
	stage := "platform"
	if filepath.Clean(dir) == filepath.Clean(d.config.Paths.TerraformInfraDir) {
		stage = "infra"
	}
	if err := d.stageLegacyRemoteState(ctx, runtime, project, name, stage, output); err != nil {
		return nil, err
	}
	keyPrefix := strings.Trim(runtime.KeyPrefix, "/") + "/projects/" + project
	objectKey := keyPrefix + "/" + name + "/" + stage + "/terraform.tfstate"
	configPath, err := d.writeStateBackendConfig(ctx, runtime, keyPrefix, stage)
	if err != nil {
		return nil, err
	}
	_, _ = fmt.Fprintf(output, "\n==> 统一 Terraform 状态中心：s3://%s/%s（版本化 + 原生锁）\n", runtime.Bucket, objectKey)
	return []string{"-reconfigure", "-backend-config=" + configPath}, nil
}

func (d *Deployment) writeStateBackendConfig(ctx context.Context, runtime statebackend.Runtime, keyPrefix, stage string) (string, error) {
	directory, _ := ctx.Value(stateRuntimeDirectoryContextKey{}).(string)
	if directory == "" {
		return "", errors.New("Terraform state credential runtime directory is missing")
	}
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return "", fmt.Errorf("create Terraform state credential runtime directory: %w", err)
	}
	lines := []string{
		"bucket = " + strconv.Quote(runtime.Bucket),
		"region = " + strconv.Quote(runtime.Region),
		"key = " + strconv.Quote(stage+"/terraform.tfstate"),
		"workspace_key_prefix = " + strconv.Quote(keyPrefix),
		"encrypt = true",
		"use_lockfile = true",
		"access_key = " + strconv.Quote(runtime.AccessKeyID),
		"secret_key = " + strconv.Quote(runtime.SecretAccessKey),
	}
	if runtime.SessionToken != "" {
		lines = append(lines, "token = "+strconv.Quote(runtime.SessionToken))
	}
	if runtime.KMSKeyID != "" {
		lines = append(lines, "kms_key_id = "+strconv.Quote(runtime.KMSKeyID))
	}
	configPath := filepath.Join(directory, stage+"-backend.hcl")
	if err := os.WriteFile(configPath, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		return "", fmt.Errorf("write Terraform state backend credentials: %w", err)
	}
	return configPath, nil
}

func (d *Deployment) cleanupStateBackendRuntime(name, runtimeDirectory string) {
	_ = os.RemoveAll(runtimeDirectory)
	for _, stage := range []string{"infra", "platform"} {
		dataDir := filepath.Join(d.config.Paths.DataDir, "terraform", name, stage)
		_ = os.Remove(filepath.Join(dataDir, "terraform.tfstate"))
		_ = os.Remove(filepath.Join(dataDir, "terraform.tfstate.backup"))
	}
}

func terraformStateBucketName(prefix, accountID, project string) string {
	prefix = strings.Trim(strings.ToLower(strings.TrimSpace(prefix)), "-")
	project = strings.Trim(strings.ToLower(strings.TrimSpace(project)), "-")
	digest := sha256.Sum256([]byte(project))
	if len(project) > 24 {
		project = project[:24]
	}
	return fmt.Sprintf("%s-%s-%s-%s", prefix, accountID, project, hex.EncodeToString(digest[:4]))
}

func (d *Deployment) ensureStateBucket(ctx context.Context, bucket string) error {
	region := d.config.TerraformState.Region
	base := Command{Name: d.config.Tools.AWS, Dir: d.config.Paths.RepositoryRoot, Env: d.commandEnv(ctx, "")}
	head := base
	head.Args = []string{"s3api", "head-bucket", "--bucket", bucket, "--region", region}
	if err := d.executor.Run(ctx, head, io.Discard); err != nil {
		if !d.config.TerraformState.AutoCreate {
			return fmt.Errorf("state bucket %s is unavailable and auto_create is disabled: %w", bucket, err)
		}
		create := base
		create.Args = []string{"s3api", "create-bucket", "--bucket", bucket, "--region", region}
		if region != "us-east-1" {
			create.Args = append(create.Args, "--create-bucket-configuration", "LocationConstraint="+region)
		}
		if err := d.executor.Run(ctx, create, io.Discard); err != nil {
			return fmt.Errorf("create dedicated Terraform state bucket %s: %w", bucket, err)
		}
	}
	settings := []struct {
		name string
		args []string
	}{
		{name: "block public access", args: []string{"s3api", "put-public-access-block", "--bucket", bucket, "--region", region, "--public-access-block-configuration", `{"BlockPublicAcls":true,"IgnorePublicAcls":true,"BlockPublicPolicy":true,"RestrictPublicBuckets":true}`}},
		{name: "enable versioning", args: []string{"s3api", "put-bucket-versioning", "--bucket", bucket, "--region", region, "--versioning-configuration", `{"Status":"Enabled"}`}},
		{name: "enable encryption", args: []string{"s3api", "put-bucket-encryption", "--bucket", bucket, "--region", region, "--server-side-encryption-configuration", `{"Rules":[{"ApplyServerSideEncryptionByDefault":{"SSEAlgorithm":"AES256"}}]}`}},
		{name: "enforce bucket ownership", args: []string{"s3api", "put-bucket-ownership-controls", "--bucket", bucket, "--region", region, "--ownership-controls", `{"Rules":[{"ObjectOwnership":"BucketOwnerEnforced"}]}`}},
		{name: "tag state bucket", args: []string{"s3api", "put-bucket-tagging", "--bucket", bucket, "--region", region, "--tagging", `{"TagSet":[{"Key":"ManagedBy","Value":"ops-deploy-platform"},{"Key":"Purpose","Value":"terraform-state"}]}`}},
	}
	policy, err := json.Marshal(map[string]any{
		"Version": "2012-10-17",
		"Statement": []map[string]any{{
			"Sid": "DenyInsecureTransport", "Effect": "Deny", "Principal": "*", "Action": "s3:*",
			"Resource":  []string{"arn:aws:s3:::" + bucket, "arn:aws:s3:::" + bucket + "/*"},
			"Condition": map[string]any{"Bool": map[string]string{"aws:SecureTransport": "false"}},
		}},
	})
	if err != nil {
		return err
	}
	settings = append(settings, struct {
		name string
		args []string
	}{name: "require TLS", args: []string{"s3api", "put-bucket-policy", "--bucket", bucket, "--region", region, "--policy", string(policy)}})
	for _, setting := range settings {
		command := base
		command.Args = setting.args
		if err := d.executor.Run(ctx, command, io.Discard); err != nil {
			return fmt.Errorf("%s for Terraform state bucket %s: %w", setting.name, bucket, err)
		}
	}
	return nil
}

func (d *Deployment) stageAllLegacyLocalStates(dir string, output io.Writer) error {
	stage := filepath.Base(dir)
	workspaceRoot := filepath.Join(dir, "terraform.tfstate.d")
	entries, err := os.ReadDir(workspaceRoot)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("list legacy Terraform workspaces: %w", err)
	}
	for _, entry := range entries {
		if !entry.IsDir() || !environment.ValidName(entry.Name()) {
			continue
		}
		source := filepath.Join(workspaceRoot, entry.Name(), "terraform.tfstate")
		if _, err := os.Stat(source); errors.Is(err, os.ErrNotExist) {
			continue
		} else if err != nil {
			return fmt.Errorf("inspect legacy state for %s: %w", entry.Name(), err)
		}
		doc, err := d.environments.Load(entry.Name())
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				// A project/environment may have been deleted after an older
				// local deployment. Move it out of Terraform's active workspace
				// tree so init cannot offer to copy it into an unrelated backend.
				// The quarantine path deliberately has no project assignment.
				destination := d.orphanedLocalStatePath(entry.Name(), stage)
				if err := moveSensitiveStateFile(source, destination); err != nil {
					return fmt.Errorf("quarantine unowned legacy state %s/%s: %w", stage, entry.Name(), err)
				}
				if err := moveSensitiveStateFile(source+".backup", destination+".backup"); err != nil && !errors.Is(err, os.ErrNotExist) {
					return fmt.Errorf("quarantine unowned legacy state backup %s/%s: %w", stage, entry.Name(), err)
				}
				_, _ = fmt.Fprintf(output, "\n==> 已隔离无法确认归属的历史 State：%s/%s（移出活动工作区，不会迁移、覆盖或删除）\n", stage, entry.Name())
				continue
			}
			return fmt.Errorf("cannot assign legacy state %s to a project: %w", entry.Name(), err)
		}
		project := documentString(doc, "project")
		if project == "" {
			return fmt.Errorf("legacy state %s has no project ownership", entry.Name())
		}
		destination := d.stagedLocalStatePath(project, entry.Name(), stage)
		if err := moveSensitiveStateFile(source, destination); err != nil {
			return fmt.Errorf("stage legacy state %s/%s: %w", stage, entry.Name(), err)
		}
		if err := moveSensitiveStateFile(source+".backup", destination+".backup"); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("stage legacy state backup %s/%s: %w", stage, entry.Name(), err)
		}
		_, _ = fmt.Fprintf(output, "\n==> 已隔离本地旧 state：%s/%s，等待迁移到所属项目 S3 bucket\n", stage, entry.Name())
	}
	return nil
}

func (d *Deployment) stagedLocalStatePath(project, name, stage string) string {
	return filepath.Join(d.config.Paths.DataDir, "state-migration-staging", project, name, stage, "terraform.tfstate")
}

func (d *Deployment) orphanedLocalStatePath(name, stage string) string {
	return filepath.Join(d.config.Paths.DataDir, "state-orphans", stage, name, "terraform.tfstate")
}

func (d *Deployment) stageLegacyRemoteState(ctx context.Context, runtime statebackend.Runtime, project, name, stage string, output io.Writer) error {
	metadataPath := filepath.Join(d.config.Paths.DataDir, "state-metadata", project, name, stage+".json")
	if metadataPayload, readErr := os.ReadFile(metadataPath); readErr == nil {
		var metadata terraformStateMetadata
		if json.Unmarshal(metadataPayload, &metadata) == nil && metadata.Backend == "s3" && metadata.Bucket == runtime.Bucket && metadata.Region == runtime.Region && strings.HasPrefix(metadata.ObjectKey, strings.Trim(runtime.KeyPrefix, "/")+"/projects/"+project+"/") {
			return nil
		}
	} else if !errors.Is(readErr, os.ErrNotExist) {
		return fmt.Errorf("read Terraform state metadata before central migration: %w", readErr)
	}
	accountPayload, err := d.captureCommand(ctx, Command{
		Name: d.config.Tools.AWS, Args: []string{"sts", "get-caller-identity", "--query", "Account", "--output", "text"},
		Dir: d.config.Paths.RepositoryRoot, Env: d.commandEnv(ctx, ""),
	})
	if err != nil {
		return fmt.Errorf("resolve project AWS account for legacy state migration: %w", err)
	}
	accountID := strings.TrimSpace(string(accountPayload))
	if len(accountID) != 12 || strings.Trim(accountID, "0123456789") != "" {
		return errors.New("AWS returned an invalid project account ID during legacy state migration")
	}
	legacyBucket := terraformStateBucketName(d.config.TerraformState.BucketPrefix, accountID, project)
	legacyKey := strings.Trim(d.config.TerraformState.KeyPrefix, "/") + "/" + stage + "/" + name + "/terraform.tfstate"
	if legacyBucket == runtime.Bucket && strings.HasPrefix(legacyKey, strings.Trim(runtime.KeyPrefix, "/")+"/") {
		return nil
	}
	payload, err := d.captureCommand(ctx, Command{
		Name: d.config.Tools.AWS,
		Args: []string{"s3", "cp", "s3://" + legacyBucket + "/" + legacyKey, "-", "--no-progress", "--region", d.config.TerraformState.Region},
		Dir:  d.config.Paths.RepositoryRoot, Env: d.commandEnv(ctx, ""),
	})
	if err != nil {
		message := strings.ToLower(err.Error())
		if strings.Contains(message, "nosuchbucket") || strings.Contains(message, "nosuchkey") || strings.Contains(message, "not found") || strings.Contains(message, "404") {
			return nil
		}
		return fmt.Errorf("read legacy project S3 state before central migration: %w", err)
	}
	legacy, err := decodeTerraformState(payload)
	if err != nil || legacy.Lineage == "" {
		return errors.New("legacy project S3 state is invalid; central migration was blocked")
	}
	stagedPath := d.stagedLocalStatePath(project, name, stage)
	if localPayload, readErr := os.ReadFile(stagedPath); readErr == nil {
		local, decodeErr := decodeTerraformState(localPayload)
		if decodeErr != nil {
			return fmt.Errorf("decode staged local state before central migration: %w", decodeErr)
		}
		if local.Lineage != "" && local.Lineage != legacy.Lineage {
			return errors.New("legacy local and project S3 Terraform state lineages differ; central migration was blocked")
		}
		if local.Serial > legacy.Serial {
			return fmt.Errorf("legacy local Terraform state serial %d is newer than project S3 serial %d; central migration was blocked", local.Serial, legacy.Serial)
		}
		if err := d.archiveSupersededStagedState(ctx, name, stage, stagedPath, localPayload); err != nil {
			return err
		}
	} else if !errors.Is(readErr, os.ErrNotExist) {
		return fmt.Errorf("read staged local state before central migration: %w", readErr)
	}
	if err := os.MkdirAll(filepath.Dir(stagedPath), 0o700); err != nil {
		return err
	}
	if err := os.WriteFile(stagedPath, payload, 0o600); err != nil {
		return fmt.Errorf("stage authoritative project S3 state for central migration: %w", err)
	}
	_, _ = fmt.Fprintf(output, "\n==> 已读取原项目账号 state 并完成 lineage/serial 对账：s3://%s/%s\n", legacyBucket, legacyKey)
	return nil
}

func (d *Deployment) archiveSupersededStagedState(ctx context.Context, name, stage, stagedPath string, payload []byte) error {
	project, _ := ctx.Value(stateProjectContextKey{}).(string)
	directory := filepath.Join(d.config.Paths.DataDir, "state-archives", project, name, stage)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return err
	}
	stamp := time.Now().UTC().Format("20060102T150405.000000000Z")
	if err := os.WriteFile(filepath.Join(directory, stamp+"-superseded-before-central-migration.tfstate"), payload, 0o600); err != nil {
		return err
	}
	if err := os.Remove(stagedPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

func moveSensitiveStateFile(source, destination string) error {
	payload, err := os.ReadFile(source) // #nosec G304 -- both paths are platform-owned Terraform state locations.
	if err != nil {
		return err
	}
	if existing, readErr := os.ReadFile(destination); readErr == nil {
		if sha256.Sum256(existing) != sha256.Sum256(payload) {
			return errors.New("staging already contains a different state file; automatic overwrite was blocked")
		}
		return os.Remove(source)
	} else if !errors.Is(readErr, os.ErrNotExist) {
		return readErr
	}
	if err := os.MkdirAll(filepath.Dir(destination), 0o700); err != nil {
		return err
	}
	if err := os.Rename(source, destination); err == nil {
		return nil
	}
	if err := os.WriteFile(destination, payload, 0o600); err != nil {
		return err
	}
	if err := os.Remove(source); err != nil {
		_ = os.Remove(destination)
		return err
	}
	return nil
}

func (d *Deployment) migrateLocalStateToS3(ctx context.Context, dir, name string, output io.Writer) error {
	if !d.config.TerraformState.Enabled {
		return nil
	}
	project, _ := ctx.Value(stateProjectContextKey{}).(string)
	localPath := d.stagedLocalStatePath(project, name, filepath.Base(dir))
	localPayload, err := os.ReadFile(localPath) // #nosec G304 -- path is derived from validated environment and configured Terraform roots.
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read legacy local state: %w", err)
	}
	local, err := decodeTerraformState(localPayload)
	if err != nil {
		return fmt.Errorf("decode legacy local state: %w", err)
	}
	remotePayload, err := d.captureCommand(ctx, Command{
		Name: d.config.Tools.Terraform, Args: []string{"state", "pull"}, Dir: dir,
		Env: d.terraformEnv(ctx, name, dir),
	})
	if err != nil {
		return fmt.Errorf("read S3 remote state before migration: %w", err)
	}
	remote, err := decodeTerraformState(remotePayload)
	if err != nil {
		return fmt.Errorf("decode S3 remote state before migration: %w", err)
	}
	switch decideTerraformStateMigration(local, remote) {
	case terraformStateArchiveLocal:
		return d.archiveMigratedLocalState(ctx, dir, name, localPath, localPayload, output)
	case terraformStatePushLocal:
		_, _ = fmt.Fprintf(output, "\n==> 首次迁移 %s/%s 本地 Terraform state 到 S3；迁移前副本将保留在加密平台卷\n", name, filepath.Base(dir))
		if err := d.executor.Run(ctx, Command{
			Name: d.config.Tools.Terraform, Args: []string{"state", "push", "-force", localPath}, Dir: dir,
			Env: d.terraformEnv(ctx, name, dir),
		}, io.Discard); err != nil {
			return fmt.Errorf("push legacy local state to S3: %w", err)
		}
		verifiedPayload, err := d.captureCommand(ctx, Command{
			Name: d.config.Tools.Terraform, Args: []string{"state", "pull"}, Dir: dir,
			Env: d.terraformEnv(ctx, name, dir),
		})
		if err != nil {
			return fmt.Errorf("verify migrated S3 state: %w", err)
		}
		verified, err := decodeTerraformState(verifiedPayload)
		if err != nil || verified.Lineage != local.Lineage || len(verified.Resources) != len(local.Resources) {
			return errors.New("S3 state verification did not match the legacy local state; local state was retained")
		}
		return d.archiveMigratedLocalState(ctx, dir, name, localPath, localPayload, output)
	case terraformStateLineageConflict:
		return errors.New("both local and S3 Terraform states contain resources but their lineages differ; automatic overwrite was blocked")
	case terraformStateLocalNewer:
		return fmt.Errorf("local Terraform state serial %d is newer than S3 serial %d; automatic overwrite was blocked", local.Serial, remote.Serial)
	default:
		return errors.New("unsupported Terraform state migration decision")
	}
}

type terraformStateMigrationDecision uint8

const (
	terraformStateArchiveLocal terraformStateMigrationDecision = iota
	terraformStatePushLocal
	terraformStateLineageConflict
	terraformStateLocalNewer
)

// decideTerraformStateMigration protects the S3 state from a stale local copy.
// A newly-created remote workspace has serial zero and may safely receive the
// legacy state. An empty remote state with a positive serial is different: it
// normally represents a successful destroy and must remain authoritative.
func decideTerraformStateMigration(local, remote terraformStateSnapshot) terraformStateMigrationDecision {
	localManaged := managedStateResourceCount(local)
	remoteManaged := managedStateResourceCount(remote)
	// Provider data-source cache is reproducible and does not represent owned
	// infrastructure. Never let a data-only legacy state conflict with a clean
	// central workspace or replace its lineage.
	if localManaged == 0 {
		return terraformStateArchiveLocal
	}
	// Terraform creates a brand-new remote workspace with serial 1 and no
	// resources. It is safe to seed only that pristine state. A later empty
	// state with a higher serial normally represents a completed destroy and
	// must remain authoritative so stale local resources cannot be resurrected.
	if remoteManaged == 0 && remote.Serial <= 1 && len(remote.Resources) == 0 {
		if local.Serial > 0 || localManaged > 0 {
			return terraformStatePushLocal
		}
		return terraformStateArchiveLocal
	}
	if local.Lineage == "" || remote.Lineage == "" || remote.Lineage != local.Lineage {
		return terraformStateLineageConflict
	}
	if remote.Serial < local.Serial {
		return terraformStateLocalNewer
	}
	return terraformStateArchiveLocal
}

func decodeTerraformState(payload []byte) (terraformStateSnapshot, error) {
	var state terraformStateSnapshot
	if err := json.Unmarshal(payload, &state); err != nil {
		return terraformStateSnapshot{}, err
	}
	return state, nil
}

func managedStateResourceCount(state terraformStateSnapshot) int {
	count := 0
	for _, resource := range state.Resources {
		if resource.Mode == "managed" {
			count += len(resource.Instances)
		}
	}
	return count
}

type terraformStateMetadata struct {
	Project          string    `json:"project"`
	Environment      string    `json:"environment"`
	Stage            string    `json:"stage"`
	Backend          string    `json:"backend"`
	Bucket           string    `json:"bucket"`
	Region           string    `json:"region"`
	ObjectKey        string    `json:"object_key"`
	LockKey          string    `json:"lock_key"`
	Lineage          string    `json:"lineage"`
	Serial           int64     `json:"serial"`
	ManagedResources int       `json:"managed_resources"`
	UpdatedAt        time.Time `json:"updated_at"`
}

func (d *Deployment) reconcileTerraformState(ctx context.Context, dir, name string, output io.Writer) error {
	if err := d.migrateLocalStateToS3(ctx, dir, name, output); err != nil {
		return err
	}
	return d.persistRemoteStateMetadata(ctx, dir, name)
}

func (d *Deployment) persistRemoteStateMetadata(ctx context.Context, dir, name string) error {
	if !d.config.TerraformState.Enabled {
		return nil
	}
	payload, err := d.captureCommand(ctx, Command{
		Name: d.config.Tools.Terraform, Args: []string{"state", "pull"}, Dir: dir,
		Env: d.terraformEnv(ctx, name, dir),
	})
	if err != nil {
		return fmt.Errorf("refresh Terraform state metadata: %w", err)
	}
	state, err := decodeTerraformState(payload)
	if err != nil {
		return fmt.Errorf("decode Terraform state metadata: %w", err)
	}
	project, _ := ctx.Value(stateProjectContextKey{}).(string)
	runtime, ok := ctx.Value(stateBackendContextKey{}).(statebackend.Runtime)
	if !ok {
		return statebackend.ErrNotConfigured
	}
	stage := filepath.Base(dir)
	objectKey := strings.Trim(runtime.KeyPrefix, "/") + "/projects/" + project + "/" + name + "/" + stage + "/terraform.tfstate"
	metadata := terraformStateMetadata{
		Project: project, Environment: name, Stage: stage, Backend: "s3",
		Bucket: runtime.Bucket, Region: runtime.Region, ObjectKey: objectKey, LockKey: objectKey + ".tflock",
		Lineage: state.Lineage, Serial: state.Serial, ManagedResources: managedStateResourceCount(state), UpdatedAt: time.Now().UTC(),
	}
	encoded, err := json.MarshalIndent(metadata, "", "  ")
	if err != nil {
		return err
	}
	directory := filepath.Join(d.config.Paths.DataDir, "state-metadata", project, name)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return fmt.Errorf("create Terraform state metadata directory: %w", err)
	}
	path := filepath.Join(directory, stage+".json")
	temporary := path + ".tmp"
	if err := os.WriteFile(temporary, encoded, 0o600); err != nil {
		return fmt.Errorf("write Terraform state metadata: %w", err)
	}
	if err := os.Rename(temporary, path); err != nil {
		return fmt.Errorf("publish Terraform state metadata: %w", err)
	}
	return nil
}

func (d *Deployment) archiveMigratedLocalState(ctx context.Context, dir, name, localPath string, payload []byte, output io.Writer) error {
	project, _ := ctx.Value(stateProjectContextKey{}).(string)
	stage := filepath.Base(dir)
	archiveDir := filepath.Join(d.config.Paths.DataDir, "state-archives", project, name, stage)
	if err := os.MkdirAll(archiveDir, 0o700); err != nil {
		return fmt.Errorf("create local state archive: %w", err)
	}
	stamp := time.Now().UTC().Format("20060102T150405.000000000Z")
	archivePath := filepath.Join(archiveDir, stamp+"-pre-s3-migration.tfstate")
	if err := os.WriteFile(archivePath, payload, 0o600); err != nil {
		return fmt.Errorf("archive migrated local state: %w", err)
	}
	if backup, err := os.ReadFile(localPath + ".backup"); err == nil {
		if err := os.WriteFile(archivePath+".backup", backup, 0o600); err != nil {
			return fmt.Errorf("archive migrated local state backup: %w", err)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("read local state backup: %w", err)
	}
	if err := os.Remove(localPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("retire migrated local state: %w", err)
	}
	if err := os.Remove(localPath + ".backup"); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("retire migrated local state backup: %w", err)
	}
	_, _ = fmt.Fprintf(output, "==> S3 state 校验完成，本地旧 state 已归档到 %s\n", archivePath)
	return nil
}

func (d *Deployment) captureCommand(ctx context.Context, command Command) ([]byte, error) {
	var output bytes.Buffer
	if err := d.executor.Run(ctx, command, &output); err != nil {
		return nil, err
	}
	return output.Bytes(), nil
}

func (d *Deployment) terraformWorkspace(ctx context.Context, dir, name string, output io.Writer) error {
	env := d.terraformDataEnv(ctx, name, dir)
	label := stepPreparePlatform
	if filepath.Base(dir) == filepath.Base(d.config.Paths.TerraformInfraDir) {
		label = stepPrepareInfra
	}
	jobs.StepStarted(ctx, label)
	selectCommand := Command{
		Name: d.config.Tools.Terraform,
		Args: []string{"workspace", "select", name},
		Dir:  dir,
		Env:  env,
	}
	if err := d.executor.Run(ctx, selectCommand, io.Discard); err == nil {
		_, _ = fmt.Fprintf(output, "\n==> Selected Terraform workspace %s for %s\n", name, filepath.Base(dir))
		err := d.reconcileTerraformState(ctx, dir, name, output)
		jobs.StepFinished(ctx, label, err)
		if err != nil {
			return fmt.Errorf("%s: %w", label, err)
		}
		return nil
	}
	_, _ = fmt.Fprintf(output, "\n==> %s\n", label)
	command := Command{
		Name: d.config.Tools.Terraform,
		Args: []string{"workspace", "new", name},
		Dir:  dir,
		Env:  env,
	}
	_, _ = fmt.Fprintf(output, "$ %s %s\n", command.Name, strings.Join(command.Args, " "))
	err := d.executor.Run(ctx, command, output)
	if err != nil {
		jobs.StepFinished(ctx, label, err)
		return fmt.Errorf("%s: %w", label, err)
	}
	err = d.reconcileTerraformState(ctx, dir, name, output)
	jobs.StepFinished(ctx, label, err)
	if err != nil {
		return fmt.Errorf("%s: %w", label, err)
	}
	return nil
}

func (d *Deployment) terraformApply(ctx context.Context, dir, name string, destroy bool, phase string, output io.Writer) error {
	label := stepApplyInfra
	if destroy {
		label = stepDestroyPlatform
		if filepath.Base(dir) == filepath.Base(d.config.Paths.TerraformInfraDir) {
			label = stepDestroyInfra
		}
	} else if phase == "base" {
		label = stepApplyBase
	} else if phase == "components" {
		label = stepApplyComponents
	} else if phase == "access" {
		label = stepApplyAccess
	}
	return d.terraformApplyNamed(ctx, dir, name, destroy, phase, label, output)
}

func (d *Deployment) deploymentPlanPath(jobID, phase string) (string, error) {
	planDir := filepath.Join(d.config.Paths.DataDir, "plans")
	if err := os.MkdirAll(planDir, 0o700); err != nil {
		return "", err
	}
	return filepath.Join(planDir, jobID+"-"+phase+".tfplan"), nil
}

func (d *Deployment) terraformPlanToFile(ctx context.Context, dir, name, phase, planPath, label string, output io.Writer) error {
	args := []string{
		"plan", "-input=false", "-no-color", "-out=" + planPath,
		"-var=config_file=" + d.environments.Path(name),
	}
	if phase != "" {
		args = append(args, "-var=deployment_phase="+phase)
	}
	if filepath.Clean(dir) == filepath.Clean(d.config.Paths.TerraformInfraDir) {
		if cidrs, ok := ctx.Value(eksPublicAccessCIDRsContextKey{}).([]string); ok {
			encoded, err := json.Marshal(cidrs)
			if err != nil {
				return fmt.Errorf("encode merged EKS API public access CIDRs: %w", err)
			}
			args = append(args, "-var=eks_public_access_cidrs_override="+string(encoded))
		}
	}
	if phase == "base" {
		for _, target := range phaseOneBaseTargets() {
			args = append(args, "-target="+target)
		}
	}
	return d.step(ctx, output, label, Command{
		Name: d.config.Tools.Terraform,
		Args: args,
		Dir:  dir,
		Env:  d.terraformEnv(ctx, name, dir),
	})
}

// withMergedEKSPublicAccessCIDRs makes the platform's EKS API whitelist
// additive. The environment document remains the list that the platform must
// ensure, while the runtime Terraform plan receives its union with AWS. AWS
// console additions therefore survive every later platform deployment.
func (d *Deployment) withMergedEKSPublicAccessCIDRs(ctx context.Context, doc environment.Document, output io.Writer) (context.Context, error) {
	configured := documentStringList(doc, "eks.public_access_cidrs")
	region := documentString(doc, "region")
	cluster := environment.ClusterName(doc)
	var payload bytes.Buffer
	command := Command{
		Name: d.config.Tools.AWS,
		Args: []string{
			"eks", "describe-cluster", "--region", region, "--name", cluster,
			"--query", "cluster.resourcesVpcConfig.publicAccessCidrs", "--output", "json", "--no-cli-pager",
		},
		Dir: d.config.Paths.RepositoryRoot,
		Env: d.commandEnv(ctx, ""),
	}
	if err := d.executor.Run(ctx, command, &payload); err != nil {
		details := strings.ToLower(payload.String() + "\n" + err.Error())
		if strings.Contains(details, "resourcenotfoundexception") || strings.Contains(details, "no cluster found") {
			merged := normalizedUniqueStrings(configured)
			_, _ = fmt.Fprintf(output, "EKS 集群 %s 尚未创建；本次将使用平台配置的 %d 条 API 公网白名单初始化集群。\n", cluster, len(merged))
			return context.WithValue(ctx, eksPublicAccessCIDRsContextKey{}, merged), nil
		}
		return ctx, fmt.Errorf("读取 AWS EKS API 当前公网白名单失败，平台已安全停止，避免覆盖 AWS 控制台已有地址: %w", err)
	}
	var actual []string
	if err := json.Unmarshal(payload.Bytes(), &actual); err != nil {
		return ctx, fmt.Errorf("解析 AWS EKS API 当前公网白名单失败，平台已安全停止: %w", err)
	}
	actual = normalizedUniqueStrings(actual)
	configured = normalizedUniqueStrings(configured)
	actualSet := make(map[string]struct{}, len(actual))
	for _, cidr := range actual {
		actualSet[cidr] = struct{}{}
	}
	additions := 0
	for _, cidr := range configured {
		if _, exists := actualSet[cidr]; !exists {
			additions++
		}
	}
	merged := normalizedUniqueStrings(append(append([]string{}, actual...), configured...))
	_, _ = fmt.Fprintf(output, "EKS API 公网白名单采用增量模式：AWS 当前 %d 条，平台本次新增 %d 条，计划保留 %d 条；不会删除 AWS 已有地址。\n", len(actual), additions, len(merged))
	return context.WithValue(ctx, eksPublicAccessCIDRsContextKey{}, merged), nil
}

func documentStringList(doc environment.Document, path string) []string {
	value, _ := environment.GetPath(doc, path)
	switch items := value.(type) {
	case []string:
		return normalizedUniqueStrings(items)
	case []any:
		values := make([]string, 0, len(items))
		for _, item := range items {
			if text, ok := item.(string); ok {
				values = append(values, text)
			}
		}
		return normalizedUniqueStrings(values)
	default:
		return []string{}
	}
}

func normalizedUniqueStrings(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}

func (d *Deployment) terraformApplyPlan(ctx context.Context, dir, name, planPath, label string, output io.Writer) error {
	applyErr := d.step(ctx, output, label, Command{
		Name: d.config.Tools.Terraform,
		Args: []string{"apply", "-input=false", "-no-color", planPath},
		Dir:  dir,
		Env:  d.terraformEnv(ctx, name, dir),
	})
	metadataErr := d.persistRemoteStateMetadata(ctx, dir, name)
	if applyErr != nil {
		return applyErr
	}
	if metadataErr != nil {
		return fmt.Errorf("persist Terraform state metadata after applying saved plan: %w", metadataErr)
	}
	return nil
}

func phaseOneBaseTargets() []string {
	return []string{
		"kubernetes_namespace_v1.this",
		"aws_eks_addon.base",
		"aws_eks_addon.ebs_csi",
		"kubernetes_storage_class_v1.gp3",
		"helm_release.load_balancer_controller",
		"helm_release.metrics_server",
		"helm_release.cluster_autoscaler",
		"helm_release.consul",
		"kubernetes_service_v1.consul_http",
		"helm_release.etcd",
		"tls_private_key.etcd_ca",
		"tls_self_signed_cert.etcd_ca",
		"tls_private_key.etcd",
		"tls_cert_request.etcd",
		"tls_locally_signed_cert.etcd",
		"kubernetes_secret_v1.etcd_tls",
		"random_password.etcd_web",
		"kubernetes_cron_job_v1.consul_backup",
		"kubernetes_cron_job_v1.etcd_backup",
		"kubernetes_service_account_v1.platform_backup",
		"aws_eks_pod_identity_association.platform_backup",
		"aws_iam_role_policy_attachment.platform_backup",
		"aws_iam_policy.platform_backup",
		"aws_iam_role.platform_backup",
	}
}

// phaseTwoComponentTargets keeps an ordinary component/access update away
// from stateful phase-one services such as Consul and etcd. Terraform still
// refreshes and applies every phase-two object in one locked state operation,
// but a domain, alert or optional-component change can no longer trigger a
// Helm upgrade (and a potentially long rollout wait) for a base service.
func phaseTwoComponentTargets() []string {
	return []string{
		"helm_release.external_dns",
		"aws_iam_role.external_dns",
		"aws_iam_policy.external_dns",
		"aws_iam_role_policy_attachment.external_dns",
		"aws_eks_pod_identity_association.external_dns",
		"helm_release.cert_manager",
		"helm_release.catalog",
		"aws_security_group.higress_nlb",
		"aws_vpc_security_group_ingress_rule.higress_nlb_ipv4",
		"aws_vpc_security_group_ingress_rule.higress_nlb_ipv6",
		"aws_vpc_security_group_egress_rule.higress_nlb",
		"helm_release.otel_elasticsearch",
		"helm_release.loki_collector",
		"kubernetes_config_map_v1.grafana_loki_datasource",
		"kubernetes_config_map_v1.grafana_tempo_datasource",
		"kubernetes_secret_v1.grafana_elasticsearch_datasource",
		"kubernetes_secret_v1.grafana_otel_elasticsearch_datasource",
		"kubernetes_config_map_v1.grafana_eks_dashboard",
		"kubernetes_config_map_v1.grafana_logs_dashboard",
		"kubernetes_config_map_v1.grafana_traces_dashboard",
		"random_password.xxl_job_mysql",
		"random_password.xxl_job_admin",
		"random_password.nacos_auth_token",
		"random_password.nacos_identity_value",
		"random_password.higress_admin",
		"random_password.data_service",
		"random_password.bytebase_admin",
		"random_password.redisinsight_admin",
		"random_password.redisinsight_encryption",
		"random_password.etcd_workbench_admin",
		"random_password.etcd_workbench_encryption",
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
		"random_password.jaeger_ui",
		"random_password.otel_elasticsearch",
		"kubernetes_manifest.tls_certificate",
		"kubernetes_ingress_v1.domain",
		"kubernetes_service_v1.tcp_route",
		"kubernetes_config_map_v1.alerting",
		"kubernetes_secret_v1.alerting_channels",
	}
}

func phaseTwoAccessTargets() []string {
	return []string{
		"kubernetes_manifest.tls_certificate",
		"kubernetes_ingress_v1.domain",
		"kubernetes_service_v1.tcp_route",
		"kubernetes_config_map_v1.alerting",
		"kubernetes_secret_v1.alerting_channels",
	}
}

func (d *Deployment) terraformApplyNamed(ctx context.Context, dir, name string, destroy bool, phase, label string, output io.Writer) error {
	action := "apply"
	if destroy {
		action = "destroy"
	}
	args := []string{
		action, "-input=false", "-auto-approve", "-no-color",
		"-var=config_file=" + d.environments.Path(name),
	}
	if phase != "" {
		args = append(args, "-var=deployment_phase="+phase)
	}
	if phase == "base" && !destroy {
		// Stage 1 uses an explicit apply scope so rerunning it after stage 2
		// cannot remove optional components, certificates or ingress objects
		// that already exist in the same Terraform state.
		for _, target := range phaseOneBaseTargets() {
			args = append(args, "-target="+target)
		}
	} else if phase == "components" && !destroy {
		// Phase two is intentionally incremental and must not reconcile phase
		// one Helm releases. Namespace and storage dependencies required by an
		// enabled chart are still traversed by Terraform automatically.
		for _, target := range phaseTwoComponentTargets() {
			args = append(args, "-target="+target)
		}
		_, _ = fmt.Fprintln(output, "阶段2 增量模式：只对账可选组件、TLS、域名与告警资源；不会升级 EKS、Consul 或 etcd。")
	} else if phase == "access" && !destroy {
		for _, target := range phaseTwoAccessTargets() {
			args = append(args, "-target="+target)
		}
		_, _ = fmt.Fprintln(output, "阶段2 接入配置模式：只更新域名、TCP 转发、TLS 与告警；不会安装、升级或重建任何业务组件。")
	}
	applyErr := d.step(ctx, output, label, Command{
		Name: d.config.Tools.Terraform,
		Args: args,
		Dir:  dir,
		Env:  d.terraformEnv(ctx, name, dir),
	})
	metadataErr := d.persistRemoteStateMetadata(ctx, dir, name)
	if applyErr != nil {
		return applyErr
	}
	if metadataErr != nil {
		return fmt.Errorf("persist Terraform state metadata after %s: %w", action, metadataErr)
	}
	return nil
}

func (d *Deployment) cleanupOrphanedEKSNetworkResources(ctx context.Context, name string, doc environment.Document, output io.Writer) error {
	const label = stepCleanupOrphanedNetwork
	jobs.StepStarted(ctx, label)
	_, _ = fmt.Fprintf(output, "\n==> %s\n", label)

	vpcID, found, err := d.resolveManagedVPCIDForCleanup(ctx, name, doc, output)
	if err != nil {
		jobs.StepFinished(ctx, label, err)
		return err
	}
	if !found {
		_, _ = fmt.Fprintln(output, "未发现当前项目环境仍存在的受管 VPC，网络残留清理无需执行。")
		jobs.StepFinished(ctx, label, nil)
		return nil
	}

	region := documentString(doc, "region")
	deletedENIs := 0
	cleanupDeadline := time.Now().Add(10 * time.Minute)
	for {
		var payload struct {
			NetworkInterfaces []struct {
				ID     string `json:"NetworkInterfaceId"`
				Status string `json:"Status"`
			} `json:"NetworkInterfaces"`
		}
		if err := d.awsJSON(ctx, doc, &payload,
			"ec2", "describe-network-interfaces", "--filters",
			"Name=vpc-id,Values="+vpcID,
			"Name=description,Values=aws-K8S-*",
		); err != nil {
			wrapped := fmt.Errorf("describe orphaned EKS network interfaces: %w", err)
			jobs.StepFinished(ctx, label, wrapped)
			return wrapped
		}
		if len(payload.NetworkInterfaces) == 0 {
			break
		}

		waiting := make([]string, 0, len(payload.NetworkInterfaces))
		for _, networkInterface := range payload.NetworkInterfaces {
			eniID := strings.TrimSpace(networkInterface.ID)
			if !validAWSResourceID(eniID, "eni-") {
				err := fmt.Errorf("refuse orphan ENI cleanup for invalid ENI ID %q", eniID)
				jobs.StepFinished(ctx, label, err)
				return err
			}
			if !strings.EqualFold(strings.TrimSpace(networkInterface.Status), "available") {
				waiting = append(waiting, eniID+"("+networkInterface.Status+")")
				continue
			}
			_, _ = fmt.Fprintf(output, "Deleting available orphaned EKS network interface %s in %s\n", eniID, vpcID)
			command := Command{
				Name: d.config.Tools.AWS,
				Args: []string{"ec2", "delete-network-interface", "--region", region, "--network-interface-id", eniID},
				Dir:  d.config.Paths.RepositoryRoot,
				Env:  d.commandEnv(ctx, ""),
			}
			if err := d.executor.Run(ctx, command, output); err != nil {
				wrapped := fmt.Errorf("delete orphaned EKS network interface %s: %w", eniID, err)
				jobs.StepFinished(ctx, label, wrapped)
				return wrapped
			}
			deletedENIs++
		}

		if time.Now().After(cleanupDeadline) {
			err := fmt.Errorf("等待 EKS 遗留网卡释放超过 10 分钟，仍未释放：%s；平台已停止删除 VPC，请确认 EKS/EC2 正在终止的资源后重试", strings.Join(waiting, ", "))
			jobs.StepFinished(ctx, label, err)
			return err
		}
		if len(waiting) > 0 {
			_, _ = fmt.Fprintf(output, "等待 EKS VPC CNI 网卡释放后再删除 VPC：%s\n", strings.Join(waiting, ", "))
		}
		timer := time.NewTimer(10 * time.Second)
		select {
		case <-ctx.Done():
			timer.Stop()
			err := ctx.Err()
			jobs.StepFinished(ctx, label, err)
			return err
		case <-timer.C:
		}
	}

	var securityGroupOutput bytes.Buffer
	describeSecurityGroups := Command{
		Name: d.config.Tools.AWS,
		Args: []string{
			"ec2", "describe-security-groups", "--region", region,
			"--filters", "Name=vpc-id,Values=" + vpcID, "Name=group-name,Values=eks-cluster-sg-*",
			"--query", "SecurityGroups[].GroupId", "--output", "text",
		},
		Dir: d.config.Paths.RepositoryRoot,
		Env: d.commandEnv(ctx, ""),
	}
	if err := d.executor.Run(ctx, describeSecurityGroups, io.MultiWriter(output, &securityGroupOutput)); err != nil {
		wrapped := fmt.Errorf("describe orphaned EKS cluster security groups: %w", err)
		jobs.StepFinished(ctx, label, wrapped)
		return wrapped
	}
	deletedSecurityGroups := 0
	for _, groupID := range strings.Fields(securityGroupOutput.String()) {
		if !validAWSResourceID(groupID, "sg-") {
			err := fmt.Errorf("refuse orphan security group cleanup for invalid group ID %q", groupID)
			jobs.StepFinished(ctx, label, err)
			return err
		}
		var references bytes.Buffer
		describeReferences := Command{
			Name: d.config.Tools.AWS,
			Args: []string{
				"ec2", "describe-network-interfaces", "--region", region,
				"--filters", "Name=group-id,Values=" + groupID,
				"--query", "NetworkInterfaces[].NetworkInterfaceId", "--output", "text",
			},
			Dir: d.config.Paths.RepositoryRoot,
			Env: d.commandEnv(ctx, ""),
		}
		if err := d.executor.Run(ctx, describeReferences, io.MultiWriter(output, &references)); err != nil {
			wrapped := fmt.Errorf("inspect EKS cluster security group %s references: %w", groupID, err)
			jobs.StepFinished(ctx, label, wrapped)
			return wrapped
		}
		if len(strings.Fields(references.String())) > 0 {
			_, _ = fmt.Fprintf(output, "Keeping EKS cluster security group %s because it is still referenced by a network interface.\n", groupID)
			continue
		}
		_, _ = fmt.Fprintf(output, "Deleting unreferenced orphaned EKS cluster security group %s in %s\n", groupID, vpcID)
		deleteGroup := Command{
			Name: d.config.Tools.AWS,
			Args: []string{"ec2", "delete-security-group", "--region", region, "--group-id", groupID},
			Dir:  d.config.Paths.RepositoryRoot,
			Env:  d.commandEnv(ctx, ""),
		}
		if err := d.executor.Run(ctx, deleteGroup, output); err != nil {
			wrapped := fmt.Errorf("delete orphaned EKS cluster security group %s: %w", groupID, err)
			jobs.StepFinished(ctx, label, wrapped)
			return wrapped
		}
		deletedSecurityGroups++
	}
	_, _ = fmt.Fprintf(output, "Orphaned EKS network cleanup complete: %d ENI and %d security group deleted.\n", deletedENIs, deletedSecurityGroups)
	jobs.StepFinished(ctx, label, nil)
	return nil
}

func (d *Deployment) resolveManagedVPCIDForCleanup(ctx context.Context, name string, doc environment.Document, output io.Writer) (string, bool, error) {
	var vpcOutput bytes.Buffer
	vpcCommand := Command{
		Name: d.config.Tools.Terraform,
		Args: []string{"output", "-raw", "vpc_id"},
		Dir:  d.config.Paths.TerraformInfraDir,
		Env:  d.terraformEnv(ctx, name, d.config.Paths.TerraformInfraDir),
	}
	terraformErr := d.executor.Run(ctx, vpcCommand, &vpcOutput)
	vpcID := strings.TrimSpace(vpcOutput.String())
	if terraformErr == nil && validAWSResourceID(vpcID, "vpc-") {
		return vpcID, true, nil
	}

	// Terraform clears output values as soon as a partial destroy begins, even
	// when the VPC itself remains in state/AWS. `terraform output -raw` exits 0
	// and writes a colored "No outputs found" warning in that situation. Never
	// treat diagnostic text as an AWS ID; recover the VPC through the ownership
	// tags that the platform applies to every managed VPC.
	_, _ = fmt.Fprintln(output, "Terraform 输出中已没有可用的 vpc_id，正在按项目/环境归属标签查询剩余 VPC。")
	var payload struct {
		VPCs []struct {
			ID string `json:"VpcId"`
		} `json:"Vpcs"`
	}
	project := documentString(doc, "project")
	environmentName := documentString(doc, "environment")
	if project == "" || environmentName == "" {
		return "", false, errors.New("无法从环境配置确认项目或环境，拒绝按标签清理 VPC")
	}
	if err := d.awsJSON(ctx, doc, &payload,
		"ec2", "describe-vpcs", "--filters",
		"Name=tag:Project,Values="+project,
		"Name=tag:Environment,Values="+environmentName,
		"Name=tag:ManagedBy,Values=Terraform",
	); err != nil {
		return "", false, fmt.Errorf("按项目/环境归属标签查询剩余 VPC: %w", err)
	}
	if len(payload.VPCs) == 0 {
		return "", false, nil
	}
	if len(payload.VPCs) != 1 {
		return "", false, fmt.Errorf("项目 %s/%s 匹配到 %d 个受管 VPC，平台拒绝模糊清理", project, environmentName, len(payload.VPCs))
	}
	vpcID = strings.TrimSpace(payload.VPCs[0].ID)
	if !validAWSResourceID(vpcID, "vpc-") {
		return "", false, fmt.Errorf("AWS 返回了不合法的受管 VPC ID %q，平台拒绝清理", vpcID)
	}
	_, _ = fmt.Fprintf(output, "已通过归属标签恢复受管 VPC：%s。\n", vpcID)
	return vpcID, true, nil
}

func validAWSResourceID(value, prefix string) bool {
	if !strings.HasPrefix(value, prefix) || len(value) <= len(prefix) {
		return false
	}
	for _, char := range value[len(prefix):] {
		if (char < '0' || char > '9') && (char < 'a' || char > 'f') {
			return false
		}
	}
	return true
}

func (d *Deployment) updateKubeconfig(ctx context.Context, name string, doc environment.Document, output io.Writer) (string, error) {
	region := documentString(doc, "region")
	cluster := environment.ClusterName(doc)
	dir := filepath.Join(d.config.Paths.DataDir, "kubeconfigs")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	path := filepath.Join(dir, name)
	err := d.step(ctx, output, stepUpdateKubeconfig, Command{
		Name: d.config.Tools.AWS,
		Args: []string{
			"eks", "update-kubeconfig", "--region", region,
			"--name", cluster, "--alias", cluster, "--kubeconfig", path,
		},
		Dir: d.config.Paths.RepositoryRoot,
		Env: d.commandEnv(ctx, path),
	})
	if err == nil {
		err = os.Chmod(path, 0o600)
	}
	return path, err
}

func (d *Deployment) checkExistingEKS(ctx context.Context, doc environment.Document, kubeconfig string, output io.Writer) error {
	jobs.StepStarted(ctx, stepCheckExistingEKS)
	_, _ = fmt.Fprintf(output, "\n==> %s\n", stepCheckExistingEKS)
	authCommand := Command{
		Name: d.config.Tools.Kubectl, Args: []string{"auth", "can-i", "*", "*", "--all-namespaces"},
		Dir: d.config.Paths.RepositoryRoot, Env: d.commandEnv(ctx, kubeconfig),
	}
	_, _ = fmt.Fprintf(output, "$ %s %s\n", authCommand.Name, strings.Join(authCommand.Args, " "))
	var authOutput bytes.Buffer
	if err := d.executor.Run(ctx, authCommand, io.MultiWriter(output, &authOutput)); err != nil {
		jobs.StepFinished(ctx, stepCheckExistingEKS, err)
		return fmt.Errorf("existing EKS preflight failed: the selected AWS identity cannot authenticate to Kubernetes: %w", err)
	}
	if !strings.EqualFold(strings.TrimSpace(authOutput.String()), "yes") {
		err := fmt.Errorf("the selected AWS identity is authenticated but does not have cluster-wide Kubernetes administration permission")
		jobs.StepFinished(ctx, stepCheckExistingEKS, err)
		return fmt.Errorf("existing EKS preflight failed: %w; add an EKS access entry or update aws-auth", err)
	}

	commands := []Command{
		{Name: d.config.Tools.Kubectl, Args: []string{"get", "nodes", "-o", "wide"}, Dir: d.config.Paths.RepositoryRoot, Env: d.commandEnv(ctx, kubeconfig)},
	}
	if !environment.ManageClusterAddons(doc) {
		storageClasses := existingEKSStorageClasses(doc)
		if len(storageClasses) > 0 {
			commands = append(commands, Command{Name: d.config.Tools.Kubectl, Args: []string{"get", "csidriver", "ebs.csi.aws.com"}, Dir: d.config.Paths.RepositoryRoot, Env: d.commandEnv(ctx, kubeconfig)})
			for _, storageClass := range storageClasses {
				commands = append(commands, Command{Name: d.config.Tools.Kubectl, Args: []string{"get", "storageclass", storageClass}, Dir: d.config.Paths.RepositoryRoot, Env: d.commandEnv(ctx, kubeconfig)})
			}
		}
		if enabledPath(doc, "components.cert_manager.enabled") {
			commands = append(commands, Command{Name: d.config.Tools.Kubectl, Args: []string{"get", "crd", "certificates.cert-manager.io"}, Dir: d.config.Paths.RepositoryRoot, Env: d.commandEnv(ctx, kubeconfig)})
		}
	}
	for _, command := range commands {
		_, _ = fmt.Fprintf(output, "$ %s %s\n", command.Name, strings.Join(command.Args, " "))
		if err := d.executor.Run(ctx, command, output); err != nil {
			jobs.StepFinished(ctx, stepCheckExistingEKS, err)
			return fmt.Errorf("existing EKS preflight failed while running %s %s: %w", command.Name, strings.Join(command.Args, " "), err)
		}
	}
	jobs.StepFinished(ctx, stepCheckExistingEKS, nil)
	return nil
}

func existingEKSStorageClasses(doc environment.Document) []string {
	values := make(map[string]bool)
	for _, item := range []struct{ enabled, class string }{
		{"components.consul.enabled", "components.consul.storage_class"},
		{"components.etcd.enabled", "components.etcd.storage_class"},
		{"components.catalog.loki.enabled", "components.catalog.loki.values.singleBinary.persistence.storageClass"},
		{"components.catalog.clickvisual_stack.enabled", "components.catalog.clickvisual_stack.values.kafka.storage.className"},
		{"components.catalog.clickvisual_stack.enabled", "components.catalog.clickvisual_stack.values.clickhouse.storage.className"},
		{"components.catalog.clickvisual_stack.enabled", "components.catalog.clickvisual_stack.values.mysql.storage.className"},
	} {
		if enabledPath(doc, item.enabled) {
			if raw, ok := environment.GetPath(doc, item.class); ok {
				if value, valid := raw.(string); valid && strings.TrimSpace(value) != "" {
					values[strings.TrimSpace(value)] = true
				}
			}
		}
	}
	result := make([]string, 0, len(values))
	for value := range values {
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}

func enabledPath(doc environment.Document, path string) bool {
	value, ok := environment.GetPath(doc, path)
	enabled, valid := value.(bool)
	return ok && valid && enabled
}

func cloneJobParameters(source map[string]string) map[string]string {
	if len(source) == 0 {
		return nil
	}
	result := make(map[string]string, len(source))
	for key, value := range source {
		result[key] = value
	}
	return result
}

func (d *Deployment) step(ctx context.Context, output io.Writer, label string, command Command) error {
	jobs.StepStarted(ctx, label)
	_, _ = fmt.Fprintf(output, "\n==> %s\n", label)
	_, _ = fmt.Fprintf(output, "$ %s %s\n", command.Name, strings.Join(command.Args, " "))
	if err := d.executor.Run(ctx, command, output); err != nil {
		jobs.StepFinished(ctx, label, err)
		return fmt.Errorf("%s: %w", label, err)
	}
	jobs.StepFinished(ctx, label, nil)
	return nil
}

func (d *Deployment) commandEnv(ctx context.Context, kubeconfig string) []string {
	env := []string{
		"TF_IN_AUTOMATION=1",
		"CHECKPOINT_DISABLE=1",
		"NO_COLOR=1",
	}
	projectEnvironment, hasProjectCredential := ctx.Value(awsEnvironmentContextKey{}).([]string)
	// Bare entries tell mergeEnvironment to remove every inherited AWS identity
	// selector. Only the explicitly selected credential is appended below.
	env = append(env, "AWS_ACCESS_KEY_ID", "AWS_SECRET_ACCESS_KEY", "AWS_PROFILE", "AWS_DEFAULT_PROFILE", "AWS_SESSION_TOKEN")
	if kubeconfig != "" {
		env = append(env, "KUBECONFIG="+kubeconfig)
	}
	if hasProjectCredential && len(projectEnvironment) > 0 {
		env = append(env, projectEnvironment...)
	}
	return env
}

func (d *Deployment) stateBackendCommandEnv(ctx context.Context, runtime statebackend.Runtime) []string {
	// State objects belong to the platform-wide backend account, which may be
	// different from the project's AWS account. These entries intentionally
	// follow commandEnv so mergeEnvironment gives them final precedence.
	env := append(d.commandEnv(ctx, ""),
		"AWS_ACCESS_KEY_ID="+runtime.AccessKeyID,
		"AWS_SECRET_ACCESS_KEY="+runtime.SecretAccessKey,
		"AWS_REGION="+runtime.Region,
		"AWS_DEFAULT_REGION="+runtime.Region,
		"AWS_PROFILE",
		"AWS_DEFAULT_PROFILE",
	)
	if runtime.SessionToken != "" {
		env = append(env, "AWS_SESSION_TOKEN="+runtime.SessionToken)
	} else {
		env = append(env, "AWS_SESSION_TOKEN")
	}
	return env
}

func (d *Deployment) terraformDataEnv(ctx context.Context, name, dir string) []string {
	stage := filepath.Base(dir)
	dataDir := filepath.Join(d.config.Paths.DataDir, "terraform", name, stage)
	env := append(d.commandEnv(ctx, ""), "TF_DATA_DIR="+dataDir)
	if stage == filepath.Base(d.config.Paths.TerraformInfraDir) {
		if passwords, ok := ctx.Value(dataServicePasswordsContextKey{}).(map[string]string); ok && len(passwords) > 0 {
			if encoded, err := json.Marshal(passwords); err == nil {
				env = append(env, "TF_VAR_data_service_passwords="+string(encoded))
			}
		}
	}
	return env
}

func (d *Deployment) terraformEnv(ctx context.Context, name, dir string) []string {
	return append(d.terraformDataEnv(ctx, name, dir), "TF_WORKSPACE="+name)
}

func documentString(doc environment.Document, path string) string {
	value, _ := environment.GetPath(doc, path)
	result, _ := value.(string)
	return strings.TrimSpace(result)
}

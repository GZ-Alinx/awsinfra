package httpapi

import (
	"context"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/GZ-Alinx/awsinfra/internal/access"
	"github.com/GZ-Alinx/awsinfra/internal/environment"
	"github.com/GZ-Alinx/awsinfra/internal/jobs"
	statusservice "github.com/GZ-Alinx/awsinfra/internal/status"
)

type environmentLifecycle struct {
	Status       string
	Detail       string
	UpdatedAt    *time.Time
	LatestJobID  string
	LatestAction string
	LatestStatus string
	Progress     int
}

func (s *Server) enrichProjectEnvironmentLifecycles(ctx context.Context, projects []access.Project) {
	jobGroups := make(map[string][]jobs.Job)
	if s.jobs != nil {
		for _, job := range s.jobs.Snapshot() {
			key := job.Project + "\x00" + job.Environment
			jobGroups[key] = append(jobGroups[key], job)
		}
	}

	type lifecycleTarget struct {
		project     string
		environment *access.ProjectEnvironment
	}
	targets := make([]lifecycleTarget, 0, len(projects)*len(access.EnvironmentDefinitions))
	for projectIndex := range projects {
		for environmentIndex := range projects[projectIndex].Environments {
			targets = append(targets, lifecycleTarget{
				project:     projects[projectIndex].Key,
				environment: &projects[projectIndex].Environments[environmentIndex],
			})
		}
	}
	if len(targets) == 0 {
		return
	}
	targetNames := make([]string, 0, len(targets))
	for _, target := range targets {
		targetNames = append(targetNames, target.environment.TargetName)
	}
	cachedReports := make(map[string]*statusservice.Report)
	if s.status != nil {
		cachedReports = s.status.CachedMany(ctx, targetNames)
	}

	workers := min(8, len(targets))
	queue := make(chan lifecycleTarget)
	var wait sync.WaitGroup
	wait.Add(workers)
	for range workers {
		go func() {
			defer wait.Done()
			for target := range queue {
				item := target.environment
				jobItems := jobGroups[target.project+"\x00"+item.Environment]
				report := cachedReports[item.TargetName]
				existingEKS := report != nil && report.Cluster.TargetType == environment.TargetExistingEKS
				// A cached status already records the immutable target type. Only
				// fall back to the full environment document on a cache miss.
				if report == nil || strings.TrimSpace(report.Cluster.TargetType) == "" {
					if doc, err := s.environments.Load(item.TargetName); err == nil {
						existingEKS = environment.IsExistingEKS(doc)
					}
				}
				lifecycle := deriveEnvironmentLifecycleForTarget(jobItems, report, existingEKS)
				phaseOneDeployed, phaseTwoDeployed := deriveDeploymentPhases(jobItems, report, existingEKS)
				item.LifecycleStatus = lifecycle.Status
				item.LifecycleDetail = lifecycle.Detail
				item.LifecycleUpdatedAt = lifecycle.UpdatedAt
				item.LatestJobID = lifecycle.LatestJobID
				item.LatestJobAction = lifecycle.LatestAction
				item.LatestJobStatus = lifecycle.LatestStatus
				item.LatestJobProgress = lifecycle.Progress
				item.PhaseOneDeployed = phaseOneDeployed
				item.PhaseTwoDeployed = phaseTwoDeployed
			}
		}()
	}
	for _, target := range targets {
		select {
		case queue <- target:
		case <-ctx.Done():
			close(queue)
			wait.Wait()
			return
		}
	}
	close(queue)
	wait.Wait()
}

const (
	phaseOneInfraMutation = "创建或更新 AWS 基础资源"
	phaseOneBaseMutation  = "阶段1 · 安装 EKS 基础组件与基础服务"
	phaseTwoMutation      = "阶段2 · 安装组件并应用接入配置"
)

// deriveDeploymentPhases answers whether the next operation is a first
// deployment or an update. A failed or canceled task counts as an update only
// after it entered an apply step: initialization, validation and planning do
// not prove that this environment owns any resource.
func deriveDeploymentPhases(items []jobs.Job, report *statusservice.Report, existingEKS bool) (bool, bool) {
	items = append([]jobs.Job(nil), items...)
	sort.SliceStable(items, func(i, j int) bool { return items[i].CreatedAt.After(items[j].CreatedAt) })
	phaseOne, phaseTwo, foundDestroyBoundary := false, false, false
	for _, item := range items {
		if item.Action == jobs.ActionDestroy && item.Status == jobs.StatusSucceeded {
			foundDestroyBoundary = true
			break
		}
		switch item.Action {
		case jobs.ActionDeploy:
			phaseOne = phaseOne || item.Status == jobs.StatusSucceeded || jobEnteredMutationStep(item, phaseOneInfraMutation, phaseOneBaseMutation)
		case jobs.ActionPlatform:
			phaseTwo = phaseTwo || item.Status == jobs.StatusSucceeded || jobEnteredMutationStep(item, phaseTwoMutation)
		}
	}
	if !foundDestroyBoundary && !existingEKS && report != nil && report.Cluster.Reachable && strings.EqualFold(strings.TrimSpace(report.Cluster.Status), "ACTIVE") {
		phaseOne = true
	}
	return phaseOne, phaseTwo
}

// shouldResetMissingCloudBaseline reports whether a successful destroy is the
// latest phase-one lifecycle boundary. Planning and read-only operations after
// a destroy do not create resources, while any newer deployment attempt owns
// the next generation (including partially-created resources) and must retain
// its baseline for safe drift detection.
func shouldResetMissingCloudBaseline(items []jobs.Job) bool {
	items = append([]jobs.Job(nil), items...)
	sort.SliceStable(items, func(i, j int) bool { return items[i].CreatedAt.After(items[j].CreatedAt) })
	for _, item := range items {
		switch {
		case item.Action == jobs.ActionDeploy:
			return false
		case item.Action == jobs.ActionDestroy && item.Status == jobs.StatusSucceeded:
			return true
		}
	}
	return false
}

func (s *Server) resetMissingCloudBaselineAfterDestroy(ctx context.Context, project, environmentName string) error {
	if s.resources == nil || !shouldResetMissingCloudBaseline(s.jobs.List(project, environmentName)) {
		return nil
	}
	return s.resources.ResetMissingCloudConfigurationAfterDestroy(ctx, project, environmentName)
}

func jobEnteredMutationStep(item jobs.Job, names ...string) bool {
	if item.Status != jobs.StatusFailed && item.Status != jobs.StatusCanceled && item.Status != jobs.StatusIgnored {
		return false
	}
	for _, step := range item.Steps {
		if !slicesContainString(names, step.Name) {
			continue
		}
		if step.StartedAt != nil || step.Status == jobs.StepRunning || step.Status == jobs.StepSucceeded || step.Status == jobs.StepFailed {
			return true
		}
	}
	return false
}

func slicesContainString(items []string, wanted string) bool {
	for _, item := range items {
		if item == wanted {
			return true
		}
	}
	return false
}

func deriveEnvironmentLifecycle(items []jobs.Job, report *statusservice.Report) environmentLifecycle {
	return deriveEnvironmentLifecycleForTarget(items, report, false)
}

func deriveEnvironmentLifecycleForTarget(items []jobs.Job, report *statusservice.Report, existingEKS bool) environmentLifecycle {
	items = append([]jobs.Job(nil), items...)
	sort.SliceStable(items, func(i, j int) bool { return items[i].CreatedAt.After(items[j].CreatedAt) })
	visibleItems := make([]jobs.Job, 0, len(items))
	for _, item := range items {
		if item.Status != jobs.StatusIgnored {
			visibleItems = append(visibleItems, item)
		}
	}
	if existingEKS && !hasResourceMutation(visibleItems) {
		// The shared cluster can be ACTIVE before this project has installed a
		// single resource. Its AWS status must not make an empty environment look
		// deployed (or prevent the project from being deleted).
		report = nil
	}

	for _, job := range visibleItems {
		if job.Status == jobs.StatusQueued || job.Status == jobs.StatusRunning {
			return lifecycleFromActiveJob(job)
		}
	}

	if len(visibleItems) > 0 {
		latest := visibleItems[0]
		if latest.Status == jobs.StatusFailed {
			return lifecycleFromFailedJob(latest)
		}
		if latest.Status == jobs.StatusCanceled {
			return lifecycleFromJob(latest, "canceled", "最近一次操作已取消")
		}
		if latest.Status == jobs.StatusSucceeded && (latest.Action == jobs.ActionValidate || latest.Action == jobs.ActionPlan) {
			// A successful check or plan makes a previously failed configuration
			// deployable again, but it does not change already-created resources.
			for _, job := range visibleItems[1:] {
				if job.Action == jobs.ActionDeploy || job.Action == jobs.ActionPlatform || job.Action == jobs.ActionAccess || job.Action == jobs.ActionTLS || job.Action == jobs.ActionStorageExpand || job.Action == jobs.ActionStorageShrink || job.Action == jobs.ActionDestroy {
					if job.Status == jobs.StatusSucceeded {
						return lifecycleFromCompletedMutation(job, report)
					}
					break
				}
			}
			return lifecycleFromJob(latest, "ready", "配置已校验，等待部署")
		}
		if latest.Action == jobs.ActionDeploy || latest.Action == jobs.ActionPlatform || latest.Action == jobs.ActionAccess || latest.Action == jobs.ActionTLS || latest.Action == jobs.ActionStorageExpand || latest.Action == jobs.ActionStorageShrink || latest.Action == jobs.ActionDestroy {
			if latest.Status == jobs.StatusSucceeded {
				return lifecycleFromCompletedMutation(latest, report)
			}
		}
	}

	if state, ok := lifecycleFromCachedReport(report, false); ok {
		return state
	}
	if len(items) > 0 && items[0].Status == jobs.StatusIgnored {
		return lifecycleFromJob(items[0], "ready", "最近一次失败已由运维人员忽略，资源结果未被标记为成功")
	}
	return environmentLifecycle{Status: "ready", Detail: "环境配置已保存，等待首次部署"}
}

func hasResourceMutation(items []jobs.Job) bool {
	for _, item := range items {
		if item.Action == jobs.ActionDeploy || item.Action == jobs.ActionPlatform || item.Action == jobs.ActionAccess || item.Action == jobs.ActionTLS || item.Action == jobs.ActionStorageExpand || item.Action == jobs.ActionStorageShrink || item.Action == jobs.ActionDestroy {
			return true
		}
	}
	return false
}

func lifecycleFromActiveJob(job jobs.Job) environmentLifecycle {
	if job.Status == jobs.StatusQueued {
		return lifecycleFromJob(job, "queued", "任务已进入执行队列")
	}
	status, detail := "deploying", "正在创建或更新基础资源"
	switch job.Action {
	case jobs.ActionValidate:
		status, detail = "validating", "正在校验环境和部署参数"
	case jobs.ActionPlan:
		status, detail = "planning", "正在生成基础资源执行计划"
	case jobs.ActionPlatform:
		status, detail = "configuring", "正在安装组件和接入配置"
	case jobs.ActionAccess:
		status, detail = "configuring", "正在更新域名、TLS 与告警接入配置"
	case jobs.ActionTLS:
		status, detail = "configuring", "正在创建或更新 TLS 证书 Secret"
	case jobs.ActionStorageExpand:
		status, detail = "configuring", "正在在线扩容日志平台存储"
	case jobs.ActionStorageShrink:
		status, detail = "configuring", "正在安全迁移并缩小日志平台存储"
	case jobs.ActionDestroy:
		status, detail = "destroying", "正在销毁环境资源"
	}
	return lifecycleFromJob(job, status, detail)
}

func lifecycleFromFailedJob(job jobs.Job) environmentLifecycle {
	status, detail := "deployment_failed", "基础资源部署失败，可进入任务查看原因并重试"
	switch job.Action {
	case jobs.ActionValidate:
		status, detail = "validation_failed", "配置校验失败，可进入任务查看具体问题"
	case jobs.ActionPlan:
		status, detail = "plan_failed", "资源规划失败，可进入任务查看具体问题"
	case jobs.ActionPlatform:
		status, detail = "component_failed", "组件或接入配置失败，可进入任务重试"
	case jobs.ActionAccess:
		status, detail = "component_failed", "域名、TLS 或告警接入配置失败，可进入任务查看原因"
	case jobs.ActionTLS:
		status, detail = "component_failed", "TLS 证书配置失败，可进入任务查看原因并重试"
	case jobs.ActionStorageExpand:
		status, detail = "component_failed", "日志平台存储扩容失败，可进入任务查看原因并重试"
	case jobs.ActionStorageShrink:
		status, detail = "component_failed", "日志平台存储迁移缩容失败，可进入任务查看回滚结果"
	case jobs.ActionDestroy:
		status, detail = "destroy_failed", "环境未完全销毁，可进入任务继续清理"
	}
	if strings.TrimSpace(job.FailureHint) != "" {
		detail = job.FailureHint
	}
	return lifecycleFromJob(job, status, detail)
}

func lifecycleFromCompletedMutation(job jobs.Job, report *statusservice.Report) environmentLifecycle {
	if job.Action == jobs.ActionDestroy {
		return lifecycleFromJob(job, "destroyed", "环境资源已销毁，可修改配置后重新部署")
	}
	if state, ok := lifecycleFromCachedReport(report, true); ok {
		state.LatestJobID = job.ID
		state.LatestAction = string(job.Action)
		state.LatestStatus = string(job.Status)
		state.Progress = job.Progress
		state.UpdatedAt = jobFinishedAt(job)
		return state
	}
	detail := "基础资源运行中"
	if job.Action == jobs.ActionPlatform {
		detail = "基础资源和平台组件已部署"
	} else if job.Action == jobs.ActionAccess {
		detail = "域名、TLS 与告警接入配置已应用"
	} else if job.Action == jobs.ActionTLS {
		detail = "TLS 证书配置已应用"
	} else if job.Action == jobs.ActionStorageExpand {
		detail = "日志平台存储扩容已完成"
	} else if job.Action == jobs.ActionStorageShrink {
		detail = "日志平台存储迁移缩容已完成，原盘已保留"
	}
	return lifecycleFromJob(job, "running", detail)
}

func lifecycleFromCachedReport(report *statusservice.Report, resourcesExpected bool) (environmentLifecycle, bool) {
	if report == nil {
		return environmentLifecycle{}, false
	}
	updatedAt := report.ObservedAt
	state := environmentLifecycle{UpdatedAt: &updatedAt}
	switch strings.ToUpper(strings.TrimSpace(report.Cluster.Status)) {
	case "ACTIVE":
		state.Status, state.Detail = "running", "EKS 集群运行正常"
	case "CREATING", "UPDATING":
		state.Status, state.Detail = "updating", "AWS 正在创建或更新 EKS 集群"
	case "DELETING":
		state.Status, state.Detail = "destroying", "AWS 正在删除 EKS 集群"
	case "FAILED", "DEGRADED":
		state.Status, state.Detail = "abnormal", "EKS 集群状态异常，请进入环境查看"
	case "NOT_FOUND", "":
		if !resourcesExpected {
			return environmentLifecycle{}, false
		}
		state.Status, state.Detail = "abnormal", "最近部署已成功，但未检测到 EKS 集群"
	default:
		state.Status, state.Detail = "abnormal", "EKS 集群当前状态："+report.Cluster.Status
	}
	return state, true
}

func lifecycleFromJob(job jobs.Job, status, detail string) environmentLifecycle {
	return environmentLifecycle{
		Status: status, Detail: detail, UpdatedAt: jobFinishedAt(job),
		LatestJobID: job.ID, LatestAction: string(job.Action), LatestStatus: string(job.Status), Progress: job.Progress,
	}
}

func jobFinishedAt(job jobs.Job) *time.Time {
	if job.FinishedAt != nil {
		value := *job.FinishedAt
		return &value
	}
	if job.StartedAt != nil {
		value := *job.StartedAt
		return &value
	}
	value := job.CreatedAt
	return &value
}

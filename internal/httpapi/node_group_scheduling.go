package httpapi

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"ops-deploy-platform/internal/environment"
	"ops-deploy-platform/internal/jobs"
	statusservice "ops-deploy-platform/internal/status"
)

var errNodeGroupPlanningLocked = errors.New("EKS 已创建，已有节点组受删除和结构变更保护")

func validateNodeGroupPlanningChange(current, next environment.Document, locked bool) error {
	if !locked {
		return nil
	}
	changes := environment.CompareSchedulingPlans(current, next)
	if changes.EnabledChanged {
		return fmt.Errorf("%w：工作负载调度模式不能修改；可以新增节点组或调整已有节点组 Min / Max 容量", errNodeGroupPlanningLocked)
	}
	if len(changes.Removed) > 0 {
		return fmt.Errorf("%w：不能删除已有节点组 %s；请保留它们并按需调整 Min / Max 容量", errNodeGroupPlanningLocked, strings.Join(changes.Removed, "、"))
	}
	if len(changes.Modified) > 0 {
		return fmt.Errorf("%w：已有节点组 %s 的用途、实例规格、网络、磁盘或调度规则不能修改；请新增节点组承载新规划", errNodeGroupPlanningLocked, strings.Join(changes.Modified, "、"))
	}
	return nil
}

func (s *Server) phaseOneAlreadyDeployed(ctx context.Context, project, environmentName, targetName string, doc environment.Document) bool {
	items := []jobs.Job{}
	if s.jobs != nil {
		for _, item := range s.jobs.Snapshot() {
			if item.Project == project && item.Environment == environmentName {
				items = append(items, item)
			}
		}
	}
	var report = s.cachedStatusReport(ctx, targetName)
	deployed, _ := deriveDeploymentPhases(items, report, environment.IsExistingEKS(doc))
	return deployed
}

func (s *Server) cachedStatusReport(ctx context.Context, targetName string) *statusservice.Report {
	if s.status == nil {
		return nil
	}
	return s.status.CachedMany(ctx, []string{targetName})[targetName]
}

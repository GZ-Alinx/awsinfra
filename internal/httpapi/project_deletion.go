package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"ops-deploy-platform/internal/access"
	"ops-deploy-platform/internal/environment"
	"ops-deploy-platform/internal/jobs"
	statusservice "ops-deploy-platform/internal/status"
)

// projectDeletionBlockers is the server-side resource ownership gate. Project
// metadata can only be removed after every environment's Terraform state is
// empty and every resource-changing job has been followed by a successful
// destroy. Existing-EKS environments use the same gate for platform-owned
// resources without requiring the shared cluster itself to disappear.
func (s *Server) projectDeletionBlockers(ctx context.Context, project access.Project) []string {
	blockers := make([]string, 0)
	for _, item := range project.Environments {
		if s.staticCDN != nil {
			cdnResources, cdnErr := s.staticCDN.List(ctx, project.Key, item.Environment, false)
			if cdnErr != nil {
				blockers = append(blockers, fmt.Sprintf("%s：无法确认静态资源 CDN 状态", access.EnvironmentName(item.Environment)))
			} else if len(cdnResources) > 0 {
				blockers = append(blockers, fmt.Sprintf("%s：仍有 %d 个 S3 + CloudFront 静态资源，请先在“静态资源 CDN”中删除", access.EnvironmentName(item.Environment), len(cdnResources)))
			}
		}
		doc, err := s.environments.Load(item.TargetName)
		if err != nil {
			blockers = append(blockers, fmt.Sprintf("%s：无法读取环境配置，不能确认资源归属", access.EnvironmentName(item.Environment)))
			continue
		}
		doc = environment.ApplyDefaults(doc, project.Key, item.Environment)
		label := access.EnvironmentName(item.Environment)

		infraCount, infraFound, infraErr := managedTerraformResourceCountWithMetadata(
			terraformStateMetadataPath(s.config.Paths.DataDir, project.Key, item.TargetName, "infra"),
			terraformWorkspaceStatePath(s.config.Paths.TerraformInfraDir, item.TargetName),
		)
		platformCount, platformFound, platformErr := managedTerraformResourceCountWithMetadata(
			terraformStateMetadataPath(s.config.Paths.DataDir, project.Key, item.TargetName, "platform"),
			terraformWorkspaceStatePath(s.config.Paths.TerraformPlatformDir, item.TargetName),
		)
		if infraErr != nil || platformErr != nil {
			blockers = append(blockers, fmt.Sprintf("%s：Terraform 状态无法读取，请先修复状态并完成销毁", label))
			continue
		}
		if total := infraCount + platformCount; total > 0 {
			detail := make([]string, 0, 2)
			if infraCount > 0 {
				detail = append(detail, fmt.Sprintf("基础资源 %d 项", infraCount))
			}
			if platformCount > 0 {
				detail = append(detail, fmt.Sprintf("组件资源 %d 项", platformCount))
			}
			blockers = append(blockers, fmt.Sprintf("%s：Terraform 仍跟踪%s，请先执行环境销毁", label, strings.Join(detail, "、")))
			continue
		}

		var jobItems []jobs.Job
		if s.jobs != nil {
			jobItems = s.jobs.List(project.Key, item.Environment)
		}
		if mutation, found := latestResourceMutation(jobItems); found {
			if mutation.Action == jobs.ActionDestroy && mutation.Status == jobs.StatusSucceeded {
				continue
			}
			if mutation.Action == jobs.ActionDestroy {
				blockers = append(blockers, fmt.Sprintf("%s：最近一次销毁未成功，请进入任务日志修复并重试", label))
			} else {
				blockers = append(blockers, fmt.Sprintf("%s：已执行过资源或组件部署，必须先完成环境销毁", label))
			}
			continue
		}

		// State files are the primary ownership record. This cached observation is
		// an additional guard for legacy environments whose history/state may have
		// been cleaned manually. A shared existing EKS is intentionally excluded.
		if !environment.IsExistingEKS(doc) && !infraFound && !platformFound && s.status != nil {
			if report, found, _ := s.status.Cached(ctx, item.TargetName); found && clusterMayExist(report) {
				blockers = append(blockers, fmt.Sprintf("%s：仍检测到 EKS 集群状态 %s，请先在环境中执行销毁", label, report.Cluster.Status))
			}
		}
	}
	return blockers
}

// environmentStateBlockers verifies the authoritative ownership records after
// a successful destroy. It intentionally ignores deployment history because
// the completion hook runs while the destroy job is still marked as running.
func (s *Server) environmentStateBlockers(project string, item access.ProjectEnvironment) []string {
	if s.staticCDN != nil {
		cdnResources, err := s.staticCDN.List(context.Background(), project, item.Environment, false)
		if err != nil {
			return []string{"无法确认静态资源 CDN 状态，平台已保留环境配置"}
		}
		if len(cdnResources) > 0 {
			return []string{fmt.Sprintf("仍有 %d 个 S3 + CloudFront 静态资源，平台已保留环境配置", len(cdnResources))}
		}
	}
	infraCount, _, infraErr := managedTerraformResourceCountWithMetadata(
		terraformStateMetadataPath(s.config.Paths.DataDir, project, item.TargetName, "infra"),
		terraformWorkspaceStatePath(s.config.Paths.TerraformInfraDir, item.TargetName),
	)
	platformCount, _, platformErr := managedTerraformResourceCountWithMetadata(
		terraformStateMetadataPath(s.config.Paths.DataDir, project, item.TargetName, "platform"),
		terraformWorkspaceStatePath(s.config.Paths.TerraformPlatformDir, item.TargetName),
	)
	if infraErr != nil || platformErr != nil {
		return []string{"销毁已结束，但 Terraform 状态元数据无法读取，平台已保留环境配置以便排查"}
	}
	if infraCount+platformCount == 0 {
		return nil
	}
	return []string{fmt.Sprintf("销毁已结束，但 Terraform 仍跟踪基础资源 %d 项、组件资源 %d 项，平台已保留环境配置", infraCount, platformCount)}
}

func terraformWorkspaceStatePath(terraformDir, targetName string) string {
	return filepath.Join(terraformDir, "terraform.tfstate.d", targetName, "terraform.tfstate")
}

func terraformStateMetadataPath(dataDir, project, targetName, stage string) string {
	return filepath.Join(dataDir, "state-metadata", project, targetName, stage+".json")
}

func managedTerraformResourceCountWithMetadata(metadataPath, legacyStatePath string) (count int, found bool, err error) {
	payload, metadataErr := os.ReadFile(metadataPath) // #nosec G304 -- path is derived from validated project/environment and configured data root.
	if metadataErr == nil {
		var metadata struct {
			Backend          string `json:"backend"`
			ManagedResources int    `json:"managed_resources"`
		}
		if err := json.Unmarshal(payload, &metadata); err != nil {
			return 0, true, err
		}
		if metadata.Backend != "s3" || metadata.ManagedResources < 0 {
			return 0, true, errors.New("invalid Terraform state metadata")
		}
		return metadata.ManagedResources, true, nil
	}
	if !errors.Is(metadataErr, os.ErrNotExist) {
		return 0, true, metadataErr
	}
	return managedTerraformResourceCount(legacyStatePath)
}

func managedTerraformResourceCount(path string) (count int, found bool, err error) {
	payload, err := os.ReadFile(path) // #nosec G304 -- path is built from administrator-owned Terraform roots and a validated environment target name.
	if os.IsNotExist(err) {
		return 0, false, nil
	}
	if err != nil {
		return 0, true, err
	}
	var state struct {
		Resources []struct {
			Mode      string            `json:"mode"`
			Instances []json.RawMessage `json:"instances"`
		} `json:"resources"`
	}
	if err := json.Unmarshal(payload, &state); err != nil {
		return 0, true, err
	}
	for _, resource := range state.Resources {
		if resource.Mode == "managed" {
			count += len(resource.Instances)
		}
	}
	return count, true, nil
}

func latestResourceMutation(items []jobs.Job) (jobs.Job, bool) {
	mutations := make([]jobs.Job, 0, len(items))
	for _, item := range items {
		if item.Action == jobs.ActionDeploy || item.Action == jobs.ActionPlatform || item.Action == jobs.ActionAccess || item.Action == jobs.ActionTLS || item.Action == jobs.ActionDestroy {
			mutations = append(mutations, item)
		}
	}
	if len(mutations) == 0 {
		return jobs.Job{}, false
	}
	sort.SliceStable(mutations, func(i, j int) bool { return mutations[i].CreatedAt.After(mutations[j].CreatedAt) })
	return mutations[0], true
}

func clusterMayExist(report *statusservice.Report) bool {
	if report == nil {
		return false
	}
	status := strings.ToUpper(strings.TrimSpace(report.Cluster.Status))
	return status != "" && status != "NOT_FOUND"
}

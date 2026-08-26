package runner

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/GZ-Alinx/awsinfra/internal/environment"
	"github.com/GZ-Alinx/awsinfra/internal/jobs"
)

type managedPVC struct {
	Name             string
	Namespace        string
	Component        string
	StorageClass     string
	Requested        string
	Capacity         string
	AccessModes      []string
	VolumeMode       string
	WorkloadKind     string
	WorkloadName     string
	VolumeName       string
	Project          string
	Environment      string
	Active           bool
	AllowExpansion   bool
	OriginalReplicas int
	VolumeIndex      int
}

type managedWorkload struct {
	Name       string
	Namespace  string
	Replicas   int
	Ready      int
	Volumes    []managedWorkloadVolume
	MatchLabel map[string]string
}

type managedWorkloadVolume struct {
	Name      string
	ClaimName string
}

type managedMigration struct {
	source  managedPVC
	target  string
	copyPod string
}

func (d *Deployment) resizeClickVisualStorage(ctx context.Context, environmentName, jobID string, action jobs.Action, doc environment.Document, output io.Writer) error {
	parameters, _ := ctx.Value(jobParametersContextKey{}).(map[string]string)
	component := strings.ToLower(strings.TrimSpace(parameters["component"]))
	openTelemetryStorage := component == "opentelemetry_collector" || component == "otel-elasticsearch"
	if openTelemetryStorage {
		if !enabledPath(doc, "components.catalog.opentelemetry_collector.enabled") {
			return errors.New("OpenTelemetry 尚未启用，不能执行存储操作")
		}
		if component == "otel-elasticsearch" && !boolAt(doc, "components.catalog.opentelemetry_collector.values.elasticsearch.enabled") {
			return errors.New("OpenTelemetry 专用 Elasticsearch 尚未启用，不能执行存储操作")
		}
		if action != jobs.ActionStorageExpand {
			return errors.New("OpenTelemetry 持久化存储只支持在线扩容，不支持平台自动缩容")
		}
	} else {
		if !enabledPath(doc, "components.catalog.clickvisual_stack.enabled") {
			return errors.New("ClickVisual 日志平台尚未启用，不能执行存储操作")
		}
		if component != "kafka" && component != "clickhouse" && component != "mysql" {
			return errors.New("存储任务参数无效：只支持 kafka、clickhouse、mysql、opentelemetry_collector 或 otel-elasticsearch")
		}
	}
	targetGi, err := strconv.Atoi(strings.TrimSpace(parameters["target_size_gi"]))
	minimumGi := 1
	if component == "otel-elasticsearch" {
		minimumGi = 10
	}
	if err != nil || targetGi < minimumGi || targetGi > 16384 {
		return fmt.Errorf("存储任务参数无效：目标容量必须在 %d GiB 到 16384 GiB 之间", minimumGi)
	}
	safetyPercent, err := strconv.Atoi(strings.TrimSpace(parameters["safety_percent"]))
	if err != nil || safetyPercent < 10 || safetyPercent > 100 {
		return errors.New("存储任务参数无效：安全余量必须在 10% 到 100% 之间")
	}

	kubeconfig, err := d.updateKubeconfig(ctx, environmentName, doc, output)
	if err != nil {
		return err
	}
	pvcs, workloads, err := d.inspectManagedStorage(ctx, kubeconfig, doc, component, output)
	if err != nil {
		return err
	}
	active := make([]managedPVC, 0)
	for _, pvc := range pvcs {
		if pvc.Component == component && pvc.Active {
			active = append(active, pvc)
		}
	}
	if len(active) == 0 {
		return fmt.Errorf("%s 未发现正在使用的 PVC，请先完成组件部署", component)
	}
	targetBytes := int64(targetGi) * 1024 * 1024 * 1024
	pending := make([]managedPVC, 0, len(active))
	for _, pvc := range active {
		requestedBytes, valid := kubernetesStorageBytes(pvc.Requested)
		if !valid {
			return fmt.Errorf("PVC %s/%s 的当前容量 %q 无法识别", pvc.Namespace, pvc.Name, pvc.Requested)
		}
		switch action {
		case jobs.ActionStorageExpand:
			if requestedBytes > targetBytes {
				return fmt.Errorf("PVC %s/%s 当前请求容量为 %s，扩容目标不能更小", pvc.Namespace, pvc.Name, pvc.Requested)
			}
			capacityBytes, capacityValid := kubernetesStorageBytes(pvc.Capacity)
			if requestedBytes < targetBytes || !capacityValid || capacityBytes < targetBytes {
				pending = append(pending, pvc)
			}
		case jobs.ActionStorageShrink:
			if requestedBytes < targetBytes {
				return fmt.Errorf("PVC %s/%s 当前容量为 %s，缩容目标不能更大", pvc.Namespace, pvc.Name, pvc.Requested)
			}
			if requestedBytes > targetBytes {
				pending = append(pending, pvc)
			}
		}
	}
	if len(pending) == 0 {
		environment.SetPath(doc, managedStorageSizePath(component), fmt.Sprintf("%dGi", targetGi))
		if action == jobs.ActionStorageShrink {
			for _, pvc := range active {
				setClickVisualActiveClaim(doc, component, pvc.WorkloadName, pvc.Name)
			}
		}
		if err := d.environments.Save(environmentName, doc); err != nil {
			return fmt.Errorf("保存已完成的存储配置: %w", err)
		}
		d.finishNoopStorageSteps(ctx, action)
		_, _ = fmt.Fprintf(output, "%s 的全部活动 PVC 已达到 %dGi，本次重试无需重复变更。\n", component, targetGi)
		return nil
	}
	switch action {
	case jobs.ActionStorageExpand:
		return d.expandManagedStorage(ctx, environmentName, kubeconfig, component, targetGi, targetBytes, doc, pending, workloads, output)
	case jobs.ActionStorageShrink:
		return d.shrinkManagedStorage(ctx, environmentName, kubeconfig, component, targetGi, targetBytes, safetyPercent, jobID, doc, pending, active, workloads, output)
	default:
		return jobs.ErrInvalidAction
	}
}

func (d *Deployment) finishNoopStorageSteps(ctx context.Context, action jobs.Action) {
	steps := []string{stepExpandManagedStorage, stepVerifyManagedStorage}
	if action == jobs.ActionStorageShrink {
		steps = []string{stepStopStorageWorkload, stepMigrateManagedStorage, stepSwitchManagedStorage, stepVerifyManagedStorage}
	}
	for _, step := range steps {
		jobs.StepStarted(ctx, step)
		jobs.StepFinished(ctx, step, nil)
	}
}

func (d *Deployment) inspectManagedStorage(ctx context.Context, kubeconfig string, doc environment.Document, component string, output io.Writer) ([]managedPVC, map[string]managedWorkload, error) {
	jobs.StepStarted(ctx, stepInspectManagedStorage)
	_, _ = fmt.Fprintf(output, "\n==> %s\n", stepInspectManagedStorage)
	stack := "clickvisual"
	if component == "opentelemetry_collector" || component == "otel-elasticsearch" {
		stack = "opentelemetry"
	}
	pvcPayload, err := d.storageKubectlCapture(ctx, kubeconfig, output,
		"get", "persistentvolumeclaims", "-A", "-l", "ops-deploy.io/stack="+stack, "-o", "json")
	if err != nil {
		jobs.StepFinished(ctx, stepInspectManagedStorage, err)
		return nil, nil, fmt.Errorf("读取日志平台 PVC: %w", err)
	}
	classPayload, err := d.storageKubectlCapture(ctx, kubeconfig, output,
		"get", "storageclasses.storage.k8s.io", "-o", "json")
	if err != nil {
		jobs.StepFinished(ctx, stepInspectManagedStorage, err)
		return nil, nil, fmt.Errorf("读取 StorageClass: %w", err)
	}
	workloadPayload, err := d.storageKubectlCapture(ctx, kubeconfig, output,
		"get", "statefulsets.apps", "-A", "-l", "ops-deploy.io/stack="+stack, "-o", "json")
	if err != nil {
		jobs.StepFinished(ctx, stepInspectManagedStorage, err)
		return nil, nil, fmt.Errorf("读取日志平台 StatefulSet: %w", err)
	}
	pvcs, workloads, err := decodeRunnerManagedStorage(pvcPayload, classPayload, workloadPayload)
	if err != nil {
		jobs.StepFinished(ctx, stepInspectManagedStorage, err)
		return nil, nil, fmt.Errorf("解析日志平台存储状态: %w", err)
	}
	project, environmentName := documentString(doc, "project"), documentString(doc, "environment")
	filtered := pvcs[:0]
	for _, pvc := range pvcs {
		if pvc.Project == project && pvc.Environment == environmentName {
			filtered = append(filtered, pvc)
		}
	}
	_, _ = fmt.Fprintf(output, "已发现 %d 个当前环境受管 PVC，目标子组件：%s。\n", len(filtered), component)
	jobs.StepFinished(ctx, stepInspectManagedStorage, nil)
	return filtered, workloads, nil
}

func (d *Deployment) expandManagedStorage(ctx context.Context, environmentName, kubeconfig, component string, targetGi int, targetBytes int64, doc environment.Document, pvcs []managedPVC, workloads map[string]managedWorkload, output io.Writer) error {
	jobs.StepStarted(ctx, stepExpandManagedStorage)
	_, _ = fmt.Fprintf(output, "\n==> %s\n", stepExpandManagedStorage)
	environment.SetPath(doc, managedStorageSizePath(component), fmt.Sprintf("%dGi", targetGi))
	if err := d.environments.Save(environmentName, doc); err != nil {
		jobs.StepFinished(ctx, stepExpandManagedStorage, err)
		return fmt.Errorf("保存扩容后的期望配置: %w", err)
	}
	for _, pvc := range pvcs {
		currentBytes, valid := kubernetesStorageBytes(pvc.Requested)
		if !valid {
			err := fmt.Errorf("PVC %s/%s 的当前容量 %q 无法识别", pvc.Namespace, pvc.Name, pvc.Requested)
			jobs.StepFinished(ctx, stepExpandManagedStorage, err)
			return err
		}
		if currentBytes > targetBytes {
			err := fmt.Errorf("PVC %s/%s 当前容量为 %s，扩容目标不能更小", pvc.Namespace, pvc.Name, pvc.Requested)
			jobs.StepFinished(ctx, stepExpandManagedStorage, err)
			return err
		}
		if !pvc.AllowExpansion {
			err := fmt.Errorf("PVC %s/%s 使用的 StorageClass %s 不允许在线扩容", pvc.Namespace, pvc.Name, pvc.StorageClass)
			jobs.StepFinished(ctx, stepExpandManagedStorage, err)
			return err
		}
		if currentBytes < targetBytes {
			patch, _ := json.Marshal(map[string]any{
				"spec": map[string]any{"resources": map[string]any{"requests": map[string]string{"storage": fmt.Sprintf("%dGi", targetGi)}}},
			})
			if err := d.storageKubectlRun(ctx, kubeconfig, output, "patch", "persistentvolumeclaim", pvc.Name, "-n", pvc.Namespace, "--type=merge", "-p", string(patch)); err != nil {
				jobs.StepFinished(ctx, stepExpandManagedStorage, err)
				return fmt.Errorf("提交 PVC %s/%s 扩容: %w", pvc.Namespace, pvc.Name, err)
			}
			_, _ = fmt.Fprintf(output, "PVC %s/%s 已提交扩容：%s -> %dGi。\n", pvc.Namespace, pvc.Name, pvc.Requested, targetGi)
		} else {
			_, _ = fmt.Fprintf(output, "PVC %s/%s 已提交过 %dGi 请求，继续等待实际容量完成扩展。\n", pvc.Namespace, pvc.Name, targetGi)
		}
	}
	jobs.StepFinished(ctx, stepExpandManagedStorage, nil)

	jobs.StepStarted(ctx, stepVerifyManagedStorage)
	_, _ = fmt.Fprintf(output, "\n==> %s\n", stepVerifyManagedStorage)
	for _, pvc := range pvcs {
		if err := d.waitForPVCCapacity(ctx, kubeconfig, pvc.Namespace, pvc.Name, targetBytes, 20*time.Minute, output); err != nil {
			jobs.StepFinished(ctx, stepVerifyManagedStorage, err)
			return err
		}
	}
	if err := d.verifyManagedWorkloads(ctx, kubeconfig, pvcs, workloads, output); err != nil {
		jobs.StepFinished(ctx, stepVerifyManagedStorage, err)
		return err
	}
	_, _ = fmt.Fprintf(output, "%s 存储已在线扩容到 %dGi，工作负载保持可用。\n", component, targetGi)
	jobs.StepFinished(ctx, stepVerifyManagedStorage, nil)
	return nil
}

func (d *Deployment) reconcileOpenTelemetryStorage(ctx context.Context, kubeconfig string, doc environment.Document, output io.Writer) error {
	jobs.StepStarted(ctx, stepReconcileCollectorWAL)
	_, _ = fmt.Fprintf(output, "\n==> %s\n", stepReconcileCollectorWAL)
	fail := func(err error) error {
		jobs.StepFinished(ctx, stepReconcileCollectorWAL, err)
		return err
	}
	if !enabledPath(doc, "components.catalog.opentelemetry_collector.enabled") {
		_, _ = fmt.Fprintln(output, "OpenTelemetry Collector 未启用，无需对账队列存储。")
		jobs.StepFinished(ctx, stepReconcileCollectorWAL, nil)
		return nil
	}
	namespace := "platform-server"
	if value, ok := environment.GetPath(doc, "components.catalog.opentelemetry_collector.namespace"); ok {
		if configured := strings.TrimSpace(fmt.Sprint(value)); configured != "" {
			namespace = configured
		}
	}
	targets := []struct {
		component string
		display   string
		path      string
		enabled   bool
	}{
		{component: "opentelemetry_collector", display: "Collector WAL", path: "components.catalog.opentelemetry_collector.values.storage.expandedSize", enabled: true},
		{component: "otel-elasticsearch", display: "OpenTelemetry Elasticsearch", path: "components.catalog.opentelemetry_collector.values.elasticsearch.storage.expandedSize", enabled: boolAt(doc, "components.catalog.opentelemetry_collector.values.elasticsearch.enabled")},
	}
	for _, item := range targets {
		if !item.enabled {
			continue
		}
		rawTarget, _ := environment.GetPath(doc, item.path)
		target := strings.TrimSpace(fmt.Sprint(rawTarget))
		if target == "" || target == "<nil>" {
			_, _ = fmt.Fprintf(output, "%s 尚未执行过在线扩容，保留初始 PVC 容量。\n", item.display)
			continue
		}
		if err := d.reconcileOpenTelemetryPVCs(ctx, kubeconfig, namespace, item.component, item.display, target, output); err != nil {
			return fail(err)
		}
	}
	jobs.StepFinished(ctx, stepReconcileCollectorWAL, nil)
	return nil
}

func (d *Deployment) reconcileOpenTelemetryPVCs(ctx context.Context, kubeconfig, namespace, component, display, target string, output io.Writer) error {
	targetBytes, valid := kubernetesStorageBytes(target)
	if !valid {
		return fmt.Errorf("%s 扩容目标 %q 无法识别", display, target)
	}
	payload, err := d.storageKubectlCapture(ctx, kubeconfig, output,
		"get", "persistentvolumeclaims", "-n", namespace, "-l", "ops-deploy.io/stack=opentelemetry,ops-deploy.io/component="+component, "-o", "json")
	if err != nil {
		return fmt.Errorf("读取 %s PVC: %w", display, err)
	}
	var response struct {
		Items []struct {
			Metadata struct {
				Name string `json:"name"`
			} `json:"metadata"`
			Spec struct {
				Resources struct {
					Requests map[string]string `json:"requests"`
				} `json:"resources"`
			} `json:"spec"`
		} `json:"items"`
	}
	if err := json.Unmarshal(payload, &response); err != nil {
		return fmt.Errorf("解析 %s PVC: %w", display, err)
	}
	if len(response.Items) == 0 {
		return fmt.Errorf("%s 已启用但未发现 PVC", display)
	}
	for _, item := range response.Items {
		requested := item.Spec.Resources.Requests["storage"]
		requestedBytes, requestedValid := kubernetesStorageBytes(requested)
		if !requestedValid {
			return fmt.Errorf("PVC %s/%s 的容量 %q 无法识别", namespace, item.Metadata.Name, requested)
		}
		if requestedBytes > targetBytes {
			return fmt.Errorf("PVC %s/%s 已为 %s，大于配置的扩容目标 %s；平台不会自动缩容", namespace, item.Metadata.Name, requested, target)
		}
		if requestedBytes < targetBytes {
			patch, _ := json.Marshal(map[string]any{
				"spec": map[string]any{"resources": map[string]any{"requests": map[string]string{"storage": target}}},
			})
			if err := d.storageKubectlRun(ctx, kubeconfig, output, "patch", "persistentvolumeclaim", item.Metadata.Name, "-n", namespace, "--type=merge", "-p", string(patch)); err != nil {
				return fmt.Errorf("扩容 %s PVC %s/%s: %w", display, namespace, item.Metadata.Name, err)
			}
			_, _ = fmt.Fprintf(output, "%s PVC %s/%s 已按平台目标从 %s 扩容到 %s。\n", display, namespace, item.Metadata.Name, requested, target)
		}
		if err := d.waitForPVCCapacity(ctx, kubeconfig, namespace, item.Metadata.Name, targetBytes, 20*time.Minute, output); err != nil {
			return err
		}
	}
	_, _ = fmt.Fprintf(output, "%s 的 %d 个活动 PVC 已全部达到 %s。\n", display, len(response.Items), target)
	return nil
}

func (d *Deployment) shrinkManagedStorage(ctx context.Context, environmentName, kubeconfig, component string, targetGi int, targetBytes int64, safetyPercent int, jobID string, doc environment.Document, pvcs, allActive []managedPVC, workloads map[string]managedWorkload, output io.Writer) error {
	for _, pvc := range pvcs {
		currentBytes, valid := kubernetesStorageBytes(pvc.Requested)
		if !valid {
			return fmt.Errorf("PVC %s/%s 的当前容量 %q 无法识别", pvc.Namespace, pvc.Name, pvc.Requested)
		}
		if currentBytes <= targetBytes {
			return fmt.Errorf("PVC %s/%s 当前容量为 %s，缩容目标必须更小", pvc.Namespace, pvc.Name, pvc.Requested)
		}
	}
	originalDoc, err := cloneEnvironmentDocument(doc)
	if err != nil {
		return err
	}
	for _, pvc := range allActive {
		setClickVisualActiveClaim(doc, component, pvc.WorkloadName, pvc.Name)
	}
	for index := range pvcs {
		if !strings.EqualFold(pvcs[index].WorkloadKind, "statefulset") ||
			strings.TrimSpace(pvcs[index].WorkloadName) == "" ||
			strings.TrimSpace(pvcs[index].VolumeName) == "" {
			return fmt.Errorf(
				"PVC %s/%s 缺少受支持的 StatefulSet 归属标签，平台拒绝自动迁移",
				pvcs[index].Namespace,
				pvcs[index].Name,
			)
		}
		workload, exists := workloads[pvcWorkloadKey(pvcs[index])]
		if !exists {
			return fmt.Errorf("找不到 PVC %s/%s 对应的 StatefulSet %s", pvcs[index].Namespace, pvcs[index].Name, pvcs[index].WorkloadName)
		}
		pvcs[index].OriginalReplicas = workload.Replicas
		pvcs[index].VolumeIndex = -1
		for volumeIndex, volume := range workload.Volumes {
			if volume.Name == pvcs[index].VolumeName && volume.ClaimName == pvcs[index].Name {
				pvcs[index].VolumeIndex = volumeIndex
				break
			}
		}
		if pvcs[index].VolumeIndex < 0 {
			return fmt.Errorf("StatefulSet %s/%s 没有引用预期的 PVC 卷 %s，平台拒绝自动切换", pvcs[index].Namespace, pvcs[index].WorkloadName, pvcs[index].VolumeName)
		}
	}

	jobs.StepStarted(ctx, stepStopStorageWorkload)
	_, _ = fmt.Fprintf(output, "\n==> %s\n", stepStopStorageWorkload)
	scaledDown := make([]managedPVC, 0, len(pvcs))
	for _, pvc := range pvcs {
		if err := d.scaleManagedWorkload(ctx, kubeconfig, pvc, 0, output); err != nil {
			d.restoreManagedWorkloads(ctx, kubeconfig, scaledDown, output)
			jobs.StepFinished(ctx, stepStopStorageWorkload, err)
			return err
		}
		scaledDown = append(scaledDown, pvc)
	}
	for _, pvc := range pvcs {
		if err := d.waitForStatefulSetReplicas(ctx, kubeconfig, pvc.Namespace, pvc.WorkloadName, 0, 10*time.Minute, output); err != nil {
			d.restoreManagedWorkloads(ctx, kubeconfig, scaledDown, output)
			jobs.StepFinished(ctx, stepStopStorageWorkload, err)
			return err
		}
	}
	_, _ = fmt.Fprintln(output, "目标子组件已停止，源 PVC 不再被业务 Pod 写入。")
	jobs.StepFinished(ctx, stepStopStorageWorkload, nil)

	migrated := make([]managedMigration, 0, len(pvcs))
	jobs.StepStarted(ctx, stepMigrateManagedStorage)
	_, _ = fmt.Fprintf(output, "\n==> %s\n", stepMigrateManagedStorage)
	for index, pvc := range pvcs {
		suffix := storageJobSuffix(jobID, index)
		targetName := storageReplacementPVCName(pvc.Name, suffix)
		copyPod := storageMigrationPodName(component, suffix)
		if err := d.createReplacementPVC(ctx, kubeconfig, doc, pvc, targetName, targetGi, output); err != nil {
			d.restoreManagedWorkloads(ctx, kubeconfig, scaledDown, output)
			jobs.StepFinished(ctx, stepMigrateManagedStorage, err)
			return err
		}
		if err := d.copyManagedPVC(ctx, kubeconfig, doc, pvc, targetName, copyPod, targetGi, safetyPercent, output); err != nil {
			d.restoreManagedWorkloads(ctx, kubeconfig, scaledDown, output)
			jobs.StepFinished(ctx, stepMigrateManagedStorage, err)
			return fmt.Errorf("PVC %s/%s 数据迁移失败，原 PVC 未切换且服务已恢复: %w", pvc.Namespace, pvc.Name, err)
		}
		migrated = append(migrated, managedMigration{source: pvc, target: targetName, copyPod: copyPod})
	}
	jobs.StepFinished(ctx, stepMigrateManagedStorage, nil)

	jobs.StepStarted(ctx, stepSwitchManagedStorage)
	_, _ = fmt.Fprintf(output, "\n==> %s\n", stepSwitchManagedStorage)
	switched := make([]managedMigration, 0, len(migrated))
	for _, item := range migrated {
		if err := d.patchManagedWorkloadClaim(ctx, kubeconfig, item.source, item.target, output); err != nil {
			d.rollbackManagedStorage(ctx, kubeconfig, switched, scaledDown, output)
			jobs.StepFinished(ctx, stepSwitchManagedStorage, err)
			return fmt.Errorf("切换到新 PVC 失败，已回滚原 PVC: %w", err)
		}
		switched = append(switched, item)
		setClickVisualActiveClaim(doc, component, item.source.WorkloadName, item.target)
	}
	environment.SetPath(doc, managedStorageSizePath(component), fmt.Sprintf("%dGi", targetGi))
	if err := d.environments.Save(environmentName, doc); err != nil {
		d.rollbackManagedStorage(ctx, kubeconfig, switched, scaledDown, output)
		_ = d.environments.Save(environmentName, originalDoc)
		jobs.StepFinished(ctx, stepSwitchManagedStorage, err)
		return fmt.Errorf("保存新 PVC 绑定配置失败，已回滚原 PVC: %w", err)
	}
	jobs.StepFinished(ctx, stepSwitchManagedStorage, nil)

	jobs.StepStarted(ctx, stepVerifyManagedStorage)
	_, _ = fmt.Fprintf(output, "\n==> %s\n", stepVerifyManagedStorage)
	for _, pvc := range pvcs {
		if err := d.scaleManagedWorkload(ctx, kubeconfig, pvc, pvc.OriginalReplicas, output); err != nil {
			d.rollbackManagedStorage(ctx, kubeconfig, switched, scaledDown, output)
			_ = d.environments.Save(environmentName, originalDoc)
			jobs.StepFinished(ctx, stepVerifyManagedStorage, err)
			return fmt.Errorf("恢复工作负载失败，已尝试回滚原 PVC: %w", err)
		}
	}
	for _, pvc := range pvcs {
		if err := d.waitForManagedWorkloadReady(ctx, kubeconfig, pvc, 20*time.Minute, output); err != nil {
			d.rollbackManagedStorage(ctx, kubeconfig, switched, scaledDown, output)
			_ = d.environments.Save(environmentName, originalDoc)
			jobs.StepFinished(ctx, stepVerifyManagedStorage, err)
			return fmt.Errorf("新 PVC 启动验证失败，已尝试回滚原 PVC: %w", err)
		}
	}
	for _, item := range migrated {
		annotation, _ := json.Marshal(map[string]any{"metadata": map[string]any{"annotations": map[string]string{"ops-deploy.io/retained-after-resize": "true"}}})
		_ = d.storageKubectlRun(ctx, kubeconfig, output, "patch", "persistentvolumeclaim", item.source.Name, "-n", item.source.Namespace, "--type=merge", "-p", string(annotation))
		_ = d.storageKubectlRun(ctx, kubeconfig, output, "delete", "pod", item.copyPod, "-n", item.source.Namespace, "--ignore-not-found=true", "--wait=false")
	}
	_, _ = fmt.Fprintf(output, "%s 已安全迁移到 %dGi。原 PVC 已保留，可由运维确认数据后再清理。\n", component, targetGi)
	jobs.StepFinished(ctx, stepVerifyManagedStorage, nil)
	return nil
}

func (d *Deployment) createReplacementPVC(ctx context.Context, kubeconfig string, doc environment.Document, source managedPVC, target string, targetGi int, output io.Writer) error {
	spec := map[string]any{
		"accessModes":      source.AccessModes,
		"storageClassName": source.StorageClass,
		"resources":        map[string]any{"requests": map[string]string{"storage": fmt.Sprintf("%dGi", targetGi)}},
	}
	if source.VolumeMode != "" {
		spec["volumeMode"] = source.VolumeMode
	}
	manifest := map[string]any{
		"apiVersion": "v1",
		"kind":       "PersistentVolumeClaim",
		"metadata": map[string]any{
			"name": target, "namespace": source.Namespace,
			"annotations": map[string]string{"helm.sh/resource-policy": "keep"},
			"labels": map[string]string{
				"ops-deploy.io/stack":         "clickvisual",
				"ops-deploy.io/project":       documentString(doc, "project"),
				"ops-deploy.io/environment":   documentString(doc, "environment"),
				"ops-deploy.io/component":     source.Component,
				"ops-deploy.io/workload-kind": source.WorkloadKind,
				"ops-deploy.io/workload-name": source.WorkloadName,
				"ops-deploy.io/volume-name":   source.VolumeName,
			},
		},
		"spec": spec,
	}
	payload, _ := json.Marshal(manifest)
	return d.storageKubectlInput(ctx, kubeconfig, output, payload, "apply", "-f", "-")
}

func (d *Deployment) copyManagedPVC(ctx context.Context, kubeconfig string, doc environment.Document, source managedPVC, target, podName string, targetGi, safetyPercent int, output io.Writer) error {
	// Keep the migration runtime platform-controlled. A project Helm value must
	// never be able to substitute an arbitrary image that receives both PVCs.
	const toolboxImage = "busybox:1.36"
	script := fmt.Sprintf(`set -eu
used_kb="$(du -sk /source | awk '{print $1}')"
target_kb=%d
required_kb=$((used_kb + (used_kb * %d / 100)))
echo "源数据 ${used_kb} KiB；目标容量 ${target_kb} KiB；包含安全余量后需要 ${required_kb} KiB"
if [ "$required_kb" -ge "$target_kb" ]; then
  echo "目标容量不足：请提高目标容量或清理源数据后重试" >&2
  exit 42
fi
cp -a /source/. /target/
sync
copied_kb="$(du -sk /target | awk '{print $1}')"
echo "复制完成，目标已写入 ${copied_kb} KiB"
if [ "$copied_kb" -lt "$used_kb" ]; then
  echo "复制后数据量小于源数据，拒绝切换" >&2
  exit 43
fi`, targetGi*1024*1024, safetyPercent)
	manifest := map[string]any{
		"apiVersion": "v1", "kind": "Pod",
		"metadata": map[string]any{
			"name": podName, "namespace": source.Namespace,
			"labels": map[string]string{
				"ops-deploy.io/stack":       "clickvisual",
				"ops-deploy.io/project":     documentString(doc, "project"),
				"ops-deploy.io/environment": documentString(doc, "environment"),
				"ops-deploy.io/component":   "storage-migration",
			},
		},
		"spec": map[string]any{
			"restartPolicy":                "Never",
			"automountServiceAccountToken": false,
			"securityContext":              map[string]any{"seccompProfile": map[string]string{"type": "RuntimeDefault"}},
			"containers": []any{map[string]any{
				"name": "copy", "image": toolboxImage,
				"command": []string{"sh", "-ec", script},
				"securityContext": map[string]any{
					"runAsUser": 0, "runAsNonRoot": false, "allowPrivilegeEscalation": false,
					"capabilities": map[string]any{"drop": []string{"ALL"}, "add": []string{"CHOWN", "FOWNER", "DAC_OVERRIDE"}},
				},
				"resources": map[string]any{
					"requests": map[string]string{"cpu": "100m", "memory": "128Mi"},
					"limits":   map[string]string{"cpu": "2", "memory": "2Gi"},
				},
				"volumeMounts": []any{
					map[string]any{"name": "source", "mountPath": "/source", "readOnly": true},
					map[string]any{"name": "target", "mountPath": "/target"},
				},
			}},
			"volumes": []any{
				map[string]any{"name": "source", "persistentVolumeClaim": map[string]string{"claimName": source.Name}},
				map[string]any{"name": "target", "persistentVolumeClaim": map[string]string{"claimName": target}},
			},
		},
	}
	payload, _ := json.Marshal(manifest)
	if err := d.storageKubectlInput(ctx, kubeconfig, output, payload, "apply", "-f", "-"); err != nil {
		return err
	}
	if err := d.waitForMigrationPod(ctx, kubeconfig, source.Namespace, podName, 60*time.Minute, output); err != nil {
		if logs, logErr := d.storageKubectlCapture(ctx, kubeconfig, output, "logs", podName, "-n", source.Namespace, "--tail=200"); logErr == nil {
			_, _ = output.Write(logs)
			if len(logs) > 0 && logs[len(logs)-1] != '\n' {
				_, _ = fmt.Fprintln(output)
			}
		}
		return err
	}
	if logs, err := d.storageKubectlCapture(ctx, kubeconfig, output, "logs", podName, "-n", source.Namespace, "--tail=200"); err == nil {
		_, _ = output.Write(logs)
		if len(logs) > 0 && logs[len(logs)-1] != '\n' {
			_, _ = fmt.Fprintln(output)
		}
	}
	return nil
}

func (d *Deployment) patchManagedWorkloadClaim(ctx context.Context, kubeconfig string, pvc managedPVC, claimName string, output io.Writer) error {
	patch, _ := json.Marshal([]map[string]any{{
		"op": "replace", "path": fmt.Sprintf("/spec/template/spec/volumes/%d/persistentVolumeClaim/claimName", pvc.VolumeIndex), "value": claimName,
	}})
	return d.storageKubectlRun(ctx, kubeconfig, output, "patch", "statefulset", pvc.WorkloadName, "-n", pvc.Namespace, "--type=json", "-p", string(patch))
}

func (d *Deployment) scaleManagedWorkload(ctx context.Context, kubeconfig string, pvc managedPVC, replicas int, output io.Writer) error {
	return d.storageKubectlRun(ctx, kubeconfig, output, "scale", "statefulset", pvc.WorkloadName, "-n", pvc.Namespace, "--replicas", strconv.Itoa(replicas))
}

func (d *Deployment) restoreManagedWorkloads(ctx context.Context, kubeconfig string, pvcs []managedPVC, output io.Writer) {
	for _, pvc := range pvcs {
		_ = d.scaleManagedWorkload(ctx, kubeconfig, pvc, pvc.OriginalReplicas, output)
	}
}

func (d *Deployment) rollbackManagedStorage(ctx context.Context, kubeconfig string, switched []managedMigration, all []managedPVC, output io.Writer) {
	for _, pvc := range all {
		_ = d.scaleManagedWorkload(ctx, kubeconfig, pvc, 0, output)
	}
	for _, item := range switched {
		_ = d.patchManagedWorkloadClaim(ctx, kubeconfig, item.source, item.source.Name, output)
	}
	d.restoreManagedWorkloads(ctx, kubeconfig, all, output)
}

func (d *Deployment) verifyManagedWorkloads(ctx context.Context, kubeconfig string, pvcs []managedPVC, workloads map[string]managedWorkload, output io.Writer) error {
	for _, pvc := range pvcs {
		workload, ok := workloads[pvcWorkloadKey(pvc)]
		if !ok || workload.Replicas == 0 {
			continue
		}
		if err := d.waitForManagedWorkloadReady(ctx, kubeconfig, pvc, 10*time.Minute, output); err != nil {
			return err
		}
	}
	return nil
}

func (d *Deployment) waitForManagedWorkloadReady(ctx context.Context, kubeconfig string, pvc managedPVC, timeout time.Duration, output io.Writer) error {
	return d.storageKubectlRun(ctx, kubeconfig, output, "rollout", "status", "statefulset/"+pvc.WorkloadName, "-n", pvc.Namespace, "--timeout", timeout.String())
}

func (d *Deployment) waitForStatefulSetReplicas(ctx context.Context, kubeconfig, namespace, name string, expected int, timeout time.Duration, output io.Writer) error {
	deadline := time.Now().Add(timeout)
	for {
		payload, err := d.storageKubectlCapture(ctx, kubeconfig, io.Discard, "get", "statefulset", name, "-n", namespace, "-o", "json")
		if err == nil {
			var state struct {
				Status struct {
					Replicas int `json:"replicas"`
				} `json:"status"`
			}
			if json.Unmarshal(payload, &state) == nil && state.Status.Replicas == expected {
				_, _ = fmt.Fprintf(output, "StatefulSet %s/%s 当前副本数已变为 %d。\n", namespace, name, expected)
				return nil
			}
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("等待 StatefulSet %s/%s 缩容到 %d 个副本超时", namespace, name, expected)
		}
		if err := waitStoragePoll(ctx, 3*time.Second); err != nil {
			return err
		}
	}
}

func (d *Deployment) waitForPVCCapacity(ctx context.Context, kubeconfig, namespace, name string, targetBytes int64, timeout time.Duration, output io.Writer) error {
	deadline := time.Now().Add(timeout)
	for {
		payload, err := d.storageKubectlCapture(ctx, kubeconfig, io.Discard, "get", "persistentvolumeclaim", name, "-n", namespace, "-o", "json")
		if err == nil {
			var item struct {
				Spec struct {
					Resources struct {
						Requests map[string]string `json:"requests"`
					} `json:"resources"`
				} `json:"spec"`
				Status struct {
					Capacity map[string]string `json:"capacity"`
				} `json:"status"`
			}
			if json.Unmarshal(payload, &item) == nil {
				requested, _ := kubernetesStorageBytes(item.Spec.Resources.Requests["storage"])
				capacity, _ := kubernetesStorageBytes(item.Status.Capacity["storage"])
				if requested >= targetBytes && capacity >= targetBytes {
					_, _ = fmt.Fprintf(output, "PVC %s/%s 容量已达到 %s。\n", namespace, name, item.Status.Capacity["storage"])
					return nil
				}
			}
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("等待 PVC %s/%s 扩容完成超时；AWS EBS 可能仍在后台处理，可稍后在存储面板刷新状态", namespace, name)
		}
		if err := waitStoragePoll(ctx, 5*time.Second); err != nil {
			return err
		}
	}
}

func (d *Deployment) waitForMigrationPod(ctx context.Context, kubeconfig, namespace, name string, timeout time.Duration, output io.Writer) error {
	deadline := time.Now().Add(timeout)
	for {
		payload, err := d.storageKubectlCapture(ctx, kubeconfig, io.Discard, "get", "pod", name, "-n", namespace, "-o", "json")
		if err == nil {
			var pod struct {
				Status struct {
					Phase   string `json:"phase"`
					Reason  string `json:"reason"`
					Message string `json:"message"`
				} `json:"status"`
			}
			if json.Unmarshal(payload, &pod) == nil {
				switch pod.Status.Phase {
				case "Succeeded":
					return nil
				case "Failed":
					return fmt.Errorf("数据迁移 Pod %s/%s 失败：%s %s", namespace, name, pod.Status.Reason, pod.Status.Message)
				}
			}
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("数据迁移 Pod %s/%s 在 %s 内未完成", namespace, name, timeout)
		}
		if err := waitStoragePoll(ctx, 5*time.Second); err != nil {
			return err
		}
	}
}

func (d *Deployment) storageKubectlCapture(ctx context.Context, kubeconfig string, output io.Writer, args ...string) ([]byte, error) {
	command := Command{Name: d.config.Tools.Kubectl, Args: args, Dir: d.config.Paths.RepositoryRoot, Env: d.commandEnv(ctx, kubeconfig)}
	_, _ = fmt.Fprintf(output, "$ kubectl %s\n", strings.Join(args, " "))
	return d.captureCommand(ctx, command)
}

func (d *Deployment) storageKubectlRun(ctx context.Context, kubeconfig string, output io.Writer, args ...string) error {
	command := Command{Name: d.config.Tools.Kubectl, Args: args, Dir: d.config.Paths.RepositoryRoot, Env: d.commandEnv(ctx, kubeconfig)}
	_, _ = fmt.Fprintf(output, "$ kubectl %s\n", strings.Join(args, " "))
	return d.executor.Run(ctx, command, output)
}

func (d *Deployment) storageKubectlInput(ctx context.Context, kubeconfig string, output io.Writer, payload []byte, args ...string) error {
	command := Command{Name: d.config.Tools.Kubectl, Args: args, Dir: d.config.Paths.RepositoryRoot, Env: d.commandEnv(ctx, kubeconfig), Stdin: bytes.NewReader(payload)}
	_, _ = fmt.Fprintf(output, "$ kubectl %s（清单内容已隐藏）\n", strings.Join(args, " "))
	return d.executor.Run(ctx, command, output)
}

func decodeRunnerManagedStorage(pvcPayload, classPayload, workloadPayload []byte) ([]managedPVC, map[string]managedWorkload, error) {
	var classes struct {
		Items []struct {
			Metadata struct {
				Name string `json:"name"`
			} `json:"metadata"`
			AllowVolumeExpansion bool `json:"allowVolumeExpansion"`
		} `json:"items"`
	}
	if err := json.Unmarshal(classPayload, &classes); err != nil {
		return nil, nil, err
	}
	expansion := make(map[string]bool, len(classes.Items))
	for _, item := range classes.Items {
		expansion[item.Metadata.Name] = item.AllowVolumeExpansion
	}
	var workloadList struct {
		Items []struct {
			Metadata struct {
				Name      string `json:"name"`
				Namespace string `json:"namespace"`
			} `json:"metadata"`
			Spec struct {
				Replicas             *int `json:"replicas"`
				VolumeClaimTemplates []struct {
					Metadata struct {
						Name string `json:"name"`
					} `json:"metadata"`
				} `json:"volumeClaimTemplates"`
				Selector struct {
					MatchLabels map[string]string `json:"matchLabels"`
				} `json:"selector"`
				Template struct {
					Spec struct {
						Volumes []struct {
							Name                  string `json:"name"`
							PersistentVolumeClaim *struct {
								ClaimName string `json:"claimName"`
							} `json:"persistentVolumeClaim"`
						} `json:"volumes"`
					} `json:"spec"`
				} `json:"template"`
			} `json:"spec"`
			Status struct {
				ReadyReplicas int `json:"readyReplicas"`
			} `json:"status"`
		} `json:"items"`
	}
	if err := json.Unmarshal(workloadPayload, &workloadList); err != nil {
		return nil, nil, err
	}
	workloads := make(map[string]managedWorkload, len(workloadList.Items))
	activeClaims := make(map[string]bool)
	for _, item := range workloadList.Items {
		replicas := 1
		if item.Spec.Replicas != nil {
			replicas = *item.Spec.Replicas
		}
		workload := managedWorkload{
			Name: item.Metadata.Name, Namespace: item.Metadata.Namespace,
			Replicas: replicas, Ready: item.Status.ReadyReplicas, MatchLabel: item.Spec.Selector.MatchLabels,
		}
		for _, claimTemplate := range item.Spec.VolumeClaimTemplates {
			for ordinal := 0; ordinal < replicas; ordinal++ {
				claimName := fmt.Sprintf("%s-%s-%d", claimTemplate.Metadata.Name, item.Metadata.Name, ordinal)
				activeClaims[item.Metadata.Namespace+"\x00"+claimName] = true
			}
		}
		for _, volume := range item.Spec.Template.Spec.Volumes {
			if volume.PersistentVolumeClaim == nil {
				continue
			}
			workload.Volumes = append(workload.Volumes, managedWorkloadVolume{Name: volume.Name, ClaimName: volume.PersistentVolumeClaim.ClaimName})
			activeClaims[item.Metadata.Namespace+"\x00"+volume.PersistentVolumeClaim.ClaimName] = true
		}
		workloads[item.Metadata.Namespace+"\x00"+item.Metadata.Name] = workload
	}
	var pvcList struct {
		Items []struct {
			Metadata struct {
				Name      string            `json:"name"`
				Namespace string            `json:"namespace"`
				Labels    map[string]string `json:"labels"`
			} `json:"metadata"`
			Spec struct {
				StorageClassName string   `json:"storageClassName"`
				AccessModes      []string `json:"accessModes"`
				VolumeMode       string   `json:"volumeMode"`
				Resources        struct {
					Requests map[string]string `json:"requests"`
				} `json:"resources"`
			} `json:"spec"`
			Status struct {
				Capacity map[string]string `json:"capacity"`
			} `json:"status"`
		} `json:"items"`
	}
	if err := json.Unmarshal(pvcPayload, &pvcList); err != nil {
		return nil, nil, err
	}
	pvcs := make([]managedPVC, 0, len(pvcList.Items))
	for _, item := range pvcList.Items {
		labels := item.Metadata.Labels
		pvc := managedPVC{
			Name: item.Metadata.Name, Namespace: item.Metadata.Namespace,
			Component: labels["ops-deploy.io/component"], Project: labels["ops-deploy.io/project"], Environment: labels["ops-deploy.io/environment"],
			StorageClass: item.Spec.StorageClassName, Requested: item.Spec.Resources.Requests["storage"], Capacity: item.Status.Capacity["storage"],
			AccessModes: append([]string(nil), item.Spec.AccessModes...), VolumeMode: item.Spec.VolumeMode,
			WorkloadKind: labels["ops-deploy.io/workload-kind"], WorkloadName: labels["ops-deploy.io/workload-name"], VolumeName: labels["ops-deploy.io/volume-name"],
			Active: activeClaims[item.Metadata.Namespace+"\x00"+item.Metadata.Name], AllowExpansion: expansion[item.Spec.StorageClassName],
			VolumeIndex: -1,
		}
		if workload, ok := workloads[pvcWorkloadKey(pvc)]; ok {
			pvc.OriginalReplicas = workload.Replicas
			for index, volume := range workload.Volumes {
				if volume.Name == pvc.VolumeName && volume.ClaimName == pvc.Name {
					pvc.VolumeIndex = index
				}
			}
		}
		pvcs = append(pvcs, pvc)
	}
	return pvcs, workloads, nil
}

func managedStorageSizePath(component string) string {
	if component == "opentelemetry_collector" {
		// Keep the immutable StatefulSet claim template at initialSize. The
		// expandedSize is reconciled onto every live/new replica PVC after Helm
		// updates, so later scale-out replicas are expanded automatically.
		return "components.catalog.opentelemetry_collector.values.storage.expandedSize"
	}
	if component == "otel-elasticsearch" {
		return "components.catalog.opentelemetry_collector.values.elasticsearch.storage.expandedSize"
	}
	return "components.catalog.clickvisual_stack.values." + component + ".storage.size"
}

func setClickVisualActiveClaim(doc environment.Document, component, workloadName, claim string) {
	if component != "kafka" {
		environment.SetPath(doc, "components.catalog.clickvisual_stack.values."+component+".storage.activeClaim", claim)
		return
	}
	replicas := 1
	if raw, ok := environment.GetPath(doc, "components.catalog.clickvisual_stack.values.kafka.replicas"); ok {
		switch value := raw.(type) {
		case int:
			replicas = value
		case float64:
			replicas = int(value)
		}
	}
	claims := make([]any, replicas)
	if raw, ok := environment.GetPath(doc, "components.catalog.clickvisual_stack.values.kafka.storage.activeClaims"); ok {
		if existing, valid := raw.([]any); valid {
			copy(claims, existing)
		}
	}
	for index := range claims {
		if value, valid := claims[index].(string); !valid || strings.TrimSpace(value) == "" {
			claims[index] = fmt.Sprintf("clickvisual-kafka-data-%d", index)
		}
	}
	index, err := strconv.Atoi(strings.TrimPrefix(workloadName, "clickvisual-kafka-"))
	if err == nil && index >= 0 && index < len(claims) {
		claims[index] = claim
	}
	environment.SetPath(doc, "components.catalog.clickvisual_stack.values.kafka.storage.activeClaims", claims)
}

func cloneEnvironmentDocument(doc environment.Document) (environment.Document, error) {
	payload, err := json.Marshal(doc)
	if err != nil {
		return nil, err
	}
	var cloned environment.Document
	if err := json.Unmarshal(payload, &cloned); err != nil {
		return nil, err
	}
	return cloned, nil
}

func kubernetesStorageBytes(value string) (int64, bool) {
	value = strings.TrimSpace(value)
	units := []struct {
		suffix string
		factor int64
	}{
		{"Ti", 1024 * 1024 * 1024 * 1024},
		{"Gi", 1024 * 1024 * 1024},
		{"Mi", 1024 * 1024},
		{"Ki", 1024},
	}
	for _, unit := range units {
		if !strings.HasSuffix(value, unit.suffix) {
			continue
		}
		number := strings.TrimSpace(strings.TrimSuffix(value, unit.suffix))
		size, err := strconv.ParseInt(number, 10, 64)
		return size * unit.factor, err == nil && size > 0
	}
	size, err := strconv.ParseInt(value, 10, 64)
	return size, err == nil && size > 0
}

func storageReplacementPVCName(source, suffix string) string {
	maxSource := 63 - len("-resize-") - len(suffix)
	if maxSource < 1 {
		maxSource = 1
	}
	source = strings.Trim(source, "-")
	if len(source) > maxSource {
		source = strings.TrimRight(source[:maxSource], "-")
	}
	return source + "-resize-" + suffix
}

func storageMigrationPodName(component, suffix string) string {
	name := "storage-copy-" + component + "-" + suffix
	if len(name) > 63 {
		name = strings.TrimRight(name[:63], "-")
	}
	return name
}

func storageJobSuffix(jobID string, index int) string {
	value := strings.ToLower(strings.TrimSpace(jobID))
	value = strings.ReplaceAll(value, "_", "-")
	if len(value) > 8 {
		value = value[len(value)-8:]
	}
	return fmt.Sprintf("%s-%d", strings.Trim(value, "-"), index)
}

func pvcWorkloadKey(pvc managedPVC) string {
	return pvc.Namespace + "\x00" + pvc.WorkloadName
}

func waitStoragePoll(ctx context.Context, interval time.Duration) error {
	timer := time.NewTimer(interval)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

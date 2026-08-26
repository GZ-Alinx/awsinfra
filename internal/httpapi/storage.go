package httpapi

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"ops-deploy-platform/internal/auth"
	"ops-deploy-platform/internal/environment"
	"ops-deploy-platform/internal/jobs"
)

func (s *Server) listClickVisualStackStorage(w http.ResponseWriter, r *http.Request) {
	project, environmentKey := r.PathValue("project"), r.PathValue("environment")
	if _, err := s.requireProjectView(r, project); err != nil {
		writeAccessError(w, err)
		return
	}
	if err := s.requireBoundProjectAWSCredential(r.Context(), project); err != nil {
		writeProjectAWSCredentialError(w, err)
		return
	}
	if s.status == nil {
		writeError(w, http.StatusServiceUnavailable, errors.New("EKS 状态服务不可用"))
		return
	}
	item, err := s.accessControl.Environment(r.Context(), project, environmentKey)
	if err != nil {
		writeAccessError(w, err)
		return
	}
	doc, err := s.environments.Load(item.TargetName)
	if err != nil {
		writeError(w, http.StatusNotFound, errors.New("环境部署配置不存在"))
		return
	}
	if !environmentBoolean(doc, "components.catalog.clickvisual_stack.enabled") {
		writeError(w, http.StatusConflict, errors.New("当前环境尚未启用 ClickVisual 日志平台"))
		return
	}
	ctx, cancel := contextWithTimeout(r, 60*time.Second)
	defer cancel()
	report, err := s.status.ListClickVisualStorage(ctx, item.TargetName)
	if err != nil {
		writeError(w, http.StatusFailedDependency, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, map[string]any{
		"project": project, "environment": environmentKey,
		"observed_at": report.ObservedAt, "items": report.Items,
	})
}

func (s *Server) listOpenTelemetryCollectorStorage(w http.ResponseWriter, r *http.Request) {
	project, environmentKey := r.PathValue("project"), r.PathValue("environment")
	if _, err := s.requireProjectView(r, project); err != nil {
		writeAccessError(w, err)
		return
	}
	if err := s.requireBoundProjectAWSCredential(r.Context(), project); err != nil {
		writeProjectAWSCredentialError(w, err)
		return
	}
	if s.status == nil {
		writeError(w, http.StatusServiceUnavailable, errors.New("EKS 状态服务不可用"))
		return
	}
	item, err := s.accessControl.Environment(r.Context(), project, environmentKey)
	if err != nil {
		writeAccessError(w, err)
		return
	}
	doc, err := s.environments.Load(item.TargetName)
	if err != nil {
		writeError(w, http.StatusNotFound, errors.New("环境部署配置不存在"))
		return
	}
	if !environmentBoolean(doc, "components.catalog.opentelemetry_collector.enabled") {
		writeError(w, http.StatusConflict, errors.New("当前环境尚未启用 OpenTelemetry Collector"))
		return
	}
	ctx, cancel := contextWithTimeout(r, 60*time.Second)
	defer cancel()
	report, err := s.status.ListOpenTelemetryStorage(ctx, item.TargetName)
	if err != nil {
		writeError(w, http.StatusFailedDependency, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, map[string]any{
		"project": project, "environment": environmentKey,
		"observed_at": report.ObservedAt, "items": report.Items,
	})
}

func (s *Server) expandOpenTelemetryCollectorStorage(w http.ResponseWriter, r *http.Request) {
	project, environmentKey := r.PathValue("project"), r.PathValue("environment")
	if err := s.requireProjectDeploy(r, project); err != nil {
		writeAccessError(w, err)
		return
	}
	if err := s.requireBoundProjectAWSCredential(r.Context(), project); err != nil {
		writeProjectAWSCredentialError(w, err)
		return
	}
	item, err := s.accessControl.Environment(r.Context(), project, environmentKey)
	if err != nil {
		writeAccessError(w, err)
		return
	}
	doc, err := s.environments.Load(item.TargetName)
	if err != nil {
		writeError(w, http.StatusNotFound, errors.New("环境部署配置不存在"))
		return
	}
	if !environmentBoolean(doc, "components.catalog.opentelemetry_collector.enabled") {
		writeError(w, http.StatusConflict, errors.New("当前环境尚未启用 OpenTelemetry Collector"))
		return
	}
	var request struct {
		Component     string `json:"component"`
		Operation     string `json:"operation"`
		TargetSizeGi  int    `json:"target_size_gib"`
		SafetyPercent int    `json:"safety_percent"`
		Confirm       string `json:"confirm"`
	}
	if err := decodeJSON(w, r, &request); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	request.Component = strings.ToLower(strings.TrimSpace(request.Component))
	request.Operation = strings.ToLower(strings.TrimSpace(request.Operation))
	if (request.Component != "opentelemetry_collector" && request.Component != "otel-elasticsearch") || request.Operation != "expand" {
		writeError(w, http.StatusBadRequest, errors.New("OpenTelemetry 持久化存储只支持 Collector WAL 或专用 Elasticsearch 在线扩容"))
		return
	}
	if request.Component == "otel-elasticsearch" && !environmentBoolean(doc, "components.catalog.opentelemetry_collector.values.elasticsearch.enabled") {
		writeError(w, http.StatusConflict, errors.New("当前环境尚未启用 OpenTelemetry 专用 Elasticsearch"))
		return
	}
	minimumGi := 1
	displayName := "OpenTelemetry Collector WAL"
	if request.Component == "otel-elasticsearch" {
		minimumGi = 10
		displayName = "OpenTelemetry Elasticsearch"
	}
	if request.TargetSizeGi < minimumGi || request.TargetSizeGi > 16384 {
		writeError(w, http.StatusBadRequest, fmt.Errorf("目标容量必须在 %d GiB 到 16384 GiB 之间", minimumGi))
		return
	}
	if request.Confirm != project+"/"+environmentKey+":"+request.Component+":expand" {
		writeError(w, http.StatusBadRequest, errors.New("存储操作确认内容不匹配"))
		return
	}
	if s.status == nil {
		writeError(w, http.StatusServiceUnavailable, errors.New("EKS 状态服务不可用"))
		return
	}
	ctx, cancel := contextWithTimeout(r, 60*time.Second)
	defer cancel()
	report, err := s.status.ListOpenTelemetryStorage(ctx, item.TargetName)
	if err != nil {
		writeError(w, http.StatusFailedDependency, err)
		return
	}
	activeCount, pendingCount := 0, 0
	for _, storage := range report.Items {
		if storage.Component != request.Component || !storage.Active {
			continue
		}
		activeCount++
		requested, valid := storageQuantityGi(storage.Requested)
		if !valid {
			writeError(w, http.StatusConflict, fmt.Errorf("当前 %s PVC 请求容量无法识别，请刷新后重试", displayName))
			return
		}
		if !storage.AllowExpansion {
			writeError(w, http.StatusConflict, errors.New("当前 StorageClass 不支持在线扩容"))
			return
		}
		if request.TargetSizeGi < requested {
			writeError(w, http.StatusConflict, errors.New("扩容目标不能小于当前 PVC 请求容量"))
			return
		}
		capacity, capacityValid := storageQuantityGi(storage.Capacity)
		if request.TargetSizeGi > requested || !capacityValid || capacity < request.TargetSizeGi {
			pendingCount++
		}
	}
	if activeCount == 0 {
		writeError(w, http.StatusConflict, fmt.Errorf("未发现 %s 正在使用的 PVC，请先完成组件部署", displayName))
		return
	}
	if pendingCount == 0 {
		writeError(w, http.StatusConflict, fmt.Errorf("全部 %s PVC 已达到目标容量，无需重复执行", displayName))
		return
	}
	session, _ := auth.SessionFromContext(r.Context())
	job, err := s.jobs.SubmitWithParameters(project, environmentKey, item.TargetName, session.Username, jobs.ActionStorageExpand, map[string]string{
		"component":      request.Component,
		"target_size_gi": strconv.Itoa(request.TargetSizeGi),
		"safety_percent": "30",
	})
	if err != nil {
		writeJobSubmitError(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, job)
}

func (s *Server) resizeClickVisualStackStorage(w http.ResponseWriter, r *http.Request) {
	project, environmentKey := r.PathValue("project"), r.PathValue("environment")
	if err := s.requireProjectDeploy(r, project); err != nil {
		writeAccessError(w, err)
		return
	}
	if err := s.requireBoundProjectAWSCredential(r.Context(), project); err != nil {
		writeProjectAWSCredentialError(w, err)
		return
	}
	item, err := s.accessControl.Environment(r.Context(), project, environmentKey)
	if err != nil {
		writeAccessError(w, err)
		return
	}
	doc, err := s.environments.Load(item.TargetName)
	if err != nil {
		writeError(w, http.StatusNotFound, errors.New("环境部署配置不存在"))
		return
	}
	if !environmentBoolean(doc, "components.catalog.clickvisual_stack.enabled") {
		writeError(w, http.StatusConflict, errors.New("当前环境尚未启用 ClickVisual 日志平台"))
		return
	}
	var request struct {
		Component     string `json:"component"`
		Operation     string `json:"operation"`
		TargetSizeGi  int    `json:"target_size_gib"`
		SafetyPercent int    `json:"safety_percent"`
		Confirm       string `json:"confirm"`
	}
	if err := decodeJSON(w, r, &request); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	request.Component = strings.ToLower(strings.TrimSpace(request.Component))
	request.Operation = strings.ToLower(strings.TrimSpace(request.Operation))
	if request.Component != "kafka" && request.Component != "clickhouse" && request.Component != "mysql" {
		writeError(w, http.StatusBadRequest, errors.New("只支持 Kafka、ClickHouse 或 MySQL 存储操作"))
		return
	}
	if request.Operation != "expand" && request.Operation != "shrink" {
		writeError(w, http.StatusBadRequest, errors.New("存储操作必须为扩容或缩容"))
		return
	}
	if request.TargetSizeGi < 1 || request.TargetSizeGi > 16384 {
		writeError(w, http.StatusBadRequest, errors.New("目标容量必须在 1 GiB 到 16384 GiB 之间"))
		return
	}
	if request.SafetyPercent == 0 {
		request.SafetyPercent = 30
	}
	if request.SafetyPercent < 10 || request.SafetyPercent > 100 {
		writeError(w, http.StatusBadRequest, errors.New("缩容安全余量必须在 10% 到 100% 之间"))
		return
	}
	requiredConfirmation := project + "/" + environmentKey + ":" + request.Component + ":" + request.Operation
	if request.Confirm != requiredConfirmation {
		writeError(w, http.StatusBadRequest, errors.New("存储操作确认内容不匹配"))
		return
	}
	if s.status == nil {
		writeError(w, http.StatusServiceUnavailable, errors.New("EKS 状态服务不可用"))
		return
	}
	ctx, cancel := contextWithTimeout(r, 60*time.Second)
	defer cancel()
	report, err := s.status.ListClickVisualStorage(ctx, item.TargetName)
	if err != nil {
		writeError(w, http.StatusFailedDependency, err)
		return
	}
	activeCount := 0
	pendingCount := 0
	for _, storage := range report.Items {
		if storage.Component != request.Component || !storage.Active {
			continue
		}
		activeCount++
		requested, requestedValid := storageQuantityGi(storage.Requested)
		capacity, capacityValid := storageQuantityGi(storage.Capacity)
		if !requestedValid {
			writeError(w, http.StatusConflict, errors.New("当前 PVC 请求容量无法识别，请刷新存储状态后重试"))
			return
		}
		if request.Operation == "expand" && !storage.AllowExpansion {
			writeError(w, http.StatusConflict, errors.New("当前 StorageClass 不支持在线扩容，请更换允许扩容的存储类"))
			return
		}
		if request.Operation == "expand" {
			if request.TargetSizeGi < requested {
				writeError(w, http.StatusConflict, errors.New("扩容目标不能小于任何活动 PVC 的当前请求容量"))
				return
			}
			if request.TargetSizeGi > requested || !capacityValid || capacity < request.TargetSizeGi {
				pendingCount++
			}
			continue
		}
		if request.TargetSizeGi > requested {
			writeError(w, http.StatusConflict, errors.New("缩容目标不能大于任何活动 PVC 的当前容量"))
			return
		}
		if request.TargetSizeGi < requested {
			pendingCount++
		}
	}
	if activeCount == 0 {
		writeError(w, http.StatusConflict, errors.New("未发现该子组件正在使用的 PVC，请先成功部署日志平台"))
		return
	}
	if pendingCount == 0 {
		writeError(w, http.StatusConflict, errors.New("全部活动 PVC 已达到目标容量，无需重复执行"))
		return
	}
	action := jobs.ActionStorageExpand
	if request.Operation == "shrink" {
		action = jobs.ActionStorageShrink
	}
	session, _ := auth.SessionFromContext(r.Context())
	job, err := s.jobs.SubmitWithParameters(project, environmentKey, item.TargetName, session.Username, action, map[string]string{
		"component":      request.Component,
		"target_size_gi": strconv.Itoa(request.TargetSizeGi),
		"safety_percent": strconv.Itoa(request.SafetyPercent),
	})
	if err != nil {
		writeJobSubmitError(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, job)
}

func environmentBoolean(doc environment.Document, path string) bool {
	value, ok := environment.GetPath(doc, path)
	result, valid := value.(bool)
	return ok && valid && result
}

func storageQuantityGi(value string) (int, bool) {
	value = strings.TrimSpace(value)
	multiplier := 1
	switch {
	case strings.HasSuffix(value, "Gi"):
		value = strings.TrimSuffix(value, "Gi")
	case strings.HasSuffix(value, "Ti"):
		value = strings.TrimSuffix(value, "Ti")
		multiplier = 1024
	default:
		return 0, false
	}
	size, err := strconv.Atoi(value)
	return size * multiplier, err == nil && size > 0
}

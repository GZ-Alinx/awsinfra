package httpapi

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"strings"
	"text/template"
	"time"

	"github.com/GZ-Alinx/awsinfra/internal/alertingrelay"
	"github.com/GZ-Alinx/awsinfra/internal/environment"
)

const maxAlertTestResponseBytes = 64 * 1024
const maxAlertmanagerPayloadBytes = 1 << 20

// coreAlertNames is intentionally an allowlist. The upstream monitoring chart
// includes many informational, recording-rule and target-discovery alerts that
// are useful in Prometheus but too noisy as human notifications. Keep the
// metrics and rules available for diagnostics while sending only actionable
// resource, workload, service and monitoring-chain health events by default.
var coreAlertNames = map[string]struct{}{
	// EKS API, nodes and kubelet.
	"kubeapidown": {}, "kubeaggregatedapidown": {}, "kubeapierrorshigh": {}, "kubeapilatencyhigh": {},
	"kubeproxydown": {}, "corednsdown": {}, "corednserrorshigh": {}, "corednslatencyhigh": {},
	"kubenodenotready": {}, "kubenodeunreachable": {},
	"kubenodepressure": {}, "kubenodereadinessflapping": {}, "kubenodeeviction": {},
	"kubeletdown": {}, "kubelettoomanypods": {}, "kubeletpodstartuplatencyhigh": {},
	// Kubernetes workloads.
	"kubecontainerwaiting": {}, "kubepodcrashlooping": {}, "kubepodnotready": {}, "kubepodoomkilled": {},
	"kubedeploymentgenerationmismatch": {}, "kubedeploymentreplicasmismatch": {}, "kubedeploymentrolloutstuck": {},
	"kubestatefulsetgenerationmismatch": {}, "kubestatefulsetreplicasmismatch": {}, "kubestatefulsetupdatenotrolledout": {},
	"kubedaemonsetmisscheduled": {}, "kubedaemonsetnotscheduled": {}, "kubedaemonsetrolloutstuck": {},
	"kubejobfailed": {}, "kubejobnotcompleted": {}, "kubepdbnotenoughhealthypods": {},
	"kubehpamaxedout": {}, "kubehpareplicasmismatch": {},
	// Persistent storage and node capacity.
	"kubepersistentvolumeerrors": {}, "kubepersistentvolumefillingup": {}, "kubepersistentvolumeinodesfillingup": {},
	"nodecpuhighusage": {}, "nodecpuusagehigh": {}, "nodememoryhighutilization": {},
	"nodediskiosaturation": {}, "nodesystemsaturation": {}, "nodefiledescriptorlimit": {},
	"nodefilesystemalmostoutoffiles": {}, "nodefilesystemalmostoutofspace": {},
	"nodefilesystemfilesfillingup": {}, "nodefilesystemspacefillingup": {},
	"nodenetworkreceiveerrs": {}, "nodenetworktransmiterrs": {},
	"noderaiddegraded": {}, "noderaiddiskfailure": {},
	// Monitoring-chain health: without these, all other alerts can silently stop.
	"alertmanagerclusterdown": {}, "alertmanagerclustercrashlooping": {}, "alertmanagerfailedreload": {}, "alertmanagermembersinconsistent": {},
	"prometheusbadconfig": {}, "prometheusnotconnectedtoalertmanagers": {}, "prometheusnotingestingsamples": {},
	"prometheusrulefailures": {}, "prometheustsdbcompactionsfailing": {}, "prometheustsdbreloadsfailing": {},
	"prometheusoperatornotready": {}, "prometheusoperatorrejectedresources": {},
	"kubestatemetricsdown": {}, "nodeexporterdown": {},
	// Common service and certificate probes outside kube-prometheus-stack.
	"blackboxprobefailed": {}, "httpprobefailed": {}, "probefailed": {},
	"serviceunavailable": {}, "serviceavailabilityfailed": {}, "tlscertificateexpiringsoon": {}, "probesslcertexpiry": {},
	// Platform-native deployment and data-service health events.
	"deploymentfailed": {}, "databaseconnectionfailed": {}, "mysqldown": {}, "postgresdown": {},
	"postgresqldown": {}, "redisdown": {}, "mongodbdown": {}, "rabbitmqdown": {},
}

var coreAlertEventTypes = map[string]struct{}{
	"deployment":           {},
	"database":             {},
	"service-availability": {},
	"kubernetes-workload":  {},
	"node-resource":        {},
}

type alertChannel struct {
	Name      string
	Type      string
	Address   string
	SecretRef string
}

type alertMessage struct {
	Title     string
	Markdown  string
	Status    string
	Severity  string
	EventType string
}

func (m alertMessage) plainText() string {
	return strings.TrimSpace(strings.TrimSpace(m.Title) + "\n\n" + strings.TrimSpace(m.Markdown))
}

func (s *Server) testEnvironmentAlertChannel(w http.ResponseWriter, r *http.Request) {
	projectKey, environmentKey := r.PathValue("project"), r.PathValue("environment")
	if err := s.requireProjectConfigure(r, projectKey); err != nil {
		writeAccessError(w, err)
		return
	}
	item, err := s.accessControl.Environment(r.Context(), projectKey, environmentKey)
	if err != nil {
		writeAccessError(w, err)
		return
	}
	doc, err := s.environments.Load(item.TargetName)
	if err != nil {
		writeError(w, http.StatusNotFound, err)
		return
	}
	doc = environment.ApplyDefaults(doc, projectKey, environmentKey)
	channel, err := findAlertChannel(doc, r.PathValue("channel"))
	if err != nil {
		writeError(w, http.StatusNotFound, err)
		return
	}
	if channel.SecretRef != "" && channel.Address == "" {
		writeError(w, http.StatusBadRequest, errors.New("该通道仅配置了凭据引用，测试发送暂不支持从外部 Secret 读取地址"))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	provider, err := sendAlertChannelTest(ctx, channel, projectKey, environmentKey)
	if err != nil {
		writeError(w, http.StatusFailedDependency, fmt.Errorf("告警通道测试发送失败：%w", err))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"status": "sent", "channel": channel.Name, "type": provider,
		"message": "测试消息已发送，请在目标群组或通道中确认接收结果",
	})
}

func (s *Server) testEnvironmentAlertScenario(w http.ResponseWriter, r *http.Request) {
	projectKey, environmentKey := r.PathValue("project"), r.PathValue("environment")
	if err := s.requireProjectConfigure(r, projectKey); err != nil {
		writeAccessError(w, err)
		return
	}
	item, err := s.accessControl.Environment(r.Context(), projectKey, environmentKey)
	if err != nil {
		writeAccessError(w, err)
		return
	}
	doc, err := s.environments.Load(item.TargetName)
	if err != nil {
		writeError(w, http.StatusNotFound, err)
		return
	}
	doc = environment.ApplyDefaults(doc, projectKey, environmentKey)
	alerting, _ := doc["alerting"].(map[string]any)
	channels := configuredAlertChannels(alerting)
	if len(channels) == 0 {
		writeError(w, http.StatusBadRequest, errors.New("请先保存至少一个告警通道，再测试告警场景"))
		return
	}
	scenario := strings.TrimSpace(r.PathValue("scenario"))
	payload, err := alertingrelay.SamplePayload(scenario, projectKey, environmentKey, time.Now())
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	message, templateName, err := renderConfiguredAlertMessage(alerting, payload, projectKey, environmentKey, environment.ClusterName(doc))
	if err != nil {
		writeError(w, http.StatusBadRequest, fmt.Errorf("渲染告警模板失败：%w", err))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 45*time.Second)
	defer cancel()
	delivered, failed := deliverAlertMessage(ctx, item.TargetName, channels, message)
	if len(delivered) == 0 {
		writeError(w, http.StatusFailedDependency, fmt.Errorf("%s 场景测试发送失败，%d 个通道均未送达", scenario, len(failed)))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"status": "sent", "scenario": scenario, "template": templateName,
		"delivered": delivered, "failed": failed,
	})
}

// relayAlertmanager is intentionally outside browser session authentication:
// Alertmanager runs in a target EKS cluster and authenticates with a derived,
// environment-scoped bearer token. The regular auth middleware only bypasses
// this exact route prefix; token verification remains mandatory here.
func (s *Server) relayAlertmanager(w http.ResponseWriter, r *http.Request) {
	target := strings.TrimSpace(r.PathValue("target"))
	if !environment.ValidName(target) {
		writeError(w, http.StatusNotFound, errors.New("alert relay target not found"))
		return
	}
	provided := strings.TrimSpace(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
	if provided == "" || !alertingrelay.VerifyToken(s.config.CredentialKey(), target, provided) {
		writeError(w, http.StatusUnauthorized, errors.New("invalid alert relay credentials"))
		return
	}
	doc, err := s.environments.Load(target)
	if err != nil {
		writeError(w, http.StatusNotFound, errors.New("alert relay target not found"))
		return
	}
	project, environmentKey := documentString(doc, "project"), documentString(doc, "environment")
	doc = environment.ApplyDefaults(doc, project, environmentKey)
	alerting, ok := doc["alerting"].(map[string]any)
	if !ok || !documentBoolValue(alerting["enabled"]) {
		writeError(w, http.StatusGone, errors.New("alert center is disabled for this environment"))
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxAlertmanagerPayloadBytes)
	defer r.Body.Close()
	var payload alertingrelay.Payload
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		writeError(w, http.StatusBadRequest, errors.New("invalid Alertmanager payload"))
		return
	}
	if len(payload.Alerts) == 0 {
		writeError(w, http.StatusBadRequest, errors.New("Alertmanager payload contains no alerts"))
		return
	}
	payload, suppressed := filterAlertPayloadForDelivery(alerting, payload)
	if len(payload.Alerts) == 0 {
		log.Printf("alert delivery suppressed: target=%s policy=core non_core=%d", target, suppressed)
		writeJSON(w, http.StatusAccepted, map[string]any{
			"status": "suppressed", "reason": "non_core_alert", "suppressed": suppressed,
		})
		return
	}
	message, _, err := renderConfiguredAlertMessage(alerting, payload, project, environmentKey, environment.ClusterName(doc))
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	channels := configuredAlertChannels(alerting)
	if len(channels) == 0 {
		writeError(w, http.StatusGone, errors.New("no alert channels are configured for this environment"))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 45*time.Second)
	defer cancel()
	delivered, failed := deliverAlertMessage(ctx, target, channels, message)
	if len(delivered) == 0 {
		writeError(w, http.StatusBadGateway, fmt.Errorf("all %d alert channel deliveries failed", len(failed)))
		return
	}
	status := "delivered"
	if len(failed) > 0 {
		status = "partially_delivered"
	}
	response := map[string]any{"status": status, "delivered": delivered, "failed": failed}
	if suppressed > 0 {
		response["suppressed"] = suppressed
	}
	writeJSON(w, http.StatusOK, response)
}

func filterAlertPayloadForDelivery(alerting map[string]any, payload alertingrelay.Payload) (alertingrelay.Payload, int) {
	policy := strings.ToLower(strings.TrimSpace(documentStringValue(alerting["delivery_policy"])))
	if policy == "all" {
		return payload, 0
	}
	filtered := payload
	filtered.Alerts = make([]alertingrelay.Alert, 0, len(payload.Alerts))
	for _, alert := range payload.Alerts {
		if isCoreAlert(alert) {
			filtered.Alerts = append(filtered.Alerts, alert)
		}
	}
	if len(filtered.Alerts) > 0 {
		filtered.Status = "resolved"
		for _, alert := range filtered.Alerts {
			alertStatus := strings.TrimSpace(alert.Status)
			if alertStatus == "" {
				alertStatus = payload.Status
			}
			if strings.EqualFold(alertStatus, "firing") {
				filtered.Status = "firing"
				break
			}
		}
	}
	return filtered, len(payload.Alerts) - len(filtered.Alerts)
}

func isCoreAlert(alert alertingrelay.Alert) bool {
	if strings.EqualFold(strings.TrimSpace(alert.Labels["ops_deploy_core"]), "true") ||
		strings.EqualFold(strings.TrimSpace(alert.Labels["notification_class"]), "core") {
		return true
	}
	if _, ok := coreAlertEventTypes[strings.ToLower(strings.TrimSpace(alert.Labels["event_type"]))]; ok {
		return true
	}
	_, ok := coreAlertNames[strings.ToLower(strings.TrimSpace(alert.Labels["alertname"]))]
	return ok
}

func deliverAlertMessage(ctx context.Context, target string, channels []alertChannel, message alertMessage) ([]string, []string) {
	delivered, failed := make([]string, 0, len(channels)), make([]string, 0)
	for _, channel := range channels {
		if channel.Address == "" {
			failed = append(failed, channel.Name)
			continue
		}
		if _, err := sendAlertChannelMessage(ctx, channel, message); err != nil {
			failed = append(failed, channel.Name)
			log.Printf("alert delivery failed: target=%s channel=%s type=%s error=%v", target, channel.Name, channel.Type, err)
			continue
		}
		delivered = append(delivered, channel.Name)
	}
	return delivered, failed
}

type alertTemplateData struct {
	Status            string
	StatusText        string
	StatusIcon        string
	Severity          string
	SeverityText      string
	Project           string
	Environment       string
	EventType         string
	CommonLabels      map[string]string
	CommonAnnotations map[string]string
	StartsAt          string
	AlertCount        int
	AlertName         string
	AlertNameText     string
	Cluster           string
	Namespace         string
	Workload          string
	Pod               string
	Container         string
	Node              string
	Service           string
	MonitorTarget     string
	Instance          string
	Stage             string
	JobID             string
	Engine            string
	HTTPStatus        string
	CurrentValue      string
	Threshold         string
	Duration          string
	Availability      string
	Summary           string
	Description       string
	OriginalMessage   string
	Advice            string
	RunbookURL        string
	RelatedAlerts     string
}

type localizedPrometheusAlert struct {
	Name        string
	Summary     string
	Description string
	Advice      string
}

var localizedPrometheusAlerts = map[string]localizedPrometheusAlert{
	"kubecontrollermanagerdown": {
		Name:        "Kubernetes 控制器管理器监控不可达",
		Summary:     "Prometheus 已无法发现 Kubernetes 控制器管理器的指标目标",
		Description: "controller-manager 监控目标已从 Prometheus 服务发现中消失。在 EKS 中该组件由 AWS 管理且默认不对租户集群暴露指标端点，不代表业务 Pod 或 EKS 集群已经故障。",
		Advice:      "先在 AWS EKS 和平台环境概览确认控制平面为 ACTIVE。若集群正常，应关闭 EKS 不可采集的 controller-manager 监控规则，而不是排查业务 Pod。",
	},
	"kubeschedulerdown": {
		Name:        "Kubernetes 调度器监控不可达",
		Summary:     "Prometheus 已无法发现 Kubernetes 调度器的指标目标",
		Description: "scheduler 监控目标已从 Prometheus 服务发现中消失。EKS 调度器由 AWS 管理，默认不暴露可由租户 Prometheus 采集的控制面指标端点。",
		Advice:      "先确认 EKS 控制平面为 ACTIVE 且 Pod 调度正常；若正常，关闭 EKS 不可采集的 scheduler 监控规则。",
	},
	"kubeetcddown": {
		Name:        "Kubernetes etcd 监控不可达",
		Summary:     "Prometheus 已无法发现 EKS 控制面 etcd 指标目标",
		Description: "EKS 控制面 etcd 由 AWS 全托管，不会向租户集群暴露可采集的 etcd 指标端点。",
		Advice:      "在 AWS EKS 确认集群状态与 API Server 可用性；正常时关闭 EKS 不可采集的 etcd 监控规则。",
	},
	"kubeapidown": {
		Name:        "Kubernetes API Server 监控不可达",
		Summary:     "Prometheus 无法采集 Kubernetes API Server 指标",
		Description: "API Server 指标目标不可达，可能是 EKS 控制平面、集群网络、RBAC 或 ServiceMonitor 配置异常。",
		Advice:      "确认 EKS 状态、kubectl 连接、API Endpoint 访问策略、Prometheus ServiceAccount 权限和 ServiceMonitor 状态。",
	},
	"kubepodcrashlooping": {
		Name:        "Pod 持续重启",
		Summary:     "Pod 正在反复进入 CrashLoopBackOff",
		Description: "容器连续启动失败，已超过 Prometheus 告警规则设定的持续时间。",
		Advice:      "查看 Pod 事件、当前日志和上一次容器日志，检查启动参数、Secret/ConfigMap、健康检查及依赖服务。",
	},
	"kubepodnotready": {
		Name:        "Pod 长时间未就绪",
		Summary:     "Pod 持续处于未就绪状态",
		Description: "Pod 未在预期时间内达到 Ready，Service 可能无法将流量转发到该 Pod。",
		Advice:      "检查 readiness/startup probe、Pod 事件、镜像拉取、资源调度与上游依赖。",
	},
	"kubedeploymentreplicasmismatch": {
		Name:        "Deployment 副本数不匹配",
		Summary:     "Deployment 可用副本长时间未达到期望值",
		Description: "Deployment 期望副本与实际可用副本不一致，可能存在调度、启动或健康检查问题。",
		Advice:      "查看 Deployment rollout 状态、ReplicaSet、Pod 事件与最近发布变更。",
	},
	"kubenodenotready": {
		Name:        "Kubernetes 节点未就绪",
		Summary:     "集群节点持续处于 NotReady",
		Description: "kubelet 上报的节点 Ready 状态异常，该节点上工作负载可能受影响。",
		Advice:      "检查 EC2 实例、kubelet、CNI、磁盘和网络状态，必要时排空并替换节点。",
	},
}

func enrichAlertTemplateData(data *alertTemplateData, payload alertingrelay.Payload, alert alertingrelay.Alert, configuredCluster string) {
	labels, annotations := alert.Labels, alert.Annotations
	data.AlertCount = len(payload.Alerts)
	data.AlertName = firstNonEmptyAlertValue(labels["alertname"], "UnknownAlert")
	data.Cluster = firstNonEmptyAlertValue(labels["cluster"], labels["cluster_name"], configuredCluster, "未知集群")
	data.Namespace = strings.TrimSpace(labels["namespace"])
	data.Workload = firstNonEmptyAlertValue(labels["workload"], labels["deployment"], labels["statefulset"], labels["daemonset"])
	data.Pod = strings.TrimSpace(labels["pod"])
	data.Container = strings.TrimSpace(labels["container"])
	data.Node = firstNonEmptyAlertValue(labels["node"], labels["kubernetes_node"])
	data.Service = firstNonEmptyAlertValue(labels["service"], labels["service_name"])
	data.MonitorTarget = firstNonEmptyAlertValue(labels["job"], labels["endpoint"])
	data.Instance = strings.TrimSpace(labels["instance"])
	data.Stage, data.JobID = strings.TrimSpace(labels["stage"]), strings.TrimSpace(labels["job_id"])
	data.Engine, data.HTTPStatus = strings.TrimSpace(labels["engine"]), strings.TrimSpace(labels["status_code"])
	data.CurrentValue = firstNonEmptyAlertValue(annotations["value"], labels["value"])
	data.Threshold = firstNonEmptyAlertValue(annotations["threshold"], labels["threshold"])
	data.Duration = firstNonEmptyAlertValue(annotations["duration"], labels["duration"])
	data.Availability = firstNonEmptyAlertValue(annotations["availability"], labels["availability"])
	data.RunbookURL = firstNonEmptyAlertValue(annotations["runbook_url"], alert.GeneratorURL)
	if !alert.StartsAt.IsZero() {
		data.StartsAt = alert.StartsAt.UTC().Format("2006-01-02 15:04:05 UTC")
	} else {
		data.StartsAt = "未提供"
	}
	localized, known := localizedPrometheusAlerts[strings.ToLower(data.AlertName)]
	if known {
		data.AlertNameText, data.Summary, data.Description, data.Advice = localized.Name, localized.Summary, localized.Description, localized.Advice
	} else {
		data.AlertNameText = "Prometheus 自定义监控规则"
		data.Summary = "监控规则 " + data.AlertName + " 已触发"
		data.Description = "Prometheus 已检测到异常；下方保留原始监控信息，请结合影响对象、指标目标和处理建议排查。"
		data.Advice = defaultAlertAdvice(data.EventType)
	}
	if data.Status == "resolved" || data.Status == "recovered" {
		data.Summary = data.AlertNameText + "已恢复"
		data.Description = "此前的告警条件已解除。请结合持续时间和最近变更确认服务已稳定。"
		data.Advice = "继续观察相关指标与日志，确认告警没有反复触发。"
	}
	original := strings.TrimSpace(strings.Join([]string{annotations["summary"], annotations["description"]}, " "))
	if original != "" {
		data.OriginalMessage = original
	}
	data.RelatedAlerts = relatedAlertMarkdown(payload.Alerts[1:])
}

func defaultAlertAdvice(eventType string) string {
	switch eventType {
	case "cluster-control-plane":
		return "检查 EKS 控制平面状态、Prometheus Targets、ServiceMonitor 和相关 RBAC；确认是真实故障还是托管组件指标不可采集。"
	case "node-resource":
		return "检查节点负载、Pod requests/limits 和节点组容量，必要时扩容或迁移工作负载。"
	case "deployment":
		return "进入平台任务日志查看首个错误，修复配置或依赖后再执行重试。"
	case "database":
		return "检查实例状态、安全组、DNS、连接数和凭据是否有效。"
	case "service-availability":
		return "检查网关、Ingress、后端 Service、Pod 就绪状态和最近发布变更。"
	default:
		return "查看 Kubernetes 事件、相关容器日志、健康检查和最近配置变更。"
	}
}

func relatedAlertMarkdown(alerts []alertingrelay.Alert) string {
	if len(alerts) == 0 {
		return ""
	}
	limit := len(alerts)
	if limit > 9 {
		limit = 9
	}
	lines := make([]string, 0, limit+1)
	for _, alert := range alerts[:limit] {
		name := firstNonEmptyAlertValue(alert.Labels["alertname"], "UnknownAlert")
		if localized, ok := localizedPrometheusAlerts[strings.ToLower(name)]; ok {
			name = localized.Name
		}
		scope := firstNonEmptyAlertValue(alert.Labels["pod"], alert.Labels["workload"], alert.Labels["node"], alert.Labels["instance"], alert.Labels["service"])
		if scope != "" {
			name += " · `" + scope + "`"
		}
		lines = append(lines, "- "+name)
	}
	if len(alerts) > limit {
		lines = append(lines, fmt.Sprintf("- 其余 %d 条请在 Alertmanager 中查看", len(alerts)-limit))
	}
	return strings.Join(lines, "\n")
}

func renderConfiguredAlertMessage(alerting map[string]any, payload alertingrelay.Payload, project, environmentKey, configuredCluster string) (alertMessage, string, error) {
	fallback, err := alertingrelay.Render(payload, project, environmentKey)
	if err != nil {
		return alertMessage{}, "", err
	}
	if len(payload.Alerts) == 0 {
		return alertMessage{Title: "AWS 部署平台告警", Markdown: fallback}, "", nil
	}
	first := payload.Alerts[0]
	eventType := alertEventType(first)
	severity := strings.ToLower(strings.TrimSpace(first.Labels["severity"]))
	status := strings.ToLower(strings.TrimSpace(payload.Status))
	if status == "" {
		status = strings.ToLower(strings.TrimSpace(first.Status))
	}
	statusText, statusIcon := alertStatusPresentation(status, severity)
	fallbackMessage := alertMessage{
		Title:    statusIcon + " " + statusText + "｜" + firstNonEmptyAlertValue(first.Annotations["summary"], first.Labels["alertname"], "平台告警"),
		Markdown: fallback, Status: status, Severity: severity, EventType: eventType,
	}
	templates, _ := alerting["templates"].([]any)
	var selected map[string]any
	for _, raw := range templates {
		candidate, ok := raw.(map[string]any)
		if !ok || strings.TrimSpace(documentStringValue(candidate["event_type"])) != eventType {
			continue
		}
		if selected == nil {
			selected = candidate
		}
		if strings.EqualFold(strings.TrimSpace(documentStringValue(candidate["severity"])), severity) {
			selected = candidate
			break
		}
	}
	if selected == nil {
		return fallbackMessage, "", nil
	}
	data := alertTemplateData{
		Status: status, StatusText: statusText, StatusIcon: statusIcon,
		Severity: severity, SeverityText: alertSeverityText(severity), Project: project, Environment: environmentKey, EventType: eventType,
		CommonLabels: first.Labels, CommonAnnotations: first.Annotations,
	}
	enrichAlertTemplateData(&data, payload, first, configuredCluster)
	render := func(name, value string) (string, error) {
		parsed, err := template.New(name).Funcs(template.FuncMap{"toUpper": strings.ToUpper, "toLower": strings.ToLower}).Option("missingkey=zero").Parse(value)
		if err != nil {
			return "", err
		}
		var output bytes.Buffer
		if err := parsed.Execute(&output, data); err != nil {
			return "", err
		}
		return strings.TrimSpace(output.String()), nil
	}
	title, err := render("title", documentStringValue(selected["title"]))
	if err != nil {
		return alertMessage{}, "", err
	}
	body, err := render("body", documentStringValue(selected["body"]))
	if err != nil {
		return alertMessage{}, "", err
	}
	if title == "" && body == "" {
		return fallbackMessage, "", nil
	}
	if title == "" {
		title = fallbackMessage.Title
	}
	if body == "" {
		body = fallback
	}
	title = truncateAlertText(title, 200)
	body = truncateAlertText(body, 12000)
	return alertMessage{Title: title, Markdown: body, Status: status, Severity: severity, EventType: eventType}, strings.TrimSpace(documentStringValue(selected["name"])), nil
}

func firstNonEmptyAlertValue(values ...string) string {
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			return value
		}
	}
	return ""
}

func truncateAlertText(value string, limit int) string {
	characters := []rune(strings.TrimSpace(value))
	if len(characters) <= limit {
		return string(characters)
	}
	return string(characters[:limit]) + "…"
}

func alertStatusPresentation(status, severity string) (string, string) {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "resolved", "recovered":
		return "告警已恢复", "🟢"
	case "normal", "ok", "success":
		return "运行正常", "🟢"
	}
	if strings.EqualFold(strings.TrimSpace(severity), "critical") || strings.EqualFold(strings.TrimSpace(severity), "fatal") {
		return "严重告警", "🔴"
	}
	return "异常告警", "🟡"
}

func alertSeverityText(severity string) string {
	switch strings.ToLower(strings.TrimSpace(severity)) {
	case "critical", "fatal":
		return "严重"
	case "warning", "warn":
		return "异常"
	case "info", "normal":
		return "正常"
	default:
		return "未知"
	}
}

func alertEventType(alert alertingrelay.Alert) string {
	if value := strings.TrimSpace(alert.Labels["event_type"]); value != "" {
		return value
	}
	name := strings.ToLower(alert.Labels["alertname"])
	switch {
	case strings.Contains(name, "deploy") || alert.Labels["job_id"] != "":
		return "deployment"
	case strings.Contains(name, "database") || strings.Contains(name, "mysql") || strings.Contains(name, "postgres") || alert.Labels["engine"] != "":
		return "database"
	case strings.Contains(name, "controllermanager") || strings.Contains(name, "schedulerdown") || strings.Contains(name, "etcddown") || strings.Contains(name, "kubeapidown"):
		return "cluster-control-plane"
	case strings.Contains(name, "node") || alert.Labels["node"] != "" || (alert.Labels["instance"] != "" && alert.Labels["namespace"] == ""):
		return "node-resource"
	case strings.Contains(name, "availability") || strings.Contains(name, "http") || alert.Labels["status_code"] != "":
		return "service-availability"
	default:
		return "kubernetes-workload"
	}
}

func configuredAlertChannels(alerting map[string]any) []alertChannel {
	rawChannels, _ := alerting["channels"].([]any)
	result := make([]alertChannel, 0, len(rawChannels))
	for _, raw := range rawChannels {
		value, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		name := strings.TrimSpace(documentStringValue(value["name"]))
		if name == "" {
			continue
		}
		result = append(result, alertChannel{
			Name:      name,
			Type:      strings.ToLower(strings.TrimSpace(documentStringValue(value["type"]))),
			Address:   strings.TrimSpace(documentStringValue(value["address"])),
			SecretRef: strings.TrimSpace(documentStringValue(value["secret_ref"])),
		})
	}
	return result
}

func documentBoolValue(value any) bool {
	result, _ := value.(bool)
	return result
}

func findAlertChannel(doc environment.Document, name string) (alertChannel, error) {
	alerting, ok := doc["alerting"].(map[string]any)
	if !ok {
		return alertChannel{}, errors.New("环境尚未配置告警中心")
	}
	channels, ok := alerting["channels"].([]any)
	if !ok {
		return alertChannel{}, errors.New("环境尚未配置告警通道")
	}
	for _, raw := range channels {
		value, ok := raw.(map[string]any)
		if !ok || strings.TrimSpace(documentStringValue(value["name"])) != name {
			continue
		}
		return alertChannel{
			Name:      name,
			Type:      strings.ToLower(strings.TrimSpace(documentStringValue(value["type"]))),
			Address:   strings.TrimSpace(documentStringValue(value["address"])),
			SecretRef: strings.TrimSpace(documentStringValue(value["secret_ref"])),
		}, nil
	}
	return alertChannel{}, errors.New("未找到该告警通道，请先保存环境配置后再测试")
}

func documentStringValue(value any) string {
	result, _ := value.(string)
	return result
}

func sendAlertChannelTest(ctx context.Context, channel alertChannel, project, environmentKey string) (string, error) {
	message := alertMessage{
		Title:    "🟢 运行正常｜告警通道连接测试",
		Markdown: fmt.Sprintf("**测试结果：** 通道发送正常\n**项目 / 环境：** `%s` / `%s`\n**测试时间：** %s\n\n> 这是一条连接测试消息，不代表集群发生了真实告警。", project, environmentKey, time.Now().Format(time.RFC3339)),
		Status:   "normal", Severity: "normal", EventType: "channel-test",
	}
	return sendAlertChannelMessage(ctx, channel, message)
}

func sendAlertChannelMessage(ctx context.Context, channel alertChannel, message alertMessage) (string, error) {
	provider := normalizedAlertProvider(channel.Type, channel.Address)
	if provider == "email" {
		return provider, errors.New("邮箱通道需要先配置 SMTP 服务，当前可使用 Webhook 类通道测试")
	}
	if provider == "telegram" {
		return provider, errors.New("Telegram 测试需要同时配置 Bot Token 与 Chat ID，当前单地址配置不足")
	}
	endpoint, addresses, err := validateAlertWebhookURL(ctx, channel.Address)
	if err != nil {
		return provider, err
	}
	payload, err := buildAlertTestPayload(provider, message)
	if err != nil {
		return provider, err
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return provider, errors.New("生成测试消息失败")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint.String(), bytes.NewReader(body))
	if err != nil {
		return provider, errors.New("创建测试请求失败")
	}
	req.Header.Set("Content-Type", "application/json; charset=utf-8")
	client := &http.Client{
		Timeout:   20 * time.Second,
		Transport: pinnedAlertTransport(endpoint, addresses),
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return errors.New("告警 Webhook 不允许重定向")
		},
	}
	response, err := client.Do(req)
	if err != nil {
		return provider, errors.New("Webhook 请求未成功完成")
	}
	defer response.Body.Close()
	responseBody, err := io.ReadAll(io.LimitReader(response.Body, maxAlertTestResponseBytes+1))
	if err != nil {
		return provider, errors.New("无法读取 Webhook 响应")
	}
	if len(responseBody) > maxAlertTestResponseBytes {
		return provider, errors.New("Webhook 响应内容超过安全限制")
	}
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return provider, fmt.Errorf("Webhook 返回 HTTP %d", response.StatusCode)
	}
	if provider == "lark" {
		var result struct {
			Code          *int   `json:"code"`
			StatusCode    *int   `json:"StatusCode"`
			Message       string `json:"msg"`
			StatusMessage string `json:"StatusMessage"`
		}
		if err := json.Unmarshal(responseBody, &result); err != nil {
			return provider, errors.New("Lark 返回了无法识别的响应")
		}
		if (result.Code != nil && *result.Code != 0) || (result.StatusCode != nil && *result.StatusCode != 0) {
			message := strings.TrimSpace(result.Message)
			if message == "" {
				message = strings.TrimSpace(result.StatusMessage)
			}
			if message == "" {
				message = "Lark 拒绝了测试消息"
			}
			return provider, errors.New(message)
		}
	}
	return provider, nil
}

func normalizedAlertProvider(channelType, address string) string {
	channelType = strings.ToLower(strings.TrimSpace(channelType))
	if channelType == "lark" || channelType == "feishu" {
		return "lark"
	}
	if channelType == "webhook" {
		if parsed, err := url.Parse(address); err == nil {
			host := strings.TrimSuffix(strings.ToLower(parsed.Hostname()), ".")
			if host == "open.larksuite.com" || host == "open.feishu.cn" {
				return "lark"
			}
		}
	}
	return channelType
}

func alertMessageColors(message alertMessage) (string, string) {
	status := strings.ToLower(strings.TrimSpace(message.Status))
	if status == "resolved" || status == "recovered" || status == "normal" || status == "ok" || status == "success" {
		return "green", "#00b42a"
	}
	severity := strings.ToLower(strings.TrimSpace(message.Severity))
	if severity == "critical" || severity == "fatal" {
		return "red", "#f53f3f"
	}
	return "yellow", "#f7ba1e"
}

func buildAlertTestPayload(provider string, message alertMessage) (map[string]any, error) {
	title := truncateAlertText(message.Title, 200)
	if title == "" {
		title = "AWS 部署平台告警"
	}
	markdown := truncateAlertText(message.Markdown, 12000)
	if markdown == "" {
		markdown = title
	}
	plainText := message.plainText()
	theme, color := alertMessageColors(message)
	switch provider {
	case "lark":
		return map[string]any{
			"msg_type": "interactive",
			"card": map[string]any{
				"config":   map[string]any{"wide_screen_mode": true},
				"header":   map[string]any{"template": theme, "title": map[string]any{"tag": "plain_text", "content": title}},
				"elements": []any{map[string]any{"tag": "div", "text": map[string]any{"tag": "lark_md", "content": markdown}}},
			},
		}, nil
	case "slack":
		return map[string]any{"attachments": []any{map[string]any{
			"color":  color,
			"blocks": []any{map[string]any{"type": "section", "text": map[string]any{"type": "mrkdwn", "text": "*" + title + "*\n\n" + markdown}}},
		}}}, nil
	case "dingtalk":
		return map[string]any{"msgtype": "markdown", "markdown": map[string]any{"title": title, "text": "### " + title + "\n\n" + markdown}}, nil
	case "wecom":
		fontColor := "warning"
		if theme == "green" {
			fontColor = "info"
		}
		return map[string]any{"msgtype": "markdown", "markdown": map[string]any{"content": fmt.Sprintf("<font color=\"%s\">**%s**</font>\n\n%s", fontColor, title, markdown)}}, nil
	case "webhook":
		return map[string]any{"event": "alert_notification", "source": "ops-deploy-platform", "status": message.Status, "severity": message.Severity, "event_type": message.EventType, "color": color, "title": title, "markdown": markdown, "message": plainText}, nil
	default:
		return nil, fmt.Errorf("暂不支持 %q 类型的通道测试", provider)
	}
}

func validateAlertWebhookURL(ctx context.Context, raw string) (*url.URL, []net.IP, error) {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.Hostname() == "" || parsed.User != nil || parsed.Fragment != "" {
		return nil, nil, errors.New("测试发送仅允许使用完整的 HTTPS Webhook 地址")
	}
	host := strings.TrimSuffix(strings.ToLower(parsed.Hostname()), ".")
	if host == "localhost" || strings.HasSuffix(host, ".localhost") || strings.HasSuffix(host, ".local") {
		return nil, nil, errors.New("Webhook 地址不能指向本机或内网")
	}
	resolved, err := net.DefaultResolver.LookupIPAddr(ctx, host)
	if err != nil || len(resolved) == 0 {
		return nil, nil, errors.New("Webhook 域名解析失败")
	}
	addresses := make([]net.IP, 0, len(resolved))
	for _, item := range resolved {
		if !publicAlertIP(item.IP) {
			return nil, nil, errors.New("Webhook 地址不能指向本机、内网或保留地址")
		}
		addresses = append(addresses, item.IP)
	}
	return parsed, addresses, nil
}

func publicAlertIP(ip net.IP) bool {
	return ip != nil && !ip.IsLoopback() && !ip.IsPrivate() && !ip.IsLinkLocalUnicast() && !ip.IsLinkLocalMulticast() &&
		!ip.IsMulticast() && !ip.IsUnspecified()
}

func pinnedAlertTransport(endpoint *url.URL, addresses []net.IP) *http.Transport {
	host := endpoint.Hostname()
	port := endpoint.Port()
	if port == "" {
		port = "443"
	}
	dialer := &net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}
	return &http.Transport{
		Proxy:                 nil,
		ForceAttemptHTTP2:     true,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: 15 * time.Second,
		TLSClientConfig:       &tls.Config{MinVersion: tls.VersionTLS12, ServerName: host},
		DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
			var lastErr error
			for _, address := range addresses {
				connection, err := dialer.DialContext(ctx, network, net.JoinHostPort(address.String(), port))
				if err == nil {
					return connection, nil
				}
				lastErr = err
			}
			if lastErr == nil {
				lastErr = errors.New("Webhook 域名没有可用公网地址")
			}
			return nil, lastErr
		},
	}
}

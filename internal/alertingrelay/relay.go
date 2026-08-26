package alertingrelay

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
)

const tokenContext = "ops-deploy-platform:alert-relay:v1:"

var ScenarioTypes = []string{
	"cluster-control-plane",
	"kubernetes-workload",
	"node-resource",
	"deployment",
	"database",
	"service-availability",
	"recovery",
}

type Payload struct {
	Status      string  `json:"status"`
	Receiver    string  `json:"receiver"`
	ExternalURL string  `json:"externalURL"`
	Alerts      []Alert `json:"alerts"`
}

type Alert struct {
	Status       string            `json:"status"`
	Labels       map[string]string `json:"labels"`
	Annotations  map[string]string `json:"annotations"`
	StartsAt     time.Time         `json:"startsAt"`
	EndsAt       time.Time         `json:"endsAt"`
	GeneratorURL string            `json:"generatorURL"`
}

func DeriveToken(encodedKey, target string) (string, error) {
	key, err := base64.StdEncoding.DecodeString(strings.TrimSpace(encodedKey))
	if err != nil || len(key) != 32 {
		clear(key)
		return "", errors.New("credential encryption key must be a base64-encoded 32-byte key")
	}
	target = strings.TrimSpace(target)
	if target == "" || len(target) > 63 {
		clear(key)
		return "", errors.New("alert relay target is invalid")
	}
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write([]byte(tokenContext + target))
	result := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	clear(key)
	return result, nil
}

func VerifyToken(encodedKey, target, provided string) bool {
	expected, err := DeriveToken(encodedKey, target)
	if err != nil || len(provided) != len(expected) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(expected), []byte(provided)) == 1
}

func Render(payload Payload, project, environment string) (string, error) {
	if len(payload.Alerts) == 0 {
		return "", errors.New("Alertmanager payload contains no alerts")
	}
	status := strings.ToLower(strings.TrimSpace(payload.Status))
	if status == "" {
		status = strings.ToLower(strings.TrimSpace(payload.Alerts[0].Status))
	}
	statusLabel := "告警触发"
	if status == "resolved" {
		statusLabel = "告警恢复"
	}
	var builder strings.Builder
	_, _ = fmt.Fprintf(&builder, "AWS 部署平台 · %s\n项目：%s\n环境：%s\n告警数量：%d\n", statusLabel, project, environment, len(payload.Alerts))
	limit := len(payload.Alerts)
	if limit > 10 {
		limit = 10
	}
	for index := 0; index < limit; index++ {
		item := payload.Alerts[index]
		name := firstNonEmpty(item.Labels["alertname"], item.Annotations["summary"], "未命名告警")
		severity := firstNonEmpty(item.Labels["severity"], "unknown")
		_, _ = fmt.Fprintf(&builder, "\n%d. %s [%s]", index+1, name, severity)
		if summary := strings.TrimSpace(item.Annotations["summary"]); summary != "" && summary != name {
			_, _ = fmt.Fprintf(&builder, "\n   %s", truncate(summary, 300))
		}
		if description := strings.TrimSpace(item.Annotations["description"]); description != "" {
			_, _ = fmt.Fprintf(&builder, "\n   %s", truncate(description, 500))
		}
		labels := selectedLabels(item.Labels)
		if labels != "" {
			_, _ = fmt.Fprintf(&builder, "\n   %s", labels)
		}
		if !item.StartsAt.IsZero() {
			_, _ = fmt.Fprintf(&builder, "\n   开始：%s", item.StartsAt.Format(time.RFC3339))
		}
	}
	if len(payload.Alerts) > limit {
		_, _ = fmt.Fprintf(&builder, "\n\n其余 %d 条告警已合并省略，请到 Alertmanager 查看详情。", len(payload.Alerts)-limit)
	}
	return truncate(builder.String(), 12000), nil
}

func SamplePayload(eventType, project, environment string, now time.Time) (Payload, error) {
	payloadStatus := "firing"
	base := Alert{
		Status:   "firing",
		StartsAt: now.UTC(),
		Labels: map[string]string{
			"event_type": eventType, "project": project, "environment": environment,
		},
		Annotations: map[string]string{},
	}
	switch eventType {
	case "cluster-control-plane":
		base.Labels["alertname"] = "KubeControllerManagerDown"
		base.Labels["severity"] = "critical"
		base.Labels["cluster"] = project + "-" + environment + "-eks"
		base.Labels["job"] = "kube-controller-manager"
		base.Annotations["summary"] = "Target disappeared from Prometheus target discovery."
		base.Annotations["description"] = "KubeControllerManager has disappeared from Prometheus target discovery."
		base.Annotations["runbook_url"] = "https://runbooks.prometheus-operator.dev/runbooks/kubernetes/kubecontrollermanagerdown"
	case "kubernetes-workload":
		base.Labels["alertname"] = "PodCrashLooping"
		base.Labels["severity"] = "critical"
		base.Labels["cluster"] = project + "-" + environment + "-eks"
		base.Labels["namespace"] = "platform-server"
		base.Labels["workload"] = "sample-api"
		base.Labels["pod"] = "sample-api-7d9f6c8d5-test"
		base.Annotations["summary"] = "示例工作负载持续重启"
		base.Annotations["description"] = "Pod 在 10 分钟内多次进入 CrashLoopBackOff，请检查容器日志和健康检查。"
	case "node-resource":
		base.Labels["alertname"] = "NodeCPUUsageHigh"
		base.Labels["severity"] = "warning"
		base.Labels["instance"] = "ip-10-40-1-25"
		base.Labels["node"] = "ip-10-40-1-25"
		base.Annotations["summary"] = "示例节点 CPU 使用率持续偏高"
		base.Annotations["value"] = "92%"
		base.Annotations["threshold"] = "85% / 10m"
	case "deployment":
		base.Labels["alertname"] = "DeploymentFailed"
		base.Labels["severity"] = "critical"
		base.Labels["stage"] = "阶段2 · 组件与接入配置"
		base.Labels["job_id"] = "sample-job"
		base.Annotations["summary"] = "示例自动化部署失败"
		base.Annotations["description"] = "Terraform/Helm 执行返回错误，请进入平台查看阶段日志和失败诊断。"
	case "database":
		base.Labels["alertname"] = "DatabaseConnectionFailed"
		base.Labels["severity"] = "critical"
		base.Labels["service"] = "game-mysql"
		base.Labels["engine"] = "mysql"
		base.Annotations["summary"] = "示例数据库连接异常"
		base.Annotations["duration"] = "5m"
		base.Annotations["description"] = "应用连接池无法建立新连接，请检查实例状态、安全组和凭据。"
	case "service-availability":
		base.Labels["alertname"] = "ServiceAvailabilityLow"
		base.Labels["severity"] = "warning"
		base.Labels["service"] = "sample-gateway"
		base.Labels["status_code"] = "503"
		base.Annotations["summary"] = "示例服务可用性下降"
		base.Annotations["availability"] = "97.2%"
		base.Annotations["description"] = "最近 5 分钟错误率超过阈值，请检查网关与后端 Service。"
	case "recovery":
		payloadStatus = "resolved"
		base.Status = "resolved"
		base.EndsAt = now.UTC()
		base.Labels["event_type"] = "service-availability"
		base.Labels["alertname"] = "ServiceAvailabilityRecovered"
		base.Labels["severity"] = "warning"
		base.Labels["service"] = "sample-gateway"
		base.Labels["status_code"] = "200"
		base.Annotations["summary"] = "示例服务可用性已经恢复"
		base.Annotations["availability"] = "100%"
		base.Annotations["description"] = "网关和后端 Service 已恢复正常，本条用于验证绿色恢复通知。"
	default:
		return Payload{}, fmt.Errorf("unsupported alert scenario %q", eventType)
	}
	return Payload{Status: payloadStatus, Receiver: "ops-deploy-platform-scenario-test", Alerts: []Alert{base}}, nil
}

func selectedLabels(labels map[string]string) string {
	keys := []string{"namespace", "pod", "deployment", "statefulset", "service", "node", "instance"}
	values := make([]string, 0, len(keys))
	for _, key := range keys {
		if value := strings.TrimSpace(labels[key]); value != "" {
			values = append(values, key+"="+truncate(value, 120))
		}
	}
	sort.Strings(values)
	return strings.Join(values, " · ")
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			return value
		}
	}
	return ""
}

func truncate(value string, limit int) string {
	value = strings.TrimSpace(value)
	characters := []rune(value)
	if len(characters) <= limit {
		return value
	}
	return string(characters[:limit]) + "…"
}

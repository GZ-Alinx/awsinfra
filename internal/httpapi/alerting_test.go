package httpapi

import (
	"context"
	"net"
	"os"
	"strings"
	"testing"
	"time"

	"ops-deploy-platform/internal/alertingrelay"
	"ops-deploy-platform/internal/environment"
)

func TestNormalizedAlertProviderRecognizesLarkWebhook(t *testing.T) {
	if got := normalizedAlertProvider("webhook", "https://open.larksuite.com/open-apis/bot/v2/hook/redacted"); got != "lark" {
		t.Fatalf("provider=%q, want lark", got)
	}
	if got := normalizedAlertProvider("lark", "https://example.com/hook"); got != "lark" {
		t.Fatalf("explicit provider=%q, want lark", got)
	}
}

func TestBuildAlertTestPayloadUsesColoredLarkMarkdownCard(t *testing.T) {
	payload, err := buildAlertTestPayload("lark", alertMessage{Title: "严重告警", Markdown: "**服务：** api", Status: "firing", Severity: "critical"})
	if err != nil {
		t.Fatal(err)
	}
	if payload["msg_type"] != "interactive" {
		t.Fatalf("unexpected Lark message type: %#v", payload)
	}
	card, ok := payload["card"].(map[string]any)
	if !ok {
		t.Fatalf("Lark payload has no card: %#v", payload)
	}
	header, _ := card["header"].(map[string]any)
	if header["template"] != "red" {
		t.Fatalf("critical Lark card is not red: %#v", header)
	}
	elements, _ := card["elements"].([]any)
	first, _ := elements[0].(map[string]any)
	text, _ := first["text"].(map[string]any)
	if text["tag"] != "lark_md" || text["content"] != "**服务：** api" {
		t.Fatalf("unexpected Lark Markdown content: %#v", card)
	}

	recovered, err := buildAlertTestPayload("lark", alertMessage{Title: "恢复", Markdown: "恢复", Status: "resolved", Severity: "critical"})
	if err != nil {
		t.Fatal(err)
	}
	recoveredCard, _ := recovered["card"].(map[string]any)
	recoveredHeader, _ := recoveredCard["header"].(map[string]any)
	if recoveredHeader["template"] != "green" {
		t.Fatalf("recovered Lark card is not green: %#v", recoveredHeader)
	}
}

func TestAlertWebhookValidationRejectsUnsafeDestinations(t *testing.T) {
	for _, value := range []string{
		"http://open.larksuite.com/open-apis/bot/v2/hook/example",
		"https://localhost/hook",
		"https://service.local/hook",
		"https://user:password@example.com/hook",
	} {
		if _, _, err := validateAlertWebhookURL(context.Background(), value); err == nil {
			t.Fatalf("unsafe webhook URL was accepted: %s", value)
		}
	}
	for _, value := range []string{"127.0.0.1", "10.0.0.1", "169.254.169.254", "::1", "fc00::1"} {
		if publicAlertIP(net.ParseIP(value)) {
			t.Fatalf("private or local IP was accepted: %s", value)
		}
	}
}

func TestRenderConfiguredAlertMessageUsesMatchingScenarioTemplate(t *testing.T) {
	payload, err := alertingrelay.SamplePayload("database", "demo", "test", time.Date(2026, 7, 18, 4, 5, 6, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	alerting := map[string]any{"templates": []any{map[string]any{
		"name": "database-critical", "event_type": "database", "severity": "critical",
		"title": "[{{ .Status | toUpper }}] {{ .CommonLabels.service }}",
		"body":  "{{ .CommonAnnotations.summary }} · {{ .CommonLabels.environment }}",
	}}}
	message, templateName, err := renderConfiguredAlertMessage(alerting, payload, "demo", "test", "demo-test-eks")
	if err != nil {
		t.Fatal(err)
	}
	if templateName != "database-critical" {
		t.Fatalf("template=%q, want database-critical", templateName)
	}
	for _, expected := range []string{"[FIRING]", "game-mysql", "示例数据库连接异常", "test"} {
		if !strings.Contains(message.plainText(), expected) {
			t.Fatalf("rendered template is missing %q: %s", expected, message.plainText())
		}
	}
	if message.Status != "firing" || message.Severity != "critical" || message.EventType != "database" {
		t.Fatalf("rendered alert metadata is incomplete: %#v", message)
	}
}

func TestAlertEventTypeClassification(t *testing.T) {
	tests := []struct {
		name   string
		labels map[string]string
		want   string
	}{
		{name: "explicit", labels: map[string]string{"event_type": "database"}, want: "database"},
		{name: "deployment", labels: map[string]string{"alertname": "DeploymentFailed"}, want: "deployment"},
		{name: "database", labels: map[string]string{"engine": "mysql"}, want: "database"},
		{name: "EKS control plane", labels: map[string]string{"alertname": "KubeControllerManagerDown"}, want: "cluster-control-plane"},
		{name: "node", labels: map[string]string{"node": "ip-10-0-0-1"}, want: "node-resource"},
		{name: "availability", labels: map[string]string{"status_code": "503"}, want: "service-availability"},
		{name: "workload fallback", labels: map[string]string{"alertname": "PodCrashLooping"}, want: "kubernetes-workload"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := alertEventType(alertingrelay.Alert{Labels: test.labels}); got != test.want {
				t.Fatalf("event type=%q, want %q", got, test.want)
			}
		})
	}
}

func TestCoreAlertDeliveryFiltersNoiseWithoutDroppingActionableHealth(t *testing.T) {
	payload := alertingrelay.Payload{Status: "firing", Receiver: "platform", Alerts: []alertingrelay.Alert{
		{Status: "firing", Labels: map[string]string{"alertname": "KubeControllerManagerDown", "severity": "critical"}},
		{Status: "firing", Labels: map[string]string{"alertname": "KubePodCrashLooping", "severity": "critical"}},
		{Status: "resolved", Labels: map[string]string{"alertname": "DatabaseConnectionFailed", "event_type": "database"}},
		{Status: "firing", Labels: map[string]string{"alertname": "InfoInhibitor", "severity": "info"}},
	}}
	filtered, suppressed := filterAlertPayloadForDelivery(map[string]any{"delivery_policy": "core"}, payload)
	if suppressed != 2 || len(filtered.Alerts) != 2 {
		t.Fatalf("core filter returned %d alerts and suppressed %d: %#v", len(filtered.Alerts), suppressed, filtered.Alerts)
	}
	if filtered.Alerts[0].Labels["alertname"] != "KubePodCrashLooping" || filtered.Alerts[1].Labels["event_type"] != "database" {
		t.Fatalf("core filter kept the wrong alerts: %#v", filtered.Alerts)
	}
	if filtered.Status != "firing" {
		t.Fatalf("filtered payload status=%q, want firing", filtered.Status)
	}
}

func TestCoreAlertDeliveryAllowsEKSAPIAndSuppressesManagedControlPlaneTargets(t *testing.T) {
	for name, want := range map[string]bool{
		"KubeAPIDown":               true,
		"KubeControllerManagerDown": false,
		"KubeSchedulerDown":         false,
		"TargetDown":                false,
		"Watchdog":                  false,
	} {
		if got := isCoreAlert(alertingrelay.Alert{Labels: map[string]string{"alertname": name}}); got != want {
			t.Fatalf("isCoreAlert(%s)=%t, want %t", name, got, want)
		}
	}
}

func TestAllAlertDeliveryPolicyPreservesPayload(t *testing.T) {
	payload := alertingrelay.Payload{Status: "firing", Alerts: []alertingrelay.Alert{{Labels: map[string]string{"alertname": "Watchdog"}}}}
	filtered, suppressed := filterAlertPayloadForDelivery(map[string]any{"delivery_policy": "all"}, payload)
	if suppressed != 0 || len(filtered.Alerts) != 1 || filtered.Alerts[0].Labels["alertname"] != "Watchdog" {
		t.Fatalf("all policy changed the payload: %#v / suppressed=%d", filtered, suppressed)
	}
}

func TestKubeControllerManagerAlertIsLocalizedWithoutEmptyWorkloadFields(t *testing.T) {
	doc := environment.DefaultDocument("kbp", "test")
	alerting := doc["alerting"].(map[string]any)
	payload := alertingrelay.Payload{Status: "firing", Alerts: []alertingrelay.Alert{{
		Status:   "firing",
		StartsAt: time.Date(2026, 7, 20, 7, 38, 39, 0, time.UTC),
		Labels: map[string]string{
			"alertname": "KubeControllerManagerDown",
			"severity":  "critical",
			"job":       "kube-controller-manager",
		},
		Annotations: map[string]string{
			"summary":     "Target disappeared from Prometheus target discovery.",
			"description": "KubeControllerManager has disappeared from Prometheus target discovery.",
		},
	}}}
	message, templateName, err := renderConfiguredAlertMessage(alerting, payload, "kbp", "test", "kbp-test-eks")
	if err != nil {
		t.Fatal(err)
	}
	if templateName != "cluster-control-plane-critical" || message.EventType != "cluster-control-plane" {
		t.Fatalf("control-plane alert used the wrong template: %q / %#v", templateName, message)
	}
	for _, expected := range []string{
		"EKS 控制面监控", "kbp-test-eks", "KubeControllerManagerDown",
		"Kubernetes 控制器管理器监控不可达", "不代表业务 Pod 或 EKS 集群已经故障", "2026-07-20 07:38:39 UTC",
	} {
		if !strings.Contains(message.plainText(), expected) {
			t.Fatalf("localized control-plane alert is missing %q:\n%s", expected, message.plainText())
		}
	}
	for _, emptyField := range []string{"Namespace：", "工作负载：", "Pod："} {
		if strings.Contains(message.Markdown, emptyField) {
			t.Fatalf("control-plane alert contains irrelevant empty field %q:\n%s", emptyField, message.Markdown)
		}
	}
}

func TestDefaultAlertTemplatesRenderEveryScenario(t *testing.T) {
	alerting := environment.DefaultDocument("demo", "test")["alerting"].(map[string]any)
	for _, scenario := range alertingrelay.ScenarioTypes {
		t.Run(scenario, func(t *testing.T) {
			payload, err := alertingrelay.SamplePayload(scenario, "demo", "test", time.Date(2026, 7, 23, 1, 2, 3, 0, time.UTC))
			if err != nil {
				t.Fatal(err)
			}
			message, templateName, err := renderConfiguredAlertMessage(alerting, payload, "demo", "test", "demo-test-eks")
			if err != nil {
				t.Fatalf("default template failed to render: %v", err)
			}
			if templateName == "" || message.Title == "" || message.Markdown == "" {
				t.Fatalf("scenario did not use a complete default template: %q / %#v", templateName, message)
			}
			for _, expected := range []string{"**影响范围**", "项目：", "环境：", "demo-test-eks", "摘要：", "开始：", "建议："} {
				if !strings.Contains(message.Markdown, expected) {
					t.Fatalf("scenario %s is missing %q:\n%s", scenario, expected, message.Markdown)
				}
			}
		})
	}
}

// TestLiveAlertScenarios is opt-in because it sends every supported real
// webhook test message.
// It is used before production releases to verify provider connectivity for
// every supported scenario without committing a webhook address to the repo.
func TestLiveAlertScenarios(t *testing.T) {
	address := strings.TrimSpace(os.Getenv("OPS_ALERT_LIVE_WEBHOOK"))
	if address == "" {
		t.Skip("OPS_ALERT_LIVE_WEBHOOK is not configured")
	}
	channel := alertChannel{Name: "release-check", Type: "webhook", Address: address}
	alerting := environment.DefaultDocument("ops-deploy-platform", "release-check")["alerting"].(map[string]any)
	for _, scenario := range alertingrelay.ScenarioTypes {
		t.Run(scenario, func(t *testing.T) {
			payload, err := alertingrelay.SamplePayload(scenario, "ops-deploy-platform", "release-check", time.Now())
			if err != nil {
				t.Fatal(err)
			}
			message, _, err := renderConfiguredAlertMessage(alerting, payload, "ops-deploy-platform", "release-check", "ops-deploy-platform-release-check-eks")
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
			defer cancel()
			if _, err := sendAlertChannelMessage(ctx, channel, message); err != nil {
				t.Fatalf("live %s delivery failed: %v", scenario, err)
			}
		})
	}
}

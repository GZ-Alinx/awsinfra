package alertingrelay

import (
	"encoding/base64"
	"strings"
	"testing"
	"time"
)

func TestRelayTokenIsStableAndScoped(t *testing.T) {
	key := base64.StdEncoding.EncodeToString([]byte("01234567890123456789012345678901"))
	first, err := DeriveToken(key, "demo-test")
	if err != nil {
		t.Fatal(err)
	}
	second, err := DeriveToken(key, "demo-test")
	if err != nil {
		t.Fatal(err)
	}
	other, err := DeriveToken(key, "demo-prod")
	if err != nil {
		t.Fatal(err)
	}
	if first != second || first == other || !VerifyToken(key, "demo-test", first) || VerifyToken(key, "demo-prod", first) {
		t.Fatalf("relay token is not deterministic and environment-scoped")
	}
}

func TestRenderAlertmanagerPayload(t *testing.T) {
	message, err := Render(Payload{Status: "firing", Alerts: []Alert{{
		Status: "firing", StartsAt: time.Date(2026, 7, 18, 1, 2, 3, 0, time.UTC),
		Labels:      map[string]string{"alertname": "PodCrashLooping", "severity": "critical", "namespace": "game", "pod": "api-123"},
		Annotations: map[string]string{"summary": "API pod is restarting", "description": "restart count exceeded the threshold"},
	}}}, "demo", "test")
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{"告警触发", "demo", "test", "PodCrashLooping", "critical", "namespace=game", "pod=api-123"} {
		if !strings.Contains(message, expected) {
			t.Fatalf("rendered message is missing %q: %s", expected, message)
		}
	}
}

func TestEveryAlertScenarioBuildsAndRenders(t *testing.T) {
	for _, eventType := range ScenarioTypes {
		payload, err := SamplePayload(eventType, "demo", "test", time.Date(2026, 7, 18, 1, 2, 3, 0, time.UTC))
		if err != nil {
			t.Fatalf("build %s: %v", eventType, err)
		}
		message, err := Render(payload, "demo", "test")
		if err != nil {
			t.Fatalf("render %s: %v", eventType, err)
		}
		if !strings.Contains(message, payload.Alerts[0].Labels["alertname"]) {
			t.Fatalf("scenario %s did not render its alert name: %s", eventType, message)
		}
	}
}

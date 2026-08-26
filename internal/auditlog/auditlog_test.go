package auditlog

import "testing"

func TestEnrichDescribesPlatformOperations(t *testing.T) {
	tests := []struct {
		method, path, operation, resource, target string
	}{
		{"POST", "/api/auth/login", "登录", "登录会话", "login"},
		{"PUT", "/api/projects/kbp/environments/test", "更新", "项目环境", "kbp / test"},
		{"DELETE", "/api/projects/kbp/environments/test/kubernetes/ingresses/kbp-game/game-admin", "删除或取消", "Ingress", "kbp / test / kbp-game / game-admin"},
		{"POST", "/api/projects/kbp/cicd/jobs/game-admin/sync", "同步", "CI/CD 任务", "kbp / game-admin"},
		{"POST", "/api/projects/kbp/environments/test/static-cdns/kbp-assets/invalidate", "刷新 CDN 缓存", "静态资源 CDN", "kbp / test / kbp-assets"},
		{"PUT", "/api/projects/kbp/environments/test/static-cdns/kbp-assets/objects/images/logo.png", "上传文件", "静态资源 CDN", "kbp / test / kbp-assets / images"},
		{"DELETE", "/api/projects/kbp/environments/test/static-cdns/kbp-assets/objects/images/logo.png", "删除或取消", "静态资源 CDN", "kbp / test / kbp-assets / images"},
		{"POST", "/api/projects/kbp/environments/test/credentials/mysql/reveal", "查看敏感信息", "环境访问凭据", "kbp / test / mysql"},
		{"POST", "/api/internal/alerting/relay/kbp-test", "创建或执行", "系统告警中继", "kbp-test"},
		{"POST", "/api/cicd/webhooks/gitlab/kbp", "创建或执行", "CI/CD Webhook", "gitlab / kbp"},
		{"DELETE", "/api/users/operator", "删除或取消", "平台用户", "operator"},
	}
	for _, test := range tests {
		event := Enrich(Event{Method: test.method, Path: test.path, ResponseStatus: 200})
		if event.Operation != test.operation || event.Resource != test.resource || event.Target != test.target || !event.Successful {
			t.Fatalf("%s %s => %#v", test.method, test.path, event)
		}
	}
}

func TestEnrichMarksRejectedOperationFailed(t *testing.T) {
	event := Enrich(Event{Method: "PUT", Path: "/api/users/operator", ResponseStatus: 403})
	if event.Successful {
		t.Fatal("403 event was marked successful")
	}
	if event.Summary == "" {
		t.Fatal("audit summary was not generated")
	}
}

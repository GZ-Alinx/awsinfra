package auditlog

import (
	"path"
	"strings"
	"time"
)

type Event struct {
	ID             uint64    `json:"id"`
	OccurredAt     time.Time `json:"occurred_at"`
	Username       string    `json:"username"`
	Method         string    `json:"method"`
	Path           string    `json:"path"`
	ResponseStatus int       `json:"response_status"`
	RemoteAddress  string    `json:"remote_address"`
	DurationMS     int64     `json:"duration_ms"`
	Operation      string    `json:"operation"`
	Resource       string    `json:"resource"`
	Target         string    `json:"target"`
	Summary        string    `json:"summary"`
	Successful     bool      `json:"successful"`
}

type Query struct {
	Username      string
	Method        string
	Result        string
	Keyword       string
	IncludeSystem bool
	From          *time.Time
	To            *time.Time
	Page          int
	PageSize      int
}

type Page struct {
	Items    []Event `json:"items"`
	Total    int64   `json:"total"`
	Page     int     `json:"page"`
	PageSize int     `json:"page_size"`
}

func Enrich(event Event) Event {
	event.Successful = event.ResponseStatus >= 200 && event.ResponseStatus < 400
	event.Operation, event.Resource, event.Target = describe(event.Method, event.Path)
	parts := []string{event.Operation}
	if event.Resource != "" {
		parts = append(parts, event.Resource)
	}
	if event.Target != "" {
		parts = append(parts, event.Target)
	}
	event.Summary = strings.Join(parts, " · ")
	return event
}

func describe(method, requestPath string) (operation, resource, target string) {
	cleaned := path.Clean("/" + strings.TrimSpace(requestPath))
	segments := strings.Split(strings.Trim(cleaned, "/"), "/")
	if len(segments) < 2 || segments[0] != "api" {
		return operationFor(method, segments), "平台接口", cleaned
	}

	operation = operationFor(method, segments)
	resource, target = resourceFor(segments[1:])
	return operation, resource, target
}

func operationFor(method string, segments []string) string {
	method = strings.ToUpper(strings.TrimSpace(method))
	if method == "PUT" {
		for _, segment := range segments {
			if segment == "objects" {
				return "上传文件"
			}
		}
	}
	last := ""
	if len(segments) > 0 {
		last = segments[len(segments)-1]
	}
	switch last {
	case "login":
		if method == "POST" {
			return "登录"
		}
	case "logout":
		if method == "POST" {
			return "退出登录"
		}
	case "password":
		if method == "PUT" {
			return "修改密码"
		}
	case "profile":
		if method == "PUT" {
			return "修改资料"
		}
	case "test":
		if method == "POST" {
			return "连接测试"
		}
	case "validate", "inspect", "preview", "analyze", "versions":
		if method == "POST" {
			return "校验"
		}
	case "activate":
		if method == "POST" {
			return "启用"
		}
	case "provision":
		if method == "POST" {
			return "开通"
		}
	case "sync":
		if method == "POST" {
			return "同步"
		}
	case "refresh":
		if method == "POST" {
			return "刷新状态"
		}
	case "upload-url":
		if method == "POST" {
			return "申请上传"
		}
	case "invalidate":
		if method == "POST" {
			return "刷新 CDN 缓存"
		}
	case "retry":
		if method == "POST" {
			return "重试"
		}
	case "cancel":
		if method == "POST" {
			return "取消"
		}
	case "ignore":
		if method == "POST" {
			return "忽略失败"
		}
	case "reveal":
		if method == "POST" {
			return "查看敏感信息"
		}
	case "rotate":
		if method == "POST" {
			return "轮换密钥"
		}
	case "builds":
		if method == "POST" {
			return "触发构建"
		}
	}
	switch strings.ToUpper(method) {
	case "POST":
		return "创建或执行"
	case "PUT", "PATCH":
		return "更新"
	case "DELETE":
		return "删除或取消"
	default:
		return "访问"
	}
}

func resourceFor(segments []string) (resource, target string) {
	if len(segments) == 0 {
		return "平台", ""
	}
	if segments[0] == "auth" {
		return "登录会话", valueAt(segments, 1)
	}
	if segments[0] == "me" {
		return "个人账号", valueAt(segments, 1)
	}
	if segments[0] == "users" {
		return "平台用户", valueAt(segments, 1)
	}
	if segments[0] == "jobs" {
		return "平台任务", valueAt(segments, 1)
	}
	if segments[0] == "aws-credentials" {
		return "AWS 凭据", valueAt(segments, 1)
	}
	if segments[0] == "terraform-state" {
		return "Terraform 状态中心", ""
	}
	if segments[0] == "internal" && valueAt(segments, 1) == "alerting" {
		return "系统告警中继", valueAt(segments, 3)
	}
	if segments[0] == "cicd" && valueAt(segments, 1) == "webhooks" {
		return "CI/CD Webhook", joinTarget(valueAt(segments, 2), valueAt(segments, 3))
	}
	if segments[0] == "component-catalog" {
		return "组件目录", valueAt(segments, 1)
	}
	if segments[0] == "platform" && valueAt(segments, 1) == "gitlab" {
		return "GitLab 服务器", valueAt(segments, 3)
	}
	if segments[0] != "projects" {
		return segmentLabel(segments[0]), valueAt(segments, 1)
	}

	project := valueAt(segments, 1)
	if len(segments) == 2 {
		return "项目", project
	}
	switch segments[2] {
	case "members":
		return "项目成员", joinTarget(project, valueAt(segments, 3))
	case "aws-credentials", "aws-credential-selection":
		return "项目 AWS 凭据", project
	case "environments":
		environment := valueAt(segments, 3)
		if len(segments) <= 4 {
			return "项目环境", joinTarget(project, environment)
		}
		switch segments[4] {
		case "tls-certificates":
			return "TLS 证书", joinTarget(project, environment, valueAt(segments, 5))
		case "data-service-credentials":
			return "数据服务凭据", joinTarget(project, environment, valueAt(segments, 5))
		case "credentials":
			return "环境访问凭据", joinTarget(project, environment, valueAt(segments, 5))
		case "static-cdns":
			return "静态资源 CDN", joinTarget(project, environment, valueAt(segments, 5), valueAt(segments, 7))
		case "kubernetes":
			if valueAt(segments, 5) == "ingresses" {
				return "Ingress", joinTarget(project, environment, valueAt(segments, 6), valueAt(segments, 7))
			}
		case "alerting":
			return "告警配置", joinTarget(project, environment, valueAt(segments, 5), valueAt(segments, 6))
		case "cicd":
			return "环境 CI/CD", joinTarget(project, environment, valueAt(segments, 5))
		}
		return "项目环境", joinTarget(project, environment)
	case "cicd":
		kind := valueAt(segments, 3)
		target := joinTarget(project, valueAt(segments, 4))
		label := map[string]string{
			"connections":           "CI/CD 连接",
			"credentials":           "CI/CD 凭据",
			"repositories":          "CI/CD 仓库",
			"jobs":                  "CI/CD 任务",
			"builds":                "CI/CD 构建",
			"delivery":              "GitLab 交付配置",
			"ecr":                   "ECR 仓库",
			"notification-channels": "通知渠道",
		}[kind]
		if label == "" {
			label = "CI/CD 配置"
		}
		return label, target
	default:
		return "项目", project
	}
}

func segmentLabel(value string) string {
	if value == "" {
		return "平台"
	}
	return strings.ReplaceAll(value, "-", " ")
}

func valueAt(values []string, index int) string {
	if index < 0 || index >= len(values) {
		return ""
	}
	return strings.TrimSpace(values[index])
}

func joinTarget(values ...string) string {
	filtered := make([]string, 0, len(values))
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			filtered = append(filtered, value)
		}
	}
	return strings.Join(filtered, " / ")
}

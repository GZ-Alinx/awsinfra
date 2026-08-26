package httpapi

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/GZ-Alinx/awsinfra/internal/gitlab"
)

func TestWriteGitLabRequestErrorExplainsWAFWithoutLeakingUpstreamBody(t *testing.T) {
	response := httptest.NewRecorder()
	err := errors.Join(gitlab.ErrRequest, errors.New("HTTP 403: Cloudflare/WAF rejected SECRET-UPSTREAM-BODY"))

	writeGitLabError(response, err)

	if response.Code != http.StatusFailedDependency {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusFailedDependency)
	}
	body := response.Body.String()
	if !strings.Contains(body, "Cloudflare/WAF") || !strings.Contains(body, "API Token") {
		t.Fatalf("response is not actionable: %s", body)
	}
	if strings.Contains(body, "SECRET-UPSTREAM-BODY") {
		t.Fatalf("upstream response leaked to client: %s", body)
	}
}

func TestWriteGitLabRequestErrorExplainsGroupOwnerRequirement(t *testing.T) {
	response := httptest.NewRecorder()
	err := errors.Join(gitlab.ErrRequest, errors.New("创建业务源码只读 Group Deploy Token 失败: HTTP 403: Forbidden"))

	writeGitLabError(response, err)

	if response.Code != http.StatusFailedDependency {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusFailedDependency)
	}
	body := response.Body.String()
	if !strings.Contains(body, "Owner") || !strings.Contains(body, "api 权限") || !strings.Contains(body, "业务源码") {
		t.Fatalf("response is not actionable: %s", body)
	}
}

func TestWriteGitLabRequestErrorExplainsTokenFailure(t *testing.T) {
	response := httptest.NewRecorder()
	err := errors.Join(gitlab.ErrRequest, errors.New("HTTP 401: unauthorized"))

	writeGitLabError(response, err)

	if !strings.Contains(response.Body.String(), "Token 无效或已过期") {
		t.Fatalf("unexpected response: %s", response.Body.String())
	}
}

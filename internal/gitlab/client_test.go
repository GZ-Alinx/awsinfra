package gitlab

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestProjectBranchesUsesGitLabCompatiblePagination(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("User-Agent") != "OpsDeployPlatform/1.7" {
			http.Error(w, "missing platform user agent", http.StatusBadRequest)
			return
		}
		if r.URL.Path != "/api/v4/projects/42/repository/branches" || r.URL.Query().Get("per_page") != "100" || r.URL.Query().Get("page") != "1" {
			http.Error(w, "unexpected branch request", http.StatusBadRequest)
			return
		}
		if r.URL.Query().Has("sort") || r.URL.Query().Has("order_by") {
			http.Error(w, "unsupported sorting", http.StatusBadRequest)
			return
		}
		_ = json.NewEncoder(w).Encode([]map[string]any{{"name": "test", "default": true}})
	}))
	defer server.Close()
	client, err := newGitLabClient(server.URL, "token")
	if err != nil {
		t.Fatal(err)
	}
	branches, err := client.projectBranches(context.Background(), 42)
	if err != nil || len(branches) != 1 || branches[0].Name != "test" {
		t.Fatalf("branches = %#v, err=%v", branches, err)
	}
}

func TestGitLabErrorMessageCloudflare(t *testing.T) {
	response := &http.Response{StatusCode: http.StatusForbidden, Header: http.Header{
		"Server": []string{"cloudflare"},
		"Cf-Ray": []string{"abc123-SIN"},
	}}
	message := gitLabErrorMessage(response, []byte("<!DOCTYPE html><title>Attention Required! | Cloudflare</title>"))
	if !strings.Contains(message, "Cloudflare/WAF") || !strings.Contains(message, "abc123-SIN") || strings.Contains(message, "<!DOCTYPE") {
		t.Fatalf("unexpected diagnostic: %q", message)
	}
}

func TestGitLabErrorMessageDoesNotMisclassifyProxiedGitLabJSON(t *testing.T) {
	response := &http.Response{StatusCode: http.StatusForbidden, Header: http.Header{
		"Server":       []string{"cloudflare"},
		"Cf-Ray":       []string{"abc123-SIN"},
		"Content-Type": []string{"application/json"},
	}}
	message := gitLabErrorMessage(response, []byte(`{"message":"403 Forbidden"}`))
	if strings.Contains(message, "Cloudflare/WAF") || !strings.Contains(message, "403 Forbidden") {
		t.Fatalf("proxied GitLab JSON 403 was misclassified: %q", message)
	}
}

func TestGitLabErrorMessageHTML(t *testing.T) {
	response := &http.Response{StatusCode: http.StatusUnauthorized, Header: http.Header{"Content-Type": []string{"text/html"}}}
	message := gitLabErrorMessage(response, []byte("<html>sign in</html>"))
	if !strings.Contains(message, "HTML 页面") || strings.Contains(message, "<html>") {
		t.Fatalf("unexpected diagnostic: %q", message)
	}
}

func TestGroupFallsBackToExactSearchAfterGitLabDetailFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/api/v4/groups/bigdata_group":
			http.Error(w, `{"message":"500 Internal Server Error"}`, http.StatusInternalServerError)
		case r.URL.Path == "/api/v4/groups" && r.URL.Query().Get("search") == "bigdata_group":
			_ = json.NewEncoder(w).Encode([]gitLabGroup{{ID: 101, FullPath: "other"}, {ID: 102, FullPath: "bigdata_group"}})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	client, err := newGitLabClient(server.URL, "token")
	if err != nil {
		t.Fatal(err)
	}
	group, err := client.group(context.Background(), "bigdata_group")
	if err != nil || group.ID != 102 || group.FullPath != "bigdata_group" {
		t.Fatalf("group fallback = %#v, %v", group, err)
	}
}

func TestGroupDoesNotBypassAuthorizationFailure(t *testing.T) {
	searchCalled := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v4/groups" {
			searchCalled = true
		}
		http.Error(w, `{"message":"403 Forbidden"}`, http.StatusForbidden)
	}))
	defer server.Close()
	client, err := newGitLabClient(server.URL, "token")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.group(context.Background(), "ops"); err == nil || searchCalled {
		t.Fatalf("authorization failure was bypassed: err=%v search=%v", err, searchCalled)
	}
}

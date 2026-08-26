package httpapi

import (
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"ops-deploy-platform/internal/cicd"
	"ops-deploy-platform/internal/gitlab"
)

func TestGitLabWebhookServicesMatchesOnlyRegisteredJobRepository(t *testing.T) {
	job := cicd.Job{ServiceKeys: []string{"gateway", "aviator"}}
	catalog := []gitlab.ServiceSpec{
		{Key: "gateway", SourceRepository: "https://gitlab.example.com/kbp/game/gateway.git"},
		{Key: "aviator", SourceRepository: "https://gitlab.example.com/kbp/game/aviator.git"},
		{Key: "unrelated", SourceRepository: "https://gitlab.example.com/kbp/game/gateway.git"},
	}
	got := gitLabWebhookServices(job, catalog, []string{"https://gitlab.example.com/kbp/game/gateway"})
	if want := []string{"gateway"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("gitLabWebhookServices() = %#v, want %#v", got, want)
	}
	if got := gitLabWebhookServices(job, catalog, []string{"https://gitlab.example.com/kbp/game/other.git"}); len(got) != 0 {
		t.Fatalf("unregistered repository matched services: %#v", got)
	}
}

func TestNormalizeGitRepositoryPreservesNonDefaultEndpoint(t *testing.T) {
	tests := map[string]string{
		"https://GitLab.Example.com/group/service.git/":           "gitlab.example.com/group/service",
		"https://gitlab.example.com/group/service.git":            "gitlab.example.com/group/service",
		"https://gitlab.example.com:8443/group/service":           "gitlab.example.com:8443/group/service",
		"https://user:token@gitlab.example.com/group/service.git": "",
		"not-a-url": "",
	}
	for input, want := range tests {
		if got := normalizeGitRepository(input); got != want {
			t.Errorf("normalizeGitRepository(%q) = %q, want %q", input, got, want)
		}
	}
	if normalizeGitRepository("https://gitlab.example.com:8443/group/service.git") == normalizeGitRepository("https://gitlab.example.com/group/service.git") {
		t.Fatal("repositories on different GitLab ports must not match")
	}
}

func TestGitLabWebhookDeliveryIDIsStableAndBounded(t *testing.T) {
	payload := gitLabPushWebhook{Ref: "refs/heads/main", After: strings.Repeat("a", 40)}
	request := httptest.NewRequest("POST", "/", nil)
	request.Header.Set("X-Gitlab-Event-UUID", strings.Repeat("event", 100))
	first := gitLabWebhookDeliveryID(request, payload, []string{"https://gitlab.example.com/group/service.git"})
	second := gitLabWebhookDeliveryID(request, payload, []string{"https://gitlab.example.com/group/service.git"})
	if first != second || len(first) > 64 {
		t.Fatalf("delivery ID is unstable or unbounded: %q / %q", first, second)
	}
	request.Header.Del("X-Gitlab-Event-UUID")
	fallback := gitLabWebhookDeliveryID(request, payload, []string{"https://gitlab.example.com/group/service.git"})
	if fallback == "" || len(fallback) != 64 {
		t.Fatalf("fallback delivery ID = %q", fallback)
	}
}

func TestAllZeroGitSHARecognizesBranchDeletion(t *testing.T) {
	if !allZeroGitSHA(strings.Repeat("0", 40)) {
		t.Fatal("all-zero SHA was not recognized as a branch deletion")
	}
	if allZeroGitSHA(strings.Repeat("a", 40)) || allZeroGitSHA("") {
		t.Fatal("normal commit SHA was treated as a branch deletion")
	}
}

package httpapi

import "testing"

func TestParseGitRelayPath(t *testing.T) {
	tests := []struct {
		value, kind, service, operation string
		ok                              bool
	}{
		{value: "jenkinsfiles.git/info/refs", kind: "jenkinsfiles", operation: "info/refs", ok: true},
		{value: "manifests.git/git-upload-pack", kind: "manifests", operation: "git-upload-pack", ok: true},
		{value: "source/go-smoke.git/info/refs", kind: "source", service: "go-smoke", operation: "info/refs", ok: true},
		{value: "source/../git-upload-pack"},
		{value: "arbitrary/repository.git/info/refs"},
	}
	for _, test := range tests {
		kind, service, operation, ok := parseGitRelayPath(test.value)
		if kind != test.kind || service != test.service || operation != test.operation || ok != test.ok {
			t.Fatalf("parseGitRelayPath(%q) = %q, %q, %q, %t", test.value, kind, service, operation, ok)
		}
	}
}

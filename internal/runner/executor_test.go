package runner

import (
	"bytes"
	"strings"
	"testing"
)

func TestCommandFailureExcerptKeepsProviderErrorsAndRedactsSecrets(t *testing.T) {
	input := strings.Repeat("plan output\n", 80) + `
Error: cannot re-use a name that is still in use

  with helm_release.consul[0]
password = "should-not-leak"
`
	excerpt := commandFailureExcerpt(input)
	if !strings.Contains(excerpt, "cannot re-use") || !strings.Contains(excerpt, "helm_release.consul") {
		t.Fatalf("provider error was lost: %q", excerpt)
	}
	if strings.Contains(excerpt, "should-not-leak") || !strings.Contains(excerpt, "[REDACTED]") {
		t.Fatalf("provider error was not redacted: %q", excerpt)
	}
}

func TestBoundedTailWriterKeepsOnlyRecentOutput(t *testing.T) {
	writer := &boundedTailWriter{limit: 8}
	_, _ = writer.Write([]byte("123456"))
	_, _ = writer.Write([]byte("7890"))
	if writer.String() != "34567890" {
		t.Fatalf("unexpected tail: %q", writer.String())
	}
}

func TestRedactingWriter(t *testing.T) {
	var target bytes.Buffer
	writer := NewRedactingWriter(&target)
	input := "password=super-secret authorization: Bearer abc123 normal=value\n"
	if _, err := writer.Write([]byte(input)); err != nil {
		t.Fatal(err)
	}
	if err := writer.Flush(); err != nil {
		t.Fatal(err)
	}
	result := target.String()
	if strings.Contains(result, "super-secret") || strings.Contains(result, "abc123") {
		t.Fatalf("secret was not redacted: %q", result)
	}
	if !strings.Contains(result, "normal=value") || strings.Count(result, "[REDACTED]") != 2 {
		t.Fatalf("unexpected redacted output: %q", result)
	}
}

func TestRedactingWriterAcrossWrites(t *testing.T) {
	var target bytes.Buffer
	writer := NewRedactingWriter(&target)
	for _, chunk := range []string{"AWS_SECRET_ACCESS_", "KEY=must-not-escape", "\nnormal=visible"} {
		if _, err := writer.Write([]byte(chunk)); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Flush(); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(target.String(), "must-not-escape") || !strings.Contains(target.String(), "normal=visible") {
		t.Fatalf("chunked secret was not redacted: %q", target.String())
	}
}

func TestMergeEnvironmentOverridesInheritedValues(t *testing.T) {
	result := mergeEnvironment(
		[]string{"PATH=/bin", "AWS_ACCESS_KEY_ID=ambient", "AWS_PROFILE=ambient", "AWS_DEFAULT_PROFILE=ambient", "AWS_SESSION_TOKEN=ambient-token"},
		[]string{"AWS_ACCESS_KEY_ID=project", "AWS_PROFILE", "AWS_DEFAULT_PROFILE", "AWS_SESSION_TOKEN", "NEW=value"},
	)
	joined := strings.Join(result, "\n")
	if strings.Contains(joined, "ambient") {
		t.Fatalf("inherited AWS identity was not replaced: %q", joined)
	}
	for _, expected := range []string{"PATH=/bin", "AWS_ACCESS_KEY_ID=project", "NEW=value"} {
		if !strings.Contains(joined, expected) {
			t.Fatalf("missing %q in %q", expected, joined)
		}
	}
	for _, removed := range []string{"AWS_PROFILE=", "AWS_DEFAULT_PROFILE=", "AWS_SESSION_TOKEN="} {
		if strings.Contains(joined, removed) {
			t.Fatalf("removed environment key %q is still present in %q", removed, joined)
		}
	}
}

func TestMergeEnvironmentCanRestoreRemovedTemporaryCredential(t *testing.T) {
	result := mergeEnvironment(
		[]string{"AWS_SESSION_TOKEN=ambient-token"},
		[]string{"AWS_SESSION_TOKEN", "AWS_SESSION_TOKEN=project-token"},
	)
	if joined := strings.Join(result, "\n"); joined != "AWS_SESSION_TOKEN=project-token" {
		t.Fatalf("project session token was not restored: %q", joined)
	}
}

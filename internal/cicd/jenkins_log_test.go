package cicd

import (
	"strings"
	"testing"
)

func TestSanitizeJenkinsLogRemovesConsoleNotesAcrossOffset(t *testing.T) {
	annotation := "\x1b[8mha:" + strings.Repeat("sensitive-looking-base64", 30) + "==\x1b[0m"
	payload := []byte("before\n" + annotation + "after\n\x1b[31mred\x1b[0m\n")
	offset := int64(len("before\n") + len(annotation)/2)
	start := max(offset-4096, 0)
	got := sanitizeJenkinsLog(payload[start:], start, offset)
	if strings.Contains(got, "base64") || strings.Contains(got, "ha:") || strings.Contains(got, "\x1b") {
		t.Fatalf("hidden Jenkins console metadata leaked: %q", got)
	}
	if !strings.Contains(got, "after\nred\n") {
		t.Fatalf("visible log content was lost: %q", got)
	}
}

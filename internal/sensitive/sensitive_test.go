package sensitive

import (
	"strings"
	"testing"
)

func TestSanitizeRemovesInlineValuesButKeepsReferences(t *testing.T) {
	value := map[string]any{
		"password":   "plain-text",
		"secret_ref": "platform/secret/password",
		"nested":     map[string]any{"api_key": "abc123", "token_enabled": true},
	}
	paths := Sanitize(value)
	if len(paths) != 2 || value["password"] != nil || value["secret_ref"] == nil {
		t.Fatalf("unexpected sanitized value=%#v paths=%#v", value, paths)
	}
	if value["nested"].(map[string]any)["token_enabled"] != true {
		t.Fatal("non-secret token setting was removed")
	}
}

func TestRedactText(t *testing.T) {
	accessKey := "AKIA" + "1234567890ABCDEF"
	value := RedactText("AWS_ACCESS_KEY_ID=" + accessKey + " authorization: Bearer secret-token")
	if strings.Contains(value, accessKey) || strings.Contains(value, "secret-token") {
		t.Fatalf("sensitive value was not redacted: %q", value)
	}
}

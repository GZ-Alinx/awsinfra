package projectarchive

import (
	"strings"
	"testing"
)

func TestSanitizeYAMLRedactsSecretAndEmbeddedConfiguration(t *testing.T) {
	source := []byte(`apiVersion: v1
kind: ConfigMap
metadata:
  name: loki
data:
  config.yaml: |
    storage:
      s3:
        secret_access_key: supersecretpassword
---
apiVersion: v1
kind: Secret
metadata:
  name: credentials
data:
  password: c2VjcmV0
`)
	result := string(sanitizeYAML(source, true))
	if strings.Contains(result, "supersecretpassword") || strings.Contains(result, "c2VjcmV0") {
		t.Fatalf("sensitive value leaked:\n%s", result)
	}
	if !strings.Contains(result, "secret_access_key: __REDACTED__") || !strings.Contains(result, "__REDACTED__") {
		t.Fatalf("redaction marker missing:\n%s", result)
	}
}

func TestSanitizeYAMLPreservesNonSecretSchedulingFields(t *testing.T) {
	source := []byte("automountServiceAccountToken: false\npasswordKey: admin-password\npassword: live-secret\n")
	result := string(sanitizeYAML(source, false))
	if !strings.Contains(result, "automountServiceAccountToken: false") || !strings.Contains(result, "passwordKey: admin-password") {
		t.Fatalf("non-secret fields changed:\n%s", result)
	}
	if strings.Contains(result, "live-secret") {
		t.Fatalf("password was not redacted:\n%s", result)
	}
}

func TestSanitizeYAMLRedactsAWSAccessKeyEnvironmentValue(t *testing.T) {
	accessKey := "AKIA" + "1234567890ABCDEF"
	source := []byte("env:\n  - name: AWS_ACCESS_KEY_ID\n    value: " + accessKey + "\n")
	result := string(sanitizeYAML(source, true))
	if strings.Contains(result, accessKey) || !strings.Contains(result, "value: __REDACTED__") {
		t.Fatalf("AWS access key id leaked:\n%s", result)
	}
}

func TestKnownSecretIsRemovedFromGeneratedShellCommand(t *testing.T) {
	values := []byte("storage:\n  secretKey: supersecretpassword\n")
	manifest := []byte("command: |\n  echo supersecretpassword > /tmp/credentials\n")
	result := redactKnownValues(sanitizeYAML(manifest, true), collectSensitiveYAML(values))
	if strings.Contains(string(result), "supersecretpassword") {
		t.Fatalf("known secret leaked into generated command:\n%s", result)
	}
}

func TestSafeJoinRejectsTraversal(t *testing.T) {
	if _, err := safeJoin(t.TempDir(), "../../secret"); err == nil {
		t.Fatal("expected traversal to be rejected")
	}
}

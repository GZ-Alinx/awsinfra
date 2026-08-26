package statebackend

import (
	"errors"
	"testing"
)

func TestValidateInputAcceptsCentralStatePath(t *testing.T) {
	err := validateInput(Input{Bucket: "ops-deploy-state-123456789012", Region: "ap-southeast-1", KeyPrefix: "ops-deploy", AccessKeyID: "AKIAEXAMPLE123456", SecretAccessKey: "example-secret-value"})
	if err != nil {
		t.Fatal(err)
	}
}

func TestValidateInputRejectsUnsafeBucketAndPrefix(t *testing.T) {
	for _, input := range []Input{
		{Bucket: "S3://Bucket", Region: "ap-south-1", KeyPrefix: "ops-deploy"},
		{Bucket: "127.0.0.1", Region: "ap-south-1", KeyPrefix: "ops-deploy"},
		{Bucket: "valid-state-bucket", Region: "ap-south-1", KeyPrefix: "../state"},
	} {
		if err := validateInput(input); !errors.Is(err, ErrInvalidConfig) {
			t.Fatalf("validateInput(%#v) error = %v", input, err)
		}
	}
}

func TestStateOutputScopeCannotEscapeProjectPrefix(t *testing.T) {
	for _, value := range []string{"../other", "demo/test", "demo\\test", "项目", ""} {
		if validStateScope(value) {
			t.Fatalf("unsafe state scope %q was accepted", value)
		}
	}
	for _, value := range []string{"demo", "demo-test", "test1"} {
		if !validStateScope(value) {
			t.Fatalf("valid state scope %q was rejected", value)
		}
	}
}

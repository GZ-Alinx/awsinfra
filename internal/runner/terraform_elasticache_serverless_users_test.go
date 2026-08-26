package runner

import (
	"os"
	"strings"
	"testing"
)

func TestTerraformElastiCacheServerlessUsesAuthenticatedDefaultUser(t *testing.T) {
	payload, err := os.ReadFile("../../terraform/infra/data-services.tf")
	if err != nil {
		t.Fatal(err)
	}
	source := string(payload)
	for _, required := range []string{
		`resource "aws_elasticache_user" "serverless_default"`,
		`user_name     = "default"`,
		`passwords = [random_password.elasticache[0].result]`,
		`aws_elasticache_user.serverless_default[0].user_id`,
	} {
		if !strings.Contains(source, required) {
			t.Fatalf("serverless Redis default-user requirement is missing %q", required)
		}
	}
	if strings.Contains(source, `user_ids      = ["default"]`) {
		t.Fatal("the unauthenticated AWS built-in default user must not be used")
	}
}

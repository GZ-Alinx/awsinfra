package runner

import (
	"os"
	"strings"
	"testing"
)

func TestTerraformDatabaseCredentialModesAreMutuallyExclusive(t *testing.T) {
	payload, err := os.ReadFile("../../terraform/infra/data-services.tf")
	if err != nil {
		t.Fatal(err)
	}
	source := string(payload)
	for _, expression := range []string{
		"manage_master_user_password   = local.rds_credentials_self_managed ? null : true",
		"manage_master_user_password   = local.aurora_credentials_self_managed ? null : true",
	} {
		if !strings.Contains(source, expression) {
			t.Fatalf("missing mutually exclusive credential expression %q", expression)
		}
	}
	for _, unsafe := range []string{
		"manage_master_user_password   = !local.rds_credentials_self_managed",
		"manage_master_user_password   = !local.aurora_credentials_self_managed",
	} {
		if strings.Contains(source, unsafe) {
			t.Fatalf("explicit false still conflicts with password: %q", unsafe)
		}
	}
}

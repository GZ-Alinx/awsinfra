package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRenderInitialEnvironmentContainsNoPlaintextAdminPasswordAndKeepsDSNConsistent(t *testing.T) {
	datastore := datastoreInitConfig{
		Mode: datastoreModeLocal, MySQLDSN: "ops:mysql-random@tcp(127.0.0.1:13306)/ops_deploy?parseTime=true",
		MySQLPassword: "mysql-random", MySQLRootPassword: "root-random", RedisAddress: "127.0.0.1:6379", RedisPassword: "redis-random",
	}
	payload := string(renderInitialEnvironment("$argon2id$encoded", datastore, "base64-key"))
	for _, expected := range []string{
		`OPS_DEPLOY_PASSWORD_HASH="$argon2id$encoded"`,
		`OPS_MYSQL_PASSWORD="mysql-random"`,
		`OPS_MYSQL_DSN="ops:mysql-random@tcp(127.0.0.1:13306)/ops_deploy?parseTime=true"`,
		`OPS_DEPLOY_CREDENTIAL_KEY="base64-key"`,
		`OPS_DEPLOY_REDIS_DATABASE="0"`,
	} {
		if !strings.Contains(payload, expected) {
			t.Fatalf("generated environment is missing %q", expected)
		}
	}
	if strings.Contains(payload, "admin-password") {
		t.Fatal("generated environment contains a plaintext admin password")
	}
}

func TestRenderExternalEnvironmentOmitsLocalComposePasswords(t *testing.T) {
	datastore := datastoreInitConfig{
		Mode: datastoreModeExternal, MySQLDSN: "ops:secret@tcp(mysql.example:3306)/ops_deploy?parseTime=true",
		RedisAddress: "redis.example:6379", RedisPassword: "redis-secret", RedisDatabase: 3,
	}
	payload := string(renderInitialEnvironment("hash", datastore, "key"))
	if strings.Contains(payload, "OPS_MYSQL_ROOT_PASSWORD") || strings.Contains(payload, "OPS_MYSQL_PASSWORD=") {
		t.Fatal("external datastore output contains local Docker passwords")
	}
	for _, expected := range []string{`OPS_DEPLOY_REDIS_ADDRESS="redis.example:6379"`, `OPS_DEPLOY_REDIS_DATABASE="3"`} {
		if !strings.Contains(payload, expected) {
			t.Fatalf("generated environment is missing %q", expected)
		}
	}
}

func TestEnvironmentAssignmentsRoundTripSpecialCharacters(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".env")
	want := `p@ss word#quote'and"slash\\value`
	if err := os.WriteFile(path, []byte(envAssignment("OPS_SPECIAL_VALUE", want)+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Unsetenv("OPS_SPECIAL_VALUE") })
	if err := loadDotEnv(path); err != nil {
		t.Fatal(err)
	}
	if got := os.Getenv("OPS_SPECIAL_VALUE"); got != want {
		t.Fatalf("round trip = %q, want %q", got, want)
	}
}

func TestLoadDotEnvSupportsDSNAndPreservesProcessEnvironment(t *testing.T) {
	t.Setenv("EXISTING_VALUE", "from-process")
	path := filepath.Join(t.TempDir(), ".env")
	content := "# local dependencies\nOPS_TEST_DSN=ops:password@tcp(127.0.0.1:3306)/ops?parseTime=true&charset=utf8mb4\nOPS_TEST_HASH='$argon2id$v=19$example'\nEXISTING_VALUE=from-file\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = os.Unsetenv("OPS_TEST_DSN")
		_ = os.Unsetenv("OPS_TEST_HASH")
	})

	if err := loadDotEnv(path); err != nil {
		t.Fatal(err)
	}
	if got := os.Getenv("OPS_TEST_DSN"); got != "ops:password@tcp(127.0.0.1:3306)/ops?parseTime=true&charset=utf8mb4" {
		t.Fatalf("DSN = %q", got)
	}
	if got := os.Getenv("OPS_TEST_HASH"); got != "$argon2id$v=19$example" {
		t.Fatalf("hash = %q", got)
	}
	if got := os.Getenv("EXISTING_VALUE"); got != "from-process" {
		t.Fatalf("existing value was overwritten: %q", got)
	}
}

func TestLoadDotEnvIgnoresMissingFile(t *testing.T) {
	if err := loadDotEnv(filepath.Join(t.TempDir(), "missing.env")); err != nil {
		t.Fatal(err)
	}
}

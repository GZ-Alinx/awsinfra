package appconfig

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestLoadAppliesDefaultsAndResolvesPaths(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	content := []byte(`
server:
  listen_address: 127.0.0.1:9090
security:
  admin_username: admin
  password_hash_env: TEST_PASSWORD_HASH
paths:
  repository_root: repo
components:
  - key: etcd
    config_path: components.etcd.enabled
`)
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Server.ReadTimeout != 30*time.Second || cfg.Jobs.Timeout != 3*time.Hour || cfg.Security.SessionTTL != 8*time.Hour {
		t.Fatalf("unexpected duration defaults: read=%s job=%s session=%s", cfg.Server.ReadTimeout, cfg.Jobs.Timeout, cfg.Security.SessionTTL)
	}
	wantRoot := filepath.Join(dir, "repo")
	if cfg.Paths.RepositoryRoot != wantRoot || cfg.Paths.EnvironmentsDir != filepath.Join(wantRoot, "environments") {
		t.Fatalf("paths were not resolved against config location: %#v", cfg.Paths)
	}
	if cfg.Tools.Terraform != "terraform" || cfg.Jobs.MaxParallel != 1 {
		t.Fatalf("defaults not applied: tools=%#v jobs=%#v", cfg.Tools, cfg.Jobs)
	}
}

func TestLoadRejectsRemoteListenerWithoutTokenEnvironment(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte("server:\n  listen_address: 0.0.0.0:8080\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil {
		t.Fatal("expected remote listener validation error")
	}
}

func TestLoadAppliesKubernetesRuntimeOverrides(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte(`
server:
  listen_address: 127.0.0.1:8080
security:
  cookie_secure: false
datastore:
  redis:
    address: 127.0.0.1:6379
`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("OPS_DEPLOY_LISTEN_ADDRESS", "0.0.0.0:8080")
	t.Setenv("OPS_DEPLOY_REDIS_ADDRESS", "ops-deploy-redis:6379")
	t.Setenv("OPS_DEPLOY_REDIS_DATABASE", "4")
	t.Setenv("OPS_DEPLOY_COOKIE_SECURE", "true")
	t.Setenv("OPS_DEPLOY_EXTERNAL_ORIGIN", "https://ops.example.com")
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Server.ListenAddress != "0.0.0.0:8080" || cfg.DataStore.Redis.Address != "ops-deploy-redis:6379" || cfg.DataStore.Redis.Database != 4 {
		t.Fatalf("runtime addresses were not applied: %#v", cfg)
	}
	if !cfg.Security.CookieSecure || cfg.Security.ExternalOrigin != "https://ops.example.com" {
		t.Fatalf("runtime security overrides were not applied: %#v", cfg.Security)
	}
}

func TestLoadRejectsInvalidCookieSecureOverride(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte("server:\n  listen_address: 127.0.0.1:8080\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("OPS_DEPLOY_COOKIE_SECURE", "sometimes")
	if _, err := Load(path); err == nil {
		t.Fatal("expected invalid boolean override to be rejected")
	}
}

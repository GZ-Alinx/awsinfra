package selfdeploy

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-sql-driver/mysql"
	"gopkg.in/yaml.v3"
)

func TestExampleConfigAndManifestRender(t *testing.T) {
	config, err := LoadConfig(filepath.Join("..", "..", "deploy", "kubernetes", "deploy.example.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := New(config, io.Discard, io.Discard).RenderManifest("123456789012.dkr.ecr.ap-southeast-1.amazonaws.com/ops-deploy-platform:test-1")
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(manifest, []byte("kind: Ingress")) {
		t.Fatal("safe example configuration must not expose an ingress by default")
	}
	if !bytes.Contains(manifest, []byte("namespace: \"ops-deploy-system\"")) {
		t.Fatal("target namespace is missing from rendered manifest")
	}
	if bytes.Contains(manifest, []byte("name: MYSQL_PWD")) {
		t.Fatal("MYSQL_PWD must not be injected globally because it breaks the official MySQL first-run initialization")
	}
	if !bytes.Contains(manifest, []byte("SELECT 1")) {
		t.Fatal("MySQL probes must execute an authenticated query")
	}
	if !bytes.Contains(manifest, []byte("name: wait-for-datastores")) {
		t.Fatal("platform pod must wait for MySQL and Redis before starting")
	}

	decoder := yaml.NewDecoder(bytes.NewReader(manifest))
	kinds := map[string]int{}
	for {
		var document map[string]any
		err := decoder.Decode(&document)
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("rendered manifest is not valid YAML: %v", err)
		}
		if kind, ok := document["kind"].(string); ok {
			kinds[kind]++
		}
	}
	for _, kind := range []string{"Namespace", "ConfigMap", "Service", "StatefulSet", "PersistentVolumeClaim", "Deployment", "NetworkPolicy"} {
		if kinds[kind] == 0 {
			t.Fatalf("rendered manifest is missing %s", kind)
		}
	}
}

func TestBuildTLSSecretDocumentValidatesCertificateHost(t *testing.T) {
	now := time.Date(2026, 7, 17, 0, 0, 0, 0, time.UTC)
	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	certificateDER, err := x509.CreateCertificate(rand.Reader, &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "*.example.com"},
		DNSNames:     []string{"*.example.com", "example.com"},
		NotBefore:    now.Add(-time.Hour),
		NotAfter:     now.Add(24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
	}, &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "*.example.com"},
		DNSNames:     []string{"*.example.com", "example.com"},
		NotBefore:    now.Add(-time.Hour),
		NotAfter:     now.Add(24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
	}, &privateKey.PublicKey, privateKey)
	if err != nil {
		t.Fatal(err)
	}
	source, err := json.Marshal(kubernetesSecret{
		Type: "kubernetes.io/tls",
		Data: map[string]string{
			"tls.crt": base64.StdEncoding.EncodeToString(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certificateDER})),
			"tls.key": base64.StdEncoding.EncodeToString(pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(privateKey)})),
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	document, err := buildTLSSecretDocument(source, "target", "wildcard", "ops.example.com", now)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(document, []byte(`"namespace":"target"`)) || !bytes.Contains(document, []byte(`"name":"wildcard"`)) {
		t.Fatalf("target TLS Secret metadata is missing: %s", document)
	}
	if _, err := buildTLSSecretDocument(source, "target", "wildcard", "nested.ops.example.com", now); err == nil {
		t.Fatal("wildcard certificate must not cover a multi-level hostname")
	}
	mismatchedKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	var mismatched kubernetesSecret
	if err := json.Unmarshal(source, &mismatched); err != nil {
		t.Fatal(err)
	}
	mismatched.Data["tls.key"] = base64.StdEncoding.EncodeToString(pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(mismatchedKey)}))
	mismatchedPayload, err := json.Marshal(mismatched)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := buildTLSSecretDocument(mismatchedPayload, "target", "wildcard", "ops.example.com", now); err == nil || !strings.Contains(err.Error(), "do not match") {
		t.Fatalf("mismatched TLS private key must be rejected, got %v", err)
	}
}

func TestIngressExternalOriginMustMatchHost(t *testing.T) {
	config, err := LoadConfig(filepath.Join("..", "..", "deploy", "kubernetes", "deploy.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	config.Kubernetes.ExternalOrigin = "https://wrong.example.com"
	if err := config.Validate(); err == nil || !strings.Contains(err.Error(), "must exactly match") {
		t.Fatalf("expected external origin mismatch to be rejected, got %v", err)
	}
}

func TestFindIngressHostConflictsIgnoresOnlyManagedIngress(t *testing.T) {
	payload := []byte(`{
  "items": [
    {"metadata":{"namespace":"target","name":"ops-deploy-platform"},"spec":{"rules":[{"host":"ops.example.com"}]}},
    {"metadata":{"namespace":"higress-system","name":"manual","annotations":{"higress.io/destination":"other.default.svc:80"}},"spec":{"rules":[{"host":"OPS.EXAMPLE.COM."}]}},
    {"metadata":{"namespace":"other","name":"unrelated"},"spec":{"rules":[{"host":"other.example.com"}]}}
  ]
}`)
	conflicts, err := findIngressHostConflicts(payload, "ops.example.com", "target", "ops-deploy-platform")
	if err != nil {
		t.Fatal(err)
	}
	if len(conflicts) != 1 || !strings.Contains(conflicts[0], "higress-system/manual") || !strings.Contains(conflicts[0], "other.default.svc:80") {
		t.Fatalf("unexpected conflicts: %#v", conflicts)
	}
}

func TestFindIngressDestinationAliases(t *testing.T) {
	payload := []byte(`{
  "items": [
    {"metadata":{"namespace":"higress-system","name":"old","annotations":{"higress.io/destination":"ops-deploy-platform.target.svc.cluster.local:80"}},"spec":{"rules":[{"host":"old.example.com"}]}},
    {"metadata":{"namespace":"higress-system","name":"current","annotations":{"higress.io/destination":"ops-deploy-platform.target.svc.cluster.local:80"}},"spec":{"rules":[{"host":"new.example.com"}]}},
    {"metadata":{"namespace":"other","name":"unrelated","annotations":{"higress.io/destination":"other.target.svc.cluster.local:80"}},"spec":{"rules":[{"host":"other.example.com"}]}}
  ]
}`)
	aliases, err := findIngressDestinationAliases(payload, "new.example.com", "ops-deploy-platform.target.svc.cluster.local:80", "target", "ops-deploy-platform")
	if err != nil {
		t.Fatal(err)
	}
	if len(aliases) != 1 || !strings.Contains(aliases[0], "higress-system/old") || !strings.Contains(aliases[0], "old.example.com") {
		t.Fatalf("unexpected aliases: %#v", aliases)
	}
}

func TestValidateHealthPayload(t *testing.T) {
	if err := validateHealthPayload([]byte(`{"status":"ok","dependencies":{"mysql":"up"}}`)); err != nil {
		t.Fatal(err)
	}
	if err := validateHealthPayload([]byte(`{"status":"degraded"}`)); err == nil {
		t.Fatal("degraded health response must be rejected")
	}
	if err := validateHealthPayload([]byte(`<html>gateway error</html>`)); err == nil {
		t.Fatal("non-JSON health response must be rejected")
	}
}

func TestRetryHealthCheckRecoversFromTransientFailure(t *testing.T) {
	attempts := 0
	err := retryHealthCheck(context.Background(), 4, 0, func() error {
		attempts++
		if attempts < 3 {
			return errors.New("transient gateway failure")
		}
		return nil
	})
	if err != nil || attempts != 3 {
		t.Fatalf("transient health failure was not retried: attempts=%d err=%v", attempts, err)
	}
}

func TestLoadConfigRejectsUnknownField(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "Dockerfile"), []byte("FROM scratch\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "manifest.tmpl"), []byte("apiVersion: v1\nkind: Namespace\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	payload := `
cluster:
  name: example
  region: ap-south-1
registry:
  repository: example
build:
  context: .
  dockerfile: Dockerfile
kubernetes:
  namespace: example
  manifest_template: manifest.tmpl
  storage_class: gp3
  platform_storage: 1Gi
  mysql_storage: 1Gi
  redis_storage: 1Gi
unexpected: true
`
	path := filepath.Join(dir, "deploy.yaml")
	if err := os.WriteFile(path, []byte(payload), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadConfig(path); err == nil || !strings.Contains(err.Error(), "field unexpected") {
		t.Fatalf("expected strict YAML error, got %v", err)
	}
}

func TestLoadSecretsBuildsEscapedMySQLDSN(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "secrets.env")
	credentialKey := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{7}, 32))
	mysqlPassword := "strong:p@ss/word?#123"
	payload := strings.Join([]string{
		"OPS_DEPLOY_PASSWORD_HASH='$argon2id$v=19$m=65536,t=3,p=2$c2FsdA$aGFzaA'",
		"OPS_DEPLOY_CREDENTIAL_KEY='" + credentialKey + "'",
		"OPS_MYSQL_PASSWORD='" + mysqlPassword + "'",
		"OPS_MYSQL_ROOT_PASSWORD='different-root-password-123'",
		"OPS_REDIS_PASSWORD='different-redis-password-123'",
	}, "\n")
	if err := os.WriteFile(path, []byte(payload), 0o600); err != nil {
		t.Fatal(err)
	}
	values, err := loadSecrets(path)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := mysql.ParseDSN(values["OPS_MYSQL_DSN"])
	if err != nil {
		t.Fatalf("generated MySQL DSN is invalid: %v", err)
	}
	if parsed.Passwd != mysqlPassword || parsed.Addr != "ops-deploy-mysql:3306" || parsed.DBName != "ops_deploy" {
		t.Fatalf("generated MySQL DSN does not preserve the configured values: %#v", parsed)
	}
}

func TestLoadSecretsRequiresPrivateFileMode(t *testing.T) {
	path := filepath.Join(t.TempDir(), "secrets.env")
	if err := os.WriteFile(path, []byte("placeholder=true\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := loadSecrets(path); err == nil || !strings.Contains(err.Error(), "0600") {
		t.Fatalf("expected file mode error, got %v", err)
	}
}

func TestImageTagValidation(t *testing.T) {
	for _, valid := range []string{"v1.2.3", "build-20260717T010203Z", "release_candidate.1"} {
		if !imageTagPattern.MatchString(valid) {
			t.Fatalf("expected valid image tag %q", valid)
		}
	}
	for _, invalid := range []string{"", ".leading-dot", "bad/tag", "bad:tag", strings.Repeat("a", 129)} {
		if imageTagPattern.MatchString(invalid) {
			t.Fatalf("expected invalid image tag %q", invalid)
		}
	}
}

func TestValidateImageTagAvailability(t *testing.T) {
	if err := validateImageTagAvailability("v1", true, false); err == nil || !strings.Contains(err.Error(), "禁止覆盖") {
		t.Fatalf("existing immutable tag was not rejected before build: %v", err)
	}
	if err := validateImageTagAvailability("v2", false, true); err == nil || !strings.Contains(err.Error(), "不存在") {
		t.Fatalf("missing skip-build tag was not rejected: %v", err)
	}
	if err := validateImageTagAvailability("v2", false, false); err != nil {
		t.Fatalf("new build tag was rejected: %v", err)
	}
	if err := validateImageTagAvailability("v1", true, true); err != nil {
		t.Fatalf("existing skip-build tag was rejected: %v", err)
	}
}

func TestPatchKubeconfigEndpointPreservesTLSHostname(t *testing.T) {
	path := filepath.Join(t.TempDir(), "kubeconfig")
	payload := []byte(`apiVersion: v1
kind: Config
clusters:
  - name: target-cluster
    cluster:
      certificate-authority-data: dGVzdA==
      server: https://target.eks.amazonaws.com
contexts:
  - name: ops-deploy-platform
    context:
      cluster: target-cluster
      user: target-user
current-context: ops-deploy-platform
users:
  - name: target-user
    user:
      token: redacted
`)
	if err := os.WriteFile(path, payload, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := patchKubeconfigEndpoint(path, "ops-deploy-platform", "target.eks.amazonaws.com", "54.255.20.8"); err != nil {
		t.Fatal(err)
	}
	updated, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{"server: https://54.255.20.8", "tls-server-name: target.eks.amazonaws.com", "token: redacted"} {
		if !bytes.Contains(updated, []byte(expected)) {
			t.Fatalf("patched kubeconfig is missing %q:\n%s", expected, updated)
		}
	}
}

func TestEndpointRequiresPublicResolutionForDNSFailureOrFakeIP(t *testing.T) {
	if !endpointRequiresPublicResolution(nil, errors.New("no such host")) {
		t.Fatal("DNS lookup failure must fall back to encrypted public DNS")
	}
	if !endpointRequiresPublicResolution(nil, nil) {
		t.Fatal("empty DNS response must fall back to encrypted public DNS")
	}
	if !endpointRequiresPublicResolution([]net.IPAddr{{IP: net.ParseIP("198.18.0.10")}}, nil) {
		t.Fatal("Fake-IP-only DNS response must fall back to encrypted public DNS")
	}
	if endpointRequiresPublicResolution([]net.IPAddr{{IP: net.ParseIP("54.255.20.8")}}, nil) {
		t.Fatal("a public EKS address must not require encrypted DNS fallback")
	}
}

func TestEphemeralDockerConfigIsPrivateAndRemoved(t *testing.T) {
	deployer := New(&Config{}, io.Discard, io.Discard)
	cleanup, err := deployer.useEphemeralDockerConfig()
	if err != nil {
		t.Fatal(err)
	}
	directory := deployer.dockerConfig
	info, err := os.Stat(filepath.Join(directory, "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("temporary Docker config mode is %o, expected 0600", info.Mode().Perm())
	}
	cleanup()
	if deployer.dockerConfig != "" {
		t.Fatal("temporary Docker config must be detached after cleanup")
	}
	if _, err := os.Stat(directory); !os.IsNotExist(err) {
		t.Fatalf("temporary Docker config was not removed: %v", err)
	}
}

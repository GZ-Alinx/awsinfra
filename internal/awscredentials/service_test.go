package awscredentials

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"errors"
	"os"
	"strings"
	"testing"
)

type memoryStore struct {
	record   StoredCredential
	found    bool
	selected string
}

func (s *memoryStore) ListAWSCredentials(context.Context) ([]StoredCredential, error) {
	if !s.found {
		return nil, nil
	}
	return []StoredCredential{s.record}, nil
}
func (s *memoryStore) GetAWSCredential(_ context.Context, key string) (StoredCredential, error) {
	if !s.found || (s.record.Key != "" && s.record.Key != key) {
		return StoredCredential{}, os.ErrNotExist
	}
	return s.record, nil
}
func (s *memoryStore) GetSelectedAWSCredential(context.Context, string) (StoredCredential, error) {
	if !s.found {
		return StoredCredential{}, os.ErrNotExist
	}
	return s.record, nil
}
func (s *memoryStore) SaveAWSCredential(_ context.Context, record StoredCredential) error {
	s.record, s.found = record, true
	return nil
}
func (s *memoryStore) DeleteAWSCredential(context.Context, string) error {
	s.found = false
	return nil
}
func (s *memoryStore) SelectProjectAWSCredential(_ context.Context, _, key string) error {
	s.selected = key
	return nil
}

func testService(t *testing.T, store Store) *Service {
	t.Helper()
	block, err := aes.NewCipher(make([]byte, 32))
	if err != nil {
		t.Fatal(err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		t.Fatal(err)
	}
	return &Service{store: store, aead: aead}
}

func TestMissingCredentialNeverFallsBack(t *testing.T) {
	service := testService(t, &memoryStore{})
	info, err := service.Info(context.Background(), "demo")
	if err != nil {
		t.Fatal(err)
	}
	if info.Configured || info.ProjectKey != "demo" || info.Source != "project-credential-required" {
		t.Fatalf("unexpected unbound project metadata: %#v", info)
	}
	if _, err := service.Environment(context.Background(), "demo"); !errors.Is(err, ErrCredentialNotBound) {
		t.Fatalf("missing credential error = %v", err)
	}
}

func TestSelectedCredentialMustBelongToRequestedProject(t *testing.T) {
	store := &memoryStore{record: StoredCredential{Key: "other-admin", ProjectKey: "other"}, found: true}
	service := testService(t, store)
	if _, err := service.Info(context.Background(), "demo"); !errors.Is(err, ErrCredentialMismatch) {
		t.Fatalf("info cross-project error = %v", err)
	}
	if _, err := service.Environment(context.Background(), "demo"); !errors.Is(err, ErrCredentialMismatch) {
		t.Fatalf("environment cross-project error = %v", err)
	}
	if err := service.Delete(context.Background(), "demo"); !errors.Is(err, ErrCredentialMismatch) {
		t.Fatalf("delete cross-project error = %v", err)
	}
	if !store.found {
		t.Fatal("cross-project credential was deleted")
	}
}

func TestEncryptedCredentialRoundTripAndPublicMasking(t *testing.T) {
	store := &memoryStore{}
	service := testService(t, store)
	secret := "very-secret-access-key-value"
	token := "temporary-session-token"
	secretCipher, err := service.encrypt("demo\x00secret", []byte(secret))
	if err != nil {
		t.Fatal(err)
	}
	tokenCipher, err := service.encrypt("demo\x00token", []byte(token))
	if err != nil {
		t.Fatal(err)
	}
	store.record = StoredCredential{Key: "demo-default", DisplayName: "默认 AWS 身份", ProjectKey: "demo", AccessKeyID: "TEST1234567890VALUE", SecretAccessKeyCipher: secretCipher, SessionTokenCipher: tokenCipher}
	store.found = true

	entries, err := service.Environment(context.Background(), "demo")
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(entries, "\n")
	if !strings.Contains(joined, secret) || !strings.Contains(joined, token) {
		t.Fatalf("credential did not decrypt for process use: %q", joined)
	}
	info, err := service.Info(context.Background(), "demo")
	if err != nil {
		t.Fatal(err)
	}
	if !info.Configured || !strings.HasPrefix(info.MaskedAccessKey, "TEST") || !strings.HasSuffix(info.MaskedAccessKey, "ALUE") || !strings.Contains(info.MaskedAccessKey, "****") {
		t.Fatalf("unexpected public metadata: %#v", info)
	}
	if strings.Contains(info.MaskedAccessKey, secret) || strings.Contains(info.MaskedAccessKey, token) {
		t.Fatal("public metadata leaked secret material")
	}
}

func TestCiphertextIsBoundToProjectAndField(t *testing.T) {
	service := testService(t, &memoryStore{})
	ciphertext, err := service.encrypt("demo\x00secret", []byte("secret"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = service.decrypt("other\x00secret", ciphertext); err == nil {
		t.Fatal("ciphertext decrypted under another project")
	}
	if _, err = service.decrypt("demo\x00token", ciphertext); err == nil {
		t.Fatal("ciphertext decrypted as another field")
	}
	if _, err = service.decrypt("demo\x00secret", "invalid"); err == nil {
		t.Fatal("invalid ciphertext should fail")
	}
}

func TestSelectRejectsCredentialOwnedByAnotherProject(t *testing.T) {
	store := &memoryStore{record: StoredCredential{Key: "project-a-admin", ProjectKey: "project-a"}, found: true}
	service := testService(t, store)
	if _, err := service.Select(context.Background(), "project-b", "project-a-admin"); !errors.Is(err, ErrCredentialMismatch) {
		t.Fatalf("select error = %v", err)
	}
}

func TestNormalizeCredentialKeyAcceptsUserFacingLabels(t *testing.T) {
	tests := map[string]string{
		"KBP小游戏":             "kbp",
		"Payment Prod Admin": "payment-prod-admin",
		"123 临时凭据":           "aws-123",
	}
	for input, expected := range tests {
		if actual := NormalizeCredentialKey(input); actual != expected {
			t.Errorf("NormalizeCredentialKey(%q) = %q, want %q", input, actual, expected)
		}
	}
	chineseOnly := NormalizeCredentialKey("生产部署凭据")
	if !strings.HasPrefix(chineseOnly, "aws-credential-") || !credentialKeyPattern.MatchString(chineseOnly) {
		t.Fatalf("Chinese-only credential key was not normalized: %q", chineseOnly)
	}
}

func TestCredentialValidationReturnsFieldSpecificErrors(t *testing.T) {
	service := testService(t, &memoryStore{})
	_, err := service.SaveNamedAndVerify(context.Background(), "demo", "演示凭据", "demo", "admin", Input{
		AccessKeyID: "not-an-access-key", SecretAccessKey: "long-enough-secret-key", Region: "ap-south-1",
	})
	if !errors.Is(err, ErrInvalidCredential) || !strings.Contains(err.Error(), "Access Key ID") {
		t.Fatalf("unexpected validation error: %v", err)
	}
}

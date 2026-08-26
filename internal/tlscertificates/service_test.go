package tlscertificates

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"math/big"
	"os"
	"strings"
	"testing"
	"time"
)

type memoryStore struct{ records map[string]StoredMaterial }

func (s *memoryStore) recordKey(project, environment, key string) string {
	return project + "/" + environment + "/" + key
}
func (s *memoryStore) ListTLSCertificates(_ context.Context, project, environment string) ([]StoredMaterial, error) {
	result := make([]StoredMaterial, 0)
	for _, record := range s.records {
		if record.ProjectKey == project && record.EnvironmentKey == environment {
			result = append(result, record)
		}
	}
	return result, nil
}
func (s *memoryStore) GetTLSCertificate(_ context.Context, project, environment, key string) (StoredMaterial, error) {
	record, ok := s.records[s.recordKey(project, environment, key)]
	if !ok {
		return StoredMaterial{}, os.ErrNotExist
	}
	return record, nil
}
func (s *memoryStore) SaveTLSCertificate(_ context.Context, record StoredMaterial) error {
	if s.records == nil {
		s.records = make(map[string]StoredMaterial)
	}
	now := time.Now().UTC()
	record.CreatedAt, record.UpdatedAt = now, now
	s.records[s.recordKey(record.ProjectKey, record.EnvironmentKey, record.Key)] = record
	return nil
}
func (s *memoryStore) DeleteTLSCertificate(_ context.Context, project, environment, key string) error {
	recordKey := s.recordKey(project, environment, key)
	if _, ok := s.records[recordKey]; !ok {
		return os.ErrNotExist
	}
	delete(s.records, recordKey)
	return nil
}

func newTestService(t *testing.T, store Store) *Service {
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

func certificatePair(t *testing.T, key *rsa.PrivateKey, dnsName string) (string, string) {
	t.Helper()
	now := time.Now()
	template := &x509.Certificate{
		SerialNumber: big.NewInt(now.UnixNano()), Subject: pkix.Name{CommonName: dnsName},
		DNSNames: []string{dnsName}, NotBefore: now.Add(-time.Hour), NotAfter: now.Add(24 * time.Hour),
		KeyUsage:    x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	certificate := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	privateKey := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
	return string(certificate), string(privateKey)
}

func TestEncryptedTLSMaterialRoundTrip(t *testing.T) {
	store := &memoryStore{records: make(map[string]StoredMaterial)}
	service := newTestService(t, store)
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	certificate, privateKey := certificatePair(t, key, "app.example.com")
	info, err := service.Save(context.Background(), "demo", "test", "web-tls", "admin", Input{CertificatePEM: certificate, PrivateKeyPEM: privateKey})
	if err != nil {
		t.Fatal(err)
	}
	if !info.Configured || info.Key != "web-tls" || len(info.DNSNames) != 1 || info.DNSNames[0] != "app.example.com" {
		t.Fatalf("unexpected public info: %#v", info)
	}
	record := store.records["demo/test/web-tls"]
	if strings.Contains(record.CertificateCipher, "BEGIN CERTIFICATE") || strings.Contains(record.PrivateKeyCipher, "PRIVATE KEY") || strings.Contains(record.PrivateKeyCipher, privateKey) {
		t.Fatal("stored TLS material contains plaintext")
	}
	material, err := service.Material(context.Background(), "demo", "test", "web-tls")
	if err != nil {
		t.Fatal(err)
	}
	if string(material.CertificatePEM) != certificate || string(material.PrivateKeyPEM) != privateKey {
		t.Fatal("decrypted TLS material does not match input")
	}
}

func TestTLSPrivateKeyMustMatchLeafCertificate(t *testing.T) {
	service := newTestService(t, &memoryStore{records: make(map[string]StoredMaterial)})
	certificateKey, _ := rsa.GenerateKey(rand.Reader, 2048)
	otherKey, _ := rsa.GenerateKey(rand.Reader, 2048)
	certificate, _ := certificatePair(t, certificateKey, "app.example.com")
	_, otherPrivateKey := certificatePair(t, otherKey, "other.example.com")
	_, err := service.Save(context.Background(), "demo", "test", "web-tls", "admin", Input{CertificatePEM: certificate, PrivateKeyPEM: otherPrivateKey})
	if !errors.Is(err, ErrInvalidMaterial) || !strings.Contains(err.Error(), "does not match") {
		t.Fatalf("mismatched key error = %v", err)
	}
}

func TestTLSMaterialIsBoundToProjectEnvironmentAndKey(t *testing.T) {
	store := &memoryStore{records: make(map[string]StoredMaterial)}
	service := newTestService(t, store)
	key, _ := rsa.GenerateKey(rand.Reader, 2048)
	certificate, privateKey := certificatePair(t, key, "app.example.com")
	if _, err := service.Save(context.Background(), "demo", "test", "web-tls", "admin", Input{CertificatePEM: certificate, PrivateKeyPEM: privateKey}); err != nil {
		t.Fatal(err)
	}
	record := store.records["demo/test/web-tls"]
	store.records["other/test/web-tls"] = record
	if _, err := service.Material(context.Background(), "other", "test", "web-tls"); err == nil {
		t.Fatal("cross-project ciphertext unexpectedly decrypted")
	}
}

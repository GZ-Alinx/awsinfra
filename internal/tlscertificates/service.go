package tlscertificates

import (
	"bytes"
	"context"
	"crypto"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"
	"time"

	"ops-deploy-platform/internal/appconfig"
)

var (
	ErrInvalidMaterial = errors.New("invalid TLS certificate material")
	ErrMaterialMissing = errors.New("TLS certificate material is not configured")
	namePattern        = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`)
)

const (
	maxCertificateBytes = 512 << 10
	maxPrivateKeyBytes  = 128 << 10
)

type StoredMaterial struct {
	ProjectKey        string
	EnvironmentKey    string
	Key               string
	CertificateCipher string
	PrivateKeyCipher  string
	Fingerprint       string
	Subject           string
	DNSNames          []string
	NotBefore         time.Time
	NotAfter          time.Time
	UpdatedBy         string
	CreatedAt         time.Time
	UpdatedAt         time.Time
}

type Store interface {
	ListTLSCertificates(context.Context, string, string) ([]StoredMaterial, error)
	GetTLSCertificate(context.Context, string, string, string) (StoredMaterial, error)
	SaveTLSCertificate(context.Context, StoredMaterial) error
	DeleteTLSCertificate(context.Context, string, string, string) error
}

type Input struct {
	CertificatePEM string `json:"certificate_pem"`
	PrivateKeyPEM  string `json:"private_key_pem"`
}

type PublicInfo struct {
	Key         string    `json:"key"`
	Configured  bool      `json:"configured"`
	Fingerprint string    `json:"fingerprint"`
	Subject     string    `json:"subject"`
	DNSNames    []string  `json:"dns_names"`
	NotBefore   time.Time `json:"not_before"`
	NotAfter    time.Time `json:"not_after"`
	UpdatedBy   string    `json:"updated_by"`
	UpdatedAt   time.Time `json:"updated_at"`
}

type Material struct {
	CertificatePEM []byte
	PrivateKeyPEM  []byte
}

type Service struct {
	store Store
	aead  cipher.AEAD
}

func New(config *appconfig.Config, store Store) (*Service, error) {
	encoded := strings.TrimSpace(config.CredentialKey())
	if encoded == "" {
		return nil, fmt.Errorf("%s is not set", config.Security.CredentialKeyEnv)
	}
	key, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil || len(key) != 32 {
		return nil, fmt.Errorf("%s must contain a base64-encoded 32-byte key", config.Security.CredentialKeyEnv)
	}
	block, err := aes.NewCipher(key)
	clear(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &Service{store: store, aead: aead}, nil
}

func (s *Service) List(ctx context.Context, project, environment string) ([]PublicInfo, error) {
	records, err := s.store.ListTLSCertificates(ctx, project, environment)
	if err != nil {
		return nil, err
	}
	result := make([]PublicInfo, 0, len(records))
	for _, record := range records {
		result = append(result, publicInfo(record))
	}
	return result, nil
}

func (s *Service) Save(ctx context.Context, project, environment, key, updatedBy string, input Input) (PublicInfo, error) {
	project, environment, key = normalizeScope(project, environment, key)
	if !validScope(project, environment, key) {
		return PublicInfo{}, fmt.Errorf("%w: project, environment, or certificate key is invalid", ErrInvalidMaterial)
	}
	certificatePEM := []byte(strings.TrimSpace(input.CertificatePEM) + "\n")
	privateKeyPEM := []byte(strings.TrimSpace(input.PrivateKeyPEM) + "\n")
	if len(certificatePEM) <= 1 || len(certificatePEM) > maxCertificateBytes {
		return PublicInfo{}, fmt.Errorf("%w: certificate chain must contain 1 to %d bytes", ErrInvalidMaterial, maxCertificateBytes)
	}
	if len(privateKeyPEM) <= 1 || len(privateKeyPEM) > maxPrivateKeyBytes {
		return PublicInfo{}, fmt.Errorf("%w: private key must contain 1 to %d bytes", ErrInvalidMaterial, maxPrivateKeyBytes)
	}
	leaf, err := validatePair(certificatePEM, privateKeyPEM, time.Now())
	if err != nil {
		return PublicInfo{}, err
	}
	certificateCipher, err := s.encrypt(aad(project, environment, key, "certificate"), certificatePEM)
	if err != nil {
		return PublicInfo{}, err
	}
	privateKeyCipher, err := s.encrypt(aad(project, environment, key, "private-key"), privateKeyPEM)
	if err != nil {
		return PublicInfo{}, err
	}
	fingerprint := sha256.Sum256(leaf.Raw)
	record := StoredMaterial{
		ProjectKey: project, EnvironmentKey: environment, Key: key,
		CertificateCipher: certificateCipher, PrivateKeyCipher: privateKeyCipher,
		Fingerprint: strings.ToUpper(hex.EncodeToString(fingerprint[:])), Subject: leaf.Subject.String(),
		DNSNames: append([]string(nil), leaf.DNSNames...), NotBefore: leaf.NotBefore.UTC(), NotAfter: leaf.NotAfter.UTC(),
		UpdatedBy: strings.TrimSpace(updatedBy),
	}
	if err := s.store.SaveTLSCertificate(ctx, record); err != nil {
		return PublicInfo{}, err
	}
	stored, err := s.store.GetTLSCertificate(ctx, project, environment, key)
	if err != nil {
		return PublicInfo{}, err
	}
	return publicInfo(stored), nil
}

func (s *Service) Delete(ctx context.Context, project, environment, key string) error {
	project, environment, key = normalizeScope(project, environment, key)
	if !validScope(project, environment, key) {
		return ErrInvalidMaterial
	}
	return s.store.DeleteTLSCertificate(ctx, project, environment, key)
}

func (s *Service) Material(ctx context.Context, project, environment, key string) (Material, error) {
	project, environment, key = normalizeScope(project, environment, key)
	if !validScope(project, environment, key) {
		return Material{}, ErrInvalidMaterial
	}
	record, err := s.store.GetTLSCertificate(ctx, project, environment, key)
	if errors.Is(err, os.ErrNotExist) {
		return Material{}, ErrMaterialMissing
	}
	if err != nil {
		return Material{}, err
	}
	certificate, err := s.decrypt(aad(project, environment, key, "certificate"), record.CertificateCipher)
	if err != nil {
		return Material{}, fmt.Errorf("decrypt TLS certificate %s: %w", key, err)
	}
	privateKey, err := s.decrypt(aad(project, environment, key, "private-key"), record.PrivateKeyCipher)
	if err != nil {
		clear(certificate)
		return Material{}, fmt.Errorf("decrypt TLS private key %s: %w", key, err)
	}
	if _, err := validatePair(certificate, privateKey, time.Now()); err != nil {
		clear(certificate)
		clear(privateKey)
		return Material{}, err
	}
	return Material{CertificatePEM: certificate, PrivateKeyPEM: privateKey}, nil
}

func validatePair(certificatePEM, privateKeyPEM []byte, now time.Time) (*x509.Certificate, error) {
	var certificates []*x509.Certificate
	rest := certificatePEM
	for len(bytes.TrimSpace(rest)) > 0 {
		block, remaining := pem.Decode(rest)
		if block == nil || block.Type != "CERTIFICATE" {
			return nil, fmt.Errorf("%w: certificate chain must contain PEM CERTIFICATE blocks only", ErrInvalidMaterial)
		}
		certificate, err := x509.ParseCertificate(block.Bytes)
		if err != nil {
			return nil, fmt.Errorf("%w: parse certificate: %v", ErrInvalidMaterial, err)
		}
		certificates = append(certificates, certificate)
		rest = remaining
	}
	if len(certificates) == 0 {
		return nil, fmt.Errorf("%w: no certificate was found", ErrInvalidMaterial)
	}
	leaf := certificates[0]
	if now.Before(leaf.NotBefore) {
		return nil, fmt.Errorf("%w: leaf certificate is not valid before %s", ErrInvalidMaterial, leaf.NotBefore.UTC().Format(time.RFC3339))
	}
	if !now.Before(leaf.NotAfter) {
		return nil, fmt.Errorf("%w: leaf certificate expired at %s", ErrInvalidMaterial, leaf.NotAfter.UTC().Format(time.RFC3339))
	}
	privateKey, err := parsePrivateKey(privateKeyPEM)
	if err != nil {
		return nil, err
	}
	publicKey, ok := privateKey.(crypto.Signer)
	if !ok {
		return nil, fmt.Errorf("%w: unsupported private key type", ErrInvalidMaterial)
	}
	certificatePublic, err := x509.MarshalPKIXPublicKey(leaf.PublicKey)
	if err != nil {
		return nil, fmt.Errorf("%w: encode certificate public key", ErrInvalidMaterial)
	}
	privatePublic, err := x509.MarshalPKIXPublicKey(publicKey.Public())
	if err != nil || !bytes.Equal(certificatePublic, privatePublic) {
		return nil, fmt.Errorf("%w: private key does not match the leaf certificate", ErrInvalidMaterial)
	}
	return leaf, nil
}

func parsePrivateKey(value []byte) (any, error) {
	block, rest := pem.Decode(value)
	if block == nil || len(bytes.TrimSpace(rest)) != 0 {
		return nil, fmt.Errorf("%w: private key must contain exactly one PEM block", ErrInvalidMaterial)
	}
	if x509.IsEncryptedPEMBlock(block) || strings.Contains(block.Type, "ENCRYPTED") { //nolint:staticcheck -- needed to reject legacy encrypted PEM blocks explicitly.
		return nil, fmt.Errorf("%w: encrypted private keys are not supported", ErrInvalidMaterial)
	}
	if key, err := x509.ParsePKCS8PrivateKey(block.Bytes); err == nil {
		return key, nil
	}
	if key, err := x509.ParsePKCS1PrivateKey(block.Bytes); err == nil {
		return key, nil
	}
	if key, err := x509.ParseECPrivateKey(block.Bytes); err == nil {
		return key, nil
	}
	return nil, fmt.Errorf("%w: private key must be PKCS#8, PKCS#1, or EC PEM", ErrInvalidMaterial)
}

func (s *Service) encrypt(aad string, plaintext []byte) (string, error) {
	nonce := make([]byte, s.aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", err
	}
	sealed := s.aead.Seal(nonce, nonce, plaintext, []byte(aad))
	return "v1:" + base64.StdEncoding.EncodeToString(sealed), nil
}

func (s *Service) decrypt(aad, value string) ([]byte, error) {
	encoded, ok := strings.CutPrefix(value, "v1:")
	if !ok {
		return nil, errors.New("unsupported TLS material ciphertext")
	}
	sealed, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil || len(sealed) <= s.aead.NonceSize() {
		return nil, errors.New("invalid TLS material ciphertext")
	}
	return s.aead.Open(nil, sealed[:s.aead.NonceSize()], sealed[s.aead.NonceSize():], []byte(aad))
}

func publicInfo(record StoredMaterial) PublicInfo {
	return PublicInfo{
		Key: record.Key, Configured: true, Fingerprint: record.Fingerprint, Subject: record.Subject,
		DNSNames: append([]string(nil), record.DNSNames...), NotBefore: record.NotBefore, NotAfter: record.NotAfter,
		UpdatedBy: record.UpdatedBy, UpdatedAt: record.UpdatedAt,
	}
}

func normalizeScope(project, environment, key string) (string, string, string) {
	return strings.ToLower(strings.TrimSpace(project)), strings.ToLower(strings.TrimSpace(environment)), strings.ToLower(strings.TrimSpace(key))
}

func validScope(project, environment, key string) bool {
	return len(project) <= 48 && len(environment) <= 8 && len(key) <= 63 && namePattern.MatchString(project) && namePattern.MatchString(environment) && namePattern.MatchString(key)
}

func aad(project, environment, key, kind string) string {
	return "ops-deploy/tls/" + project + "\x00" + environment + "\x00" + key + "\x00" + kind
}

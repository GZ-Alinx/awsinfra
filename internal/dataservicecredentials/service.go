package dataservicecredentials

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/GZ-Alinx/awsinfra/internal/appconfig"
)

var (
	ErrInvalidCredential = errors.New("invalid data service credential")
	ErrCredentialMissing = errors.New("data service credential is not configured")
	scopePattern         = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`)
	usernamePattern      = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_]{0,15}$`)
)

var supportedServices = map[string]bool{"rds": true, "aurora": true}

type StoredCredential struct {
	ProjectKey, EnvironmentKey, ServiceKey string
	Username, PasswordCipher, UpdatedBy    string
	CreatedAt, UpdatedAt                   time.Time
}

type Store interface {
	ListDataServiceCredentials(context.Context, string, string) ([]StoredCredential, error)
	GetDataServiceCredential(context.Context, string, string, string) (StoredCredential, error)
	SaveDataServiceCredential(context.Context, StoredCredential) error
	DeleteDataServiceCredential(context.Context, string, string, string) error
}

type Input struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

type PublicInfo struct {
	ServiceKey string    `json:"service_key"`
	Username   string    `json:"username"`
	Configured bool      `json:"configured"`
	UpdatedBy  string    `json:"updated_by"`
	UpdatedAt  time.Time `json:"updated_at"`
}

type Material struct {
	Username string
	Password string
}

type Service struct {
	store Store
	aead  cipher.AEAD
}

func New(config *appconfig.Config, store Store) (*Service, error) {
	key, err := base64.StdEncoding.DecodeString(strings.TrimSpace(config.CredentialKey()))
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
	project, environment, _ = normalize(project, environment, "rds")
	if !validScope(project, environment, "rds") {
		return nil, ErrInvalidCredential
	}
	records, err := s.store.ListDataServiceCredentials(ctx, project, environment)
	if err != nil {
		return nil, err
	}
	result := make([]PublicInfo, 0, len(records))
	for _, record := range records {
		result = append(result, publicInfo(record))
	}
	return result, nil
}

func (s *Service) Save(ctx context.Context, project, environment, service, updatedBy string, input Input) (PublicInfo, error) {
	project, environment, service = normalize(project, environment, service)
	input.Username = strings.TrimSpace(input.Username)
	if !validScope(project, environment, service) || !usernamePattern.MatchString(input.Username) || !validPassword(input.Password) {
		return PublicInfo{}, fmt.Errorf("%w: 用户名需以字母开头且最多 16 位；密码需 8-41 位可打印 ASCII，且不能包含空格、/ 、\" 或 @", ErrInvalidCredential)
	}
	passwordCipher, err := s.encrypt(aad(project, environment, service), []byte(input.Password))
	if err != nil {
		return PublicInfo{}, err
	}
	record := StoredCredential{
		ProjectKey: project, EnvironmentKey: environment, ServiceKey: service,
		Username: input.Username, PasswordCipher: passwordCipher, UpdatedBy: strings.TrimSpace(updatedBy),
	}
	if err := s.store.SaveDataServiceCredential(ctx, record); err != nil {
		return PublicInfo{}, err
	}
	stored, err := s.store.GetDataServiceCredential(ctx, project, environment, service)
	if err != nil {
		return PublicInfo{}, err
	}
	return publicInfo(stored), nil
}

func (s *Service) Delete(ctx context.Context, project, environment, service string) error {
	project, environment, service = normalize(project, environment, service)
	if !validScope(project, environment, service) {
		return ErrInvalidCredential
	}
	return s.store.DeleteDataServiceCredential(ctx, project, environment, service)
}

func (s *Service) Materials(ctx context.Context, project, environment string) (map[string]Material, error) {
	records, err := s.store.ListDataServiceCredentials(ctx, strings.ToLower(strings.TrimSpace(project)), strings.ToLower(strings.TrimSpace(environment)))
	if err != nil {
		return nil, err
	}
	result := make(map[string]Material, len(records))
	for _, record := range records {
		password, err := s.decrypt(aad(record.ProjectKey, record.EnvironmentKey, record.ServiceKey), record.PasswordCipher)
		if err != nil {
			return nil, fmt.Errorf("decrypt %s credential: %w", record.ServiceKey, err)
		}
		result[record.ServiceKey] = Material{Username: record.Username, Password: string(password)}
		clear(password)
	}
	return result, nil
}

func validPassword(value string) bool {
	if len(value) < 8 || len(value) > 41 {
		return false
	}
	for _, character := range value {
		if character < 33 || character > 126 || character == '/' || character == '"' || character == '@' {
			return false
		}
	}
	return true
}

func normalize(project, environment, service string) (string, string, string) {
	return strings.ToLower(strings.TrimSpace(project)), strings.ToLower(strings.TrimSpace(environment)), strings.ToLower(strings.TrimSpace(service))
}

func validScope(project, environment, service string) bool {
	return len(project) <= 48 && len(environment) <= 8 && scopePattern.MatchString(project) && scopePattern.MatchString(environment) && supportedServices[service]
}

func aad(project, environment, service string) string {
	return "data-service\x00" + project + "\x00" + environment + "\x00" + service
}

func (s *Service) encrypt(aadValue string, plaintext []byte) (string, error) {
	nonce := make([]byte, s.aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", err
	}
	sealed := s.aead.Seal(nonce, nonce, plaintext, []byte(aadValue))
	return "v1:" + base64.StdEncoding.EncodeToString(sealed), nil
}

func (s *Service) decrypt(aadValue, value string) ([]byte, error) {
	encoded, ok := strings.CutPrefix(value, "v1:")
	if !ok {
		return nil, errors.New("unsupported data service credential ciphertext")
	}
	sealed, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil || len(sealed) <= s.aead.NonceSize() {
		return nil, errors.New("invalid data service credential ciphertext")
	}
	return s.aead.Open(nil, sealed[:s.aead.NonceSize()], sealed[s.aead.NonceSize():], []byte(aadValue))
}

func publicInfo(record StoredCredential) PublicInfo {
	return PublicInfo{ServiceKey: record.ServiceKey, Username: record.Username, Configured: true, UpdatedBy: record.UpdatedBy, UpdatedAt: record.UpdatedAt}
}

func IsMissing(err error) bool {
	return errors.Is(err, os.ErrNotExist) || errors.Is(err, ErrCredentialMissing)
}

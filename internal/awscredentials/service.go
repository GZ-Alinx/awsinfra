package awscredentials

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"io"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"time"

	"ops-deploy-platform/internal/appconfig"
)

var (
	ErrInvalidCredential  = errors.New("invalid AWS credential")
	ErrVerificationFailed = errors.New("AWS credential verification failed")
	ErrCredentialMismatch = errors.New("AWS credential does not belong to this project")
	ErrCredentialNotBound = errors.New("current project has no selected AWS credential")
	accessKeyPattern      = regexp.MustCompile(`^[A-Z0-9]{16,128}$`)
	credentialKeyPattern  = regexp.MustCompile(`^[a-z][a-z0-9-]{0,62}$`)
	regionPattern         = regexp.MustCompile(`^[a-z]{2}(?:-gov)?-[a-z]+-[0-9]$`)
)

type credentialValidationError struct{ message string }

func (e credentialValidationError) Error() string { return e.message }
func (e credentialValidationError) Unwrap() error { return ErrInvalidCredential }

type credentialVerificationError struct{ message string }

func (e credentialVerificationError) Error() string { return e.message }
func (e credentialVerificationError) Unwrap() error { return ErrVerificationFailed }

func invalidCredential(message string) error {
	return credentialValidationError{message: message}
}

func verificationFailed(message string) error {
	return credentialVerificationError{message: message}
}

type StoredCredential struct {
	Key                   string
	DisplayName           string
	ProjectKey            string
	AccessKeyID           string
	SecretAccessKeyCipher string
	SessionTokenCipher    string
	AccountID             string
	PrincipalARN          string
	PrincipalUserID       string
	UpdatedBy             string
	VerifiedAt            time.Time
	CreatedAt             time.Time
	UpdatedAt             time.Time
	Selected              bool
	ProjectArchived       bool
}

type Store interface {
	ListAWSCredentials(context.Context) ([]StoredCredential, error)
	GetAWSCredential(context.Context, string) (StoredCredential, error)
	GetSelectedAWSCredential(context.Context, string) (StoredCredential, error)
	SaveAWSCredential(context.Context, StoredCredential) error
	DeleteAWSCredential(context.Context, string) error
	SelectProjectAWSCredential(context.Context, string, string) error
}

type Input struct {
	AccessKeyID     string `json:"access_key_id"`
	SecretAccessKey string `json:"secret_access_key"`
	SessionToken    string `json:"session_token"`
	Region          string `json:"region"`
}

type PublicInfo struct {
	Key             string    `json:"key,omitempty"`
	DisplayName     string    `json:"display_name,omitempty"`
	ProjectKey      string    `json:"project_key,omitempty"`
	Configured      bool      `json:"configured"`
	Source          string    `json:"source"`
	MaskedAccessKey string    `json:"masked_access_key,omitempty"`
	AccountID       string    `json:"account_id,omitempty"`
	PrincipalARN    string    `json:"principal_arn,omitempty"`
	PrincipalUserID string    `json:"principal_user_id,omitempty"`
	Profile         string    `json:"profile,omitempty"`
	VerifiedAt      time.Time `json:"verified_at,omitempty"`
	UpdatedBy       string    `json:"updated_by,omitempty"`
	CreatedAt       time.Time `json:"created_at,omitempty"`
	UpdatedAt       time.Time `json:"updated_at,omitempty"`
	Selected        bool      `json:"selected"`
	ProjectArchived bool      `json:"project_archived"`
}

type Service struct {
	store Store
	tool  string
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
	return &Service{store: store, tool: config.Tools.AWS, aead: aead}, nil
}

func (s *Service) Info(ctx context.Context, project string) (PublicInfo, error) {
	project = strings.TrimSpace(strings.ToLower(project))
	record, err := s.store.GetSelectedAWSCredential(ctx, project)
	if errors.Is(err, os.ErrNotExist) {
		return PublicInfo{ProjectKey: project, Configured: false, Source: "project-credential-required"}, nil
	}
	if err != nil {
		return PublicInfo{}, err
	}
	if record.ProjectKey != project {
		return PublicInfo{}, ErrCredentialMismatch
	}
	record.Selected = true
	return publicInfo(record), nil
}

func (s *Service) List(ctx context.Context) ([]PublicInfo, error) {
	records, err := s.store.ListAWSCredentials(ctx)
	if err != nil {
		return nil, err
	}
	result := make([]PublicInfo, 0, len(records))
	for _, record := range records {
		result = append(result, publicInfo(record))
	}
	return result, nil
}

func (s *Service) SaveNamedAndVerify(ctx context.Context, key, displayName, project, updatedBy string, input Input) (PublicInfo, error) {
	displayName = strings.TrimSpace(displayName)
	if strings.TrimSpace(key) == "" {
		key = displayName
	}
	key = NormalizeCredentialKey(key)
	project = strings.TrimSpace(strings.ToLower(project))
	if !credentialKeyPattern.MatchString(key) {
		return PublicInfo{}, invalidCredential("凭据标识无法转换为有效资源标识，请使用中文、英文、数字或连字符")
	}
	if displayName == "" || len([]rune(displayName)) > 128 {
		return PublicInfo{}, invalidCredential("凭据名称应为 1–128 个字符")
	}
	if project == "" {
		return PublicInfo{}, invalidCredential("请选择凭据所属项目")
	}
	return s.saveAndVerify(ctx, key, displayName, project, updatedBy, input)
}

func (s *Service) SaveAndVerify(ctx context.Context, project, updatedBy string, input Input) (PublicInfo, error) {
	project = strings.TrimSpace(strings.ToLower(project))
	return s.saveAndVerify(ctx, project+"-default", "默认 AWS 身份", project, updatedBy, input)
}

func (s *Service) saveAndVerify(ctx context.Context, key, displayName, project, updatedBy string, input Input) (PublicInfo, error) {
	input.AccessKeyID = strings.TrimSpace(input.AccessKeyID)
	input.SecretAccessKey = strings.TrimSpace(input.SecretAccessKey)
	input.SessionToken = strings.TrimSpace(input.SessionToken)
	input.Region = strings.TrimSpace(strings.ToLower(input.Region))
	if !accessKeyPattern.MatchString(input.AccessKeyID) {
		return PublicInfo{}, invalidCredential("Access Key ID 格式不正确：应为 16–128 位大写字母或数字，且不要包含空格")
	}
	if len(input.SecretAccessKey) < 16 || len(input.SecretAccessKey) > 256 {
		return PublicInfo{}, invalidCredential("Secret Access Key 长度应为 16–256 个字符")
	}
	if len(input.SessionToken) > 16<<10 {
		return PublicInfo{}, invalidCredential("Session Token 不能超过 16 KiB")
	}
	if input.Region == "" {
		input.Region = "ap-south-1"
	}
	if !regionPattern.MatchString(input.Region) {
		return PublicInfo{}, invalidCredential("AWS Region 格式不正确，例如 ap-south-1")
	}
	if existing, err := s.store.GetAWSCredential(ctx, key); err == nil && existing.ProjectKey != project {
		return PublicInfo{}, ErrCredentialMismatch
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return PublicInfo{}, err
	}
	identity, err := s.verify(ctx, input)
	if err != nil {
		return PublicInfo{}, err
	}
	secretCipher, err := s.encrypt(project+"\x00secret", []byte(input.SecretAccessKey))
	if err != nil {
		return PublicInfo{}, err
	}
	tokenCipher := ""
	if input.SessionToken != "" {
		tokenCipher, err = s.encrypt(project+"\x00token", []byte(input.SessionToken))
		if err != nil {
			return PublicInfo{}, err
		}
	}
	now := time.Now().UTC()
	record := StoredCredential{
		Key: key, DisplayName: displayName, ProjectKey: project, AccessKeyID: input.AccessKeyID, SecretAccessKeyCipher: secretCipher,
		SessionTokenCipher: tokenCipher, AccountID: identity.Account, PrincipalARN: identity.ARN,
		PrincipalUserID: identity.UserID, UpdatedBy: updatedBy, VerifiedAt: now,
	}
	if err := s.store.SaveAWSCredential(ctx, record); err != nil {
		return PublicInfo{}, err
	}
	if selected, err := s.store.GetSelectedAWSCredential(ctx, project); errors.Is(err, os.ErrNotExist) {
		if err := s.store.SelectProjectAWSCredential(ctx, project, key); err != nil {
			return PublicInfo{}, err
		}
		record.Selected = true
	} else if err != nil {
		return PublicInfo{}, err
	} else {
		if selected.ProjectKey != project {
			return PublicInfo{}, ErrCredentialMismatch
		}
		record.Selected = selected.Key == key
	}
	return publicInfo(record), nil
}

func (s *Service) Delete(ctx context.Context, project string) error {
	project = strings.TrimSpace(strings.ToLower(project))
	record, err := s.store.GetSelectedAWSCredential(ctx, project)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if record.ProjectKey != project {
		return ErrCredentialMismatch
	}
	return s.store.DeleteAWSCredential(ctx, record.Key)
}

func (s *Service) DeleteNamed(ctx context.Context, key string) error {
	if !credentialKeyPattern.MatchString(key) {
		return ErrInvalidCredential
	}
	return s.store.DeleteAWSCredential(ctx, key)
}

func (s *Service) Select(ctx context.Context, project, key string) (PublicInfo, error) {
	project = strings.TrimSpace(strings.ToLower(project))
	record, err := s.store.GetAWSCredential(ctx, key)
	if err != nil {
		return PublicInfo{}, err
	}
	if record.ProjectKey != project {
		return PublicInfo{}, ErrCredentialMismatch
	}
	if err := s.store.SelectProjectAWSCredential(ctx, project, key); err != nil {
		return PublicInfo{}, err
	}
	record.Selected = true
	return publicInfo(record), nil
}

// Environment returns short-lived process environment entries. Callers must
// never log this slice or include it in command display strings.
func (s *Service) Environment(ctx context.Context, project string) ([]string, error) {
	project = strings.TrimSpace(strings.ToLower(project))
	record, err := s.store.GetSelectedAWSCredential(ctx, project)
	if errors.Is(err, os.ErrNotExist) {
		return nil, ErrCredentialNotBound
	}
	if err != nil {
		return nil, err
	}
	if record.ProjectKey != project {
		return nil, ErrCredentialMismatch
	}
	secret, err := s.decrypt(project+"\x00secret", record.SecretAccessKeyCipher)
	if err != nil {
		return nil, err
	}
	defer clear(secret)
	env := []string{"AWS_ACCESS_KEY_ID=" + record.AccessKeyID, "AWS_SECRET_ACCESS_KEY=" + string(secret)}
	if record.SessionTokenCipher != "" {
		token, decryptErr := s.decrypt(project+"\x00token", record.SessionTokenCipher)
		if decryptErr != nil {
			return nil, decryptErr
		}
		env = append(env, "AWS_SESSION_TOKEN="+string(token))
		clear(token)
	}
	return env, nil
}

type identity struct {
	Account string `json:"Account"`
	ARN     string `json:"Arn"`
	UserID  string `json:"UserId"`
}

func (s *Service) verify(ctx context.Context, input Input) (identity, error) {
	commandCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(commandCtx, s.tool, "sts", "get-caller-identity", "--region", input.Region, "--output", "json", "--no-cli-pager") // #nosec G204 -- AWS tool is administrator-configured and region is allowlist-validated.
	cmd.Env = append(withoutAWSCredentials(os.Environ()),
		"AWS_ACCESS_KEY_ID="+input.AccessKeyID,
		"AWS_SECRET_ACCESS_KEY="+input.SecretAccessKey,
		"AWS_REGION="+input.Region,
		"AWS_DEFAULT_REGION="+input.Region,
	)
	if input.SessionToken != "" {
		cmd.Env = append(cmd.Env, "AWS_SESSION_TOKEN="+input.SessionToken)
	}
	payload, err := cmd.CombinedOutput()
	if err != nil {
		message := string(payload)
		switch {
		case errors.Is(commandCtx.Err(), context.DeadlineExceeded):
			return identity{}, verificationFailed("AWS STS 验证超时，请检查本机网络、代理和所选 Region")
		case strings.Contains(message, "InvalidClientTokenId"), strings.Contains(message, "UnrecognizedClientException"):
			return identity{}, verificationFailed("AWS 拒绝了 Access Key ID：凭据不存在、已停用，或 Session Token 不匹配")
		case strings.Contains(message, "SignatureDoesNotMatch"), strings.Contains(message, "IncompleteSignature"):
			return identity{}, verificationFailed("AWS 签名校验失败，请确认 Secret Access Key 与 Access Key ID 配套")
		case strings.Contains(message, "ExpiredToken"), strings.Contains(message, "TokenRefreshRequired"):
			return identity{}, verificationFailed("AWS Session Token 已过期，请重新生成临时凭据")
		case strings.Contains(message, "Could not connect to the endpoint URL"), strings.Contains(message, "Failed to connect"):
			return identity{}, verificationFailed("无法连接 AWS STS，请检查本机网络、代理和所选 Region")
		default:
			return identity{}, verificationFailed("AWS STS 验证失败，请检查 AK/SK；临时凭据还必须填写配套的 Session Token")
		}
	}
	var result identity
	if json.Unmarshal(payload, &result) != nil || result.Account == "" || result.ARN == "" {
		return identity{}, verificationFailed("AWS STS 返回了无法识别的身份信息，请稍后重试")
	}
	return result, nil
}

func (s *Service) encrypt(aad string, plaintext []byte) (string, error) {
	nonce := make([]byte, s.aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", err
	}
	sealed := s.aead.Seal(nonce, nonce, plaintext, []byte(aad))
	return "v1:" + base64.RawStdEncoding.EncodeToString(sealed), nil
}

func (s *Service) decrypt(aad, encoded string) ([]byte, error) {
	version, payload, ok := strings.Cut(encoded, ":")
	if !ok || version != "v1" {
		return nil, errors.New("unsupported credential ciphertext")
	}
	sealed, err := base64.RawStdEncoding.DecodeString(payload)
	if err != nil || len(sealed) < s.aead.NonceSize() {
		return nil, errors.New("invalid credential ciphertext")
	}
	nonce, ciphertext := sealed[:s.aead.NonceSize()], sealed[s.aead.NonceSize():]
	return s.aead.Open(nil, nonce, ciphertext, []byte(aad))
}

func publicInfo(record StoredCredential) PublicInfo {
	return PublicInfo{
		Key: record.Key, DisplayName: record.DisplayName, ProjectKey: record.ProjectKey,
		Configured: true, Source: "project-encrypted-credential", MaskedAccessKey: mask(record.AccessKeyID),
		AccountID: record.AccountID, PrincipalARN: record.PrincipalARN, PrincipalUserID: record.PrincipalUserID,
		VerifiedAt: record.VerifiedAt, UpdatedBy: record.UpdatedBy, CreatedAt: record.CreatedAt,
		UpdatedAt: record.UpdatedAt, Selected: record.Selected, ProjectArchived: record.ProjectArchived,
	}
}

func mask(value string) string {
	if len(value) <= 8 {
		return "****"
	}
	return value[:4] + strings.Repeat("*", len(value)-8) + value[len(value)-4:]
}

// NormalizeCredentialKey converts a user-facing credential label into the
// stable identifier stored by the platform. The display name remains intact.
func NormalizeCredentialKey(value string) string {
	raw := strings.TrimSpace(strings.ToLower(value))
	if raw == "" {
		return ""
	}
	var builder strings.Builder
	separator := false
	for _, character := range raw {
		switch {
		case character >= 'a' && character <= 'z', character >= '0' && character <= '9':
			if separator && builder.Len() > 0 {
				builder.WriteByte('-')
			}
			builder.WriteRune(character)
			separator = false
		default:
			separator = true
		}
	}
	slug := strings.Trim(builder.String(), "-")
	if slug == "" {
		hash := fnv.New32a()
		_, _ = hash.Write([]byte(raw))
		slug = fmt.Sprintf("aws-credential-%08x", hash.Sum32())
	}
	if slug[0] >= '0' && slug[0] <= '9' {
		slug = "aws-" + slug
	}
	if len(slug) > 63 {
		slug = strings.TrimRight(slug[:63], "-")
	}
	return slug
}

func withoutAWSCredentials(source []string) []string {
	blocked := map[string]bool{
		"AWS_ACCESS_KEY_ID": true, "AWS_SECRET_ACCESS_KEY": true, "AWS_SESSION_TOKEN": true,
		"AWS_PROFILE": true, "AWS_DEFAULT_PROFILE": true,
	}
	result := make([]string, 0, len(source))
	for _, item := range source {
		key, _, _ := strings.Cut(item, "=")
		if !blocked[key] {
			result = append(result, item)
		}
	}
	return result
}

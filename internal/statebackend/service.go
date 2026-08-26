package statebackend

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path"
	"regexp"
	"strings"
	"time"

	"github.com/GZ-Alinx/awsinfra/internal/appconfig"
)

var (
	ErrNotConfigured = errors.New("统一 Terraform 状态中心尚未配置")
	ErrInvalidConfig = errors.New("invalid Terraform state backend configuration")
	bucketPattern    = regexp.MustCompile(`^[a-z0-9][a-z0-9.-]{1,61}[a-z0-9]$`)
	regionPattern    = regexp.MustCompile(`^[a-z]{2}(?:-gov)?-[a-z]+-[0-9]$`)
	accessKeyPattern = regexp.MustCompile(`^[A-Z0-9]{16,128}$`)
)

type StoredConfig struct {
	Enabled               bool
	Bucket                string
	Region                string
	KeyPrefix             string
	KMSKeyID              string
	AccessKeyID           string
	SecretAccessKeyCipher string
	SessionTokenCipher    string
	AccountID             string
	PrincipalARN          string
	PrincipalUserID       string
	UpdatedBy             string
	VerifiedAt, UpdatedAt time.Time
}

type Store interface {
	GetTerraformStateBackend(context.Context) (StoredConfig, error)
	SaveTerraformStateBackend(context.Context, StoredConfig) error
}

type Input struct {
	Enabled         bool   `json:"enabled"`
	Bucket          string `json:"bucket"`
	Region          string `json:"region"`
	KeyPrefix       string `json:"key_prefix"`
	KMSKeyID        string `json:"kms_key_id"`
	AccessKeyID     string `json:"access_key_id"`
	SecretAccessKey string `json:"secret_access_key"`
	SessionToken    string `json:"session_token"`
}

type PublicInfo struct {
	Configured      bool      `json:"configured"`
	Enabled         bool      `json:"enabled"`
	Bucket          string    `json:"bucket,omitempty"`
	Region          string    `json:"region,omitempty"`
	KeyPrefix       string    `json:"key_prefix,omitempty"`
	KMSKeyID        string    `json:"kms_key_id,omitempty"`
	MaskedAccessKey string    `json:"masked_access_key,omitempty"`
	AccountID       string    `json:"account_id,omitempty"`
	PrincipalARN    string    `json:"principal_arn,omitempty"`
	UpdatedBy       string    `json:"updated_by,omitempty"`
	VerifiedAt      time.Time `json:"verified_at,omitempty"`
	UpdatedAt       time.Time `json:"updated_at,omitempty"`
}

// Runtime is intentionally never serialized or returned by the HTTP API.
type Runtime struct {
	Bucket, Region, KeyPrefix, KMSKeyID        string
	AccountID                                  string
	AccessKeyID, SecretAccessKey, SessionToken string
}

type Service struct {
	store Store
	tool  string
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
	return &Service{store: store, tool: config.Tools.AWS, aead: aead}, nil
}

func (s *Service) Info(ctx context.Context) (PublicInfo, error) {
	record, err := s.store.GetTerraformStateBackend(ctx)
	if errors.Is(err, os.ErrNotExist) {
		return PublicInfo{}, nil
	}
	if err != nil {
		return PublicInfo{}, err
	}
	return publicInfo(record), nil
}

func (s *Service) Runtime(ctx context.Context) (Runtime, error) {
	record, err := s.store.GetTerraformStateBackend(ctx)
	if errors.Is(err, os.ErrNotExist) || !record.Enabled {
		return Runtime{}, ErrNotConfigured
	}
	if err != nil {
		return Runtime{}, err
	}
	secret, err := s.decrypt("terraform-state\x00secret", record.SecretAccessKeyCipher)
	if err != nil {
		return Runtime{}, err
	}
	defer clear(secret)
	runtime := Runtime{Bucket: record.Bucket, Region: record.Region, KeyPrefix: record.KeyPrefix, KMSKeyID: record.KMSKeyID, AccountID: record.AccountID, AccessKeyID: record.AccessKeyID, SecretAccessKey: string(secret)}
	if record.SessionTokenCipher != "" {
		token, decryptErr := s.decrypt("terraform-state\x00token", record.SessionTokenCipher)
		if decryptErr != nil {
			return Runtime{}, decryptErr
		}
		runtime.SessionToken = string(token)
		clear(token)
	}
	return runtime, nil
}

// StateOutputs reads the root module outputs directly from the centralized
// S3 state. Runtime Terraform backend files are deliberately removed after a
// job, so status and resource discovery must not depend on a local backend
// cache surviving the deployment process.
func (s *Service) StateOutputs(ctx context.Context, project, target, stage string) (map[string]any, error) {
	project = strings.TrimSpace(strings.ToLower(project))
	target = strings.TrimSpace(strings.ToLower(target))
	stage = strings.TrimSpace(strings.ToLower(stage))
	if !validStateScope(project) || !validStateScope(target) || (stage != "infra" && stage != "platform") {
		return nil, errors.New("invalid Terraform state output scope")
	}
	runtime, err := s.Runtime(ctx)
	if err != nil {
		return nil, err
	}
	defer clear([]byte(runtime.SecretAccessKey))
	defer clear([]byte(runtime.SessionToken))
	objectKey := strings.Trim(runtime.KeyPrefix, "/") + "/projects/" + project + "/" + target + "/" + stage + "/terraform.tfstate"
	commandCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	payload, err := s.command(commandCtx, runtimeCredentialEnvironment(runtime),
		"s3", "cp", "s3://"+runtime.Bucket+"/"+objectKey, "-",
		"--region", runtime.Region, "--only-show-errors", "--no-progress")
	if err != nil {
		return nil, fmt.Errorf("read centralized Terraform state s3://%s/%s: %w", runtime.Bucket, objectKey, err)
	}
	var state struct {
		Outputs map[string]struct {
			Value any `json:"value"`
		} `json:"outputs"`
	}
	if err := json.Unmarshal(payload, &state); err != nil {
		return nil, fmt.Errorf("decode centralized Terraform state outputs: %w", err)
	}
	result := make(map[string]any, len(state.Outputs))
	for key, output := range state.Outputs {
		result[key] = output.Value
	}
	return result, nil
}

func validStateScope(value string) bool {
	if value == "" || len(value) > 63 || strings.ContainsAny(value, "/\\\x00\r\n") || strings.Contains(value, "..") {
		return false
	}
	for _, character := range value {
		if (character < 'a' || character > 'z') && (character < '0' || character > '9') && character != '-' {
			return false
		}
	}
	return true
}

func runtimeCredentialEnvironment(runtime Runtime) []string {
	env := []string{
		"AWS_ACCESS_KEY_ID=" + runtime.AccessKeyID,
		"AWS_SECRET_ACCESS_KEY=" + runtime.SecretAccessKey,
		"AWS_REGION=" + runtime.Region,
		"AWS_DEFAULT_REGION=" + runtime.Region,
	}
	if runtime.SessionToken != "" {
		env = append(env, "AWS_SESSION_TOKEN="+runtime.SessionToken)
	}
	return env
}

func (s *Service) SaveAndVerify(ctx context.Context, updatedBy string, input Input) (PublicInfo, error) {
	input.Bucket = strings.TrimSpace(strings.ToLower(input.Bucket))
	input.Region = strings.TrimSpace(strings.ToLower(input.Region))
	input.KeyPrefix = strings.Trim(strings.TrimSpace(input.KeyPrefix), "/")
	input.KMSKeyID = strings.TrimSpace(input.KMSKeyID)
	input.AccessKeyID = strings.TrimSpace(input.AccessKeyID)
	input.SecretAccessKey = strings.TrimSpace(input.SecretAccessKey)
	input.SessionToken = strings.TrimSpace(input.SessionToken)
	if err := validateInput(input); err != nil {
		return PublicInfo{}, err
	}
	if existing, err := s.store.GetTerraformStateBackend(ctx); err == nil {
		if input.AccessKeyID == "" {
			input.AccessKeyID = existing.AccessKeyID
		}
		if input.SecretAccessKey == "" {
			secret, decryptErr := s.decrypt("terraform-state\x00secret", existing.SecretAccessKeyCipher)
			if decryptErr != nil {
				return PublicInfo{}, decryptErr
			}
			input.SecretAccessKey = string(secret)
			clear(secret)
			if input.SessionToken == "" && existing.SessionTokenCipher != "" {
				token, decryptErr := s.decrypt("terraform-state\x00token", existing.SessionTokenCipher)
				if decryptErr != nil {
					return PublicInfo{}, decryptErr
				}
				input.SessionToken = string(token)
				clear(token)
			}
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return PublicInfo{}, err
	}
	if input.AccessKeyID == "" || input.SecretAccessKey == "" {
		return PublicInfo{}, fmt.Errorf("%w: 状态中心 Access Key ID 和 Secret Access Key 不能为空", ErrInvalidConfig)
	}
	identity, err := s.verifyAndHarden(ctx, input)
	if err != nil {
		return PublicInfo{}, err
	}
	secretCipher, err := s.encrypt("terraform-state\x00secret", []byte(input.SecretAccessKey))
	if err != nil {
		return PublicInfo{}, err
	}
	tokenCipher := ""
	if input.SessionToken != "" {
		tokenCipher, err = s.encrypt("terraform-state\x00token", []byte(input.SessionToken))
		if err != nil {
			return PublicInfo{}, err
		}
	}
	now := time.Now().UTC()
	record := StoredConfig{Enabled: input.Enabled, Bucket: input.Bucket, Region: input.Region, KeyPrefix: input.KeyPrefix, KMSKeyID: input.KMSKeyID, AccessKeyID: input.AccessKeyID, SecretAccessKeyCipher: secretCipher, SessionTokenCipher: tokenCipher, AccountID: identity.Account, PrincipalARN: identity.ARN, PrincipalUserID: identity.UserID, UpdatedBy: updatedBy, VerifiedAt: now, UpdatedAt: now}
	if err := s.store.SaveTerraformStateBackend(ctx, record); err != nil {
		return PublicInfo{}, err
	}
	input.SecretAccessKey, input.SessionToken = "", ""
	return publicInfo(record), nil
}

func validateInput(input Input) error {
	if !bucketPattern.MatchString(input.Bucket) || net.ParseIP(input.Bucket) != nil || strings.Contains(input.Bucket, "..") {
		return fmt.Errorf("%w: S3 bucket 名称格式不正确", ErrInvalidConfig)
	}
	if !regionPattern.MatchString(input.Region) {
		return fmt.Errorf("%w: AWS Region 格式不正确", ErrInvalidConfig)
	}
	if input.KeyPrefix == "" || len(input.KeyPrefix) > 512 || path.Clean(input.KeyPrefix) != input.KeyPrefix || strings.HasPrefix(input.KeyPrefix, ".") {
		return fmt.Errorf("%w: state 路径前缀不能为空、不能包含相对路径，且不能超过 512 个字符", ErrInvalidConfig)
	}
	if len(input.AccessKeyID) > 128 || len(input.SecretAccessKey) > 256 || len(input.SessionToken) > 16<<10 {
		return fmt.Errorf("%w: 状态中心凭据长度不合法", ErrInvalidConfig)
	}
	if input.AccessKeyID != "" && !accessKeyPattern.MatchString(input.AccessKeyID) {
		return fmt.Errorf("%w: 状态中心 Access Key ID 格式不正确", ErrInvalidConfig)
	}
	if input.SecretAccessKey != "" && len(input.SecretAccessKey) < 16 {
		return fmt.Errorf("%w: 状态中心 Secret Access Key 长度不正确", ErrInvalidConfig)
	}
	return nil
}

type identity struct{ Account, ARN, UserID string }

func (s *Service) verifyAndHarden(ctx context.Context, input Input) (identity, error) {
	verifyCtx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	env := credentialEnvironment(input)
	payload, err := s.command(verifyCtx, env, "sts", "get-caller-identity", "--region", input.Region, "--output", "json", "--no-cli-pager")
	if err != nil {
		return identity{}, fmt.Errorf("状态中心 AWS 身份验证失败: %w", err)
	}
	var raw struct {
		Account string `json:"Account"`
		ARN     string `json:"Arn"`
		UserID  string `json:"UserId"`
	}
	if json.Unmarshal(payload, &raw) != nil || raw.Account == "" || raw.ARN == "" {
		return identity{}, errors.New("状态中心 AWS STS 返回了无法识别的身份")
	}
	if _, err := s.command(verifyCtx, env, "s3api", "head-bucket", "--bucket", input.Bucket, "--region", input.Region); err != nil {
		return identity{}, fmt.Errorf("无法访问状态中心 S3 bucket %s: %w", input.Bucket, err)
	}
	locationPayload, err := s.command(verifyCtx, env, "s3api", "get-bucket-location", "--bucket", input.Bucket, "--query", "LocationConstraint", "--output", "text", "--region", input.Region)
	if err != nil {
		return identity{}, fmt.Errorf("无法确认状态桶 Region: %w", err)
	}
	location := strings.TrimSpace(string(locationPayload))
	if location == "None" || location == "null" || location == "" {
		location = "us-east-1"
	} else if location == "EU" {
		location = "eu-west-1"
	}
	if location != input.Region {
		return identity{}, fmt.Errorf("状态桶实际位于 %s，与填写的 Region %s 不一致", location, input.Region)
	}
	settings := [][]string{
		{"s3api", "put-public-access-block", "--bucket", input.Bucket, "--region", input.Region, "--public-access-block-configuration", `{"BlockPublicAcls":true,"IgnorePublicAcls":true,"BlockPublicPolicy":true,"RestrictPublicBuckets":true}`},
		{"s3api", "put-bucket-versioning", "--bucket", input.Bucket, "--region", input.Region, "--versioning-configuration", `{"Status":"Enabled"}`},
	}
	encryption := `{"Rules":[{"ApplyServerSideEncryptionByDefault":{"SSEAlgorithm":"AES256"},"BucketKeyEnabled":true}]}`
	if input.KMSKeyID != "" {
		encryption = fmt.Sprintf(`{"Rules":[{"ApplyServerSideEncryptionByDefault":{"SSEAlgorithm":"aws:kms","KMSMasterKeyID":%q},"BucketKeyEnabled":true}]}`, input.KMSKeyID)
	}
	settings = append(settings, []string{"s3api", "put-bucket-encryption", "--bucket", input.Bucket, "--region", input.Region, "--server-side-encryption-configuration", encryption})
	for _, args := range settings {
		if _, err := s.command(verifyCtx, env, args...); err != nil {
			return identity{}, fmt.Errorf("状态桶安全初始化失败（需要版本控制、加密和公共访问阻止权限）: %w", err)
		}
	}
	probe, err := os.CreateTemp("", "ops-state-probe-*")
	if err != nil {
		return identity{}, err
	}
	probePath := probe.Name()
	defer os.Remove(probePath)
	if _, err := probe.WriteString("ops-deploy-state-center-probe"); err != nil {
		probe.Close()
		return identity{}, err
	}
	if err := probe.Chmod(0o600); err != nil {
		probe.Close()
		return identity{}, err
	}
	if err := probe.Close(); err != nil {
		return identity{}, err
	}
	probeKey := input.KeyPrefix + "/.platform/permission-probe-" + fmt.Sprint(time.Now().UnixNano())
	if _, err := s.command(verifyCtx, env, "s3api", "put-object", "--bucket", input.Bucket, "--key", probeKey, "--body", probePath, "--region", input.Region); err != nil {
		return identity{}, fmt.Errorf("状态桶缺少对象写入权限: %w", err)
	}
	if _, err := s.command(verifyCtx, env, "s3api", "head-object", "--bucket", input.Bucket, "--key", probeKey, "--region", input.Region); err != nil {
		return identity{}, fmt.Errorf("状态桶缺少对象读取权限: %w", err)
	}
	if _, err := s.command(verifyCtx, env, "s3api", "delete-object", "--bucket", input.Bucket, "--key", probeKey, "--region", input.Region); err != nil {
		return identity{}, fmt.Errorf("状态桶缺少锁或测试对象删除权限: %w", err)
	}
	return identity{Account: raw.Account, ARN: raw.ARN, UserID: raw.UserID}, nil
}

func (s *Service) command(ctx context.Context, env []string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, s.tool, args...) // #nosec G204 -- tool is administrator-configured; arguments are validated or constant.
	cmd.Env = append(withoutAWSCredentials(os.Environ()), env...)
	payload, err := cmd.CombinedOutput()
	if err != nil {
		message := strings.TrimSpace(string(payload))
		if len(message) > 1200 {
			message = message[len(message)-1200:]
		}
		return nil, fmt.Errorf("%w: %s", err, message)
	}
	return payload, nil
}

func credentialEnvironment(input Input) []string {
	env := []string{"AWS_ACCESS_KEY_ID=" + input.AccessKeyID, "AWS_SECRET_ACCESS_KEY=" + input.SecretAccessKey, "AWS_REGION=" + input.Region, "AWS_DEFAULT_REGION=" + input.Region}
	if input.SessionToken != "" {
		env = append(env, "AWS_SESSION_TOKEN="+input.SessionToken)
	}
	return env
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
		return nil, errors.New("unsupported state credential ciphertext")
	}
	sealed, err := base64.RawStdEncoding.DecodeString(payload)
	if err != nil || len(sealed) < s.aead.NonceSize() {
		return nil, errors.New("invalid state credential ciphertext")
	}
	nonce, ciphertext := sealed[:s.aead.NonceSize()], sealed[s.aead.NonceSize():]
	return s.aead.Open(nil, nonce, ciphertext, []byte(aad))
}

func publicInfo(record StoredConfig) PublicInfo {
	return PublicInfo{Configured: true, Enabled: record.Enabled, Bucket: record.Bucket, Region: record.Region, KeyPrefix: record.KeyPrefix, KMSKeyID: record.KMSKeyID, MaskedAccessKey: mask(record.AccessKeyID), AccountID: record.AccountID, PrincipalARN: record.PrincipalARN, UpdatedBy: record.UpdatedBy, VerifiedAt: record.VerifiedAt, UpdatedAt: record.UpdatedAt}
}

func mask(value string) string {
	if len(value) <= 8 {
		return "****"
	}
	return value[:4] + strings.Repeat("*", len(value)-8) + value[len(value)-4:]
}

func withoutAWSCredentials(source []string) []string {
	blocked := map[string]bool{"AWS_ACCESS_KEY_ID": true, "AWS_SECRET_ACCESS_KEY": true, "AWS_SESSION_TOKEN": true, "AWS_PROFILE": true, "AWS_DEFAULT_PROFILE": true}
	result := make([]string, 0, len(source))
	for _, item := range source {
		key, _, _ := strings.Cut(item, "=")
		if !blocked[key] {
			result = append(result, item)
		}
	}
	return result
}

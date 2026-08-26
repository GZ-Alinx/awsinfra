package staticcdn

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/url"
	"os"
	"os/exec"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/GZ-Alinx/awsinfra/internal/appconfig"
)

var (
	ErrInvalid        = errors.New("静态资源 CDN 配置不合法")
	ErrNotFound       = errors.New("静态资源 CDN 不存在")
	ErrConflict       = errors.New("静态资源 CDN 已存在")
	ErrBucketNotEmpty = errors.New("S3 Bucket 中仍有文件，请清空后再删除")
	ErrDeletePending  = errors.New("CloudFront 正在停用，请等待状态更新后再次确认删除")

	bucketPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{1,61}[a-z0-9]$`)
	regionPattern = regexp.MustCompile(`^[a-z]{2}(?:-gov)?-[a-z]+-[0-9]$`)
)

const (
	cachingOptimizedPolicyID = "658327ea-f89d-4fab-a63d-7e88639e58f6"
	corsSecurityPolicyID     = "e61eb60c-9c35-4d20-a928-2b84e02af89c"
)

type Resource struct {
	ProjectKey      string    `json:"project_key"`
	EnvironmentKey  string    `json:"environment_key"`
	DisplayName     string    `json:"display_name"`
	BucketName      string    `json:"bucket_name"`
	Region          string    `json:"region"`
	CORSOrigins     []string  `json:"cors_origins"`
	DistributionID  string    `json:"distribution_id,omitempty"`
	DistributionARN string    `json:"distribution_arn,omitempty"`
	DomainName      string    `json:"domain_name,omitempty"`
	CDNURL          string    `json:"cdn_url,omitempty"`
	OACID           string    `json:"oac_id,omitempty"`
	Status          string    `json:"status"`
	LastError       string    `json:"last_error,omitempty"`
	CreatedBy       string    `json:"created_by"`
	CreatedAt       time.Time `json:"created_at"`
	UpdatedAt       time.Time `json:"updated_at"`
}

type Object struct {
	Key          string    `json:"key"`
	Size         int64     `json:"size"`
	ETag         string    `json:"etag"`
	LastModified time.Time `json:"last_modified"`
	CDNURL       string    `json:"cdn_url"`
}

type UploadAuthorization struct {
	Key       string    `json:"key"`
	Method    string    `json:"method"`
	UploadURL string    `json:"upload_url"`
	CDNURL    string    `json:"cdn_url"`
	ExpiresAt time.Time `json:"expires_at"`
}

type Store interface {
	ListStaticCDNs(context.Context, string, string) ([]Resource, error)
	GetStaticCDN(context.Context, string, string, string) (Resource, error)
	SaveStaticCDN(context.Context, Resource) error
	DeleteStaticCDN(context.Context, string, string, string) error
}

type AWSCredentialProvider interface {
	Environment(context.Context, string) ([]string, error)
}

type commandRunner interface {
	Run(context.Context, []string, ...string) ([]byte, error)
}

type execRunner struct{ tool string }

func (r execRunner) Run(ctx context.Context, environment []string, arguments ...string) ([]byte, error) {
	commandCtx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	cmd := exec.CommandContext(commandCtx, r.tool, arguments...) // #nosec G204 -- tool path is administrator-owned and all resource identifiers are validated.
	cmd.Env = append(withoutAWSCredentials(os.Environ()), environment...)
	payload, err := cmd.CombinedOutput()
	if err == nil {
		return payload, nil
	}
	message := strings.TrimSpace(string(payload))
	if errors.Is(commandCtx.Err(), context.DeadlineExceeded) {
		return nil, fmt.Errorf("AWS 请求超时: %w", commandCtx.Err())
	}
	if message == "" {
		message = err.Error()
	}
	return nil, &CommandError{Message: message, Err: err}
}

type CommandError struct {
	Message string
	Err     error
}

func (e *CommandError) Error() string { return e.Message }
func (e *CommandError) Unwrap() error { return e.Err }

type Service struct {
	store       Store
	credentials AWSCredentialProvider
	runner      commandRunner
	now         func() time.Time
}

func New(config *appconfig.Config, store Store, credentials AWSCredentialProvider) *Service {
	return &Service{store: store, credentials: credentials, runner: execRunner{tool: config.Tools.AWS}, now: func() time.Time { return time.Now().UTC() }}
}

func (s *Service) List(ctx context.Context, project, environment string, refresh bool) ([]Resource, error) {
	items, err := s.store.ListStaticCDNs(ctx, project, environment)
	if err != nil || !refresh {
		return items, err
	}
	for index := range items {
		refreshed, refreshErr := s.Refresh(ctx, project, environment, items[index].BucketName)
		if refreshErr == nil {
			items[index] = refreshed
		}
	}
	return items, nil
}

func (s *Service) Create(ctx context.Context, input Resource, createdBy string) (Resource, error) {
	normalizeResource(&input)
	if err := validateResource(input); err != nil {
		return Resource{}, err
	}
	awsEnvironment, err := s.awsEnvironment(ctx, input.ProjectKey, input.Region)
	if err != nil {
		return Resource{}, err
	}
	existing, getErr := s.store.GetStaticCDN(ctx, input.ProjectKey, input.EnvironmentKey, input.BucketName)
	isRetry := getErr == nil
	if getErr != nil && !errors.Is(getErr, os.ErrNotExist) {
		return Resource{}, getErr
	}
	if isRetry && existing.Status != "failed" {
		return Resource{}, ErrConflict
	}
	if isRetry {
		input = existing
		input.LastError = ""
	} else {
		input.CreatedBy = createdBy
		input.Status = "creating"
		input.CreatedAt = s.now()
	}
	input.UpdatedAt = s.now()
	if err := s.store.SaveStaticCDN(ctx, input); err != nil {
		return Resource{}, err
	}
	fail := func(operationErr error) (Resource, error) {
		input.Status = "failed"
		input.LastError = safeAWSError(operationErr)
		input.UpdatedAt = s.now()
		_ = s.store.SaveStaticCDN(context.WithoutCancel(ctx), input)
		return input, operationErr
	}
	if !isRetry {
		if err := s.createBucket(ctx, awsEnvironment, input); err != nil {
			return fail(err)
		}
	} else if _, err := s.run(ctx, awsEnvironment, "s3api", "head-bucket", "--bucket", input.BucketName, "--region", input.Region); err != nil {
		if err := s.createBucket(ctx, awsEnvironment, input); err != nil {
			return fail(err)
		}
	}
	if err := s.configureBucket(ctx, awsEnvironment, input); err != nil {
		return fail(err)
	}
	if input.OACID == "" {
		oacID, err := s.createOAC(ctx, awsEnvironment, input)
		if err != nil {
			return fail(err)
		}
		input.OACID = oacID
		input.UpdatedAt = s.now()
		if err := s.store.SaveStaticCDN(ctx, input); err != nil {
			return fail(err)
		}
	}
	if input.DistributionID == "" {
		distribution, err := s.createDistribution(ctx, awsEnvironment, input)
		if err != nil {
			return fail(err)
		}
		input.DistributionID = distribution.ID
		input.DistributionARN = distribution.ARN
		input.DomainName = distribution.DomainName
		input.CDNURL = "https://" + distribution.DomainName
		input.Status = strings.ToLower(distribution.Status)
		input.UpdatedAt = s.now()
		if err := s.store.SaveStaticCDN(ctx, input); err != nil {
			return fail(err)
		}
	}
	if err := s.putBucketPolicy(ctx, awsEnvironment, input); err != nil {
		return fail(err)
	}
	input.LastError = ""
	if input.Status == "" || input.Status == "creating" {
		input.Status = "inprogress"
	}
	input.UpdatedAt = s.now()
	if err := s.store.SaveStaticCDN(ctx, input); err != nil {
		return Resource{}, err
	}
	return input, nil
}

func (s *Service) Refresh(ctx context.Context, project, environment, bucket string) (Resource, error) {
	resource, err := s.store.GetStaticCDN(ctx, project, environment, bucket)
	if errors.Is(err, os.ErrNotExist) {
		return Resource{}, ErrNotFound
	}
	if err != nil {
		return Resource{}, err
	}
	if resource.DistributionID == "" {
		return resource, nil
	}
	awsEnvironment, err := s.awsEnvironment(ctx, project, resource.Region)
	if err != nil {
		return Resource{}, err
	}
	payload, err := s.run(ctx, awsEnvironment, "cloudfront", "get-distribution", "--id", resource.DistributionID)
	if err != nil {
		if commandContains(err, "NoSuchDistribution") {
			resource.Status = "deleted"
			resource.LastError = ""
			_ = s.store.SaveStaticCDN(ctx, resource)
			return resource, nil
		}
		return Resource{}, err
	}
	var response struct {
		Distribution distributionResponse `json:"Distribution"`
	}
	if err := json.Unmarshal(payload, &response); err != nil {
		return Resource{}, fmt.Errorf("解析 CloudFront 状态失败: %w", err)
	}
	resource.Status = strings.ToLower(response.Distribution.Status)
	resource.DomainName = response.Distribution.DomainName
	resource.CDNURL = "https://" + response.Distribution.DomainName
	resource.DistributionARN = response.Distribution.ARN
	resource.LastError = ""
	resource.UpdatedAt = s.now()
	if err := s.store.SaveStaticCDN(ctx, resource); err != nil {
		return Resource{}, err
	}
	return resource, nil
}

func (s *Service) Objects(ctx context.Context, project, environment, bucket, prefix string) ([]Object, error) {
	resource, awsEnvironment, err := s.resourceEnvironment(ctx, project, environment, bucket)
	if err != nil {
		return nil, err
	}
	prefix = strings.TrimPrefix(strings.TrimSpace(prefix), "/")
	if !validObjectPrefix(prefix) {
		return nil, ErrInvalid
	}
	payload, err := s.run(ctx, awsEnvironment, "s3api", "list-objects-v2", "--bucket", resource.BucketName, "--prefix", prefix, "--max-keys", "1000", "--region", resource.Region)
	if err != nil {
		return nil, err
	}
	var response struct {
		Contents []struct {
			Key          string    `json:"Key"`
			Size         int64     `json:"Size"`
			ETag         string    `json:"ETag"`
			LastModified time.Time `json:"LastModified"`
		} `json:"Contents"`
	}
	if err := json.Unmarshal(payload, &response); err != nil {
		return nil, fmt.Errorf("解析 S3 文件列表失败: %w", err)
	}
	items := make([]Object, 0, len(response.Contents))
	for _, item := range response.Contents {
		items = append(items, Object{
			Key: item.Key, Size: item.Size, ETag: strings.Trim(item.ETag, `"`),
			LastModified: item.LastModified, CDNURL: objectCDNURL(resource, item.Key),
		})
	}
	return items, nil
}

func (s *Service) AuthorizeUpload(ctx context.Context, project, environment, bucket, key string) (UploadAuthorization, error) {
	resource, awsEnvironment, err := s.resourceEnvironment(ctx, project, environment, bucket)
	if err != nil {
		return UploadAuthorization{}, err
	}
	key = normalizeObjectKey(key)
	if !validObjectKey(key) {
		return UploadAuthorization{}, ErrInvalid
	}
	credential := credentialFromEnvironment(awsEnvironment)
	if credential.AccessKeyID == "" || credential.SecretAccessKey == "" {
		return UploadAuthorization{}, errors.New("项目 AWS 凭据不可用")
	}
	now := s.now()
	uploadURL := presignedPUTURL(resource.BucketName, resource.Region, key, credential, now, 15*time.Minute)
	return UploadAuthorization{
		Key: key, Method: "PUT", UploadURL: uploadURL, CDNURL: objectCDNURL(resource, key),
		ExpiresAt: now.Add(15 * time.Minute),
	}, nil
}

// UploadObject provides a same-origin fallback when a browser cannot reach a
// presigned S3 URL because of a local network policy, extension, proxy, or
// transient CORS failure. The HTTP layer enforces the request size limit. A
// private temporary file is used because aws s3api put-object requires a file
// path for binary payloads; it is removed on every return path.
func (s *Service) UploadObject(ctx context.Context, project, environment, bucket, key, contentType string, body io.Reader) error {
	resource, awsEnvironment, err := s.resourceEnvironment(ctx, project, environment, bucket)
	if err != nil {
		return err
	}
	key = normalizeObjectKey(key)
	if !validObjectKey(key) || body == nil {
		return ErrInvalid
	}
	temporary, err := os.CreateTemp("", "ops-static-cdn-upload-*")
	if err != nil {
		return fmt.Errorf("创建上传缓冲文件失败: %w", err)
	}
	temporaryPath := temporary.Name()
	defer func() { _ = os.Remove(temporaryPath) }()
	if _, err := io.Copy(temporary, body); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("保存上传缓冲文件失败: %w", err)
	}
	arguments := []string{
		"s3api", "put-object", "--bucket", resource.BucketName, "--key", key,
		"--body", temporaryPath, "--content-type", safeContentType(contentType), "--region", resource.Region,
	}
	_, err = s.run(ctx, awsEnvironment, arguments...)
	return err
}

func (s *Service) DeleteObject(ctx context.Context, project, environment, bucket, key string) error {
	resource, awsEnvironment, err := s.resourceEnvironment(ctx, project, environment, bucket)
	if err != nil {
		return err
	}
	key = normalizeObjectKey(key)
	if !validObjectKey(key) {
		return ErrInvalid
	}
	_, err = s.run(ctx, awsEnvironment, "s3api", "delete-object", "--bucket", resource.BucketName, "--key", key, "--region", resource.Region)
	return err
}

func (s *Service) Invalidate(ctx context.Context, project, environment, bucket string, paths []string) error {
	resource, awsEnvironment, err := s.resourceEnvironment(ctx, project, environment, bucket)
	if err != nil {
		return err
	}
	if resource.DistributionID == "" {
		return ErrNotFound
	}
	normalized := make([]string, 0, len(paths))
	for _, item := range paths {
		item = "/" + strings.TrimPrefix(strings.TrimSpace(item), "/")
		if item == "/" || len(item) > 1025 || strings.ContainsAny(item, "\r\n\x00") {
			return ErrInvalid
		}
		normalized = append(normalized, item)
	}
	if len(normalized) == 0 || len(normalized) > 100 {
		return ErrInvalid
	}
	arguments := []string{"cloudfront", "create-invalidation", "--distribution-id", resource.DistributionID, "--paths"}
	arguments = append(arguments, normalized...)
	_, err = s.run(ctx, awsEnvironment, arguments...)
	return err
}

func (s *Service) Delete(ctx context.Context, project, environment, bucket string) (Resource, error) {
	resource, awsEnvironment, err := s.resourceEnvironment(ctx, project, environment, bucket)
	if err != nil {
		return Resource{}, err
	}
	if resource.Status == "failed" && resource.DistributionID == "" && resource.OACID == "" &&
		(strings.Contains(resource.LastError, "名称已被占用") || strings.Contains(resource.LastError, "BucketAlready")) {
		if err := s.store.DeleteStaticCDN(ctx, project, environment, bucket); err != nil {
			return Resource{}, err
		}
		resource.Status = "deleted"
		return resource, nil
	}
	objects, err := s.Objects(ctx, project, environment, bucket, "")
	if err != nil {
		return Resource{}, err
	}
	if len(objects) > 0 {
		return Resource{}, ErrBucketNotEmpty
	}
	if resource.DistributionID != "" {
		payload, configErr := s.run(ctx, awsEnvironment, "cloudfront", "get-distribution-config", "--id", resource.DistributionID)
		if configErr != nil && !commandContains(configErr, "NoSuchDistribution") {
			return Resource{}, configErr
		}
		if configErr == nil {
			var response struct {
				ETag               string         `json:"ETag"`
				DistributionConfig map[string]any `json:"DistributionConfig"`
			}
			if err := json.Unmarshal(payload, &response); err != nil {
				return Resource{}, err
			}
			enabled, _ := response.DistributionConfig["Enabled"].(bool)
			statusPayload, statusErr := s.run(ctx, awsEnvironment, "cloudfront", "get-distribution", "--id", resource.DistributionID)
			if statusErr != nil {
				return Resource{}, statusErr
			}
			var statusResponse struct {
				Distribution distributionResponse `json:"Distribution"`
			}
			if err := json.Unmarshal(statusPayload, &statusResponse); err != nil {
				return Resource{}, err
			}
			if enabled {
				response.DistributionConfig["Enabled"] = false
				configJSON, _ := json.Marshal(response.DistributionConfig)
				if _, err := s.run(ctx, awsEnvironment, "cloudfront", "update-distribution", "--id", resource.DistributionID, "--if-match", response.ETag, "--distribution-config", string(configJSON)); err != nil {
					return Resource{}, err
				}
				resource.Status = "disabling"
				resource.UpdatedAt = s.now()
				_ = s.store.SaveStaticCDN(ctx, resource)
				return resource, ErrDeletePending
			}
			if !strings.EqualFold(statusResponse.Distribution.Status, "Deployed") {
				resource.Status = "disabling"
				_ = s.store.SaveStaticCDN(ctx, resource)
				return resource, ErrDeletePending
			}
			if _, err := s.run(ctx, awsEnvironment, "cloudfront", "delete-distribution", "--id", resource.DistributionID, "--if-match", response.ETag); err != nil {
				return Resource{}, err
			}
		}
	}
	if resource.OACID != "" {
		payload, getErr := s.run(ctx, awsEnvironment, "cloudfront", "get-origin-access-control", "--id", resource.OACID)
		if getErr == nil {
			var response struct {
				ETag string `json:"ETag"`
			}
			if err := json.Unmarshal(payload, &response); err == nil && response.ETag != "" {
				if _, err := s.run(ctx, awsEnvironment, "cloudfront", "delete-origin-access-control", "--id", resource.OACID, "--if-match", response.ETag); err != nil && !commandContains(err, "NoSuchOriginAccessControl") {
					return Resource{}, err
				}
			}
		}
	}
	_, _ = s.run(ctx, awsEnvironment, "s3api", "delete-bucket-policy", "--bucket", resource.BucketName, "--region", resource.Region)
	if _, err := s.run(ctx, awsEnvironment, "s3api", "delete-bucket", "--bucket", resource.BucketName, "--region", resource.Region); err != nil {
		return Resource{}, err
	}
	if err := s.store.DeleteStaticCDN(ctx, project, environment, bucket); err != nil {
		return Resource{}, err
	}
	resource.Status = "deleted"
	return resource, nil
}

func normalizeResource(input *Resource) {
	input.ProjectKey = strings.TrimSpace(strings.ToLower(input.ProjectKey))
	input.EnvironmentKey = strings.TrimSpace(strings.ToLower(input.EnvironmentKey))
	input.DisplayName = strings.TrimSpace(input.DisplayName)
	input.BucketName = strings.TrimSpace(strings.ToLower(input.BucketName))
	input.Region = strings.TrimSpace(strings.ToLower(input.Region))
	origins := make([]string, 0, len(input.CORSOrigins))
	seen := make(map[string]bool)
	for _, origin := range input.CORSOrigins {
		origin = strings.TrimSpace(origin)
		if origin != "" && !seen[origin] {
			origins = append(origins, origin)
			seen[origin] = true
		}
	}
	if len(origins) == 0 {
		origins = []string{"*"}
	}
	input.CORSOrigins = origins
}

func validateResource(input Resource) error {
	if input.ProjectKey == "" || input.EnvironmentKey == "" || input.DisplayName == "" || len([]rune(input.DisplayName)) > 128 {
		return ErrInvalid
	}
	if !bucketPattern.MatchString(input.BucketName) || strings.Contains(input.BucketName, "--") ||
		strings.HasPrefix(input.BucketName, "xn--") || strings.HasPrefix(input.BucketName, "sthree-") ||
		strings.HasPrefix(input.BucketName, "amzn-s3-demo-") || strings.HasSuffix(input.BucketName, "-s3alias") ||
		strings.HasSuffix(input.BucketName, "--ol-s3") || strings.HasSuffix(input.BucketName, ".mrap") {
		return fmt.Errorf("%w：S3 名称须为 3–63 位小写字母、数字或连字符，且不要使用 AWS 保留前后缀", ErrInvalid)
	}
	if !regionPattern.MatchString(input.Region) {
		return fmt.Errorf("%w：AWS Region 格式不正确", ErrInvalid)
	}
	if len(input.CORSOrigins) == 0 || len(input.CORSOrigins) > 20 {
		return ErrInvalid
	}
	for _, origin := range input.CORSOrigins {
		if origin == "*" {
			if len(input.CORSOrigins) != 1 {
				return fmt.Errorf("%w：跨域来源使用 * 时不能再填写其他来源", ErrInvalid)
			}
			continue
		}
		parsed, err := url.Parse(origin)
		if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" || parsed.Path != "" {
			return fmt.Errorf("%w：跨域来源须为 * 或完整的 http/https Origin", ErrInvalid)
		}
	}
	return nil
}

func (s *Service) awsEnvironment(ctx context.Context, project, region string) ([]string, error) {
	if s.credentials == nil {
		return nil, errors.New("项目 AWS 凭据服务不可用")
	}
	environment, err := s.credentials.Environment(ctx, project)
	if err != nil {
		return nil, err
	}
	return append(environment, "AWS_REGION="+region, "AWS_DEFAULT_REGION="+region), nil
}

func (s *Service) resourceEnvironment(ctx context.Context, project, environment, bucket string) (Resource, []string, error) {
	resource, err := s.store.GetStaticCDN(ctx, project, environment, bucket)
	if errors.Is(err, os.ErrNotExist) {
		return Resource{}, nil, ErrNotFound
	}
	if err != nil {
		return Resource{}, nil, err
	}
	awsEnvironment, err := s.awsEnvironment(ctx, project, resource.Region)
	return resource, awsEnvironment, err
}

func (s *Service) createBucket(ctx context.Context, environment []string, resource Resource) error {
	arguments := []string{"s3api", "create-bucket", "--bucket", resource.BucketName, "--region", resource.Region}
	if resource.Region != "us-east-1" {
		arguments = append(arguments, "--create-bucket-configuration", `{"LocationConstraint":"`+resource.Region+`"}`)
	}
	_, err := s.run(ctx, environment, arguments...)
	if err != nil {
		if commandContains(err, "BucketAlreadyExists") || commandContains(err, "BucketAlreadyOwnedByYou") {
			return fmt.Errorf("S3 Bucket 名称已被占用，请更换名称: %w", ErrConflict)
		}
	}
	return err
}

func (s *Service) configureBucket(ctx context.Context, environment []string, resource Resource) error {
	commands := [][]string{
		{"s3api", "put-bucket-ownership-controls", "--bucket", resource.BucketName, "--ownership-controls", `{"Rules":[{"ObjectOwnership":"BucketOwnerEnforced"}]}`, "--region", resource.Region},
		{"s3api", "put-public-access-block", "--bucket", resource.BucketName, "--public-access-block-configuration", `{"BlockPublicAcls":true,"IgnorePublicAcls":true,"BlockPublicPolicy":true,"RestrictPublicBuckets":true}`, "--region", resource.Region},
		{"s3api", "put-bucket-encryption", "--bucket", resource.BucketName, "--server-side-encryption-configuration", `{"Rules":[{"ApplyServerSideEncryptionByDefault":{"SSEAlgorithm":"AES256"}}]}`, "--region", resource.Region},
	}
	cors := map[string]any{"CORSRules": []any{map[string]any{
		"AllowedOrigins": resource.CORSOrigins, "AllowedHeaders": []string{"*"},
		"AllowedMethods": []string{"GET", "HEAD", "PUT", "POST"}, "ExposeHeaders": []string{"ETag"},
		"MaxAgeSeconds": 3600,
	}}}
	corsJSON, _ := json.Marshal(cors)
	commands = append(commands, []string{"s3api", "put-bucket-cors", "--bucket", resource.BucketName, "--cors-configuration", string(corsJSON), "--region", resource.Region})
	for _, command := range commands {
		if _, err := s.run(ctx, environment, command...); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) createOAC(ctx context.Context, environment []string, resource Resource) (string, error) {
	sum := sha256.Sum256([]byte(resource.ProjectKey + "/" + resource.EnvironmentKey + "/" + resource.BucketName))
	config := map[string]string{
		"Name": "ops-static-cdn-" + hex.EncodeToString(sum[:6]), "Description": resource.ProjectKey + "/" + resource.EnvironmentKey + " " + resource.BucketName,
		"SigningProtocol": "sigv4", "SigningBehavior": "always", "OriginAccessControlOriginType": "s3",
	}
	configJSON, _ := json.Marshal(config)
	payload, err := s.run(ctx, environment, "cloudfront", "create-origin-access-control", "--origin-access-control-config", string(configJSON))
	if err != nil {
		return "", err
	}
	var response struct {
		OriginAccessControl struct {
			ID string `json:"Id"`
		} `json:"OriginAccessControl"`
	}
	if err := json.Unmarshal(payload, &response); err != nil || response.OriginAccessControl.ID == "" {
		return "", errors.New("CloudFront OAC 创建成功但返回结果无法识别")
	}
	return response.OriginAccessControl.ID, nil
}

type distributionResponse struct {
	ID         string `json:"Id"`
	ARN        string `json:"ARN"`
	DomainName string `json:"DomainName"`
	Status     string `json:"Status"`
}

func (s *Service) createDistribution(ctx context.Context, environment []string, resource Resource) (distributionResponse, error) {
	originID := "s3-" + resource.BucketName
	config := map[string]any{
		"CallerReference": resource.ProjectKey + "-" + resource.EnvironmentKey + "-" + resource.BucketName + "-" + strconv.FormatInt(s.now().UnixNano(), 10),
		"Comment":         resource.DisplayName + " (" + resource.ProjectKey + "/" + resource.EnvironmentKey + ")",
		"Enabled":         true,
		"IsIPV6Enabled":   true,
		"HttpVersion":     "http2and3",
		"PriceClass":      "PriceClass_100",
		"Origins": map[string]any{"Quantity": 1, "Items": []any{map[string]any{
			"Id": originID, "DomainName": resource.BucketName + ".s3." + resource.Region + ".amazonaws.com",
			"S3OriginConfig": map[string]any{"OriginAccessIdentity": ""}, "OriginAccessControlId": resource.OACID,
		}}},
		"DefaultCacheBehavior": map[string]any{
			"TargetOriginId": originID, "ViewerProtocolPolicy": "redirect-to-https", "Compress": true,
			"CachePolicyId": cachingOptimizedPolicyID, "ResponseHeadersPolicyId": corsSecurityPolicyID,
			"TrustedSigners":   map[string]any{"Enabled": false, "Quantity": 0},
			"TrustedKeyGroups": map[string]any{"Enabled": false, "Quantity": 0},
			"AllowedMethods":   map[string]any{"Quantity": 2, "Items": []string{"GET", "HEAD"}, "CachedMethods": map[string]any{"Quantity": 2, "Items": []string{"GET", "HEAD"}}},
		},
		"Restrictions":      map[string]any{"GeoRestriction": map[string]any{"RestrictionType": "none", "Quantity": 0}},
		"ViewerCertificate": map[string]any{"CloudFrontDefaultCertificate": true, "MinimumProtocolVersion": "TLSv1", "CertificateSource": "cloudfront"},
	}
	configJSON, _ := json.Marshal(config)
	payload, err := s.run(ctx, environment, "cloudfront", "create-distribution", "--distribution-config", string(configJSON))
	if err != nil {
		return distributionResponse{}, err
	}
	var response struct {
		Distribution distributionResponse `json:"Distribution"`
	}
	if err := json.Unmarshal(payload, &response); err != nil || response.Distribution.ID == "" || response.Distribution.DomainName == "" {
		return distributionResponse{}, errors.New("CloudFront Distribution 创建成功但返回结果无法识别")
	}
	return response.Distribution, nil
}

func (s *Service) putBucketPolicy(ctx context.Context, environment []string, resource Resource) error {
	if resource.DistributionARN == "" {
		identityPayload, err := s.run(ctx, environment, "sts", "get-caller-identity")
		if err != nil {
			return err
		}
		var identity struct {
			Account string `json:"Account"`
		}
		if err := json.Unmarshal(identityPayload, &identity); err != nil || identity.Account == "" {
			return errors.New("无法识别项目 AWS Account")
		}
		resource.DistributionARN = "arn:aws:cloudfront::" + identity.Account + ":distribution/" + resource.DistributionID
	}
	policy := map[string]any{
		"Version": "2012-10-17",
		"Statement": []any{map[string]any{
			"Sid": "AllowCloudFrontServicePrincipalReadOnly", "Effect": "Allow",
			"Principal": map[string]string{"Service": "cloudfront.amazonaws.com"}, "Action": "s3:GetObject",
			"Resource":  "arn:aws:s3:::" + resource.BucketName + "/*",
			"Condition": map[string]any{"StringEquals": map[string]string{"AWS:SourceArn": resource.DistributionARN}},
		}},
	}
	policyJSON, _ := json.Marshal(policy)
	_, err := s.run(ctx, environment, "s3api", "put-bucket-policy", "--bucket", resource.BucketName, "--policy", string(policyJSON), "--region", resource.Region)
	return err
}

func (s *Service) run(ctx context.Context, environment []string, arguments ...string) ([]byte, error) {
	return s.runner.Run(ctx, environment, append(arguments, "--output", "json", "--no-cli-pager")...)
}

type runtimeCredential struct {
	AccessKeyID, SecretAccessKey, SessionToken string
}

func credentialFromEnvironment(environment []string) runtimeCredential {
	var credential runtimeCredential
	for _, entry := range environment {
		key, value, found := strings.Cut(entry, "=")
		if !found {
			continue
		}
		switch key {
		case "AWS_ACCESS_KEY_ID":
			credential.AccessKeyID = value
		case "AWS_SECRET_ACCESS_KEY":
			credential.SecretAccessKey = value
		case "AWS_SESSION_TOKEN":
			credential.SessionToken = value
		}
	}
	return credential
}

func presignedPUTURL(bucket, region, key string, credential runtimeCredential, now time.Time, lifetime time.Duration) string {
	host := bucket + ".s3." + region + ".amazonaws.com"
	date := now.UTC().Format("20060102")
	credentialScope := date + "/" + region + "/s3/aws4_request"
	query := url.Values{
		"X-Amz-Algorithm":     {"AWS4-HMAC-SHA256"},
		"X-Amz-Credential":    {credential.AccessKeyID + "/" + credentialScope},
		"X-Amz-Date":          {now.UTC().Format("20060102T150405Z")},
		"X-Amz-Expires":       {strconv.FormatInt(int64(lifetime/time.Second), 10)},
		"X-Amz-SignedHeaders": {"host"},
	}
	if credential.SessionToken != "" {
		query.Set("X-Amz-Security-Token", credential.SessionToken)
	}
	canonicalURI := "/" + escapeObjectKey(key)
	canonicalQuery := query.Encode()
	canonicalRequest := "PUT\n" + canonicalURI + "\n" + canonicalQuery + "\nhost:" + host + "\n\nhost\nUNSIGNED-PAYLOAD"
	requestHash := sha256.Sum256([]byte(canonicalRequest))
	stringToSign := "AWS4-HMAC-SHA256\n" + now.UTC().Format("20060102T150405Z") + "\n" + credentialScope + "\n" + hex.EncodeToString(requestHash[:])
	dateKey := hmacSHA256([]byte("AWS4"+credential.SecretAccessKey), date)
	regionKey := hmacSHA256(dateKey, region)
	serviceKey := hmacSHA256(regionKey, "s3")
	signingKey := hmacSHA256(serviceKey, "aws4_request")
	signature := hex.EncodeToString(hmacSHA256(signingKey, stringToSign))
	return "https://" + host + canonicalURI + "?" + canonicalQuery + "&X-Amz-Signature=" + signature
}

func hmacSHA256(key []byte, value string) []byte {
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write([]byte(value))
	return mac.Sum(nil)
}

func objectCDNURL(resource Resource, key string) string {
	return strings.TrimSuffix(resource.CDNURL, "/") + "/" + escapeObjectKey(key)
}

func escapeObjectKey(key string) string {
	segments := strings.Split(key, "/")
	for index := range segments {
		segments[index] = url.PathEscape(segments[index])
	}
	return strings.Join(segments, "/")
}

func normalizeObjectKey(key string) string {
	return strings.TrimPrefix(strings.TrimSpace(key), "/")
}

func validObjectPrefix(prefix string) bool {
	return len(prefix) <= 1024 && !strings.ContainsAny(prefix, "\x00\r\n") && !strings.Contains(prefix, `\`)
}

func validObjectKey(key string) bool {
	if key == "" || len([]byte(key)) > 1024 || !validObjectPrefix(key) || strings.HasSuffix(key, "/") {
		return false
	}
	for _, segment := range strings.Split(key, "/") {
		if segment == "" || segment == "." || segment == ".." {
			return false
		}
	}
	return path.Clean("/"+key) == "/"+key
}

func safeContentType(value string) string {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > 255 || strings.ContainsAny(value, "\r\n\x00") {
		return "application/octet-stream"
	}
	mediaType, parameters, err := mime.ParseMediaType(value)
	if err != nil || mediaType == "" {
		return "application/octet-stream"
	}
	return mime.FormatMediaType(mediaType, parameters)
}

func safeAWSError(err error) string {
	message := strings.TrimSpace(err.Error())
	if len(message) > 1800 {
		message = message[:1800]
	}
	return message
}

func commandContains(err error, value string) bool {
	return err != nil && strings.Contains(strings.ToLower(err.Error()), strings.ToLower(value))
}

func withoutAWSCredentials(environment []string) []string {
	blocked := map[string]bool{
		"AWS_ACCESS_KEY_ID": true, "AWS_SECRET_ACCESS_KEY": true, "AWS_SESSION_TOKEN": true,
		"AWS_PROFILE": true, "AWS_DEFAULT_PROFILE": true, "AWS_REGION": true, "AWS_DEFAULT_REGION": true,
		"AWS_WEB_IDENTITY_TOKEN_FILE": true, "AWS_ROLE_ARN": true, "AWS_ROLE_SESSION_NAME": true,
		"AWS_CONTAINER_CREDENTIALS_FULL_URI": true, "AWS_CONTAINER_CREDENTIALS_RELATIVE_URI": true,
	}
	result := make([]string, 0, len(environment))
	for _, entry := range environment {
		key, _, _ := strings.Cut(entry, "=")
		if !blocked[key] {
			result = append(result, entry)
		}
	}
	sort.Strings(result)
	return result
}

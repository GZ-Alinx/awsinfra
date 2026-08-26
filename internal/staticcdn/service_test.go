package staticcdn

import (
	"context"
	"errors"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"
)

func TestValidateResourceDefaultsWildcardCORS(t *testing.T) {
	resource := Resource{
		ProjectKey: "KBP", EnvironmentKey: "TEST", DisplayName: "静态资源",
		BucketName: "kbp-game-assets-test", Region: "ap-southeast-1",
	}
	normalizeResource(&resource)
	if err := validateResource(resource); err != nil {
		t.Fatal(err)
	}
	if resource.ProjectKey != "kbp" || len(resource.CORSOrigins) != 1 || resource.CORSOrigins[0] != "*" {
		t.Fatalf("unexpected normalized resource: %#v", resource)
	}
}

func TestValidateResourceRejectsUnsafeBucketAndMixedWildcard(t *testing.T) {
	for _, resource := range []Resource{
		{ProjectKey: "kbp", EnvironmentKey: "test", DisplayName: "x", BucketName: "Bucket.With.Dot", Region: "ap-southeast-1", CORSOrigins: []string{"*"}},
		{ProjectKey: "kbp", EnvironmentKey: "test", DisplayName: "x", BucketName: "valid-bucket-name", Region: "ap-southeast-1", CORSOrigins: []string{"*", "https://example.com"}},
	} {
		normalizeResource(&resource)
		if err := validateResource(resource); err == nil {
			t.Fatalf("invalid resource accepted: %#v", resource)
		}
	}
}

func TestPresignedPUTURLIsScopedAndTemporary(t *testing.T) {
	now := time.Date(2026, 7, 28, 4, 0, 0, 0, time.UTC)
	raw := presignedPUTURL("kbp-assets-test", "ap-southeast-1", "images/logo 中文.png", runtimeCredential{
		AccessKeyID: "AKIAEXAMPLE12345678", SecretAccessKey: "secret", SessionToken: "session-token",
	}, now, 15*time.Minute)
	parsed, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.Host != "kbp-assets-test.s3.ap-southeast-1.amazonaws.com" || !strings.Contains(parsed.EscapedPath(), "images/logo%20%E4%B8%AD%E6%96%87.png") {
		t.Fatalf("unexpected upload URL: %s", raw)
	}
	query := parsed.Query()
	if query.Get("X-Amz-Expires") != "900" || query.Get("X-Amz-Security-Token") != "session-token" || query.Get("X-Amz-Signature") == "" {
		t.Fatalf("missing signing fields: %v", query)
	}
}

type memoryStore struct{ item *Resource }

func (s *memoryStore) ListStaticCDNs(context.Context, string, string) ([]Resource, error) {
	if s.item == nil {
		return []Resource{}, nil
	}
	return []Resource{*s.item}, nil
}
func (s *memoryStore) GetStaticCDN(context.Context, string, string, string) (Resource, error) {
	if s.item == nil {
		return Resource{}, os.ErrNotExist
	}
	return *s.item, nil
}
func (s *memoryStore) SaveStaticCDN(_ context.Context, item Resource) error {
	copy := item
	s.item = &copy
	return nil
}
func (s *memoryStore) DeleteStaticCDN(context.Context, string, string, string) error {
	if s.item == nil {
		return os.ErrNotExist
	}
	s.item = nil
	return nil
}

type fixedCredentials struct{}

func (fixedCredentials) Environment(context.Context, string) ([]string, error) {
	return []string{"AWS_ACCESS_KEY_ID=AKIAEXAMPLE12345678", "AWS_SECRET_ACCESS_KEY=secret"}, nil
}

type fakeRunner struct {
	commands      []string
	uploadPayload []byte
	uploadPath    string
}

func (r *fakeRunner) Run(_ context.Context, _ []string, arguments ...string) ([]byte, error) {
	command := strings.Join(arguments, " ")
	r.commands = append(r.commands, command)
	if strings.HasPrefix(command, "s3api put-object ") {
		for index := range arguments {
			if arguments[index] == "--body" && index+1 < len(arguments) {
				r.uploadPath = arguments[index+1]
				r.uploadPayload, _ = os.ReadFile(r.uploadPath)
			}
		}
	}
	switch {
	case strings.HasPrefix(command, "cloudfront create-origin-access-control "):
		return []byte(`{"OriginAccessControl":{"Id":"E-OAC"}}`), nil
	case strings.HasPrefix(command, "cloudfront create-distribution "):
		return []byte(`{"Distribution":{"Id":"D123","ARN":"arn:aws:cloudfront::123456789012:distribution/D123","DomainName":"d123.cloudfront.net","Status":"InProgress"}}`), nil
	default:
		return []byte(`{}`), nil
	}
}

func TestUploadObjectUsesPrivateTemporaryFileAndContentType(t *testing.T) {
	store := &memoryStore{item: &Resource{
		ProjectKey: "kbp", EnvironmentKey: "test", BucketName: "kbp-game-assets-test", Region: "ap-southeast-1",
	}}
	runner := &fakeRunner{}
	service := &Service{store: store, credentials: fixedCredentials{}, runner: runner}
	if err := service.UploadObject(context.Background(), "kbp", "test", "kbp-game-assets-test", "images/截屏 01.png", "image/png", strings.NewReader("png-data")); err != nil {
		t.Fatal(err)
	}
	if string(runner.uploadPayload) != "png-data" {
		t.Fatalf("unexpected upload payload: %q", runner.uploadPayload)
	}
	joined := strings.Join(runner.commands, "\n")
	if !strings.Contains(joined, "s3api put-object --bucket kbp-game-assets-test --key images/截屏 01.png") || !strings.Contains(joined, "--content-type image/png") {
		t.Fatalf("unexpected upload command: %s", joined)
	}
	if runner.uploadPath == "" {
		t.Fatal("temporary upload path was not captured")
	}
	if _, err := os.Stat(runner.uploadPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("temporary upload file was not removed: %v", err)
	}
	if safeContentType("text/plain; charset=utf-8") != "text/plain; charset=utf-8" || safeContentType("bad\nvalue") != "application/octet-stream" {
		t.Fatal("content type sanitization failed")
	}
}

func TestCreateBuildsPrivateS3AndCloudFrontBinding(t *testing.T) {
	store := &memoryStore{}
	runner := &fakeRunner{}
	service := &Service{
		store: store, credentials: fixedCredentials{}, runner: runner,
		now: func() time.Time { return time.Date(2026, 7, 28, 4, 0, 0, 0, time.UTC) },
	}
	resource, err := service.Create(context.Background(), Resource{
		ProjectKey: "kbp", EnvironmentKey: "test", DisplayName: "静态资源",
		BucketName: "kbp-game-assets-test", Region: "ap-southeast-1", CORSOrigins: []string{"*"},
	}, "admin")
	if err != nil {
		t.Fatal(err)
	}
	if resource.Status != "inprogress" || resource.CDNURL != "https://d123.cloudfront.net" || resource.OACID != "E-OAC" {
		t.Fatalf("unexpected resource: %#v", resource)
	}
	joined := strings.Join(runner.commands, "\n")
	for _, required := range []string{
		"s3api put-public-access-block", "s3api put-bucket-cors",
		"cloudfront create-origin-access-control", "cloudfront create-distribution",
		"s3api put-bucket-policy",
	} {
		if !strings.Contains(joined, required) {
			t.Fatalf("missing command %q:\n%s", required, joined)
		}
	}
	if store.item == nil || store.item.DistributionID != "D123" {
		t.Fatal(errors.New("resource state was not persisted"))
	}
}

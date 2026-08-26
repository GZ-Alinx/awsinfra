package staticcdn_test

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"ops-deploy-platform/internal/appconfig"
	"ops-deploy-platform/internal/awscredentials"
	"ops-deploy-platform/internal/persistence"
	"ops-deploy-platform/internal/staticcdn"
)

func TestProxyUploadIntegration(t *testing.T) {
	project := strings.TrimSpace(os.Getenv("OPS_STATIC_CDN_TEST_PROJECT"))
	environment := strings.TrimSpace(os.Getenv("OPS_STATIC_CDN_TEST_ENVIRONMENT"))
	bucket := strings.TrimSpace(os.Getenv("OPS_STATIC_CDN_TEST_BUCKET"))
	if project == "" || environment == "" || bucket == "" {
		t.Skip("set OPS_STATIC_CDN_TEST_PROJECT, OPS_STATIC_CDN_TEST_ENVIRONMENT and OPS_STATIC_CDN_TEST_BUCKET to run")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	config, err := appconfig.Load("../../config.yaml")
	if err != nil {
		t.Fatal(err)
	}
	store, err := persistence.Open(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	credentials, err := awscredentials.New(config, store)
	if err != nil {
		t.Fatal(err)
	}
	service := staticcdn.New(config, store, credentials)
	key := "_ops-deploy-diagnostics/中文 空格-proxy-upload.txt"
	if err := service.UploadObject(ctx, project, environment, bucket, key, "text/plain; charset=utf-8", strings.NewReader("ops-deploy proxy upload diagnostic\n")); err != nil {
		t.Fatal(err)
	}
	defer func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cleanupCancel()
		if err := service.DeleteObject(cleanupCtx, project, environment, bucket, key); err != nil {
			t.Errorf("cleanup diagnostic object: %v", err)
		}
	}()
	objects, err := service.Objects(ctx, project, environment, bucket, "_ops-deploy-diagnostics/")
	if err != nil {
		t.Fatal(err)
	}
	for _, object := range objects {
		if object.Key == key && object.Size > 0 {
			return
		}
	}
	t.Fatalf("uploaded object %q was not returned by S3", key)
}

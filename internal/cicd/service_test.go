package cicd

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"ops-deploy-platform/internal/appconfig"
)

func TestGitLabWebhookSecretLifecycle(t *testing.T) {
	store := newMemoryStore()
	key := make([]byte, 32)
	for index := range key {
		key[index] = byte(index + 1)
	}
	t.Setenv("TEST_WEBHOOK_CICD_KEY", base64.StdEncoding.EncodeToString(key))
	service, err := New(&appconfig.Config{Security: appconfig.SecurityConfig{CredentialKeyEnv: "TEST_WEBHOOK_CICD_KEY"}}, store)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	job := Job{ProjectKey: "project-a", Key: "release", Enabled: true, TriggerMode: "manual", TriggerBranch: "main"}
	if err := store.SaveCICDJob(ctx, job); err != nil {
		t.Fatal(err)
	}
	if _, err := service.RotateWebhookSecret(ctx, "project-a", "release"); !errors.Is(err, ErrConflict) {
		t.Fatalf("RotateWebhookSecret(manual) error = %v, want conflict", err)
	}
	job.TriggerMode = "gitlab_push"
	if err := store.SaveCICDJob(ctx, job); err != nil {
		t.Fatal(err)
	}
	secret, err := service.RotateWebhookSecret(ctx, "project-a", "release")
	if err != nil {
		t.Fatal(err)
	}
	if len(secret.SecretToken) < 40 {
		t.Fatalf("webhook token is unexpectedly short: %d", len(secret.SecretToken))
	}
	stored, err := store.GetCICDJob(ctx, "project-a", "release")
	if err != nil {
		t.Fatal(err)
	}
	if stored.WebhookSecretHash == "" || stored.WebhookSecretHash == secret.SecretToken || !stored.WebhookConfigured {
		t.Fatalf("webhook token was not stored as a digest: %#v", stored)
	}
	encoded, err := json.Marshal(stored)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), stored.WebhookSecretHash) || strings.Contains(string(encoded), secret.SecretToken) {
		t.Fatal("webhook secret material leaked through the Job JSON contract")
	}
	if _, err := service.AuthenticateGitLabWebhook(ctx, "project-a", "release", "wrong-token"); !errors.Is(err, ErrWebhookUnauthorized) {
		t.Fatalf("AuthenticateGitLabWebhook(wrong) error = %v", err)
	}
	if authenticated, err := service.AuthenticateGitLabWebhook(ctx, "project-a", "release", secret.SecretToken); err != nil || authenticated.Key != "release" {
		t.Fatalf("AuthenticateGitLabWebhook(valid) = %#v, %v", authenticated, err)
	}
	replacement, err := service.RotateWebhookSecret(ctx, "project-a", "release")
	if err != nil {
		t.Fatal(err)
	}
	if replacement.SecretToken == secret.SecretToken {
		t.Fatal("rotated webhook token did not change")
	}
	if _, err := service.AuthenticateGitLabWebhook(ctx, "project-a", "release", secret.SecretToken); !errors.Is(err, ErrWebhookUnauthorized) {
		t.Fatalf("old webhook token remains valid after rotation: %v", err)
	}
}

func TestGeneratedJobUsesOneEnvironmentScopedDeliveryRepository(t *testing.T) {
	store := newMemoryStore()
	key := make([]byte, 32)
	for index := range key {
		key[index] = byte(index + 11)
	}
	t.Setenv("TEST_JOB_ENV_CICD_KEY", base64.StdEncoding.EncodeToString(key))
	service, err := New(&appconfig.Config{Security: appconfig.SecurityConfig{CredentialKeyEnv: "TEST_JOB_ENV_CICD_KEY"}}, store)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if _, err = service.SaveConnection(ctx, "project-a", "", ConnectionInput{Key: "jenkins", EnvironmentKey: "test", DisplayName: "Jenkins", BaseURL: "https://jenkins.example.com", Username: "admin", APIToken: "token"}); err != nil {
		t.Fatal(err)
	}
	repository, err := service.SaveRepository(ctx, "project-a", "", Repository{Key: "ops-delivery", DisplayName: "统一交付仓库", Provider: "gitlab", Purpose: "general", CloneURL: "https://git.example/project-a-ops-delivery.git", DefaultBranch: "main"})
	if err != nil {
		t.Fatal(err)
	}
	credential, err := service.SaveCredential(ctx, "project-a", "", CredentialInput{Key: "gitlab-read", ConnectionKey: "jenkins", DisplayName: "GitLab Read", Kind: "existing", ExternalID: "ops-project-a-gitlab-read"})
	if err != nil {
		t.Fatal(err)
	}
	input := Job{
		Key: "project-a-test-release", EnvironmentKey: "test", DisplayName: "Test Release", ServiceName: "api", ServiceKeys: []string{"api"}, Language: "go",
		JenkinsfileMode: "generated", ConnectionKey: "jenkins", JenkinsJobName: "project-a-test-release", Enabled: true,
		JenkinsfileRepository: repository.Key, JenkinsfileCredential: credential.Key, ManifestRepository: repository.Key, ManifestCredential: credential.Key,
		JenkinsfileContent: "pipeline {\n  agent any\n  stages { stage('test') { steps { echo 'test' } } }\n}",
	}
	job, err := service.SaveJob(ctx, "project-a", "", input)
	if err != nil {
		t.Fatal(err)
	}
	if job.JenkinsfileRepo != repository.CloneURL || job.ManifestRepo != repository.CloneURL || job.JenkinsfilePath != "environments/test/pipelines/project-a-test-release/Jenkinsfile" || job.ManifestPath != "environments/test" || job.Parameters["DEPLOY_ENV"] != "test" || !strings.HasSuffix(job.JenkinsfileContent, "\n") {
		t.Fatalf("generated Job was not isolated by repository path: %#v", job)
	}
	changed := input
	changed.EnvironmentKey = "prod"
	if _, err := service.SaveJob(ctx, "project-a", input.Key, changed); !errors.Is(err, ErrConflict) || !strings.Contains(err.Error(), "不能改为") {
		t.Fatalf("Job environment change error = %v, want immutable environment conflict", err)
	}
	conflict := input
	conflict.Key, conflict.JenkinsJobName = "another-test-release", "another-test-release"
	conflict.JenkinsfilePath = job.JenkinsfilePath
	if _, err := service.SaveJob(ctx, "project-a", "", conflict); !errors.Is(err, ErrConflict) || !strings.Contains(err.Error(), "Jenkinsfile 路径已被") {
		t.Fatalf("duplicate Jenkinsfile path error = %v, want path ownership conflict", err)
	}
}

func TestJenkinsConnectionsAndCredentialsAreHardIsolatedByEnvironment(t *testing.T) {
	store := newMemoryStore()
	key := make([]byte, 32)
	for index := range key {
		key[index] = byte(index + 31)
	}
	t.Setenv("TEST_CICD_ENV_ISOLATION_KEY", base64.StdEncoding.EncodeToString(key))
	service, err := New(&appconfig.Config{Security: appconfig.SecurityConfig{CredentialKeyEnv: "TEST_CICD_ENV_ISOLATION_KEY"}}, store)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	for _, environment := range []string{"test", "prod"} {
		connectionKey := environment + "-jenkins"
		if _, err := service.SaveConnection(ctx, "project-a", "", ConnectionInput{
			Key: connectionKey, EnvironmentKey: environment, DisplayName: environment + " Jenkins",
			BaseURL: "https://" + environment + "-jenkins.example.com", Username: "automation", APIToken: "token-" + environment,
		}); err != nil {
			t.Fatal(err)
		}
		credential, err := service.SaveCredential(ctx, "project-a", "", CredentialInput{
			Key: environment + "-git-read", ConnectionKey: connectionKey,
			DisplayName: environment + " GitLab", Kind: "existing",
		})
		if err != nil {
			t.Fatal(err)
		}
		wantID := "ops-project-a-" + environment + "-git-read"
		if credential.EnvironmentKey != environment || credential.ExternalID != wantID {
			t.Fatalf("%s credential = %#v, want isolated ID %s", environment, credential, wantID)
		}
	}
	testConnections, err := service.ListConnectionsForEnvironment(ctx, "project-a", "test")
	if err != nil || len(testConnections) != 1 || testConnections[0].Key != "test-jenkins" {
		t.Fatalf("test Jenkins scope leaked: %#v, %v", testConnections, err)
	}
	prodCredentials, err := service.ListCredentialsForEnvironment(ctx, "project-a", "prod")
	if err != nil || len(prodCredentials) != 1 || prodCredentials[0].ConnectionKey != "prod-jenkins" {
		t.Fatalf("production credential scope leaked: %#v, %v", prodCredentials, err)
	}
	if _, err := service.GetConnectionForEnvironment(ctx, "project-a", "prod", "test-jenkins"); !errors.Is(err, ErrConflict) || !strings.Contains(err.Error(), "禁止跨环境") {
		t.Fatalf("production accepted test Jenkins: %v", err)
	}
	if _, err := service.SaveJob(ctx, "project-a", "", Job{
		Key: "prod-release", EnvironmentKey: "prod", DisplayName: "Production Release", ServiceName: "api", ServiceKeys: []string{"api"},
		Language: "go", JenkinsfileMode: "existing", ExecutionMode: "serial", FailurePolicy: "stop", TriggerMode: "manual",
		ConnectionKey: "test-jenkins", JenkinsJobName: "prod-release", Enabled: true,
		JenkinsfileRepo: "https://git.example.com/pipelines.git", JenkinsfileBranch: "main", JenkinsfilePath: "prod/Jenkinsfile",
		ManifestRepo: "https://git.example.com/manifests.git", ManifestBranch: "main", ManifestPath: "environments/prod",
	}); !errors.Is(err, ErrConflict) || !strings.Contains(err.Error(), "禁止跨环境") {
		t.Fatalf("production Job accepted test Jenkins: %v", err)
	}
}

func TestJenkinsPipelineLifecycleAndSecretRedaction(t *testing.T) {
	var mu sync.Mutex
	jobCreated := false
	jobDeleted := false
	var jobConfig []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if username, token, ok := r.BasicAuth(); !ok || username != "automation" || token != "test-api-token" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/json":
			w.Header().Set("X-Jenkins", "2.516.1")
			_, _ = io.WriteString(w, `{}`)
		case r.Method == http.MethodGet && r.URL.Path == "/crumbIssuer/api/json":
			_, _ = io.WriteString(w, `{"crumbRequestField":"Jenkins-Crumb","crumb":"test-crumb"}`)
		case r.Method == http.MethodGet && r.URL.Path == "/job/demo-pipeline/config.xml":
			mu.Lock()
			exists := jobCreated && !jobDeleted
			payload := append([]byte(nil), jobConfig...)
			mu.Unlock()
			if !exists {
				http.NotFound(w, r)
				return
			}
			_, _ = w.Write(payload)
		case r.Method == http.MethodPost && r.URL.Path == "/createItem":
			if r.Header.Get("Jenkins-Crumb") != "test-crumb" {
				http.Error(w, "missing crumb", http.StatusForbidden)
				return
			}
			payload, _ := io.ReadAll(r.Body)
			if !strings.Contains(string(payload), "https://git.example/jenkinsfiles.git") || !strings.Contains(string(payload), "MANIFEST_CREDENTIAL_ID") || !strings.Contains(string(payload), "gitlab-project-a") ||
				!strings.Contains(string(payload), "hudson.model.ChoiceParameterDefinition") || !strings.Contains(string(payload), "come-app-user-api") ||
				!strings.Contains(string(payload), "hudson.model.BooleanParameterDefinition") || strings.Contains(string(payload), "test-api-token") || strings.Contains(string(payload), "gitlab-test-token") {
				http.Error(w, "invalid config", http.StatusBadRequest)
				return
			}
			mu.Lock()
			jobCreated = true
			jobConfig = append([]byte(nil), payload...)
			mu.Unlock()
			w.WriteHeader(http.StatusOK)
		case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/credentials/store/system/domain/_/credential/"):
			http.NotFound(w, r)
		case r.Method == http.MethodPost && r.URL.Path == "/credentials/store/system/domain/_/createCredentials":
			payload, _ := io.ReadAll(r.Body)
			if !strings.Contains(string(payload), "gitlab-project-a") || !strings.Contains(string(payload), "gitlab-test-token") {
				http.Error(w, "invalid credential", http.StatusBadRequest)
				return
			}
			w.WriteHeader(http.StatusOK)
		case r.Method == http.MethodPost && r.URL.Path == "/job/demo-pipeline/buildWithParameters":
			_ = r.ParseForm()
			if r.FormValue("JENKINSFILE_CREDENTIAL_ID") != "gitlab-project-a" || r.FormValue("MANIFEST_CREDENTIAL_ID") != "gitlab-project-a" ||
				r.FormValue("server") != "come-app-user-api" || r.FormValue("branch") != "feature/demo" || r.FormValue("REPLICAS") != "3" || r.FormValue("DRY_RUN") != "false" {
				http.Error(w, "missing credential ids", http.StatusBadRequest)
				return
			}
			w.Header().Set("Location", serverURL(r)+"queue/item/1/")
			w.WriteHeader(http.StatusCreated)
		case r.Method == http.MethodPost && r.URL.Path == "/job/demo-pipeline/doDelete":
			if r.Header.Get("Jenkins-Crumb") != "test-crumb" {
				http.Error(w, "missing crumb", http.StatusForbidden)
				return
			}
			mu.Lock()
			jobDeleted = true
			mu.Unlock()
			w.WriteHeader(http.StatusOK)
		case r.Method == http.MethodGet && r.URL.Path == "/queue/item/1/api/json":
			_, _ = io.WriteString(w, `{"executable":{"number":42,"url":"`+serverURL(r)+`job/demo-pipeline/42/"}}`)
		case r.Method == http.MethodGet && r.URL.Path == "/job/demo-pipeline/42/api/json":
			_, _ = io.WriteString(w, `{"building":false,"result":"SUCCESS","url":"`+serverURL(r)+`job/demo-pipeline/42/","timestamp":1700000000000,"duration":1200}`)
		case r.Method == http.MethodGet && r.URL.Path == "/job/demo-pipeline/42/logText/progressiveText":
			w.Header().Set("X-Text-Size", "15")
			w.Header().Set("X-More-Data", "false")
			_, _ = io.WriteString(w, "BUILD SUCCESS\n")
		default:
			http.Error(w, "unexpected route", http.StatusNotFound)
		}
	}))
	defer server.Close()

	store := newMemoryStore()
	key := make([]byte, 32)
	for index := range key {
		key[index] = byte(index + 1)
	}
	t.Setenv("TEST_CICD_KEY", base64.StdEncoding.EncodeToString(key))
	service, err := New(&appconfig.Config{Security: appconfig.SecurityConfig{CredentialKeyEnv: "TEST_CICD_KEY"}}, store)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	connection, err := service.SaveConnection(ctx, "project-a", "", ConnectionInput{Key: "jenkins", EnvironmentKey: "dev", DisplayName: "Jenkins", BaseURL: server.URL, Username: "automation", APIToken: "test-api-token"})
	if err != nil {
		t.Fatal(err)
	}
	if !connection.Configured || strings.Contains(store.connections["project-a/jenkins"].TokenCipher, "test-api-token") {
		t.Fatal("connection secret was not encrypted")
	}
	if tested, err := service.TestConnection(ctx, "project-a", "jenkins"); err != nil || tested.JenkinsVersion != "2.516.1" {
		t.Fatalf("test connection = %#v, %v", tested, err)
	}
	service.SetTunnelProvider(tunnelStub{url: server.URL})
	managed, err := service.SaveManagedConnection(ctx, ManagedConnectionInput{
		Key: "dev-jenkins", ProjectKey: "project-a", EnvironmentKey: "dev", TargetName: "project-a-dev",
		DisplayName: "开发环境 Jenkins", Region: "ap-south-1", ClusterName: "project-a-dev-eks",
		Namespace: "project-a-dev-platform", ServiceName: "jenkins", ServicePort: 8080,
		Username: "automation", Password: "test-api-token",
	})
	if err != nil {
		t.Fatal(err)
	}
	if managed.ConnectionMode != "eks_port_forward" || managed.EnvironmentKey != "dev" || !strings.Contains(managed.BaseURL, ".svc.cluster.local") {
		t.Fatalf("managed connection = %#v", managed)
	}
	if tested, err := service.TestConnection(ctx, "project-a", managed.Key); err != nil || tested.LastCheckStatus != "healthy" {
		t.Fatalf("managed test = %#v, %v", tested, err)
	}
	credential, err := service.SaveCredential(ctx, "project-a", "", CredentialInput{
		Key: "gitlab", ConnectionKey: "jenkins", DisplayName: "GitLab 项目凭据", Kind: "gitlab_token",
		ExternalID: "gitlab-project-a", Username: "oauth2", Password: "gitlab-test-token",
	})
	if err != nil {
		t.Fatal(err)
	}
	credential, err = service.SyncCredential(ctx, "project-a", credential.Key)
	if err != nil || credential.SyncStatus != "ready" || credential.ExternalID != "gitlab-project-a" {
		t.Fatalf("sync GitLab credential = %#v, %v", credential, err)
	}
	if authorized, err := service.AuthorizeGitRelay(ctx, "project-a", credential.Key, "oauth2", "gitlab-test-token"); err != nil || !authorized {
		t.Fatalf("AuthorizeGitRelay(valid) = %t, %v", authorized, err)
	}
	if authorized, err := service.AuthorizeGitRelay(ctx, "project-a", credential.Key, "oauth2", "wrong-token"); err != nil || authorized {
		t.Fatalf("AuthorizeGitRelay(invalid) = %t, %v", authorized, err)
	}
	autoID, err := service.SaveCredential(ctx, "project-a", "", CredentialInput{Key: "existing-git", ConnectionKey: managed.Key, DisplayName: "Jenkins 已有 Git 凭据", Kind: "existing"})
	if err != nil || autoID.ExternalID != "ops-project-a-dev-existing-git" || autoID.EnvironmentKey != "dev" || autoID.SyncStatus != "ready" {
		t.Fatalf("auto-generated credential ID = %#v, %v", autoID, err)
	}
	jenkinsfiles, err := service.SaveRepository(ctx, "project-a", "", Repository{Key: "jenkinsfiles", DisplayName: "Jenkinsfile 仓库", Provider: "gitlab", Purpose: "jenkinsfile", CloneURL: "https://git.example/jenkinsfiles.git", DefaultBranch: "main", DefaultPath: "go/Jenkinsfile"})
	if err != nil {
		t.Fatal(err)
	}
	manifests, err := service.SaveRepository(ctx, "project-a", "", Repository{Key: "manifests", DisplayName: "部署清单仓库", Provider: "gitlab", Purpose: "manifest", CloneURL: "https://git.example/manifests.git", DefaultBranch: "main", DefaultPath: "apps/demo"})
	if err != nil {
		t.Fatal(err)
	}

	job, err := service.SaveJob(ctx, "project-a", "", Job{Key: "demo", DisplayName: "Demo", ServiceName: "demo", Language: "go", ConnectionKey: "jenkins", JenkinsJobName: "demo-pipeline", Enabled: true, JenkinsfileRepository: jenkinsfiles.Key, JenkinsfileCredential: credential.Key, ManifestRepository: manifests.Key, ManifestCredential: credential.Key, EnvironmentPaths: map[string]string{"dev": "apps/demo/dev"}, ParameterDefinitions: []ParameterDefinition{
		{Name: "server", Type: "choice", DefaultValue: "come-app-admin", Choices: []string{"come-app-admin", "come-app-user-api"}, Description: "选择服务", Required: true},
		{Name: "branch", Type: "string", DefaultValue: "main", Description: "源码分支", Required: true},
		{Name: "REPLICAS", Type: "number", DefaultValue: "2", Description: "副本数", Required: true},
		{Name: "DRY_RUN", Type: "boolean", DefaultValue: "false"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if job.JenkinsfileRepo != jenkinsfiles.CloneURL || job.ManifestRepo != manifests.CloneURL || job.JenkinsfilePath != jenkinsfiles.DefaultPath || job.ManifestPath != manifests.DefaultPath {
		t.Fatalf("job repository selection was not resolved: %#v", job)
	}
	if job.SyncStatus != "pending" {
		t.Fatalf("a saved but unsynchronized job must be pending, got %#v", job)
	}
	job, err = service.SyncJob(ctx, "project-a", job.Key)
	if err != nil || job.SyncStatus != "ready" {
		t.Fatalf("sync job = %#v, %v", job, err)
	}
	build, err := service.TriggerBuild(ctx, "project-a", job.Key, "tester", BuildInput{Environment: "dev", ImageTag: "v1", Parameters: map[string]string{"server": "come-app-user-api", "branch": "feature/demo", "REPLICAS": "3"}})
	if err != nil {
		t.Fatal(err)
	}
	duplicate, err := service.TriggerBuild(ctx, "project-a", job.Key, "tester", BuildInput{Environment: "dev", ImageTag: "v1", Parameters: map[string]string{"server": "come-app-user-api", "branch": "feature/demo", "REPLICAS": "3"}})
	if err != nil || duplicate.ID != build.ID || len(store.builds) != 1 {
		t.Fatalf("coalesced Jenkins queue item created a duplicate build: first=%#v duplicate=%#v err=%v", build, duplicate, err)
	}
	build, err = service.refreshBuild(ctx, build)
	if err != nil || build.BuildNumber != 42 || build.Status != "running" {
		t.Fatalf("queue refresh = %#v, %v", build, err)
	}
	build, err = service.refreshBuild(ctx, build)
	if err != nil || build.Status != "succeeded" {
		t.Fatalf("build refresh = %#v, %v", build, err)
	}
	logs, err := service.BuildLogs(ctx, "project-a", build.ID, 0)
	if err != nil || logs.Text != "BUILD SUCCESS\n" || logs.More {
		t.Fatalf("logs = %#v, %v", logs, err)
	}
	job, err = service.RecordJobSyncFailure(ctx, "project-a", job.Key, errors.New("repository unavailable"))
	if err != nil || job.SyncStatus != "failed" || !strings.Contains(job.SyncError, "repository unavailable") {
		t.Fatalf("record GitLab sync failure = %#v, %v", job, err)
	}
	usage, err := service.JobBuildUsage(ctx, "project-a", job.Key)
	if err != nil || usage.TotalBuilds != 1 || usage.ActiveBuilds != 0 || usage.HistoricalBuilds != 1 {
		t.Fatalf("JobBuildUsage() = %#v, %v", usage, err)
	}
	activeJob := job
	activeJob.Key, activeJob.JenkinsJobName = "active-demo", "active-demo"
	if err := store.SaveCICDJob(ctx, activeJob); err != nil {
		t.Fatal(err)
	}
	activeBuild := Build{ID: "active-build", ProjectKey: "project-a", JobKey: activeJob.Key, Environment: "dev", Status: "running", CreatedAt: time.Now().UTC()}
	if err := store.SaveCICDBuild(ctx, activeBuild); err != nil {
		t.Fatal(err)
	}
	if _, err := service.DeleteJob(ctx, "project-a", activeJob.Key, false); !errors.Is(err, ErrConflict) || !strings.Contains(err.Error(), "运行中") {
		t.Fatalf("active build did not block Job deletion with a clear message: %v", err)
	}
	activeBuild.Status = "canceled"
	if err := store.SaveCICDBuild(ctx, activeBuild); err != nil {
		t.Fatal(err)
	}
	if _, err := service.DeleteJob(ctx, "project-a", activeJob.Key, false); err != nil {
		t.Fatalf("completed active Job could not be deleted locally: %v", err)
	}
	deletion, err := service.DeleteJob(ctx, "project-a", job.Key, true)
	if err != nil || !deletion.RemoteDeleted || deletion.HistoricalBuildsRetained != 1 {
		t.Fatalf("DeleteJob() = %#v, %v", deletion, err)
	}
	if _, err := store.GetCICDJob(ctx, "project-a", job.Key); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("deleted platform Job is still present: %v", err)
	}
	if _, err := store.GetCICDBuild(ctx, "project-a", build.ID); err != nil {
		t.Fatalf("completed build history was not retained: %v", err)
	}
	managedJob := job
	managedJob.Key = "managed-demo"
	managedJob.ConnectionKey = managed.Key
	managedJob.SyncStatus = "ready"
	if err := store.SaveCICDJob(ctx, managedJob); err != nil {
		t.Fatal(err)
	}
	if _, err := service.TriggerBuild(ctx, "project-a", managedJob.Key, "tester", BuildInput{Environment: "prod"}); !errors.Is(err, ErrConflict) {
		t.Fatalf("managed Jenkins accepted a cross-environment build: %v", err)
	}
	if err := service.DeleteProjectData(ctx, "project-a"); err != nil {
		t.Fatal(err)
	}
	if len(store.connections) != 0 || len(store.repositories) != 0 || len(store.jobs) != 0 || len(store.builds) != 0 {
		t.Fatal("project CI/CD data was not cleaned")
	}
}

func TestJenkinsConnectionValidationMessages(t *testing.T) {
	key := make([]byte, 32)
	t.Setenv("TEST_CICD_VALIDATION_KEY", base64.StdEncoding.EncodeToString(key))
	service, err := New(&appconfig.Config{Security: appconfig.SecurityConfig{CredentialKeyEnv: "TEST_CICD_VALIDATION_KEY"}}, newMemoryStore())
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name  string
		input ConnectionInput
		want  string
	}{
		{name: "invalid key", input: ConnectionInput{Key: "jenkins_main", EnvironmentKey: "test", DisplayName: "Jenkins", BaseURL: "https://jenkins.example.com", Username: "admin", APIToken: "token"}, want: "连接标识只能使用"},
		{name: "missing scheme", input: ConnectionInput{Key: "jenkins-main", EnvironmentKey: "test", DisplayName: "Jenkins", BaseURL: "jenkins.example.com", Username: "admin", APIToken: "token"}, want: "完整 URL"},
		{name: "unconfirmed insecure remote URL", input: ConnectionInput{Key: "jenkins-main", EnvironmentKey: "test", DisplayName: "Jenkins", BaseURL: "http://jenkins.example.com", Username: "admin", APIToken: "token"}, want: "明确确认允许明文连接"},
		{name: "missing username", input: ConnectionInput{Key: "jenkins-main", EnvironmentKey: "test", DisplayName: "Jenkins", BaseURL: "https://jenkins.example.com", APIToken: "token"}, want: "用户名不能为空"},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			_, err := service.SaveConnection(context.Background(), "project-a", "", test.input)
			if !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("SaveConnection() error = %v, want ErrInvalid containing %q", err, test.want)
			}
		})
	}
	connection, err := service.SaveConnection(context.Background(), "project-a", "", ConnectionInput{
		Key: "jenkins-http", EnvironmentKey: "test", DisplayName: "内网 Jenkins", BaseURL: "http://jenkins.internal:8080",
		Username: "admin", APIToken: "token", AllowInsecureHTTP: true,
	})
	if err != nil || connection.BaseURL != "http://jenkins.internal:8080" || !connection.Configured {
		t.Fatalf("confirmed HTTP Jenkins connection = %#v, %v", connection, err)
	}
}

func TestGeneratedJobUsesProjectRepositoriesAndRunsSourcePreflight(t *testing.T) {
	key := make([]byte, 32)
	t.Setenv("TEST_CICD_RELAY_KEY", base64.StdEncoding.EncodeToString(key))
	store := newMemoryStore()
	service, err := New(&appconfig.Config{Security: appconfig.SecurityConfig{CredentialKeyEnv: "TEST_CICD_RELAY_KEY"}}, store)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if _, err := service.SaveConnection(ctx, "project-a", "", ConnectionInput{Key: "jenkins", EnvironmentKey: "test", DisplayName: "Jenkins", BaseURL: "https://jenkins.example.com", Username: "admin", APIToken: "token"}); err != nil {
		t.Fatal(err)
	}
	for _, repository := range []Repository{
		{Key: "ops-delivery-jenkinsfiles", DisplayName: "流水线仓库", Provider: "gitlab", Purpose: "jenkinsfile", CloneURL: "https://git.example/project-a-jenkinsfiles.git", DefaultBranch: "main"},
		{Key: "ops-delivery-manifests", DisplayName: "清单仓库", Provider: "gitlab", Purpose: "manifest", CloneURL: "https://git.example/project-a-manifests.git", DefaultBranch: "main"},
	} {
		if _, err := service.SaveRepository(ctx, "project-a", "", repository); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := service.SaveCredential(ctx, "project-a", "", CredentialInput{Key: "gitlab-delivery-read", ConnectionKey: "jenkins", DisplayName: "GitLab Read", Kind: "existing", ExternalID: "ops-project-a-gitlab-read"}); err != nil {
		t.Fatal(err)
	}
	job := Job{Key: "gateway", ProjectKey: "project-a", EnvironmentKey: "test", DisplayName: "Gateway", ServiceName: "gateway", ServiceKeys: []string{"gateway"}, Language: "go", JenkinsfileMode: "generated", ConnectionKey: "jenkins", JenkinsJobName: "gateway", Enabled: true, JenkinsfileRepo: "https://git.example/direct.git", JenkinsfileBranch: "main", JenkinsfilePath: "jobs/gateway/Jenkinsfile", JenkinsfileCredential: "legacy", ManifestRepo: "https://git.example/manifests.git", ManifestBranch: "main", ManifestCredential: "legacy", Parameters: map[string]string{"JENKINS_AGENT_MODE": "kubernetes", "barch": "test", "IMAGE_TAG": "old"}, ParameterDefinitions: []ParameterDefinition{{Name: "barch", Type: "string", DefaultValue: "test"}}, SyncStatus: "ready"}
	if err := store.SaveCICDJob(ctx, job); err != nil {
		t.Fatal(err)
	}
	job, err = service.PrepareGeneratedJobRepositories(ctx, "project-a", job.Key)
	if err != nil {
		t.Fatal(err)
	}
	if job.JenkinsfileRepository != "ops-delivery-jenkinsfiles" || job.ManifestRepository != "ops-delivery-manifests" || job.JenkinsfileCredential != "gitlab-delivery-read" || job.ManifestCredential != "gitlab-delivery-read" {
		t.Fatalf("generated job was not pinned to project repositories: %#v", job)
	}
	if len(job.ParameterDefinitions) != 0 || job.Parameters["barch"] != "" || job.Parameters["IMAGE_TAG"] != "" || job.Parameters["DEPLOY_ENV"] != "test" ||
		job.Parameters["MANIFEST_CREDENTIAL_ID"] != "ops-project-a-gitlab-read" || job.Parameters["DEPLOY_VERIFY_MODE"] != "rollout" ||
		job.Parameters["ROLLOUT_TIMEOUT_MINUTES"] != "5" || job.Parameters["ROLLBACK_ON_FAILURE"] != "false" {
		t.Fatalf("generated job parameters were not migrated to fixed Jenkinsfile configuration: %#v", job)
	}
	parameters := parameterDefinitions(job, map[string]string{"jenkinsfile": "ignored", "manifest": "ignored"})
	for _, expected := range []string{"<hudson.model.ChoiceParameterDefinition>", "<name>TARGET_SERVICES</name>", "<string>gateway</string>", "Jenkins 每次只能选择一个服务", "<name>GIT_BRANCH</name>", "留空使用所选服务登记的默认分支", "<defaultValue></defaultValue>"} {
		if !strings.Contains(parameters, expected) {
			t.Fatalf("generated Jenkins parameters missing %q: %s", expected, parameters)
		}
	}
	for _, forbidden := range []string{"DEPLOY_ENV", "IMAGE_TAG", "MANIFEST_CREDENTIAL_ID", "barch", "JENKINSFILE_CREDENTIAL_ID"} {
		if strings.Contains(parameters, forbidden) {
			t.Fatalf("generated Jenkins parameters expose internal value %q: %s", forbidden, parameters)
		}
	}
	triggerParameters := buildParameters(job, BuildInput{Branch: "feature/demo", Services: []string{"gateway"}, ImageTag: "ignored", Parameters: map[string]string{"DEPLOY_ENV": "prod"}})
	if len(triggerParameters) != 2 || triggerParameters["GIT_BRANCH"] != "feature/demo" || triggerParameters["TARGET_SERVICES"] != "gateway" {
		t.Fatalf("generated build trigger parameters = %#v", triggerParameters)
	}
	if _, err := service.SaveCredential(ctx, "project-a", "", CredentialInput{Key: "operator-gitlab", ConnectionKey: "jenkins", DisplayName: "Operator GitLab", Kind: "existing", ExternalID: "operator-project-a-read"}); err != nil {
		t.Fatal(err)
	}
	job.JenkinsfileCredential, job.ManifestCredential = "operator-gitlab", "operator-gitlab"
	if err := store.SaveCICDJob(ctx, job); err != nil {
		t.Fatal(err)
	}
	job, err = service.PrepareGeneratedJobRepositories(ctx, "project-a", job.Key)
	if err != nil {
		t.Fatal(err)
	}
	if job.JenkinsfileCredential != "operator-gitlab" || job.ManifestCredential != "operator-gitlab" || job.Parameters["MANIFEST_CREDENTIAL_ID"] != "operator-project-a-read" {
		t.Fatalf("generated job did not preserve the operator-selected credential: %#v", job)
	}
	job.JenkinsfileMode = "existing"
	job.CompactParameters = true
	job.Parameters = map[string]string{"DEPLOY_ENV": "prod", "IMAGE_TAG": "internal"}
	job.ParameterDefinitions = []ParameterDefinition{{Name: "DEPLOY_ENV", Type: "choice", Choices: []string{"dev", "prod"}}}
	compactXML := parameterDefinitions(job, map[string]string{"jenkinsfile": "ignored", "manifest": "ignored"})
	if strings.Contains(compactXML, "DEPLOY_ENV") || strings.Contains(compactXML, "IMAGE_TAG") {
		t.Fatalf("compact existing job exposed internal parameters: %s", compactXML)
	}
	compactTrigger := buildParameters(job, BuildInput{Branch: "release", Services: []string{"gateway"}, ImageTag: "ignored", Parameters: map[string]string{"DEPLOY_ENV": "prod"}})
	if len(compactTrigger) != 2 || compactTrigger["GIT_BRANCH"] != "release" || compactTrigger["TARGET_SERVICES"] != "gateway" {
		t.Fatalf("compact existing build trigger parameters = %#v", compactTrigger)
	}
	job.JenkinsfileMode = "generated"
	job.SyncStatus = "ready"
	job.ServiceKeys = []string{"gateway", "aviator"}
	if err := store.SaveCICDJob(ctx, job); err != nil {
		t.Fatal(err)
	}
	if _, err := service.TriggerBuild(ctx, "project-a", job.Key, "tester", BuildInput{Environment: "test", Services: []string{"gateway", "aviator"}}); !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), "每次只能构建一个服务") {
		t.Fatalf("multi-service TriggerBuild() error = %v, want single-service validation", err)
	}
	want := errors.New("source repository denied")
	preflightCalled := false
	service.SetBuildSourcePreflight(func(_ context.Context, project string, checked Job, services []string) error {
		preflightCalled = true
		if project != "project-a" || checked.Key != "gateway" || len(services) != 1 || services[0] != "gateway" {
			t.Fatalf("unexpected preflight input: %s %#v %#v", project, checked, services)
		}
		return want
	})
	if _, err := service.TriggerBuild(ctx, "project-a", job.Key, "tester", BuildInput{Environment: "test", Services: []string{"gateway"}}); !errors.Is(err, want) {
		t.Fatalf("TriggerBuild() error = %v, want preflight error", err)
	}
	if !preflightCalled {
		t.Fatal("source preflight was not called")
	}
}

func TestGeneratedJobConfigurationNormalizesDeploymentPolicy(t *testing.T) {
	config := generatedJobConfiguration(map[string]string{
		"DEPLOY_VERIFY_MODE":      "unexpected",
		"ROLLOUT_TIMEOUT_MINUTES": "999",
		"ROLLBACK_ON_FAILURE":     "yes",
		"UNSUPPORTED":             "must-not-survive",
	}, "test", "manifest-read")
	if config["DEPLOY_ENV"] != "test" || config["DEPLOY_VERIFY_MODE"] != "rollout" ||
		config["ROLLOUT_TIMEOUT_MINUTES"] != "5" || config["ROLLBACK_ON_FAILURE"] != "false" ||
		config["MANIFEST_CREDENTIAL_ID"] != "manifest-read" || config["UNSUPPORTED"] != "" {
		t.Fatalf("invalid deployment policy was not normalized safely: %#v", config)
	}

	config = generatedJobConfiguration(map[string]string{
		"DEPLOY_VERIFY_MODE":      "apply",
		"ROLLOUT_TIMEOUT_MINUTES": "12",
		"ROLLBACK_ON_FAILURE":     "true",
	}, "uat", "")
	if config["DEPLOY_VERIFY_MODE"] != "apply" || config["ROLLOUT_TIMEOUT_MINUTES"] != "12" || config["ROLLBACK_ON_FAILURE"] != "true" {
		t.Fatalf("valid deployment policy was not preserved: %#v", config)
	}
}

func TestValidateGitRepositoryWithoutCredential(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/repo.git/info/refs" || r.URL.Query().Get("service") != "git-upload-pack" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/x-git-upload-pack-advertisement")
		_, _ = io.WriteString(w, "001e# service=git-upload-pack\n0000")
	}))
	defer server.Close()
	key := make([]byte, 32)
	t.Setenv("TEST_CICD_GIT_CHECK_KEY", base64.StdEncoding.EncodeToString(key))
	service, err := New(&appconfig.Config{Security: appconfig.SecurityConfig{CredentialKeyEnv: "TEST_CICD_GIT_CHECK_KEY"}}, newMemoryStore())
	if err != nil {
		t.Fatal(err)
	}
	result, err := service.ValidateGitRepository(context.Background(), server.URL+"/repo.git")
	if err != nil || !result.SmartHTTP || result.HTTPStatus != http.StatusOK {
		t.Fatalf("ValidateGitRepository() = %#v, %v", result, err)
	}
}

func serverURL(r *http.Request) string { return "http://" + r.Host + "/" }

func TestMarkerProgressFallback(t *testing.T) {
	job := Job{ServiceKeys: []string{"gateway", "admin"}}
	logs := strings.Join([]string{
		"@@OPS_STAGE|gateway|checkout|running",
		"@@OPS_STAGE|gateway|checkout|succeeded",
		"@@OPS_STAGE|gateway|build|running",
		"@@OPS_STAGE|gateway|build|succeeded",
		"@@OPS_STAGE|gateway|image|running",
		"@@OPS_DEPLOY_BEGIN",
		"@@OPS_DEPLOY|gateway|test|repo/gateway:v1",
	}, "\n")
	stages, progress, current := markerProgress(logs, job, "running")
	if len(stages) != 4 || progress <= 10 || progress >= 100 || current == "" {
		t.Fatalf("markerProgress() = stages=%#v progress=%d current=%q", stages, progress, current)
	}
	if stages[2].Status != "running" || stages[3].Status != "running" {
		t.Fatalf("unexpected marker states: %#v", stages)
	}
}

func TestMarkerProgressClosesRunningStageWhenBuildIsCanceled(t *testing.T) {
	job := Job{ServiceKeys: []string{"api"}}
	logs := "@@OPS_STAGE|api|checkout|succeeded\n@@OPS_STAGE|api|deploy|running\n"
	stages, _, current := markerProgress(logs, job, "canceled")
	if current != "" {
		t.Fatalf("canceled build still reports a current stage: %q", current)
	}
	if len(stages) != 2 || stages[1].Status != "canceled" {
		t.Fatalf("canceled build stages = %#v", stages)
	}
}

type memoryStore struct {
	mu           sync.Mutex
	connections  map[string]StoredConnection
	credentials  map[string]StoredCredential
	repositories map[string]Repository
	jobs         map[string]Job
	builds       map[string]Build
}

type tunnelStub struct{ url string }

func (t tunnelStub) Ensure(context.Context, ManagedEndpoint) (string, error) { return t.url, nil }

func newMemoryStore() *memoryStore {
	return &memoryStore{connections: map[string]StoredConnection{}, credentials: map[string]StoredCredential{}, repositories: map[string]Repository{}, jobs: map[string]Job{}, builds: map[string]Build{}}
}
func scope(project, key string) string { return project + "/" + key }
func (s *memoryStore) ListCICDConnections(_ context.Context, project string) ([]StoredConnection, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r := []StoredConnection{}
	for _, v := range s.connections {
		if v.ProjectKey == project {
			r = append(r, v)
		}
	}
	return r, nil
}
func (s *memoryStore) GetCICDConnection(_ context.Context, project, key string) (StoredConnection, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.connections[scope(project, key)]
	if !ok {
		return v, os.ErrNotExist
	}
	return v, nil
}
func (s *memoryStore) SaveCICDConnection(_ context.Context, v StoredConnection) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now().UTC()
	if v.CreatedAt.IsZero() {
		v.CreatedAt = now
	}
	v.UpdatedAt = now
	s.connections[scope(v.ProjectKey, v.Key)] = v
	return nil
}
func (s *memoryStore) DeleteCICDConnection(_ context.Context, project, key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.connections, scope(project, key))
	return nil
}
func (s *memoryStore) ListCICDCredentials(_ context.Context, project string) ([]StoredCredential, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r := []StoredCredential{}
	for _, v := range s.credentials {
		if v.ProjectKey == project {
			r = append(r, v)
		}
	}
	return r, nil
}
func (s *memoryStore) GetCICDCredential(_ context.Context, project, key string) (StoredCredential, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.credentials[scope(project, key)]
	if !ok {
		return v, os.ErrNotExist
	}
	return v, nil
}
func (s *memoryStore) SaveCICDCredential(_ context.Context, v StoredCredential) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.credentials[scope(v.ProjectKey, v.Key)] = v
	return nil
}
func (s *memoryStore) DeleteCICDCredential(_ context.Context, project, key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.credentials, scope(project, key))
	return nil
}
func (s *memoryStore) ListCICDRepositories(_ context.Context, project string) ([]Repository, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	result := []Repository{}
	for _, item := range s.repositories {
		if item.ProjectKey == project {
			result = append(result, item)
		}
	}
	return result, nil
}
func (s *memoryStore) GetCICDRepository(_ context.Context, project, key string) (Repository, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	item, ok := s.repositories[scope(project, key)]
	if !ok {
		return item, os.ErrNotExist
	}
	return item, nil
}
func (s *memoryStore) SaveCICDRepository(_ context.Context, item Repository) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now().UTC()
	if item.CreatedAt.IsZero() {
		item.CreatedAt = now
	}
	item.UpdatedAt = now
	s.repositories[scope(item.ProjectKey, item.Key)] = item
	return nil
}
func (s *memoryStore) DeleteCICDRepository(_ context.Context, project, key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.repositories, scope(project, key))
	return nil
}
func (s *memoryStore) ListCICDJobs(_ context.Context, project string) ([]Job, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r := []Job{}
	for _, v := range s.jobs {
		if v.ProjectKey == project {
			r = append(r, v)
		}
	}
	return r, nil
}
func (s *memoryStore) GetCICDJob(_ context.Context, project, key string) (Job, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.jobs[scope(project, key)]
	if !ok {
		return v, os.ErrNotExist
	}
	return v, nil
}
func (s *memoryStore) SaveCICDJob(_ context.Context, v Job) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now().UTC()
	if v.CreatedAt.IsZero() {
		v.CreatedAt = now
	}
	v.UpdatedAt = now
	s.jobs[scope(v.ProjectKey, v.Key)] = v
	return nil
}
func (s *memoryStore) DeleteCICDJob(_ context.Context, project, key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.jobs, scope(project, key))
	return nil
}
func (s *memoryStore) GetCICDJobBuildUsage(_ context.Context, project, key string) (JobBuildUsage, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var usage JobBuildUsage
	for _, build := range s.builds {
		if build.ProjectKey != project || build.JobKey != key {
			continue
		}
		usage.TotalBuilds++
		if build.Status == "queued" || build.Status == "running" {
			usage.ActiveBuilds++
		}
	}
	usage.HistoricalBuilds = usage.TotalBuilds - usage.ActiveBuilds
	return usage, nil
}
func (s *memoryStore) ListCICDBuilds(_ context.Context, project, environment string, limit int) ([]Build, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r := []Build{}
	for _, v := range s.builds {
		if v.ProjectKey == project && (environment == "" || v.Environment == environment) {
			r = append(r, v)
		}
	}
	return r, nil
}
func (s *memoryStore) GetCICDBuild(_ context.Context, project, id string) (Build, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.builds[id]
	if !ok || v.ProjectKey != project {
		return v, os.ErrNotExist
	}
	return v, nil
}
func (s *memoryStore) SaveCICDBuild(_ context.Context, v Build) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.builds[v.ID] = v
	return nil
}
func (s *memoryStore) HasActiveCICDBuilds(_ context.Context, project string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, v := range s.builds {
		if v.ProjectKey == project && (v.Status == "queued" || v.Status == "running") {
			return true, nil
		}
	}
	return false, nil
}
func (s *memoryStore) DeleteCICDProject(_ context.Context, project string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for key, v := range s.builds {
		if v.ProjectKey == project {
			delete(s.builds, key)
		}
	}
	for key, v := range s.jobs {
		if v.ProjectKey == project {
			delete(s.jobs, key)
		}
	}
	for key, v := range s.credentials {
		if v.ProjectKey == project {
			delete(s.credentials, key)
		}
	}
	for key, v := range s.repositories {
		if v.ProjectKey == project {
			delete(s.repositories, key)
		}
	}
	for key, v := range s.connections {
		if v.ProjectKey == project {
			delete(s.connections, key)
		}
	}
	return nil
}

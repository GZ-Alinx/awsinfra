package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/GZ-Alinx/awsinfra/internal/appconfig"
	"github.com/GZ-Alinx/awsinfra/internal/awscredentials"
	"github.com/GZ-Alinx/awsinfra/internal/cicd"
	"github.com/GZ-Alinx/awsinfra/internal/gitlab"
	"github.com/GZ-Alinx/awsinfra/internal/kubetunnel"
	"github.com/GZ-Alinx/awsinfra/internal/persistence"
)

type smokeResult struct {
	Project            string            `json:"project"`
	Environment        string            `json:"environment"`
	Service            string            `json:"service"`
	SourceRepository   string            `json:"source_repository"`
	JenkinsfilePath    string            `json:"jenkinsfile_path"`
	ManifestPath       string            `json:"manifest_path"`
	ImageRepository    string            `json:"image_repository"`
	BuildID            string            `json:"build_id"`
	BuildNumber        int64             `json:"build_number"`
	Status             string            `json:"status"`
	Progress           int               `json:"progress"`
	Stages             []cicd.BuildStage `json:"stages"`
	HealthResponse     string            `json:"health_response,omitempty"`
	JenkinsCredential  string            `json:"jenkins_credential_id"`
	PodIdentityRoleARN string            `json:"pod_identity_role_arn"`
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() error {
	configPath := flag.String("config", "/app/config.yaml", "platform configuration path")
	project := flag.String("project", "demo", "project key")
	environment := flag.String("environment", "test", "environment key")
	connectionKey := flag.String("connection", "test-jenkins", "managed Jenkins connection key")
	serviceKey := flag.String("service", "go-smoke", "smoke service key")
	namespace := flag.String("namespace", "example-app", "deployment namespace")
	flag.Parse()

	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Minute)
	defer cancel()
	config, err := appconfig.Load(*configPath)
	if err != nil {
		return err
	}
	store, err := persistence.Open(ctx, config)
	if err != nil {
		return err
	}
	defer store.Close()
	awsService, err := awscredentials.New(config, store)
	if err != nil {
		return err
	}
	gitlabService, err := gitlab.New(config, store)
	if err != nil {
		return err
	}
	cicdService, err := cicd.New(config, store)
	if err != nil {
		return err
	}
	tunnels := kubetunnel.New(config, awsService)
	defer tunnels.Close()
	cicdService.SetTunnelProvider(tunnels)
	connection, err := store.GetCICDConnection(ctx, *project, *connectionKey)
	if err != nil {
		return err
	}
	if connection.ConnectionMode != "eks_port_forward" || connection.EnvironmentKey != *environment || connection.ClusterName == "" || connection.Region == "" {
		return fmt.Errorf("selected Jenkins connection is not bound to %s/%s managed EKS", *project, *environment)
	}
	awsEnv, err := awsService.Environment(ctx, *project)
	if err != nil {
		return err
	}
	runtimeEnv := append(withoutAWS(os.Environ()), awsEnv...)
	runtimeEnv = append(runtimeEnv, "AWS_REGION="+connection.Region, "AWS_DEFAULT_REGION="+connection.Region)

	fmt.Println("[1/7] 准备 ECR 与最小权限 Pod Identity")
	accountID, err := runAWS(ctx, config.Tools.AWS, runtimeEnv, "sts", "get-caller-identity", "--query", "Account", "--output", "text", "--no-cli-pager")
	if err != nil {
		return err
	}
	repositoryName := *project + "/" + *serviceKey
	imageRepository := strings.TrimSpace(accountID) + ".dkr.ecr." + connection.Region + ".amazonaws.com/" + repositoryName
	if err := ensureECR(ctx, config.Tools.AWS, runtimeEnv, repositoryName); err != nil {
		return err
	}
	serviceAccount := "jenkins-" + *project
	roleARN, err := ensurePodIdentity(ctx, config.Tools.AWS, runtimeEnv, strings.TrimSpace(accountID), connection.Region, connection.ClusterName, *project, *environment, serviceAccount, repositoryName)
	if err != nil {
		return err
	}

	fmt.Println("[2/7] 配置 Jenkins Agent ServiceAccount 与命名空间发布权限")
	kubeconfig, cleanup, err := prepareKubeconfig(ctx, config, runtimeEnv, connection.Region, connection.ClusterName)
	if err != nil {
		return err
	}
	defer cleanup()
	if err := applyRuntimeRBAC(ctx, config.Tools.Kubectl, runtimeEnv, kubeconfig, serviceAccount, *namespace); err != nil {
		return err
	}

	fmt.Println("[3/7] 创建 Go 源码仓库并提交最小服务")
	source, err := gitlabService.EnsureSourceRepository(ctx, *project, *serviceKey, smokeSourceFiles(*serviceKey))
	if err != nil {
		return err
	}

	fmt.Println("[4/7] 同步 GitLab 凭据到项目 Jenkins")
	credential, err := gitlabService.SyncDeliveryCredential(ctx, *project, *connectionKey, cicdService)
	if err != nil {
		return err
	}

	fmt.Println("[5/7] 生成并回读校验部署清单与 Jenkinsfile")
	delivery, err := gitlabService.GetDelivery(ctx, *project)
	if err != nil {
		return err
	}
	// Keep the two GitLab repository registrations canonical. Relay endpoints
	// use distinct repository keys so provisioning can continue to verify GitLab
	// ownership and never mistakes a relay URL for a different repository.
	if _, err := cicdService.SaveRepository(ctx, *project, "ops-delivery-jenkinsfiles", cicd.Repository{
		Key: "ops-delivery-jenkinsfiles", DisplayName: "项目部署流水线", Provider: "gitlab", Purpose: "jenkinsfile",
		CloneURL: delivery.JenkinsfilesCloneURL, DefaultBranch: delivery.DefaultBranch, DefaultPath: "jobs", Description: "由 AWS 部署平台创建，仅属于当前项目",
	}); err != nil {
		return err
	}
	if _, err := cicdService.SaveRepository(ctx, *project, "ops-delivery-manifests", cicd.Repository{
		Key: "ops-delivery-manifests", DisplayName: "项目部署清单", Provider: "gitlab", Purpose: "manifest",
		CloneURL: delivery.ManifestsCloneURL, DefaultBranch: delivery.DefaultBranch, DefaultPath: "environments", Description: "由 AWS 部署平台创建，仅属于当前项目",
	}); err != nil {
		return err
	}
	relayBase := strings.TrimSuffix(strings.TrimSpace(config.Security.ExternalOrigin), "/")
	if relayBase == "" {
		return fmt.Errorf("security.external_origin is required for the regional Git relay")
	}
	projectPath := url.PathEscape(*project)
	servicePath := url.PathEscape(*serviceKey)
	jenkinsfilesRelay := relayBase + "/git-relay/" + projectPath + "/jenkinsfiles.git"
	manifestsRelay := relayBase + "/git-relay/" + projectPath + "/manifests.git"
	sourceRelay := relayBase + "/git-relay/" + projectPath + "/source/" + servicePath + ".git"
	service := gitlab.ServiceSpec{
		Key:                  *serviceKey,
		DisplayName:          "Go 构建验证服务",
		WorkloadType:         "backend",
		Language:             "go",
		RuntimeVersion:       "1.24",
		SourceRepository:     sourceRelay,
		SourceBranch:         "main",
		SourceCredentialID:   credential.ExternalID,
		ManifestCredentialID: credential.ExternalID,
		BuildCommand:         "go test ./...",
		BuildContext:         ".",
		Dockerfile:           "Dockerfile",
		ImageRepository:      imageRepository,
		ImagePullPolicy:      "Always",
		Namespace:            *namespace,
		ContainerPort:        8080,
		Replicas:             1,
		RevisionHistoryLimit: 3,
		HealthPath:           "/healthz",
	}
	delivery.Services = upsertService(delivery.Services, service)
	if _, err := gitlabService.SaveDelivery(ctx, *project, delivery); err != nil {
		return err
	}
	provisioned, err := gitlabService.Provision(ctx, *project)
	if err != nil {
		return err
	}
	if _, err := cicdService.SaveRepository(ctx, *project, "ops-delivery-jenkinsfiles-relay", cicd.Repository{
		Key: "ops-delivery-jenkinsfiles-relay", DisplayName: "项目部署流水线（区域中继）", Provider: "gitlab", Purpose: "jenkinsfile",
		CloneURL: jenkinsfilesRelay, DefaultBranch: delivery.DefaultBranch, DefaultPath: "jobs", Description: "通过平台受限 Git 中继供多区域 Jenkins 读取",
	}); err != nil {
		return err
	}
	if _, err := cicdService.SaveRepository(ctx, *project, "ops-delivery-manifests-relay", cicd.Repository{
		Key: "ops-delivery-manifests-relay", DisplayName: "项目部署清单（区域中继）", Provider: "gitlab", Purpose: "manifest",
		CloneURL: manifestsRelay, DefaultBranch: delivery.DefaultBranch, DefaultPath: "environments", Description: "通过平台受限 Git 中继供多区域 Jenkins 读取",
	}); err != nil {
		return err
	}
	job, err := cicdService.SaveJob(ctx, *project, *serviceKey, cicd.Job{
		Key:                   *serviceKey,
		DisplayName:           "Go 服务构建与部署验证",
		ServiceName:           *serviceKey,
		ServiceKeys:           []string{*serviceKey},
		Language:              "go",
		JenkinsfileMode:       "generated",
		ExecutionMode:         "serial",
		FailurePolicy:         "stop",
		ConnectionKey:         *connectionKey,
		JenkinsJobName:        *project + "-" + *serviceKey,
		Enabled:               true,
		JenkinsfileRepository: "ops-delivery-jenkinsfiles-relay",
		JenkinsfileCredential: credential.Key,
		ManifestRepository:    "ops-delivery-manifests-relay",
		ManifestCredential:    credential.Key,
		EnvironmentPaths:      map[string]string{*environment: "environments/" + *environment},
		Parameters: map[string]string{
			"JENKINS_AGENT_MODE":                 "kubernetes",
			"JENKINS_KUBERNETES_SERVICE_ACCOUNT": serviceAccount,
			"PIPELINE_TIMEOUT_MINUTES":           "20",
		},
		ParameterDefinitions: []cicd.ParameterDefinition{
			{Name: "RELEASE_KIND", Type: "choice", DefaultValue: "full", Choices: []string{"full", "config-only"}, Description: "发布类型", Required: true},
			{Name: "DRY_RUN", Type: "boolean", DefaultValue: "false", Description: "仅校验参数，不改变默认发布流程"},
		},
	})
	if err != nil {
		return err
	}
	if _, _, err := gitlabService.SyncJobJenkinsfile(ctx, *project, job); err != nil {
		return err
	}
	job, err = cicdService.SyncJob(ctx, *project, job.Key)
	if err != nil {
		return err
	}

	fmt.Println("[6/7] 触发 Jenkins 构建并持续采集阶段进度")
	imageTag := "smoke-" + time.Now().UTC().Format("20060102-150405")
	build, err := cicdService.TriggerBuild(ctx, *project, job.Key, "platform-smoke", cicd.BuildInput{Environment: *environment, Branch: "main", ImageTag: imageTag, Services: []string{*serviceKey}})
	if err != nil {
		return err
	}
	build, err = waitForBuild(ctx, cicdService, *project, build)
	if err != nil {
		return err
	}
	if build.Status != "succeeded" {
		logs, _ := cicdService.BuildLogs(ctx, *project, build.ID, 0)
		return fmt.Errorf("Jenkins build %s ended as %s: %s", build.ID, build.Status, tail(logs.Text, 80))
	}

	fmt.Println("[7/7] 验证 Deployment、Service 与集群内健康接口")
	health, err := verifyWorkload(ctx, config.Tools.Kubectl, runtimeEnv, kubeconfig, *namespace, *serviceKey)
	if err != nil {
		return err
	}
	result := smokeResult{
		Project: *project, Environment: *environment, Service: *serviceKey,
		SourceRepository: source.CloneURL,
		JenkinsfilePath:  "jobs/" + job.Key + "/Jenkinsfile",
		ManifestPath:     "environments/" + *environment + "/" + *serviceKey + "/manifest.yaml",
		ImageRepository:  imageRepository + ":" + imageTag,
		BuildID:          build.ID, BuildNumber: build.BuildNumber, Status: build.Status, Progress: build.Progress, Stages: build.Stages,
		HealthResponse: strings.TrimSpace(health), JenkinsCredential: credential.ExternalID, PodIdentityRoleARN: roleARN,
	}
	_ = provisioned
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	return encoder.Encode(result)
}

func ensureECR(ctx context.Context, aws string, env []string, repository string) error {
	if _, err := runAWS(ctx, aws, env, "ecr", "describe-repositories", "--repository-names", repository, "--no-cli-pager"); err != nil {
		if !strings.Contains(err.Error(), "RepositoryNotFoundException") {
			return err
		}
		if _, err := runAWS(ctx, aws, env, "ecr", "create-repository", "--repository-name", repository, "--image-tag-mutability", "IMMUTABLE", "--image-scanning-configuration", "scanOnPush=true", "--encryption-configuration", "encryptionType=AES256", "--no-cli-pager"); err != nil {
			return err
		}
	}
	policy := `{"rules":[{"rulePriority":1,"description":"Keep 20 smoke images","selection":{"tagStatus":"any","countType":"imageCountMoreThan","countNumber":20},"action":{"type":"expire"}}]}`
	_, err := runAWS(ctx, aws, env, "ecr", "put-lifecycle-policy", "--repository-name", repository, "--lifecycle-policy-text", policy, "--no-cli-pager")
	return err
}

func ensurePodIdentity(ctx context.Context, aws string, env []string, account, region, cluster, project, environment, serviceAccount, repository string) (string, error) {
	roleName := project + "-" + environment + "-jenkins-ecr"
	trust := `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":{"Service":"pods.eks.amazonaws.com"},"Action":["sts:AssumeRole","sts:TagSession"]}]}`
	roleARN, err := runAWS(ctx, aws, env, "iam", "get-role", "--role-name", roleName, "--query", "Role.Arn", "--output", "text", "--no-cli-pager")
	if err != nil {
		if !strings.Contains(err.Error(), "NoSuchEntity") {
			return "", err
		}
		roleARN, err = runAWS(ctx, aws, env, "iam", "create-role", "--role-name", roleName, "--assume-role-policy-document", trust, "--description", "Jenkins Kubernetes agents for "+project+"/"+environment, "--query", "Role.Arn", "--output", "text", "--no-cli-pager")
		if err != nil {
			return "", err
		}
	}
	roleARN = strings.TrimSpace(roleARN)
	resourceARN := "arn:aws:ecr:" + region + ":" + account + ":repository/" + strings.SplitN(repository, "/", 2)[0] + "/*"
	policy := fmt.Sprintf(`{"Version":"2012-10-17","Statement":[{"Sid":"ECRLogin","Effect":"Allow","Action":"ecr:GetAuthorizationToken","Resource":"*"},{"Sid":"ProjectECRPush","Effect":"Allow","Action":["ecr:BatchCheckLayerAvailability","ecr:GetDownloadUrlForLayer","ecr:BatchGetImage","ecr:InitiateLayerUpload","ecr:UploadLayerPart","ecr:CompleteLayerUpload","ecr:PutImage"],"Resource":%q}]}`, resourceARN)
	if _, err := runAWS(ctx, aws, env, "iam", "put-role-policy", "--role-name", roleName, "--policy-name", "ProjectECRPush", "--policy-document", policy, "--no-cli-pager"); err != nil {
		return "", err
	}
	associationID, listErr := runAWS(ctx, aws, env, "eks", "list-pod-identity-associations", "--cluster-name", cluster, "--namespace", "platform-server", "--service-account", serviceAccount, "--query", "associations[0].associationId", "--output", "text", "--no-cli-pager")
	if listErr != nil {
		return "", listErr
	}
	associationID = strings.TrimSpace(associationID)
	if associationID == "" || associationID == "None" {
		var createErr error
		for attempt := 0; attempt < 5; attempt++ {
			_, createErr = runAWS(ctx, aws, env, "eks", "create-pod-identity-association", "--cluster-name", cluster, "--namespace", "platform-server", "--service-account", serviceAccount, "--role-arn", roleARN, "--no-cli-pager")
			if createErr == nil {
				break
			}
			time.Sleep(2 * time.Second)
		}
		if createErr != nil {
			return "", createErr
		}
	} else {
		currentRole, err := runAWS(ctx, aws, env, "eks", "describe-pod-identity-association", "--cluster-name", cluster, "--association-id", associationID, "--query", "association.roleArn", "--output", "text", "--no-cli-pager")
		if err != nil {
			return "", err
		}
		if strings.TrimSpace(currentRole) != roleARN {
			if _, err := runAWS(ctx, aws, env, "eks", "update-pod-identity-association", "--cluster-name", cluster, "--association-id", associationID, "--role-arn", roleARN, "--no-cli-pager"); err != nil {
				return "", err
			}
		}
	}
	return roleARN, nil
}

func prepareKubeconfig(ctx context.Context, config *appconfig.Config, env []string, region, cluster string) (string, func(), error) {
	dir, err := os.MkdirTemp("", "ops-go-smoke-kubeconfig-")
	if err != nil {
		return "", func() {}, err
	}
	path := filepath.Join(dir, "config")
	command := exec.CommandContext(ctx, config.Tools.AWS, "eks", "update-kubeconfig", "--name", cluster, "--region", region, "--kubeconfig", path, "--alias", "ops-go-smoke", "--no-cli-pager") // #nosec G204 -- validated platform records only.
	command.Env = env
	if output, err := command.CombinedOutput(); err != nil {
		os.RemoveAll(dir)
		return "", func() {}, fmt.Errorf("prepare kubeconfig: %w: %s", err, strings.TrimSpace(string(output)))
	}
	return path, func() { _ = os.RemoveAll(dir) }, nil
}

func applyRuntimeRBAC(ctx context.Context, kubectl string, env []string, kubeconfig, serviceAccount, namespace string) error {
	manifest := fmt.Sprintf(`apiVersion: v1
kind: Namespace
metadata:
  name: %s
---
apiVersion: v1
kind: ServiceAccount
metadata:
  name: %s
  namespace: platform-server
---
apiVersion: rbac.authorization.k8s.io/v1
kind: Role
metadata:
  name: ops-deploy-jenkins
  namespace: %s
rules:
  - apiGroups: [""]
    resources: ["services", "configmaps", "secrets", "serviceaccounts", "persistentvolumeclaims", "pods", "pods/log"]
    verbs: ["get", "list", "watch", "create", "update", "patch", "delete"]
  - apiGroups: ["apps"]
    resources: ["deployments", "deployments/status", "statefulsets", "statefulsets/status", "daemonsets", "daemonsets/status", "replicasets"]
    verbs: ["get", "list", "watch", "create", "update", "patch", "delete"]
  - apiGroups: ["batch"]
    resources: ["jobs", "cronjobs"]
    verbs: ["get", "list", "watch", "create", "update", "patch", "delete"]
  - apiGroups: ["autoscaling"]
    resources: ["horizontalpodautoscalers"]
    verbs: ["get", "list", "watch", "create", "update", "patch", "delete"]
  - apiGroups: ["networking.k8s.io"]
    resources: ["ingresses", "networkpolicies"]
    verbs: ["get", "list", "watch", "create", "update", "patch", "delete"]
  - apiGroups: ["policy"]
    resources: ["poddisruptionbudgets"]
    verbs: ["get", "list", "watch", "create", "update", "patch", "delete"]
---
apiVersion: rbac.authorization.k8s.io/v1
kind: RoleBinding
metadata:
  name: ops-deploy-jenkins
  namespace: %s
subjects:
  - kind: ServiceAccount
    name: %s
    namespace: platform-server
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: Role
  name: ops-deploy-jenkins
`, namespace, serviceAccount, namespace, namespace, serviceAccount)
	_, err := runKubectl(ctx, kubectl, env, kubeconfig, []byte(manifest), "apply", "-f", "-")
	return err
}

func verifyWorkload(ctx context.Context, kubectl string, env []string, kubeconfig, namespace, service string) (string, error) {
	if _, err := runKubectl(ctx, kubectl, env, kubeconfig, nil, "rollout", "status", "deployment/"+service, "-n", namespace, "--timeout=5m"); err != nil {
		return "", err
	}
	// Create, wait and read the probe pod explicitly. `kubectl run --rm --attach`
	// occasionally races container creation and mixes a harmless attach warning into
	// an otherwise successful health response.
	checkName := fmt.Sprintf("%s-health-%d", service, time.Now().Unix())
	defer func() {
		_, _ = runKubectl(context.Background(), kubectl, env, kubeconfig, nil, "delete", "pod", checkName, "-n", namespace, "--ignore-not-found", "--wait=false")
	}()
	if _, err := runKubectl(ctx, kubectl, env, kubeconfig, nil, "run", checkName, "-n", namespace, "--restart=Never", "--image=curlimages/curl:8.10.1", "--", "curl", "-fsS", "http://"+service+":8080/healthz"); err != nil {
		return "", err
	}
	if _, err := runKubectl(ctx, kubectl, env, kubeconfig, nil, "wait", "pod/"+checkName, "-n", namespace, "--for=jsonpath={.status.phase}=Succeeded", "--timeout=90s"); err != nil {
		return "", err
	}
	return runKubectl(ctx, kubectl, env, kubeconfig, nil, "logs", checkName, "-n", namespace)
}

func runAWS(ctx context.Context, tool string, env []string, args ...string) (string, error) {
	command := exec.CommandContext(ctx, tool, args...) // #nosec G204 -- explicit administrator workflow with fixed AWS operations.
	command.Env = env
	output, err := command.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("aws %s: %w: %s", strings.Join(args[:min(2, len(args))], " "), err, strings.TrimSpace(string(output)))
	}
	return string(output), nil
}

func runKubectl(ctx context.Context, tool string, env []string, kubeconfig string, stdin []byte, args ...string) (string, error) {
	fullArgs := append([]string{"--kubeconfig", kubeconfig, "--context", "ops-go-smoke"}, args...)
	command := exec.CommandContext(ctx, tool, fullArgs...) // #nosec G204 -- explicit administrator workflow with fixed Kubernetes operations.
	command.Env = env
	if stdin != nil {
		command.Stdin = bytes.NewReader(stdin)
	}
	output, err := command.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("kubectl %s: %w: %s", strings.Join(args[:min(2, len(args))], " "), err, strings.TrimSpace(string(output)))
	}
	return string(output), nil
}

func waitForBuild(ctx context.Context, service *cicd.Service, project string, target cicd.Build) (cicd.Build, error) {
	ticker := time.NewTicker(3 * time.Second)
	defer ticker.Stop()
	lastStage := ""
	for {
		builds, err := service.ListBuilds(ctx, project, target.Environment, 100)
		if err != nil {
			return target, err
		}
		for _, build := range builds {
			if build.ID != target.ID {
				continue
			}
			target = build
			if build.CurrentStage != lastStage {
				fmt.Printf("      Jenkins: %s · %d%% · %s\n", build.Status, build.Progress, build.CurrentStage)
				lastStage = build.CurrentStage
			}
			if build.Status != "queued" && build.Status != "running" {
				return build, nil
			}
			break
		}
		select {
		case <-ctx.Done():
			return target, ctx.Err()
		case <-ticker.C:
		}
	}
}

func upsertService(items []gitlab.ServiceSpec, service gitlab.ServiceSpec) []gitlab.ServiceSpec {
	result := make([]gitlab.ServiceSpec, 0, len(items)+1)
	replaced := false
	for _, item := range items {
		if item.Key == service.Key {
			result, replaced = append(result, service), true
		} else {
			result = append(result, item)
		}
	}
	if !replaced {
		result = append(result, service)
	}
	return result
}

func smokeSourceFiles(service string) []gitlab.SourceFile {
	mainSource := fmt.Sprintf(`package main

import (
  "encoding/json"
  "log"
  "net/http"
  "os"
  "time"
)

var version = "development"

func main() {
  mux := http.NewServeMux()
  mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) { w.Header().Set("Content-Type", "application/json"); _ = json.NewEncoder(w).Encode(map[string]any{"status":"ok","service":%q,"version":version,"time":time.Now().UTC()}) })
  mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) { w.Header().Set("Content-Type", "application/json"); _ = json.NewEncoder(w).Encode(map[string]string{"message":"Go CI/CD smoke service is running","service":%q,"version":version}) })
  address := ":8080"
  if port := os.Getenv("PORT"); port != "" { address = ":" + port }
  log.Printf("starting %s on %%s", address)
  log.Fatal(http.ListenAndServe(address, mux))
}
`, service, service, service)
	return []gitlab.SourceFile{
		{Path: "go.mod", Content: "module " + service + "\n\ngo 1.24\n"},
		{Path: "main.go", Content: mainSource},
		{Path: "main_test.go", Content: `package main

import (
  "net/http"
  "net/http/httptest"
  "testing"
)

func TestHealthEndpointContract(t *testing.T) {
  handler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
  response := httptest.NewRecorder()
  handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/healthz", nil))
  if response.Code != http.StatusOK { t.Fatalf("status = %d", response.Code) }
}
`},
		{Path: "Dockerfile", Content: `FROM golang:1.24-alpine AS build
WORKDIR /src
COPY go.mod ./
COPY . .
ARG BUILD_VERSION=development
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags "-s -w -X main.version=${BUILD_VERSION}" -o /out/app .

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/app /app
EXPOSE 8080
USER nonroot:nonroot
ENTRYPOINT ["/app"]
`},
		{Path: ".dockerignore", Content: ".git\n.ops-*\nREADME.md\n"},
		{Path: "README.md", Content: "# " + service + "\n\n由 AWS 部署平台创建，用于验证 Go、Jenkinsfile、ECR 与 Kubernetes 部署闭环。\n"},
	}
}

func withoutAWS(source []string) []string {
	blocked := map[string]bool{"AWS_ACCESS_KEY_ID": true, "AWS_SECRET_ACCESS_KEY": true, "AWS_SESSION_TOKEN": true, "AWS_PROFILE": true, "AWS_DEFAULT_PROFILE": true, "AWS_REGION": true, "AWS_DEFAULT_REGION": true}
	result := make([]string, 0, len(source))
	for _, item := range source {
		key, _, _ := strings.Cut(item, "=")
		if !blocked[key] {
			result = append(result, item)
		}
	}
	return result
}

func tail(value string, lines int) string {
	items := strings.Split(strings.TrimSpace(value), "\n")
	if len(items) > lines {
		items = items[len(items)-lines:]
	}
	return strings.Join(items, "\n")
}

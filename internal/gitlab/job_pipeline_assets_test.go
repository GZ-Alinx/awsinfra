package gitlab

import (
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"

	"github.com/GZ-Alinx/awsinfra/internal/cicd"
)

func TestGeneratedPipelineFilesAreDeterministicAndSelfContained(t *testing.T) {
	expectedVersionPrefix := "lib/v" + pipelineSchemaVersion + "/"
	for _, path := range []string{pipelineLibraryPath, kanikoBuildScriptPath, dockerBuildScriptPath, deployScriptPath} {
		if !strings.HasPrefix(path, expectedVersionPrefix) {
			t.Fatalf("pipeline module %q is not isolated by schema version %s", path, pipelineSchemaVersion)
		}
	}

	job := cicd.Job{
		Key:        "release",
		ProjectKey: "demo",
		Parameters: map[string]string{
			"JENKINS_AGENT_MODE":      "kubernetes",
			"DEPLOY_VERIFY_MODE":      "apply",
			"ROLLBACK_ON_FAILURE":     "true",
			"ROLLOUT_TIMEOUT_MINUTES": "12",
		},
	}
	first := []ServiceSpec{
		{Key: "worker", SourceRepository: "https://git.example/demo/worker.git", SourceBranch: "release", BuildContext: "server", DockerfileSource: "source", Dockerfile: "server/Dockerfile", ImageRepository: "registry.example/demo/worker", Namespace: "demo"},
		{Key: "api", SourceRepository: "https://git.example/demo/api.git", SourceBranch: "main", BuildContext: ".", DockerfileSource: "platform", ImageRepository: "registry.example/demo/api", Namespace: "demo"},
	}
	second := []ServiceSpec{first[1], first[0]}
	filesA := generateJobFiles(job, first, "https://git.example/demo/manifests.git", "main")
	filesB := generateJobFiles(job, second, "https://git.example/demo/manifests.git", "main")
	if !reflect.DeepEqual(filesA, filesB) {
		t.Fatal("generated pipeline files depend on service input order")
	}

	paths := make(map[string]bool, len(filesA))
	for _, file := range filesA {
		if paths[file.Path] {
			t.Fatalf("generated duplicate file path %q", file.Path)
		}
		paths[file.Path] = true
		if strings.TrimSpace(file.Content) == "" {
			t.Fatalf("generated empty file %q", file.Path)
		}
	}
	for _, path := range []string{
		"jobs/release/Jenkinsfile",
		"jobs/release/services.groovy",
		pipelineLibraryPath,
		kanikoBuildScriptPath,
		dockerBuildScriptPath,
		deployScriptPath,
	} {
		if !paths[path] {
			t.Fatalf("generated pipeline is missing module %q", path)
		}
	}
	jenkinsfile := generatedFileContent(t, filesA, "jenkinsfiles", "jobs/release/Jenkinsfile")
	library := generatedFileContent(t, filesA, "jenkinsfiles", pipelineLibraryPath)
	if regexp.MustCompile(`@@[A-Z][A-Z0-9_]*@@`).MatchString(jenkinsfile + library) {
		t.Fatal("generated pipeline contains an unresolved template marker")
	}
	for _, expected := range []string{
		"OPS_PIPELINE_SCHEMA = '" + pipelineSchemaVersion + "'",
		"DEPLOY_VERIFY_MODE = 'apply'",
		"ROLLOUT_TIMEOUT_MINUTES = '12'",
		"ROLLBACK_ON_FAILURE = 'true'",
		"createVersion(env.BUILD_NUMBER, env.DEPLOY_ENV)",
	} {
		if !strings.Contains(jenkinsfile, expected) {
			t.Fatalf("Jenkinsfile missing normalized deployment setting %q", expected)
		}
	}
	for _, expected := range []string{
		"environmentPrefix + '-' + new Date().format('yyyyMMddHHmmss')",
		"prod-20260806153000_42",
	} {
		if !strings.Contains(library, expected) {
			t.Fatalf("pipeline library missing environment-prefixed image tag behavior %q", expected)
		}
	}
}

func TestGeneratedPipelineHonorsEnvironmentScopedRepositoryPaths(t *testing.T) {
	job := cicd.Job{
		Key: "release", ProjectKey: "demo", EnvironmentKey: "prod",
		JenkinsfilePath: "environments/prod/pipelines/release/Jenkinsfile",
		ManifestPath:    "environments/prod", EnvironmentPaths: map[string]string{"prod": "environments/prod"},
		Parameters: map[string]string{"DEPLOY_ENV": "prod"},
	}
	files := generateJobFiles(job, []ServiceSpec{{
		Key: "api", SourceRepository: "https://git.example/demo/api.git", SourceBranch: "main", BuildContext: ".", DockerfileSource: "source", Dockerfile: "Dockerfile", ImageRepository: "registry.example/demo/api", Namespace: "demo-prod",
	}}, "https://git.example/demo/ops-delivery.git", "main")
	jenkinsfile := generatedFileContent(t, files, "jenkinsfiles", "environments/prod/pipelines/release/Jenkinsfile")
	_ = generatedFileContent(t, files, "jenkinsfiles", "environments/prod/pipelines/release/services.groovy")
	library := generatedFileContent(t, files, "jenkinsfiles", pipelineLibraryPath)
	if !strings.Contains(jenkinsfile, "MANIFEST_ROOT = 'environments/prod'") {
		t.Fatalf("Jenkinsfile did not pin the production manifest directory:\n%s", jenkinsfile)
	}
	if !strings.Contains(library, `"${script.env.MANIFEST_ROOT}/${serviceKey}/manifest.yaml"`) {
		t.Fatalf("pipeline library does not read the environment-scoped manifest root:\n%s", library)
	}
}

func TestGeneratedPipelineUsesEnvironmentScopedDockerfileAndCustomJenkinsfile(t *testing.T) {
	custom := "pipeline {\n  agent any\n  stages { stage('prod') { steps { echo 'prod' } } }\n}"
	job := cicd.Job{
		Key: "release", ProjectKey: "demo", EnvironmentKey: "prod",
		JenkinsfilePath:    "environments/prod/pipelines/release/Jenkinsfile",
		JenkinsfileContent: custom,
		Parameters:         map[string]string{"DEPLOY_ENV": "prod"},
	}
	files := generateJobFiles(job, []ServiceSpec{{
		Key: "api", SourceRepository: "https://git.example/demo/api.git", SourceBranch: "main", BuildContext: ".", DockerfileSource: "platform",
		DockerfileContent: "FROM scratch\n", DockerfileContents: map[string]string{"prod": "FROM debian:bookworm-slim\n"}, ImageRepository: "registry.example/demo/api", Namespace: "demo-prod",
	}}, "https://git.example/demo/ops-delivery.git", "main")
	if got := generatedFileContent(t, files, "jenkinsfiles", job.JenkinsfilePath); got != custom+"\n" {
		t.Fatalf("custom Jenkinsfile was not preserved:\n%s", got)
	}
	dockerfile := generatedFileContent(t, files, "jenkinsfiles", "environments/prod/dockerfiles/api/Dockerfile")
	if dockerfile != "FROM debian:bookworm-slim\n" {
		t.Fatalf("production Dockerfile did not use the environment override: %q", dockerfile)
	}
}

func TestGeneratedPipelineShellAssetsPassSyntaxCheck(t *testing.T) {
	tests := []struct {
		name    string
		shell   string
		content string
	}{
		{name: "kaniko", shell: "sh", content: kanikoBuildScript},
		{name: "docker", shell: "bash", content: dockerBuildScript},
		{name: "deploy", shell: "sh", content: deployKubernetesScript},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := exec.LookPath(test.shell); err != nil {
				t.Skipf("%s is not installed", test.shell)
			}
			if output, err := exec.Command(test.shell, "-n", "-c", test.content).CombinedOutput(); err != nil {
				t.Fatalf("%s syntax check failed: %v\n%s", test.name, err, output)
			}
		})
	}
}

func TestGroovyStringKeepsGeneratedConfigurationInsideOneLiteral(t *testing.T) {
	actual := groovyString("line1'\n${env.SECRET}\r\u2028line2")
	for _, forbidden := range []string{"\n", "\r", "\u2028"} {
		if strings.Contains(actual, forbidden) {
			t.Fatalf("groovyString left an unsafe literal fragment %q in %q", forbidden, actual)
		}
	}
	for _, expected := range []string{`\'`, `\n`, `\r`, `\u2028`, `\${env.SECRET}`} {
		if !strings.Contains(actual, expected) {
			t.Fatalf("groovyString did not escape %q in %q", expected, actual)
		}
	}

	comment := groovyCommentText("release */\n pipeline { unsafe() }")
	if strings.Contains(comment, "*/") || strings.ContainsAny(comment, "\r\n") {
		t.Fatalf("generated Jenkinsfile comment can escape its comment block: %q", comment)
	}
}

func TestDeployModuleInitializesImageInspectionVariables(t *testing.T) {
	if strings.Contains(deployKubernetesScript, "${actual_image}") {
		t.Fatal("deploy script references the historical uninitialized actual_image variable")
	}
	assignment := strings.Index(deployKubernetesScript, "actual_images=$(kubectl get deployment")
	usage := strings.Index(deployKubernetesScript, "实际镜像: $actual_images")
	if assignment < 0 || usage < 0 || assignment > usage {
		t.Fatal("deploy script must initialize actual_images before reporting it")
	}
	for _, expected := range []string{
		`kubectl apply --dry-run=server`,
		`OPS_EXPECTED_IMAGE`,
		`ROLLBACK_ON_FAILURE:-false`,
		`自动回滚未开启；保留现场供排查`,
	} {
		if !strings.Contains(deployKubernetesScript, expected) {
			t.Fatalf("deploy script missing safety behavior %q", expected)
		}
	}
}

func TestDeployScriptValidatesBeforeMutationAndVerifiesImage(t *testing.T) {
	tempDir := t.TempDir()
	logPath := filepath.Join(tempDir, "kubectl.log")
	manifestPath := filepath.Join(tempDir, "manifest.yaml")
	writeExecutable(t, filepath.Join(tempDir, "kubectl"), `#!/bin/sh
printf '%s\n' "$*" >> "$FAKE_KUBECTL_LOG"
case "$1" in
  create)
    echo deployment.apps/api
    ;;
  apply)
    if [ "${2:-}" = "--dry-run=server" ]; then
      echo deployment.apps/api
    else
      echo deployment.apps/api configured
    fi
    ;;
  get)
    case "$*" in
      *deployment.kubernetes.io/revision*) printf '4' ;;
      *containers*image*) printf '%s' "$OPS_EXPECTED_IMAGE" ;;
    esac
    ;;
  rollout)
    echo deployment/api successfully rolled out
    ;;
esac
`)
	if err := os.WriteFile(manifestPath, []byte("apiVersion: apps/v1\nkind: Deployment\nmetadata:\n  name: api\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	command := exec.Command("sh", "-c", deployKubernetesScript)
	command.Env = append(os.Environ(),
		"PATH="+tempDir+":"+os.Getenv("PATH"),
		"WORKSPACE="+tempDir,
		"BUILD_NUMBER=19",
		"FAKE_KUBECTL_LOG="+logPath,
		"OPS_RENDERED_MANIFEST="+manifestPath,
		"OPS_TARGET_NAMESPACE=demo",
		"OPS_EXPECTED_IMAGE=registry.example/demo/api:19",
		"DEPLOY_VERIFY_MODE=rollout",
		"ROLLOUT_TIMEOUT_MINUTES=5",
		"ROLLBACK_ON_FAILURE=false",
	)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("deploy script failed: %v\n%s", err, output)
	}
	if !strings.Contains(string(output), "工作负载已就绪: demo/api -> registry.example/demo/api:19") {
		t.Fatalf("deploy output does not report the verified image:\n%s", output)
	}
	calls, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	dryRun := strings.Index(string(calls), "apply --dry-run=server")
	apply := strings.Index(string(calls), "apply -f")
	if dryRun < 0 || apply < 0 || dryRun > apply {
		t.Fatalf("server validation must run before apply:\n%s", calls)
	}
}

func TestDeployScriptExplainsDuplicateServicePortWithoutMutation(t *testing.T) {
	tempDir := t.TempDir()
	logPath := filepath.Join(tempDir, "kubectl.log")
	manifestPath := filepath.Join(tempDir, "manifest.yaml")
	writeExecutable(t, filepath.Join(tempDir, "kubectl"), `#!/bin/sh
printf '%s\n' "$*" >> "$FAKE_KUBECTL_LOG"
if [ "$1" = create ]; then
  echo deployment.apps/api
  exit 0
fi
if [ "$1" = apply ] && [ "${2:-}" = "--dry-run=server" ]; then
  echo 'The Service "api" is invalid: spec.ports[1].name: Duplicate value: "http"' >&2
  exit 1
fi
echo "unexpected kubectl mutation" >&2
exit 99
`)
	if err := os.WriteFile(manifestPath, []byte("apiVersion: v1\nkind: Service\nmetadata:\n  name: api\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	command := exec.Command("sh", "-c", deployKubernetesScript)
	command.Env = append(os.Environ(),
		"PATH="+tempDir+":"+os.Getenv("PATH"),
		"WORKSPACE="+tempDir,
		"BUILD_NUMBER=20",
		"FAKE_KUBECTL_LOG="+logPath,
		"OPS_RENDERED_MANIFEST="+manifestPath,
		"OPS_TARGET_NAMESPACE=demo",
		"OPS_EXPECTED_IMAGE=registry.example/demo/api:20",
	)
	output, err := command.CombinedOutput()
	if err == nil {
		t.Fatalf("invalid Service unexpectedly passed validation:\n%s", output)
	}
	if !strings.Contains(string(output), "检测到 Service 端口名称重复") || !strings.Contains(string(output), "尚未修改集群") {
		t.Fatalf("deploy output does not explain the safe validation failure:\n%s", output)
	}
	calls, readErr := os.ReadFile(logPath)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if strings.Contains(string(calls), "apply -f") {
		t.Fatalf("deploy script mutated the cluster after validation failure:\n%s", calls)
	}
}

func writeExecutable(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o700); err != nil {
		t.Fatal(err)
	}
}

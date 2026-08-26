package cicd

import (
	"strings"
	"testing"
)

func TestAnalyzeJenkinsfileMultiServiceAndSecretRedaction(t *testing.T) {
	const source = `
pipeline {
  agent { kubernetes { yaml """apiVersion: v1\nkind: Pod""" } }
  parameters {
    choice(name: 'server', choices: ['app-admin', 'app-api', 'app-rpc'], description: '选择服务')
    string(name: 'branch', defaultValue: 'main', description: '源码分支')
    booleanParam(name: 'DRY_RUN', defaultValue: false, description: '仅验证')
  }
  environment {
    GIT_CREDENTIALS_ID = 'git-code-credentials'
    REGISTRY_CREDENTIALS_ID = 'registry-credentials'
    NAMESPACE = 'demo-dev'
    DEPLOY_GIT_URL = 'https://git.example/ops/deploy.git'
    DEPLOY_BRANCH = 'main'
	BUILDER_IMAGE = 'golang:1.26-alpine'
    LARK_WEBHOOK = 'https://hooks.example/very-secret-value'
  }
  stages {
    stage('初始化') { steps { echo 'init' } }
    stage('构建') { steps { sh 'go build ./...' } }
    stage('部署') { steps { sh 'kubectl apply -f app.yaml' } }
  }
}
env.DEPLOY_YAML = "dev/backend/${serviceName}/k8s.yaml"
`
	analysis, err := analyzeJenkinsfile(source)
	if err != nil {
		t.Fatal(err)
	}
	if analysis.Language != "go" || analysis.RuntimeVersion != "1.26" {
		t.Fatalf("language detection = %#v", analysis)
	}
	if analysis.ServiceParameter != "server" || strings.Join(analysis.Services, ",") != "app-admin,app-api,app-rpc" {
		t.Fatalf("services = %#v", analysis.Services)
	}
	if len(analysis.Parameters) != 3 || len(analysis.Stages) != 3 {
		t.Fatalf("parameters/stages = %#v / %#v", analysis.Parameters, analysis.Stages)
	}
	if analysis.Settings["NAMESPACE"] != "demo-dev" || analysis.Settings["DEPLOY_BRANCH"] != "main" {
		t.Fatalf("safe settings = %#v", analysis.Settings)
	}
	if len(analysis.Repositories) != 1 || analysis.Repositories[0].Role != "manifest" || analysis.Repositories[0].Path != "dev/backend/${serviceName}/k8s.yaml" {
		t.Fatalf("repositories = %#v", analysis.Repositories)
	}
	if len(analysis.SensitiveVariables) != 1 || analysis.SensitiveVariables[0] != "LARK_WEBHOOK" {
		t.Fatalf("sensitive variables = %#v", analysis.SensitiveVariables)
	}
	serialized := strings.Join(analysis.Warnings, " ") + strings.Join(analysis.SensitiveVariables, " ")
	for _, repository := range analysis.Repositories {
		serialized += repository.URL
	}
	if strings.Contains(serialized, "very-secret-value") {
		t.Fatal("analysis leaked a secret literal")
	}
	if analysis.Suggestion.ServiceName != "backend-services" || analysis.Suggestion.ManifestRepo == "" {
		t.Fatalf("suggestion = %#v", analysis.Suggestion)
	}
	if len(analysis.CredentialReferences) != 3 {
		t.Fatalf("credential references = %#v", analysis.CredentialReferences)
	}
}

func TestAnalyzeJenkinsfileRejectsInvalidInput(t *testing.T) {
	for _, input := range []string{"", "echo hello", string(make([]byte, maxJenkinsfileBytes+1))} {
		if _, err := analyzeJenkinsfile(input); !strings.Contains(err.Error(), ErrInvalid.Error()) {
			t.Fatalf("expected invalid error for input length %d, got %v", len(input), err)
		}
	}
}

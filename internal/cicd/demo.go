package cicd

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"
)

const (
	demoJobKey     = "ops-ui-demo"
	demoJenkinsJob = "ops-platform-ui-demo"
)

// StartDemoBuild creates and triggers a side-effect-free Jenkins Pipeline used
// to verify the platform's build progress and live log experience. The Job is
// disabled in the platform immediately after it is queued, while the completed
// build and its logs remain available for inspection.
func (s *Service) StartDemoBuild(ctx context.Context, project, connectionKey, environment, requestedBy string) (Build, error) {
	project = strings.TrimSpace(project)
	connectionKey = strings.ToLower(strings.TrimSpace(connectionKey))
	requestedBy = strings.TrimSpace(requestedBy)
	if project == "" || !keyPattern.MatchString(connectionKey) {
		return Build{}, fmt.Errorf("%w: project and connection are required", ErrInvalid)
	}
	connection, err := s.store.GetCICDConnection(ctx, project, connectionKey)
	if err != nil {
		return Build{}, err
	}
	environment = strings.ToLower(strings.TrimSpace(environment))
	if environment == "" {
		environment = strings.ToLower(strings.TrimSpace(connection.EnvironmentKey))
	}
	if !keyPattern.MatchString(environment) {
		return Build{}, fmt.Errorf("%w: a valid environment is required", ErrInvalid)
	}
	if requestedBy == "" {
		requestedBy = "platform-demo"
	}

	now := time.Now().UTC()
	job := Job{
		Key:                  demoJobKey,
		ProjectKey:           project,
		DisplayName:          "UI 动态构建演示（无业务变更）",
		ServiceName:          "ui-demo",
		ServiceKeys:          []string{"ui-demo"},
		Language:             "mixed",
		JenkinsfileMode:      "existing",
		ExecutionMode:        "serial",
		FailurePolicy:        "stop",
		ConnectionKey:        connectionKey,
		JenkinsJobName:       demoJenkinsJob,
		Enabled:              true,
		JenkinsfileRepo:      "https://example.invalid/ops-ui-demo.git",
		JenkinsfileBranch:    "main",
		JenkinsfilePath:      "Jenkinsfile",
		ManifestRepo:         "https://example.invalid/ops-ui-demo-manifests.git",
		ManifestBranch:       "main",
		ManifestPath:         "environments/" + environment + "/ui-demo/manifest.yaml",
		EnvironmentPaths:     map[string]string{environment: "environments/" + environment + "/ui-demo/manifest.yaml"},
		Parameters:           map[string]string{"DEMO_MODE": "true"},
		ParameterDefinitions: []ParameterDefinition{{Name: "DEMO_MODE", Type: "boolean", DefaultValue: "true", Description: "只执行界面演示，不构建或部署业务资源"}},
		SyncStatus:           "ready",
		LastSyncedAt:         now,
		CreatedAt:            now,
		UpdatedAt:            now,
	}
	if existing, getErr := s.store.GetCICDJob(ctx, project, demoJobKey); getErr == nil {
		job.CreatedAt = existing.CreatedAt
	} else if !errors.Is(getErr, os.ErrNotExist) {
		return Build{}, getErr
	}

	_, client, err := s.client(ctx, project, connectionKey)
	if err != nil {
		return Build{}, err
	}
	if err := client.upsertInlineJob(ctx, job, demoPipelineScript); err != nil {
		return Build{}, err
	}
	if err := s.store.SaveCICDJob(ctx, job); err != nil {
		return Build{}, err
	}

	build, triggerErr := s.TriggerBuild(ctx, project, demoJobKey, requestedBy, BuildInput{
		Environment: environment,
		Branch:      "demo",
		ImageTag:    "preview-" + now.Format("20060102-150405"),
		Services:    []string{"ui-demo"},
		Parameters:  map[string]string{"DEMO_MODE": "true"},
	})
	job.Enabled = false
	job.UpdatedAt = time.Now().UTC()
	disableErr := s.store.SaveCICDJob(ctx, job)
	if triggerErr != nil {
		return Build{}, triggerErr
	}
	if disableErr != nil {
		return Build{}, disableErr
	}
	return build, nil
}

const demoPipelineScript = `pipeline {
  agent any
  options {
    timeout(time: 5, unit: 'MINUTES')
  }
  stages {
    stage('ui-demo / 环境准备') {
      steps {
        echo '@@OPS_STAGE|ui-demo|checkout|running'
        echo '正在准备隔离的构建工作区，不会读取或修改业务仓库。'
        sleep time: 6, unit: 'SECONDS'
        echo '@@OPS_STAGE|ui-demo|checkout|succeeded'
      }
    }
    stage('ui-demo / 代码检查') {
      steps {
        echo '@@OPS_STAGE|ui-demo|build|running'
        echo '模拟执行代码检查与依赖分析。'
        sleep time: 7, unit: 'SECONDS'
        echo '@@OPS_STAGE|ui-demo|build|succeeded'
      }
    }
    stage('ui-demo / 镜像演示') {
      steps {
        echo '@@OPS_STAGE|ui-demo|image|running'
        echo '模拟镜像构建进度；本任务不会构建、推送任何镜像。'
        sleep time: 8, unit: 'SECONDS'
        echo '@@OPS_STAGE|ui-demo|image|succeeded'
      }
    }
    stage('ui-demo / 部署演示') {
      steps {
        echo '@@OPS_DEPLOY_BEGIN'
        echo '@@OPS_STAGE|ui-demo|deploy|running'
        echo '模拟部署清单更新；不会连接或变更业务 Kubernetes 资源。'
        sleep time: 9, unit: 'SECONDS'
        echo '@@OPS_DEPLOY|ui-demo|test|demo.invalid/ui-demo:preview'
        echo '@@OPS_STAGE|ui-demo|deploy|succeeded'
        echo '@@OPS_DEPLOY_END'
      }
    }
  }
}`

// StartGitCredentialProbe verifies a Jenkins credential through the same Git
// CLI path used by Pipeline SCM checkout. Neither the username nor password is
// printed; Jenkins masks the bound variables and the temporary askpass file is
// removed before the stage exits.
func (s *Service) StartGitCredentialProbe(ctx context.Context, project, connectionKey, environment, credentialKey, repositoryURL string) (Build, error) {
	credential, err := s.store.GetCICDCredential(ctx, project, credentialKey)
	if err != nil {
		return Build{}, err
	}
	if credential.ConnectionKey != connectionKey || credential.SyncStatus != "ready" {
		return Build{}, fmt.Errorf("%w: Git credential is not ready for the selected Jenkins", ErrInvalid)
	}
	job := Job{Key: "ops-git-probe", ProjectKey: project, DisplayName: "Git HTTPS 凭据探测（无业务变更）", ServiceName: "git-probe", ServiceKeys: []string{"git-probe"}, Language: "go", JenkinsfileMode: "existing", ExecutionMode: "serial", FailurePolicy: "stop", ConnectionKey: connectionKey, JenkinsJobName: "ops-platform-git-probe", Enabled: true, JenkinsfileRepo: "https://example.invalid/git-probe.git", JenkinsfileBranch: "main", JenkinsfilePath: "Jenkinsfile", ManifestRepo: "https://example.invalid/git-probe-manifests.git", ManifestBranch: "main", ManifestPath: "manifest.yaml", SyncStatus: "ready", LastSyncedAt: time.Now().UTC(), CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC()}
	if existing, getErr := s.store.GetCICDJob(ctx, project, job.Key); getErr == nil {
		job.CreatedAt = existing.CreatedAt
	} else if !errors.Is(getErr, os.ErrNotExist) {
		return Build{}, getErr
	}
	_, client, err := s.client(ctx, project, connectionKey)
	if err != nil {
		return Build{}, err
	}
	script := gitCredentialProbeScript(credential.ExternalID, repositoryURL)
	if err := client.upsertInlineJob(ctx, job, script); err != nil {
		return Build{}, err
	}
	if err := s.store.SaveCICDJob(ctx, job); err != nil {
		return Build{}, err
	}
	build, triggerErr := s.TriggerBuild(ctx, project, job.Key, "platform-git-probe", BuildInput{Environment: environment, Services: []string{"git-probe"}})
	job.Enabled = false
	disableErr := s.store.SaveCICDJob(ctx, job)
	if triggerErr != nil {
		return Build{}, triggerErr
	}
	if disableErr != nil {
		return Build{}, disableErr
	}
	return build, nil
}

func gitCredentialProbeScript(credentialID, repositoryURL string) string {
	return fmt.Sprintf(`pipeline {
  agent any
  stages {
    stage('Git HTTPS credential probe') {
      steps {
        withCredentials([usernamePassword(credentialsId: %s, usernameVariable: 'GIT_USER', passwordVariable: 'GIT_TOKEN')]) {
          withEnv(['PROBE_REPOSITORY=%s']) {
            sh '''#!/bin/sh
              set -eu
              set +x
              test -n "$GIT_USER" && test -n "$GIT_TOKEN"
              ASKPASS=$(mktemp)
              RESPONSE=$(mktemp)
              HEADERS=$(mktemp)
              trap 'rm -f "$ASKPASS" "$RESPONSE" "$HEADERS"' EXIT
              printf '#!/bin/sh\ncase "$1" in *Username*) echo "$GIT_USER";; *) echo "$GIT_TOKEN";; esac\n' > "$ASKPASS"
              chmod 700 "$ASKPASS"
              SMART_URL="${PROBE_REPOSITORY%%/}/info/refs?service=git-upload-pack"
              HTTP_STATUS=$(curl --silent --show-error --output "$RESPONSE" --dump-header "$HEADERS" --user "$GIT_USER:$GIT_TOKEN" --header 'Accept: application/x-git-upload-pack-advertisement' --write-out '%%{http_code}' "$SMART_URL")
              CONTENT_TYPE=$(awk 'BEGIN{IGNORECASE=1} /^content-type:/ {sub(/^[^:]*:[[:space:]]*/, ""); sub(/\r$/, ""); value=$0} END{print value}' "$HEADERS")
              echo "Git smart HTTP status=$HTTP_STATUS content-type=$CONTENT_TYPE"
              if [ "$HTTP_STATUS" != "200" ]; then
                tr '\n' ' ' < "$RESPONSE" | sed 's/<[^>]*>/ /g; s/[[:space:]][[:space:]]*/ /g' | cut -c1-800
              fi
              test "$HTTP_STATUS" = "200"
              grep -a -q '# service=git-upload-pack' "$RESPONSE"
              GIT_ASKPASS="$ASKPASS" GIT_TERMINAL_PROMPT=0 git ls-remote --heads "$PROBE_REPOSITORY" main >/dev/null
              echo 'Git HTTPS credential probe succeeded'
            '''
          }
        }
      }
    }
  }
}`, groovySingleQuoted(credentialID), strings.ReplaceAll(repositoryURL, "'", "%27"))
}

func groovySingleQuoted(value string) string {
	value = strings.ReplaceAll(value, `\`, `\\`)
	value = strings.ReplaceAll(value, `'`, `\'`)
	value = strings.ReplaceAll(value, `$`, `\$`)
	return `'` + value + `'`
}

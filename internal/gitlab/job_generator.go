package gitlab

import (
	"fmt"
	"net/url"
	"path"
	"sort"
	"strconv"
	"strings"

	"ops-deploy-platform/internal/cicd"
)

const (
	pipelineSchemaVersion = "4"
	pipelineLibraryPath   = "lib/v4/opsPipeline.groovy"
	kanikoBuildScriptPath = "lib/v4/scripts/build-kaniko.sh"
	dockerBuildScriptPath = "lib/v4/scripts/build-docker.sh"
	deployScriptPath      = "lib/v4/scripts/deploy-kubernetes.sh"
)

func generateJobFiles(job cicd.Job, services []ServiceSpec, manifestURL, manifestBranch string) []GeneratedFile {
	environment := jobEnvironment(job)
	services = normalizedJobServices(environment, services)
	jenkinsfilePath := strings.TrimSpace(job.JenkinsfilePath)
	if jenkinsfilePath == "" {
		jenkinsfilePath = "jobs/" + job.Key + "/Jenkinsfile"
	}
	jobPath := strings.TrimSuffix(path.Dir(jenkinsfilePath), ".")
	if jobPath != "" {
		jobPath += "/"
	}
	files := []GeneratedFile{
		{Repository: "jenkinsfiles", Path: jenkinsfilePath, Content: renderedJobJenkinsfile(job, manifestURL, manifestBranch)},
		{Repository: "jenkinsfiles", Path: jobPath + "services.groovy", Content: renderServiceCatalog(services)},
		{Repository: "jenkinsfiles", Path: pipelineLibraryPath, Content: renderOpsPipelineLibrary()},
		{Repository: "jenkinsfiles", Path: kanikoBuildScriptPath, Content: kanikoBuildScript},
		{Repository: "jenkinsfiles", Path: dockerBuildScriptPath, Content: dockerBuildScript},
		{Repository: "jenkinsfiles", Path: deployScriptPath, Content: deployKubernetesScript},
	}
	for _, service := range services {
		if service.DockerfileSource == "platform" {
			files = append(files, GeneratedFile{
				Repository: "jenkinsfiles",
				Path:       managedDockerfilePath(environment, service.Key),
				Content:    dockerfileContentForEnvironment(service, environment),
			})
		}
	}
	return files
}

func jobEnvironment(job cicd.Job) string {
	environment := strings.ToLower(strings.TrimSpace(job.EnvironmentKey))
	if environment == "" {
		environment = strings.ToLower(strings.TrimSpace(job.Parameters["DEPLOY_ENV"]))
	}
	if !validDeliveryEnvironment(environment) {
		environment = "dev"
	}
	return environment
}

func normalizedJobServices(environment string, input []ServiceSpec) []ServiceSpec {
	services := append([]ServiceSpec(nil), input...)
	sort.Slice(services, func(i, j int) bool { return services[i].Key < services[j].Key })
	for index := range services {
		services[index].DockerfileSource = dockerfileSource(services[index])
		if services[index].DockerfileSource == "platform" {
			services[index].Dockerfile = managedDockerfilePath(environment, services[index].Key)
		} else if strings.TrimSpace(services[index].Dockerfile) == "" {
			services[index].Dockerfile = "Dockerfile"
		}
		if strings.TrimSpace(services[index].BuildContext) == "" {
			services[index].BuildContext = "."
		}
	}
	return services
}

func renderedJobJenkinsfile(job cicd.Job, manifestURL, manifestBranch string) string {
	if content := strings.TrimSpace(job.JenkinsfileContent); content != "" {
		return content + "\n"
	}
	return renderJobJenkinsfile(job, manifestURL, manifestBranch)
}

func renderServiceCatalog(services []ServiceSpec) string {
	var catalog strings.Builder
	fmt.Fprintf(&catalog, `/*
 * 当前 Job 的服务配置
 * -----------------------------------------------------------------------------
 * 由运维自动部署平台生成，供 Jenkinsfile 和 %s 读取。
 *
 * 配置按“源码 → 构建 → 镜像 → 部署”分组，便于人工检查。
 * 此文件只允许保存 Jenkins Credential ID，禁止填写 Token、密码等明文。
 */

def services() {
  return [
`, pipelineLibraryPath)
	for index, service := range services {
		registryHost, awsRegion := registryDetails(service.ImageRepository)
		fmt.Fprintf(&catalog, `    %s: [
      displayName: %s,

      // 业务源码仓库：Jenkins 只读检出，不修改业务代码。
      source: [
        repository: %s,
        defaultBranch: %s,
        credentialId: %s
      ],

      // 镜像构建：依赖安装、编译和测试均由 Dockerfile 负责。
      build: [
        context: %s,
        dockerfileSource: %s,
        dockerfile: %s,
        target: %s,
        runEnvironment: %s,
        goPrivate: %s
      ],

      // 镜像推送目标。
      image: [
        repository: %s,
        registryHost: %s,
        awsRegion: %s
      ],

      // Kubernetes 发布目标。这里只保存凭据 ID，不保存密码明文。
      deploy: [
        namespace: %s,
        etcdPasswordCredentialId: %s
      ]
    ]`,
			groovyString(service.Key),
			groovyString(defaultValue(service.DisplayName, service.Key)),
			groovyString(service.SourceRepository),
			groovyString(service.SourceBranch),
			groovyString(service.SourceCredentialID),
			groovyString(service.BuildContext),
			groovyString(service.DockerfileSource),
			groovyString(service.Dockerfile),
			groovyString(service.DockerTarget),
			groovyString(service.RunEnvironment),
			groovyString(repositoryHost(service.SourceRepository)),
			groovyString(service.ImageRepository),
			groovyString(registryHost),
			groovyString(awsRegion),
			groovyString(service.Namespace),
			groovyString(service.EtcdPasswordCredentialID),
		)
		if index+1 < len(services) {
			catalog.WriteString(",")
		}
		catalog.WriteString("\n")
	}
	catalog.WriteString(`  ]
}

return this
`)
	return catalog.String()
}

func renderJobJenkinsfile(job cicd.Job, manifestURL, manifestBranch string) string {
	timeoutMinutes := boundedInt(job.Parameters["PIPELINE_TIMEOUT_MINUTES"], 30, 5, 180)
	rolloutTimeoutMinutes := boundedInt(job.Parameters["ROLLOUT_TIMEOUT_MINUTES"], 5, 1, 30)
	agentMode := strings.ToLower(strings.TrimSpace(job.Parameters["JENKINS_AGENT_MODE"]))
	if agentMode != "kubernetes" {
		agentMode = "node"
	}
	agentBlock := fmt.Sprintf(`agent {
    node {
      label %s
      customWorkspace "workspace/${JOB_NAME}/${BUILD_NUMBER}"
    }
  }`, groovyString(defaultValue(job.Parameters["JENKINS_AGENT_LABEL"], "master")))
	if agentMode == "kubernetes" {
		agentBlock = kubernetesAgentBlock(job)
	}
	verifyMode := strings.ToLower(strings.TrimSpace(job.Parameters["DEPLOY_VERIFY_MODE"]))
	if verifyMode != "apply" {
		verifyMode = "rollout"
	}
	rollbackOnFailure := strings.EqualFold(strings.TrimSpace(job.Parameters["ROLLBACK_ON_FAILURE"]), "true")
	jenkinsfilePath := strings.TrimSpace(job.JenkinsfilePath)
	if jenkinsfilePath == "" {
		jenkinsfilePath = "jobs/" + job.Key + "/Jenkinsfile"
	}
	catalogPath := path.Join(path.Dir(jenkinsfilePath), "services.groovy")
	defaultManifestCredential := "ops-gitlab-read"
	if projectKey := strings.ToLower(strings.TrimSpace(job.ProjectKey)); projectKey != "" {
		defaultManifestCredential = "ops-" + projectKey + "-gitlab-read"
	}

	template := `/*
 * @@JOB_DISPLAY_NAME@@
 * =============================================================================
 * 由运维自动部署平台生成 · Pipeline Schema @@SCHEMA_VERSION@@
 *
 * 这个 Jenkinsfile 只负责流程编排，不包含业务编译逻辑：
 *   1. 检出当前流水线仓库
 *   2. 根据 services.groovy 检出选中的业务服务
 *   3. 调用 Dockerfile 构建并推送镜像
 *   4. 渲染部署清单并发布到 Kubernetes
 *
 * 配置位置：
 *   - 当前 Job 服务配置：@@CATALOG_PATH@@
 *   - 公共流水线逻辑：@@LIBRARY_PATH@@
 *   - 业务编译与运行方式：各服务 Dockerfile
 *
 * 人工调整前请注意：平台再次同步 Job 时会覆盖本文件。
 */

// 同时加载公共流水线模块和当前 Job 的服务配置。
// Jenkins 不保证跨 stage 保存普通 Groovy 对象，因此每个 stage 都重新加载。
def loadPipelineDefinition(script) {
  return [
    runtime: script.load("${script.env.WORKSPACE}/.ops-pipeline/@@LIBRARY_PATH@@"),
    services: script.load("${script.env.WORKSPACE}/.ops-pipeline/@@CATALOG_PATH@@").services()
  ]
}

// Jenkins 首次只知道 Jenkinsfile；这里把完整流水线仓库检出到固定目录。
def checkoutPipelineRepository(script, pipelineScm) {
  def checkoutAction = {
    script.dir('.ops-pipeline') {
      script.deleteDir()
      script.checkout(pipelineScm)
    }
  }
  if (script.env.OPS_AGENT_MODE == 'kubernetes') {
    script.container('git') { checkoutAction() }
  } else {
    checkoutAction()
  }
}

// 后续阶段通过两个小文件读取本次构建选择，避免依赖不可序列化的全局变量。
def readBuildIdentity(script) {
  return [
    serviceKey: script.readFile('.ops-service-key').trim(),
    imageTag: script.readFile('.ops-image-tag').trim()
  ]
}

// 只有公共模块已经成功检出时才发送通知，避免初始化失败掩盖真实错误。
def notifyBuildResult(script, String status, String title) {
  def modulePath = "${script.env.WORKSPACE}/.ops-pipeline/@@LIBRARY_PATH@@"
  if (script.fileExists(modulePath)) {
    script.load(modulePath).notifyBuild(script, status, title)
  }
}

// 清理本次构建产生的临时文件，不删除 Jenkins 构建日志和归档记录。
def cleanupPipelineWorkspace(script) {
  def cleanupAction = {
    script.sh 'rm -rf .ops-manifests .ops-pipeline .ops-work .ops-runtime .ops-rendered-*.yaml .ops-docker-auth .ops-service-key .ops-image-tag .ops-image-ref .ops-image-digest .ops-source-revision .ops-notify-*'
  }
  if (script.env.OPS_AGENT_MODE == 'kubernetes') {
    script.container('git') { cleanupAction() }
  } else {
    cleanupAction()
  }
}

pipeline {
  @@AGENT_BLOCK@@

  options {
    // 每个 Job 串行执行，避免共享工作区或同一环境同时发布。
    skipDefaultCheckout(true)
    timeout(time: @@TIMEOUT_MINUTES@@, unit: 'MINUTES')
    disableConcurrentBuilds()
    buildDiscarder(logRotator(numToKeepStr: '20', daysToKeepStr: '30'))
  }

  environment {
    // 流水线运行模式。
    OPS_PIPELINE_SCHEMA = @@SCHEMA_VERSION_VALUE@@
    OPS_AGENT_MODE = @@AGENT_MODE@@

    // 部署清单仓库。
    MANIFEST_REPOSITORY = @@MANIFEST_URL@@
    MANIFEST_BRANCH = @@MANIFEST_BRANCH@@
    MANIFEST_CREDENTIAL_ID = @@MANIFEST_CREDENTIAL_ID@@
	MANIFEST_ROOT = @@MANIFEST_ROOT@@

    // Kubernetes 发布策略。
    DEPLOY_ENV = @@DEPLOY_ENV@@
    DEPLOY_VERIFY_MODE = @@DEPLOY_VERIFY_MODE@@
    ROLLOUT_TIMEOUT_MINUTES = @@ROLLOUT_TIMEOUT_MINUTES@@
    ROLLBACK_ON_FAILURE = @@ROLLBACK_ON_FAILURE@@

    // 可选的云身份和通知凭据。
    AWS_PROFILE = @@AWS_PROFILE@@
    TELEGRAM_CREDENTIALS_ID = @@TELEGRAM_CREDENTIAL_ID@@
    LARK_CREDENTIALS_ID = @@LARK_CREDENTIAL_ID@@
  }

  stages {
    stage('初始化流水线') {
      steps {
        script {
          checkoutPipelineRepository(this, scm)

          def pipelineDefinition = loadPipelineDefinition(this)
          if (pipelineDefinition.runtime.schemaVersion() != env.OPS_PIPELINE_SCHEMA) {
            error("流水线模块版本不匹配: Jenkinsfile=${env.OPS_PIPELINE_SCHEMA}, module=${pipelineDefinition.runtime.schemaVersion()}")
          }

          def serviceKey = pipelineDefinition.runtime.selectedService(
            this,
            params.TARGET_SERVICES,
            pipelineDefinition.services
          )
          def imageTag = pipelineDefinition.runtime.createVersion(env.BUILD_NUMBER, env.DEPLOY_ENV)

          // 保存本次构建身份，供后续 stage 和通知稳定读取。
          writeFile file: '.ops-service-key', text: serviceKey
          writeFile file: '.ops-image-tag', text: imageTag

          def service = pipelineDefinition.services[serviceKey]
          echo "服务: ${service.displayName} (${serviceKey})"
          echo "分支: ${params.GIT_BRANCH?.trim() ?: service.source.defaultBranch}"
          echo "镜像版本: ${imageTag}"
          echo "部署环境: ${env.DEPLOY_ENV}"
        }
      }
    }

    stage('获取业务代码') {
      steps {
        script {
          def pipelineDefinition = loadPipelineDefinition(this)
          def build = readBuildIdentity(this)
          pipelineDefinition.runtime.checkoutSource(
            this,
            build.serviceKey,
            pipelineDefinition.services[build.serviceKey],
            params.GIT_BRANCH
          )
        }
      }
    }

    stage('构建并推送镜像') {
      steps {
        script {
          def pipelineDefinition = loadPipelineDefinition(this)
          def build = readBuildIdentity(this)
          pipelineDefinition.runtime.buildAndPushImage(
            this,
            build.serviceKey,
            pipelineDefinition.services[build.serviceKey],
            build.imageTag
          )
        }
      }
    }

    stage('部署到 Kubernetes') {
      steps {
        script {
          def pipelineDefinition = loadPipelineDefinition(this)
          def build = readBuildIdentity(this)
          pipelineDefinition.runtime.deployService(
            this,
            build.serviceKey,
            pipelineDefinition.services[build.serviceKey],
            build.imageTag
          )
        }
      }
    }
  }

  post {
    success {
      script {
        notifyBuildResult(this, 'SUCCESS', '构建和部署成功')
      }
    }

    failure {
      script {
        notifyBuildResult(this, 'FAILURE', '构建或部署失败')
      }
    }

    always {
      script {
        try {
          cleanupPipelineWorkspace(this)
        } catch (error) {
          echo "工作区清理已跳过: ${error.message}"
        }
        echo "流水线结束: ${currentBuild.currentResult}"
      }
    }
  }
}
`

	values := map[string]string{
		"@@JOB_DISPLAY_NAME@@":        groovyCommentText(defaultValue(job.DisplayName, job.Key)),
		"@@SCHEMA_VERSION@@":          pipelineSchemaVersion,
		"@@SCHEMA_VERSION_VALUE@@":    groovyString(pipelineSchemaVersion),
		"@@CATALOG_PATH@@":            catalogPath,
		"@@LIBRARY_PATH@@":            pipelineLibraryPath,
		"@@AGENT_BLOCK@@":             agentBlock,
		"@@AGENT_MODE@@":              groovyString(agentMode),
		"@@TIMEOUT_MINUTES@@":         strconv.Itoa(timeoutMinutes),
		"@@MANIFEST_URL@@":            groovyString(manifestURL),
		"@@MANIFEST_BRANCH@@":         groovyString(manifestBranch),
		"@@MANIFEST_CREDENTIAL_ID@@":  groovyString(defaultValue(job.Parameters["MANIFEST_CREDENTIAL_ID"], defaultManifestCredential)),
		"@@MANIFEST_ROOT@@":           groovyString(manifestRootForJob(job)),
		"@@DEPLOY_ENV@@":              groovyString(defaultValue(job.Parameters["DEPLOY_ENV"], "dev")),
		"@@DEPLOY_VERIFY_MODE@@":      groovyString(verifyMode),
		"@@ROLLOUT_TIMEOUT_MINUTES@@": groovyString(strconv.Itoa(rolloutTimeoutMinutes)),
		"@@ROLLBACK_ON_FAILURE@@":     groovyString(strconv.FormatBool(rollbackOnFailure)),
		"@@AWS_PROFILE@@":             groovyString(job.Parameters["AWS_PROFILE"]),
		"@@TELEGRAM_CREDENTIAL_ID@@":  groovyString(job.Parameters["TELEGRAM_CREDENTIALS_ID"]),
		"@@LARK_CREDENTIAL_ID@@":      groovyString(job.Parameters["LARK_CREDENTIALS_ID"]),
	}
	return replaceTemplateValues(template, values)
}

func manifestRootForJob(job cicd.Job) string {
	environment := strings.ToLower(strings.TrimSpace(job.EnvironmentKey))
	if environment == "" {
		environment = strings.ToLower(strings.TrimSpace(job.Parameters["DEPLOY_ENV"]))
	}
	if environment == "" {
		environment = "dev"
	}
	root := strings.Trim(strings.TrimSpace(job.EnvironmentPaths[environment]), "/")
	if root == "" {
		root = strings.Trim(strings.TrimSpace(job.ManifestPath), "/")
	}
	if root == "" || root == "environments" {
		root = "environments/" + environment
	}
	return root
}

func replaceTemplateValues(template string, values map[string]string) string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool { return len(keys[i]) > len(keys[j]) })
	for _, key := range keys {
		template = strings.ReplaceAll(template, key, values[key])
	}
	return template
}

func groovyCommentText(value string) string {
	return strings.TrimSpace(strings.NewReplacer(
		"\r", " ",
		"\n", " ",
		"*/", "* /",
	).Replace(value))
}

func boundedInt(raw string, fallback, minimum, maximum int) int {
	value, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil || value < minimum || value > maximum {
		return fallback
	}
	return value
}

func registryDetails(imageRepository string) (host, region string) {
	host = strings.SplitN(strings.TrimSpace(imageRepository), "/", 2)[0]
	parts := strings.Split(host, ".")
	if len(parts) >= 6 && parts[1] == "dkr" && parts[2] == "ecr" && parts[4] == "amazonaws" && parts[5] == "com" {
		region = parts[3]
	}
	return host, region
}

func kubernetesAgentBlock(job cicd.Job) string {
	serviceAccount := strings.ToLower(strings.TrimSpace(job.Parameters["JENKINS_KUBERNETES_SERVICE_ACCOUNT"]))
	if !keyPattern.MatchString(serviceAccount) {
		serviceAccount = "jenkins"
	}
	return fmt.Sprintf(`agent {
    kubernetes {
      defaultContainer 'git'
      yaml '''
apiVersion: v1
kind: Pod
spec:
  serviceAccountName: %s
  securityContext:
    fsGroup: 1000
  containers:
    # 检出流水线仓库、业务源码和部署清单。
    - name: git
      image: alpine/git:2.47.2
      command:
        - cat
      tty: true
    # 在 Pod 内构建并推送镜像，不依赖 Docker daemon。
    - name: kaniko
      image: gcr.io/kaniko-project/executor:v1.23.2-debug
      command:
        - /busybox/cat
      tty: true
    # 校验和应用 Kubernetes 部署清单。
    - name: kubectl
      image: alpine/kubectl:1.34.0
      command:
        - cat
      tty: true
'''
    }
  }`, serviceAccount)
}

func repositoryHost(raw string) string {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return ""
	}
	return parsed.Hostname()
}

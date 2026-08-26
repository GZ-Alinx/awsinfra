package gitlab

func renderOpsPipelineLibrary() string {
	return replaceTemplateValues(opsPipelineLibrary, map[string]string{
		"@@PIPELINE_SCHEMA@@":     pipelineSchemaVersion,
		"@@KANIKO_BUILD_SCRIPT@@": kanikoBuildScriptPath,
		"@@DOCKER_BUILD_SCRIPT@@": dockerBuildScriptPath,
		"@@DEPLOY_SCRIPT@@":       deployScriptPath,
	})
}

const opsPipelineLibrary = `// 由运维自动部署平台管理；平台重新同步时会覆盖本文件。
/*
 * 公共流水线运行模块
 * =============================================================================
 * 本文件只实现通用动作，不保存任何项目密码：
 *
 *   checkoutSource()       检出业务源码并校验构建入口
 *   buildAndPushImage()    调用 Docker 或 Kaniko 构建、推送镜像
 *   deployService()        渲染部署清单、校验并发布到 Kubernetes
 *   notifyBuild()          发送可选的 Telegram / Lark 构建通知
 *
 * Job 和服务配置分别位于 jobs/<job>/Jenkinsfile、services.groovy。
 * lib/v@@PIPELINE_SCHEMA@@ 目录用于隔离流水线版本，避免升级一个 Job 影响其他 Job。
 */

// Jenkinsfile 在初始化阶段用它确认入口文件和公共模块属于同一版本。
def schemaVersion() {
  return '@@PIPELINE_SCHEMA@@'
}

// 生成可排序、可追踪且不重复的镜像版本。
// ECR 仓库由同一项目的多个环境共享，因此 Tag 必须带环境前缀，
// 例如 prod-20260806153000_42，避免生产和测试镜像难以区分。
def createVersion(String buildNumber, String environment) {
  def environmentPrefix = (environment ?: 'unknown').trim().toLowerCase()
  if (!(environmentPrefix ==~ /[a-z0-9][a-z0-9_.-]*/)) {
    throw new IllegalArgumentException("非法部署环境，无法生成镜像版本: ${environment}")
  }
  return environmentPrefix + '-' + new Date().format('yyyyMMddHHmmss') + "_${buildNumber ?: '0'}"
}

// 将 Secret Text 安全放入 YAML 双引号字符串，不把换行破坏成新字段。
def yamlDoubleQuoted(String value) {
  return '"' + (value ?: '').replace('\\', '\\\\').replace('"', '\\"').replace('\r', '\\r').replace('\n', '\\n') + '"'
}

// Jenkins 每次只能构建一个服务；平台多选时会拆成多条独立构建。
def selectedService(script, String raw, Map services) {
  def allowed = services.keySet().toList().sort()
  if (allowed.isEmpty()) {
    script.error('当前 Job 没有关联任何服务')
  }
  def selected = (raw ?: allowed[0]).trim().toLowerCase()
  if (!selected || selected.contains(',') || !services.containsKey(selected)) {
    script.error("每次只能构建一个已登记服务，可选值: ${allowed.join(', ')}")
  }
  validateServiceConfig(script, selected, services[selected])
  return selected
}

// 在任何源码检出或集群变更前检查人工修改后的服务配置。
def validateServiceConfig(script, String serviceKey, Map config) {
  def required = [
    'source.repository': config.source?.repository,
    'source.defaultBranch': config.source?.defaultBranch,
    'build.context': config.build?.context,
    'build.dockerfileSource': config.build?.dockerfileSource,
    'build.dockerfile': config.build?.dockerfile,
    'image.repository': config.image?.repository,
    'image.registryHost': config.image?.registryHost,
    'deploy.namespace': config.deploy?.namespace
  ]
  def missing = required.findAll { _, value -> !value?.toString()?.trim() }.keySet().toList()
  if (!missing.isEmpty()) {
    script.error("服务 ${serviceKey} 配置不完整，缺少字段: ${missing.join(', ')}")
  }
}

// -----------------------------------------------------------------------------
// 1. 源码检出
// -----------------------------------------------------------------------------

def checkoutSource(script, String serviceKey, Map config, String requestedBranch) {
  script.echo("@@OPS_STAGE|${serviceKey}|checkout|running")
  def branch = requestedBranch?.trim() ?: config.source.defaultBranch
  if (!branch?.trim()) {
    script.error("服务 ${serviceKey} 没有可用的源码分支")
  }

  // 每个服务使用独立源码目录，避免一个 Job 关联多个服务时互相覆盖。
  def checkoutAction = {
    script.dir(".ops-work/${serviceKey}") {
      script.deleteDir()
      def remote = [url: config.source.repository]
      if (config.source.credentialId) {
        remote.credentialsId = config.source.credentialId
      }
      script.retry(3) {
        script.checkout([
          $class: 'GitSCM',
          branches: [[name: "*/${branch}"]],
          userRemoteConfigs: [remote],
          extensions: [
            [$class: 'CloneOption', honorRefspec: true, noTags: false, shallow: false, timeout: 20],
            [$class: 'CheckoutOption', timeout: 20]
          ]
        ])
      }
      def revision = script.sh(returnStdout: true, script: 'git rev-parse HEAD').trim()
      script.writeFile file: "${script.env.WORKSPACE}/.ops-source-revision", text: revision
      script.echo("@@OPS_SOURCE|${serviceKey}|${branch}|${revision}")
    }

    // Dockerfile 可由业务仓库维护，也可由平台托管在流水线仓库。
    def dockerfilePath = config.build.dockerfileSource == 'source'
      ? "${script.env.WORKSPACE}/.ops-work/${serviceKey}/${config.build.dockerfile}"
      : "${script.env.WORKSPACE}/.ops-pipeline/${config.build.dockerfile}"
    script.withEnv([
      "OPS_SOURCE_DIR=${script.env.WORKSPACE}/.ops-work/${serviceKey}",
      "OPS_BUILD_CONTEXT=${script.env.WORKSPACE}/.ops-work/${serviceKey}/${config.build.context}",
      "OPS_DOCKERFILE_PATH=${dockerfilePath}"
    ]) {
      script.sh '''#!/bin/sh
        set -eu
        test -d "$OPS_SOURCE_DIR" || { echo "源码目录不存在: $OPS_SOURCE_DIR"; exit 1; }
        test -d "$OPS_BUILD_CONTEXT" || { echo "Build Context 不存在: $OPS_BUILD_CONTEXT"; exit 1; }
        test -f "$OPS_DOCKERFILE_PATH" || { echo "Dockerfile 不存在: $OPS_DOCKERFILE_PATH"; exit 1; }
      '''
    }
  }
  if (script.env.OPS_AGENT_MODE == 'kubernetes') {
    script.container('git') { checkoutAction() }
  } else {
    checkoutAction()
  }
  script.echo("@@OPS_STAGE|${serviceKey}|checkout|succeeded")
}

// -----------------------------------------------------------------------------
// 2. 镜像构建与推送
// -----------------------------------------------------------------------------

def buildAndPushImage(script, String serviceKey, Map config, String imageTag) {
  script.echo("@@OPS_STAGE|${serviceKey}|image|running")
  def dockerfilePath = config.build.dockerfileSource == 'source'
    ? "${script.env.WORKSPACE}/.ops-work/${serviceKey}/${config.build.dockerfile}"
    : "${script.env.WORKSPACE}/.ops-pipeline/${config.build.dockerfile}"
  def fullImage = "${config.image.repository}:${imageTag}"

  // 只通过环境变量把本次构建契约传给执行脚本。
  def buildEnvironment = [
    "OPS_SERVICE_KEY=${serviceKey}",
    "OPS_IMAGE_NAME=${config.image.repository}",
    "OPS_IMAGE_TAG=${imageTag}",
    "OPS_FULL_IMAGE=${fullImage}",
    "OPS_BUILD_CONTEXT=${script.env.WORKSPACE}/.ops-work/${serviceKey}/${config.build.context}",
    "OPS_DOCKERFILE_PATH=${dockerfilePath}",
    "OPS_DOCKER_TARGET=${config.build.target ?: ''}",
    "OPS_RUN_ENV=${config.build.runEnvironment ?: script.env.DEPLOY_ENV}",
    "OPS_REGISTRY_HOST=${config.image.registryHost}",
    "OPS_AWS_REGION=${config.image.awsRegion}",
    "OPS_GOPRIVATE=${config.build.goPrivate ?: ''}"
  ]
  def buildAction = {
    script.withEnv(buildEnvironment) {
      if (script.env.OPS_AGENT_MODE == 'kubernetes') {
        script.container('kaniko') {
          script.withEnv([
            "AWS_REGION=${config.image.awsRegion}",
            "AWS_DEFAULT_REGION=${config.image.awsRegion}",
            'AWS_SDK_LOAD_CONFIG=true'
          ]) {
            script.sh '''#!/busybox/sh
              exec /busybox/sh "$WORKSPACE/.ops-pipeline/@@KANIKO_BUILD_SCRIPT@@"
            '''
          }
        }
      } else {
        script.sh '/usr/bin/env bash "$WORKSPACE/.ops-pipeline/@@DOCKER_BUILD_SCRIPT@@"'
      }
    }
  }

  // Credential 只在构建动作执行期间绑定，Jenkins 会屏蔽日志中的明文。
  if (config.source.credentialId) {
    script.withCredentials([[
      $class: 'UsernamePasswordMultiBinding',
      credentialsId: config.source.credentialId,
      usernameVariable: 'SOURCE_GIT_USER',
      passwordVariable: 'SOURCE_GIT_TOKEN'
    ]]) {
      buildAction()
    }
  } else {
    script.withEnv(['SOURCE_GIT_USER=', 'SOURCE_GIT_TOKEN=']) {
      buildAction()
    }
  }
  script.writeFile file: '.ops-image-ref', text: fullImage
  script.echo("@@OPS_IMAGE|${serviceKey}|${fullImage}")
  script.echo("@@OPS_STAGE|${serviceKey}|image|succeeded")
}

// -----------------------------------------------------------------------------
// 3. Kubernetes 发布
// -----------------------------------------------------------------------------

def deployService(script, String serviceKey, Map config, String imageTag) {
  def fullImage = "${config.image.repository}:${imageTag}"
  script.echo('@@OPS_DEPLOY_BEGIN')
  script.echo("@@OPS_STAGE|${serviceKey}|deploy|running")
  script.echo("@@OPS_DEPLOY|${serviceKey}|${script.env.DEPLOY_ENV}|${fullImage}")

  script.withCredentials([[
    $class: 'UsernamePasswordMultiBinding',
    credentialsId: script.env.MANIFEST_CREDENTIAL_ID,
    usernameVariable: 'GIT_USER',
    passwordVariable: 'GIT_TOKEN'
  ]]) {
    def cloneAction = {
      script.sh '''#!/bin/sh
        set -eu
        rm -rf .ops-manifests
        ASKPASS=$(mktemp)
        cleanup() { rm -f "$ASKPASS"; }
        trap cleanup EXIT
        printf '#!/bin/sh\ncase "$1" in *Username*) printf "%s\\n" "$GIT_USER";; *) printf "%s\\n" "$GIT_TOKEN";; esac\n' > "$ASKPASS"
        chmod 700 "$ASKPASS"
        GIT_ASKPASS="$ASKPASS" GIT_TERMINAL_PROMPT=0 \
          git clone --depth 1 --branch "$MANIFEST_BRANCH" "$MANIFEST_REPOSITORY" .ops-manifests
      '''
    }
    if (script.env.OPS_AGENT_MODE == 'kubernetes') {
      script.container('git') { cloneAction() }
    } else {
      cloneAction()
    }
  }

  // 每个环境、每个服务都有独立清单，禁止跨环境复用渲染结果。
	def relativePath = "${script.env.MANIFEST_ROOT}/${serviceKey}/manifest.yaml"
  def sourcePath = ".ops-manifests/${relativePath}"
  if (!script.fileExists(sourcePath)) {
    script.error("部署清单不存在: ${relativePath}")
  }
  def content = script.readFile(sourcePath)
  if (!content.contains('{{IMAGE}}')) {
    script.error("部署清单 ${relativePath} 缺少 {{IMAGE}} 占位符，已拒绝部署旧镜像")
  }
  content = content.replace('{{IMAGE}}', fullImage)
  if (content.contains('{{ETCD_PASSWORD}}')) {
    if (!config.deploy.etcdPasswordCredentialId) {
      script.error("服务 ${serviceKey} 的清单需要 etcd 密码凭据")
    }
    script.withCredentials([[
      $class: 'StringBinding',
      credentialsId: config.deploy.etcdPasswordCredentialId,
      variable: 'OPS_ETCD_PASSWORD_VALUE'
    ]]) {
      content = content.replace('{{ETCD_PASSWORD}}', yamlDoubleQuoted(script.env.OPS_ETCD_PASSWORD_VALUE))
    }
  }
  def unresolved = (content =~ /\{\{[A-Z][A-Z0-9_]*\}\}/).find()
  if (unresolved) {
    script.error("部署清单 ${relativePath} 仍有未解析的受管占位符")
  }

  def renderedPath = ".ops-rendered-${serviceKey}.yaml"
  script.writeFile file: renderedPath, text: content
  try {
    def deployAction = {
      script.withEnv([
        "OPS_RENDERED_MANIFEST=${script.env.WORKSPACE}/${renderedPath}",
        "OPS_TARGET_NAMESPACE=${config.deploy.namespace}",
        "OPS_EXPECTED_IMAGE=${fullImage}"
      ]) {
        script.sh '/bin/sh "$WORKSPACE/.ops-pipeline/@@DEPLOY_SCRIPT@@"'
      }
    }
    if (script.env.OPS_AGENT_MODE == 'kubernetes') {
      script.container('kubectl') { deployAction() }
    } else {
      deployAction()
    }
  } finally {
    script.sh "rm -f '${renderedPath}'"
  }
  script.echo("@@OPS_STAGE|${serviceKey}|deploy|succeeded")
  script.echo('@@OPS_DEPLOY_END')
}

// -----------------------------------------------------------------------------
// 4. 构建通知
// -----------------------------------------------------------------------------

def notifyBuild(script, String status, String title) {
  def serviceKey = script.fileExists('.ops-service-key') ? script.readFile('.ops-service-key').trim() : 'unknown'
  def imageTag = script.fileExists('.ops-image-tag') ? script.readFile('.ops-image-tag').trim() : 'unknown'
  def sourceRevision = script.fileExists('.ops-source-revision') ? script.readFile('.ops-source-revision').trim() : 'unknown'
  def message = """${title}
服务: ${serviceKey}
分支: ${script.params.GIT_BRANCH?.trim() ?: '服务默认分支'}
版本: ${imageTag}
源码提交: ${sourceRevision}
环境: ${script.env.DEPLOY_ENV}
状态: ${status}
任务: ${script.env.JOB_NAME} #${script.env.BUILD_NUMBER}
时间: ${new Date().format('yyyy-MM-dd HH:mm:ss')}"""
  script.writeFile file: '.ops-notify-message.txt', text: message

  if (script.env.TELEGRAM_CREDENTIALS_ID?.trim()) {
    try {
      script.withCredentials([[
        $class: 'StringBinding',
        credentialsId: script.env.TELEGRAM_CREDENTIALS_ID,
        variable: 'TG_CREDS'
      ]]) {
        script.sh '''#!/bin/sh
          set +e
          TG_TOKEN="${TG_CREDS%%|*}"
          TG_CHAT_ID="${TG_CREDS#*|}"
          curl -fsS -X POST "https://api.telegram.org/bot${TG_TOKEN}/sendMessage" \
            --data-urlencode "chat_id=${TG_CHAT_ID}" \
            --data-urlencode 'text@.ops-notify-message.txt' \
            --data-urlencode 'disable_web_page_preview=true' >/dev/null 2>&1 || true
        '''
      }
    } catch (error) {
      script.echo("Telegram 通知发送失败: ${error.message}")
    }
  }

  if (script.env.LARK_CREDENTIALS_ID?.trim()) {
    try {
      def payload = groovy.json.JsonOutput.toJson([msg_type: 'text', content: [text: message]])
      script.writeFile file: '.ops-notify-lark.json', text: payload
      script.withCredentials([[
        $class: 'StringBinding',
        credentialsId: script.env.LARK_CREDENTIALS_ID,
        variable: 'LARK_WEBHOOK'
      ]]) {
        script.sh '''#!/bin/sh
          set +e
          curl -fsS -X POST "$LARK_WEBHOOK" \
            -H 'Content-Type: application/json' \
            -d @.ops-notify-lark.json >/dev/null 2>&1 || true
        '''
      }
    } catch (error) {
      script.echo("Lark 通知发送失败: ${error.message}")
    }
  }
}

return this
`

const kanikoBuildScript = `#!/busybox/sh
set -eu

# Kubernetes 动态 Agent 的镜像构建入口。
# 输入全部来自 opsPipeline.groovy 注入的 OPS_* 环境变量。
# 本脚本只负责调用 Kaniko；业务编译逻辑必须保留在 Dockerfile。

# 先检查路径，错误会直接指向平台中需要修正的字段。
test -d "$OPS_BUILD_CONTEXT" || {
  echo "Build Context 不存在: $OPS_BUILD_CONTEXT"
  exit 1
}
test -f "$OPS_DOCKERFILE_PATH" || {
  echo "Dockerfile 不存在: $OPS_DOCKERFILE_PATH"
  exit 1
}

TARGET_ARG=""
if [ -n "${OPS_DOCKER_TARGET:-}" ]; then
  TARGET_ARG="--target=$OPS_DOCKER_TARGET"
fi

# Kaniko 在 Pod 内直接推送镜像，不依赖 Docker daemon。
echo "开始构建镜像: $OPS_FULL_IMAGE"
/kaniko/executor \
  --context "$OPS_BUILD_CONTEXT" \
  --dockerfile "$OPS_DOCKERFILE_PATH" \
  $TARGET_ARG \
  --build-arg "RUN_ENV=$OPS_RUN_ENV" \
  --build-arg "BUILD_VERSION=$OPS_IMAGE_TAG" \
  --build-arg "SOURCE_GIT_USER=${SOURCE_GIT_USER:-}" \
  --build-arg "SOURCE_GIT_TOKEN=${SOURCE_GIT_TOKEN:-}" \
  --build-arg "GOPRIVATE=$OPS_GOPRIVATE" \
  --destination "$OPS_FULL_IMAGE" \
  --digest-file "$WORKSPACE/.ops-image-digest" \
  --cleanup

# 摘要用于确认镜像确实已推送完成，而不只是本地构建成功。
test -s "$WORKSPACE/.ops-image-digest" || {
  echo "镜像已推送，但未取得镜像摘要"
  exit 1
}
echo "镜像推送完成: $OPS_FULL_IMAGE@$(cat "$WORKSPACE/.ops-image-digest")"
`

const dockerBuildScript = `#!/usr/bin/env bash
set -euo pipefail

# 固定 Jenkins 节点的镜像构建入口。
# 节点必须预装 Docker 和 AWS CLI；业务编译逻辑由 Dockerfile 负责。

# 先检查平台配置的构建路径。
[[ -d "$OPS_BUILD_CONTEXT" ]] || {
  echo "Build Context 不存在: $OPS_BUILD_CONTEXT"
  exit 1
}
[[ -f "$OPS_DOCKERFILE_PATH" ]] || {
  echo "Dockerfile 不存在: $OPS_DOCKERFILE_PATH"
  exit 1
}

target_args=()
if [[ -n "${OPS_DOCKER_TARGET:-}" ]]; then
  target_args=(--target "$OPS_DOCKER_TARGET")
fi

# 每个服务使用独立 Docker 配置目录，避免并发 Job 共享登录状态。
export DOCKER_CONFIG="$WORKSPACE/.ops-docker-auth/$OPS_SERVICE_KEY"
rm -rf "$DOCKER_CONFIG"
install -d -m 700 "$DOCKER_CONFIG"
cleanup() {
  docker image rm "$OPS_FULL_IMAGE" >/dev/null 2>&1 || true
  rm -rf "$DOCKER_CONFIG"
}
trap cleanup EXIT

# ECR 使用短期登录令牌；其他镜像仓库由 Jenkins 节点预先配置。
if [[ "$OPS_REGISTRY_HOST" == *.dkr.ecr.*.amazonaws.com ]]; then
  profile_args=()
  if [[ -n "${AWS_PROFILE:-}" ]]; then
    profile_args=(--profile "$AWS_PROFILE")
  fi
  aws ecr get-login-password --region "$OPS_AWS_REGION" "${profile_args[@]}" |
    docker login --username AWS --password-stdin "$OPS_REGISTRY_HOST" >/dev/null
fi

# Dockerfile 负责安装依赖、编译、测试和生成最终运行镜像。
echo "开始构建镜像: $OPS_FULL_IMAGE"
DOCKER_BUILDKIT=1 docker build \
  --file "$OPS_DOCKERFILE_PATH" \
  "${target_args[@]}" \
  --build-arg "RUN_ENV=$OPS_RUN_ENV" \
  --build-arg "BUILD_VERSION=$OPS_IMAGE_TAG" \
  --build-arg "SOURCE_GIT_USER=${SOURCE_GIT_USER:-}" \
  --build-arg "SOURCE_GIT_TOKEN=${SOURCE_GIT_TOKEN:-}" \
  --build-arg "GOPRIVATE=$OPS_GOPRIVATE" \
  --tag "$OPS_FULL_IMAGE" \
  "$OPS_BUILD_CONTEXT"

docker push "$OPS_FULL_IMAGE"
echo "镜像推送完成: $OPS_FULL_IMAGE"
`

const deployKubernetesScript = `#!/bin/sh
set -eu

# Kubernetes 发布入口。
# 执行顺序：本地解析 → 服务端 dry-run → apply → rollout → 镜像核对。
# 任意校验失败都会返回非零状态；自动回滚默认关闭以保留故障现场。

test -f "$OPS_RENDERED_MANIFEST" || {
  echo "渲染后的部署清单不存在: $OPS_RENDERED_MANIFEST"
  exit 1
}

runtime_dir="$WORKSPACE/.ops-runtime/${BUILD_NUMBER:-0}-$$"
mkdir -p "$runtime_dir"
cleanup() {
  rm -rf "$runtime_dir"
}
trap cleanup EXIT

# 记录清单中的 Deployment，后续逐个等待并核对镜像。
workloads_file="$runtime_dir/deployments"
revisions_file="$runtime_dir/revisions"
: > "$workloads_file"
: > "$revisions_file"

kubectl create --dry-run=client -f "$OPS_RENDERED_MANIFEST" -o name |
  sed -n 's#^deployment.apps/##p' |
  sort -u > "$workloads_file"

# 先让 Kubernetes API Server 完整校验，成功前绝不修改集群。
validation_log="$runtime_dir/server-validation.log"
if ! kubectl apply --dry-run=server -f "$OPS_RENDERED_MANIFEST" >"$validation_log" 2>&1; then
  cat "$validation_log"
  if grep -q 'spec\.ports.*Duplicate value' "$validation_log"; then
    echo "检测到 Service 端口名称重复。常见原因是修改了 Service port 后，旧端口被 kubectl 合并保留；请先核对线上 Service 的 spec.ports。"
  fi
  echo "Kubernetes 服务端校验失败，尚未修改集群。请优先检查上方第一个错误。"
  exit 1
fi
cat "$validation_log"

# 保存发布前 revision，只有显式开启回滚时才会使用。
while IFS= read -r workload; do
  [ -n "$workload" ] || continue
  revision=$(kubectl get deployment "$workload" \
    -n "$OPS_TARGET_NAMESPACE" \
    -o jsonpath='{.metadata.annotations.deployment\.kubernetes\.io/revision}' \
    2>/dev/null || true)
  printf '%s %s\n' "$workload" "$revision" >> "$revisions_file"
done < "$workloads_file"

# dry-run 已通过，现在才真正修改集群。
kubectl apply -f "$OPS_RENDERED_MANIFEST"

# “仅应用”适用于不希望 Jenkins 等待就绪的特殊场景。
if [ "${DEPLOY_VERIFY_MODE:-rollout}" = "apply" ]; then
  echo "部署清单已应用；当前策略不等待工作负载就绪。"
  exit 0
fi

# 默认逐个等待 Deployment 就绪，并确认实际运行的是本次镜像。
rollout_failed=0
while IFS= read -r workload; do
  [ -n "$workload" ] || continue
  if ! kubectl rollout status "deployment/$workload" \
    -n "$OPS_TARGET_NAMESPACE" \
    --timeout="${ROLLOUT_TIMEOUT_MINUTES:-5}m"; then
    rollout_failed=1
    echo "工作负载未按时就绪: $OPS_TARGET_NAMESPACE/$workload"
    kubectl get deployment "$workload" -n "$OPS_TARGET_NAMESPACE" -o wide || true
    kubectl describe deployment "$workload" -n "$OPS_TARGET_NAMESPACE" || true
    kubectl get pods -n "$OPS_TARGET_NAMESPACE" -o wide || true
    break
  fi
  actual_images=$(kubectl get deployment "$workload" \
    -n "$OPS_TARGET_NAMESPACE" \
    -o jsonpath='{.spec.template.spec.containers[*].image}')
  case " $actual_images " in
    *" $OPS_EXPECTED_IMAGE "*) ;;
    *)
      echo "工作负载镜像校验失败: $workload"
      echo "期望镜像: $OPS_EXPECTED_IMAGE"
      echo "实际镜像: $actual_images"
      rollout_failed=1
      break
      ;;
  esac
  echo "工作负载已就绪: $OPS_TARGET_NAMESPACE/$workload -> $OPS_EXPECTED_IMAGE"
done < "$workloads_file"

if [ "$rollout_failed" -eq 0 ]; then
  exit 0
fi

# 自动回滚是显式选项。默认保留现场，便于查看 Pod 事件和日志。
if [ "${ROLLBACK_ON_FAILURE:-false}" = "true" ]; then
  echo "部署失败，开始恢复发布前的 Deployment revision"
  while read -r workload revision; do
    [ -n "$workload" ] || continue
    if [ -n "$revision" ]; then
      kubectl rollout undo "deployment/$workload" \
        -n "$OPS_TARGET_NAMESPACE" \
        --to-revision="$revision" || true
    fi
  done < "$revisions_file"
else
  echo "自动回滚未开启；保留现场供排查，可在平台修正后重试。"
fi
exit 1
`

<template>
  <div class="service-catalog">
    <a-card>
      <template #title><span class="card-title">服务与部署清单</span></template>
      <template #extra
        ><a-space
          ><a-button :loading="loading" @click="load"
            ><icon-refresh />刷新</a-button
          ><a-button
            type="primary"
            :disabled="!store.canConfigure || !delivery.server_key"
            @click="openService()"
            ><icon-plus />添加服务</a-button
          ></a-space
        ></template
      >
      <a-alert type="info" show-icon class="section-gap"
        >平台只负责编辑和同步；部署清单实际保存在项目 GitLab
        仓库。添加或编辑服务时会一次完成保存、提交和回读校验。</a-alert
      >
      <a-alert
        v-if="!delivery.server_key"
        type="warning"
        show-icon
        class="section-gap"
        >请先到“项目接入”选择 GitLab 并创建项目专属仓库。</a-alert
      >
      <a-table
        :data="delivery.services"
        row-key="key"
        :pagination="{ pageSize: 10 }"
        :loading="loading"
      >
        <template #columns>
          <a-table-column title="服务"
            ><template #cell="{ record }"
              ><div class="primary-cell">
                <strong>{{ record.display_name }}</strong>
                <div>
                  <a-tag
                    size="small"
                    :color="
                      record.workload_type === 'frontend'
                        ? 'purple'
                        : 'arcoblue'
                    "
                    >{{
                      record.workload_type === "frontend" ? "前端" : "后端"
                    }}</a-tag
                  >
                  <code>{{ record.key }}</code>
                </div>
              </div></template
            ></a-table-column
          >
          <a-table-column title="源码与构建"
            ><template #cell="{ record }"
              ><div class="primary-cell">
                <span>{{ compactURL(record.source_repository) }}</span
                ><small
                  >{{ record.source_branch }} ·
                  {{ record.language.toUpperCase() }}
                  {{ record.runtime_version }} · Dockerfile:
                  {{
                    record.dockerfile_source === "source"
                      ? "业务仓库"
                      : "平台托管"
                  }}</small
                >
              </div></template
            ></a-table-column
          >
          <a-table-column title="部署参数"
            ><template #cell="{ record }"
              ><div class="primary-cell">
                <span
                  >{{ record.namespace }}:{{ record.container_port }} ·
                  {{ record.replicas }} 副本</span
                ><small>{{ record.image_repository }}</small>
              </div></template
            ></a-table-column
          >
          <a-table-column title="部署清单"
            ><template #cell="{ record }"
              ><div class="primary-cell">
                <code
                  >environments/&lt;env&gt;/{{ record.key }}/manifest.yaml</code
                ><a-tag
                  size="small"
                  :color="
                    record.manifest_mode === 'repository' ? 'orange' : 'green'
                  "
                  >{{
                    record.manifest_mode === "repository"
                      ? "仓库维护 · 平台不覆盖"
                      : "平台生成"
                  }}</a-tag
                >
              </div></template
            ></a-table-column
          >
          <a-table-column title="操作" :width="150"
            ><template #cell="{ record }"
              ><a-space
                ><a-button
                  size="mini"
                  :disabled="!store.canConfigure"
                  @click="openService(record)"
                  >编辑</a-button
                ><a-popconfirm
                  content="只从平台服务目录移除；GitLab 已有文件不会自动删除。被 Job 引用时请先修改 Job。"
                  @ok="removeService(record)"
                  ><a-button
                    size="mini"
                    status="danger"
                    :disabled="!store.canConfigure"
                    >移除</a-button
                  ></a-popconfirm
                ></a-space
              ></template
            ></a-table-column
          >
        </template>
        <template #empty
          ><a-empty description="尚未登记服务；添加后可在 Job 中多选"
        /></template>
      </a-table>
      <div class="actions">
        <span class="external-source"
          >配置真源：GitLab · 仅同步平台托管的 Dockerfile
          和部署清单</span
        ><a-popconfirm
          content="确认同步平台托管的 Dockerfile 与部署清单？仓库维护模式的清单不会被修改。"
          @ok="syncManifests"
          ><a-button
            :disabled="!store.canConfigure || !delivery.server_key"
            :loading="syncing"
            >同步平台托管文件</a-button
          ></a-popconfirm
        >
      </div>
    </a-card>
  </div>

  <a-modal
    v-model:visible="serviceVisible"
    :title="
      editingServiceIndex >= 0 ? '编辑服务并同步清单' : '添加服务并生成清单'
    "
    width="960px"
    ok-text="保存并同步 GitLab"
    :ok-loading="saving || syncing"
    @before-ok="saveService"
  >
    <a-form :model="serviceForm" layout="vertical">
      <a-alert type="success" show-icon class="section-gap"
        >只填写常用参数即可；分支、构建路径、Namespace、资源规格等已有安全默认值。</a-alert
      >
      <a-alert
        v-if="!delivery.source_server_key"
        type="warning"
        show-icon
        class="section-gap"
        >请选择业务源码 GitLab，之后才能从仓库列表选择业务代码。</a-alert
      >
      <a-grid :cols="4" :col-gap="16"
        ><a-grid-item
          ><a-form-item label="服务标识" required
            ><a-input
              v-model="serviceForm.key"
              :disabled="editingServiceIndex >= 0" /></a-form-item></a-grid-item
        ><a-grid-item
          ><a-form-item label="服务名称"
            ><a-input
              v-model="serviceForm.display_name"
              placeholder="默认使用服务标识" /></a-form-item></a-grid-item
        ><a-grid-item
          ><a-form-item label="部署类型" required
            ><a-radio-group
              v-model="serviceForm.workload_type"
              type="button"
              @change="workloadChanged"
              ><a-radio value="backend">后端</a-radio
              ><a-radio value="frontend">前端</a-radio></a-radio-group
            ></a-form-item
          ></a-grid-item
        ><a-grid-item
          ><a-form-item label="语言" required
            ><a-select v-model="serviceForm.language" @change="languageChanged"
              ><a-option value="go">Go</a-option
              ><a-option value="java">Java</a-option
              ><a-option value="node">Node.js / 前端</a-option></a-select
            ></a-form-item
          ></a-grid-item
        ></a-grid
      >
      <div class="source-selector">
        <div class="source-selector-head">
          <div class="source-selector-mark">G</div>
          <div>
            <strong>业务源码仓库</strong
            ><span>从项目绑定 GitLab 的全部授权根组选择代码</span>
          </div>
          <a-tag :color="sourceRepositories.length ? 'green' : 'gray'"
            >{{ sourceRepositories.length }} 个可选仓库</a-tag
          >
        </div>
        <div v-if="selectedSourceRootGroups.length" class="source-scope-bar">
          <span>授权范围</span
          ><a-tag
            v-for="group in selectedSourceRootGroups"
            :key="group"
            color="arcoblue"
            >{{ group }}</a-tag
          >
        </div>
        <a-grid :cols="4" :col-gap="16">
          <a-grid-item
            ><a-form-item
              label="业务源码 GitLab"
              required
              extra="项目级绑定；仓库会从全部授权根组汇总。"
              ><a-select
                :model-value="delivery.source_server_key"
                :disabled="sourceBindingLocked || sourceLoading"
                :loading="sourceLoading"
                placeholder="选择 GitLab 服务器"
                @change="sourceServerChanged"
                ><a-option
                  v-for="server in sourceServers"
                  :key="server.key"
                  :value="server.key"
                  :disabled="server.last_check_status === 'failed'"
                  >{{ server.display_name
                  }}{{
                    server.last_check_status === "failed" ? "（连接异常）" : ""
                  }}</a-option
                ></a-select
              ><template v-if="selectedSourceRootGroups.length" #extra
                >已汇总
                {{ selectedSourceRootGroups.length }} 个授权根组</template
              ></a-form-item
            ></a-grid-item
          >
          <a-grid-item :span="2"
            ><a-form-item
              label="业务源码仓库"
              required
              :extra="sourceRepositorySummary"
              ><a-select
                v-model="serviceForm.source_repository"
                :disabled="!delivery.source_server_key || sourceLoading"
                :loading="sourceLoading"
                allow-search
                placeholder="选择业务源码仓库"
                @change="sourceRepositoryChanged"
                ><a-option
                  v-for="repository in sourceRepositories"
                  :key="repository.project_id"
                  :value="repository.clone_url"
                  ><span class="repository-option"
                    ><a-tag size="small" color="green">{{
                      repository.root_group
                    }}</a-tag
                    ><span class="repository-option-separator">/</span
                    ><span>{{ sourceRepositoryRelativePath(repository) }}</span
                    ><small>· {{ repository.default_branch }}</small></span
                  ></a-option
                ></a-select
              ></a-form-item
            ></a-grid-item
          >
          <a-grid-item
            ><a-form-item label="源码分支" required extra="从所选仓库实时读取。"
              ><a-select
                v-model="serviceForm.source_branch"
                :loading="branchLoading"
                :disabled="!serviceForm.source_repository"
                allow-search
                placeholder="选择源码分支"
                ><a-option
                  v-if="sourceBranchMissing"
                  :value="serviceForm.source_branch"
                  >{{ serviceForm.source_branch }} · 当前配置</a-option
                ><a-option
                  v-for="branch in sourceBranches"
                  :key="branch.name"
                  :value="branch.name"
                  >{{ branch.name }}{{ branch.default ? " · 默认" : ""
                  }}{{ branch.protected ? " · 受保护" : "" }}</a-option
                ></a-select
              ></a-form-item
            ></a-grid-item
          >
        </a-grid>
        <div v-if="delivery.source_server_key" class="credential-hint">
          <a-tag color="green">凭据自动闭环</a-tag
          ><span
            >保存服务后，创建或同步 Job 时自动把项目级只读凭据录入该 Job 使用的
            Jenkins。</span
          >
        </div>
      </div>
      <a-grid :cols="4" :col-gap="16"
        ><a-grid-item :span="3"
          ><a-form-item
            label="ECR 镜像仓库"
            required
            extra="可填写仓库名（如 demo/gateway）或当前项目 AWS 账号下的完整 ECR 地址；保存时自动创建或复用，并回填规范地址。"
            ><a-input
              v-model="serviceForm.image_repository"
              placeholder="项目/服务名" /></a-form-item></a-grid-item
        ><a-grid-item
          ><a-form-item label="服务端口" required
            ><a-input-number
              v-model="serviceForm.container_port"
              :min="1"
              :max="65535" /></a-form-item></a-grid-item
      ></a-grid>
      <a-collapse class="advanced-options" :default-active-key="[]">
        <a-collapse-item key="build" header="高级 · 构建设置"
          ><a-grid :cols="2" :col-gap="16"
            ><a-grid-item
              ><a-form-item label="运行时版本"
                ><a-select v-model="serviceForm.runtime_version" allow-create
                  ><a-option
                    v-for="version in runtimeVersions"
                    :key="version"
                    :value="version"
                    >{{ version }}</a-option
                  ></a-select
                ></a-form-item
              ></a-grid-item
            ><a-grid-item
              ><a-form-item label="源码访问方式"
                ><div
                  v-if="delivery.source_server_key"
                  class="managed-source-access"
                >
                  <a-tag color="green">Jenkins 自动凭据</a-tag
                  ><span>Job 同步时按仓库所属根组自动录入独立只读凭据</span>
                </div>
                <a-select
                  v-else
                  v-model="serviceForm.source_credential_id"
                  allow-clear
                  placeholder="选择 Jenkins 凭据"
                  ><a-option
                    v-for="credential in sourceCredentials"
                    :key="credential.key"
                    :value="credential.external_id"
                    >{{ credential.display_name }} ·
                    {{ credential.external_id }}</a-option
                  ></a-select
                ></a-form-item
              ></a-grid-item
            ></a-grid
          ><a-alert type="info" show-icon class="build-ownership-alert"
            >Jenkins 只负责拉取源码、执行 Dockerfile 和部署清单；依赖安装、编译、运行配置以及
            ENTRYPOINT/CMD 全部由 Dockerfile 完成。默认清单不创建业务 ConfigMap、自定义
            ServiceAccount 或无引用 Volume。</a-alert
          ><div class="dockerfile-settings">
            <div class="dockerfile-settings-head">
              <div>
                <strong>Dockerfile 从哪里读取？</strong>
                <span>这个选择会直接决定 Jenkins 构建时使用的文件。</span>
              </div>
              <a-radio-group
                v-model="serviceForm.dockerfile_source"
                type="button"
                @change="dockerfileSourceChanged"
              >
                <a-radio value="platform">平台集中管理</a-radio>
                <a-radio value="source">业务源码仓库</a-radio>
              </a-radio-group>
            </div>

            <div
              v-if="serviceForm.dockerfile_source === 'source'"
              class="source-dockerfile-box"
            >
              <div class="source-dockerfile-context">
                <span>当前源码</span>
                <strong>{{ selectedSourceRepository?.path || "未选择仓库" }}</strong>
                <a-tag color="arcoblue">{{ serviceForm.source_branch || "未选择分支" }}</a-tag>
              </div>
              <a-form-item label="Dockerfile 位置" required>
                <a-radio-group v-model="sourceDockerfileLocation" type="button">
                  <a-radio value="root">项目根目录（Dockerfile）</a-radio>
                  <a-radio value="custom">自定义相对路径</a-radio>
                </a-radio-group>
              </a-form-item>
              <a-form-item
                v-if="sourceDockerfileLocation === 'custom'"
                label="Dockerfile 相对路径"
                extra="相对于业务源码仓库根目录，例如 deploy/Dockerfile。"
                required
              >
                <a-input
                  v-model="serviceForm.dockerfile"
                  placeholder="deploy/Dockerfile"
                />
              </a-form-item>
              <div class="dockerfile-check-row">
                <a-spin v-if="sourceFileCheckStatus === 'checking'" :size="16" />
                <a-tag
                  v-else
                  :color="sourceFileCheckColor"
                >{{ sourceFileCheckLabel }}</a-tag>
                <span>{{ sourceFileCheckMessage }}</span>
                <a-button
                  size="mini"
                  :loading="sourceFileCheckStatus === 'checking'"
                  @click="checkSourceDockerfile(true)"
                >重新检查</a-button>
              </div>
              <a-alert
                v-if="sourceFileCheckStatus === 'missing'"
                type="error"
                show-icon
              >未找到 Dockerfile，请先向当前业务分支提交该文件，或修改上方路径。</a-alert>
              <a-alert
                v-else-if="sourceFileCheckStatus === 'error'"
                type="warning"
                show-icon
              >暂时无法校验文件，请检查 GitLab 连接和只读 Token。</a-alert>
              <div class="source-dockerfile-note">
                Jenkins 从构建时选定的业务分支读取；平台不会向业务仓库写入或覆盖 Dockerfile。
              </div>
            </div>

            <a-grid :cols="4" :col-gap="16">
              <a-grid-item
                ><a-form-item label="Build Context"
                  ><a-input v-model="serviceForm.build_context" /></a-form-item
              ></a-grid-item>
              <a-grid-item
                v-if="serviceForm.dockerfile_source !== 'source'"
                :span="2"
                ><a-form-item
                  label="Dockerfile 管理路径"
                  extra="与 Jenkinsfile 保存在同一个运维交付仓库，并按当前环境隔离。"
                  ><a-input
                    :model-value="managedDockerfilePath"
                    readonly /></a-form-item
              ></a-grid-item>
              <a-grid-item
                ><a-form-item label="Docker Target"
                  ><a-input v-model="serviceForm.docker_target" /></a-form-item
              ></a-grid-item>
              <a-grid-item
                ><a-form-item label="RUN_ENV"
                  ><a-input
                    v-model="serviceForm.run_environment"
                    placeholder="默认跟随发布环境" /></a-form-item
              ></a-grid-item>
            </a-grid>
          </div>
          <a-form-item
            v-if="serviceForm.dockerfile_source !== 'source'"
            label="Dockerfile（平台集中管理）"
            :extra="`当前编辑 ${store.currentEnvironmentKey || 'dev'} 环境；保存后同步到 ${managedDockerfilePath}。留空时平台按语言生成模板。`"
            ><a-textarea
              v-model="serviceForm.dockerfile_content"
              placeholder="留空由平台自动生成"
              :auto-size="{ minRows: 10, maxRows: 24 }" /></a-form-item
        ></a-collapse-item>
        <a-collapse-item key="kubernetes" header="高级 · Kubernetes 设置"
          ><a-grid :cols="4" :col-gap="16"
            ><a-grid-item
              ><a-form-item
                label="部署清单来源"
                extra="复杂服务建议由运维在 GitLab 仓库维护，平台不会覆盖文件。"
                ><a-select v-model="serviceForm.manifest_mode"
                  ><a-option value="repository"
                    >仓库维护（推荐复杂服务）</a-option
                  ><a-option value="platform">平台生成</a-option></a-select
                ></a-form-item
              ></a-grid-item
            ><a-grid-item
              ><a-form-item
                label="Namespace"
                extra="读取当前项目环境已配置的 Namespace。"
                ><a-select
                  v-model="serviceForm.namespace"
                  allow-search
                  placeholder="选择当前环境 Namespace"
                  ><a-option
                    v-if="namespaceMissing"
                    :value="serviceForm.namespace"
                    >{{ serviceForm.namespace }} · 当前配置</a-option
                  ><a-option
                    v-for="namespace in namespaceOptions"
                    :key="namespace"
                    :value="namespace"
                    >{{ namespace }}</a-option
                  ></a-select
                ></a-form-item
              ></a-grid-item
            ><a-grid-item
              ><a-form-item label="副本数"
                ><a-input-number
                  v-model="serviceForm.replicas"
                  :min="1"
                  :max="50" /></a-form-item></a-grid-item
            ><a-grid-item
              ><a-form-item label="历史版本保留"
                ><a-input-number
                  v-model="serviceForm.revision_history_limit"
                  :min="1"
                  :max="20" /></a-form-item></a-grid-item></a-grid
          ><a-alert
            v-if="workloadSchedulingEnabled && serviceForm.manifest_mode === 'platform'"
            type="success"
            show-icon
            class="section-gap"
            >当前环境启用了节点用途规划；平台生成的业务 Deployment 会自动绑定
            <code>workload-class=application</code>，调度到业务服务节点组。</a-alert
          >
          ><a-alert
            v-if="serviceForm.manifest_mode === 'repository'"
            type="warning"
            show-icon
            class="section-gap"
            >平台仍会同步此服务的
            Dockerfile；现有部署清单由运维仓库负责，保存服务和“重新同步”都不会覆盖。若需要节点隔离，请在仓库清单中自行添加
            <code>nodeSelector</code>。</a-alert
          ><a-grid :cols="3" :col-gap="16"
            ><a-grid-item
              ><a-form-item label="镜像拉取策略"
                ><a-select v-model="serviceForm.image_pull_policy"
                  ><a-option value="Always">Always</a-option
                  ><a-option value="IfNotPresent">IfNotPresent</a-option
                  ><a-option value="Never">Never</a-option></a-select
                ></a-form-item
              ></a-grid-item
            ><a-grid-item
              ><a-form-item label="镜像拉取 Secret"
                ><a-input-tag
                  v-model="
                    serviceForm.image_pull_secrets
                  " /></a-form-item></a-grid-item
            ><a-grid-item
              ><a-form-item label="容器时区"
                ><a-input
                  v-model="
                    serviceForm.timezone
                  " /></a-form-item></a-grid-item></a-grid
          ><a-form-item
            label="非敏感环境变量"
            extra="每行 KEY=VALUE；密码、Token 和私钥会被拒绝，不会写入 GitLab 清单。"
            ><a-textarea
              v-model="environmentVariablesText"
              placeholder="SPRING_PROFILES_ACTIVE=test"
              :auto-size="{ minRows: 3, maxRows: 8 }" /></a-form-item
          ><a-form-item
            label="Secret 环境变量引用"
            extra="每行 ENV_NAME=secret-name/key；只保存 Kubernetes Secret 引用，密文不进入平台数据库、GitLab、Jenkins 参数和日志。"
            ><a-textarea
              v-model="secretEnvironmentVariablesText"
              placeholder="SPRING_DATASOURCE_PASSWORD=mysql-auth/password"
              :auto-size="{ minRows: 2, maxRows: 8 }" /></a-form-item
          ><div
            v-if="
              serviceForm.language === 'java' &&
              serviceForm.workload_type === 'backend' &&
              serviceForm.manifest_mode === 'platform'
            "
            class="java-runtime-settings"
          >
            <a-alert type="info" show-icon class="section-gap"
              >每行作为一个 JVM 参数写入 Deployment <code>args</code>；平台生成
              <code>command: java</code>，并固定追加 <code>-jar app.jar</code>。
              <code v-pre>{{environment}}</code> 会按清单目录自动替换为
              dev/test/uat/prod。</a-alert
            >
            <a-form-item
              label="Java JVM 启动参数"
              extra="每行一个参数；不要填写 YAML 的 args:。"
              ><a-textarea
                v-model="javaOptionsText"
                placeholder="-Dspring.profiles.active={{environment}}&#10;-Xms14g&#10;-Xmx20g"
                :auto-size="{ minRows: 4, maxRows: 12 }"
              /></a-form-item
            >
            <a-alert type="warning" show-icon
              >配置 <code>-Xmx</code> 后，Memory Limit 必须大于堆上限，还要为
              Metaspace、线程栈和堆外内存保留空间。</a-alert
            >
          </div>
          <a-form-item
            v-if="serviceForm.workload_type === 'frontend'"
            label="HTTP 健康检查路径"
            ><a-input v-model="serviceForm.health_path" /></a-form-item
        ></a-collapse-item>
        <a-collapse-item
          v-if="serviceForm.workload_type === 'backend'"
          key="etcd"
          header="可选 · etcd 配置 Secret"
          ><a-form-item label="生成 etcd 配置"
            ><a-switch v-model="serviceForm.etcd_config_enabled" /></a-form-item
          ><template v-if="serviceForm.etcd_config_enabled"
            ><a-alert type="warning" show-icon class="section-gap"
              >GitLab 只保存密码占位符，部署时从 Jenkins Secret Text Credential
              临时渲染。</a-alert
            ><a-form-item label="etcd Hosts" required
              ><a-input-tag v-model="serviceForm.etcd_hosts" /></a-form-item
            ><a-grid :cols="3" :col-gap="16"
              ><a-grid-item
                ><a-form-item label="配置 Key" required
                  ><a-input
                    v-model="
                      serviceForm.etcd_config_key
                    " /></a-form-item></a-grid-item
              ><a-grid-item
                ><a-form-item label="用户名" required
                  ><a-input
                    v-model="
                      serviceForm.etcd_username
                    " /></a-form-item></a-grid-item
              ><a-grid-item
                ><a-form-item label="密码 Credential ID" required
                  ><a-input
                    v-model="
                      serviceForm.etcd_password_credential_id
                    " /></a-form-item></a-grid-item></a-grid
            ><a-grid :cols="2" :col-gap="16"
              ><a-grid-item
                ><a-form-item label="配置文件名" required
                  ><a-input
                    v-model="
                      serviceForm.etcd_config_file
                    " /></a-form-item></a-grid-item
              ><a-grid-item
                ><a-form-item label="容器挂载路径" required
                  ><a-input
                    v-model="
                      serviceForm.etcd_mount_path
                    " /></a-form-item></a-grid-item></a-grid></template
        ></a-collapse-item>
        <a-collapse-item key="resources" header="高级 · 容器资源规格"
          ><a-grid :cols="4" :col-gap="16"
            ><a-grid-item
              ><a-form-item label="CPU Request"
                ><a-input
                  v-model="
                    serviceForm.cpu_request
                  " /></a-form-item></a-grid-item
            ><a-grid-item
              ><a-form-item label="Memory Request"
                ><a-input
                  v-model="
                    serviceForm.memory_request
                  " /></a-form-item></a-grid-item
            ><a-grid-item
              ><a-form-item label="CPU Limit"
                ><a-input
                  v-model="serviceForm.cpu_limit" /></a-form-item></a-grid-item
            ><a-grid-item
              ><a-form-item label="Memory Limit"
                ><a-input
                  v-model="
                    serviceForm.memory_limit
                  " /></a-form-item></a-grid-item></a-grid
        ></a-collapse-item>
      </a-collapse>
    </a-form>
  </a-modal>
</template>

<script setup lang="ts">
import { computed, reactive, ref, watch } from "vue";
import { Message } from "@arco-design/web-vue";
import { IconPlus, IconRefresh } from "@arco-design/web-vue/es/icon";
import { api } from "@/services/api";
import { usePlatformStore } from "@/stores/platform";

interface ServiceSpec {
  key: string;
  display_name: string;
  workload_type: "backend" | "frontend";
  language: "go" | "java" | "node";
  runtime_version: string;
  source_repository: string;
  source_branch: string;
  source_credential_id: string;
  manifest_credential_id: string;
  build_command: string;
  build_context: string;
  dockerfile_source: "platform" | "source";
  dockerfile: string;
  dockerfile_content: string;
  dockerfile_contents?: Record<string, string>;
  manifest_mode: "platform" | "repository";
  docker_target: string;
  run_environment: string;
  image_repository: string;
  image_pull_secrets: string[];
  image_pull_policy: "Always" | "IfNotPresent" | "Never";
  namespace: string;
  workload_class: "" | "application" | "platform" | "stateful" | "general";
  container_port: number;
  replicas: number;
  revision_history_limit: number;
  timezone: string;
  java_options: string[];
  environment_variables: Record<string, string>;
  secret_environment_variables: Record<
    string,
    { secret_name: string; secret_key: string }
  >;
  cpu_request: string;
  memory_request: string;
  cpu_limit: string;
  memory_limit: string;
  health_path: string;
  etcd_config_enabled: boolean;
  etcd_hosts: string[];
  etcd_config_key: string;
  etcd_username: string;
  etcd_password_credential_id: string;
  etcd_config_file: string;
  etcd_mount_path: string;
  nginx_server_config: string;
}
interface Delivery {
  project_key: string;
  server_key: string;
  root_group: string;
  source_server_key: string;
  source_root_group: string;
  services: ServiceSpec[];
}
interface Credential {
  key: string;
  display_name: string;
  kind: string;
  external_id: string;
  sync_status: string;
}
interface SourceRepository {
  project_id: number;
  name: string;
  path: string;
  root_group: string;
  clone_url: string;
  default_branch: string;
  source_server_key: string;
}
interface SourceBranch {
  name: string;
  default: boolean;
  protected: boolean;
}
interface SourceFileCheck {
  project_id: number;
  branch: string;
  path: string;
  exists: boolean;
}
interface ECRRepository {
  name: string;
  uri: string;
  region: string;
  created: boolean;
}
interface GeneratedJobReference {
  key: string;
  jenkinsfile_mode: "generated" | "existing";
  service_keys: string[];
}
interface GitLabServer {
  key: string;
  display_name: string;
  base_url: string;
  root_group: string;
  root_groups?: string[];
  configured: boolean;
  last_check_status?: string;
}
const emit = defineEmits<{ changed: [] }>();
const store = usePlatformStore();
const loading = ref(false);
const saving = ref(false);
const syncing = ref(false);
const serviceVisible = ref(false);
const editingServiceIndex = ref(-1);
const credentials = ref<Credential[]>([]);
const sourceCredentials = computed(() =>
  credentials.value.filter(
    (item) =>
      item.sync_status === "ready" &&
      ["gitlab_token", "username_password"].includes(item.kind),
  ),
);
const sourceRepositories = ref<SourceRepository[]>([]);
const sourceServers = ref<GitLabServer[]>([]);
const sourceLoading = ref(false);
const sourceBranches = ref<SourceBranch[]>([]);
const branchLoading = ref(false);
type SourceFileCheckStatus =
  | "idle"
  | "checking"
  | "found"
  | "missing"
  | "error";
const sourceFileCheckStatus = ref<SourceFileCheckStatus>("idle");
const sourceFileCheckMessage = ref("选择源码仓库和分支后自动检查。");
let sourceFileCheckSequence = 0;
let sourceFileCheckTimer: number | undefined;
const sourceBindingLocked = computed(() =>
  Boolean(delivery.source_server_key && delivery.services.length),
);
const emptyDelivery = (): Delivery => ({
  project_key: store.currentProjectKey,
  server_key: "",
  root_group: "",
  source_server_key: "",
  source_root_group: "",
  services: [],
});
const delivery = reactive<Delivery>(emptyDelivery());
const namespaceOptions = computed(() =>
  Object.keys((store.config?.namespaces as Record<string, unknown>) || {})
    .filter(Boolean)
    .sort(),
);
const workloadSchedulingEnabled = computed(() =>
  Boolean((store.config?.eks as any)?.workload_scheduling?.enabled),
);
const emptyService = (): ServiceSpec => ({
  key: "",
  display_name: "",
  workload_type: "backend",
  language: "go",
  runtime_version: "1.24",
  source_repository: "",
  source_branch: "main",
  source_credential_id: "",
  manifest_credential_id: "",
  build_command: "",
  build_context: ".",
  dockerfile_source: "platform",
  dockerfile: "Dockerfile",
  dockerfile_content: "",
  dockerfile_contents: {},
  manifest_mode: "platform",
  docker_target: "",
  run_environment: "",
  image_repository: "",
  image_pull_secrets: [],
  image_pull_policy: "Always",
  namespace: namespaceOptions.value[0] || store.currentProjectKey || "",
  workload_class: workloadSchedulingEnabled.value ? "application" : "",
  container_port: 8080,
  replicas: 1,
  revision_history_limit: 5,
  timezone: "Asia/Shanghai",
  java_options: [],
  environment_variables: {},
  secret_environment_variables: {},
  cpu_request: "100m",
  memory_request: "128Mi",
  cpu_limit: "1",
  memory_limit: "512Mi",
  health_path: "",
  etcd_config_enabled: false,
  etcd_hosts: [],
  etcd_config_key: "",
  etcd_username: "admin",
  etcd_password_credential_id: "",
  etcd_config_file: "config.yaml",
  etcd_mount_path: "/app/apps/api/etc/config.yaml",
  nginx_server_config: "",
});
const serviceForm = reactive<ServiceSpec>(emptyService());
const javaOptionsText = ref("");
const environmentVariablesText = ref("");
const secretEnvironmentVariablesText = ref("");
const managedDockerfilePath = computed(
  () =>
    `environments/${store.currentEnvironmentKey || "dev"}/dockerfiles/${serviceForm.key.trim().toLowerCase() || "<服务标识>"}/Dockerfile`,
);
const selectedSourceRepository = computed(() =>
  sourceRepositories.value.find(
    (item) => item.clone_url === serviceForm.source_repository,
  ),
);
const sourceDockerfileLocation = computed({
  get: () =>
    serviceForm.dockerfile.trim() === "" ||
    serviceForm.dockerfile.trim() === "Dockerfile"
      ? "root"
      : "custom",
  set: (value: string) => {
    serviceForm.dockerfile =
      value === "root" ? "Dockerfile" : "deploy/Dockerfile";
  },
});
const sourceFileCheckLabel = computed(() => {
  switch (sourceFileCheckStatus.value) {
    case "checking":
      return "检查中";
    case "found":
      return "已找到";
    case "missing":
      return "未找到";
    case "error":
      return "校验失败";
    default:
      return "待检查";
  }
});
const sourceFileCheckColor = computed(() => {
  switch (sourceFileCheckStatus.value) {
    case "found":
      return "green";
    case "missing":
      return "red";
    case "error":
      return "orange";
    default:
      return "gray";
  }
});
const namespaceMissing = computed(() =>
  Boolean(
    serviceForm.namespace &&
    !namespaceOptions.value.includes(serviceForm.namespace),
  ),
);
const sourceBranchMissing = computed(() =>
  Boolean(
    serviceForm.source_branch &&
    !sourceBranches.value.some(
      (item) => item.name === serviceForm.source_branch,
    ),
  ),
);
const runtimeVersions = computed(() =>
  serviceForm.language === "java"
    ? ["21", "17", "11"]
    : serviceForm.language === "node"
      ? ["22", "20", "18"]
      : ["1.24", "1.23", "1.22"],
);
const selectedSourceServer = computed(() =>
  sourceServers.value.find((item) => item.key === delivery.source_server_key),
);
const selectedSourceRootGroups = computed(() =>
  selectedSourceServer.value?.root_groups?.length
    ? selectedSourceServer.value.root_groups
    : ([selectedSourceServer.value?.root_group].filter(Boolean) as string[]),
);
const sourceRepositorySummary = computed(() =>
  delivery.source_server_key
    ? `已汇总 ${selectedSourceRootGroups.value.length} 个根组、${sourceRepositories.value.length} 个仓库；平台只读，不修改业务代码。`
    : "请先选择业务源码 GitLab。",
);
function sourceRepositoryRelativePath(repository: SourceRepository) {
  const rootGroup = repository.root_group.trim().replace(/^\/+|\/+$/g, "");
  const repositoryPath = repository.path.trim().replace(/^\/+|\/+$/g, "");
  if (rootGroup && repositoryPath.startsWith(`${rootGroup}/`)) {
    return repositoryPath.slice(rootGroup.length + 1);
  }
  return repositoryPath;
}
const scope = () =>
  `/api/projects/${encodeURIComponent(store.currentProjectKey)}/cicd`;
async function load() {
  if (!store.currentProjectKey) {
    Object.assign(delivery, emptyDelivery());
    credentials.value = [];
    sourceRepositories.value = [];
    sourceServers.value = [];
    return;
  }
  const revision = store.scopeRevision;
  loading.value = true;
  try {
    const [item, credentialResult, serverCatalog] = await Promise.all([
      api<Delivery>(`${scope()}/delivery`),
      api<{ credentials: Credential[] }>(`${scope()}/credentials`),
      api<GitLabServer[]>(`${scope()}/gitlab-servers`),
    ]);
    if (revision !== store.scopeRevision) return;
    Object.assign(delivery, emptyDelivery(), item, {
      services: item.services || [],
    });
    credentials.value = credentialResult.credentials || [];
    sourceServers.value = serverCatalog.filter((server) => server.configured);
    sourceRepositories.value = item.source_server_key
      ? (
          await api<{ repositories: SourceRepository[] }>(
            `${scope()}/source-repositories`,
          )
        ).repositories || []
      : [];
  } catch (error: any) {
    if (revision === store.scopeRevision) Message.error(error.message);
  } finally {
    if (revision === store.scopeRevision) loading.value = false;
  }
}
watch(
  () => store.scopeRevision,
  () => void load(),
  { immediate: true },
);
async function persist(notify = false) {
  if (
    !delivery.server_key ||
    !delivery.root_group ||
    !delivery.source_server_key ||
    !delivery.source_root_group
  ) {
    Message.warning("请先完成交付 GitLab、业务源码 GitLab及授权根组接入");
    return false;
  }
  saving.value = true;
  try {
    const saved = await api<Delivery>(`${scope()}/delivery`, {
      method: "PUT",
      body: JSON.stringify({
        server_key: delivery.server_key,
        root_group: delivery.root_group,
        source_server_key: delivery.source_server_key,
        source_root_group: delivery.source_root_group,
        services: delivery.services,
      }),
    });
    Object.assign(delivery, saved, { services: saved.services || [] });
    emit("changed");
    if (notify) Message.success("服务目录已保存");
    return true;
  } catch (error: any) {
    Message.error(error.message);
    return false;
  } finally {
    saving.value = false;
  }
}
function openService(item?: ServiceSpec) {
  editingServiceIndex.value = item
    ? delivery.services.findIndex((candidate) => candidate.key === item.key)
    : -1;
  Object.assign(serviceForm, emptyService(), item || {});
  const environment = store.currentEnvironmentKey || "dev";
  if (serviceForm.dockerfile_source !== "source") {
    const baseline = serviceForm.dockerfile_content || "";
    const contents = { ...(serviceForm.dockerfile_contents || {}) };
    for (const key of ["dev", "test", "uat", "prod"]) {
      if (!contents[key] && baseline) contents[key] = baseline;
    }
    serviceForm.dockerfile_contents = contents;
    serviceForm.dockerfile_content = contents[environment] || baseline;
  }
  serviceForm.manifest_mode = serviceForm.manifest_mode || "platform";
  if (workloadSchedulingEnabled.value && !serviceForm.workload_class)
    serviceForm.workload_class = "application";
  serviceForm.dockerfile_source = serviceForm.dockerfile_source || "platform";
  if (!serviceForm.dockerfile)
    serviceForm.dockerfile =
      serviceForm.dockerfile_source === "source"
        ? "Dockerfile"
        : managedDockerfilePath.value;
  javaOptionsText.value = (serviceForm.java_options || []).join("\n");
  environmentVariablesText.value = Object.entries(
    serviceForm.environment_variables || {},
  )
    .sort(([a], [b]) => a.localeCompare(b))
    .map(([key, value]) => `${key}=${value}`)
    .join("\n");
  secretEnvironmentVariablesText.value = Object.entries(
    serviceForm.secret_environment_variables || {},
  )
    .sort(([a], [b]) => a.localeCompare(b))
    .map(
      ([key, reference]) =>
        `${key}=${reference.secret_name}/${reference.secret_key}`,
    )
    .join("\n");
  sourceBranches.value = [];
  sourceFileCheckStatus.value = "idle";
  sourceFileCheckMessage.value = "选择源码仓库和分支后自动检查。";
  serviceVisible.value = true;
  if (serviceForm.source_repository)
    void loadSourceBranches(serviceForm.source_repository, false);
}
function dockerfileSourceChanged(value: any) {
  if (String(value) === "source") {
    if (
      !serviceForm.dockerfile.trim() ||
      serviceForm.dockerfile === managedDockerfilePath.value ||
      /^dockerfiles\/[^/]+\/Dockerfile$/.test(serviceForm.dockerfile)
    ) {
      serviceForm.dockerfile = "Dockerfile";
    }
    return;
  }
  serviceForm.dockerfile = managedDockerfilePath.value;
  sourceFileCheckStatus.value = "idle";
  sourceFileCheckMessage.value = "当前由平台集中管理 Dockerfile。";
}
async function checkSourceDockerfile(notify = false) {
  if (serviceForm.dockerfile_source !== "source") return true;
  const repository = selectedSourceRepository.value;
  const branch = serviceForm.source_branch.trim();
  const path = serviceForm.dockerfile.trim() || "Dockerfile";
  if (!repository || !branch || !path) {
    sourceFileCheckStatus.value = "idle";
    sourceFileCheckMessage.value = "请先选择源码仓库、分支和 Dockerfile 位置。";
    if (notify) Message.warning(sourceFileCheckMessage.value);
    return false;
  }
  const sequence = ++sourceFileCheckSequence;
  sourceFileCheckStatus.value = "checking";
  sourceFileCheckMessage.value = `正在检查 ${repository.path}@${branch}:${path}`;
  try {
    const query = new URLSearchParams({ branch, path });
    const result = await api<SourceFileCheck>(
      `${scope()}/source-repositories/${repository.project_id}/files/check?${query.toString()}`,
    );
    if (sequence !== sourceFileCheckSequence) return false;
    sourceFileCheckStatus.value = result.exists ? "found" : "missing";
    sourceFileCheckMessage.value = !result.exists
      ? `未找到 ${result.path}`
      : `已找到 ${result.path}`;
    if (notify) {
      if (result.exists) {
        Message.success("已在当前源码分支找到 Dockerfile");
      } else {
        Message.warning(`未找到 ${result.path}`);
      }
    }
    return result.exists;
  } catch (error: any) {
    if (sequence !== sourceFileCheckSequence) return false;
    sourceFileCheckStatus.value = "error";
    sourceFileCheckMessage.value = `校验失败：${error.message}`;
    if (notify) Message.error(sourceFileCheckMessage.value);
    return false;
  }
}
function scheduleSourceDockerfileCheck() {
  window.clearTimeout(sourceFileCheckTimer);
  if (
    !serviceVisible.value ||
    serviceForm.dockerfile_source !== "source"
  ) {
    return;
  }
  sourceFileCheckTimer = window.setTimeout(
    () => void checkSourceDockerfile(false),
    350,
  );
}
watch(
  () => [
    serviceVisible.value,
    serviceForm.dockerfile_source,
    serviceForm.source_repository,
    serviceForm.source_branch,
    serviceForm.dockerfile,
  ],
  scheduleSourceDockerfileCheck,
);
function languageChanged(value: any) {
  const language = String(value);
  serviceForm.runtime_version =
    language === "java" ? "21" : language === "node" ? "20" : "1.24";
  if (language !== "java") {
    serviceForm.java_options = [];
    javaOptionsText.value = "";
  }
}
function workloadChanged(value: any) {
  if (String(value) === "frontend") {
    serviceForm.language = "node";
    serviceForm.runtime_version = "20";
    serviceForm.container_port = 8080;
    serviceForm.health_path = "/healthz";
    serviceForm.cpu_request = "50m";
    serviceForm.memory_request = "64Mi";
    serviceForm.cpu_limit = "500m";
    serviceForm.memory_limit = "256Mi";
    serviceForm.etcd_config_enabled = false;
    serviceForm.java_options = [];
    javaOptionsText.value = "";
  } else {
    serviceForm.language = "go";
    serviceForm.runtime_version = "1.24";
    serviceForm.container_port = 8080;
    serviceForm.health_path = "";
    serviceForm.cpu_request = "100m";
    serviceForm.memory_request = "128Mi";
    serviceForm.cpu_limit = "1";
    serviceForm.memory_limit = "512Mi";
    serviceForm.java_options = [];
    javaOptionsText.value = "";
  }
}
async function sourceServerChanged(value: any) {
  const next = String(value || "").trim();
  if (!next || next === delivery.source_server_key) return;
  if (!delivery.server_key) {
    Message.warning("请先在“项目接入”创建交付仓库");
    return;
  }
  const server = sourceServers.value.find((item) => item.key === next);
  const nextRoot = server?.root_groups?.[0] || server?.root_group || "";
  const previous = delivery.source_server_key;
  const previousRoot = delivery.source_root_group;
  sourceLoading.value = true;
  try {
    const saved = await api<Delivery>(`${scope()}/delivery`, {
      method: "PUT",
      body: JSON.stringify({
        server_key: delivery.server_key,
        root_group: delivery.root_group,
        source_server_key: next,
        source_root_group: nextRoot,
        services: delivery.services,
      }),
    });
    Object.assign(delivery, saved, { services: saved.services || [] });
    sourceRepositories.value =
      (
        await api<{ repositories: SourceRepository[] }>(
          `${scope()}/source-repositories`,
        )
      ).repositories || [];
    serviceForm.source_repository = "";
    sourceBranches.value = [];
    emit("changed");
    const groupCount = server?.root_groups?.length || 1;
    Message.success(
      `业务源码 GitLab 已绑定，已汇总 ${groupCount} 个授权根组、${sourceRepositories.value.length} 个仓库`,
    );
  } catch (error: any) {
    delivery.source_server_key = previous;
    delivery.source_root_group = previousRoot;
    Message.error(error.message);
  } finally {
    sourceLoading.value = false;
  }
}
function inferServiceIdentity() {
  if (serviceForm.key) return;
  const name =
    serviceForm.source_repository
      .trim()
      .replace(/\/$/, "")
      .split("/")
      .pop()
      ?.replace(/\.git$/, "") || "";
  serviceForm.key = name
    .toLowerCase()
    .replace(/[^a-z0-9-]+/g, "-")
    .replace(/^-+|-+$/g, "")
    .slice(0, 63);
  if (!serviceForm.display_name) serviceForm.display_name = name;
}
async function loadSourceBranches(value: string, selectDefault = true) {
  const repository = sourceRepositories.value.find(
    (item) => item.clone_url === String(value),
  );
  sourceBranches.value = [];
  if (!repository) return;
  branchLoading.value = true;
  try {
    const result = await api<{ branches: SourceBranch[] }>(
      `${scope()}/source-repositories/${repository.project_id}/branches`,
    );
    sourceBranches.value = result.branches || [];
    if (selectDefault) {
      const preferred =
        sourceBranches.value.find((item) => item.default)?.name ||
        repository.default_branch ||
        sourceBranches.value[0]?.name ||
        "main";
      serviceForm.source_branch = preferred;
    }
  } catch (error: any) {
    Message.error(`读取源码分支失败：${error.message}`);
  } finally {
    branchLoading.value = false;
  }
}
function sourceRepositoryChanged(value: any) {
  const repository = sourceRepositories.value.find(
    (item) => item.clone_url === String(value),
  );
  if (repository) {
    serviceForm.source_branch = repository.default_branch || "main";
    inferServiceIdentity();
    void loadSourceBranches(repository.clone_url, true);
  }
}
async function ensureECRRepository() {
  const region = String(
    store.config?.region || store.currentEnvironment?.region || "",
  ).trim();
  if (!region) throw new Error("当前环境未配置 AWS Region，无法创建 ECR 仓库");
  const result = await api<ECRRepository>(`${scope()}/ecr/repositories`, {
    method: "POST",
    body: JSON.stringify({ region, repository: serviceForm.image_repository }),
  });
  serviceForm.image_repository = result.uri;
  return result;
}
async function saveService() {
  inferServiceIdentity();
  serviceForm.key = serviceForm.key.trim().toLowerCase();
  serviceForm.display_name = serviceForm.display_name.trim() || serviceForm.key;
  serviceForm.source_repository = serviceForm.source_repository.trim();
  serviceForm.source_branch = serviceForm.source_branch.trim();
  serviceForm.build_command = "";
  serviceForm.nginx_server_config = "";
  serviceForm.image_repository = serviceForm.image_repository.trim();
  serviceForm.namespace = serviceForm.namespace.trim();
  serviceForm.java_options = javaOptionsText.value
    .split(/\r?\n/)
    .map((item) => item.trim())
    .filter(Boolean);
  serviceForm.dockerfile =
    serviceForm.dockerfile_source === "source"
      ? serviceForm.dockerfile.trim() || "Dockerfile"
      : managedDockerfilePath.value;
  if (serviceForm.dockerfile_source === "platform") {
    const environment = store.currentEnvironmentKey || "dev";
    const contents = { ...(serviceForm.dockerfile_contents || {}) };
    if (serviceForm.dockerfile_content.trim())
      contents[environment] = serviceForm.dockerfile_content;
    else delete contents[environment];
    serviceForm.dockerfile_contents = contents;
  }
  const variables: Record<string, string> = {};
  for (const raw of environmentVariablesText.value.split(/\r?\n/)) {
    const line = raw.trim();
    if (!line) continue;
    const separator = line.indexOf("=");
    if (separator < 1) {
      Message.warning(`环境变量格式不正确：${line}`);
      return false;
    }
    const key = line.slice(0, separator).trim();
    if (
      !/^[A-Za-z_][A-Za-z0-9_]{0,127}$/.test(key) ||
      /(PASSWORD|PASSWD|SECRET|TOKEN|PRIVATE_KEY|ACCESS_KEY)/i.test(key)
    ) {
      Message.warning(`环境变量 ${key} 不合法或可能包含敏感信息`);
      return false;
    }
    if (Object.prototype.hasOwnProperty.call(variables, key)) {
      Message.warning(`环境变量 ${key} 重复`);
      return false;
    }
    variables[key] = line.slice(separator + 1);
  }
  serviceForm.environment_variables = variables;
  const secretVariables: Record<
    string,
    { secret_name: string; secret_key: string }
  > = {};
  for (const raw of secretEnvironmentVariablesText.value.split(/\r?\n/)) {
    const line = raw.trim();
    if (!line) continue;
    const separator = line.indexOf("=");
    const referenceSeparator = line.lastIndexOf("/");
    if (separator < 1 || referenceSeparator <= separator + 1) {
      Message.warning(`Secret 环境变量格式不正确：${line}`);
      return false;
    }
    const key = line.slice(0, separator).trim();
    const secretName = line.slice(separator + 1, referenceSeparator).trim();
    const secretKey = line.slice(referenceSeparator + 1).trim();
    if (
      !/^[A-Za-z_][A-Za-z0-9_]{0,127}$/.test(key) ||
      !/^[a-z0-9][a-z0-9-]{0,62}$/.test(secretName) ||
      !/^[A-Za-z0-9][A-Za-z0-9._-]{0,252}$/.test(secretKey)
    ) {
      Message.warning(`Secret 环境变量 ${key || line} 不正确`);
      return false;
    }
    if (
      Object.prototype.hasOwnProperty.call(variables, key) ||
      Object.prototype.hasOwnProperty.call(secretVariables, key)
    ) {
      Message.warning(`环境变量 ${key} 重复`);
      return false;
    }
    secretVariables[key] = {
      secret_name: secretName,
      secret_key: secretKey,
    };
  }
  serviceForm.secret_environment_variables = secretVariables;
  if (
    !serviceForm.key ||
    !serviceForm.source_repository ||
    !serviceForm.source_branch ||
    !serviceForm.image_repository ||
    !serviceForm.namespace
  ) {
    Message.warning(
      "请填写服务标识并选择源码仓库、源码分支、ECR 镜像仓库和 Namespace",
    );
    return false;
  }
  if (
    serviceForm.dockerfile_source === "source" &&
    !(await checkSourceDockerfile(true))
  ) {
    return false;
  }
  if (
    serviceForm.etcd_config_enabled &&
    (!serviceForm.etcd_password_credential_id.trim() ||
      !serviceForm.etcd_config_file.trim() ||
      !serviceForm.etcd_mount_path.trim())
  ) {
    Message.warning("请补全 etcd 密码凭据和配置文件信息");
    return false;
  }
  if (
    delivery.services.some(
      (item, index) =>
        item.key === serviceForm.key && index !== editingServiceIndex.value,
    )
  ) {
    Message.warning("服务标识不能重复");
    return false;
  }
  saving.value = true;
  let ecr: ECRRepository;
  try {
    ecr = await ensureECRRepository();
  } catch (error: any) {
    Message.error(`ECR 仓库准备失败：${error.message}`);
    saving.value = false;
    return false;
  } finally {
    if (saving.value) saving.value = false;
  }
  const item = JSON.parse(JSON.stringify(serviceForm));
  if (editingServiceIndex.value >= 0)
    delivery.services.splice(editingServiceIndex.value, 1, item);
  else delivery.services.push(item);
  delivery.services.sort((a, b) => a.key.localeCompare(b.key));
  if (!(await persist(false))) return false;
  syncing.value = true;
  try {
    const result = await api<{
      created_files: number;
      updated_files: number;
      deleted_files: number;
    }>(`${scope()}/delivery/provision`, { method: "POST" });
    const syncedJobs = await syncGeneratedJobsForService(serviceForm.key);
    emit("changed");
    Message.success(
      `${ecr.created ? "ECR 仓库已创建" : "ECR 仓库已复用"}；交付文件已同步：新建 ${result.created_files}，更新 ${result.updated_files}，清理 ${result.deleted_files || 0}；已更新 ${syncedJobs} 个关联 Jenkins Job`,
    );
    return true;
  } catch (error: any) {
    Message.error(
      `服务映射已保存，但交付仓库或关联 Jenkins Job 同步失败：${error.message}`,
    );
    return false;
  } finally {
    syncing.value = false;
  }
}
async function syncGeneratedJobsForService(serviceKey: string) {
  const result = await api<{ jobs: GeneratedJobReference[] }>(
    `${scope()}/jobs`,
  );
  const affected = (result.jobs || []).filter(
    (job) =>
      job.jenkinsfile_mode === "generated" &&
      (job.service_keys || []).includes(serviceKey),
  );
  for (const job of affected) {
    await api(`${scope()}/jobs/${encodeURIComponent(job.key)}/sync`, {
      method: "POST",
    });
  }
  return affected.length;
}
async function removeService(item: ServiceSpec) {
  const previous = [...delivery.services];
  delivery.services = delivery.services.filter(
    (candidate) => candidate.key !== item.key,
  );
  if (await persist(false)) {
    Message.success("服务映射已移除；GitLab 历史清单按安全策略保留");
  } else {
    delivery.services = previous;
  }
}
async function syncManifests() {
  if (!(await persist(false))) return;
  syncing.value = true;
  try {
    const result = await api<{
      created_files: number;
      updated_files: number;
      deleted_files: number;
    }>(`${scope()}/delivery/provision`, { method: "POST" });
    Message.success(
      `平台托管文件已同步：新建 ${result.created_files}，更新 ${result.updated_files}，清理旧文件 ${result.deleted_files || 0}`,
    );
    emit("changed");
  } catch (error: any) {
    Message.error(error.message);
  } finally {
    syncing.value = false;
  }
}
const compactURL = (value: string) =>
  value?.replace(/^https?:\/\//, "").replace(/\.git$/, "") || "—";
</script>

<style scoped>
.service-catalog {
  display: flex;
  flex-direction: column;
  gap: 16px;
}
.section-gap {
  margin-bottom: 16px;
}
.card-title {
  font-weight: 600;
}
.primary-cell {
  display: flex;
  flex-direction: column;
  align-items: flex-start;
  gap: 4px;
  min-width: 150px;
}
.primary-cell span,
.primary-cell small {
  max-width: 360px;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.primary-cell small,
.external-source {
  color: #86909c;
}
.external-source {
  font-size: 12px;
}
.actions {
  display: flex;
  justify-content: space-between;
  align-items: center;
  gap: 16px;
  margin-top: 18px;
}
.source-selector {
  margin-bottom: 16px;
  overflow: hidden;
  border: 1px solid #badbc5;
  border-radius: 12px;
  background: #fbfffc;
  box-shadow: 0 8px 24px rgb(0 180 42 / 6%);
}
.source-selector-head {
  min-height: 60px;
  padding: 10px 15px;
  display: grid;
  grid-template-columns: auto minmax(0, 1fr) auto;
  align-items: center;
  gap: 11px;
  border-bottom: 1px solid #d9eee0;
  background: linear-gradient(90deg, #effcf3, #fff);
}
.source-selector-head > div:nth-child(2) {
  min-width: 0;
}
.source-selector-head strong,
.source-selector-head span {
  display: block;
}
.source-selector-head strong {
  font-size: 13px;
}
.source-selector-head span {
  margin-top: 3px;
  color: #86909c;
  font-size: 11px;
}
.source-selector-mark {
  width: 34px;
  height: 34px;
  display: grid;
  place-items: center;
  border-radius: 9px;
  color: #fff;
  background: linear-gradient(145deg, #00b42a, #008f22);
  font-weight: 800;
  box-shadow: 0 5px 12px rgb(0 180 42 / 20%);
}
.source-scope-bar {
  padding: 9px 15px;
  display: flex;
  align-items: center;
  flex-wrap: wrap;
  gap: 7px;
  border-bottom: 1px dashed #d9eee0;
  background: #f8fffa;
}
.source-scope-bar > span {
  margin-right: 2px;
  color: #4e5969;
  font-size: 11px;
  font-weight: 600;
}
.source-selector > .arco-grid {
  padding: 14px 15px 8px;
}
.repository-option {
  width: 100%;
  display: flex;
  align-items: center;
  gap: 7px;
}
.repository-option > span:nth-child(2) {
  min-width: 0;
  flex: 1;
  overflow: hidden;
  text-overflow: ellipsis;
}
.repository-option small {
  color: #86909c;
}
.credential-hint {
  padding: 0 15px 12px;
  display: flex;
  align-items: center;
  gap: 8px;
  color: #4e5969;
  font-size: 12px;
}
.advanced-options {
  margin-top: 8px;
}
.java-runtime-settings {
  margin: 10px 0 16px;
  padding: 16px;
  border: 1px solid #ffd591;
  border-radius: 12px;
  background: linear-gradient(135deg, #fffaf0, #fff);
  box-shadow: 0 8px 24px rgb(255 125 0 / 6%);
}
.java-runtime-settings > :deep(.arco-alert:last-child) {
  margin-top: 4px;
}
.managed-source-access {
  display: flex;
  align-items: center;
  gap: 8px;
  min-height: 32px;
  color: #4e5969;
  font-size: 12px;
}
.dockerfile-settings {
  margin: 8px 0 16px;
  overflow: hidden;
  border: 1px solid #bed1f7;
  border-radius: 12px;
  background: #fbfdff;
}
.dockerfile-settings-head {
  min-height: 64px;
  padding: 12px 16px;
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 16px;
  border-bottom: 1px solid #dce7fb;
  background: linear-gradient(90deg, #eef5ff, #fff);
}
.dockerfile-settings-head strong,
.dockerfile-settings-head span {
  display: block;
}
.dockerfile-settings-head strong {
  font-size: 14px;
}
.dockerfile-settings-head span {
  margin-top: 3px;
  color: #86909c;
  font-size: 12px;
}
.dockerfile-settings > .arco-grid {
  padding: 14px 16px 0;
}
.source-dockerfile-box {
  padding: 14px 16px;
  border-bottom: 1px solid #dce7fb;
  background: #fff;
}
.source-dockerfile-context,
.dockerfile-check-row {
  display: flex;
  align-items: center;
  flex-wrap: wrap;
  gap: 8px;
}
.source-dockerfile-context {
  margin-bottom: 14px;
}
.source-dockerfile-context > span,
.dockerfile-check-row > span {
  color: #4e5969;
  font-size: 12px;
}
.dockerfile-check-row {
  min-height: 32px;
  margin-bottom: 10px;
}
.dockerfile-check-row > span:nth-child(2) {
  flex: 1;
}
.source-dockerfile-note {
  margin-top: 10px;
  color: #86909c;
  font-size: 12px;
  line-height: 1.6;
}
code {
  font-size: 12px;
  color: #165dff;
}
@media (max-width: 900px) {
  .source-selector :deep(.arco-grid) {
    grid-template-columns: 1fr !important;
  }
  .credential-hint {
    align-items: flex-start;
  }
  .dockerfile-settings-head {
    align-items: flex-start;
    flex-direction: column;
  }
}
</style>

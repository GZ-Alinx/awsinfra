<template>
  <div class="cicd-page">
    <a-alert v-if="!store.currentProjectKey" type="warning" show-icon>请先在右上角选择项目，CI/CD 连接、凭据、Job 和构建记录均按项目隔离。</a-alert>
    <a-card class="hero-card">
      <div class="hero-content">
        <div><span class="eyebrow">JENKINS DELIVERY</span><h2>CICD</h2><p>平台负责对接与操作，Jenkins 保存 Job，GitLab 保存 Jenkinsfile 和部署清单。</p></div>
        <div class="hero-stats"><div><strong>{{ scopedJobs.length }}</strong><span>当前环境 Job</span></div><div><strong>{{ runningCount }}</strong><span>执行中</span></div><div><strong>{{ environmentConnections.length }}</strong><span>可用 Jenkins</span></div></div>
      </div>
    </a-card>

    <a-card v-if="store.currentProjectKey && store.currentEnvironmentKey" class="integration-card">
      <template #title><span class="card-title">当前环境 Jenkins 对接</span></template>
      <template #extra><a-button size="small" :loading="integrationLoading" @click="loadIntegration"><icon-refresh />检测</a-button></template>
      <a-spin :loading="integrationLoading" style="width:100%">
        <div v-if="managedJenkins" class="integration-content">
          <div class="integration-main">
            <div class="integration-title"><strong>{{ store.currentProject?.display_name }} · {{ store.currentEnvironment?.display_name }}</strong><a-tag :color="deploymentColor(managedJenkins.deployment_status)">{{ deploymentName(managedJenkins.deployment_status) }}</a-tag><a-tag v-if="managedJenkins.connected" :color="managedJenkins.connection_healthy ? 'green' : 'orange'">{{ managedJenkins.connection_healthy ? '已对接' : '待重连' }}</a-tag></div>
            <div v-if="managedJenkins.enabled" class="integration-meta"><span>Namespace <strong>{{ managedJenkins.namespace }}</strong></span><span>Service <strong>{{ managedJenkins.service_name }}:{{ managedJenkins.service_port }}</strong></span><span>连接方式 <strong>EKS 安全隧道</strong></span></div>
            <p v-if="managedJenkins.reason" class="integration-reason">{{ managedJenkins.reason }}</p>
            <p v-else-if="managedJenkins.connected">该环境由平台部署的 Jenkins 已与 CI/CD 绑定，Job、构建与日志都会走当前 EKS 环境。</p>
            <p v-else>Jenkins 已从当前项目环境自动识别，对接时平台会受控读取 Kubernetes Secret 并建立本地回环隧道。</p>
          </div>
          <a-space>
            <a-button v-if="!managedJenkins.enabled" type="primary" @click="router.push({name:'environment'})">前往启用 Jenkins</a-button>
            <a-button v-else type="primary" :disabled="!managedJenkins.can_connect || !store.canConfigure || !store.canViewSecrets" @click="openManagedConnect">{{ managedJenkins.connected ? '重新对接' : '对接当前环境 Jenkins' }}</a-button>
          </a-space>
        </div>
        <a-empty v-else description="尚未获取当前环境 Jenkins 状态" />
      </a-spin>
    </a-card>

    <a-tabs v-model:active-key="activeTab" type="rounded" class="cicd-tabs">
      <a-tab-pane key="delivery" title="项目接入">
        <delivery-repository-panel @provisioned="loadAll" />
      </a-tab-pane>
      <a-tab-pane key="services" title="服务与清单">
        <service-catalog-panel @changed="loadAll" />
      </a-tab-pane>
      <a-tab-pane key="jobs" title="Job 管理">
        <a-card>
          <template #title><span class="card-title">项目 Job</span></template>
          <template #extra><a-space><a-button :loading="loading" @click="loadAll"><icon-refresh />刷新</a-button><a-button type="primary" :disabled="!store.canConfigure || !environmentConnections.length || !deliveryServices.length" @click="openJob()"><icon-plus />创建 Job</a-button></a-space></template>
          <a-alert v-if="!environmentConnections.length" type="warning" show-icon class="section-alert">当前环境尚未对接 Jenkins。为防止测试与生产串用，其他环境的 Jenkins 不会显示，也不能作为当前 Job 的执行目标。</a-alert>
          <a-alert v-else-if="!deliveryServices.length" type="info" show-icon class="section-alert">请先在“服务与清单”添加服务并同步部署清单，再创建 Job。</a-alert>
          <a-table :data="scopedJobs" :loading="loading" row-key="key" :pagination="{ pageSize: 10 }">
            <template #columns>
              <a-table-column title="Job / 服务"><template #cell="{ record }"><div class="primary-cell"><strong>{{ record.display_name }}</strong><small>{{ record.jenkins_job_name }}</small><div class="tag-list compact-tags"><a-tag size="small" color="purple">{{ store.environmentLabel(record.environment_key) }}</a-tag><a-tag v-for="service in jobServices(record)" :key="service" size="small" color="arcoblue">{{ serviceName(service) }}</a-tag></div></div></template></a-table-column>
              <a-table-column title="执行策略" :width="185"><template #cell="{ record }"><div class="primary-cell"><div class="tag-list compact-tags"><a-tag :color="record.jenkinsfile_mode === 'generated' ? 'green' : 'gray'">{{ record.jenkinsfile_mode === 'generated' ? '平台生成' : '已有 Jenkinsfile' }}</a-tag><a-tag :color="record.trigger_mode === 'gitlab_push' ? 'arcoblue' : 'gray'">{{ record.trigger_mode === 'gitlab_push' ? 'Push 自动触发' : '手动触发' }}</a-tag></div><small>{{ record.jenkinsfile_mode === 'generated' ? '每次构建一个服务' : '由现有 Jenkinsfile 执行' }}<template v-if="record.trigger_mode === 'gitlab_push'"> · {{ record.trigger_branch }}</template></small></div></template></a-table-column>
              <a-table-column title="Jenkinsfile"><template #cell="{ record }"><div class="repo-cell"><span>{{ repositoryName(record.jenkinsfile_repository, record.jenkinsfile_repo) }}</span><a-link :href="gitLabFileURL(record.jenkinsfile_repo,record.jenkinsfile_branch,record.jenkinsfile_path)" target="_blank">{{ record.jenkinsfile_branch }} / {{ record.jenkinsfile_path }}</a-link></div></template></a-table-column>
              <a-table-column title="Dockerfile"><template #cell="{ record }"><div class="repo-cell"><template v-for="service in jobServices(record)" :key="service"><span>{{ serviceName(service) }}</span><small>{{ jobDockerfilePath(deliveryServices.find(item=>item.key===service),record.environment_key) }}</small></template></div></template></a-table-column>
              <a-table-column title="部署清单"><template #cell="{ record }"><div class="repo-cell"><span>{{ repositoryName(record.manifest_repository, record.manifest_repo) }}</span><small>{{ record.manifest_branch }} / {{ record.manifest_path }}</small></div></template></a-table-column>
              <a-table-column title="同步状态" :width="300"><template #cell="{ record }"><div class="job-sync-status"><a-tag :color="syncColor(record.sync_status)">{{ syncName(record.sync_status) }}</a-tag><small v-if="record.sync_status === 'failed' && record.sync_error" :title="record.sync_error">{{ record.sync_error }}</small></div></template></a-table-column>
              <a-table-column title="操作" :width="390"><template #cell="{ record }"><a-space wrap>
                <a-button size="mini" type="primary" :disabled="!store.canDeploy || record.sync_status !== 'ready' || !record.enabled" @click="openBuild(record)">构建</a-button>
                <a-button v-if="record.trigger_mode === 'gitlab_push'" size="mini" :disabled="!store.canConfigure" @click="openWebhookSetup(record)">Webhook</a-button>
                <a-button size="mini" :loading="busyKey === `job-sync-${record.key}`" :disabled="!store.canConfigure" @click="syncJob(record)">重新同步</a-button>
                <a-button size="mini" :disabled="!store.canConfigure" @click="openJob(record)">编辑</a-button>
                <a-button size="mini" status="danger" :disabled="!store.canConfigure" @click="openDeleteJob(record)">删除</a-button>
              </a-space></template></a-table-column>
            </template>
            <template #empty><a-empty description="尚未配置 CI/CD Job" /></template>
          </a-table>
        </a-card>
      </a-tab-pane>

      <a-tab-pane key="builds" title="构建记录">
        <a-card>
          <template #title><span class="card-title">构建与发布记录</span></template><template #extra><a-space><span v-if="runningCount" class="live-stream"><i />每 2 秒自动更新</span><a-button :loading="loadingBuilds" @click="loadBuilds()"><icon-refresh />刷新</a-button></a-space></template>
          <a-table :data="builds" :loading="loadingBuilds" row-key="id" :pagination="{ pageSize: 12 }">
            <template #columns>
              <a-table-column title="构建"><template #cell="{ record }"><a-link @click="openLogs(record)">#{{ record.build_number || '排队中' }}</a-link><div class="muted-id">{{ record.id }}</div></template></a-table-column>
              <a-table-column title="Job"><template #cell="{ record }">{{ jobName(record.job_key) }}</template></a-table-column>
              <a-table-column title="环境" :width="100"><template #cell="{ record }"><a-tag>{{ store.environmentLabel(record.environment) }}</a-tag></template></a-table-column>
              <a-table-column title="状态" :width="125"><template #cell="{ record }"><div class="live-status" :class="`status-${record.status}`"><span class="status-dot" /><a-tag :color="buildColor(record.status)">{{ buildName(record.status) }}</a-tag></div></template></a-table-column>
              <a-table-column title="实时进度" :width="280"><template #cell="{ record }"><div class="build-progress" :class="{ 'is-running': record.status === 'running' }"><div class="progress-caption"><span>{{ record.current_stage || buildName(record.status) }}</span><strong>{{ buildProgress(record) }}%</strong></div><div class="progress-shell"><a-progress :percent="buildProgress(record) / 100" :status="record.status === 'failed' ? 'danger' : record.status === 'succeeded' ? 'success' : 'normal'" size="small" :show-text="false" /></div><div v-if="record.stages?.length" class="record-stage-dots"><span v-for="stage in record.stages.slice(-8)" :key="`${stage.service}-${stage.name}`" :class="`dot-${stage.status}`" :title="`${stage.name} · ${stageStatusName(stage.status)}`" /></div></div></template></a-table-column>
              <a-table-column title="发起人" data-index="requested_by" :width="120" />
              <a-table-column title="创建时间"><template #cell="{ record }">{{ formatTime(record.created_at) }}</template></a-table-column>
              <a-table-column title="操作" :width="230"><template #cell="{ record }"><a-space><a-button size="mini" @click="openLogs(record)">日志</a-button><a-button v-if="['failed','canceled'].includes(record.status) && jobExists(record.job_key)" size="mini" type="primary" :disabled="!store.canDeploy" @click="retryBuild(record)">重试</a-button><a-tag v-else-if="!jobExists(record.job_key)" color="gray">Job 已删除</a-tag><a-popconfirm v-if="['queued','running'].includes(record.status)" content="确认停止该 Jenkins 构建？" @ok="cancelBuild(record)"><a-button size="mini" status="danger" :disabled="!store.canDeploy">停止</a-button></a-popconfirm></a-space></template></a-table-column>
            </template><template #empty><a-empty description="当前项目与环境还没有构建记录" /></template>
          </a-table>
        </a-card>
      </a-tab-pane>

      <a-tab-pane key="settings" title="Jenkins 设置">
        <div class="settings-grid">
          <a-card><template #title><span class="card-title">Jenkins 连接</span></template><template #extra><a-button type="primary" size="small" :disabled="!store.canConfigure" @click="openConnection()"><icon-plus />添加</a-button></template>
            <a-alert type="warning" show-icon class="section-alert">Jenkins 连接按“项目 + 环境”硬隔离。当前仅展示 {{ store.environmentLabel(store.currentEnvironmentKey) }}连接，不能跨环境复用。</a-alert>
            <a-table :data="environmentConnections" :pagination="false" row-key="key"><template #columns>
              <a-table-column title="连接"><template #cell="{ record }"><div class="primary-cell"><strong>{{ record.display_name }}</strong><small>{{ record.base_url }}</small><small v-if="record.environment_key">来源：{{ store.environmentLabel(record.environment_key) }} · {{ record.connection_mode === 'eks_port_forward' ? 'EKS 安全隧道' : '直连' }}</small></div></template></a-table-column>
              <a-table-column title="健康状态" :width="130"><template #cell="{ record }"><a-tooltip :content="record.last_check_error || ''"><a-tag :color="record.last_check_status === 'healthy' ? 'green' : record.last_check_status === 'failed' ? 'red' : 'gray'">{{ record.last_check_status === 'healthy' ? '正常' : record.last_check_status === 'failed' ? '异常' : '未检测' }}</a-tag></a-tooltip></template></a-table-column>
              <a-table-column title="操作" :width="250"><template #cell="{ record }"><a-space><a-button size="mini" :loading="busyKey === `connection-test-${record.key}`" :disabled="!store.canConfigure" @click="testConnection(record)">测试</a-button><a-button v-if="record.connection_mode !== 'eks_port_forward'" size="mini" :disabled="!store.canConfigure" @click="openConnection(record)">编辑</a-button><a-tag v-else color="arcoblue">环境管理</a-tag><a-popconfirm content="只有未被凭据和 Job 引用的连接才能删除。" @ok="deleteConnection(record)"><a-button size="mini" status="danger" :disabled="!store.canConfigure">删除</a-button></a-popconfirm></a-space></template></a-table-column>
            </template><template #empty><a-empty description="尚未配置 Jenkins" /></template></a-table>
          </a-card>
          <a-card><template #title><span class="card-title">当前环境 Jenkins 凭据</span></template><template #extra><a-button type="primary" size="small" :disabled="!store.canConfigure || !environmentConnections.length" @click="openCredential()"><icon-plus />添加 GitLab 认证</a-button></template>
            <a-alert type="info" show-icon class="section-alert">每个环境会生成独立 Credential ID，并只同步到当前环境 Jenkins。复制下方 ID 后可手动写入 Jenkinsfile；平台永远不会显示 Token 明文。</a-alert>
            <a-table :data="environmentCredentials" :pagination="false" row-key="key"><template #columns>
              <a-table-column title="凭据"><template #cell="{ record }"><div class="primary-cell"><strong>{{ record.display_name }}</strong><small>{{ store.environmentLabel(record.environment_key) }} · {{ credentialKindName(record.kind) }}</small><code>{{ record.external_id }}</code><small>Jenkinsfile：credentialsId: '{{ record.external_id }}'</small></div></template></a-table-column>
              <a-table-column title="同步" :width="110"><template #cell="{ record }"><a-tooltip :content="record.sync_error || ''"><a-tag :color="syncColor(record.sync_status)">{{ syncName(record.sync_status) }}</a-tag></a-tooltip></template></a-table-column>
              <a-table-column title="操作" :width="380"><template #cell="{ record }"><a-space><a-button size="mini" @click="copyCredentialID(record.external_id)">复制 ID</a-button><a-button size="mini" @click="copyCredentialReference(record.external_id)">复制 Jenkinsfile 引用</a-button><a-button v-if="record.kind !== 'existing'" size="mini" :loading="busyKey === `credential-sync-${record.key}`" :disabled="!store.canConfigure" @click="syncCredential(record)">重新同步</a-button><a-button size="mini" :disabled="!store.canConfigure" @click="openCredential(record)">编辑</a-button><a-popconfirm content="被 Job 引用的凭据不允许删除。" @ok="deleteCredential(record)"><a-button size="mini" status="danger" :disabled="!store.canConfigure">删除</a-button></a-popconfirm></a-space></template></a-table-column>
            </template><template #empty><a-empty description="尚未配置 Jenkins 凭据" /></template></a-table>
          </a-card>
          <a-card class="repository-card"><template #title><span class="card-title">项目 Git 仓库目录</span></template><template #extra><a-button type="primary" size="small" :disabled="!store.canConfigure" @click="openRepository()"><icon-plus />添加仓库</a-button></template>
            <a-alert type="info" show-icon class="section-alert">可登记任意数量的 Jenkinsfile、业务源码和部署清单仓库；创建构建任务时直接选择，URL、默认分支和路径会自动带入。</a-alert>
            <a-table :data="repositories" :pagination="{pageSize:8}" row-key="key"><template #columns>
              <a-table-column title="仓库"><template #cell="{record}"><div class="primary-cell"><strong>{{ record.display_name }}</strong><small>{{ record.key }} · {{ repositoryPurposeName(record.purpose) }}</small></div></template></a-table-column>
              <a-table-column title="Provider" :width="110"><template #cell="{record}"><a-tag :color="record.provider==='gitlab'?'orange':'gray'">{{ record.provider==='gitlab'?'GitLab':'Git' }}</a-tag></template></a-table-column>
              <a-table-column title="Clone URL"><template #cell="{record}"><div class="repo-cell"><span>{{ compactRepo(record.clone_url) }}</span><small>{{ record.default_branch }}<template v-if="record.default_path"> / {{ record.default_path }}</template></small></div></template></a-table-column>
              <a-table-column title="操作" :width="170"><template #cell="{record}"><a-space><a-button size="mini" :disabled="!store.canConfigure" @click="openRepository(record)">编辑</a-button><a-popconfirm content="被构建任务引用的仓库不允许删除。" @ok="deleteRepository(record)"><a-button size="mini" status="danger" :disabled="!store.canConfigure">删除</a-button></a-popconfirm></a-space></template></a-table-column>
            </template><template #empty><a-empty description="尚未登记项目 Git 仓库" /></template></a-table>
          </a-card>
        </div>
      </a-tab-pane>
    </a-tabs>

    <a-modal v-model:visible="connectionVisible" :title="editingConnection ? '编辑 Jenkins 连接' : '添加 Jenkins 连接'" :ok-loading="saving" @before-ok="saveConnection">
      <a-alert type="warning" show-icon class="section-alert">该连接只属于 {{ store.environmentLabel(store.currentEnvironmentKey) }}，保存后不能改绑到其他环境。</a-alert>
      <a-form :model="connectionForm" layout="vertical"><a-grid :cols="2" :col-gap="16"><a-grid-item><a-form-item label="所属环境" required><a-input :model-value="store.environmentLabel(connectionForm.environment_key)" disabled /></a-form-item></a-grid-item><a-grid-item><a-form-item label="连接标识" required extra="建议包含环境，例如 prod-jenkins。"><a-input v-model="connectionForm.key" :disabled="editingConnection" /></a-form-item></a-grid-item><a-grid-item><a-form-item label="显示名称" required><a-input v-model="connectionForm.display_name" /></a-form-item></a-grid-item></a-grid><a-form-item label="Jenkins 地址" required extra="填写包含 http:// 或 https:// 的完整 URL。"><a-input v-model="connectionForm.base_url" /></a-form-item><a-alert v-if="connectionUsesInsecureHTTP" type="warning" show-icon style="margin-bottom:16px">HTTP 会以明文传输 Jenkins 用户名和 API Token，只应在受信任的内网中使用。<template #action><a-switch v-model="connectionForm.allow_insecure_http" checked-text="已确认" unchecked-text="未确认" /></template></a-alert><a-form-item label="Jenkins 用户名" required><a-input v-model="connectionForm.username" autocomplete="off" /></a-form-item><a-form-item label="API Token" :required="!editingConnection" :extra="editingConnection ? '留空则保留原 Token。' : '请使用 Jenkins API Token，不要填写登录密码。'"><a-input-password v-model="connectionForm.api_token" autocomplete="new-password" /></a-form-item></a-form>
    </a-modal>

    <a-modal v-model:visible="managedConnectVisible" title="对接当前环境 Jenkins" ok-text="确认对接" :ok-loading="managedConnecting" @before-ok="connectManagedJenkins" @cancel="managedPassword=''">
      <a-alert type="warning" show-icon>平台将使用当前项目绑定的 AWS 凭据生成 kubeconfig，通过回环端口转发连接 Jenkins，并读取该环境 Jenkins Secret。凭据只会加密保存，不会回显。</a-alert>
      <a-descriptions v-if="managedJenkins" :column="2" size="small" style="margin-top:16px"><a-descriptions-item label="项目环境">{{ store.currentProject?.display_name }} / {{ store.currentEnvironment?.display_name }}</a-descriptions-item><a-descriptions-item label="Jenkins Service">{{ managedJenkins.namespace }}/{{ managedJenkins.service_name }}:{{ managedJenkins.service_port }}</a-descriptions-item></a-descriptions>
      <a-form :model="{}" layout="vertical" style="margin-top:12px"><a-form-item label="当前平台登录密码" required extra="用于本次敏感凭据读取的二次验证。"><a-input-password v-model="managedPassword" autocomplete="current-password" /></a-form-item></a-form>
    </a-modal>

    <a-modal v-model:visible="credentialVisible" :title="credentialReturnToJob ? '为当前 Job 添加 GitLab Token' : editingCredential ? '编辑 Jenkins 凭据' : '添加 GitLab / Jenkins 凭据'" width="680px" :ok-loading="saving" @before-ok="saveCredential" @cancel="credentialReturnToJob=false">
      <a-alert v-if="credentialReturnToJob" type="info" show-icon class="section-alert">填写 GitLab 用户名和 Personal Access Token。保存后平台会加密存储、自动写入当前 Jenkins，并把 Credential ID 绑定到 Jenkinsfile 与部署清单仓库。</a-alert>
      <a-form :model="credentialForm" layout="vertical"><a-grid :cols="2" :col-gap="16"><a-grid-item><a-form-item label="所属环境" required><a-input :model-value="store.environmentLabel(credentialForm.environment_key)" disabled /></a-form-item></a-grid-item><a-grid-item><a-form-item label="凭据标识" required><a-input v-model="credentialForm.key" :disabled="editingCredential" /></a-form-item></a-grid-item><a-grid-item><a-form-item label="显示名称" required><a-input v-model="credentialForm.display_name" /></a-form-item></a-grid-item><a-grid-item><a-form-item label="目标 Jenkins" required><a-select v-model="credentialForm.connection_key" :disabled="editingCredential"><a-option v-for="item in environmentConnections" :key="item.key" :value="item.key">{{ item.display_name }}</a-option></a-select></a-form-item></a-grid-item><a-grid-item><a-form-item label="凭据类型" required><a-select v-model="credentialForm.kind" :disabled="editingCredential"><a-option value="gitlab_token">GitLab HTTPS Token</a-option><a-option value="existing">引用 Jenkins 已有凭据</a-option><a-option value="username_password">通用用户名 / 密码</a-option><a-option value="secret_text">Secret Text / Token</a-option><a-option value="ssh_private_key">SSH 私钥</a-option></a-select></a-form-item></a-grid-item></a-grid><a-form-item label="Jenkins Credential ID" extra="可以指定；留空时平台按 ops-项目-环境-凭据标识生成，保存后会在管理表格输出。"><a-input v-model="credentialForm.external_id" :disabled="editingCredential" /></a-form-item><a-form-item label="描述"><a-input v-model="credentialForm.description" /></a-form-item>
        <template v-if="credentialForm.kind === 'username_password' || credentialForm.kind === 'gitlab_token'"><a-form-item :label="credentialForm.kind === 'gitlab_token' ? 'GitLab 用户名' : '用户名'" required><a-input v-model="credentialForm.username" autocomplete="off" /></a-form-item><a-form-item :label="credentialForm.kind === 'gitlab_token' ? 'GitLab Personal Access Token' : '密码 / Token'" :required="!editingCredential"><a-input-password v-model="credentialForm.password" autocomplete="new-password" /></a-form-item></template>
        <a-form-item v-else-if="credentialForm.kind === 'secret_text'" label="Secret Text / Token" :required="!editingCredential"><a-textarea v-model="credentialForm.secret_text" :auto-size="{ minRows: 3, maxRows: 6 }" /></a-form-item>
        <template v-else-if="credentialForm.kind === 'ssh_private_key'"><a-form-item label="SSH 用户名" required><a-input v-model="credentialForm.username" /></a-form-item><a-form-item label="SSH 私钥" :required="!editingCredential"><a-textarea v-model="credentialForm.private_key" :auto-size="{ minRows: 5, maxRows: 10 }" /></a-form-item><a-form-item label="私钥口令"><a-input-password v-model="credentialForm.passphrase" /></a-form-item></template>
      </a-form>
    </a-modal>

    <a-modal v-model:visible="repositoryVisible" :title="editingRepository ? '编辑项目仓库' : '添加项目仓库'" width="700px" :ok-loading="saving" @before-ok="saveRepository">
      <a-form :model="repositoryForm" layout="vertical"><a-grid :cols="2" :col-gap="16"><a-grid-item><a-form-item label="仓库标识" required><a-input v-model="repositoryForm.key" :disabled="editingRepository" /></a-form-item></a-grid-item><a-grid-item><a-form-item label="显示名称" required><a-input v-model="repositoryForm.display_name" /></a-form-item></a-grid-item><a-grid-item><a-form-item label="Provider" required><a-select v-model="repositoryForm.provider"><a-option value="gitlab">GitLab</a-option><a-option value="generic_git">通用 Git</a-option></a-select></a-form-item></a-grid-item><a-grid-item><a-form-item label="仓库用途" required><a-select v-model="repositoryForm.purpose"><a-option value="jenkinsfile">Jenkinsfile</a-option><a-option value="manifest">部署清单</a-option><a-option value="source">业务源码</a-option><a-option value="general">通用</a-option></a-select></a-form-item></a-grid-item></a-grid><a-form-item label="HTTPS Clone URL" required><a-input v-model="repositoryForm.clone_url" /></a-form-item><a-grid :cols="2" :col-gap="16"><a-grid-item><a-form-item label="默认分支" required><a-input v-model="repositoryForm.default_branch" /></a-form-item></a-grid-item><a-grid-item><a-form-item label="默认路径"><a-input v-model="repositoryForm.default_path" /></a-form-item></a-grid-item></a-grid><a-form-item label="说明"><a-input v-model="repositoryForm.description" /></a-form-item></a-form>
    </a-modal>

    <a-modal v-model:visible="jobVisible" :title="editingJob ? '编辑并同步 Jenkins Job' : '创建并同步 Jenkins Job'" width="1120px" ok-text="保存并同步 Jenkins" :ok-loading="saving" @before-ok="saveJob">
      <a-form :model="jobForm" layout="vertical">
        <a-alert type="success" show-icon>这里只建立对接关系。Jenkins Job 配置保存在 Jenkins，Jenkinsfile 与部署清单保存在 GitLab；保存后会自动回读校验。</a-alert>
        <a-divider orientation="left">常用配置</a-divider><a-grid :cols="4" :col-gap="16"><a-grid-item><a-form-item label="所属环境" required extra="保存后固定，不能切换到其他环境。"><a-input :model-value="store.environmentLabel(jobForm.environment_key)" disabled /></a-form-item></a-grid-item><a-grid-item><a-form-item label="Job 标识" required><a-input v-model="jobForm.key" :disabled="editingJob" /></a-form-item></a-grid-item><a-grid-item><a-form-item label="显示名称" required><a-input v-model="jobForm.display_name" /></a-form-item></a-grid-item><a-grid-item><a-form-item label="目标 Jenkins" required><a-select v-model="jobForm.connection_key" :disabled="environmentConnections.length === 1" @change="jobConnectionChanged"><a-option v-for="item in environmentConnections" :key="item.key" :value="item.key">{{ item.display_name }}</a-option></a-select></a-form-item></a-grid-item></a-grid>
        <a-form-item label="构建服务" required extra="一个 Job 可包含多个服务；每个服务仍使用自己的部署清单。"><a-select v-model="jobForm.service_keys" multiple allow-search placeholder="选择一个或多个服务"><a-option v-for="item in deliveryServices" :key="item.key" :value="item.key">{{ item.display_name }} · {{ item.key }} · {{ item.language.toUpperCase() }}</a-option></a-select></a-form-item>
        <a-grid :cols="3" :col-gap="16"><a-grid-item><a-form-item label="Jenkinsfile 来源"><a-radio-group v-model="jobForm.jenkinsfile_mode" type="button" @change="selectDefaultJobCredential"><a-radio value="generated">平台生成到 GitLab</a-radio><a-radio value="existing">对接已有文件</a-radio></a-radio-group></a-form-item></a-grid-item><a-grid-item :span="2"><a-form-item label="GitLab 仓库凭据" required extra="两种 Jenkinsfile 来源都使用该凭据访问 Jenkinsfile 与部署清单仓库；同步 Job 时会自动写入目标 Jenkins。"><div class="job-credential-picker"><a-select :model-value="jobForm.jenkinsfile_credential" allow-clear placeholder="选择已同步凭据" @change="selectSharedJobCredential"><a-option v-if="jobForm.jenkinsfile_mode === 'generated'" :value="PLATFORM_MANAGED_CREDENTIAL">平台自动生成只读凭据（推荐）</a-option><a-option v-for="item in credentialsForConnection" :key="item.key" :value="item.key">{{ item.display_name }} · {{ item.external_id }}</a-option></a-select><a-button type="outline" :disabled="!jobForm.connection_key" @click="openJobGitLabCredential"><icon-plus />账号 Token</a-button></div></a-form-item></a-grid-item></a-grid>
        <template v-if="jobForm.jenkinsfile_mode === 'generated'">
          <div class="delivery-path-card">
            <div class="delivery-path-heading"><div><strong>运维交付仓库与路径</strong><p>平台自动管理 Jenkinsfile、服务配置和部署清单；环境隔离由仓库路径保证。</p></div><a-radio-group v-model="jobForm.delivery_repository_mode" type="button" @change="generatedRepositoryModeChanged"><a-radio value="unified">同一仓库（推荐）</a-radio><a-radio value="separate">兼容双仓库</a-radio></a-radio-group></div>
            <a-form-item v-if="jobForm.delivery_repository_mode === 'unified'" label="运维交付仓库" required extra="Jenkinsfile 与部署清单使用同一仓库、同一分支，通过不同目录隔离。"><a-select v-model="jobForm.delivery_repository_key" @change="generatedDeliveryRepositoryChanged"><a-option v-for="item in deliveryRepositories" :key="item.key" :value="item.key">{{ item.display_name }} · {{ compactRepo(item.clone_url) }}</a-option></a-select></a-form-item>
            <a-alert v-else type="info" show-icon class="section-alert">保留旧项目的流水线仓库与部署清单仓库，不迁移、不覆盖现有文件。</a-alert>
            <a-grid :cols="2" :col-gap="16">
              <a-grid-item><a-form-item label="Jenkinsfile 路径" required><a-input v-model="jobForm.jenkinsfile_path" /></a-form-item></a-grid-item>
              <a-grid-item><a-form-item label="部署清单环境目录" required><a-input v-model="jobForm.manifest_path" /></a-form-item></a-grid-item>
            </a-grid>
          </div>
          <a-alert type="info" show-icon class="section-alert"><div class="external-config-summary"><span>环境 <code>{{ jobForm.environment_key }}</code> · Job 固定绑定，构建时不可修改</span><span>Jenkinsfile <code>{{ jobForm.jenkinsfile_repo || '请先创建项目交付仓库' }}/{{ jobForm.jenkinsfile_path }}</code></span><span>服务配置 <code>{{ jobServicesPath }}</code></span><span>部署清单 <code>{{ jobForm.manifest_repo || '请先创建项目交付仓库' }}/{{ jobForm.manifest_path }}/&lt;service&gt;/manifest.yaml</code></span><span>公共模块 <code>lib/v4/</code>；业务编译继续由各服务 Dockerfile 负责。</span></div></a-alert>
          <a-collapse class="section-alert">
            <a-collapse-item key="jenkinsfile-editor" header="高级 · Jenkinsfile 在线维护">
              <a-alert type="warning" show-icon class="section-alert">留空使用平台标准 Jenkinsfile；填写后将作为当前环境、当前 Job 的脚本同步到 GitLab。恢复为空即可重新使用平台模板。</a-alert>
              <a-form-item label="Jenkinsfile 内容" :extra="`同步路径：${jobForm.jenkinsfile_path}`"><a-textarea v-model="jobForm.jenkinsfile_content" placeholder="留空使用平台标准模板" :auto-size="{ minRows: 12, maxRows: 26 }" /></a-form-item>
              <a-space><a-button size="small" :disabled="!jobForm.jenkinsfile_content" @click="jobForm.jenkinsfile_content=''">恢复平台模板</a-button><a-link v-if="editingJob && jobForm.jenkinsfile_repo" :href="gitLabFileURL(jobForm.jenkinsfile_repo,jobForm.jenkinsfile_branch,jobForm.jenkinsfile_path)" target="_blank">查看 GitLab 当前文件</a-link></a-space>
            </a-collapse-item>
          </a-collapse>
        </template>
        <div v-if="jobForm.jenkinsfile_mode === 'existing'" class="jenkinsfile-import-banner"><div><strong>已有 Jenkinsfile？</strong><p>粘贴后自动识别构建参数、仓库与凭据要求。</p></div><a-button type="outline" @click="openJenkinsfileImport">智能导入 Jenkinsfile</a-button></div>
        <template v-if="jobForm.jenkinsfile_mode === 'existing'"><a-divider orientation="left">已有 Jenkinsfile 与部署清单</a-divider>
        <a-form-item label="Jenkinsfile 仓库" required>
          <a-select v-model="jobForm.jenkinsfile_repo" allow-search allow-create placeholder="选择仓库或粘贴 HTTPS 地址" @change="selectJobRepository('jenkinsfile',$event)">
            <a-option v-for="item in jenkinsfileRepositories" :key="item.key" :value="item.clone_url">{{ item.display_name }} · {{ compactRepo(item.clone_url) }}</a-option>
          </a-select>
        </a-form-item>
        <a-grid :cols="2" :col-gap="16">
          <a-grid-item><a-form-item label="分支"><a-input v-model="jobForm.jenkinsfile_branch" /></a-form-item></a-grid-item>
          <a-grid-item><a-form-item label="Jenkinsfile 路径"><a-input v-model="jobForm.jenkinsfile_path" /></a-form-item></a-grid-item>
        </a-grid>

        <a-form-item label="部署清单仓库" required>
          <a-select v-model="jobForm.manifest_repo" allow-search allow-create placeholder="选择仓库或粘贴 HTTPS 地址" @change="selectJobRepository('manifest',$event)">
            <a-option v-for="item in manifestRepositories" :key="item.key" :value="item.clone_url">{{ item.display_name }} · {{ compactRepo(item.clone_url) }}</a-option>
          </a-select>
        </a-form-item>
        <a-grid :cols="2" :col-gap="16">
          <a-grid-item><a-form-item label="分支"><a-input v-model="jobForm.manifest_branch" /></a-form-item></a-grid-item>
          <a-grid-item><a-form-item label="环境目录"><a-input v-model="jobForm.manifest_path" /></a-form-item></a-grid-item>
        </a-grid>
        <a-alert type="success" show-icon>只保存 Jenkins Credential ID 引用，不会把 Token、账号或密码写入代码仓库。</a-alert>
        <a-collapse class="job-advanced-collapse">
          <a-collapse-item key="source" header="高级兼容 · 业务源码仓库">
            <a-alert type="info" show-icon class="section-alert">仅当 Jenkinsfile 读取平台注入的源码仓库地址时配置；Jenkinsfile 自行拉取源码时保持为空。</a-alert>
            <a-form-item label="业务源码仓库（可选）" extra="选择项目已登记仓库，或直接粘贴 HTTPS Clone URL。">
              <a-select v-model="jobForm.source_repo" allow-clear allow-search allow-create placeholder="选择仓库或粘贴 HTTPS 地址" @change="selectJobRepository('source',$event)">
                <a-option v-for="item in sourceRepositories" :key="item.key" :value="item.clone_url">{{ item.display_name }} · {{ compactRepo(item.clone_url) }}</a-option>
              </a-select>
            </a-form-item>
          </a-collapse-item>
        </a-collapse></template>
        <section class="job-notification-card">
          <div class="job-notification-copy"><strong>Lark 构建通知</strong><p>直接复用当前环境“告警管理”中的 Lark / 飞书 Webhook。平台会把 Webhook 转成 Jenkins Secret Text，不向浏览器或 GitLab 返回明文。</p></div>
          <a-form-item label="告警通道" class="job-notification-select"><a-select v-model="jobForm.lark_alert_channel" allow-clear :loading="notificationChannelsLoading" placeholder="不发送构建通知" @change="jobForm.lark_credential_id=''" @popup-visible-change="visible=>visible&&loadNotificationChannels()"><a-option v-for="item in notificationChannels" :key="`${item.environment}-${item.name}`" :value="item.name" :disabled="!item.configured">{{ item.name }} · {{ store.environmentLabel(item.environment) }}<template v-if="!item.configured"> · 未配置地址</template></a-option></a-select></a-form-item>
          <div v-if="jobForm.lark_credential_id" class="job-notification-id"><span>Jenkins Credential ID</span><code>{{ jobForm.lark_credential_id }}</code><a-button size="mini" type="text" @click="copyCredentialID(jobForm.lark_credential_id)">复制</a-button></div>
          <a-alert v-if="jobForm.jenkinsfile_mode === 'existing' && jobForm.lark_alert_channel" type="info" show-icon>已有 Jenkinsfile 可通过上面的 Credential ID 使用 <code>withCredentials(string(...))</code>；保存同步后会生成或更新该 ID。</a-alert>
        </section>
        <section class="job-trigger-card">
          <div class="job-trigger-heading"><div><strong>构建触发方式</strong><p>开启后，GitLab 代码 Push 会先通知平台，再由平台创建 Jenkins 构建，因此自动构建也会完整记录进度和日志。</p></div><a-radio-group v-model="jobForm.trigger_mode" type="button"><a-radio value="manual">仅手动构建</a-radio><a-radio value="gitlab_push">GitLab Push 自动触发</a-radio></a-radio-group></div>
          <template v-if="jobForm.trigger_mode === 'gitlab_push'">
            <a-grid :cols="2" :col-gap="16"><a-grid-item><a-form-item label="监听分支" required extra="只有该分支的 Push 会触发构建。"><a-select v-model="jobForm.trigger_branch" allow-create allow-search><a-option v-for="branch in triggerBranchOptions" :key="branch" :value="branch">{{ branch }}</a-option></a-select></a-form-item></a-grid-item><a-grid-item><a-form-item label="Webhook 地址（填入 GitLab）" required extra="保存后会生成 Secret Token。"><a-input :model-value="jobWebhookURL(jobForm.key)" readonly><template #append><a-button type="text" @click="copyText(jobWebhookURL(jobForm.key), 'Webhook 地址')">复制</a-button></template></a-input></a-form-item></a-grid-item></a-grid>
            <a-alert :type="editingJob && !currentJobWebhookConfigured ? 'warning' : 'info'" show-icon>{{ editingJob && currentJobWebhookConfigured ? 'Webhook Secret 已配置。如已遗失，可在 Job 列表点击“Webhook”重新生成。' : '保存 Job 后平台会生成一次性 Secret Token；在 GitLab 勾选 Push events 并填入地址与 Token。' }}</a-alert>
          </template>
        </section>
        <a-divider orientation="left">创建前对接汇总</a-divider>
        <div class="job-delivery-summary">
          <div class="job-delivery-summary-head"><div><strong>{{ store.environmentLabel(jobForm.environment_key) }} · {{ jobForm.jenkins_job_name || jobForm.key }}</strong><p>保存时依次同步 GitLab 凭据、交付文件和 Jenkins Job SCM 配置。</p></div><a-space><a-button size="small" @click="jobVisible=false;activeTab='services'">管理 Dockerfile</a-button><a-tag :color="jobSummaryReady ? 'green' : 'orange'">{{ jobSummaryReady ? '配置完整' : '待补充' }}</a-tag></a-space></div>
          <a-descriptions :column="2" bordered size="small"><a-descriptions-item label="目标 Jenkins">{{ selectedJobConnection?.display_name || '未选择' }}</a-descriptions-item><a-descriptions-item label="交付仓库">{{ compactRepo(jobForm.jenkinsfile_repo || '未选择') }} · {{ jobForm.jenkinsfile_branch }}</a-descriptions-item><a-descriptions-item label="Jenkinsfile">{{ jobForm.jenkinsfile_path }}</a-descriptions-item><a-descriptions-item label="部署清单">{{ jobForm.manifest_path }}/&lt;service&gt;/manifest.yaml</a-descriptions-item></a-descriptions>
          <a-table :data="selectedJobServices" :pagination="false" size="small" row-key="key" class="job-service-summary"><template #columns><a-table-column title="服务"><template #cell="{record}"><div class="primary-cell"><strong>{{ record.display_name }}</strong><small>{{ record.key }} · {{ record.source_branch }}</small></div></template></a-table-column><a-table-column title="源码仓库"><template #cell="{record}"><code>{{ compactRepo(record.source_repository) }}</code></template></a-table-column><a-table-column title="Dockerfile"><template #cell="{record}"><div class="repo-cell"><code>{{ jobDockerfilePath(record) }}</code><small>{{ record.dockerfile_source === 'source' ? '业务源码仓库' : `${jobForm.environment_key} 环境交付目录` }}</small></div></template></a-table-column><a-table-column title="部署目标"><template #cell="{record}"><div class="repo-cell"><span>{{ record.namespace }}</span><small>{{ jobForm.manifest_path }}/{{ record.key }}/manifest.yaml</small></div></template></a-table-column></template></a-table>
        </div>
        <a-collapse class="job-advanced-collapse" :default-active-key="[]">
          <a-collapse-item key="runtime" header="高级 · 运行设置">
            <a-alert v-if="jobForm.jenkinsfile_mode === 'generated'" type="info" show-icon class="section-alert">
              平台生成的 Job 每次只构建一个服务，失败后立即停止，不需要再配置串行、并行或失败策略。
            </a-alert>
            <a-grid :cols="3" :col-gap="16">
              <a-grid-item>
                <a-form-item label="Jenkins Job 名"><a-input v-model="jobForm.jenkins_job_name" /></a-form-item>
              </a-grid-item>
              <a-grid-item>
                <a-form-item label="启用 Job"><a-switch v-model="jobForm.enabled" /></a-form-item>
              </a-grid-item>
              <a-grid-item v-if="jobForm.jenkinsfile_mode === 'generated'">
                <a-form-item label="Agent 类型">
                  <a-select v-model="jobForm.agent_mode">
                    <a-option value="kubernetes">Kubernetes 动态 Agent</a-option>
                    <a-option value="node">固定 Jenkins 节点</a-option>
                  </a-select>
                </a-form-item>
              </a-grid-item>
              <a-grid-item v-if="jobForm.jenkinsfile_mode === 'generated' && jobForm.agent_mode === 'kubernetes'">
                <a-form-item label="Agent ServiceAccount" extra="用于 ECR Pod Identity 与目标 Namespace RBAC。">
                  <a-input v-model="jobForm.kubernetes_service_account" />
                </a-form-item>
              </a-grid-item>
              <a-grid-item v-if="jobForm.jenkinsfile_mode === 'generated' && jobForm.agent_mode === 'node'">
                <a-form-item label="Agent Label"><a-input v-model="jobForm.agent_label" /></a-form-item>
              </a-grid-item>
              <a-grid-item v-if="jobForm.jenkinsfile_mode === 'generated'">
                <a-form-item label="超时（分钟）">
                  <a-input-number v-model="jobForm.pipeline_timeout_minutes" :min="5" :max="180" />
                </a-form-item>
              </a-grid-item>
              <a-grid-item v-if="jobForm.jenkinsfile_mode === 'generated'">
                <a-form-item label="部署完成判定" extra="推荐等待 Deployment 就绪；仅应用只确认 Kubernetes 已接收清单。">
                  <a-select v-model="jobForm.deploy_verify_mode">
                    <a-option value="rollout">应用并等待工作负载就绪</a-option>
                    <a-option value="apply">仅应用部署清单</a-option>
                  </a-select>
                </a-form-item>
              </a-grid-item>
              <a-grid-item v-if="jobForm.jenkinsfile_mode === 'generated' && jobForm.deploy_verify_mode === 'rollout'">
                <a-form-item label="就绪超时（分钟）">
                  <a-input-number v-model="jobForm.rollout_timeout_minutes" :min="1" :max="30" />
                </a-form-item>
              </a-grid-item>
              <a-grid-item v-if="jobForm.jenkinsfile_mode === 'generated' && jobForm.deploy_verify_mode === 'rollout'">
                <a-form-item label="失败自动回滚" extra="默认关闭以保留现场；开启后恢复已有 Deployment 的发布前 revision。">
                  <a-switch v-model="jobForm.rollback_on_failure" />
                </a-form-item>
              </a-grid-item>
            </a-grid>
            <a-grid v-if="jobForm.jenkinsfile_mode === 'generated'" :cols="2" :col-gap="16">
              <a-grid-item>
                <a-form-item label="AWS CLI Profile">
                  <a-input v-model="jobForm.aws_profile" placeholder="Pod Identity / 实例角色可留空" />
                </a-form-item>
              </a-grid-item>
              <a-grid-item>
                <a-form-item label="Telegram 凭据">
                  <a-select v-model="jobForm.telegram_credential_id" allow-clear>
                    <a-option v-for="item in secretTextCredentialsForConnection" :key="item.key" :value="item.external_id">
                      {{ item.display_name }} · {{ item.external_id }}
                    </a-option>
                  </a-select>
                </a-form-item>
              </a-grid-item>
            </a-grid>
            <a-grid v-if="jobForm.jenkinsfile_mode === 'existing'" :cols="2" :col-gap="16">
              <a-grid-item>
                <a-form-item label="环境清单路径">
                  <a-textarea v-model="jobForm.environment_paths_text" :auto-size="{ minRows: 3, maxRows: 6 }" />
                </a-form-item>
              </a-grid-item>
              <a-grid-item>
                <a-form-item label="固定 Jenkins 参数">
                  <a-textarea v-model="jobForm.parameters_text" :auto-size="{ minRows: 3, maxRows: 6 }" />
                </a-form-item>
              </a-grid-item>
            </a-grid>
          </a-collapse-item>
          <a-collapse-item v-if="jobForm.jenkinsfile_mode === 'existing'" key="parameters" header="高级 · Jenkinsfile 构建入口">
        <a-alert type="info" show-icon class="section-alert">推荐仅暴露“构建服务”和“业务代码分支”；环境、镜像、仓库和凭据等固定值由 Jenkinsfile 管理。</a-alert>
        <a-form-item label="简化构建入口"><a-switch v-model="jobForm.compact_parameters" /><template #extra>开启后 Jenkins 和平台都只显示服务与分支。</template></a-form-item>
        <template v-if="!jobForm.compact_parameters">
        <div class="parameter-heading"><div><strong>Jenkinsfile 可选参数</strong><p>参数名必须与 <code>params.xxx</code> 完全一致；选项参数会在构建时显示下拉框。</p></div><a-button size="small" type="outline" @click="addParameterDefinition"><icon-plus />添加参数</a-button></div>
        <a-table class="parameter-table" :data="jobForm.parameter_definitions" :pagination="false" row-key="_id" :bordered="{cell:true}">
          <template #columns>
            <a-table-column title="参数名" :width="150"><template #cell="{record}"><a-input v-model="record.name" placeholder="server" /></template></a-table-column>
            <a-table-column title="类型" :width="118"><template #cell="{record}"><a-select v-model="record.type" @change="normalizeParameter(record)"><a-option value="string">文本</a-option><a-option value="choice">选项</a-option><a-option value="number">数字</a-option><a-option value="boolean">布尔</a-option></a-select></template></a-table-column>
            <a-table-column title="可选值"><template #cell="{record}"><a-input-tag v-if="record.type === 'choice'" v-model="record.choices" placeholder="输入后回车，如 come-app-admin" @change="normalizeParameter(record)" /><span v-else class="muted-id">—</span></template></a-table-column>
            <a-table-column title="默认值" :width="180"><template #cell="{record}"><a-select v-if="record.type === 'choice'" v-model="record.default_value"><a-option v-for="choice in record.choices" :key="choice" :value="choice">{{ choice }}</a-option></a-select><a-radio-group v-else-if="record.type === 'boolean'" v-model="record.default_value" type="button"><a-radio value="true">是</a-radio><a-radio value="false">否</a-radio></a-radio-group><a-input v-else v-model="record.default_value" :placeholder="record.type === 'number' ? '1' : 'main'" /></template></a-table-column>
            <a-table-column title="必填" :width="70"><template #cell="{record}"><a-switch v-model="record.required" /></template></a-table-column>
            <a-table-column title="说明" :width="170"><template #cell="{record}"><a-input v-model="record.description" /></template></a-table-column>
            <a-table-column title="" :width="58"><template #cell="{rowIndex}"><a-button size="mini" status="danger" @click="removeParameterDefinition(rowIndex)">删除</a-button></template></a-table-column>
          </template>
          <template #empty><a-empty description="未定义可选参数；仅会传递平台内置参数" /></template>
        </a-table>
        </template>
          </a-collapse-item>
        </a-collapse>
      </a-form>
    </a-modal>

    <a-modal v-model:visible="webhookSetupVisible" title="GitLab Push 自动触发" width="720px" :footer="false" @cancel="closeWebhookSetup">
      <a-alert type="success" show-icon>在 GitLab 项目的 Settings → Webhooks 中填写以下信息，并只勾选 <strong>Push events</strong>。</a-alert>
      <a-form v-if="webhookSetupJob" :model="webhookSetupJob" layout="vertical" class="webhook-setup-form">
        <a-form-item label="URL"><a-input :model-value="jobWebhookURL(webhookSetupJob.key)" readonly><template #append><a-button type="text" @click="copyText(jobWebhookURL(webhookSetupJob.key), 'Webhook 地址')">复制</a-button></template></a-input></a-form-item>
        <a-form-item label="Secret Token" :extra="webhookSecret ? '该 Token 只显示这一次，请立即配置到 GitLab。' : '平台不回显已保存 Token；如果遗失，请重新生成。'"><a-input-password :model-value="webhookSecret || '已安全配置（不回显）'" readonly><template v-if="webhookSecret" #append><a-button type="text" @click="copyText(webhookSecret, 'Secret Token')">复制</a-button></template></a-input-password></a-form-item>
        <a-descriptions :column="2" bordered size="small"><a-descriptions-item label="触发事件">Push events</a-descriptions-item><a-descriptions-item label="监听分支">{{ webhookSetupJob.trigger_branch }}</a-descriptions-item><a-descriptions-item label="SSL 验证">开启</a-descriptions-item><a-descriptions-item label="重复事件">自动去重</a-descriptions-item></a-descriptions>
        <div class="webhook-actions"><a-popconfirm content="重新生成后，GitLab 中的旧 Token 会立即失效，确认继续？" @ok="rotateWebhookSecret(webhookSetupJob)"><a-button status="warning" :loading="webhookRotating">重新生成 Secret Token</a-button></a-popconfirm><a-button type="primary" @click="closeWebhookSetup">已完成配置</a-button></div>
      </a-form>
    </a-modal>

    <a-modal v-model:visible="deleteJobVisible" title="删除 CI/CD Job" width="600px" ok-text="确认删除" :ok-loading="deleteJobSaving" :ok-button-props="{ status: 'danger', disabled: deleteJobUsage.active_builds > 0 }" @before-ok="confirmDeleteJob">
      <a-alert v-if="deleteJobUsage.active_builds" type="error" show-icon>该 Job 仍有 {{ deleteJobUsage.active_builds }} 个排队中或运行中的构建，请先停止后再删除。</a-alert>
      <a-alert v-else type="warning" show-icon>删除平台 Job 后，{{ deleteJobUsage.historical_builds }} 条已完成构建历史会继续保留，但不再支持重试。</a-alert>
      <a-descriptions v-if="deleteJobTarget" :column="1" bordered size="small" style="margin-top:16px"><a-descriptions-item label="平台 Job">{{ deleteJobTarget.display_name }}（{{ deleteJobTarget.key }}）</a-descriptions-item><a-descriptions-item label="Jenkins Job">{{ deleteJobTarget.jenkins_job_name }}</a-descriptions-item><a-descriptions-item label="历史构建">{{ deleteJobUsage.total_builds }} 条</a-descriptions-item></a-descriptions>
      <div class="delete-remote-option"><div><strong>同时删除 Jenkins 中的 Job</strong><p>开启后会连接当前绑定的 Jenkins 删除远程 Job；关闭时只解除平台管理。</p></div><a-switch v-model="deleteRemoteJob" /></div>
    </a-modal>

    <a-modal v-model:visible="buildVisible" title="触发 Jenkins 构建" width="720px" :ok-loading="saving" ok-text="开始构建" @before-ok="triggerBuild"><a-alert type="info" show-icon>{{ isCompactJob(buildTarget) ? '只需选择构建服务；代码分支可选，留空时使用每个服务登记的默认分支。环境、镜像版本、仓库和凭据由 Jenkinsfile 自动处理。' : '构建将在当前环境绑定的 Jenkins 中执行。' }}</a-alert><a-form :model="buildForm" layout="vertical" style="margin-top:16px"><a-grid :cols="2" :col-gap="16"><a-grid-item v-if="!isCompactJob(buildTarget)"><a-form-item label="Job"><a-input :model-value="buildTarget?.display_name" disabled /></a-form-item></a-grid-item><a-grid-item v-if="!isCompactJob(buildTarget)"><a-form-item label="发布环境" required><a-select v-model="buildForm.environment" :disabled="buildEnvironments.length === 1"><a-option v-for="item in buildEnvironments" :key="item.environment" :value="item.environment">{{ item.display_name }}</a-option></a-select></a-form-item></a-grid-item><a-grid-item v-if="isCompactJob(buildTarget) || (!hasBuildParameter('branch') && !hasBuildParameter('GIT_BRANCH'))"><a-form-item label="代码分支" extra="留空则使用所选服务登记的默认分支。"><a-input v-model="buildForm.branch" placeholder="服务默认分支" allow-clear /></a-form-item></a-grid-item><a-grid-item v-if="!isCompactJob(buildTarget) && !hasBuildParameter('IMAGE_TAG')"><a-form-item label="镜像标签"><a-input v-model="buildForm.image_tag" /></a-form-item></a-grid-item></a-grid>
      <a-form-item label="本次构建服务" required extra="平台支持多选；每个服务会拆成独立 Jenkins 构建，并显式传入该服务登记的默认分支。"><a-select v-model="buildForm.services" multiple allow-search><a-option v-for="service in jobServices(buildTarget)" :key="service" :value="service">{{ serviceName(service) }} · {{ service }} · {{ serviceDefaultBranch(service) }}</a-option></a-select></a-form-item>
      <template v-if="!isCompactJob(buildTarget)"><a-divider v-if="buildTarget?.parameter_definitions?.length" orientation="left">Jenkinsfile 参数</a-divider><a-grid v-if="buildTarget?.parameter_definitions?.length" :cols="2" :col-gap="16"><a-grid-item v-for="item in buildTarget.parameter_definitions" :key="item.name"><a-form-item :label="item.name" :required="item.required" :extra="item.description"><a-select v-if="item.type === 'choice'" v-model="buildForm.parameters[item.name]"><a-option v-for="choice in item.choices" :key="choice" :value="choice">{{ choice }}</a-option></a-select><a-radio-group v-else-if="item.type === 'boolean'" v-model="buildForm.parameters[item.name]" type="button"><a-radio value="true">是</a-radio><a-radio value="false">否</a-radio></a-radio-group><a-input v-else v-model="buildForm.parameters[item.name]" :input-attrs="item.type === 'number' ? { inputmode: 'decimal' } : undefined" /></a-form-item></a-grid-item></a-grid>
      <a-form-item label="高级附加参数" extra="每行一个 KEY=value；同名时会覆盖上方值，不要填写密码。"><a-textarea v-model="buildForm.parameters_text" :auto-size="{ minRows: 3, maxRows: 7 }" /></a-form-item></template></a-form></a-modal>

    <a-drawer v-model:visible="jenkinsfileImportVisible" width="820px" title="智能导入 Jenkinsfile" :mask-closable="false" @cancel="clearJenkinsfileImport">
      <a-alert type="info" show-icon>只做静态解析，不执行 Groovy，也不会保存 Jenkinsfile 或其中的敏感值。解析完成后可先预览，再应用到当前 Job。</a-alert>
      <a-form :model="{ content: jenkinsfileContent }" layout="vertical" style="margin-top:16px"><a-form-item label="Jenkinsfile 内容" extra="支持 Declarative Pipeline 的 choice、string、booleanParam、stage、环境常量、Git 仓库和 Credential ID 识别。"><a-textarea v-model="jenkinsfileContent" placeholder="粘贴 Jenkinsfile 内容" :auto-size="{ minRows: 12, maxRows: 22 }" /></a-form-item><a-button type="primary" long :loading="analyzingJenkinsfile" @click="analyzeJenkinsfile">开始解析</a-button></a-form>
      <template v-if="jenkinsfileAnalysis">
        <a-divider orientation="left">解析结果</a-divider>
        <div class="analysis-summary"><div><strong>{{ jenkinsfileAnalysis.services?.length || 0 }}</strong><span>可选服务</span></div><div><strong>{{ jenkinsfileAnalysis.parameters?.length || 0 }}</strong><span>构建参数</span></div><div><strong>{{ jenkinsfileAnalysis.stages?.length || 0 }}</strong><span>流水线阶段</span></div><div><strong>{{ (jenkinsfileAnalysis.language || '—').toUpperCase() }}</strong><span>语言 {{ jenkinsfileAnalysis.runtime_version || '' }}</span></div></div>
        <section v-if="jenkinsfileAnalysis.services?.length" class="analysis-section"><strong>服务选择</strong><div class="tag-list"><a-tag v-for="item in jenkinsfileAnalysis.services" :key="item" color="arcoblue">{{ item }}</a-tag></div></section>
        <section v-if="jenkinsfileAnalysis.parameters?.length" class="analysis-section"><strong>自动生成的构建控件</strong><div class="analysis-list"><div v-for="item in jenkinsfileAnalysis.parameters" :key="item.name"><span>{{ item.name }}</span><a-tag size="small">{{ parameterTypeName(item.type) }}</a-tag><small v-if="item.type === 'choice'">{{ item.choices.length }} 个选项</small><small v-else>默认：{{ item.default_value || '空' }}</small></div></div></section>
        <section v-if="jenkinsfileAnalysis.repositories?.length" class="analysis-section"><strong>检测到的仓库</strong><div class="analysis-list"><div v-for="item in jenkinsfileAnalysis.repositories" :key="`${item.role}-${item.url}`"><a-tag size="small" color="purple">{{ repositoryRoleName(item.role) }}</a-tag><span class="ellipsis">{{ compactRepo(item.url) }}</span><small>{{ item.branch || '默认分支' }}<template v-if="item.path"> / {{ item.path }}</template></small></div></div></section>
        <section v-if="jenkinsfileAnalysis.credential_references?.length" class="analysis-section"><strong>Jenkins 凭据检查</strong><div class="analysis-list"><div v-for="item in jenkinsfileAnalysis.credential_references" :key="`${item.variable}-${item.external_id}`"><span>{{ item.variable }}</span><code>{{ item.external_id || '需要迁移为 Jenkins 凭据' }}</code><a-tag size="small" :color="matchingCredential(item) ? 'green' : 'orange'">{{ matchingCredential(item) ? '当前环境已匹配' : '需要配置' }}</a-tag></div></div></section>
        <a-alert v-for="warning in jenkinsfileAnalysis.warnings" :key="warning" type="warning" show-icon class="analysis-warning">{{ warning }}</a-alert>
      </template>
      <template #footer><a-space><a-button @click="clearJenkinsfileImport">取消</a-button><a-button type="primary" :disabled="!jenkinsfileAnalysis" @click="applyJenkinsfileImport">应用到当前 Job</a-button></a-space></template>
    </a-drawer>

    <a-drawer v-model:visible="logsVisible" width="980px" :footer="false" title="构建实时进度" @cancel="stopLogPolling">
      <template v-if="logBuild">
        <div class="build-live-header">
          <div class="build-live-identity"><div class="live-title"><strong>{{ jobName(logBuild.job_key) }}</strong><span>#{{ logBuild.build_number || '排队中' }}</span></div><div class="live-subtitle"><span>{{ store.environmentLabel(logBuild.environment) }}</span><span>·</span><span>{{ logBuild.requested_by || '系统' }}</span><div class="live-status" :class="`status-${logBuild.status}`"><span class="status-dot" /><a-tag :color="buildColor(logBuild.status)">{{ buildName(logBuild.status) }}</a-tag></div><span v-if="logIsLive" class="live-stream"><i />实时更新</span></div></div>
          <a-space><a-button size="small" @click="resetLogs"><icon-refresh />重新加载</a-button><a :href="logBuild.build_url" target="_blank" rel="noopener noreferrer"><a-button v-if="logBuild.build_url" size="small">打开 Jenkins</a-button></a></a-space>
        </div>

        <section class="build-live-overview" :class="`overview-${logBuild.status}`">
          <div class="live-progress-number"><strong>{{ buildProgress(logBuild) }}</strong><span>%</span></div>
          <div class="live-progress-main"><div class="live-progress-heading"><strong>{{ logBuild.current_stage || buildName(logBuild.status) }}</strong><small>{{ logIsLive ? 'Jenkins 正在持续返回最新状态' : '本次构建状态已固定' }}</small></div><div class="progress-shell live-progress-shell" :class="{ 'is-running': logIsLive }"><a-progress :percent="buildProgress(logBuild) / 100" :status="logBuild.status === 'failed' ? 'danger' : logBuild.status === 'succeeded' ? 'success' : 'normal'" :show-text="false" /></div></div>
          <div class="stage-stats"><div><strong>{{ logStageStats.total }}</strong><span>全部阶段</span></div><div class="stat-success"><strong>{{ logStageStats.succeeded }}</strong><span>已成功</span></div><div v-if="logStageStats.running" class="stat-running"><strong>{{ logStageStats.running }}</strong><span>执行中</span></div><div v-if="logStageStats.pending" class="stat-pending"><strong>{{ logStageStats.pending }}</strong><span>等待中</span></div><div v-if="logStageStats.failed" class="stat-failed"><strong>{{ logStageStats.failed }}</strong><span>失败</span></div></div>
        </section>

        <a-alert v-if="logBuild.error" type="error" show-icon class="build-error"><strong>失败原因：</strong>{{ logBuild.error }}</a-alert>
        <div v-if="logBuild.stages?.length" class="stage-grid live-stage-grid"><div v-for="(stage,index) in logBuild.stages" :key="`${stage.service}-${stage.name}`" class="stage-item" :class="`stage-${stage.status}`"><div class="stage-sequence"><span>{{ index + 1 }}</span><i /></div><div class="stage-content"><div><strong>{{ stage.name }}</strong><a-tag size="small" :color="stageColor(stage.status)">{{ stageStatusName(stage.status) }}</a-tag></div><small>{{ stage.service ? serviceName(stage.service) : '流水线' }}<template v-if="stage.duration_ms"> · {{ formatDuration(stage.duration_ms) }}</template></small></div></div></div>
        <div v-else class="stage-waiting"><span class="waiting-radar" /><div><strong>{{ logBuild.status === 'queued' ? '等待 Jenkins 执行器' : '正在识别流水线阶段' }}</strong><p>开始执行后会在这里动态展示检出、构建镜像和部署等阶段。</p></div></div>

        <section class="live-log-panel"><div class="live-log-toolbar"><div><strong>流水线输出</strong><span v-if="logIsLive" class="live-stream"><i />日志流连接中</span></div><a-space><span class="auto-scroll-label">自动跟随</span><a-switch v-model="autoScroll" size="small" /><a-button size="mini" @click="scrollLogsToBottom">回到底部</a-button></a-space></div><a-tabs v-model:active-key="logTab" type="rounded"><a-tab-pane key="deployment"><template #title>部署日志 <a-badge :count="deploymentLogLines" :max-count="999" /></template><pre class="cicd-terminal deployment-terminal">{{ deploymentLogText || '当前还没有部署阶段日志。构建、镜像推送和清单应用后会显示在这里。' }}</pre></a-tab-pane><a-tab-pane key="full"><template #title>完整 Jenkins 日志 <a-badge :count="fullLogLines" :max-count="999" /></template><pre ref="terminal" class="cicd-terminal">{{ logText || (logBuild.status === 'queued' ? '正在等待 Jenkins 分配执行器…' : '正在等待日志输出…') }}</pre></a-tab-pane></a-tabs></section>
      </template>
    </a-drawer>
  </div>
</template>

<script setup lang="ts">
import { computed, nextTick, onMounted, onUnmounted, reactive, ref, watch } from 'vue';
import { useRouter } from 'vue-router';
import { Message } from '@arco-design/web-vue';
import { IconPlus, IconRefresh } from '@arco-design/web-vue/es/icon';
import { api } from '@/services/api';
import { createCICDJobPayload, createSingleServiceBuildRequests, type ParameterDefinitionDraft as ParameterDefinition } from '@/services/cicdPayload';
import { copyToClipboard } from '@/services/clipboard';
import { usePlatformStore } from '@/stores/platform';
import DeliveryRepositoryPanel from '@/components/cicd/DeliveryRepositoryPanel.vue';
import ServiceCatalogPanel from '@/components/cicd/ServiceCatalogPanel.vue';

interface Connection { key:string; display_name:string; base_url:string; username:string; configured:boolean; connection_mode:string; environment_key?:string; jenkins_version?:string; last_check_status?:string; last_check_error?:string }
interface Credential { key:string; environment_key:string; connection_key:string; display_name:string; kind:string; external_id:string; description?:string; configured:boolean; sync_status?:string; sync_error?:string }
interface Repository { key:string; display_name:string; provider:'gitlab'|'generic_git'; purpose:'jenkinsfile'|'manifest'|'source'|'general'; clone_url:string; default_branch:string; default_path?:string; description?:string }
interface ServiceSpec { key:string; display_name:string; workload_type?:'backend'|'frontend'; language:'go'|'java'|'node'; runtime_version:string; source_repository:string; source_branch:string; dockerfile_source?:'platform'|'source'; dockerfile?:string; dockerfile_content?:string; dockerfile_contents?:Record<string,string>; namespace?:string }
interface Job { key:string; environment_key:string; display_name:string; service_name:string; service_keys?:string[]; language:string; jenkinsfile_mode?:'generated'|'existing'; execution_mode?:'serial'|'parallel'; failure_policy?:'stop'|'continue'; compact_parameters?:boolean; connection_key:string; jenkins_job_name:string; enabled:boolean; trigger_mode?:'manual'|'gitlab_push'; trigger_branch?:string; webhook_configured?:boolean; jenkinsfile_repository?:string; jenkinsfile_repo:string; jenkinsfile_branch:string; jenkinsfile_path:string; jenkinsfile_content?:string; jenkinsfile_credential?:string; source_repository?:string; source_repo?:string; manifest_repository?:string; manifest_repo:string; manifest_branch:string; manifest_path:string; manifest_credential?:string; environment_paths?:Record<string,string>; build_command?:string; runtime_version?:string; parameters?:Record<string,string>; parameter_definitions?:ParameterDefinition[]; sync_status?:string; sync_error?:string }
interface BuildStage { name:string; service?:string; kind?:string; status:string; duration_ms?:number }
interface Build { id:string; job_key:string; environment:string; requested_by:string; status:string; result?:string; build_number:number; build_url?:string; error?:string; created_at:string; services?:string[]; progress:number; current_stage?:string; stages?:BuildStage[] }
interface JobBuildUsage { total_builds:number; active_builds:number; historical_builds:number }
interface JobDeletionResult { job_key:string; jenkins_job_name:string; remote_deletion_requested:boolean; remote_deleted:boolean; remote_already_missing:boolean; historical_builds_retained:number }
interface ManagedJenkins { project:string; environment:string; enabled:boolean; deployment_status:string; deployment_detail?:string; namespace?:string; service_name?:string; service_port?:number; internal_url?:string; external_url?:string; connection_mode?:string; connection_key?:string; connected:boolean; connection_healthy:boolean; can_connect:boolean; reason?:string }
interface NotificationChannel { name:string; type:'lark'; environment:string; configured:boolean }
interface JenkinsfileRepositoryHint { role:string; url:string; branch?:string; path?:string }
interface JenkinsfileCredentialReference { variable:string; external_id?:string; suggested_kind:string; usage:string; hardcoded:boolean }
interface JenkinsfileJobSuggestion { display_name?:string; service_name?:string; language?:string; runtime_version?:string; manifest_repo?:string; manifest_branch?:string; manifest_path?:string }
interface JenkinsfileAnalysis { language?:string; runtime_version?:string; service_parameter?:string; services?:string[]; parameters?:ParameterDefinition[]; stages?:string[]; repositories?:JenkinsfileRepositoryHint[]; credential_references?:JenkinsfileCredentialReference[]; settings?:Record<string,string>; sensitive_variables?:string[]; warnings?:string[]; suggestion:JenkinsfileJobSuggestion }

const store=usePlatformStore();const router=useRouter();const activeTab=ref('delivery');const loading=ref(false);const loadingBuilds=ref(false);const saving=ref(false);const busyKey=ref('');
const connections=ref<Connection[]>([]);const credentials=ref<Credential[]>([]);const repositories=ref<Repository[]>([]);const deliveryServices=ref<ServiceSpec[]>([]);const jobs=ref<Job[]>([]);const builds=ref<Build[]>([]);
const notificationChannels=ref<NotificationChannel[]>([]);const notificationChannelsLoading=ref(false);const credentialReturnToJob=ref(false);
const connectionVisible=ref(false),credentialVisible=ref(false),repositoryVisible=ref(false),jobVisible=ref(false),buildVisible=ref(false),logsVisible=ref(false),jenkinsfileImportVisible=ref(false),deleteJobVisible=ref(false),webhookSetupVisible=ref(false);
const managedJenkins=ref<ManagedJenkins|null>(null);const integrationLoading=ref(false);const managedConnectVisible=ref(false);const managedConnecting=ref(false);const managedPassword=ref('');
const editingConnection=ref(false),editingCredential=ref(false),editingRepository=ref(false),editingJob=ref(false);const buildTarget=ref<Job|null>(null);const logBuild=ref<Build|null>(null);const logText=ref('');const deploymentLogText=ref('');const logTab=ref('deployment');const logOffset=ref(0);const terminal=ref<HTMLElement>();const autoScroll=ref(true);let pollTimer=0;let buildListPollTimer=0;let generation=0;let logPollingErrorNotified=false;
const BUILD_LIST_POLL_MS=2000;const BUILD_LIST_IDLE_POLL_MS=5000;const LOG_POLL_MS=1200;const LOG_RETRY_MS=2500;
const PLATFORM_MANAGED_CREDENTIAL='__platform_managed__';
const jenkinsfileContent=ref('');const jenkinsfileAnalysis=ref<JenkinsfileAnalysis|null>(null);const analyzingJenkinsfile=ref(false);
const deleteJobTarget=ref<Job|null>(null);const deleteJobUsage=reactive<JobBuildUsage>({total_builds:0,active_builds:0,historical_builds:0});const deleteRemoteJob=ref(true);const deleteJobSaving=ref(false);
const webhookSetupJob=ref<Job|null>(null);const webhookSecret=ref('');const webhookRotating=ref(false);
const connectionForm=reactive({key:'',environment_key:'',display_name:'',base_url:'',username:'',api_token:'',allow_insecure_http:false});
const connectionKeyPattern=/^[a-z0-9][a-z0-9-]{0,62}$/;
const credentialForm=reactive({key:'',environment_key:'',connection_key:'',display_name:'',kind:'gitlab_token',external_id:'',description:'',username:'oauth2',password:'',secret_text:'',private_key:'',passphrase:''});
const repositoryForm=reactive({key:'',display_name:'',provider:'gitlab' as 'gitlab'|'generic_git',purpose:'general' as Repository['purpose'],clone_url:'',default_branch:'main',default_path:'',description:''});
const jobForm=reactive({key:'',environment_key:'',display_name:'',service_name:'',service_keys:[] as string[],language:'mixed',jenkinsfile_mode:'generated' as 'generated'|'existing',execution_mode:'serial' as 'serial'|'parallel',failure_policy:'stop' as 'stop'|'continue',compact_parameters:true,connection_key:'',jenkins_job_name:'',enabled:true,trigger_mode:'manual' as 'manual'|'gitlab_push',trigger_branch:'main',delivery_repository_mode:'unified' as 'unified'|'separate',delivery_repository_key:'',jenkinsfile_repository:'',jenkinsfile_repo:'',jenkinsfile_branch:'main',jenkinsfile_path:'',jenkinsfile_content:'',jenkinsfile_credential:'',source_repository:'',source_repo:'',manifest_repository:'',manifest_repo:'',manifest_branch:'main',manifest_path:'',manifest_credential:'',environment_paths_text:'',build_command:'',runtime_version:'',parameters_text:'',agent_mode:'kubernetes' as 'kubernetes'|'node',kubernetes_service_account:'jenkins',agent_label:'master',aws_profile:'',pipeline_timeout_minutes:30,deploy_verify_mode:'rollout' as 'rollout'|'apply',rollout_timeout_minutes:5,rollback_on_failure:false,telegram_credential_id:'',lark_alert_channel:'',lark_credential_id:'',parameter_definitions:[] as ParameterDefinition[]});
const buildForm=reactive({environment:'',branch:'',image_tag:'',services:[] as string[],parameters:{} as Record<string,string>,parameters_text:''});
const runningCount=computed(()=>builds.value.filter(item=>['queued','running'].includes(item.status)).length);
const connectionUsesInsecureHTTP=computed(()=>{try{const target=new URL(connectionForm.base_url.trim());return target.protocol==='http:'&&!['localhost','127.0.0.1','[::1]'].includes(target.hostname)}catch{return false}});
const environmentConnections=computed(()=>connections.value.filter(item=>item.environment_key===store.currentEnvironmentKey));
const environmentCredentials=computed(()=>credentials.value.filter(item=>item.environment_key===store.currentEnvironmentKey&&environmentConnections.value.some(connection=>connection.key===item.connection_key)));
const scopedJobs=computed(()=>jobs.value.filter(item=>item.environment_key?item.environment_key===store.currentEnvironmentKey:environmentConnections.value.some(connection=>connection.key===item.connection_key)));
const credentialsForConnection=computed(()=>credentials.value.filter(item=>item.connection_key===jobForm.connection_key&&item.sync_status==='ready'));
const selectedJobConnection=computed(()=>environmentConnections.value.find(item=>item.key===jobForm.connection_key));
const selectedJobServices=computed(()=>jobForm.service_keys.map(key=>deliveryServices.value.find(item=>item.key===key)).filter((item):item is ServiceSpec=>Boolean(item)));
const jobSummaryReady=computed(()=>Boolean(selectedJobConnection.value&&selectedJobServices.value.length&&jobForm.jenkinsfile_repo&&jobForm.jenkinsfile_path&&jobForm.manifest_repo&&jobForm.manifest_path));
const secretTextCredentialsForConnection=computed(()=>credentialsForConnection.value.filter(item=>item.kind==='secret_text'||item.kind==='existing'));
const jenkinsfileRepositories=computed(()=>repositories.value.filter(item=>item.purpose==='jenkinsfile'||item.purpose==='general'));
const manifestRepositories=computed(()=>repositories.value.filter(item=>item.purpose==='manifest'||item.purpose==='general'));
const deliveryRepositories=computed(()=>repositories.value.filter(item=>item.key==='ops-delivery'));
const sourceRepositories=computed(()=>repositories.value.filter(item=>item.purpose==='source'||item.purpose==='general'));
const triggerBranchOptions=computed(()=>[...new Set(['main',...jobForm.service_keys.map(key=>deliveryServices.value.find(item=>item.key===key)?.source_branch||'')])].filter(Boolean));
const currentJobWebhookConfigured=computed(()=>jobs.value.find(item=>item.key===jobForm.key)?.webhook_configured===true);
const jobServicesPath=computed(()=>{const value=jobForm.jenkinsfile_path||'';const index=value.lastIndexOf('/');return`${index>=0?value.slice(0,index+1):''}services.groovy`});
const buildEnvironments=computed(()=>{const environment=buildTarget.value?.environment_key;return(store.currentProject?.environments||[]).filter(item=>!environment||item.environment===environment)});
const logIsLive=computed(()=>Boolean(logBuild.value&&['queued','running'].includes(logBuild.value.status)));
const logStageStats=computed(()=>{const stages=logBuild.value?.stages||[];const count=(statuses:string[])=>stages.filter(item=>statuses.includes(item.status)).length;return{total:stages.length,succeeded:count(['succeeded']),running:count(['running']),pending:count(['pending','paused']),failed:count(['failed'])}});
const deploymentLogLines=computed(()=>deploymentLogText.value?deploymentLogText.value.split('\n').filter(Boolean).length:0);
const fullLogLines=computed(()=>logText.value?logText.value.split('\n').filter(Boolean).length:0);
const scopePath=()=>`/api/projects/${encodeURIComponent(store.currentProjectKey)}/cicd`;
const environmentScopePath=()=>`/api/projects/${encodeURIComponent(store.currentProjectKey)}/environments/${encodeURIComponent(store.currentEnvironmentKey)}/cicd`;
const environmentQuery=()=>`environment=${encodeURIComponent(store.currentEnvironmentKey)}`;
const hasValidScope=()=>Boolean(store.currentProject?.environments.some(item=>item.environment===store.currentEnvironmentKey));
const parseMap=(value:string)=>Object.fromEntries(value.split('\n').map(line=>line.trim()).filter(Boolean).map(line=>{const index=line.indexOf('=');return index<1?[line,'']:[line.slice(0,index).trim(),line.slice(index+1).trim()]}).filter(([key])=>key));
const mapText=(value?:Record<string,string>)=>Object.entries(value||{}).map(([key,item])=>`${key}=${item}`).join('\n');
const loadIntegration=async()=>{if(!hasValidScope()){managedJenkins.value=null;return}const revision=store.scopeRevision;integrationLoading.value=true;try{const result=await api<ManagedJenkins>(`${environmentScopePath()}/jenkins`);if(revision===store.scopeRevision)managedJenkins.value=result}catch(error:any){if(revision===store.scopeRevision){managedJenkins.value=null;Message.error(error.message)}}finally{if(revision===store.scopeRevision)integrationLoading.value=false}};
const loadAll=async()=>{if(!hasValidScope()){connections.value=[];credentials.value=[];repositories.value=[];deliveryServices.value=[];jobs.value=[];builds.value=[];notificationChannels.value=[];managedJenkins.value=null;return}const revision=store.scopeRevision;loading.value=true;try{const [c,r,repos,j,delivery]=await Promise.all([api<{connections:Connection[]}>(`${environmentScopePath()}/connections`),api<{credentials:Credential[]}>(`${environmentScopePath()}/credentials`),api<{repositories:Repository[]}>(`${scopePath()}/repositories`),api<{jobs:Job[]}>(`${scopePath()}/jobs`),api<{services?:ServiceSpec[]}>(`${scopePath()}/delivery`)]);if(revision!==store.scopeRevision)return;connections.value=c.connections;credentials.value=r.credentials;repositories.value=repos.repositories;deliveryServices.value=delivery.services||[];jobs.value=j.jobs;await Promise.all([loadBuilds(false),loadIntegration()])}catch(error:any){if(revision===store.scopeRevision)Message.error(error.message)}finally{if(revision===store.scopeRevision)loading.value=false}};
async function loadNotificationChannels(){if(!store.currentProjectKey||!store.currentEnvironmentKey||!jobForm.connection_key){notificationChannels.value=[];return}const revision=store.scopeRevision;notificationChannelsLoading.value=true;try{const query=new URLSearchParams({connection:jobForm.connection_key,environment:store.currentEnvironmentKey});const result=await api<{channels:NotificationChannel[]}>(`${scopePath()}/notification-channels?${query}`);if(revision===store.scopeRevision)notificationChannels.value=result.channels||[]}catch(error:any){if(revision===store.scopeRevision){notificationChannels.value=[];Message.error(`无法读取当前环境告警通道：${error.message}`)}}finally{if(revision===store.scopeRevision)notificationChannelsLoading.value=false}}
const loadBuilds=async(notify=true)=>{if(!store.currentProjectKey)return;const revision=store.scopeRevision;if(notify)loadingBuilds.value=true;try{const env=store.currentEnvironmentKey?`?environment=${encodeURIComponent(store.currentEnvironmentKey)}`:'';const response=await api<{builds:Build[]}>(`${scopePath()}/builds${env}`);if(revision===store.scopeRevision)builds.value=response.builds}catch(error:any){if(notify&&revision===store.scopeRevision)Message.error(error.message)}finally{if(notify&&revision===store.scopeRevision)loadingBuilds.value=false}};
function stopBuildListPolling(){window.clearTimeout(buildListPollTimer);buildListPollTimer=0}
function scheduleBuildListPolling(){stopBuildListPolling();if(activeTab.value!=='builds'||!store.currentProjectKey||logsVisible.value)return;const delay=runningCount.value>0?BUILD_LIST_POLL_MS:BUILD_LIST_IDLE_POLL_MS;buildListPollTimer=window.setTimeout(async()=>{if(document.visibilityState!=='hidden'&&!loadingBuilds.value)await loadBuilds(false);scheduleBuildListPolling()},delay)}
function handleVisibilityChange(){if(document.visibilityState!=='visible')return;if(activeTab.value==='builds'&&!logsVisible.value)void loadBuilds(false);scheduleBuildListPolling();if(logsVisible.value&&logBuild.value&&['queued','running'].includes(logBuild.value.status)&&!pollTimer){const current=generation;void pollLogs(current)}}
watch(()=>store.scopeRevision,()=>{stopLogPolling();stopBuildListPolling();void loadAll()},{immediate:true});watch(activeTab,async key=>{if(key==='builds'&&!logsVisible.value)await loadBuilds(false);scheduleBuildListPolling()});watch(runningCount,scheduleBuildListPolling);watch(logsVisible,visible=>{if(visible)stopBuildListPolling();else scheduleBuildListPolling()});watch(()=>jobForm.jenkinsfile_mode,()=>{if(jobVisible.value)applyManagedDeliveryPaths()});watch(()=>jobForm.key,()=>{if(jobVisible.value&&!editingJob.value&&jobForm.jenkinsfile_mode==='generated')jobForm.jenkinsfile_path=`environments/${jobForm.environment_key||'dev'}/pipelines/${jobForm.key||'job'}/Jenkinsfile`});onMounted(()=>document.addEventListener('visibilitychange',handleVisibilityChange));onUnmounted(()=>{stopLogPolling();stopBuildListPolling();document.removeEventListener('visibilitychange',handleVisibilityChange)});

function openConnection(item?:Connection){const environment=store.currentEnvironmentKey;editingConnection.value=Boolean(item);Object.assign(connectionForm,{key:item?.key||`${environment}-jenkins`,environment_key:environment,display_name:item?.display_name||`${store.environmentLabel(environment)} Jenkins`,base_url:item?.base_url||'',username:item?.username||'',api_token:'',allow_insecure_http:Boolean(item?.base_url?.startsWith('http://'))});connectionVisible.value=true}
function validateConnectionForm(){
  connectionForm.key=connectionForm.key.trim().toLowerCase();connectionForm.display_name=connectionForm.display_name.trim();connectionForm.base_url=connectionForm.base_url.trim();connectionForm.username=connectionForm.username.trim();
  if(!connectionForm.key){Message.warning('请填写连接标识');return false}
  if(!connectionKeyPattern.test(connectionForm.key)){Message.warning('连接标识只能使用小写字母、数字和连字符，且必须以字母或数字开头');return false}
  if(!connectionForm.display_name){Message.warning('请填写 Jenkins 显示名称');return false}
  if(!connectionForm.base_url){Message.warning('请填写 Jenkins 地址');return false}
  let target:URL;try{target=new URL(connectionForm.base_url)}catch{Message.warning('Jenkins 地址必须是完整 URL，例如 https://jenkins.example.com');return false}
  if(!['https:','http:'].includes(target.protocol)||!target.hostname||target.username||target.password||target.search||target.hash){Message.warning('Jenkins 地址格式不合法，不能包含账号密码、查询参数或锚点');return false}
  const localHosts=new Set(['localhost','127.0.0.1','[::1]']);
  if(target.protocol==='http:'&&!localHosts.has(target.hostname)&&!connectionForm.allow_insecure_http){Message.warning('使用外部 HTTP Jenkins 前，请开启“HTTP 明文连接”确认开关');return false}
  if(!connectionForm.username){Message.warning('请填写 Jenkins 用户名');return false}
  if(!editingConnection.value&&!connectionForm.api_token.trim()){Message.warning('首次添加 Jenkins 连接必须填写 API Token');return false}
  return true
}
async function saveConnection(){if(!validateConnectionForm())return false;saving.value=true;try{const base=editingConnection.value?`${environmentScopePath()}/connections/${encodeURIComponent(connectionForm.key)}`:`${environmentScopePath()}/connections`;await api(base,{method:editingConnection.value?'PUT':'POST',body:JSON.stringify(connectionForm)});connectionForm.api_token='';await loadAll();Message.success(`${store.environmentLabel(store.currentEnvironmentKey)} Jenkins 连接已独立保存`);return true}catch(error:any){Message.error(error.message);return false}finally{saving.value=false}}
async function testConnection(item:Connection){busyKey.value=`connection-test-${item.key}`;try{await api(`${environmentScopePath()}/connections/${encodeURIComponent(item.key)}/test`,{method:'POST'});await loadAll();Message.success('当前环境 Jenkins 连接正常')}catch(error:any){Message.error(error.message);await loadAll()}finally{busyKey.value=''}}
async function deleteConnection(item:Connection){try{await api(`${environmentScopePath()}/connections/${encodeURIComponent(item.key)}`,{method:'DELETE'});await loadAll();Message.success('当前环境连接已删除')}catch(error:any){Message.error(error.message)}}
function openManagedConnect(){managedPassword.value='';managedConnectVisible.value=true}
async function connectManagedJenkins(){if(!managedPassword.value){Message.warning('请输入当前平台登录密码');return false}managedConnecting.value=true;try{await api(`/api/projects/${encodeURIComponent(store.currentProjectKey)}/environments/${encodeURIComponent(store.currentEnvironmentKey)}/cicd/jenkins/connect`,{method:'POST',body:JSON.stringify({password:managedPassword.value})});managedPassword.value='';await loadAll();Message.success('当前环境 Jenkins 已对接，可以创建并同步 Job');return true}catch(error:any){managedPassword.value='';Message.error(error.message);return false}finally{managedConnecting.value=false}}

function openCredential(item?:Credential){const environment=store.currentEnvironmentKey;credentialReturnToJob.value=false;editingCredential.value=Boolean(item);Object.assign(credentialForm,{key:item?.key||`${environment}-gitlab-read`,environment_key:environment,connection_key:item?.connection_key||environmentConnections.value[0]?.key||'',display_name:item?.display_name||`${store.environmentLabel(environment)} GitLab 只读凭据`,kind:item?.kind||'gitlab_token',external_id:item?.external_id||'',description:item?.description||'',username:item?'':'oauth2',password:'',secret_text:'',private_key:'',passphrase:''});credentialVisible.value=true}
function openJobGitLabCredential(){const slug=(value:string,max:number)=>value.toLowerCase().replace(/[^a-z0-9-]+/g,'-').replace(/^-+|-+$/g,'').slice(0,max);const base=slug(jobForm.key||'job',32)||'job';const connection=slug(jobForm.connection_key||'jenkins',18)||'jenkins';const key=`gitlab-${base}-${connection}`.slice(0,63).replace(/-+$/,'');const existing=credentials.value.find(item=>item.key===key&&item.connection_key===jobForm.connection_key);openCredential(existing);credentialReturnToJob.value=true;Object.assign(credentialForm,{key:existing?.key||key,connection_key:jobForm.connection_key,display_name:existing?.display_name||`${jobForm.display_name||jobForm.key||'当前 Job'} GitLab 仓库凭据`,kind:'gitlab_token',external_id:existing?.external_id||'',description:existing?.description||'用于 Jenkins 读取 Jenkinsfile 与部署清单仓库',username:existing?'':'oauth2',password:'',secret_text:'',private_key:'',passphrase:''})}
async function saveCredential(){if(!credentialForm.key.trim()||!credentialForm.display_name.trim()||!credentialForm.connection_key){Message.warning('请补全凭据信息');return false}if(credentialForm.kind==='gitlab_token'&&!credentialForm.username.trim()){Message.warning('请填写 GitLab 用户名；使用 Personal Access Token 时通常可填 oauth2');return false}if(credentialForm.kind==='gitlab_token'&&!editingCredential.value&&!credentialForm.password.trim()){Message.warning('请填写 GitLab Personal Access Token');return false}const returnToJob=credentialReturnToJob.value;saving.value=true;try{const base=editingCredential.value?`${environmentScopePath()}/credentials/${encodeURIComponent(credentialForm.key)}`:`${environmentScopePath()}/credentials`;const saved=await api<Credential>(base,{method:editingCredential.value?'PUT':'POST',body:JSON.stringify(credentialForm)});Object.assign(credentialForm,{external_id:saved.external_id,password:'',secret_text:'',private_key:'',passphrase:''});if(saved.kind!=='existing'){try{await api<Credential>(`${environmentScopePath()}/credentials/${encodeURIComponent(saved.key)}/sync`,{method:'POST'})}catch(error:any){await loadAll();Message.error(`凭据已保存，Credential ID：${saved.external_id}；同步 Jenkins 失败：${error.message}`);return false}}await loadAll();if(returnToJob)selectSharedJobCredential(saved.key);credentialReturnToJob.value=false;Message.success(returnToJob?`当前环境 GitLab Token 已写入 Jenkins 并绑定 Job：${saved.external_id}`:`当前环境凭据已同步，Credential ID：${saved.external_id}`);return true}catch(error:any){Message.error(error.message);return false}finally{saving.value=false}}
async function syncCredential(item:Credential){busyKey.value=`credential-sync-${item.key}`;try{const synced=await api<Credential>(`${environmentScopePath()}/credentials/${encodeURIComponent(item.key)}/sync`,{method:'POST'});await loadAll();Message.success(`当前环境凭据已同步，Credential ID：${synced.external_id}`)}catch(error:any){Message.error(error.message);await loadAll()}finally{busyKey.value=''}}
async function deleteCredential(item:Credential){try{await api(`${environmentScopePath()}/credentials/${encodeURIComponent(item.key)}`,{method:'DELETE'});await loadAll();Message.success('当前环境凭据已删除')}catch(error:any){Message.error(error.message)}}
async function copyText(value:string,label:string){try{await copyToClipboard(value);Message.success(`已复制${label}`)}catch{Message.error(`复制失败，请手动选择${label}`)}}
async function copyCredentialID(value:string){await copyText(value,'Credential ID')}
async function copyCredentialReference(value:string){await copyText(`credentialsId: '${value}'`,'Jenkinsfile 凭据引用')}

function openRepository(item?:Repository){editingRepository.value=Boolean(item);Object.assign(repositoryForm,{key:item?.key||'',display_name:item?.display_name||'',provider:item?.provider||'gitlab',purpose:item?.purpose||'general',clone_url:item?.clone_url||'',default_branch:item?.default_branch||'main',default_path:item?.default_path||'',description:item?.description||''});repositoryVisible.value=true}
async function saveRepository(){if(!repositoryForm.key.trim()||!repositoryForm.display_name.trim()||!repositoryForm.clone_url.trim()){Message.warning('请补全仓库信息');return false}saving.value=true;try{const path=editingRepository.value?`${scopePath()}/repositories/${encodeURIComponent(repositoryForm.key)}`:`${scopePath()}/repositories`;await api(path,{method:editingRepository.value?'PUT':'POST',body:JSON.stringify(repositoryForm)});await loadAll();Message.success('项目仓库已保存，可在构建任务中选择');return true}catch(error:any){Message.error(error.message);return false}finally{saving.value=false}}
async function deleteRepository(item:Repository){try{await api(`${scopePath()}/repositories/${encodeURIComponent(item.key)}`,{method:'DELETE'});await loadAll();Message.success('仓库已删除')}catch(error:any){Message.error(error.message)}}
function selectJobRepository(kind:'jenkinsfile'|'source'|'manifest',value:unknown){const cloneURL=typeof value==='string'?value.trim():'';const item=repositories.value.find(repository=>repository.clone_url===cloneURL);if(kind==='jenkinsfile'){jobForm.jenkinsfile_repository=item?.key||'';jobForm.jenkinsfile_repo=cloneURL;if(item){jobForm.jenkinsfile_branch=item.default_branch;jobForm.jenkinsfile_path=item.default_path||'Jenkinsfile'}}else if(kind==='source'){jobForm.source_repository=item?.key||'';jobForm.source_repo=cloneURL}else{jobForm.manifest_repository=item?.key||'';jobForm.manifest_repo=cloneURL;if(item){jobForm.manifest_branch=item.default_branch;jobForm.manifest_path=item.default_path||'deploy'}}applyManagedDeliveryPaths()}
function applyManagedDeliveryPaths(){
  if(jobForm.jenkinsfile_mode!=='generated')return;
  const environment=jobForm.environment_key||store.currentEnvironmentKey||'dev';
  const pipeline=repositories.value.find(item=>item.key==='ops-delivery-jenkinsfiles');
  const manifests=repositories.value.find(item=>item.key==='ops-delivery-manifests');
  const unified=repositories.value.find(item=>item.key==='ops-delivery')||deliveryRepositories.value.find(item=>item.key===jobForm.delivery_repository_key);
  if(jobForm.delivery_repository_mode==='unified'&&unified){
    jobForm.delivery_repository_key=unified.key;
    jobForm.jenkinsfile_repository=unified.key;jobForm.jenkinsfile_repo=unified.clone_url;jobForm.jenkinsfile_branch=unified.default_branch||'main';
    jobForm.manifest_repository=unified.key;jobForm.manifest_repo=unified.clone_url;jobForm.manifest_branch=unified.default_branch||'main';
  }else{
    jobForm.delivery_repository_mode='separate';jobForm.delivery_repository_key='';
    if(pipeline){jobForm.jenkinsfile_repository=pipeline.key;jobForm.jenkinsfile_repo=pipeline.clone_url;jobForm.jenkinsfile_branch=pipeline.default_branch||'main'}
    if(manifests){jobForm.manifest_repository=manifests.key;jobForm.manifest_repo=manifests.clone_url;jobForm.manifest_branch=manifests.default_branch||'main'}
  }
  if(!jobForm.jenkinsfile_path)jobForm.jenkinsfile_path=`environments/${environment}/pipelines/${jobForm.key||'job'}/Jenkinsfile`;
  if(!jobForm.manifest_path)jobForm.manifest_path=`environments/${environment}`;
  jobForm.environment_paths_text=`${environment}=${jobForm.manifest_path}`;
}
function generatedRepositoryModeChanged(){applyManagedDeliveryPaths()}
function generatedDeliveryRepositoryChanged(value:unknown){jobForm.delivery_repository_key=typeof value==='string'?value:'';applyManagedDeliveryPaths()}
function selectSharedJobCredential(value:unknown){const key=typeof value==='string'?value:'';jobForm.jenkinsfile_credential=key;jobForm.manifest_credential=key}
function selectDefaultJobCredential(){const selected=credentialsForConnection.value.some(item=>item.key===jobForm.jenkinsfile_credential)||(jobForm.jenkinsfile_mode==='generated'&&jobForm.jenkinsfile_credential===PLATFORM_MANAGED_CREDENTIAL);if(!selected)selectSharedJobCredential('');const preferred=credentialsForConnection.value.find(item=>item.key==='gitlab-delivery-read')||credentialsForConnection.value.find(item=>item.external_id.endsWith('-gitlab-read'))||credentialsForConnection.value.find(item=>item.kind==='gitlab_token')||credentialsForConnection.value[0];if(preferred&&!jobForm.jenkinsfile_credential)selectSharedJobCredential(preferred.key);else if(jobForm.jenkinsfile_mode==='generated'&&!jobForm.jenkinsfile_credential)selectSharedJobCredential(PLATFORM_MANAGED_CREDENTIAL)}
function jobConnectionChanged(){selectDefaultJobCredential();jobForm.lark_alert_channel='';jobForm.lark_credential_id='';void loadNotificationChannels()}
function defaultJobIdentity(){const environment=store.currentEnvironmentKey||'dev';const base=(store.currentProjectKey||'project').toLowerCase().replace(/[^a-z0-9-]+/g,'-').replace(/^-+|-+$/g,'').slice(0,45);return `${base||'project'}-${environment}-release`}

const parameterID=()=>`${Date.now()}-${Math.random().toString(36).slice(2)}`;
function addParameterDefinition(){jobForm.parameter_definitions.push({_id:parameterID(),name:'',type:'string',default_value:'',choices:[],description:'',required:false})}
function removeParameterDefinition(index:number){jobForm.parameter_definitions.splice(index,1)}
function normalizeParameter(parameter:ParameterDefinition){if(parameter.type==='choice'){parameter.choices=[...new Set((parameter.choices||[]).map(item=>item.trim()).filter(Boolean))];if(!parameter.choices.includes(parameter.default_value))parameter.default_value=parameter.choices[0]||''}else{parameter.choices=[];if(parameter.type==='boolean'&&!['true','false'].includes(parameter.default_value))parameter.default_value='false'}}
function hasBuildParameter(name:string){return Boolean(buildTarget.value?.parameter_definitions?.some(item=>item.name===name))}
function isCompactJob(item?:Job|null){return Boolean(item&&(item.jenkinsfile_mode==='generated'||item.compact_parameters))}
function jobServices(job?:Job|null){if(!job)return[];return job.service_keys?.length?job.service_keys:(job.service_name?[job.service_name]:[])}
function serviceName(key:string){return deliveryServices.value.find(item=>item.key===key)?.display_name||key}
function serviceDefaultBranch(key:string){return deliveryServices.value.find(item=>item.key===key)?.source_branch||'main'}
function jobWebhookURL(jobKey:string){return `${window.location.origin}/api/cicd/webhooks/gitlab/${encodeURIComponent(store.currentProjectKey)}/${encodeURIComponent(jobKey.trim())}`}
function openWebhookSetup(item:Job,secret=''){webhookSetupJob.value=item;webhookSecret.value=secret;webhookSetupVisible.value=true}
function closeWebhookSetup(){webhookSetupVisible.value=false;webhookSecret.value=''}
async function rotateWebhookSecret(item:Job){webhookRotating.value=true;try{const result=await api<{secret_token:string}>(`${scopePath()}/jobs/${encodeURIComponent(item.key)}/webhook/rotate`,{method:'POST'});webhookSecret.value=result.secret_token;webhookSetupJob.value={...item,webhook_configured:true};await loadAll();Message.success('Secret Token 已重新生成，请立即更新 GitLab Webhook')}catch(error:any){Message.error(error.message)}finally{webhookRotating.value=false}}

function openJenkinsfileImport(){jenkinsfileAnalysis.value=null;jenkinsfileImportVisible.value=true}
function clearJenkinsfileImport(){jenkinsfileContent.value='';jenkinsfileAnalysis.value=null;jenkinsfileImportVisible.value=false}
async function analyzeJenkinsfile(){if(!jenkinsfileContent.value.trim()){Message.warning('请先粘贴 Jenkinsfile 内容');return}analyzingJenkinsfile.value=true;try{jenkinsfileAnalysis.value=await api<JenkinsfileAnalysis>(`${scopePath()}/jenkinsfile/analyze`,{method:'POST',body:JSON.stringify({content:jenkinsfileContent.value})});Message.success(`解析完成：识别到 ${jenkinsfileAnalysis.value.services?.length||0} 个服务、${jenkinsfileAnalysis.value.parameters?.length||0} 个构建参数`)}catch(error:any){jenkinsfileAnalysis.value=null;Message.error(error.message)}finally{analyzingJenkinsfile.value=false}}
function matchingCredential(reference:JenkinsfileCredentialReference){return environmentCredentials.value.find(item=>Boolean(reference.external_id)&&item.external_id===reference.external_id&&item.sync_status==='ready')}
function applyJenkinsfileImport(){const analysis=jenkinsfileAnalysis.value;if(!analysis)return;jobForm.jenkinsfile_mode='existing';const suggestion=analysis.suggestion||{};if(!jobForm.display_name&&suggestion.display_name)jobForm.display_name=suggestion.display_name;if(!jobForm.service_name&&suggestion.service_name)jobForm.service_name=suggestion.service_name;if(!jobForm.key&&suggestion.service_name)jobForm.key=suggestion.service_name.toLowerCase().replace(/[^a-z0-9-]+/g,'-').replace(/^-+|-+$/g,'').slice(0,63);if(!jobForm.jenkins_job_name&&jobForm.key)jobForm.jenkins_job_name=jobForm.key;if(['java','go'].includes(analysis.language||''))jobForm.language=analysis.language||'mixed';if(analysis.runtime_version)jobForm.runtime_version=analysis.runtime_version;const detected=(analysis.services||[]).filter(key=>deliveryServices.value.some(service=>service.key===key));if(detected.length)jobForm.service_keys=detected;const imported=(analysis.parameters||[]).map(item=>({...item,_id:parameterID(),choices:[...(item.choices||[])]}));const names=new Set(imported.map(item=>item.name));jobForm.parameter_definitions=[...jobForm.parameter_definitions.filter(item=>!names.has(item.name)),...imported];const manifest=(analysis.repositories||[]).find(item=>item.role==='manifest');if(manifest){const registered=repositories.value.find(item=>item.clone_url.replace(/\/$/,'')===manifest.url.replace(/\/$/,''));jobForm.manifest_repository=registered?.key||'';jobForm.manifest_repo=registered?.clone_url||manifest.url;jobForm.manifest_branch=manifest.branch||registered?.default_branch||suggestion.manifest_branch||'main';jobForm.manifest_path=manifest.path||registered?.default_path||suggestion.manifest_path||'deploy'}const gitReference=(analysis.credential_references||[]).find(item=>item.usage==='Git 仓库'&&matchingCredential(item));const matched=gitReference?matchingCredential(gitReference):undefined;if(matched){if(!jobForm.jenkinsfile_credential)jobForm.jenkinsfile_credential=matched.key;if(!jobForm.manifest_credential)jobForm.manifest_credential=matched.key}const missing=(analysis.credential_references||[]).filter(item=>!matchingCredential(item)).length;clearJenkinsfileImport();Message.success(missing?`配置已带入；还有 ${missing} 个 Jenkins 凭据需要在“Jenkins 设置”中补充`:'Jenkinsfile 配置和凭据已自动带入')}

function editableJobParameters(parameters:Record<string,string>){const result={...parameters};for(const key of ['LARK_ALERT_CHANNEL','LARK_ALERT_ENVIRONMENT','LARK_CREDENTIALS_ID','DEPLOY_VERIFY_MODE','ROLLOUT_TIMEOUT_MINUTES','ROLLBACK_ON_FAILURE'])delete result[key];return result}
function openJob(item?:Job){
  editingJob.value=Boolean(item);notificationChannels.value=[];
  const parameters=item?.parameters||{};
  const identity=item?.key||defaultJobIdentity();
  const environment=item?.environment_key||store.currentEnvironmentKey||'dev';
  const sameRepository=Boolean(item&&item.jenkinsfile_repo&&item.jenkinsfile_repo===item.manifest_repo&&item.jenkinsfile_repository==='ops-delivery');
  const defaultAgentMode=environmentConnections.value[0]?.connection_mode==='eks_port_forward'?'kubernetes':'node';
  Object.assign(jobForm,{
    key:identity,environment_key:environment,
    display_name:item?.display_name||`${store.currentProject?.display_name||store.currentProjectKey||'项目'} ${store.environmentLabel(environment)}构建发布`,
    service_name:item?.service_name||'',service_keys:item?jobServices(item):[],language:item?.language||'mixed',
    jenkinsfile_mode:item?.jenkinsfile_mode||'generated',execution_mode:item?.execution_mode||'serial',failure_policy:item?.failure_policy||'stop',compact_parameters:item?.compact_parameters??true,
    connection_key:item?.connection_key||environmentConnections.value[0]?.key||'',jenkins_job_name:item?.jenkins_job_name||identity,enabled:item?.enabled??true,
    trigger_mode:item?.trigger_mode||'manual',trigger_branch:item?.trigger_branch||'main',
    delivery_repository_mode:sameRepository?'unified':'separate',delivery_repository_key:sameRepository?(item?.jenkinsfile_repository||''):'',
    jenkinsfile_repository:item?.jenkinsfile_repository||'',jenkinsfile_repo:item?.jenkinsfile_repo||'',jenkinsfile_branch:item?.jenkinsfile_branch||'main',jenkinsfile_path:item?.jenkinsfile_path||'',jenkinsfile_content:item?.jenkinsfile_content||'',jenkinsfile_credential:item?.jenkinsfile_credential||'',
    source_repository:item?.source_repository||'',source_repo:item?.source_repo||'',
    manifest_repository:item?.manifest_repository||'',manifest_repo:item?.manifest_repo||'',manifest_branch:item?.manifest_branch||'main',manifest_path:item?.manifest_path||'',manifest_credential:item?.manifest_credential||'',
    environment_paths_text:mapText(item?.environment_paths),build_command:item?.build_command||'',runtime_version:item?.runtime_version||'',parameters_text:mapText(editableJobParameters(parameters)),
    agent_mode:(parameters.JENKINS_AGENT_MODE||defaultAgentMode) as 'kubernetes'|'node',kubernetes_service_account:parameters.JENKINS_KUBERNETES_SERVICE_ACCOUNT||'jenkins',agent_label:parameters.JENKINS_AGENT_LABEL||'master',aws_profile:parameters.AWS_PROFILE||'',
    pipeline_timeout_minutes:Number(parameters.PIPELINE_TIMEOUT_MINUTES||30),deploy_verify_mode:(parameters.DEPLOY_VERIFY_MODE==='apply'?'apply':'rollout') as 'rollout'|'apply',rollout_timeout_minutes:Number(parameters.ROLLOUT_TIMEOUT_MINUTES||5),rollback_on_failure:parameters.ROLLBACK_ON_FAILURE==='true',
    telegram_credential_id:parameters.TELEGRAM_CREDENTIALS_ID||'',lark_alert_channel:parameters.LARK_ALERT_CHANNEL||'',lark_credential_id:parameters.LARK_CREDENTIALS_ID||'',
    parameter_definitions:(item?.parameter_definitions||[]).map(parameter=>({...parameter,_id:parameterID(),choices:[...(parameter.choices||[])]})),
  });
  if(!item){jobForm.delivery_repository_mode=repositories.value.some(repository=>repository.key==='ops-delivery')?'unified':'separate';jobForm.delivery_repository_key=''}
  applyManagedDeliveryPaths();selectDefaultJobCredential();jobVisible.value=true;void loadNotificationChannels()
}
async function saveJob(){
  applyManagedDeliveryPaths();selectDefaultJobCredential();jobForm.service_keys=[...new Set(jobForm.service_keys.map(item=>item.trim()).filter(Boolean))];jobForm.service_name=jobForm.service_keys[0]||jobForm.key;
  const languages=[...new Set(jobForm.service_keys.map(key=>deliveryServices.value.find(item=>item.key===key)?.language).filter(Boolean))];jobForm.language=languages.length===1?String(languages[0]):'mixed';
  if(!jobForm.environment_key||!jobForm.key.trim()||!jobForm.display_name.trim()||!jobForm.service_keys.length||!jobForm.connection_key||!jobForm.jenkins_job_name.trim()||!jobForm.jenkinsfile_repo.trim()||!jobForm.manifest_repo.trim()){Message.warning('请选择构建服务并确认环境、Jenkins 与项目交付仓库已对接');return false}
  if(!jobForm.jenkinsfile_path.trim()||!jobForm.manifest_path.trim()){Message.warning('请填写 Jenkinsfile 路径和部署清单环境目录');return false}
  if(jobForm.trigger_mode==='gitlab_push'&&!jobForm.trigger_branch.trim()){Message.warning('请选择 GitLab Push 自动触发的监听分支');return false}
  if(!jobForm.jenkinsfile_credential||!jobForm.manifest_credential){Message.warning('请选择 GitLab 仓库凭据，或点击“账号 Token”直接创建并同步');return false}
  if(jobForm.jenkinsfile_mode==='generated'&&jobForm.agent_mode==='node'&&!jobForm.agent_label.trim())jobForm.agent_label='master';
  if(jobForm.jenkinsfile_mode==='generated'&&jobForm.agent_mode==='kubernetes'&&!jobForm.kubernetes_service_account.trim()){Message.warning('请填写 Kubernetes Agent ServiceAccount');return false}
  saving.value=true;let jobSaved=false;
  try{
    const parameters=parseMap(jobForm.parameters_text);delete parameters.LARK_CREDENTIALS_ID;delete parameters.LARK_ALERT_CHANNEL;delete parameters.LARK_ALERT_ENVIRONMENT;
    if(jobForm.lark_alert_channel.trim()){parameters.LARK_ALERT_CHANNEL=jobForm.lark_alert_channel.trim();parameters.LARK_ALERT_ENVIRONMENT=store.currentEnvironmentKey}
    if(jobForm.jenkinsfile_mode==='generated'){
      parameters.JENKINS_AGENT_MODE=jobForm.agent_mode;
      if(jobForm.agent_mode==='kubernetes'){parameters.JENKINS_KUBERNETES_SERVICE_ACCOUNT=jobForm.kubernetes_service_account.trim();delete parameters.JENKINS_AGENT_LABEL}else{parameters.JENKINS_AGENT_LABEL=jobForm.agent_label.trim();delete parameters.JENKINS_KUBERNETES_SERVICE_ACCOUNT}
      parameters.PIPELINE_TIMEOUT_MINUTES=String(jobForm.pipeline_timeout_minutes||30);
      parameters.DEPLOY_VERIFY_MODE=jobForm.deploy_verify_mode;
      parameters.ROLLOUT_TIMEOUT_MINUTES=String(jobForm.rollout_timeout_minutes||5);
      parameters.ROLLBACK_ON_FAILURE=String(jobForm.rollback_on_failure);
      for(const [key,value] of [['AWS_PROFILE',jobForm.aws_profile],['TELEGRAM_CREDENTIALS_ID',jobForm.telegram_credential_id]] as const){if(value.trim())parameters[key]=value.trim();else delete parameters[key]}
    }
    const payloadForm=jobForm.jenkinsfile_credential===PLATFORM_MANAGED_CREDENTIAL?{...jobForm,jenkinsfile_credential:'',manifest_credential:''}:jobForm;
    const body=createCICDJobPayload(payloadForm,parseMap(jobForm.environment_paths_text),parameters);const path=editingJob.value?`${scopePath()}/jobs/${encodeURIComponent(jobForm.key)}`:`${scopePath()}/jobs`;
    const saved=await api<Job>(path,{method:editingJob.value?'PUT':'POST',body:JSON.stringify(body)});jobSaved=true;
    const synced=await api<Job>(`${scopePath()}/jobs/${encodeURIComponent(jobForm.key)}/sync`,{method:'POST'});jobForm.lark_credential_id=synced.parameters?.LARK_CREDENTIALS_ID||'';
    let generatedSecret='';
    if(jobForm.trigger_mode==='gitlab_push'&&!saved.webhook_configured){try{const secret=await api<{secret_token:string}>(`${scopePath()}/jobs/${encodeURIComponent(jobForm.key)}/webhook/rotate`,{method:'POST'});generatedSecret=secret.secret_token}catch(error:any){Message.warning(`Job 已同步，但 Webhook Secret 生成失败：${error.message}；可在 Job 列表点击“Webhook”重试`)}}
    if(jobForm.trigger_mode==='gitlab_push'){window.setTimeout(()=>openWebhookSetup({...synced,webhook_configured:Boolean(generatedSecret)||synced.webhook_configured},generatedSecret),0)}
    await loadAll();Message.success(jobForm.lark_alert_channel?'GitLab 凭据、Lark Webhook、Jenkinsfile 与 Jenkins Job 已同步并校验':'GitLab 凭据、Jenkinsfile、部署引用和 Jenkins Job 已同步并校验');return true
  }catch(error:any){await loadAll();const persisted=jobs.value.find(item=>item.key===jobForm.key)?.sync_error;const detail=persisted||error.message;Message.error({content:jobSaved?`Job 已保存，但同步未完成：${detail}；修复后可点击“重新同步”继续`:`Job 保存失败：${detail}`,duration:10_000});return false}finally{saving.value=false}
}
async function syncJob(item:Job){busyKey.value=`job-sync-${item.key}`;try{await api(`${scopePath()}/jobs/${encodeURIComponent(item.key)}/sync`,{method:'POST'});await loadAll();Message.success('GitLab 与 Jenkins 已重新同步并回读校验')}catch(error:any){await loadAll();const detail=jobs.value.find(record=>record.key===item.key)?.sync_error||error.message;Message.error({content:detail,duration:10_000})}finally{busyKey.value=''}}
async function openDeleteJob(item:Job){busyKey.value=`job-delete-${item.key}`;try{const usage=await api<JobBuildUsage>(`${scopePath()}/jobs/${encodeURIComponent(item.key)}/usage`);deleteJobTarget.value=item;Object.assign(deleteJobUsage,usage);deleteRemoteJob.value=true;deleteJobVisible.value=true}catch(error:any){Message.error(error.message)}finally{busyKey.value=''}}
async function confirmDeleteJob(){if(!deleteJobTarget.value||deleteJobUsage.active_builds>0)return false;deleteJobSaving.value=true;try{const result=await api<JobDeletionResult>(`${scopePath()}/jobs/${encodeURIComponent(deleteJobTarget.value.key)}?delete_remote=${deleteRemoteJob.value}`,{method:'DELETE'});await loadAll();const remote=result.remote_deletion_requested?(result.remote_deleted?'，Jenkins 远程 Job 已删除':result.remote_already_missing?'，Jenkins 远程 Job 原本就不存在':''):'，Jenkins 远程 Job 已保留';Message.success(`平台 Job 已删除${remote}，保留 ${result.historical_builds_retained} 条构建历史`);return true}catch(error:any){Message.error(error.message);return false}finally{deleteJobSaving.value=false}}

function openBuild(item:Job){buildTarget.value=item;const parameters={...(item.parameters||{})};for(const definition of item.parameter_definitions||[]){if(parameters[definition.name]===undefined)parameters[definition.name]=definition.default_value||''}Object.assign(buildForm,{environment:item.environment_key||store.currentEnvironmentKey||'',branch:'',image_tag:new Date().toISOString().replace(/[-:TZ.]/g,'').slice(0,14),services:jobServices(item),parameters,parameters_text:''});buildVisible.value=true}
async function triggerBuild(){
  if(!buildTarget.value||!buildForm.environment){Message.warning('请选择发布环境');return false}
  if(!buildForm.services.length){Message.warning('请至少选择一个构建服务');return false}
  const parameters={...buildForm.parameters,...parseMap(buildForm.parameters_text)};
  for(const definition of buildTarget.value.parameter_definitions||[]){if(definition.required&&!String(parameters[definition.name]||'').trim()){Message.warning(`请填写构建参数 ${definition.name}`);return false}}
  const defaultBranches=Object.fromEntries(deliveryServices.value.map(service=>[service.key,service.source_branch||'main']));
  const requests=createSingleServiceBuildRequests({environment:buildForm.environment,branch:buildForm.branch,image_tag:buildForm.image_tag,services:buildForm.services,default_branches:defaultBranches,parameters});
  const jobKey=buildTarget.value.key;
  const created:Build[]=[];
  const failed:Error[]=[];
  saving.value=true;
  try{
    // Queue requests in selection order. This still creates independent Jenkins
    // builds, while avoiding a burst of concurrent tunnel/Jenkins requests.
    for(const payload of requests){
      try{created.push(await api<Build>(`${scopePath()}/jobs/${encodeURIComponent(jobKey)}/builds`,{method:'POST',body:JSON.stringify(payload)}))}
      catch(error:any){failed.push(error instanceof Error?error:new Error(String(error)))}
    }
    await loadBuilds(false);
    if(!created.length){Message.error(failed.map(error=>error.message).join('；')||'构建提交失败');return false}
    activeTab.value='builds';
    if(failed.length){Message.warning(`已提交 ${created.length} 个服务，${failed.length} 个提交失败；可在构建记录中单独重试`)}
    else{Message.success(`已拆分提交 ${created.length} 个独立 Jenkins 构建`)}
    if(created.length===1)openLogs(created[0]);
    return true
  }finally{saving.value=false}
}
async function retryBuild(item:Build){try{const created=await api<Build>(`${scopePath()}/builds/${encodeURIComponent(item.id)}/retry`,{method:'POST'});await loadBuilds(false);Message.success('已重新提交 Jenkins 构建');openLogs(created)}catch(error:any){Message.error(error.message)}}
async function cancelBuild(item:Build){try{await api(`${scopePath()}/builds/${encodeURIComponent(item.id)}/cancel`,{method:'POST'});await loadBuilds(false);Message.success('已发送停止指令')}catch(error:any){Message.error(error.message)}}
function openLogs(item:Build){stopLogPolling();logBuild.value=item;logText.value='';deploymentLogText.value='';logTab.value='deployment';logOffset.value=0;autoScroll.value=true;logPollingErrorNotified=false;logsVisible.value=true;const current=++generation;void pollLogs(current)}
function resetLogs(){if(!logBuild.value)return;logText.value='';deploymentLogText.value='';logOffset.value=0;logPollingErrorNotified=false;window.clearTimeout(pollTimer);pollTimer=0;const current=++generation;void pollLogs(current)}
function stopLogPolling(){generation++;window.clearTimeout(pollTimer);pollTimer=0}
function scrollLogsToBottom(){nextTick(()=>{if(terminal.value)terminal.value.scrollTop=terminal.value.scrollHeight})}
async function pollLogs(current:number,finalPass=false){if(current!==generation||!logBuild.value||!logsVisible.value)return;pollTimer=0;const id=logBuild.value.id;const wasLive=['queued','running'].includes(logBuild.value.status);let logHasMore=false;try{await loadBuilds(false);const fresh=builds.value.find(item=>item.id===id);if(fresh)logBuild.value=fresh;try{const chunk=await api<{text:string;next_offset:number;more:boolean}>(`${scopePath()}/builds/${encodeURIComponent(id)}/logs?offset=${logOffset.value}`);if(current!==generation)return;logText.value+=chunk.text;logOffset.value=chunk.next_offset;logHasMore=chunk.more;await nextTick();if(autoScroll.value)scrollLogsToBottom()}catch(error:any){if(!String(error.message).includes('排队'))throw error}try{const deployment=await api<{text:string}>(`${scopePath()}/builds/${encodeURIComponent(id)}/deployment-logs`);if(current===generation)deploymentLogText.value=deployment.text||''}catch(error:any){if(!String(error.message).includes('排队')&&!String(error.message).includes('暂无部署日志'))throw error}logPollingErrorNotified=false;const isLive=Boolean(logBuild.value&&['queued','running'].includes(logBuild.value.status));if(isLive)pollTimer=window.setTimeout(()=>void pollLogs(current),LOG_POLL_MS);else if(logHasMore)pollTimer=window.setTimeout(()=>void pollLogs(current,true),250);else if(wasLive&&!finalPass)pollTimer=window.setTimeout(()=>void pollLogs(current,true),400)}catch(error:any){if(current!==generation)return;if(!logPollingErrorNotified){logPollingErrorNotified=true;Message.warning(`实时日志连接暂时中断，正在自动重连：${error.message}`)}const isLive=Boolean(logBuild.value&&['queued','running'].includes(logBuild.value.status));if(isLive||!finalPass)pollTimer=window.setTimeout(()=>void pollLogs(current,finalPass),LOG_RETRY_MS)}}

const compactRepo=(value:string)=>value.replace(/^https:\/\//,'').replace(/\.git$/,'');
const gitLabFileURL=(repository:string,branch:string,filePath:string)=>`${repository.replace(/\.git$/,'')}/-/blob/${encodeURIComponent(branch||'main')}/${filePath.split('/').map(encodeURIComponent).join('/')}`;
const jobDockerfilePath=(service:ServiceSpec|undefined,environment=jobForm.environment_key||'dev')=>!service?'未登记':service.dockerfile_source==='source'?(service.dockerfile||'Dockerfile'):`environments/${environment}/dockerfiles/${service.key}/Dockerfile`;
const repositoryName=(key:string|undefined,url:string)=>repositories.value.find(item=>item.key===key)?.display_name||compactRepo(url);const repositoryPurposeName=(value:string)=>({jenkinsfile:'Jenkinsfile',manifest:'部署清单',source:'业务源码',general:'通用'}[value]||value);const jobExists=(key:string)=>jobs.value.some(item=>item.key===key);const jobName=(key:string)=>jobs.value.find(item=>item.key===key)?.display_name||`已删除 · ${key}`;const formatTime=(value:string)=>value?new Date(value).toLocaleString():'—';
const repositoryRoleName=(value:string)=>({jenkinsfile:'Jenkinsfile',manifest:'部署清单',source:'业务源码'}[value]||value);const parameterTypeName=(value:string)=>({string:'文本',choice:'选项',number:'数字',boolean:'布尔'}[value]||value);
const syncColor=(value?:string)=>value==='ready'?'green':value==='failed'?'red':'gray';const syncName=(value?:string)=>value==='ready'?'已就绪':value==='failed'?'同步失败':'未同步';
const buildColor=(value:string)=>({queued:'orange',running:'blue',succeeded:'green',failed:'red',canceled:'gray'}[value]||'gray');const buildName=(value:string)=>({queued:'排队中',running:'构建中',succeeded:'成功',failed:'失败',canceled:'已停止'}[value]||value);
const buildProgress=(item:Build)=>Math.max(0,Math.min(100,item.progress??(item.status==='succeeded'?100:item.status==='running'?15:item.status==='queued'?5:0)));const stageColor=(value:string)=>({succeeded:'green',failed:'red',running:'blue',canceled:'gray',paused:'orange',skipped:'gray',pending:'gray'}[value]||'gray');const stageStatusName=(value:string)=>({succeeded:'成功',failed:'失败',running:'进行中',canceled:'已停止',paused:'等待',skipped:'跳过',pending:'待执行'}[value]||value);
const formatDuration=(value:number)=>value>=60000?`${Math.floor(value/60000)}分${Math.round(value%60000/1000)}秒`:`${Math.max(1,Math.round(value/1000))}秒`;
const credentialKindName=(value:string)=>({existing:'Jenkins 已有',gitlab_token:'GitLab HTTPS Token',username_password:'用户名/密码',secret_text:'Secret Text',ssh_private_key:'SSH 私钥'}[value]||value);
const deploymentColor=(value:string)=>({healthy:'green',missing:'red',disabled:'gray',drift:'orange'}[value]||'gray');const deploymentName=(value:string)=>({healthy:'Jenkins 运行中',missing:'Jenkins 异常',disabled:'Jenkins 未启用',drift:'Jenkins 状态漂移',unknown:'状态未知'}[value]||value);
</script>

<style scoped>
.delivery-path-card{margin:16px 0;padding:16px 18px 2px;border:1px solid #c9d8ff;border-radius:12px;background:linear-gradient(135deg,#f5f8ff,#fff)}.delivery-path-heading{display:flex;align-items:flex-start;justify-content:space-between;gap:20px;margin-bottom:14px}.delivery-path-heading p{margin:5px 0 0;color:#86909c;font-size:12px;line-height:1.6}
.job-delivery-summary{margin-bottom:16px;padding:16px;border:1px solid #c9d8ff;border-radius:12px;background:linear-gradient(135deg,#f5f8ff,#fff)}.job-delivery-summary-head{display:flex;align-items:flex-start;justify-content:space-between;gap:16px;margin-bottom:14px}.job-delivery-summary-head p{margin:5px 0 0;color:#86909c;font-size:12px}.job-service-summary{margin-top:14px}.job-service-summary code{display:block;max-width:330px;overflow:hidden;color:#165dff;text-overflow:ellipsis;white-space:nowrap}
.delete-remote-option{display:flex;align-items:center;justify-content:space-between;gap:24px;margin-top:16px;padding:14px 16px;border:1px solid #e5e8ef;border-radius:10px;background:#f7f8fa}.delete-remote-option p{margin:5px 0 0;color:#86909c;font-size:12px}
.job-sync-status{display:flex;min-width:0;flex-direction:column;align-items:flex-start;gap:6px}.job-sync-status small{display:-webkit-box;max-width:270px;overflow:hidden;color:#f53f3f;font-size:12px;line-height:1.45;-webkit-box-orient:vertical;-webkit-line-clamp:3;word-break:break-word}
.job-credential-picker{display:flex;align-items:center;gap:10px}.job-credential-picker>.arco-select{min-width:0;flex:1}.job-credential-picker>.arco-btn{flex:0 0 auto}.job-notification-card{display:grid;grid-template-columns:minmax(240px,1fr) minmax(280px,1fr);align-items:center;gap:10px 22px;margin:16px 0;padding:16px 18px;border:1px solid #dce6ff;border-radius:12px;background:linear-gradient(135deg,#f5f8ff,#fff)}.job-notification-copy p{margin:5px 0 0;color:#86909c;font-size:12px;line-height:1.6}.job-notification-select{margin:0}.job-notification-id{display:flex;grid-column:1/-1;align-items:center;gap:8px;padding-top:10px;border-top:1px dashed #d9e2f5;color:#4e5969;font-size:12px}.job-notification-id code{padding:3px 7px;border-radius:5px;background:#edf3ff;color:#165dff}.job-notification-card>.arco-alert{grid-column:1/-1}
.job-trigger-card{margin:16px 0;padding:16px 18px;border:1px solid #cfe5dc;border-radius:12px;background:linear-gradient(135deg,#f2fbf7,#fff)}.job-trigger-heading{display:flex;align-items:center;justify-content:space-between;gap:20px;margin-bottom:14px}.job-trigger-heading p{margin:5px 0 0;color:#86909c;font-size:12px;line-height:1.6}.webhook-setup-form{margin-top:16px}.webhook-actions{display:flex;justify-content:flex-end;gap:10px;margin-top:18px}
.cicd-page{display:flex;flex-direction:column;gap:16px}.hero-card{background:linear-gradient(120deg,#f4f7ff,#fff 60%,#effcf8);border:1px solid #dfe7fa}.hero-content{display:flex;justify-content:space-between;align-items:center;gap:32px}.hero-content h2{margin:6px 0 8px;font-size:24px}.hero-content p{margin:0;color:#687386;max-width:760px}.eyebrow{font-size:11px;font-weight:700;letter-spacing:1.5px;color:#165dff}.hero-stats{display:flex;gap:12px}.hero-stats div{min-width:80px;padding:12px 16px;border-radius:10px;background:#fff;box-shadow:0 4px 16px #1d3b6820;text-align:center}.hero-stats strong{display:block;font-size:22px}.hero-stats span{font-size:12px;color:#86909c}.integration-card{border-color:#c9d8ff}.integration-content{display:flex;justify-content:space-between;align-items:center;gap:24px}.integration-main{min-width:0}.integration-title{display:flex;align-items:center;gap:10px}.integration-title strong{font-size:16px}.integration-meta{display:flex;gap:24px;margin-top:12px;color:#4e5969}.integration-meta span{font-size:13px}.integration-main p{margin:10px 0 0;color:#86909c}.integration-reason{color:#f53f3f!important}.cicd-tabs{background:transparent}.settings-grid{display:grid;grid-template-columns:1fr 1fr;gap:16px}.repository-card{grid-column:1/-1}.section-alert{margin-bottom:14px}.external-config-summary{display:flex;flex-direction:column;gap:5px}.external-config-summary code{word-break:break-all}.primary-cell,.repo-cell{display:flex;flex-direction:column;align-items:flex-start;gap:4px;min-width:140px}.primary-cell small,.repo-cell small,.muted-id{font-size:12px;color:#86909c}.primary-cell code{font-size:12px;color:#165dff}.repo-cell span{max-width:420px;overflow:hidden;text-overflow:ellipsis;white-space:nowrap}.compact-tags{gap:4px}.build-progress{display:flex;flex-direction:column;gap:5px}.progress-caption{display:flex;align-items:center;justify-content:space-between;gap:8px}.progress-caption span{max-width:205px;overflow:hidden;color:#4e5969;font-size:12px;text-overflow:ellipsis;white-space:nowrap}.progress-caption strong{color:#165dff;font-size:12px}.progress-shell{position:relative;overflow:hidden;border-radius:999px}.progress-shell.is-running::after{position:absolute;inset:0;width:35%;background:linear-gradient(90deg,transparent,#ffffffb8,transparent);content:"";transform:translateX(-140%);animation:progress-shine 1.65s ease-in-out infinite}.record-stage-dots{display:flex;gap:4px}.record-stage-dots span{width:7px;height:7px;border-radius:50%;background:#c9cdd4}.record-stage-dots .dot-succeeded{background:#00b42a}.record-stage-dots .dot-running{background:#165dff;animation:status-pulse 1.3s ease-in-out infinite}.record-stage-dots .dot-failed{background:#f53f3f}.live-status{display:inline-flex;align-items:center;gap:6px}.status-dot{width:8px;height:8px;border-radius:50%;background:#86909c;box-shadow:0 0 0 3px #86909c18}.status-running .status-dot{background:#165dff;box-shadow:0 0 0 4px #165dff1f;animation:status-pulse 1.3s ease-in-out infinite}.status-queued .status-dot{background:#ff7d00;animation:status-pulse 1.8s ease-in-out infinite}.status-succeeded .status-dot{background:#00b42a}.status-failed .status-dot{background:#f53f3f}.status-canceled .status-dot{background:#86909c}.build-live-header{display:flex;align-items:center;justify-content:space-between;gap:18px;margin-bottom:14px}.build-live-identity{display:flex;flex-direction:column;gap:7px}.live-title{display:flex;align-items:baseline;gap:9px}.live-title strong{font-size:19px}.live-title>span{color:#86909c;font:13px ui-monospace,SFMono-Regular,Menlo,monospace}.live-subtitle{display:flex;align-items:center;gap:8px;color:#86909c;font-size:12px}.live-stream{display:inline-flex;align-items:center;gap:5px;color:#165dff;font-size:12px;font-weight:600}.live-stream i{width:7px;height:7px;border-radius:50%;background:#165dff;box-shadow:0 0 0 4px #165dff1c;animation:status-pulse 1.25s ease-in-out infinite}.build-live-overview{display:grid;grid-template-columns:105px minmax(260px,1fr) auto;align-items:center;gap:22px;padding:20px;border:1px solid #dce6ff;border-radius:14px;background:radial-gradient(circle at 0 0,#e8f3ff 0,transparent 37%),linear-gradient(135deg,#f7faff,#fff);box-shadow:0 8px 26px #244f8d12}.overview-succeeded{border-color:#b7ebc5;background:radial-gradient(circle at 0 0,#e8ffea 0,transparent 37%),linear-gradient(135deg,#f7fff8,#fff)}.overview-failed{border-color:#ffd0d0;background:radial-gradient(circle at 0 0,#fff0f0 0,transparent 37%),linear-gradient(135deg,#fff9f9,#fff)}.live-progress-number{display:flex;align-items:flex-start;justify-content:center;color:#165dff}.live-progress-number strong{font-size:44px;line-height:1;font-variant-numeric:tabular-nums}.live-progress-number span{margin-top:4px;font-size:15px;font-weight:700}.overview-succeeded .live-progress-number{color:#00b42a}.overview-failed .live-progress-number{color:#f53f3f}.live-progress-main{display:flex;flex-direction:column;gap:11px}.live-progress-heading{display:flex;flex-direction:column;gap:4px}.live-progress-heading strong{font-size:15px}.live-progress-heading small{color:#86909c}.stage-stats{display:flex;align-items:center;gap:10px}.stage-stats>div{min-width:68px;padding:9px 10px;border:1px solid #e5e8ef;border-radius:9px;background:#ffffffd9;text-align:center}.stage-stats strong,.stage-stats span{display:block}.stage-stats strong{font-size:18px}.stage-stats span{margin-top:2px;color:#86909c;font-size:11px}.stage-stats .stat-success strong{color:#00b42a}.stage-stats .stat-running strong{color:#165dff}.stage-stats .stat-pending strong{color:#ff7d00}.stage-stats .stat-failed strong{color:#f53f3f}.build-error{margin-top:14px}.live-stage-grid{grid-template-columns:repeat(2,minmax(0,1fr));gap:10px;margin:16px 0}.live-stage-grid .stage-item{position:relative;display:flex;align-items:center;gap:12px;min-height:64px;padding:11px 13px;border-color:#e5e8ef;background:#fff;overflow:hidden}.live-stage-grid .stage-succeeded{border-color:#b7ebc5;background:linear-gradient(100deg,#f2fff5,#fff)}.live-stage-grid .stage-running{border-color:#9fc4ff;background:linear-gradient(100deg,#edf5ff,#fff);box-shadow:0 0 0 1px #165dff14,0 8px 22px #165dff12;animation:stage-glow 1.9s ease-in-out infinite}.live-stage-grid .stage-failed{border-color:#ffd0d0;background:linear-gradient(100deg,#fff2f2,#fff)}.stage-sequence{position:relative;display:grid;place-items:center;width:30px;height:30px;flex:0 0 30px;border-radius:50%;background:#f2f3f5;color:#4e5969;font-size:12px;font-weight:700}.stage-succeeded .stage-sequence{background:#e8ffea;color:#00b42a}.stage-running .stage-sequence{background:#e8f3ff;color:#165dff}.stage-failed .stage-sequence{background:#ffece8;color:#f53f3f}.stage-content{display:flex;min-width:0;flex:1;flex-direction:column;gap:5px}.stage-content>div{display:flex;align-items:center;justify-content:space-between;gap:8px}.stage-content strong{overflow:hidden;text-overflow:ellipsis;white-space:nowrap}.stage-content small{grid-column:auto}.stage-waiting{display:flex;align-items:center;gap:16px;margin:16px 0;padding:18px;border:1px dashed #bedaff;border-radius:12px;background:#f7fbff}.stage-waiting p{margin:5px 0 0;color:#86909c}.waiting-radar{position:relative;width:34px;height:34px;flex:0 0 34px;border:2px solid #165dff;border-radius:50%}.waiting-radar::before,.waiting-radar::after{position:absolute;inset:5px;border:1px solid #165dff80;border-radius:50%;content:"";animation:radar 1.8s ease-out infinite}.waiting-radar::after{animation-delay:.6s}.live-log-panel{padding:14px 14px 0;border:1px solid #e5e8ef;border-radius:12px;background:#fbfcfe}.live-log-toolbar{display:flex;align-items:center;justify-content:space-between;gap:16px;margin-bottom:8px}.live-log-toolbar>div{display:flex;align-items:center;gap:10px}.auto-scroll-label{color:#86909c;font-size:12px}.cicd-terminal{height:calc(100vh - 560px);min-height:280px;overflow:auto;margin:0;padding:16px;border:1px solid #263348;border-radius:9px;background:linear-gradient(180deg,#111827,#0c1423);box-shadow:inset 0 1px 18px #0004;color:#d1fae5;font:12px/1.7 ui-monospace,SFMono-Regular,Menlo,monospace;white-space:pre-wrap;word-break:break-word}.deployment-terminal{color:#dbeafe}@keyframes status-pulse{0%,100%{opacity:1;transform:scale(1)}50%{opacity:.48;transform:scale(.75)}}@keyframes progress-shine{0%{transform:translateX(-140%)}65%,100%{transform:translateX(390%)}}@keyframes stage-glow{0%,100%{box-shadow:0 0 0 1px #165dff14,0 6px 18px #165dff0d}50%{box-shadow:0 0 0 3px #165dff1b,0 10px 28px #165dff20}}@keyframes radar{0%{opacity:.8;transform:scale(.35)}100%{opacity:0;transform:scale(1.9)}}@media(prefers-reduced-motion:reduce){.status-dot,.live-stream i,.record-stage-dots span,.progress-shell::after,.stage-running,.waiting-radar::before,.waiting-radar::after{animation:none!important}}@media(max-width:1200px){.settings-grid{grid-template-columns:1fr}.repository-card{grid-column:auto}.hero-content,.integration-content{align-items:flex-start;flex-direction:column}.hero-stats{width:100%}.hero-stats div{flex:1}.integration-meta{flex-direction:column;gap:6px}.stage-grid{grid-template-columns:1fr 1fr}.build-live-overview{grid-template-columns:85px 1fr}.stage-stats{grid-column:1/-1;justify-content:flex-end}}@media(max-width:720px){.jenkinsfile-import-banner,.build-live-header{align-items:flex-start;flex-direction:column}.analysis-summary{grid-template-columns:repeat(2,1fr)}.analysis-list>div{align-items:flex-start;flex-direction:column}.analysis-list small{margin-left:0}.stage-grid,.live-stage-grid{grid-template-columns:1fr}.build-live-overview{grid-template-columns:1fr}.live-progress-number{justify-content:flex-start}.stage-stats{justify-content:flex-start;flex-wrap:wrap}.live-log-toolbar{align-items:flex-start;flex-direction:column}.cicd-terminal{height:360px}}
@media(max-width:720px){.job-credential-picker,.job-trigger-heading{align-items:stretch;flex-direction:column}.job-notification-card{grid-template-columns:1fr}.job-notification-id,.job-notification-card>.arco-alert{grid-column:1}.webhook-actions{align-items:stretch;flex-direction:column}}
</style>

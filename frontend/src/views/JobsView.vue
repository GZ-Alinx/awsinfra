<template>
  <div>
    <div class="page-header"><div><h2>部署任务与日志</h2><p>{{ store.currentProject?.display_name }} / {{ store.currentEnvironment?.display_name }}：实时展示步骤、整体进度和分环境日志</p></div><a-space><a-popconfirm v-if="store.jobs.some((job) => ['succeeded','failed','canceled','ignored'].includes(job.status))" content="只清理当前环境已结束的任务记录与日志，运行中任务不会删除。" @ok="clearHistory"><a-button status="danger">清理历史</a-button></a-popconfirm><a-button :loading="loading" @click="refresh"><icon-refresh />刷新</a-button></a-space></div>
    <div class="job-summary-grid">
      <a-card><span>任务总数</span><strong>{{ store.jobs.length }}</strong></a-card>
      <a-card><span>执行中</span><strong class="running-text">{{ statusCount('running') + statusCount('queued') }}</strong></a-card>
      <a-card><span>成功</span><strong class="success-text">{{ statusCount('succeeded') }}</strong></a-card>
      <a-card v-if="statusCount('failed') > 0"><span>失败</span><strong class="danger-text">{{ statusCount('failed') }}</strong></a-card>
      <a-card v-else-if="statusCount('ignored') > 0"><span>已忽略</span><strong>{{ statusCount('ignored') }}</strong></a-card>
    </div>

    <a-card v-if="selected" class="deployment-console full-card">
      <template #title><span class="card-title">当前任务 · {{ actionName(selected.action) }}</span></template>
      <template #extra><a-space><a-tag :color="statusColor(selected.status)">{{ statusName(selected.status) }}</a-tag><a-button v-if="planCanDeploy(selected)" size="small" type="primary" @click="openDeployAfterPlan(selected)">确认计划并执行阶段1</a-button><a-button v-if="canApplyAccessOnly(selected)" size="small" type="primary" :loading="accessing" @click="applyAccessOnly(selected)">仅应用接入配置</a-button><a-popconfirm v-if="selected.status === 'failed' && requiresRepair(selected)" content="当前问题不能靠重复执行解决。确认已经完成页面建议的状态修复或资源对账？" @ok="retry(selected)"><a-button size="small" type="primary" :loading="retrying">确认已修复并重试</a-button></a-popconfirm><a-button v-else-if="['failed','canceled','ignored'].includes(selected.status)" size="small" type="primary" :loading="retrying" @click="retry(selected)">重试操作</a-button><a-button v-if="['failed','canceled'].includes(selected.status) && selected.action !== 'destroy'" size="small" status="warning" @click="openIgnore(selected)">忽略本次失败</a-button><a-button size="small" @click="resetLog"><icon-refresh />刷新日志</a-button><a-popconfirm v-if="['queued','running'].includes(selected.status)" content="确认取消当前任务？" @ok="cancel(selected)"><a-button size="small" status="danger">取消任务</a-button></a-popconfirm></a-space></template>
      <a-alert v-if="planCanDeploy(selected)" type="success" show-icon class="plan-ready-alert">阶段1计划已生成。请先检查下方计划日志，确认资源变更符合预期后再执行部署。</a-alert>
      <div class="progress-heading"><div><strong>{{ selected.progress || 0 }}%</strong><span>{{ selected.current_step ? stageName(selected.current_step) : (selected.status === 'succeeded' ? (planCanDeploy(selected) ? '计划生成完成，等待确认部署' : '任务完成') : selected.status === 'failed' ? '任务失败' : selected.status === 'ignored' ? '失败已确认忽略' : '等待执行') }}</span></div><div class="step-counters"><a-tag color="green">成功 {{ visibleSucceededSteps }}</a-tag><a-tag v-if="visibleFailedSteps" :color="selected.status === 'ignored' ? 'orange' : 'red'">失败 {{ visibleFailedSteps }}</a-tag><a-tag>显示步骤 {{ visibleSelectedSteps.length }}</a-tag></div></div>
      <a-progress :percent="(selected.progress || 0) / 100" :status="selected.status === 'failed' ? 'danger' : selected.status === 'succeeded' ? 'success' : 'normal'" :show-text="false" animation />
      <section v-if="selected.status === 'failed' && selectedDiagnosis" class="job-diagnosis">
        <div class="diagnosis-heading"><div><span class="diagnosis-icon">!</span><div><small>平台诊断</small><h3>{{ selectedDiagnosis.title }}</h3></div></div><a-tag color="red">{{ selectedDiagnosis.code }}</a-tag></div>
        <div class="diagnosis-stage"><span>失败阶段</span><strong>{{ selectedDiagnosis.stage || failedStep(selected) || actionName(selected.action) }}</strong></div>
        <div class="diagnosis-grid">
          <div><label>直接原因</label><p>{{ selectedDiagnosis.cause }}</p></div>
          <div><label>影响范围</label><p>{{ selectedDiagnosis.impact }}</p></div>
          <div class="diagnosis-action"><label>建议处理</label><p>{{ selectedDiagnosis.suggestion }}</p></div>
          <div><label>重试条件</label><p>{{ selectedDiagnosis.retry }}</p></div>
        </div>
        <details v-if="selected.error" class="technical-error"><summary>查看底层技术错误</summary><pre>{{ selected.error }}</pre></details>
      </section>
      <a-alert v-if="selected.status === 'ignored'" type="warning" show-icon class="plan-ready-alert">本次失败已由 {{ selected.ignored_by || '运维人员' }} 确认忽略：{{ selected.ignore_reason }}。这只归档告警，不会把失败步骤伪装成成功，也不会跳过未执行的资源操作；仍可查看日志并重新提交任务。</a-alert>
      <div v-if="visibleSelectedSteps.length" class="job-step-grid">
        <div v-for="(step, index) in visibleSelectedSteps" :key="`${index}-${step.name}`" :class="['job-step', step.status]">
          <span class="step-index">{{ step.status === 'succeeded' ? '✓' : step.status === 'failed' ? '!' : index + 1 }}</span>
          <div><strong>{{ stageName(step.name) }}</strong><small>{{ stepStatusName(step.status) }}<template v-if="step.error"> · {{ step.error }}</template></small></div>
        </div>
      </div>
      <a-descriptions :column="4" size="small" class="job-meta">
        <a-descriptions-item label="任务 ID">{{ selected.id }}</a-descriptions-item><a-descriptions-item label="发起人">{{ selected.requested_by }}</a-descriptions-item><a-descriptions-item label="创建时间">{{ formatTime(selected.created_at) }}</a-descriptions-item><a-descriptions-item label="耗时">{{ duration(selected) }}</a-descriptions-item>
      </a-descriptions>
      <div class="log-heading"><span>实时部署日志</span><small>data/jobs/{{ selected.project }}/{{ selected.environment }}/{{ selected.id }}.log</small></div>
      <pre ref="terminalRef" class="terminal deployment-terminal">{{ logContent || '等待任务输出…' }}</pre>
    </a-card>

    <a-card>
      <template #title><span class="card-title">任务历史</span></template>
      <a-table :data="store.jobs" :loading="loading || store.loadingJobs || store.loadingEnvironment" row-key="id" :pagination="{ pageSize: 10 }" @row-click="selectRow">
        <template #columns>
          <a-table-column title="任务 ID" data-index="id"><template #cell="{ record }"><a-link @click.stop="selectJob(record)">{{ record.id }}</a-link></template></a-table-column>
          <a-table-column title="环境" :width="110"><template #cell="{ record }"><a-tag>{{ store.environmentLabel(record.environment) }}</a-tag></template></a-table-column>
          <a-table-column title="操作" data-index="action"><template #cell="{ record }">{{ actionName(record.action) }}</template></a-table-column>
          <a-table-column title="整体进度" :width="220"><template #cell="{ record }"><div class="table-progress"><a-progress :percent="(record.progress || 0) / 100" :show-text="false" size="small" /><span>{{ record.progress || 0 }}%</span></div></template></a-table-column>
          <a-table-column title="执行结果" :width="170"><template #cell="{ record }"><span class="success-text">成功 {{ record.success_steps || 0 }}</span><template v-if="record.failed_steps"><span> / </span><span :class="record.status === 'ignored' ? 'ignored-text' : 'danger-text'">{{ record.status === 'ignored' ? '已忽略' : '失败' }} {{ record.failed_steps }}</span></template></template></a-table-column>
          <a-table-column title="状态"><template #cell="{ record }"><a-tag :color="statusColor(record.status)">{{ statusName(record.status) }}</a-tag></template></a-table-column>
          <a-table-column title="创建时间"><template #cell="{ record }">{{ formatTime(record.created_at) }}</template></a-table-column>
          <a-table-column title="操作" :width="340"><template #cell="{ record }"><a-space><a-button size="mini" @click.stop="selectJob(record)">查看</a-button><a-button v-if="planCanDeploy(record)" size="mini" type="primary" @click.stop="openDeployAfterPlan(record)">确认部署</a-button><a-button v-if="canApplyAccessOnly(record)" size="mini" type="primary" :loading="accessing" @click.stop="applyAccessOnly(record)">仅接入配置</a-button><a-popconfirm v-if="record.status === 'failed' && requiresRepair(record)" content="确认已经完成状态修复或资源对账？" @ok="retry(record)"><a-button size="mini" type="primary" :loading="retrying" :disabled="retrying" @click.stop>修复后重试</a-button></a-popconfirm><a-button v-else-if="['failed','canceled','ignored'].includes(record.status)" size="mini" type="primary" :loading="retrying" :disabled="retrying" @click.stop="retry(record)">重试</a-button><a-button v-if="['failed','canceled'].includes(record.status) && record.action !== 'destroy'" size="mini" status="warning" @click.stop="openIgnore(record)">忽略</a-button><a-popconfirm v-if="['queued','running'].includes(record.status)" content="确认取消？" @ok="cancel(record)"><a-button size="mini" status="danger" @click.stop>取消</a-button></a-popconfirm></a-space></template></a-table-column>
        </template>
        <template #empty><a-empty description="还没有部署任务" /></template>
      </a-table>
    </a-card>
  </div>

  <a-modal v-model:visible="retryDestroyVisible" title="重试销毁任务" ok-text="验证并重试" :ok-loading="retrying" @before-ok="retryDestroy">
    <a-alert type="warning" show-icon>销毁属于高风险操作，重试前需要重新验证当前账号密码。</a-alert>
    <a-form :model="{}" layout="vertical" style="margin-top:16px"><a-form-item label="当前登录密码" required><a-input-password v-model="retryPassword" autocomplete="current-password" /></a-form-item></a-form>
  </a-modal>
  <a-modal v-model:visible="ignoreVisible" title="忽略本次失败记录" ok-text="确认忽略" :ok-loading="ignoring" @before-ok="ignoreFailure">
    <a-alert type="warning" show-icon>忽略只会解除页面红色失败提醒并保留审计记录，不会继续执行剩余步骤，也不会把部署结果标记为成功。销毁失败不能忽略。</a-alert>
    <a-form :model="{}" layout="vertical" style="margin-top:16px">
      <a-form-item label="忽略原因" required><a-textarea v-model="ignoreReason" :max-length="500" show-word-limit placeholder="例如：非关键测试检查，已人工核对实际资源状态" /></a-form-item>
      <a-form-item :label="`请输入 ${ignoreScope} 确认`" required><a-input v-model="ignoreConfirm" :placeholder="ignoreScope" autocomplete="off" /></a-form-item>
    </a-form>
  </a-modal>
  <a-modal v-model:visible="deployAfterPlanVisible" title="确认阶段1资源部署" ok-text="确认并开始部署" :ok-loading="deployingPlan" @before-ok="deployAfterPlan">
    <a-alert type="warning" show-icon>这会在 AWS 创建或更新可能产生费用的 VPC、EKS、云中间件与云数据库资源。执行时 Terraform 会基于最新云端状态重新核对变更。</a-alert>
    <a-form :model="{}" layout="vertical" style="margin-top:16px">
      <a-form-item :label="`请输入 ${deployPlanScope} 确认部署`" required>
        <a-input v-model="deployConfirm" :placeholder="deployPlanScope" autocomplete="off" />
      </a-form-item>
    </a-form>
  </a-modal>
</template>

<script setup lang="ts">
import { computed, nextTick, onUnmounted, ref, watch } from 'vue';
import { useRoute, useRouter } from 'vue-router';
import { Message } from '@arco-design/web-vue';
import { IconRefresh } from '@arco-design/web-vue/es/icon';
import { APIError, api } from '@/services/api';
import { visibleJobSteps } from '@/services/jobStepVisibility';
import { usePlatformStore } from '@/stores/platform';
import type { Job, JobDiagnosis, JobStatus } from '@/types';

const store = usePlatformStore();
const route = useRoute();
const router = useRouter();
const loading = ref(false);
const retrying = ref(false);
const accessing = ref(false);
const retryDestroyVisible = ref(false);
const retryTarget = ref<Job | null>(null);
const retryPassword = ref('');
const ignoreVisible = ref(false);
const ignoreTarget = ref<Job | null>(null);
const ignoreReason = ref('');
const ignoreConfirm = ref('');
const ignoring = ref(false);
const deployAfterPlanVisible = ref(false);
const deployPlan = ref<Job | null>(null);
const deployConfirm = ref('');
const deployingPlan = ref(false);
const selected = ref<Job | null>(null);
const logContent = ref('');
const logOffset = ref(0);
const terminalRef = ref<HTMLElement>();
let timer = 0;
let pollGeneration = 0;
let pollController: AbortController | null = null;
let scopeSelectionGeneration = 0;

const legacyStageNames: Record<string, string> = {
  'Initialize infra Terraform': '初始化 AWS 基础资源 Terraform',
  'Initialize platform Terraform': '初始化 EKS 平台组件 Terraform',
  'Prepare infra Terraform workspace': '选择 AWS 基础资源状态空间',
  'Prepare platform Terraform workspace': '选择 EKS 平台组件状态空间',
  'Apply infra Terraform': '创建或更新 AWS 基础资源',
  'Apply phase 1 base services': '阶段1 · 安装 EKS 基础组件与基础服务',
  'Apply phase 2 components and access configuration': '阶段2 · 安装组件并应用接入配置',
  'Update isolated kubeconfig': '更新当前环境 EKS 访问配置',
  'Verify Kubernetes nodes': '验证 EKS 节点是否可用',
  'Verify platform Pods': '验证平台组件 Pod 是否健康',
  'Destroy platform Terraform': '销毁 EKS 平台组件资源',
  'Destroy infra Terraform': '销毁 AWS 基础资源',
};
const stageName = (value: string) => legacyStageNames[value] || value;
const fallbackDiagnosis = (job: Job): JobDiagnosis => ({ code: 'unknown', title: '任务未完成', stage: failedStep(job) || actionName(job.action), cause: job.failure_hint || fallbackFailureHint(job), impact: '当前阶段未完成，前面已成功的步骤会保留。', suggestion: '查看下方技术错误和完整日志，优先处理第一个 Error。', retry: '确认原因已修复后再重试。' });
const selectedDiagnosis = computed(() => selected.value ? (selected.value.diagnosis || fallbackDiagnosis(selected.value)) : null);
const visibleSelectedSteps = computed(() => visibleJobSteps(selected.value?.steps, selected.value?.action || '', store.config));
const visibleSucceededSteps = computed(() => visibleSelectedSteps.value.filter((step) => step.status === 'succeeded').length);
const visibleFailedSteps = computed(() => visibleSelectedSteps.value.filter((step) => step.status === 'failed').length);
const deployPlanScope = computed(() => deployPlan.value ? `${deployPlan.value.project}/${deployPlan.value.environment}` : '项目/环境');
const ignoreScope = computed(() => ignoreTarget.value ? `${ignoreTarget.value.project}/${ignoreTarget.value.environment}` : '项目/环境');

const isCurrentScopeJob = (job: Job) => job.project === store.currentProjectKey && job.environment === store.currentEnvironmentKey;
const refresh = async () => {
  loading.value = true;
  try {
    await store.loadJobs();
    if (selected.value) selected.value = store.jobs.find((item) => item.id === selected.value?.id) || null;
    await synchronizeSelection(store.scopeRevision);
  } catch (error: any) { Message.error(error.message); }
  finally { loading.value = false; }
};
const selectJob = async (job: Job) => {
  if (!isCurrentScopeJob(job)) return;
  const scopeRevision = store.scopeRevision;
  stopPolling();
  selected.value = job;
  logContent.value = '';
  logOffset.value = 0;
  if (String(route.query.job || '') !== job.id) await router.replace({ query: { ...route.query, job: job.id } });
  if (scopeRevision !== store.scopeRevision || !isCurrentScopeJob(job)) return;
  void pollLog(pollGeneration, scopeRevision);
};
const selectRow = (record: any) => selectJob(record as Job);
const resetLog = () => { logContent.value = ''; logOffset.value = 0; stopPolling(); void pollLog(pollGeneration, store.scopeRevision); };
async function pollLog(generation: number, scopeRevision: number) {
  if (!selected.value || scopeRevision !== store.scopeRevision || !isCurrentScopeJob(selected.value)) return;
  const jobID = selected.value.id;
  const controller = new AbortController();
  pollController = controller;
  try {
    const [job, logs] = await Promise.all([
      api<Job>(`/api/jobs/${encodeURIComponent(jobID)}`, { signal: controller.signal }),
      api<{ data: string; next_offset: number; complete: boolean }>(`/api/jobs/${encodeURIComponent(jobID)}/logs?offset=${logOffset.value}`, { signal: controller.signal }),
    ]);
    if (generation !== pollGeneration || scopeRevision !== store.scopeRevision || selected.value?.id !== jobID || !isCurrentScopeJob(job)) return;
    selected.value = job; logContent.value += logs.data; logOffset.value = logs.next_offset;
    const jobIndex = store.jobs.findIndex((item) => item.id === job.id);
    if (jobIndex >= 0) store.jobs[jobIndex] = job;
    else store.jobs = [job, ...store.jobs];
    await nextTick(); if (terminalRef.value) terminalRef.value.scrollTop = terminalRef.value.scrollHeight;
    if (generation !== pollGeneration || scopeRevision !== store.scopeRevision || selected.value?.id !== jobID) return;
    if (!logs.complete) timer = window.setTimeout(() => void pollLog(generation, scopeRevision), 1000);
    else {
      void store.refreshProjects().catch(() => undefined);
      // Validation and planning do not change AWS resources. Avoid a needless
      // live cloud scan so their result page becomes available immediately.
      if (['deploy', 'platform', 'access', 'tls', 'storage_expand', 'storage_shrink', 'destroy'].includes(job.action)) {
        void Promise.allSettled([store.loadResources(true), store.loadStatus()]);
      }
    }
  } catch (error: any) {
    if (error?.name !== 'AbortError') Message.error(error.message);
  } finally {
    if (pollController === controller) pollController = null;
  }
}
function stopPolling() {
  pollGeneration += 1;
  window.clearTimeout(timer);
  timer = 0;
  pollController?.abort();
  pollController = null;
}
const cancel = async (job: Job) => {
  stopPolling();
  try {
    await api(`/api/jobs/${encodeURIComponent(job.id)}`, { method: 'DELETE' });
    Message.success('取消请求已发送，正在等待当前步骤安全退出');
    await refresh();
    if (selected.value?.id === job.id) void pollLog(pollGeneration, store.scopeRevision);
  } catch (error: any) {
    Message.error(error.message);
    if (selected.value?.id === job.id) void pollLog(pollGeneration, store.scopeRevision);
  }
};
const retry = async (job: Job) => {
  if (retrying.value) return;
  if (job.action === 'destroy') { retryTarget.value = job; retryPassword.value = ''; retryDestroyVisible.value = true; return; }
  stopPolling();
  retrying.value = true;
  try {
    const confirm = ['deploy', 'platform', 'access', 'tls'].includes(job.action) ? `${job.project}/${job.environment}` : '';
    const created = await api<Job>(`/api/jobs/${encodeURIComponent(job.id)}/retry`, { method: 'POST', body: JSON.stringify({ confirm }) });
    Message.success('重试任务已创建'); await refresh(); await selectJob(created);
  } catch (error:any) {
    Message.error(retryErrorMessage(error));
  }
  finally { retrying.value = false; }
};
const retryDestroy = async () => {
  if (!retryTarget.value || !retryPassword.value) { Message.warning('请输入当前登录密码'); return false; }
  retrying.value = true;
  try {
    const job = retryTarget.value;
    const created = await api<Job>(`/api/jobs/${encodeURIComponent(job.id)}/retry`, { method: 'POST', body: JSON.stringify({ confirm: `destroy:${job.project}/${job.environment}`, password: retryPassword.value }) });
    retryPassword.value = ''; retryTarget.value = null; Message.success('销毁重试任务已创建'); await refresh(); await selectJob(created); return true;
  } catch (error:any) { retryPassword.value = ''; Message.error(error.message); return false; }
  finally { retrying.value = false; }
};
const openIgnore = (job: Job) => {
  ignoreTarget.value = job;
  ignoreReason.value = '';
  ignoreConfirm.value = '';
  ignoreVisible.value = true;
};
const ignoreFailure = async () => {
  if (!ignoreTarget.value) return false;
  if (ignoreReason.value.trim().length < 3) { Message.warning('请填写至少 3 个字符的忽略原因'); return false; }
  if (ignoreConfirm.value.trim() !== ignoreScope.value) { Message.warning('忽略确认内容不匹配'); return false; }
  ignoring.value = true;
  try {
    const job = ignoreTarget.value;
    const ignored = await api<Job>(`/api/jobs/${encodeURIComponent(job.id)}/ignore`, {
      method: 'POST',
      body: JSON.stringify({ confirm: ignoreConfirm.value.trim(), reason: ignoreReason.value.trim() }),
    });
    ignoreVisible.value = false;
    ignoreTarget.value = null;
    ignoreReason.value = '';
    ignoreConfirm.value = '';
    Message.success('失败记录已标记为忽略，技术日志和失败步骤仍会保留');
    await refresh();
    await selectJob(ignored);
    return true;
  } catch (error: any) {
    Message.error(error.message);
    return false;
  } finally { ignoring.value = false; }
};
const openDeployAfterPlan = (job: Job) => {
  deployPlan.value = job;
  deployConfirm.value = '';
  deployAfterPlanVisible.value = true;
};
const deployAfterPlan = async () => {
  if (!deployPlan.value) return false;
  if (deployConfirm.value.trim() !== deployPlanScope.value) { Message.warning('部署确认内容不匹配'); return false; }
  deployingPlan.value = true;
  try {
    const created = await api<Job>('/api/jobs', {
      method: 'POST',
      body: JSON.stringify({ project: deployPlan.value.project, environment: deployPlan.value.environment, action: 'deploy', confirm: deployPlanScope.value }),
    });
    deployConfirm.value = '';
    deployPlan.value = null;
    Message.success('阶段1部署任务已创建');
    await refresh();
    await selectJob(created);
    return true;
  } catch (error: any) { Message.error(error.message); return false; }
  finally { deployingPlan.value = false; }
};
const clearHistory = async () => {
  try {
    const result = await api<{ deleted: number }>(`/api/jobs?project=${encodeURIComponent(store.currentProjectKey)}&environment=${encodeURIComponent(store.currentEnvironmentKey)}`, { method: 'DELETE' });
    selected.value = null; logContent.value = ''; stopPolling(); await router.replace({ query: { ...route.query, job: undefined } }); await refresh(); Message.success(`已清理 ${result.deleted} 条历史任务`);
  } catch (error:any) { Message.error(error.message); }
};
const failedStep = (job: Job) => stageName(job.steps?.find((step) => step.status === 'failed')?.name || '');
const fallbackFailureHint = (job: Job) => failedStep(job) ? `失败发生在“${failedStep(job)}”。请查看下方日志中的首个 Error，修正配置或外部依赖后点击“重试操作”。` : '请查看日志末尾与首个 Error，修正后点击“重试操作”。';
const retryErrorMessage = (error: any) => error instanceof APIError && error.status === 409
  ? `暂时不能重试：${error.message}。原任务和已创建资源会保留，无需手动删除；按提示处理后再重试即可。`
  : error?.message || '重试请求失败';
const requiresRepair = (job: Job) => ['helm_release_state_conflict', 'manager_restarted', 'terraform_state_locked', 'terraform_lockfile_readonly'].includes(job.diagnosis?.code || '');
const canApplyAccessOnly = (job: Job) => job.action === 'platform' && job.status === 'failed'
  && ['statefulset_immutable_upgrade', 'job_log_storage_unwritable'].includes(job.diagnosis?.code || '');
const applyAccessOnly = async (job: Job) => {
  if (accessing.value) return;
  stopPolling();
  accessing.value = true;
  try {
    const created = await api<Job>('/api/jobs', {
      method: 'POST',
      body: JSON.stringify({ project: job.project, environment: job.environment, action: 'access', confirm: `${job.project}/${job.environment}` }),
    });
    Message.success('已创建仅接入配置任务，不会升级或重建已安装组件');
    await refresh();
    await selectJob(created);
  } catch (error: any) {
    Message.error(error?.message || '创建接入配置任务失败');
  } finally { accessing.value = false; }
};
const planCanDeploy = (job: Job) => job.action === 'plan' && job.status === 'succeeded' && !store.jobs.some((item) =>
  item.id !== job.id && new Date(item.created_at).getTime() > new Date(job.created_at).getTime()
  && ['validate', 'plan', 'deploy', 'destroy'].includes(item.action),
);
const statusCount = (status: JobStatus) => store.jobs.filter((item) => item.status === status).length;
const actionName = (value: string) => ({ validate: '校验配置', plan: '阶段1生成计划', deploy: '阶段1基础部署', platform: '阶段2组件与接入', access: '阶段2域名/TLS/告警接入', tls: '阶段2 TLS证书', storage_expand: '日志平台存储扩容', storage_shrink: '日志平台安全缩容', destroy: '销毁环境' }[value] || value);
const statusName = (value: string) => ({ queued: '排队中', running: '执行中', succeeded: '成功', failed: '失败', canceled: '已取消', ignored: '已忽略' }[value] || value);
const stepStatusName = (value: string) => ({ pending: '等待执行', running: '正在执行', succeeded: '成功', failed: '失败' }[value] || value);
const statusColor = (value: string) => ({ queued: 'orange', running: 'arcoblue', succeeded: 'green', failed: 'red', canceled: 'gray', ignored: 'orange' }[value] || 'gray');
const formatTime = (value?: string) => value ? new Date(value).toLocaleString('zh-CN') : '—';
const duration = (job: Job) => { if (!job.started_at) return '—'; const end = job.finished_at ? new Date(job.finished_at).getTime() : Date.now(); const seconds = Math.max(0, Math.round((end - new Date(job.started_at).getTime()) / 1000)); return seconds < 60 ? `${seconds}s` : `${Math.floor(seconds / 60)}m ${seconds % 60}s`; };

function resetScopeSelection() {
  scopeSelectionGeneration += 1;
  stopPolling();
  selected.value = null;
  logContent.value = '';
  logOffset.value = 0;
  retryTarget.value = null;
  retryPassword.value = '';
  retryDestroyVisible.value = false;
  ignoreVisible.value = false;
  ignoreTarget.value = null;
  ignoreReason.value = '';
  ignoreConfirm.value = '';
  deployPlan.value = null;
  deployConfirm.value = '';
  deployAfterPlanVisible.value = false;
}

async function synchronizeSelection(revision: number) {
  if (!store.initialized || store.loadingEnvironment || revision !== store.scopeRevision) return;
  const generation = ++scopeSelectionGeneration;
  if (!store.scopeKey) {
    resetScopeSelection();
    if (route.query.job) {
      const query = { ...route.query };
      delete query.job;
      await router.replace({ query });
    }
    return;
  }
  const queryID = String(route.query.job || '');
  const current = selected.value && isCurrentScopeJob(selected.value)
    ? store.jobs.find((item) => item.id === selected.value?.id)
    : undefined;
  const initial = store.jobs.find((item) => item.id === queryID)
    || current
    || store.jobs.find((item) => ['running', 'queued'].includes(item.status))
    || store.jobs[0];
  if (generation !== scopeSelectionGeneration || revision !== store.scopeRevision) return;
  if (initial) {
    if (selected.value?.id === initial.id) selected.value = initial;
    else await selectJob(initial);
    return;
  }
  resetScopeSelection();
  if (route.query.job) {
    const query = { ...route.query };
    delete query.job;
    await router.replace({ query });
  }
}

watch(() => store.scopeRevision, resetScopeSelection, { flush: 'sync' });
watch(
  [() => store.initialized, () => store.scopeRevision, () => store.loadingEnvironment, () => String(route.query.job || '')],
  ([initialized, revision, environmentLoading]) => {
    if (initialized && !environmentLoading) void synchronizeSelection(Number(revision));
  },
  { immediate: true },
);
onUnmounted(stopPolling);
</script>

<style scoped>
.plan-ready-alert { margin:0 0 16px; }
.job-diagnosis { margin:16px 0; overflow:hidden; border:1px solid #f3a6a2; border-radius:8px; background:#fffafa; }
.diagnosis-heading { padding:14px 16px; display:flex; align-items:center; justify-content:space-between; border-bottom:1px solid #fde2e1; background:#fff3f2; }
.diagnosis-heading > div { display:flex; align-items:center; gap:10px; }.diagnosis-heading small { color:var(--color-text-3); }.diagnosis-heading h3 { margin:2px 0 0; color:#c91f1f; font-size:16px; }
.diagnosis-icon { width:30px; height:30px; display:grid; place-items:center; color:#fff; border-radius:50%; background:#f53f3f; font-weight:800; }
.diagnosis-stage { padding:10px 16px; display:flex; gap:12px; border-bottom:1px solid #f2f3f5; }.diagnosis-stage span { color:var(--color-text-3); }.diagnosis-stage strong { color:var(--color-text-1); }
.diagnosis-grid { padding:14px 16px; display:grid; grid-template-columns:1fr 1fr; gap:14px 20px; }.diagnosis-grid > div { min-width:0; }.diagnosis-grid label { color:var(--color-text-3); font-size:11px; }.diagnosis-grid p { margin:5px 0 0; color:var(--color-text-1); line-height:1.65; }.diagnosis-action { padding:10px 12px; border-radius:6px; background:#f2f7ff; }.diagnosis-action label { color:#165dff; }
.technical-error { margin:0 16px 14px; border-top:1px solid #f2f3f5; }.technical-error summary { padding:10px 0 4px; color:var(--color-text-3); cursor:pointer; }.technical-error pre { max-height:220px; margin:6px 0 0; padding:10px; overflow:auto; border-radius:6px; background:#1f2329; color:#d9e1ec; font:11px/1.55 ui-monospace,SFMono-Regular,Menlo,monospace; white-space:pre-wrap; overflow-wrap:anywhere; }
.ignored-text { color:#ff7d00; }
@media (max-width: 900px) { .diagnosis-grid { grid-template-columns:1fr; } }
</style>

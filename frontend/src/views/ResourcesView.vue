<template>
  <div>
    <div class="page-header">
      <div><h2>环境资源与访问</h2><p>{{ store.currentProject?.display_name }} / {{ store.currentEnvironment?.display_name }}：统一查看云资源、自建组件、域名和受控凭据</p></div>
      <a-button :loading="store.loadingResources" :disabled="!store.currentEnvironment" @click="refresh"><icon-refresh />同步资源</a-button>
    </div>
    <a-alert v-if="snapshot?.cloud_sync" :type="cloudSyncAlertType" show-icon class="full-card">
      <template #title>{{ cloudSyncTitle }}</template>
      {{ cloudSyncDescription }}
    </a-alert>
    <a-alert v-for="warning in snapshot?.warnings || []" :key="warning" type="warning" show-icon class="full-card">{{ warning }}</a-alert>
	    <a-card v-if="snapshot?.resources?.length" class="full-card">
      <template #title><span class="card-title">环境基础信息</span></template>
      <a-descriptions :column="4" bordered size="small">
        <a-descriptions-item label="AWS账号">{{ snapshot.info.aws_account_id || '部署后获取' }}</a-descriptions-item>
        <a-descriptions-item label="Region">{{ snapshot.info.region }}</a-descriptions-item>
        <a-descriptions-item label="VPC">{{ snapshot.info.vpc_id || '部署后获取' }}</a-descriptions-item>
        <a-descriptions-item label="VPC CIDR">{{ snapshot.info.vpc_cidr }}</a-descriptions-item>
        <a-descriptions-item label="EKS">{{ snapshot.info.cluster_name }}</a-descriptions-item>
        <a-descriptions-item label="资源网络"><a-tag :color="snapshot.info.network_mode === 'public' ? 'arcoblue' : 'purple'">{{ snapshot.info.network_mode === 'public' ? 'Public 子网私网地址' : 'Private 子网' }}</a-tag></a-descriptions-item>
        <a-descriptions-item label="Availability Zones">{{ snapshot.info.availability_zones.join('、') }}</a-descriptions-item>
        <a-descriptions-item label="Namespaces">{{ snapshot.info.namespaces.join('、') }}</a-descriptions-item>
        <a-descriptions-item label="NAT Gateway">{{ natGatewayText }}</a-descriptions-item>
        <a-descriptions-item label="NAT 固定出口 IP">{{ Object.values(snapshot.info.nat_gateway_ips || {}).join('、') || '未创建' }}</a-descriptions-item>
      </a-descriptions>
    </a-card>

	    <a-tabs v-if="snapshot?.resources?.length" v-model:active-key="sourceFilter" type="rounded" class="resource-tabs">
      <a-tab-pane key="all" title="全部资源" />
      <a-tab-pane key="cloud" title="AWS 云服务" />
      <a-tab-pane key="self-hosted" title="自建组件" />
    </a-tabs>
    <div v-if="filteredResources.length" class="resource-grid">
      <a-card v-for="resource in filteredResources" :key="resource.key" class="resource-card" hoverable>
        <template #title>
          <div class="resource-title"><span class="resource-symbol">{{ resource.source === 'cloud' ? 'AWS' : 'K8S' }}</span><div><strong>{{ resource.display_name }}</strong><small>{{ resource.category }} · {{ resource.provider }}</small></div></div>
        </template>
        <template #extra><a-badge :status="statusBadge(resource.status)" :text="statusText(resource.status)" /></template>
        <a-space wrap class="resource-meta"><a-tag v-if="resource.version">{{ resource.version }}</a-tag><a-tag v-if="resource.specification" color="blue">{{ resource.specification }}</a-tag><a-tag v-if="resource.namespace" color="purple">{{ resource.namespace }}</a-tag></a-space>
        <div v-if="resource.configuration?.length" class="configuration-section">
          <div class="section-caption">AWS 实际参数</div>
          <div class="configuration-list">
            <div v-for="field in resource.configuration" :key="field.path" class="configuration-row">
              <span>{{ field.label }}</span>
              <div><code>{{ configurationValue(field.actual) }}</code><a-tag size="small" :color="configurationStateColor(field.state)">{{ configurationStateText(field.state) }}</a-tag></div>
              <small v-if="field.state !== 'synced'">平台期望：{{ configurationValue(field.desired) }}</small>
            </div>
          </div>
        </div>
        <div class="access-section">
          <div class="section-caption">访问入口</div>
          <div v-if="resource.access_points?.length" class="access-list">
            <div v-for="point in resource.access_points" :key="`${point.name}-${point.url || point.host}`" class="access-row">
              <div><span>{{ point.name }}</span><code>{{ accessValue(point) }}</code></div>
              <a-space><a-tag size="small" :color="point.visibility === 'public' ? 'arcoblue' : 'gray'">{{ point.visibility === 'public' ? '公网入口' : '集群/VPC内' }}</a-tag><a-button size="mini" @click="copy(accessValue(point))"><icon-copy /></a-button><a-button v-if="point.url?.startsWith('http')" size="mini" type="primary" @click="open(point.url)"><icon-launch /></a-button></a-space>
            </div>
          </div>
          <a-empty v-else description="部署完成后自动采集访问地址" />
        </div>
        <div v-if="resource.credentials?.length" class="credential-section">
          <div class="section-caption">访问凭据</div>
          <div v-for="credential in resource.credentials" :key="credential.id" class="credential-row">
            <div><span>{{ credential.label }}</span><code>{{ credential.username || '用户名随密钥返回' }} / ••••••••••••</code></div>
            <a-button size="small" :status="credential.available ? 'normal' : 'warning'" :loading="revealingCredentialID === credential.id" :disabled="!credential.available || !store.canViewSecrets || Boolean(revealingCredentialID)" @click="reveal(credential)"><icon-eye />{{ credential.available ? '查看凭据' : '凭据不可用' }}</a-button>
          </div>
          <small v-if="!store.canViewSecrets" class="secret-help">当前账号没有项目凭据查看权限</small>
        </div>
      </a-card>
    </div>
	    <a-empty v-else description="尚未检测到已部署资源；仅保存或勾选配置不会出现在这里">
	      <a-button type="primary" @click="refresh"><icon-refresh />重新同步实际资源</a-button>
	    </a-empty>
  </div>

  <a-modal v-model:visible="secretVisible" title="访问凭据" :footer="false" @cancel="clearSecret">
    <a-alert type="success" show-icon>无需再次验证密码；凭据会持续显示，关闭窗口或切换项目、环境后隐藏。</a-alert>
    <a-descriptions :column="1" bordered style="margin-top:16px">
      <a-descriptions-item v-for="(value, key) in secretValues" :key="key" :label="String(key)"><div class="secret-value"><code>{{ value }}</code><a-button size="mini" @click="copy(value)"><icon-copy /></a-button></div></a-descriptions-item>
    </a-descriptions>
  </a-modal>
</template>

<script setup lang="ts">
import { computed, ref, watch } from 'vue';
import { Message } from '@arco-design/web-vue';
import { IconCopy, IconEye, IconLaunch, IconRefresh } from '@arco-design/web-vue/es/icon';
import { api } from '@/services/api';
import { copyToClipboard } from '@/services/clipboard';
import { usePlatformStore } from '@/stores/platform';
import type { ResourceAccessPoint, ResourceCredential } from '@/types';

const store = usePlatformStore();
const sourceFilter = ref('all');
const secretVisible = ref(false);
const revealingCredentialID = ref('');
const secretValues = ref<Record<string, string>>({});
let revealGeneration = 0;
let freshnessRequestedScope = '';
const snapshot = computed(() => store.resources);
const filteredResources = computed(() => (snapshot.value?.resources || []).filter((item) => sourceFilter.value === 'all' || item.source === sourceFilter.value));
const cloudSyncAlertType = computed(() => ({ synced: 'success', pending: 'info', drifted: 'warning', conflict: 'error', unavailable: 'warning' }[snapshot.value?.cloud_sync?.status || ''] || 'info') as any);
const cloudSyncTitle = computed(() => ({ synced: 'AWS 实际配置已同步', pending: '存在待部署的平台修改', drifted: '检测到 AWS 控制台变更', conflict: '平台与 AWS 同时修改，存在冲突', unavailable: '部分 AWS 实际参数暂不可用' }[snapshot.value?.cloud_sync?.status || ''] || 'AWS 配置状态'));
const cloudSyncDescription = computed(() => {
  const sync = snapshot.value?.cloud_sync;
  if (!sync) return '';
  return `同步 ${sync.synced_fields} 项 · 待部署 ${sync.pending_fields} 项 · AWS漂移 ${sync.drifted_fields} 项 · 冲突 ${sync.conflict_fields} 项。实际参数采集时间：${sync.observed_at ? new Date(sync.observed_at).toLocaleString() : '尚未采集'}`;
});
const natGatewayText = computed(() => {
  const mode = snapshot.value?.info.nat_gateway_mode;
  const count = Object.keys(snapshot.value?.info.nat_gateway_ips || {}).length;
  if (mode === 'external') return '已有 VPC 外部管理';
  if (count > 0) return `${count} 个 · ${mode === 'always' ? '始终创建' : 'Private 网络按需创建'}`;
  if (mode === 'disabled') return '已关闭';
  return '按需 · 当前未创建';
});

const refresh = async () => { try { await store.loadResources(true); Message.success('资源目录已同步'); } catch (error: any) { Message.error(error.message); } };
const accessValue = (point: ResourceAccessPoint) => point.url || `${point.protocol}://${point.host}${point.port ? `:${point.port}` : ''}`;
const copy = async (value: string) => {
  try { await copyToClipboard(value); Message.success('已复制'); }
  catch { Message.error('复制失败，请手动选择内容'); }
};
const open = (url?: string) => { if (url) window.open(url, '_blank', 'noopener,noreferrer'); };
const statusBadge = (value: string) => ({ healthy: 'success', pending: 'processing', missing: 'danger', drift: 'warning', disabled: 'normal' }[value] || 'normal') as any;
const statusText = (value: string) => ({ healthy: '可用', pending: '等待部署', missing: '缺失', drift: '配置漂移', disabled: '未启用' }[value] || value);
const configurationStateColor = (value: string) => ({ synced: 'green', pending: 'arcoblue', drifted: 'orange', conflict: 'red' }[value] || 'gray');
const configurationStateText = (value: string) => ({ synced: '一致', pending: '待部署', drifted: 'AWS已变更', conflict: '冲突' }[value] || value);
const configurationValue = (value: unknown) => {
  if (Array.isArray(value)) return value.join('、');
  if (value === true) return '开启';
  if (value === false) return '关闭';
  if (value === null || value === undefined || value === '') return '—';
  return typeof value === 'object' ? JSON.stringify(value) : String(value);
};
const reveal = async (credential: ResourceCredential) => {
  if (revealingCredentialID.value || !credential.available || !store.canViewSecrets) return;
  const revision = store.scopeRevision;
  const projectKey = store.currentProjectKey;
  const environmentKey = store.currentEnvironmentKey;
  const credentialID = credential.id;
  const generation = ++revealGeneration;
  revealingCredentialID.value = credentialID;
  clearSecret();
  try {
    const response = await api<{ values: Record<string, string> }>(`/api/projects/${encodeURIComponent(projectKey)}/environments/${encodeURIComponent(environmentKey)}/credentials/${encodeURIComponent(credentialID)}/reveal`, { method: 'POST', body: '{}' });
    if (revision !== store.scopeRevision) return false;
    secretValues.value = response.values;
    secretVisible.value = true;
    return true;
  } catch (error: any) {
    if (generation === revealGeneration && revision === store.scopeRevision) {
      Message.error(error.message);
      if (error?.status === 404) void store.loadResources().catch(() => undefined);
    }
    return false;
  }
  finally { if (generation === revealGeneration) revealingCredentialID.value = ''; }
};
function clearSecret() { secretValues.value = {}; secretVisible.value = false; }
watch(() => store.scopeRevision, () => {
  revealGeneration += 1;
  clearSecret();
  sourceFilter.value = 'all';
  revealingCredentialID.value = '';
}, { flush: 'sync' });
watch(
  () => [store.currentProjectKey, store.currentEnvironmentKey, store.scopeRevision, store.loadingResources, snapshot.value?.observed_at] as const,
  ([projectKey, environmentKey, revision, loading, observedAt]) => {
    if (!projectKey || !environmentKey || loading || !snapshot.value) return;
    const observed = Date.parse(String(observedAt || ''));
    if (Number.isFinite(observed) && Date.now() - observed < 2 * 60 * 1000) return;
    const scope = `${projectKey}/${environmentKey}/${revision}`;
    if (freshnessRequestedScope === scope) return;
    freshnessRequestedScope = scope;
    // Render the persisted snapshot immediately, then reconcile stale cards in
    // the background so opening this page remains fast and authoritative.
    void store.loadResources(true).catch((error: any) => Message.warning(`实时资源同步失败：${error.message}`));
  },
  { immediate: true },
);
</script>

<style scoped>
.configuration-section { margin-top: 16px; }
.configuration-list { display: grid; grid-template-columns: repeat(2, minmax(0, 1fr)); gap: 8px; }
.configuration-row { min-width: 0; padding: 9px 10px; border: 1px solid var(--color-border-2); border-radius: 8px; background: var(--color-fill-1); }
.configuration-row > span { display: block; margin-bottom: 5px; color: var(--color-text-2); font-size: 12px; }
.configuration-row > div { display: flex; align-items: center; justify-content: space-between; gap: 8px; }
.configuration-row code { overflow: hidden; color: var(--color-text-1); text-overflow: ellipsis; white-space: nowrap; }
.configuration-row small { display: block; overflow: hidden; margin-top: 5px; color: rgb(var(--orange-6)); text-overflow: ellipsis; white-space: nowrap; }
@media (max-width: 900px) { .configuration-list { grid-template-columns: 1fr; } }
</style>

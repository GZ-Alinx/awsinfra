<template>
  <div class="ingress-page">
    <div class="page-head">
      <div class="page-head__content">
        <div class="page-kicker"><span class="live-dot" />Kubernetes 网络入口</div>
        <h2>Ingress 管理</h2>
        <p>集中查看、创建和在线维护当前 EKS 环境中的域名、路径、后端服务与 TLS 转发规则。</p>
        <div class="scope-tags">
          <a-tag color="arcoblue"><icon-apps />项目：{{ store.currentProject?.display_name || '未选择' }}</a-tag>
          <a-tag color="purple"><icon-cloud />环境：{{ store.currentEnvironment?.display_name || '未选择' }}</a-tag>
          <a-tag><icon-storage />{{ namespaces.length }} 个可管理 Namespace</a-tag>
        </div>
      </div>
      <a-space class="page-actions">
        <a-button :loading="loading" @click="loadIngresses"><icon-refresh />刷新</a-button>
        <a-button
          type="outline"
          :loading="syncingConfig"
          :disabled="!canMutate || !store.currentEnvironment || loading"
          @click="confirmSyncConfig"
        >
          <icon-refresh />同步到部署配置
        </a-button>
        <a-button type="primary" :disabled="!canMutate || !store.currentEnvironment" @click="openConfiguredDomain()">
          <icon-plus />新增域名路由
        </a-button>
      </a-space>
    </div>

    <a-alert type="info" show-icon class="scope-alert">
      本页会同时对比项目部署配置与 EKS 实际 Ingress。“同步到部署配置”按域名聚合集群中的多条路径：EKS 路由更多时回填到项目配置，项目配置更多或数量相同时保留项目配置；不会自动导入不属于当前项目域名的对象。
    </a-alert>

    <a-row :gutter="14" class="summary-row">
      <a-col :xs="12" :sm="12" :md="6">
        <a-card class="summary-card summary-card--blue">
          <div class="summary-card__icon"><icon-apps /></div>
          <div><span>统一路由视图</span><strong>{{ ingresses.length }}</strong><small>期望配置与集群实际合并</small></div>
        </a-card>
      </a-col>
      <a-col :xs="12" :sm="12" :md="6">
        <a-card class="summary-card summary-card--green">
          <div class="summary-card__icon"><icon-cloud /></div>
          <div><span>配置已同步</span><strong>{{ syncedCount }}</strong><small>配置与 EKS 实际一致</small></div>
        </a-card>
      </a-col>
      <a-col :xs="12" :sm="12" :md="6">
        <a-card class="summary-card summary-card--purple">
          <div class="summary-card__icon"><icon-safe /></div>
          <div><span>待处理</span><strong>{{ attentionCount }}</strong><small>待部署或发生配置漂移</small></div>
        </a-card>
      </a-col>
      <a-col :xs="12" :sm="12" :md="6">
        <a-card class="summary-card summary-card--orange">
          <div class="summary-card__icon"><icon-storage /></div>
          <div><span>仅集群存在</span><strong>{{ clusterOnlyCount }}</strong><small>尚未纳入部署配置</small></div>
        </a-card>
      </a-col>
    </a-row>

    <a-card class="ingress-list-card">
      <div class="list-heading">
        <div>
          <h3>路由规则</h3>
          <p>平台托管规则以部署配置为准；手工规则只存在于集群，建议逐步清理或迁移。</p>
        </div>
        <div class="observed-time">
          <span>{{ observedAt ? `最近同步 ${formatTime(observedAt)}` : '尚未从集群同步' }}</span>
          <strong>{{ filteredIngresses.length }} / {{ ingresses.length }}</strong>
        </div>
      </div>

      <div class="filter-row">
        <div class="filter-controls">
          <a-input-search v-model="filter.keyword" allow-clear placeholder="搜索名称、域名或后端 Service" class="search-control" />
          <a-select v-model="filter.namespace" allow-clear placeholder="全部 Namespace" style="width:210px">
            <a-option v-for="namespace in namespaces" :key="namespace" :value="namespace">{{ namespace }}</a-option>
          </a-select>
          <a-select v-model="filter.className" allow-clear placeholder="全部 IngressClass" style="width:190px">
            <a-option v-for="className in classNames" :key="className" :value="className">{{ className }}</a-option>
          </a-select>
          <a-button v-if="hasActiveFilters" type="text" @click="resetFilters">清空筛选</a-button>
        </div>
        <a-tag :color="loading ? 'orange' : 'green'">
          <span class="status-dot" :class="{ 'status-dot--loading': loading }" />
          {{ loading ? '正在同步集群' : '集群数据已同步' }}
        </a-tag>
      </div>

      <a-table
        class="ingress-table"
        :data="filteredIngresses"
        :loading="loading"
        :pagination="{ pageSize: 12, showTotal: true, showPageSize: true, pageSizeOptions: [12, 24, 48] }"
        row-key="row_key"
        :scroll="{ x: 1450 }"
        :bordered="{ cell: false }"
      >
        <template #columns>
          <a-table-column title="Ingress" :width="225" fixed="left">
            <template #cell="{ record }">
              <div class="identity-cell">
                <strong>{{ record.name }}</strong>
                <span><icon-storage />{{ record.namespace }}</span>
              </div>
            </template>
          </a-table-column>
          <a-table-column title="同步状态" :width="150">
            <template #cell="{ record }">
              <div class="sync-status-cell">
                <a-tag :color="syncStatusMeta(record).color">
                  <span class="status-dot" :class="syncStatusMeta(record).dotClass" />
                  {{ syncStatusMeta(record).label }}
                </a-tag>
                <small>{{ syncStatusMeta(record).hint }}</small>
              </div>
            </template>
          </a-table-column>
          <a-table-column title="IngressClass" :width="145">
            <template #cell="{ record }"><a-tag color="arcoblue" bordered>{{ record.class_name || '未设置' }}</a-tag></template>
          </a-table-column>
          <a-table-column title="域名" :width="275">
            <template #cell="{ record }">
              <div v-if="record.hosts.length" class="stacked-values">
                <a-tooltip v-for="host in record.hosts" :key="host" :content="`点击复制 ${host}`">
                  <a-link class="domain-link" @click="copyText(host)">{{ host }}</a-link>
                </a-tooltip>
              </div>
              <span v-else class="muted-text">全部 Host / IP 入口</span>
            </template>
          </a-table-column>
          <a-table-column title="路径与后端 Service" :width="360">
            <template #cell="{ record }">
              <div class="backend-list">
                <div v-for="(path, index) in record.paths.slice(0, 3)" :key="`${path.host}-${path.path}-${index}`">
                  <code>{{ path.path || '/' }}</code><span class="route-arrow">→</span>
                  <span :title="`${path.service_namespace || record.namespace}/${path.service_name}:${path.service_port}`">
                    {{ path.service_namespace || record.namespace }}/{{ path.service_name }}:{{ path.service_port }}
                  </span>
                </div>
                <small v-if="record.paths.length > 3">另有 {{ record.paths.length - 3 }} 条路径</small>
                <span v-if="!record.paths.length" class="muted-text">未发现有效后端路径</span>
              </div>
            </template>
          </a-table-column>
          <a-table-column title="TLS" :width="180">
            <template #cell="{ record }">
              <div v-if="record.tls_secrets.length" class="stacked-values">
                <a-tag v-for="secret in record.tls_secrets" :key="secret" color="green">{{ secret }}</a-tag>
              </div>
              <a-tag v-else color="gray">未启用</a-tag>
            </template>
          </a-table-column>
          <a-table-column title="负载均衡地址" :width="260">
            <template #cell="{ record }">
              <div v-if="record.addresses.length" class="address-cell">
                <a-tooltip v-for="address in record.addresses" :key="address" :content="address">
                  <a-link @click="copyText(address)">{{ address }}</a-link>
                </a-tooltip>
              </div>
              <a-tag v-else color="orange">等待地址</a-tag>
            </template>
          </a-table-column>
          <a-table-column title="管理来源" :width="135">
            <template #cell="{ record }">
              <a-tag :color="record.desired ? 'arcoblue' : record.managed_by === 'ingress-editor' ? 'purple' : 'gray'">
                {{ record.desired ? '部署配置' : record.managed_by === 'ingress-editor' ? '在线编辑器' : '外部创建' }}
              </a-tag>
            </template>
          </a-table-column>
          <a-table-column title="操作" :width="190" fixed="right" align="right">
            <template #cell="{ record }">
              <a-space class="ingress-actions" :size="8">
                <a-button v-if="record.desired" size="mini" type="primary" @click="openConfiguredDomain(record)">编辑部署配置</a-button>
                <a-button v-else size="mini" type="outline" @click="openEdit(record)">YAML 编辑</a-button>
                <a-popconfirm
                  v-if="!record.desired"
                  :content="`确认删除 ${record.namespace}/${record.name}？该操作会立即影响线上路由。`"
                  ok-text="确认删除"
                  @ok="removeIngress(record)"
                >
                  <a-button size="mini" status="danger" :disabled="!canMutate"><icon-delete /></a-button>
                </a-popconfirm>
              </a-space>
            </template>
          </a-table-column>
        </template>
        <template #empty>
          <div class="empty-state">
            <a-empty :description="store.currentEnvironment ? '当前环境尚无 Ingress 路由' : '请先选择项目和环境'" />
            <a-button
              v-if="store.currentEnvironment && canMutate"
              type="primary"
              size="small"
              @click="openConfiguredDomain()"
            ><icon-plus />创建第一条部署路由</a-button>
          </div>
        </template>
      </a-table>
    </a-card>

    <a-modal
      v-model:visible="editorVisible"
      :title="editorMode === 'create' ? '创建 Ingress' : `编辑 ${editor.originalNamespace}/${editor.originalName}`"
      width="min(1480px, 95vw)"
      modal-class="ingress-editor-modal"
      body-class="ingress-editor-body"
      :body-style="{ maxHeight: 'calc(100vh - 150px)', overflow: 'auto', padding: '20px 24px 24px' }"
      :mask-closable="false"
      :footer="false"
      unmount-on-close
    >
      <a-spin :loading="editorLoading" style="width:100%">
        <div v-if="editorMode === 'create'" class="create-builder">
          <div class="builder-heading">
            <div>
              <strong>快速生成路由</strong>
              <span>选择后端服务和协议后生成标准 Ingress YAML，也可以继续在下方手动调整。</span>
            </div>
            <a-tag color="arcoblue">表单生成</a-tag>
          </div>
          <a-form :model="draft" layout="vertical">
            <a-grid :cols="4" :col-gap="14">
              <a-grid-item><a-form-item label="Namespace" required><a-select v-model="draft.namespace" @change="draft.service = ''; draft.servicePort = ''"><a-option v-for="namespace in namespaces" :key="namespace" :value="namespace">{{ namespace }}</a-option></a-select></a-form-item></a-grid-item>
              <a-grid-item><a-form-item label="Ingress 名称" required><a-input v-model="draft.name" placeholder="例如 game-api" /></a-form-item></a-grid-item>
              <a-grid-item><a-form-item label="IngressClass" required><a-select v-model="draft.className" allow-create allow-search><a-option value="higress">Higress</a-option><a-option value="nginx">NGINX Ingress</a-option><a-option value="nginx-ingress">F5 NGINX Ingress</a-option></a-select></a-form-item></a-grid-item>
              <a-grid-item><a-form-item label="域名" extra="留空表示不限制 Host"><a-input v-model="draft.host" placeholder="api.example.com" /></a-form-item></a-grid-item>
              <a-grid-item><a-form-item label="访问路径" required><a-input v-model="draft.path" placeholder="/" /></a-form-item></a-grid-item>
              <a-grid-item><a-form-item label="后端 Service" required><a-select v-model="draft.service" allow-search :loading="servicesLoading" @popup-visible-change="ensureServices"><a-option v-for="service in namespaceServices" :key="service.name" :value="service.name">{{ service.name }}</a-option></a-select></a-form-item></a-grid-item>
              <a-grid-item><a-form-item label="Service 端口" required><a-select v-model="draft.servicePort" allow-search><a-option v-for="port in selectedServicePorts" :key="`${port.name}-${port.port}`" :value="port.port">{{ port.name ? `${port.name} · ` : '' }}{{ port.port }}</a-option></a-select></a-form-item></a-grid-item>
              <a-grid-item><a-form-item label="后端协议"><a-select v-model="draft.backendProtocol"><a-option value="http">HTTP / WebSocket</a-option><a-option value="https">HTTPS</a-option><a-option value="grpc">gRPC / h2c</a-option><a-option value="grpcs">gRPCS</a-option></a-select></a-form-item></a-grid-item>
              <a-grid-item><a-form-item label="启用 TLS"><a-switch v-model="draft.tlsEnabled" /></a-form-item></a-grid-item>
              <a-grid-item v-if="draft.tlsEnabled" :span="2"><a-form-item label="TLS Secret" required><a-select v-model="draft.tlsSecret" allow-create allow-search placeholder="选择已有证书或输入 Secret 名称"><a-option v-for="secret in availableTLSSecrets" :key="secret" :value="secret">{{ secret }}</a-option></a-select></a-form-item></a-grid-item>
              <a-grid-item><a-form-item label="生成 YAML"><a-button long @click="generateYAML">根据表单刷新 YAML</a-button></a-form-item></a-grid-item>
            </a-grid>
          </a-form>
        </div>

        <a-alert v-else type="info" show-icon class="editor-alert">
          保存时会检查资源版本；若该 Ingress 已被部署任务或其他用户更新，平台会拒绝覆盖并要求重新加载。
        </a-alert>

        <div class="yaml-toolbar">
          <div>
            <strong>Ingress YAML</strong>
            <span>仅允许单个 networking.k8s.io/v1 Ingress；禁止其他资源类型和高风险 snippet 注解。</span>
            <div class="yaml-meta">
              <a-tag size="small">{{ yamlLineCount }} 行</a-tag>
              <a-tag size="small">{{ yamlSizeText }}</a-tag>
              <a-tag size="small" color="green">保存前服务端安全校验</a-tag>
            </div>
          </div>
          <a-space>
            <a-button :loading="validating" @click="validateYAML">校验并格式化</a-button>
            <a-button type="primary" :loading="saving" :disabled="!canMutate" @click="saveIngress">
              <icon-save />{{ editorMode === 'create' ? '创建并应用' : '保存到集群' }}
            </a-button>
          </a-space>
        </div>
        <a-textarea
          v-model="editor.yaml"
          class="yaml-editor"
          :auto-size="{ minRows: 28, maxRows: 42 }"
          placeholder="apiVersion: networking.k8s.io/v1"
          spellcheck="false"
        />
        <div v-if="validationWarnings.length" class="validation-warnings">
          <a-alert v-for="warning in validationWarnings" :key="warning" type="warning" show-icon>{{ warning }}</a-alert>
        </div>
      </a-spin>
    </a-modal>
  </div>
</template>

<script setup lang="ts">
import { computed, onMounted, reactive, ref, watch } from 'vue';
import { useRouter } from 'vue-router';
import { Message, Modal } from '@arco-design/web-vue';
import {
  IconApps,
  IconCloud,
  IconDelete,
  IconPlus,
  IconRefresh,
  IconSafe,
  IconSave,
  IconStorage,
} from '@arco-design/web-vue/es/icon';
import { stringify } from 'yaml';
import { api } from '@/services/api';
import { copyToClipboard } from '@/services/clipboard';
import { usePlatformStore } from '@/stores/platform';
import type { Dict, KubernetesIngress, KubernetesIngressDocument, KubernetesIngressValidation } from '@/types';

type KubernetesService = {
  name: string;
  namespace: string;
  type: string;
  ports: Array<{ name?: string; port: number; app_protocol?: string }>;
  endpoint_health_known: boolean;
  ready_endpoints: number;
};

const store = usePlatformStore();
const router = useRouter();
const ingresses = ref<KubernetesIngress[]>([]);
const services = ref<KubernetesService[]>([]);
const loading = ref(false);
const syncingConfig = ref(false);
const servicesLoading = ref(false);
const observedAt = ref('');
const editorVisible = ref(false);
const editorMode = ref<'create' | 'edit'>('create');
const editorLoading = ref(false);
const validating = ref(false);
const saving = ref(false);
const validationWarnings = ref<string[]>([]);
const filter = reactive({ namespace: '', className: '', keyword: '' });
const editor = reactive({ yaml: '', resourceVersion: '', originalNamespace: '', originalName: '' });
const draft = reactive({
  namespace: '', name: '', className: 'higress', host: '', path: '/', service: '',
  servicePort: '' as string | number, backendProtocol: 'http', tlsEnabled: true, tlsSecret: '',
});

const scopePath = computed(() => {
  if (!store.currentProjectKey || !store.currentEnvironmentKey) return '';
  return `/api/projects/${encodeURIComponent(store.currentProjectKey)}/environments/${encodeURIComponent(store.currentEnvironmentKey)}/kubernetes`;
});
const namespaces = computed(() => Object.keys(store.config?.namespaces || {}).sort());
const canMutate = computed(() => store.canConfigure && store.canDeploy);
const classNames = computed(() => [...new Set(ingresses.value.map((item) => item.class_name).filter(Boolean))].sort());
const syncedCount = computed(() => ingresses.value.filter((item) => item.sync_status === 'synced').length);
const attentionCount = computed(() => ingresses.value.filter((item) => ['pending', 'drifted', 'conflict'].includes(item.sync_status)).length);
const clusterOnlyCount = computed(() => ingresses.value.filter((item) => item.sync_status === 'cluster-only').length);
const namespaceServices = computed(() => services.value.filter((item) => item.namespace === draft.namespace));
const selectedServicePorts = computed(() => namespaceServices.value.find((item) => item.name === draft.service)?.ports || []);
const availableTLSSecrets = computed(() => (store.config?.tls?.certificates || [])
  .filter((item: Record<string, any>) => !item.namespace || item.namespace === draft.namespace)
  .map((item: Record<string, any>) => String(item.tls_secret_name || '').trim())
  .filter(Boolean));
const filteredIngresses = computed(() => {
  const keyword = filter.keyword.trim().toLowerCase();
  return ingresses.value.filter((item) => {
    if (filter.namespace && item.namespace !== filter.namespace) return false;
    if (filter.className && item.class_name !== filter.className) return false;
    if (!keyword) return true;
    return [
      item.namespace, item.name, item.class_name, ...item.hosts,
      ...item.paths.flatMap((path) => [path.path, path.service_name, path.service_port]),
    ].some((value) => String(value).toLowerCase().includes(keyword));
  }).map((item) => ({ ...item, row_key: `${item.namespace}/${item.name}` }));
});
const hasActiveFilters = computed(() => Boolean(filter.namespace || filter.className || filter.keyword.trim()));
const yamlLineCount = computed(() => editor.yaml ? editor.yaml.split(/\r?\n/).length : 0);
const yamlSizeText = computed(() => {
  const bytes = new TextEncoder().encode(editor.yaml).length;
  return bytes < 1024 ? `${bytes} B` : `${(bytes / 1024).toFixed(1)} KB`;
});

const formatTime = (value: string) => new Date(value).toLocaleString('zh-CN', { hour12: false });
const copyText = async (value: string) => {
  try { await copyToClipboard(value); Message.success('已复制'); }
  catch { Message.error('复制失败，请手动选择内容'); }
};
const resetFilters = () => {
  filter.namespace = '';
  filter.className = '';
  filter.keyword = '';
};

const normalizeIngress = (value: unknown): KubernetesIngress | null => {
  if (!value || typeof value !== 'object') return null;
  const item = value as Record<string, unknown>;
  const name = String(item.name || '').trim();
  const namespace = String(item.namespace || '').trim();
  if (!name || !namespace) return null;
  const paths = Array.isArray(item.paths)
    ? item.paths
      .filter((path): path is Record<string, unknown> => Boolean(path && typeof path === 'object'))
      .map((path) => ({
        host: String(path.host || ''),
        path: String(path.path || '/'),
        path_type: String(path.path_type || ''),
        service_name: String(path.service_name || ''),
        service_namespace: String(path.service_namespace || ''),
        service_port: String(path.service_port || ''),
      }))
    : [];
  const stringList = (input: unknown) => Array.isArray(input)
    ? input.map((entry) => String(entry || '').trim()).filter(Boolean)
    : [];
  return {
    name,
    namespace,
    class_name: String(item.class_name || ''),
    resource_version: String(item.resource_version || ''),
    hosts: stringList(item.hosts),
    paths,
    tls_secrets: stringList(item.tls_secrets),
    addresses: stringList(item.addresses),
    creation_timestamp: item.creation_timestamp ? String(item.creation_timestamp) : undefined,
    managed_by: item.managed_by ? String(item.managed_by) : undefined,
    backend_protocol: item.backend_protocol ? String(item.backend_protocol) : undefined,
    sync_status: ['synced', 'pending', 'drifted', 'conflict', 'cluster-only'].includes(String(item.sync_status))
      ? item.sync_status as KubernetesIngress['sync_status']
      : 'cluster-only',
    desired: Boolean(item.desired),
    config_index: Number.isInteger(item.config_index) ? Number(item.config_index) : undefined,
  };
};

const syncStatusMeta = (record: KubernetesIngress) => {
  switch (record.sync_status) {
    case 'synced':
      return { color: 'green', label: '已同步', hint: '配置与集群一致', dotClass: '' };
    case 'pending':
      return { color: 'arcoblue', label: '待部署', hint: '执行阶段二后创建', dotClass: 'status-dot--pending' };
    case 'drifted':
      return { color: 'orange', label: '配置漂移', hint: '阶段二将恢复期望配置', dotClass: 'status-dot--warning' };
    case 'conflict':
      return { color: 'red', label: '配置冲突', hint: '同一 Ingress 被重复配置', dotClass: 'status-dot--danger' };
    default:
      return { color: 'gray', label: '仅集群存在', hint: '未纳入部署配置', dotClass: 'status-dot--muted' };
  }
};

const openConfiguredDomain = async (record?: KubernetesIngress) => {
  const query: Record<string, string> = { tab: 'domains' };
  if (record?.config_index !== undefined) query.domain_index = String(record.config_index);
  await router.push({ name: 'environment', query });
};

const loadIngresses = async () => {
  if (!scopePath.value) {
    ingresses.value = [];
    return;
  }
  loading.value = true;
  try {
    const result = await api<{ observed_at: string; ingresses: KubernetesIngress[] }>(`${scopePath.value}/ingresses`, { timeoutMs: 90_000 });
    ingresses.value = (Array.isArray(result.ingresses) ? result.ingresses : [])
      .map(normalizeIngress)
      .filter((item): item is KubernetesIngress => item !== null);
    observedAt.value = result.observed_at || '';
  } catch (error: any) {
    ingresses.value = [];
    Message.error(error.message);
  } finally {
    loading.value = false;
  }
};

type IngressConfigSyncResponse = {
  config: Dict;
  report: {
    updated_domains: number;
    imported_domains: number;
    imported_routes: number;
    consolidated_domains: number;
    preserved_domains: number;
    skipped: string[];
  };
};

const syncConfig = async () => {
  if (!scopePath.value || syncingConfig.value) return false;
  const revision = store.scopeRevision;
  syncingConfig.value = true;
  try {
    const result = await api<IngressConfigSyncResponse>(`${scopePath.value}/ingresses/sync-config`, {
      method: 'POST',
      timeoutMs: 120_000,
    });
    if (revision !== store.scopeRevision) return true;
    store.config = result.config;
    await loadIngresses();
    const report = result.report;
    if (report.updated_domains > 0) {
      Message.success(`已从 EKS 新增 ${report.imported_domains || 0} 个域名、回填 ${report.imported_routes} 条路由，更新 ${report.updated_domains} 个域名配置`);
      if (report.skipped.length) Message.warning(`${report.skipped.length} 项存在冲突，已安全跳过`);
      await router.push({ name: 'environment', query: { tab: 'domains' } });
    } else {
      Message.info(report.skipped.length
        ? `没有可自动回填的规则，${report.skipped.length} 项冲突已安全跳过`
        : '平台域名转发配置已经与当前 EKS Ingress 一致');
    }
    return true;
  } catch (error: any) {
    Message.error(error.message || '同步域名路由失败');
    return false;
  } finally {
    syncingConfig.value = false;
  }
};

const confirmSyncConfig = () => {
  Modal.confirm({
    title: '从 EKS 同步域名与转发规则？',
    content: '平台按域名聚合当前项目 Namespace 内的路径，并以 EKS 中已存在的路由为准回填；新域名会自动登记。平台中尚未部署到集群的规则不会被删除。此操作只更新项目配置，不修改 EKS。',
    okText: '开始同步',
    cancelText: '取消',
    onOk: syncConfig,
  });
};

const loadServices = async () => {
  if (!scopePath.value || servicesLoading.value) return;
  servicesLoading.value = true;
  try {
    const result = await api<{ services: KubernetesService[] }>(`${scopePath.value}/services`, { timeoutMs: 75_000 });
    services.value = result.services || [];
  } catch (error: any) {
    Message.error(error.message);
  } finally {
    servicesLoading.value = false;
  }
};
const ensureServices = (visible: boolean) => { if (visible && !services.value.length) void loadServices(); };

const resetDraft = () => {
  const defaultClass = store.config?.components?.catalog?.higress?.enabled ? 'higress' : 'nginx';
  Object.assign(draft, {
    namespace: namespaces.value[0] || '', name: '', className: defaultClass, host: '', path: '/',
    service: '', servicePort: '', backendProtocol: 'http', tlsEnabled: true, tlsSecret: '',
  });
};

const generateYAML = () => {
  const name = draft.name.trim();
  const namespace = draft.namespace.trim();
  const service = draft.service.trim();
  const servicePort = Number(draft.servicePort);
  if (!namespace || !name || !service || !servicePort) {
    Message.warning('请先填写 Namespace、Ingress 名称、后端 Service 和端口');
    return false;
  }
  const selectedService = namespaceServices.value.find((item) => item.name === service);
  if (selectedService?.endpoint_health_known && selectedService.ready_endpoints === 0) {
    Message.warning(`Service ${namespace}/${service} 当前没有 Ready Endpoint，请先恢复对应 Pod`);
    return false;
  }
  const host = draft.host.trim();
  const annotations: Record<string, string> = {};
  if (draft.backendProtocol !== 'http') {
    const key = draft.className === 'higress' ? 'higress.io/backend-protocol' : 'nginx.ingress.kubernetes.io/backend-protocol';
    annotations[key] = draft.backendProtocol.toUpperCase();
  }
  const metadata: Record<string, any> = { name, namespace };
  if (Object.keys(annotations).length) metadata.annotations = annotations;
  const spec: Record<string, any> = {
    ingressClassName: draft.className,
    rules: [{
      ...(host ? { host } : {}),
      http: {
        paths: [{
          path: draft.path.trim() || '/',
          pathType: 'Prefix',
          backend: { service: { name: service, port: { number: servicePort } } },
        }],
      },
    }],
  };
  if (draft.tlsEnabled) {
    if (!host || !draft.tlsSecret.trim()) {
      Message.warning('启用 TLS 时必须填写域名和 TLS Secret');
      return false;
    }
    spec.tls = [{ hosts: [host], secretName: draft.tlsSecret.trim() }];
  }
  editor.yaml = stringify({ apiVersion: 'networking.k8s.io/v1', kind: 'Ingress', metadata, spec }, { lineWidth: 0 });
  validationWarnings.value = [];
  return true;
};

const openCreate = async () => {
  if (!canMutate.value) {
    Message.warning('创建 Ingress 需要当前项目的配置修改和部署权限');
    return;
  }
  editorMode.value = 'create';
  Object.assign(editor, { yaml: '', resourceVersion: '', originalNamespace: '', originalName: '' });
  validationWarnings.value = [];
  resetDraft();
  editorVisible.value = true;
  if (!services.value.length) await loadServices();
};

const openEdit = async (record: KubernetesIngress) => {
  if (!scopePath.value) return;
  editorMode.value = 'edit';
  editorVisible.value = true;
  editorLoading.value = true;
  validationWarnings.value = [];
  try {
    const namespace = encodeURIComponent(record.namespace);
    const name = encodeURIComponent(record.name);
    const result = await api<KubernetesIngressDocument>(`${scopePath.value}/ingresses/${namespace}/${name}`, { timeoutMs: 90_000 });
    Object.assign(editor, {
      yaml: result.yaml,
      resourceVersion: result.ingress.resource_version,
      originalNamespace: result.ingress.namespace,
      originalName: result.ingress.name,
    });
  } catch (error: any) {
    Message.error(error.message);
    editorVisible.value = false;
  } finally {
    editorLoading.value = false;
  }
};

const validateYAML = async () => {
  if (!scopePath.value || !editor.yaml.trim()) {
    Message.warning('Ingress YAML 不能为空');
    return false;
  }
  validating.value = true;
  try {
    const result = await api<KubernetesIngressValidation>(`${scopePath.value}/ingresses/validate`, {
      method: 'POST',
      body: JSON.stringify({ yaml: editor.yaml }),
      timeoutMs: 90_000,
    });
    editor.yaml = result.normalized_yaml;
    validationWarnings.value = result.warnings || [];
    Message.success(`校验通过：${result.ingress.namespace}/${result.ingress.name}`);
    return true;
  } catch (error: any) {
    Message.error(error.message);
    return false;
  } finally {
    validating.value = false;
  }
};

const saveIngress = async () => {
  if (!canMutate.value) {
    Message.warning('保存 Ingress 需要当前项目的配置修改和部署权限');
    return;
  }
  if (editorMode.value === 'create' && !editor.yaml.trim() && !generateYAML()) return;
  if (!editor.yaml.trim()) {
    Message.warning('Ingress YAML 不能为空');
    return;
  }
  saving.value = true;
  try {
    let result: KubernetesIngressDocument;
    if (editorMode.value === 'create') {
      result = await api<KubernetesIngressDocument>(`${scopePath.value}/ingresses`, {
        method: 'POST', body: JSON.stringify({ yaml: editor.yaml }), timeoutMs: 120_000,
      });
    } else {
      const namespace = encodeURIComponent(editor.originalNamespace);
      const name = encodeURIComponent(editor.originalName);
      result = await api<KubernetesIngressDocument>(`${scopePath.value}/ingresses/${namespace}/${name}`, {
        method: 'PUT',
        body: JSON.stringify({ yaml: editor.yaml, resource_version: editor.resourceVersion }),
        timeoutMs: 120_000,
      });
    }
    editor.yaml = result.yaml;
    editor.resourceVersion = result.ingress.resource_version;
    editor.originalNamespace = result.ingress.namespace;
    editor.originalName = result.ingress.name;
    Message.success(`Ingress ${result.ingress.namespace}/${result.ingress.name} 已应用`);
    editorVisible.value = false;
    await loadIngresses();
  } catch (error: any) {
    Message.error(error.message);
  } finally {
    saving.value = false;
  }
};

const removeIngress = async (record: KubernetesIngress) => {
  if (!canMutate.value || !scopePath.value) {
    Message.warning('删除 Ingress 需要当前项目的配置修改和部署权限');
    return;
  }
  try {
    const namespace = encodeURIComponent(record.namespace);
    const name = encodeURIComponent(record.name);
    const version = encodeURIComponent(record.resource_version || '');
    await api<void>(`${scopePath.value}/ingresses/${namespace}/${name}?resource_version=${version}`, {
      method: 'DELETE', timeoutMs: 90_000,
    });
    Message.success(`Ingress ${record.namespace}/${record.name} 已删除`);
    await loadIngresses();
  } catch (error: any) {
    Message.error(error.message);
  }
};

watch(() => store.scopeKey, () => {
  services.value = [];
  filter.namespace = '';
  filter.className = '';
  filter.keyword = '';
  void loadIngresses();
});
onMounted(loadIngresses);
</script>

<style scoped>
.ingress-page { display: flex; flex-direction: column; gap: 16px; }
.page-head {
  position: relative;
  display: flex;
  align-items: center;
  justify-content: space-between;
  min-height: 132px;
  padding: 24px 28px;
  overflow: hidden;
  border: 1px solid rgba(var(--primary-6), .16);
  border-radius: 14px;
  background:
    radial-gradient(circle at 87% 15%, rgba(var(--primary-6), .15), transparent 28%),
    linear-gradient(135deg, var(--color-bg-2), rgba(var(--primary-1), .72));
  box-shadow: 0 12px 34px rgba(15, 23, 42, .07);
}
.page-head::after {
  position: absolute;
  right: -36px;
  bottom: -82px;
  width: 210px;
  height: 210px;
  border: 28px solid rgba(var(--primary-6), .07);
  border-radius: 50%;
  content: "";
}
.page-head__content, .page-actions { position: relative; z-index: 1; }
.page-kicker {
  display: flex;
  align-items: center;
  gap: 7px;
  margin-bottom: 7px;
  color: rgb(var(--primary-6));
  font-size: 11px;
  font-weight: 700;
  letter-spacing: .08em;
  text-transform: uppercase;
}
.live-dot, .status-dot {
  display: inline-block;
  width: 7px;
  height: 7px;
  margin-right: 5px;
  border-radius: 50%;
  background: rgb(var(--success-6));
  box-shadow: 0 0 0 3px rgba(var(--success-6), .13);
}
.page-head h2 { margin: 0 0 7px; font-size: 26px; line-height: 1.25; color: var(--color-text-1); }
.page-head p { max-width: 760px; margin: 0; color: var(--color-text-2); line-height: 1.6; }
.scope-tags { display: flex; flex-wrap: wrap; gap: 8px; margin-top: 14px; }
.scope-tags :deep(.arco-tag) { gap: 5px; padding: 0 10px; border-radius: 999px; }
.scope-alert { border-radius: 10px; }
.summary-row { margin-top: 0; row-gap: 14px; }
.summary-card {
  position: relative;
  height: 112px;
  overflow: hidden;
  border: 1px solid var(--color-border-2);
  border-radius: 12px;
  box-shadow: 0 6px 22px rgba(15, 23, 42, .055);
  transition: transform .2s ease, box-shadow .2s ease;
}
.summary-card:hover { transform: translateY(-2px); box-shadow: 0 10px 28px rgba(15, 23, 42, .09); }
.summary-card::before { position: absolute; inset: 0 auto 0 0; width: 4px; background: var(--summary-accent); content: ""; }
.summary-card :deep(.arco-card-body) { display: flex; align-items: center; gap: 14px; height: 100%; padding: 19px 20px; }
.summary-card__icon {
  display: grid;
  flex: 0 0 42px;
  width: 42px;
  height: 42px;
  place-items: center;
  border-radius: 11px;
  background: var(--summary-bg);
  color: var(--summary-accent);
  font-size: 21px;
}
.summary-card :deep(.arco-card-body) > div:last-child { display: grid; grid-template: auto 1fr / 1fr auto; flex: 1; align-items: center; column-gap: 12px; }
.summary-card span { color: var(--color-text-2); font-size: 13px; font-weight: 600; }
.summary-card strong { grid-area: 1 / 2 / 3; color: var(--color-text-1); font-size: 30px; line-height: 1; }
.summary-card small { margin-top: 5px; color: var(--color-text-3); font-size: 12px; }
.summary-card--blue { --summary-accent: rgb(var(--primary-6)); --summary-bg: rgba(var(--primary-6), .1); }
.summary-card--green { --summary-accent: rgb(var(--success-6)); --summary-bg: rgba(var(--success-6), .1); }
.summary-card--purple { --summary-accent: #7c3aed; --summary-bg: rgba(124, 58, 237, .1); }
.summary-card--orange { --summary-accent: rgb(var(--orange-6)); --summary-bg: rgba(var(--orange-6), .11); }
.ingress-list-card { border: 1px solid var(--color-border-2); border-radius: 12px; box-shadow: 0 8px 28px rgba(15, 23, 42, .06); }
.list-heading { display: flex; align-items: flex-start; justify-content: space-between; gap: 18px; margin-bottom: 18px; }
.list-heading h3 { margin: 0 0 5px; color: var(--color-text-1); font-size: 17px; }
.list-heading p { margin: 0; color: var(--color-text-3); font-size: 13px; }
.observed-time { display: flex; align-items: center; gap: 12px; color: var(--color-text-3); font-size: 12px; }
.observed-time strong { padding: 5px 10px; border-radius: 8px; background: var(--color-fill-2); color: var(--color-text-2); font-size: 13px; }
.filter-row {
  display: flex;
  justify-content: space-between;
  align-items: center;
  gap: 16px;
  margin: 0 -20px 16px;
  padding: 13px 20px;
  border-block: 1px solid var(--color-border-2);
  background: var(--color-fill-1);
}
.filter-controls { display: flex; flex-wrap: wrap; align-items: center; gap: 10px; }
.search-control { width: 320px; }
.status-dot--loading, .status-dot--pending {
  background: rgb(var(--orange-6));
  box-shadow: 0 0 0 3px rgba(var(--orange-6), .13);
}
.status-dot--warning {
  background: rgb(var(--orange-6));
  box-shadow: 0 0 0 3px rgba(var(--orange-6), .13);
}
.status-dot--muted {
  background: var(--color-text-4);
  box-shadow: 0 0 0 3px var(--color-fill-3);
}
.status-dot--danger {
  background: rgb(var(--danger-6));
  box-shadow: 0 0 0 3px rgba(var(--danger-6), .13);
}
.sync-status-cell { display: flex; flex-direction: column; align-items: flex-start; gap: 5px; }
.sync-status-cell small { color: var(--color-text-3); font-size: 10px; white-space: nowrap; }
.muted-text { color: var(--color-text-3); font-size: 12px; }
.ingress-table { margin-bottom: -8px; }
.ingress-table :deep(.arco-table-th) { height: 44px; background: var(--color-fill-1); color: var(--color-text-2); font-size: 12px; font-weight: 700; }
.ingress-table :deep(.arco-table-td) { padding-block: 13px; }
.ingress-table :deep(.arco-table-tr:hover .arco-table-td) { background: rgba(var(--primary-1), .55); }
.ingress-table :deep(.arco-table-col-fixed-right) { box-shadow: -8px 0 16px rgba(15, 23, 42, .045); }
.ingress-actions { display: flex; width: 100%; justify-content: flex-end; }
.ingress-actions :deep(.arco-btn) { flex: 0 0 auto; }
.identity-cell, .stacked-values, .backend-list, .address-cell { display: flex; flex-direction: column; gap: 5px; min-width: 0; }
.identity-cell strong { color: var(--color-text-1); }
.identity-cell span { display: flex; align-items: center; gap: 4px; color: var(--color-text-3); font-size: 12px; }
.domain-link { max-width: 250px; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.backend-list div { display: flex; gap: 8px; align-items: center; min-width: 0; }
.backend-list code { flex: 0 0 auto; min-width: 34px; padding: 2px 7px; border: 1px solid var(--color-border-2); border-radius: 5px; background: var(--color-bg-2); color: rgb(var(--primary-6)); text-align: center; }
.route-arrow { flex: 0 0 auto; color: var(--color-text-4); }
.backend-list span { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.backend-list small { color: rgb(var(--primary-6)); }
.address-cell :deep(.arco-link) { max-width: 220px; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.empty-state { display: flex; flex-direction: column; align-items: center; padding: 38px 0 46px; }
.create-builder { margin-bottom: 10px; padding: 18px 20px 0; border: 1px solid var(--color-border-2); border-radius: 10px; background: var(--color-fill-1); }
.builder-heading { display: flex; align-items: flex-start; justify-content: space-between; gap: 16px; margin-bottom: 14px; }
.builder-heading > div { display: flex; flex-direction: column; gap: 4px; }
.builder-heading strong { color: var(--color-text-1); font-size: 15px; }
.builder-heading span { color: var(--color-text-3); font-size: 12px; }
.editor-alert { margin-bottom: 14px; }
.yaml-toolbar { display: flex; align-items: flex-end; justify-content: space-between; gap: 18px; margin: 18px 0 11px; }
.yaml-toolbar > div { display: flex; flex-direction: column; gap: 3px; }
.yaml-toolbar span { color: var(--color-text-3); font-size: 12px; }
.yaml-meta { display: flex; flex-flow: row wrap; gap: 6px; margin-top: 7px; }
.yaml-editor { width: 100%; }
.yaml-editor :deep(.arco-textarea-wrapper) { border: 1px solid #334155; border-radius: 10px; background: #0b1220; box-shadow: inset 0 1px 0 rgba(255, 255, 255, .03), 0 8px 24px rgba(15, 23, 42, .12); }
.yaml-editor :deep(.arco-textarea-wrapper:hover), .yaml-editor :deep(.arco-textarea-wrapper:focus-within) { border-color: #60a5fa; box-shadow: 0 0 0 3px rgba(96, 165, 250, .14), 0 10px 28px rgba(15, 23, 42, .16); }
.yaml-editor :deep(textarea) {
  min-height: 540px;
  padding: 18px 20px;
  font-family: "SFMono-Regular", Consolas, "Liberation Mono", monospace;
  font-size: 13px;
  line-height: 1.62;
  tab-size: 2;
  background: #0b1220;
  color: #dbeafe;
  caret-color: #93c5fd;
  border-radius: 10px;
  resize: vertical;
}
.validation-warnings { display: flex; flex-direction: column; gap: 8px; margin-top: 10px; }
@media (max-width: 1180px) {
  .page-head, .list-heading, .filter-row, .yaml-toolbar { align-items: stretch; flex-direction: column; }
  .page-actions { align-self: flex-start; }
  .filter-row { margin-inline: -16px; }
  .filter-row > :deep(.arco-tag) { align-self: flex-start; }
}
@media (max-width: 720px) {
  .page-head { padding: 20px; }
  .page-head h2 { font-size: 23px; }
  .page-actions { width: 100%; }
  .page-actions :deep(.arco-space-item:last-child), .page-actions :deep(.arco-space-item:last-child .arco-btn) { flex: 1; }
  .filter-controls, .search-control, .filter-controls :deep(.arco-select-view) { width: 100% !important; }
  .observed-time { align-items: flex-start; flex-direction: column; gap: 7px; }
  .yaml-toolbar :deep(.arco-space) { width: 100%; }
  .yaml-toolbar :deep(.arco-btn) { flex: 1; }
  .yaml-editor :deep(textarea) { min-height: 420px; padding: 14px; font-size: 12px; }
}
</style>

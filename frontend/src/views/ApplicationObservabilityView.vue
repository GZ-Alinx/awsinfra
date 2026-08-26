<template>
  <div class="observability-page">
    <section class="observability-hero">
      <div>
        <span class="hero-kicker">APPLICATION OBSERVABILITY</span>
        <h2>应用全景观测</h2>
        <p>从访问入口、域名、Service、Deployment 一直追踪到数据库与中间件，真实业务链路和异常影响范围一眼可见。</p>
      </div>
      <div class="hero-actions">
        <div class="source-state" :class="{ connected: topology?.source.prometheus }">
          <span />
          {{ topology?.source.runtime_graph ? '实际调用链已接入' : topology?.source.prometheus ? 'Prometheus 已接入' : 'Kubernetes 实时状态' }}
        </div>
        <label class="auto-refresh-control">
          <span>自动刷新</span>
          <a-switch v-model="autoRefresh" type="round" size="small" />
        </label>
        <a-button type="primary" :loading="loading" :disabled="!store.scopeKey" @click="loadTopology(true)">
          <template #icon><icon-refresh /></template>
          重新扫描
        </a-button>
      </div>
    </section>

    <a-alert
      v-for="warning in displayWarnings"
      :key="warning"
      type="warning"
      class="topology-warning"
      closable
    >
      {{ warning }}
    </a-alert>

    <section class="health-summary">
      <article class="health-total">
        <span>链路节点</span>
        <strong>{{ networkSummary.total }}</strong>
        <small>{{ networkSummary.connections }} 条真实关系 · {{ store.currentProject?.display_name || '未选择项目' }} · {{ store.currentEnvironment?.display_name || '未选择环境' }}</small>
      </article>
      <article class="health-state normal">
        <i />
        <div><span>正常</span><strong>{{ networkSummary.normal }}</strong></div>
        <small>入口、应用与依赖均正常</small>
      </article>
      <article class="health-state warning">
        <i />
        <div><span>告警</span><strong>{{ networkSummary.warning }}</strong></div>
        <small>需要关注，服务仍可用</small>
      </article>
      <article class="health-state abnormal">
        <i />
        <div><span>异常</span><strong>{{ networkSummary.abnormal }}</strong></div>
        <small>服务不可用或严重故障</small>
      </article>
    </section>

    <section class="observability-toolbar">
      <a-input v-model="keyword" allow-clear placeholder="搜索域名、Service、Deployment、数据库或 Namespace" class="topology-search">
        <template #prefix><icon-search /></template>
      </a-input>
      <a-radio-group v-model="activeLayer" type="button" size="small">
        <a-radio value="all">全部链路</a-radio>
        <a-radio value="entry">访问入口</a-radio>
        <a-radio value="application">应用服务</a-radio>
        <a-radio value="data">数据依赖</a-radio>
      </a-radio-group>
      <a-select v-model="activeState" class="state-filter">
        <a-option value="all">全部状态</a-option>
        <a-option value="normal">正常</a-option>
        <a-option value="warning">告警</a-option>
        <a-option value="abnormal">异常</a-option>
      </a-select>
    </section>

    <section v-if="topology" class="connection-trust">
      <strong>连接证据</strong>
      <span class="endpoint"><i />Kubernetes 已验证 {{ networkSummary.verified }}</span>
      <span class="declared"><i />工作负载配置依赖 {{ networkSummary.declared }}</span>
      <small>不读取 Secret；没有 Ingress、EndpointSlice 或工作负载配置依据的关系不会生成</small>
    </section>

    <section v-if="!store.scopeKey" class="observability-empty">
      <a-empty description="请先在右上角选择项目和环境" />
    </section>
    <section v-else-if="loading && !topology" class="observability-loading">
      <a-spin :size="34" />
      <strong>正在扫描应用拓扑</strong>
      <span>读取 Ingress、Deployment、Service、EndpointSlice、ConfigMap 和就绪状态</span>
    </section>
    <section v-else-if="topology && networkNodes.length === 0" class="observability-empty">
      <a-empty description="当前项目环境尚未发现有证据的业务访问链路" />
    </section>
    <section v-else class="observatory-workspace">
      <div
        ref="topologyFullscreenHost"
        class="topology-scene-wrap"
        :class="{ 'is-fullscreen': isTopologyFullscreen }"
      >
        <div class="scene-heading">
          <div>
            <strong>业务访问关系网</strong>
            <span>公网入口、业务服务、EKS 运行区与运维保障统一呈现；连线均来自当前 EKS 实际配置</span>
          </div>
          <div class="scene-heading-actions">
            <a-radio-group v-model="topologyViewMode" type="button" size="mini" class="topology-view-switch" aria-label="拓扑视图模式">
              <a-radio value="2d">网络拓扑</a-radio>
              <a-radio value="3d">3D 空间</a-radio>
            </a-radio-group>
            <div class="scene-legend" aria-label="健康状态说明">
              <span class="normal"><i />正常</span>
              <span class="warning"><i />告警</span>
              <span class="abnormal"><i />异常</span>
            </div>
            <button
              type="button"
              class="fullscreen-toggle"
              :aria-label="isTopologyFullscreen ? '退出全屏查看' : '全屏查看应用拓扑'"
              :title="isTopologyFullscreen ? '退出全屏（Esc）' : '全屏查看'"
              @click="toggleTopologyFullscreen"
            >
              <IconFullscreenExit v-if="isTopologyFullscreen" />
              <IconFullscreen v-else />
              {{ isTopologyFullscreen ? '退出全屏' : '全屏查看' }}
            </button>
            <button type="button" class="detail-panel-toggle" @click="detailPanelOpen = !detailPanelOpen">
              {{ detailPanelOpen ? '收起详情' : '显示详情' }}
            </button>
          </div>
        </div>
        <ApplicationTopologyScene
          v-if="filteredNodes.length"
          :nodes="filteredNodes"
          :edges="visibleEdges"
          :selected-id="selectedNodeID"
          :detail-open="detailPanelOpen"
          :selection-pinned="selectionPinned"
          :view-mode="topologyViewMode"
          :project-name="store.currentProject?.display_name || store.currentProjectKey"
          :environment-name="store.currentEnvironment?.display_name || store.currentEnvironmentKey"
          @select="selectNode"
          @clear="clearNodeSelection"
        />
        <div v-if="!filteredNodes.length" class="scene-filter-empty">
          <a-empty description="没有符合当前筛选条件的对象" />
        </div>

        <aside v-show="detailPanelOpen" class="topology-detail">
        <template v-if="selectedNode">
          <div class="detail-heading">
            <div :class="['detail-state', selectedNode.state]"><i />{{ stateLabel(selectedNode.state) }}</div>
            <span>{{ kindLabel(selectedNode.kind) }}</span>
            <button type="button" class="detail-clear-selection" @click="clearNodeSelection">取消选择</button>
          </div>
          <h3>{{ selectedNode.name }}</h3>
          <p>{{ selectedNode.state_reason }}</p>
          <div class="detail-scope"><span>Namespace</span><strong>{{ selectedNode.namespace }}</strong></div>

          <div class="detail-metrics">
            <article><span>就绪副本</span><strong>{{ selectedNode.ready_replicas ?? selectedNode.ready_pods ?? 0 }} / {{ selectedNode.desired_replicas ?? selectedNode.pods ?? 0 }}</strong></article>
            <article v-if="selectedNode.kind === 'Service'"><span>实际端点</span><strong>{{ selectedNode.ready_endpoints || 0 }} / {{ selectedNode.total_endpoints || 0 }}</strong></article>
            <article><span>容器重启</span><strong>{{ selectedNode.restarts || 0 }}</strong></article>
            <article><span>CPU</span><strong>{{ formatCPU(selectedNode.cpu_cores) }}</strong></article>
            <article><span>内存</span><strong>{{ formatMemory(selectedNode.memory_bytes) }}</strong></article>
          </div>

          <div class="detail-section">
            <strong>上下游业务链路</strong>
            <div v-if="selectedConnections.length" class="connection-list">
              <article v-for="connection in selectedConnections" :key="connection.edge.id" :class="connection.edge.state">
                <header>
                  <span>{{ connection.direction === 'out' ? '出站' : '入站' }}</span>
                  <b>{{ relationLabel(connection.edge.relation) }}</b>
                  <em v-if="connection.edge.verified">已验证</em>
                  <em v-else class="declared">配置关系</em>
                </header>
                <strong>{{ connection.peer?.namespace }}/{{ connection.peer?.name }}</strong>
                <small>{{ connection.edge.label || connection.edge.protocol || 'Kubernetes 连接' }}</small>
                <p>{{ connection.edge.evidence }}</p>
              </article>
            </div>
            <small v-else>当前对象没有符合主视图规则的上下游链路。</small>
          </div>

          <div class="detail-section">
            <strong>Service 与端口</strong>
            <div v-if="selectedNode.services.length" class="detail-list">
              <span v-for="service in selectedNode.services" :key="service">{{ service }}</span>
            </div>
            <small v-else>当前对象没有关联 Service</small>
            <div v-if="selectedNode.ports.length" class="port-list">
              <code v-for="port in selectedNode.ports" :key="`${port.name}-${port.port}`">{{ port.name || 'TCP' }} : {{ port.port }}</code>
            </div>
          </div>

          <div class="detail-section">
            <strong>访问入口</strong>
            <div v-if="selectedNode.hosts.length" class="host-list">
              <span v-for="host in selectedNode.hosts" :key="host" :title="host">{{ host }}</span>
            </div>
            <small v-else>当前对象没有域名入口</small>
          </div>

          <div v-if="nodeAlerts.length" class="detail-section node-alerts">
            <strong>活动告警</strong>
            <article v-for="alert in nodeAlerts" :key="`${alert.name}-${alert.pod || alert.service || ''}`" :class="alert.state">
              <span>{{ alert.name }}</span>
              <small>{{ alert.summary || alert.pod || alert.service || 'Prometheus 检测到异常' }}</small>
            </article>
          </div>
        </template>
        <div v-else class="detail-empty">
          <icon-dashboard />
          <strong>选择一个应用对象</strong>
          <span>查看实时状态、资源指标、Service、域名与活动告警。</span>
        </div>
      </aside>
      </div>
    </section>

    <footer v-if="topology" class="observability-footer">
      <span>最后观测：{{ observedAt }}</span>
      <span>数据范围：当前项目与环境登记的 Namespace</span>
      <span>虚线表示从非敏感运行配置识别出的数据依赖；实线表示 Kubernetes 已验证关系</span>
    </footer>
  </div>
</template>

<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref, watch } from 'vue';
import { Message } from '@arco-design/web-vue';
import {
  IconDashboard,
  IconFullscreen,
  IconFullscreenExit,
  IconRefresh,
  IconSearch,
} from '@arco-design/web-vue/es/icon';
import ApplicationTopologyScene from '@/components/ApplicationTopologyScene.vue';
import { api } from '@/services/api';
import { usePlatformStore } from '@/stores/platform';
import type {
  ApplicationHealthState,
  ApplicationTopology,
  ApplicationTopologyAlert,
  ApplicationTopologyEdge,
  ApplicationTopologyNode,
} from '@/types';

type SelectedConnection = {
  edge: ApplicationTopologyEdge;
  direction: 'in' | 'out';
  peer?: ApplicationTopologyNode;
};

const store = usePlatformStore();
const topology = ref<ApplicationTopology | null>(null);
const loading = ref(false);
const autoRefresh = ref(true);
const keyword = ref('');
const activeLayer = ref('all');
const activeState = ref('all');
const selectedNodeID = ref('');
const detailPanelOpen = ref(false);
const selectionPinned = ref(false);
const topologyViewMode = ref<'2d' | '3d'>('2d');
const topologyFullscreenHost = ref<HTMLElement | null>(null);
const isTopologyFullscreen = ref(false);
let refreshTimer = 0;
let requestRevision = 0;

const networkEdges = computed<ApplicationTopologyEdge[]>(() => {
  if (!topology.value) return [];
  const nodes = new Map(topology.value.nodes.map((node) => [node.id, node]));
  const supportedRelations = new Set([
    'exposes_domain', 'ingress_route', 'endpoint', 'service_selector',
    'runtime_request', 'declared_dependency',
  ]);
  return topology.value.edges.filter((edge) => {
    const source = nodes.get(edge.source);
    const target = nodes.get(edge.target);
    if (!source || !target) return false;
    if (!supportedRelations.has(edge.relation)) return false;
    // Data Services are the terminal dependency objects. Their backing
    // StatefulSet/Deployment would duplicate the database in the main graph.
    if ((edge.relation === 'endpoint' || edge.relation === 'service_selector')
      && source.kind === 'Service' && source.layer === 'data') return false;
    return true;
  });
});

const networkNodes = computed<ApplicationTopologyNode[]>(() => {
  const nodeIDs = new Set<string>();
  networkEdges.value.forEach((edge) => {
    nodeIDs.add(edge.source);
    nodeIDs.add(edge.target);
  });
  return (topology.value?.nodes || []).filter((node) => nodeIDs.has(node.id));
});

const networkSummary = computed(() => {
  const nodes = networkNodes.value;
  return {
    total: nodes.length,
    normal: nodes.filter((node) => node.state === 'normal').length,
    warning: nodes.filter((node) => node.state === 'warning').length,
    abnormal: nodes.filter((node) => node.state === 'abnormal').length,
    connections: networkEdges.value.length,
    verified: networkEdges.value.filter((edge) => edge.verified).length,
    declared: networkEdges.value.filter((edge) => !edge.verified).length,
  };
});

const displayWarnings = computed(() => (topology.value?.warnings || []).map((warning) => (
  warning.includes('Prometheus Service')
    ? 'Prometheus 指标暂未接入：CPU、内存与活动告警暂不可用；Kubernetes 业务关系链不受影响'
    : warning
)));

const nodeFilterLayer = (node: ApplicationTopologyNode) => {
  if (node.layer === 'data') return 'data';
  if (node.kind === 'Gateway' || node.kind === 'Domain' || node.kind === 'Ingress') return 'entry';
  return 'application';
};

const filteredNodes = computed(() => {
  const search = keyword.value.trim().toLowerCase();
  return networkNodes.value.filter((node) => {
    if (activeLayer.value !== 'all' && nodeFilterLayer(node) !== activeLayer.value) return false;
    if (activeState.value !== 'all' && node.state !== activeState.value) return false;
    if (!search) return true;
    return [
      node.name, node.namespace, node.kind, ...node.services, ...node.hosts,
    ].some((value) => String(value).toLowerCase().includes(search));
  });
});

const selectedNode = computed(() => {
  const nodes = networkNodes.value;
  return nodes.find((node) => node.id === selectedNodeID.value) || null;
});
const visibleEdges = computed<ApplicationTopologyEdge[]>(() => {
  const visibleIDs = new Set(filteredNodes.value.map((node) => node.id));
  return networkEdges.value.filter((edge) => visibleIDs.has(edge.source) && visibleIDs.has(edge.target));
});
const selectedConnections = computed<SelectedConnection[]>(() => {
  if (!selectedNode.value) return [];
  const nodes = new Map(networkNodes.value.map((node) => [node.id, node]));
  const result: SelectedConnection[] = [];
  for (const edge of networkEdges.value) {
    if (edge.source === selectedNode.value?.id) {
      result.push({ edge, direction: 'out', peer: nodes.get(edge.target) });
    } else if (edge.target === selectedNode.value?.id) {
      result.push({ edge, direction: 'in', peer: nodes.get(edge.source) });
    }
  }
  return result.sort((left, right) => Number(right.edge.verified) - Number(left.edge.verified));
});
const nodeAlerts = computed<ApplicationTopologyAlert[]>(() => {
  if (!selectedNode.value) return [];
  const node = selectedNode.value;
  return (topology.value?.alerts || []).filter((alert) => {
    if (alert.namespace && alert.namespace !== node.namespace) return false;
    return (alert.workload && alert.workload === node.name)
      || (alert.service && node.services.includes(alert.service))
      || (alert.pod && alert.pod.startsWith(`${node.name}-`));
  });
});
const observedAt = computed(() => topology.value?.observed_at
  ? new Date(topology.value.observed_at).toLocaleString('zh-CN')
  : '尚未观测');

const loadTopology = async (fresh = false) => {
  if (!store.currentProjectKey || !store.currentEnvironmentKey) {
    topology.value = null;
    return;
  }
  const revision = ++requestRevision;
  loading.value = true;
  try {
    const project = encodeURIComponent(store.currentProjectKey);
    const environment = encodeURIComponent(store.currentEnvironmentKey);
    const response = await api<ApplicationTopology>(
      `/api/projects/${project}/environments/${environment}/observability/topology${fresh ? '?fresh=true' : ''}`,
      { timeoutMs: 65_000, activity: false },
    );
    if (revision !== requestRevision) return;
    topology.value = response;
    const selectedExists = networkNodes.value.some((node) => node.id === selectedNodeID.value);
    if (!selectedExists) {
      selectedNodeID.value = '';
      selectionPinned.value = false;
    }
    if (fresh) Message.success('应用拓扑已重新扫描');
  } catch (error: any) {
    if (revision === requestRevision) {
      topology.value = null;
      Message.error(error?.message || '应用拓扑扫描失败');
    }
  } finally {
    if (revision === requestRevision) loading.value = false;
  }
};

const refreshWhenVisible = () => {
  if (autoRefresh.value && document.visibilityState === 'visible' && !loading.value) void loadTopology(false);
};
const syncTopologyFullscreenState = () => {
  isTopologyFullscreen.value = document.fullscreenElement === topologyFullscreenHost.value;
};
const toggleTopologyFullscreen = async () => {
  try {
    if (document.fullscreenElement === topologyFullscreenHost.value) {
      await document.exitFullscreen();
      return;
    }
    if (!topologyFullscreenHost.value?.requestFullscreen) {
      Message.warning('当前浏览器不支持全屏查看');
      return;
    }
    await topologyFullscreenHost.value.requestFullscreen();
  } catch (error: any) {
    Message.error(error?.message || '无法进入全屏模式');
  }
};
onMounted(() => {
  void loadTopology(false);
  refreshTimer = window.setInterval(refreshWhenVisible, 30_000);
  document.addEventListener('fullscreenchange', syncTopologyFullscreenState);
});
onUnmounted(() => {
  requestRevision += 1;
  window.clearInterval(refreshTimer);
  document.removeEventListener('fullscreenchange', syncTopologyFullscreenState);
});
watch(() => store.scopeKey, () => {
  topology.value = null;
  selectedNodeID.value = '';
  selectionPinned.value = false;
  keyword.value = '';
  activeLayer.value = 'all';
  activeState.value = 'all';
  void loadTopology(false);
});
watch([activeLayer, activeState, keyword], () => {
  if (selectedNode.value && !filteredNodes.value.some((node) => node.id === selectedNode.value?.id)) {
    clearNodeSelection();
  }
});

const stateLabel = (state: ApplicationHealthState) => ({
  normal: '正常', warning: '告警', abnormal: '异常',
}[state] || '未知');
const kindLabel = (kind: string) => ({
  Gateway: '访问入口', Domain: '域名', Ingress: 'Ingress', Service: '服务端点', Deployment: '应用部署', StatefulSet: '有状态服务', DaemonSet: '节点服务',
}[kind] || kind);
const relationLabel = (relation: string) => ({
  exposes_domain: '入口承载域名',
  ingress_route: 'Ingress 路由',
  endpoint: '运行端点',
  service_selector: 'Service 选择器',
  runtime_request: '实际调用流量',
  declared_dependency: '配置依赖',
}[relation] || relation);
const formatCPU = (value?: number) => value && value > 0 ? `${Math.round(value * 1000)} m` : '—';
const formatMemory = (value?: number) => {
  if (!value || value <= 0) return '—';
  const mib = value / 1024 / 1024;
  return mib >= 1024 ? `${(mib / 1024).toFixed(1)} GiB` : `${Math.round(mib)} MiB`;
};
const selectNode = (id: string) => {
  if (selectionPinned.value && selectedNodeID.value === id) {
    clearNodeSelection();
    return;
  }
  selectedNodeID.value = id;
  selectionPinned.value = true;
  detailPanelOpen.value = true;
};
const clearNodeSelection = () => {
  selectedNodeID.value = '';
  selectionPinned.value = false;
  detailPanelOpen.value = false;
};
</script>

<style scoped>
.observability-page {
  --topology-normal: #2dd4a7;
  --topology-warning: #f5b83b;
  --topology-abnormal: #ff5d68;
  --topology-ink: #d9f4f2;
  display: flex;
  flex-direction: column;
  gap: 14px;
}
.observability-hero {
  min-height: 114px;
  padding: 22px 24px;
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 24px;
  overflow: hidden;
  position: relative;
  border: 1px solid #dbe5f1;
  border-radius: 12px;
  background:
    radial-gradient(circle at 87% 12%, rgb(28 161 171 / 12%), transparent 25%),
    linear-gradient(125deg, #fff 0%, #f4f8fc 60%, #edf8f8 100%);
  box-shadow: 0 8px 28px rgb(22 55 83 / 7%);
}
.observability-hero::after {
  content: "";
  width: 210px;
  height: 210px;
  position: absolute;
  right: -70px;
  bottom: -150px;
  border: 1px solid rgb(35 138 151 / 16%);
  border-radius: 50%;
}
.hero-kicker { color: #168a94; font-size: 10px; font-weight: 800; letter-spacing: .16em; }
.observability-hero h2 { margin: 7px 0 5px; color: #152b45; font-size: 25px; letter-spacing: -.025em; }
.observability-hero p { margin: 0; color: #687c91; font-size: 12px; }
.hero-actions { z-index: 1; display: flex; align-items: center; gap: 10px; }
.source-state {
  min-height: 30px;
  padding: 0 10px;
  display: inline-flex;
  align-items: center;
  gap: 7px;
  color: #6f8193;
  border: 1px solid #d7e1ec;
  border-radius: 20px;
  background: rgb(255 255 255 / 78%);
  font-size: 10px;
}
.source-state span { width: 7px; height: 7px; border-radius: 50%; background: #8797a7; }
.source-state.connected { color: #16856d; border-color: #b8e5d8; }
.source-state.connected span { background: var(--topology-normal); box-shadow: 0 0 0 4px rgb(45 212 167 / 12%); }
.auto-refresh-control { display: inline-flex; align-items: center; gap: 7px; color: #64788d; font-size: 10px; white-space: nowrap; cursor: pointer; }
.topology-warning { border-radius: 9px; }
.health-summary { display: grid; grid-template-columns: 1.25fr repeat(3, 1fr); gap: 12px; }
.health-summary article {
  min-height: 92px;
  padding: 17px 18px;
  border: 1px solid #e0e7ef;
  border-radius: 11px;
  background: #fff;
  box-shadow: 0 5px 18px rgb(31 64 91 / 5%);
}
.health-total { display: flex; flex-direction: column; justify-content: center; }
.health-total span, .health-state span { color: #718398; font-size: 10px; font-weight: 600; }
.health-total strong { margin: 2px 0; color: #1a3550; font-size: 27px; }
.health-total small, .health-state small { color: #93a1af; font-size: 9px; }
.health-state { position: relative; display: grid; grid-template-columns: 10px minmax(0, 1fr); grid-template-rows: 1fr auto; gap: 5px 11px; }
.health-state > i { width: 9px; height: 42px; grid-row: 1 / span 2; border-radius: 5px; align-self: center; }
.health-state > div { display: flex; align-items: baseline; justify-content: space-between; }
.health-state strong { color: #243d55; font-size: 24px; }
.health-state.normal > i { background: var(--topology-normal); box-shadow: 0 0 16px rgb(45 212 167 / 25%); }
.health-state.warning > i { background: var(--topology-warning); box-shadow: 0 0 16px rgb(245 184 59 / 25%); }
.health-state.abnormal > i { background: var(--topology-abnormal); box-shadow: 0 0 16px rgb(255 93 104 / 25%); }
.observability-toolbar {
  min-height: 54px;
  padding: 10px 12px;
  display: grid;
  grid-template-columns: minmax(260px, 1fr) minmax(300px, 420px) 120px;
  align-items: center;
  gap: 12px;
  border: 1px solid #e0e7ef;
  border-radius: 10px;
  background: #fff;
}
.topology-search, .state-filter { width: 100%; }
.observability-toolbar :deep(.arco-radio-group) { width: 100%; display: flex; flex-wrap: nowrap; }
.observability-toolbar :deep(.arco-radio-button) { min-width: 78px; flex: 1 1 auto; white-space: nowrap; }
.connection-trust {
  min-height: 40px;
  padding: 7px 12px;
  display: flex;
  align-items: center;
  gap: 13px;
  color: #718398;
  border: 1px solid #e0e7ef;
  border-radius: 9px;
  background: #fff;
  font-size: 9px;
}
.connection-trust > strong { color: #385269; font-size: 9px; }
.connection-trust > span { display: inline-flex; align-items: center; gap: 5px; white-space: nowrap; }
.connection-trust i { width: 7px; height: 7px; border-radius: 2px; }
.connection-trust .runtime i { background: #63e6ff; box-shadow: 0 0 7px rgb(99 230 255 / 35%); }
.connection-trust .endpoint i { background: #38d9a9; }
.connection-trust .declared i { background: #879ca7; }
.connection-trust small { margin-left: auto; color: #91a0ae; font-size: 8px; }
.observability-loading, .observability-empty {
  min-height: 520px;
  display: flex;
  align-items: center;
  justify-content: center;
  flex-direction: column;
  gap: 10px;
  border: 1px solid #dce6ef;
  border-radius: 13px;
  background: #fff;
}
.observability-loading strong { margin-top: 8px; color: #26435e; }
.observability-loading span { color: #8292a3; font-size: 11px; }
.observatory-workspace { min-width: 0; }
.topology-scene-wrap {
  min-width: 0;
  min-height: 0;
  overflow: hidden;
  position: relative;
  border: 1px solid #d9e3ec;
  border-radius: 16px;
  background: #f7f9fc;
  box-shadow: 0 14px 38px rgb(35 66 96 / 11%);
}
.scene-heading {
  min-height: 64px;
  padding: 0 18px;
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 18px;
  position: relative;
  z-index: 5;
  color: #243b53;
  border-bottom: 1px solid #e1e8ef;
  background: #fff;
}
.scene-heading strong, .scene-heading span { display: block; }
.scene-heading strong { font-size: 14px; }
.scene-heading > div:first-child > span { margin-top: 4px; color: #6f8295; font-size: 11px; }
.scene-heading-actions, .scene-legend { display: flex; align-items: center; gap: 14px; }
.topology-view-switch :deep(.arco-radio-button) { min-width: 72px; font-size: 10px; }
.scene-legend span { display: inline-flex; align-items: center; gap: 6px; color: #61768b; font-size: 11px; }
.scene-legend i, .detail-state i { width: 7px; height: 7px; border-radius: 50%; }
.scene-legend .normal i, .detail-state.normal i { background: var(--topology-normal); }
.scene-legend .warning i, .detail-state.warning i { background: var(--topology-warning); }
.scene-legend .abnormal i, .detail-state.abnormal i { background: var(--topology-abnormal); }
.fullscreen-toggle, .detail-panel-toggle {
  padding: 6px 10px;
  display: inline-flex;
  align-items: center;
  gap: 6px;
  color: #42617e;
  border: 1px solid #cdd9e5;
  border-radius: 6px;
  background: #f4f7fa;
  font-size: 11px;
  cursor: pointer;
}
.fullscreen-toggle svg { font-size: 14px; }
.fullscreen-toggle:hover, .detail-panel-toggle:hover { color: #176cc0; border-color: #8eb9e3; background: #edf5fc; }
.topology-scene-wrap.is-fullscreen {
  width: 100vw;
  height: 100vh;
  min-height: 100vh;
  display: flex;
  flex-direction: column;
  border: 0;
  border-radius: 0;
  background: #edf2f6;
  box-shadow: none;
}
.topology-scene-wrap.is-fullscreen .scene-heading { min-height: 68px; flex: 0 0 68px; }
.topology-scene-wrap.is-fullscreen :deep(.application-topology-map) {
  height: calc(100vh - 68px);
  min-height: 0;
  flex: 1 1 auto;
}
.topology-scene-wrap.is-fullscreen :deep(.map-viewport) {
  height: calc(100vh - 114px);
  max-height: none;
}
.topology-scene-wrap.is-fullscreen .topology-detail {
  max-height: none;
  top: 84px;
  bottom: 16px;
}
.scene-filter-empty { min-height: 430px; display: grid; place-items: center; position: absolute; inset: 64px 0 0; z-index: 6; background: rgb(247 249 252 / 90%); }
.topology-detail {
  width: 332px;
  max-height: 716px;
  padding: 18px;
  overflow-y: auto;
  position: absolute;
  top: 80px;
  right: 16px;
  bottom: 16px;
  z-index: 8;
  color: #30485f;
  border: 1px solid #d5e0ea;
  border-radius: 12px;
  background: rgb(255 255 255 / 97%);
  box-shadow: 0 18px 42px rgb(42 70 98 / 20%);
  backdrop-filter: blur(14px);
  scrollbar-color: #315b82 transparent;
  scrollbar-width: thin;
}
.detail-heading { display: flex; align-items: center; justify-content: space-between; gap: 10px; }
.detail-heading > span { color: #6f8499; font-size: 11px; }
.detail-clear-selection {
  margin-left: auto;
  padding: 4px 8px;
  color: #41759e;
  border: 1px solid #d5e2ed;
  border-radius: 5px;
  background: #f6f9fc;
  cursor: pointer;
  font-size: 10px;
}
.detail-clear-selection:hover { color: #176cc0; border-color: #8eb9e3; background: #edf5fc; }
.detail-state { padding: 4px 8px; display: inline-flex; align-items: center; gap: 6px; border-radius: 14px; font-size: 11px; font-weight: 700; }
.detail-state.normal { color: #178761; background: rgb(45 185 143 / 12%); }
.detail-state.warning { color: #9b6c08; background: rgb(221 164 41 / 13%); }
.detail-state.abnormal { color: #b73c48; background: rgb(223 91 102 / 12%); }
.topology-detail h3 { margin: 14px 0 5px; overflow-wrap: anywhere; color: #20384f; font-size: 19px; }
.topology-detail > p { min-height: 34px; margin: 0; color: #667d92; font-size: 11px; line-height: 1.55; }
.detail-scope { margin: 14px 0; padding: 9px 10px; display: flex; align-items: center; justify-content: space-between; gap: 8px; border: 1px solid #e0e7ee; border-radius: 7px; background: #f6f8fb; }
.detail-scope span { color: #6f879e; font-size: 10px; }
.detail-scope strong { max-width: 180px; overflow: hidden; color: #35536e; text-overflow: ellipsis; white-space: nowrap; font-size: 11px; }
.detail-metrics { display: grid; grid-template-columns: repeat(2, 1fr); gap: 8px; }
.detail-metrics article { min-height: 62px; padding: 10px; border: 1px solid #e0e7ee; border-radius: 7px; background: #f8fafc; }
.detail-metrics span, .detail-metrics strong { display: block; }
.detail-metrics span { color: #6d879f; font-size: 10px; }
.detail-metrics strong { margin-top: 7px; color: #28455f; font-size: 15px; }
.detail-section { margin-top: 17px; padding-top: 14px; border-top: 1px solid #e4eaf0; }
.detail-section > strong { display: block; margin-bottom: 9px; color: #405d77; font-size: 11px; }
.detail-section > small { color: #718aa0; font-size: 10px; }
.detail-list, .port-list { display: flex; flex-wrap: wrap; gap: 5px; }
.detail-list span { padding: 4px 6px; color: #247e96; border-radius: 4px; background: rgb(52 158 183 / 12%); font-size: 10px; }
.port-list { margin-top: 7px; }
.port-list code { padding: 3px 5px; color: #456782; border-radius: 3px; background: rgb(95 129 165 / 12%); font-size: 10px; }
.host-list { display: flex; flex-direction: column; gap: 5px; }
.host-list span { overflow: hidden; color: #247f9d; text-overflow: ellipsis; white-space: nowrap; font-size: 10px; }
.node-alerts article { margin-top: 6px; padding: 8px; border-left: 3px solid var(--topology-warning); border-radius: 4px; background: rgb(245 184 59 / 10%); }
.node-alerts article.abnormal { border-color: var(--topology-abnormal); background: rgb(255 93 104 / 10%); }
.node-alerts span, .node-alerts small { display: block; }
.node-alerts span { color: #465f77; font-size: 10px; font-weight: 700; }
.node-alerts small { margin-top: 3px; color: #75899c; font-size: 9px; }
.connection-list { display: flex; flex-direction: column; gap: 7px; }
.connection-list article {
  padding: 9px 10px;
  border: 1px solid #dde5ed;
  border-left: 3px solid var(--topology-normal);
  border-radius: 7px;
  background: #f8fafc;
}
.connection-list article.warning { border-left-color: var(--topology-warning); }
.connection-list article.abnormal { border-left-color: var(--topology-abnormal); }
.connection-list header { display: flex; align-items: center; gap: 5px; }
.connection-list header span, .connection-list header b, .connection-list header em {
  padding: 2px 5px;
  border-radius: 4px;
  font-size: 9px;
  font-style: normal;
}
.connection-list header span { color: #9bb2c8; background: rgb(104 135 164 / 16%); }
.connection-list header b { color: #79d7e4; background: rgb(51 161 177 / 13%); }
.connection-list header em { margin-left: auto; color: #55e2b9; background: rgb(45 212 167 / 11%); }
.connection-list header em.declared { color: #9aaabd; background: rgb(119 137 157 / 14%); }
.connection-list article > strong, .connection-list article > small { display: block; }
.connection-list article > strong { margin-top: 7px; overflow-wrap: anywhere; color: #38536c; font-size: 11px; }
.connection-list article > small { margin-top: 3px; color: #6f879e; font-size: 10px; }
.connection-list article > p { margin: 6px 0 0; color: #728ba1; font-size: 10px; line-height: 1.45; }
.detail-empty { min-height: 520px; display: flex; align-items: center; justify-content: center; flex-direction: column; text-align: center; }
.detail-empty svg { color: #5e7c98; font-size: 35px; }
.detail-empty strong { margin-top: 12px; color: #48647e; }
.detail-empty span { max-width: 210px; margin-top: 6px; color: #728ba2; font-size: 11px; line-height: 1.5; }
.observability-footer {
  padding: 0 4px 10px;
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
  color: #8a99a8;
  font-size: 9px;
}
@media (max-width: 1100px) {
  .health-summary { grid-template-columns: repeat(2, 1fr); }
  .observability-hero { align-items: flex-start; flex-direction: column; }
  .hero-actions { width: 100%; flex-wrap: wrap; }
  .observability-toolbar { display: flex; align-items: stretch; flex-direction: column; }
  .connection-trust { align-items: flex-start; flex-wrap: wrap; }
  .connection-trust small { width: 100%; margin-left: 0; }
  .topology-search, .state-filter { width: 100%; }
  .observability-footer { align-items: flex-start; flex-direction: column; }
  .scene-heading { padding-top: 10px; padding-bottom: 10px; align-items: flex-start; flex-direction: column; }
  .scene-heading-actions { width: 100%; flex-wrap: wrap; }
  .topology-scene-wrap.is-fullscreen .scene-heading { min-height: 116px; flex-basis: 116px; }
  .topology-scene-wrap.is-fullscreen :deep(.application-topology-map) { height: calc(100vh - 116px); }
  .topology-scene-wrap.is-fullscreen :deep(.map-viewport) { height: calc(100vh - 162px); }
}
@media (max-width: 760px) {
  .scene-heading { padding: 0 12px; }
  .scene-legend { display: none; }
  .topology-detail {
    width: auto;
    max-height: 43%;
    top: auto;
    left: 12px;
    right: 12px;
    bottom: 12px;
  }
  .detail-empty { min-height: 180px; }
}
</style>

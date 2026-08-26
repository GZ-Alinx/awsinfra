<template>
  <div
    ref="sceneHost"
    class="application-topology-map"
    :class="{ 'detail-open': props.detailOpen, 'view-3d': props.viewMode === '3d' }"
    :style="sceneStyle"
  >
    <header class="map-context">
      <div>
        <span class="source-live" />
        <strong>{{ props.viewMode === '3d' ? '实时空间架构图' : '实时系统架构图' }}</strong>
        <small>{{ props.nodes.length }} 个节点 · {{ props.edges.length }} 条关系</small>
      </div>
      <div class="map-hints">
        <span><i class="solid" />Kubernetes 已验证</span>
        <span><i class="dashed" />配置依赖</span>
        <small>{{ interactionHint }}</small>
        <div class="map-zoom" aria-label="拓扑缩放控制">
          <button type="button" :disabled="zoom <= minimumZoom" aria-label="缩小拓扑" title="缩小" @click="changeZoom(-zoomStep)">−</button>
          <button type="button" class="zoom-value" title="恢复 100%" @click="setZoom(1)">{{ zoomPercent }}%</button>
          <button type="button" :disabled="zoom >= maximumZoom" aria-label="放大拓扑" title="放大" @click="changeZoom(zoomStep)">＋</button>
          <button type="button" class="fit-view" title="根据当前可用区域缩放" @click="fitToViewport">适应</button>
        </div>
        <button
          v-if="props.selectionPinned || zoom !== 1"
          type="button"
          class="restore-overview"
          @click="restoreOverview"
        >
          恢复全景
        </button>
      </div>
    </header>

    <div ref="mapViewport" class="map-viewport" @wheel="handleWheel">
      <div class="map-board-stage" :style="stageStyle">
        <div
          class="architecture-board"
          role="img"
          :aria-label="sceneDescription"
          :style="boardStyle"
          @pointermove="updateTilt"
          @mouseleave="resetInteraction"
          @click="handleBoardClick"
        >
        <aside class="project-rail">
          <span>{{ props.environmentName || '当前环境' }}</span>
          <strong>{{ props.projectName || '项目' }}</strong>
          <b>应用系统架构图</b>
          <small>REAL-TIME ARCHITECTURE</small>
        </aside>

        <section
          v-for="region in regionLayouts"
          :key="region.key"
          :class="['architecture-region', `region-${region.key}`]"
          :style="region.style"
        >
          <header>
            <strong>{{ region.label }}</strong>
            <span>{{ region.subtitle }}</span>
          </header>
        </section>

        <div
          v-for="region in subregionLayouts"
          :key="region.key"
          :class="['subregion', `subregion-${region.key}`]"
          :style="region.style"
        >
          <span>{{ region.label }}</span>
          <small>{{ region.count }} 项</small>
        </div>

        <aside class="cluster-panel" :style="clusterPanelStyle">
          <header><TopologyNodeIcon family="workload" badge="EKS" title="EKS 集群" color="#ec6541" /></header>
          <strong>EKS 集群运行区</strong>
          <span>当前项目登记的应用空间</span>
          <div class="cluster-metrics">
            <article><b>{{ clusterSummary.namespaces }}</b><small>Namespace</small></article>
            <article><b>{{ clusterSummary.workloads }}</b><small>工作负载</small></article>
            <article><b>{{ clusterSummary.ready }}/{{ clusterSummary.desired }}</b><small>就绪副本</small></article>
          </div>
          <footer :class="clusterSummary.state"><i />{{ stateLabel(clusterSummary.state) }}</footer>
        </aside>

        <svg class="relation-layer" :viewBox="`0 0 ${mapWidth} ${mapHeight}`" aria-hidden="true">
          <defs>
            <marker id="architecture-arrow" markerWidth="8" markerHeight="8" refX="7" refY="4" orient="auto" markerUnits="strokeWidth">
              <path d="M 0 0 L 8 4 L 0 8 z" fill="currentColor" />
            </marker>
          </defs>
          <path
            v-for="edge in edgeLayouts"
            :key="edge.edge.id"
            :d="edge.path"
            :class="edgeClasses(edge.edge)"
            marker-end="url(#architecture-arrow)"
          />
        </svg>

        <button
          v-for="item in nodeLayouts"
          :key="item.node.id"
          type="button"
          :class="nodeClasses(item.node)"
          :style="{
            left: `${item.x}px`,
            top: `${item.y}px`,
            width: `${item.width}px`,
            height: `${item.height}px`,
            '--node-accent': item.presentation.color,
            '--node-depth': `${item.depth}px`,
          }"
          :aria-label="`${item.node.name}，${kindLabel(item.node)}，${stateLabel(item.node.state)}`"
          :aria-pressed="props.selectionPinned && item.node.id === props.selectedId"
          @mouseenter="hoveredID = item.node.id"
          @mouseleave="hoveredID = ''"
          @focus="hoveredID = item.node.id"
          @blur="hoveredID = ''"
          @click.stop="handleNodeSelect(item.node.id)"
        >
          <TopologyNodeIcon
            :family="item.presentation.family"
            :badge="item.presentation.badge"
            :title="item.presentation.title"
            :color="item.presentation.color"
          />
          <span class="node-copy">
            <strong :title="item.node.name">{{ item.node.name }}</strong>
            <small :title="item.node.namespace"><b>{{ zoneShort(item.zone) }}</b>{{ item.node.namespace }}</small>
          </span>
          <span :class="['node-health', item.node.state]" :title="stateLabel(item.node.state)" />
        </button>

          <div v-if="zoneGroups.operations.length === 0" class="operations-empty" :style="operationsEmptyStyle">
            当前观测范围未发现 CI/CD 或监控工作负载
          </div>
        </div>
      </div>
    </div>
  </div>
</template>

<script setup lang="ts">
import {
  computed, nextTick, onMounted, onUnmounted, ref, watch,
} from 'vue';
import TopologyNodeIcon from '@/components/TopologyNodeIcon.vue';
import {
  topologyArchitectureZone,
  topologyNodePresentation,
  type TopologyArchitectureZone,
  type TopologyNodePresentation,
} from '@/services/topologyPresentation';
import type {
  ApplicationHealthState,
  ApplicationTopologyEdge,
  ApplicationTopologyNode,
} from '@/types';

type NodeLayout = {
  node: ApplicationTopologyNode;
  zone: TopologyArchitectureZone;
  x: number;
  y: number;
  width: number;
  height: number;
  depth: number;
  presentation: TopologyNodePresentation;
};

const props = defineProps<{
  nodes: ApplicationTopologyNode[];
  edges: ApplicationTopologyEdge[];
  selectedId: string;
  detailOpen?: boolean;
  selectionPinned?: boolean;
  viewMode?: '2d' | '3d';
  projectName?: string;
  environmentName?: string;
}>();
const emit = defineEmits<{ select: [id: string]; clear: [] }>();

const sceneHost = ref<HTMLDivElement | null>(null);
const mapViewport = ref<HTMLDivElement | null>(null);
const viewportWidth = ref(1280);
const hoveredID = ref('');
const tiltX = ref(0);
const tiltY = ref(0);
const zoom = ref(1);
let resizeObserver: ResizeObserver | null = null;

const minimumZoom = 0.55;
const maximumZoom = 1.5;
const zoomStep = 0.1;

const leftRail = 122;
const mainLeft = 142;
const outer = 16;
const nodeWidth = 168;
const nodeHeight = 56;
const nodePitchX = 184;
const nodePitchY = 68;
const edgeRegionHeight = 192;
const businessTop = outer + edgeRegionHeight + 14;
const dataRegionHeight = 106;

const emptyGroups = (): Record<TopologyArchitectureZone, ApplicationTopologyNode[]> => ({
  edge: [], service: [], workload: [], data: [], operations: [],
});

const zoneGroups = computed(() => {
  const groups = emptyGroups();
  props.nodes.forEach((node) => groups[topologyArchitectureZone(node)].push(node));
  Object.values(groups).forEach((nodes) => nodes.sort((left, right) => (
    `${left.namespace}/${left.name}`.localeCompare(`${right.namespace}/${right.name}`)
  )));

  const neighbourOrder = new Map<string, number>();
  groups.service.forEach((node, index) => neighbourOrder.set(node.id, index));
  groups.workload.sort((left, right) => {
    const rank = (node: ApplicationTopologyNode) => {
      const indexes = props.edges
        .filter((edge) => edge.source === node.id || edge.target === node.id)
        .map((edge) => edge.source === node.id ? edge.target : edge.source)
        .filter((id) => neighbourOrder.has(id))
        .map((id) => Number(neighbourOrder.get(id)));
      return indexes.length ? indexes.reduce((total, index) => total + index, 0) / indexes.length : Number.MAX_SAFE_INTEGER;
    };
    return rank(left) - rank(right) || left.name.localeCompare(right.name);
  });
  return groups;
});

const edgeDomains = computed(() => zoneGroups.value.edge.filter((node) => node.kind === 'Domain'));
const edgeGateways = computed(() => zoneGroups.value.edge.filter((node) => node.kind !== 'Domain'));
const balancedRows = (count: number, maximum: number) => {
  if (count <= 1) return 1;
  if (count <= 4) return Math.min(2, maximum);
  if (count <= 9) return Math.min(3, maximum);
  return maximum;
};
const serviceRows = computed(() => balancedRows(zoneGroups.value.service.length, 4));
const workloadRows = computed(() => balancedRows(zoneGroups.value.workload.length, 4));
const applicationRows = computed(() => Math.max(serviceRows.value, workloadRows.value));
const operationRows = computed(() => balancedRows(zoneGroups.value.operations.length, 3));
const serviceColumns = computed(() => Math.max(1, Math.ceil(zoneGroups.value.service.length / serviceRows.value)));
const workloadColumns = computed(() => Math.max(1, Math.ceil(zoneGroups.value.workload.length / workloadRows.value)));
const domainColumns = computed(() => Math.max(1, Math.ceil(edgeDomains.value.length / 2)));
const gatewayColumns = computed(() => Math.max(1, Math.ceil(edgeGateways.value.length / 2)));
const operationColumns = computed(() => Math.max(1, Math.ceil(zoneGroups.value.operations.length / operationRows.value)));
const applicationRegionHeight = computed(() => Math.max(
  222,
  34 + (applicationRows.value - 1) * nodePitchY + nodeHeight + 18,
));
const dataTop = computed(() => businessTop + 42 + applicationRegionHeight.value + 14);
const businessHeight = computed(() => Math.max(
  398,
  dataTop.value + dataRegionHeight + 18 - businessTop,
));
const operationsTop = computed(() => businessTop + businessHeight.value + 14);
const operationsHeight = computed(() => Math.max(
  zoneGroups.value.operations.length === 0 ? 142 : 178,
  58 + (operationRows.value - 1) * nodePitchY + nodeHeight + 20,
));
const mapHeight = computed(() => operationsTop.value + operationsHeight.value + outer);

const requiredColumns = computed(() => Math.max(
  domainColumns.value + gatewayColumns.value,
  serviceColumns.value + workloadColumns.value + 1,
  operationColumns.value + 2,
));
const dataRequiredWidth = computed(() => (
  mainLeft + 176 + zoneGroups.value.data.length * 115 + 278 + outer
));
const mapWidth = computed(() => Math.max(
  1440,
  Math.floor(viewportWidth.value),
  mainLeft + requiredColumns.value * nodePitchX + 330,
  dataRequiredWidth.value,
));
const businessWidth = computed(() => mapWidth.value - mainLeft - 278);
const clusterLeft = computed(() => mapWidth.value - 264);
const serviceAreaWidth = computed(() => Math.max(220, serviceColumns.value * nodePitchX + 42));
const workloadLeft = computed(() => mainLeft + 24 + serviceAreaWidth.value + 24);
const workloadAreaWidth = computed(() => Math.max(220, businessWidth.value - serviceAreaWidth.value - 72));

const regionLayouts = computed(() => [
  {
    key: 'edge', label: '公网接入与域名解析', subtitle: 'DNS、域名与网关入口',
    style: { left: `${mainLeft}px`, top: `${outer}px`, width: `${mapWidth.value - mainLeft - outer}px`, height: `${edgeRegionHeight}px` },
  },
  {
    key: 'business', label: '业务服务运行区', subtitle: 'Service、Deployment 与数据依赖',
    style: { left: `${mainLeft}px`, top: `${businessTop}px`, width: `${businessWidth.value}px`, height: `${businessHeight.value}px` },
  },
  {
    key: 'cluster', label: '云原生运行', subtitle: 'EKS 状态汇总',
    style: { left: `${clusterLeft.value}px`, top: `${businessTop}px`, width: '246px', height: `${businessHeight.value}px` },
  },
  {
    key: 'operations', label: '运维保障体系', subtitle: 'CI/CD、监控、日志与发布能力',
    style: { left: `${mainLeft}px`, top: `${operationsTop.value}px`, width: `${mapWidth.value - mainLeft - outer}px`, height: `${operationsHeight.value}px` },
  },
]);

const subregionLayouts = computed(() => {
  const domainWidth = Math.max(248, domainColumns.value * nodePitchX + 38);
  const gatewayLeft = mainLeft + 18 + domainWidth + 18;
  return [
    { key: 'domains', label: 'DNS / 域名入口', count: edgeDomains.value.length, style: { left: `${mainLeft + 16}px`, top: '43px', width: `${domainWidth}px`, height: '152px' } },
    { key: 'gateways', label: '网关 / Ingress', count: edgeGateways.value.length, style: { left: `${gatewayLeft}px`, top: '43px', width: `${mapWidth.value - gatewayLeft - 34}px`, height: '152px' } },
    { key: 'services', label: '服务发现 / Service', count: zoneGroups.value.service.length, style: { left: `${mainLeft + 16}px`, top: `${businessTop + 42}px`, width: `${serviceAreaWidth.value}px`, height: `${applicationRegionHeight.value}px` } },
    { key: 'workloads', label: '核心应用工作负载', count: zoneGroups.value.workload.length, style: { left: `${workloadLeft.value}px`, top: `${businessTop + 42}px`, width: `${workloadAreaWidth.value}px`, height: `${applicationRegionHeight.value}px` } },
    { key: 'data', label: '中间件与数据库', count: zoneGroups.value.data.length, style: { left: `${mainLeft + 16}px`, top: `${dataTop.value}px`, width: `${businessWidth.value - 32}px`, height: `${dataRegionHeight}px` } },
  ];
});

const layoutRows = (
  nodes: ApplicationTopologyNode[], zone: TopologyArchitectureZone, startX: number, startY: number, rows: number, depth: number,
) => nodes.map((node, index): NodeLayout => ({
  node,
  zone,
  x: startX + Math.floor(index / rows) * nodePitchX,
  y: startY + (index % rows) * nodePitchY,
  width: nodeWidth,
  height: nodeHeight,
  depth,
  presentation: topologyNodePresentation(node),
}));

const layoutDataStrip = (nodes: ApplicationTopologyNode[]) => nodes.map((node, index): NodeLayout => ({
  node,
  zone: 'data',
  x: mainLeft + 176 + index * 115,
  y: dataTop.value + 36,
  width: 108,
  height: nodeHeight,
  depth: 30,
  presentation: topologyNodePresentation(node),
}));

const nodeLayouts = computed<NodeLayout[]>(() => {
  const domainWidth = Math.max(248, domainColumns.value * nodePitchX + 38);
  const gatewayStart = mainLeft + 18 + domainWidth + 32;
  return [
    ...layoutRows(edgeDomains.value, 'edge', mainLeft + 34, 66, 2, 42),
    ...layoutRows(edgeGateways.value, 'edge', gatewayStart, 66, 2, 42),
    ...layoutRows(zoneGroups.value.service, 'service', mainLeft + 36, businessTop + 76, serviceRows.value, 34),
    ...layoutRows(zoneGroups.value.workload, 'workload', workloadLeft.value + 20, businessTop + 76, workloadRows.value, 44),
    ...layoutDataStrip(zoneGroups.value.data),
    ...layoutRows(zoneGroups.value.operations, 'operations', mainLeft + 176, operationsTop.value + 58, operationRows.value, 28),
  ];
});

const layoutByID = computed(() => new Map(nodeLayouts.value.map((item) => [item.node.id, item])));
const edgeLayouts = computed(() => props.edges.map((edge) => {
  // For human-facing request flow, a domain precedes the gateway even though
  // the API relation is named "gateway exposes domain".
  const reverseForTraffic = edge.relation === 'exposes_domain';
  const source = layoutByID.value.get(reverseForTraffic ? edge.target : edge.source);
  const target = layoutByID.value.get(reverseForTraffic ? edge.source : edge.target);
  if (!source || !target) return null;
  const sourceCenter = { x: source.x + source.width / 2, y: source.y + source.height / 2 };
  const targetCenter = { x: target.x + target.width / 2, y: target.y + target.height / 2 };
  const horizontal = Math.abs(targetCenter.x - sourceCenter.x) > Math.abs(targetCenter.y - sourceCenter.y);
  if (horizontal) {
    const forward = targetCenter.x >= sourceCenter.x;
    const sx = forward ? source.x + source.width : source.x;
    const tx = forward ? target.x : target.x + target.width;
    const bend = Math.max(34, Math.abs(tx - sx) * .42);
    return { edge, path: `M ${sx} ${sourceCenter.y} C ${forward ? sx + bend : sx - bend} ${sourceCenter.y}, ${forward ? tx - bend : tx + bend} ${targetCenter.y}, ${tx} ${targetCenter.y}` };
  }
  const forward = targetCenter.y >= sourceCenter.y;
  const sy = forward ? source.y + source.height : source.y;
  const ty = forward ? target.y : target.y + target.height;
  const bend = Math.max(34, Math.abs(ty - sy) * .42);
  return { edge, path: `M ${sourceCenter.x} ${sy} C ${sourceCenter.x} ${forward ? sy + bend : sy - bend}, ${targetCenter.x} ${forward ? ty - bend : ty + bend}, ${targetCenter.x} ${ty}` };
}).filter((item): item is { edge: ApplicationTopologyEdge; path: string } => Boolean(item)));

const focusID = computed(() => (props.selectionPinned && props.selectedId ? props.selectedId : hoveredID.value));
const focusedNodeIDs = computed(() => {
  const ids = new Set<string>();
  if (!focusID.value) return ids;
  ids.add(focusID.value);
  props.edges.forEach((edge) => {
    if (edge.source === focusID.value) ids.add(edge.target);
    if (edge.target === focusID.value) ids.add(edge.source);
  });
  return ids;
});

const edgeClasses = (edge: ApplicationTopologyEdge) => ({
  'relation-edge': true,
  declared: !edge.verified,
  runtime: edge.relation === 'runtime_request',
  warning: edge.state === 'warning',
  abnormal: edge.state === 'abnormal',
  focused: Boolean(focusID.value) && (edge.source === focusID.value || edge.target === focusID.value),
  muted: Boolean(focusID.value) && edge.source !== focusID.value && edge.target !== focusID.value,
});
const nodeClasses = (node: ApplicationTopologyNode) => ({
  'architecture-node': true,
  [`zone-${topologyArchitectureZone(node)}`]: true,
  [`state-${node.state}`]: true,
  selected: node.id === props.selectedId && props.selectionPinned,
  focused: node.id === focusID.value,
  related: Boolean(focusID.value) && focusedNodeIDs.value.has(node.id) && node.id !== focusID.value,
  muted: Boolean(focusID.value) && !focusedNodeIDs.value.has(node.id),
});

const clusterSummary = computed(() => {
  const workloads = zoneGroups.value.workload;
  const namespaces = new Set(props.nodes.map((node) => node.namespace).filter(Boolean)).size;
  const desired = workloads.reduce((total, node) => total + (node.desired_replicas || node.pods || 0), 0);
  const ready = workloads.reduce((total, node) => total + (node.ready_replicas || node.ready_pods || 0), 0);
  const state: ApplicationHealthState = props.nodes.some((node) => node.state === 'abnormal')
    ? 'abnormal'
    : props.nodes.some((node) => node.state === 'warning') ? 'warning' : 'normal';
  return { namespaces, workloads: workloads.length, desired, ready, state };
});

const sceneDescription = computed(() => `项目应用系统架构图，共 ${props.nodes.length} 个节点、${props.edges.length} 条有依据的关系`);
const interactionHint = computed(() => props.selectionPinned
  ? '链路已锁定；再次点击或点击空白恢复'
  : '点击锁定链路；Ctrl/Command + 滚轮缩放');
const zoomPercent = computed(() => Math.round(zoom.value * 100));
const scaledMapHeight = computed(() => Math.ceil(mapHeight.value * zoom.value));
const stageStyle = computed(() => ({
  width: `${Math.floor(mapWidth.value * zoom.value)}px`,
  height: `${scaledMapHeight.value}px`,
}));
const sceneStyle = computed(() => ({
  minHeight: `${Math.min(scaledMapHeight.value, 790) + 46}px`,
}));
const boardStyle = computed(() => ({
  width: `${mapWidth.value}px`,
  height: `${mapHeight.value}px`,
  '--user-zoom': zoom.value,
  '--tilt-x': `${tiltX.value}deg`,
  '--tilt-y': `${tiltY.value}deg`,
}));
const clusterPanelStyle = computed(() => ({ left: `${clusterLeft.value + 22}px`, top: `${businessTop + 70}px` }));
const operationsEmptyStyle = computed(() => ({ left: `${mainLeft + 176}px`, top: `${operationsTop.value + 72}px` }));

const kindLabel = (node: ApplicationTopologyNode) => {
  if (node.layer === 'data') return ({ Service: '数据服务', StatefulSet: '有状态服务', Deployment: '数据组件' }[node.kind] || node.kind);
  return ({ Gateway: '网关', Domain: '域名', Service: 'Service', Deployment: 'Deployment', StatefulSet: 'StatefulSet', DaemonSet: 'DaemonSet' }[node.kind] || node.kind);
};
const zoneShort = (zone: TopologyArchitectureZone) => ({ edge: 'EDGE', service: 'SVC', workload: 'APP', data: 'DATA', operations: 'OPS' }[zone]);
const stateLabel = (state: ApplicationHealthState) => ({ normal: '正常', warning: '告警', abnormal: '异常' }[state] || '未知');

const updateTilt = (event: PointerEvent) => {
  if (props.viewMode !== '3d') return;
  const rect = (event.currentTarget as HTMLElement).getBoundingClientRect();
  tiltY.value = (((event.clientX - rect.left) / rect.width) - .5) * 3.6;
  tiltX.value = (((event.clientY - rect.top) / rect.height) - .5) * -2.4;
};
const resetInteraction = () => {
  hoveredID.value = '';
  tiltX.value = 0;
  tiltY.value = 0;
};
const setZoom = (value: number) => {
  zoom.value = Math.min(maximumZoom, Math.max(minimumZoom, Math.round(value * 100) / 100));
};
const changeZoom = (delta: number) => setZoom(zoom.value + delta);
const fitToViewport = () => {
  const availableWidth = Math.max(360, (mapViewport.value?.clientWidth || viewportWidth.value) - 2);
  setZoom(Math.min(1, availableWidth / mapWidth.value));
  if (mapViewport.value) {
    mapViewport.value.scrollTo({ left: 0, top: 0, behavior: 'smooth' });
  }
};
const restoreOverview = () => {
  setZoom(1);
  emit('clear');
  if (mapViewport.value) {
    mapViewport.value.scrollTo({ left: 0, top: 0, behavior: 'smooth' });
  }
};
const handleWheel = (event: WheelEvent) => {
  if (!event.ctrlKey && !event.metaKey) return;
  event.preventDefault();
  changeZoom(event.deltaY > 0 ? -zoomStep : zoomStep);
};
const handleBoardClick = (event: MouseEvent) => {
  if ((event.target as HTMLElement).closest('.architecture-node')) return;
  hoveredID.value = '';
  emit('clear');
};
const handleNodeSelect = (id: string) => {
  if (props.selectionPinned && props.selectedId === id) hoveredID.value = '';
  emit('select', id);
};
const updateWidth = () => {
  if (mapViewport.value) viewportWidth.value = Math.max(360, mapViewport.value.clientWidth);
};
onMounted(async () => {
  await nextTick();
  resizeObserver = new ResizeObserver(updateWidth);
  if (mapViewport.value) resizeObserver.observe(mapViewport.value);
  updateWidth();
});
onUnmounted(() => resizeObserver?.disconnect());
watch(() => [props.nodes, props.edges, props.viewMode], resetInteraction, { deep: true });
</script>

<style scoped>
.application-topology-map {
  min-height: 0;
  overflow: hidden;
  color: #263c51;
  background: linear-gradient(180deg, #f8fafc 0%, #edf2f6 100%);
}
.map-context {
  min-height: 46px;
  padding: 0 14px;
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 16px;
  color: #597086;
  border-bottom: 1px solid #dde5ed;
  background: #fff;
  font-size: 12px;
}
.map-context > div, .map-hints, .map-hints span { display: flex; align-items: center; }
.map-context > div { gap: 8px; }
.map-context strong { color: #243b53; font-size: 13px; }
.map-context small { color: #73869a; font-size: 11px; }
.source-live { width: 7px; height: 7px; border-radius: 50%; background: #21b98c; box-shadow: 0 0 0 4px rgb(33 185 140 / 12%); }
.map-hints { gap: 14px; }
.map-hints span { gap: 5px; white-space: nowrap; }
.map-hints i { width: 16px; height: 0; border-top: 2px solid #7289a0; }
.map-hints i.dashed { border-top-style: dashed; }
.map-zoom {
  height: 30px;
  padding: 2px;
  display: inline-flex;
  align-items: center;
  gap: 2px;
  border: 1px solid #d7e1ea;
  border-radius: 7px;
  background: #f7f9fb;
}
.map-zoom button, .restore-overview {
  height: 24px;
  padding: 0 8px;
  color: #49647d;
  border: 0;
  border-radius: 5px;
  background: transparent;
  cursor: pointer;
  font-size: 11px;
  transition: color .15s ease, background .15s ease;
}
.map-zoom button:not(:disabled):hover, .restore-overview:hover { color: #176cc0; background: #e8f2fb; }
.map-zoom button:disabled { opacity: .35; cursor: not-allowed; }
.map-zoom .zoom-value { min-width: 48px; color: #24445f; font-weight: 700; }
.map-zoom .fit-view { border-left: 1px solid #d7e1ea; border-radius: 0 5px 5px 0; }
.restore-overview { flex: 0 0 auto; color: #176cc0; background: #edf5fc; }
.map-viewport {
  width: 100%;
  max-height: 790px;
  overflow: auto;
  perspective: 1520px;
  perspective-origin: 50% 2%;
  scrollbar-color: #aebdca #eef3f7;
  scrollbar-gutter: stable;
}
.map-board-stage {
  margin: 0 auto;
  overflow: hidden;
  position: relative;
  transform-style: preserve-3d;
  transition: width .2s ease, height .2s ease;
}
.architecture-board {
  position: relative;
  overflow: hidden;
  border: 1px solid #8fa1b2;
  background-color: #eef3f6;
  background-image:
    radial-gradient(circle at 82% 8%, rgb(116 181 205 / 11%), transparent 28%),
    linear-gradient(rgb(73 98 119 / 5%) 1px, transparent 1px),
    linear-gradient(90deg, rgb(73 98 119 / 5%) 1px, transparent 1px);
  background-size: auto, 26px 26px, 26px 26px;
  transform: scale(var(--user-zoom, 1));
  transform-origin: 0 0;
  transform-style: preserve-3d;
  transition: transform .24s ease;
}
.view-3d .architecture-board {
  transform: scale(var(--user-zoom, 1)) translateY(8px) rotateX(calc(6.2deg + var(--tilt-x, 0deg))) rotateY(var(--tilt-y, 0deg)) scale(.972);
  box-shadow: 0 42px 78px rgb(33 56 80 / 25%);
}
.project-rail {
  width: 108px;
  padding: 18px 12px;
  display: flex;
  align-items: center;
  justify-content: center;
  flex-direction: column;
  position: absolute;
  left: 16px;
  top: 16px;
  bottom: 16px;
  z-index: 1;
  color: #17586c;
  border: 1px solid #68aeba;
  background: linear-gradient(180deg, #a9e2e4, #86d1d5);
  text-align: center;
  box-shadow: inset 0 0 0 1px rgb(255 255 255 / 35%);
  transform-style: preserve-3d;
}
.view-3d .project-rail { box-shadow: 0 18px 28px rgb(26 80 92 / 20%); transform: translateZ(26px); }
.project-rail span { color: #36717f; font-size: 11px; }
.project-rail strong { margin-top: 10px; overflow-wrap: anywhere; color: #1d5970; font-size: 17px; }
.project-rail b { margin-top: 10px; color: #16718b; font-size: 18px; line-height: 1.45; }
.project-rail small { margin-top: 18px; color: #438993; font-size: 7px; letter-spacing: .12em; writing-mode: vertical-rl; }
.architecture-region {
  position: absolute;
  z-index: 0;
  border: 1px solid #81909d;
  background: rgb(255 255 255 / 33%);
  transform-style: preserve-3d;
}
.architecture-region > header {
  height: 30px;
  padding: 0 12px;
  display: flex;
  align-items: center;
  gap: 12px;
  border-bottom: 1px solid #9ba7b2;
  background: #91d962;
}
.architecture-region > header strong { color: #274539; font-size: 12px; }
.architecture-region > header span { color: #527064; font-size: 9px; }
.region-edge { background: rgb(239 243 246 / 92%); }
.region-business { background: rgb(211 214 217 / 72%); }
.region-cluster { background: rgb(226 229 231 / 86%); }
.region-cluster > header { background: #99dc6c; }
.region-operations > header { background: #f0a044; }
.region-operations { background: rgb(234 238 240 / 91%); }
.view-3d .architecture-region { box-shadow: 0 18px 32px rgb(44 65 84 / 15%); transform: translateZ(10px); }
.subregion {
  padding: 25px 8px 8px;
  position: absolute;
  z-index: 1;
  border: 1px solid #a2adb7;
  background: rgb(255 255 255 / 27%);
  pointer-events: none;
  transform-style: preserve-3d;
}
.view-3d .subregion { box-shadow: 0 9px 18px rgb(45 65 83 / 9%); transform: translateZ(16px); }
.subregion > span { position: absolute; left: 8px; top: 6px; color: #3e5366; font-size: 10px; font-weight: 700; }
.subregion > small { position: absolute; right: 8px; top: 6px; color: #758695; font-size: 8px; }
.subregion-domains { background: rgb(150 222 106 / 12%); }
.subregion-gateways { background: rgb(237 167 73 / 12%); }
.subregion-services { background: rgb(72 169 187 / 9%); }
.subregion-workloads { background: rgb(85 115 217 / 8%); }
.subregion-data { background: rgb(244 209 84 / 17%); }
.cluster-panel {
  width: 202px;
  min-height: 244px;
  padding: 18px;
  display: flex;
  align-items: center;
  flex-direction: column;
  position: absolute;
  z-index: 3;
  border: 1px solid #c2a36d;
  background: linear-gradient(160deg, #fff9e3, #f2e4a6);
  box-shadow: 0 8px 20px rgb(77 67 43 / 10%);
  transform-style: preserve-3d;
}
.view-3d .cluster-panel { box-shadow: 0 24px 38px rgb(95 72 33 / 20%); transform: translateZ(40px); }
.cluster-panel > header { margin-bottom: 10px; }
.cluster-panel > strong { color: #b95637; font-size: 14px; }
.cluster-panel > span { margin-top: 5px; color: #827452; font-size: 9px; }
.cluster-metrics { width: 100%; margin-top: 18px; display: grid; grid-template-columns: repeat(2, 1fr); gap: 7px; }
.cluster-metrics article { padding: 8px; border: 1px solid rgb(139 119 74 / 20%); background: rgb(255 255 255 / 48%); }
.cluster-metrics article:last-child { grid-column: 1 / -1; }
.cluster-metrics b, .cluster-metrics small { display: block; }
.cluster-metrics b { color: #4d5560; font-size: 15px; }
.cluster-metrics small { margin-top: 3px; color: #8c826d; font-size: 8px; }
.cluster-panel footer { margin-top: 14px; padding: 5px 9px; display: inline-flex; align-items: center; gap: 5px; border-radius: 14px; font-size: 9px; }
.cluster-panel footer i { width: 7px; height: 7px; border-radius: 50%; background: #27b88a; }
.cluster-panel footer.normal { color: #197b60; background: rgb(39 184 138 / 12%); }
.cluster-panel footer.warning { color: #976b0d; background: rgb(221 164 41 / 16%); }
.cluster-panel footer.warning i { background: #dda429; }
.cluster-panel footer.abnormal { color: #af3d48; background: rgb(223 91 102 / 14%); }
.cluster-panel footer.abnormal i { background: #df5b66; }
.relation-layer { width: 100%; height: 100%; position: absolute; inset: 0; z-index: 2; overflow: visible; pointer-events: none; transform: translateZ(18px); }
.relation-edge {
  color: #687988;
  fill: none;
  stroke: currentColor;
  stroke-width: 1.2;
  opacity: .48;
  transition: opacity .15s ease, stroke-width .15s ease, color .15s ease;
}
.relation-edge.declared { stroke-dasharray: 5 5; }
.relation-edge.runtime { color: #1c9ab1; stroke-width: 1.7; }
.relation-edge.warning { color: #c38c25; opacity: .75; }
.relation-edge.abnormal { color: #d75662; opacity: .9; }
.relation-edge.focused { color: #1f70cf; stroke-width: 2.5; opacity: 1; }
.relation-edge.muted { opacity: .06; }
.architecture-node {
  padding: 0 8px;
  display: grid;
  grid-template-columns: 36px minmax(0, 1fr) 8px;
  align-items: center;
  gap: 8px;
  position: absolute;
  z-index: 4;
  color: #2c4055;
  text-align: left;
  border: 1px solid color-mix(in srgb, var(--node-accent) 34%, #bdc8d2);
  border-left: 3px solid var(--node-accent);
  border-radius: 3px;
  background: rgb(255 255 255 / 94%);
  box-shadow: 0 4px 11px rgb(38 61 84 / 12%);
  cursor: pointer;
  transform: translateZ(var(--node-depth, 0)) translateY(var(--node-lift, 0));
  transition: border-color .15s ease, box-shadow .15s ease, opacity .15s ease, transform .15s ease;
}
.view-3d .architecture-node { box-shadow: 0 11px 18px rgb(38 61 84 / 18%); }
.architecture-node:hover, .architecture-node:focus-visible, .architecture-node.focused, .architecture-node.selected {
  z-index: 7;
  --node-lift: -2px;
  border-color: #4f91d9;
  outline: none;
  box-shadow: 0 0 0 3px rgb(57 132 218 / 13%), 0 9px 20px rgb(43 82 124 / 18%);
}
.architecture-node.related { border-color: #87b5e6; box-shadow: 0 5px 14px rgb(49 102 158 / 14%); }
.architecture-node.muted { opacity: .18; }
.architecture-node.state-warning { background: #fffcf2; }
.architecture-node.state-abnormal { background: #fff7f7; }
.architecture-node.zone-data {
  padding: 0 5px;
  grid-template-columns: 30px minmax(0, 1fr) 6px;
  gap: 4px;
}
.architecture-node.zone-data :deep(.topology-icon) {
  width: 30px;
  height: 30px;
  border-radius: 8px;
}
.architecture-node.zone-data :deep(.topology-icon svg) { width: 18px; height: 18px; }
.architecture-node.zone-data .node-copy strong { font-size: 10px; }
.architecture-node.zone-data .node-copy small { font-size: 7px; }
.node-copy, .node-copy strong, .node-copy small { display: block; min-width: 0; }
.node-copy strong, .node-copy small { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.node-copy strong { color: #263c50; font-size: 12px; font-weight: 650; }
.node-copy small { margin-top: 4px; color: #718397; font-size: 9px; }
.node-copy small b { margin-right: 5px; color: var(--node-accent); font-size: 7px; letter-spacing: .04em; }
.node-health { width: 7px; height: 7px; border-radius: 50%; background: #27b88a; box-shadow: 0 0 0 3px rgb(39 184 138 / 11%); }
.node-health.warning { background: #dda429; box-shadow: 0 0 0 3px rgb(221 164 41 / 13%); }
.node-health.abnormal { background: #df5b66; box-shadow: 0 0 0 3px rgb(223 91 102 / 13%); }
.operations-empty { position: absolute; z-index: 3; color: #81909d; font-size: 10px; }
.detail-open .map-viewport { width: calc(100% - 348px); }
@media (max-width: 1100px) {
  .map-context { padding-top: 10px; padding-bottom: 10px; align-items: flex-start; flex-direction: column; }
  .map-hints { width: 100%; flex-wrap: wrap; }
  .map-hints > small { flex: 1 1 220px; }
  .detail-open .map-viewport { width: 100%; }
}
@media (max-width: 760px) {
  .map-hints span { display: none; }
}
@media (prefers-reduced-motion: reduce) {
  .relation-edge, .architecture-node, .architecture-board { transition: none; }
  .view-3d .architecture-board { transform: scale(var(--user-zoom, 1)) translateY(8px) rotateX(6.2deg) scale(.972); }
}
</style>

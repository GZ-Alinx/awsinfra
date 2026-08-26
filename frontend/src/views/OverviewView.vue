<template>
  <div>
	<div class="page-header"><div><h2>{{ store.currentProject?.display_name || '项目' }} / {{ store.currentEnvironment?.display_name || '请先创建环境' }}</h2><p>最后观测：{{ observedAt }}</p></div><a-tag :color="clusterColor" size="large">{{ clusterLabel }}</a-tag></div>
    <a-alert v-for="warning in report?.warnings || []" :key="warning" type="warning" show-icon closable class="full-card">{{ warning }}</a-alert>
    <div class="metric-grid">
      <a-card class="metric-card" hoverable><div class="metric-title"><span>EKS 集群</span><span class="metric-icon"><icon-cloud /></span></div><div class="metric-value"><a-tag :color="clusterColor" size="large">{{ clusterLabel }}</a-tag></div><div class="metric-help">{{ report?.cluster.version ? `Kubernetes ${report.cluster.version}` : report?.cluster.name || '等待刷新' }}</div></a-card>
      <a-card class="metric-card" hoverable><div class="metric-title"><span>节点健康</span><span class="metric-icon"><icon-computer /></span></div><div class="metric-value">{{ readyNodes }} / {{ report?.nodes.length || 0 }}</div><div class="metric-help">Ready / Total</div></a-card>
      <a-card class="metric-card" hoverable><div class="metric-title"><span>Pod 健康</span><span class="metric-icon"><icon-storage /></span></div><div class="metric-value">{{ report?.pods.ready || 0 }} / {{ report?.pods.total || 0 }}</div><div class="metric-help">Ready / Total</div></a-card>
      <a-card class="metric-card" hoverable><div class="metric-title"><span>组件健康</span><span class="metric-icon"><icon-apps /></span></div><div class="metric-value">{{ healthyComponents }} / {{ desiredComponents }}</div><div class="metric-help">Healthy / Desired</div></a-card>
    </div>
    <a-card class="full-card deployed-components-card">
      <template #title><span class="card-title">已成功部署组件</span></template>
      <template #extra><a-tag :color="deployedComponents.length ? 'green' : 'gray'">{{ deployedComponents.length }} 个已部署</a-tag></template>
      <div v-if="store.loadingStatus && !report" class="deployed-components-loading"><a-spin />正在读取当前环境组件状态</div>
      <div v-else-if="deployedComponents.length" class="deployed-component-grid">
        <div v-for="component in deployedComponents" :key="component.key" class="deployed-component-item">
          <div class="deployed-component-icon"><icon-apps /></div>
          <div class="deployed-component-content">
            <div class="deployed-component-heading"><strong>{{ component.display_name }}</strong><a-tag color="green" size="small">部署成功</a-tag></div>
            <span>{{ component.category || '扩展组件' }}</span>
            <small :title="component.detail || ''">{{ component.detail || '已完成部署并通过 Helm 状态探测' }}</small>
          </div>
        </div>
      </div>
      <a-empty v-else description="尚未探测到成功部署的组件；完成组件部署后点击右上角刷新" />
    </a-card>
    <a-card class="full-card kubeconfig-card">
      <template #title>
        <div class="kubeconfig-card-heading">
          <span class="kubeconfig-card-icon"><icon-code-square /></span>
          <div>
            <div class="kubeconfig-title-line">
              <strong>本地 kubeconfig 接入</strong>
              <span class="kubeconfig-state" :class="clusterHealthy ? 'ready' : 'waiting'">
                <i />{{ clusterHealthy ? '可配置' : '等待 EKS' }}
              </span>
            </div>
            <small>为当前项目环境生成本机集群访问命令</small>
          </div>
        </div>
      </template>
      <div class="kubeconfig-security-note" :class="{ warning: !clusterHealthy }">
        <icon-safe />
        <div>
          <strong>{{ clusterHealthy ? '凭据不离开用户电脑' : '当前集群暂不可接入' }}</strong>
          <span>{{ clusterHealthy ? '命令只更新本机 kubeconfig，不会读取或显示平台凭据池中的 AK/SK。' : '等待 EKS 集群恢复正常后，复制操作会自动开放。' }}</span>
        </div>
      </div>
      <div class="kubeconfig-summary">
        <div class="kubeconfig-summary-item"><span>Region</span><strong :title="kubeconfigRegion">{{ kubeconfigRegion }}</strong></div>
        <div class="kubeconfig-summary-item"><span>EKS 集群</span><strong :title="kubeconfigCluster">{{ kubeconfigCluster }}</strong></div>
        <div class="kubeconfig-summary-item"><span>Context 别名</span><strong :title="kubeconfigContext">{{ kubeconfigContext }}</strong></div>
      </div>
      <div class="kubeconfig-profile">
        <div class="kubeconfig-profile-heading">
          <strong>本机 AWS Profile <em>可选</em></strong>
          <small>只影响下方命令，不保存到平台。</small>
        </div>
        <a-input v-model="localAWSProfile" :max-length="128" allow-clear placeholder="留空使用 AWS 默认凭据链" />
      </div>
      <a-tabs v-model:active-key="kubeconfigMode" type="rounded" class="kubeconfig-tabs">
        <a-tab-pane key="merge" title="合并到 ~/.kube/config（推荐）">
          <div class="kubeconfig-command-list">
            <div class="kubeconfig-command">
              <div class="kubeconfig-command-heading"><span class="kubeconfig-step">1</span><div><strong>写入或更新集群配置</strong><small>保留已有集群，使用唯一 Context 别名。</small></div></div>
              <a-button size="small" type="primary" :disabled="!clusterHealthy" @click="copy(mergeKubeconfigCommand, 'kubeconfig 写入命令')"><icon-copy />复制</a-button>
              <pre><code>{{ mergeKubeconfigCommand }}</code></pre>
            </div>
            <div class="kubeconfig-command">
              <div class="kubeconfig-command-heading"><span class="kubeconfig-step">2</span><div><strong>切换 Context 并验证</strong><small>验证当前 AWS 身份是否可以访问集群。</small></div></div>
              <a-button size="small" :disabled="!clusterHealthy" @click="copy(mergeVerifyCommand, 'Context 验证命令')"><icon-copy />复制</a-button>
              <pre><code>{{ mergeVerifyCommand }}</code></pre>
            </div>
          </div>
        </a-tab-pane>
        <a-tab-pane key="isolated" title="独立 kubeconfig 文件">
          <div class="kubeconfig-command-list">
            <div class="kubeconfig-command">
              <div class="kubeconfig-command-heading"><span class="kubeconfig-step">1</span><div><strong>创建环境独立配置</strong><small>不修改默认 kubeconfig，适合多环境隔离。</small></div></div>
              <a-button size="small" type="primary" :disabled="!clusterHealthy" @click="copy(isolatedKubeconfigCommand, '独立 kubeconfig 命令')"><icon-copy />复制</a-button>
              <pre><code>{{ isolatedKubeconfigCommand }}</code></pre>
            </div>
            <div class="kubeconfig-command">
              <div class="kubeconfig-command-heading"><span class="kubeconfig-step">2</span><div><strong>使用独立配置验证</strong><small>KUBECONFIG 仅对这条命令生效。</small></div></div>
              <a-button size="small" :disabled="!clusterHealthy" @click="copy(isolatedVerifyCommand, '独立配置验证命令')"><icon-copy />复制</a-button>
              <pre><code>{{ isolatedVerifyCommand }}</code></pre>
            </div>
          </div>
        </a-tab-pane>
      </a-tabs>
      <div class="kubeconfig-doc-links">
        <span>本机前置工具：</span>
        <a href="https://docs.aws.amazon.com/cli/latest/userguide/getting-started-install.html" target="_blank" rel="noopener noreferrer">AWS CLI v2</a>
        <a href="https://kubernetes.io/docs/tasks/tools/" target="_blank" rel="noopener noreferrer">kubectl</a>
      </div>
    </a-card>
    <div class="content-grid">
      <a-card><template #title><span class="card-title">EKS 节点</span></template>
        <a-table :data="report?.nodes || []" :pagination="false" size="small" :scroll="{ y: 260 }">
          <template #columns><a-table-column title="节点" data-index="name" /><a-table-column title="实例" data-index="instance_type" /><a-table-column title="AZ" data-index="zone" /><a-table-column title="状态"><template #cell="{ record }"><a-tag :color="record.ready ? 'green' : 'red'">{{ record.ready ? 'Ready' : 'Not Ready' }}</a-tag></template></a-table-column></template>
          <template #empty><a-empty description="未发现集群节点" /></template>
        </a-table>
      </a-card>
      <a-card><template #title><span class="card-title">托管服务地址</span></template>
        <a-descriptions v-if="outputs.length" :column="1" size="medium" bordered>
          <a-descriptions-item v-for="item in outputs" :key="item.key" :label="item.label"><div class="endpoint-value" :title="item.value">{{ item.value }}</div></a-descriptions-item>
        </a-descriptions>
        <div v-else class="empty-block"><a-empty description="部署后显示 RDS、Aurora、Redis、ECR 和备份地址" /></div>
      </a-card>
    </div>
    <a-card><template #title><span class="card-title">需要关注的 Pod</span></template>
      <a-table :data="report?.pods.unhealthy || []" :pagination="false" size="small">
        <template #columns><a-table-column title="Namespace" data-index="namespace" /><a-table-column title="Pod" data-index="name" /><a-table-column title="Phase" data-index="phase"><template #cell="{ record }"><a-tag color="orangered">{{ record.phase }}</a-tag></template></a-table-column><a-table-column title="原因"><template #cell="{ record }">{{ record.reason || '容器未就绪' }}</template></a-table-column></template>
        <template #empty><a-empty description="没有发现异常 Pod" /></template>
      </a-table>
    </a-card>
  </div>
</template>

<script setup lang="ts">
import { computed, ref, watch } from 'vue';
import { Message } from '@arco-design/web-vue';
import { IconApps, IconCloud, IconCodeSquare, IconComputer, IconCopy, IconSafe, IconStorage } from '@arco-design/web-vue/es/icon';
import { copyToClipboard } from '@/services/clipboard';
import { usePlatformStore } from '@/stores/platform';

const store = usePlatformStore();
const kubeconfigMode = ref('merge');
const localAWSProfile = ref('');
watch(() => store.scopeRevision, () => {
  kubeconfigMode.value = 'merge';
  localAWSProfile.value = '';
}, { flush: 'sync' });
const report = computed(() => store.status);
const readyNodes = computed(() => (report.value?.nodes || []).filter((node) => node.ready).length);
const desiredComponents = computed(() => (report.value?.components || []).filter((item) => item.desired).length);
const healthyComponents = computed(() => (report.value?.components || []).filter((item) => item.status === 'healthy').length);
const visibleComponentKeys = computed(() => new Set([
  ...(store.platform?.components || []).filter((item) => !item.hidden).map((item) => item.key),
  ...(store.componentCatalog || []).map((item) => item.key),
]));
const deployedComponents = computed(() => (report.value?.components || [])
  .filter((item) => item.desired && item.actual && item.status === 'healthy' && visibleComponentKeys.value.has(item.key))
  .sort((left, right) => `${left.category}/${left.display_name}`.localeCompare(`${right.category}/${right.display_name}`, 'zh-CN')));
const clusterHealthy = computed(() => report.value?.cluster.status === 'ACTIVE' && report.value.cluster.reachable);
const clusterLabel = computed(() => !report.value ? '未检查' : clusterHealthy.value ? '正常' : '异常');
const clusterColor = computed(() => !report.value ? 'gray' : clusterHealthy.value ? 'green' : 'red');
const observedAt = computed(() => report.value?.observed_at ? new Date(report.value.observed_at).toLocaleString('zh-CN') : '尚未获取');
const kubeconfigRegion = computed(() => String(store.config?.region || store.currentEnvironment?.region || 'ap-south-1'));
const kubeconfigCluster = computed(() => report.value?.cluster.name || `${store.currentEnvironment?.target_name || `${store.currentProjectKey}-${store.currentEnvironmentKey}`}-eks`);
const kubeconfigContext = computed(() => `ops/${store.currentProjectKey}/${store.currentEnvironmentKey}`);
const kubeconfigFileName = computed(() => `${store.currentProjectKey}-${store.currentEnvironmentKey}.yaml`);
const shellQuote = (value: string) => `'${value.replace(/'/g, `'"'"'`)}'`;
const profileArgument = computed(() => localAWSProfile.value.trim() ? ` --profile ${shellQuote(localAWSProfile.value.trim())}` : '');
const updateKubeconfigBase = computed(() => `aws eks update-kubeconfig --region ${shellQuote(kubeconfigRegion.value)} --name ${shellQuote(kubeconfigCluster.value)} --alias ${shellQuote(kubeconfigContext.value)}${profileArgument.value}`);
const mergeKubeconfigCommand = computed(() => updateKubeconfigBase.value);
const mergeVerifyCommand = computed(() => `kubectl config use-context ${shellQuote(kubeconfigContext.value)} && kubectl get nodes`);
const isolatedKubeconfigPath = computed(() => `$HOME/.kube/ops-deploy/${kubeconfigFileName.value}`);
const isolatedKubeconfigCommand = computed(() => `mkdir -p "$HOME/.kube/ops-deploy" && ${updateKubeconfigBase.value} --kubeconfig "${isolatedKubeconfigPath.value}"`);
const isolatedVerifyCommand = computed(() => `KUBECONFIG="${isolatedKubeconfigPath.value}" kubectl get nodes`);
const copy = async (value: string, label: string) => {
  try { await copyToClipboard(value); Message.success(`${label}已复制`); }
  catch { Message.error('复制失败，请手动选择命令文本'); }
};
const labels: Record<string, string> = { rds_endpoint: 'RDS 管理库', aurora_writer_endpoint: 'Aurora Writer', aurora_reader_endpoint: 'Aurora Reader', elasticache_configuration_endpoint: 'ElastiCache', platform_backup_bucket: '备份 S3', ecr_repository_urls: 'ECR Repositories' };
const outputs = computed(() => Object.entries(report.value?.outputs || {}).map(([key, value]) => ({ key, label: labels[key] || key, value: typeof value === 'string' ? value : JSON.stringify(value) })));
</script>

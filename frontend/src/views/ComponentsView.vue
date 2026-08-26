<template>
  <div>
    <div class="page-header">
      <div><h2>可用组件目录</h2><p>维护平台当前支持的 Helm 组件定义。这里只登记能力，不执行部署；项目环境在“部署配置”中选择后由阶段 2 安装。</p></div>
      <a-space><a-button :loading="loading" @click="load"><icon-refresh />刷新</a-button><a-button v-if="auth.canManageComponents" type="primary" @click="openEditor()"><icon-plus />添加 Helm 组件</a-button></a-space>
    </div>

    <a-alert type="info" show-icon class="full-card">新增组件时填写 Helm 仓库、Chart 和版本，平台会调用 Helm 获取默认 values。默认参数可在页面以表单或 YAML 两种方式修改，保存后自动进入所有项目环境的可选组件列表。</a-alert>

    <a-card class="full-card">
      <template #title><span class="card-title">平台内置支持</span></template>
      <a-table :data="coreComponents" :pagination="false" row-key="key" size="small">
        <template #columns>
          <a-table-column title="组件" :width="220" data-index="display_name" />
          <a-table-column title="分类" :width="150"><template #cell="{ record }"><a-tag>{{ record.category }}</a-tag></template></a-table-column>
          <a-table-column title="用途" data-index="description" />
          <a-table-column title="接入方式" :width="150"><template #cell="{ record }"><a-tag color="green">{{ record.status_type === 'helm' ? '平台内置 Helm' : record.status_type }}</a-tag></template></a-table-column>
        </template>
      </a-table>
    </a-card>

    <a-card>
      <template #title><span class="card-title">扩展 Helm 组件</span></template>
      <a-table :data="store.componentCatalog" :pagination="{ pageSize: 10 }" row-key="key">
        <template #columns>
          <a-table-column title="组件" :width="220"><template #cell="{ record }"><div class="component-name"><strong>{{ record.display_name }}</strong><code>{{ record.key }}</code></div></template></a-table-column>
          <a-table-column title="分类" :width="130"><template #cell="{ record }"><a-tag color="purple">{{ record.category }}</a-tag></template></a-table-column>
          <a-table-column title="Helm 仓库"><template #cell="{ record }"><a-tooltip :content="record.repository"><span class="ellipsis-text">{{ record.repository }}</span></a-tooltip></template></a-table-column>
          <a-table-column title="Chart" :width="170" data-index="chart" />
          <a-table-column title="版本" :width="130"><template #cell="{ record }"><code>{{ record.chart_version || 'latest' }}</code></template></a-table-column>
          <a-table-column title="默认 Namespace" :width="160" data-index="default_namespace" />
          <a-table-column title="参数" :width="100"><template #cell="{ record }"><a-tag color="arcoblue">{{ countParameters(record.values) }} 项</a-tag></template></a-table-column>
          <a-table-column v-if="auth.canManageComponents" title="操作" :width="145" fixed="right"><template #cell="{ record }"><a-space><a-button size="mini" @click="openEditor(record)">编辑</a-button><a-popconfirm :content="`删除组件 ${record.display_name}？已有环境配置不会自动删除。`" @ok="remove(record.key)"><a-button size="mini" status="danger">删除</a-button></a-popconfirm></a-space></template></a-table-column>
        </template>
        <template #empty><a-empty description="组件目录为空"><a-button v-if="auth.canManageComponents" type="primary" @click="openEditor()">添加第一个 Helm 组件</a-button></a-empty></template>
      </a-table>
    </a-card>
  </div>

  <a-modal v-model:visible="editorVisible" :title="updating ? '编辑 Helm 组件' : '添加 Helm 组件'" width="1040px" :ok-loading="saving" ok-text="保存到组件目录" @before-ok="save" @cancel="clearEditor">
    <a-form :model="editor" layout="vertical">
      <a-grid :cols="3" :col-gap="16">
        <a-grid-item><a-form-item label="组件标识" required><a-input v-model="editor.key" :disabled="updating" placeholder="grafana-tempo" /></a-form-item></a-grid-item>
        <a-grid-item><a-form-item label="显示名称" required><a-input v-model="editor.display_name" placeholder="Grafana Tempo" /></a-form-item></a-grid-item>
        <a-grid-item><a-form-item label="组件分类" required><a-select v-model="editor.category" allow-create allow-search><a-option v-for="category in categories" :key="category" :value="category">{{ category }}</a-option></a-select></a-form-item></a-grid-item>
        <a-grid-item :span="3"><a-form-item label="说明"><a-input v-model="editor.description" placeholder="说明组件用途和依赖" /></a-form-item></a-grid-item>
        <a-grid-item :span="2"><a-form-item label="Helm 仓库" required><a-input v-model="editor.repository" placeholder="https://grafana.github.io/helm-charts 或 oci://..." /></a-form-item></a-grid-item>
        <a-grid-item><a-form-item label="Chart" required><a-input v-model="editor.chart" placeholder="tempo" /></a-form-item></a-grid-item>
        <a-grid-item><a-form-item label="Chart 版本"><a-input v-model="editor.chart_version" placeholder="留空使用仓库最新版" /></a-form-item></a-grid-item>
        <a-grid-item><a-form-item label="默认 Namespace" required><a-input v-model="editor.default_namespace" placeholder="monitoring" /></a-form-item></a-grid-item>
        <a-grid-item :span="2"><a-form-item label="副本参数路径" extra="集群模式下平台会将这些 Helm values 设为副本数；可填写多个，例如 replicaCount、controller.replicas。"><a-input-tag v-model="editor.replica_paths" placeholder="replicaCount" /></a-form-item></a-grid-item>
        <a-grid-item><a-form-item label="仓库匹配"><a-button long :loading="inspecting" @click="inspect"><icon-download />从 Helm 仓库获取默认配置</a-button></a-form-item></a-grid-item>
      </a-grid>
    </a-form>

    <a-alert v-if="inspectMessage" :type="inspectError ? 'error' : 'success'" show-icon class="full-card">{{ inspectMessage }}</a-alert>

    <a-tabs v-model:active-key="valueTab" type="rounded">
      <a-tab-pane key="form" title="可视参数编辑">
        <a-table :data="parameterRows" :pagination="{ pageSize: 12 }" size="small" row-key="path" :scroll="{ y: 360 }">
          <template #columns>
            <a-table-column title="参数路径" :width="360"><template #cell="{ record }"><code>{{ record.path }}</code></template></a-table-column>
            <a-table-column title="类型" :width="100"><template #cell="{ record }"><a-tag>{{ record.type }}</a-tag></template></a-table-column>
            <a-table-column title="值"><template #cell="{ record }">
              <a-switch v-if="record.type === 'boolean'" :model-value="record.value" @change="updateParameter(record.path, Boolean($event))" />
              <a-input-number v-else-if="record.type === 'number'" :model-value="record.value" @change="updateParameter(record.path, Number($event))" />
              <a-textarea v-else-if="record.type === 'json'" :model-value="JSON.stringify(record.value)" :auto-size="{ minRows: 1, maxRows: 4 }" @change="updateJSONParameter(record.path, String($event))" />
              <a-input v-else :model-value="String(record.value ?? '')" @change="updateParameter(record.path, String($event))" />
            </template></a-table-column>
          </template>
          <template #empty><a-empty description="Chart 未提供默认 values；可以切换到 YAML 手工填写" /></template>
        </a-table>
      </a-tab-pane>
      <a-tab-pane key="yaml" title="YAML 配置">
        <a-textarea v-model="editor.values_yaml" class="yaml-editor" :auto-size="{ minRows: 18, maxRows: 28 }" placeholder="replicaCount: 2" />
        <small class="field-help">保存时后端会再次解析并校验 YAML，最大 1 MiB。</small>
      </a-tab-pane>
    </a-tabs>
  </a-modal>
</template>

<script setup lang="ts">
import { computed, reactive, ref } from 'vue';
import { Message } from '@arco-design/web-vue';
import { IconDownload, IconPlus, IconRefresh } from '@arco-design/web-vue/es/icon';
import { parse, stringify } from 'yaml';
import { useAuthStore } from '@/stores/auth';
import { usePlatformStore } from '@/stores/platform';
import type { Dict, HelmComponent } from '@/types';

const auth = useAuthStore(); const store = usePlatformStore();
const loading = ref(false); const saving = ref(false); const inspecting = ref(false);
const editorVisible = ref(false); const updating = ref(false); const valueTab = ref('form');
const inspectMessage = ref(''); const inspectError = ref(false);
const categories = ['CICD', '配置与注册中心', '调度', '消息队列', '监控', '日志', '告警', '网关', '安全', '其他'];
	const coreComponents = computed(() => (store.platform?.components || []).filter((item) => !item.hidden));
const emptyEditor = (): HelmComponent => ({ key: '', display_name: '', category: '其他', description: '', repository: '', chart: '', chart_version: '', default_namespace: 'platform-server', replica_paths: [], values_yaml: '{}\n', values: {}, created_by: '', created_at: '', updated_at: '' });
const editor = reactive<HelmComponent>(emptyEditor());

type ParameterRow = { path: string; type: string; value: any };
const parsedValues = computed<Dict>(() => { try { return parse(editor.values_yaml || '{}') || {}; } catch { return {}; } });
const flatten = (value: any, prefix = '', result: ParameterRow[] = []): ParameterRow[] => {
  if (Array.isArray(value)) { if (prefix) result.push({ path: prefix, type: 'json', value }); return result; }
  if (value && typeof value === 'object') { for (const [key, child] of Object.entries(value)) flatten(child, prefix ? `${prefix}.${key}` : key, result); return result; }
  if (prefix) result.push({ path: prefix, type: value === null ? 'string' : typeof value, value });
  return result;
};
const parameterRows = computed(() => flatten(parsedValues.value));
const countParameters = (values: Dict) => flatten(values || {}).length;
const setPath = (source: Dict, path: string, value: any) => { const keys = path.split('.'); let target = source; for (const key of keys.slice(0, -1)) { if (!target[key] || typeof target[key] !== 'object') target[key] = {}; target = target[key]; } target[keys[keys.length - 1]] = value; };
const updateParameter = (path: string, value: any) => { const values = JSON.parse(JSON.stringify(parsedValues.value)); setPath(values, path, value); editor.values_yaml = stringify(values); };
const updateJSONParameter = (path: string, value: string) => { try { updateParameter(path, JSON.parse(value)); } catch { Message.warning('数组或对象请输入合法 JSON'); } };

const load = async () => { loading.value = true; try { await store.loadComponentCatalog(); } catch (error:any) { Message.error(error.message); } finally { loading.value = false; } };
const clearEditor = () => { Object.assign(editor, emptyEditor()); updating.value = false; valueTab.value = 'form'; inspectMessage.value = ''; inspectError.value = false; };
const openEditor = (component?: HelmComponent) => { clearEditor(); if (component) { Object.assign(editor, JSON.parse(JSON.stringify(component))); updating.value = true; } editorVisible.value = true; };
const inspect = async () => {
  if (!editor.repository || !editor.chart) { Message.warning('请先填写 Helm 仓库和 Chart'); return; }
  inspecting.value = true; inspectMessage.value = ''; inspectError.value = false;
  try { const result = await store.inspectHelmComponent(editor); editor.values_yaml = result.values_yaml || '{}\n'; editor.values = result.values || {}; const filtered = result.filtered_sensitive_paths?.length ? `；已过滤 ${result.filtered_sensitive_paths.length} 个疑似明文敏感参数` : ''; inspectMessage.value = `已从仓库获取 ${countParameters(editor.values)} 项默认参数，可以在下方直接调整${filtered}。`; }
  catch (error:any) { inspectError.value = true; inspectMessage.value = error.message; }
  finally { inspecting.value = false; }
};
const save = async () => {
  if (!editor.key || !editor.display_name || !editor.category || !editor.repository || !editor.chart || !editor.default_namespace) { Message.warning('组件标识、名称、分类、仓库、Chart和Namespace不能为空'); return false; }
  try { editor.values = parse(editor.values_yaml || '{}') || {}; } catch { Message.error('Values YAML 格式不合法'); valueTab.value = 'yaml'; return false; }
  saving.value = true;
  try { await store.saveHelmComponent(JSON.parse(JSON.stringify(editor)), updating.value); Message.success(updating.value ? '组件定义已更新' : '组件已加入平台目录'); clearEditor(); return true; }
  catch (error:any) { Message.error(error.message); return false; }
  finally { saving.value = false; }
};
const remove = async (key: string) => { try { await store.deleteHelmComponent(key); Message.success('组件已从平台目录删除'); } catch (error:any) { Message.error(error.message); } };
</script>

<style scoped>
.component-name { display:flex; flex-direction:column; gap:5px; }
.component-name code { color:var(--color-text-3); font-size:12px; }
.ellipsis-text { display:block; max-width:360px; overflow:hidden; text-overflow:ellipsis; white-space:nowrap; }
.yaml-editor :deep(textarea) { font-family:ui-monospace,SFMono-Regular,Menlo,Monaco,Consolas,monospace; font-size:12px; line-height:1.6; }
</style>

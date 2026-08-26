<template>
  <div>
    <div class="page-header">
      <div><h2>项目管理</h2><p>部署以项目为边界，每个项目最多拥有开发、测试、预发布、生产四个环境。</p></div>
      <a-button v-if="auth.canManageProjects" type="primary" @click="projectVisible = true"><icon-plus />创建项目</a-button>
    </div>

    <a-empty v-if="!store.projects.length" description="还没有可访问的项目" />
    <div v-else class="project-grid">
      <a-card v-for="project in store.projects" :key="project.key" class="project-card" :bordered="false">
		<div class="project-card-heading">
          <div class="project-title"><span class="project-mark">{{ project.display_name.slice(0, 1) }}</span><span><strong>{{ project.display_name }}</strong><small>{{ project.key }}</small></span></div>
		  <a-space class="project-actions">
			<a-button v-if="project.permission.can_configure && availableEnvironmentDefinitions(project).length" size="mini" type="primary" @click="showEnvironmentModal(project.key)"><icon-plus />创建环境</a-button>
            <a-dropdown v-if="auth.canManageProjects && project.permission.can_configure">
              <a-button size="mini"><icon-more /></a-button>
              <template #content>
                <a-doption @click="openEditProject(project)">编辑项目信息</a-doption>
                <a-doption @click="confirmDeleteProject(project)">{{ projectCleanupEnvironments(project).length ? '清理资源后删除项目' : '删除项目' }}</a-doption>
              </template>
            </a-dropdown>
		  </a-space>
		</div>
		<div class="project-status-row" aria-label="项目状态与权限">
		  <a-tag :color="project.selected_aws_credential_key ? 'green' : 'orangered'">{{ project.selected_aws_credential_key ? 'AWS 已绑定' : 'AWS 未绑定' }}</a-tag>
		  <a-tag v-if="project.permission.can_deploy" color="arcoblue">可部署</a-tag>
		  <a-tag v-if="project.permission.can_configure" color="green">可改配置</a-tag>
		</div>
        <p class="project-description">{{ project.description || '暂无项目说明' }}</p>

        <div class="environment-slots" :class="{ dense: project.environments.length > 2 }">
          <div
			v-for="environment in project.environments"
			:key="environment.environment"
            class="environment-slot"
			:class="['active', 'navigable', `status-${environmentState(project, environment.environment).code}`]"
			role="button"
			tabindex="0"
			@click="enterEnvironment(project, environment.environment)"
			@keydown.enter.prevent="enterEnvironment(project, environment.environment)"
          >
            <div class="environment-slot-heading">
			  <div><span class="environment-code">{{ environment.environment.toUpperCase() }}</span><strong>{{ environment.display_name }}</strong></div>
			  <a-tag size="small" :color="environmentState(project, environment.environment).color">{{ environmentState(project, environment.environment).label }}</a-tag>
            </div>
            <div class="environment-slot-meta">
			  <small>{{ environment.region }}</small>
			  <small :title="environmentState(project, environment.environment).detail">{{ environmentState(project, environment.environment).detail }}</small>
            </div>
			<div class="environment-slot-footer" :class="{ busy: environmentState(project, environment.environment).busy }">
			  <div v-if="environmentState(project, environment.environment).busy" class="environment-slot-progress">
				<a-progress :percent="environmentState(project, environment.environment).progress / 100" size="mini" :show-text="false" animation />
				<span>{{ Math.round(environmentState(project, environment.environment).progress) }}%</span>
			  </div>
			  <span class="environment-slot-action" :class="{ primary: environmentState(project, environment.environment).primary, busy: environmentState(project, environment.environment).busy }">
				{{ environmentState(project, environment.environment).action }}
			  </span>
			</div>
          </div>
		  <a-empty v-if="!project.environments.length" description="尚未创建环境，请使用右上角“创建环境”" />
        </div>
      </a-card>
    </div>
  </div>

  <a-modal v-model:visible="projectVisible" title="创建项目" :ok-loading="savingProject" @before-ok="createProject">
    <a-form :model="projectForm" layout="vertical">
	  <a-form-item label="项目名称" required extra="支持中文、英文、数字和常用符号。"><a-input v-model="projectForm.display_name" :max-length="128" show-word-limit /></a-form-item>
	  <a-form-item label="资源标识（可选）" extra="用于 Kubernetes、Terraform 和 AWS 资源名；不填时根据项目名称自动生成。"><a-input v-model="projectForm.key" :max-length="128" /></a-form-item>
	  <a-alert v-if="normalizedProjectKey" type="info" show-icon style="margin-bottom:16px">内部资源标识将使用：<code>{{ normalizedProjectKey }}</code></a-alert>
      <a-form-item label="项目说明"><a-textarea v-model="projectForm.description" :max-length="1000" show-word-limit /></a-form-item>
    </a-form>
  </a-modal>

  <a-modal v-model:visible="projectEditVisible" title="编辑项目信息" :ok-loading="savingProjectEdit" @before-ok="updateProject">
    <a-form :model="projectEditForm" layout="vertical">
      <a-form-item label="项目名称" required extra="仅修改平台显示名称，不会重命名或重建 AWS、EKS、Terraform 资源。">
        <a-input v-model="projectEditForm.display_name" :max-length="128" show-word-limit />
      </a-form-item>
      <a-form-item label="内部资源标识" extra="资源标识创建后不可修改，用于关联环境、权限、任务和云资源。">
        <a-input v-model="projectEditForm.key" disabled />
      </a-form-item>
      <a-form-item label="项目说明"><a-textarea v-model="projectEditForm.description" :max-length="1000" show-word-limit /></a-form-item>
    </a-form>
  </a-modal>

  <a-modal v-model:visible="environmentVisible" title="创建项目环境" :ok-loading="savingEnvironment" @before-ok="createEnvironment">
    <a-alert v-if="environmentForm.target_type === 'managed'" type="info" show-icon>平台将创建并托管 VPC、EKS、节点组、云服务和必要 Add-on；也可以克隆已有环境参数。</a-alert>
    <a-alert v-else type="warning" show-icon>直接使用当前项目 AWS 凭据下的已有 EKS，跳过阶段 1。平台只管理该项目环境的 Namespace、Helm 组件与接入配置，销毁时不会删除原 EKS、VPC 或节点组。</a-alert>
    <a-form :model="environmentForm" layout="vertical" style="margin-top:16px">
      <a-form-item label="所属项目"><a-input :model-value="projectName(environmentForm.project)" disabled /></a-form-item>
	  <a-form-item label="目标环境" required><a-select v-model="environmentForm.environment" placeholder="选择要创建的环境"><a-option v-for="definition in availableEnvironmentChoices" :key="definition.key" :value="definition.key">{{ definition.display_name }}（{{ definition.key.toUpperCase() }}）</a-option></a-select></a-form-item>
      <a-form-item label="部署目标" required>
        <a-radio-group v-model="environmentForm.target_type" type="button" @change="changeEnvironmentTarget">
          <a-radio value="managed">新建并托管 AWS 资源</a-radio>
          <a-radio value="existing_eks">接入已有 EKS</a-radio>
        </a-radio-group>
      </a-form-item>
      <a-form-item label="AWS Region" required>
        <a-select v-model="environmentForm.region" allow-search @change="changeEnvironmentRegion">
          <a-option v-for="region in store.platform?.aws_regions || []" :key="region.code" :value="region.code">{{ region.code }} · {{ region.name }}{{ region.opt_in ? '（需启用）' : '' }}</a-option>
        </a-select>
      </a-form-item>
      <a-form-item v-if="environmentForm.target_type === 'existing_eks'" label="已有 EKS 集群" required extra="列表来自当前项目选中的 AWS 凭据；需要 eks:ListClusters、eks:DescribeCluster 及该集群的 Kubernetes 管理权限。">
        <div class="eks-cluster-picker">
          <a-select v-model="environmentForm.existing_cluster_name" allow-search allow-create :loading="loadingEKSClusters" placeholder="选择集群，或输入集群名称">
            <a-option v-for="cluster in eksClusters" :key="cluster.name" :value="cluster.name">{{ cluster.name }}</a-option>
          </a-select>
          <a-button :loading="loadingEKSClusters" @click="loadEKSClusters">刷新</a-button>
        </div>
        <small v-if="eksClustersError" class="cluster-error">{{ eksClustersError }}；仍可手工输入准确集群名称。</small>
      </a-form-item>
      <a-form-item label="配置模板" required>
        <a-select v-model="environmentForm.source" placeholder="选择模板">
          <a-option value="__default__">系统默认模板 · ap-south-1（推荐）</a-option>
          <a-option v-for="source in sourceEnvironments" :key="source.value" :value="source.value">{{ source.label }}</a-option>
        </a-select>
      </a-form-item>
    </a-form>
  </a-modal>
</template>

<script setup lang="ts">
import { computed, onMounted, onUnmounted, reactive, ref } from 'vue';
import { useRouter } from 'vue-router';
import { Message, Modal } from '@arco-design/web-vue';
import { IconMore, IconPlus } from '@arco-design/web-vue/es/icon';
import { api } from '@/services/api';
import { useAuthStore } from '@/stores/auth';
import { usePlatformStore } from '@/stores/platform';
import type { EKSClusterInfo, EKSClusterResponse, EnvironmentLifecycleStatus, Project, ProjectEnvironment } from '@/types';

const router = useRouter();
const auth = useAuthStore();
const store = usePlatformStore();
const projectVisible = ref(false);
const projectEditVisible = ref(false);
const environmentVisible = ref(false);
const savingProject = ref(false);
const savingProjectEdit = ref(false);
const savingEnvironment = ref(false);
const projectForm = reactive({ key: '', display_name: '', description: '' });
const projectEditForm = reactive({ key: '', display_name: '', description: '' });
const environmentForm = reactive({ project: '', environment: '', source: '', target_type: 'managed', existing_cluster_name: '', region: 'ap-south-1' });
const eksClusters = ref<EKSClusterInfo[]>([]);
const loadingEKSClusters = ref(false);
const eksClustersError = ref('');

const normalizeProjectKey = (value: string) => {
  const raw = value.trim().toLowerCase();
  if (!raw) return '';
  let slug = ''; let separator = false;
  for (const character of raw) {
    if (/^[a-z0-9]$/.test(character)) {
      if (separator && slug) slug += '-';
      slug += character; separator = false;
    } else separator = true;
  }
  slug = slug.replace(/^-+|-+$/g, '');
  if (!slug) {
    let hash = 0x811c9dc5;
    for (const byte of new TextEncoder().encode(raw)) hash = Math.imul(hash ^ byte, 0x01000193) >>> 0;
    slug = `project-${hash.toString(16).padStart(8, '0')}`;
  }
  if (/^[0-9]/.test(slug)) slug = `p-${slug}`;
  return slug.slice(0, 48).replace(/-+$/g, '');
};
const normalizedProjectKey = computed(() => normalizeProjectKey(projectForm.key || projectForm.display_name));

const sourceEnvironments = computed(() => store.projects.flatMap((project) => project.environments.map((environment) => ({
  value: `${project.key}/${environment.environment}`,
  label: `${project.display_name} / ${environment.display_name} (${environment.region})`,
}))));
const availableEnvironmentDefinitions = (project: Project) => (store.platform?.environment_definitions || [])
	.filter((definition) => !project.environments.some((environment) => environment.environment === definition.key));
const availableEnvironmentChoices = computed(() => {
	const project = store.projects.find((item) => item.key === environmentForm.project);
	return project ? availableEnvironmentDefinitions(project) : [];
});
const existingEnvironment = (project: Project, key: string) => project.environments.find((item) => item.environment === key);
const projectName = (key: string) => store.projects.find((item) => item.key === key)?.display_name || key;

type CardState = { code: EnvironmentLifecycleStatus | 'uncreated'; label: string; color: string; detail: string; action: string; route: 'create' | 'environment' | 'overview' | 'jobs'; busy: boolean; primary: boolean; progress: number };
const stateDefinitions: Record<EnvironmentLifecycleStatus, Omit<CardState, 'code' | 'detail' | 'progress'>> = {
  ready: { label: '待部署', color: 'orangered', action: '配置与部署', route: 'environment', busy: false, primary: true },
  queued: { label: '等待执行', color: 'orange', action: '查看队列', route: 'jobs', busy: true, primary: false },
  validating: { label: '校验中', color: 'arcoblue', action: '查看任务', route: 'jobs', busy: true, primary: false },
  planning: { label: '规划中', color: 'arcoblue', action: '查看任务', route: 'jobs', busy: true, primary: false },
  deploying: { label: '部署中', color: 'arcoblue', action: '查看部署', route: 'jobs', busy: true, primary: false },
  configuring: { label: '组件配置中', color: 'purple', action: '查看部署', route: 'jobs', busy: true, primary: false },
  updating: { label: '更新中', color: 'arcoblue', action: '查看环境', route: 'overview', busy: true, primary: false },
  running: { label: '运行中', color: 'green', action: '环境概览', route: 'overview', busy: false, primary: false },
  destroying: { label: '销毁中', color: 'orange', action: '查看销毁', route: 'jobs', busy: true, primary: false },
  destroyed: { label: '已销毁', color: 'gray', action: '重新部署', route: 'environment', busy: false, primary: true },
  validation_failed: { label: '配置校验失败', color: 'red', action: '查看问题', route: 'jobs', busy: false, primary: true },
  plan_failed: { label: '资源规划失败', color: 'red', action: '查看问题', route: 'jobs', busy: false, primary: true },
  deployment_failed: { label: '部署失败', color: 'red', action: '查看并重试', route: 'jobs', busy: false, primary: true },
  component_failed: { label: '组件部署失败', color: 'red', action: '查看并重试', route: 'jobs', busy: false, primary: true },
  destroy_failed: { label: '销毁未完成', color: 'red', action: '继续清理', route: 'jobs', busy: false, primary: true },
  canceled: { label: '已取消', color: 'gray', action: '查看任务', route: 'jobs', busy: false, primary: false },
  abnormal: { label: '状态异常', color: 'red', action: '进入排查', route: 'overview', busy: false, primary: true },
};
const environmentState = (project: Project, key: string): CardState => {
  const environment = existingEnvironment(project, key);
  if (!environment) return { code: 'uncreated', label: '尚未创建', color: 'gray', detail: '尚未填写环境配置', action: '创建配置', route: 'create', busy: false, primary: true, progress: 0 };
  const code = environment.lifecycle_status || 'ready';
  const definition = stateDefinitions[code] || stateDefinitions.ready;
  return { code, ...definition, detail: environment.lifecycle_detail || definition.label, progress: environment.latest_job_progress || 0 };
};
const createProject = async () => {
  if (!projectForm.display_name.trim()) {
    Message.warning('请填写项目名称'); return false;
  }
  savingProject.value = true;
  try {
	const created = await store.createProject({ ...projectForm, key: normalizedProjectKey.value });
    Object.assign(projectForm, { key: '', display_name: '', description: '' });
	Message.success(`项目已创建（资源标识：${created.key}），请继续创建环境`);
    return true;
  } catch (error: any) { Message.error(error.message); return false; }
  finally { savingProject.value = false; }
};
const openEditProject = (project: Project) => {
  Object.assign(projectEditForm, { key: project.key, display_name: project.display_name, description: project.description || '' });
  projectEditVisible.value = true;
};
const updateProject = async () => {
  if (!projectEditForm.display_name.trim()) {
    Message.warning('请填写项目名称'); return false;
  }
  savingProjectEdit.value = true;
  try {
    await store.updateProject(projectEditForm.key, {
      display_name: projectEditForm.display_name.trim(),
      description: projectEditForm.description.trim(),
    });
    Message.success('项目信息已更新，内部资源标识及已有云资源不受影响');
    return true;
  } catch (error: any) { Message.error(error.message); return false; }
  finally { savingProjectEdit.value = false; }
};
const showEnvironmentModal = (projectKey: string, environment = '') => {
	const project = store.projects.find((item) => item.key === projectKey);
	const selectedEnvironment = environment || (project ? availableEnvironmentDefinitions(project)[0]?.key : '') || '';
  Object.assign(environmentForm, { project: projectKey, environment: selectedEnvironment, source: '__default__', target_type: 'managed', existing_cluster_name: '', region: 'ap-south-1' });
  eksClusters.value = []; eksClustersError.value = '';
  environmentVisible.value = true;
};
const changeEnvironmentTarget = (value: string | number | boolean) => {
  environmentForm.existing_cluster_name = '';
  eksClustersError.value = '';
  if (String(value) === 'existing_eks') void loadEKSClusters();
};
const changeEnvironmentRegion = () => {
  environmentForm.existing_cluster_name = '';
  eksClusters.value = [];
  eksClustersError.value = '';
  if (environmentForm.target_type === 'existing_eks') void loadEKSClusters();
};
const loadEKSClusters = async () => {
  if (!environmentForm.project || !environmentForm.region || loadingEKSClusters.value) return;
  loadingEKSClusters.value = true; eksClustersError.value = '';
  try {
    const response = await api<EKSClusterResponse>(`/api/projects/${encodeURIComponent(environmentForm.project)}/aws-catalog/eks-clusters?region=${encodeURIComponent(environmentForm.region)}`);
    eksClusters.value = response.clusters;
    if (!response.clusters.length) eksClustersError.value = `${environmentForm.region} 没有查询到 EKS 集群`;
  } catch (error: any) {
    eksClusters.value = [];
    eksClustersError.value = error.message;
  } finally { loadingEKSClusters.value = false; }
};
const createEnvironment = async () => {
	if (!environmentForm.environment) { Message.warning('请选择要创建的环境'); return false; }
  if (!environmentForm.source) { Message.warning('请选择配置模板'); return false; }
  if (!environmentForm.region) { Message.warning('请选择 AWS Region'); return false; }
  if (environmentForm.target_type === 'existing_eks' && !environmentForm.existing_cluster_name.trim()) { Message.warning('请选择或填写已有 EKS 集群名称'); return false; }
  savingEnvironment.value = true;
  try {
    const [sourceProject, sourceEnvironment] = environmentForm.source === '__default__' ? ['', ''] : environmentForm.source.split('/');
    await store.selectProject(environmentForm.project);
    await store.createEnvironment(environmentForm.environment, sourceProject, sourceEnvironment, environmentForm.target_type, environmentForm.existing_cluster_name.trim(), environmentForm.region);
    Message.success(environmentForm.target_type === 'existing_eks' ? '已有 EKS 环境已接入，请检查组件参数后直接部署' : '项目环境已创建，请继续完善部署配置');
    await router.push({ name: 'environment' });
    return true;
  } catch (error: any) { Message.error(error.message); return false; }
  finally { savingEnvironment.value = false; }
};
const enterEnvironment = async (project: Project, environmentKey: string) => {
  const environment = existingEnvironment(project, environmentKey);
  if (!environment) {
    if (project.permission.can_configure) showEnvironmentModal(project.key, environmentKey);
    return;
  }
  const state = environmentState(project, environmentKey);
  await store.selectScope(project.key, environmentKey);
  if (state.route === 'jobs') {
    await router.push({ name: 'jobs', query: environment.latest_job_id ? { job: environment.latest_job_id } : {} });
    return;
  }
  await router.push({ name: state.route });
};
const confirmDeleteProject = (project: Project) => {
	const blockers = projectCleanupEnvironments(project);
	if (blockers.length) {
		Modal.warning({
			title: `项目 ${project.display_name} 暂时不能删除`,
			content: `以下环境仍有运行中或未完成清理的资源：${blockers.map((item) => item.display_name).join('、')}。请逐个进入环境执行销毁；已有 EKS 环境只会卸载平台组件，不会删除共享集群。`,
			okText: '我知道了',
		});
		return;
	}
  Modal.confirm({
    title: `删除项目 ${project.display_name}`,
		content: '平台将再次检查所有环境的 Terraform 状态和部署历史；只有确认资源已清理才会删除项目元数据和环境配置。该项目的 AWS 凭据会完整保留，不会随项目删除。',
    okText: '确认删除', okButtonProps: { status: 'danger' },
    onOk: async () => {
			try {
				await api(`/api/projects/${encodeURIComponent(project.key)}`, { method: 'DELETE', body: JSON.stringify({ confirm: project.key }) });
				Message.success('项目已删除，所属 AWS 凭据已保留');
				await store.loadProjects();
			} catch (error: any) {
				Message.error(error.message);
				return false;
			}
    },
  });
};

const projectCleanupEnvironments = (project: Project) => project.environments.filter((item) => {
	if (item.lifecycle_status === 'destroyed') return false;
	if (['deploy', 'platform', 'access', 'tls', 'storage_expand', 'storage_shrink'].includes(String(item.latest_job_action || ''))) return true;
	if (item.latest_job_action === 'destroy') return item.latest_job_status !== 'succeeded';
	// An existing EKS cluster can be ACTIVE even though this project has never
	// installed anything. Only active jobs (or an actual mutation above) should
	// pre-block deletion in the card; the API remains the authoritative state gate.
	return ['queued', 'validating', 'planning', 'deploying', 'configuring', 'destroying'].includes(String(item.lifecycle_status || ''));
});

let refreshTimer = 0;
const hasBusyEnvironment = computed(() => store.projects.some((project) => project.environments.some((environment) => environmentState(project, environment.environment).busy)));
const scheduleRefresh = () => {
  window.clearTimeout(refreshTimer);
  refreshTimer = window.setTimeout(refreshLifecycle, hasBusyEnvironment.value ? 3000 : 15000);
};
const refreshLifecycle = async () => {
  try {
    if (document.visibilityState === 'visible') await store.refreshProjects();
  } catch {
    // Background refresh is best effort; interactive API calls still surface errors.
  } finally { scheduleRefresh(); }
};
onMounted(scheduleRefresh);
onUnmounted(() => window.clearTimeout(refreshTimer));
</script>

<style scoped>
.project-card :deep(.arco-card-body) { height: 100%; padding: 15px 16px 14px; box-sizing: border-box; display: flex; flex-direction: column; overflow: hidden; }
.project-card-heading { min-width: 0; margin-bottom: 7px; display: flex; align-items: center; justify-content: space-between; gap: 10px; }
.project-title { min-width: 0; flex: 1 1 auto; }
.project-title > span:last-child { min-width: 0; }
.project-title strong, .project-title small { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.project-title strong { font-size: 13px; line-height: 1.35; }
.project-status-row { min-height: 22px; margin: 0 0 6px; display: flex; flex-wrap: nowrap; align-items: center; gap: 5px; overflow: hidden; }
.project-status-row :deep(.arco-tag) { margin: 0; }
.project-status-row :deep(.arco-tag-size-medium) { height: 21px; padding: 0 7px; font-size: 10px; line-height: 21px; }
.project-actions { flex: 0 0 auto; flex-wrap: nowrap; }
.project-actions :deep(.arco-btn-size-mini) { height: 25px; padding: 0 8px; font-size: 10px; }
.project-description { height: 18px; min-height: 18px; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.environment-slots { min-height: 0; flex: 1 1 auto; grid-template-rows: none; grid-auto-rows: 96px; align-content: start; overflow: hidden; }
.environment-slots.dense { grid-template-rows: repeat(2, minmax(0, 1fr)); grid-auto-rows: auto; align-content: stretch; }
.environment-slot { min-height: 0; padding: 4px 7px; gap: 1px; justify-content: flex-start; }
.environment-slot-heading, .environment-slot-heading > div { min-width: 0; }
.environment-slot-heading { flex: 0 0 auto; }
.environment-slot-heading strong { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.environment-slot-meta { min-height: 31px; margin: 2px 0 1px; flex: 0 0 auto; align-content: start; gap: 1px; }
.environment-slot-meta small { overflow: visible; color: var(--color-text-2); font-size: 9px; line-height: 1.25; text-overflow: clip; white-space: normal; }
.environment-slot-meta small:first-child { overflow: hidden; color: var(--color-text-3); font-size: 8.5px; line-height: 1.2; text-overflow: ellipsis; white-space: nowrap; }
.environment-slot-meta small:last-child { display: -webkit-box; overflow: hidden; color: #4e5969; font-weight: 500; -webkit-box-orient: vertical; -webkit-line-clamp: 2; line-clamp: 2; white-space: normal; }
.environment-slots.dense .environment-slot { padding-top: 3px; padding-bottom: 3px; gap: 0; }
.environment-slots.dense .environment-slot-meta { min-height: 23px; margin: 1px 0; }
.environment-slots.dense .environment-slot-meta small:last-child { -webkit-line-clamp: 1; line-clamp: 1; }
.environment-slot-footer { min-width: 0; min-height: 21px; margin-top: auto; display: grid; grid-template-columns: minmax(0, 1fr); align-items: center; gap: 8px; flex: 0 0 auto; }
.environment-slot-footer.busy { grid-template-columns: minmax(0, 1fr) auto; }
.environment-slot-progress { min-width: 0; display: grid; grid-template-columns: minmax(0, 1fr) 29px; align-items: center; gap: 6px; }
.environment-slot-progress :deep(.arco-progress) { min-width: 0; margin: 0; }
.environment-slot-progress > span { color: var(--color-text-3); font-size: 9px; font-variant-numeric: tabular-nums; text-align: right; }
.environment-slot-footer .environment-slot-action { max-width: 100%; justify-self: start; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.environment-slot-footer.busy .environment-slot-action { justify-self: end; color: #0e42d2; border-color: #bedaff; background: #edf3ff; box-shadow: inset 0 1px 0 rgb(255 255 255 / 75%); }
.environment-slots :deep(.arco-empty) { min-height: 0; padding: 18px 8px; grid-column: 1 / -1; }
.environment-slots :deep(.arco-empty-image) { height: 38px; }
.environment-slots :deep(.arco-empty-description) { margin-top: 5px; font-size: 10px; }
.eks-cluster-picker { display: flex; gap: 10px; width: 100%; }
.eks-cluster-picker .arco-select-view { flex: 1; }
.cluster-error { display: block; margin-top: 6px; color: rgb(var(--danger-6)); }

@media (max-width: 560px) {
	.project-grid { grid-auto-rows: 332px; }
	.project-card { height: 332px; }
	.project-card-heading { align-items: flex-start; flex-wrap: wrap; }
	.project-actions { width: 100%; margin-left: 41px; }
}
</style>

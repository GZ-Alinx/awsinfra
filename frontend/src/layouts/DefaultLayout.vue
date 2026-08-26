<template>
  <a-layout class="app-layout">
    <transition name="global-activity">
      <div v-if="navigationPending || pendingOperations > 0" class="global-activity-indicator" role="status" aria-live="polite">
        <span class="global-activity-spinner" />
        <span>{{ navigationPending ? '正在打开页面…' : `正在处理操作${pendingOperations > 1 ? `（${pendingOperations}）` : ''}…` }}</span>
      </div>
    </transition>
    <a-layout-sider class="app-sider" :width="224" :collapsed-width="56" :collapsed="collapsed" collapsible breakpoint="xl" @collapse="collapsed = $event">
      <div class="brand" :class="{ compact: collapsed }">
        <div class="brand-logo">A</div>
        <div v-if="!collapsed"><strong>AWSInfra</strong><span>AWS 部署平台</span></div>
      </div>
      <a-menu aria-label="平台主导航" :selected-keys="[String(route.name)]" :default-open-keys="['project-delivery', 'platform-admin']" :style="{ width: '100%' }">
		<a-menu-item key="projects" @click="go('projects')"><template #icon><icon-apps /></template>项目与环境</a-menu-item>
		<a-sub-menu key="project-delivery"><template #icon><icon-cloud /></template><template #title>项目交付</template>
		  <a-menu-item key="overview" @click="go('overview')"><template #icon><icon-dashboard /></template>环境概览</a-menu-item>
		  <a-menu-item key="observability" @click="go('observability')"><template #icon><icon-eye /></template>应用全景观测</a-menu-item>
		  <a-menu-item key="environment" @click="go('environment')"><template #icon><icon-settings /></template>部署配置</a-menu-item>
		  <a-menu-item key="ingresses" @click="go('ingresses')"><template #icon><icon-link /></template>Ingress 管理</a-menu-item>
		  <a-menu-item key="static-cdn" @click="go('static-cdn')"><template #icon><icon-storage /></template>静态资源 CDN</a-menu-item>
		  <a-menu-item key="resources" @click="go('resources')"><template #icon><icon-storage /></template>资源与访问</a-menu-item>
		  <a-menu-item key="jobs" @click="go('jobs')"><template #icon><icon-list /></template>任务与日志</a-menu-item>
		  <a-menu-item key="cicd" @click="go('cicd')"><template #icon><icon-play-arrow /></template>CICD</a-menu-item>
		</a-sub-menu>
		<a-sub-menu key="platform-admin"><template #icon><icon-settings /></template><template #title>平台管理</template>
		  <a-menu-item key="aws-connection" @click="go('aws-connection')"><template #icon><icon-link /></template>AWS 凭据池</a-menu-item>
		  <a-menu-item v-if="auth.canManageCredentials" key="terraform-state" @click="go('terraform-state')"><template #icon><icon-storage /></template>Terraform 状态中心</a-menu-item>
		  <a-menu-item v-if="auth.canManageCredentials" key="gitlab-servers" @click="go('gitlab-servers')"><template #icon><icon-code-square /></template>GitLab 服务器</a-menu-item>
		  <a-menu-item key="components" @click="go('components')"><template #icon><icon-apps /></template>组件目录</a-menu-item>
		  <a-menu-item v-if="auth.canManageUsers" key="users" @click="go('users')"><template #icon><icon-user /></template>用户与授权</a-menu-item>
		  <a-menu-item v-if="auth.canViewAudit" key="audit-events" @click="go('audit-events')"><template #icon><icon-list /></template>操作审计</a-menu-item>
		</a-sub-menu>
      </a-menu>
      <div v-if="!collapsed" class="sider-version">
        <div><span class="online-dot" /> 平台 {{ store.platform?.version || '—' }}</div>
        <div class="dependency-row"><span :class="['dependency-dot', store.health?.dependencies.mysql === 'up' ? 'up' : 'down']" />MySQL<span :class="['dependency-dot', store.health?.dependencies.redis === 'up' ? 'up' : 'down']" />Redis</div>
      </div>
    </a-layout-sider>

    <a-layout class="main-layout">
      <a-layout-header class="app-header">
        <div class="header-title">
		  <a-breadcrumb><a-breadcrumb-item>{{ store.currentProject?.display_name || '尚未选择项目' }}</a-breadcrumb-item><a-breadcrumb-item>{{ store.currentEnvironment?.display_name || '尚未创建环境' }}</a-breadcrumb-item></a-breadcrumb>
          <strong>{{ pageTitle }}</strong>
        </div>
        <div class="header-actions">
		  <transition name="production-warning">
			<div v-if="isProductionEnvironment" class="production-environment-alert" role="alert" aria-live="polite" title="当前为生产环境，请谨慎操作">
			  <span class="production-environment-pulse" aria-hidden="true" />
			  <icon-exclamation-circle-fill />
			  <span class="production-environment-copy">生产环境 · 操作谨慎</span>
			</div>
		  </transition>
			  <a-select :model-value="store.currentProjectKey" class="project-select" :loading="store.loadingEnvironment" placeholder="选择项目" @change="changeProject">
				<template #prefix><icon-apps /></template>
				<a-option v-for="item in store.projects" :key="item.key" :value="item.key">{{ item.display_name }}</a-option>
          </a-select>
			  <a-select
				:model-value="store.currentEnvironmentKey"
				:class="['environment-select', { 'is-production': isProductionEnvironment }]"
				:loading="store.loadingEnvironment"
				placeholder="选择环境"
				:disabled="!store.currentProject"
				:aria-label="isProductionEnvironment ? '当前选择生产环境，操作要谨慎' : '选择环境'"
				@change="changeEnvironment"
			  >
				<a-option v-for="item in store.currentProject?.environments || []" :key="item.environment" :value="item.environment">{{ item.display_name }} · {{ item.region }}</a-option>
			  </a-select>
          <a-button v-if="route.name === 'overview'" :loading="store.loadingStatus" @click="refresh"><icon-refresh />刷新</a-button>
          <a-button
            class="lifecycle-action"
            :type="lifecycleAction.type"
            :status="lifecycleAction.status"
            :loading="store.loadingEnvironment"
            :disabled="lifecycleAction.disabled"
            @click="openLifecycleAction"
          >
            <icon-settings v-if="lifecycleAction.route === 'environment'" />
            <icon-apps v-else />
            {{ lifecycleAction.label }}
          </a-button>
          <a-dropdown trigger="click">
            <a-button class="user-trigger" aria-label="打开个人菜单">
			  <a-avatar :size="30" class="user-avatar">{{ (auth.session?.display_name || auth.session?.username || 'U').slice(0, 1).toUpperCase() }}</a-avatar>
              <span class="user-trigger-copy"><strong>{{ auth.session?.display_name || auth.session?.username }}</strong><small>{{ auth.session?.is_admin ? '超级管理员' : '平台用户' }}</small></span>
              <icon-down class="user-trigger-arrow" />
            </a-button>
            <template #content>
			  <a-doption disabled><div class="account-summary"><strong>{{ auth.session?.display_name || auth.session?.username }}</strong><small>{{ auth.session?.username }} · {{ auth.session?.is_admin ? '超级管理员' : '平台用户' }}</small></div></a-doption>
              <a-divider :margin="4" />
			  <a-doption @click="openProfile"><icon-user /> 个人资料</a-doption>
			  <a-doption @click="passwordVisible = true"><icon-settings /> 修改密码</a-doption>
			  <a-divider :margin="4" />
              <a-doption @click="logout"><icon-export /> 退出登录</a-doption>
            </template>
          </a-dropdown>
        </div>
      </a-layout-header>
      <a-layout-content class="app-content">
        <a-spin :loading="initializing" tip="正在加载环境…" class="content-spin">
          <router-view />
        </a-spin>
      </a-layout-content>
    </a-layout>
    <a-back-top :visible-height="360" />
  </a-layout>

  <a-modal v-model:visible="profileVisible" title="个人资料与权限" width="680px" :ok-loading="profileSaving" ok-text="保存资料" @before-ok="saveProfile">
    <a-spin :loading="profileLoading" style="width:100%">
      <div class="profile-identity">
        <a-avatar :size="48">{{ (profileForm.display_name || auth.session?.username || 'U').slice(0, 1).toUpperCase() }}</a-avatar>
        <div><strong>{{ profileForm.display_name || auth.session?.username }}</strong><span>{{ auth.session?.username }}</span></div>
        <a-tag :color="auth.session?.is_admin ? 'orangered' : 'arcoblue'">{{ auth.session?.is_admin ? '超级管理员' : '平台用户' }}</a-tag>
      </div>
      <a-form :model="profileForm" layout="vertical">
        <a-grid :cols="2" :col-gap="16">
          <a-grid-item><a-form-item label="用户名"><a-input :model-value="auth.session?.username" disabled /></a-form-item></a-grid-item>
          <a-grid-item><a-form-item label="用户昵称" required><a-input v-model="profileForm.display_name" :max-length="128" show-word-limit /></a-form-item></a-grid-item>
        </a-grid>
      </a-form>
      <a-divider />
      <div class="profile-section"><strong>平台管理权限</strong><a-space wrap><a-tag v-for="label in platformPermissionLabels" :key="label" color="purple">{{ label }}</a-tag><span v-if="!platformPermissionLabels.length" class="muted-text">无平台管理权限</span></a-space></div>
      <div class="profile-section"><strong>可访问项目</strong><a-space wrap><a-tag v-for="permission in profileUser?.permissions || []" :key="permission.project_key" color="arcoblue">{{ projectPermissionLabel(permission) }}</a-tag><span v-if="!(profileUser?.permissions || []).length" class="muted-text">尚未分配项目</span></a-space></div>
    </a-spin>
  </a-modal>

  <a-modal v-model:visible="passwordVisible" title="修改登录密码" :ok-loading="passwordSaving" ok-text="确认修改" @before-ok="savePassword" @cancel="clearPasswordForm">
    <a-alert type="warning" show-icon>密码修改成功后会退出当前会话，其他已登录会话也会失效。</a-alert>
    <a-form :model="passwordForm" layout="vertical" style="margin-top:16px">
      <a-form-item label="当前密码" required><a-input-password v-model="passwordForm.current_password" autocomplete="current-password" /></a-form-item>
      <a-form-item label="新密码" required extra="至少 12 个字符，且不能与当前密码相同。"><a-input-password v-model="passwordForm.new_password" autocomplete="new-password" /></a-form-item>
      <a-form-item label="确认新密码" required><a-input-password v-model="passwordForm.confirm_password" autocomplete="new-password" /></a-form-item>
    </a-form>
  </a-modal>

</template>

<script setup lang="ts">
import { computed, onMounted, onUnmounted, reactive, ref } from 'vue';
import { useRoute, useRouter } from 'vue-router';
import { Message } from '@arco-design/web-vue';
import {
  IconApps, IconCloud, IconDashboard, IconExport, IconLink, IconList,
	IconCodeSquare, IconDown, IconExclamationCircleFill, IconEye, IconPlayArrow, IconRefresh, IconSettings, IconStorage, IconUser,
} from '@arco-design/web-vue/es/icon';
import { useAuthStore } from '@/stores/auth';
import { usePlatformStore } from '@/stores/platform';
import { api } from '@/services/api';
import type { ProjectPermission, UserInfo } from '@/types';
import { navigationPending } from '@/router';

const route = useRoute();
const router = useRouter();
const auth = useAuthStore();
const store = usePlatformStore();
const collapsed = ref(false);
const initializing = ref(false);
const profileVisible = ref(false);
const profileLoading = ref(false);
const profileSaving = ref(false);
const passwordVisible = ref(false);
const passwordSaving = ref(false);
const pendingOperations = ref(0);
const profileUser = ref<UserInfo | null>(null);
const profileForm = reactive({ display_name: '' });
const passwordForm = reactive({ current_password: '', new_password: '', confirm_password: '' });
let jobPollTimer = 0;
let jobPollBusy = false;
const requestStarted = () => { pendingOperations.value += 1; };
const requestEnded = () => { pendingOperations.value = Math.max(0, pendingOperations.value - 1); };
const pageTitle = computed(() => ({ projects: '项目与环境', 'aws-connection': 'AWS 凭据池', 'terraform-state': 'Terraform 状态中心', 'gitlab-servers': 'GitLab 服务器', overview: '环境概览', observability: '应用全景观测', environment: '部署配置', ingresses: 'Ingress 管理', 'static-cdn': '静态资源 CDN', resources: '环境资源与访问', jobs: '任务与日志', cicd: 'CICD', components: '可用组件目录', users: '用户与授权', 'audit-events': '操作审计' }[String(route.name)] || 'AWS 部署平台'));
const isProductionEnvironment = computed(() => store.currentEnvironmentKey.trim().toLowerCase() === 'prod');
const deploymentStageTab = computed(() => {
  const targetType = String(store.config?.deployment_target?.type || store.status?.cluster?.target_type || 'managed');
  if (targetType === 'existing_eks') return 'components';
  if (store.status?.cluster?.reachable) return 'components';
  const environment = store.currentEnvironment;
  const latestAction = environment?.latest_job_action || [...store.jobs].sort((left, right) => new Date(right.created_at).getTime() - new Date(left.created_at).getTime())[0]?.action;
  if (latestAction === 'platform' || latestAction === 'access' || latestAction === 'tls' || latestAction === 'storage_expand' || latestAction === 'storage_shrink') return 'components';
  if (['running', 'updating', 'configuring', 'component_failed', 'abnormal'].includes(environment?.lifecycle_status || '')) return 'components';
  return 'basic';
});
const lifecycleAction = computed(() => {
  const project = store.currentProject;
  const environment = store.currentEnvironment;
  if (!project) return { label: '选择项目', route: 'projects', type: 'outline', status: 'normal', disabled: false } as const;
  if (!environment) return { label: '创建环境', route: 'projects', type: 'primary', status: 'normal', disabled: !store.canConfigure, job: '' } as const;
	  const stageTwo = deploymentStageTab.value === 'components';
	  const deployed = stageTwo ? environment.phase_two_deployed : environment.phase_one_deployed;
	  const label = store.canDeploy ? `${deployed ? '更新部署' : '开始部署'}【阶段${stageTwo ? '二' : '一'}】` : '查看部署配置';
	  return { label, route: 'environment', type: 'primary', status: 'normal', disabled: store.loadingEnvironment } as const;
});
const platformPermissionLabels = computed(() => {
  const permission = profileUser.value?.platform_permissions || auth.session?.platform_permissions;
  if (auth.session?.is_admin) return ['项目管理', '用户与授权', '凭据与集成管理', '组件目录管理'];
  if (!permission) return [];
  return [
    permission.can_manage_projects ? '项目管理' : '',
    permission.can_manage_users ? '用户与授权' : '',
    permission.can_manage_credentials ? '凭据与集成管理' : '',
    permission.can_manage_components ? '组件目录管理' : '',
    permission.can_view_audit ? '查看操作审计' : '',
  ].filter(Boolean);
});

const pollActiveJob = async () => {
  // JobsView already streams the selected task every second. Running the
  // global header poll there would duplicate task-list requests.
  if (route.name === 'jobs' || jobPollBusy || !store.currentProjectKey || !store.currentEnvironmentKey) return;
  const hadActiveJob = store.jobs.some((job) => ['queued', 'running'].includes(job.status));
  if (!hadActiveJob) return;
  jobPollBusy = true;
  try {
    await store.loadJobs();
    if (!store.jobs.some((job) => ['queued', 'running'].includes(job.status))) {
      await Promise.allSettled([store.refreshProjects(), store.loadStatus()]);
    }
  } catch {
    // The task page remains the authoritative place for request errors. Header
    // polling is intentionally quiet to avoid repeated global notifications.
  } finally { jobPollBusy = false; }
};

onMounted(async () => {
  window.addEventListener('ops:request-start', requestStarted);
  window.addEventListener('ops:request-end', requestEnded);
  initializing.value = true;
  try {
    await store.initialize();
  } catch (error: any) { Message.error(error.message); }
  finally {
    initializing.value = false;
    jobPollTimer = window.setInterval(pollActiveJob, 2500);
  }
});
onUnmounted(() => {
  window.clearInterval(jobPollTimer);
  window.removeEventListener('ops:request-start', requestStarted);
  window.removeEventListener('ops:request-end', requestEnded);
});

const go = async (key: string) => {
  if (route.name === key) { window.scrollTo({ top: 0, behavior: 'smooth' }); return; }
  try { await router.push({ name: key }); }
  catch (error: any) {
    // Chunk/version errors are recovered centrally by router.onError.
    if (!/dynamically imported module|module script|Loading chunk|ChunkLoadError/i.test(String(error?.message || error))) Message.error('页面打开失败，请重试');
  }
};
const changeProject = async (key: unknown) => {
	try { await store.selectProject(String(key)); }
	catch (error: any) { Message.error(error.message); }
};
const changeEnvironment = async (name: unknown) => {
	try { await store.selectEnvironment(String(name)); }
  catch (error: any) { Message.error(error.message); }
};
const refresh = async () => {
  try { await store.loadStatus(true); Message.success('状态已刷新'); }
  catch (error: any) { Message.error(error.message); }
};
const openLifecycleAction = async () => {
  const action = lifecycleAction.value;
  if (action.disabled) return;
  await router.push({ name: action.route, query: action.route === 'environment' ? { tab: deploymentStageTab.value } : {} });
};
const openProfile = async () => {
  profileVisible.value = true;
  profileLoading.value = true;
  try {
    profileUser.value = await api<UserInfo>('/api/me');
    profileForm.display_name = profileUser.value.display_name;
  } catch (error: any) { Message.error(error.message); }
  finally { profileLoading.value = false; }
};
const projectPermissionLabel = (permission: ProjectPermission) => {
  const name = store.projects.find((item) => item.key === permission.project_key)?.display_name || permission.project_key;
  const actions = [permission.can_deploy ? '部署' : '', permission.can_configure ? '配置' : '', permission.can_view_secrets ? '凭据' : ''].filter(Boolean);
  return `${name} · ${actions.length ? actions.join('/') : '只读'}`;
};
const saveProfile = async () => {
  const displayName = profileForm.display_name.trim();
  if (!displayName) { Message.warning('用户昵称不能为空'); return false; }
  profileSaving.value = true;
  try {
    profileUser.value = await auth.updateProfile(displayName);
    Message.success('个人资料已更新');
    return true;
  } catch (error: any) { Message.error(error.message); return false; }
  finally { profileSaving.value = false; }
};
const clearPasswordForm = () => Object.assign(passwordForm, { current_password: '', new_password: '', confirm_password: '' });
const savePassword = async () => {
  if (passwordForm.new_password.length < 12) { Message.warning('新密码至少 12 个字符'); return false; }
  if (passwordForm.new_password !== passwordForm.confirm_password) { Message.warning('两次输入的新密码不一致'); return false; }
  if (passwordForm.new_password === passwordForm.current_password) { Message.warning('新密码不能与当前密码相同'); return false; }
  passwordSaving.value = true;
  try {
    await auth.changePassword(passwordForm.current_password, passwordForm.new_password);
    clearPasswordForm(); store.$reset();
    Message.success('密码已修改，请重新登录');
    await router.replace({ name: 'login' });
    return true;
  } catch (error: any) { Message.error(error.message); return false; }
  finally { passwordSaving.value = false; }
};
const logout = async () => {
  await auth.logout();
	store.$reset();
  await router.replace({ name: 'login' });
};
</script>

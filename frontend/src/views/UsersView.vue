<template>
  <div>
    <div class="page-header"><div><h2>用户与授权</h2><p>权限分为平台管理能力和项目成员权限；是否能访问项目始终以项目授权为准。</p></div><a-button type="primary" @click="createVisible = true"><icon-user-add />新增用户</a-button></div>
    <a-alert type="info" show-icon class="full-card">系统会自动加载你有权管理的项目。平台管理员也不会自动看到全部项目，必须在下方明确开启“项目访问”。</a-alert>
    <a-card>
      <a-table :data="users" :loading="loading" row-key="username" :pagination="{ pageSize: 12 }">
        <template #columns>
          <a-table-column title="用户" :width="210"><template #cell="{ record }"><div class="user-cell"><a-avatar>{{ record.display_name.slice(0, 1) }}</a-avatar><span><strong>{{ record.display_name }}</strong><small>{{ record.username }}</small></span></div></template></a-table-column>
          <a-table-column title="身份" :width="130"><template #cell="{ record }"><a-tag :color="record.is_admin ? 'orangered' : hasPlatformPermission(record) ? 'purple' : 'arcoblue'">{{ record.is_admin ? '超级管理员' : hasPlatformPermission(record) ? '平台管理员' : '项目成员' }}</a-tag></template></a-table-column>
          <a-table-column title="平台管理" :width="260"><template #cell="{ record }"><a-space wrap><a-tag v-for="label in platformPermissionText(record)" :key="label" color="purple">{{ label }}</a-tag><span v-if="!platformPermissionText(record).length" class="muted-text">无</span></a-space></template></a-table-column>
          <a-table-column title="可访问项目"><template #cell="{ record }"><a-space wrap><a-tag v-for="permission in record.permissions || []" :key="permission.project_key" color="gray">{{ projectName(permission.project_key) }}：{{ permissionText(permission) }}</a-tag><span v-if="!(record.permissions || []).length" class="muted-text">未授权项目</span></a-space></template></a-table-column>
          <a-table-column title="状态" :width="90"><template #cell="{ record }"><a-badge :status="record.active ? 'success' : 'normal'" :text="record.active ? '启用' : '停用'" /></template></a-table-column>
          <a-table-column title="操作" :width="160" fixed="right"><template #cell="{ record }"><a-space><a-button size="mini" @click="editUser(record)">编辑授权</a-button><a-popconfirm v-if="record.username !== auth.session?.username" content="确认删除该用户？" @ok="deleteUser(record)"><a-button size="mini" status="danger">删除</a-button></a-popconfirm></a-space></template></a-table-column>
        </template>
      </a-table>
    </a-card>
  </div>

  <a-modal v-model:visible="createVisible" title="新增用户" width="640px" :ok-loading="saving" @before-ok="createUser">
    <a-form :model="createForm" layout="vertical">
      <a-grid :cols="2" :col-gap="16">
        <a-grid-item><a-form-item label="用户名" required extra="小写字母开头。"><a-input v-model="createForm.username" /></a-form-item></a-grid-item>
        <a-grid-item><a-form-item label="显示名称" required><a-input v-model="createForm.display_name" /></a-form-item></a-grid-item>
        <a-grid-item :span="2"><a-form-item label="初始密码" required extra="至少 12 个字符，只保存 Argon2id 哈希。"><a-input-password v-model="createForm.password" /></a-form-item></a-grid-item>
        <a-grid-item v-if="auth.session?.is_admin"><a-form-item label="超级管理员"><a-switch v-model="createForm.is_admin" /></a-form-item></a-grid-item>
      </a-grid>
      <a-divider>平台管理权限</a-divider>
      <div class="platform-permission-grid">
        <permission-switch v-model="createForm.platform_permissions.can_manage_projects" label="项目管理" description="创建、修改和删除有权访问的项目" :disabled="createForm.is_admin || !auth.canManageProjects" />
        <permission-switch v-model="createForm.platform_permissions.can_manage_users" label="用户与授权" description="管理用户并分配本人持有的项目权限" :disabled="createForm.is_admin || !auth.canManageUsers" />
        <permission-switch v-model="createForm.platform_permissions.can_manage_credentials" label="凭据与集成管理" description="维护 AWS 凭据、GitLab 服务器与平台安全集成" :disabled="createForm.is_admin || !auth.canManageCredentials" />
        <permission-switch v-model="createForm.platform_permissions.can_manage_components" label="组件目录管理" description="维护平台 Helm 组件目录" :disabled="createForm.is_admin || !auth.canManageComponents" />
        <permission-switch v-model="createForm.platform_permissions.can_view_audit" label="查看操作审计" description="查看所有平台用户的操作、来源地址和执行结果" :disabled="createForm.is_admin || !auth.canViewAudit" />
      </div>
    </a-form>
  </a-modal>

  <a-drawer v-model:visible="editVisible" :width="860" title="编辑用户与授权" unmount-on-close>
    <template #footer><a-space><a-button @click="editVisible = false">取消</a-button><a-button type="primary" :loading="saving" @click="saveUser">保存用户与权限</a-button></a-space></template>
    <a-form v-if="editForm" :model="editForm" layout="vertical">
      <a-grid :cols="2" :col-gap="16">
        <a-grid-item><a-form-item label="用户名"><a-input :model-value="editForm.username" disabled /></a-form-item></a-grid-item>
        <a-grid-item><a-form-item label="显示名称"><a-input v-model="editForm.display_name" /></a-form-item></a-grid-item>
        <a-grid-item><a-form-item label="启用账号"><a-switch v-model="editForm.active" :disabled="isEditingSelf" /></a-form-item></a-grid-item>
        <a-grid-item v-if="auth.session?.is_admin"><a-form-item label="超级管理员"><a-switch v-model="editForm.is_admin" :disabled="isEditingSelf" /></a-form-item></a-grid-item>
        <a-grid-item :span="2"><a-form-item label="重置密码" :extra="isEditingSelf ? '请使用右上角个人菜单修改自己的密码。' : '不修改请留空。'"><a-input-password v-model="editForm.password" :disabled="isEditingSelf" placeholder="至少 12 个字符" /></a-form-item></a-grid-item>
      </a-grid>
    </a-form>

    <a-divider />
    <div class="permission-heading"><div><h3>平台管理权限</h3><p>平台能力只决定能否进入管理功能，不会扩大项目可见范围。</p></div><a-tag v-if="editForm?.is_admin" color="orangered">超级管理员拥有全部平台能力</a-tag></div>
    <div v-if="editForm" class="platform-permission-grid">
      <permission-switch v-model="editForm.platform_permissions.can_manage_projects" label="项目管理" description="创建、修改和删除项目" :disabled="editForm.is_admin || isEditingSelf || !auth.canManageProjects" />
      <permission-switch v-model="editForm.platform_permissions.can_manage_users" label="用户与授权" description="管理用户和项目成员" :disabled="editForm.is_admin || isEditingSelf || !auth.canManageUsers" />
      <permission-switch v-model="editForm.platform_permissions.can_manage_credentials" label="凭据与集成管理" description="维护 AWS 凭据、GitLab 服务器与平台安全集成" :disabled="editForm.is_admin || isEditingSelf || !auth.canManageCredentials" />
      <permission-switch v-model="editForm.platform_permissions.can_manage_components" label="组件目录管理" description="维护扩展 Helm 组件" :disabled="editForm.is_admin || isEditingSelf || !auth.canManageComponents" />
      <permission-switch v-model="editForm.platform_permissions.can_view_audit" label="查看操作审计" description="查看所有平台用户的操作、来源地址和执行结果" :disabled="editForm.is_admin || isEditingSelf || !auth.canViewAudit" />
    </div>

    <a-divider />
    <div class="permission-heading"><div><h3>项目权限</h3><p>系统已自动识别你可管理的项目；先开启项目访问，再分配操作能力。</p></div><a-tag color="arcoblue">{{ permissionRows.filter((item) => item.can_view).length }} / {{ permissionRows.length }} 个项目</a-tag></div>
    <a-table :data="permissionRows" :pagination="false" size="small" row-key="project_key">
      <template #columns>
        <a-table-column title="项目" data-index="display_name" />
        <a-table-column title="项目访问" :width="120"><template #cell="{ record }"><a-switch v-model="record.can_view" @change="toggleProjectAccess(record, Boolean($event))" /></template></a-table-column>
        <a-table-column title="部署" :width="110"><template #cell="{ record }"><a-switch v-model="record.can_deploy" :disabled="!record.can_view || !callerProjectPermission(record.project_key)?.can_deploy" /></template></a-table-column>
        <a-table-column title="配置修改" :width="120"><template #cell="{ record }"><a-switch v-model="record.can_configure" :disabled="!record.can_view || !callerProjectPermission(record.project_key)?.can_configure" /></template></a-table-column>
        <a-table-column title="查看凭据" :width="120"><template #cell="{ record }"><a-switch v-model="record.can_view_secrets" :disabled="!record.can_view || !callerProjectPermission(record.project_key)?.can_view_secrets" /></template></a-table-column>
      </template>
    </a-table>
  </a-drawer>
</template>

<script setup lang="ts">
import { computed, defineComponent, h, onMounted, reactive, ref } from 'vue';
import { Message, Switch } from '@arco-design/web-vue';
import { IconUserAdd } from '@arco-design/web-vue/es/icon';
import { api } from '@/services/api';
import { useAuthStore } from '@/stores/auth';
import { usePlatformStore } from '@/stores/platform';
import type { PlatformPermission, ProjectPermission, UserInfo } from '@/types';

const emptyPlatformPermission = (): PlatformPermission => ({ can_manage_projects: false, can_manage_users: false, can_manage_credentials: false, can_manage_components: false, can_view_audit: false });
const PermissionSwitch = defineComponent({
  props: { modelValue: Boolean, label: String, description: String, disabled: Boolean }, emits: ['update:modelValue'],
  setup(props, { emit }) { return () => h('div', { class: 'permission-switch-card' }, [h('div', [h('strong', props.label), h('span', props.description)]), h(Switch, { modelValue: props.modelValue, disabled: props.disabled, 'onUpdate:modelValue': (value: string | number | boolean) => emit('update:modelValue', Boolean(value)) })]); },
});

type PermissionRow = ProjectPermission & { display_name: string };
const auth = useAuthStore();
const store = usePlatformStore();
const users = ref<UserInfo[]>([]);
const loading = ref(false);
const saving = ref(false);
const createVisible = ref(false);
const editVisible = ref(false);
const editForm = ref<(UserInfo & { password: string }) | null>(null);
const permissionRows = ref<PermissionRow[]>([]);
const createForm = reactive({ username: '', display_name: '', password: '', is_admin: false, active: true, platform_permissions: emptyPlatformPermission() });
const isEditingSelf = computed(() => editForm.value?.username === auth.session?.username);

onMounted(loadUsers);
async function loadUsers() {
  loading.value = true;
  try { users.value = await api<UserInfo[]>('/api/users'); }
  catch (error: any) { Message.error(error.message); }
  finally { loading.value = false; }
}
const projectName = (key: string) => store.projects.find((item) => item.key === key)?.display_name || key;
const permissionText = (permission: ProjectPermission) => [permission.can_deploy ? '部署' : '', permission.can_configure ? '改配置' : '', permission.can_view_secrets ? '看凭据' : ''].filter(Boolean).join(' + ') || '只读';
const hasPlatformPermission = (user: UserInfo) => user.is_admin || Object.values(user.platform_permissions || {}).some(Boolean);
const platformPermissionText = (user: UserInfo) => user.is_admin ? ['全部平台能力'] : [
  user.platform_permissions?.can_manage_projects ? '项目' : '', user.platform_permissions?.can_manage_users ? '用户' : '',
  user.platform_permissions?.can_manage_credentials ? '凭据' : '', user.platform_permissions?.can_manage_components ? '组件' : '',
  user.platform_permissions?.can_view_audit ? '审计' : '',
].filter(Boolean);
const callerProjectPermission = (key: string) => store.projects.find((item) => item.key === key)?.permission;
const toggleProjectAccess = (row: PermissionRow, enabled: boolean) => {
  if (!enabled) Object.assign(row, { can_deploy: false, can_configure: false, can_view_secrets: false });
};
const createUser = async () => {
  if (!/^[a-z][a-z0-9._-]{2,63}$/.test(createForm.username) || !createForm.display_name.trim() || createForm.password.length < 12) {
    Message.warning('请填写合法用户信息，密码至少 12 位'); return false;
  }
  saving.value = true;
  try {
    await api('/api/users', { method: 'POST', body: JSON.stringify(createForm) });
    Object.assign(createForm, { username: '', display_name: '', password: '', is_admin: false, active: true, platform_permissions: emptyPlatformPermission() });
    await loadUsers(); Message.success('用户已创建，请继续分配项目权限'); return true;
  } catch (error: any) { Message.error(error.message); return false; }
  finally { saving.value = false; }
};
const editUser = (user: UserInfo) => {
  const copy = JSON.parse(JSON.stringify(user));
  copy.platform_permissions ||= emptyPlatformPermission();
  editForm.value = { ...copy, password: '' };
  const permissions = new Map((user.permissions || []).map((item) => [item.project_key, item]));
  permissionRows.value = store.projects.map((project) => ({
    project_key: project.key, display_name: project.display_name,
    can_view: permissions.get(project.key)?.can_view || false,
    can_deploy: permissions.get(project.key)?.can_deploy || false,
    can_configure: permissions.get(project.key)?.can_configure || false,
    can_view_secrets: permissions.get(project.key)?.can_view_secrets || false,
  }));
  editVisible.value = true;
};
const saveUser = async () => {
  if (!editForm.value) return;
  if (editForm.value.password && editForm.value.password.length < 12) { Message.warning('新密码至少 12 位'); return; }
  saving.value = true;
  try {
    const user = editForm.value;
    await api(`/api/users/${encodeURIComponent(user.username)}`, {
      method: 'PUT', body: JSON.stringify({ display_name: user.display_name, password: user.password, is_admin: user.is_admin, active: user.active, platform_permissions: user.platform_permissions }),
    });
    await Promise.all(permissionRows.value.map((permission) => api(`/api/projects/${encodeURIComponent(permission.project_key)}/members/${encodeURIComponent(user.username)}`, {
      method: 'PUT', body: JSON.stringify({ can_view: permission.can_view, can_deploy: permission.can_deploy, can_configure: permission.can_configure, can_view_secrets: permission.can_view_secrets }),
    })));
    await Promise.all([loadUsers(), store.loadProjects(store.currentProjectKey, store.currentEnvironmentKey)]);
    Message.success('用户、平台能力与项目权限已保存'); editVisible.value = false;
  } catch (error: any) { Message.error(error.message); }
  finally { saving.value = false; }
};
const deleteUser = async (user: UserInfo) => {
  try { await api(`/api/users/${encodeURIComponent(user.username)}`, { method: 'DELETE' }); await loadUsers(); Message.success('用户已删除'); }
  catch (error: any) { Message.error(error.message); }
};
</script>

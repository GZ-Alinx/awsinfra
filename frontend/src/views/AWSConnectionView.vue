<template>
  <div>
    <div class="page-header">
      <div><h2>AWS 凭据池</h2><p>平台统一保管 AK/SK；每条凭据归属一个项目，项目从自己的凭据中选择部署权限入口。</p></div>
      <a-space><a-button :loading="loading" @click="load"><icon-refresh />刷新</a-button><a-button v-if="auth.canManageCredentials" type="primary" @click="openEditor()"><icon-plus />新增凭据</a-button></a-space>
    </div>

    <a-alert type="warning" show-icon class="full-card">AK/SK 会先通过 AWS STS 验证，再以 AES-256-GCM 加密保存到 MySQL。每条凭据只能由所属项目选择和使用；未绑定项目会被拒绝执行，绝不回退到其他项目或平台进程凭据。Secret Access Key 与 Session Token 永不回显，也不会进入环境 YAML、接口响应或部署日志。</a-alert>

    <a-card class="full-card">
      <template #title><span class="card-title">平台凭据目录</span></template>
      <a-table :data="credentials" :pagination="{ pageSize: 10 }" row-key="key">
        <template #columns>
          <a-table-column title="凭据" :width="210"><template #cell="{ record }"><div class="credential-name"><strong>{{ record.display_name }}</strong><code>{{ record.key }}</code></div></template></a-table-column>
          <a-table-column title="所属项目" :width="200"><template #cell="{ record }"><a-tag :color="record.project_archived ? 'gray' : 'arcoblue'">{{ projectName(record.project_key) }}{{ record.project_archived ? '（已删除）' : '' }}</a-tag></template></a-table-column>
          <a-table-column title="Access Key" :width="190" data-index="masked_access_key" />
          <a-table-column title="AWS Account" :width="140" data-index="account_id" />
          <a-table-column title="验证身份"><template #cell="{ record }"><a-tooltip :content="record.principal_arn"><span class="ellipsis-text">{{ record.principal_arn || '—' }}</span></a-tooltip></template></a-table-column>
          <a-table-column title="状态" :width="170"><template #cell="{ record }"><a-tag :color="record.project_archived ? 'orange' : record.selected ? 'green' : 'gray'">{{ record.project_archived ? '已保留（项目已删除）' : record.selected ? '项目当前入口' : '备用凭据' }}</a-tag></template></a-table-column>
          <a-table-column title="最近验证" :width="180"><template #cell="{ record }">{{ formatTime(record.verified_at) }}</template></a-table-column>
          <a-table-column title="操作" :width="250" fixed="right"><template #cell="{ record }"><a-space>
            <a-button size="mini" type="primary" :disabled="record.project_archived || record.selected || !canConfigureProject(record.project_key)" @click="selectCredential(record)">设为权限入口</a-button>
            <a-button v-if="auth.canManageCredentials && !record.project_archived && canConfigureProject(record.project_key)" size="mini" @click="openEditor(record)">更新</a-button>
            <a-button v-if="auth.canManageCredentials && (record.project_archived || canConfigureProject(record.project_key))" size="mini" status="danger" @click="openDelete(record)">删除</a-button>
          </a-space></template></a-table-column>
        </template>
        <template #empty><a-empty description="尚未创建 AWS 凭据"><a-button v-if="auth.canManageCredentials" type="primary" @click="openEditor()">新增第一条凭据</a-button></a-empty></template>
      </a-table>
    </a-card>

    <a-card>
      <template #title><span class="card-title">权限入口规则</span></template>
      <a-steps :current="3" type="dot">
        <a-step title="平台录入" description="管理员创建凭据并指定所属项目" />
        <a-step title="项目选择" description="有配置权限的成员选择该项目当前入口" />
		<a-step title="严格隔离" description="仅该项目的查询、部署、状态采集与销毁使用所选身份" />
      </a-steps>
    </a-card>
  </div>

  <a-modal v-model:visible="editorVisible" :title="updating ? '更新 AWS 凭据' : '新增 AWS 凭据'" width="760px" :ok-loading="saving" ok-text="验证并保存" @before-ok="save" @cancel="clearEditor">
    <a-form :model="editor" layout="vertical">
      <a-grid :cols="2" :col-gap="16">
        <a-grid-item><a-form-item label="凭据标识（可选）" extra="用于平台内部引用；支持中文，留空时根据显示名称自动生成。"><a-input v-model="editor.key" :disabled="updating" placeholder="可填写 KBP小游戏" /></a-form-item></a-grid-item>
        <a-grid-item><a-form-item label="显示名称" required><a-input v-model="editor.display_name" placeholder="生产部署身份" /></a-form-item></a-grid-item>
        <a-grid-item><a-form-item label="所属项目" required><a-select v-model="editor.project_key" :disabled="updating" allow-search><a-option v-for="project in store.projects" :key="project.key" :value="project.key">{{ project.display_name }} · {{ project.key }}</a-option></a-select></a-form-item></a-grid-item>
        <a-grid-item><a-form-item label="验证 Region"><a-select v-model="editor.region" allow-search><a-option v-for="region in store.platform?.aws_regions || []" :key="region.code" :value="region.code">{{ region.code }} · {{ region.name }}</a-option></a-select></a-form-item></a-grid-item>
        <a-grid-item :span="2"><a-form-item label="Access Key ID" required><a-input v-model="editor.access_key_id" autocomplete="off" placeholder="AKIA... / ASIA..." /></a-form-item></a-grid-item>
        <a-grid-item :span="2"><a-form-item label="Secret Access Key" required><a-input-password v-model="editor.secret_access_key" autocomplete="new-password" /></a-form-item></a-grid-item>
        <a-grid-item :span="2"><a-form-item label="Session Token（仅临时凭据）"><a-textarea v-model="editor.session_token" :auto-size="{ minRows: 2, maxRows: 4 }" /></a-form-item></a-grid-item>
		<a-grid-item v-if="!updating && normalizedCredentialKey" :span="2"><a-alert type="info" show-icon>内部凭据标识将使用：<code>{{ normalizedCredentialKey }}</code></a-alert></a-grid-item>
		<a-grid-item :span="2"><a-form-item label="当前平台登录密码（可选）" extra="填写时会执行二次身份校验；留空可直接验证并保存 AWS 凭据。"><a-input-password v-model="editor.password" autocomplete="current-password" placeholder="可留空" /></a-form-item></a-grid-item>
      </a-grid>
    </a-form>
  </a-modal>

  <a-modal v-model:visible="deleteVisible" title="删除 AWS 凭据" :ok-loading="deleting" ok-text="确认删除" @before-ok="removeCredential">
	<a-alert type="warning" show-icon>{{ deleteTarget?.project_archived ? '该凭据是删除项目时保留的 AWS 凭据。只有在这里手动确认，才会真正删除。' : `将删除“${deleteTarget?.display_name}”。如果它是项目当前权限入口，该项目会立即变为未绑定状态，所有 AWS 查询、部署和销毁都将停止；平台不会回退到任何默认凭据。` }}</a-alert>
	<a-form :model="deleteForm" layout="vertical" style="margin-top:16px"><a-form-item label="当前平台登录密码（可选）" extra="填写时执行二次身份校验，留空可直接删除。"><a-input-password v-model="deleteForm.password" autocomplete="current-password" placeholder="可留空" /></a-form-item></a-form>
  </a-modal>
</template>

<script setup lang="ts">
import { computed, reactive, ref } from 'vue';
import { Message, Modal } from '@arco-design/web-vue';
import { IconPlus, IconRefresh } from '@arco-design/web-vue/es/icon';
import { useAuthStore } from '@/stores/auth';
import { usePlatformStore } from '@/stores/platform';
import type { AWSCredentialInfo } from '@/types';

const auth = useAuthStore();
const store = usePlatformStore();
const credentials = computed(() => store.awsCredentials);
const loading = ref(false); const saving = ref(false); const deleting = ref(false);
const editorVisible = ref(false); const deleteVisible = ref(false); const updating = ref(false);
const deleteTarget = ref<AWSCredentialInfo | null>(null);
const editor = reactive({ key: '', display_name: '', project_key: '', access_key_id: '', secret_access_key: '', session_token: '', region: 'ap-south-1', password: '' });
const deleteForm = reactive({ password: '' });

const projectName = (key?: string) => store.projects.find((item) => item.key === key)?.display_name || key || '—';
const canConfigureProject = (key?: string) => Boolean(store.projects.find((item) => item.key === key)?.permission.can_configure);
const normalizeCredentialKey = (value: string) => {
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
    slug = `aws-credential-${hash.toString(16).padStart(8, '0')}`;
  }
  if (/^[0-9]/.test(slug)) slug = `aws-${slug}`;
  return slug.slice(0, 63).replace(/-+$/g, '');
};
const normalizedCredentialKey = computed(() => normalizeCredentialKey(editor.key || editor.display_name));
const load = async () => { loading.value = true; try { await Promise.all([store.refreshProjects(), store.loadAWSCredentials()]); } catch (error:any) { Message.error(error.message); } finally { loading.value = false; } };
const clearEditor = () => Object.assign(editor, { key: '', display_name: '', project_key: store.currentProjectKey || store.projects[0]?.key || '', access_key_id: '', secret_access_key: '', session_token: '', region: store.currentEnvironment?.region || 'ap-south-1', password: '' });
const openEditor = (record?: AWSCredentialInfo) => { clearEditor(); updating.value = Boolean(record); if (record) Object.assign(editor, { key: record.key, display_name: record.display_name, project_key: record.project_key }); editorVisible.value = true; };
const save = async () => {
	if (!editor.display_name.trim() || !editor.project_key || !editor.access_key_id.trim() || !editor.secret_access_key.trim()) { Message.warning('凭据名称、所属项目和 AK/SK 均不能为空'); return false; }
	const accessKeyID = editor.access_key_id.trim();
	if (!/^[A-Z0-9]{16,128}$/.test(accessKeyID)) { Message.warning('Access Key ID 应为 16–128 位大写字母或数字，请检查是否包含空格'); return false; }
	const secretAccessKey = editor.secret_access_key.trim();
	if (secretAccessKey.length < 16 || secretAccessKey.length > 256) { Message.warning('Secret Access Key 长度应为 16–256 个字符'); return false; }
	if (editor.session_token.trim().length > 16 * 1024) { Message.warning('Session Token 不能超过 16 KiB'); return false; }
  saving.value = true;
  try { await store.saveNamedAWSCredential({ ...editor, key: normalizedCredentialKey.value, access_key_id: accessKeyID, secret_access_key: secretAccessKey, session_token: editor.session_token.trim() }); clearEditor(); Message.success('AWS STS验证通过，凭据已加密保存'); return true; }
  catch (error:any) { Message.error(error.message); return false; }
  finally { saving.value = false; }
};
const selectCredential = (record: AWSCredentialInfo) => Modal.confirm({ title: '切换项目 AWS 权限入口', content: `确认让“${projectName(record.project_key)}”后续部署使用“${record.display_name}”？`, okText: '确认切换', onOk: async () => { try { await store.selectAWSCredential(String(record.project_key), String(record.key)); Message.success('项目 AWS 权限入口已切换'); } catch (error:any) { Message.error(error.message); } } });
const openDelete = (record: AWSCredentialInfo) => { deleteTarget.value = record; deleteForm.password = ''; deleteVisible.value = true; };
const removeCredential = async () => { if (!deleteTarget.value?.key) return false; deleting.value = true; try { await store.deleteNamedAWSCredential(deleteTarget.value.key, deleteForm.password); deleteForm.password = ''; deleteTarget.value = null; Message.success('AWS 凭据已删除'); return true; } catch (error:any) { Message.error(error.message); return false; } finally { deleting.value = false; } };
const formatTime = (value?: string) => value ? new Date(value).toLocaleString('zh-CN') : '—';
</script>

<style scoped>
.credential-name { display:flex; flex-direction:column; gap:5px; }
.credential-name code { color:var(--color-text-3); font-size:12px; }
.ellipsis-text { display:block; max-width:340px; overflow:hidden; text-overflow:ellipsis; white-space:nowrap; }
</style>

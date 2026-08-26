<template>
  <div class="static-cdn-page">
    <div class="page-header">
      <div>
        <h2>静态资源 CDN</h2>
        <p>一键创建私有 S3 + CloudFront OAC，上传文件后直接获得公网 HTTPS CDN 地址。</p>
      </div>
      <a-space>
        <a-button :loading="loading" :disabled="!hasScope" @click="load(true)"><icon-refresh />刷新状态</a-button>
        <a-button type="primary" :disabled="!canCreate" @click="openCreate"><icon-plus />创建 S3 + CDN</a-button>
      </a-space>
    </div>

    <a-alert v-if="!hasScope" type="warning" show-icon>请先选择项目和环境。</a-alert>
    <a-alert v-else-if="!store.awsCredential?.configured" type="warning" show-icon>
      当前项目尚未关联 AWS 权限入口，不能创建或操作 S3/CloudFront。
      <a-link @click="router.push({ name: 'aws-connection' })">前往 AWS 凭据池</a-link>
    </a-alert>
    <a-alert v-else type="success" show-icon>
      当前使用项目 AWS 账号 <strong>{{ store.awsCredential.account_id || '已验证账号' }}</strong>
      · {{ store.awsCredential.display_name || store.awsCredential.key }}。S3 保持私有，仅允许对应 CloudFront OAC 读取。
    </a-alert>

    <a-card class="permission-card">
      <a-descriptions :column="3" bordered size="small">
        <a-descriptions-item label="项目">{{ store.currentProject?.display_name || '—' }}</a-descriptions-item>
        <a-descriptions-item label="环境">{{ store.currentEnvironment?.display_name || '—' }}</a-descriptions-item>
        <a-descriptions-item label="AWS Region">{{ store.currentEnvironment?.region || '—' }}</a-descriptions-item>
        <a-descriptions-item label="S3 权限">私有 Bucket · 禁止公网访问</a-descriptions-item>
        <a-descriptions-item label="CDN 权限">CloudFront OAC 只读</a-descriptions-item>
        <a-descriptions-item label="默认跨域"><code>Access-Control-Allow-Origin: *</code></a-descriptions-item>
      </a-descriptions>
    </a-card>

    <a-spin :loading="loading" style="width:100%">
      <div v-if="resources.length" class="cdn-grid">
        <a-card v-for="resource in resources" :key="resource.bucket_name" class="cdn-card" hoverable>
          <template #title>
            <div class="resource-heading">
              <div class="aws-symbol">CDN</div>
              <div><strong>{{ resource.display_name }}</strong><code>{{ resource.bucket_name }}</code></div>
            </div>
          </template>
          <template #extra><a-tag :color="statusColor(resource.status)">{{ statusText(resource.status) }}</a-tag></template>

          <a-alert v-if="resource.last_error" type="error" show-icon class="resource-error">{{ resource.last_error }}</a-alert>
          <a-descriptions :column="1" size="small" class="resource-details">
            <a-descriptions-item label="S3"><code>s3://{{ resource.bucket_name }}</code></a-descriptions-item>
            <a-descriptions-item label="CloudFront ID"><code>{{ resource.distribution_id || '创建中' }}</code></a-descriptions-item>
            <a-descriptions-item label="CDN 地址">
              <div v-if="resource.cdn_url" class="url-row"><a-link :href="resource.cdn_url" target="_blank">{{ resource.cdn_url }}</a-link><a-button size="mini" @click="copy(resource.cdn_url!)"><icon-copy /></a-button></div>
              <span v-else>等待 CloudFront 返回域名</span>
            </a-descriptions-item>
            <a-descriptions-item label="跨域">{{ resource.cors_origins.join('、') }}</a-descriptions-item>
          </a-descriptions>

          <a-space wrap class="resource-actions">
            <a-button type="primary" :disabled="!store.awsCredential?.configured" @click="openFiles(resource)"><icon-folder />文件管理</a-button>
            <a-button :loading="refreshingBucket === resource.bucket_name" @click="refreshResource(resource)"><icon-refresh />同步状态</a-button>
            <a-button :disabled="!resource.distribution_id || !store.canConfigure" @click="invalidate(resource, ['/*'])">刷新全部缓存</a-button>
            <a-button v-if="resource.status === 'failed'" status="warning" :disabled="!canCreate" @click="retryCreate(resource)">重试创建</a-button>
            <a-button status="danger" :disabled="!store.canConfigure" @click="openDelete(resource)">删除资源</a-button>
          </a-space>
        </a-card>
      </div>
      <div v-else-if="hasScope" class="cdn-empty-panel">
        <a-empty description="当前环境尚未创建静态资源 CDN">
          <a-button type="primary" :disabled="!canCreate" @click="openCreate"><icon-plus />创建第一个 S3 + CDN</a-button>
        </a-empty>
        <div class="cdn-onboarding" aria-label="静态资源 CDN 使用流程">
          <div><span>1</span><strong>创建资源</strong><small>填写全局唯一的 S3 Bucket 名称</small></div>
          <i />
          <div><span>2</span><strong>上传文件</strong><small>浏览器通过预签名地址直传私有 S3</small></div>
          <i />
          <div><span>3</span><strong>复制地址</strong><small>直接获得 CloudFront HTTPS CDN 地址</small></div>
        </div>
      </div>
    </a-spin>
  </div>

  <a-modal v-model:visible="createVisible" title="创建 S3 + CloudFront CDN" width="720px" :ok-loading="creating" ok-text="一键创建" @before-ok="createResource">
    <a-alert type="info" show-icon>平台会创建私有 S3、启用加密和公共访问阻止，再创建 CloudFront OAC 与只读 Bucket Policy。CloudFront 全球部署通常需要等待一段时间。</a-alert>
    <a-form :model="createForm" layout="vertical" style="margin-top:16px">
      <a-grid :cols="2" :col-gap="16">
        <a-grid-item><a-form-item label="资源名称" required><a-input v-model="createForm.display_name" placeholder="游戏静态资源" /></a-form-item></a-grid-item>
        <a-grid-item><a-form-item label="AWS Region"><a-input :model-value="store.currentEnvironment?.region" disabled /></a-form-item></a-grid-item>
        <a-grid-item :span="2"><a-form-item label="S3 Bucket 名称" required extra="AWS 全局唯一；仅支持小写字母、数字和连字符，创建后不可修改。"><a-input v-model="createForm.bucket_name" placeholder="example-app-assets-test" /></a-form-item></a-grid-item>
        <a-grid-item :span="2"><a-form-item label="允许跨域来源" extra="默认 *；如需限制，可每行填写一个完整 Origin，例如 https://test.example.com。"><a-textarea v-model="createForm.cors_origins" :auto-size="{ minRows: 3, maxRows: 6 }" /></a-form-item></a-grid-item>
      </a-grid>
    </a-form>
    <a-alert type="warning" show-icon>该操作会在当前项目关联的 AWS 账号中创建计费资源，并记录到操作审计。</a-alert>
  </a-modal>

  <a-drawer v-model:visible="filesVisible" :width="960" :title="`文件管理 · ${selected?.bucket_name || ''}`" unmount-on-close @cancel="closeFiles">
    <template #footer>
      <a-space><a-button @click="filesVisible = false">关闭</a-button><a-button :loading="loadingFiles" @click="loadObjects"><icon-refresh />刷新文件</a-button></a-space>
    </template>
    <a-alert type="success" show-icon>文件通过 15 分钟有效的 S3 PUT 预签名地址直传，不经过平台服务器；上传完成后会自动刷新对应 CDN 路径缓存。</a-alert>
    <div class="upload-toolbar">
      <a-input v-model="uploadPrefix" allow-clear placeholder="可选目录，例如 uploads/images" style="width:320px"><template #prepend>上传目录</template></a-input>
      <a-button type="primary" :loading="uploading" :disabled="!store.canConfigure" @click="fileInput?.click()"><icon-upload />选择文件上传</a-button>
      <input ref="fileInput" type="file" multiple class="hidden-input" @change="uploadFiles" />
      <span v-if="uploading" class="muted-text">{{ uploadProgress }}</span>
    </div>
    <a-table :data="objects" :loading="loadingFiles" row-key="key" :pagination="{ pageSize: 20 }">
      <template #columns>
        <a-table-column title="文件路径" data-index="key"><template #cell="{ record }"><div class="file-key"><strong>{{ record.key }}</strong><small>{{ record.etag }}</small></div></template></a-table-column>
        <a-table-column title="大小" :width="110"><template #cell="{ record }">{{ formatBytes(record.size) }}</template></a-table-column>
        <a-table-column title="更新时间" :width="190"><template #cell="{ record }">{{ formatTime(record.last_modified) }}</template></a-table-column>
        <a-table-column title="操作" :width="240" fixed="right"><template #cell="{ record }"><a-space>
          <a-button size="mini" @click="copy(record.cdn_url)"><icon-copy />CDN 地址</a-button>
          <a-button size="mini" @click="invalidate(selected!, [`/${record.key}`])">刷新缓存</a-button>
          <a-button size="mini" status="danger" :disabled="!store.canConfigure" @click="removeObject(record)">删除</a-button>
        </a-space></template></a-table-column>
      </template>
      <template #empty><a-empty description="Bucket 为空，可以选择文件上传" /></template>
    </a-table>
  </a-drawer>

  <a-modal v-model:visible="deleteVisible" title="删除 S3 + CloudFront" :ok-loading="deleting" ok-text="确认删除" :ok-button-props="{ status: 'danger', disabled: deleteConfirm !== deleteTarget?.bucket_name }" @before-ok="deleteResource">
    <a-alert type="warning" show-icon>必须先清空 Bucket。平台会先停用 CloudFront；全球状态变为 Deployed 后，再次确认删除即可移除 Distribution、OAC、Bucket Policy 和空 Bucket。</a-alert>
    <div style="margin-top:16px">
      <div style="margin-bottom:8px">输入 {{ deleteTarget?.bucket_name || '' }} 确认</div>
      <a-input v-model="deleteConfirm" />
    </div>
  </a-modal>
</template>

<script setup lang="ts">
import { computed, onMounted, reactive, ref, watch } from 'vue';
import { useRouter } from 'vue-router';
import { Message, Modal } from '@arco-design/web-vue';
import { IconCopy, IconFolder, IconPlus, IconRefresh, IconUpload } from '@arco-design/web-vue/es/icon';
import { api } from '@/services/api';
import { copyToClipboard } from '@/services/clipboard';
import { usePlatformStore } from '@/stores/platform';
import type { StaticCDNObject, StaticCDNResource, StaticCDNUploadAuthorization } from '@/types';

const router = useRouter();
const store = usePlatformStore();
const resources = ref<StaticCDNResource[]>([]);
const objects = ref<StaticCDNObject[]>([]);
const selected = ref<StaticCDNResource | null>(null);
const loading = ref(false);
const creating = ref(false);
const loadingFiles = ref(false);
const uploading = ref(false);
const refreshingBucket = ref('');
const createVisible = ref(false);
const filesVisible = ref(false);
const deleteVisible = ref(false);
const deleting = ref(false);
const deleteTarget = ref<StaticCDNResource | null>(null);
const deleteConfirm = ref('');
const uploadPrefix = ref('');
const uploadProgress = ref('');
const fileInput = ref<HTMLInputElement | null>(null);
const createForm = reactive({ display_name: '', bucket_name: '', cors_origins: '*' });

const hasScope = computed(() => Boolean(store.currentProjectKey && store.currentEnvironmentKey));
const canCreate = computed(() => Boolean(hasScope.value && store.canConfigure && store.awsCredential?.configured));
const basePath = () => `/api/projects/${encodeURIComponent(store.currentProjectKey)}/environments/${encodeURIComponent(store.currentEnvironmentKey)}/static-cdns`;

async function load(fresh = false) {
  if (!hasScope.value) { resources.value = []; return; }
  const revision = store.scopeRevision;
  loading.value = true;
  try {
    const items = await api<StaticCDNResource[]>(`${basePath()}${fresh ? '?fresh=true' : ''}`);
    if (revision === store.scopeRevision) resources.value = items;
  } catch (error: any) {
    if (revision === store.scopeRevision) Message.error(error.message);
  } finally {
    if (revision === store.scopeRevision) loading.value = false;
  }
}

function openCreate() {
  createForm.display_name = `${store.currentProject?.display_name || store.currentProjectKey}静态资源`;
  createForm.bucket_name = `${store.currentProjectKey}-${store.currentEnvironmentKey}-assets`;
  createForm.cors_origins = '*';
  createVisible.value = true;
}

async function createResource() {
  const origins = createForm.cors_origins.split(/[\n,]/).map((item) => item.trim()).filter(Boolean);
  if (!createForm.display_name.trim() || !createForm.bucket_name.trim()) {
    Message.warning('请填写资源名称和 S3 Bucket 名称');
    return false;
  }
  creating.value = true;
  try {
    const resource = await api<StaticCDNResource>(basePath(), {
      method: 'POST', timeoutMs: 0,
      body: JSON.stringify({ display_name: createForm.display_name, bucket_name: createForm.bucket_name, cors_origins: origins }),
    });
    resources.value.push(resource);
    Message.success(`创建请求已完成，CDN 地址：${resource.cdn_url || '等待 CloudFront 返回'}`);
    return true;
  } catch (error: any) {
    Message.error(error.message);
    await load();
    return false;
  } finally {
    creating.value = false;
  }
}

async function retryCreate(resource: StaticCDNResource) {
  creating.value = true;
  try {
    await api<StaticCDNResource>(basePath(), {
      method: 'POST', timeoutMs: 0,
      body: JSON.stringify({ display_name: resource.display_name, bucket_name: resource.bucket_name, cors_origins: resource.cors_origins }),
    });
    Message.success('已重新执行创建流程');
    await load(true);
  } catch (error: any) { Message.error(error.message); await load(); }
  finally { creating.value = false; }
}

async function refreshResource(resource: StaticCDNResource) {
  refreshingBucket.value = resource.bucket_name;
  try {
    const updated = await api<StaticCDNResource>(`${basePath()}/${encodeURIComponent(resource.bucket_name)}/refresh`, { method: 'POST' });
    const index = resources.value.findIndex((item) => item.bucket_name === resource.bucket_name);
    if (index >= 0) resources.value[index] = updated;
    if (selected.value?.bucket_name === updated.bucket_name) selected.value = updated;
    Message.success(`CloudFront 状态：${statusText(updated.status)}`);
  } catch (error: any) { Message.error(error.message); }
  finally { refreshingBucket.value = ''; }
}

async function openFiles(resource: StaticCDNResource) {
  selected.value = resource;
  uploadPrefix.value = '';
  filesVisible.value = true;
  await loadObjects();
}

async function loadObjects() {
  if (!selected.value) return;
  loadingFiles.value = true;
  try {
    objects.value = await api<StaticCDNObject[]>(`${basePath()}/${encodeURIComponent(selected.value.bucket_name)}/objects`);
  } catch (error: any) { Message.error(error.message); }
  finally { loadingFiles.value = false; }
}

async function uploadFiles(event: Event) {
  const input = event.target as HTMLInputElement;
  const files = Array.from(input.files || []);
  input.value = '';
  if (!selected.value || !files.length) return;
  uploading.value = true;
  const uploadedPaths: string[] = [];
  let failed = 0;
  const prefix = uploadPrefix.value.trim().replace(/^\/+|\/+$/g, '');
  for (let index = 0; index < files.length; index += 1) {
    const file = files[index];
    const key = [prefix, file.name].filter(Boolean).join('/');
    uploadProgress.value = `${index + 1}/${files.length} · ${key}`;
    try {
      const authorization = await api<StaticCDNUploadAuthorization>(`${basePath()}/${encodeURIComponent(selected.value.bucket_name)}/upload-url`, {
        method: 'POST', body: JSON.stringify({ key }),
      });
      try {
        const response = await fetch(authorization.upload_url, {
          method: authorization.method, body: file,
          headers: file.type ? { 'Content-Type': file.type } : undefined,
        });
        if (!response.ok) throw new Error(`S3 上传失败（HTTP ${response.status}）`);
      } catch (directError: any) {
        try {
          const objectPath = key.split('/').map(encodeURIComponent).join('/');
          await api<void>(`${basePath()}/${encodeURIComponent(selected.value.bucket_name)}/objects/${objectPath}`, {
            method: 'PUT', body: file,
            headers: { 'Content-Type': file.type || 'application/octet-stream' },
            timeoutMs: 120_000,
          });
          Message.info(`${file.name}：浏览器直传失败，已通过平台安全中转上传`);
        } catch (proxyError: any) {
          const directMessage = String(directError?.message || '网络请求失败');
          throw new Error(`S3 直传失败：${directMessage}；平台中转也失败：${proxyError?.message || '未知错误'}`);
        }
      }
      uploadedPaths.push(`/${authorization.key}`);
    } catch (error: any) {
      failed += 1;
      Message.error(`${file.name}：${error.message}`);
    }
  }
  try {
    if (uploadedPaths.length) {
      try {
        for (let index = 0; index < uploadedPaths.length; index += 100) {
          await invalidate(selected.value, uploadedPaths.slice(index, index + 100), false);
        }
      } catch { /* upload succeeded; the user can retry invalidation independently */ }
      Message.success(`已上传 ${uploadedPaths.length} 个文件${failed ? `，失败 ${failed} 个` : ''}`);
    }
    await loadObjects();
  } finally {
    uploading.value = false;
    uploadProgress.value = '';
  }
}

async function invalidate(resource: StaticCDNResource, paths: string[], notify = true) {
  try {
    await api(`${basePath()}/${encodeURIComponent(resource.bucket_name)}/invalidate`, {
      method: 'POST', body: JSON.stringify({ paths }),
    });
    if (notify) Message.success('CloudFront 缓存刷新已提交');
  } catch (error: any) {
    Message.error(error.message);
    throw error;
  }
}

function removeObject(object: StaticCDNObject) {
  if (!selected.value) return;
  Modal.warning({
    title: '删除 S3 文件',
    content: `确认删除 ${object.key}？该操作不可恢复。`,
    hideCancel: false,
    onOk: async () => {
      try {
        await api(`${basePath()}/${encodeURIComponent(selected.value!.bucket_name)}/objects/${object.key.split('/').map(encodeURIComponent).join('/')}`, { method: 'DELETE' });
        objects.value = objects.value.filter((item) => item.key !== object.key);
        Message.success('文件已删除');
      } catch (error: any) { Message.error(error.message); }
    },
  });
}

function openDelete(resource: StaticCDNResource) {
  deleteTarget.value = resource;
  deleteConfirm.value = '';
  deleteVisible.value = true;
}

async function deleteResource() {
  if (!deleteTarget.value || deleteConfirm.value !== deleteTarget.value.bucket_name) return false;
  deleting.value = true;
  try {
    const response = await api<any>(`${basePath()}/${encodeURIComponent(deleteTarget.value.bucket_name)}`, {
      method: 'DELETE', timeoutMs: 0, body: JSON.stringify({ confirm: deleteConfirm.value }),
    });
    if (response?.resource?.status === 'disabling') {
      Message.warning(response.message || 'CloudFront 正在停用，请稍后再次确认删除');
      await load();
      return true;
    }
    resources.value = resources.value.filter((item) => item.bucket_name !== deleteTarget.value?.bucket_name);
    Message.success('S3 + CloudFront 资源已删除');
    return true;
  } catch (error: any) { Message.error(error.message); return false; }
  finally { deleting.value = false; }
}

function closeFiles() {
  selected.value = null;
  objects.value = [];
}

const copy = async (value: string) => {
  try { await copyToClipboard(value); Message.success('已复制'); }
  catch { Message.error('复制失败，请手动选择内容'); }
};
const statusText = (value: string) => ({ deployed: '已部署', inprogress: '部署中', creating: '创建中', failed: '创建失败', disabling: '停用中', deleted: '已删除' }[value.toLowerCase()] || value);
const statusColor = (value: string) => ({ deployed: 'green', inprogress: 'arcoblue', creating: 'arcoblue', failed: 'red', disabling: 'orange', deleted: 'gray' }[value.toLowerCase()] || 'gray');
const formatTime = (value: string) => value ? new Date(value).toLocaleString('zh-CN') : '—';
const formatBytes = (value: number) => {
  if (value < 1024) return `${value} B`;
  if (value < 1024 ** 2) return `${(value / 1024).toFixed(1)} KiB`;
  if (value < 1024 ** 3) return `${(value / 1024 ** 2).toFixed(1)} MiB`;
  return `${(value / 1024 ** 3).toFixed(1)} GiB`;
};

watch(() => store.scopeRevision, () => {
  resources.value = [];
  filesVisible.value = false;
  selected.value = null;
  void Promise.all([store.loadAWSCredential(), load()]);
});
onMounted(() => void Promise.all([store.loadAWSCredential(), load()]));
</script>

<style scoped>
.static-cdn-page { min-width: 0; }
.permission-card { margin: 16px 0; }
.cdn-grid { display: grid; grid-template-columns: repeat(auto-fit, minmax(430px, 1fr)); gap: 16px; }
.cdn-card { min-width: 0; }
.resource-heading { display: flex; align-items: center; gap: 12px; min-width: 0; }
.resource-heading > div:last-child { display: flex; flex-direction: column; gap: 3px; min-width: 0; }
.resource-heading code { color: var(--color-text-3); font-size: 12px; overflow: hidden; text-overflow: ellipsis; }
.aws-symbol { width: 42px; height: 42px; display: grid; place-items: center; border-radius: 10px; background: #ff9900; color: #111827; font-weight: 800; font-size: 12px; }
.resource-error { margin-bottom: 12px; overflow-wrap: anywhere; }
.resource-details { margin-top: 4px; }
.resource-actions { margin-top: 16px; }
.url-row { display: flex; align-items: center; gap: 8px; min-width: 0; }
.url-row a { max-width: 360px; overflow: hidden; text-overflow: ellipsis; }
.upload-toolbar { display: flex; align-items: center; gap: 12px; margin: 18px 0; }
.hidden-input { display: none; }
.cdn-empty-panel { margin-top: 16px; padding: 34px 24px 28px; border: 1px dashed #c7d7ef; border-radius: 14px; background: linear-gradient(145deg, rgb(247 250 255 / 92%), #fff); }
.cdn-empty-panel :deep(.arco-empty) { padding: 0; }
.cdn-onboarding { max-width: 820px; margin: 30px auto 0; display: grid; grid-template-columns: minmax(0, 1fr) 44px minmax(0, 1fr) 44px minmax(0, 1fr); align-items: center; gap: 12px; }
.cdn-onboarding > div { min-width: 0; padding: 16px; border: 1px solid #e0e9f7; border-radius: 12px; background: #fff; box-shadow: 0 8px 22px rgb(29 57 102 / 6%); }
.cdn-onboarding span { width: 28px; height: 28px; margin-bottom: 10px; display: grid; place-items: center; color: #fff; border-radius: 8px; background: linear-gradient(145deg, #4080ff, #165dff); font-size: 12px; font-weight: 700; box-shadow: 0 5px 12px rgb(22 93 255 / 20%); }
.cdn-onboarding strong, .cdn-onboarding small { display: block; }
.cdn-onboarding strong { font-size: 13px; }
.cdn-onboarding small { margin-top: 5px; color: var(--color-text-3); font-size: 11px; line-height: 1.5; }
.cdn-onboarding > i { height: 1px; background: linear-gradient(90deg, #c7d7ef, #94bfff); }
.muted-text { color: var(--color-text-3); }
.file-key { display: flex; flex-direction: column; gap: 3px; min-width: 0; }
.file-key strong { word-break: break-all; }
.file-key small { color: var(--color-text-4); font-family: monospace; }
@media (max-width: 980px) { .cdn-onboarding { grid-template-columns: 1fr; }.cdn-onboarding > i { width: 1px; height: 18px; margin: -5px auto; } }
@media (max-width: 800px) { .cdn-grid { grid-template-columns: 1fr; } .upload-toolbar { align-items: stretch; flex-direction: column; } }
</style>

<template>
  <div class="state-page">
    <div class="page-header">
      <div><h2>Terraform 状态中心</h2><p>平台统一管理所有项目的 Terraform state；业务资源凭据与状态存储凭据完全分离。</p></div>
      <a-space><a-button :loading="loading" @click="load"><icon-refresh />刷新</a-button><a-button type="primary" :loading="saving" @click="save"><icon-save />验证并保存</a-button></a-space>
    </div>

    <a-alert type="warning" show-icon class="full-card">
      S3 bucket 是所有项目创建、扩容和销毁的权威状态源。保存时会验证 STS、S3 读写删除权限，并强制开启版本控制、默认加密和公共访问阻止。Secret Access Key 与 Session Token 仅以 AES-256-GCM 密文保存，不会回显或写入部署日志。
    </a-alert>

    <div class="summary-grid">
      <a-card><span>配置状态</span><strong :class="config.configured && config.enabled ? 'success-text' : 'danger-text'">{{ config.configured && config.enabled ? '已启用' : '未配置' }}</strong></a-card>
      <a-card><span>状态 AWS 账号</span><strong>{{ config.account_id || '—' }}</strong></a-card>
      <a-card><span>已登记状态</span><strong>{{ states.length }}</strong></a-card>
      <a-card><span>仍有托管资源</span><strong>{{ states.reduce((sum, item) => sum + item.managed_resources, 0) }}</strong></a-card>
    </div>

    <a-card class="full-card config-card">
      <template #title><span class="card-title">统一 S3 状态存储</span></template>
      <a-form :model="form" layout="vertical">
        <a-grid :cols="2" :col-gap="18" :row-gap="4">
          <a-grid-item><a-form-item label="启用统一状态中心" extra="关闭后会阻止新的 Terraform 操作，避免回退到本机 state。"><a-switch v-model="form.enabled" /></a-form-item></a-grid-item>
          <a-grid-item><a-form-item label="AWS Region" required><a-select v-model="form.region" allow-search><a-option v-for="region in store.platform?.aws_regions || []" :key="region.code" :value="region.code">{{ region.code }} · {{ region.name }}</a-option></a-select></a-form-item></a-grid-item>
          <a-grid-item><a-form-item label="S3 Bucket" required extra="填写已经创建的专用 bucket 名称，不要填写 s3://。"><a-input v-model="form.bucket" placeholder="ops-deploy-terraform-state" /></a-form-item></a-grid-item>
          <a-grid-item><a-form-item label="State 路径前缀" required extra="最终路径：前缀/projects/项目/环境/阶段/terraform.tfstate"><a-input v-model="form.key_prefix" placeholder="ops-deploy" /></a-form-item></a-grid-item>
          <a-grid-item :span="2"><a-form-item label="KMS Key ARN 或 Alias（可选）" extra="留空使用 S3 AES256；填写后使用指定 KMS Key 加密。"><a-input v-model="form.kms_key_id" placeholder="arn:aws:kms:... 或 alias/terraform-state" /></a-form-item></a-grid-item>
          <a-grid-item><a-form-item label="状态中心 Access Key ID" :required="!config.configured" :extra="config.configured ? `当前：${config.masked_access_key}；留空表示保持不变` : '仅用于统一状态 bucket，不参与项目资源创建'"><a-input v-model="form.access_key_id" autocomplete="off" placeholder="AKIA... / ASIA..." /></a-form-item></a-grid-item>
          <a-grid-item><a-form-item label="Secret Access Key" :required="!config.configured" extra="更新其他配置时可留空，平台会继续使用已有加密凭据。"><a-input-password v-model="form.secret_access_key" autocomplete="new-password" /></a-form-item></a-grid-item>
          <a-grid-item :span="2"><a-form-item label="Session Token（仅临时凭据）"><a-textarea v-model="form.session_token" :auto-size="{ minRows: 2, maxRows: 4 }" /></a-form-item></a-grid-item>
          <a-grid-item :span="2"><a-form-item label="当前平台登录密码（可选）" extra="填写后执行二次身份校验。"><a-input-password v-model="form.password" autocomplete="current-password" placeholder="可留空" /></a-form-item></a-grid-item>
        </a-grid>
      </a-form>
      <a-alert v-if="config.principal_arn" type="success" show-icon>最近验证身份：{{ config.principal_arn }} · {{ formatTime(config.verified_at) }}</a-alert>
    </a-card>

    <a-card class="full-card">
      <template #title><span class="card-title">项目状态索引</span></template>
      <template #extra><span class="muted-text">S3 使用对象 Key 模拟目录，不需要手工创建文件夹</span></template>
      <a-table :data="states" row-key="object_key" :pagination="{ pageSize: 10 }" :loading="loading">
        <template #columns>
          <a-table-column title="项目 / 环境" :width="190"><template #cell="{ record }"><div class="state-scope"><strong>{{ projectName(record.project) }}</strong><span>{{ record.environment }}</span></div></template></a-table-column>
          <a-table-column title="阶段" :width="110"><template #cell="{ record }"><a-tag :color="record.stage === 'infra' ? 'arcoblue' : 'purple'">{{ record.stage === 'infra' ? '基础资源' : '平台组件' }}</a-tag></template></a-table-column>
          <a-table-column title="S3 对象"><template #cell="{ record }"><a-tooltip :content="`s3://${record.bucket}/${record.object_key}`"><code class="object-key">{{ record.object_key }}</code></a-tooltip></template></a-table-column>
          <a-table-column title="Serial" data-index="serial" :width="90" />
          <a-table-column title="托管资源" :width="110"><template #cell="{ record }"><a-tag :color="record.managed_resources > 0 ? 'green' : 'gray'">{{ record.managed_resources }}</a-tag></template></a-table-column>
          <a-table-column title="集中状态" :width="110"><template #cell="{ record }"><a-tag :color="config.configured && record.bucket === config.bucket && record.object_key.startsWith(`${config.key_prefix}/projects/`) ? 'green' : 'orange'">{{ config.configured && record.bucket === config.bucket && record.object_key.startsWith(`${config.key_prefix}/projects/`) ? '已集中' : '待迁移' }}</a-tag></template></a-table-column>
          <a-table-column title="最近同步" :width="180"><template #cell="{ record }">{{ formatTime(record.updated_at) }}</template></a-table-column>
        </template>
        <template #empty><a-empty description="尚无项目 state；项目首次执行 Terraform 后会自动创建对应路径" /></template>
      </a-table>
    </a-card>
  </div>
</template>

<script setup lang="ts">
import { onMounted, reactive, ref } from 'vue';
import { Message } from '@arco-design/web-vue';
import { IconRefresh, IconSave } from '@arco-design/web-vue/es/icon';
import { api } from '@/services/api';
import { usePlatformStore } from '@/stores/platform';
import type { TerraformStateCenter, TerraformStateCenterConfig, TerraformStateLocation } from '@/types';

const store = usePlatformStore();
const loading = ref(false); const saving = ref(false);
const config = ref<TerraformStateCenterConfig>({ configured: false, enabled: true });
const states = ref<TerraformStateLocation[]>([]);
const form = reactive({ enabled: true, bucket: '', region: 'ap-south-1', key_prefix: 'ops-deploy', kms_key_id: '', access_key_id: '', secret_access_key: '', session_token: '', password: '' });

const apply = (payload: TerraformStateCenter) => {
  config.value = payload.config;
  states.value = payload.states || [];
  Object.assign(form, { enabled: payload.config.configured ? payload.config.enabled : true, bucket: payload.config.bucket || '', region: payload.config.region || 'ap-south-1', key_prefix: payload.config.key_prefix || 'ops-deploy', kms_key_id: payload.config.kms_key_id || '', access_key_id: '', secret_access_key: '', session_token: '', password: '' });
};
const load = async () => { loading.value = true; try { apply(await api<TerraformStateCenter>('/api/terraform-state')); } catch (error:any) { Message.error(error.message); } finally { loading.value = false; } };
const save = async () => {
  if (!form.bucket.trim() || !form.region || !form.key_prefix.trim()) { Message.warning('请填写 S3 Bucket、Region 和路径前缀'); return; }
  if (!config.value.configured && (!form.access_key_id.trim() || !form.secret_access_key.trim())) { Message.warning('首次配置必须填写状态中心 AK/SK'); return; }
  saving.value = true;
  try {
    await api<TerraformStateCenterConfig>('/api/terraform-state', { method: 'PUT', body: JSON.stringify({ ...form, bucket: form.bucket.trim(), key_prefix: form.key_prefix.trim(), access_key_id: form.access_key_id.trim(), secret_access_key: form.secret_access_key.trim(), session_token: form.session_token.trim() }) });
    Message.success('状态中心连接和安全配置验证通过'); await load();
  } catch (error:any) { Message.error(error.message); }
  finally { saving.value = false; }
};
const projectName = (key: string) => store.projects.find((item) => item.key === key)?.display_name || key;
const formatTime = (value?: string) => value ? new Date(value).toLocaleString('zh-CN') : '—';
onMounted(load);
</script>

<style scoped>
.state-page{display:flex;flex-direction:column;gap:16px}.summary-grid{display:grid;grid-template-columns:repeat(4,minmax(0,1fr));gap:14px}.summary-grid :deep(.arco-card-body){display:flex;flex-direction:column;gap:7px}.summary-grid span{font-size:12px;color:var(--color-text-3)}.summary-grid strong{font-size:20px}.config-card :deep(.arco-card-body){padding:20px 24px}.state-scope{display:flex;flex-direction:column;gap:4px}.state-scope span{font-size:12px;color:var(--color-text-3)}.object-key{display:block;max-width:560px;overflow:hidden;text-overflow:ellipsis;white-space:nowrap}@media(max-width:1100px){.summary-grid{grid-template-columns:repeat(2,minmax(0,1fr))}}
</style>

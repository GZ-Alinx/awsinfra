<template>
  <a-divider orientation="left">高级参数</a-divider>
  <a-grid :cols="2" :col-gap="10">
    <template v-if="kind === 'rds' || kind === 'postgres'">
      <a-grid-item><a-form-item label="引擎版本"><a-select :model-value="selectedVersion" :loading="versionLoading" allow-search @popup-visible-change="(visible) => visible && loadVersions()" @change="setVersion(String($event))"><a-option v-for="version in versionOptions" :key="version" :value="version">{{ version }}</a-option></a-select><small class="field-help" :class="{ 'danger-text': versionError }">{{ versionHint }}</small></a-form-item></a-grid-item>
      <a-grid-item><a-form-item label="最大自动扩容 GiB"><a-input-number v-model="model.max_allocated_storage" :min="model.allocated_storage || 20" /></a-form-item></a-grid-item>
      <a-grid-item><a-form-item label="备份保留天数"><a-input-number v-model="model.backup_retention_days" :min="0" :max="35" /></a-form-item></a-grid-item>
      <a-grid-item><a-form-item label="Performance Insights"><a-switch v-model="model.performance_insights_enabled" /></a-form-item></a-grid-item>
    </template>
    <template v-else-if="kind === 'aurora'">
      <a-grid-item><a-form-item label="引擎版本"><a-select :model-value="selectedVersion" :loading="versionLoading" allow-search @popup-visible-change="(visible) => visible && loadVersions()" @change="setVersion(String($event))"><a-option v-for="version in versionOptions" :key="version" :value="version">{{ version }}</a-option></a-select><small class="field-help" :class="{ 'danger-text': versionError }">{{ versionHint }}</small></a-form-item></a-grid-item>
      <a-grid-item><a-form-item label="备份保留天数"><a-input-number v-model="model.backup_retention_days" :min="1" :max="35" /></a-form-item></a-grid-item>
      <a-grid-item><a-form-item label="Performance Insights"><a-switch v-model="model.performance_insights_enabled" /></a-form-item></a-grid-item>
    </template>
    <template v-else-if="kind === 'documentdb'">
      <a-grid-item><a-form-item label="引擎版本"><a-select :model-value="selectedVersion" :loading="versionLoading" allow-search @popup-visible-change="(visible) => visible && loadVersions()" @change="setVersion(String($event))"><a-option v-for="version in versionOptions" :key="version" :value="version">{{ version }}</a-option></a-select><small class="field-help" :class="{ 'danger-text': versionError }">{{ versionHint }}</small></a-form-item></a-grid-item>
      <a-grid-item><a-form-item label="备份保留天数"><a-input-number v-model="model.backup_retention_days" :min="1" :max="35" /></a-form-item></a-grid-item>
    </template>
    <template v-else-if="kind === 'elasticache'">
      <a-grid-item><a-form-item label="引擎"><a-select :model-value="model.engine" @change="changeElastiCacheEngine(String($event))"><a-option value="valkey">Valkey</a-option><a-option value="redis">Redis OSS</a-option></a-select></a-form-item></a-grid-item>
      <a-grid-item><a-form-item label="引擎版本"><a-select :model-value="selectedVersion" :loading="versionLoading" allow-search @popup-visible-change="(visible) => visible && loadVersions()" @change="setVersion(String($event))"><a-option v-for="version in versionOptions" :key="version" :value="version">{{ version }}</a-option></a-select><small class="field-help" :class="{ 'danger-text': versionError }">{{ versionHint }}</small></a-form-item></a-grid-item>
      <a-grid-item><a-form-item label="快照保留天数"><a-input-number v-model="model.snapshot_retention_days" :min="0" :max="35" /></a-form-item></a-grid-item>
      <a-grid-item><a-form-item label="传输加密" :extra="model.mode === 'serverless' ? 'AWS Serverless 强制开启，无法关闭。' : '新建配置默认关闭；已运行实例不会被自动改写。'"><a-switch :model-value="model.mode === 'serverless' || model.tls_enabled" :disabled="model.mode === 'serverless'" @change="model.tls_enabled = Boolean($event)" /></a-form-item></a-grid-item>
    </template>
    <template v-else-if="kind === 'msk'">
      <a-grid-item><a-form-item label="Kafka 版本"><a-select :model-value="selectedVersion" :loading="versionLoading" :disabled="model.mode === 'serverless'" allow-search @popup-visible-change="(visible) => visible && loadVersions()" @change="setVersion(String($event))"><a-option v-for="version in versionOptions" :key="version" :value="version">{{ version }}</a-option></a-select><small class="field-help" :class="{ 'danger-text': versionError }">{{ model.mode === 'serverless' ? 'Serverless 版本由 AWS 托管' : versionHint }}</small></a-form-item></a-grid-item>
      <a-grid-item :span="2"><a-form-item label="监控粒度"><a-select v-model="model.enhanced_monitoring"><a-option value="DEFAULT">默认</a-option><a-option value="PER_BROKER">每 Broker</a-option><a-option value="PER_TOPIC_PER_BROKER">每 Topic / Broker</a-option><a-option value="PER_TOPIC_PER_PARTITION">每 Topic / Partition</a-option></a-select></a-form-item></a-grid-item>
    </template>
    <template v-else-if="kind === 'mq'">
      <a-grid-item><a-form-item label="引擎版本"><a-select :model-value="selectedVersion" :loading="versionLoading" allow-search @popup-visible-change="(visible) => visible && loadVersions()" @change="setVersion(String($event))"><a-option v-for="version in versionOptions" :key="version" :value="version">{{ version }}</a-option></a-select><small class="field-help" :class="{ 'danger-text': versionError }">{{ versionHint }}</small></a-form-item></a-grid-item>
      <a-grid-item><a-form-item label="通用日志"><a-switch v-model="model.general_logs_enabled" /></a-form-item></a-grid-item>
    </template>
    <template v-else-if="kind === 'ecr'">
      <a-grid-item><a-form-item label="推送时扫描镜像"><a-switch v-model="model.scan_on_push" /></a-form-item></a-grid-item>
      <a-grid-item><a-form-item label="镜像标签策略"><a-select v-model="model.image_tag_mutability"><a-option value="IMMUTABLE">不可覆盖</a-option><a-option value="MUTABLE">允许覆盖</a-option></a-select></a-form-item></a-grid-item>
      <a-grid-item><a-form-item label="无标签镜像保留天数"><a-input-number v-model="model.untagged_expire_days" :min="1" :max="3650" /></a-form-item></a-grid-item>
      <a-grid-item :span="2">
        <a-alert type="info" :show-icon="true">
          ECR 按 AWS 账号与 Region 在项目内共享，dev / test / uat / prod 会复用同名仓库；环境销毁不会删除仓库或镜像。请用环境前缀 Tag（如 prod-版本号）区分镜像。
        </a-alert>
      </a-grid-item>
    </template>
    <a-grid-item v-if="supportsAutoMinor"><a-form-item label="自动次版本升级" extra="新建服务默认关闭；如需升级，由运维确认版本后手动开启。"><a-space><a-switch v-model="model.auto_minor_version_upgrade" /><a-tag v-if="!model.auto_minor_version_upgrade" color="gray">默认关闭</a-tag><a-tag v-else color="orange">已开启</a-tag></a-space></a-form-item></a-grid-item>
    <a-grid-item v-if="supportsApply"><a-form-item label="立即应用变更"><a-switch v-model="model.apply_immediately" /></a-form-item></a-grid-item>
    <a-grid-item v-if="kind === 'postgres' || kind === 'aurora'"><a-form-item label="删除保护"><a-switch v-model="model.deletion_protection" /></a-form-item></a-grid-item>
    <a-grid-item v-if="supportsFinalSnapshot"><a-form-item label="删除时跳过最终快照" extra="生产环境建议关闭。"><a-switch v-model="model.skip_final_snapshot" /></a-form-item></a-grid-item>
  </a-grid>
</template>

<script setup lang="ts">
import { computed, inject, nextTick } from 'vue';
import type { Dict } from '@/types';

const props = defineProps<{ kind: 'rds' | 'postgres' | 'aurora' | 'documentdb' | 'elasticache' | 'msk' | 'mq' | 'ecr'; model: Dict }>();
type EngineVersionCatalog = { options: (service: string, engine: string, current: string) => string[]; loading: (service: string, engine: string) => boolean; hint: (service: string, engine: string) => string; load: (service: string, engine: string, force?: boolean) => Promise<void> | void };
const engineCatalog = inject<EngineVersionCatalog>('cloudEngineVersionCatalog');
const versionService = computed(() => ({ rds: 'rds-mysql', postgres: 'rds-postgres', aurora: 'aurora-mysql', documentdb: 'documentdb', elasticache: 'elasticache', msk: 'msk', mq: 'amazon-mq', ecr: '' }[props.kind] || ''));
const versionEngine = computed(() => props.kind === 'elasticache' ? String(props.model.engine || 'valkey') : '');
const selectedVersion = computed(() => String(props.kind === 'msk' ? props.model.kafka_version || '' : props.model.engine_version || ''));
const versionOptions = computed(() => engineCatalog?.options(versionService.value, versionEngine.value, selectedVersion.value) || [selectedVersion.value].filter(Boolean));
const versionLoading = computed(() => Boolean(engineCatalog?.loading(versionService.value, versionEngine.value)));
const versionHint = computed(() => engineCatalog?.hint(versionService.value, versionEngine.value) || '');
const versionError = computed(() => /失败|错误|无权|不可用|failed|error/i.test(versionHint.value));
const loadVersions = () => versionService.value ? engineCatalog?.load(versionService.value, versionEngine.value) : undefined;
const elasticacheParameterGroup = (engine: string, version: string) => {
  const [major = '', minor = '0'] = version.split('.');
  if (engine === 'valkey') return `default.valkey${major || '8'}.cluster.on`;
  if (major === '7' || !major) return 'default.redis7.cluster.on';
  if (major === '6') return 'default.redis6.x.cluster.on';
  return `default.redis${major}.${minor}.cluster.on`;
};
const changeElastiCacheEngine = async (engine: string) => {
  props.model.engine = engine;
  props.model.engine_version = engine === 'redis' ? '7.1' : '8.2';
  props.model.parameter_group_name = elasticacheParameterGroup(engine, String(props.model.engine_version));
  await nextTick();
  loadVersions();
};
const setVersion = (version: string) => {
  if (props.kind === 'msk') props.model.kafka_version = version;
  else {
    props.model.engine_version = version;
    if (props.kind === 'elasticache') props.model.parameter_group_name = elasticacheParameterGroup(String(props.model.engine || 'valkey'), version);
  }
};
const supportsAutoMinor = computed(() => ['rds', 'postgres', 'aurora', 'documentdb', 'elasticache', 'mq'].includes(props.kind));
const supportsApply = computed(() => ['rds', 'postgres', 'aurora', 'documentdb', 'elasticache', 'mq'].includes(props.kind));
const supportsFinalSnapshot = computed(() => ['rds', 'postgres', 'aurora', 'documentdb'].includes(props.kind));
</script>

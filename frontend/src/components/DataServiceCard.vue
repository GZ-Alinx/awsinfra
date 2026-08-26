<template>
  <a-card v-if="isActiveEditor" class="managed-service-card enabled">
    <template #title><span class="managed-service-title"><i />{{ title }}</span></template>
    <template #extra><a-space size="small"><a-tag size="small" :color="model.enabled ? 'green' : 'gray'">{{ model.enabled ? '已启用' : '未启用' }}</a-tag><a-switch :model-value="model.enabled" @change="toggleService(Boolean($event))" /></a-space></template>
    <div v-if="model.enabled" class="managed-service-parameters">
      <div class="managed-service-caption"><strong>部署参数</strong><span>仅保存当前环境的配置，执行阶段 1 后生效</span></div>
      <a-form :model="model" layout="vertical">
        <slot name="form" />
        <cloud-service-advanced-options v-if="advancedKind" :kind="advancedKind" :model="model" />
      </a-form>
    </div>
  </a-card>
</template>

<script setup lang="ts">
import { computed, inject, ref } from 'vue';
import type { Ref } from 'vue';
import type { Dict } from '@/types';
import CloudServiceAdvancedOptions from '@/components/CloudServiceAdvancedOptions.vue';
type AdvancedKind = 'rds' | 'postgres' | 'aurora' | 'documentdb' | 'elasticache' | 'msk' | 'mq' | 'ecr';
const props = defineProps<{ title: string; model: Dict }>();
const activeCloudService = inject<Ref<string>>('activeCloudService', ref(''));
const requestCloudServiceToggle = inject<(key: string, value: boolean) => void>('requestCloudServiceToggle');
const serviceKey = computed(() => ({
  'RDS MySQL': 'rds',
  'RDS PostgreSQL': 'postgres',
  'Aurora MySQL': 'aurora',
  'Amazon DocumentDB（MongoDB 兼容）': 'documentdb',
  'AWS ElastiCache（Redis / Valkey）': 'elasticache',
  'Amazon MSK Kafka': 'msk',
  'Amazon MQ RabbitMQ': 'amazon_mq',
  'Amazon ECR 镜像仓库': 'ecr',
} as Record<string, string>)[props.title] || '');
const isActiveEditor = computed(() => Boolean(props.model.enabled && serviceKey.value === activeCloudService.value));
const toggleService = (value: boolean) => {
  if (requestCloudServiceToggle) requestCloudServiceToggle(serviceKey.value, value);
  else props.model.enabled = value;
};
const advancedKind = computed<AdvancedKind | undefined>(() => ({
  'RDS MySQL': 'rds',
  'RDS PostgreSQL': 'postgres',
  'Aurora MySQL': 'aurora',
  'Amazon DocumentDB（MongoDB 兼容）': 'documentdb',
  'AWS ElastiCache（Redis / Valkey）': 'elasticache',
  'Amazon MSK Kafka': 'msk',
  'Amazon MQ RabbitMQ': 'mq',
  'Amazon ECR 镜像仓库': 'ecr',
} as Record<string, AdvancedKind>)[props.title]);
</script>

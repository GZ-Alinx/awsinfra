<template>
  <div class="audit-page">
    <div class="page-header">
      <div>
        <h2>操作审计</h2>
        <p>记录平台用户的创建、修改、删除、发布、凭据查看和登录等操作，便于追踪变更和安全复核。</p>
      </div>
      <a-space>
        <a-tag color="arcoblue">共 {{ total }} 条</a-tag>
        <a-button :loading="loading" @click="loadEvents"><icon-refresh />刷新</a-button>
      </a-space>
    </div>

    <a-alert type="info" show-icon>
      审计日志只记录操作元数据和执行结果，不保存请求正文、密码、Token、证书或其他敏感内容。
    </a-alert>

    <a-card class="filter-card">
      <div class="filter-grid">
        <a-input
          v-model="filters.username"
          allow-clear
          placeholder="操作用户"
          @press-enter="applyFilters"
        />
        <a-select v-model="filters.method" placeholder="全部操作类型">
          <a-option value="">全部操作类型</a-option>
          <a-option value="POST">创建 / 执行</a-option>
          <a-option value="PUT">更新</a-option>
          <a-option value="PATCH">部分更新</a-option>
          <a-option value="DELETE">删除 / 取消</a-option>
        </a-select>
        <a-select v-model="filters.result" placeholder="全部执行结果">
          <a-option value="">全部执行结果</a-option>
          <a-option value="success">成功</a-option>
          <a-option value="failed">失败 / 被拒绝</a-option>
        </a-select>
        <a-select v-model="filters.period" placeholder="时间范围">
          <a-option value="24h">最近 24 小时</a-option>
          <a-option value="7d">最近 7 天</a-option>
          <a-option value="30d">最近 30 天</a-option>
          <a-option value="all">全部时间</a-option>
        </a-select>
        <a-input
          v-model="filters.keyword"
          allow-clear
          placeholder="搜索用户、接口或资源标识"
          class="keyword-input"
          @press-enter="applyFilters"
        />
        <a-space class="filter-actions">
          <a-checkbox v-model="filters.includeSystem">显示系统事件</a-checkbox>
          <a-button type="primary" @click="applyFilters"><icon-search />查询</a-button>
          <a-button @click="resetFilters">重置</a-button>
        </a-space>
      </div>
    </a-card>

    <a-card class="table-card">
      <a-table
        :data="events"
        :loading="loading"
        row-key="id"
        :pagination="false"
        :scroll="{ x: 1120 }"
        @row-click="openDetail"
      >
        <template #columns>
          <a-table-column title="操作时间" :width="180">
            <template #cell="{ record }">
              <div class="time-cell">
                <strong>{{ formatDate(record.occurred_at) }}</strong>
                <small>#{{ record.id }}</small>
              </div>
            </template>
          </a-table-column>
          <a-table-column title="用户" :width="145">
            <template #cell="{ record }">
              <a-space>
                <a-avatar :size="28">{{ record.username.slice(0, 1).toUpperCase() }}</a-avatar>
                <strong>{{ record.username }}</strong>
              </a-space>
            </template>
          </a-table-column>
          <a-table-column title="操作" :width="155">
            <template #cell="{ record }">
              <a-tag :color="methodColor(record.method)">{{ record.operation }}</a-tag>
            </template>
          </a-table-column>
          <a-table-column title="资源与目标" :width="340">
            <template #cell="{ record }">
              <div class="target-cell">
                <strong>{{ record.resource || '平台接口' }}</strong>
                <small :title="record.target || record.path">{{ record.target || record.path }}</small>
              </div>
            </template>
          </a-table-column>
          <a-table-column title="结果" :width="125">
            <template #cell="{ record }">
              <a-badge
                :status="record.successful ? 'success' : 'danger'"
                :text="record.successful ? `成功 ${record.response_status}` : `失败 ${record.response_status}`"
              />
            </template>
          </a-table-column>
          <a-table-column title="来源地址" data-index="remote_address" :width="190" />
          <a-table-column title="耗时" :width="95">
            <template #cell="{ record }">{{ formatDuration(record.duration_ms) }}</template>
          </a-table-column>
          <a-table-column title="" :width="70" fixed="right">
            <template #cell="{ record }">
              <a-button size="mini" type="text" @click.stop="openDetail(record)">详情</a-button>
            </template>
          </a-table-column>
        </template>
        <template #empty>
          <a-empty description="当前筛选条件下没有审计记录" />
        </template>
      </a-table>

      <div class="pagination-row">
        <span>第 {{ page }} 页，每页 {{ pageSize }} 条</span>
        <a-pagination
          :current="page"
          :page-size="pageSize"
          :total="total"
          show-total
          @change="changePage"
        />
      </div>
    </a-card>
  </div>

  <a-drawer
    v-model:visible="detailVisible"
    :width="600"
    title="审计事件详情"
    unmount-on-close
  >
    <a-descriptions v-if="selected" :column="1" bordered>
      <a-descriptions-item label="事件编号">#{{ selected.id }}</a-descriptions-item>
      <a-descriptions-item label="操作时间">{{ formatDate(selected.occurred_at, true) }}</a-descriptions-item>
      <a-descriptions-item label="操作用户">{{ selected.username }}</a-descriptions-item>
      <a-descriptions-item label="操作说明">{{ selected.summary }}</a-descriptions-item>
      <a-descriptions-item label="资源类型">{{ selected.resource || '平台接口' }}</a-descriptions-item>
      <a-descriptions-item label="操作目标">{{ selected.target || '—' }}</a-descriptions-item>
      <a-descriptions-item label="请求方法"><a-tag :color="methodColor(selected.method)">{{ selected.method }}</a-tag></a-descriptions-item>
      <a-descriptions-item label="请求路径"><code>{{ selected.path }}</code></a-descriptions-item>
      <a-descriptions-item label="执行结果">
        <a-badge
          :status="selected.successful ? 'success' : 'danger'"
          :text="selected.successful ? `成功（HTTP ${selected.response_status}）` : `失败或被拒绝（HTTP ${selected.response_status}）`"
        />
      </a-descriptions-item>
      <a-descriptions-item label="来源地址">{{ selected.remote_address }}</a-descriptions-item>
      <a-descriptions-item label="处理耗时">{{ formatDuration(selected.duration_ms) }}</a-descriptions-item>
    </a-descriptions>
  </a-drawer>
</template>

<script setup lang="ts">
import { onMounted, reactive, ref } from 'vue';
import { Message } from '@arco-design/web-vue';
import { IconRefresh, IconSearch } from '@arco-design/web-vue/es/icon';
import { api } from '@/services/api';
import type { AuditEvent, AuditEventPage } from '@/types';

const events = ref<AuditEvent[]>([]);
const selected = ref<AuditEvent | null>(null);
const detailVisible = ref(false);
const loading = ref(false);
const total = ref(0);
const page = ref(1);
const pageSize = 20;
const filters = reactive({
  username: '',
  method: '',
  result: '',
  period: '7d',
  keyword: '',
  includeSystem: false,
});

onMounted(loadEvents);

function auditStartTime() {
  const periods: Record<string, number> = {
    '24h': 24 * 60 * 60 * 1000,
    '7d': 7 * 24 * 60 * 60 * 1000,
    '30d': 30 * 24 * 60 * 60 * 1000,
  };
  const duration = periods[filters.period];
  return duration ? new Date(Date.now() - duration).toISOString() : '';
}

async function loadEvents() {
  loading.value = true;
  try {
    const params = new URLSearchParams({
      page: String(page.value),
      page_size: String(pageSize),
    });
    if (filters.username.trim()) params.set('username', filters.username.trim());
    if (filters.method) params.set('method', filters.method);
    if (filters.result) params.set('result', filters.result);
    if (filters.keyword.trim()) params.set('keyword', filters.keyword.trim());
    if (filters.includeSystem) params.set('include_system', 'true');
    const from = auditStartTime();
    if (from) params.set('from', from);

    const result = await api<AuditEventPage>(`/api/audit-events?${params.toString()}`);
    events.value = result.items || [];
    total.value = result.total || 0;
  } catch (error: any) {
    Message.error(error.message);
  } finally {
    loading.value = false;
  }
}

function applyFilters() {
  page.value = 1;
  void loadEvents();
}

function resetFilters() {
  Object.assign(filters, { username: '', method: '', result: '', period: '7d', keyword: '', includeSystem: false });
  page.value = 1;
  void loadEvents();
}

function changePage(value: number) {
  page.value = value;
  void loadEvents();
}

function openDetail(record: AuditEvent | Record<string, unknown>) {
  selected.value = record as AuditEvent;
  detailVisible.value = true;
}

function methodColor(method: string) {
  return ({ POST: 'green', PUT: 'arcoblue', PATCH: 'purple', DELETE: 'red' } as Record<string, string>)[method] || 'gray';
}

function formatDate(value: string, includeSeconds = false) {
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return value || '—';
  return new Intl.DateTimeFormat('zh-CN', {
    year: 'numeric',
    month: '2-digit',
    day: '2-digit',
    hour: '2-digit',
    minute: '2-digit',
    second: includeSeconds ? '2-digit' : undefined,
    hour12: false,
  }).format(date);
}

function formatDuration(value: number) {
  if (value < 1000) return `${value} ms`;
  return `${(value / 1000).toFixed(value < 10_000 ? 2 : 1)} s`;
}
</script>

<style scoped>
.audit-page { display: flex; flex-direction: column; gap: 16px; }
.filter-card, .table-card { border: 1px solid var(--color-border-2); }
.filter-grid { display: grid; grid-template-columns: 160px 170px 170px 160px minmax(260px, 1fr) auto; gap: 12px; align-items: center; }
.keyword-input { min-width: 0; }
.filter-actions { justify-self: end; white-space: nowrap; }
.time-cell, .target-cell { display: flex; flex-direction: column; gap: 4px; min-width: 0; }
.time-cell strong { font-size: 12px; }
.time-cell small, .target-cell small { overflow: hidden; color: var(--color-text-3); font-size: 11px; text-overflow: ellipsis; white-space: nowrap; }
.target-cell strong { font-size: 13px; }
.pagination-row { display: flex; align-items: center; justify-content: space-between; gap: 16px; margin-top: 18px; color: var(--color-text-3); font-size: 12px; }
code { padding: 3px 6px; border-radius: 4px; background: var(--color-fill-2); word-break: break-all; }
@media (max-width: 1280px) {
  .filter-grid { grid-template-columns: repeat(3, minmax(0, 1fr)); }
}
@media (max-width: 760px) {
  .filter-grid { grid-template-columns: 1fr; }
  .pagination-row { align-items: flex-start; flex-direction: column; }
}
</style>

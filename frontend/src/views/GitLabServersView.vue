<template>
  <div class="gitlab-page">
    <a-card class="page-hero">
      <div><span class="eyebrow">PLATFORM INTEGRATION</span><h2>GitLab 服务器</h2><p>统一管理交付 GitLab 和业务源码 GitLab。Token 加密保存且不会回显。</p></div>
      <a-button type="primary" @click="openServer()"><icon-plus />添加 GitLab</a-button>
    </a-card>
    <a-alert type="info" show-icon>每条连接可以授权多个根组。项目接入时会明确选择交付根组和业务源码根组；GitLab Token 需要 <code>api</code> 权限，且不会下发到 Jenkins。</a-alert>
    <a-card>
      <template #title>GitLab 连接目录</template>
      <template #extra><a-button :loading="loading" @click="load"><icon-refresh />刷新</a-button></template>
      <a-table :data="servers" :loading="loading" row-key="key" :pagination="false">
        <template #columns>
          <a-table-column title="服务器"><template #cell="{record}"><div class="primary-cell"><strong>{{ record.display_name }}</strong><span>{{ record.base_url }}</span><small>{{ record.key }}</small></div></template></a-table-column>
          <a-table-column title="授权根组"><template #cell="{record}"><div class="root-groups"><a-tag v-for="group in serverRootGroups(record)" :key="group" color="arcoblue">{{ group }}</a-tag></div><div class="muted">默认 {{ record.root_group }} · {{ record.default_branch }} · {{ record.visibility === 'private' ? '私有' : '内部' }}</div></template></a-table-column>
          <a-table-column title="连接状态" :width="140"><template #cell="{record}"><a-tooltip :content="record.last_check_error || ''"><a-tag :color="record.last_check_status === 'healthy' ? 'green' : record.last_check_status === 'failed' ? 'red' : 'gray'">{{ statusName(record.last_check_status) }}</a-tag></a-tooltip></template></a-table-column>
          <a-table-column title="操作" :width="250"><template #cell="{record}"><a-space><a-button size="mini" :loading="busy === record.key" @click="test(record)">测试</a-button><a-button size="mini" @click="openServer(record)">编辑</a-button><a-popconfirm content="只有未被项目绑定的 GitLab 服务器才能删除。" @ok="remove(record)"><a-button size="mini" status="danger">删除</a-button></a-popconfirm></a-space></template></a-table-column>
        </template>
        <template #empty><a-empty description="尚未配置 GitLab 服务器" /></template>
      </a-table>
    </a-card>
  </div>

  <a-modal v-model:visible="visible" :title="editing ? '编辑 GitLab 服务器' : '添加 GitLab 服务器'" width="680px" :ok-loading="saving" @before-ok="save">
    <a-form :model="form" layout="vertical">
      <a-grid :cols="2" :col-gap="16">
        <a-grid-item><a-form-item label="服务器标识" required extra="仅允许小写字母、数字和连字符。"><a-input v-model="form.key" :disabled="editing" /></a-form-item></a-grid-item>
        <a-grid-item><a-form-item label="显示名称" required><a-input v-model="form.display_name" /></a-form-item></a-grid-item>
      </a-grid>
      <a-form-item label="GitLab 地址" required extra="支持 GitLab 根地址或子路径，例如 https://gitlab.example.com。"><a-input v-model="form.base_url" /></a-form-item>
      <a-alert v-if="form.base_url.trim().startsWith('http://')" type="warning" show-icon class="form-alert">使用 HTTP 会明文传输 Access Token，仅允许在受信任内网中启用。<template #action><a-switch v-model="form.allow_insecure_http" checked-text="已确认" /></template></a-alert>
      <a-form-item label="授权根组" required>
        <div class="root-group-editor">
          <div class="root-group-editor-head">
            <div><strong>配置仓库访问范围</strong><span>根组及其子组项目会出现在“服务与清单”中</span></div>
            <a-tag :color="normalizeRootGroups(form.root_groups).length ? 'green' : 'gray'">{{ normalizeRootGroups(form.root_groups).length }} / 20</a-tag>
          </div>
          <a-input-tag v-model="form.root_groups" allow-clear placeholder="输入根组路径" />
          <div class="root-group-shortcuts"><span><kbd>Enter</kbd> 添加</span><span><kbd>,</kbd> 批量分隔</span><span><kbd>↵</kbd> 支持换行粘贴</span></div>
          <div v-if="normalizeRootGroups(form.root_groups).length" class="root-group-default"><span>默认根组</span><strong>{{ normalizeRootGroups(form.root_groups)[0] }}</strong><small>新项目默认使用，可在项目接入时调整</small></div>
        </div>
      </a-form-item>
      <a-grid :cols="2" :col-gap="16">
        <a-grid-item><a-form-item label="默认分支" required><a-input v-model="form.default_branch" /></a-form-item></a-grid-item>
        <a-grid-item><a-form-item label="仓库可见性" required><a-select v-model="form.visibility"><a-option value="private">私有（推荐）</a-option><a-option value="internal">内部可见</a-option></a-select></a-form-item></a-grid-item>
      </a-grid>
      <a-form-item label="Group / Project Access Token" :required="!editing" :extra="editing ? '留空表示保留已有 Token；建议 Owner + api，以便自动签发最小权限 Deploy Token。只有 read_repository 时平台会验证后采用兼容模式。' : '推荐 Owner + api；也支持无法管理 Deploy Token、但具有 read_repository 的只读 Token。GitLab API 不需要填写用户名。'"><a-input-password v-model="form.access_token" autocomplete="new-password" /></a-form-item>
    </a-form>
  </a-modal>
</template>

<script setup lang="ts">
import { onMounted, reactive, ref } from 'vue';
import { Message } from '@arco-design/web-vue';
import { IconPlus, IconRefresh } from '@arco-design/web-vue/es/icon';
import { api } from '@/services/api';

interface GitLabServer { key:string; display_name:string; base_url:string; root_group:string; root_groups?:string[]; default_branch:string; visibility:string; allow_insecure_http:boolean; configured:boolean; last_check_status?:string; last_check_error?:string }
const servers=ref<GitLabServer[]>([]);const loading=ref(false);const saving=ref(false);const visible=ref(false);const editing=ref(false);const busy=ref('');
const form=reactive({key:'',display_name:'',base_url:'',root_groups:[] as string[],default_branch:'main',visibility:'private',allow_insecure_http:false,access_token:''});
const load=async()=>{loading.value=true;try{servers.value=await api<GitLabServer[]>('/api/platform/gitlab/servers')}catch(error:any){Message.error(error.message)}finally{loading.value=false}};
onMounted(load);
function serverRootGroups(item:GitLabServer){return item.root_groups?.length?item.root_groups:[item.root_group].filter(Boolean)}
function normalizeRootGroups(values:string[]){return [...new Map(values.flatMap(value=>value.split(/[\s,，;；]+/)).map(value=>value.trim().replace(/^\/+|\/+$/g,'')).filter(Boolean).map(value=>[value.toLowerCase(),value])).values()]}
function openServer(item?:GitLabServer){editing.value=Boolean(item);Object.assign(form,{key:item?.key||'',display_name:item?.display_name||'',base_url:item?.base_url||'',root_groups:item?serverRootGroups(item):[],default_branch:item?.default_branch||'main',visibility:item?.visibility||'private',allow_insecure_http:item?.allow_insecure_http||false,access_token:''});visible.value=true}
async function save(){form.key=form.key.trim().toLowerCase();form.display_name=form.display_name.trim();form.base_url=form.base_url.trim();form.root_groups=normalizeRootGroups(form.root_groups);form.default_branch=form.default_branch.trim();if(!form.key||!form.display_name||!form.base_url||!form.root_groups.length||!form.default_branch||(!editing.value&&!form.access_token)){Message.warning('请补全 GitLab 服务器信息并至少添加一个授权根组');return false}if(form.base_url.startsWith('http://')&&!form.allow_insecure_http){Message.warning('使用 HTTP 前请确认内网风险');return false}saving.value=true;try{const path=editing.value?`/api/platform/gitlab/servers/${encodeURIComponent(form.key)}`:'/api/platform/gitlab/servers';await api(path,{method:editing.value?'PUT':'POST',body:JSON.stringify({...form,root_group:form.root_groups[0]})});form.access_token='';await load();Message.success('GitLab 服务器及授权根组已保存');return true}catch(error:any){Message.error(error.message);return false}finally{saving.value=false}}
async function test(item:GitLabServer){busy.value=item.key;try{await api(`/api/platform/gitlab/servers/${encodeURIComponent(item.key)}/test`,{method:'POST'});Message.success('GitLab Token 和全部授权根组均可用')}catch(error:any){Message.error(error.message)}finally{busy.value='';await load()}}
async function remove(item:GitLabServer){try{await api(`/api/platform/gitlab/servers/${encodeURIComponent(item.key)}`,{method:'DELETE'});await load();Message.success('GitLab 服务器已删除')}catch(error:any){Message.error(error.message)}}
const statusName=(value?:string)=>value==='healthy'?'正常':value==='failed'?'异常':'未检测';
</script>

<style scoped>
.gitlab-page{display:flex;flex-direction:column;gap:16px}.page-hero{background:linear-gradient(120deg,#f3f7ff,#fff 62%,#fff7ed);border:1px solid #dce6fb}.page-hero :deep(.arco-card-body){display:flex;align-items:center;justify-content:space-between;gap:24px}.page-hero h2{margin:6px 0 8px;font-size:24px}.page-hero p{margin:0;color:#687386}.eyebrow{font-size:11px;font-weight:700;letter-spacing:1.5px;color:#fc6b1d}.primary-cell{display:flex;flex-direction:column;gap:4px}.primary-cell span{color:#4e5969}.primary-cell small,.muted{font-size:12px;color:#86909c}.root-groups{display:flex;flex-wrap:wrap;gap:6px;margin-bottom:5px}.root-group-editor{width:100%;overflow:hidden;border:1px solid #d8e5f5;border-radius:10px;background:#fbfcff}.root-group-editor-head{padding:11px 13px;display:flex;align-items:center;justify-content:space-between;gap:12px;border-bottom:1px solid #e7edf7;background:linear-gradient(90deg,#f2f7ff,#fff)}.root-group-editor-head strong,.root-group-editor-head span{display:block}.root-group-editor-head strong{font-size:13px}.root-group-editor-head span{margin-top:2px;color:#86909c;font-size:11px}.root-group-editor :deep(.arco-input-tag){margin:12px 12px 7px;width:calc(100% - 24px);min-height:42px;border-color:#c9d7ed;background:#fff}.root-group-shortcuts{padding:0 12px 10px;display:flex;align-items:center;flex-wrap:wrap;gap:13px;color:#86909c;font-size:11px}.root-group-shortcuts kbd{min-width:22px;padding:2px 5px;display:inline-flex;justify-content:center;border:1px solid #c9d2e1;border-bottom-width:2px;border-radius:4px;color:#4e5969;background:#fff;font:10px ui-monospace,SFMono-Regular,Menlo,monospace}.root-group-default{padding:9px 12px;display:grid;grid-template-columns:auto auto minmax(0,1fr);align-items:center;gap:8px;border-top:1px dashed #d8e5f5;background:#f6fff8;font-size:11px}.root-group-default span{color:#4e5969}.root-group-default strong{padding:2px 7px;border-radius:5px;color:#0e7a3b;background:#dff7e5}.root-group-default small{color:#86909c;text-align:right}.form-alert{margin-bottom:16px}code{color:#165dff}@media(max-width:700px){.root-group-editor-head{align-items:flex-start}.root-group-default{grid-template-columns:1fr}.root-group-default small{text-align:left}}
</style>

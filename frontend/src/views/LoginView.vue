<template>
  <div class="login-page">
    <div class="login-brand"><strong>运维自动部署平台</strong></div>
    <section class="login-visual">
      <div class="visual-copy">
        <p>AWS INFRASTRUCTURE AUTOMATION</p>
        <h1>让基础设施交付<br />清晰、可控、可追踪</h1>
        <span>统一管理 Terraform、EKS 组件、任务日志与运行状态。</span>
      </div>
      <div class="visual-orbit" />
    </section>
    <section class="login-panel">
      <div class="login-box">
        <div class="login-heading"><h2>登录运维自动部署平台</h2><p>输入平台账号以继续访问</p></div>
        <a-alert v-if="errorMessage" type="error" :show-icon="true" closable @close="errorMessage = ''">{{ errorMessage }}</a-alert>
        <a-form ref="formRef" :model="form" layout="vertical" size="large" @submit-success="submit">
          <a-form-item field="username" label="用户名" :rules="[{ required: true, message: '请输入用户名' }]">
            <a-input v-model="form.username" placeholder="用户名" allow-clear><template #prefix><icon-user /></template></a-input>
          </a-form-item>
          <a-form-item field="password" label="密码" :rules="[{ required: true, message: '请输入密码' }]">
            <a-input-password v-model="form.password" placeholder="登录密码" allow-clear><template #prefix><icon-lock /></template></a-input-password>
          </a-form-item>
          <div class="login-security"><icon-safe /> 使用 Argon2id 与安全会话保护</div>
          <a-button html-type="submit" type="primary" long :loading="loading">登录</a-button>
        </a-form>
        <div class="login-footer"><span class="online-dot" /> 平台服务在线</div>
      </div>
    </section>
  </div>
</template>

<script setup lang="ts">
import { reactive, ref } from 'vue';
import { useRoute, useRouter } from 'vue-router';
import { Message } from '@arco-design/web-vue';
import { IconLock, IconSafe, IconUser } from '@arco-design/web-vue/es/icon';
import { useAuthStore } from '@/stores/auth';
import { APIError } from '@/services/api';

const route = useRoute();
const router = useRouter();
const auth = useAuthStore();
const form = reactive({ username: 'admin', password: '' });
const loading = ref(false);
const errorMessage = ref('');

const loginRedirect = () => {
  const candidate = typeof route.query.redirect === 'string' ? route.query.redirect : '';
  return candidate.startsWith('/') && !candidate.startsWith('//') && !candidate.startsWith('/login')
    ? candidate
    : '/projects';
};

const forceAuthenticatedReload = (redirect: string) => {
  // Hash-only location.replace() is a same-document navigation and cannot
  // recover a stalled lazy route or guard. Update the target synchronously,
  // then reload the document so the HttpOnly cookie is restored from scratch.
  const target = `${window.location.pathname}${window.location.search}#${redirect}`;
  window.history.replaceState(null, '', target);
  window.location.reload();
};

const navigateAfterLogin = async (redirect: string) => {
  let timer = 0;
  try {
    await Promise.race([
      router.replace(redirect),
      new Promise<never>((_, reject) => {
        timer = window.setTimeout(() => reject(new Error('navigation timeout')), 2_500);
      }),
    ]);
    if (router.currentRoute.value.path === '/login') forceAuthenticatedReload(redirect);
  } catch {
    forceAuthenticatedReload(redirect);
  } finally {
    window.clearTimeout(timer);
  }
};

const submit = async () => {
  if (loading.value) return;
  loading.value = true;
  errorMessage.value = '';
  try {
    await auth.login(form.username, form.password);
  } catch (error: any) {
    form.password = '';
    if (error instanceof APIError && error.status === 429) errorMessage.value = '登录尝试过多，请稍后再试。';
    else if (error instanceof APIError && error.status === 401) errorMessage.value = '用户名或密码错误。';
    else if (error?.name === 'AbortError') errorMessage.value = '登录请求超时，请检查网络后重试。';
    else errorMessage.value = '登录请求失败，请检查网络或平台服务状态。';
    loading.value = false;
    return;
  }
  form.password = '';
  const redirect = loginRedirect();
  Message.success({ content: '登录成功，正在进入平台…', duration: 1800 });
  loading.value = false;
  await new Promise((resolve) => window.setTimeout(resolve, 300));
  await navigateAfterLogin(redirect);
};
</script>

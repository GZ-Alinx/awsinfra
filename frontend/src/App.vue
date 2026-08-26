<template><router-view /></template>

<script setup lang="ts">
import { onMounted, onUnmounted } from 'vue';
import { useRouter } from 'vue-router';
import { Message } from '@arco-design/web-vue';
import { useAuthStore } from '@/stores/auth';

const router = useRouter();
const auth = useAuthStore();
const expired = () => {
  auth.$patch({ session: null });
  Message.warning('登录会话已过期，请重新登录');
  router.replace({ name: 'login' });
};
const navigationError = (event: Event) => {
  const detail = (event as CustomEvent<{ message?: string }>).detail;
  Message.error(detail?.message || '页面打开失败，请重试');
};
const offline = () => Message.warning('当前网络已断开，恢复连接后可继续操作');
const online = () => Message.success('网络连接已恢复');
onMounted(() => {
  window.addEventListener('ops:session-expired', expired);
  window.addEventListener('ops:navigation-error', navigationError);
  window.addEventListener('offline', offline);
  window.addEventListener('online', online);
});
onUnmounted(() => {
  window.removeEventListener('ops:session-expired', expired);
  window.removeEventListener('ops:navigation-error', navigationError);
  window.removeEventListener('offline', offline);
  window.removeEventListener('online', online);
});
</script>

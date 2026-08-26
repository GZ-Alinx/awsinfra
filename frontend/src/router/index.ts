import { createRouter, createWebHashHistory } from 'vue-router';
import { ref } from 'vue';
import { useAuthStore } from '@/stores/auth';

export const navigationPending = ref(false);
export const navigationTarget = ref('');

const router = createRouter({
  history: createWebHashHistory(),
  routes: [
    { path: '/login', name: 'login', component: () => import('@/views/LoginView.vue'), meta: { public: true } },
    {
      path: '/', component: () => import('@/layouts/DefaultLayout.vue'),
      children: [
		{ path: '', redirect: '/projects' },
		{ path: 'projects', name: 'projects', component: () => import('@/views/ProjectsView.vue') },
		{ path: 'aws-connection', name: 'aws-connection', component: () => import('@/views/AWSConnectionView.vue') },
		{ path: 'terraform-state', name: 'terraform-state', component: () => import('@/views/TerraformStateView.vue'), meta: { manageCredentials: true } },
		{ path: 'gitlab-servers', name: 'gitlab-servers', component: () => import('@/views/GitLabServersView.vue'), meta: { manageCredentials: true } },
        { path: 'overview', name: 'overview', component: () => import('@/views/OverviewView.vue') },
        { path: 'observability', name: 'observability', component: () => import('@/views/ApplicationObservabilityView.vue') },
        { path: 'environment', name: 'environment', component: () => import('@/views/EnvironmentView.vue') },
        { path: 'ingresses', name: 'ingresses', component: () => import('@/views/IngressView.vue') },
        { path: 'static-cdn', name: 'static-cdn', component: () => import('@/views/StaticCDNView.vue') },
        { path: 'resources', name: 'resources', component: () => import('@/views/ResourcesView.vue') },
        { path: 'jobs', name: 'jobs', component: () => import('@/views/JobsView.vue') },
		{ path: 'cicd', name: 'cicd', component: () => import('@/views/CICDView.vue') },
		{ path: 'components', name: 'components', component: () => import('@/views/ComponentsView.vue') },
		{ path: 'users', name: 'users', component: () => import('@/views/UsersView.vue'), meta: { manageUsers: true } },
		{ path: 'audit-events', name: 'audit-events', component: () => import('@/views/AuditEventsView.vue'), meta: { viewAudit: true } },
      ],
    },
  ],
});

router.beforeEach(async (to) => {
  navigationPending.value = true;
  navigationTarget.value = String(to.name || to.path);
  const auth = useAuthStore();
  if (!auth.initialized) await auth.restore();
  if (!to.meta.public && !auth.session) return { name: 'login', query: { redirect: to.fullPath } };
	if (to.meta.manageUsers && !auth.canManageUsers) return { name: 'projects' };
	if (to.meta.viewAudit && !auth.canViewAudit) return { name: 'projects' };
	if (to.meta.manageCredentials && !auth.canManageCredentials) return { name: 'projects' };
	if (to.name === 'login' && auth.session) return { name: 'projects' };
});

router.afterEach(() => {
  navigationPending.value = false;
  navigationTarget.value = '';
  sessionStorage.removeItem('ops-route-recovery');
});

router.onError((error, to) => {
  navigationPending.value = false;
  navigationTarget.value = '';
  const message = String(error?.message || error);
  const staleChunk = /dynamically imported module|module script|Loading chunk|ChunkLoadError|text\/html.*module/i.test(message);
  if (staleChunk) {
    const lastRecovery = Number(sessionStorage.getItem('ops-route-recovery') || 0);
    if (!Number.isFinite(lastRecovery) || Date.now() - lastRecovery > 15_000) {
      sessionStorage.setItem('ops-route-recovery', String(Date.now()));
      const target = to?.fullPath || '/projects';
      window.history.replaceState(null, '', `${window.location.pathname}${window.location.search}#${target}`);
      window.location.reload();
      return;
    }
  }
  window.dispatchEvent(new CustomEvent('ops:navigation-error', { detail: { message: '页面打开失败，请检查网络后重试', technical: message } }));
});

export default router;

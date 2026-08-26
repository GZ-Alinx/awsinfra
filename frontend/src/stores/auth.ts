import { defineStore } from 'pinia';
import { api, setCSRFToken } from '@/services/api';
import type { Session, UserInfo } from '@/types';

export const useAuthStore = defineStore('auth', {
  state: () => ({ session: null as Session | null, initialized: false }),
  getters: {
    canManageProjects: (state) => Boolean(state.session?.is_admin || state.session?.platform_permissions?.can_manage_projects),
    canManageUsers: (state) => Boolean(state.session?.is_admin || state.session?.platform_permissions?.can_manage_users),
    canManageCredentials: (state) => Boolean(state.session?.is_admin || state.session?.platform_permissions?.can_manage_credentials),
    canManageComponents: (state) => Boolean(state.session?.is_admin || state.session?.platform_permissions?.can_manage_components),
    canViewAudit: (state) => Boolean(state.session?.is_admin || state.session?.platform_permissions?.can_view_audit),
    hasPlatformAccess(): boolean {
      return this.canManageProjects || this.canManageUsers || this.canManageCredentials || this.canManageComponents || this.canViewAudit;
    },
  },
  actions: {
    async restore() {
      try {
        this.session = await api<Session>('/api/auth/session');
        setCSRFToken(this.session.csrf_token);
      } catch {
        this.session = null;
        setCSRFToken('');
      } finally {
        this.initialized = true;
      }
    },
    async login(username: string, password: string) {
      const controller = new AbortController();
      const timeout = window.setTimeout(() => controller.abort(), 15_000);
      try {
        this.session = await api<Session>('/api/auth/login', {
          method: 'POST', body: JSON.stringify({ username, password }), signal: controller.signal,
        });
        setCSRFToken(this.session.csrf_token);
        this.initialized = true;
      } finally {
        window.clearTimeout(timeout);
      }
    },
    async logout() {
      try { await api<void>('/api/auth/logout', { method: 'POST' }); } finally {
        this.session = null;
        setCSRFToken('');
      }
    },
    async updateProfile(displayName: string) {
      const response = await api<{ user: UserInfo; session: Session }>('/api/me/profile', {
        method: 'PUT', body: JSON.stringify({ display_name: displayName }),
      });
      this.session = response.session;
      setCSRFToken(response.session.csrf_token);
      return response.user;
    },
    async changePassword(currentPassword: string, newPassword: string) {
      await api<void>('/api/me/password', {
        method: 'PUT', body: JSON.stringify({ current_password: currentPassword, new_password: newPassword }),
      });
      this.session = null;
      setCSRFToken('');
    },
  },
});

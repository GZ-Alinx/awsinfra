import { defineStore } from 'pinia';
import { api } from '@/services/api';
import type {
  AWSCredentialInfo, Dict, HealthInfo, HelmComponent, HelmInspectResult, HelmVersionResult, Job, JobAction, PlatformInfo, Project, ProjectEnvironment, ResourceSnapshot, StatusReport, TLSCertificateInfo,
} from '@/types';

const plainClone = <T>(value: T): T => JSON.parse(JSON.stringify(value));

type ScopePrefetch = {
  projectKey: string;
  environmentKey: string;
  config: Promise<Dict | null>;
  jobs: Promise<Job[] | null>;
  tlsCertificates: Promise<TLSCertificateInfo[] | null>;
};

const prefetchScope = (projectKey: string, environmentKey: string): ScopePrefetch => {
  const project = encodeURIComponent(projectKey);
  const environment = encodeURIComponent(environmentKey);
  return {
    projectKey,
    environmentKey,
    config: api<Dict>(`/api/projects/${project}/environments/${environment}`).catch(() => null),
    jobs: api<Job[]>(`/api/jobs?project=${project}&environment=${environment}`).catch(() => null),
    tlsCertificates: api<TLSCertificateInfo[]>(`/api/projects/${project}/environments/${environment}/tls-certificates`).catch(() => null),
  };
};

const mergeComponentCatalog = (config: Dict, catalog: HelmComponent[]) => {
  config.components ||= {};
  config.components.catalog ||= {};
  for (const item of catalog) {
    if (config.components.catalog[item.key]) continue;
    config.components.catalog[item.key] = {
      enabled: false,
      display_name: item.display_name,
      category: item.category,
      repository: item.repository,
      chart: item.chart,
      chart_version: item.chart_version,
      release_name: item.key,
      namespace: item.default_namespace,
      deployment_mode: 'standalone',
      replicas: 1,
      replica_paths: plainClone(item.replica_paths || []),
      service_name: '',
      service_port: 80,
      protocol: 'http',
      username: '',
      secret_name: '',
      secret_key: '',
      domain: '',
      tls: false,
      timeout: 1200,
      values: plainClone(item.values || {}),
    };
  }
  return config;
};

export const usePlatformStore = defineStore('platform', {
  state: () => ({
    initialized: false,
    platform: null as PlatformInfo | null,
    health: null as HealthInfo | null,
    projects: [] as Project[],
    currentProjectKey: localStorage.getItem('ops-current-project') || '',
    currentEnvironmentKey: localStorage.getItem('ops-current-environment') || '',
    config: null as Dict | null,
    status: null as StatusReport | null,
    jobs: [] as Job[],
    resources: null as ResourceSnapshot | null,
	awsCredential: null as AWSCredentialInfo | null,
    awsCredentials: [] as AWSCredentialInfo[],
    componentCatalog: [] as HelmComponent[],
    tlsCertificates: [] as TLSCertificateInfo[],
    loadingEnvironment: false,
    loadingJobs: false,
    loadingStatus: false,
    loadingResources: false,
    loadingTLSCertificates: false,
    // Every project/environment switch receives a new revision. Async
    // responses may update the store only while their revision is current.
    scopeRevision: 0,
  }),
  getters: {
    currentProject(state): Project | undefined {
      return state.projects.find((item) => item.key === state.currentProjectKey);
    },
    currentEnvironment(state): ProjectEnvironment | undefined {
      return state.projects.find((item) => item.key === state.currentProjectKey)
        ?.environments.find((item) => item.environment === state.currentEnvironmentKey);
    },
    scopeKey(state): string {
      return state.currentProjectKey && state.currentEnvironmentKey
        ? `${state.currentProjectKey}/${state.currentEnvironmentKey}`
        : '';
    },
    currentName(): string { return this.currentEnvironment?.target_name || ''; },
    canDeploy(): boolean { return Boolean(this.currentProject?.permission.can_deploy); },
    canConfigure(): boolean { return Boolean(this.currentProject?.permission.can_configure); },
    canViewSecrets(): boolean { return Boolean(this.currentProject?.permission.can_view_secrets); },
    environmentLabel() {
      return (key: string) => this.platform?.environment_definitions.find((item) => item.key === key)?.display_name || key;
    },
  },
  actions: {
    async initialize() {
      try {
        const storedProject = this.currentProjectKey;
        const storedEnvironment = this.currentEnvironmentKey;
        const scopePrefetch = storedProject && storedEnvironment ? prefetchScope(storedProject, storedEnvironment) : null;
        const healthRequest = api<HealthInfo>('/api/health').catch(() => null);
        // Start secondary catalogs immediately, but never keep the environment
        // form behind them. They reconcile into the active scope when ready.
        const awsCredentialsRequest = api<AWSCredentialInfo[]>('/api/aws-credentials').catch(() => null);
        const componentCatalogRequest = api<HelmComponent[]>('/api/component-catalog').catch(() => null);
        const [platform, projects, prefetchedConfig] = await Promise.all([
          api<PlatformInfo>('/api/platform'),
          api<Project[]>('/api/projects'),
          scopePrefetch?.config || Promise.resolve(null),
        ]);
        this.platform = platform;
        this.projects = projects;
        void healthRequest.then((health) => { if (health) this.health = health; });

        const projectKeys = projects.map((item) => item.key);
        const projectKey = [storedProject, projectKeys[0]].find((key) => key && projectKeys.includes(key)) || '';
        if (!projectKey) {
          this.clearScope();
          return;
        }
        const project = projects.find((item) => item.key === projectKey)!;
        const environments = project.environments.map((item) => item.environment);
        const environmentKey = [storedEnvironment, environments[0]].find((key) => key && environments.includes(key as any)) || '';
        this.currentProjectKey = projectKey;
        this.awsCredential = null;
        localStorage.setItem('ops-current-project', projectKey);

        const prefetchedScopeIsValid = Boolean(
          scopePrefetch && prefetchedConfig && scopePrefetch.projectKey === projectKey && scopePrefetch.environmentKey === environmentKey,
        );
        if (environmentKey && prefetchedScopeIsValid && scopePrefetch && prefetchedConfig) {
          const revision = this.scopeRevision + 1;
          this.scopeRevision = revision;
          this.currentEnvironmentKey = environmentKey;
          this.config = mergeComponentCatalog(prefetchedConfig, this.componentCatalog);
          this.jobs = [];
          this.status = null;
          this.resources = null;
          this.tlsCertificates = [];
          this.loadingEnvironment = false;
          this.loadingJobs = true;
          this.loadingTLSCertificates = true;
          localStorage.setItem('ops-current-environment', environmentKey);
          void scopePrefetch.jobs.then((items) => {
            if (this.scopeRevision !== revision) return;
            if (items) {
              this.jobs = items;
              this.loadingJobs = false;
            } else void this.loadJobs().catch(() => undefined);
          });
          void scopePrefetch.tlsCertificates.then((items) => {
            if (this.scopeRevision !== revision) return;
            if (items) {
              this.tlsCertificates = items;
              this.loadingTLSCertificates = false;
            } else void this.loadTLSCertificates().catch(() => undefined);
          });
          void Promise.allSettled([this.loadStatus(), this.loadResources()]);
        } else if (environmentKey) {
          await this.selectScope(projectKey, environmentKey);
        } else {
          this.clearEnvironment();
        }
        void awsCredentialsRequest.then((items) => {
          if (!items) return;
          this.awsCredentials = items;
          this.awsCredential = items.find((item) => item.project_key === this.currentProjectKey && item.selected) || null;
        });
        void componentCatalogRequest.then((items) => {
          if (!items) return;
          this.componentCatalog = items;
          if (this.config) mergeComponentCatalog(this.config, items);
        });
      } finally {
        this.initialized = true;
      }
    },
    async loadHealth() {
      const health = await api<HealthInfo>('/api/health');
      this.health = health;
      return health;
    },
    async loadProjects(preferredProject = '', preferredEnvironment = '') {
      this.projects = await api<Project[]>('/api/projects');
      const projectKeys = this.projects.map((item) => item.key);
      const projectKey = [preferredProject, this.currentProjectKey, projectKeys[0]]
        .find((key) => key && projectKeys.includes(key)) || '';
      if (!projectKey) {
        this.clearScope();
        return;
      }
      const project = this.projects.find((item) => item.key === projectKey)!;
      const environments = project.environments.map((item) => item.environment);
      const environmentKey = [preferredEnvironment, this.currentEnvironmentKey, environments[0]]
        .find((key) => key && environments.includes(key as any)) || '';
      this.currentProjectKey = projectKey;
      this.awsCredential = null;
      localStorage.setItem('ops-current-project', projectKey);
      if (environmentKey) {
        await this.selectScope(projectKey, environmentKey);
        void this.loadAWSCredential().catch(() => undefined);
      }
      else {
        this.clearEnvironment();
        void this.loadAWSCredential().catch(() => undefined);
      }
    },
	async refreshProjects() {
	  // Refresh lifecycle badges without reloading the selected environment's
	  // large editable configuration and operational snapshots.
	  this.projects = await api<Project[]>('/api/projects');
	},
    async selectProject(projectKey: string) {
      const project = this.projects.find((item) => item.key === projectKey);
      if (!project) return;
      this.currentProjectKey = projectKey;
      this.awsCredential = null;
      localStorage.setItem('ops-current-project', projectKey);
      const environment = project.environments.find((item) => item.environment === this.currentEnvironmentKey)
        || project.environments[0];
      if (environment) {
        await this.selectScope(projectKey, environment.environment);
        void this.loadAWSCredential().catch(() => undefined);
      }
      else {
        this.clearEnvironment();
        void this.loadAWSCredential().catch(() => undefined);
      }
    },
    async selectEnvironment(environmentKey: string) {
      if (!this.currentProjectKey) return;
      await this.selectScope(this.currentProjectKey, environmentKey);
    },
    async selectScope(projectKey: string, environmentKey: string) {
      const project = this.projects.find((item) => item.key === projectKey);
      const environment = project?.environments.find((item) => item.environment === environmentKey);
      if (!project || !environment) return;
      const revision = this.scopeRevision + 1;
      this.scopeRevision = revision;
      this.currentProjectKey = projectKey;
      this.currentEnvironmentKey = environmentKey;
      // Never render configuration, jobs, status, resources, or credentials
      // from the previously selected environment while the new scope loads.
      this.config = null;
      this.jobs = [];
      this.status = null;
      this.resources = null;
      this.tlsCertificates = [];
      localStorage.setItem('ops-current-project', projectKey);
      localStorage.setItem('ops-current-environment', environmentKey);
      this.loadingEnvironment = true;
      // These snapshots are independent of the editable document. Starting
      // them together removes the old config -> status -> resources waterfall;
      // none of them can overwrite another scope because every action carries
      // the revision captured above.
      const contextRequest = Promise.allSettled([
        this.loadJobs(), this.loadTLSCertificates(), this.loadStatus(), this.loadResources(),
      ]);
      try {
        const config = await api<Dict>(`/api/projects/${encodeURIComponent(projectKey)}/environments/${encodeURIComponent(environmentKey)}`);
        if (this.scopeRevision === revision) this.config = mergeComponentCatalog(config, this.componentCatalog);
      } finally {
        if (this.scopeRevision === revision) this.loadingEnvironment = false;
      }
      // Task history, certificates, status and resource snapshots are useful
      // context, but none of them should block the editable environment form.
      void contextRequest;
    },
    async createProject(project: Pick<Project, 'key' | 'display_name' | 'description'>) {
	  const created = await api<Project>('/api/projects', { method: 'POST', body: JSON.stringify(project) });
	  await this.loadProjects(created.key);
	  return created;
    },
    async updateProject(projectKey: string, project: Pick<Project, 'display_name' | 'description'>) {
      const updated = await api<Project>(`/api/projects/${encodeURIComponent(projectKey)}`, {
        method: 'PUT', body: JSON.stringify(project),
      });
      const index = this.projects.findIndex((item) => item.key === projectKey);
      if (index >= 0) this.projects[index] = updated;
      else this.projects.push(updated);
      return updated;
    },
	async createEnvironment(environment: string, sourceProject: string, sourceEnvironment: string, targetType = 'managed', existingClusterName = '', region = '') {
      const projectKey = this.currentProjectKey;
      await api(`/api/projects/${encodeURIComponent(projectKey)}/environments`, {
        method: 'POST',
		body: JSON.stringify({ environment, source_project: sourceProject, source_environment: sourceEnvironment, target_type: targetType, existing_cluster_name: existingClusterName, region }),
      });
      await this.loadProjects(projectKey, environment);
    },
    async saveEnvironment(config: Dict) {
      const projectKey = this.currentProjectKey; const environmentKey = this.currentEnvironmentKey; const revision = this.scopeRevision;
		const saved = await api<Dict>(`/api/projects/${encodeURIComponent(projectKey)}/environments/${encodeURIComponent(environmentKey)}`, {
        method: 'PUT', body: JSON.stringify(config),
      });
      if (this.scopeRevision === revision) {
        this.config = mergeComponentCatalog(saved, this.componentCatalog);
        const environment = this.currentProject?.environments.find((item) => item.environment === environmentKey);
        if (environment) environment.region = String(saved.region || environment.region || '');
        // The write response is authoritative. Lifecycle badges and the
        // certificate summary are secondary reads and must not delay the
        // save-and-deploy path.
        void Promise.allSettled([this.refreshProjects(), this.loadTLSCertificates()]);
      }
    },
    async deleteEnvironment(options: { destroyResources?: boolean; destroyConfirm?: string; password?: string } = {}): Promise<Job | null> {
      const projectKey = this.currentProjectKey;
      const environmentKey = this.currentEnvironmentKey;
      const job = await api<Job | undefined>(`/api/projects/${encodeURIComponent(projectKey)}/environments/${encodeURIComponent(environmentKey)}`, {
		method: 'DELETE', body: JSON.stringify({
			confirm: `${projectKey}/${environmentKey}`,
			destroy_resources: Boolean(options.destroyResources),
			destroy_confirm: options.destroyConfirm || '',
			password: options.password || '',
		}),
      });
		if (job) return job;
      this.clearEnvironment();
      await this.loadProjects(projectKey);
		return null;
    },
    async loadStatus(fresh = false) {
      if (!this.currentProjectKey || !this.currentEnvironmentKey) return;
      const projectKey = this.currentProjectKey; const environmentKey = this.currentEnvironmentKey; const revision = this.scopeRevision;
      this.loadingStatus = true;
      try {
        const status = await api<StatusReport>(`/api/projects/${encodeURIComponent(projectKey)}/environments/${encodeURIComponent(environmentKey)}/status${fresh ? '?fresh=true' : ''}`);
        if (this.scopeRevision === revision) this.status = status;
      } finally {
        if (this.scopeRevision === revision) this.loadingStatus = false;
      }
    },
    async loadJobs() {
      if (!this.currentProjectKey || !this.currentEnvironmentKey) return;
      const projectKey = this.currentProjectKey; const environmentKey = this.currentEnvironmentKey; const revision = this.scopeRevision;
      this.loadingJobs = true;
      try {
        const jobs = await api<Job[]>(`/api/jobs?project=${encodeURIComponent(projectKey)}&environment=${encodeURIComponent(environmentKey)}`);
        if (this.scopeRevision === revision) this.jobs = jobs;
      } finally {
        if (this.scopeRevision === revision) this.loadingJobs = false;
      }
    },
    async loadResources(fresh = false) {
      if (!this.currentProjectKey || !this.currentEnvironmentKey) return;
      const projectKey = this.currentProjectKey; const environmentKey = this.currentEnvironmentKey; const revision = this.scopeRevision;
      this.loadingResources = true;
      try {
        const resources = await api<ResourceSnapshot>(`/api/projects/${encodeURIComponent(projectKey)}/environments/${encodeURIComponent(environmentKey)}/resources${fresh ? '?fresh=true' : ''}`);
        if (this.scopeRevision === revision) this.resources = resources;
      } finally {
        if (this.scopeRevision === revision) this.loadingResources = false;
      }
    },
    async loadAWSConfiguration() {
      if (!this.currentProjectKey || !this.currentEnvironmentKey) return;
      const projectKey = this.currentProjectKey; const environmentKey = this.currentEnvironmentKey; const revision = this.scopeRevision;
      this.loadingResources = true;
      try {
        const resources = await api<ResourceSnapshot>(`/api/projects/${encodeURIComponent(projectKey)}/environments/${encodeURIComponent(environmentKey)}/resources?cloud_only=true`);
        if (this.scopeRevision === revision) this.resources = resources;
      } finally {
        if (this.scopeRevision === revision) this.loadingResources = false;
      }
    },
    async syncEnvironmentAWSConfiguration() {
      if (!this.currentProjectKey || !this.currentEnvironmentKey) throw new Error('请先选择项目环境');
      const projectKey = this.currentProjectKey; const environmentKey = this.currentEnvironmentKey; const revision = this.scopeRevision;
      this.loadingResources = true;
      try {
        const response = await api<{ config: Dict; resources: ResourceSnapshot }>(`/api/projects/${encodeURIComponent(projectKey)}/environments/${encodeURIComponent(environmentKey)}/resources/sync-aws`, {
          method: 'POST', body: '{}',
        });
        if (this.scopeRevision === revision) {
          this.config = mergeComponentCatalog(response.config, this.componentCatalog);
          this.resources = response.resources;
        }
        return response;
      } finally {
        if (this.scopeRevision === revision) this.loadingResources = false;
      }
    },
    async loadTLSCertificates() {
      if (!this.currentProjectKey || !this.currentEnvironmentKey) return;
      const projectKey = this.currentProjectKey; const environmentKey = this.currentEnvironmentKey; const revision = this.scopeRevision;
      this.loadingTLSCertificates = true;
      try {
        const items = await api<TLSCertificateInfo[]>(`/api/projects/${encodeURIComponent(projectKey)}/environments/${encodeURIComponent(environmentKey)}/tls-certificates`);
        if (this.scopeRevision === revision) this.tlsCertificates = items;
      } finally {
        if (this.scopeRevision === revision) this.loadingTLSCertificates = false;
      }
    },
    async saveTLSCertificate(key: string, certificatePEM: string, privateKeyPEM: string) {
      const projectKey = this.currentProjectKey; const environmentKey = this.currentEnvironmentKey; const revision = this.scopeRevision;
      const saved = await api<TLSCertificateInfo>(`/api/projects/${encodeURIComponent(projectKey)}/environments/${encodeURIComponent(environmentKey)}/tls-certificates/${encodeURIComponent(key)}`, {
        method: 'PUT', body: JSON.stringify({ certificate_pem: certificatePEM, private_key_pem: privateKeyPEM }),
      });
      if (this.scopeRevision === revision) {
        const index = this.tlsCertificates.findIndex((item) => item.key === saved.key);
        if (index >= 0) this.tlsCertificates[index] = saved; else this.tlsCertificates.push(saved);
      }
      return saved;
    },
    async deleteTLSCertificate(key: string) {
      const projectKey = this.currentProjectKey; const environmentKey = this.currentEnvironmentKey; const revision = this.scopeRevision;
      await api(`/api/projects/${encodeURIComponent(projectKey)}/environments/${encodeURIComponent(environmentKey)}/tls-certificates/${encodeURIComponent(key)}`, { method: 'DELETE' });
      if (this.scopeRevision === revision) this.tlsCertificates = this.tlsCertificates.filter((item) => item.key !== key);
    },
	async createJob(action: JobAction, confirm = '', password = '') {
      const job = await api<Job>('/api/jobs', {
        method: 'POST',
		body: JSON.stringify({ project: this.currentProjectKey, environment: this.currentEnvironmentKey, action, confirm, password }),
      });
      const existing = this.jobs.findIndex((item) => item.id === job.id);
      if (existing >= 0) this.jobs[existing] = job;
      else this.jobs = [job, ...this.jobs];
      // The POST response already contains the newly persisted task. Reconcile
      // history in the background so navigation to live logs is immediate.
      void this.loadJobs().catch(() => undefined);
      return job;
    },
	async loadAWSCredential() {
	  const projectKey = this.currentProjectKey;
	  this.awsCredential = null;
	  if (!projectKey) return;
      const known = this.awsCredentials.find((item) => item.project_key === projectKey && item.selected);
      const credential = known || await api<AWSCredentialInfo>(`/api/projects/${encodeURIComponent(projectKey)}/aws-credentials`);
	  if (this.currentProjectKey !== projectKey || credential.project_key !== projectKey) return;
      this.awsCredential = credential;
      const existing = this.awsCredentials.findIndex((item) => item.key === credential.key && item.project_key === projectKey);
      if (existing >= 0) this.awsCredentials[existing] = credential;
      else if (credential.configured) this.awsCredentials.push(credential);
	},
	async loadAWSCredentials() {
      this.awsCredentials = await api<AWSCredentialInfo[]>('/api/aws-credentials');
      this.awsCredential = this.awsCredentials.find((item) => item.project_key === this.currentProjectKey && item.selected) || null;
    },
	async saveAWSCredential(input: { access_key_id: string; secret_access_key: string; session_token?: string; region: string; password: string }) {
	  this.awsCredential = await api<AWSCredentialInfo>(`/api/projects/${encodeURIComponent(this.currentProjectKey)}/aws-credentials`, { method: 'PUT', body: JSON.stringify(input) });
	  await Promise.all([this.loadAWSCredentials(), this.refreshProjects()]);
	},
	async deleteAWSCredential(password: string) {
	  await api(`/api/projects/${encodeURIComponent(this.currentProjectKey)}/aws-credentials`, { method: 'DELETE', body: JSON.stringify({ password }) });
	  await Promise.all([this.loadAWSCredentials(), this.refreshProjects()]);
	},
	async saveNamedAWSCredential(input: { key: string; display_name: string; project_key: string; access_key_id: string; secret_access_key: string; session_token?: string; region: string; password: string }) {
      await api<AWSCredentialInfo>('/api/aws-credentials', { method: 'POST', body: JSON.stringify(input) });
      await Promise.all([this.loadAWSCredentials(), this.refreshProjects()]);
    },
	async deleteNamedAWSCredential(key: string, password: string) {
      await api(`/api/aws-credentials/${encodeURIComponent(key)}`, { method: 'DELETE', body: JSON.stringify({ password }) });
      await Promise.all([this.loadAWSCredentials(), this.refreshProjects()]);
    },
	async selectAWSCredential(projectKey: string, credentialKey: string) {
      await api<AWSCredentialInfo>(`/api/projects/${encodeURIComponent(projectKey)}/aws-credential-selection`, {
        method: 'PUT', body: JSON.stringify({ credential_key: credentialKey }),
      });
      await Promise.all([this.loadAWSCredentials(), this.refreshProjects()]);
    },
    async loadComponentCatalog() {
      this.componentCatalog = await api<HelmComponent[]>('/api/component-catalog');
      if (this.config) mergeComponentCatalog(this.config, this.componentCatalog);
    },
    async inspectHelmComponent(input: Pick<HelmComponent, 'repository' | 'chart' | 'chart_version'>) {
      return api<HelmInspectResult>('/api/component-catalog/inspect', { method: 'POST', body: JSON.stringify(input) });
    },
    async loadHelmComponentVersions(input: Pick<HelmComponent, 'repository' | 'chart' | 'chart_version'>) {
      return api<HelmVersionResult>('/api/component-catalog/versions', { method: 'POST', body: JSON.stringify(input) });
    },
    async saveHelmComponent(component: HelmComponent, updating = false) {
      const endpoint = updating ? `/api/component-catalog/${encodeURIComponent(component.key)}` : '/api/component-catalog';
      await api<HelmComponent>(endpoint, { method: updating ? 'PUT' : 'POST', body: JSON.stringify(component) });
      await this.loadComponentCatalog();
    },
    async deleteHelmComponent(key: string) {
      await api(`/api/component-catalog/${encodeURIComponent(key)}`, { method: 'DELETE' });
      await this.loadComponentCatalog();
    },
    clearEnvironment() {
      this.scopeRevision += 1;
      this.currentEnvironmentKey = '';
      this.config = null;
      this.status = null;
      this.resources = null;
      this.jobs = [];
      this.tlsCertificates = [];
      this.loadingEnvironment = false;
      this.loadingJobs = false;
      this.loadingStatus = false;
      this.loadingResources = false;
      this.loadingTLSCertificates = false;
      localStorage.removeItem('ops-current-environment');
    },
    clearScope() {
      this.currentProjectKey = '';
	  this.awsCredential = null;
      localStorage.removeItem('ops-current-project');
      this.clearEnvironment();
    },
  },
});

export type Dict = Record<string, any>;

export interface PlatformPermission {
  can_manage_projects: boolean;
  can_manage_users: boolean;
  can_manage_credentials: boolean;
  can_manage_components: boolean;
  can_view_audit: boolean;
}

export interface Session {
  username: string;
  display_name: string;
  is_admin: boolean;
  platform_permissions: PlatformPermission;
  csrf_token: string;
  expires_at: string;
}

export interface AuditEvent {
  id: number;
  occurred_at: string;
  username: string;
  method: 'POST' | 'PUT' | 'PATCH' | 'DELETE' | string;
  path: string;
  response_status: number;
  remote_address: string;
  duration_ms: number;
  operation: string;
  resource: string;
  target: string;
  summary: string;
  successful: boolean;
}

export interface AuditEventPage {
  items: AuditEvent[];
  total: number;
  page: number;
  page_size: number;
}

export interface HealthInfo {
  status: 'ok' | 'degraded';
  version: string;
  time: string;
  dependencies: Record<string, string>;
}

export interface StaticCDNResource {
  project_key: string;
  environment_key: string;
  display_name: string;
  bucket_name: string;
  region: string;
  cors_origins: string[];
  distribution_id?: string;
  distribution_arn?: string;
  domain_name?: string;
  cdn_url?: string;
  oac_id?: string;
  status: string;
  last_error?: string;
  created_by: string;
  created_at: string;
  updated_at: string;
}

export interface StaticCDNObject {
  key: string;
  size: number;
  etag: string;
  last_modified: string;
  cdn_url: string;
}

export interface StaticCDNUploadAuthorization {
  key: string;
  method: 'PUT';
  upload_url: string;
  cdn_url: string;
  expires_at: string;
}

export interface ComponentConfig {
  key: string;
  display_name: string;
  category: string;
  description: string;
  config_path: string;
  stage: string;
  status_type: string;
  status_name: string;
  hidden: boolean;
  kind?: string;
}

export interface PlatformInfo {
  version: string;
  components: ComponentConfig[];
  auth_required: boolean;
  aws_profile: string;
  max_parallel: number;
  environment_definitions: EnvironmentDefinition[];
  aws_regions: AWSRegion[];
}

export interface AWSRegion {
  code: string;
  name: string;
  availability_zones: number;
  opt_in: boolean;
}

export interface AWSCredentialInfo {
  key?: string;
  display_name?: string;
  project_key?: string;
  configured: boolean;
  source: 'project-encrypted-credential' | 'project-credential-required' | string;
  masked_access_key?: string;
  account_id?: string;
  principal_arn?: string;
  principal_user_id?: string;
  profile?: string;
  verified_at?: string;
  updated_by?: string;
  created_at?: string;
  updated_at?: string;
  selected: boolean;
	project_archived: boolean;
}

export interface TerraformStateCenterConfig {
  configured: boolean;
  enabled: boolean;
  bucket?: string;
  region?: string;
  key_prefix?: string;
  kms_key_id?: string;
  masked_access_key?: string;
  account_id?: string;
  principal_arn?: string;
  updated_by?: string;
  verified_at?: string;
  updated_at?: string;
}

export interface TerraformStateLocation {
  project: string;
  environment: string;
  stage: 'infra' | 'platform' | string;
  backend: string;
  bucket: string;
  region: string;
  object_key: string;
  lineage: string;
  serial: number;
  managed_resources: number;
  updated_at: string;
}

export interface TerraformStateCenter {
  config: TerraformStateCenterConfig;
  states: TerraformStateLocation[];
}

export interface HelmComponent {
  key: string;
  display_name: string;
  category: string;
  description: string;
  repository: string;
  chart: string;
  chart_version: string;
  default_namespace: string;
  replica_paths: string[];
  values_yaml: string;
  values: Dict;
  created_by: string;
  created_at: string;
  updated_at: string;
}

export interface HelmInspectResult {
  repository: string;
  chart: string;
  chart_version: string;
  values_yaml: string;
  values: Dict;
	filtered_sensitive_paths?: string[];
}

export interface HelmVersionOption {
  version: string;
  app_version?: string;
  description?: string;
}

export interface HelmVersionResult {
  repository: string;
  chart: string;
  versions: HelmVersionOption[];
}

export interface TLSCertificateInfo {
  key: string;
  configured: boolean;
  fingerprint: string;
  subject: string;
  dns_names: string[];
  not_before: string;
  not_after: string;
  updated_by: string;
  updated_at: string;
}

export interface EKSVersionInfo {
  version: string;
  patch_version?: string;
  default: boolean;
  status: 'STANDARD_SUPPORT' | 'EXTENDED_SUPPORT' | 'UNSUPPORTED' | string;
  default_platform_version?: string;
  release_date?: string;
  end_of_standard_support_date?: string;
  end_of_extended_support_date?: string;
}

export interface EKSVersionResponse {
  region: string;
  source: 'aws-live';
  versions: EKSVersionInfo[];
}

export interface EKSClusterInfo {
  name: string;
}

export interface EKSClusterResponse {
  region: string;
  source: 'aws-live';
  clusters: EKSClusterInfo[];
}

export interface AWSVPCSubnetInfo {
  id: string;
  name?: string;
  cidr: string;
  availability_zone: string;
  available_ip_count: number;
  map_public_ip_on_launch: boolean;
}

export interface AWSVPCInfo {
  id: string;
  name?: string;
  cidr: string;
  default: boolean;
  state: string;
  subnets: AWSVPCSubnetInfo[];
}

export interface AWSVPCResponse {
  region: string;
  source: 'aws-live';
  vpcs: AWSVPCInfo[];
}

export interface AWSSecurityGroupInfo {
  id: string;
  name: string;
  display_name?: string;
  vpc_id: string;
  description?: string;
  ingress_source_count: number;
  allows_http: boolean;
  allows_https: boolean;
  public_http: boolean;
  public_https: boolean;
  selectable: boolean;
  blocked_reason?: string;
  platform_managed_guard: boolean;
}

export interface AWSSecurityGroupResponse {
  region: string;
  vpc_id?: string;
  source: 'aws-live';
  security_groups: AWSSecurityGroupInfo[];
}

export interface EC2InstanceTypeInfo {
  name: string;
  current_generation: boolean;
  vcpu: number;
  memory_mib: number;
  architectures: string[];
  network_performance: string;
  maximum_network_interfaces: number;
  ebs_optimized_support: string;
  instance_storage_supported: boolean;
  burstable: boolean;
  usage_classes: string[];
}

export interface EC2InstanceTypeResponse {
  region: string;
  query: string;
  source: 'aws-live';
  instance_types: EC2InstanceTypeInfo[];
}

export interface AWSServiceInstanceOption {
  value: string;
  engine_versions?: string[];
  deployment_modes?: string[];
  availability_zones?: string[];
  multi_az_capable?: boolean;
  storage_types?: string[];
}

export interface AWSServiceInstanceTypeResponse {
  region: string;
  service: string;
  source: 'aws-live';
  instance_types: AWSServiceInstanceOption[];
}

export interface AWSEngineVersionResponse {
  region: string;
  service: string;
  engine?: string;
  versions: string[];
  source: string;
}

export interface EnvironmentDefinition {
  key: 'dev' | 'test' | 'uat' | 'prod';
  display_name: string;
  order: number;
}

export interface ProjectPermission {
  project_key: string;
  username?: string;
  can_view: boolean;
  can_deploy: boolean;
  can_configure: boolean;
  can_view_secrets: boolean;
}

export interface ProjectEnvironment {
  project_key: string;
  environment: EnvironmentDefinition['key'];
  display_name: string;
  target_name: string;
  region: string;
  lifecycle_status?: EnvironmentLifecycleStatus;
  lifecycle_detail?: string;
  lifecycle_updated_at?: string;
  latest_job_id?: string;
  latest_job_action?: JobAction;
  latest_job_status?: JobStatus;
  latest_job_progress?: number;
  phase_one_deployed: boolean;
  phase_two_deployed: boolean;
  created_at: string;
}

export type EnvironmentLifecycleStatus =
  | 'ready' | 'queued' | 'validating' | 'planning' | 'deploying' | 'configuring'
  | 'updating' | 'running' | 'destroying' | 'destroyed' | 'validation_failed'
  | 'plan_failed' | 'deployment_failed' | 'component_failed' | 'destroy_failed'
  | 'canceled' | 'abnormal';

export interface Project {
  key: string;
  display_name: string;
  description: string;
  selected_aws_credential_key: string;
  environments: ProjectEnvironment[];
  permission: ProjectPermission;
  created_at: string;
  updated_at: string;
}

export interface UserInfo {
  username: string;
  display_name: string;
  is_admin: boolean;
  active: boolean;
  platform_permissions: PlatformPermission;
  permissions: ProjectPermission[];
  created_at: string;
  updated_at: string;
}

export interface EnvironmentSummary {
  name: string;
  project: string;
  region: string;
  cluster_name: string;
  components: string[];
}

export type JobStatus = 'queued' | 'running' | 'succeeded' | 'failed' | 'canceled' | 'ignored';
export type JobAction = 'validate' | 'plan' | 'deploy' | 'platform' | 'access' | 'tls' | 'storage_expand' | 'storage_shrink' | 'destroy';

export interface Job {
  id: string;
  project: string;
  environment: string;
  target_name?: string;
  requested_by: string;
  action: JobAction;
  status: JobStatus;
  created_at: string;
  started_at?: string;
  finished_at?: string;
  error?: string;
  failure_hint?: string;
  diagnosis?: JobDiagnosis;
  ignored_at?: string;
  ignored_by?: string;
  ignore_reason?: string;
  log_size: number;
  progress: number;
  total_steps: number;
  success_steps: number;
  failed_steps: number;
  current_step?: string;
  steps: JobStep[];
  completion_action?: 'delete_environment';
  parameters?: Record<string, string>;
}

export interface ManagedStorage {
  component: 'kafka' | 'clickhouse' | 'mysql' | string;
  namespace: string;
  pvc_name: string;
  requested: string;
  capacity: string;
  phase: string;
  storage_class: string;
  allow_expansion: boolean;
  workload_kind: string;
  workload_name: string;
  volume_name: string;
  active: boolean;
  retained: boolean;
}

export interface ManagedStorageReport {
  project: string;
  environment: string;
  observed_at: string;
  items: ManagedStorage[];
}

export interface JobDiagnosis {
  code: string;
  title: string;
  stage: string;
  cause: string;
  impact: string;
  suggestion: string;
  retry: string;
}

export interface JobStep {
  name: string;
  status: 'pending' | 'running' | 'succeeded' | 'failed';
  started_at?: string;
  finished_at?: string;
  error?: string;
}

export interface StatusReport {
  project: string;
  environment: string;
  environment_name: string;
  target_name: string;
  observed_at: string;
  cluster: { name: string; status: string; version?: string; endpoint?: string; target_type?: 'managed' | 'existing_eks'; reachable: boolean };
  nodes: Array<{ name: string; ready: boolean; instance_type?: string; zone?: string; version?: string }>;
  pods: { total: number; ready: number; pending: number; failed: number; unhealthy: Array<{ namespace: string; name: string; phase: string; reason?: string }> };
  components: Array<{ key: string; display_name: string; category: string; desired: boolean; actual: boolean; status: string; detail?: string }>;
  releases: Array<{ name: string; namespace: string; status: string; chart: string; app_version: string }>;
  outputs: Dict;
  warnings: string[];
}

export type ApplicationHealthState = 'normal' | 'warning' | 'abnormal';

export interface ApplicationTopologyNode {
  id: string;
  name: string;
  namespace: string;
  kind: 'Ingress' | 'Service' | 'Deployment' | 'StatefulSet' | 'DaemonSet' | string;
  layer: 'entry' | 'application' | 'data' | 'observability' | string;
  state: ApplicationHealthState;
  state_reason: string;
  desired_replicas?: number;
  ready_replicas?: number;
  pods?: number;
  ready_pods?: number;
  restarts?: number;
  cpu_cores?: number;
  memory_bytes?: number;
  ready_endpoints?: number;
  total_endpoints?: number;
  services: string[];
  ports: Array<{ name?: string; port: number; app_protocol?: string }>;
  hosts: string[];
  labels?: Record<string, string>;
}

export interface ApplicationTopologyEdge {
  id: string;
  source: string;
  target: string;
  relation: 'ingress_route' | 'service_selector' | 'endpoint' | 'runtime_request' | string;
  protocol?: string;
  label?: string;
  evidence: string;
  verified: boolean;
  state: ApplicationHealthState;
  ready_endpoints?: number;
  total_endpoints?: number;
  request_rate?: number;
}

export interface ApplicationTopologyAlert {
  name: string;
  severity: string;
  state: ApplicationHealthState;
  namespace?: string;
  workload?: string;
  pod?: string;
  service?: string;
  summary?: string;
  description?: string;
  started_at?: string;
}

export interface ApplicationTopology {
  project: string;
  environment: string;
  environment_name: string;
  target_name: string;
  observed_at: string;
  source: { kubernetes: boolean; prometheus: boolean; runtime_graph: boolean; detail: string; connection_detail: string };
  summary: {
    normal: number;
    warning: number;
    abnormal: number;
    total: number;
    connections: number;
    runtime_connections: number;
    endpoint_connections: number;
    declared_connections: number;
  };
  nodes: ApplicationTopologyNode[];
  edges: ApplicationTopologyEdge[];
  alerts: ApplicationTopologyAlert[];
  warnings: string[];
}

export interface ResourceAccessPoint {
  name: string;
  type: string;
  visibility: 'public' | 'private' | string;
  protocol: string;
  host?: string;
  port?: number;
  url?: string;
  description?: string;
}

export interface ResourceCredential {
  id: string;
  label: string;
  username?: string;
  provider: string;
  available: boolean;
}

export interface EnvironmentResource {
  key: string;
  display_name: string;
  category: string;
  source: 'cloud' | 'self-hosted';
  provider: string;
  status: string;
  version?: string;
  specification?: string;
  namespace?: string;
  access_points: ResourceAccessPoint[];
  credentials: ResourceCredential[];
  metadata: Dict;
  configuration?: Array<{
    path: string;
    label: string;
    desired?: unknown;
    actual?: unknown;
    state: 'synced' | 'pending' | 'drifted' | 'conflict' | string;
    syncable: boolean;
  }>;
}

export interface ResourceSnapshot {
  project: string;
  environment: string;
  observed_at: string;
  cloud_sync: {
    status: 'synced' | 'pending' | 'drifted' | 'conflict' | 'unavailable' | string;
    observed_at?: string;
    synced_fields: number;
    pending_fields: number;
    drifted_fields: number;
    conflict_fields: number;
    unavailable_resources: number;
    blocking_changes: boolean;
  };
  info: {
    aws_account_id?: string;
    region: string;
    availability_zones: string[];
    vpc_id?: string;
    vpc_cidr: string;
    cluster_name: string;
    cluster_endpoint?: string;
    namespaces: string[];
    public_subnets: Record<string, string>;
    private_subnets: Record<string, string>;
    network_mode: string;
    nat_gateway_mode: string;
    nat_gateway_ips: Record<string, string>;
  };
  resources: EnvironmentResource[];
  warnings: string[];
}

export interface KubernetesIngressPath {
  host?: string;
  path: string;
  path_type?: string;
  service_name: string;
  service_namespace?: string;
  service_port: string;
}

export interface KubernetesIngress {
  name: string;
  namespace: string;
  class_name: string;
  resource_version: string;
  hosts: string[];
  paths: KubernetesIngressPath[];
  tls_secrets: string[];
  addresses: string[];
  creation_timestamp?: string;
  managed_by?: string;
  backend_protocol?: string;
  sync_status: 'synced' | 'pending' | 'drifted' | 'conflict' | 'cluster-only';
  desired: boolean;
  config_index?: number;
}

export interface KubernetesIngressDocument {
  ingress: KubernetesIngress;
  yaml: string;
}

export interface KubernetesIngressValidation {
  valid: boolean;
  ingress: KubernetesIngress;
  normalized_yaml: string;
  warnings: string[];
}

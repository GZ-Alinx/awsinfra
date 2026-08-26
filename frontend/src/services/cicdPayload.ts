export interface ParameterDefinitionDraft {
  _id?: string;
  name: string;
  type: 'string' | 'choice' | 'number' | 'boolean';
  default_value: string;
  choices: string[];
  description: string;
  required: boolean;
}

export interface ParameterDefinitionPayload {
  name: string;
  type: 'string' | 'choice' | 'number' | 'boolean';
  default_value: string;
  choices: string[];
  description: string;
  required: boolean;
}

export interface CICDBuildTriggerInput {
  environment: string;
  branch: string;
  image_tag: string;
  services: readonly string[];
  default_branches?: Readonly<Record<string, string>>;
  parameters: Record<string, string>;
}

export interface CICDSingleServiceBuildTriggerInput extends Omit<CICDBuildTriggerInput, 'services'> {
  services: [string];
}

const writableJobFields = [
  'key', 'environment_key', 'display_name', 'service_name', 'service_keys', 'language',
  'jenkinsfile_mode', 'execution_mode', 'failure_policy', 'compact_parameters', 'connection_key',
  'jenkins_job_name', 'enabled', 'trigger_mode', 'trigger_branch', 'jenkinsfile_repository', 'jenkinsfile_repo',
  'jenkinsfile_branch', 'jenkinsfile_path', 'jenkinsfile_content', 'jenkinsfile_credential',
  'source_repository', 'source_repo', 'manifest_repository', 'manifest_repo',
  'manifest_branch', 'manifest_path', 'manifest_credential', 'build_command',
  'runtime_version',
] as const;

export function parameterDefinitionsPayload(items: readonly ParameterDefinitionDraft[]): ParameterDefinitionPayload[] {
  return items.map((item) => ({
    name: item.name,
    type: item.type,
    default_value: item.default_value,
    choices: [...(item.choices || [])],
    description: item.description,
    required: item.required,
  }));
}

// Build an explicit API DTO instead of spreading the reactive form. This keeps
// table-only fields (such as `_id`) and any future UI state out of the strict Go
// JSON contract.
export function createCICDJobPayload<T extends object>(
  form: T,
  environmentPaths: Record<string, string>,
  parameters: Record<string, string>,
): Record<string, unknown> {
  const source = form as Record<string, unknown>;
  const payload: Record<string, unknown> = {};
  for (const field of writableJobFields) payload[field] = source[field];
  payload.environment_paths = { ...environmentPaths };
  payload.parameters = { ...parameters };
  payload.parameter_definitions = source.jenkinsfile_mode === 'generated' || source.compact_parameters
    ? []
    : parameterDefinitionsPayload((source.parameter_definitions || []) as ParameterDefinitionDraft[]);
  return payload;
}

// Jenkins exposes a single-choice service parameter. The platform still offers
// multi-select by fanning one platform request out into one Jenkins build per
// service, which preserves independent logs, retries and build results.
export function createSingleServiceBuildRequests(input: CICDBuildTriggerInput): CICDSingleServiceBuildTriggerInput[] {
  const services = [...new Set(input.services.map((service) => service.trim()).filter(Boolean))];
  return services.map((service) => ({
    environment: input.environment,
    branch: input.branch.trim() || input.default_branches?.[service]?.trim() || '',
    image_tag: input.image_tag,
    services: [service],
    parameters: { ...input.parameters },
  }));
}

import assert from 'node:assert/strict';
import test from 'node:test';
import { createCICDJobPayload, createSingleServiceBuildRequests } from '../src/services/cicdPayload.ts';

test('CI/CD Job payload strips UI-only and response-only fields', () => {
  const form = {
    key: 'game-release',
		environment_key: 'test',
    display_name: 'Game Release',
    service_name: 'gateway',
    service_keys: ['gateway'],
    language: 'go',
    jenkinsfile_mode: 'generated',
    execution_mode: 'serial',
    failure_policy: 'stop',
    connection_key: 'test-jenkins',
    jenkins_job_name: 'game-release',
    enabled: true,
		trigger_mode: 'gitlab_push',
		trigger_branch: 'main',
    jenkinsfile_repository: 'ops-delivery-jenkinsfiles',
    jenkinsfile_repo: 'https://git.example/jenkinsfiles.git',
    jenkinsfile_branch: 'main',
    jenkinsfile_path: 'jobs/game-release/Jenkinsfile',
		jenkinsfile_content: 'pipeline { agent any }',
    jenkinsfile_credential: 'gitlab-read',
    source_repository: '',
    source_repo: '',
    manifest_repository: 'ops-delivery-manifests',
    manifest_repo: 'https://git.example/manifests.git',
    manifest_branch: 'main',
    manifest_path: 'environments',
    manifest_credential: 'gitlab-read',
    build_command: '',
    runtime_version: '1.24',
    parameter_definitions: [{
      _id: 'ui-row-1', name: 'RELEASE_KIND', type: 'choice' as const,
      default_value: 'full', choices: ['full', 'config-only'],
      description: '发布类型', required: true,
    }],
    // These fields must never be accepted from the editing form.
    parameters_text: 'SHOULD_NOT_LEAK=yes',
    agent_mode: 'kubernetes',
    sync_status: 'ready',
		webhook_configured: true,
		webhook_secret_hash: 'must-never-leave-the-server',
    project_key: 'another-project',
    _dialog_state: true,
  };

  const payload = createCICDJobPayload(form, { test: 'environments/test' }, { JENKINS_AGENT_MODE: 'kubernetes' });
  const encoded = JSON.stringify(payload);
  assert.equal(encoded.includes('"_id"'), false);
  assert.equal(encoded.includes('parameters_text'), false);
  assert.equal(encoded.includes('agent_mode'), false);
  assert.equal(encoded.includes('sync_status'), false);
	assert.equal(encoded.includes('webhook_configured'), false);
	assert.equal(encoded.includes('webhook_secret_hash'), false);
  assert.equal(encoded.includes('project_key'), false);
	assert.equal(payload.trigger_mode, 'gitlab_push');
	assert.equal(payload.trigger_branch, 'main');
	assert.equal(payload.environment_key, 'test');
	assert.equal(payload.jenkinsfile_content, 'pipeline { agent any }');
	// 平台生成模式只允许“构建服务 + 代码分支”，不把历史
	// 自定义参数送回后端或 Jenkins。
	assert.deepEqual(payload.parameter_definitions, []);
  assert.equal(form.parameter_definitions[0]._id, 'ui-row-1');
});

test('existing Jenkinsfile mode preserves declared parameters', () => {
	const payload = createCICDJobPayload({
		key: 'existing-release', jenkinsfile_mode: 'existing',
		parameter_definitions: [{
			_id: 'ui-row-2', name: 'RELEASE_KIND', type: 'choice' as const,
			default_value: 'full', choices: ['full', 'config-only'],
			description: '发布类型', required: true,
		}],
	}, {}, {});
	assert.deepEqual(payload.parameter_definitions, [{
		name: 'RELEASE_KIND', type: 'choice', default_value: 'full',
		choices: ['full', 'config-only'], description: '发布类型', required: true,
	}]);
});

test('compact existing Jenkinsfile mode exposes only service and branch', () => {
	const payload = createCICDJobPayload({
		key: 'compact-release', jenkinsfile_mode: 'existing', compact_parameters: true,
		parameter_definitions: [{
			_id: 'ui-row-3', name: 'DEPLOY_ENV', type: 'choice' as const,
			default_value: 'test', choices: ['test'], description: '环境', required: true,
		}],
	}, {}, { DEPLOY_ENV: 'test' });
	assert.equal(payload.compact_parameters, true);
	assert.deepEqual(payload.parameter_definitions, []);
});

test('platform multi-select fans out to one Jenkins build per service', () => {
	const requests = createSingleServiceBuildRequests({
		environment: 'test', branch: '', image_tag: 'v1',
		services: ['gateway', 'sevenup', 'gateway'], default_branches: { gateway: 'main', sevenup: 'test' },
		parameters: { RELEASE_KIND: 'full' },
	});
	assert.deepEqual(requests.map((item) => item.services), [['gateway'], ['sevenup']]);
	assert.deepEqual(requests.map((item) => item.branch), ['main', 'test']);
	assert.notEqual(requests[0].parameters, requests[1].parameters);
});

test('explicit branch overrides every selected service default', () => {
	const requests = createSingleServiceBuildRequests({
		environment: 'uat', branch: 'release/2026-07', image_tag: 'v2',
		services: ['gateway', 'sevenup'], default_branches: { gateway: 'main', sevenup: 'test' }, parameters: {},
	});
	assert.deepEqual(requests.map((item) => item.branch), ['release/2026-07', 'release/2026-07']);
});

import assert from 'node:assert/strict';
import test from 'node:test';

import { deploymentActionState, type DeploymentActionInput } from '../src/services/deploymentAction.ts';

const defaults: DeploymentActionInput = {
  stage: 2,
  deployed: true,
  dirty: false,
  canConfigure: true,
  canDeploy: true,
  awsCredentialReady: true,
  baseReady: true,
  environmentBusy: false,
  existingEKSTarget: false,
};

test('unsaved domain or component changes enable save-and-deploy', () => {
  const state = deploymentActionState({ ...defaults, dirty: true });

  assert.equal(state.disabled, false);
  assert.equal(state.requiresSave, true);
  assert.equal(state.label, '保存并更新部署【阶段二】');
});

test('first phase deployment uses the same save-before-deploy behavior', () => {
  const state = deploymentActionState({ ...defaults, stage: 1, deployed: false, dirty: true });

  assert.equal(state.disabled, false);
  assert.equal(state.label, '保存并开始部署【阶段一】');
});

test('an active task still prevents concurrent deployment for the same environment', () => {
  const state = deploymentActionState({ ...defaults, dirty: true, environmentBusy: true });

  assert.equal(state.disabled, true);
  assert.match(state.reason, /任务结束后/);
});

test('dirty configuration requires configure permission before deployment', () => {
  const state = deploymentActionState({ ...defaults, dirty: true, canConfigure: false });

  assert.equal(state.disabled, true);
  assert.match(state.reason, /配置修改权限/);
});

test('saved configuration does not require configure permission to deploy', () => {
  const state = deploymentActionState({ ...defaults, canConfigure: false });

  assert.equal(state.disabled, false);
  assert.equal(state.requiresSave, false);
});

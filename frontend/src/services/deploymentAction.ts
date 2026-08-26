export type DeploymentStage = 1 | 2;

export type DeploymentActionInput = {
  stage: DeploymentStage;
  deployed: boolean;
  dirty: boolean;
  canConfigure: boolean;
  canDeploy: boolean;
  awsCredentialReady: boolean;
  baseReady: boolean;
  environmentBusy: boolean;
  existingEKSTarget: boolean;
};

export type DeploymentActionState = {
  disabled: boolean;
  requiresSave: boolean;
  label: string;
  reason: string;
};

// Environment configuration is edited as one document. Domain routes,
// components, cloud services, namespaces and alerting therefore use the same
// save-before-deploy rule instead of maintaining fragile per-tab flags.
export function deploymentActionState(input: DeploymentActionInput): DeploymentActionState {
  const stageName = input.stage === 1 ? '一' : '二';
  const actionName = input.deployed ? '更新部署' : '开始部署';
  const baseLabel = `${actionName}【阶段${stageName}】`;
  const label = input.dirty ? `保存并${baseLabel}` : baseLabel;

  if (!input.canDeploy) {
    return { disabled: true, requiresSave: input.dirty, label, reason: '当前用户没有该项目的部署权限' };
  }
  if (input.dirty && !input.canConfigure) {
    return { disabled: true, requiresSave: true, label, reason: '当前配置已修改，但用户没有配置修改权限' };
  }
  if (!input.awsCredentialReady) {
    return { disabled: true, requiresSave: input.dirty, label, reason: '请先绑定并选中当前项目的 AWS 凭据' };
  }
  if (input.environmentBusy) {
    return { disabled: true, requiresSave: input.dirty, label, reason: '当前环境已有部署任务在执行，任务结束后可保存并更新部署' };
  }
  if (input.stage === 2 && !input.baseReady) {
    return {
      disabled: true,
      requiresSave: input.dirty,
      label,
      reason: input.existingEKSTarget ? '已有 EKS 接入检查尚未通过' : '阶段二需要等待 EKS 正常运行',
    };
  }
  return {
    disabled: false,
    requiresSave: input.dirty,
    label,
    reason: input.dirty ? '点击后会先保存当前全部修改，再创建部署任务' : '',
  };
}

# 安全策略

## 支持范围

安全修复优先覆盖最新的 `main` 和最新发布版本。公开版本尚未承诺长期维护旧分支。

## 报告漏洞

请使用 GitHub 仓库的 **Security → Report a vulnerability** 私密报告功能，不要在公开 Issue 中粘贴漏洞、凭据或客户信息。报告请包含受影响版本、复现条件、影响、最小复现和建议修复；维护者会在 3 个工作日内确认收到。

## 生产安全基线

- 平台自身应置于私网或可信反向代理后，只通过 HTTPS 暴露；设置 `cookie_secure: true` 和准确的 `external_origin`。
- 管理员密码至少 12 位，生产建议接入企业身份系统并限制平台管理员人数。
- `OPS_DEPLOY_CREDENTIAL_KEY` 必须由密码管理系统托管并备份。丢失该密钥将无法解密已保存凭据；泄露后需轮换全部受影响凭据。
- 项目 AWS 权限优先使用独立 AssumeRole 和短期 STS。不得在镜像、YAML、Git、日志或 Terraform 变量文件中保存长期 AK/SK。
- Terraform State 使用独立 S3 Bucket、版本控制、SSE-KMS、公共访问阻断和最小权限；生产环境应启用对象锁或跨账号备份。
- MySQL、Redis、Jenkins、Grafana、数据库和消息队列不应直接暴露到公网。公网入口应经过网关、TLS、鉴权、白名单和审计。
- 启用 CloudTrail、GuardDuty、AWS Config、EKS 控制面日志、Kubernetes 审计与平台审计日志。
- 部署前审查 Terraform Plan；生产销毁、缩容、Namespace 删除和凭据修改必须执行双人复核。

## 仓库防泄漏

仓库忽略 `.env`、`secrets.env`、kubeconfig、State、运行数据、环境实例和本机发布配置。忽略规则不是安全边界：提交前仍应运行 Secret 扫描，并检查 `git diff --cached`。一旦凭据进入 Git 历史，应先在供应商处吊销/轮换，再清理历史；只删除文件不足以消除风险。

依赖漏洞由 GitHub Dependabot、CodeQL、Go vulnerability database 和 npm audit 持续检查。

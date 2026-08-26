# 运维手册

## 每日检查

- `/api/health` 及 MySQL、Redis 依赖状态；
- 队列中、运行中、超时和失败任务；
- Terraform State 最近写入、版本和锁；
- EKS 节点/Pod/PVC、网关 Target、证书到期时间；
- AWS 配额剩余值、成本异常、CloudTrail 和安全告警；
- 数据库备份和跨账号 State 备份是否成功。

## 备份优先级

必须同时备份：

1. 平台 MySQL（项目、权限、凭据密文、配置、任务元数据）；
2. `OPS_DEPLOY_CREDENTIAL_KEY`（独立密码管理系统，双人恢复）；
3. Terraform State S3 全版本和 KMS Key 可恢复性；
4. 项目 GitLab 的 Jenkinsfile、Dockerfile、部署清单；
5. 平台数据 PVC 中的日志/归档（按保留策略）；
6. TLS 私钥和外部服务凭据（仅在批准的 Secret 系统）。

MySQL 与主密钥缺少任一项都不能完整恢复 AWS 凭据。每季度在隔离环境执行一次恢复演练。

## 升级流程

1. 阅读 Changelog，锁定不可变镜像 Tag。
2. 备份并验证恢复点。
3. 在测试环境运行全部自动测试和一次真实 Plan。
4. 生产维护窗口执行滚动升级。
5. 验证登录、权限、凭据解密、项目环境、任务日志、State 和 AWS/EKS 查询。
6. 观察一个完整发布周期后结束变更。

## 故障处理

先保存证据，不要立即删除 PVC、Namespace、State 或数据库：

```console
kubectl get pods -n ops-deploy-system -o wide
kubectl get events -n ops-deploy-system --sort-by=.lastTimestamp
kubectl logs deployment/ops-deploy-platform -n ops-deploy-system --tail=300
kubectl describe pvc -n ops-deploy-system
```

任务失败先处理日志中的第一个实际错误。Terraform 超时不等于资源未创建；重新 Apply 前必须刷新 State 和云端实际状态。锁只在确认没有执行器存活后才能解除。

## 销毁

- 接入已有 EKS：只卸载平台创建且带所有权标签的组件，不删除集群、VPC、共享 Namespace 或未知资源。
- 平台新建环境：销毁前列出资源、备份 State/数据、验证项目/账号/Region/环境四元组。
- 删除项目不删除凭据；存在运行资源时必须先完成资源清理或经过显式保留确认。
- Namespace 默认受保护；只有单独的强制流程并确认 Namespace 内全部对象归属时才允许删除。

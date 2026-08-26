# 环境从 0 到 1 的交付标准

本标准把“点部署按钮”改造成可审计的交付流水线。每个门禁结果只能是 `PASS`、`WARN`、`BLOCK` 或 `MANUAL`，证据写入同一个交付报告。

## 阶段与门禁

| 门禁 | 检查内容 | 阻断条件 |
|---|---|---|
| G0 配置完整性 | 项目、环境、账号、Region、CIDR、AZ、节点组、数据/组件、成本标签 | 缺字段、CIDR 冲突、生产安全项未确认 |
| G1 AWS 实时预检 | STS、权限、Region 可用性、Service Quotas、已有资源和漂移 | 身份错误；剩余 vCPU < 96；剩余 EIP < 5；关键权限缺失 |
| G2 计划审查 | 刷新 State、Terraform Plan、费用/删除/替换摘要 | 非预期 Destroy/Replace、State 不可用、控制台冲突未裁决 |
| G3 阶段 1 | VPC、NAT、EKS、节点组、ECR、云数据库/中间件 | 资源失败或没有进入权威 State |
| G4 EKS 验收 | Add-on、DNS、存储、网络、出网、调度、扩缩容、审计日志 | CoreDNS/CSI/网络不可用，Pod 无法调度或出网 |
| G5 阶段 2 | 组件、可观测、网关、TLS、域名、告警 | 关键组件不健康、Target 不健康、TLS/路由错误 |
| G6 业务验收 | CI/CD、健康检查、日志/指标/Trace、告警测试、恢复演练 | 发布不可追溯、监控告警未闭环 |
| G7 交付 | 访问清单、账号、架构、成本、备份、Runbook、责任人 | 文档或恢复证据缺失 |

配额判断使用剩余值：`remaining = effective_quota - current_usage - pending_reservations`，不能把总配额当剩余配额。AWS API 无法取得实时用量时标记 `MANUAL` 并阻断生产 Apply。

## 基础配置标准

- 项目 ID 稳定，显示名称可修改；所有资源包含项目、环境、Owner、Cost Center 标签。
- `dev/test/uat/prod` 账号或角色权限隔离；生产凭据、Jenkins、State 和数据库独立。
- VPC 至少跨 3 AZ。公共/私有子网用途由环境配置明确，生产节点和数据库优先私网，统一 NAT 出网。
- 节点组用途标签标准化：`ingress-gateway`、`business-workload`、`platform-ops`；专用组用 label/taint/toleration 强隔离。
- EKS API 公网 CIDR 与 AWS 现有配置合并，不能覆盖控制台新增白名单。
- 默认启用 EBS CSI、VPC CNI、CoreDNS、kube-proxy、Pod Identity；版本与集群兼容。
- 所有持久数据定义 StorageClass、容量、备份、保留时间、扩容方式和恢复目标。

## EKS 创建后 Checklist

```console
kubectl get nodes -L ops-deploy.io/workload-class
kubectl get pods -A -o wide
kubectl get storageclass
kubectl get csidriver
kubectl get events -A --sort-by=.lastTimestamp
```

- 三个 AZ 节点均 Ready，标签和污点符合规划；
- CoreDNS、VPC CNI、kube-proxy、EBS CSI、Metrics Server 正常；
- 测试 Pod 能解析 Service DNS、访问 AWS API、写入并重挂 PVC；
- Cluster Autoscaler 能对 Pending Pod 扩容，节点组 max 能满足副本和滚动余量；
- NLB/ALB Target 健康，HTTP/WSS 长连接和 TLS 续期通过；
- Prometheus、日志、Trace、Alertmanager 收到测试数据；
- CloudTrail、EKS 控制面日志、备份和成本标签有效。

## 交付物

- 实际架构、CIDR/AZ/子网、节点组和调度标准；
- Terraform Plan/Apply 记录、State 位置和恢复步骤；
- 云资源端点和凭据引用（不在文档写明文密码）；
- kubeconfig 获取命令、Ingress/TLS/域名清单；
- 组件版本、Helm values、PVC、备份和升级说明；
- CI/CD 仓库/路径/凭据 ID/回滚步骤；
- 告警通道测试记录、SLO、负责人和值班方式；
- 销毁和接入已有 EKS 的所有权边界。

交付后定时同步 AWS/EKS 实际状态。发现漂移时先展示差异和风险，由用户选择“采用实际配置”或“恢复平台配置”，不得自动覆盖。

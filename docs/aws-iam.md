# AWS 权限模型

## 身份边界

每条 AWS 凭据都必须明确属于一个项目。环境只能选择本项目凭据；未绑定时禁止部署，绝不回退到其他项目或宿主机默认 Profile。

生产推荐模型：

1. 平台运行身份只允许 `sts:AssumeRole` 到批准的项目角色。
2. 每个 AWS 账号/项目/环境建立独立角色，Trust Policy 限制平台身份和 External ID。
3. 角色使用短期 STS，会话时长、区域和权限边界受控。
4. 破坏性权限与日常 Plan/ReadOnly 权限分离；生产 Apply 经过审批。

## 能力与权限域

| 能力 | 主要 AWS API 域 |
|---|---|
| 身份和目录 | STS、EC2 Describe、EKS Describe、Service Quotas |
| VPC/EKS | EC2、EKS、IAM/PassRole、Auto Scaling、KMS、ELBv2 |
| EKS Add-on | EKS Addon、IAM Role/Policy、Pod Identity/OIDC |
| 数据服务 | RDS、ElastiCache、Kafka/MSK、Amazon MQ、DocumentDB、Secrets Manager、KMS |
| 镜像 | ECR 仓库和镜像读写 |
| DNS/证书 | Route 53、ACM（仅选择启用时） |
| State Center | 指定 S3 Bucket/Prefix、KMS Key、DynamoDB（如使用锁表） |
| 只读同步 | 对上述资源的 List/Describe/Get |

不同组件组合需要不同权限，因此仓库不提供一个伪“最小”的全能管理员策略。正确做法是根据生成的 Terraform Plan 和 CloudTrail，在测试账号构建角色并收敛权限；用 IAM Access Analyzer 验证。

## State Center 权限

状态身份应与项目资源身份分离，只允许：

- 列出指定 Bucket 和 Prefix；
- 读写/删除项目环境对应的 State Object；
- 使用指定 KMS Key；
- 读取 Bucket Versioning/Encryption；
- 不允许访问其他组织 Bucket 或 Prefix。

Bucket 必须启用版本控制、SSE-KMS、Block Public Access、TLS-only Policy 和访问日志。平台迁移 State 前要生成清单、校验对象并保留旧版本。

## 验证

```console
aws --profile example-admin sts get-caller-identity
aws --profile example-admin eks describe-cluster --region ap-south-1 --name example-eks-cluster
aws --profile example-admin service-quotas list-service-quotas --service-code ec2 --region ap-south-1
```

凭据错误时日志只应显示 ARN/Account/错误码，不得打印 Secret Access Key、Session Token 或完整请求签名。

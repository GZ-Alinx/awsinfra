# 参与贡献

感谢参与 AWSInfra。提交变更代表你同意按 Apache-2.0 许可贡献代码。

## 开发流程

1. 从 `main` 创建短期分支。
2. 不得提交真实 AK/SK、Token、密码、TLS 私钥、kubeconfig、Terraform State、任务日志或客户数据。
3. 修改基础设施时同时更新变量校验、平台表单、Terraform/Helm 实现和测试。
4. 运行完整校验：

```console
npm --prefix frontend ci
npm --prefix frontend run typecheck
npm --prefix frontend test
npm --prefix frontend run build
go test -race ./...
go vet ./...
terraform fmt -check -recursive terraform
terraform -chdir=terraform/infra init -backend=false
terraform -chdir=terraform/infra validate
terraform -chdir=terraform/platform init -backend=false
terraform -chdir=terraform/platform validate
```

5. Pull Request 说明必须包含影响范围、验证证据、升级方式和回滚方式。

## 兼容性约束

- 已存在的项目和环境配置必须能继续读取；新增字段应有安全默认值。
- 不得让一个项目回退使用其他项目或机器默认的 AWS 凭据。
- 接入已有 EKS 时，仅管理平台明确创建的资源；不得删除集群、VPC、Namespace 或未知工作负载。
- 变更云资源前先刷新实时配置并生成 Plan，不得用陈旧平台值静默覆盖控制台变更。
- 删除、缩容、凭据和公网暴露相关变更必须有单独测试。

安全问题请不要创建公开 Issue，按 [SECURITY.md](SECURITY.md) 报告。

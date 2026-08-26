# 将平台自身部署到 EKS

仓库的 `platform-deploy` 是 Go 原生发布器。它使用配置指定的独立 kubeconfig 和 Context，不修改 `~/.kube/config` 当前上下文。

## 前置条件

- 目标 EKS 已可访问，CoreDNS、VPC CNI、EBS CSI 和网关/LB Controller 正常。
- 当前 AWS Profile 可执行 `sts:GetCallerIdentity`、ECR 推送和 `eks:DescribeCluster`。
- 目标 StorageClass 支持动态 PVC；生产建议 EBS `gp3` 加密。
- Docker Buildx 可构建目标架构镜像。
- 若启用域名：DNS、网关 Service 和源 Namespace TLS Secret 已存在。

## 1. 创建本机配置

```console
cp deploy/kubernetes/deploy.example.yaml deploy/kubernetes/deploy.yaml
cp deploy/kubernetes/secrets.env.example deploy/kubernetes/secrets.env
chmod 600 deploy/kubernetes/secrets.env
```

生成密码哈希和随机 Secret：

```console
go run ./cmd/ops-deploy hash-password
openssl rand -base64 32
openssl rand -base64 24
```

必须填写并安全备份：管理员哈希、凭据主密钥、MySQL 密码/root 密码、Redis 密码。已有数据库升级时必须沿用原来的 `OPS_DEPLOY_CREDENTIAL_KEY`，否则历史密文不可恢复。

编辑 `deploy.yaml`：

- `cluster`：准确的集群、Region、Profile、隔离 kubeconfig；
- `registry`：目标私有 ECR 仓库和可选不可变 Tag；
- `build`：仓库上下文、发布 Dockerfile 和 CPU 架构；
- `kubernetes`：Namespace、StorageClass、PVC 容量和 Service；
- `ingress`：仅在网关和 TLS 已准备好后启用。

真实 `deploy.yaml` 和 `secrets.env` 已被 Git 忽略。

## 2. 预检和渲染

```console
go run ./cmd/platform-deploy preflight --config deploy/kubernetes/deploy.yaml
go run ./cmd/platform-deploy render --config deploy/kubernetes/deploy.yaml
```

预检至少确认 AWS 身份、EKS、ECR、kubectl、StorageClass、目标 Namespace、网关和 TLS。渲染输出只能存放在受控目录，不得提交包含 Secret 的清单。

## 3. 发布

```console
go run ./cmd/platform-deploy deploy --config deploy/kubernetes/deploy.yaml
go run ./cmd/platform-deploy status --config deploy/kubernetes/deploy.yaml
```

等价 Make 目标：

```console
make platform-update PLATFORM_IMAGE_TAG=v1.20.3
make platform-status
```

发布器创建/复用私有 ECR，构建并推送镜像，初始化持久运行目录，应用 Secret/Service/StatefulSet/Deployment，等待滚动完成并执行健康检查。

## 4. 验证

```console
kubectl --kubeconfig data/kubeconfigs/ops-deploy-platform get pods,pvc,svc,ingress -n ops-deploy-system
kubectl --kubeconfig data/kubeconfigs/ops-deploy-platform rollout status deployment/ops-deploy-platform -n ops-deploy-system --timeout=10m
kubectl --kubeconfig data/kubeconfigs/ops-deploy-platform logs deployment/ops-deploy-platform -n ops-deploy-system --tail=200
```

验证登录、健康 API、项目/环境切换、任务日志、AWS STS、State Center 和一次只生成 Plan 的测试任务。

## 5. 升级和回滚

每次发布使用不可变 Tag，先备份 MySQL、Redis、平台数据 PVC、State Center 配置和 `OPS_DEPLOY_CREDENTIAL_KEY`。升级：

```console
make platform-update PLATFORM_IMAGE_TAG=v1.20.4
```

健康检查失败时：

```console
make platform-rollback
make platform-status
```

回滚镜像不会自动回滚数据库数据。涉及数据迁移的版本必须先验证向后兼容和恢复演练。

## 风险边界

- 发布目标由 `deploy.yaml` 明确指定，不依据当前 kubectl Context 猜测。
- 不要把生产与测试指向同一个数据库、Redis、Namespace 或 ECR Tag。
- 不要在发布工具之外手工修改其管理的 Deployment/StatefulSet；紧急变更后应回写配置。
- TLS Secret 跨 Namespace 必须复制生成独立 Secret，Kubernetes Ingress 不能直接引用其他 Namespace 的 Secret。

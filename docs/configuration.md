# 配置参考

平台配置由公开的 `config.yaml` 和私密环境变量组成。YAML 只保存非敏感策略；密码、Token 和加密密钥必须通过 `.env`、Secret 或密码管理系统注入。

## 核心配置

| 区域 | 作用 | 生产建议 |
|---|---|---|
| `server` | 监听地址、超时、可选服务端 TLS | 私网监听或代理后端；外部仅 HTTPS |
| `security` | 管理员、Cookie、会话、Helm SSRF 防护 | `cookie_secure: true`，准确设置 `external_origin` |
| `paths` | 环境、数据和 Terraform 模板目录 | 使用持久卷；目录权限 0700 |
| `tools` | Terraform/AWS/kubectl/Helm 命令 | 使用受控、锁定版本的镜像 |
| `terraform_state` | 旧状态发现和迁移入口 | 实际中心配置应在“Terraform State Center”管理 |
| `datastore` | MySQL/Redis、连接池和状态缓存 | 托管数据库、加密、备份、私网访问 |
| `jobs` | 并行数、任务超时、历史上限 | 按执行节点容量调节；同一环境仍保持互斥 |
| `components` | 组件目录和状态映射 | 新组件需同时实现 Terraform/Helm 与测试 |

## 必填环境变量

| 变量 | 说明 |
|---|---|
| `OPS_DEPLOY_PASSWORD_HASH` | 管理员密码 Argon2id 哈希 |
| `OPS_DEPLOY_CREDENTIAL_KEY` | 32 字节 Base64 AES-256-GCM 主密钥 |
| `OPS_MYSQL_DSN` | 平台 MySQL DSN |
| `OPS_DEPLOY_REDIS_ADDRESS` | Redis `host:port`，初始化已有数据存储时自动生成 |
| `OPS_DEPLOY_REDIS_DATABASE` | Redis Database 编号，默认 `0` |
| `OPS_REDIS_PASSWORD` | Redis 密码 |

源码运行可执行 `go run ./cmd/ops-deploy init --config config.yaml`，发行版执行 `awsinfra init --config config.yaml`。向导可以生成本地 Docker 密码，也可以填写并验证已有 MySQL/Redis。生产 Secret 不应从示例值直接复制。

## 目录和数据

- `environments/`：本机导入/导出的环境实例；数据库是平台权威数据源。
- `data/`：日志、任务、隔离 kubeconfig 和运行缓存。
- `terraform/*/terraform.tfstate.d/`：本地临时工作空间，不应成为生产权威 State。
- `deploy/kubernetes/deploy.yaml`、`secrets.env`：当前机器的发布目标和 Secret。
- `deploy/project-archives.yaml`：当前组织的项目归档清单。

以上均被 Git 忽略。公开示例以 `.example.yaml` 或 `.example` 结尾。

## 配置变更原则

1. 平台先实时读取 AWS/EKS 状态，再判断平台期望值、实际值和上次管理值。
2. 对控制台新增的白名单、规则和共享资源采用集合合并，不能无提示覆盖。
3. 用户明确选择“采用 AWS 实际配置”后才更新平台基线。
4. 缩容、删除、引擎升级、网络和权限变化必须单独生成 Plan。
5. `prod` 环境需要更严格的审核、备份和回滚证据。

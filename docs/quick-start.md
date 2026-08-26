# 本地快速开始

本指南在本机用 Docker 启动 MySQL/Redis，Go 进程运行平台。它适合功能验证和开发，不是生产拓扑。

## 1. 准备工具

- Go 1.25.13 或更高版本
- Node.js 20.19 或更高版本（推荐 24 LTS）
- Docker Engine/Desktop 与 Docker Compose v2
- 创建云资源时：Terraform 1.9+、AWS CLI v2、kubectl、Helm 3+

确认工具：

```console
go version
node --version
docker compose version
terraform version
aws --version
kubectl version --client
helm version
```

## 2. 初始化

```console
git clone https://github.com/GZ-Alinx/ops-deploy-platform.git
cd ops-deploy-platform
go run ./cmd/ops-deploy init --config config.yaml
```

初始化会要求输入并确认管理员密码，然后生成 `.env`：

- Argon2id 管理员密码哈希；
- MySQL 普通用户和 root 随机密码；
- Redis 随机密码；
- 32 字节 AWS 凭据加密主密钥；
- 与随机密码一致的本地 MySQL DSN。

`.env` 使用 `0600` 权限，已存在时不会覆盖。请将它安全备份；不要提交到 Git。

## 3. 启动与验证

```console
make local
```

浏览器访问 <http://127.0.0.1:8080>，使用 `admin` 和初始化密码登录。

```console
curl --fail http://127.0.0.1:8080/api/health
docker compose ps
```

服务数据保存在 Docker named volumes，普通停止不会丢失：

```console
make docker-down
```

重新启动执行 `make local`。只有确定要永久删除本地数据库和 Redis 数据时才执行：

```console
docker compose down --volumes
```

## 4. 首次创建环境

1. 在“项目管理”新建项目，再创建 `dev/test/uat/prod` 中需要的环境。
2. 在平台级 AWS 凭据池添加身份，并明确绑定到该项目。
3. 通过 STS 身份校验后，在项目环境中选择这条凭据。
4. 配置 Region、网络、EKS 节点组和云服务；不要在第一轮同时启用所有组件。
5. 刷新 AWS 实际状态，执行额度门禁，生成阶段 1 Plan 并人工审查。
6. 完成 VPC/EKS/数据服务后运行 EKS 验收，再执行阶段 2。

平台不会使用其他项目凭据或机器默认凭据兜底。没有项目绑定凭据时，部署会明确失败。

## 5. 本地开发

前端热更新：

```console
npm --prefix frontend ci
npm --prefix frontend run dev
```

另一个终端启动 API：

```console
docker compose up -d --wait mysql redis
go run ./cmd/ops-deploy serve --config config.yaml
```

若登录失败，先检查 `.env`、MySQL/Redis健康状态和 `data/logs/manager.log`，不要通过删除数据库绕过问题。

# AWSInfra 发行版安装指南

发行包已经包含 AWSInfra 后端、嵌入式 Web 控制台、`config.yaml`、Terraform/Helm 模板、Docker Compose 数据服务和运维文档。首次体验不需要安装 Go 或 Node.js。

## 支持的平台

| 下载包 | 操作系统与架构 |
|---|---|
| `awsinfra_<version>_windows_amd64.zip` | Windows 10/11、Windows Server 2019+，x86-64 |
| `awsinfra_<version>_darwin_arm64.tar.gz` | macOS Apple Silicon（M1/M2/M3/M4） |
| `awsinfra_<version>_darwin_amd64.tar.gz` | macOS Intel x86-64 |
| `awsinfra_<version>_linux_amd64.tar.gz` | Linux x86-64 / AMD64 |
| `awsinfra_<version>_linux_arm64.tar.gz` | Linux ARM64 / AArch64 |

32 位 Windows 不受支持。AWS CLI v2、Terraform 和现代 Kubernetes 工具均以 64 位系统为生产基线。

## 1. 下载与校验

从 [GitHub Releases](https://github.com/GZ-Alinx/awsinfra/releases) 下载对应安装包和 `checksums.txt`。

macOS/Linux：

```console
grep 'awsinfra_1.0.0_darwin_arm64.tar.gz' checksums.txt | shasum -a 256 -c -
tar -xzf awsinfra_1.0.0_darwin_arm64.tar.gz
cd awsinfra_1.0.0_darwin_arm64
```

Windows PowerShell：

```powershell
Get-FileHash .\awsinfra_1.0.0_windows_amd64.zip -Algorithm SHA256
Expand-Archive .\awsinfra_1.0.0_windows_amd64.zip -DestinationPath .
Set-Location .\awsinfra_1.0.0_windows_amd64
```

将 PowerShell 输出与 `checksums.txt` 对应行比较。当前社区发行包未进行 Apple Notarization 或 Windows Authenticode 签名；如系统拦截，请先核对 SHA-256 和 GitHub 仓库来源，再按组织安全策略放行。

## 2. 选择数据存储

AWSInfra 必须使用 MySQL 和 Redis。初始化命令会先设置管理员密码，再让用户选择数据存储模式；`.env` 已被排除在 Git 外，并按当前操作系统可用的最严格文件权限创建。

### 方式 A：本地 Docker（首次体验推荐）

需要 Docker Desktop 或 Docker Engine + Compose v2：

```console
./awsinfra init --config config.yaml --datastore local
docker compose up -d --wait mysql redis
./awsinfra serve --config config.yaml
```

Windows PowerShell：

```powershell
.\awsinfra.exe init --config .\config.yaml --datastore local
docker compose up -d --wait mysql redis
.\awsinfra.exe serve --config .\config.yaml
```

初始化会生成本地 MySQL、MySQL root、Redis、管理员密码哈希和凭据加密主密钥。普通停止使用 `docker compose down`；不要执行 `docker compose down --volumes`，除非确定要永久删除平台数据。

### 方式 B：已有 MySQL/Redis

先创建空数据库，推荐 MySQL 8.0/8.4、`utf8mb4` 字符集。平台账号需要在该数据库内创建和变更表、索引以及读写数据的权限。Redis 建议 7.x，并限制为平台主机可访问。

```console
./awsinfra init --config config.yaml --datastore external
```

向导会要求填写：

- MySQL 主机、端口、数据库、用户名、密码和 TLS 模式；
- Redis 主机、端口、密码和 Database；
- 平台管理员密码。

写入 `.env` 前会实际连接 MySQL 与 Redis。网络暂未放通但需要先生成配置时，可以显式添加 `--skip-datastore-check`；启动服务时仍会重新连接并在失败时终止，不能绕过依赖检查。

## 3. 启动与登录

```console
./awsinfra version
./awsinfra serve --config config.yaml
```

默认访问地址是 <http://127.0.0.1:8080>，用户名为 `admin`，密码是初始化时设置的密码。进程必须从安装包根目录启动，因为 `config.yaml` 默认引用同目录下的 `terraform/`、`data/` 和 `environments/`。

开放给其他机器访问前，应修改 `server.listen_address`，并通过可信反向代理提供 HTTPS；同时设置 `security.cookie_secure: true` 和准确的 `security.external_origin`。不要直接将 8080 端口暴露到公网。

## 4. 创建 AWS/EKS 资源前安装工具

仅查看 UI 和管理平台数据不需要这些工具。执行 AWS/EKS 部署任务时，AWSInfra 会调用本机命令，需安装并加入 `PATH`：

- Terraform 1.9 或更高版本；
- AWS CLI v2；
- kubectl（与目标 EKS 版本兼容）；
- Helm 3 或更高版本。

验证：

```console
terraform version
aws --version
kubectl version --client
helm version
```

AWS AK/SK、AssumeRole 和项目权限在登录后的“AWS 凭据池”配置，不要写入安装目录、命令历史或源码。

## 5. 升级与备份

升级前备份 MySQL、`.env`、中心 Terraform State 和 `data/`。停止旧进程，保留原有 `.env`、`config.yaml`、`data/`、`environments/`，用新发行包的可执行文件和 Terraform 模板替换程序文件，再启动并检查 `/api/health`。

`OPS_DEPLOY_CREDENTIAL_KEY` 丢失后无法恢复已加密的 AWS/GitLab/Jenkins 凭据，必须纳入密码管理系统和加密备份。

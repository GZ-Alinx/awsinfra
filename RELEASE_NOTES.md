# AWSInfra v1.0

AWSInfra 是面向 AWS/EKS 的项目化基础设施与应用交付平台。v1.0 是第一个可公开下载、初始化和运行的多平台版本。

## 主要能力

- 项目与环境：以项目为权限边界管理 `dev`、`test`、`uat`、`prod`，记录创建、部署、运行、异常和销毁状态。
- AWS 基础设施：Terraform 管理 VPC、EKS、节点组、ECR、RDS/Aurora、DocumentDB、ElastiCache、MSK、Amazon MQ 等资源。
- EKS 组件：支持 Higress、Jenkins、Argo CD、Prometheus/Grafana、Loki、Tempo、OpenTelemetry、EFK、ClickVisual、Consul、etcd、RabbitMQ 等组件。
- 两阶段部署：基础资源与云服务、Kubernetes 组件与接入配置分阶段计划、执行、重试和诊断。
- 访问管理：TLS 证书、Ingress、域名多路由、Service 发现、负载均衡地址和资源连接信息统一管理。
- CI/CD：GitLab 与 Jenkins 接入，按环境管理 Jenkinsfile、Dockerfile、部署清单、凭据、构建进度和实时日志。
- 可观测性：集群指标、日志、链路追踪、告警通道/模板和应用拓扑。
- 安全治理：项目级 AWS 凭据隔离、AES-256-GCM 加密、Argon2id 登录密码、RBAC、操作审计、生产环境提醒和危险操作保护。
- 状态与漂移：集中 Terraform State、AWS/EKS 实际状态回读、差异检测及避免无提示覆盖控制台变更。

## v1.0 新增的发行能力

- 提供 Windows AMD64、macOS Intel、macOS Apple Silicon、Linux AMD64、Linux ARM64 五个安装包。
- 安装包内置前端、默认配置、Terraform/Helm 模板和完整文档，不需要 Go/Node.js 即可启动。
- 初始化向导支持本地 Docker MySQL/Redis 或填写已有 MySQL/Redis，并在写入配置前验证连接。
- `.env` 使用安全引号处理特殊字符，自动生成 Argon2id 密码哈希和 256 位凭据加密主密钥。
- Windows 与 Unix 使用独立的进程终止和文件权限实现，所有目标均经过真实交叉编译。
- 发布产物提供 SHA-256 `checksums.txt`。

## 使用提醒

- 首次使用请先在独立 AWS 测试账号验证，AWS 托管资源会产生费用。
- 创建 AWS/EKS 资源仍需在本机安装 Terraform、AWS CLI、kubectl 和 Helm。
- 发行包未进行 Apple/Windows 商业代码签名，请从本仓库 Release 下载并校验 SHA-256。
- 现有 Kubernetes 资源名、Terraform State 和 `ops-deploy` 内部兼容标识保持不变，升级时不要因产品改名手工重建资源。

完整安装步骤见安装包中的 `docs/release-installation.md`。

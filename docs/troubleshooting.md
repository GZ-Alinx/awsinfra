# 故障排查

## 平台无法登录

```console
curl -v http://127.0.0.1:8080/api/health
docker compose ps
docker compose logs --tail=100 mysql redis
tail -n 200 data/logs/manager.log
```

确认 `.env` 的 DSN 密码与首次创建 MySQL Volume 时一致。修改 `.env` 不会自动修改已初始化数据库用户密码。不要通过删除 Volume 处理生产问题。

## AWS 凭据无效

- 凭据必须属于当前项目并已绑定；
- 校验 Region 和 STS 身份；
- 临时凭据必须同时提供 Session Token 且未过期；
- 检查系统时间、代理和企业网络；
- 403 时根据日志中的具体 API 补权限，不要直接授予 AdministratorAccess。

```console
aws --profile example-admin sts get-caller-identity
```

## Terraform State 锁或漂移

1. 确认没有该环境的运行中任务和外部 Terraform 进程。
2. 检查 State Backend、对象版本和锁持有者。
3. 刷新云端实际状态并生成新 Plan。
4. 只有锁确认为孤儿时才使用平台“解锁”功能；不要手工删除 State 对象。

部署历史被清理不代表 State 被删除，State Center 才是资源所有权的权威记录。

## Pod Pending

```console
kubectl describe pod POD -n NAMESPACE
kubectl get nodes -L ops-deploy.io/workload-class
kubectl describe node NODE
kubectl get events -n NAMESPACE --sort-by=.lastTimestamp
```

重点检查 CPU/内存、每节点 Pod 上限、节点组 max、AZ/PVC、nodeSelector/affinity、taint/toleration。自动扩容器只能在存在可扩节点组且 Pod 调度约束可满足时扩容。

## Helm/Terraform 等待超时

超时可能表示资源部分创建。先查看 Helm release、Pod、PVC、Service Events 和云端资源，再决定原地重试；不要先删除 Namespace/PVC。组件卸载也不得删除共享 Namespace。

## 网关没有地址或 `no healthy upstream`

```console
kubectl get svc,ingress -A
kubectl describe svc higress-gateway -n higress-system
kubectl get endpoints,endpointslices -n APP_NAMESPACE
kubectl get targetgroupbinding -A
```

确认 AWS Load Balancer Controller/Service Controller 模式、子网标签、安全组、Target 健康、Service 端口/协议和 readiness。WSS 是客户端到网关 TLS；Pod 未监听 TLS 时，网关到 Service 应使用 HTTP/WebSocket。

## 数据库 TCP 域名无法握手

HTTP Ingress 不能代理 MySQL 原生协议。应使用网关明确的 TCP Route/Listener、NLB TCP Service 或私网隧道。数据库默认不应对全互联网开放；限制源 CIDR并启用数据库鉴权和传输加密。

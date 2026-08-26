# Metrics、Logs、Traces 一体化可观测体系

平台采用以下数据链路，所有组件都通过 Helm 管理，默认部署到 `monitoring` Namespace：

```text
Node / Pod 日志与指标 -> OTel Agent DaemonSet
应用 OTLP ------------> OTel Gateway StatefulSet
                            |- Metrics -> Prometheus
                            |- Logs ----> Loki
                            |- Traces --> Jaeger Collector -> Badger / Elasticsearch -> Jaeger UI
                            |- Traces --> Tempo（可选）
                            `- Logs ----> Elasticsearch（可选）

Prometheus + Loki + Tempo（可选）+ Elasticsearch -> Grafana
Jaeger Storage -> Jaeger Query/UI
PrometheusRule -> Alertmanager -> 平台告警通道
```

## 平台部署顺序

1. `kube-prometheus-stack`：Prometheus、Grafana、Alertmanager、node-exporter、kube-state-metrics。
2. `Loki`：低成本 Kubernetes 日志查询。
3. `OpenTelemetry 专用 Elasticsearch`（可选）：独立保存 OTel 日志与 Jaeger Trace，不复用 EFK。
4. `Jaeger`：Collector、Trace 持久化、Query API 和带基础认证的 Jaeger UI。
5. `Tempo`（可选）：需要 Grafana TraceQL 或双写时启用。
6. `EFK`（可选）：独立的 Fluentd 日志采集、全文检索与 Kibana，不参与 OTel/Jaeger 存储。
7. `OpenTelemetry Collector`：Agent DaemonSet + Gateway StatefulSet。
8. Grafana 数据源、EKS/日志/链路 Dashboard 和 PrometheusRule。

在“阶段 2 · 安装组件”中启用 OpenTelemetry Collector 时，平台会自动启用 Prometheus + Grafana、Loki 和 Jaeger。Jaeger 是默认 Trace 后端与查询面板；Tempo 保持可选。Jaeger 测试模式使用带 EBS PVC 的 Badger；勾选“独立 Elasticsearch 存储”后，平台创建独立的 `otel-elasticsearch` Helm Release、Secret、StatefulSet、Service 和每节点 PVC，同时让 OTel 日志写入 `logs-otel*`、Jaeger Trace 写入 `jaeger-*`。该实例与 EFK 完全隔离。

## 应用接入

Go、Java 和 Node.js 应用都可使用以下通用环境变量。Namespace 按实际环境替换：

```yaml
env:
  - name: OTEL_SERVICE_NAME
    value: order-api
  - name: OTEL_EXPORTER_OTLP_ENDPOINT
    value: http://opentelemetry-collector.monitoring.svc.cluster.local:4318
  - name: OTEL_RESOURCE_ATTRIBUTES
    value: deployment.environment.name=test,service.version=1.0.0
  - name: OTEL_TRACES_EXPORTER
    value: otlp
  - name: OTEL_METRICS_EXPORTER
    value: otlp
  - name: OTEL_LOGS_EXPORTER
    value: otlp
```

Java Agent 还需要在容器启动参数中加入 `-javaagent:/otel/opentelemetry-javaagent.jar`。Go 和 Node.js 应在应用入口初始化对应的 OpenTelemetry SDK；平台只负责稳定的 OTLP 接收地址，不修改业务代码。

## 测试版与生产推荐值

| 项目 | 最小测试版 | 生产推荐版 |
|---|---:|---:|
| OTel Gateway | 1 副本、200m/256Mi、10Gi WAL | 3 副本、500m/512Mi、每副本 20Gi WAL |
| OTel Agent | 每节点 100m/192Mi | 每节点 200m/256Mi 起 |
| Prometheus | 50Gi、15 天 | 200Gi 起，按指标基数评估 |
| Grafana | 10Gi | 20Gi 起 |
| Alertmanager | 10Gi | 20Gi 起，至少 2 副本 |
| Loki | 20Gi、7 天 | 100Gi 起；大规模改用 S3 |
| Jaeger | 1 副本、Badger 20Gi、7 天 | 3 副本、Elasticsearch、30 天索引清理策略 |
| Tempo（可选） | 20Gi、7 天 | 100Gi、30 天；大规模改用 tempo-distributed + S3 |
| OTel 专用 Elasticsearch | 1 节点、50Gi | 3 节点、每节点 200Gi 起，JVM Heap 不超过容器内存 50% |

所有在线磁盘操作只允许扩容。Kubernetes 与 EBS 不支持安全的原地缩容；缩容必须新建卷、迁移数据并切换工作负载。

## Grafana 自动配置

平台自动创建以下数据源：

- `Prometheus`：默认指标数据源。
- `Loki`：Pod 日志和 Kubernetes 事件。
- `Tempo`：TraceQL、Trace 到日志、Trace 到指标、服务拓扑。
- `OpenTelemetry Elasticsearch`：仅勾选专用存储时创建，使用独立数据源、独立密码和 `logs-otel*` 索引。
- `EFK Elasticsearch`：仅 EFK 启用时创建，与 OTel/Jaeger 数据完全隔离。

自动导入以下 Dashboard：

- EKS 集群总览、Node 与 Pod 核心资源。
- Kubernetes 集群日志。
- Tempo 链路请求率、错误率、P95 延迟和最近 Trace。

## 手工 Helm 等价命令

平台会自动完成这些步骤；以下命令仅用于独立排障或在平台外复现。

```sh
helm repo add prometheus-community https://prometheus-community.github.io/helm-charts
helm repo add grafana https://grafana.github.io/helm-charts
helm repo add open-telemetry https://open-telemetry.github.io/opentelemetry-helm-charts
helm repo add jaegertracing https://jaegertracing.github.io/helm-charts
helm repo add elastic https://helm.elastic.co
helm repo update

helm upgrade --install prometheus prometheus-community/kube-prometheus-stack -n monitoring --create-namespace -f prometheus-values.yaml
helm upgrade --install loki grafana/loki -n monitoring -f loki-values.yaml
helm upgrade --install jaeger ./terraform/platform/charts/jaeger-stack -n monitoring -f jaeger-values.yaml
# 可选：helm upgrade --install tempo grafana/tempo -n monitoring -f tempo-values.yaml
# 可选：helm upgrade --install otel-elasticsearch ./terraform/platform/charts/otel-elasticsearch -n monitoring -f otel-elasticsearch-values.yaml
helm upgrade --install opentelemetry-collector ./terraform/platform/charts/observability-otel -n monitoring -f otel-values.yaml
```

平台分别管理 `otel-elasticsearch` 与 `efk-stack` 两套生命周期，二者名称、密码、PVC、索引和 Grafana 数据源均不共享。不要在平台外创建同名 `otel-elasticsearch` Release。

## 验证

```sh
kubectl get pods,pvc -n monitoring
kubectl get servicemonitor,prometheusrule -n monitoring
kubectl get service opentelemetry-collector jaeger loki-gateway otel-elasticsearch -n monitoring
kubectl logs -n monitoring statefulset/opentelemetry-collector -c gateway --tail=100
kubectl logs -n monitoring daemonset/opentelemetry-agent -c agent --tail=100
kubectl logs -n monitoring statefulset/jaeger -c jaeger --tail=100
```

验证数据链路：

1. Grafana Explore 选择 Prometheus，查询 `up`。
2. 选择 Loki，查询 `{k8s_namespace_name!=""}`。
3. 打开 Jaeger UI，按 Service、Operation、Tags 和时间范围查询 Trace。
4. 打开一条 Trace，确认 Span 瀑布、耗时、错误标签与服务依赖完整。
5. 查看 OTel Gateway 指标 `otelcol_exporter_sent_spans`、`otelcol_exporter_sent_log_records` 和 `otelcol_exporter_send_failed_*`。

无 Trace 数据时，先确认应用已真正启用对应语言的 OpenTelemetry SDK/Agent；只配置环境变量不会自动改造一个未集成 SDK 的应用。

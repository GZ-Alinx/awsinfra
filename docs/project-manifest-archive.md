# 已部署项目清单归档

归档工具从实际 Kubernetes 集群读取 Helm release，而不是只复制平台数据库中的期望配置。每次同步生成一个不可变时间快照，并更新 `current` 软链接。

```text
../已部署项目归档/<project>/<environment>/
  current -> snapshots/<UTC timestamp>
  snapshots/<UTC timestamp>/
    archive.yaml
    cluster/resources.current.redacted.yaml
    cluster/secret-inventory.txt
    helm/<namespace>/<release>/
      release.yaml
      chart/
      values.current.redacted.yaml
      manifest.current.redacted.yaml
  working/helm/<namespace>/<release>/values.override.yaml
```

同步全部有效环境：

```sh
make archive-sync
```

变更 Helm release 时只编辑 `working` 下的 override 文件，先预检，再应用：

```sh
make archive-plan PROJECT=demo ENVIRONMENT=prod NAMESPACE=monitoring RELEASE=prometheus
make archive-apply PROJECT=demo ENVIRONMENT=prod NAMESPACE=monitoring RELEASE=prometheus \
  CONFIRM=demo/prod/monitoring/prometheus
```

安全约束：

- 不归档 Kubernetes Secret 正文。
- values 和渲染清单中的密码、Token、Webhook 等字段会脱敏。
- `helm upgrade` 使用 `--reuse-values`，未写入 override 的现有密码不会被清空。
- 每次生产应用都要求项目、环境、Namespace、release 四段确认字符串。
- 归档位于平台源码目录之外的 `../已部署项目归档`，不会被 Go 构建扫描，也不会误提交到平台 Git 仓库。

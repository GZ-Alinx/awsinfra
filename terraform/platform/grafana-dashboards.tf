locals {
  grafana_eks_dashboard = jsonencode(jsondecode(templatefile("${path.module}/dashboards/eks-core-overview.json.tftpl", {
    cluster_name = local.cluster_name
    environment  = local.environment
  })))
  grafana_logs_dashboard = jsonencode(jsondecode(templatefile("${path.module}/dashboards/cluster-logs.json.tftpl", {
    cluster_name = local.cluster_name
    environment  = local.environment
  })))
  grafana_traces_dashboard = jsonencode(jsondecode(templatefile("${path.module}/dashboards/traces.json.tftpl", {
    cluster_name = local.cluster_name
    environment  = local.environment
  })))
}

resource "kubernetes_config_map_v1" "grafana_traces_dashboard" {
  count = local.tempo_enabled && local.grafana_enabled ? 1 : 0

  metadata {
    name      = "ops-deploy-traces-dashboard"
    namespace = local.grafana_namespace
    labels = {
      grafana_dashboard       = "1"
      "ops-deploy.io/managed" = "true"
      "ops-deploy.io/type"    = "tracing-dashboard"
    }
  }

  data = {
    "ops-deploy-traces.json" = local.grafana_traces_dashboard
  }

  depends_on = [
    helm_release.catalog,
    kubernetes_config_map_v1.grafana_tempo_datasource,
  ]
}

# The platform explicitly enables the Grafana dashboard sidecar and makes it
# watch ConfigMaps carrying grafana_dashboard=1 in every namespace. Keeping
# dashboards in dedicated Terraform resources makes them versioned, idempotent
# and automatically available to both existing and newly created environments.
resource "kubernetes_config_map_v1" "grafana_eks_dashboard" {
  count = local.grafana_enabled ? 1 : 0

  metadata {
    name      = "ops-deploy-eks-core-dashboard"
    namespace = local.grafana_namespace
    labels = {
      grafana_dashboard       = "1"
      "ops-deploy.io/managed" = "true"
      "ops-deploy.io/type"    = "monitoring-dashboard"
    }
  }

  data = {
    "ops-deploy-eks-core.json" = local.grafana_eks_dashboard
  }

  depends_on = [helm_release.catalog]
}

resource "kubernetes_config_map_v1" "grafana_logs_dashboard" {
  count = local.loki_enabled && local.grafana_enabled ? 1 : 0

  metadata {
    name      = "ops-deploy-cluster-logs-dashboard"
    namespace = local.grafana_namespace
    labels = {
      grafana_dashboard       = "1"
      "ops-deploy.io/managed" = "true"
      "ops-deploy.io/type"    = "logging-dashboard"
    }
  }

  data = {
    "ops-deploy-cluster-logs.json" = local.grafana_logs_dashboard
  }

  depends_on = [
    helm_release.catalog,
    helm_release.loki_collector,
    kubernetes_config_map_v1.grafana_loki_datasource,
  ]
}

# Grafana is the single query entry for metrics, logs and traces. Datasources
# are provisioned declaratively so a new environment is usable immediately
# after Helm finishes; no administrator needs to click through Grafana.
resource "kubernetes_config_map_v1" "grafana_tempo_datasource" {
  count = local.tempo_enabled && local.grafana_enabled ? 1 : 0

  metadata {
    name      = "tempo-grafana-datasource"
    namespace = local.grafana_namespace
    labels = {
      grafana_datasource      = "1"
      "ops-deploy.io/managed" = "true"
    }
  }

  data = {
    "tempo-datasource.yaml" = yamlencode({
      apiVersion = 1
      datasources = [{
        name      = "Tempo"
        uid       = "tempo"
        type      = "tempo"
        access    = "proxy"
        url       = local.tempo_url
        editable  = false
        isDefault = false
        jsonData = {
          httpMethod = "GET"
          nodeGraph  = { enabled = true }
          serviceMap = { datasourceUid = "prometheus" }
          tracesToLogsV2 = {
            datasourceUid      = "loki"
            spanStartTimeShift = "-1h"
            spanEndTimeShift   = "1h"
            filterByTraceID    = true
            filterBySpanID     = true
            tags               = ["service.name", "k8s.namespace.name", "k8s.pod.name"]
          }
          tracesToMetrics = {
            datasourceUid = "prometheus"
            queries = [
              { name = "请求速率", query = "sum(rate(traces_spanmetrics_calls_total{$$__tags}[5m]))" },
              { name = "错误速率", query = "sum(rate(traces_spanmetrics_calls_total{$$__tags,status_code=\"STATUS_CODE_ERROR\"}[5m]))" },
            ]
          }
          traceQuery = { timeShiftEnabled = true, spanStartTimeShift = "-1h", spanEndTimeShift = "1h" }
        }
        version = 1
      }]
    })
  }

  depends_on = [helm_release.catalog]
}

# Elasticsearch credentials must never be stored in a ConfigMap. The Grafana
# sidecar reads this labelled Secret and writes the secure provisioning file
# directly into Grafana's datasource directory.
resource "kubernetes_secret_v1" "grafana_elasticsearch_datasource" {
  count = local.efk_enabled && local.grafana_enabled ? 1 : 0

  metadata {
    name      = "elasticsearch-grafana-datasource"
    namespace = local.grafana_namespace
    labels = {
      grafana_datasource      = "1"
      "ops-deploy.io/managed" = "true"
    }
  }

  data = {
    "elasticsearch-datasource.yaml" = yamlencode({
      apiVersion = 1
      datasources = [{
        name          = "Elasticsearch"
        uid           = "elasticsearch"
        type          = "elasticsearch"
        access        = "proxy"
        url           = local.efk_elasticsearch_url
        editable      = false
        isDefault     = false
        basicAuth     = true
        basicAuthUser = "elastic"
        jsonData = {
          index     = "kubernetes-${local.project}-${local.environment}-*"
          timeField = "@timestamp"
          interval  = "Daily"
        }
        secureJsonData = {
          basicAuthPassword = random_password.efk_elastic[0].result
        }
        version = 1
      }]
    })
  }

  type       = "Opaque"
  depends_on = [helm_release.catalog]
}

# The OpenTelemetry Elasticsearch instance has a distinct datasource and
# credential. This keeps EFK log indices and OTel/Jaeger observability data
# clearly separated in Grafana and in Terraform state.
resource "kubernetes_secret_v1" "grafana_otel_elasticsearch_datasource" {
  count = local.otel_elasticsearch_enabled && local.grafana_enabled ? 1 : 0

  metadata {
    name      = "otel-elasticsearch-grafana-datasource"
    namespace = local.grafana_namespace
    labels = {
      grafana_datasource      = "1"
      "ops-deploy.io/managed" = "true"
    }
  }

  data = {
    "otel-elasticsearch-datasource.yaml" = yamlencode({
      apiVersion = 1
      datasources = [{
        name          = "OpenTelemetry Elasticsearch"
        uid           = "otel-elasticsearch"
        type          = "elasticsearch"
        access        = "proxy"
        url           = local.otel_elasticsearch_url
        editable      = false
        isDefault     = false
        basicAuth     = true
        basicAuthUser = "elastic"
        jsonData = {
          index     = "logs-otel*"
          timeField = "@timestamp"
        }
        secureJsonData = {
          basicAuthPassword = random_password.otel_elasticsearch[0].result
        }
        version = 1
      }]
    })
  }

  type       = "Opaque"
  depends_on = [helm_release.otel_elasticsearch, helm_release.catalog]
}

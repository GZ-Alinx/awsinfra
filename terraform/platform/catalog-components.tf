resource "helm_release" "catalog" {
  for_each = local.enabled_catalog_components

  name             = try(each.value.release_name, each.key)
  repository       = try(each.value.builtin_chart, "") != "" ? null : each.value.repository
  chart            = try(each.value.builtin_chart, "") != "" ? "${path.module}/charts/${each.value.builtin_chart}" : each.value.chart
  version          = try(each.value.chart_version, null)
  namespace        = try(each.value.namespace, "platform-server")
  create_namespace = false

  values = [yamlencode(try(each.value.values, {}))]

  # New managed environments split business workloads from operational
  # components. Charts use different nodeSelector paths, so keep the mapping
  # platform-owned and fall back to the conventional top-level value.
  dynamic "set" {
    for_each = local.workload_scheduling_enabled ? toset(lookup(local.catalog_node_selector_paths, each.key, ["nodeSelector.workload-class"])) : toset([])
    content {
      name  = set.value
      value = "platform"
      type  = "string"
    }
  }

  # A workload-class label alone is not unique during blue/green migration,
  # because the legacy and replacement node groups can temporarily share it.
  # The pool label pins stateless platform workloads to the intended
  # replacement group. Stateful components deliberately keep only the
  # workload-class selector: their zonal EBS PVC may be in an AZ where the
  # selected pool has no node. Never recreate the omitted selector with a
  # fallback, otherwise RabbitMQ/Prometheus/Bytebase can remain Pending.
  dynamic "set" {
    for_each = local.workload_scheduling_enabled && contains(keys(local.platform_node_selector), "ops-deploy.io/pool") && !contains(local.catalog_zonal_storage_components, each.key) ? toset([
      for path in lookup(local.catalog_node_selector_paths, each.key, ["nodeSelector.workload-class"]) :
      replace(path, "workload-class", "ops-deploy\\.io/pool")
    ]) : toset([])
    content {
      name  = set.value
      value = local.platform_node_selector["ops-deploy.io/pool"]
      type  = "string"
    }
  }

  dynamic "set" {
    for_each = local.workload_scheduling_enabled ? lookup(local.catalog_platform_toleration_set_values, each.key, {
      "tolerations[0].key"      = "workload-class"
      "tolerations[0].operator" = "Equal"
      "tolerations[0].value"    = "platform"
      "tolerations[0].effect"   = "NoSchedule"
    }) : {}
    content {
      name  = set.key
      value = set.value
      type  = "string"
    }
  }

  # Higress separates its public data plane from its controller and console.
  # This is intentionally not part of the generic platform mapping above.
  dynamic "set" {
    for_each = each.key == "higress" && local.workload_scheduling_enabled && local.selected_gateway_node_group != null ? {
      "higress-core.gateway.nodeSelector.workload-class"       = "gateway"
      "higress-core.gateway.nodeSelector.ops-deploy\\.io/pool" = try(local.gateway_node_selector["ops-deploy.io/pool"], "ingress-gateway")
      "higress-core.gateway.tolerations[0].key"                = "workload-class"
      "higress-core.gateway.tolerations[0].operator"           = "Equal"
      "higress-core.gateway.tolerations[0].value"              = "gateway"
      "higress-core.gateway.tolerations[0].effect"             = "NoSchedule"
    } : {}
    content {
      name  = set.key
      value = set.value
      type  = "string"
    }
  }

  # Jaeger receives the platform scope as resource metadata and connects only
  # to the dedicated OpenTelemetry Elasticsearch release. EFK remains an
  # independent logging product and can be installed or removed separately.
  dynamic "set" {
    for_each = each.key == "jaeger" ? {
      "project"                        = local.project
      "environment"                    = local.environment
      "storage.elasticsearch.endpoint" = local.otel_elasticsearch_url
    } : {}
    content {
      name  = set.key
      value = set.value
      type  = "string"
    }
  }

  dynamic "set" {
    for_each = toset(try(each.value.replica_paths, []))
    content {
      name  = set.value
      value = tostring(try(each.value.deployment_mode, "standalone") == "cluster" ? try(each.value.replicas, 3) : 1)
    }
  }

  # The built-in observability chart renders an Agent DaemonSet and a central
  # Gateway StatefulSet. These values bind emitted resource attributes to the
  # actual project/environment/cluster even when an existing EKS is imported.
  dynamic "set" {
    for_each = each.key == "opentelemetry_collector" ? {
      "project"                                = local.project
      "environment"                            = local.environment
      "clusterName"                            = local.cluster_name
      "destinations.elasticsearch.endpoint"    = local.otel_elasticsearch_url
      "destinations.elasticsearch.secretName"  = "otel-elasticsearch-access"
      "destinations.elasticsearch.usernameKey" = "username"
      "destinations.elasticsearch.passwordKey" = "password"
    } : {}
    content {
      name  = set.key
      value = set.value
    }
  }

  # Loki SingleBinary needs its ring replication factor to follow the selected
  # deployment mode in addition to the StatefulSet replica count.
  dynamic "set" {
    for_each = each.key == "loki" ? ["commonConfig.replication_factor"] : []
    content {
      name  = set.value
      value = tostring(try(each.value.deployment_mode, "standalone") == "cluster" ? min(try(each.value.replicas, 3), 3) : 1)
    }
  }

  # Database consoles are wired to the matching self-hosted service with its
  # cluster-local FQDN. Credentials are supplied separately through
  # set_sensitive so neither the generated config nor plan logs expose them.
  dynamic "set" {
    for_each = each.key == "bytebase" ? {
      "integration.mysql.enabled"  = tostring(local.bytebase_mysql_integration)
      "integration.mysql.host"     = "${try(local.enabled_catalog_components["mysql"].service_name, "mysql")}.${try(local.enabled_catalog_components["mysql"].namespace, "platform-server")}.svc.cluster.local"
      "integration.mysql.port"     = tostring(try(local.enabled_catalog_components["mysql"].service_port, 3306))
      "integration.mysql.username" = try(local.enabled_catalog_components["mysql"].values.auth.username, "root")
      } : each.key == "redisinsight" ? {
      "connection.redis.host"  = "${try(local.enabled_catalog_components["redis"].service_name, "redis")}.${try(local.enabled_catalog_components["redis"].namespace, "platform-server")}.svc.cluster.local"
      "connection.redis.port"  = tostring(try(local.enabled_catalog_components["redis"].service_port, 6379))
      "connection.redis.alias" = "${local.project}-${local.environment} Redis"
      "connection.redis.tls"   = "false"
    } : {}
    content {
      name  = set.key
      value = set.value
      type  = "string"
    }
  }

  # An imported EKS cluster can already run another monitoring stack. Keep
  # this environment's Loki RBAC namespace-scoped so it never tries to adopt
  # cluster-scoped objects owned by a different Helm release.
  dynamic "set" {
    for_each = each.key == "loki" && !local.manage_cluster_addons ? ["rbac.namespaced"] : []
    content {
      name  = set.value
      value = "true"
    }
  }

  # node-exporter defaults to hostNetwork and port 9100. A shared/imported EKS
  # may already expose that port from another Prometheus release, which leaves
  # every new DaemonSet Pod Pending. Pod networking keeps the release isolated
  # while Prometheus can still scrape the exporter through its Service.
  dynamic "set" {
    for_each = each.key == "prometheus" && !local.manage_cluster_addons ? ["prometheus-node-exporter.hostNetwork"] : []
    content {
      name  = set.value
      value = "false"
    }
  }

  # Managed clusters install AWS Load Balancer Controller and can register Pod
  # IPs directly. An imported EKS cluster with manage_addons=false must not be
  # mutated with cluster-level IAM/controllers, so use the EKS service
  # controller's NLB instance mode instead. Setting type=external on such a
  # cluster leaves the Service pending forever because no controller claims it.
  dynamic "set" {
    for_each = each.key == "higress" ? (local.manage_cluster_addons ? {
      "higress-core.gateway.service.annotations.service\\.beta\\.kubernetes\\.io/aws-load-balancer-type"                                = "external"
      "higress-core.gateway.service.annotations.service\\.beta\\.kubernetes\\.io/aws-load-balancer-scheme"                              = local.higress_nlb_scheme
      "higress-core.gateway.service.annotations.service\\.beta\\.kubernetes\\.io/aws-load-balancer-nlb-target-type"                     = "ip"
      "higress-core.gateway.service.annotations.service\\.beta\\.kubernetes\\.io/aws-load-balancer-ip-address-type"                     = "ipv4"
      "higress-core.gateway.service.annotations.service\\.beta\\.kubernetes\\.io/aws-load-balancer-attributes"                          = "load_balancing.cross_zone.enabled=true"
      "higress-core.gateway.service.annotations.service\\.beta\\.kubernetes\\.io/aws-load-balancer-additional-resource-tags"            = "Project=${local.project},Environment=${local.environment},ManagedBy=OpsDeployPlatform"
      "higress-core.gateway.service.annotations.service\\.beta\\.kubernetes\\.io/aws-load-balancer-security-groups"                     = join(",", local.higress_nlb_frontend_security_group_ids)
      "higress-core.gateway.service.annotations.service\\.beta\\.kubernetes\\.io/aws-load-balancer-manage-backend-security-group-rules" = tostring(try(each.value.nlb.manage_backend_security_group_rules, true))
      "higress-core.gateway.service.annotations.service\\.beta\\.kubernetes\\.io/aws-load-balancer-listener-attributes\\.TCP-80"        = "tcp.idle_timeout.seconds=${try(each.value.nlb.idle_timeout_seconds, 600)}"
      "higress-core.gateway.service.annotations.service\\.beta\\.kubernetes\\.io/aws-load-balancer-listener-attributes\\.TCP-443"       = "tcp.idle_timeout.seconds=${try(each.value.nlb.idle_timeout_seconds, 600)}"
      "higress-core.gateway.service.externalTrafficPolicy"                                                                              = try(each.value.nlb.external_traffic_policy, "Local")
      } : {
      "higress-core.gateway.service.annotations.service\\.beta\\.kubernetes\\.io/aws-load-balancer-type"                              = "nlb"
      "higress-core.gateway.service.annotations.service\\.beta\\.kubernetes\\.io/aws-load-balancer-cross-zone-load-balancing-enabled" = "true"
    }) : {}
    content {
      name  = set.key
      value = set.value
      type  = "string"
    }
  }

  # The platform creates one AlertmanagerConfig in the Prometheus namespace.
  # Select only that managed object; this prevents unrelated tenant configs in
  # a shared cluster from being merged into this environment's Alertmanager.
  dynamic "set" {
    for_each = each.key == "prometheus" ? {
      "alertmanager.alertmanagerSpec.alertmanagerConfigSelector.matchLabels.ops-deploy\\.io/managed"                  = "true"
      "alertmanager.alertmanagerSpec.alertmanagerConfigNamespaceSelector.matchLabels.kubernetes\\.io/metadata\\.name" = try(each.value.namespace, "monitoring")
      "alertmanager.alertmanagerSpec.alertmanagerConfigMatcherStrategy.type"                                          = "None"
    } : {}
    content {
      name  = set.key
      value = set.value
      type  = "string"
    }
  }

  dynamic "set_sensitive" {
    for_each = merge(
      each.key == "xxl_job" ? {
        "mysql.password" = random_password.xxl_job_mysql[0].result
        "admin.password" = random_password.xxl_job_admin[0].result
      } : {},
      each.key == "nacos" ? {
        "auth.token"         = base64encode(random_password.nacos_auth_token[0].result)
        "auth.identityValue" = random_password.nacos_identity_value[0].result
      } : {},
      each.key == "higress" ? {
        "higress-console.admin.password" = random_password.higress_admin[0].result
      } : {},
      contains(["mysql", "redis", "activemq", "mongodb"], each.key) ? {
        "auth.password" = random_password.data_service[each.key].result
      } : {},
      each.key == "bytebase" ? {
        "admin.password" = random_password.bytebase_admin[0].result
      } : {},
      each.key == "bytebase" && local.bytebase_mysql_integration ? {
        "integration.mysql.password" = random_password.data_service["mysql"].result
      } : {},
      each.key == "redisinsight" ? {
        "basicAuth.password"        = random_password.redisinsight_admin[0].result
        "encryptionKey"             = random_password.redisinsight_encryption[0].result
        "connection.redis.password" = random_password.data_service["redis"].result
      } : {},
      each.key == "etcd_workbench" ? {
        "basicAuth.password" = random_password.etcd_workbench_admin[0].result
        "encryptionKey"      = random_password.etcd_workbench_encryption[0].result
      } : {},
      each.key == "clickvisual_stack" ? {
        "secrets.mysqlPassword"      = random_password.clickvisual_mysql[0].result
        "secrets.clickhousePassword" = random_password.clickvisual_clickhouse[0].result
        "secrets.adminPassword"      = random_password.clickvisual_admin[0].result
        "secrets.proxyToken"         = random_password.clickvisual_proxy_token[0].result
        "secrets.secretKey"          = random_password.clickvisual_secret_key[0].result
        "secrets.encryptionKey"      = random_password.clickvisual_encryption_key[0].result
        "secrets.kafkaClusterId"     = random_id.clickvisual_kafka_cluster[0].b64_url
      } : {},
      each.key == "efk_stack" ? {
        "secrets.elasticPassword"           = random_password.efk_elastic[0].result
        "secrets.kibanaSystemPassword"      = random_password.efk_kibana_system[0].result
        "secrets.fluentdPassword"           = random_password.efk_fluentd[0].result
        "secrets.securityEncryptionKey"     = random_password.efk_security_key[0].result
        "secrets.savedObjectsEncryptionKey" = random_password.efk_saved_objects_key[0].result
        "secrets.reportingEncryptionKey"    = random_password.efk_reporting_key[0].result
      } : {},
      each.key == "jaeger" ? {
        "basicAuth.password" = random_password.jaeger_ui[0].result
      } : {},
      each.key == "jaeger" && try(each.value.values.storage.backend, "badger") == "elasticsearch" ? {
        "storage.elasticsearch.password" = random_password.otel_elasticsearch[0].result
      } : {}
    )
    content {
      name  = set_sensitive.key
      value = set_sensitive.value
      type  = "string"
    }
  }

  wait          = true
  wait_for_jobs = true
  # ClickVisual contains several stateful services. A hard failure such as a
  # full Kafka volume must not keep the whole deployment waiting for a
  # user-supplied 30-120 minute Helm timeout. Ten minutes is sufficient for a
  # normal first image pull and startup; the runner then records Pod events and
  # the failing container log with a direct recovery instruction.
  timeout = each.key == "clickvisual_stack" ? min(try(each.value.timeout, 600), 600) : try(each.value.timeout, 1200)

  depends_on = [
    aws_eks_addon.base,
    helm_release.otel_elasticsearch,
    kubernetes_namespace_v1.this,
    kubernetes_storage_class_v1.gp3
  ]
}

# OpenTelemetry owns a dedicated Elasticsearch lifecycle. It is intentionally
# not represented by the EFK release: disabling or upgrading EFK must never
# delete Trace/log indices used by the observability pipeline.
resource "helm_release" "otel_elasticsearch" {
  count = local.otel_elasticsearch_enabled ? 1 : 0

  name             = "otel-elasticsearch"
  chart            = "${path.module}/charts/otel-elasticsearch"
  namespace        = local.otel_namespace
  create_namespace = false
  values = [yamlencode(merge(local.otel_elasticsearch, {
    project      = local.project
    environment  = local.environment
    nodeSelector = local.workload_scheduling_enabled ? local.platform_node_selector : try(local.otel_elasticsearch.nodeSelector, {})
    tolerations  = local.workload_scheduling_enabled ? local.platform_tolerations : try(local.otel_elasticsearch.tolerations, [])
    allowedNamespaces = distinct(compact([
      local.otel_namespace,
      local.jaeger_enabled ? try(local.enabled_catalog_components.jaeger.namespace, "monitoring") : "",
      local.grafana_enabled ? local.grafana_namespace : ""
    ]))
  }))]

  set_sensitive {
    name  = "auth.password"
    value = random_password.otel_elasticsearch[0].result
    type  = "string"
  }

  wait          = true
  wait_for_jobs = true
  timeout       = try(local.otel_elasticsearch.timeout, 1200)

  depends_on = [
    aws_eks_addon.base,
    kubernetes_namespace_v1.this,
  ]
}

# Loki stores and queries logs, but it does not collect Kubernetes logs by
# itself. Install one unprivileged Alloy deployment per environment and stream
# every Pod container plus cluster events through the Kubernetes API. A single
# replica deliberately avoids duplicate ingestion; it can collect the whole
# cluster without a privileged DaemonSet or host filesystem mounts.
resource "helm_release" "loki_collector" {
  count = local.loki_enabled && !local.otel_agent_logs_enabled ? 1 : 0

  name             = "loki-alloy"
  repository       = "https://grafana.github.io/helm-charts"
  chart            = "alloy"
  version          = "1.10.1"
  namespace        = local.loki_namespace
  create_namespace = false

  values = [yamlencode({
    crds = {
      create = false
    }
    alloy = {
      enableReporting = false
      configMap = {
        content = <<-EOT
          logging {
            level  = "info"
            format = "logfmt"
          }

          discovery.kubernetes "pods" {
            role = "pod"
          }

          discovery.relabel "pod_logs" {
            targets = discovery.kubernetes.pods.targets

            rule {
              source_labels = ["__meta_kubernetes_namespace"]
              target_label  = "namespace"
            }
            rule {
              source_labels = ["__meta_kubernetes_pod_name"]
              target_label  = "pod"
            }
            rule {
              source_labels = ["__meta_kubernetes_pod_container_name"]
              target_label  = "container"
            }
            rule {
              source_labels = ["__meta_kubernetes_pod_node_name"]
              target_label  = "node"
            }
            rule {
              source_labels = ["__meta_kubernetes_pod_label_app_kubernetes_io_name"]
              regex         = "(.+)"
              target_label  = "app"
            }
            rule {
              source_labels = ["__meta_kubernetes_pod_controller_name"]
              regex         = "(.+)"
              target_label  = "workload"
            }
            rule {
              source_labels = ["__meta_kubernetes_namespace", "__meta_kubernetes_pod_container_name"]
              separator     = "/"
              target_label  = "job"
            }
            rule {
              target_label = "cluster"
              replacement  = "${local.cluster_name}"
            }
            rule {
              target_label = "environment"
              replacement  = "${local.environment}"
            }
          }

          loki.source.kubernetes "pods" {
            targets    = discovery.relabel.pod_logs.output
            forward_to = [loki.write.local.receiver]
          }

          loki.source.kubernetes_events "cluster" {
            job_name   = "integrations/kubernetes/eventhandler"
            log_format = "logfmt"
            forward_to = [loki.write.local.receiver]
          }

          loki.write "local" {
            endpoint {
              url = "${local.loki_gateway_url}/loki/api/v1/push"
            }
          }
        EOT
      }
      resources = {
        requests = {
          cpu    = "100m"
          memory = "256Mi"
        }
        limits = {
          cpu    = "1"
          memory = "1Gi"
        }
      }
    }
    controller = {
      type         = "deployment"
      replicas     = 1
      nodeSelector = local.platform_node_selector
      tolerations  = local.platform_tolerations
    }
    serviceMonitor = {
      enabled = local.grafana_enabled
    }
  })]

  wait          = true
  wait_for_jobs = true
  timeout       = 1200

  depends_on = [helm_release.catalog]
}

# kube-prometheus-stack runs a Grafana sidecar that watches ConfigMaps carrying
# this label. Provisioning through that sidecar keeps the data source
# idempotent and updates it automatically when the Loki Service or Namespace
# changes; no Grafana password or manual click-through is needed.
resource "kubernetes_config_map_v1" "grafana_loki_datasource" {
  count = local.loki_enabled && local.grafana_enabled ? 1 : 0

  metadata {
    name      = "loki-grafana-datasource"
    namespace = local.grafana_namespace
    labels = {
      grafana_datasource      = "1"
      "ops-deploy.io/managed" = "true"
    }
  }

  data = {
    "loki-datasource.yaml" = yamlencode({
      apiVersion = 1
      datasources = [{
        name      = "Loki"
        uid       = "loki"
        type      = "loki"
        access    = "proxy"
        url       = local.loki_gateway_url
        editable  = false
        isDefault = false
        jsonData = {
          timeout  = 60
          maxLines = 5000
        }
        version = 1
      }]
    })
  }

  depends_on = [helm_release.catalog, helm_release.loki_collector]
}

resource "random_password" "xxl_job_mysql" {
  count   = contains(keys(local.enabled_catalog_components), "xxl_job") ? 1 : 0
  length  = 32
  special = false
}

resource "random_password" "xxl_job_admin" {
  count   = contains(keys(local.enabled_catalog_components), "xxl_job") ? 1 : 0
  length  = 24
  special = false
}

resource "random_password" "nacos_auth_token" {
  count   = contains(keys(local.enabled_catalog_components), "nacos") ? 1 : 0
  length  = 32
  special = false
}

resource "random_password" "nacos_identity_value" {
  count   = contains(keys(local.enabled_catalog_components), "nacos") ? 1 : 0
  length  = 32
  special = false
}

resource "random_password" "higress_admin" {
  count   = contains(keys(local.enabled_catalog_components), "higress") ? 1 : 0
  length  = 24
  special = false
}

resource "random_password" "data_service" {
  for_each = toset([
    for key in ["mysql", "redis", "activemq", "mongodb"] : key
    if contains(keys(local.enabled_catalog_components), key)
  ])
  length  = 32
  special = false
}

resource "random_password" "bytebase_admin" {
  count   = contains(keys(local.enabled_catalog_components), "bytebase") ? 1 : 0
  length  = 24
  special = false
}

resource "random_password" "redisinsight_admin" {
  count   = contains(keys(local.enabled_catalog_components), "redisinsight") ? 1 : 0
  length  = 24
  special = false
}

resource "random_password" "redisinsight_encryption" {
  count   = contains(keys(local.enabled_catalog_components), "redisinsight") ? 1 : 0
  length  = 48
  special = false
}

resource "random_password" "etcd_workbench_admin" {
  count   = contains(keys(local.enabled_catalog_components), "etcd_workbench") ? 1 : 0
  length  = 24
  special = false
}

resource "random_password" "etcd_workbench_encryption" {
  count   = contains(keys(local.enabled_catalog_components), "etcd_workbench") ? 1 : 0
  length  = 16
  special = false
}

resource "random_password" "clickvisual_mysql" {
  count   = contains(keys(local.enabled_catalog_components), "clickvisual_stack") ? 1 : 0
  length  = 32
  special = false
}

resource "random_password" "clickvisual_clickhouse" {
  count   = contains(keys(local.enabled_catalog_components), "clickvisual_stack") ? 1 : 0
  length  = 32
  special = false
}

resource "random_password" "clickvisual_admin" {
  count   = contains(keys(local.enabled_catalog_components), "clickvisual_stack") ? 1 : 0
  length  = 24
  special = false
}

resource "random_password" "clickvisual_proxy_token" {
  count   = contains(keys(local.enabled_catalog_components), "clickvisual_stack") ? 1 : 0
  length  = 32
  special = false
}

resource "random_password" "clickvisual_secret_key" {
  count   = contains(keys(local.enabled_catalog_components), "clickvisual_stack") ? 1 : 0
  length  = 32
  special = false
}

resource "random_password" "clickvisual_encryption_key" {
  count   = contains(keys(local.enabled_catalog_components), "clickvisual_stack") ? 1 : 0
  length  = 32
  special = false
}

resource "random_id" "clickvisual_kafka_cluster" {
  count       = contains(keys(local.enabled_catalog_components), "clickvisual_stack") ? 1 : 0
  byte_length = 16
}

resource "random_password" "efk_elastic" {
  count   = contains(keys(local.enabled_catalog_components), "efk_stack") ? 1 : 0
  length  = 32
  special = false
}

resource "random_password" "otel_elasticsearch" {
  # Keep the password stable while the environment exists. Disabling the
  # optional release may retain its PVCs; regenerating the built-in `elastic`
  # password on re-enable would make those retained indices inaccessible.
  count   = 1
  length  = 32
  special = false
}

resource "random_password" "efk_kibana_system" {
  count   = contains(keys(local.enabled_catalog_components), "efk_stack") ? 1 : 0
  length  = 32
  special = false
}

resource "random_password" "efk_fluentd" {
  count   = contains(keys(local.enabled_catalog_components), "efk_stack") ? 1 : 0
  length  = 32
  special = false
}

resource "random_password" "efk_security_key" {
  count   = contains(keys(local.enabled_catalog_components), "efk_stack") ? 1 : 0
  length  = 48
  special = false
}

resource "random_password" "efk_saved_objects_key" {
  count   = contains(keys(local.enabled_catalog_components), "efk_stack") ? 1 : 0
  length  = 48
  special = false
}

resource "random_password" "efk_reporting_key" {
  count   = contains(keys(local.enabled_catalog_components), "efk_stack") ? 1 : 0
  length  = 48
  special = false
}

resource "random_password" "jaeger_ui" {
  count   = contains(keys(local.enabled_catalog_components), "jaeger") ? 1 : 0
  length  = 24
  special = false
}

resource "kubernetes_manifest" "tls_certificate" {
  for_each = local.managed_tls_certificates

  manifest = {
    apiVersion = "cert-manager.io/v1"
    kind       = "Certificate"
    metadata = {
      name      = try(each.value.certificate_name, each.key)
      namespace = try(each.value.namespace, "platform-server")
    }
    spec = {
      secretName = each.value.tls_secret_name
      dnsNames   = each.value.domains
      issuerRef = {
        name = try(each.value.issuer_name, "letsencrypt-prod")
        kind = try(each.value.issuer_kind, "ClusterIssuer")
      }
    }
  }

  depends_on = [helm_release.cert_manager]
}

resource "kubernetes_ingress_v1" "domain" {
  for_each = local.http_routes

  metadata {
    name = trimspace(try(each.value.name, "")) != "" ? each.value.name : (
      try(each.value.access_type, "domain") == "ip" ? "ip-route-${each.key}" : replace(each.value.domain, ".", "-")
    )
    namespace   = try(each.value.namespace, "platform-server")
    annotations = try(each.value.annotations, {})
    labels = {
      "app.kubernetes.io/managed-by"  = "ops-deploy-platform"
      "ops-deploy.io/managed-by"      = "deployment-config"
      "ops-deploy.io/project"         = local.project
      "ops-deploy.io/environment"     = local.environment
      "ops-deploy.io/config-position" = each.key
    }
  }

  spec {
    ingress_class_name = try(each.value.gateway, "higress")

    dynamic "tls" {
      for_each = try(each.value.access_type, "domain") != "ip" && try(each.value.tls_enabled, false) ? [1] : []
      content {
        hosts = [each.value.domain]
        secret_name = try(
          local.tls_certificates[try(each.value.certificate_ref, "")].tls_secret_name,
          each.value.tls_secret_name,
          "${replace(each.value.domain, ".", "-")}-tls"
        )
      }
    }

    rule {
      # Null creates a catch-all rule so the gateway LoadBalancer IP/DNS name can
      # be used directly when no domain is available.
      host = try(each.value.access_type, "domain") == "ip" ? null : each.value.domain
      http {
        dynamic "path" {
          # Existing environments store one backend directly on the domain.
          # New environments store one or more path backends in routes.
          for_each = try(each.value.routes, [each.value])
          content {
            path      = try(path.value.path, "/")
            path_type = try(path.value.path_type, "Prefix")
            backend {
              service {
                name = path.value.service
                port { number = path.value.service_port }
              }
            }
          }
        }
      }
    }
  }

}

# A Kubernetes Ingress is an HTTP routing primitive and must never be used for
# databases, message brokers or other raw TCP services. Each TCP rule gets its
# own AWS NLB-backed Service. The new Service mirrors the selected backend
# Service selector and targetPort, so workloads remain owned by their original
# Deployment/StatefulSet and no duplicate Pods are created.
data "kubernetes_service_v1" "tcp_backend" {
  for_each = local.tcp_routes

  metadata {
    name      = each.value.service
    namespace = each.value.namespace
  }
}

resource "kubernetes_service_v1" "tcp_route" {
  for_each = local.tcp_routes

  metadata {
    name      = substr(trimspace(try(each.value.name, "")) != "" ? each.value.name : "tcp-${each.value.service}-${each.key}", 0, 63)
    namespace = each.value.namespace
    labels = {
      "app.kubernetes.io/managed-by" = "ops-deploy-platform"
      "ops-deploy.io/route-type"     = "tcp-nlb"
    }
    annotations = merge(
      try(each.value.annotations, {}),
      local.manage_cluster_addons ? {
        "service.beta.kubernetes.io/aws-load-balancer-type"                 = "external"
        "service.beta.kubernetes.io/aws-load-balancer-nlb-target-type"      = "ip"
        "service.beta.kubernetes.io/aws-load-balancer-scheme"               = try(each.value.tcp_scheme, "internet-facing")
        "service.beta.kubernetes.io/aws-load-balancer-attributes"           = "load_balancing.cross_zone.enabled=true"
        "service.beta.kubernetes.io/aws-load-balancer-healthcheck-protocol" = "tcp"
        } : merge({
          "service.beta.kubernetes.io/aws-load-balancer-type"                              = "nlb"
          "service.beta.kubernetes.io/aws-load-balancer-cross-zone-load-balancing-enabled" = "true"
          "service.beta.kubernetes.io/aws-load-balancer-healthcheck-protocol"              = "tcp"
          }, try(each.value.tcp_scheme, "internet-facing") == "internal" ? {
          # The legacy in-tree AWS service controller treats this annotation as
          # an opt-in. Omit it entirely for public NLBs instead of writing false.
          "service.beta.kubernetes.io/aws-load-balancer-internal" = "true"
      } : {})
    )
  }

  spec {
    type                        = "LoadBalancer"
    external_traffic_policy     = "Cluster"
    load_balancer_source_ranges = try(each.value.allowed_cidrs, [])
    selector                    = data.kubernetes_service_v1.tcp_backend[each.key].spec[0].selector

    port {
      name     = "tcp-${try(each.value.external_port, each.value.service_port)}"
      protocol = "TCP"
      port     = try(each.value.external_port, each.value.service_port)
      target_port = try(one([
        for backend_port in data.kubernetes_service_v1.tcp_backend[each.key].spec[0].port : backend_port.target_port
        if backend_port.port == each.value.service_port
      ]), each.value.service_port)
    }
  }

  wait_for_load_balancer = true

  lifecycle {
    precondition {
      condition     = length(try(data.kubernetes_service_v1.tcp_backend[each.key].spec[0].selector, {})) > 0
      error_message = "TCP backend Service ${each.value.namespace}/${each.value.service} has no selector and cannot be exposed through a dedicated NLB Service."
    }
    precondition {
      condition = length([
        for backend_port in data.kubernetes_service_v1.tcp_backend[each.key].spec[0].port : backend_port.port
        if backend_port.port == each.value.service_port
      ]) == 1
      error_message = "TCP backend Service ${each.value.namespace}/${each.value.service} does not expose configured port ${each.value.service_port}."
    }
  }
}

resource "kubernetes_config_map_v1" "alerting" {
  count = local.phase_two_enabled && try(local.config.alerting.enabled, false) ? 1 : 0

  metadata {
    name      = "platform-alerting-catalog"
    namespace = try(local.config.alerting.namespace, "monitoring")
  }

  data = {
    "templates.json" = jsonencode(try(local.config.alerting.templates, []))
  }

}

resource "kubernetes_secret_v1" "alerting_channels" {
  count = local.phase_two_enabled && try(local.config.alerting.enabled, false) ? 1 : 0

  metadata {
    name      = "platform-alerting-channels"
    namespace = try(local.config.alerting.namespace, "monitoring")
  }

  type = "Opaque"
  data = {
    "channels.json" = jsonencode(try(local.config.alerting.channels, []))
  }

}

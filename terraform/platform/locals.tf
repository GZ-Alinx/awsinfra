data "aws_caller_identity" "current" {}

locals {
  config         = yamldecode(file(var.config_file))
  project        = local.config.project
  environment    = local.config.environment
  region         = local.config.region
  name_prefix    = "${local.project}-${local.environment}"
  target_type    = try(local.config.deployment_target.type, "managed")
  managed_target = local.target_type != "existing_eks"
  cluster_name   = local.managed_target ? "${local.name_prefix}-eks" : local.config.deployment_target.cluster_name
  # Existing EKS is shared infrastructure. Never create, update or delete its
  # cluster-level add-ons, storage classes or controller IAM integration.
  manage_cluster_addons = local.managed_target
  iam_name_prefix       = local.managed_target ? local.cluster_name : "${local.name_prefix}-existing"
  vpc_id                = data.aws_eks_cluster.this.vpc_config[0].vpc_id
  # Access-only runs must evaluate the same desired TLS/domain/alert objects as
  # a normal phase-two run. The runner then constrains the apply with an
  # explicit target allowlist, so unrelated Helm releases are never touched.
  # Treating access as disabled here would turn every targeted access resource
  # into count=0/for_each={}, which could plan destructive removals.
  phase_two_enabled = contains(["components", "access"], var.deployment_phase)

  common_tags = merge(
    {
      Project     = local.project
      Environment = local.environment
      ManagedBy   = "Terraform"
    },
    try(local.config.tags, {})
  )

  components            = local.config.components
  configured_namespaces = tomap(local.config.namespaces)

  # Preserve the native yamldecode object because node groups may legitimately
  # have different optional fields. A conditional between that object and {}
  # forces Terraform to unify two incompatible static object types when more
  # than one heterogeneous group exists. Keep the configuration intact and
  # gate only the derived names used by managed-cluster scheduling.
  node_groups                 = try(local.config.eks.node_groups, {})
  node_group_names            = local.managed_target ? sort(keys(local.node_groups)) : []
  workload_scheduling_enabled = local.managed_target && try(local.config.eks.workload_scheduling.enabled, false)
  platform_node_group_names = [
    for name in local.node_group_names : name
    if try(local.node_groups[name].labels["workload-class"], "") == "platform"
  ]
  gateway_node_group_names = [
    for name in local.node_group_names : name
    if try(local.node_groups[name].labels["workload-class"], "") == "gateway"
  ]
  application_node_group_names = [
    for name in local.node_group_names : name
    if try(local.node_groups[name].labels["workload-class"], "") == "application"
  ]
  selected_platform_node_group = contains(local.platform_node_group_names, "platform-ops") ? "platform-ops" : (length(local.platform_node_group_names) > 0 ? local.platform_node_group_names[0] : (length(local.node_group_names) > 0 ? local.node_group_names[0] : null))
  selected_gateway_node_group  = contains(local.gateway_node_group_names, "ingress-gateway") ? "ingress-gateway" : (length(local.gateway_node_group_names) > 0 ? local.gateway_node_group_names[0] : null)
  selected_application_node_group = contains(local.application_node_group_names, "business-workload") ? "business-workload" : (
    length(local.application_node_group_names) > 0 ? local.application_node_group_names[0] : null
  )
  platform_node_selector = local.selected_platform_node_group == null ? tomap({}) : (
    local.workload_scheduling_enabled
    ? tomap(merge(
      { "workload-class" = "platform" },
      try(local.node_groups[local.selected_platform_node_group].capacity_deferred, false) ? {} : {
        "ops-deploy.io/pool" = try(local.node_groups[local.selected_platform_node_group].labels["ops-deploy.io/pool"], local.selected_platform_node_group)
      }
    ))
    : tomap(try(local.node_groups[local.selected_platform_node_group].labels, {}))
  )
  gateway_node_selector = local.selected_gateway_node_group == null ? tomap({}) : tomap({
    "workload-class"     = "gateway"
    "ops-deploy.io/pool" = try(local.node_groups[local.selected_gateway_node_group].labels["ops-deploy.io/pool"], local.selected_gateway_node_group)
  })
  application_node_selector = local.selected_application_node_group == null ? tomap({}) : tomap({
    "workload-class"     = "application"
    "ops-deploy.io/pool" = try(local.node_groups[local.selected_application_node_group].labels["ops-deploy.io/pool"], local.selected_application_node_group)
  })
  platform_tolerations = local.workload_scheduling_enabled && local.selected_platform_node_group != null ? [{
    key      = "workload-class"
    operator = "Equal"
    value    = "platform"
    effect   = "NoSchedule"
  }] : []
  # Existing stateful platform services may already own zonal EBS PVCs that
  # predate the dedicated platform-ops pool. Pinning those StatefulSets to one
  # multi-AZ managed node group can make a replica permanently unschedulable:
  # Cluster Autoscaler cannot choose a specific AZ for an already-bound PVC.
  # Keep the strong workload-class isolation, but allow the scheduler to use
  # any platform node in the PVC's AZ. Stateless operational components still
  # use the exact ops-deploy.io/pool selector above.
  stateful_platform_node_selector = local.workload_scheduling_enabled && local.selected_platform_node_group != null ? tomap({
    "workload-class" = "platform"
  }) : local.platform_node_selector
  gateway_tolerations = local.workload_scheduling_enabled && local.selected_gateway_node_group != null ? [{
    key      = "workload-class"
    operator = "Equal"
    value    = "gateway"
    effect   = "NoSchedule"
  }] : []
  platform_node_selector_yaml          = length(local.platform_node_selector) == 0 ? "" : trimspace(yamlencode(local.platform_node_selector))
  platform_tolerations_yaml            = length(local.platform_tolerations) == 0 ? "" : trimspace(yamlencode(local.platform_tolerations))
  stateful_platform_node_selector_yaml = length(local.stateful_platform_node_selector) == 0 ? "" : trimspace(yamlencode(local.stateful_platform_node_selector))
  catalog_node_selector_paths = {
    jenkins                 = ["controller.nodeSelector.workload-class"]
    argocd                  = ["global.nodeSelector.workload-class"]
    gitlab                  = ["global.nodeSelector.workload-class"]
    kafka                   = ["controller.nodeSelector.workload-class", "broker.nodeSelector.workload-class"]
    prometheus              = ["prometheus.prometheusSpec.nodeSelector.workload-class", "alertmanager.alertmanagerSpec.nodeSelector.workload-class", "grafana.nodeSelector.workload-class", "kube-state-metrics.nodeSelector.workload-class", "prometheusOperator.nodeSelector.workload-class", "prometheusOperator.admissionWebhooks.patch.nodeSelector.workload-class"]
    opentelemetry_collector = ["nodeSelector.workload-class"]
    jaeger                  = ["nodeSelector.workload-class"]
    tempo                   = ["nodeSelector.workload-class"]
    loki                    = ["singleBinary.nodeSelector.workload-class", "gateway.nodeSelector.workload-class"]
    # The data-plane gateway runs on the gateway pool; controller and console
    # remain operational workloads on platform-ops.
    higress           = ["higress-core.controller.nodeSelector.workload-class", "higress-console.nodeSelector.workload-class"]
    nginx_ingress     = ["controller.nodeSelector.workload-class"]
    clickvisual_stack = ["nodeSelector.workload-class"]
    efk_stack         = ["nodeSelector.workload-class"]
  }
  # Stateful catalog components own zonal EBS volumes. During a node-pool
  # migration an existing PVC can be in an AZ where the newly selected
  # platform-ops pool has no node. Keep workload-class taint isolation, but do
  # not pin these releases to one exact managed node group.
  catalog_zonal_storage_components = toset([
    "activemq",
    "bytebase",
    "clickvisual_stack",
    "efk_stack",
    "etcd_workbench",
    "gitlab",
    "jaeger",
    "jenkins",
    "kafka",
    "loki",
    "mongodb",
    "mysql",
    "nacos",
    "opentelemetry_collector",
    "prometheus",
    "rabbitmq",
    "redis",
    "redisinsight",
    "tempo",
    "xxl_job"
  ])
  catalog_platform_toleration_paths = {
    jenkins                 = ["controller.tolerations"]
    argocd                  = ["global.tolerations"]
    gitlab                  = ["global.tolerations"]
    kafka                   = ["controller.tolerations", "broker.tolerations"]
    prometheus              = ["prometheus.prometheusSpec.tolerations", "alertmanager.alertmanagerSpec.tolerations", "grafana.tolerations", "kube-state-metrics.tolerations", "prometheusOperator.tolerations", "prometheusOperator.admissionWebhooks.patch.tolerations"]
    opentelemetry_collector = ["tolerations"]
    jaeger                  = ["tolerations"]
    tempo                   = ["tolerations"]
    loki                    = ["singleBinary.tolerations", "gateway.tolerations"]
    higress                 = ["higress-core.controller.tolerations", "higress-console.tolerations"]
    nginx_ingress           = ["controller.tolerations"]
    clickvisual_stack       = ["tolerations"]
    efk_stack               = ["tolerations"]
  }
  catalog_platform_toleration_set_values = {
    for component, paths in local.catalog_platform_toleration_paths : component => merge([
      for path in paths : {
        "${path}[0].key"      = "workload-class"
        "${path}[0].operator" = "Equal"
        "${path}[0].value"    = "platform"
        "${path}[0].effect"   = "NoSchedule"
      }
    ]...)
  }

  base_addons = local.manage_cluster_addons ? merge(
    try(local.components.eks_addons.vpc_cni, true) ? { vpc-cni = {} } : {},
    try(local.components.eks_addons.coredns, true) ? { coredns = {} } : {},
    try(local.components.eks_addons.kube_proxy, true) ? { kube-proxy = {} } : {},
    try(local.components.eks_addons.pod_identity_agent, true) ? { eks-pod-identity-agent = {} } : {}
  ) : {}

  ebs_csi_configured      = try(local.components.eks_addons.ebs_csi_driver, true)
  ebs_csi_enabled         = local.manage_cluster_addons && local.ebs_csi_configured
  lbc_enabled             = local.manage_cluster_addons && try(local.components.aws_load_balancer_controller.enabled, false)
  metrics_server_enabled  = local.manage_cluster_addons && try(local.components.metrics_server.enabled, false)
  autoscaler_enabled      = local.manage_cluster_addons && try(local.components.cluster_autoscaler.enabled, false)
  external_dns_configured = local.phase_two_enabled && try(local.components.external_dns.enabled, false)
  cert_manager_configured = local.phase_two_enabled && try(local.components.cert_manager.enabled, false)
  external_dns_enabled    = local.manage_cluster_addons && local.external_dns_configured
  cert_manager_enabled    = local.manage_cluster_addons && local.cert_manager_configured
  consul_enabled          = try(local.components.consul.enabled, false)
  etcd_enabled            = try(local.components.etcd.enabled, false)
  catalog_components      = try(local.components.catalog, {})
  enabled_catalog_components = {
    for key, component in local.catalog_components : key => component
    if local.phase_two_enabled && try(component.enabled, false)
  }
  # Component namespaces are prerequisites, not optional Helm side effects.
  # Include every enabled component target even for an older environment
  # document that predates automatic Namespace persistence. The Go defaults
  # layer saves these names back to the document, so disabling a component
  # later preserves the Namespace instead of planning its destruction.
  enabled_component_namespaces = toset(compact(concat(
    [
      local.consul_enabled ? try(local.components.consul.namespace, "platform-server") : "",
      local.etcd_enabled ? try(local.components.etcd.namespace, "platform-server") : ""
    ],
    [for _, component in local.enabled_catalog_components : try(component.namespace, "platform-server")]
  )))
  namespaces = merge(
    local.configured_namespaces,
    { for namespace in local.enabled_component_namespaces : namespace => {} }
  )
  loki_enabled    = contains(keys(local.enabled_catalog_components), "loki")
  grafana_enabled = contains(keys(local.enabled_catalog_components), "prometheus")
  tempo_enabled   = contains(keys(local.enabled_catalog_components), "tempo")
  jaeger_enabled  = contains(keys(local.enabled_catalog_components), "jaeger")
  efk_enabled     = contains(keys(local.enabled_catalog_components), "efk_stack")
  otel_enabled    = contains(keys(local.enabled_catalog_components), "opentelemetry_collector")
  otel_namespace  = try(local.enabled_catalog_components.opentelemetry_collector.namespace, "monitoring")
  otel_elasticsearch = try(
    local.enabled_catalog_components.opentelemetry_collector.values.elasticsearch,
    {}
  )
  otel_elasticsearch_enabled = local.otel_enabled && try(local.otel_elasticsearch.enabled, false)
  otel_elasticsearch_url     = "http://otel-elasticsearch.${local.otel_namespace}.svc.cluster.local:9200"
  otel_agent_logs_enabled = try(
    local.enabled_catalog_components.opentelemetry_collector.values.agent.enabled &&
    local.enabled_catalog_components.opentelemetry_collector.values.agent.logs.enabled,
    false
  )
  bytebase_mysql_integration = (
    contains(keys(local.enabled_catalog_components), "bytebase") &&
    contains(keys(local.enabled_catalog_components), "mysql")
  )
  redisinsight_redis_integration = (
    contains(keys(local.enabled_catalog_components), "redisinsight") &&
    contains(keys(local.enabled_catalog_components), "redis")
  )
  loki_namespace        = try(local.enabled_catalog_components["loki"].namespace, "monitoring")
  loki_service_name     = try(local.enabled_catalog_components["loki"].service_name, "loki-gateway")
  loki_service_port     = try(local.enabled_catalog_components["loki"].service_port, 80)
  loki_gateway_url      = "http://${local.loki_service_name}.${local.loki_namespace}.svc.cluster.local:${local.loki_service_port}"
  grafana_namespace     = try(local.enabled_catalog_components["prometheus"].namespace, "monitoring")
  tempo_namespace       = try(local.enabled_catalog_components["tempo"].namespace, "monitoring")
  tempo_service_name    = try(local.enabled_catalog_components["tempo"].service_name, "tempo")
  tempo_service_port    = try(local.enabled_catalog_components["tempo"].service_port, 3200)
  tempo_url             = "http://${local.tempo_service_name}.${local.tempo_namespace}.svc.cluster.local:${local.tempo_service_port}"
  efk_namespace         = try(local.enabled_catalog_components["efk_stack"].namespace, "monitoring")
  efk_elasticsearch_url = "http://efk-elasticsearch.${local.efk_namespace}.svc.cluster.local:9200"
  loki_ebs_persistence_enabled = try(
    local.enabled_catalog_components.loki.values.loki.storage.type == "filesystem" &&
    local.enabled_catalog_components.loki.values.singleBinary.persistence.enabled,
    false
  )
  jaeger_badger_persistence_enabled = try(
    local.enabled_catalog_components.jaeger.values.storage.backend == "badger",
    false
  )
  domains = {
    for index, domain in try(local.config.domains, []) : tostring(index) => domain
    if local.phase_two_enabled && try(domain.enabled, true)
  }
  http_routes = {
    for key, domain in local.domains : key => domain
    if lower(try(domain.protocol, try(domain.tls_enabled, false) ? "https" : "http")) != "tcp"
  }
  tcp_routes = {
    for key, domain in local.domains : key => domain
    if lower(try(domain.protocol, try(domain.tls_enabled, false) ? "https" : "http")) == "tcp"
  }
  tls_certificates = {
    for certificate in try(local.config.tls.certificates, []) : certificate.key => certificate
    if local.phase_two_enabled && try(certificate.enabled, true)
  }
  managed_tls_certificates = {
    for key, certificate in local.tls_certificates : key => certificate
    if try(certificate.mode, "cert-manager") == "cert-manager"
  }

  consul_backup_enabled = local.consul_enabled && try(local.components.consul.backup.enabled, false)
  etcd_backup_enabled   = local.etcd_enabled && try(local.components.etcd.backup.enabled, false)
  backup_enabled        = local.consul_backup_enabled || local.etcd_backup_enabled
  backup_bucket         = "${local.name_prefix}-platform-backups-${data.aws_caller_identity.current.account_id}-${local.region}"
  backup_namespaces = toset(compact([
    local.consul_backup_enabled ? local.components.consul.namespace : "",
    local.etcd_backup_enabled ? local.components.etcd.namespace : ""
  ]))
}

check "namespace_names" {
  assert {
    condition = alltrue([
      for name in keys(local.namespaces) :
      can(regex("^[a-z0-9]([-a-z0-9]*[a-z0-9])?$", name)) && length(name) <= 63
    ])
    error_message = "Kubernetes namespace names must be RFC 1123 labels: lowercase letters, digits and hyphens only."
  }
}

check "required_component_namespaces" {
  assert {
    condition = (
      (!local.consul_enabled || contains(keys(local.namespaces), local.components.consul.namespace)) &&
      (!local.etcd_enabled || contains(keys(local.namespaces), local.components.etcd.namespace))
    )
    error_message = "The Consul and etcd namespace values must exist in the top-level namespaces map."
  }
}

check "stateful_storage" {
  assert {
    condition     = (!local.consul_enabled && !local.etcd_enabled) || local.ebs_csi_configured
    error_message = "Consul or etcd requires components.eks_addons.ebs_csi_driver=true."
  }
}

check "catalog_stateful_storage" {
  assert {
    condition     = (!local.loki_ebs_persistence_enabled && !local.jaeger_badger_persistence_enabled && !local.otel_elasticsearch_enabled) || local.ebs_csi_configured
    error_message = "Loki, Jaeger Badger or OpenTelemetry Elasticsearch persistence requires components.eks_addons.ebs_csi_driver=true."
  }
}

check "loki_logging_stack" {
  assert {
    condition     = !local.loki_enabled || local.grafana_enabled
    error_message = "Loki requires the Prometheus + Grafana component so logs have a managed query UI and data source."
  }
}

check "database_console_integrations" {
  assert {
    condition = (
      (!contains(keys(local.enabled_catalog_components), "bytebase") || local.bytebase_mysql_integration) &&
      (!contains(keys(local.enabled_catalog_components), "redisinsight") || local.redisinsight_redis_integration)
    )
    error_message = "Bytebase requires the self-hosted MySQL component, and RedisInsight requires the self-hosted Redis component."
  }
}

check "stateful_workload_nodes" {
  assert {
    condition     = !local.managed_target || (!local.consul_enabled && !local.etcd_enabled) || length(local.node_group_names) > 0
    error_message = "Consul or etcd requires at least one EKS node group."
  }
}

check "deployment_target" {
  assert {
    condition = (
      contains(["managed", "existing_eks"], local.target_type) &&
      (local.managed_target || can(regex("^[A-Za-z0-9][A-Za-z0-9_-]{0,99}$", local.cluster_name)))
    )
    error_message = "deployment_target must be managed, or existing_eks with a valid cluster name."
  }
}

check "pod_identity_agent" {
  assert {
    condition = (
      !local.ebs_csi_enabled && !local.lbc_enabled && !local.autoscaler_enabled &&
      !local.external_dns_enabled && !local.backup_enabled
    ) || try(local.components.eks_addons.pod_identity_agent, true)
    error_message = "AWS-integrated add-ons and backup jobs require components.eks_addons.pod_identity_agent=true."
  }
}

check "tls_certificate_provider" {
  assert {
    condition     = length(local.managed_tls_certificates) == 0 || local.cert_manager_configured
    error_message = "cert-manager TLS certificates require components.cert_manager.enabled=true."
  }
}

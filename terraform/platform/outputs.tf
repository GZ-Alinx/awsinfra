output "cluster_name" {
  value = local.cluster_name
}

output "namespaces" {
  value = keys(kubernetes_namespace_v1.this)
}

output "enabled_components" {
  value = {
    aws_load_balancer_controller = local.lbc_enabled
    metrics_server               = local.metrics_server_enabled
    cluster_autoscaler           = local.autoscaler_enabled
    external_dns                 = local.external_dns_enabled
    cert_manager                 = local.cert_manager_enabled
    consul                       = local.consul_enabled
    etcd                         = local.etcd_enabled
    ebs_csi_driver               = local.ebs_csi_enabled
  }
}

output "consul_address" {
  value = local.consul_enabled ? "http://consul-http.${local.components.consul.namespace}.svc.cluster.local:8500" : null
}

output "consul_web_address" {
  value = local.consul_enabled ? "https://consul-ui.${local.components.consul.namespace}.svc.cluster.local:443" : null
}

output "etcd_address" {
  value = local.etcd_enabled ? "${try(local.components.etcd.tls_enabled, true) ? "https" : "http"}://etcd.${local.components.etcd.namespace}.svc.cluster.local:2379" : null
}

output "etcd_web_address" {
  value = local.etcd_enabled && try(local.components.etcd.web_ui.enabled, true) ? "http://etcd-web.${local.components.etcd.namespace}.svc.cluster.local:80" : null
}

output "backup_bucket" {
  value = local.backup_enabled ? local.backup_bucket : null
}

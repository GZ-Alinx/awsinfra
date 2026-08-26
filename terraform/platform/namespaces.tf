resource "kubernetes_namespace_v1" "this" {
  for_each = local.namespaces

  # A Namespace is a shared failure boundary: deleting it also deletes every
  # workload, Secret, Service, Ingress and PVC inside it. Component lifecycle
  # operations must therefore never be allowed to destroy this resource.
  lifecycle {
    prevent_destroy = true
  }

  metadata {
    name = each.key
    labels = {
      "app.kubernetes.io/part-of" = local.project
      environment                 = local.environment
    }
  }
}

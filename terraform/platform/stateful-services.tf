resource "helm_release" "consul" {
  count = local.consul_enabled ? 1 : 0

  name       = "consul"
  repository = "https://helm.releases.hashicorp.com"
  chart      = "consul"
  version    = local.components.consul.chart_version
  namespace  = local.components.consul.namespace

  values = [yamlencode({
    global = {
      name       = "consul"
      datacenter = "${local.name_prefix}-dc1"
      image      = local.components.consul.image
      tls = {
        enabled           = true
        enableAutoEncrypt = true
        verify            = true
        httpsOnly         = false
      }
      acls = {
        manageSystemACLs = true
      }
      gossipEncryption = {
        autoGenerate = true
      }
    }
    server = {
      enabled         = true
      replicas        = local.components.consul.replicas
      bootstrapExpect = local.components.consul.replicas
      storage         = local.components.consul.storage_size
      storageClass    = local.components.consul.storage_class
      persistentVolumeClaimRetentionPolicy = {
        whenDeleted = try(local.components.consul.retain_pvc_on_delete, true) ? "Retain" : "Delete"
        whenScaled  = "Retain"
      }
      connect      = false
      nodeSelector = local.stateful_platform_node_selector_yaml
      tolerations  = local.platform_tolerations_yaml
      disruptionBudget = {
        enabled        = true
        maxUnavailable = 1
      }
      topologySpreadConstraints = <<-EOT
        - maxSkew: 1
          topologyKey: topology.kubernetes.io/zone
          whenUnsatisfiable: DoNotSchedule
          labelSelector:
            matchLabels:
              app: {{ template "consul.name" . }}
              release: "{{ .Release.Name }}"
              component: server
      EOT
    }
    client = {
      enabled = false
    }
    connectInject = {
      enabled = false
    }
    dns = {
      enabled = false
    }
    ui = {
      enabled = true
      service = {
        type = "ClusterIP"
      }
    }
  })]

  wait    = true
  timeout = 600

  depends_on = [
    kubernetes_namespace_v1.this,
    kubernetes_storage_class_v1.gp3
  ]
}

resource "kubernetes_service_v1" "consul_http" {
  count = local.consul_enabled ? 1 : 0

  metadata {
    name      = "consul-http"
    namespace = local.components.consul.namespace
    labels = {
      app       = "consul"
      component = "server"
      release   = "consul"
    }
  }

  spec {
    selector = {
      app       = "consul"
      component = "server"
      release   = "consul"
    }

    port {
      name         = "http"
      port         = 8500
      target_port  = "8500"
      protocol     = "TCP"
      app_protocol = "http"
    }

    type = "ClusterIP"
  }

  depends_on = [helm_release.consul]
}

resource "tls_private_key" "etcd_ca" {
  count = local.etcd_enabled ? 1 : 0

  algorithm   = "ECDSA"
  ecdsa_curve = "P256"
}

resource "tls_self_signed_cert" "etcd_ca" {
  count = local.etcd_enabled ? 1 : 0

  private_key_pem       = tls_private_key.etcd_ca[0].private_key_pem
  validity_period_hours = 87600
  is_ca_certificate     = true

  subject {
    common_name  = "${local.cluster_name}-etcd-ca"
    organization = local.project
  }

  allowed_uses = [
    "cert_signing",
    "crl_signing",
    "digital_signature"
  ]
}

resource "tls_private_key" "etcd" {
  count = local.etcd_enabled ? 1 : 0

  algorithm   = "ECDSA"
  ecdsa_curve = "P256"
}

resource "tls_cert_request" "etcd" {
  count = local.etcd_enabled ? 1 : 0

  private_key_pem = tls_private_key.etcd[0].private_key_pem
  dns_names = [
    "etcd",
    "etcd.${local.components.etcd.namespace}",
    "etcd.${local.components.etcd.namespace}.svc",
    "etcd.${local.components.etcd.namespace}.svc.cluster.local",
    "etcd-client",
    "etcd-client.${local.components.etcd.namespace}",
    "etcd-client.${local.components.etcd.namespace}.svc",
    "etcd-client.${local.components.etcd.namespace}.svc.cluster.local",
    "*.etcd-headless.${local.components.etcd.namespace}.svc.cluster.local",
    "localhost"
  ]
  ip_addresses = ["127.0.0.1"]

  subject {
    common_name  = "etcd"
    organization = local.project
  }
}

resource "tls_locally_signed_cert" "etcd" {
  count = local.etcd_enabled ? 1 : 0

  cert_request_pem      = tls_cert_request.etcd[0].cert_request_pem
  ca_private_key_pem    = tls_private_key.etcd_ca[0].private_key_pem
  ca_cert_pem           = tls_self_signed_cert.etcd_ca[0].cert_pem
  validity_period_hours = 8760
  early_renewal_hours   = 720

  allowed_uses = [
    "digital_signature",
    "key_encipherment",
    "server_auth",
    "client_auth"
  ]
}

resource "kubernetes_secret_v1" "etcd_tls" {
  count = local.etcd_enabled ? 1 : 0

  metadata {
    name      = "etcd-tls"
    namespace = kubernetes_namespace_v1.this[local.components.etcd.namespace].metadata[0].name
  }

  data = {
    "ca.crt"  = tls_self_signed_cert.etcd_ca[0].cert_pem
    "tls.crt" = tls_locally_signed_cert.etcd[0].cert_pem
    "tls.key" = tls_private_key.etcd[0].private_key_pem
  }

  type = "Opaque"
}

resource "random_password" "etcd_web" {
  count = local.etcd_enabled && try(local.components.etcd.web_ui.enabled, true) ? 1 : 0

  length  = 24
  special = false
}

resource "helm_release" "etcd" {
  count = local.etcd_enabled ? 1 : 0

  name      = "etcd"
  chart     = "${path.module}/charts/etcd"
  namespace = local.components.etcd.namespace

  values = [yamlencode({
    replicaCount  = local.components.etcd.replicas
    image         = local.components.etcd.image
    tlsSecretName = kubernetes_secret_v1.etcd_tls[0].metadata[0].name
    tls = {
      enabled = try(local.components.etcd.tls_enabled, true)
    }
    storage = {
      className      = local.components.etcd.storage_class
      size           = local.components.etcd.storage_size
      retainOnDelete = try(local.components.etcd.retain_pvc_on_delete, true)
    }
    webUI = {
      enabled        = try(local.components.etcd.web_ui.enabled, true)
      username       = try(local.components.etcd.web_ui.username, "admin")
      password       = try(random_password.etcd_web[0].result, "")
      backendImage   = try(local.components.etcd.web_ui.backend_image, "ghcr.io/etcdfinder/etcdfinder:latest@sha256:657d1bf0990c048a78905cceeea65e2d2ed95573dce9c9b91e5936d8be1a61b4")
      frontendImage  = try(local.components.etcd.web_ui.frontend_image, "ghcr.io/etcdfinder/etcdfinder-ui:latest@sha256:479f1c63834000b824c1be500b7ebe0cf10065c5bdba19a3bfdc9dee1a2ab58a")
      searchImage    = try(local.components.etcd.web_ui.search_image, "getmeili/meilisearch:v1.15.2@sha256:fe500cf9cca05cb9f027981583f28eccf17d35d94499c1f8b7b844e7418152fc")
      grpcProxyImage = try(local.components.etcd.web_ui.grpc_proxy_image, "envoyproxy/envoy:distroless-v1.38.0@sha256:a7a56545102f7a682e0cafea2c9b8448af1b09ebb710eab688dfb931e3ec7ff6")
      authProxyImage = try(local.components.etcd.web_ui.auth_proxy_image, "nginx:1.28.0-alpine@sha256:30f1c0d78e0ad60901648be663a710bdadf19e4c10ac6782c235200619158284")
    }
    nodeSelector = local.stateful_platform_node_selector
    tolerations  = local.platform_tolerations
  })]

  wait    = true
  timeout = 600

  depends_on = [
    kubernetes_secret_v1.etcd_tls,
    kubernetes_storage_class_v1.gp3
  ]
}

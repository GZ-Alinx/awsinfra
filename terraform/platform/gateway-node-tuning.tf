# Dedicated gateway nodes carry long-lived HTTP/WebSocket connections. Apply
# the small set of host networking limits that cannot be set from an
# unprivileged Envoy container. The DaemonSet is scoped by both pool labels and
# the gateway taint, so it cannot run on business or platform nodes.
resource "kubernetes_daemon_set_v1" "gateway_node_tuning" {
  count = local.phase_two_enabled && local.workload_scheduling_enabled && local.selected_gateway_node_group != null ? 1 : 0

  metadata {
    name      = "ops-deploy-gateway-node-tuning"
    namespace = "kube-system"
    labels = {
      "app.kubernetes.io/name"       = "gateway-node-tuning"
      "app.kubernetes.io/managed-by" = "ops-deploy-platform"
    }
  }

  spec {
    selector {
      match_labels = {
        "app.kubernetes.io/name" = "gateway-node-tuning"
      }
    }

    template {
      metadata {
        labels = {
          "app.kubernetes.io/name" = "gateway-node-tuning"
        }
      }

      spec {
        host_network  = true
        host_pid      = true
        node_selector = local.gateway_node_selector

        dynamic "toleration" {
          for_each = local.gateway_tolerations
          content {
            key      = toleration.value.key
            operator = toleration.value.operator
            value    = toleration.value.value
            effect   = toleration.value.effect
          }
        }

        container {
          name  = "sysctl"
          image = "public.ecr.aws/docker/library/busybox:1.36.1"
          command = ["/bin/sh", "-ec", <<-EOT
            sysctl -w net.core.somaxconn=65535
            sysctl -w net.ipv4.ip_local_port_range='10240 65535'
            sysctl -w net.ipv4.tcp_keepalive_time=600
            sysctl -w net.ipv4.tcp_fin_timeout=30
            sysctl -w fs.file-max=2097152
            if [ -f /proc/sys/net/netfilter/nf_conntrack_max ]; then
              sysctl -w net.netfilter.nf_conntrack_max=1048576
            fi
            while true; do sleep 3600; done
          EOT
          ]

          security_context {
            privileged                 = true
            allow_privilege_escalation = true
            run_as_user                = 0
          }

          resources {
            requests = {
              cpu    = "10m"
              memory = "16Mi"
            }
            limits = {
              cpu    = "100m"
              memory = "64Mi"
            }
          }
        }
      }
    }
  }
}

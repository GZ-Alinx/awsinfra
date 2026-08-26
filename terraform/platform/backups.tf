resource "kubernetes_cron_job_v1" "consul_backup" {
  count = local.consul_backup_enabled ? 1 : 0

  metadata {
    name      = "consul-snapshot-backup"
    namespace = local.components.consul.namespace
  }

  spec {
    schedule                      = local.components.consul.backup.schedule
    concurrency_policy            = "Forbid"
    successful_jobs_history_limit = 2
    failed_jobs_history_limit     = 3

    job_template {
      metadata {}

      spec {
        backoff_limit           = 2
        active_deadline_seconds = 600

        template {
          metadata {}

          spec {
            service_account_name = kubernetes_service_account_v1.platform_backup[local.components.consul.namespace].metadata[0].name
            restart_policy       = "Never"

            container {
              name    = "snapshot"
              image   = "curlimages/curl:8.16.0"
              command = ["/bin/sh", "-ec"]
              args = [<<-EOT
                rm -f /backup/snapshot.snap /backup/snapshot.ready
                curl --fail --silent --show-error \
                  --cacert /consul/tls/ca/tls.crt \
                  --header "X-Consul-Token: $${CONSUL_HTTP_TOKEN}" \
                  --output /backup/snapshot.snap \
                  https://consul-server:8501/v1/snapshot
                touch /backup/snapshot.ready
              EOT
              ]

              env {
                name = "CONSUL_HTTP_TOKEN"
                value_from {
                  secret_key_ref {
                    name = "consul-bootstrap-acl-token"
                    key  = "token"
                  }
                }
              }

              volume_mount {
                name       = "backup"
                mount_path = "/backup"
              }

              volume_mount {
                name       = "consul-ca"
                mount_path = "/consul/tls/ca"
                read_only  = true
              }

              resources {
                requests = {
                  cpu    = "50m"
                  memory = "64Mi"
                }
                limits = {
                  cpu    = "500m"
                  memory = "256Mi"
                }
              }
            }

            container {
              name    = "upload"
              image   = "public.ecr.aws/aws-cli/aws-cli:2.31.30"
              command = ["/bin/sh", "-ec"]
              args = [<<-EOT
                until [ -f /backup/snapshot.ready ]; do sleep 2; done
                aws s3 cp /backup/snapshot.snap \
                  "s3://${local.backup_bucket}/consul/consul-$(date -u +%Y%m%dT%H%M%SZ).snap"
              EOT
              ]

              volume_mount {
                name       = "backup"
                mount_path = "/backup"
              }
            }

            volume {
              name = "backup"
              empty_dir {}
            }

            volume {
              name = "consul-ca"
              secret {
                secret_name = "consul-ca-cert"
              }
            }
          }
        }
      }
    }
  }

  depends_on = [
    helm_release.consul,
    aws_eks_pod_identity_association.platform_backup
  ]
}

resource "kubernetes_cron_job_v1" "etcd_backup" {
  count = local.etcd_backup_enabled ? 1 : 0

  metadata {
    name      = "etcd-snapshot-backup"
    namespace = local.components.etcd.namespace
  }

  spec {
    schedule                      = local.components.etcd.backup.schedule
    concurrency_policy            = "Forbid"
    successful_jobs_history_limit = 2
    failed_jobs_history_limit     = 3

    job_template {
      metadata {}

      spec {
        backoff_limit           = 2
        active_deadline_seconds = 600

        template {
          metadata {}

          spec {
            service_account_name = kubernetes_service_account_v1.platform_backup[local.components.etcd.namespace].metadata[0].name
            restart_policy       = "Never"

            security_context {
              fs_group = 1000
            }

            container {
              name    = "snapshot"
              image   = local.components.etcd.image
              command = ["/usr/local/bin/etcdctl"]
              args = concat(
                ["--endpoints=${try(local.components.etcd.tls_enabled, true) ? "https" : "http"}://etcd:2379"],
                try(local.components.etcd.tls_enabled, true) ? [
                  "--cacert=/etc/etcd/tls/ca.crt",
                  "--cert=/etc/etcd/tls/tls.crt",
                  "--key=/etc/etcd/tls/tls.key",
                ] : [],
                ["snapshot", "save", "/backup/snapshot.db"],
              )

              env {
                name  = "ETCDCTL_API"
                value = "3"
              }

              volume_mount {
                name       = "backup"
                mount_path = "/backup"
              }

              volume_mount {
                name       = "etcd-tls"
                mount_path = "/etc/etcd/tls"
                read_only  = true
              }
            }

            container {
              name    = "upload"
              image   = "public.ecr.aws/aws-cli/aws-cli:2.31.30"
              command = ["/bin/sh", "-ec"]
              args = [<<-EOT
                # etcdctl writes snapshot.db.part first and atomically renames
                # it to snapshot.db only after the snapshot is complete.
                until [ -s /backup/snapshot.db ]; do sleep 2; done
                aws s3 cp /backup/snapshot.db \
                  "s3://${local.backup_bucket}/etcd/etcd-$(date -u +%Y%m%dT%H%M%SZ).db"
              EOT
              ]

              volume_mount {
                name       = "backup"
                mount_path = "/backup"
              }
            }

            volume {
              name = "backup"
              empty_dir {}
            }

            volume {
              name = "etcd-tls"
              secret {
                secret_name = kubernetes_secret_v1.etcd_tls[0].metadata[0].name
              }
            }
          }
        }
      }
    }
  }

  depends_on = [
    helm_release.etcd,
    aws_eks_pod_identity_association.platform_backup
  ]
}

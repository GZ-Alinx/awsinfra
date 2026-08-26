locals {
  consul_backup_enabled = try(local.config.components.consul.enabled, false) && try(local.config.components.consul.backup.enabled, false)
  etcd_backup_enabled   = try(local.config.components.etcd.enabled, false) && try(local.config.components.etcd.backup.enabled, false)
  # The bucket is backup history, not a runtime component. Turning off Consul
  # or etcd must stop their CronJobs without erasing the backups operators may
  # need during a rollback. It remains managed while either backup policy is
  # configured; deleting backup storage requires explicitly disabling both
  # backup policies rather than merely uninstalling the services.
  platform_backup_enabled = (
    try(local.config.components.consul.backup.enabled, false) ||
    try(local.config.components.etcd.backup.enabled, false)
  )
  backup_retention_days = max(
    try(local.config.components.consul.backup.retention_days, 1),
    try(local.config.components.etcd.backup.retention_days, 1)
  )
}

resource "aws_s3_bucket" "platform_backups" {
  count = local.platform_backup_enabled ? 1 : 0

  bucket        = "${local.name_prefix}-platform-backups-${data.aws_caller_identity.current.account_id}-${local.region}"
  force_destroy = true
}

resource "aws_s3_bucket_public_access_block" "platform_backups" {
  count = local.platform_backup_enabled ? 1 : 0

  bucket                  = aws_s3_bucket.platform_backups[0].id
  block_public_acls       = true
  block_public_policy     = true
  ignore_public_acls      = true
  restrict_public_buckets = true
}

resource "aws_s3_bucket_versioning" "platform_backups" {
  count = local.platform_backup_enabled ? 1 : 0

  bucket = aws_s3_bucket.platform_backups[0].id
  versioning_configuration {
    status = "Enabled"
  }
}

resource "aws_s3_bucket_server_side_encryption_configuration" "platform_backups" {
  count = local.platform_backup_enabled ? 1 : 0

  bucket = aws_s3_bucket.platform_backups[0].id
  rule {
    apply_server_side_encryption_by_default {
      sse_algorithm = "AES256"
    }
  }
}

resource "aws_s3_bucket_lifecycle_configuration" "platform_backups" {
  count = local.platform_backup_enabled ? 1 : 0

  bucket = aws_s3_bucket.platform_backups[0].id

  rule {
    id     = "expire-platform-backups"
    status = "Enabled"

    filter {}

    expiration {
      days = local.backup_retention_days
    }

    noncurrent_version_expiration {
      noncurrent_days = local.backup_retention_days
    }
  }

  depends_on = [aws_s3_bucket_versioning.platform_backups]
}

resource "aws_kms_key" "data" {
  count = local.rds_config.enabled || local.aurora_config.enabled || local.postgres_config.enabled || local.documentdb_config.enabled || local.elasticache_config.enabled || local.msk_config.enabled || local.amazon_mq_config.enabled ? 1 : 0

  description             = "KMS key for ${local.name_prefix} managed data services"
  deletion_window_in_days = 7
  enable_key_rotation     = true
}

resource "aws_kms_alias" "data" {
  count = length(aws_kms_key.data)

  name          = "alias/${local.name_prefix}-data"
  target_key_id = aws_kms_key.data[0].key_id
}

resource "aws_db_instance" "admin" {
  count = local.rds_config.enabled ? 1 : 0

  identifier     = "${local.name_prefix}-admin"
  engine         = local.rds_config.engine
  engine_version = try(local.rds_config.engine_version, null)
  instance_class = local.rds_config.instance_class

  db_name  = local.rds_config.database_name
  username = local.rds_config.master_username
  port     = local.rds_config.port

  # The AWS provider treats an explicit false as configured, so it still
  # conflicts with password. Omit the argument entirely for self-managed
  # credentials and set it only when RDS owns the password lifecycle.
  manage_master_user_password   = local.rds_credentials_self_managed ? null : true
  master_user_secret_kms_key_id = local.rds_credentials_self_managed ? null : aws_kms_key.data[0].arn
  password                      = local.rds_credentials_self_managed ? lookup(var.data_service_passwords, "rds", null) : null

  allocated_storage     = local.rds_config.allocated_storage
  max_allocated_storage = local.rds_config.max_allocated_storage
  storage_type          = "gp3"
  storage_encrypted     = true
  kms_key_id            = aws_kms_key.data[0].arn

  db_subnet_group_name   = aws_db_subnet_group.this.name
  vpc_security_group_ids = [aws_security_group.rds[0].id]
  publicly_accessible    = false
  multi_az               = try(local.rds_config.multi_az, false)

  backup_retention_period    = local.rds_config.backup_retention_days
  backup_window              = try(local.rds_config.backup_window, "18:00-19:00")
  maintenance_window         = try(local.rds_config.maintenance_window, "sun:19:00-sun:20:00")
  copy_tags_to_snapshot      = true
  auto_minor_version_upgrade = try(local.rds_config.auto_minor_version_upgrade, false)

  deletion_protection       = try(local.rds_config.deletion_protection, false)
  skip_final_snapshot       = try(local.rds_config.skip_final_snapshot, true)
  final_snapshot_identifier = try(local.rds_config.skip_final_snapshot, true) ? null : "${local.name_prefix}-admin-final"

  performance_insights_enabled          = try(local.rds_config.performance_insights_enabled, true)
  performance_insights_retention_period = try(local.rds_config.performance_insights_enabled, true) ? 7 : null
  performance_insights_kms_key_id       = try(local.rds_config.performance_insights_enabled, true) ? aws_kms_key.data[0].arn : null

  apply_immediately = try(local.rds_config.apply_immediately, true)

  lifecycle {
    precondition {
      condition     = !local.rds_credentials_self_managed || try(length(var.data_service_passwords["rds"]) >= 8, false)
      error_message = "RDS self-managed credentials require a runtime master password from the platform credential vault."
    }
  }
}

resource "aws_secretsmanager_secret" "rds" {
  count = local.rds_config.enabled && local.rds_credentials_self_managed ? 1 : 0

  name                    = "${local.name_prefix}/rds/admin"
  kms_key_id              = aws_kms_key.data[0].arn
  recovery_window_in_days = 0
}

resource "aws_secretsmanager_secret_version" "rds" {
  count = local.rds_config.enabled && local.rds_credentials_self_managed ? 1 : 0

  secret_id = aws_secretsmanager_secret.rds[0].id
  secret_string = jsonencode({
    username = local.rds_config.master_username
    password = var.data_service_passwords["rds"]
    host     = aws_db_instance.admin[0].address
    port     = local.rds_config.port
    database = local.rds_config.database_name
    engine   = local.rds_config.engine
  })
}

data "aws_rds_engine_version" "aurora" {
  count = local.aurora_config.enabled ? 1 : 0

  engine  = local.aurora_config.engine
  version = local.aurora_config.engine_version
}

resource "aws_rds_cluster_parameter_group" "game" {
  count = local.aurora_config.enabled ? 1 : 0

  name_prefix = "${local.name_prefix}-aurora-"
  family      = data.aws_rds_engine_version.aurora[0].parameter_group_family
  description = "Managed by ops-deploy-platform for ${local.name_prefix}"

  parameter {
    apply_method = "immediate"
    name         = "require_secure_transport"
    value        = try(local.aurora_config.tls_enabled, false) ? "ON" : "OFF"
  }

  lifecycle {
    create_before_destroy = true
  }
}

resource "aws_rds_cluster" "game" {
  count = local.aurora_config.enabled ? 1 : 0

  cluster_identifier = "${local.name_prefix}-game"
  engine             = local.aurora_config.engine
  engine_version     = try(local.aurora_config.engine_version, null)
  engine_mode        = "provisioned"
  database_name      = local.aurora_config.database_name
  master_username    = local.aurora_config.master_username
  port               = local.aurora_config.port

  # master_password and manage_master_user_password are mutually exclusive in
  # the provider schema. null means omitted; false would still conflict.
  manage_master_user_password   = local.aurora_credentials_self_managed ? null : true
  master_user_secret_kms_key_id = local.aurora_credentials_self_managed ? null : aws_kms_key.data[0].arn
  master_password               = local.aurora_credentials_self_managed ? lookup(var.data_service_passwords, "aurora", null) : null

  db_subnet_group_name            = aws_db_subnet_group.this.name
  db_cluster_parameter_group_name = aws_rds_cluster_parameter_group.game[0].name
  vpc_security_group_ids          = [aws_security_group.rds[0].id]
  availability_zones              = sort(tolist(local.data_subnet_azs))

  storage_encrypted = true
  kms_key_id        = aws_kms_key.data[0].arn

  serverlessv2_scaling_configuration {
    min_capacity = local.aurora_config.min_acu
    max_capacity = local.aurora_config.max_acu
  }

  backup_retention_period = local.aurora_config.backup_retention_days
  preferred_backup_window = try(local.aurora_config.backup_window, "17:00-18:00")
  preferred_maintenance_window = try(
    local.aurora_config.maintenance_window,
    "sun:18:00-sun:19:00"
  )
  copy_tags_to_snapshot = true

  # Aurora accepts seconds and limits the target window to 72 hours. Keep the
  # switch separate from the configured window so adding defaults to an older
  # environment never enables a billable feature unexpectedly.
  backtrack_window = try(local.aurora_config.backtrack_enabled, false) ? (
    try(local.aurora_config.backtrack_window_hours, 72) * 3600
  ) : 0

  deletion_protection       = try(local.aurora_config.deletion_protection, false)
  skip_final_snapshot       = try(local.aurora_config.skip_final_snapshot, true)
  final_snapshot_identifier = try(local.aurora_config.skip_final_snapshot, true) ? null : "${local.name_prefix}-game-final"

  apply_immediately = try(local.aurora_config.apply_immediately, true)

  lifecycle {
    precondition {
      condition     = !local.aurora_credentials_self_managed || try(length(var.data_service_passwords["aurora"]) >= 8, false)
      error_message = "Aurora self-managed credentials require a runtime master password from the platform credential vault."
    }
    precondition {
      condition = (
        !try(local.aurora_config.backtrack_enabled, false) ||
        (local.aurora_config.engine == "aurora-mysql" && try(local.aurora_config.backtrack_window_hours, 72) >= 1 && try(local.aurora_config.backtrack_window_hours, 72) <= 72)
      )
      error_message = "Aurora backtrack requires aurora-mysql and a target window between 1 and 72 hours."
    }
  }
}

resource "aws_secretsmanager_secret" "aurora" {
  count = local.aurora_config.enabled && local.aurora_credentials_self_managed ? 1 : 0

  name                    = "${local.name_prefix}/aurora/game"
  kms_key_id              = aws_kms_key.data[0].arn
  recovery_window_in_days = 0
}

resource "aws_secretsmanager_secret_version" "aurora" {
  count = local.aurora_config.enabled && local.aurora_credentials_self_managed ? 1 : 0

  secret_id = aws_secretsmanager_secret.aurora[0].id
  secret_string = jsonencode({
    username        = local.aurora_config.master_username
    password        = var.data_service_passwords["aurora"]
    writer_endpoint = aws_rds_cluster.game[0].endpoint
    reader_endpoint = aws_rds_cluster.game[0].reader_endpoint
    port            = local.aurora_config.port
    database        = local.aurora_config.database_name
    engine          = local.aurora_config.engine
  })
}

resource "aws_rds_cluster_instance" "game" {
  count = local.aurora_config.enabled ? local.aurora_config.instance_count : 0

  identifier         = "${local.name_prefix}-game-${count.index + 1}"
  cluster_identifier = aws_rds_cluster.game[0].id
  instance_class     = "db.serverless"
  engine             = aws_rds_cluster.game[0].engine
  engine_version     = aws_rds_cluster.game[0].engine_version

  availability_zone = sort(tolist(local.data_subnet_azs))[count.index % length(local.data_subnet_azs)]
  promotion_tier    = count.index

  publicly_accessible                   = false
  auto_minor_version_upgrade            = try(local.aurora_config.auto_minor_version_upgrade, false)
  performance_insights_enabled          = try(local.aurora_config.performance_insights_enabled, true)
  performance_insights_retention_period = try(local.aurora_config.performance_insights_enabled, true) ? 7 : null
  performance_insights_kms_key_id       = try(local.aurora_config.performance_insights_enabled, true) ? aws_kms_key.data[0].arn : null
}

resource "random_password" "elasticache" {
  count = local.elasticache_config.enabled ? 1 : 0

  length           = 32
  special          = true
  override_special = "!&#$^<>-"
}

resource "aws_elasticache_replication_group" "game" {
  count = local.elasticache_config.enabled && try(local.elasticache_config.mode, "cluster") != "serverless" ? 1 : 0

  replication_group_id = "${local.name_prefix}-game"
  description          = "${local.name_prefix} game active data cache"

  engine               = local.elasticache_config.engine
  engine_version       = try(local.elasticache_config.engine_version, null)
  node_type            = local.elasticache_config.node_type
  port                 = local.elasticache_config.port
  parameter_group_name = local.elasticache_parameter_group_name

  num_node_groups         = local.elasticache_config.num_node_groups
  replicas_per_node_group = local.elasticache_replicas_per_node_group
  automatic_failover_enabled = (
    local.elasticache_replicas_per_node_group > 0
  )
  multi_az_enabled = local.elasticache_replicas_per_node_group > 0

  subnet_group_name  = aws_elasticache_subnet_group.this.name
  security_group_ids = [aws_security_group.elasticache[0].id]

  at_rest_encryption_enabled = true
  transit_encryption_enabled = try(local.elasticache_config.tls_enabled, false)
  auth_token                 = try(local.elasticache_config.tls_enabled, false) ? random_password.elasticache[0].result : null
  auth_token_update_strategy = try(local.elasticache_config.tls_enabled, false) ? "SET" : null
  kms_key_id                 = aws_kms_key.data[0].arn

  snapshot_retention_limit = local.elasticache_config.snapshot_retention_days
  snapshot_window          = try(local.elasticache_config.snapshot_window, "16:00-17:00")
  maintenance_window       = try(local.elasticache_config.maintenance_window, "sun:17:00-sun:18:00")

  apply_immediately          = try(local.elasticache_config.apply_immediately, true)
  auto_minor_version_upgrade = try(local.elasticache_config.auto_minor_version_upgrade, false)
}

resource "aws_elasticache_user" "serverless" {
  count = local.elasticache_config.enabled && try(local.elasticache_config.mode, "cluster") == "serverless" ? 1 : 0

  user_id       = substr("${local.name_prefix}-cache-user", 0, 40)
  user_name     = "app"
  access_string = "on ~* +@all"
  engine        = local.elasticache_config.engine

  authentication_mode {
    type      = "password"
    passwords = [random_password.elasticache[0].result]
  }
}

# Redis user groups must contain a member whose user_name is exactly
# "default". Do not use AWS's built-in nopass default user: create an
# authenticated default user with the same generated password while keeping
# the explicit "app" identity returned to applications.
resource "aws_elasticache_user" "serverless_default" {
  count = local.elasticache_config.enabled && try(local.elasticache_config.mode, "cluster") == "serverless" ? 1 : 0

  user_id       = substr("${local.name_prefix}-cache-default", 0, 40)
  user_name     = "default"
  access_string = "on ~* +@all"
  engine        = local.elasticache_config.engine

  authentication_mode {
    type      = "password"
    passwords = [random_password.elasticache[0].result]
  }
}

resource "aws_elasticache_user_group" "serverless" {
  count = local.elasticache_config.enabled && try(local.elasticache_config.mode, "cluster") == "serverless" ? 1 : 0

  engine        = local.elasticache_config.engine
  user_group_id = substr("${local.name_prefix}-cache-users", 0, 40)
  user_ids = [
    aws_elasticache_user.serverless_default[0].user_id,
    aws_elasticache_user.serverless[0].user_id,
  ]
}

resource "aws_elasticache_serverless_cache" "game" {
  count = local.elasticache_config.enabled && try(local.elasticache_config.mode, "cluster") == "serverless" ? 1 : 0

  name                     = substr("${local.name_prefix}-game", 0, 40)
  engine                   = local.elasticache_config.engine
  major_engine_version     = split(".", local.elasticache_config.engine_version)[0]
  description              = "${local.name_prefix} game active data cache"
  kms_key_id               = aws_kms_key.data[0].arn
  security_group_ids       = [aws_security_group.elasticache[0].id]
  subnet_ids               = local.data_subnet_ids
  user_group_id            = aws_elasticache_user_group.serverless[0].user_group_id
  snapshot_retention_limit = try(local.elasticache_config.snapshot_retention_days, 3)

  cache_usage_limits {
    data_storage {
      maximum = try(local.elasticache_config.max_storage_gb, 100)
      unit    = "GB"
    }
    ecpu_per_second {
      maximum = try(local.elasticache_config.max_ecpu, 5000)
    }
  }
}

resource "aws_secretsmanager_secret" "elasticache" {
  count = local.elasticache_config.enabled ? 1 : 0

  name                    = "${local.name_prefix}/elasticache/game"
  kms_key_id              = aws_kms_key.data[0].arn
  recovery_window_in_days = 0
}

resource "aws_secretsmanager_secret_version" "elasticache" {
  count = local.elasticache_config.enabled ? 1 : 0

  secret_id = aws_secretsmanager_secret.elasticache[0].id
  secret_string = jsonencode({
    endpoint = try(local.elasticache_config.mode, "cluster") == "serverless" ? aws_elasticache_serverless_cache.game[0].endpoint[0].address : aws_elasticache_replication_group.game[0].configuration_endpoint_address
    port     = try(local.elasticache_config.mode, "cluster") == "serverless" ? aws_elasticache_serverless_cache.game[0].endpoint[0].port : local.elasticache_config.port
    username = try(local.elasticache_config.mode, "cluster") == "serverless" ? "app" : "default"
    token    = try(local.elasticache_config.mode, "cluster") == "serverless" || try(local.elasticache_config.tls_enabled, false) ? random_password.elasticache[0].result : null
    tls      = try(local.elasticache_config.mode, "cluster") == "serverless" || try(local.elasticache_config.tls_enabled, false)
  })
}

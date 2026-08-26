resource "aws_db_instance" "postgres" {
  count = local.postgres_config.enabled ? 1 : 0

  identifier     = "${local.name_prefix}-postgres"
  engine         = "postgres"
  engine_version = try(local.postgres_config.engine_version, null)
  instance_class = local.postgres_config.instance_class

  db_name  = local.postgres_config.database_name
  username = local.postgres_config.master_username
  port     = local.postgres_config.port

  manage_master_user_password   = true
  master_user_secret_kms_key_id = aws_kms_key.data[0].arn

  allocated_storage     = local.postgres_config.allocated_storage
  max_allocated_storage = local.postgres_config.max_allocated_storage
  storage_type          = "gp3"
  storage_encrypted     = true
  kms_key_id            = aws_kms_key.data[0].arn

  db_subnet_group_name   = aws_db_subnet_group.this.name
  vpc_security_group_ids = [aws_security_group.rds[0].id]
  publicly_accessible    = false
  multi_az               = try(local.postgres_config.multi_az, false)

  backup_retention_period               = local.postgres_config.backup_retention_days
  backup_window                         = try(local.postgres_config.backup_window, null)
  maintenance_window                    = try(local.postgres_config.maintenance_window, null)
  auto_minor_version_upgrade            = try(local.postgres_config.auto_minor_version_upgrade, false)
  copy_tags_to_snapshot                 = true
  performance_insights_enabled          = try(local.postgres_config.performance_insights_enabled, true)
  performance_insights_kms_key_id       = try(local.postgres_config.performance_insights_enabled, true) ? aws_kms_key.data[0].arn : null
  performance_insights_retention_period = try(local.postgres_config.performance_insights_enabled, true) ? 7 : null
  deletion_protection                   = try(local.postgres_config.deletion_protection, false)
  skip_final_snapshot                   = try(local.postgres_config.skip_final_snapshot, true)
  final_snapshot_identifier             = try(local.postgres_config.skip_final_snapshot, true) ? null : "${local.name_prefix}-postgres-final"
  apply_immediately                     = try(local.postgres_config.apply_immediately, true)
}

resource "aws_docdb_cluster" "this" {
  count = local.documentdb_config.enabled ? 1 : 0

  cluster_identifier = "${local.name_prefix}-documentdb"
  engine             = "docdb"
  engine_version     = try(local.documentdb_config.engine_version, null)
  master_username    = local.documentdb_config.master_username
  port               = try(local.documentdb_config.port, 27017)

  manage_master_user_password = true

  db_subnet_group_name   = aws_docdb_subnet_group.this[0].name
  vpc_security_group_ids = [aws_security_group.rds[0].id]
  availability_zones     = sort(tolist(local.data_subnet_azs))

  storage_encrypted = true
  storage_type      = try(local.documentdb_config.storage_type, "standard")
  kms_key_id        = aws_kms_key.data[0].arn

  backup_retention_period         = try(local.documentdb_config.backup_retention_days, 7)
  enabled_cloudwatch_logs_exports = try(local.documentdb_config.enabled_cloudwatch_logs_exports, [])
  deletion_protection             = try(local.documentdb_config.deletion_protection, false)
  skip_final_snapshot             = try(local.documentdb_config.skip_final_snapshot, true)
  final_snapshot_identifier       = try(local.documentdb_config.skip_final_snapshot, true) ? null : "${local.name_prefix}-documentdb-final"
  apply_immediately               = try(local.documentdb_config.apply_immediately, true)
}

resource "aws_docdb_cluster_instance" "this" {
  count = local.documentdb_config.enabled ? local.documentdb_config.instance_count : 0

  identifier                 = "${local.name_prefix}-documentdb-${count.index + 1}"
  cluster_identifier         = aws_docdb_cluster.this[0].id
  instance_class             = local.documentdb_config.instance_class
  availability_zone          = sort(tolist(local.data_subnet_azs))[count.index % length(local.data_subnet_azs)]
  auto_minor_version_upgrade = try(local.documentdb_config.auto_minor_version_upgrade, false)
  apply_immediately          = try(local.documentdb_config.apply_immediately, true)
}

resource "aws_msk_serverless_cluster" "this" {
  count = local.msk_config.enabled && try(local.msk_config.mode, "serverless") == "serverless" ? 1 : 0

  cluster_name = "${local.name_prefix}-kafka"

  vpc_config {
    subnet_ids         = local.data_subnet_ids
    security_group_ids = [aws_security_group.messaging[0].id]
  }

  client_authentication {
    sasl {
      iam { enabled = true }
    }
  }
}

resource "aws_msk_cluster" "this" {
  count = local.msk_config.enabled && try(local.msk_config.mode, "serverless") == "provisioned" ? 1 : 0

  cluster_name           = "${local.name_prefix}-kafka"
  kafka_version          = local.msk_config.kafka_version
  number_of_broker_nodes = try(local.msk_config.broker_count, 3)

  broker_node_group_info {
    instance_type   = local.msk_config.instance_type
    client_subnets  = local.data_subnet_ids
    security_groups = [aws_security_group.messaging[0].id]
    storage_info {
      ebs_storage_info { volume_size = try(local.msk_config.volume_size, 100) }
    }
  }

  client_authentication {
    sasl {
      iam = true
    }
  }
  encryption_info {
    encryption_in_transit {
      client_broker = "TLS"
      in_cluster    = true
    }
  }
  enhanced_monitoring = try(local.msk_config.enhanced_monitoring, "PER_BROKER")
}

resource "random_password" "amazon_mq" {
  count = local.amazon_mq_config.enabled ? 1 : 0

  length           = 32
  special          = true
  override_special = "!#$%^*-_+"
}

resource "aws_mq_broker" "rabbitmq" {
  count = local.amazon_mq_config.enabled ? 1 : 0

  broker_name = "${local.name_prefix}-rabbitmq"
  # The provider canonicalizes the API enum to "RabbitMQ" in state. Using the
  # all-caps API spelling here creates a perpetual force-replacement diff.
  engine_type                = "RabbitMQ"
  engine_version             = local.amazon_mq_config.engine_version
  host_instance_type         = local.amazon_mq_config.host_instance_type
  deployment_mode            = local.amazon_mq_config.deployment_mode
  publicly_accessible        = false
  auto_minor_version_upgrade = try(local.amazon_mq_config.auto_minor_version_upgrade, false)
  apply_immediately          = try(local.amazon_mq_config.apply_immediately, true)

  subnet_ids      = local.amazon_mq_config.deployment_mode == "CLUSTER_MULTI_AZ" ? local.data_subnet_ids : [local.data_subnet_ids[0]]
  security_groups = [aws_security_group.messaging[0].id]

  user {
    username = local.amazon_mq_config.master_username
    password = random_password.amazon_mq[0].result
  }

  logs {
    general = try(local.amazon_mq_config.general_logs_enabled, true)
  }
}

resource "aws_secretsmanager_secret" "amazon_mq" {
  count = local.amazon_mq_config.enabled ? 1 : 0

  name                    = "${local.name_prefix}/amazon-mq/rabbitmq"
  kms_key_id              = aws_kms_key.data[0].arn
  recovery_window_in_days = 0
}

resource "aws_secretsmanager_secret_version" "amazon_mq" {
  count = local.amazon_mq_config.enabled ? 1 : 0

  secret_id = aws_secretsmanager_secret.amazon_mq[0].id
  secret_string = jsonencode({
    username = local.amazon_mq_config.master_username
    password = random_password.amazon_mq[0].result
    endpoint = try(aws_mq_broker.rabbitmq[0].instances[0].endpoints[0], "")
  })
}

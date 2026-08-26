output "region" {
  value = local.region
}

output "aws_account_id" {
  value = data.aws_caller_identity.current.account_id
}

output "cluster_name" {
  value = aws_eks_cluster.this.name
}

output "cluster_endpoint" {
  value     = aws_eks_cluster.this.endpoint
  sensitive = true
}

output "vpc_id" {
  value = local.vpc_id
}

output "private_subnet_ids" {
  value = local.create_network ? { for az, subnet in aws_subnet.private : az => subnet.id } : {}
}

output "public_subnet_ids" {
  value = local.create_network ? { for az, subnet in aws_subnet.public : az => subnet.id } : {}
}

output "workload_subnet_ids" {
  value = local.workload_subnet_ids_by_az
}

output "data_subnet_ids" {
  value = local.data_subnet_ids
}

output "nat_gateway_mode" {
  value = local.nat_gateway_mode
}

output "nat_gateway_ids" {
  value = { for az, gateway in aws_nat_gateway.this : az => gateway.id }
}

output "nat_gateway_public_ips" {
  value = { for az, address in aws_eip.nat : az => address.public_ip }
}

output "rds_endpoint" {
  value = local.rds_config.enabled ? aws_db_instance.admin[0].address : null
}

output "rds_master_secret_arn" {
  value = !local.rds_config.enabled ? null : (
    local.rds_credentials_self_managed ? try(aws_secretsmanager_secret.rds[0].arn, null) : try(aws_db_instance.admin[0].master_user_secret[0].secret_arn, null)
  )
}

output "postgres_endpoint" {
  value = local.postgres_config.enabled ? aws_db_instance.postgres[0].address : null
}

output "postgres_master_secret_arn" {
  value = local.postgres_config.enabled ? try(aws_db_instance.postgres[0].master_user_secret[0].secret_arn, null) : null
}

output "documentdb_endpoint" {
  value = local.documentdb_config.enabled ? aws_docdb_cluster.this[0].endpoint : null
}

output "documentdb_reader_endpoint" {
  value = local.documentdb_config.enabled ? aws_docdb_cluster.this[0].reader_endpoint : null
}

output "documentdb_master_secret_arn" {
  value = local.documentdb_config.enabled ? try(aws_docdb_cluster.this[0].master_user_secret[0].secret_arn, null) : null
}

output "aurora_writer_endpoint" {
  value = local.aurora_config.enabled ? aws_rds_cluster.game[0].endpoint : null
}

output "aurora_reader_endpoint" {
  value = local.aurora_config.enabled ? aws_rds_cluster.game[0].reader_endpoint : null
}

output "aurora_master_secret_arn" {
  value = !local.aurora_config.enabled ? null : (
    local.aurora_credentials_self_managed ? try(aws_secretsmanager_secret.aurora[0].arn, null) : try(aws_rds_cluster.game[0].master_user_secret[0].secret_arn, null)
  )
}

output "elasticache_configuration_endpoint" {
  value = !local.elasticache_config.enabled ? null : (
    try(local.elasticache_config.mode, "cluster") == "serverless" ? aws_elasticache_serverless_cache.game[0].endpoint[0].address : aws_elasticache_replication_group.game[0].configuration_endpoint_address
  )
}

output "elasticache_reader_endpoint" {
  value = !local.elasticache_config.enabled || try(local.elasticache_config.mode, "cluster") != "serverless" ? null : aws_elasticache_serverless_cache.game[0].reader_endpoint[0].address
}

output "elasticache_secret_arn" {
  value = local.elasticache_config.enabled ? aws_secretsmanager_secret.elasticache[0].arn : null
}

output "msk_bootstrap_brokers" {
  value = !local.msk_config.enabled ? null : (
    try(local.msk_config.mode, "serverless") == "serverless" ? aws_msk_serverless_cluster.this[0].bootstrap_brokers_sasl_iam : aws_msk_cluster.this[0].bootstrap_brokers_sasl_iam
  )
}

output "amazon_mq_endpoint" {
  value = local.amazon_mq_config.enabled ? try(aws_mq_broker.rabbitmq[0].instances[0].endpoints[0], null) : null
}

output "amazon_mq_console_url" {
  value = local.amazon_mq_config.enabled ? try(aws_mq_broker.rabbitmq[0].instances[0].console_url, null) : null
}

output "amazon_mq_secret_arn" {
  value = local.amazon_mq_config.enabled ? aws_secretsmanager_secret.amazon_mq[0].arn : null
}

output "ecr_repository_urls" {
  value = local.ecr_config.enabled ? {
    for name in toset(local.ecr_config.repositories) :
    name => "${data.aws_caller_identity.current.account_id}.dkr.ecr.${local.region}.${data.aws_partition.current.dns_suffix}/${local.project}/${name}"
  } : {}
}

output "platform_backup_bucket" {
  value = local.platform_backup_enabled ? aws_s3_bucket.platform_backups[0].id : null
}

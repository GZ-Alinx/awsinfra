resource "aws_security_group" "rds" {
  count = local.rds_config.enabled || local.aurora_config.enabled || local.postgres_config.enabled || local.documentdb_config.enabled ? 1 : 0

  name        = "${local.name_prefix}-database"
  description = "Database access from EKS private subnets"
  vpc_id      = local.vpc_id

  tags = {
    Name = "${local.name_prefix}-database"
  }
}

locals {
  database_ingress_ports = toset(concat(
    local.rds_config.enabled ? [local.rds_config.port] : [],
    local.aurora_config.enabled ? [local.aurora_config.port] : [],
    local.postgres_config.enabled ? [local.postgres_config.port] : [],
    local.documentdb_config.enabled ? [local.documentdb_config.port] : []
  ))
  database_ingress_rules = {
    for pair in setproduct(toset(keys(local.workload_subnet_cidrs)), local.database_ingress_ports) :
    "${pair[0]}-${pair[1]}" => {
      availability_zone = pair[0]
      cidr              = local.workload_subnet_cidrs[pair[0]]
      port              = pair[1]
    }
  }
}

resource "aws_vpc_security_group_ingress_rule" "rds" {
  for_each = local.database_ingress_rules

  security_group_id = aws_security_group.rds[0].id
  cidr_ipv4         = each.value.cidr
  from_port         = each.value.port
  to_port           = each.value.port
  ip_protocol       = "tcp"
  description       = "EKS workload subnet ${each.value.availability_zone}"
}

resource "aws_security_group" "elasticache" {
  count = local.elasticache_config.enabled ? 1 : 0

  name        = "${local.name_prefix}-elasticache"
  description = "ElastiCache access from EKS private subnets"
  vpc_id      = local.vpc_id

  tags = {
    Name = "${local.name_prefix}-elasticache"
  }
}

resource "aws_vpc_security_group_ingress_rule" "elasticache" {
  for_each = local.elasticache_config.enabled ? local.workload_subnet_cidrs : {}

  security_group_id = aws_security_group.elasticache[0].id
  cidr_ipv4         = each.value
  from_port         = local.elasticache_config.port
  to_port           = local.elasticache_config.port
  ip_protocol       = "tcp"
  description       = "EKS workload subnet ${each.key}"
}

resource "aws_security_group" "messaging" {
  count = local.msk_config.enabled || local.amazon_mq_config.enabled ? 1 : 0

  name        = "${local.name_prefix}-messaging"
  description = "Managed messaging access from EKS workload subnets"
  vpc_id      = local.vpc_id

  tags = { Name = "${local.name_prefix}-messaging" }
}

locals {
  messaging_ports = toset(concat(
    local.msk_config.enabled ? [9098] : [],
    local.amazon_mq_config.enabled ? [443, 5671] : []
  ))
  messaging_ingress_rules = {
    for pair in setproduct(toset(keys(local.workload_subnet_cidrs)), local.messaging_ports) :
    "${pair[0]}-${pair[1]}" => { cidr = local.workload_subnet_cidrs[pair[0]], port = pair[1], az = pair[0] }
  }
}

resource "aws_vpc_security_group_ingress_rule" "messaging" {
  for_each = local.messaging_ingress_rules

  security_group_id = aws_security_group.messaging[0].id
  cidr_ipv4         = each.value.cidr
  from_port         = each.value.port
  to_port           = each.value.port
  ip_protocol       = "tcp"
  description       = "EKS workload subnet ${each.value.az}"
}

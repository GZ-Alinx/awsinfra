locals {
  config       = yamldecode(file(var.config_file))
  project      = local.config.project
  environment  = local.config.environment
  region       = local.config.region
  name_prefix  = "${local.project}-${local.environment}"
  cluster_name = "${local.name_prefix}-eks"

  network_mode                     = try(local.config.network.mode, "create")
  create_network                   = local.network_mode == "create"
  availability_zones               = tolist(local.config.network.availability_zones)
  public_subnets                   = tomap(local.config.network.public_subnets)
  private_subnets                  = tomap(local.config.network.private_subnets)
  existing_vpc_id                  = try(local.config.network.existing_vpc_id, "")
  existing_vpc_cidr                = try(local.config.network.existing_vpc_cidr, "")
  existing_workload_subnet_ids     = toset(try(local.config.network.existing_workload_subnet_ids, []))
  existing_data_subnet_ids         = toset(try(local.config.network.existing_data_subnet_ids, []))
  vpc_id                           = local.create_network ? aws_vpc.this[0].id : data.aws_vpc.existing[0].id
  vpc_cidr                         = local.create_network ? local.config.network.vpc_cidr : local.existing_vpc_cidr
  workload_subnet_type             = try(local.config.network.workload_subnet_type, "public")
  data_subnet_type                 = try(local.config.network.data_subnet_type, local.workload_subnet_type)
  configured_workload_subnet_zones = toset(try(local.config.network.workload_subnet_zones, local.availability_zones))
  workload_subnet_zones            = local.create_network ? local.configured_workload_subnet_zones : toset(local.availability_zones)
  data_subnet_zones                = toset(try(local.config.network.data_subnet_zones, local.availability_zones))
  created_workload_subnet_cidrs = {
    for az, cidr in(local.workload_subnet_type == "private" ? local.private_subnets : local.public_subnets) :
    az => cidr if contains(local.workload_subnet_zones, az)
  }
  existing_workload_subnet_cidrs = {
    for subnet in values(data.aws_subnet.existing_workload) : subnet.availability_zone => subnet.cidr_block
  }
  workload_subnet_cidrs = local.create_network ? local.created_workload_subnet_cidrs : local.existing_workload_subnet_cidrs
  created_workload_subnet_ids_by_az = local.workload_subnet_type == "private" ? {
    for az in local.workload_subnet_zones : az => aws_subnet.private[az].id
    } : {
    for az in local.workload_subnet_zones : az => aws_subnet.public[az].id
  }
  existing_workload_subnet_ids_by_az = {
    for subnet in values(data.aws_subnet.existing_workload) : subnet.availability_zone => subnet.id
  }
  workload_subnet_ids_by_az = local.create_network ? local.created_workload_subnet_ids_by_az : local.existing_workload_subnet_ids_by_az
  workload_subnet_ids       = [for az in sort(keys(local.workload_subnet_ids_by_az)) : local.workload_subnet_ids_by_az[az]]
  created_data_subnet_ids = local.data_subnet_type == "private" ? [
    for az in sort(tolist(local.data_subnet_zones)) : aws_subnet.private[az].id
    ] : [
    for az in sort(tolist(local.data_subnet_zones)) : aws_subnet.public[az].id
  ]
  data_subnet_ids = local.create_network ? local.created_data_subnet_ids : sort(tolist(local.existing_data_subnet_ids))
  data_subnet_azs = local.create_network ? local.data_subnet_zones : toset([
    for subnet in values(data.aws_subnet.existing_data) : subnet.availability_zone
  ])
  private_network_required = local.create_network && (local.workload_subnet_type == "private" || local.data_subnet_type == "private")
  nat_gateway_mode         = try(local.config.network.nat_gateway_mode, "when-private")
  nat_gateway_required = local.create_network && (
    local.nat_gateway_mode == "always" ||
    (local.nat_gateway_mode == "when-private" && local.private_network_required)
  )
  single_nat_gateway = try(local.config.network.single_nat_gateway, true)
  nat_gateway_azs = !local.nat_gateway_required ? [] : (
    local.single_nat_gateway ? [local.availability_zones[0]] : local.availability_zones
  )

  common_tags = merge(
    {
      Project     = local.project
      Environment = local.environment
      ManagedBy   = "Terraform"
    },
    try(local.config.tags, {})
  )

  # Node groups are intentionally allowed to have different optional fields
  # (for example taints on a platform group but not on an application group).
  # tomap() tries to coerce every object value to one identical Terraform type
  # and therefore fails as soon as operators add a heterogeneous second group.
  # for_each accepts the decoded object directly, so keep its native shape.
  node_groups = try(local.config.eks.node_groups, {})

  rds_config         = try(local.config.data_services.rds, { enabled = false })
  aurora_config      = try(local.config.data_services.aurora, { enabled = false })
  postgres_config    = try(local.config.data_services.postgres, { enabled = false })
  documentdb_config  = try(local.config.data_services.documentdb, { enabled = false })
  elasticache_config = try(local.config.data_services.elasticache, { enabled = false })
  msk_config         = try(local.config.data_services.msk, { enabled = false })
  amazon_mq_config   = try(local.config.data_services.amazon_mq, { enabled = false })
  ecr_config         = try(local.config.ecr, { enabled = false, repositories = [] })

  rds_credential_management       = try(local.rds_config.credential_management, "aws-managed")
  aurora_credential_management    = try(local.aurora_config.credential_management, "aws-managed")
  rds_credentials_self_managed    = local.rds_credential_management == "self-managed"
  aurora_credentials_self_managed = local.aurora_credential_management == "self-managed"

  elasticache_engine         = lower(try(local.elasticache_config.engine, "valkey"))
  elasticache_engine_version = tostring(try(local.elasticache_config.engine_version, local.elasticache_engine == "redis" ? "7.1" : "8.2"))
  elasticache_version_parts  = split(".", local.elasticache_engine_version)
  elasticache_major_version  = local.elasticache_version_parts[0]
  elasticache_minor_version  = length(local.elasticache_version_parts) > 1 ? local.elasticache_version_parts[1] : "0"
  elasticache_default_parameter_group_name = local.elasticache_engine == "valkey" ? "default.valkey${local.elasticache_major_version}.cluster.on" : (
    local.elasticache_major_version == "7" ? "default.redis7.cluster.on" : (
      local.elasticache_major_version == "6" ? "default.redis6.x.cluster.on" : "default.redis${local.elasticache_major_version}.${local.elasticache_minor_version}.cluster.on"
    )
  )
  elasticache_configured_parameter_group_name = trimspace(try(local.elasticache_config.parameter_group_name, ""))
  elasticache_parameter_group_name = (
    local.elasticache_configured_parameter_group_name != "" && !startswith(local.elasticache_configured_parameter_group_name, "default.")
    ? local.elasticache_configured_parameter_group_name
    : local.elasticache_default_parameter_group_name
  )

  # Capacity is configured as an exact desired size. nodes_per_shard includes
  # the primary node; the AWS provider field below accepts only the additional
  # read replicas. Keeping this conversion in one place prevents UI wording or
  # stale legacy values from multiplying the cluster on every update.
  elasticache_nodes_per_shard = try(
    tonumber(local.elasticache_config.nodes_per_shard),
    tonumber(local.elasticache_config.replicas_per_node_group) + 1
  )
  elasticache_replicas_per_node_group = local.elasticache_nodes_per_shard - 1
  elasticache_total_nodes = (
    try(local.elasticache_config.mode, "cluster") == "cluster"
    ? tonumber(local.elasticache_config.num_node_groups) * local.elasticache_nodes_per_shard
    : 0
  )

  # Terraform has no native CIDR containment helper. Convert IPv4 network
  # addresses to integers so the checks below work on every supported CLI.
  vpc_network_octets     = [for octet in split(".", cidrhost(local.vpc_cidr, 0)) : parseint(octet, 10)]
  service_network_octets = [for octet in split(".", cidrhost(local.config.network.service_ipv4_cidr, 0)) : parseint(octet, 10)]
  vpc_prefix_length      = parseint(split("/", local.vpc_cidr)[1], 10)
  service_prefix_length  = parseint(split("/", local.config.network.service_ipv4_cidr)[1], 10)
  vpc_network_number     = sum([for index, octet in local.vpc_network_octets : octet * pow(256, 3 - index)])
  service_network_number = sum([for index, octet in local.service_network_octets : octet * pow(256, 3 - index)])
  vpc_last_number        = local.vpc_network_number + pow(2, 32 - local.vpc_prefix_length) - 1
  service_last_number    = local.service_network_number + pow(2, 32 - local.service_prefix_length) - 1
  subnet_ranges = {
    for cidr in concat(values(local.public_subnets), values(local.private_subnets)) : cidr => {
      network_number = sum([
        for index, octet in [for part in split(".", cidrhost(cidr, 0)) : parseint(part, 10)] :
        octet * pow(256, 3 - index)
      ])
      last_number = sum([
        for index, octet in [for part in split(".", cidrhost(cidr, 0)) : parseint(part, 10)] :
        octet * pow(256, 3 - index)
      ]) + pow(2, 32 - parseint(split("/", cidr)[1], 10)) - 1
    }
  }
}

check "network_subnet_azs" {
  assert {
    condition = !local.create_network || (
      length(setsubtract(toset(keys(local.public_subnets)), toset(local.availability_zones))) == 0 &&
      length(setsubtract(toset(local.availability_zones), toset(keys(local.public_subnets)))) == 0 &&
      length(setsubtract(toset(keys(local.private_subnets)), toset(local.availability_zones))) == 0 &&
      length(setsubtract(toset(local.availability_zones), toset(keys(local.private_subnets)))) == 0
    )
    error_message = "All subnet maps must contain exactly the availability_zones configured in network.availability_zones."
  }
}

check "node_group_azs" {
  assert {
    condition = alltrue([
      for group in values(local.node_groups) :
      length(setsubtract(toset(group.availability_zones), local.workload_subnet_zones)) == 0
    ])
    error_message = "Every EKS node group availability_zones entry must be selected in network.workload_subnet_zones."
  }
}

check "network_cidr_separation" {
  assert {
    condition = (
      (local.vpc_last_number < local.service_network_number || local.service_last_number < local.vpc_network_number) &&
      (!local.create_network || alltrue([
        for subnet in values(local.subnet_ranges) :
        subnet.network_number >= local.vpc_network_number && subnet.last_number <= local.vpc_last_number
      ]))
    )
    error_message = "Kubernetes service_ipv4_cidr must not overlap the VPC, and every subnet CIDR must be inside vpc_cidr."
  }
}

check "network_placement" {
  assert {
    condition = !local.create_network || (
      contains(["public", "private"], local.workload_subnet_type) &&
      contains(["public", "private"], local.data_subnet_type) &&
      length(local.workload_subnet_zones) >= 2 &&
      length(local.data_subnet_zones) >= 2 &&
      length(setsubtract(local.workload_subnet_zones, toset(local.availability_zones))) == 0 &&
      length(setsubtract(local.data_subnet_zones, toset(local.availability_zones))) == 0
    )
    error_message = "Workload and data subnet types must be public/private and must select two or three configured availability zones."
  }
}

check "existing_vpc_selection" {
  assert {
    condition = local.create_network || (
      data.aws_vpc.existing[0].enable_dns_support &&
      data.aws_vpc.existing[0].enable_dns_hostnames &&
      data.aws_vpc.existing[0].cidr_block == local.existing_vpc_cidr &&
      length(local.existing_workload_subnet_ids) >= 2 &&
      length(local.existing_workload_subnet_ids) <= 3 &&
      length(local.existing_data_subnet_ids) >= 2 &&
      length(local.existing_data_subnet_ids) <= 3 &&
      length(local.workload_subnet_ids_by_az) == length(local.existing_workload_subnet_ids) &&
      length(local.data_subnet_azs) == length(local.existing_data_subnet_ids) &&
      alltrue([for subnet in values(data.aws_subnet.existing_workload) : subnet.vpc_id == local.existing_vpc_id]) &&
      alltrue([for subnet in values(data.aws_subnet.existing_data) : subnet.vpc_id == local.existing_vpc_id])
    )
    error_message = "Existing VPC mode requires DNS support/hostnames and two or three explicitly selected subnets in distinct availability zones for both workloads and data."
  }
}

check "data_service_capacity" {
  assert {
    condition = (
      !local.aurora_config.enabled || local.aurora_config.instance_count >= 1
      ) && (
      !local.elasticache_config.enabled || try(local.elasticache_config.mode, "cluster") != "cluster" || (
        local.elasticache_config.num_node_groups >= 1 &&
        local.elasticache_config.num_node_groups <= 500 &&
        local.elasticache_nodes_per_shard >= 1 &&
        local.elasticache_nodes_per_shard <= 6 &&
        local.elasticache_total_nodes <= 500
      )
      ) && (
      !local.documentdb_config.enabled || (local.documentdb_config.instance_count >= 1 && local.documentdb_config.instance_count <= 16)
    )
    error_message = "Enabled Aurora needs capacity; node-based ElastiCache requires 1-500 shards, 1-6 total nodes per shard and at most 500 total nodes; enabled DocumentDB must have 1 to 16 instances."
  }
}

check "elasticache_engine_compatibility" {
  assert {
    condition = !local.elasticache_config.enabled || (
      contains(["redis", "valkey"], local.elasticache_engine) &&
      contains(["cluster", "serverless"], try(local.elasticache_config.mode, "cluster")) &&
      (
        local.elasticache_engine == "redis"
        ? contains(["4", "5", "6", "7"], local.elasticache_major_version)
        : contains(["7", "8", "9"], local.elasticache_major_version)
      )
    )
    error_message = "ElastiCache engine/version is incompatible: Redis OSS supports configured major versions 4-7; Valkey supports configured major versions 7-9."
  }
}

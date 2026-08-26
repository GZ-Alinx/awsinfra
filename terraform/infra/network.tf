data "aws_vpc" "existing" {
  count = local.create_network ? 0 : 1
  id    = local.existing_vpc_id
}

data "aws_subnet" "existing_workload" {
  for_each = local.create_network ? toset([]) : local.existing_workload_subnet_ids
  id       = each.value
}

data "aws_subnet" "existing_data" {
  for_each = local.create_network ? toset([]) : local.existing_data_subnet_ids
  id       = each.value
}

resource "aws_vpc" "this" {
  count = local.create_network ? 1 : 0

  cidr_block           = local.config.network.vpc_cidr
  enable_dns_support   = true
  enable_dns_hostnames = true

  tags = {
    Name = "${local.name_prefix}-vpc"
  }
}

resource "aws_internet_gateway" "this" {
  count  = local.create_network ? 1 : 0
  vpc_id = aws_vpc.this[0].id

  tags = {
    Name = "${local.name_prefix}-igw"
  }
}

resource "aws_subnet" "public" {
  for_each = local.create_network ? local.public_subnets : {}

  vpc_id                  = aws_vpc.this[0].id
  availability_zone       = each.key
  cidr_block              = each.value
  map_public_ip_on_launch = true

  tags = {
    Name                     = "${local.name_prefix}-public-${each.key}"
    "kubernetes.io/role/elb" = "1"
  }
}

resource "aws_subnet" "private" {
  for_each = local.create_network ? local.private_subnets : {}

  vpc_id            = aws_vpc.this[0].id
  availability_zone = each.key
  cidr_block        = each.value

  tags = {
    Name                                          = "${local.name_prefix}-private-${each.key}"
    "kubernetes.io/role/internal-elb"             = "1"
    "kubernetes.io/cluster/${local.cluster_name}" = "shared"
  }
}

resource "aws_route_table" "public" {
  count  = local.create_network ? 1 : 0
  vpc_id = aws_vpc.this[0].id

  route {
    cidr_block = "0.0.0.0/0"
    gateway_id = aws_internet_gateway.this[0].id
  }

  tags = {
    Name = "${local.name_prefix}-public"
  }
}

resource "aws_route_table_association" "public" {
  for_each = aws_subnet.public

  subnet_id      = each.value.id
  route_table_id = aws_route_table.public[0].id
}

resource "aws_eip" "nat" {
  for_each = toset(local.nat_gateway_azs)

  domain = "vpc"

  tags = {
    Name = "${local.name_prefix}-nat-${each.key}"
  }

  depends_on = [aws_internet_gateway.this]
}

resource "aws_nat_gateway" "this" {
  for_each = toset(local.nat_gateway_azs)

  allocation_id = aws_eip.nat[each.key].id
  subnet_id     = aws_subnet.public[each.key].id

  tags = {
    Name = "${local.name_prefix}-nat-${each.key}"
  }

  depends_on = [aws_internet_gateway.this]
}

resource "aws_route_table" "private" {
  for_each = local.create_network ? toset(local.availability_zones) : toset([])

  vpc_id = aws_vpc.this[0].id

  dynamic "route" {
    for_each = local.nat_gateway_required ? [1] : []
    content {
      cidr_block = "0.0.0.0/0"
      nat_gateway_id = aws_nat_gateway.this[
        local.single_nat_gateway ? local.availability_zones[0] : each.key
      ].id
    }
  }

  tags = {
    Name = "${local.name_prefix}-private-${each.key}"
  }
}

resource "aws_route_table_association" "private" {
  for_each = aws_subnet.private

  subnet_id      = each.value.id
  route_table_id = aws_route_table.private[each.key].id
}

resource "aws_db_subnet_group" "this" {
  name       = "${local.name_prefix}-database"
  subnet_ids = local.data_subnet_ids

  tags = {
    Name = "${local.name_prefix}-database"
  }
}

resource "aws_elasticache_subnet_group" "this" {
  name       = "${local.name_prefix}-elasticache"
  subnet_ids = local.data_subnet_ids
}

resource "aws_docdb_subnet_group" "this" {
  count = local.documentdb_config.enabled ? 1 : 0

  name       = "${local.name_prefix}-documentdb"
  subnet_ids = local.data_subnet_ids

  tags = {
    Name = "${local.name_prefix}-documentdb"
  }
}

moved {
  from = aws_vpc.this
  to   = aws_vpc.this[0]
}

moved {
  from = aws_internet_gateway.this
  to   = aws_internet_gateway.this[0]
}

moved {
  from = aws_route_table.public
  to   = aws_route_table.public[0]
}

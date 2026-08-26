locals {
  higress_enabled = contains(keys(local.enabled_catalog_components), "higress")

  # A managed EKS environment uses AWS Load Balancer Controller and can attach
  # a platform-owned frontend security group to the Higress NLB. Imported EKS
  # environments retain their legacy Service-controller behavior and are not
  # mutated with account-level networking resources.
  higress_nlb_security_group_enabled = local.higress_enabled && local.manage_cluster_addons
  higress_nlb_security_group_mode    = try(local.enabled_catalog_components.higress.nlb.security_group_mode, "managed")
  higress_nlb_scheme                 = try(local.enabled_catalog_components.higress.nlb.scheme, "internet-facing")
  higress_nlb_allowed_ports = toset([
    for port in try(local.enabled_catalog_components.higress.nlb.allowed_ports, [80, 443]) : tonumber(port)
  ])
  higress_nlb_custom_security_group_ids = toset([
    for security_group_id in try(local.enabled_catalog_components.higress.nlb.security_group_ids, []) : lower(trimspace(security_group_id))
    if trimspace(security_group_id) != ""
  ])
  higress_nlb_attached_custom_security_group_ids = local.higress_nlb_security_group_mode == "managed" ? toset([]) : local.higress_nlb_custom_security_group_ids
  # Keep the platform guard SG attached in every mode. In custom-only mode it
  # has no ingress rules, so access is controlled exclusively by the selected
  # existing groups while the NLB still retains SG support for its full life.
  higress_nlb_managed_ingress_enabled = local.higress_nlb_security_group_enabled && local.higress_nlb_security_group_mode != "custom"
  higress_nlb_frontend_security_group_ids = local.higress_nlb_security_group_enabled ? distinct(concat(
    [aws_security_group.higress_nlb[0].id],
    sort(tolist(local.higress_nlb_attached_custom_security_group_ids)),
  )) : []
  higress_nlb_allowed_cidrs = toset([
    for cidr in try(local.enabled_catalog_components.higress.nlb.allowed_cidrs, ["0.0.0.0/0"]) : trimspace(cidr)
    if trimspace(cidr) != ""
  ])
  higress_nlb_ipv4_cidrs = toset([
    for cidr in local.higress_nlb_allowed_cidrs : cidr if !strcontains(cidr, ":")
  ])
  higress_nlb_ipv6_cidrs = toset([
    for cidr in local.higress_nlb_allowed_cidrs : cidr if strcontains(cidr, ":")
  ])
  higress_nlb_ipv4_ingress = {
    for pair in setproduct(local.higress_nlb_ipv4_cidrs, local.higress_nlb_allowed_ports) :
    "${pair[1]}-${sha1(pair[0])}" => { cidr = pair[0], port = pair[1] }
  }
  higress_nlb_ipv6_ingress = {
    for pair in setproduct(local.higress_nlb_ipv6_cidrs, local.higress_nlb_allowed_ports) :
    "${pair[1]}-${sha1(pair[0])}" => { cidr = pair[0], port = pair[1] }
  }
}

resource "aws_security_group" "higress_nlb" {
  count = local.higress_nlb_security_group_enabled ? 1 : 0

  name_prefix = "${local.name_prefix}-higress-nlb-"
  description = "Managed frontend access for the ${local.name_prefix} Higress NLB"
  vpc_id      = local.vpc_id

  tags = merge(local.common_tags, {
    Name                      = "${local.name_prefix}-higress-nlb"
    "ops-deploy.io/component" = "higress"
    "ops-deploy.io/resource"  = "nlb-frontend-security-group"
  })

  lifecycle {
    create_before_destroy = true
  }
}

resource "aws_vpc_security_group_ingress_rule" "higress_nlb_ipv4" {
  for_each = local.higress_nlb_managed_ingress_enabled ? local.higress_nlb_ipv4_ingress : {}

  security_group_id = aws_security_group.higress_nlb[0].id
  cidr_ipv4         = each.value.cidr
  from_port         = each.value.port
  to_port           = each.value.port
  ip_protocol       = "tcp"
  description       = "Higress public listener ${each.value.port}"
}

resource "aws_vpc_security_group_ingress_rule" "higress_nlb_ipv6" {
  for_each = local.higress_nlb_managed_ingress_enabled ? local.higress_nlb_ipv6_ingress : {}

  security_group_id = aws_security_group.higress_nlb[0].id
  cidr_ipv6         = each.value.cidr
  from_port         = each.value.port
  to_port           = each.value.port
  ip_protocol       = "tcp"
  description       = "Higress public listener ${each.value.port}"
}

resource "aws_vpc_security_group_egress_rule" "higress_nlb" {
  count = local.higress_nlb_security_group_enabled ? 1 : 0

  security_group_id = aws_security_group.higress_nlb[0].id
  cidr_ipv4         = "0.0.0.0/0"
  ip_protocol       = "-1"
  description       = "Higress targets and health checks"
}

data "aws_security_group" "higress_nlb_custom" {
  for_each = local.higress_nlb_security_group_enabled ? local.higress_nlb_attached_custom_security_group_ids : toset([])

  id = each.value
}

check "higress_nlb_custom_security_groups_use_cluster_vpc" {
  assert {
    condition = alltrue([
      for security_group in data.aws_security_group.higress_nlb_custom : security_group.vpc_id == local.vpc_id
    ])
    error_message = "Every custom Higress NLB security group must belong to the EKS cluster VPC."
  }
}

check "higress_nlb_custom_security_groups_are_safe_frontends" {
  assert {
    condition = alltrue([
      for security_group in data.aws_security_group.higress_nlb_custom :
      security_group.name != "default" &&
      !startswith(security_group.name, "eks-cluster-sg-") &&
      try(security_group.tags["ops-deploy.io/resource"], "") != "nlb-frontend-security-group"
    ])
    error_message = "Custom Higress NLB security groups cannot be the VPC default group, an EKS cluster group, or another platform-managed NLB guard group."
  }
}

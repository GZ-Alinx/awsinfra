data "aws_caller_identity" "current" {}
data "aws_partition" "current" {}

resource "aws_kms_key" "eks" {
  description             = "KMS key for ${local.cluster_name} Kubernetes secrets"
  deletion_window_in_days = 7
  enable_key_rotation     = true
}

resource "aws_kms_alias" "eks" {
  name          = "alias/${local.cluster_name}"
  target_key_id = aws_kms_key.eks.key_id
}

resource "aws_cloudwatch_log_group" "eks" {
  name              = "/aws/eks/${local.cluster_name}/cluster"
  retention_in_days = try(local.config.eks.log_retention_days, 30)
}

resource "aws_iam_role" "eks_cluster" {
  name = "${local.cluster_name}-cluster-role"

  assume_role_policy = jsonencode({
    Version = "2012-10-17"
    Statement = [{
      Effect = "Allow"
      Principal = {
        Service = "eks.amazonaws.com"
      }
      Action = "sts:AssumeRole"
    }]
  })
}

resource "aws_iam_role_policy_attachment" "eks_cluster" {
  role       = aws_iam_role.eks_cluster.name
  policy_arn = "arn:aws:iam::aws:policy/AmazonEKSClusterPolicy"
}

resource "aws_eks_cluster" "this" {
  name     = local.cluster_name
  role_arn = aws_iam_role.eks_cluster.arn
  version  = local.config.eks.kubernetes_version

  access_config {
    authentication_mode                         = "API_AND_CONFIG_MAP"
    bootstrap_cluster_creator_admin_permissions = true
  }

  enabled_cluster_log_types = try(local.config.eks.enabled_control_plane_logs, [])

  encryption_config {
    provider {
      key_arn = aws_kms_key.eks.arn
    }
    resources = ["secrets"]
  }

  kubernetes_network_config {
    service_ipv4_cidr = local.config.network.service_ipv4_cidr
  }

  vpc_config {
    endpoint_private_access = try(local.config.eks.endpoint_private_access, true)
    endpoint_public_access  = try(local.config.eks.endpoint_public_access, true)
    public_access_cidrs = var.eks_public_access_cidrs_override != null ? (
      var.eks_public_access_cidrs_override
    ) : try(local.config.eks.public_access_cidrs, ["0.0.0.0/0"])
    subnet_ids = local.workload_subnet_ids
  }

  depends_on = [
    aws_iam_role_policy_attachment.eks_cluster,
    aws_cloudwatch_log_group.eks
  ]
}

resource "aws_iam_role" "eks_node" {
  name = "${local.cluster_name}-node-role"

  assume_role_policy = jsonencode({
    Version = "2012-10-17"
    Statement = [{
      Effect = "Allow"
      Principal = {
        Service = "ec2.amazonaws.com"
      }
      Action = "sts:AssumeRole"
    }]
  })
}

resource "aws_iam_role_policy_attachment" "eks_node_worker" {
  role       = aws_iam_role.eks_node.name
  policy_arn = "arn:aws:iam::aws:policy/AmazonEKSWorkerNodePolicy"
}

resource "aws_iam_role_policy_attachment" "eks_node_ecr" {
  role       = aws_iam_role.eks_node.name
  policy_arn = "arn:aws:iam::aws:policy/AmazonEC2ContainerRegistryPullOnly"
}

resource "aws_iam_role_policy_attachment" "eks_node_cni" {
  role       = aws_iam_role.eks_node.name
  policy_arn = "arn:aws:iam::aws:policy/AmazonEKS_CNI_Policy"
}

resource "aws_launch_template" "node" {
  for_each = local.node_groups

  name_prefix            = "${local.cluster_name}-${each.key}-"
  update_default_version = true

  block_device_mappings {
    device_name = "/dev/xvda"

    ebs {
      encrypted             = true
      volume_size           = each.value.disk_size
      volume_type           = "gp3"
      delete_on_termination = true
    }
  }

  metadata_options {
    http_endpoint               = "enabled"
    http_tokens                 = "required"
    http_put_response_hop_limit = 1
    instance_metadata_tags      = "disabled"
  }

  monitoring {
    enabled = true
  }

  tag_specifications {
    resource_type = "instance"
    tags = merge(local.common_tags, {
      Name = "${local.cluster_name}-${each.key}"
    })
  }

  tag_specifications {
    resource_type = "volume"
    tags = merge(local.common_tags, {
      Name = "${local.cluster_name}-${each.key}"
    })
  }

  lifecycle {
    create_before_destroy = true
  }
}

resource "aws_eks_node_group" "this" {
  for_each = local.node_groups

  cluster_name    = aws_eks_cluster.this.name
  node_group_name = each.key
  node_role_arn   = aws_iam_role.eks_node.arn
  subnet_ids = [for az in each.value.availability_zones : (
    local.create_network && try(each.value.subnet_type, local.workload_subnet_type) == "private"
    ? aws_subnet.private[az].id
    : local.workload_subnet_ids_by_az[az]
  )]
  instance_types = each.value.instance_types
  capacity_type  = each.value.capacity_type
  ami_type       = try(each.value.ami_type, "AL2023_x86_64_STANDARD")
  labels         = try(each.value.labels, {})
  # Cluster Autoscaler must know labels and taints before a scale-from-zero
  # node exists. Without node-template tags, a tainted dedicated pool can
  # remain at zero forever because pending Pods are considered unschedulable.
  tags = merge(
    {
      "k8s.io/cluster-autoscaler/enabled"               = "true"
      "k8s.io/cluster-autoscaler/${local.cluster_name}" = "owned"
    },
    {
      for key, value in try(each.value.labels, {}) :
      "k8s.io/cluster-autoscaler/node-template/label/${key}" => tostring(value)
    },
    {
      for taint in try(each.value.taints, []) :
      "k8s.io/cluster-autoscaler/node-template/taint/${taint.key}" => "${try(taint.value, "")}:${lookup({
        NO_SCHEDULE        = "NoSchedule"
        PREFER_NO_SCHEDULE = "PreferNoSchedule"
        NO_EXECUTE         = "NoExecute"
      }, taint.effect, taint.effect)}"
    }
  )

  launch_template {
    id      = aws_launch_template.node[each.key].id
    version = aws_launch_template.node[each.key].latest_version
  }

  scaling_config {
    desired_size = try(each.value.capacity_deferred, false) ? 0 : each.value.desired_size
    min_size     = try(each.value.capacity_deferred, false) ? 0 : each.value.min_size
    max_size     = each.value.max_size
  }

  update_config {
    max_unavailable = 1
  }

  dynamic "taint" {
    for_each = try(each.value.taints, [])
    content {
      key    = taint.value.key
      value  = try(taint.value.value, null)
      effect = taint.value.effect
    }
  }

  depends_on = [
    aws_iam_role_policy_attachment.eks_node_worker,
    aws_iam_role_policy_attachment.eks_node_ecr,
    aws_iam_role_policy_attachment.eks_node_cni,
    aws_route_table_association.private,
    data.aws_subnet.existing_workload
  ]

  lifecycle {
    ignore_changes = [scaling_config[0].desired_size]
  }
}

resource "aws_eks_access_entry" "admin" {
  for_each = toset(try(local.config.eks.admin_principal_arns, []))

  cluster_name  = aws_eks_cluster.this.name
  principal_arn = each.value
  type          = "STANDARD"
}

resource "aws_eks_access_policy_association" "admin" {
  for_each = aws_eks_access_entry.admin

  cluster_name  = aws_eks_cluster.this.name
  principal_arn = each.value.principal_arn
  policy_arn    = "arn:aws:eks::aws:cluster-access-policy/AmazonEKSClusterAdminPolicy"

  access_scope {
    type = "cluster"
  }
}

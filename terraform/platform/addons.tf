resource "aws_eks_addon" "base" {
  for_each = local.base_addons

  cluster_name                = local.cluster_name
  addon_name                  = each.key
  resolve_conflicts_on_create = "OVERWRITE"
  resolve_conflicts_on_update = "PRESERVE"

  # Every managed node pool is intentionally tainted. CoreDNS does not
  # tolerate the platform taint by default, so a new cluster would otherwise
  # remain DEGRADED forever with all DNS pods Pending. Keep this critical
  # add-on on the platform pool and preserve the AWS default tolerations.
  configuration_values = each.key == "coredns" && local.workload_scheduling_enabled && local.selected_platform_node_group != null ? jsonencode({
    nodeSelector = local.platform_node_selector
    tolerations = concat([
      {
        key      = "CriticalAddonsOnly"
        operator = "Exists"
      },
      {
        key    = "node-role.kubernetes.io/control-plane"
        effect = "NoSchedule"
      }
    ], local.platform_tolerations)
  }) : null

  timeouts {
    create = "30m"
    update = "30m"
  }
}

data "aws_iam_policy_document" "pod_identity_assume" {
  statement {
    effect  = "Allow"
    actions = ["sts:AssumeRole", "sts:TagSession"]

    principals {
      type        = "Service"
      identifiers = ["pods.eks.amazonaws.com"]
    }
  }
}

resource "aws_iam_role" "ebs_csi" {
  count = local.ebs_csi_enabled ? 1 : 0

  name               = "${local.iam_name_prefix}-ebs-csi"
  assume_role_policy = data.aws_iam_policy_document.pod_identity_assume.json
}

resource "aws_iam_role_policy_attachment" "ebs_csi" {
  count = local.ebs_csi_enabled ? 1 : 0

  role       = aws_iam_role.ebs_csi[0].name
  policy_arn = "arn:aws:iam::aws:policy/service-role/AmazonEBSCSIDriverPolicy"
}

resource "aws_eks_pod_identity_association" "ebs_csi" {
  count = local.ebs_csi_enabled ? 1 : 0

  cluster_name    = local.cluster_name
  namespace       = "kube-system"
  service_account = "ebs-csi-controller-sa"
  role_arn        = aws_iam_role.ebs_csi[0].arn
}

resource "aws_eks_addon" "ebs_csi" {
  count = local.ebs_csi_enabled ? 1 : 0

  cluster_name                = local.cluster_name
  addon_name                  = "aws-ebs-csi-driver"
  resolve_conflicts_on_create = "OVERWRITE"
  resolve_conflicts_on_update = "PRESERVE"

  # The controller is a platform workload while the node DaemonSet must run on
  # every tainted worker node that can attach EBS volumes.
  configuration_values = local.workload_scheduling_enabled && local.selected_platform_node_group != null ? jsonencode({
    controller = {
      nodeSelector = local.platform_node_selector
      tolerations = concat([
        {
          key      = "CriticalAddonsOnly"
          operator = "Exists"
        },
        {
          operator          = "Exists"
          effect            = "NoExecute"
          tolerationSeconds = 300
        }
      ], local.platform_tolerations)
    }
    node = {
      tolerateAllTaints = true
    }
  }) : null

  timeouts {
    create = "30m"
    update = "30m"
  }

  depends_on = [
    aws_iam_role_policy_attachment.ebs_csi,
    aws_eks_pod_identity_association.ebs_csi,
    aws_eks_addon.base
  ]
}

resource "kubernetes_storage_class_v1" "gp3" {
  count = local.ebs_csi_enabled ? 1 : 0

  metadata {
    name = "gp3"
    annotations = {
      "storageclass.kubernetes.io/is-default-class" = "true"
    }
  }

  storage_provisioner    = "ebs.csi.aws.com"
  reclaim_policy         = "Delete"
  volume_binding_mode    = "WaitForFirstConsumer"
  allow_volume_expansion = true

  parameters = {
    type      = "gp3"
    encrypted = "true"
  }

  depends_on = [aws_eks_addon.ebs_csi]
}

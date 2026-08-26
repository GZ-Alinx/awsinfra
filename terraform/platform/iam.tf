resource "aws_iam_role" "load_balancer_controller" {
  count = local.lbc_enabled ? 1 : 0

  name               = "${local.iam_name_prefix}-load-balancer-controller"
  assume_role_policy = data.aws_iam_policy_document.pod_identity_assume.json
}

resource "aws_iam_policy" "load_balancer_controller" {
  count = local.lbc_enabled ? 1 : 0

  name   = "${local.iam_name_prefix}-load-balancer-controller"
  policy = file("${path.module}/policies/aws-load-balancer-controller.json")
}

resource "aws_iam_role_policy_attachment" "load_balancer_controller" {
  count = local.lbc_enabled ? 1 : 0

  role       = aws_iam_role.load_balancer_controller[0].name
  policy_arn = aws_iam_policy.load_balancer_controller[0].arn
}

resource "aws_eks_pod_identity_association" "load_balancer_controller" {
  count = local.lbc_enabled ? 1 : 0

  cluster_name    = local.cluster_name
  namespace       = "kube-system"
  service_account = "aws-load-balancer-controller"
  role_arn        = aws_iam_role.load_balancer_controller[0].arn
}

data "aws_iam_policy_document" "cluster_autoscaler" {
  count = local.autoscaler_enabled ? 1 : 0

  statement {
    effect = "Allow"
    actions = [
      "autoscaling:DescribeAutoScalingGroups",
      "autoscaling:DescribeAutoScalingInstances",
      "autoscaling:DescribeLaunchConfigurations",
      "autoscaling:DescribeScalingActivities",
      "autoscaling:DescribeTags",
      "ec2:DescribeImages",
      "ec2:DescribeInstanceTypes",
      "ec2:DescribeLaunchTemplateVersions",
      "ec2:GetInstanceTypesFromInstanceRequirements",
      "eks:DescribeNodegroup"
    ]
    resources = ["*"]
  }

  statement {
    effect = "Allow"
    actions = [
      "autoscaling:SetDesiredCapacity",
      "autoscaling:TerminateInstanceInAutoScalingGroup"
    ]
    resources = ["*"]

    condition {
      test     = "StringEquals"
      variable = "aws:ResourceTag/k8s.io/cluster-autoscaler/enabled"
      values   = ["true"]
    }

    condition {
      test     = "StringEquals"
      variable = "aws:ResourceTag/k8s.io/cluster-autoscaler/${local.cluster_name}"
      values   = ["owned"]
    }
  }
}

resource "aws_iam_role" "cluster_autoscaler" {
  count = local.autoscaler_enabled ? 1 : 0

  name               = "${local.iam_name_prefix}-cluster-autoscaler"
  assume_role_policy = data.aws_iam_policy_document.pod_identity_assume.json
}

resource "aws_iam_policy" "cluster_autoscaler" {
  count = local.autoscaler_enabled ? 1 : 0

  name   = "${local.iam_name_prefix}-cluster-autoscaler"
  policy = data.aws_iam_policy_document.cluster_autoscaler[0].json
}

resource "aws_iam_role_policy_attachment" "cluster_autoscaler" {
  count = local.autoscaler_enabled ? 1 : 0

  role       = aws_iam_role.cluster_autoscaler[0].name
  policy_arn = aws_iam_policy.cluster_autoscaler[0].arn
}

resource "aws_eks_pod_identity_association" "cluster_autoscaler" {
  count = local.autoscaler_enabled ? 1 : 0

  cluster_name    = local.cluster_name
  namespace       = "kube-system"
  service_account = "cluster-autoscaler"
  role_arn        = aws_iam_role.cluster_autoscaler[0].arn
}

data "aws_iam_policy_document" "external_dns" {
  count = local.external_dns_enabled ? 1 : 0

  statement {
    effect = "Allow"
    actions = [
      "route53:ChangeResourceRecordSets"
    ]
    resources = try(local.components.external_dns.route53_zone_arns, ["*"])
  }

  statement {
    effect = "Allow"
    actions = [
      "route53:ListHostedZones",
      "route53:ListResourceRecordSets",
      "route53:ListTagsForResource"
    ]
    resources = ["*"]
  }
}

resource "aws_iam_role" "external_dns" {
  count = local.external_dns_enabled ? 1 : 0

  name               = "${local.iam_name_prefix}-external-dns"
  assume_role_policy = data.aws_iam_policy_document.pod_identity_assume.json
}

resource "aws_iam_policy" "external_dns" {
  count = local.external_dns_enabled ? 1 : 0

  name   = "${local.iam_name_prefix}-external-dns"
  policy = data.aws_iam_policy_document.external_dns[0].json
}

resource "aws_iam_role_policy_attachment" "external_dns" {
  count = local.external_dns_enabled ? 1 : 0

  role       = aws_iam_role.external_dns[0].name
  policy_arn = aws_iam_policy.external_dns[0].arn
}

resource "aws_eks_pod_identity_association" "external_dns" {
  count = local.external_dns_enabled ? 1 : 0

  cluster_name    = local.cluster_name
  namespace       = "kube-system"
  service_account = "external-dns"
  role_arn        = aws_iam_role.external_dns[0].arn
}

data "aws_iam_policy_document" "platform_backup" {
  count = local.backup_enabled ? 1 : 0

  statement {
    effect = "Allow"
    actions = [
      "s3:ListBucket",
      "s3:GetBucketLocation"
    ]
    resources = ["arn:aws:s3:::${local.backup_bucket}"]
  }

  statement {
    effect = "Allow"
    actions = [
      "s3:PutObject",
      "s3:AbortMultipartUpload"
    ]
    resources = ["arn:aws:s3:::${local.backup_bucket}/*"]
  }
}

resource "aws_iam_role" "platform_backup" {
  count = local.backup_enabled ? 1 : 0

  name               = "${local.iam_name_prefix}-platform-backup"
  assume_role_policy = data.aws_iam_policy_document.pod_identity_assume.json
}

resource "aws_iam_policy" "platform_backup" {
  count = local.backup_enabled ? 1 : 0

  name   = "${local.iam_name_prefix}-platform-backup"
  policy = data.aws_iam_policy_document.platform_backup[0].json
}

resource "aws_iam_role_policy_attachment" "platform_backup" {
  count = local.backup_enabled ? 1 : 0

  role       = aws_iam_role.platform_backup[0].name
  policy_arn = aws_iam_policy.platform_backup[0].arn
}

resource "kubernetes_service_account_v1" "platform_backup" {
  for_each = local.backup_namespaces

  metadata {
    name      = "platform-backup"
    namespace = kubernetes_namespace_v1.this[each.value].metadata[0].name
  }
}

resource "aws_eks_pod_identity_association" "platform_backup" {
  for_each = local.backup_namespaces

  cluster_name    = local.cluster_name
  namespace       = each.value
  service_account = kubernetes_service_account_v1.platform_backup[each.value].metadata[0].name
  role_arn        = aws_iam_role.platform_backup[0].arn
}

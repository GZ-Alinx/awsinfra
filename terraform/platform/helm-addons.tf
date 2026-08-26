resource "helm_release" "load_balancer_controller" {
  count = local.lbc_enabled ? 1 : 0

  name       = "aws-load-balancer-controller"
  repository = "https://aws.github.io/eks-charts"
  chart      = "aws-load-balancer-controller"
  version    = local.components.aws_load_balancer_controller.chart_version
  namespace  = "kube-system"

  values = [yamlencode({
    clusterName = local.cluster_name
    region      = local.region
    vpcId       = local.vpc_id
    serviceAccount = {
      create = true
      name   = "aws-load-balancer-controller"
    }
    # Higress always supplies at least one frontend security group so the NLB
    # keeps security-group support for its full lifecycle. Keep the shared
    # backend SG feature explicit: the Service annotation can then authorize
    # the controller to reconcile target ENI rules. Restricted rules remain
    # enabled so only the Service's actual target port range is opened.
    enableBackendSecurityGroup          = true
    disableRestrictedSecurityGroupRules = false
    nodeSelector                        = local.platform_node_selector
    tolerations                         = local.platform_tolerations
  })]

  wait    = true
  timeout = 900

  depends_on = [
    aws_eks_addon.base,
    aws_eks_pod_identity_association.load_balancer_controller,
    aws_iam_role_policy_attachment.load_balancer_controller
  ]
}

resource "helm_release" "metrics_server" {
  count = local.metrics_server_enabled ? 1 : 0

  name       = "metrics-server"
  repository = "https://kubernetes-sigs.github.io/metrics-server"
  chart      = "metrics-server"
  version    = local.components.metrics_server.chart_version
  namespace  = "kube-system"

  values = [yamlencode({
    replicas = 2
    podDisruptionBudget = {
      enabled      = true
      minAvailable = 1
    }
    nodeSelector = local.platform_node_selector
    tolerations  = local.platform_tolerations
  })]

  wait    = true
  timeout = 600
}

resource "helm_release" "cluster_autoscaler" {
  count = local.autoscaler_enabled ? 1 : 0

  name       = "cluster-autoscaler"
  repository = "https://kubernetes.github.io/autoscaler"
  chart      = "cluster-autoscaler"
  version    = local.components.cluster_autoscaler.chart_version
  namespace  = "kube-system"

  values = [yamlencode({
    autoDiscovery = {
      clusterName = local.cluster_name
    }
    awsRegion = local.region
    # Cluster Autoscaler only supports the Kubernetes minor version it was
    # built for. EKS stores the version as a minor release (for example 1.36),
    # while the upstream image uses a full semantic version (for example
    # v1.36.0).
    image = {
      tag = "v${local.config.eks.kubernetes_version}.0"
    }
    rbac = {
      serviceAccount = {
        create = true
        name   = "cluster-autoscaler"
      }
      # Kubernetes 1.34+ exposes Dynamic Resource Allocation objects to the
      # scheduler snapshot used by Cluster Autoscaler. Chart 9.53.0 does not
      # yet include these resources in its default ClusterRole. Without the
      # read permissions below the autoscaler remains in "Initializing" and
      # Pending Pods never trigger a node-group scale-up.
      additionalRules = [{
        apiGroups = ["resource.k8s.io"]
        resources = [
          "deviceclasses",
          "resourceclaims",
          "resourceclaimtemplates",
          "resourceslices"
        ]
        verbs = ["get", "list", "watch"]
      }]
    }
    extraArgs = {
      balance-similar-node-groups = true
      expander                    = "least-waste"
      # Platform-managed Pods use emptyDir for disposable runtime files. The
      # default value (true) makes every such node permanently unremovable,
      # even when PDBs and persistent volumes make the workload drain-safe.
      skip-nodes-with-local-storage = false
      skip-nodes-with-system-pods   = false
    }
    nodeSelector = local.platform_node_selector
    tolerations  = local.platform_tolerations
  })]

  wait    = true
  timeout = 600

  depends_on = [
    aws_eks_addon.base,
    aws_eks_pod_identity_association.cluster_autoscaler,
    aws_iam_role_policy_attachment.cluster_autoscaler
  ]
}

resource "helm_release" "external_dns" {
  count = local.external_dns_enabled ? 1 : 0

  name       = "external-dns"
  repository = "https://kubernetes-sigs.github.io/external-dns"
  chart      = "external-dns"
  version    = local.components.external_dns.chart_version
  namespace  = "kube-system"

  values = [yamlencode({
    provider = {
      name = "aws"
    }
    policy        = "sync"
    registry      = "txt"
    txtOwnerId    = local.cluster_name
    domainFilters = try(local.components.external_dns.domain_filters, [])
    sources       = ["service", "ingress"]
    serviceAccount = {
      create = true
      name   = "external-dns"
    }
    nodeSelector = local.platform_node_selector
    tolerations  = local.platform_tolerations
  })]

  wait    = true
  timeout = 600

  depends_on = [
    aws_eks_addon.base,
    aws_eks_pod_identity_association.external_dns,
    aws_iam_role_policy_attachment.external_dns
  ]
}

resource "helm_release" "cert_manager" {
  count = local.cert_manager_enabled ? 1 : 0

  name             = "cert-manager"
  repository       = "https://charts.jetstack.io"
  chart            = "cert-manager"
  version          = local.components.cert_manager.chart_version
  namespace        = "cert-manager"
  create_namespace = true

  values = [yamlencode({
    crds = {
      enabled = true
    }
    replicaCount = 2
    nodeSelector = local.platform_node_selector
    tolerations  = local.platform_tolerations
    webhook = {
      nodeSelector = local.platform_node_selector
      tolerations  = local.platform_tolerations
    }
    cainjector = {
      nodeSelector = local.platform_node_selector
      tolerations  = local.platform_tolerations
    }
  })]

  wait    = true
  timeout = 600
}

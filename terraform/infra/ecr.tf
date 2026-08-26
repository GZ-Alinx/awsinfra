# ECR repositories are account-and-Region resources. They are created and
# reconciled idempotently from each environment's explicit repository list by
# the deployment runner before Terraform plans the environment. Keeping them
# outside environment state prevents an environment destroy from deleting
# either its image history or another environment's repositories.

# Environments created by older platform releases may still have ECR objects
# in their Terraform state. Forget those addresses without deleting the AWS
# repositories or their lifecycle policies.
removed {
  from = aws_ecr_repository.this

  lifecycle {
    destroy = false
  }
}

removed {
  from = aws_ecr_lifecycle_policy.this

  lifecycle {
    destroy = false
  }
}

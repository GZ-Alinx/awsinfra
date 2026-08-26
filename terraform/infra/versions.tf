terraform {
  required_version = ">= 1.9.0"

  # Runtime values and the dedicated state-center identity are supplied by the
  # Go deployment service. Project AWS credentials remain provider-only.
  backend "s3" {}

  required_providers {
    aws = {
      source  = "hashicorp/aws"
      version = "~> 6.61"
    }
    random = {
      source  = "hashicorp/random"
      version = "~> 3.7"
    }
  }
}

provider "aws" {
  region = local.region

  # AWS can attach this account-level migration tag outside Terraform. It is
  # provider-managed metadata and must not create noisy, destructive drift in
  # otherwise unrelated deployment plans.
  ignore_tags {
    keys = ["map-migrated"]
  }

  default_tags {
    tags = local.common_tags
  }
}

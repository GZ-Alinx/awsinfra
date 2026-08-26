variable "config_file" {
  description = "Absolute or module-relative path to the environment YAML configuration."
  type        = string
}

variable "data_service_passwords" {
  description = "Runtime-only self-managed master passwords. Supplied by the platform credential vault and never stored in environment YAML."
  type        = map(string)
  default     = {}
  sensitive   = true
}

variable "eks_public_access_cidrs_override" {
  description = "Runtime-only union of the AWS EKS API public access CIDRs and the CIDRs requested by the platform. Null uses the environment configuration."
  type        = list(string)
  default     = null
}

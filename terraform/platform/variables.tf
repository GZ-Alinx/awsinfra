variable "config_file" {
  description = "Absolute or module-relative path to the environment YAML configuration."
  type        = string
}

variable "deployment_phase" {
  description = "base installs required EKS services; components adds optional components and access resources; access updates only TLS, domains, TCP routes and alerts."
  type        = string
  default     = "components"

  validation {
    condition     = contains(["base", "components", "access"], var.deployment_phase)
    error_message = "deployment_phase must be base, components, or access."
  }
}

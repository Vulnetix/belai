variable "cloudflare_api_token" {
  description = "Cloudflare API token with DNS edit permission on the vulnetix.com zone."
  type        = string
  sensitive   = true
}

variable "cloudflare_zone_id" {
  description = "Cloudflare zone ID for vulnetix.com."
  type        = string
  default     = "d3aeda6727dbbae840f5abb1aab1444d"
}

variable "github_pages_target" {
  description = "GitHub Pages CNAME target for the Vulnetix organisation."
  type        = string
  default     = "vulnetix.github.io"
}

variable "manage_pages" {
  description = "Whether Terraform owns the GitHub Pages block. Default false; import the repository before enabling."
  type        = bool
  default     = false
}

variable "github_token" {
  description = "GitHub token for the integrations/github provider. Required only when manage_pages is true."
  type        = string
  sensitive   = true
  default     = ""
}
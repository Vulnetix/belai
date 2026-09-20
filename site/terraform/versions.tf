terraform {
  required_version = ">= 1.5"

  required_providers {
    cloudflare = {
      source  = "cloudflare/cloudflare"
      version = "~> 5.0"
    }

    github = {
      source  = "integrations/github"
      version = "~> 6.0"
    }
  }

  backend "s3" {
    bucket       = "vdb-manager-terraform-state"
    key          = "signet-site/terraform.tfstate"
    region       = "ap-southeast-2"
    encrypt      = true
    use_lockfile = true
  }
}

provider "cloudflare" {
  api_token = var.cloudflare_api_token
}

provider "github" {
  # The repository lives in the Vulnetix org, not the authenticating user's
  # account; pin the owner so repository names resolve there.
  owner = "Vulnetix"
  token = var.github_token
}
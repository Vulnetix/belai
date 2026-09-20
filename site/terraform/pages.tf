# GitHub Pages configuration, delivered gated and safe.
#
# The provider now ships a dedicated github_repository_pages resource, so this
# file owns *only* the Pages config — never the whole repository. It is still
# net-new for the org (no sibling repo uses the GitHub provider), so it defaults
# to inert:
#
#   * var.manage_pages defaults to false — `terraform plan` proposes no change.
#   * The first enabling step is a documented import, never a blind apply:
#
#       terraform import github_repository_pages.signet signet
#
# Flip manage_pages on only after reading `terraform plan` output.

resource "github_repository_pages" "signet" {
  count = var.manage_pages ? 1 : 0

  repository = "signet"

  # The deployment is performed by .github/workflows/pages.yml via
  # actions/deploy-pages; GitHub therefore runs in "workflow" build mode and
  # the custom domain is declared here to match site/public/CNAME.
  build_type = "workflow"
  cname      = "signet.vulnetix.com"

  lifecycle {
    # Never let a stray `terraform destroy` delete the Pages binding.
    prevent_destroy = true
  }
}
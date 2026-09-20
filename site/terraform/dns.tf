# DNS for the Signet marketing site.
#
# signet.vulnetix.com -> GitHub Pages.
#
# Must NOT be proxied. GitHub Pages terminates TLS itself using a certificate it
# issues for the custom domain, and it can only complete that issuance when it
# sees the real CNAME. Turning the orange cloud on leaves Pages unable to
# validate the domain and serving a certificate error.
#
# The value must stay in step with site/public/CNAME: Astro publishes that file
# verbatim, and Pages reads it to decide which domain it is serving. If the two
# disagree, Pages unbinds the custom domain on the next deploy — which is why
# .github/workflows/pages.yml asserts the file after every build.

data "cloudflare_zone" "vulnetix" {
  zone_id = var.cloudflare_zone_id
}

resource "cloudflare_dns_record" "signet" {
  zone_id = var.cloudflare_zone_id
  name    = "signet" # label, not FQDN
  type    = "CNAME"
  content = var.github_pages_target
  proxied = false # mandatory — Pages cannot issue LE behind the proxy
  ttl     = 300

  comment = "Keep in sync with site/public/CNAME."
}
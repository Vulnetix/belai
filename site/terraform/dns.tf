# DNS for the Belai marketing site.
#
# belai.vulnetix.com -> GitHub Pages.
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

resource "cloudflare_dns_record" "belai" {
  zone_id = var.cloudflare_zone_id
  name    = "belai" # label, not FQDN
  type    = "CNAME"
  content = var.github_pages_target
  proxied = false # mandatory — Pages cannot issue LE behind the proxy
  ttl     = 300

  comment = "Keep in sync with site/public/CNAME."
}
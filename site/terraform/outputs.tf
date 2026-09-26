output "site_url" {
  description = "Public URL of the marketing site."
  value       = "https://belai.vulnetix.com/"
}

output "site_record" {
  description = "The managed CNAME, for confirming it matches site/public/CNAME."
  value = {
    name    = cloudflare_dns_record.belai.name
    type    = cloudflare_dns_record.belai.type
    content = cloudflare_dns_record.belai.content
    proxied = cloudflare_dns_record.belai.proxied
  }
}
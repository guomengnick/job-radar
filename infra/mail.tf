# Email ingest (extension 1). Job-alert mail that the radar cannot fetch through
# a public API (104) is forwarded to an address on this domain and received by
# SES. Route 53 hosts the domain's DNS so Terraform manages the MX and DKIM
# records SES needs. Everything here is skipped when mail_domain is empty.
#
# After the first apply, set the registrar's nameservers to the
# mail_name_servers output. Destroying the zone gives it new nameservers, so
# the registrar must be updated again after a rebuild.

locals {
  # A one-key map, so each resource is addressed as [var.mail_domain].
  mail = var.mail_domain == "" ? {} : { (var.mail_domain) = true }
}

resource "aws_route53_zone" "mail" {
  for_each = local.mail
  name     = each.key
  comment  = "job-radar email ingest"
}

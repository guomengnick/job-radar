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

# --- SES receiving -----------------------------------------------------------

locals {
  mail_rule_set = "job-radar"
  mail_rule     = "store-job-alerts"
}

# SES only receives mail for a verified domain. Easy DKIM verifies it with
# three CNAME records; SES keeps checking until they resolve, so verification
# finishes once the registrar points at the Route 53 nameservers.
resource "aws_sesv2_email_identity" "mail" {
  for_each       = local.mail
  email_identity = each.key
}

resource "aws_route53_record" "mail_dkim" {
  # Easy DKIM always issues three tokens. Their values are only known after
  # apply, so the records are counted rather than keyed by token.
  count   = var.mail_domain == "" ? 0 : 3
  zone_id = aws_route53_zone.mail[var.mail_domain].zone_id
  name    = "${aws_sesv2_email_identity.mail[var.mail_domain].dkim_signing_attributes[0].tokens[count.index]}._domainkey.${var.mail_domain}"
  type    = "CNAME"
  ttl     = 1800
  records = ["${aws_sesv2_email_identity.mail[var.mail_domain].dkim_signing_attributes[0].tokens[count.index]}.dkim.amazonses.com"]
}

resource "aws_route53_record" "mail_mx" {
  for_each = local.mail
  zone_id  = aws_route53_zone.mail[each.key].zone_id
  name     = each.key
  type     = "MX"
  ttl      = 300
  records  = ["10 inbound-smtp.${var.region}.amazonaws.com"]
}

# Raw messages land here. They can contain personal data, so the bucket is
# private and encrypted, and objects expire after 30 days.
resource "aws_s3_bucket" "mail" {
  for_each = local.mail
  bucket   = "job-radar-mail-${data.aws_caller_identity.me.account_id}"
}

resource "aws_s3_bucket_public_access_block" "mail" {
  for_each                = local.mail
  bucket                  = aws_s3_bucket.mail[each.key].id
  block_public_acls       = true
  block_public_policy     = true
  ignore_public_acls      = true
  restrict_public_buckets = true
}

resource "aws_s3_bucket_server_side_encryption_configuration" "mail" {
  for_each = local.mail
  bucket   = aws_s3_bucket.mail[each.key].id
  rule {
    apply_server_side_encryption_by_default {
      sse_algorithm = "AES256"
    }
  }
}

resource "aws_s3_bucket_lifecycle_configuration" "mail" {
  for_each = local.mail
  bucket   = aws_s3_bucket.mail[each.key].id
  rule {
    id     = "expire-raw-mail"
    status = "Enabled"
    filter {}
    expiration {
      days = 30
    }
  }
}

# A resource policy: it says who may write into the bucket (SES, and only for
# this account's receipt rule), while the Lambda roles in iam.tf say what each
# function may reach.
data "aws_iam_policy_document" "mail_bucket" {
  for_each = local.mail
  statement {
    sid       = "AllowSESPuts"
    actions   = ["s3:PutObject"]
    resources = ["${aws_s3_bucket.mail[each.key].arn}/*"]
    principals {
      type        = "Service"
      identifiers = ["ses.amazonaws.com"]
    }
    condition {
      test     = "StringEquals"
      variable = "AWS:SourceAccount"
      values   = [data.aws_caller_identity.me.account_id]
    }
    condition {
      test     = "StringEquals"
      variable = "AWS:SourceArn"
      values   = ["arn:aws:ses:${var.region}:${data.aws_caller_identity.me.account_id}:receipt-rule-set/${local.mail_rule_set}:receipt-rule/${local.mail_rule}"]
    }
  }
}

resource "aws_s3_bucket_policy" "mail" {
  for_each = local.mail
  bucket   = aws_s3_bucket.mail[each.key].id
  policy   = data.aws_iam_policy_document.mail_bucket[each.key].json
}

resource "aws_ses_receipt_rule_set" "mail" {
  for_each      = local.mail
  rule_set_name = local.mail_rule_set
}

# Only one rule set can be active per account and region.
resource "aws_ses_active_receipt_rule_set" "mail" {
  for_each      = local.mail
  rule_set_name = aws_ses_receipt_rule_set.mail[each.key].rule_set_name
}

resource "aws_ses_receipt_rule" "store" {
  for_each      = local.mail
  name          = local.mail_rule
  rule_set_name = aws_ses_receipt_rule_set.mail[each.key].rule_set_name
  recipients    = ["jobs@${each.key}"]
  enabled       = true
  scan_enabled  = true
  tls_policy    = "Require"

  s3_action {
    bucket_name       = aws_s3_bucket.mail[each.key].id
    object_key_prefix = "inbound/"
    position          = 1
  }

  # SES checks that it may write to the bucket when the rule is created.
  depends_on = [aws_s3_bucket_policy.mail]
}

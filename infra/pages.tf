# The batch button's pages. Each batch is kept as sent, so it can be judged
# again (a new rubric, a failed request) without reopening the tabs. Job
# pages are public, but which ones the user opened is personal: private,
# encrypted, and expired after 30 days, as the mail is.
resource "aws_s3_bucket" "pages" {
  bucket = "job-radar-pages-${data.aws_caller_identity.me.account_id}"
}

resource "aws_s3_bucket_public_access_block" "pages" {
  bucket                  = aws_s3_bucket.pages.id
  block_public_acls       = true
  block_public_policy     = true
  ignore_public_acls      = true
  restrict_public_buckets = true
}

resource "aws_s3_bucket_server_side_encryption_configuration" "pages" {
  bucket = aws_s3_bucket.pages.id
  rule {
    apply_server_side_encryption_by_default {
      sse_algorithm = "AES256"
    }
  }
}

resource "aws_s3_bucket_lifecycle_configuration" "pages" {
  bucket = aws_s3_bucket.pages.id
  rule {
    id     = "expire-pages"
    status = "Enabled"
    filter {}
    expiration {
      days = 30
    }
  }
}

# The cheap rules a page goes through before the model (app.PageRules). A
# plain String, not a secret: the user changes it with one command, and
# Terraform only creates it, so an edit is never reverted. See DEPLOY.
resource "aws_ssm_parameter" "batch_rules" {
  name = var.batch_rules_param
  type = "String"
  value = jsonencode({
    exclude_title_words    = ["現場工程師", "業務", "韌體"]
    exclude_location_words = []
    min_annual_salary      = 0
    min_monthly_top_pay    = 0
  })

  lifecycle {
    ignore_changes = [value]
  }
}

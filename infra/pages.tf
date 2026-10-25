# Job pages the model judged. batches/ keeps each batch as sent, so it can
# be judged again (a new rubric, a failed request) without reopening the
# tabs: 30 days, as the mail. jd/ keeps each judged page on its own, which
# its judgement points at: a year, so judgements can be checked against the
# posting and used as an evaluation set. Job pages are public, but which
# ones the user opened is personal: private and encrypted.
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
    id     = "expire-batches"
    status = "Enabled"
    filter {
      prefix = "batches/"
    }
    expiration {
      days = 30
    }
  }

  rule {
    id     = "expire-judged-pages"
    status = "Enabled"
    filter {
      prefix = "jd/"
    }
    expiration {
      days = 365
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

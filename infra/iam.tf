# Least privilege per function: only digest can read the bot token, and only
# sync/scan can write boards. See docs/ARCHITECTURE.md §安全.

data "aws_iam_policy_document" "lambda_assume" {
  statement {
    actions = ["sts:AssumeRole"]
    principals {
      type        = "Service"
      identifiers = ["lambda.amazonaws.com"]
    }
  }
}

resource "aws_iam_role" "fn" {
  for_each           = local.functions
  name               = "job-radar-${each.key}"
  assume_role_policy = data.aws_iam_policy_document.lambda_assume.json
}

data "aws_caller_identity" "me" {}

locals {
  param_arn = "arn:aws:ssm:${var.region}:${data.aws_caller_identity.me.account_id}:parameter${var.telegram_token_param}"

  statements = {
    scan = [
      { actions = ["dynamodb:Query", "dynamodb:BatchWriteItem"], resources = [aws_dynamodb_table.boards.arn] },
      { actions = ["dynamodb:PutItem"], resources = [aws_dynamodb_table.jobs.arn, aws_dynamodb_table.runs.arn] },
    ]
    digest = [
      { actions = ["dynamodb:Query"], resources = [aws_dynamodb_table.runs.arn, "${aws_dynamodb_table.jobs.arn}/index/pending-index", "${aws_dynamodb_table.jobs.arn}/index/source-index"] },
      { actions = ["dynamodb:UpdateItem"], resources = [aws_dynamodb_table.jobs.arn] },
      { actions = ["ssm:GetParameter"], resources = [local.param_arn] },
    ]
    sync = [
      { actions = ["dynamodb:Query", "dynamodb:BatchWriteItem"], resources = [aws_dynamodb_table.boards.arn] },
    ]
    # Read its four parameters; store the judgement (assess# items); list
    # the 104 jobs for the extension.
    clip = [
      { actions = ["ssm:GetParameter"], resources = [for p in [var.anthropic_key_param, var.assess_profile_param, var.clip_secret_param, var.telegram_token_param] :
      "arn:aws:ssm:${var.region}:${data.aws_caller_identity.me.account_id}:parameter${p}"] },
      { actions = ["dynamodb:PutItem"], resources = [aws_dynamodb_table.jobs.arn] },
      { actions = ["dynamodb:Query"], resources = ["${aws_dynamodb_table.jobs.arn}/index/source-index"] },
    ]
    # Read stored mail; insert 104 alert jobs (conditional puts) and the run
    # record. No reads, updates or deletes on either table.
    ingest = [
      { actions = ["s3:GetObject"], resources = [for b in aws_s3_bucket.mail : "${b.arn}/inbound/*"] },
      { actions = ["dynamodb:PutItem"], resources = [aws_dynamodb_table.jobs.arn, aws_dynamodb_table.runs.arn] },
    ]
  }
}

data "aws_iam_policy_document" "fn" {
  for_each = local.functions

  # The log group ARN is spelled out rather than read from the resource, so
  # adding a function does not defer (and re-show) every role's policy.
  statement {
    actions   = ["logs:CreateLogStream", "logs:PutLogEvents"]
    resources = ["arn:aws:logs:${var.region}:${data.aws_caller_identity.me.account_id}:log-group:/aws/lambda/job-radar-${each.key}:*"]
  }

  dynamic "statement" {
    for_each = local.statements[each.key]
    content {
      actions   = statement.value.actions
      resources = statement.value.resources
    }
  }
}

resource "aws_iam_role_policy" "fn" {
  for_each = local.functions
  name     = "job-radar-${each.key}"
  role     = aws_iam_role.fn[each.key].id
  policy   = data.aws_iam_policy_document.fn[each.key].json
}

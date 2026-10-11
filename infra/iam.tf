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
      { actions = ["dynamodb:Query"], resources = [aws_dynamodb_table.runs.arn, "${aws_dynamodb_table.jobs.arn}/index/pending-index"] },
      { actions = ["dynamodb:UpdateItem"], resources = [aws_dynamodb_table.jobs.arn] },
      { actions = ["ssm:GetParameter"], resources = [local.param_arn] },
    ]
    sync = [
      { actions = ["dynamodb:Query", "dynamodb:BatchWriteItem"], resources = [aws_dynamodb_table.boards.arn] },
    ]
  }
}

data "aws_iam_policy_document" "fn" {
  for_each = local.functions

  statement {
    actions   = ["logs:CreateLogStream", "logs:PutLogEvents"]
    resources = ["${aws_cloudwatch_log_group.fn[each.key].arn}:*"]
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

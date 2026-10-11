# One Go binary, three functions with different timeouts and permissions.
# Lambda over Fargate/EC2: the job runs minutes a day and idles otherwise. See ADR 1.

locals {
  functions = {
    scan   = { timeout = 900, memory = 512 }
    digest = { timeout = 60, memory = 256 }
    sync   = { timeout = 300, memory = 256 }
  }
  tables = {
    TABLE_BOARDS = aws_dynamodb_table.boards.name
    TABLE_JOBS   = aws_dynamodb_table.jobs.name
    TABLE_RUNS   = aws_dynamodb_table.runs.name
  }
  digest_env = {
    TELEGRAM_TOKEN_PARAM = var.telegram_token_param
    TELEGRAM_CHAT_ID     = var.telegram_chat_id
    EXPECTED_ATS         = "greenhouse,lever,ashby"
  }
}

resource "aws_cloudwatch_log_group" "fn" {
  for_each          = local.functions
  name              = "/aws/lambda/job-radar-${each.key}"
  retention_in_days = 14
}

resource "aws_lambda_function" "fn" {
  for_each         = local.functions
  function_name    = "job-radar-${each.key}"
  role             = aws_iam_role.fn[each.key].arn
  filename         = var.lambda_zip
  source_code_hash = filebase64sha256(var.lambda_zip)
  handler          = "bootstrap"
  runtime          = "provided.al2023"
  architectures    = ["arm64"]
  timeout          = each.value.timeout
  memory_size      = each.value.memory

  environment {
    variables = merge(local.tables, each.key == "digest" ? local.digest_env : {})
  }

  depends_on = [aws_cloudwatch_log_group.fn]
}

# Scheduler invokes asynchronously; Lambda's own async retries would re-run a
# failed scan or send the digest twice. A failed run is reported instead.
resource "aws_lambda_function_event_invoke_config" "fn" {
  for_each               = local.functions
  function_name          = aws_lambda_function.fn[each.key].function_name
  maximum_retry_attempts = 0
}

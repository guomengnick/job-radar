# One Go binary, one function per task with its own timeout and permissions.
# Lambda over Fargate/EC2: the job runs minutes a day and idles otherwise. See ADR 1.

locals {
  # retries: Lambda's own retries of an asynchronous invocation.
  functions = merge(
    {
      scan   = { timeout = 900, memory = 512, retries = 0 }
      digest = { timeout = 60, memory = 256, retries = 0 }
      sync   = { timeout = 300, memory = 256, retries = 0 }
      # Called by the browser extension and answered synchronously; one model
      # call takes 10–60 seconds, submitting a batch a few.
      clip = { timeout = 120, memory = 256, retries = 0 }
      # Every ten minutes: sends the summary of each batch that has ended.
      # Once a day after the digest: sends the day's board jobs as a batch.
      batch = { timeout = 120, memory = 256, retries = 0 }
      # Forwards each alarm from the SNS topic to Telegram. SNS invokes it
      # asynchronously; Lambda's two retries ride out a Telegram hiccup.
      alarm = { timeout = 30, memory = 128, retries = 2 }
    },
    # ingest exists only when mail receiving is configured (mail.tf).
    { for d in keys(local.mail) : "ingest" => { timeout = 60, memory = 256, retries = 2 } },
  )
  tables = {
    TABLE_BOARDS = aws_dynamodb_table.boards.name
    TABLE_JOBS   = aws_dynamodb_table.jobs.name
    TABLE_RUNS   = aws_dynamodb_table.runs.name
  }
  telegram_env = {
    TELEGRAM_TOKEN_PARAM = var.telegram_token_param
    TELEGRAM_CHAT_ID     = var.telegram_chat_id
  }
  env = {
    digest = merge(local.telegram_env, { EXPECTED_ATS = "greenhouse,lever,ashby" })
    clip = merge(local.telegram_env, {
      ANTHROPIC_KEY_PARAM  = var.anthropic_key_param
      ASSESS_PROFILE_PARAM = var.assess_profile_param
      CLIP_SECRET_PARAM    = var.clip_secret_param
      BATCH_RULES_PARAM    = var.batch_rules_param
      PAGES_BUCKET         = aws_s3_bucket.pages.bucket
    })
    # The scan keeps the pages of the board jobs it will notify.
    scan = { PAGES_BUCKET = aws_s3_bucket.pages.bucket }
    batch = merge(local.telegram_env, {
      ANTHROPIC_KEY_PARAM  = var.anthropic_key_param
      ASSESS_PROFILE_PARAM = var.assess_profile_param
      PAGES_BUCKET         = aws_s3_bucket.pages.bucket
      DLQ_URL              = aws_sqs_queue.dlq.url
    })
    alarm = local.telegram_env
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
    variables = merge(local.tables, lookup(local.env, each.key, {}))
  }

  depends_on = [aws_cloudwatch_log_group.fn]
}

# Scheduler invokes asynchronously; Lambda's own async retries would re-run a
# failed scan or send the digest twice, so those report a failed run instead.
# ingest is the exception: S3 invokes it, a failure there is transient (a bad
# message is reported, not raised), and re-reading a message is harmless.
resource "aws_lambda_function_event_invoke_config" "fn" {
  for_each               = local.functions
  function_name          = aws_lambda_function.fn[each.key].function_name
  maximum_retry_attempts = each.value.retries

  # An asynchronous run (the scheduler, S3, SNS) that still fails after
  # those retries goes to the dead-letter queue: AWS sends the original
  # event and the error, and radar redrive can run it again. clip answers
  # the extension synchronously and has no such runs.
  dynamic "destination_config" {
    for_each = each.key == "clip" ? [] : [1]
    content {
      on_failure {
        destination = aws_sqs_queue.dlq.arn
      }
    }
  }
}

# The 送到雷達 endpoint. The URL is public (the extension cannot sign AWS
# requests), so the function itself checks the shared secret before doing
# anything. Both permissions are needed for a public URL: InvokeFunctionUrl,
# and InvokeFunction limited to calls that arrive through the URL.
resource "aws_lambda_function_url" "clip" {
  function_name      = aws_lambda_function.fn["clip"].function_name
  authorization_type = "NONE"
}

resource "aws_lambda_permission" "clip_url" {
  statement_id           = "public-function-url"
  action                 = "lambda:InvokeFunctionUrl"
  function_name          = aws_lambda_function.fn["clip"].function_name
  principal              = "*"
  function_url_auth_type = "NONE"
}

resource "aws_lambda_permission" "clip_invoke" {
  statement_id             = "invoke-through-function-url"
  action                   = "lambda:InvokeFunction"
  function_name            = aws_lambda_function.fn["clip"].function_name
  principal                = "*"
  invoked_via_function_url = true
}

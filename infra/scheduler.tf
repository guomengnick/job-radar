# EventBridge Scheduler: cron with a real time zone, so 08:00 means 08:00 in Taipei.

data "aws_iam_policy_document" "scheduler_assume" {
  statement {
    actions = ["sts:AssumeRole"]
    principals {
      type        = "Service"
      identifiers = ["scheduler.amazonaws.com"]
    }
  }
}

resource "aws_iam_role" "scheduler" {
  name               = "job-radar-scheduler"
  assume_role_policy = data.aws_iam_policy_document.scheduler_assume.json
}

resource "aws_iam_role_policy" "scheduler" {
  name = "invoke-job-radar"
  role = aws_iam_role.scheduler.id
  policy = jsonencode({
    Version = "2012-10-17"
    Statement = [{
      Effect   = "Allow"
      Action   = "lambda:InvokeFunction"
      Resource = [for f in aws_lambda_function.fn : f.arn]
    }]
  })
}

locals {
  ats = ["greenhouse", "lever", "ashby"]

  schedules = merge(
    { for a in local.ats : "scan-${a}" => {
      cron = "cron(0 8 * * ? *)", fn = "scan", input = { task = "scan", ats = a }
    } },
    { for a in local.ats : "sync-${a}" => {
      cron = "cron(0 7 ? * SUN *)", fn = "sync", input = { task = "sync-boards", ats = a }
    } },
    { digest = { cron = "cron(40 8 * * ? *)", fn = "digest", input = { task = "digest" } } },
  )
}

resource "aws_scheduler_schedule" "s" {
  for_each                     = local.schedules
  name                         = "job-radar-${each.key}"
  schedule_expression          = each.value.cron
  schedule_expression_timezone = var.timezone

  flexible_time_window {
    mode = "OFF"
  }

  target {
    arn      = aws_lambda_function.fn[each.value.fn].arn
    role_arn = aws_iam_role.scheduler.arn
    input    = jsonencode(each.value.input)

    retry_policy {
      maximum_retry_attempts = 1
    }
  }
}

resource "aws_sns_topic" "alerts" {
  name = "job-radar-alerts"
}

# Email stays as the fallback: it still arrives if the alarm function or
# Telegram is down.
resource "aws_sns_topic_subscription" "email" {
  topic_arn = aws_sns_topic.alerts.arn
  protocol  = "email"
  endpoint  = var.alert_email
}

# Every alarm also goes to Telegram, in Chinese, through the alarm function.
resource "aws_sns_topic_subscription" "telegram" {
  topic_arn = aws_sns_topic.alerts.arn
  protocol  = "lambda"
  endpoint  = aws_lambda_function.fn["alarm"].arn
}

resource "aws_lambda_permission" "alarm_sns" {
  statement_id  = "invoke-from-alerts-topic"
  action        = "lambda:InvokeFunction"
  function_name = aws_lambda_function.fn["alarm"].function_name
  principal     = "sns.amazonaws.com"
  source_arn    = aws_sns_topic.alerts.arn
}

# Heartbeat: the radar failing silently is the worst failure, so alarm when
# no scan succeeded in 24 hours. Missing data counts as breaching.
resource "aws_cloudwatch_metric_alarm" "heartbeat" {
  alarm_name          = "job-radar-no-successful-scan-24h"
  alarm_description   = "24 小時內沒有一次成功的掃描。看 /aws/lambda/job-radar-scan 的 log。"
  namespace           = "JobRadar"
  metric_name         = "RunSucceeded"
  statistic           = "Sum"
  period              = 21600
  evaluation_periods  = 4
  datapoints_to_alarm = 4
  threshold           = 1
  comparison_operator = "LessThanThreshold"
  treat_missing_data  = "breaching"
  alarm_actions       = [aws_sns_topic.alerts.arn]
  ok_actions          = [aws_sns_topic.alerts.arn]
}

resource "aws_cloudwatch_metric_alarm" "throttled" {
  alarm_name          = "job-radar-throttled"
  alarm_description   = "6 小時內被限速（HTTP 429）超過 100 次：調低 config/radar.yml 的 scan.rps。"
  namespace           = "JobRadar"
  metric_name         = "BoardsThrottled"
  statistic           = "Sum"
  period              = 21600
  evaluation_periods  = 1
  threshold           = 100
  comparison_operator = "GreaterThanThreshold"
  treat_missing_data  = "notBreaching"
  alarm_actions       = [aws_sns_topic.alerts.arn]
}

resource "aws_cloudwatch_metric_alarm" "errors" {
  for_each            = local.functions
  alarm_name          = "job-radar-${each.key}-errors"
  alarm_description   = "job-radar-${each.key} 這一小時有執行失敗。看 /aws/lambda/job-radar-${each.key} 的 log。"
  namespace           = "AWS/Lambda"
  metric_name         = "Errors"
  dimensions          = { FunctionName = aws_lambda_function.fn[each.key].function_name }
  statistic           = "Sum"
  period              = 3600
  evaluation_periods  = 1
  threshold           = 0
  comparison_operator = "GreaterThanThreshold"
  treat_missing_data  = "notBreaching"
  alarm_actions       = [aws_sns_topic.alerts.arn]
}

resource "aws_cloudwatch_dashboard" "radar" {
  dashboard_name = "job-radar"
  dashboard_body = jsonencode({
    widgets = [
      {
        type = "metric", x = 0, y = 0, width = 12, height = 6
        properties = {
          title   = "Boards per day", region = var.region, stat = "Sum", period = 86400, view = "timeSeries"
          metrics = [for m in ["BoardsChecked", "BoardsNotFound", "BoardsThrottled", "BoardsFailed"] : ["JobRadar", m]]
        }
      },
      {
        type = "metric", x = 12, y = 0, width = 12, height = 6
        properties = {
          title   = "New jobs / successful runs per day", region = var.region, stat = "Sum", period = 86400, view = "timeSeries"
          metrics = [["JobRadar", "JobsNew"], ["JobRadar", "RunSucceeded"]]
        }
      },
      {
        type = "metric", x = 0, y = 6, width = 12, height = 6
        properties = {
          title   = "Scan duration (s)", region = var.region, stat = "Maximum", period = 86400, view = "timeSeries"
          metrics = [["JobRadar", "DurationSeconds"]]
        }
      },
      {
        type = "log", x = 12, y = 6, width = 12, height = 6
        properties = {
          title = "Latest scan results", region = var.region, view = "table"
          query = "SOURCE '/aws/lambda/job-radar-scan' | filter msg = 'scan finished' | fields @timestamp, ats, status, boards_checked, jobs_new | sort @timestamp desc | limit 20"
        }
      },
    ]
  })
}

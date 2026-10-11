resource "aws_sns_topic" "alerts" {
  name = "job-radar-alerts"
}

resource "aws_sns_topic_subscription" "email" {
  topic_arn = aws_sns_topic.alerts.arn
  protocol  = "email"
  endpoint  = var.alert_email
}

# Heartbeat: the radar failing silently is the worst failure, so alarm when
# no scan succeeded in 24 hours. Missing data counts as breaching.
resource "aws_cloudwatch_metric_alarm" "heartbeat" {
  alarm_name          = "job-radar-no-successful-scan-24h"
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
  alarm_description   = "Many 429s: lower scan.rps in config/radar.yml."
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

# The dead-letter queue: what failed and will not be retried automatically,
# kept for 14 days with what it takes to send it again (radar redrive):
# a judgement with a permanent error, or one that failed every retry (the
# batch function sends those), and an asynchronous Lambda run that failed
# after its retries (Lambda sends those: the original event and the error).
# The batch summary names each one as it goes in; the alarm below says the
# queue is not empty, so nothing waits there unnoticed.
resource "aws_sqs_queue" "dlq" {
  name                      = "job-radar-dlq"
  message_retention_seconds = 14 * 24 * 60 * 60
  sqs_managed_sse_enabled   = true
}

resource "aws_cloudwatch_metric_alarm" "dlq" {
  alarm_name          = "job-radar-dlq-not-empty"
  alarm_description   = "DLQ 裡有重試也救不回來的工作。用 make dlq 看是什麼，修好後用 make redrive 重送。"
  namespace           = "AWS/SQS"
  metric_name         = "ApproximateNumberOfMessagesVisible"
  dimensions          = { QueueName = aws_sqs_queue.dlq.name }
  statistic           = "Maximum"
  period              = 300
  evaluation_periods  = 1
  threshold           = 0
  comparison_operator = "GreaterThanThreshold"
  treat_missing_data  = "notBreaching"
  alarm_actions       = [aws_sns_topic.alerts.arn]
  ok_actions          = [aws_sns_topic.alerts.arn]
}

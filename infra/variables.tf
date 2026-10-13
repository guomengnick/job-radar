variable "region" {
  description = "The account's organization SCP allows only ap-southeast-2. The ATS APIs are in the US, but the scan is bound by the rate limit, not latency. See ADR 6."
  type        = string
  default     = "ap-southeast-2"
}

variable "alert_email" {
  description = "Receives alarm and budget emails (confirm the SNS subscription email once)."
  type        = string
}

variable "telegram_chat_id" {
  description = "Chat that receives the daily digest."
  type        = string
}

variable "telegram_token_param" {
  description = "SSM SecureString holding the bot token. Created by hand so the token never enters Terraform state."
  type        = string
  default     = "/job-radar/telegram-bot-token"
}

variable "lambda_zip" {
  description = "Built by `make lambda`."
  type        = string
  default     = "../build/lambda.zip"
}

variable "monthly_budget_usd" {
  type    = number
  default = 10
}

variable "timezone" {
  type    = string
  default = "Asia/Taipei"
}

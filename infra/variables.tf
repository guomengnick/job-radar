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

variable "anthropic_key_param" {
  description = "SSM SecureString holding the Anthropic API key for 送到雷達. Created by hand, like the bot token."
  type        = string
  default     = "/job-radar/anthropic-api-key"
}

variable "assess_profile_param" {
  description = "SSM SecureString holding the user's profile that 送到雷達 judges pages against. Personal, so never in this repository."
  type        = string
  default     = "/job-radar/assess-profile"
}

variable "clip_secret_param" {
  description = "SSM SecureString holding the shared secret the browser extension sends in x-radar-secret."
  type        = string
  default     = "/job-radar/clip-secret"
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

variable "mail_domain" {
  description = "Domain whose mail SES receives for job-alert ingest (for example nicholaskuo.dev). Empty turns the email resources off."
  type        = string
  default     = ""
}

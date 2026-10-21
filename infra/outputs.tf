output "functions" {
  value = { for k, f in aws_lambda_function.fn : k => f.function_name }
}

output "tables" {
  value = local.tables
}

output "dashboard_url" {
  value = "https://${var.region}.console.aws.amazon.com/cloudwatch/home?region=${var.region}#dashboards/dashboard/job-radar"
}

output "mail_name_servers" {
  description = "Set these as the domain's nameservers at the registrar."
  value       = flatten([for z in aws_route53_zone.mail : z.name_servers])
}

output "mail_bucket" {
  value = [for b in aws_s3_bucket.mail : b.id]
}

output "clip_url" {
  description = "Paste into the 送到雷達 extension's options."
  value       = aws_lambda_function_url.clip.function_url
}

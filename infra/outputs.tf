output "functions" {
  value = { for k, f in aws_lambda_function.fn : k => f.function_name }
}

output "tables" {
  value = local.tables
}

output "dashboard_url" {
  value = "https://${var.region}.console.aws.amazon.com/cloudwatch/home?region=${var.region}#dashboards/dashboard/job-radar"
}

terraform {
  required_version = ">= 1.10"
  required_providers {
    aws = {
      source  = "hashicorp/aws"
      version = "~> 6.0"
    }
  }
  # Remote state in a private S3 bucket; values come from backend.hcl
  # (copy backend.hcl.example). State can contain secrets: never commit it.
  backend "s3" {}
}

provider "aws" {
  region = var.region
  default_tags {
    tags = { Project = "job-radar" }
  }
}

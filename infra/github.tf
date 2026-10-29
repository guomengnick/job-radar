# GitHub Actions signs in to AWS with OIDC: GitHub issues a short-lived token
# for each run, AWS trusts it only for this repository, and no access key is
# stored anywhere. Two roles:
#
# - plan: read-only, for pull requests from this repository, to show what a
#   change would do to the stack (plan.yml);
# - deploy: may change the stack, but only for a job in this repository's
#   "production" environment, which GitHub lets run only from main and only
#   after the user approves it (deploy.yml).
#
# The first apply has to be local (make apply), since these roles do not
# exist yet; after that, deploys can go through GitHub. See DEPLOY.zh-TW.md.

# The repository's token subject starts with owner@owner_id/name@repo_id
# (GitHub's immutable subject, on for this repository; see
# GET /repos/{owner}/{repo}/actions/oidc/customization/sub). The IDs stay
# the same through a rename, and a repository deleted and created again
# under the same name gets a new ID, so it cannot sign in as this one.
variable "github_subject" {
  description = "Prefix of the OIDC token subject of the repository whose workflows may sign in."
  type        = string
  default     = "repo:guomengnick@44256664/job-radar@1414345206"
}

resource "aws_iam_openid_connect_provider" "github" {
  url            = "https://token.actions.githubusercontent.com"
  client_id_list = ["sts.amazonaws.com"]
}

data "aws_iam_policy_document" "github_trust" {
  for_each = {
    # Pull requests, and the plan job of a deploy (run from main).
    plan   = ["${var.github_subject}:pull_request", "${var.github_subject}:ref:refs/heads/main"]
    deploy = ["${var.github_subject}:environment:production"]
  }
  statement {
    actions = ["sts:AssumeRoleWithWebIdentity"]
    principals {
      type        = "Federated"
      identifiers = [aws_iam_openid_connect_provider.github.arn]
    }
    condition {
      test     = "StringEquals"
      variable = "token.actions.githubusercontent.com:aud"
      values   = ["sts.amazonaws.com"]
    }
    condition {
      test     = "StringEquals"
      variable = "token.actions.githubusercontent.com:sub"
      values   = each.value
    }
  }
}

resource "aws_iam_role" "github" {
  for_each             = data.aws_iam_policy_document.github_trust
  name                 = "job-radar-github-${each.key}"
  assume_role_policy   = each.value.json
  max_session_duration = 3600
}

# plan reads everything but the secrets. Terraform only needs the batch
# rules parameter, a plain String; the SecureStrings stay unreadable.
resource "aws_iam_role_policy_attachment" "github_plan_read" {
  role       = aws_iam_role.github["plan"].name
  policy_arn = "arn:aws:iam::aws:policy/ReadOnlyAccess"
}

data "aws_iam_policy_document" "github_plan_no_secrets" {
  statement {
    effect  = "Deny"
    actions = ["ssm:GetParameter", "ssm:GetParameters", "ssm:GetParameterHistory", "ssm:GetParametersByPath"]
    resources = [for p in [var.telegram_token_param, var.anthropic_key_param, var.assess_profile_param, var.clip_secret_param] :
    "arn:aws:ssm:${var.region}:${data.aws_caller_identity.me.account_id}:parameter${p}"]
  }
}

resource "aws_iam_role_policy" "github_plan_no_secrets" {
  name   = "no-secrets"
  role   = aws_iam_role.github["plan"].id
  policy = data.aws_iam_policy_document.github_plan_no_secrets.json
}

# deploy changes everything in the stack. PowerUserAccess covers all of it
# but IAM; IAM is allowed only on job-radar-* roles and this OIDC provider,
# so a run cannot create users, keys or roles for anything else.
resource "aws_iam_role_policy_attachment" "github_deploy_power" {
  role       = aws_iam_role.github["deploy"].name
  policy_arn = "arn:aws:iam::aws:policy/PowerUserAccess"
}

data "aws_iam_policy_document" "github_deploy_iam" {
  statement {
    actions = ["iam:*"]
    resources = [
      "arn:aws:iam::${data.aws_caller_identity.me.account_id}:role/job-radar-*",
      aws_iam_openid_connect_provider.github.arn,
    ]
  }
  statement {
    actions   = ["iam:GetPolicy", "iam:GetPolicyVersion", "iam:ListPolicies"]
    resources = ["arn:aws:iam::aws:policy/*"]
  }
}

resource "aws_iam_role_policy" "github_deploy_iam" {
  name   = "job-radar-roles-only"
  role   = aws_iam_role.github["deploy"].id
  policy = data.aws_iam_policy_document.github_deploy_iam.json
}

# On-demand tables: pay per request, nothing runs while idle, and no VPC is
# needed (which avoids a NAT gateway). See ADR 2.

resource "aws_dynamodb_table" "boards" {
  name         = "job-radar-boards"
  billing_mode = "PAY_PER_REQUEST"
  hash_key     = "ats"
  range_key    = "slug"

  attribute {
    name = "ats"
    type = "S"
  }
  attribute {
    name = "slug"
    type = "S"
  }
}

resource "aws_dynamodb_table" "jobs" {
  name         = "job-radar-jobs"
  billing_mode = "PAY_PER_REQUEST"
  hash_key     = "key"

  attribute {
    name = "key"
    type = "S"
  }
  attribute {
    name = "pending"
    type = "S"
  }
  attribute {
    name = "first_seen"
    type = "S"
  }
  attribute {
    name = "ats"
    type = "S"
  }

  # Sparse index: only unsent jobs carry "pending", so the digest reads just those.
  global_secondary_index {
    name            = "pending-index"
    projection_type = "ALL"

    key_schema {
      attribute_name = "pending"
      key_type       = "HASH"
    }
    key_schema {
      attribute_name = "first_seen"
      key_type       = "RANGE"
    }
  }

  # Every job by source and first-seen time, whatever its state: the
  # extension lists a day's 104 jobs from it, and the digest the ones it left
  # out. Assessments carry no "ats", so they stay out.
  global_secondary_index {
    name            = "source-index"
    projection_type = "ALL"

    key_schema {
      attribute_name = "ats"
      key_type       = "HASH"
    }
    key_schema {
      attribute_name = "first_seen"
      key_type       = "RANGE"
    }
  }
}

resource "aws_dynamodb_table" "runs" {
  name         = "job-radar-runs"
  billing_mode = "PAY_PER_REQUEST"
  hash_key     = "pk"
  range_key    = "sk"

  attribute {
    name = "pk"
    type = "S"
  }
  attribute {
    name = "sk"
    type = "S"
  }

  ttl {
    attribute_name = "expires_at"
    enabled        = true
  }
}

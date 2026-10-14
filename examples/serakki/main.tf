terraform {
  required_version = "= 1.16.5"
  required_providers {
    neon = {
      source  = "marek-hanzal/neon"
      version = "= 0.17.0-serakki.2"
    }
    bunnynet = {
      source  = "BunnyWay/bunnynet"
      version = "= 0.19.1"
    }
  }
}

provider "neon" {}
provider "bunnynet" {}

variable "environment" {
  type    = string
  default = "dev"
  validation {
    condition     = contains(["dev", "prod"], var.environment)
    error_message = "Choose dev or prod; each environment uses a separate Neon project."
  }
}

variable "function_zip" {
  type        = string
  description = "Absolute path to the prebuilt ZIP with index.mjs at its root. Build it before planning."
}

locals {
  dns_name = var.environment == "dev" ? "api.dev" : "api"
}

data "bunnynet_dns_zone" "serakki" {
  domain = "serakki.dev"
}

resource "neon_project" "app" {
  name                      = "serakki-${var.environment}"
  org_id                    = "org-ancient-grass-37795644"
  region_id                 = "aws-eu-central-1"
  pg_version                = 17
  history_retention_seconds = 21600

  branch {
    name          = var.environment
    database_name = "app"
    role_name     = "app"
  }

  primary_compute {
    autoscaling_limit_min_cu = 0.25
    autoscaling_limit_max_cu = 1
  }

  lifecycle {
    prevent_destroy = true
  }
}

resource "neon_branch_auth" "app" {
  project_id    = neon_project.app.id
  branch_id     = neon_project.app.default_branch_id
  database_name = neon_project.app.database_name
}

resource "neon_function" "api" {
  project_id       = neon_project.app.id
  branch_id        = neon_project.app.default_branch_id
  slug             = "api"
  runtime          = "nodejs24"
  zip_file_path    = var.function_zip
  source_code_hash = filebase64sha256(var.function_zip)
  environment_variables = {
    APP_ENV            = var.environment
    NEON_AUTH_BASE_URL = neon_branch_auth.app.base_url
  }
  deployment_timeout = "10m"
}

resource "neon_custom_branch_domain" "api" {
  project_id  = neon_function.api.project_id
  branch_id   = neon_function.api.branch_id
  domain      = "${local.dns_name}.serakki.dev"
  entity_type = "function"
  entity_id   = neon_function.api.slug
}

resource "bunnynet_dns_record" "api" {
  zone        = data.bunnynet_dns_zone.serakki.id
  name        = local.dns_name
  type        = "CNAME"
  value       = neon_custom_branch_domain.api.cname_target
  ttl         = 300
  accelerated = false

  lifecycle {
    precondition {
      condition     = neon_custom_branch_domain.api.cname_target != ""
      error_message = "Neon did not return a CNAME target for this region."
    }
  }
}

output "api" {
  value = {
    project_id        = neon_project.app.id
    branch_id         = neon_project.app.default_branch_id
    function_id       = neon_function.api.function_id
    deployment_id     = neon_function.api.current_deployment_id
    native_url        = neon_function.api.invocation_url
    custom_url        = "https://${neon_custom_branch_domain.api.domain}"
    cname_target      = neon_custom_branch_domain.api.cname_target
    auth_base_url     = neon_branch_auth.app.base_url
    auth_jwks_url     = neon_branch_auth.app.jwks_url
    activation_status = neon_custom_branch_domain.api.status
  }
}

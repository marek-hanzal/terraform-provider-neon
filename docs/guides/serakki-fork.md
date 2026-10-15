---
page_title: "Serakki fork: local build and integration"
---

# Serakki fork: local build and integration

This fork extends official `neondatabase/terraform-provider-neon` with four resources. It is not published to the Terraform Registry. Its local installation address is `registry.terraform.io/marek-hanzal/neon`, version `0.17.0-serakki.5`. The SDK is pinned to `github.com/kislerdm/neon-sdk-go v0.30.1`; the Go module retains its upstream path.

## Maintained fork branch

`codex/serakki` is the long-lived branch for our maintained provider. Keep the normal sibling checkout at `../terraform-provider-neon`; ongoing fork changes belong on this branch. `main` retains the upstream baseline. Separate heads are retained only while needed by open upstream PRs.

```shell
git clone --branch codex/serakki https://github.com/marek-hanzal/terraform-provider-neon.git ../terraform-provider-neon
```

Terraform providers are installed by source address and version, not by Git branch. Serakki's `infra/provider.json` records `repository`, `branch: "codex/serakki"`, an exact `commit`, `version`, checkout path and six binary digests. The installer obtains source from this branch and builds the pinned commit into the filesystem mirror. Deploy uses the pinned version and checksums; it does not follow branch HEAD automatically.

Update deliberately: review the new branch commit, bump the provider version for implementation changes, build/verify all six packages, then update the commit/version/digests and Terraform lock together. Documentation-only changes may advance the source commit without changing the binary version/digests. A checkout with local changes or a different commit must fail installation/deploy admission rather than silently switching revisions. Preserve existing version packages in the mirror.

## Toolchain and build

From this checkout:

```shell
mise trust
mise install
mise run test
mise run lint
mise exec -- go vet ./...
mise run docs
mise run install
export TF_CLI_CONFIG_FILE="$PWD/build/terraform.rc"
```

`mise.toml` pins Go 1.27.2, Terraform 1.16.5 and golangci-lint 2.14.0. The source still supports the upstream Go 1.24 baseline. Builds use `CGO_ENABLED=0`, readonly modules, `-trimpath`, `-buildvcs=false`, and explicit version/address linker values. The generated CLI config points only `marek-hanzal/neon` at the local filesystem mirror, while other providers continue using their normal registry installation. No global `dev_overrides`, home-directory installation or provider publication is needed.

The supported fork build targets are `darwin_amd64`, `darwin_arm64`, `linux_amd64`, `linux_arm64`, `windows_amd64`, and `windows_arm64`. Run `mise run crossbuild` to populate all six packages under `build/mirror`. Cross-compilation is checked locally; execution is checked on the development host. On Windows, use the WSL build environment and select the Windows target for native Terraform, or run Terraform entirely inside WSL with the Linux target.

Use the same built mirror and source commit for all consumers of this unpublished version. Preserve and verify the binary SHA256 manifest handed off with the build. A changed implementation must get a new local version after handoff; do not overwrite a version already locked by Serakki.

## Prebuilt function archive

Serakki builds its Hono default `fetch` handler with `argc web-build dev`, yielding `.out/web/dev/api/index.mjs`. Build TypeScript and bundle dependencies before Terraform planning. The provider uploads the archive; it does not transpile source or install dependencies.

From the provider checkout, package the already-built bundle deterministically:

```shell
mise exec -- go run ./cmd/function-zip \
  -input ../serakki/.out/web/dev/api/index.mjs \
  -output build/serakki-api.zip
```

This offline build utility stores `index.mjs` at the ZIP root with fixed metadata. It is separate from Terraform; deployment, updates and deletion always go through the resource and its state. Identical bundle bytes produce identical ZIP bytes. A pre-existing ZIP with dependencies/assets is also supported, subject to Neon's upload limits. The provider checks for a root `index.mjs` and limits the compressed archive to 32 MiB. The API performs asynchronous build validation of extracted contents.

Build the ZIP before `terraform plan`. Keep it available and unchanged until apply. `source_code_hash = filebase64sha256(var.function_zip)` makes this explicit; the provider also calculates the hash when omitted and rechecks it against the exact bytes uploaded at apply.

## Declarative example

This is an integration reference. Apply it as the existing Serakki infrastructure owner's work, with the appropriate remote backend/state and environment. Do not create a second state claiming resources that are already managed elsewhere. The example defaults to dev; prod deployment requires a separate explicit decision.

Leave `neon_project.store_password` unset to use its supported default. The live Neon API currently rejects `store_password = "no"` with HTTP 400, even though the provider schema accepts it. Project role passwords are sensitive values in Terraform state; use the existing private state backend.

```terraform
terraform {
  required_version = "= 1.16.5"
  required_providers {
    neon = {
      source  = "marek-hanzal/neon"
      version = "= 0.17.0-serakki.5"
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

# Optional configuration ownership is separate from Auth enablement.
resource "neon_branch_auth_config" "app" {
  project_id       = neon_branch_auth.app.project_id
  branch_id        = neon_branch_auth.app.branch_id
  database_name    = neon_branch_auth.app.database_name
  application_name = "Serakki"
  allow_localhost  = var.environment == "dev"

  magic_link = {
    enabled            = true
    expires_in_minutes = 15
    disable_sign_up    = false # Allow new user registration.
  }

  # This configured set owns all Better Auth trusted redirect origins.
  trusted_domains = var.environment == "dev" ? [
    "https://studio.dev.serakki.dev",
    "https://game.dev.serakki.dev",
    ] : [
    "https://studio.serakki.dev",
    "https://game.serakki.dev",
  ]
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
```

Use a separate state/workspace for each environment. The reference matches Serakki's `serakki-dev` / `serakki-prod` projects, `org-ancient-grass-37795644`, Frankfurt (`aws-eu-central-1`), database/role `app`, branches `dev` / `prod`, function slug `api`, and domains `api.dev.serakki.dev` / `api.serakki.dev`.

The dependency chain is project/database → managed Auth → function → custom domain → Bunny CNAME. `cname_target` is the exact API response, including an empty value when a region has no custom-domain front door. Domain registration does not wait on DNS/TLS, so Bunny can create the CNAME. Follow-up reads expose activation status; public HTTPS must be checked after DNS propagation. Neon Functions use their Node.js 24 default fetch handler, compatible with `export default app` from Hono.

## Schema contract and recovery

| Resource | Identity/import ID | Replacement inputs | Mutable inputs | Key computed outputs |
| --- | --- | --- | --- | --- |
| `neon_function` | `PROJECT/BRANCH/SLUG` | `project_id`, `branch_id`, `slug` | `name`, `runtime`, ZIP/hash, env, timeout | `function_id`, `invocation_url`, `current_deployment_id`, `current_deployment_status`, `active_deployment_id`, `environment_variable_names` |
| `neon_custom_branch_domain` | `PROJECT/BRANCH/DOMAIN` | project/branch/domain/entity binding | none | `cname_target`, `status`, `dns_status`, `binding_status`, `status_reason` |
| `neon_branch_auth` | `PROJECT/BRANCH/DATABASE` | project/branch/database | none | `base_url`, `jwks_url`, `auth_provider`, `auth_provider_project_id`, `owned_by` |

Function code/runtime/env changes deploy in place. The waiter accepts only the requested deployment ID after it is completed and active; it returns errors for failure, supersession, timeout and cancellation. An accepted deployment retains identity on failure so Terraform can recover. API errors are errors rather than warnings. Read/404 removes missing resources from state; other HTTP errors retain state. The function identifier may equal the slug. Read also compares the function creation timestamp as a generation signal, so a changed creation time triggers reconciliation even when function/deployment identifiers repeat. Equivalent timestamps with different offsets or fractional precision are treated as the same instant. The API has no guaranteed generation-unique token, so detection is limited to changes in the returned metadata. Metadata and environment names are read back. Neon never returns env values or remote ZIP hashes, so direct value/byte comparison is unavailable; external identity/deployment changes invalidate managed inputs and cause reconciliation.

`environment_variables` omitted means unmanaged; `{}` removes user variables by sending explicit deletion tombstones. Nonempty configured values stay sensitive in Terraform output but remain present in state. Empty values cannot be used as stored values because the API interprets them as deletion. After import, code/environment remain unmanaged until explicitly configured. A configured local ZIP is deployed in place at the next apply.

The separate `neon_branch_auth_config` manages selected Magic Link, email/password, application name, localhost, redirect origin and custom SMTP settings. See [managed Auth settings](managed-auth-settings.md) for field ownership, API mapping and the validation boundary. Config removal leaves remote settings unchanged.

Auth enablement is managed Better Auth only, one integration per branch. It exports service/JWKS URLs, does not store the API's `secret_server_key`, and does not configure users/OAuth/email. Destroy disables with `delete_data=false`, retaining Auth schema and user data. A foreign Auth provider, unmanaged owner, or integration on a different database returns an error instead of silently taking ownership.

If Serakki state already uses `kislerdm/neon`, back up state and use Terraform's native `state replace-provider` to change its provider address to `marek-hanzal/neon`, then inspect the plan. Do not import/create duplicates. For the unpublished fork, populate platform checksums from the mirror:

```shell
terraform providers lock \
  -fs-mirror=/absolute/path/to/terraform-provider-neon/build/mirror \
  -platform=darwin_amd64 -platform=darwin_arm64 \
  -platform=linux_amd64 -platform=linux_arm64 \
  -platform=windows_amd64 -platform=windows_arm64 \
  registry.terraform.io/marek-hanzal/neon
```

## Validation boundary

Local verification covers Go build/vet/lint, race detection, mock HTTP and actual Terraform plan/apply/import/destroy lifecycles, the example's provider schema, all six platform builds, deterministic rebuild/ZIP output, and invoking the existing bundled Hono handler. It does not establish live Neon or Bunny infrastructure behavior. The Serakki integration chat owns the first dev plan/apply and native/custom `/health`, managed Auth read, DNS/TLS, and in-place code/env update checks. Production apply remains separate.

## API and draft audit

The public Neon OpenAPI snapshot and SDK v0.30.1 support the requested functions, custom branch domains and Auth APIs. The SDK's multipart helper omits an empty environment map and its methods do not accept context; this provider uses a focused multipart adapter and a per-operation context transport, retaining SDK types and JSON methods. No SDK fork or vendored generated client is needed. The old branch-deletion `hardDelete` parameter is no longer in the current API.

The implementation was informed by the function draft `fa8f99b9570861b3843038e4390b2df8d4726b80` and domain draft `b19cbf8cce81f6d51f04ab6c82597ed12a78191a`, without cherry-picking them. It fixes the draft's map conversion, missing code digest, destructive code/env updates, absent deployment wait and incomplete domain read state handling. Tests cover env deletion, import, drift, failed deployments, cancellation, supersession, HTTP error state preservation and cross-database Auth refusal.

References: [Functions issue #29](https://github.com/neondatabase/terraform-provider-neon/issues/29), [domains issue #36](https://github.com/neondatabase/terraform-provider-neon/issues/36), [Auth issue #42](https://github.com/neondatabase/terraform-provider-neon/issues/42), [Functions API](https://neon.com/docs/reference/api/functions), [Auth API](https://neon.com/docs/reference/api/auth), [OpenAPI](https://d216pytvakpmhr.cloudfront.net/api_spec/main/v2.json).

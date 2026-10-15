---
page_title: "Managed Better Auth settings"
---

# Managed Better Auth settings

The fork's `0.17.0-serakki.3` release introduced `neon_branch_auth_config` separately from `neon_branch_auth`. `0.17.0-serakki.4` adds custom SMTP management. The existing resource owns Auth enablement; the new resource owns selected settings. Keeping those lifecycles separate means failed configuration cannot force replacement of an Auth integration. Config removal performs no remote writes, while explicit false/empty values update the configured settings.

```terraform
variable "auth_smtp_password" {
  type      = string
  sensitive = true
}

resource "neon_branch_auth_config" "app" {
  # References order configuration after managed Auth enablement.
  project_id       = neon_branch_auth.app.project_id
  branch_id        = neon_branch_auth.app.branch_id
  database_name    = neon_branch_auth.app.database_name
  application_name = "Example App"
  allow_localhost  = false

  magic_link = {
    enabled            = true
    expires_in_minutes = 15
    disable_sign_up    = false # false permits new user registration.
  }

  email_and_password = {
    enabled                    = true
    email_verification_method  = "otp"
    require_email_verification = true
  }

  email_provider = {
    type         = "standard"
    host         = "smtp.example.com"
    port         = 465
    username     = "smtp-user"
    password     = var.auth_smtp_password
    sender_email = "login@example.com"
    sender_name  = "Example App"
  }

  trusted_domains = ["https://app.example.com", "https://*.preview.example.com"]
}
```

Only configured fields are managed. Nested fields can be independently omitted or released; no provider defaults are imposed. `trusted_domains` is a complete set of Better Auth redirect origins only when configured. Import uses `PROJECT/BRANCH/DATABASE` and leaves settings unmanaged until specified. The captured integration ID prevents accidentally configuring a replacement integration without an explicit import. There is no user/session/tenancy management and no email is sent by the resource.

## Verified API mapping

Checked against the current [official OpenAPI](https://d216pytvakpmhr.cloudfront.net/api_spec/main/v2.json), [managed Auth API guide](https://neon.com/docs/auth/guides/manage-auth-api), [managed plugin subset](https://neon.com/docs/auth/guides/plugins), [Magic Link guide](https://neon.com/docs/auth/guides/plugins/magic-link) and [trusted domain guide](https://neon.com/docs/auth/guides/configure-domains) on 2026-10-15. The [email provider GET](https://api-docs.neon.tech/reference/getneonauthemailprovider) and [PATCH](https://api-docs.neon.tech/reference/updateneonauthemailprovider) contracts were checked against the [release OpenAPI](https://neon.com/api_spec/release/v2.json) on the same date. SDK `github.com/kislerdm/neon-sdk-go v0.30.1` already supports all endpoints used by this release.

All paths below are relative to `/projects/{project_id}/branches/{branch_id}/auth`. The current OpenAPI exposes **GET only** on `/plugins`; updates use individual plugin endpoints. Do not infer a generic PATCH payload from the guide's summary table.

| Terraform setting | Read | Write | Contract |
| --- | --- | --- | --- |
| `magic_link.enabled` | GET `/plugins`, `magic_link.enabled` | PATCH `/plugins/magic-link`, `enabled` | boolean |
| `magic_link.expires_in_minutes` | `magic_link.expires_in` | `expires_in` | integer, 5–1440 **minutes** |
| `magic_link.disable_sign_up` | `magic_link.disable_sign_up` | `disable_sign_up` | false permits new registration |
| `email_and_password` | GET `/email_and_password` | PATCH same path | seven typed fields; omitted PATCH keys are unchanged |
| `email_provider` | GET `/email_provider` | PATCH same path, `type=standard` | host, TCP port 1–65535, username, password, sender email/name; omitted fields are unchanged for Better Auth |
| `application_name` | GET base Auth integration, `name` | PATCH `/config`, `name` | 1–256 Unicode characters; endpoint is documented in the guide but marked `x-internal` in OpenAPI |
| `allow_localhost` | GET `/allow_localhost` | PATCH same path | required boolean JSON field |
| `trusted_domains` | GET `/domains`, filter `auth_provider=better_auth` | POST individual `domain`; DELETE `{auth_provider, domains:[{domain}]}` | full http(s) origins; wildcard hostnames supported |

Email/password fields are `enabled`, `email_verification_method` (`link` or `otp`), `require_email_verification`, `auto_sign_in_after_verification`, `send_verification_email_on_sign_up`, `send_verification_email_on_sign_in`, and `disable_sign_up`. These flags configure future auth behavior; the Terraform resource does not trigger those flows or send emails.

The `/plugins` aggregate can contain OAuth/SMTP credentials depending on permissions. The provider projects only Magic Link settings into state and never persists unrelated plugin data. Missing/null required configuration fields return an error instead of SDK zero values; settings responses are bounded to 1 MiB. The parent Auth integration must be Neon-owned Better Auth on the expected branch/database.

## SMTP credential ownership

`email_provider.type` is required and accepts only `standard`; each SMTP field can be independently omitted or released. Configure all six fields when first replacing the shared provider. Later updates send only changed managed fields, and Better Auth merges omitted fields server-side. Legacy Stack Auth is refused by the existing integration guard. Removing the SMTP object or entire config resource leaves the remote provider and all Auth users intact.

Host, username and password follow OpenAPI's sensitive classification. Configured credentials are retained in Terraform state, so the state backend must protect them. GET can return the actual stored password to credential-reading callers or an empty redaction to ordinary callers; PATCH always redacts it. The provider detects password drift only when GET reveals a nonempty actual password. Empty read-back preserves the known configured password and is never written back. Unmanaged credentials are never captured by refresh/import. API/decode errors suppress response details because they may contain secrets.

If the Console switches the remote provider to `shared`, refresh exposes provider drift and the next corrective apply resends all configured SMTP fields. No SMTP connection or email-send endpoint is called by Terraform. Delivery and live credential acceptance require separate explicit runtime verification.

## Other managed capabilities and follow-up slices

| Capability | Current managed API surface | Scope/status |
| --- | --- | --- |
| Admin, JWT, OpenAPI plugins | Supported managed SDK functionality; no corresponding configuration keys/endpoints in this OpenAPI inventory | No arbitrary upstream Better Auth options are forwarded |
| Email OTP | Email/password `email_verification_method=otp` | Supported by this release; no invented generic plugin switch |
| Organization | GET aggregate plus PATCH `/plugins/organization`; enabled, limits, creator role and invitation-email flag | Mapped only; tenancy excluded |
| Phone Number | GET/PATCH `/plugins/phone-number`; enabled and current OpenAPI `otp_expires_in` (60–600 seconds) | Mapped only; current SDK type lacks the newer expiration field, so a future slice must reconcile that contract |
| Auth as OAuth server | GET/PATCH `/plugins/oauth-provider`, client/resource endpoints | Mapped only; distinct from adding third-party sign-in providers |
| Third-party OAuth credentials | GET/POST `/oauth_providers`, PATCH/DELETE `/oauth_providers/{id}` | Future slice; Google/GitHub/Microsoft/Vercel IDs, client ID/secret and optional Microsoft tenant ID. Omitted update fields preserve current values. `shared` keys are development-only; `standard` uses owned credentials. Secret read-back must be treated as sensitive and must not be assumed absent/redacted |
| Email test endpoints | POST `/send_test_email` and `/email_provider/test` | Not called; live email sends excluded |
| Webhooks, users, sessions, UI | Other API/SDK endpoints | Outside this slice |

## Validation boundary

This release is validated through focused HTTP fixtures and real Terraform plan/apply/refresh/import/destroy against `httptest`, plus full unit/race/vet/lint, generated docs, HCL validation and six platform builds. SMTP fixtures additionally cover sensitive-output rejection, redacted GET/PATCH stability, readable password drift, rotation, credential omission, shared-provider drift, errors without credential leakage and identity-guarded updates. Fixtures cover Console-style drift, explicit false and minute boundaries, field-level omission/release, empty objects/sets, identity guards, failed/partial writes and retry. Config-only removal sends no Auth disable call, and existing integration identity remains unchanged through updates and recovery.

No cloud Auth settings were changed and no emails were sent during this slice. These are mocked lifecycle proofs, not a claim of live settings validation. The existing `.2` Function/Auth/domain live evidence remains separate. OAuth follow-up work needs its own typed credential ownership and read-back tests before implementation.

## Build and integration

Use the maintained `codex/serakki` branch in the normal sibling `../terraform-provider-neon` checkout. Pin the exact commit/version and all six digests from the Auth-settings handoff; coordinate checkout/pin changes with the Serakki owner and preserve older mirror packages. Terraform continues to install the pinned `marek-hanzal/neon` binary from the local mirror, while the Git branch identifies the source development line. From that clean pinned checkout:

```shell
mise trust
mise install
mise run crossbuild
neon_provider_checkout="$PWD"
export TF_CLI_CONFIG_FILE="$neon_provider_checkout/build/terraform.rc"
cd /absolute/path/to/consuming/terraform/module
terraform init -upgrade
terraform providers lock -fs-mirror="$neon_provider_checkout/build/mirror" \
  -platform=darwin_amd64 -platform=darwin_arm64 \
  -platform=linux_amd64 -platform=linux_arm64 \
  -platform=windows_amd64 -platform=windows_arm64 \
  registry.terraform.io/marek-hanzal/neon
```

Run Terraform commands in the consuming module, with its existing backend/state and the new checkout's absolute mirror path. Pin `marek-hanzal/neon = 0.17.0-serakki.4`, the exact source commit and all binary digests together. Inspect the plan before a future live apply; updating the provider pin alone should not change existing resources. Add one config resource with references to the already-managed Auth integration rather than creating another Auth resource. No migration or `state replace-provider` is required when retaining the current provider address.

resource "neon_branch_auth_webhook" "mail" {
  project_id      = neon_branch_auth.app.project_id
  branch_id       = neon_branch_auth.app.branch_id
  database_name   = neon_branch_auth.app.database_name
  enabled         = true
  webhook_url     = "${neon_function.api.invocation_url}/api/auth/email"
  enabled_events  = ["send.magic_link"]
  timeout_seconds = 5
}

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

  trusted_domains = ["https://app.example.com", "https://*.preview.example.com"]
}

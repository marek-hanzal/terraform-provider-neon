package provider

import (
	"context"
	"fmt"
	"net/url"
	"sort"
	"unicode/utf8"

	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
	neon "github.com/kislerdm/neon-sdk-go"
)

type authMagicLinkModel struct {
	Enabled          types.Bool  `tfsdk:"enabled"`
	ExpiresInMinutes types.Int64 `tfsdk:"expires_in_minutes"`
	DisableSignUp    types.Bool  `tfsdk:"disable_sign_up"`
}

type authEmailPasswordModel struct {
	Enabled                       types.Bool   `tfsdk:"enabled"`
	EmailVerificationMethod       types.String `tfsdk:"email_verification_method"`
	RequireEmailVerification      types.Bool   `tfsdk:"require_email_verification"`
	AutoSignInAfterVerification   types.Bool   `tfsdk:"auto_sign_in_after_verification"`
	SendVerificationEmailOnSignUp types.Bool   `tfsdk:"send_verification_email_on_sign_up"`
	SendVerificationEmailOnSignIn types.Bool   `tfsdk:"send_verification_email_on_sign_in"`
	DisableSignUp                 types.Bool   `tfsdk:"disable_sign_up"`
}

func authSettingsSchema() map[string]schema.Attribute {
	boolean := func(description string) schema.BoolAttribute {
		return schema.BoolAttribute{Optional: true, Description: description + " Omit to leave unmanaged."}
	}
	return map[string]schema.Attribute{
		"application_name": schema.StringAttribute{Optional: true, Description: "Application name shown in Auth emails, 1–256 characters. Omit to leave unmanaged.", Validators: []validator.String{authSettingsStringValidator{"application_name"}}},
		"allow_localhost":  boolean("Allow authentication from localhost origins."),
		"trusted_domains":  schema.SetAttribute{Optional: true, ElementType: types.StringType, Description: "Authoritative set of Better Auth redirect origins, including http(s) scheme and no trailing slash. Wildcard hostnames are supported. Omit to leave unmanaged; an empty set removes all Better Auth entries. Other auth providers are untouched.", Validators: []validator.Set{authTrustedDomainsValidator{}}},
		"magic_link": schema.SingleNestedAttribute{Optional: true, Description: "Managed Magic Link settings. Each omitted field is unmanaged; no defaults are imposed.", Attributes: map[string]schema.Attribute{
			"enabled":            boolean("Enable passwordless email magic links."),
			"expires_in_minutes": schema.Int64Attribute{Optional: true, Description: "Link expiration in minutes, from 5 to 1440. Omit to leave unmanaged.", Validators: []validator.Int64{authMagicLinkExpiryValidator{}}},
			"disable_sign_up":    boolean("Disable new user registration through magic links. false allows registration."),
		}},
		"email_and_password": schema.SingleNestedAttribute{Optional: true, Description: "Managed email/password settings. Each omitted field is unmanaged.", Attributes: map[string]schema.Attribute{
			"enabled":                            boolean("Enable email/password authentication."),
			"email_verification_method":          schema.StringAttribute{Optional: true, Description: "Email verification method: link or otp. Omit to leave unmanaged.", Validators: []validator.String{authSettingsStringValidator{"email_verification_method"}}},
			"require_email_verification":         boolean("Require email verification before sign-in."),
			"auto_sign_in_after_verification":    boolean("Sign users in automatically after email verification."),
			"send_verification_email_on_sign_up": boolean("Send verification email on sign-up."),
			"send_verification_email_on_sign_in": boolean("Send verification email when an unverified user signs in."),
			"disable_sign_up":                    boolean("Disable new user registration through email/password."),
		}},
	}
}

type authSettingsStringValidator struct{ kind string }

func (v authSettingsStringValidator) Description(context.Context) string {
	return "validates " + v.kind
}
func (v authSettingsStringValidator) MarkdownDescription(ctx context.Context) string {
	return v.Description(ctx)
}
func (v authSettingsStringValidator) ValidateString(_ context.Context, req validator.StringRequest, resp *validator.StringResponse) {
	if req.ConfigValue.IsNull() || req.ConfigValue.IsUnknown() {
		return
	}
	s := req.ConfigValue.ValueString()
	valid := false
	switch v.kind {
	case "application_name":
		valid = utf8.RuneCountInString(s) >= 1 && utf8.RuneCountInString(s) <= 256
	case "email_verification_method":
		valid = s == "link" || s == "otp"
	}
	if !valid {
		resp.Diagnostics.AddAttributeError(req.Path, "Invalid Auth Setting", "Invalid "+v.kind+".")
	}
}

type authMagicLinkExpiryValidator struct{}

func (authMagicLinkExpiryValidator) Description(context.Context) string {
	return "Magic Link expiration must be 5–1440 minutes"
}
func (v authMagicLinkExpiryValidator) MarkdownDescription(ctx context.Context) string {
	return v.Description(ctx)
}
func (v authMagicLinkExpiryValidator) ValidateInt64(ctx context.Context, req validator.Int64Request, resp *validator.Int64Response) {
	if !req.ConfigValue.IsNull() && !req.ConfigValue.IsUnknown() && (req.ConfigValue.ValueInt64() < 5 || req.ConfigValue.ValueInt64() > 1440) {
		resp.Diagnostics.AddAttributeError(req.Path, "Invalid Magic Link Expiration", v.Description(ctx))
	}
}

type authTrustedDomainsValidator struct{}

func (authTrustedDomainsValidator) Description(context.Context) string {
	return "Trusted domains must be http(s) origins without credentials, paths, queries, fragments or trailing slashes"
}
func (v authTrustedDomainsValidator) MarkdownDescription(ctx context.Context) string {
	return v.Description(ctx)
}
func (v authTrustedDomainsValidator) ValidateSet(ctx context.Context, req validator.SetRequest, resp *validator.SetResponse) {
	if req.ConfigValue.IsNull() || req.ConfigValue.IsUnknown() {
		return
	}
	for _, element := range req.ConfigValue.Elements() {
		s := element.(types.String)
		if s.IsUnknown() {
			continue
		}
		u, err := url.Parse(s.ValueString())
		if s.IsNull() || err != nil || u.Hostname() == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.Path != "" || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" {
			resp.Diagnostics.AddAttributeError(req.Path, "Invalid Auth Trusted Domain", v.Description(ctx))
		}
	}
}

func authManagedBool(previous, actual types.Bool) types.Bool {
	if previous.IsNull() {
		return previous
	}
	return actual
}

// Only project remote values onto explicitly managed attributes. Null continues
// to mean unmanaged, including individual fields within a configured object.
func (r *branchAuthConfigResource) readSettings(ctx context.Context, model *branchAuthConfigModel, auth neon.NeonAuthIntegration) error {
	client, closeBodies, err := r.client.scopedAuthSettingsSDK(ctx)
	defer closeBodies()
	if err != nil {
		return err
	}
	project, branch := url.PathEscape(model.ProjectID.ValueString()), url.PathEscape(model.BranchID.ValueString())
	if !model.ApplicationName.IsNull() {
		if auth.Name == nil {
			return fmt.Errorf("auth response has no application name for managed application_name")
		}
		model.ApplicationName = types.StringValue(*auth.Name)
	}
	if model.MagicLink != nil && (!model.MagicLink.Enabled.IsNull() || !model.MagicLink.DisableSignUp.IsNull() || !model.MagicLink.ExpiresInMinutes.IsNull()) {
		plugins, err := client.GetNeonAuthPluginConfigs(project, branch)
		if err != nil {
			return fmt.Errorf("read Magic Link settings: %w", err)
		}
		if plugins.MagicLink == nil {
			return fmt.Errorf("auth plugins response has no magic_link configuration")
		}
		remote, target := plugins.MagicLink, model.MagicLink
		if remote.ExpiresIn < 5 || remote.ExpiresIn > 1440 {
			return fmt.Errorf("auth plugins response has an invalid Magic Link expiration")
		}
		target.Enabled = authManagedBool(target.Enabled, types.BoolValue(remote.Enabled))
		target.DisableSignUp = authManagedBool(target.DisableSignUp, types.BoolValue(remote.DisableSignUp))
		if !target.ExpiresInMinutes.IsNull() {
			target.ExpiresInMinutes = types.Int64Value(int64(remote.ExpiresIn))
		}
	}
	if model.EmailAndPassword != nil && (!model.EmailAndPassword.Enabled.IsNull() || !model.EmailAndPassword.DisableSignUp.IsNull() || !model.EmailAndPassword.RequireEmailVerification.IsNull() || !model.EmailAndPassword.AutoSignInAfterVerification.IsNull() || !model.EmailAndPassword.SendVerificationEmailOnSignUp.IsNull() || !model.EmailAndPassword.SendVerificationEmailOnSignIn.IsNull() || !model.EmailAndPassword.EmailVerificationMethod.IsNull()) {
		remote, err := client.GetNeonAuthEmailAndPasswordConfig(project, branch)
		if err != nil {
			return fmt.Errorf("read email/password settings: %w", err)
		}
		target := model.EmailAndPassword
		target.Enabled = authManagedBool(target.Enabled, types.BoolValue(remote.Enabled))
		target.DisableSignUp = authManagedBool(target.DisableSignUp, types.BoolValue(remote.DisableSignUp))
		target.RequireEmailVerification = authManagedBool(target.RequireEmailVerification, types.BoolValue(remote.RequireEmailVerification))
		target.AutoSignInAfterVerification = authManagedBool(target.AutoSignInAfterVerification, types.BoolValue(remote.AutoSignInAfterVerification))
		target.SendVerificationEmailOnSignUp = authManagedBool(target.SendVerificationEmailOnSignUp, types.BoolValue(remote.SendVerificationEmailOnSignUp))
		target.SendVerificationEmailOnSignIn = authManagedBool(target.SendVerificationEmailOnSignIn, types.BoolValue(remote.SendVerificationEmailOnSignIn))
		if !target.EmailVerificationMethod.IsNull() {
			if remote.EmailVerificationMethod.String() == "" {
				return fmt.Errorf("auth response has no email verification method")
			}
			target.EmailVerificationMethod = types.StringValue(remote.EmailVerificationMethod.String())
		}
	}
	if !model.AllowLocalhost.IsNull() {
		remote, err := client.GetNeonAuthAllowLocalhost(project, branch)
		if err != nil {
			return fmt.Errorf("read localhost setting: %w", err)
		}
		model.AllowLocalhost = types.BoolValue(remote.AllowLocalhost)
	}
	if !model.TrustedDomains.IsNull() {
		domains, err := client.ListBranchNeonAuthTrustedDomains(project, branch)
		if err != nil {
			return fmt.Errorf("read trusted domains: %w", err)
		}
		values := []string{}
		for _, domain := range domains.Domains {
			if domain.AuthProvider == neon.NeonAuthSupportedAuthProviderBetterAuth {
				values = append(values, domain.Domain)
			}
		}
		actual, diagnostics := types.SetValueFrom(ctx, types.StringType, values)
		if diagnostics.HasError() {
			return fmt.Errorf("invalid trusted domains response: %v", diagnostics)
		}
		model.TrustedDomains = actual
	}
	return nil
}

func authBoolPatch(desired, previous types.Bool) *bool {
	if desired.IsNull() || desired.Equal(previous) {
		return nil
	}
	return desired.ValueBoolPointer()
}

func (r *branchAuthConfigResource) applySettings(ctx context.Context, previous, plan branchAuthConfigModel) error {
	client, closeBodies, err := r.client.scopedAuthSettingsSDK(ctx)
	defer closeBodies()
	if err != nil {
		return err
	}
	project, branch := url.PathEscape(plan.ProjectID.ValueString()), url.PathEscape(plan.BranchID.ValueString())
	if plan.MagicLink != nil {
		old := authMagicLinkModel{}
		if previous.MagicLink != nil {
			old = *previous.MagicLink
		}
		v := plan.MagicLink
		patch := neon.NeonAuthMagicLinkConfigUpdate{Enabled: authBoolPatch(v.Enabled, old.Enabled), DisableSignUp: authBoolPatch(v.DisableSignUp, old.DisableSignUp)}
		if !v.ExpiresInMinutes.IsNull() && !v.ExpiresInMinutes.Equal(old.ExpiresInMinutes) {
			n := int32(v.ExpiresInMinutes.ValueInt64())
			patch.ExpiresIn = &n
		}
		if patch.Enabled != nil || patch.DisableSignUp != nil || patch.ExpiresIn != nil {
			if _, err := client.UpdateNeonAuthMagicLinkPlugin(project, branch, patch); err != nil {
				return fmt.Errorf("update Magic Link settings: %w", err)
			}
		}
	}
	if plan.EmailAndPassword != nil {
		old := authEmailPasswordModel{}
		if previous.EmailAndPassword != nil {
			old = *previous.EmailAndPassword
		}
		v := plan.EmailAndPassword
		patch := neon.NeonAuthEmailAndPasswordConfigUpdate{
			Enabled: authBoolPatch(v.Enabled, old.Enabled), DisableSignUp: authBoolPatch(v.DisableSignUp, old.DisableSignUp),
			RequireEmailVerification:      authBoolPatch(v.RequireEmailVerification, old.RequireEmailVerification),
			AutoSignInAfterVerification:   authBoolPatch(v.AutoSignInAfterVerification, old.AutoSignInAfterVerification),
			SendVerificationEmailOnSignUp: authBoolPatch(v.SendVerificationEmailOnSignUp, old.SendVerificationEmailOnSignUp),
			SendVerificationEmailOnSignIn: authBoolPatch(v.SendVerificationEmailOnSignIn, old.SendVerificationEmailOnSignIn),
		}
		if !v.EmailVerificationMethod.IsNull() && !v.EmailVerificationMethod.Equal(old.EmailVerificationMethod) {
			method, err := neon.NewNeonAuthEmailVerificationMethod(v.EmailVerificationMethod.ValueString())
			if err != nil {
				return err
			}
			patch.EmailVerificationMethod = &method
		}
		if patch.Enabled != nil || patch.DisableSignUp != nil || patch.RequireEmailVerification != nil || patch.AutoSignInAfterVerification != nil || patch.SendVerificationEmailOnSignUp != nil || patch.SendVerificationEmailOnSignIn != nil || patch.EmailVerificationMethod != nil {
			if _, err := client.UpdateNeonAuthEmailAndPasswordConfig(project, branch, patch); err != nil {
				return fmt.Errorf("update email/password settings: %w", err)
			}
		}
	}
	if !plan.ApplicationName.IsNull() && !plan.ApplicationName.Equal(previous.ApplicationName) {
		if _, err := client.UpdateNeonAuthConfig(project, branch, neon.NeonAuthConfigUpdate{Name: plan.ApplicationName.ValueString()}); err != nil {
			return fmt.Errorf("update application name: %w", err)
		}
	}
	if !plan.AllowLocalhost.IsNull() && !plan.AllowLocalhost.Equal(previous.AllowLocalhost) {
		if _, err := client.UpdateNeonAuthAllowLocalhost(project, branch, neon.UpdateNeonAuthAllowLocalhostRequest{AllowLocalhost: plan.AllowLocalhost.ValueBool()}); err != nil {
			return fmt.Errorf("update localhost setting: %w", err)
		}
	}
	if !plan.TrustedDomains.IsNull() && !plan.TrustedDomains.Equal(previous.TrustedDomains) {
		if err := reconcileAuthDomains(ctx, client, project, branch, plan.TrustedDomains); err != nil {
			return err
		}
	}
	return nil
}

func reconcileAuthDomains(ctx context.Context, client *neon.Client, project, branch string, desired types.Set) error {
	var values []string
	if diags := desired.ElementsAs(ctx, &values, false); diags.HasError() {
		return fmt.Errorf("invalid trusted domains: %v", diags)
	}
	domains, err := client.ListBranchNeonAuthTrustedDomains(project, branch)
	if err != nil {
		return fmt.Errorf("read trusted domains before update: %w", err)
	}
	current, wanted := map[string]bool{}, map[string]bool{}
	for _, domain := range domains.Domains {
		if domain.AuthProvider == neon.NeonAuthSupportedAuthProviderBetterAuth {
			current[domain.Domain] = true
		}
	}
	sort.Strings(values)
	// Add before removing so an addition failure does not revoke current redirects.
	for _, domain := range values {
		wanted[domain] = true
		if !current[domain] {
			if err := client.AddBranchNeonAuthTrustedDomain(project, branch, neon.NeonAuthAddDomainToRedirectURIWhitelistRequest{Domain: domain, AuthProvider: neon.NeonAuthSupportedAuthProviderBetterAuth}); err != nil {
				return fmt.Errorf("add trusted domain: %w", err)
			}
		}
	}
	removed := []string{}
	for domain := range current {
		if !wanted[domain] {
			removed = append(removed, domain)
		}
	}
	sort.Strings(removed)
	if len(removed) != 0 {
		items := make([]neon.NeonAuthDeleteDomainFromRedirectURIWhitelistItem, len(removed))
		for i, domain := range removed {
			items[i].Domain = domain
		}
		if err := client.DeleteBranchNeonAuthTrustedDomain(project, branch, neon.NeonAuthDeleteDomainFromRedirectURIWhitelistRequest{AuthProvider: neon.NeonAuthSupportedAuthProviderBetterAuth, Domains: items}); err != nil {
			return fmt.Errorf("remove trusted domains: %w", err)
		}
	}
	return nil
}

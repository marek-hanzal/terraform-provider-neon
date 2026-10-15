package provider

import (
	"context"
	"errors"
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
	neon "github.com/kislerdm/neon-sdk-go"
)

type authEmailProviderModel struct {
	Type        types.String `tfsdk:"type"`
	Host        types.String `tfsdk:"host"`
	Port        types.Int64  `tfsdk:"port"`
	Username    types.String `tfsdk:"username"`
	Password    types.String `tfsdk:"password"`
	SenderEmail types.String `tfsdk:"sender_email"`
	SenderName  types.String `tfsdk:"sender_name"`
}

func authEmailProviderSchema() schema.SingleNestedAttribute {
	return schema.SingleNestedAttribute{Optional: true, Description: "Custom SMTP provider for Auth emails. Set type to standard; omitted fields are unmanaged and preserve stored values. Removing this object leaves the remote provider unchanged. No email is sent.", Attributes: map[string]schema.Attribute{
		"type":         schema.StringAttribute{Required: true, Description: "Custom SMTP provider type: standard.", Validators: []validator.String{authSettingsStringValidator{"email_provider.type"}}},
		"host":         schema.StringAttribute{Optional: true, Sensitive: true, Description: "SMTP server hostname. Omit to leave unmanaged."},
		"port":         schema.Int64Attribute{Optional: true, Description: "SMTP TCP port, 1–65535. Common values: 465 or 587. Omit to leave unmanaged.", Validators: []validator.Int64{authSMTPPortValidator{}}},
		"username":     schema.StringAttribute{Optional: true, Sensitive: true, Description: "SMTP username. Omit to leave unmanaged."},
		"password":     schema.StringAttribute{Optional: true, Sensitive: true, Description: "SMTP password. Stored in Terraform state when configured. GET is not a reliable password echo; refresh retains the last configured value and cannot detect password drift. Omit to leave unmanaged."},
		"sender_email": schema.StringAttribute{Optional: true, Description: "From address for Auth emails. Omit to leave unmanaged."},
		"sender_name":  schema.StringAttribute{Optional: true, Description: "Sender display name for Auth emails. Omit to leave unmanaged."},
	}}
}

type authSMTPPortValidator struct{}

func (authSMTPPortValidator) Description(context.Context) string {
	return "SMTP TCP port must be 1–65535"
}
func (v authSMTPPortValidator) MarkdownDescription(ctx context.Context) string {
	return v.Description(ctx)
}
func (v authSMTPPortValidator) ValidateInt64(ctx context.Context, req validator.Int64Request, resp *validator.Int64Response) {
	if !req.ConfigValue.IsNull() && !req.ConfigValue.IsUnknown() && (req.ConfigValue.ValueInt64() < 1 || req.ConfigValue.ValueInt64() > 65535) {
		resp.Diagnostics.AddAttributeError(req.Path, "Invalid SMTP Port", v.Description(ctx))
	}
}

// SMTP responses can contain secrets even on errors. Never include SDK error
// bodies or decoded values in Terraform diagnostics.
func authEmailProviderError(operation string, err error) error {
	var apiErr neon.Error
	if errors.As(err, &apiErr) {
		return fmt.Errorf("%s email provider: Neon returned HTTP %d", operation, apiErr.HTTPCode)
	}
	if errors.Is(err, context.Canceled) {
		return fmt.Errorf("%s email provider: %w", operation, context.Canceled)
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return fmt.Errorf("%s email provider: %w", operation, context.DeadlineExceeded)
	}
	return fmt.Errorf("%s email provider: request or response failed; response details suppressed because they may contain SMTP credentials", operation)
}

func authManagedString(previous types.String, actual string) types.String {
	if previous.IsNull() {
		return previous
	}
	return types.StringValue(actual)
}

func readAuthEmailProvider(client *neon.Client, project, branch string, target *authEmailProviderModel) error {
	remote, err := client.GetNeonAuthEmailProvider(project, branch)
	if err != nil {
		return authEmailProviderError("read", err)
	}
	target.Type = types.StringValue(remote.Type)
	if remote.Type == "shared" {
		// The remote custom provider was removed. Null fields expose that drift;
		// a subsequent standard update must resend all configured credentials.
		target.Host, target.Username, target.Password = types.StringNull(), types.StringNull(), types.StringNull()
		target.Port = types.Int64Null()
		target.SenderEmail = authManagedString(target.SenderEmail, valueOrEmpty(remote.SharedEmailServer.SenderEmail))
		target.SenderName = authManagedString(target.SenderName, valueOrEmpty(remote.SharedEmailServer.SenderName))
		return nil
	}
	v := remote.StandardEmailServerResponse
	target.Host = authManagedString(target.Host, v.Host)
	target.Username = authManagedString(target.Username, v.Username)
	target.SenderEmail = authManagedString(target.SenderEmail, v.SenderEmail)
	target.SenderName = authManagedString(target.SenderName, v.SenderName)
	if !target.Port.IsNull() {
		target.Port = types.Int64Value(int64(v.Port))
	}
	// GET can return an opaque/masked password without an authoritative echo
	// discriminator. Preserve managed state; never turn a response into a credential.
	return nil
}

func valueOrEmpty(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func applyAuthEmailProvider(client *neon.Client, project, branch string, previous, desired *authEmailProviderModel) error {
	old := authEmailProviderModel{}
	if previous != nil {
		old = *previous
	}
	typeChanged := !desired.Type.Equal(old.Type)
	patchString := func(value, before types.String) *string {
		if value.IsNull() || (!typeChanged && value.Equal(before)) {
			return nil
		}
		return value.ValueStringPointer()
	}
	patch := neon.StandardEmailServer{
		Host: patchString(desired.Host, old.Host), Username: patchString(desired.Username, old.Username),
		Password: patchString(desired.Password, old.Password), SenderEmail: patchString(desired.SenderEmail, old.SenderEmail),
		SenderName: patchString(desired.SenderName, old.SenderName),
	}
	if !desired.Port.IsNull() && (typeChanged || !desired.Port.Equal(old.Port)) {
		port := int(desired.Port.ValueInt64())
		patch.Port = &port
	}
	if !typeChanged && patch.Host == nil && patch.Port == nil && patch.Username == nil && patch.Password == nil && patch.SenderEmail == nil && patch.SenderName == nil {
		return nil
	}
	_, err := client.UpdateNeonAuthEmailProvider(project, branch, neon.NeonAuthEmailServerConfig{Type: desired.Type.ValueString(), StandardEmailServer: patch})
	if err != nil {
		return authEmailProviderError("update", err)
	}
	return nil
}

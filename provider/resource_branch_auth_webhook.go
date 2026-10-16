package provider

import (
	"context"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
	neon "github.com/kislerdm/neon-sdk-go"
)

var _ resource.ResourceWithConfigure = (*branchAuthWebhookResource)(nil)
var _ resource.ResourceWithImportState = (*branchAuthWebhookResource)(nil)

type branchAuthWebhookResource struct{ client *neonClient }
type branchAuthWebhookModel struct {
	ID                    types.String `tfsdk:"id"`
	ProjectID             types.String `tfsdk:"project_id"`
	BranchID              types.String `tfsdk:"branch_id"`
	DatabaseName          types.String `tfsdk:"database_name"`
	AuthProviderProjectID types.String `tfsdk:"auth_provider_project_id"`
	Enabled               types.Bool   `tfsdk:"enabled"`
	WebhookURL            types.String `tfsdk:"webhook_url"`
	EnabledEvents         types.Set    `tfsdk:"enabled_events"`
	TimeoutSeconds        types.Int64  `tfsdk:"timeout_seconds"`
}

func NewBranchAuthWebhookResource() resource.Resource { return &branchAuthWebhookResource{} }
func (r *branchAuthWebhookResource) Metadata(_ context.Context, _ resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = "neon_branch_auth_webhook"
}
func (r *branchAuthWebhookResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.client = configureBackend(req, resp)
}
func (r *branchAuthWebhookResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{Description: "Owns one existing managed Auth integration's webhook configuration. Deploy its handler first. Destroy disables delivery before dependent infrastructure is removed; accounts and sessions remain intact. Manage exactly one resource per branch.", Attributes: map[string]schema.Attribute{
		"id":                       schema.StringAttribute{Computed: true, Description: "Webhook identity: PROJECT_ID/BRANCH_ID/DATABASE_NAME."},
		"project_id":               backendIdentityAttribute("Neon project ID."),
		"branch_id":                backendIdentityAttribute("Neon branch ID."),
		"database_name":            backendIdentityAttribute("Database of the existing managed Better Auth integration."),
		"auth_provider_project_id": schema.StringAttribute{Computed: true, Description: "Integration identity; refuses to configure a replacement integration."},
		"enabled":                  schema.BoolAttribute{Required: true, Description: "Enable webhook delivery. Disabling restores built-in delivery for subscribed mail events."},
		"webhook_url":              schema.StringAttribute{Required: true, Description: "Public HTTPS handler URL. No credentials or fragments; Neon enforces public DNS and rejects redirects.", Validators: []validator.String{authWebhookURLValidator{}}},
		"enabled_events":           schema.SetAttribute{Required: true, ElementType: types.StringType, Description: "Authoritative set of subscribed Neon webhook event names."},
		"timeout_seconds":          schema.Int64Attribute{Required: true, Description: "Per-attempt timeout, 1–10 seconds. Neon caps all three attempts at 15 seconds.", Validators: []validator.Int64{authWebhookTimeoutValidator{}}},
	}}
}

type authWebhookURLValidator struct{}

func (authWebhookURLValidator) Description(context.Context) string {
	return "Use a public HTTPS URL without credentials or fragments"
}
func (v authWebhookURLValidator) MarkdownDescription(ctx context.Context) string {
	return v.Description(ctx)
}
func (v authWebhookURLValidator) ValidateString(ctx context.Context, req validator.StringRequest, resp *validator.StringResponse) {
	if req.ConfigValue.IsNull() || req.ConfigValue.IsUnknown() {
		return
	}
	u, err := url.Parse(req.ConfigValue.ValueString())
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.Fragment != "" {
		resp.Diagnostics.AddAttributeError(req.Path, "Invalid Webhook URL", v.Description(ctx))
	}
}

type authWebhookTimeoutValidator struct{}

func (authWebhookTimeoutValidator) Description(context.Context) string {
	return "Webhook timeout must be 1–10 seconds"
}
func (v authWebhookTimeoutValidator) MarkdownDescription(ctx context.Context) string {
	return v.Description(ctx)
}
func (v authWebhookTimeoutValidator) ValidateInt64(ctx context.Context, req validator.Int64Request, resp *validator.Int64Response) {
	if !req.ConfigValue.IsNull() && !req.ConfigValue.IsUnknown() && (req.ConfigValue.ValueInt64() < 1 || req.ConfigValue.ValueInt64() > 10) {
		resp.Diagnostics.AddAttributeError(req.Path, "Invalid Webhook Timeout", v.Description(ctx))
	}
}

func (r *branchAuthWebhookResource) integration(ctx context.Context, model branchAuthWebhookModel) (neon.NeonAuthIntegration, error) {
	return (&branchAuthConfigResource{client: r.client}).get(ctx, branchAuthConfigModel{ProjectID: model.ProjectID, BranchID: model.BranchID, DatabaseName: model.DatabaseName, AuthProviderProjectID: model.AuthProviderProjectID})
}
func (r *branchAuthWebhookResource) write(ctx context.Context, model branchAuthWebhookModel, disable bool) error {
	sdk, stop, err := r.client.scopedAuthSettingsSDK(ctx)
	if err != nil {
		return err
	}
	defer stop()
	config := neon.NeonAuthWebhookConfig{Enabled: false}
	if !disable {
		config.Enabled = model.Enabled.ValueBool()
		config.WebhookURL = model.WebhookURL.ValueStringPointer()
		timeout := uint8(model.TimeoutSeconds.ValueInt64())
		config.TimeoutSeconds = &timeout
		var names []string
		if diagnostics := model.EnabledEvents.ElementsAs(ctx, &names, false); diagnostics.HasError() {
			return fmt.Errorf("invalid webhook events")
		}
		for _, name := range names {
			event, err := neon.NewNeonAuthWebhookConfigEnabledEventsItem(name)
			if err != nil {
				return fmt.Errorf("invalid webhook event %q", name)
			}
			config.EnabledEvents = append(config.EnabledEvents, event)
		}
		if len(config.EnabledEvents) == 0 {
			return fmt.Errorf("at least one webhook event is required")
		}
	}
	_, err = sdk.UpdateNeonAuthWebhookConfig(model.ProjectID.ValueString(), model.BranchID.ValueString(), config)
	return err
}
func (r *branchAuthWebhookResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan branchAuthWebhookModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	auth, err := r.integration(ctx, plan)
	if err != nil {
		resp.Diagnostics.AddError("Unable to Read Webhook Integration", err.Error())
		return
	}
	plan.ID = types.StringValue(strings.Join([]string{plan.ProjectID.ValueString(), plan.BranchID.ValueString(), plan.DatabaseName.ValueString()}, "/"))
	plan.AuthProviderProjectID = types.StringValue(auth.AuthProviderProjectID)
	// Retain identity if PUT partially succeeds; replacement Delete can disable safely.
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
	if err := r.write(ctx, plan, false); err != nil {
		resp.Diagnostics.AddError("Unable to Configure Auth Webhook", err.Error())
	}
}
func (r *branchAuthWebhookResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state branchAuthWebhookModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	auth, err := r.integration(ctx, state)
	if apiNotFound(err) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		resp.Diagnostics.AddError("Unable to Read Webhook Integration", err.Error())
		return
	}
	sdk, stop, err := r.client.scopedAuthSettingsSDK(ctx)
	if err != nil {
		resp.Diagnostics.AddError("Unable to Read Auth Webhook", err.Error())
		return
	}
	defer stop()
	config, err := sdk.GetNeonAuthWebhookConfig(state.ProjectID.ValueString(), state.BranchID.ValueString())
	if err != nil {
		resp.Diagnostics.AddError("Unable to Read Auth Webhook", err.Error())
		return
	}
	state.AuthProviderProjectID = types.StringValue(auth.AuthProviderProjectID)
	state.Enabled = types.BoolValue(config.Enabled)
	state.WebhookURL = types.StringPointerValue(config.WebhookURL)
	if config.TimeoutSeconds == nil {
		state.TimeoutSeconds = types.Int64Null()
	} else {
		state.TimeoutSeconds = types.Int64Value(int64(*config.TimeoutSeconds))
	}
	names := make([]string, 0, len(config.EnabledEvents))
	for _, event := range config.EnabledEvents {
		names = append(names, event.String())
	}
	events, diagnostics := types.SetValueFrom(ctx, types.StringType, names)
	resp.Diagnostics.Append(diagnostics...)
	state.EnabledEvents = events
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}
func (r *branchAuthWebhookResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state branchAuthWebhookModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	if _, err := r.integration(ctx, state); err != nil {
		resp.Diagnostics.AddError("Unable to Check Webhook Integration", err.Error())
		return
	}
	plan.ID = state.ID
	plan.AuthProviderProjectID = state.AuthProviderProjectID
	if err := r.write(ctx, plan, false); err != nil {
		resp.Diagnostics.AddError("Unable to Update Auth Webhook", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}
func (r *branchAuthWebhookResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state branchAuthWebhookModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	if _, err := r.integration(ctx, state); apiNotFound(err) {
		return
	} else if err != nil {
		resp.Diagnostics.AddError("Unable to Check Webhook Integration", err.Error())
		return
	}
	if err := r.write(ctx, state, true); err != nil {
		resp.Diagnostics.AddError("Unable to Disable Auth Webhook", err.Error())
	}
}
func (r *branchAuthWebhookResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	parts := strings.Split(req.ID, "/")
	if len(parts) != 3 || parts[0] == "" || parts[1] == "" || parts[2] == "" {
		resp.Diagnostics.AddError("Invalid Import Identifier", "Use PROJECT_ID/BRANCH_ID/DATABASE_NAME.")
		return
	}
	state := branchAuthWebhookModel{ID: types.StringValue(req.ID), ProjectID: types.StringValue(parts[0]), BranchID: types.StringValue(parts[1]), DatabaseName: types.StringValue(parts[2]), EnabledEvents: types.SetNull(types.StringType)}
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

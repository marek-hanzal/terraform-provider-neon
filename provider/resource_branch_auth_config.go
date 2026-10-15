package provider

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"
	neon "github.com/kislerdm/neon-sdk-go"
)

var _ resource.Resource = (*branchAuthConfigResource)(nil)
var _ resource.ResourceWithConfigure = (*branchAuthConfigResource)(nil)
var _ resource.ResourceWithImportState = (*branchAuthConfigResource)(nil)

type branchAuthConfigResource struct{ client *neonClient }

type branchAuthConfigModel struct {
	ID                    types.String            `tfsdk:"id"`
	ProjectID             types.String            `tfsdk:"project_id"`
	BranchID              types.String            `tfsdk:"branch_id"`
	DatabaseName          types.String            `tfsdk:"database_name"`
	AuthProviderProjectID types.String            `tfsdk:"auth_provider_project_id"`
	ApplicationName       types.String            `tfsdk:"application_name"`
	AllowLocalhost        types.Bool              `tfsdk:"allow_localhost"`
	MagicLink             *authMagicLinkModel     `tfsdk:"magic_link"`
	EmailAndPassword      *authEmailPasswordModel `tfsdk:"email_and_password"`
	EmailProvider         *authEmailProviderModel `tfsdk:"email_provider"`
	TrustedDomains        types.Set               `tfsdk:"trusted_domains"`
}

func NewBranchAuthConfigResource() resource.Resource { return &branchAuthConfigResource{} }
func (r *branchAuthConfigResource) Metadata(_ context.Context, _ resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = "neon_branch_auth_config"
}
func (r *branchAuthConfigResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.client = configureBackend(req, resp)
}
func (r *branchAuthConfigResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	attributes := authSettingsSchema()
	attributes["id"] = schema.StringAttribute{Computed: true, Description: "Settings identity: PROJECT_ID/BRANCH_ID/DATABASE_NAME."}
	attributes["project_id"] = backendIdentityAttribute("Neon project ID. Reference the managed neon_branch_auth resource to order configuration after enablement.")
	attributes["branch_id"] = backendIdentityAttribute("Neon branch ID. Manage only one config resource per branch.")
	attributes["database_name"] = backendIdentityAttribute("Database of the existing Neon-managed Better Auth integration.")
	attributes["auth_provider_project_id"] = schema.StringAttribute{Computed: true, Description: "Integration identity captured to prevent settings being applied to a different integration."}
	resp.Schema = schema.Schema{Description: "Manages selected settings of an existing Neon-managed Better Auth integration. Changes never enable, disable or replace Auth. Removing this resource leaves remote settings and user data intact.", Attributes: attributes}
}

func (r *branchAuthConfigResource) get(ctx context.Context, model branchAuthConfigModel) (neon.NeonAuthIntegration, error) {
	if r.client == nil {
		return neon.NeonAuthIntegration{}, fmt.Errorf("configure the Neon provider")
	}
	owner := &branchAuthResource{client: r.client}
	auth, err := owner.get(ctx, branchAuthModel{ProjectID: model.ProjectID, BranchID: model.BranchID})
	if err != nil {
		return auth, err
	}
	if auth.DbName != model.DatabaseName.ValueString() {
		return auth, fmt.Errorf("branch Auth uses database %q, not managed database %q", auth.DbName, model.DatabaseName.ValueString())
	}
	if !model.AuthProviderProjectID.IsNull() && !model.AuthProviderProjectID.IsUnknown() && model.AuthProviderProjectID.ValueString() != "" && auth.AuthProviderProjectID != model.AuthProviderProjectID.ValueString() {
		return auth, fmt.Errorf("the Auth integration changed; remove the old config resource from state and import the intended integration before managing its settings")
	}
	return auth, nil
}

func (r *branchAuthConfigResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan branchAuthConfigModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	auth, err := r.get(ctx, plan)
	if err != nil {
		resp.Diagnostics.AddError("Unable to Read Branch Auth Before Configuration", err.Error())
		return
	}
	plan.ID = types.StringValue(strings.Join([]string{plan.ProjectID.ValueString(), plan.BranchID.ValueString(), plan.DatabaseName.ValueString()}, "/"))
	plan.AuthProviderProjectID = types.StringValue(auth.AuthProviderProjectID)
	// Failed Create may taint this logical config resource, never the separate
	// Auth integration. Delete only forgets ownership; retries read fresh domains.
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
	if err := r.applySettings(ctx, branchAuthConfigModel{TrustedDomains: types.SetNull(types.StringType)}, plan); err != nil {
		resp.Diagnostics.AddError("Unable to Configure Branch Auth Settings", err.Error())
		return
	}
	_, err = r.get(ctx, plan)
	if err != nil {
		resp.Diagnostics.AddError("Unable to Read Branch Auth Settings", err.Error())
		return
	}
	// Apply must retain every known/null planned setting. Read owns remote drift;
	// an immediate GET can be stale, normalized or contain opaque credentials.
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *branchAuthConfigResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state branchAuthConfigModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	auth, err := r.get(ctx, state)
	if apiNotFound(err) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err == nil {
		err = r.readSettings(ctx, &state, auth)
	}
	if err != nil {
		resp.Diagnostics.AddError("Unable to Read Branch Auth Settings", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *branchAuthConfigResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state branchAuthConfigModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	_, err := r.get(ctx, state)
	if err != nil {
		resp.Diagnostics.AddError("Unable to Check Branch Auth Before Settings Update", err.Error())
		return
	}
	if err := r.applySettings(ctx, state, plan); err != nil {
		// Earlier PATCHes may have succeeded. Retain prior state; the next
		// refresh and fresh domain diff reconcile partial writes safely.
		resp.Diagnostics.AddError("Unable to Update Branch Auth Settings", err.Error())
		return
	}
	plan.ID, plan.AuthProviderProjectID = state.ID, state.AuthProviderProjectID
	_, err = r.get(ctx, plan)
	if err != nil {
		resp.Diagnostics.AddError("Unable to Read Updated Branch Auth Settings", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *branchAuthConfigResource) Delete(ctx context.Context, _ resource.DeleteRequest, resp *resource.DeleteResponse) {
	// Settings have no independent remote lifecycle or reset endpoint. Explicit
	// false/empty values change them; removal only relinquishes management.
	resp.State.RemoveResource(ctx)
}

func (r *branchAuthConfigResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	parts, err := splitBackendID(req.ID)
	if err != nil {
		resp.Diagnostics.AddError("Invalid Branch Auth Config Import ID", err.Error())
		return
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	model := branchAuthConfigModel{ID: types.StringValue(req.ID), ProjectID: types.StringValue(parts[0]), BranchID: types.StringValue(parts[1]), DatabaseName: types.StringValue(parts[2]), TrustedDomains: types.SetNull(types.StringType)}
	auth, err := r.get(ctx, model)
	if err != nil {
		resp.Diagnostics.AddError("Unable to Import Branch Auth Settings", err.Error())
		return
	}
	model.AuthProviderProjectID = types.StringValue(auth.AuthProviderProjectID)
	resp.Diagnostics.Append(resp.State.Set(ctx, &model)...)
}

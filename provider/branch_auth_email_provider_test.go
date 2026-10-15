package provider

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

	frameworkresource "github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/resource"
	"github.com/hashicorp/terraform-plugin-sdk/v2/terraform"
	"github.com/stretchr/testify/require"
)

const authSMTPFullSettings = `email_provider = {
  type = "standard"
  host = "smtp.example.com"
  port = 465
  username = "smtp-user"
  password = "smtp-secret"
  sender_email = "login@example.com"
  sender_name = "Example"
}`

// Redacted GET/PATCH must not erase or resend a configured password. Real GET
// responses are not password echoes; releasing/importing ownership must not capture them.
func TestBranchAuthEmailProviderTerraformLifecycle(t *testing.T) {
	m := newAuthSettingsMock(t)
	server := httptest.NewServer(m)
	defer server.Close()
	full := authSettingsConfig(authSMTPFullSettings)
	rotated := strings.Replace(full, "smtp-secret", "rotated-secret", 1)
	partial := authSettingsConfig(`email_provider = { type = "standard", sender_name = "Changed" }`)
	check := func(assertions func(*terraform.State)) resource.TestCheckFunc {
		return func(state *terraform.State) error {
			m.mu.Lock()
			defer m.mu.Unlock()
			require.Equal(t, 1, m.creates)
			require.Zero(t, m.deletes)
			require.Equal(t, "auth-original", state.RootModule().Resources["neon_branch_auth.app"].Primary.Attributes["auth_provider_project_id"])
			assertions(state)
			return nil
		}
	}
	attrs := func(state *terraform.State) map[string]string {
		return state.RootModule().Resources["neon_branch_auth_config.settings"].Primary.Attributes
	}
	var writes int
	resource.UnitTest(t, resource.TestCase{ProtoV6ProviderFactories: backendFactories(server), Steps: []resource.TestStep{
		{Config: full + `output "unsafe_password" { value = neon_branch_auth_config.settings.email_provider.password }`, ExpectError: regexp.MustCompile("Output refers to sensitive values")},
		{Config: full, Check: check(func(state *terraform.State) {
			require.Equal(t, "smtp-secret", attrs(state)["email_provider.password"])
			require.Equal(t, authSettingsCall{"PATCH", "/email_provider", map[string]any{"type": "standard", "host": "smtp.example.com", "port": float64(465), "username": "smtp-user", "password": "smtp-secret", "sender_email": "login@example.com", "sender_name": "Example"}}, m.calls[len(m.calls)-1])
			writes = len(m.calls)
		})},
		{Config: full, PlanOnly: true},
		{Config: rotated, Check: check(func(state *terraform.State) {
			require.Len(t, m.calls, writes+1)
			require.Equal(t, map[string]any{"type": "standard", "password": "rotated-secret"}, m.calls[len(m.calls)-1].body)
			require.Equal(t, "rotated-secret", attrs(state)["email_provider.password"])
		})},
		{PreConfig: func() {
			m.mu.Lock()
			defer m.mu.Unlock()
			m.redactSMTPPassword = false
			m.smtp["password"] = "console-secret"
			m.smtp["host"] = "console.example.com"
		}, Config: rotated, PlanOnly: true, ExpectNonEmptyPlan: true},
		{Config: rotated, Check: check(func(_ *terraform.State) {
			require.Equal(t, "console-secret", m.smtp["password"])
			require.Equal(t, "smtp.example.com", m.smtp["host"])
			require.NotContains(t, m.calls[len(m.calls)-1].body, "password")
		})},
		{Config: partial, Check: check(func(state *terraform.State) {
			require.Equal(t, map[string]any{"type": "standard", "sender_name": "Changed"}, m.calls[len(m.calls)-1].body)
			require.Empty(t, attrs(state)["email_provider.password"])
			require.Empty(t, attrs(state)["email_provider.host"])
			require.Empty(t, attrs(state)["email_provider.username"])
			writes = len(m.calls)
		})},
		{PreConfig: func() {
			m.mu.Lock()
			defer m.mu.Unlock()
			m.smtp["password"] = "unmanaged-console-secret"
			m.smtp["username"] = "unmanaged-user"
		}, Config: partial, PlanOnly: true},
		{Config: partial, Check: check(func(state *terraform.State) {
			require.Len(t, m.calls, writes)
			encoded, err := json.Marshal(state)
			require.NoError(t, err)
			require.NotContains(t, string(encoded), "unmanaged-console-secret")
			require.NotContains(t, string(encoded), "unmanaged-user")
		})},
		{Config: full},
		{PreConfig: func() {
			m.mu.Lock()
			defer m.mu.Unlock()
			m.smtp = map[string]any{"type": "shared", "sender_email": "login@example.com", "sender_name": "Example"}
		}, Config: full, PlanOnly: true, ExpectNonEmptyPlan: true},
		{Config: full, Check: check(func(_ *terraform.State) {
			require.Len(t, m.calls[len(m.calls)-1].body, 7)
			require.Equal(t, "smtp-secret", m.smtp["password"])
		})},
		{Config: authSettingsConfig(""), Check: check(func(state *terraform.State) {
			require.Empty(t, attrs(state)["email_provider.password"])
			writes = len(m.calls)
		})},
		{ResourceName: "neon_branch_auth_config.settings", ImportState: true, ImportStateVerify: true},
		{Config: authSettingsParentConfig(), Check: check(func(state *terraform.State) {
			require.NotContains(t, state.RootModule().Resources, "neon_branch_auth_config.settings")
			require.Len(t, m.calls, writes)
			require.True(t, m.exists)
			require.Equal(t, "smtp-secret", m.smtp["password"])
		})},
	}})
}

// Terraform, rather than a model-only assertion, enforces the known sensitive
// plan value. The host case additionally leaves observable drift visible to Read.
func TestBranchAuthEmailProviderApplyPreservesSensitivePlan(t *testing.T) {
	for _, tc := range []struct {
		name, field, response string
		drift                 bool
	}{
		{"opaque password response", "password", "opaque-response-value", false},
		{"stale managed hostname", "host", "stale.example.com", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := newAuthSettingsMock(t)
			m.smtpResponseOverrides = map[string]any{tc.field: tc.response}
			server := httptest.NewServer(m)
			defer server.Close()
			full := authSettingsConfig(authSMTPFullSettings)
			check := func(password string) resource.TestCheckFunc {
				return func(state *terraform.State) error {
					m.mu.Lock()
					defer m.mu.Unlock()
					attrs := state.RootModule().Resources["neon_branch_auth_config.settings"].Primary.Attributes
					require.Equal(t, password, attrs["email_provider.password"])
					require.Equal(t, "smtp.example.com", attrs["email_provider.host"])
					require.Equal(t, 1, m.creates)
					require.Zero(t, m.deletes)
					return nil
				}
			}
			resource.UnitTest(t, resource.TestCase{ProtoV6ProviderFactories: backendFactories(server), Steps: []resource.TestStep{
				{Config: full, ExpectNonEmptyPlan: tc.drift, Check: check("smtp-secret")},
				{Config: strings.Replace(full, "smtp-secret", "rotated-secret", 1), ExpectNonEmptyPlan: tc.drift, Check: check("rotated-secret")},
			}})
		})
	}
}

func smtpManagedModel() branchAuthConfigModel {
	return branchAuthConfigModel{
		ID: types.StringValue("p/b/app"), ProjectID: types.StringValue("p"), BranchID: types.StringValue("b"), DatabaseName: types.StringValue("app"), AuthProviderProjectID: types.StringValue("auth-original"),
		TrustedDomains: types.SetNull(types.StringType),
		EmailProvider:  &authEmailProviderModel{Type: types.StringValue("standard"), Password: types.StringValue("known-secret"), SenderName: types.StringValue("Example")},
	}
}

func TestBranchAuthEmailProviderErrorsPreserveStateAndSecrets(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		status     int
	}{
		{"forbidden", `{"message":"known-secret REMOTE-SECRET","code":"SECRET"}`, 403},
		{"missing endpoint", `{"message":"REMOTE-SECRET"}`, 404},
		{"incomplete settings", `{"type":"standard","password":"REMOTE-SECRET"}`, 200},
		{"null password", `{"type":"standard","host":"smtp.example.com","port":465,"username":"u","password":null,"sender_email":"a@example.com","sender_name":"A"}`, 200},
		{"invalid type", `{"type":"REMOTE-SECRET"}`, 200},
		{"invalid password", `{"type":"standard","host":"smtp.example.com","port":465,"username":"u","password":{"secret":"REMOTE-SECRET"},"sender_email":"a@example.com","sender_name":"A"}`, 200},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			m := newAuthSettingsMock(t)
			m.exists = true
			client, server := testBackendClient(t, http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				if strings.HasSuffix(req.URL.Path, "/email_provider") {
					w.WriteHeader(tc.status)
					_, _ = io.WriteString(w, tc.body)
					return
				}
				m.ServeHTTP(w, req)
			}))
			defer server.Close()
			r := &branchAuthConfigResource{client: client}
			var schema frameworkresource.SchemaResponse
			r.Schema(ctx, frameworkresource.SchemaRequest{}, &schema)
			state := tfsdk.State{Schema: schema.Schema}
			model := smtpManagedModel()
			require.False(t, state.Set(ctx, &model).HasError())
			response := frameworkresource.ReadResponse{State: state}
			r.Read(ctx, frameworkresource.ReadRequest{State: state}, &response)
			require.True(t, response.Diagnostics.HasError())
			require.True(t, state.Raw.Equal(response.State.Raw))
			for _, diagnostic := range response.Diagnostics {
				require.NotContains(t, diagnostic.Detail(), "known-secret")
				require.NotContains(t, diagnostic.Detail(), "REMOTE-SECRET")
			}
			api, closeBodies, err := client.scopedAuthSettingsSDK(ctx)
			require.NoError(t, err)
			err = applyAuthEmailProvider(api, "p", "b", nil, model.EmailProvider)
			closeBodies()
			if tc.status >= 400 {
				require.Error(t, err)
				require.NotContains(t, err.Error(), "known-secret")
				require.NotContains(t, err.Error(), "REMOTE-SECRET")
			}
		})
	}
}

func TestBranchAuthEmailProviderUpdateIdentityGuard(t *testing.T) {
	ctx := context.Background()
	m := newAuthSettingsMock(t)
	m.exists = true
	client, server := testBackendClient(t, m)
	defer server.Close()
	r := &branchAuthConfigResource{client: client}
	var schema frameworkresource.SchemaResponse
	r.Schema(ctx, frameworkresource.SchemaRequest{}, &schema)
	model := smtpManagedModel()
	model.AuthProviderProjectID = types.StringValue("replaced-integration")
	state := tfsdk.State{Schema: schema.Schema}
	plan := tfsdk.Plan{Schema: schema.Schema}
	require.False(t, state.Set(ctx, &model).HasError())
	model.EmailProvider.Password = types.StringValue("rotated-secret")
	require.False(t, plan.Set(ctx, &model).HasError())
	response := frameworkresource.UpdateResponse{State: state}
	r.Update(ctx, frameworkresource.UpdateRequest{State: state, Plan: plan}, &response)
	require.True(t, response.Diagnostics.HasError())
	require.True(t, state.Raw.Equal(response.State.Raw))
	require.Empty(t, m.calls)
	require.True(t, m.exists)
}

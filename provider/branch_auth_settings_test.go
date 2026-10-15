package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/path"
	frameworkresource "github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/resource"
	"github.com/hashicorp/terraform-plugin-sdk/v2/terraform"
	"github.com/stretchr/testify/require"
)

type authSettingsCall struct {
	method, path string
	body         map[string]any
}

type authSettingsMock struct {
	t                         *testing.T
	mu                        sync.Mutex
	exists                    bool
	creates, deletes          int
	deleteData                bool
	name                      string
	magic, email              map[string]any
	localhost                 bool
	domains                   map[string]map[string]bool
	calls                     []authSettingsCall
	failDomain, failPath      string
	failStatus                int
	missingMagic, missingName bool
}

func newAuthSettingsMock(t *testing.T) *authSettingsMock {
	return &authSettingsMock{t: t, name: "Default", localhost: true,
		magic:   map[string]any{"enabled": false, "expires_in": float64(5), "disable_sign_up": false},
		email:   map[string]any{"enabled": true, "email_verification_method": "link", "require_email_verification": false, "auto_sign_in_after_verification": true, "send_verification_email_on_sign_up": false, "send_verification_email_on_sign_in": true, "disable_sign_up": true},
		domains: map[string]map[string]bool{"better_auth": {"https://old.example.com": true}, "stack": {"https://legacy.example.com": true}},
	}
}

func (m *authSettingsMock) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if r.Header.Get("Authorization") != "Bearer test" {
		m.t.Error("missing API authorization")
		w.WriteHeader(401)
		return
	}
	p := strings.TrimPrefix(r.URL.Path, "/api/v2/projects/p/branches/b/auth")
	if p == r.URL.Path {
		m.t.Errorf("unexpected origin/path %s", r.URL)
		w.WriteHeader(500)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	if m.failPath != "" && p == m.failPath {
		w.WriteHeader(m.failStatus)
		_, _ = io.WriteString(w, `{"code":"TEST_ERROR","message":"synthetic refusal"}`)
		return
	}
	respond := func(v any) {
		if err := json.NewEncoder(w).Encode(v); err != nil {
			m.t.Error(err)
		}
	}
	if r.Method == "GET" {
		if !m.exists {
			w.WriteHeader(404)
			respond(map[string]string{"code": "NOT_FOUND", "message": "missing integration"})
			return
		}
		switch p {
		case "":
			v := map[string]any{"auth_provider": "better_auth", "auth_provider_project_id": "auth-original", "branch_id": "b", "db_name": "app", "owned_by": "neon", "created_at": "2026-10-15T00:00:00Z", "base_url": "https://auth.example.com", "jwks_url": "https://auth.example.com/jwks"}
			if !m.missingName {
				v["name"] = m.name
			}
			respond(v)
		case "/plugins":
			v := map[string]any{"oauth_providers": []any{map[string]any{"id": "google", "type": "standard", "client_secret": "DO-NOT-PERSIST"}}}
			if !m.missingMagic {
				v["magic_link"] = m.magic
			}
			respond(v)
		case "/email_and_password":
			respond(m.email)
		case "/allow_localhost":
			respond(map[string]any{"allow_localhost": m.localhost})
		case "/domains":
			entries := []map[string]string{}
			for provider, domains := range m.domains {
				for domain := range domains {
					entries = append(entries, map[string]string{"domain": domain, "auth_provider": provider})
				}
			}
			sort.Slice(entries, func(i, j int) bool { return entries[i]["domain"] < entries[j]["domain"] })
			respond(map[string]any{"domains": entries})
		default:
			m.t.Errorf("unexpected GET %s", p)
			w.WriteHeader(500)
		}
		return
	}
	var body map[string]any
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		m.t.Error(err)
		w.WriteHeader(400)
		return
	}
	m.calls = append(m.calls, authSettingsCall{r.Method, p, body})
	switch {
	case p == "" && r.Method == "POST":
		if m.exists {
			w.WriteHeader(409)
			return
		}
		require.Equal(m.t, "better_auth", body["auth_provider"])
		require.Equal(m.t, "app", body["database_name"])
		m.exists = true
		m.creates++
		w.WriteHeader(201)
		respond(map[string]any{"auth_provider": "better_auth", "auth_provider_project_id": "auth-original", "base_url": "https://auth.example.com", "jwks_url": "https://auth.example.com/jwks", "schema_name": "neon_auth", "table_name": "users_sync", "pub_client_key": "", "secret_server_key": "DO-NOT-PERSIST"})
	case p == "" && r.Method == "DELETE":
		m.deleteData = body["delete_data"].(bool)
		m.exists = false
		m.deletes++
		respond(map[string]any{})
	case p == "/plugins/magic-link" && r.Method == "PATCH":
		require.NotEmpty(m.t, body)
		for key, value := range body {
			if _, ok := m.magic[key]; !ok {
				m.t.Errorf("unsupported Magic Link field %s", key)
			}
			m.magic[key] = value
		}
		respond(m.magic)
	case p == "/email_and_password" && r.Method == "PATCH":
		require.NotEmpty(m.t, body)
		for key, value := range body {
			if _, ok := m.email[key]; !ok {
				m.t.Errorf("unsupported email field %s", key)
			}
			m.email[key] = value
		}
		respond(m.email)
	case p == "/allow_localhost" && r.Method == "PATCH":
		m.localhost = body["allow_localhost"].(bool)
		respond(map[string]any{"allow_localhost": m.localhost})
	case p == "/config" && r.Method == "PATCH":
		m.name = body["name"].(string)
		respond(map[string]any{"name": m.name})
	case p == "/domains" && r.Method == "POST":
		require.Equal(m.t, "better_auth", body["auth_provider"])
		domain := body["domain"].(string)
		if domain == m.failDomain {
			w.WriteHeader(500)
			respond(map[string]string{"code": "TEST_ERROR", "message": "synthetic domain failure"})
			return
		}
		if m.domains["better_auth"][domain] {
			m.t.Errorf("duplicate domain POST: %s", domain)
			w.WriteHeader(409)
			return
		}
		m.domains["better_auth"][domain] = true
		w.WriteHeader(201)
	case p == "/domains" && r.Method == "DELETE":
		require.Equal(m.t, "better_auth", body["auth_provider"])
		for _, item := range body["domains"].([]any) {
			delete(m.domains["better_auth"], item.(map[string]any)["domain"].(string))
		}
		respond(map[string]any{})
	default:
		m.t.Errorf("unexpected mutation %s %s", r.Method, p)
		w.WriteHeader(500)
	}
}

func authSettingsParentConfig() string {
	return `provider "neon" { api_key = "test" }
resource "neon_branch_auth" "app" {
  project_id = "p"
  branch_id = "b"
  database_name = "app"
}
`
}

func authSettingsConfig(settings string) string {
	return authSettingsParentConfig() + `resource "neon_branch_auth_config" "settings" {
  project_id = neon_branch_auth.app.project_id
  branch_id = neon_branch_auth.app.branch_id
  database_name = neon_branch_auth.app.database_name
` + settings + "\n}\n"
}

func TestBranchAuthSettingsTerraformLifecycle(t *testing.T) {
	m := newAuthSettingsMock(t)
	server := httptest.NewServer(m)
	defer server.Close()
	full := `
  application_name = "Example App"
  allow_localhost = false
  trusted_domains = ["https://app.example.com", "https://*.preview.example.com"]
  magic_link = { enabled = true, expires_in_minutes = 15, disable_sign_up = true }
  email_and_password = {
    enabled = false
    email_verification_method = "otp"
    require_email_verification = true
    auto_sign_in_after_verification = false
    send_verification_email_on_sign_up = true
    send_verification_email_on_sign_in = false
    disable_sign_up = false
  }`
	partial := `magic_link = { enabled = true }
  email_and_password = { enabled = false }`
	check := func(assertions func(*terraform.State)) resource.TestCheckFunc {
		return func(state *terraform.State) error {
			m.mu.Lock()
			defer m.mu.Unlock()
			require.Equal(t, 1, m.creates)
			require.Zero(t, m.deletes)
			attrs := state.RootModule().Resources["neon_branch_auth.app"].Primary.Attributes
			require.Equal(t, "auth-original", attrs["auth_provider_project_id"])
			require.Equal(t, "2026-10-15T00:00:00Z", attrs["created_at"])
			encoded, err := json.Marshal(state)
			require.NoError(t, err)
			require.NotContains(t, string(encoded), "DO-NOT-PERSIST")
			assertions(state)
			return nil
		}
	}
	assertFull := func(_ *terraform.State) {
		require.Equal(t, map[string]any{"enabled": true, "expires_in": float64(15), "disable_sign_up": true}, m.magic)
		require.Equal(t, false, m.email["enabled"])
		require.Equal(t, "otp", m.email["email_verification_method"])
		require.Equal(t, false, m.email["disable_sign_up"])
		require.Equal(t, false, m.email["auto_sign_in_after_verification"])
		require.Equal(t, false, m.email["send_verification_email_on_sign_in"])
		require.Equal(t, "Example App", m.name)
		require.False(t, m.localhost)
	}
	var writesBeforeRelease int
	resource.UnitTest(t, resource.TestCase{ProtoV6ProviderFactories: backendFactories(server), CheckDestroy: func(_ *terraform.State) error {
		m.mu.Lock()
		defer m.mu.Unlock()
		require.Equal(t, 1, m.deletes)
		require.False(t, m.deleteData)
		return nil
	}, Steps: []resource.TestStep{
		{Config: authSettingsConfig(full), Check: check(func(state *terraform.State) {
			assertFull(state)
			require.Equal(t, map[string]bool{"https://app.example.com": true, "https://*.preview.example.com": true}, m.domains["better_auth"])
			require.True(t, m.domains["stack"]["https://legacy.example.com"])
		})},
		{Config: authSettingsConfig(strings.Replace(full, "enabled = true, expires_in_minutes = 15, disable_sign_up = true", "enabled = false, expires_in_minutes = 1440, disable_sign_up = false", 1)), Check: check(func(_ *terraform.State) {
			require.Equal(t, map[string]any{"enabled": false, "expires_in": float64(1440), "disable_sign_up": false}, m.magic)
			require.Equal(t, authSettingsCall{"PATCH", "/plugins/magic-link", map[string]any{"enabled": false, "expires_in": float64(1440), "disable_sign_up": false}}, m.calls[len(m.calls)-1])
		})},

		{PreConfig: func() {
			m.mu.Lock()
			defer m.mu.Unlock()
			m.magic["enabled"] = false
			m.magic["expires_in"] = float64(40)
			m.magic["disable_sign_up"] = false
			m.email["enabled"] = true
			m.email["email_verification_method"] = "link"
			m.email["disable_sign_up"] = true
			m.name = "Console"
			m.localhost = true
			m.domains["better_auth"] = map[string]bool{"https://console.example.com": true}
		}, Config: authSettingsConfig(full), PlanOnly: true, ExpectNonEmptyPlan: true},
		{Config: authSettingsConfig(full), Check: check(func(state *terraform.State) { assertFull(state); writesBeforeRelease = len(m.calls) })},
		{Config: authSettingsConfig(partial), Check: check(func(_ *terraform.State) {
			require.Len(t, m.calls, writesBeforeRelease)
			require.Equal(t, float64(15), m.magic["expires_in"])
		})},
		{PreConfig: func() {
			m.mu.Lock()
			defer m.mu.Unlock()
			m.magic["expires_in"] = float64(42)
			m.magic["disable_sign_up"] = false
			m.name = "Unmanaged Console"
			m.localhost = true
			m.domains["better_auth"]["https://external.example.com"] = true
			m.email["require_email_verification"] = false
		}, Config: authSettingsConfig(partial), PlanOnly: true},
		{Config: authSettingsConfig("magic_link = {}\n email_and_password = {}"), Check: check(func(_ *terraform.State) {
			require.Len(t, m.calls, writesBeforeRelease)
			require.Equal(t, float64(42), m.magic["expires_in"])
			require.Equal(t, "Unmanaged Console", m.name)
		})},
		{Config: authSettingsConfig(""), Check: check(func(_ *terraform.State) { require.Len(t, m.calls, writesBeforeRelease) })},
		{ResourceName: "neon_branch_auth_config.settings", ImportState: true, ImportStateVerify: true},
		{Config: authSettingsConfig(full), Check: check(assertFull)},
		{Config: authSettingsConfig(strings.Replace(full, `["https://app.example.com", "https://*.preview.example.com"]`, `[]`, 1)), Check: check(func(_ *terraform.State) {
			require.Empty(t, m.domains["better_auth"])
			require.True(t, m.domains["stack"]["https://legacy.example.com"])
		})},
		{Config: authSettingsParentConfig(), Check: check(func(state *terraform.State) {
			require.NotContains(t, state.RootModule().Resources, "neon_branch_auth_config.settings")
			require.True(t, m.exists)
			require.Equal(t, true, m.magic["enabled"])
		})},
	}})
}

func TestBranchAuthSettingsPartialFailureRecovery(t *testing.T) {
	for _, duringCreate := range []bool{true, false} {
		t.Run(fmt.Sprintf("create=%v", duringCreate), func(t *testing.T) {
			m := newAuthSettingsMock(t)
			m.failDomain = "https://b.example.com"
			server := httptest.NewServer(m)
			defer server.Close()
			config := authSettingsConfig(`magic_link = { enabled = true }
  trusted_domains = ["https://a.example.com", "https://b.example.com"]`)
			steps := []resource.TestStep{}
			if !duringCreate {
				steps = append(steps, resource.TestStep{Config: authSettingsConfig("")})
			}
			steps = append(steps, resource.TestStep{Config: config, ExpectError: regexp.MustCompile("add trusted domain")}, resource.TestStep{PreConfig: func() { m.mu.Lock(); defer m.mu.Unlock(); m.failDomain = "" }, Config: config, Check: func(_ *terraform.State) error {
				m.mu.Lock()
				defer m.mu.Unlock()
				require.Equal(t, 1, m.creates)
				require.Zero(t, m.deletes)
				require.Equal(t, map[string]bool{"https://a.example.com": true, "https://b.example.com": true}, m.domains["better_auth"])
				adds := 0
				for _, call := range m.calls {
					if call.method == "POST" && call.path == "/domains" && call.body["domain"] == "https://a.example.com" {
						adds++
					}
				}
				require.Equal(t, 1, adds)
				return nil
			}})
			resource.UnitTest(t, resource.TestCase{ProtoV6ProviderFactories: backendFactories(server), Steps: steps})
		})
	}
}

func TestBranchAuthSettingsReadErrorsRetainIntegration(t *testing.T) {
	for _, tc := range []struct {
		name                      string
		status                    int
		missingMagic, missingName bool
	}{
		{"forbidden", 403, false, false}, {"missing feature endpoint", 404, false, false}, {"server error", 500, false, false}, {"missing magic link", 0, true, false}, {"missing application name", 0, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := newAuthSettingsMock(t)
			m.exists = true
			m.failStatus = tc.status
			m.missingMagic = tc.missingMagic
			m.missingName = tc.missingName
			if tc.status != 0 {
				m.failPath = "/plugins"
			}
			client, server := testBackendClient(t, m)
			defer server.Close()
			r := &branchAuthConfigResource{client: client}
			ctx := context.Background()
			var schemaResponse frameworkresource.SchemaResponse
			r.Schema(ctx, frameworkresource.SchemaRequest{}, &schemaResponse)
			state := tfsdk.State{Schema: schemaResponse.Schema}
			model := branchAuthConfigModel{ID: types.StringValue("p/b/app"), ProjectID: types.StringValue("p"), BranchID: types.StringValue("b"), DatabaseName: types.StringValue("app"), TrustedDomains: types.SetNull(types.StringType), ApplicationName: types.StringValue("Example"), MagicLink: &authMagicLinkModel{Enabled: types.BoolValue(true)}}
			diags := state.Set(ctx, &model)
			require.False(t, diags.HasError(), "%v", diags)
			response := frameworkresource.ReadResponse{State: state}
			r.Read(ctx, frameworkresource.ReadRequest{State: state}, &response)
			require.True(t, response.Diagnostics.HasError())
			require.True(t, state.Raw.Equal(response.State.Raw))
			require.Empty(t, m.calls)
		})
	}
}

func TestBranchAuthSettingsValidation(t *testing.T) {
	ctx := context.Background()
	for _, n := range []int64{4, 5, 1440, 1441} {
		var resp validator.Int64Response
		authMagicLinkExpiryValidator{}.ValidateInt64(ctx, validator.Int64Request{Path: path.Root("expires_in_minutes"), ConfigValue: types.Int64Value(n)}, &resp)
		require.Equal(t, n < 5 || n > 1440, resp.Diagnostics.HasError())
	}
	for _, tc := range []struct {
		kind, value string
		invalid     bool
	}{
		{"application_name", "", true}, {"application_name", strings.Repeat("ě", 256), false}, {"application_name", strings.Repeat("ě", 257), true},
		{"email_verification_method", "link", false}, {"email_verification_method", "otp", false}, {"email_verification_method", "magic_link", true},
	} {
		var resp validator.StringResponse
		authSettingsStringValidator{tc.kind}.ValidateString(ctx, validator.StringRequest{Path: path.Root(tc.kind), ConfigValue: types.StringValue(tc.value)}, &resp)
		require.Equal(t, tc.invalid, resp.Diagnostics.HasError(), tc.value)
	}
	for _, tc := range []struct {
		value   string
		invalid bool
	}{
		{"https://app.example.com", false}, {"https://*.preview.example.com", false}, {"http://localhost:3000", false}, {"http://[::1]:3000", false},
		{"app.example.com", true}, {"https://example.com/", true}, {"https://example.com/path", true}, {"https://user:pass@example.com", true}, {"https://example.com?query=1", true}, {"https://example.com#fragment", true}, {"ftp://example.com", true},
	} {
		value, diags := types.SetValueFrom(ctx, types.StringType, []string{tc.value})
		require.False(t, diags.HasError())
		var resp validator.SetResponse
		authTrustedDomainsValidator{}.ValidateSet(ctx, validator.SetRequest{Path: path.Root("trusted_domains"), ConfigValue: value}, &resp)
		require.Equal(t, tc.invalid, resp.Diagnostics.HasError(), tc.value)
	}
}

func TestBranchAuthConfigIncompleteResponsesRetainState(t *testing.T) {
	for _, tc := range []struct{ name, path, body string }{
		{"missing localhost", "/allow_localhost", `{}`},
		{"null localhost", "/allow_localhost", `{"allow_localhost":null}`},
		{"missing magic boolean", "/plugins", `{"magic_link":{"expires_in":5,"disable_sign_up":false}}`},
		{"null magic boolean", "/plugins", `{"magic_link":{"enabled":null,"expires_in":5,"disable_sign_up":false}}`},
		{"missing signup boolean", "/plugins", `{"magic_link":{"enabled":false,"expires_in":5}}`},
		{"missing email boolean", "/email_and_password", `{}`},
		{"missing domains array", "/domains", `{}`},
		{"null domains array", "/domains", `{"domains":null}`},
		{"incomplete domain entry", "/domains", `{"domains":[{"auth_provider":"better_auth"}]}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			m := newAuthSettingsMock(t)
			m.exists = true
			client, server := testBackendClient(t, http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				if strings.HasSuffix(req.URL.Path, "/auth"+tc.path) {
					w.Header().Set("Content-Type", "application/json")
					_, _ = io.WriteString(w, tc.body)
					return
				}
				m.ServeHTTP(w, req)
			}))
			defer server.Close()
			r := &branchAuthConfigResource{client: client}
			model := branchAuthConfigModel{ID: types.StringValue("p/b/app"), ProjectID: types.StringValue("p"), BranchID: types.StringValue("b"), DatabaseName: types.StringValue("app"), AuthProviderProjectID: types.StringValue("auth-original"), TrustedDomains: types.SetNull(types.StringType)}
			switch tc.path {
			case "/plugins":
				model.MagicLink = &authMagicLinkModel{Enabled: types.BoolValue(false)}
			case "/email_and_password":
				model.EmailAndPassword = &authEmailPasswordModel{Enabled: types.BoolValue(false)}
			case "/allow_localhost":
				model.AllowLocalhost = types.BoolValue(false)
			case "/domains":
				model.TrustedDomains = types.SetValueMust(types.StringType, []attr.Value{})
			}
			var schemaResponse frameworkresource.SchemaResponse
			r.Schema(ctx, frameworkresource.SchemaRequest{}, &schemaResponse)
			state := tfsdk.State{Schema: schemaResponse.Schema}
			diags := state.Set(ctx, &model)
			require.False(t, diags.HasError(), "%v", diags)
			response := frameworkresource.ReadResponse{State: state}
			r.Read(ctx, frameworkresource.ReadRequest{State: state}, &response)
			require.True(t, response.Diagnostics.HasError())
			require.True(t, state.Raw.Equal(response.State.Raw))
			require.Empty(t, m.calls)
			if tc.path == "/domains" {
				api, closeBodies, err := client.scopedAuthSettingsSDK(ctx)
				require.NoError(t, err)
				defer closeBodies()
				desired, diags := types.SetValueFrom(ctx, types.StringType, []string{"https://new.example.com"})
				require.False(t, diags.HasError())
				require.Error(t, reconcileAuthDomains(ctx, api, "p", "b", desired))
				require.Empty(t, m.calls)
			}
		})
	}
}

func TestBranchAuthConfigIdentityGuardAndRemoval(t *testing.T) {
	ctx := context.Background()
	m := newAuthSettingsMock(t)
	m.exists = true
	client, server := testBackendClient(t, m)
	defer server.Close()
	r := &branchAuthConfigResource{client: client}
	var schemaResponse frameworkresource.SchemaResponse
	r.Schema(ctx, frameworkresource.SchemaRequest{}, &schemaResponse)
	for _, identity := range []struct{ database, id string }{{"wrong", "auth-original"}, {"app", "replacement"}} {
		model := branchAuthConfigModel{ID: types.StringValue("p/b/" + identity.database), ProjectID: types.StringValue("p"), BranchID: types.StringValue("b"), DatabaseName: types.StringValue(identity.database), AuthProviderProjectID: types.StringValue(identity.id), TrustedDomains: types.SetNull(types.StringType)}
		state := tfsdk.State{Schema: schemaResponse.Schema}
		require.False(t, state.Set(ctx, &model).HasError())
		response := frameworkresource.ReadResponse{State: state}
		r.Read(ctx, frameworkresource.ReadRequest{State: state}, &response)
		require.True(t, response.Diagnostics.HasError())
		require.True(t, state.Raw.Equal(response.State.Raw))
		removed := frameworkresource.DeleteResponse{State: state}
		r.Delete(ctx, frameworkresource.DeleteRequest{State: state}, &removed)
		require.False(t, removed.Diagnostics.HasError())
		require.True(t, removed.State.Raw.IsNull())
		require.True(t, m.exists)
		require.Empty(t, m.calls)
	}
	for _, id := range []string{"p/b/wrong", "/b/app", "p//app", "p/b/", "p/b/app/extra"} {
		response := frameworkresource.ImportStateResponse{State: tfsdk.State{Schema: schemaResponse.Schema}}
		r.ImportState(ctx, frameworkresource.ImportStateRequest{ID: id}, &response)
		require.True(t, response.Diagnostics.HasError(), id)
	}
	m.exists = false
	model := branchAuthConfigModel{ID: types.StringValue("p/b/app"), ProjectID: types.StringValue("p"), BranchID: types.StringValue("b"), DatabaseName: types.StringValue("app"), TrustedDomains: types.SetNull(types.StringType)}
	state := tfsdk.State{Schema: schemaResponse.Schema}
	require.False(t, state.Set(ctx, &model).HasError())
	response := frameworkresource.ReadResponse{State: state}
	r.Read(ctx, frameworkresource.ReadRequest{State: state}, &response)
	require.False(t, response.Diagnostics.HasError())
	require.True(t, response.State.Raw.IsNull())
}

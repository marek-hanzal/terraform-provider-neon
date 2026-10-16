package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	frameworkresource "github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/resource"
	"github.com/hashicorp/terraform-plugin-sdk/v2/terraform"
	"github.com/stretchr/testify/require"
)

// Native mock lifecycle: handler configuration is independently owned from Auth.
func TestBranchAuthWebhookTerraformLifecycle(t *testing.T) {
	var mu sync.Mutex
	config := map[string]any{"enabled": false}
	var writes []map[string]any
	integration := "auth-original"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		require.Equal(t, "Bearer test", r.Header.Get("Authorization"))
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == "GET" && strings.HasSuffix(r.URL.Path, "/auth"):
			_ = json.NewEncoder(w).Encode(map[string]any{"auth_provider": "better_auth", "auth_provider_project_id": integration, "branch_id": "b", "db_name": "app", "owned_by": "neon", "created_at": "2026-10-15T00:00:00Z", "base_url": "https://auth.example.com", "jwks_url": "https://auth.example.com/jwks"})
		case strings.HasSuffix(r.URL.Path, "/auth/webhooks") && r.Method == "GET":
			_ = json.NewEncoder(w).Encode(config)
		case strings.HasSuffix(r.URL.Path, "/auth/webhooks") && r.Method == "PUT":
			var body map[string]any
			require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
			writes = append(writes, body)
			for key, value := range body {
				config[key] = value
			}
			_ = json.NewEncoder(w).Encode(config)
		default:
			t.Errorf("unexpected API request %s %s", r.Method, r.URL.Path)
			w.WriteHeader(500)
		}
	}))
	defer server.Close()
	hcl := func(endpoint string) string {
		return fmt.Sprintf(`provider "neon" { api_key = "test" }
resource "neon_branch_auth_webhook" "mail" {
  project_id = "p"
  branch_id = "b"
  database_name = "app"
  enabled = true
  webhook_url = %q
  enabled_events = ["send.magic_link"]
  timeout_seconds = 5
}`, endpoint)
	}
	resource.UnitTest(t, resource.TestCase{ProtoV6ProviderFactories: backendFactories(server), CheckDestroy: func(_ *terraform.State) error {
		mu.Lock()
		defer mu.Unlock()
		require.Equal(t, map[string]any{"enabled": false}, writes[len(writes)-1])
		return nil
	}, Steps: []resource.TestStep{
		{Config: hcl("https://handler.example.com/api/auth/email"), Check: resource.ComposeTestCheckFunc(
			resource.TestCheckResourceAttr("neon_branch_auth_webhook.mail", "enabled", "true"),
			resource.TestCheckResourceAttr("neon_branch_auth_webhook.mail", "auth_provider_project_id", "auth-original"),
		)},
		{ResourceName: "neon_branch_auth_webhook.mail", ImportState: true, ImportStateVerify: true},
		{PreConfig: func() { mu.Lock(); config["enabled"] = false; mu.Unlock() }, Config: hcl("https://handler.example.com/api/auth/email"), Check: func(_ *terraform.State) error {
			mu.Lock()
			defer mu.Unlock()
			require.Equal(t, true, config["enabled"])
			return nil
		}},
		{Config: hcl("https://other.example.com/api/auth/email"), Check: resource.TestCheckResourceAttr("neon_branch_auth_webhook.mail", "webhook_url", "https://other.example.com/api/auth/email")},
	}})
}

func TestBranchAuthWebhookRejectsMissingRequiredResponse(t *testing.T) {
	for _, body := range []string{`{}`, `{"enabled":null}`} {
		require.Error(t, validateAuthSettingsResponse("/projects/p/branches/b/auth/webhooks", []byte(body)))
	}
	require.NoError(t, validateAuthSettingsResponse("/projects/p/branches/b/auth/webhooks", []byte(`{"enabled":false}`)))
}

func TestBranchAuthWebhookDoesNotDisableReplacementIntegration(t *testing.T) {
	ctx := context.Background()
	client, server := testBackendClient(t, http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		require.Equal(t, "GET", req.Method)
		require.True(t, strings.HasSuffix(req.URL.Path, "/auth"))
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"auth_provider":"better_auth","auth_provider_project_id":"replacement","branch_id":"b","db_name":"app","owned_by":"neon","created_at":"2026-10-15T00:00:00Z","base_url":"https://auth.example.com","jwks_url":"https://auth.example.com/jwks"}`))
	}))
	defer server.Close()
	r := &branchAuthWebhookResource{client: client}
	var schemaResponse frameworkresource.SchemaResponse
	r.Schema(ctx, frameworkresource.SchemaRequest{}, &schemaResponse)
	state := tfsdk.State{Schema: schemaResponse.Schema}
	model := branchAuthWebhookModel{ID: types.StringValue("p/b/app"), ProjectID: types.StringValue("p"), BranchID: types.StringValue("b"), DatabaseName: types.StringValue("app"), AuthProviderProjectID: types.StringValue("original"), EnabledEvents: types.SetNull(types.StringType)}
	require.False(t, state.Set(ctx, &model).HasError())
	var response frameworkresource.DeleteResponse
	r.Delete(ctx, frameworkresource.DeleteRequest{State: state}, &response)
	require.True(t, response.Diagnostics.HasError())
}

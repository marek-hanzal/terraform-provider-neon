package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	neon "github.com/kislerdm/neon-sdk-go"
)

// The SDK decodes missing required booleans/slices into false/nil. Validate the
// settings response shape before SDK decoding can conceal that information.
type authSettingsHTTPClient struct{ client neon.HTTPClient }

func (c authSettingsHTTPClient) Do(req *http.Request) (*http.Response, error) {
	resp, err := c.client.Do(req)
	if err != nil || resp == nil || req.Method != http.MethodGet || resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return resp, err
	}
	suffixes := []string{"/auth/plugins", "/auth/email_and_password", "/auth/email_provider", "/auth/allow_localhost", "/auth/domains", "/auth/webhooks"}
	matched := false
	for _, suffix := range suffixes {
		if strings.HasSuffix(req.URL.Path, suffix) {
			matched = true
			break
		}
	}
	if !matched {
		return resp, nil
	}
	const limit = 1 << 20
	body, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	_ = resp.Body.Close()
	if err != nil {
		return nil, fmt.Errorf("read auth settings response: %w", err)
	}
	if len(body) > limit {
		return nil, fmt.Errorf("auth settings response exceeds 1 MiB")
	}
	if err := validateAuthSettingsResponse(req.URL.Path, body); err != nil {
		return nil, err
	}
	resp.Body = io.NopCloser(bytes.NewReader(body))
	return resp, nil
}

func authResponseObject(body []byte, required ...string) (map[string]json.RawMessage, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(body, &fields); err != nil || fields == nil {
		return nil, fmt.Errorf("invalid auth settings response object")
	}
	for _, name := range required {
		value, ok := fields[name]
		if !ok || bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			return nil, fmt.Errorf("auth settings response is missing required field %q", name)
		}
	}
	return fields, nil
}

func validateAuthSettingsResponse(path string, body []byte) error {
	switch {
	case strings.HasSuffix(path, "/auth/webhooks"):
		_, err := authResponseObject(body, "enabled")
		return err
	case strings.HasSuffix(path, "/auth/email_provider"):
		fields, err := authResponseObject(body, "type")
		if err != nil {
			return err
		}
		var kind string
		if err := json.Unmarshal(fields["type"], &kind); err != nil || (kind != "standard" && kind != "shared") {
			return fmt.Errorf("invalid auth email provider response type")
		}
		if kind == "standard" {
			_, err = authResponseObject(body, "host", "port", "username", "password", "sender_email", "sender_name")
			return err
		}
	case strings.HasSuffix(path, "/auth/plugins"):
		fields, err := authResponseObject(body, "magic_link")
		if err != nil {
			return err
		}
		_, err = authResponseObject(fields["magic_link"], "enabled", "expires_in", "disable_sign_up")
		return err
	case strings.HasSuffix(path, "/auth/email_and_password"):
		_, err := authResponseObject(body, "enabled", "email_verification_method", "require_email_verification", "auto_sign_in_after_verification", "send_verification_email_on_sign_up", "send_verification_email_on_sign_in", "disable_sign_up")
		return err
	case strings.HasSuffix(path, "/auth/allow_localhost"):
		_, err := authResponseObject(body, "allow_localhost")
		return err
	case strings.HasSuffix(path, "/auth/domains"):
		fields, err := authResponseObject(body, "domains")
		if err != nil {
			return err
		}
		var domains []json.RawMessage
		if err := json.Unmarshal(fields["domains"], &domains); err != nil {
			return fmt.Errorf("invalid auth trusted domains response array")
		}
		for _, domain := range domains {
			if _, err := authResponseObject(domain, "domain", "auth_provider"); err != nil {
				return err
			}
		}
	}
	return nil
}

func (c *neonClient) scopedAuthSettingsSDK(ctx context.Context) (*neon.Client, func(), error) {
	scoped := *c
	httpClient := c.sdkCfg.HTTPClient
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 2 * time.Minute}
	}
	scoped.sdkCfg.HTTPClient = authSettingsHTTPClient{client: httpClient}
	return scoped.scopedSDK(ctx)
}

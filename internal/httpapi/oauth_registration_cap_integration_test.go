package httpapi_test

import (
	"context"
	"strings"
	"testing"

	"github.com/ncecere/open-mcp-portal/internal/config"
)

// OAUTH_MAX_REGISTERED_CLIENTS reaches POST /oauth/register: over the cap the
// answer is 503 temporarily_unavailable (an OAuth error body, never cached, no
// Retry-After); the registrations already issued are untouched.
func TestOAuthRegistrationCapAnswers503(t *testing.T) {
	app := newTestApp(t, func(c *config.Config) { c.OAuthMaxRegisteredClients = 2 })
	body := map[string]any{"client_name": "App", "redirect_uris": []string{testRedirect}}
	for i := 1; i <= 2; i++ {
		if status, out, _ := jsonPost(t, app, "/oauth/register", body); status != 201 || out["client_id"] == "" {
			t.Fatalf("registration %d = %d %v", i, status, out)
		}
	}
	status, out, hdr := jsonPost(t, app, "/oauth/register", body)
	desc, _ := out["error_description"].(string)
	if status != 503 || out["error"] != "temporarily_unavailable" || !strings.Contains(desc, "limit of registered apps") ||
		hdr.Get("Cache-Control") != "no-store" || hdr.Get("Retry-After") != "" || !strings.HasPrefix(hdr.Get("Content-Type"), "application/json") {
		t.Fatalf("over the cap = %d %v %v", status, out, hdr)
	}
	// Bad metadata is still a 400 of its own kind, not the cap's.
	if status, out, _ := jsonPost(t, app, "/oauth/register", map[string]any{"redirect_uris": []string{"http://evil.example/cb"}}); status != 400 || out["error"] != "invalid_redirect_uri" {
		t.Fatalf("bad metadata over the cap = %d %v", status, out)
	}
	var n int
	if err := app.Pool.QueryRow(context.Background(), `SELECT count(*) FROM oauth_clients`).Scan(&n); err != nil || n != 2 {
		t.Fatalf("%d clients stored (%v)", n, err)
	}
}

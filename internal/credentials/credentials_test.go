package credentials

import (
	"strings"
	"testing"

	"github.com/ncecere/open-mcp-portal/internal/apikeys"
	"github.com/ncecere/open-mcp-portal/internal/serviceaccounts"
)

func TestClassify(t *testing.T) {
	body := strings.Repeat("A", TokenBodyLen)
	cases := []struct {
		in   string
		want Kind
	}{
		{"ompadm_" + body, PlatformToken},
		{"omppub_" + body, PublisherToken},
		{"omp_" + body, MCPCredential},
		{"omp_sa_" + body, MCPCredential},
		{"ompat_" + body, MCPCredential},
		{"omprt_" + body, MCPCredential},
		{"ompac_" + body, MCPCredential},
		{"ompadm_" + body[1:], Unknown},       // too short
		{"ompadm_" + body + "A", Unknown},     // too long
		{"omppub_" + body[1:] + "!", Unknown}, // not URL-safe
		{"ompadm_", Unknown},
		{"Bearer ompadm_" + body, Unknown},
		{"", Unknown},
		{"ghp_" + body, Unknown},
		{"omcp_" + body, Unknown}, // the pre-rename prefix no longer authenticates anything
	}
	for _, c := range cases {
		if got := Classify(c.in); got != c.want {
			t.Errorf("Classify(%.20q) = %d, want %d", c.in, got, c.want)
		}
	}
	if !PlatformToken.IsAutomationToken() || !PublisherToken.IsAutomationToken() || MCPCredential.IsAutomationToken() || Unknown.IsAutomationToken() {
		t.Error("IsAutomationToken")
	}
}

// Automation tokens are never MCP keys: /mcp's key checks refuse both prefixes
// before any lookup.
func TestAutomationTokensAreNotMCPKeys(t *testing.T) {
	body := strings.Repeat("B", TokenBodyLen)
	for _, tok := range []string{"ompadm_" + body, "omppub_" + body} {
		if apikeys.LooksLikeKey(tok) || serviceaccounts.LooksLikeKey(tok) {
			t.Errorf("%.12s... looks like an MCP key", tok)
		}
	}
	if !apikeys.LooksLikeKey("omp_" + body) {
		t.Error("a personal key stopped looking like one")
	}
}

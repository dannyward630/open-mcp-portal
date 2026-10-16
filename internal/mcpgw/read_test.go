package mcpgw

import (
	"testing"

	"github.com/ncecere/open-mcp-gateway/internal/gateways"
	"github.com/ncecere/open-mcp-gateway/internal/store/dbgen"
)

func offer(kind, server, name string, allowed bool, denied string) gateways.Offer {
	return gateways.Offer{Selected: gateways.Selected{Capability: dbgen.McpCapability{Kind: kind, Name: name},
		Server: dbgen.McpServer{Slug: server}}, Allowed: allowed, Denied: denied}
}

func TestMatchRead(t *testing.T) {
	offers := []gateways.Offer{
		offer("resource", "status", "status://services", true, ""),
		offer("resource_template", "status", "status://services/{service}", true, ""),
		offer("resource", "other", "status://services", false, "role"),
		offer("tool", "status", "status://services/x", true, ""),
	}
	cases := []struct {
		uri, original, denied string
		allowed, matched      bool
	}{
		{"omcp://status/status://services", "status://services", "", true, true},
		{"omcp://status/status://services/email", "status://services/email", "", true, true},
		{"omcp://status/status://services/a/b", "", "", false, false},
		{"omcp://status/status://other", "", "", false, false},
		{"omcp://other/status://services", "status://services", "role", false, true},
		{"omcp://nope/status://services", "", "", false, false},
		{"status://services", "", "", false, false},
		{"omcp://status/status://services/..", "", "", false, false},
	}
	for _, c := range cases {
		got, ok := matchRead(offers, c.uri)
		if ok != c.allowed || (got != nil) != c.matched {
			t.Errorf("%s: %+v %v", c.uri, got, ok)
			continue
		}
		if got != nil && (got.original != c.original || got.offer.Denied != c.denied) {
			t.Errorf("%s: original %q denied %q", c.uri, got.original, got.offer.Denied)
		}
	}
}

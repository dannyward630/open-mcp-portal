package registry

import (
	"testing"

	"github.com/google/uuid"

	"github.com/ncecere/open-mcp-portal/internal/store/dbgen"
)

func TestOriginOf(t *testing.T) {
	for _, c := range []struct {
		a, b string
		same bool
	}{
		// The path, a trailing slash, case and an explicit default port are not another origin.
		{"https://api.example.com/mcp", "https://api.example.com/mcp/", true},
		{"https://api.example.com/mcp", "https://api.example.com/v2/other", true},
		{"https://api.example.com/mcp", "HTTPS://API.Example.COM/mcp", true},
		{"https://api.example.com/mcp", "https://api.example.com:443/mcp", true},
		{"https://api.example.com/mcp", "https://api.example.com./mcp", true},
		{"http://127.0.0.1:8091/mcp", "http://127.0.0.1:8091/mcp/", true},
		{"http://localhost/mcp", "http://localhost:80/mcp", true},
		{"https://[2001:db8::1]/mcp", "https://[2001:DB8::1]:443/x", true},
		// Another scheme, host or port is.
		{"https://api.example.com/mcp", "http://api.example.com/mcp", false},
		{"https://api.example.com/mcp", "https://api.example.org/mcp", false},
		{"https://api.example.com/mcp", "https://evil.example/mcp", false},
		{"https://api.example.com/mcp", "https://api.example.com:8443/mcp", false},
		{"https://api.example.com/mcp", "https://api.example.com.evil.example/mcp", false},
		{"https://api.example.com/mcp", "https://sub.api.example.com/mcp", false},
		{"http://127.0.0.1:8091/mcp", "http://127.0.0.1:8092/mcp", false},
		{"https://[2001:db8::1]/mcp", "https://[2001:db8::2]/mcp", false},
	} {
		if got := sameOrigin(c.a, c.b); got != c.same {
			t.Errorf("sameOrigin(%q, %q) = %v, want %v (%q vs %q)", c.a, c.b, got, c.same, originOf(c.a), originOf(c.b))
		}
	}
	if originOf("https://Api.Example.com:443/mcp") != "https://api.example.com" || originOf("http://h:8080/x") != "http://h:8080" {
		t.Errorf("origin spelling: %q %q", originOf("https://Api.Example.com:443/mcp"), originOf("http://h:8080/x"))
	}
}

// F-3: a grant to receive a person's provider token survives a path-only
// edit and nothing else about the URL or the requirement's provider.
func TestGrantResetReason(t *testing.T) {
	p1, p2 := uuid.NullUUID{UUID: uuid.New(), Valid: true}, uuid.NullUUID{UUID: uuid.New(), Valid: true}
	sv := func(url string, p uuid.NullUUID) dbgen.McpServer { return dbgen.McpServer{URL: url, AuthProviderID: p} }
	for _, c := range []struct {
		name      string
		cur, next dbgen.McpServer
		want      string
	}{
		{"same", sv("https://a.example/mcp", p1), sv("https://a.example/mcp", p1), ""},
		{"path only", sv("https://a.example/mcp", p1), sv("https://A.example:443/v2/mcp/", p1), ""},
		{"host", sv("https://a.example/mcp", p1), sv("https://b.example/mcp", p1), "origin_changed"},
		{"port", sv("https://a.example/mcp", p1), sv("https://a.example:8443/mcp", p1), "origin_changed"},
		{"scheme", sv("https://a.example/mcp", p1), sv("http://a.example/mcp", p1), "origin_changed"},
		{"other provider", sv("https://a.example/mcp", p1), sv("https://a.example/mcp", p2), "provider_changed"},
		{"requirement removed", sv("https://a.example/mcp", p1), sv("https://a.example/mcp", uuid.NullUUID{}), "provider_changed"},
	} {
		if got := grantResetReason(c.cur, c.next); got != c.want {
			t.Errorf("%s: %q, want %q", c.name, got, c.want)
		}
	}
}

package registry

import "testing"

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

package upstream

import (
	"context"
	"errors"
	"net/netip"
	"strings"
	"testing"

	"github.com/ncecere/open-mcp-gateway/internal/apperr"
)

func TestNormalizeURL(t *testing.T) {
	for _, c := range []struct {
		in           string
		allowPrivate bool
		reason       string // "" = accepted
	}{
		{"https://status.example.com/mcp", false, ""},
		{"  https://status.example.com/mcp ", false, ""},
		{"http://status.example.com/mcp", false, ReasonScheme}, // https only
		{"http://status.example.com/mcp", true, ReasonScheme},  // http only for loopback
		{"http://127.0.0.1:9000/mcp", false, ReasonScheme},     // http and loopback refused
		{"https://127.0.0.1:9000/mcp", false, ReasonPrivateAddress},
		{"http://127.0.0.1:9000/mcp", true, ""},                         // the development allowance
		{"http://localhost:9000/mcp", true, ""},                         // loopback by name
		{"http://[::1]:9000/mcp", true, ""},                             // IPv6 loopback
		{"https://localhost/mcp", false, ReasonPrivateAddress},          // loopback by name refused
		{"https://api.localhost/mcp", false, ReasonPrivateAddress},      // *.localhost
		{"https://10.1.2.3/mcp", false, ReasonPrivateAddress},           // private
		{"https://192.168.1.10/mcp", false, ReasonPrivateAddress},       // private
		{"https://169.254.169.254/latest", false, ReasonPrivateAddress}, // link-local (metadata)
		{"https://100.64.0.1/mcp", false, ReasonPrivateAddress},         // CGNAT
		{"https://[fe80::1]/mcp", false, ReasonPrivateAddress},          // IPv6 link-local
		{"https://[::ffff:10.0.0.1]/mcp", false, ReasonPrivateAddress},  // IPv4-mapped private
		{"https://0177.0.0.1/mcp", false, ReasonPrivateAddress},         // octal notation
		{"https://0x7f.1/mcp", false, ReasonPrivateAddress},             // hex notation
		{"https://2130706433/mcp", false, ReasonPrivateAddress},         // decimal notation
		{"https://10.1.2.3/mcp", true, ""},                              // private with the allowance
		{"https://8.8.8.8/mcp", false, ""},                              // a public literal
		{"https://user:pw@status.example.com/mcp", false, ReasonUserInfo},
		{"https://status.example.com/mcp?key=secret", false, ReasonQuery},
		{"https://status.example.com/mcp?", false, ReasonQuery},
		{"https://status.example.com/mcp#x", false, ReasonFragment},
		{"ftp://status.example.com/mcp", false, ReasonScheme},
		{"status.example.com/mcp", false, ReasonInvalid},
		{"https://", false, ReasonInvalid},
		{"https://" + strings.Repeat("a", 500) + ".example.com/", false, ReasonTooLong},
	} {
		_, err := NormalizeURL(c.in, c.allowPrivate)
		if c.reason == "" {
			if err != nil {
				t.Errorf("NormalizeURL(%.60q, %v) = %v, want ok", c.in, c.allowPrivate, err)
			}
			continue
		}
		e, ok := apperr.As(err)
		if !ok || e.Status != 422 || e.Code != "url_not_allowed" {
			t.Errorf("NormalizeURL(%.60q, %v) = %v, want 422 url_not_allowed", c.in, c.allowPrivate, err)
			continue
		}
		if got := e.Details.(map[string]any)["reason"]; got != c.reason {
			t.Errorf("NormalizeURL(%.60q, %v) reason = %v, want %s", c.in, c.allowPrivate, got, c.reason)
		}
	}
}

func TestPublicAddr(t *testing.T) {
	for addr, want := range map[string]bool{
		"8.8.8.8": true, "1.1.1.1": true, "2606:4700::1111": true,
		"10.0.0.1": false, "172.16.5.4": false, "192.168.0.1": false, "127.0.0.1": false, "169.254.1.1": false,
		"100.64.1.1": false, "0.0.0.0": false, "224.0.0.1": false, "::1": false, "fe80::1": false, "fc00::1": false,
		"2001:db8::1": false, "::ffff:127.0.0.1": false, "168.63.129.16": false,
	} {
		if got := PublicAddr(netip.MustParseAddr(addr)); got != want {
			t.Errorf("PublicAddr(%s) = %v, want %v", addr, got, want)
		}
	}
}

// The dialer refuses a host that resolves to a private address (a DNS
// change can't point a registered server inside), unless allowed, and
// dials only the addresses it checked.
func TestDialerRefusesPrivateResolution(t *testing.T) {
	d := &dialer{lookup: func(context.Context, string) ([]netip.Addr, error) {
		return []netip.Addr{netip.MustParseAddr("203.0.114.1"), netip.MustParseAddr("10.0.0.5")}, nil
	}}
	if _, err := d.dial(context.Background(), "tcp", "status.example.com:443"); !errors.Is(err, ErrBlockedAddress) {
		t.Fatalf("mixed answer: %v", err)
	}
	if _, err := d.dial(context.Background(), "tcp", "127.0.0.1:1"); !errors.Is(err, ErrBlockedAddress) {
		t.Fatalf("loopback literal: %v", err)
	}
	none := &dialer{lookup: func(context.Context, string) ([]netip.Addr, error) { return nil, nil }}
	if _, err := none.dial(context.Background(), "tcp", "status.example.com:443"); err == nil || !strings.Contains(err.Error(), "no addresses") {
		t.Fatalf("no addresses: %v", err)
	}
}

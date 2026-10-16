package upstream

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/ncecere/open-mcp-gateway/internal/apperr"
	"github.com/ncecere/open-mcp-gateway/internal/netpolicy"
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
		{"https://127.0.0.1:9000/mcp", false, ReasonBlockedAddress},
		{"http://127.0.0.1:9000/mcp", true, ""},                         // the development allowance
		{"http://localhost:9000/mcp", true, ""},                         // loopback by name
		{"http://[::1]:9000/mcp", true, ""},                             // IPv6 loopback
		{"https://localhost/mcp", false, ReasonBlockedAddress},          // loopback by name refused
		{"https://api.localhost/mcp", false, ReasonBlockedAddress},      // *.localhost
		{"https://10.1.2.3/mcp", false, ReasonPrivateAddress},           // private
		{"https://192.168.1.10/mcp", false, ReasonPrivateAddress},       // private
		{"https://169.254.169.254/latest", false, ReasonBlockedAddress}, // link-local (metadata)
		{"https://100.64.0.1/mcp", false, ReasonPrivateAddress},         // CGNAT
		{"https://[fe80::1]/mcp", false, ReasonBlockedAddress},          // IPv6 link-local
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

// testPolicy: 10.20.0.0/16 (http allowed), fd00:1234::/32, the host
// mcp.corp.example.com (http allowed) and *.svc.cluster.local.
func testPolicy() *netpolicy.Policy {
	return netpolicy.New([]netpolicy.Entry{
		{Kind: netpolicy.KindCIDR, Value: "10.20.0.0/16", AllowHTTP: true},
		{Kind: netpolicy.KindCIDR, Value: "fd00:1234::/32"},
		{Kind: netpolicy.KindHost, Value: "mcp.corp.example.com", AllowHTTP: true},
		{Kind: netpolicy.KindHost, Value: "*.svc.cluster.local"},
	})
}

func TestNormalizeURLWithPolicy(t *testing.T) {
	p := testPolicy()
	for _, c := range []struct {
		in     string
		reason string // "" = accepted
	}{
		{"https://status.example.com/mcp", ""},
		{"https://10.20.0.5/mcp", ""},           // a literal inside a cidr entry
		{"http://10.20.0.5:8080/mcp", ""},       // ...that allows http
		{"https://[::ffff:10.20.0.5]/mcp", ""},  // its IPv4-mapped form
		{"http://[::ffff:10.20.0.5]/mcp", ""},   // http through the mapped form too
		{"https://[fd00:1234::5]:8443/mcp", ""}, // IPv6 cidr entry
		{"http://[fd00:1234::5]/mcp", ReasonScheme},
		{"https://10.21.0.5/mcp", ReasonPrivateAddress},
		{"http://10.21.0.5/mcp", ReasonScheme},
		{"http://mcp.corp.example.com/mcp", ""}, // a host entry that allows http
		{"http://MCP.Corp.Example.com.:8080/mcp", ""},
		{"http://api.mcp.corp.example.com/mcp", ReasonScheme}, // an exact entry doesn't cover subdomains
		{"https://api.svc.cluster.local/mcp", ""},
		{"http://api.svc.cluster.local/mcp", ReasonScheme}, // no allowHttp
		{"http://svc.cluster.local/mcp", ReasonScheme},
		{"https://anything.example.net/mcp", ""}, // names are resolved when dialling
		{"http://status.example.com/mcp", ReasonScheme},
		{"https://127.0.0.1/mcp", ReasonBlockedAddress},
		{"https://169.254.169.254/latest", ReasonBlockedAddress},
		{"https://[::ffff:169.254.169.254]/latest", ReasonBlockedAddress},
		{"https://[fe80::1%25eth0]/mcp", ReasonBlockedAddress},
		{"https://0.0.0.0/mcp", ReasonBlockedAddress},
		{"https://localhost/mcp", ReasonBlockedAddress},
		{"https://localhost./mcp", ReasonBlockedAddress},
		{"https://0x0a.20.0.5/mcp", ReasonPrivateAddress}, // other notations are never allowed
	} {
		_, err := NormalizeURLWith(c.in, false, p)
		if c.reason == "" {
			if err != nil {
				t.Errorf("NormalizeURLWith(%q) = %v, want ok", c.in, err)
			}
			continue
		}
		e, ok := apperr.As(err)
		if !ok || e.Code != "url_not_allowed" {
			t.Errorf("NormalizeURLWith(%q) = %v, want url_not_allowed", c.in, err)
			continue
		}
		if got := e.Details.(map[string]any)["reason"]; got != c.reason {
			t.Errorf("NormalizeURLWith(%q) reason = %v, want %s", c.in, got, c.reason)
		}
	}
	_, err := NormalizeURLWith("https://10.21.0.5/mcp", false, p)
	if e, _ := apperr.As(err); !strings.Contains(e.Message, "platform admin") {
		t.Errorf("private_address message = %q", e.Message)
	}
}

// fakeResolver answers from a table.
func fakeResolver(table map[string][]string) resolver {
	return func(_ context.Context, host string) ([]netip.Addr, error) {
		addrs, ok := table[strings.TrimSuffix(strings.ToLower(host), ".")]
		if !ok {
			return nil, errors.New("no such host")
		}
		out := make([]netip.Addr, len(addrs))
		for i, a := range addrs {
			out[i] = netip.MustParseAddr(a)
		}
		return out, nil
	}
}

// errDialled marks a dial the policy approved (the test never connects).
var errDialled = errors.New("dialled")

// The dialer applies the policy to every resolved address, with pinning:
// a host entry never lets DNS point at blocked addresses.
func TestDialerPolicy(t *testing.T) {
	var dialled []string
	d := &dialer{
		policy: func(context.Context) *netpolicy.Policy { return testPolicy() },
		lookup: fakeResolver(map[string][]string{
			"mcp.corp.example.com":     {"192.168.7.7"},                  // a host entry: any private address
			"rebind.svc.cluster.local": {"169.254.169.254"},              // a host entry, but metadata
			"mixed.svc.cluster.local":  {"10.9.9.9", "127.0.0.1"},        // one blocked address refuses all
			"build.example.com":        {"10.20.3.4"},                    // a name resolving into a cidr entry
			"build6.example.com":       {"fd00:1234::7", "2606:4700::1"}, // IPv6 cidr entry plus public
			"outside.example.com":      {"10.21.0.1"},                    // private, no entry
			"mapped.example.com":       {"::ffff:169.254.169.254"},       // mapped metadata
			"public.example.com":       {"8.8.8.8"},
		}),
		connect: func(_ context.Context, _, address string) (net.Conn, error) {
			dialled = append(dialled, address)
			return nil, errDialled
		},
	}
	for _, c := range []struct {
		address string
		allowed bool
		pinned  string // the address dialled first
	}{
		{"mcp.corp.example.com:443", true, "192.168.7.7:443"},
		{"MCP.CORP.EXAMPLE.COM.:443", true, "192.168.7.7:443"},
		{"rebind.svc.cluster.local:443", false, ""},
		{"mixed.svc.cluster.local:443", false, ""},
		{"build.example.com:443", true, "10.20.3.4:443"},
		{"build6.example.com:443", true, "[fd00:1234::7]:443"},
		{"outside.example.com:443", false, ""},
		{"mapped.example.com:443", false, ""},
		{"public.example.com:443", true, "8.8.8.8:443"},
		{"10.20.0.9:8080", true, "10.20.0.9:8080"},
		{"[::ffff:10.20.0.9]:8080", true, "10.20.0.9:8080"}, // dialled unmapped
		{"10.21.0.9:8080", false, ""},
		{"127.0.0.1:1", false, ""},
		{"[fe80::1]:443", false, ""},
	} {
		dialled = nil
		_, err := d.dial(context.Background(), "tcp", c.address)
		if c.allowed {
			if !errors.Is(err, errDialled) || len(dialled) == 0 || dialled[0] != c.pinned {
				t.Errorf("dial %s = %v %v, want a dial to %s", c.address, err, dialled, c.pinned)
			}
			continue
		}
		if !errors.Is(err, ErrBlockedAddress) || len(dialled) != 0 {
			t.Errorf("dial %s = %v %v, want refused without dialling", c.address, err, dialled)
		}
	}
	// The policy is read at every dial: an entry removed applies at once.
	empty := *d
	empty.policy = func(context.Context) *netpolicy.Policy { return nil }
	if _, err := empty.dial(context.Background(), "tcp", "mcp.corp.example.com:443"); !errors.Is(err, ErrBlockedAddress) {
		t.Errorf("after removal = %v", err)
	}
}

// Check resolves names (never connecting) and reports every address.
func TestCheck(t *testing.T) {
	c := &Client{Networks: staticPolicy{testPolicy()}, Lookup: fakeResolver(map[string][]string{
		"mcp.corp.example.com":     {"192.168.7.7"},
		"rebind.svc.cluster.local": {"10.1.1.1", "169.254.169.254"},
		"build.example.com":        {"10.20.3.4", "8.8.8.8"},
		"outside.example.com":      {"10.21.0.1"},
		"public.example.com":       {"8.8.8.8"},
	})}
	type addr = AddressCheck
	for _, tc := range []struct {
		url     string
		allowed bool
		reason  string
		matched string
		addrs   []addr
	}{
		{"http://mcp.corp.example.com/mcp", true, "host_entry", "mcp.corp.example.com", []addr{{"192.168.7.7", true, "host_entry"}}},
		{"https://rebind.svc.cluster.local/mcp", false, "blocked_address", "",
			[]addr{{"10.1.1.1", true, "host_entry"}, {"169.254.169.254", false, "blocked_address"}}},
		{"https://build.example.com/mcp", true, "cidr_entry", "10.20.0.0/16", []addr{{"10.20.3.4", true, "cidr_entry"}, {"8.8.8.8", true, "public"}}},
		{"https://outside.example.com/mcp", false, "private_address", "", []addr{{"10.21.0.1", false, "private_address"}}},
		{"https://public.example.com/mcp", true, "public", "", []addr{{"8.8.8.8", true, "public"}}},
		{"http://10.20.0.5/mcp", true, "cidr_entry", "10.20.0.0/16", []addr{{"10.20.0.5", true, "cidr_entry"}}},
		{"https://10.21.0.5/mcp", false, "private_address", "", []addr{{"10.21.0.5", false, "private_address"}}},
		{"https://169.254.169.254/", false, "blocked_address", "", []addr{{"169.254.169.254", false, "blocked_address"}}},
		{"http://public.example.com/mcp", false, "scheme", "", nil},
		{"https://unknown.example.com/mcp", false, "dns_failed", "", nil},
		{"https://u:p@public.example.com/mcp", false, "userinfo", "", nil},
		{"nope", false, "invalid", "", nil},
	} {
		r := c.Check(context.Background(), tc.url)
		matched := ""
		if r.Matched != nil {
			matched = r.Matched.Value
		}
		if r.Allowed != tc.allowed || r.Reason != tc.reason || matched != tc.matched || r.Message == "" || len(r.Addresses) != len(tc.addrs) {
			t.Errorf("Check(%s) = %+v (matched %q)", tc.url, r, matched)
			continue
		}
		for i := range tc.addrs {
			if r.Addresses[i] != tc.addrs[i] {
				t.Errorf("Check(%s) address %d = %+v, want %+v", tc.url, i, r.Addresses[i], tc.addrs[i])
			}
		}
	}
	dev := &Client{AllowPrivate: true, Lookup: c.Lookup}
	if r := dev.Check(context.Background(), "https://outside.example.com/mcp"); !r.Allowed || r.Reason != "development" {
		t.Errorf("development = %+v", r)
	}
}

type staticPolicy struct{ p *netpolicy.Policy }

func (s staticPolicy) Current(context.Context) *netpolicy.Policy { return s.p }

// A client refuses a URL the policy no longer allows before connecting.
func TestClientRefusesURLOutsidePolicy(t *testing.T) {
	c := &Client{Networks: staticPolicy{nil}, Log: testClient(false).Log}
	_, err := c.Discover(context.Background(), Target{URL: "http://mcp.corp.example.com/mcp", Timeout: time.Second})
	var e *Error
	if !errors.As(err, &e) || e.Class != ClassNotAllowed {
		t.Fatalf("Discover = %v", err)
	}
}

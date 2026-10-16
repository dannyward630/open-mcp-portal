package netpolicy

import (
	"net/netip"
	"strings"
	"testing"
)

func TestCanonical(t *testing.T) {
	for _, c := range []struct {
		kind, value string
		want        string // canonical value ("" when refused)
		problem     string
	}{
		{KindCIDR, "10.20.0.0/16", "10.20.0.0/16", ""},
		{KindCIDR, " 10.20.3.4/16 ", "10.20.0.0/16", ""}, // masked
		{KindCIDR, "10.20.0.5", "10.20.0.5/32", ""},      // a bare address
		{KindCIDR, "fd00:1234::/32", "fd00:1234::/32", ""},
		{KindCIDR, "FD00:1234:0:0::1/32", "fd00:1234::/32", ""},
		{KindCIDR, "fd00::5", "fd00::5/128", ""},
		{KindCIDR, "::ffff:10.0.0.0/104", "10.0.0.0/8", ""}, // IPv4-mapped becomes IPv4
		{KindCIDR, "::ffff:0:0/80", "", ProblemCIDR},        // mapped but shorter than /96
		{KindCIDR, "0.0.0.0/0", "0.0.0.0/0", ""},            // broad, but not only blocked addresses
		{KindCIDR, "10.0.0.0/33", "", ProblemCIDR},
		{KindCIDR, "10.0.0.0/", "", ProblemCIDR},
		{KindCIDR, "corp.example.com", "", ProblemCIDR},
		{KindCIDR, "fe80::1%eth0/64", "", ProblemCIDR}, // zones
		{KindCIDR, "", "", ProblemCIDR},
		{KindCIDR, "127.0.0.0/8", "", ProblemAlwaysBlocked},
		{KindCIDR, "127.1.2.3/24", "", ProblemAlwaysBlocked},
		{KindCIDR, "169.254.169.254", "", ProblemAlwaysBlocked},
		{KindCIDR, "::ffff:127.0.0.1", "", ProblemAlwaysBlocked},
		{KindCIDR, "::1", "", ProblemAlwaysBlocked},
		{KindCIDR, "fe80::/10", "", ProblemAlwaysBlocked},
		{KindCIDR, "fe80::/9", "fe80::/9", ""}, // wider than link-local: fec0::/10 is not blocked
		{KindCIDR, "224.0.0.0/4", "", ProblemAlwaysBlocked},
		{KindCIDR, "ff02::/16", "", ProblemAlwaysBlocked},
		{KindCIDR, "255.255.255.255", "", ProblemAlwaysBlocked},
		{KindCIDR, "0.0.0.0/8", "", ProblemAlwaysBlocked},
		{KindHost, "mcp.corp.example.com", "mcp.corp.example.com", ""},
		{KindHost, "MCP.Corp.Example.COM.", "mcp.corp.example.com", ""}, // case and trailing dot
		{KindHost, "*.corp.example.com", "*.corp.example.com", ""},
		{KindHost, "*.Svc.Cluster.Local.", "*.svc.cluster.local", ""},
		{KindHost, "xn--bcher-kva.example", "xn--bcher-kva.example", ""},
		{KindHost, "internal", "internal", ""},
		{KindHost, "bücher.example", "", ProblemNonASCII},
		{KindHost, "*", "", ProblemHost},
		{KindHost, "*.", "", ProblemHost},
		{KindHost, "a.*.example.com", "", ProblemHost},
		{KindHost, "*example.com", "", ProblemHost},
		{KindHost, "**.example.com", "", ProblemHost},
		{KindHost, "mcp.corp.example.com:8443", "", ProblemHost},
		{KindHost, "https://mcp.corp.example.com", "", ProblemHost},
		{KindHost, "mcp.corp.example.com/mcp", "", ProblemHost},
		{KindHost, "under_score.example.com", "", ProblemHost},
		{KindHost, "-bad.example.com", "", ProblemHost},
		{KindHost, "a..example.com", "", ProblemHost},
		{KindHost, strings.Repeat("a", 64) + ".example.com", "", ProblemHost},
		{KindHost, "10.0.0.1", "", ProblemIPAsHost},
		{KindHost, "0177.0.0.1", "", ProblemIPAsHost},
		{KindHost, "*.10.0.0", "", ProblemIPAsHost},
		{KindHost, "localhost", "", ProblemAlwaysBlocked},
		{KindHost, "*.localhost", "", ProblemAlwaysBlocked},
		{KindHost, "api.localhost", "", ProblemAlwaysBlocked},
		{"dns", "x", "", ProblemKind},
	} {
		got, problem := Canonical(Entry{Kind: c.kind, Value: c.value})
		switch {
		case c.problem == "" && problem != nil:
			t.Errorf("Canonical(%s %q) = %v, want %q", c.kind, c.value, problem, c.want)
		case c.problem == "" && got.Value != c.want:
			t.Errorf("Canonical(%s %q) = %q, want %q", c.kind, c.value, got.Value, c.want)
		case c.problem != "" && (problem == nil || problem.Reason != c.problem):
			t.Errorf("Canonical(%s %q) = %+v %v, want %s", c.kind, c.value, got, problem, c.problem)
		}
	}
}

func TestCanonicalNote(t *testing.T) {
	got, problem := Canonical(Entry{Kind: KindHost, Value: "a.example.com", Note: "  build farm  "})
	if problem != nil || got.Note != "build farm" {
		t.Fatalf("note = %q %v", got.Note, problem)
	}
	if _, problem := Canonical(Entry{Kind: KindHost, Value: "a.example.com", Note: strings.Repeat("é", MaxNote)}); problem != nil {
		t.Fatalf("200 characters refused: %v", problem)
	}
	for _, note := range []string{strings.Repeat("n", MaxNote+1), "two\nlines"} {
		if _, problem := Canonical(Entry{Kind: KindHost, Value: "a.example.com", Note: note}); problem == nil || problem.Reason != ProblemNote {
			t.Errorf("note %.20q accepted", note)
		}
	}
}

func TestValidate(t *testing.T) {
	got, problem := Validate([]Entry{{Kind: KindCIDR, Value: "10.1.2.3/16"}, {Kind: KindHost, Value: "A.example.com", AllowHTTP: true}})
	if problem != nil || got[0].Value != "10.1.0.0/16" || got[1] != (Entry{Kind: KindHost, Value: "a.example.com", AllowHTTP: true}) {
		t.Fatalf("Validate = %+v %v", got, problem)
	}
	_, problem = Validate([]Entry{{Kind: KindCIDR, Value: "10.1.0.0/16"}, {Kind: KindCIDR, Value: "10.1.9.9/16"}})
	if problem == nil || problem.Index != 1 || problem.Reason != ProblemDuplicate {
		t.Fatalf("duplicate = %v", problem)
	}
	// The same value as another kind is not a duplicate (and can't be: hosts aren't addresses).
	_, problem = Validate([]Entry{{Kind: KindHost, Value: "ok.example.com"}, {Kind: KindCIDR, Value: "bad"}})
	if problem == nil || problem.Index != 1 || problem.Reason != ProblemCIDR {
		t.Fatalf("second invalid = %v", problem)
	}
	many := make([]Entry, MaxEntries+1)
	for i := range many {
		many[i] = Entry{Kind: KindCIDR, Value: netip.AddrFrom4([4]byte{10, byte(i), 0, 0}).String() + "/16"}
	}
	if _, problem := Validate(many[:MaxEntries]); problem != nil {
		t.Fatalf("%d entries: %v", MaxEntries, problem)
	}
	if _, problem := Validate(many); problem == nil || problem.Reason != ProblemTooMany || problem.Index != MaxEntries {
		t.Fatalf("too many = %v", problem)
	}
}

func TestParseList(t *testing.T) {
	got, err := ParseList(" 10.20.0.0/16, http:*.svc.cluster.local ,mcp.corp.example.com,HTTP:10.30.1.1,fd00::/8,,http:fd01::/16 ")
	if err != nil {
		t.Fatal(err)
	}
	want := []Entry{
		{Kind: KindCIDR, Value: "10.20.0.0/16"},
		{Kind: KindHost, Value: "*.svc.cluster.local", AllowHTTP: true},
		{Kind: KindHost, Value: "mcp.corp.example.com"},
		{Kind: KindCIDR, Value: "10.30.1.1/32", AllowHTTP: true},
		{Kind: KindCIDR, Value: "fd00::/8"},
		{Kind: KindCIDR, Value: "fd01::/16", AllowHTTP: true},
	}
	if len(got) != len(want) {
		t.Fatalf("ParseList = %+v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("entry %d = %+v, want %+v", i, got[i], want[i])
		}
	}
	if FormatList(got) != "10.20.0.0/16,http:*.svc.cluster.local,mcp.corp.example.com,http:10.30.1.1/32,fd00::/8,http:fd01::/16" {
		t.Errorf("FormatList = %s", FormatList(got))
	}
	if got, err := ParseList(""); err != nil || len(got) != 0 {
		t.Errorf("empty = %v %v", got, err)
	}
	for _, bad := range []string{"10.0.0.0/33", "127.0.0.0/8", "https://mcp.example.com", "mcp.example.com:443", "bücher.example", "http:", "*"} {
		if _, err := ParseList(bad); err == nil {
			t.Errorf("ParseList(%q) accepted", bad)
		}
	}
}

func TestClassify(t *testing.T) {
	for addr, want := range map[string]Class{
		"8.8.8.8": Public, "1.1.1.1": Public, "2606:4700::1111": Public, "::ffff:8.8.8.8": Public,
		// Always blocked.
		"127.0.0.1": Blocked, "127.255.0.1": Blocked, "::1": Blocked, "0.0.0.0": Blocked, "0.1.2.3": Blocked, "::": Blocked,
		"169.254.169.254": Blocked, "169.254.1.1": Blocked, "fe80::1": Blocked, "febf::1": Blocked,
		"224.0.0.1": Blocked, "239.1.1.1": Blocked, "ff02::1": Blocked, "ff0e::1": Blocked, "255.255.255.255": Blocked,
		"168.63.129.16": Blocked, "100.100.100.200": Blocked, "fd00:ec2::254": Blocked,
		"::ffff:127.0.0.1": Blocked, "::ffff:169.254.169.254": Blocked, "::ffff:0.0.0.0": Blocked, // IPv4-mapped
		"::127.0.0.1": Blocked, "::169.254.169.254": Blocked, // IPv4-compatible
		"64:ff9b::a9fe:a9fe": Blocked, "64:ff9b::7f00:1": Blocked, // NAT64 forms
		// Private: allowed only by an entry.
		"10.0.0.1": Private, "172.16.5.4": Private, "192.168.0.1": Private, "100.64.1.1": Private, "198.18.0.1": Private,
		"192.0.2.1": Private, "240.0.0.1": Private, "fc00::1": Private, "fd12:3456::1": Private, "2001:db8::1": Private,
		"fec0::1": Private, "::ffff:10.0.0.1": Private, "::10.0.0.1": Private, "64:ff9b::a00:1": Private,
	} {
		if got := Classify(netip.MustParseAddr(addr)); got != want {
			t.Errorf("Classify(%s) = %v, want %v", addr, got, want)
		}
	}
	if Classify(netip.Addr{}) != Blocked || Classify(netip.MustParseAddr("fd00::1%eth0")) != Blocked {
		t.Error("invalid or zoned addresses must be blocked")
	}
}

func TestHostMatches(t *testing.T) {
	for _, c := range []struct {
		pattern, host string
		want          bool
	}{
		{"*.corp.example.com", "mcp.corp.example.com", true},
		{"*.corp.example.com", "a.b.corp.example.com", true},
		{"*.corp.example.com", "MCP.Corp.Example.Com", true},  // case
		{"*.corp.example.com", "mcp.corp.example.com.", true}, // trailing dot
		{"*.corp.example.com", "corp.example.com", false},     // subdomains only
		{"*.corp.example.com", "corp.example.com.", false},
		{"*.corp.example.com", "evilcorp.example.com", false},
		{"*.corp.example.com", "mcp.corp.example.com.evil.com", false},
		{"*.corp.example.com", ".corp.example.com", false},
		{"*.corp.example.com", "mcp..corp.example.com", false},
		{"*.corp.example.com", "", false},
		{"mcp.corp.example.com", "mcp.corp.example.com", true},
		{"mcp.corp.example.com", "MCP.CORP.EXAMPLE.COM.", true},
		{"mcp.corp.example.com", "a.mcp.corp.example.com", false},
		{"mcp.corp.example.com", "mcp.corp.example.co", false},
		{"mcp.corp.example.com", "mcp.corp.example.com:443", false}, // a port is not part of the host
		{"mcp.corp.example.com", "mcp.corp.example.com..", false},
	} {
		if got := HostMatches(c.pattern, c.host); got != c.want {
			t.Errorf("HostMatches(%q, %q) = %v, want %v", c.pattern, c.host, got, c.want)
		}
	}
}

func TestCheckAddr(t *testing.T) {
	p := New([]Entry{
		{Kind: KindCIDR, Value: "10.20.0.0/16"},
		{Kind: KindCIDR, Value: "fd00:1234::/32", AllowHTTP: true},
		{Kind: KindHost, Value: "*.svc.cluster.local", AllowHTTP: true},
	}, []Entry{
		{Kind: KindHost, Value: "mcp.corp.example.com"},
		{Kind: KindCIDR, Value: "0.0.0.0/0"}, // even everything never allows blocked addresses
	})
	type want struct {
		allowed bool
		reason  string
		entry   string
	}
	for _, c := range []struct {
		host, addr string
		want       want
	}{
		{"8.8.8.8", "8.8.8.8", want{true, ReasonPublic, ""}},
		{"api.example.com", "8.8.8.8", want{true, ReasonPublic, ""}},
		{"10.20.3.4", "10.20.3.4", want{true, ReasonCIDREntry, "10.20.0.0/16"}},
		{"build.example.com", "10.20.3.4", want{true, ReasonCIDREntry, "10.20.0.0/16"}}, // a name resolving inside a cidr entry
		{"::ffff:10.20.3.4", "::ffff:10.20.3.4", want{true, ReasonCIDREntry, "10.20.0.0/16"}},
		{"fd00:1234::9", "fd00:1234::9", want{true, ReasonCIDREntry, "fd00:1234::/32"}},
		{"fd00:1235::9", "fd00:1235::9", want{false, ReasonPrivateAddress, ""}},
		{"x.svc.cluster.local", "172.20.1.1", want{true, ReasonHostEntry, "*.svc.cluster.local"}},
		{"mcp.corp.example.com.", "192.168.1.5", want{true, ReasonHostEntry, "mcp.corp.example.com"}},
		{"corp.example.com", "192.168.1.5", want{true, ReasonCIDREntry, "0.0.0.0/0"}},
		// Blocked, whatever the entries.
		{"x.svc.cluster.local", "169.254.169.254", want{false, ReasonBlockedAddress, ""}},
		{"mcp.corp.example.com", "127.0.0.1", want{false, ReasonBlockedAddress, ""}},
		{"mcp.corp.example.com", "::ffff:169.254.169.254", want{false, ReasonBlockedAddress, ""}},
		{"mcp.corp.example.com", "fe80::1", want{false, ReasonBlockedAddress, ""}},
		{"0.0.0.0", "0.0.0.0", want{false, ReasonBlockedAddress, ""}},
	} {
		v := p.CheckAddr(c.host, netip.MustParseAddr(c.addr))
		entry := ""
		if v.Entry != nil {
			entry = v.Entry.Value
		}
		if (want{v.Allowed, v.Reason, entry}) != c.want {
			t.Errorf("CheckAddr(%s, %s) = %+v %q, want %+v", c.host, c.addr, v, entry, c.want)
		}
	}
	// Without entries: public only.
	var none *Policy
	if v := none.CheckAddr("10.0.0.1", netip.MustParseAddr("10.0.0.1")); v.Allowed || v.Reason != ReasonPrivateAddress {
		t.Errorf("nil policy private = %+v", v)
	}
	if v := none.CheckAddr("x", netip.MustParseAddr("8.8.8.8")); !v.Allowed {
		t.Errorf("nil policy public = %+v", v)
	}
	if !none.Empty() || New().Empty() != true || p.Empty() {
		t.Error("Empty")
	}
}

func TestHTTPAllowed(t *testing.T) {
	p := New([]Entry{
		{Kind: KindCIDR, Value: "10.20.0.0/16", AllowHTTP: true},
		{Kind: KindCIDR, Value: "10.30.0.0/16"},
		{Kind: KindHost, Value: "*.svc.cluster.local", AllowHTTP: true},
		{Kind: KindHost, Value: "mcp.corp.example.com"},
		{Kind: KindHost, Value: "both.corp.example.com"},
		{Kind: KindHost, Value: "*.corp.example.com", AllowHTTP: true},
	})
	for host, want := range map[string]bool{
		"10.20.0.5":                true,  // a literal inside a cidr entry with allowHttp
		"10.30.0.5":                false, // without allowHttp
		"::ffff:10.20.0.5":         true,
		"api.svc.cluster.local":    true,
		"API.SVC.CLUSTER.LOCAL.":   true,
		"svc.cluster.local":        false, // *.suffix: subdomains only
		"mcp.corp.example.com":     true,  // also matches *.corp.example.com, which allows http
		"both.corp.example.com":    true,
		"corp.example.com":         false,
		"evilcorp.example.com":     false,
		"build.example.com":        false, // even if it resolves inside 10.20.0.0/16: names need a host entry
		"8.8.8.8":                  false,
		"127.0.0.1":                false,
		"api.svc.cluster.local:80": false,
	} {
		if _, got := p.HTTPAllowed(host); got != want {
			t.Errorf("HTTPAllowed(%q) = %v, want %v", host, got, want)
		}
	}
}

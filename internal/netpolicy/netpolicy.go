// Package netpolicy is the address policy for MCP server URLs
// (docs/decisions.md, decision 20; docs/registry.md "Security"): which
// addresses the gateway may reach, and over which scheme.
//
// Every address falls in one of three classes:
//
//   - blocked: loopback, link-local (including cloud metadata such as
//     169.254.169.254), unspecified, multicast, broadcast, 0.0.0.0/8 and
//     IPv4-mapped, IPv4-compatible and NAT64 forms of those. Never allowed,
//     whatever the allowlist says.
//   - public: allocated public unicast. Always allowed (over https).
//   - private: everything else (RFC 1918, CGNAT, ULA, documentation and
//     reserved ranges...). Allowed only by an allowlist entry.
//
// The allowlist has two kinds of entries: cidr (an IPv4 or IPv6 prefix) and
// host (an exact host name, or *.suffix for its subdomains only). An entry
// with AllowHTTP also allows plain http for URLs whose host matches it.
// Entries come from the platform admins (stored in the database) and from
// the deployment (UPSTREAM_ALLOWED_NETWORKS).
//
// The package has no dependencies on the rest of the gateway, so config,
// upstream and registry can all use it.
package netpolicy

import (
	"fmt"
	"net/netip"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Entry kinds.
const (
	KindCIDR = "cidr"
	KindHost = "host"
)

// MaxEntries bounds the admin-managed list; MaxNote an entry's note (in
// characters).
const (
	MaxEntries = 100
	MaxNote    = 200
)

// Entry is one allowlist entry.
type Entry struct {
	Kind      string `json:"kind"`
	Value     string `json:"value"`
	AllowHTTP bool   `json:"allowHttp"`
	Note      string `json:"note,omitempty"`
}

// Entry problems (details.reason of 422 invalid_entry).
const (
	ProblemKind          = "invalid_kind"
	ProblemCIDR          = "invalid_cidr"
	ProblemHost          = "invalid_host"
	ProblemNonASCII      = "non_ascii"
	ProblemIPAsHost      = "ip_as_host"
	ProblemAlwaysBlocked = "always_blocked"
	ProblemNote          = "invalid_note"
	ProblemDuplicate     = "duplicate"
	ProblemTooMany       = "too_many"
)

// EntryError is an invalid entry: its index in the list, a machine reason
// and a message for people.
type EntryError struct {
	Index   int
	Reason  string
	Message string
}

func (e *EntryError) Error() string { return fmt.Sprintf("entry %d: %s", e.Index+1, e.Message) }

// Canonical checks an entry and returns its canonical form: a masked
// prefix ("10.20.0.0/16"; a bare address becomes /32 or /128, an
// IPv4-mapped prefix its IPv4 form), or a lower-case host name without a
// trailing dot. The note is trimmed.
func Canonical(e Entry) (Entry, *EntryError) {
	note := strings.TrimSpace(e.Note)
	if utf8.RuneCountInString(note) > MaxNote || strings.IndexFunc(note, unicode.IsControl) >= 0 {
		return e, &EntryError{Reason: ProblemNote, Message: fmt.Sprintf("The note must be at most %d characters on one line.", MaxNote)}
	}
	out := Entry{Kind: e.Kind, AllowHTTP: e.AllowHTTP, Note: note}
	value := strings.TrimSpace(e.Value)
	switch e.Kind {
	case KindCIDR:
		p, problem := canonicalPrefix(value)
		if problem != nil {
			return e, problem
		}
		out.Value = p.String()
	case KindHost:
		h, problem := canonicalHostEntry(value)
		if problem != nil {
			return e, problem
		}
		out.Value = h
	default:
		return e, &EntryError{Reason: ProblemKind, Message: "The kind must be cidr or host."}
	}
	return out, nil
}

func canonicalPrefix(value string) (netip.Prefix, *EntryError) {
	bad := &EntryError{Reason: ProblemCIDR, Message: "Enter a network such as 10.20.0.0/16 or fd00:1234::/32."}
	if value == "" || strings.Contains(value, "%") {
		return netip.Prefix{}, bad
	}
	p, err := netip.ParsePrefix(value)
	if err != nil {
		ip, err := netip.ParseAddr(value)
		if err != nil {
			return netip.Prefix{}, bad
		}
		p = netip.PrefixFrom(ip, ip.BitLen())
	}
	if p.Addr().Is4In6() {
		if p.Bits() < 96 {
			return netip.Prefix{}, &EntryError{Reason: ProblemCIDR, Message: "An IPv4-mapped network must be /96 or longer; enter its IPv4 form instead."}
		}
		p = netip.PrefixFrom(p.Addr().Unmap(), p.Bits()-96)
	}
	p = p.Masked()
	if insideBlocked(p) {
		return netip.Prefix{}, &EntryError{Reason: ProblemAlwaysBlocked,
			Message: "This network is loopback, link-local, cloud metadata, unspecified, multicast or broadcast, which MCP servers may never use."}
	}
	return p, nil
}

func canonicalHostEntry(value string) (string, *EntryError) {
	h := strings.TrimSuffix(strings.ToLower(value), ".")
	for _, r := range h {
		if r > unicode.MaxASCII {
			return "", &EntryError{Reason: ProblemNonASCII, Message: "Enter international names in their ASCII (punycode) form, such as xn--bcher-kva.example."}
		}
	}
	name, wildcard := strings.CutPrefix(h, "*.")
	if _, err := netip.ParseAddr(strings.Trim(name, "[]")); err == nil || numericName(name) {
		return "", &EntryError{Reason: ProblemIPAsHost, Message: "Addresses belong in a cidr entry, such as 10.20.0.5/32."}
	}
	if !validHostName(name) {
		return "", &EntryError{Reason: ProblemHost,
			Message: "Enter a host name such as mcp.corp.example.com, or *.corp.example.com for its subdomains (no scheme, port or path)."}
	}
	if name == "localhost" || strings.HasSuffix(name, ".localhost") {
		return "", &EntryError{Reason: ProblemAlwaysBlocked, Message: "localhost is loopback, which MCP servers may never use."}
	}
	if wildcard {
		return "*." + name, nil
	}
	return name, nil
}

// validHostName reports whether h is a lower-case LDH host name: labels of
// 1-63 letters, digits and hyphens (not at either end), at most 253
// characters.
func validHostName(h string) bool {
	if h == "" || len(h) > 253 {
		return false
	}
	for _, label := range strings.Split(h, ".") {
		if label == "" || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for i := 0; i < len(label); i++ {
			c := label[i]
			if (c < 'a' || c > 'z') && (c < '0' || c > '9') && c != '-' {
				return false
			}
		}
	}
	return true
}

// numericName reports whether every label of h is digits (or hex with 0x):
// an address in a notation netip doesn't parse, such as 0177.0.0.1.
func numericName(h string) bool {
	if h == "" {
		return false
	}
	for _, part := range strings.Split(h, ".") {
		if part == "" {
			continue
		}
		p := strings.TrimPrefix(part, "0x")
		if p == "" {
			return false
		}
		hex := len(p) != len(part)
		for _, r := range p {
			if !(r >= '0' && r <= '9') && !(hex && r >= 'a' && r <= 'f') {
				return false
			}
		}
	}
	return true
}

// NumericHost is numericName for a URL host (any case).
func NumericHost(host string) bool { return numericName(strings.ToLower(host)) }

// Validate canonicalises an admin list: at most MaxEntries, each valid, no
// two with the same kind and value.
func Validate(entries []Entry) ([]Entry, *EntryError) {
	if len(entries) > MaxEntries {
		return nil, &EntryError{Index: MaxEntries, Reason: ProblemTooMany, Message: fmt.Sprintf("At most %d entries.", MaxEntries)}
	}
	out := make([]Entry, 0, len(entries))
	seen := map[string]int{}
	for i, e := range entries {
		c, problem := Canonical(e)
		if problem != nil {
			problem.Index = i
			return nil, problem
		}
		key := c.Kind + " " + c.Value
		if j, dup := seen[key]; dup {
			return nil, &EntryError{Index: i, Reason: ProblemDuplicate, Message: fmt.Sprintf("Entry %d already lists %s.", j+1, c.Value)}
		}
		seen[key] = i
		out = append(out, c)
	}
	return out, nil
}

// ParseList reads the deployment list (UPSTREAM_ALLOWED_NETWORKS):
// comma-separated entries, each a network or address (cidr) or a host name
// or *.suffix (host), optionally prefixed with "http:" to allow plain http.
// Example: 10.20.0.0/16,http:*.svc.cluster.local,mcp.corp.example.com
func ParseList(s string) ([]Entry, error) {
	var out []Entry
	seen := map[string]bool{}
	for _, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		e := Entry{}
		if rest, ok := strings.CutPrefix(strings.ToLower(part), "http:"); ok {
			e.AllowHTTP, part = true, strings.TrimSpace(part[len(part)-len(rest):])
		}
		e.Kind, e.Value = KindHost, part
		if looksLikeAddress(part) {
			e.Kind = KindCIDR
		}
		c, problem := Canonical(e)
		if problem != nil {
			return nil, fmt.Errorf("invalid entry %q: %s", part, problem.Message)
		}
		if key := c.Kind + " " + c.Value; !seen[key] {
			seen[key] = true
			out = append(out, c)
		}
	}
	return out, nil
}

// looksLikeAddress reports whether a deployment entry is a network or
// address rather than a host name.
func looksLikeAddress(s string) bool {
	if strings.Contains(s, "/") || strings.Contains(s, ":") {
		return true
	}
	_, err := netip.ParseAddr(s)
	return err == nil
}

// FormatList is the deployment list as UPSTREAM_ALLOWED_NETWORKS spells it.
func FormatList(entries []Entry) string {
	parts := make([]string, len(entries))
	for i, e := range entries {
		parts[i] = e.Value
		if e.AllowHTTP {
			parts[i] = "http:" + e.Value
		}
	}
	return strings.Join(parts, ",")
}

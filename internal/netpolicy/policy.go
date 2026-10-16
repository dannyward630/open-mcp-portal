package netpolicy

import (
	"net/netip"
	"strings"
)

// Class is an address's class.
type Class int

const (
	Blocked Class = iota // never allowed
	Public               // always allowed
	Private              // allowed by an allowlist entry
)

func prefixes(ranges ...string) []netip.Prefix {
	out := make([]netip.Prefix, len(ranges))
	for i, r := range ranges {
		out[i] = netip.MustParsePrefix(r)
	}
	return out
}

// blockedNetworks are never allowed: loopback, link-local, unspecified,
// multicast, broadcast, "this network", and cloud metadata endpoints
// outside link-local (Azure's wire server, Alibaba's metadata service and
// AWS's IPv6 IMDS).
var blockedNetworks = prefixes(
	"0.0.0.0/8", "127.0.0.0/8", "169.254.0.0/16", "224.0.0.0/4", "255.255.255.255/32",
	"168.63.129.16/32", "100.100.100.200/32",
	"::/128", "::1/128", "fe80::/10", "ff00::/8", "fd00:ec2::254/128",
)

// Embedded-IPv4 forms: an address here carries an IPv4 address in its last
// 32 bits (IPv4-compatible, and NAT64 well-known prefix), which is checked
// as well.
var embedsIPv4 = prefixes("::/96", "64:ff9b::/96")

// notPublic are ranges that are not public even though Go considers them
// global unicast: private, CGNAT, documentation, benchmarking, reserved,
// and IPv6 special assignments, documentation, 6to4/Teredo and
// translation. Only allocated global unicast (2000::/3) counts as public
// IPv6.
var notPublic = prefixes(
	"10.0.0.0/8", "100.64.0.0/10", "172.16.0.0/12", "192.0.0.0/24", "192.0.2.0/24", "192.88.99.0/24", "192.168.0.0/16",
	"198.18.0.0/15", "198.51.100.0/24", "203.0.113.0/24", "240.0.0.0/4",
	"2001::/23", "2001:db8::/32", "2002::/16", "3fff::/20",
)

var globalUnicast6 = netip.MustParsePrefix("2000::/3")

// Classify returns an address's class.
func Classify(ip netip.Addr) Class {
	if !ip.IsValid() || ip.Zone() != "" {
		return Blocked
	}
	ip = ip.Unmap()
	if blockedAddr(ip) {
		return Blocked
	}
	if publicAddr(ip) {
		return Public
	}
	return Private
}

func blockedAddr(ip netip.Addr) bool {
	if ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsInterfaceLocalMulticast() ||
		ip.IsMulticast() || ip.IsUnspecified() {
		return true
	}
	for _, p := range blockedNetworks {
		if p.Contains(ip) {
			return true
		}
	}
	if ip.Is6() {
		for _, p := range embedsIPv4 {
			if p.Contains(ip) {
				b := ip.As16()
				if v4 := netip.AddrFrom4([4]byte{b[12], b[13], b[14], b[15]}); blockedAddr(v4) {
					return true
				}
			}
		}
	}
	return false
}

func publicAddr(ip netip.Addr) bool {
	if !ip.IsGlobalUnicast() {
		return false
	}
	if ip.Is6() && !globalUnicast6.Contains(ip) {
		return false
	}
	for _, p := range notPublic {
		if p.Contains(ip) {
			return false
		}
	}
	return true
}

// PublicAddr reports whether ip is a public unicast address.
func PublicAddr(ip netip.Addr) bool { return Classify(ip) == Public }

// insideBlocked reports whether every address of p is blocked (an entry
// for it could never allow anything).
func insideBlocked(p netip.Prefix) bool {
	for _, b := range blockedNetworks {
		if b.Bits() <= p.Bits() && b.Contains(p.Addr()) {
			return true
		}
	}
	return false
}

// Policy is the allowlist in force: deployment entries and admin entries.
// The zero value and nil allow public addresses only.
type Policy struct {
	cidrs []cidrEntry
	hosts []Entry
}

type cidrEntry struct {
	prefix netip.Prefix
	entry  Entry
}

// New compiles entries (canonical, as Canonical returns them; others are
// skipped). Earlier lists come first in matches.
func New(lists ...[]Entry) *Policy {
	p := &Policy{}
	for _, list := range lists {
		for _, e := range list {
			c, problem := Canonical(e)
			if problem != nil {
				continue
			}
			switch c.Kind {
			case KindCIDR:
				p.cidrs = append(p.cidrs, cidrEntry{prefix: netip.MustParsePrefix(c.Value), entry: c})
			case KindHost:
				p.hosts = append(p.hosts, c)
			}
		}
	}
	return p
}

// Empty reports whether the policy has no entries.
func (p *Policy) Empty() bool { return p == nil || len(p.cidrs)+len(p.hosts) == 0 }

// CanonicalHost is a URL host as entries are matched against it: lower
// case, without a trailing dot ("" when it is not a valid host name).
func CanonicalHost(host string) string {
	h := strings.TrimSuffix(strings.ToLower(host), ".")
	if !validHostName(h) {
		return ""
	}
	return h
}

// HostMatches reports whether a host entry's value matches a URL host:
// exactly, or for *.suffix any name ending in .suffix (not suffix itself).
func HostMatches(pattern, host string) bool {
	h := CanonicalHost(host)
	if h == "" {
		return false
	}
	if suffix, ok := strings.CutPrefix(pattern, "*"); ok {
		return len(h) > len(suffix) && strings.HasSuffix(h, suffix)
	}
	return h == pattern
}

// MatchHost returns the host entry matching a URL host, preferring one that
// allows http.
func (p *Policy) MatchHost(host string) (Entry, bool) {
	if p == nil {
		return Entry{}, false
	}
	var found *Entry
	for i := range p.hosts {
		if HostMatches(p.hosts[i].Value, host) {
			if p.hosts[i].AllowHTTP {
				return p.hosts[i], true
			}
			if found == nil {
				found = &p.hosts[i]
			}
		}
	}
	if found == nil {
		return Entry{}, false
	}
	return *found, true
}

// MatchAddr returns the cidr entry containing ip, preferring one that
// allows http.
func (p *Policy) MatchAddr(ip netip.Addr) (Entry, bool) {
	if p == nil || !ip.IsValid() || ip.Zone() != "" {
		return Entry{}, false
	}
	ip = ip.Unmap()
	var found *Entry
	for i := range p.cidrs {
		if p.cidrs[i].prefix.Contains(ip) {
			if p.cidrs[i].entry.AllowHTTP {
				return p.cidrs[i].entry, true
			}
			if found == nil {
				found = &p.cidrs[i].entry
			}
		}
	}
	if found == nil {
		return Entry{}, false
	}
	return *found, true
}

// Address verdicts (Verdict.Reason).
const (
	ReasonPublic         = "public"          // a public address
	ReasonCIDREntry      = "cidr_entry"      // a private address inside a cidr entry
	ReasonHostEntry      = "host_entry"      // a private address of a host matching a host entry
	ReasonDevelopment    = "development"     // the development allowance
	ReasonBlockedAddress = "blocked_address" // never allowed
	ReasonPrivateAddress = "private_address" // private and not allowed by any entry
)

// Verdict is whether one address may be dialled for a URL host.
type Verdict struct {
	Allowed bool
	Reason  string
	Entry   *Entry // the entry that allowed it
}

// CheckAddr decides whether ip, an address of the URL host (a literal
// address or a name that resolved to it), may be dialled: blocked never,
// public always, private when host matches a host entry or ip is inside a
// cidr entry.
func (p *Policy) CheckAddr(host string, ip netip.Addr) Verdict {
	switch Classify(ip) {
	case Blocked:
		return Verdict{Reason: ReasonBlockedAddress}
	case Public:
		return Verdict{Allowed: true, Reason: ReasonPublic}
	}
	// A host entry names the server, so it is the more specific match.
	if e, ok := p.MatchHost(host); ok {
		return Verdict{Allowed: true, Reason: ReasonHostEntry, Entry: &e}
	}
	if e, ok := p.MatchAddr(ip); ok {
		return Verdict{Allowed: true, Reason: ReasonCIDREntry, Entry: &e}
	}
	return Verdict{Reason: ReasonPrivateAddress}
}

// HTTPAllowed reports whether plain http is allowed for a URL host: a host
// entry with AllowHTTP matches it, or it is a literal address inside a cidr
// entry with AllowHTTP. The matching entry is returned.
func (p *Policy) HTTPAllowed(host string) (Entry, bool) {
	if ip, err := netip.ParseAddr(host); err == nil {
		if Classify(ip) == Blocked {
			return Entry{}, false
		}
		e, ok := p.MatchAddr(ip)
		return e, ok && e.AllowHTTP
	}
	e, ok := p.MatchHost(host)
	return e, ok && e.AllowHTTP
}

package upstream

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/ncecere/open-mcp-gateway/internal/apperr"
)

// The address rule (adapted from Grounded's internal/mcpclient, see
// docs/registry.md "Security"): an MCP server's URL is https, and its host
// resolves to public addresses only, checked when the URL is saved (for
// literal addresses and localhost) and again, pinned, every time a
// connection is dialled, so a DNS change can't point a registered server at
// the internal network. With the development allowance (DEV_AUTH on a
// loopback APP_URL) private, loopback and link-local addresses are accepted,
// and plain http is accepted for loopback hosts. Redirects are never
// followed and proxy settings from the environment are never used.

// ErrBlockedAddress: the URL's host is (or resolves to) an address the rule
// refuses, or the server redirected.
var ErrBlockedAddress = errors.New("upstream: address not allowed")

// MaxURL bounds a server URL.
const MaxURL = 500

// URL problems (details.reason of 422 url_not_allowed).
const (
	ReasonInvalid        = "invalid"
	ReasonTooLong        = "too_long"
	ReasonUserInfo       = "userinfo"
	ReasonQuery          = "query"
	ReasonFragment       = "fragment"
	ReasonScheme         = "scheme"
	ReasonPrivateAddress = "private_address"
)

func refused(reason, msg string) error {
	e := apperr.New(http.StatusUnprocessableEntity, "url_not_allowed", msg)
	e.Details = map[string]any{"reason": reason}
	return e
}

// NormalizeURL checks a server URL: https (http only for a loopback host
// with the development allowance), a host, no user info, query or fragment
// (credentials belong in the header, which is stored encrypted), and a
// literal IP address or localhost only when the rule allows it. Host names
// are resolved when dialling. Problems are 422 url_not_allowed with
// details.reason.
func NormalizeURL(raw string, allowPrivate bool) (string, error) {
	raw = strings.TrimSpace(raw)
	u, err := url.Parse(raw)
	if reason, msg := urlShapeProblem(raw, u, err); reason != "" {
		return "", refused(reason, msg)
	}
	host := u.Hostname()
	switch u.Scheme {
	case "https":
	case "http":
		if !allowPrivate || !loopbackHost(host) {
			return "", refused(ReasonScheme, "The URL must use https.")
		}
	default:
		return "", refused(ReasonScheme, "The URL must use https.")
	}
	const notPublic = "The URL's address is private, loopback or link-local. MCP servers must be reachable at a public address."
	if allowPrivate {
		return u.String(), nil
	}
	if ip, err := netip.ParseAddr(host); err == nil {
		if !PublicAddr(ip) {
			return "", refused(ReasonPrivateAddress, notPublic)
		}
		return u.String(), nil
	}
	if strings.EqualFold(host, "localhost") || strings.HasSuffix(strings.ToLower(host), ".localhost") {
		return "", refused(ReasonPrivateAddress, notPublic)
	}
	if numericHost(host) {
		// Other notations of an address ("0177.0.0.1", "0x7f.1", "127.1") are
		// what SSRF attempts use; they are never treated as public.
		return "", refused(ReasonPrivateAddress, notPublic)
	}
	return u.String(), nil
}

// urlShapeProblem says what is wrong with a URL's shape ("" when nothing
// is). A host name's addresses are checked when dialling, not here.
func urlShapeProblem(raw string, u *url.URL, err error) (reason, msg string) {
	switch {
	case len(raw) > MaxURL:
		return ReasonTooLong, fmt.Sprintf("The URL is too long: at most %d characters.", MaxURL)
	case err != nil || u.Host == "" || u.Hostname() == "":
		return ReasonInvalid, "Enter a full URL, such as https://status.example.com/mcp."
	case u.User != nil:
		return ReasonUserInfo, "The URL can't contain a user name or password. Put credentials in the header instead."
	case u.RawQuery != "" || u.ForceQuery:
		return ReasonQuery, "The URL can't have a query (?…). Put credentials in the header instead."
	case u.Fragment != "" || strings.Contains(raw, "#"):
		return ReasonFragment, "The URL can't have a fragment (#…)."
	}
	return "", ""
}

func loopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip, err := netip.ParseAddr(host)
	return err == nil && ip.IsLoopback()
}

// numericHost reports whether host is made only of digits, dots and hex
// prefixes (an address in a notation netip doesn't parse).
func numericHost(host string) bool {
	if host == "" {
		return false
	}
	for _, part := range strings.Split(strings.ToLower(host), ".") {
		if part == "" {
			continue
		}
		p := strings.TrimPrefix(part, "0x")
		if p == "" {
			return false
		}
		hex := len(p) != len(part)
		for _, r := range p {
			isDigit := r >= '0' && r <= '9'
			isHex := hex && r >= 'a' && r <= 'f'
			if !isDigit && !isHex {
				return false
			}
		}
	}
	return true
}

var deniedNetworks = func() []netip.Prefix {
	ranges := []string{
		"0.0.0.0/8", "10.0.0.0/8", "100.64.0.0/10", "127.0.0.0/8", "169.254.0.0/16", "168.63.129.16/32", "172.16.0.0/12",
		"192.0.0.0/24", "192.0.2.0/24", "192.88.99.0/24", "192.168.0.0/16", "198.18.0.0/15", "198.51.100.0/24", "203.0.113.0/24",
		"224.0.0.0/4", "240.0.0.0/4",
		// IPv6: only allocated global unicast (2000::/3) is accepted, minus
		// special assignments, documentation, 6to4/Teredo and translation.
		"2001::/23", "2001:db8::/32", "2002::/16", "3fff::/20",
	}
	out := make([]netip.Prefix, 0, len(ranges))
	for _, r := range ranges {
		out = append(out, netip.MustParsePrefix(r))
	}
	return out
}()

var globalUnicast6 = netip.MustParsePrefix("2000::/3")

// PublicAddr reports whether ip is a public unicast address: not private,
// loopback, link-local, CGNAT, metadata, documentation or translation space.
func PublicAddr(ip netip.Addr) bool {
	if !ip.IsValid() || ip.Zone() != "" {
		return false
	}
	ip = ip.Unmap()
	if !ip.IsGlobalUnicast() {
		return false
	}
	if ip.Is6() && !globalUnicast6.Contains(ip) {
		return false
	}
	for _, p := range deniedNetworks {
		if p.Contains(ip) {
			return false
		}
	}
	return true
}

// resolver looks up a host's addresses (tests replace it).
type resolver func(ctx context.Context, host string) ([]netip.Addr, error)

func defaultResolver(ctx context.Context, host string) ([]netip.Addr, error) {
	return net.DefaultResolver.LookupNetIP(ctx, "ip", host)
}

// dialer dials only addresses the rule allows, resolving the host once and
// pinning the connection to the approved addresses.
type dialer struct {
	allowPrivate bool
	lookup       resolver
	net          net.Dialer
}

func (d *dialer) dial(ctx context.Context, network, address string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return nil, err
	}
	var ips []netip.Addr
	if ip, err := netip.ParseAddr(host); err == nil {
		ips = []netip.Addr{ip}
	} else if ips, err = d.lookup(ctx, host); err != nil {
		return nil, fmt.Errorf("upstream: resolve %s: %w", host, err)
	}
	if len(ips) == 0 {
		return nil, fmt.Errorf("upstream: %s has no addresses", host)
	}
	if !d.allowPrivate {
		for _, ip := range ips {
			if !PublicAddr(ip) {
				return nil, fmt.Errorf("%w: %s resolved to %s", ErrBlockedAddress, host, ip)
			}
		}
	}
	var errs []error
	for _, ip := range ips {
		conn, err := d.net.DialContext(ctx, network, net.JoinHostPort(ip.Unmap().String(), port))
		if err == nil {
			return conn, nil
		}
		errs = append(errs, err)
		if ctx.Err() != nil {
			break
		}
	}
	return nil, fmt.Errorf("upstream: dial %s: %w", host, errors.Join(errs...))
}

// exchange records what the server answered, for error classes.
type exchange struct {
	mu     sync.Mutex
	status int // the last HTTP status >= 400 (0: none)
}

func (e *exchange) setStatus(code int) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if code >= 400 {
		e.status = code
	}
}

func (e *exchange) lastStatus() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.status
}

// ErrResponseTooLarge: a response body passed MaxResponseBytes.
var ErrResponseTooLarge = errors.New("upstream: response too large")

// headerTransport adds the static header, records error statuses and caps
// response bodies.
type headerTransport struct {
	next        http.RoundTripper
	header      string
	value       string
	maxResponse int64
	ex          *exchange
}

func (t *headerTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if t.header != "" {
		req = req.Clone(req.Context())
		req.Header.Set(t.header, t.value)
	}
	res, err := t.next.RoundTrip(req)
	if err != nil {
		return nil, err
	}
	t.ex.setStatus(res.StatusCode)
	res.Body = &cappedBody{ReadCloser: res.Body, left: t.maxResponse}
	return res, nil
}

// cappedBody fails a read past its limit.
type cappedBody struct {
	io.ReadCloser
	left int64
}

func (b *cappedBody) Read(p []byte) (int, error) {
	if b.left <= 0 {
		return 0, ErrResponseTooLarge
	}
	if int64(len(p)) > b.left {
		p = p[:b.left+1] // one byte more tells a body that ends exactly at the limit from a longer one
	}
	n, err := b.ReadCloser.Read(p)
	b.left -= int64(n)
	if b.left < 0 {
		return n - 1, ErrResponseTooLarge
	}
	return n, err
}

// httpClient builds the client of one exchange with a server: the guarded
// dialer, no proxy from the environment, no redirects, the header and the
// body cap. done closes its idle connections.
func (c *Client) httpClient(t Target, ex *exchange) (client *http.Client, done func()) {
	d := &dialer{allowPrivate: c.AllowPrivate, lookup: c.resolve(), net: net.Dialer{Timeout: 10 * time.Second}}
	tr := &http.Transport{
		Proxy:                 nil, // never HTTP(S)_PROXY: the dialer must see the real destination
		DialContext:           d.dial,
		ForceAttemptHTTP2:     true,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: t.Timeout,
		MaxIdleConns:          4,
		IdleConnTimeout:       30 * time.Second,
	}
	return &http.Client{
		Transport: &headerTransport{next: tr, header: t.HeaderName, value: t.HeaderValue, maxResponse: c.maxResponse(), ex: ex},
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return fmt.Errorf("%w: MCP servers may not redirect", ErrBlockedAddress)
		},
	}, tr.CloseIdleConnections
}

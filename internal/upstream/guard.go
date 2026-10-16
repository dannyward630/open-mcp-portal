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
	"github.com/ncecere/open-mcp-gateway/internal/netpolicy"
)

// The address rule (adapted from Grounded's internal/mcpclient, see
// docs/registry.md "Security" and internal/netpolicy): an MCP server's URL
// is https, and every address its host is or resolves to is allowed by the
// policy: public addresses always, private (internal) ones when a platform
// admin's or the deployment's allowlist entry covers them, and loopback,
// link-local (including cloud metadata), unspecified, multicast and
// broadcast addresses never. Plain http is accepted only for hosts an entry
// allows it for. The rule is checked when the URL is saved (shape, scheme,
// literal addresses and host entries), before every exchange, and, pinned,
// every time a connection is dialled, against every address the host
// resolves to, so a DNS change can't point a registered server at an
// address the policy refuses. With the development allowance (DEV_AUTH on
// a loopback APP_URL) private, loopback and link-local addresses are
// accepted, and plain http is accepted for loopback hosts. Redirects are
// never followed and proxy settings from the environment are never used.

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
	ReasonPrivateAddress = netpolicy.ReasonPrivateAddress
	ReasonBlockedAddress = netpolicy.ReasonBlockedAddress
)

// Messages of the address problems.
const (
	MessagePrivateAddress = "The URL's address is internal (private). MCP servers must be reachable at a public address, " +
		"unless a platform admin allows the internal network (Admin › Internal networks)."
	MessageBlockedAddress = "The URL's address is loopback, link-local (such as cloud metadata), unspecified, multicast or broadcast, " +
		"which MCP servers may never use."
	messageNumericHost = "Write the address in standard form (such as 10.20.0.5); other numeric notations are refused."
	messageScheme      = "The URL must use https."
)

func refused(reason, msg string) error {
	e := apperr.New(http.StatusUnprocessableEntity, "url_not_allowed", msg)
	e.Details = map[string]any{"reason": reason}
	return e
}

// NormalizeURL is NormalizeURLWith without allowlist entries.
func NormalizeURL(raw string, allowPrivate bool) (string, error) {
	return NormalizeURLWith(raw, allowPrivate, nil)
}

// NormalizeURLWith checks a server URL against the policy: https (http
// only for a host an entry allows it for, or a loopback host with the
// development allowance), a host, no user info, query or fragment
// (credentials belong in the header, which is stored encrypted), and a
// literal IP address or localhost only when the rule allows it. Host names
// are resolved when dialling. Problems are 422 url_not_allowed with
// details.reason.
func NormalizeURLWith(raw string, allowPrivate bool, p *netpolicy.Policy) (string, error) {
	raw = strings.TrimSpace(raw)
	u, err := url.Parse(raw)
	if reason, msg := urlShapeProblem(raw, u, err); reason != "" {
		return "", refused(reason, msg)
	}
	host := u.Hostname()
	switch u.Scheme {
	case "https":
	case "http":
		_, entry := p.HTTPAllowed(host)
		if !entry && !(allowPrivate && loopbackHost(host)) {
			return "", refused(ReasonScheme, messageScheme)
		}
	default:
		return "", refused(ReasonScheme, messageScheme)
	}
	if allowPrivate {
		return u.String(), nil
	}
	if ip, err := netip.ParseAddr(host); err == nil {
		if v := p.CheckAddr(host, ip); !v.Allowed {
			return "", addressRefused(v.Reason)
		}
		return u.String(), nil
	}
	if h := strings.ToLower(strings.TrimSuffix(host, ".")); h == "localhost" || strings.HasSuffix(h, ".localhost") {
		return "", refused(ReasonBlockedAddress, MessageBlockedAddress)
	}
	if netpolicy.NumericHost(host) {
		// Other notations of an address ("0177.0.0.1", "0x7f.1", "127.1") are
		// what SSRF attempts use; they are never allowed.
		return "", refused(ReasonPrivateAddress, messageNumericHost)
	}
	return u.String(), nil
}

func addressRefused(reason string) error {
	if reason == netpolicy.ReasonBlockedAddress {
		return refused(ReasonBlockedAddress, MessageBlockedAddress)
	}
	return refused(ReasonPrivateAddress, MessagePrivateAddress)
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

// PublicAddr reports whether ip is a public unicast address: not private,
// loopback, link-local, CGNAT, metadata, documentation or translation space.
func PublicAddr(ip netip.Addr) bool { return netpolicy.PublicAddr(ip) }

// resolver looks up a host's addresses (tests replace it).
type resolver func(ctx context.Context, host string) ([]netip.Addr, error)

func defaultResolver(ctx context.Context, host string) ([]netip.Addr, error) {
	return net.DefaultResolver.LookupNetIP(ctx, "ip", host)
}

// dialer dials only addresses the rule allows, resolving the host once and
// pinning the connection to the approved addresses.
type dialer struct {
	allowPrivate bool
	// policy is the allowlist in force (read at every dial; nil: none).
	policy func(ctx context.Context) *netpolicy.Policy
	lookup resolver
	// connect dials an approved address (nil: net.Dialer with a 10s timeout).
	connect func(ctx context.Context, network, address string) (net.Conn, error)
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
		var p *netpolicy.Policy
		if d.policy != nil {
			p = d.policy(ctx)
		}
		for _, ip := range ips {
			if v := p.CheckAddr(host, ip); !v.Allowed {
				return nil, fmt.Errorf("%w: %s resolved to %s (%s)", ErrBlockedAddress, host, ip, v.Reason)
			}
		}
	}
	connect := d.connect
	if connect == nil {
		nd := &net.Dialer{Timeout: 10 * time.Second}
		connect = nd.DialContext
	}
	var errs []error
	for _, ip := range ips {
		conn, err := connect(ctx, network, net.JoinHostPort(ip.Unmap().String(), port))
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

// GuardedDial returns a dial function that applies the address rule of
// this client (the development allowance and the allowlist in force) to
// every address a host resolves to, and pins the connection to them; a
// refused address fails with ErrBlockedAddress. Other fetches of URLs
// chosen by someone else (OAuth client metadata documents) use it.
func (c *Client) GuardedDial() func(ctx context.Context, network, address string) (net.Conn, error) {
	d := &dialer{allowPrivate: c.AllowPrivate, policy: c.policy, lookup: c.resolve(), connect: c.Dial}
	return d.dial
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
	next          http.RoundTripper
	header        string
	value         string
	identity      string
	identityValue string
	maxResponse   int64
	ex            *exchange
}

func (t *headerTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if t.header != "" || t.identity != "" {
		req = req.Clone(req.Context())
	}
	if t.header != "" {
		req.Header.Set(t.header, t.value)
	}
	// The identity never replaces the server's own header: the caller picks
	// a different header name when the static one is Authorization.
	if t.identity != "" && !strings.EqualFold(t.identity, t.header) {
		req.Header.Set(t.identity, t.identityValue)
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
	d := &dialer{allowPrivate: c.AllowPrivate, policy: c.policy, lookup: c.resolve(), connect: c.Dial}
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
		Transport: &headerTransport{next: tr, header: t.HeaderName, value: t.HeaderValue, identity: t.IdentityHeader,
			identityValue: t.IdentityValue, maxResponse: c.maxResponse(), ex: ex},
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return fmt.Errorf("%w: MCP servers may not redirect", ErrBlockedAddress)
		},
	}, tr.CloseIdleConnections
}

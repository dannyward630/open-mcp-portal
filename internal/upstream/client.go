// Package upstream is the guarded MCP client the gateway uses to reach the
// remote MCP servers teams register (adapted from Grounded's
// internal/mcpclient; docs/registry.md).
//
// It speaks Streamable HTTP only, through the official Go SDK: stateless
// protocol 2026-07-28 first (server/discover) and the initialize handshake
// for older servers. It advertises no client capabilities (no roots,
// sampling or elicitation) and refuses multi-round-trip input requests.
// Every exchange goes through the address rule in guard.go. The transport
// sits behind Target and Client so gateway-hosted servers can be added later
// without changing the registry (docs/decisions.md, decision 10).
package upstream

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/ncecere/open-mcp-gateway/internal/buildinfo"
)

// DefaultMaxResponseBytes caps one HTTP response from a server.
const DefaultMaxResponseBytes = 1 << 20

// Client reaches MCP servers.
type Client struct {
	// AllowPrivate is the development allowance (DEV_AUTH on a loopback
	// APP_URL): private, loopback and link-local addresses, and http for
	// loopback hosts.
	AllowPrivate bool
	// Networks is the allowlist of internal networks and hosts in force,
	// read at every check and dial (nil: none, so public addresses only).
	Networks NetworkPolicy
	// Dial connects to an address the rule approved (nil: a net.Dialer with
	// a 10s timeout). Tests replace it to reach a local server that stands
	// in for an allowlisted address.
	Dial func(ctx context.Context, network, address string) (net.Conn, error)
	// MaxResponseBytes caps one HTTP response (DefaultMaxResponseBytes when 0).
	MaxResponseBytes int64
	Log              *slog.Logger
	// Lookup resolves host names (nil: the system resolver). Tests replace it.
	Lookup func(ctx context.Context, host string) ([]netip.Addr, error)
}

func (c *Client) maxResponse() int64 {
	if c.MaxResponseBytes > 0 {
		return c.MaxResponseBytes
	}
	return DefaultMaxResponseBytes
}

func (c *Client) resolve() resolver {
	if c.Lookup != nil {
		return c.Lookup
	}
	return defaultResolver
}

func (c *Client) log() *slog.Logger {
	if c.Log == nil {
		return slog.Default()
	}
	return c.Log
}

// Target is where and how to reach one server.
type Target struct {
	URL                     string
	HeaderName, HeaderValue string
	// IdentityHeader and IdentityValue carry the caller's signed identity
	// on forwarded calls (empty for discovery and health checks). The
	// gateway never forwards a client's own credential.
	IdentityHeader, IdentityValue string
	// Timeout bounds the whole exchange (connect and every list page).
	Timeout time.Duration
}

// session is one connection to a server.
type session struct {
	cs   *mcp.ClientSession
	ex   *exchange
	done func()
}

func (s *session) close() {
	_ = s.cs.Close()
	s.done()
}

// open connects to a server: server/discover (2026-07-28), or the
// initialize handshake for older servers.
func (c *Client) open(ctx context.Context, t Target) (*session, error) {
	// The policy may have changed since the URL was saved.
	if _, err := c.NormalizeURL(ctx, t.URL); err != nil {
		return nil, notAllowed(err)
	}
	ex := &exchange{}
	hc, done := c.httpClient(t, ex)
	client := mcp.NewClient(&mcp.Implementation{Name: "open-mcp-gateway", Title: "Open MCP Gateway", Version: buildinfo.Version}, &mcp.ClientOptions{
		Logger: slog.New(slog.DiscardHandler),
		// No roots, sampling or elicitation: the gateway can't answer a
		// remote server's questions.
		Capabilities:   &mcp.ClientCapabilities{},
		MultiRoundTrip: &mcp.MultiRoundTripOptions{Disabled: true},
	})
	tr := &mcp.StreamableClientTransport{Endpoint: t.URL, HTTPClient: hc, MaxRetries: -1, DisableStandaloneSSE: true,
		MaxEventSize: int(c.maxResponse())}
	cs, err := client.Connect(ctx, tr, nil)
	if err != nil {
		done()
		return nil, c.classify(ctx, t, ex, err)
	}
	return &session{cs: cs, ex: ex, done: done}, nil
}

// Discovery is what a server listed.
type Discovery struct {
	ServerName, ServerVersion, ProtocolVersion string
	Capabilities                               []Capability
	Latency                                    time.Duration
}

// Discover connects to a server and reads its tools, resources, resource
// templates and prompts (every page, at most MaxPerKind of each). Kinds the
// server doesn't advertise are not read. It never calls a tool. Failures
// are *Error.
func (c *Client) Discover(ctx context.Context, t Target) (Discovery, error) {
	start := time.Now()
	ctx, cancel := context.WithTimeout(ctx, t.Timeout)
	defer cancel()
	sess, err := c.open(ctx, t)
	if err != nil {
		return Discovery{Latency: time.Since(start)}, err
	}
	defer sess.close()
	d := Discovery{}
	var caps *mcp.ServerCapabilities
	if res := sess.cs.InitializeResult(); res != nil {
		caps = res.Capabilities
		d.ProtocolVersion = clip(res.ProtocolVersion, 100)
		if res.ServerInfo != nil {
			d.ServerName, d.ServerVersion = clip(res.ServerInfo.Name, 200), clip(res.ServerInfo.Version, 100)
		}
	}
	steps := []struct {
		advertised bool
		list       func() ([]Capability, error)
	}{
		{caps == nil || caps.Tools != nil, func() ([]Capability, error) { return c.listTools(ctx, t, sess) }},
		{caps == nil || caps.Resources != nil, func() ([]Capability, error) { return c.listResources(ctx, t, sess) }},
		{caps == nil || caps.Resources != nil, func() ([]Capability, error) { return c.listResourceTemplates(ctx, t, sess) }},
		{caps == nil || caps.Prompts != nil, func() ([]Capability, error) { return c.listPrompts(ctx, t, sess) }},
	}
	for _, s := range steps {
		if !s.advertised {
			continue
		}
		listed, err := s.list()
		if err != nil {
			d.Latency = time.Since(start)
			return d, err
		}
		d.Capabilities = append(d.Capabilities, listed...)
	}
	d.Latency = time.Since(start)
	return d, nil
}

// pager reads every page of one kind's list. page returns a page's
// capabilities (already converted) and the next cursor.
func (c *Client) pager(ctx context.Context, t Target, sess *session, kind string, page func(cursor string) ([]Capability, string, error)) ([]Capability, error) {
	var out []Capability
	seen := map[string]bool{}
	cursors := map[string]bool{}
	cursor := ""
	for pages := 0; ; pages++ {
		if pages >= maxPages {
			return nil, &Error{Class: ClassBadResponse, Message: fmt.Sprintf("The server's %s list has more than %d pages.", kindLabel(kind), maxPages)}
		}
		items, next, err := page(cursor)
		if err != nil {
			var e *Error
			if errors.As(err, &e) {
				return nil, e
			}
			if pages == 0 && methodNotFound(err) {
				return nil, nil // advertised, but not served: nothing of this kind
			}
			return nil, c.classify(ctx, t, sess.ex, err)
		}
		for _, cp := range items {
			if !validName(cp.Name) || seen[cp.Name] {
				continue
			}
			if len(out) >= MaxPerKind {
				return nil, &Error{Class: ClassTooLarge, Message: fmt.Sprintf("The server lists more than %d %ss.", MaxPerKind, kindLabel(kind))}
			}
			seen[cp.Name] = true
			out = append(out, cp)
		}
		if next == "" {
			return out, nil
		}
		if cursors[next] || len(next) > 4096 {
			return nil, &Error{Class: ClassBadResponse, Message: fmt.Sprintf("The server's %s list repeats a page.", kindLabel(kind))}
		}
		cursors[next] = true
		cursor = next
	}
}

func methodNotFound(err error) bool {
	var rpc *jsonrpc.Error
	return errors.As(err, &rpc) && rpc.Code == jsonrpc.CodeMethodNotFound
}

func (c *Client) listTools(ctx context.Context, t Target, sess *session) ([]Capability, error) {
	return c.pager(ctx, t, sess, KindTool, func(cursor string) ([]Capability, string, error) {
		res, err := sess.cs.ListTools(ctx, &mcp.ListToolsParams{Cursor: cursor})
		if err != nil {
			return nil, "", err
		}
		out := make([]Capability, 0, len(res.Tools))
		for _, tool := range res.Tools {
			if tool == nil {
				continue
			}
			v := *tool
			v.Title, v.Description = clip(v.Title, MaxTitle), clip(v.Description, MaxDescription)
			title := v.Title
			if title == "" && v.Annotations != nil {
				title = clip(v.Annotations.Title, MaxTitle)
			}
			cp, err := newCapability(KindTool, v.Name, title, v.Description, &v)
			if err != nil {
				return nil, "", err
			}
			out = append(out, cp)
		}
		return out, res.NextCursor, nil
	})
}

func (c *Client) listResources(ctx context.Context, t Target, sess *session) ([]Capability, error) {
	return c.pager(ctx, t, sess, KindResource, func(cursor string) ([]Capability, string, error) {
		res, err := sess.cs.ListResources(ctx, &mcp.ListResourcesParams{Cursor: cursor})
		if err != nil {
			return nil, "", err
		}
		out := make([]Capability, 0, len(res.Resources))
		for _, r := range res.Resources {
			if r == nil {
				continue
			}
			v := *r
			v.Title, v.Description = clip(v.Title, MaxTitle), clip(v.Description, MaxDescription)
			title := v.Title
			if title == "" {
				title = clip(v.Name, MaxTitle)
			}
			cp, err := newCapability(KindResource, v.URI, title, v.Description, &v)
			if err != nil {
				return nil, "", err
			}
			out = append(out, cp)
		}
		return out, res.NextCursor, nil
	})
}

func (c *Client) listResourceTemplates(ctx context.Context, t Target, sess *session) ([]Capability, error) {
	return c.pager(ctx, t, sess, KindResourceTemplate, func(cursor string) ([]Capability, string, error) {
		res, err := sess.cs.ListResourceTemplates(ctx, &mcp.ListResourceTemplatesParams{Cursor: cursor})
		if err != nil {
			return nil, "", err
		}
		out := make([]Capability, 0, len(res.ResourceTemplates))
		for _, r := range res.ResourceTemplates {
			if r == nil {
				continue
			}
			v := *r
			v.Title, v.Description = clip(v.Title, MaxTitle), clip(v.Description, MaxDescription)
			title := v.Title
			if title == "" {
				title = clip(v.Name, MaxTitle)
			}
			cp, err := newCapability(KindResourceTemplate, v.URITemplate, title, v.Description, &v)
			if err != nil {
				return nil, "", err
			}
			out = append(out, cp)
		}
		return out, res.NextCursor, nil
	})
}

func (c *Client) listPrompts(ctx context.Context, t Target, sess *session) ([]Capability, error) {
	return c.pager(ctx, t, sess, KindPrompt, func(cursor string) ([]Capability, string, error) {
		res, err := sess.cs.ListPrompts(ctx, &mcp.ListPromptsParams{Cursor: cursor})
		if err != nil {
			return nil, "", err
		}
		out := make([]Capability, 0, len(res.Prompts))
		for _, p := range res.Prompts {
			if p == nil {
				continue
			}
			v := *p
			v.Title, v.Description = clip(v.Title, MaxTitle), clip(v.Description, MaxDescription)
			cp, err := newCapability(KindPrompt, v.Name, v.Title, v.Description, &v)
			if err != nil {
				return nil, "", err
			}
			out = append(out, cp)
		}
		return out, res.NextCursor, nil
	})
}

// Error classes (the API's McpErrorClass).
const (
	ClassUnavailable = "unavailable"
	ClassAuth        = "auth"
	ClassNotFound    = "not_found"
	ClassRateLimited = "rate_limited"
	ClassBadRequest  = "bad_request"
	ClassBadResponse = "bad_response"
	ClassConfig      = "config"
	ClassTimeout     = "timeout"
	ClassTooLarge    = "too_large"
	ClassNotAllowed  = "not_allowed"
)

// Error is a failed exchange with a server: a class and a message safe to
// show the team (never a header value or a raw body).
type Error struct {
	Class      string
	HTTPStatus int
	Message    string
	// RPCCode is the server's JSON-RPC error code when it answered with an
	// error (0 otherwise).
	RPCCode int64
}

func (e *Error) Error() string { return e.Message }

// classify turns a transport or protocol error into an *Error.
func (c *Client) classify(ctx context.Context, t Target, ex *exchange, err error) error {
	var e *Error
	if errors.As(err, &e) {
		return e
	}
	status := ex.lastStatus()
	var rpc *jsonrpc.Error
	var netErr *url.Error
	switch {
	case errors.Is(err, ErrBlockedAddress) || strings.Contains(err.Error(), ErrBlockedAddress.Error()):
		return &Error{Class: ClassNotAllowed, Message: messageNotAllowed}
	case errors.Is(err, ErrResponseTooLarge) || strings.Contains(err.Error(), ErrResponseTooLarge.Error()):
		return &Error{Class: ClassTooLarge, Message: fmt.Sprintf("The server's response was larger than %d bytes.", c.maxResponse())}
	case errors.Is(err, context.DeadlineExceeded) || errors.Is(ctx.Err(), context.DeadlineExceeded):
		return &Error{Class: ClassTimeout, Message: "The server didn't answer in time."}
	case status == http.StatusUnauthorized || status == http.StatusForbidden:
		return &Error{Class: ClassAuth, HTTPStatus: status, Message: fmt.Sprintf("The server refused the credentials (HTTP %d).", status)}
	case status == http.StatusNotFound:
		return &Error{Class: ClassNotFound, HTTPStatus: status, Message: "The server answered 404: check the URL."}
	case status == http.StatusTooManyRequests:
		return &Error{Class: ClassRateLimited, HTTPStatus: status, Message: "The server is rate limiting requests (HTTP 429)."}
	case status == http.StatusBadRequest:
		return &Error{Class: ClassBadRequest, HTTPStatus: status, Message: "The server refused the request (HTTP 400)."}
	case status >= 400:
		return &Error{Class: ClassUnavailable, HTTPStatus: status, Message: fmt.Sprintf("The server answered HTTP %d.", status)}
	case errors.As(err, &netErr):
		c.log().InfoContext(ctx, "mcp server unreachable", "err", t.safe(err.Error()))
		return &Error{Class: ClassUnavailable, Message: unreachableMessage(err)}
	case errors.As(err, &rpc):
		class := ClassBadResponse
		if rpc.Code == jsonrpc.CodeInvalidParams || rpc.Code == jsonrpc.CodeInvalidRequest {
			class = ClassBadRequest
		}
		return &Error{Class: class, RPCCode: rpc.Code, Message: "The server returned an error: " + t.safe(rpc.Message)}
	case errors.Is(err, io.ErrUnexpectedEOF):
		return &Error{Class: ClassBadResponse, Message: "The server's response was cut off."}
	}
	// Logged without the header value, should an error ever quote it.
	c.log().WarnContext(ctx, "mcp server exchange failed", "err", t.safe(err.Error()))
	return &Error{Class: ClassUnavailable, Message: unreachableMessage(err)}
}

// safe is SafeMessage with both the header value and the identity token
// redacted.
func (t Target) safe(msg string) string {
	if t.IdentityValue != "" {
		msg = strings.ReplaceAll(msg, t.IdentityValue, "[redacted]")
	}
	return SafeMessage(msg, t.HeaderValue)
}

// MaxMessage bounds a stored or returned message (in characters).
const MaxMessage = 300

// Secrets that must never be stored, even if a server echoes them in an
// error message: bearer tokens, OpenAI-style keys and key=value secrets.
var secretPattern = regexp.MustCompile(`(?i)\bbearer\s+\S+|\bsk-[A-Za-z0-9_\-*.]{6,}|\b(api[_-]?key|token|secret|password)\s*[=:]\s*\S+`)

// SafeMessage makes a message fit to store and show: the server's header
// value (when given) and anything that looks like a secret redacted,
// whitespace collapsed, at most MaxMessage characters.
func SafeMessage(msg, headerValue string) string {
	msg = strings.ToValidUTF8(msg, "")
	if headerValue != "" {
		msg = strings.ReplaceAll(msg, headerValue, "[redacted]")
	}
	msg = secretPattern.ReplaceAllString(msg, "[redacted]")
	msg = strings.Join(strings.Fields(msg), " ")
	if utf8.RuneCountInString(msg) > MaxMessage {
		msg = string([]rune(msg)[:MaxMessage-1]) + "…"
	}
	return msg
}

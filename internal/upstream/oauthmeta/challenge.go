// Package oauthmeta is the client side of the MCP authorization spec
// (2025-11-25) for MCP servers that are OAuth resource servers themselves
// (docs/phase7-upstream-oauth.md): WWW-Authenticate challenges (RFC 6750,
// RFC 9728 resource_metadata), protected-resource metadata (RFC 9728),
// authorization server metadata (RFC 8414 with path insertion, then OpenID
// Connect discovery), Dynamic Client Registration (RFC 7591) and the
// unauthenticated initialize probe of health checks.
//
// Every request goes through internal/upstream's address guard and the
// internal-network allowlist: the URL is checked before it is sent, the
// connection is dialled with the guarded dialer (every resolved address
// checked, connections pinned), proxies from the environment are ignored,
// redirects are refused, each request has its own deadline and response
// bodies are bounded. URLs found in answers (a resource_metadata parameter,
// authorization servers, endpoints) are untrusted input and go through the
// same checks.
package oauthmeta

import (
	"strings"
)

// Challenge is one parsed WWW-Authenticate challenge.
type Challenge struct {
	// Scheme as sent (compare case-insensitively).
	Scheme string
	// Params are the auth-params, keys lower-cased.
	Params map[string]string
}

// IsBearer reports whether the challenge's scheme is Bearer.
func (c Challenge) IsBearer() bool { return strings.EqualFold(c.Scheme, "Bearer") }

// ResourceMetadata is the RFC 9728 resource_metadata parameter ("": none).
func (c Challenge) ResourceMetadata() string { return c.Params["resource_metadata"] }

// Error is the RFC 6750 error parameter (invalid_token, insufficient_scope...).
func (c Challenge) Error() string { return c.Params["error"] }

// Scopes are the scope parameter's scopes (space-separated).
func (c Challenge) Scopes() []string {
	out := []string{}
	for _, s := range strings.Fields(c.Params["scope"]) {
		if !contains(out, s) {
			out = append(out, s)
		}
	}
	return out
}

func contains(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}

// maxHeader bounds the WWW-Authenticate values parsed.
const maxHeader = 8 << 10

// ParseChallenges parses WWW-Authenticate header values (RFC 9110 §11.6.1):
// one or more challenges, each a scheme followed by comma-separated
// auth-params (token=token or token="quoted"). A token68 is kept under the
// empty key. Malformed input yields the challenges read so far.
func ParseChallenges(values []string) []Challenge {
	var out []Challenge
	for _, v := range values {
		if len(v) > maxHeader {
			v = v[:maxHeader]
		}
		out = append(out, parseOne(v)...)
	}
	return out
}

// Bearer is the first Bearer challenge among header values (nil: none).
func Bearer(values []string) *Challenge {
	for _, c := range ParseChallenges(values) {
		if c.IsBearer() {
			c := c
			return &c
		}
	}
	return nil
}

func isTokenChar(b byte) bool {
	switch {
	case b >= 'a' && b <= 'z', b >= 'A' && b <= 'Z', b >= '0' && b <= '9':
		return true
	}
	return strings.IndexByte("!#$%&'*+-.^_`|~/=", b) >= 0
}

type lexer struct {
	s string
	i int
}

func (l *lexer) skipSpace() {
	for l.i < len(l.s) && (l.s[l.i] == ' ' || l.s[l.i] == '\t') {
		l.i++
	}
}

func (l *lexer) token() string {
	start := l.i
	for l.i < len(l.s) && isTokenChar(l.s[l.i]) && l.s[l.i] != '=' {
		l.i++
	}
	return l.s[start:l.i]
}

// value reads a quoted string, or (leniently) anything up to the next
// comma or space.
func (l *lexer) value() (string, bool) {
	if l.i < len(l.s) && l.s[l.i] == '"' {
		l.i++
		var b strings.Builder
		for l.i < len(l.s) {
			c := l.s[l.i]
			switch {
			case c == '\\' && l.i+1 < len(l.s):
				b.WriteByte(l.s[l.i+1])
				l.i += 2
			case c == '"':
				l.i++
				return b.String(), true
			default:
				b.WriteByte(c)
				l.i++
			}
		}
		return b.String(), false
	}
	start := l.i
	for l.i < len(l.s) && l.s[l.i] != ',' && l.s[l.i] != ' ' && l.s[l.i] != '\t' {
		l.i++
	}
	return l.s[start:l.i], l.i > start
}

func parseOne(s string) []Challenge {
	l := &lexer{s: s}
	var out []Challenge
	var cur *Challenge
	for {
		l.skipSpace()
		for l.i < len(l.s) && l.s[l.i] == ',' {
			l.i++
			l.skipSpace()
		}
		if l.i >= len(l.s) {
			break
		}
		start := l.i
		tok := l.token()
		if tok == "" {
			return out // garbage
		}
		l.skipSpace()
		if l.i < len(l.s) && l.s[l.i] == '=' && cur != nil {
			// An auth-param of the current challenge (or a token68 ending in =).
			l.i++
			l.skipSpace()
			v, ok := l.value()
			if !ok && v == "" {
				// token68 such as "abc==": keep it whole.
				cur.Params[""] = l.s[start:l.i]
				continue
			}
			cur.Params[strings.ToLower(tok)] = v
			continue
		}
		// A new challenge: scheme [ token68 | auth-params ].
		out = append(out, Challenge{Scheme: tok, Params: map[string]string{}})
		cur = &out[len(out)-1]
		// A token68 directly after the scheme.
		save := l.i
		t := l.token()
		l.skipSpace()
		if t != "" && (l.i >= len(l.s) || l.s[l.i] == ',') {
			cur.Params[""] = t
			continue
		}
		if t != "" && l.s[l.i] == '=' {
			// Could be "k=v" (param) or "abc==" (token68): params need a value.
			l.i = save
			continue
		}
		l.i = save
	}
	return out
}

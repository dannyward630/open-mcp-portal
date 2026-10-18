package registry

import (
	"net/url"
	"strings"

	"github.com/ncecere/open-mcp-portal/internal/store/dbgen"
)

// An origin is scheme, host and port, canonicalised: lower case, no trailing
// dot, the scheme's default port left out. A secret stored for a server is
// for that origin; a URL edit that moves to another one must not carry it
// along (docs/threat-model.md, F-1). Case, a trailing slash, an explicit
// default port and the path do not change the origin.
func originOf(raw string) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		// Unparsable: never equal to anything (a stored URL passed
		// NormalizeURL, so this is not reached in practice).
		return "invalid:" + raw
	}
	scheme := strings.ToLower(u.Scheme)
	host := strings.TrimSuffix(strings.ToLower(u.Hostname()), ".")
	if strings.Contains(host, ":") {
		host = "[" + host + "]"
	}
	port := u.Port()
	if (scheme == "https" && port == "443") || (scheme == "http" && port == "80") {
		port = ""
	}
	if port != "" {
		host += ":" + port
	}
	return scheme + "://" + host
}

// sameOrigin reports whether two URLs have the same origin.
func sameOrigin(a, b string) bool { return originOf(a) == originOf(b) }

// errAuthValueRequired: the URL moves to another origin, so the stored
// header value would be sent there.
var errAuthValueRequired = unprocessable("auth_value_required",
	"The URL moves to another host, so the stored header value can't be reused. Enter the header's value again, or remove the header.",
	map[string]any{"field": "authValue"})

// requireValueForOrigin refuses an update that would send the stored header
// value to another origin: a different origin than cur's needs a new value
// in the same request, or the header removed (or authMode none, which
// removes it). mode is the auth mode after the update.
func requireValueForOrigin(cur dbgen.McpServer, newURL string, in UpdateInput, mode string) error {
	switch {
	case mode == AuthNone, cur.AuthValueCipher == nil, in.AuthValue != nil:
		return nil
	case in.AuthHeaderName != nil && *in.AuthHeaderName == "":
		return nil
	case sameOrigin(cur.URL, newURL):
		return nil
	}
	return errAuthValueRequired
}

// originChange is an update's audit metadata for a URL change: always the old
// and new URL's origins when they differ.
func originChange(before, after string) map[string]any {
	if sameOrigin(before, after) {
		return nil
	}
	return map[string]any{"oldOrigin": originOf(before), "newOrigin": originOf(after)}
}

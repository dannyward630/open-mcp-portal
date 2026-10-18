package registry

import (
	"context"
	"net/url"
	"strings"

	"github.com/google/uuid"

	"github.com/ncecere/open-mcp-portal/internal/audit"
	"github.com/ncecere/open-mcp-portal/internal/authz"
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

// OriginOf is the canonical origin of a server URL (see originOf). Consent
// to send a person's provider token to a server is bound to it (F-3).
func OriginOf(raw string) string { return originOf(raw) }

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

// resetProviderGrants deletes every person's grant to use their provider
// connection at a server (F-3): its URL moved to another origin, its
// provider changed or went, or it is being deleted. Audited, when there was
// any, as mcp_server.provider_grants_reset (a count; no names, no secrets).
func resetProviderGrants(ctx context.Context, q *dbgen.Queries, a authz.Actor, teamID, serverID uuid.UUID, reason string, extra map[string]any) error {
	n, err := q.DeleteProviderServerGrantsForServer(ctx, serverID)
	if err != nil || n == 0 {
		return err
	}
	e := a.Audit("mcp_server.provider_grants_reset", "mcp_server", serverID.String())
	e.TeamID = teamID
	e.Metadata = map[string]any{"reason": reason, "grantsDeleted": n}
	for k, v := range extra {
		e.Metadata[k] = v
	}
	return audit.Record(ctx, q, e)
}

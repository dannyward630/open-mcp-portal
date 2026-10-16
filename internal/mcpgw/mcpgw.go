// Package mcpgw serves gateways at /mcp/{slug} (SPEC.md §6;
// docs/phase3-gateways.md "Runtime").
//
// It speaks MCP's Streamable HTTP transport statelessly: every request is a
// POST carrying one JSON-RPC message and is answered with one JSON
// response (GET and DELETE are 405: there are no sessions and no server
// stream). It implements initialize, ping, tools/list, tools/call,
// prompts/list, prompts/get, and (when the gateway selects resources or
// resource templates) resources/list, resources/templates/list and
// resources/read; other methods are "method not found", including
// resources/subscribe and server/discover, so clients of the 2026-07-28
// protocol fall back to initialize.
//
// Every request is authenticated (Authorization: Bearer ...) with a
// personal API key (omcp_...), an OAuth access token of a connected client
// (omat_..., internal/oauth; accepted only by its own gateway) or a service
// account key (omcp_sa_..., internal/serviceaccounts), and re-checks
// everything before it lists or calls: the credential and its person (or
// service account, and its team) are active, the key may be used on this
// gateway, the gateway is enabled (and a team gateway's team not archived)
// and the caller may use it, and each capability is still selected, served
// (Availability) and allowed to their role. A refused call looks like an unknown tool,
// prompt or resource, is recorded as denied and never reaches the server. Calls are
// forwarded through internal/upstream (address guard, the server's timeout,
// 1 MiB bounds, the server's static header) with a signed identity
// (internal/identity) and never the client's credential. Lists are served
// from the approved definitions in the database, without contacting
// servers. Every list and call is recorded in mcp_calls, without arguments
// or results.
package mcpgw

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ncecere/open-mcp-gateway/internal/apikeys"
	"github.com/ncecere/open-mcp-gateway/internal/apperr"
	"github.com/ncecere/open-mcp-gateway/internal/buildinfo"
	"github.com/ncecere/open-mcp-gateway/internal/calls"
	"github.com/ncecere/open-mcp-gateway/internal/gateways"
	"github.com/ncecere/open-mcp-gateway/internal/httpx"
	"github.com/ncecere/open-mcp-gateway/internal/identity"
	"github.com/ncecere/open-mcp-gateway/internal/oauth"
	"github.com/ncecere/open-mcp-gateway/internal/registry"
	"github.com/ncecere/open-mcp-gateway/internal/serviceaccounts"
	"github.com/ncecere/open-mcp-gateway/internal/store/dbgen"
	"github.com/ncecere/open-mcp-gateway/internal/upstream"
)

// Bounds.
const (
	// MaxRequestBytes bounds a client's request body.
	MaxRequestBytes = 1 << 20
	// MaxResponseBytes bounds a response to a call (a server's own response
	// is bounded by internal/upstream as well).
	MaxResponseBytes = 1 << 20
	// listPageBytes bounds the definitions in one list page.
	listPageBytes = 512 << 10
)

// Protocol versions this endpoint negotiates, newest first.
var protocolVersions = []string{"2025-11-25", "2025-06-18", "2025-03-26", "2024-11-05"}

// statelessVersion is the first protocol version without initialize; such
// versions in the MCP-Protocol-Version header are accepted (server/discover
// then fails, and the client falls back to initialize).
const statelessVersion = "2026-07-28"

// metaClientInfo is where 2026-07-28 clients put clientInfo in every
// request's _meta.
const metaClientInfo = "io.modelcontextprotocol/clientInfo"

// JSON-RPC error codes.
const (
	codeParseError     = -32700
	codeInvalidRequest = -32600
	codeMethodNotFound = -32601
	codeInvalidParams  = -32602
	codeInternal       = -32603
	// codeResourceNotFound is MCP's "resource not found".
	codeResourceNotFound = -32002
	// codeRefused is the implementation-defined code of HTTP-level refusals
	// (no credential, unknown gateway).
	codeRefused = -32001
)

// Methods.
const (
	methodInitialize  = "initialize"
	methodPing        = "ping"
	methodToolsList   = "tools/list"
	methodToolsCall   = "tools/call"
	methodPromptsList = "prompts/list"
	methodPromptsGet  = "prompts/get"
	methodResources   = "resources/list"
	methodTemplates   = "resources/templates/list"
	methodRead        = "resources/read"
)

// DeniedKeyScope is why a request with a key limited to other gateways is
// refused.
const DeniedKeyScope = "key_scope"

var slugRE = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]{0,46}[a-z0-9])?$`)

// Handler serves /mcp/{slug}.
type Handler struct {
	Gateways *gateways.Service
	Keys     *apikeys.Service
	// OAuth checks connected clients' access tokens (nil: none accepted).
	OAuth *oauth.Service
	// ServiceAccounts checks service account keys (nil: none accepted).
	ServiceAccounts *serviceaccounts.Service
	Identity        *identity.Service
	Registry        *registry.Service
	Pool            *pgxpool.Pool
	AppURL          string
	Log             *slog.Logger
}

func (h *Handler) log() *slog.Logger {
	if h.Log == nil {
		return slog.Default()
	}
	return h.Log
}

// message is one JSON-RPC message from a client.
type message struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   json.RawMessage `json:"error,omitempty"`
}

type rpcError struct {
	Code    int64  `json:"code"`
	Message string `json:"message"`
}

type response struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

var nullID = json.RawMessage("null")

// writeRPC writes one JSON-RPC response and returns its size.
func writeRPC(w http.ResponseWriter, status int, id json.RawMessage, result any, rerr *rpcError) int64 {
	if len(id) == 0 {
		id = nullID
	}
	res := response{JSONRPC: "2.0", ID: id, Result: result, Error: rerr}
	if rerr == nil && result == nil {
		res.Result = struct{}{}
	}
	b, err := json.Marshal(res)
	if err != nil {
		b, _ = json.Marshal(response{JSONRPC: "2.0", ID: id, Error: &rpcError{Code: codeInternal, Message: "The response can't be encoded"}})
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_, _ = w.Write(b)
	return int64(len(b))
}

func refuse(w http.ResponseWriter, status int, msg string) {
	writeRPC(w, status, nil, nil, &rpcError{Code: codeRefused, Message: msg})
}

// ServeHTTP handles every method on /mcp/{slug}.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	if r.Method != http.MethodPost {
		// Stateless: no server stream (GET) and no session to end (DELETE).
		w.Header().Set("Allow", "POST")
		refuse(w, http.StatusMethodNotAllowed, "This MCP endpoint accepts POST only")
		return
	}
	slug := r.PathValue("slug")
	if origin := r.Header.Get("Origin"); origin != "" && origin != h.AppURL {
		// DNS rebinding protection (Streamable HTTP §Security): browsers on
		// other origins are refused.
		refuse(w, http.StatusForbidden, "Origin not allowed")
		return
	}
	principal, ok := h.authenticate(w, r, slug)
	if !ok {
		return
	}
	if ct := r.Header.Get("Content-Type"); !strings.HasPrefix(strings.ToLower(strings.TrimSpace(ct)), "application/json") {
		refuse(w, http.StatusUnsupportedMediaType, "Content-Type must be application/json")
		return
	}
	if !acceptsJSON(r.Header.Values("Accept")) {
		refuse(w, http.StatusNotAcceptable, "Accept must include application/json")
		return
	}
	if v := r.Header.Get("MCP-Protocol-Version"); v != "" && !slices.Contains(protocolVersions, v) && v < statelessVersion {
		refuse(w, http.StatusBadRequest, "Unsupported MCP-Protocol-Version (supported: "+strings.Join(protocolVersions, ", ")+")")
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, MaxRequestBytes))
	if err != nil {
		var mbe *http.MaxBytesError
		if errors.As(err, &mbe) {
			refuse(w, http.StatusRequestEntityTooLarge, "The request is larger than 1 MiB")
			return
		}
		refuse(w, http.StatusBadRequest, "The request body can't be read")
		return
	}
	trimmed := bytes.TrimSpace(body)
	if len(trimmed) > 0 && trimmed[0] == '[' {
		writeRPC(w, http.StatusBadRequest, nil, nil, &rpcError{Code: codeInvalidRequest, Message: "JSON-RPC batches are not supported"})
		return
	}
	var msg message
	if err := json.Unmarshal(trimmed, &msg); err != nil {
		writeRPC(w, http.StatusBadRequest, nil, nil, &rpcError{Code: codeParseError, Message: "The request is not valid JSON"})
		return
	}
	if msg.JSONRPC != "2.0" || (msg.Method == "" && msg.Result == nil && msg.Error == nil) || bytes.Equal(msg.ID, nullID) {
		writeRPC(w, http.StatusBadRequest, msg.ID, nil, &rpcError{Code: codeInvalidRequest, Message: "Not a JSON-RPC 2.0 request"})
		return
	}

	rc := &request{h: h, w: w, r: r, start: start, msg: msg, principal: principal, requestBytes: int64(len(body))}
	if !rc.resolveGateway(slug) {
		return
	}
	if msg.Method == "" || len(msg.ID) == 0 {
		// A notification (notifications/initialized, cancelled) or a
		// response: accepted, nothing to answer.
		w.WriteHeader(http.StatusAccepted)
		return
	}
	rc.dispatch()
}

func acceptsJSON(values []string) bool {
	if len(values) == 0 {
		return true
	}
	for _, v := range values {
		for _, part := range strings.Split(v, ",") {
			mt := strings.ToLower(strings.TrimSpace(strings.Split(part, ";")[0]))
			if mt == "application/json" || mt == "application/*" || mt == "*/*" {
				return true
			}
		}
	}
	return false
}

// principal is who a request authenticates as: a person with an API key
// (calls.PrincipalAPIKey), a person through a connected OAuth client
// (calls.PrincipalOAuth), or a service account with one of its keys
// (calls.PrincipalServiceAccount).
type principal struct {
	kind string
	// user is the person (API keys and OAuth clients).
	user dbgen.User
	// keyID and keyName are the API key or service account key used.
	keyID   uuid.UUID
	keyName string
	// scope limits a key to these gateways (empty: every gateway its owner
	// may use). An OAuth token is bound to its gateway when authenticated.
	scope []uuid.UUID
	// grant is the connected client's grant (OAuth).
	grant *dbgen.OauthGrant
	// account is the service account and its team.
	account *serviceaccounts.Principal
}

func (p principal) inScope(gatewayID uuid.UUID) bool {
	return len(p.scope) == 0 || slices.Contains(p.scope, gatewayID)
}

// authenticate checks the credential: an OAuth access token (only for its
// own gateway), a service account key or an API key. Anything else is 401
// with a WWW-Authenticate challenge pointing at the gateway's protected
// resource metadata, where OAuth clients learn how to sign in.
func (h *Handler) authenticate(w http.ResponseWriter, r *http.Request, slug string) (principal, bool) {
	metadata := h.AppURL + "/.well-known/oauth-protected-resource/mcp"
	if slugRE.MatchString(slug) {
		metadata += "/" + slug
	}
	raw := r.Header.Get("Authorization")
	scheme, token, found := strings.Cut(raw, " ")
	token = strings.TrimSpace(token)
	if raw == "" || !found || !strings.EqualFold(scheme, "Bearer") || token == "" {
		w.Header().Set("WWW-Authenticate", `Bearer resource_metadata="`+metadata+`"`)
		refuse(w, http.StatusUnauthorized, "Sign in with OAuth, or send a key as Authorization: Bearer omcp_...")
		return principal{}, false
	}
	invalid := func(msg string) (principal, bool) {
		w.Header().Set("WWW-Authenticate", `Bearer error="invalid_token", resource_metadata="`+metadata+`"`)
		refuse(w, http.StatusUnauthorized, msg)
		return principal{}, false
	}
	failed := func(err error) (principal, bool) {
		h.log().ErrorContext(r.Context(), "mcp authentication failed", "err", err, "request_id", httpx.RequestID(r.Context()))
		refuse(w, http.StatusInternalServerError, "Something went wrong")
		return principal{}, false
	}
	ctx := r.Context()
	switch {
	case oauth.IsAccessToken(token):
		if h.OAuth == nil {
			return invalid("The access token is invalid, expired or revoked. Sign in again.")
		}
		p, err := h.OAuth.Authenticate(ctx, token, slug)
		if errors.Is(err, oauth.ErrInvalidToken) {
			return invalid("The access token is invalid, expired, revoked or for another gateway. Sign in again.")
		} else if err != nil {
			return failed(err)
		}
		if err := h.OAuth.Touch(ctx, p.Grant.ID); err != nil {
			h.log().WarnContext(ctx, "could not record a connected app's use", "err", err)
		}
		g := p.Grant
		return principal{kind: calls.PrincipalOAuth, user: p.User, grant: &g}, true
	case serviceaccounts.LooksLikeKey(token):
		if h.ServiceAccounts == nil {
			return invalid("The service account key is invalid, expired or revoked")
		}
		p, err := h.ServiceAccounts.Authenticate(ctx, token)
		if errors.Is(err, serviceaccounts.ErrInvalid) {
			return invalid("The service account key is invalid, expired or revoked, or the account is turned off")
		} else if err != nil {
			return failed(err)
		}
		if err := h.ServiceAccounts.Touch(ctx, p); err != nil {
			h.log().WarnContext(ctx, "could not record a service account key's use", "err", err)
		}
		return principal{kind: calls.PrincipalServiceAccount, keyID: p.Key.ID, keyName: p.Key.Name, scope: p.Gateways, account: &p}, true
	}
	p, err := h.Keys.Authenticate(ctx, token)
	if errors.Is(err, apikeys.ErrInvalid) {
		return invalid("The API key is invalid, expired or revoked")
	} else if err != nil {
		return failed(err)
	}
	if err := h.Keys.Touch(ctx, p.Key.ID); err != nil {
		h.log().WarnContext(ctx, "could not record API key use", "err", err)
	}
	return principal{kind: calls.PrincipalAPIKey, user: p.User, keyID: p.Key.ID, keyName: p.Key.Name, scope: p.Gateways}, true
}

// request is one exchange.
type request struct {
	h            *Handler
	w            http.ResponseWriter
	r            *http.Request
	start        time.Time
	msg          message
	principal    principal
	requestBytes int64
	gateway      dbgen.Gateway
	caller       gateways.Caller
}

func recorded(method string) bool {
	switch method {
	case methodToolsList, methodToolsCall, methodPromptsList, methodPromptsGet, methodResources, methodTemplates, methodRead:
		return true
	}
	return false
}

// resolveGateway finds the gateway and checks the key and the person may
// use it. An unknown or disabled gateway, a team gateway of an archived
// team, one the key is not limited to, or one they may not use, is 404
// (recorded as denied for lists and calls of an existing gateway).
func (rc *request) resolveGateway(slug string) bool {
	ctx := rc.r.Context()
	g, err := rc.h.Gateways.Resolve(ctx, slug)
	if err != nil {
		if !isNotFound(err) {
			rc.internal(err)
			return false
		}
		refuse(rc.w, http.StatusNotFound, "Gateway not found")
		return false
	}
	rc.gateway = g
	denied := ""
	if !rc.principal.inScope(g.ID) {
		denied = DeniedKeyScope
	} else if denied, err = rc.h.Gateways.Unserved(ctx, g); err != nil {
		rc.internal(err)
		return false
	}
	if denied == "" {
		var c gateways.Caller
		if sa := rc.principal.account; sa != nil {
			c, err = rc.h.Gateways.ServiceAccountCaller(ctx, g, sa.Team.ID, sa.Account.Role)
		} else {
			c, err = rc.h.Gateways.CallerOf(ctx, g, rc.principal.user)
		}
		if err != nil {
			rc.internal(err)
			return false
		}
		rc.caller = c
		if !c.Allowed() {
			denied = "no_access"
		}
	}
	if denied == "" {
		return true
	}
	n := int64(0)
	if rc.msg.Method != "" {
		n = writeRPC(rc.w, http.StatusNotFound, rc.msg.ID, nil, &rpcError{Code: codeRefused, Message: "Gateway not found"})
	} else {
		refuse(rc.w, http.StatusNotFound, "Gateway not found")
	}
	if recorded(rc.msg.Method) && len(rc.msg.ID) > 0 {
		rc.record(calls.Call{Outcome: calls.OutcomeDenied, ErrorClass: denied, ExposedName: rc.requestedName()}, n)
	}
	return false
}

func isNotFound(err error) bool {
	e, ok := apperr.As(err)
	return ok && e.Status == http.StatusNotFound
}

func (rc *request) internal(err error) {
	rc.h.log().ErrorContext(rc.r.Context(), "mcp request failed", "err", err, "request_id", httpx.RequestID(rc.r.Context()))
	writeRPC(rc.w, http.StatusInternalServerError, rc.msg.ID, nil, &rpcError{Code: codeInternal, Message: "Something went wrong"})
}

// clientName is clientInfo.name when this request carries it (initialize,
// or the _meta of 2026-07-28 requests).
func (rc *request) clientName() string {
	var p struct {
		ClientInfo *struct {
			Name string `json:"name"`
		} `json:"clientInfo"`
		Meta map[string]json.RawMessage `json:"_meta"`
	}
	if len(rc.msg.Params) == 0 || json.Unmarshal(rc.msg.Params, &p) != nil {
		return ""
	}
	if p.ClientInfo != nil {
		return p.ClientInfo.Name
	}
	var info struct {
		Name string `json:"name"`
	}
	if raw, ok := p.Meta[metaClientInfo]; ok && json.Unmarshal(raw, &info) == nil {
		return info.Name
	}
	return ""
}

// requestedName is params.name of a call, or params.uri of a read, for
// records (bounded by calls.Record).
func (rc *request) requestedName() string {
	var p struct {
		Name string `json:"name"`
		URI  string `json:"uri"`
	}
	switch rc.msg.Method {
	case methodToolsCall, methodPromptsGet:
		_ = json.Unmarshal(rc.msg.Params, &p)
		return p.Name
	case methodRead:
		_ = json.Unmarshal(rc.msg.Params, &p)
		return p.URI
	}
	return ""
}

// record stores the exchange's metadata (never its arguments or result).
func (rc *request) record(c calls.Call, responseBytes int64) {
	c.RequestID, c.Method, c.Gateway = httpx.RequestID(rc.r.Context()), rc.msg.Method, rc.gateway
	p := rc.principal
	c.PrincipalKind, c.APIKeyID, c.KeyName = p.kind, p.keyID, p.keyName
	switch {
	case p.account != nil:
		c.ServiceAccountID, c.ServiceAccountName, c.ServiceAccountTeamID = p.account.Account.ID, p.account.Account.Name, p.account.Team.ID
	case p.grant != nil:
		c.UserID, c.OAuthGrantID, c.OAuthClientName, c.OAuthClientKind = p.user.ID, p.grant.ID, p.grant.ClientName, p.grant.ClientKind
	default:
		c.UserID = p.user.ID
	}
	c.ClientName = rc.clientName()
	c.Latency, c.RequestBytes, c.ResponseBytes = time.Since(rc.start), rc.requestBytes, responseBytes
	ctx, cancel := context.WithTimeout(context.WithoutCancel(rc.r.Context()), 5*time.Second)
	defer cancel()
	if err := calls.Record(ctx, dbgen.New(rc.h.Pool), c); err != nil {
		rc.h.log().WarnContext(ctx, "could not record an MCP call", "err", err, "request_id", c.RequestID)
	}
	rc.h.log().DebugContext(ctx, "mcp call", "gateway", rc.gateway.Slug, "method", c.Method, "name", c.ExposedName,
		"outcome", c.Outcome, "error_class", c.ErrorClass, "latency_ms", c.Latency.Milliseconds(), "request_id", c.RequestID)
}

func (rc *request) reply(result any) int64 {
	return writeRPC(rc.w, http.StatusOK, rc.msg.ID, result, nil)
}

func (rc *request) fail(code int64, msg string) int64 {
	return writeRPC(rc.w, http.StatusOK, rc.msg.ID, nil, &rpcError{Code: code, Message: msg})
}

func (rc *request) dispatch() {
	switch rc.msg.Method {
	case methodInitialize:
		rc.initialize()
	case methodPing:
		rc.reply(struct{}{})
	case methodToolsList:
		rc.list(upstream.KindTool, "tools")
	case methodPromptsList:
		rc.list(upstream.KindPrompt, "prompts")
	case methodToolsCall:
		rc.call(upstream.KindTool)
	case methodPromptsGet:
		rc.call(upstream.KindPrompt)
	case methodResources, methodTemplates, methodRead:
		has, err := rc.hasResources()
		switch {
		case err != nil:
			rc.internal(err)
		case !has:
			rc.methodNotFound()
		case rc.msg.Method == methodResources:
			rc.list(upstream.KindResource, "resources")
		case rc.msg.Method == methodTemplates:
			rc.list(upstream.KindResourceTemplate, "resourceTemplates")
		default:
			rc.read()
		}
	default:
		rc.methodNotFound()
	}
}

func (rc *request) methodNotFound() {
	rc.fail(codeMethodNotFound, "Method not found: "+clip(rc.msg.Method, 100))
}

// hasResources reports whether the gateway selects any resource or
// resource template (whether or not served now): only then does it
// advertise and serve the resources methods.
func (rc *request) hasResources() (bool, error) {
	all, err := rc.h.Gateways.Offers(rc.r.Context(), rc.gateway, rc.caller)
	if err != nil {
		return false, err
	}
	for _, o := range all {
		if gateways.IsResourceKind(o.Capability.Kind) {
			return true, nil
		}
	}
	return false, nil
}

func clip(s string, n int) string {
	r := []rune(s)
	if len(r) > n {
		return string(r[:n])
	}
	return s
}

// initialize negotiates the protocol version (the client's when supported,
// else the newest) and advertises tools and prompts, and resources when the
// gateway selects any (no subscriptions).
func (rc *request) initialize() {
	var p struct {
		ProtocolVersion string `json:"protocolVersion"`
	}
	if len(rc.msg.Params) > 0 && json.Unmarshal(rc.msg.Params, &p) != nil {
		rc.fail(codeInvalidParams, "Invalid initialize parameters")
		return
	}
	version := protocolVersions[0]
	if slices.Contains(protocolVersions, p.ProtocolVersion) {
		version = p.ProtocolVersion
	}
	caps := map[string]any{
		"tools":   map[string]any{"listChanged": false},
		"prompts": map[string]any{"listChanged": false},
	}
	if has, err := rc.hasResources(); err != nil {
		rc.internal(err)
		return
	} else if has {
		caps["resources"] = map[string]any{"listChanged": false, "subscribe": false}
	}
	rc.reply(map[string]any{
		"protocolVersion": version,
		"capabilities":    caps,
		"serverInfo":      map[string]any{"name": rc.gateway.Slug, "title": rc.gateway.Name, "version": buildinfo.Version},
	})
}

// offers reads what the caller may use of kind now, by exposed name.
func (rc *request) offers(kind string) ([]gateways.Offer, error) {
	all, err := rc.h.Gateways.Offers(rc.r.Context(), rc.gateway, rc.caller)
	if err != nil {
		return nil, err
	}
	out := make([]gateways.Offer, 0, len(all))
	for _, o := range all {
		if o.Capability.Kind == kind {
			out = append(out, o)
		}
	}
	slices.SortFunc(out, func(a, b gateways.Offer) int { return strings.Compare(a.ExposedName, b.ExposedName) })
	return out, nil
}

// listed is a stored definition with the exposed name (tools, prompts) or
// URI (resources: uri, templates: uriTemplate); a resource's own name,
// title, description and mimeType stay as approved.
func listed(o gateways.Offer) (json.RawMessage, error) {
	def := map[string]json.RawMessage{}
	if err := json.Unmarshal(o.Capability.Definition, &def); err != nil {
		return nil, err
	}
	exposed, _ := json.Marshal(o.ExposedName)
	switch o.Capability.Kind {
	case upstream.KindResource:
		def["uri"] = exposed
	case upstream.KindResourceTemplate:
		def["uriTemplate"] = exposed
	default:
		def["name"] = exposed
	}
	delete(def, "_meta")
	return json.Marshal(def)
}

// list serves tools/list, prompts/list, resources/list or
// resources/templates/list from the approved definitions, paged by size;
// the cursor is the last exposed name (or URI) of the page.
func (rc *request) list(kind, key string) {
	var p struct {
		Cursor string `json:"cursor"`
	}
	if len(rc.msg.Params) > 0 && json.Unmarshal(rc.msg.Params, &p) != nil {
		n := rc.fail(codeInvalidParams, "Invalid list parameters")
		rc.record(calls.Call{Outcome: calls.OutcomeBadRequest, ErrorClass: "invalid_params"}, n)
		return
	}
	after := ""
	if p.Cursor != "" {
		b, err := base64.RawURLEncoding.DecodeString(p.Cursor)
		if err != nil || len(b) == 0 {
			n := rc.fail(codeInvalidParams, "Invalid cursor")
			rc.record(calls.Call{Outcome: calls.OutcomeBadRequest, ErrorClass: "invalid_cursor"}, n)
			return
		}
		after = string(b)
	}
	offers, err := rc.offers(kind)
	if err != nil {
		rc.internal(err)
		return
	}
	items := []json.RawMessage{}
	size, last, next := 0, "", ""
	for _, o := range offers {
		if !o.Allowed || (after != "" && o.ExposedName <= after) {
			continue
		}
		def, err := listed(o)
		if err != nil {
			rc.internal(err)
			return
		}
		if size+len(def) > listPageBytes && len(items) > 0 {
			next = base64.RawURLEncoding.EncodeToString([]byte(last))
			break
		}
		size, last = size+len(def), o.ExposedName
		items = append(items, def)
	}
	result := map[string]any{key: items}
	if next != "" {
		result["nextCursor"] = next
	}
	n := rc.reply(result)
	rc.record(calls.Call{Outcome: calls.OutcomeOK}, n)
}

package testutil

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// FakeMCP is a remote MCP server for tests of the registry and for local
// development (cmd/fakemcp), built with the official Go SDK and served
// statelessly over Streamable HTTP at URL() (adapted from Grounded). It
// lists:
//
//   - tools check_outage {service}, search_tickets {query} and
//     create_ticket {title} (annotated destructive);
//   - the resource status://services;
//   - the resource template status://services/{service};
//   - the prompt incident_summary {service}.
//
// RequireHeader makes it answer 401 to requests without a header;
// SetDescription, Hide and AddTool change what tools/list returns (change
// detection); SetPageSize makes every list paginate. EnableGatewayTools adds
// whoami (the identity the portal sent) and echo, and records every tool
// call (ToolCalls) for gateway tests.
type FakeMCP struct {
	*httptest.Server

	mu           sync.Mutex
	header       string
	value        string
	hidden       map[string]bool
	descriptions map[string]string
	extra        []string
	pageSize     int
	requests     int

	gatewayTools bool
	// echoSecrets adds echo_secrets and image (payload capture tests).
	echoSecrets bool
	// schemaTools adds lookup (EnableSchemaTools).
	schemaTools bool
	jwksURL     string
	audience    string
	toolCalls   []ToolCall
	reads       []ToolCall
	// OnToolCall, when set, sees every tool call and resource read
	// (cmd/fakemcp logs them).
	OnToolCall func(ToolCall)
	// protect makes the fake an OAuth resource server (Protect).
	protect *Protection
}

// Protection makes the fake an OAuth-protected MCP server (Phase 7): it
// answers 401 with a Bearer challenge naming its protected-resource
// metadata, serves that metadata, and accepts only bearer tokens its
// authorization server says are active and bound to this server's URL.
type Protection struct {
	// Issuer is the authorization server listed in the metadata.
	Issuer string
	// Introspect asks the authorization server about a token.
	Introspect func(ctx context.Context, token string) Introspection
	// ScopesSupported are listed in the metadata; ChallengeScope is the
	// 401 challenge's scope ("": none).
	ScopesSupported []string
	ChallengeScope  string
	// Resource overrides the metadata's resource ("": this server's URL).
	Resource string
	// NoResourceMetadataParam leaves resource_metadata out of the 401 (the
	// metadata is then found at the well-known URI).
	NoResourceMetadataParam bool
	// WriteScope is the scope the write tool needs (403 insufficient_scope
	// without it; default "write").
	WriteScope string
	// IgnoreResource accepts tokens bound to another resource (or none).
	IgnoreResource bool
}

// Protect makes the fake an OAuth resource server, and adds the write tool.
func (f *FakeMCP) Protect(p Protection) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if p.WriteScope == "" {
		p.WriteScope = "write"
	}
	f.protect = &p
}

// SetProtection changes the protection under the fake's lock.
func (f *FakeMCP) SetProtection(change func(p *Protection)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	change(f.protect)
}

func (f *FakeMCP) protection() *Protection {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.protect == nil {
		return nil
	}
	p := *f.protect
	return &p
}

// selfURL is the fake's endpoint as a request reached it.
func selfURL(r *http.Request) string { return "http://" + r.Host + "/mcp" }

func metadataURL(r *http.Request) string {
	return "http://" + r.Host + "/.well-known/oauth-protected-resource/mcp"
}

// guard enforces Protection on an MCP request; false when it answered.
func (f *FakeMCP) guard(w http.ResponseWriter, r *http.Request, p *Protection) bool {
	challenge := func(status int, errCode, scope string) bool {
		parts := []string{}
		if errCode != "" {
			parts = append(parts, `error="`+errCode+`"`)
		}
		if scope != "" {
			parts = append(parts, `scope="`+scope+`"`)
		}
		if !p.NoResourceMetadataParam {
			parts = append(parts, `resource_metadata="`+metadataURL(r)+`"`)
		}
		w.Header().Set("WWW-Authenticate", "Bearer "+strings.Join(parts, ", "))
		http.Error(w, http.StatusText(status), status)
		return false
	}
	token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !ok || token == "" {
		return challenge(http.StatusUnauthorized, "", p.ChallengeScope)
	}
	in := p.Introspect(r.Context(), token)
	resource := p.Resource
	if resource == "" {
		resource = selfURL(r)
	}
	if !in.Active || (!p.IgnoreResource && in.Resource != resource) {
		return challenge(http.StatusUnauthorized, "invalid_token", p.ChallengeScope)
	}
	// The write tool needs WriteScope (a step-up).
	body, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	r.Body = io.NopCloser(bytes.NewReader(body))
	var msg struct {
		Method string `json:"method"`
		Params struct {
			Name string `json:"name"`
		} `json:"params"`
	}
	_ = json.Unmarshal(body, &msg)
	if msg.Method == "tools/call" && msg.Params.Name == "write" && !slices.Contains(in.Scopes, p.WriteScope) {
		return challenge(http.StatusForbidden, "insufficient_scope", strings.Join(append(append([]string{}, in.Scopes...), p.WriteScope), " "))
	}
	return true
}

func (f *FakeMCP) serveResourceMetadata(w http.ResponseWriter, r *http.Request, p *Protection) {
	resource := p.Resource
	if resource == "" {
		resource = selfURL(r)
	}
	scopes := p.ScopesSupported
	if scopes == nil {
		scopes = []string{}
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"resource": resource, "authorization_servers": []string{p.Issuer},
		"scopes_supported": scopes, "bearer_methods_supported": []string{"header"}, "resource_name": "Fake protected MCP"})
}

// IntrospectAt asks a fake OAuth provider's /introspect over HTTP
// (cmd/fakemcp -protect).
func IntrospectAt(endpoint string) func(ctx context.Context, token string) Introspection {
	return func(ctx context.Context, token string) Introspection {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(url.Values{"token": {token}}.Encode()))
		if err != nil {
			return Introspection{}
		}
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			return Introspection{}
		}
		defer res.Body.Close()
		var out Introspection
		_ = json.NewDecoder(io.LimitReader(res.Body, 64<<10)).Decode(&out)
		return out
	}
}

// ToolCall is what the fake received with one tool call (or resource
// read: Tool is then "resources/read" and URI the URI read).
type ToolCall struct {
	Tool string
	URI  string
	// Authorization and Identity are the Authorization and
	// MCP-Gateway-Identity headers as received.
	Authorization, Identity string
	// Header is every request header.
	Header http.Header
}

// FakeMCPTools is how many tools the fake lists by default; with the
// resource, template and prompt it lists FakeMCPCapabilities in all.
const (
	FakeMCPTools        = 3
	FakeMCPCapabilities = 6
)

// NewFakeMCP starts the fake on a loopback address (closed with the test).
func NewFakeMCP(t testing.TB) *FakeMCP {
	f, h := NewFakeMCPHandler()
	f.Server = httptest.NewServer(h)
	t.Cleanup(f.Close)
	return f
}

// NewFakeMCPHandler builds the fake without starting a server (cmd/fakemcp
// serves it; URL() then isn't set).
func NewFakeMCPHandler() (*FakeMCP, http.Handler) {
	f := &FakeMCP{hidden: map[string]bool{}, descriptions: map[string]string{}}
	h := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return f.server() },
		&mcp.StreamableHTTPOptions{Stateless: true, JSONResponse: true})
	return f, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.requests++
		name, value := f.header, f.value
		f.mu.Unlock()
		if p := f.protection(); p != nil {
			if r.URL.Path == "/.well-known/oauth-protected-resource/mcp" {
				f.serveResourceMetadata(w, r, p)
				return
			}
			if strings.HasPrefix(r.URL.Path, "/.well-known/") {
				http.NotFound(w, r)
				return
			}
			if !f.guard(w, r, p) {
				return
			}
		}
		if name != "" && r.Header.Get(name) != value {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		h.ServeHTTP(w, r)
	})
}

// URL is the Streamable HTTP endpoint.
func (f *FakeMCP) URL() string { return f.Server.URL + "/mcp" }

// RequireHeader makes every request need name: value.
func (f *FakeMCP) RequireHeader(name, value string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.header, f.value = name, value
}

// SetDescription changes a tool's description in tools/list.
func (f *FakeMCP) SetDescription(tool, description string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.descriptions[tool] = description
}

// Hide removes a tool, the resource, the template or the prompt (by name
// or URI) from its list.
func (f *FakeMCP) Hide(name string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.hidden[name] = true
}

// Show undoes Hide.
func (f *FakeMCP) Show(name string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.hidden, name)
}

// AddTool lists one more (trivial) tool.
func (f *FakeMCP) AddTool(name string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.extra = append(f.extra, name)
}

// SetPageSize sets the list page size (0: the SDK default).
func (f *FakeMCP) SetPageSize(n int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.pageSize = n
}

// EnableGatewayTools adds the tools whoami, provider_token and echo {text}.
// whoami returns the identity token it received as JSON: which header
// carried it, its claims and, with SetJWKS, whether it verifies.
// provider_token reports the delegated credential it received
// (ProviderTokenReport): never the token, only a digest prefix.
func (f *FakeMCP) EnableGatewayTools() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.gatewayTools = true
}

// EnableEchoSecrets adds two tools for payload capture tests: echo_secrets
// returns, as text, every credential it received (the identity token, the
// provider token and the Authorization and X-Api-Key header values), as a
// misbehaving server might; image returns an image content item. Never
// enabled by cmd/fakemcp.
func (f *FakeMCP) EnableEchoSecrets() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.echoSecrets = true
}

// EnableSchemaTools adds lookup {region, limit, note}, a tool with features of
// the 2026-07-28 revision: its region and limit arguments are marked
// x-mcp-header (Region, Limit), its outputSchema is not an object schema
// (["null","array"]) and its result's structuredContent is an array. It
// records its calls (ToolCalls, with the headers it received).
func (f *FakeMCP) EnableSchemaTools() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.schemaTools = true
}

// SetJWKS makes whoami verify the token against the key set at url, for
// audience.
func (f *FakeMCP) SetJWKS(url, audience string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.jwksURL, f.audience = url, audience
}

// ToolCalls are the tool calls received so far.
func (f *FakeMCP) ToolCalls() []ToolCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]ToolCall(nil), f.toolCalls...)
}

func (f *FakeMCP) sawCall(tool string, h http.Header) {
	c := ToolCall{Tool: tool, Authorization: h.Get("Authorization"), Identity: h.Get("MCP-Gateway-Identity"), Header: h.Clone()}
	f.mu.Lock()
	f.toolCalls = append(f.toolCalls, c)
	hook := f.OnToolCall
	f.mu.Unlock()
	if hook != nil {
		hook(c)
	}
}

// ResourceReads are the resource reads received so far.
func (f *FakeMCP) ResourceReads() []ToolCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]ToolCall(nil), f.reads...)
}

func (f *FakeMCP) sawRead(req *mcp.ReadResourceRequest) {
	h := http.Header{}
	if req.Extra != nil && req.Extra.Header != nil {
		h = req.Extra.Header
	}
	c := ToolCall{Tool: "resources/read", URI: req.Params.URI, Authorization: h.Get("Authorization"),
		Identity: h.Get("MCP-Gateway-Identity"), Header: h.Clone()}
	f.mu.Lock()
	f.reads = append(f.reads, c)
	hook := f.OnToolCall
	f.mu.Unlock()
	if hook != nil {
		hook(c)
	}
}

func headerOf(req *mcp.CallToolRequest) http.Header {
	if req.Extra == nil || req.Extra.Header == nil {
		return http.Header{}
	}
	return req.Extra.Header
}

// Requests counts every HTTP request.
func (f *FakeMCP) Requests() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.requests
}

func fakeText(s string) *mcp.CallToolResult {
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: s}}}
}

func stringArg(name, description string) map[string]any {
	return map[string]any{"type": "object", "required": []string{name},
		"properties": map[string]any{name: map[string]any{"type": "string", "description": description}}}
}

// server builds the MCP server of one request from the current state.
func (f *FakeMCP) server() *mcp.Server {
	f.mu.Lock()
	defer f.mu.Unlock()
	s := mcp.NewServer(&mcp.Implementation{Name: "fake-status", Version: "1.0.0"}, &mcp.ServerOptions{PageSize: f.pageSize})
	yes := true
	tools := []*mcp.Tool{
		{Name: "check_outage", Title: "Check outage", Description: "Reports whether a company service has an outage right now.",
			InputSchema: stringArg("service", "The service, for example email")},
		{Name: "search_tickets", Title: "Search tickets", Description: "Searches open support tickets.",
			InputSchema: stringArg("query", "Words to look for")},
		{Name: "create_ticket", Title: "Create ticket", Description: "Opens a support ticket.",
			InputSchema: stringArg("title", "The ticket's title"), Annotations: &mcp.ToolAnnotations{DestructiveHint: &yes}},
	}
	for _, name := range f.extra {
		tools = append(tools, &mcp.Tool{Name: name, Description: "An added tool.", InputSchema: map[string]any{"type": "object"}})
	}
	for _, t := range tools {
		if f.hidden[t.Name] {
			continue
		}
		if d, ok := f.descriptions[t.Name]; ok {
			t.Description = d
		}
		name := t.Name
		s.AddTool(t, func(_ context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			f.sawCall(name, headerOf(req))
			return fakeText(fmt.Sprintf("%s: ok", name)), nil
		})
	}
	if f.gatewayTools {
		jwksURL, audience := f.jwksURL, f.audience
		describe := func(name, d string) string {
			if v, ok := f.descriptions[name]; ok {
				return v
			}
			return d
		}
		protected := f.protect != nil
		s.AddTool(&mcp.Tool{Name: "whoami", Title: "Who am I", Description: describe("whoami", "Shows the identity the portal sent with this call."),
			InputSchema: map[string]any{"type": "object"}}, func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			h := headerOf(req)
			f.sawCall("whoami", h)
			if !protected {
				out, _ := json.Marshal(Whoami(ctx, h, jwksURL, audience))
				return fakeText(string(out)), nil
			}
			// Protected: Authorization holds the person's token, never shown;
			// the identity token can only be in MCP-Gateway-Identity.
			bearer := strings.TrimPrefix(h.Get("Authorization"), "Bearer ")
			only := h.Clone()
			only.Del("Authorization")
			who := Whoami(ctx, only, jwksURL, audience)
			if bearer != "" {
				who.BearerSHA256 = TokenDigest(bearer)
			}
			out, _ := json.Marshal(who)
			return fakeText(string(out)), nil
		})
		s.AddTool(&mcp.Tool{Name: "provider_token", Title: "Provider token",
			Description: describe("provider_token", "Reports whether the portal sent a provider token with this call (never the token itself)."),
			InputSchema: map[string]any{"type": "object"}}, func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			h := headerOf(req)
			f.sawCall("provider_token", h)
			out, _ := json.Marshal(ProviderTokenReport(ctx, h, jwksURL, audience))
			return fakeText(string(out)), nil
		})
		s.AddTool(&mcp.Tool{Name: "echo", Title: "Echo", Description: describe("echo", "Returns its text."), InputSchema: stringArg("text", "Any text")},
			func(_ context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
				f.sawCall("echo", headerOf(req))
				var args struct {
					Text string `json:"text"`
				}
				_ = json.Unmarshal(req.Params.Arguments, &args)
				return fakeText(args.Text), nil
			})
	}
	if f.schemaTools {
		s.AddTool(&mcp.Tool{Name: "lookup", Title: "Lookup", Description: "Looks something up (arrays, parameter headers).",
			InputSchema: map[string]any{"type": "object", "properties": map[string]any{
				"region": map[string]any{"type": "string", "x-mcp-header": "Region"},
				"limit":  map[string]any{"type": "integer", "x-mcp-header": "Limit"},
				"note":   map[string]any{"type": "string"},
			}},
			OutputSchema: map[string]any{"type": []string{"null", "array"}}},
			func(_ context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
				f.sawCall("lookup", headerOf(req))
				var args struct {
					Region string `json:"region"`
					Limit  int    `json:"limit"`
				}
				_ = json.Unmarshal(req.Params.Arguments, &args)
				return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "lookup: ok"}},
					StructuredContent: []any{map[string]any{"region": args.Region, "limit": args.Limit}}}, nil
			})
	}
	if f.protect != nil {
		s.AddTool(&mcp.Tool{Name: "write", Title: "Write", Description: "Needs the write scope.", InputSchema: map[string]any{"type": "object"}},
			func(_ context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
				f.sawCall("write", headerOf(req))
				return fakeText("written"), nil
			})
	}
	if f.echoSecrets {
		s.AddTool(&mcp.Tool{Name: "echo_secrets", Title: "Echo secrets", Description: "Returns the credentials it received.",
			InputSchema: map[string]any{"type": "object"}}, func(_ context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			h := headerOf(req)
			f.sawCall("echo_secrets", h)
			_, id := IdentityToken(h)
			return &mcp.CallToolResult{Content: []mcp.Content{
				&mcp.TextContent{Text: fmt.Sprintf("identity=%s provider=%s authorization=%s apikey=%s ", id,
					h.Get("MCP-Gateway-Provider-Token"), h.Get("Authorization"), h.Get("X-Api-Key"))},
				&mcp.TextContent{Text: string(req.Params.Arguments)},
			}}, nil
		})
		s.AddTool(&mcp.Tool{Name: "image", Title: "Image", Description: "Returns an image.", InputSchema: map[string]any{"type": "object"}},
			func(_ context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
				f.sawCall("image", headerOf(req))
				return &mcp.CallToolResult{Content: []mcp.Content{&mcp.ImageContent{Data: []byte("0123456789abcdef0123456789abcdef"), MIMEType: "image/png"}}}, nil
			})
	}
	if !f.hidden["status://services"] {
		s.AddResource(&mcp.Resource{URI: "status://services", Name: "services", Title: "Services", Description: "Every monitored service.",
			MIMEType: "text/plain"}, func(_ context.Context, req *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
			f.sawRead(req)
			return &mcp.ReadResourceResult{Contents: []*mcp.ResourceContents{{URI: req.Params.URI, MIMEType: "text/plain", Text: "email\nvpn"}}}, nil
		})
	}
	if !f.hidden["status://services/{service}"] {
		s.AddResourceTemplate(&mcp.ResourceTemplate{URITemplate: "status://services/{service}", Name: "service", Title: "Service status",
			Description: "The status of one service.", MIMEType: "text/plain"},
			func(_ context.Context, req *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
				f.sawRead(req)
				return &mcp.ReadResourceResult{Contents: []*mcp.ResourceContents{{URI: req.Params.URI, MIMEType: "text/plain", Text: "operating normally"}}}, nil
			})
	}
	if !f.hidden["incident_summary"] {
		s.AddPrompt(&mcp.Prompt{Name: "incident_summary", Title: "Incident summary", Description: "Summarise an incident for a service.",
			Arguments: []*mcp.PromptArgument{{Name: "service", Description: "The affected service", Required: true}}},
			func(_ context.Context, req *mcp.GetPromptRequest) (*mcp.GetPromptResult, error) {
				return &mcp.GetPromptResult{Messages: []*mcp.PromptMessage{{Role: "user",
					Content: &mcp.TextContent{Text: "Summarise the incident for " + req.Params.Arguments["service"]}}}}, nil
			})
	}
	return s
}

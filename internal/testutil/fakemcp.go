package testutil

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
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
// whoami (the identity the gateway sent) and echo, and records every tool
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
	jwksURL      string
	audience     string
	toolCalls    []ToolCall
	reads        []ToolCall
	// OnToolCall, when set, sees every tool call and resource read
	// (cmd/fakemcp logs them).
	OnToolCall func(ToolCall)
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
		s.AddTool(&mcp.Tool{Name: "whoami", Title: "Who am I", Description: describe("whoami", "Shows the identity the gateway sent with this call."),
			InputSchema: map[string]any{"type": "object"}}, func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			h := headerOf(req)
			f.sawCall("whoami", h)
			out, _ := json.Marshal(Whoami(ctx, h, jwksURL, audience))
			return fakeText(string(out)), nil
		})
		s.AddTool(&mcp.Tool{Name: "provider_token", Title: "Provider token",
			Description: describe("provider_token", "Reports whether the gateway sent a provider token with this call (never the token itself)."),
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

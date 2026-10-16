package testutil

import (
	"context"
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
// detection); SetPageSize makes every list paginate.
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
			return fakeText(fmt.Sprintf("%s: ok", name)), nil
		})
	}
	if !f.hidden["status://services"] {
		s.AddResource(&mcp.Resource{URI: "status://services", Name: "services", Title: "Services", Description: "Every monitored service.",
			MIMEType: "text/plain"}, func(_ context.Context, req *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
			return &mcp.ReadResourceResult{Contents: []*mcp.ResourceContents{{URI: req.Params.URI, MIMEType: "text/plain", Text: "email\nvpn"}}}, nil
		})
	}
	if !f.hidden["status://services/{service}"] {
		s.AddResourceTemplate(&mcp.ResourceTemplate{URITemplate: "status://services/{service}", Name: "service", Title: "Service status",
			Description: "The status of one service.", MIMEType: "text/plain"},
			func(_ context.Context, req *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
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

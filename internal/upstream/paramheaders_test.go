package upstream

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const githubLike = `{"name":"list_branches","inputSchema":{"type":"object","required":["owner","repo"],"properties":{
"owner":{"type":"string","x-mcp-header":"owner"},"repo":{"type":"string","x-mcp-header":"repo"},
"page":{"type":"number"},"draft":{"type":"boolean","x-mcp-header":"Draft"},"num":{"type":"integer","x-mcp-header":"Num"},
"filter":{"type":"object","properties":{"label":{"type":"string","x-mcp-header":"Label"}}},
"bad":{"type":"string","x-mcp-header":"no spaces allowed"}}}}`

func TestParamHeaders(t *testing.T) {
	got := ParamHeaders(json.RawMessage(githubLike), json.RawMessage(
		`{"owner":"ncecere","repo":"résumé","page":2,"draft":true,"num":1.5,"filter":{"label":" bug"},"bad":"x"}`))
	want := map[string]string{
		"Mcp-Param-owner": "ncecere",
		"Mcp-Param-repo":  "=?base64?csOpc3Vtw6k=?=",
		"Mcp-Param-Draft": "true",
		"Mcp-Param-Label": "=?base64?IGJ1Zw==?=",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("ParamHeaders = %v, want %v", got, want)
	}
	if h := ParamHeaders(json.RawMessage(`{"name":"get_me","inputSchema":{"type":"object"}}`), json.RawMessage(`{}`)); h != nil {
		t.Errorf("no annotations = %v", h)
	}
	if h := ParamHeaders(json.RawMessage(githubLike), nil); h != nil {
		t.Errorf("no arguments = %v", h)
	}
}

// The headers go on the tools/call request only.
func TestParamHeadersOnToolsCallOnly(t *testing.T) {
	var mu sync.Mutex
	seen := map[string]string{}
	srv := mcp.NewServer(&mcp.Implementation{Name: "p", Version: "1"}, nil)
	srv.AddTool(&mcp.Tool{Name: "list_branches", InputSchema: json.RawMessage(`{"type":"object"}`)},
		func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "ok"}}}, nil
		})
	h := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return srv }, &mcp.StreamableHTTPOptions{Stateless: true})
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if v := r.Header.Get("Mcp-Param-repo"); v != "" {
			mu.Lock()
			seen[r.Header.Get("Mcp-Method")+"|"+v] = v
			mu.Unlock()
		}
		h.ServeHTTP(w, r)
	}))
	defer ts.Close()
	target := Target{URL: ts.URL, Timeout: 5 * time.Second,
		ParamHeaders: ParamHeaders(json.RawMessage(githubLike), json.RawMessage(`{"owner":"o","repo":"r"}`))}
	if _, _, err := testClient(true).CallTool(context.Background(), target, "list_branches", json.RawMessage(`{"owner":"o","repo":"r"}`)); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(seen) != 1 {
		t.Fatalf("Mcp-Param-repo seen on %d requests (%v), want exactly the tools/call", len(seen), seen)
	}
}

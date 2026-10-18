package upstream

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestReadStream(t *testing.T) {
	var notes []string
	on := func(m rpcMessage) { notes = append(notes, m.Method) }
	stream := ": keep-alive\n\n" +
		"event: message\ndata: {\"jsonrpc\":\"2.0\",\"method\":\"notifications/progress\",\"params\":{\"progressToken\":1,\"progress\":1}}\n\n" +
		"data: {\"jsonrpc\":\"2.0\",\"id\":9,\"method\":\"sampling/createMessage\",\"params\":{}}\n\n" + // a request on the stream: ignored
		"event: message\r\ndata: {\"jsonrpc\":\"2.0\",\"method\":\"notifications/message\",\r\ndata: \"params\":{}}\r\n\r\n" +
		"event: message\ndata: {\"jsonrpc\":\"2.0\",\"id\":1,\"result\":{\"resultType\":\"complete\"}}\n\n" +
		"event: message\ndata: {\"jsonrpc\":\"2.0\",\"method\":\"notifications/progress\"}\n\n"
	m, err := readStream(strings.NewReader(stream), on)
	if err != nil || m == nil || m.Error != nil || !strings.Contains(string(m.Result), "complete") {
		t.Fatalf("got %+v, %v", m, err)
	}
	if strings.Join(notes, ",") != "notifications/progress,notifications/message" {
		t.Fatalf("notifications = %v", notes)
	}
	// An error response ends it too; a final event without a blank line is
	// still read.
	m, err = readStream(strings.NewReader("data: {\"jsonrpc\":\"2.0\",\"id\":1,\"error\":{\"code\":-32602,\"message\":\"no\"}}"), nil)
	if err != nil || m == nil || m.Error == nil || m.Error.Code != -32602 {
		t.Fatalf("got %+v, %v", m, err)
	}
	// Cut off before a response.
	if _, err := readStream(strings.NewReader("data: {\"jsonrpc\":\"2.0\",\"method\":\"notifications/progress\"}\n\n"), nil); !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("err = %v", err)
	}
	if _, err := readStream(strings.NewReader(""), nil); !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("err = %v", err)
	}
}

func TestMcpNameHeader(t *testing.T) {
	for in, want := range map[string]string{
		"get_weather":        "get_weather",
		"file:///a/b.json":   "file:///a/b.json",
		"héllo":              "=?base64?aMOpbGxv?=",
		" padded":            "=?base64?IHBhZGRlZA==?=",
		"=?base64?literal?=": "=?base64?PT9iYXNlNjQ/bGl0ZXJhbD89?=",
	} {
		if got := mcpNameHeader(in); got != want {
			t.Errorf("%q: %q, want %q", in, got, want)
		}
	}
}

// What the portal sends a modern server, on the wire.
func TestForwardModernRequestShape(t *testing.T) {
	var gotHeader http.Header
	var gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		if strings.Contains(string(b), "server/discover") {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"resultType":"complete","supportedVersions":["2026-07-28"],"capabilities":{}}}`))
			return
		}
		gotHeader, gotBody = r.Header.Clone(), string(b)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"resultType":"complete","content":[{"type":"text","text":"ok"}]}}`))
	}))
	defer srv.Close()
	c := eraClient()
	const tp = "00-0af7651916cd43dd8448eb211c80319c-00f067aa0ba902b7-01"
	_, err := c.Forward(context.Background(), Target{URL: srv.URL, ServerID: "x", Timeout: 5 * time.Second, ParamHeaders: map[string]string{"Mcp-Param-Region": "eu"}}, ForwardRequest{
		Method: MethodToolsCall, Name: "t", Arguments: []byte(`{"a":1}`), Elicitation: []byte(`{"url":{}}`),
		InputResponses: []byte(`{"k":{"action":"accept"}}`), RequestState: "s1", ProgressToken: []byte(`"p"`), Trace: Trace{Traceparent: tp}})
	if err != nil {
		t.Fatal(err)
	}
	if gotHeader.Get("MCP-Protocol-Version") != "2026-07-28" || gotHeader.Get("Mcp-Method") != "tools/call" || gotHeader.Get("Mcp-Name") != "t" ||
		gotHeader.Get("Mcp-Param-Region") != "eu" || !strings.Contains(gotHeader.Get("Accept"), "text/event-stream") || gotHeader.Get("Mcp-Session-Id") != "" {
		t.Fatalf("headers = %v", gotHeader)
	}
	for _, want := range []string{`"name":"t"`, `"arguments":{"a":1}`, `"inputResponses":{"k":{"action":"accept"}}`, `"requestState":"s1"`,
		`"io.modelcontextprotocol/protocolVersion":"2026-07-28"`, `"io.modelcontextprotocol/clientCapabilities":{"elicitation":{"url":{}}}`,
		`"progressToken":"p"`, `"traceparent":"` + tp + `"`, `"io.modelcontextprotocol/clientInfo":{"name":"open-mcp-portal"`} {
		if !strings.Contains(gotBody, want) {
			t.Errorf("the body lacks %s: %s", want, gotBody)
		}
	}
	// Without elicitation, no capability at all.
	if _, err := c.Forward(context.Background(), Target{URL: srv.URL, ServerID: "x", Timeout: 5 * time.Second}, ForwardRequest{Method: MethodToolsCall, Name: "t"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(gotBody, `"io.modelcontextprotocol/clientCapabilities":{}`) || strings.Contains(gotBody, "progressToken") ||
		strings.Contains(gotBody, "traceparent") || strings.Contains(gotBody, "requestState") || strings.Contains(gotBody, "inputResponses") {
		t.Errorf("a plain call's body = %s", gotBody)
	}
}

func TestForwardModernResults(t *testing.T) {
	answer := func(body string) *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			b, _ := io.ReadAll(r.Body)
			w.Header().Set("Content-Type", "application/json")
			if strings.Contains(string(b), "server/discover") {
				_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"supportedVersions":["2026-07-28"]}}`))
				return
			}
			_, _ = w.Write([]byte(body))
		}))
	}
	call := func(body string) (*ForwardResult, error) {
		srv := answer(body)
		defer srv.Close()
		return eraClient().Forward(context.Background(), Target{URL: srv.URL, ServerID: "x", Timeout: 5 * time.Second}, ForwardRequest{Method: MethodToolsCall, Name: "t"})
	}
	res, err := call(`{"jsonrpc":"2.0","id":1,"result":{"resultType":"input_required","inputRequests":{"a":{"method":"elicitation/create","params":{}}},"requestState":"s"}}`)
	if err != nil || res.Input == nil || res.Input.RequestState != "s" || !strings.Contains(string(res.Input.Requests), `"a"`) || res.Tool != nil {
		t.Fatalf("input_required = %+v, %v", res, err)
	}
	// A bare state is an input request with nothing to ask; nothing at all is
	// a bad server.
	if res, err := call(`{"jsonrpc":"2.0","id":1,"result":{"resultType":"input_required","inputRequests":{},"requestState":"s"}}`); err != nil || res.Input == nil || res.Input.Requests != nil {
		t.Fatalf("state only = %+v, %v", res, err)
	}
	for _, body := range []string{
		`{"jsonrpc":"2.0","id":1,"result":{"resultType":"input_required"}}`,
		`{"jsonrpc":"2.0","id":1,"result":{"resultType":"mystery","content":[]}}`,
		`{"jsonrpc":"2.0","id":1,"result":[1]}`,
		`not json`,
	} {
		var e *Error
		if _, err := call(body); !errors.As(err, &e) || e.Class != ClassBadResponse {
			t.Errorf("%.60s: %v", body, err)
		}
	}
	// A JSON-RPC error keeps its code.
	var e *Error
	if _, err := call(`{"jsonrpc":"2.0","id":1,"error":{"code":-32602,"message":"Unknown tool"}}`); !errors.As(err, &e) || e.RPCCode != -32602 || e.Class != ClassBadRequest {
		t.Fatalf("rpc error = %#v", err)
	}
	// A complete result with no resultType (a server of the earlier draft).
	if res, err := call(`{"jsonrpc":"2.0","id":1,"result":{"content":[{"type":"text","text":"x"}],"isError":true}}`); err != nil || res.Tool == nil || !res.Tool.IsError {
		t.Fatalf("complete = %+v, %v", res, err)
	}
}

func TestSameJSONValue(t *testing.T) {
	for _, tc := range []struct {
		got  any
		want string
		ok   bool
	}{
		{"a", `"a"`, true}, {float64(42), `42`, true}, {"42", `42`, false}, {float64(1), `1.0`, true}, {"a", `"b"`, false}, {nil, `null`, true},
	} {
		if got := sameJSONValue(tc.got, []byte(tc.want)); got != tc.ok {
			t.Errorf("%v vs %s: %v", tc.got, tc.want, got)
		}
	}
}

// MRTR: a retry is a different request, so it has a different JSON-RPC id
// (found by the conformance scenario sep-2322-client-request-state).
func TestForwardUsesAFreshRequestID(t *testing.T) {
	var ids []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		var m struct {
			ID     json.Number `json:"id"`
			Method string      `json:"method"`
		}
		_ = json.Unmarshal(b, &m)
		w.Header().Set("Content-Type", "application/json")
		if m.Method == "server/discover" {
			_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":` + m.ID.String() + `,"result":{"supportedVersions":["2026-07-28"]}}`))
			return
		}
		ids = append(ids, m.ID.String())
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":` + m.ID.String() + `,"result":{"content":[]}}`))
	}))
	defer srv.Close()
	c := eraClient()
	for range 3 {
		if _, err := c.Forward(context.Background(), Target{URL: srv.URL, ServerID: "x", Timeout: 5 * time.Second}, ForwardRequest{Method: MethodToolsCall, Name: "t"}); err != nil {
			t.Fatal(err)
		}
	}
	if len(ids) != 3 || ids[0] == ids[1] || ids[1] == ids[2] || ids[0] == ids[2] {
		t.Fatalf("ids = %v", ids)
	}
}

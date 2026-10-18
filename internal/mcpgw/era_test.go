package mcpgw

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

func req(method string, params string) message {
	m := message{JSONRPC: "2.0", ID: json.RawMessage("1"), Method: method}
	if params != "" {
		m.Params = json.RawMessage(params)
	}
	return m
}

func hdr(kv ...string) http.Header {
	h := http.Header{}
	for i := 0; i < len(kv); i += 2 {
		h.Set(kv[i], kv[i+1])
	}
	return h
}

const modernMeta = `"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28","io.modelcontextprotocol/clientCapabilities":{}}`

func TestDetectEra(t *testing.T) {
	b64 := "=?base64?" + base64.StdEncoding.EncodeToString([]byte("tëst")) + "?="
	cases := []struct {
		name   string
		msg    message
		header http.Header
		modern bool
		status int
		code   int64
		part   string
	}{
		// Legacy: nothing modern in the request.
		{name: "initialize", msg: req("initialize", `{"protocolVersion":"2025-11-25"}`)},
		{name: "initialize with modern meta is still legacy", msg: req("initialize", `{`+modernMeta+`}`), header: hdr("MCP-Protocol-Version", "2026-07-28")},
		{name: "bare tools/list", msg: req("tools/list", "")},
		{name: "legacy header", msg: req("tools/list", ""), header: hdr("MCP-Protocol-Version", "2025-06-18")},
		{name: "legacy meta version", msg: req("tools/list", `{"_meta":{"io.modelcontextprotocol/protocolVersion":"2025-11-25"}}`)},
		{name: "clientInfo only", msg: req("tools/list", `{"_meta":{"io.modelcontextprotocol/clientInfo":{"name":"x"}}}`)},
		{name: "unknown old header stays legacy", msg: req("tools/list", ""), header: hdr("MCP-Protocol-Version", "2023-01-01")},
		{name: "garbage header stays legacy", msg: req("tools/list", ""), header: hdr("MCP-Protocol-Version", "latest")},
		{name: "notification", msg: message{JSONRPC: "2.0", Method: "notifications/x"}, header: hdr("MCP-Protocol-Version", "2026-07-28")},
		{name: "non-object params", msg: req("tools/list", `[1]`)},

		// Modern and valid.
		{name: "discover", msg: req("server/discover", `{`+modernMeta+`}`), modern: true,
			header: hdr("MCP-Protocol-Version", "2026-07-28", "Mcp-Method", "server/discover")},
		{name: "tools/list", msg: req("tools/list", `{`+modernMeta+`}`), modern: true,
			header: hdr("MCP-Protocol-Version", "2026-07-28", "Mcp-Method", "tools/list")},
		{name: "tools/call", msg: req("tools/call", `{"name":"a__b",`+modernMeta+`}`), modern: true,
			header: hdr("MCP-Protocol-Version", "2026-07-28", "Mcp-Method", "tools/call", "Mcp-Name", "a__b")},
		{name: "prompts/get", msg: req("prompts/get", `{"name":"a__b",`+modernMeta+`}`), modern: true,
			header: hdr("MCP-Protocol-Version", "2026-07-28", "Mcp-Method", "prompts/get", "Mcp-Name", "a__b")},
		{name: "resources/read", msg: req("resources/read", `{"uri":"omp://a/b://c",`+modernMeta+`}`), modern: true,
			header: hdr("MCP-Protocol-Version", "2026-07-28", "Mcp-Method", "resources/read", "Mcp-Name", "omp://a/b://c")},
		{name: "base64 name", msg: req("tools/call", `{"name":"tëst",`+modernMeta+`}`), modern: true,
			header: hdr("MCP-Protocol-Version", "2026-07-28", "Mcp-Method", "tools/call", "Mcp-Name", b64)},
		{name: "ping is modern (and then not found)", msg: req("ping", `{`+modernMeta+`}`), modern: true,
			header: hdr("MCP-Protocol-Version", "2026-07-28", "Mcp-Method", "ping")},
		{name: "unknown body name is the handler's problem", msg: req("tools/call", `{`+modernMeta+`}`), modern: true,
			header: hdr("MCP-Protocol-Version", "2026-07-28", "Mcp-Method", "tools/call", "Mcp-Name", "x")},

		// Modern and refused.
		{name: "no capabilities", msg: req("tools/list", `{"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28"}}`), modern: true,
			header: hdr("MCP-Protocol-Version", "2026-07-28", "Mcp-Method", "tools/list"), status: 400, code: -32602, part: "clientCapabilities"},
		{name: "capabilities not an object", msg: req("tools/list", `{"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28","io.modelcontextprotocol/clientCapabilities":[]}}`),
			modern: true, header: hdr("MCP-Protocol-Version", "2026-07-28", "Mcp-Method", "tools/list"), status: 400, code: -32602, part: "object"},
		{name: "header only, no meta", msg: req("tools/list", ""), modern: true, header: hdr("MCP-Protocol-Version", "2026-07-28", "Mcp-Method", "tools/list"),
			status: 400, code: -32602, part: "protocolVersion"},
		{name: "version not a string", msg: req("tools/list", `{"_meta":{"io.modelcontextprotocol/protocolVersion":5}}`), modern: true,
			status: 400, code: -32602, part: "protocolVersion"},
		{name: "no version header", msg: req("tools/list", `{`+modernMeta+`}`), modern: true, header: hdr("Mcp-Method", "tools/list"),
			status: 400, code: -32020, part: "MCP-Protocol-Version"},
		{name: "version header mismatch", msg: req("tools/list", `{`+modernMeta+`}`), modern: true,
			header: hdr("MCP-Protocol-Version", "2025-11-25", "Mcp-Method", "tools/list"), status: 400, code: -32020, part: "does not match"},
		{name: "unsupported version", modern: true, status: 400, code: -32022,
			msg:    req("tools/list", `{"_meta":{"io.modelcontextprotocol/protocolVersion":"1900-01-01","io.modelcontextprotocol/clientCapabilities":{}}}`),
			header: hdr("MCP-Protocol-Version", "1900-01-01", "Mcp-Method", "tools/list")},
		{name: "future version", modern: true, status: 400, code: -32022,
			msg:    req("tools/list", `{"_meta":{"io.modelcontextprotocol/protocolVersion":"2027-01-01","io.modelcontextprotocol/clientCapabilities":{}}}`),
			header: hdr("MCP-Protocol-Version", "2027-01-01", "Mcp-Method", "tools/list")},
		{name: "future version in the header alone", msg: req("tools/list", ""), modern: true, header: hdr("MCP-Protocol-Version", "2027-01-01"),
			status: 400, code: -32602},
		{name: "no method header", msg: req("tools/list", `{`+modernMeta+`}`), modern: true, header: hdr("MCP-Protocol-Version", "2026-07-28"),
			status: 400, code: -32020, part: "Mcp-Method"},
		{name: "method mismatch", msg: req("tools/list", `{`+modernMeta+`}`), modern: true,
			header: hdr("MCP-Protocol-Version", "2026-07-28", "Mcp-Method", "prompts/list"), status: 400, code: -32020, part: "Mcp-Method"},
		{name: "no name header", msg: req("tools/call", `{"name":"a",`+modernMeta+`}`), modern: true,
			header: hdr("MCP-Protocol-Version", "2026-07-28", "Mcp-Method", "tools/call"), status: 400, code: -32020, part: "Mcp-Name"},
		{name: "name mismatch", msg: req("tools/call", `{"name":"a",`+modernMeta+`}`), modern: true,
			header: hdr("MCP-Protocol-Version", "2026-07-28", "Mcp-Method", "tools/call", "Mcp-Name", "b"), status: 400, code: -32020, part: "Mcp-Name"},
		{name: "name is the original, not exposed", msg: req("tools/call", `{"name":"srv__a",`+modernMeta+`}`), modern: true,
			header: hdr("MCP-Protocol-Version", "2026-07-28", "Mcp-Method", "tools/call", "Mcp-Name", "a"), status: 400, code: -32020, part: "Mcp-Name"},
		{name: "uri mismatch", msg: req("resources/read", `{"uri":"x://a",`+modernMeta+`}`), modern: true,
			header: hdr("MCP-Protocol-Version", "2026-07-28", "Mcp-Method", "resources/read", "Mcp-Name", "x://b"), status: 400, code: -32020, part: "Mcp-Name"},
		{name: "bad base64 name", msg: req("tools/call", `{"name":"a",`+modernMeta+`}`), modern: true,
			header: hdr("MCP-Protocol-Version", "2026-07-28", "Mcp-Method", "tools/call", "Mcp-Name", "=?base64?***?="), status: 400, code: -32020, part: "valid header"},
		{name: "base64 of another name", msg: req("tools/call", `{"name":"a",`+modernMeta+`}`), modern: true,
			header: hdr("MCP-Protocol-Version", "2026-07-28", "Mcp-Method", "tools/call", "Mcp-Name", "=?base64?"+base64.StdEncoding.EncodeToString([]byte("b"))+"?="),
			status: 400, code: -32020, part: "Mcp-Name"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			st, err := detectEra(c.msg, c.header)
			if st.modern != c.modern {
				t.Fatalf("modern = %v, want %v (err %v)", st.modern, c.modern, err)
			}
			if c.code == 0 {
				if err != nil {
					t.Fatalf("refused: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("not refused; want %d", c.code)
			}
			if err.status != c.status || err.code != c.code || !strings.Contains(err.message, c.part) {
				t.Fatalf("got %d %d %q, want %d %d ...%s...", err.status, err.code, err.message, c.status, c.code, c.part)
			}
		})
	}
}

func TestUnsupportedVersionData(t *testing.T) {
	_, err := detectEra(req("tools/list", `{"_meta":{"io.modelcontextprotocol/protocolVersion":"1900-01-01","io.modelcontextprotocol/clientCapabilities":{}}}`),
		hdr("MCP-Protocol-Version", "1900-01-01", "Mcp-Method", "tools/list"))
	if err == nil {
		t.Fatal("accepted")
	}
	data := err.data.(map[string]any)
	supported := data["supported"].([]string)
	if data["requested"] != "1900-01-01" || supported[0] != "2026-07-28" || supported[len(supported)-1] != "2024-11-05" || len(supported) != 5 {
		t.Fatalf("data = %v", data)
	}
}

func TestDecodeHeaderValue(t *testing.T) {
	enc := func(s string) string { return "=?base64?" + base64.StdEncoding.EncodeToString([]byte(s)) + "?=" }
	good := map[string]string{
		"plain":                   "plain",
		"us-west1":                "us-west1",
		enc("Hello, 世界"):          "Hello, 世界",
		enc(" padded "):           " padded ",
		enc("line1\nline2"):       "line1\nline2",
		enc("=?base64?literal?="): "=?base64?literal?=",
		"=?base64?":               "=?base64?", // not the sentinel: no suffix
		"tab\tinside":             "tab\tinside",
	}
	for in, want := range good {
		if got, ok := decodeHeaderValue(in); !ok || got != want {
			t.Errorf("decodeHeaderValue(%q) = %q %v, want %q", in, got, ok, want)
		}
	}
	for _, in := range []string{"caf\u00e9", "a\x01b", "a\x7fb", "=?base64?!!!?=", "=?base64?/w==?=" /* invalid UTF-8 */} {
		if got, ok := decodeHeaderValue(in); ok {
			t.Errorf("decodeHeaderValue(%q) = %q, want a refusal", in, got)
		}
	}
}

const sqlTool = `{"name":"execute_sql","inputSchema":{"type":"object","properties":{
	"region":{"type":"string","x-mcp-header":"Region"},
	"query":{"type":"string"},
	"limit":{"type":"integer","x-mcp-header":"Limit"},
	"dry":{"type":"boolean","x-mcp-header":"Dry"},
	"opts":{"type":"object","properties":{"tenant":{"type":"string","x-mcp-header":"Tenant"}}},
	"list":{"type":"array","items":{"type":"string","x-mcp-header":"Nope"}},
	"bad":{"type":"string","x-mcp-header":"has space"}}}}`

func TestValidateParamHeaders(t *testing.T) {
	def := json.RawMessage(sqlTool)
	enc := func(s string) string { return "=?base64?" + base64.StdEncoding.EncodeToString([]byte(s)) + "?=" }
	cases := []struct {
		name string
		args string
		h    http.Header
		ok   bool
	}{
		{"all present", `{"region":"us-west1","query":"q","limit":10,"dry":true,"opts":{"tenant":"t1"}}`,
			hdr("Mcp-Param-Region", "us-west1", "Mcp-Param-Limit", "10", "Mcp-Param-Dry", "true", "Mcp-Param-Tenant", "t1"), true},
		{"absent argument, no header", `{"query":"q"}`, hdr(), true},
		{"null argument, no header", `{"region":null}`, hdr(), true},
		{"integer compared numerically", `{"limit":42}`, hdr("Mcp-Param-Limit", "42.0"), true},
		{"base64", `{"region":"Hello, 世界"}`, hdr("Mcp-Param-Region", enc("Hello, 世界")), true},
		{"no arguments at all", ``, hdr(), true},
		{"x-mcp-header under items is ignored", `{"list":["a"]}`, hdr("Mcp-Param-Nope", "a"), true},
		{"invalid header name is ignored", `{"bad":"v"}`, hdr(), true},
		{"missing header", `{"region":"us"}`, hdr(), false},
		{"mismatch", `{"region":"us"}`, hdr("Mcp-Param-Region", "eu"), false},
		{"mismatch in a nested property", `{"opts":{"tenant":"a"}}`, hdr("Mcp-Param-Tenant", "b"), false},
		{"header for an absent argument", `{}`, hdr("Mcp-Param-Region", "us"), false},
		{"header for a null argument", `{"region":null}`, hdr("Mcp-Param-Region", "us"), false},
		{"integer mismatch", `{"limit":1}`, hdr("Mcp-Param-Limit", "2"), false},
		{"integer header not a number", `{"limit":1}`, hdr("Mcp-Param-Limit", "one"), false},
		{"fractional header for an integer", `{"limit":1}`, hdr("Mcp-Param-Limit", "1.5"), false},
		{"boolean spelled wrong", `{"dry":true}`, hdr("Mcp-Param-Dry", "True"), false},
		{"non-primitive argument", `{"region":{"a":1}}`, hdr("Mcp-Param-Region", "x"), false},
		{"invalid characters", `{"region":"us"}`, hdr("Mcp-Param-Region", "u\x01s"), false},
		{"invalid base64", `{"region":"us"}`, hdr("Mcp-Param-Region", "=?base64?%%?="), false},
		{"repeated header", `{"region":"us"}`, http.Header{"Mcp-Param-Region": {"us", "us"}}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := validateParamHeaders(c.h, def, json.RawMessage(c.args))
			if (err == nil) != c.ok {
				t.Fatalf("err = %v, want ok=%v", err, c.ok)
			}
			if err != nil && (err.code != codeHeaderMismatch || err.status != 400) {
				t.Fatalf("err = %+v", err)
			}
		})
	}
	// A tool without annotations never needs headers.
	if err := validateParamHeaders(hdr(), json.RawMessage(`{"name":"x","inputSchema":{"type":"object","properties":{"a":{"type":"string"}}}}`), json.RawMessage(`{"a":"b"}`)); err != nil {
		t.Fatal(err)
	}
}

func TestParamBindingsOrder(t *testing.T) {
	var got []string
	for _, b := range paramBindings(json.RawMessage(sqlTool)) {
		got = append(got, b.header+"="+strings.Join(b.path, "."))
	}
	want := "Mcp-Param-Dry=dry Mcp-Param-Limit=limit Mcp-Param-Tenant=opts.tenant Mcp-Param-Region=region"
	if strings.Join(got, " ") != want {
		t.Fatalf("bindings = %v", got)
	}
}

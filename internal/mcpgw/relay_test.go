package mcpgw

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ncecere/open-mcp-portal/internal/upstream"
)

func rawJSON(s string) json.RawMessage { return json.RawMessage(s) }

func elicitForm(msg string) string {
	return fmt.Sprintf(`{"method":"elicitation/create","params":{"mode":"form","message":%q,"requestedSchema":{"type":"object","properties":{"a":{"type":"string"}}}}}`, msg)
}

func TestRelayedRequestsValidation(t *testing.T) {
	both := elicitModes{form: true, url: true}
	formOnly := elicitModes{form: true, empty: true}
	many := func(n int) string {
		parts := []string{}
		for i := range n {
			parts = append(parts, fmt.Sprintf(`"k%d":%s`, i, elicitForm("m")))
		}
		return "{" + strings.Join(parts, ",") + "}"
	}
	for _, tc := range []struct {
		name  string
		in    string
		modes elicitModes
		// want: "" for relayed, else a part of the refusal; tool says it is a
		// tool-error refusal (sampling/roots), not a bad server.
		want string
		tool bool
	}{
		{"one form", `{"a":` + elicitForm("m") + `}`, both, "", false},
		{"eight is the limit", many(8), both, "", false},
		{"nine is too many", many(9), both, "more than 8", false},
		{"unique keys", `{"a":` + elicitForm("m") + `,"a":` + elicitForm("n") + `}`, both, "repeat a key", false},
		{"empty key", `{"":` + elicitForm("m") + `}`, both, "invalid key", false},
		{"long key", `{"` + strings.Repeat("k", 200) + `":` + elicitForm("m") + `}`, both, "invalid key", false},
		{"control key", `{"a\nb":` + elicitForm("m") + `}`, both, "invalid key", false},
		{"too big", `{"a":` + elicitForm(strings.Repeat("x", 3000)) + `,"b":` + elicitForm(strings.Repeat("x", 3000)) + `,"c":` + elicitForm(strings.Repeat("x", 3000)) +
			`,"d":` + elicitForm(strings.Repeat("x", 3000)) + `,"e":` + elicitForm(strings.Repeat("x", 3000)) + `,"f":` + elicitForm(strings.Repeat("x", 3000)) + `}`, both, "more input than", false},
		{"message too long", `{"a":` + elicitForm(strings.Repeat("x", 5000)) + `}`, both, "no usable message", false},
		{"no message", `{"a":{"method":"elicitation/create","params":{"mode":"form","requestedSchema":{"type":"object"}}}}`, both, "no usable message", false},
		{"sampling", `{"a":{"method":"sampling/createMessage","params":{"messages":[],"maxTokens":1}}}`, both, "sampling/roots", true},
		{"roots", `{"a":{"method":"roots/list","params":{}}}`, both, "sampling/roots", true},
		{"sampling beside a form", `{"a":` + elicitForm("m") + `,"b":{"method":"sampling/createMessage","params":{}}}`, both, "sampling/roots", true},
		{"unknown method", `{"a":{"method":"ping"}}`, both, "doesn't know how", false},
		{"unknown mode", `{"a":{"method":"elicitation/create","params":{"mode":"carrier-pigeon","message":"m"}}}`, both, "unknown mode", false},
		{"form without declared form", `{"a":{"method":"elicitation/create","params":{"mode":"form","message":"m","requestedSchema":{"type":"object"}}}}`, elicitModes{url: true}, "didn't declare", false},
		{"url without declared url", `{"a":{"method":"elicitation/create","params":{"mode":"url","message":"m","url":"https://example.com/x"}}}`, formOnly, "didn't declare", false},
		{"mode omitted is form", `{"a":{"method":"elicitation/create","params":{"message":"m","requestedSchema":{"type":"object","properties":{}}}}}`, formOnly, "", false},
		{"form without schema", `{"a":{"method":"elicitation/create","params":{"mode":"form","message":"m"}}}`, both, "requestedSchema", false},
		{"schema of another type", `{"a":{"method":"elicitation/create","params":{"mode":"form","message":"m","requestedSchema":{"type":"array"}}}}`, both, "requestedSchema", false},
		{"property that is not an object", `{"a":{"method":"elicitation/create","params":{"mode":"form","message":"m","requestedSchema":{"type":"object","properties":{"x":5}}}}}`, both, "invalid property", false},
		{"url ok", `{"a":{"method":"elicitation/create","params":{"mode":"url","message":"m","url":"https://example.com/x"}}}`, both, "", false},
		{"url http", `{"a":{"method":"elicitation/create","params":{"mode":"url","message":"m","url":"http://example.com/x"}}}`, both, "isn't https", false},
		{"not an object", `[1]`, both, "not an object", false},
		{"not JSON", `{`, both, "not valid JSON", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out, keys, err := relayedRequests(rawJSON(tc.in), tc.modes, false)
			if tc.want == "" {
				if err != nil || len(out) == 0 || len(keys) != len(out) {
					t.Fatalf("refused: %v", err)
				}
				return
			}
			ref, ok := err.(*relayRefusal)
			if !ok || !strings.Contains(ref.message, tc.want) || ref.toolError != tc.tool {
				t.Fatalf("err = %#v, want %q (tool %v)", err, tc.want, tc.tool)
			}
		})
	}

	// Rebuilt: only what the client needs, whatever else the server put in.
	out, keys, err := relayedRequests(rawJSON(`{"b":{"method":"elicitation/create","_meta":{"x":1},"params":{"mode":"url","message":"m",
		"url":"https://example.com/x","elicitationId":"abc","_meta":{"y":2},"extra":true}},"a":`+elicitForm("n")+`}`), both, false)
	if err != nil || strings.Join(keys, ",") != "a,b" {
		t.Fatalf("%v %v", keys, err)
	}
	b, _ := json.Marshal(out["b"])
	if string(b) != `{"method":"elicitation/create","params":{"message":"m","mode":"url","url":"https://example.com/x"}}` {
		t.Fatalf("rebuilt = %s", b)
	}
	// Nothing to relay is not an error (a bare requestState).
	if out, keys, err := relayedRequests(nil, both, false); out != nil || keys != nil || err != nil {
		t.Fatalf("nothing: %v %v %v", out, keys, err)
	}
}

func TestSafeElicitationURL(t *testing.T) {
	for url, want := range map[string]bool{
		"https://example.com/connect?request=abc": true,
		"HTTPS://Example.com/x":                   true,
		"http://example.com/x":                    false,
		"http://127.0.0.1:8088/x":                 false,
		"https://user@example.com/":               false,
		"https://example.com/ x":                  false,
		"javascript:alert(1)":                     false,
		"data:text/html,x":                        false,
		"mailto:a@b.c":                            false,
		"//example.com":                           false,
		"":                                        false,
		"https:example.com":                       false,
	} {
		if got := safeElicitationURL(url, false); got != want {
			t.Errorf("%q: %v, want %v", url, got, want)
		}
	}
	// Development: plain http for loopback hosts only.
	for url, want := range map[string]bool{
		"http://127.0.0.1:8088/x":   true,
		"http://localhost:8088/x":   true,
		"http://[::1]:8088/x":       true,
		"http://example.com/x":      false,
		"http://10.0.0.5/x":         false,
		"http://127.0.0.1.evil.io/": false,
	} {
		if got := safeElicitationURL(url, true); got != want {
			t.Errorf("dev %q: %v, want %v", url, got, want)
		}
	}
}

func TestClientElicitation(t *testing.T) {
	for _, tc := range []struct {
		caps string
		want elicitModes
		decl string
	}{
		{`{}`, elicitModes{}, ""},
		{`{"elicitation":{}}`, elicitModes{form: true, empty: true}, `{}`},
		{`{"elicitation":{"form":{}}}`, elicitModes{form: true}, `{"form":{}}`},
		{`{"elicitation":{"url":{}}}`, elicitModes{url: true}, `{"url":{}}`},
		{`{"elicitation":{"form":{"a":1},"url":{}}}`, elicitModes{form: true, url: true}, `{"form":{},"url":{}}`},
		{`{"elicitation":null}`, elicitModes{}, ""},
		{`{"elicitation":"yes"}`, elicitModes{}, ""},
	} {
		var caps map[string]json.RawMessage
		_ = json.Unmarshal([]byte(tc.caps), &caps)
		rc := &request{modern: true, clientCaps: caps}
		got := rc.clientElicitation()
		if got != tc.want || string(got.declared()) != tc.decl {
			t.Errorf("%s: %+v declared %q, want %+v %q", tc.caps, got, got.declared(), tc.want, tc.decl)
		}
	}
	// A legacy request declares nothing, whatever its params say.
	if m := (&request{modern: false, clientCaps: map[string]json.RawMessage{"elicitation": rawJSON(`{}`)}}).clientElicitation(); m.any() {
		t.Error("a legacy request declared elicitation")
	}
}

func TestRelayResponses(t *testing.T) {
	keys := []string{"a", "b"}
	params := func(s string) json.RawMessage { return rawJSON(`{"name":"x","inputResponses":` + s + `}`) }
	got, err := relayResponses(params(`{"a":{"action":"accept","content":{"x":1}},"c":{"action":"accept"},"b":{"action":"decline"}}`), keys)
	if err != nil || string(got) != `{"a":{"action":"accept","content":{"x":1}},"b":{"action":"decline"}}` {
		t.Fatalf("got %s, %v", got, err)
	}
	for _, in := range []string{`[]`, `{"a":{"action":"maybe"}}`, `{"a":"accept"}`, `{"a":{"action":"accept","content":[1]}}`, `{"a":{}}`} {
		if _, err := relayResponses(params(in), keys); err == nil {
			t.Errorf("%s was accepted", in)
		}
	}
	for _, in := range []string{`null`, `{}`, `{"zzz":{"action":"accept"}}`} {
		if got, err := relayResponses(params(in), keys); got != nil || err != nil {
			t.Errorf("%s = %s, %v", in, got, err)
		}
	}
	if got, err := relayResponses(rawJSON(`{"name":"x"}`), keys); got != nil || err != nil {
		t.Errorf("none = %s, %v", got, err)
	}
}

func TestStreamBounds(t *testing.T) {
	rec := httptest.NewRecorder()
	rc := &request{w: rec, r: httptest.NewRequest("POST", "/mcp/x", nil), h: &Handler{}}
	st := rc.startStream(rawJSON(`"tok"`))
	defer st.close()
	if rec.Code != 200 || rec.Header().Get("Content-Type") != "text/event-stream" || rec.Header().Get("X-Accel-Buffering") != "no" {
		t.Fatalf("headers = %d %v", rec.Code, rec.Header())
	}
	total := 10.0
	st.progress(upstream.Progress{Progress: 1, Total: &total, Message: "one"})
	st.progress(upstream.Progress{Progress: 1}) // not increasing: dropped
	st.progress(upstream.Progress{Progress: 0.5})
	st.progress(upstream.Progress{Progress: 2, Message: strings.Repeat("é", 2000)})
	for i := 3; i < 200; i++ {
		st.progress(upstream.Progress{Progress: float64(i)})
	}
	body := rec.Body.String()
	if n := strings.Count(body, "notifications/progress"); n != maxProgressEvents {
		t.Fatalf("%d progress events", n)
	}
	if !strings.Contains(body, `"progressToken":"tok"`) || !strings.Contains(body, `"total":10`) {
		t.Fatalf("body = %.300s", body)
	}
	// The message is cut to its bound on a character boundary.
	for _, line := range strings.Split(body, "\n") {
		if strings.HasPrefix(line, "data: ") && strings.Contains(line, "éé") {
			var ev struct{ Params struct{ Message string } }
			if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &ev); err != nil || len(ev.Params.Message) > maxProgressMessage {
				t.Fatalf("message of %d bytes (%v)", len(ev.Params.Message), err)
			}
		}
	}
	// The response is the last event; nothing follows it.
	n := writeRPC(rc.w, http.StatusOK, rawJSON("7"), map[string]any{"ok": true}, nil)
	st.progress(upstream.Progress{Progress: 1000})
	st.comment()
	tail := rec.Body.String()[len(body):]
	if !strings.HasPrefix(tail, "event: message\ndata: {") || strings.Count(tail, "\n\n") != 1 || n <= 0 || st.bytes() != int64(rec.Body.Len()) {
		t.Fatalf("tail = %q (n %d, bytes %d, body %d)", tail, n, st.bytes(), rec.Body.Len())
	}

	// A progress event that would pass the byte bound is dropped.
	rec2 := httptest.NewRecorder()
	rc2 := &request{w: rec2, r: httptest.NewRequest("POST", "/mcp/x", nil), h: &Handler{}}
	st2 := rc2.startStream(rawJSON(`1`))
	defer st2.close()
	st2.mu.Lock()
	st2.total = maxStreamBytes - 10
	st2.mu.Unlock()
	st2.progress(upstream.Progress{Progress: 1})
	if strings.Contains(rec2.Body.String(), "progress") {
		t.Fatal("progress past the byte bound was sent")
	}
}

func TestStreamKeepAliveComments(t *testing.T) {
	rec := httptest.NewRecorder()
	h := &Handler{}
	h.SetStreamKeepAlive(10 * time.Millisecond)
	rc := &request{w: rec, r: httptest.NewRequest("POST", "/mcp/x", nil), h: h}
	st := rc.startStream(rawJSON(`"t"`))
	time.Sleep(80 * time.Millisecond)
	st.close()
	if n := strings.Count(rec.Body.String(), ": keep-alive\n\n"); n < 3 {
		t.Fatalf("%d keep-alive comments", n)
	}
	before := rec.Body.Len()
	time.Sleep(40 * time.Millisecond)
	if rec.Body.Len() != before {
		t.Fatal("a comment was written after close")
	}
	if (&Handler{}).streamKeepAlive() != 15*time.Second {
		t.Fatal("the default keep-alive isn't 15 s")
	}
}

func TestProgressToken(t *testing.T) {
	for in, want := range map[string]bool{
		`{"_meta":{"progressToken":"abc"}}`:                              true,
		`{"_meta":{"progressToken":12}}`:                                 true,
		`{"_meta":{"progressToken":-3}}`:                                 true,
		`{"_meta":{"progressToken":""}}`:                                 false,
		`{"_meta":{"progressToken":1.5}}`:                                false,
		`{"_meta":{"progressToken":true}}`:                               false,
		`{"_meta":{"progressToken":null}}`:                               false,
		`{"_meta":{"progressToken":{"a":1}}}`:                            false,
		`{"_meta":{"progressToken":"` + strings.Repeat("x", 300) + `"}}`: false,
		`{"_meta":{}}`: false,
		`{}`:           false,
	} {
		rc := &request{msg: message{Params: rawJSON(in)}}
		if _, ok := rc.progressToken(); ok != want {
			t.Errorf("%.60s: %v, want %v", in, ok, want)
		}
	}
	for accept, want := range map[string]bool{
		"application/json, text/event-stream":    true,
		"text/event-stream":                      true,
		"TEXT/EVENT-STREAM;q=0.9, application/*": true,
		"application/json":                       false,
		"*/*":                                    false,
		"":                                       false,
	} {
		if got := acceptsStream([]string{accept}); got != want {
			t.Errorf("Accept %q: %v, want %v", accept, got, want)
		}
	}
}

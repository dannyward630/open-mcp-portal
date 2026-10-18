package httpapi_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/ncecere/open-mcp-portal/internal/httpapi"
	"github.com/ncecere/open-mcp-portal/internal/httpapi/apitypes"
	"github.com/ncecere/open-mcp-portal/internal/testutil"
)

// "Connect your account" as a multi round-trip request (docs/mcp-protocol.md
// "Consent"): modern clients that declare URL elicitation get an
// input_required result; everyone else keeps -32042.

var urlCaps = map[string]any{"elicitation": map[string]any{"url": map[string]any{}}}

// mcall posts a modern request to the gateway "support" and returns the
// answer (always HTTP 200 here), keeping it for the secret sentinel.
func (e *provEnv) mcall(key, method string, params, caps map[string]any) map[string]any {
	e.t.Helper()
	code, _, out := rawModern(e.t, e.app, "support", key, method, params, caps, nil)
	if code != 200 {
		e.t.Fatalf("%s = %d %v", method, code, out)
	}
	b, _ := json.Marshal(out)
	e.responses = append(e.responses, string(b))
	return out
}

func toolCall(name string, extra map[string]any) map[string]any {
	p := map[string]any{"name": name, "arguments": map[string]any{}}
	for k, v := range extra {
		p[k] = v
	}
	return p
}

// retryWith is the extra params of a retry: the state and the person's
// answer to the one input request.
func retryWith(state, action string) map[string]any {
	m := map[string]any{"requestState": state}
	if action != "" {
		m["inputResponses"] = map[string]any{"connect_github": map[string]any{"action": action}}
	}
	return m
}

// lastCall waits for the latest call record to read want ("outcome class
// provider": calls are recorded after the answer is written).
func (e *provEnv) lastCall(want string) {
	e.t.Helper()
	last := ""
	for range 100 {
		last = e.scalar(`SELECT coalesce((SELECT outcome || ' ' || coalesce(error_class, '') || ' ' || coalesce(provider_slug, '')
			FROM mcp_calls ORDER BY id DESC LIMIT 1), '')`)
		if last == want {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	e.t.Fatalf("recorded %q, want %q", last, want)
}

// inputRequired checks an input_required answer for the github provider and
// returns the link and the requestState.
func (e *provEnv) inputRequired(out map[string]any, messagePart string) (link, state string) {
	e.t.Helper()
	if out["error"] != nil {
		e.t.Fatalf("an error, not an input request: %v", out)
	}
	r := resultOf(e.t, out)
	if r["resultType"] != "input_required" || !slices.Equal(keysOf(r), []string{"_meta", "inputRequests", "requestState", "resultType"}) {
		e.t.Fatalf("result = %v", r)
	}
	if serverInfoOf(e.t, r)["name"] != "support" {
		e.t.Fatalf("serverInfo = %v", r["_meta"])
	}
	reqs := r["inputRequests"].(map[string]any)
	if !slices.Equal(keysOf(reqs), []string{"connect_github"}) {
		e.t.Fatalf("inputRequests = %v", reqs)
	}
	ir := reqs["connect_github"].(map[string]any)
	params := ir["params"].(map[string]any)
	link, _ = params["url"].(string)
	msg, _ := params["message"].(string)
	if ir["method"] != "elicitation/create" || params["mode"] != "url" || !slices.Equal(keysOf(params), []string{"message", "mode", "url"}) ||
		!strings.HasPrefix(link, e.app.URL+"/connect/github?request=") || !strings.Contains(msg, messagePart) {
		e.t.Fatalf("input request = %v (want %q)", ir, messagePart)
	}
	state, _ = r["requestState"].(string)
	if state == "" || strings.Contains(state, requestID(link)) {
		e.t.Fatalf("requestState = %q", state)
	}
	return link, state
}

// connectQuiet follows a consent link as blair without failing the test
// from another goroutine (an SDK client's handler runs in its own).
func (e *provEnv) connectQuiet(link string) error {
	var st apitypes.ConnectStarted
	code, ec := e.member.call("POST", "/v1/me/connections/start", map[string]any{"providerId": e.provider.Id, "requestId": requestID(link)}, &st, nil)
	if code != 200 {
		return fmt.Errorf("start = %d %s", code, ec)
	}
	hc := noRedirects(e.member)
	res, err := hc.Get(st.AuthorizeUrl)
	if err != nil {
		return err
	}
	res.Body.Close()
	res, err = hc.Get(res.Header.Get("Location"))
	if err != nil {
		return err
	}
	res.Body.Close()
	if res.StatusCode != http.StatusSeeOther || !strings.Contains(res.Header.Get("Location"), "connected") {
		return fmt.Errorf("callback = %d %s", res.StatusCode, res.Header.Get("Location"))
	}
	return nil
}

func TestConsentAsInputRequired(t *testing.T) {
	e := newProvEnv(t)
	e.mustSetCaps(e.owner, toolsTeam+"/gateways/"+e.gateway.Id.String(), sel(
		e.capID(e.owner, e.server, "tool", "provider_token"), e.capID(e.owner, e.server, "tool", "whoami"),
		e.capID(e.owner, e.server, "tool", "echo"), e.capID(e.owner, e.server, "resource", "status://services"),
		e.capID(e.owner, e.server, "prompt", "incident_summary")))
	const tool = "status__provider_token"
	msg := "Connect your Github account (repo) to use " + tool + " in the SUPPORT gateway."

	// A modern client with URL elicitation: an input request with the link,
	// nothing forwarded, the call recorded as before.
	out := e.mcall(e.key, "tools/call", toolCall(tool, nil), urlCaps)
	link, state := e.inputRequired(out, msg)
	if got, _ := json.Marshal(resultOf(t, out)["inputRequests"]); string(got) != fmt.Sprintf(
		`{"connect_github":{"method":"elicitation/create","params":{"message":%q,"mode":"url","url":%q}}}`, msg, link) {
		t.Fatalf("inputRequests = %s", got)
	}
	e.lastCall("needs_authorization not_connected github")
	if n := e.toolCalls("provider_token"); n != 0 {
		t.Fatalf("server contacted %d times without consent", n)
	}
	// It is the link the legacy error carries too (the same request).
	if legacyLink := e.consentURL(e.callTool(e.key, tool), "not_connected"); legacyLink != link {
		t.Fatalf("legacy link %s, modern link %s", legacyLink, link)
	}
	// Not a form, no elicitationId (removed in 2026-07-28), no cache hints.
	if strings.Contains(fmt.Sprint(out), "elicitationId") || resultOf(t, out)["ttlMs"] != nil {
		t.Fatalf("answer = %v", out)
	}

	// A modern client without URL elicitation (none, form only) gets no -32042
	// (2026-07-28 forbids it): a tool error with the same link in its text.
	for name, caps := range map[string]map[string]any{
		"no capabilities": {}, "elicitation without modes": {"elicitation": map[string]any{}},
		"form only": {"elicitation": map[string]any{"form": map[string]any{}}}, "sampling only": {"sampling": map[string]any{}},
	} {
		got := e.mcall(e.key, "tools/call", toolCall(tool, nil), caps)
		if l := plainConsentLink(t, got, e.app.URL, e.provider.Slug); l != link {
			t.Errorf("%s: link %s", name, l)
		}
		e.lastCall("needs_authorization not_connected github")
	}
	// Legacy clients keep -32042.
	e.consentURL(e.callTool(e.key, tool), "not_connected")

	// The person hasn't connected yet: the retry asks again, with the same
	// link and a new state.
	again := e.mcall(e.key, "tools/call", toolCall(tool, retryWith(state, "accept")), urlCaps)
	link2, state2 := e.inputRequired(again, msg)
	if link2 != link || state2 == state {
		t.Fatalf("second ask: link %s (was %s), state reused %v", link2, link, state2 == state)
	}
	// A retry without an answer does too (the client may retry at once).
	if l, _ := e.inputRequired(e.mcall(e.key, "tools/call", toolCall(tool, retryWith(state2, "")), urlCaps), msg); l != link {
		t.Fatalf("retry without an answer: %s", l)
	}

	// Declined or cancelled: a clean tool error; nothing forwarded.
	for _, action := range []string{"decline", "cancel"} {
		got := e.mcall(e.key, "tools/call", toolCall(tool, retryWith(state, action)), urlCaps)
		r := resultOf(t, got)
		content, _ := r["content"].([]any)
		if r["isError"] != true || r["resultType"] != "complete" || len(content) != 1 || content[0].(map[string]any)["text"] != "authorization declined" {
			t.Fatalf("%s = %v", action, got)
		}
		e.lastCall("tool_error authorization_declined github")
	}
	// An answer without a state of ours means nothing: it is asked again.
	e.inputRequired(e.mcall(e.key, "tools/call", toolCall(tool, map[string]any{
		"inputResponses": map[string]any{"connect_github": map[string]any{"action": "decline"}}}), urlCaps), msg)
	// inputResponses must be an object.
	if code, _, bad := rawModern(t, e.app, "support", e.key, "tools/call", toolCall(tool, map[string]any{"inputResponses": []any{1}}), urlCaps, nil); code != 200 || rpcErrorCode(bad) != -32602 {
		t.Errorf("inputResponses as an array = %d %v", code, bad)
	}
	if n := e.toolCalls("provider_token"); n != 0 {
		t.Fatalf("server contacted %d times without consent", n)
	}

	// The same for a read and a prompt (the other two requests that may
	// return input_required); a decline there is a refusal with a reason.
	uri := "omp://status/status://services"
	_, rstate := e.inputRequired(e.mcall(e.key, "resources/read", map[string]any{"uri": uri}, urlCaps), "to use")
	e.lastCall("needs_authorization not_connected github")
	code, _, got := rawModern(t, e.app, "support", e.key, "resources/read", map[string]any{"uri": uri, "requestState": rstate,
		"inputResponses": map[string]any{"connect_github": map[string]any{"action": "decline"}}}, urlCaps, nil)
	if _, m, data := rpcErr(got); code != 200 || rpcErrorCode(got) != -32010 || m != "authorization declined" || data["reason"] != "authorization_declined" {
		t.Fatalf("declined read = %d %v", code, got)
	}
	e.lastCall("denied authorization_declined github")
	pp := map[string]any{"name": "status__incident_summary", "arguments": map[string]any{"service": "vpn"}}
	_, pstate := e.inputRequired(e.mcall(e.key, "prompts/get", pp, urlCaps), "status__incident_summary")
	pp["requestState"], pp["inputResponses"] = pstate, map[string]any{"connect_github": map[string]any{"action": "cancel"}}
	if _, m, data := rpcErr(e.mcall(e.key, "prompts/get", pp, urlCaps)); m != "authorization declined" || data["reason"] != "authorization_declined" {
		t.Fatalf("declined prompt = %v", m)
	}
	// Lists and the other tools don't ask for anything.
	if r := resultOf(t, e.mcall(e.key, "tools/list", nil, urlCaps)); r["resultType"] != "complete" {
		t.Fatalf("tools/list = %v", r)
	}

	// Connect, then retry: the same request goes through, complete.
	if to := e.connect(e.member, map[string]any{"providerId": e.provider.Id, "requestId": requestID(link)}); !strings.Contains(to, "result=connected") {
		t.Fatalf("connect = %s", to)
	}
	done := e.mcall(e.key, "tools/call", toolCall(tool, retryWith(state2, "accept")), urlCaps)
	r := resultOf(t, done)
	if r["resultType"] != "complete" || r["isError"] == true || serverInfoOf(t, r)["name"] != "support" || r["inputRequests"] != nil || r["requestState"] != nil {
		t.Fatalf("retry = %v", done)
	}
	rep := e.providerReport(done)
	issued := e.oauth.Issued()
	if !rep.Received || rep.Provider != "github" || rep.TokenSHA256 != testutil.TokenDigest(issued[len(issued)-1]) || !rep.Verified {
		t.Fatalf("report = %+v", rep)
	}
	e.lastCall("ok  github")
	// A retry without a state works the same now, and so does a stale one
	// (state only ever re-runs the checks).
	for _, extra := range []map[string]any{nil, retryWith(state, "accept")} {
		if r := resultOf(t, e.mcall(e.key, "tools/call", toolCall(tool, extra), urlCaps)); r["resultType"] != "complete" || r["isError"] == true {
			t.Fatalf("after connecting = %v", r)
		}
	}
	// A decline is honoured whatever the connection says now: the person
	// said no.
	if r := resultOf(t, e.mcall(e.key, "tools/call", toolCall(tool, retryWith(state, "decline")), urlCaps)); r["isError"] != true {
		t.Fatalf("decline = %v", r)
	}

	// The sentinel: a person asked for more access while holding tokens.
	// The state never holds a credential, and neither does the answer.
	code2, ec := e.override("whoami", map[string]any{"mode": "scopes", "scopes": []string{"repo", "read:org"}})
	mustCode(t, "override", code2, ec, 200, "")
	got = e.mcall(e.key, "tools/call", toolCall("status__whoami", nil), urlCaps)
	_, wstate := e.inputRequired(got, "Grant more access to your Github account (repo, read:org) to use status__whoami")
	e.lastCall("needs_authorization missing_scopes github")
	secrets := append(append(e.oauth.Issued(), e.oauth.IssuedRefresh()...), e.oauth.ClientSecret, e.key)
	if len(secrets) < 4 {
		t.Fatalf("only %d secrets to look for", len(secrets))
	}
	raw, _ := base64.RawURLEncoding.DecodeString(wstate)
	for _, sec := range secrets {
		b, _ := json.Marshal(got)
		if strings.Contains(string(b), sec) || strings.Contains(wstate, sec) || strings.Contains(string(raw), sec) {
			t.Fatalf("a credential (%d chars) is in the answer or the requestState", len(sec))
		}
		for _, resp := range e.responses {
			if strings.Contains(resp, sec) {
				t.Fatalf("a credential (%d chars) is in an answer", len(sec))
			}
		}
	}
	if strings.Contains(e.logs.String(), wstate) {
		t.Error("a requestState was logged")
	}
}

// What a requestState binds, and what a client can't do with it.
func TestRequestStateBinding(t *testing.T) {
	var mu sync.Mutex
	var offset time.Duration
	e := newProvEnvHook(t, func(d *httpapi.Deps) {
		d.MCP.(interface{ SetClock(func() time.Time) }).SetClock(func() time.Time {
			mu.Lock()
			defer mu.Unlock()
			return time.Now().Add(offset)
		})
	})
	path := toolsTeam + "/gateways/" + e.gateway.Id.String()
	whoami, token := e.capID(e.owner, e.server, "tool", "whoami"), e.capID(e.owner, e.server, "tool", "provider_token")
	resource := e.capID(e.owner, e.server, "resource", "status://services")
	e.mustSetCaps(e.owner, path, sel(token, whoami, resource))
	second := e.createGateway(e.owner, toolsTeam+"/gateways", "second")
	e.mustSetCaps(e.owner, toolsTeam+"/gateways/"+second.Id.String(), sel(token, whoami))
	const tool = "status__provider_token"
	alexKey := newKey(t, e.editor, "alex").Secret

	_, state := e.inputRequired(e.mcall(e.key, "tools/call", toolCall(tool, nil), urlCaps), tool)
	rejected := func(what, class string, code int, out map[string]any, part string) {
		t.Helper()
		if code != 200 || rpcErrorCode(out) != -32602 {
			t.Fatalf("%s = %d %v", what, code, out)
		}
		if _, m, _ := rpcErr(out); !strings.Contains(m, part) {
			t.Fatalf("%s: message %q, want it to say %q", what, m, part)
		}
		e.lastCall("bad_request " + class + " ")
	}
	try := func(key, gateway, method string, params map[string]any) (int, map[string]any) {
		c, _, o := rawModern(t, e.app, gateway, key, method, params, urlCaps, nil)
		return c, o
	}

	// Altered, cut short, made up: not valid, and nothing is said about why.
	flip := func(s string, i int) string {
		b := []byte(s)
		if b[i] == 'A' {
			b[i] = 'B'
		} else {
			b[i] = 'A'
		}
		return string(b)
	}
	for what, bad := range map[string]string{
		"a changed first byte": flip(state, 0), "a changed middle byte": flip(state, len(state)/2), "a changed final byte": flip(state, len(state)-2),
		"truncated": state[:len(state)-4], "extended": state + "AAAA", "garbage": "not-a-state", "not base64": "!!!!", "empty-ish": "AA",
		"another encoding": strings.NewReplacer("-", "+", "_", "/").Replace(state) + "==",
	} {
		code, out := try(e.key, "support", "tools/call", toolCall(tool, retryWith(bad, "accept")))
		rejected(what, "request_state_invalid", code, out, "requestState is not valid")
	}
	code, out := try(e.key, "support", "tools/call", toolCall(tool, map[string]any{"requestState": 12}))
	rejected("a number", "request_state_invalid", code, out, "requestState is not valid")
	code, out = try(e.key, "support", "tools/call", toolCall(tool, map[string]any{"requestState": strings.Repeat("A", 5000)}))
	rejected("an oversized state", "request_state_invalid", code, out, "requestState is not valid")
	// The same refusals for reads and prompts.
	code, out = try(e.key, "support", "resources/read", map[string]any{"uri": "omp://status/status://services", "requestState": "junk"})
	rejected("a read with junk", "request_state_invalid", code, out, "requestState is not valid")
	// Refused before anything else: no connection, no server contact.
	if n := e.toolCalls("provider_token"); n != 0 {
		t.Fatalf("server contacted %d times", n)
	}

	// Another person's state (and one of theirs for you).
	code, out = try(alexKey, "support", "tools/call", toolCall(tool, retryWith(state, "accept")))
	rejected("another person", "request_state_invalid", code, out, "requestState is not valid")
	_, astate := e.inputRequired(mustOK(t, alexKey, e, "tools/call", toolCall(tool, nil)), tool)
	code, out = try(e.key, "support", "tools/call", toolCall(tool, retryWith(astate, "accept")))
	rejected("another person's state", "request_state_invalid", code, out, "requestState is not valid")

	// Another request: another tool, other arguments, another method, another
	// gateway. The retry must be the request it was issued for.
	mismatch := "issued for a different request"
	code, out = try(e.key, "support", "tools/call", toolCall("status__whoami", retryWith(state, "accept")))
	rejected("another tool", "request_state_mismatch", code, out, mismatch)
	other := toolCall(tool, retryWith(state, "accept"))
	other["arguments"] = map[string]any{"x": 1}
	code, out = try(e.key, "support", "tools/call", other)
	rejected("other arguments", "request_state_mismatch", code, out, mismatch)
	code, out = try(e.key, "support", "resources/read", map[string]any{"uri": "omp://status/status://services", "requestState": state})
	rejected("another method", "request_state_mismatch", code, out, mismatch)
	code, out = try(e.key, "second", "tools/call", toolCall(tool, retryWith(state, "accept")))
	rejected("another gateway", "request_state_mismatch", code, out, mismatch)
	// A retry is allowed to differ in what the digest leaves out: _meta
	// (progress tokens, client info), the JSON-RPC id, key order, spacing.
	c, _, o := rawMCP(t, e.app, "POST", "support", e.key, rpc(77, "tools/call", map[string]any{
		"requestState": state, "arguments": map[string]any{}, "name": tool,
		"_meta": map[string]any{"io.modelcontextprotocol/protocolVersion": modernVersion, "progressToken": "p-1",
			"io.modelcontextprotocol/clientInfo":         map[string]any{"name": "another-client", "version": "9"},
			"io.modelcontextprotocol/clientCapabilities": urlCaps}}),
		modernHeaders("tools/call", map[string]any{"name": tool}, nil))
	if c != 200 || o["error"] != nil || resultOf(t, o)["resultType"] != "input_required" {
		t.Fatalf("retry with other _meta = %d %v", c, o)
	}

	// Time: valid for ten minutes.
	mu.Lock()
	offset = 9*time.Minute + 30*time.Second
	mu.Unlock()
	e.inputRequired(mustOK(t, e.key, e, "tools/call", toolCall(tool, retryWith(state, "accept"))), tool)
	mu.Lock()
	offset = 10*time.Minute + 5*time.Second
	mu.Unlock()
	code, out = try(e.key, "support", "tools/call", toolCall(tool, retryWith(state, "accept")))
	rejected("an expired state", "request_state_expired", code, out, "has expired")
	// An expired state is no loss: the request without it asks afresh.
	e.inputRequired(mustOK(t, e.key, e, "tools/call", toolCall(tool, nil)), tool)
	mu.Lock()
	offset = 0
	mu.Unlock()

	// A modern client that never declared URL elicitation never gets a
	// state, and the one it sends is checked all the same.
	code, out = try(e.key, "support", "tools/call", toolCall(tool, retryWith("junk", "")))
	rejected("junk without URL elicitation", "request_state_invalid", code, out, "requestState is not valid")
	// Legacy clients don't have the field: it is ignored.
	_, _, lo := rawMCP(t, e.app, "POST", "support", e.key, rpc(1, "tools/call", toolCall(tool, retryWith("junk", "decline"))), nil)
	e.consentURL(lo, "not_connected")
}

func mustOK(t *testing.T, key string, e *provEnv, method string, params map[string]any) map[string]any {
	t.Helper()
	return e.mcall(key, method, params, urlCaps)
}

// The official SDK's client, which fulfills the input request by itself:
// it shows the link (here: opens it as the person), retries, and the call
// goes through; or the person declines.
func TestConsentWithSDKClient(t *testing.T) {
	e := newProvEnv(t)
	ctx := context.Background()
	var mu sync.Mutex
	var asked []mcp.ElicitParams
	action := "decline"
	var connectErr error
	opts := &mcp.ClientOptions{
		Capabilities: &mcp.ClientCapabilities{Elicitation: &mcp.ElicitationCapabilities{URL: &mcp.URLElicitationCapabilities{}}},
		ElicitationHandler: func(_ context.Context, req *mcp.ElicitRequest) (*mcp.ElicitResult, error) {
			mu.Lock()
			defer mu.Unlock()
			asked = append(asked, *req.Params)
			if action == "accept" {
				connectErr = e.connectQuiet(req.Params.URL)
			}
			return &mcp.ElicitResult{Action: action}, nil
		},
	}
	cs := connectEra(t, e.app, "support", e.key, "", opts)
	if cs.InitializeResult().ProtocolVersion != modernVersion {
		t.Fatalf("session = %+v", cs.InitializeResult())
	}

	res, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "status__provider_token"})
	if err != nil || !res.IsError || resultText(t, res) != "authorization declined" {
		t.Fatalf("declined = %+v %v", res, err)
	}
	mu.Lock()
	if len(asked) != 1 || asked[0].Mode != "url" || !strings.HasPrefix(asked[0].URL, e.app.URL+"/connect/github?request=") ||
		!strings.Contains(asked[0].Message, "Github account (repo)") {
		t.Fatalf("asked = %+v", asked)
	}
	action = "accept"
	mu.Unlock()
	if n := e.toolCalls("provider_token"); n != 0 {
		t.Fatalf("server contacted %d times", n)
	}

	res, err = cs.CallTool(ctx, &mcp.CallToolParams{Name: "status__provider_token"})
	mu.Lock()
	cerr, n := connectErr, len(asked)
	mu.Unlock()
	if cerr != nil {
		t.Fatalf("the person's side of the link: %v", cerr)
	}
	if err != nil || res.IsError {
		t.Fatalf("after consent = %+v %v", res, err)
	}
	var rep testutil.ProviderReport
	if err := json.Unmarshal([]byte(resultText(t, res)), &rep); err != nil || !rep.Received || rep.Provider != "github" || !rep.Verified {
		t.Fatalf("report = %+v %v", rep, err)
	}
	if n != 2 {
		t.Fatalf("asked %d times", n)
	}
	// Connected: no more questions.
	res, err = cs.CallTool(ctx, &mcp.CallToolParams{Name: "status__provider_token"})
	mu.Lock()
	n = len(asked)
	mu.Unlock()
	if err != nil || res.IsError || n != 2 {
		t.Fatalf("third call = %+v %v (asked %d)", res, err, n)
	}

	// A client without URL elicitation (the SDK's default) gets a tool error
	// it can show, with the link in its text (not -32042).
	plain := connectEra(t, e.app, "support", e.key, "", nil)
	e.scalar(`DELETE FROM provider_connections WHERE user_id = $1 RETURNING 'x'`, e.userID("blair@localhost"))
	res, err = plain.CallTool(ctx, &mcp.CallToolParams{Name: "status__provider_token"})
	if err != nil || res == nil || !res.IsError || len(res.Content) != 1 || !strings.Contains(fmt.Sprint(res.Content[0].(*mcp.TextContent).Text), "/connect/github?request=") {
		t.Fatalf("plain modern client = %+v %v", res, err)
	}
}

// Per-person OAuth to a protected MCP server (phase 7): the same flow.
func TestUpstreamOAuthConsentAsInputRequired(t *testing.T) {
	e := newUpEnv(t, true)
	code, raw := e.createOAuth(e.editor, "gh", e.pm.URL(), map[string]any{"registration": "auto", "scopes": []string{"repo"}}, nil)
	if code != 201 {
		t.Fatalf("create = %d %s", code, raw)
	}
	var created struct{ Data apitypes.McpServerCreated }
	_ = json.Unmarshal(raw, &created)
	sv := created.Data.Server
	id := sv.Id.String()
	slug, providerID := sv.Oauth.ProviderSlug, sv.Oauth.ProviderId.String()
	e.setup(e.editor, id)
	sv = e.get(e.editor, id)
	gw := e.createGateway(e.owner, toolsTeam+"/gateways", "ghgw")
	e.mustSetCaps(e.owner, toolsTeam+"/gateways/"+gw.Id.String(), sel(e.capID(e.owner, sv, "tool", "whoami")))
	key := newKey(t, e.member, "laptop").Secret

	pmBefore := e.pm.Requests()
	call := func(extra map[string]any, caps map[string]any) map[string]any {
		t.Helper()
		c, _, out := rawModern(t, e.app, "ghgw", key, "tools/call", toolCall("gh__whoami", extra), caps, nil)
		if c != 200 {
			t.Fatalf("call = %d %v", c, out)
		}
		return out
	}
	out := call(nil, urlCaps)
	r := resultOf(t, out)
	reqs, _ := r["inputRequests"].(map[string]any)
	ir, _ := reqs["connect_"+slug].(map[string]any)
	params, _ := ir["params"].(map[string]any)
	link, _ := params["url"].(string)
	state, _ := r["requestState"].(string)
	if r["resultType"] != "input_required" || len(reqs) != 1 || params["mode"] != "url" || state == "" ||
		!strings.HasPrefix(link, e.app.URL+"/connect/"+slug+"?request=") ||
		!strings.HasPrefix(params["message"].(string), "Sign in to GH") || !strings.HasSuffix(params["message"].(string), "to use gh__whoami in the GHGW gateway.") {
		t.Fatalf("answer = %v", out)
	}
	e.recorded("needs_authorization not_connected " + slug)
	if e.pm.Requests() != pmBefore {
		t.Fatal("the server was contacted without a connection")
	}
	// Without URL elicitation: a tool error (no -32042 for a modern client) with the same link.
	if l := plainConsentLink(t, call(nil, nil), e.app.URL, slug); l != link {
		t.Fatalf("link %s vs %s", l, link)
	}
	e.recorded("needs_authorization not_connected " + slug)
	if to := e.connectVia(e.member, providerID, link); !strings.Contains(to, "result=connected") {
		t.Fatalf("connect = %s", to)
	}
	done := call(retryWith(state, "accept"), urlCaps)
	if r := resultOf(t, done); r["resultType"] != "complete" || r["isError"] == true {
		t.Fatalf("retry = %v", done)
	}
	if who := whoamiOf(t, done); !who.Verified || who.BearerSHA256 == "" {
		t.Fatalf("whoami = %+v", who)
	}
}

// plainConsentLink checks the answer a modern client that did not declare URL
// elicitation gets for a tool that needs a connection: a tool error (isError),
// not a JSON-RPC error, and not -32042, with the /connect link in its text.
func plainConsentLink(t *testing.T, out map[string]any, appURL, slug string) string {
	t.Helper()
	if out["error"] != nil {
		t.Fatalf("a JSON-RPC error, want a tool error: %v", out)
	}
	r := resultOf(t, out)
	content, _ := r["content"].([]any)
	if r["isError"] != true || len(content) != 1 || r["resultType"] != "complete" {
		t.Fatalf("answer = %v", out)
	}
	text, _ := content[0].(map[string]any)["text"].(string)
	prefix := appURL + "/connect/" + slug + "?request="
	i := strings.Index(text, prefix)
	if i < 0 {
		t.Fatalf("no link in %q", text)
	}
	link := text[i:]
	if j := strings.IndexAny(link, " ,"); j >= 0 {
		link = link[:j]
	}
	return link
}

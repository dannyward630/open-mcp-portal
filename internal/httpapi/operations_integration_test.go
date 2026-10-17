package httpapi_test

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ncecere/open-mcp-portal/internal/apikeys"
	"github.com/ncecere/open-mcp-portal/internal/app"
	"github.com/ncecere/open-mcp-portal/internal/config"
	"github.com/ncecere/open-mcp-portal/internal/httpapi"
	"github.com/ncecere/open-mcp-portal/internal/httpapi/apitypes"
	"github.com/ncecere/open-mcp-portal/internal/observability"
	"github.com/ncecere/open-mcp-portal/internal/secrets"
	"github.com/ncecere/open-mcp-portal/internal/testutil"
)

// Phase 6 (docs/phase6-operations.md): rate limits shared by replicas,
// payload capture, pepper rotation, call details, usage and metrics.

const otherPepper = "YWJjZGVmZ2hpamtsbW5vcHFyc3R1dnd4eXphYmNkZWY="

// replica serves the same database with its own services (another pod).
func replica(t *testing.T, pool *pgxpool.Pool, mutate func(*config.Config)) *testApp {
	t.Helper()
	var handler http.Handler
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { handler.ServeHTTP(w, r) }))
	t.Cleanup(srv.Close)
	cfg := config.Defaults()
	cfg.AppURL, cfg.DevAuth, cfg.DatabaseURL = srv.URL, true, "unused"
	cfg.EncryptionKey, cfg.APIKeyPepper = testEncryptionKey, testEncryptionKey
	cfg.RequestsPerMinute, cfg.LoginAttemptsPerMinute = 100_000, 100_000
	if mutate != nil {
		mutate(&cfg)
	}
	if err := cfg.Validate("serve"); err != nil {
		t.Fatal(err)
	}
	deps, err := app.NewDeps(cfg, pool, testutil.Logger())
	if err != nil {
		t.Fatal(err)
	}
	deps.Health = httpapi.NewHealth(nil)
	deps.Web, deps.WebBuilt = testWeb, true
	handler = httpapi.NewAPIHandler(deps)
	jar, _ := cookiejar.New(nil)
	return &testApp{t: t, URL: srv.URL, Pool: pool, Client: &http.Client{Jar: jar}}
}

// freshWindow waits for a new rate limit window when the current one ends
// within a few seconds, and forgets every counter.
func freshWindow(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	var left float64
	if err := pool.QueryRow(context.Background(),
		`SELECT extract(epoch FROM date_trunc('minute', now()) + interval '1 minute' - now())::float8`).Scan(&left); err != nil {
		t.Fatal(err)
	}
	if left < 10 {
		time.Sleep(time.Duration(left*float64(time.Second)) + 200*time.Millisecond)
	}
	if _, err := pool.Exec(context.Background(), `DELETE FROM rate_limit_counters`); err != nil {
		t.Fatal(err)
	}
}

// waitCalls waits until n calls are recorded (a call is recorded right
// after its answer is written).
func waitCalls(t *testing.T, pool *pgxpool.Pool, n int) {
	t.Helper()
	for i := 0; i < 100; i++ {
		var got int
		if err := pool.QueryRow(context.Background(), `SELECT count(*) FROM mcp_calls`).Scan(&got); err != nil {
			t.Fatal(err)
		}
		if got >= n {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("fewer than %d calls recorded", n)
}

func (e *gwEnv) putLimits(items ...map[string]any) (int, string) {
	e.t.Helper()
	var cur apitypes.LimitSettings
	e.admin.get("/v1/admin/settings/limits", &cur)
	return e.admin.call("PUT", "/v1/admin/settings/limits", map[string]any{"items": items}, nil, ifMatch(cur.Revision))
}

func (e *gwEnv) putTeamLimits(team string, items ...map[string]any) (int, string) {
	e.t.Helper()
	var cur apitypes.TeamLimits
	e.admin.get("/v1/admin/teams/"+team+"/limits", &cur)
	return e.admin.call("PUT", "/v1/admin/teams/"+team+"/limits", map[string]any{"items": items}, nil, ifMatch(cur.Revision))
}

func limit(key string, def, ceiling any) map[string]any {
	return map[string]any{"key": key, "default": def, "ceiling": ceiling}
}

func errDetails(t *testing.T, s *session, method, path string, body any, headers map[string]string) (int, map[string]any) {
	t.Helper()
	code, raw := s.raw(method, path, body, headers)
	var env struct {
		Error struct {
			Code    string         `json:"code"`
			Details map[string]any `json:"details"`
		} `json:"error"`
	}
	_ = json.Unmarshal(raw, &env)
	d := env.Error.Details
	if d == nil {
		d = map[string]any{}
	}
	d["code"] = env.Error.Code
	return code, d
}

func TestRateLimits(t *testing.T) {
	e := newRuntimeEnv(t)
	pool := e.app.Pool
	other := replica(t, pool, nil)
	// Contents are captured on this gateway: nothing refused is.
	g := e.gateway(e.owner, e.supportTeamPath)
	code, ec := e.owner.call("PATCH", e.supportTeamPath, map[string]any{"capturePayloads": true}, nil, ifMatch(g.Revision))
	mustCode(t, "capture on", code, ec, 200, "")

	// Per credential: two replicas count together.
	code, ec = e.putLimits(limit("credential_per_minute", 4, nil), limit("person_per_minute", 1000, nil))
	mustCode(t, "limits", code, ec, 200, "")
	freshWindow(t, pool)
	for i, a := range []*testApp{e.app, other, e.app, other} {
		code, h, _ := rawMCP(t, a, "POST", "support", e.key.Secret, rpc(i, "tools/list", nil), nil)
		if code != 200 || h.Get("RateLimit-Limit") != "4" || h.Get("RateLimit-Remaining") != strconv.Itoa(3-i) || h.Get("RateLimit-Reset") == "" {
			t.Fatalf("request %d = %d %v", i+1, code, h)
		}
	}
	calls := len(e.fake.ToolCalls())
	code, h, out := rawMCP(t, other, "POST", "support", e.key.Secret, rpc(9, "tools/call", map[string]any{"name": "status__whoami"}), nil)
	retry, _ := strconv.Atoi(h.Get("Retry-After"))
	errObj, _ := out["error"].(map[string]any)
	data, _ := errObj["data"].(map[string]any)
	if code != 429 || retry < 1 || retry > 60 || rpcErrorCode(out) != -32029 || data["reason"] != "rate_limited" ||
		data["limit"] != "credential_per_minute" || data["limitPerMinute"] != float64(4) || data["retryAfterSeconds"] != float64(retry) ||
		!strings.Contains(errObj["message"].(string), "Rate limit exceeded (credential)") || out["id"] != float64(9) {
		t.Fatalf("over the limit = %d %v %v", code, h, out)
	}
	if len(e.fake.ToolCalls()) != calls {
		t.Fatal("a refused call reached the server")
	}
	waitCalls(t, pool, 5)
	if got := e.scalar(`SELECT outcome || ' ' || error_class || ' ' || exposed_name FROM mcp_calls ORDER BY id DESC LIMIT 1`); got != "rate_limited credential_per_minute status__whoami" {
		t.Fatalf("recorded %q", got)
	}
	if e.scalar(`SELECT count(*)::text FROM mcp_call_payloads`) != "0" {
		t.Fatal("a refused call was captured")
	}
	// A notification over the limit is refused too (not recorded).
	code, _, _ = rawMCP(t, e.app, "POST", "support", e.key.Secret, map[string]any{"jsonrpc": "2.0", "method": "notifications/initialized"}, nil)
	if code != 429 {
		t.Fatalf("notification = %d", code)
	}

	// Per person: every credential of the person.
	code, ec = e.putLimits(limit("credential_per_minute", nil, nil), limit("person_per_minute", 2, nil))
	mustCode(t, "limits", code, ec, 200, "")
	freshWindow(t, pool)
	second := newKey(t, e.member, "second")
	rawMCP(t, e.app, "POST", "support", e.key.Secret, rpc(1, "ping", nil), nil)
	rawMCP(t, other, "POST", "support", second.Secret, rpc(2, "ping", nil), nil)
	code, _, out = rawMCP(t, e.app, "POST", "support", second.Secret, rpc(3, "ping", nil), nil)
	if data := rpcData(out); code != 429 || data["limit"] != "person_per_minute" {
		t.Fatalf("person = %d %v", code, out)
	}

	// Per gateway: the team's override, the gateway's own value, the ceiling.
	code, ec = e.putLimits(limit("person_per_minute", nil, nil))
	mustCode(t, "limits", code, ec, 200, "")
	code, ec = e.putTeamLimits("tools", map[string]any{"key": "gateway_per_minute", "value": 3})
	mustCode(t, "team override", code, ec, 200, "")
	if g := e.gateway(e.owner, e.supportTeamPath); g.EffectiveRateLimitPerMinute == nil || *g.EffectiveRateLimitPerMinute != 3 ||
		g.RateLimitPerMinute != nil || g.CapturePayloads == nil || !*g.CapturePayloads {
		t.Fatalf("gateway = %+v", g)
	}
	g = e.gateway(e.owner, e.supportTeamPath)
	code, ec = e.owner.call("PATCH", e.supportTeamPath, map[string]any{"rateLimitPerMinute": 2}, nil, ifMatch(g.Revision))
	mustCode(t, "gateway limit", code, ec, 200, "")
	if g := e.gateway(e.owner, e.supportTeamPath); *g.EffectiveRateLimitPerMinute != 2 || *g.RateLimitPerMinute != 2 {
		t.Fatalf("own value = %+v", g)
	}
	// Platform admins don't set a team gateway's limit or capture.
	g = e.gateway(e.owner, e.supportTeamPath)
	code, ec = e.admin.call("PATCH", e.support, map[string]any{"rateLimitPerMinute": 5}, nil, ifMatch(g.Revision))
	mustCode(t, "admin on a team gateway", code, ec, 403, "team_gateway")
	code, ec = e.admin.call("PATCH", e.support, map[string]any{"capturePayloads": false}, nil, ifMatch(g.Revision))
	mustCode(t, "admin capture on a team gateway", code, ec, 403, "team_gateway")
	// A ceiling below both: capped at runtime and counted.
	code, ec = e.putLimits(limit("gateway_per_minute", 1, 1))
	mustCode(t, "ceiling", code, ec, 200, "")
	var ls apitypes.LimitSettings
	e.auditor.get("/v1/admin/settings/limits", &ls)
	for _, it := range ls.Items {
		if it.Key == "gateway_per_minute" && (it.CappedGateways != 1 || it.CappedTeams != 1 || *it.Ceiling != 1 || it.Max != 1_000_000) {
			t.Fatalf("capped counts = %+v", it)
		}
		if it.Key == "service_account_per_minute" && (it.BuiltInDefault == nil || *it.BuiltInDefault != 1200) {
			t.Fatalf("built-in = %+v", it)
		}
	}
	if g := e.gateway(e.owner, e.supportTeamPath); *g.EffectiveRateLimitPerMinute != 1 {
		t.Fatalf("capped = %+v", g)
	}
	var tl apitypes.TeamLimits
	e.member.get(toolsTeam+"/limits", &tl)
	for _, it := range tl.Items {
		if it.Key == "gateway_per_minute" && (*it.Override != 3 || *it.Effective != 1) {
			t.Fatalf("team view = %+v", it)
		}
	}
	freshWindow(t, pool)
	if code, _, _ := rawMCP(t, e.app, "POST", "support", e.key.Secret, rpc(1, "ping", nil), nil); code != 200 {
		t.Fatalf("first = %d", code)
	}
	// (The replica that saved the ceiling applies it at once; others within
	// 30 seconds.)
	code, _, out = rawMCP(t, e.app, "POST", "support", second.Secret, rpc(2, "ping", nil), nil)
	if data := rpcData(out); code != 429 || data["limit"] != "gateway_per_minute" {
		t.Fatalf("gateway = %d %v", code, out)
	}
	// The shared platform gateway has no limit of its own: unaffected.
	if code, _, _ := rawMCP(t, e.app, "POST", "shared", e.key.Secret, rpc(1, "ping", nil), nil); code != 200 {
		t.Fatalf("other gateway = %d", code)
	}

	// Bounds and refusals.
	g = e.gateway(e.owner, e.supportTeamPath)
	code, d := errDetails(t, e.owner, "PATCH", e.supportTeamPath, map[string]any{"rateLimitPerMinute": 5}, ifMatch(g.Revision))
	if code != 422 || d["code"] != "invalid_limit" || d["reason"] != "above_ceiling" {
		t.Fatalf("gateway above ceiling = %d %v", code, d)
	}
	var tlim apitypes.TeamLimits
	e.admin.get("/v1/admin/teams/tools/limits", &tlim)
	for name, tc := range map[string]struct {
		path   string
		body   map[string]any
		rev    int64
		reason string
	}{
		"team above ceiling": {"/v1/admin/teams/tools/limits", map[string]any{"items": []any{map[string]any{"key": "gateway_per_minute", "value": 5}}}, tlim.Revision, "above_ceiling"},
		"team person":        {"/v1/admin/teams/tools/limits", map[string]any{"items": []any{map[string]any{"key": "person_per_minute", "value": 5}}}, tlim.Revision, "not_overridable"},
		"team duplicate": {"/v1/admin/teams/tools/limits", map[string]any{"items": []any{map[string]any{"key": "gateway_per_minute", "value": 1},
			map[string]any{"key": "gateway_per_minute", "value": 1}}}, tlim.Revision, "duplicate"},
		"ceiling on person":     {"/v1/admin/settings/limits", map[string]any{"items": []any{limit("person_per_minute", 5, 10)}}, 0, "not_overridable"},
		"default above ceiling": {"/v1/admin/settings/limits", map[string]any{"items": []any{limit("gateway_per_minute", 50, 10)}}, 0, "default_above_ceiling"},
	} {
		rev := tc.rev
		if rev == 0 {
			e.admin.get("/v1/admin/settings/limits", &ls)
			rev = ls.Revision
		}
		code, d := errDetails(t, e.admin, "PUT", tc.path, tc.body, ifMatch(rev))
		if code != 422 || d["code"] != "invalid_limit" || d["reason"] != tc.reason {
			t.Errorf("%s = %d %v", name, code, d)
		}
	}

	// Per service account (the team override, all its keys).
	code, ec = e.putLimits(limit("gateway_per_minute", nil, nil))
	mustCode(t, "limits", code, ec, 200, "")
	code, ec = e.putTeamLimits("tools", map[string]any{"key": "service_account_per_minute", "value": 1},
		map[string]any{"key": "gateway_per_minute", "value": nil})
	mustCode(t, "team override", code, ec, 200, "")
	sa := createSA(t, e.owner, map[string]any{"name": "bot"})
	k1 := createSAKey(t, e.owner, sa.Id.String(), map[string]any{"name": "a"})
	k2 := createSAKey(t, e.owner, sa.Id.String(), map[string]any{"name": "b"})
	g = e.gateway(e.owner, e.supportTeamPath)
	code, ec = e.owner.call("PATCH", e.supportTeamPath, map[string]any{"rateLimitPerMinute": nil}, nil, ifMatch(g.Revision))
	mustCode(t, "inherit", code, ec, 200, "")
	freshWindow(t, pool)
	if code, h, _ := rawMCP(t, e.app, "POST", "support", k1.Secret, rpc(1, "ping", nil), nil); code != 200 || h.Get("RateLimit-Limit") != "1" {
		t.Fatalf("service account first = %d %v", code, h)
	}
	code, _, out = rawMCP(t, other, "POST", "support", k2.Secret, rpc(2, "tools/list", nil), nil)
	if data := rpcData(out); code != 429 || data["limit"] != "service_account_per_minute" {
		t.Fatalf("service account = %d %v", code, out)
	}

	// The OAuth endpoints' per-address limit is shared too, and keyed by a
	// digest of the address.
	freshWindow(t, pool)
	for i := 0; i < 60; i++ {
		a := e.app
		if i%2 == 1 {
			a = other
		}
		res, err := http.Post(a.URL+"/oauth/token", "application/x-www-form-urlencoded", strings.NewReader("grant_type=nope"))
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		if res.StatusCode == 429 {
			t.Fatalf("request %d limited", i+1)
		}
	}
	res, err := http.Post(other.URL+"/oauth/token", "application/x-www-form-urlencoded", strings.NewReader("grant_type=nope"))
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != 429 || res.Header.Get("Retry-After") == "" {
		t.Fatalf("61st token request = %d", res.StatusCode)
	}
	if n := e.scalar(`SELECT count(*)::text FROM rate_limit_counters WHERE bucket LIKE '%127.0.0.1%'`); n != "0" {
		t.Fatal("a client address is stored in a bucket")
	}
}

// capEnv is the runtime env with a static header on the server and the
// fake's echo_secrets and image tools selected on the team gateway.
type capEnv struct {
	*rtEnv
	logs *syncBuffer
	seen string // the last call ID lastCall returned
}

const staticSecret = "static-header-secret-0123456789"

func newCapEnv(t *testing.T) *capEnv {
	logs := &syncBuffer{}
	log := slog.New(slog.NewJSONHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	e := &capEnv{rtEnv: newRuntimeEnvHook(t, log, nil), logs: logs}
	e.fake.EnableEchoSecrets()
	e.patchServer(e.owner, e.server, map[string]any{"authHeaderName": "X-Api-Key", "authValue": staticSecret})
	e.refresh(e.owner, e.server)
	ids := []string{e.capID(e.owner, e.server, "tool", "whoami")}
	for _, name := range []string{"echo_secrets", "echo", "image"} {
		ids = append(ids, e.capID(e.owner, e.server, "tool", name))
	}
	ids = append(ids, e.capID(e.owner, e.server, "prompt", "incident_summary"), e.capID(e.owner, e.server, "resource_template", "status://services/{service}"))
	e.mustSetCaps(e.owner, e.supportTeamPath, sel(ids...))
	return e
}

func (e *capEnv) setGatewayCapture(s *session, path string, on bool) {
	e.t.Helper()
	g := e.gateway(s, path)
	code, ec := s.call("PATCH", path, map[string]any{"capturePayloads": on}, nil, ifMatch(g.Revision))
	mustCode(e.t, "gateway capture", code, ec, 200, "")
}

func (e *capEnv) setServerCapture(s *session, on bool) (int, string) {
	e.t.Helper()
	var sv apitypes.McpServer
	s.get(toolsTeam+"/mcp-servers/"+e.server.Id.String(), &sv)
	return s.call("PATCH", toolsTeam+"/mcp-servers/"+e.server.Id.String(), map[string]any{"capturePayloads": on}, nil, ifMatch(sv.Revision))
}

// lastCall is the latest call record (a call is recorded right after its
// answer is written, so wait for it).
func (e *capEnv) lastCall() string {
	e.t.Helper()
	for i := 0; i < 100; i++ {
		if id := e.scalar(`SELECT coalesce(max(id), 0)::text FROM mcp_calls`); id != e.seen {
			e.seen = id
			return id
		}
		time.Sleep(20 * time.Millisecond)
	}
	e.t.Fatal("the call was not recorded")
	return ""
}

func (e *capEnv) payload(s *session, path string) (int, apitypes.CallPayload, http.Header) {
	e.t.Helper()
	req, _ := http.NewRequest("GET", e.app.URL+path, nil)
	res, err := s.client.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	defer res.Body.Close()
	var env struct{ Data apitypes.CallPayload }
	_ = json.NewDecoder(res.Body).Decode(&env)
	return res.StatusCode, env.Data, res.Header
}

func TestPayloadCapture(t *testing.T) {
	e := newCapEnv(t)
	call := func(gateway, method string, params any) map[string]any {
		t.Helper()
		code, _, out := rawMCP(t, e.app, "POST", gateway, e.key.Secret, rpc(1, method, params), nil)
		if code != 200 {
			t.Fatalf("%s = %d %v", method, code, out)
		}
		return out
	}
	// Editors can't switch a server's capture (they may resend the value).
	code, ec := e.setServerCapture(e.editor, true)
	mustCode(t, "editor capture", code, ec, 403, "capture_role")
	code, ec = e.setServerCapture(e.editor, false)
	mustCode(t, "editor resends", code, ec, 200, "")
	var cs apitypes.CaptureSettings
	e.admin.get("/v1/admin/settings/capture", &cs)
	code, d := errDetails(t, e.admin, "PUT", "/v1/admin/settings/capture", map[string]any{"patterns": []any{map[string]any{"pattern": "a*"}}}, ifMatch(cs.Revision))
	if code != 422 || d["code"] != "invalid_pattern" || d["reason"] != "matches_empty" || d["index"] != float64(0) {
		t.Fatalf("empty-matching pattern = %d %v", code, d)
	}
	code, ec = e.admin.call("PUT", "/v1/admin/settings/capture", map[string]any{"patterns": []any{map[string]any{"pattern": "EMP-[0-9]{6}", "note": "IDs"}}},
		nil, ifMatch(cs.Revision))
	mustCode(t, "patterns", code, ec, 200, "")

	// Nothing is captured until a switch is on.
	call("support", "tools/call", map[string]any{"name": "status__echo", "arguments": map[string]any{"text": "hi"}})
	e.lastCall()
	e.setGatewayCapture(e.owner, e.supportTeamPath, true)
	call("support", "tools/list", nil)
	call("support", "tools/call", map[string]any{"name": "status__nope"})
	e.lastCall()
	if n := e.scalar(`SELECT count(*)::text FROM mcp_call_payloads`); n != "0" {
		t.Fatalf("lists and refused calls are not captured (%s)", n)
	}

	sentinels := []string{"hunter2", "EMP-123456", staticSecret, e.key.Secret}
	out := call("support", "tools/call", map[string]any{"name": "status__echo_secrets",
		"arguments": map[string]any{"password": "hunter2", "note": "employee EMP-123456", "pasted": "my key is " + e.key.Secret}})
	echoed := resultTextOf(out)
	jwt := between(echoed, "identity=", " ")
	if len(jwt) < 50 || !strings.Contains(echoed, staticSecret) {
		t.Fatalf("the fake didn't echo the credentials: %s", echoed)
	}
	sentinels = append(sentinels, jwt)
	secretsCall := e.lastCall()
	if raw := e.scalar(`SELECT encode(arguments_cipher, 'escape') || encode(result_cipher, 'escape') FROM mcp_call_payloads WHERE call_id = $1::bigint`, secretsCall); strings.Contains(raw, "EMP-") || strings.Contains(raw, "status__") {
		t.Fatal("payloads are not encrypted at rest")
	}
	big := strings.Repeat("ü", 12_000) // 24 000 bytes
	call("support", "tools/call", map[string]any{"name": "status__echo", "arguments": map[string]any{"text": big}})
	bigCall := e.lastCall()
	call("support", "tools/call", map[string]any{"name": "status__image"})
	imageCall := e.lastCall()
	call("support", "prompts/get", map[string]any{"name": "status__incident_summary", "arguments": map[string]any{"service": "vpn"}})
	e.lastCall()
	call("support", "resources/read", map[string]any{"uri": "omp://status/status://services/email"})
	readCall := e.lastCall()
	if n := e.scalar(`SELECT count(*)::text || ' ' || bool_and(by_gateway AND NOT by_server)::text FROM mcp_call_payloads`); n != "5 true" {
		t.Fatalf("captured = %s", n)
	}

	// The team's owners and admins open them through the team route.
	base := toolsTeam + "/calls/"
	code, p, h := e.payload(e.owner, base+secretsCall+"/payload")
	if code != 200 || h.Get("Cache-Control") != "no-store" || len(p.CapturedBy) != 1 || p.CapturedBy[0] != "gateway" || p.Result == nil ||
		p.Redactions < 5 || !p.ExpiresAt.After(time.Now().Add(13*24*time.Hour)) {
		t.Fatalf("payload = %d %+v", code, p)
	}
	all := p.Arguments.Content + p.Result.Content
	for _, s := range sentinels {
		if strings.Contains(all, s) {
			t.Errorf("%.20s… reached the stored payload: %s", s, all)
		}
	}
	if !strings.Contains(p.Arguments.Content, `"password":"[REDACTED]"`) || !strings.Contains(p.Result.Content, "identity=[REDACTED]") {
		t.Errorf("payload = %s / %s", p.Arguments.Content, p.Result.Content)
	}
	_, p, _ = e.payload(e.owner, base+bigCall+"/payload")
	if !p.Truncated || !p.Arguments.Truncated || len(p.Arguments.Content) > 16384 || !utf8.ValidString(p.Arguments.Content) ||
		p.Arguments.Bytes < 24000 {
		t.Errorf("truncation = %+v", p.Arguments)
	}
	_, p, _ = e.payload(e.owner, base+imageCall+"/payload")
	if !strings.Contains(p.Result.Content, `"data":"[binary 32 bytes]"`) {
		t.Errorf("image = %s", p.Result.Content)
	}
	_, p, _ = e.payload(e.owner, base+readCall+"/payload")
	if p.Arguments.Content != `{"uri":"omp://status/status://services/email"}` {
		t.Errorf("read arguments = %s", p.Arguments.Content)
	}
	// Call detail and list say captured for those who may open it.
	var c apitypes.McpCall
	if code := e.owner.get(base+secretsCall, &c); code != 200 || c.Captured == nil || !*c.Captured {
		t.Fatalf("call detail = %d %+v", code, c)
	}
	var page apitypes.McpCallPage
	e.admin.get(toolsTeam+"/calls?limit=1", &page)
	if *page.Items[0].Captured {
		t.Error("platform staff can't open it through the team route")
	}
	e.admin.get("/v1/admin/calls?limit=1", &page)
	if !*page.Items[0].Captured {
		t.Error("platform staff open it through the admin route")
	}
	// Members, platform staff through the team route, other teams: refused.
	if code, _, _ := e.payload(e.member, base+secretsCall+"/payload"); code != 403 {
		t.Errorf("member = %d", code)
	}
	if code, _, _ := e.payload(e.admin, base+secretsCall+"/payload"); code != 403 {
		t.Errorf("platform admin through the team route = %d", code)
	}
	if code, _, _ := e.payload(e.outsider, "/v1/teams/other/calls/"+secretsCall+"/payload"); code != 404 {
		t.Errorf("another team = %d", code)
	}
	if code, p, _ := e.payload(e.auditor, "/v1/admin/calls/"+secretsCall+"/payload"); code != 200 || p.CallId == 0 {
		t.Errorf("auditor through the admin route = %d", code)
	}
	views := e.scalar(`SELECT string_agg(metadata->>'via', ',' ORDER BY id) || ' ' || bool_and(team_id = (SELECT id FROM teams WHERE slug = 'tools'))::text
		FROM audit_log WHERE action = 'mcp_call.payload_view'`)
	if views != "team,team,team,team,admin true" {
		t.Errorf("views audited = %s", views)
	}

	// Server only: the server's team reads it, through a platform gateway too.
	e.setGatewayCapture(e.owner, e.supportTeamPath, false)
	code, ec = e.setServerCapture(e.owner, true)
	mustCode(t, "server capture", code, ec, 200, "")
	call("shared", "tools/call", map[string]any{"name": "status__whoami"})
	serverCall := e.lastCall()
	if code, p, _ := e.payload(e.owner, base+serverCall+"/payload"); code != 200 || p.CapturedBy[0] != "server" {
		t.Fatalf("server capture = %d %+v", code, p)
	}
	// A platform gateway's switch: only the admin route.
	code, ec = e.setServerCapture(e.owner, false)
	mustCode(t, "server capture off", code, ec, 200, "")
	e.setGatewayCapture(e.admin, e.shared, true)
	call("shared", "tools/call", map[string]any{"name": "status__whoami"})
	platformCall := e.lastCall()
	if code, d := errDetails(t, e.owner, "GET", base+platformCall+"/payload", nil, nil); code != 404 || d["code"] != "payload_not_found" {
		t.Fatalf("platform gateway's capture through the team = %d %v", code, d)
	}
	if code, _, _ := e.payload(e.admin, "/v1/admin/calls/"+platformCall+"/payload"); code != 200 {
		t.Fatalf("admin = %d", code)
	}
	// Both.
	code, ec = e.setServerCapture(e.owner, true)
	mustCode(t, "server capture", code, ec, 200, "")
	call("shared", "tools/call", map[string]any{"name": "status__whoami"})
	e.lastCall()
	if both := e.scalar(`SELECT (by_gateway AND by_server)::text FROM mcp_call_payloads ORDER BY call_id DESC LIMIT 1`); both != "true" {
		t.Fatal("both switches")
	}

	// Transparency fields.
	var sv apitypes.McpServer
	e.owner.get(toolsTeam+"/mcp-servers/"+e.server.Id.String(), &sv)
	mine := myGateways(t, e.member)
	var opts apitypes.GatewayOptionPage
	e.owner.get(toolsTeam+"/gateway-options?limit=100", &opts)
	if sv.CapturePayloads == nil || !*sv.CapturePayloads || !*mine["shared"].CapturesPayloads || !*opts.Items[0].ServerCapturesPayloads {
		t.Errorf("capture flags: server %v, my gateway %v", sv.CapturePayloads, mine["shared"].CapturesPayloads)
	}
	if n := e.scalar(`SELECT count(*)::text FROM audit_log WHERE action = 'mcp_server.update' AND after_state->>'capturePayloads' = 'true'`); n == "0" {
		t.Error("capture changes are audited")
	}

	// The tester shows the redaction without storing or logging the sample.
	var tr apitypes.RedactionTestResult
	code, ec = e.auditor.call("POST", "/v1/admin/settings/capture/test", map[string]any{"sample": `{"token":"sentinel-sample-zz","x":"EMP-654321"}`}, &tr, nil)
	mustCode(t, "test", code, ec, 200, "")
	if tr.Redactions != 2 || strings.Contains(tr.Redacted, "sentinel-sample-zz") || len(tr.Rules) != 2 {
		t.Fatalf("test = %+v", tr)
	}

	// Sentinel: no payload content reaches the logs or the audit log.
	auditText := e.scalar(`SELECT coalesce(string_agg(before_state::text || after_state::text || metadata::text, ' '), '') FROM audit_log`)
	for _, s := range append(sentinels, "sentinel-sample-zz", "Summarise the incident", "employee") {
		if strings.Contains(e.logs.String(), s) {
			t.Errorf("%.20s… reached the logs", s)
		}
		if strings.Contains(auditText, s) {
			t.Errorf("%.20s… reached the audit log", s)
		}
	}
}

func rpcData(out map[string]any) map[string]any {
	e, _ := out["error"].(map[string]any)
	d, _ := e["data"].(map[string]any)
	return d
}

func resultTextOf(out map[string]any) string {
	r, _ := out["result"].(map[string]any)
	content, _ := r["content"].([]any)
	if len(content) == 0 {
		return ""
	}
	text, _ := content[0].(map[string]any)["text"].(string)
	return text
}

func between(s, start, end string) string {
	_, after, ok := strings.Cut(s, start)
	if !ok {
		return ""
	}
	v, _, _ := strings.Cut(after, end)
	return v
}

func hmacHex(pepper, label, token string) string {
	key, _ := secrets.ParseKey(pepper)
	m := hmac.New(sha256.New, key)
	m.Write([]byte(label + token))
	return hex.EncodeToString(m.Sum(nil))
}

func TestPepperRotation(t *testing.T) {
	e := newRuntimeEnv(t)
	pool := e.app.Pool
	unused := newKey(t, e.member, "unused")
	sa := createSA(t, e.owner, map[string]any{"name": "rot-bot"})
	saKey := createSAKey(t, e.owner, sa.Id.String(), map[string]any{"name": "k"})
	// A connected app's tokens, made under the old pepper.
	raw := make([]byte, 32)
	_, _ = rand.Read(raw)
	access := "ompat_" + base64.RawURLEncoding.EncodeToString(raw)
	rolled := replica(t, pool, func(c *config.Config) { c.APIKeyPepper, c.APIKeyPepperPrevious = otherPepper, testEncryptionKey })
	grant := e.scalar(`INSERT INTO oauth_grants (user_id, gateway_id, client_id, client_kind, client_name, resource)
		SELECT u.id, g.id, 'ompcl_rot', 'registered', 'Rot client', $1 FROM users u, gateways g WHERE u.email = 'blair@localhost' AND g.slug = 'support'
		RETURNING id::text`, rolled.URL+"/mcp/support")
	e.scalar(`INSERT INTO oauth_tokens (grant_id, kind, token_hash, resource, expires_at) VALUES ($1, 'access', $2, $3, now() + interval '1 hour')
		RETURNING id::text`, grant, hmacHex(testEncryptionKey, "oauth:", access), rolled.URL+"/mcp/support")

	// The rolled deployment accepts old keys and re-digests them.
	for name, key := range map[string]string{"api key": e.key.Secret, "service account key": saKey.Secret, "oauth token": access} {
		if code, _, out := rawMCP(t, rolled, "POST", "support", key, rpc(1, "tools/list", nil), nil); code != 200 {
			t.Fatalf("%s on the rolled deployment = %d %v", name, code, out)
		}
	}
	newID := hex.EncodeToString(apikeys.PepperID(mustKey(otherPepper)))
	if got := e.scalar(`SELECT digest || ' ' || encode(pepper_id, 'hex') FROM api_keys WHERE id = $1`, e.key.Key.Id.String()); got != hmacHex(otherPepper, "", e.key.Secret)+" "+newID {
		t.Fatalf("api key not re-digested: %s", got)
	}
	if got := e.scalar(`SELECT encode(pepper_id, 'hex') FROM service_account_keys WHERE id = $1`, saKey.Key.Id.String()); got != newID {
		t.Fatalf("service account key not re-digested: %s", got)
	}
	if n := e.scalar(`SELECT count(*)::text FROM audit_log WHERE action IN ('api_key.pepper_rehash', 'service_account_key.pepper_rehash') AND actor_kind = 'system'`); n != "2" {
		t.Fatalf("rehash audit = %s", n)
	}
	// Once the previous pepper is removed, re-digested keys keep working and
	// unused ones don't.
	retired := replica(t, pool, func(c *config.Config) { c.APIKeyPepper = otherPepper })
	if code, _, _ := rawMCP(t, retired, "POST", "support", e.key.Secret, rpc(1, "ping", nil), nil); code != 200 {
		t.Fatalf("re-digested key = %d", code)
	}
	if code, _, _ := rawMCP(t, retired, "POST", "support", saKey.Secret, rpc(1, "ping", nil), nil); code != 200 {
		t.Fatalf("re-digested service account key = %d", code)
	}
	if code, _, _ := rawMCP(t, retired, "POST", "support", unused.Secret, rpc(1, "ping", nil), nil); code != 401 {
		t.Fatalf("unused old key = %d", code)
	}
}

func mustKey(s string) []byte {
	k, _ := secrets.ParseKey(s)
	return k
}

func TestCallDetailAndUsageEndpoints(t *testing.T) {
	e := newRuntimeEnv(t)
	for i := 0; i < 3; i++ {
		rawMCP(t, e.app, "POST", "support", e.key.Secret, rpc(i, "tools/call", map[string]any{"name": "status__whoami"}), nil)
	}
	rawMCP(t, e.app, "POST", "shared", e.key.Secret, rpc(9, "tools/list", nil), nil)
	waitCalls(t, e.app.Pool, 4)
	callID := e.scalar(`SELECT min(id)::text FROM mcp_calls`)
	var c apitypes.McpCall
	if code := e.owner.get(toolsTeam+"/calls/"+callID, &c); code != 200 || c.Gateway.Slug != "support" || c.Captured == nil || *c.Captured {
		t.Fatalf("team call = %d %+v", code, c)
	}
	if code, d := errDetails(t, e.owner, "GET", toolsTeam+"/calls/999999", nil, nil); code != 404 || d["code"] != "call_not_found" {
		t.Fatalf("unknown call = %d %v", code, d)
	}
	if code, d := errDetails(t, e.owner, "GET", toolsTeam+"/calls/"+callID+"/payload", nil, nil); code != 404 || d["code"] != "payload_not_found" {
		t.Fatalf("not captured = %d %v", code, d)
	}
	if code := e.admin.get("/v1/admin/calls/"+callID, &c); code != 200 {
		t.Fatalf("admin call = %d", code)
	}
	// Usage from rollups.
	deps := mustDeps(t, e.app.Pool)
	if _, err := deps.Usage.Rollup(context.Background()); err != nil {
		t.Fatal(err)
	}
	var rep apitypes.UsageReport
	// The platform gateway's list is not the team's (no server, no service
	// account of the team).
	if code := e.editor.get(toolsTeam+"/usage?groupBy=gateway&series=true", &rep); code != 200 || rep.Totals.Calls != 3 || rep.RolledUpTo == nil ||
		len(rep.Rows) != 1 || rep.Rows[0].Ref == nil || rep.Rows[0].Ref.Slug != "support" || rep.Rows[0].Series == nil {
		t.Fatalf("team usage = %d %+v", code, rep)
	}
	if code := e.member.get(toolsTeam+"/usage", &rep); code != 403 {
		t.Fatalf("member usage = %d", code)
	}
	if code, d := errDetails(t, e.editor, "GET", toolsTeam+"/usage?from=2026-01-01T00:30:00Z", nil, nil); code != 400 || d["code"] != "invalid_range" {
		t.Fatalf("bad range = %d %v", code, d)
	}
	if code := e.auditor.get("/v1/admin/usage?groupBy=capability&method=tools/call", &rep); code != 200 || rep.Totals.Calls != 3 ||
		rep.Rows[0].Capability == nil || rep.Rows[0].Capability.ExposedName != "status__whoami" {
		t.Fatalf("admin usage = %d %+v", code, rep)
	}
	// Metrics: the request counters moved; no ID or email is a label.
	m := observability.NewMetrics(e.app.Pool)
	rec := httptest.NewRecorder()
	m.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/metrics", nil))
	text := rec.Body.String()
	if !strings.Contains(text, `omp_mcp_requests_total{method="tools/call",outcome="ok"}`) || !strings.Contains(text, `omp_mcp_servers{state=`) ||
		!strings.Contains(text, "omp_db_pool_max_connections") {
		t.Fatal("metrics missing")
	}
	for _, s := range []string{e.key.Key.Id.String(), "blair@localhost", e.userID("blair@localhost")} {
		if strings.Contains(text, `"`+s+`"`) {
			t.Errorf("%s is a label value", s)
		}
	}
}

func mustDeps(t *testing.T, pool *pgxpool.Pool) httpapi.Deps {
	t.Helper()
	cfg := config.Defaults()
	cfg.AppURL, cfg.DevAuth, cfg.DatabaseURL = "http://localhost:1", true, "unused"
	cfg.EncryptionKey, cfg.APIKeyPepper = testEncryptionKey, testEncryptionKey
	deps, err := app.NewDeps(cfg, pool, testutil.Logger())
	if err != nil {
		t.Fatal(err)
	}
	return deps
}

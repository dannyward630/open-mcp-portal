package httpapi_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/url"
	"slices"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/ncecere/open-mcp-gateway/internal/httpapi/apitypes"
	"github.com/ncecere/open-mcp-gateway/internal/testutil"
)

// Phase 3 follow-ups: gateway-options paging, resources and resource
// templates, and API keys limited to gateways.

func optionPage(t *testing.T, s *session, path string) apitypes.GatewayOptionPage {
	t.Helper()
	var page apitypes.GatewayOptionPage
	if code := s.get(path, &page); code != 200 {
		t.Fatalf("%s = %d", path, code)
	}
	return page
}

// allOptions reads every page of base (which has a query string) limit at a
// time.
func allOptions(t *testing.T, s *session, base string, limit int) ([]apitypes.GatewayOption, int) {
	t.Helper()
	var out []apitypes.GatewayOption
	pages := 0
	cursor := ""
	for {
		path := fmt.Sprintf("%s&limit=%d", base, limit)
		if cursor != "" {
			path += "&cursor=" + url.QueryEscape(cursor)
		}
		page := optionPage(t, s, path)
		pages++
		if len(page.Items) > limit {
			t.Fatalf("page of %d > %d", len(page.Items), limit)
		}
		out = append(out, page.Items...)
		if page.NextCursor == nil {
			return out, pages
		}
		cursor = *page.NextCursor
		if pages > 50 {
			t.Fatal("paging doesn't end")
		}
	}
}

func TestGatewayOptionsPaging(t *testing.T) {
	e := newGwEnv(t, nil)
	e.publish(e.outsider, e.other)

	for _, path := range []string{toolsTeam + "/gateway-options?q=", "/v1/admin/gateway-options?q="} {
		s := e.owner
		if strings.HasPrefix(path, "/v1/admin") {
			s = e.auditor
		}
		full := optionPage(t, s, path+"&limit=500").Items
		if len(full) < 8 {
			t.Fatalf("%s: %d options", path, len(full))
		}
		paged, pages := allOptions(t, s, path, 3)
		if pages != (len(full)+2)/3 {
			t.Errorf("%s: %d pages for %d options", path, pages, len(full))
		}
		ids := func(opts []apitypes.GatewayOption) []string {
			out := []string{}
			for _, o := range opts {
				out = append(out, o.CapabilityId.String())
			}
			return out
		}
		if !slices.Equal(ids(paged), ids(full)) {
			t.Fatalf("%s: paged %v, whole %v", path, ids(paged), ids(full))
		}
		// Ordered by server: each server's options are together.
		seen := map[string]bool{}
		last := ""
		for _, o := range full {
			if o.ServerSlug != last {
				if seen[o.ServerSlug] {
					t.Fatalf("%s: server %s isn't contiguous", path, o.ServerSlug)
				}
				seen[o.ServerSlug], last = true, o.ServerSlug
			}
		}
		// Default page size is 100.
		if got := optionPage(t, s, path); len(got.Items) != len(full) || got.NextCursor != nil {
			t.Errorf("%s default page = %d", path, len(got.Items))
		}
	}

	// Filters: kind, server, search.
	page := optionPage(t, e.owner, toolsTeam+"/gateway-options?kind=resource")
	got := []string{}
	for _, o := range page.Items {
		got = append(got, o.ExposedName)
		if o.Kind != "resource" {
			t.Errorf("kind filter: %+v", o)
		}
	}
	slices.Sort(got)
	if !slices.Equal(got, []string{"omcp://other-status/status://services", "omcp://status/status://services"}) {
		t.Fatalf("resources = %v", got)
	}
	page = optionPage(t, e.owner, toolsTeam+"/gateway-options?kind=resource_template&server="+e.other.Id.String())
	if len(page.Items) != 1 || page.Items[0].ExposedName != "omcp://other-status/status://services/{service}" || page.Items[0].Source != "catalog" {
		t.Fatalf("templates of other = %+v", page.Items)
	}
	page = optionPage(t, e.owner, toolsTeam+"/gateway-options?q=whoami&server="+e.server.Id.String())
	if len(page.Items) != 1 || page.Items[0].ExposedName != "status__whoami" {
		t.Fatalf("search = %+v", page.Items)
	}
	page = optionPage(t, e.admin, "/v1/admin/gateway-options?server="+e.server.Id.String())
	if len(page.Items) != 0 {
		t.Fatalf("a private server is offered to platform gateways: %+v", page.Items)
	}
	for _, q := range []string{"limit=0", "limit=501", "limit=x", "cursor=nope", "server=nope", "kind=sampling",
		"cursor=" + url.QueryEscape(base64JSON(`["a","b","c","d"]`))} {
		if code, raw := e.owner.raw("GET", toolsTeam+"/gateway-options?"+q, nil, nil); code != 400 {
			t.Errorf("%s = %d %s", q, code, raw)
		}
	}
}

func base64JSON(s string) string { return base64.RawURLEncoding.EncodeToString([]byte(s)) }

// insertCapability adds a listed capability to a server directly (as a
// refresh would) and returns its ID.
func (e *gwEnv) insertCapability(sv apitypes.McpServer, kind, name string) string {
	e.t.Helper()
	def, _ := json.Marshal(map[string]any{"uri": name, "uriTemplate": name, "name": "inserted"})
	return e.scalar(`INSERT INTO mcp_capabilities (server_id, kind, name, definition, fingerprint)
		VALUES ($1, $2, $3, $4, repeat('c', 64)) RETURNING id::text`, sv.Id, kind, name, def)
}

func TestGatewayResourceSelection(t *testing.T) {
	e := newGwEnv(t, nil)
	gw := e.createGateway(e.owner, toolsTeam+"/gateways", "docs")
	path := toolsTeam + "/gateways/" + gw.Id.String()
	resource := e.capID(e.owner, e.server, "resource", "status://services")
	template := e.capID(e.owner, e.server, "resource_template", "status://services/{service}")
	got := e.mustSetCaps(e.owner, path, sel(resource, template+":admin"))
	if !slices.Equal(exposed(got.Items), []string{"omcp://status/status://services", "omcp://status/status://services/{service}"}) {
		t.Fatalf("selection = %v", exposed(got.Items))
	}
	for _, it := range got.Items {
		if !it.Available || (it.Kind != "resource" && it.Kind != "resource_template") {
			t.Errorf("item %+v", it)
		}
	}

	// A template with other RFC 6570 operators isn't offered, and can't be
	// selected, with the reason in the error.
	unsupported := e.insertCapability(e.server, "resource_template", "status://search{?q}")
	for _, o := range optionPage(t, e.owner, toolsTeam+"/gateway-options?kind=resource_template").Items {
		if o.CapabilityId.String() == unsupported {
			t.Fatal("an unsupported template is offered")
		}
	}
	code, raw := e.setCaps(e.owner, path, sel(resource, unsupported))
	if code != 422 || errorCode(raw) != "capability_not_selectable" || !strings.Contains(string(raw), `"`+unsupported+`":"unsupported_template"`) {
		t.Fatalf("unsupported template = %d %s", code, raw)
	}

	// Exposed URIs clash case-insensitively, like names.
	upper := e.insertCapability(e.server, "resource", "STATUS://services")
	code, raw = e.setCaps(e.owner, path, sel(resource, upper))
	if code != 422 || errorCode(raw) != "name_clash" || !strings.Contains(string(raw), "omcp://status/") {
		t.Fatalf("uri clash = %d %s", code, raw)
	}

	// My gateways lists them with their exposed URIs (role applied).
	var detail apitypes.MyGatewayDetail
	if code := e.member.get("/v1/me/gateways/"+gw.Id.String(), &detail); code != 200 ||
		!slices.Equal(exposed(detail.Capabilities), []string{"omcp://status/status://services"}) {
		t.Fatalf("my gateway = %d %+v", code, detail.Capabilities)
	}
	if g := e.gateway(e.owner, path); g.CapabilityCount != 2 || g.UnavailableCount != 0 {
		t.Fatalf("counts = %+v", g)
	}
}

func TestMCPResources(t *testing.T) {
	e := newGwEnv(t, nil)
	jwks := e.app.URL + "/.well-known/jwks.json"
	e.fake.SetJWKS(jwks, e.fake.URL())
	gw := e.createGateway(e.owner, toolsTeam+"/gateways", "docs")
	path := toolsTeam + "/gateways/" + gw.Id.String()
	resource := e.capID(e.owner, e.server, "resource", "status://services")
	template := e.capID(e.owner, e.server, "resource_template", "status://services/{service}")
	echo := e.capID(e.owner, e.server, "tool", "echo")
	e.mustSetCaps(e.owner, path, sel(resource, template, echo))
	key := newKey(t, e.member, "laptop")
	ctx := context.Background()

	cs := connect(t, e.app, "docs", key.Secret)
	if init := cs.InitializeResult(); init.Capabilities.Resources == nil || init.Capabilities.Resources.Subscribe {
		t.Fatalf("initialize = %+v", init.Capabilities)
	}
	res, err := cs.ListResources(ctx, nil)
	if err != nil || len(res.Resources) != 1 {
		t.Fatalf("resources/list = %+v %v", res, err)
	}
	if r := res.Resources[0]; r.URI != "omcp://status/status://services" || r.Name != "services" || r.Title != "Services" ||
		r.Description != "Every monitored service." || r.MIMEType != "text/plain" {
		t.Fatalf("resource = %+v", r)
	}
	tmpl, err := cs.ListResourceTemplates(ctx, nil)
	if err != nil || len(tmpl.ResourceTemplates) != 1 || tmpl.ResourceTemplates[0].URITemplate != "omcp://status/status://services/{service}" ||
		tmpl.ResourceTemplates[0].Name != "service" {
		t.Fatalf("resources/templates/list = %+v %v", tmpl, err)
	}

	// A fixed resource: forwarded with the identity (resource claim), URIs
	// given back in the exposed form.
	read, err := cs.ReadResource(ctx, &mcp.ReadResourceParams{URI: "omcp://status/status://services"})
	if err != nil || len(read.Contents) != 1 || read.Contents[0].URI != "omcp://status/status://services" || read.Contents[0].Text != "email\nvpn" {
		t.Fatalf("read fixed = %+v %v", read, err)
	}
	reads := e.fake.ResourceReads()
	if len(reads) != 1 || reads[0].URI != "status://services" {
		t.Fatalf("fake read %+v", reads)
	}
	var claims struct {
		Resource string `json:"resource"`
		Tool     string `json:"tool"`
		Gateway  string `json:"gateway"`
	}
	if err := testutil.VerifyIdentity(ctx, strings.TrimPrefix(reads[0].Authorization, "Bearer "), jwks, e.fake.URL(), &claims); err != nil ||
		claims.Resource != "omcp://status/status://services" || claims.Tool != "" || claims.Gateway != "docs" {
		t.Fatalf("identity: %v %+v", err, claims)
	}
	if strings.Contains(reads[0].Authorization, key.Secret) {
		t.Fatal("the key reached the server")
	}

	// A templated one.
	read, err = cs.ReadResource(ctx, &mcp.ReadResourceParams{URI: "omcp://status/status://services/email"})
	if err != nil || len(read.Contents) != 1 || read.Contents[0].URI != "omcp://status/status://services/email" ||
		read.Contents[0].Text != "operating normally" {
		t.Fatalf("read templated = %+v %v", read, err)
	}
	if reads := e.fake.ResourceReads(); len(reads) != 2 || reads[1].URI != "status://services/email" {
		t.Fatalf("fake reads %+v", reads)
	}

	// URIs matching nothing selected are refused without contacting the
	// server.
	before := e.fake.Requests()
	for _, uri := range []string{
		"omcp://status/status://services/a/b",      // "/" needs {+var}
		"omcp://status/status://services/..",       // dot segment
		"omcp://status/status://other",             // not selected
		"omcp://other-status/status://services",    // another server
		"status://services",                        // not exposed
		"omcp://status/status://services/email?x=", // reserved characters
	} {
		if _, err := cs.ReadResource(ctx, &mcp.ReadResourceParams{URI: uri}); err == nil {
			t.Errorf("read %s went through", uri)
		}
	}
	if e.fake.Requests() != before {
		t.Fatal("a refused read reached the server")
	}
	code, _, out := rawMCP(t, e.app, "POST", "docs", key.Secret, rpc(1, "resources/read", map[string]any{"uri": "omcp://status/status://other"}), nil)
	if code != 200 || rpcErrorCode(out) != -32002 {
		t.Fatalf("raw refused read = %d %v", code, out)
	}
	for _, m := range []string{"resources/subscribe", "resources/unsubscribe"} {
		if code, _, out := rawMCP(t, e.app, "POST", "docs", key.Secret, rpc(2, m, map[string]any{"uri": "omcp://status/status://services"}), nil); code != 200 || rpcErrorCode(out) != -32601 {
			t.Errorf("%s = %d %v", m, code, out)
		}
	}

	// Between list and read: the template is removed, the resource needs
	// admin. Both reads are refused without reaching the server.
	e.mustSetCaps(e.owner, path, sel(resource+":admin", echo))
	before = e.fake.Requests()
	for _, uri := range []string{"omcp://status/status://services/email", "omcp://status/status://services"} {
		if _, err := cs.ReadResource(ctx, &mcp.ReadResourceParams{URI: uri}); err == nil {
			t.Errorf("read %s after revocation went through", uri)
		}
	}
	if e.fake.Requests() != before {
		t.Fatal("a revoked read reached the server")
	}
	if res, err := cs.ListResources(ctx, nil); err != nil || len(res.Resources) != 0 {
		t.Fatalf("resources after revocation = %+v %v", res, err)
	}
	// The owner (admin and above) still reads it.
	ownerKey := newKey(t, e.owner, "owner")
	if read, err := connect(t, e.app, "docs", ownerKey.Secret).ReadResource(ctx, &mcp.ReadResourceParams{URI: "omcp://status/status://services"}); err != nil ||
		len(read.Contents) != 1 {
		t.Fatalf("owner read = %+v %v", read, err)
	}

	records := e.scalar(`SELECT string_agg(method || ' ' || outcome || ' ' || coalesce(error_class, '-') || ' ' || coalesce(exposed_name, '-'), ', ' ORDER BY id) FROM mcp_calls`)
	for _, want := range []string{
		"resources/list ok - -", "resources/templates/list ok - -",
		"resources/read ok - omcp://status/status://services,", "resources/read ok - omcp://status/status://services/email",
		"resources/read denied unknown omcp://status/status://other", "resources/read denied unknown omcp://status/status://services/email",
		"resources/read denied role omcp://status/status://services",
	} {
		if !strings.Contains(records+",", want) {
			t.Errorf("calls %q lack %q", records, want)
		}
	}

	// A gateway without resources doesn't advertise or serve them.
	plain := e.createGateway(e.owner, toolsTeam+"/gateways", "plain")
	e.mustSetCaps(e.owner, toolsTeam+"/gateways/"+plain.Id.String(), sel(echo))
	if init := connect(t, e.app, "plain", key.Secret).InitializeResult(); init.Capabilities.Resources != nil {
		t.Fatalf("plain initialize = %+v", init.Capabilities)
	}
	if code, _, out := rawMCP(t, e.app, "POST", "plain", key.Secret, rpc(3, "resources/read", map[string]any{"uri": "omcp://status/status://services"}), nil); code != 200 || rpcErrorCode(out) != -32601 {
		t.Errorf("plain read = %d %v", code, out)
	}
}

func createKey(t *testing.T, s *session, body map[string]any) (int, []byte, apitypes.ApiKeyCreated) {
	t.Helper()
	code, raw := s.raw("POST", "/v1/me/api-keys", body, nil)
	var env struct{ Data apitypes.ApiKeyCreated }
	_ = json.Unmarshal(raw, &env)
	return code, raw, env.Data
}

func TestAPIKeyScopes(t *testing.T) {
	logs := &syncBuffer{}
	e := &rtEnv{gwEnv: newGwEnv(t, slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug})))}
	whoami := e.capID(e.owner, e.server, "tool", "whoami")
	e.publish(e.owner, e.server)
	support := e.createGateway(e.owner, toolsTeam+"/gateways", "support")
	e.supportTeamPath = toolsTeam + "/gateways/" + support.Id.String()
	e.mustSetCaps(e.owner, e.supportTeamPath, sel(whoami))
	shared := e.createGateway(e.admin, "/v1/admin/gateways", "shared")
	e.shared = "/v1/admin/gateways/" + shared.Id.String()
	e.mustSetCaps(e.admin, e.shared, sel(whoami))
	var grant apitypes.GatewayGrant
	code, ec := e.admin.call("POST", e.shared+"/grants", map[string]any{"subjectKind": "user", "userEmail": "blair@localhost"}, &grant, nil)
	mustCode(t, "grant", code, ec, 201, "")

	// Limited to the shared gateway (duplicates collapse).
	code, raw, scoped := createKey(t, e.member, map[string]any{"name": "shared only", "gatewayIds": []string{shared.Id.String(), shared.Id.String()}})
	if code != 201 || len(scoped.Key.Gateways) != 1 || scoped.Key.Gateways[0].Id != shared.Id || scoped.Key.Gateways[0].Slug != "shared" ||
		!scoped.Key.Gateways[0].Exists {
		t.Fatalf("scoped key = %d %s", code, raw)
	}
	all := newKey(t, e.member, "everywhere")
	if len(all.Key.Gateways) != 0 {
		t.Fatalf("unscoped key = %+v", all.Key)
	}
	toolsList := func(slug, secret string) int {
		t.Helper()
		code, _, _ := rawMCP(t, e.app, "POST", slug, secret, rpc(1, "tools/list", nil), nil)
		return code
	}
	if toolsList("shared", scoped.Secret) != 200 || toolsList("support", all.Secret) != 200 || toolsList("shared", all.Secret) != 200 {
		t.Fatal("a key in scope was refused")
	}
	requests := e.fake.Requests()
	if code := toolsList("support", scoped.Secret); code != 404 {
		t.Fatalf("out of scope = %d", code)
	}
	code, _, out := rawMCP(t, e.app, "POST", "support", scoped.Secret, rpc(1, "tools/call", map[string]any{"name": "status__whoami"}), nil)
	if code != 404 || rpcErrorCode(out) == 0 || e.fake.Requests() != requests {
		t.Fatalf("out of scope call = %d %v", code, out)
	}
	if got := e.scalar(`SELECT method || ':' || outcome || ':' || coalesce(error_class, '') FROM mcp_calls ORDER BY id DESC LIMIT 1`); got != "tools/call:denied:key_scope" {
		t.Fatalf("recorded %s", got)
	}

	// Only gateways the creator can use now.
	outsiderGw := e.createGateway(e.outsider, "/v1/teams/other/gateways", "other-gw")
	unknown := "7f1d5c64-1f3c-4c3e-9a51-9f6a4e0f8b10"
	code, raw, _ = createKey(t, e.member, map[string]any{"name": "x", "gatewayIds": []string{shared.Id.String(), outsiderGw.Id.String(), unknown}})
	if code != 422 || errorCode(raw) != "gateway_not_usable" || !strings.Contains(string(raw), outsiderGw.Id.String()) ||
		!strings.Contains(string(raw), unknown) || strings.Contains(string(raw), `"`+shared.Id.String()+`"`) {
		t.Fatalf("not usable = %d %s", code, raw)
	}
	many := []string{}
	for i := 0; i < 51; i++ {
		many = append(many, fmt.Sprintf("7f1d5c64-1f3c-4c3e-9a51-%012d", i))
	}
	if code, raw, _ := createKey(t, e.member, map[string]any{"name": "x", "gatewayIds": many}); code != 400 || errorCode(raw) != "too_many_gateways" {
		t.Fatalf("51 gateways = %d %s", code, raw)
	}
	// The platform admin can't use the team gateway (not a member).
	if code, raw, _ := createKey(t, e.admin, map[string]any{"name": "x", "gatewayIds": []string{support.Id.String()}}); code != 422 {
		t.Fatalf("admin scope to team gateway = %d %s", code, raw)
	}
	// A switched-off gateway can't be chosen.
	code, ec = e.owner.call("PATCH", e.supportTeamPath, map[string]any{"enabled": false}, nil, ifMatch(e.gateway(e.owner, e.supportTeamPath).Revision))
	mustCode(t, "switch off", code, ec, 200, "")
	if code, raw, _ := createKey(t, e.member, map[string]any{"name": "x", "gatewayIds": []string{support.Id.String()}}); code != 422 {
		t.Fatalf("scope to a disabled gateway = %d %s", code, raw)
	}
	code, ec = e.owner.call("PATCH", e.supportTeamPath, map[string]any{"enabled": true}, nil, ifMatch(e.gateway(e.owner, e.supportTeamPath).Revision))
	mustCode(t, "switch on", code, ec, 200, "")

	// A scope never widens access: without the grant, the scoped key is
	// refused for no_access.
	code, ec = e.admin.call("DELETE", e.shared+"/grants/"+grant.Id.String(), nil, nil, nil)
	mustCode(t, "remove grant", code, ec, 200, "")
	if code := toolsList("shared", scoped.Secret); code != 404 {
		t.Fatalf("scoped key without access = %d", code)
	}
	if got := e.scalar(`SELECT outcome || ':' || coalesce(error_class, '') FROM mcp_calls ORDER BY id DESC LIMIT 1`); got != "denied:no_access" {
		t.Fatalf("recorded %s", got)
	}

	// Deleting the gateway leaves the entry; the key works nowhere.
	code, ec = e.admin.call("DELETE", e.shared, nil, nil, nil)
	mustCode(t, "delete shared", code, ec, 200, "")
	var keys []apitypes.ApiKey
	e.member.get("/v1/me/api-keys", &keys)
	var listed *apitypes.ApiKey
	for i := range keys {
		if keys[i].Id == scoped.Key.Id {
			listed = &keys[i]
		}
		if keys[i].Id == all.Key.Id && len(keys[i].Gateways) != 0 {
			t.Errorf("unscoped key lists gateways %+v", keys[i].Gateways)
		}
	}
	if listed == nil || len(listed.Gateways) != 1 || listed.Gateways[0].Exists || listed.Gateways[0].Slug != "shared" || listed.Gateways[0].Name != "SHARED" {
		t.Fatalf("listed = %+v", listed)
	}
	if code := toolsList("support", scoped.Secret); code != 404 {
		t.Fatalf("dangling scope = %d", code)
	}
	if code := toolsList("support", all.Secret); code != 200 {
		t.Fatalf("unscoped key = %d", code)
	}

	// Audit: the scoped gateway IDs; never the secrets.
	if after := e.scalar(`SELECT after_state::text FROM audit_log WHERE action = 'api_key.create' AND target_id = $1`, scoped.Key.Id.String()); !strings.Contains(after, shared.Id.String()) {
		t.Fatalf("audit = %s", after)
	}
	code, ec = e.member.call("DELETE", "/v1/me/api-keys/"+scoped.Key.Id.String(), nil, nil, nil)
	mustCode(t, "revoke", code, ec, 200, "")
	if before := e.scalar(`SELECT before_state::text FROM audit_log WHERE action = 'api_key.revoke' AND target_id = $1`, scoped.Key.Id.String()); !strings.Contains(before, shared.Id.String()) {
		t.Fatalf("revoke audit = %s", before)
	}
	for what, text := range map[string]string{
		"api_keys":         e.scalar(`SELECT string_agg(row_to_json(k)::text, '') FROM api_keys k`),
		"api_key_gateways": e.scalar(`SELECT coalesce(string_agg(row_to_json(k)::text, ''), '') FROM api_key_gateways k`),
		"audit_log":        e.scalar(`SELECT string_agg(row_to_json(a)::text, '') FROM audit_log a`),
		"mcp_calls":        e.scalar(`SELECT string_agg(row_to_json(c)::text, '') FROM mcp_calls c`),
		"logs":             logs.String(),
	} {
		for _, secret := range []string{scoped.Secret, all.Secret} {
			if strings.Contains(text, secret) || strings.Contains(text, secret[len("omcp_"):]) {
				t.Errorf("%s contains an API key", what)
			}
		}
	}
	_, rawList := e.member.raw("GET", "/v1/me/api-keys", nil, nil)
	if strings.Contains(string(rawList), scoped.Secret) || strings.Contains(string(rawList), all.Secret) {
		t.Fatal("the key list contains a secret")
	}
}

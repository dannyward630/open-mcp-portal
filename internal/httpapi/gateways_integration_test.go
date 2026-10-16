package httpapi_test

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"testing"

	"github.com/ncecere/open-mcp-gateway/internal/httpapi/apitypes"
	"github.com/ncecere/open-mcp-gateway/internal/testutil"
)

// gwEnv is two teams with a server each: "tools" (owner user, editor alex,
// member blair) with server "status" on fake, and "other" (owner casey)
// with the private server "other-status" on otherFake. Both fakes have the
// gateway tools (whoami, echo).
type gwEnv struct {
	t                                               *testing.T
	app                                             *testApp
	admin, auditor, owner, editor, member, outsider *session
	fake, otherFake                                 *testutil.FakeMCP
	server, other                                   apitypes.McpServer
}

func newGwEnv(t *testing.T, log *slog.Logger) *gwEnv {
	t.Helper()
	if log == nil {
		log = testutil.Logger()
	}
	app := newTestAppLog(t, nil, log)
	e := &gwEnv{t: t, app: app, fake: testutil.NewFakeMCP(t), otherFake: testutil.NewFakeMCP(t)}
	e.fake.EnableGatewayTools()
	e.otherFake.EnableGatewayTools()
	e.admin, e.auditor = app.signIn("admin"), app.signIn("auditor")
	e.owner, e.editor, e.member, e.outsider = app.signIn("user"), app.signIn("alex"), app.signIn("blair"), app.signIn("casey")
	createTeam(t, e.admin, "tools", "user@localhost")
	createTeam(t, e.admin, "other", "casey@localhost")
	for email, role := range map[string]string{"alex@localhost": "editor", "blair@localhost": "member"} {
		code, ec := e.owner.call("POST", toolsTeam+"/members", map[string]any{"email": email, "role": role}, nil, nil)
		mustCode(t, "add "+email, code, ec, 201, "")
	}
	e.server = e.createServer(e.owner, "tools", "status", e.fake.URL(), nil)
	e.other = e.createServer(e.outsider, "other", "other-status", e.otherFake.URL(), nil)
	return e
}

func (e *gwEnv) createServer(s *session, team, slug, url string, extra map[string]any) apitypes.McpServer {
	e.t.Helper()
	body := map[string]any{"slug": slug, "name": strings.ToUpper(slug), "url": url}
	for k, v := range extra {
		body[k] = v
	}
	var out apitypes.McpServerCreated
	code, ec := s.call("POST", "/v1/teams/"+team+"/mcp-servers", body, &out, nil)
	mustCode(e.t, "create server "+slug, code, ec, 201, "")
	if out.RefreshError != nil {
		e.t.Fatalf("discovery of %s failed: %+v", slug, out.RefreshError)
	}
	return out.Server
}

// capID is the ID of a server's capability of kind and name.
func (e *gwEnv) capID(s *session, sv apitypes.McpServer, kind, name string) string {
	e.t.Helper()
	var caps []apitypes.McpCapability
	if code := s.get("/v1/teams/"+sv.TeamSlug+"/mcp-servers/"+sv.Id.String()+"/capabilities", &caps); code != 200 {
		e.t.Fatalf("capabilities = %d", code)
	}
	for _, c := range caps {
		if string(c.Kind) == kind && c.Name == name {
			return c.Id.String()
		}
	}
	e.t.Fatalf("no %s %s on %s", kind, name, sv.Slug)
	return ""
}

// publish publishes a server (its owner asks, the platform admin approves).
func (e *gwEnv) publish(s *session, sv apitypes.McpServer) {
	e.t.Helper()
	base := "/v1/teams/" + sv.TeamSlug + "/mcp-servers/" + sv.Id.String()
	code, ec := s.call("POST", base+"/publish", nil, nil, nil)
	mustCode(e.t, "publish", code, ec, 200, "")
	code, ec = e.admin.call("POST", "/v1/admin/mcp-servers/"+sv.Id.String()+"/review", map[string]any{"decision": "approve"}, nil, nil)
	mustCode(e.t, "approve", code, ec, 200, "")
}

func (e *gwEnv) refresh(s *session, sv apitypes.McpServer) {
	e.t.Helper()
	code, ec := s.call("POST", "/v1/teams/"+sv.TeamSlug+"/mcp-servers/"+sv.Id.String()+"/refresh", nil, nil, nil)
	mustCode(e.t, "refresh", code, ec, 200, "")
}

// patchServer changes a server with its current revision.
func (e *gwEnv) patchServer(s *session, sv apitypes.McpServer, body map[string]any) {
	e.t.Helper()
	path := "/v1/teams/" + sv.TeamSlug + "/mcp-servers/" + sv.Id.String()
	var cur apitypes.McpServer
	if code := s.get(path, &cur); code != 200 {
		e.t.Fatalf("get server = %d", code)
	}
	code, ec := s.call("PATCH", path, body, nil, ifMatch(cur.Revision))
	mustCode(e.t, "patch server", code, ec, 200, "")
}

// gateway reads a gateway (path: its team or admin URL).
func (e *gwEnv) gateway(s *session, path string) apitypes.Gateway {
	e.t.Helper()
	var g apitypes.Gateway
	if code := s.get(path, &g); code != 200 {
		e.t.Fatalf("get %s = %d", path, code)
	}
	return g
}

func (e *gwEnv) createGateway(s *session, path, slug string) apitypes.Gateway {
	e.t.Helper()
	var g apitypes.Gateway
	code, ec := s.call("POST", path, map[string]any{"slug": slug, "name": strings.ToUpper(slug)}, &g, nil)
	mustCode(e.t, "create gateway "+slug, code, ec, 201, "")
	return g
}

// sel builds a selection: capability IDs, each optionally "id:minRole".
func sel(ids ...string) map[string]any {
	items := []map[string]any{}
	for _, id := range ids {
		item := map[string]any{}
		if capID, role, ok := strings.Cut(id, ":"); ok {
			item["capabilityId"], item["minRole"] = capID, role
		} else {
			item["capabilityId"] = id
		}
		items = append(items, item)
	}
	return map[string]any{"items": items}
}

// setCaps replaces a gateway's selection with its current revision; path is
// the gateway's URL.
func (e *gwEnv) setCaps(s *session, path string, body map[string]any) (int, []byte) {
	e.t.Helper()
	g := e.gateway(e.admin, "/v1/admin/gateways/"+path[strings.LastIndex(path, "/")+1:])
	return s.raw("PUT", path+"/capabilities", body, ifMatch(g.Revision))
}

func (e *gwEnv) mustSetCaps(s *session, path string, body map[string]any) apitypes.GatewayCapabilities {
	e.t.Helper()
	code, raw := e.setCaps(s, path, body)
	if code != 200 {
		e.t.Fatalf("set capabilities = %d %s", code, raw)
	}
	var env struct{ Data apitypes.GatewayCapabilities }
	_ = json.Unmarshal(raw, &env)
	return env.Data
}

func exposed(items []apitypes.GatewayCapability) []string {
	out := []string{}
	for _, it := range items {
		out = append(out, it.ExposedName)
	}
	slices.Sort(out)
	return out
}

func TestGatewaySelectionRules(t *testing.T) {
	e := newGwEnv(t, nil)
	base := toolsTeam + "/gateways"
	body := map[string]any{"slug": "support", "name": "Support"}

	// Owners and admins create team gateways; nobody else does.
	code, ec := e.member.call("POST", base, body, nil, nil)
	mustCode(t, "member create", code, ec, 403, "forbidden")
	code, ec = e.editor.call("POST", base, body, nil, nil)
	mustCode(t, "editor create", code, ec, 403, "forbidden")
	code, ec = e.outsider.call("POST", base, body, nil, nil)
	mustCode(t, "outsider create", code, ec, 404, "team_not_found")
	code, ec = e.admin.call("POST", base, body, nil, nil)
	mustCode(t, "platform admin create", code, ec, 403, "forbidden")
	gw := e.createGateway(e.owner, base, "support")
	if gw.Url != e.app.URL+"/mcp/support" || gw.OwnerKind != "team" || gw.TeamSlug == nil || *gw.TeamSlug != "tools" || !gw.Enabled ||
		gw.CapabilityCount != 0 || gw.GrantCount != 0 {
		t.Fatalf("gateway = %+v", gw)
	}
	gwPath := base + "/" + gw.Id.String()
	code, ec = e.outsider.call("POST", "/v1/teams/other/gateways", body, nil, nil)
	mustCode(t, "slug taken", code, ec, 409, "slug_taken")
	code, ec = e.admin.call("POST", "/v1/admin/gateways", body, nil, nil)
	mustCode(t, "slug taken (platform)", code, ec, 409, "slug_taken")
	code, ec = e.owner.call("POST", base, map[string]any{"slug": "Bad_Slug", "name": "x"}, nil, nil)
	mustCode(t, "bad slug", code, ec, 400, "invalid_slug")
	if code := e.member.get(gwPath, nil); code != 200 {
		t.Fatalf("member read = %d", code)
	}
	if code := e.outsider.get(gwPath, nil); code != 404 {
		t.Fatalf("outsider read = %d", code)
	}

	// Options: the team's own server (tools and prompts, no resources), and
	// nothing of another team's private server.
	var opts []apitypes.GatewayOption
	if code := e.owner.get(toolsTeam+"/gateway-options", &opts); code != 200 {
		t.Fatalf("options = %d", code)
	}
	names := []string{}
	for _, o := range opts {
		names = append(names, o.ExposedName)
		if o.Source != "team" || o.ServerSlug != "status" {
			t.Errorf("option %+v", o)
		}
	}
	slices.Sort(names)
	want := []string{"status__check_outage", "status__create_ticket", "status__echo", "status__incident_summary", "status__search_tickets", "status__whoami"}
	if !slices.Equal(names, want) {
		t.Fatalf("options = %v, want %v", names, want)
	}
	if code := e.member.get(toolsTeam+"/gateway-options", nil); code != 403 {
		t.Fatalf("member options = %d", code)
	}
	if code := e.owner.get(toolsTeam+"/gateway-options?kind=resource", nil); code != 400 {
		t.Fatalf("resource kind = %d", code)
	}

	// Another team's private server, a resource, and an unknown ID are refused.
	foreign := e.capID(e.outsider, e.other, "tool", "check_outage")
	resource := e.capID(e.owner, e.server, "resource", "status://services")
	for what, id := range map[string]string{"foreign private": foreign, "resource": resource, "unknown": "7f1d5c64-1f3c-4c3e-9a51-9f6a4e0f8b10"} {
		code, raw := e.setCaps(e.owner, gwPath, sel(id))
		if code != 422 || errorCode(raw) != "capability_not_selectable" || !strings.Contains(string(raw), id) {
			t.Errorf("%s: %d %s", what, code, raw)
		}
	}

	// Once published (and approved), the other team's server is in the catalog.
	e.publish(e.outsider, e.other)
	opts = nil
	e.owner.get(toolsTeam+"/gateway-options?q=other", &opts)
	if len(opts) == 0 || opts[0].Source != "catalog" || opts[0].TeamSlug != "other" {
		t.Fatalf("catalog options = %+v", opts)
	}
	// A capability it adds after approval isn't approved: not selectable.
	e.otherFake.AddTool("brand_new")
	e.refresh(e.outsider, e.other)
	unapproved := e.capID(e.outsider, e.other, "tool", "brand_new")
	if code, raw := e.setCaps(e.owner, gwPath, sel(unapproved)); code != 422 || errorCode(raw) != "capability_not_selectable" {
		t.Fatalf("unapproved catalog capability: %d %s", code, raw)
	}

	whoami, echo := e.capID(e.owner, e.server, "tool", "whoami"), e.capID(e.owner, e.server, "tool", "echo")
	got := e.mustSetCaps(e.owner, gwPath, sel(whoami+":admin", echo, foreign))
	if !slices.Equal(exposed(got.Items), []string{"other-status__check_outage", "status__echo", "status__whoami"}) {
		t.Fatalf("selection = %v", exposed(got.Items))
	}
	for _, it := range got.Items {
		if !it.Available || (it.Name == "whoami") != (it.MinRole != nil && *it.MinRole == "admin") {
			t.Errorf("item %+v", it)
		}
	}
	if code, raw := e.setCaps(e.member, gwPath, sel(echo)); code != 403 {
		t.Fatalf("member set = %d %s", code, raw)
	}
	code, raw := e.owner.raw("PUT", gwPath+"/capabilities", sel(echo), ifMatch(1))
	if code != 412 {
		t.Fatalf("stale revision = %d %s", code, raw)
	}
	if code, raw := e.setCaps(e.owner, gwPath, sel(echo, echo)); code != 400 || errorCode(raw) != "duplicate_capability" {
		t.Fatalf("duplicate = %d %s", code, raw)
	}
	if code, raw := e.setCaps(e.owner, gwPath, sel(echo+":boss")); code != 400 || errorCode(raw) != "invalid_role" {
		t.Fatalf("bad role = %d %s", code, raw)
	}

	// A tool with the same name as a prompt would expose the same name.
	e.fake.AddTool("incident_summary")
	e.refresh(e.owner, e.server)
	clashTool, clashPrompt := e.capID(e.owner, e.server, "tool", "incident_summary"), e.capID(e.owner, e.server, "prompt", "incident_summary")
	if code, raw := e.setCaps(e.owner, gwPath, sel(clashTool, clashPrompt)); code != 422 || errorCode(raw) != "name_clash" ||
		!strings.Contains(string(raw), "status__incident_summary") {
		t.Fatalf("name clash = %d %s", code, raw)
	}

	// Platform gateways: catalog capabilities only, and no minimum role.
	shared := e.createGateway(e.admin, "/v1/admin/gateways", "shared")
	sharedPath := "/v1/admin/gateways/" + shared.Id.String()
	if code, raw := e.setCaps(e.admin, sharedPath, sel(whoami)); code != 422 || errorCode(raw) != "capability_not_selectable" {
		t.Fatalf("platform gateway with a private capability: %d %s", code, raw)
	}
	if code, raw := e.setCaps(e.admin, sharedPath, sel(foreign+":member")); code != 422 || errorCode(raw) != "min_role_not_allowed" {
		t.Fatalf("platform gateway minRole: %d %s", code, raw)
	}
	if got := e.mustSetCaps(e.admin, sharedPath, sel(foreign)); len(got.Items) != 1 || got.Items[0].MinRole != nil {
		t.Fatalf("platform selection = %+v", got)
	}
	if code, raw := e.setCaps(e.auditor, sharedPath, sel()); code != 403 {
		t.Fatalf("auditor set = %d %s", code, raw)
	}

	// Platform admins may only switch a team gateway off or on.
	adminGw := "/v1/admin/gateways/" + gw.Id.String()
	rev := e.gateway(e.admin, adminGw).Revision
	code, ec = e.admin.call("PATCH", adminGw, map[string]any{"name": "Renamed"}, nil, ifMatch(rev))
	mustCode(t, "admin rename team gateway", code, ec, 403, "team_gateway")
	code, ec = e.admin.call("PATCH", adminGw, map[string]any{"enabled": false}, nil, ifMatch(rev))
	mustCode(t, "admin switch off", code, ec, 200, "")
	if code, raw := e.setCaps(e.admin, adminGw, sel()); code != 403 || errorCode(raw) != "team_gateway" {
		t.Fatalf("admin set team selection = %d %s", code, raw)
	}
	code, ec = e.admin.call("DELETE", adminGw, nil, nil, nil)
	mustCode(t, "admin delete team gateway", code, ec, 403, "team_gateway")
	code, ec = e.admin.call("POST", adminGw+"/grants", map[string]any{"subjectKind": "group", "group": "x"}, nil, nil)
	mustCode(t, "grant a team gateway", code, ec, 403, "team_gateway")

	// Availability is live: the catalog server leaves the catalog, the own
	// server is switched off.
	code, ec = e.outsider.call("POST", "/v1/teams/other/mcp-servers/"+e.other.Id.String()+"/unpublish", nil, nil, nil)
	mustCode(t, "unpublish", code, ec, 200, "")
	e.patchServer(e.owner, e.server, map[string]any{"enabled": false})
	var caps apitypes.GatewayCapabilities
	e.member.get(gwPath+"/capabilities", &caps)
	reasons := map[string]string{}
	for _, it := range caps.Items {
		if it.Available || it.UnavailableReason == nil {
			t.Errorf("still available: %+v", it)
			continue
		}
		reasons[it.ExposedName] = string(*it.UnavailableReason)
	}
	if reasons["other-status__check_outage"] != "not_published" || reasons["status__whoami"] != "server_disabled" || reasons["status__echo"] != "server_disabled" {
		t.Fatalf("reasons = %v", reasons)
	}
	if g := e.gateway(e.owner, gwPath); g.CapabilityCount != 3 || g.UnavailableCount != 3 || g.Enabled {
		t.Fatalf("counts = %+v", g)
	}
	// Selections that aren't served now can be kept (not added).
	if got := e.mustSetCaps(e.owner, gwPath, sel(whoami, foreign)); len(got.Items) != 2 {
		t.Fatalf("kept = %+v", got)
	}

	// Delete, and the audit trail.
	code, ec = e.owner.call("DELETE", gwPath, nil, nil, nil)
	mustCode(t, "delete", code, ec, 200, "")
	var page apitypes.AuditPage
	e.owner.get(toolsTeam+"/audit?action=gateway.", &page)
	actions := map[string]int{}
	for _, it := range page.Items {
		actions[it.Action]++
		if it.TargetType != "gateway" || it.TargetLabel == nil || *it.TargetLabel != "SUPPORT" {
			t.Errorf("audit entry %s target %s %v", it.Action, it.TargetType, it.TargetLabel)
		}
	}
	if actions["gateway.create"] != 1 || actions["gateway.capabilities_set"] != 2 || actions["gateway.update"] != 1 || actions["gateway.delete"] != 1 {
		t.Fatalf("team audit = %v", actions)
	}
}

func myGateways(t *testing.T, s *session) map[string]apitypes.MyGateway {
	t.Helper()
	var list []apitypes.MyGateway
	if code := s.get("/v1/me/gateways", &list); code != 200 {
		t.Fatalf("my gateways = %d", code)
	}
	out := map[string]apitypes.MyGateway{}
	for _, g := range list {
		out[g.Slug] = g
	}
	return out
}

func access(g apitypes.MyGateway) string {
	out := []string{}
	for _, a := range g.Access {
		out = append(out, string(a))
	}
	return strings.Join(out, ",")
}

func TestGatewayGrantsAndMyGateways(t *testing.T) {
	e := newGwEnv(t, nil)
	e.publish(e.outsider, e.other)
	foreign := e.capID(e.outsider, e.other, "tool", "check_outage")
	shared := e.createGateway(e.admin, "/v1/admin/gateways", "shared")
	sharedPath := "/v1/admin/gateways/" + shared.Id.String()
	e.mustSetCaps(e.admin, sharedPath, sel(foreign))

	grant := func(body map[string]any) apitypes.GatewayGrant {
		t.Helper()
		var g apitypes.GatewayGrant
		code, ec := e.admin.call("POST", sharedPath+"/grants", body, &g, nil)
		mustCode(t, fmt.Sprint("grant ", body), code, ec, 201, "")
		return g
	}
	teamGrant := grant(map[string]any{"subjectKind": "team", "team": "tools"})
	userGrant := grant(map[string]any{"subjectKind": "user", "userEmail": "BLAIR@localhost"})
	grant(map[string]any{"subjectKind": "group", "group": "engineering"})
	if teamGrant.Team == nil || teamGrant.Team.Slug != "tools" || userGrant.User == nil || userGrant.User.Email != "blair@localhost" {
		t.Fatalf("grants = %+v %+v", teamGrant, userGrant)
	}
	for _, c := range []struct {
		body map[string]any
		code int
		err  string
	}{
		{map[string]any{"subjectKind": "team", "team": "tools"}, 409, "grant_exists"},
		{map[string]any{"subjectKind": "team", "team": "nope"}, 404, "subject_not_found"},
		{map[string]any{"subjectKind": "user", "userEmail": "nobody@example.com"}, 404, "subject_not_found"},
		{map[string]any{"subjectKind": "team", "group": "x"}, 400, "invalid_grant"},
		{map[string]any{"subjectKind": "group", "group": "a", "team": "tools"}, 400, "invalid_grant"},
	} {
		code, ec := e.admin.call("POST", sharedPath+"/grants", c.body, nil, nil)
		mustCode(t, fmt.Sprint(c.body), code, ec, c.code, c.err)
	}
	code, ec := e.auditor.call("POST", sharedPath+"/grants", map[string]any{"subjectKind": "group", "group": "z"}, nil, nil)
	mustCode(t, "auditor grant", code, ec, 403, "")
	var grants []apitypes.GatewayGrant
	if code := e.auditor.get(sharedPath+"/grants", &grants); code != 200 || len(grants) != 3 {
		t.Fatalf("grants = %d %+v", code, grants)
	}

	// Why each person may use it: their team, themselves, their groups
	// (dev personas: user and alex are in engineering, casey in support).
	mine := myGateways(t, e.member)
	if g := mine["shared"]; access(g) != "team_grant,user_grant" || g.CapabilityCount != 1 || g.OwnerKind != "platform" ||
		g.Url != e.app.URL+"/mcp/shared" {
		t.Fatalf("blair: %+v", mine)
	}
	if g := myGateways(t, e.owner)["shared"]; access(g) != "team_grant,group_grant" {
		t.Fatalf("owner: %+v", g)
	}
	if _, ok := myGateways(t, e.outsider)["shared"]; ok {
		t.Fatal("casey sees an ungranted gateway")
	}
	grant(map[string]any{"subjectKind": "group", "group": "support"})
	if g := myGateways(t, e.outsider)["shared"]; access(g) != "group_grant" {
		t.Fatalf("casey via group: %+v", g)
	}

	// A team gateway is for the team's members; minRole narrows what each sees.
	support := e.createGateway(e.owner, toolsTeam+"/gateways", "support")
	supportPath := toolsTeam + "/gateways/" + support.Id.String()
	whoami, echo := e.capID(e.owner, e.server, "tool", "whoami"), e.capID(e.owner, e.server, "tool", "echo")
	e.mustSetCaps(e.owner, supportPath, sel(whoami+":admin", echo))
	if g := myGateways(t, e.member)["support"]; access(g) != "team_member" || g.CapabilityCount != 1 || g.TeamSlug == nil || *g.TeamSlug != "tools" {
		t.Fatalf("blair support: %+v", g)
	}
	if g := myGateways(t, e.owner)["support"]; g.CapabilityCount != 2 {
		t.Fatalf("owner support: %+v", g)
	}
	var detail apitypes.MyGatewayDetail
	if code := e.member.get("/v1/me/gateways/"+support.Id.String(), &detail); code != 200 ||
		!slices.Equal(exposed(detail.Capabilities), []string{"status__echo"}) {
		t.Fatalf("blair detail = %d %+v", code, detail)
	}
	if code := e.outsider.get("/v1/me/gateways/"+support.Id.String(), nil); code != 404 {
		t.Fatalf("casey detail = %d", code)
	}
	if _, ok := myGateways(t, e.admin)["support"]; ok {
		t.Fatal("platform admin can use a team gateway they're not a member of")
	}

	// Switched off: no one can connect.
	code, ec = e.owner.call("PATCH", supportPath, map[string]any{"enabled": false}, nil, ifMatch(e.gateway(e.owner, supportPath).Revision))
	mustCode(t, "switch off", code, ec, 200, "")
	if _, ok := myGateways(t, e.member)["support"]; ok {
		t.Fatal("a disabled gateway is listed")
	}
	if code := e.member.get("/v1/me/gateways/"+support.Id.String(), nil); code != 404 {
		t.Fatalf("disabled detail = %d", code)
	}

	// Removing grants takes effect at once.
	for _, g := range []apitypes.GatewayGrant{teamGrant, userGrant} {
		code, ec := e.admin.call("DELETE", sharedPath+"/grants/"+g.Id.String(), nil, nil, nil)
		mustCode(t, "remove grant", code, ec, 200, "")
	}
	code, ec = e.admin.call("DELETE", sharedPath+"/grants/"+userGrant.Id.String(), nil, nil, nil)
	mustCode(t, "remove again", code, ec, 404, "grant_not_found")
	if g := myGateways(t, e.member)["shared"]; access(g) != "group_grant" {
		t.Fatalf("blair after removal (still in support): %+v", g)
	}
	if g := myGateways(t, e.owner)["shared"]; access(g) != "group_grant" {
		t.Fatalf("owner after removal: %+v", g)
	}
	if v := e.gateway(e.admin, sharedPath); v.GrantCount != 2 {
		t.Fatalf("grant count = %d", v.GrantCount)
	}

	// Grants are audited as gateway_grant entries with readable labels.
	var page apitypes.AuditPage
	e.admin.get("/v1/admin/audit?targetType=gateway_grant", &page)
	labels := map[string]int{}
	for _, it := range page.Items {
		if it.TargetLabel == nil || it.TeamId != nil {
			t.Errorf("grant entry %+v", it)
			continue
		}
		labels[it.Action+" "+*it.TargetLabel]++
	}
	if labels["gateway.grant_add SHARED · tools"] != 1 || labels["gateway.grant_remove SHARED · blair@localhost"] != 1 ||
		labels["gateway.grant_add SHARED · engineering"] != 1 {
		t.Fatalf("grant audit = %v", labels)
	}
}

// The availability rule in Go (Availability) and SQL (GatewayStats) agree.
func TestGatewayCountsMatchAvailability(t *testing.T) {
	e := newGwEnv(t, nil)
	e.publish(e.outsider, e.other)
	gw := e.createGateway(e.owner, toolsTeam+"/gateways", "support")
	path := toolsTeam + "/gateways/" + gw.Id.String()
	e.mustSetCaps(e.owner, path, sel(e.capID(e.owner, e.server, "tool", "whoami"), e.capID(e.outsider, e.other, "tool", "check_outage"),
		e.capID(e.outsider, e.other, "tool", "search_tickets")))
	check := func(what string, wantUnavailable int64) {
		t.Helper()
		var caps apitypes.GatewayCapabilities
		e.owner.get(path+"/capabilities", &caps)
		n := int64(0)
		for _, it := range caps.Items {
			if !it.Available {
				n++
			}
		}
		if g := e.gateway(e.owner, path); g.UnavailableCount != n || n != wantUnavailable {
			t.Fatalf("%s: stats %d, items %d, want %d", what, g.UnavailableCount, n, wantUnavailable)
		}
	}
	check("all served", 0)
	e.otherFake.SetDescription("search_tickets", "Now different.")
	e.refresh(e.outsider, e.other)
	check("changed", 1)
	e.otherFake.Hide("check_outage")
	e.refresh(e.outsider, e.other)
	check("gone", 2)
	e.patchServer(e.owner, e.server, map[string]any{"enabled": false})
	check("disabled", 3)
	if _, err := e.app.Pool.Exec(context.Background(), `UPDATE mcp_servers SET enabled = true`); err != nil {
		t.Fatal(err)
	}
	check("enabled again", 2)
}

package httpapi_test

import "fmt"

// The authorization matrix's classification of every operation in
// api/openapi.yaml (SPEC.md §4, §9): who may call it on their own team (team
// B) or at all (global routes), and who may call it on another team (team
// A). Platform admins and auditors read any team's metadata; only platform
// admins change platform objects; team owners and admins manage members;
// editors and above read the team's audit log.

// caller is a kind of credential.
type caller int

const (
	cAnon caller = iota
	cMember
	cEditor
	cTeamAdmin
	cOwner
	cPlatformAdmin
	cAuditor
	numCallers
)

var callerNames = [numCallers]string{"anonymous", "member", "editor", "team-admin", "owner", "platform-admin", "auditor"}

func (c caller) String() string { return callerNames[c] }

// callers is a set of callers.
type callers uint32

func of(cs ...caller) callers {
	var s callers
	for _, c := range cs {
		s |= 1 << c
	}
	return s
}

func (s callers) has(c caller) bool { return s&(1<<c) != 0 }

var (
	everyone = callers(1<<numCallers - 1)
	members  = of(cMember, cEditor, cTeamAdmin, cOwner)
	editors  = of(cEditor, cTeamAdmin, cOwner)
	admins   = of(cTeamAdmin, cOwner)
	platform = of(cPlatformAdmin, cAuditor)
	padmin   = of(cPlatformAdmin)
	signedIn = members | platform
)

// scope says how an operation is exercised.
type scope int

const (
	// scopeGlobal: no team in the path; every caller calls it once.
	scopeGlobal scope = iota
	// scopeTeam: the path names a team; team B's callers call it on team B,
	// and every caller on team A.
	scopeTeam
)

// policy is one operation's classification.
type policy struct {
	scope scope
	// own: callers allowed on their own team (or at all, for scopeGlobal).
	own callers
	// foreign: callers allowed on team A.
	foreign callers
	// anyStatus: the status is not checked (sign-in plumbing), only that the
	// call didn't fail on the server.
	anyStatus bool
	build     func(c *mctx) request
}

// request is one call.
type request struct {
	method, path string
	body         any
	headers      map[string]string
	// anon: send without the caller's session (sign-in endpoints would
	// replace it).
	anon bool
	// ownSession: call with a new session of the caller's user (logout
	// would end the shared one).
	ownSession bool
}

func get(p string) request                       { return request{method: "GET", path: p} }
func post(p string, body any) request            { return request{method: "POST", path: p, body: body} }
func patch(p string, body any) request           { return request{method: "PATCH", path: p, body: body} }
func del(p string) request                       { return request{method: "DELETE", path: p} }
func (r request) h(hs map[string]string) request { r.headers = hs; return r }

var rev1 = map[string]string{"If-Match": `"1"`}

var matrixPolicies = map[string]policy{
	// Operations and sign-in.
	"getHealthz":    {own: everyone, build: func(*mctx) request { return get("/healthz") }},
	"getReadyz":     {own: everyone, build: func(*mctx) request { return get("/readyz") }},
	"getAuthConfig": {own: everyone, build: func(*mctx) request { return get("/v1/auth/config") }},
	"startLogin":    {anyStatus: true, build: func(*mctx) request { r := get("/auth/login"); r.anon = true; return r }},
	"completeLogin": {anyStatus: true, build: func(*mctx) request { r := get("/auth/callback?state=x"); r.anon = true; return r }},
	"devLogin": {anyStatus: true, build: func(*mctx) request {
		r := post("/auth/dev", map[string]string{"account": "casey"})
		r.anon = true
		return r
	}},
	"logout":      {own: signedIn, build: func(*mctx) request { r := post("/auth/logout", nil); r.ownSession = true; return r }},
	"getMe":       {own: signedIn, build: func(*mctx) request { return get("/v1/me") }},
	"listMyTeams": {own: signedIn, build: func(*mctx) request { return get("/v1/teams") }},

	// Teams, members, invites and the team audit log.
	"getTeam":     {scope: scopeTeam, own: members, foreign: platform, build: func(c *mctx) request { return get(c.team("")) }},
	"listMembers": {scope: scopeTeam, own: members, foreign: platform, build: func(c *mctx) request { return get(c.team("/members")) }},
	"addMember": {scope: scopeTeam, own: admins, build: func(c *mctx) request {
		return post(c.team("/members"), map[string]any{"email": fmt.Sprintf("added-%d@example.com", c.e.next()), "role": "member"})
	}},
	"updateMember": {scope: scopeTeam, own: admins, build: func(c *mctx) request {
		return patch(c.team("/members/"+c.member()), map[string]any{"role": "editor"}).h(rev1)
	}},
	"removeMember": {scope: scopeTeam, own: admins, build: func(c *mctx) request { return del(c.team("/members/" + c.member())) }},
	"listInvites":  {scope: scopeTeam, own: admins, foreign: platform, build: func(c *mctx) request { return get(c.team("/invites")) }},
	"revokeInvite": {scope: scopeTeam, own: admins, foreign: padmin, build: func(c *mctx) request {
		return del(c.team("/invites/" + c.invite()))
	}},
	"listTeamAudit": {scope: scopeTeam, own: editors, foreign: platform, build: func(c *mctx) request { return get(c.team("/audit")) }},
	"getTeamAuditEntry": {scope: scopeTeam, own: editors, foreign: platform, build: func(c *mctx) request {
		return get(c.team("/audit/" + c.tf.auditID))
	}},

	// Platform administration.
	"adminListUsers": {own: platform, build: func(*mctx) request { return get("/v1/admin/users") }},
	"adminGetUser":   {own: platform, build: func(c *mctx) request { return get("/v1/admin/users/" + c.e.spareUser) }},
	"adminUpdateUser": {own: padmin, build: func(c *mctx) request {
		return patch("/v1/admin/users/"+c.user(), map[string]any{"platformRole": "platform_auditor"}).h(rev1)
	}},
	"adminListTeams": {own: platform, build: func(*mctx) request { return get("/v1/admin/teams") }},
	"adminCreateTeam": {own: padmin, build: func(c *mctx) request {
		n := c.e.next()
		return post("/v1/admin/teams", map[string]any{"slug": fmt.Sprintf("new-%d", n), "name": "New", "ownerEmail": fmt.Sprintf("o-%d@example.com", n)})
	}},
	"adminGetTeam": {own: platform, build: func(c *mctx) request { return get("/v1/admin/teams/" + c.e.b.slug) }},
	"adminUpdateTeam": {own: padmin, build: func(c *mctx) request {
		return patch("/v1/admin/teams/"+c.spareTeam(), map[string]any{"description": "updated by the matrix"}).h(rev1)
	}},
	"adminAssignOwner": {own: padmin, build: func(c *mctx) request {
		return post("/v1/admin/teams/"+c.spareTeam()+"/owners", map[string]any{"email": fmt.Sprintf("owner-%d@example.com", c.e.next())})
	}},
	"adminListAudit":     {own: platform, build: func(*mctx) request { return get("/v1/admin/audit") }},
	"adminGetAuditEntry": {own: platform, build: func(c *mctx) request { return get("/v1/admin/audit/" + c.e.b.auditID) }},

	// MCP servers (team B's policy is self_publish, so its owners and admins
	// approve changed capabilities). Allowed writes get fresh servers.
	"listTeamMcpServers": {scope: scopeTeam, own: members, foreign: platform, build: func(c *mctx) request { return get(c.team("/mcp-servers")) }},
	"createTeamMcpServer": {scope: scopeTeam, own: editors, build: func(c *mctx) request {
		return post(c.team("/mcp-servers"), map[string]any{"slug": fmt.Sprintf("new-srv-%d", c.e.next()), "name": "New", "url": c.e.fakeURL})
	}},
	"getTeamMcpServer": {scope: scopeTeam, own: members, foreign: platform, build: func(c *mctx) request {
		return get(c.team("/mcp-servers/" + c.tf.server))
	}},
	"updateTeamMcpServer": {scope: scopeTeam, own: editors, build: func(c *mctx) request {
		return patch(c.team("/mcp-servers/"+c.server("private")), map[string]any{"description": "updated by the matrix"}).h(rev1)
	}},
	"deleteTeamMcpServer": {scope: scopeTeam, own: admins, build: func(c *mctx) request { return del(c.team("/mcp-servers/" + c.server("private"))) }},
	"testTeamMcpServer": {scope: scopeTeam, own: editors, build: func(c *mctx) request {
		return post(c.team("/mcp-servers/"+c.server("private")+"/test"), nil)
	}},
	"refreshTeamMcpServer": {scope: scopeTeam, own: editors, build: func(c *mctx) request {
		return post(c.team("/mcp-servers/"+c.server("private")+"/refresh"), nil)
	}},
	"listTeamMcpCapabilities": {scope: scopeTeam, own: members, foreign: platform, build: func(c *mctx) request {
		return get(c.team("/mcp-servers/" + c.tf.server + "/capabilities"))
	}},
	"approveTeamMcpCapability": {scope: scopeTeam, own: admins, build: func(c *mctx) request {
		srv, capID := c.changedCapability()
		return request{method: "PUT", path: c.team("/mcp-servers/" + srv + "/capabilities/" + capID + "/approval")}
	}},
	"listTeamMcpHealthChecks": {scope: scopeTeam, own: members, foreign: platform, build: func(c *mctx) request {
		return get(c.team("/mcp-servers/" + c.tf.server + "/health-checks"))
	}},
	"publishTeamMcpServer": {scope: scopeTeam, own: admins, build: func(c *mctx) request {
		return post(c.team("/mcp-servers/"+c.server("private")+"/publish"), map[string]any{"note": "please"})
	}},
	"unpublishTeamMcpServer": {scope: scopeTeam, own: admins, build: func(c *mctx) request {
		return post(c.team("/mcp-servers/"+c.server("published")+"/unpublish"), nil)
	}},
	"getTeamPublishing": {scope: scopeTeam, own: members, foreign: platform, build: func(c *mctx) request { return get(c.team("/publishing")) }},

	// The catalog: anyone signed in.
	"listCatalogServers": {own: signedIn, build: func(*mctx) request { return get("/v1/catalog/servers") }},
	"getCatalogServer":   {own: signedIn, build: func(c *mctx) request { return get("/v1/catalog/servers/" + c.e.b.server) }},

	// Registry administration.
	"adminListMcpServers":      {own: platform, build: func(*mctx) request { return get("/v1/admin/mcp-servers?changes=true") }},
	"adminGetMcpServer":        {own: platform, build: func(c *mctx) request { return get("/v1/admin/mcp-servers/" + c.e.b.server) }},
	"adminListMcpCapabilities": {own: platform, build: func(c *mctx) request { return get("/v1/admin/mcp-servers/" + c.e.b.server + "/capabilities") }},
	"adminListMcpHealthChecks": {own: platform, build: func(c *mctx) request { return get("/v1/admin/mcp-servers/" + c.e.b.server + "/health-checks") }},
	"adminReviewMcpServer": {own: padmin, build: func(c *mctx) request {
		return post("/v1/admin/mcp-servers/"+c.server("pending")+"/review", map[string]any{"decision": "approve", "note": "ok"})
	}},
	"adminApproveMcpCapability": {own: padmin, build: func(c *mctx) request {
		srv, capID := c.changedCapability()
		return request{method: "PUT", path: "/v1/admin/mcp-servers/" + srv + "/capabilities/" + capID + "/approval"}
	}},
	"adminGetPublishingSettings": {own: platform, build: func(*mctx) request { return get("/v1/admin/settings/publishing") }},
	"adminUpdatePublishingSettings": {own: padmin, build: func(c *mctx) request {
		rev := c.e.exec(`SELECT revision::text FROM platform_settings`)
		return request{method: "PUT", path: "/v1/admin/settings/publishing", body: map[string]any{"defaultPolicy": "approval_required"},
			headers: map[string]string{"If-Match": `"` + rev + `"`}}
	}},
	"adminGetTeamPublishing": {own: platform, build: func(c *mctx) request { return get("/v1/admin/teams/" + c.e.b.slug + "/publishing") }},
	"adminUpdateTeamPublishing": {own: padmin, build: func(c *mctx) request {
		return request{method: "PUT", path: "/v1/admin/teams/" + c.spareTeam() + "/publishing", body: map[string]any{"override": "self_publish"},
			headers: rev1}
	}},

	// Team gateways (owners and admins write; members and platform staff
	// read). Allowed writes get fresh gateways.
	"listTeamGateways": {scope: scopeTeam, own: members, foreign: platform, build: func(c *mctx) request { return get(c.team("/gateways")) }},
	"createTeamGateway": {scope: scopeTeam, own: admins, build: func(c *mctx) request {
		return post(c.team("/gateways"), map[string]any{"slug": fmt.Sprintf("new-gw-%d", c.e.next()), "name": "New"})
	}},
	"getTeamGateway": {scope: scopeTeam, own: members, foreign: platform, build: func(c *mctx) request {
		return get(c.team("/gateways/" + c.tf.gateway))
	}},
	"updateTeamGateway": {scope: scopeTeam, own: admins, build: func(c *mctx) request {
		return patch(c.team("/gateways/"+c.gateway()), map[string]any{"description": "updated by the matrix"}).h(rev1)
	}},
	"deleteTeamGateway": {scope: scopeTeam, own: admins, build: func(c *mctx) request { return del(c.team("/gateways/" + c.gateway())) }},
	"listTeamGatewayCapabilities": {scope: scopeTeam, own: members, foreign: platform, build: func(c *mctx) request {
		return get(c.team("/gateways/" + c.tf.gateway + "/capabilities"))
	}},
	"setTeamGatewayCapabilities": {scope: scopeTeam, own: admins, build: func(c *mctx) request {
		return request{method: "PUT", path: c.team("/gateways/" + c.gateway() + "/capabilities"), body: map[string]any{"items": []any{}}, headers: rev1}
	}},
	"listTeamGatewayOptions": {scope: scopeTeam, own: admins, build: func(c *mctx) request { return get(c.team("/gateway-options")) }},
	"listTeamCalls":          {scope: scopeTeam, own: admins, foreign: platform, build: func(c *mctx) request { return get(c.team("/calls")) }},

	// Gateway administration.
	"adminListGateways": {own: platform, build: func(*mctx) request { return get("/v1/admin/gateways?ownerKind=platform") }},
	"adminCreateGateway": {own: padmin, build: func(c *mctx) request {
		return post("/v1/admin/gateways", map[string]any{"slug": fmt.Sprintf("platform-gw-%d", c.e.next()), "name": "Platform"})
	}},
	"adminGetGateway": {own: platform, build: func(c *mctx) request { return get("/v1/admin/gateways/" + c.e.platformGateway) }},
	"adminUpdateGateway": {own: padmin, build: func(c *mctx) request {
		return patch("/v1/admin/gateways/"+c.platformGateway(), map[string]any{"description": "updated by the matrix"}).h(rev1)
	}},
	"adminDeleteGateway": {own: padmin, build: func(c *mctx) request { return del("/v1/admin/gateways/" + c.platformGateway()) }},
	"adminListGatewayCapabilities": {own: platform, build: func(c *mctx) request {
		return get("/v1/admin/gateways/" + c.e.platformGateway + "/capabilities")
	}},
	"adminSetGatewayCapabilities": {own: padmin, build: func(c *mctx) request {
		return request{method: "PUT", path: "/v1/admin/gateways/" + c.platformGateway() + "/capabilities", body: map[string]any{"items": []any{}}, headers: rev1}
	}},
	"adminListGatewayOptions": {own: platform, build: func(*mctx) request { return get("/v1/admin/gateway-options") }},
	"adminListGatewayGrants": {own: platform, build: func(c *mctx) request {
		return get("/v1/admin/gateways/" + c.e.platformGateway + "/grants")
	}},
	"adminAddGatewayGrant": {own: padmin, build: func(c *mctx) request {
		return post("/v1/admin/gateways/"+c.platformGateway()+"/grants", map[string]any{"subjectKind": "group", "group": "matrix-group"})
	}},
	"adminRemoveGatewayGrant": {own: padmin, build: func(c *mctx) request {
		gw, grant := c.grant()
		return del("/v1/admin/gateways/" + gw + "/grants/" + grant)
	}},
	"adminListCalls": {own: platform, build: func(*mctx) request { return get("/v1/admin/calls") }},

	// The caller's gateways (the fixture platform gateway is granted to
	// every matrix caller) and API keys.
	"listMyGateways": {own: signedIn, build: func(*mctx) request { return get("/v1/me/gateways") }},
	"getMyGateway":   {own: signedIn, build: func(c *mctx) request { return get("/v1/me/gateways/" + c.e.platformGateway) }},
	"listMyApiKeys":  {own: signedIn, build: func(*mctx) request { return get("/v1/me/api-keys") }},
	"createMyApiKey": {own: signedIn, build: func(*mctx) request { return post("/v1/me/api-keys", map[string]any{"name": "matrix key"}) }},
	"revokeMyApiKey": {own: signedIn, build: func(c *mctx) request { return del("/v1/me/api-keys/" + c.apiKey()) }},
}

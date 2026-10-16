package gateways

import (
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/ncecere/open-mcp-gateway/internal/store/dbgen"
)

func TestAvailability(t *testing.T) {
	team, other := uuid.New(), uuid.New()
	teamGw := dbgen.Gateway{OwnerKind: OwnerTeam, TeamID: uuid.NullUUID{UUID: team, Valid: true}}
	platformGw := dbgen.Gateway{OwnerKind: OwnerPlatform}
	fp, old := "a", "b"
	now := time.Now()
	cases := []struct {
		name     string
		gw       dbgen.Gateway
		cap      dbgen.McpCapability
		server   dbgen.McpServer
		ok       bool
		because  string
		archived Archived
	}{
		{"own private server", teamGw, dbgen.McpCapability{Kind: "tool", Fingerprint: fp}, dbgen.McpServer{TeamID: team, Enabled: true, PublishState: "private"}, true, "", Archived{}},
		{"own changed tool", teamGw, dbgen.McpCapability{Kind: "tool", Fingerprint: fp, ApprovedFingerprint: &old}, dbgen.McpServer{TeamID: team, Enabled: true, PublishState: "published"}, true, "", Archived{}},
		{"own disabled", teamGw, dbgen.McpCapability{Kind: "tool", Fingerprint: fp}, dbgen.McpServer{TeamID: team, PublishState: "private"}, false, ReasonServerDisabled, Archived{}},
		{"own gone", teamGw, dbgen.McpCapability{Kind: "prompt", Fingerprint: fp, GoneAt: &now}, dbgen.McpServer{TeamID: team, Enabled: true}, false, ReasonGone, Archived{}},
		{"resource", teamGw, dbgen.McpCapability{Kind: "resource", Fingerprint: fp}, dbgen.McpServer{TeamID: team, Enabled: true}, false, ReasonGone, Archived{}},
		{"catalog approved", teamGw, dbgen.McpCapability{Kind: "tool", Fingerprint: fp, ApprovedFingerprint: &fp}, dbgen.McpServer{TeamID: other, Enabled: true, PublishState: "published"}, true, "", Archived{}},
		{"catalog private", teamGw, dbgen.McpCapability{Kind: "tool", Fingerprint: fp, ApprovedFingerprint: &fp}, dbgen.McpServer{TeamID: other, Enabled: true, PublishState: "private"}, false, ReasonNotPublished, Archived{}},
		{"catalog unapproved", platformGw, dbgen.McpCapability{Kind: "tool", Fingerprint: fp}, dbgen.McpServer{TeamID: other, Enabled: true, PublishState: "published"}, false, ReasonNotApproved, Archived{}},
		{"catalog changed", platformGw, dbgen.McpCapability{Kind: "tool", Fingerprint: fp, ApprovedFingerprint: &old}, dbgen.McpServer{TeamID: team, Enabled: true, PublishState: "published"}, false, ReasonChanged, Archived{}},
		{"platform needs publishing", platformGw, dbgen.McpCapability{Kind: "tool", Fingerprint: fp, ApprovedFingerprint: &fp}, dbgen.McpServer{TeamID: team, Enabled: true, PublishState: "pending"}, false, ReasonNotPublished, Archived{}},
		{"catalog server's team archived", platformGw, dbgen.McpCapability{Kind: "tool", Fingerprint: fp, ApprovedFingerprint: &fp}, dbgen.McpServer{TeamID: other, Enabled: true, PublishState: "published"}, false, ReasonTeamArchived, Archived{ServerTeam: true}},
		{"own team archived", teamGw, dbgen.McpCapability{Kind: "tool", Fingerprint: fp}, dbgen.McpServer{TeamID: team, Enabled: true, PublishState: "private"}, false, ReasonTeamArchived, Archived{GatewayTeam: true, ServerTeam: true}},
		{"gateway's team archived, catalog server", teamGw, dbgen.McpCapability{Kind: "tool", Fingerprint: fp, ApprovedFingerprint: &fp}, dbgen.McpServer{TeamID: other, Enabled: true, PublishState: "published"}, false, ReasonTeamArchived, Archived{GatewayTeam: true}},
		{"archived and disabled", platformGw, dbgen.McpCapability{Kind: "tool", Fingerprint: fp, ApprovedFingerprint: &fp}, dbgen.McpServer{TeamID: other, PublishState: "published"}, false, ReasonTeamArchived, Archived{ServerTeam: true}},
		{"gone wins over archived", platformGw, dbgen.McpCapability{Kind: "tool", Fingerprint: fp, GoneAt: &now}, dbgen.McpServer{TeamID: other, Enabled: true}, false, ReasonGone, Archived{ServerTeam: true}},
	}
	for _, c := range cases {
		ok, why := Availability(c.gw, c.cap, c.server, c.archived)
		if ok != c.ok || why != c.because {
			t.Errorf("%s: %v %q, want %v %q", c.name, ok, why, c.ok, c.because)
		}
	}
}

func TestEvaluateMinRole(t *testing.T) {
	team := dbgen.Gateway{OwnerKind: OwnerTeam}
	admin := "admin"
	items := []Selected{{Available: true, MinRole: &admin}, {Available: true}, {Available: false, Reason: ReasonGone}}
	got := Evaluate(team, items, Caller{Role: "editor", Access: []string{AccessTeamMember}})
	if got[0].Allowed || got[0].Denied != DeniedRole || !got[1].Allowed || got[2].Allowed || got[2].Denied != ReasonGone {
		t.Fatalf("editor = %+v", got)
	}
	if got := Evaluate(team, items, Caller{Role: "owner"}); !got[0].Allowed {
		t.Fatal("owner refused")
	}
	// Platform gateways have no minimum role.
	if got := Evaluate(dbgen.Gateway{OwnerKind: OwnerPlatform}, items, Caller{Access: []string{AccessUserGrant}}); !got[0].Allowed {
		t.Fatal("platform gateway applied a role")
	}
}

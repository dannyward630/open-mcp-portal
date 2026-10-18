package registry

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/ncecere/open-mcp-portal/internal/authz"
	"github.com/ncecere/open-mcp-portal/internal/identity"
	"github.com/ncecere/open-mcp-portal/internal/store/dbgen"
	"github.com/ncecere/open-mcp-portal/internal/testutil"
)

// Only the manual Test of a server with Send identity on, by a person, sends
// an identity token: scheduled checks, refresh, a Test with Send identity
// off and a Test by an automation token (no person) send none.
func TestIdentityTokenOnlyOnManualTest(t *testing.T) {
	s, exec := testService(t)
	s.SetIdentity(identity.New(s.pool, s.box, "https://portal.example.com", testutil.Logger()))
	ctx := context.Background()
	team := exec(`INSERT INTO teams (slug, name) VALUES ('t', 'T') RETURNING id::text`)
	user := uuid.MustParse(exec(`INSERT INTO users (oidc_issuer, oidc_subject, email, display_name) VALUES ('i', 's', 'ed@example.com', 'Ed') RETURNING id::text`))
	exec(`INSERT INTO team_members (team_id, user_id, role) VALUES ($1, $2, 'editor') RETURNING user_id::text`, team, user)

	f := testutil.NewFakeMCP(t)
	server := func(slug string, sendIdentity bool) dbgen.McpServer {
		id := uuid.MustParse(exec(`INSERT INTO mcp_servers (team_id, slug, name, url, send_identity) VALUES ($1, $2, $2, $3, $4) RETURNING id::text`,
			team, slug, f.URL(), sendIdentity))
		sv, err := s.q.GetMCPServer(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		return sv
	}
	on, off := server("on", true), server("off", false)
	person := authz.Actor{UserID: user}
	sent := func(since int) []string {
		var out []string
		for _, a := range f.RequestAuths()[since:] {
			out = append(out, a.Authorization+a.Identity)
		}
		return out
	}
	mark := func() int { return len(f.RequestAuths()) }

	// Scheduled health checks and refresh: nothing, whatever the switch says.
	n := mark()
	if sum, err := s.RunHealthChecks(ctx, time.Nanosecond); err != nil || sum.Healthy != 2 {
		t.Fatalf("health pass = %+v, %v", sum, err)
	}
	if _, _, err := s.refresh(ctx, person, on); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(sent(n), ""); got != "" {
		t.Fatalf("a scheduled check or a refresh sent credentials: %q", got)
	}

	// A person's manual Test: the token, for the right person.
	n = mark()
	if res := s.testAs(ctx, person, on); !res.OK {
		t.Fatalf("test = %+v", res.Err)
	}
	got := sent(n)
	if len(got) == 0 || !strings.HasPrefix(got[0], "Bearer ey") {
		t.Fatalf("manual test sent %q", got)
	}

	// Send identity off, and an automation token: nothing.
	for name, c := range map[string]struct {
		sv dbgen.McpServer
		a  authz.Actor
	}{
		"switch off":       {off, person},
		"automation token": {on, authz.Actor{Kind: authz.KindAutomationToken, TokenID: uuid.New(), PlatformRole: "platform_admin"}},
	} {
		n = mark()
		if res := s.testAs(ctx, c.a, c.sv); !res.OK {
			t.Fatalf("%s: %+v", name, res.Err)
		}
		if got := strings.Join(sent(n), ""); got != "" {
			t.Fatalf("%s: sent credentials %q", name, got)
		}
	}

	// No signer set (an installation that never wired one): no token either.
	s.SetIdentity(nil)
	n = mark()
	if res := s.testAs(ctx, person, on); !res.OK || strings.Join(sent(n), "") != "" {
		t.Fatalf("without a signer: %+v %q", res, sent(n))
	}
}

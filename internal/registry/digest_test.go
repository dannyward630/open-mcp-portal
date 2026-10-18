package registry

import (
	"testing"

	"github.com/google/uuid"

	"github.com/ncecere/open-mcp-portal/internal/store/dbgen"
)

func TestReviewDigest(t *testing.T) {
	provider := uuid.New()
	base := dbgen.McpServer{URL: "https://a.example/mcp", AuthMode: AuthNone}
	caps := []CapabilityRef{
		{"tool", "a", "f1"}, {"tool", "b", "f2"}, {"resource", "status://x", "f3"}, {"prompt", "p", "f4"},
	}
	want := ReviewDigest(base, caps)
	if len(want) != 64 {
		t.Fatalf("digest = %q", want)
	}
	if got := ReviewDigest(base, []CapabilityRef{caps[3], caps[1], caps[2], caps[0]}); got != want {
		t.Error("the order of capabilities changes the digest")
	}
	if got := ReviewDigest(base, caps); got != want {
		t.Error("the digest is not stable")
	}
	withRequirement := func(scopes ...string) dbgen.McpServer {
		sv := base
		sv.AuthProviderID = uuid.NullUUID{UUID: provider, Valid: true}
		sv.AuthScopes = scopes
		return sv
	}
	header := "X-API-Key"
	oauth := base
	oauth.AuthMode = AuthOAuth
	for name, got := range map[string]string{
		"a changed fingerprint":   ReviewDigest(base, []CapabilityRef{{"tool", "a", "f1x"}, caps[1], caps[2], caps[3]}),
		"a new capability":        ReviewDigest(base, append([]CapabilityRef{{"tool", "c", "f5"}}, caps...)),
		"a dropped capability":    ReviewDigest(base, caps[:3]),
		"a renamed capability":    ReviewDigest(base, []CapabilityRef{{"tool", "a2", "f1"}, caps[1], caps[2], caps[3]}),
		"another kind":            ReviewDigest(base, []CapabilityRef{{"prompt", "a", "f1"}, caps[1], caps[2], caps[3]}),
		"another URL":             ReviewDigest(dbgen.McpServer{URL: "https://a.example/mcp2", AuthMode: AuthNone}, caps),
		"another origin":          ReviewDigest(dbgen.McpServer{URL: "https://b.example/mcp", AuthMode: AuthNone}, caps),
		"another auth mode":       ReviewDigest(oauth, caps),
		"a static header":         ReviewDigest(dbgen.McpServer{URL: base.URL, AuthHeaderName: &header}, caps),
		"a requirement":           ReviewDigest(withRequirement(), caps),
		"requirement scopes":      ReviewDigest(withRequirement("repo"), caps),
		"other requirement scope": ReviewDigest(withRequirement("repo", "admin:org"), caps),
	} {
		if got == want {
			t.Errorf("%s doesn't change the digest", name)
		}
	}
	if ReviewDigest(withRequirement("a", "b"), caps) != ReviewDigest(withRequirement("b", "a"), caps) {
		t.Error("the order of scopes changes the digest")
	}
	// Fields can't run into each other.
	if ReviewDigest(base, []CapabilityRef{{"tool", "ab", "c"}}) == ReviewDigest(base, []CapabilityRef{{"tool", "a", "bc"}}) {
		t.Error("name and fingerprint collide")
	}
}

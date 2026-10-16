package registry

import (
	"testing"

	"github.com/ncecere/open-mcp-gateway/internal/apperr"
	"github.com/ncecere/open-mcp-gateway/internal/authz"
	"github.com/ncecere/open-mcp-gateway/internal/store/dbgen"
)

func code(err error) string {
	if e, ok := apperr.As(err); ok {
		return e.Code
	}
	if err != nil {
		return err.Error()
	}
	return ""
}

func TestPublishTransition(t *testing.T) {
	const ar, sp = authz.PolicyApprovalRequired, authz.PolicySelfPublish
	for _, c := range []struct {
		state, policy, next string
		approve             bool
		err                 string
	}{
		{StatePrivate, ar, StatePending, false, ""},
		{StateRejected, ar, StatePending, false, ""},
		{StatePending, ar, "", false, "already_pending"},
		{StatePublished, ar, "", false, "already_published"},
		{StatePrivate, sp, StatePublished, true, ""},
		{StateRejected, sp, StatePublished, true, ""},
		{StatePending, sp, StatePublished, true, ""}, // requested before the team could self-publish
		{StatePublished, sp, "", false, "already_published"},
		{"bogus", ar, "", false, "invalid_state"},
	} {
		next, approve, err := PublishTransition(c.state, c.policy)
		if next != c.next || approve != c.approve || code(err) != c.err {
			t.Errorf("publish %s under %s = %q %v %q, want %q %v %q", c.state, c.policy, next, approve, code(err), c.next, c.approve, c.err)
		}
	}
}

func TestReviewTransition(t *testing.T) {
	for _, c := range []struct {
		state, decision, next string
		approve               bool
		err                   string
	}{
		{StatePending, DecisionApprove, StatePublished, true, ""},
		{StatePending, DecisionReject, StateRejected, false, ""},
		{StatePublished, DecisionReject, StateRejected, false, ""},
		{StatePublished, DecisionApprove, "", false, "invalid_state"},
		{StatePrivate, DecisionApprove, "", false, "invalid_state"},
		{StatePrivate, DecisionReject, "", false, "invalid_state"},
		{StateRejected, DecisionReject, "", false, "invalid_state"},
		{StateRejected, DecisionApprove, "", false, "invalid_state"},
		{StatePending, "maybe", "", false, "invalid_decision"},
	} {
		next, approve, err := ReviewTransition(c.state, c.decision)
		if next != c.next || approve != c.approve || code(err) != c.err {
			t.Errorf("%s %s = %q %v %q, want %q %v %q", c.decision, c.state, next, approve, code(err), c.next, c.approve, c.err)
		}
	}
}

func TestUnpublishTransition(t *testing.T) {
	for state, want := range map[string]string{StatePending: "", StatePublished: "", StateRejected: "", StatePrivate: "not_published"} {
		next, err := UnpublishTransition(state)
		if code(err) != want || (want == "" && next != StatePrivate) {
			t.Errorf("unpublish %s = %q %q", state, next, code(err))
		}
	}
}

func TestApprovedAndChanged(t *testing.T) {
	a, b := "aa", "bb"
	now := dbgen.McpCapability{}.FirstSeenAt
	for _, c := range []struct {
		name              string
		cap               dbgen.McpCapability
		published         bool
		approved, changed bool
	}{
		{"never approved", dbgen.McpCapability{Fingerprint: a}, true, false, false},
		{"approved", dbgen.McpCapability{Fingerprint: a, ApprovedFingerprint: &a}, true, true, false},
		{"changed on a published server", dbgen.McpCapability{Fingerprint: b, ApprovedFingerprint: &a}, true, false, true},
		{"changed on a private server", dbgen.McpCapability{Fingerprint: b, ApprovedFingerprint: &a}, false, false, false},
		{"gone", dbgen.McpCapability{Fingerprint: a, ApprovedFingerprint: &a, GoneAt: &now}, true, false, false},
	} {
		if got := IsApproved(c.cap); got != c.approved {
			t.Errorf("%s: approved = %v", c.name, got)
		}
		if got := IsChanged(c.cap, c.published); got != c.changed {
			t.Errorf("%s: changed = %v", c.name, got)
		}
	}
}

func TestValueHint(t *testing.T) {
	for in, want := range map[string]string{"": "", "short": "", "1234567": "", "12345678": "5678", "Bearer abcdéfgh": "éfgh"} {
		if got := valueHint(in); got != want {
			t.Errorf("valueHint(%q) = %q, want %q", in, got, want)
		}
	}
}

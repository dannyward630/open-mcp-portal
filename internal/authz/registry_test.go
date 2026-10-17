package authz

import (
	"testing"

	"github.com/ncecere/open-mcp-portal/internal/apperr"
)

func TestRegistryRoles(t *testing.T) {
	for _, c := range []struct {
		role           string
		edit, manage   bool
		approveSelf    bool
		approveDefault bool
	}{
		{"", false, false, false, false},
		{RoleMember, false, false, false, false},
		{RoleEditor, true, false, false, false},
		{RoleAdmin, true, true, true, false},
		{RoleOwner, true, true, true, false},
	} {
		if got := CheckServerEdit(c.role) == nil; got != c.edit {
			t.Errorf("%q edit = %v", c.role, got)
		}
		if got := CheckServerManage(c.role) == nil; got != c.manage {
			t.Errorf("%q manage = %v", c.role, got)
		}
		if got := CheckCapabilityApproval(c.role, PolicySelfPublish) == nil; got != c.approveSelf {
			t.Errorf("%q approve (self_publish) = %v", c.role, got)
		}
		err := CheckCapabilityApproval(c.role, PolicyApprovalRequired)
		if got := err == nil; got != c.approveDefault {
			t.Errorf("%q approve (approval_required) = %v", c.role, got)
		}
		if c.manage {
			if e, _ := apperr.As(err); e == nil || e.Status != 403 || e.Code != "approval_required" {
				t.Errorf("%q approve under approval_required = %v, want 403 approval_required", c.role, err)
			}
		} else if e, _ := apperr.As(err); e == nil || e.Code != "forbidden" {
			t.Errorf("%q approve = %v, want forbidden", c.role, err)
		}
	}
}

func TestEffectivePolicy(t *testing.T) {
	self, bogus := PolicySelfPublish, "anything"
	if EffectivePolicy(nil, PolicyApprovalRequired) != PolicyApprovalRequired ||
		EffectivePolicy(&self, PolicyApprovalRequired) != PolicySelfPublish ||
		EffectivePolicy(&bogus, PolicyApprovalRequired) != PolicyApprovalRequired {
		t.Fatal("effective policy is the override, else the default")
	}
}

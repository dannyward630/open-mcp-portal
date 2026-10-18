package httpapi_test

import (
	"strings"
	"testing"

	"github.com/ncecere/open-mcp-portal/internal/httpapi/apitypes"
)

// F-2: approvals name what the approver saw. These build the request bodies
// of an approver who has just read the server or capability, as the web
// pages do.

// reviewDigestOf is a server's reviewDigest as an admin reads it now.
func reviewDigestOf(t testing.TB, admin *session, serverID string) string {
	t.Helper()
	var sv apitypes.McpServer
	if code := admin.get("/v1/admin/mcp-servers/"+serverID, &sv); code != 200 || sv.ReviewDigest == nil || *sv.ReviewDigest == "" {
		t.Fatalf("read server %s = %d %+v", serverID, code, sv.ReviewDigest)
	}
	return *sv.ReviewDigest
}

// approveBody is an approve decision for a server as admin reads it now; a
// note may follow.
func approveBody(t testing.TB, admin *session, serverID string, note ...string) map[string]any {
	t.Helper()
	body := map[string]any{"decision": "approve", "expectedDigest": reviewDigestOf(t, admin, serverID)}
	if len(note) > 0 {
		body["note"] = note[0]
	}
	return body
}

// approvalBody is the body that approves the capability named in an approval
// path (".../mcp-servers/{server}/capabilities/{capability}/approval") as it
// is now.
func approvalBody(t testing.TB, admin *session, path string) map[string]any {
	t.Helper()
	parts := strings.Split(path, "/")
	n := len(parts)
	server, capID := parts[n-4], parts[n-2]
	var caps []apitypes.McpCapability
	if code := admin.get("/v1/admin/mcp-servers/"+server+"/capabilities", &caps); code != 200 {
		t.Fatalf("read capabilities of %s = %d", server, code)
	}
	for _, c := range caps {
		if c.Id.String() == capID {
			return map[string]any{"expectedFingerprint": c.Fingerprint}
		}
	}
	return map[string]any{"expectedFingerprint": "unknown capability"}
}

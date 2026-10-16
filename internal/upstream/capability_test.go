package upstream

import (
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestCanonicalAndFingerprint(t *testing.T) {
	a, err := Canonical([]byte(`{"b": 1, "a": {"y": [1, 2.50, "<x>"], "x": null}}`))
	if err != nil {
		t.Fatal(err)
	}
	if string(a) != `{"a":{"x":null,"y":[1,2.50,"<x>"]},"b":1}` {
		t.Fatalf("canonical = %s", a)
	}
	b, _ := Canonical([]byte("{\n  \"a\": {\"x\": null, \"y\": [1, 2.50, \"<x>\"]},\n  \"b\": 1\n}"))
	if Fingerprint(a) != Fingerprint(b) {
		t.Fatal("same value, different fingerprints")
	}
	if fp := Fingerprint(a); len(fp) != 64 || strings.Trim(fp, "0123456789abcdef") != "" {
		t.Fatalf("fingerprint %q is not SHA-256 hex", fp)
	}
	// Big integers keep their digits.
	c, _ := Canonical([]byte(`{"n": 12345678901234567890}`))
	if string(c) != `{"n":12345678901234567890}` {
		t.Fatalf("number changed: %s", c)
	}
	d, _ := Canonical([]byte(`{"b": 2, "a": {"y": [1, 2.50, "<x>"], "x": null}}`))
	if Fingerprint(a) == Fingerprint(d) {
		t.Fatal("different values, same fingerprint")
	}
	if _, err := Canonical([]byte(`{`)); err == nil {
		t.Fatal("invalid JSON accepted")
	}
}

// A capability's definition is the listed object without _meta, so a
// server's volatile metadata doesn't look like a change, and a changed
// description does.
func TestCapabilityDefinition(t *testing.T) {
	tool := &mcp.Tool{Name: "t", Description: "d", InputSchema: map[string]any{"type": "object"}, Meta: mcp.Meta{"requestedAt": "now"}}
	cp, err := newCapability(KindTool, tool.Name, "", tool.Description, tool)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(cp.Definition), "_meta") || !strings.Contains(string(cp.Definition), `"description":"d"`) {
		t.Fatalf("definition = %s", cp.Definition)
	}
	tool.Meta = mcp.Meta{"requestedAt": "later"}
	same, _ := newCapability(KindTool, tool.Name, "", tool.Description, tool)
	if same.Fingerprint != cp.Fingerprint {
		t.Fatal("_meta changed the fingerprint")
	}
	tool.Description = "d2"
	changed, _ := newCapability(KindTool, tool.Name, "", tool.Description, tool)
	if changed.Fingerprint == cp.Fingerprint {
		t.Fatal("a new description kept the fingerprint")
	}
	huge := &mcp.Tool{Name: "h", InputSchema: map[string]any{"type": "object", "description": strings.Repeat("x", MaxDefinition)}}
	if _, err := newCapability(KindTool, huge.Name, "", "", huge); err == nil {
		t.Fatal("oversized definition accepted")
	}
}

func TestValidNameAndClip(t *testing.T) {
	if validName("") || validName(strings.Repeat("a", MaxName+1)) || validName("a\nb") || validName(string([]byte{0xff})) {
		t.Fatal("invalid name accepted")
	}
	if !validName("status://services/{service}") {
		t.Fatal("URI template refused")
	}
	if got := clip("héllo", 2); got != "hé" {
		t.Fatalf("clip = %q", got)
	}
}

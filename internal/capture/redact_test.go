package capture

import (
	"encoding/json"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestBuiltInRules(t *testing.T) {
	cases := map[string]string{
		"bearer":              "Authorization was Bearer abc.DEF-123_xyz~ then",
		"jwt":                 "token eyJhbGciOiJFUzI1NiJ9.eyJzdWIiOiIxMjM0In0.c2lnbmF0dXJl here",
		"gateway_credentials": "key omcp_" + strings.Repeat("A", 43) + " and omcp_sa_" + strings.Repeat("b", 43) + " omat_" + strings.Repeat("c", 43),
		"github":              "ghp_" + strings.Repeat("a", 36) + " github_pat_" + strings.Repeat("B", 30),
		"gitlab":              "glpat-" + strings.Repeat("x", 20),
		"slack":               "xoxb-1234567890-abcdefghij",
		"google":              "ya29.a0AfH6SMBx and AIza" + strings.Repeat("Z", 35),
		"aws_access_key":      "AKIAABCDEFGHIJKLMNOP",
		"private_key_pem":     "-----BEGIN RSA PRIVATE KEY-----\nMIIBOg\n-----END RSA PRIVATE KEY-----",
		"url_userinfo":        "postgres://admin:hunter2@db.example.com/x",
	}
	secrets := map[string][]string{
		"bearer":              {"abc.DEF-123_xyz~"},
		"jwt":                 {"eyJhbGciOiJFUzI1NiJ9"},
		"gateway_credentials": {"AAAAAAAAAAAAAAAA", "bbbbbbbbbbbbbbbb", "cccccccccccccccc"},
		"github":              {"aaaaaaaaaaaaaaaaaaaa", "BBBBBBBBBBBBBBBBBBBB"},
		"gitlab":              {"glpat-xxxx"},
		"slack":               {"abcdefghij"},
		"google":              {"a0AfH6SMBx", "ZZZZZZZZZZZZ"},
		"aws_access_key":      {"AKIAABCDEFGHIJKLMNOP"},
		"private_key_pem":     {"MIIBOg"},
		"url_userinfo":        {"hunter2", "admin:"},
	}
	for rule, sample := range cases {
		p := NewRedactor(nil).Text(sample)
		if p.Rules[rule] == 0 {
			t.Errorf("%s: no match in %q (rules %v, out %q)", rule, sample, p.Rules, p.Content)
		}
		for _, s := range secrets[rule] {
			if strings.Contains(p.Content, s) {
				t.Errorf("%s: %q still in %q", rule, s, p.Content)
			}
		}
	}
	if p := NewRedactor(nil).Text("postgres://admin:hunter2@db"); p.Content != "postgres://[REDACTED]@db" {
		t.Errorf("url_userinfo = %q", p.Content)
	}
	if p := NewRedactor(nil).Text("an ordinary sentence about tokens and keys"); p.Redactions != 0 {
		t.Errorf("false positive: %+v", p)
	}
}

func TestSensitiveKeysPatternsSecretsAndBinary(t *testing.T) {
	patterns, perr := Compile([]Pattern{{Pattern: `EMP-[0-9]{6}`}})
	if perr != nil {
		t.Fatal(perr)
	}
	r := NewRedactor(patterns, "the-identity-jwt-value", "short")
	in := `{"Password":"p","nested":{"api_key":{"x":1},"Client-Secret":"s","ok":"EMP-123456 and the-identity-jwt-value"},
		"content":[{"type":"image","data":"QUJDREVGR0g=","mimeType":"image/png"},{"type":"text","text":"short"}],
		"resource":{"uri":"x","blob":"AAAAAAAA"}}`
	p := r.Redact([]byte(in))
	var out map[string]any
	if err := json.Unmarshal([]byte(p.Content), &out); err != nil {
		t.Fatalf("not JSON: %v %s", err, p.Content)
	}
	if out["Password"] != Redacted {
		t.Errorf("Password = %v", out["Password"])
	}
	nested := out["nested"].(map[string]any)
	if nested["api_key"] != Redacted || nested["Client-Secret"] != Redacted || nested["ok"] != "[REDACTED] and [REDACTED]" {
		t.Errorf("nested = %v", nested)
	}
	content := out["content"].([]any)
	if img := content[0].(map[string]any); img["data"] != "[binary 8 bytes]" || img["mimeType"] != "image/png" {
		t.Errorf("image = %v", img)
	}
	if txt := content[1].(map[string]any); txt["text"] != "short" {
		t.Errorf("short values are not call secrets: %v", txt)
	}
	if res := out["resource"].(map[string]any); res["blob"] != "[binary 6 bytes]" {
		t.Errorf("blob = %v", res)
	}
	if p.Rules[RuleSensitiveKeys] != 3 || p.Rules["pattern:0"] != 1 || p.Rules[RuleCallSecrets] != 1 || p.Redactions != 5 {
		t.Errorf("rules = %v, redactions %d", p.Rules, p.Redactions)
	}
}

func TestTruncationAtUTF8Boundary(t *testing.T) {
	text := strings.Repeat("é", MaxPartBytes) // 2 bytes each
	raw, _ := json.Marshal(map[string]string{"text": "x" + text})
	p := NewRedactor(nil).Redact(raw)
	if !p.Truncated || len(p.Content) > MaxPartBytes || !utf8.ValidString(p.Content) || p.Bytes != int64(len(raw)) {
		t.Fatalf("truncated=%v len=%d valid=%v bytes=%d", p.Truncated, len(p.Content), utf8.ValidString(p.Content), p.Bytes)
	}
	if s, cut := Truncate("abc", 3); s != "abc" || cut {
		t.Fatal("short strings are kept")
	}
}

func TestCompileRefusals(t *testing.T) {
	many := make([]Pattern, MaxPatterns+1)
	for i := range many {
		many[i] = Pattern{Pattern: "x"}
	}
	for name, tc := range map[string]struct {
		ps     []Pattern
		reason string
		index  int
	}{
		"syntax":        {[]Pattern{{Pattern: "ok"}, {Pattern: "("}}, "syntax", 1},
		"too_long":      {[]Pattern{{Pattern: strings.Repeat("a", MaxPatternLength+1)}}, "too_long", 0},
		"empty":         {[]Pattern{{Pattern: ""}}, "too_long", 0},
		"matches_empty": {[]Pattern{{Pattern: "a*"}}, "matches_empty", 0},
		"too_many":      {many, "too_many", MaxPatterns},
	} {
		_, perr := Compile(tc.ps)
		if perr == nil || perr.Reason != tc.reason || perr.Index != tc.index {
			t.Errorf("%s: %+v", name, perr)
		}
	}
}

func TestNotJSONIsText(t *testing.T) {
	p := NewRedactor(nil).Redact([]byte(`password: Bearer abcdefgh`))
	if p.Content != "password: [REDACTED]" || p.Redactions != 1 {
		t.Fatalf("%+v", p)
	}
}

func TestJSONInsideStrings(t *testing.T) {
	p := NewRedactor(nil).Redact([]byte(`{"text":"args={\"password\":\"hunter2\"}","inner":"{\"token\":\"abc\",\"n\":1}"}`))
	if strings.Contains(p.Content, "abc") || p.Rules[RuleSensitiveKeys] != 1 {
		t.Fatalf("%+v", p)
	}
}

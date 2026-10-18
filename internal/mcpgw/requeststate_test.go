package mcpgw

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ncecere/open-mcp-portal/internal/secrets"
)

func randKey(t *testing.T) []byte {
	t.Helper()
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	return b
}

func newBox(t *testing.T, current []byte, previous ...[]byte) *stateBox {
	t.Helper()
	box, err := secrets.New(current, previous...)
	if err != nil {
		t.Fatal(err)
	}
	s, err := newStateBox(box)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

var sampleClaims = stateClaims{Purpose: purposeConsent, Principal: "user:u1", Gateway: "g1", Method: "tools/call", Name: "srv__tool", Digest: "abc"}

func TestStateRoundTrip(t *testing.T) {
	s := newBox(t, randKey(t))
	now := time.Unix(1_800_000_000, 0)
	s.now = func() time.Time { return now }
	tok, err := s.seal(sampleClaims)
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.open(tok)
	if err != nil {
		t.Fatal(err)
	}
	want := sampleClaims
	want.IssuedAt, want.ExpiresAt = now.Unix(), now.Add(10*time.Minute).Unix()
	if got != want {
		t.Fatalf("claims = %+v, want %+v", got, want)
	}
	// Fresh nonce every time.
	if tok2, _ := s.seal(sampleClaims); tok2 == tok {
		t.Fatal("two tokens are equal")
	}
	// Still valid just before the expiry, not at it.
	s.now = func() time.Time { return now.Add(10*time.Minute - time.Second) }
	if _, err := s.open(tok); err != nil {
		t.Fatalf("one second before expiry: %v", err)
	}
	s.now = func() time.Time { return now.Add(10 * time.Minute) }
	if _, err := s.open(tok); !errors.Is(err, errStateExpired) {
		t.Fatalf("at expiry: %v", err)
	}
	s.now = func() time.Time { return now.Add(time.Hour) }
	if _, err := s.open(tok); !errors.Is(err, errStateExpired) {
		t.Fatalf("an hour later: %v", err)
	}
}

func TestStateTampering(t *testing.T) {
	s := newBox(t, randKey(t))
	tok, _ := s.seal(sampleClaims)
	raw, _ := base64.RawURLEncoding.DecodeString(tok)
	// Every single-bit change, anywhere, is refused.
	for i := range raw {
		for bit := 0; bit < 8; bit += 3 {
			bad := slices.Clone(raw)
			bad[i] ^= 1 << bit
			if _, err := s.open(base64.RawURLEncoding.EncodeToString(bad)); !errors.Is(err, errStateInvalid) {
				t.Fatalf("byte %d bit %d accepted: %v", i, bit, err)
			}
		}
	}
	for _, bad := range []string{"", "!!!", tok[:len(tok)-1], tok + "A", tok[:10], base64.RawURLEncoding.EncodeToString([]byte{1}),
		base64.RawURLEncoding.EncodeToString(append([]byte{2}, raw[1:]...))} {
		if _, err := s.open(bad); !errors.Is(err, errStateInvalid) {
			t.Errorf("open(%q) = %v", bad, err)
		}
	}
	// Another key, another deployment.
	other := newBox(t, randKey(t))
	if _, err := other.open(tok); !errors.Is(err, errStateInvalid) {
		t.Fatalf("another key's token opened: %v", err)
	}
}

func TestStateKeyRotation(t *testing.T) {
	oldKey, newKey := randKey(t), randKey(t)
	before := newBox(t, oldKey)
	tok, _ := before.seal(sampleClaims)
	// After a rotation the old key is the previous one: the person mid-consent isn't stranded...
	after := newBox(t, newKey, oldKey)
	if _, err := after.open(tok); err != nil {
		t.Fatalf("token from the previous key: %v", err)
	}
	// ...new tokens use the new key and the old deployment can't read them...
	tok2, _ := after.seal(sampleClaims)
	if _, err := before.open(tok2); err == nil {
		t.Fatal("old key opened a new token")
	}
	// ...and once the previous key is dropped, old tokens are refused.
	if _, err := newBox(t, newKey).open(tok); !errors.Is(err, errStateInvalid) {
		t.Fatalf("after dropping the previous key: %v", err)
	}
}

// The token's key is derived: the raw ENCRYPTION_KEY (which seals stored
// secrets) can't open it.
func TestStateKeyIsDerived(t *testing.T) {
	raw := randKey(t)
	s := newBox(t, raw)
	tok, _ := s.seal(sampleClaims)
	b, _ := base64.RawURLEncoding.DecodeString(tok)
	block, _ := aes.NewCipher(raw)
	aead, _ := cipher.NewGCM(block)
	if _, err := aead.Open(nil, b[1:1+aead.NonceSize()], b[1+aead.NonceSize():], stateAAD); err == nil {
		t.Fatal("the raw key opened a requestState")
	}
}

// What a requestState holds, and doesn't: opened here (the client can't),
// it has the binding and times, nothing else.
func TestStateHoldsNoSecrets(t *testing.T) {
	var keys []string
	b, _ := json.Marshal(sampleClaims)
	var m map[string]any
	_ = json.Unmarshal(b, &m)
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	if strings.Join(keys, ",") != "d,exp,g,iat,m,n,p,u" {
		t.Fatalf("claims fields = %v: a new field needs a review for secrets", keys)
	}
	// And the sealed form shows nothing of it.
	s := newBox(t, randKey(t))
	tok, _ := s.seal(sampleClaims)
	for _, leak := range []string{"user:u1", "srv__tool", "tools/call", "consent"} {
		if strings.Contains(tok, leak) {
			t.Fatalf("token contains %q", leak)
		}
		raw, _ := base64.RawURLEncoding.DecodeString(tok)
		if strings.Contains(string(raw), leak) {
			t.Fatalf("decoded token contains %q", leak)
		}
	}
}

func TestParamsDigest(t *testing.T) {
	base := paramsDigest("tools/call", json.RawMessage(`{"name":"a","arguments":{"x":1,"y":[1,2]}}`))
	same := []string{
		`{"arguments":{"y":[1,2],"x":1},"name":"a"}`,
		`{"name":"a","arguments":{"x":1,"y":[1,2]},"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28","progressToken":"p"}}`,
		`{"name":"a","arguments":{"x":1,"y":[1,2]},"inputResponses":{"k":{"action":"accept"}},"requestState":"abc"}`,
		`{ "name" : "a" , "arguments" : { "x" : 1 , "y" : [ 1 , 2 ] } }`,
	}
	for _, p := range same {
		if got := paramsDigest("tools/call", json.RawMessage(p)); got != base {
			t.Errorf("digest of %s differs", p)
		}
	}
	different := map[string]string{
		"name":      `{"name":"b","arguments":{"x":1,"y":[1,2]}}`,
		"argument":  `{"name":"a","arguments":{"x":2,"y":[1,2]}}`,
		"order":     `{"name":"a","arguments":{"x":1,"y":[2,1]}}`,
		"type":      `{"name":"a","arguments":{"x":"1","y":[1,2]}}`,
		"extra":     `{"name":"a","arguments":{"x":1,"y":[1,2]},"other":true}`,
		"no args":   `{"name":"a"}`,
		"precision": `{"name":"a","arguments":{"x":1.0,"y":[1,2]}}`,
	}
	for what, p := range different {
		if got := paramsDigest("tools/call", json.RawMessage(p)); got == base {
			t.Errorf("a different %s has the same digest", what)
		}
	}
	if paramsDigest("prompts/get", json.RawMessage(`{"name":"a","arguments":{"x":1,"y":[1,2]}}`)) == base {
		t.Error("the method isn't part of the digest")
	}
	// Large integers keep their exact value.
	if paramsDigest("tools/call", json.RawMessage(`{"n":9007199254740993}`)) == paramsDigest("tools/call", json.RawMessage(`{"n":9007199254740992}`)) {
		t.Error("big integers collide")
	}
}

func TestShape(t *testing.T) {
	rc := &request{modern: true}
	rc.gateway.Slug, rc.gateway.Name = "support", "Support"
	m := rc.shape(map[string]any{"tools": []any{}}).(map[string]any)
	meta := m["_meta"].(map[string]any)
	info := meta[metaServerInfo].(map[string]any)
	if m["resultType"] != "complete" || info["name"] != "support" || info["title"] != "Support" || info["version"] == "" {
		t.Fatalf("shape = %v", m)
	}
	if m := rc.shape(map[string]any{"resultType": "input_required"}).(map[string]any); m["resultType"] != "input_required" {
		t.Fatalf("resultType overwritten: %v", m)
	}
	raw := rc.shape(json.RawMessage(`{"content":[],"resultType":"input_required","_meta":{"x":1,"io.modelcontextprotocol/serverInfo":{"name":"up"}}}`)).(json.RawMessage)
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	meta = out["_meta"].(map[string]any)
	if out["resultType"] != "complete" || meta["x"] != float64(1) || meta[metaServerInfo].(map[string]any)["name"] != "support" {
		t.Fatalf("relayed = %s", raw)
	}
	// Reads carry cache hints; nothing else does.
	rc.msg.Method = methodRead
	out = nil
	_ = json.Unmarshal(rc.shape(json.RawMessage(`{"contents":[]}`)).(json.RawMessage), &out)
	if out["ttlMs"] != float64(0) || out["cacheScope"] != "private" {
		t.Fatalf("read = %v", out)
	}
	rc.msg.Method = methodToolsCall
	out = nil
	_ = json.Unmarshal(rc.shape(json.RawMessage(`{"content":[]}`)).(json.RawMessage), &out)
	if _, ok := out["ttlMs"]; ok {
		t.Fatalf("call has cache hints: %v", out)
	}
	// Legacy results are untouched.
	legacy := &request{}
	in := json.RawMessage(`{"content":[]}`)
	if got := legacy.shape(in).(json.RawMessage); string(got) != string(in) {
		t.Fatalf("legacy changed: %s", got)
	}
	lm := map[string]any{"tools": []any{}}
	if got := legacy.shape(lm).(map[string]any); len(got) != 1 {
		t.Fatalf("legacy map changed: %v", got)
	}
}

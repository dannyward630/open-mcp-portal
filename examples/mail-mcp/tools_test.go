package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"log"
	"mime"
	"net/http"
	"net/http/httptest"
	"net/mail"
	"strings"
	"sync"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// mailEnv is the mail server (behind Guard) talking to a fake Gmail, with a
// fake gateway minting identity tokens.
type mailEnv struct {
	t     *testing.T
	gw    *fakeGateway
	gmail *FakeGmail
	srv   *MailServer
	url   string
	logs  *strings.Builder
	mu    sync.Mutex
	calls []CallInfo
}

func newMailEnv(t *testing.T) *mailEnv {
	t.Helper()
	e := &mailEnv{t: t, gw: newFakeGateway(t), gmail: NewFakeGmail(), logs: &strings.Builder{}}
	gmailSrv := httptest.NewServer(e.gmail)
	t.Cleanup(gmailSrv.Close)
	var logMu sync.Mutex
	logger := log.New(writerFunc(func(p []byte) (int, error) { logMu.Lock(); defer logMu.Unlock(); return e.logs.Write(p) }), "", 0)
	e.srv = &MailServer{Gmail: &Gmail{Base: gmailSrv.URL}, Provider: "google", MaxBodyChars: 500, Logf: logger.Printf,
		OnCall: func(c CallInfo) { e.mu.Lock(); e.calls = append(e.calls, c); e.mu.Unlock() }}
	v := e.gw.verifier()
	mux := http.NewServeMux()
	mux.Handle("/mcp", (&Guard{Verifier: v, Logf: logger.Printf}).Wrap(e.srv.Handler()))
	hs := httptest.NewServer(mux)
	t.Cleanup(hs.Close)
	e.url = hs.URL + "/mcp"
	return e
}

type writerFunc func([]byte) (int, error)

func (f writerFunc) Write(p []byte) (int, error) { return f(p) }

// headerRT adds headers to every request, as the gateway does.
type headerRT map[string]string

func (h headerRT) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	for k, v := range h {
		r.Header.Set(k, v)
	}
	return http.DefaultTransport.RoundTrip(r)
}

// session connects like the gateway: an identity token for claims and,
// when providerToken is set, the provider headers.
func (e *mailEnv) session(claims map[string]any, providerToken string) *mcp.ClientSession {
	e.t.Helper()
	h := headerRT{}
	if claims != nil {
		h["Authorization"] = "Bearer " + e.gw.mint("kid-1", claims)
	}
	if providerToken != "" {
		p, _ := claims["provider"].(string)
		h[headerProvider], h[headerProviderToken] = p, providerToken
	}
	c := mcp.NewClient(&mcp.Implementation{Name: "test-gateway", Version: "1"}, nil)
	s, err := c.Connect(context.Background(), &mcp.StreamableClientTransport{Endpoint: e.url, HTTPClient: &http.Client{Transport: h}}, nil)
	if err != nil {
		e.t.Fatal(err)
	}
	e.t.Cleanup(func() { _ = s.Close() })
	return s
}

func callTool(t *testing.T, s *mcp.ClientSession, name string, args map[string]any) (*mcp.CallToolResult, string) {
	t.Helper()
	res, err := s.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	var text strings.Builder
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			text.WriteString(tc.Text)
		}
	}
	return res, text.String()
}

func structured[T any](t *testing.T, res *mcp.CallToolResult) T {
	t.Helper()
	var out T
	b, _ := json.Marshal(res.StructuredContent)
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatalf("structured content %s: %v", b, err)
	}
	return out
}

func TestToolsList(t *testing.T) {
	e := newMailEnv(t)
	// Discovery comes without an identity token, as from the gateway.
	res, err := e.session(nil, "").ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	ann := map[string]*mcp.ToolAnnotations{}
	for _, tool := range res.Tools {
		ann[tool.Name] = tool.Annotations
	}
	if len(ann) != 3 || !ann["search_messages"].ReadOnlyHint || !ann["read_message"].ReadOnlyHint {
		t.Fatalf("tools = %v", ann)
	}
	d := ann["create_draft"]
	if d.ReadOnlyHint || d.DestructiveHint == nil || *d.DestructiveHint {
		t.Fatalf("create_draft annotations = %+v", d)
	}
}

func TestSearchReadDraft(t *testing.T) {
	e := newMailEnv(t)
	const token = "ya29.fake-access-token-for-tests"
	s := e.session(baseClaims(), token)

	res, text := callTool(t, s, "search_messages", map[string]any{"query": "from:alice"})
	if res.IsError {
		t.Fatalf("search: %s", text)
	}
	out := structured[SearchOutput](t, res)
	if len(out.Messages) != 2 || out.Messages[0].ID != "18c1a2b3c4d5e6f4" || out.Messages[0].Subject != "Re: Quarterly report draft" ||
		!strings.Contains(out.Messages[0].From, "alice@example.com") || out.Messages[0].Date == "" || out.Messages[0].Snippet == "" {
		t.Fatalf("search = %+v", out)
	}
	if res, _ := callTool(t, s, "search_messages", map[string]any{"query": "", "max": 2}); len(structured[SearchOutput](t, res).Messages) != 2 {
		t.Fatal("max not applied")
	}
	if res, text := callTool(t, s, "search_messages", map[string]any{"query": "x", "max": 26}); !res.IsError || !strings.Contains(text, "between 1 and 25") {
		t.Fatalf("max 26 = %v %s", res.IsError, text)
	}
	if res, _ := callTool(t, s, "search_messages", map[string]any{"query": "nothing-matches-this"}); len(structured[SearchOutput](t, res).Messages) != 0 {
		t.Fatal("expected no messages")
	}

	// Plain text with an attachment.
	res, text = callTool(t, s, "read_message", map[string]any{"id": "18c1a2b3c4d5e6f1"})
	msg := structured[Message](t, res)
	if res.IsError || msg.Headers["Subject"] != "Quarterly report draft" || !strings.Contains(msg.Body, "revenue section") ||
		msg.Truncated || len(msg.Attachments) != 1 || msg.Attachments[0] != "q3-report.pdf" {
		t.Fatalf("read = %s", text)
	}
	// HTML only: tags removed.
	res, _ = callTool(t, s, "read_message", map[string]any{"id": "18c1a2b3c4d5e6f2"})
	if msg := structured[Message](t, res); !strings.Contains(msg.Body, "Email will be unavailable Saturday 02:00–04:00 UTC.") || strings.Contains(msg.Body, "<") {
		t.Fatalf("html body = %q", msg.Body)
	}
	// Long body: cut to MaxBodyChars.
	res, _ = callTool(t, s, "read_message", map[string]any{"id": "18c1a2b3c4d5e6f4"})
	if msg := structured[Message](t, res); !msg.Truncated || len([]rune(msg.Body)) != 500 {
		t.Fatalf("truncated=%v len=%d", msg.Truncated, len([]rune(msg.Body)))
	}
	for id, want := range map[string]string{"../drafts": "must be a Gmail message ID", "ffffffffffffffff": "no such message"} {
		if res, text := callTool(t, s, "read_message", map[string]any{"id": id}); !res.IsError || !strings.Contains(text, want) {
			t.Fatalf("read %q = %v %s", id, res.IsError, text)
		}
	}

	// Draft: a plain-text RFC 5322 message, not sent.
	res, text = callTool(t, s, "create_draft", map[string]any{"to": "Bob Builder <bob@example.com>", "subject": "Re: Lunch — Friday",
		"body": "Friday at noon works.\n\nPat"})
	if d := structured[Draft](t, res); res.IsError || d.DraftID == "" || d.MessageID == "" {
		t.Fatalf("draft = %s", text)
	}
	drafts := e.gmail.Drafts()
	if len(drafts) != 1 {
		t.Fatalf("drafts = %d", len(drafts))
	}
	m, err := mail.ReadMessage(strings.NewReader(string(drafts[0])))
	if err != nil {
		t.Fatal(err)
	}
	subject, _ := new(mime.WordDecoder).DecodeHeader(m.Header.Get("Subject"))
	body, _ := io.ReadAll(base64.NewDecoder(base64.StdEncoding, m.Body))
	if m.Header.Get("To") != `"Bob Builder" <bob@example.com>` || subject != "Re: Lunch — Friday" || string(body) != "Friday at noon works.\n\nPat" {
		t.Fatalf("draft message: to=%q subject=%q body=%q", m.Header.Get("To"), subject, body)
	}
	for _, args := range []map[string]any{
		{"to": "not an address", "subject": "x", "body": "y"},
		{"to": "a@example.com", "subject": "two\r\nBcc: evil@example.com", "body": "y"},
		{"to": "a@example.com", "subject": " ", "body": "y"},
	} {
		if res, text := callTool(t, s, "create_draft", args); !res.IsError {
			t.Fatalf("draft %v accepted: %s", args, text)
		}
	}
	if len(e.gmail.Drafts()) != 1 {
		t.Fatal("an invalid draft reached Gmail")
	}

	// Gmail saw the provider token; logs never contain it, the query or contents.
	if seen := e.gmail.TokensSeen(); len(seen) == 0 || seen[0] != digest(token) {
		t.Fatalf("Gmail tokens = %v", seen)
	}
	logs := e.logs.String()
	for _, secret := range []string{token, "from:alice", "revenue section", "Friday at noon", "pat@example.com"} {
		if strings.Contains(logs, secret) {
			t.Fatalf("logs contain %q:\n%s", secret, logs)
		}
	}
	if !strings.Contains(logs, "tool=search_messages sub=6f1c2a40-0000-4000-8000-000000000001 jti=jti-1") {
		t.Fatalf("logs = %s", logs)
	}
}

func TestProviderChecks(t *testing.T) {
	e := newMailEnv(t)
	args := map[string]any{"query": "lunch"}

	// No requirement on the server: the gateway sends neither the token nor
	// the claim. The tool explains what to configure.
	res, text := callTool(t, e.session(with(baseClaims(), "provider", nil), ""), "search_messages", args)
	if !res.IsError || !strings.Contains(text, "user authorization requirement") || !strings.Contains(text, `"google"`) {
		t.Fatalf("no token = %v %s", res.IsError, text)
	}
	// The requirement names another provider.
	res, text = callTool(t, e.session(with(baseClaims(), "provider", "github"), "gho_x"), "search_messages", args)
	if !res.IsError || !strings.Contains(text, `token for the "github" auth provider`) {
		t.Fatalf("wrong provider = %v %s", res.IsError, text)
	}
	// Service accounts never act on a mailbox.
	sa := with(with(baseClaims(), "principal_type", "service_account"), "email", nil)
	res, text = callTool(t, e.session(sa, "ya29.x"), "search_messages", args)
	if !res.IsError || !strings.Contains(text, "service accounts") {
		t.Fatalf("service account = %v %s", res.IsError, text)
	}
	// Google says the token was revoked.
	e.gmail.Revoke("ya29.revoked")
	res, text = callTool(t, e.session(baseClaims(), "ya29.revoked"), "search_messages", args)
	if !res.IsError || !strings.Contains(text, "My connections") {
		t.Fatalf("revoked = %v %s", res.IsError, text)
	}
	if strings.Contains(text, "ya29.revoked") {
		t.Fatal("token in tool error")
	}
	if len(e.gmail.Drafts()) != 0 {
		t.Fatal("unexpected draft")
	}
	e.mu.Lock()
	n := len(e.calls)
	e.mu.Unlock()
	if n != 4 {
		t.Fatalf("calls = %d", n)
	}
}

// Without an identity token, tools/call never reaches a tool.
func TestCallWithoutIdentityIs401(t *testing.T) {
	e := newMailEnv(t)
	body := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"search_messages","arguments":{"query":""}}}`
	req, _ := http.NewRequest(http.MethodPost, e.url, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set(headerProvider, "google")
	req.Header.Set(headerProviderToken, "ya29.stolen")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != 401 || len(e.gmail.TokensSeen()) != 0 {
		t.Fatalf("status = %d, Gmail requests = %d", res.StatusCode, len(e.gmail.TokensSeen()))
	}
}

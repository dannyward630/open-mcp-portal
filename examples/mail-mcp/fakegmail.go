package main

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
)

// FakeGmail is an in-memory stand-in for the parts of the Gmail API this
// server uses (messages.list, messages.get with format metadata/full,
// drafts.create). It accepts any bearer token except those marked revoked,
// so the example runs locally with cmd/fakeoauth standing in for Google.
//
// Queries support free words (matched against sender, subject and body),
// from:, to:, subject: and is:unread, all case-insensitive.
type FakeGmail struct {
	mu       sync.Mutex
	messages []fakeMessage
	drafts   []fakeDraft
	revoked  map[string]bool
	tokens   []string // digests of the tokens seen, oldest first
}

type fakeMessage struct {
	ID, ThreadID, From, To, Subject, Date, Plain, HTML string
	Labels                                             []string
	Attachment                                         string
}

type fakeDraft struct {
	ID, MessageID string
	Raw           []byte
}

// NewFakeGmail returns a fake mailbox with a few canned messages.
func NewFakeGmail() *FakeGmail {
	return &FakeGmail{revoked: map[string]bool{}, messages: []fakeMessage{
		{ID: "18c1a2b3c4d5e6f1", ThreadID: "18c1a2b3c4d5e6f1", From: "Alice Example <alice@example.com>", To: "you@example.com",
			Subject: "Quarterly report draft", Date: "Mon, 12 Oct 2026 09:14:00 -0400", Labels: []string{"INBOX", "UNREAD"},
			Plain:      "Hi,\n\nThe Q3 report draft is attached. Could you review the revenue section by Thursday?\n\nThanks,\nAlice",
			Attachment: "q3-report.pdf"},
		{ID: "18c1a2b3c4d5e6f2", ThreadID: "18c1a2b3c4d5e6f2", From: "IT Service Desk <servicedesk@example.com>", To: "you@example.com",
			Subject: "Scheduled maintenance this weekend", Date: "Tue, 13 Oct 2026 16:02:00 -0400", Labels: []string{"INBOX"},
			HTML: "<html><body><p>Email will be <b>unavailable</b> Saturday 02:00&ndash;04:00 UTC.</p><p>No action is needed.</p></body></html>"},
		{ID: "18c1a2b3c4d5e6f3", ThreadID: "18c1a2b3c4d5e6f3", From: "Bob Builder <bob@example.com>", To: "you@example.com",
			Subject: "Lunch on Friday?", Date: "Wed, 14 Oct 2026 11:30:00 -0400", Labels: []string{"INBOX", "UNREAD"},
			Plain: "Are you free for lunch on Friday at noon? The new place on 5th street.\n\nBob"},
		{ID: "18c1a2b3c4d5e6f4", ThreadID: "18c1a2b3c4d5e6f1", From: "Alice Example <alice@example.com>", To: "you@example.com",
			Subject: "Re: Quarterly report draft", Date: "Thu, 15 Oct 2026 08:45:00 -0400", Labels: []string{"INBOX"},
			Plain: "Small correction: the deadline is Friday, not Thursday.\n\nAlice\n\n" + strings.Repeat("(quoted text) ", 3000)},
	}}
}

// Revoke makes the fake answer 401 to a token (a revoked grant).
func (f *FakeGmail) Revoke(token string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.revoked[token] = true
}

// TokensSeen are the SHA-256 digests (hex) of the bearer tokens received.
func (f *FakeGmail) TokensSeen() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.tokens...)
}

// Drafts returns the raw RFC 5322 drafts created so far.
func (f *FakeGmail) Drafts() [][]byte {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([][]byte, len(f.drafts))
	for i, d := range f.drafts {
		out[i] = d.Raw
	}
	return out
}

func digest(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}

func gmailError(w http.ResponseWriter, code int, status, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{"code": code, "message": msg, "status": status}})
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

// ServeHTTP serves /gmail/v1/users/me/... (mount it at the API origin).
func (f *FakeGmail) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	auth := r.Header.Get("Authorization")
	token, ok := strings.CutPrefix(auth, "Bearer ")
	f.mu.Lock()
	revoked := f.revoked[token]
	if ok && token != "" {
		f.tokens = append(f.tokens, digest(token))
	}
	f.mu.Unlock()
	if !ok || token == "" || revoked {
		gmailError(w, http.StatusUnauthorized, "UNAUTHENTICATED", "Request had invalid authentication credentials.")
		return
	}
	path, found := strings.CutPrefix(r.URL.Path, "/gmail/v1/users/me/")
	if !found {
		gmailError(w, http.StatusNotFound, "NOT_FOUND", "Not found")
		return
	}
	switch {
	case r.Method == http.MethodGet && path == "messages":
		f.list(w, r)
	case r.Method == http.MethodGet && strings.HasPrefix(path, "messages/"):
		f.get(w, r, strings.TrimPrefix(path, "messages/"))
	case r.Method == http.MethodPost && path == "drafts":
		f.createDraft(w, r)
	default:
		gmailError(w, http.StatusNotFound, "NOT_FOUND", "Not found")
	}
}

func (m fakeMessage) matches(query string) bool {
	text := strings.ToLower(m.From + " " + m.Subject + " " + m.Plain + " " + m.HTML)
	for _, term := range strings.Fields(strings.ToLower(query)) {
		key, val, hasKey := strings.Cut(term, ":")
		switch {
		case hasKey && key == "from":
			if !strings.Contains(strings.ToLower(m.From), val) {
				return false
			}
		case hasKey && key == "to":
			if !strings.Contains(strings.ToLower(m.To), val) {
				return false
			}
		case hasKey && key == "subject":
			if !strings.Contains(strings.ToLower(m.Subject), val) {
				return false
			}
		case hasKey && key == "is" && val == "unread":
			unread := false
			for _, l := range m.Labels {
				unread = unread || l == "UNREAD"
			}
			if !unread {
				return false
			}
		case hasKey && (key == "newer_than" || key == "older_than" || key == "in" || key == "label"):
			// Accepted and ignored by the fake.
		default:
			if !strings.Contains(text, term) {
				return false
			}
		}
	}
	return true
}

func (f *FakeGmail) list(w http.ResponseWriter, r *http.Request) {
	max := 100
	if v := r.URL.Query().Get("maxResults"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > 500 {
			gmailError(w, http.StatusBadRequest, "INVALID_ARGUMENT", "Invalid maxResults")
			return
		}
		max = n
	}
	q := r.URL.Query().Get("q")
	f.mu.Lock()
	defer f.mu.Unlock()
	type ref struct {
		ID       string `json:"id"`
		ThreadID string `json:"threadId"`
	}
	out := []ref{}
	// Newest first, like Gmail.
	for i := len(f.messages) - 1; i >= 0 && len(out) < max; i-- {
		if m := f.messages[i]; m.matches(q) {
			out = append(out, ref{m.ID, m.ThreadID})
		}
	}
	res := map[string]any{"resultSizeEstimate": len(out)}
	if len(out) > 0 {
		res["messages"] = out
	}
	writeJSON(w, res)
}

func b64(s string) string { return base64.URLEncoding.EncodeToString([]byte(s)) }

func (f *FakeGmail) get(w http.ResponseWriter, r *http.Request, id string) {
	f.mu.Lock()
	var msg *fakeMessage
	for i := range f.messages {
		if f.messages[i].ID == id {
			m := f.messages[i]
			msg = &m
		}
	}
	f.mu.Unlock()
	if msg == nil {
		gmailError(w, http.StatusNotFound, "NOT_FOUND", "Requested entity was not found.")
		return
	}
	headers := []map[string]string{{"name": "From", "value": msg.From}, {"name": "To", "value": msg.To},
		{"name": "Subject", "value": msg.Subject}, {"name": "Date", "value": msg.Date},
		{"name": "Message-ID", "value": "<" + msg.ID + "@mail.example.com>"}}
	snippet := msg.Plain
	if snippet == "" {
		snippet = htmlToText(msg.HTML)
	}
	snippet = strings.Join(strings.Fields(snippet), " ")
	if len(snippet) > 100 {
		snippet = snippet[:100]
	}
	res := map[string]any{"id": msg.ID, "threadId": msg.ThreadID, "labelIds": msg.Labels, "snippet": snippet}
	q := r.URL.Query()
	switch q.Get("format") {
	case "metadata":
		want := map[string]bool{}
		for _, h := range q["metadataHeaders"] {
			want[strings.ToLower(h)] = true
		}
		var hs []map[string]string
		for _, h := range headers {
			if len(want) == 0 || want[strings.ToLower(h["name"])] {
				hs = append(hs, h)
			}
		}
		res["payload"] = map[string]any{"mimeType": "multipart/alternative", "headers": hs}
	case "", "full":
		var parts []map[string]any
		if msg.Plain != "" {
			parts = append(parts, map[string]any{"mimeType": "text/plain", "body": map[string]any{"size": len(msg.Plain), "data": b64(msg.Plain)}})
		}
		if msg.HTML != "" {
			parts = append(parts, map[string]any{"mimeType": "text/html", "body": map[string]any{"size": len(msg.HTML), "data": b64(msg.HTML)}})
		}
		payload := map[string]any{"mimeType": "multipart/alternative", "headers": headers, "parts": parts}
		if msg.Attachment != "" {
			payload = map[string]any{"mimeType": "multipart/mixed", "headers": headers, "parts": []map[string]any{
				{"mimeType": "multipart/alternative", "parts": parts},
				{"mimeType": "application/pdf", "filename": msg.Attachment, "body": map[string]any{"size": 12345, "attachmentId": "att1"}},
			}}
		}
		res["payload"] = payload
	default:
		gmailError(w, http.StatusBadRequest, "INVALID_ARGUMENT", "Invalid format")
		return
	}
	writeJSON(w, res)
}

func (f *FakeGmail) createDraft(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Message struct {
			Raw string `json:"raw"`
		} `json:"message"`
	}
	body, _ := io.ReadAll(io.LimitReader(r.Body, 36<<20))
	if err := json.Unmarshal(body, &req); err != nil || req.Message.Raw == "" {
		gmailError(w, http.StatusBadRequest, "INVALID_ARGUMENT", "Missing draft message")
		return
	}
	raw, err := base64.URLEncoding.DecodeString(req.Message.Raw)
	if err != nil {
		gmailError(w, http.StatusBadRequest, "INVALID_ARGUMENT", "Invalid value at 'draft.message.raw'")
		return
	}
	f.mu.Lock()
	n := len(f.drafts) + 1
	d := fakeDraft{ID: fmt.Sprintf("r-fake-draft-%d", n), MessageID: fmt.Sprintf("18d0000000000%03d", n), Raw: raw}
	f.drafts = append(f.drafts, d)
	f.mu.Unlock()
	writeJSON(w, map[string]any{"id": d.ID, "message": map[string]any{"id": d.MessageID, "threadId": d.MessageID, "labelIds": []string{"DRAFT"}}})
}

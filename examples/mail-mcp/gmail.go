package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"mime"
	"net/http"
	"net/mail"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"
)

// Gmail is a minimal client of the Gmail REST API
// (https://developers.google.com/gmail/api/reference/rest) that acts with
// the person's access token, which the gateway sends with each call.
type Gmail struct {
	// Base is the API origin, https://gmail.googleapis.com (or the fake's).
	Base string
	HTTP *http.Client
}

// Gmail API failures, phrased for the person (and the model) reading the
// tool error. They never contain the token or message contents.
var (
	errTokenRejected = errors.New("Google rejected the access token (revoked or expired). Reconnect your Google account under Account › My connections in the gateway, then try again")
	errForbidden     = errors.New("Google refused the request: the connected account didn't grant the scope this tool needs, or the Gmail API isn't enabled for the OAuth client")
	errNotFound      = errors.New("no such message")
	errUnavailable   = errors.New("Gmail is unavailable right now; try again later")
)

func (g *Gmail) client() *http.Client {
	if g.HTTP != nil {
		return g.HTTP
	}
	return &http.Client{Timeout: 20 * time.Second}
}

// do sends one request to /gmail/v1/users/me/<path> and decodes the JSON
// answer into out.
func (g *Gmail) do(ctx context.Context, token, method, path string, query url.Values, body, out any) error {
	u := strings.TrimRight(g.Base, "/") + "/gmail/v1/users/me/" + path
	if len(query) > 0 {
		u += "?" + query.Encode()
	}
	var rdr io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, u, rdr)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	res, err := g.client().Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return errUnavailable
	}
	defer res.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(res.Body, 25<<20))
	if err != nil {
		return errUnavailable
	}
	switch {
	case res.StatusCode == http.StatusUnauthorized:
		return errTokenRejected
	case res.StatusCode == http.StatusForbidden:
		return errForbidden
	case res.StatusCode == http.StatusNotFound:
		return errNotFound
	case res.StatusCode == http.StatusBadRequest:
		// Google's error.message for a 400 describes the request (for
		// example an invalid query), not mailbox contents.
		var e struct {
			Error struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		_ = json.Unmarshal(raw, &e)
		msg := e.Error.Message
		if len(msg) > 200 {
			msg = msg[:200]
		}
		return fmt.Errorf("Gmail refused the request: %s", msg)
	case res.StatusCode >= 300:
		return errUnavailable
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return errUnavailable
	}
	return nil
}

type gmailHeader struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

type gmailBody struct {
	Size int    `json:"size"`
	Data string `json:"data"` // base64url
}

type gmailPart struct {
	MimeType string        `json:"mimeType"`
	Filename string        `json:"filename"`
	Headers  []gmailHeader `json:"headers"`
	Body     gmailBody     `json:"body"`
	Parts    []gmailPart   `json:"parts"`
}

type gmailMessage struct {
	ID       string    `json:"id"`
	ThreadID string    `json:"threadId"`
	LabelIDs []string  `json:"labelIds"`
	Snippet  string    `json:"snippet"`
	Payload  gmailPart `json:"payload"`
}

func (p gmailPart) header(name string) string {
	for _, h := range p.Headers {
		if strings.EqualFold(h.Name, name) {
			return h.Value
		}
	}
	return ""
}

// MessageSummary is one search result.
type MessageSummary struct {
	ID      string `json:"id"`
	From    string `json:"from"`
	Subject string `json:"subject"`
	Date    string `json:"date"`
	Snippet string `json:"snippet"`
}

// Search lists up to max messages matching a Gmail search query, with the
// From, Subject and Date headers and Gmail's snippet.
func (g *Gmail) Search(ctx context.Context, token, query string, max int) ([]MessageSummary, error) {
	var list struct {
		Messages []struct {
			ID string `json:"id"`
		} `json:"messages"`
	}
	q := url.Values{"maxResults": {fmt.Sprint(max)}}
	if query != "" {
		q.Set("q", query)
	}
	if err := g.do(ctx, token, http.MethodGet, "messages", q, nil, &list); err != nil {
		return nil, err
	}
	out := make([]MessageSummary, len(list.Messages))
	errs := make([]error, len(list.Messages))
	sem := make(chan struct{}, 5) // Gmail's per-user rate limits favour few parallel requests
	var wg sync.WaitGroup
	for i, m := range list.Messages {
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			var msg gmailMessage
			meta := url.Values{"format": {"metadata"}, "metadataHeaders": {"From", "Subject", "Date"}}
			if err := g.do(ctx, token, http.MethodGet, "messages/"+url.PathEscape(m.ID), meta, nil, &msg); err != nil {
				errs[i] = err
				return
			}
			out[i] = MessageSummary{ID: msg.ID, From: msg.Payload.header("From"), Subject: msg.Payload.header("Subject"),
				Date: msg.Payload.header("Date"), Snippet: html.UnescapeString(msg.Snippet)}
		}()
	}
	wg.Wait()
	// A message deleted between the list and the get is skipped; any other
	// failure fails the search.
	kept := out[:0]
	for i, s := range out {
		switch {
		case errs[i] == nil:
			kept = append(kept, s)
		case errors.Is(errs[i], errNotFound):
		default:
			return nil, errs[i]
		}
	}
	return kept, nil
}

// Message is a message read in full.
type Message struct {
	ID        string            `json:"id"`
	ThreadID  string            `json:"threadId"`
	Labels    []string          `json:"labels,omitempty"`
	Headers   map[string]string `json:"headers"`
	Body      string            `json:"body"`
	Truncated bool              `json:"truncated"`
	// Attachments lists attachment file names (their contents are not read).
	Attachments []string `json:"attachments,omitempty"`
}

var shownHeaders = []string{"From", "To", "Cc", "Reply-To", "Subject", "Date", "Message-ID"}

// Read fetches one message and returns its main headers and its plain-text
// body (the text/plain part, else the text/html part with tags removed),
// cut to maxChars characters.
func (g *Gmail) Read(ctx context.Context, token, id string, maxChars int) (*Message, error) {
	var msg gmailMessage
	if err := g.do(ctx, token, http.MethodGet, "messages/"+url.PathEscape(id), url.Values{"format": {"full"}}, nil, &msg); err != nil {
		return nil, err
	}
	out := &Message{ID: msg.ID, ThreadID: msg.ThreadID, Labels: msg.LabelIDs, Headers: map[string]string{}}
	for _, h := range shownHeaders {
		if v := msg.Payload.header(h); v != "" {
			out.Headers[h] = v
		}
	}
	plain, htmlText := "", ""
	var walk func(p gmailPart)
	walk = func(p gmailPart) {
		if p.Filename != "" {
			out.Attachments = append(out.Attachments, p.Filename)
			return
		}
		switch {
		case strings.HasPrefix(p.MimeType, "text/plain") && plain == "":
			plain = decodeBody(p.Body.Data)
		case strings.HasPrefix(p.MimeType, "text/html") && htmlText == "":
			htmlText = htmlToText(decodeBody(p.Body.Data))
		}
		for _, c := range p.Parts {
			walk(c)
		}
	}
	walk(msg.Payload)
	body := plain
	if body == "" {
		body = htmlText
	}
	if body == "" {
		body = html.UnescapeString(msg.Snippet)
	}
	out.Body, out.Truncated = truncate(strings.TrimSpace(body), maxChars)
	return out, nil
}

func decodeBody(data string) string {
	b, err := base64.URLEncoding.DecodeString(data)
	if err != nil {
		b, err = base64.RawURLEncoding.DecodeString(strings.TrimRight(data, "="))
		if err != nil {
			return ""
		}
	}
	return string(b)
}

var (
	reScriptStyle = regexp.MustCompile(`(?is)<(script|style)[^>]*>.*?</(script|style)>`)
	reBreak       = regexp.MustCompile(`(?i)<(br|/p|/div|/tr|/li|/h[1-6])[^>]*>`)
	reTag         = regexp.MustCompile(`(?s)<[^>]*>`)
	reBlankLines  = regexp.MustCompile(`\n\s*\n\s*\n+`)
)

// htmlToText is a rough HTML-to-text conversion, enough for a model to read
// an HTML-only email.
func htmlToText(s string) string {
	s = reScriptStyle.ReplaceAllString(s, "")
	s = reBreak.ReplaceAllString(s, "\n")
	s = reTag.ReplaceAllString(s, "")
	s = html.UnescapeString(s)
	return reBlankLines.ReplaceAllString(s, "\n\n")
}

// truncate cuts s to at most n characters (runes).
func truncate(s string, n int) (string, bool) {
	if n <= 0 {
		return s, false
	}
	r := []rune(s)
	if len(r) <= n {
		return s, false
	}
	return string(r[:n]), true
}

// Draft is a created draft.
type Draft struct {
	DraftID   string `json:"draftId"`
	MessageID string `json:"messageId"`
}

// CreateDraft saves a plain-text draft (it is not sent).
func (g *Gmail) CreateDraft(ctx context.Context, token string, to []*mail.Address, subject, body string) (*Draft, error) {
	raw, err := buildMessage(to, subject, body)
	if err != nil {
		return nil, err
	}
	var out struct {
		ID      string `json:"id"`
		Message struct {
			ID string `json:"id"`
		} `json:"message"`
	}
	req := map[string]any{"message": map[string]string{"raw": base64.URLEncoding.EncodeToString(raw)}}
	if err := g.do(ctx, token, http.MethodPost, "drafts", nil, req, &out); err != nil {
		return nil, err
	}
	return &Draft{DraftID: out.ID, MessageID: out.Message.ID}, nil
}

// buildMessage writes an RFC 5322 message with a UTF-8 plain-text body.
// Gmail fills in From with the account's address.
func buildMessage(to []*mail.Address, subject, body string) ([]byte, error) {
	if strings.ContainsAny(subject, "\r\n") {
		return nil, errors.New("subject must be one line")
	}
	addrs := make([]string, len(to))
	for i, a := range to {
		addrs[i] = a.String() // encodes non-ASCII names
	}
	var b bytes.Buffer
	fmt.Fprintf(&b, "To: %s\r\n", strings.Join(addrs, ", "))
	fmt.Fprintf(&b, "Subject: %s\r\n", mime.QEncoding.Encode("utf-8", subject))
	b.WriteString("MIME-Version: 1.0\r\nContent-Type: text/plain; charset=UTF-8\r\nContent-Transfer-Encoding: base64\r\n\r\n")
	enc := base64.StdEncoding.EncodeToString([]byte(body))
	for len(enc) > 76 {
		b.WriteString(enc[:76] + "\r\n")
		enc = enc[76:]
	}
	b.WriteString(enc + "\r\n")
	return b.Bytes(), nil
}

package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/mail"
	"regexp"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// MailServer holds what the tools need. One *mcp.Server is built per HTTP
// request (the gateway is stateless), bound to that request's verified
// identity.
type MailServer struct {
	Gmail *Gmail
	// Provider is the auth provider slug the gateway must name, for example
	// "google" (the slug a platform admin gave the provider).
	Provider string
	// MaxBodyChars cuts read_message bodies (default 20000).
	MaxBodyChars int
	// Logf logs one line per tool call: tool, subject, jti, outcome. Never
	// a token, a query or message contents.
	Logf func(format string, args ...any)
	// OnCall, when set, sees every tool call after the checks (tests).
	OnCall func(CallInfo)

	cache *mcp.SchemaCache
}

// CallInfo is what OnCall reports.
type CallInfo struct {
	Tool     string
	Identity Identity
	// TokenReceived: the call carried a provider token for Provider.
	TokenReceived bool
	Err           error
}

// Handler is the Streamable HTTP endpoint, without authentication (wrap it
// with Guard).
func (m *MailServer) Handler() http.Handler {
	if m.cache == nil {
		m.cache = mcp.NewSchemaCache()
	}
	return mcp.NewStreamableHTTPHandler(func(r *http.Request) *mcp.Server {
		return m.server(IdentityFrom(r.Context()))
	}, &mcp.StreamableHTTPOptions{Stateless: true, JSONResponse: true})
}

func (m *MailServer) logf(format string, args ...any) {
	if m.Logf != nil {
		m.Logf(format, args...)
	}
}

// SearchInput, ReadInput and DraftInput are the tools' arguments.
type SearchInput struct {
	Query string `json:"query" jsonschema:"Gmail search query, as in the Gmail search box, for example 'from:alice newer_than:7d is:unread'"`
	Max   int    `json:"max,omitempty" jsonschema:"Most messages to return, 1 to 25 (default 10)"`
}

type ReadInput struct {
	ID string `json:"id" jsonschema:"The message ID, from search_messages"`
}

type DraftInput struct {
	To      string `json:"to" jsonschema:"Recipients, comma-separated email addresses"`
	Subject string `json:"subject" jsonschema:"The subject line"`
	Body    string `json:"body" jsonschema:"The plain-text body"`
}

// SearchOutput is search_messages' structured result.
type SearchOutput struct {
	Messages []MessageSummary `json:"messages"`
}

var messageID = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

func (m *MailServer) server(id *Identity) *mcp.Server {
	s := mcp.NewServer(&mcp.Implementation{Name: "mail-mcp", Title: "Mail (Gmail)", Version: "1.0.0"},
		&mcp.ServerOptions{SchemaCache: m.cache})
	no, yes := false, true

	mcp.AddTool(s, &mcp.Tool{Name: "search_messages", Title: "Search mail",
		Description: "Searches the caller's Gmail mailbox with a Gmail search query and returns, for each message, " +
			"its ID, sender, subject, date and a short snippet.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true, OpenWorldHint: &yes}},
		func(ctx context.Context, req *mcp.CallToolRequest, in SearchInput) (*mcp.CallToolResult, SearchOutput, error) {
			var out SearchOutput
			err := m.call(ctx, "search_messages", req, id, func(token string) error {
				max := in.Max
				switch {
				case max == 0:
					max = 10
				case max < 1 || max > 25:
					return errors.New("max must be between 1 and 25")
				}
				msgs, err := m.Gmail.Search(ctx, token, in.Query, max)
				out.Messages = msgs
				if out.Messages == nil {
					out.Messages = []MessageSummary{}
				}
				return err
			})
			return nil, out, err
		})

	mcp.AddTool(s, &mcp.Tool{Name: "read_message", Title: "Read mail",
		Description: "Reads one message from the caller's Gmail mailbox: its main headers and its plain-text body " +
			"(long bodies are cut; attachments are listed by name, not read).",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true, OpenWorldHint: &yes}},
		func(ctx context.Context, req *mcp.CallToolRequest, in ReadInput) (*mcp.CallToolResult, *Message, error) {
			var out *Message
			err := m.call(ctx, "read_message", req, id, func(token string) error {
				if !messageID.MatchString(in.ID) {
					return errors.New("id must be a Gmail message ID from search_messages")
				}
				max := m.MaxBodyChars
				if max == 0 {
					max = 20000
				}
				var err error
				out, err = m.Gmail.Read(ctx, token, in.ID, max)
				return err
			})
			return nil, out, err
		})

	mcp.AddTool(s, &mcp.Tool{Name: "create_draft", Title: "Create draft",
		Description: "Saves a plain-text email draft in the caller's Gmail mailbox. It does not send it: the person " +
			"reviews and sends drafts in Gmail.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: false, DestructiveHint: &no, IdempotentHint: false, OpenWorldHint: &yes}},
		func(ctx context.Context, req *mcp.CallToolRequest, in DraftInput) (*mcp.CallToolResult, *Draft, error) {
			var out *Draft
			err := m.call(ctx, "create_draft", req, id, func(token string) error {
				to, err := mail.ParseAddressList(in.To)
				if err != nil || len(to) == 0 || len(to) > 50 {
					return errors.New("to must be 1 to 50 comma-separated email addresses")
				}
				if strings.TrimSpace(in.Subject) == "" || len(in.Subject) > 998 {
					return errors.New("subject is required (one line, at most 998 characters)")
				}
				if len(in.Body) > 1<<20 {
					return errors.New("body is too long (at most 1 MiB)")
				}
				out, err = m.Gmail.CreateDraft(ctx, token, to, in.Subject, in.Body)
				return err
			})
			return nil, out, err
		})
	return s
}

// call runs the checks every mailbox tool needs, then fn with the person's
// Google access token. An error becomes a tool error (isError), which the
// model sees as text.
func (m *MailServer) call(ctx context.Context, tool string, req *mcp.CallToolRequest, id *Identity, fn func(token string) error) error {
	start := time.Now()
	var h http.Header
	if req.Extra != nil {
		h = req.Extra.Header
	}
	token := h.Get(headerProviderToken)
	err := m.authorize(id, h)
	if err == nil {
		err = fn(token)
	}
	info := CallInfo{Tool: tool, TokenReceived: token != "", Err: err}
	sub, jti := "-", "-"
	if id != nil {
		info.Identity = *id
		sub, jti = id.Subject, id.ID
	}
	outcome := "ok"
	if err != nil {
		outcome = "error: " + err.Error()
	}
	m.logf("tool=%s sub=%s jti=%s provider=%q took=%s %s", tool, sub, jti, h.Get(headerProvider),
		time.Since(start).Round(time.Millisecond), outcome)
	if m.OnCall != nil {
		m.OnCall(info)
	}
	return err
}

// authorize checks what the gateway sent with a mailbox tool call. The
// Guard already verified the identity token and refused provider headers
// that don't match its provider claim.
func (m *MailServer) authorize(id *Identity, h http.Header) error {
	if id == nil {
		// Unreachable behind Guard (tools/call needs a token), but never act
		// without one.
		return errors.New("this server only accepts calls through Open MCP Gateway")
	}
	if id.PrincipalType != "user" {
		return errors.New("this tool reads a person's mailbox; service accounts can't use it")
	}
	token := h.Get(headerProviderToken)
	if token == "" || id.Provider == "" {
		return fmt.Errorf("no Google account token came with this call. The gateway sends one only when the server's "+
			"user authorization requirement is set: ask the team that registered this server to set the requirement to "+
			"the %q auth provider with the Gmail scopes (gmail.readonly; gmail.compose for create_draft)", m.Provider)
	}
	if id.Provider != m.Provider || h.Get(headerProvider) != m.Provider {
		return fmt.Errorf("the gateway sent a token for the %q auth provider, but this server works with %q: "+
			"the server's user authorization requirement names the wrong provider", id.Provider, m.Provider)
	}
	return nil
}

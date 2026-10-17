package oauthmeta

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"strings"
	"time"
)

// Dynamic Client Registration (RFC 7591) and the health probe.

// RegistrationRequest is the client the gateway registers.
type RegistrationRequest struct {
	ClientName              string   `json:"client_name"`
	RedirectURIs            []string `json:"redirect_uris"`
	GrantTypes              []string `json:"grant_types"`
	ResponseTypes           []string `json:"response_types"`
	TokenEndpointAuthMethod string   `json:"token_endpoint_auth_method"`
	Scope                   string   `json:"scope,omitempty"`
	ClientURI               string   `json:"client_uri,omitempty"`
}

// Registration is the authorization server's answer. ClientSecret is a
// secret: seal it, never log it.
type Registration struct {
	ClientID, ClientSecret  string
	TokenEndpointAuthMethod string
	// SecretExpiresAt is nil when the secret doesn't expire.
	SecretExpiresAt *time.Time
}

// RegistrationError is a refused or failed registration: Code is an OAuth
// error code or one of ours (never the server's own words).
type RegistrationError struct {
	Code   string
	Status int
}

func (e *RegistrationError) Error() string { return "client registration failed: " + e.Code }

var codeRE = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,64}$`)

// Register registers a client at endpoint (guarded like discovery).
func (f *Fetcher) Register(ctx context.Context, endpoint string, req RegistrationRequest) (Registration, error) {
	s := f.newSession(nil)
	defer s.done()
	body, _ := json.Marshal(req)
	a := s.send(ctx, "registration", http.MethodPost, endpoint, body, http.Header{"Content-Type": {"application/json"}, "Accept": {"application/json"}})
	switch {
	case a.result == StepNotAllowed:
		return Registration{}, &RegistrationError{Code: "not_allowed"}
	case a.result == StepTimeout:
		return Registration{}, &RegistrationError{Code: "timeout"}
	case a.status == 0:
		return Registration{}, &RegistrationError{Code: "unreachable"}
	}
	var m map[string]json.RawMessage
	if json.Unmarshal(a.body, &m) != nil {
		return Registration{}, &RegistrationError{Code: "invalid_response", Status: a.status}
	}
	str := func(k string) string {
		var v string
		_ = json.Unmarshal(m[k], &v)
		return v
	}
	if a.status != http.StatusCreated && a.status != http.StatusOK {
		code := "http_error"
		if c := str("error"); codeRE.MatchString(c) {
			code = strings.ToLower(c)
		}
		return Registration{}, &RegistrationError{Code: code, Status: a.status}
	}
	out := Registration{ClientID: str("client_id"), ClientSecret: str("client_secret"), TokenEndpointAuthMethod: str("token_endpoint_auth_method")}
	if out.ClientID == "" || len(out.ClientID) > 500 || len(out.ClientSecret) > 2000 || strings.ContainsAny(out.ClientID+out.ClientSecret, "\r\n") {
		return Registration{}, &RegistrationError{Code: "invalid_response", Status: a.status}
	}
	if out.TokenEndpointAuthMethod == "" {
		out.TokenEndpointAuthMethod = req.TokenEndpointAuthMethod
	}
	var exp int64
	if raw, ok := m["client_secret_expires_at"]; ok && json.Unmarshal(raw, &exp) == nil && exp > 0 {
		t := time.Unix(exp, 0).UTC()
		out.SecretExpiresAt = &t
	}
	return out, nil
}

// ProbeResult is the health probe's outcome: the HTTP status (0 when no
// answer came), the Bearer challenge of a 401, and how the request failed
// (Result is a Step result).
type ProbeResult struct {
	Status    int
	Challenge *Challenge
	Result    string
	Latency   time.Duration
}

// Probe sends one unauthenticated initialize (health checks of oauth
// servers use nobody's token).
func (f *Fetcher) Probe(ctx context.Context, rawURL string) ProbeResult {
	var res Result
	s := f.newSession(&res)
	defer s.done()
	a := s.send(ctx, purposeMCP, http.MethodPost, rawURL, InitializeBody(), mcpHeaders())
	out := ProbeResult{Status: a.status, Result: a.result}
	if len(res.Steps) == 1 {
		out.Latency = res.Steps[0].Latency
	}
	if a.status == http.StatusUnauthorized {
		out.Challenge = Bearer(a.header.Values("WWW-Authenticate"))
	}
	return out
}

// ErrNoBearer: a 401 without a Bearer challenge.
var ErrNoBearer = errors.New("oauthmeta: no Bearer challenge")

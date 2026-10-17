package oauthmeta

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/ncecere/open-mcp-portal/internal/buildinfo"
	"github.com/ncecere/open-mcp-portal/internal/upstream"
)

// Bounds of discovery (docs/phase7-upstream-oauth.md, "Setup by discovery").
const (
	RequestTimeout = 5 * time.Second
	MaxBody        = 64 << 10
	MaxRequests    = 8
	// ProtocolVersion is sent with the probe's initialize.
	ProtocolVersion = "2025-11-25"
)

// Outcomes.
const (
	OutcomeNoAuth      = "no_auth"
	OutcomeOAuth       = "oauth"
	OutcomeOtherAuth   = "other_auth"
	OutcomeUnreachable = "unreachable"
)

// Problem codes (DetectAuthProblem.code).
const (
	ProblemNoResourceMetadata    = "no_resource_metadata"
	ProblemResourceMismatch      = "resource_mismatch"
	ProblemSeveralServers        = "several_authorization_servers"
	ProblemMetadataNotFound      = "metadata_not_found"
	ProblemInvalidMetadata       = "invalid_metadata"
	ProblemIssuerMismatch        = "issuer_mismatch"
	ProblemNoPKCE                = "no_pkce"
	ProblemEndpointNotAllowed    = "endpoint_not_allowed"
	ProblemNoRegistration        = "no_registration"
	ProblemNoRefresh             = "no_refresh"
	purposeMCP                   = "mcp"
	purposeResourceMetadata      = "resource_metadata"
	purposeAuthorizationMetadata = "authorization_server_metadata"
)

// Step results.
const (
	StepOK         = "ok"
	StepChallenge  = "challenge"
	StepNotFound   = "not_found"
	StepError      = "error"
	StepNotAllowed = "not_allowed"
	StepTimeout    = "timeout"
)

// Step is one request discovery made.
type Step struct {
	Purpose, URL string
	HTTPStatus   int // 0: no answer
	Result       string
	Latency      time.Duration
}

// Problem is something discovery found wrong (Blocking: oauth mode can't
// be used) or worth knowing.
type Problem struct {
	Code     string
	Blocking bool
	Message  string
}

// ResourceMetadata is RFC 9728 protected-resource metadata.
type ResourceMetadata struct {
	URL, Resource        string
	ResourceName         string
	AuthorizationServers []string
	ScopesSupported      []string
	BearerMethods        []string
}

// ServerMetadata is RFC 8414 / OpenID Connect authorization server metadata.
type ServerMetadata struct {
	Identifier                                    string // as the resource metadata lists it
	Issuer, MetadataURL                           string
	AuthorizationEndpoint, TokenEndpoint          string
	RevocationEndpoint, RegistrationEndpoint      string // "": none
	ScopesSupported, CodeChallengeMethods         []string
	TokenEndpointAuthMethods, GrantTypesSupported []string
	IssParameterSupported, CIMDSupported          bool
}

// Result is what discovery found.
type Result struct {
	URL        string
	Outcome    string
	HTTPStatus int
	Challenge  *Challenge
	Resource   *ResourceMetadata
	// SuggestedURL is the metadata's resource when it differs from URL only
	// by a trailing slash or the case of scheme and host.
	SuggestedURL string
	Server       *ServerMetadata
	// FoundByFallback: the resource metadata came from a well-known URI,
	// not from the challenge's resource_metadata.
	FoundByFallback bool
	Problems        []Problem
	Steps           []Step
	SuggestedScopes []string
}

// Blocking returns the blocking problems.
func (r Result) Blocking() []Problem {
	var out []Problem
	for _, p := range r.Problems {
		if p.Blocking {
			out = append(out, p)
		}
	}
	return out
}

// Usable reports whether oauth mode can be configured from this result.
func (r Result) Usable() bool {
	return r.Outcome == OutcomeOAuth && r.Resource != nil && r.Server != nil && len(r.Blocking()) == 0
}

func (r *Result) problem(code string, blocking bool, msg string) {
	r.Problems = append(r.Problems, Problem{Code: code, Blocking: blocking, Message: msg})
}

// Fetcher makes discovery's requests through the guard of Client.
type Fetcher struct {
	Client *upstream.Client
	// TLS is the TLS configuration (tests: to trust a test server).
	TLS *tls.Config
	// Timeout per request (RequestTimeout when 0).
	Timeout time.Duration
	// MaxRequests bounds one discovery (MaxRequests when 0).
	MaxRequests int
}

var errRedirect = errors.New("oauthmeta: redirects are not followed")

// session is one discovery's HTTP client and request budget.
type session struct {
	f    *Fetcher
	hc   *http.Client
	done func()
	left int
	res  *Result
}

func (f *Fetcher) timeout() time.Duration {
	if f.Timeout > 0 {
		return f.Timeout
	}
	return RequestTimeout
}

func (f *Fetcher) newSession(res *Result) *session {
	max := f.MaxRequests
	if max <= 0 {
		max = MaxRequests
	}
	tr := &http.Transport{
		Proxy:                 nil, // never HTTP(S)_PROXY: the dialer must see the real destination
		DialContext:           f.Client.GuardedDial(),
		TLSClientConfig:       f.TLS,
		ForceAttemptHTTP2:     true,
		TLSHandshakeTimeout:   f.timeout(),
		ResponseHeaderTimeout: f.timeout(),
		MaxIdleConns:          4,
		IdleConnTimeout:       10 * time.Second,
	}
	hc := &http.Client{Transport: tr, Timeout: f.timeout(), CheckRedirect: func(*http.Request, []*http.Request) error { return errRedirect }}
	return &session{f: f, hc: hc, done: tr.CloseIdleConnections, left: max, res: res}
}

// answer is one bounded response.
type answer struct {
	status int
	header http.Header
	body   []byte
	result string // a Step result when the request failed or was refused
}

// send makes one guarded request and records it as a step.
func (s *session) send(ctx context.Context, purpose, method, rawURL string, body []byte, header http.Header) answer {
	step := Step{Purpose: purpose, URL: rawURL}
	if s.left <= 0 {
		return answer{result: StepError}
	}
	s.left--
	start := time.Now()
	defer func() {
		step.Latency = time.Since(start)
		if s.res != nil {
			s.res.Steps = append(s.res.Steps, step)
		}
	}()
	if _, err := s.f.Client.NormalizeURL(ctx, rawURL); err != nil {
		step.Result = StepNotAllowed
		return answer{result: StepNotAllowed}
	}
	ctx, cancel := context.WithTimeout(ctx, s.f.timeout())
	defer cancel()
	var rdr io.Reader
	if body != nil {
		rdr = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, rawURL, rdr)
	if err != nil {
		step.Result = StepError
		return answer{result: StepError}
	}
	for k, v := range header {
		req.Header[k] = v
	}
	req.Header.Set("User-Agent", "open-mcp-portal/"+buildinfo.Version)
	res, err := s.hc.Do(req)
	if err != nil {
		switch {
		case errors.Is(err, upstream.ErrBlockedAddress) || errors.Is(err, errRedirect):
			step.Result = StepNotAllowed
		case errors.Is(err, context.DeadlineExceeded) || ctx.Err() != nil:
			step.Result = StepTimeout
		default:
			step.Result = StepError
		}
		return answer{result: step.Result}
	}
	defer res.Body.Close()
	step.HTTPStatus = res.StatusCode
	b, err := io.ReadAll(io.LimitReader(res.Body, MaxBody+1))
	if err != nil || len(b) > MaxBody {
		step.Result = StepError
		return answer{status: res.StatusCode, header: res.Header, result: StepError}
	}
	switch {
	case res.StatusCode == http.StatusUnauthorized || res.StatusCode == http.StatusForbidden:
		step.Result = StepChallenge
	case res.StatusCode == http.StatusNotFound:
		step.Result = StepNotFound
	case res.StatusCode >= 200 && res.StatusCode < 300:
		step.Result = StepOK
	default:
		step.Result = StepError
	}
	return answer{status: res.StatusCode, header: res.Header, body: b, result: step.Result}
}

// InitializeBody is the probe's request: an MCP initialize without
// credentials.
func InitializeBody() []byte {
	b, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "initialize", "params": map[string]any{
		"protocolVersion": ProtocolVersion, "capabilities": map[string]any{},
		"clientInfo": map[string]any{"name": "open-mcp-portal", "version": buildinfo.Version}}})
	return b
}

func mcpHeaders() http.Header {
	return http.Header{"Content-Type": {"application/json"}, "Accept": {"application/json, text/event-stream"},
		"Mcp-Protocol-Version": {ProtocolVersion}}
}

// Options choose among what discovery finds.
type Options struct {
	// AuthorizationServer is the one to use when the resource metadata lists
	// several ("": the first).
	AuthorizationServer string
}

// Discover probes an MCP server URL (already checked by the caller) and
// follows the MCP authorization spec as a client would. It never fails:
// what went wrong is in the result's outcome, problems and steps.
func (f *Fetcher) Discover(ctx context.Context, rawURL string, opt Options) Result {
	res := Result{URL: rawURL, Problems: []Problem{}, Steps: []Step{}, SuggestedScopes: []string{}}
	s := f.newSession(&res)
	defer s.done()

	a := s.send(ctx, purposeMCP, http.MethodPost, rawURL, InitializeBody(), mcpHeaders())
	res.HTTPStatus = a.status
	switch {
	case a.status >= 200 && a.status < 300:
		res.Outcome = OutcomeNoAuth
		return res
	case a.status == http.StatusUnauthorized:
	case a.status == http.StatusForbidden:
		res.Outcome = OutcomeOtherAuth
		return res
	default:
		res.Outcome = OutcomeUnreachable
		return res
	}
	ch := Bearer(a.header.Values("WWW-Authenticate"))
	if ch == nil {
		res.Outcome = OutcomeOtherAuth
		return res
	}
	res.Challenge = ch
	prm, ok := s.resourceMetadata(ctx, rawURL, ch)
	if !ok {
		res.Outcome = OutcomeOtherAuth
		return res
	}
	res.Outcome = OutcomeOAuth
	res.Resource = prm
	res.SuggestedScopes = ch.Scopes()
	if len(res.SuggestedScopes) == 0 {
		res.SuggestedScopes = append([]string{}, prm.ScopesSupported...)
	}
	exact, near := ResourceMatches(prm.Resource, rawURL)
	if !exact {
		if near {
			res.SuggestedURL = prm.Resource
			res.problem(ProblemResourceMismatch, true, fmt.Sprintf("The server identifies itself as %s; register it with exactly that URL.", prm.Resource))
		} else {
			res.problem(ProblemResourceMismatch, true, fmt.Sprintf("The protected-resource metadata is for %s, not this URL, so the portal can't use it.", prm.Resource))
			return res
		}
	}
	as := prm.AuthorizationServers[0]
	if len(prm.AuthorizationServers) > 1 {
		res.problem(ProblemSeveralServers, false, fmt.Sprintf("The server lists %d authorization servers; %s is used unless you choose another.",
			len(prm.AuthorizationServers), as))
	}
	if opt.AuthorizationServer != "" {
		if !contains(prm.AuthorizationServers, opt.AuthorizationServer) {
			res.problem(ProblemInvalidMetadata, true, "The chosen authorization server isn't one the server lists.")
			return res
		}
		as = opt.AuthorizationServer
	}
	s.authorizationServer(ctx, as)
	return res
}

// resourceMetadata reads the protected-resource metadata: from the
// challenge's resource_metadata, else the well-known URIs (path form, then
// root form).
func (s *session) resourceMetadata(ctx context.Context, rawURL string, ch *Challenge) (*ResourceMetadata, bool) {
	var candidates []string
	if rm := ch.ResourceMetadata(); rm != "" {
		candidates = []string{rm}
	} else {
		candidates = WellKnownResourceURLs(rawURL)
	}
	for _, c := range candidates {
		a := s.send(ctx, purposeResourceMetadata, http.MethodGet, c, nil, http.Header{"Accept": {"application/json"}})
		switch {
		case a.result == StepNotAllowed:
			s.res.problem(ProblemEndpointNotAllowed, true, "The protected-resource metadata URL "+c+" is refused by the address rules.")
			continue
		case a.status != http.StatusOK:
			continue
		}
		prm, err := parseResourceMetadata(a.body)
		if err != nil {
			s.res.problem(ProblemInvalidMetadata, true, "The protected-resource metadata at "+c+" is invalid: "+err.Error())
			return nil, false
		}
		prm.URL = c
		if ch.ResourceMetadata() == "" {
			s.res.FoundByFallback = true
			s.res.problem(ProblemNoResourceMetadata, false, "The 401 challenge has no resource_metadata; the metadata was found at "+c+".")
		}
		return prm, true
	}
	s.res.problem(ProblemMetadataNotFound, true, "No protected-resource metadata was found for this URL.")
	return nil, false
}

// WellKnownResourceURLs are RFC 9728's URIs for a resource: the path form
// (/.well-known/oauth-protected-resource{path}), then the root form.
func WellKnownResourceURLs(rawURL string) []string {
	u, err := url.Parse(rawURL)
	if err != nil {
		return nil
	}
	origin := u.Scheme + "://" + u.Host
	root := origin + "/.well-known/oauth-protected-resource"
	if u.Path == "" || u.Path == "/" {
		return []string{root}
	}
	return []string{root + u.Path, root}
}

// AuthorizationServerMetadataURLs are the URLs the MCP spec reads an
// authorization server's metadata from, in order: for an issuer with a
// path, RFC 8414 path insertion, OpenID Connect path insertion, then OpenID
// Connect path appending; without a path, the two root forms.
func AuthorizationServerMetadataURLs(issuer string) []string {
	u, err := url.Parse(issuer)
	if err != nil || u.Host == "" {
		return nil
	}
	origin := u.Scheme + "://" + u.Host
	path := strings.TrimSuffix(u.Path, "/")
	if path == "" {
		return []string{origin + "/.well-known/oauth-authorization-server", origin + "/.well-known/openid-configuration"}
	}
	return []string{origin + "/.well-known/oauth-authorization-server" + path, origin + "/.well-known/openid-configuration" + path,
		origin + path + "/.well-known/openid-configuration"}
}

func (s *session) authorizationServer(ctx context.Context, identifier string) {
	res := s.res
	if _, err := s.f.Client.NormalizeURL(ctx, identifier); err != nil {
		res.problem(ProblemEndpointNotAllowed, true, "The authorization server "+identifier+" isn't an https URL the portal may reach.")
		return
	}
	for _, c := range AuthorizationServerMetadataURLs(identifier) {
		a := s.send(ctx, purposeAuthorizationMetadata, http.MethodGet, c, nil, http.Header{"Accept": {"application/json"}})
		if a.status != http.StatusOK {
			continue
		}
		md, err := parseServerMetadata(a.body)
		if err != nil {
			res.problem(ProblemInvalidMetadata, true, "The authorization server metadata at "+c+" is invalid: "+err.Error())
			return
		}
		md.Identifier, md.MetadataURL = identifier, c
		res.Server = md
		s.checkServer(ctx, md)
		return
	}
	res.problem(ProblemMetadataNotFound, true, "No authorization server metadata was found for "+identifier+".")
}

func (s *session) checkServer(ctx context.Context, md *ServerMetadata) {
	res := s.res
	if md.Issuer != md.Identifier {
		res.problem(ProblemIssuerMismatch, true, fmt.Sprintf("The metadata's issuer %s differs from the authorization server %s.", md.Issuer, md.Identifier))
	}
	if !contains(md.CodeChallengeMethods, "S256") {
		res.problem(ProblemNoPKCE, true, "The authorization server doesn't list PKCE S256, which the MCP spec requires.")
	}
	for _, e := range []struct{ name, url string }{{"authorization", md.AuthorizationEndpoint}, {"token", md.TokenEndpoint},
		{"revocation", md.RevocationEndpoint}, {"registration", md.RegistrationEndpoint}} {
		if e.url == "" {
			continue
		}
		if _, err := s.f.Client.NormalizeURL(ctx, e.url); err != nil {
			res.problem(ProblemEndpointNotAllowed, true, fmt.Sprintf("The %s endpoint %s isn't an https URL the portal may reach.", e.name, e.url))
		}
	}
	if md.RegistrationEndpoint == "" {
		res.problem(ProblemNoRegistration, false, "The authorization server offers no automatic registration: create an OAuth client there and enter it.")
	}
	if len(md.GrantTypesSupported) > 0 && !contains(md.GrantTypesSupported, "refresh_token") {
		res.problem(ProblemNoRefresh, false, "The authorization server doesn't list refresh_token: people sign in again when their token expires.")
	}
}

// ResourceMatches compares a metadata resource with a server URL: exact
// (equal strings), or near (equal but for a trailing slash or the case of
// the scheme and host).
func ResourceMatches(resource, rawURL string) (exact, near bool) {
	if resource == rawURL {
		return true, true
	}
	a, err1 := url.Parse(resource)
	b, err2 := url.Parse(rawURL)
	if err1 != nil || err2 != nil || a.RawQuery != "" || a.Fragment != "" || a.User != nil {
		return false, false
	}
	norm := func(u *url.URL) string {
		return strings.ToLower(u.Scheme) + "://" + strings.ToLower(u.Host) + strings.TrimSuffix(u.EscapedPath(), "/")
	}
	return false, norm(a) == norm(b)
}

type rawResourceMetadata struct {
	Resource             string   `json:"resource"`
	ResourceName         string   `json:"resource_name"`
	AuthorizationServers []string `json:"authorization_servers"`
	ScopesSupported      []string `json:"scopes_supported"`
	BearerMethods        []string `json:"bearer_methods_supported"`
}

func parseResourceMetadata(b []byte) (*ResourceMetadata, error) {
	var m rawResourceMetadata
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, errors.New("not a JSON object")
	}
	if m.Resource == "" {
		return nil, errors.New("resource is missing")
	}
	var servers []string
	for _, s := range m.AuthorizationServers {
		if s != "" && len(s) <= 500 && !contains(servers, s) {
			servers = append(servers, s)
		}
	}
	if len(servers) == 0 {
		return nil, errors.New("authorization_servers is missing")
	}
	return &ResourceMetadata{Resource: m.Resource, ResourceName: clip(m.ResourceName, 200), AuthorizationServers: first(servers, 20),
		ScopesSupported: first(cleanScopes(m.ScopesSupported), 200), BearerMethods: first(m.BearerMethods, 10)}, nil
}

type rawServerMetadata struct {
	Issuer                   string   `json:"issuer"`
	AuthorizationEndpoint    string   `json:"authorization_endpoint"`
	TokenEndpoint            string   `json:"token_endpoint"`
	RevocationEndpoint       string   `json:"revocation_endpoint"`
	RegistrationEndpoint     string   `json:"registration_endpoint"`
	ScopesSupported          []string `json:"scopes_supported"`
	CodeChallengeMethods     []string `json:"code_challenge_methods_supported"`
	TokenEndpointAuthMethods []string `json:"token_endpoint_auth_methods_supported"`
	GrantTypesSupported      []string `json:"grant_types_supported"`
	IssParameterSupported    bool     `json:"authorization_response_iss_parameter_supported"`
	CIMDSupported            bool     `json:"client_id_metadata_document_supported"`
}

func parseServerMetadata(b []byte) (*ServerMetadata, error) {
	var m rawServerMetadata
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, errors.New("not a JSON object")
	}
	switch {
	case m.Issuer == "":
		return nil, errors.New("issuer is missing")
	case m.AuthorizationEndpoint == "":
		return nil, errors.New("authorization_endpoint is missing")
	case m.TokenEndpoint == "":
		return nil, errors.New("token_endpoint is missing")
	}
	for _, v := range []string{m.Issuer, m.AuthorizationEndpoint, m.TokenEndpoint, m.RevocationEndpoint, m.RegistrationEndpoint} {
		if len(v) > 500 {
			return nil, errors.New("a URL is longer than 500 characters")
		}
	}
	return &ServerMetadata{Issuer: m.Issuer, AuthorizationEndpoint: m.AuthorizationEndpoint, TokenEndpoint: m.TokenEndpoint,
		RevocationEndpoint: m.RevocationEndpoint, RegistrationEndpoint: m.RegistrationEndpoint,
		ScopesSupported: first(cleanScopes(m.ScopesSupported), 200), CodeChallengeMethods: first(m.CodeChallengeMethods, 10),
		TokenEndpointAuthMethods: first(m.TokenEndpointAuthMethods, 10), GrantTypesSupported: first(m.GrantTypesSupported, 20),
		IssParameterSupported: m.IssParameterSupported, CIMDSupported: m.CIMDSupported}, nil
}

// cleanScopes keeps scope tokens (RFC 6749 characters), without duplicates.
func cleanScopes(in []string) []string {
	out := []string{}
	for _, s := range in {
		if s == "" || len(s) > 200 || strings.ContainsAny(s, " \"\\") || contains(out, s) {
			continue
		}
		ok := true
		for i := 0; i < len(s); i++ {
			if s[i] < 0x21 || s[i] > 0x7e {
				ok = false
			}
		}
		if ok {
			out = append(out, s)
		}
	}
	return out
}

func first(s []string, n int) []string {
	if s == nil {
		return []string{}
	}
	if len(s) > n {
		return s[:n]
	}
	return s
}

func clip(s string, n int) string {
	r := []rune(s)
	if len(r) > n {
		return string(r[:n])
	}
	return s
}

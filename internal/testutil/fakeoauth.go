package testutil

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"html/template"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"
)

// FakeOAuth is a third-party OAuth 2.0 provider (the authorization code
// grant with optional PKCE S256, refresh with rotation, RFC 7009
// revocation and a userinfo endpoint) for tests of delegated credentials
// and for local development (cmd/fakeoauth). Its knobs make it misbehave the
// ways real providers do: narrower grants, no scope in the answer,
// form-encoded or Slack-style answers, refused or failing refreshes, a
// failing userinfo endpoint.
//
// Endpoints: GET /authorize (a page with Approve and Deny, or straight back
// with AutoApprove), POST /token, POST /revoke, GET /userinfo.
type FakeOAuth struct {
	*httptest.Server

	// ClientID and ClientSecret are the registered client's (the secret may
	// come in the form or as HTTP Basic).
	ClientID, ClientSecret string

	mu sync.Mutex
	// AutoApprove sends the browser straight back with a code (tests);
	// otherwise /authorize shows a page with Approve and Deny.
	AutoApprove bool
	// Deny answers every authorization with error=access_denied.
	Deny bool
	// AccessTTL is an access token's life (0: no expires_in).
	AccessTTL time.Duration
	// IssueRefresh issues refresh tokens; RotateRefresh replaces one on
	// every use (the old one then fails with invalid_grant).
	IssueRefresh, RotateRefresh bool
	// GrantScopes, when set, is what every authorization grants (else what
	// was asked); OmitScope leaves scope out of token answers.
	GrantScopes []string
	OmitScope   bool
	// FailRefresh: "" (work), "invalid_grant" (400) or "unavailable" (503).
	FailRefresh string
	// FormEncoded answers the token endpoint form-encoded (like GitHub
	// without Accept: application/json); Slack answers like Slack's
	// oauth.v2.access with the user token under authed_user.
	FormEncoded, Slack bool
	// FailUserinfo makes /userinfo answer 500.
	FailUserinfo bool
	// RefreshDelay delays refresh answers (concurrency tests).
	RefreshDelay time.Duration
	// Login is what /userinfo calls the account.
	Login string

	codes    map[string]fakeCode
	access   map[string][]string // token -> scopes
	refresh  map[string][]string
	revoked  []string
	issued   []string
	issuedRT []string
	lastAuth url.Values
	tokens   int
	refreshs int
}

type fakeCode struct {
	redirectURI, challenge string
	scopes                 []string
}

// NewFakeOAuth starts a fake provider on a loopback address (closed with the
// test), auto-approving, with one-hour access tokens and rotating refresh
// tokens.
func NewFakeOAuth(t testing.TB) *FakeOAuth {
	f, h := NewFakeOAuthHandler("fake-client", "fake-secret-0123456789")
	f.AutoApprove = true
	f.Server = httptest.NewServer(h)
	t.Cleanup(f.Close)
	return f
}

// NewFakeOAuthHandler builds a fake provider without starting a server.
func NewFakeOAuthHandler(clientID, clientSecret string) (*FakeOAuth, http.Handler) {
	f := &FakeOAuth{ClientID: clientID, ClientSecret: clientSecret, AccessTTL: time.Hour, IssueRefresh: true, RotateRefresh: true,
		Login: "octocat", codes: map[string]fakeCode{}, access: map[string][]string{}, refresh: map[string][]string{}}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /authorize", f.authorize)
	mux.HandleFunc("POST /token", f.token)
	mux.HandleFunc("POST /revoke", f.revoke)
	mux.HandleFunc("GET /userinfo", f.userinfo)
	return f, mux
}

// Set changes knobs under the fake's lock.
func (f *FakeOAuth) Set(change func(f *FakeOAuth)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	change(f)
}

func fakeToken(prefix string) string {
	b := make([]byte, 24)
	_, _ = rand.Read(b)
	return prefix + base64.RawURLEncoding.EncodeToString(b)
}

// LastAuthorize is the query of the latest authorization request.
func (f *FakeOAuth) LastAuthorize() url.Values {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.lastAuth
}

// Issued are the access tokens issued so far, oldest first.
func (f *FakeOAuth) Issued() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.issued...)
}

// IssuedRefresh are the refresh tokens issued so far, oldest first.
func (f *FakeOAuth) IssuedRefresh() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.issuedRT...)
}

// Revoked are the tokens revoked so far.
func (f *FakeOAuth) Revoked() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.revoked...)
}

// TokenRequests and RefreshRequests count token endpoint requests (all, and
// refresh_token grants).
func (f *FakeOAuth) TokenRequests() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.tokens
}

func (f *FakeOAuth) RefreshRequests() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.refreshs
}

// Valid reports whether an access token is current (issued and not
// revoked).
func (f *FakeOAuth) Valid(token string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	_, ok := f.access[token]
	return ok
}

var fakeConsentPage = template.Must(template.New("consent").Parse(`<!doctype html><html lang="en"><head><meta charset="utf-8">
<title>Fake OAuth provider</title><style>body{font-family:system-ui,sans-serif;max-width:32rem;margin:4rem auto;line-height:1.5}
a{display:inline-block;margin-right:1rem;padding:.5rem 1rem;border:1px solid #888;border-radius:.4rem;text-decoration:none}</style></head>
<body><h1>Fake OAuth provider</h1><p>An app ({{.Client}}) asks for these scopes:</p><ul>{{range .Scopes}}<li><code>{{.}}</code></li>{{else}}<li>(none)</li>{{end}}</ul>
<p><a href="{{.Approve}}">Approve</a><a href="{{.Deny}}">Deny</a></p></body></html>`))

func (f *FakeOAuth) authorize(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	redirect, state := q.Get("redirect_uri"), q.Get("state")
	if q.Get("response_type") != "code" || q.Get("client_id") != f.ClientID || redirect == "" || state == "" {
		http.Error(w, "invalid authorization request", http.StatusBadRequest)
		return
	}
	if m := q.Get("code_challenge_method"); q.Get("code_challenge") != "" && m != "S256" {
		http.Error(w, "only S256", http.StatusBadRequest)
		return
	}
	scopes := strings.FieldsFunc(q.Get("scope")+","+q.Get("user_scope"), func(r rune) bool { return r == ' ' || r == ',' })
	f.mu.Lock()
	f.lastAuth = q
	auto, deny := f.AutoApprove, f.Deny
	if f.GrantScopes != nil {
		scopes = append([]string(nil), f.GrantScopes...)
	}
	f.mu.Unlock()
	back, err := url.Parse(redirect)
	if err != nil {
		http.Error(w, "bad redirect_uri", http.StatusBadRequest)
		return
	}
	answer := func(v url.Values) string {
		u := *back
		u.RawQuery = v.Encode()
		return u.String()
	}
	denied := answer(url.Values{"error": {"access_denied"}, "state": {state}})
	if deny || q.Get("decision") == "deny" {
		http.Redirect(w, r, denied, http.StatusFound)
		return
	}
	if !auto && q.Get("decision") != "approve" {
		approve := *r.URL
		aq := approve.Query()
		aq.Set("decision", "approve")
		approve.RawQuery = aq.Encode()
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_ = fakeConsentPage.Execute(w, map[string]any{"Client": f.ClientID, "Scopes": scopes, "Approve": approve.String(), "Deny": denied})
		return
	}
	code := fakeToken("fcode_")
	f.mu.Lock()
	f.codes[code] = fakeCode{redirectURI: redirect, challenge: q.Get("code_challenge"), scopes: scopes}
	f.mu.Unlock()
	http.Redirect(w, r, answer(url.Values{"code": {code}, "state": {state}}), http.StatusFound)
}

func (f *FakeOAuth) clientOK(r *http.Request) bool {
	id, secret, basic := r.BasicAuth()
	if basic {
		id, _ = url.QueryUnescape(id)
		secret, _ = url.QueryUnescape(secret)
	} else {
		id, secret = r.PostForm.Get("client_id"), r.PostForm.Get("client_secret")
	}
	return id == f.ClientID && (f.ClientSecret == "" || secret == f.ClientSecret)
}

func (f *FakeOAuth) fail(w http.ResponseWriter, status int, code string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": code, "error_description": "fake provider says no"})
}

func (f *FakeOAuth) token(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		f.fail(w, http.StatusBadRequest, "invalid_request")
		return
	}
	f.mu.Lock()
	f.tokens++
	f.mu.Unlock()
	if !f.clientOK(r) {
		f.fail(w, http.StatusUnauthorized, "invalid_client")
		return
	}
	var scopes []string
	switch r.PostForm.Get("grant_type") {
	case "authorization_code":
		f.mu.Lock()
		c, ok := f.codes[r.PostForm.Get("code")]
		delete(f.codes, r.PostForm.Get("code"))
		f.mu.Unlock()
		if !ok || c.redirectURI != r.PostForm.Get("redirect_uri") {
			f.fail(w, http.StatusBadRequest, "invalid_grant")
			return
		}
		if c.challenge != "" {
			sum := sha256.Sum256([]byte(r.PostForm.Get("code_verifier")))
			if base64.RawURLEncoding.EncodeToString(sum[:]) != c.challenge {
				f.fail(w, http.StatusBadRequest, "invalid_grant")
				return
			}
		}
		scopes = c.scopes
	case "refresh_token":
		f.mu.Lock()
		f.refreshs++
		delay, failMode := f.RefreshDelay, f.FailRefresh
		f.mu.Unlock()
		time.Sleep(delay)
		switch failMode {
		case "invalid_grant":
			f.fail(w, http.StatusBadRequest, "invalid_grant")
			return
		case "unavailable":
			http.Error(w, "upstream down", http.StatusServiceUnavailable)
			return
		}
		f.mu.Lock()
		rt := r.PostForm.Get("refresh_token")
		s, ok := f.refresh[rt]
		if ok && f.RotateRefresh {
			delete(f.refresh, rt)
		}
		f.mu.Unlock()
		if !ok {
			f.fail(w, http.StatusBadRequest, "invalid_grant")
			return
		}
		scopes = s
	default:
		f.fail(w, http.StatusBadRequest, "unsupported_grant_type")
		return
	}
	f.issue(w, r, scopes)
}

// issue answers with a new access token (and a refresh token).
func (f *FakeOAuth) issue(w http.ResponseWriter, r *http.Request, scopes []string) {
	f.mu.Lock()
	access := fakeToken("fat_")
	f.access[access] = scopes
	f.issued = append(f.issued, access)
	out := map[string]any{"access_token": access, "token_type": "bearer"}
	if f.AccessTTL > 0 {
		out["expires_in"] = int64(f.AccessTTL / time.Second)
	}
	refresh := ""
	if f.IssueRefresh {
		refresh = r.PostForm.Get("refresh_token")
		if refresh == "" || f.RotateRefresh {
			refresh = fakeToken("frt_")
		}
		f.refresh[refresh] = scopes
		f.issuedRT = append(f.issuedRT, refresh)
		out["refresh_token"] = refresh
	}
	if !f.OmitScope {
		out["scope"] = strings.Join(scopes, " ")
	}
	form, slack := f.FormEncoded, f.Slack
	f.mu.Unlock()
	switch {
	case slack:
		if s, ok := out["scope"].(string); ok {
			out["scope"] = strings.ReplaceAll(s, " ", ",")
		}
		out["token_type"] = "user"
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "access_token": "xoxb-bot-not-for-people", "token_type": "bot",
			"authed_user": out})
	case form:
		v := url.Values{}
		for k, val := range out {
			v.Set(k, fmt.Sprint(val))
		}
		w.Header().Set("Content-Type", "application/x-www-form-urlencoded")
		_, _ = w.Write([]byte(v.Encode()))
	default:
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(out)
	}
}

func (f *FakeOAuth) revoke(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil || !f.clientOK(r) {
		f.fail(w, http.StatusUnauthorized, "invalid_client")
		return
	}
	token := r.PostForm.Get("token")
	f.mu.Lock()
	f.revoked = append(f.revoked, token)
	delete(f.access, token)
	if _, ok := f.refresh[token]; ok {
		delete(f.refresh, token)
		// Revoking a refresh token ends its access tokens too (as Google does).
		for k := range f.access {
			delete(f.access, k)
		}
	}
	f.mu.Unlock()
	w.WriteHeader(http.StatusOK)
}

func (f *FakeOAuth) userinfo(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	failing, login := f.FailUserinfo, f.Login
	_, ok := f.access[strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")]
	f.mu.Unlock()
	switch {
	case failing:
		http.Error(w, "userinfo down", http.StatusInternalServerError)
	case !ok:
		w.WriteHeader(http.StatusUnauthorized)
	default:
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"login": login, "sub": "fake-" + login})
	}
}

// TokenDigest is the hex SHA-256 prefix of a token that the fake MCP
// server's provider_token tool reports (never the token itself).
func TokenDigest(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])[:16]
}

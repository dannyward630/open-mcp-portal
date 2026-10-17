package providers

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/ncecere/open-mcp-gateway/internal/apperr"
	"github.com/ncecere/open-mcp-gateway/internal/audit"
	"github.com/ncecere/open-mcp-gateway/internal/auth"
	"github.com/ncecere/open-mcp-gateway/internal/authz"
	"github.com/ncecere/open-mcp-gateway/internal/registry"
	"github.com/ncecere/open-mcp-gateway/internal/store"
	"github.com/ncecere/open-mcp-gateway/internal/store/dbgen"
)

// Connecting an account: consent links (connect requests) that calls answer
// with, the start of an authorization (state + PKCE, bound to the browser
// session), and the provider's redirect back to /connect/callback.

// Lifetimes and limits.
const (
	// RequestTTL is a consent link's life; StateTTL an authorization's.
	RequestTTL = 10 * time.Minute
	StateTTL   = 10 * time.Minute
	// MaxOpenRequests per person; the oldest open one is dropped.
	MaxOpenRequests = 20
	// requestKeep: expired and used requests answer 410 this long, then
	// they are deleted.
	requestKeep = 24 * time.Hour
	// MaxReturnTo bounds a return path.
	MaxReturnTo = 2000
	idBytes     = 32
)

// The web app's result page and its results.
const (
	DonePath            = "/connect/done"
	ResultConnected     = "connected"
	ResultMissingScopes = "missing_scopes"
	ResultDenied        = "denied"
	ResultExpired       = "expired"
	ResultWrongPerson   = "wrong_person"
	ResultError         = "error"
)

var (
	errNoRequest      = apperr.NotFound("connect_request_not_found", "This link was made for someone else, or doesn't exist")
	errRequestExpired = apperr.New(http.StatusGone, "connect_request_expired", "This link expired. Run the tool again.")
	requestIDRE       = regexp.MustCompile(`^[A-Za-z0-9_-]{43}$`)
)

func randomID() string {
	b := make([]byte, idBytes)
	_, _ = rand.Read(b) // crypto/rand never fails on supported platforms
	return base64.RawURLEncoding.EncodeToString(b)
}

// requestDigest is a request ID's stored key: base64url SHA-256.
func requestDigest(id string) string {
	sum := sha256.Sum256([]byte(id))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

func requestAAD(digest string) []byte { return []byte("provider_connect_request:" + digest) }

func stateAAD(digest string) []byte { return []byte("provider_auth_state:" + digest) }

// ConnectURL is the web app's consent page for a request.
func (s *Service) ConnectURL(providerSlug, requestID string) string {
	return s.appURL + "/connect/" + providerSlug + "?request=" + requestID
}

// SafeReturnPath reports whether p is a path on this site: one leading
// slash, no "//", backslash, scheme or control character, at most 2000
// characters. The same holds for every percent-decoding of p (browsers and
// proxies may decode a path once or more), and an encoded slash or
// backslash (%2F, %5C, also double-encoded) is refused outright.
func SafeReturnPath(p string) bool {
	if p == "" || len(p) > MaxReturnTo || !strings.HasPrefix(p, "/") {
		return false
	}
	for layer, i := p, 0; ; i++ {
		lower := strings.ToLower(layer)
		if !strings.HasPrefix(layer, "/") || strings.Contains(layer, "//") || strings.Contains(layer, `\`) ||
			strings.Contains(lower, "%2f") || strings.Contains(lower, "%5c") || !oneLine(layer) {
			return false
		}
		if !strings.Contains(layer, "%") {
			break
		}
		if i == 4 { // more layers of encoding than any legitimate path
			return false
		}
		decoded, err := url.PathUnescape(layer)
		if err != nil {
			return false
		}
		layer = decoded
	}
	u, err := url.Parse(p)
	return err == nil && u.Scheme == "" && u.Host == "" && u.User == nil
}

// union appends to a the scopes of the others it lacks, in order.
func union(lists ...[]string) []string {
	out := []string{}
	for _, l := range lists {
		for _, s := range l {
			if !contains(out, s) {
				out = append(out, s)
			}
		}
	}
	return out
}

// RequestView is a consent link as its person's confirm page sees it.
type RequestView struct {
	Request  dbgen.ProviderConnectRequest
	ID       string
	Provider ProviderRef
	// Granted are the active connection's scopes (empty without one), and
	// AccountLabel its account; RequestScopes what will be asked for.
	Granted, RequestScopes []string
	AccountLabel           *string
	// Reason is why connecting is needed now (ReasonNotConnected,
	// ReasonNeedsReconsent or ReasonMissingScopes), from the connection as
	// it is now; the stored reason only when it already covers the request.
	Reason string
	// The gateway as it is now (or as it was, when deleted).
	GatewaySlug, GatewayName string
	GatewayExists            bool
}

// loadRequest finds a request of a person: 404 for an unknown ID or another
// person's, 410 once expired or used.
func (s *Service) loadRequest(ctx context.Context, q *dbgen.Queries, userID uuid.UUID, id string) (dbgen.ProviderConnectRequest, error) {
	if !requestIDRE.MatchString(id) {
		return dbgen.ProviderConnectRequest{}, errNoRequest
	}
	r, err := q.GetConnectRequest(ctx, requestDigest(id))
	if errors.Is(store.NotFound(err), store.ErrNotFound) || (err == nil && r.UserID != userID) {
		return r, errNoRequest
	} else if err != nil {
		return r, err
	}
	if r.UsedAt != nil || !s.now().Before(r.ExpiresAt) {
		return r, errRequestExpired
	}
	return r, nil
}

// GetRequest returns a consent link for the person it was made for.
func (s *Service) GetRequest(ctx context.Context, a authz.Actor, id string) (RequestView, error) {
	r, err := s.loadRequest(ctx, s.q, a.UserID, id)
	if err != nil {
		return RequestView{}, err
	}
	p, err := getProvider(ctx, s.q, r.ProviderID, false)
	if err != nil {
		return RequestView{}, err
	}
	v := RequestView{Request: r, ID: id, Provider: refOf(p), Granted: []string{}, GatewaySlug: r.GatewaySlug, GatewayName: r.GatewayName,
		Reason: ReasonNotConnected}
	if c, err := s.q.GetProviderConnection(ctx, dbgen.GetProviderConnectionParams{UserID: a.UserID, ProviderID: p.ID}); err == nil {
		v.AccountLabel = c.AccountLabel
		switch {
		case c.Status != StatusActive:
			v.Reason = ReasonNeedsReconsent
		case !Covers(c.Scopes, r.RequiredScopes):
			v.Granted, v.Reason = nonNil(c.Scopes), ReasonMissingScopes
		default:
			// Connected since the link was made: nothing is missing now.
			v.Granted, v.Reason = nonNil(c.Scopes), r.Reason
		}
	} else if !errors.Is(store.NotFound(err), store.ErrNotFound) {
		return RequestView{}, err
	}
	v.RequestScopes = union(v.Granted, r.RequiredScopes)
	if g, err := s.q.GetGateway(ctx, r.GatewayID); err == nil {
		v.GatewaySlug, v.GatewayName, v.GatewayExists = g.Slug, g.Name, true
	} else if !errors.Is(store.NotFound(err), store.ErrNotFound) {
		return RequestView{}, err
	}
	return v, nil
}

func refOf(p dbgen.AuthProvider) ProviderRef {
	return ProviderRef{ID: p.ID, Slug: p.Slug, Name: p.Name, Preset: p.Preset, LogoURL: p.LogoUrl}
}

// StartInput begins connecting. SessionID is the caller's browser session.
type StartInput struct {
	ProviderID uuid.UUID
	RequestID  *string
	ReturnTo   *string
	Scopes     *[]string
	SessionID  string
}

// Started says where to send the browser.
type Started struct {
	AuthorizeURL string
	Scopes       []string
	ExpiresAt    time.Time
}

func pkcePair() (verifier, challenge string) {
	verifier = randomID()
	sum := sha256.Sum256([]byte(verifier))
	return verifier, base64.RawURLEncoding.EncodeToString(sum[:])
}

// Start begins connecting (or reconnecting) an account: it stores a
// single-use state bound to the browser session, the person, the provider
// and the request, and returns the provider's authorization URL, built from
// the stored settings only. The scopes asked for are the union of the
// connection's granted scopes (when active), the caller's scopes (or the
// provider's default scopes when neither scopes nor a request is given) and
// the request's required scopes.
func (s *Service) Start(ctx context.Context, a authz.Actor, in StartInput) (Started, error) {
	if in.SessionID == "" {
		return Started{}, apperr.New(http.StatusForbidden, "session_required", "Connect accounts from the web app")
	}
	if in.ReturnTo != nil && !SafeReturnPath(*in.ReturnTo) {
		return Started{}, apperr.Invalid("invalid_return_to", "returnTo must be a path on this site")
	}
	var extra []string
	if in.Scopes != nil {
		var err error
		if extra, err = normalizeScopes(*in.Scopes); err != nil {
			return Started{}, err
		}
	}
	p, err := getProvider(ctx, s.q, in.ProviderID, false)
	if err != nil {
		return Started{}, err
	}
	if !p.Enabled {
		return Started{}, errNoProvider
	}
	var requestDig *string
	var required []string
	if in.RequestID != nil {
		r, err := s.loadRequest(ctx, s.q, a.UserID, *in.RequestID)
		if err != nil {
			return Started{}, err
		}
		if r.ProviderID != p.ID {
			return Started{}, apperr.Conflict("provider_mismatch", "This link is for another provider")
		}
		d := r.IDDigest
		requestDig, required = &d, nonNil(r.RequiredScopes)
	}
	granted := []string{}
	if c, err := s.q.GetProviderConnection(ctx, dbgen.GetProviderConnectionParams{UserID: a.UserID, ProviderID: p.ID}); err == nil {
		if c.Status == StatusActive {
			granted = nonNil(c.Scopes)
		}
	} else if !errors.Is(store.NotFound(err), store.ErrNotFound) {
		return Started{}, err
	}
	asked := extra
	if in.Scopes == nil && in.RequestID == nil {
		asked = nonNil(p.DefaultScopes)
	}
	scopes := union(granted, asked, required)
	mustHave := union(required, extra)
	if in.Scopes == nil && in.RequestID == nil {
		mustHave = asked
	}
	state := randomID()
	digest := auth.Digest(state)
	expires := s.now().Add(StateTTL)
	var verifier, challenge string
	var verifierCT []byte
	if p.Pkce {
		verifier, challenge = pkcePair()
		if verifierCT, err = s.box.Seal([]byte(verifier), stateAAD(digest)); err != nil {
			return Started{}, err
		}
	}
	err = store.InTx(ctx, s.pool, func(q *dbgen.Queries, _ pgx.Tx) error {
		if _, err := q.DeleteExpiredProviderAuthStates(ctx); err != nil {
			return err
		}
		return q.InsertProviderAuthState(ctx, dbgen.InsertProviderAuthStateParams{
			StateDigest: digest, UserID: a.UserID, SessionDigest: auth.Digest(in.SessionID), ProviderID: p.ID, Scopes: scopes,
			RequiredScopes: mustHave, CodeVerifierCipher: verifierCT, RequestDigest: requestDig, ReturnTo: in.ReturnTo, ExpiresAt: expires,
		})
	})
	if err != nil {
		return Started{}, err
	}
	return Started{AuthorizeURL: s.authorizeURL(p, state, challenge, scopes), Scopes: scopes, ExpiresAt: expires}, nil
}

func normalizeScopes(in []string) ([]string, error) { return registry.NormalizeScopes(in) }

// authorizeURL builds the provider's authorization request from its stored
// settings.
func (s *Service) authorizeURL(p dbgen.AuthProvider, state, challenge string, scopes []string) string {
	q := url.Values{}
	for k, v := range extraParams(p.ExtraAuthorizeParams) {
		q.Set(k, v)
	}
	q.Set("response_type", "code")
	q.Set("client_id", p.ClientID)
	q.Set("redirect_uri", s.RedirectURI())
	q.Set("state", state)
	if len(scopes) > 0 {
		q.Set(p.ScopeParam, strings.Join(scopes, p.ScopeSeparator))
	}
	if challenge != "" {
		q.Set("code_challenge", challenge)
		q.Set("code_challenge_method", "S256")
	}
	return p.AuthorizationUrl + "?" + q.Encode()
}

// CallbackInput is the provider's redirect back, with the browser's
// session (SessionID "" when signed out) and its person.
type CallbackInput struct {
	State, Code, Error  string
	SessionID           string
	User                dbgen.User
	RequestID, ClientIP string
}

// done is the web app's result page.
func done(provider, result string, missing []string) string {
	q := url.Values{"result": {result}}
	if provider != "" {
		q.Set("provider", provider)
	}
	for _, m := range missing {
		q.Add("missing", m)
	}
	return DonePath + "?" + q.Encode()
}

// Callback completes connecting and returns the same-origin path to send
// the browser to: the request's return path, or the web app's result page.
// The state is deleted before anything else is checked; it must belong to
// this browser session (OAuth CSRF), and names the provider whose token
// endpoint receives the code (mix-up). Audited as
// provider_connection.create.
func (s *Service) Callback(ctx context.Context, in CallbackInput) (string, error) {
	if in.State == "" || len(in.State) > 200 {
		return done("", ResultExpired, nil), nil
	}
	st, err := s.q.TakeProviderAuthState(ctx, auth.Digest(in.State))
	if errors.Is(store.NotFound(err), store.ErrNotFound) {
		return done("", ResultExpired, nil), nil
	} else if err != nil {
		return "", err
	}
	p, err := s.q.GetAuthProvider(ctx, st.ProviderID)
	if errors.Is(store.NotFound(err), store.ErrNotFound) {
		return done("", ResultExpired, nil), nil
	} else if err != nil {
		return "", err
	}
	if !s.now().Before(st.ExpiresAt) {
		return done(p.Slug, ResultExpired, nil), nil
	}
	if in.SessionID == "" || auth.Digest(in.SessionID) != st.SessionDigest || in.User.ID != st.UserID || in.User.Status != auth.StatusActive {
		return done(p.Slug, ResultWrongPerson, nil), nil
	}
	if in.Error != "" {
		return done(p.Slug, ResultDenied, nil), nil
	}
	if !p.Enabled || in.Code == "" || len(in.Code) > 4096 {
		return done(p.Slug, ResultError, nil), nil
	}
	verifier := ""
	if st.CodeVerifierCipher != nil {
		b, err := s.box.Open(st.CodeVerifierCipher, stateAAD(st.StateDigest))
		if err != nil {
			return done(p.Slug, ResultError, nil), nil
		}
		verifier = string(b)
	}
	tok, ee := s.exchange(ctx, p, in.Code, verifier)
	if ee != nil {
		s.Log.InfoContext(ctx, "auth provider code exchange failed", "provider", p.Slug, "error", ee.Code, "status", ee.Status)
		return done(p.Slug, ResultError, nil), nil
	}
	granted := nonNil(st.Scopes)
	if tok.HasScope {
		granted = tok.Scopes
	}
	label := s.accountLabel(ctx, p, tok.Access)
	var expires *time.Time
	if tok.ExpiresIn > 0 {
		t := s.now().Add(time.Duration(tok.ExpiresIn) * time.Second)
		expires = &t
	}
	actor := authz.Actor{UserID: in.User.ID, PlatformRole: in.User.PlatformRole, RequestID: in.RequestID, ClientIP: in.ClientIP}
	err = store.InTx(ctx, s.pool, func(q *dbgen.Queries, _ pgx.Tx) error {
		return s.store(ctx, q, actor, in.User, p, st, tok, granted, label, expires)
	})
	if err != nil {
		return "", err
	}
	var missing []string
	for _, m := range st.RequiredScopes {
		if !contains(granted, m) {
			missing = append(missing, m)
		}
	}
	switch {
	case len(missing) > 0:
		return done(p.Slug, ResultMissingScopes, missing), nil
	case st.ReturnTo != nil && SafeReturnPath(*st.ReturnTo):
		return *st.ReturnTo, nil
	}
	return done(p.Slug, ResultConnected, nil), nil
}

// store saves new tokens as the person's active connection and uses up the
// request.
func (s *Service) store(ctx context.Context, q *dbgen.Queries, a authz.Actor, u dbgen.User, p dbgen.AuthProvider, st dbgen.ProviderAuthState,
	tok tokenSet, granted []string, label string, expires *time.Time) error {
	var labelPtr *string
	if label != "" {
		labelPtr = &label
	}
	cur, err := q.LockProviderConnection(ctx, dbgen.LockProviderConnectionParams{UserID: u.ID, ProviderID: p.ID})
	reconnect := err == nil
	if err != nil && !errors.Is(store.NotFound(err), store.ErrNotFound) {
		return err
	}
	id := uuid.New()
	if reconnect {
		id = cur.ID
	}
	access, err := s.seal(id, "access", tok.Access)
	if err != nil {
		return err
	}
	refresh, err := s.seal(id, "refresh", tok.Refresh)
	if err != nil {
		return err
	}
	if reconnect && refresh == nil && cur.Status == StatusActive && cur.RefreshTokenCipher != nil {
		// Some providers (Google) only send a refresh token on first consent;
		// the one held stays valid.
		refresh = cur.RefreshTokenCipher
	}
	var row dbgen.ProviderConnection
	if reconnect {
		row, err = q.ReconnectProviderConnection(ctx, dbgen.ReconnectProviderConnectionParams{ID: id, Scopes: granted, AccountLabel: labelPtr,
			AccessTokenCipher: access, RefreshTokenCipher: refresh, ExpiresAt: expires})
	} else {
		row, err = q.InsertProviderConnection(ctx, dbgen.InsertProviderConnectionParams{ID: id, UserID: u.ID, ProviderID: p.ID, Scopes: granted,
			AccountLabel: labelPtr, AccessTokenCipher: access, RefreshTokenCipher: refresh, ExpiresAt: expires})
	}
	if err != nil {
		return err
	}
	viaRequest := false
	if st.RequestDigest != nil {
		n, err := q.MarkConnectRequestUsed(ctx, *st.RequestDigest)
		if err != nil {
			return err
		}
		viaRequest = n > 0
	}
	e := a.Audit("provider_connection.create", "provider_connection", row.ID.String())
	if reconnect {
		e.Before = connectionSnapshot(cur)
	}
	e.After = connectionSnapshot(row)
	e.Metadata = map[string]any{"name": displayName(u) + " · " + p.Name, "scopes": granted, "reconnect": reconnect,
		"viaRequest": viaRequest, "provider": p.Slug}
	return audit.Record(ctx, q, e)
}

// Cleanup deletes expired authorization states, and connect requests a day
// after they expired. Safe on every replica.
func (s *Service) Cleanup(ctx context.Context) (int64, error) {
	n, err := s.q.DeleteExpiredProviderAuthStates(ctx)
	if err != nil {
		return n, err
	}
	m, err := s.q.DeleteStaleConnectRequests(ctx, s.now().Add(-requestKeep))
	return n + m, err
}

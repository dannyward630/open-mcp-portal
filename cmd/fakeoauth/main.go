// Command fakeoauth runs a fake third-party OAuth 2.0 provider for local
// development of delegated credentials (docs/phase5-delegated-credentials.md):
//
//	go run ./cmd/fakeoauth                  # http://127.0.0.1:8092
//	go run ./cmd/fakeoauth -ttl 2m          # short-lived access tokens, to watch refreshes
//
// It serves GET /authorize (a page with Approve and Deny; -auto approves at
// once), POST /token (authorization_code with optional PKCE S256, and
// refresh_token with rotation), POST /revoke (RFC 7009) and GET /userinfo
// ({"login": ...}), with the logic of testutil.FakeOAuth. Register it in
// Admin › Auth providers with DEV_AUTH on a loopback APP_URL (which allows
// plain http to loopback addresses):
//
//	Authorization URL  http://127.0.0.1:8092/authorize
//	Token URL          http://127.0.0.1:8092/token
//	Revocation URL     http://127.0.0.1:8092/revoke
//	Userinfo URL       http://127.0.0.1:8092/userinfo
//	Client ID / secret fake-client / fake-secret-0123456789 (-client-id, -client-secret)
//
// Tokens are logged only as a digest prefix.
package main

import (
	"flag"
	"log"
	"net/http"
	"time"

	"github.com/ncecere/open-mcp-gateway/internal/testutil"
)

func main() {
	addr := flag.String("addr", "127.0.0.1:8092", "listen address")
	clientID := flag.String("client-id", "fake-client", "the registered client ID")
	clientSecret := flag.String("client-secret", "fake-secret-0123456789", "the client secret (empty: a public client)")
	ttl := flag.Duration("ttl", time.Hour, "access token lifetime (0: tokens don't expire)")
	auto := flag.Bool("auto", false, "approve every authorization without showing a page")
	rotate := flag.Bool("rotate", true, "rotate refresh tokens on every use")
	login := flag.String("login", "octocat", "the account name /userinfo reports")
	flag.Parse()

	f, h := testutil.NewFakeOAuthHandler(*clientID, *clientSecret)
	f.Set(func(f *testutil.FakeOAuth) {
		f.AccessTTL, f.AutoApprove, f.RotateRefresh, f.Login = *ttl, *auto, *rotate, *login
	})
	logged := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		h.ServeHTTP(w, r)
		log.Printf("%s %s (%s)", r.Method, r.URL.Path, time.Since(start).Round(time.Millisecond))
	})
	log.Printf("fake OAuth provider on http://%s (client %s)", *addr, *clientID)
	srv := &http.Server{Addr: *addr, Handler: logged, ReadHeaderTimeout: 10 * time.Second}
	log.Fatal(srv.ListenAndServe())
}

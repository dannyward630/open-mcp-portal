// Command mail-mcp is a reference MCP server for Gmail that runs behind Open
// MCP Gateway and relies on the gateway's delegated credentials
// (docs/phase5-delegated-credentials.md) instead of running its own OAuth:
// the gateway signs the person in, holds their Google token, refreshes it,
// and forwards it with each call in MCP-Gateway-Provider-Token. See
// examples/mail-mcp/README.md.
//
//	go run ./examples/mail-mcp                 # real Gmail, gateway on http://localhost:8088
//	go run ./examples/mail-mcp -fake-gmail     # an in-process fake Gmail (with make fake-oauth)
//
// Every request is authenticated with the gateway's identity token (ES256,
// verified against -jwks, iss = -issuer, aud = -audience). Requests without
// one may only list tools (the gateway's discovery and health checks carry
// no token); tools/call without a valid token gets 401. The mailbox tools
// also need the identity token's provider claim to equal -provider and the
// MCP-Gateway-Provider header to match it.
//
// Tokens, queries and message contents are never logged.
package main

import (
	"context"
	"errors"
	"flag"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"
)

func main() {
	addr := flag.String("addr", "127.0.0.1:8093", "listen address")
	gateway := flag.String("gateway", "http://localhost:8088", "the gateway's URL (its APP_URL); sets -issuer and -jwks when they are empty")
	issuer := flag.String("issuer", "", "the identity token's required iss (default: -gateway)")
	jwks := flag.String("jwks", "", "the gateway's JWKS URL (default: -gateway + /.well-known/jwks.json)")
	audience := flag.String("audience", "", "this server's URL exactly as registered in the gateway (default: http://<addr>/mcp)")
	provider := flag.String("provider", "google", "the auth provider slug the gateway must send tokens for")
	gmailAPI := flag.String("gmail-api", "https://gmail.googleapis.com", "the Gmail API origin")
	fakeGmail := flag.Bool("fake-gmail", false, "serve a fake Gmail API at /fake-gmail on this server and use it")
	maxBody := flag.Int("max-body-chars", 20000, "read_message cuts bodies to this many characters")
	discoveryHeader := flag.String("discovery-header", "X-Discovery-Key", "with -discovery-key: the static header anonymous discovery requests must carry")
	discoveryKey := flag.String("discovery-key", os.Getenv("MAIL_MCP_DISCOVERY_KEY"), "require this value in -discovery-header on requests without an identity token (env MAIL_MCP_DISCOVERY_KEY)")
	flag.Parse()

	gw := strings.TrimRight(*gateway, "/")
	if *issuer == "" {
		*issuer = gw
	}
	if *jwks == "" {
		*jwks = gw + "/.well-known/jwks.json"
	}
	if *audience == "" {
		*audience = "http://" + *addr + "/mcp"
	}
	if *provider == "" {
		log.Fatal("-provider is required")
	}

	mux := http.NewServeMux()
	api := *gmailAPI
	if *fakeGmail {
		mux.Handle("/fake-gmail/", http.StripPrefix("/fake-gmail", NewFakeGmail()))
		api = "http://" + *addr + "/fake-gmail"
		log.Printf("fake Gmail API at %s (accepts any bearer token)", api)
	}

	logger := log.New(os.Stderr, "", log.LstdFlags)
	mail := &MailServer{Gmail: &Gmail{Base: api}, Provider: *provider, MaxBodyChars: *maxBody, Logf: logger.Printf}
	guard := &Guard{Verifier: &Verifier{JWKSURL: *jwks, Issuer: *issuer, Audience: *audience}, Logf: logger.Printf}
	if *discoveryKey != "" {
		guard.DiscoveryHeader, guard.DiscoveryKey = *discoveryHeader, *discoveryKey
	}
	mux.Handle("/mcp", guard.Wrap(mail.Handler()))
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("ok\n")) })

	srv := &http.Server{Addr: *addr, Handler: mux, ReadHeaderTimeout: 10 * time.Second, ReadTimeout: 30 * time.Second,
		WriteTimeout: 60 * time.Second}
	log.Printf("mail MCP server on http://%s/mcp (audience %s, issuer %s, provider %q, Gmail %s)", *addr, *audience, *issuer, *provider, api)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		sctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = srv.Shutdown(sctx)
	}()
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatal(err)
	}
}

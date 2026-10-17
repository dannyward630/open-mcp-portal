// Command fakemcp runs a fake remote MCP server for local development and
// smoke tests of the registry (docs/registry.md):
//
//	go run ./cmd/fakemcp                        # http://127.0.0.1:8091/mcp
//	go run ./cmd/fakemcp -header X-API-Key -value dev-key
//	go run ./cmd/fakemcp -describe 'check_outage=Now also reports planned work.'
//
// It serves Streamable HTTP statelessly with the capabilities of
// testutil.FakeMCP: tools check_outage, search_tickets and create_ticket, the
// resource status://services, the resource template
// status://services/{service} and the prompt incident_summary. -describe
// (repeatable; or FAKEMCP_DESCRIBE, entries separated by ";") changes a
// tool's description, so a refresh sees a changed capability. Register it
// with DEV_AUTH on a loopback APP_URL, which allows http and loopback
// addresses.
//
// For gateways it also has the tools whoami (the identity token the gateway
// sent, its claims and whether it verifies), provider_token (whether the
// call carried a delegated provider token: the provider, a SHA-256 prefix of
// the token, never the token, and whether the identity token's provider
// claim matches) and echo {text} (-gateway-tools, on by default), and logs
// the identity of every tool call. With -jwks (for
// example http://localhost:8088/.well-known/jwks.json) tokens are verified
// for -audience, by default this server's URL:
//
//	go run ./cmd/fakemcp -jwks http://localhost:8088/.well-known/jwks.json
package main

import (
	"context"
	"flag"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/ncecere/open-mcp-gateway/internal/testutil"
)

type descriptions []string

func (d *descriptions) String() string     { return strings.Join(*d, ";") }
func (d *descriptions) Set(v string) error { *d = append(*d, v); return nil }

func main() {
	addr := flag.String("addr", "127.0.0.1:8091", "listen address")
	header := flag.String("header", "", "require this request header (for example Authorization)")
	value := flag.String("value", "", "the header's value")
	gatewayTools := flag.Bool("gateway-tools", true, "add the whoami, provider_token and echo tools")
	jwks := flag.String("jwks", "", "verify identity tokens against this JWKS URL")
	audience := flag.String("audience", "", "the audience identity tokens must have (default: this server's URL)")
	var describe descriptions
	flag.Var(&describe, "describe", "tool=description: change a tool's description (repeatable)")
	flag.Parse()
	if env := os.Getenv("FAKEMCP_DESCRIBE"); env != "" {
		describe = append(describe, strings.Split(env, ";")...)
	}

	f, h := testutil.NewFakeMCPHandler()
	if *header != "" {
		f.RequireHeader(*header, *value)
	}
	for _, d := range describe {
		tool, text, ok := strings.Cut(d, "=")
		if !ok || tool == "" {
			log.Fatalf("-describe %q: want tool=description", d)
		}
		f.SetDescription(tool, text)
		log.Printf("tool %s: description changed", tool)
	}
	if *gatewayTools {
		f.EnableGatewayTools()
		aud := *audience
		if aud == "" {
			aud = "http://" + *addr + "/mcp"
		}
		f.SetJWKS(*jwks, aud)
		f.OnToolCall = func(c testutil.ToolCall) {
			header, _ := testutil.IdentityToken(c.Header)
			who := testutil.Whoami(context.Background(), c.Header, *jwks, aud)
			if c.URI != "" {
				log.Printf("read %s: identity in %q, sub=%v aud=%v gateway=%v resource=%v verified=%v %s", c.URI, header,
					who.Claims["sub"], who.Claims["aud"], who.Claims["gateway"], who.Claims["resource"], who.Verified, who.Error)
				return
			}
			prov := testutil.ProviderTokenReport(context.Background(), c.Header, *jwks, aud)
			log.Printf("tool %s: identity in %q, sub=%v aud=%v gateway=%v tool=%v verified=%v provider=%q token=%v claim=%q %s", c.Tool, header,
				who.Claims["sub"], who.Claims["aud"], who.Claims["gateway"], who.Claims["tool"], who.Verified, prov.Provider, prov.Received,
				prov.Claim, who.Error)
		}
	}
	mux := http.NewServeMux()
	mux.Handle("/mcp", h)
	log.Printf("fake MCP server on http://%s/mcp", *addr)
	srv := &http.Server{Addr: *addr, Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	log.Fatal(srv.ListenAndServe())
}

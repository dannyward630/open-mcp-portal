package oauth

import (
	"context"
	"errors"
	"net"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ncecere/open-mcp-portal/internal/testutil"
)

// capEnv is a service over a real database with a cap on registered clients.
func capEnv(t *testing.T, max int) (*Service, *pgxpool.Pool) {
	t.Helper()
	pool, _ := testutil.NewDB(t)
	s := New(pool, nil, Config{Issuer: "https://portal.example.com", Pepper: make([]byte, 32), MaxRegisteredClients: max})
	s.Log = testutil.Logger()
	return s, pool
}

func register(s *Service) (Registered, error) {
	return s.Register(context.Background(), ClientMetadata{ClientName: "App", RedirectURIs: []string{"https://app.example.com/cb"}}, "req-1", "203.0.113.7")
}

func liveClients(t *testing.T, pool *pgxpool.Pool) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(context.Background(), `SELECT count(*) FROM oauth_clients`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func wantFull(t *testing.T, err error) {
	t.Helper()
	var oe *Error
	if !errors.As(err, &oe) || oe.Code != "temporarily_unavailable" || oe.Status != http.StatusServiceUnavailable || oe.ShowPage() ||
		!strings.Contains(oe.Description, "limit of registered apps") {
		t.Fatalf("err = %v, want 503 temporarily_unavailable", err)
	}
}

// Under the cap registrations succeed, at the cap they are refused (nothing
// is stored or audited), and a registration that went unused for 30 days
// frees its slot.
func TestRegistrationCap(t *testing.T) {
	s, pool := capEnv(t, 3)
	ctx := context.Background()
	var first string
	for i := range 3 {
		reg, err := register(s)
		if err != nil {
			t.Fatalf("registration %d under the cap: %v", i+1, err)
		}
		if i == 0 {
			first = reg.ID
		}
	}
	wantFull(t, func() error { _, err := register(s); return err }())
	if n := liveClients(t, pool); n != 3 {
		t.Fatalf("%d clients stored after a refusal", n)
	}
	var audits int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM audit_log WHERE action = 'oauth.client_register'`).Scan(&audits); err != nil || audits != 3 {
		t.Fatalf("register audits = %d %v (a refusal is not audited)", audits, err)
	}
	// Invalid metadata is still told as such at the cap, before anything else.
	_, err := s.Register(ctx, ClientMetadata{}, "req", "")
	var oe *Error
	if !errors.As(err, &oe) || oe.Code != "invalid_redirect_uri" {
		t.Fatalf("bad metadata at the cap: %v", err)
	}

	// One registration unused for 31 days is expired: its slot is free (and
	// it is deleted by the registration that takes it).
	if _, err := pool.Exec(ctx, `UPDATE oauth_clients SET last_used_at = now() - interval '31 days' WHERE client_id = $1`, first); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ResolveClient(ctx, first); err == nil {
		t.Fatal("an expired registration still resolves")
	}
	if _, err := register(s); err != nil {
		t.Fatalf("registration after one expired: %v", err)
	}
	if n := liveClients(t, pool); n != 3 {
		t.Fatalf("%d clients after the slot was reused", n)
	}
	wantFull(t, func() error { _, err := register(s); return err }())
	// Just inside the 30 days still counts.
	if _, err := pool.Exec(ctx, `UPDATE oauth_clients SET last_used_at = now() - interval '29 days' WHERE id = (SELECT id FROM oauth_clients LIMIT 1)`); err != nil {
		t.Fatal(err)
	}
	wantFull(t, func() error { _, err := register(s); return err }())
}

// 0 turns the cap off.
func TestRegistrationCapZeroIsNoCap(t *testing.T) {
	s, pool := capEnv(t, 0)
	for i := range 25 {
		if _, err := register(s); err != nil {
			t.Fatalf("registration %d without a cap: %v", i+1, err)
		}
	}
	if n := liveClients(t, pool); n != 25 {
		t.Fatalf("%d clients", n)
	}
}

// Concurrent registrations, as from several replicas, can't overshoot the cap.
func TestRegistrationCapHoldsUnderConcurrency(t *testing.T) {
	s, pool := capEnv(t, 5)
	var ok, full atomic.Int64
	var wg sync.WaitGroup
	for range 24 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := register(s)
			var oe *Error
			switch {
			case err == nil:
				ok.Add(1)
			case errors.As(err, &oe) && oe.Code == "temporarily_unavailable":
				full.Add(1)
			default:
				t.Errorf("registration: %v", err)
			}
		}()
	}
	wg.Wait()
	if ok.Load() != 5 || full.Load() != 19 || liveClients(t, pool) != 5 {
		t.Fatalf("registered %d, refused %d, stored %d", ok.Load(), full.Load(), liveClients(t, pool))
	}
}

// Metadata-document clients aren't rows and never count: at the cap, one still
// resolves (so it can sign in), and registered clients already issued keep
// working.
func TestRegistrationCapSparesMetadataDocumentClients(t *testing.T) {
	s, _ := capEnv(t, 1)
	reg, err := register(s)
	if err != nil {
		t.Fatal(err)
	}
	wantFull(t, func() error { _, err := register(s); return err }())

	srv := clientDocServer(t)
	_, port, _ := net.SplitHostPort(srv.Listener.Addr().String())
	s.docs = docFetcher(t, srv, map[string]string{"public.example.com": "8.8.8.8"}, nil, false)
	c, err := s.ResolveClient(context.Background(), "https://public.example.com:"+port+"/client.json")
	if err != nil || c.Kind != KindMetadata {
		t.Fatalf("metadata document client at the cap: %+v %v", c, err)
	}
	if c, err := s.ResolveClient(context.Background(), reg.ID); err != nil || c.Kind != KindRegistered {
		t.Fatalf("registered client at the cap: %+v %v", c, err)
	}
}

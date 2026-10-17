package registry

import (
	"context"
	"net/netip"
	"sync"
	"testing"
	"time"

	"github.com/ncecere/open-mcp-portal/internal/authz"
	"github.com/ncecere/open-mcp-portal/internal/netpolicy"
	"github.com/ncecere/open-mcp-portal/internal/secrets"
	"github.com/ncecere/open-mcp-portal/internal/testutil"
	"github.com/ncecere/open-mcp-portal/internal/upstream"
)

type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *fakeClock) now() time.Time      { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *fakeClock) add(d time.Duration) { c.mu.Lock(); defer c.mu.Unlock(); c.t = c.t.Add(d) }
func allowed(p *netpolicy.Policy, a string) bool {
	return p.CheckAddr("mcp.corp.example.com", netip.MustParseAddr(a)).Allowed
}

// Two replicas: the one that saves applies the change at once, the other
// within NetworkCacheTTL. Deployment entries always apply.
func TestNetworksPropagation(t *testing.T) {
	pool, _ := testutil.NewDB(t)
	box, err := secrets.New(make([]byte, 32))
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	clock := &fakeClock{t: time.Now()}
	deployment, _ := netpolicy.ParseList("10.30.0.0/16")
	replica := func() (*Service, *Networks) {
		n := NewNetworks(pool, deployment, testutil.Logger())
		n.now = clock.now
		return New(pool, box, &upstream.Client{Networks: n, Log: testutil.Logger()}, testutil.Logger()), n
	}
	a, na := replica()
	_, nb := replica()
	if !allowed(na.Current(ctx), "10.30.0.1") || allowed(na.Current(ctx), "10.20.0.1") || allowed(nb.Current(ctx), "10.20.0.1") {
		t.Fatal("initial policy")
	}

	admin := authz.Actor{PlatformRole: authz.PlatformAdmin}
	st, err := a.UpdateNetworkSettings(ctx, admin, []netpolicy.Entry{{Kind: netpolicy.KindCIDR, Value: "10.20.0.0/16"}}, 1)
	if err != nil || st.Revision != 2 || len(st.Deployment) != 1 {
		t.Fatalf("update = %+v %v", st, err)
	}
	if !allowed(na.Current(ctx), "10.20.0.1") {
		t.Fatal("the saving replica must apply the change at once")
	}
	if allowed(nb.Current(ctx), "10.20.0.1") {
		t.Fatal("the other replica reads again only after the TTL")
	}
	clock.add(NetworkCacheTTL + time.Second)
	if !allowed(nb.Current(ctx), "10.20.0.1") || !allowed(nb.Current(ctx), "10.30.0.1") {
		t.Fatal("the other replica must apply the change within the TTL")
	}

	// Auditors read but don't write; a stale revision conflicts.
	auditor := authz.Actor{PlatformRole: authz.PlatformAuditor}
	if _, err := a.GetNetworkSettings(ctx, auditor); err != nil {
		t.Fatal(err)
	}
	if _, err := a.UpdateNetworkSettings(ctx, auditor, nil, 2); err == nil {
		t.Fatal("auditor update accepted")
	}
	if _, err := a.UpdateNetworkSettings(ctx, admin, nil, 1); err == nil {
		t.Fatal("stale update accepted")
	}

	// A failed read keeps the last copy.
	pool.Close()
	clock.add(NetworkCacheTTL + time.Second)
	if !allowed(nb.Current(ctx), "10.20.0.1") {
		t.Fatal("a failed read must keep the last copy")
	}
}

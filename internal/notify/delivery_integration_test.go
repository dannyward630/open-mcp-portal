package notify

import (
	"context"
	"crypto/x509"
	"fmt"
	"io"
	"mime/quotedprintable"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/ncecere/open-mcp-portal/internal/apperr"
	"github.com/ncecere/open-mcp-portal/internal/authz"
	"github.com/ncecere/open-mcp-portal/internal/config"
	"github.com/ncecere/open-mcp-portal/internal/httpapi/apitypes"
	"github.com/ncecere/open-mcp-portal/internal/store"
	"github.com/ncecere/open-mcp-portal/internal/testutil"
)

type delivery struct {
	header http.Header
	body   []byte
}

// receiver is a webhook endpoint that records what it gets and answers with
// the next status of its script (then 200).
type receiver struct {
	*httptest.Server
	mu     sync.Mutex
	got    []delivery
	script []int
}

func newReceiver(t *testing.T, script ...int) *receiver {
	r := &receiver{script: script}
	r.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		body, _ := io.ReadAll(req.Body)
		r.mu.Lock()
		r.got = append(r.got, delivery{req.Header.Clone(), body})
		status := 200
		if len(r.script) > 0 {
			status, r.script = r.script[0], r.script[1:]
		}
		r.mu.Unlock()
		w.WriteHeader(status)
		_, _ = w.Write([]byte("RESPONSE-BODY-SENTINEL"))
	}))
	t.Cleanup(r.Close)
	return r
}

func (r *receiver) deliveries() []delivery {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]delivery(nil), r.got...)
}

// destination makes a destination through the service (as a platform admin or
// the team's owner) and returns it with its secret.
func (e *tenv) destination(actor authz.Actor, sc Scope, format, rawURL string, events ...string) (Destination, string) {
	e.t.Helper()
	if len(events) == 0 {
		events = []string{ServerUnhealthy, ServerRecovered}
	}
	name := fmt.Sprintf("dest-%d", e.next())
	in := Input{Name: &name, Format: format, URL: &rawURL, Events: &events}
	if sc.platform() {
		on := true
		in.TeamEvents = &on
	}
	d, secret, err := e.svc.Create(context.Background(), actor, sc, in)
	if err != nil {
		e.t.Fatalf("create destination: %v", err)
	}
	return d, secret
}

func TestWebhookDeliveryEndToEnd(t *testing.T) {
	e := newEnv(t, nil)
	team := e.team("ops")
	owner := e.user("none")
	pa := e.user("platform_admin")
	e.member(team, owner, "owner")
	srv := e.server(team, "published")
	rcv := newReceiver(t)
	d, secret := e.destination(personActor(owner), Scope{TeamRef: "ops"}, "json", rcv.URL+"/hook/SENTINELPATH")
	if !strings.HasPrefix(secret, SecretPrefix) || len(secret) != len(SecretPrefix)+43 || d.D.SecretHint != secret[len(secret)-4:] || d.D.UrlDisplay != "http://"+strings.TrimPrefix(rcv.URL, "http://")+"/..." {
		t.Errorf("secret %q hint %q display %q", secret, d.D.SecretHint, d.D.UrlDisplay)
	}
	e.emit(ServerUnhealthy, team, "w1", unhealthy(srv, "Payments"))
	sum := e.runOnce()
	e.run(`UPDATE notification_outbox SET error_class = error_class`) // touch nothing: the sentinel check below reads every table
	for _, table := range []string{"audit_log", "notification_events", "notification_outbox", "notification_inbox"} {
		if dump := e.exec(fmt.Sprintf(`SELECT coalesce(string_agg(t::text, ' '), '') FROM %s t`, table)); strings.Contains(dump, "SENTINELPATH") || strings.Contains(dump, secret) {
			t.Errorf("%s contains the URL path or the secret", table)
		}
	}
	if strings.Contains(e.logs.String(), "SENTINELPATH") || strings.Contains(e.logs.String(), secret) {
		t.Error("the logs contain the URL path or the secret")
	}
	if sum.Sent != 2 { // the webhook and the owner's email
		t.Fatalf("%+v", sum)
	}
	got := rcv.deliveries()
	if len(got) != 1 {
		t.Fatalf("%d deliveries", len(got))
	}
	ts, _ := strconv.ParseInt(got[0].header.Get(HeaderTimestamp), 10, 64)
	if !Verify(secret, got[0].header.Get(HeaderSignature), ts, got[0].body, time.Now(), 5*time.Minute) {
		t.Errorf("the receiver can't verify the signature: %v", got[0].header)
	}
	evID := e.exec(`SELECT public_id::text FROM notification_events WHERE dedup_key = 'w1'`)
	if got[0].header.Get(HeaderEventID) != evID || got[0].header.Get(HeaderEvent) != ServerUnhealthy || got[0].header.Get(HeaderAttempt) != "1" {
		t.Errorf("headers %v", got[0].header)
	}
	if len(got[0].body) > 8192 || !strings.Contains(string(got[0].body), `"type":"server.unhealthy"`) || !strings.Contains(string(got[0].body), `"slug":"ops"`) {
		t.Errorf("body: %s", got[0].body)
	}
	// Health and the delivery log.
	cur, _ := e.svc.Get(context.Background(), personActor(owner), Scope{TeamRef: "ops"}, d.D.ID)
	if cur.D.ConsecutiveFailures != 0 || cur.D.LastSuccessAt == nil {
		t.Errorf("health: %+v", cur.D)
	}
	log, err := e.svc.Deliveries(context.Background(), personActor(owner), Scope{TeamRef: "ops"}, d.D.ID)
	if err != nil || len(log) != 1 || log[0].State != StateSent || log[0].Attempts != 1 || log[0].HttpStatus == nil || *log[0].HttpStatus != 200 {
		t.Errorf("delivery log: %+v %v", log, err)
	}
	// A second run delivers nothing again.
	if sum := e.runOnce(); sum.Sent != 0 || len(rcv.deliveries()) != 1 {
		t.Errorf("repeated: %+v", sum)
	}
	// A platform destination takes the team's events only with teamEvents on,
	// and publish.requested always.
	plat, _ := e.destination(adminActor(pa), Scope{}, "slack", rcv.URL+"/platform", ServerUnhealthy, PublishRequested)
	e.emit(ServerUnhealthy, team, "w2", unhealthy(srv, "Payments"))
	e.emit(PublishRequested, team, "w3", apitypes.PublishRequestedData{Server: serverRef(srv, "srv", "S"), Reason: "new"})
	e.runOnce()
	if n := e.count(`SELECT count(*) FROM notification_outbox WHERE destination_id = $1 AND state = 'sent'`, plat.D.ID); n != 2 {
		t.Errorf("the platform destination got %d deliveries, want 2", n)
	}
	// Turn teamEvents off: publish.requested still, the team event no longer.
	off := false
	evs := []string{PublishRequested}
	if _, err := e.svc.Update(context.Background(), adminActor(pa), Scope{}, plat.D.ID, Input{TeamEvents: &off, Events: &evs}, plat.D.Revision); err != nil {
		t.Fatal(err)
	}
	e.emit(ServerUnhealthy, team, "w4", unhealthy(srv, "Payments"))
	e.runOnce()
	if n := e.count(`SELECT count(*) FROM notification_outbox WHERE destination_id = $1`, plat.D.ID); n != 2 {
		t.Errorf("without teamEvents the platform destination got %d rows", n)
	}
}

func TestRetrySchedulePermanentFailureAndExpiry(t *testing.T) {
	e := newEnv(t, nil)
	team := e.team("ops")
	owner := e.user("none")
	e.member(team, owner, "owner")
	srv := e.server(team, "published")
	rcv := newReceiver(t, 500, 503, 200)
	d, _ := e.destination(personActor(owner), Scope{TeamRef: "ops"}, "slack", rcv.URL+"/hook")
	e.emit(ServerUnhealthy, team, "r1", unhealthy(srv, "S"))
	state := func() (string, int, string, time.Duration) {
		var st string
		var attempts int
		var class *string
		var wait float64
		if err := e.pool.QueryRow(context.Background(), `SELECT state, attempts, error_class, extract(epoch FROM next_attempt_at - now()) FROM notification_outbox WHERE destination_id = $1`, d.D.ID).
			Scan(&st, &attempts, &class, &wait); err != nil {
			t.Fatal(err)
		}
		c := ""
		if class != nil {
			c = *class
		}
		return st, attempts, c, time.Duration(wait * float64(time.Second))
	}
	due := func() {
		e.run(`UPDATE notification_outbox SET next_attempt_at = now() WHERE destination_id = $1`, d.D.ID)
	}

	e.runOnce()
	st, attempts, class, wait := state()
	if st != StatePending || attempts != 1 || class != ClassServerError || wait < 50*time.Second || wait > 70*time.Second {
		t.Fatalf("after attempt 1: %s %d %s wait %s (want pending, 1, server_error, ~1m)", st, attempts, class, wait)
	}
	e.runOnce() // not due: nothing happens
	if _, attempts, _, _ := state(); attempts != 1 {
		t.Fatal("a delivery that isn't due was attempted")
	}
	due()
	e.runOnce()
	if st, attempts, _, wait := state(); st != StatePending || attempts != 2 || wait < 4*time.Minute || wait > 5*time.Minute+10*time.Second {
		t.Fatalf("after attempt 2: %s %d wait %s (want ~5m)", st, attempts, wait)
	}
	due()
	e.runOnce()
	if st, attempts, class, _ := state(); st != StateSent || attempts != 3 || class != "" {
		t.Fatalf("after attempt 3: %s %d %q", st, attempts, class)
	}
	if h := rcv.deliveries(); len(h) != 3 || h[2].header.Get(HeaderAttempt) != "3" || h[0].header.Get(HeaderDelivery) != h[2].header.Get(HeaderDelivery) {
		t.Errorf("attempts %d; the delivery ID is the same across attempts", len(h))
	}
	// The schedule after the first failures: 1 m, 5 m, 30 m, 2 h, 6 h, then failed.
	want := []time.Duration{time.Minute, 5 * time.Minute, 30 * time.Minute, 2 * time.Hour, 6 * time.Hour}
	bad := newReceiver(t, 500, 500, 500, 500, 500, 500, 500)
	d2, _ := e.destination(personActor(owner), Scope{TeamRef: "ops"}, "slack", bad.URL+"/hook")
	e.emit(ServerUnhealthy, team, "r2", unhealthy(srv, "S"))
	for i := 0; i < MaxAttempts; i++ {
		e.run(`UPDATE notification_outbox SET next_attempt_at = now() WHERE destination_id = $1`, d2.D.ID)
		e.runOnce()
		var st string
		var secs float64
		e.t.Helper()
		if err := e.pool.QueryRow(context.Background(), `SELECT state, extract(epoch FROM next_attempt_at - now()) FROM notification_outbox WHERE destination_id = $1`, d2.D.ID).Scan(&st, &secs); err != nil {
			t.Fatal(err)
		}
		if i < len(want) {
			if st != StatePending || time.Duration(secs*float64(time.Second)) < want[i]-30*time.Second || time.Duration(secs*float64(time.Second)) > want[i]+30*time.Second {
				t.Fatalf("after attempt %d: %s, next in %.0fs, want %s", i+1, st, secs, want[i])
			}
		} else if st != StateFailed {
			t.Fatalf("after %d attempts the delivery should have failed: %s", MaxAttempts, st)
		}
	}
	if len(bad.deliveries()) != MaxAttempts {
		t.Errorf("%d attempts, want %d", len(bad.deliveries()), MaxAttempts)
	}

	// A permanent error (4xx other than 408 and 429) fails at once.
	perm := newReceiver(t, 404)
	d3, _ := e.destination(personActor(owner), Scope{TeamRef: "ops"}, "slack", perm.URL+"/hook")
	e.emit(ServerUnhealthy, team, "r3", unhealthy(srv, "S"))
	e.runOnce()
	if got := e.exec(`SELECT state || '/' || attempts || '/' || error_class FROM notification_outbox WHERE destination_id = $1`, d3.D.ID); got != "failed/1/rejected" {
		t.Errorf("a 404 = %s", got)
	}

	// A delivery undelivered 24 hours after it was queued expires.
	exp := newReceiver(t, 500)
	d4, _ := e.destination(personActor(owner), Scope{TeamRef: "ops"}, "slack", exp.URL+"/hook")
	e.emit(ServerUnhealthy, team, "r4", unhealthy(srv, "S"))
	e.runOnce()
	e.run(`UPDATE notification_outbox SET created_at = now() - interval '25 hours', next_attempt_at = now() WHERE destination_id = $1`, d4.D.ID)
	before := len(exp.deliveries())
	e.runOnce()
	if got := e.exec(`SELECT state || '/' || error_class FROM notification_outbox WHERE destination_id = $1`, d4.D.ID); got != "failed/expired" || len(exp.deliveries()) != before {
		t.Errorf("expiry: %s (%d attempts)", got, len(exp.deliveries())-before)
	}
}

func TestDestinationStopsAfterTwentyFailures(t *testing.T) {
	e := newEnv(t, nil)
	team := e.team("ops")
	owner := e.user("none")
	e.member(team, owner, "owner")
	srv := e.server(team, "published")
	bad := newReceiver(t, 500)
	d, _ := e.destination(personActor(owner), Scope{TeamRef: "ops"}, "slack", bad.URL+"/hook")
	e.run(`UPDATE notification_destinations SET consecutive_failures = 19 WHERE id = $1`, d.D.ID)
	e.emit(ServerUnhealthy, team, "s1", unhealthy(srv, "S"))
	e.runOnce()
	cur, _ := e.svc.Get(context.Background(), personActor(owner), Scope{TeamRef: "ops"}, d.D.ID)
	if cur.D.Enabled || cur.D.DisabledReason == nil || *cur.D.DisabledReason != "failures" || cur.D.ConsecutiveFailures != 20 {
		t.Fatalf("destination: enabled %v reason %v failures %d", cur.D.Enabled, cur.D.DisabledReason, cur.D.ConsecutiveFailures)
	}
	if e.count(`SELECT count(*) FROM audit_log WHERE action = 'notification_destination.disable' AND actor_kind = 'system' AND target_id = $1`, d.D.ID.String()) != 1 {
		t.Error("the automatic stop is audited, by the system")
	}
	// Queued deliveries to a stopped destination are skipped, not sent.
	e.emit(ServerUnhealthy, team, "s2", unhealthy(srv, "S"))
	e.run(`UPDATE notification_outbox SET next_attempt_at = now()`)
	n := len(bad.deliveries())
	e.runOnce()
	if len(bad.deliveries()) != n {
		t.Error("a stopped destination was delivered to")
	}
	if e.count(`SELECT count(*) FROM notification_outbox WHERE destination_id = $1 AND state = 'skipped'`, d.D.ID) < 1 {
		t.Error("deliveries to a stopped destination are skipped")
	}
	// Turning it back on clears the count.
	on := true
	back, err := e.svc.Update(context.Background(), personActor(owner), Scope{TeamRef: "ops"}, d.D.ID, Input{Enabled: &on}, cur.D.Revision)
	if err != nil || !back.D.Enabled || back.D.DisabledReason != nil || back.D.ConsecutiveFailures != 0 {
		t.Errorf("re-enabled: %+v %v", back.D, err)
	}
}

func TestSecondDispatcherIsBlockedByTheLock(t *testing.T) {
	e := newEnv(t, nil)
	release, ok, err := store.TryAdvisoryLockText(context.Background(), e.pool, lockKey)
	if err != nil || !ok {
		t.Fatal(err)
	}
	if sum, err := e.svc.Run(context.Background()); err != nil || sum.Led {
		t.Fatalf("a second dispatcher ran while the first held the lock: %+v %v", sum, err)
	}
	release()
	if sum := e.runOnce(); !sum.Led {
		t.Error("the dispatcher should run once the lock is free")
	}
	st, _ := e.q.GetNotificationState(context.Background())
	if st.LastRunAt == nil || st.KeyScanAt == nil {
		t.Errorf("the heartbeat: %+v", st)
	}
}

// One team's slow endpoint doesn't stall everyone's notifications: rows are
// delivered per destination, and a destination gets at most 3 per run.
func TestTarpitDestinationDoesNotStallOthers(t *testing.T) {
	e := newEnv(t, nil)
	team := e.team("ops")
	owner := e.user("none")
	e.member(team, owner, "owner")
	srv := e.server(team, "published")
	release := make(chan struct{})
	var tarpitHits atomic.Int32
	tarpit := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tarpitHits.Add(1)
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	defer tarpit.Close()
	defer func() {
		select {
		case <-release:
		default:
			close(release)
		}
	}()
	fast := newReceiver(t)
	e.destination(personActor(owner), Scope{TeamRef: "ops"}, "slack", tarpit.URL+"/slow")
	e.destination(personActor(owner), Scope{TeamRef: "ops"}, "slack", fast.URL+"/fast")
	for i := 0; i < 5; i++ {
		e.emit(ServerUnhealthy, team, fmt.Sprintf("t%d", i), unhealthy(srv, "S"))
	}
	done := make(chan RunSummary, 1)
	go func() { sum, _ := e.svc.Run(context.Background()); done <- sum }()
	deadline := time.After(5 * time.Second)
	for len(fast.deliveries()) < 3 {
		select {
		case <-deadline:
			t.Fatalf("the fast destination got %d deliveries while the tarpit held a worker", len(fast.deliveries()))
		case <-time.After(20 * time.Millisecond):
		}
	}
	select {
	case <-done:
		t.Fatal("the run ended before the tarpit answered, so it did not stall the others by accident")
	default:
	}
	close(release)
	<-done
	// At most 3 rows per destination per run: the fast one got 3, and 2 wait.
	if got := len(fast.deliveries()); got != 3 {
		t.Errorf("the fast destination got %d in one run, want the per-destination cap of 3", got)
	}
	if e.count(`SELECT count(*) FROM notification_outbox WHERE state = 'pending' AND attempts = 0`) != 6 { // 2 of each: the tarpit, the fast one, the owner's email
		t.Errorf("rows beyond the cap wait for the next run: %d", e.count(`SELECT count(*) FROM notification_outbox WHERE state = 'pending' AND attempts = 0`))
	}
	e.runOnce()
	if got := len(fast.deliveries()); got != 5 {
		t.Errorf("after the next run the fast destination has %d", got)
	}
}

// Destinations through the service: URLs are checked with the webhook guard
// (public https only, never the MCP servers' allowlist) and the stored values
// are sealed; neither the URL nor a secret reaches the audit log, the logs or
// any table in clear.
func TestDestinationCRUDSealingAndSentinels(t *testing.T) {
	e := newEnv(t, NewWebhookClient(nil, nil)) // the real guard
	team := e.team("ops")
	owner, editor, outsider := e.user("none"), e.user("none"), e.user("none")
	pa, auditor := e.user("platform_admin"), e.user("platform_auditor")
	e.member(team, owner, "owner")
	e.member(team, editor, "editor")
	// The MCP servers' allowlist must not open webhook destinations.
	e.run(`UPDATE network_settings SET entries = '[{"kind":"cidr","value":"10.0.0.0/8","allowHttp":true}]'::jsonb`)
	ctx := context.Background()
	sc := Scope{TeamRef: "ops"}
	create := func(actor authz.Actor, sc Scope, raw string, events ...string) error {
		name := fmt.Sprintf("n%d", e.next())
		_, _, err := e.svc.Create(ctx, actor, sc, Input{Name: &name, Format: "slack", URL: &raw, Events: &events})
		return err
	}
	code := func(err error) string {
		if ae, ok := apperr.As(err); ok {
			if d, ok := ae.Details.(map[string]any); ok {
				if r, ok := d["reason"]; ok {
					return ae.Code + "/" + r.(string)
				}
			}
			return ae.Code
		}
		return errString(err)
	}
	for raw, want := range map[string]string{
		"https://10.1.2.3/hook":        "url_not_allowed/private_address", // the MCP servers' allowlist has 10.0.0.0/8
		"http://10.1.2.3/hook":         "url_not_allowed/scheme",
		"https://127.0.0.1/hook":       "url_not_allowed/blocked_address",
		"https://169.254.169.254/hook": "url_not_allowed/blocked_address",
		"https://[::ffff:7f00:1]/x":    "url_not_allowed/blocked_address",
		"http://example.com/hook":      "url_not_allowed/scheme",
	} {
		if got := code(create(personActor(owner), sc, raw, ServerUnhealthy)); got != want {
			t.Errorf("%s: %s, want %s", raw, got, want)
		}
	}
	// Roles.
	good := "https://hooks.example.com/services/T0SENTINEL/B1SENTINEL/XSECRETSENTINEL?k=QUERYSENTINEL"
	if got := code(create(personActor(editor), sc, good, ServerUnhealthy)); got != "forbidden" {
		t.Errorf("an editor: %s", got)
	}
	if got := code(create(personActor(outsider), sc, good, ServerUnhealthy)); got != "team_not_found" {
		t.Errorf("an outsider: %s", got)
	}
	if got := code(create(adminActor(pa), sc, good, ServerUnhealthy)); got != "forbidden" {
		t.Errorf("a platform admin writing a team's destination: %s", got)
	}
	if got := code(create(authz.Actor{UserID: auditor, PlatformRole: authz.PlatformAuditor}, Scope{}, good, PublishRequested)); got != "forbidden" {
		t.Errorf("an auditor writing a platform destination: %s", got)
	}
	// Events must be receivable by the destination.
	if got := code(create(personActor(owner), sc, good, PublishRequested)); got != "invalid_events" {
		t.Errorf("publish.requested on a team destination: %s", got)
	}
	if got := code(create(personActor(owner), sc, good, ConnectionNeedsReconsent)); got != "invalid_events" {
		t.Errorf("a personal event on a destination: %s", got)
	}
	if got := code(create(personActor(owner), sc, good)); got != "invalid_events" {
		t.Errorf("no events: %s", got)
	}
	if got := code(create(personActor(owner), sc, good, "nope")); got != "invalid_event" {
		t.Errorf("an unknown event: %s", got)
	}
	if got := code(create(adminActor(pa), Scope{}, good, ServerUnhealthy)); got != "invalid_events" {
		t.Errorf("a team event on a platform destination without teamEvents: %s", got)
	}

	d, _, err := e.svc.Create(ctx, personActor(owner), sc, Input{Name: ptr("Slack"), Format: "json", URL: &good, Events: &[]string{ServerUnhealthy}})
	if err != nil {
		t.Fatal(err)
	}
	if got := code(create(personActor(owner), sc, good, ServerUnhealthy)); got != "" && got != "<nil>" {
		t.Errorf("a second destination: %s", got)
	}
	if _, _, err := e.svc.Create(ctx, personActor(owner), sc, Input{Name: ptr("slack"), Format: "json", URL: &good, Events: &[]string{ServerUnhealthy}}); code(err) != "name_taken" {
		t.Errorf("names are unique within a scope, case-insensitively: %v", err)
	}
	// 10 per team.
	for i := 0; i < MaxDestinations; i++ {
		if err := create(personActor(owner), sc, good, ServerUnhealthy); err != nil {
			if code(err) != "too_many_destinations" || i < MaxDestinations-3 {
				t.Fatalf("destination %d: %v", i, err)
			}
			break
		}
		if i == MaxDestinations-1 {
			t.Fatal("more than 10 destinations were created")
		}
	}
	// Sealed: the stored URL isn't the URL, the display is scheme and host.
	var cipher []byte
	_ = e.pool.QueryRow(ctx, `SELECT url_cipher FROM notification_destinations WHERE id = $1`, d.D.ID).Scan(&cipher)
	if strings.Contains(string(cipher), "SENTINEL") || strings.Contains(string(cipher), "hooks.example.com") || d.D.UrlDisplay != "https://hooks.example.com/..." {
		t.Errorf("the URL is not sealed or the display leaks: %q", d.D.UrlDisplay)
	}
	// Update the URL (checked again), rotate, test, delete: audited without secrets.
	bad := "https://127.0.0.1/hook"
	if _, err := e.svc.Update(ctx, personActor(owner), sc, d.D.ID, Input{URL: &bad}, d.D.Revision); code(err) != "url_not_allowed/blocked_address" {
		t.Errorf("updating to a blocked URL: %v", err)
	}
	if _, err := e.svc.Update(ctx, personActor(owner), sc, d.D.ID, Input{Name: ptr("renamed")}, d.D.Revision+5); code(err) != "revision_conflict" {
		t.Errorf("a stale revision: %v", err)
	}
	up, err := e.svc.Update(ctx, personActor(owner), sc, d.D.ID, Input{Name: ptr("renamed")}, d.D.Revision)
	if err != nil || up.D.Revision != d.D.Revision+1 {
		t.Fatalf("%v", err)
	}
	rotated, newSecret, err := e.svc.RotateSecret(ctx, personActor(owner), sc, d.D.ID, 24)
	if err != nil || rotated.D.SecretPreviousUntil == nil || len(rotated.D.SecretPreviousCipher) == 0 || newSecret == "" {
		t.Fatalf("%+v %v", rotated.D, err)
	}
	if _, _, err := e.svc.RotateSecret(ctx, personActor(owner), sc, d.D.ID, 99); code(err) != "invalid_grace" {
		t.Errorf("grace: %v", err)
	}
	res, err := e.svc.Test(ctx, personActor(owner), sc, d.D.ID) // the real guard: example.com can't resolve here, or is blocked: the outcome is a result, not an error
	if err != nil || res.OK {
		t.Logf("test result: %+v %v", res, err)
	}
	for i := 0; i < TestsPerHour+1; i++ {
		_, err = e.svc.Test(ctx, personActor(owner), sc, d.D.ID)
		if err != nil {
			break
		}
	}
	if code(err) != "rate_limited" {
		t.Errorf("tests are limited to %d an hour: %v", TestsPerHour, err)
	}
	if err := e.svc.Delete(ctx, personActor(owner), sc, d.D.ID); err != nil {
		t.Fatal(err)
	}
	// Sentinels: the path, the query and the secrets reach nothing but the sealed column.
	for _, table := range []string{"audit_log", "notification_destinations", "notification_events", "notification_outbox", "notification_inbox"} {
		dump := e.exec(fmt.Sprintf(`SELECT coalesce(string_agg(t::text, ' '), '') FROM %s t`, table))
		for _, bad := range []string{"SENTINEL", "T0SENT", newSecret, SecretPrefix + "x"} {
			if table == "notification_destinations" && strings.HasPrefix(bad, "SENTINEL") {
				continue // the sealed column holds binary
			}
			if strings.Contains(dump, bad) {
				t.Errorf("%s contains %q", table, bad)
			}
		}
	}
	if !strings.Contains(e.exec(`SELECT string_agg(action, ',' ORDER BY id) FROM audit_log WHERE action LIKE 'notification_destination.%'`), "notification_destination.rotate_secret") {
		t.Error("rotation is audited")
	}
	for _, bad := range []string{"SENTINEL", newSecret} {
		if strings.Contains(e.logs.String(), bad) {
			t.Errorf("the logs contain %q", bad)
		}
	}
}

func TestSMTPDelivery(t *testing.T) {
	smtp := testutil.NewFakeSMTP(t)
	smtp.Configure(func(f *testutil.FakeSMTP) { f.RequireTLS, f.Username, f.Password = true, "mailer", "s3cret" })
	cfg := config.SMTP{Host: smtp.Host, Port: smtp.Port, Username: "mailer", Password: "s3cret", From: "Test Portal <portal@example.com>"}
	e := newEnv(t, nil)
	e.svc.cfg.SMTP = cfg
	e.svc.SetMailer(&SMTPMailer{Config: cfg, RootCAs: smtp.Roots})
	team := e.team("ops")
	owner := e.user("none")
	e.member(team, owner, "owner")
	srv := e.server(team, "published")
	hostile := "Pay\r\nBcc: spy@example.com\r\nSubject: pwned <b>x</b>"
	e.emit(ServerUnhealthy, team, "m1", unhealthy(srv, hostile))
	if sum := e.runOnce(); sum.Sent != 1 {
		t.Fatalf("%+v", sum)
	}
	msgs := smtp.Messages()
	if len(msgs) != 1 || !msgs[0].TLS || msgs[0].User != "mailer" || len(msgs[0].To) != 1 {
		t.Fatalf("messages: %+v (STARTTLS and AUTH are required)", msgs)
	}
	head, body, _ := strings.Cut(msgs[0].Data, "\r\n\r\n")
	for _, line := range strings.Split(head, "\r\n") {
		if strings.HasPrefix(strings.ToLower(line), "bcc:") || strings.HasPrefix(strings.ToLower(line), "subject: pwned") {
			t.Errorf("a header was injected: %q", line)
		}
	}
	if !strings.Contains(head, "Auto-Submitted: auto-generated") || !strings.Contains(head, "X-OMP-Event: server.unhealthy") || !strings.Contains(head, "Subject: [Test Portal]") ||
		!strings.Contains(head, "From: \"Test Portal\" <portal@example.com>") && !strings.Contains(head, "From: Test Portal <portal@example.com>") {
		t.Errorf("headers:\n%s", head)
	}
	if raw, err := io.ReadAll(quotedprintable.NewReader(strings.NewReader(body))); err != nil || !strings.Contains(string(raw), "https://portal.example.com/teams/ops/servers/") {
		t.Errorf("the link is the portal's: %v", err)
	}
	if _, htmlPart, _ := strings.Cut(body, "text/html"); strings.Contains(htmlPart, "<b>x</b>") { // the plain-text part shows it literally; the HTML part escapes it
		t.Errorf("body:\n%s", body)
	}
	// The address is the profile's, read now: never in the outbox.
	if e.count(`SELECT count(*) FROM notification_outbox WHERE dedup_key LIKE '%@%'`) != 0 {
		t.Error("an address was stored in the outbox")
	}

	// No STARTTLS offered: the send fails (never a downgrade) and nothing is sent.
	plain := testutil.NewFakeSMTP(t)
	plain.Configure(func(f *testutil.FakeSMTP) { f.OfferStartTLS = false })
	m := &SMTPMailer{Config: config.SMTP{Host: plain.Host, Port: plain.Port, From: "p@example.com"}, RootCAs: plain.Roots}
	if merr := m.Send(context.Background(), Mail{To: "a@example.com", Subject: "x", Text: "t", HTML: "h"}); merr == nil || merr.Class != ClassTLS || len(plain.Messages()) != 0 {
		t.Errorf("without STARTTLS: %+v, %d message(s)", merr, len(plain.Messages()))
	}
	for _, cmd := range plain.Commands() {
		if cmd == "MAIL" || cmd == "AUTH" {
			t.Errorf("%s was sent without TLS", cmd)
		}
	}
	// An untrusted certificate is refused (there is no insecure switch).
	untrusted := &SMTPMailer{Config: cfg, RootCAs: x509.NewCertPool()}
	if merr := untrusted.Send(context.Background(), Mail{To: "a@example.com", Subject: "x", Text: "t", HTML: "h"}); merr == nil || merr.Class != ClassTLS {
		t.Errorf("an untrusted certificate: %+v", merr)
	}
	// Wrong password and a refused recipient.
	badPw := &SMTPMailer{Config: config.SMTP{Host: smtp.Host, Port: smtp.Port, Username: "mailer", Password: "wrong", From: "p@example.com"}, RootCAs: smtp.Roots}
	if merr := badPw.Send(context.Background(), Mail{To: "a@example.com", Subject: "x", Text: "t", HTML: "h"}); merr == nil || merr.Class != ClassAuth {
		t.Errorf("a wrong password: %+v", merr)
	}
	smtp.Configure(func(f *testutil.FakeSMTP) { f.RejectRcpt = true })
	if merr := (&SMTPMailer{Config: cfg, RootCAs: smtp.Roots}).Send(context.Background(), Mail{To: "a@example.com", Subject: "x", Text: "t", HTML: "h"}); merr == nil || merr.Class != ClassAddress || !merr.Permanent {
		t.Errorf("a refused recipient: %+v", merr)
	}
	// doctor's check: connect, EHLO, TLS, AUTH, QUIT, no message.
	smtp.Configure(func(f *testutil.FakeSMTP) { f.RejectRcpt = false })
	n := len(smtp.Messages())
	if merr := CheckWith(context.Background(), cfg, smtp.Roots); merr != nil {
		t.Errorf("check: %+v", merr)
	}
	if len(smtp.Messages()) != n {
		t.Error("the check sent a message")
	}
	if merr := CheckWith(context.Background(), cfg, nil); merr == nil || merr.Class != ClassTLS {
		t.Errorf("the check verifies certificates too: %+v", merr)
	}
	// Test email: to the caller's own address only, five an hour.
	pa := e.user("platform_admin")
	e.svc.SetMailer(&SMTPMailer{Config: cfg, RootCAs: smtp.Roots})
	var res TestResult
	var err error
	for i := 0; i < TestsEmailPerHour; i++ {
		if res, err = e.svc.TestEmail(context.Background(), adminActor(pa)); err != nil || !res.OK {
			t.Fatalf("test email %d: %+v %v", i, res, err)
		}
	}
	if _, err = e.svc.TestEmail(context.Background(), adminActor(pa)); err == nil || !strings.Contains(err.Error(), "rate_limited") {
		t.Errorf("the sixth test email: %v", err)
	}
	last := smtp.Messages()[len(smtp.Messages())-1]
	if want := e.exec(`SELECT email FROM users WHERE id = $1`, pa); len(last.To) != 1 || last.To[0] != want {
		t.Errorf("the test email went to %v, not the caller's %s", last.To, want)
	}
	if _, err := e.svc.TestEmail(context.Background(), personActor(owner)); err == nil {
		t.Error("only platform admins test email")
	}
}

func TestEmailNotConfigured(t *testing.T) {
	e := newEnv(t, nil)
	e.svc.cfg.SMTP = config.SMTP{}
	pa := e.user("platform_admin")
	if _, err := e.svc.TestEmail(context.Background(), adminActor(pa)); err == nil || !strings.Contains(err.Error(), "email_not_configured") {
		t.Errorf("err = %v", err)
	}
	team := e.team("ops")
	owner := e.user("none")
	e.member(team, owner, "owner")
	e.emit(ServerUnhealthy, team, "x", unhealthy(e.server(team, "published"), "S"))
	e.runOnce()
	if e.count(`SELECT count(*) FROM notification_outbox WHERE channel = 'email'`) != 0 || e.inbox(owner) != 1 {
		t.Error("without SMTP the inbox works and no email is queued")
	}
	st, err := e.svc.GetSettings(context.Background(), adminActor(pa))
	if err != nil || st.Email.Configured || st.Email.Host != "" {
		t.Errorf("%+v %v", st.Email, err)
	}
}

func TestRetentionPurge(t *testing.T) {
	e := newEnv(t, nil)
	team := e.team("ops")
	owner := e.user("none")
	e.member(team, owner, "owner")
	srv := e.server(team, "published")
	e.emit(ServerUnhealthy, team, "old", unhealthy(srv, "S"))
	e.emit(ServerUnhealthy, team, "new", unhealthy(srv, "S"))
	e.runOnce()
	old := e.exec(`SELECT id::text FROM notification_events WHERE dedup_key = 'old'`)
	e.run(`UPDATE notification_events SET occurred_at = now() - interval '15 days' WHERE id = $1::bigint`, old)
	e.run(`UPDATE notification_outbox SET created_at = now() - interval '15 days' WHERE event_id = $1::bigint`, old)
	e.run(`UPDATE notification_inbox SET created_at = now() - interval '91 days' WHERE event_id = $1::bigint`, old)
	kinds := map[string]int64{}
	if _, err := e.svc.Purge(context.Background(), 1, func(k string, n int64) { kinds[k] += n }); err != nil {
		t.Fatal(err)
	}
	if e.count(`SELECT count(*) FROM notification_events`) != 1 || e.count(`SELECT count(*) FROM notification_inbox`) != 1 || e.count(`SELECT count(*) FROM notification_outbox`) != 1 {
		t.Errorf("events %d inbox %d outbox %d, want 1 each", e.count(`SELECT count(*) FROM notification_events`), e.count(`SELECT count(*) FROM notification_inbox`), e.count(`SELECT count(*) FROM notification_outbox`))
	}
	if kinds["notification_inbox"] != 1 || kinds["notification_outbox"] != 1 {
		t.Errorf("counted: %v", kinds)
	}
	// An inbox row survives its event (the title and link were copied), up to 90 days.
	e.run(`UPDATE notification_events SET occurred_at = now() - interval '20 days'`)
	if _, err := e.svc.Purge(context.Background(), 100, nil); err != nil {
		t.Fatal(err)
	}
	if e.count(`SELECT count(*) FROM notification_events`) != 0 || e.count(`SELECT count(*) FROM notification_inbox`) != 1 {
		t.Error("the inbox outlives its event")
	}
}

func TestInboxOperations(t *testing.T) {
	e := newEnv(t, nil)
	team := e.team("ops")
	a, b := e.user("none"), e.user("none")
	e.member(team, a, "owner")
	e.member(team, b, "owner")
	srv := e.server(team, "published")
	for i := 0; i < 5; i++ {
		e.emit(ServerUnhealthy, team, fmt.Sprintf("i%d", i), unhealthy(srv, fmt.Sprintf("S%d", i)))
	}
	e.fanOutAll()
	ctx := context.Background()
	p, err := e.svc.ListInbox(ctx, personActor(a), false, 0, 2)
	if err != nil || len(p.Items) != 2 || !p.More || p.UnreadCount != 5 || p.Items[0].Row.ID < p.Items[1].Row.ID {
		t.Fatalf("%+v %v", p, err)
	}
	p2, _ := e.svc.ListInbox(ctx, personActor(a), false, p.Items[1].Row.ID, 10)
	if len(p2.Items) != 3 || p2.More {
		t.Errorf("the next page: %d items", len(p2.Items))
	}
	if p.Items[0].Link == nil || p.Items[0].Link.Type != LinkServer || p.Items[0].Link.TeamSlug != "ops" || p.Items[0].Team == nil || p.Items[0].Team.Slug != "ops" {
		t.Errorf("link %+v team %+v", p.Items[0].Link, p.Items[0].Team)
	}
	if err := e.svc.MarkRead(ctx, personActor(a), p.Items[0].Row.ID); err != nil {
		t.Fatal(err)
	}
	if err := e.svc.MarkRead(ctx, personActor(a), p.Items[0].Row.ID); err != nil {
		t.Errorf("marking read is idempotent: %v", err)
	}
	if err := e.svc.MarkRead(ctx, personActor(b), p.Items[1].Row.ID); err != nil && !strings.Contains(err.Error(), "notification_not_found") {
		t.Errorf("%v", err)
	}
	// B's row of the same event is a different row: marking A's row as B is a 404.
	var bRow int64
	_ = e.pool.QueryRow(ctx, `SELECT id FROM notification_inbox WHERE user_id = $1 ORDER BY id LIMIT 1`, b).Scan(&bRow)
	if err := e.svc.MarkRead(ctx, personActor(a), bRow); err == nil || !strings.Contains(err.Error(), "notification_not_found") {
		t.Errorf("another person's notification: %v", err)
	}
	if err := e.svc.MarkRead(ctx, personActor(a), 999999); err == nil {
		t.Error("a missing notification")
	}
	if n, _ := e.svc.UnreadCount(ctx, personActor(a)); n != 4 {
		t.Errorf("unread = %d", n)
	}
	unread, _ := e.svc.ListInbox(ctx, personActor(a), true, 0, 10)
	if len(unread.Items) != 4 || unread.UnreadCount != 4 {
		t.Errorf("unread list: %d", len(unread.Items))
	}
	if n, err := e.svc.MarkAllRead(ctx, personActor(a)); err != nil || n != 4 {
		t.Errorf("marked %d %v", n, err)
	}
	if n, _ := e.svc.MarkAllRead(ctx, personActor(a)); n != 0 {
		t.Error("read-all is idempotent")
	}
	if n, _ := e.svc.UnreadCount(ctx, personActor(b)); n != 5 {
		t.Error("b's unread count is b's own")
	}
	_ = uuid.Nil
}

package notify

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/ncecere/open-mcp-portal/internal/audit"
	"github.com/ncecere/open-mcp-portal/internal/store"
	"github.com/ncecere/open-mcp-portal/internal/store/dbgen"
)

// Outbox states.
const (
	StatePending = "pending"
	StateSent    = "sent"
	StateFailed  = "failed"
	StateSkipped = "skipped"
)

// Error classes (apitypes.NotificationErrorClass).
const (
	ClassNotAllowed  = "not_allowed"
	ClassConnect     = "connect"
	ClassTLS         = "tls"
	ClassTimeout     = "timeout"
	ClassAuth        = "auth"
	ClassRejected    = "rejected"
	ClassServerError = "server_error"
	ClassAddress     = "address"
	ClassExpired     = "expired"
	ClassInternal    = "internal"
)

// RunSummary is what one dispatcher run did.
type RunSummary struct {
	// Led is false when another replica held the lock.
	Led                                       bool
	FannedOut, Sent, Retried, Failed, Skipped int
	KeysEmitted                               int
}

// Run is one dispatcher pass if no other replica is running one
// (advisory lock omp.notifications): hourly, scan for expiring keys; fan
// out up to MaxFanOut events; send up to MaxSend due deliveries.
func (s *Service) Run(ctx context.Context) (RunSummary, error) {
	release, ok, err := store.TryAdvisoryLockText(ctx, s.pool, lockKey)
	if err != nil {
		return RunSummary{}, err
	}
	if s.ObserveLeader != nil {
		s.ObserveLeader(ok)
	}
	if !ok {
		return RunSummary{}, nil
	}
	defer release()
	sum := RunSummary{Led: true}
	if err := s.q.TouchNotificationRun(ctx); err != nil {
		return sum, err
	}
	state, err := s.q.GetNotificationState(ctx)
	if err != nil {
		return sum, err
	}
	if state.KeyScanAt == nil || s.now().Sub(*state.KeyScanAt) >= time.Hour {
		n, err := s.ScanKeys(ctx)
		sum.KeysEmitted = n
		if err != nil {
			s.log.WarnContext(ctx, "notifications: key expiry scan failed", "err", err)
		} else if err := s.q.TouchNotificationKeyScan(ctx); err != nil {
			return sum, err
		}
	}
	for i := 0; i < MaxFanOut; i++ {
		done, err := s.fanOutNext(ctx)
		if err != nil {
			return sum, err
		}
		if done {
			break
		}
		sum.FannedOut++
	}
	if err := s.send(ctx, &sum); err != nil {
		return sum, err
	}
	return sum, nil
}

// fanOutNext fans out the oldest unfanned event in a transaction of its own
// (done: there was none).
func (s *Service) fanOutNext(ctx context.Context) (done bool, err error) {
	err = store.InTx(ctx, s.pool, func(q *dbgen.Queries, _ pgx.Tx) error {
		ev, err := q.LockNextUnfannedEvent(ctx)
		if errors.Is(store.NotFound(err), store.ErrNotFound) {
			done = true
			return nil
		} else if err != nil {
			return err
		}
		if err := s.fanOut(ctx, q, ev); err != nil {
			return err
		}
		return q.MarkNotificationEventFannedOut(ctx, ev.ID)
	})
	return done, err
}

// teamOf loads the team of an event (nil: none, or deleted).
func teamOf(ctx context.Context, q *dbgen.Queries, ev dbgen.NotificationEvent) (*teamInfo, string, error) {
	if !ev.TeamID.Valid {
		return nil, "", nil
	}
	t, err := q.GetTeamByID(ctx, ev.TeamID.UUID)
	if errors.Is(store.NotFound(err), store.ErrNotFound) {
		return nil, "", nil
	} else if err != nil {
		return nil, "", err
	}
	return &teamInfo{ID: t.ID, Slug: t.Slug, Name: t.Name}, t.Status, nil
}

// fanOut resolves an event's recipients and writes their inbox rows and the
// email and webhook outbox rows (idempotent: unique (user, event) and the
// outbox's dedup keys).
func (s *Service) fanOut(ctx context.Context, q *dbgen.Queries, ev dbgen.NotificationEvent) error {
	team, status, err := teamOf(ctx, q, ev)
	if err != nil {
		return err
	}
	if ev.TeamID.Valid && (team == nil || status != "active") {
		return nil // an archived or deleted team gets nothing
	}
	msg := compose(ev, team)
	rs, err := s.recipients(ctx, q, ev)
	if err != nil {
		return err
	}
	prefs, err := s.channelPrefs(ctx, q, ev.Event, rs)
	if err != nil {
		return err
	}
	var link json.RawMessage
	if msg.Link != nil {
		if link, err = json.Marshal(msg.Link); err != nil {
			return err
		}
	}
	for _, r := range rs {
		if prefs[r.ID][ChannelInApp] {
			if _, err := q.InsertNotificationInbox(ctx, dbgen.InsertNotificationInboxParams{UserID: r.ID, EventID: ev.ID, Event: ev.Event,
				TeamID: ev.TeamID, Title: msg.Title, Body: msg.Body, Link: link}); err != nil {
				return err
			}
		}
		if !prefs[r.ID][ChannelEmail] || !s.EmailAvailable() || !r.Verified {
			continue
		}
		state := StatePending
		if n, err := q.EmailsQueuedForUserSince(ctx, uuid.NullUUID{UUID: r.ID, Valid: true}); err != nil {
			return err
		} else if n >= EmailsPerHour {
			state = StateSkipped // the inbox still has it
			s.observe(ChannelEmail, ev.Event, "skipped")
		}
		if _, err := q.InsertNotificationOutbox(ctx, dbgen.InsertNotificationOutboxParams{EventID: ev.ID, Channel: ChannelEmail,
			UserID: uuid.NullUUID{UUID: r.ID, Valid: true}, State: state, DedupKey: fmt.Sprintf("email:%d:%s", ev.ID, r.ID)}); err != nil {
			return err
		}
	}
	teamRoute, platformRoute, needTeamEvents := webhookRoute(ev)
	if !teamRoute && !platformRoute {
		return nil
	}
	p := dbgen.ListDestinationsForEventParams{Event: ev.Event, TeamEvent: needTeamEvents}
	if teamRoute {
		p.TeamID = ev.TeamID
	}
	dests, err := q.ListDestinationsForEvent(ctx, p)
	if err != nil {
		return err
	}
	for _, d := range dests {
		if d.Scope == "platform" && !platformRoute {
			continue
		}
		if _, err := q.InsertNotificationOutbox(ctx, dbgen.InsertNotificationOutboxParams{EventID: ev.ID, Channel: ChannelWebhook,
			DestinationID: uuid.NullUUID{UUID: d.ID, Valid: true}, State: StatePending, DedupKey: fmt.Sprintf("webhook:%d:%s", ev.ID, d.ID)}); err != nil {
			return err
		}
	}
	return nil
}

// result is the outcome of one delivery attempt.
type result struct {
	OK     bool
	Status int
	Class  string
	// Permanent: retrying can't help (a 4xx other than 408 and 429, an SMTP
	// 5xx).
	Permanent bool
	Message   string
}

// send delivers up to MaxSend due outbox rows, Parallel at a time.
func (s *Service) send(ctx context.Context, sum *RunSummary) error {
	if _, err := s.q.ExpireOldPendingNotificationOutbox(ctx); err != nil {
		return err
	}
	rows, err := s.q.ClaimDueNotificationOutbox(ctx, dbgen.ClaimDueNotificationOutboxParams{LeaseSeconds: lease, Batch: MaxSend, PerTarget: PerTarget})
	if err != nil {
		return err
	}
	// Rows are delivered in groups, one per destination (or person): a group
	// is sequential, groups run Parallel at a time, so one team's slow
	// endpoint holds one worker and the others' notifications go on.
	groups := map[string][]dbgen.NotificationOutbox{}
	var order []string
	for _, row := range rows {
		key := row.DestinationID.UUID.String() + "/" + row.UserID.UUID.String()
		if _, ok := groups[key]; !ok {
			order = append(order, key)
		}
		groups[key] = append(groups[key], row)
	}
	var (
		mu  sync.Mutex
		wg  sync.WaitGroup
		sem = make(chan struct{}, Parallel)
	)
	for _, key := range order {
		wg.Add(1)
		sem <- struct{}{}
		go func(group []dbgen.NotificationOutbox) {
			defer wg.Done()
			defer func() { <-sem }()
			for _, row := range group {
				state := s.deliver(ctx, row)
				mu.Lock()
				switch state {
				case StateSent:
					sum.Sent++
				case StateFailed:
					sum.Failed++
				case StateSkipped:
					sum.Skipped++
				default:
					sum.Retried++
				}
				mu.Unlock()
			}
		}(groups[key])
	}
	wg.Wait()
	return nil
}

// deliver makes one attempt for a claimed outbox row and records it. It
// returns the row's new state.
func (s *Service) deliver(ctx context.Context, row dbgen.NotificationOutbox) string {
	ev, err := s.q.GetNotificationEvent(ctx, row.EventID)
	if err != nil {
		return s.record(ctx, row, ev, result{Class: ClassInternal, Permanent: true})
	}
	if s.now().Sub(row.CreatedAt) > ExpireAfter {
		return s.record(ctx, row, ev, result{Class: ClassExpired, Permanent: true})
	}
	team, _, err := teamOf(ctx, s.q, ev)
	if err != nil {
		return s.record(ctx, row, ev, result{Class: ClassInternal})
	}
	msg := compose(ev, team)
	var res result
	switch row.Channel {
	case ChannelEmail:
		res = s.deliverEmail(ctx, row, ev, msg)
	case ChannelWebhook:
		res = s.deliverWebhook(ctx, row, ev, team, msg)
	default:
		res = result{Class: ClassInternal, Permanent: true}
	}
	return s.record(ctx, row, ev, res)
}

// skipped is the result of a delivery that no longer applies.
var skipped = result{Class: "skipped"}

// record stores an attempt's outcome: sent, failed (permanent, or after the
// last attempt), skipped, or pending again after the backoff.
func (s *Service) record(ctx context.Context, row dbgen.NotificationOutbox, ev dbgen.NotificationEvent, res result) string {
	if res.Class == "skipped" {
		p := dbgen.RecordNotificationOutboxResultParams{ID: row.ID, State: StateSkipped, Attempts: row.Attempts}
		if err := s.q.RecordNotificationOutboxResult(context.WithoutCancel(ctx), p); err != nil {
			s.log.WarnContext(ctx, "notifications: recording a skipped delivery failed", "err", err)
		}
		s.observe(row.Channel, ev.Event, "skipped")
		return StateSkipped
	}
	attempt := row.Attempts + 1
	p := dbgen.RecordNotificationOutboxResultParams{ID: row.ID, Attempts: attempt}
	if res.Status > 0 {
		st := int32(res.Status)
		p.HttpStatus = &st
	}
	outcome := "sent"
	switch {
	case res.OK:
		p.State = StateSent
	case res.Permanent || int(attempt) >= MaxAttempts:
		p.State = StateFailed
		cl := res.Class
		p.ErrorClass = &cl
		outcome = "failed"
	default:
		p.State = StatePending
		cl := res.Class
		p.ErrorClass = &cl
		next := s.now().Add(retryDelays[min(int(attempt), len(retryDelays))-1])
		p.NextAttemptAt = &next
		outcome = "retry"
	}
	bg := context.WithoutCancel(ctx)
	if err := s.q.RecordNotificationOutboxResult(bg, p); err != nil {
		s.log.WarnContext(ctx, "notifications: recording a delivery failed", "err", err)
	}
	s.observe(row.Channel, ev.Event, outcome)
	if row.Channel == ChannelWebhook && row.DestinationID.Valid {
		s.destinationHealth(bg, row.DestinationID.UUID, res.OK, res.Class)
	}
	return p.State
}

// destinationHealth keeps a destination's failure count; 20 failed attempts
// in a row stop it (disabledReason failures, audited as
// notification_destination.disable).
func (s *Service) destinationHealth(ctx context.Context, id uuid.UUID, ok bool, class string) {
	if ok {
		if err := s.q.RecordDestinationSuccess(ctx, id); err != nil {
			s.log.WarnContext(ctx, "notifications: recording a destination success failed", "err", err)
		}
		return
	}
	d, err := s.q.RecordDestinationFailure(ctx, dbgen.RecordDestinationFailureParams{ID: id, ErrorClass: &class})
	if err != nil {
		if !errors.Is(store.NotFound(err), store.ErrNotFound) {
			s.log.WarnContext(ctx, "notifications: recording a destination failure failed", "err", err)
		}
		return
	}
	if d.ConsecutiveFailures < StopAfter || !d.Enabled {
		return
	}
	err = store.InTx(ctx, s.pool, func(q *dbgen.Queries, _ pgx.Tx) error {
		stopped, err := q.StopNotificationDestination(ctx, id)
		if errors.Is(store.NotFound(err), store.ErrNotFound) {
			return nil // another run stopped it
		} else if err != nil {
			return err
		}
		return audit.Record(ctx, q, audit.Entry{ActorKind: audit.ActorSystem, Action: "notification_destination.disable",
			TargetType: "notification_destination", TargetID: id.String(), TeamID: stopped.TeamID.UUID,
			Before: map[string]any{"enabled": true}, After: map[string]any{"enabled": false, "disabledReason": "failures"},
			Metadata: map[string]any{"name": stopped.Name, "reason": "failures", "failures": stopped.ConsecutiveFailures, "lastErrorClass": class}})
	})
	if err != nil {
		s.log.WarnContext(ctx, "notifications: stopping a destination failed", "err", err)
	}
}

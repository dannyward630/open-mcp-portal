package notify

import (
	"context"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/ncecere/open-mcp-portal/internal/apperr"
	"github.com/ncecere/open-mcp-portal/internal/audit"
	"github.com/ncecere/open-mcp-portal/internal/authz"
	"github.com/ncecere/open-mcp-portal/internal/store"
	"github.com/ncecere/open-mcp-portal/internal/store/dbgen"
)

// Settings is Admin > Notifications: whether email is on (never the
// username or password), the delivery queue and the dispatcher's heartbeat.
type Settings struct {
	Email struct {
		Configured    bool
		From, Host    string
		Port          int
		TLS           string
		Authenticated bool
		SentLast24h   int64
		FailedLast24h int64
	}
	TeamDestinations, PlatformDestinations, Stopped int64
	Pending, FailedLast24h                          int64
	OldestPendingAt, LastRunAt                      *time.Time
	IntervalSeconds                                 int32
}

// GetSettings reads the settings (platform admins and auditors).
func (s *Service) GetSettings(ctx context.Context, a authz.Actor) (Settings, error) {
	var out Settings
	if !a.CanReadPlatform() {
		return out, apperr.Forbidden("Only platform admins and auditors can see the notification settings")
	}
	cfg := s.cfg.SMTP
	out.Email.Configured, out.Email.Authenticated = cfg.Enabled(), cfg.Authenticates()
	if cfg.Enabled() {
		out.Email.From, out.Email.Host, out.Email.Port, out.Email.TLS = cfg.From, cfg.Host, cfg.EffectivePort(), cfg.Mode()
	}
	qs, err := s.q.NotificationQueueStats(ctx)
	if err != nil {
		return out, err
	}
	ds, err := s.q.NotificationDestinationStats(ctx)
	if err != nil {
		return out, err
	}
	st, err := s.q.GetNotificationState(ctx)
	if err != nil {
		return out, err
	}
	out.Email.SentLast24h, out.Email.FailedLast24h = qs.EmailSent24h, qs.EmailFailed24h
	out.TeamDestinations, out.PlatformDestinations, out.Stopped = ds.TeamDestinations, ds.PlatformDestinations, ds.Stopped
	out.Pending, out.FailedLast24h = qs.Pending, qs.Failed24h
	if qs.OldestPendingAt.Unix() > 0 {
		t := qs.OldestPendingAt
		out.OldestPendingAt = &t
	}
	out.LastRunAt, out.IntervalSeconds = st.LastRunAt, int32(s.cfg.Interval/time.Second)
	return out, nil
}

// TestsEmailPerHour is how many test emails an admin may send per hour.
const TestsEmailPerHour = 5

// TestEmail sends a test message to the caller's own address (never one from
// the request) and reports the outcome: errorClass is empty on success.
// 409 email_not_configured without SMTP_HOST; 429 rate_limited after 5 per
// hour per admin. Audited as settings.notifications_test_email.
func (s *Service) TestEmail(ctx context.Context, a authz.Actor) (TestResult, error) {
	if a.IsToken() {
		return TestResult{}, errSession
	}
	if !a.IsPlatformAdmin() {
		return TestResult{}, apperr.Forbidden("Only platform admins can send a test email")
	}
	if !s.EmailAvailable() {
		return TestResult{}, apperr.Conflict("email_not_configured", "Email isn't configured: the operator sets SMTP_HOST")
	}
	var user dbgen.GetNotificationUserRow
	err := store.InTx(ctx, s.pool, func(q *dbgen.Queries, _ pgx.Tx) error {
		n, err := q.CountRecentAudit(ctx, dbgen.CountRecentAuditParams{Action: "settings.notifications_test_email", ActorUserID: nullUUID(a.UserID)})
		if err != nil {
			return err
		}
		if n >= TestsEmailPerHour {
			e := apperr.New(http.StatusTooManyRequests, "rate_limited", "At most 5 test emails per hour")
			e.RetryAfter = time.Minute
			return e
		}
		if user, err = q.GetNotificationUser(ctx, a.UserID); err != nil {
			return err
		}
		e := a.Audit("settings.notifications_test_email", "platform_settings", "notifications")
		e.Metadata = map[string]any{"name": "Notifications"}
		return audit.Record(ctx, q, e)
	})
	if err != nil {
		return TestResult{}, err
	}
	start := time.Now()
	msg := Message{Title: "Test email", Body: "This is a test email from " + s.cfg.InstanceName + ". If you can read it, email works."}
	text, html, err := s.render(msg)
	if err != nil {
		return TestResult{}, err
	}
	merr := s.mail.Send(ctx, Mail{To: user.Email, ToName: user.DisplayName, Subject: s.subject(msg.Title), Event: Test, Text: text, HTML: html})
	out := TestResult{OK: merr == nil, DurationMs: int(time.Since(start).Milliseconds()), Message: "The test email was sent to your address."}
	if merr != nil {
		out.Message, out.ErrorClass = "The test email couldn't be sent.", merr.Class
	}
	return out, nil
}

// Purge deletes what retention says: inbox rows after 90 days, events and
// outbox rows after 14, in batches of batch (the retention loop's step; it
// counts omp_retention_deleted_total{kind=notification_inbox|notification_outbox}
// through observe).
func (s *Service) Purge(ctx context.Context, batch int32, observe func(kind string, n int64)) (int64, error) {
	var total int64
	for _, k := range []struct {
		kind string
		fn   func(context.Context, int32) (int64, error)
	}{
		{"notification_inbox", s.q.PurgeNotificationInbox},
		{"notification_outbox", s.q.PurgeNotificationOutbox},
		{"notification_events", s.q.PurgeNotificationEvents},
	} {
		for i := 0; i < 100; i++ {
			n, err := k.fn(ctx, batch)
			if err != nil {
				return total, err
			}
			total += n
			if observe != nil && n > 0 && k.kind != "notification_events" {
				observe(k.kind, n)
			}
			if n < int64(batch) {
				break
			}
		}
	}
	return total, nil
}

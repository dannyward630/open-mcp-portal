package notify

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/ncecere/open-mcp-portal/internal/apperr"
	"github.com/ncecere/open-mcp-portal/internal/audit"
	"github.com/ncecere/open-mcp-portal/internal/authz"
	"github.com/ncecere/open-mcp-portal/internal/httpapi/apitypes"
	"github.com/ncecere/open-mcp-portal/internal/httpx"
	"github.com/ncecere/open-mcp-portal/internal/store"
	"github.com/ncecere/open-mcp-portal/internal/store/dbgen"
	"github.com/ncecere/open-mcp-portal/internal/teams"
)

// Webhook destinations (docs/notifications.md): team destinations are
// managed by the team's owners and admins, platform destinations by
// platform admins; platform staff read any. The URL and the signing secret
// are sealed with ENCRYPTION_KEY and never returned.

// TestsPerHour is how many tests one destination takes per hour.
const TestsPerHour = 10

var (
	errTeamNotFound = apperr.NotFound("team_not_found", "Team not found")
	errNotFound     = apperr.NotFound("destination_not_found", "Destination not found")
	errAdminOnly    = apperr.Forbidden("Only platform admins can change platform destinations")
	errPlatformRead = apperr.Forbidden("Only platform admins and auditors can see platform destinations")
	errArchived     = apperr.Conflict("team_archived", "This team is archived and read-only")
	errName         = apperr.Invalid("invalid_name", "Name must be 1-100 characters on one line")
	errFormat       = apperr.Invalid("invalid_format", "format must be json or slack")
	errNameTaken    = apperr.Conflict("name_taken", "Another destination already uses that name")
	errTooMany      = apperr.Conflict("too_many_destinations", "At most 10 destinations per team and 10 for the platform")
	errNotSigned    = apperr.Conflict("not_signed", "Slack destinations are unsigned: there is no secret to rotate")
	errSession      = apperr.New(http.StatusForbidden, "session_required", "Notification settings are changed in a browser session only")
)

// Destination is a destination as the API shows it.
type Destination struct {
	D                  dbgen.NotificationDestination
	TeamSlug, TeamName string
}

// Scope selects team destinations (TeamRef) or the platform's ("").
type Scope struct{ TeamRef string }

func (sc Scope) platform() bool { return sc.TeamRef == "" }

// access resolves the destination scope for an actor: write (and which
// team) or read.
type access struct {
	team  dbgen.Team
	write bool
}

func (s *Service) access(ctx context.Context, q *dbgen.Queries, a authz.Actor, sc Scope, write, lock bool) (access, error) {
	// Tokens read platform destinations (platform:read: the URL is sealed and
	// never returned, so there is nothing secret to read) and never change
	// anything or touch a team's, whatever their scopes.
	if a.IsToken() && (write || !sc.platform()) {
		return access{}, errSession
	}
	if sc.platform() {
		switch {
		case write && !a.IsPlatformAdmin():
			return access{}, errAdminOnly
		case !a.CanReadPlatform():
			return access{}, errPlatformRead
		}
		return access{write: a.IsPlatformAdmin()}, nil
	}
	t, err := teams.Resolve(ctx, q, sc.TeamRef)
	if err != nil {
		return access{}, err
	}
	if lock {
		if t, err = q.LockTeam(ctx, t.ID); err != nil {
			return access{}, err
		}
	}
	role, err := teams.MemberRole(ctx, q, t.ID, a.UserID)
	if err != nil {
		return access{}, err
	}
	canWrite := authz.RoleAtLeast(role, authz.RoleAdmin)
	switch {
	case role == "" && !a.CanReadPlatform():
		return access{}, errTeamNotFound
	case !canWrite && !a.CanReadPlatform():
		return access{}, apperr.Forbidden("Only team owners and admins can see the team's notification destinations")
	case write && !canWrite:
		return access{}, apperr.Forbidden("Only team owners and admins can change the team's notification destinations")
	case write && t.Status != teams.StatusActive:
		return access{}, errArchived
	}
	return access{team: t, write: canWrite}, nil
}

func (ac access) teamID() uuid.NullUUID {
	return uuid.NullUUID{UUID: ac.team.ID, Valid: ac.team.ID != uuid.Nil}
}

func (s *Service) view(d dbgen.NotificationDestination, team dbgen.Team) Destination {
	return Destination{D: d, TeamSlug: team.Slug, TeamName: team.Name}
}

// destinationFor loads a destination of the scope (404 for another scope's).
func destinationFor(ctx context.Context, q *dbgen.Queries, ac access, id uuid.UUID, lock bool) (dbgen.NotificationDestination, error) {
	var d dbgen.NotificationDestination
	var err error
	if lock {
		d, err = q.LockNotificationDestination(ctx, id)
	} else {
		d, err = q.GetNotificationDestination(ctx, id)
	}
	if errors.Is(store.NotFound(err), store.ErrNotFound) {
		return d, errNotFound
	} else if err != nil {
		return d, err
	}
	if (ac.team.ID == uuid.Nil && d.Scope != "platform") || (ac.team.ID != uuid.Nil && d.TeamID.UUID != ac.team.ID) {
		return d, errNotFound
	}
	return d, nil
}

// snapshot is a destination's audited form: never the URL or a secret.
func snapshot(d dbgen.NotificationDestination) map[string]any {
	return map[string]any{"name": d.Name, "format": d.Format, "events": d.Events, "teamEvents": d.TeamEvents, "enabled": d.Enabled, "urlDisplay": d.UrlDisplay}
}

func (s *Service) audit(ctx context.Context, q *dbgen.Queries, a authz.Actor, action string, d dbgen.NotificationDestination, before, after any, meta map[string]any) error {
	e := a.Audit(action, "notification_destination", d.ID.String())
	e.TeamID = d.TeamID.UUID
	e.Before, e.After = before, after
	e.Metadata = map[string]any{"name": d.Name}
	for k, v := range meta {
		e.Metadata[k] = v
	}
	return audit.Record(ctx, q, e)
}

// List lists a scope's destinations.
func (s *Service) List(ctx context.Context, a authz.Actor, sc Scope) ([]Destination, error) {
	ac, err := s.access(ctx, s.q, a, sc, false, false)
	if err != nil {
		return nil, err
	}
	rows, err := s.q.ListNotificationDestinations(ctx, ac.teamID())
	if err != nil {
		return nil, err
	}
	out := make([]Destination, len(rows))
	for i, r := range rows {
		out[i] = s.view(r, ac.team)
	}
	return out, nil
}

// Get returns one destination.
func (s *Service) Get(ctx context.Context, a authz.Actor, sc Scope, id uuid.UUID) (Destination, error) {
	ac, err := s.access(ctx, s.q, a, sc, false, false)
	if err != nil {
		return Destination{}, err
	}
	d, err := destinationFor(ctx, s.q, ac, id, false)
	return s.view(d, ac.team), err
}

// Input is a new destination, or the changes of one.
type Input struct {
	Name       *string
	Format     string
	URL        *string
	Events     *[]string
	TeamEvents *bool
	Enabled    *bool
}

func checkName(name string) (string, error) {
	name = strings.TrimSpace(name)
	if n := utf8.RuneCountInString(name); n < 1 || n > 100 || strings.ContainsAny(name, "\r\n\x00") {
		return "", errName
	}
	return name, nil
}

// checkEvents validates the events a destination takes (400 invalid_event
// for an unknown one, 422 invalid_events for one it can't receive, or none).
func checkEvents(events []string, platform, teamEventsOn bool) error {
	for _, e := range events {
		if !ValidEvent(e) {
			return apperr.Invalid("invalid_event", "Unknown event "+clip(e, 40))
		}
	}
	allowed := TeamDestinationEvents
	if platform {
		allowed = PlatformDestinationEvents(teamEventsOn)
	}
	bad := []string{}
	for _, e := range events {
		if !slices.Contains(allowed, e) {
			bad = append(bad, e)
		}
	}
	if len(events) == 0 || len(bad) > 0 {
		err := apperr.New(http.StatusUnprocessableEntity, "invalid_events", "This destination can't receive "+strings.Join(bad, ", ")+": choose at least one event it can receive")
		if len(events) == 0 {
			err.Message = "Choose at least one event"
		}
		err.Details = map[string]any{"events": bad}
		return err
	}
	return nil
}

func dedupe(in []string) []string {
	out := make([]string, 0, len(in))
	for _, e := range in {
		if !slices.Contains(out, e) {
			out = append(out, e)
		}
	}
	return out
}

func lastFour(secret string) string {
	if len(secret) <= 4 {
		return secret
	}
	return secret[len(secret)-4:]
}

func newSecret() string { return SecretPrefix + httpx.NewID(32) }

// Create makes a destination (the scope's writers; at most 10 per scope;
// 409 team_archived). A json destination gets a signing secret, returned
// once. Audited as notification_destination.create, without URL or secret.
func (s *Service) Create(ctx context.Context, a authz.Actor, sc Scope, in Input) (Destination, string, error) {
	name, err := checkName(deref(in.Name))
	if err != nil {
		return Destination{}, "", err
	}
	if in.Format != "json" && in.Format != "slack" {
		return Destination{}, "", errFormat
	}
	teamEvents := in.TeamEvents != nil && *in.TeamEvents && sc.platform()
	if in.Events == nil {
		in.Events = &[]string{}
	}
	events := dedupe(*in.Events)
	if err := checkEvents(events, sc.platform(), teamEvents); err != nil {
		return Destination{}, "", err
	}
	raw := deref(in.URL)
	if err := s.CheckURL(ctx, raw); err != nil {
		return Destination{}, "", err
	}
	id := uuid.New()
	urlCipher, err := s.box.Seal([]byte(strings.TrimSpace(raw)), URLAAD(id))
	if err != nil {
		return Destination{}, "", err
	}
	p := dbgen.InsertNotificationDestinationParams{ID: id, Format: in.Format, Name: name, UrlCipher: urlCipher, UrlDisplay: display(raw), Events: events,
		TeamEvents: teamEvents, Enabled: in.Enabled == nil || *in.Enabled}
	secret := ""
	if in.Format == "json" {
		secret = newSecret()
		if p.SecretCipher, err = s.box.Seal([]byte(secret), SecretAAD(id)); err != nil {
			return Destination{}, "", err
		}
		p.SecretHint = lastFour(secret)
	}
	var out Destination
	err = store.InTx(ctx, s.pool, func(q *dbgen.Queries, _ pgx.Tx) error {
		ac, err := s.access(ctx, q, a, sc, true, true)
		if err != nil {
			return err
		}
		if sc.platform() {
			// Serialised by an advisory lock, like the platform's tokens.
			if err := q.LockPlatformNotificationDestinations(ctx); err != nil {
				return err
			}
		}
		n, err := q.CountNotificationDestinations(ctx, ac.teamID())
		if err != nil {
			return err
		}
		if n >= MaxDestinations {
			return errTooMany
		}
		p.Scope, p.TeamID = "platform", ac.teamID()
		if !sc.platform() {
			p.Scope = "team"
		}
		creator, _ := q.GetNotificationActorName(ctx, a.UserID)
		p.CreatedBy, p.CreatedByName = uuid.NullUUID{UUID: a.UserID, Valid: true}, &creator
		d, err := q.InsertNotificationDestination(ctx, p)
		if apperr.IsUniqueViolation(err, "notification_destinations_name_key") {
			return errNameTaken
		} else if err != nil {
			return err
		}
		out = s.view(d, ac.team)
		return s.audit(ctx, q, a, "notification_destination.create", d, nil, snapshot(d), nil)
	})
	return out, secret, err
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// Update changes a destination (If-Match revision). A new url replaces the
// sealed one (checked as on creation); enabling a destination that was
// stopped after repeated failures clears the failure count.
func (s *Service) Update(ctx context.Context, a authz.Actor, sc Scope, id uuid.UUID, in Input, revision int64) (Destination, error) {
	var newCipher []byte
	var newDisplay string
	if in.URL != nil {
		if err := s.CheckURL(ctx, *in.URL); err != nil {
			return Destination{}, err
		}
		var err error
		if newCipher, err = s.box.Seal([]byte(strings.TrimSpace(*in.URL)), URLAAD(id)); err != nil {
			return Destination{}, err
		}
		newDisplay = display(*in.URL)
	}
	var name string
	if in.Name != nil {
		var err error
		if name, err = checkName(*in.Name); err != nil {
			return Destination{}, err
		}
	}
	var out Destination
	err := store.InTx(ctx, s.pool, func(q *dbgen.Queries, _ pgx.Tx) error {
		ac, err := s.access(ctx, q, a, sc, true, false)
		if err != nil {
			return err
		}
		cur, err := destinationFor(ctx, q, ac, id, true)
		if err != nil {
			return err
		}
		if cur.Revision != revision {
			return apperr.Stale()
		}
		p := dbgen.UpdateNotificationDestinationParams{ID: id, Name: cur.Name, UrlCipher: cur.UrlCipher, UrlDisplay: cur.UrlDisplay, Events: cur.Events,
			TeamEvents: cur.TeamEvents, Enabled: cur.Enabled, DisabledReason: cur.DisabledReason, ConsecutiveFailures: cur.ConsecutiveFailures,
			UpdatedBy: uuid.NullUUID{UUID: a.UserID, Valid: true}}
		if in.Name != nil {
			p.Name = name
		}
		if in.URL != nil {
			p.UrlCipher, p.UrlDisplay = newCipher, newDisplay
		}
		if in.TeamEvents != nil && sc.platform() {
			p.TeamEvents = *in.TeamEvents
		}
		if in.Events != nil {
			p.Events = dedupe(*in.Events)
		}
		if in.Events != nil || in.TeamEvents != nil {
			if err := checkEvents(p.Events, sc.platform(), p.TeamEvents); err != nil {
				return err
			}
		}
		if in.Enabled != nil {
			p.Enabled = *in.Enabled
			if *in.Enabled && cur.DisabledReason != nil {
				p.DisabledReason, p.ConsecutiveFailures = nil, 0
			}
			if !*in.Enabled && cur.Enabled {
				p.DisabledReason = nil // switched off by a person
			}
		}
		d, err := q.UpdateNotificationDestination(ctx, p)
		if apperr.IsUniqueViolation(err, "notification_destinations_name_key") {
			return errNameTaken
		} else if err != nil {
			return err
		}
		out = s.view(d, ac.team)
		return s.audit(ctx, q, a, "notification_destination.update", d, snapshot(cur), snapshot(d), nil)
	})
	return out, err
}

// Delete removes a destination; deliveries still waiting for it are dropped.
func (s *Service) Delete(ctx context.Context, a authz.Actor, sc Scope, id uuid.UUID) error {
	return store.InTx(ctx, s.pool, func(q *dbgen.Queries, _ pgx.Tx) error {
		ac, err := s.access(ctx, q, a, sc, true, false)
		if err != nil {
			return err
		}
		cur, err := destinationFor(ctx, q, ac, id, true)
		if err != nil {
			return err
		}
		if err := q.DeleteNotificationDestination(ctx, id); err != nil {
			return err
		}
		return s.audit(ctx, q, a, "notification_destination.delete", cur, snapshot(cur), nil, nil)
	})
}

// RotateSecret makes a new signing secret, returned once. For graceHours the
// old one keeps signing too (v1=<new>, v1=<old>); 0 ends it now. 409
// not_signed for a Slack destination.
func (s *Service) RotateSecret(ctx context.Context, a authz.Actor, sc Scope, id uuid.UUID, graceHours int) (Destination, string, error) {
	if graceHours < 0 || graceHours > 72 {
		return Destination{}, "", apperr.Invalid("invalid_grace", "graceHours must be between 0 and 72")
	}
	var out Destination
	secret := newSecret()
	err := store.InTx(ctx, s.pool, func(q *dbgen.Queries, _ pgx.Tx) error {
		ac, err := s.access(ctx, q, a, sc, true, false)
		if err != nil {
			return err
		}
		cur, err := destinationFor(ctx, q, ac, id, true)
		if err != nil {
			return err
		}
		if cur.Format != "json" {
			return errNotSigned
		}
		p := dbgen.RotateNotificationDestinationSecretParams{ID: id, SecretHint: lastFour(secret), UpdatedBy: uuid.NullUUID{UUID: a.UserID, Valid: true}}
		if p.SecretCipher, err = s.box.Seal([]byte(secret), SecretAAD(id)); err != nil {
			return err
		}
		if graceHours > 0 {
			old, err := s.box.Open(cur.SecretCipher, SecretAAD(id))
			if err != nil {
				return err
			}
			if p.SecretPreviousCipher, err = s.box.Seal(old, PreviousSecretAAD(id)); err != nil {
				return err
			}
			until := s.now().Add(time.Duration(graceHours) * time.Hour)
			p.SecretPreviousUntil = &until
		}
		d, err := q.RotateNotificationDestinationSecret(ctx, p)
		if err != nil {
			return err
		}
		out = s.view(d, ac.team)
		return s.audit(ctx, q, a, "notification_destination.rotate_secret", d, nil, nil, map[string]any{"graceHours": graceHours})
	})
	return out, secret, err
}

// TestResult is the outcome of a test send.
type TestResult struct {
	OK         bool
	ErrorClass string
	HTTPStatus int
	DurationMs int
	Message    string
}

// fixed explanations, safe to show: never the destination's response.
func explain(res result) string {
	switch {
	case res.OK:
		return "The destination accepted the test."
	case res.Message != "":
		return res.Message
	}
	switch res.Class {
	case ClassRejected:
		return "The destination refused the request."
	case ClassServerError:
		return "The destination answered with a server error."
	}
	return "The test failed."
}

// Test sends a test payload straight away, through the same address check
// and signing as real deliveries, and reports the outcome (not stored in the
// delivery log). 429 rate_limited after 10 tests per destination per hour;
// 409 team_archived.
func (s *Service) Test(ctx context.Context, a authz.Actor, sc Scope, id uuid.UUID) (TestResult, error) {
	var d dbgen.NotificationDestination
	var team dbgen.Team
	err := store.InTx(ctx, s.pool, func(q *dbgen.Queries, _ pgx.Tx) error {
		ac, err := s.access(ctx, q, a, sc, true, false)
		if err != nil {
			return err
		}
		if d, err = destinationFor(ctx, q, ac, id, true); err != nil {
			return err
		}
		team = ac.team
		n, err := q.CountRecentAudit(ctx, dbgen.CountRecentAuditParams{Action: "notification_destination.test", TargetID: ptr(id.String())})
		if err != nil {
			return err
		}
		if n >= TestsPerHour {
			e := apperr.New(http.StatusTooManyRequests, "rate_limited", "At most 10 tests per destination per hour")
			e.RetryAfter = time.Minute
			return e
		}
		// Counted now, so tests that fail count too.
		return s.audit(ctx, q, a, "notification_destination.test", d, nil, nil, nil)
	})
	if err != nil {
		return TestResult{}, err
	}
	start := time.Now()
	msg := Message{Title: "Test notification", Body: "This is a test from " + s.cfg.InstanceName + ". If you can read it, the destination works."}
	var body []byte
	testID := uuid.New()
	if d.Format == "slack" {
		body = slackBody(s.slackText(msg))
	} else {
		ev := dbgen.NotificationEvent{PublicID: testID, Event: Test, OccurredAt: s.now(), Payload: mustJSON(apitypes.NotificationTestData{Message: msg.Body})}
		var tm *teamInfo
		if team.ID != uuid.Nil {
			tm = &teamInfo{ID: team.ID, Slug: team.Slug, Name: team.Name}
		}
		if body, err = s.payload(ev, tm, msg); err != nil {
			return TestResult{}, err
		}
	}
	res := s.sendWebhook(ctx, d, Test, testID, 0, 1, body)
	out := TestResult{OK: res.OK, HTTPStatus: res.Status, DurationMs: int(time.Since(start).Milliseconds()), Message: explain(res)}
	if !res.OK {
		out.ErrorClass = res.Class
	}
	return out, nil
}

// Delivery is one row of a destination's delivery log.
type Delivery = dbgen.ListNotificationDeliveriesRow

// Deliveries lists the last 50 queue rows of a destination (14 days):
// state, attempts, HTTP status and error class; never payloads or
// responses.
func (s *Service) Deliveries(ctx context.Context, a authz.Actor, sc Scope, id uuid.UUID) ([]Delivery, error) {
	ac, err := s.access(ctx, s.q, a, sc, false, false)
	if err != nil {
		return nil, err
	}
	if _, err := destinationFor(ctx, s.q, ac, id, false); err != nil {
		return nil, err
	}
	return s.q.ListNotificationDeliveries(ctx, dbgen.ListNotificationDeliveriesParams{DestinationID: uuid.NullUUID{UUID: id, Valid: true}, PageSize: 50})
}

func mustJSON(v any) json.RawMessage {
	b, _ := json.Marshal(v)
	return b
}

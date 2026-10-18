package notify

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"slices"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/ncecere/open-mcp-portal/internal/apperr"
	"github.com/ncecere/open-mcp-portal/internal/authz"
	"github.com/ncecere/open-mcp-portal/internal/store"
	"github.com/ncecere/open-mcp-portal/internal/store/dbgen"
)

// The in-app inbox and the person's preferences: personal, never audited.
// Every operation is about the caller's own rows (404 notification_not_found
// for another person's).

var errNoNotification = apperr.NotFound("notification_not_found", "Notification not found")

func ignoreNoRows(err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	return err
}

// Item is one inbox row with its team.
type Item struct {
	Row   dbgen.ListNotificationInboxRow
	Link  *Link
	Team  *teamInfo
	Event string
}

// Page is one page of the inbox and the caller's total unread count.
type Page struct {
	Items       []Item
	More        bool
	UnreadCount int64
}

// ListInbox lists the caller's notifications newest first, limit at a time
// after beforeID (0: from the newest).
func (s *Service) ListInbox(ctx context.Context, a authz.Actor, unreadOnly bool, beforeID int64, limit int32) (Page, error) {
	p := dbgen.ListNotificationInboxParams{UserID: a.UserID, UnreadOnly: unreadOnly, PageSize: limit + 1}
	if beforeID > 0 {
		p.BeforeID = &beforeID
	}
	rows, err := s.q.ListNotificationInbox(ctx, p)
	if err != nil {
		return Page{}, err
	}
	out := Page{}
	if len(rows) > int(limit) {
		rows, out.More = rows[:limit], true
	}
	for _, r := range rows {
		it := Item{Row: r, Event: r.Event}
		if len(r.Link) > 0 {
			var l Link
			if json.Unmarshal(r.Link, &l) == nil && l.Type != "" {
				it.Link = &l
			}
		}
		if r.TeamID.Valid && r.TeamSlug != "" {
			it.Team = &teamInfo{ID: r.TeamID.UUID, Slug: r.TeamSlug, Name: r.TeamName}
		}
		out.Items = append(out.Items, it)
	}
	out.UnreadCount, err = s.q.CountUnreadNotifications(ctx, a.UserID)
	return out, err
}

// UnreadCount is the caller's unread notifications.
func (s *Service) UnreadCount(ctx context.Context, a authz.Actor) (int64, error) {
	return s.q.CountUnreadNotifications(ctx, a.UserID)
}

// MarkRead marks one of the caller's notifications read (idempotent; 404
// for another person's, or one that is gone).
func (s *Service) MarkRead(ctx context.Context, a authz.Actor, id int64) error {
	n, err := s.q.MarkNotificationRead(ctx, dbgen.MarkNotificationReadParams{ID: id, UserID: a.UserID})
	if err != nil {
		return err
	}
	if n == 0 {
		return errNoNotification
	}
	return nil
}

// MarkAllRead marks every unread notification of the caller read and says
// how many changed.
func (s *Service) MarkAllRead(ctx context.Context, a authz.Actor) (int64, error) {
	return s.q.MarkAllNotificationsRead(ctx, a.UserID)
}

// Preference is a person's choice for one event.
type Preference struct {
	Event        string
	InApp, Email bool
	Custom       bool
}

// Preferences are a person's choices for the events they can receive.
type Preferences struct {
	EmailAvailable bool
	EmailAddress   string
	Items          []Preference
}

func (s *Service) preferences(ctx context.Context, q *dbgen.Queries, userID uuid.UUID) (Preferences, error) {
	u, err := q.GetNotificationUser(ctx, userID)
	if err != nil {
		return Preferences{}, err
	}
	events, err := s.receivable(ctx, userID, u.PlatformRole)
	if err != nil {
		return Preferences{}, err
	}
	rows, err := q.ListNotificationPreferences(ctx, userID)
	if err != nil {
		return Preferences{}, err
	}
	out := Preferences{EmailAvailable: s.EmailAvailable(), EmailAddress: u.Email, Items: make([]Preference, 0, len(events))}
	for _, e := range events {
		p := Preference{Event: e, InApp: true, Email: true}
		for _, r := range rows {
			if r.Event != e {
				continue
			}
			p.Custom = true
			if r.Channel == ChannelInApp {
				p.InApp = r.Enabled
			} else {
				p.Email = r.Enabled
			}
		}
		out.Items = append(out.Items, p)
	}
	return out, nil
}

// GetPreferences lists the events the caller can receive with their
// in-app and email choices (defaults: both on).
func (s *Service) GetPreferences(ctx context.Context, a authz.Actor) (Preferences, error) {
	return s.preferences(ctx, s.q, a.UserID)
}

// Change is one event's new choices (nil: keep).
type Change struct {
	Event        string
	InApp, Email *bool
}

// UpdatePreferences changes the caller's choices. 400 invalid_event for an
// unknown event; 422 not_receivable (details.events) for one the caller
// can't receive. Only choices that differ from the defaults are stored.
func (s *Service) UpdatePreferences(ctx context.Context, a authz.Actor, changes []Change) (Preferences, error) {
	var out Preferences
	for _, c := range changes {
		if !ValidEvent(c.Event) {
			return out, apperr.Invalid("invalid_event", "Unknown event "+clip(c.Event, 40))
		}
	}
	err := store.InTx(ctx, s.pool, func(q *dbgen.Queries, _ pgx.Tx) error {
		u, err := q.GetNotificationUser(ctx, a.UserID)
		if err != nil {
			return err
		}
		allowed, err := s.receivable(ctx, a.UserID, u.PlatformRole)
		if err != nil {
			return err
		}
		var bad []string
		for _, c := range changes {
			if !slices.Contains(allowed, c.Event) && !slices.Contains(bad, c.Event) {
				bad = append(bad, c.Event)
			}
		}
		if len(bad) > 0 {
			e := apperr.New(http.StatusUnprocessableEntity, "not_receivable", "You can't receive "+bad[0]+" notifications")
			e.Details = map[string]any{"events": bad}
			return e
		}
		for _, c := range changes {
			for ch, v := range map[string]*bool{ChannelInApp: c.InApp, ChannelEmail: c.Email} {
				switch {
				case v == nil:
				case *v: // the default: no row
					err = q.DeleteNotificationPreference(ctx, dbgen.DeleteNotificationPreferenceParams{UserID: a.UserID, Event: c.Event, Channel: ch})
				default:
					err = q.UpsertNotificationPreference(ctx, dbgen.UpsertNotificationPreferenceParams{UserID: a.UserID, Event: c.Event, Channel: ch, Enabled: false})
				}
				if err != nil {
					return err
				}
			}
		}
		out, err = s.preferences(ctx, q, a.UserID)
		return err
	})
	return out, err
}

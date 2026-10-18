package registry

import (
	"context"

	"github.com/google/uuid"

	"github.com/ncecere/open-mcp-portal/internal/authz"
	"github.com/ncecere/open-mcp-portal/internal/store/dbgen"
)

// Notifier is told about the registry's changes and checks that people may
// want to hear about (internal/notify implements it; docs/notifications.md).
// Calls are made with the queries of the change's transaction (or the pool)
// and never fail it: a notification can't stop what it reports on.
type Notifier interface {
	// ServerChecked: a health check of the server was stored.
	ServerChecked(ctx context.Context, q *dbgen.Queries, serverID uuid.UUID)
	// CapabilitiesRefreshed: a refresh stored the server's capabilities.
	CapabilitiesRefreshed(ctx context.Context, q *dbgen.Queries, serverID uuid.UUID)
	// PublishRequested: the server entered review (reason new, url_changed,
	// upstream_auth_changed or requirement_changed).
	PublishRequested(ctx context.Context, q *dbgen.Queries, serverID uuid.UUID, reason string, a authz.Actor)
	// PublishDecided: a platform admin approved or rejected the server.
	PublishDecided(ctx context.Context, q *dbgen.Queries, serverID uuid.UUID, approved bool, a authz.Actor)
}

type noNotifier struct{}

func (noNotifier) ServerChecked(context.Context, *dbgen.Queries, uuid.UUID)         {}
func (noNotifier) CapabilitiesRefreshed(context.Context, *dbgen.Queries, uuid.UUID) {}
func (noNotifier) PublishRequested(context.Context, *dbgen.Queries, uuid.UUID, string, authz.Actor) {
}
func (noNotifier) PublishDecided(context.Context, *dbgen.Queries, uuid.UUID, bool, authz.Actor) {}

// SetNotifier installs the notification hooks.
func (s *Service) SetNotifier(n Notifier) { s.notify = n }

func (s *Service) notifier() Notifier {
	if s.notify == nil {
		return noNotifier{}
	}
	return s.notify
}

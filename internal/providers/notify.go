package providers

import (
	"context"

	"github.com/google/uuid"

	"github.com/ncecere/open-mcp-portal/internal/store/dbgen"
)

// Notifier is told when a connection needs its person to sign in again
// (internal/notify implements it; docs/notifications.md). The call is made
// with the queries of the change's transaction (or the pool) and never
// fails it.
type Notifier interface {
	ConnectionReconsent(ctx context.Context, q *dbgen.Queries, connectionID uuid.UUID, reason string)
}

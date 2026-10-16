package registry

import (
	"context"

	"github.com/google/uuid"

	"github.com/ncecere/open-mcp-gateway/internal/authz"
	"github.com/ncecere/open-mcp-gateway/internal/store/dbgen"
)

// The company catalog: published, enabled servers, and only their listed
// capabilities whose current definition is approved. Any signed-in person
// (an active session) may browse it.

// ListCatalog lists published, enabled servers by (name, id). Their counts
// are of approved, listed capabilities (ServerView.Approved).
func (s *Service) ListCatalog(ctx context.Context, _ authz.Actor, p ListParams) ([]ServerView, error) {
	qp := p.query()
	qp.Catalog, qp.PublishState, qp.ChangesOnly = true, nil, false
	rows, err := s.q.ListMCPServers(ctx, qp)
	if err != nil {
		return nil, err
	}
	return views(ctx, s.q, rows)
}

// CatalogServer is a published server with its approved capabilities.
type CatalogServer struct {
	View         ServerView
	Capabilities []dbgen.McpCapability
}

// GetCatalog returns a published, enabled server; any other is not found.
func (s *Service) GetCatalog(ctx context.Context, _ authz.Actor, id uuid.UUID) (CatalogServer, error) {
	v, err := s.view(ctx, s.q, id)
	if err != nil {
		return CatalogServer{}, err
	}
	if v.Server.PublishState != StatePublished || !v.Server.Enabled {
		return CatalogServer{}, errNoServer
	}
	caps, err := s.q.ListApprovedMCPCapabilities(ctx, id)
	if err != nil {
		return CatalogServer{}, err
	}
	return CatalogServer{View: v, Capabilities: caps}, nil
}

package limits

import (
	"context"
	"time"

	"github.com/google/uuid"

	"github.com/ncecere/open-mcp-portal/internal/ratelimit"
	"github.com/ncecere/open-mcp-portal/internal/store/dbgen"
)

// Subject is who a request counts for.
type Subject struct {
	// UserID is the person (API keys and OAuth connections).
	UserID uuid.UUID
	// CredentialID is the personal API key or OAuth grant.
	CredentialID uuid.UUID
	// ServiceAccountID and ServiceAccountTeam: a service account's request.
	ServiceAccountID   uuid.UUID
	ServiceAccountTeam *dbgen.Team
	Gateway            dbgen.Gateway
	// GatewayTeam is a team gateway's team (nil for platform gateways).
	GatewayTeam *dbgen.Team
}

// Usage is the caller's own tightest limit (for RateLimit-* headers).
type Usage struct {
	Limit, Remaining int64
	Reset            time.Duration
}

// Decision is the outcome of a check.
type Decision struct {
	// Exceeded names the limit refused (the most specific one), "" when the
	// request may go ahead.
	Exceeded string
	// LimitPerMinute is the exceeded limit's value.
	LimitPerMinute int64
	// Reset is how long until the window ends.
	Reset time.Duration
	// Own is the caller's tightest own limit (person, credential or service
	// account), nil when none applies. Gateway-wide counts are never shown.
	Own *Usage
}

// precedence orders limits from the most specific (named when several are
// exceeded).
var precedence = []string{CredentialPerMinute, PersonPerMinute, ServiceAccountPerMinute, GatewayPerMinute}

type bucket struct {
	key, name string
	limit     int64
}

// Check counts one request in every bucket that applies (unlimited ones are
// not counted) and decides. Refused requests count too.
func (s *Service) Check(ctx context.Context, st ratelimit.Store, sub Subject) (Decision, error) {
	set, err := s.Current(ctx)
	if err != nil {
		return Decision{}, err
	}
	var bs []bucket
	add := func(key, name string, v *int64) {
		if v != nil {
			bs = append(bs, bucket{key: key, name: name, limit: *v})
		}
	}
	if sub.UserID != uuid.Nil {
		add(PersonPerMinute, "p:"+sub.UserID.String(), set.Default(PersonPerMinute))
	}
	if sub.CredentialID != uuid.Nil {
		add(CredentialPerMinute, "c:"+sub.CredentialID.String(), set.Default(CredentialPerMinute))
	}
	if sub.ServiceAccountID != uuid.Nil {
		var o Overrides
		if sub.ServiceAccountTeam != nil {
			o = TeamOverrides(*sub.ServiceAccountTeam)
		}
		add(ServiceAccountPerMinute, "s:"+sub.ServiceAccountID.String(), set.TeamEffective(o, ServiceAccountPerMinute))
	}
	var gt Overrides
	if sub.GatewayTeam != nil {
		gt = TeamOverrides(*sub.GatewayTeam)
	}
	add(GatewayPerMinute, "g:"+sub.Gateway.ID.String(), set.GatewayEffective(sub.Gateway.RateLimitPerMinute, gt))
	if len(bs) == 0 {
		return Decision{}, nil
	}
	names := make([]string, len(bs))
	for i, b := range bs {
		names[i] = b.name
	}
	counts, reset, err := st.Hit(ctx, names)
	if err != nil {
		return Decision{}, err
	}
	d := Decision{Reset: reset}
	exceeded := map[string]int64{}
	for i, b := range bs {
		if counts[i] > b.limit {
			exceeded[b.key] = b.limit
		}
		if b.key != GatewayPerMinute {
			rem := max(b.limit-counts[i], 0)
			if d.Own == nil || rem < d.Own.Remaining {
				d.Own = &Usage{Limit: b.limit, Remaining: rem, Reset: reset}
			}
		}
	}
	for _, k := range precedence {
		if v, ok := exceeded[k]; ok {
			d.Exceeded, d.LimitPerMinute = k, v
			break
		}
	}
	return d, nil
}

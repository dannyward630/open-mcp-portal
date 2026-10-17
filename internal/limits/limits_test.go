package limits

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/ncecere/open-mcp-portal/internal/ratelimit"
	"github.com/ncecere/open-mcp-portal/internal/store/dbgen"
)

func n(v int64) *int64 { return &v }

func eq(a, b *int64) bool { return (a == nil) == (b == nil) && (a == nil || *a == *b) }

func TestEffectiveValues(t *testing.T) {
	st := decodeSettings(dbgen.LimitSetting{Items: []byte(`{"gateway_per_minute":{"default":100,"ceiling":500},
		"service_account_per_minute":{"default":50,"ceiling":80},"person_per_minute":{"default":7,"ceiling":9}}`)})
	if st.Ceiling(PersonPerMinute) != nil || *st.Default(PersonPerMinute) != 7 || *st.Default(CredentialPerMinute) != 300 {
		t.Fatalf("non-overridable keys keep no ceiling; unset keys are built in: %+v", st.Items)
	}
	team := Overrides{GatewayPerMinute: 300, ServiceAccountPerMinute: 200}
	for name, tc := range map[string]struct{ got, want *int64 }{
		"gateway own value":             {st.GatewayEffective(n(40), team), n(40)},
		"gateway own value capped":      {st.GatewayEffective(n(900), team), n(500)},
		"team override":                 {st.GatewayEffective(nil, team), n(300)},
		"platform gateway: default":     {st.GatewayEffective(nil, nil), n(100)},
		"team without override":         {st.GatewayEffective(nil, Overrides{}), n(100)},
		"service account override cap":  {st.TeamEffective(team, ServiceAccountPerMinute), n(80)},
		"service account default":       {st.TeamEffective(Overrides{}, ServiceAccountPerMinute), n(50)},
		"person ignores team overrides": {st.TeamEffective(Overrides{PersonPerMinute: 1}, PersonPerMinute), n(7)},
	} {
		if !eq(tc.got, tc.want) {
			t.Errorf("%s = %v, want %v", name, tc.got, tc.want)
		}
	}
	if !eq(Cap(nil, n(5)), n(5)) || Cap(nil, nil) != nil || !eq(Cap(n(3), n(5)), n(3)) {
		t.Error("Cap")
	}
}

func TestCheckNamesTheMostSpecificLimit(t *testing.T) {
	s := New(nil, nil)
	s.set(decodeSettings(dbgen.LimitSetting{Items: []byte(`{"person_per_minute":{"default":2},"credential_per_minute":{"default":2},
		"gateway_per_minute":{"default":2,"ceiling":null}}`)}))
	at := time.Unix(1_800_000_000, 0).Truncate(time.Minute).Add(10 * time.Second)
	store := ratelimit.NewWithClock(func() time.Time { return at })
	sub := Subject{UserID: uuid.New(), CredentialID: uuid.New(), Gateway: dbgen.Gateway{ID: uuid.New()}}
	for i := 0; i < 2; i++ {
		d, err := s.Check(context.Background(), store, sub)
		if err != nil || d.Exceeded != "" {
			t.Fatalf("request %d: %+v %v", i+1, d, err)
		}
		if d.Own == nil || d.Own.Limit != 2 || d.Own.Remaining != int64(1-i) {
			t.Fatalf("own = %+v", d.Own)
		}
	}
	d, _ := s.Check(context.Background(), store, sub)
	if d.Exceeded != CredentialPerMinute || d.LimitPerMinute != 2 || d.Reset != 50*time.Second {
		t.Fatalf("third = %+v", d)
	}
	// Another credential of the same person: person and gateway exceeded,
	// person named.
	sub.CredentialID = uuid.New()
	if d, _ := s.Check(context.Background(), store, sub); d.Exceeded != PersonPerMinute {
		t.Fatalf("other credential = %+v", d)
	}
	// Another person on the same gateway: only the gateway.
	if d, _ := s.Check(context.Background(), store, Subject{UserID: uuid.New(), CredentialID: uuid.New(), Gateway: sub.Gateway}); d.Exceeded != GatewayPerMinute {
		t.Fatalf("other person = %+v", d)
	}
	// A service account (team override above the ceiling is capped).
	s.set(decodeSettings(dbgen.LimitSetting{Items: []byte(`{"service_account_per_minute":{"default":10,"ceiling":1}}`)}))
	team := dbgen.Team{LimitOverrides: []byte(`{"service_account_per_minute":5}`)}
	sa := Subject{ServiceAccountID: uuid.New(), ServiceAccountTeam: &team, Gateway: dbgen.Gateway{ID: uuid.New()}}
	if d, _ := s.Check(context.Background(), store, sa); d.Exceeded != "" || d.Own.Limit != 1 {
		t.Fatalf("service account first = %+v", d)
	}
	if d, _ := s.Check(context.Background(), store, sa); d.Exceeded != ServiceAccountPerMinute {
		t.Fatalf("service account second = %+v", d)
	}
}

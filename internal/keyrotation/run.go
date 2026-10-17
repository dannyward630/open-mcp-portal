package keyrotation

import (
	"context"
	"errors"
	"fmt"
	"io"
	"text/tabwriter"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ncecere/open-mcp-portal/internal/apikeys"
	"github.com/ncecere/open-mcp-portal/internal/audit"
	"github.com/ncecere/open-mcp-portal/internal/secrets"
	"github.com/ncecere/open-mcp-portal/internal/store/dbgen"
)

// DefaultBatchSize is how many values one transaction re-encrypts.
const DefaultBatchSize = 100

// Peppers are API_KEY_PEPPER and API_KEY_PEPPER_PREVIOUS (decoded; nil
// when unset).
type Peppers struct {
	Current, Previous []byte
}

// PepperState of a key's digest.
const (
	PepperPrevious = "previous" // re-digested on next use; stops working when the previous pepper is removed
	PepperRetired  = "retired"  // made with a pepper no longer configured: can't work, revoke it
)

// PepperKey is a usable key whose digest is not on the current pepper.
type PepperKey struct {
	Kind       string // api_key or service_account_key
	ID         uuid.UUID
	Name       string
	Owner      string // a personal key's owner (email)
	TeamSlug   string // a service account key's team
	LastUsedAt *time.Time
	State      string
}

// PepperReport lists usable personal and service account keys that are
// not on the current pepper, and counts unexpired OAuth tokens not on it.
func PepperReport(ctx context.Context, q *dbgen.Queries, p Peppers) ([]PepperKey, int64, error) {
	cur := apikeys.PepperID(p.Current)
	rows, err := q.KeysNotOnPepper(ctx, cur)
	if err != nil {
		return nil, 0, err
	}
	var prev []byte
	if p.Previous != nil {
		prev = apikeys.PepperID(p.Previous)
	}
	out := []PepperKey{}
	for _, r := range rows {
		st := PepperRetired
		if prev != nil && (r.PepperID == nil || string(r.PepperID) == string(prev)) {
			st = PepperPrevious
		}
		out = append(out, PepperKey{Kind: r.Kind, ID: r.ID, Name: r.Name, Owner: r.Owner, TeamSlug: r.TeamSlug, LastUsedAt: r.LastUsedAt, State: st})
	}
	tokens, err := q.CountOAuthTokensNotOnPepper(ctx, cur)
	return out, tokens, err
}

// StampPeppers records which pepper made digests from before pepper IDs:
// the current one when no previous pepper is set, else the previous one.
func StampPeppers(ctx context.Context, pool *pgxpool.Pool, p Peppers) (int64, error) {
	id := apikeys.PepperID(p.Current)
	if p.Previous != nil {
		id = apikeys.PepperID(p.Previous)
	}
	var total int64
	err := pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
		q := dbgen.New(tx)
		for _, f := range []func(context.Context, []byte) (int64, error){q.StampAPIKeyPeppers, q.StampServiceAccountKeyPeppers, q.StampOAuthTokenPeppers} {
			n, err := f(ctx, id)
			if err != nil {
				return err
			}
			total += n
		}
		return nil
	})
	return total, err
}

// ErrNoPreviousKey: re-encryption needs ENCRYPTION_KEY_PREVIOUS.
var ErrNoPreviousKey = errors.New("ENCRYPTION_KEY_PREVIOUS is not set (or equals ENCRYPTION_KEY): nothing to re-encrypt from")

// Options control a run.
type Options struct {
	// DryRun checks and counts; nothing is written, not even the audit log.
	DryRun    bool
	BatchSize int
	Out       io.Writer
}

// Summary is what a run found and did.
type Summary struct {
	Columns     []ColumnStats
	PepperKeys  []PepperKey
	OAuthTokens int64
}

// Reencrypted is the number of values moved to the current key.
func (s Summary) Reencrypted() int {
	n := 0
	for _, c := range s.Columns {
		n += c.Reencrypted
	}
	return n
}

// Run checks every sealed value, re-seals those on the previous key
// (unless DryRun) and reports keys still on another pepper. It refuses to
// write anything if a value opens with neither key. Without a previous
// key it only checks and reports.
func Run(ctx context.Context, pool *pgxpool.Pool, box *secrets.Box, p Peppers, o Options) (Summary, error) {
	if o.BatchSize <= 0 {
		o.BatchSize = DefaultBatchSize
	}
	if o.Out == nil {
		o.Out = io.Discard
	}
	var sum Summary
	fmt.Fprintf(o.Out, "Encryption key: current key id %x. Checking %d sealed column(s)...\n", box.CurrentKeyID()[:4], len(Columns))
	var bad []BadRow
	for _, c := range Columns {
		st, err := Scan(ctx, pool, box, c, o.BatchSize)
		if err != nil {
			return sum, err
		}
		sum.Columns = append(sum.Columns, st)
		bad = append(bad, st.Undecryptable...)
		fmt.Fprintf(o.Out, "  %-45s %6d stored, %6d current key, %6d previous key, %d undecryptable\n",
			st.Column, st.Total, st.Current, st.Previous, len(st.Undecryptable))
	}
	if len(bad) > 0 {
		return sum, &UndecryptableError{Rows: bad}
	}
	previous := 0
	for _, st := range sum.Columns {
		previous += st.Previous
	}
	switch {
	case o.DryRun:
		fmt.Fprintf(o.Out, "Dry run: %d value(s) would be re-encrypted.\n", previous)
	case previous > 0 && !box.HasPrevious():
		return sum, ErrNoPreviousKey
	case previous > 0:
		fmt.Fprintln(o.Out, "Re-encrypting with the current key...")
		for i, c := range Columns {
			todo := sum.Columns[i].Previous
			if todo == 0 {
				continue
			}
			n, err := reencrypt(ctx, pool, box, c, o.BatchSize, func(done int) {
				fmt.Fprintf(o.Out, "  %s: %d of %d\n", c.Name(), done, todo)
			})
			sum.Columns[i].Reencrypted = n
			if err != nil {
				return sum, err
			}
		}
		if err := verify(ctx, pool, box, o); err != nil {
			return sum, err
		}
	default:
		fmt.Fprintln(o.Out, "Every sealed value is on the current key.")
	}
	var err error
	if sum.PepperKeys, sum.OAuthTokens, err = PepperReport(ctx, dbgen.New(pool), p); err != nil {
		return sum, fmt.Errorf("pepper report: %w (has `open-mcp-portal migrate` run?)", err)
	}
	printPepper(o.Out, p, sum)
	if o.DryRun {
		fmt.Fprintln(o.Out, "\nDry run: nothing was written.")
		return sum, nil
	}
	return sum, record(ctx, pool, p, sum)
}

func verify(ctx context.Context, pool *pgxpool.Pool, box *secrets.Box, o Options) error {
	left := 0
	for _, c := range Columns {
		st, err := Scan(ctx, pool, box, c, o.BatchSize)
		if err != nil {
			return err
		}
		left += st.Previous + len(st.Undecryptable)
	}
	if left > 0 {
		return fmt.Errorf("%d value(s) were written with another key during the run: make sure every pod runs with the new ENCRYPTION_KEY, then run again", left)
	}
	fmt.Fprintln(o.Out, "Every sealed value is on the current key. ENCRYPTION_KEY_PREVIOUS can be removed.")
	return nil
}

func record(ctx context.Context, pool *pgxpool.Pool, p Peppers, sum Summary) error {
	cols := map[string]any{}
	for _, c := range sum.Columns {
		cols[c.Column] = map[string]int{"reencrypted": c.Reencrypted, "alreadyCurrent": c.Current}
	}
	counts := map[string]map[string]int{PepperPrevious: {}, PepperRetired: {}}
	for _, k := range sum.PepperKeys {
		counts[k.State][k.Kind]++
	}
	meta := map[string]any{"columns": cols, "pepper": map[string]any{"previousConfigured": p.Previous != nil,
		"onPreviousPepper": counts[PepperPrevious], "onRetiredPepper": counts[PepperRetired], "oauthTokens": sum.OAuthTokens}}
	return pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
		return audit.Record(ctx, dbgen.New(tx), audit.Entry{
			ActorKind: audit.ActorSystem, Action: "platform.key_rotation", TargetType: "platform_settings", TargetID: "keys", Metadata: meta,
		})
	})
}

// printPepper writes the pepper part of a run: kinds, IDs, names, owners
// and last use, never digests or keys.
func printPepper(w io.Writer, p Peppers, sum Summary) {
	fmt.Fprintln(w)
	if p.Previous != nil {
		fmt.Fprintln(w, "API key pepper: API_KEY_PEPPER_PREVIOUS is set; keys on it are re-digested with API_KEY_PEPPER when next used.")
	} else {
		fmt.Fprintln(w, "API key pepper: API_KEY_PEPPER_PREVIOUS is not set.")
	}
	if sum.OAuthTokens > 0 {
		fmt.Fprintf(w, "  %d unexpired connected-app token(s) are on another pepper (access tokens expire within an hour; refresh tokens are replaced when used).\n", sum.OAuthTokens)
	}
	if len(sum.PepperKeys) == 0 {
		fmt.Fprintln(w, "  Every usable API key and service account key is on the current pepper.")
		return
	}
	fmt.Fprintf(w, "  %d usable key(s) are not on the current pepper:\n", len(sum.PepperKeys))
	tw := tabwriter.NewWriter(w, 2, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "  STATE\tKIND\tID\tNAME\tOWNER/TEAM\tLAST USED")
	for _, k := range sum.PepperKeys {
		last := "never"
		if k.LastUsedAt != nil {
			last = k.LastUsedAt.UTC().Format("2006-01-02 15:04Z")
		}
		who := k.Owner
		if who == "" {
			who = k.TeamSlug
		}
		fmt.Fprintf(tw, "  %s\t%s\t%s\t%s\t%s\t%s\n", k.State, k.Kind, k.ID, k.Name, who, last)
	}
	_ = tw.Flush()
}

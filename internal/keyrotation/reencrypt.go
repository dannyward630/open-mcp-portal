package keyrotation

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ncecere/open-mcp-gateway/internal/audit"
	"github.com/ncecere/open-mcp-gateway/internal/secrets"
	"github.com/ncecere/open-mcp-gateway/internal/store/dbgen"
)

// ColumnStats counts one column's stored values.
type ColumnStats struct {
	Column   string // table.column
	Total    int    // non-NULL values
	Current  int    // on ENCRYPTION_KEY (before this run)
	Previous int    // on ENCRYPTION_KEY_PREVIOUS (before this run)
	// Reencrypted is how many values this run moved to the current key.
	Reencrypted int
	// Undecryptable rows open with neither key; nothing is written while
	// any exist.
	Undecryptable []BadRow
}

// BadRow names a value that cannot be decrypted (never its contents).
type BadRow struct {
	Column string
	Key    string
	Reason string // "unknown key", "malformed" or "authentication failed"
}

// UndecryptableError lists every value that opens with neither key.
type UndecryptableError struct{ Rows []BadRow }

func (e *UndecryptableError) Error() string {
	var b strings.Builder
	fmt.Fprintf(&b, "%d stored value(s) cannot be decrypted with ENCRYPTION_KEY or ENCRYPTION_KEY_PREVIOUS; nothing was re-encrypted:", len(e.Rows))
	for _, r := range e.Rows {
		fmt.Fprintf(&b, "\n  %s %s (%s)", r.Column, r.Key, r.Reason)
	}
	b.WriteString("\nSet the key these were written with as ENCRYPTION_KEY_PREVIOUS, or re-enter or delete them, then run again.")
	return b.String()
}

type row struct {
	key string
	ct  []byte
}

func collect(rows pgx.Rows) ([]row, error) {
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (row, error) {
		var x row
		err := r.Scan(&x.key, &x.ct)
		return x, err
	})
}

func reason(box *secrets.Box, ct []byte) string {
	switch box.Which(ct) {
	case secrets.SlotMalformed:
		return "malformed"
	case secrets.SlotUnknown:
		return "unknown key"
	}
	return "authentication failed"
}

// Scan reads every value of a column (without locks) and checks that each
// one decrypts, counting which key it is on.
func Scan(ctx context.Context, pool *pgxpool.Pool, box *secrets.Box, c Column, batch int) (ColumnStats, error) {
	st := ColumnStats{Column: c.Name()}
	q := fmt.Sprintf("SELECT %[3]s::text, %[2]s FROM %[1]s WHERE %[2]s IS NOT NULL AND %[3]s > $1::%[4]s ORDER BY %[3]s LIMIT $2",
		c.table(), c.col(), c.key(), c.KeyType)
	for after := c.start(); ; {
		rows, err := pool.Query(ctx, q, after, batch)
		if err != nil {
			return st, fmt.Errorf("%s: %w", c.Name(), err)
		}
		items, err := collect(rows)
		if err != nil {
			return st, fmt.Errorf("%s: %w", c.Name(), err)
		}
		for _, it := range items {
			st.Total++
			plain, err := box.Open(it.ct, c.AAD(it.key))
			clear(plain)
			switch {
			case err != nil:
				st.Undecryptable = append(st.Undecryptable, BadRow{Column: c.Name(), Key: it.key, Reason: reason(box, it.ct)})
			case box.Which(it.ct) == secrets.SlotCurrent:
				st.Current++
			default:
				st.Previous++
			}
			after = it.key
		}
		if len(items) < batch {
			return st, nil
		}
	}
}

// reencryptBatch moves up to batch values of a column that are not on the
// current key (and sort after `after`) to the current key, in one
// transaction with row locks, audited with the count. It returns how many
// it moved and the last key seen.
func reencryptBatch(ctx context.Context, pool *pgxpool.Pool, box *secrets.Box, c Column, after string, batch int) (int, string, error) {
	sel := fmt.Sprintf("SELECT %[3]s::text, %[2]s FROM %[1]s WHERE %[2]s IS NOT NULL AND substring(%[2]s FROM %[5]d FOR %[6]d) IS DISTINCT FROM $1 "+
		"AND %[3]s > $2::%[4]s ORDER BY %[3]s LIMIT $3 FOR UPDATE", c.table(), c.col(), c.key(), c.KeyType, secrets.IDOffset+1, secrets.IDLen)
	upd := fmt.Sprintf("UPDATE %s SET %s = $1 WHERE %s = $2::%s", c.table(), c.col(), c.key(), c.KeyType)
	n, last := 0, after
	err := pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, sel, box.CurrentKeyID(), after, batch)
		if err != nil {
			return err
		}
		items, err := collect(rows)
		if err != nil || len(items) == 0 {
			return err
		}
		for _, it := range items {
			plain, err := box.Open(it.ct, c.AAD(it.key))
			if err != nil {
				return &UndecryptableError{Rows: []BadRow{{Column: c.Name(), Key: it.key, Reason: reason(box, it.ct)}}}
			}
			sealed, err := box.Seal(plain, c.AAD(it.key))
			clear(plain)
			if err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, upd, sealed, it.key); err != nil {
				return err
			}
			n, last = n+1, it.key
		}
		return audit.Record(ctx, dbgen.New(tx), audit.Entry{
			ActorKind: audit.ActorSystem, Action: "platform.secrets_reencrypt", TargetType: "platform_settings", TargetID: "keys",
			Metadata: map[string]any{"column": c.Name(), "rows": n},
		})
	})
	var bad *UndecryptableError
	if err != nil && !errors.As(err, &bad) {
		err = fmt.Errorf("%s: %w", c.Name(), err)
	}
	return n, last, err
}

// reencrypt moves every value of a column to the current key, batch by
// batch, calling progress after each batch.
func reencrypt(ctx context.Context, pool *pgxpool.Pool, box *secrets.Box, c Column, batch int, progress func(done int)) (int, error) {
	done := 0
	for after := c.start(); ; {
		n, last, err := reencryptBatch(ctx, pool, box, c, after, batch)
		done += n
		if err != nil || n == 0 {
			return done, err
		}
		progress(done)
		after = last
		if n < batch {
			return done, nil
		}
	}
}

// KeyCounts counts a column's values by the key id they carry (no
// decryption: doctor's quick check).
type KeyCounts struct {
	Column                           string
	Total, Current, Previous, Others int64
}

// Count counts every column's values by key id. Others carry neither key
// (or are malformed): they can't be decrypted.
func Count(ctx context.Context, pool *pgxpool.Pool, current, previous []byte) ([]KeyCounts, error) {
	out := make([]KeyCounts, 0, len(Columns))
	for _, c := range Columns {
		id := fmt.Sprintf("substring(%s FROM %d FOR %d)", c.col(), secrets.IDOffset+1, secrets.IDLen)
		q := fmt.Sprintf(`SELECT count(*), count(*) FILTER (WHERE %[2]s = $1), count(*) FILTER (WHERE $2::bytea IS NOT NULL AND %[2]s = $2)
			FROM %[1]s WHERE %[3]s IS NOT NULL`, c.table(), id, c.col())
		k := KeyCounts{Column: c.Name()}
		if err := pool.QueryRow(ctx, q, current, previous).Scan(&k.Total, &k.Current, &k.Previous); err != nil {
			return nil, fmt.Errorf("%s: %w", c.Name(), err)
		}
		k.Others = k.Total - k.Current - k.Previous
		out = append(out, k)
	}
	return out, nil
}

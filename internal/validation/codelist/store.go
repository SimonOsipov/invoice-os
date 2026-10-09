package codelist

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// apply replaces one list with the published state in a single transaction and
// records the diff. The DELETE runs before the upsert so a failed upsert proves
// the rollback (TestSync_DBErrorRollsBackTheList).
func apply(ctx context.Context, pool *pgxpool.Pool, l List, grouped map[string][]json.RawMessage, entries int) (Change, error) {
	codes := make([]string, 0, len(grouped))
	texts := make([]string, 0, len(grouped))
	for c, es := range grouped {
		b, err := json.Marshal(es)
		if err != nil {
			return Change{}, fmt.Errorf("codelist: apply %s: %w", l.Name, err)
		}
		codes = append(codes, c)
		texts = append(texts, string(b))
	}

	tx, err := pool.Begin(ctx)
	if err != nil {
		return Change{}, fmt.Errorf("codelist: apply %s: begin: %w", l.Name, err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// Serialises concurrent syncs of one list.
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext('nrs_codes:' || $1))`, l.Name); err != nil {
		return Change{}, fmt.Errorf("codelist: apply %s: lock: %w", l.Name, err)
	}
	existing, err := queryCodes(ctx, tx, `SELECT code FROM nrs_codes WHERE list = $1`, l.Name)
	if err != nil {
		return Change{}, fmt.Errorf("codelist: apply %s: read existing: %w", l.Name, err)
	}
	removed, err := queryCodes(ctx, tx,
		`DELETE FROM nrs_codes WHERE list = $1 AND NOT (code = ANY($2::text[])) RETURNING code`, l.Name, codes)
	if err != nil {
		return Change{}, fmt.Errorf("codelist: apply %s: delete removed: %w", l.Name, err)
	}
	// jsonb comparison: key order and whitespace are not a change.
	upserted, err := queryCodes(ctx, tx, `
INSERT INTO nrs_codes (list, code, entries)
SELECT $1, c, e::jsonb FROM unnest($2::text[], $3::text[]) AS u(c, e)
ON CONFLICT (list, code) DO UPDATE SET entries = EXCLUDED.entries
WHERE nrs_codes.entries IS DISTINCT FROM EXCLUDED.entries
RETURNING code`, l.Name, codes, texts)
	if err != nil {
		return Change{}, fmt.Errorf("codelist: apply %s: upsert: %w", l.Name, err)
	}

	had := make(map[string]bool, len(existing))
	for _, c := range existing {
		had[c] = true
	}
	ch := Change{Added: []string{}, Removed: removed, Changed: []string{}, Entries: entries}
	for _, c := range upserted {
		if had[c] {
			ch.Changed = append(ch.Changed, c)
		} else {
			ch.Added = append(ch.Added, c)
		}
	}
	slices.Sort(ch.Added)
	slices.Sort(ch.Removed)
	slices.Sort(ch.Changed)

	if _, err := tx.Exec(ctx,
		`INSERT INTO nrs_code_list_syncs (list, entry_count, added, removed, changed) VALUES ($1, $2, $3, $4, $5)`,
		l.Name, ch.Entries, ch.Added, ch.Removed, ch.Changed); err != nil {
		return Change{}, fmt.Errorf("codelist: apply %s: record sync: %w", l.Name, err)
	}
	if err := tx.Commit(ctx); err != nil {
		return Change{}, fmt.Errorf("codelist: apply %s: commit: %w", l.Name, err)
	}
	return ch, nil
}

// queryCodes returns the single text column of every row, never nil.
func queryCodes(ctx context.Context, tx pgx.Tx, sql string, args ...any) ([]string, error) {
	rows, err := tx.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	out, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if out == nil {
		out = []string{}
	}
	return out, err
}

// LoadTx reads the named lists over the caller's tx in one query. A list with
// no rows is absent from the result.
func LoadTx(ctx context.Context, tx pgx.Tx, names []string) (map[string]map[string]struct{}, error) {
	rows, err := tx.Query(ctx, `SELECT list, code FROM nrs_codes WHERE list = ANY($1::text[])`, names)
	if err != nil {
		return nil, fmt.Errorf("codelist: load: %w", err)
	}
	defer rows.Close()
	out := map[string]map[string]struct{}{}
	for rows.Next() {
		var list, code string
		if err := rows.Scan(&list, &code); err != nil {
			return nil, fmt.Errorf("codelist: load: %w", err)
		}
		if out[list] == nil {
			out[list] = map[string]struct{}{}
		}
		out[list][code] = struct{}{}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("codelist: load: %w", err)
	}
	return out, nil
}

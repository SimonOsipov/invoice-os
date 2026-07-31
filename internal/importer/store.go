package importer

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
	"github.com/SimonOsipov/invoice-os/internal/platform/db"
)

// Sentinels for the importer error model — mirrors internal/invoice's naming
// (ErrValidation/ErrNotFound).
var (
	// ErrValidation is returned when an INSERT's foreign key (e.g. a bogus
	// entity_id) or a malformed input is rejected by Postgres, mirroring
	// internal/invoice.Store.Create's 23503/22P02 mapping.
	ErrValidation = errors.New("importer: validation")

	// ErrNotFound is returned when a lookup resolves to zero rows under the
	// caller's tenant (RLS-scoped) — mirrors internal/invoice's sentinel.
	ErrNotFound = errors.New("importer: not found")
)

// RowError is one entry in an import_batches.errors jsonb array — either a
// single-row problem (Row set) or one shared across several rows (Rows set),
// e.g. rows that disagree with each other on a value. Exactly one of
// Row/Rows is set per entry; row numbers are 1-based spreadsheet rows.
type RowError struct {
	Row      int    `json:"row,omitempty"`
	Rows     []int  `json:"rows,omitempty"`
	Field    string `json:"field,omitempty"`
	RuleKey  string `json:"rule_key,omitempty"`
	Severity string `json:"severity,omitempty"`
	Message  string `json:"message"`
}

// Store persists import_batches rows as the invoice_app role, mirroring
// internal/invoice.Store's shape: it holds the app-role pool and every
// method wraps db.WithinRequestTenantTx so RLS enforces tenant isolation.
type Store struct {
	pool *pgxpool.Pool
}

// NewStore wraps the app-role connection pool. The caller owns the pool's
// lifecycle.
func NewStore(pool *pgxpool.Pool) *Store {
	return &Store{pool: pool}
}

// pgCode extracts the SQLSTATE from err, or "" if err does not wrap a
// *pgconn.PgError. Copied verbatim from internal/invoice's own copy (itself
// copied from internal/portfolio) — codebase convention is a per-package
// copy, not a cross-package import.
func pgCode(err error) string {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.Code
	}
	return ""
}

// CreateBatch inserts one import_batches row (tenant_id from the caller's
// identity, status 'processing', counts zeroed, errors '[]') and returns its
// generated id. A foreign_key_violation (23503, a non-existent entity_id) or
// an invalid_text_representation (22P02, a malformed entity_id uuid) maps to
// ErrValidation, mirroring internal/invoice.Store.Create's entity_id
// handling — a bogus entity_id must never 500.
//
// filename is wrapped in nullif($3, '') so an unusable/empty name (already
// coerced to "" by sanitizeFilename before it ever reaches here) persists as
// SQL NULL, never the empty string (AC #4) -- '' would make an unrecorded
// source indistinguishable from a file genuinely named nothing.
func (s *Store) CreateBatch(ctx context.Context, entityID, filename string) (string, error) {
	var id string
	err := db.WithinRequestTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		identity, _ := auth.IdentityFromContext(ctx)

		if err := tx.QueryRow(ctx,
			`INSERT INTO import_batches
			   (tenant_id, entity_id, filename, status, rows_total, rows_valid, rows_invalid, errors)
			 VALUES ($1, $2, nullif($3, ''), 'processing', 0, 0, 0, '[]'::jsonb)
			 RETURNING id`,
			identity.TenantID, entityID, filename,
		).Scan(&id); err != nil {
			switch pgCode(err) {
			case "23503", "22P02":
				return ErrValidation
			}
			return err
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	return id, nil
}

// Finalize updates one import_batches row's terminal counts/status/errors. A
// nil errs marshals to the jsonb empty array `[]`, never `null` (init to
// []RowError{} first). RLS scopes the UPDATE's WHERE to the caller's tenant
// automatically — no manual tenant filter. A missing id (or one RLS makes
// invisible to the caller's tenant) updates zero rows; Postgres does not
// error on that by itself, so the command tag's RowsAffected is checked
// explicitly and mapped to ErrNotFound — otherwise a caller would be told a
// batch was finalized when nothing was actually written.
func (s *Store) Finalize(ctx context.Context, id string, rowsTotal, rowsValid, rowsInvalid int, errs []RowError, status string) error {
	if errs == nil {
		errs = []RowError{}
	}
	payload, err := json.Marshal(errs)
	if err != nil {
		return err
	}

	return db.WithinRequestTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx,
			`UPDATE import_batches
			 SET status = $1, rows_total = $2, rows_valid = $3, rows_invalid = $4, errors = $5
			 WHERE id = $6`,
			status, rowsTotal, rowsValid, rowsInvalid, payload, id,
		)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return ErrNotFound
		}
		return nil
	})
}

// ExistingNumbers returns the subset of numbers already stored as
// invoice_number on invoices for entityID — one query, not N. RLS scopes the
// SELECT to the caller's tenant automatically, so a same-numbered invoice
// under another tenant's entity never registers as "already used"
// ([dedup-boundary]). An empty numbers slice short-circuits to an empty map
// with no query.
func (s *Store) ExistingNumbers(ctx context.Context, entityID string, numbers []string) (map[string]bool, error) {
	found := map[string]bool{}
	if len(numbers) == 0 {
		return found, nil
	}

	err := db.WithinRequestTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx,
			`SELECT invoice_number FROM invoices WHERE entity_id = $1 AND invoice_number = ANY($2)`,
			entityID, numbers,
		)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var number string
			if err := rows.Scan(&number); err != nil {
				return err
			}
			found[number] = true
		}
		return rows.Err()
	})
	if err != nil {
		return nil, err
	}
	return found, nil
}

// EntitySupplier returns the (name, tin) of the business_entities row
// identified by entityID, for use as an import batch's supplier defaults
// ([supplier-from-entity]). tin is nullable and scans into a nil *string
// when unset. Zero rows under the caller's tenant (RLS-scoped, same as a
// genuinely nonexistent id) maps to ErrNotFound.
func (s *Store) EntitySupplier(ctx context.Context, entityID string) (name string, tin *string, err error) {
	txErr := db.WithinRequestTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx,
			`SELECT name, tin FROM business_entities WHERE id = $1`, entityID,
		).Scan(&name, &tin)
	})
	if txErr != nil {
		if errors.Is(txErr, pgx.ErrNoRows) {
			return "", nil, ErrNotFound
		}
		return "", nil, txErr
	}
	return name, tin, nil
}

// Batch is one import_batches row plus its DERIVED rule_set_version
// (task-283). Every field except RuleSetVersion is FROZEN at Finalize --
// the live ledger counters (ready_invoices/invoices_clean/
// invoices_with_violations) are deliberately absent (plan R2; they are
// re-derived, live, from GET /v1/invoices?import_batch_id=... instead).
// RuleSetVersion is *int, nil when nothing under this batch was ever
// validated -- never a false 0 ([Stage-1 F2]).
type Batch struct {
	ID          string
	EntityID    string
	Filename    *string // NEW (BULK-01-01). nil = not recorded.
	Status      string
	RowsTotal   int
	RowsValid   int
	RowsInvalid int
	Errors      []RowError

	RuleSetVersion *int
	CreatedAt      time.Time
}

// GetBatch returns one import_batches row plus its DERIVED rule_set_version:
// min(rule_set_versions.version) over the invoices linked to the batch by
// import_batch_id.
//
// min(), NOT LIMIT 1. "One version per batch" holds at import time only:
// POST /v1/invoices/{id}/validate re-stamps a single invoice with the
// then-active version, so a batch imported under v2 with one re-validated
// row holds two versions, and a LIMIT 1 without ORDER BY would return an
// arbitrary one that can change between calls (task-283 R4). min() is
// stable, and is also the semantically right answer for the review screen's
// RULE SET sub-line -- the version the batch was IMPORTED under, since
// re-validate only ever moves forward. A batch with no invoices yields SQL
// NULL, which scans to a nil *int -- never a false 0 ([Stage-1 F2]).
//
// rule_set_versions is a GLOBAL reference table (no tenant_id, no RLS,
// GRANT SELECT to invoice_app), so the subselect is scoped purely by the
// RLS-visible invoices it joins through.
//
// Error mapping is EntitySupplier's lookup shape (WithinRequestTenantTx +
// ErrNoRows->ErrNotFound, so a cross-tenant id is indistinguishable from a
// nonexistent one -- no existence oracle, task-283 R5) PLUS CreateBatch's
// 22P02->ErrValidation mapping: EntitySupplier's shape ALONE would 500 on a
// malformed uuid. That second mapping is defence in depth -- GetHandler
// rejects a malformed id with its own uuid.Parse guard before ever calling
// here -- but a non-HTTP caller must not get a 500 either.
func (s *Store) GetBatch(ctx context.Context, id string) (Batch, error) {
	var b Batch
	var rawErrors []byte

	txErr := db.WithinRequestTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx,
			`SELECT b.id, b.entity_id, b.filename, b.status,
			        b.rows_total, b.rows_valid, b.rows_invalid,
			        b.errors, b.created_at,
			        (SELECT min(rsv.version)
			           FROM rule_set_versions rsv
			           JOIN invoices i ON i.rule_set_version_id = rsv.id
			          WHERE i.import_batch_id = b.id)
			   FROM import_batches b
			  WHERE b.id = $1`, id,
		).Scan(
			&b.ID, &b.EntityID, &b.Filename, &b.Status,
			&b.RowsTotal, &b.RowsValid, &b.RowsInvalid,
			&rawErrors, &b.CreatedAt, &b.RuleSetVersion,
		)
	})
	if txErr != nil {
		if errors.Is(txErr, pgx.ErrNoRows) {
			return Batch{}, ErrNotFound
		}
		if pgCode(txErr) == "22P02" {
			return Batch{}, ErrValidation
		}
		return Batch{}, txErr
	}

	// Errors is never nil on the way out: the column is NOT NULL DEFAULT
	// '[]'::jsonb and Finalize writes an empty array rather than null, but a
	// jsonb null literal is still representable, and json.Unmarshal of one
	// would leave the slice nil -- which marshals back out as JSON null, not
	// [] (task-283 trap 4 / spec 4).
	b.Errors = []RowError{}
	if len(rawErrors) > 0 && string(rawErrors) != "null" {
		if err := json.Unmarshal(rawErrors, &b.Errors); err != nil {
			return Batch{}, err
		}
		if b.Errors == nil {
			b.Errors = []RowError{}
		}
	}
	return b, nil
}

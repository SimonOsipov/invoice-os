package document

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/SimonOsipov/invoice-os/internal/audit"
	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
	"github.com/SimonOsipov/invoice-os/internal/platform/db"
)

// ErrValidation is returned when a malformed id is rejected by Postgres (22P02),
// mirroring internal/importer's mapping — a bad id must never 500. ErrNotFound
// (s3.go) covers the row-not-found case too.
var ErrValidation = errors.New("document: validation")

// Document is one documents row. Filename and DeclaredContentType are nil when
// the column is NULL, i.e. nothing usable was recorded.
type Document struct {
	ID                  string
	StorageKey          string
	ContentHash         string
	SizeBytes           int64
	Filename            *string
	DeclaredContentType *string
	CreatedAt           time.Time
}

const documentColumns = `id, storage_key, content_hash, size_bytes, filename, declared_content_type, created_at`

func scanDocument(row pgx.Row, d *Document) error {
	return row.Scan(&d.ID, &d.StorageKey, &d.ContentHash, &d.SizeBytes, &d.Filename, &d.DeclaredContentType, &d.CreatedAt)
}

// Store persists documents rows as the invoice_app role: it holds the app-role
// pool and every method wraps db.WithinRequestTenantTx, so RLS enforces tenant
// isolation. There is deliberately no update or delete method — the table's
// grants are SELECT/INSERT only.
type Store struct {
	pool *pgxpool.Pool
}

// NewStore wraps the app-role connection pool. The caller owns the pool's
// lifecycle.
func NewStore(pool *pgxpool.Pool) *Store {
	return &Store{pool: pool}
}

// pgCode extracts the SQLSTATE from err, or "" if err does not wrap a
// *pgconn.PgError. Per-package copy, per codebase convention.
func pgCode(err error) string {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.Code
	}
	return ""
}

// Upsert inserts one documents row, or resolves the existing one when this
// tenant already holds the same content hash, and writes document.created or
// document.reused in the SAME transaction. tenant_id comes from the caller's
// identity, so no caller can supply one.
//
// filename and declared_content_type go through nullif so an unrecorded value
// persists as SQL NULL rather than the empty string, which would be
// indistinguishable from a file genuinely named nothing.
func (s *Store) Upsert(ctx context.Context, d Document) (Document, bool, error) {
	var out Document
	var created bool

	err := db.WithinRequestTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		id, _ := auth.IdentityFromContext(ctx)

		err := scanDocument(tx.QueryRow(ctx,
			`INSERT INTO documents
			   (tenant_id, storage_key, content_hash, size_bytes, filename, declared_content_type)
			 VALUES ($1, $2, $3, $4, nullif($5, ''), nullif($6, ''))
			 ON CONFLICT (tenant_id, content_hash) DO NOTHING
			 RETURNING `+documentColumns,
			id.TenantID, d.StorageKey, d.ContentHash, d.SizeBytes, d.Filename, d.DeclaredContentType,
		), &out)
		switch {
		case err == nil:
			created = true
		case errors.Is(err, pgx.ErrNoRows):
			// DO NOTHING RETURNING yields zero rows on conflict, not the existing
			// row, so the row has to be read back here. RLS scopes it, and the
			// unique index is (tenant_id, content_hash), so this can only ever
			// resolve the caller's own row.
			if err := scanDocument(tx.QueryRow(ctx,
				`SELECT `+documentColumns+` FROM documents WHERE content_hash = $1`, d.ContentHash,
			), &out); err != nil {
				return err
			}
		default:
			return err
		}

		event := "document.reused"
		if created {
			event = "document.created"
		}
		return audit.Record(ctx, tx, id.Subject, event, map[string]any{"id": out.ID})
	})
	if err != nil {
		return Document{}, false, err
	}
	return out, created, nil
}

// Get returns one RLS-visible documents row and writes document.read in the same
// transaction. A cross-tenant id and a genuinely absent one both 0-row, so both
// yield ErrNotFound and neither leaves an audit row — a refused read must not
// leak that the id exists into the trail. A malformed uuid raises 22P02, mapped
// to ErrValidation.
func (s *Store) Get(ctx context.Context, id string) (Document, error) {
	var out Document

	err := db.WithinRequestTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		caller, _ := auth.IdentityFromContext(ctx)

		if err := scanDocument(tx.QueryRow(ctx,
			`SELECT `+documentColumns+` FROM documents WHERE id = $1`, id,
		), &out); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrNotFound
			}
			if pgCode(err) == "22P02" {
				return ErrValidation
			}
			return err
		}

		return audit.Record(ctx, tx, caller.Subject, "document.read", map[string]any{"id": out.ID})
	})
	if err != nil {
		return Document{}, err
	}
	return out, nil
}

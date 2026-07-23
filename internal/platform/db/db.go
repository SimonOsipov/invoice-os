// Package db is the tenant-aware data-access layer every ASComply service
// shares. Its core is WithinTenantTx: it runs a function inside a transaction with
// the app.current_tenant GUC set for the life of that transaction, which is what
// makes Postgres Row-Level Security enforce tenant isolation (docs/migrations.md §4).
//
// The tenant is passed EXPLICITLY, not read from a request context, so the exact
// same helper serves both the HTTP path (tenant from the verified JWT) and the M5
// worker (tenant from the job payload). The thin request-scoped convenience wrapper
// that bridges the HTTP path lives in tenant.go.
package db

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ErrNoTenant is returned by WithinTenantTx when the tenant id is empty or not a
// valid UUID. It is deliberately fail-closed: the helper runs no statement at all
// rather than risk an unscoped (cross-tenant) query.
var ErrNoTenant = errors.New("db: missing or invalid tenant id")

// NewPool opens a pgx connection pool for the given connection string (the app
// role's DATABASE_URL). The caller owns the pool and must Close it on shutdown.
func NewPool(ctx context.Context, dsn string) (*pgxpool.Pool, error) {
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return nil, fmt.Errorf("db: open pool: %w", err)
	}
	return pool, nil
}

// WithinTenantTx runs fn inside a single transaction whose app.current_tenant GUC
// is set to tenantID for the life of that transaction. It uses set_config(name,
// value, is_local=true) — the parameterizable form of SET LOCAL, since the bare
// SET LOCAL statement cannot bind $1. Because the setting is transaction-local it
// vanishes the moment the transaction ends and can never bleed onto the next
// borrower of a pooled connection (the property M2-07 proves adversarially). This
// is the ONLY sanctioned way to touch tenant-scoped tables: every repository call
// wraps one of these.
//
// tenantID is validated as a UUID before the transaction opens; an empty or
// malformed id returns ErrNoTenant and issues no statement (fail-closed). A non-nil
// return from fn — or a panic — rolls the transaction back; nil commits.
func WithinTenantTx(ctx context.Context, pool *pgxpool.Pool, tenantID string, fn func(pgx.Tx) error) error {
	if _, err := uuid.Parse(tenantID); err != nil {
		return ErrNoTenant
	}

	tx, err := pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("db: begin tx: %w", err)
	}
	// Roll back on any early return or panic. After a successful Commit this is a
	// no-op (Rollback on a committed tx returns ErrTxClosed, which we ignore).
	defer func() { _ = tx.Rollback(ctx) }()

	if _, err := tx.Exec(ctx, "SELECT set_config('app.current_tenant', $1, true)", tenantID); err != nil {
		return fmt.Errorf("db: set tenant context: %w", err)
	}

	if err := fn(tx); err != nil {
		return err
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("db: commit tx: %w", err)
	}
	return nil
}

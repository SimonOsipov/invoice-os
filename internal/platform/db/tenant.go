package db

import (
	"context"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
)

// WithinRequestTenantTx is the HTTP-path convenience over WithinTenantTx: it pulls
// the tenant from the verified Identity the auth middleware placed in ctx, so
// handlers don't thread the tenant id by hand. It returns ErrNoTenant when no
// identity is present — an unauthenticated request must never reach tenant-scoped
// data.
//
// The core WithinTenantTx stays free of this auth dependency on purpose: the M5
// worker has no request identity and calls WithinTenantTx directly with the job's
// tenant_id (the worker-role pattern, docs/migrations.md).
func WithinRequestTenantTx(ctx context.Context, pool *pgxpool.Pool, fn func(pgx.Tx) error) error {
	id, ok := auth.IdentityFromContext(ctx)
	if !ok {
		return ErrNoTenant
	}
	return WithinTenantTx(ctx, pool, id.TenantID, fn)
}

package db

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
)

// ErrNotActiveMember refuses a request whose caller holds a membership row in
// this tenant that is not 'active'. Distinct from ErrNoTenant (401) on purpose:
// the caller IS authenticated and IS in the right tenant (D-3).
var ErrNotActiveMember = errors.New("db: caller's membership in this workspace is not active")

// NotActiveMemberMessage is the wire body for ErrNotActiveMember. Written down
// once in Go — TestHandlerMappingMessageIsNeverRetyped. Two TypeScript copies are
// hand-maintained and pinned to this literal by frontend/app/src/lib/wireMirrors.test.ts:
// lib/authedFetch.ts's NOT_ACTIVE_MEMBER_MESSAGE and e2e/api/suspension.spec.ts's
// NOT_ACTIVE_MESSAGE. Reword all three together.
const NotActiveMemberMessage = "your membership in this workspace is not active"

// WithinRequestTenantTx is the HTTP path's entry point: it pulls the tenant from
// the verified Identity the auth middleware placed in ctx, so handlers don't thread
// the tenant id by hand. It returns ErrNoTenant when no identity is present — an
// unauthenticated request must never reach tenant-scoped data — and
// ErrNotActiveMember when the caller's membership in that tenant is not active. It
// no longer delegates to WithinTenantTx; see WithinRequestTenantTxOpts below.
//
// The core WithinTenantTx stays free of this auth dependency on purpose: the M5
// worker has no request identity and calls WithinTenantTx directly with the job's
// tenant_id (the worker-role pattern, docs/migrations.md).
func WithinRequestTenantTx(ctx context.Context, pool *pgxpool.Pool, fn func(pgx.Tx) error) error {
	return WithinRequestTenantTxOpts(ctx, pool, pgx.TxOptions{}, fn)
}

// WithinRequestTenantTxOpts is WithinRequestTenantTx with caller-chosen transaction
// options, passed straight through to pool.BeginTx (D-33, AUDIT-05-07) —
// TestRLS_RequestSeamHonoursTxOptionsWhileGating.
//
// It also gates the read path on membership status: a caller whose row in the
// current tenant exists and is not 'active' is refused with ErrNotActiveMember
// before the closure runs. No row at all still proceeds (D-17, NARROW) —
// TestRLS_RequestSeamAllowsACallerWithNoMembershipRow.
func WithinRequestTenantTxOpts(ctx context.Context, pool *pgxpool.Pool, opts pgx.TxOptions, fn func(pgx.Tx) error) error {
	id, ok := auth.IdentityFromContext(ctx)
	if !ok {
		return ErrNoTenant
	}
	if _, err := uuid.Parse(id.TenantID); err != nil {
		return ErrNoTenant
	}
	// memberships.user_id is uuid: a non-uuid subject can match no row, and a
	// failed statement would poison the batch's transaction.
	if _, err := uuid.Parse(id.Subject); err != nil {
		return WithinTenantTxOpts(ctx, pool, id.TenantID, opts, fn)
	}

	tx, err := pool.BeginTx(ctx, opts)
	if err != nil {
		return fmt.Errorf("db: begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// One round trip: the gate rides the set_config the seam already sends (D-1).
	// set_config is queued first because the membership select is RLS-scoped by
	// the GUC it sets — TestRLS_RequestSeamIssuesOneRoundTripForTheGate.
	b := &pgx.Batch{}
	b.Queue("SELECT set_config('app.current_tenant', $1, true)", id.TenantID)
	b.Queue("SELECT status FROM memberships WHERE user_id = $1", id.Subject)
	br := tx.SendBatch(ctx, b)
	if _, err := br.Exec(); err != nil {
		_ = br.Close()
		return fmt.Errorf("db: set tenant context: %w", err)
	}
	var status string
	scanErr := br.QueryRow().Scan(&status)
	// Close returns the batch's stored error first, so scanErr must be classified
	// before closeErr or the specific wrap below is unreachable.
	closeErr := br.Close()
	switch {
	case scanErr != nil && !errors.Is(scanErr, pgx.ErrNoRows):
		return fmt.Errorf("db: read caller membership: %w", scanErr)
	case closeErr != nil:
		return fmt.Errorf("db: close batch: %w", closeErr)
	case scanErr == nil && status != "active":
		return ErrNotActiveMember
	}

	if err := fn(tx); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("db: commit tx: %w", err)
	}
	return nil
}

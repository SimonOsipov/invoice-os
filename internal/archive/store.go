package archive

import (
	"context"
	"io"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/SimonOsipov/invoice-os/internal/audit"
	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
	"github.com/SimonOsipov/invoice-os/internal/platform/db"
)

// Store assembles evidence bundles as invoice_app. It holds the app-role pool
// (DATABASE_URL); the caller owns the pool's lifecycle.
type Store struct {
	pool        *pgxpool.Pool
	maxInvoices int
	auditTx     func(ctx context.Context, tenantID string, write func(pgx.Tx) error) error // test seam; nil = recordBundleEvent
}

// NewStore wires the production cap (D-34) -- assemble treats a non-positive
// cap as a programming error, not a default, so a forgotten wiring cannot
// silently pass.
func NewStore(pool *pgxpool.Pool) *Store {
	return &Store{pool: pool, maxInvoices: maxBundleInvoices}
}

// Assemble streams one evidence bundle to w. Nothing reaches w until the entity
// resolves and the invoice count clears the cap (D-33, D-37). onStart, when
// non-nil, fires once with the bundle filename before the first byte (D-41);
// a nil onStart is a no-op.
func (s *Store) Assemble(ctx context.Context, r Request, w io.Writer, onStart func(filename string)) error {
	id, ok := auth.IdentityFromContext(ctx)
	if !ok {
		return db.ErrNoTenant
	}
	read := func(fw io.Writer) (*bundleWriter, error) {
		var bw *bundleWriter
		err := db.WithinRequestTenantTxOpts(ctx, s.pool, bundleTxOptions, func(tx pgx.Tx) error {
			var err error
			bw, err = buildBundle(ctx, tx, r, fw, assembleOpts{
				tenantID:    id.TenantID,
				subject:     id.Subject,
				maxInvoices: s.maxInvoices,
				now:         time.Now(),
				onStart:     onStart,
			})
			return err
		})
		return bw, err
	}
	return s.streamAudited(ctx, w, id.TenantID, id.Subject, r, read,
		func(ctx context.Context, tx pgx.Tx, p bundleAudit) error {
			return memberBundleExported(ctx, tx, id.Subject, p)
		},
		func(ctx context.Context, tx pgx.Tx, p bundleAudit) error {
			return memberBundleFailed(ctx, tx, id.Subject, p)
		})
}

func memberBundleExported(ctx context.Context, tx pgx.Tx, subject string, p bundleAudit) error {
	return audit.Record(ctx, tx, subject, "evidence_bundle.exported", p)
}

func memberBundleFailed(ctx context.Context, tx pgx.Tx, subject string, p bundleAudit) error {
	return audit.Record(ctx, tx, subject, "evidence_bundle.export_failed", p)
}

// Preview reports what Assemble would produce, without producing it. Unlike
// Assemble it needs neither TenantID nor Subject (the response carries no tenant
// id and no generated_by), so WithinRequestTenantTxOpts' own fail-closed
// ErrNoTenant is the whole identity check -- no actor.Resolve call (D-51).
func (s *Store) Preview(ctx context.Context, r Request) (Preview, error) {
	var p Preview
	err := db.WithinRequestTenantTxOpts(ctx, s.pool, bundleTxOptions, func(tx pgx.Tx) error {
		var err error
		p, err = preview(ctx, tx, r, previewOpts{maxInvoices: s.maxInvoices})
		return err
	})
	if err != nil {
		return Preview{}, err
	}
	return p, nil
}

package erp

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"

	"github.com/SimonOsipov/invoice-os/internal/audit"
	"github.com/SimonOsipov/invoice-os/internal/platform/db"
	"github.com/SimonOsipov/invoice-os/internal/platform/queue"
)

// releaseGrant runs after the caller cleared its connection's grant_id. It serialises on an
// advisory lock because invoice_app cannot row-lock erp_grants. A grant no connection uses is
// deleted and its secret's delete is enqueued in the same transaction.
func releaseGrant(ctx context.Context, tx pgx.Tx, q *queue.Client, grantID, connectionID string) error {
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('erp_grant:' || $1::text, 0))`, grantID); err != nil {
		return err
	}
	var tenantID, system string
	var secretRef *string
	err := tx.QueryRow(ctx,
		`DELETE FROM erp_grants WHERE id = $1 AND NOT EXISTS (SELECT 1 FROM erp_connections WHERE grant_id = $1)
		 RETURNING tenant_id::text, system, secret_ref`, grantID).Scan(&tenantID, &system, &secretRef)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil // a sibling already deleted it, or another connection still uses it
	}
	if err != nil || secretRef == nil {
		return err
	}
	_, err = q.EnqueueTx(ctx, tx, tenantID, "erp_secret_delete:"+grantID,
		SecretDeleteArgs{TenantID: tenantID, SecretRef: *secretRef, ConnectionID: connectionID, System: system}, nil)
	return err
}

// SecretDeleteArgs deletes one released grant's secret. The tenant key is tenant_id: purge_tenant
// deletes a purged tenant's jobs by it.
type SecretDeleteArgs struct {
	TenantID     string `json:"tenant_id"`
	SecretRef    string `json:"secret_ref"`
	ConnectionID string `json:"connection_id"`
	System       string `json:"system"`
}

func (SecretDeleteArgs) Kind() string     { return "erp_secret_delete" }
func (a SecretDeleteArgs) Tenant() string { return a.TenantID }

// InsertOpts: retried until it succeeds, in practice; about 41 days of hourly retries, then the discard is reported.
func (SecretDeleteArgs) InsertOpts() river.InsertOpts { return river.InsertOpts{MaxAttempts: 1000} }

// SecretDeleteWorker deletes the secret, then records erp.token.deleted once.
type SecretDeleteWorker struct {
	river.WorkerDefaults[SecretDeleteArgs]
	Pool    *pgxpool.Pool
	Secrets SecretStore
}

const secretDeleteRetryCap = time.Hour

// NextRetry is attempt^4 seconds (River's default) capped at an hour.
func (*SecretDeleteWorker) NextRetry(job *river.Job[SecretDeleteArgs]) time.Time {
	a := int64(job.Attempt)
	if a >= 8 { // 8^4 s already exceeds the cap; also keeps a^4 from overflowing
		return time.Now().Add(secretDeleteRetryCap)
	}
	return time.Now().Add(time.Duration(a*a*a*a) * time.Second)
}

func (w *SecretDeleteWorker) Work(ctx context.Context, job *river.Job[SecretDeleteArgs]) error {
	args := job.Args
	if err := w.Secrets.Delete(ctx, args.SecretRef); err != nil {
		return fmt.Errorf("erp: delete secret: %w", err)
	}
	return db.WithinTenantTx(ctx, w.Pool, args.TenantID, func(tx pgx.Tx) error {
		_, err := queue.OncePerJob(ctx, tx, args.TenantID, job.ID, func() error {
			return audit.Record(ctx, tx, "system", "erp.token.deleted", map[string]any{"system": args.System, "connection_id": args.ConnectionID})
		})
		return err
	})
}

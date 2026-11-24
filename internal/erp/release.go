package erp

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"

	"github.com/SimonOsipov/invoice-os/internal/audit"
	"github.com/SimonOsipov/invoice-os/internal/platform/db"
	"github.com/SimonOsipov/invoice-os/internal/platform/queue"
)

// releaseGrant runs after the caller cleared its connection's grant_id. It serialises on an
// advisory lock because invoice_app cannot row-lock erp_grants. A grant no connection uses is
// deleted and its secret's delete is enqueued in the same transaction. revoke asks the job to end the
// grant at the vendor first (a Disconnect); a reconnect's old grant is not revoked.
func releaseGrant(ctx context.Context, tx pgx.Tx, q *queue.Client, grantID, connectionID string, revoke bool) error {
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('erp_grant:' || $1::text, 0))`, grantID); err != nil {
		return err
	}
	var tenantID, system, kind string
	var secretRef *string
	err := tx.QueryRow(ctx,
		`DELETE FROM erp_grants WHERE id = $1 AND NOT EXISTS (SELECT 1 FROM erp_connections WHERE grant_id = $1)
		 RETURNING tenant_id::text, system, kind, secret_ref`, grantID).Scan(&tenantID, &system, &kind, &secretRef)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil // a sibling already deleted it, or another connection still uses it
	}
	if err != nil || secretRef == nil {
		return err
	}
	args := SecretDeleteArgs{TenantID: tenantID, SecretRef: *secretRef, ConnectionID: connectionID, System: system}
	if revoke {
		args.Revoke, args.GrantKind = true, kind
		var key *string
		if err := tx.QueryRow(ctx, `SELECT external_key FROM erp_connections WHERE id = $1`, connectionID).Scan(&key); err != nil {
			return err
		}
		if key != nil {
			args.ExternalKey = *key
		}
	}
	_, err = q.EnqueueTx(ctx, tx, tenantID, "erp_secret_delete:"+grantID, args, nil)
	return err
}

// SecretDeleteArgs deletes one released grant's secret. The tenant key is tenant_id: purge_tenant
// deletes a purged tenant's jobs by it.
type SecretDeleteArgs struct {
	TenantID     string `json:"tenant_id"`
	SecretRef    string `json:"secret_ref"`
	ConnectionID string `json:"connection_id"`
	System       string `json:"system"`
	// Revoke ends the grant at the vendor before the delete; GrantKind is the released grant's kind,
	// which the row no longer holds when the job runs.
	Revoke    bool   `json:"revoke,omitempty"`
	GrantKind string `json:"grant_kind,omitempty"`
	// ExternalKey is the vendor company of the released connection; empty on a job enqueued before it existed.
	ExternalKey string `json:"external_key,omitempty"`
}

func (SecretDeleteArgs) Kind() string     { return "erp_secret_delete" }
func (a SecretDeleteArgs) Tenant() string { return a.TenantID }

// InsertOpts: retried until it succeeds, in practice; about 41 days of hourly retries, then the discard is reported.
func (SecretDeleteArgs) InsertOpts() river.InsertOpts { return river.InsertOpts{MaxAttempts: 1000} }

// SecretDeleteWorker deletes the secret, then records erp.token.deleted once.
type SecretDeleteWorker struct {
	river.WorkerDefaults[SecretDeleteArgs]
	Pool     *pgxpool.Pool
	Secrets  SecretStore
	Registry Registry
	Logger   *slog.Logger

	revokeTimeout time.Duration // tests shorten it; zero is revokeBudget
}

const revokeBudget = 10 * time.Second

func (w *SecretDeleteWorker) log() *slog.Logger {
	if w.Logger != nil {
		return w.Logger
	}
	return slog.Default()
}

// revoke ends the grant at the vendor. A failure is logged with the system only and never blocks
// the delete: the secret must go either way.
func (w *SecretDeleteWorker) revoke(ctx context.Context, args SecretDeleteArgs) {
	conn, ok := w.Registry[args.System]
	if !ok {
		w.log().WarnContext(ctx, "erp: vendor revoke skipped: no connector", slog.String("system", args.System))
		return
	}
	payload, _, err := w.Secrets.Access(ctx, args.SecretRef)
	if errors.Is(err, ErrSecretNotFound) {
		return
	}
	if err != nil {
		w.log().WarnContext(ctx, "erp: vendor revoke skipped: secret unreadable", slog.String("system", args.System))
		return
	}
	budget := w.revokeTimeout
	if budget == 0 {
		budget = revokeBudget
	}
	rctx, cancel := context.WithTimeout(ctx, budget)
	defer cancel()
	err = conn.Revoke(rctx, Credentials{Kind: GrantKind(args.GrantKind), Secret: payload})
	if err != nil && !errors.Is(err, ErrUnsupported) {
		w.log().WarnContext(ctx, "erp: vendor revoke failed", slog.String("system", args.System))
	}
}

// realmShared reports whether another active connection, of any tenant, holds the vendor company:
// a vendor revoke may end the app's authorization for the whole company.
func (w *SecretDeleteWorker) realmShared(ctx context.Context, args SecretDeleteArgs) (bool, error) {
	var shared bool
	err := db.WithinTenantTx(ctx, w.Pool, uuid.Nil.String(), func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM public.erp_webhook_targets($1, $2))`, args.System, args.ExternalKey).Scan(&shared)
	})
	return shared, err
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
	if args.Revoke {
		shared := false
		if args.ExternalKey != "" {
			var err error
			if shared, err = w.realmShared(ctx, args); err != nil {
				return fmt.Errorf("erp: realm lookup: %w", err)
			}
		}
		if !shared {
			w.revoke(ctx, args)
		}
	}
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

package erp

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"

	"github.com/SimonOsipov/invoice-os/internal/audit"
	"github.com/SimonOsipov/invoice-os/internal/platform/db"
	"github.com/SimonOsipov/invoice-os/internal/platform/queue"
)

// RefreshArgs renews one grant's access token. The tenant key is tenant_id: purge_tenant deletes a
// purged tenant's jobs by it. Version is the grant version the job claims.
type RefreshArgs struct {
	TenantID string `json:"tenant_id"`
	GrantID  string `json:"grant_id"`
	Version  int64  `json:"version"`
}

func (RefreshArgs) Kind() string     { return "erp_token_refresh" }
func (a RefreshArgs) Tenant() string { return a.TenantID }

// InsertOpts: eight attempts, the budget of the submission job.
func (RefreshArgs) InsertOpts() river.InsertOpts { return river.InsertOpts{MaxAttempts: 8} }

// refreshAddVersionTries and the delay between them bound the in-attempt AddVersion retry: the
// vendor may already have consumed the refresh token it was sent, so the new material is kept.
const (
	refreshAddVersionTries = 5
	refreshAddVersionDelay = time.Second
	refreshAddVersionLimit = 30 * time.Second
)

// errNotClaimed rolls the claim back when the grant is gone or at another version.
var errNotClaimed = errors.New("erp: refresh not claimed")

// RefreshWorker renews a grant's token and chains the next job.
type RefreshWorker struct {
	river.WorkerDefaults[RefreshArgs]
	Pool     *pgxpool.Pool
	Registry Registry
	Secrets  SecretStore
	Queue    *queue.Client
	Logger   *slog.Logger

	addVersionDelay time.Duration // zero means refreshAddVersionDelay; tests shorten it
}

type claimedGrant struct{ system, kind, secretRef string }

func (w *RefreshWorker) Work(ctx context.Context, job *river.Job[RefreshArgs]) error {
	err := w.refresh(ctx, job)
	if err == nil {
		return nil
	}
	a := job.Args
	if errors.Is(err, ErrGrantRevoked) {
		if terr := w.reconsent(ctx, a, "revoked"); terr != nil {
			return terr
		}
		return river.JobCancel(err)
	}
	if job.Attempt >= job.MaxAttempts {
		if terr := w.reconsent(ctx, a, "exhausted"); terr != nil {
			return terr
		}
	}
	return err
}

func (w *RefreshWorker) log() *slog.Logger {
	if w.Logger != nil {
		return w.Logger
	}
	return slog.Default()
}

func (w *RefreshWorker) refresh(ctx context.Context, job *river.Job[RefreshArgs]) error {
	a := job.Args
	g, err := w.claim(ctx, job)
	if err != nil || g == nil {
		return err
	}
	claimed := a.Version + 1
	conn, ok := w.Registry[g.system]
	if !ok || w.Secrets == nil {
		return fmt.Errorf("erp: no connector or secret store for system %q", g.system)
	}
	old, oldVersion, err := w.Secrets.Access(ctx, g.secretRef)
	if err != nil {
		return fmt.Errorf("erp: read grant secret: %w", err)
	}
	next, err := conn.Refresh(ctx, Credentials{Kind: GrantKind(g.kind), Secret: old})
	if err != nil {
		return fmt.Errorf("erp: refresh: %w", err)
	}
	if err := w.addVersion(ctx, g.secretRef, next.Secret); err != nil {
		w.log().ErrorContext(ctx, "erp: refreshed token not stored", "grant_id", a.GrantID, "attempt", job.Attempt)
		return fmt.Errorf("erp: store refreshed grant: %w", err)
	}
	err = db.WithinTenantTx(ctx, w.Pool, a.TenantID, func(tx pgx.Tx) error {
		ct, err := tx.Exec(ctx, `UPDATE erp_grants SET expires_at = $3, updated_at = now() WHERE id = $1 AND version = $2`,
			a.GrantID, claimed, next.ExpiresAt)
		if err != nil {
			return err
		}
		if ct.RowsAffected() == 0 {
			return errNotClaimed // released while the vendor call ran
		}
		if err := audit.Record(ctx, tx, "system", "erp.token.refreshed", map[string]any{"system": g.system, "grant_id": a.GrantID}); err != nil {
			return err
		}
		if next.ExpiresAt == nil {
			return nil
		}
		_, err = w.Queue.EnqueueTx(ctx, tx, a.TenantID, fmt.Sprintf("erp_refresh:%s:%d", a.GrantID, claimed),
			RefreshArgs{TenantID: a.TenantID, GrantID: a.GrantID, Version: claimed}, &river.InsertOpts{ScheduledAt: next.ExpiresAt.Add(-refreshLead)})
		return err
	})
	if errors.Is(err, errNotClaimed) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("erp: record refresh: %w", err)
	}
	// ceiling: a failed disable leaves that one older version enabled; the vendor already rotated it.
	if err := w.Secrets.DisableVersion(ctx, g.secretRef, oldVersion); err != nil {
		w.log().ErrorContext(ctx, "erp: old grant version not disabled", "grant_id", a.GrantID)
	}
	return nil
}

// claim bumps the grant's version, once per job. A job whose marker exists is its own retry and
// proceeds as the claimant. It returns nil when the grant is gone or at another version.
func (w *RefreshWorker) claim(ctx context.Context, job *river.Job[RefreshArgs]) (*claimedGrant, error) {
	a := job.Args
	var g claimedGrant
	var ref *string
	err := db.WithinTenantTx(ctx, w.Pool, a.TenantID, func(tx pgx.Tx) error {
		_, err := queue.OncePerJob(ctx, tx, a.TenantID, job.ID, func() error {
			ct, err := tx.Exec(ctx, `UPDATE erp_grants SET version = version + 1, updated_at = now() WHERE id = $1 AND version = $2`, a.GrantID, a.Version)
			if err != nil {
				return err
			}
			if ct.RowsAffected() == 0 {
				return errNotClaimed
			}
			return nil
		})
		if err != nil {
			return err
		}
		return tx.QueryRow(ctx, `SELECT system, kind, secret_ref FROM erp_grants WHERE id = $1 AND version = $2`, a.GrantID, a.Version+1).Scan(&g.system, &g.kind, &ref)
	})
	if errors.Is(err, errNotClaimed) || errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("erp: claim refresh: %w", err)
	}
	if ref == nil {
		return nil, errors.New("erp: grant has no secret")
	}
	g.secretRef = *ref
	return &g, nil
}

// addVersion retries on a context that outlives a cancelled attempt.
func (w *RefreshWorker) addVersion(ctx context.Context, ref string, payload []byte) error {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), refreshAddVersionLimit)
	defer cancel()
	delay := w.addVersionDelay
	if delay == 0 {
		delay = refreshAddVersionDelay
	}
	var err error
	for i := 0; i < refreshAddVersionTries; i++ {
		if _, err = w.Secrets.AddVersion(ctx, ref, payload); err == nil {
			return nil
		}
		if i == refreshAddVersionTries-1 {
			break
		}
		select {
		case <-time.After(delay << i):
		case <-ctx.Done():
			return err
		}
	}
	return err
}

// reconsent moves every active connection on the grant to reconsent and records why. The status
// change is the once-guard: a repeat finds no active row and writes nothing.
func (w *RefreshWorker) reconsent(ctx context.Context, a RefreshArgs, reason string) error {
	return db.WithinTenantTx(ctx, w.Pool, a.TenantID, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `UPDATE erp_connections SET status = 'reconsent', updated_at = now() WHERE grant_id = $1 AND status = 'active' RETURNING id::text, system`, a.GrantID)
		if err != nil {
			return err
		}
		type moved struct{ id, system string }
		var got []moved
		for rows.Next() {
			var m moved
			if err := rows.Scan(&m.id, &m.system); err != nil {
				return err
			}
			got = append(got, m)
		}
		if err := rows.Err(); err != nil {
			return err
		}
		for _, m := range got {
			if err := audit.Record(ctx, tx, "system", "erp.connection.reauth_required", map[string]any{"system": m.system, "connection_id": m.id}); err != nil {
				return err
			}
		}
		if len(got) == 0 {
			return nil
		}
		return audit.Record(ctx, tx, "system", "erp.token.refresh_failed", map[string]any{"system": got[0].system, "grant_id": a.GrantID, "reason": reason})
	})
}

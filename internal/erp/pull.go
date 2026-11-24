package erp

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"

	"github.com/SimonOsipov/invoice-os/internal/platform/db"
	"github.com/SimonOsipov/invoice-os/internal/platform/queue"
	"github.com/SimonOsipov/invoice-os/internal/reconciliation"
)

// PullInterval is how often the sweep enqueues a pull per active connection.
const PullInterval = time.Minute

// maxPullPages bounds one job; the next sweep continues from the saved cursor.
const maxPullPages = 50

// PullArgs is one connection's pull.
type PullArgs struct {
	TenantID     string `json:"tenant_id"`
	ConnectionID string `json:"connection_id"`
}

func (PullArgs) Kind() string { return "erp.pull" }

func (a PullArgs) Tenant() string { return a.TenantID }

// InsertOpts omits completed so the next sweep can enqueue again; River requires the other four.
func (PullArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{MaxAttempts: 8, UniqueOpts: river.UniqueOpts{ByArgs: true, ByState: inFlightStates}}
}

var inFlightStates = []rivertype.JobState{
	rivertype.JobStateAvailable,
	rivertype.JobStatePending,
	rivertype.JobStateRetryable,
	rivertype.JobStateRunning,
	rivertype.JobStateScheduled,
}

type PullWorker struct {
	river.WorkerDefaults[PullArgs]
	Pool     *pgxpool.Pool
	Queue    *queue.Client
	Registry Registry
	Logger   *slog.Logger
}

type pullState struct {
	connInfo
	cursor []byte
	epoch  int64
}

func readPullState(ctx context.Context, tx pgx.Tx, id string, lock bool) (pullState, bool, error) {
	q := `SELECT entity_id::text, system, coalesce(external_key, ''), status, grant_id::text, cursor, ingest_epoch
	      FROM erp_connections WHERE id = $1`
	if lock {
		q += " FOR NO KEY UPDATE"
	}
	var s pullState
	err := tx.QueryRow(ctx, q, id).Scan(&s.entity, &s.system, &s.externalKey, &s.status, &s.grantID, &s.cursor, &s.epoch)
	if errors.Is(err, pgx.ErrNoRows) {
		return pullState{}, false, nil
	}
	return s, err == nil, err
}

func (w *PullWorker) Work(ctx context.Context, job *river.Job[PullArgs]) error {
	a := job.Args
	var (
		start pullState
		found bool
		creds Credentials
	)
	err := db.WithinTenantTx(ctx, w.Pool, a.TenantID, func(tx pgx.Tx) error {
		var err error
		if start, found, err = readPullState(ctx, tx, a.ConnectionID, false); err != nil || !found {
			return err
		}
		if start.status != "active" || w.Registry[start.system] == nil {
			found = false
			return nil
		}
		locked, err := db.WorkspaceLocked(ctx, tx)
		if err != nil || locked {
			found = false
			return err
		}
		creds, err = credentialsFor(ctx, tx, start.grantID)
		return err
	})
	if err != nil || !found {
		return err
	}
	conn := w.Registry[start.system]
	cursor := start.cursor
	for page := 0; page < maxPullPages; page++ {
		changes, err := conn.ListChanged(ctx, creds, start.externalKey, cursor)
		var rl *RateLimitError
		switch {
		case errors.As(err, &rl):
			return river.JobSnooze(rl.RetryAfter)
		case errors.Is(err, ErrBadCursor):
			// Keep ingest_epoch: the spent keys dedupe the relisted documents.
			_, err = w.commitPage(ctx, a, start, cursor, nil, nil)
			return err
		case err != nil:
			return err
		}
		ok, err := w.commitPage(ctx, a, start, cursor, &changes, changes.Cursor)
		if err != nil || !ok || !changes.More {
			return err
		}
		cursor = changes.Cursor
	}
	return nil
}

// commitPage enqueues the page's ingests and saves the cursor in one transaction. With no page it
// only nulls the cursor. It reports false when the connection changed since the pull began.
func (w *PullWorker) commitPage(ctx context.Context, a PullArgs, start pullState, from []byte, changes *Changes, next []byte) (bool, error) {
	committed := false
	err := db.WithinTenantTx(ctx, w.Pool, a.TenantID, func(tx pgx.Tx) error {
		cur, found, err := readPullState(ctx, tx, a.ConnectionID, true)
		if err != nil || !found {
			return err
		}
		if cur.status != "active" || cur.epoch != start.epoch || !bytes.Equal(cur.cursor, from) {
			return nil
		}
		if changes != nil {
			for _, ref := range changes.Refs {
				args := IngestArgs{TenantID: a.TenantID, ConnectionID: a.ConnectionID, SourceDocID: ref.SourceDocID, SourceRevision: ref.SourceRevision, Deleted: ref.Deleted}
				key := IngestKey(a.ConnectionID, cur.epoch, ref.SourceDocID, ref.SourceRevision, ref.Deleted)
				if _, err := w.Queue.EnqueueTx(ctx, tx, a.TenantID, key, args, nil); err != nil {
					return err
				}
			}
		}
		if _, err := tx.Exec(ctx, `UPDATE erp_connections SET cursor = $2, last_sync_at = CASE WHEN $3 THEN now() ELSE last_sync_at END, updated_at = now() WHERE id = $1`,
			a.ConnectionID, next, changes != nil); err != nil {
			return err
		}
		committed = true
		return nil
	})
	return committed, err
}

// Sweeper enqueues one pull per active connection of a registered system.
type Sweeper struct {
	Reader, Pool *pgxpool.Pool
	River        *river.Client[pgx.Tx]
	Registry     Registry
	Logger       *slog.Logger
}

func (s *Sweeper) SweepOnce(ctx context.Context) error {
	tenants, err := reconciliation.EnumerateTenants(ctx, s.Reader)
	if err != nil {
		return err
	}
	systems := s.Registry.Systems()
	var errs []error
	for _, tenant := range tenants {
		err := db.WithinTenantTx(ctx, s.Pool, tenant, func(tx pgx.Tx) error {
			if locked, err := db.WorkspaceLocked(ctx, tx); err != nil || locked {
				return err
			}
			rows, err := tx.Query(ctx, `SELECT id::text FROM erp_connections WHERE status = 'active' AND system = ANY($1)`, systems)
			if err != nil {
				return err
			}
			ids, err := pgx.CollectRows(rows, pgx.RowTo[string])
			if err != nil {
				return err
			}
			for _, id := range ids {
				if _, err := s.River.InsertTx(ctx, tx, PullArgs{TenantID: tenant, ConnectionID: id}, nil); err != nil {
					return err
				}
			}
			return nil
		})
		if err != nil {
			errs = append(errs, fmt.Errorf("erp sweep tenant %s: %w", tenant, err))
			if s.Logger != nil {
				s.Logger.Error("erp pull sweep", "tenant_id", tenant, "err", err)
			}
		}
	}
	return errors.Join(errs...)
}

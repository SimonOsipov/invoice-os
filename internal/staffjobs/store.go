// Package staffjobs is the staff Submissions surface.
package staffjobs

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
	"github.com/riverqueue/river/rivertype"

	"github.com/SimonOsipov/invoice-os/internal/audit"
	"github.com/SimonOsipov/invoice-os/internal/invoice"
	"github.com/SimonOsipov/invoice-os/internal/platform/db"
	"github.com/SimonOsipov/invoice-os/internal/submission"
)

var (
	ErrJobNotFound    = errors.New("staffjobs: job not found")
	ErrJobState       = errors.New("staffjobs: job is not in the required state")
	ErrAcknowledged   = errors.New("staffjobs: the Access Point acknowledged this job")
	ErrRiverDuplicate = errors.New("staffjobs: another River job holds this submission")
	ErrRiverRunning   = errors.New("staffjobs: the previous River job is running")
)

// recordStaff is a var so a test can force an audit failure.
var recordStaff = audit.RecordStaff

// readAfterCommit is a var so a test can force the post-commit read to fail.
var readAfterCommit = (*Store).Get

// committed returns the job as read after a commit. A failed read never fails the action:
// it answers from what the write knows (before plus the new state and one history step).
func (s *Store) committed(ctx context.Context, before Detail, state, invoiceStatus string, attempts *int) Detail {
	d, err := readAfterCommit(s, ctx, before.Job.ID)
	if err == nil {
		return d
	}
	slog.ErrorContext(ctx, "staffjobs: re-read after commit", "job_id", before.Job.ID, "err", err)
	d = Detail{Job: before.Job, History: append([]Step(nil), before.History...)}
	d.Job.State, d.Job.InvoiceStatus = state, invoiceStatus
	if attempts != nil {
		d.Job.Attempts = *attempts
	}
	from := before.Job.InvoiceStatus
	now := time.Now().UTC()
	d.Job.UpdatedAt = now
	d.History = append(d.History, Step{FromStatus: &from, ToStatus: invoiceStatus, Actor: audit.SupportActor, ChangedAt: now})
	return d
}

// Job is the staff_read_submission_job(s) row. MaxAttempts is not a column; the handlers fill it.
type Job struct {
	ID            string    `json:"id"`
	TenantID      string    `json:"tenant_id"`
	TenantName    string    `json:"tenant_name"`
	InvoiceID     string    `json:"invoice_id"`
	InvoiceNumber string    `json:"invoice_number"`
	InvoiceStatus string    `json:"invoice_status"`
	State         string    `json:"state"`
	Attempts      int       `json:"attempts"`
	MaxAttempts   int       `json:"max_attempts" db:"-"`
	LastError     *string   `json:"last_error"`
	Acknowledged  bool      `json:"acknowledged"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
}

type Step struct {
	FromStatus *string   `json:"from_status"`
	ToStatus   string    `json:"to_status"`
	Actor      string    `json:"actor"`
	ChangedAt  time.Time `json:"changed_at"`
}

type Detail struct {
	Job     Job    `json:"job"`
	History []Step `json:"history"`
}

type Store struct {
	pool     *pgxpool.Pool
	invoices *invoice.Store
	river    *river.Client[pgx.Tx]
}

func NewStore(pool *pgxpool.Pool, invoices *invoice.Store, rc *river.Client[pgx.Tx]) *Store {
	return &Store{pool: pool, invoices: invoices, river: rc}
}

const jobColumns = `id::text, tenant_id::text, tenant_name, invoice_id::text, invoice_number, invoice_status,
	state, attempts, last_error, acknowledged, created_at, updated_at`

// List returns the newest 200 jobs of every tenant, filtered by state and tenant when given.
func (s *Store) List(ctx context.Context, state *string, tenant *uuid.UUID) ([]Job, error) {
	actor, err := db.StaffActor(ctx)
	if err != nil {
		return nil, err
	}
	var out []Job
	err = db.WithinStaffTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT `+jobColumns+` FROM public.staff_read_submission_jobs($1, $2, $3)`, actor, state, tenant)
		if err != nil {
			return db.StaffReadError("staffjobs: list jobs", err)
		}
		out, err = pgx.CollectRows(rows, pgx.RowToStructByPos[Job])
		return db.StaffReadError("staffjobs: list jobs", err)
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// Get returns one job and its invoice's status history, oldest first. A malformed id is ErrJobNotFound.
func (s *Store) Get(ctx context.Context, id string) (Detail, error) {
	actor, err := db.StaffActor(ctx)
	if err != nil {
		return Detail{}, err
	}
	jobID, err := uuid.Parse(id)
	if err != nil {
		return Detail{}, ErrJobNotFound
	}
	var out Detail
	err = db.WithinStaffTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT `+jobColumns+` FROM public.staff_read_submission_job($1, $2)`, actor, jobID)
		if err != nil {
			return db.StaffReadError("staffjobs: read job", err)
		}
		jobs, err := pgx.CollectRows(rows, pgx.RowToStructByPos[Job])
		if err != nil {
			return db.StaffReadError("staffjobs: read job", err)
		}
		if len(jobs) == 0 {
			return ErrJobNotFound
		}
		out.Job = jobs[0]

		rows, err = tx.Query(ctx, `SELECT from_status, to_status, actor, changed_at
			FROM public.staff_read_invoice_status_history($1, $2, $3)`, actor, out.Job.TenantID, out.Job.InvoiceID)
		if err != nil {
			return db.StaffReadError("staffjobs: read job", err)
		}
		out.History, err = pgx.CollectRows(rows, pgx.RowToStructByPos[Step])
		return db.StaffReadError("staffjobs: read job", err)
	})
	if err != nil {
		return Detail{}, err
	}
	return out, nil
}

// Redrive re-queues a dead_lettered job that the Access Point never acknowledged. reason is already trimmed.
func (s *Store) Redrive(ctx context.Context, id, reason string) (Detail, error) {
	actor, err := db.StaffActor(ctx)
	if err != nil {
		return Detail{}, err
	}
	// The tenant of the write comes from the staff read; the request carries none.
	before, err := s.Get(ctx, id)
	if err != nil {
		return Detail{}, err
	}
	job := before.Job
	err = db.WithinTenantTx(ctx, s.pool, job.TenantID, func(tx pgx.Tx) error {
		var tenantID, invoiceID, idemKey, state string
		var pollRef *string
		var riverJobID *int64
		if err := tx.QueryRow(ctx, `SELECT tenant_id::text, invoice_id::text, idempotency_key, state, poll_ref, river_job_id
			FROM submission_jobs WHERE id = $1 FOR UPDATE`, job.ID).
			Scan(&tenantID, &invoiceID, &idemKey, &state, &pollRef, &riverJobID); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrJobNotFound
			}
			return fmt.Errorf("staffjobs: lock job: %w", err)
		}
		if state != "dead_lettered" {
			return ErrJobState
		}
		if pollRef != nil {
			return ErrAcknowledged
		}
		if err := s.invoices.RequeueFailedTx(ctx, tx, invoiceID, tenantID); err != nil {
			return err
		}
		if riverJobID != nil {
			if _, err := s.river.JobDeleteTx(ctx, tx, *riverJobID); err != nil {
				switch {
				case errors.Is(err, rivertype.ErrNotFound):
				case errors.Is(err, rivertype.ErrJobRunning):
					return ErrRiverRunning
				default:
					return fmt.Errorf("staffjobs: delete old River job: %w", err)
				}
			}
		}
		args := submission.SubmitArgs{TenantID: tenantID, InvoiceID: invoiceID, IdempotencyKey: idemKey}
		if args.Tenant() != job.TenantID {
			return fmt.Errorf("staffjobs: job tenant %s differs from the transaction's %s", args.Tenant(), job.TenantID)
		}
		res, err := s.river.InsertTx(ctx, tx, args, nil)
		if err != nil {
			return fmt.Errorf("staffjobs: insert River job: %w", err)
		}
		if res.UniqueSkippedAsDuplicate {
			return ErrRiverDuplicate
		}
		if _, err := tx.Exec(ctx, `UPDATE submission_jobs SET state = 'queued', attempts = 0, river_job_id = $2 WHERE id = $1`,
			job.ID, res.Job.ID); err != nil {
			return fmt.Errorf("staffjobs: requeue job: %w", err)
		}
		tenantUUID, err := uuid.Parse(tenantID)
		if err != nil {
			return fmt.Errorf("staffjobs: job tenant: %w", err)
		}
		jobUUID, err := uuid.Parse(job.ID)
		if err != nil {
			return fmt.Errorf("staffjobs: job id: %w", err)
		}
		return recordStaff(ctx, tx, audit.StaffEvent{
			Actor:     actor,
			Event:     "submission.job.redriven",
			TenantID:  tenantUUID,
			SubjectID: jobUUID,
			Payload: map[string]string{
				"job_id": job.ID, "invoice_id": invoiceID, "invoice_number": job.InvoiceNumber, "reason": reason,
			},
		})
	})
	if err != nil {
		return Detail{}, err
	}
	zero := 0
	return s.committed(ctx, before, "queued", "queued", &zero), nil
}

// Cancel stops a queued job and fails its invoice. reason is already trimmed.
func (s *Store) Cancel(ctx context.Context, id, reason string) (Detail, error) {
	actor, err := db.StaffActor(ctx)
	if err != nil {
		return Detail{}, err
	}
	before, err := s.Get(ctx, id)
	if err != nil {
		return Detail{}, err
	}
	job := before.Job
	err = db.WithinTenantTx(ctx, s.pool, job.TenantID, func(tx pgx.Tx) error {
		var tenantID, invoiceID, state string
		var riverJobID *int64
		if err := tx.QueryRow(ctx, `SELECT tenant_id::text, invoice_id::text, state, river_job_id
			FROM submission_jobs WHERE id = $1 FOR UPDATE`, job.ID).
			Scan(&tenantID, &invoiceID, &state, &riverJobID); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrJobNotFound
			}
			return fmt.Errorf("staffjobs: lock job: %w", err)
		}
		if state != "queued" {
			return ErrJobState
		}
		if _, err := tx.Exec(ctx, `UPDATE submission_jobs SET state = 'cancelled' WHERE id = $1`, job.ID); err != nil {
			return fmt.Errorf("staffjobs: cancel job: %w", err)
		}
		if err := s.invoices.CancelQueuedTx(ctx, tx, invoiceID, tenantID); err != nil {
			return err
		}
		if riverJobID != nil {
			if _, err := s.river.JobCancelTx(ctx, tx, *riverJobID); err != nil && !errors.Is(err, rivertype.ErrNotFound) {
				return fmt.Errorf("staffjobs: cancel River job: %w", err)
			}
		}
		tenantUUID, err := uuid.Parse(tenantID)
		if err != nil {
			return fmt.Errorf("staffjobs: job tenant: %w", err)
		}
		jobUUID, err := uuid.Parse(job.ID)
		if err != nil {
			return fmt.Errorf("staffjobs: job id: %w", err)
		}
		return recordStaff(ctx, tx, audit.StaffEvent{
			Actor:     actor,
			Event:     "submission.job.cancelled",
			TenantID:  tenantUUID,
			SubjectID: jobUUID,
			Payload: map[string]string{
				"job_id": job.ID, "invoice_id": invoiceID, "invoice_number": job.InvoiceNumber, "reason": reason,
			},
		})
	})
	if err != nil {
		return Detail{}, err
	}
	return s.committed(ctx, before, "cancelled", "failed", nil), nil
}

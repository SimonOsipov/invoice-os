// Package staffjobs is the staff Submissions surface: cross-tenant reads of submission_jobs through staff_read_ functions.
package staffjobs

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"

	"github.com/SimonOsipov/invoice-os/internal/invoice"
	"github.com/SimonOsipov/invoice-os/internal/platform/db"
)

var ErrJobNotFound = errors.New("staffjobs: job not found")

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

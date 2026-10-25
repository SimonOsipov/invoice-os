package systemhealth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
	"github.com/SimonOsipov/invoice-os/internal/platform/db"
)

type Store struct{ Pool *pgxpool.Pool }

type Sample struct {
	TakenAt      time.Time
	FleetStatus  string
	ServicesDown *int
	JobCounts    map[string]int64
	KindCounts   map[string]map[string]int64 // nil for a sample from before per-kind sampling
}

// Live is the staff read of the moment: submission job states and River states per kind.
type Live struct {
	Jobs  map[string]int64
	Kinds map[string]map[string]int64
}

// Record writes the slot's sample and drops samples past the window, in one transaction.
func (s *Store) Record(ctx context.Context, slot time.Time, fleetStatus string, down *int) (bool, error) {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return false, fmt.Errorf("systemhealth: record: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	tag, err := tx.Exec(ctx, `INSERT INTO public.health_samples (taken_at, fleet_status, services_down, job_counts, kind_counts)
		SELECT $1, $2, $3, public.submission_job_state_counts(),
			(SELECT coalesce(jsonb_object_agg(k.kind, k.states), '{}'::jsonb)
			   FROM (SELECT c.kind, jsonb_object_agg(c.state, c.n) AS states
			           FROM (SELECT j.kind, j.state::text AS state, count(*) AS n FROM public.river_job j GROUP BY j.kind, j.state) c
			          GROUP BY c.kind) k)
		ON CONFLICT (taken_at) DO NOTHING`, slot, fleetStatus, down)
	if err != nil {
		return false, fmt.Errorf("systemhealth: record: %w", err)
	}
	if _, err := tx.Exec(ctx, `DELETE FROM public.health_samples WHERE taken_at < $1`, slot.Add(-TrendWindow)); err != nil {
		return false, fmt.Errorf("systemhealth: record: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return false, fmt.Errorf("systemhealth: record: %w", err)
	}
	return tag.RowsAffected() == 1, nil
}

// StaffCheck asks the database whether the caller is active staff and returns db.ErrNotStaff if not.
// ceiling: borrows staff_read_health_samples as the check; a rename or a change to its guard breaks it, add a dedicated function then
func (s *Store) StaffCheck(ctx context.Context) error {
	actor, err := db.StaffActor(ctx)
	if err != nil {
		return err
	}
	return db.WithinStaffTx(ctx, s.Pool, func(tx pgx.Tx) error {
		var n int64
		err := tx.QueryRow(ctx, `SELECT count(*) FROM public.staff_read_health_samples($1, 'infinity')`, actor).Scan(&n)
		return db.StaffReadError("systemhealth: staff metrics check", err)
	})
}

// StaffSnapshot reads the live counts and the samples since the given time, oldest first.
func (s *Store) StaffSnapshot(ctx context.Context, since time.Time) (Live, []Sample, error) {
	id, ok := auth.StaffFromContext(ctx)
	if !ok {
		return Live{}, nil, db.ErrNotStaff
	}
	actor, err := uuid.Parse(id.Subject)
	if err != nil {
		return Live{}, nil, db.ErrNotStaff
	}
	var live Live
	var samples []Sample
	err = db.WithinStaffTx(ctx, s.Pool, func(tx pgx.Tx) error {
		var raw []byte
		if err := tx.QueryRow(ctx, `SELECT public.staff_read_submission_job_counts($1)`, actor).Scan(&raw); err != nil {
			return staffReadError("systemhealth: staff health", err)
		}
		if err := json.Unmarshal(raw, &live.Jobs); err != nil {
			return fmt.Errorf("systemhealth: staff health: %w", err)
		}
		if err := tx.QueryRow(ctx, `SELECT public.staff_read_river_job_counts($1)`, actor).Scan(&raw); err != nil {
			return staffReadError("systemhealth: staff health", err)
		}
		if err := json.Unmarshal(raw, &live.Kinds); err != nil {
			return fmt.Errorf("systemhealth: staff health: %w", err)
		}
		rows, err := tx.Query(ctx, `SELECT taken_at, fleet_status, services_down, job_counts, kind_counts
			FROM public.staff_read_health_samples($1, $2)`, actor, since)
		if err != nil {
			return staffReadError("systemhealth: staff health", err)
		}
		samples, err = pgx.CollectRows(rows, pgx.RowToStructByPos[Sample])
		return staffReadError("systemhealth: staff health", err)
	})
	if err != nil {
		return Live{}, nil, err
	}
	return live, samples, nil
}

// ceiling: copy of staffReadError in internal/tenancy/staff_tenants.go; consolidate after the epic merges
// staffReadError maps only the function's own refusal to ErrNotStaff; a lost grant is also 42501 and stays a server fault.
func staffReadError(op string, err error) error {
	if err == nil {
		return nil
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "42501" && strings.HasSuffix(pgErr.Message, ": actor is not staff") {
		return db.ErrNotStaff
	}
	return fmt.Errorf("%s: %w", op, err)
}

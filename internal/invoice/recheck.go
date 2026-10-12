package invoice

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Rechecker re-checks, tenant by tenant, the invoices a rule-set version covers once its start
// date arrives, and the invoices a pending custom-rule revision covers. It lives here, not in internal/reconciliation, so that package never imports
// internal/invoice (import cycle through internal/approval's tests). Pool must be the invoice_app pool.
type Rechecker struct {
	Pool    *pgxpool.Pool
	Store   *Store
	Gate    *Gate
	Tenants func(context.Context) ([]string, error)
	Now     func() time.Time
	Logger  *slog.Logger

	// Tests replace these; nil means the package functions over Pool, Store and Gate.
	due     func(ctx context.Context, today time.Time) ([]DueVersion, error)
	recheck func(ctx context.Context, tenantID string, v DueVersion, today time.Time) (RevalidateResult, error)
	mark    func(ctx context.Context, versionID string) error

	// recheckCustom is RunCustomOnce's per-tenant pass; nil means RecheckCustomRules.
	recheckCustom func(ctx context.Context, tenantID string) (RevalidateResult, error)

	// done holds the tenants that passed an open version. Single-flight via the Sweeper, so no lock.
	// ceiling: a failing tenant re-sends its own covered invoices every tick; the Sweeper reports the streak once.
	done map[string]map[string]bool
}

// RunOnce re-checks every due version, oldest first, for every tenant not yet done, and writes a
// version's marker only when all enumerated tenants are done. One tenant's failure does not stop
// the others; the joined errors come back and the version stays due.
func (r *Rechecker) RunOnce(ctx context.Context) error {
	now := time.Now
	if r.Now != nil {
		now = r.Now
	}
	y, m, d := now().UTC().Date()
	today := time.Date(y, m, d, 0, 0, 0, 0, time.UTC)

	due := r.due
	if due == nil {
		due = func(ctx context.Context, today time.Time) ([]DueVersion, error) {
			return DueRechecks(ctx, r.Pool, today)
		}
	}
	recheck := r.recheck
	if recheck == nil {
		recheck = func(ctx context.Context, tenantID string, v DueVersion, today time.Time) (RevalidateResult, error) {
			return RecheckCovered(ctx, r.Pool, r.Store, r.Gate, tenantID, v, today)
		}
	}
	mark := r.mark
	if mark == nil {
		mark = func(ctx context.Context, versionID string) error { return MarkRechecked(ctx, r.Pool, versionID) }
	}
	logger := r.Logger
	if logger == nil {
		logger = slog.Default()
	}

	versions, err := due(ctx, today)
	if err != nil {
		return err
	}
	if r.done == nil {
		r.done = map[string]map[string]bool{}
	}
	open := make(map[string]bool, len(versions))
	for _, v := range versions {
		open[v.ID] = true
	}
	for id := range r.done {
		if !open[id] {
			delete(r.done, id)
		}
	}
	if len(versions) == 0 {
		return nil
	}

	tenants, err := r.Tenants(ctx)
	if err != nil {
		return fmt.Errorf("invoice: recheck: enumerate tenants: %w", err)
	}

	var failures []error
	for _, v := range versions {
		// A marker suppresses the re-check, so never write one for a version that has not started.
		if v.EffectiveFrom.After(today) {
			continue
		}
		if r.done[v.ID] == nil {
			r.done[v.ID] = map[string]bool{}
		}
		allDone := true
		for _, tenantID := range tenants {
			if r.done[v.ID][tenantID] {
				continue
			}
			res, err := recheck(ctx, tenantID, v, today)
			if err != nil {
				allDone = false
				logger.ErrorContext(ctx, "invoice: recheck failed", "version", v.Version, "tenant_id", tenantID, "error", err)
				failures = append(failures, fmt.Errorf("invoice: recheck version %d tenant %s: %w", v.Version, tenantID, err))
				continue
			}
			r.done[v.ID][tenantID] = true
			logger.InfoContext(ctx, "invoice: recheck done", "version", v.Version, "tenant_id", tenantID,
				"examined", res.Examined, "demoted", res.Demoted)
		}
		if !allDone {
			continue
		}
		if err := mark(ctx, v.ID); err != nil {
			failures = append(failures, fmt.Errorf("invoice: recheck version %d: %w", v.Version, err))
			continue
		}
		delete(r.done, v.ID)
	}
	return errors.Join(failures...)
}

// RunCustomOnce re-checks, for every tenant, the invoices that pending custom-rule revisions
// cover (D16). One tenant's failure does not stop the others; the joined errors come back and the
// failed tenant's revisions stay pending for the next tick.
func (r *Rechecker) RunCustomOnce(ctx context.Context) error {
	recheck := r.recheckCustom
	if recheck == nil {
		recheck = func(ctx context.Context, tenantID string) (RevalidateResult, error) {
			return RecheckCustomRules(ctx, r.Pool, r.Store, r.Gate, tenantID)
		}
	}
	logger := r.Logger
	if logger == nil {
		logger = slog.Default()
	}

	tenants, err := r.Tenants(ctx)
	if err != nil {
		return fmt.Errorf("invoice: custom recheck: enumerate tenants: %w", err)
	}
	var failures []error
	for _, tenantID := range tenants {
		res, err := recheck(ctx, tenantID)
		if err != nil {
			logger.ErrorContext(ctx, "invoice: custom recheck failed", "tenant_id", tenantID, "error", err)
			failures = append(failures, fmt.Errorf("invoice: custom recheck tenant %s: %w", tenantID, err))
			continue
		}
		if res.Pending > 0 {
			logger.InfoContext(ctx, "invoice: custom recheck done", "tenant_id", tenantID,
				"pending", res.Pending, "examined", res.Examined, "demoted", res.Demoted)
		}
	}
	return errors.Join(failures...)
}

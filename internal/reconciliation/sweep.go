// M5-06-05: the sweep orchestrator (Reconciler.SweepOnce), composing the M5-06-02..04
// primitives (Scan/ReArmPoll/recordDriftAudit/recordAutoFixAudit) into the per-sweep,
// per-tenant loop the M5-06 System Design describes — reader-pool enumeration, then one
// db.WithinTenantTx(AppPool, tenantID, ...) per tenant, heal-or-flag routing inside it.
//
// M5-06-06: Config/ConfigFromEnv, the RECONCILE_* env-driven thresholds Reconciler.Cfg
// carries and Sweeper's ticker interval (sweeper.go) reads.
package reconciliation

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/SimonOsipov/invoice-os/internal/platform/db"
	"github.com/SimonOsipov/invoice-os/internal/platform/queue"
)

// env var names + documented defaults (M5-06 story, [M5-06-06] AC-3, Decisions
// [tick-interval-default] / [poll-overdue-threshold]).
const (
	envReconcileInterval = "RECONCILE_INTERVAL"
	envPollOverdueGrace  = "RECONCILE_POLL_OVERDUE_GRACE"
	envHopCeiling        = "RECONCILE_HOP_CEILING"
	envMaxPendingAge     = "RECONCILE_MAX_PENDING_AGE"

	defaultReconcileInterval = 5 * time.Minute
	defaultPollOverdueGrace  = 15 * time.Minute
	defaultHopCeiling        = 20
	defaultMaxPendingAge     = 24 * time.Hour
)

// Config parameterizes one Reconciler: the sweep's tick Interval (read by Sweeper, not by
// SweepOnce itself) plus the three Scan thresholds — PollOverdueGrace/MaxPendingAge/
// HopCeiling map 1:1 onto Thresholds{Grace, MaxPendingAge, HopCeiling}.
type Config struct {
	Interval         time.Duration
	PollOverdueGrace time.Duration
	MaxPendingAge    time.Duration
	HopCeiling       int
}

// ConfigFromEnv reads RECONCILE_INTERVAL/RECONCILE_POLL_OVERDUE_GRACE/RECONCILE_HOP_CEILING/
// RECONCILE_MAX_PENDING_AGE, applying the documented defaults (5m/15m/20/24h) when a var is
// unset, and failing on a malformed value — mirrors submission.MockConfigFromEnv /
// RateLimitConfigFromEnv's env-edge pattern (internal/submission/mock_adapter.go:129,
// internal/submission/ratelimit.go:122). internal/platform/config.go's envInt/envDuration
// helpers are unexported to that package, so the four vars are parsed inline here rather
// than reused.
//
// Each of the four env vars is independent: unset (or empty) takes the documented default;
// present but unparseable is a fatal boot error (the zero Config, never a partially-
// populated one alongside the error).
func ConfigFromEnv() (Config, error) {
	interval, err := reconcileEnvDuration(envReconcileInterval, defaultReconcileInterval)
	if err != nil {
		return Config{}, err
	}
	grace, err := reconcileEnvDuration(envPollOverdueGrace, defaultPollOverdueGrace)
	if err != nil {
		return Config{}, err
	}
	maxAge, err := reconcileEnvDuration(envMaxPendingAge, defaultMaxPendingAge)
	if err != nil {
		return Config{}, err
	}
	ceiling, err := reconcileEnvInt(envHopCeiling, defaultHopCeiling)
	if err != nil {
		return Config{}, err
	}

	return Config{
		Interval:         interval,
		PollOverdueGrace: grace,
		MaxPendingAge:    maxAge,
		HopCeiling:       ceiling,
	}, nil
}

// reconcileEnvDuration reads key as a time.Duration, returning def when key is unset/empty
// and a wrapped error when it is present but not a valid duration.
func reconcileEnvDuration(key string, def time.Duration) (time.Duration, error) {
	raw := os.Getenv(key)
	if raw == "" {
		return def, nil
	}
	d, err := time.ParseDuration(raw)
	if err != nil {
		return 0, fmt.Errorf("reconciliation: invalid %s=%q: %w", key, raw, err)
	}
	return d, nil
}

// reconcileEnvInt reads key as an int, returning def when key is unset/empty and a wrapped
// error when it is present but not a valid integer.
func reconcileEnvInt(key string, def int) (int, error) {
	raw := os.Getenv(key)
	if raw == "" {
		return def, nil
	}
	n, err := strconv.Atoi(raw)
	if err != nil {
		return 0, fmt.Errorf("reconciliation: invalid %s=%q: %w", key, raw, err)
	}
	return n, nil
}

// thresholds projects Config's three Scan cutoffs onto Thresholds, so SweepOnce can hand
// them straight to Scan without either type knowing about the other's remaining fields
// (Interval is Sweeper's concern, not Scan's).
func (c Config) thresholds() Thresholds {
	return Thresholds{
		Grace:         c.PollOverdueGrace,
		MaxPendingAge: c.MaxPendingAge,
		HopCeiling:    c.HopCeiling,
	}
}

// Reconciler composes one sweep: ReaderPool enumerates every tenant (invoice_tenant_reader,
// DATABASE_READER_URL — the tenant_enumerate policy, migrations/20260707122459_tenants_rls.sql:40),
// AppPool + Queue run Scan/ReArmPoll/audit per tenant inside db.WithinTenantTx (invoice_app,
// DATABASE_URL) — the two-role transaction model (M5-06 System Design).
type Reconciler struct {
	ReaderPool *pgxpool.Pool // invoice_tenant_reader: SELECT id FROM tenants, no GUC set
	AppPool    *pgxpool.Pool // invoice_app: every per-tenant Scan/ReArmPoll/audit write
	Queue      *queue.Client // enqueue-only client ReArmPoll's EnqueueTx targets
	Cfg        Config
	Logger     *slog.Logger

	// afterHeal is a TEST-ONLY hook (nil in production): when set, SweepOnce invokes it
	// inside the per-tenant tx immediately after a lost_poll finding's ReArmPoll +
	// recordAutoFixAudit have BOTH already succeeded, passing the id of the tenant being
	// healed. A non-nil return propagates out of the per-tenant closure, forcing
	// db.WithinTenantTx to roll back the WHOLE tenant tx — the re-arm's river_job insert
	// AND its auto_fixed audit row both vanish together. Mirrors
	// TestExactlyOnce_OutboxThreeWayAtomicity's sentinel-after-a-real-write pattern
	// (internal/submission/failure_modes_test.go:244-266) so the three-way rollback-
	// atomicity property (M5-06-05 AC-5) is provable against a REAL ReArmPoll, not a fake
	// queue.Client — queue.Client is a concrete struct with no double, matching
	// SubmitWorker.Queue *queue.Client (internal/submission/worker.go).
	afterHeal func(tenantID string) error
}

// SweepOnce runs exactly one sweep: enumerate every tenant on ReaderPool (no GUC set —
// the tenant_enumerate policy ORs in every row), then for each tenant, inside its own
// db.WithinTenantTx(AppPool, tenantID, ...): Scan, and for every Finding either heal
// (Kind==LostPoll: ReArmPoll then recordAutoFixAudit, both in the same tx, then afterHeal
// if set) or flag (recordDriftAudit). The reconciler never writes invoice/job status
// directly (AC-3) — PollWorker applies any eventual verdict under its own lock (M5-06
// System Design).
//
// A per-tenant tx failure (Scan error, ReArmPoll error, audit-write error, or an afterHeal
// sentinel) rolls back that tenant's tx alone and is logged — it does NOT abort the sweep;
// SweepOnce moves on to the next tenant so one bad tenant can never starve every other
// tenant's reconciliation. SweepOnce itself returns nil unless tenant enumeration fails
// (there is nothing left to compose per-tenant errors into).
func (r *Reconciler) SweepOnce(ctx context.Context) error {
	tenantIDs, err := r.enumerateTenants(ctx)
	if err != nil {
		return err
	}

	th := r.Cfg.thresholds()
	for _, tenantID := range tenantIDs {
		if err := db.WithinTenantTx(ctx, r.AppPool, tenantID, func(tx pgx.Tx) error {
			return r.sweepTenant(ctx, tx, tenantID, th)
		}); err != nil && r.Logger != nil {
			r.Logger.ErrorContext(ctx, "reconciliation: tenant sweep failed",
				"tenant_id", tenantID, "error", err)
		}
	}
	return nil
}

// enumerateTenants lists every tenant id via ReaderPool (invoice_tenant_reader, no
// app.current_tenant GUC set) — the tenant_enumerate policy
// (migrations/20260707122459_tenants_rls.sql:40) ORs in every row for this role alone.
func (r *Reconciler) enumerateTenants(ctx context.Context) ([]string, error) {
	rows, err := r.ReaderPool.Query(ctx, `SELECT id FROM tenants ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("reconciliation: enumerate tenants: %w", err)
	}
	defer rows.Close()

	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("reconciliation: scan tenant id: %w", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("reconciliation: enumerate tenants rows: %w", err)
	}
	return ids, nil
}

// sweepTenant runs Scan for one tenant inside its own tx and routes every Finding: a
// healable (lost_poll) finding is re-armed then audited as auto_fixed, then afterHeal (the
// test-only rollback hook) runs if set; every other finding is audited as drift_detected.
//
// attempts for ReArmPoll is re-read from submission_jobs inside this SAME tx rather than
// carried on Finding — Finding has no Attempts field, and adding one would widen every
// Round A test's struct literal for a value only this one caller needs
// ([reconciler-rereads-attempts]).
func (r *Reconciler) sweepTenant(ctx context.Context, tx pgx.Tx, tenantID string, th Thresholds) error {
	findings, err := Scan(ctx, tx, th)
	if err != nil {
		return err
	}

	for _, f := range findings {
		if !f.Healable {
			if err := recordDriftAudit(ctx, tx, f); err != nil {
				return err
			}
			continue
		}

		var attempts int
		if err := tx.QueryRow(ctx, `SELECT attempts FROM submission_jobs WHERE id = $1`,
			*f.SubmissionJobID).Scan(&attempts); err != nil {
			return fmt.Errorf("reconciliation: read job attempts for re-arm: %w", err)
		}
		if _, err := ReArmPoll(ctx, tx, r.Queue, tenantID, f, attempts); err != nil {
			return err
		}
		if err := recordAutoFixAudit(ctx, tx, f); err != nil {
			return err
		}
		if r.afterHeal != nil {
			if err := r.afterHeal(tenantID); err != nil {
				return err
			}
		}
	}
	return nil
}

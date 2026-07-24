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
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

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
// internal/submission/ratelimit.go:122).
//
// TODO(M5-06-06): executor — parse each of the four vars (time.ParseDuration /
// strconv.Atoi), default when unset/empty, return the zero Config + a wrapped error on
// malformed input (never a partially-populated Config alongside a non-nil error).
func ConfigFromEnv() (Config, error) {
	return Config{}, nil
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
// TODO(M5-06-05): executor — enumerate tenants via ReaderPool, loop
// db.WithinTenantTx(AppPool, tenantID, ...) per tenant, Scan the tx with r.Cfg's
// thresholds, route each Finding (heal vs flag), invoke r.afterHeal right after a heal's
// ReArmPoll+recordAutoFixAudit both succeed.
func (r *Reconciler) SweepOnce(ctx context.Context) error {
	return nil
}

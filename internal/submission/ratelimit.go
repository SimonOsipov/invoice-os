// ratelimit.go: M5-04-04 (task-232) -- an in-memory, per-tenant, fixed one-minute-window
// RateLimiter (Allow), a SELECT-only reader over submission_rate_limits (RateLimitFor), and
// the env-config loader for the per-minute default (RateLimitConfigFromEnv). No worker
// wiring here -- Allow is a pure decision function that writes nothing and returns no error;
// the blocked_rate_limit evidence row, the river.JobSnooze return, and the
// attempts-not-incremented rule all belong to M5-04-05.
package submission

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
)

// submissionRateLimitEnv is the env var RateLimitConfigFromEnv reads.
const submissionRateLimitEnv = "SUBMISSION_RATE_LIMIT_PER_MINUTE"

// submissionRateLimitDefault is what RateLimitConfigFromEnv returns when
// submissionRateLimitEnv is unset.
const submissionRateLimitDefault = 60

// window is one tenant's fixed one-minute tally: n calls allowed since start.
type window struct {
	start time.Time
	n     int
}

// RateLimiter is an in-memory, per-tenant, fixed one-minute-window limiter. The zero value
// is not usable -- construct with NewRateLimiter. now is always a caller-supplied parameter
// (never time.Now() internally), so callers and tests can drive the window deterministically.
type RateLimiter struct {
	mu      sync.Mutex
	windows map[string]*window
}

// NewRateLimiter builds an empty limiter.
func NewRateLimiter() *RateLimiter {
	return &RateLimiter{windows: make(map[string]*window)}
}

// Allow reports whether tenantID may make one more call under limit in the one-minute
// window containing now, and (when denied) how long until the window rolls over.
//
// The whole read-check-increment sequence runs under a single mutex held for the entire
// call -- never a separate read then a separate write -- so two goroutines racing the same
// tenant at limit cannot both observe n < limit and both increment
// (TestRateLimiter_ConcurrentCallsAllowExactlyLimit, -race). now is always a caller-supplied
// parameter, never time.Now() here, which is what makes the window-rollover and concurrency
// tests deterministic without a wall-clock sleep.
//
// Fixed one-minute window, not a token bucket ([fixed-window-limiter] names and rejects that
// alternative as roughly twice the code for a limit M9-02 will retune anyway).
//
// Allow itself writes nothing and returns no error -- it is a pure decision function. The
// caller wired in M5-04-05 (not here) is what turns a denial into an app_exchange evidence
// row (Outcome: blocked_rate_limit, written at attempts+1 WITHOUT incrementing attempts) and
// a river.JobSnooze(retryAfter) return. Per [attempts-counts-wire-attempts] and QA finding
// F4, that makes app_exchange.attempt deliberately NOT attempt-ordinal-unique -- N
// consecutive blocks all carry the same attempts+1 value, and the first real wire attempt
// reuses it. That is schema-legal (no UNIQUE(submission_job_id, attempt)) and accepted:
// ordering is carried by occurred_at, not attempt. Do NOT add a sub-counter or sequence
// column to "repair" this in M5-04-05 -- the tradeoff was already made and rejected at the
// story level.
func (l *RateLimiter) Allow(tenantID string, limit int, now time.Time) (ok bool, retryAfter time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()

	w := l.windows[tenantID]
	if w == nil || now.Sub(w.start) >= time.Minute {
		w = &window{start: now}
		l.windows[tenantID] = w
	}

	if w.n < limit {
		w.n++
		return true, 0
	}
	return false, w.start.Add(time.Minute).Sub(now)
}

// RateLimitFor reads the configured per-minute ceiling for the tx's tenant from
// submission_rate_limits, falling back to def when no row exists (pgx.ErrNoRows) -- an
// unconfigured firm is the expected common case until M7-04 ships a self-service writer, not
// an error condition.
//
// Deliberately NO "WHERE tenant_id = $1" predicate: RateLimitFor takes no tenant parameter,
// so there is nothing to bind one to. Scoping comes from the tenant_isolation RLS policy +
// the app.current_tenant GUC already set on tx alone -- an application-SQL tenant filter here
// would make TestRLS_RateLimitForIsTenantScoped pass even with RLS disabled, i.e. vacuous.
func RateLimitFor(ctx context.Context, tx pgx.Tx, def int) (int, error) {
	var maxPerMinute int
	err := tx.QueryRow(ctx, `SELECT max_per_minute FROM submission_rate_limits`).Scan(&maxPerMinute)
	if errors.Is(err, pgx.ErrNoRows) {
		return def, nil
	}
	if err != nil {
		return 0, fmt.Errorf("submission: read submission_rate_limits: %w", err)
	}
	return maxPerMinute, nil
}

// RateLimitConfigFromEnv reads SUBMISSION_RATE_LIMIT_PER_MINUTE, mirroring
// MockConfigFromEnv's three-branch rule (mock_adapter.go:129):
//
//   - unset (os.LookupEnv reports absent) -> (submissionRateLimitDefault, nil).
//   - present but unparseable by strconv.Atoi, INCLUDING present-but-empty ("") -> error
//     naming the variable and the offending value.
//   - parses but <= 0 -> error naming the variable and the offending value. Written <= 0,
//     not < 0: the DB invariant is max_per_minute > 0, so a configured zero would silently
//     block every submission for that firm. This guard is net-new -- strconv.Atoi("-5")
//     returns (-5, nil), no parse error, and internal/platform/config.go's envInt is
//     unexported (so unreusable here) and lacks this check too.
//
// Fail-closed: only genuine absence gets the friendly default; anything present and wrong is
// a fatal boot error, never silently clamped or ignored.
func RateLimitConfigFromEnv() (int, error) {
	raw, present := os.LookupEnv(submissionRateLimitEnv)
	if !present {
		return submissionRateLimitDefault, nil
	}
	n, err := strconv.Atoi(raw)
	if err != nil {
		return 0, fmt.Errorf("submission: invalid %s=%q: %w", submissionRateLimitEnv, raw, err)
	}
	if n <= 0 {
		return 0, fmt.Errorf("submission: invalid %s=%q: must be greater than 0", submissionRateLimitEnv, raw)
	}
	return n, nil
}

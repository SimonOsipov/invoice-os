// ratelimit.go: M5-04-04 (task-232), RALPH Stage 2.5 (Mode A) stub surface. Declares
// RateLimiter/Allow, RateLimitFor and RateLimitConfigFromEnv ONLY so ratelimit_test.go and
// ratelimit_db_test.go compile and fail on their target assertions, never on a compile
// error. No production logic ships in this commit -- Allow always denies (the zero value),
// and RateLimitFor / RateLimitConfigFromEnv always return errRateLimiterNotImplemented,
// mirroring the errActorNotImplemented / errPortNotImplemented precedent
// (internal/invoice/submission_port.go, M5-04-03's RED commit dc067f1). Real bodies land in
// this subtask's Mode B (Executor, Stage 3) pass.
package submission

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
)

// errRateLimiterNotImplemented is the RED-stage stub body RateLimitFor and
// RateLimitConfigFromEnv return below. Allow has no error return (its declared signature is
// (bool, time.Duration)), so it stubs to the zero value (false, 0) instead -- there is no
// sentinel to hand back through it.
var errRateLimiterNotImplemented = errors.New("submission: rate limiter not implemented")

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
// STUB (Mode A): always denies. Body lands in this subtask's Mode B pass.
func (l *RateLimiter) Allow(tenantID string, limit int, now time.Time) (ok bool, retryAfter time.Duration) {
	return false, 0
}

// RateLimitFor reads the configured per-minute ceiling for the tx's tenant from
// submission_rate_limits, falling back to def when no row exists (pgx.ErrNoRows). Tenant
// scoping comes from RLS + the app.current_tenant GUC alone -- no explicit tenant_id
// predicate belongs in the SQL (there is no tenant parameter to bind one with).
//
// STUB (Mode A): always errors. Body lands in this subtask's Mode B pass.
func RateLimitFor(ctx context.Context, tx pgx.Tx, def int) (int, error) {
	return 0, errRateLimiterNotImplemented
}

// RateLimitConfigFromEnv reads SUBMISSION_RATE_LIMIT_PER_MINUTE: unset -> (60, nil);
// unparseable -> error naming the variable and the value; <= 0 -> error naming the variable
// and the value.
//
// STUB (Mode A): always errors. Body lands in this subtask's Mode B pass.
func RateLimitConfigFromEnv() (int, error) {
	return 0, errRateLimiterNotImplemented
}

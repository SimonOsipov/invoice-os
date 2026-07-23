// ratelimit_adversarial_test.go: QA Mode B adversarial coverage for M5-04-04 (task-232),
// beyond the nine AC-derived Test Specs (T04-1..T04-9) already GREEN in ratelimit_test.go /
// ratelimit_db_test.go. Same reuse rules as the rest of the package: package
// submission_test, requireExchangeDB / fx / exChain / rlSeedLimit / rlBaseTime / rlEnvVar
// (all package-scoped in ratelimit_test.go / ratelimit_db_test.go), no new writer function,
// no testify, no t.Skip beyond requireExchangeDB's established gate.
//
// Seven cases:
//
//  1. TestRateLimiter_ZeroAndNegativeLimitDenyWithSafeRetryAfter — limit<=0 denies every
//     call and retryAfter is never negative (river.JobSnooze panics on a negative duration —
//     a real crash risk, not cosmetic).
//  2. TestRateLimiter_ExactWindowBoundaryResetsAtPreciselyOneMinute — a call at exactly
//     windowStart+60s gets a fresh window (the shipped '>=' comparison), one at
//     windowStart+(60s-1ns) does not. QA Mode B mutation-proved this gap: T04-3 as specced
//     only exercises +61s, which is on the "reset" side of BOTH '>=' and a mutated '>', so a
//     '>' mutant survives T04-3 unnoticed; this test is the boundary T04-3 doesn't reach.
//  3. TestRateLimiter_ClockGoingBackwardsNeverPanicsOrGoesNegative — now regresses within an
//     established window (NTP correction); asserts no panic and retryAfter is never
//     negative. Logs (does not assert) that retryAfter CAN then exceed one minute under
//     backward skew — a narrower deviation from AC-2's forward-time framing, not a crash
//     risk, reported to QA rather than asserted against here.
//  4. TestRateLimiter_ManyTenantsDoNotLeakState — 200 distinct tenants at limit=1 each;
//     every one gets its own independent single allowance, none leak. (l.windows itself is
//     unexported and never evicted — worth recording as an unbounded-growth memory
//     consideration for a process-lifetime RateLimiter; out of scope to fix here.)
//  5. TestRateLimitFor_ConfiguredRowAtCheckBoundary — max_per_minute=1, the CHECK's minimum
//     legal value, round-trips.
//  6. TestRateLimitFor_NoTenantGUCFailsClosedToDefault — a tx with NO app.current_tenant GUC
//     set at all (bypassing db.WithinTenantTx) sees zero rows under RLS and falls back to
//     def, even with a real row seeded for a real tenant — never that tenant's row.
//  7. TestRateLimitConfigFromEnv_AdversarialEdgeValues — whitespace (" 60 "), a value past
//     int range, and a numeric-prefixed non-numeric string ("60abc") are all fatal-shaped,
//     each naming the variable and the offending value.
package submission_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/SimonOsipov/invoice-os/internal/platform/db"
	"github.com/SimonOsipov/invoice-os/internal/submission"
)

// TestRateLimiter_ZeroAndNegativeLimitDenyWithSafeRetryAfter: limit=0 and negative limits
// must deny every call, and retryAfter must never be negative -- river.JobSnooze panics on a
// negative duration, so a wrong sign here is a crash risk for the caller wired in M5-04-05,
// not a cosmetic mismatch.
func TestRateLimiter_ZeroAndNegativeLimitDenyWithSafeRetryAfter(t *testing.T) {
	for _, limit := range []int{0, -1, -100} {
		t.Run(fmt.Sprintf("limit=%d", limit), func(t *testing.T) {
			l := submission.NewRateLimiter()
			tenant := uuid.NewString()

			ok, retryAfter := l.Allow(tenant, limit, rlBaseTime)
			if ok {
				t.Fatalf("Allow with limit=%d: ok = true, want false (every call must be denied)", limit)
			}
			if retryAfter < 0 {
				t.Fatalf("Allow with limit=%d: retryAfter = %v, want >= 0 -- river.JobSnooze panics "+
					"on a negative duration", limit, retryAfter)
			}
			if retryAfter == 0 {
				t.Errorf("Allow with limit=%d (denied): retryAfter = 0, want > 0 -- a zero retryAfter "+
					"on a denial is indistinguishable from an allowed call to a caller checking ok alone",
					limit)
			}
			if retryAfter > time.Minute {
				t.Errorf("Allow with limit=%d (denied): retryAfter = %v, want <= time.Minute", limit, retryAfter)
			}

			// A second call must also be denied -- limit<=0 is not a one-shot fluke.
			ok2, retryAfter2 := l.Allow(tenant, limit, rlBaseTime)
			if ok2 {
				t.Errorf("second Allow with limit=%d: ok = true, want false", limit)
			}
			if retryAfter2 < 0 {
				t.Errorf("second Allow with limit=%d: retryAfter = %v, want >= 0", limit, retryAfter2)
			}
		})
	}
}

// TestRateLimiter_ExactWindowBoundaryResetsAtPreciselyOneMinute: proves the shipped '>='
// comparison at the exact window boundary, not merely "sometime after 60s" (T04-3 tests at
// +61s, a duration that satisfies both '>=' and a mutated '>' and so cannot distinguish
// them -- QA Mode B mutation testing confirmed a '>' mutant survives T04-3 unnoticed).
func TestRateLimiter_ExactWindowBoundaryResetsAtPreciselyOneMinute(t *testing.T) {
	const limit = 1
	l := submission.NewRateLimiter()
	tenant := uuid.NewString()

	if ok, _ := l.Allow(tenant, limit, rlBaseTime); !ok {
		t.Fatalf("call 1 at window start: ok = false, want true")
	}
	if ok, _ := l.Allow(tenant, limit, rlBaseTime); ok {
		t.Fatalf("call 2 at window start (over limit=1): ok = true, want false")
	}

	// One nanosecond BEFORE the boundary: still the same window, still exhausted.
	justBefore := rlBaseTime.Add(time.Minute - time.Nanosecond)
	if ok, _ := l.Allow(tenant, limit, justBefore); ok {
		t.Errorf("call at windowStart+(60s-1ns): ok = true, want false -- the window must not " +
			"roll over one nanosecond early")
	}

	// AT the boundary: now.Sub(start) == time.Minute, so '>=' requires a reset.
	atBoundary := rlBaseTime.Add(time.Minute)
	ok, retryAfter := l.Allow(tenant, limit, atBoundary)
	if !ok {
		t.Fatalf("call at windowStart+60s exactly: ok = false, want true -- the algorithm's " +
			"own '>=' comparison requires the window to reset AT the boundary, not strictly after it")
	}
	if retryAfter != 0 {
		t.Errorf("call at windowStart+60s exactly (allowed): retryAfter = %v, want 0", retryAfter)
	}
}

// TestRateLimiter_ClockGoingBackwardsNeverPanicsOrGoesNegative: an NTP correction or clock
// skew can move `now` backward relative to a window already established from an earlier,
// later `now`. Must never panic and must never return a negative retryAfter (the
// river.JobSnooze crash risk); a retryAfter exceeding one minute under this adversarial
// input is logged, not asserted against -- see the file header.
func TestRateLimiter_ClockGoingBackwardsNeverPanicsOrGoesNegative(t *testing.T) {
	const limit = 1
	l := submission.NewRateLimiter()
	tenant := uuid.NewString()

	if ok, _ := l.Allow(tenant, limit, rlBaseTime); !ok {
		t.Fatalf("call 1: ok = false, want true")
	}

	// The clock jumps BACKWARD 10s relative to the window's own start.
	earlier := rlBaseTime.Add(-10 * time.Second)

	var ok bool
	var retryAfter time.Duration
	func() {
		defer func() {
			if r := recover(); r != nil {
				t.Fatalf("Allow with a backward-moving clock panicked: %v", r)
			}
		}()
		ok, retryAfter = l.Allow(tenant, limit, earlier)
	}()

	if ok {
		t.Fatalf("Allow with a backward clock (tenant already at limit=1): ok = true, want false")
	}
	if retryAfter < 0 {
		t.Fatalf("Allow with a backward clock: retryAfter = %v, want >= 0 -- river.JobSnooze "+
			"panics on a negative duration", retryAfter)
	}
	t.Logf("retryAfter under 10s backward clock skew = %v (informational; AC-2's <=1min bound "+
		"assumes forward-moving time -- the shipped window.start.Add(time.Minute).Sub(now) "+
		"formula can exceed one minute when now has regressed past window.start)", retryAfter)
}

// TestRateLimiter_ManyTenantsDoNotLeakState: 200 distinct tenants, each limit=1 -- every one
// gets its own independent allowance and none leak into another's tally.
func TestRateLimiter_ManyTenantsDoNotLeakState(t *testing.T) {
	const tenantCount = 200
	l := submission.NewRateLimiter()

	tenants := make([]string, tenantCount)
	for i := range tenants {
		tenants[i] = uuid.NewString()
	}

	for i, tenant := range tenants {
		if ok, _ := l.Allow(tenant, 1, rlBaseTime); !ok {
			t.Fatalf("tenant %d/%d: first call denied, want allowed", i, tenantCount)
		}
	}
	for i, tenant := range tenants {
		if ok, _ := l.Allow(tenant, 1, rlBaseTime); ok {
			t.Errorf("tenant %d/%d: second call allowed, want denied (limit=1 already spent)", i, tenantCount)
		}
	}

	// Not asserted -- l.windows is unexported and this is a black-box test: the map never
	// evicts an entry once created, so a RateLimiter living as long as the process (the real
	// deployment shape) accumulates one window per DISTINCT tenant ever seen, unbounded, for
	// the process lifetime. Worth recording as a memory consideration; out of scope here.
}

// TestRateLimitFor_ConfiguredRowAtCheckBoundary: max_per_minute=1 is the CHECK's minimum
// legal value (CHECK (max_per_minute > 0)) -- must round-trip like any other value.
func TestRateLimitFor_ConfiguredRowAtCheckBoundary(t *testing.T) {
	f := requireExchangeDB(t)
	ctx := context.Background()
	tenantID, _, _, cleanup := exChain(t, f)
	defer cleanup()

	rlSeedLimit(ctx, t, f, tenantID, 1)

	var got int
	err := db.WithinTenantTx(ctx, f.app, tenantID, func(tx pgx.Tx) error {
		var e error
		got, e = submission.RateLimitFor(ctx, tx, 60)
		return e
	})
	if err != nil {
		t.Fatalf("RateLimitFor with a seeded max_per_minute=1 row: %v", err)
	}
	if got != 1 {
		t.Errorf("RateLimitFor with max_per_minute=1 seeded = %d, want 1", got)
	}
}

// TestRateLimitFor_NoTenantGUCFailsClosedToDefault: a plain tx opened directly on the app
// pool (bypassing db.WithinTenantTx entirely) never sets app.current_tenant. The
// tenant_isolation policy's own migration comment states "an unset GUC -> NULL -> no rows"
// -- this proves that empirically, with a REAL row seeded for a REAL tenant sitting in the
// table, rather than merely trusting the comment.
func TestRateLimitFor_NoTenantGUCFailsClosedToDefault(t *testing.T) {
	f := requireExchangeDB(t)
	ctx := context.Background()
	tenantID, _, _, cleanup := exChain(t, f)
	defer cleanup()

	rlSeedLimit(ctx, t, f, tenantID, 5)

	tx, err := f.app.Begin(ctx)
	if err != nil {
		t.Fatalf("begin tx with no tenant GUC: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	got, err := submission.RateLimitFor(ctx, tx, 60)
	if errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("RateLimitFor with no tenant GUC set returned pgx.ErrNoRows directly, want it "+
			"folded into the (def, nil) fallback like any other no-visible-row case: %v", err)
	}
	if err != nil {
		t.Fatalf("RateLimitFor with no tenant GUC set: %v", err)
	}
	if got != 60 {
		t.Errorf("RateLimitFor with no tenant GUC set = %d, want 60 (the default) -- a missing "+
			"tenant context must fail CLOSED (see zero rows, fall back to def), never surface "+
			"another tenant's configured row", got)
	}
}

// TestRateLimitConfigFromEnv_AdversarialEdgeValues: values strconv.Atoi rejects for reasons
// other than the four RED-spec cases (T04-9) -- surrounding whitespace, a magnitude beyond
// int range, and a numeric-prefixed non-numeric string -- must still be fatal-shaped.
func TestRateLimitConfigFromEnv_AdversarialEdgeValues(t *testing.T) {
	cases := []struct {
		name  string
		value string
	}{
		{"surrounded by whitespace", " 60 "},
		{"beyond int range", "99999999999999999999999999"},
		{"numeric-prefixed non-numeric", "60abc"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(rlEnvVar, tc.value)

			got, err := submission.RateLimitConfigFromEnv()
			if err == nil {
				t.Fatalf("RateLimitConfigFromEnv() with %s=%q = (%d, nil), want an error", rlEnvVar, tc.value, got)
			}
			if !strings.Contains(err.Error(), rlEnvVar) {
				t.Errorf("RateLimitConfigFromEnv() with %s=%q error = %q, want it to name the variable %s",
					rlEnvVar, tc.value, err.Error(), rlEnvVar)
			}
			if !strings.Contains(err.Error(), fmt.Sprintf("%q", tc.value)) {
				t.Errorf("RateLimitConfigFromEnv() with %s=%q error = %q, want it to name the offending value %q",
					rlEnvVar, tc.value, err.Error(), tc.value)
			}
		})
	}
}

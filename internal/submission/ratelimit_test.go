// ratelimit_test.go: the UNIT half of M5-04-04's (task-232) spec, transcribed verbatim from
// the Test Specs table. Authored BEFORE internal/submission/ratelimit.go's real bodies exist
// (RALPH Stage 2.5, Mode A) -- the stub (ratelimit.go) makes this compile and fail on each
// case's target assertion, never on a compile error.
//
// Spec-to-test map (task-232's Test Specs table):
//
//	T04-1 TestRateLimiter_AllowsUpToLimitThenDenies
//	T04-2 TestRateLimiter_TenantsAreIndependent
//	T04-3 TestRateLimiter_WindowRollsOverAndRestartsAtOne
//	T04-4 TestRateLimiter_RetryAfterIsUsableAsASnooze
//	T04-5 TestRateLimiter_ConcurrentCallsAllowExactlyLimit
//	T04-9 TestRateLimitConfigFromEnv
//
// T04-6/7/8 are DB-backed and live in ratelimit_db_test.go.
package submission_test

import (
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/SimonOsipov/invoice-os/internal/submission"
)

// rlBaseTime is a fixed reference instant every RateLimiter unit test seeds its windows
// from -- deterministic, no wall-clock dependency ([fixed-window-limiter]'s now-as-parameter
// requirement is what makes this possible without a real sleep).
var rlBaseTime = time.Date(2026, 7, 23, 12, 0, 0, 0, time.UTC)

// T04-1: with limit=N, calls 1..N return ok and call N+1 returns !ok, independently for a
// few limits (table-driven per the spec).
func TestRateLimiter_AllowsUpToLimitThenDenies(t *testing.T) {
	for _, limit := range []int{1, 3, 5} {
		t.Run(fmt.Sprintf("limit=%d", limit), func(t *testing.T) {
			l := submission.NewRateLimiter()
			tenant := uuid.NewString()

			for i := 1; i <= limit; i++ {
				if ok, _ := l.Allow(tenant, limit, rlBaseTime); !ok {
					t.Fatalf("call %d of %d: ok = false, want true", i, limit)
				}
			}
			if ok, _ := l.Allow(tenant, limit, rlBaseTime); ok {
				t.Errorf("call %d of %d (over the limit): ok = true, want false", limit+1, limit)
			}
		})
	}
}

// T04-2: tenant A exhausting limit=1 must not affect tenant B's tally.
func TestRateLimiter_TenantsAreIndependent(t *testing.T) {
	l := submission.NewRateLimiter()
	tenantA, tenantB := uuid.NewString(), uuid.NewString()

	if ok, _ := l.Allow(tenantA, 1, rlBaseTime); !ok {
		t.Fatalf("tenant A call 1: ok = false, want true")
	}
	if ok, _ := l.Allow(tenantA, 1, rlBaseTime); ok {
		t.Fatalf("tenant A call 2 (over its limit=1): ok = true, want false")
	}
	if ok, _ := l.Allow(tenantB, 1, rlBaseTime); !ok {
		t.Errorf("tenant B call 1: ok = false, want true -- tenant A's exhaustion must not leak across tenants")
	}
}

// T04-3: after now+61s the exhausted tenant is allowed again, and the counter restarts at 1
// -- proven with limit=2 so "restarted at 1" (a fresh two-call quota) is distinguishable from
// both "still exhausted" and "reset to unlimited".
func TestRateLimiter_WindowRollsOverAndRestartsAtOne(t *testing.T) {
	const limit = 2
	l := submission.NewRateLimiter()
	tenant := uuid.NewString()

	for i := 1; i <= limit; i++ {
		if ok, _ := l.Allow(tenant, limit, rlBaseTime); !ok {
			t.Fatalf("call %d of %d before rollover: ok = false, want true", i, limit)
		}
	}
	if ok, _ := l.Allow(tenant, limit, rlBaseTime); ok {
		t.Fatalf("call %d of %d before rollover (over the limit): ok = true, want false", limit+1, limit)
	}

	later := rlBaseTime.Add(61 * time.Second)

	first, retryAfter := l.Allow(tenant, limit, later)
	if !first {
		t.Fatalf("first call after 61s rollover: ok = false, want true")
	}
	if retryAfter != 0 {
		t.Errorf("first call after rollover: retryAfter = %v, want 0", retryAfter)
	}
	if second, _ := l.Allow(tenant, limit, later); !second {
		t.Errorf("second call in the new window: ok = false, want true -- the counter must have "+
			"restarted at 1 (fresh quota of limit=%d), not stayed exhausted", limit)
	}
	if third, _ := l.Allow(tenant, limit, later); third {
		t.Errorf("third call in the new window (over limit=%d again): ok = true, want false", limit)
	}
}

// T04-4: a denied call reports 0 < retryAfter <= time.Minute; an allowed call reports
// retryAfter == 0.
func TestRateLimiter_RetryAfterIsUsableAsASnooze(t *testing.T) {
	l := submission.NewRateLimiter()
	tenant := uuid.NewString()

	ok, retryAfter := l.Allow(tenant, 1, rlBaseTime)
	if !ok {
		t.Fatalf("call 1: ok = false, want true")
	}
	if retryAfter != 0 {
		t.Errorf("allowed call: retryAfter = %v, want 0", retryAfter)
	}

	ok, retryAfter = l.Allow(tenant, 1, rlBaseTime)
	if ok {
		t.Fatalf("call 2 (over limit=1): ok = true, want false")
	}
	if retryAfter <= 0 || retryAfter > time.Minute {
		t.Errorf("denied call: retryAfter = %v, want (0, time.Minute]", retryAfter)
	}
}

// T04-5: 50 concurrent callers, limit=10 -- exactly 10 return ok. CI does NOT run -race for
// this package (checked ci.yml, Makefile and every shell/yml under .github: zero hits), so
// this count is the real oracle and must stand on its own under a plain `go test`; -race is a
// local-only bonus that would additionally catch an unguarded read-check-increment sequence,
// not what makes this test meaningful in CI.
func TestRateLimiter_ConcurrentCallsAllowExactlyLimit(t *testing.T) {
	const goroutines = 50
	const limit = 10

	l := submission.NewRateLimiter()
	tenant := uuid.NewString()

	var wg sync.WaitGroup
	start := make(chan struct{})
	results := make([]bool, goroutines)
	for i := range results {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start // released together to maximise contention
			ok, _ := l.Allow(tenant, limit, rlBaseTime)
			results[i] = ok
		}(i)
	}
	close(start)
	wg.Wait()

	got := 0
	for _, ok := range results {
		if ok {
			got++
		}
	}
	if got != limit {
		t.Errorf("concurrent allowed calls = %d (of %d callers), want exactly %d", got, goroutines, limit)
	}
}

// rlEnvVar is the env var RateLimitConfigFromEnv reads.
const rlEnvVar = "SUBMISSION_RATE_LIMIT_PER_MINUTE"

// T04-9: env parsing is fail-closed on "abc", ""-but-set, "0" and "-5" (each an error naming
// the variable and the value), and returns 60 with no error when unset -- the AC-4 orphan
// Stage 1 added, since T04-7 exercises RateLimitFor's own `def` fallback on a missing DB row,
// a different mechanism from RateLimitConfigFromEnv's unset branch.
func TestRateLimitConfigFromEnv(t *testing.T) {
	t.Run("unset", func(t *testing.T) {
		// t.Setenv first so its cleanup restores whatever the runner had, then Unsetenv --
		// t.Setenv(k, "") would test EMPTY, a different string reaching a different branch,
		// leaving genuine absence unproven. Precedent: mock_adapter_test.go's
		// TestMockConfigFromEnv "unset" subtest.
		t.Setenv(rlEnvVar, "sentinel")
		os.Unsetenv(rlEnvVar)
		if got := os.Getenv(rlEnvVar); got != "" {
			t.Fatalf("test setup: %s = %q after Unsetenv, want absent", rlEnvVar, got)
		}

		got, err := submission.RateLimitConfigFromEnv()
		if err != nil {
			t.Fatalf("RateLimitConfigFromEnv() with %s unset returned unexpected error: %v", rlEnvVar, err)
		}
		if got != 60 {
			t.Errorf("RateLimitConfigFromEnv() with %s unset = %d, want 60 (the documented default)", rlEnvVar, got)
		}
	})

	fatalCases := []struct {
		name  string
		value string
	}{
		{"unparseable", "abc"},
		{"present but empty", ""},
		{"zero", "0"},
		{"negative", "-5"},
	}
	for _, tc := range fatalCases {
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

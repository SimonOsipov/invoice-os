package gateway

import (
	"log/slog"
	"strings"
	"sync"
	"time"
)

const (
	SignInMaxFailures = 10
	SignInWindow      = 15 * time.Minute
	SignInMaxKeys     = 100_000
)

const signInSweepEvery = time.Minute

// SignInThrottle caps sign-in attempts per email address.
// ceiling: 10 wrong attempts per window lock a victim out indefinitely, even with the right password; revisit with a per-client-IP limit.
// ceiling: in-process counts; a restart clears them and replicas do not share them.
type SignInThrottle struct {
	mu        sync.Mutex
	max       int
	maxKeys   int
	window    time.Duration
	now       func() time.Time
	counts    map[string]signInCount
	lastSweep time.Time
	lastWarn  time.Time
	sweeps    int
}

type signInCount struct {
	n     int
	start time.Time // first counted attempt of the window
}

func NewSignInThrottle(max, maxKeys int, window time.Duration, now func() time.Time) *SignInThrottle {
	return &SignInThrottle{max: max, maxKeys: maxKeys, window: window, now: now, counts: make(map[string]signInCount)}
}

func signInKey(email string) string { return strings.ToLower(strings.TrimSpace(email)) }

// Reserve counts one attempt for email; false means refuse before GoTrue.
func (t *SignInThrottle) Reserve(email string) bool {
	key := signInKey(email)
	t.mu.Lock()
	defer t.mu.Unlock()
	now := t.now()

	if now.Sub(t.lastSweep) >= signInSweepEvery {
		t.sweep(now)
	}
	c, held := t.counts[key]
	if !held && len(t.counts) >= t.maxKeys {
		t.sweep(now)
		// ceiling: fails closed at maxKeys (~30 MB); a flood of new addresses blocks new sign-ins until keys expire.
		if len(t.counts) >= t.maxKeys {
			if now.Sub(t.lastWarn) >= time.Minute {
				t.lastWarn = now
				slog.Default().Warn("sign-in throttle full; refusing new addresses", slog.Int("keys", len(t.counts)))
			}
			return false
		}
	}
	if !held || !now.Before(c.start.Add(t.window)) {
		c = signInCount{start: now}
	}
	if c.n >= t.max {
		return false
	}
	c.n++
	t.counts[key] = c
	return true
}

// sweep drops expired keys; the caller holds t.mu.
func (t *SignInThrottle) sweep(now time.Time) {
	t.sweeps++
	t.lastSweep = now
	for k, c := range t.counts {
		if !now.Before(c.start.Add(t.window)) {
			delete(t.counts, k)
		}
	}
}

// Refund returns one reservation for an outcome that did not test the password.
func (t *SignInThrottle) Refund(email string) {
	key := signInKey(email)
	t.mu.Lock()
	defer t.mu.Unlock()
	if c, ok := t.counts[key]; ok && c.n > 0 {
		c.n--
		t.counts[key] = c
	}
}

// Reset clears email's count after a successful sign-in.
func (t *SignInThrottle) Reset(email string) {
	key := signInKey(email)
	t.mu.Lock()
	defer t.mu.Unlock()
	delete(t.counts, key)
}

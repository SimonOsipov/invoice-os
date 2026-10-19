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
	name      string // names the instance in the full-map WARN
	mu        sync.Mutex
	max       int
	maxKeys   int
	window    time.Duration
	now       func() time.Time
	counts    map[string]signInCount
	lastSweep time.Time
	// nextExpiry never exceeds the earliest live expiry: later keys expire after every survivor.
	nextExpiry time.Time
	lastWarn   time.Time
	sweeps     int
}

type signInCount struct {
	n     int
	start time.Time // first counted attempt of the window
}

func NewSignInThrottle(name string, max, maxKeys int, window time.Duration, now func() time.Time) *SignInThrottle {
	return &SignInThrottle{name: name, max: max, maxKeys: maxKeys, window: window, now: now, counts: make(map[string]signInCount)}
}

func signInKey(email string) string { return strings.ToLower(strings.TrimSpace(email)) }

// Reserve counts one attempt for email; false means refuse before GoTrue.
func (t *SignInThrottle) Reserve(email string) bool { return t.reserve(signInKey(email), false) }

// ReserveOrAdmit counts one request for key as given. A key not held is admitted uncounted when the map is full.
func (t *SignInThrottle) ReserveOrAdmit(key string) bool { return t.reserve(key, true) }

func (t *SignInThrottle) reserve(key string, admitWhenFull bool) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	now := t.now()

	if now.Sub(t.lastSweep) >= signInSweepEvery {
		t.sweep(now)
	}
	c, held := t.counts[key]
	if !held && len(t.counts) >= t.maxKeys {
		if !now.Before(t.nextExpiry) {
			t.sweep(now)
		}
		// ceiling: at maxKeys (~30 MB) Reserve refuses new keys and ReserveOrAdmit admits them uncounted, until keys expire.
		if len(t.counts) >= t.maxKeys {
			verb := "refusing new addresses"
			if admitWhenFull {
				verb = "admitting new keys"
			}
			if now.Sub(t.lastWarn) >= time.Minute {
				t.lastWarn = now
				slog.Default().Warn(t.name+" throttle full; "+verb, slog.Int("keys", len(t.counts)))
			}
			return admitWhenFull
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
	t.nextExpiry = time.Time{}
	for k, c := range t.counts {
		exp := c.start.Add(t.window)
		if !now.Before(exp) {
			delete(t.counts, k)
		} else if t.nextExpiry.IsZero() || exp.Before(t.nextExpiry) {
			t.nextExpiry = exp
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

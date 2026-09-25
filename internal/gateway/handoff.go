package gateway

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"sync"
	"time"
)

// HandoffTTL is how long an exchange code stays redeemable.
const HandoffTTL = 60 * time.Second

// HandoffMaxLive caps live codes; a full store refuses Put.
const HandoffMaxLive = 10_000

// HandoffStore holds single-use exchange codes, each bound to a sign-in state.
// ceiling: in-process, so a restart drops live codes and replicas do not share them; move to a shared store before scaling out.
type HandoffStore struct {
	mu      sync.Mutex
	ttl     time.Duration
	now     func() time.Time
	entries map[[32]byte]handoffEntry // keyed by sha256(code); the plaintext code is never stored
	// nextExpiry is never later than the earliest live expiry, so a Put before it has nothing to sweep.
	nextExpiry time.Time
	sweeps     int
}

type handoffEntry struct {
	accessToken string
	stateHash   [32]byte
	expiresAt   time.Time
}

func NewHandoffStore(ttl time.Duration, now func() time.Time) *HandoffStore {
	return &HandoffStore{ttl: ttl, now: now, entries: make(map[[32]byte]handoffEntry)}
}

// Put stores accessToken and returns a fresh exchange code; false means the store is full.
func (s *HandoffStore) Put(accessToken string, stateHash [32]byte) (string, bool) {
	b := make([]byte, 32)
	rand.Read(b) // never fails on Go 1.24+
	code := base64.RawURLEncoding.EncodeToString(b)

	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	if !now.Before(s.nextExpiry) {
		s.sweep(now)
	}
	if len(s.entries) >= HandoffMaxLive {
		return "", false
	}
	exp := now.Add(s.ttl)
	if len(s.entries) == 0 || exp.Before(s.nextExpiry) {
		s.nextExpiry = exp
	}
	s.entries[sha256.Sum256([]byte(code))] = handoffEntry{accessToken, stateHash, exp}
	return code, true
}

// sweep drops expired entries and resets nextExpiry; the caller holds s.mu.
// ceiling: O(n) scan on a Put after the earliest code expired, n <= HandoffMaxLive; bucket by expiry if it shows in profiles.
func (s *HandoffStore) sweep(now time.Time) {
	s.sweeps++
	s.nextExpiry = time.Time{}
	for k, e := range s.entries {
		if !now.Before(e.expiresAt) {
			delete(s.entries, k)
		} else if s.nextExpiry.IsZero() || e.expiresAt.Before(s.nextExpiry) {
			s.nextExpiry = e.expiresAt
		}
	}
}

// Take redeems code once; a wrong state or expired code still spends it.
func (s *HandoffStore) Take(code, state string) (string, bool) {
	key := sha256.Sum256([]byte(code))
	s.mu.Lock()
	e, found := s.entries[key]
	delete(s.entries, key)
	now := s.now()
	s.mu.Unlock()

	if !found || !now.Before(e.expiresAt) {
		return "", false
	}
	sh := sha256.Sum256([]byte(state))
	if subtle.ConstantTimeCompare(sh[:], e.stateHash[:]) != 1 {
		return "", false
	}
	return e.accessToken, true
}

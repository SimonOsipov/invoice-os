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

// HandoffStore holds single-use exchange codes, each bound to a sign-in state.
// ceiling: in-process, so a restart drops live codes and replicas do not share them; move to a shared store before scaling out.
type HandoffStore struct {
	mu      sync.Mutex
	ttl     time.Duration
	now     func() time.Time
	entries map[[32]byte]handoffEntry // keyed by sha256(code); the plaintext code is never stored
}

type handoffEntry struct {
	accessToken string
	stateHash   [32]byte
	expiresAt   time.Time
}

func NewHandoffStore(ttl time.Duration, now func() time.Time) *HandoffStore {
	return &HandoffStore{ttl: ttl, now: now, entries: make(map[[32]byte]handoffEntry)}
}

// Put stores accessToken and returns a fresh exchange code.
func (s *HandoffStore) Put(accessToken string, stateHash [32]byte) string {
	b := make([]byte, 32)
	rand.Read(b) // never fails on Go 1.24+
	code := base64.RawURLEncoding.EncodeToString(b)

	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	// ceiling: O(n) sweep on every Put; amortise it if live codes reach ~10k.
	for k, e := range s.entries {
		if !now.Before(e.expiresAt) {
			delete(s.entries, k)
		}
	}
	s.entries[sha256.Sum256([]byte(code))] = handoffEntry{accessToken, stateHash, now.Add(s.ttl)}
	return code
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

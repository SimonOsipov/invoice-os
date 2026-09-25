package gateway

import "time"

// HandoffTTL is how long an exchange code stays redeemable.
const HandoffTTL = 60 * time.Second

// HandoffStore holds single-use exchange codes, each bound to a sign-in state.
type HandoffStore struct{}

func NewHandoffStore(ttl time.Duration, now func() time.Time) *HandoffStore {
	return &HandoffStore{}
}

// Put stores accessToken and returns a fresh exchange code.
func (s *HandoffStore) Put(accessToken string, stateHash [32]byte) string {
	return ""
}

// Take redeems code once; a wrong state or expired code still spends it.
func (s *HandoffStore) Take(code, state string) (string, bool) {
	return "", false
}

package gateway

import (
	"sync"
	"time"
)

const (
	SignInMaxFailures = 10
	SignInWindow      = 15 * time.Minute
	SignInMaxKeys     = 100_000
)

// SignInThrottle caps sign-in attempts per email address.
type SignInThrottle struct {
	mu     sync.Mutex
	sweeps int
}

func NewSignInThrottle(max, maxKeys int, window time.Duration, now func() time.Time) *SignInThrottle {
	return &SignInThrottle{}
}

// Reserve counts one attempt for email; false means refuse before GoTrue.
func (t *SignInThrottle) Reserve(email string) bool { return false }

// Refund returns one reservation for an outcome that did not test the password.
func (t *SignInThrottle) Refund(email string) {}

// Reset clears email's count after a successful sign-in.
func (t *SignInThrottle) Reset(email string) {}

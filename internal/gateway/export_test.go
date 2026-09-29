package gateway

import "github.com/SimonOsipov/invoice-os/internal/platform"

// BuildSHAForTest exposes the compiled-in sha to this package's tests without
// re-importing platform in every file.
func BuildSHAForTest() string { return platform.BuildSHA }

// SweepsForTest reports how many expiry sweeps the throttle has run.
func (t *SignInThrottle) SweepsForTest() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.sweeps
}

// SweepsForTest reports how many expiry sweeps the store has run.
func (s *HandoffStore) SweepsForTest() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.sweeps
}

// SetMaxEntriesForTest lowers the cache cap so a test can fill it.
func (c *SessionChecker) SetMaxEntriesForTest(n int) { c.maxEntries = n }

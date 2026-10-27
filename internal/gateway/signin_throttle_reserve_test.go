package gateway

// Reserve is a test helper: production callers use Admit.
func (t *SignInThrottle) Reserve(email string) bool {
	ok, err := t.Admit(email)
	return ok && err == nil
}

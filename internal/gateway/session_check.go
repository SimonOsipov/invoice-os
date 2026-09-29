package gateway

import (
	"log/slog"
	"net/http"
	"net/url"
	"time"
)

const (
	SessionCheckTTL        = 30 * time.Second
	SessionCheckMaxEntries = 100_000
	SessionCheckTimeout    = 5 * time.Second
)

// SessionChecker refuses a verified token whose GoTrue session is gone.
type SessionChecker struct {
	authURL    *url.URL
	client     *http.Client
	now        func() time.Time
	log        *slog.Logger
	maxEntries int
}

// NewSessionChecker builds a checker against GoTrue at authURL; a nil authURL answers 503 to every checked request.
func NewSessionChecker(authURL *url.URL, client *http.Client, now func() time.Time, log *slog.Logger) *SessionChecker {
	return &SessionChecker{authURL: authURL, client: client, now: now, log: log, maxEntries: SessionCheckMaxEntries}
}

// Middleware runs after the verifier. Stub: passes every request through.
func (c *SessionChecker) Middleware(next http.Handler) http.Handler { return next }

// EvictSubject drops every cached verdict for sub. Stub: does nothing.
func (c *SessionChecker) EvictSubject(sub string) {}

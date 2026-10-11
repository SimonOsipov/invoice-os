package gateway

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"sync"
	"time"

	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
)

const (
	SessionCheckTTL        = 30 * time.Second
	SessionCheckMaxEntries = 100_000
	SessionCheckTimeout    = 5 * time.Second

	maxSessionCheckBody = 1 << 10
	maxSessionUserBody  = 16 << 10
)

// goneCodes are the GoTrue error_codes that mean the session is gone.
var goneCodes = map[string]bool{
	"session_not_found": true,
	"user_not_found":    true,
	"user_banned":       true,
	"session_expired":   true,
}

type verdict int

const (
	verdictUnavailable verdict = iota
	verdictLive
	verdictRevoked
)

type sessionEntry struct {
	subject        string
	live           bool
	confirmedEmail string
	checkedAt      time.Time
}

type confirmedEmailKey struct{}

// confirmedEmailFrom is the email GoTrue confirmed for this session, or "".
func confirmedEmailFrom(ctx context.Context) string {
	s, _ := ctx.Value(confirmedEmailKey{}).(string)
	return s
}

// sessionCall is one shared GoTrue /user call; evicted stops it from caching its verdict.
type sessionCall struct {
	subject        string
	done           chan struct{}
	verdict        verdict
	confirmedEmail string
	evicted        bool
}

// SessionChecker refuses a verified token whose GoTrue session is gone.
type SessionChecker struct {
	userURL    string
	client     *http.Client
	now        func() time.Time
	log        *slog.Logger
	maxEntries int

	mu       sync.Mutex
	entries  map[string]sessionEntry // keyed by session_id
	inflight map[string]*sessionCall // keyed by session_id
}

// NewSessionChecker builds a checker against GoTrue at authURL; a nil authURL answers 503 to every checked request.
// It uses a copy of client that never follows a redirect: a followed 3xx could turn a refusal into a 200.
func NewSessionChecker(authURL *url.URL, client *http.Client, now func() time.Time, log *slog.Logger) *SessionChecker {
	var noRedirect http.Client
	if client != nil {
		noRedirect = *client
	}
	noRedirect.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	c := &SessionChecker{
		client:     &noRedirect,
		now:        now,
		log:        log,
		maxEntries: SessionCheckMaxEntries,
		entries:    map[string]sessionEntry{},
		inflight:   map[string]*sessionCall{},
	}
	if authURL != nil {
		c.userURL = authURL.JoinPath("user").String()
	}
	return c
}

// Middleware runs after the verifier; a token without session_id is not checked.
func (c *SessionChecker) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, _ := auth.IdentityFromContext(r.Context())
		if id.SessionID == "" {
			next.ServeHTTP(w, r)
			return
		}
		v, email := c.check(r, id)
		switch v {
		case verdictLive:
			next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), confirmedEmailKey{}, email)))
		case verdictRevoked:
			// The verifier's own refusal bytes, so a revoked session looks like any bad token.
			w.Header().Set("WWW-Authenticate", "Bearer")
			writeError(w, http.StatusUnauthorized, "unauthorized")
		default:
			writeError(w, http.StatusServiceUnavailable, "session check unavailable")
		}
	})
}

func (c *SessionChecker) check(r *http.Request, id auth.Identity) (verdict, string) {
	if c.userURL == "" {
		return verdictUnavailable, ""
	}
	c.mu.Lock()
	start := c.now()
	if e, ok := c.entries[id.SessionID]; ok && start.Sub(e.checkedAt) < SessionCheckTTL {
		c.mu.Unlock()
		if e.live {
			return verdictLive, e.confirmedEmail
		}
		return verdictRevoked, ""
	}
	call, shared := c.inflight[id.SessionID]
	if !shared {
		call = &sessionCall{subject: id.Subject, done: make(chan struct{})}
		c.inflight[id.SessionID] = call
	}
	c.mu.Unlock()

	if shared {
		select {
		case <-call.done:
			return call.verdict, call.confirmedEmail
		case <-r.Context().Done():
			return verdictUnavailable, ""
		}
	}
	c.run(r, id.SessionID, call, start)
	return call.verdict, call.confirmedEmail
}

// run performs the shared call. The cleanup is deferred so a panic still releases the waiters.
func (c *SessionChecker) run(r *http.Request, sid string, call *sessionCall, start time.Time) {
	defer func() {
		c.mu.Lock()
		if c.inflight[sid] == call {
			delete(c.inflight, sid)
		}
		if !call.evicted && call.verdict != verdictUnavailable {
			c.store(sid, sessionEntry{subject: call.subject, live: call.verdict == verdictLive, confirmedEmail: call.confirmedEmail, checkedAt: start})
		}
		c.mu.Unlock()
		close(call.done)
	}()
	// Shared by every waiter, so one caller's cancellation must not fail it for the rest.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), SessionCheckTimeout)
	defer cancel()
	// GoTrue accepts only "Bearer <token>"; the verifier also takes other casing and spacing.
	token, _ := auth.BearerToken(r)
	call.verdict, call.confirmedEmail = c.ask(ctx, "Bearer "+token)
}

// ask calls GoTrue /user with the caller's bearer. It reads error_code from a refusal and
// email and email_confirmed_at from a 200; the email is returned only when confirmed.
func (c *SessionChecker) ask(ctx context.Context, authorization string) (verdict, string) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.userURL, nil)
	if err != nil {
		c.log.WarnContext(ctx, "session check: build request", slog.String("error", err.Error()))
		return verdictUnavailable, ""
	}
	req.Header.Set("Authorization", authorization)
	resp, err := c.client.Do(req)
	if err != nil {
		c.log.WarnContext(ctx, "session check: gotrue unreachable", slog.String("error", err.Error()))
		return verdictUnavailable, ""
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusOK {
		body := io.LimitReader(resp.Body, maxSessionUserBody)
		defer func() { _, _ = io.Copy(io.Discard, body) }()
		// A body that does not decode is still live, with no confirmed email.
		var u struct {
			Email            string  `json:"email"`
			EmailConfirmedAt *string `json:"email_confirmed_at"`
		}
		if json.NewDecoder(body).Decode(&u) != nil || u.EmailConfirmedAt == nil || *u.EmailConfirmedAt == "" {
			return verdictLive, ""
		}
		return verdictLive, u.Email
	}
	body := io.LimitReader(resp.Body, maxSessionCheckBody)
	defer func() { _, _ = io.Copy(io.Discard, body) }()
	if sessionGone(resp.StatusCode, body) {
		return verdictRevoked, ""
	}
	c.log.WarnContext(ctx, "session check: gotrue /user refused", slog.Int("upstream_status", resp.StatusCode))
	return verdictUnavailable, ""
}

// sessionGone reports whether a GoTrue 401/403 names a goneCodes error_code. It reads 1 KiB of body, only error_code.
func sessionGone(status int, body io.Reader) bool {
	if status == http.StatusUnauthorized || status == http.StatusForbidden {
		var e struct {
			ErrorCode string `json:"error_code"`
		}
		return json.NewDecoder(io.LimitReader(body, maxSessionCheckBody)).Decode(&e) == nil && goneCodes[e.ErrorCode]
	}
	return false
}

// store caches a verdict; a full cache sweeps expired entries and, if still full, stores nothing. Caller holds mu.
// ceiling: O(n) scan on every store while full, n <= SessionCheckMaxEntries; bucket by expiry if it shows in profiles.
func (c *SessionChecker) store(sid string, e sessionEntry) {
	if _, ok := c.entries[sid]; !ok && len(c.entries) >= c.maxEntries {
		now := c.now()
		for k, old := range c.entries {
			if now.Sub(old.checkedAt) >= SessionCheckTTL {
				delete(c.entries, k)
			}
		}
		if len(c.entries) >= c.maxEntries {
			return
		}
	}
	c.entries[sid] = e
}

// EvictSubject drops every cached verdict for sub; its in-flight calls answer their waiters but cache nothing.
// ceiling: O(n) scan over every entry; index by subject above ~10k live sessions.
func (c *SessionChecker) EvictSubject(sub string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for sid, e := range c.entries {
		if e.subject == sub {
			delete(c.entries, sid)
		}
	}
	for sid, call := range c.inflight {
		if call.subject == sub {
			call.evicted = true
			delete(c.inflight, sid)
		}
	}
}

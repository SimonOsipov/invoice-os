package gateway

import (
	"context"
	"log/slog"
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/SimonOsipov/invoice-os/internal/platform"
)

const (
	EventSignedIn               = "account.signed_in"
	EventSignInFailed           = "account.sign_in_failed"
	EventSignInBlocked          = "account.sign_in_blocked"
	EventSignedOut              = "account.signed_out"
	EventPasswordResetRequested = "account.password_reset_requested"
	EventPasswordResetCompleted = "account.password_reset_completed"

	// Mirrors the CHECK on account_event_handoffs.user_agent.
	maxUserAgentRunes = 512
)

// AccountEvent is one account event. UserID or Email must be set; IP and UserAgent are filled from the request.
type AccountEvent struct {
	Event, UserID, Email, IP, UserAgent string
}

// AccountEvents records account events. A handler given nil records nothing and logs nothing (tests only; main passes NewAccountEvents).
type AccountEvents interface {
	Record(ctx context.Context, e AccountEvent) error
}

// AccountEventOutbox writes each event to account_event_handoffs as invoice_gateway, inside the
// request; the worker moves the row into the audit log.
type AccountEventOutbox struct{ pool *pgxpool.Pool }

// Record inserts the event. A client that leaves must not drop it.
func (o *AccountEventOutbox) Record(ctx context.Context, e AccountEvent) error {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), outboxTimeout)
	defer cancel()
	_, err := o.pool.Exec(ctx, `INSERT INTO account_event_handoffs (event, user_id, email, ip, user_agent)
		VALUES ($1, $2, $3, $4, $5)`, e.Event, nullIfEmpty(e.UserID), nullIfEmpty(e.Email), e.IP, e.UserAgent)
	return err
}

func nullIfEmpty(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// logOnlyEvents logs the event name and stores nothing.
type logOnlyEvents struct{ log *slog.Logger }

func (l logOnlyEvents) Record(ctx context.Context, e AccountEvent) error {
	l.log.InfoContext(ctx, "account-event: not stored, no gateway pool", slog.String("event", e.Event))
	return nil
}

// NewAccountEvents returns the outbox with a pool and the log-only recorder without one.
func NewAccountEvents(pool *pgxpool.Pool, log *slog.Logger) AccountEvents {
	if pool == nil {
		return logOnlyEvents{log}
	}
	return &AccountEventOutbox{pool: pool}
}

// recordAccountEvent adds the client IP and user agent and records e. A failure is logged by event
// name only and never changes the answer.
func recordAccountEvent(r *http.Request, events AccountEvents, log *slog.Logger, e AccountEvent) {
	if events == nil {
		return
	}
	e.IP = clientIP(r, platform.Posture(platform.DeployEnv()))
	e.UserAgent = cleanUserAgent(r.UserAgent())
	if err := events.Record(r.Context(), e); err != nil {
		log.ErrorContext(r.Context(), "account-event: record failed", slog.String("event", e.Event))
		platform.CaptureError(r.Context(), err)
	}
}

// cleanUserAgent makes ua valid UTF-8 without NUL and at most maxUserAgentRunes long.
func cleanUserAgent(ua string) string {
	ua = strings.ReplaceAll(strings.ToValidUTF8(ua, ""), "\x00", "")
	if utf8.RuneCountInString(ua) <= maxUserAgentRunes {
		return ua
	}
	return string([]rune(ua)[:maxUserAgentRunes])
}

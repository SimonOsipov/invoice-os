package gateway

import (
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// ceiling: 2 × RegistrationMailPerHour must stay under GOTRUE_RATE_LIMIT_EMAIL_SENT (invoice-os-infra terraform/run.tf).
// ceiling: RegistrationMailPerDay fits Resend Free's 100 a day; drop it on a plan with no daily limit.
const (
	RegistrationMailPerHour = 10
	RegistrationMailPerDay  = 30
)

// RegistrationMail is the instance-wide share of GoTrue mail that /auth/register and /auth/resend-verification may spend.
type RegistrationMail struct {
	hour, day *SignInThrottle
	now       func() time.Time
}

func NewRegistrationMail(scope string, perHour, perDay int, now func() time.Time) *RegistrationMail {
	return &RegistrationMail{
		hour: NewSignInThrottle(scope+"-hour", perHour, 2, time.Hour, now),
		day:  NewSignInThrottle(scope+"-day", perDay, 4, 24*time.Hour, now),
		now:  now,
	}
}

// Shared counts in gateway_throttle_counts through pool, so every instance sees one count.
func (m *RegistrationMail) Shared(pool *pgxpool.Pool, log *slog.Logger) {
	m.hour.Shared(pool, log)
	m.day.Shared(pool, log)
}

func (m *RegistrationMail) dayKey() string { return "day:" + m.now().UTC().Format(time.DateOnly) }

// Admit reserves one send in both windows; spent names the window that refused ("hour" or "day").
// A day refusal or error refunds the hour. A nil m admits.
func (m *RegistrationMail) Admit() (spent string, err error) {
	if m == nil {
		return "", nil
	}
	ok, err := m.hour.Admit("all")
	if err != nil {
		return "", err
	}
	if !ok {
		return "hour", nil
	}
	ok, err = m.day.Admit(m.dayKey())
	if err != nil || !ok {
		m.hour.Refund("all")
		if err != nil {
			return "", err
		}
		return "day", nil
	}
	return "", nil
}

// Refund returns one send to both windows. A nil m does nothing.
func (m *RegistrationMail) Refund() {
	if m == nil {
		return
	}
	m.hour.Refund("all")
	m.day.Refund(m.dayKey())
}

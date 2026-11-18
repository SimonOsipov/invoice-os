package gateway

import (
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// ceiling: 2 × (RegistrationMailPerHour + ResetMailPerHour) plus the invitee reserve must stay under GOTRUE_RATE_LIMIT_EMAIL_SENT (invoice-os-infra terraform/run.tf).
// ceiling: RegistrationMailPerDay + ResetMailPerDay must leave half of Resend Free's 100 a day to other mail.
// ceiling: anyone can spend the reset share from 3 client IPs (IPv6 per /64) in an hour, or 7 in 24 h, and stop every user's reset mail until the window turns; a /48 holder has many /64s; revisit if the reset alert repeats.
const (
	ResetMailPerHour   = 3  // share, key "all"
	ResetMailPerDay    = 20 // share, key "day:<UTC date>"
	ResetMailPerIPHour = 1  // per gotrueAddress(r)
	ResetMailPerIPDay  = 3  // per gotrueAddress(r), 24 h
)

// ResetMail is the instance-wide share of GoTrue mail that /auth/request-password-reset may spend, bounded per client IP.
type ResetMail struct {
	hour, day, ipHour, ipDay *SignInThrottle
	now                      func() time.Time
}

func NewResetMail(scope string, perHour, perDay, perIPHour, perIPDay int, now func() time.Time) *ResetMail {
	return &ResetMail{
		hour:   NewSignInThrottle(scope+"-hour", perHour, 2, time.Hour, now),
		day:    NewSignInThrottle(scope+"-day", perDay, 4, 24*time.Hour, now),
		ipHour: NewSignInThrottle(scope+"-ip-hour", perIPHour, ResendMaxKeys, time.Hour, now),
		ipDay:  NewSignInThrottle(scope+"-ip-day", perIPDay, ResendMaxKeys, 24*time.Hour, now),
		now:    now,
	}
}

// Shared counts all four windows in gateway_throttle_counts through pool.
func (m *ResetMail) Shared(pool *pgxpool.Pool, log *slog.Logger) {
	m.hour.Shared(pool, log)
	m.day.Shared(pool, log)
	m.ipHour.Shared(pool, log)
	m.ipDay.Shared(pool, log)
}

// ResetHold is what one Hold took; the zero value took nothing.
type ResetHold struct {
	m             *ResetMail
	ipKey, dayKey string // dayKey is fixed at Hold time so a refund after UTC midnight returns the same day
}

// Hold takes one send in ip-hour, ip-day, hour and day, in that order; spent names the refusing window.
// A refusal or error refunds what this call took and returns a zero hold. A nil m admits.
func (m *ResetMail) Hold(ipKey string) (h ResetHold, spent string, err error) {
	if m == nil {
		return ResetHold{}, "", nil
	}
	h = ResetHold{m: m, ipKey: ipKey, dayKey: "day:" + m.now().UTC().Format(time.DateOnly)}
	steps := [...]struct {
		t         *SignInThrottle
		key, name string
	}{{m.ipHour, ipKey, "ip-hour"}, {m.ipDay, ipKey, "ip-day"}, {m.hour, "all", "hour"}, {m.day, h.dayKey, "day"}}
	for i, s := range steps {
		ok, aerr := s.t.Admit(s.key)
		if aerr == nil && ok {
			continue
		}
		for _, p := range steps[:i] {
			p.t.Refund(p.key)
		}
		if aerr != nil {
			return ResetHold{}, "", aerr
		}
		return ResetHold{}, s.name, nil
	}
	return h, "", nil
}

// Refund returns exactly what Hold took, on Hold's day key.
func (h ResetHold) Refund() {
	if h.m == nil {
		return
	}
	h.m.ipHour.Refund(h.ipKey)
	h.m.ipDay.Refund(h.ipKey)
	h.m.hour.Refund("all")
	h.m.day.Refund(h.dayKey)
}

package gateway

import (
	"context"
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/SimonOsipov/invoice-os/internal/platform"
)

var (
	ErrIdPMailCap            = errors.New("identity provider mail cap reached")
	ErrRegistrationMailSpent = errors.New("registration mail share spent")
)

// MailAlertEvery is the least time between two captures of one error.
const MailAlertEvery = 6 * time.Hour

type mailAlertGate struct {
	mu   sync.Mutex
	last map[error]time.Time
	now  func() time.Time
}

// ceiling: one capture per error per 6 h per instance keeps an attack under ~500 Sentry events a month; revisit on a paid Sentry plan
var mailAlerts = &mailAlertGate{last: map[error]time.Time{}, now: time.Now}

// isIdPMailCap reports whether a 429 over_email_send_rate_limit is the instance cap, not the per-address cooldown.
func isIdPMailCap(gt gotrueError) bool {
	return gt.ErrorCode == "over_email_send_rate_limit" && !strings.HasPrefix(gt.Msg, msgResendCooldownPrefix)
}

// alertMail captures err with no request data. The 6-hour bucket in the fingerprint makes each capture a new issue.
func alertMail(ctx context.Context, err error) {
	g := mailAlerts
	g.mu.Lock()
	now := g.now()
	if at, ok := g.last[err]; ok && now.Sub(at) < MailAlertEvery {
		g.mu.Unlock()
		return
	}
	g.last[err] = now
	g.mu.Unlock()
	platform.CaptureErrorGrouped(ctx, err, "gateway-mail-alert", err.Error(), now.UTC().Truncate(MailAlertEvery).Format(time.RFC3339))
}

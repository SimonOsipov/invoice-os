package gateway

import (
	"log/slog"
	"net/http"
	"time"
)

// One per-IP count of link redeems, shared by the confirm, reset and invitee set-password POSTs.
const (
	LinkRedeemPerIP    = 30
	LinkRedeemIPWindow = time.Hour
	LinkRedeemMaxKeys  = 10_000
)

// admitLinkRedeem counts one redeem under gotrueAddress(r); false: redirect to failed, no GoTrue call.
func admitLinkRedeem(r *http.Request, perIP *SignInThrottle, enforce bool, label string, log *slog.Logger) bool {
	held, err := perIP.Admit(gotrueAddress(r))
	if held {
		return true
	}
	if err == nil { // a db error is logged by the throttle
		log.WarnContext(r.Context(), label+": limit reached", slog.String("limit", "ip"), slog.Bool("enforced", enforce))
	}
	return !enforce
}

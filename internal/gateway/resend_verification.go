package gateway

import (
	"encoding/json"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"time"
)

// ceiling: counts are in-process; a restart clears them and replicas do not share them.
// ceiling: an attacker can spend a victim address's 3 per hour; a full map (10,000 keys) refuses every new key, so a flood switches resend off for new addresses.
// ceiling: clients behind one NAT share 10 resends an hour; GoTrue 4xx answers are refunded, so one client can repeat them without bound (no mail goes out).
const (
	ResendPerAddress = 3
	ResendPerIP      = 10
	ResendWindow     = time.Hour
	ResendMaxKeys    = 10_000
)

// clientKey keys the per-IP limit and names its source: Railway's edge sets X-Real-IP; IPv6 is keyed by its /64.
// RemoteAddr, the fallback, is normalised the same way.
// ceiling: the header is trusted only while the gateway is served straight from Railway's edge; a proxy in front makes every key the proxy's IP.
func clientKey(r *http.Request) (key, source string) {
	if ip, err := netip.ParseAddr(strings.TrimSpace(r.Header.Get("X-Real-IP"))); err == nil {
		return normalizeIP(ip), "header"
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	if ip, err := netip.ParseAddr(host); err == nil {
		return normalizeIP(ip), "remote_addr"
	}
	return host, "remote_addr"
}

func normalizeIP(ip netip.Addr) string {
	ip = ip.Unmap()
	if ip.Is6() {
		// A /64 is one subscriber; per-address keys would give an attacker 2^64 of them.
		return netip.PrefixFrom(ip.WithZone(""), 64).Masked().String()
	}
	return ip.String()
}

// ResendVerificationHandler answers POST /auth/resend-verification with one answer for every account state.
// Every answer except a 400 arrives no earlier than minResponse after the request; 0 means no wait.
func ResendVerificationHandler(authURL *url.URL, client *http.Client, minResponse time.Duration, perAddress, perIP *SignInThrottle, enforce bool, log *slog.Logger) http.Handler {
	resend := authURL.JoinPath("resend").String()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Not postOnly: it sets headers on a POST, and a gone client must see no write.
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", http.MethodPost)
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		start := time.Now()
		var in struct {
			Email string `json:"email"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxExchangeBodyBytes)).Decode(&in); err != nil {
			writeError(w, http.StatusBadRequest, "invalid request body")
			return
		}
		email := strings.TrimSpace(in.Email)
		if email == "" {
			writeError(w, http.StatusBadRequest, "email is required")
			return
		}
		if len(email) > maxEmailBytes {
			writeError(w, http.StatusBadRequest, "invalid email address")
			return
		}

		// IP first: a request refused for its IP spends no address count.
		key, source := clientKey(r)
		ipHeld := perIP.Reserve(key)
		addrHeld := false
		limited := func(limit string) bool {
			log.WarnContext(r.Context(), "resend-verification: limit reached",
				slog.String("limit", limit), slog.String("key_source", source), slog.Bool("enforced", enforce))
			return enforce
		}
		refused := false
		if !ipHeld {
			refused = limited("ip")
		} else if addrHeld = perAddress.Reserve(email); !addrHeld {
			refused = limited("address")
		}

		var upstream time.Duration
		if !refused {
			status, gt, err := postGoTrue(r, client, resend, map[string]string{"type": "signup", "email": email}, nil)
			upstream = time.Since(start)
			// GoTrue mails nothing when it answers 4xx; 2xx, 5xx and transport errors may have mailed.
			if err == nil && status >= http.StatusBadRequest && status < http.StatusInternalServerError {
				if ipHeld {
					perIP.Refund(key)
				}
				if addrHeld {
					perAddress.Refund(email)
				}
			}
			switch {
			case err != nil:
				log.WarnContext(r.Context(), "resend-verification: gotrue unreachable", slog.String("error", err.Error()))
			case status == http.StatusOK:
			case gt.ErrorCode == "validation_failed":
				writeError(w, http.StatusBadRequest, "invalid email address")
				return
			case gt.ErrorCode == "over_email_send_rate_limit":
				log.WarnContext(r.Context(), "resend-verification: gotrue email send rate limit", slog.Int("upstream_status", status))
			default:
				log.WarnContext(r.Context(), "resend-verification: gotrue resend failed",
					slog.Int("upstream_status", status), slog.String("error_code", gt.ErrorCode))
			}
		}

		if holdMinimum(r.Context(), log, "resend-verification: timing", start, upstream, minResponse) {
			writeJSON(w, http.StatusAccepted, map[string]string{"status": "accepted"})
		}
	})
}

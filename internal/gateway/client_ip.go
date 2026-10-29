package gateway

import (
	"net"
	"net/http"
	"net/netip"
	"strings"

	"github.com/SimonOsipov/invoice-os/internal/platform"
)

// clientIP is the caller's address, or "" when it cannot be trusted. Hosted takes the
// X-Forwarded-For entry second from the right (the load balancer appends <client>, <rule IP>),
// preview the rightmost, local the RemoteAddr host. Never the leftmost: a client writes it.
func clientIP(r *http.Request, posture platform.PostureKind) string {
	if posture == platform.PostureLocal {
		host, _, err := net.SplitHostPort(r.RemoteAddr)
		if err != nil {
			host = r.RemoteAddr
		}
		a, err := netip.ParseAddr(host)
		if err != nil {
			return ""
		}
		return a.Unmap().String()
	}
	entries := strings.Split(strings.Join(r.Header.Values("X-Forwarded-For"), ","), ",")
	back := 1
	if posture == platform.PostureHosted {
		back = 2
	}
	if len(entries) < back {
		return ""
	}
	a, err := netip.ParseAddr(strings.TrimSpace(entries[len(entries)-back]))
	if err != nil {
		return ""
	}
	a = a.Unmap()
	if a.Zone() != "" || !a.IsGlobalUnicast() || a.IsPrivate() {
		return ""
	}
	return a.String()
}

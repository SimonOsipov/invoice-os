package tenancy

import (
	"context"
	"net/http"
	"net/netip"

	"github.com/SimonOsipov/invoice-os/internal/accountevents"
	"github.com/SimonOsipov/invoice-os/internal/platform"
)

type requestDetailKey struct{}

type requestDetail struct{ ip, userAgent string }

// withRequestDetail carries the gateway's X-User-IP and the user agent to the audit write.
func withRequestDetail(r *http.Request) context.Context {
	return context.WithValue(r.Context(), requestDetailKey{},
		requestDetail{ip: r.Header.Get(platform.HeaderUserIP), userAgent: r.UserAgent()})
}

// requestDetailOf returns the audit ip_address and device; the unknown literals match the account.* events.
func requestDetailOf(ctx context.Context) (ip, device string) {
	d, _ := ctx.Value(requestDetailKey{}).(requestDetail)
	ip = accountevents.UnknownIP
	if addr, err := netip.ParseAddr(d.ip); err == nil {
		ip = addr.Unmap().String()
	}
	return ip, accountevents.Parse(d.userAgent).Label()
}

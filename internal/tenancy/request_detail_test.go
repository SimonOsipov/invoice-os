package tenancy

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

const (
	uaChromeMac      = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/130.0.0.0 Safari/537.36"
	uaFirefoxWindows = "Mozilla/5.0 (Windows NT 10.0; Win64; x64; rv:131.0) Gecko/20100101 Firefox/131.0"
)

// withDetail wraps ctx with the request detail a gateway-fronted request carries.
func withDetail(ctx context.Context, ip, ua string) context.Context {
	r := httptest.NewRequestWithContext(ctx, http.MethodPost, "/", nil)
	if ip != "" {
		r.Header.Set("X-User-IP", ip)
	}
	if ua != "" {
		r.Header.Set("User-Agent", ua)
	}
	return withRequestDetail(r)
}

func TestRequestDetailOf_Table(t *testing.T) {
	for _, c := range []struct{ name, ip, wantIP string }{
		{"ipv4", "203.0.113.7", "203.0.113.7"},
		{"ipv6", "2001:db8::7", "2001:db8::7"},
		{"ipv4-mapped ipv6 is unmapped", "::ffff:203.0.113.7", "203.0.113.7"},
		{"empty", "", "Unknown IP"},
		{"garbage", "garbage", "Unknown IP"},
	} {
		t.Run(c.name, func(t *testing.T) {
			ip, device := requestDetailOf(withDetail(context.Background(), c.ip, uaChromeMac))
			if ip != c.wantIP || device != "Chrome on macOS" {
				t.Errorf("requestDetailOf = (%q, %q), want (%q, Chrome on macOS)", ip, device, c.wantIP)
			}
		})
	}
	t.Run("no detail", func(t *testing.T) {
		if ip, device := requestDetailOf(context.Background()); ip != "Unknown IP" || device != "Unknown device" {
			t.Errorf("requestDetailOf = (%q, %q), want the unknown literals", ip, device)
		}
	})
}

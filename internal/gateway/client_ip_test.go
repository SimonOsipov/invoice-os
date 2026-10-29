package gateway

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/SimonOsipov/invoice-os/internal/platform"
)

func ipCase(t *testing.T, posture platform.PostureKind, remote string, xff []string, want string) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	if remote != "" {
		req.RemoteAddr = remote
	}
	for _, v := range xff {
		req.Header.Add("X-Forwarded-For", v)
	}
	if got := clientIP(req, posture); got != want {
		t.Errorf("clientIP(%s, remote %q, xff %q) = %q, want %q", posture, remote, xff, got, want)
	}
}

func TestClientIP_Table(t *testing.T) {
	hosted, preview, local := platform.PostureHosted, platform.PosturePreview, platform.PostureLocal
	one := func(v string) []string { return []string{v} }
	ipCase(t, hosted, "", one("198.51.100.1, 203.0.113.7, 34.1.2.3"), "203.0.113.7")
	ipCase(t, hosted, "", one("203.0.113.7"), "")
	ipCase(t, preview, "", one("198.51.100.1, 203.0.113.7"), "203.0.113.7")
	ipCase(t, local, "127.0.0.1:5555", nil, "127.0.0.1")
	ipCase(t, preview, "", one("garbage"), "")
	ipCase(t, preview, "", one("2a00:1450::1"), "2a00:1450::1")
	ipCase(t, hosted, "", one("  198.51.100.1 ,   203.0.113.7  , 34.1.2.3 "), "203.0.113.7")
	ipCase(t, preview, "", nil, "")
	ipCase(t, hosted, "", nil, "")
	ipCase(t, hosted, "", []string{"198.51.100.1", "203.0.113.7, 34.1.2.3"}, "203.0.113.7")
}

func TestClientIP_NonPublicEntryIsUnknown(t *testing.T) {
	one := func(v string) []string { return []string{v} }
	ipCase(t, platform.PostureHosted, "", one("203.0.113.7, 10.8.0.2, 34.1.2.3"), "")
	ipCase(t, platform.PostureHosted, "", one("198.51.100.1, 169.254.1.1, 34.1.2.3"), "")
	ipCase(t, platform.PosturePreview, "", one("198.51.100.1, 192.168.1.4"), "")
	ipCase(t, platform.PosturePreview, "", one("198.51.100.1, ::1"), "")
	ipCase(t, platform.PostureLocal, "127.0.0.1:5555", nil, "127.0.0.1")
}

func TestClientIP_SpoofAndOddForms(t *testing.T) {
	hosted, preview, local := platform.PostureHosted, platform.PosturePreview, platform.PostureLocal
	one := func(v string) []string { return []string{v} }
	ipCase(t, hosted, "", one("1.1.1.1, 2.2.2.2, 3.3.3.3, 198.51.100.9, 203.0.113.7, 34.1.2.3"), "203.0.113.7")
	ipCase(t, preview, "", one("1.1.1.1, 2.2.2.2, 203.0.113.7"), "203.0.113.7")
	ipCase(t, hosted, "", one("203.0.113.7, 34.1.2.3, "), "34.1.2.3")
	ipCase(t, hosted, "", one(", 34.1.2.3"), "")
	ipCase(t, preview, "", one("::ffff:203.0.113.7"), "203.0.113.7")
	ipCase(t, preview, "", one("::ffff:10.0.0.1"), "")
	ipCase(t, preview, "", one("fe80::1%eth0"), "")
	ipCase(t, preview, "", one("2a00:1450::1%eth0"), "")
	ipCase(t, preview, "", one("fe80::1"), "")
	ipCase(t, preview, "", one("fc00::1"), "")
	ipCase(t, preview, "", one("0.0.0.0"), "")
	ipCase(t, preview, "", one("255.255.255.255"), "")
	ipCase(t, preview, "", one("224.0.0.1"), "")
	ipCase(t, preview, "", one("172.16.0.1"), "")
	ipCase(t, preview, "", one("127.0.0.1"), "")
	ipCase(t, preview, "", one("203.0.113.7:443"), "")
	ipCase(t, local, "[::1]:5555", one("203.0.113.7"), "::1")
	ipCase(t, local, "garbage", nil, "")
	ipCase(t, local, "203.0.113.7", nil, "203.0.113.7")
}

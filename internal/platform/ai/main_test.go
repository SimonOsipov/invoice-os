// main_test.go swaps http.DefaultTransport for a loopback-only guard so no
// test in this package can reach a real host.
package ai

import (
	"fmt"
	"net/http"
	"os"
	"strings"
	"testing"
)

type loopbackOnly struct {
	base http.RoundTripper
}

func (l loopbackOnly) RoundTrip(req *http.Request) (*http.Response, error) {
	switch req.URL.Hostname() {
	case "127.0.0.1", "::1", "localhost":
		return l.base.RoundTrip(req)
	default:
		return nil, fmt.Errorf("ai test guard: refused host %q", req.URL.Hostname())
	}
}

func TestMain(m *testing.M) {
	orig := http.DefaultTransport
	http.DefaultTransport = loopbackOnly{orig}
	os.Exit(m.Run())
}

// T17
func TestMain_RefusesNonLoopbackHosts(t *testing.T) {
	_, err := http.Get("https://openrouter.ai/")
	if err == nil {
		t.Fatal("http.Get(openrouter.ai) err = nil, want the guard to refuse it")
	}
	if !strings.Contains(err.Error(), "ai test guard") {
		t.Errorf("err = %v, want it to contain %q", err, "ai test guard")
	}
}

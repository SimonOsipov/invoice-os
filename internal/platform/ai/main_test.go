// main_test.go swaps http.DefaultTransport for a loopback-only guard so no
// test in this package can reach a real host.
package ai

import (
	"fmt"
	"net/http"
	"os"
	"strings"
	"sync/atomic"
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

// dials counts every RoundTrip through this package's test binary, refused
// hosts included. noDials reads it as a before/after delta, which two tests
// could not share, so no test in this package may call t.Parallel.
var dials atomic.Int32

type countingTransport struct{ base http.RoundTripper }

func (c countingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	dials.Add(1)
	return c.base.RoundTrip(req)
}

const (
	// envSubprocess makes this binary call FromEnv once and exit 0 instead of
	// running tests. Re-execing it is the only way an os.Exit or log.Fatal
	// inside FromEnv is observable (TestFromEnv_DoesNotExitTheProcess).
	envSubprocess = "AI_TEST_FROMENV_SUBPROCESS"
	// subprocessDone is printed only if FromEnv returned.
	subprocessDone = "FROMENV-RETURNED"
)

func TestMain(m *testing.M) {
	orig := http.DefaultTransport
	http.DefaultTransport = countingTransport{loopbackOnly{orig}}
	if os.Getenv(envSubprocess) != "" {
		c, err := FromEnv(nil)
		fmt.Printf("%s client=%v err=%v\n", subprocessDone, c != nil, err)
		os.Exit(0)
	}
	os.Exit(m.Run())
}

// noDials fails the test if fn dialled anything.
func noDials(t *testing.T, fn func()) {
	t.Helper()
	before := dials.Load()
	fn()
	if after := dials.Load() - before; after != 0 {
		t.Errorf("%d dial(s) during the call, want 0", after)
	}
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

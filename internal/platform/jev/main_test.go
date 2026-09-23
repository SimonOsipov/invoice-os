// main_test.go swaps http.DefaultTransport for a loopback-only guard so no
// test in this package can reach a real host.
package jev

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
		return nil, fmt.Errorf("jev test guard: refused host %q", req.URL.Hostname())
	}
}

// dials counts every RoundTrip, refused hosts included. noDials reads it as a
// before/after delta, so no test in this package may call t.Parallel.
var dials atomic.Int32

type countingTransport struct{ base http.RoundTripper }

func (c countingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	dials.Add(1)
	return c.base.RoundTrip(req)
}

const (
	// envSubprocess makes this binary call FromEnv once and exit 0; only a
	// re-exec can observe an os.Exit inside FromEnv.
	envSubprocess  = "JEV_TEST_FROMENV_SUBPROCESS"
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

// noDials fails the test if fn sent anything through http.DefaultTransport.
func noDials(t *testing.T, fn func()) {
	t.Helper()
	before := dials.Load()
	fn()
	if after := dials.Load() - before; after != 0 {
		t.Errorf("%d dial(s) during the call, want 0", after)
	}
}

func TestMain_RefusesNonLoopbackHosts(t *testing.T) {
	before := dials.Load()
	_, err := http.Get("https://api.typesafe.ai/")
	if err == nil {
		t.Fatal("http.Get(api.typesafe.ai) err = nil, want the guard to refuse it")
	}
	if !strings.Contains(err.Error(), "jev test guard") {
		t.Errorf("err = %v, want it to contain %q", err, "jev test guard")
	}
	if got := dials.Load() - before; got != 1 {
		t.Errorf("dial counter moved by %d, want 1: noDials reads a counter that never moves", got)
	}
}

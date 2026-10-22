package systemhealth

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/SimonOsipov/invoice-os/internal/gateway"
	"github.com/SimonOsipov/invoice-os/internal/platform"
)

func readFrom(t *testing.T, h http.HandlerFunc, timeout time.Duration) (gateway.FleetHealth, error) {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return readServer(t, srv, timeout)
}

func readServer(t *testing.T, srv *httptest.Server, timeout time.Duration) (gateway.FleetHealth, error) {
	t.Helper()
	base, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	return NewFleetReader(base, &http.Client{Timeout: timeout}, "gw-token")(context.Background())
}

func TestFleetReader_DecodesOKAndDegraded(t *testing.T) {
	for _, tc := range []struct {
		code int
		body string
		want string
		down int
	}{
		{200, `{"status":"ok","services":[{"name":"a","status":"up"}]}`, "ok", 0},
		{503, `{"status":"degraded","services":[{"name":"a","status":"up"},{"name":"b","status":"down","error":"x"}]}`, "degraded", 1},
	} {
		var reqs atomic.Int32
		var path string
		f, err := readFrom(t, func(w http.ResponseWriter, r *http.Request) {
			reqs.Add(1)
			path = r.URL.Path
			w.WriteHeader(tc.code)
			_, _ = w.Write([]byte(tc.body))
		}, time.Second)
		if err != nil {
			t.Fatalf("%d: %v", tc.code, err)
		}
		if f.Status != tc.want || ServicesDown(f) != tc.down || path != "/healthz/fleet" || reqs.Load() != 1 {
			t.Errorf("%d: status=%q down=%d path=%q reqs=%d", tc.code, f.Status, ServicesDown(f), path, reqs.Load())
		}
	}
}

func TestFleetReader_RefusesEveryOtherAnswer(t *testing.T) {
	answer := func(code int, body string) http.HandlerFunc {
		return func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(code)
			_, _ = w.Write([]byte(body))
		}
	}
	for name, h := range map[string]http.HandlerFunc{
		"500":             answer(500, `{"status":"ok","services":[]}`),
		"404":             answer(404, ``),
		"not json":        answer(200, `not json`),
		"unknown status":  answer(200, `{"status":"maybe","services":[]}`),
		"empty status":    answer(200, `{"services":[]}`),
		"body over 64KiB": answer(200, `{"status":"ok","services":[],"pad":"`+strings.Repeat("x", 70<<10)+`"}`),
		"timeout": func(w http.ResponseWriter, r *http.Request) {
			select {
			case <-r.Context().Done():
			case <-time.After(2 * time.Second):
			}
		},
	} {
		if _, err := readFrom(t, h, 100*time.Millisecond); err == nil {
			t.Errorf("%s: want an error", name)
		}
	}

	srv := httptest.NewServer(answer(200, `{"status":"ok","services":[]}`))
	srv.Close()
	if _, err := readServer(t, srv, time.Second); err == nil {
		t.Error("closed server: want an error")
	}
}

func TestFleetReader_BodyLimitIsExactly64KiB(t *testing.T) {
	const doc = `{"status":"ok","services":[]}`
	pad := func(n int) http.HandlerFunc {
		return func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(doc + strings.Repeat(" ", n-len(doc))))
		}
	}
	if f, err := readFrom(t, pad(maxFleetBody), time.Second); err != nil || f.Status != "ok" {
		t.Errorf("body of exactly 64 KiB: %v, %q; want ok", err, f.Status)
	}
	if _, err := readFrom(t, pad(maxFleetBody+1), time.Second); err == nil {
		t.Error("valid JSON one byte over 64 KiB: want an error")
	}
}

func TestServicesDown_CountsEveryNonUpService(t *testing.T) {
	mk := func(statuses ...string) gateway.FleetHealth {
		var f gateway.FleetHealth
		for _, s := range statuses {
			f.Services = append(f.Services, gateway.ServiceHealth{Status: s})
		}
		return f
	}
	if got := ServicesDown(mk("up", "down", "up", "down")); got != 2 {
		t.Errorf("got %d, want 2", got)
	}
	if got := ServicesDown(mk("up", "unknown", "")); got != 2 {
		t.Errorf("non-up, non-down statuses: got %d, want 2", got)
	}
	if got := ServicesDown(mk("up", "up")); got != 0 {
		t.Errorf("got %d, want 0", got)
	}
}

// The reader and the gateway handler share one wire contract: the real handler's own status words and 503 must be accepted.
func TestFleetReader_ReadsTheRealGatewayRollUp(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }))
	t.Cleanup(up.Close)
	dead := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusInternalServerError) }))
	t.Cleanup(dead.Close)
	parse := func(s *httptest.Server) *url.URL { u, _ := url.Parse(s.URL); return u }

	for _, tc := range []struct {
		name      string
		upstreams map[string]*url.URL
		status    string
		down      []string
	}{
		{"all up", map[string]*url.URL{"alpha": parse(up)}, "ok", nil},
		{"one down", map[string]*url.URL{"alpha": parse(up), "beta": parse(dead)}, "degraded", []string{"beta"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			gw := httptest.NewServer(gateway.FleetHealthHandler(tc.upstreams, nil, nil))
			t.Cleanup(gw.Close)
			f, err := readServer(t, gw, 5*time.Second)
			if err != nil {
				t.Fatalf("read the real roll-up: %v", err)
			}
			var down []string
			for _, s := range f.Services {
				if s.Status != "up" {
					down = append(down, s.Name)
				}
			}
			if f.Status != tc.status || len(f.Services) != len(tc.upstreams)+1 || !slices.Equal(down, tc.down) || ServicesDown(f) != len(tc.down) {
				t.Errorf("status=%q services=%d down=%v, want %q, %d services, down %v", f.Status, len(f.Services), down, tc.status, len(tc.upstreams)+1, tc.down)
			}
		})
	}
}

func TestFleetReader_OwnsItsDeadline(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		select {
		case <-release:
		case <-time.After(5 * time.Second):
		}
	}))
	t.Cleanup(srv.Close)
	t.Cleanup(func() { close(release) })
	base, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	_, err = NewFleetReaderTimeout(base, &http.Client{}, "gw-token", 100*time.Millisecond)(context.Background())
	if err == nil {
		t.Fatal("a gateway that never answers must be an error")
	}
	if d := time.Since(start); d > 2*time.Second {
		t.Fatalf("reader waited %v; it must stop at its own deadline", d)
	}
}

type deadlineSeen struct {
	deadline time.Time
	token    string
}

func (d *deadlineSeen) RoundTrip(r *http.Request) (*http.Response, error) {
	d.deadline, _ = r.Context().Deadline()
	d.token = r.Header.Get(platform.HeaderGatewayToken)
	return nil, context.DeadlineExceeded
}

// main.go builds the reader with NewFleetReader: it must put a FleetTimeout deadline on the request without a client Timeout.
func TestFleetReader_TheProductionConstructorSetsTheFleetTimeoutDeadline(t *testing.T) {
	seen := &deadlineSeen{}
	base, _ := url.Parse("http://gateway.invalid:8080")
	start := time.Now()
	_, _ = NewFleetReader(base, &http.Client{Transport: seen}, "gw-token")(context.Background())
	if seen.token != "gw-token" {
		t.Errorf("%s = %q, want the gateway token: /healthz/fleet refuses a call without it", platform.HeaderGatewayToken, seen.token)
	}
	if seen.deadline.IsZero() {
		t.Fatal("the request carried no deadline")
	}
	if got := seen.deadline.Sub(start); got < FleetTimeout-time.Second || got > FleetTimeout+time.Second {
		t.Errorf("deadline %v after the call began, want about FleetTimeout %v", got, FleetTimeout)
	}
}

func TestGatewayURL_RequiresAnAbsoluteHTTPURL(t *testing.T) {
	for _, raw := range []string{"", "gateway.railway.internal:8080", "ftp://x", "http://"} {
		u, err := GatewayURL(raw)
		if err == nil || u != nil {
			t.Errorf("GatewayURL(%q) = %v, %v; want nil and an error", raw, u, err)
			continue
		}
		if raw == "" && !strings.Contains(err.Error(), "GATEWAY_URL is required") {
			t.Errorf("GatewayURL(%q) error = %q, want it to say GATEWAY_URL is required", raw, err)
		}
	}
	for _, raw := range []string{"http://gateway.railway.internal:8080", "https://gateway.example.com"} {
		u, err := GatewayURL(raw)
		if err != nil || u == nil || u.String() != raw {
			t.Errorf("GatewayURL(%q) = %v, %v; want the URL and no error", raw, u, err)
		}
	}
}

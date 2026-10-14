package systemhealth

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/SimonOsipov/invoice-os/internal/gateway"
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
	return NewFleetReader(base, &http.Client{Timeout: timeout})(context.Background())
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
	if got := ServicesDown(mk("up", "up")); got != 0 {
		t.Errorf("got %d, want 0", got)
	}
}

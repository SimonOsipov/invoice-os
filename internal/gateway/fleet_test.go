package gateway

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
)

// healthzUpstream is a stand-in context service: 200 on GET /healthz, else 404. When
// down is true it answers /healthz with 503, mimicking an unhealthy service.
func healthzUpstream(t *testing.T, down bool) *url.URL {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/healthz" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if down {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)
	u, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatalf("parse upstream url: %v", err)
	}
	return u
}

func doFleet(t *testing.T, upstreams map[string]*url.URL) (*httptest.ResponseRecorder, FleetHealth) {
	t.Helper()
	rec := httptest.NewRecorder()
	FleetHealthHandler(upstreams, nil).ServeHTTP(rec, httptest.NewRequest("GET", "/healthz/fleet", nil))
	var body FleetHealth
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode fleet body %q: %v", rec.Body.String(), err)
	}
	return rec, body
}

// statusByName indexes the payload for per-service assertions.
func statusByName(fh FleetHealth) map[string]ServiceHealth {
	m := make(map[string]ServiceHealth, len(fh.Services))
	for _, s := range fh.Services {
		m[s.Name] = s
	}
	return m
}

func TestFleetHealthAllUp(t *testing.T) {
	services := []string{"tenancy", "portfolio", "invoice", "validation", "submission", "dashboard", "notifications"}
	upstreams := make(map[string]*url.URL, len(services))
	for _, svc := range services {
		upstreams[svc] = healthzUpstream(t, false)
	}

	rec, body := doFleet(t, upstreams)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 when all backends are up", rec.Code)
	}
	if body.Status != fleetOK {
		t.Errorf("overall status = %q, want %q", body.Status, fleetOK)
	}
	// gateway + the seven context services.
	if len(body.Services) != len(services)+1 {
		t.Fatalf("reported %d services, want %d (gateway + 7 context)", len(body.Services), len(services)+1)
	}
	byName := statusByName(body)
	if g, ok := byName["gateway"]; !ok || g.Status != statusUp {
		t.Errorf("gateway = %+v, want present and up", g)
	}
	for _, svc := range services {
		if s, ok := byName[svc]; !ok || s.Status != statusUp {
			t.Errorf("%s = %+v, want present and up", svc, s)
		}
	}
}

func TestFleetHealthOneDownReports503AndCulprit(t *testing.T) {
	upstreams := map[string]*url.URL{
		"tenancy":       healthzUpstream(t, false),
		"portfolio":     healthzUpstream(t, true), // the one unhealthy service
		"invoice":       healthzUpstream(t, false),
		"validation":    healthzUpstream(t, false),
		"submission":    healthzUpstream(t, false),
		"dashboard":     healthzUpstream(t, false),
		"notifications": healthzUpstream(t, false),
	}

	rec, body := doFleet(t, upstreams)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503 when a backend is down", rec.Code)
	}
	if body.Status != fleetDegraded {
		t.Errorf("overall status = %q, want %q", body.Status, fleetDegraded)
	}
	byName := statusByName(body)
	culprit, ok := byName["portfolio"]
	if !ok || culprit.Status != statusDown {
		t.Fatalf("portfolio = %+v, want present and down (the culprit must be named)", culprit)
	}
	if culprit.Error == "" {
		t.Error("a down service must carry an error reason in the body")
	}
	// Every other service stays up — a single failure does not poison the roll-up.
	for _, svc := range []string{"tenancy", "invoice", "validation", "submission", "dashboard", "notifications"} {
		if s := byName[svc]; s.Status != statusUp {
			t.Errorf("%s = %+v, want up (only portfolio is down)", svc, s)
		}
	}
}

// TestFleetHealthUnreachableIsDown proves a backend that refuses the connection (its
// server is closed, as when scaled to zero) is reported down, not hung — the per-probe
// timeout / transport error is what the CI health-gate relies on.
func TestFleetHealthUnreachableIsDown(t *testing.T) {
	dead := httptest.NewServer(http.NotFoundHandler())
	deadURL, _ := url.Parse(dead.URL)
	dead.Close() // now every dial to deadURL is refused

	upstreams := map[string]*url.URL{"tenancy": deadURL}
	rec, body := doFleet(t, upstreams)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503 when a backend is unreachable", rec.Code)
	}
	if s := statusByName(body)["tenancy"]; s.Status != statusDown || s.Error == "" {
		t.Errorf("tenancy = %+v, want down with an error reason", s)
	}
}

package gateway

import (
	"encoding/json"
	"maps"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"sync"
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
	FleetHealthHandler(upstreams, nil, nil).ServeHTTP(rec, httptest.NewRequest("GET", "/healthz/fleet", nil))
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

// buildUpstream answers /healthz with a real body carrying the given build, so
// the probe's decode path is exercised rather than assumed.
func buildUpstream(t *testing.T, build string, body string) *url.URL {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/healthz" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if body != "" {
			_, _ = w.Write([]byte(body))
			return
		}
		_, _ = w.Write([]byte(`{"status":"ok","build":"` + build + `"}`))
	}))
	t.Cleanup(srv.Close)
	u, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatalf("parse upstream url: %v", err)
	}
	return u
}

// The deploy gate blocks until every service reports the commit under test, so
// the per-service build has to survive the probe and reach the payload.
func TestFleetReportsEachServiceBuild(t *testing.T) {
	_, body := doFleet(t, map[string]*url.URL{
		"invoice":   buildUpstream(t, "abc1234", ""),
		"tenancy":   buildUpstream(t, "abc1234", ""),
		"dashboard": buildUpstream(t, "0000000", ""),
	})

	got := map[string]string{}
	for _, s := range body.Services {
		got[s.Name] = s.Build
	}
	if got["invoice"] != "abc1234" || got["tenancy"] != "abc1234" {
		t.Errorf("builds = %v, want invoice and tenancy on abc1234", got)
	}
	// A service left on an older build must be visible as itself, not smoothed
	// over -- that difference is the entire point of the field.
	if got["dashboard"] != "0000000" {
		t.Errorf("dashboard build = %q, want 0000000", got["dashboard"])
	}
	// The gateway answers for itself and must report its own compiled-in sha.
	if got["gateway"] != BuildSHAForTest() {
		t.Errorf("gateway build = %q, want %q", got["gateway"], BuildSHAForTest())
	}
}

// A build mismatch is not an outage: /healthz/fleet must still be 200/ok, or the
// two independent questions ("is it serving?" / "is it the right commit?")
// collapse into one and the endpoint starts lying in the other direction.
func TestFleetBuildMismatchIsStillHealthy(t *testing.T) {
	rec, body := doFleet(t, map[string]*url.URL{
		"invoice": buildUpstream(t, "aaaaaaa", ""),
		"tenancy": buildUpstream(t, "bbbbbbb", ""),
	})
	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200 -- differing builds are not an outage", rec.Code)
	}
	if body.Status != fleetOK {
		t.Errorf("fleet status = %q, want %q", body.Status, fleetOK)
	}
}

// An undecodable body still settled up/down on the 2xx; Build stays empty and
// the gate reports the mismatch on its own terms rather than the probe failing.
func TestFleetUndecodableHealthzBodyStaysUpWithEmptyBuild(t *testing.T) {
	rec, body := doFleet(t, map[string]*url.URL{
		"invoice": buildUpstream(t, "", "not json at all"),
	})
	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", rec.Code)
	}
	for _, s := range body.Services {
		if s.Name == "invoice" {
			if s.Status != statusUp {
				t.Errorf("status = %q, want up", s.Status)
			}
			if s.Build != "" {
				t.Errorf("build = %q, want empty", s.Build)
			}
		}
	}
}

const jwksPath = "/.well-known/jwks.json"

// authPaths is the per-service health path the gateway passes for `auth`.
var authPaths = map[string]string{"auth": ".well-known/jwks.json"}

// pathRecordingUpstream answers only okPath, with status and body, and 404s every
// other path. It returns the paths it was asked for.
func pathRecordingUpstream(t *testing.T, okPath string, status int, body string) (*url.URL, func() []string) {
	t.Helper()
	var mu sync.Mutex
	var hits []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		hits = append(hits, r.URL.Path)
		mu.Unlock()
		if r.URL.Path != okPath {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	u, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatalf("parse upstream url: %v", err)
	}
	return u, func() []string {
		mu.Lock()
		defer mu.Unlock()
		return slices.Clone(hits)
	}
}

func healthzWithBuild(t *testing.T, build string) (*url.URL, func() []string) {
	t.Helper()
	return pathRecordingUpstream(t, "/healthz", http.StatusOK, `{"status":"ok","build":"`+build+`"}`)
}

func doFleetWithPaths(t *testing.T, upstreams map[string]*url.URL, paths map[string]string) (*httptest.ResponseRecorder, FleetHealth) {
	t.Helper()
	rec := httptest.NewRecorder()
	FleetHealthHandler(upstreams, paths, nil).ServeHTTP(rec, httptest.NewRequest("GET", "/healthz/fleet", nil))
	var body FleetHealth
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode fleet body %q: %v", rec.Body.String(), err)
	}
	return rec, body
}

func TestFleetProbesAuthAtJWKSPath(t *testing.T) {
	authURL, authHits := pathRecordingUpstream(t, jwksPath, http.StatusOK, `{"keys":[{"kty":"EC","crv":"P-256","kid":"k1","x":"x","y":"y"}]}`)
	invoiceURL, invoiceHits := healthzWithBuild(t, "abc1234")

	rec, body := doFleetWithPaths(t, map[string]*url.URL{"auth": authURL, "invoice": invoiceURL}, authPaths)

	byName := statusByName(body)
	a, ok := byName["auth"]
	if !ok {
		t.Fatalf("roll-up omits auth: %s", rec.Body.String())
	}
	if a.Status != statusUp {
		t.Errorf("auth = %+v, want up on a 200 from %s", a, jwksPath)
	}
	if a.Build != "" {
		t.Errorf("auth build = %q, want empty: a JWKS body carries no build", a.Build)
	}
	if hits := authHits(); !slices.Contains(hits, jwksPath) || slices.Contains(hits, "/healthz") {
		t.Errorf("auth upstream was asked for %v, want %s and never /healthz", hits, jwksPath)
	}

	// Every other service is still probed at /healthz and keeps its build.
	if inv := byName["invoice"]; inv.Status != statusUp || inv.Build != "abc1234" {
		t.Errorf("invoice = %+v, want up with build abc1234", inv)
	}
	if hits := invoiceHits(); len(hits) == 0 || slices.ContainsFunc(hits, func(p string) bool { return p != "/healthz" }) {
		t.Errorf("invoice upstream was asked for %v, want /healthz only", hits)
	}
	if rec.Code != http.StatusOK || body.Status != fleetOK {
		t.Errorf("roll-up = %d %q, want 200 %q", rec.Code, body.Status, fleetOK)
	}
}

func TestFleetAuthJWKSDownIsDown(t *testing.T) {
	authURL, authHits := pathRecordingUpstream(t, jwksPath, http.StatusServiceUnavailable, `{}`)

	rec, body := doFleetWithPaths(t, map[string]*url.URL{"auth": authURL}, authPaths)

	// Without this, a probe of /healthz (404, also down) would pass the checks below.
	if hits := authHits(); !slices.Contains(hits, jwksPath) {
		t.Errorf("auth upstream was asked for %v, want %s", hits, jwksPath)
	}
	a, ok := statusByName(body)["auth"]
	if !ok {
		t.Fatalf("roll-up omits auth: %s", rec.Body.String())
	}
	if a.Status != statusDown {
		t.Errorf("auth = %+v, want down on a 503", a)
	}
	if !strings.Contains(a.Error, "503") {
		t.Errorf("auth error = %q, want it to name the 503", a.Error)
	}
	if rec.Code != http.StatusServiceUnavailable || body.Status != fleetDegraded {
		t.Errorf("roll-up = %d %q, want 503 %q", rec.Code, body.Status, fleetDegraded)
	}
}

// A nil map, or a map without the service, keeps every probe on /healthz.
func TestFleetDefaultHealthPathUnchanged(t *testing.T) {
	for name, paths := range map[string]map[string]string{
		"nil map":       nil,
		"entry missing": authPaths,
	} {
		t.Run(name, func(t *testing.T) {
			a, aHits := healthzWithBuild(t, "abc1234")
			b, bHits := healthzWithBuild(t, "abc1234")

			rec, body := doFleetWithPaths(t, map[string]*url.URL{"invoice": a, "tenancy": b}, paths)

			if rec.Code != http.StatusOK {
				t.Errorf("status = %d, want 200 (body %s)", rec.Code, rec.Body.String())
			}
			for svc, hits := range map[string][]string{"invoice": aHits(), "tenancy": bHits()} {
				if len(hits) == 0 {
					t.Errorf("%s was never probed", svc)
				}
				for _, p := range hits {
					if p != "/healthz" {
						t.Errorf("%s was probed at %q, want /healthz", svc, p)
					}
				}
			}
			for _, svc := range []string{"invoice", "tenancy"} {
				if s := statusByName(body)[svc]; s.Status != statusUp || s.Build != "abc1234" {
					t.Errorf("%s = %+v, want up with build abc1234", svc, s)
				}
			}
		})
	}
}

// The roll-up is public, so GoTrue's version must not reach it even when the
// JWKS body carries one. The stray build key proves the entry takes nothing from the body.
func TestFleetRollupPublishesNoAuthVersion(t *testing.T) {
	authURL, _ := pathRecordingUpstream(t, jwksPath, http.StatusOK, `{"keys":[{"kty":"EC","kid":"k1"}],"version":"v9","build":"leaked"}`)

	rec, _ := doFleetWithPaths(t, map[string]*url.URL{"auth": authURL}, authPaths)

	var raw struct {
		Services []map[string]any `json:"services"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &raw); err != nil {
		t.Fatalf("decode %q: %v", rec.Body.String(), err)
	}
	if len(raw.Services) == 0 {
		t.Fatalf("roll-up has no services: %s", rec.Body.String())
	}
	var entry map[string]any
	for _, s := range raw.Services {
		if s["name"] == "auth" {
			entry = s
		}
	}
	if entry == nil {
		t.Fatalf("roll-up omits auth: %s", rec.Body.String())
	}
	if keys := slices.Sorted(maps.Keys(entry)); !slices.Equal(keys, []string{"name", "status"}) {
		t.Errorf("auth entry keys = %v, want [name status] (entry %v)", keys, entry)
	}
	if entry["status"] != statusUp {
		t.Errorf("auth status = %v, want %q", entry["status"], statusUp)
	}
	if b := rec.Body.String(); strings.Contains(strings.ToLower(b), "version") || strings.Contains(b, "v9") {
		t.Errorf("roll-up body names a version: %s", b)
	}
}

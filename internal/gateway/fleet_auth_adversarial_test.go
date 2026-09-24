package gateway

import (
	"encoding/json"
	"maps"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

// Only 2xx is up; a 3xx without Location is not followed and counts as down.
func TestFleetAuthJWKSStatusClasses(t *testing.T) {
	for _, c := range []struct {
		status int
		up     bool
	}{
		{200, true}, {204, true}, {299, true},
		{302, false}, {304, false}, {404, false}, {500, false},
	} {
		t.Run(strconv.Itoa(c.status), func(t *testing.T) {
			authURL, hits := pathRecordingUpstream(t, jwksPath, c.status, "")
			rec, body := doFleetWithPaths(t, map[string]*url.URL{"auth": authURL}, authPaths)

			if h := hits(); !slices.Contains(h, jwksPath) {
				t.Fatalf("auth upstream was asked for %v, want %s", h, jwksPath)
			}
			a, ok := statusByName(body)["auth"]
			if !ok {
				t.Fatalf("roll-up omits auth: %s", rec.Body.String())
			}
			if c.up {
				if a.Status != statusUp || rec.Code != http.StatusOK {
					t.Errorf("%d: auth = %+v, roll-up %d, want up and 200", c.status, a, rec.Code)
				}
				return
			}
			if a.Status != statusDown || rec.Code != http.StatusServiceUnavailable {
				t.Errorf("%d: auth = %+v, roll-up %d, want down and 503", c.status, a, rec.Code)
			}
			if !strings.Contains(a.Error, strconv.Itoa(c.status)) {
				t.Errorf("%d: error %q does not name the status", c.status, a.Error)
			}
		})
	}
}

// The probe client follows a redirect; the final status decides.
func TestFleetAuthJWKSRedirectIsFollowed(t *testing.T) {
	for _, c := range []struct {
		name   string
		target int
		up     bool
	}{
		{"to 200", http.StatusOK, true},
		{"to 404", http.StatusNotFound, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case jwksPath:
					http.Redirect(w, r, "/moved", http.StatusFound)
				case "/moved":
					w.WriteHeader(c.target)
				default:
					w.WriteHeader(http.StatusTeapot)
				}
			}))
			t.Cleanup(srv.Close)
			u, _ := url.Parse(srv.URL)

			_, body := doFleetWithPaths(t, map[string]*url.URL{"auth": u}, authPaths)
			a := statusByName(body)["auth"]
			if got := a.Status == statusUp; got != c.up {
				t.Errorf("auth = %+v, want up=%v", a, c.up)
			}
		})
	}
}

// A 2xx JWKS path is up whatever its body; nothing from the body is published.
func TestFleetAuthNonJSONBodyIsUpWithNoExtraKeys(t *testing.T) {
	authURL, _ := pathRecordingUpstream(t, jwksPath, http.StatusOK, `<html>version 2.0 build x</html>`)
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
	if entry["status"] != statusUp {
		t.Errorf("auth = %v, want up on a 200", entry)
	}
	if keys := slices.Sorted(maps.Keys(entry)); !slices.Equal(keys, []string{"name", "status"}) {
		t.Errorf("auth entry keys = %v, want [name status]", keys)
	}
}

// A hung auth is down within the per-probe timeout and does not hold up the roll-up.
func TestFleetSlowAuthTimesOutWithinProbeTimeout(t *testing.T) {
	release := make(chan struct{})
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-release:
		}
	}))
	t.Cleanup(slow.Close)
	t.Cleanup(func() { close(release) })
	slowURL, _ := url.Parse(slow.URL)
	invoiceURL, _ := healthzWithBuild(t, "abc1234")

	start := time.Now()
	rec, body := doFleetWithPaths(t, map[string]*url.URL{"auth": slowURL, "invoice": invoiceURL}, authPaths)
	elapsed := time.Since(start)

	// 3s is the probe timeout this subtask shipped with; 1s is scheduling slack.
	if elapsed > 4*time.Second {
		t.Errorf("roll-up took %v, want under 4s", elapsed)
	}
	byName := statusByName(body)
	if a := byName["auth"]; a.Status != statusDown || a.Error == "" {
		t.Errorf("auth = %+v, want down with a reason", a)
	}
	if inv := byName["invoice"]; inv.Status != statusUp {
		t.Errorf("invoice = %+v, want up", inv)
	}
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("roll-up = %d, want 503", rec.Code)
	}
}

// A healthPaths entry for a service that is not an upstream adds nothing.
func TestFleetHealthPathForAnUnprobedServiceIsIgnored(t *testing.T) {
	invoiceURL, hits := healthzWithBuild(t, "abc1234")
	paths := map[string]string{"auth": ".well-known/jwks.json", "ghost": "nowhere"}

	rec, body := doFleetWithPaths(t, map[string]*url.URL{"invoice": invoiceURL}, paths)

	if len(body.Services) == 0 {
		t.Fatalf("roll-up has no services: %s", rec.Body.String())
	}
	var names []string
	for _, s := range body.Services {
		names = append(names, s.Name)
	}
	if !slices.Equal(names, []string{"gateway", "invoice"}) {
		t.Errorf("roll-up names %v, want [gateway invoice]", names)
	}
	if h := hits(); len(h) == 0 || slices.ContainsFunc(h, func(p string) bool { return p != "/healthz" }) {
		t.Errorf("invoice was asked for %v, want /healthz only", h)
	}
	if rec.Code != http.StatusOK {
		t.Errorf("roll-up = %d, want 200", rec.Code)
	}
}

// AUTH_URL may carry a path prefix; the JWKS path joins under it.
func TestFleetAuthJWKSPathJoinsUnderABasePath(t *testing.T) {
	authURL, hits := pathRecordingUpstream(t, "/auth/v1"+jwksPath, http.StatusOK, `{"keys":[]}`)
	base := authURL.JoinPath("auth", "v1")

	_, body := doFleetWithPaths(t, map[string]*url.URL{"auth": base}, authPaths)

	if h := hits(); !slices.Equal(h, []string{"/auth/v1" + jwksPath}) {
		t.Errorf("auth was asked for %v, want [/auth/v1%s]", h, jwksPath)
	}
	if a := statusByName(body)["auth"]; a.Status != statusUp {
		t.Errorf("auth = %+v, want up", a)
	}
}

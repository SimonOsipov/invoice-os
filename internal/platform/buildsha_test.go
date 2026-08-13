package platform

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The embedded file ends with a newline; an untrimmed value would be compared
// against $GITHUB_SHA by the deploy gate and never match.
func TestBuildSHAIsTrimmed(t *testing.T) {
	if BuildSHA != strings.TrimSpace(BuildSHA) {
		t.Errorf("BuildSHA = %q, want no surrounding whitespace", BuildSHA)
	}
	if BuildSHA == "" {
		t.Error("BuildSHA is empty; the deploy gate would never see a match")
	}
}

// /healthz carries it, because that is the only thing the gateway's fleet probe
// reads and the whole gate is built on it.
func TestHealthzCarriesBuild(t *testing.T) {
	rec := httptest.NewRecorder()
	healthzHandler(rec, httptest.NewRequest("GET", "/healthz", nil))

	var body map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode %q: %v", rec.Body.String(), err)
	}
	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", rec.Code)
	}
	if body["status"] != "ok" {
		t.Errorf("status field = %q, want ok", body["status"])
	}
	if body["build"] != BuildSHA {
		t.Errorf("build field = %q, want %q", body["build"], BuildSHA)
	}
}

// DBReset rides the same probe, but only where something set it: the gateway is
// the one binary that provisions, and the other seven services' /healthz bodies
// must stay exactly what they were. dev-env.yml's health-gate distinguishes
// "false" (a guard said no) from an absent field (a build too old to carry it),
// and reports them differently — so an empty value must not serialize as one.
func TestHealthzCarriesDBResetOnlyWhenSet(t *testing.T) {
	t.Cleanup(func(original string) func() {
		return func() { DBReset = original }
	}(DBReset))

	for _, c := range []struct {
		set       string
		wantField string
		wantOK    bool
	}{
		{"", "", false},
		{"true", "true", true},
		{"false", "false", true},
	} {
		DBReset = c.set

		rec := httptest.NewRecorder()
		healthzHandler(rec, httptest.NewRequest("GET", "/healthz", nil))

		var body map[string]string
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatalf("DBReset=%q: decode %q: %v", c.set, rec.Body.String(), err)
		}
		got, ok := body["db_reset"]
		if ok != c.wantOK {
			t.Errorf("DBReset=%q: db_reset present = %v, want %v (body %q)", c.set, ok, c.wantOK, rec.Body.String())
		}
		if got != c.wantField {
			t.Errorf("DBReset=%q: db_reset = %q, want %q", c.set, got, c.wantField)
		}
		if body["build"] != BuildSHA {
			t.Errorf("DBReset=%q: build field = %q, want %q — the existing gate must keep working", c.set, body["build"], BuildSHA)
		}
	}
}

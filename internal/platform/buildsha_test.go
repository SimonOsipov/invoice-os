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

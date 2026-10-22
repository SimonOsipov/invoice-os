package platform

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/getsentry/sentry-go"
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

// MockIssuer rides the same probe on the same terms: only the gateway sets it,
// and every other service's body must stay as it was.
func TestHealthzCarriesMockIssuerOnlyWhenSet(t *testing.T) {
	t.Cleanup(func(original string) func() {
		return func() { MockIssuer = original }
	}(MockIssuer))

	for _, c := range []struct {
		set       string
		wantField string
		wantOK    bool
	}{
		{"", "", false},
		{"absent", "absent", true},
		{"off", "off", true},
		{"on", "on", true},
	} {
		MockIssuer = c.set

		rec := httptest.NewRecorder()
		healthzHandler(rec, httptest.NewRequest("GET", "/healthz", nil))

		var body map[string]string
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatalf("MockIssuer=%q: decode %q: %v", c.set, rec.Body.String(), err)
		}
		got, ok := body["mock_issuer"]
		if ok != c.wantOK {
			t.Errorf("MockIssuer=%q: mock_issuer present = %v, want %v (body %q)", c.set, ok, c.wantOK, rec.Body.String())
		}
		if got != c.wantField {
			t.Errorf("MockIssuer=%q: mock_issuer = %q, want %q", c.set, got, c.wantField)
		}
		if body["build"] != BuildSHA {
			t.Errorf("MockIssuer=%q: build field = %q, want %q", c.set, body["build"], BuildSHA)
		}
	}
}

// Contacts rides the same probe on the same terms: only notifications sets it, and every
// other service's body must stay as it was.
func TestHealthzCarriesContactsOnlyWhenSet(t *testing.T) {
	t.Cleanup(func(original string) func() {
		return func() { Contacts = original }
	}(Contacts))

	for _, c := range []struct {
		set    string
		wantOK bool
	}{
		{"", false},
		{"off", true},
		{"fake", true},
		{"real", true},
	} {
		Contacts = c.set

		rec := httptest.NewRecorder()
		healthzHandler(rec, httptest.NewRequest("GET", "/healthz", nil))

		var body map[string]string
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatalf("Contacts=%q: decode %q: %v", c.set, rec.Body.String(), err)
		}
		got, ok := body["contacts"]
		if ok != c.wantOK {
			t.Errorf("Contacts=%q: contacts present = %v, want %v (body %q)", c.set, ok, c.wantOK, rec.Body.String())
		}
		if got != c.set {
			t.Errorf("Contacts=%q: contacts = %q, want %q", c.set, got, c.set)
		}
		if body["build"] != BuildSHA {
			t.Errorf("Contacts=%q: build field = %q, want %q", c.set, body["build"], BuildSHA)
		}
	}
}

// AuthIssuers is gateway-only, like MockIssuer.
func TestHealthzCarriesAuthIssuersOnlyWhenSet(t *testing.T) {
	t.Cleanup(func(original string) func() {
		return func() { AuthIssuers = original }
	}(AuthIssuers))

	for _, c := range []struct {
		set    string
		wantOK bool
	}{
		{"", false},
		{"1", true},
		{"2", true},
	} {
		AuthIssuers = c.set

		rec := httptest.NewRecorder()
		healthzHandler(rec, httptest.NewRequest("GET", "/healthz", nil))

		var body map[string]string
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatalf("AuthIssuers=%q: decode %q: %v", c.set, rec.Body.String(), err)
		}
		got, ok := body["auth_issuers"]
		if ok != c.wantOK {
			t.Errorf("AuthIssuers=%q: auth_issuers present = %v, want %v (body %q)", c.set, ok, c.wantOK, rec.Body.String())
		}
		if got != c.set {
			t.Errorf("AuthIssuers=%q: auth_issuers = %q, want %q", c.set, got, c.set)
		}
		if body["build"] != BuildSHA {
			t.Errorf("AuthIssuers=%q: build field = %q, want %q", c.set, body["build"], BuildSHA)
		}
	}
}

// TestHealthzBodyIsUnchangedOnAServiceThatNeverProvisions: eight of the nine
// fleet binaries never set the provisioning vars, so their /healthz carries only
// status, build and sentry. sentry is deliberate on every binary. The whole key
// set is asserted so any further field fails here.
func TestHealthzBodyIsUnchangedOnAServiceThatNeverProvisions(t *testing.T) {
	t.Cleanup(func(issuers, issuer string) func() {
		return func() { AuthIssuers, MockIssuer = issuers, issuer }
	}(AuthIssuers, MockIssuer))

	AuthIssuers, MockIssuer = "", ""
	sentry.CurrentHub().BindClient(nil)
	t.Cleanup(func() { sentry.CurrentHub().BindClient(nil) })

	rec := httptest.NewRecorder()
	healthzHandler(rec, httptest.NewRequest("GET", "/healthz", nil))

	var body map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode %q: %v", rec.Body.String(), err)
	}
	if len(body) == 0 {
		t.Fatalf("/healthz returned an empty object (%q); the comparison below would be vacuous", rec.Body.String())
	}

	want := map[string]string{"status": "ok", "build": BuildSHA, "sentry": "off"}
	if len(body) != len(want) {
		t.Errorf("/healthz carries %d field(s) %v on a non-provisioning service, want exactly %v", len(body), body, want)
	}
	for k, v := range want {
		if body[k] != v {
			t.Errorf("/healthz %q = %q, want %q", k, body[k], v)
		}
	}
}

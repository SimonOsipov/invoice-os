package validation

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
)

// refusalBody is the exact 403 body, flat envelope plus the encoder's newline.
const refusalBody = `{"error":"rules are managed by ASComply"}` + "\n"

// doToggle issues a PATCH /v1/rules/{key} through ToggleHandler, with
// r.PathValue("key") set directly (ServeHTTP is called without a mux).
func doToggle(t *testing.T, id *auth.Identity, key string, body io.Reader) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest("PATCH", "/v1/rules/"+key, body)
	r.SetPathValue("key", key)
	if id != nil {
		r = r.WithContext(auth.WithIdentity(r.Context(), *id))
	}
	rec := httptest.NewRecorder()
	ToggleHandler().ServeHTTP(rec, r)
	return rec
}

func tenantIdentity() *auth.Identity {
	return &auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: uuid.NewString()}
}

// assertRefusal asserts the uniform 403: status, exact body bytes, JSON type.
func assertRefusal(t *testing.T, rec *httptest.ResponseRecorder) {
	t.Helper()
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 (body=%q)", rec.Code, rec.Body.String())
	}
	if got := rec.Body.String(); got != refusalBody {
		t.Errorf("body = %q, want %q", got, refusalBody)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q, want %q", ct, "application/json")
	}
}

func TestToggle_RefusesACustomer403(t *testing.T) {
	rec := doToggle(t, tenantIdentity(), "vat-standard-rate", strings.NewReader(`{"enabled":false}`))
	assertRefusal(t, rec)

	var body map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response %q: %v", rec.Body.String(), err)
	}
	if len(body) != 1 || body["error"] != "rules are managed by ASComply" {
		t.Errorf("body = %v, want exactly {error: rules are managed by ASComply}", body)
	}
}

func TestToggle_RefusesEnableToo403(t *testing.T) {
	rec := doToggle(t, tenantIdentity(), "vat-standard-rate", strings.NewReader(`{"enabled":true}`))
	assertRefusal(t, rec)
}

// TestToggle_RefusalIsUniform: no key or body shape is an oracle. No 400, 404,
// 409 or 413 is reachable.
func TestToggle_RefusalIsUniform(t *testing.T) {
	cases := []struct {
		name, key, body string
	}{
		{"unknown key", "no-such-rule", `{"enabled":false}`},
		{"empty body", "vat-standard-rate", ``},
		{"empty object", "vat-standard-rate", `{}`},
		{"truncated JSON", "vat-standard-rate", `{"enabled":`},
		{"not JSON", "vat-standard-rate", `{not json`},
		{"2 MiB body", "vat-standard-rate", strings.Repeat("a", 2<<20)},
	}
	id := tenantIdentity()
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assertRefusal(t, doToggle(t, id, tc.key, strings.NewReader(tc.body)))
		})
	}
}

// readRecorder records whether Read was called.
type readRecorder struct{ reads int }

func (r *readRecorder) Read([]byte) (int, error) {
	r.reads++
	return 0, io.EOF
}

func TestToggle_DoesNotReadTheBody(t *testing.T) {
	body := &readRecorder{}
	assertRefusal(t, doToggle(t, tenantIdentity(), "vat-standard-rate", body))
	if body.reads != 0 {
		t.Errorf("body Read called %d times, want 0", body.reads)
	}
}

func TestToggle_NoIdentity401(t *testing.T) {
	rec := doToggle(t, nil, "vat-standard-rate", strings.NewReader(`{"enabled":false}`))

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401 (body=%q)", rec.Code, rec.Body.String())
	}
	if got, want := rec.Body.String(), `{"error":"unauthorized"}`+"\n"; got != want {
		t.Errorf("body = %q, want %q", got, want)
	}
}

// TestHandlers_ErrorEnvelopeShape: the 403 and the 401 are the flat
// {"error": "<msg>"} envelope, one key, application/json.
func TestHandlers_ErrorEnvelopeShape(t *testing.T) {
	t.Run("toggle 403", func(t *testing.T) {
		rec := doToggle(t, tenantIdentity(), "vat-standard-rate", strings.NewReader(`{"enabled":true}`))
		if rec.Code != http.StatusForbidden {
			t.Fatalf("status = %d, want 403", rec.Code)
		}
		assertFlatErrorEnvelope(t, rec)
	})
	t.Run("toggle 401", func(t *testing.T) {
		rec := doToggle(t, nil, "vat-standard-rate", strings.NewReader(`{"enabled":true}`))
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("status = %d, want 401", rec.Code)
		}
		assertFlatErrorEnvelope(t, rec)
	})
}

// assertFlatErrorEnvelope asserts rec's body decodes to a JSON object with
// EXACTLY one key ("error") holding a string, and that Content-Type is
// application/json.
func assertFlatErrorEnvelope(t *testing.T, rec *httptest.ResponseRecorder) {
	t.Helper()
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q, want %q", ct, "application/json")
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response %q: %v", rec.Body.String(), err)
	}
	if len(body) != 1 {
		t.Fatalf("body has %d keys, want exactly 1 (%q): %+v", len(body), "error", body)
	}
	msg, ok := body["error"].(string)
	if !ok || msg == "" {
		t.Errorf(`body["error"] = %#v, want a non-empty string`, body["error"])
	}
}

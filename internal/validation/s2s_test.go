// M4-04-03 (task-109, Test-first: yes) -- Mode A RED specs for
// S2SMiddleware (VB-03..VB-06): reads X-S2S-Token, constant-time compares
// it against the configured token, 401s BEFORE any body read on a
// missing/wrong token, and grants nothing on a bare X-Tenant-ID header
// ([s2s-identity]: "a peer's tenant assertion is never trusted"). See
// batch_qa_scaffold.go's file header for exactly which of these the
// current (QA Mode-A, temporary) scaffold gets wrong on purpose.
package validation

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const s2sTestToken = "correct-peer-token"

// readSpy wraps an io.Reader and records whether Read was ever called, so
// VB-03 can assert the request body is genuinely untouched on the 401 path
// (not merely that the response happens to be 401).
type readSpy struct {
	io.Reader
	read *bool
}

func (r *readSpy) Read(p []byte) (int, error) {
	*r.read = true
	return r.Reader.Read(p)
}

// doS2S drives S2SMiddleware(s2sTestToken) wrapping a trivial 200 inner
// handler, with the given X-S2S-Token/X-Tenant-ID headers (empty = header
// absent). bodyRead reports whether the request body was ever Read.
func doS2S(t *testing.T, headerToken, headerTenant string) (rec *httptest.ResponseRecorder, bodyRead bool) {
	t.Helper()
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	r := httptest.NewRequest("POST", "/v1/validate/batch", &readSpy{Reader: strings.NewReader(`{"invoices":[]}`), read: &bodyRead})
	if headerToken != "" {
		r.Header.Set("X-S2S-Token", headerToken)
	}
	if headerTenant != "" {
		r.Header.Set("X-Tenant-ID", headerTenant)
	}
	rec = httptest.NewRecorder()
	S2SMiddleware(s2sTestToken)(next).ServeHTTP(rec, r)
	return rec, bodyRead
}

// TestS2S_MissingToken401BodyNeverRead (VB-03): no X-S2S-Token -> 401, and
// the request body must never be read ([s2s-peer-auth]: "401 before any
// body read").
func TestS2S_MissingToken401BodyNeverRead(t *testing.T) {
	rec, bodyRead := doS2S(t, "", "")
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401 (body=%s)", rec.Code, rec.Body.String())
	}
	if bodyRead {
		t.Error("the request body was read before the 401 -- S2SMiddleware must check the token " +
			"BEFORE touching the body [s2s-peer-auth]")
	}
}

// TestS2S_WrongToken401 (VB-04): a wrong X-S2S-Token -> 401.
func TestS2S_WrongToken401(t *testing.T) {
	rec, _ := doS2S(t, "wrong-token", "")
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401 (body=%s)", rec.Code, rec.Body.String())
	}
}

// TestS2S_TenantHeaderAloneGrantsNothing401 (VB-05): X-Tenant-ID set, NO
// s2s token -> 401 -- a tenant header grants nothing ([s2s-identity]: "a
// peer's tenant assertion is never trusted").
func TestS2S_TenantHeaderAloneGrantsNothing401(t *testing.T) {
	rec, _ := doS2S(t, "", "some-tenant")
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401 -- an X-Tenant-ID header with no S2S token must grant "+
			"nothing [s2s-identity] (body=%s)", rec.Code, rec.Body.String())
	}
}

// TestS2S_ValidTokenNoTenant200 (VB-06): a correct X-S2S-Token with NO
// X-Tenant-ID -> 200 -- the endpoint needs no tenant.
func TestS2S_ValidTokenNoTenant200(t *testing.T) {
	rec, _ := doS2S(t, s2sTestToken, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 -- the endpoint needs no tenant (body=%s)", rec.Code, rec.Body.String())
	}
}

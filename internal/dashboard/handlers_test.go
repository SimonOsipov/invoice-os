// M4-07-03 (task-157): HTTP acceptance tests for RollupHandler -- written
// BEFORE the real handler logic exists (RED against handlers.go's
// not-implemented stub: RollupHandler currently always answers 501 "not
// implemented" without checking identity or calling the injected rollup
// closure, so every assertion below fails on its status/body value, not on a
// compile error). httptest + a fake rollup closure, no DB -- mirrors
// internal/invoice/handlers_test.go's doInvoiceCreate/doInvoiceGet idiom
// (net/http/httptest, auth.WithIdentity for identity injection, logger
// always passed literal nil).
//
// Spec-to-test map (Test Specs table, M4-07-03 story / task-157):
//
//	DASH-30 TestRollupHandler_200Body
//	DASH-31 TestRollupHandler_EmptyTenantSerializesAsArrays
//	DASH-32 TestRollupHandler_NoIdentity401
//	DASH-33 TestRollupHandler_ErrNoTenant401
//	DASH-34 TestRollupHandler_OpaqueStoreError500
//	DASH-35 TestRollupHandler_NilLoggerDoesNotPanic
package dashboard

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
	"github.com/SimonOsipov/invoice-os/internal/platform/db"
)

// rollupBody mirrors the Rollup JSON response shape, plus an Error field for
// the shared {"error":"..."} envelope -- same convention as
// internal/invoice/handlers_test.go's invoiceBody. Client/Bucket/Counts/
// RuleCount are the production types (this file is package dashboard, same
// as dashboard.go), reused directly rather than shadowed.
type rollupBody struct {
	Totals        Bucket      `json:"totals"`
	Clients       []Client    `json:"clients"`
	TopViolations []RuleCount `json:"top_violations"`
	Error         string      `json:"error"`
}

// doRollup issues GET /v1/rollup through RollupHandler with a fake rollup
// closure and an optional identity, decoding whatever JSON the handler
// writes. The logger is always literal nil -- the norm every existing
// handler test in this codebase already exercises (invoice/handlers_test.go,
// portfolio/portfolio_test.go), which is exactly what DASH-35 pins.
func doRollup(t *testing.T, rollup func(ctx context.Context) (Rollup, error), id *auth.Identity) (*httptest.ResponseRecorder, rollupBody) {
	t.Helper()
	r := httptest.NewRequest(http.MethodGet, "/v1/rollup", nil)
	if id != nil {
		r = r.WithContext(auth.WithIdentity(r.Context(), *id))
	}
	rec := httptest.NewRecorder()
	RollupHandler(rollup, nil).ServeHTTP(rec, r)
	var resp rollupBody
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response %q: %v", rec.Body.String(), err)
	}
	return rec, resp
}

// TestRollupHandler_200Body (DASH-30): a fake rollup returning 1 client with
// 2 broken drafts and 1 top violation must round-trip field-for-field,
// including all 7 state keys (even the zero ones) present in the raw body.
// Also pins the handler calls rollup exactly once (no double-query) and that
// Content-Type is application/json.
func TestRollupHandler_200Body(t *testing.T) {
	id := auth.Identity{Subject: "user-1", Role: "authenticated", TenantID: uuid.NewString()}
	want := Rollup{
		Totals: Bucket{
			Counts:         Counts{Draft: 2, Validated: 0, Queued: 0, Submitted: 0, Accepted: 0, Rejected: 0, Failed: 0},
			NeedsAttention: 2,
		},
		Clients: []Client{
			{
				EntityID:   uuid.NewString(),
				EntityName: "Dangote Cement PLC",
				Bucket: Bucket{
					Counts:         Counts{Draft: 2, Validated: 0, Queued: 0, Submitted: 0, Accepted: 0, Rejected: 0, Failed: 0},
					NeedsAttention: 2,
				},
			},
		},
		TopViolations: []RuleCount{{RuleKey: "supplier-tin-required", Invoices: 2}},
	}
	calls := 0
	rollup := func(ctx context.Context) (Rollup, error) {
		calls++
		return want, nil
	}
	rec, resp := doRollup(t, rollup, &id)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}
	if calls != 1 {
		t.Errorf("rollup called %d times, want exactly 1", calls)
	}
	if !json.Valid(rec.Body.Bytes()) {
		t.Fatalf("response body is not valid JSON: %s", rec.Body.String())
	}
	if !reflect.DeepEqual(resp.Totals, want.Totals) {
		t.Errorf("Totals = %+v, want %+v", resp.Totals, want.Totals)
	}
	if !reflect.DeepEqual(resp.Clients, want.Clients) {
		t.Errorf("Clients = %+v, want %+v", resp.Clients, want.Clients)
	}
	if !reflect.DeepEqual(resp.TopViolations, want.TopViolations) {
		t.Errorf("TopViolations = %+v, want %+v", resp.TopViolations, want.TopViolations)
	}

	raw := rec.Body.String()
	for _, key := range []string{
		`"draft":2`, `"validated":0`, `"queued":0`, `"submitted":0`,
		`"accepted":0`, `"rejected":0`, `"failed":0`,
	} {
		if !strings.Contains(raw, key) {
			t.Errorf("response body missing %s (all 7 state keys must be present, zeros included): %s", key, raw)
		}
	}
}

// TestRollupHandler_EmptyTenantSerializesAsArrays (DASH-31): a fake rollup
// returning zero clients and zero violations must serialize "clients":[] and
// "top_violations":[] in the raw body, never null.
func TestRollupHandler_EmptyTenantSerializesAsArrays(t *testing.T) {
	id := auth.Identity{Subject: "user-1", Role: "authenticated", TenantID: uuid.NewString()}
	rollup := func(ctx context.Context) (Rollup, error) {
		return Rollup{Clients: []Client{}, TopViolations: []RuleCount{}}, nil
	}
	rec, _ := doRollup(t, rollup, &id)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
	}
	raw := rec.Body.String()
	if !strings.Contains(raw, `"clients":[]`) {
		t.Errorf(`body missing "clients":[]: %s`, raw)
	}
	if !strings.Contains(raw, `"top_violations":[]`) {
		t.Errorf(`body missing "top_violations":[]: %s`, raw)
	}
	if strings.Contains(raw, `"clients":null`) || strings.Contains(raw, `"top_violations":null`) {
		t.Errorf("clients/top_violations serialized as null, not []: %s", raw)
	}
	if !json.Valid(rec.Body.Bytes()) {
		t.Fatalf("response body is not valid JSON: %s", raw)
	}
}

// TestRollupHandler_NoIdentity401 (DASH-32): no identity in the request
// context must 401 with the exact envelope, checked BEFORE rollup is ever
// invoked.
func TestRollupHandler_NoIdentity401(t *testing.T) {
	calls := 0
	rollup := func(ctx context.Context) (Rollup, error) {
		calls++
		t.Fatal("rollup must not run without an identity")
		return Rollup{}, nil
	}
	rec, resp := doRollup(t, rollup, nil)

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401 (body=%s)", rec.Code, rec.Body.String())
	}
	if got := strings.TrimSpace(rec.Body.String()); got != `{"error":"unauthorized"}` {
		t.Errorf(`body = %s, want exactly {"error":"unauthorized"}`, got)
	}
	if resp.Error != "unauthorized" {
		t.Errorf("decoded error = %q, want %q", resp.Error, "unauthorized")
	}
	if calls != 0 {
		t.Errorf("rollup called %d times, want 0 (must not be called without identity)", calls)
	}
	if !json.Valid(rec.Body.Bytes()) {
		t.Fatalf("response body is not valid JSON: %s", rec.Body.String())
	}
}

// TestRollupHandler_ErrNoTenant401 (DASH-33): a valid identity but the store
// returning db.ErrNoTenant must still 401 with the exact envelope.
func TestRollupHandler_ErrNoTenant401(t *testing.T) {
	id := auth.Identity{Subject: "user-1", Role: "authenticated", TenantID: uuid.NewString()}
	calls := 0
	rollup := func(ctx context.Context) (Rollup, error) {
		calls++
		return Rollup{}, db.ErrNoTenant
	}
	rec, resp := doRollup(t, rollup, &id)

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401 (body=%s)", rec.Code, rec.Body.String())
	}
	if got := strings.TrimSpace(rec.Body.String()); got != `{"error":"unauthorized"}` {
		t.Errorf(`body = %s, want exactly {"error":"unauthorized"}`, got)
	}
	if resp.Error != "unauthorized" {
		t.Errorf("decoded error = %q, want %q", resp.Error, "unauthorized")
	}
	if calls != 1 {
		t.Errorf("rollup called %d times, want exactly 1", calls)
	}
	if !json.Valid(rec.Body.Bytes()) {
		t.Fatalf("response body is not valid JSON: %s", rec.Body.String())
	}
}

// TestRollupHandler_OpaqueStoreError500 (DASH-34): any other store error
// must 500 with the generic envelope, and the raw internal error text must
// never reach the response body (information-leak check).
func TestRollupHandler_OpaqueStoreError500(t *testing.T) {
	id := auth.Identity{Subject: "user-1", Role: "authenticated", TenantID: uuid.NewString()}
	internalErr := errors.New(`pq: relation "secret_table" does not exist`)
	calls := 0
	rollup := func(ctx context.Context) (Rollup, error) {
		calls++
		return Rollup{}, internalErr
	}
	rec, resp := doRollup(t, rollup, &id)

	if rec.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500 (body=%s)", rec.Code, rec.Body.String())
	}
	raw := rec.Body.String()
	if got := strings.TrimSpace(raw); got != `{"error":"internal server error"}` {
		t.Errorf(`body = %s, want exactly {"error":"internal server error"}`, got)
	}
	if resp.Error != "internal server error" {
		t.Errorf("decoded error = %q, want %q", resp.Error, "internal server error")
	}
	if strings.Contains(raw, "secret") {
		t.Errorf("response body leaked internal error text: %s", raw)
	}
	if calls != 1 {
		t.Errorf("rollup called %d times, want exactly 1", calls)
	}
	if !json.Valid(rec.Body.Bytes()) {
		t.Fatalf("response body is not valid JSON: %s", raw)
	}
}

// TestRollupHandler_NilLoggerDoesNotPanic (DASH-35): RollupHandler(fake, nil)
// must not panic on any status path -- the shipped portfolio/invoice
// convention (`if log == nil { log = slog.Default() }`), which every other
// test in this file already exercises via doRollup's hardcoded nil logger.
// This test names that guarantee explicitly across all four paths.
func TestRollupHandler_NilLoggerDoesNotPanic(t *testing.T) {
	id := auth.Identity{Subject: "user-1", Role: "authenticated", TenantID: uuid.NewString()}
	cases := []struct {
		name   string
		id     *auth.Identity
		rollup func(ctx context.Context) (Rollup, error)
	}{
		{"200", &id, func(ctx context.Context) (Rollup, error) {
			return Rollup{Clients: []Client{}, TopViolations: []RuleCount{}}, nil
		}},
		{"401-no-identity", nil, func(ctx context.Context) (Rollup, error) {
			return Rollup{}, nil
		}},
		{"401-no-tenant", &id, func(ctx context.Context) (Rollup, error) {
			return Rollup{}, db.ErrNoTenant
		}},
		{"500", &id, func(ctx context.Context) (Rollup, error) {
			return Rollup{}, errors.New("boom")
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			defer func() {
				if r := recover(); r != nil {
					t.Fatalf("RollupHandler with nil logger panicked: %v", r)
				}
			}()
			rec, _ := doRollup(t, tc.rollup, tc.id)
			if !json.Valid(rec.Body.Bytes()) {
				t.Fatalf("response body is not valid JSON: %s", rec.Body.String())
			}
		})
	}
}

// M5-04-07 (task-231) Mode A (test-first): HTTP-layer specs for BatchSubmitHandler, written
// BEFORE the real handler logic exists (RED against handlers.go's not-implemented stub:
// BatchSubmitHandler currently always answers 501 "not implemented", ignoring the request
// and the injected submit closure entirely, so every assertion below fails on its status
// code / body shape, never a compile error). httptest + fake submit closures, no DB --
// mirrors handlers_test.go's doInvoiceCreate/do* idiom.
//
// Spec-to-test map:
//
//	T07-7 (bound half) TestBatchSubmitHandler_IdempotencyKeyLengthBound
//	T07-8              TestBatchSubmitHandler_RequestValidationTable
//	T07-9              TestBatchSubmitHandler_EmptyResultsMarshalsEmptyArrayNotNull
//
// T07-7's shape half (deriveBatchSubmitKey's own format) and T07-1..T07-6/T07-10..T07-12
// are DB-backed -- batch_submit_test.go.
package invoice

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
)

// batchSubmitRequestWire is the test-local mirror of handlers.go's own batchSubmitReq
// (same JSON shape, distinct Go type -- the established handlers_test.go convention, e.g.
// createInvoiceRequest vs createRequest).
type batchSubmitRequestWire struct {
	InvoiceIDs     []string `json:"invoice_ids"`
	IdempotencyKey string   `json:"idempotency_key"`
}

// batchSubmitResponseWire decodes BatchSubmitHandler's response body -- either the success
// shape (results) or the shared {"error":"..."} envelope.
type batchSubmitResponseWire struct {
	Results []BatchSubmitResultItem `json:"results"`
	Error   string                  `json:"error"`
}

func marshalBatchSubmit(t *testing.T, body batchSubmitRequestWire) string {
	t.Helper()
	b, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal batch submit request: %v", err)
	}
	return string(b)
}

// doBatchSubmit drives POST /v1/invoices/submissions against BatchSubmitHandler directly
// (mirrors doInvoiceCreate/doInvoiceTransition's identity-injection shape).
func doBatchSubmit(t *testing.T, submit func(ctx context.Context, in BatchSubmitInput) (BatchSubmitResult, error), id *auth.Identity, rawBody string) (*httptest.ResponseRecorder, batchSubmitResponseWire) {
	t.Helper()
	r := httptest.NewRequest(http.MethodPost, "/v1/invoices/submissions", strings.NewReader(rawBody))
	if id != nil {
		r = r.WithContext(auth.WithIdentity(r.Context(), *id))
	}
	rec := httptest.NewRecorder()
	BatchSubmitHandler(submit, nil).ServeHTTP(rec, r)
	var resp batchSubmitResponseWire
	_ = json.Unmarshal(rec.Body.Bytes(), &resp) // best-effort; some assertions read raw bytes instead
	return rec, resp
}

// nUUIDs returns n freshly generated, well-formed UUID strings.
func nUUIDs(n int) []string {
	ids := make([]string, n)
	for i := range ids {
		ids[i] = uuid.NewString()
	}
	return ids
}

// --- T07-8 -------------------------------------------------------------------

// TestBatchSubmitHandler_RequestValidationTable (T07-8): every case below must be rejected
// BEFORE submit is ever called -- empty invoice_ids -> 400; 201 ids (over the 200 cap) ->
// 400; blank idempotency_key -> 400; a non-uuid id -> 400; no identity -> 401.
func TestBatchSubmitHandler_RequestValidationTable(t *testing.T) {
	validID := uuid.NewString()

	tests := []struct {
		name       string
		identity   *auth.Identity
		body       batchSubmitRequestWire
		wantStatus int
	}{
		{
			name:       "empty invoice_ids",
			identity:   &auth.Identity{Subject: "user-1", Role: "authenticated", TenantID: uuid.NewString()},
			body:       batchSubmitRequestWire{InvoiceIDs: []string{}, IdempotencyKey: "key-1"},
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "201 ids exceeds the 200 cap",
			identity:   &auth.Identity{Subject: "user-1", Role: "authenticated", TenantID: uuid.NewString()},
			body:       batchSubmitRequestWire{InvoiceIDs: nUUIDs(201), IdempotencyKey: "key-1"},
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "blank idempotency_key",
			identity:   &auth.Identity{Subject: "user-1", Role: "authenticated", TenantID: uuid.NewString()},
			body:       batchSubmitRequestWire{InvoiceIDs: []string{validID}, IdempotencyKey: ""},
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "non-uuid invoice id",
			identity:   &auth.Identity{Subject: "user-1", Role: "authenticated", TenantID: uuid.NewString()},
			body:       batchSubmitRequestWire{InvoiceIDs: []string{"not-a-uuid"}, IdempotencyKey: "key-1"},
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "no identity",
			identity:   nil,
			body:       batchSubmitRequestWire{InvoiceIDs: []string{validID}, IdempotencyKey: "key-1"},
			wantStatus: http.StatusUnauthorized,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			submitCalled := false
			submit := func(ctx context.Context, in BatchSubmitInput) (BatchSubmitResult, error) {
				submitCalled = true
				return BatchSubmitResult{Results: []BatchSubmitResultItem{}}, nil
			}

			rec, resp := doBatchSubmit(t, submit, tt.identity, marshalBatchSubmit(t, tt.body))

			if rec.Code != tt.wantStatus {
				t.Errorf("status = %d, want %d (body=%s)", rec.Code, tt.wantStatus, rec.Body.String())
			}
			if resp.Error == "" {
				t.Errorf("body error = %q, want a non-empty {\"error\":...} message (body=%s)", resp.Error, rec.Body.String())
			}
			if submitCalled {
				t.Errorf("submit was called, want it NEVER reached for a rejected request")
			}
		})
	}
}

// --- T07-7 (bound half) -------------------------------------------------------

// TestBatchSubmitHandler_IdempotencyKeyLengthBound (T07-7, bound half): the idempotency_key
// bound is 218 chars, not the story's original "201-char" example -- task-231's Stage 1+2
// Implementation Notes correct this: idempotency_keys' CHECK is char_length <= 255
// (migrations/20260707193000_river_and_idempotency.sql:394), the derived key is
// "<request key>:<invoice id>" (1 colon + a 36-char uuid), so 255 - 1 - 36 = 218 is the
// precise maximum request-key length that always keeps the derived key within bound. A key
// at exactly 218 chars must be ACCEPTED (submit reached); one char over (219) must be
// REJECTED 400 before any write (submit never reached).
func TestBatchSubmitHandler_IdempotencyKeyLengthBound(t *testing.T) {
	validID := uuid.NewString()

	tests := []struct {
		name             string
		keyLen           int
		wantStatus       int
		wantSubmitCalled bool
	}{
		{name: "at the 218-char bound is accepted", keyLen: 218, wantStatus: http.StatusOK, wantSubmitCalled: true},
		{name: "219 chars (one over) is rejected 400 before any write", keyLen: 219, wantStatus: http.StatusBadRequest, wantSubmitCalled: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			key := strings.Repeat("k", tt.keyLen)
			submitCalled := false
			submit := func(ctx context.Context, in BatchSubmitInput) (BatchSubmitResult, error) {
				submitCalled = true
				return BatchSubmitResult{Results: []BatchSubmitResultItem{
					{InvoiceID: validID, Enqueued: true, Status: "queued"},
				}}, nil
			}
			identity := &auth.Identity{Subject: "user-1", Role: "authenticated", TenantID: uuid.NewString()}
			body := marshalBatchSubmit(t, batchSubmitRequestWire{InvoiceIDs: []string{validID}, IdempotencyKey: key})

			rec, _ := doBatchSubmit(t, submit, identity, body)

			if rec.Code != tt.wantStatus {
				t.Errorf("keyLen=%d: status = %d, want %d (body=%s)", tt.keyLen, rec.Code, tt.wantStatus, rec.Body.String())
			}
			if submitCalled != tt.wantSubmitCalled {
				t.Errorf("keyLen=%d: submit called = %v, want %v", tt.keyLen, submitCalled, tt.wantSubmitCalled)
			}
		})
	}
}

// --- T07-9 ---------------------------------------------------------------------

// TestBatchSubmitHandler_EmptyResultsMarshalsEmptyArrayNotNull (T07-9, AC-5): when submit
// returns a zero-enqueue BatchSubmitResult (an explicit non-nil, zero-length Results
// slice -- exactly what Stage 3's make([]BatchSubmitResultItem, 0, ...) builds), the
// response body must contain "results":[], never "results":null.
//
// Asserted on the RAW MARSHALLED BYTES (rec.Body), not on the decoded Go slice -- the exact
// M4-16 trap this story's Implementation Notes name: a nil []T is len 0 in Go but marshals
// to JSON null, and decoding null back into a Go slice re-nils it, silently hiding the
// defect from a decode-then-len-check assertion.
func TestBatchSubmitHandler_EmptyResultsMarshalsEmptyArrayNotNull(t *testing.T) {
	id := auth.Identity{Subject: "user-1", Role: "authenticated", TenantID: uuid.NewString()}
	submit := func(ctx context.Context, in BatchSubmitInput) (BatchSubmitResult, error) {
		return BatchSubmitResult{Results: make([]BatchSubmitResultItem, 0)}, nil
	}
	body := marshalBatchSubmit(t, batchSubmitRequestWire{InvoiceIDs: []string{uuid.NewString()}, IdempotencyKey: "key-1"})

	rec, _ := doBatchSubmit(t, submit, &id, body)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
	}
	raw := rec.Body.Bytes()
	if !bytes.Contains(raw, []byte(`"results":[]`)) {
		t.Errorf("body = %s, want raw JSON to contain \"results\":[]", raw)
	}
	if bytes.Contains(raw, []byte(`"results":null`)) {
		t.Errorf("body = %s, must NOT contain \"results\":null", raw)
	}
}

// --- CodeRabbit PR #92 (handlers.go:547) --------------------------------------

// TestBatchSubmitHandler_BodySizeCap: regression test for maxBatchSubmitBodyBytes'
// http.MaxBytesReader wrap. An over-cap body must be rejected 400 before it can be fully
// decoded (submit never reached); a full 200-id batch with a 218-char idempotency_key --
// the endpoint's own legitimate maximum, ~8.1 KB -- must still succeed comfortably inside
// the 64 KiB cap.
func TestBatchSubmitHandler_BodySizeCap(t *testing.T) {
	t.Run("body over the cap is rejected 400 before decode succeeds", func(t *testing.T) {
		submitCalled := false
		submit := func(ctx context.Context, in BatchSubmitInput) (BatchSubmitResult, error) {
			submitCalled = true
			return BatchSubmitResult{Results: []BatchSubmitResultItem{}}, nil
		}
		identity := &auth.Identity{Subject: "user-1", Role: "authenticated", TenantID: uuid.NewString()}
		oversizedKey := strings.Repeat("k", maxBatchSubmitBodyBytes+1)
		body := marshalBatchSubmit(t, batchSubmitRequestWire{InvoiceIDs: []string{uuid.NewString()}, IdempotencyKey: oversizedKey})

		rec, resp := doBatchSubmit(t, submit, identity, body)

		if rec.Code != http.StatusBadRequest {
			t.Errorf("status = %d, want 400 (body=%s)", rec.Code, rec.Body.String())
		}
		if resp.Error == "" {
			t.Errorf("body error = %q, want a non-empty {\"error\":...} message", resp.Error)
		}
		if submitCalled {
			t.Errorf("submit was called, want it NEVER reached for an over-cap body")
		}
	})

	t.Run("a full 200-id batch with a 218-char key, the legitimate maximum, still succeeds", func(t *testing.T) {
		ids := nUUIDs(maxBatchSubmitInvoiceIDs)
		results := make([]BatchSubmitResultItem, len(ids))
		for i, id := range ids {
			results[i] = BatchSubmitResultItem{InvoiceID: id, Enqueued: true, Status: "queued"}
		}
		submit := func(ctx context.Context, in BatchSubmitInput) (BatchSubmitResult, error) {
			return BatchSubmitResult{Results: results}, nil
		}
		identity := &auth.Identity{Subject: "user-1", Role: "authenticated", TenantID: uuid.NewString()}
		key := strings.Repeat("k", maxBatchSubmitIdempotencyKeyLen)
		body := marshalBatchSubmit(t, batchSubmitRequestWire{InvoiceIDs: ids, IdempotencyKey: key})

		rec, _ := doBatchSubmit(t, submit, identity, body)

		if rec.Code != http.StatusOK {
			t.Errorf("status = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
		}
	})
}

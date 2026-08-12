package approval

// task-491 (APPR-07-06, Mode B / QA phase): adversarial coverage beyond the Test Specs
// table decision_handlers_test.go transcribes, plus one mutation-testing gap found by
// hand: mutating DecideHandler to build the caller's identity from a body field instead
// of auth.Identity is invisible to every httptest.NewServer-with-record-string mock in
// decision_handlers_test.go, since none of them read ctx at all. Same harness, no new
// helpers except capturedIdentity below.
//
// Run with `go test -count=1 ./internal/approval/...` (pure, no DSN).

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
)

// TestApprovalHandler_SeamReceivesCallersIdentityUnaffectedByBodyActorFields closes a
// mutation-testing gap: TestApprovalHandler_ActorIsIdentityNotBody only inspects the
// decision/reason arguments the seam receives, never ctx, so a handler that swapped
// auth.Identity's Subject for a body-supplied "by" before building ctx would still pass
// that test. This mock reads ctx instead, so it can see the swap.
func TestApprovalHandler_SeamReceivesCallersIdentityUnaffectedByBodyActorFields(t *testing.T) {
	id := caller()
	var capturedSubject string
	decide := Decider(func(ctx context.Context, _, _, _ string) (Run, error) {
		if got, ok := auth.IdentityFromContext(ctx); ok {
			capturedSubject = got.Subject
		}
		return Run{RunID: decisionHandlerTestID, State: "approved"}, nil
	})
	body := `{"decision":"approved","by":"attacker","actor":"attacker"}`
	rec := serveDecision(t, decide, nil, "/v1/invoices/"+decisionHandlerTestID+"/approvals", body, &id)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	if capturedSubject != id.Subject {
		t.Errorf("ctx identity subject reaching the seam = %q, want the caller's own %q -- a body-supplied by/actor must never override it", capturedSubject, id.Subject)
	}
}

// TestApprovalHandler_NullDecisionValueIs400: JSON `null` for a string field leaves it
// at its zero value ("") rather than erroring the decode -- AC-4's "absent" case
// extended to the literal null spelling, which TestApprovalHandler_UnknownDecisionIs400
// never tries.
func TestApprovalHandler_NullDecisionValueIs400(t *testing.T) {
	id := caller()
	decide := failClosedDecide(t)
	rec := serveDecision(t, decide, nil, "/v1/invoices/"+decisionHandlerTestID+"/approvals", `{"decision":null}`, &id)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400: %s", rec.Code, rec.Body.String())
	}
	if got := errorMessage(t, rec.Body.Bytes()); got != `decision must be "approved" or "rejected"` {
		t.Errorf("error = %q, want %q", got, `decision must be "approved" or "rejected"`)
	}
}

// TestApprovalHandler_UnknownFieldsAreIgnored: json.Decode has no DisallowUnknownFields
// call anywhere in this handler (unlike a strict-decode API), so an extra top-level key
// or a nested object must not fail the decode or reach the seam.
func TestApprovalHandler_UnknownFieldsAreIgnored(t *testing.T) {
	id := caller()
	var capturedDecision, capturedReason string
	decide := Decider(func(_ context.Context, _, decision, reason string) (Run, error) {
		capturedDecision, capturedReason = decision, reason
		return Run{RunID: decisionHandlerTestID, State: "approved"}, nil
	})
	body := `{"decision":"approved","reason":"ok","actor_id":"x","metadata":{"a":1},"tags":[1,2,3]}`
	rec := serveDecision(t, decide, nil, "/v1/invoices/"+decisionHandlerTestID+"/approvals", body, &id)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (unknown fields must not break decode): %s", rec.Code, rec.Body.String())
	}
	if capturedDecision != "approved" || capturedReason != "ok" {
		t.Errorf("seam received (%q, %q), want (approved, ok)", capturedDecision, capturedReason)
	}
}

// TestApprovalHandler_JSONArrayBodyIs400: a top-level JSON array cannot decode into
// decisionRequest (a struct) -- json.Decoder returns a type error, which must land as
// the same "invalid request body" 400 as any other malformed body, not a panic or 500.
func TestApprovalHandler_JSONArrayBodyIs400(t *testing.T) {
	id := caller()
	decide := failClosedDecide(t)
	rec := serveDecision(t, decide, nil, "/v1/invoices/"+decisionHandlerTestID+"/approvals", `["approved","rejected"]`, &id)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400: %s", rec.Code, rec.Body.String())
	}
	if got := errorMessage(t, rec.Body.Bytes()); got != "invalid request body" {
		t.Errorf("error = %q, want %q", got, "invalid request body")
	}
}

// TestApprovalHandler_DuplicateDecisionKeyLastValueWins: encoding/json's documented
// last-value-wins rule for duplicate object keys, pinned at this handler's own decode
// call rather than assumed. Both decision AND reason repeat, so the test cannot pass by
// accident on whichever field happens to decode first.
func TestApprovalHandler_DuplicateDecisionKeyLastValueWins(t *testing.T) {
	id := caller()
	var capturedDecision, capturedReason string
	decide := Decider(func(_ context.Context, _, decision, reason string) (Run, error) {
		capturedDecision, capturedReason = decision, reason
		return Run{RunID: decisionHandlerTestID, State: "rejected"}, nil
	})
	body := `{"decision":"approved","reason":"first reason","decision":"rejected","reason":"second reason"}`
	rec := serveDecision(t, decide, nil, "/v1/invoices/"+decisionHandlerTestID+"/approvals", body, &id)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	if capturedDecision != "rejected" {
		t.Errorf("seam received decision = %q, want %q (the LAST decision key)", capturedDecision, "rejected")
	}
	if capturedReason != "second reason" {
		t.Errorf("seam received reason = %q, want %q (the LAST reason key)", capturedReason, "second reason")
	}
}

// TestApprovalHandler_ContentTypeIsNotEnforced: this handler's decode goes straight to
// json.NewDecoder(r.Body) with no Content-Type check anywhere in the call chain (unlike
// some frameworks' strict body parsers) -- a body that IS valid JSON must decode
// successfully whatever Content-Type says, or says nothing at all. Documents current
// behaviour; no AC requires the header be checked.
func TestApprovalHandler_ContentTypeIsNotEnforced(t *testing.T) {
	cases := []struct {
		name        string
		contentType string
		setHeader   bool
	}{
		{"wrong Content-Type (text/plain)", "text/plain", true},
		{"no Content-Type header at all", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			id := caller()
			var ran bool
			decide := Decider(func(context.Context, string, string, string) (Run, error) {
				ran = true
				return Run{RunID: decisionHandlerTestID, State: "approved"}, nil
			})
			r := httptest.NewRequest("POST", "/v1/invoices/"+decisionHandlerTestID+"/approvals", strings.NewReader(`{"decision":"approved"}`))
			if tc.setHeader {
				r.Header.Set("Content-Type", tc.contentType)
			}
			r = r.WithContext(auth.WithIdentity(r.Context(), id))
			rec := httptest.NewRecorder()
			decisionMux(decide, nil).ServeHTTP(rec, r)

			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200 (Content-Type is not enforced): %s", rec.Code, rec.Body.String())
			}
			if !ran {
				t.Fatal("the seam never ran")
			}
		})
	}
}

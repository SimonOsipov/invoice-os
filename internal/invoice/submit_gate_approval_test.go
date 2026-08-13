// APPR-08-05: can_submit's approval arm -- submitGate's third rung and GetHandler's
// third seam (approvalFacts).
package invoice

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
)

// wantAwaitingApprovalReason is handlers.go's awaitingApprovalReason, restated as a
// literal so a silent edit to the const cannot rewrite the oracle with it. Em dash
// is U+2014.
const wantAwaitingApprovalReason = "This invoice is waiting on approval — it can be submitted once an approver approves it."

// doInvoiceGetGated is the fourth GET wrapper: doInvoiceGetAs plus an explicit
// approvalFacts seam, which the other three hardwire to clearApprovalStub.
func doInvoiceGetGated(t *testing.T, get func(ctx context.Context, id string) (Invoice, error), callerRole func(ctx context.Context) (string, error), approvalFacts func(ctx context.Context, id string) (ApprovalFacts, error), id *auth.Identity, invoiceID string) (*httptest.ResponseRecorder, submitGateBody) {
	t.Helper()
	r := httptest.NewRequest(http.MethodGet, "/v1/invoices/"+invoiceID, nil)
	r.SetPathValue("id", invoiceID)
	if id != nil {
		r = r.WithContext(auth.WithIdentity(r.Context(), *id))
	}
	rec := httptest.NewRecorder()
	GetHandler(get, callerRole, approvalFacts, nil).ServeHTTP(rec, r)
	var resp submitGateBody
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response %q: %v", rec.Body.String(), err)
	}
	return rec, resp
}

// --- AC-4: submitGate's third rung -------------------------------------------

// TestSubmitGate_ApprovalArmOnlyFiresOnValidated: the delta against
// shippedSubmitGateOracle is exactly ONE row. A status that is not validated has no
// run to wait on, so the awaiting-approval sentence would be a lie there.
func TestSubmitGate_ApprovalArmOnlyFiresOnValidated(t *testing.T) {
	if len(shippedSubmitGateOracle) != len(allStatuses) {
		t.Fatalf("oracle covers %d statuses, want all %d", len(shippedSubmitGateOracle), len(allStatuses))
	}
	for _, role := range []string{"admin", "reviewer"} {
		for _, s := range allStatuses {
			t.Run(string(s)+"_"+role, func(t *testing.T) {
				w, ok := shippedSubmitGateOracle[s]
				if !ok {
					t.Fatalf("no oracle entry for status %q", s)
				}
				if s == StatusValidated {
					w = submitGateWant{false, wantAwaitingApprovalReason}
				}
				can, reason := submitGate(s, role, false)
				assertSubmitGateRow(t, w, can, reason)
			})
		}
	}
}

// TestSubmitGate_RoleStillWinsOverApproval: role is the FIRST rung, so a preparer
// reads the role sentence even when the run is open -- which is the refusal the
// doors actually answer (TransitionHandler/BatchSubmitHandler 403 on role before
// any row is read).
func TestSubmitGate_RoleStillWinsOverApproval(t *testing.T) {
	can, reason := submitGate(StatusValidated, "preparer", false)
	if can {
		t.Error("can = true for a preparer, want false")
	}
	if reason == nil {
		t.Fatalf("reason = nil, want %q", wantNotApproverTransmitReason)
	}
	if *reason != wantNotApproverTransmitReason {
		t.Errorf("reason = %q, want the role sentence %q", *reason, wantNotApproverTransmitReason)
	}
}

// TestSubmitGate_ClearValidatedApproverSubmits is the permissive control: a gate
// that always refuses cannot pass this and
// TestSubmitGate_BlockedValidatedApproverGetsTheSharedSentence both.
func TestSubmitGate_ClearValidatedApproverSubmits(t *testing.T) {
	can, reason := submitGate(StatusValidated, "admin", true)
	if !can {
		t.Error("can = false for an approver on a clear validated invoice, want true")
	}
	if reason != nil {
		t.Errorf("reason = %q, want nil", *reason)
	}
}

// TestSubmitGate_BlockedValidatedApproverGetsTheSharedSentence: the ONE sentence
// statusForErr's 409 arm already uses, so the button's reason and the refusal the
// caller would get read identically. NOT batchSubmitReasonAwaitingApproval, which
// is the machine code.
func TestSubmitGate_BlockedValidatedApproverGetsTheSharedSentence(t *testing.T) {
	can, reason := submitGate(StatusValidated, "admin", false)
	if can {
		t.Error("can = true for an approver on a blocked validated invoice, want false")
	}
	if reason == nil {
		t.Fatalf("reason = nil, want %q", wantAwaitingApprovalReason)
	}
	if *reason != wantAwaitingApprovalReason {
		t.Errorf("reason = %q, want %q", *reason, wantAwaitingApprovalReason)
	}
	if *reason == batchSubmitReasonAwaitingApproval {
		t.Errorf("reason = %q, the machine skip code -- the wire reason is a sentence", *reason)
	}
}

// TestSubmitGate_ApprovalArmCanOnlyNarrow: can==true still implies canEdit(s), the
// invariant TestCanSubmit_ImpliesCanEdit pins for the SPA actions-bar nesting.
func TestSubmitGate_ApprovalArmCanOnlyNarrow(t *testing.T) {
	for _, role := range []string{"admin", "reviewer"} {
		for _, s := range allStatuses {
			for _, clear := range []bool{true, false} {
				can, _ := submitGate(s, role, clear)
				if can && !canEdit(s) {
					t.Errorf("submitGate(%q, %q, %v) = true but canEdit(%q) = false -- the actions bar renders iff can_edit", s, role, clear, s)
				}
				if !clear && can {
					t.Errorf("submitGate(%q, %q, false) = true -- a blocked approval verdict may only narrow", s, role)
				}
			}
		}
	}
}

// --- AC-5: submitBlockedReason is untouched ----------------------------------

// TestSubmitBlockedReason_UnchangedByTheApprovalArm: same signature, same table,
// nil for both unknowns. The arm lives in submitGate, so this pure function of
// Status must not have grown a validated case.
func TestSubmitBlockedReason_UnchangedByTheApprovalArm(t *testing.T) {
	want := map[Status]string{
		StatusDraft:     exactSubmitBlockedReasonDraft,
		StatusValidated: "",
		StatusQueued:    "",
		StatusSubmitted: "",
		StatusAccepted:  "",
		StatusRejected:  exactSubmitBlockedReasonRejected,
		StatusFailed:    "",
	}
	if len(want) != len(allStatuses) {
		t.Fatalf("oracle covers %d statuses, want all %d", len(want), len(allStatuses))
	}
	for _, s := range allStatuses {
		got := submitBlockedReason(s)
		switch w := want[s]; {
		case w == "" && got != nil:
			t.Errorf("submitBlockedReason(%q) = %q, want nil", s, *got)
		case w != "" && got == nil:
			t.Errorf("submitBlockedReason(%q) = nil, want %q", s, w)
		case w != "" && got != nil && *got != w:
			t.Errorf("submitBlockedReason(%q) = %q, want %q", s, *got, w)
		}
	}
	for _, s := range []Status{"", "bogus-status"} {
		if got := submitBlockedReason(s); got != nil {
			t.Errorf("submitBlockedReason(%q) = %q, want nil", s, *got)
		}
	}
}

// --- AC-3: GetHandler's third seam -------------------------------------------

// TestGetHandler_ApprovalFactsErrorFailsClosedNot500: a seam error yields the zero
// ApprovalFacts (TransmitClear false) AND a log record, with the response still
// 200. Copies TestGetHandler_UnrenderableQRPayloadIsLogged's observable-logger
// idiom -- doInvoiceGetGated passes a nil logger, where an emission would be
// unobservable.
func TestGetHandler_ApprovalFactsErrorFailsClosedNot500(t *testing.T) {
	id := auth.Identity{Subject: "user-1", Role: "authenticated", TenantID: uuid.NewString()}
	invoiceID := uuid.NewString()

	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, nil))

	r := httptest.NewRequest(http.MethodGet, "/v1/invoices/"+invoiceID, nil)
	r.SetPathValue("id", invoiceID)
	r = r.WithContext(auth.WithIdentity(r.Context(), id))
	rec := httptest.NewRecorder()
	GetHandler(
		invoiceAtStatusStub(StatusValidated),
		fixedRoleStub("admin", nil),
		fixedApprovalStub(false, errors.New("approval read exploded")),
		logger,
	).ServeHTTP(rec, r)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 -- a failed approval read must never make an invoice unviewable (body=%s)", rec.Code, rec.Body.String())
	}
	var resp submitGateBody
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response %q: %v", rec.Body.String(), err)
	}
	// Logged FIRST and with t.Error, never t.Fatalf: the fail-closed half copies
	// callerRole's convention, but the LOGGING half has no callerRole precedent
	// (all three of those sites log nothing), so it must be independently
	// observable rather than masked by a body assertion above it.
	if buf.Len() == 0 {
		t.Error("expected the approval-read failure to be logged via the injected *slog.Logger, but the log buffer is empty")
	}
	if resp.CanSubmit {
		t.Error("can_submit = true after a seam error, want false -- the zero ApprovalFacts reads TransmitClear false")
	}
	switch {
	case resp.SubmitBlockedReason == nil:
		t.Errorf("submit_blocked_reason = null, want %q", wantAwaitingApprovalReason)
	case *resp.SubmitBlockedReason != wantAwaitingApprovalReason:
		t.Errorf("submit_blocked_reason = %q, want %q", *resp.SubmitBlockedReason, wantAwaitingApprovalReason)
	}
}

// TestGetHandler_ApprovalFactsResolvedOnEveryStatus: exactly one seam call per
// request whatever the status -- the TestGetHandler_CallerRoleResolvedOnEveryStatus
// precedent. A short-circuit on non-validated statuses would starve
// can_approve/can_reject (APPR-08-06). Those ARE status-gated -- approvalGate's
// rung 2 refuses anything but validated -- but the seam must still be resolved on
// every status: the wire has to carry an honest reason wherever the ladder stops,
// and a reordered ladder must not silently read a zero-valued fact.
func TestGetHandler_ApprovalFactsResolvedOnEveryStatus(t *testing.T) {
	for _, s := range allStatuses {
		t.Run(string(s), func(t *testing.T) {
			id := auth.Identity{Subject: "user-1", Role: "authenticated", TenantID: uuid.NewString()}
			calls := 0
			facts := func(ctx context.Context, gotID string) (ApprovalFacts, error) {
				calls++
				return ApprovalFacts{TransmitClear: true}, nil
			}
			rec, _ := doInvoiceGetGated(t, invoiceAtStatusStub(s), fixedRoleStub("admin", nil), facts, &id, uuid.NewString())
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
			}
			if calls != 1 {
				t.Errorf("approvalFacts called %d times on status %q, want exactly 1", calls, s)
			}
		})
	}
}

// TestGetHandler_ApprovalSeamKeyedOnTheFetchedRowId: Store.Get returns Postgres's
// canonical uuid text, so the seam must receive THAT, never r.PathValue -- the
// same trap Store.Transition guards with lockedID.
func TestGetHandler_ApprovalSeamKeyedOnTheFetchedRowId(t *testing.T) {
	id := auth.Identity{Subject: "user-1", Role: "authenticated", TenantID: uuid.NewString()}
	canonical := uuid.NewString()
	requested := "{" + strings.ToUpper(canonical) + "}"

	get := func(ctx context.Context, gotID string) (Invoice, error) {
		return Invoice{ID: canonical, Status: StatusValidated}, nil
	}
	var seen []string
	facts := func(ctx context.Context, gotID string) (ApprovalFacts, error) {
		seen = append(seen, gotID)
		return ApprovalFacts{TransmitClear: true}, nil
	}

	rec, _ := doInvoiceGetGated(t, get, fixedRoleStub("admin", nil), facts, &id, requested)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
	}
	if len(seen) != 1 {
		t.Fatalf("approvalFacts called %d times, want exactly 1", len(seen))
	}
	if seen[0] != canonical {
		t.Errorf("approvalFacts received %q, want the FETCHED row id %q -- keying on r.PathValue lets a braced or uppercase spelling read a different row, or none", seen[0], canonical)
	}
}

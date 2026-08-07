// RED, stub-backed tests for the resolved-outside wire layer -- written before
// ResolveOutsideHandler/UnresolveOutsideHandler/resolveOutsideGate have real
// bodies (handlers.go currently ships 501 stubs and a (false, nil) gate
// stub). httptest-level unit tests against injected store funcs, no DB.
//
// Test Specs map (architecture plan Section 7):
//
//	T4-1  TestResolveOutsideHandler_EmptyReason400
//	T4-2  TestResolveOutsideHandler_MissingReason400
//	T4-3  TestResolveOutsideHandler_OversizedReason400
//	T4-4  TestResolveOutsideHandler_ReasonIsTrimmed
//	T4-5  TestResolveOutsideHandler_NoIdentity401
//	T4-6  TestResolveOutsideHandler_ErrorStatusMap
//	T4-7  TestUnresolveOutsideHandler_NoBodyRequired
//	T4-8  TestGetHandler_ResolveOutsideFlagAllStatuses
//	T4-9  TestGetHandler_ResolveOutsideFalseForPreparer
//	T4-10 TestGetHandler_ResolveOutsideFalseNotOmitted
//	T4-11 TestGetHandler_ResolveOutsideKeysAppearExactlyOnce
//	T4-13 TestGetHandler_CallerRoleErrorFailsClosed
//	T4-14 TestGetHandler_ResolvedInvoiceStillCanResolve
//	T4-15 TestResolveOutsideGate_ReasonsAreNonEmpty
//
// T4-12 is the two pre-existing golden tests in handlers_test.go
// (TestGetHandler_ActionFlagsAdditiveKeepAllExistingKeys /
// TestGetHandler_ActionFlagKeysOrderedLast), extended there, not here.
package invoice

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
)

// resolveOutsideRequestBody mirrors the {"reason":"..."} wire shape
// ResolveOutsideHandler will decode (handlers.go:784-786's keepAsIsRequest
// shape) -- kept test-local since the executor stage owns the real request
// struct.
type resolveOutsideRequestBody struct {
	Reason string `json:"reason"`
}

// wantResolveOutsideStatusReason/wantResolveOutsideApproverReason are the
// exact copy resolveOutsideGate must emit (architecture plan Section 5) --
// the approver copy carries an em dash (U+2014) with single spaces, matching
// revalidateBlockedReason/submitBlockedReason's own convention.
const (
	wantResolveOutsideStatusReason   = "Only a failed invoice can be marked resolved outside the system."
	wantResolveOutsideApproverReason = "Only an approver can mark an invoice resolved outside the system — ask an admin or a reviewer on your team."
)

// doInvoiceResolveOutside drives POST /v1/invoices/{id}/resolved-outside
// through the real handler -- cloned from doInvoiceKeepAsIs's exact shape
// (kept_as_is_test.go), reusing its keepAsIsResponseBody decode target since
// both write the same kept_as_is_at/by/reason triple.
func doInvoiceResolveOutside(t *testing.T, resolve func(ctx context.Context, id, reason string) (Invoice, error), id *auth.Identity, invoiceID, rawBody string) (*httptest.ResponseRecorder, keepAsIsResponseBody) {
	t.Helper()
	r := httptest.NewRequest(http.MethodPost, "/v1/invoices/"+invoiceID+"/resolved-outside", strings.NewReader(rawBody))
	r.SetPathValue("id", invoiceID)
	if id != nil {
		r = r.WithContext(auth.WithIdentity(r.Context(), *id))
	}
	rec := httptest.NewRecorder()
	ResolveOutsideHandler(resolve, nil).ServeHTTP(rec, r)
	var resp keepAsIsResponseBody
	if len(rec.Body.Bytes()) > 0 {
		if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
			t.Fatalf("decode response %q: %v", rec.Body.String(), err)
		}
	}
	return rec, resp
}

// doInvoiceUnresolveOutside is doInvoiceResolveOutside's DELETE sibling --
// no body to decode, mirroring doInvoiceUnkeepAsIs's shape.
func doInvoiceUnresolveOutside(t *testing.T, unresolve func(ctx context.Context, id string) (Invoice, error), id *auth.Identity, invoiceID string) (*httptest.ResponseRecorder, keepAsIsResponseBody) {
	t.Helper()
	r := httptest.NewRequest(http.MethodDelete, "/v1/invoices/"+invoiceID+"/resolved-outside", nil)
	r.SetPathValue("id", invoiceID)
	if id != nil {
		r = r.WithContext(auth.WithIdentity(r.Context(), *id))
	}
	rec := httptest.NewRecorder()
	UnresolveOutsideHandler(unresolve, nil).ServeHTTP(rec, r)
	var resp keepAsIsResponseBody
	if len(rec.Body.Bytes()) > 0 {
		if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
			t.Fatalf("decode response %q: %v", rec.Body.String(), err)
		}
	}
	return rec, resp
}

// getWithRoleBody is this file's own wire mirror for GET /v1/invoices/{id} --
// decodes only the fields the gate tests below need, mirroring
// keepAsIsResponseBody's "test-local subset" convention.
type getWithRoleBody struct {
	Status                      string  `json:"status"`
	CanResolveOutside           bool    `json:"can_resolve_outside"`
	ResolveOutsideBlockedReason *string `json:"resolve_outside_blocked_reason"`
}

// doInvoiceGetWithRole drives GET /v1/invoices/{id} with an explicit
// callerRole stub -- doInvoiceGet (handlers_test.go) always injects
// adminRoleStub, so the gate-precedence tests below need their own variant.
func doInvoiceGetWithRole(t *testing.T, get func(ctx context.Context, id string) (Invoice, error), callerRole func(ctx context.Context) (string, error), id *auth.Identity, invoiceID string) (*httptest.ResponseRecorder, getWithRoleBody) {
	t.Helper()
	r := httptest.NewRequest(http.MethodGet, "/v1/invoices/"+invoiceID, nil)
	r.SetPathValue("id", invoiceID)
	if id != nil {
		r = r.WithContext(auth.WithIdentity(r.Context(), *id))
	}
	rec := httptest.NewRecorder()
	GetHandler(get, callerRole, nil).ServeHTTP(rec, r)
	var resp getWithRoleBody
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response %q: %v", rec.Body.String(), err)
	}
	return rec, resp
}

// --- T4-1..T4-6: ResolveOutsideHandler's pre-store guards --------------------

// TestResolveOutsideHandler_EmptyReason400 (T4-1): a whitespace-only reason
// must 400 before resolve is ever called.
func TestResolveOutsideHandler_EmptyReason400(t *testing.T) {
	invoiceID := uuid.NewString()
	id := auth.Identity{Subject: "user-1", Role: "authenticated", TenantID: uuid.NewString()}
	called := false
	resolve := func(ctx context.Context, gotID, reason string) (Invoice, error) {
		called = true
		return Invoice{}, nil
	}
	body, err := json.Marshal(resolveOutsideRequestBody{Reason: "   "})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	rec, resp := doInvoiceResolveOutside(t, resolve, &id, invoiceID, string(body))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (body=%s)", rec.Code, rec.Body.String())
	}
	if resp.Error == "" {
		t.Error("expected a non-empty error message")
	}
	if called {
		t.Error("resolve must not be called for a whitespace-only reason")
	}
}

// TestResolveOutsideHandler_MissingReason400 (T4-2): an omitted reason field
// (decodes to "") must 400 the same as whitespace-only.
func TestResolveOutsideHandler_MissingReason400(t *testing.T) {
	invoiceID := uuid.NewString()
	id := auth.Identity{Subject: "user-1", Role: "authenticated", TenantID: uuid.NewString()}
	called := false
	resolve := func(ctx context.Context, gotID, reason string) (Invoice, error) {
		called = true
		return Invoice{}, nil
	}
	rec, resp := doInvoiceResolveOutside(t, resolve, &id, invoiceID, `{}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (body=%s)", rec.Code, rec.Body.String())
	}
	if resp.Error == "" {
		t.Error("expected a non-empty error message")
	}
	if called {
		t.Error("resolve must not be called when reason is missing")
	}
}

// TestResolveOutsideHandler_OversizedReason400 (T4-3): 1001 chars (one over
// maxKeepAsIsReasonLen) is the first invalid length; store never called.
func TestResolveOutsideHandler_OversizedReason400(t *testing.T) {
	invoiceID := uuid.NewString()
	id := auth.Identity{Subject: "user-1", Role: "authenticated", TenantID: uuid.NewString()}
	called := false
	resolve := func(ctx context.Context, gotID, reason string) (Invoice, error) {
		called = true
		return Invoice{}, nil
	}
	body, err := json.Marshal(resolveOutsideRequestBody{Reason: strings.Repeat("a", maxKeepAsIsReasonLen+1)})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	rec, resp := doInvoiceResolveOutside(t, resolve, &id, invoiceID, string(body))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (body=%s)", rec.Code, rec.Body.String())
	}
	if resp.Error == "" {
		t.Error("expected a non-empty error message")
	}
	if called {
		t.Error("resolve must not be called for an oversized reason")
	}
}

// TestResolveOutsideHandler_ReasonIsTrimmed (T4-4): leading/trailing
// whitespace must be stripped before resolve is called.
func TestResolveOutsideHandler_ReasonIsTrimmed(t *testing.T) {
	invoiceID := uuid.NewString()
	id := auth.Identity{Subject: "user-1", Role: "authenticated", TenantID: uuid.NewString()}
	called := false
	var gotReason string
	resolve := func(ctx context.Context, gotID, reason string) (Invoice, error) {
		called = true
		gotReason = reason
		return Invoice{ID: gotID, Status: StatusFailed}, nil
	}
	body, err := json.Marshal(resolveOutsideRequestBody{Reason: "  filed  "})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	rec, _ := doInvoiceResolveOutside(t, resolve, &id, invoiceID, string(body))
	if !called {
		t.Fatalf("resolve was never called (status=%d, body=%s), want it called with the trimmed reason", rec.Code, rec.Body.String())
	}
	if gotReason != "filed" {
		t.Errorf("resolve received reason = %q, want the trimmed %q", gotReason, "filed")
	}
}

// TestResolveOutsideHandler_NoIdentity401 (T4-5): identity-first-401, same
// order as every other handler in this package.
func TestResolveOutsideHandler_NoIdentity401(t *testing.T) {
	invoiceID := uuid.NewString()
	resolve := func(ctx context.Context, gotID, reason string) (Invoice, error) {
		t.Fatal("resolve must not be called when identity is absent")
		return Invoice{}, nil
	}
	body, err := json.Marshal(resolveOutsideRequestBody{Reason: "irrelevant"})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	rec, resp := doInvoiceResolveOutside(t, resolve, nil, invoiceID, string(body))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401 (body=%s)", rec.Code, rec.Body.String())
	}
	if resp.Error == "" {
		t.Error("expected a non-empty error message")
	}
}

// TestResolveOutsideHandler_ErrorStatusMap (T4-6): each store sentinel maps
// to its statusForErr status.
func TestResolveOutsideHandler_ErrorStatusMap(t *testing.T) {
	id := auth.Identity{Subject: "user-1", Role: "authenticated", TenantID: uuid.NewString()}
	tests := []struct {
		name string
		err  error
		want int
	}{
		{"not_permitted", ErrNotPermitted, http.StatusForbidden},
		{"not_resolvable", ErrNotResolvable, http.StatusConflict},
		{"not_found", ErrNotFound, http.StatusNotFound},
		{"validation", ErrValidation, http.StatusBadRequest},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			invoiceID := uuid.NewString()
			resolve := func(ctx context.Context, gotID, reason string) (Invoice, error) {
				return Invoice{}, tt.err
			}
			body, err := json.Marshal(resolveOutsideRequestBody{Reason: "valid reason"})
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			rec, resp := doInvoiceResolveOutside(t, resolve, &id, invoiceID, string(body))
			if rec.Code != tt.want {
				t.Errorf("err=%v: status = %d, want %d (body=%s)", tt.err, rec.Code, tt.want, rec.Body.String())
			}
			if resp.Error == "" {
				t.Errorf("err=%v: expected a non-empty error message", tt.err)
			}
		})
	}
}

// --- T4-7: UnresolveOutsideHandler --------------------------------------------

// TestUnresolveOutsideHandler_NoBodyRequired (T4-7): DELETE with no body
// succeeds and returns the cleared invoice.
func TestUnresolveOutsideHandler_NoBodyRequired(t *testing.T) {
	invoiceID := uuid.NewString()
	id := auth.Identity{Subject: "user-1", Role: "authenticated", TenantID: uuid.NewString()}
	want := Invoice{ID: invoiceID, Status: StatusFailed}
	unresolve := func(ctx context.Context, gotID string) (Invoice, error) {
		return want, nil
	}
	rec, resp := doInvoiceUnresolveOutside(t, unresolve, &id, invoiceID)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
	}
	if resp.Status != string(StatusFailed) {
		t.Errorf("status field = %q, want %q", resp.Status, StatusFailed)
	}
}

// --- T4-8/T4-9: GetHandler's resolve-outside flag, and gate precedence ------

// TestGetHandler_ResolveOutsideFlagAllStatuses (T4-8): for an approver,
// can_resolve_outside is true ONLY on failed; every other status carries the
// exact status-blocked copy.
func TestGetHandler_ResolveOutsideFlagAllStatuses(t *testing.T) {
	id := auth.Identity{Subject: "user-1", Role: "authenticated", TenantID: uuid.NewString()}
	adminRole := func(ctx context.Context) (string, error) { return "admin", nil }
	statuses := []Status{StatusDraft, StatusValidated, StatusQueued, StatusSubmitted, StatusAccepted, StatusRejected, StatusFailed}
	for _, s := range statuses {
		t.Run(string(s), func(t *testing.T) {
			invoiceID := uuid.NewString()
			get := func(ctx context.Context, gotID string) (Invoice, error) {
				return Invoice{ID: gotID, Status: s}, nil
			}
			_, resp := doInvoiceGetWithRole(t, get, adminRole, &id, invoiceID)
			wantCan := s == StatusFailed
			if resp.CanResolveOutside != wantCan {
				t.Errorf("can_resolve_outside = %v, want %v", resp.CanResolveOutside, wantCan)
			}
			if wantCan {
				if resp.ResolveOutsideBlockedReason != nil {
					t.Errorf("reason = %q, want nil for an approver on a failed invoice", *resp.ResolveOutsideBlockedReason)
				}
				return
			}
			if resp.ResolveOutsideBlockedReason == nil {
				t.Fatal("reason = nil, want the status-blocked copy")
			}
			if got := *resp.ResolveOutsideBlockedReason; got != wantResolveOutsideStatusReason {
				t.Errorf("reason = %q, want %q", got, wantResolveOutsideStatusReason)
			}
		})
	}
}

// TestGetHandler_ResolveOutsideFalseForPreparer (T4-9): pins the
// status-first-then-role gate precedence. A preparer (non-approver) on a
// failed invoice gets the approver-required copy (status passes, role
// fails); the SAME preparer on a draft gets the status copy, NOT the
// approver copy -- status is checked first, so an irrelevant-action reason
// never leaks through for a non-approver on a non-failed invoice.
func TestGetHandler_ResolveOutsideFalseForPreparer(t *testing.T) {
	id := auth.Identity{Subject: "user-1", Role: "authenticated", TenantID: uuid.NewString()}
	preparerRole := func(ctx context.Context) (string, error) { return "preparer", nil }

	tests := []struct {
		name   string
		status Status
		want   string
	}{
		{"failed_gets_approver_reason", StatusFailed, wantResolveOutsideApproverReason},
		{"draft_gets_status_reason_not_approver", StatusDraft, wantResolveOutsideStatusReason},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			invoiceID := uuid.NewString()
			get := func(ctx context.Context, gotID string) (Invoice, error) {
				return Invoice{ID: gotID, Status: tt.status}, nil
			}
			_, resp := doInvoiceGetWithRole(t, get, preparerRole, &id, invoiceID)
			if resp.CanResolveOutside {
				t.Error("can_resolve_outside = true, want false for a preparer")
			}
			if resp.ResolveOutsideBlockedReason == nil {
				t.Fatal("reason = nil, want a non-nil reason")
			}
			if got := *resp.ResolveOutsideBlockedReason; got != tt.want {
				t.Errorf("reason = %q, want %q", got, tt.want)
			}
		})
	}
}

// --- T4-10/T4-11: additive wire shape ----------------------------------------

// TestGetHandler_ResolveOutsideFalseNotOmitted (T4-10): a false
// can_resolve_outside must marshal as the literal key, never be omitted.
func TestGetHandler_ResolveOutsideFalseNotOmitted(t *testing.T) {
	invoiceID := uuid.NewString()
	id := auth.Identity{Subject: "user-1", Role: "authenticated", TenantID: uuid.NewString()}
	get := func(ctx context.Context, gotID string) (Invoice, error) {
		return Invoice{ID: gotID, Status: StatusDraft}, nil
	}
	callerRole := func(ctx context.Context) (string, error) { return "admin", nil }
	rec, _ := doInvoiceGetWithRole(t, get, callerRole, &id, invoiceID)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"can_resolve_outside":false`) {
		t.Errorf("body = %s, want the literal \"can_resolve_outside\":false present (never omitted)", rec.Body.String())
	}
}

// TestGetHandler_ResolveOutsideKeysAppearExactlyOnce (T4-11): both new keys
// occur exactly once on the wire.
func TestGetHandler_ResolveOutsideKeysAppearExactlyOnce(t *testing.T) {
	invoiceID := uuid.NewString()
	id := auth.Identity{Subject: "user-1", Role: "authenticated", TenantID: uuid.NewString()}
	get := func(ctx context.Context, gotID string) (Invoice, error) {
		return Invoice{ID: gotID, Status: StatusFailed}, nil
	}
	callerRole := func(ctx context.Context) (string, error) { return "admin", nil }
	rec, _ := doInvoiceGetWithRole(t, get, callerRole, &id, invoiceID)
	for _, key := range []string{`"can_resolve_outside"`, `"resolve_outside_blocked_reason"`} {
		if n := strings.Count(rec.Body.String(), key); n != 1 {
			t.Errorf("key %s appears %d times in body, want exactly 1 (body=%s)", key, n, rec.Body.String())
		}
	}
}

// --- T4-13/T4-14: fail-closed and re-resolve edge cases ----------------------

// TestGetHandler_CallerRoleErrorFailsClosed (T4-13): callerRole erroring
// must not 5xx the GET -- fail closed, same as Store.CallerRole's own
// "return empty string, never an error" contract, but the handler must not
// assume an injected func honors it.
func TestGetHandler_CallerRoleErrorFailsClosed(t *testing.T) {
	invoiceID := uuid.NewString()
	id := auth.Identity{Subject: "user-1", Role: "authenticated", TenantID: uuid.NewString()}
	get := func(ctx context.Context, gotID string) (Invoice, error) {
		return Invoice{ID: gotID, Status: StatusFailed}, nil
	}
	erroringRole := func(ctx context.Context) (string, error) { return "", errors.New("membership lookup failed") }
	rec, resp := doInvoiceGetWithRole(t, get, erroringRole, &id, invoiceID)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 even when callerRole errors (body=%s)", rec.Code, rec.Body.String())
	}
	if resp.CanResolveOutside {
		t.Error("can_resolve_outside = true, want false when callerRole errors (fail closed)")
	}
	if resp.ResolveOutsideBlockedReason == nil {
		t.Error("reason = nil, want a non-nil reason when callerRole errors")
	}
}

// TestGetHandler_ResolvedInvoiceStillCanResolve (T4-14): re-resolving is
// legal, so an already-resolved failed invoice must still report
// can_resolve_outside:true for an approver -- the flag does not go false
// once resolved.
func TestGetHandler_ResolvedInvoiceStillCanResolve(t *testing.T) {
	invoiceID := uuid.NewString()
	id := auth.Identity{Subject: "user-1", Role: "authenticated", TenantID: uuid.NewString()}
	resolvedAt := time.Now().UTC()
	resolvedBy := "someone"
	resolvedReason := "already resolved outside"
	get := func(ctx context.Context, gotID string) (Invoice, error) {
		return Invoice{
			ID: gotID, Status: StatusFailed,
			KeptAsIsAt: &resolvedAt, KeptAsIsBy: &resolvedBy, KeptAsIsReason: &resolvedReason,
		}, nil
	}
	adminRole := func(ctx context.Context) (string, error) { return "admin", nil }
	_, resp := doInvoiceGetWithRole(t, get, adminRole, &id, invoiceID)
	if !resp.CanResolveOutside {
		t.Error("can_resolve_outside = false, want true -- re-resolving an already-resolved failed invoice is legal")
	}
	if resp.ResolveOutsideBlockedReason != nil {
		t.Errorf("reason = %q, want nil -- an approver on a failed invoice is never blocked, resolved or not", *resp.ResolveOutsideBlockedReason)
	}
}

// TestGetHandler_CallerRoleSkippedWhenNotFailed pins the perf fix (CodeRabbit
// review finding 4): callerRole's own tenant-scoped query only runs when the
// invoice is failed -- resolveOutsideGate is status-first and never consults
// role otherwise.
func TestGetHandler_CallerRoleSkippedWhenNotFailed(t *testing.T) {
	id := auth.Identity{Subject: "user-1", Role: "authenticated", TenantID: uuid.NewString()}
	statuses := []Status{StatusDraft, StatusValidated, StatusQueued, StatusSubmitted, StatusAccepted, StatusRejected}
	for _, s := range statuses {
		t.Run(string(s), func(t *testing.T) {
			invoiceID := uuid.NewString()
			get := func(ctx context.Context, gotID string) (Invoice, error) {
				return Invoice{ID: gotID, Status: s}, nil
			}
			calls := 0
			role := func(ctx context.Context) (string, error) {
				calls++
				return "admin", nil
			}
			doInvoiceGetWithRole(t, get, role, &id, invoiceID)
			if calls != 0 {
				t.Errorf("callerRole called %d times for status %q, want 0", calls, s)
			}
		})
	}
}

// TestGetHandler_CallerRoleCalledOnceWhenFailed pins the other half: on a
// failed invoice, callerRole is still called -- exactly once.
func TestGetHandler_CallerRoleCalledOnceWhenFailed(t *testing.T) {
	invoiceID := uuid.NewString()
	id := auth.Identity{Subject: "user-1", Role: "authenticated", TenantID: uuid.NewString()}
	get := func(ctx context.Context, gotID string) (Invoice, error) {
		return Invoice{ID: gotID, Status: StatusFailed}, nil
	}
	calls := 0
	role := func(ctx context.Context) (string, error) {
		calls++
		return "admin", nil
	}
	doInvoiceGetWithRole(t, get, role, &id, invoiceID)
	if calls != 1 {
		t.Errorf("callerRole called %d times for a failed invoice, want 1", calls)
	}
}

// --- T4-15: the gate's own invariant -----------------------------------------

// TestResolveOutsideGate_ReasonsAreNonEmpty (T4-15): whenever the gate
// returns false, the reason must be a non-nil, non-empty string -- the
// default arm rule submitBlockedReason already follows (handlers.go:249).
func TestResolveOutsideGate_ReasonsAreNonEmpty(t *testing.T) {
	statuses := []Status{StatusDraft, StatusValidated, StatusQueued, StatusSubmitted, StatusAccepted, StatusRejected, StatusFailed}
	roles := []string{"admin", "reviewer", "preparer", "viewer", ""}
	for _, s := range statuses {
		for _, role := range roles {
			t.Run(string(s)+"_"+role, func(t *testing.T) {
				can, reason := resolveOutsideGate(s, role)
				if can {
					if reason != nil {
						t.Errorf("can=true but reason=%q, want nil when the action is allowed", *reason)
					}
					return
				}
				if reason == nil {
					t.Fatal("can=false but reason=nil, want a non-nil server-authored reason")
				}
				if strings.TrimSpace(*reason) == "" {
					t.Error("reason is empty/whitespace-only, want real copy")
				}
			})
		}
	}
}

// --- QA adversarial: boundary length, DELETE body tolerance, unicode, exact copy --

// TestResolveOutsideHandler_ReasonExactly1000CharsValid pins the boundary
// paired with T4-3's 1001-char rejection: exactly maxKeepAsIsReasonLen must
// pass through unrejected and unmodified.
func TestResolveOutsideHandler_ReasonExactly1000CharsValid(t *testing.T) {
	invoiceID := uuid.NewString()
	id := auth.Identity{Subject: "user-1", Role: "authenticated", TenantID: uuid.NewString()}
	var gotReason string
	resolve := func(ctx context.Context, gotID, reason string) (Invoice, error) {
		gotReason = reason
		return Invoice{ID: gotID, Status: StatusFailed}, nil
	}
	body, err := json.Marshal(resolveOutsideRequestBody{Reason: strings.Repeat("a", maxKeepAsIsReasonLen)})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	rec, _ := doInvoiceResolveOutside(t, resolve, &id, invoiceID, string(body))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 for a reason at exactly the %d-char bound (body=%s)", rec.Code, maxKeepAsIsReasonLen, rec.Body.String())
	}
	if len(gotReason) != maxKeepAsIsReasonLen {
		t.Errorf("resolve received a %d-char reason, want the full %d", len(gotReason), maxKeepAsIsReasonLen)
	}
}

// TestUnresolveOutsideHandler_BodyPresentIgnored: DELETE decodes no body, so
// a client that sends one anyway must not be rejected or change the outcome.
func TestUnresolveOutsideHandler_BodyPresentIgnored(t *testing.T) {
	invoiceID := uuid.NewString()
	id := auth.Identity{Subject: "user-1", Role: "authenticated", TenantID: uuid.NewString()}
	want := Invoice{ID: invoiceID, Status: StatusFailed}
	unresolve := func(ctx context.Context, gotID string) (Invoice, error) { return want, nil }

	r := httptest.NewRequest(http.MethodDelete, "/v1/invoices/"+invoiceID+"/resolved-outside", strings.NewReader(`{"reason":"unexpected but present"}`))
	r.SetPathValue("id", invoiceID)
	r = r.WithContext(auth.WithIdentity(r.Context(), id))
	rec := httptest.NewRecorder()
	UnresolveOutsideHandler(unresolve, nil).ServeHTTP(rec, r)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 even with a body present (body=%s)", rec.Code, rec.Body.String())
	}
}

// TestResolveOutsideHandler_UnicodeReasonRoundTrips: a reason carrying a
// multi-byte em dash must decode and trim byte-for-byte -- a naive
// byte-slice trim (rather than strings.TrimSpace's rune-aware one) would
// corrupt it.
func TestResolveOutsideHandler_UnicodeReasonRoundTrips(t *testing.T) {
	invoiceID := uuid.NewString()
	id := auth.Identity{Subject: "user-1", Role: "authenticated", TenantID: uuid.NewString()}
	const want = "resolved by ops — see ticket OPS-4821"
	var gotReason string
	resolve := func(ctx context.Context, gotID, reason string) (Invoice, error) {
		gotReason = reason
		return Invoice{ID: gotID, Status: StatusFailed}, nil
	}
	body, err := json.Marshal(resolveOutsideRequestBody{Reason: "  " + want + "  "})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	rec, _ := doInvoiceResolveOutside(t, resolve, &id, invoiceID, string(body))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
	}
	if gotReason != want {
		t.Errorf("resolve received reason = %q, want %q", gotReason, want)
	}
}

// TestResolveOutsideHandler_ErrorMessagesExactText pins statusForErr's two
// story-specific sentinel mappings by exact text -- ErrorStatusMap (T4-6)
// only checks status code and non-emptiness.
func TestResolveOutsideHandler_ErrorMessagesExactText(t *testing.T) {
	id := auth.Identity{Subject: "user-1", Role: "authenticated", TenantID: uuid.NewString()}
	tests := []struct {
		name string
		err  error
		want string
	}{
		{"not_permitted", ErrNotPermitted, "approver rights are required to mark an invoice resolved outside the system"},
		{"not_resolvable", ErrNotResolvable, "only a failed invoice can be marked resolved outside the system"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			invoiceID := uuid.NewString()
			resolve := func(ctx context.Context, gotID, reason string) (Invoice, error) {
				return Invoice{}, tt.err
			}
			body, err := json.Marshal(resolveOutsideRequestBody{Reason: "valid reason"})
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			rec, resp := doInvoiceResolveOutside(t, resolve, &id, invoiceID, string(body))
			if resp.Error != tt.want {
				t.Errorf("err=%v: message = %q, want exact %q (status=%d)", tt.err, resp.Error, tt.want, rec.Code)
			}
		})
	}
}

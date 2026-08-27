// Specs for the transmission role gate on POST /v1/invoices/submissions and
// POST /v1/invoices/{id}/transitions. Adversarial siblings (cross-tenant
// resolution, guard precedence) are in transmission_rbac_adversarial_test.go.
//
// AC map:
//
//	AC-1 TestBatchSubmit_PreparerRefused, TestTransition_PreparerRefused,
//	     TestBatchSubmit_RealStore_PreparerRefusedNothingEnqueued
//	AC-2 TestBatchSubmit_AdminAndReviewerAllowed, TestTransition_AdminAndReviewerAllowed,
//	     TestTransition_RealStore_ReviewerAllowedPreparerRefused
//	AC-3 TestTransmitGate_NoExistenceOracle
//	AC-4 TestTransmitGate_UnauthenticatedStillFourOhOne
//	AC-5 TestTransmitGate_ResolverErrorFailsClosed
//	AC-6 TestTransmitGate_NoMembershipRefused
//	AC-7 TestTransmitGate_SuspendedApproverRefused
//	AC-8 TestTransmitGate_RefusalPrecedesValidatedGuard
//
// The three RealStore/Suspended tests are DB-backed (dbTestPools, which
// silently skips without DATABASE_URL/DATABASE_SUPERUSER_URL); the rest are
// pure-unit httptest against injected closures.
//
// GET /v1/invoices/{id}'s read-side half -- can_submit/submit_blocked_reason
// telling a non-approver the same truth -- is specced in the second half of
// this file, under its own AC map.
package invoice

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
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
	"github.com/SimonOsipov/invoice-os/internal/platform/db"
)

// wantNotApproverTransmitReason is the 403 copy both doors must emit, pinned
// test-locally (the resolved_outside_handlers_test.go convention) so a typo in
// the production constant fails here instead of agreeing with itself.
const wantNotApproverTransmitReason = "Only an admin or a reviewer can submit an invoice to NRS/MBS — ask an approver on your team."

const transitionToQueuedBody = `{"target":"queued"}`

// fixedRoleStub is adminRoleStub's parameterized sibling -- these specs drive
// the gate across roles, the no-membership ("", nil) shape, and a resolver error.
func fixedRoleStub(role string, err error) func(ctx context.Context) (string, error) {
	return func(ctx context.Context) (string, error) { return role, err }
}

// doInvoiceTransitionAs mirrors doInvoiceTransition (handlers_test.go) with an
// explicit callerRole, leaving that helper's 19 existing call sites untouched.
func doInvoiceTransitionAs(t *testing.T, transition func(ctx context.Context, id string, target Status) (Invoice, error), callerRole func(ctx context.Context) (string, error), id *auth.Identity, invoiceID, rawBody string) (*httptest.ResponseRecorder, invoiceBody) {
	t.Helper()
	r := httptest.NewRequest(http.MethodPost, "/v1/invoices/"+invoiceID+"/transitions", strings.NewReader(rawBody))
	r.SetPathValue("id", invoiceID)
	if id != nil {
		r = r.WithContext(auth.WithIdentity(r.Context(), *id))
	}
	rec := httptest.NewRecorder()
	TransitionHandler(transition, callerRole, nil).ServeHTTP(rec, r)
	var resp invoiceBody
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response %q: %v", rec.Body.String(), err)
	}
	return rec, resp
}

// doBatchSubmitAs is doBatchSubmit (batch_submit_handler_test.go) with an
// explicit callerRole, same reason as doInvoiceTransitionAs above.
func doBatchSubmitAs(t *testing.T, submit func(ctx context.Context, in BatchSubmitInput) (BatchSubmitResult, error), callerRole func(ctx context.Context) (string, error), id *auth.Identity, rawBody string) (*httptest.ResponseRecorder, batchSubmitResponseWire) {
	t.Helper()
	r := httptest.NewRequest(http.MethodPost, "/v1/invoices/submissions", strings.NewReader(rawBody))
	if id != nil {
		r = r.WithContext(auth.WithIdentity(r.Context(), *id))
	}
	rec := httptest.NewRecorder()
	BatchSubmitHandler(submit, callerRole, nil).ServeHTTP(rec, r)
	var resp batchSubmitResponseWire
	_ = json.Unmarshal(rec.Body.Bytes(), &resp) // best-effort; raw bytes are read directly where identity matters
	return rec, resp
}

// oneInvoiceBatchBody is a well-formed single-id batch body with a fresh
// idempotency key, so replayed keys never confound a refusal assertion.
func oneInvoiceBatchBody(t *testing.T, invoiceID string) string {
	t.Helper()
	return marshalBatchSubmit(t, batchSubmitRequestWire{InvoiceIDs: []string{invoiceID}, IdempotencyKey: "transmit-gate-" + uuid.NewString()})
}

func assertTransmitRefused(t *testing.T, rec *httptest.ResponseRecorder, gotError string) {
	t.Helper()
	if rec.Code != http.StatusForbidden {
		t.Errorf("status = %d, want %d (body=%s)", rec.Code, http.StatusForbidden, rec.Body.String())
	}
	if gotError != wantNotApproverTransmitReason {
		t.Errorf("error = %q, want %q", gotError, wantNotApproverTransmitReason)
	}
}

func mustInvoiceStatus(t *testing.T, super *pgxpool.Pool, invoiceID string) string {
	t.Helper()
	var status string
	if err := super.QueryRow(context.Background(), `SELECT status FROM invoices WHERE id = $1`, invoiceID).Scan(&status); err != nil {
		t.Fatalf("read invoice status: %v", err)
	}
	return status
}

// --- AC-1: a preparer is refused at both doors -------------------------------

func TestBatchSubmit_PreparerRefused(t *testing.T) {
	id := auth.Identity{Subject: "user-1", Role: "authenticated", TenantID: uuid.NewString()}
	called := false
	submit := func(ctx context.Context, in BatchSubmitInput) (BatchSubmitResult, error) {
		called = true
		return BatchSubmitResult{Results: []BatchSubmitResultItem{}}, nil
	}

	rec, resp := doBatchSubmitAs(t, submit, fixedRoleStub("preparer", nil), &id, oneInvoiceBatchBody(t, uuid.NewString()))

	assertTransmitRefused(t, rec, resp.Error)
	if called {
		t.Error("submit was called for a preparer; the refusal must precede the store")
	}
}

func TestTransition_PreparerRefused(t *testing.T) {
	id := auth.Identity{Subject: "user-1", Role: "authenticated", TenantID: uuid.NewString()}
	invoiceID := uuid.NewString()
	called := false
	transition := func(ctx context.Context, gotID string, target Status) (Invoice, error) {
		called = true
		return Invoice{ID: gotID, Status: target}, nil
	}

	rec, resp := doInvoiceTransitionAs(t, transition, fixedRoleStub("preparer", nil), &id, invoiceID, transitionToQueuedBody)

	assertTransmitRefused(t, rec, resp.Error)
	if called {
		t.Error("transition was called for a preparer; the refusal must precede the store")
	}
}

// --- AC-2: admin AND reviewer both keep transmitting -------------------------

func TestBatchSubmit_AdminAndReviewerAllowed(t *testing.T) {
	for _, role := range []string{"admin", "reviewer"} {
		t.Run(role, func(t *testing.T) {
			id := auth.Identity{Subject: "user-1", Role: "authenticated", TenantID: uuid.NewString()}
			invoiceID := uuid.NewString()
			calls := 0
			submit := func(ctx context.Context, in BatchSubmitInput) (BatchSubmitResult, error) {
				calls++
				return BatchSubmitResult{Results: []BatchSubmitResultItem{{InvoiceID: invoiceID, Enqueued: true, Status: string(StatusQueued)}}}, nil
			}

			rec, resp := doBatchSubmitAs(t, submit, fixedRoleStub(role, nil), &id, oneInvoiceBatchBody(t, invoiceID))

			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
			}
			if calls != 1 {
				t.Errorf("submit called %d times, want 1", calls)
			}
			if len(resp.Results) != 1 || !resp.Results[0].Enqueued {
				t.Errorf("results = %+v, want one enqueued item", resp.Results)
			}
		})
	}
}

func TestTransition_AdminAndReviewerAllowed(t *testing.T) {
	for _, role := range []string{"admin", "reviewer"} {
		t.Run(role, func(t *testing.T) {
			id := auth.Identity{Subject: "user-1", Role: "authenticated", TenantID: uuid.NewString()}
			invoiceID := uuid.NewString()
			calls := 0
			transition := func(ctx context.Context, gotID string, target Status) (Invoice, error) {
				calls++
				return Invoice{ID: gotID, Status: target}, nil
			}

			rec, resp := doInvoiceTransitionAs(t, transition, fixedRoleStub(role, nil), &id, invoiceID, transitionToQueuedBody)

			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
			}
			if calls != 1 {
				t.Errorf("transition called %d times, want 1", calls)
			}
			if resp.Status != string(StatusQueued) {
				t.Errorf("status = %q, want %q", resp.Status, StatusQueued)
			}
		})
	}
}

// --- AC-6: no membership row resolves to "" and is refused -------------------

func TestTransmitGate_NoMembershipRefused(t *testing.T) {
	id := auth.Identity{Subject: "user-1", Role: "authenticated", TenantID: uuid.NewString()}
	// ("", nil) is exactly what callerRoleTx returns for an unseeded subject.
	noMembership := fixedRoleStub("", nil)

	t.Run("batch submit", func(t *testing.T) {
		called := false
		submit := func(ctx context.Context, in BatchSubmitInput) (BatchSubmitResult, error) {
			called = true
			return BatchSubmitResult{Results: []BatchSubmitResultItem{}}, nil
		}
		rec, resp := doBatchSubmitAs(t, submit, noMembership, &id, oneInvoiceBatchBody(t, uuid.NewString()))
		assertTransmitRefused(t, rec, resp.Error)
		if called {
			t.Error("submit was called for a subject with no membership")
		}
	})

	t.Run("transition", func(t *testing.T) {
		called := false
		transition := func(ctx context.Context, gotID string, target Status) (Invoice, error) {
			called = true
			return Invoice{ID: gotID, Status: target}, nil
		}
		rec, resp := doInvoiceTransitionAs(t, transition, noMembership, &id, uuid.NewString(), transitionToQueuedBody)
		assertTransmitRefused(t, rec, resp.Error)
		if called {
			t.Error("transition was called for a subject with no membership")
		}
	})
}

// --- AC-5: a resolver error fails closed to 403, never 500 -------------------

func TestTransmitGate_ResolverErrorFailsClosed(t *testing.T) {
	id := auth.Identity{Subject: "user-1", Role: "authenticated", TenantID: uuid.NewString()}
	// The role value is deliberately an approver: only the error may decide.
	boom := fixedRoleStub("admin", errors.New("boom"))

	t.Run("batch submit", func(t *testing.T) {
		called := false
		submit := func(ctx context.Context, in BatchSubmitInput) (BatchSubmitResult, error) {
			called = true
			return BatchSubmitResult{Results: []BatchSubmitResultItem{}}, nil
		}
		rec, resp := doBatchSubmitAs(t, submit, boom, &id, oneInvoiceBatchBody(t, uuid.NewString()))
		assertTransmitRefused(t, rec, resp.Error)
		if called {
			t.Error("submit was called after the role resolver errored")
		}
	})

	t.Run("transition", func(t *testing.T) {
		called := false
		transition := func(ctx context.Context, gotID string, target Status) (Invoice, error) {
			called = true
			return Invoice{ID: gotID, Status: target}, nil
		}
		rec, resp := doInvoiceTransitionAs(t, transition, boom, &id, uuid.NewString(), transitionToQueuedBody)
		assertTransmitRefused(t, rec, resp.Error)
		if called {
			t.Error("transition was called after the role resolver errored")
		}
	})
}

// --- AC-3: the refusal carries no existence oracle ---------------------------

type transmitProbe struct {
	name string
	do   func(t *testing.T) (*httptest.ResponseRecorder, string)
}

// assertIndistinguishable requires a 403 with byte-identical body across every
// probe -- a blocked caller must not learn whether an id exists from the shape
// of its refusal.
func assertIndistinguishable(t *testing.T, probes []transmitProbe) {
	t.Helper()
	var wantRaw string
	for i, p := range probes {
		rec, gotError := p.do(t)
		assertTransmitRefused(t, rec, gotError)
		raw := rec.Body.String()
		if i == 0 {
			wantRaw = raw
			continue
		}
		if raw != wantRaw {
			t.Errorf("%s body = %q, want byte-identical to %s's %q", p.name, raw, probes[0].name, wantRaw)
		}
	}
}

func TestTransmitGate_NoExistenceOracle(t *testing.T) {
	id := auth.Identity{Subject: "user-1", Role: "authenticated", TenantID: uuid.NewString()}
	knownID := uuid.NewString()
	preparer := fixedRoleStub("preparer", nil)
	const malformedJSON = `{"target":`

	t.Run("batch submit", func(t *testing.T) {
		submit := func(ctx context.Context, in BatchSubmitInput) (BatchSubmitResult, error) {
			for _, got := range in.InvoiceIDs {
				if got != knownID {
					return BatchSubmitResult{}, ErrNotFound
				}
			}
			return BatchSubmitResult{Results: []BatchSubmitResultItem{{InvoiceID: knownID, Enqueued: true, Status: string(StatusQueued)}}}, nil
		}
		probe := func(rawBody string) func(*testing.T) (*httptest.ResponseRecorder, string) {
			return func(t *testing.T) (*httptest.ResponseRecorder, string) {
				rec, resp := doBatchSubmitAs(t, submit, preparer, &id, rawBody)
				return rec, resp.Error
			}
		}
		assertIndistinguishable(t, []transmitProbe{
			{"real id", probe(oneInvoiceBatchBody(t, knownID))},
			{"unknown id", probe(oneInvoiceBatchBody(t, uuid.NewString()))},
			{"malformed id", probe(oneInvoiceBatchBody(t, "not-a-uuid"))},
			{"malformed json", probe(malformedJSON)},
		})
	})

	t.Run("transition", func(t *testing.T) {
		transition := func(ctx context.Context, gotID string, target Status) (Invoice, error) {
			if gotID != knownID {
				return Invoice{}, ErrNotFound
			}
			return Invoice{ID: gotID, Status: target}, nil
		}
		probe := func(invoiceID, rawBody string) func(*testing.T) (*httptest.ResponseRecorder, string) {
			return func(t *testing.T) (*httptest.ResponseRecorder, string) {
				rec, resp := doInvoiceTransitionAs(t, transition, preparer, &id, invoiceID, rawBody)
				return rec, resp.Error
			}
		}
		assertIndistinguishable(t, []transmitProbe{
			{"real id", probe(knownID, transitionToQueuedBody)},
			{"unknown id", probe(uuid.NewString(), transitionToQueuedBody)},
			{"malformed id", probe("not-a-uuid", transitionToQueuedBody)},
			{"malformed json", probe(knownID, malformedJSON)},
		})
	})
}

// --- AC-4: 401 still wins, and the resolver is never reached -----------------

func TestTransmitGate_UnauthenticatedStillFourOhOne(t *testing.T) {
	resolved := 0
	countingRole := func(ctx context.Context) (string, error) {
		resolved++
		return "admin", nil
	}

	t.Run("batch submit", func(t *testing.T) {
		resolved = 0
		submit := func(ctx context.Context, in BatchSubmitInput) (BatchSubmitResult, error) {
			t.Error("submit was called for an unauthenticated request")
			return BatchSubmitResult{}, nil
		}
		rec, _ := doBatchSubmitAs(t, submit, countingRole, nil, oneInvoiceBatchBody(t, uuid.NewString()))
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("status = %d, want %d (body=%s)", rec.Code, http.StatusUnauthorized, rec.Body.String())
		}
		if resolved != 0 {
			t.Errorf("callerRole invoked %d times for an unauthenticated request, want 0", resolved)
		}
	})

	t.Run("transition", func(t *testing.T) {
		resolved = 0
		transition := func(ctx context.Context, gotID string, target Status) (Invoice, error) {
			t.Error("transition was called for an unauthenticated request")
			return Invoice{}, nil
		}
		rec, _ := doInvoiceTransitionAs(t, transition, countingRole, nil, uuid.NewString(), transitionToQueuedBody)
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("status = %d, want %d (body=%s)", rec.Code, http.StatusUnauthorized, rec.Body.String())
		}
		if resolved != 0 {
			t.Errorf("callerRole invoked %d times for an unauthenticated request, want 0", resolved)
		}
	})
}

// --- AC-8: the refusal precedes every other guard ----------------------------

func TestTransmitGate_RefusalPrecedesValidatedGuard(t *testing.T) {
	id := auth.Identity{Subject: "user-1", Role: "authenticated", TenantID: uuid.NewString()}
	called := false
	transition := func(ctx context.Context, gotID string, target Status) (Invoice, error) {
		called = true
		return Invoice{ID: gotID, Status: target}, nil
	}

	rec, resp := doInvoiceTransitionAs(t, transition, fixedRoleStub("preparer", nil), &id, uuid.NewString(), `{"target":"validated"}`)

	if rec.Code == http.StatusConflict {
		t.Errorf("status = 409 (%q); the role refusal must precede the validated-is-earned guard", resp.Error)
	}
	assertTransmitRefused(t, rec, resp.Error)
	if called {
		t.Error("transition was called for a preparer")
	}
}

// --- DB-backed: the real Store.CallerRole resolver ---------------------------

func TestBatchSubmit_RealStore_PreparerRefusedNothingEnqueued(t *testing.T) {
	super, app := dbTestPools(t)

	tenantID := seedTenant(t, super, "transmit gate batch tenant")
	entityID := seedEntity(t, super, tenantID, "transmit gate batch entity")
	invID := seedInvoiceAtStatus(t, super, tenantID, entityID, "TRANSMIT-GATE-BATCH", StatusValidated)
	subject := uuid.NewString()
	seedMembership(t, super, tenantID, subject, "preparer")

	store := NewStore(app)
	submitter := NewSubmitter(store, newInsertOnlyQueueClient(t, app))
	id := auth.Identity{Subject: subject, Role: "authenticated", TenantID: tenantID}

	rec, resp := doBatchSubmitAs(t, submitter.BatchSubmit, store.CallerRole, &id, oneInvoiceBatchBody(t, invID))

	assertTransmitRefused(t, rec, resp.Error)
	if got := mustInvoiceStatus(t, super, invID); got != string(StatusValidated) {
		t.Errorf("invoice status = %q, want %q -- a refused batch must not move the row", got, StatusValidated)
	}
	if n := countBatchSubmitJobs(t, app, invID); n != 0 {
		t.Errorf("river_job rows for %s = %d, want 0", invID, n)
	}
}

func TestTransition_RealStore_ReviewerAllowedPreparerRefused(t *testing.T) {
	super, app := dbTestPools(t)

	tenantID := seedTenant(t, super, "transmit gate transition tenant")
	entityID := seedEntity(t, super, tenantID, "transmit gate transition entity")
	// Two invoices, not one: a second transition on the reviewer's now-queued
	// row would answer ErrRedundantTransition and mask the gate's own verdict.
	reviewerInv := seedInvoiceAtStatus(t, super, tenantID, entityID, "TRANSMIT-GATE-REVIEWER", StatusValidated)
	preparerInv := seedInvoiceAtStatus(t, super, tenantID, entityID, "TRANSMIT-GATE-PREPARER", StatusValidated)

	reviewer := uuid.NewString()
	preparer := uuid.NewString()
	seedMembership(t, super, tenantID, reviewer, "reviewer")
	seedMembership(t, super, tenantID, preparer, "preparer")

	store := NewStore(app)
	reviewerID := auth.Identity{Subject: reviewer, Role: "authenticated", TenantID: tenantID}
	preparerID := auth.Identity{Subject: preparer, Role: "authenticated", TenantID: tenantID}

	rec, resp := doInvoiceTransitionAs(t, store.Transition, store.CallerRole, &reviewerID, reviewerInv, transitionToQueuedBody)
	if rec.Code != http.StatusOK {
		t.Fatalf("reviewer status = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
	}
	if resp.Status != string(StatusQueued) {
		t.Errorf("reviewer response status = %q, want %q", resp.Status, StatusQueued)
	}
	if got := mustInvoiceStatus(t, super, reviewerInv); got != string(StatusQueued) {
		t.Errorf("reviewer invoice status = %q, want %q", got, StatusQueued)
	}

	rec, resp = doInvoiceTransitionAs(t, store.Transition, store.CallerRole, &preparerID, preparerInv, transitionToQueuedBody)
	assertTransmitRefused(t, rec, resp.Error)
	if got := mustInvoiceStatus(t, super, preparerInv); got != string(StatusValidated) {
		t.Errorf("preparer invoice status = %q, want %q -- a refused transition must not move the row", got, StatusValidated)
	}
}

// AC-7: callerRoleTx filters status = 'active', so a suspended admin resolves
// to "" and is refused exactly like a preparer.
func TestTransmitGate_SuspendedApproverRefused(t *testing.T) {
	super, app := dbTestPools(t)

	tenantID := seedTenant(t, super, "transmit gate suspended tenant")
	entityID := seedEntity(t, super, tenantID, "transmit gate suspended entity")
	batchInv := seedInvoiceAtStatus(t, super, tenantID, entityID, "TRANSMIT-GATE-SUSPENDED-BATCH", StatusValidated)
	transitionInv := seedInvoiceAtStatus(t, super, tenantID, entityID, "TRANSMIT-GATE-SUSPENDED-TRANSITION", StatusValidated)

	subject := uuid.NewString()
	seedMembershipWithStatus(t, super, tenantID, subject, "admin", "suspended")

	store := NewStore(app)
	submitter := NewSubmitter(store, newInsertOnlyQueueClient(t, app))
	id := auth.Identity{Subject: subject, Role: "authenticated", TenantID: tenantID}

	t.Run("batch submit", func(t *testing.T) {
		rec, resp := doBatchSubmitAs(t, submitter.BatchSubmit, store.CallerRole, &id, oneInvoiceBatchBody(t, batchInv))
		assertTransmitRefused(t, rec, resp.Error)
		if got := mustInvoiceStatus(t, super, batchInv); got != string(StatusValidated) {
			t.Errorf("invoice status = %q, want %q", got, StatusValidated)
		}
	})

	t.Run("transition", func(t *testing.T) {
		rec, resp := doInvoiceTransitionAs(t, store.Transition, store.CallerRole, &id, transitionInv, transitionToQueuedBody)
		assertTransmitRefused(t, rec, resp.Error)
		if got := mustInvoiceStatus(t, super, transitionInv); got != string(StatusValidated) {
			t.Errorf("invoice status = %q, want %q", got, StatusValidated)
		}
	})
}

// =============================================================================
// GET /v1/invoices/{id}'s submit gate: submitGate answers role before status,
// and GetHandler resolves the caller role on every status, not only failed.
//
// AC map (this half's own numbering, distinct from the transmit doors' above):
//
//	AC-1 TestGetHandler_SubmitBlockedReasonRoleArm,
//	     TestGetHandler_RealStore_PreparerSeesRoleReason,
//	     TestGetHandler_RealStore_NoMembershipRefused
//	AC-2 TestSubmitGate_AdminAndReviewerUnchanged        (guard)
//	AC-3 TestSubmitGate_RoleBeforeStatus
//	AC-4 TestGetHandler_RoleResolvedOnValidatedStatus
//	AC-5 TestGetHandler_CanSubmitKeysStillExactlyOnce    (guard)
//	AC-6 TestGetHandler_RoleArmFailsClosed
//	AC-8 the two seedMembership repairs in handlers_test.go
// =============================================================================

// submitGateBody is this half's wire mirror for GET /v1/invoices/{id} -- only
// the two keys the submit gate owns, following getWithRoleBody's convention
// (resolved_outside_handlers_test.go).
type submitGateBody struct {
	CanSubmit           bool    `json:"can_submit"`
	SubmitBlockedReason *string `json:"submit_blocked_reason"`
}

// doInvoiceGetAs is doInvoiceGet (handlers_test.go) with an explicit
// callerRole, which that helper hardwires to adminRoleStub.
func doInvoiceGetAs(t *testing.T, get func(ctx context.Context, id string) (Invoice, error), callerRole func(ctx context.Context) (string, error), id *auth.Identity, invoiceID string) (*httptest.ResponseRecorder, submitGateBody) {
	t.Helper()
	r := httptest.NewRequest(http.MethodGet, "/v1/invoices/"+invoiceID, nil)
	r.SetPathValue("id", invoiceID)
	if id != nil {
		r = r.WithContext(auth.WithIdentity(r.Context(), *id))
	}
	rec := httptest.NewRecorder()
	GetHandler(get, callerRole, clearApprovalStub, nil).ServeHTTP(rec, r)
	var resp submitGateBody
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response %q: %v", rec.Body.String(), err)
	}
	return rec, resp
}

func invoiceAtStatusStub(s Status) func(ctx context.Context, id string) (Invoice, error) {
	return func(ctx context.Context, gotID string) (Invoice, error) {
		return Invoice{ID: gotID, Status: s}, nil
	}
}

// assertRoleReason pins the non-approver arm: can_submit false AND the ONE
// refusal sentence, never null and never a status message.
func assertRoleReason(t *testing.T, resp submitGateBody) {
	t.Helper()
	if resp.CanSubmit {
		t.Error("can_submit = true for a non-approver, want false")
	}
	if resp.SubmitBlockedReason == nil {
		t.Fatalf("submit_blocked_reason = null, want %q", wantNotApproverTransmitReason)
	}
	if *resp.SubmitBlockedReason != wantNotApproverTransmitReason {
		t.Errorf("submit_blocked_reason = %q, want %q", *resp.SubmitBlockedReason, wantNotApproverTransmitReason)
	}
}

// --- AC-1: a preparer's GET carries the role sentence, on validated too ------

func TestGetHandler_SubmitBlockedReasonRoleArm(t *testing.T) {
	id := auth.Identity{Subject: "user-1", Role: "authenticated", TenantID: uuid.NewString()}

	// validated is the status that today renders can_submit:true -- the one
	// place the read side would otherwise offer a preparer a button that 403s.
	rec, resp := doInvoiceGetAs(t, invoiceAtStatusStub(StatusValidated), fixedRoleStub("preparer", nil), &id, uuid.NewString())

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
	}
	assertRoleReason(t, resp)
}

// --- AC-3: role is checked BEFORE status -------------------------------------

func TestSubmitGate_RoleBeforeStatus(t *testing.T) {
	// draft/rejected carry a status sentence today, queued/accepted carry null:
	// both wrong answers for a preparer, and both must become the role sentence.
	for _, s := range []Status{StatusDraft, StatusRejected, StatusQueued, StatusAccepted} {
		t.Run(string(s), func(t *testing.T) {
			can, reason := submitGate(s, "preparer", true)
			if can {
				t.Error("can = true for a preparer, want false")
			}
			if reason == nil {
				t.Fatalf("reason = nil, want %q", wantNotApproverTransmitReason)
			}
			if *reason != wantNotApproverTransmitReason {
				t.Errorf("reason = %q, want the role sentence %q -- a status message sends a preparer into a re-validate loop that can never end in a submit", *reason, wantNotApproverTransmitReason)
			}
		})
	}
}

// --- AC-2: an approver's two values are byte-identical to today's ------------

// submitGateWant is one oracle row. Empty reason means JSON null.
type submitGateWant struct {
	can    bool
	reason string
}

// shippedSubmitGateOracle is submitGate's approver answer on today's table.
// Hardcoded, never produced by calling canSubmit/submitBlockedReason, so the
// tests reading it cannot degrade into a tautology. Package-level so the
// approval-arm specs (submit_gate_approval_test.go) state their delta against
// this one literal instead of restating it.
var shippedSubmitGateOracle = map[Status]submitGateWant{
	StatusDraft:     {false, exactSubmitBlockedReasonDraft},
	StatusValidated: {true, ""},
	StatusQueued:    {false, ""},
	StatusSubmitted: {false, ""},
	StatusAccepted:  {false, ""},
	StatusRejected:  {false, exactSubmitBlockedReasonRejected},
	StatusFailed:    {false, ""},
}

// assertSubmitGateRow compares one (can, reason) pair against one oracle row.
func assertSubmitGateRow(t *testing.T, w submitGateWant, can bool, reason *string) {
	t.Helper()
	if can != w.can {
		t.Errorf("can = %v, want %v", can, w.can)
	}
	switch {
	case w.reason == "" && reason != nil:
		t.Errorf("reason = %q, want nil", *reason)
	case w.reason != "" && reason == nil:
		t.Errorf("reason = nil, want %q", w.reason)
	case w.reason != "" && reason != nil && *reason != w.reason:
		t.Errorf("reason = %q, want %q", *reason, w.reason)
	}
}

func TestSubmitGate_AdminAndReviewerUnchanged(t *testing.T) {
	want := shippedSubmitGateOracle
	if len(want) != len(allStatuses) {
		t.Fatalf("oracle covers %d statuses, want all %d", len(want), len(allStatuses))
	}
	// approvalClear=true IS the "unchanged" condition: a flag-off deployment
	// folds to clear, so every row here must stay byte-identical to the answer
	// shipped before the approval arm existed.
	for _, role := range []string{"admin", "reviewer"} {
		for _, s := range allStatuses {
			t.Run(string(s)+"_"+role, func(t *testing.T) {
				w, ok := want[s]
				if !ok {
					t.Fatalf("no oracle entry for status %q", s)
				}
				can, reason := submitGate(s, role, true)
				assertSubmitGateRow(t, w, can, reason)
			})
		}
	}
}

// --- AC-4: the role resolves on every status, not only failed ----------------

func TestGetHandler_RoleResolvedOnValidatedStatus(t *testing.T) {
	id := auth.Identity{Subject: "user-1", Role: "authenticated", TenantID: uuid.NewString()}
	calls := 0
	role := func(ctx context.Context) (string, error) {
		calls++
		return "admin", nil
	}

	doInvoiceGetAs(t, invoiceAtStatusStub(StatusValidated), role, &id, uuid.NewString())

	if calls != 1 {
		t.Errorf("callerRole called %d times on a validated invoice, want 1 -- submitGate consults role on every status", calls)
	}
}

// --- AC-6: a resolver error fails closed inside a 200 ------------------------

func TestGetHandler_RoleArmFailsClosed(t *testing.T) {
	id := auth.Identity{Subject: "user-1", Role: "authenticated", TenantID: uuid.NewString()}
	// The role value is deliberately an approver: only the error may decide.
	boom := fixedRoleStub("admin", errors.New("boom"))

	rec, resp := doInvoiceGetAs(t, invoiceAtStatusStub(StatusValidated), boom, &id, uuid.NewString())

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 -- a role-resolver error must never become a 5xx (body=%s)", rec.Code, rec.Body.String())
	}
	assertRoleReason(t, resp)
}

// --- AC-5: the new arm changes values only, never the key set or its order ---

// TestGetHandler_CanSubmitKeysStillExactlyOnce re-runs the embed-boundary guard
// TestGetHandler_CanSubmitKeyAppearsExactlyOnce (handlers_test.go) on BOTH arms
// and extends it to submit_blocked_reason, which had no occurrence guard --
// under encoding/json's ambiguous-field rule a same-depth duplicate tag silently
// drops both entries.
func TestGetHandler_CanSubmitKeysStillExactlyOnce(t *testing.T) {
	id := auth.Identity{Subject: "user-1", Role: "authenticated", TenantID: uuid.NewString()}

	keysFor := func(t *testing.T, role string) []string {
		t.Helper()
		rec, _ := doInvoiceGetAs(t, invoiceAtStatusStub(StatusValidated), fixedRoleStub(role, nil), &id, uuid.NewString())
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: status = %d, want 200 (body=%s)", role, rec.Code, rec.Body.String())
		}
		body := rec.Body.String()
		for _, k := range []string{`"can_submit":`, `"submit_blocked_reason":`} {
			if got := strings.Count(body, k); got != 1 {
				t.Errorf("%s: body has %d occurrences of %s key, want exactly 1 (body=%s)", role, got, k, body)
			}
		}
		return topLevelKeyOrder(t, rec.Body.Bytes())
	}

	approverKeys := keysFor(t, "admin")
	preparerKeys := keysFor(t, "preparer")
	if !reflect.DeepEqual(preparerKeys, approverKeys) {
		t.Errorf("preparer key order =\n%v\nwant the approver arm's\n%v", preparerKeys, approverKeys)
	}
}

// --- DB-backed: the real Store.CallerRole behind the read side ---------------

func TestGetHandler_RealStore_PreparerSeesRoleReason(t *testing.T) {
	super, app := dbTestPools(t)

	tenantID := seedTenant(t, super, "submit gate role tenant")
	entityID := seedEntity(t, super, tenantID, "submit gate role entity")
	invID := seedInvoiceAtStatus(t, super, tenantID, entityID, "SUBMIT-GATE-ROLE", StatusValidated)

	admin := uuid.NewString()
	preparer := uuid.NewString()
	seedMembership(t, super, tenantID, admin, "admin")
	seedMembership(t, super, tenantID, preparer, "preparer")

	store := NewStore(app)
	adminID := auth.Identity{Subject: admin, Role: "authenticated", TenantID: tenantID}
	preparerID := auth.Identity{Subject: preparer, Role: "authenticated", TenantID: tenantID}

	// The admin leg is the non-vacuity floor: it proves this row really is
	// submittable, so the preparer's false can only come from role.
	rec, adminResp := doInvoiceGetAs(t, store.Get, store.CallerRole, &adminID, invID)
	if rec.Code != http.StatusOK {
		t.Fatalf("admin: status = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
	}
	if !adminResp.CanSubmit {
		t.Error("admin: can_submit = false on a validated invoice, want true")
	}
	if adminResp.SubmitBlockedReason != nil {
		t.Errorf("admin: submit_blocked_reason = %q, want null", *adminResp.SubmitBlockedReason)
	}

	rec, preparerResp := doInvoiceGetAs(t, store.Get, store.CallerRole, &preparerID, invID)
	if rec.Code != http.StatusOK {
		t.Fatalf("preparer: status = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
	}
	assertRoleReason(t, preparerResp)
}

// TestGetHandler_RealStore_NoMembershipRefused (AUDIT-12-07): the refusal
// moved earlier. Store.Get itself runs over db.WithinRequestTenantTx, so a caller
// with no membership row is now refused (db.ErrNotActiveMember, 403) before
// GetHandler ever calls callerRole -- this no longer reaches the ("", nil) role-
// reason shape the name describes.
func TestGetHandler_RealStore_NoMembershipRefused(t *testing.T) {
	super, app := dbTestPools(t)

	tenantID := seedTenant(t, super, "submit gate no-membership tenant")
	entityID := seedEntity(t, super, tenantID, "submit gate no-membership entity")
	invID := seedInvoiceAtStatus(t, super, tenantID, entityID, "SUBMIT-GATE-NOMEM", StatusValidated)

	store := NewStore(app)
	id := auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: tenantID}

	rec, _ := doInvoiceGetAs(t, store.Get, store.CallerRole, &id, invID)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 (body=%s)", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), db.NotActiveMemberMessage) {
		t.Errorf("body = %s, want it to carry db.NotActiveMemberMessage", rec.Body.String())
	}
}

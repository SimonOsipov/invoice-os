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
package invoice

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
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

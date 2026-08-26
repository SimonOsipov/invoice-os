// Adversarial coverage for the transmission role gate, on top of the
// AC-derived specs in transmission_rbac_test.go: cross-tenant role resolution,
// cross-tenant reach past an allowed gate, the guards the refusal must still
// outrank, and GET's read side, where the same role now feeds two gates.
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

	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
	"github.com/SimonOsipov/invoice-os/internal/platform/db"
)

// --- cross-tenant role resolution --------------------------------------------

// callerRoleTx (store.go:1390) has NO tenant_id predicate -- one subject with
// memberships in two tenants leaves exactly one visible row only because of
// memberships' tenant_isolation RLS policy. Drop or weaken that policy and the
// gate would answer with "the user's role somewhere", so the same subject would
// transmit in a tenant where they are only a preparer. Both directions are
// asserted on ONE fixture: whichever row a predicate-less query happened to
// pick, one of the two must break.
func TestTransmitGate_RealStore_ResolvesRequestTenantRoleNotUserRole(t *testing.T) {
	super, app := dbTestPools(t)

	tenantA := seedTenant(t, super, "transmit gate cross-tenant A")
	tenantB := seedTenant(t, super, "transmit gate cross-tenant B")
	entityA := seedEntity(t, super, tenantA, "transmit gate cross-tenant entity A")
	entityB := seedEntity(t, super, tenantB, "transmit gate cross-tenant entity B")
	invA := seedInvoiceAtStatus(t, super, tenantA, entityA, "TRANSMIT-XTENANT-A", StatusValidated)
	invB := seedInvoiceAtStatus(t, super, tenantB, entityB, "TRANSMIT-XTENANT-B", StatusValidated)

	subject := uuid.NewString()
	seedMembership(t, super, tenantA, subject, "admin")
	seedMembership(t, super, tenantB, subject, "preparer")

	store := NewStore(app)
	submitter := NewSubmitter(store, newInsertOnlyQueueClient(t, app))
	inA := auth.Identity{Subject: subject, Role: "authenticated", TenantID: tenantA}
	inB := auth.Identity{Subject: subject, Role: "authenticated", TenantID: tenantB}

	t.Run("refused in the tenant where the role is preparer", func(t *testing.T) {
		rec, resp := doInvoiceTransitionAs(t, store.Transition, store.CallerRole, &inB, invB, transitionToQueuedBody)
		assertTransmitRefused(t, rec, resp.Error)
		if got := mustInvoiceStatus(t, super, invB); got != string(StatusValidated) {
			t.Errorf("tenant B invoice status = %q, want %q", got, StatusValidated)
		}

		batchRec, batchResp := doBatchSubmitAs(t, submitter.BatchSubmit, store.CallerRole, &inB, oneInvoiceBatchBody(t, invB))
		assertTransmitRefused(t, batchRec, batchResp.Error)
		if n := countBatchSubmitJobs(t, app, invB); n != 0 {
			t.Errorf("river_job rows for the tenant B invoice = %d, want 0", n)
		}
	})

	t.Run("allowed in the tenant where the same subject is admin", func(t *testing.T) {
		rec, resp := doInvoiceTransitionAs(t, store.Transition, store.CallerRole, &inA, invA, transitionToQueuedBody)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
		}
		if resp.Status != string(StatusQueued) {
			t.Errorf("response status = %q, want %q", resp.Status, StatusQueued)
		}
		if got := mustInvoiceStatus(t, super, invA); got != string(StatusQueued) {
			t.Errorf("tenant A invoice status = %q, want %q", got, StatusQueued)
		}
	})
}

// An approver's 403 is lifted, not their tenant boundary: an admin in tenant A
// aiming at a tenant B invoice must get the SAME 404 body a never-existing uuid
// gets, so a passed gate is not a cross-tenant existence oracle.
func TestTransmitGate_RealStore_ApproverCannotReachAnotherTenantsInvoice(t *testing.T) {
	super, app := dbTestPools(t)

	tenantA := seedTenant(t, super, "transmit gate reach A")
	tenantB := seedTenant(t, super, "transmit gate reach B")
	entityB := seedEntity(t, super, tenantB, "transmit gate reach entity B")
	invB := seedInvoiceAtStatus(t, super, tenantB, entityB, "TRANSMIT-REACH-B", StatusValidated)

	admin := uuid.NewString()
	seedMembership(t, super, tenantA, admin, "admin")

	store := NewStore(app)
	submitter := NewSubmitter(store, newInsertOnlyQueueClient(t, app))
	id := auth.Identity{Subject: admin, Role: "authenticated", TenantID: tenantA}
	ghost := uuid.NewString()

	t.Run("transition", func(t *testing.T) {
		assertIndistinguishable404(t, []transmitProbe{
			{"other tenant's invoice", func(t *testing.T) (*httptest.ResponseRecorder, string) {
				rec, resp := doInvoiceTransitionAs(t, store.Transition, store.CallerRole, &id, invB, transitionToQueuedBody)
				return rec, resp.Error
			}},
			{"never-existed id", func(t *testing.T) (*httptest.ResponseRecorder, string) {
				rec, resp := doInvoiceTransitionAs(t, store.Transition, store.CallerRole, &id, ghost, transitionToQueuedBody)
				return rec, resp.Error
			}},
		})
		if got := mustInvoiceStatus(t, super, invB); got != string(StatusValidated) {
			t.Errorf("tenant B invoice status = %q, want %q", got, StatusValidated)
		}
	})

	t.Run("batch submit", func(t *testing.T) {
		assertIndistinguishable404(t, []transmitProbe{
			{"other tenant's invoice", func(t *testing.T) (*httptest.ResponseRecorder, string) {
				rec, resp := doBatchSubmitAs(t, submitter.BatchSubmit, store.CallerRole, &id, oneInvoiceBatchBody(t, invB))
				return rec, resp.Error
			}},
			{"never-existed id", func(t *testing.T) (*httptest.ResponseRecorder, string) {
				rec, resp := doBatchSubmitAs(t, submitter.BatchSubmit, store.CallerRole, &id, oneInvoiceBatchBody(t, ghost))
				return rec, resp.Error
			}},
		})
		if n := countBatchSubmitJobs(t, app, invB); n != 0 {
			t.Errorf("river_job rows for the tenant B invoice = %d, want 0", n)
		}
	})
}

// assertIndistinguishable404 is assertIndistinguishable's not-found twin: every
// probe must answer 404 with a byte-identical body.
func assertIndistinguishable404(t *testing.T, probes []transmitProbe) {
	t.Helper()
	var wantRaw string
	for i, p := range probes {
		rec, gotError := p.do(t)
		if rec.Code != http.StatusNotFound {
			t.Errorf("%s status = %d, want 404 (body=%s)", p.name, rec.Code, rec.Body.String())
		}
		if gotError == wantNotApproverTransmitReason {
			t.Errorf("%s answered the role refusal; an admin's gate must be open", p.name)
		}
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

// --- an 'invited' member is not an active one --------------------------------

// callerRoleTx filters status = 'active', and 'invited' is the third value the
// column's CHECK allows (migrations/20260808140706_memberships_status_and_identity.sql).
// TestTransmitGate_SuspendedApproverRefused only pins 'suspended'.
func TestTransmitGate_RealStore_InvitedApproverRefused(t *testing.T) {
	super, app := dbTestPools(t)

	tenantID := seedTenant(t, super, "transmit gate invited tenant")
	entityID := seedEntity(t, super, tenantID, "transmit gate invited entity")
	inv := seedInvoiceAtStatus(t, super, tenantID, entityID, "TRANSMIT-GATE-INVITED", StatusValidated)

	subject := uuid.NewString()
	seedMembershipWithStatus(t, super, tenantID, subject, "admin", "invited")

	store := NewStore(app)
	id := auth.Identity{Subject: subject, Role: "authenticated", TenantID: tenantID}

	rec, resp := doInvoiceTransitionAs(t, store.Transition, store.CallerRole, &id, inv, transitionToQueuedBody)
	assertTransmitRefused(t, rec, resp.Error)
	if got := mustInvoiceStatus(t, super, inv); got != string(StatusValidated) {
		t.Errorf("invoice status = %q, want %q", got, StatusValidated)
	}
}

// --- the refusal outranks every body guard, not just the ones AC-3 probes ----

// TestTransmitGate_NoExistenceOracle probes id shapes and malformed JSON. The
// batch door's own pre-store 400s (empty list, 200 cap, blank/over-long
// idempotency key) and the transition door's unknown-target 400 are separate
// branches that a refactor could hoist above the gate one at a time.
func TestTransmitGate_RefusalOutranksBodyGuards(t *testing.T) {
	id := auth.Identity{Subject: "user-1", Role: "authenticated", TenantID: uuid.NewString()}
	preparer := fixedRoleStub("preparer", nil)

	t.Run("batch submit", func(t *testing.T) {
		submit := func(ctx context.Context, in BatchSubmitInput) (BatchSubmitResult, error) {
			t.Error("submit was called for a preparer")
			return BatchSubmitResult{}, nil
		}
		probe := func(rawBody string) func(*testing.T) (*httptest.ResponseRecorder, string) {
			return func(t *testing.T) (*httptest.ResponseRecorder, string) {
				rec, resp := doBatchSubmitAs(t, submit, preparer, &id, rawBody)
				return rec, resp.Error
			}
		}
		overCap := make([]string, maxBatchSubmitInvoiceIDs+1)
		for i := range overCap {
			overCap[i] = uuid.NewString()
		}
		assertIndistinguishable(t, []transmitProbe{
			{"well-formed", probe(oneInvoiceBatchBody(t, uuid.NewString()))},
			{"empty invoice_ids", probe(marshalBatchSubmit(t, batchSubmitRequestWire{InvoiceIDs: []string{}, IdempotencyKey: uuid.NewString()}))},
			{"over the 200 cap", probe(marshalBatchSubmit(t, batchSubmitRequestWire{InvoiceIDs: overCap, IdempotencyKey: uuid.NewString()}))},
			{"blank idempotency_key", probe(marshalBatchSubmit(t, batchSubmitRequestWire{InvoiceIDs: []string{uuid.NewString()}, IdempotencyKey: ""}))},
			{"over-long idempotency_key", probe(marshalBatchSubmit(t, batchSubmitRequestWire{InvoiceIDs: []string{uuid.NewString()}, IdempotencyKey: strings.Repeat("k", maxBatchSubmitIdempotencyKeyLen+1)}))},
			// Past maxBatchSubmitBodyBytes: MaxBytesReader is installed after
			// the gate, so the body is never read at all.
			{"over the 64 KiB body cap", probe(marshalBatchSubmit(t, batchSubmitRequestWire{InvoiceIDs: []string{uuid.NewString()}, IdempotencyKey: strings.Repeat("k", maxBatchSubmitBodyBytes*2)}))},
			{"empty body", probe("")},
		})
	})

	t.Run("transition", func(t *testing.T) {
		transition := func(ctx context.Context, gotID string, target Status) (Invoice, error) {
			t.Error("transition was called for a preparer")
			return Invoice{}, nil
		}
		probe := func(rawBody string) func(*testing.T) (*httptest.ResponseRecorder, string) {
			return func(t *testing.T) (*httptest.ResponseRecorder, string) {
				rec, resp := doInvoiceTransitionAs(t, transition, preparer, &id, uuid.NewString(), rawBody)
				return rec, resp.Error
			}
		}
		assertIndistinguishable(t, []transmitProbe{
			{"legal target", probe(transitionToQueuedBody)},
			{"unknown target", probe(`{"target":"not-a-status"}`)},
			{"absent target", probe(`{}`)},
			{"empty body", probe("")},
		})
	})
}

// --- the gate is one round-trip, not one per guard ---------------------------

// The architect's own note says the gate costs exactly one memberships
// round-trip per transmit request. A second call site added later (a
// submit_blocked_reason-style read gate on the write path, say) would double
// that silently.
func TestTransmitGate_ApproverResolvesRoleExactlyOnce(t *testing.T) {
	id := auth.Identity{Subject: "user-1", Role: "authenticated", TenantID: uuid.NewString()}
	resolved := 0
	countingRole := func(ctx context.Context) (string, error) {
		resolved++
		return "admin", nil
	}

	t.Run("batch submit", func(t *testing.T) {
		resolved = 0
		submit := func(ctx context.Context, in BatchSubmitInput) (BatchSubmitResult, error) {
			return BatchSubmitResult{Results: []BatchSubmitResultItem{}}, nil
		}
		rec, _ := doBatchSubmitAs(t, submit, countingRole, &id, oneInvoiceBatchBody(t, uuid.NewString()))
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
		}
		if resolved != 1 {
			t.Errorf("callerRole invoked %d times, want 1", resolved)
		}
	})

	t.Run("transition", func(t *testing.T) {
		resolved = 0
		transition := func(ctx context.Context, gotID string, target Status) (Invoice, error) {
			return Invoice{ID: gotID, Status: target}, nil
		}
		rec, _ := doInvoiceTransitionAs(t, transition, countingRole, &id, uuid.NewString(), transitionToQueuedBody)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
		}
		if resolved != 1 {
			t.Errorf("callerRole invoked %d times, want 1", resolved)
		}
	})
}

// --- GET's submit gate: the read side of the same role resolution ------------

// GET resolves a role on EVERY status now, so the cross-tenant hazard
// TestTransmitGate_RealStore_ResolvesRequestTenantRoleNotUserRole pins for the
// write doors applies here too: callerRoleTx has no tenant_id predicate, and
// only memberships' tenant_isolation RLS keeps one row visible.
func TestGetHandler_RealStore_SubmitGateResolvesRequestTenantRoleNotUserRole(t *testing.T) {
	super, app := dbTestPools(t)

	tenantA := seedTenant(t, super, "submit gate cross-tenant A")
	tenantB := seedTenant(t, super, "submit gate cross-tenant B")
	entityA := seedEntity(t, super, tenantA, "submit gate cross-tenant entity A")
	entityB := seedEntity(t, super, tenantB, "submit gate cross-tenant entity B")
	invA := seedInvoiceAtStatus(t, super, tenantA, entityA, "SUBMIT-XTENANT-A", StatusValidated)
	invB := seedInvoiceAtStatus(t, super, tenantB, entityB, "SUBMIT-XTENANT-B", StatusValidated)

	subject := uuid.NewString()
	seedMembership(t, super, tenantA, subject, "admin")
	seedMembership(t, super, tenantB, subject, "preparer")
	// A second, unrelated admin in tenant B is the non-vacuity floor: it proves
	// invB is submittable by STATUS, so the preparer leg's false is the role.
	adminB := uuid.NewString()
	seedMembership(t, super, tenantB, adminB, "admin")

	store := NewStore(app)
	inA := auth.Identity{Subject: subject, Role: "authenticated", TenantID: tenantA}
	inB := auth.Identity{Subject: subject, Role: "authenticated", TenantID: tenantB}
	inAdminB := auth.Identity{Subject: adminB, Role: "authenticated", TenantID: tenantB}

	assertSubmittable := func(t *testing.T, id auth.Identity, invoiceID string) {
		t.Helper()
		rec, resp := doInvoiceGetAs(t, store.Get, store.CallerRole, &id, invoiceID)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
		}
		if !resp.CanSubmit {
			t.Error("can_submit = false, want true")
		}
		if resp.SubmitBlockedReason != nil {
			t.Errorf("submit_blocked_reason = %q, want null", *resp.SubmitBlockedReason)
		}
	}

	t.Run("submittable in the tenant where the same subject is admin", func(t *testing.T) {
		assertSubmittable(t, inA, invA)
	})

	t.Run("blocked in the tenant where the same subject is a preparer", func(t *testing.T) {
		rec, resp := doInvoiceGetAs(t, store.Get, store.CallerRole, &inB, invB)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
		}
		assertRoleReason(t, resp)
	})

	t.Run("another admin in tenant B sees the same row as submittable", func(t *testing.T) {
		assertSubmittable(t, inAdminB, invB)
	})
}

// The request seam refuses a non-active caller before the store reads anything
// (db.WithinRequestTenantTxOpts), so a suspended or invited admin gets no body at
// all -- no flag, no reason, and no invoice. Pinned at the store and at the read
// door, because an enabled Submit button is exactly what suspension must stop.
func TestGetHandler_RealStore_SuspendedAndInvitedApproverRefused(t *testing.T) {
	super, app := dbTestPools(t)

	tenantID := seedTenant(t, super, "submit gate member status tenant")
	entityID := seedEntity(t, super, tenantID, "submit gate member status entity")
	invID := seedInvoiceAtStatus(t, super, tenantID, entityID, "SUBMIT-GATE-MEMBER-STATUS", StatusValidated)

	store := NewStore(app)

	active := uuid.NewString()
	seedMembership(t, super, tenantID, active, "admin")
	activeID := auth.Identity{Subject: active, Role: "authenticated", TenantID: tenantID}
	rec, resp := doInvoiceGetAs(t, store.Get, store.CallerRole, &activeID, invID)
	if rec.Code != http.StatusOK {
		t.Fatalf("active admin: status = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
	}
	if !resp.CanSubmit {
		t.Fatal("active admin: can_submit = false -- the fixture must be submittable before membership status can be blamed")
	}

	for _, status := range []string{"suspended", "invited"} {
		t.Run(status, func(t *testing.T) {
			subject := uuid.NewString()
			seedMembershipWithStatus(t, super, tenantID, subject, "admin", status)
			id := auth.Identity{Subject: subject, Role: "authenticated", TenantID: tenantID}

			rec, resp := doInvoiceGetAs(t, store.Get, store.CallerRole, &id, invID)
			// The seam refuses the read, and the refusal reaches the wire as the
			// 403 the mapper produces -- the same body every other handler gives.
			assertNotActiveMember403(t, rec)
			if resp.CanSubmit || resp.SubmitBlockedReason != nil {
				t.Errorf("the refusal still published can_submit=%v submit_blocked_reason=%v, want neither key set",
					resp.CanSubmit, resp.SubmitBlockedReason)
			}
			if _, err := store.Get(auth.WithIdentity(context.Background(), id), invID); !errors.Is(err, db.ErrNotActiveMember) {
				t.Errorf("Store.Get err = %v, want db.ErrNotActiveMember -- the refusal, named", err)
			}
		})
	}
}

// AC-1 says ANY status, but the wire is pinned for a non-approver only on
// validated (TestGetHandler_SubmitBlockedReasonRoleArm) and failed (below);
// submitGate's own table covers four. This closes the status axis end to end.
func TestGetHandler_NonApproverBlockedOnEveryStatus(t *testing.T) {
	id := auth.Identity{Subject: "user-1", Role: "authenticated", TenantID: uuid.NewString()}
	for _, s := range allStatuses {
		t.Run(string(s), func(t *testing.T) {
			rec, resp := doInvoiceGetAs(t, invoiceAtStatusStub(s), fixedRoleStub("preparer", nil), &id, uuid.NewString())
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
			}
			assertRoleReason(t, resp)
		})
	}
}

// --- the two gates, one shared role ------------------------------------------

// bothGatesBody is a wire mirror for the four keys the shared role decides --
// submitGateBody covers only the submit pair, getWithRoleBody only the
// resolve-outside one.
type bothGatesBody struct {
	CanSubmit                   bool    `json:"can_submit"`
	SubmitBlockedReason         *string `json:"submit_blocked_reason"`
	CanResolveOutside           bool    `json:"can_resolve_outside"`
	ResolveOutsideBlockedReason *string `json:"resolve_outside_blocked_reason"`
}

func doInvoiceGetBothGates(t *testing.T, get func(ctx context.Context, id string) (Invoice, error), callerRole func(ctx context.Context) (string, error), id *auth.Identity, invoiceID string) (*httptest.ResponseRecorder, bothGatesBody) {
	t.Helper()
	r := httptest.NewRequest(http.MethodGet, "/v1/invoices/"+invoiceID, nil)
	r.SetPathValue("id", invoiceID)
	if id != nil {
		r = r.WithContext(auth.WithIdentity(r.Context(), *id))
	}
	rec := httptest.NewRecorder()
	GetHandler(get, callerRole, clearApprovalStub, nil).ServeHTTP(rec, r)
	var resp bothGatesBody
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response %q: %v", rec.Body.String(), err)
	}
	return rec, resp
}

// failed is the one status where BOTH gates consult role, so it is where
// removing the failed-only short-circuit could have perturbed
// resolveOutsideGate. Each gate must keep its OWN sentence: one shared sentence
// would point a blocked preparer at the wrong door.
func TestGetHandler_FailedInvoiceBothGatesAnswerFromTheSameRole(t *testing.T) {
	id := auth.Identity{Subject: "user-1", Role: "authenticated", TenantID: uuid.NewString()}
	for _, tc := range []struct {
		name     string
		role     string
		approver bool
	}{
		{"admin", "admin", true},
		{"reviewer", "reviewer", true},
		{"preparer", "preparer", false},
		// "" is what callerRoleTx answers for no membership, suspended, invited.
		{"no membership", "", false},
		{"unknown role", "owner", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec, resp := doInvoiceGetBothGates(t, invoiceAtStatusStub(StatusFailed), fixedRoleStub(tc.role, nil), &id, uuid.NewString())
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
			}

			// failed is not validated, so nobody may submit -- but only a
			// non-approver is told WHY by the role sentence.
			if resp.CanSubmit {
				t.Error("can_submit = true on a failed invoice, want false")
			}
			if tc.approver {
				if resp.SubmitBlockedReason != nil {
					t.Errorf("submit_blocked_reason = %q, want null for an approver on failed", *resp.SubmitBlockedReason)
				}
				if !resp.CanResolveOutside {
					t.Error("can_resolve_outside = false, want true -- the removed short-circuit must not have changed this gate")
				}
				if resp.ResolveOutsideBlockedReason != nil {
					t.Errorf("resolve_outside_blocked_reason = %q, want null", *resp.ResolveOutsideBlockedReason)
				}
				return
			}
			if resp.SubmitBlockedReason == nil {
				t.Fatalf("submit_blocked_reason = null, want %q", wantNotApproverTransmitReason)
			}
			if *resp.SubmitBlockedReason != wantNotApproverTransmitReason {
				t.Errorf("submit_blocked_reason = %q, want %q", *resp.SubmitBlockedReason, wantNotApproverTransmitReason)
			}
			if resp.CanResolveOutside {
				t.Error("can_resolve_outside = true for a non-approver, want false")
			}
			if resp.ResolveOutsideBlockedReason == nil {
				t.Fatalf("resolve_outside_blocked_reason = null, want %q", wantResolveOutsideApproverReason)
			}
			if *resp.ResolveOutsideBlockedReason != wantResolveOutsideApproverReason {
				t.Errorf("resolve_outside_blocked_reason = %q, want %q -- each gate names its own action", *resp.ResolveOutsideBlockedReason, wantResolveOutsideApproverReason)
			}
		})
	}
}

// TestGetHandler_CallerRoleCalledOnceWhenFailed counts resolutions; this pins
// the consequence, and also catches a gate wired to a stale or empty role while
// the count stays 1. A resolver answering differently on a second call would
// leave the two gates disagreeing inside one response -- both are asserted, so
// either evaluation order fails.
func TestGetHandler_BothGatesReadOneResolution(t *testing.T) {
	id := auth.Identity{Subject: "user-1", Role: "authenticated", TenantID: uuid.NewString()}
	calls := 0
	alternating := func(ctx context.Context) (string, error) {
		calls++
		if calls == 1 {
			return "admin", nil
		}
		return "preparer", nil
	}

	rec, resp := doInvoiceGetBothGates(t, invoiceAtStatusStub(StatusFailed), alternating, &id, uuid.NewString())
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
	}
	if resp.SubmitBlockedReason != nil {
		t.Errorf("submit_blocked_reason = %q, want null -- the submit gate read a second, preparer resolution (callerRole called %d times)", *resp.SubmitBlockedReason, calls)
	}
	if !resp.CanResolveOutside {
		t.Errorf("can_resolve_outside = false, want true -- the resolve-outside gate read a second, preparer resolution (callerRole called %d times)", calls)
	}
}

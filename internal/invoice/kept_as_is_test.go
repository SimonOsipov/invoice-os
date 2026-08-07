// INVCR-01-15 (D6, task-291): RED, DB-backed tests for `Keep as-is` -- auditable
// triage, written BEFORE Store.KeepAsIs/UnkeepAsIs exist (RED against the not-yet-added
// methods: every assertion below fails to compile/run until store.go/handlers.go/
// main.go land, then fails on VALUE until the real bodies are correct). Reuses the
// dbTestPools/seedTenant/seedEntity/seedInvoiceWithViolations/seedInvoiceAtStatus/
// mustCount/auditCount harness from store_test.go/transition_adversarial_test.go (same
// package) -- nothing here is redefined.
//
// Spec-to-test map (Test Specs table, task-291):
//
//	AC-4  TestRLS_KeepAsIsCrossTenantIs404               [QA finding C5]
//	AC-1  TestKeptAsIs_PartialTripleRejected
//	AC-1  TestKeptAsIs_NonDraftRejected
//	AC-2  TestKeptAsIs_ProjectionRoundTrips
//	AC-3  TestKeepAsIsHandler_CleanInvoice409
//	AC-3  TestKeepAsIsHandler_ActorIsIdentityNotBody
//	AC-5  TestKeptAsIs_AuditInSameTx
//
// Plus five tests beyond the architect's table, added to close real gaps AC #3/#6
// otherwise leave untested (flagged, not smuggled -- see each test's own doc comment):
//
//	(extra) TestKeepAsIsHandler_EmptyReason400
//	(extra) TestKeepAsIsHandler_OversizedReason400
//	(extra) TestKeepAsIsHandler_NoIdentity401
//	(extra) TestKeepAsIsHandler_UnkeepClearsMarksAndAudits   (AC #6's only positive leg)
//	(extra) TestKeepAsIsHandler_UnkeepAlreadyUnkeptIsNoop
//
// D10/D6 note: nothing here drives a transition. legalTransitions/the status CHECKs are
// asserted untouched by TestLegalTransitionsUnchanged (transition_test.go), not here.
//
// Run: `make test-rls` will NOT cover this package (it targets ./internal/platform/db/...
// at port 5432) -- use:
//
//	DATABASE_URL="postgres://invoice_app:app@localhost:5434/invoice_os?sslmode=disable" \
//	DATABASE_SUPERUSER_URL="postgres://postgres:postgres@localhost:5434/invoice_os?sslmode=disable" \
//	go test -count=1 -p 1 ./internal/invoice/...
package invoice

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
	"github.com/SimonOsipov/invoice-os/internal/platform/db"
)

// keepAsIsResponseBody is this file's own test-local wire mirror -- decodes the three
// new kept_as_is_* keys directly (handlers_test.go's shared invoiceBody type is left
// untouched beyond the SYNC-style key-count fixes this story already required there;
// adding fields to it for tests that live entirely in this file would widen a shared
// type for a single-file need).
type keepAsIsResponseBody struct {
	Status         string  `json:"status"`
	KeptAsIsAt     *string `json:"kept_as_is_at"`
	KeptAsIsBy     *string `json:"kept_as_is_by"`
	KeptAsIsReason *string `json:"kept_as_is_reason"`
	Error          string  `json:"error"`
}

// doInvoiceKeepAsIs drives POST /v1/invoices/{id}/keep-as-is through the REAL HTTP
// handler -- cloned from doInvoiceEdit's exact identity-injection/path-value shape
// (handlers_test.go).
func doInvoiceKeepAsIs(t *testing.T, keep func(ctx context.Context, id, reason string) (Invoice, error), id *auth.Identity, invoiceID, rawBody string) (*httptest.ResponseRecorder, keepAsIsResponseBody) {
	t.Helper()
	r := httptest.NewRequest(http.MethodPost, "/v1/invoices/"+invoiceID+"/keep-as-is", strings.NewReader(rawBody))
	r.SetPathValue("id", invoiceID)
	if id != nil {
		r = r.WithContext(auth.WithIdentity(r.Context(), *id))
	}
	rec := httptest.NewRecorder()
	KeepAsIsHandler(keep, nil).ServeHTTP(rec, r)
	var resp keepAsIsResponseBody
	if len(rec.Body.Bytes()) > 0 {
		if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
			t.Fatalf("decode response %q: %v", rec.Body.String(), err)
		}
	}
	return rec, resp
}

// doInvoiceUnkeepAsIs is doInvoiceKeepAsIs's DELETE sibling -- no body to decode,
// mirroring doInvoiceEdit/doInvoiceTransition's own shape for a no-body verb (there is
// no doHistory-style precedent for DELETE in this package, so this follows
// doImportGetBatch's GET shape instead -- same "no request body" case).
func doInvoiceUnkeepAsIs(t *testing.T, unkeep func(ctx context.Context, id string) (Invoice, error), id *auth.Identity, invoiceID string) (*httptest.ResponseRecorder, keepAsIsResponseBody) {
	t.Helper()
	r := httptest.NewRequest(http.MethodDelete, "/v1/invoices/"+invoiceID+"/keep-as-is", nil)
	r.SetPathValue("id", invoiceID)
	if id != nil {
		r = r.WithContext(auth.WithIdentity(r.Context(), *id))
	}
	rec := httptest.NewRecorder()
	UnkeepAsIsHandler(unkeep, nil).ServeHTTP(rec, r)
	var resp keepAsIsResponseBody
	if len(rec.Body.Bytes()) > 0 {
		if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
			t.Fatalf("decode response %q: %v", rec.Body.String(), err)
		}
	}
	return rec, resp
}

// seedDraftWithBlockingViolation is this file's one-line shorthand over
// seedInvoiceWithViolations (store_test.go) for the fixture every positive KeepAsIs
// spec needs: a draft carrying exactly one severity:"error" violation -- keepable per
// Store.KeepAsIs's own guard.
func seedDraftWithBlockingViolation(t *testing.T, super *pgxpool.Pool, tenantID, entityID, number string) string {
	t.Helper()
	return seedInvoiceWithViolations(t, super, tenantID, entityID, number, string(StatusDraft),
		`[{"rule_key":"vat-standard-rate","severity":"error","message":"bad rate"}]`)
}

// mustKeptAsIsTriple reads the three kept_as_is_* columns back as the superuser
// (bypasses RLS) -- the mutation-verify half of every negative spec below: a refusal
// must leave the columns exactly as they were, not merely return the right status code.
func mustKeptAsIsTriple(t *testing.T, super *pgxpool.Pool, id string) (at, by, reason *string) {
	t.Helper()
	if err := super.QueryRow(context.Background(),
		`SELECT kept_as_is_at::text, kept_as_is_by, kept_as_is_reason FROM invoices WHERE id = $1`, id,
	).Scan(&at, &by, &reason); err != nil {
		t.Fatalf("read back kept_as_is triple for %s: %v", id, err)
	}
	return
}

// --- AC-4 [QA finding C5] ---------------------------------------------------

// TestRLS_KeepAsIsCrossTenantIs404 (AC-4, [QA finding C5]): the ONE new *write* path
// this story adds to invoices, and until this test the three new columns' FORCE RLS
// inheritance was asserted in prose, not tested (unlike siblings 06/07, which both name
// a cross-tenant TestRLS_* test). Two legs, ONE test:
//
//  1. POST leg -- tenant 2 owns a draft invoice with a blocking violation; tenant 1
//     POSTs keep-as-is against it -> 404, indistinguishable from an unknown uuid
//     (RLS's own FOR UPDATE lock+read 0-rows, same as every other lock-and-read method
//     in this package); re-read as tenant 2 (superuser bypass) -> all three columns
//     still NULL and no invoice.kept_as_is audit row -- mutation-verify: the refusal
//     wrote nothing, not merely "returned 404".
//  2. DELETE leg -- tenant 2's invoice IS ALREADY kept; tenant 1 DELETEs it -> 404;
//     re-read as tenant 2 -> it stays kept, byte-identical to before the refused
//     DELETE, and no invoice.unkept_as_is audit row.
func TestRLS_KeepAsIsCrossTenantIs404(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenant1 := seedTenant(t, super, "KEEP-RLS tenant 1")
	tenant2 := seedTenant(t, super, "KEEP-RLS tenant 2")
	entity2 := seedEntity(t, super, tenant2, "KEEP-RLS entity 2")

	store := NewStore(app)
	id1 := auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: tenant1}
	c2 := auth.WithIdentity(ctx, auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: tenant2})

	// --- leg 1: POST against a not-yet-kept tenant-2 invoice ---
	invB := seedDraftWithBlockingViolation(t, super, tenant2, entity2, "KEEP-RLS-POST")

	beforeAudit := auditCount(t, app, tenant2, "invoice.kept_as_is")
	body, err := json.Marshal(keepAsIsRequest{Reason: "cross-tenant attempt"})
	if err != nil {
		t.Fatalf("marshal keep-as-is request: %v", err)
	}

	rec, _ := doInvoiceKeepAsIs(t, store.KeepAsIs, &id1, invB, string(body))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("POST keep-as-is (tenant 1 on tenant 2's invoice) status = %d, want 404 (body=%s)", rec.Code, rec.Body.String())
	}

	unknownRec, _ := doInvoiceKeepAsIs(t, store.KeepAsIs, &id1, uuid.NewString(), string(body))
	if unknownRec.Code != http.StatusNotFound {
		t.Fatalf("POST keep-as-is (unknown id) status = %d, want 404", unknownRec.Code)
	}
	if !bytesEqual(rec.Body.Bytes(), unknownRec.Body.Bytes()) {
		t.Errorf("cross-tenant body = %s, unknown-id body = %s -- want byte-identical (no existence oracle)", rec.Body.String(), unknownRec.Body.String())
	}

	at, by, reason := mustKeptAsIsTriple(t, super, invB)
	if at != nil || by != nil || reason != nil {
		t.Errorf("tenant 2's invoice kept_as_is triple after refused cross-tenant POST = (%v,%v,%v), want all NULL (nothing written)", at, by, reason)
	}
	if n := auditCount(t, app, tenant2, "invoice.kept_as_is"); n != beforeAudit {
		t.Errorf("invoice.kept_as_is audit rows for tenant 2 = %d, want unchanged %d", n, beforeAudit)
	}

	// --- leg 2: DELETE against an ALREADY-KEPT tenant-2 invoice ---
	invD := seedDraftWithBlockingViolation(t, super, tenant2, entity2, "KEEP-RLS-DELETE")
	kept, err := store.KeepAsIs(c2, invD, "legitimate keep, as tenant 2")
	if err != nil {
		t.Fatalf("setup: KeepAsIs (as tenant 2, own invoice): %v", err)
	}
	if kept.KeptAsIsAt == nil {
		t.Fatal("setup: KeepAsIs did not stamp kept_as_is_at -- the refusal check below would be vacuous")
	}

	beforeUnkeptAudit := auditCount(t, app, tenant2, "invoice.unkept_as_is")
	beforeAt, beforeBy, beforeReason := mustKeptAsIsTriple(t, super, invD)

	delRec, _ := doInvoiceUnkeepAsIs(t, store.UnkeepAsIs, &id1, invD)
	if delRec.Code != http.StatusNotFound {
		t.Fatalf("DELETE keep-as-is (tenant 1 on tenant 2's KEPT invoice) status = %d, want 404 (body=%s)", delRec.Code, delRec.Body.String())
	}

	unknownDelRec, _ := doInvoiceUnkeepAsIs(t, store.UnkeepAsIs, &id1, uuid.NewString())
	if unknownDelRec.Code != http.StatusNotFound {
		t.Fatalf("DELETE keep-as-is (unknown id) status = %d, want 404", unknownDelRec.Code)
	}
	if !bytesEqual(delRec.Body.Bytes(), unknownDelRec.Body.Bytes()) {
		t.Errorf("cross-tenant DELETE body = %s, unknown-id body = %s -- want byte-identical", delRec.Body.String(), unknownDelRec.Body.String())
	}

	afterAt, afterBy, afterReason := mustKeptAsIsTriple(t, super, invD)
	if afterAt == nil || afterBy == nil || afterReason == nil {
		t.Fatalf("tenant 2's invoice kept_as_is triple after refused cross-tenant DELETE = (%v,%v,%v), want it to STAY KEPT (all non-NULL)", afterAt, afterBy, afterReason)
	}
	if *afterAt != *beforeAt || *afterBy != *beforeBy || *afterReason != *beforeReason {
		t.Errorf("tenant 2's kept_as_is triple changed across a refused cross-tenant DELETE: before=(%s,%s,%s) after=(%s,%s,%s), want byte-identical",
			*beforeAt, *beforeBy, *beforeReason, *afterAt, *afterBy, *afterReason)
	}
	if n := auditCount(t, app, tenant2, "invoice.unkept_as_is"); n != beforeUnkeptAudit {
		t.Errorf("invoice.unkept_as_is audit rows for tenant 2 = %d, want unchanged %d", n, beforeUnkeptAudit)
	}
}

// bytesEqual is a tiny local helper so this file needs no bytes.Equal import solely
// for the two byte-identical assertions above.
func bytesEqual(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// --- AC-1: the two new CHECKs -----------------------------------------------

// TestKeptAsIs_PartialTripleRejected (AC-1): a raw UPDATE setting only
// kept_as_is_reason (leaving _at/_by NULL) must 23514 against
// invoices_kept_as_is_complete -- proves the "all three or none" invariant rests on
// the DB, not on Store.KeepAsIs always writing all three faithfully.
func TestKeptAsIs_PartialTripleRejected(t *testing.T) {
	super, _ := dbTestPools(t)
	ctx := context.Background()

	tenantID := seedTenant(t, super, "KEEP-CHECK-1 tenant")
	entityID := seedEntity(t, super, tenantID, "KEEP-CHECK-1 entity")
	invID := seedInvoice(t, super, tenantID, entityID, "KEEP-CHECK-1")

	_, err := super.Exec(ctx, `UPDATE invoices SET kept_as_is_reason = 'partial' WHERE id = $1`, invID)
	if err == nil {
		t.Fatal("UPDATE setting only kept_as_is_reason succeeded, want a 23514 (invoices_kept_as_is_complete) violation")
	}
	if code := pgCode(err); code != "23514" {
		t.Fatalf("pgCode = %q, want 23514 (check_violation): %v", code, err)
	}
	if !strings.Contains(err.Error(), "invoices_kept_as_is_complete") {
		t.Errorf("error = %v, want it to name invoices_kept_as_is_complete", err)
	}
}

// TestKeptAsIs_NonDraftRejected (AC-1): a validated invoice, force-triple-written,
// must 23514 against invoices_kept_as_is_status -- D6's "it stays a draft and can
// never be sent" enforced by the DB, not by Store.KeepAsIs's own status guard alone.
func TestKeptAsIs_NonDraftRejected(t *testing.T) {
	super, _ := dbTestPools(t)
	ctx := context.Background()

	tenantID := seedTenant(t, super, "KEEP-CHECK-2 tenant")
	entityID := seedEntity(t, super, tenantID, "KEEP-CHECK-2 entity")
	invID := seedInvoiceAtStatus(t, super, tenantID, entityID, "KEEP-CHECK-2", StatusValidated)

	_, err := super.Exec(ctx,
		`UPDATE invoices SET kept_as_is_at = now(), kept_as_is_by = 'someone', kept_as_is_reason = 'x' WHERE id = $1`, invID)
	if err == nil {
		t.Fatal("UPDATE setting the triple on a validated invoice succeeded, want a 23514 (invoices_kept_as_is_status) violation")
	}
	if code := pgCode(err); code != "23514" {
		t.Fatalf("pgCode = %q, want 23514 (check_violation): %v", code, err)
	}
	if !strings.Contains(err.Error(), "invoices_kept_as_is_status") {
		t.Errorf("error = %v, want it to name invoices_kept_as_is_status", err)
	}
}

// --- AC-2: the positional projection ----------------------------------------

// TestKeptAsIs_ProjectionRoundTrips (AC-2): keep an invoice, then Store.Get it back --
// all three columns must come back with the right values IN THE RIGHT FIELDS, guarding
// invoiceColumns/scanInvoice's positional scan (AC #2's own "same order" requirement --
// a swap between kept_as_is_by and kept_as_is_reason would silently scan the reason
// into By and vice versa, since both are *string).
func TestKeptAsIs_ProjectionRoundTrips(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := seedTenant(t, super, "KEEP-PROJ tenant")
	entityID := seedEntity(t, super, tenantID, "KEEP-PROJ entity")

	store := NewStore(app)
	subject := uuid.NewString()
	c := auth.WithIdentity(ctx, auth.Identity{Subject: subject, Role: "authenticated", TenantID: tenantID})

	invID := seedDraftWithBlockingViolation(t, super, tenantID, entityID, "KEEP-PROJ")

	const wantReason = "Buyer confirmed the discrepancy is intentional; will not recur."
	kept, err := store.KeepAsIs(c, invID, wantReason)
	if err != nil {
		t.Fatalf("KeepAsIs: %v", err)
	}
	if kept.KeptAsIsAt == nil || kept.KeptAsIsBy == nil || kept.KeptAsIsReason == nil {
		t.Fatalf("KeepAsIs return = (at=%v by=%v reason=%v), want all three non-nil", kept.KeptAsIsAt, kept.KeptAsIsBy, kept.KeptAsIsReason)
	}
	if *kept.KeptAsIsBy != subject {
		t.Errorf("KeepAsIs return .KeptAsIsBy = %q, want the caller's subject %q", *kept.KeptAsIsBy, subject)
	}
	if *kept.KeptAsIsReason != wantReason {
		t.Errorf("KeepAsIs return .KeptAsIsReason = %q, want %q", *kept.KeptAsIsReason, wantReason)
	}

	got, err := store.Get(c, invID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.KeptAsIsAt == nil {
		t.Fatal("Get.KeptAsIsAt = nil, want the timestamp KeepAsIs stamped")
	}
	if !got.KeptAsIsAt.Equal(*kept.KeptAsIsAt) {
		t.Errorf("Get.KeptAsIsAt = %v, want %v (KeepAsIs's own RETURNING value)", got.KeptAsIsAt, kept.KeptAsIsAt)
	}
	if got.KeptAsIsBy == nil || *got.KeptAsIsBy != subject {
		t.Errorf("Get.KeptAsIsBy = %v, want a pointer to %q", got.KeptAsIsBy, subject)
	}
	if got.KeptAsIsReason == nil || *got.KeptAsIsReason != wantReason {
		t.Errorf("Get.KeptAsIsReason = %v, want a pointer to %q", got.KeptAsIsReason, wantReason)
	}

	// The AUDIT record's own payload must carry the reason too -- "the reason text is
	// the point" (this story's own founder ruling), and the audit trail is the
	// PERMANENT record of it; the invoices.kept_as_is_reason column alone is mutable
	// (a later re-keep or a clear-on-edit/promote overwrites or nulls it), so a
	// reason-less audit row would leave no durable trace of the FIRST decision. This
	// specifically guards against a KeepAsIs that stamps the column correctly but
	// forgets to also carry the reason into audit.Record's payload.
	var payloadJSON string
	if err := db.WithinTenantTx(ctx, app, tenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx,
			`SELECT payload::text FROM audit_log WHERE event = 'invoice.kept_as_is' ORDER BY created_at DESC LIMIT 1`,
		).Scan(&payloadJSON)
	}); err != nil {
		t.Fatalf("read back invoice.kept_as_is audit payload: %v", err)
	}
	var payload struct {
		ID     string `json:"id"`
		Reason string `json:"reason"`
	}
	if err := json.Unmarshal([]byte(payloadJSON), &payload); err != nil {
		t.Fatalf("unmarshal audit payload %q: %v", payloadJSON, err)
	}
	if payload.ID != invID {
		t.Errorf("audit payload id = %q, want %q", payload.ID, invID)
	}
	if payload.Reason != wantReason {
		t.Errorf("audit payload reason = %q, want %q (the audit trail must carry the reason too, not just the invoices column)", payload.Reason, wantReason)
	}
}

// --- AC-3: the handler's guards ----------------------------------------------

// TestKeepAsIsHandler_CleanInvoice409 (AC-3): a draft with ZERO violations -> POST ->
// 409, nothing written. Keeping a clean invoice is meaningless -- there is nothing
// being suppressed.
func TestKeepAsIsHandler_CleanInvoice409(t *testing.T) {
	super, app := dbTestPools(t)

	tenantID := seedTenant(t, super, "KEEP-CLEAN tenant")
	entityID := seedEntity(t, super, tenantID, "KEEP-CLEAN entity")
	store := NewStore(app)
	id := auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: tenantID}

	invID := seedInvoiceWithViolations(t, super, tenantID, entityID, "KEEP-CLEAN", string(StatusDraft), `[]`)

	beforeAudit := auditCount(t, app, tenantID, "invoice.kept_as_is")
	body, err := json.Marshal(keepAsIsRequest{Reason: "trying to keep a clean invoice"})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	rec, resp := doInvoiceKeepAsIs(t, store.KeepAsIs, &id, invID, string(body))
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409 (body=%s)", rec.Code, rec.Body.String())
	}
	if resp.Error == "" {
		t.Error("expected a non-empty error message in the body")
	}

	at, by, reason := mustKeptAsIsTriple(t, super, invID)
	if at != nil || by != nil || reason != nil {
		t.Errorf("kept_as_is triple after a refused clean-invoice keep = (%v,%v,%v), want all NULL", at, by, reason)
	}
	if n := auditCount(t, app, tenantID, "invoice.kept_as_is"); n != beforeAudit {
		t.Errorf("invoice.kept_as_is audit rows = %d, want unchanged %d", n, beforeAudit)
	}
}

// TestKeepAsIsHandler_ActorIsIdentityNotBody (AC-3): a body smuggling `"by":
// "someone-else"` must be ignored -- kept_as_is_by is ALWAYS the verified token
// subject. keepAsIsRequest has no `By` field at all, so json.Decode drops the extra
// key silently; this test proves that structural fact rather than merely asserting it.
func TestKeepAsIsHandler_ActorIsIdentityNotBody(t *testing.T) {
	super, app := dbTestPools(t)

	tenantID := seedTenant(t, super, "KEEP-ACTOR tenant")
	entityID := seedEntity(t, super, tenantID, "KEEP-ACTOR entity")
	store := NewStore(app)
	subject := uuid.NewString()
	id := auth.Identity{Subject: subject, Role: "authenticated", TenantID: tenantID}

	invID := seedDraftWithBlockingViolation(t, super, tenantID, entityID, "KEEP-ACTOR")

	rawBody := `{"reason":"legitimate reason","by":"someone-else"}`
	rec, resp := doInvoiceKeepAsIs(t, store.KeepAsIs, &id, invID, rawBody)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
	}
	if resp.KeptAsIsBy == nil || *resp.KeptAsIsBy != subject {
		t.Errorf("kept_as_is_by = %v, want the token subject %q, never the body's \"by\"", resp.KeptAsIsBy, subject)
	}

	_, by, _ := mustKeptAsIsTriple(t, super, invID)
	if by == nil || *by != subject {
		t.Errorf("stored kept_as_is_by = %v, want %q", by, subject)
	}
}

// --- (extra) AC-3's remaining guards, not individually named in the Test Specs
// table but required by AC #3's own text and by "the executor must NOT" item 8 ---

// TestKeepAsIsHandler_EmptyReason400 (extra, AC #3 / must-NOT item 8): an empty
// (post-trim) reason must 400 BEFORE Store.KeepAsIs is ever called -- mirrors
// BatchSubmitHandler's own pre-tx idempotency_key guard.
func TestKeepAsIsHandler_EmptyReason400(t *testing.T) {
	super, app := dbTestPools(t)

	tenantID := seedTenant(t, super, "KEEP-EMPTY tenant")
	entityID := seedEntity(t, super, tenantID, "KEEP-EMPTY entity")
	store := NewStore(app)
	id := auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: tenantID}

	invID := seedDraftWithBlockingViolation(t, super, tenantID, entityID, "KEEP-EMPTY")

	for _, reason := range []string{"", "   ", "\t\n"} {
		body, err := json.Marshal(keepAsIsRequest{Reason: reason})
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		rec, resp := doInvoiceKeepAsIs(t, store.KeepAsIs, &id, invID, string(body))
		if rec.Code != http.StatusBadRequest {
			t.Errorf("reason=%q: status = %d, want 400 (body=%s)", reason, rec.Code, rec.Body.String())
		}
		if resp.Error == "" {
			t.Errorf("reason=%q: expected a non-empty error message", reason)
		}
	}

	at, _, _ := mustKeptAsIsTriple(t, super, invID)
	if at != nil {
		t.Errorf("kept_as_is_at after every empty-reason attempt = %v, want NULL (nothing written)", at)
	}
}

// TestKeepAsIsHandler_OversizedReason400 (extra, AC #3 / must-NOT item 8): a reason
// over maxKeepAsIsReasonLen (1000 bytes) must 400, nothing written.
func TestKeepAsIsHandler_OversizedReason400(t *testing.T) {
	super, app := dbTestPools(t)

	tenantID := seedTenant(t, super, "KEEP-OVERSIZE tenant")
	entityID := seedEntity(t, super, tenantID, "KEEP-OVERSIZE entity")
	store := NewStore(app)
	id := auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: tenantID}

	invID := seedDraftWithBlockingViolation(t, super, tenantID, entityID, "KEEP-OVERSIZE")

	body, err := json.Marshal(keepAsIsRequest{Reason: strings.Repeat("a", maxKeepAsIsReasonLen+1)})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	rec, resp := doInvoiceKeepAsIs(t, store.KeepAsIs, &id, invID, string(body))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (body=%s)", rec.Code, rec.Body.String())
	}
	if resp.Error == "" {
		t.Error("expected a non-empty error message")
	}

	at, _, _ := mustKeptAsIsTriple(t, super, invID)
	if at != nil {
		t.Errorf("kept_as_is_at after an oversized-reason attempt = %v, want NULL", at)
	}
}

// TestKeepAsIsHandler_NoIdentity401 (extra, "identity-first-401" per AC #3): matches
// every other handler in this package's own identity-first ordering.
func TestKeepAsIsHandler_NoIdentity401(t *testing.T) {
	invoiceID := uuid.NewString()
	keep := func(ctx context.Context, id, reason string) (Invoice, error) {
		t.Fatal("keep must not be called when identity is absent")
		return Invoice{}, nil
	}
	body, err := json.Marshal(keepAsIsRequest{Reason: "irrelevant"})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	rec, resp := doInvoiceKeepAsIs(t, keep, nil, invoiceID, string(body))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401 (body=%s)", rec.Code, rec.Body.String())
	}
	if resp.Error == "" {
		t.Error("expected a non-empty error message")
	}
}

// --- AC-5: audit-in-the-same-tx ----------------------------------------------

// TestKeptAsIs_AuditInSameTx (AC-5): a forced audit-write failure (a 256-char
// Subject, tripping audit_log's own char_length(actor)<=255 CHECK -- the SAME
// technique GATE-13/TestApplyValidation_LongActorRollsBackWholeTx already uses in
// this package) must roll the WHOLE transaction back: the column write never lands
// either, proving the two are one atomic unit, not "write columns, best-effort audit".
func TestKeptAsIs_AuditInSameTx(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := seedTenant(t, super, "KEEP-AUDIT-TX tenant")
	entityID := seedEntity(t, super, tenantID, "KEEP-AUDIT-TX entity")
	store := NewStore(app)

	invID := seedDraftWithBlockingViolation(t, super, tenantID, entityID, "KEEP-AUDIT-TX")

	longSubject := strings.Repeat("a", 256)
	cCrafted := auth.WithIdentity(ctx, auth.Identity{Subject: longSubject, Role: "authenticated", TenantID: tenantID})

	beforeAudit := auditCount(t, app, tenantID, "invoice.kept_as_is")

	_, err := store.KeepAsIs(cCrafted, invID, "should never land")
	if err == nil {
		t.Fatal("KeepAsIs with a 256-char actor succeeded, want an audit_log actor CHECK violation (SQLSTATE 23514)")
	}
	if code := pgCode(err); code != "23514" {
		t.Fatalf("pgCode = %q, want 23514 (check_violation): %v", code, err)
	}

	at, by, reason := mustKeptAsIsTriple(t, super, invID)
	if at != nil || by != nil || reason != nil {
		t.Errorf("kept_as_is triple after a rolled-back KeepAsIs = (%v,%v,%v), want all NULL (the column write rolled back WITH the failed audit write)", at, by, reason)
	}
	if n := auditCount(t, app, tenantID, "invoice.kept_as_is"); n != beforeAudit {
		t.Errorf("invoice.kept_as_is audit rows = %d, want unchanged %d", n, beforeAudit)
	}
}

// --- (extra) AC #6's positive coverage ---------------------------------------

// TestKeepAsIsHandler_UnkeepClearsMarksAndAudits (extra, AC #6): the ONE positive
// (non-cross-tenant) leg proving DELETE actually works end to end -- the Test Specs
// table only names the cross-tenant DELETE leg (folded into
// TestRLS_KeepAsIsCrossTenantIs404 above); without this test, a DELETE handler that
// always 404'd would still pass every named spec.
func TestKeepAsIsHandler_UnkeepClearsMarksAndAudits(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := seedTenant(t, super, "KEEP-UNKEEP tenant")
	entityID := seedEntity(t, super, tenantID, "KEEP-UNKEEP entity")
	store := NewStore(app)
	id := auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: tenantID}
	c := auth.WithIdentity(ctx, id)

	invID := seedDraftWithBlockingViolation(t, super, tenantID, entityID, "KEEP-UNKEEP")
	if _, err := store.KeepAsIs(c, invID, "will be un-kept"); err != nil {
		t.Fatalf("setup KeepAsIs: %v", err)
	}
	if at, _, _ := mustKeptAsIsTriple(t, super, invID); at == nil {
		t.Fatal("setup: kept_as_is_at is NULL after KeepAsIs -- the DELETE assertion below would be vacuous")
	}

	beforeAudit := auditCount(t, app, tenantID, "invoice.unkept_as_is")

	rec, resp := doInvoiceUnkeepAsIs(t, store.UnkeepAsIs, &id, invID)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
	}
	if resp.KeptAsIsAt != nil || resp.KeptAsIsBy != nil || resp.KeptAsIsReason != nil {
		t.Errorf("response kept_as_is triple = (%v,%v,%v), want all null", resp.KeptAsIsAt, resp.KeptAsIsBy, resp.KeptAsIsReason)
	}
	if resp.Status != string(StatusDraft) {
		t.Errorf("response status = %q, want unchanged %q (D6: un-keeping never transitions)", resp.Status, StatusDraft)
	}

	at, by, reason := mustKeptAsIsTriple(t, super, invID)
	if at != nil || by != nil || reason != nil {
		t.Errorf("stored kept_as_is triple after DELETE = (%v,%v,%v), want all NULL", at, by, reason)
	}
	if n := auditCount(t, app, tenantID, "invoice.unkept_as_is"); n != beforeAudit+1 {
		t.Errorf("invoice.unkept_as_is audit rows = %d, want %d (+1)", n, beforeAudit+1)
	}
}

// TestKeepAsIsHandler_UnkeepAlreadyUnkeptIsNoop (extra): DELETE on an invoice that was
// never kept must be idempotent -- 200, nothing written, no audit row -- never a 404
// (there is no "un-keepable state" to refuse into) and never a spurious audit entry.
func TestKeepAsIsHandler_UnkeepAlreadyUnkeptIsNoop(t *testing.T) {
	super, app := dbTestPools(t)

	tenantID := seedTenant(t, super, "KEEP-UNKEEP-NOOP tenant")
	entityID := seedEntity(t, super, tenantID, "KEEP-UNKEEP-NOOP entity")
	store := NewStore(app)
	id := auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: tenantID}

	invID := seedDraftWithBlockingViolation(t, super, tenantID, entityID, "KEEP-UNKEEP-NOOP")

	beforeAudit := auditCount(t, app, tenantID, "invoice.unkept_as_is")

	rec, resp := doInvoiceUnkeepAsIs(t, store.UnkeepAsIs, &id, invID)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (idempotent no-op, body=%s)", rec.Code, rec.Body.String())
	}
	if resp.KeptAsIsAt != nil {
		t.Errorf("response kept_as_is_at = %v, want NULL", resp.KeptAsIsAt)
	}

	if n := auditCount(t, app, tenantID, "invoice.unkept_as_is"); n != beforeAudit {
		t.Errorf("invoice.unkept_as_is audit rows = %d, want unchanged %d (a no-op un-keep must not audit)", n, beforeAudit)
	}
}

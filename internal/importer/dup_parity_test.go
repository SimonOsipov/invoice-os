// M4-06-02 -- QA Mode-A/B invariant-lock tests. Unlike M4-06-01's RED-then-
// GREEN specs, these are written to PASS against the CURRENT + M4-06-01
// code: they lock invariants the story's System Design (Design Question 2/3/
// 4) argues already hold, so a passing run here is the expected, correct
// outcome -- a RED result would mean the story's own predicate-parity/
// RLS/self-exclusion argument is WRONG, and must be reported rather than
// "fixed" by editing production code (this subtask makes NO production
// changes, per the story's Files-to-modify list).
//
// HOME: package importer for every test in this file, per the story's own
// routing -- this package is the only one that can reach BOTH stores (the
// importer -> invoice import edge, service.go's `"github.com/.../internal/
// invoice"` import, is one-directional; internal/invoice never imports
// internal/importer), which PAR-01 in particular needs.
//
// Spec-to-test map (M4-06-02, Test Specs table):
//
//	PAR-01 TestPredicateParity_StateBlindImporterIndexParitySweep     (Core AC#2)
//	PAR-02 TestPredicateParity_IndexShapeIsUniqueNonPartialOnExactColumns (Core AC#2)
//	PAR-05 TestPredicateParity_CrossTenantNoFalseCollision            (Core AC#5)
//	PAR-06 TestPredicateParity_SelfExclusionReimportLeavesStoredRowUntouched (Core AC#6)
//
// PAR-03/PAR-04 (the manual-path store/HTTP locks) live in
// internal/invoice/store_test.go and internal/invoice/handlers_test.go
// respectively -- they exercise ONLY internal/invoice and so belong beside
// its own suite, not here.
//
// Run:
//
//	DATABASE_URL="postgres://invoice_app:app@localhost:5432/invoice_os?sslmode=disable" \
//	DATABASE_SUPERUSER_URL="postgres://postgres:postgres@localhost:5432/invoice_os?sslmode=disable" \
//	go test -count=1 -v -run 'TestPredicateParity' ./internal/importer/...
package importer

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/SimonOsipov/invoice-os/internal/invoice"
	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
)

// --- PAR-01 ------------------------------------------------------------

// PAR-01: state-blind importer<->index parity sweep (Core AC#2,
// [state-blind]/[predicate-parity]). For EACH of the 7 canonical invoice
// lifecycle states (internal/invoice/invoice.go:29-35), a stored
// (entity, number) row in EXACTLY that state must be flagged by BOTH (i)
// the importer's ExistingNumbers precheck AND (ii) invoice.Store.Create's
// unique-index backstop -- the two mechanisms the story's Design Question 2
// argues are behaviourally identical despite sharing no SQL.
//
// Meaningfulness (why this is not tautological): ExistingNumbers's query
// (store.go:146-149) is an unconditional `SELECT invoice_number FROM
// invoices WHERE entity_id = $1 AND invoice_number = ANY($2)` -- no `status`
// predicate at all. If a future edit added one (e.g. `AND status = 'draft'`,
// mirroring a superficially-plausible "only flag re-importable drafts"
// reading), assertion (i) below would start failing for every non-draft
// state in this sweep -- draft would still pass (vacuously), but
// validated/queued/submitted/accepted/rejected/failed would each report
// existing["INV-..."] == false while (ii) (the index, which carries no such
// condition) still correctly returns ErrDuplicateNumber. The sweep's PAIRING
// of (i) and (ii) per state is what catches the divergence -- a sweep that
// checked only (i) or only (ii) in isolation would not. Verified empirically
// below (not just argued): temporarily adding `AND status = 'draft'` to
// ExistingNumbers's query and re-running this test turns exactly the 6
// non-draft subtests RED on assertion (i), confirming the sweep catches a
// state-partial regression precisely where it would occur.
func TestPredicateParity_StateBlindImporterIndexParitySweep(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	states := []invoice.Status{
		invoice.StatusDraft,
		invoice.StatusValidated,
		invoice.StatusQueued,
		invoice.StatusSubmitted,
		invoice.StatusAccepted,
		invoice.StatusRejected,
		invoice.StatusFailed,
	}

	ibStore := NewStore(app)
	invStore := invoice.NewStore(app)

	for _, state := range states {
		state := state
		t.Run(string(state), func(t *testing.T) {
			tenantID := seedTenant(t, super, "PAR-01 "+string(state)+" tenant")
			entityID := seedEntity(t, super, tenantID, "PAR-01 "+string(state)+" entity")
			number := "INV-" + string(state)

			invID := seedInvoice(t, super, tenantID, entityID, number)
			if _, err := super.Exec(ctx,
				`UPDATE invoices SET status = $1 WHERE id = $2`, string(state), invID,
			); err != nil {
				t.Fatalf("seed status=%s: %v", state, err)
			}

			c := auth.WithIdentity(ctx, auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: tenantID})

			// (i) the importer precheck flags it, regardless of the stored
			// row's status.
			existing, err := ibStore.ExistingNumbers(c, entityID, []string{number})
			if err != nil {
				t.Fatalf("ExistingNumbers: %v", err)
			}
			if !existing[number] {
				t.Errorf("ExistingNumbers[%q] (stored status=%s) = %v, want true -- the importer precheck must be state-blind (PAR-01, Core AC#2)", number, state, existing[number])
			}

			// (ii) the manual store-level backstop rejects a same-number
			// Create, regardless of the stored row's own state.
			_, err = invStore.Create(c, invoice.CreateInput{EntityID: entityID, InvoiceNumber: number})
			if !errors.Is(err, invoice.ErrDuplicateNumber) {
				t.Errorf("Store.Create(%q) against a stored row with status=%s: err = %v, want ErrDuplicateNumber (PAR-01, Core AC#2)", number, state, err)
			}
		})
	}
}

// --- PAR-02 --------------------------------------------------------------

// PAR-02: index-shape guard (read-only, Core AC#2, [uniqueness-scope]). The
// structural twin of PAR-01's behavioural sweep: confirms
// invoices_tenant_entity_number_uq is UNIQUE over EXACTLY (tenant_id,
// entity_id, invoice_number), in that order, with NO WHERE clause
// (non-partial). Asserted on the FULL indexdef string, not merely a
// column-list substring: the sibling invoices_import_batch_id_idx IS
// partial (`... WHERE (import_batch_id IS NOT NULL)`,
// migrations/20260714103137_invoices.sql:89), so "does the string mention a
// WHERE clause at all" is a meaningful, non-vacuous check on THIS index --
// it is not simply true of every index in this table.
//
// Meaningfulness: if a future migration made this index partial (e.g.
// `WHERE status <> 'rejected'`), that state's row would no longer be
// rejected by Store.Create (the index would stop covering it) even though
// ExistingNumbers's unconditional SELECT still flagged it -- exactly the
// divergence PAR-01 exists to catch, but PAR-02 catches it earlier and more
// directly by reading the index definition itself. Verified empirically: a
// throwaway `ALTER INDEX ... ` cannot add a WHERE post-hoc in Postgres (a
// partial index must be created with the predicate), so the equivalent
// regression was verified instead by pointing this exact query at
// invoices_import_batch_id_idx (the real partial sibling) and confirming
// the `strings.Contains(def, "WHERE")` assertion below correctly fires RED
// for it -- proving the assertion is not vacuously true for every index in
// this table.
func TestPredicateParity_IndexShapeIsUniqueNonPartialOnExactColumns(t *testing.T) {
	super, _ := dbTestPools(t)
	ctx := context.Background()

	var def string
	if err := super.QueryRow(ctx,
		`SELECT pg_get_indexdef('invoices_tenant_entity_number_uq'::regclass)`,
	).Scan(&def); err != nil {
		t.Fatalf("pg_get_indexdef: %v", err)
	}

	if !strings.Contains(def, "UNIQUE") {
		t.Errorf("indexdef = %q, want it to contain %q", def, "UNIQUE")
	}
	if !strings.Contains(def, "(tenant_id, entity_id, invoice_number)") {
		t.Errorf("indexdef = %q, want the exact column list %q in order", def, "(tenant_id, entity_id, invoice_number)")
	}
	if strings.Contains(def, "WHERE") {
		t.Errorf("indexdef = %q, want NO WHERE clause -- invoices_tenant_entity_number_uq must stay non-partial (state-blind, [uniqueness-scope], PAR-02 Core AC#2)", def)
	}
}

// --- PAR-05 --------------------------------------------------------------

// PAR-05: cross-tenant RLS -- no false collision (Core AC#5,
// [dedup-boundary]). Mirrors IB-STORE-04's two-tenant pattern
// (store_test.go:287-315), but exercises BOTH against-store mechanisms this
// story touches: a same-numbered invoice under tenant B must register as
// neither an importer-precheck hit nor a manual Store.Create collision for
// tenant A's own entity. Pairs positive read-backs (tenant A gets its own
// row) with the negative ones (tenant B's is untouched, never treated as a
// collision) so this cannot vacuously pass on a nil/empty result.
func TestPredicateParity_CrossTenantNoFalseCollision(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantA := seedTenant(t, super, "PAR-05 tenant A")
	tenantB := seedTenant(t, super, "PAR-05 tenant B")
	entityA := seedEntity(t, super, tenantA, "PAR-05 A entity")
	entityB := seedEntity(t, super, tenantB, "PAR-05 B entity")

	seedInvoice(t, super, tenantB, entityB, "INV-X") // tenant B's own "INV-X"

	cA := auth.WithIdentity(ctx, auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: tenantA})

	// (i) the importer precheck does not see tenant B's row.
	ibStore := NewStore(app)
	existing, err := ibStore.ExistingNumbers(cA, entityA, []string{"INV-X"})
	if err != nil {
		t.Fatalf("ExistingNumbers: %v", err)
	}
	if existing["INV-X"] {
		t.Errorf(`ExistingNumbers(as tenant A)["INV-X"] = true, want absent -- tenant B's row must be RLS-scoped out (PAR-05, Core AC#5)`)
	}

	// (i.b) the importer precheck does not see tenant B's row even when
	// queried through the FOREIGN entity (entityB) -- proves RLS scoping,
	// not mere entity-scoping (a query that only filtered by entity_id
	// would still find it here).
	foreign, err := ibStore.ExistingNumbers(cA, entityB, []string{"INV-X"})
	if err != nil {
		t.Fatalf("ExistingNumbers(as tenant A, foreign entityB): %v", err)
	}
	if foreign["INV-X"] {
		t.Error(`ExistingNumbers(as tenant A, entityB)["INV-X"] = true, want absent -- tenant B's row must be RLS-scoped out, not merely entity-scoped (PAR-05, Core AC#5)`)
	}

	// (ii) a manual Create for tenant A's own entity succeeds -- the DB
	// backstop is likewise tenant-scoped (the unique index leads with
	// tenant_id).
	invStore := invoice.NewStore(app)
	inv, err := invStore.Create(cA, invoice.CreateInput{EntityID: entityA, InvoiceNumber: "INV-X"})
	if err != nil {
		t.Fatalf(`Store.Create(tenant A, entity A, "INV-X") = %v, want success -- tenant B's own "INV-X" must not collide (PAR-05, Core AC#5)`, err)
	}
	if inv.ID == "" {
		t.Fatal("Store.Create returned an empty id")
	}

	if got := countInvoicesByNumber(t, super, entityA, "INV-X"); got != 1 {
		t.Errorf("tenant A's own INV-X rows = %d, want 1 (just created)", got)
	}
	if got := countInvoicesByNumber(t, super, entityB, "INV-X"); got != 1 {
		t.Errorf("tenant B's own INV-X rows = %d, want unchanged 1 -- tenant A's Create must not have touched it", got)
	}
}

// --- PAR-06 --------------------------------------------------------------

// PAR-06: self-exclusion by construction (Core AC#6). HOME: package
// importer. The story's own argument (System Design, Design Question 3):
// the only against-store check here compares un-persisted CANDIDATE rows
// (no id yet, dry-run refs are literally the invoice_number) against the
// store, so a persisted row can never "flag its own number" -- there is no
// code path that compares a persisted row against itself. Re-importing an
// already-stored number is simply the ordinary duplicate path: the new
// candidate is quarantined, and the STORED row is neither duplicated nor
// mutated by the attempt.
func TestPredicateParity_SelfExclusionReimportLeavesStoredRowUntouched(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := seedTenant(t, super, "PAR-06 tenant")
	entityID := seedEntity(t, super, tenantID, "PAR-06 entity")
	seedInvoice(t, super, tenantID, entityID, "INV-SELF")

	svc := newTestService(app)
	c := auth.WithIdentity(ctx, auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: tenantID})

	rows := [][]string{
		mkRow("INV-SELF", "2026-01-10", "T1", "B1", "NGN", "10.00", "1.00", "11.00", "SelfItem", "1", "10.00"), // sheet 2
	}

	res, err := svc.Import(c, entityID, stdMapping, stdHeader, rows, false)
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	if res.ReadyInvoices != 0 || res.QuarantinedInvoices != 1 {
		t.Fatalf("(ReadyInvoices=%d QuarantinedInvoices=%d), want (0,1) -- the re-import candidate must be quarantined, never treated as its own twin", res.ReadyInvoices, res.QuarantinedInvoices)
	}
	if len(res.Errors) != 1 {
		t.Fatalf("len(Errors) = %d, want 1: %+v", len(res.Errors), res.Errors)
	}
	if got := res.Errors[0].RuleKey; got != ruleKeyDuplicateInvoiceNumber {
		t.Errorf("RuleKey = %q, want %q", got, ruleKeyDuplicateInvoiceNumber)
	}

	if got := countInvoicesByNumber(t, super, entityID, "INV-SELF"); got != 1 {
		t.Errorf("INV-SELF rows = %d, want exactly 1 (the original stored row, unmodified -- self-exclusion by construction, PAR-06 Core AC#6)", got)
	}
}

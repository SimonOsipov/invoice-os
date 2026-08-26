// AUDIT-11-05 adversarial: the three properties the loop test leaves open. Everything else
// on this surface belongs to AUDIT-11-09, which proves it against raw-INSERT rows and
// audit.Query; these three need a real writer AND audit.NewStore().List, whose identity ->
// tenant-GUC seam and membership gate 09 never crosses.
//
// Run: `DEV_DB_PORT=5442 make test-invoice` (go test -p 1). A bare run with only DEV_DB_PORT
// set skips every case here and still prints ok (CF-6).
package invoice

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/SimonOsipov/invoice-os/internal/audit"
	"github.com/SimonOsipov/invoice-os/internal/platform/db"
)

// anSharedNumber is held by an invoice in BOTH tenants. invoices_tenant_entity_number_uq
// scopes uniqueness to one tenant, so this is a legal collision and the sharpest form of
// the isolation question.
const anSharedNumber = "AN-SHARED-42"

// The number search does not cross tenants through the request seam. AUDIT-11-09 asserts
// this against raw-INSERT rows through audit.Query with an explicit tenant string; here
// both tenants' rows come from the real writer and the tenant comes from the identity.
func TestAuditNumber_SearchByAnotherTenantsIdentityFindsNothing(t *testing.T) {
	super, app := dbTestPools(t)

	tenantA := seedTenant(t, super, "AN cross-tenant A")
	tenantB := seedTenant(t, super, "AN cross-tenant B")
	entityA := seedEntity(t, super, tenantA, "AN cross-tenant A entity")
	entityB := seedEntity(t, super, tenantB, "AN cross-tenant B entity")

	ctxA, _ := auditNumberIdentity(tenantA)
	ctxB, _ := auditNumberIdentity(tenantB)
	store := NewStore(app)

	invA, err := store.Create(ctxA, CreateInput{EntityID: entityA, InvoiceNumber: anSharedNumber})
	if err != nil {
		t.Fatalf("Create in tenant A: %v", err)
	}
	invB, err := store.Create(ctxB, CreateInput{EntityID: entityB, InvoiceNumber: anSharedNumber})
	if err != nil {
		t.Fatalf("Create in tenant B: %v", err)
	}
	number := mustInvoiceNumber(t, super, invA.ID)
	if number != mustInvoiceNumber(t, super, invB.ID) {
		t.Fatalf("the two invoices do not share a number, so there is no collision to fence")
	}

	// Controls first: each tenant really can reach its own row by that number. Without
	// them a reader that returned nothing to anybody would pass.
	fromA := auditSearchList(t, app, tenantA, number)
	a := auditSearchFind(t, app, tenantA, fromA, number, "invoice.created", invA.ID)
	auditSearchAssertNumber(t, a, "store.go:269", number)

	fromB := auditSearchList(t, app, tenantB, number)
	b := auditSearchFind(t, app, tenantB, fromB, number, "invoice.created", invB.ID)
	auditSearchAssertNumber(t, b, "store.go:269", number)

	// Neither page may carry the other tenant's row, and neither total may count it.
	assertNoForeignRow(t, "tenant A", fromA.Events, invB.ID)
	assertNoForeignRow(t, "tenant B", fromB.Events, invA.ID)
	if fromA.Total != 1 {
		t.Errorf("tenant A's page reports total %d for %q, want 1 -- tenant B holds a second invoice with that number", fromA.Total, number)
	}
	if fromB.Total != 1 {
		t.Errorf("tenant B's page reports total %d for %q, want 1 -- tenant A holds a second invoice with that number", fromB.Total, number)
	}
}

func assertNoForeignRow(t *testing.T, label string, events []audit.Event, foreignID string) {
	t.Helper()
	for _, e := range events {
		if strings.Contains(string(e.Payload), foreignID) {
			t.Errorf("%s's page carries a %s row naming the other tenant's invoice %s; payload = %s",
				label, e.Event, foreignID, e.Payload)
		}
	}
}

// One number, several event types, one search. The loop test drives ten writers into ten
// separate tenants and asserts one row each, so nothing yet says a search returns the whole
// history of an invoice rather than a single row.
func TestAuditNumber_SearchReturnsEveryEventTypeForTheNumber(t *testing.T) {
	const number = "AN-MULTI-EVENT-1"

	super, app := dbTestPools(t)
	tenantID := seedTenant(t, super, "AN multi-event tenant")
	entityID := seedEntity(t, super, tenantID, "AN multi-event entity")
	ctx, _ := auditNumberIdentity(tenantID)
	store := NewStore(app)

	inv, err := store.Create(ctx, CreateInput{EntityID: entityID, InvoiceNumber: number})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	buyer := "AN multi buyer"
	if _, err := store.Update(ctx, inv.ID, UpdateInput{BuyerName: &buyer}); err != nil {
		t.Fatalf("Update: %v", err)
	}
	if _, err := store.Transition(ctx, inv.ID, StatusValidated); err != nil {
		t.Fatalf("Transition(draft->validated): %v", err)
	}

	want := mustInvoiceNumber(t, super, inv.ID)
	got := auditSearchList(t, app, tenantID, want)

	for _, event := range []string{"invoice.created", "invoice.updated", "invoice.transitioned"} {
		e := auditSearchFind(t, app, tenantID, got, want, event, inv.ID)
		auditSearchAssertNumber(t, e, "three writers, one invoice", want)
	}
	if got.Total != 3 {
		t.Errorf("q=%q reports total %d, want 3 -- three real writers wrote for this invoice and one search must return all of them:%s",
			want, got.Total, auditSearchDump(got))
	}
	// The facet count is derived from the same predicate, so a page that widened silently
	// would show it here.
	if len(got.Facets.Event) != 3 {
		t.Errorf("q=%q returned %d event facets, want 3:%s", want, len(got.Facets.Event), auditSearchDump(got))
	}
}

// errAuditNumberForcedRollback aborts the closure after the writer has run.
var errAuditNumberForcedRollback = errors.New("audit number: forced rollback")

// An audit row written in a transaction that rolls back is not findable. The row and the
// column write share one transaction, so the search must see neither.
func TestAuditNumber_RolledBackWriterLeavesNothingFindable(t *testing.T) {
	const rolledBack, committed = "AN-ROLLBACK-1", "AN-COMMITTED-1"

	super, app := dbTestPools(t)
	ctx := context.Background()
	tenantID := seedTenant(t, super, "AN rollback tenant")
	entityID := seedEntity(t, super, tenantID, "AN rollback entity")
	store := NewStore(app)
	versionID := seedRuleSetVersionID(t, super)
	vs := []Violation{{RuleKey: "vat-standard-rate", Severity: "error", Message: "bad rate"}}

	// seedInvoiceWithViolations writes no audit row, so the invoice exists and its number
	// resolves while the log holds nothing for it.
	aborted := seedInvoiceWithViolations(t, super, tenantID, entityID, rolledBack, string(StatusValidated), "[]")
	kept := seedInvoiceWithViolations(t, super, tenantID, entityID, committed, string(StatusValidated), "[]")

	err := db.WithinTenantTx(ctx, app, tenantID, func(tx pgx.Tx) error {
		if _, err := store.DemoteRevalidatedTx(ctx, tx, aborted, tenantID, vs, versionID); err != nil {
			return err
		}
		return errAuditNumberForcedRollback
	})
	if !errors.Is(err, errAuditNumberForcedRollback) {
		t.Fatalf("WithinTenantTx returned %v, want the forced rollback -- the writer must have run before the abort", err)
	}

	// Positive control: the identical drive, committed, IS findable. Without it a search
	// that was simply broken would satisfy the assertion below.
	if err := db.WithinTenantTx(ctx, app, tenantID, func(tx pgx.Tx) error {
		_, e := store.DemoteRevalidatedTx(ctx, tx, kept, tenantID, vs, versionID)
		return e
	}); err != nil {
		t.Fatalf("DemoteRevalidatedTx (committed control): %v", err)
	}
	keptNumber := mustInvoiceNumber(t, super, kept)
	control := auditSearchList(t, app, tenantID, keptNumber)
	e := auditSearchFind(t, app, tenantID, control, keptNumber, "invoice.validated", kept)
	auditSearchAssertNumber(t, e, "revalidate.go:81", keptNumber)

	abortedNumber := mustInvoiceNumber(t, super, aborted)
	if abortedNumber == "" || strings.Contains(keptNumber, abortedNumber) {
		t.Fatalf("the rolled-back number %q is not distinguishable from the committed one %q", abortedNumber, keptNumber)
	}
	got := auditSearchList(t, app, tenantID, abortedNumber)
	if got.Total != 0 || len(got.Events) != 0 {
		t.Errorf("q=%q returned %d rows (total %d) for an invoice whose only writer rolled back; the number still resolves through invoices, so the log itself must hold nothing:%s",
			abortedNumber, len(got.Events), got.Total, auditSearchDump(got))
	}
}

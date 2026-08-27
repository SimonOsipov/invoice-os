// INVCR-01-06 (task-282): RED acceptance tests for the review-screen filters
// on Store.List -- ImportBatchID, Status, NeedsFix, RuleKey, Query -- written
// BEFORE Store.List/ListHandler apply any of them (Stage 2.5 compile
// scaffolding: the 5 fields exist on ListFilter, invoice.go, but store.go's
// List ignores them entirely, so every assertion below fails on its VALUE --
// the filtered total/membership -- not on a compile error).
//
// Spec-to-test map (Implementation Plan §7, task-282):
//
//	spec 1  TestStoreList_ImportBatchIDNarrowsBeforePaging      (AC-2)
//	spec 2  TestRLS_ListImportBatchIDCrossTenantIsEmptyNot404   (AC-7)
//	spec 3  TestStoreList_FiltersAreANDed                       (AC-2)
//	spec 4  TestStoreList_NeedsFixIsDraftWithBlockingViolation  (AC-4)
//	spec 5  TestStoreList_RuleKeyFilterIsParameterised          (AC-3)
//	spec 6  TestStoreList_QueryMatchesNumberOrBuyer             (AC-5)
//	spec 11 TestStoreList_EmptyBatchReturnsEmptySliceNotNil     (AC-8, GREEN BEFORE)
//
// Plus one test beyond the architect's 11-row table, added to satisfy this
// task's own honesty requirement ("if a q filter is in scope, its spec must
// pin the parenthesisation") -- spec 6 tests Query in isolation, which
// cannot discriminate an unparenthesised OR fragment from a parenthesised
// one:
//
//	(extra) TestStoreList_QueryANDsWithImportBatchID             (AC-2/AC-5)
//
// Specs 7-9 (handler-level) live in handlers_test.go; spec 10
// (TestStoreList_ZeroFilterQueryUnchanged, GREEN BEFORE) lives in
// store_test.go. seedImportBatch is reused from import_batch_test.go:37, NOT
// redefined here.
//
// Run: `make test-rls` will NOT cover this package (it targets
// ./internal/platform/db/... at port 5432) -- use:
//
//	DATABASE_URL="postgres://invoice_app:app@localhost:5434/invoice_os?sslmode=disable" \
//	DATABASE_SUPERUSER_URL="postgres://postgres:postgres@localhost:5434/invoice_os?sslmode=disable" \
//	go test -count=1 -p 1 ./internal/invoice/...
//
// BULK-01-02 (task-306) addendum: ListFilter.ImportBatchID widened to
// ImportBatchIDs []string ([one-review-screen]) so the review screen can
// narrow across every batch a multi-file run produced, on one query
// (`= ANY($n)`, no cast -- Decision 2). The 6 ListFilter{ImportBatchID: ...}
// sites above are MIGRATED IN PLACE to one-element slices (every existing
// assertion keeps its meaning); new multi-id specs are appended at the
// bottom of this file:
//
//	BULK-02-1 TestStoreList_SeveralImportBatchIDsUnion                       (AC-1)
//	BULK-02-2 TestStoreList_OneImportBatchIDUnchanged                        (AC-1)
//	BULK-02-3 TestStoreList_EmptyOrNilImportBatchIDsIsAbsent                 (AC-1)
//	BULK-02-4 TestStoreList_SeveralImportBatchIDsNarrowBeforePaging          (AC-1)
//	BULK-02-6 TestStoreList_MalformedImportBatchIDMemberIsValidationError    (AC-3)
//	BULK-02-7 TestRLS_SeveralImportBatchIDsCrossTenantMemberIsInvisible      (AC-9)
//
// Store.List's own Mode A stub (store.go) narrows on ONLY
// f.ImportBatchIDs[0] via plain "=" -- the several-ids union is deliberately
// NOT implemented yet, so every spec above except BULK-02-2/3 fails on
// membership/total or on a nil error where ErrValidation is wanted, never on
// a compile or setup error.
package invoice

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/SimonOsipov/invoice-os/internal/platform/auth"

	"github.com/google/uuid"
)

// seedInvoiceWithBatchAt seeds an invoice with an explicit import_batch_id
// (nil for none) and a forced created_at -- neither seedInvoice nor
// seedInvoiceAt (entity_filter_test.go:40) writes the import_batch_id
// column, so this combines the two idioms rather than adding a third
// unrelated helper.
func seedInvoiceWithBatchAt(t *testing.T, super *pgxpool.Pool, tenantID, entityID, number string, batchID *string, createdAt time.Time) string {
	t.Helper()
	ctx := context.Background()
	var id string
	if err := super.QueryRow(ctx,
		`INSERT INTO invoices (tenant_id, entity_id, invoice_number, import_batch_id) VALUES ($1, $2, $3, $4) RETURNING id`,
		tenantID, entityID, number, batchID,
	).Scan(&id); err != nil {
		t.Fatalf("seed invoices (with batch): %v", err)
	}
	t.Cleanup(func() {
		_, _ = super.Exec(context.Background(), `DELETE FROM invoices WHERE id = $1`, id)
	})
	if _, err := super.Exec(ctx, `UPDATE invoices SET created_at = $1 WHERE id = $2`, createdAt, id); err != nil {
		t.Fatalf("force-seed invoice created_at: %v", err)
	}
	return id
}

// seedInvoiceWithBatchAndStatus seeds an invoice under a given import batch,
// then force-writes status/violations exactly like seedInvoiceWithViolations
// (store_test.go:160) -- combines the two so specs can compose ImportBatchID
// with Status/NeedsFix fixtures without a second Create-independent seeding
// path.
func seedInvoiceWithBatchAndStatus(t *testing.T, super *pgxpool.Pool, tenantID, entityID, number, batchID, status, violationsJSON string) string {
	t.Helper()
	id := seedInvoiceWithBatchAt(t, super, tenantID, entityID, number, &batchID, time.Now().UTC())
	if _, err := super.Exec(context.Background(),
		`UPDATE invoices SET status = $1, violations = $2::jsonb WHERE id = $3`,
		status, violationsJSON, id,
	); err != nil {
		t.Fatalf("force-seed invoice status/violations: %v", err)
	}
	return id
}

// seedInvoiceWithBuyer seeds an invoice with an explicit invoice_number and
// buyer_name -- needed for the Query (q) filter spec, which searches
// invoice_number OR buyer_name.
func seedInvoiceWithBuyer(t *testing.T, super *pgxpool.Pool, tenantID, entityID, number, buyerName string) string {
	t.Helper()
	ctx := context.Background()
	var id string
	if err := super.QueryRow(ctx,
		`INSERT INTO invoices (tenant_id, entity_id, invoice_number, buyer_name) VALUES ($1, $2, $3, $4) RETURNING id`,
		tenantID, entityID, number, buyerName,
	).Scan(&id); err != nil {
		t.Fatalf("seed invoices (with buyer): %v", err)
	}
	t.Cleanup(func() {
		_, _ = super.Exec(context.Background(), `DELETE FROM invoices WHERE id = $1`, id)
	})
	return id
}

// seedInvoiceWithTINs seeds an invoice with explicit buyer_tin/supplier_tin
// -- needed for the Query (q) filter's TIN arms; no existing helper writes
// these columns. Pass "" for whichever TIN a given test doesn't care about.
func seedInvoiceWithTINs(t *testing.T, super *pgxpool.Pool, tenantID, entityID, number, buyerTIN, supplierTIN string) string {
	t.Helper()
	ctx := context.Background()
	var id string
	if err := super.QueryRow(ctx,
		`INSERT INTO invoices (tenant_id, entity_id, invoice_number, buyer_tin, supplier_tin) VALUES ($1, $2, $3, $4, $5) RETURNING id`,
		tenantID, entityID, number, buyerTIN, supplierTIN,
	).Scan(&id); err != nil {
		t.Fatalf("seed invoices (with TINs): %v", err)
	}
	t.Cleanup(func() {
		_, _ = super.Exec(context.Background(), `DELETE FROM invoices WHERE id = $1`, id)
	})
	return id
}

// TestStoreList_ImportBatchIDNarrowsBeforePaging (spec 1, AC-2): batch A's 3
// invoices are seeded at the OLDEST created_at; 60 "other" (no batch)
// invoices are seeded strictly newer, so the unfiltered newest-50
// tenant-wide window is entirely non-batch-A rows (total 63). A sanity
// pre-assert confirms the fixture actually exercises the trap before the
// real assertion: List(ImportBatchID: batchA, Limit: 50) must still return
// all 3 of batch A's rows and total 3 -- a filter-AFTER-paginate
// (re)implementation would return 0/0 here, exactly the
// [entity-id-cut]-class regression entity_filter_test.go:15-18 already
// guards for EntityID.
func TestStoreList_ImportBatchIDNarrowsBeforePaging(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := seedTenant(t, super, "BATCH-FILTER-PAGE tenant")
	entityID := seedEntity(t, super, tenantID, "BATCH-FILTER-PAGE entity")
	batchA := seedImportBatch(t, super, tenantID, entityID)

	base := time.Now().UTC()
	at := func(offsetHours int) time.Time { return base.Add(time.Duration(offsetHours) * time.Hour) }

	batchAIDs := make([]string, 0, 3)
	for i := 0; i < 3; i++ {
		batchAIDs = append(batchAIDs, seedInvoiceWithBatchAt(t, super, tenantID, entityID, fmt.Sprintf("BATCH-FILTER-PAGE-A-%d", i), &batchA, at(i)))
	}
	for i := 0; i < 60; i++ {
		// Offset by +10 so every "other" row sorts strictly newer than any
		// of batch A's 3.
		seedInvoiceWithBatchAt(t, super, tenantID, entityID, fmt.Sprintf("BATCH-FILTER-PAGE-OTHER-%d", i), nil, at(10+i))
	}

	store := NewStore(app)
	c := auth.WithIdentity(ctx, auth.Identity{Subject: memberSubject, Role: "authenticated", TenantID: tenantID})

	// Sanity: an unfiltered Limit:50 tenant-wide window is entirely "other"
	// rows. If this fails, the fixture no longer exercises the trap this
	// test exists to catch.
	unfiltered, unfilteredTotal, err := store.List(c, ListFilter{Limit: 50, Offset: 0})
	if err != nil {
		t.Fatalf("List (unfiltered sanity): %v", err)
	}
	if unfilteredTotal != 63 {
		t.Fatalf("List (unfiltered) total = %d, want 63 (3 batch A + 60 other)", unfilteredTotal)
	}
	batchASet := map[string]bool{}
	for _, id := range batchAIDs {
		batchASet[id] = true
	}
	for _, inv := range unfiltered {
		if batchASet[inv.ID] {
			t.Fatalf("fixture assumption broken: batch A's invoice %s is inside the newest-50 tenant-wide window -- this test no longer exercises the filter-after-paginate trap", inv.ID)
		}
	}

	// The real assertion: batch A's 3 rows are NOT in that newest-50 window,
	// yet List(ImportBatchID: batchA, Limit: 50) must still return all 3 --
	// proving the WHERE narrows before LIMIT runs, not after.
	items, total, err := store.List(c, ListFilter{ImportBatchIDs: []string{batchA}, Limit: 50, Offset: 0})
	if err != nil {
		t.Fatalf("List (ImportBatchID: batchA): %v", err)
	}
	if total != 3 {
		t.Fatalf("List (ImportBatchID: batchA).total = %d, want 3 (batch A's own filtered count, not 63 tenant-wide)", total)
	}
	if len(items) != 3 {
		t.Fatalf("List (ImportBatchID: batchA, limit=50) returned %d items, want 3 -- a filter-AFTER-paginate bug would return 0 here (batch A's rows aren't in the newest-50 tenant-wide window)", len(items))
	}
	for _, inv := range items {
		if inv.ImportBatchID == nil || *inv.ImportBatchID != batchA {
			t.Errorf("List (ImportBatchID: batchA) returned an invoice not in that batch: %+v", inv)
		}
	}
}

// TestRLS_ListImportBatchIDCrossTenantIsEmptyNot404 (spec 2, AC-7): tenant 2
// owns batch B (3 invoices); tenant 1 owns 2 invoices of its own, in its own
// batch A. Tenant 1 filtering by tenant 2's batch id must return an EMPTY
// page (total 0, err nil, never 404/ErrNotFound) -- and, in the SAME test,
// tenant 1 filtering by ITS OWN batch A must return its 2 rows. Store.List
// has no `WHERE tenant_id` (RLS alone scopes it), so without the same-tenant
// leg the cross-tenant assertion would pass vacuously even if the
// ImportBatchID filter were deleted entirely (needs_attention_test.go:166-174
// names this exact trap).
func TestRLS_ListImportBatchIDCrossTenantIsEmptyNot404(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenant1 := seedTenant(t, super, "BATCH-FILTER-RLS tenant 1")
	tenant2 := seedTenant(t, super, "BATCH-FILTER-RLS tenant 2")
	entity1 := seedEntity(t, super, tenant1, "BATCH-FILTER-RLS entity 1")
	entity2 := seedEntity(t, super, tenant2, "BATCH-FILTER-RLS entity 2")

	batchA := seedImportBatch(t, super, tenant1, entity1)
	batchB := seedImportBatch(t, super, tenant2, entity2)

	for i := 0; i < 2; i++ {
		seedInvoiceWithBatchAt(t, super, tenant1, entity1, fmt.Sprintf("BATCH-FILTER-RLS-A-%d", i), &batchA, time.Now().UTC())
	}
	for i := 0; i < 3; i++ {
		seedInvoiceWithBatchAt(t, super, tenant2, entity2, fmt.Sprintf("BATCH-FILTER-RLS-B-%d", i), &batchB, time.Now().UTC())
	}

	store := NewStore(app)
	c1 := auth.WithIdentity(ctx, auth.Identity{Subject: memberSubject, Role: "authenticated", TenantID: tenant1})

	// Cross-tenant: tenant 1 filtering by tenant 2's batch id must return an
	// empty page with total 0 -- never 404, never tenant 2's rows.
	crossItems, crossTotal, err := store.List(c1, ListFilter{ImportBatchIDs: []string{batchB}, Limit: 50})
	if err != nil {
		t.Fatalf("List (tenant 1, ImportBatchID: tenant 2's batch B) err = %v, want nil -- a cross-tenant batch id must be an EMPTY page, never an error/404", err)
	}
	if crossTotal != 0 {
		t.Errorf("List (tenant 1, ImportBatchID: batchB).total = %d, want 0", crossTotal)
	}
	if len(crossItems) != 0 {
		t.Errorf("List (tenant 1, ImportBatchID: batchB) len = %d, want 0", len(crossItems))
	}

	// Same-tenant leg, in the SAME test: proves the empty result above is
	// RLS narrowing an already-nonzero row set, not a vacuous pass from
	// tenant 1 never owning any rows of its own.
	sameItems, sameTotal, err := store.List(c1, ListFilter{ImportBatchIDs: []string{batchA}, Limit: 50})
	if err != nil {
		t.Fatalf("List (tenant 1, ImportBatchID: batchA): %v", err)
	}
	if sameTotal != 2 {
		t.Errorf("List (tenant 1, ImportBatchID: batchA).total = %d, want 2", sameTotal)
	}
	if len(sameItems) != 2 {
		t.Errorf("List (tenant 1, ImportBatchID: batchA) len = %d, want 2", len(sameItems))
	}
}

// TestStoreList_FiltersAreANDed (spec 3, AC-2): batch A gets 1 validated + 1
// erroring draft; batch B (out-of-batch) gets 1 validated row with the SAME
// status as the row we want. Filtering {ImportBatchID: batchA, Status:
// validated} must return exactly batch A's validated row -- if the two
// conditions were OR-composed instead of AND-composed, batch B's
// out-of-batch validated row would leak into the result.
func TestStoreList_FiltersAreANDed(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := seedTenant(t, super, "BATCH-FILTER-AND tenant")
	entityID := seedEntity(t, super, tenantID, "BATCH-FILTER-AND entity")
	batchA := seedImportBatch(t, super, tenantID, entityID)
	batchB := seedImportBatch(t, super, tenantID, entityID)

	aValidated := seedInvoiceWithBatchAndStatus(t, super, tenantID, entityID, "BATCH-FILTER-AND-A-validated", batchA, string(StatusValidated), `[]`)
	seedInvoiceWithBatchAndStatus(t, super, tenantID, entityID, "BATCH-FILTER-AND-A-error-draft", batchA, string(StatusDraft),
		`[{"rule_key":"vat-standard-rate","severity":"error","message":"bad rate"}]`)
	// Out-of-batch: same Status as the wanted row, different batch. This is
	// the row that discriminates OR-instead-of-AND.
	seedInvoiceWithBatchAndStatus(t, super, tenantID, entityID, "BATCH-FILTER-AND-B-validated", batchB, string(StatusValidated), `[]`)

	store := NewStore(app)
	c := auth.WithIdentity(ctx, auth.Identity{Subject: memberSubject, Role: "authenticated", TenantID: tenantID})

	items, total, err := store.List(c, ListFilter{ImportBatchIDs: []string{batchA}, Status: StatusValidated, Limit: 50})
	if err != nil {
		t.Fatalf("List (ImportBatchID: batchA, Status: validated): %v", err)
	}
	if total != 1 {
		t.Fatalf("List (ImportBatchID: batchA, Status: validated).total = %d, want 1", total)
	}
	if len(items) != 1 || items[0].ID != aValidated {
		t.Fatalf("List (ImportBatchID: batchA, Status: validated) items = %+v, want exactly [%s]", items, aValidated)
	}
}

// TestStoreList_NeedsFixIsDraftWithBlockingViolation (spec 4, AC-4): seeds a
// warning-only draft, an error draft, a validated invoice, and a REJECTED
// invoice. {NeedsFix: true} must return exactly the error draft --
// needs_fix = status='draft' AND violations @> error. The warning-only draft
// discriminates a severity-blind predicate; the rejected row discriminates
// the forbidden fold into needs_attention's fragment (which DOES match
// status IN ('rejected','failed')) -- needs_fix must NOT reuse that clause
// ([needs-fix-is-a-new-predicate]).
func TestStoreList_NeedsFixIsDraftWithBlockingViolation(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := seedTenant(t, super, "BATCH-FILTER-NEEDSFIX tenant")
	entityID := seedEntity(t, super, tenantID, "BATCH-FILTER-NEEDSFIX entity")

	warningDraftID := seedInvoiceWithViolations(t, super, tenantID, entityID, "BATCH-FILTER-NEEDSFIX-warning", string(StatusDraft),
		`[{"rule_key":"some-rule","severity":"warning","message":"advisory only"}]`)
	errorDraftID := seedInvoiceWithViolations(t, super, tenantID, entityID, "BATCH-FILTER-NEEDSFIX-error", string(StatusDraft),
		`[{"rule_key":"vat-standard-rate","severity":"error","message":"bad rate"}]`)
	validatedID := seedInvoiceWithViolations(t, super, tenantID, entityID, "BATCH-FILTER-NEEDSFIX-validated", string(StatusValidated), `[]`)
	rejectedID := seedInvoiceWithViolations(t, super, tenantID, entityID, "BATCH-FILTER-NEEDSFIX-rejected", string(StatusRejected), `[]`)

	excluded := map[string]string{
		warningDraftID: "warning-only draft",
		validatedID:    "validated",
		rejectedID:     "rejected (must not be folded into needs_fix via the needs_attention predicate)",
	}

	store := NewStore(app)
	c := auth.WithIdentity(ctx, auth.Identity{Subject: memberSubject, Role: "authenticated", TenantID: tenantID})

	items, total, err := store.List(c, ListFilter{NeedsFix: true, Limit: 50})
	if err != nil {
		t.Fatalf("List (NeedsFix: true): %v", err)
	}
	if total != 1 {
		t.Fatalf("List (NeedsFix: true).total = %d, want 1 (only the error draft)", total)
	}

	seen := map[string]bool{}
	for _, inv := range items {
		seen[inv.ID] = true
	}
	if !seen[errorDraftID] {
		t.Errorf("List (NeedsFix: true) is missing the error draft %s", errorDraftID)
	}
	for id, label := range excluded {
		if seen[id] {
			t.Errorf("List (NeedsFix: true) incorrectly returned the %s invoice %s", label, id)
		}
	}
	if len(items) != 1 {
		t.Errorf("List (NeedsFix: true) len = %d, want 1", len(items))
	}
}

// TestStoreList_NeedsFixExcludesKept (INVCR-01-15, D6, task-291, AC #9): two erroring
// drafts, one kept -- {NeedsFix: true} must return ONLY the un-kept one. D6: a kept
// row records a human decision to stop working on it and drops OUT of "Needs a fix",
// even though it still matches needs_fix's base shape (draft + a blocking violation).
func TestStoreList_NeedsFixExcludesKept(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := seedTenant(t, super, "BATCH-FILTER-NEEDSFIX-KEPT tenant")
	entityID := seedEntity(t, super, tenantID, "BATCH-FILTER-NEEDSFIX-KEPT entity")

	const violationsJSON = `[{"rule_key":"vat-standard-rate","severity":"error","message":"bad rate"}]`
	unkeptID := seedInvoiceWithViolations(t, super, tenantID, entityID, "BATCH-FILTER-NEEDSFIX-KEPT-unkept", string(StatusDraft), violationsJSON)
	keptID := seedInvoiceWithViolations(t, super, tenantID, entityID, "BATCH-FILTER-NEEDSFIX-KEPT-kept", string(StatusDraft), violationsJSON)
	if _, err := super.Exec(ctx,
		`UPDATE invoices SET kept_as_is_at = now(), kept_as_is_by = 'someone', kept_as_is_reason = 'triaged' WHERE id = $1`, keptID,
	); err != nil {
		t.Fatalf("seed kept-as-is triple: %v", err)
	}

	store := NewStore(app)
	c := auth.WithIdentity(ctx, auth.Identity{Subject: memberSubject, Role: "authenticated", TenantID: tenantID})

	items, total, err := store.List(c, ListFilter{NeedsFix: true, Limit: 50})
	if err != nil {
		t.Fatalf("List (NeedsFix: true): %v", err)
	}
	if total != 1 {
		t.Fatalf("List (NeedsFix: true).total = %d, want 1 (the kept row must drop out)", total)
	}
	if len(items) != 1 || items[0].ID != unkeptID {
		t.Fatalf("List (NeedsFix: true) items = %+v, want exactly [%s]", items, unkeptID)
	}
	for _, inv := range items {
		if inv.ID == keptID {
			t.Errorf("List (NeedsFix: true) incorrectly returned the kept invoice %s", keptID)
		}
	}
}

// TestStoreList_KeptAsIsFilterMatchesColumn (extra, task-291): the review shell's
// footer counter query -- {KeptAsIs: true} must return exactly the kept row, not the
// unkept one carrying the identical violations. Not named in the architect's Test
// Specs table (that table only names the needs_fix exclusion); added because
// ListFilter.KeptAsIs/the `kept_as_is` query param are this subtask's own addition
// (task notes record why: AC #10's footer count needs a real server total, never an
// arithmetic derivation of the other totals, [filters-are-server-side]).
func TestStoreList_KeptAsIsFilterMatchesColumn(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := seedTenant(t, super, "BATCH-FILTER-KEPTASIS tenant")
	entityID := seedEntity(t, super, tenantID, "BATCH-FILTER-KEPTASIS entity")

	const violationsJSON = `[{"rule_key":"vat-standard-rate","severity":"error","message":"bad rate"}]`
	unkeptID := seedInvoiceWithViolations(t, super, tenantID, entityID, "BATCH-FILTER-KEPTASIS-unkept", string(StatusDraft), violationsJSON)
	keptID := seedInvoiceWithViolations(t, super, tenantID, entityID, "BATCH-FILTER-KEPTASIS-kept", string(StatusDraft), violationsJSON)
	if _, err := super.Exec(ctx,
		`UPDATE invoices SET kept_as_is_at = now(), kept_as_is_by = 'someone', kept_as_is_reason = 'triaged' WHERE id = $1`, keptID,
	); err != nil {
		t.Fatalf("seed kept-as-is triple: %v", err)
	}

	store := NewStore(app)
	c := auth.WithIdentity(ctx, auth.Identity{Subject: memberSubject, Role: "authenticated", TenantID: tenantID})

	items, total, err := store.List(c, ListFilter{KeptAsIs: true, Limit: 50})
	if err != nil {
		t.Fatalf("List (KeptAsIs: true): %v", err)
	}
	if total != 1 {
		t.Fatalf("List (KeptAsIs: true).total = %d, want 1", total)
	}
	if len(items) != 1 || items[0].ID != keptID {
		t.Fatalf("List (KeptAsIs: true) items = %+v, want exactly [%s]", items, keptID)
	}
	for _, inv := range items {
		if inv.ID == unkeptID {
			t.Errorf("List (KeptAsIs: true) incorrectly returned the un-kept invoice %s", unkeptID)
		}
	}
}

// TestStoreList_KeptAsIsExcludesResolvedFailed (T6-9/T6-10): a resolved
// failed invoice (the widened mark) no longer means "kept as-is" and must
// drop out of {KeptAsIs: true}, while a kept blocked draft -- the sibling test
// above's own fixture -- still counts exactly as it does today.
func TestStoreList_KeptAsIsExcludesResolvedFailed(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := seedTenant(t, super, "BATCH-FILTER-KEPTASIS-RESOLVED tenant")
	entityID := seedEntity(t, super, tenantID, "BATCH-FILTER-KEPTASIS-RESOLVED entity")

	resolvedFailedID := seedResolvedFailed(t, super, tenantID, entityID, "BATCH-FILTER-KEPTASIS-RESOLVED-failed", uuid.NewString(), "resolved outside")

	const violationsJSON = `[{"rule_key":"vat-standard-rate","severity":"error","message":"bad rate"}]`
	keptDraftID := seedInvoiceWithViolations(t, super, tenantID, entityID, "BATCH-FILTER-KEPTASIS-RESOLVED-kept-draft", string(StatusDraft), violationsJSON)
	if _, err := super.Exec(ctx,
		`UPDATE invoices SET kept_as_is_at = now(), kept_as_is_by = 'someone', kept_as_is_reason = 'triaged' WHERE id = $1`, keptDraftID,
	); err != nil {
		t.Fatalf("seed kept-as-is triple: %v", err)
	}

	store := NewStore(app)
	c := auth.WithIdentity(ctx, auth.Identity{Subject: memberSubject, Role: "authenticated", TenantID: tenantID})

	items, total, err := store.List(c, ListFilter{KeptAsIs: true, Limit: 50})
	if err != nil {
		t.Fatalf("List (KeptAsIs: true): %v", err)
	}
	if total != 1 {
		t.Fatalf("List (KeptAsIs: true).total = %d, want 1 (only the kept draft; the resolved failed row must not count)", total)
	}
	if len(items) != 1 || items[0].ID != keptDraftID {
		t.Fatalf("List (KeptAsIs: true) items = %+v, want exactly [%s]", items, keptDraftID)
	}
	for _, inv := range items {
		if inv.ID == resolvedFailedID {
			t.Errorf("List (KeptAsIs: true) incorrectly returned the resolved failed invoice %s", resolvedFailedID)
		}
	}
}

// TestStoreList_RuleKeyFilterIsParameterised (spec 5, AC-3): one invoice
// violates vat-standard-rate, another violates currency-allowed.
// {RuleKey: "vat-standard-rate"} must return exactly the first. A
// quote-bearing rule_key sub-case must return a clean 0-row result with
// err == nil -- a string-interpolated (fmt.Sprintf-built) rule_key would
// instead emit malformed JSON/SQL and surface as a non-nil error (22P02 or
// a syntax error), not a filtered miss.
func TestStoreList_RuleKeyFilterIsParameterised(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := seedTenant(t, super, "BATCH-FILTER-RULEKEY tenant")
	entityID := seedEntity(t, super, tenantID, "BATCH-FILTER-RULEKEY entity")

	vatID := seedInvoiceWithViolations(t, super, tenantID, entityID, "BATCH-FILTER-RULEKEY-vat", string(StatusDraft),
		`[{"rule_key":"vat-standard-rate","severity":"error","message":"bad rate"}]`)
	seedInvoiceWithViolations(t, super, tenantID, entityID, "BATCH-FILTER-RULEKEY-currency", string(StatusDraft),
		`[{"rule_key":"currency-allowed","severity":"error","message":"bad currency"}]`)

	store := NewStore(app)
	c := auth.WithIdentity(ctx, auth.Identity{Subject: memberSubject, Role: "authenticated", TenantID: tenantID})

	items, total, err := store.List(c, ListFilter{RuleKey: "vat-standard-rate", Limit: 50})
	if err != nil {
		t.Fatalf("List (RuleKey: vat-standard-rate): %v", err)
	}
	if total != 1 {
		t.Fatalf("List (RuleKey: vat-standard-rate).total = %d, want 1", total)
	}
	if len(items) != 1 || items[0].ID != vatID {
		t.Fatalf("List (RuleKey: vat-standard-rate) items = %+v, want exactly [%s]", items, vatID)
	}

	// A quote-bearing rule_key must be a clean 0-row result, err == nil --
	// discriminates string interpolation (which would raise a SQL/JSON
	// syntax error instead).
	quoteItems, quoteTotal, err := store.List(c, ListFilter{RuleKey: `bogus-'"-rule`, Limit: 50})
	if err != nil {
		t.Fatalf("List (RuleKey with quotes) err = %v, want nil -- a parameterised rule_key must never raise a SQL error for adversarial input", err)
	}
	if quoteTotal != 0 {
		t.Errorf("List (RuleKey with quotes).total = %d, want 0", quoteTotal)
	}
	if len(quoteItems) != 0 {
		t.Errorf("List (RuleKey with quotes) len = %d, want 0", len(quoteItems))
	}
}

// TestStoreList_QueryMatchesNumberOrBuyer (spec 6, AC-5): one invoice
// matches only via invoice_number ("zebra"), a second matches only via
// buyer_name ("acme") -- proving the OR spans both columns, not just one.
// {Query: "%"} must match NOTHING (0 rows): the literal "%" is escaped
// before binding, deliberately reversing portfolio's own unescaped ILIKE
// idiom (portfolio_test.go's TestStoreList_SearchQWildcardIsNotEscaped,
// which asserts q="%" matches EVERY row there) -- portfolio/* is untouched
// by this change.
func TestStoreList_QueryMatchesNumberOrBuyer(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := seedTenant(t, super, "BATCH-FILTER-Q tenant")
	entityID := seedEntity(t, super, tenantID, "BATCH-FILTER-Q entity")

	numberMatch := seedInvoiceWithBuyer(t, super, tenantID, entityID, "BATCHQ-ZEBRA-001", "Wonder Traders")
	buyerMatch := seedInvoiceWithBuyer(t, super, tenantID, entityID, "BATCHQ-XYZ-002", "Acme Imports")

	store := NewStore(app)
	c := auth.WithIdentity(ctx, auth.Identity{Subject: memberSubject, Role: "authenticated", TenantID: tenantID})

	// invoice_number arm.
	items, total, err := store.List(c, ListFilter{Query: "zebra", Limit: 50})
	if err != nil {
		t.Fatalf("List (Query: zebra): %v", err)
	}
	if total != 1 || len(items) != 1 || items[0].ID != numberMatch {
		t.Fatalf("List (Query: zebra) = (items=%+v, total=%d), want exactly [%s]/1", items, total, numberMatch)
	}

	// buyer_name arm -- proves the OR, not just the invoice_number side.
	items, total, err = store.List(c, ListFilter{Query: "acme", Limit: 50})
	if err != nil {
		t.Fatalf("List (Query: acme): %v", err)
	}
	if total != 1 || len(items) != 1 || items[0].ID != buyerMatch {
		t.Fatalf("List (Query: acme) = (items=%+v, total=%d), want exactly [%s]/1", items, total, buyerMatch)
	}

	// A literal "%" must be escaped, matching NOTHING -- discriminates the
	// unescaped portfolio idiom, which would match every row.
	wildItems, wildTotal, err := store.List(c, ListFilter{Query: "%", Limit: 50})
	if err != nil {
		t.Fatalf("List (Query: \"%%\"): %v", err)
	}
	if wildTotal != 0 {
		t.Errorf("List (Query: \"%%\").total = %d, want 0 (wildcards must be escaped, not matched literally against every row)", wildTotal)
	}
	if len(wildItems) != 0 {
		t.Errorf("List (Query: \"%%\") len = %d, want 0", len(wildItems))
	}
}

// TestStoreList_QueryMatchesTINs (BUG-01-04, AC-1): one invoice matches only
// via buyer_tin, a second only via supplier_tin -- proving the widened q
// clause's two new OR arms, not just invoice_number/buyer_name. RED today
// (0/0): store.go's q fragment has no buyer_tin/supplier_tin arm yet.
func TestStoreList_QueryMatchesTINs(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := seedTenant(t, super, "BATCH-FILTER-Q-TIN tenant")
	entityID := seedEntity(t, super, tenantID, "BATCH-FILTER-Q-TIN entity")

	buyerTinMatch := seedInvoiceWithTINs(t, super, tenantID, entityID, "BATCHQ-TIN-BUYER-001", "20033344-0003", "")
	supplierTinMatch := seedInvoiceWithTINs(t, super, tenantID, entityID, "BATCHQ-TIN-SUPPLIER-002", "", "99988877-0006")

	store := NewStore(app)
	c := auth.WithIdentity(ctx, auth.Identity{Subject: memberSubject, Role: "authenticated", TenantID: tenantID})

	items, total, err := store.List(c, ListFilter{Query: "20033344-0003", Limit: 50})
	if err != nil {
		t.Fatalf("List (Query: buyer_tin): %v", err)
	}
	if total != 1 || len(items) != 1 || items[0].ID != buyerTinMatch {
		t.Fatalf("List (Query: buyer_tin) = (items=%+v, total=%d), want exactly [%s]/1", items, total, buyerTinMatch)
	}

	items, total, err = store.List(c, ListFilter{Query: "99988877-0006", Limit: 50})
	if err != nil {
		t.Fatalf("List (Query: supplier_tin): %v", err)
	}
	if total != 1 || len(items) != 1 || items[0].ID != supplierTinMatch {
		t.Fatalf("List (Query: supplier_tin) = (items=%+v, total=%d), want exactly [%s]/1", items, total, supplierTinMatch)
	}
}

// seedInvoiceWithBatchAndBuyer seeds an invoice with both an explicit
// import_batch_id (nil for none) and buyer_name -- needed only by
// TestStoreList_QueryANDsWithImportBatchID below, which composes the two.
func seedInvoiceWithBatchAndBuyer(t *testing.T, super *pgxpool.Pool, tenantID, entityID, number string, batchID *string, buyerName string) string {
	t.Helper()
	ctx := context.Background()
	var id string
	if err := super.QueryRow(ctx,
		`INSERT INTO invoices (tenant_id, entity_id, invoice_number, import_batch_id, buyer_name) VALUES ($1, $2, $3, $4, $5) RETURNING id`,
		tenantID, entityID, number, batchID, buyerName,
	).Scan(&id); err != nil {
		t.Fatalf("seed invoices (with batch and buyer): %v", err)
	}
	t.Cleanup(func() {
		_, _ = super.Exec(context.Background(), `DELETE FROM invoices WHERE id = $1`, id)
	})
	return id
}

// TestStoreList_QueryANDsWithImportBatchID (extends spec 6 -- added beyond
// the architect's 11-row table to satisfy this task's explicit honesty
// requirement: "if a q filter is in scope, its spec must pin the
// parenthesisation"). TestStoreList_QueryMatchesNumberOrBuyer above tests
// Query in ISOLATION, which cannot discriminate an unparenthesised OR
// fragment from a parenthesised one -- with no second AND-condition present,
// there is nothing for an unparenthesised `invoice_number ILIKE $n OR
// buyer_name ILIKE $n` to leak past. This test supplies that second
// condition: an out-of-batch invoice ALSO matches the query text, so an
// unparenthesised fragment -- which would bind as
// `import_batch_id = $1 AND invoice_number ILIKE $2 OR buyer_name ILIKE $2`
// -- would silently evaporate the batch filter and leak this row into the
// result with a plausible (nonzero, not obviously-wrong) total.
func TestStoreList_QueryANDsWithImportBatchID(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := seedTenant(t, super, "BATCH-FILTER-Q-AND tenant")
	entityID := seedEntity(t, super, tenantID, "BATCH-FILTER-Q-AND entity")
	batchA := seedImportBatch(t, super, tenantID, entityID)

	inBatch := seedInvoiceWithBatchAndBuyer(t, super, tenantID, entityID, "BATCHQ-AND-INBATCH", &batchA, "Acme Traders")
	// Out-of-batch (no import_batch_id at all), but its buyer_name ALSO
	// matches the query text -- the row an unparenthesised OR would leak.
	seedInvoiceWithBatchAndBuyer(t, super, tenantID, entityID, "BATCHQ-AND-OUTOFBATCH", nil, "Acme Distributors")

	store := NewStore(app)
	c := auth.WithIdentity(ctx, auth.Identity{Subject: memberSubject, Role: "authenticated", TenantID: tenantID})

	items, total, err := store.List(c, ListFilter{ImportBatchIDs: []string{batchA}, Query: "acme", Limit: 50})
	if err != nil {
		t.Fatalf("List (ImportBatchID: batchA, Query: acme): %v", err)
	}
	if total != 1 {
		t.Fatalf("List (ImportBatchID: batchA, Query: acme).total = %d, want 1 -- an unparenthesised q fragment would let the out-of-batch row leak in via OR", total)
	}
	if len(items) != 1 || items[0].ID != inBatch {
		t.Fatalf("List (ImportBatchID: batchA, Query: acme) items = %+v, want exactly [%s]", items, inBatch)
	}
}

// TestStoreList_QueryANDsWithEntityID (BUG-01-04, AC-3): two entities each
// hold a row carrying the SAME buyer_tin -- an unparenthesised q fragment
// (`entity_id = $1 AND (... OR buyer_tin ILIKE $2 ...)` missing its outer
// parens) would let entity B's matching row leak past the entity_id filter
// via OR, same trap as TestStoreList_QueryANDsWithImportBatchID above but
// exercising the new TIN arm specifically.
func TestStoreList_QueryANDsWithEntityID(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := seedTenant(t, super, "BATCH-FILTER-Q-AND-ENTITY tenant")
	entityA := seedEntity(t, super, tenantID, "BATCH-FILTER-Q-AND-ENTITY A")
	entityB := seedEntity(t, super, tenantID, "BATCH-FILTER-Q-AND-ENTITY B")

	const tin = "20033344-0003"
	rowA := seedInvoiceWithTINs(t, super, tenantID, entityA, "BATCHQ-AND-ENTITY-A", tin, "")
	seedInvoiceWithTINs(t, super, tenantID, entityB, "BATCHQ-AND-ENTITY-B", tin, "")

	store := NewStore(app)
	c := auth.WithIdentity(ctx, auth.Identity{Subject: memberSubject, Role: "authenticated", TenantID: tenantID})

	items, total, err := store.List(c, ListFilter{EntityID: entityA, Query: tin, Limit: 50})
	if err != nil {
		t.Fatalf("List (EntityID: A, Query: tin): %v", err)
	}
	if total != 1 {
		t.Fatalf("List (EntityID: A, Query: tin).total = %d, want 1 -- an unparenthesised q fragment would let entity B's matching row leak in via OR", total)
	}
	if len(items) != 1 || items[0].ID != rowA {
		t.Fatalf("List (EntityID: A, Query: tin) items = %+v, want exactly [%s]", items, rowA)
	}
}

// TestStoreList_EmptyBatchReturnsEmptySliceNotNil (spec 11, AC-8): GREEN
// BEFORE -- a regression guard, not red-first. Replaces the originally
// proposed TestListHandler_EmptyPageIsArrayNotNull, which was a pure
// duplicate of TestListHandler_EmptyState (handlers_test.go:614, already
// asserts "invoices":[] verbatim). This closes the uncovered half at the
// store layer: a real batch with genuinely zero invoices under it must
// still yield a non-nil []Invoice{} from Store.List, not a nil slice --
// already guaranteed today by `items := []Invoice{}` (store.go:490), and
// must keep holding once the filter is wired.
func TestStoreList_EmptyBatchReturnsEmptySliceNotNil(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := seedTenant(t, super, "BATCH-FILTER-EMPTY tenant")
	entityID := seedEntity(t, super, tenantID, "BATCH-FILTER-EMPTY entity")
	batchID := seedImportBatch(t, super, tenantID, entityID)
	// Deliberately no invoices seeded under batchID (or at all) -- this
	// tenant is otherwise completely empty.

	store := NewStore(app)
	c := auth.WithIdentity(ctx, auth.Identity{Subject: memberSubject, Role: "authenticated", TenantID: tenantID})

	items, total, err := store.List(c, ListFilter{ImportBatchIDs: []string{batchID}, Limit: 50})
	if err != nil {
		t.Fatalf("List (ImportBatchID: an empty batch): %v", err)
	}
	if total != 0 {
		t.Fatalf("List (ImportBatchID: an empty batch).total = %d, want 0", total)
	}
	if items == nil {
		t.Fatal("List (ImportBatchID: an empty batch) returned a nil slice, want non-nil []Invoice{} (AC-8: invoices must be [] on the wire, never null)")
	}
	if len(items) != 0 {
		t.Fatalf("List (ImportBatchID: an empty batch) len = %d, want 0", len(items))
	}
}

// --- BULK-01-02 (task-306): several import_batch_ids on one Store.List query ---

// TestStoreList_SeveralImportBatchIDsUnion (BULK-02-1, AC-1): three batches
// seeded with 2/3/1 invoices respectively -- ImportBatchIDs=[batch1,batch3]
// must return the UNION of batch1's and batch3's rows (total 3), excluding
// batch2's 3 entirely. A first-element-only (or last-element-only)
// implementation would return 2 or 1, never the union's 3.
func TestStoreList_SeveralImportBatchIDsUnion(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := seedTenant(t, super, "BATCH-FILTER-UNION tenant")
	entityID := seedEntity(t, super, tenantID, "BATCH-FILTER-UNION entity")
	batch1 := seedImportBatch(t, super, tenantID, entityID)
	batch2 := seedImportBatch(t, super, tenantID, entityID)
	batch3 := seedImportBatch(t, super, tenantID, entityID)

	var wantIDs []string
	for i := 0; i < 2; i++ {
		wantIDs = append(wantIDs, seedInvoiceWithBatchAt(t, super, tenantID, entityID, fmt.Sprintf("UNION-B1-%d", i), &batch1, time.Now().UTC()))
	}
	for i := 0; i < 3; i++ {
		seedInvoiceWithBatchAt(t, super, tenantID, entityID, fmt.Sprintf("UNION-B2-%d", i), &batch2, time.Now().UTC())
	}
	wantIDs = append(wantIDs, seedInvoiceWithBatchAt(t, super, tenantID, entityID, "UNION-B3-0", &batch3, time.Now().UTC()))

	store := NewStore(app)
	c := auth.WithIdentity(ctx, auth.Identity{Subject: memberSubject, Role: "authenticated", TenantID: tenantID})

	items, total, err := store.List(c, ListFilter{ImportBatchIDs: []string{batch1, batch3}, Limit: 50})
	if err != nil {
		t.Fatalf("List (ImportBatchIDs: [batch1, batch3]): %v", err)
	}
	if total != 3 {
		t.Fatalf("List (ImportBatchIDs: [batch1, batch3]).total = %d, want 3 (2 from batch1 + 1 from batch3, batch2's 3 excluded)", total)
	}
	if len(items) != 3 {
		t.Fatalf("List (ImportBatchIDs: [batch1, batch3]) len = %d, want 3", len(items))
	}
	want := map[string]bool{}
	for _, id := range wantIDs {
		want[id] = true
	}
	for _, inv := range items {
		if !want[inv.ID] {
			t.Errorf("List (ImportBatchIDs: [batch1, batch3]) returned an invoice not in batch1 or batch3: %s", inv.ID)
		}
	}
}

// TestStoreList_OneImportBatchIDUnchanged (BULK-02-2, AC-1): a one-element
// ImportBatchIDs slice must return EXACTLY what the shipped single-id filter
// returned before this subtask -- backward compatibility is total.
func TestStoreList_OneImportBatchIDUnchanged(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := seedTenant(t, super, "BATCH-FILTER-ONEID tenant")
	entityID := seedEntity(t, super, tenantID, "BATCH-FILTER-ONEID entity")
	batchA := seedImportBatch(t, super, tenantID, entityID)
	batchB := seedImportBatch(t, super, tenantID, entityID)

	var wantIDs []string
	for i := 0; i < 3; i++ {
		wantIDs = append(wantIDs, seedInvoiceWithBatchAt(t, super, tenantID, entityID, fmt.Sprintf("ONEID-A-%d", i), &batchA, time.Now().UTC()))
	}
	for i := 0; i < 2; i++ {
		seedInvoiceWithBatchAt(t, super, tenantID, entityID, fmt.Sprintf("ONEID-B-%d", i), &batchB, time.Now().UTC())
	}

	store := NewStore(app)
	c := auth.WithIdentity(ctx, auth.Identity{Subject: memberSubject, Role: "authenticated", TenantID: tenantID})

	items, total, err := store.List(c, ListFilter{ImportBatchIDs: []string{batchA}, Limit: 50})
	if err != nil {
		t.Fatalf("List (ImportBatchIDs: [batchA]): %v", err)
	}
	if total != 3 {
		t.Fatalf("List (ImportBatchIDs: [batchA]).total = %d, want 3 (batch A's own count, unaffected by batch B's 2)", total)
	}
	if len(items) != 3 {
		t.Fatalf("List (ImportBatchIDs: [batchA]) len = %d, want 3", len(items))
	}
	seen := map[string]bool{}
	for _, inv := range items {
		seen[inv.ID] = true
	}
	for _, id := range wantIDs {
		if !seen[id] {
			t.Errorf("List (ImportBatchIDs: [batchA]) is missing batch A's invoice %s", id)
		}
	}
}

// TestStoreList_EmptyOrNilImportBatchIDsIsAbsent (BULK-02-3, AC-1): an empty
// (non-nil, zero-length) slice AND a nil slice must both apply NO predicate
// -- the tenant-wide page -- never an empty-result WHERE. `= ANY('{}')`
// (binding an empty array) matches NOTHING, which is exactly the regression
// this guards: the fragment must be gated on `len(f.ImportBatchIDs) > 0`,
// never a bare `f.ImportBatchIDs != nil`.
func TestStoreList_EmptyOrNilImportBatchIDsIsAbsent(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := seedTenant(t, super, "BATCH-FILTER-EMPTYSLICE tenant")
	entityID := seedEntity(t, super, tenantID, "BATCH-FILTER-EMPTYSLICE entity")
	batchA := seedImportBatch(t, super, tenantID, entityID)

	for i := 0; i < 3; i++ {
		seedInvoiceWithBatchAt(t, super, tenantID, entityID, fmt.Sprintf("EMPTYSLICE-%d", i), &batchA, time.Now().UTC())
	}

	store := NewStore(app)
	c := auth.WithIdentity(ctx, auth.Identity{Subject: memberSubject, Role: "authenticated", TenantID: tenantID})

	nilItems, nilTotal, err := store.List(c, ListFilter{ImportBatchIDs: nil, Limit: 50})
	if err != nil {
		t.Fatalf("List (ImportBatchIDs: nil): %v", err)
	}
	if nilTotal != 3 {
		t.Fatalf("List (ImportBatchIDs: nil).total = %d, want 3 (tenant-wide, unfiltered)", nilTotal)
	}
	if len(nilItems) != 3 {
		t.Fatalf("List (ImportBatchIDs: nil) len = %d, want 3", len(nilItems))
	}

	emptyItems, emptyTotal, err := store.List(c, ListFilter{ImportBatchIDs: []string{}, Limit: 50})
	if err != nil {
		t.Fatalf("List (ImportBatchIDs: []): %v", err)
	}
	if emptyTotal != 3 {
		t.Fatalf("List (ImportBatchIDs: []).total = %d, want 3 (tenant-wide, unfiltered -- a `= ANY('{}')` regression would return 0)", emptyTotal)
	}
	if len(emptyItems) != 3 {
		t.Fatalf("List (ImportBatchIDs: []) len = %d, want 3", len(emptyItems))
	}
}

// TestStoreList_SeveralImportBatchIDsNarrowBeforePaging (BULK-02-4, AC-1):
// three batches of 60 invoices each (180 total); filtering by TWO of the
// three ids with Limit:50 must report pagination.total 120 (2 batches x 60),
// never 180 (all three, unfiltered) and never 50 (the page size mistaken for
// the total).
func TestStoreList_SeveralImportBatchIDsNarrowBeforePaging(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := seedTenant(t, super, "BATCH-FILTER-MULTIPAGE tenant")
	entityID := seedEntity(t, super, tenantID, "BATCH-FILTER-MULTIPAGE entity")
	batch1 := seedImportBatch(t, super, tenantID, entityID)
	batch2 := seedImportBatch(t, super, tenantID, entityID)
	batch3 := seedImportBatch(t, super, tenantID, entityID)

	for _, b := range []string{batch1, batch2, batch3} {
		for i := 0; i < 60; i++ {
			seedInvoiceWithBatchAt(t, super, tenantID, entityID, fmt.Sprintf("MULTIPAGE-%s-%d", b, i), &b, time.Now().UTC())
		}
	}

	store := NewStore(app)
	c := auth.WithIdentity(ctx, auth.Identity{Subject: memberSubject, Role: "authenticated", TenantID: tenantID})

	// Sanity: the tenant-wide unfiltered total really is 180.
	_, unfilteredTotal, err := store.List(c, ListFilter{Limit: 50})
	if err != nil {
		t.Fatalf("List (unfiltered sanity): %v", err)
	}
	if unfilteredTotal != 180 {
		t.Fatalf("List (unfiltered) total = %d, want 180 (3 batches x 60) -- fixture assumption broken", unfilteredTotal)
	}

	items, total, err := store.List(c, ListFilter{ImportBatchIDs: []string{batch1, batch2}, Limit: 50})
	if err != nil {
		t.Fatalf("List (ImportBatchIDs: [batch1, batch2]): %v", err)
	}
	if total != 120 {
		t.Fatalf("List (ImportBatchIDs: [batch1, batch2]).total = %d, want 120 (2 batches x 60), not 180 (tenant-wide) and not 50 (page size)", total)
	}
	if len(items) != 50 {
		t.Fatalf("List (ImportBatchIDs: [batch1, batch2], limit=50) len = %d, want 50 (the page window, even though total is 120)", len(items))
	}
}

// TestStoreList_MalformedImportBatchIDMemberIsValidationError (BULK-02-6,
// AC-3): a second, malformed member in ImportBatchIDs must map to
// ErrValidation, not a raw 500 -- proven against the REAL DB (dbTestPools,
// DATABASE_URL/DATABASE_SUPERUSER_URL), the only way to tell a genuine
// Postgres 22P02 (invalid_text_representation, the server's own uuid parser
// rejecting it) apart from a client-side pgx encode failure. Verified
// empirically (BULK-01-02, live against this worktree's DB): binding a Go
// []string containing one malformed member via `= ANY($1)` against a uuid
// column encodes in TEXT format with each member written verbatim, so the
// malformed one reaches Postgres's own uuid parser (string_to_uuid) and
// raises 22P02 there -- SQLSTATE 22P02, Routine "string_to_uuid" -- exactly
// where today's single-id path already lands, so the existing
// pgCode(err) == "22P02" check maps it correctly once the store binds the
// WHOLE slice via ANY(), no ::uuid[] cast needed.
func TestStoreList_MalformedImportBatchIDMemberIsValidationError(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := seedTenant(t, super, "BATCH-FILTER-MALFORMED tenant")
	entityID := seedEntity(t, super, tenantID, "BATCH-FILTER-MALFORMED entity")
	valid := seedImportBatch(t, super, tenantID, entityID)

	store := NewStore(app)
	c := auth.WithIdentity(ctx, auth.Identity{Subject: memberSubject, Role: "authenticated", TenantID: tenantID})

	_, _, err := store.List(c, ListFilter{ImportBatchIDs: []string{valid, "not-a-uuid"}, Limit: 50})
	if !errors.Is(err, ErrValidation) {
		t.Fatalf(`List (ImportBatchIDs: [valid, "not-a-uuid"]) err = %v, want ErrValidation`, err)
	}
}

// TestRLS_SeveralImportBatchIDsCrossTenantMemberIsInvisible (BULK-02-7,
// AC-9): tenant 1 filters by a slice containing tenant 2's batch id
// ALONGSIDE its own -- RLS must silently drop tenant 2's rows from the union
// (never an error, never a 404), returning ONLY tenant 1's own rows and
// total. The cross-tenant id is placed FIRST in the slice so a
// first-element-only implementation (today's Mode A stub) returns 0 rather
// than tenant 1's real count -- discriminating it from the eventual
// `= ANY($n)` union, under which RLS alone (not the WHERE fragment) is what
// makes tenant 2's id contribute nothing.
func TestRLS_SeveralImportBatchIDsCrossTenantMemberIsInvisible(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenant1 := seedTenant(t, super, "BATCH-FILTER-RLS-MULTI tenant 1")
	tenant2 := seedTenant(t, super, "BATCH-FILTER-RLS-MULTI tenant 2")
	entity1 := seedEntity(t, super, tenant1, "BATCH-FILTER-RLS-MULTI entity 1")
	entity2 := seedEntity(t, super, tenant2, "BATCH-FILTER-RLS-MULTI entity 2")

	batchA := seedImportBatch(t, super, tenant1, entity1)
	batchB := seedImportBatch(t, super, tenant2, entity2)

	var wantIDs []string
	for i := 0; i < 2; i++ {
		wantIDs = append(wantIDs, seedInvoiceWithBatchAt(t, super, tenant1, entity1, fmt.Sprintf("RLS-MULTI-A-%d", i), &batchA, time.Now().UTC()))
	}
	for i := 0; i < 3; i++ {
		seedInvoiceWithBatchAt(t, super, tenant2, entity2, fmt.Sprintf("RLS-MULTI-B-%d", i), &batchB, time.Now().UTC())
	}

	store := NewStore(app)
	c1 := auth.WithIdentity(ctx, auth.Identity{Subject: memberSubject, Role: "authenticated", TenantID: tenant1})

	items, total, err := store.List(c1, ListFilter{ImportBatchIDs: []string{batchB, batchA}, Limit: 50})
	if err != nil {
		t.Fatalf("List (tenant 1, ImportBatchIDs: [batchB, batchA]) err = %v, want nil", err)
	}
	if total != 2 {
		t.Fatalf("List (tenant 1, ImportBatchIDs: [batchB, batchA]).total = %d, want 2 (tenant 1's own batch A rows only; tenant 2's batch B contributes nothing under RLS)", total)
	}
	if len(items) != 2 {
		t.Fatalf("List (tenant 1, ImportBatchIDs: [batchB, batchA]) len = %d, want 2", len(items))
	}
	seen := map[string]bool{}
	for _, inv := range items {
		seen[inv.ID] = true
	}
	for _, id := range wantIDs {
		if !seen[id] {
			t.Errorf("List (tenant 1, ImportBatchIDs: [batchB, batchA]) is missing tenant 1's own invoice %s", id)
		}
	}
}

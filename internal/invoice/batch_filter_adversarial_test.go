// QA Mode B adversarial coverage for INVCR-01-06 (task-282), added on top of
// the executor's 12 red-first specs in batch_filter_test.go. Those specs
// exercise the five review-screen filters individually or two at a time;
// this file closes three gaps the QA pass identified were still missing:
//
//   - a genuine 5-way combined filter, asserting total AND per-row id
//     membership (a "right total, wrong rows" bug is invisible without the
//     per-row check)
//   - pagination.total reflecting the FILTERED count under a multi-filter
//     query, not the tenant-wide total
//   - placeholder numbering staying correct when NeedsFix (a literal
//     condition that consumes no bind arg) sits between two bound
//     conditions in the actual AND chain
//
// seedTenant/seedEntity/seedImportBatch/dbTestPools are reused from
// store_test.go / import_batch_test.go, not redefined.
package invoice

import (
	"context"
	"fmt"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/SimonOsipov/invoice-os/internal/platform/auth"

	"github.com/google/uuid"
)

// seedInvoiceFull seeds an invoice with every column the five review filters
// can key on: import_batch_id (nil for none), status, violations, and
// buyer_name -- no single existing helper in batch_filter_test.go or
// store_test.go sets all four at once.
func seedInvoiceFull(t *testing.T, super *pgxpool.Pool, tenantID, entityID, number string, batchID *string, status, violationsJSON, buyerName string) string {
	t.Helper()
	ctx := context.Background()
	var id string
	if err := super.QueryRow(ctx,
		`INSERT INTO invoices (tenant_id, entity_id, invoice_number, import_batch_id, buyer_name) VALUES ($1, $2, $3, $4, $5) RETURNING id`,
		tenantID, entityID, number, batchID, buyerName,
	).Scan(&id); err != nil {
		t.Fatalf("seed invoices (full): %v", err)
	}
	t.Cleanup(func() {
		_, _ = super.Exec(context.Background(), `DELETE FROM invoices WHERE id = $1`, id)
	})
	if _, err := super.Exec(ctx,
		`UPDATE invoices SET status = $1, violations = $2::jsonb WHERE id = $3`,
		status, violationsJSON, id,
	); err != nil {
		t.Fatalf("force-seed invoice status/violations (full): %v", err)
	}
	return id
}

// TestStoreList_AllFiveFiltersCombinedANDCorrectRows (QA Mode B adversarial,
// AC-2): all five review filters set at once -- ImportBatchID, Status,
// NeedsFix, RuleKey, Query -- must AND together to select exactly the one
// row matching every predicate. Four distractor rows are seeded, each
// matching every filter EXCEPT one, so a bug in any single predicate (or in
// the AND composition generally) leaks a distractor into the result. Both
// total and per-row id membership are asserted: a "right total, wrong rows"
// implementation (e.g. one that matches on count but returns a different
// row) would pass a total-only assertion but fails here.
func TestStoreList_AllFiveFiltersCombinedANDCorrectRows(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := seedTenant(t, super, "BATCH-FILTER-COMBO tenant")
	entityID := seedEntity(t, super, tenantID, "BATCH-FILTER-COMBO entity")
	batchA := seedImportBatch(t, super, tenantID, entityID)
	batchB := seedImportBatch(t, super, tenantID, entityID)

	const vatErrorViolation = `[{"rule_key":"vat-standard-rate","severity":"error","message":"bad rate"}]`
	const currencyErrorViolation = `[{"rule_key":"currency-allowed","severity":"error","message":"bad currency"}]`

	// Target: matches ImportBatchID=batchA, Status=draft, NeedsFix (draft +
	// blocking violation), RuleKey=vat-standard-rate, and Query="acme"
	// (via buyer_name) -- all five at once.
	target := seedInvoiceFull(t, super, tenantID, entityID, "COMBO-TARGET", &batchA,
		string(StatusDraft), vatErrorViolation, "Acme Combo Ltd")

	// D1 fails ONLY Status/NeedsFix (validated, not draft) -- everything
	// else (batch, buyer name) matches.
	seedInvoiceFull(t, super, tenantID, entityID, "COMBO-D1-STATUS", &batchA,
		string(StatusValidated), `[]`, "Acme D1")

	// D2 fails ONLY ImportBatchID (batch B, not A) -- status/violations/buyer
	// all otherwise match.
	seedInvoiceFull(t, super, tenantID, entityID, "COMBO-D2-BATCH", &batchB,
		string(StatusDraft), vatErrorViolation, "Acme D2")

	// D3 fails ONLY RuleKey (currency-allowed, not vat-standard-rate) --
	// still draft + error violation, so it still satisfies NeedsFix.
	seedInvoiceFull(t, super, tenantID, entityID, "COMBO-D3-RULEKEY", &batchA,
		string(StatusDraft), currencyErrorViolation, "Acme D3")

	// D4 fails ONLY Query (buyer name "Zebra", invoice number has no "acme")
	// -- batch/status/violation all otherwise match.
	seedInvoiceFull(t, super, tenantID, entityID, "ZBR-D4-QUERY", &batchA,
		string(StatusDraft), vatErrorViolation, "Zebra D4")

	store := NewStore(app)
	c := auth.WithIdentity(ctx, auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: tenantID})

	items, total, err := store.List(c, ListFilter{
		ImportBatchIDs: []string{batchA},
		Status:         StatusDraft,
		NeedsFix:       true,
		RuleKey:        "vat-standard-rate",
		Query:          "acme",
		Limit:          50,
	})
	if err != nil {
		t.Fatalf("List (all five filters): %v", err)
	}
	if total != 1 {
		t.Fatalf("List (all five filters).total = %d, want 1 -- got a distractor leaking through a broken AND composition", total)
	}
	if len(items) != 1 || items[0].ID != target {
		t.Fatalf("List (all five filters) items = %+v, want exactly [%s]", items, target)
	}
}

// TestStoreList_FilteredTotalNotTenantWide (QA Mode B adversarial, AC-2): a
// tenant with a large tenant-wide row count (20 unrelated invoices) plus a
// small subset matching a two-filter combination (Status + Query) --
// pagination's total must reflect the FILTERED count (2), never the
// tenant-wide count (22). Uses a small Limit so the filtered set doesn't
// even fit on one page, proving total is computed independently of the
// page window.
func TestStoreList_FilteredTotalNotTenantWide(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := seedTenant(t, super, "BATCH-FILTER-TOTAL tenant")
	entityID := seedEntity(t, super, tenantID, "BATCH-FILTER-TOTAL entity")

	for i := 0; i < 20; i++ {
		seedInvoiceFull(t, super, tenantID, entityID, fmt.Sprintf("TOTAL-NOISE-%d", i), nil,
			string(StatusValidated), `[]`, "Unrelated Buyer")
	}

	var matchIDs []string
	for i := 0; i < 2; i++ {
		matchIDs = append(matchIDs, seedInvoiceFull(t, super, tenantID, entityID, fmt.Sprintf("TOTAL-MATCH-%d", i), nil,
			string(StatusDraft), `[]`, "Acme Filtered Buyer"))
	}

	store := NewStore(app)
	c := auth.WithIdentity(ctx, auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: tenantID})

	// Sanity: the tenant-wide (unfiltered) total really is 22, so a bug that
	// reports the tenant-wide total instead of the filtered one is
	// distinguishable from the correct answer of 2.
	_, unfilteredTotal, err := store.List(c, ListFilter{Limit: 1})
	if err != nil {
		t.Fatalf("List (unfiltered sanity): %v", err)
	}
	if unfilteredTotal != 22 {
		t.Fatalf("List (unfiltered) total = %d, want 22 (20 noise + 2 match) -- fixture assumption broken", unfilteredTotal)
	}

	items, total, err := store.List(c, ListFilter{Status: StatusDraft, Query: "acme", Limit: 1})
	if err != nil {
		t.Fatalf("List (Status: draft, Query: acme, Limit: 1): %v", err)
	}
	if total != 2 {
		t.Fatalf("List (Status: draft, Query: acme).total = %d, want 2 (the filtered count), not 22 (tenant-wide)", total)
	}
	if len(items) != 1 {
		t.Fatalf("List (Status: draft, Query: acme, Limit: 1) returned %d items, want 1 (the page window), even though total is 2", len(items))
	}
	matched := map[string]bool{matchIDs[0]: true, matchIDs[1]: true}
	if !matched[items[0].ID] {
		t.Errorf("List (Status: draft, Query: acme, Limit: 1) returned an invoice not in the matching set: %s", items[0].ID)
	}
}

// TestStoreList_NeedsFixBetweenTwoBoundFiltersKeepsPlaceholderNumbering (QA
// Mode B adversarial, AC-2/AC-3): NeedsFix is a literal SQL fragment that
// consumes NO bind argument, and in Store.List's own if-block order it sits
// BETWEEN two bound conditions -- Status (which does not bind either, but
// ImportBatchID before it and RuleKey after it both do). This is exactly
// where an implementation that numbers placeholders by POSITION IN THE
// CONDITIONS LIST (instead of len(args), which only counts conditions that
// actually bind) would go off-by-one: RuleKey's placeholder would be
// computed one number too high, leaving args and $N references mismatched.
// Combines ImportBatchID (binds) + NeedsFix (literal, no bind) + RuleKey
// (binds) -- skipping Status entirely so NeedsFix is adjacent to both
// bound conditions on either side of it in the appended conditions slice.
func TestStoreList_NeedsFixBetweenTwoBoundFiltersKeepsPlaceholderNumbering(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := seedTenant(t, super, "BATCH-FILTER-PLACEHOLDER tenant")
	entityID := seedEntity(t, super, tenantID, "BATCH-FILTER-PLACEHOLDER entity")
	batchA := seedImportBatch(t, super, tenantID, entityID)

	const vatErrorViolation = `[{"rule_key":"vat-standard-rate","severity":"error","message":"bad rate"}]`
	const currencyErrorViolation = `[{"rule_key":"currency-allowed","severity":"error","message":"bad currency"}]`

	target := seedInvoiceFull(t, super, tenantID, entityID, "PLACEHOLDER-TARGET", &batchA,
		string(StatusDraft), vatErrorViolation, "irrelevant buyer")
	// Same batch, same NeedsFix-satisfying shape, but a DIFFERENT rule_key --
	// if RuleKey's placeholder number were wrong (e.g. off by one because
	// NeedsFix's no-bind condition was miscounted), this row could either
	// leak in (RuleKey filter silently defeated) or the query could error
	// outright (parameter count mismatch).
	seedInvoiceFull(t, super, tenantID, entityID, "PLACEHOLDER-OTHER-RULEKEY", &batchA,
		string(StatusDraft), currencyErrorViolation, "irrelevant buyer")

	store := NewStore(app)
	c := auth.WithIdentity(ctx, auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: tenantID})

	items, total, err := store.List(c, ListFilter{
		ImportBatchIDs: []string{batchA},
		NeedsFix:       true,
		RuleKey:        "vat-standard-rate",
		Limit:          50,
	})
	if err != nil {
		t.Fatalf("List (ImportBatchID, NeedsFix, RuleKey -- NeedsFix between two bound conditions): %v", err)
	}
	if total != 1 {
		t.Fatalf("List (ImportBatchID, NeedsFix, RuleKey).total = %d, want 1 -- placeholder numbering likely shifted by NeedsFix's no-bind condition", total)
	}
	if len(items) != 1 || items[0].ID != target {
		t.Fatalf("List (ImportBatchID, NeedsFix, RuleKey) items = %+v, want exactly [%s]", items, target)
	}
}

// TestStoreList_SeveralImportBatchIDsANDNeedsFix (BULK-02-5, task-306, AC-1):
// the several-ids union inside ImportBatchIDs must still AND against the
// OTHER filters, never OR across the whole condition list. Batches 1 and 2
// each get one needs-fix row and one validated (non-needs-fix) row; batch 3
// (out of the id list) gets a needs-fix row too. {ImportBatchIDs: [batch1,
// batch2], NeedsFix: true} must return EXACTLY the two needs-fix rows from
// batch1/batch2 -- batch3's needs-fix row (wrong batch) and batch1/2's
// validated rows (wrong status) must both be excluded.
func TestStoreList_SeveralImportBatchIDsANDNeedsFix(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := seedTenant(t, super, "BATCH-FILTER-MULTI-AND tenant")
	entityID := seedEntity(t, super, tenantID, "BATCH-FILTER-MULTI-AND entity")
	batch1 := seedImportBatch(t, super, tenantID, entityID)
	batch2 := seedImportBatch(t, super, tenantID, entityID)
	batch3 := seedImportBatch(t, super, tenantID, entityID)

	const errorViolation = `[{"rule_key":"vat-standard-rate","severity":"error","message":"bad rate"}]`

	fix1 := seedInvoiceWithBatchAndStatus(t, super, tenantID, entityID, "MULTI-AND-B1-FIX", batch1, string(StatusDraft), errorViolation)
	seedInvoiceWithBatchAndStatus(t, super, tenantID, entityID, "MULTI-AND-B1-OK", batch1, string(StatusValidated), `[]`)
	fix2 := seedInvoiceWithBatchAndStatus(t, super, tenantID, entityID, "MULTI-AND-B2-FIX", batch2, string(StatusDraft), errorViolation)
	seedInvoiceWithBatchAndStatus(t, super, tenantID, entityID, "MULTI-AND-B2-OK", batch2, string(StatusValidated), `[]`)
	// Out-of-list batch, same NeedsFix-satisfying shape -- discriminates an
	// OR-composed implementation (which would leak this row in) from an
	// AND-composed one.
	seedInvoiceWithBatchAndStatus(t, super, tenantID, entityID, "MULTI-AND-B3-FIX", batch3, string(StatusDraft), errorViolation)

	store := NewStore(app)
	c := auth.WithIdentity(ctx, auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: tenantID})

	items, total, err := store.List(c, ListFilter{ImportBatchIDs: []string{batch1, batch2}, NeedsFix: true, Limit: 50})
	if err != nil {
		t.Fatalf("List (ImportBatchIDs: [batch1, batch2], NeedsFix: true): %v", err)
	}
	if total != 2 {
		t.Fatalf("List (ImportBatchIDs: [batch1, batch2], NeedsFix: true).total = %d, want 2 -- batch3's needs-fix row or batch1/2's validated rows leaked through", total)
	}
	want := map[string]bool{fix1: true, fix2: true}
	if len(items) != 2 {
		t.Fatalf("List (ImportBatchIDs: [batch1, batch2], NeedsFix: true) len = %d, want 2", len(items))
	}
	for _, inv := range items {
		if !want[inv.ID] {
			t.Errorf("List (ImportBatchIDs: [batch1, batch2], NeedsFix: true) returned an unexpected invoice: %s", inv.ID)
		}
	}
}

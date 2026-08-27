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
	"reflect"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
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
	c := auth.WithIdentity(ctx, auth.Identity{Subject: memberSubject, Role: "authenticated", TenantID: tenantID})

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
	c := auth.WithIdentity(ctx, auth.Identity{Subject: memberSubject, Role: "authenticated", TenantID: tenantID})

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
	c := auth.WithIdentity(ctx, auth.Identity{Subject: memberSubject, Role: "authenticated", TenantID: tenantID})

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
	c := auth.WithIdentity(ctx, auth.Identity{Subject: memberSubject, Role: "authenticated", TenantID: tenantID})

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

// TestStoreList_SeveralImportBatchIDsOrderIndependent (QA Mode B adversarial,
// task-306, AC-1): `= ANY($n)` is a SET membership test, not a positional
// one -- [batch1, batch2] and [batch2, batch1] must return the identical
// row set and total. An implementation that (wrongly) special-cased the
// first or last element of the slice would diverge between the two orders;
// `= ANY(...)` itself cannot.
func TestStoreList_SeveralImportBatchIDsOrderIndependent(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := seedTenant(t, super, "BATCH-FILTER-ORDER tenant")
	entityID := seedEntity(t, super, tenantID, "BATCH-FILTER-ORDER entity")
	batch1 := seedImportBatch(t, super, tenantID, entityID)
	batch2 := seedImportBatch(t, super, tenantID, entityID)

	var wantIDs []string
	for i := 0; i < 2; i++ {
		wantIDs = append(wantIDs, seedInvoiceWithBatchAndStatus(t, super, tenantID, entityID, fmt.Sprintf("ORDER-B1-%d", i), batch1, string(StatusDraft), `[]`))
	}
	wantIDs = append(wantIDs, seedInvoiceWithBatchAndStatus(t, super, tenantID, entityID, "ORDER-B2-0", batch2, string(StatusDraft), `[]`))

	store := NewStore(app)
	c := auth.WithIdentity(ctx, auth.Identity{Subject: memberSubject, Role: "authenticated", TenantID: tenantID})

	forward, forwardTotal, err := store.List(c, ListFilter{ImportBatchIDs: []string{batch1, batch2}, Limit: 50})
	if err != nil {
		t.Fatalf("List (ImportBatchIDs: [batch1, batch2]): %v", err)
	}
	reversed, reversedTotal, err := store.List(c, ListFilter{ImportBatchIDs: []string{batch2, batch1}, Limit: 50})
	if err != nil {
		t.Fatalf("List (ImportBatchIDs: [batch2, batch1]): %v", err)
	}

	if forwardTotal != 3 || reversedTotal != 3 {
		t.Fatalf("total = %d (forward) / %d (reversed), want 3 for both orderings", forwardTotal, reversedTotal)
	}
	forwardIDs := map[string]bool{}
	for _, inv := range forward {
		forwardIDs[inv.ID] = true
	}
	reversedIDs := map[string]bool{}
	for _, inv := range reversed {
		reversedIDs[inv.ID] = true
	}
	if !reflect.DeepEqual(forwardIDs, reversedIDs) {
		t.Fatalf("row set differs by order: forward=%v reversed=%v, want identical sets", forwardIDs, reversedIDs)
	}
	for _, id := range wantIDs {
		if !forwardIDs[id] {
			t.Errorf("forward-order result is missing expected invoice %s", id)
		}
	}
}

// TestStoreList_SeveralImportBatchIDsDuplicatesDoNotDoubleCount (QA Mode B
// adversarial, task-306, AC-1/AC-5): the SAME batch id repeated in
// ImportBatchIDs must not double-count or duplicate rows -- `import_batch_id
// = ANY(['A','A'])` evaluates the predicate once PER INVOICE ROW regardless
// of how many times 'A' appears in the bound array, so [batchA] and
// [batchA, batchA] must yield byte-identical total AND row sets. Proven
// against the real DB rather than assumed from the SQL semantics alone,
// since Store.List's own Go-level construction of `args`/`conditions` is
// what could (in principle) introduce a second condition or duplicate a row
// in application code even though the SQL primitive itself cannot.
func TestStoreList_SeveralImportBatchIDsDuplicatesDoNotDoubleCount(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := seedTenant(t, super, "BATCH-FILTER-DUP tenant")
	entityID := seedEntity(t, super, tenantID, "BATCH-FILTER-DUP entity")
	batchA := seedImportBatch(t, super, tenantID, entityID)

	var wantIDs []string
	for i := 0; i < 3; i++ {
		wantIDs = append(wantIDs, seedInvoiceWithBatchAndStatus(t, super, tenantID, entityID, fmt.Sprintf("DUP-A-%d", i), batchA, string(StatusDraft), `[]`))
	}

	store := NewStore(app)
	c := auth.WithIdentity(ctx, auth.Identity{Subject: memberSubject, Role: "authenticated", TenantID: tenantID})

	once, onceTotal, err := store.List(c, ListFilter{ImportBatchIDs: []string{batchA}, Limit: 50})
	if err != nil {
		t.Fatalf("List (ImportBatchIDs: [batchA]): %v", err)
	}
	if onceTotal != 3 || len(once) != 3 {
		t.Fatalf("List (ImportBatchIDs: [batchA]) total/len = %d/%d, want 3/3 -- fixture assumption broken", onceTotal, len(once))
	}

	tripled, tripledTotal, err := store.List(c, ListFilter{ImportBatchIDs: []string{batchA, batchA, batchA}, Limit: 50})
	if err != nil {
		t.Fatalf("List (ImportBatchIDs: [batchA, batchA, batchA]): %v", err)
	}
	if tripledTotal != 3 {
		t.Fatalf("List (ImportBatchIDs: [batchA, batchA, batchA]).total = %d, want 3 (unchanged, not 9) -- duplicate ids must not multiply the count", tripledTotal)
	}
	if len(tripled) != 3 {
		t.Fatalf("List (ImportBatchIDs: [batchA, batchA, batchA]) len = %d, want 3 (each row exactly once, not duplicated in the page)", len(tripled))
	}
	seen := map[string]int{}
	for _, inv := range tripled {
		seen[inv.ID]++
	}
	for _, id := range wantIDs {
		if seen[id] != 1 {
			t.Errorf("invoice %s appeared %d times in the tripled-id result, want exactly 1", id, seen[id])
		}
	}
}

// TestStoreList_SeveralImportBatchIDsANDsWithOtherFilters (QA Mode B
// adversarial, task-306, AC-1): TestStoreList_SeveralImportBatchIDsANDNeedsFix
// above already proves ImportBatchIDs ANDs (never ORs) against NeedsFix.
// This extends the same discriminating shape -- a distractor row OUTSIDE
// the id list that satisfies the OTHER predicate ALONE, which an
// OR-composed implementation would leak into the result -- to the three
// remaining review filters Decision 2/3 name: NeedsAttention, Status,
// RuleKey, and Query. Each subtest seeds three rows: a target (in-list AND
// predicate-matching), an in-list non-match (predicate fails, proving the
// predicate is genuinely enforced, not just the batch id), and an
// out-of-list match (predicate passes, wrong batch -- the OR-discriminator:
// under OR this row would appear, strictly INCREASING the row count above
// the correct total of 1).
func TestStoreList_SeveralImportBatchIDsANDsWithOtherFilters(t *testing.T) {
	const vatErrorViolation = `[{"rule_key":"vat-standard-rate","severity":"error","message":"bad rate"}]`
	const otherRuleViolation = `[{"rule_key":"currency-allowed","severity":"error","message":"bad currency"}]`

	t.Run("needs_attention", func(t *testing.T) {
		super, app := dbTestPools(t)
		ctx := context.Background()
		tenantID := seedTenant(t, super, "BATCH-FILTER-AND-NA tenant")
		entityID := seedEntity(t, super, tenantID, "BATCH-FILTER-AND-NA entity")
		batchIn := seedImportBatch(t, super, tenantID, entityID)
		batchOut := seedImportBatch(t, super, tenantID, entityID)

		target := seedInvoiceFull(t, super, tenantID, entityID, "AND-NA-TARGET", &batchIn, string(StatusRejected), `[]`, "irrelevant")
		seedInvoiceFull(t, super, tenantID, entityID, "AND-NA-INLIST-CLEAN", &batchIn, string(StatusValidated), `[]`, "irrelevant")
		seedInvoiceFull(t, super, tenantID, entityID, "AND-NA-OUTLIST-NEEDSATTN", &batchOut, string(StatusRejected), `[]`, "irrelevant")

		store := NewStore(app)
		c := auth.WithIdentity(ctx, auth.Identity{Subject: memberSubject, Role: "authenticated", TenantID: tenantID})

		items, total, err := store.List(c, ListFilter{ImportBatchIDs: []string{batchIn}, NeedsAttention: true, Limit: 50})
		if err != nil {
			t.Fatalf("List (ImportBatchIDs: [batchIn], NeedsAttention: true): %v", err)
		}
		if total != 1 {
			t.Fatalf("total = %d, want 1 -- an OR-composed filter would leak the out-of-list needs-attention row in (total 2)", total)
		}
		if len(items) != 1 || items[0].ID != target {
			t.Fatalf("items = %+v, want exactly [%s]", items, target)
		}
	})

	t.Run("status", func(t *testing.T) {
		super, app := dbTestPools(t)
		ctx := context.Background()
		tenantID := seedTenant(t, super, "BATCH-FILTER-AND-STATUS tenant")
		entityID := seedEntity(t, super, tenantID, "BATCH-FILTER-AND-STATUS entity")
		batchIn := seedImportBatch(t, super, tenantID, entityID)
		batchOut := seedImportBatch(t, super, tenantID, entityID)

		target := seedInvoiceFull(t, super, tenantID, entityID, "AND-STATUS-TARGET", &batchIn, string(StatusValidated), `[]`, "irrelevant")
		seedInvoiceFull(t, super, tenantID, entityID, "AND-STATUS-INLIST-DRAFT", &batchIn, string(StatusDraft), `[]`, "irrelevant")
		seedInvoiceFull(t, super, tenantID, entityID, "AND-STATUS-OUTLIST-VALIDATED", &batchOut, string(StatusValidated), `[]`, "irrelevant")

		store := NewStore(app)
		c := auth.WithIdentity(ctx, auth.Identity{Subject: memberSubject, Role: "authenticated", TenantID: tenantID})

		items, total, err := store.List(c, ListFilter{ImportBatchIDs: []string{batchIn}, Status: StatusValidated, Limit: 50})
		if err != nil {
			t.Fatalf("List (ImportBatchIDs: [batchIn], Status: validated): %v", err)
		}
		if total != 1 {
			t.Fatalf("total = %d, want 1 -- an OR-composed filter would leak the out-of-list validated row in (total 2)", total)
		}
		if len(items) != 1 || items[0].ID != target {
			t.Fatalf("items = %+v, want exactly [%s]", items, target)
		}
	})

	t.Run("rule_key", func(t *testing.T) {
		super, app := dbTestPools(t)
		ctx := context.Background()
		tenantID := seedTenant(t, super, "BATCH-FILTER-AND-RULEKEY tenant")
		entityID := seedEntity(t, super, tenantID, "BATCH-FILTER-AND-RULEKEY entity")
		batchIn := seedImportBatch(t, super, tenantID, entityID)
		batchOut := seedImportBatch(t, super, tenantID, entityID)

		target := seedInvoiceFull(t, super, tenantID, entityID, "AND-RULEKEY-TARGET", &batchIn, string(StatusDraft), vatErrorViolation, "irrelevant")
		seedInvoiceFull(t, super, tenantID, entityID, "AND-RULEKEY-INLIST-OTHERRULE", &batchIn, string(StatusDraft), otherRuleViolation, "irrelevant")
		seedInvoiceFull(t, super, tenantID, entityID, "AND-RULEKEY-OUTLIST-SAMERULE", &batchOut, string(StatusDraft), vatErrorViolation, "irrelevant")

		store := NewStore(app)
		c := auth.WithIdentity(ctx, auth.Identity{Subject: memberSubject, Role: "authenticated", TenantID: tenantID})

		items, total, err := store.List(c, ListFilter{ImportBatchIDs: []string{batchIn}, RuleKey: "vat-standard-rate", Limit: 50})
		if err != nil {
			t.Fatalf("List (ImportBatchIDs: [batchIn], RuleKey: vat-standard-rate): %v", err)
		}
		if total != 1 {
			t.Fatalf("total = %d, want 1 -- an OR-composed filter would leak the out-of-list same-rule row in (total 2)", total)
		}
		if len(items) != 1 || items[0].ID != target {
			t.Fatalf("items = %+v, want exactly [%s]", items, target)
		}
	})

	t.Run("query", func(t *testing.T) {
		super, app := dbTestPools(t)
		ctx := context.Background()
		tenantID := seedTenant(t, super, "BATCH-FILTER-AND-QUERY tenant")
		entityID := seedEntity(t, super, tenantID, "BATCH-FILTER-AND-QUERY entity")
		batchIn := seedImportBatch(t, super, tenantID, entityID)
		batchOut := seedImportBatch(t, super, tenantID, entityID)

		target := seedInvoiceFull(t, super, tenantID, entityID, "AND-QUERY-TARGET", &batchIn, string(StatusDraft), `[]`, "Acme Query Ltd")
		seedInvoiceFull(t, super, tenantID, entityID, "AND-QUERY-INLIST-OTHERBUYER", &batchIn, string(StatusDraft), `[]`, "Zebra Buyer")
		seedInvoiceFull(t, super, tenantID, entityID, "AND-QUERY-OUTLIST-SAMEBUYER", &batchOut, string(StatusDraft), `[]`, "Acme Query Ltd")

		store := NewStore(app)
		c := auth.WithIdentity(ctx, auth.Identity{Subject: memberSubject, Role: "authenticated", TenantID: tenantID})

		items, total, err := store.List(c, ListFilter{ImportBatchIDs: []string{batchIn}, Query: "acme", Limit: 50})
		if err != nil {
			t.Fatalf("List (ImportBatchIDs: [batchIn], Query: acme): %v", err)
		}
		if total != 1 {
			t.Fatalf("total = %d, want 1 -- an OR-composed filter would leak the out-of-list same-buyer row in (total 2)", total)
		}
		if len(items) != 1 || items[0].ID != target {
			t.Fatalf("items = %+v, want exactly [%s]", items, target)
		}
	})
}

// TestStoreList_QueryUnderscoreWildcardIsEscaped (QA Mode B adversarial,
// BUG-01-04, AC-2): escapeLike's existing "%" test (TestStoreList_
// QueryMatchesNumberOrBuyer) does not exercise "_", LIKE's other
// single-character wildcard. A pasted TIN fragment like "2003_344" must
// require a literal underscore, not match "20033344" by wildcarding the
// '_' against the digit '3'.
func TestStoreList_QueryUnderscoreWildcardIsEscaped(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := seedTenant(t, super, "BATCH-FILTER-Q-UNDERSCORE tenant")
	entityID := seedEntity(t, super, tenantID, "BATCH-FILTER-Q-UNDERSCORE entity")

	seedInvoiceWithTINs(t, super, tenantID, entityID, "BATCHQ-UNDERSCORE-001", "20033344-0003", "")

	store := NewStore(app)
	c := auth.WithIdentity(ctx, auth.Identity{Subject: memberSubject, Role: "authenticated", TenantID: tenantID})

	items, total, err := store.List(c, ListFilter{Query: "2003_344", Limit: 50})
	if err != nil {
		t.Fatalf("List (Query: \"2003_344\"): %v", err)
	}
	if total != 0 {
		t.Errorf("List (Query: \"2003_344\").total = %d, want 0 -- an unescaped '_' wildcards onto the '3' in \"20033344\"", total)
	}
	if len(items) != 0 {
		t.Errorf("List (Query: \"2003_344\") len = %d, want 0", len(items))
	}
}

// TestStoreList_QueryBuyerNameCaseInsensitive (QA Mode B adversarial,
// BUG-01-04): TINs are numeric so case never matters there, but buyer_name
// is free text. Pins ILIKE (not LIKE) on the buyer_name arm explicitly, so
// a future "optimisation" to LIKE regresses a real, visible search bug.
func TestStoreList_QueryBuyerNameCaseInsensitive(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := seedTenant(t, super, "BATCH-FILTER-Q-CASE tenant")
	entityID := seedEntity(t, super, tenantID, "BATCH-FILTER-Q-CASE entity")

	match := seedInvoiceWithBuyer(t, super, tenantID, entityID, "BATCHQ-CASE-001", "ACME Traders Ltd")

	store := NewStore(app)
	c := auth.WithIdentity(ctx, auth.Identity{Subject: memberSubject, Role: "authenticated", TenantID: tenantID})

	items, total, err := store.List(c, ListFilter{Query: "acme traders", Limit: 50})
	if err != nil {
		t.Fatalf("List (Query: \"acme traders\" against \"ACME Traders Ltd\"): %v", err)
	}
	if total != 1 || len(items) != 1 || items[0].ID != match {
		t.Fatalf("List (Query: \"acme traders\") = (items=%+v, total=%d), want exactly [%s]/1 -- ILIKE must match across case", items, total, match)
	}
}

// TestStoreList_QueryTINFilteredTotalSpansMultiplePages (QA Mode B
// adversarial, BUG-01-04, AC-3): four rows share one buyer_tin among three
// unrelated noise rows; a page size smaller than the filtered set proves
// pagination.total reflects the full filtered count (4) -- computed by the
// shared COUNT(*) query -- independent of the page window, and that walking
// every page (not just the first) covers the filtered set exactly once each,
// with no omissions or duplicates. This is the property BUG-01-05's UI will
// depend on for its results-count display.
func TestStoreList_QueryTINFilteredTotalSpansMultiplePages(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := seedTenant(t, super, "BATCH-FILTER-Q-PAGES tenant")
	entityID := seedEntity(t, super, tenantID, "BATCH-FILTER-Q-PAGES entity")

	const tin = "30044455-0009"
	var matchIDs []string
	for i := 0; i < 4; i++ {
		matchIDs = append(matchIDs, seedInvoiceWithTINs(t, super, tenantID, entityID, fmt.Sprintf("BATCHQ-PAGES-MATCH-%d", i), tin, ""))
	}
	for i := 0; i < 3; i++ {
		seedInvoiceWithTINs(t, super, tenantID, entityID, fmt.Sprintf("BATCHQ-PAGES-NOISE-%d", i), "99988877-0006", "")
	}

	store := NewStore(app)
	c := auth.WithIdentity(ctx, auth.Identity{Subject: memberSubject, Role: "authenticated", TenantID: tenantID})

	seen := map[string]int{}

	page1, total1, err := store.List(c, ListFilter{Query: tin, Limit: 3, Offset: 0})
	if err != nil {
		t.Fatalf("List (Query: tin, Limit: 3, Offset: 0): %v", err)
	}
	if total1 != 4 {
		t.Fatalf("page 1 total = %d, want 4 (the filtered count), even though only 3 rows fit on this page", total1)
	}
	if len(page1) != 3 {
		t.Fatalf("page 1 len = %d, want 3", len(page1))
	}
	for _, inv := range page1 {
		seen[inv.ID]++
	}

	page2, total2, err := store.List(c, ListFilter{Query: tin, Limit: 3, Offset: 3})
	if err != nil {
		t.Fatalf("List (Query: tin, Limit: 3, Offset: 3): %v", err)
	}
	if total2 != 4 {
		t.Fatalf("page 2 total = %d, want 4 -- total must not shift between pages of the same filter", total2)
	}
	if len(page2) != 1 {
		t.Fatalf("page 2 len = %d, want 1 (the remainder)", len(page2))
	}
	for _, inv := range page2 {
		seen[inv.ID]++
	}

	if len(seen) != 4 {
		t.Fatalf("union of both pages covered %d distinct invoices, want 4: %v", len(seen), seen)
	}
	for _, id := range matchIDs {
		if seen[id] != 1 {
			t.Errorf("invoice %s appeared %d times across both pages, want exactly 1", id, seen[id])
		}
	}
}

// TestRLS_QueryTINCrossTenantIsEmpty (QA Mode B adversarial, BUG-01-04): two
// tenants each hold a row carrying the SAME buyer_tin. Store.List has no
// explicit `WHERE tenant_id` (RLS alone scopes it, same trap named by
// TestRLS_ListImportBatchIDCrossTenantIsEmptyNot404 above) -- the widened q
// predicate must not become a cross-tenant read via the new buyer_tin arm.
func TestRLS_QueryTINCrossTenantIsEmpty(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenant1 := seedTenant(t, super, "BATCH-FILTER-Q-RLS tenant 1")
	tenant2 := seedTenant(t, super, "BATCH-FILTER-Q-RLS tenant 2")
	entity1 := seedEntity(t, super, tenant1, "BATCH-FILTER-Q-RLS entity 1")
	entity2 := seedEntity(t, super, tenant2, "BATCH-FILTER-Q-RLS entity 2")

	const tin = "20033344-0003"
	own := seedInvoiceWithTINs(t, super, tenant1, entity1, "BATCHQ-RLS-OWN", tin, "")
	seedInvoiceWithTINs(t, super, tenant2, entity2, "BATCHQ-RLS-OTHER", tin, "")

	store := NewStore(app)
	c1 := auth.WithIdentity(ctx, auth.Identity{Subject: memberSubject, Role: "authenticated", TenantID: tenant1})

	items, total, err := store.List(c1, ListFilter{Query: tin, Limit: 50})
	if err != nil {
		t.Fatalf("List (tenant 1, Query: tin shared with tenant 2): %v", err)
	}
	if total != 1 {
		t.Fatalf("total = %d, want 1 -- tenant 2's row with the same buyer_tin must not leak in via the widened q predicate", total)
	}
	if len(items) != 1 || items[0].ID != own {
		t.Fatalf("items = %+v, want exactly [%s] -- got tenant 2's row instead/also", items, own)
	}
}

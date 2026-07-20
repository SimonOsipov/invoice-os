// M4-09-02 (task-183), QA Mode B adversarial coverage: the RED tests in
// needs_attention_test.go (TestStoreList_NeedsAttentionMatchesDashboardRollup,
// TestRLS_ListNeedsAttention_TenantIsolated, TestListHandler_
// NeedsAttentionParse) pin the three ACs the architect specified. This file
// adds the failure modes QA's own review flagged as NOT yet exercised:
//
//   - a minimal, airtight predicate byte-parity guard (independent of the
//     larger 7-state drift-guard fixture, so a one-sided predicate edit --
//     invoice's WHERE changed but dashboard's FILTER left alone, or vice
//     versa -- cannot hide behind unrelated combinatorics)
//   - the WHERE-applied-to-only-one-of-two-queries failure mode: total must
//     be the FILTERED count, and the returned page must be the correct
//     filtered SLICE under created_at DESC, id DESC, not merely the right
//     total with the wrong membership/order
//   - a genuinely empty needs-attention result stays []Invoice{}, never nil
//   - mixed-severity violations (an error alongside a warning still counts;
//     info/warning with no error never does)
//   - needs_attention=false (explicit) and needs_attention absent are the
//     SAME query at the wire level, not merely the same Go zero value
package invoice

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/SimonOsipov/invoice-os/internal/dashboard"
	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
)

// seedInvoiceWithViolationsAtHelper is seedInvoiceWithViolations plus an
// explicit created_at overwrite (same superuser force-write idiom). Needed so
// TestStoreList_NeedsAttentionPaginationReflectsFilteredCount can assert on
// List's created_at DESC, id DESC order deterministically -- at Postgres
// timestamptz's real-clock resolution, several inserts issued back-to-back in
// one test are not reliably distinct (and even when distinct, their relative
// order is an accident of scheduling, not something the test should depend
// on).
func seedInvoiceWithViolationsAtHelper(t *testing.T, super *pgxpool.Pool, tenantID, entityID, number, status, violationsJSON string, createdAt time.Time) string {
	t.Helper()
	id := seedInvoiceWithViolations(t, super, tenantID, entityID, number, status, violationsJSON)
	if _, err := super.Exec(context.Background(),
		`UPDATE invoices SET created_at = $1 WHERE id = $2`, createdAt, id,
	); err != nil {
		t.Fatalf("force-seed invoice created_at: %v", err)
	}
	return id
}

// TestStoreList_NeedsAttentionPredicateByteParityGuard is the MINIMAL version
// of the drift guard: exactly two invoices, chosen so the ONLY thing that can
// make this test pass or fail is whether List's predicate agrees with the
// dashboard's on the single distinguishing bit (error vs warning severity).
// A one-sided edit -- e.g. someone changes invoice/store.go's WHERE to also
// match warning-severity drafts, or changes dashboard/store.go's FILTER the
// same way but not the other -- flips exactly one of List's total or
// Rollup's Totals.NeedsAttention, so the equality assertion below catches it
// immediately, with no other seeded data to obscure which side moved.
func TestStoreList_NeedsAttentionPredicateByteParityGuard(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := seedTenant(t, super, "M4-09-02 byte-parity tenant")
	entityID := seedEntity(t, super, tenantID, "M4-09-02 byte-parity entity")

	warningDraftID := seedInvoiceWithViolations(t, super, tenantID, entityID, "M4-09-02-BP-warning", string(StatusDraft),
		`[{"rule_key":"some-rule","severity":"warning","message":"advisory only"}]`)
	errorDraftID := seedInvoiceWithViolations(t, super, tenantID, entityID, "M4-09-02-BP-error", string(StatusDraft),
		`[{"rule_key":"some-rule","severity":"error","message":"blocking"}]`)

	invStore := NewStore(app)
	dashStore := dashboard.NewStore(app)
	c := auth.WithIdentity(ctx, auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: tenantID})

	items, total, err := invStore.List(c, ListFilter{NeedsAttention: true, Limit: 100})
	if err != nil {
		t.Fatalf("List(NeedsAttention: true): %v", err)
	}
	roll, err := dashStore.Rollup(c)
	if err != nil {
		t.Fatalf("dashboard Rollup: %v", err)
	}

	if total != 1 {
		t.Fatalf("List(NeedsAttention: true).total = %d, want 1 (only the error-severity draft)", total)
	}
	if total != roll.Totals.NeedsAttention {
		t.Errorf("List(NeedsAttention: true).total = %d, dashboard Rollup().Totals.NeedsAttention = %d, want equal (byte-parity guard)",
			total, roll.Totals.NeedsAttention)
	}
	if len(items) != 1 || items[0].ID != errorDraftID {
		t.Fatalf("List(NeedsAttention: true) items = %+v, want exactly [%s] (the error-severity draft)", items, errorDraftID)
	}
	if items[0].ID == warningDraftID {
		t.Errorf("List(NeedsAttention: true) returned the warning-only draft %s, which must be excluded", warningDraftID)
	}
}

// TestStoreList_NeedsAttentionPaginationReflectsFilteredCount (the subtle
// failure mode named in the task: the WHERE predicate applied to only ONE of
// the count(*) query or the page query). Seeds 5 needs-attention invoices at
// distinct, strictly descending created_at timestamps (so their List order
// is deterministic) interleaved with 3 non-qualifying invoices -- 8 rows in
// this tenant total, 5 of which qualify. Asserts:
//
//   - pagination.total is the FILTERED count (5), not the tenant total (8),
//     on every page
//   - each page's item IDs are the correct SLICE of the filtered set, in
//     created_at DESC order -- not just the right total with the wrong
//     membership (which is exactly what would happen if the WHERE were
//     applied to the count query but not the page query, or vice versa: the
//     total would be right/wrong independently of what rows come back)
func TestStoreList_NeedsAttentionPaginationReflectsFilteredCount(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := seedTenant(t, super, "M4-09-02 pagination tenant")
	entityID := seedEntity(t, super, tenantID, "M4-09-02 pagination entity")

	base := time.Now().UTC()
	at := func(offsetHours int) time.Time { return base.Add(time.Duration(offsetHours) * time.Hour) }

	// Needs-attention rows, seeded oldest to newest (n1 oldest .. n5 newest)
	// so created_at DESC lists them n5, n4, n3, n2, n1.
	n1 := seedInvoiceWithViolationsAtHelper(t, super, tenantID, entityID, "M4-09-02-PG-n1", string(StatusRejected), `[]`, at(1))
	n2 := seedInvoiceWithViolationsAtHelper(t, super, tenantID, entityID, "M4-09-02-PG-n2", string(StatusFailed), `[]`, at(2))
	n3 := seedInvoiceWithViolationsAtHelper(t, super, tenantID, entityID, "M4-09-02-PG-n3", string(StatusDraft),
		`[{"rule_key":"x","severity":"error","message":"y"}]`, at(3))
	n4 := seedInvoiceWithViolationsAtHelper(t, super, tenantID, entityID, "M4-09-02-PG-n4", string(StatusRejected), `[]`, at(4))
	n5 := seedInvoiceWithViolationsAtHelper(t, super, tenantID, entityID, "M4-09-02-PG-n5", string(StatusFailed), `[]`, at(5))

	// Non-qualifying rows, interleaved -- must never appear on any filtered
	// page and must never be counted in the filtered total.
	seedInvoiceWithViolationsAtHelper(t, super, tenantID, entityID, "M4-09-02-PG-clean", string(StatusDraft), `[]`, at(6))
	seedInvoiceWithViolationsAtHelper(t, super, tenantID, entityID, "M4-09-02-PG-validated", string(StatusValidated), `[]`, at(7))
	seedInvoiceWithViolationsAtHelper(t, super, tenantID, entityID, "M4-09-02-PG-warning", string(StatusDraft),
		`[{"rule_key":"x","severity":"warning","message":"y"}]`, at(8))

	store := NewStore(app)
	c := auth.WithIdentity(ctx, auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: tenantID})

	// Sanity: the unfiltered tenant total is 8 (proves the filtered total
	// below isn't accidentally the tenant total in disguise).
	_, unfilteredTotal, err := store.List(c, ListFilter{Limit: 100})
	if err != nil {
		t.Fatalf("List (unfiltered): %v", err)
	}
	if unfilteredTotal != 8 {
		t.Fatalf("unfiltered List total = %d, want 8 (sanity check on the fixture)", unfilteredTotal)
	}

	wantOrder := []string{n5, n4, n3, n2, n1}

	pages := []struct {
		limit, offset int
		wantIDs       []string
	}{
		{2, 0, wantOrder[0:2]},
		{2, 2, wantOrder[2:4]},
		{2, 4, wantOrder[4:5]},
	}

	for _, p := range pages {
		items, total, err := store.List(c, ListFilter{NeedsAttention: true, Limit: p.limit, Offset: p.offset})
		if err != nil {
			t.Fatalf("List(NeedsAttention:true, limit=%d, offset=%d): %v", p.limit, p.offset, err)
		}
		if total != 5 {
			t.Errorf("List(NeedsAttention:true, limit=%d, offset=%d).total = %d, want 5 (the FILTERED count, not the tenant total 8)",
				p.limit, p.offset, total)
		}
		if len(items) != len(p.wantIDs) {
			t.Fatalf("List(NeedsAttention:true, limit=%d, offset=%d) returned %d items, want %d", p.limit, p.offset, len(items), len(p.wantIDs))
		}
		for i, inv := range items {
			if inv.ID != p.wantIDs[i] {
				t.Errorf("List(NeedsAttention:true, limit=%d, offset=%d) item[%d].ID = %s, want %s (created_at DESC order)",
					p.limit, p.offset, i, inv.ID, p.wantIDs[i])
			}
		}
	}
}

// TestStoreList_NeedsAttentionEmptyResultNotNull: a tenant with ZERO
// needs-attention invoices must get total==0 and a non-nil, empty []Invoice
// (List's existing never-nil contract, unchanged by this filter).
func TestStoreList_NeedsAttentionEmptyResultNotNull(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := seedTenant(t, super, "M4-09-02 empty-result tenant")
	entityID := seedEntity(t, super, tenantID, "M4-09-02 empty-result entity")

	seedInvoiceWithViolations(t, super, tenantID, entityID, "M4-09-02-ER-clean", string(StatusDraft), `[]`)
	seedInvoiceWithViolations(t, super, tenantID, entityID, "M4-09-02-ER-validated", string(StatusValidated), `[]`)
	seedInvoiceWithViolations(t, super, tenantID, entityID, "M4-09-02-ER-accepted", string(StatusAccepted), `[]`)
	seedInvoiceWithViolations(t, super, tenantID, entityID, "M4-09-02-ER-warning-draft", string(StatusDraft),
		`[{"rule_key":"x","severity":"warning","message":"y"}]`)

	store := NewStore(app)
	c := auth.WithIdentity(ctx, auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: tenantID})

	items, total, err := store.List(c, ListFilter{NeedsAttention: true, Limit: 50})
	if err != nil {
		t.Fatalf("List(NeedsAttention:true): %v", err)
	}
	if total != 0 {
		t.Errorf("total = %d, want 0", total)
	}
	if items == nil {
		t.Error("items is nil, want [] (never null)")
	}
	if len(items) != 0 {
		t.Errorf("len(items) = %d, want 0", len(items))
	}
}

// TestStoreList_NeedsAttentionMixedSeverityViolations: the predicate is
// `violations @> '[{"severity":"error"}]'::jsonb`, a CONTAINMENT check, not
// an exact-match or sole-element check -- a draft carrying BOTH an error and
// a warning entry still matches (the array contains an element with
// severity:"error", full stop), while a draft carrying only info/warning
// entries (no error anywhere in the array) must not.
func TestStoreList_NeedsAttentionMixedSeverityViolations(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := seedTenant(t, super, "M4-09-02 mixed-severity tenant")
	entityID := seedEntity(t, super, tenantID, "M4-09-02 mixed-severity entity")

	mixedID := seedInvoiceWithViolations(t, super, tenantID, entityID, "M4-09-02-MS-mixed", string(StatusDraft),
		`[{"rule_key":"a","severity":"error","message":"blocking"},{"rule_key":"b","severity":"warning","message":"advisory"}]`)
	infoWarningOnlyID := seedInvoiceWithViolations(t, super, tenantID, entityID, "M4-09-02-MS-info-warning", string(StatusDraft),
		`[{"rule_key":"c","severity":"info","message":"fyi"},{"rule_key":"d","severity":"warning","message":"advisory"}]`)

	store := NewStore(app)
	c := auth.WithIdentity(ctx, auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: tenantID})

	items, total, err := store.List(c, ListFilter{NeedsAttention: true, Limit: 50})
	if err != nil {
		t.Fatalf("List(NeedsAttention:true): %v", err)
	}
	if total != 1 {
		t.Fatalf("total = %d, want 1 (only the mixed error+warning draft)", total)
	}
	if len(items) != 1 || items[0].ID != mixedID {
		t.Fatalf("items = %+v, want exactly [%s] (the error+warning draft)", items, mixedID)
	}
	for _, inv := range items {
		if inv.ID == infoWarningOnlyID {
			t.Errorf("List(NeedsAttention:true) returned the info/warning-only draft %s, which carries no error severity", infoWarningOnlyID)
		}
	}
}

// TestListHandler_NeedsAttentionFalseExplicitMatchesAbsent (AC #5): the
// architect's ListFilter.NeedsAttention is a Go bool, so its OWN zero value
// already makes "absent" and "explicit false" identical at the Go level --
// the only place this distinction is real is the WIRE, where
// ?needs_attention=false and no needs_attention param at all are two
// DIFFERENT query strings that must both parse to the SAME captured
// ListFilter (false), never one 400ing or diverging from the other.
func TestListHandler_NeedsAttentionFalseExplicitMatchesAbsent(t *testing.T) {
	run := func(query string) ListFilter {
		id := auth.Identity{Subject: "user-1", Role: "authenticated", TenantID: uuid.NewString()}
		var captured ListFilter
		called := false
		list := func(ctx context.Context, f ListFilter) ([]Invoice, int, error) {
			called = true
			captured = f
			return []Invoice{}, 0, nil
		}
		rec, _ := doInvoiceList(t, list, &id, query)
		if rec.Code != http.StatusOK {
			t.Fatalf("query=%q: status = %d, want 200 (body=%s)", query, rec.Code, rec.Body.String())
		}
		if !called {
			t.Fatalf("query=%q: store.List was not called", query)
		}
		return captured
	}

	absent := run("")
	explicitFalse := run("?needs_attention=false")

	if absent.NeedsAttention {
		t.Errorf("absent ?needs_attention: captured.NeedsAttention = true, want false")
	}
	if explicitFalse.NeedsAttention {
		t.Errorf("?needs_attention=false: captured.NeedsAttention = true, want false")
	}
	if absent != explicitFalse {
		t.Errorf("captured ListFilter differs between absent (%+v) and explicit false (%+v), want identical", absent, explicitFalse)
	}
}

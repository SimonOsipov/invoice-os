// M4-07-02 (task-156): tests for Store.Rollup's TopViolations breakdown,
// written BEFORE the real implementation exists (RED -- store.go's Rollup
// pre-declares topViolations := []RuleCount{} and leaves it empty until
// M4-07-02 lands the per-rule query in the same transaction, so every
// assertion below that expects a non-empty TopViolations fails on the
// assertion, not a compile/setup error). Reuses the dbTestPools/seedTenant/
// seedEntity/seedInvoice/seedInvoiceAtStatus/seedInvoiceWithViolations
// harness from store_test.go (same package) -- no new helpers needed.
//
// Spec-to-test map (Test Specs table, M4-07-02 story / task-156):
//
//	DASH-20 TestStoreRollup_TopViolationsCountsInvoicesPerRule
//	DASH-21 TestStoreRollup_TopViolationsExcludesWarnings
//	DASH-22 TestStoreRollup_TopViolationsMixedSeverityOnOneInvoice
//	DASH-23 TestStoreRollup_TopViolationsOneInvoiceCountsOncePerRule
//	DASH-24 TestStoreRollup_TopViolationsTieBreaksByRuleKeyAscending
//	DASH-25 TestStoreRollup_TopViolationsEmptyEverywhereMarshalsAsEmptyArray
//	DASH-26 TestStoreRollup_TopViolationsMalformedElementSkippedNotFatal
//
// DASH-27 (TestRLS_DashboardTopViolationsCrossTenantIsolated) lives in
// cross_tenant_integration_test.go, alongside DASH-14's cross-tenant test.
//
// TestStoreRollup_NonArrayViolationsDoNotBreakTopViolations below is NOT a
// numbered Test Spec -- it pins a cross-subtask regression hazard between
// M4-07-01 and M4-07-02 (see its own doc comment).
//
// Run: `make test-rls`, or directly, e.g.:
//
//	DATABASE_URL="postgres://invoice_app:app@localhost:5434/invoice_os?sslmode=disable" \
//	DATABASE_SUPERUSER_URL="postgres://postgres:postgres@localhost:5434/invoice_os?sslmode=disable" \
//	go test -count=1 ./internal/dashboard/...
package dashboard

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/google/uuid"

	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
)

// assertTopViolations fails t unless got matches want exactly -- RuleKey and
// Invoices per element, IN ORDER. Several DASH-20..27 specs pin exact
// ordering (invoices DESC, rule_key ASC, AC-3); a length-only or set-based
// comparison would silently accept a shuffled or padded result.
func assertTopViolations(t *testing.T, got, want []RuleCount) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("TopViolations = %+v (%d entries), want %+v (%d entries)", got, len(got), want, len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("TopViolations[%d] = %+v, want %+v (full got = %+v, want = %+v)", i, got[i], want[i], got, want)
		}
	}
}

// DASH-20: with 3 drafts carrying a severity:"error" violation for rule
// "supplier-tin-required" and 1 draft carrying rule "currency-required",
// TopViolations must report both rules with their exact distinct-invoice
// counts, ranked count DESC (AC-1/AC-3).
func TestStoreRollup_TopViolationsCountsInvoicesPerRule(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := seedTenant(t, super, "DASH-20 tenant")
	entityID := seedEntity(t, super, tenantID, "DASH-20 entity")

	tin := `[{"rule_key":"supplier-tin-required","severity":"error","message":"x"}]`
	for i := 0; i < 3; i++ {
		seedInvoiceWithViolations(t, super, tenantID, entityID, fmt.Sprintf("DASH-20-tin-%d", i), "draft", tin)
	}
	currency := `[{"rule_key":"currency-required","severity":"error","message":"y"}]`
	seedInvoiceWithViolations(t, super, tenantID, entityID, "DASH-20-currency", "draft", currency)

	store := NewStore(app)
	cA := auth.WithIdentity(ctx, auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: tenantID})

	got, err := store.Rollup(cA)
	if err != nil {
		t.Fatalf("Rollup: %v", err)
	}
	assertTopViolations(t, got.TopViolations, []RuleCount{
		{RuleKey: "supplier-tin-required", Invoices: 3},
		{RuleKey: "currency-required", Invoices: 1},
	})
}

// DASH-21: a draft whose only violation is severity:"warning" must
// contribute nothing to TopViolations -- only severity:"error" entries are
// counted (AC-2).
func TestStoreRollup_TopViolationsExcludesWarnings(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := seedTenant(t, super, "DASH-21 tenant")
	entityID := seedEntity(t, super, tenantID, "DASH-21 entity")
	seedInvoiceWithViolations(t, super, tenantID, entityID, "DASH-21-1", "draft",
		`[{"rule_key":"buyer-name-recommended","severity":"warning","message":"x"}]`)

	store := NewStore(app)
	cA := auth.WithIdentity(ctx, auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: tenantID})

	got, err := store.Rollup(cA)
	if err != nil {
		t.Fatalf("Rollup: %v", err)
	}
	assertTopViolations(t, got.TopViolations, []RuleCount{})
}

// DASH-22: one draft carrying both a warning (rule "buyer-name-recommended")
// and an error (rule "supplier-tin-required") violation must contribute
// ONLY the error rule to TopViolations -- the warning entry on the SAME
// invoice must not leak through (AC-2).
func TestStoreRollup_TopViolationsMixedSeverityOnOneInvoice(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := seedTenant(t, super, "DASH-22 tenant")
	entityID := seedEntity(t, super, tenantID, "DASH-22 entity")
	seedInvoiceWithViolations(t, super, tenantID, entityID, "DASH-22-1", "draft",
		`[{"rule_key":"buyer-name-recommended","severity":"warning","message":"w"},`+
			`{"rule_key":"supplier-tin-required","severity":"error","message":"e"}]`)

	store := NewStore(app)
	cA := auth.WithIdentity(ctx, auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: tenantID})

	got, err := store.Rollup(cA)
	if err != nil {
		t.Fatalf("Rollup: %v", err)
	}
	assertTopViolations(t, got.TopViolations, []RuleCount{
		{RuleKey: "supplier-tin-required", Invoices: 1},
	})
}

// DASH-23: one draft carrying TWO distinct error-severity violations (rules
// "buyer-tin-required" and "supplier-tin-required") must contribute to BOTH
// rules' counts -- count(DISTINCT invoice_id) is per rule_key, not "one
// contribution per invoice overall" (AC-1).
func TestStoreRollup_TopViolationsOneInvoiceCountsOncePerRule(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := seedTenant(t, super, "DASH-23 tenant")
	entityID := seedEntity(t, super, tenantID, "DASH-23 entity")
	seedInvoiceWithViolations(t, super, tenantID, entityID, "DASH-23-1", "draft",
		`[{"rule_key":"buyer-tin-required","severity":"error","message":"r1"},`+
			`{"rule_key":"supplier-tin-required","severity":"error","message":"r2"}]`)

	store := NewStore(app)
	cA := auth.WithIdentity(ctx, auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: tenantID})

	got, err := store.Rollup(cA)
	if err != nil {
		t.Fatalf("Rollup: %v", err)
	}
	assertTopViolations(t, got.TopViolations, []RuleCount{
		{RuleKey: "buyer-tin-required", Invoices: 1},
		{RuleKey: "supplier-tin-required", Invoices: 1},
	})
}

// DASH-24: rules "b-rule" and "a-rule", each with exactly 1 erroring
// invoice (a tied count), must order by rule_key ASC as the deterministic
// tie-break -- a-rule before b-rule (AC-3). Seeded in b-then-a order
// deliberately: if ordering ever fell back to insertion/scan order instead
// of rule_key ASC, this would surface it.
func TestStoreRollup_TopViolationsTieBreaksByRuleKeyAscending(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := seedTenant(t, super, "DASH-24 tenant")
	entityID := seedEntity(t, super, tenantID, "DASH-24 entity")
	seedInvoiceWithViolations(t, super, tenantID, entityID, "DASH-24-b", "draft",
		`[{"rule_key":"b-rule","severity":"error","message":"x"}]`)
	seedInvoiceWithViolations(t, super, tenantID, entityID, "DASH-24-a", "draft",
		`[{"rule_key":"a-rule","severity":"error","message":"y"}]`)

	store := NewStore(app)
	cA := auth.WithIdentity(ctx, auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: tenantID})

	got, err := store.Rollup(cA)
	if err != nil {
		t.Fatalf("Rollup: %v", err)
	}
	assertTopViolations(t, got.TopViolations, []RuleCount{
		{RuleKey: "a-rule", Invoices: 1},
		{RuleKey: "b-rule", Invoices: 1},
	})
}

// DASH-25: 4 invoices with violations = '[]' (a valid, empty jsonb array --
// distinct from the non-array shapes the guard test below covers) must
// produce TopViolations == []RuleCount{}, marshalled as "top_violations":[],
// never null (AC-4), and jsonb_array_elements must not error on an empty
// array.
func TestStoreRollup_TopViolationsEmptyEverywhereMarshalsAsEmptyArray(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := seedTenant(t, super, "DASH-25 tenant")
	entityID := seedEntity(t, super, tenantID, "DASH-25 entity")
	for i := 0; i < 4; i++ {
		seedInvoiceWithViolations(t, super, tenantID, entityID, fmt.Sprintf("DASH-25-%d", i), "draft", `[]`)
	}

	store := NewStore(app)
	cA := auth.WithIdentity(ctx, auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: tenantID})

	got, err := store.Rollup(cA)
	if err != nil {
		t.Fatalf("Rollup: %v", err)
	}
	assertTopViolations(t, got.TopViolations, []RuleCount{})
	if got.TopViolations == nil {
		t.Fatal("TopViolations is nil, want a non-nil empty slice")
	}
	body, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	if !bytes.Contains(body, []byte(`"top_violations":[]`)) {
		t.Errorf("marshalled body = %s, want it to contain \"top_violations\":[]", body)
	}
	if bytes.Contains(body, []byte(`"top_violations":null`)) {
		t.Errorf("marshalled body = %s, want \"top_violations\":[] not null", body)
	}
}

// DASH-26: a violations array containing one malformed element (missing
// rule_key -- v->>'rule_key' evaluates to SQL NULL, which fails the query's
// `IS NOT NULL` filter) alongside one well-formed severity:"error" element
// for rule "supplier-tin-required" must not error, and must not let the
// malformed element contribute a phantom entry -- TopViolations is exactly
// [{supplier-tin-required,1}] (AC-1/AC-2).
func TestStoreRollup_TopViolationsMalformedElementSkippedNotFatal(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := seedTenant(t, super, "DASH-26 tenant")
	entityID := seedEntity(t, super, tenantID, "DASH-26 entity")
	seedInvoiceWithViolations(t, super, tenantID, entityID, "DASH-26-1", "draft",
		`[{"severity":"error"},{"rule_key":"supplier-tin-required","severity":"error","message":"y"}]`)

	store := NewStore(app)
	cA := auth.WithIdentity(ctx, auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: tenantID})

	got, err := store.Rollup(cA)
	if err != nil {
		t.Fatalf("Rollup: %v", err)
	}
	assertTopViolations(t, got.TopViolations, []RuleCount{
		{RuleKey: "supplier-tin-required", Invoices: 1},
	})
}

// TestStoreRollup_NonArrayViolationsDoNotBreakTopViolations pins a
// cross-subtask regression hazard: Postgres's jsonb_array_elements RAISES AN
// ERROR when its input is not a JSON array (a jsonb object -> "cannot
// extract elements from an object"; a jsonb scalar -> "cannot extract
// elements from a scalar" -- confirmed empirically against the dev DB,
// Stage 1 Architecture Validation item 4, M4-07-02 story/task-156). That is
// a real divergence from M4-07-01's `@>` containment predicate, which
// merely evaluates to false (never errors) on the same malformed inputs.
// TestStoreRollup_MalformedViolationsNeverErrorsOrFalselyFlags
// (rollup_adversarial_test.go, M4-07-01) force-seeds exactly these
// non-array shapes and must keep passing once this subtask's
// jsonb_array_elements query runs in the SAME transaction as that query --
// which means the per-rule query needs `AND jsonb_typeof(i.violations) =
// 'array'` in its WHERE clause (Postgres pushes that predicate to the base
// Seq Scan before the LATERAL call runs, per the Stage 1 EXPLAIN check, so
// it costs nothing).
//
// This test reseeds every malformed shape that test exercises, alongside
// one genuinely broken draft with a real rule_key, and asserts on
// Store.Rollup as a whole: no error, the valid rule still surfaces with the
// right count, and no malformed row contributes a phantom entry (no
// empty-string rule_key).
func TestStoreRollup_NonArrayViolationsDoNotBreakTopViolations(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := seedTenant(t, super, "guard tenant: non-array violations")
	entityID := seedEntity(t, super, tenantID, "guard entity")

	malformed := []struct {
		name       string
		violations string
	}{
		{"object-not-array", `{}`},
		{"array-of-scalars", `[1,2,3]`},
		{"element-missing-severity-key", `[{"rule_key":"x","message":"y"}]`},
		{"empty-array", `[]`},
		{"nested-wrapped-shape", `{"violations":[{"severity":"error"}]}`},
	}
	for _, tc := range malformed {
		seedInvoiceWithViolations(t, super, tenantID, entityID, "GUARD-"+tc.name, "draft", tc.violations)
	}
	seedInvoiceWithViolations(t, super, tenantID, entityID, "GUARD-real-error", "draft",
		`[{"rule_key":"supplier-tin-required","severity":"error","message":"z"}]`)

	store := NewStore(app)
	cA := auth.WithIdentity(ctx, auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: tenantID})

	got, err := store.Rollup(cA)
	if err != nil {
		t.Fatalf("Rollup with non-array violations shapes present: %v -- if this is a "+
			"jsonb_array_elements error, the per-rule query needs `AND jsonb_typeof(i.violations) "+
			"= 'array'` in its WHERE clause", err)
	}
	assertTopViolations(t, got.TopViolations, []RuleCount{
		{RuleKey: "supplier-tin-required", Invoices: 1},
	})
	for _, rc := range got.TopViolations {
		if rc.RuleKey == "" {
			t.Errorf("TopViolations contains an entry with empty rule_key: %+v (full = %+v)", rc, got.TopViolations)
		}
	}
}

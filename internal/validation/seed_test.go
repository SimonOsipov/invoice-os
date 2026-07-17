// M3-05-01 (Test-first: yes) — DB-backed proof that the MBS global rule-set
// v1 seed migration (migrations/<goose-ts>_seed_mbs_v1.sql, not yet authored)
// flips /v1/validate's evaluation surface from "no active rule-set" to live
// content: the ACTIVE version + its 19 rules load via
// NewStore(app).LoadActiveRuleSet, and get evaluated via
// NewDefaultEngine().Evaluate against real payloads. This is the FIRST test
// in the package to chain a DB load (store_test.go's pattern) into an engine
// evaluate (registry_test.go's pattern) -- see those files for the two
// precedents this suite combines.
//
// This suite is RED until the seed migration lands: with no active version
// in the migrated DB, LoadActiveRuleSet returns ErrNoActiveRuleSet and every
// test below fails at loadActive's t.Fatalf (never a build/compile error, a
// connection failure, or a skip -- see dbTestPools in schema_test.go for the
// env-gated skip this suite reuses, which self-skips only when
// DATABASE_URL/DATABASE_SUPERUSER_URL are unset).
//
// IMPORTANT: unlike schema_test.go/store_test.go's fixtures, this suite does
// NOT seed its own rule_set_versions row -- it asserts the MIGRATED ACTIVE
// version directly (v2 since M4-04-01; see activeSeedVersion), so it never
// contends for the partial-unique "one active version" slot other tests'
// seedVersion(...,true) fixtures occupy transiently. Only TestSeed_KillSwitch mutates shared state (one rule's
// `enabled` column, via the real app-role Store.ToggleRule path) and
// restores it in t.Cleanup; every other test in this file is read-only.
//
// Coverage (story M3-05 Test Specs; see the story's System Design table +
// .ralph/m3-05-exec-readiness.md for the exact signatures/harness):
//  1. TestSeed_ActiveVersionLoads    -- Core AC 1: exactly one active
//     version, and it is activeSeedVersion (v2 since M4-04-01) with 19 rules,
//     keys matching the pinned rule table. This is the ONE place the package
//     asserts WHICH version is active.
//  2. TestSeed_DemoContract          -- Core AC 3: the demo's bad/valid
//     payloads produce exactly the documented violations.
//  3. TestSeed_TaxMathTolerance      -- Core AC 4: tolerance 0.005, strict >
//     comparison (correct VAT passes, one-kobo-off fails, rounding-absorbed
//     passes).
//  4. TestSeed_TINFormat             -- TIN format positive/negative +
//     buyer-tin-format's "absent -> pass" semantics.
//  5. TestSeed_MandatoryPresence     -- a missing mandatory field fires its
//     own *-required rule and no other; the valid payload fires none.
//  6. TestSeed_CurrencyEnum          -- currency-allowed positive/negative.
//  7. TestSeed_RangeNonNegative      -- a negative amount fires its
//     *-non-negative rule; a valid amount does not.
//  8. TestSeed_DuplicateLineItemsCEL -- Core AC 2/8: the hardened
//     no-duplicate-line-items CEL expr fires on a shared id, passes on
//     unique ids, does NOT 500 (and is exempt from dedup) on an id-less
//     line item, and passes on an empty line_items array (advisory, per the
//     story's Decisions section). Every subtest here also doubles as the
//     "CEL compiles + returns bool" proof (no engine error on any case).
//  9. TestSeed_KillSwitch            -- Core AC 5: disabling
//     vat-standard-rate via the app-grant Store.ToggleRule path drops its
//     violation from the next evaluate, leaving only supplier-tin-format;
//     restored in cleanup.
//  10. TestSeed_ReversibilityRollback (optional, per the story's Test Specs
//     "your judgment" note) -- runs the migration's Down DELETE inside a
//     superuser tx that is always rolled back, proving the Down's effect
//     (zero active versions, zero v1 rules) without touching the shared v1
//     the tests above depend on. The CI `migrations` job's reset->up
//     round-trip is the authoritative reversibility check; this is a
//     narrower, same-package sanity check.
//
// NOT covered here (belongs to the Execution-stage test reconciliation,
// which touches schema_test.go/store_test.go directly -- explicitly out of
// scope for this Mode A RED file, per M3-05-01's Implementation Plan step 3):
//   - seedVersion(t,super,true)'s deactivate/restore-by-id LIFO fix.
//   - TestStore_LoadNoActiveErrors's deactivate/restore-by-id fix.
//   - TestSchema_NoRuleContentShipped's narrowing to exclude the seeded
//     versions (by their notes marker, not by version number).
//
// Run (same env gate as the rest of the package):
//
//	DATABASE_URL="postgres://invoice_app:app@localhost:5432/invoice_os?sslmode=disable" \
//	DATABASE_SUPERUSER_URL="postgres://postgres:postgres@localhost:5432/invoice_os?sslmode=disable" \
//	go test -count=1 ./internal/validation/...
package validation

import (
	"context"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
)

// newTestIdentity builds a fresh authenticated identity context. rule_set_versions/
// rules are GLOBAL, untenanted tables (no RLS -- see store.go's file header), so the
// specific tenant chosen here is arbitrary; Store.LoadActiveRuleSet/ToggleRule only
// require db.WithinRequestTenantTx to see a valid identity in ctx (store_test.go's
// TestStore_LoadNoIdentityErrors proves the no-identity case separately).
func newTestIdentity() context.Context {
	return auth.WithIdentity(context.Background(), auth.Identity{
		Subject: "user-1", Role: "authenticated", TenantID: uuid.NewString(),
	})
}

// activeSeedVersion is the ONE place this package names the migration-seeded
// ACTIVE rule-set version. M4-04-01 published v2 (v1's 17 base rules + the 2
// line-item rules) and deactivated v1, restoring v1's immutability -- see
// migrations/20260716185106_rule_set_v2.sql. Every fixture below that needs
// "the sanctioned active version" resolves it through here or by discovering
// it from the DB, so the next version publish is a one-line change here rather
// than a scavenger hunt through scattered literals ([active-version-pinning-is-the-bug]).
const activeSeedVersion = 2

// loadActive loads the ACTIVE RuleSet via the real Store -- the shared "DB
// load" half of this file's DB-load -> engine-evaluate chain (combining
// store_test.go's load-with-identity pattern with registry_test.go's
// NewDefaultEngine().Evaluate pattern, per this file's header).
//
// It deliberately does NOT assert a version: its callers each test something
// else entirely (tax-math tolerance, TIN format, the kill-switch...), and a
// version assertion here would t.Fatalf all of them at this line, BEFORE the
// assertion each was written to make -- the exact trap the old loadV1's
// `if rs.Version != 1` sprang on all 23 call sites the moment the active
// version stopped being literally 1 (RS-V2-15). Which version is active is
// asserted ONCE, on purpose, by TestSeed_ActiveVersionLoads.
//
// If the seed is missing entirely, LoadActiveRuleSet returns
// ErrNoActiveRuleSet and callers fail here with a message that reads
// unambiguously as "the seed is not applied yet", not a build/skip/connection
// fault.
func loadActive(t *testing.T, app *pgxpool.Pool) RuleSet {
	t.Helper()
	store := NewStore(app)
	rs, err := store.LoadActiveRuleSet(newTestIdentity())
	if err != nil {
		t.Fatalf("LoadActiveRuleSet: %v -- expected the migration-seeded active rule-set, got none "+
			"(have the migrations been applied via `make migrate-up`?)", err)
	}
	return rs
}

// hasViolation reports whether result carries a violation for the given rule key.
func hasViolation(result Result, key string) bool {
	for _, v := range result.Violations {
		if v.RuleKey == key {
			return true
		}
	}
	return false
}

// violationKeys returns result's violation rule keys in their returned order
// (Engine.Evaluate already sorts them -- Decision N16), for exact-match assertions.
func violationKeys(result Result) []string {
	keys := make([]string, len(result.Violations))
	for i, v := range result.Violations {
		keys[i] = v.RuleKey
	}
	return keys
}

// validInvoicePayload returns a fresh, fully-valid invoice payload matching the
// story's System Design "valid payload" demo fixture -- every map is a brand new
// literal on each call, so callers may mutate the result freely without any risk
// of aliasing another test's payload.
func validInvoicePayload() Payload {
	return Payload{
		"invoice": map[string]any{
			"invoice_number": "INV-2026-000123",
			"issue_date":     "2026-07-11",
			"currency":       "NGN",
			"supplier": map[string]any{
				"tin":  "12345678-0001",
				"name": "Acme Nigeria Ltd",
			},
			"buyer": map[string]any{
				"tin":  "87654321-0002",
				"name": "Buyer Ltd",
			},
			"subtotal": 1000.0,
			"vat":      75.0,
			"total":    1075.0,
			"line_items": []any{
				map[string]any{
					"id":          "1",
					"description": "Widget",
					"quantity":    10.0,
					"unit_price":  100.0,
					"line_total":  1000.0,
				},
			},
		},
	}
}

// badInvoicePayload returns the story's "malformed TIN + wrong VAT" demo fixture:
// a fresh validInvoicePayload() with supplier.tin replaced by a malformed value and
// vat replaced by an amount that does not equal 7.5% of subtotal.
func badInvoicePayload() Payload {
	p := validInvoicePayload()
	inv := p["invoice"].(map[string]any)
	inv["invoice_number"] = "INV-2026-000124"
	inv["supplier"].(map[string]any)["tin"] = "BADTIN"
	inv["vat"] = 70.0
	inv["total"] = 1070.0
	return p
}

// invoiceOf returns p's "invoice" sub-map for in-place mutation by test cases.
func invoiceOf(p Payload) map[string]any {
	return p["invoice"].(map[string]any)
}

// TestSeed_ActiveVersionLoads (Core AC 1 / Test Spec "503 -> live flip" +
// "Active set loads"): after the seed migration applies, exactly one
// rule_set_versions row is active and it is version 1; LoadActiveRuleSet
// materializes it into a RuleSet carrying all 17 seeded rule keys (the
// pinned rule table in the story's System Design section).
func TestSeed_ActiveVersionLoads(t *testing.T) {
	_, app := dbTestPools(t)
	ctx := context.Background()

	var activeCount int
	if err := app.QueryRow(ctx,
		`SELECT count(*) FROM rule_set_versions WHERE is_active`,
	).Scan(&activeCount); err != nil {
		t.Fatalf("count active rule_set_versions rows: %v", err)
	}
	if activeCount != 1 {
		t.Fatalf("count(rule_set_versions WHERE is_active) = %d, want exactly 1 -- "+
			"expected the migration-seeded active rule-set", activeCount)
	}

	rs := loadActive(t, app)
	// The ONE place this package asserts WHICH version is active (loadActive
	// itself is deliberately version-agnostic -- see its doc comment).
	if rs.Version != activeSeedVersion {
		t.Fatalf("active RuleSet.Version = %d, want %d -- expected the migration-seeded active version",
			rs.Version, activeSeedVersion)
	}
	if len(rs.Rules) != 19 {
		t.Fatalf("len(RuleSet.Rules) = %d, want 19 -- expected the migration-seeded active (v2) rule set "+
			"(v1's 17 base rules + the 2 line-item rules, re-issued under v2 by the rule_set_v2 migration)", len(rs.Rules))
	}

	wantKeys := []string{
		"buyer-tin-format",
		"currency-allowed",
		"currency-required",
		"invoice-number-required",
		"issue-date-required",
		"line-cost-non-negative",
		"line-items-required",
		"line-items-sum-subtotal",
		"no-duplicate-line-items",
		"subtotal-non-negative",
		"subtotal-required",
		"supplier-name-required",
		"supplier-tin-format",
		"supplier-tin-required",
		"total-non-negative",
		"total-required",
		"vat-non-negative",
		"vat-required",
		"vat-standard-rate",
	}
	gotKeys := make([]string, len(rs.Rules))
	for i, r := range rs.Rules {
		gotKeys[i] = r.Key
	}
	sort.Strings(gotKeys)
	if !reflect.DeepEqual(gotKeys, wantKeys) {
		t.Errorf("RuleSet.Rules keys = %v, want %v (the active v2 rule table's 19 keys: 17 base + 2 line-item rules)", gotKeys, wantKeys)
	}
}

// TestSeed_DemoContract (Core AC 3 / Test Spec "Demo: both violations" +
// "Demo: valid -> zero"): the bad payload (malformed TIN + wrong VAT) returns
// EXACTLY [supplier-tin-format, vat-standard-rate] (sorted, Decision N16),
// stamped with activeSeedVersion; the fully valid payload returns zero
// violations.
func TestSeed_DemoContract(t *testing.T) {
	_, app := dbTestPools(t)
	rs := loadActive(t, app)
	engine := NewDefaultEngine()

	t.Run("bad payload: exactly supplier-tin-format + vat-standard-rate", func(t *testing.T) {
		result, err := engine.Evaluate(badInvoicePayload(), rs)
		if err != nil {
			t.Fatalf("Evaluate(bad payload): %v", err)
		}
		if result.RuleSetVersion != activeSeedVersion {
			t.Errorf("RuleSetVersion = %d, want %d", result.RuleSetVersion, activeSeedVersion)
		}
		wantKeys := []string{"supplier-tin-format", "vat-standard-rate"}
		if got := violationKeys(result); !reflect.DeepEqual(got, wantKeys) {
			t.Errorf("bad payload violation keys = %v, want %v (exactly these two, sorted)", got, wantKeys)
		}
	})

	t.Run("valid payload: zero violations", func(t *testing.T) {
		result, err := engine.Evaluate(validInvoicePayload(), rs)
		if err != nil {
			t.Fatalf("Evaluate(valid payload): %v", err)
		}
		if len(result.Violations) != 0 {
			t.Errorf("valid payload violations = %+v, want none", result.Violations)
		}
	})
}

// TestSeed_TaxMathTolerance (Core AC 4 / Test Spec "Tax-math off-by-kobo
// (fail)" + "Tax-math correct (pass)" + "Tax-math rounding absorbed (pass)"):
// vat-standard-rate's tolerance is 0.005 with a strict > comparison -- a
// correct VAT and a correctly-rounded VAT both pass, while a one-kobo-off
// VAT fails.
func TestSeed_TaxMathTolerance(t *testing.T) {
	_, app := dbTestPools(t)
	rs := loadActive(t, app)
	engine := NewDefaultEngine()

	cases := []struct {
		name          string
		subtotal, vat float64
		wantViolation bool
	}{
		{"correct VAT (75.00 for subtotal 1000) passes", 1000.0, 75.00, false},
		{"one-kobo-off VAT (75.01 for subtotal 1000) fires", 1000.0, 75.01, true},
		{"rounding-absorbed VAT (25.00 for subtotal 333.33) passes", 333.33, 25.00, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := validInvoicePayload()
			invoiceOf(p)["subtotal"] = tc.subtotal
			invoiceOf(p)["vat"] = tc.vat

			result, err := engine.Evaluate(p, rs)
			if err != nil {
				t.Fatalf("Evaluate(subtotal=%.2f, vat=%.2f): %v", tc.subtotal, tc.vat, err)
			}
			if got := hasViolation(result, "vat-standard-rate"); got != tc.wantViolation {
				t.Errorf("vat-standard-rate fired = %v, want %v (subtotal=%.2f vat=%.2f) -- violations=%+v",
					got, tc.wantViolation, tc.subtotal, tc.vat, result.Violations)
			}
		})
	}
}

// TestSeed_TINFormat (Test Spec "TIN format (neg)" + "TIN format (pos)" +
// "Buyer TIN where-present"): supplier-tin-format fires on a malformed TIN
// (without also tripping supplier-tin-required, since the value is present)
// and passes on a well-formed one; buyer-tin-format does not fire when
// buyer.tin is simply absent (format/regex passes on absent -- "where
// present" semantics).
func TestSeed_TINFormat(t *testing.T) {
	_, app := dbTestPools(t)
	rs := loadActive(t, app)
	engine := NewDefaultEngine()

	t.Run("malformed supplier TIN fires format, not required", func(t *testing.T) {
		p := validInvoicePayload()
		invoiceOf(p)["supplier"].(map[string]any)["tin"] = "BADTIN"

		result, err := engine.Evaluate(p, rs)
		if err != nil {
			t.Fatalf("Evaluate: %v", err)
		}
		if !hasViolation(result, "supplier-tin-format") {
			t.Errorf("supplier-tin-format did not fire for TIN=%q -- violations=%+v", "BADTIN", result.Violations)
		}
		if hasViolation(result, "supplier-tin-required") {
			t.Error("supplier-tin-required fired for a present (if malformed) TIN -- should not, value is non-blank")
		}
	})

	t.Run("well-formed supplier TIN passes", func(t *testing.T) {
		result, err := engine.Evaluate(validInvoicePayload(), rs)
		if err != nil {
			t.Fatalf("Evaluate: %v", err)
		}
		if hasViolation(result, "supplier-tin-format") {
			t.Errorf("supplier-tin-format fired for a well-formed TIN -- violations=%+v", result.Violations)
		}
	})

	t.Run("absent buyer TIN does not fire buyer-tin-format", func(t *testing.T) {
		p := validInvoicePayload()
		delete(invoiceOf(p), "buyer")

		result, err := engine.Evaluate(p, rs)
		if err != nil {
			t.Fatalf("Evaluate: %v", err)
		}
		if hasViolation(result, "buyer-tin-format") {
			t.Errorf("buyer-tin-format fired when buyer is absent entirely -- format/regex must pass on absent -- violations=%+v", result.Violations)
		}
	})
}

// TestSeed_MandatoryPresence (Test Spec "Mandatory presence (neg)" +
// "Mandatory presence (pos)"): omitting a single mandatory field fires ONLY
// that field's *-required rule; the fully valid payload fires none of them.
func TestSeed_MandatoryPresence(t *testing.T) {
	_, app := dbTestPools(t)
	rs := loadActive(t, app)
	engine := NewDefaultEngine()

	t.Run("missing supplier.name fires supplier-name-required, and only it", func(t *testing.T) {
		p := validInvoicePayload()
		delete(invoiceOf(p)["supplier"].(map[string]any), "name")

		result, err := engine.Evaluate(p, rs)
		if err != nil {
			t.Fatalf("Evaluate: %v", err)
		}
		if !hasViolation(result, "supplier-name-required") {
			t.Errorf("supplier-name-required did not fire for a missing supplier.name -- violations=%+v", result.Violations)
		}
		for _, v := range result.Violations {
			if v.RuleKey != "supplier-name-required" {
				t.Errorf("unexpected violation %+v, want only supplier-name-required", v)
			}
		}
	})

	t.Run("valid payload fires no *-required violation", func(t *testing.T) {
		result, err := engine.Evaluate(validInvoicePayload(), rs)
		if err != nil {
			t.Fatalf("Evaluate: %v", err)
		}
		for _, v := range result.Violations {
			if strings.HasSuffix(v.RuleKey, "-required") {
				t.Errorf("unexpected required violation %+v on a fully valid payload", v)
			}
		}
	})
}

// TestSeed_CurrencyEnum (Test Spec "Enum (neg)" + "Enum (pos)"):
// currency-allowed fires for a non-NGN currency and passes for NGN.
func TestSeed_CurrencyEnum(t *testing.T) {
	_, app := dbTestPools(t)
	rs := loadActive(t, app)
	engine := NewDefaultEngine()

	t.Run("USD fires currency-allowed", func(t *testing.T) {
		p := validInvoicePayload()
		invoiceOf(p)["currency"] = "USD"

		result, err := engine.Evaluate(p, rs)
		if err != nil {
			t.Fatalf("Evaluate: %v", err)
		}
		if !hasViolation(result, "currency-allowed") {
			t.Errorf("currency-allowed did not fire for currency=USD -- violations=%+v", result.Violations)
		}
		if hasViolation(result, "currency-required") {
			t.Error("currency-required fired for a present (if disallowed) currency -- should not, value is non-blank")
		}
	})

	t.Run("NGN passes", func(t *testing.T) {
		result, err := engine.Evaluate(validInvoicePayload(), rs)
		if err != nil {
			t.Fatalf("Evaluate: %v", err)
		}
		if hasViolation(result, "currency-allowed") {
			t.Errorf("currency-allowed fired for currency=NGN -- violations=%+v", result.Violations)
		}
	})
}

// TestSeed_RangeNonNegative (Test Spec "Range (neg)" + "Range (pos)"): a
// negative amount fires its *-non-negative range rule; a non-negative amount
// does not.
func TestSeed_RangeNonNegative(t *testing.T) {
	_, app := dbTestPools(t)
	rs := loadActive(t, app)
	engine := NewDefaultEngine()

	t.Run("negative subtotal fires subtotal-non-negative", func(t *testing.T) {
		p := validInvoicePayload()
		invoiceOf(p)["subtotal"] = -1.0

		result, err := engine.Evaluate(p, rs)
		if err != nil {
			t.Fatalf("Evaluate: %v", err)
		}
		if !hasViolation(result, "subtotal-non-negative") {
			t.Errorf("subtotal-non-negative did not fire for subtotal=-1 -- violations=%+v", result.Violations)
		}
	})

	t.Run("non-negative subtotal passes", func(t *testing.T) {
		result, err := engine.Evaluate(validInvoicePayload(), rs)
		if err != nil {
			t.Fatalf("Evaluate: %v", err)
		}
		if hasViolation(result, "subtotal-non-negative") {
			t.Errorf("subtotal-non-negative fired for subtotal=1000 -- violations=%+v", result.Violations)
		}
	})
}

// TestSeed_DuplicateLineItemsCEL (Core AC 2/8 / Test Spec "Duplicate CEL
// (neg)" + "Duplicate CEL (pos)" + "Duplicate CEL id-less (no crash)" +
// "CEL compiles + bool"): the hardened no-duplicate-line-items expr fires on
// a shared line-item id, passes on unique ids, and -- critically -- neither
// 500s NOR misfires when a line item lacks an `id` altogether (the
// QA-Verify-hardened has(x.id)/has(y.id) guards). An empty line_items array
// also passes both this rule and line-items-required (presence-only
// semantics, per the story's Decisions section).
func TestSeed_DuplicateLineItemsCEL(t *testing.T) {
	_, app := dbTestPools(t)
	rs := loadActive(t, app)
	engine := NewDefaultEngine()

	t.Run("duplicate ids fire no-duplicate-line-items", func(t *testing.T) {
		p := validInvoicePayload()
		invoiceOf(p)["line_items"] = []any{
			map[string]any{"id": "1", "description": "Widget", "quantity": 10.0, "unit_price": 100.0, "line_total": 1000.0},
			map[string]any{"id": "1", "description": "Widget (dup)", "quantity": 1.0, "unit_price": 5.0, "line_total": 5.0},
		}

		result, err := engine.Evaluate(p, rs)
		if err != nil {
			t.Fatalf("Evaluate(duplicate ids): got error %v, want no error (CEL must compile and return bool)", err)
		}
		if !hasViolation(result, "no-duplicate-line-items") {
			t.Errorf("no-duplicate-line-items did not fire for two line items sharing id=1 -- violations=%+v", result.Violations)
		}
	})

	t.Run("unique ids pass", func(t *testing.T) {
		p := validInvoicePayload()
		invoiceOf(p)["line_items"] = []any{
			map[string]any{"id": "1", "description": "Widget", "quantity": 10.0, "unit_price": 100.0, "line_total": 1000.0},
			map[string]any{"id": "2", "description": "Gadget", "quantity": 1.0, "unit_price": 50.0, "line_total": 50.0},
		}

		result, err := engine.Evaluate(p, rs)
		if err != nil {
			t.Fatalf("Evaluate(unique ids): got error %v, want no error", err)
		}
		if hasViolation(result, "no-duplicate-line-items") {
			t.Errorf("no-duplicate-line-items fired for two line items with distinct ids -- violations=%+v", result.Violations)
		}
	})

	t.Run("id-less line item does not 500 and is exempt from dedup", func(t *testing.T) {
		p := validInvoicePayload()
		invoiceOf(p)["line_items"] = []any{
			map[string]any{"description": "no id on this item", "quantity": 1.0, "unit_price": 10.0, "line_total": 10.0},
		}

		result, err := engine.Evaluate(p, rs)
		if err != nil {
			t.Fatalf("Evaluate(id-less line item): got error %v, want no error (no 500) -- "+
				"the hardened CEL expr must guard has(x.id) before comparing ids", err)
		}
		if hasViolation(result, "no-duplicate-line-items") {
			t.Errorf("no-duplicate-line-items fired for a single id-less line item, want no violation "+
				"(id-less items are exempt from dedup -- you cannot dedupe what has no key) -- violations=%+v", result.Violations)
		}
	})

	t.Run("empty line_items array passes (advisory, per Decisions)", func(t *testing.T) {
		p := validInvoicePayload()
		invoiceOf(p)["line_items"] = []any{}

		result, err := engine.Evaluate(p, rs)
		if err != nil {
			t.Fatalf("Evaluate(empty line_items): got error %v, want no error", err)
		}
		if hasViolation(result, "no-duplicate-line-items") {
			t.Errorf("no-duplicate-line-items fired for an empty line_items array -- violations=%+v", result.Violations)
		}
		if hasViolation(result, "line-items-required") {
			t.Error("line-items-required fired for an empty (but present) line_items array -- " +
				"required checks presence of the array only, not non-emptiness (per the story's Decisions section)")
		}
	})
}

// TestSeed_KillSwitch (Core AC 5 / Test Spec "Kill-switch"): disabling
// vat-standard-rate via the real app-grant Store.ToggleRule path drops its
// violation from the very next evaluate of the bad payload -- only
// supplier-tin-format remains. The rule is restored to enabled=true in
// cleanup (direct superuser UPDATE, independent of whether ToggleRule itself
// succeeded) so this test never leaves the shared migrated v1 disabled for
// any other test in this package.
func TestSeed_KillSwitch(t *testing.T) {
	super, app := dbTestPools(t)

	// Restore on the ACTIVE version -- matching ToggleRule's own predicate
	// (`WHERE is_active`, store.go:137-139), which is what disabled the rule
	// below. Hardcoding `v.version = 1` here would silently restore the WRONG
	// row once the active version is not literally 1, leaving vat-standard-rate
	// DISABLED on the live active rule-set for every subsequent test and on the
	// shared dev DB (RS-V2-11). Mirroring ToggleRule's predicate is what stops
	// the two from drifting apart again.
	t.Cleanup(func() {
		if _, err := super.Exec(context.Background(),
			`UPDATE rules r SET enabled = true
			   FROM rule_set_versions v
			  WHERE r.rule_set_version_id = v.id AND v.is_active AND r.key = 'vat-standard-rate'`,
		); err != nil {
			t.Errorf("cleanup: restore vat-standard-rate enabled=true: %v", err)
		}
	})

	store := NewStore(app)
	if _, err := store.ToggleRule(newTestIdentity(), "vat-standard-rate", false); err != nil {
		t.Fatalf("ToggleRule(vat-standard-rate, false): %v", err)
	}

	rs := loadActive(t, app)
	engine := NewDefaultEngine()
	result, err := engine.Evaluate(badInvoicePayload(), rs)
	if err != nil {
		t.Fatalf("Evaluate(bad payload) after kill-switch: %v", err)
	}
	if hasViolation(result, "vat-standard-rate") {
		t.Error("vat-standard-rate still fired after being disabled via ToggleRule")
	}
	wantKeys := []string{"supplier-tin-format"}
	if got := violationKeys(result); !reflect.DeepEqual(got, wantKeys) {
		t.Errorf("violations after disabling vat-standard-rate = %v, want %v (only supplier-tin-format remains)", got, wantKeys)
	}
}

// TestSeed_ReversibilityRollback (Test Spec "Reversibility round-trip";
// optional per the story's Test Specs "your judgment" note): runs the
// migration's Down statement (DELETE FROM rule_set_versions WHERE version =
// 1) inside a superuser transaction that is ALWAYS rolled back, proving the
// Down's effect -- zero active versions, zero rules under version 1 (FK
// ON DELETE CASCADE) -- without permanently removing the shared active v1
// every other test in this file depends on. The CI `migrations` job's
// reset->up round-trip (every Down, then migrate-up) is the authoritative
// reversibility check; this is a narrower, same-package sanity check that
// the Down statement itself has the right shape and cascade behavior.
//
// Guards against a vacuous pass: without the seed, "DELETE ... WHERE
// version = 1" matches zero rows and the post-conditions (zero active, zero
// v1 rules) would trivially hold whether or not the seed exists -- so this
// test first asserts a version=1 row is actually THERE (with at least one
// rule under it) before running the Down, making the pre-seed state a loud
// t.Fatalf (a real RED) rather than a silent, meaningless pass.
func TestSeed_ReversibilityRollback(t *testing.T) {
	super, _ := dbTestPools(t)
	ctx := context.Background()

	tx, err := super.Begin(ctx)
	if err != nil {
		t.Fatalf("begin superuser tx: %v", err)
	}
	defer func() {
		_ = tx.Rollback(ctx) // always roll back -- proves the Down's effect without a lasting mutation.
	}()

	// M4-17: v1 is now SEALED, so the DELETE below (this test's simulated Down) would be
	// rejected by the rule_set_versions_seal_guard trigger (Guard C) -- modeling the real
	// `goose reset` ordering, where M4-17's own Down drops the lock (this DISABLE) before
	// any older migration's Down runs. DISABLE TRIGGER USER is transactional (rolled back
	// with tx) and disables only USER triggers, leaving the FK RI/cascade triggers intact,
	// so the "rules gone via ON DELETE CASCADE" post-condition below still holds. Do NOT
	// use `SET LOCAL session_replication_role = 'replica'` here -- that also suppresses RI
	// triggers, which would make the cascade assertion pass for the wrong reason.
	if _, err := tx.Exec(ctx, `ALTER TABLE rule_set_versions DISABLE TRIGGER USER`); err != nil {
		t.Fatalf("disable USER triggers on rule_set_versions: %v", err)
	}
	if _, err := tx.Exec(ctx, `ALTER TABLE rules DISABLE TRIGGER USER`); err != nil {
		t.Fatalf("disable USER triggers on rules: %v", err)
	}

	var preCount, preRuleCount int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM rule_set_versions WHERE version = 1`).Scan(&preCount); err != nil {
		t.Fatalf("count version=1 rows before Down: %v", err)
	}
	if preCount != 1 {
		t.Fatalf("count(rule_set_versions WHERE version=1) before Down = %d, want 1 -- "+
			"expected the migration-seeded active v1, got none (nothing to reverse)", preCount)
	}
	if err := tx.QueryRow(ctx,
		`SELECT count(*) FROM rules r JOIN rule_set_versions v ON v.id = r.rule_set_version_id WHERE v.version = 1`,
	).Scan(&preRuleCount); err != nil {
		t.Fatalf("count v1 rules before Down: %v", err)
	}
	if preRuleCount == 0 {
		t.Fatalf("count(rules under version=1) before Down = 0, want > 0 -- " +
			"expected the migration-seeded v1 rules, got none (nothing to cascade-delete)")
	}

	if _, err := tx.Exec(ctx, `DELETE FROM rule_set_versions WHERE version = 1`); err != nil {
		t.Fatalf("run migration Down (DELETE FROM rule_set_versions WHERE version = 1): %v -- "+
			"expected the migration-seeded active v1 to exist and be deletable", err)
	}

	// Scoped to the M3-05 seed's OWN row, not the global active count: this
	// test is about the M3-05 seed migration's Down (which deletes exactly
	// `version = 1`), and v1 IS literally version 1, permanently, by
	// definition. Asserting "zero ACTIVE versions globally" was only ever true
	// because v1 happened to be the sole active version; post-v2-publish
	// another version is active and deleting v1 correctly leaves it alone, so
	// the global assertion falsifies for a reason that has nothing to do with
	// this Down (RS-V2-13).
	var v1Count int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM rule_set_versions WHERE version = 1`).Scan(&v1Count); err != nil {
		t.Fatalf("count version=1 rows after Down: %v", err)
	}
	if v1Count != 0 {
		t.Errorf("rule_set_versions WHERE version=1 after Down = %d, want 0 (the M3-05 seed's own row is gone)", v1Count)
	}

	var v1RuleCount int
	if err := tx.QueryRow(ctx,
		`SELECT count(*) FROM rules r JOIN rule_set_versions v ON v.id = r.rule_set_version_id WHERE v.version = 1`,
	).Scan(&v1RuleCount); err != nil {
		t.Fatalf("count v1 rules after Down: %v", err)
	}
	if v1RuleCount != 0 {
		t.Errorf("rules under version=1 after Down = %d, want 0 (ON DELETE CASCADE)", v1RuleCount)
	}
}

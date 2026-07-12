// M3-10-03 — kill-switch end-to-end suite (Core AC 3): jointly proves, for
// TWO distinct seeded rule types (vat-standard-rate, a tax_math rule; and
// currency-allowed, an enum rule), that ToggleRule's validate-effect
// (no-redeploy live reload — store_test.go's TestStore_ToggleLiveReload
// pattern, seed_test.go/seed_adversarial_test.go's TestSeed_KillSwitch /
// TestSeed_KillSwitchSymmetry pattern), same-tx audit trail (store_test.go's
// TestStore_ToggleFlipsAndAudits / TestStore_ToggleRoundTripEvents pattern),
// and reversibility all hold TOGETHER for each rule, not just individually
// across separate single-purpose tests.
//
// `rules` is a GLOBAL, untenanted table (no tenant_id, no RLS — see
// store.go's file header): toggling mutates the one shared migrated v1 row
// for real. t.Cleanup (registered FIRST, before any mutation) restores both
// keys to enabled=true via a superuser UPDATE unconditionally — it must run
// even if the test fails partway through, or every other test in this
// package that assumes v1's rules start enabled would break. This suite does
// NOT call t.Parallel() (matching every other file in this package), since
// the toggle affects process-wide shared DB state.
//
// Each rule type runs under its OWN freshly-generated tenantID (uuid.NewString
// in its own t.Run) rather than sharing one across both — auditCountTenant
// counts ALL rows for {tenantID, event}, so sharing a tenant across both
// subtests would make the second subtest's "delta of 1" assertion collide
// with the first's row (see store_test.go's auditCountTenant/auditPayloadTenant,
// reused here verbatim, both scoped by tenantID+event).
//
// Run (same env gate as the rest of the package):
//
//	DATABASE_URL="postgres://invoice_app:app@localhost:5432/invoice_os?sslmode=disable" \
//	DATABASE_SUPERUSER_URL="postgres://postgres:postgres@localhost:5432/invoice_os?sslmode=disable" \
//	go test -count=1 ./internal/validation/...
package validation

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
)

// killSwitchCase pairs a seeded rule key with a mutator that, applied to a
// fresh validInvoicePayload(), fires exactly that rule (and no other) so the
// "dropped after disable / restored after enable" assertions are unambiguous
// about which rule caused the change.
type killSwitchCase struct {
	name   string
	key    string
	mutate func(p Payload)
}

// TestKillSwitch_E2E drives the kill-switch flow (baseline fire -> disable ->
// validate-effect drop -> same-tx audit -> re-enable -> paired audit ->
// reversibility) over two seeded rules from different rule TYPES
// (tax_math and enum), proving the mechanism generalizes rather than only
// working for the one rule (vat-standard-rate) the earlier seed_test.go /
// seed_adversarial_test.go suites already covered individually.
func TestKillSwitch_E2E(t *testing.T) {
	super, app := dbTestPools(t)

	// Registered FIRST, before either subtest mutates anything: must run
	// unconditionally (pass or fail, and even if a subtest never reaches its
	// own re-enable step) so no other test in this package ever observes a
	// disabled seeded rule.
	t.Cleanup(func() {
		if _, err := super.Exec(context.Background(),
			`UPDATE rules SET enabled = true WHERE key IN ('vat-standard-rate', 'currency-allowed')`,
		); err != nil {
			t.Errorf("cleanup: restore vat-standard-rate/currency-allowed enabled=true: %v", err)
		}
	})

	cases := []killSwitchCase{
		{
			name: "vat-standard-rate (tax_math)",
			key:  "vat-standard-rate",
			mutate: func(p Payload) {
				invoiceOf(p)["vat"] = 70.0 // valid subtotal 1000 -> expected vat 75 +/- 0.005; 70 fires.
			},
		},
		{
			name: "currency-allowed (enum)",
			key:  "currency-allowed",
			mutate: func(p Payload) {
				invoiceOf(p)["currency"] = "USD"
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			tenantID := uuid.NewString()
			c := auth.WithIdentity(ctx, auth.Identity{Subject: "user-1", Role: "authenticated", TenantID: tenantID})

			firePayload := validInvoicePayload()
			tc.mutate(firePayload)

			store := NewStore(app)
			engine := NewDefaultEngine()

			// Both event names are needed from step 2 onward (disable) AND at
			// step 2/4's cross-event isolation checks (a disable must not also
			// write an enabled-event row, and vice versa) -- declared together
			// up front rather than one per step.
			const disabledEvent = "validation.rule.disabled"
			const enabledEvent = "validation.rule.enabled"

			// Step 1: baseline -- the rule fires on the seeded, enabled v1.
			rs := loadV1(t, app)
			result, err := engine.Evaluate(firePayload, rs)
			if err != nil {
				t.Fatalf("Evaluate(firePayload) baseline: %v", err)
			}
			if !hasViolation(result, tc.key) {
				t.Fatalf("baseline: %s did not fire -- violations=%+v (fixture payload must trip this rule before toggling)", tc.key, result.Violations)
			}

			// Step 2: disable -- exactly one new "validation.rule.disabled" audit
			// row under this tenant, same tx as the UPDATE.
			beforeDisabled := auditCountTenant(t, app, tenantID, disabledEvent)

			toggled, err := store.ToggleRule(c, tc.key, false)
			if err != nil {
				t.Fatalf("ToggleRule(%s, false): %v", tc.key, err)
			}
			if toggled.Enabled {
				t.Errorf("ToggleRule(%s, false).Enabled = true, want false", tc.key)
			}

			afterDisabled := auditCountTenant(t, app, tenantID, disabledEvent)
			if afterDisabled != beforeDisabled+1 {
				t.Fatalf("%s audit_log rows for %s = %d, want %d (exactly one new row)", tc.key, disabledEvent, afterDisabled, beforeDisabled+1)
			}
			assertTogglePayload(t, app, tenantID, disabledEvent, tc.key, rs.Version, true, false)

			// Adversarial: column-level persistence, read directly via
			// superuser -- independent of and complementary to step 3's
			// validate-effect check (which goes through loadV1+Evaluate, not
			// the raw column). A ToggleRule that returned Enabled=false in its
			// Rule struct without the UPDATE actually landing would pass the
			// earlier `toggled.Enabled` check but fail here.
			if got := ruleEnabledV1(t, super, tc.key); got {
				t.Errorf("%s: rules.enabled column = true after ToggleRule(false), want false (column-level persistence)", tc.key)
			}
			// Adversarial: event isolation -- a disable must not ALSO write an
			// "enabled" event for this tenant. The two events are mutually
			// exclusive per flip, not "logs both, caller checks the one it
			// cares about."
			if n := auditCountTenant(t, app, tenantID, enabledEvent); n != 0 {
				t.Errorf("%s: %s audit_log rows after disable = %d, want 0 (disable must not also write an enabled-event row)", tc.key, enabledEvent, n)
			}

			// Step 3: validate-effect -- a FRESH load+evaluate must no longer fire
			// the disabled rule (no redeploy). A control rule (supplier-tin-format,
			// untouched, format-type) on the independently-bad demo payload must
			// STILL fire, proving only the toggled rule dropped.
			rs2 := loadV1(t, app)
			result2, err := engine.Evaluate(firePayload, rs2)
			if err != nil {
				t.Fatalf("Evaluate(firePayload) after disable: %v", err)
			}
			if hasViolation(result2, tc.key) {
				t.Errorf("%s still fired after being disabled via ToggleRule -- violations=%+v", tc.key, result2.Violations)
			}
			controlResult, err := engine.Evaluate(badInvoicePayload(), rs2)
			if err != nil {
				t.Fatalf("Evaluate(badInvoicePayload) control check after disabling %s: %v", tc.key, err)
			}
			if !hasViolation(controlResult, "supplier-tin-format") {
				t.Errorf("control rule supplier-tin-format did not fire after disabling %s -- only the toggled rule should have dropped, not the whole rule set", tc.key)
			}

			// Step 4: re-enable -- paired "validation.rule.enabled" audit row.
			beforeEnabled := auditCountTenant(t, app, tenantID, enabledEvent)

			restored, err := store.ToggleRule(c, tc.key, true)
			if err != nil {
				t.Fatalf("ToggleRule(%s, true) restore: %v", tc.key, err)
			}
			if !restored.Enabled {
				t.Errorf("ToggleRule(%s, true).Enabled = false, want true", tc.key)
			}

			afterEnabled := auditCountTenant(t, app, tenantID, enabledEvent)
			if afterEnabled != beforeEnabled+1 {
				t.Fatalf("%s audit_log rows for %s = %d, want %d (exactly one new row)", tc.key, enabledEvent, afterEnabled, beforeEnabled+1)
			}
			assertTogglePayload(t, app, tenantID, enabledEvent, tc.key, rs.Version, false, true)

			// Adversarial: column-level persistence after re-enable (mirrors
			// the step 2 check above).
			if got := ruleEnabledV1(t, super, tc.key); !got {
				t.Errorf("%s: rules.enabled column = false after ToggleRule(true), want true (column-level persistence)", tc.key)
			}
			// Adversarial: event isolation -- re-enabling must not ALSO write a
			// second "disabled" event for this tenant; the disabled count from
			// step 2 must stay exactly afterDisabled (unchanged), not
			// double-fire.
			if n := auditCountTenant(t, app, tenantID, disabledEvent); n != afterDisabled {
				t.Errorf("%s: %s audit_log rows after re-enable = %d, want %d (re-enable must not also write a disabled-event row)", tc.key, disabledEvent, n, afterDisabled)
			}

			// Step 5: reversibility -- a FRESH load+evaluate fires the rule again.
			rs3 := loadV1(t, app)
			result3, err := engine.Evaluate(firePayload, rs3)
			if err != nil {
				t.Fatalf("Evaluate(firePayload) after re-enable: %v", err)
			}
			if !hasViolation(result3, tc.key) {
				t.Errorf("%s did not fire after being re-enabled via ToggleRule -- violations=%+v (toggle must be reversible)", tc.key, result3.Violations)
			}
		})
	}
}

// assertTogglePayload reads the most recent audit_log row for tenantID+event
// (auditPayloadTenant, store_test.go) and asserts its payload carries the
// exact field names store.go's ToggleRule actually serializes: "key",
// "version", "from", "to" (see store.go's audit.Record call). wantVersion is
// the caller's own baseline-load RuleSet.Version (step 1's loadV1, taken
// before any toggle) rather than a hardcoded literal -- it asserts the
// payload's "version" is actually the active rule_set_versions.version at
// toggle time, not merely "some number" (store_test.go's
// TestStore_ToggleFlipsAndAudits established the type-only precedent this
// tightens).
func assertTogglePayload(t *testing.T, pool *pgxpool.Pool, tenantID, event, key string, wantVersion int, wantFrom, wantTo bool) {
	t.Helper()
	payload := auditPayloadTenant(t, pool, tenantID, event)
	var p map[string]any
	if err := json.Unmarshal(payload, &p); err != nil {
		t.Fatalf("unmarshal audit payload %s: %v", payload, err)
	}
	if p["key"] != key {
		t.Errorf("audit payload key = %v, want %q", p["key"], key)
	}
	if got, ok := p["version"].(float64); !ok {
		t.Errorf("audit payload version = %v (%T), want a numeric version", p["version"], p["version"])
	} else if got != float64(wantVersion) {
		t.Errorf("audit payload version = %v, want %d (the active rule_set_versions.version at toggle time)", got, wantVersion)
	}
	if p["from"] != wantFrom {
		t.Errorf("audit payload from = %v, want %v", p["from"], wantFrom)
	}
	if p["to"] != wantTo {
		t.Errorf("audit payload to = %v, want %v", p["to"], wantTo)
	}
}

// ruleEnabledV1 reads rules.enabled directly via the superuser pool for key
// under the migrated v1 rule_set_versions row -- column-level persistence,
// independent of and complementary to the validate-effect assertions (steps
// 3/5 above), which go through loadV1+Evaluate rather than the raw column.
func ruleEnabledV1(t *testing.T, pool *pgxpool.Pool, key string) bool {
	t.Helper()
	var enabled bool
	if err := pool.QueryRow(context.Background(),
		`SELECT r.enabled FROM rules r JOIN rule_set_versions v ON r.rule_set_version_id = v.id
		 WHERE v.version = 1 AND r.key = $1`, key,
	).Scan(&enabled); err != nil {
		t.Fatalf("read rules.enabled for key=%q: %v", key, err)
	}
	return enabled
}

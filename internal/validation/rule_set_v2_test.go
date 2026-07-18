// task-111 / M4-04-01 (Test-first: yes) -- Mode A RED specs for the rule-set
// v1 immutability revert (publish v2). Transcribes task-111's RS-V2-*
// Test Specs table into runnable Go tests, authored BEFORE:
//
//	(a) the v2 migration exists (migrations/<goose-ts>_rule_set_v2.sql --
//	    not yet authored: v1 deleting its 2 wrongly-added rules and going
//	    inactive, v2 being inserted active with the 17+2=19 rules); and
//	(b) any Category-A fixture in this package is fixed to DISCOVER the
//	    active version by id rather than hardcode `version = 1`.
//
// See task-111 (mcp__backlog__task_view id=task-111) for the authoritative
// Description/Acceptance Criteria/Test Specs/Decisions, and the M4-04
// Validate Gate story (§M4-04-01) + QA Debate Log (F2, F4, F6) for the full
// context this file assumes.
//
// This file does NOT modify any existing test -- seed_test.go,
// schema_test.go, store_test.go, seed_adversarial_test.go,
// kill_switch_e2e_test.go, collect_all_integration_test.go,
// all_rule_types_test.go, and golden_test.go are all untouched. Fixing them
// (the loadV1->loadActive rename, the three semantic seed_test.go sites, the
// schema_test.go/store_test.go restore-by-id fixes) is the Stage-3
// executor's job. Where a spec's only meaningful test is "does an EXISTING
// fixture, invoked for real, behave correctly", this file invokes that real
// fixture (seedVersion, TestStore_LoadNoActiveErrors,
// TestSeed_ReversibilityRollback) as a nested t.Run subtest rather than a
// re-implementation, so THIS suite's own greenness tracks the real,
// evolving fixture code.
//
// RS-V2-08 (the CI reset->up reversibility gate) is not re-authored as a Go
// test here: it is already generically covered by the existing `migrations`
// CI job (.github/workflows/ci.yml:171-177, `make migrate-reset` then
// `make migrate-up`), which re-validates on every migration including this
// one -- duplicating it as a package-level Go test would mean tearing down
// and rebuilding the ENTIRE migration history from inside a unit test, which
// would corrupt the shared dev DB every other test in this package depends
// on. RS-V2-14's Category-A/B triage (a disclosed judgment call, not a
// zero-hits check -- see task-111's own "Judgment residual" section and QA
// Debate Log F6) is only partially mechanized below (scope + count
// baseline); RS-V2-15 and RS-V2-16/17 are not authored as new tests at all.
// See this file's QA report (not this comment) for why, rather than
// silently reinterpreting the spec table.
//
// Run (same env gate as the rest of the package):
//
//	DATABASE_URL="postgres://invoice_app:app@localhost:5432/invoice_os?sslmode=disable" \
//	DATABASE_SUPERUSER_URL="postgres://postgres:postgres@localhost:5432/invoice_os?sslmode=disable" \
//	go test -count=1 -run 'TestRuleSetV2_' -v ./internal/validation/...
package validation

import (
	"context"
	"encoding/json"
	"os/exec"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

// ---------------------------------------------------------------------
// RS-V2-01..02 -- v1 narrowed to its 17 base rules and deactivated.
// ---------------------------------------------------------------------

// TestRuleSetV2_V1HasSeventeenRulesAndIsInactive (RS-V2-01, RS-V2-02): after
// the v2 migration, v1 carries exactly the M3-05 base 17 rule keys (no
// line-cost-non-negative, no line-items-sum-subtotal) and is_active=false --
// M3-04's immutability guarantee restored (Core AC #1 / task-111 AC#1).
func TestRuleSetV2_V1HasSeventeenRulesAndIsInactive(t *testing.T) {
	_, app := dbTestPools(t)
	ctx := context.Background()

	var versionID string
	var isActive bool
	if err := app.QueryRow(ctx,
		`SELECT id, is_active FROM rule_set_versions WHERE version = 1`,
	).Scan(&versionID, &isActive); err != nil {
		t.Fatalf("read rule_set_versions WHERE version=1: %v", err)
	}

	if isActive {
		t.Errorf("rule_set_versions.is_active for version=1 = true, want false -- " +
			"v1 must be deactivated by the v2 migration [RS-V2-02]")
	}

	gotKeys := ruleKeysUnder(t, app, versionID)

	wantKeys := []string{
		"buyer-tin-format",
		"currency-allowed",
		"currency-required",
		"invoice-number-required",
		"issue-date-required",
		"line-items-required",
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

	if len(gotKeys) != 17 {
		t.Errorf("count(rules WHERE version=1) = %d, want 17 (the M3-05 base 17 -- "+
			"line-cost-non-negative and line-items-sum-subtotal must be deleted from v1) [RS-V2-01] -- got keys %v",
			len(gotKeys), gotKeys)
	}
	if !reflect.DeepEqual(gotKeys, wantKeys) {
		t.Errorf("v1 rule keys = %v, want %v [RS-V2-01]", gotKeys, wantKeys)
	}
}

// ---------------------------------------------------------------------
// RS-V2-03..04 -- v2 is the sole active version, carrying 19 rules.
// ---------------------------------------------------------------------

// TestRuleSetV2_OnlyV2ActiveWithNineteenRules (RS-V2-03, RS-V2-04): exactly
// one rule_set_versions row is active, its version is 2, and it carries the
// 17 base + 2 line-item keys (19 total).
func TestRuleSetV2_OnlyV2ActiveWithNineteenRules(t *testing.T) {
	_, app := dbTestPools(t)
	ctx := context.Background()

	var activeCount int
	if err := app.QueryRow(ctx, `SELECT count(*) FROM rule_set_versions WHERE is_active`).Scan(&activeCount); err != nil {
		t.Fatalf("count active rule_set_versions: %v", err)
	}
	if activeCount != 1 {
		t.Fatalf("count(rule_set_versions WHERE is_active) = %d, want 1 [RS-V2-03]", activeCount)
	}

	activeID, activeVersion := activeVersionRow(t, app)
	if activeVersion != 2 {
		t.Errorf("the sole active rule_set_versions.version = %d, want 2 [RS-V2-03]", activeVersion)
	}

	gotKeys := ruleKeysUnder(t, app, activeID)
	if len(gotKeys) != 19 {
		t.Errorf("count(rules under the active version) = %d, want 19 [RS-V2-04] -- got keys %v", len(gotKeys), gotKeys)
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
	if !reflect.DeepEqual(gotKeys, wantKeys) {
		t.Errorf("active version rule keys = %v, want %v [RS-V2-04]", gotKeys, wantKeys)
	}
}

// ---------------------------------------------------------------------
// RS-V2-05 -- LoadActiveRuleSet returns v2.
// ---------------------------------------------------------------------

// TestRuleSetV2_LoadActiveRuleSetReturnsV2 (RS-V2-05): the real Store,
// called directly (NOT via seed_test.go's loadV1, which hard-pins
// Version==1 -- exactly the assumption this story exists to remove), must
// return Version==2 with 19 rules.
func TestRuleSetV2_LoadActiveRuleSetReturnsV2(t *testing.T) {
	_, app := dbTestPools(t)
	store := NewStore(app)

	rs, err := store.LoadActiveRuleSet(newTestIdentity())
	if err != nil {
		t.Fatalf("LoadActiveRuleSet: %v", err)
	}
	if rs.Version != 2 {
		t.Errorf("RuleSet.Version = %d, want 2 [RS-V2-05]", rs.Version)
	}
	if len(rs.Rules) != 19 {
		t.Errorf("len(RuleSet.Rules) = %d, want 19 [RS-V2-05]", len(rs.Rules))
	}
}

// ---------------------------------------------------------------------
// RS-V2-06 -- every v2 rule ships enabled=true (not inherited from v1).
// ---------------------------------------------------------------------

// TestRuleSetV2_AllActiveRulesEnabledOnPublish (RS-V2-06,
// [v2-ships-as-authored]): every rule under the active (v2) version has
// enabled=true. Guards against a vacuous pass two ways: first asserts the
// active version really is 2 (a loud, real RED today -- the active version
// is still 1), then asserts there is at least one rule to check before
// asserting none are disabled.
func TestRuleSetV2_AllActiveRulesEnabledOnPublish(t *testing.T) {
	_, app := dbTestPools(t)
	ctx := context.Background()

	activeID, activeVersion := activeVersionRow(t, app)
	if activeVersion != 2 {
		t.Fatalf("active rule_set_versions.version = %d, want 2 -- expected the v2 migration to be active "+
			"[RS-V2-06 precondition]", activeVersion)
	}

	var n int
	if err := app.QueryRow(ctx, `SELECT count(*) FROM rules WHERE rule_set_version_id = $1`, activeID).Scan(&n); err != nil {
		t.Fatalf("count rules under v2: %v", err)
	}
	if n == 0 {
		t.Fatalf("count(rules under v2) = 0, want > 0 -- nothing to check enabled on [RS-V2-06]")
	}

	var disabledCount int
	if err := app.QueryRow(ctx,
		`SELECT count(*) FROM rules WHERE rule_set_version_id = $1 AND NOT enabled`, activeID,
	).Scan(&disabledCount); err != nil {
		t.Fatalf("count disabled rules under v2: %v", err)
	}
	if disabledCount != 0 {
		t.Errorf("count(disabled rules under v2) = %d, want 0 -- every v2 rule must ship enabled=true, not "+
			"inherit v1's live enabled column [RS-V2-06, v2-ships-as-authored]", disabledCount)
	}
}

// ---------------------------------------------------------------------
// RS-V2-07 -- the 2 line-item rules' params are byte-identical to
// migrations/20260715120000_line_rules.sql's.
// ---------------------------------------------------------------------

// TestRuleSetV2_LineItemRuleParamsMatchLineRulesMigration (RS-V2-07): v2's
// line-cost-non-negative / line-items-sum-subtotal rows must carry the
// EXACT type/params/message the line_rules migration defines (copied, not
// re-declared -- [v2-copy-not-redeclare]).
func TestRuleSetV2_LineItemRuleParamsMatchLineRulesMigration(t *testing.T) {
	_, app := dbTestPools(t)
	ctx := context.Background()

	activeID, activeVersion := activeVersionRow(t, app)
	if activeVersion != 2 {
		t.Fatalf("active rule_set_versions.version = %d, want 2 [RS-V2-07 precondition]", activeVersion)
	}

	cases := []struct {
		key, wantType, wantParams, wantMessage string
	}{
		{
			key: "line-cost-non-negative", wantType: "cel",
			wantParams:  `{"expr":"!has(invoice.line_items) || invoice.line_items.all(x, !has(x.unit_price) || type(x.unit_price) != double || x.unit_price >= 0.0)"}`,
			wantMessage: "Line item cost must be zero or positive.",
		},
		{
			key: "line-items-sum-subtotal", wantType: "line_sum",
			wantParams:  `{"items":"line_items","amount":"unit_price","quantity":"quantity","expected":"subtotal","tolerance":0.005}`,
			wantMessage: "Line item amounts must sum to the invoice subtotal.",
		},
	}
	for _, tc := range cases {
		t.Run(tc.key, func(t *testing.T) {
			var gotType string
			var gotParams []byte
			var gotMessage string
			if err := app.QueryRow(ctx,
				`SELECT type, params, message FROM rules WHERE rule_set_version_id = $1 AND key = $2`,
				activeID, tc.key,
			).Scan(&gotType, &gotParams, &gotMessage); err != nil {
				t.Fatalf("read v2's %s row: %v -- expected the line_rules migration's params copied verbatim [RS-V2-07]", tc.key, err)
			}
			if gotType != tc.wantType {
				t.Errorf("%s.type = %q, want %q", tc.key, gotType, tc.wantType)
			}
			if gotMessage != tc.wantMessage {
				t.Errorf("%s.message = %q, want %q", tc.key, gotMessage, tc.wantMessage)
			}

			var gotParsed, wantParsed map[string]any
			if err := json.Unmarshal(gotParams, &gotParsed); err != nil {
				t.Fatalf("unmarshal v2's %s params %s: %v", tc.key, gotParams, err)
			}
			if err := json.Unmarshal([]byte(tc.wantParams), &wantParsed); err != nil {
				t.Fatalf("unmarshal expected %s params: %v", tc.key, err)
			}
			if !reflect.DeepEqual(gotParsed, wantParsed) {
				t.Errorf("%s.params decoded = %v, want %v (byte-identical to migrations/20260715120000_line_rules.sql's) [RS-V2-07]",
					tc.key, gotParsed, wantParsed)
			}
		})
	}
}

// ---------------------------------------------------------------------
// RS-V2-09 -- the v2 migration's Down restores the exact pre-migration
// state (v1 active w/ 19 rules, v2 absent).
// ---------------------------------------------------------------------

// TestRuleSetV2_DownRestoresV1 (RS-V2-09): mirrors seed_test.go's
// TestSeed_ReversibilityRollback pattern -- runs the v2 migration's Down
// (not yet authored) inside a superuser tx that is ALWAYS rolled back, so it
// never permanently mutates the shared DB other tests in this package
// depend on. Guards against a vacuous pass exactly like that test does:
// first asserts an active version=2 row (with 19 rules) actually exists --
// a loud, real RED today (there is no v2 yet) -- before attempting the Down.
func TestRuleSetV2_DownRestoresV1(t *testing.T) {
	super, _ := dbTestPools(t)
	ctx := context.Background()

	tx, err := super.Begin(ctx)
	if err != nil {
		t.Fatalf("begin superuser tx: %v", err)
	}
	defer func() {
		_ = tx.Rollback(ctx) // always roll back -- proves the Down's effect without a lasting mutation.
	}()

	// M4-17: v1 and v2 are now SEALED, so this test's simulated Down (DELETE v2, then
	// re-INSERT the line-item rules under v1) would be rejected by the seal guards --
	// modeling the real `goose reset` ordering, where M4-17's own Down drops the lock (this
	// DISABLE) before this migration's own Down runs. DISABLE TRIGGER USER is transactional
	// (rolled back with tx) and disables only USER triggers, leaving the FK RI/cascade
	// triggers intact, so the "rules gone via ON DELETE CASCADE" post-condition below still
	// holds. Do NOT use `SET LOCAL session_replication_role = 'replica'` here -- that also
	// suppresses RI triggers, which would make the cascade assertion pass for the wrong
	// reason. Both tables: rule_set_versions for the sealed-version DELETE (Guard C), rules
	// for the re-INSERT into sealed v1 (Guard A).
	if _, err := tx.Exec(ctx, `ALTER TABLE rule_set_versions DISABLE TRIGGER USER`); err != nil {
		t.Fatalf("disable USER triggers on rule_set_versions: %v", err)
	}
	if _, err := tx.Exec(ctx, `ALTER TABLE rules DISABLE TRIGGER USER`); err != nil {
		t.Fatalf("disable USER triggers on rules: %v", err)
	}

	var v2ID string
	if err := tx.QueryRow(ctx, `SELECT id FROM rule_set_versions WHERE version = 2 AND is_active`).Scan(&v2ID); err != nil {
		t.Fatalf("read the active version=2 row: %v -- expected the v2 migration's active row, got none "+
			"(has the v2 migration been applied via `make migrate-up`?) [RS-V2-09 precondition]", err)
	}
	var v2RuleCount int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM rules WHERE rule_set_version_id = $1`, v2ID).Scan(&v2RuleCount); err != nil {
		t.Fatalf("count v2 rules: %v", err)
	}
	if v2RuleCount != 19 {
		t.Fatalf("count(rules under v2) = %d, want 19 before running Down [RS-V2-09 precondition]", v2RuleCount)
	}

	// The v2 migration's Down, per task-111 §a: delete v2 (rules cascade,
	// ON DELETE CASCADE) -> reactivate v1 -> re-insert the 2 line-item rules
	// under v1, params verbatim from migrations/20260715120000_line_rules.sql.
	if _, err := tx.Exec(ctx, `DELETE FROM rule_set_versions WHERE version = 2`); err != nil {
		t.Fatalf("Down step 1 (delete v2): %v", err)
	}
	if _, err := tx.Exec(ctx, `UPDATE rule_set_versions SET is_active = true WHERE version = 1`); err != nil {
		t.Fatalf("Down step 2 (reactivate v1): %v", err)
	}
	var v1ID string
	if err := tx.QueryRow(ctx, `SELECT id FROM rule_set_versions WHERE version = 1`).Scan(&v1ID); err != nil {
		t.Fatalf("read v1 id: %v", err)
	}
	if _, err := tx.Exec(ctx,
		`INSERT INTO rules (rule_set_version_id, key, type, target, params, severity, "when", message, scope, enabled) VALUES
		 ($1, 'line-cost-non-negative', 'cel', '', '{"expr":"!has(invoice.line_items) || invoice.line_items.all(x, !has(x.unit_price) || type(x.unit_price) != double || x.unit_price >= 0.0)"}'::jsonb, 'error', NULL, 'Line item cost must be zero or positive.', 'document', true),
		 ($1, 'line-items-sum-subtotal', 'line_sum', '', '{"items":"line_items","amount":"unit_price","quantity":"quantity","expected":"subtotal","tolerance":0.005}'::jsonb, 'error', NULL, 'Line item amounts must sum to the invoice subtotal.', 'document', true)`,
		v1ID,
	); err != nil {
		t.Fatalf("Down step 3 (re-insert line-item rules under v1): %v", err)
	}

	var v1Active bool
	if err := tx.QueryRow(ctx, `SELECT is_active FROM rule_set_versions WHERE version = 1`).Scan(&v1Active); err != nil {
		t.Fatalf("read v1.is_active after Down: %v", err)
	}
	if !v1Active {
		t.Error("v1.is_active after Down = false, want true [RS-V2-09]")
	}

	var v1RuleCount int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM rules WHERE rule_set_version_id = $1`, v1ID).Scan(&v1RuleCount); err != nil {
		t.Fatalf("count v1 rules after Down: %v", err)
	}
	if v1RuleCount != 19 {
		t.Errorf("count(rules under v1) after Down = %d, want 19 (the exact pre-migration state) [RS-V2-09]", v1RuleCount)
	}

	var v2StillExists bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM rule_set_versions WHERE version = 2)`).Scan(&v2StillExists); err != nil {
		t.Fatalf("check v2 existence after Down: %v", err)
	}
	if v2StillExists {
		t.Error("rule_set_versions WHERE version=2 still exists after Down, want absent [RS-V2-09]")
	}
}

// ---------------------------------------------------------------------
// RS-V2-10, 12, 13 -- Category-A fixtures must restore the previously-active
// version by captured id, not by hardcoding `version = 1`.
// ---------------------------------------------------------------------

// simulateActiveVersion deactivates whichever rule_set_versions row is
// currently active (today, that is the real migration-seeded v1) and
// activates a fresh, disposable seedVersion(...,false) fixture row instead
// -- using only existing package fixtures + plain SQL, no schema change --
// so "the sanctioned active version" and "version = 1" diverge for the rest
// of THIS test, the same way they diverge for real once v2 ships. Registers
// a t.Cleanup that unconditionally restores the PREVIOUSLY-ACTIVE row --
// captured by id on entry -- as the sole active row at the end of the test
// (pass or fail, deactivating whatever is active FIRST so the restore itself
// can never collide with rule_set_versions_one_active), so no other test in
// the package ever observes the simulated active row once this test returns.
//
// EXECUTOR NOTE (M4-04-01 Stage 3): this cleanup originally hardcoded
// `WHERE version = 1`, correct only while v1 was the sanctioned active
// version. Post-v2-publish it set v1 active and left v2 inactive -- measured:
// a full suite run left the shared dev DB at `v1|is_active=t, v2|is_active=f`,
// i.e. the suite RE-CREATED the exact live data-integrity defect this story
// exists to fix, and knocked out TestSeed_ActiveVersionLoads /
// TestSeed_DemoContract / TestSeed_CollectAllOrdering (this file sorts before
// seed_test.go, so they ran against the corrupted active version). The helper
// whose own tests assert "restore by captured id, never hardcode version = 1"
// was itself hardcoding version = 1. Fixed by applying this task's governing
// rule to it. No assertion in this file was changed.
//
// WHY this is necessary: pre-migration there is only ever one real active
// version (v1, literally version 1), so any fixture/cleanup that hardcodes
// `version = 1` happens to restore the right row by pure coincidence -- see
// this file's TestRuleSetV2_* callers' doc comments. This helper breaks
// that coincidence on purpose, inside a disposable fixture, to make the
// hardcode-vs-discover-by-id bug class (RS-V2-10/11/12/13) OBSERVABLE before
// the real v2 migration exists.
func simulateActiveVersion(t *testing.T, super *pgxpool.Pool) (id string, version int) {
	t.Helper()
	id, version = seedVersion(t, super, false)
	sealAndActivate(t, super, id)
	return id, version
}

// activeVersionRow reads the sole active rule_set_versions row's id+version.
func activeVersionRow(t *testing.T, app *pgxpool.Pool) (id string, version int) {
	t.Helper()
	if err := app.QueryRow(context.Background(),
		`SELECT id, version FROM rule_set_versions WHERE is_active`,
	).Scan(&id, &version); err != nil {
		t.Fatalf("read the active rule_set_versions row: %v", err)
	}
	return id, version
}

// ruleKeysUnder returns the sorted rule keys under versionID.
func ruleKeysUnder(t *testing.T, pool *pgxpool.Pool, versionID string) []string {
	t.Helper()
	ctx := context.Background()
	rows, err := pool.Query(ctx, `SELECT key FROM rules WHERE rule_set_version_id = $1`, versionID)
	if err != nil {
		t.Fatalf("query rules under version id %s: %v", versionID, err)
	}
	defer rows.Close()
	var keys []string
	for rows.Next() {
		var key string
		if err := rows.Scan(&key); err != nil {
			t.Fatalf("scan key: %v", err)
		}
		keys = append(keys, key)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate rules: %v", err)
	}
	sort.Strings(keys)
	return keys
}

// TestRuleSetV2_SeedVersionRestoresPreviousActiveByID (RS-V2-10): a nested
// seedVersion(t, super, true) call (schema_test.go) -- the SAME fixture
// every DB-backed test in this package that needs an active row uses --
// must restore the row that was ACTIVE BEFORE it ran, identified by id, not
// by hardcoding `WHERE version = 1` (schema_test.go:107). Uses the REAL
// seedVersion function (unmodified): this test's greenness therefore tracks
// schema_test.go's actual fix, not a frozen copy of today's behavior.
func TestRuleSetV2_SeedVersionRestoresPreviousActiveByID(t *testing.T) {
	super, _ := dbTestPools(t)
	ctx := context.Background()

	baselineID, baselineVersion := simulateActiveVersion(t, super)

	t.Run("nested_seedVersion_active", func(t *testing.T) {
		seedVersion(t, super, true)
	})

	var gotID string
	if err := super.QueryRow(ctx, `SELECT id FROM rule_set_versions WHERE is_active`).Scan(&gotID); err != nil {
		t.Fatalf("read the active version id after the nested seedVersion(active) fixture's cleanup: %v", err)
	}
	if gotID != baselineID {
		t.Errorf("active version id after seedVersion(active)'s cleanup = %s, want %s (version=%d, the row that "+
			"was active BEFORE the nested fixture ran) -- seedVersion's cleanup (schema_test.go) must restore by "+
			"captured id, not hardcode `WHERE version = 1` (which silently reactivates the wrong row once the "+
			"sanctioned active version is not literally version 1) [RS-V2-10]", gotID, baselineID, baselineVersion)
	}
}

// TestRuleSetV2_LoadNoActiveErrorsRestoresPreviousActiveByID (RS-V2-12):
// store_test.go's TestStore_LoadNoActiveErrors deactivates the active
// version to exercise ErrNoActiveRuleSet, then restores it in cleanup --
// today hardcoded `WHERE version = 1` (store_test.go:228), with a
// leaked-fixture guard also hardcoded to `version <> 1` (store_test.go:234).
// Invokes the REAL test function (unmodified) as a subtest so this test's
// greenness tracks store_test.go's actual fix. NOTE: because that guard is
// ALSO a Category-A hardcode of the identical assumption, this subtest will
// keep failing at ITS OWN t.Fatalf until Stage 3 generalizes both sites
// together (the governing fix rule applies uniformly) -- that coupling is
// intentional, not a bug in this test.
func TestRuleSetV2_LoadNoActiveErrorsRestoresPreviousActiveByID(t *testing.T) {
	super, _ := dbTestPools(t)
	ctx := context.Background()

	baselineID, baselineVersion := simulateActiveVersion(t, super)

	t.Run("as_TestStore_LoadNoActiveErrors", TestStore_LoadNoActiveErrors)

	var gotID string
	if err := super.QueryRow(ctx, `SELECT id FROM rule_set_versions WHERE is_active`).Scan(&gotID); err != nil {
		t.Fatalf("read the active version id after TestStore_LoadNoActiveErrors's cleanup: %v", err)
	}
	if gotID != baselineID {
		t.Errorf("active version id after TestStore_LoadNoActiveErrors's cleanup = %s, want %s (version=%d) -- "+
			"its restore (store_test.go) must target the row it actually deactivated, not hardcode "+
			"`WHERE version = 1` [RS-V2-12]", gotID, baselineID, baselineVersion)
	}
}

// TestRuleSetV2_ReversibilityRollbackPostConditionSurvivesV2 (RS-V2-13):
// seed_test.go's TestSeed_ReversibilityRollback deletes v1 (within an
// always-rolled-back superuser tx) and today asserts the GLOBAL active
// count drops to 0 (seed_test.go:648-654) -- true only because v1 is the
// sole real active version today. Simulates the post-v2-publish topology
// (v1 exists but inactive; a different, disposable version is the real
// active one) and invokes the REAL test function (unmodified) as a subtest,
// so this test's greenness tracks seed_test.go's actual fix rather than a
// re-implementation of it.
func TestRuleSetV2_ReversibilityRollbackPostConditionSurvivesV2(t *testing.T) {
	super, _ := dbTestPools(t)

	simulateActiveVersion(t, super) // v1 now exists but inactive; a disposable fixture is active instead.

	t.Run("as_TestSeed_ReversibilityRollback", TestSeed_ReversibilityRollback)
}

// ---------------------------------------------------------------------
// RS-V2-11 -- the kill-switch cleanup hazard.
// ---------------------------------------------------------------------

// TestRuleSetV2_KillSwitchCleanupTargetsActiveVersion (RS-V2-11): proves the
// live-data hazard TestSeed_KillSwitch's cleanup (seed_test.go:566-574) has
// today -- its restore statement hardcodes `WHERE v.version = 1`, but
// store.go's ToggleRule (the real production code the cleanup is undoing)
// acts on `WHERE is_active` (store.go:137-139). Once the sanctioned active
// version is not literally version 1 (post-v2-publish), the cleanup
// silently restores the WRONG row, leaving the kill-switched rule disabled
// on the live active rule-set for every subsequent test (QA Debate Log F2).
//
// CAVEAT (flagged, not silently resolved): TestSeed_KillSwitch's cleanup is
// an inline anonymous func, not an extracted, independently-callable
// helper, and it cannot be invoked directly here without also going through
// seed_test.go's loadV1 (a DIFFERENT, orthogonal trap -- RS-V2-15) which
// would fail this subtest permanently regardless of whether THIS bug is
// fixed (loadActive's own version pin does not tolerate a simulated,
// non-canonical active version either). So this test reproduces the
// disable step via the REAL store.ToggleRule (production code) but mirrors
// the cleanup's CURRENT SQL verbatim as a pinned copy, rather than invoking
// seed_test.go's real closure. When Stage 3 changes seed_test.go:568-570's
// predicate from `v.version = 1` to `v.is_active` (task-111's explicit
// governing fix rule), THIS copy must be updated in lockstep -- it will not
// self-update from that file's change alone.
func TestRuleSetV2_KillSwitchCleanupTargetsActiveVersion(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	// M4-18: cannot use simulateActiveVersion here -- it now seals+activates immediately
	// (zero rules), and Guard A (M4-17) forbids inserting rules into an
	// already-sealed parent. Inline the same lawful publish order the 11 store_test.go
	// sites use instead: seed unsealed+inactive, insert the rule, THEN seal+activate.
	baselineID, _ := seedVersion(t, super, false)
	seedFullRule(t, super, baselineID, ruleFixture{Key: "vat-standard-rate", Enabled: true})
	sealAndActivate(t, super, baselineID)

	store := NewStore(app)
	if _, err := store.ToggleRule(newTestIdentity(), "vat-standard-rate", false); err != nil {
		t.Fatalf("ToggleRule(vat-standard-rate, false) on the simulated active version: %v", err)
	}

	// seed_test.go's TestSeed_KillSwitch cleanup statement, run here verbatim
	// (see this test's doc comment for why it is pinned rather than invoked).
	// UPDATED IN LOCKSTEP by M4-04-01 Stage 3 when the real cleanup's predicate
	// changed from `v.version = 1` to `v.is_active` -- the fix this test was
	// authored to demand. The doc comment above called for exactly this update;
	// without it the copy rots and this test asserts against SQL that no longer
	// exists anywhere.
	if _, err := super.Exec(ctx,
		`UPDATE rules r SET enabled = true
		   FROM rule_set_versions v
		  WHERE r.rule_set_version_id = v.id AND v.is_active AND r.key = 'vat-standard-rate'`,
	); err != nil {
		t.Fatalf("run TestSeed_KillSwitch's cleanup statement: %v", err)
	}

	var enabled bool
	if err := super.QueryRow(ctx,
		`SELECT r.enabled FROM rules r JOIN rule_set_versions v ON v.id = r.rule_set_version_id
		 WHERE v.is_active AND r.key = 'vat-standard-rate'`,
	).Scan(&enabled); err != nil {
		t.Fatalf("read vat-standard-rate.enabled on the active version: %v", err)
	}
	if !enabled {
		t.Error("vat-standard-rate.enabled = false on the ACTIVE version after running TestSeed_KillSwitch's " +
			"cleanup statement -- the cleanup hardcodes `WHERE v.version = 1`, so once the sanctioned active " +
			"version is not literally version 1 (post-v2-publish), it silently leaves the rule disabled on the " +
			"LIVE active rule-set [RS-V2-11, QA Debate Log F2]")
	}
}

// ---------------------------------------------------------------------
// RS-V2-14 (partial) -- the corrected detection command's own scope +
// count, NOT the Category-A/B triage (a disclosed judgment call).
// ---------------------------------------------------------------------

// TestRuleSetV2_DetectionCommandBaseline (RS-V2-14, partial): runs
// task-111 §b's corrected detection command VERBATIM (character-for-
// character; see that section for why a re-typed regex re-arms the trap)
// and asserts its one MECHANICALLY-checkable property: every hit lives inside
// internal/validation/**, one of the two named §c e2e artifacts,
// validationApi.test.ts, the seed migrations, or pnpm-lock.yaml (the plan's own
// "no Category-A hit exists outside this scope" claim).
//
// EXECUTOR NOTE (M4-04-01 Stage 3): this test originally also asserted
// `wantCount == 90`. That assertion was REMOVED, for two reasons, and the
// removal is called out in the PR for reviewer sign-off rather than done
// silently:
//  1. It contradicts the AC it implements. task-111 AC#6 states verbatim: "No
//     count asserted -- the command is the check", and the plan says "RS-V2-14
//     is a triage rule, not a zero-hits check."
//  2. Its stated rationale ("the scope/count are properties of the DETECTION
//     COMMAND, not of the fixture fix") is false: the count is a property of the
//     command applied to repo CONTENT, and changing that content is precisely
//     what Stage 3 does. 90 was a pre-fix snapshot; any post-fix number would be
//     one too -- and re-baselining it to whatever this diff happens to produce
//     would make the test record the implementation rather than check it.
//
// The measured post-fix count is reported in the PR body as EVIDENCE, not as an
// assertion. The scope check below -- which is AC-aligned and does not rot -- is
// untouched, as is this test's name.
//
// What this test deliberately does NOT do: classify hits into Category A
// (must-fix) vs Category B (four named benign shapes). That triage is an
// explicitly DISCLOSED judgment call (task-111's "Judgment residual"
// section; QA Debate Log F6) -- encoding the benign shapes as grep
// exclusions would reintroduce exactly the blindness three rounds of
// debate eliminated ("RS-V2-14 is a triage rule, not a zero-hits check").
// So RS-V2-14's own Then clause ("every hit is either fixed per the
// governing rule or matches a named Category-B shape") is NOT automated
// here -- it remains a human read at PR review, per the plan's own design.
// This is flagged in the QA report rather than silently reinterpreted as an
// automatable zero-hits check.
//
// This test is expected to PASS both before and after Stage 3's fix (the
// scope/count are properties of the DETECTION COMMAND, not of the fixture
// fix) -- it is a baseline/regression guard, not a red-to-green spec.
func TestRuleSetV2_DetectionCommandBaseline(t *testing.T) {
	root := repoRoot(t)
	cmd := exec.Command("bash", "-c",
		`grep -rnE '[Vv]ersion[[:space:]]*(:|==|!=|<>|=)[[:space:]]*1\b|[Vv]ersion\)?[[:space:]]*\.toBe\(1\)|loadV1' . --exclude-dir=node_modules --exclude-dir=.git --exclude-dir=vendor --exclude-dir=playwright-report`)
	cmd.Dir = root
	out, runErr := cmd.Output()
	if runErr != nil {
		if _, ok := runErr.(*exec.ExitError); !ok {
			t.Fatalf("run the detection command: %v", runErr)
		}
		// grep exits 1 when it finds nothing -- not itself a Go-level
		// error; fall through and let the count assertion below report it.
	}

	trimmed := strings.TrimRight(string(out), "\n")
	var allLines []string
	if trimmed != "" {
		allLines = strings.Split(trimmed, "\n")
	}

	// THIS file (rule_set_v2_test.go) necessarily reproduces the `version =
	// 1` pattern itself -- as prose in doc comments, as a pinned literal
	// copy of today's Category-A bug (RS-V2-11's cleanup mirror), and as
	// legitimate references to the PERMANENT historical v1 row (RS-V2-09's
	// Down mirror -- "v1" will always be version=1, by definition, forever;
	// that is not the bug). It is QA scaffolding, not part of the reviewed
	// 90-hit baseline the architecture verified live against the repo --
	// excluded from the scope check below by NAME, the same way the command's
	// own --exclude-dir flags already carve out non-reviewed directories.
	// This filters the OUTPUT for the assertion only; the command string
	// above stays byte-for-byte verbatim.
	const selfFile = "internal/validation/rule_set_v2_test.go"
	var lines []string
	for _, line := range allLines {
		file, _, ok := strings.Cut(line, ":")
		if ok && strings.TrimPrefix(file, "./") == selfFile {
			continue
		}
		lines = append(lines, line)
	}

	if len(lines) == 0 {
		t.Fatal("detection command returned no hits at all -- it is supposed to be deliberately broad and " +
			"noisy (the v1-defining seed migrations and the in-memory RuleSet{Version:1} unit fixtures are " +
			"permanent Category-B hits). Zero output means the COMMAND broke, not that the repo is clean [RS-V2-14 scope]")
	}

	for _, line := range lines {
		file, _, ok := strings.Cut(line, ":")
		if !ok {
			t.Errorf("detection command output line has no path: %q", line)
			continue
		}
		file = strings.TrimPrefix(file, "./")
		allowed := strings.HasPrefix(file, "internal/validation/") ||
			file == "e2e/api/validation.spec.ts" ||
			file == "e2e/topology/targets.ts" ||
			file == "frontend/app/src/lib/validationApi.test.ts" ||
			file == "migrations/20260711121327_seed_mbs_v1.sql" ||
			file == "migrations/20260715120000_line_rules.sql" ||
			// M4-04-01's own migration: Category B by the same rule as the two
			// above. Its `version = 1` statements DEFINE the v1->v2 topology --
			// deleting v1's two wrongly-added rules, deactivating v1, copying
			// v1's 17 into v2, and (in the Down) reactivating v1. It is the
			// authority that SETS which version is active; it never reads the
			// active version and pins the result to 1. v1 is version 1
			// permanently, by definition.
			file == "migrations/20260716185106_rule_set_v2.sql" ||
			file == "pnpm-lock.yaml"
		if !allowed {
			t.Errorf("detection command hit in an unexpected location: %q -- expected only "+
				"internal/validation/**, the two §c e2e artifacts, validationApi.test.ts, the "+
				"version-defining seed migrations, or pnpm-lock.yaml [RS-V2-14 scope]", line)
		}
	}
}

// repoRoot resolves the git worktree root so TestRuleSetV2_DetectionCommandBaseline
// can run the detection command from the right place regardless of `go test`'s
// working directory (the package dir).
func repoRoot(t *testing.T) string {
	t.Helper()
	out, err := exec.Command("git", "rev-parse", "--show-toplevel").Output()
	if err != nil {
		t.Fatalf("git rev-parse --show-toplevel: %v", err)
	}
	return strings.TrimSpace(string(out))
}

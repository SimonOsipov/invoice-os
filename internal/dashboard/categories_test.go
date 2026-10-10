package dashboard

import (
	"context"
	"sort"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

// fetchDatedRuleKeys is the guard's oracle: the keys of every dated version
// (v4, v5) read from the DB, never a hardcoded list.
func fetchDatedRuleKeys(t *testing.T, app *pgxpool.Pool) []string {
	t.Helper()
	rows, err := app.Query(context.Background(),
		`SELECT DISTINCT r.key FROM rules r JOIN rule_set_versions v ON v.id = r.rule_set_version_id WHERE v.effective_from IS NOT NULL ORDER BY r.key`)
	if err != nil {
		t.Fatalf("fetch dated rule keys: %v", err)
	}
	defer rows.Close()

	var keys []string
	for rows.Next() {
		var key string
		if err := rows.Scan(&key); err != nil {
			t.Fatalf("scan rule key: %v", err)
		}
		keys = append(keys, key)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate rule keys: %v", err)
	}
	if len(keys) == 0 {
		t.Fatal("no dated rule keys -- is the DB seeded?")
	}
	return keys
}

// fetchActiveRuleKeys reads the version in force today; MCAT-06 only.
func fetchActiveRuleKeys(t *testing.T, app *pgxpool.Pool) []string {
	t.Helper()
	rows, err := app.Query(context.Background(),
		`SELECT r.key FROM rules r JOIN rule_set_versions v ON v.id = r.rule_set_version_id WHERE v.id = rule_set_version_for((now() AT TIME ZONE 'UTC')::date)`)
	if err != nil {
		t.Fatalf("fetch active rule keys: %v", err)
	}
	defer rows.Close()

	var keys []string
	for rows.Next() {
		var key string
		if err := rows.Scan(&key); err != nil {
			t.Fatalf("scan rule key: %v", err)
		}
		keys = append(keys, key)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate rule keys: %v", err)
	}
	if len(keys) == 0 {
		t.Fatal("active rule set is empty -- is the DB seeded?")
	}
	return keys
}

// MCAT-01: every rule key of every dated version has an entry in
// ruleCategories. Fails when a published rule has no bar.
func TestCategories_EveryActiveRuleIsMapped(t *testing.T) {
	_, app := dbTestPools(t)
	active := fetchDatedRuleKeys(t, app)
	if len(active) == 0 {
		t.Fatal("no active rules read back -- the loop below would assert nothing")
	}

	for _, key := range active {
		if _, ok := ruleCategories[key]; !ok {
			t.Errorf("dated rule %q has no entry in ruleCategories", key)
		}
	}
}

// MCAT-02: every key in ruleCategories exists in a dated version. Fails
// when a renamed/dropped rule leaves a dead key that can never fire.
func TestCategories_EveryMappedKeyIsActive(t *testing.T) {
	_, app := dbTestPools(t)
	active := fetchDatedRuleKeys(t, app)

	activeSet := make(map[string]bool, len(active))
	for _, key := range active {
		activeSet[key] = true
	}

	for key := range ruleCategories {
		if !activeSet[key] {
			t.Errorf("ruleCategories has key %q, not present in any dated version", key)
		}
	}
}

// MCAT-03: the three categories partition the dated keys 37/11/19. Fails when
// a rule is quietly re-bucketed, moving a bar without a story.
func TestCategories_PartitionSizesMatchSpec(t *testing.T) {
	_, app := dbTestPools(t)
	active := fetchDatedRuleKeys(t, app)

	counts := map[Category]int{}
	for _, key := range active {
		counts[ruleCategories[key]]++
	}

	want := map[Category]int{
		CategoryFieldCompleteness: 37,
		CategoryTaxAccuracy:       11,
		CategoryIdentifiers:       19,
	}
	for cat, wantCount := range want {
		if got := counts[cat]; got != wantCount {
			t.Errorf("category %q has %d dated rules, want %d", cat, got, wantCount)
		}
	}
	if total := len(active); total != 67 {
		t.Fatalf("dated rule keys: %d, want 67 (test's own oracle is stale)", total)
	}
}

// MCAT-04: categoryKeys results are pairwise disjoint and cover the map.
// Fails when a key lands in two categories, double-counting a bar.
func TestCategories_KeysDisjointAndCoverTheMap(t *testing.T) {
	fc := categoryKeys(CategoryFieldCompleteness)
	ta := categoryKeys(CategoryTaxAccuracy)
	id := categoryKeys(CategoryIdentifiers)

	seen := map[string]Category{}
	for _, cat := range []Category{CategoryFieldCompleteness, CategoryTaxAccuracy, CategoryIdentifiers} {
		for _, key := range categoryKeys(cat) {
			if prior, ok := seen[key]; ok {
				t.Errorf("key %q appears in both %q and %q", key, prior, cat)
			}
			seen[key] = cat
		}
	}

	if got, want := len(fc)+len(ta)+len(id), len(ruleCategories); got != want {
		t.Errorf("sum of categoryKeys lengths = %d, want %d (len(ruleCategories))", got, want)
	}
	for key := range ruleCategories {
		if _, ok := seen[key]; !ok {
			t.Errorf("ruleCategories key %q is not covered by any categoryKeys() result", key)
		}
	}
}

// MCAT-05: categoryKeys is sorted ascending and stable across calls. Fails
// when SQL param order flaps, making query plans non-reproducible.
func TestCategories_KeysSortedAndStable(t *testing.T) {
	for _, cat := range []Category{CategoryFieldCompleteness, CategoryTaxAccuracy, CategoryIdentifiers} {
		first := categoryKeys(cat)
		second := categoryKeys(cat)

		if !sort.StringsAreSorted(first) {
			t.Errorf("categoryKeys(%q) = %v, not sorted ascending", cat, first)
		}
		if len(first) != len(second) {
			t.Fatalf("categoryKeys(%q) len differs across calls: %d then %d", cat, len(first), len(second))
		}
		for i := range first {
			if first[i] != second[i] {
				t.Errorf("categoryKeys(%q) not stable across calls: [%d] = %q then %q", cat, i, first[i], second[i])
			}
		}
	}
}

// MCAT-06: the guard survives a rule's enabled flip to false -- legal
// since rules_content_lock() excludes `enabled` from its sealed check.
func TestCategories_GuardSurvivesEnabledFlip(t *testing.T) {
	super, app := dbTestPools(t)
	active := fetchActiveRuleKeys(t, app)
	target := active[0]

	// Same key exists in several versions -- scope by the active row's id,
	// or the flip leaks into sealed ones.
	var ruleID string
	var prevEnabled bool
	if err := app.QueryRow(context.Background(),
		`SELECT r.id, r.enabled FROM rules r
		   JOIN rule_set_versions v ON v.id = r.rule_set_version_id
		  WHERE v.id = rule_set_version_for((now() AT TIME ZONE 'UTC')::date) AND r.key = $1`, target).Scan(&ruleID, &prevEnabled); err != nil {
		t.Fatalf("locate active rule %q: %v", target, err)
	}

	// invoice_app cannot write rules; flip as the owner/superuser.
	if _, err := super.Exec(context.Background(),
		`UPDATE rules SET enabled = false WHERE id = $1`, ruleID); err != nil {
		t.Fatalf("flip enabled=false on %q: %v", target, err)
	}
	t.Cleanup(func() {
		if _, err := super.Exec(context.Background(),
			`UPDATE rules SET enabled = $1 WHERE id = $2`, prevEnabled, ruleID); err != nil {
			t.Fatalf("restore enabled=%v on %q: %v", prevEnabled, target, err)
		}
	})

	afterFlip := fetchActiveRuleKeys(t, app)
	afterSet := make(map[string]bool, len(afterFlip))
	for _, key := range afterFlip {
		afterSet[key] = true
	}
	if !afterSet[target] {
		t.Fatalf("active rule set no longer contains %q after enabled=false -- the guard query must not filter on enabled", target)
	}

	for _, key := range afterFlip {
		if _, ok := ruleCategories[key]; !ok {
			t.Errorf("active rule %q has no entry in ruleCategories after the enabled flip", key)
		}
	}
}

// TestCategoryKeys_V5KeysFollowTheRule pins one key per D23 rule branch.
func TestCategoryKeys_V5KeysFollowTheRule(t *testing.T) {
	want := map[Category][]string{
		CategoryFieldCompleteness: {"supplier-email-required"},
		CategoryIdentifiers:       {"currency-allowed", "buyer-email-length"},
		CategoryTaxAccuracy:       {"vat-standard-rate-uncategorised"},
	}
	for cat, keys := range want {
		got := map[string]bool{}
		for _, k := range categoryKeys(cat) {
			got[k] = true
		}
		for _, k := range keys {
			if !got[k] {
				t.Errorf("categoryKeys(%q) lacks %q", cat, k)
			}
		}
	}
}

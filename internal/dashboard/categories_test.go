package dashboard

import (
	"context"
	"sort"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

// fetchActiveRuleKeys is the guard's own oracle: the active rule set read
// straight from the DB, never a hardcoded list.
func fetchActiveRuleKeys(t *testing.T, app *pgxpool.Pool) []string {
	t.Helper()
	rows, err := app.Query(context.Background(),
		`SELECT r.key FROM rules r JOIN rule_set_versions v ON v.id = r.rule_set_version_id WHERE v.is_active`)
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
		t.Fatal("active rule set is empty -- is the DB seeded with v4?")
	}
	return keys
}

// MCAT-01: every rule key of the active rule set has an entry in
// ruleCategories. Fails when a published rule has no bar.
func TestCategories_EveryActiveRuleIsMapped(t *testing.T) {
	_, app := dbTestPools(t)
	active := fetchActiveRuleKeys(t, app)

	for _, key := range active {
		if _, ok := ruleCategories[key]; !ok {
			t.Errorf("active rule %q has no entry in ruleCategories", key)
		}
	}
}

// MCAT-02: every key in ruleCategories exists in the active rule set. Fails
// when a renamed/dropped rule leaves a dead key that can never fire.
func TestCategories_EveryMappedKeyIsActive(t *testing.T) {
	_, app := dbTestPools(t)
	active := fetchActiveRuleKeys(t, app)

	activeSet := make(map[string]bool, len(active))
	for _, key := range active {
		activeSet[key] = true
	}

	for key := range ruleCategories {
		if !activeSet[key] {
			t.Errorf("ruleCategories has key %q, not present in the active rule set", key)
		}
	}
}

// MCAT-03: the three categories partition the active set 9/8/3. Fails when
// a rule is quietly re-bucketed, moving a bar without a story.
func TestCategories_PartitionSizesMatchSpec(t *testing.T) {
	_, app := dbTestPools(t)
	active := fetchActiveRuleKeys(t, app)

	counts := map[Category]int{}
	for _, key := range active {
		counts[ruleCategories[key]]++
	}

	want := map[Category]int{
		CategoryFieldCompleteness: 9,
		CategoryTaxAccuracy:       8,
		CategoryIdentifiers:       3,
	}
	for cat, wantCount := range want {
		if got := counts[cat]; got != wantCount {
			t.Errorf("category %q has %d active rules, want %d", cat, got, wantCount)
		}
	}
	if total := len(active); total != 20 {
		t.Fatalf("active rule set has %d keys, want 20 (test's own oracle is stale)", total)
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
	_, app := dbTestPools(t)
	active := fetchActiveRuleKeys(t, app)
	target := active[0]

	// Same key exists in every rule_set_versions row (v1..v4 each copy the
	// prior version) -- scope by the active row's id, not by key, or the
	// flip leaks into sealed historical versions.
	var ruleID string
	var prevEnabled bool
	if err := app.QueryRow(context.Background(),
		`SELECT r.id, r.enabled FROM rules r
		   JOIN rule_set_versions v ON v.id = r.rule_set_version_id
		  WHERE v.is_active AND r.key = $1`, target).Scan(&ruleID, &prevEnabled); err != nil {
		t.Fatalf("locate active rule %q: %v", target, err)
	}

	if _, err := app.Exec(context.Background(),
		`UPDATE rules SET enabled = false WHERE id = $1`, ruleID); err != nil {
		t.Fatalf("flip enabled=false on %q: %v", target, err)
	}
	t.Cleanup(func() {
		if _, err := app.Exec(context.Background(),
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
	for key := range ruleCategories {
		if !afterSet[key] {
			t.Errorf("ruleCategories key %q missing from the active set after the enabled flip", key)
		}
	}
}

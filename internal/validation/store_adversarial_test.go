// M4-04-03 (task-109) -- Stage 4 (QA Verify, Mode B) adversarial coverage,
// added on top of the executor's green VB-01..17 without modifying any of
// them. Closes the one gap Stage-1 addendum G3 named but VB-14 does not
// close by itself: G3's fix (loadActiveRuleSetTx's len(rules)==0 ->
// ErrEmptyRuleSet guard, store.go) is real, but nothing in the shipped VB
// suite actually DRIVES a real active-version-with-zero-rules DB state
// through either loader and asserts the guard fires. VB-14 only asserts
// against the migrated DB's real (non-empty) v2 rule-set; it would pass
// identically whether or not the guard existed. An untested guard against a
// silent fail-open is not a guard -- this file makes it one.
//
// Both loaders are covered, not just the global one: [tenant-free-ruleset-load]
// claimed "fails closed" for LoadActiveRuleSetGlobal, but store.go's actual
// refactor routes LoadActiveRuleSet through the SAME shared
// loadActiveRuleSetTx helper -- so the shipped, unchanged POST /v1/validate
// path (LoadActiveRuleSet) must be guarded too, or the ORIGINAL single-invoice
// endpoint still fails open even after this subtask ships.
//
// Fixture: seedVersion(t, super, true) (schema_test.go) with NO seedRule
// call -- a real, live rule_set_versions row, is_active=true, holding zero
// rules underneath it. This is exactly the state the Stage-1 addendum
// verified reachable live (RLS added to `rules` alone, or -- more mundanely
// today -- any operational mistake that leaves a published version's rules
// unseeded). No RLS is required to reach this state: it is a property of the
// `rules` table's row count under the active version, independent of how
// that count reached zero.
package validation

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"

	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
)

// TestStore_LoadActiveRuleSetGlobal_ZeroRulesFailsClosed (G3): a real active
// rule_set_versions row carrying ZERO rules must make LoadActiveRuleSetGlobal
// FAIL -- never succeed with an empty RuleSet. A caller that only checks
// `err == nil` before handing rs to Engine.Evaluate would otherwise get
// Violations == [] for every invoice, indistinguishable from "genuinely
// compliant": the worst failure mode available to a compliance gate.
func TestStore_LoadActiveRuleSetGlobal_ZeroRulesFailsClosed(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	// Zero rules is the point: no seedRule call follows.
	versionID, version := seedVersion(t, super, true)

	store := NewStore(app)
	rs, err := store.LoadActiveRuleSetGlobal(ctx)

	if err == nil {
		t.Fatalf("LoadActiveRuleSetGlobal against an active version (id=%s, version=%d) with ZERO rules "+
			"returned err=nil, rs=%+v -- this is the G3 silent fail-OPEN: Evaluate would find nothing to "+
			"check and every invoice would validate clean with HTTP 200", versionID, version, rs)
	}
	if !errors.Is(err, ErrNoActiveRuleSet) {
		t.Errorf("errors.Is(err, ErrNoActiveRuleSet) = false (err = %v) -- statusForErr keys off "+
			"ErrNoActiveRuleSet to answer 503; ErrEmptyRuleSet must WRAP it or this loader's failure "+
			"leaks through as an unmapped 500 instead of the documented 503", err)
	}
	if !errors.Is(err, ErrEmptyRuleSet) {
		t.Errorf("errors.Is(err, ErrEmptyRuleSet) = false (err = %v) -- the specific empty-rule-set "+
			"cause must still be discriminable from a genuinely-absent active row", err)
	}
	if rs.ID != "" || rs.Version != 0 || len(rs.Rules) != 0 {
		t.Errorf("rs = %+v, want the zero RuleSet on error -- a partially-populated RuleSet "+
			"(e.g. rs.Version/rs.ID set but Rules empty) risks a caller that checks err loosely "+
			"still finding a plausible-looking, evaluatable RuleSet", rs)
	}
}

// TestStore_LoadActiveRuleSet_ZeroRulesFailsClosed (G3, audit item 1b): the
// SAME zero-rules guard, but through LoadActiveRuleSet -- the tenant-wrapped
// loader that already shipped behind POST /v1/validate before this subtask,
// and that this subtask's refactor (loadActiveRuleSetTx) now routes through
// the identical shared code path. If this loader were NOT guarded, the
// original single-invoice playground endpoint would still fail open even
// after M4-04-03 ships, despite the story's own framing of G3 as fixed.
func TestStore_LoadActiveRuleSet_ZeroRulesFailsClosed(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	versionID, version := seedVersion(t, super, true) // zero rules

	store := NewStore(app)
	tenantID := uuid.NewString()
	c := auth.WithIdentity(ctx, auth.Identity{Subject: "user-1", Role: "authenticated", TenantID: tenantID})

	rs, err := store.LoadActiveRuleSet(c)

	if err == nil {
		t.Fatalf("LoadActiveRuleSet (identity-carrying, POST /v1/validate's path) against an active "+
			"version (id=%s, version=%d) with ZERO rules returned err=nil, rs=%+v -- the single-invoice "+
			"endpoint fails open identically to the batch path if this loader is unguarded", versionID, version, rs)
	}
	if !errors.Is(err, ErrNoActiveRuleSet) {
		t.Errorf("errors.Is(err, ErrNoActiveRuleSet) = false (err = %v) -- ValidateHandler's statusForErr "+
			"keys off ErrNoActiveRuleSet to answer 503", err)
	}
	if !errors.Is(err, ErrEmptyRuleSet) {
		t.Errorf("errors.Is(err, ErrEmptyRuleSet) = false (err = %v)", err)
	}
	if rs.ID != "" || rs.Version != 0 || len(rs.Rules) != 0 {
		t.Errorf("rs = %+v, want the zero RuleSet on error", rs)
	}
}

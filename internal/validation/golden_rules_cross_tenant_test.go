// Cross-tenant proof for the golden rules: tenant A cannot change tenant B's
// evaluation, at the handler (403) or at the grant (42501). `rules` is global, so these
// tests write the shared seeded rows when the grant is missing; each registers a
// superuser restore in t.Cleanup before its first write. No t.Parallel().
package validation

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
)

const (
	goldenVATKey      = "vat-standard-rate"
	goldenCurrencyKey = "currency-allowed"
)

// goldenTenant is one tenant's identity.
type goldenTenant struct {
	id  string
	ctx context.Context
}

// newGoldenTenant mirrors newTestIdentity (non-UUID subject, so the membership gate is
// skipped) with a fixed tenant.
func newGoldenTenant() goldenTenant {
	tenant := uuid.NewString()
	return goldenTenant{
		id: tenant,
		ctx: auth.WithIdentity(context.Background(), auth.Identity{
			Subject: "user-1", Role: "authenticated", TenantID: tenant,
		}),
	}
}

// bothRulesPayload trips vat-standard-rate (wrong VAT) and currency-allowed (USD).
func bothRulesPayload() Payload {
	p := validInvoicePayload()
	invoiceOf(p)["currency"] = "USD"
	invoiceOf(p)["vat"] = 70.0
	return p
}

// evalKeys evaluates a fresh bothRulesPayload against rs and returns the violation keys.
func evalKeys(t *testing.T, rs RuleSet) []string {
	t.Helper()
	res, err := NewDefaultEngine().Evaluate(bothRulesPayload(), rs)
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	return violationKeys(res)
}

// tenantEvaluation is a tenant's violation keys under both loaders.
type tenantEvaluation struct {
	identity []string // LoadActiveRuleSet under the tenant's identity
	global   []string // LoadActiveRuleSetGlobal, the path every invoice takes
}

func evaluationOf(t *testing.T, app *pgxpool.Pool, who goldenTenant) tenantEvaluation {
	t.Helper()
	store := NewStore(app)
	rsID, err := store.LoadActiveRuleSet(who.ctx)
	if err != nil {
		t.Fatalf("LoadActiveRuleSet(tenant %s): %v", who.id, err)
	}
	rsGlobal, err := store.LoadActiveRuleSetGlobal(context.Background())
	if err != nil {
		t.Fatalf("LoadActiveRuleSetGlobal: %v", err)
	}
	return tenantEvaluation{identity: evalKeys(t, rsID), global: evalKeys(t, rsGlobal)}
}

// activeRuleID reads the active version's rules.id for key.
func activeRuleID(t *testing.T, super *pgxpool.Pool, key string) string {
	t.Helper()
	var id string
	if err := super.QueryRow(context.Background(),
		`SELECT r.id::text FROM rules r JOIN rule_set_versions v ON v.id = r.rule_set_version_id
		 WHERE v.is_active AND r.key = $1`, key,
	).Scan(&id); err != nil {
		t.Fatalf("read active rules.id for %q: %v", key, err)
	}
	return id
}

// appToggle is tenant A's attack: invoice_app on a plain tx with A's GUC. It commits
// when the write is accepted, as a real attacker's would, so a missing grant shows in the
// next evaluation.
func appToggle(t *testing.T, app *pgxpool.Pool, tenant, ruleID string, enabled bool) error {
	t.Helper()
	ctx := context.Background()
	tx, err := app.Begin(ctx)
	if err != nil {
		t.Fatalf("begin app tx: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, fmt.Sprintf("SET LOCAL app.current_tenant = '%s'", tenant)); err != nil {
		t.Fatalf("SET LOCAL app.current_tenant: %v", err)
	}
	if _, err := tx.Exec(ctx, `UPDATE rules SET enabled = $1 WHERE id = $2`, enabled, ruleID); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// patchRefused asserts A's PATCH for key gets the uniform 403.
func patchRefused(t *testing.T, a goldenTenant, key, body string) {
	t.Helper()
	id, _ := auth.IdentityFromContext(a.ctx)
	assertRefusal(t, doToggle(t, &id, key, strings.NewReader(body)))
}

func containsKey(keys []string, key string) bool {
	for _, k := range keys {
		if k == key {
			return true
		}
	}
	return false
}

// assertSameEvaluation asserts got equals want under both loaders.
func assertSameEvaluation(t *testing.T, got, want tenantEvaluation, when string) {
	t.Helper()
	if !reflect.DeepEqual(got.identity, want.identity) {
		t.Errorf("%s: LoadActiveRuleSet keys = %v, want baseline %v", when, got.identity, want.identity)
	}
	if !reflect.DeepEqual(got.global, want.global) {
		t.Errorf("%s: LoadActiveRuleSetGlobal keys = %v, want baseline %v", when, got.global, want.global)
	}
}

func TestGoldenRules_TenantACannotChangeTenantBEvaluation(t *testing.T) {
	super, app := dbTestPools(t)
	restoreRulesOnCleanup(t, super)
	a, b := newGoldenTenant(), newGoldenTenant()
	keys := []string{goldenVATKey, goldenCurrencyKey}

	baseline := evaluationOf(t, app, b)
	for _, k := range keys {
		if !containsKey(baseline.identity, k) || !containsKey(baseline.global, k) {
			t.Fatalf("baseline %+v lacks %s: the payload must trip both rules", baseline, k)
		}
	}

	for _, k := range keys {
		patchRefused(t, a, k, `{"enabled":false}`)
	}
	for _, k := range keys {
		assertAppRefused(t, appToggle(t, app, a.id, activeRuleID(t, super, k), false), "UPDATE rules SET enabled = false ("+k+")")
	}
	for _, k := range keys {
		if !ruleEnabledActive(t, super, k) {
			t.Errorf("%s enabled = false after A's attempts, want true", k)
		}
	}

	after := evaluationOf(t, app, b)
	assertSameEvaluation(t, after, baseline, "after A's attempts")
	for _, k := range keys {
		if !containsKey(after.identity, k) || !containsKey(after.global, k) {
			t.Errorf("B's evaluation %+v lacks %s after A's attempts", after, k)
		}
	}
}

func TestGoldenRules_TenantACannotOverrideAStaffKillSwitch(t *testing.T) {
	super, app := dbTestPools(t)
	restoreRulesOnCleanup(t, super)
	a, b := newGoldenTenant(), newGoldenTenant()

	if n := runKillSwitch(t, super, goldenCurrencyKey, false); n != 1 {
		t.Fatalf("kill switch (%s, false) rows = %d, want 1", goldenCurrencyKey, n)
	}
	baseline := evaluationOf(t, app, b)
	if containsKey(baseline.identity, goldenCurrencyKey) || containsKey(baseline.global, goldenCurrencyKey) {
		t.Fatalf("baseline %+v still has %s after the kill switch", baseline, goldenCurrencyKey)
	}
	if !containsKey(baseline.identity, goldenVATKey) || !containsKey(baseline.global, goldenVATKey) {
		t.Fatalf("baseline %+v lacks %s: it would be vacuous", baseline, goldenVATKey)
	}

	patchRefused(t, a, goldenCurrencyKey, `{"enabled":true}`)
	assertAppRefused(t, appToggle(t, app, a.id, activeRuleID(t, super, goldenCurrencyKey), true),
		"UPDATE rules SET enabled = true ("+goldenCurrencyKey+")")

	if ruleEnabledActive(t, super, goldenCurrencyKey) {
		t.Errorf("%s enabled = true after A's attempts, want the staff decision (false)", goldenCurrencyKey)
	}
	assertSameEvaluation(t, evaluationOf(t, app, b), baseline, "after A's attempts")
}

// TestGoldenRules_TenantAKeepsItsOwnEvaluation: one global set, no per-tenant fork.
func TestGoldenRules_TenantAKeepsItsOwnEvaluation(t *testing.T) {
	super, app := dbTestPools(t)
	restoreRulesOnCleanup(t, super)
	a, b := newGoldenTenant(), newGoldenTenant()
	keys := []string{goldenVATKey, goldenCurrencyKey}

	baseline := evaluationOf(t, app, b)
	for _, k := range keys {
		assertAppRefused(t, appToggle(t, app, a.id, activeRuleID(t, super, k), false), "UPDATE rules SET enabled = false ("+k+")")
	}

	own := evaluationOf(t, app, a)
	assertSameEvaluation(t, own, baseline, "A's own evaluation after its refused attempts")
	for _, k := range keys {
		if !containsKey(own.identity, k) {
			t.Errorf("A's own evaluation %v lacks %s", own.identity, k)
		}
	}
}

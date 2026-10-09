// DB-backed tests for Store.LoadActiveRuleSet; the kill switch's effect is proved in kill_switch_e2e_test.go.
//
// Fixtures are seeded as the SUPERUSER (bypasses the app-role grant, which is
// SELECT-only — see schema_test.go's seedVersion/seedRule,
// reused here, plus this file's own seedFullRule for fixtures that need
// non-default field values).
//
// Fixtures are dated in year 3001+ (sealAndDate) so they never collide with the
// real versions; t.Cleanup deletes each fixture and no test runs in parallel.
//
// Run: `make dev-db` once, then with the per-role DSNs set directly (see
// dbTestPools in schema_test.go):
//
//	DATABASE_URL="postgres://invoice_app:app@localhost:5432/invoice_os?sslmode=disable" \
//	DATABASE_SUPERUSER_URL="postgres://postgres:postgres@localhost:5432/invoice_os?sslmode=disable" \
//	go test -count=1 -run 'TestStore_' ./internal/validation/...
package validation

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
	"github.com/SimonOsipov/invoice-os/internal/platform/db"
)

// ruleFixture is the full field set for seedFullRule below. Every field the
// zero value would otherwise leave ambiguous (Enabled, in particular — Go's
// bool zero value is false, which is NOT the table's `enabled` DEFAULT true)
// MUST be set explicitly by the caller; there is no "unset" sentinel here.
type ruleFixture struct {
	Key      string
	Type     string // defaults to "required" when empty
	Target   string
	Params   string // raw JSON text, e.g. `{}` or `{"min":1}`; defaults to "{}" when empty
	Severity string // defaults to "error" when empty
	When     *string
	Message  string // defaults to "qa fixture rule" when empty
	Scope    string // defaults to "document" when empty
	Enabled  bool
}

// seedFullRule inserts one rules row under versionID as the superuser, like
// schema_test.go's seedRule, but exposes every column so tests can assert
// field-mapping precisely. No cleanup of its own: it is
// always reachable from the seedVersion call that produced versionID, whose
// cleanup cascades onto this row (rules.rule_set_version_id is ON DELETE
// CASCADE — see schema_test.go's seedVersion doc comment).
func seedFullRule(t *testing.T, super *pgxpool.Pool, versionID string, f ruleFixture) (id string) {
	t.Helper()
	ctx := context.Background()
	if f.Type == "" {
		f.Type = "required"
	}
	if f.Severity == "" {
		f.Severity = "error"
	}
	if f.Message == "" {
		f.Message = "qa fixture rule"
	}
	if f.Scope == "" {
		f.Scope = "document"
	}
	if f.Params == "" {
		f.Params = "{}"
	}
	if err := super.QueryRow(ctx,
		`INSERT INTO rules (rule_set_version_id, key, type, target, params, severity, "when", message, scope, enabled)
		 VALUES ($1, $2, $3, $4, $5::jsonb, $6, $7, $8, $9, $10)
		 RETURNING id`,
		versionID, f.Key, f.Type, f.Target, f.Params, f.Severity, f.When, f.Message, f.Scope, f.Enabled,
	).Scan(&id); err != nil {
		t.Fatalf("seed full rule(key=%q): %v", f.Key, err)
	}
	return id
}

// TestStore_LoadActiveRuleSet (Test Spec #1): the active version's number and
// both its rules -- with every field (key/type/target/severity/message/scope
// /enabled, plus a structural check on params) -- must come back from
// LoadActiveRuleSet.
func TestStore_LoadActiveRuleSet(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	versionID, version := seedVersion(t, super, false)
	seedFullRule(t, super, versionID, ruleFixture{
		Key: "rule-a", Type: "range", Target: "invoice.total", Params: `{"min":0,"max":100}`,
		Severity: "warning", Message: "total out of range", Scope: "document", Enabled: true,
	})
	seedFullRule(t, super, versionID, ruleFixture{
		Key: "rule-b", Type: "required", Target: "supplier.tin", Params: `{}`,
		Severity: "error", Message: "TIN required", Scope: "document", Enabled: false,
	})
	sealAndDate(t, super, versionID, todayUTC())

	store := NewStore(app)
	tenantID := uuid.NewString()
	c := auth.WithIdentity(ctx, auth.Identity{Subject: "user-1", Role: "authenticated", TenantID: tenantID})

	rs, err := store.LoadActiveRuleSet(c)
	if err != nil {
		t.Fatalf("LoadActiveRuleSet: %v", err)
	}
	if rs.Version != version {
		t.Errorf("RuleSet.Version = %d, want %d", rs.Version, version)
	}
	if len(rs.Rules) != 2 {
		t.Fatalf("len(RuleSet.Rules) = %d, want 2", len(rs.Rules))
	}

	byKey := map[string]Rule{}
	for _, r := range rs.Rules {
		byKey[r.Key] = r
	}

	a, ok := byKey["rule-a"]
	if !ok {
		t.Fatal("rule-a not found in loaded RuleSet.Rules")
	}
	if a.Type != TypeRange || a.Target != "invoice.total" || a.Severity != Severity("warning") ||
		a.Message != "total out of range" || a.Scope != "document" || !a.Enabled {
		t.Errorf("rule-a = %+v, want type=range target=invoice.total severity=warning message=%q scope=document enabled=true",
			a, "total out of range")
	}
	var aParams map[string]any
	if err := json.Unmarshal(a.Params, &aParams); err != nil {
		t.Fatalf("unmarshal rule-a params %s: %v", a.Params, err)
	}
	if aParams["min"] != float64(0) || aParams["max"] != float64(100) {
		t.Errorf("rule-a params = %v, want min=0 max=100", aParams)
	}

	b, ok := byKey["rule-b"]
	if !ok {
		t.Fatal("rule-b not found in loaded RuleSet.Rules")
	}
	if b.Type != TypeRequired || b.Target != "supplier.tin" || b.Severity != Severity("error") ||
		b.Message != "TIN required" || b.Scope != "document" || b.Enabled {
		t.Errorf("rule-b = %+v, want type=required target=supplier.tin severity=error message=%q scope=document enabled=false",
			b, "TIN required")
	}
}

// dateRule inserts one rule under versionID as the superuser.
func dateRule(t *testing.T, super *pgxpool.Pool, versionID, key string) {
	t.Helper()
	seedFullRule(t, super, versionID, ruleFixture{Key: key, Enabled: true})
}

func dateFixture(t *testing.T, super *pgxpool.Pool, from, ruleKey string) (id string, version int) {
	t.Helper()
	id, version = seedVersion(t, super, false)
	dateRule(t, super, id, ruleKey)
	sealAndDate(t, super, id, from)
	return id, version
}

func ruleKeys(rs RuleSet) []string {
	keys := []string{}
	for _, r := range rs.Rules {
		keys = append(keys, r.Key)
	}
	return keys
}

func v4Row(t *testing.T, super *pgxpool.Pool) (id string) {
	t.Helper()
	if err := super.QueryRow(context.Background(),
		`SELECT id FROM rule_set_versions WHERE version = 4`).Scan(&id); err != nil {
		t.Fatalf("read v4: %v", err)
	}
	return id
}

func TestStore_LoadForDatesPicksTheVersionInForceOnEachDate(t *testing.T) {
	super, app := dbTestPools(t)
	aID, aVer := dateFixture(t, super, "3001-01-01", "t-a")
	bID, bVer := dateFixture(t, super, "3001-06-01", "t-b")

	dates := []string{"3000-12-31", "3001-02-01", "3001-07-01"}
	got, err := NewStore(app).LoadForDates(context.Background(), dates)
	if err != nil {
		t.Fatalf("LoadForDates: %v", err)
	}
	if len(got) != len(dates) {
		t.Fatalf("len(result) = %d, want %d (total over the requested dates)", len(got), len(dates))
	}
	v4 := v4Row(t, super)
	if got["3000-12-31"].ID != v4 || got["3000-12-31"].Version != 4 {
		t.Errorf("3000-12-31 = %s v%d, want v4 (%s)", got["3000-12-31"].ID, got["3000-12-31"].Version, v4)
	}
	if g := got["3001-02-01"]; g.ID != aID || g.Version != aVer {
		t.Errorf("3001-02-01 = %s v%d, want fixture A %s v%d", g.ID, g.Version, aID, aVer)
	}
	if g := got["3001-07-01"]; g.ID != bID || g.Version != bVer {
		t.Errorf("3001-07-01 = %s v%d, want fixture B %s v%d", g.ID, g.Version, bID, bVer)
	}
	if k := ruleKeys(got["3001-02-01"]); !reflect.DeepEqual(k, []string{"t-a"}) {
		t.Errorf("A rule keys = %v, want [t-a]", k)
	}
}

func TestStore_TodayIsV4AtHead(t *testing.T) {
	super, app := dbTestPools(t)
	want := v4Row(t, super)
	store := NewStore(app)

	g, err := store.LoadActiveRuleSetGlobal(context.Background())
	if err != nil {
		t.Fatalf("LoadActiveRuleSetGlobal: %v", err)
	}
	if g.Version != 4 || g.ID != want {
		t.Errorf("global = %s v%d, want v4 %s", g.ID, g.Version, want)
	}
	te, err := store.LoadActiveRuleSet(newTestIdentity())
	if err != nil {
		t.Fatalf("LoadActiveRuleSet: %v", err)
	}
	if te.Version != 4 || te.ID != want {
		t.Errorf("tenant = %s v%d, want v4 %s", te.ID, te.Version, want)
	}
}

func TestStore_LoadActiveRuleSetGlobalIsTheVersionInForceToday(t *testing.T) {
	super, app := dbTestPools(t)
	today := time.Now().UTC().Format(time.DateOnly) // not todayUTC(): the oracle must not share the code under test
	id, _ := dateFixture(t, super, today, "t-today")

	rs, err := NewStore(app).LoadActiveRuleSetGlobal(context.Background())
	if err != nil {
		t.Fatalf("LoadActiveRuleSetGlobal: %v", err)
	}
	if rs.ID != id {
		t.Errorf("ID = %s, want the fixture dated today %s", rs.ID, id)
	}
}

func TestStore_ScheduledVersionIsNotTodaysVersion(t *testing.T) {
	super, app := dbTestPools(t)
	tomorrow := time.Now().UTC().AddDate(0, 0, 1).Format(time.DateOnly)
	id, _ := dateFixture(t, super, tomorrow, "t-tomorrow")

	rs, err := NewStore(app).LoadActiveRuleSetGlobal(context.Background())
	if err != nil {
		t.Fatalf("LoadActiveRuleSetGlobal: %v", err)
	}
	if rs.ID == id {
		t.Errorf("loaded the version scheduled for %s as today's", tomorrow)
	}
	if rs.Version != 4 {
		t.Errorf("Version = %d, want 4", rs.Version)
	}
}

func TestStore_NoDatedVersionIsErrNoActiveRuleSet(t *testing.T) {
	super, _ := dbTestPools(t)
	ctx := context.Background()
	tx, err := super.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	for _, q := range []string{
		`ALTER TABLE rule_set_versions DISABLE TRIGGER USER`,
		`UPDATE rule_set_versions SET effective_from = NULL`,
	} {
		if _, err := tx.Exec(ctx, q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	_, err = loadForDatesTx(ctx, tx, []string{todayUTC()})
	if !errors.Is(err, ErrNoActiveRuleSet) {
		t.Fatalf("err = %v, want ErrNoActiveRuleSet", err)
	}
	if errors.Is(err, ErrEmptyRuleSet) {
		t.Errorf("err = %v is ErrEmptyRuleSet, want the plain no-version error", err)
	}
}

func TestStore_DatedVersionWithoutRulesIsErrEmptyRuleSet(t *testing.T) {
	super, app := dbTestPools(t)
	id, _ := seedVersion(t, super, false)
	sealAndDate(t, super, id, "3001-01-01")

	_, err := NewStore(app).LoadForDates(context.Background(), []string{"3001-01-01"})
	if !errors.Is(err, ErrEmptyRuleSet) || !errors.Is(err, ErrNoActiveRuleSet) {
		t.Fatalf("err = %v, want ErrEmptyRuleSet (wrapping ErrNoActiveRuleSet)", err)
	}
}

// TestStore_KillSwitchLiveReload: after the kill switch commits, a fresh
// LoadActiveRuleSet sees R.Enabled=false -- no redeploy, no cache.
func TestStore_KillSwitchLiveReload(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	versionID, _ := seedVersion(t, super, false)
	seedFullRule(t, super, versionID, ruleFixture{Key: "R", Enabled: true})
	// The kill switch still targets is_active, so the fixture is both active and in force today.
	sealAndActivate(t, super, versionID)
	sealAndDate(t, super, versionID, todayUTC())

	store := NewStore(app)
	tenantID := uuid.NewString()
	c := auth.WithIdentity(ctx, auth.Identity{Subject: "user-1", Role: "authenticated", TenantID: tenantID})

	if n := runKillSwitch(t, super, "R", false); n != 1 {
		t.Fatalf("kill switch (R, false) rows = %d, want 1", n)
	}

	rs, err := store.LoadActiveRuleSet(c)
	if err != nil {
		t.Fatalf("LoadActiveRuleSet after kill switch: %v", err)
	}
	var found bool
	for _, r := range rs.Rules {
		if r.Key != "R" {
			continue
		}
		found = true
		if r.Enabled {
			t.Error("LoadActiveRuleSet after kill switch (R,false): R.Enabled = true, want false (no redeploy)")
		}
	}
	if !found {
		t.Fatal("R not present in RuleSet.Rules after kill switch")
	}
}

// TestStore_LoadNoIdentityErrors: rules are global, but LoadActiveRuleSet still
// runs in the request-tenant tx, so a context with no identity is refused.
func TestStore_LoadNoIdentityErrors(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background() // deliberately no auth.WithIdentity

	versionID, _ := seedVersion(t, super, false)
	seedFullRule(t, super, versionID, ruleFixture{Key: "R", Enabled: true})
	sealAndDate(t, super, versionID, todayUTC())

	if _, err := NewStore(app).LoadActiveRuleSet(ctx); !errors.Is(err, db.ErrNoTenant) {
		t.Fatalf("LoadActiveRuleSet with no identity: err = %v, want db.ErrNoTenant", err)
	}
}

// TestStore_LoadOrdersAndRoundTripsFields (QA adversarial): seeds three rules
// out of key order and one carrying a non-null "when" guard plus a
// structured `params` blob, then asserts LoadActiveRuleSet (a) returns them
// sorted ORDER BY key (not insertion order), and (b) faithfully round-trips
// When (*string, non-nil), Params, Scope, Type, and Severity -- fields
// TestStore_LoadActiveRuleSet does not exercise (no "when", no ordering
// check, only two rules already alphabetical).
func TestStore_LoadOrdersAndRoundTripsFields(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	versionID, _ := seedVersion(t, super, false)
	whenExpr := "invoice.total > 0"
	paramsJSON := `{"expr":"invoice.total > 0 && invoice.total < 1000000"}`
	// Seeded out of key order (zulu, alpha, mike) to prove LoadActiveRuleSet's
	// `ORDER BY key` (store.go), not insertion order.
	seedFullRule(t, super, versionID, ruleFixture{
		Key: "zulu", Type: "cel", Target: "invoice", Params: paramsJSON,
		Severity: "info", When: &whenExpr, Message: "cel rule", Scope: "document", Enabled: true,
	})
	seedFullRule(t, super, versionID, ruleFixture{Key: "alpha", Enabled: true})
	seedFullRule(t, super, versionID, ruleFixture{Key: "mike", Enabled: true})
	sealAndDate(t, super, versionID, todayUTC())

	store := NewStore(app)
	tenantID := uuid.NewString()
	c := auth.WithIdentity(ctx, auth.Identity{Subject: "user-1", Role: "authenticated", TenantID: tenantID})

	rs, err := store.LoadActiveRuleSet(c)
	if err != nil {
		t.Fatalf("LoadActiveRuleSet: %v", err)
	}
	if len(rs.Rules) != 3 {
		t.Fatalf("len(RuleSet.Rules) = %d, want 3", len(rs.Rules))
	}

	gotKeys := []string{rs.Rules[0].Key, rs.Rules[1].Key, rs.Rules[2].Key}
	wantKeys := []string{"alpha", "mike", "zulu"}
	if !reflect.DeepEqual(gotKeys, wantKeys) {
		t.Fatalf("RuleSet.Rules key order = %v, want %v (ORDER BY key, not insertion order)", gotKeys, wantKeys)
	}

	zulu := rs.Rules[2]
	if zulu.Type != TypeCEL {
		t.Errorf("zulu.Type = %q, want %q", zulu.Type, TypeCEL)
	}
	if zulu.Severity != Severity("info") {
		t.Errorf("zulu.Severity = %q, want info", zulu.Severity)
	}
	if zulu.Scope != "document" {
		t.Errorf("zulu.Scope = %q, want document", zulu.Scope)
	}
	if zulu.When == nil {
		t.Fatal(`zulu.When = nil, want non-nil (a "when" guard was seeded)`)
	}
	if *zulu.When != whenExpr {
		t.Errorf("zulu.When = %q, want %q", *zulu.When, whenExpr)
	}

	// jsonb re-serializes on round trip (e.g. Postgres inserts a space after
	// ':', and does not guarantee the on-disk key order matches input text),
	// so a literal byte comparison against the seeded input string is not a
	// meaningful invariant. Decode both sides and compare the resulting
	// values instead -- that is what "Params round-trips faithfully" means.
	var wantParams, gotParams map[string]any
	if err := json.Unmarshal([]byte(paramsJSON), &wantParams); err != nil {
		t.Fatalf("unmarshal seeded params: %v", err)
	}
	if err := json.Unmarshal(zulu.Params, &gotParams); err != nil {
		t.Fatalf("unmarshal loaded params %s: %v", zulu.Params, err)
	}
	if !reflect.DeepEqual(wantParams, gotParams) {
		t.Errorf("zulu.Params decoded = %v, want %v", gotParams, wantParams)
	}

	// alpha/mike were seeded with no "when" guard -- confirm the nullable
	// column's zero case round-trips to a true nil *string, not a pointer to
	// an empty string.
	for _, key := range []string{"alpha", "mike"} {
		for _, r := range rs.Rules {
			if r.Key != key {
				continue
			}
			if r.When != nil {
				t.Errorf("%s.When = %q, want nil (no when guard seeded)", key, *r.When)
			}
		}
	}
}

// ---------------------------------------------------------------------
// M4-04-03 (task-109) -- VB-14/VB-15: [uuid-stamp] + [tenant-free-ruleset-
// load]'s Stage-1 addendum G3 fix.
// ---------------------------------------------------------------------

// TestStore_LoadActiveRuleSetGlobalNoIdentity (VB-14, Stage-1 addendum G3's
// required fix): LoadActiveRuleSetGlobal must succeed with NO identity in
// ctx (auth.WithIdentity is never called here -- that is the whole point of
// the tenant-free load, platform/db/tenant.go's WithinRequestTenantTx is why it must
// exist as something OTHER than a thin wrapper over LoadActiveRuleSet) --
// rs.Version and rs.ID must match the live active row, and critically
// len(rs.Rules) must equal that row's real rule count.
//
// The rule-count assertion is the G3 fix: [tenant-free-ruleset-load]'s
// "fails closed" claim is true for the version SELECT (RLS there -> zero
// rows -> ErrNoActiveRuleSet -> 503) but FALSE for the rules SELECT alone
// (RLS there, under the house nullif(...,missing_ok=true) idiom -> zero
// rows, NO ERROR -> rs.Rules = [] -> err == nil -> EVERY invoice validates
// clean, HTTP 200 -- a silent fail-OPEN in a compliance gate). VB-12
// (a valid invoice -> zero violations) cannot catch an empty rule-set on
// its own -- it is satisfied VACUOUSLY by zero rules. This
// len(rs.Rules)==<live count> assertion is the one thing standing between
// "fails closed" being ASSERTED (in prose) and being TESTED.
func TestStore_LoadActiveRuleSetGlobalNoIdentity(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background() // deliberately NOT auth.WithIdentity

	var wantID string
	var wantVersion int
	var wantRuleCount int
	if err := super.QueryRow(ctx,
		`SELECT id, version FROM rule_set_versions WHERE is_active LIMIT 1`,
	).Scan(&wantID, &wantVersion); err != nil {
		t.Fatalf("read the active rule_set_versions row: %v", err)
	}
	if err := super.QueryRow(ctx,
		`SELECT count(*) FROM rules WHERE rule_set_version_id = $1`, wantID,
	).Scan(&wantRuleCount); err != nil {
		t.Fatalf("count rules for the active version: %v", err)
	}
	if wantRuleCount == 0 {
		t.Fatalf("active version (id=%s) has 0 rules -- the fixture itself is broken; this test needs a "+
			"non-empty active rule-set to distinguish a real load from the G3 silent-empty failure mode", wantID)
	}

	store := NewStore(app)
	rs, err := store.LoadActiveRuleSetGlobal(ctx)
	if err != nil {
		t.Fatalf("LoadActiveRuleSetGlobal with no identity: err = %v, want nil -- a db.ErrNoTenant here would mean "+
			"the tenant-free load structurally cannot run without identity (platform/db/tenant.go's "+
			"WithinRequestTenantTx is why this "+
			"method must exist as something OTHER than a thin wrapper over LoadActiveRuleSet)", err)
	}
	if rs.Version != wantVersion {
		t.Errorf("rs.Version = %d, want %d (the live active version)", rs.Version, wantVersion)
	}
	if rs.ID != wantID {
		t.Errorf("rs.ID = %q, want %q (the active rule_set_versions.id) [uuid-stamp]", rs.ID, wantID)
	}
	if len(rs.Rules) != wantRuleCount {
		t.Fatalf("len(rs.Rules) = %d, want %d (the active version's live rule count) -- an empty/short Rules "+
			"slice here is the G3 silent fail-OPEN: zero rules means Evaluate finds nothing to check and every "+
			"invoice validates clean", len(rs.Rules), wantRuleCount)
	}
}

// TestStore_LoadActiveRuleSetPopulatesID (VB-15): the EXISTING
// LoadActiveRuleSet path (identity required, unchanged signature/tenant
// wrap, unchanged behavior) must keep succeeding exactly as before, and now
// ALSO populate rs.ID -- the versionID it already scans and, until this
// subtask, silently discarded (store.go:72-76, [uuid-stamp]).
func TestStore_LoadActiveRuleSetPopulatesID(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	var wantID string
	if err := super.QueryRow(ctx,
		`SELECT id FROM rule_set_versions WHERE is_active LIMIT 1`,
	).Scan(&wantID); err != nil {
		t.Fatalf("read the active rule_set_versions row: %v", err)
	}

	store := NewStore(app)
	tenantID := uuid.NewString()
	c := auth.WithIdentity(ctx, auth.Identity{Subject: "user-1", Role: "authenticated", TenantID: tenantID})

	rs, err := store.LoadActiveRuleSet(c)
	if err != nil {
		t.Fatalf("LoadActiveRuleSet: %v", err)
	}
	if rs.ID != wantID {
		t.Errorf("rs.ID = %q, want %q -- LoadActiveRuleSet already scans versionID (store.go:72-76), it must "+
			"now ALSO assign it to rs.ID [uuid-stamp]", rs.ID, wantID)
	}
}

func TestStore_LoadForDatesStartDateIsInclusiveAndDuplicatesCollapse(t *testing.T) {
	super, app := dbTestPools(t)
	aID, _ := dateFixture(t, super, "3001-01-01", "t-a")

	got, err := NewStore(app).LoadForDates(context.Background(),
		[]string{"3001-01-01", "3001-01-01", "3000-12-31"})
	if err != nil {
		t.Fatalf("LoadForDates: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("len(result) = %d, want 2 distinct dates", len(got))
	}
	if got["3001-01-01"].ID != aID {
		t.Errorf("start day resolved to %s, want fixture %s (inclusive)", got["3001-01-01"].ID, aID)
	}
	if got["3000-12-31"].ID == aID || got["3000-12-31"].Version != 4 {
		t.Errorf("day before start = %s v%d, want v4", got["3000-12-31"].ID, got["3000-12-31"].Version)
	}
}

func TestStore_LoadForDatesDateBeforeEveryStartGetsTheEarliestVersion(t *testing.T) {
	super, app := dbTestPools(t)
	aID, _ := dateFixture(t, super, "3001-01-01", "t-a")

	got, err := NewStore(app).LoadForDates(context.Background(), []string{"2000-01-01"})
	if err != nil {
		t.Fatalf("LoadForDates: %v", err)
	}
	rs, ok := got["2000-01-01"]
	if !ok {
		t.Fatal("no entry for 2000-01-01")
	}
	if rs.ID == aID || rs.Version != 4 {
		t.Errorf("2000-01-01 = %s v%d, want the earliest dated version v4", rs.ID, rs.Version)
	}
}

func TestStore_LoadForDatesEmptyAndMalformedInput(t *testing.T) {
	_, app := dbTestPools(t)
	store := NewStore(app)
	ctx := context.Background()

	got, err := store.LoadForDates(ctx, []string{})
	if err != nil || got == nil || len(got) != 0 {
		t.Errorf("empty input = (%v, %v), want empty non-nil map and nil error", got, err)
	}
	if _, err := store.LoadForDates(ctx, []string{"not-a-date"}); err == nil || errors.Is(err, ErrNoActiveRuleSet) {
		t.Errorf("malformed date err = %v, want a non-ErrNoActiveRuleSet error", err)
	}
}

func TestStore_TenantLoaderIgnoresAScheduledVersion(t *testing.T) {
	super, app := dbTestPools(t)
	tomorrow := time.Now().UTC().AddDate(0, 0, 1).Format(time.DateOnly)
	id, _ := dateFixture(t, super, tomorrow, "t-tomorrow")

	rs, err := NewStore(app).LoadActiveRuleSet(newTestIdentity())
	if err != nil {
		t.Fatalf("LoadActiveRuleSet: %v", err)
	}
	if rs.ID == id || rs.Version != 4 {
		t.Errorf("tenant loader = %s v%d, want v4 (not the version scheduled for %s)", rs.ID, rs.Version, tomorrow)
	}
}

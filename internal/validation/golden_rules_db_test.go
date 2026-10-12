// Store.GoldenRules against the dev DB. `rule_set_versions` and `rules` are global: fixtures
// clean up after themselves. No t.Parallel().
package validation

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
	"github.com/SimonOsipov/invoice-os/internal/platform/db"
)

const (
	goldenTenantOkafor    = "11111111-1111-1111-1111-111111111111"
	goldenTenantHoneywell = "22222222-2222-2222-2222-222222222222"
	// v4 starts at activeSeedDate; v5 is the scheduled migration.
	goldenV5Date = "2027-01-01"
	// Between v4's start and v5's, so v4 is in force and v5 scheduled whenever the suite runs.
	goldenPinnedToday = "2026-10-12"
)

// goldenWire is Store.GoldenRules seen as the handler writes it, so a test does not depend on field types.
type goldenWire struct {
	Today            string `json:"today"`
	RuleSetVersionID string `json:"rule_set_version_id"`
	Version          int    `json:"version"`
	EffectiveFrom    string `json:"effective_from"`
	Versions         []struct {
		RuleSetVersionID string `json:"rule_set_version_id"`
		Version          int    `json:"version"`
		State            string `json:"state"`
		EffectiveFrom    string `json:"effective_from"`
		RuleCount        int    `json:"rule_count"`
	} `json:"versions"`
	Rules []struct {
		Key      string          `json:"key"`
		Type     string          `json:"type"`
		Target   string          `json:"target"`
		Params   json.RawMessage `json:"params"`
		Severity string          `json:"severity"`
		When     *string         `json:"when"`
		Message  string          `json:"message"`
	} `json:"rules"`
}

// seedGoldenMember inserts a membership of tenantID with status and returns that user's identity.
func seedGoldenMember(t *testing.T, super *pgxpool.Pool, tenantID, role, status string) context.Context {
	t.Helper()
	user := uuid.NewString()
	id := uuid.NewString()
	if _, err := super.Exec(context.Background(),
		`INSERT INTO memberships (id, tenant_id, user_id, role, status) VALUES ($1, $2, $3, $4, $5)`,
		id, tenantID, user, role, status); err != nil {
		t.Fatalf("seed %s membership: %v", status, err)
	}
	t.Cleanup(func() { _, _ = super.Exec(context.Background(), `DELETE FROM memberships WHERE id = $1`, id) })
	return auth.WithIdentity(context.Background(), auth.Identity{Subject: user, Role: "authenticated", TenantID: tenantID})
}

func readGolden(ctx context.Context, app *pgxpool.Pool) (goldenWire, error) {
	got, err := NewStore(app).GoldenRules(ctx)
	if err != nil {
		return goldenWire{}, err
	}
	// No HTML escaping: params are compared byte for byte with the stored text.
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(got); err != nil {
		return goldenWire{}, err
	}
	var w goldenWire
	return w, json.Unmarshal(buf.Bytes(), &w)
}

// readGoldenAsOf reads as of a pinned today, so a date-based expectation does not rot.
func readGoldenAsOf(t *testing.T, ctx context.Context, app *pgxpool.Pool, today string) goldenWire {
	t.Helper()
	asOf, err := time.Parse(time.DateOnly, today)
	if err != nil {
		t.Fatal(err)
	}
	got, err := NewStore(app).goldenRulesAsOf(ctx, asOf)
	if err != nil {
		t.Fatalf("goldenRulesAsOf: %v", err)
	}
	b, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	var w goldenWire
	if err := json.Unmarshal(b, &w); err != nil {
		t.Fatal(err)
	}
	return w
}

func mustReadGolden(t *testing.T, ctx context.Context, app *pgxpool.Pool) goldenWire {
	t.Helper()
	w, err := readGolden(ctx, app)
	if err != nil {
		t.Fatalf("GoldenRules: %v", err)
	}
	return w
}

func (w goldenWire) stateOf(t *testing.T, version int) (state, from string, count int) {
	t.Helper()
	for _, v := range w.Versions {
		if v.Version == version {
			return v.State, v.EffectiveFrom, v.RuleCount
		}
	}
	t.Fatalf("version %d is not listed", version)
	return "", "", 0
}

func (w goldenWire) hasRule(key string) bool {
	for _, r := range w.Rules {
		if r.Key == key {
			return true
		}
	}
	return false
}

func TestGoldenRules_DB_ReadsTheVersionInForce(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := seedGoldenMember(t, super, goldenTenantOkafor, "admin", "active")
	got := mustReadGolden(t, ctx, app)

	inForceID, inForceVersion := versionInForce(t, super)
	var from string
	if err := super.QueryRow(context.Background(),
		`SELECT to_char(effective_from, 'YYYY-MM-DD') FROM rule_set_versions WHERE id = $1`, inForceID).Scan(&from); err != nil {
		t.Fatal(err)
	}
	if got.RuleSetVersionID != inForceID || got.Version != inForceVersion || got.EffectiveFrom != from {
		t.Errorf("in force = %s v%d from %q, want %s v%d from %q", got.RuleSetVersionID, got.Version, got.EffectiveFrom, inForceID, inForceVersion, from)
	}
	if got.Today != todayUTC() {
		t.Errorf("today = %q, want %q", got.Today, todayUTC())
	}

	rows, err := super.Query(context.Background(),
		`SELECT key, type, target, params::text, severity, "when", message FROM rules WHERE rule_set_version_id = $1 AND enabled ORDER BY key`, inForceID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	i := 0
	for rows.Next() {
		var key, typ, target, params, severity, message string
		var when *string
		if err := rows.Scan(&key, &typ, &target, &params, &severity, &when, &message); err != nil {
			t.Fatal(err)
		}
		if i >= len(got.Rules) {
			t.Fatalf("got %d rules, the version has more", len(got.Rules))
		}
		g := got.Rules[i]
		var wantParams bytes.Buffer
		if err := json.Compact(&wantParams, []byte(params)); err != nil {
			t.Fatal(err)
		}
		if g.Key != key || g.Type != typ || g.Target != target || g.Severity != severity || g.Message != message {
			t.Errorf("rule %d = %+v, want key %s type %s target %s severity %s", i, g, key, typ, target, severity)
		}
		if !bytes.Equal(g.Params, wantParams.Bytes()) {
			t.Errorf("rule %s params = %s, want %s", key, g.Params, wantParams.Bytes())
		}
		if (g.When == nil) != (when == nil) || (when != nil && *g.When != *when) {
			t.Errorf("rule %s when = %v, want %v", key, g.When, when)
		}
		i++
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if i == 0 || i != len(got.Rules) {
		t.Errorf("got %d rules, the version has %d enabled (>0)", len(got.Rules), i)
	}
}

func TestGoldenRules_DB_ListsDatedVersionsWithState(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := seedGoldenMember(t, super, goldenTenantOkafor, "admin", "active")
	// One draft may exist at a time: seal the retired fixture before opening the draft.
	retiredID, retired := seedVersion(t, super)
	sealUndated(t, super, retiredID)
	_, draft := seedVersion(t, super)

	got := readGoldenAsOf(t, ctx, app, goldenPinnedToday)
	if len(got.Versions) == 0 {
		t.Fatal("no versions listed")
	}
	if state, from, count := got.stateOf(t, 5); state != "scheduled" || from != goldenV5Date || count == 0 {
		t.Errorf("v5 = %s from %q count %d, want scheduled from %s, count > 0", state, from, count, goldenV5Date)
	}
	if state, from, count := got.stateOf(t, activeSeedVersion); state != "in_force" || from != activeSeedDate || count == 0 {
		t.Errorf("v%d = %s from %q count %d, want in_force from %s, count > 0", activeSeedVersion, state, from, count, activeSeedDate)
	}

	listed := map[int]bool{}
	prev := int(^uint(0) >> 1)
	for _, v := range got.Versions {
		listed[v.Version] = true
		if v.Version >= prev {
			t.Errorf("v%d after v%d: not highest first", v.Version, prev)
		}
		prev = v.Version
		if v.EffectiveFrom == "" || (v.State != "in_force" && v.State != "scheduled" && v.State != "superseded") {
			t.Errorf("v%d = state %q from %q, want a dated in_force, scheduled or superseded version", v.Version, v.State, v.EffectiveFrom)
		}
	}
	for _, absent := range []int{1, 2, 3, retired, draft} {
		if listed[absent] {
			t.Errorf("v%d (retired or open draft) is listed", absent)
		}
	}
}

func TestGoldenRules_DB_StatesFollowTheDates(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := seedGoldenMember(t, super, goldenTenantOkafor, "admin", "active")
	day := func(offset int) string { return time.Now().UTC().AddDate(0, 0, offset).Format(time.DateOnly) }

	// One draft may exist at a time: seal each fixture before seeding the next.
	olderID, older := seedVersion(t, super)
	sealAndDate(t, super, olderID, day(-2))
	newerID, newer := seedVersion(t, super)
	sealAndDate(t, super, newerID, day(-1))
	tomorrowID, tomorrow := seedVersion(t, super)
	sealAndDate(t, super, tomorrowID, day(1))

	got := mustReadGolden(t, ctx, app)
	for version, want := range map[int]struct{ state, from string }{
		newer:             {"in_force", day(-1)},
		older:             {"superseded", day(-2)},
		tomorrow:          {"scheduled", day(1)},
		activeSeedVersion: {"superseded", activeSeedDate},
	} {
		if state, from, _ := got.stateOf(t, version); state != want.state || from != want.from {
			t.Errorf("v%d = %s from %q, want %s from %q", version, state, from, want.state, want.from)
		}
	}
	inForce := 0
	for _, v := range got.Versions {
		if v.State == "in_force" {
			inForce++
		}
	}
	if inForce != 1 || got.Version != newer || got.RuleSetVersionID != newerID {
		t.Errorf("%d versions in force and the rules block is v%d %s, want exactly 1 and v%d %s", inForce, got.Version, got.RuleSetVersionID, newer, newerID)
	}
}

func TestGoldenRules_DB_SwitchedOffRuleIsAbsent(t *testing.T) {
	super, app := dbTestPools(t)
	restoreRulesOnCleanup(t, super)
	ctx := seedGoldenMember(t, super, goldenTenantOkafor, "admin", "active")
	actor := seedRulesStaffRow(t, super)

	before := mustReadGolden(t, ctx, app)
	if !before.hasRule(functionKey) || len(before.Rules) == 0 {
		t.Fatalf("fixture: %s is not among the %d rules in force", functionKey, len(before.Rules))
	}
	_, _, countBefore := before.stateOf(t, before.Version)

	switchRuleViaFunction(t, app, actor, functionKey, false)
	after := mustReadGolden(t, ctx, app)
	if after.hasRule(functionKey) {
		t.Errorf("%s is switched off but still listed", functionKey)
	}
	if len(after.Rules) != len(before.Rules)-1 {
		t.Errorf("got %d rules, want %d (one fewer)", len(after.Rules), len(before.Rules)-1)
	}
	if _, _, count := after.stateOf(t, after.Version); count != countBefore-1 {
		t.Errorf("v%d rule_count = %d, want %d (one fewer)", after.Version, count, countBefore-1)
	}
	if after.Version != before.Version {
		t.Errorf("version in force changed from v%d to v%d", before.Version, after.Version)
	}

	switchRuleViaFunction(t, app, actor, functionKey, true)
	if restored := mustReadGolden(t, ctx, app); !restored.hasRule(functionKey) || len(restored.Rules) != len(before.Rules) {
		t.Errorf("after the switch back: %d rules and %s listed = %t, want %d and true", len(restored.Rules), functionKey, restored.hasRule(functionKey), len(before.Rules))
	}
}

func TestGoldenRules_DB_SuspendedMemberRefused(t *testing.T) {
	super, app := dbTestPools(t)
	active := seedGoldenMember(t, super, goldenTenantOkafor, "admin", "active")
	if got := mustReadGolden(t, active, app); len(got.Rules) == 0 {
		t.Fatal("control: an active member reads no rules")
	}
	for _, status := range []string{"suspended", "invited"} {
		t.Run(status, func(t *testing.T) {
			ctx := seedGoldenMember(t, super, goldenTenantOkafor, "admin", status)
			if _, err := readGolden(ctx, app); !errors.Is(err, db.ErrNotActiveMember) {
				t.Errorf("GoldenRules as a %s member = %v, want db.ErrNotActiveMember", status, err)
			}
		})
	}
}

// A member of one tenant presenting another tenant's id is refused; the data is global, the gate is not.
func TestGoldenRules_DB_NonMemberOfTheRequestedTenantRefused(t *testing.T) {
	super, app := dbTestPools(t)
	honeywell := seedGoldenMember(t, super, goldenTenantHoneywell, "admin", "active")
	if got := mustReadGolden(t, honeywell, app); len(got.Rules) == 0 {
		t.Fatal("control: an active member of tenant 2222 reads no rules")
	}
	id, _ := auth.IdentityFromContext(honeywell)
	cross := auth.WithIdentity(context.Background(), auth.Identity{Subject: id.Subject, Role: "authenticated", TenantID: goldenTenantOkafor})
	if _, err := readGolden(cross, app); !errors.Is(err, db.ErrNotActiveMember) {
		t.Errorf("GoldenRules for tenant 1111 as a member of tenant 2222 = %v, want db.ErrNotActiveMember", err)
	}
	stranger := auth.WithIdentity(context.Background(), auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: goldenTenantOkafor})
	if _, err := readGolden(stranger, app); !errors.Is(err, db.ErrNotActiveMember) {
		t.Errorf("GoldenRules as a user with no membership = %v, want db.ErrNotActiveMember", err)
	}
}

func TestGoldenRules_DB_NoIdentityIsErrNoTenant(t *testing.T) {
	_, app := dbTestPools(t)
	if _, err := readGolden(context.Background(), app); !errors.Is(err, db.ErrNoTenant) {
		t.Errorf("GoldenRules with no identity = %v, want db.ErrNoTenant", err)
	}
}

// Temp tables shadow rule_set_versions and rules on this pool only, so no dated version exists.
func TestGoldenRules_DB_NoDatedVersionIsErrNoActiveRuleSet(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := seedGoldenMember(t, super, goldenTenantOkafor, "admin", "active")
	if got := mustReadGolden(t, ctx, app); got.Version == 0 {
		t.Fatal("control: the shared pool finds no version in force")
	}

	cfg, err := pgxpool.ParseConfig(os.Getenv("DATABASE_URL"))
	if err != nil {
		t.Fatal(err)
	}
	cfg.AfterConnect = func(ctx context.Context, c *pgx.Conn) error {
		for _, tbl := range []string{"rule_set_versions", "rules"} {
			if _, err := c.Exec(ctx, `CREATE TEMP TABLE `+tbl+` (LIKE public.`+tbl+` INCLUDING DEFAULTS)`); err != nil {
				return err
			}
		}
		return nil
	}
	empty, err := pgxpool.NewWithConfig(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(empty.Close)

	if _, err := readGolden(ctx, empty); !errors.Is(err, ErrNoActiveRuleSet) {
		t.Errorf("GoldenRules with no dated version = %v, want ErrNoActiveRuleSet", err)
	}
}

// Real store behind the real handler: the member gate answers on the wire, and no rule carries `enabled`.
func TestGoldenRulesHandler_DB_MemberGateAndWire(t *testing.T) {
	super, app := dbTestPools(t)
	h := GoldenRulesHandler(NewStore(app).GoldenRules, nil)

	rec := doGoldenRules(h, seedGoldenMember(t, super, goldenTenantOkafor, "admin", "active"))
	if rec.Code != http.StatusOK {
		t.Fatalf("active member: status = %d, want 200 (body=%q)", rec.Code, rec.Body.String())
	}
	var body struct {
		Rules []json.RawMessage `json:"rules"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Rules) == 0 {
		t.Fatal("active member: no rules on the wire")
	}
	for i, r := range body.Rules {
		assertGoldenKeys(t, fmt.Sprintf("rules[%d]", i), r, "key", "type", "target", "params", "severity", "when", "message")
	}

	for _, status := range []string{"suspended", "invited"} {
		rec := doGoldenRules(h, seedGoldenMember(t, super, goldenTenantOkafor, "admin", status))
		var e map[string]string
		_ = json.Unmarshal(rec.Body.Bytes(), &e)
		if rec.Code != http.StatusForbidden || e["error"] != db.NotActiveMemberMessage {
			t.Errorf("%s member: %d %q, want 403 %q", status, rec.Code, rec.Body.String(), db.NotActiveMemberMessage)
		}
	}
	if rec := doGoldenRules(h, context.Background()); rec.Code != http.StatusUnauthorized {
		t.Errorf("no identity: status = %d, want 401", rec.Code)
	}
}

// The version in force today carries no `when`; the one that follows it does.
func TestGoldenRules_DB_WhenSurvivesForAVersionThatHasOne(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := seedGoldenMember(t, super, goldenTenantOkafor, "admin", "active")
	const future = "2027-06-01"
	got := readGoldenAsOf(t, ctx, app, future)

	want := map[string]*string{}
	rows, err := super.Query(context.Background(),
		`SELECT key, "when" FROM rules WHERE rule_set_version_id = rule_set_version_for($1::date) AND enabled`, future)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	withWhen := 0
	for rows.Next() {
		var key string
		var when *string
		if err := rows.Scan(&key, &when); err != nil {
			t.Fatal(err)
		}
		want[key] = when
		if when != nil {
			withWhen++
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if withWhen == 0 || len(got.Rules) != len(want) {
		t.Fatalf("fixture: %d of %d rules carry a when, got %d rules (want > 0 with a when)", withWhen, len(want), len(got.Rules))
	}
	for _, r := range got.Rules {
		w, ok := want[r.Key]
		if !ok || (r.When == nil) != (w == nil) || (w != nil && *r.When != *w) {
			t.Errorf("rule %s when = %v, want %v (listed in the version: %t)", r.Key, r.When, w, ok)
		}
	}
}

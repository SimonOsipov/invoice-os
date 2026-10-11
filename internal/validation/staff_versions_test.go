// Store.Versions and Store.RulesOfVersion against the dev DB. `rule_set_versions` is global:
// fixtures clean up after themselves. No t.Parallel().
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
)

func versionsOf(t *testing.T, app *pgxpool.Pool) VersionList {
	t.Helper()
	got, err := NewStore(app).Versions(staffCtx(t, uuid.NewString()))
	if err != nil {
		t.Fatalf("Versions: %v", err)
	}
	return got
}

func stateOf(t *testing.T, l VersionList, version int) string {
	t.Helper()
	for _, v := range l.Versions {
		if v.Version == version {
			return v.State
		}
	}
	t.Fatalf("version %d not listed", version)
	return ""
}

func TestStaffVersions_ListsEveryVersionHighestFirst(t *testing.T) {
	super, app := dbTestPools(t)
	got := versionsOf(t, app)

	rows, err := super.Query(context.Background(),
		`SELECT id::text, version, (SELECT count(*) FROM rules r WHERE r.rule_set_version_id = v.id) FROM rule_set_versions v ORDER BY version DESC`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	i := 0
	for rows.Next() {
		var id string
		var version, count int
		if err := rows.Scan(&id, &version, &count); err != nil {
			t.Fatal(err)
		}
		if i >= len(got.Versions) {
			t.Fatalf("list has %d versions, the table has more", len(got.Versions))
		}
		if v := got.Versions[i]; v.RuleSetVersionID.String() != id || v.Version != version || v.RuleCount != count {
			t.Errorf("row %d = %s v%d count %d, want %s v%d count %d", i, v.RuleSetVersionID, v.Version, v.RuleCount, id, version, count)
		}
		i++
	}
	if i == 0 || i != len(got.Versions) {
		t.Errorf("list has %d versions, the table has %d (>0)", len(got.Versions), i)
	}
	if got.Today != todayUTC() {
		t.Errorf("today = %q, want %q", got.Today, todayUTC())
	}
}

// sealUndated seals a fixture without a start date (retired); sealAndDate cannot leave the date null.
func sealUndated(t *testing.T, super *pgxpool.Pool, id string) {
	t.Helper()
	ctx := context.Background()
	t.Cleanup(func() {
		tx, err := super.Begin(ctx)
		if err != nil {
			t.Errorf("sealUndated cleanup: %v", err)
			return
		}
		defer func() { _ = tx.Rollback(ctx) }()
		for _, q := range []string{
			`ALTER TABLE rule_set_versions DISABLE TRIGGER USER`,
			`ALTER TABLE rules DISABLE TRIGGER USER`,
			`DELETE FROM rule_set_versions WHERE id = '` + id + `'`,
			`ALTER TABLE rules ENABLE TRIGGER USER`,
			`ALTER TABLE rule_set_versions ENABLE TRIGGER USER`,
		} {
			if _, err := tx.Exec(ctx, q); err != nil {
				t.Errorf("sealUndated cleanup: %s: %v", q, err)
				return
			}
		}
		if err := tx.Commit(ctx); err != nil {
			t.Errorf("sealUndated cleanup: commit: %v", err)
		}
	})
	if _, err := super.Exec(ctx, `UPDATE rule_set_versions SET sealed = true WHERE id = $1`, id); err != nil {
		t.Fatalf("sealUndated(%s): %v", id, err)
	}
}

func TestStaffVersions_StatesFollowTheDates(t *testing.T) {
	super, app := dbTestPools(t)
	_, inForce := versionInForce(t, super)

	// One draft may exist at a time: seal each fixture before seeding the next.
	retiredID, retired := seedVersion(t, super)
	sealUndated(t, super, retiredID)
	scheduledID, scheduled := seedVersion(t, super)
	sealAndDate(t, super, scheduledID, "3001-01-01")
	draftID, draft := seedVersion(t, super)

	got := versionsOf(t, app)
	for version, want := range map[int]string{draft: "draft", retired: "retired", scheduled: "scheduled", inForce: "in_force", 1: "retired", 2: "retired", 3: "retired"} {
		if s := stateOf(t, got, version); s != want {
			t.Errorf("v%d state = %q, want %q", version, s, want)
		}
	}
	n := 0
	for _, v := range got.Versions {
		if v.State == "in_force" {
			n++
		}
	}
	if n != 1 {
		t.Errorf("%d versions in force, want exactly 1", n)
	}

	sealAndDate(t, super, draftID, todayUTC())
	got = versionsOf(t, app)
	if s := stateOf(t, got, draft); s != "in_force" {
		t.Errorf("dated-today fixture state = %q, want in_force", s)
	}
	if s := stateOf(t, got, inForce); s != "superseded" {
		t.Errorf("v%d state = %q, want superseded", inForce, s)
	}
}

func TestStaffVersions_DraftHasNullDates(t *testing.T) {
	super, app := dbTestPools(t)
	_, draft := seedVersion(t, super)
	var published time.Time
	if err := super.QueryRow(context.Background(), `SELECT published_at FROM rule_set_versions WHERE version = $1`, draft).Scan(&published); err != nil {
		t.Fatal(err)
	}

	got := versionsOf(t, app)
	for _, v := range got.Versions {
		raw, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		var m map[string]json.RawMessage
		if err := json.Unmarshal(raw, &m); err != nil {
			t.Fatal(err)
		}
		if v.Version == draft {
			if string(m["effective_from"]) != "null" || string(m["published_at"]) != "null" {
				t.Errorf("draft effective_from %s, published_at %s, want null and null", m["effective_from"], m["published_at"])
			}
			if v.OpenedAt == nil || !v.OpenedAt.Equal(published) {
				t.Errorf("draft opened_at = %v, want %v", v.OpenedAt, published)
			}
		} else if string(m["opened_at"]) != "null" {
			t.Errorf("sealed v%d opened_at = %s, want null", v.Version, m["opened_at"])
		}
	}
}

func TestStaffRules_RulesOfVersionReturnsThatVersion(t *testing.T) {
	super, app := dbTestPools(t)
	store := NewStore(app)
	ctx := staffCtx(t, uuid.NewString())

	got, err := store.RulesOfVersion(ctx, new(int))
	if err == nil || got.Version != 0 {
		t.Fatalf("RulesOfVersion(&0) = %+v, %v, want ErrNoSuchVersion", got, err)
	}
	if !errors.Is(err, ErrNoSuchVersion) {
		t.Fatalf("err = %v, want ErrNoSuchVersion", err)
	}

	v3 := 3
	got, err = store.RulesOfVersion(ctx, &v3)
	if err != nil {
		t.Fatalf("RulesOfVersion(&3): %v", err)
	}
	var wantID string
	if err := super.QueryRow(context.Background(), `SELECT id::text FROM rule_set_versions WHERE version = 3`).Scan(&wantID); err != nil {
		t.Fatal(err)
	}
	if got.Version != 3 || got.RuleSetVersionID.String() != wantID {
		t.Errorf("got v%d %s, want v3 %s", got.Version, got.RuleSetVersionID, wantID)
	}
	want := directRules(t, super, wantID)
	if !reflect.DeepEqual(got.Rules, want) {
		t.Errorf("v3 rules = %+v, want %+v", got.Rules, want)
	}

	inForceID, inForceVersion := versionInForce(t, super)
	byNumber, err := store.RulesOfVersion(ctx, &inForceVersion)
	if err != nil {
		t.Fatal(err)
	}
	byDate, err := store.RulesOfVersion(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(byNumber, byDate) || byDate.RuleSetVersionID.String() != inForceID || len(byDate.Rules) == 0 {
		t.Errorf("RulesOfVersion(&%d) = %+v, RulesOfVersion(nil) = %+v, want equal and non-empty", inForceVersion, byNumber, byDate)
	}
	if want := directRules(t, super, inForceID); !reflect.DeepEqual(byDate.Rules, want) {
		t.Errorf("in-force rules = %+v, want %+v", byDate.Rules, want)
	}
}

func TestStaffRules_RulesOfVersionReadsADraft(t *testing.T) {
	super, app := dbTestPools(t)
	id, version := seedVersion(t, super)
	seedRule(t, super, id, "draft-rule")
	got, err := NewStore(app).RulesOfVersion(staffCtx(t, uuid.NewString()), &version)
	if err != nil {
		t.Fatalf("RulesOfVersion(draft): %v", err)
	}
	if got.Version != version || len(got.Rules) != 1 || got.Rules[0].Key != "draft-rule" || got.Rules[0].When != nil || string(got.Rules[0].Params) != "{}" {
		t.Errorf("got %+v, want the draft with one rule, params {} and null when", got)
	}
}

func directRules(t *testing.T, super *pgxpool.Pool, versionID string) []StaffRule {
	t.Helper()
	rows, err := super.Query(context.Background(),
		`SELECT key, type, target, params, severity, "when", scope, message, enabled FROM rules WHERE rule_set_version_id = $1 ORDER BY key`, versionID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	out := []StaffRule{}
	for rows.Next() {
		var r StaffRule
		if err := rows.Scan(&r.Key, &r.Type, &r.Target, &r.Params, &r.Severity, &r.When, &r.Scope, &r.Message, &r.Enabled); err != nil {
			t.Fatal(err)
		}
		out = append(out, r)
	}
	return out
}

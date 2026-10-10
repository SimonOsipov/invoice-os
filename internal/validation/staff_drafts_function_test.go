// The rule_draft_* functions against the seeded rule sets. They commit, so every test calls
// removeDeskVersionsOnCleanup before its first call. No t.Parallel().
package validation

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	deskNotesLike    = `Rules desk: %`
	deskOneDraftName = "rule_set_versions_one_draft"
	deskNewKey       = "desk-test-rule"
	deskFarDate      = "3001-01-01"
	deskInForceKey   = "vat-standard-rate"
	deskDisabledKey  = "no-duplicate-line-items"

	deskOpenSQL    = `SELECT rule_set_version_id::text, version, from_version FROM rule_draft_open($1::uuid)`
	deskPutSQL     = `SELECT rule_set_version_id::text, version, existed, changed FROM rule_draft_put_rule($1::uuid, $2, $3, $4, $5::jsonb, $6, $7, $8, $9, $10)`
	deskRemoveSQL  = `SELECT rule_set_version_id::text, version, removed FROM rule_draft_remove_rule($1::uuid, $2)`
	deskPublishSQL = `SELECT rule_set_version_id::text, version, rule_count FROM rule_draft_publish($1::uuid, $2::date)`
)

// removeDeskVersionsOnCleanup deletes every desk version now and again in t.Cleanup, so debris
// from a killed run cannot block the one-draft index. User triggers are off for the delete (sealAndDate).
func removeDeskVersionsOnCleanup(t *testing.T, super *pgxpool.Pool) {
	t.Helper()
	remove := func() error {
		ctx := context.Background()
		tx, err := super.Begin(ctx)
		if err != nil {
			return fmt.Errorf("begin: %w", err)
		}
		defer func() { _ = tx.Rollback(ctx) }()
		for _, q := range []string{
			`ALTER TABLE rule_set_versions DISABLE TRIGGER USER`,
			`ALTER TABLE rules DISABLE TRIGGER USER`,
			`DELETE FROM rule_set_versions WHERE notes LIKE '` + deskNotesLike + `'`,
			`ALTER TABLE rules ENABLE TRIGGER USER`,
			`ALTER TABLE rule_set_versions ENABLE TRIGGER USER`,
		} {
			if _, err := tx.Exec(ctx, q); err != nil {
				return fmt.Errorf("%s: %w", q, err)
			}
		}
		return tx.Commit(ctx)
	}
	if err := remove(); err != nil {
		t.Fatalf("remove desk versions before the test: %v", err)
	}
	t.Cleanup(func() {
		if err := remove(); err != nil {
			t.Errorf("cleanup: remove desk versions: %v", err)
		}
	})
}

func pgErrOf(err error) *pgconn.PgError {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr
	}
	return nil
}

func sqlStateOf(err error) string {
	if e := pgErrOf(err); e != nil {
		return e.Code
	}
	return ""
}

// failDeskCall ends the test with a clear reason; 42883 names the missing migration.
func failDeskCall(t *testing.T, fn string, err error) {
	t.Helper()
	if sqlStateOf(err) == "42883" {
		t.Fatalf("%s does not exist (SQLSTATE 42883): the migration is not applied: %v", fn, err)
	}
	t.Fatalf("%s as invoice_app: %v", fn, err)
}

type deskOpened struct {
	id      string
	version int
	from    int
}
type deskPut struct {
	id              string
	version         int
	existed, change bool
}
type deskRemoved struct {
	id      string
	version int
	removed bool
}
type deskPublished struct {
	id      string
	version int
	count   int
}

type deskRule struct {
	key, typ, target, params, severity, message string
	when                                        *string
	enabled                                     bool
}

func deskTestRule(key, message string) deskRule {
	when := "invoice.vat > 0"
	return deskRule{key: key, typ: "format/regex", target: "invoice.supplier.tin", params: `{"pattern":"^[0-9]+$"}`,
		severity: "warning", when: &when, message: message, enabled: false}
}

func callDeskOpen(app *pgxpool.Pool, actor string) ([]deskOpened, error) {
	rows, err := app.Query(context.Background(), deskOpenSQL, actor)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []deskOpened
	for rows.Next() {
		var r deskOpened
		if err := rows.Scan(&r.id, &r.version, &r.from); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func callDeskPut(app *pgxpool.Pool, actor string, r deskRule, create bool) ([]deskPut, error) {
	rows, err := app.Query(context.Background(), deskPutSQL,
		actor, r.key, r.typ, r.target, r.params, r.severity, r.when, r.message, r.enabled, create)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []deskPut
	for rows.Next() {
		var p deskPut
		if err := rows.Scan(&p.id, &p.version, &p.existed, &p.change); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func callDeskRemove(app *pgxpool.Pool, actor, key string) ([]deskRemoved, error) {
	rows, err := app.Query(context.Background(), deskRemoveSQL, actor, key)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []deskRemoved
	for rows.Next() {
		var r deskRemoved
		if err := rows.Scan(&r.id, &r.version, &r.removed); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func callDeskPublish(app *pgxpool.Pool, actor, from string) ([]deskPublished, error) {
	rows, err := app.Query(context.Background(), deskPublishSQL, actor, from)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []deskPublished
	for rows.Next() {
		var p deskPublished
		if err := rows.Scan(&p.id, &p.version, &p.count); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func mustDeskOpen(t *testing.T, app *pgxpool.Pool, actor string) deskOpened {
	t.Helper()
	got, err := callDeskOpen(app, actor)
	if err != nil {
		failDeskCall(t, "rule_draft_open", err)
	}
	if len(got) != 1 {
		t.Fatalf("rule_draft_open returned %d rows, want 1", len(got))
	}
	return got[0]
}

func mustDeskPut(t *testing.T, app *pgxpool.Pool, actor string, r deskRule, create bool) deskPut {
	t.Helper()
	got, err := callDeskPut(app, actor, r, create)
	if err != nil {
		failDeskCall(t, "rule_draft_put_rule", err)
	}
	if len(got) != 1 {
		t.Fatalf("rule_draft_put_rule(%s, create=%t) returned %d rows, want 1", r.key, create, len(got))
	}
	return got[0]
}

func mustDeskRemove(t *testing.T, app *pgxpool.Pool, actor, key string) deskRemoved {
	t.Helper()
	got, err := callDeskRemove(app, actor, key)
	if err != nil {
		failDeskCall(t, "rule_draft_remove_rule", err)
	}
	if len(got) != 1 {
		t.Fatalf("rule_draft_remove_rule(%s) returned %d rows, want 1", key, len(got))
	}
	return got[0]
}

func mustDeskPublish(t *testing.T, app *pgxpool.Pool, actor, from string) deskPublished {
	t.Helper()
	got, err := callDeskPublish(app, actor, from)
	if err != nil {
		failDeskCall(t, "rule_draft_publish", err)
	}
	if len(got) != 1 {
		t.Fatalf("rule_draft_publish(%s) returned %d rows, want 1", from, len(got))
	}
	return got[0]
}

// dbDay is today (UTC) plus offset days, read from the database clock.
func dbDay(t *testing.T, super *pgxpool.Pool, offset int) string {
	t.Helper()
	var d string
	if err := super.QueryRow(context.Background(),
		`SELECT ((now() AT TIME ZONE 'UTC')::date + $1::int)::text`, offset).Scan(&d); err != nil {
		t.Fatalf("read the database date: %v", err)
	}
	return d
}

func unsealedCount(t *testing.T, super *pgxpool.Pool) int {
	t.Helper()
	var n int
	if err := super.QueryRow(context.Background(), `SELECT count(*) FROM rule_set_versions WHERE NOT sealed`).Scan(&n); err != nil {
		t.Fatalf("count unsealed versions: %v", err)
	}
	return n
}

func ruleCountOf(t *testing.T, super *pgxpool.Pool, versionID string) int {
	t.Helper()
	var n int
	if err := super.QueryRow(context.Background(), `SELECT count(*) FROM rules WHERE rule_set_version_id = $1`, versionID).Scan(&n); err != nil {
		t.Fatalf("count rules of %s: %v", versionID, err)
	}
	return n
}

func ruleKeysOf(t *testing.T, super *pgxpool.Pool, versionID string) []string {
	t.Helper()
	rows, err := super.Query(context.Background(), `SELECT key FROM rules WHERE rule_set_version_id = $1 ORDER BY key`, versionID)
	if err != nil {
		t.Fatalf("read the rule keys of %s: %v", versionID, err)
	}
	defer rows.Close()
	var keys []string
	for rows.Next() {
		var k string
		if err := rows.Scan(&k); err != nil {
			t.Fatalf("scan a rule key: %v", err)
		}
		keys = append(keys, k)
	}
	return keys
}

// contentOf lists each rule of a version as its jsonb without the two identity columns.
func contentOf(t *testing.T, super *pgxpool.Pool, versionID string) []string {
	t.Helper()
	rows, err := super.Query(context.Background(),
		`SELECT (to_jsonb(r) - 'id' - 'rule_set_version_id')::text FROM rules r WHERE rule_set_version_id = $1 ORDER BY 1`, versionID)
	if err != nil {
		t.Fatalf("read the rule content of %s: %v", versionID, err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			t.Fatalf("scan rule content: %v", err)
		}
		out = append(out, s)
	}
	return out
}

func ruleMessage(t *testing.T, super *pgxpool.Pool, versionID, key string) (msg string, found bool) {
	t.Helper()
	rows, err := super.Query(context.Background(), `SELECT message FROM rules WHERE rule_set_version_id = $1 AND key = $2`, versionID, key)
	if err != nil {
		t.Fatalf("read the message of %s: %v", key, err)
	}
	defer rows.Close()
	for rows.Next() {
		found = true
		if err := rows.Scan(&msg); err != nil {
			t.Fatalf("scan a message: %v", err)
		}
	}
	return msg, found
}

func ruleXmin(t *testing.T, super *pgxpool.Pool, versionID, key string) string {
	t.Helper()
	var x string
	if err := super.QueryRow(context.Background(),
		`SELECT xmin::text FROM rules WHERE rule_set_version_id = $1 AND key = $2`, versionID, key).Scan(&x); err != nil {
		t.Fatalf("read xmin of %s: %v", key, err)
	}
	return x
}

// ruleMatches reports whether the version holds the key with exactly r's content and scope 'document'.
func ruleMatches(t *testing.T, super *pgxpool.Pool, versionID string, r deskRule) bool {
	t.Helper()
	var n int
	if err := super.QueryRow(context.Background(),
		`SELECT count(*) FROM rules WHERE rule_set_version_id = $1 AND key = $2 AND type = $3 AND target = $4
		   AND params = $5::jsonb AND severity = $6 AND "when" IS NOT DISTINCT FROM $7 AND message = $8
		   AND enabled = $9 AND scope = 'document'`,
		versionID, r.key, r.typ, r.target, r.params, r.severity, r.when, r.message, r.enabled).Scan(&n); err != nil {
		t.Fatalf("match the rule %s: %v", r.key, err)
	}
	return n == 1
}

// deskSnapshot maps every rules and rule_set_versions row to its jsonb.
func deskSnapshot(t *testing.T, super *pgxpool.Pool) map[string]string {
	t.Helper()
	rows, err := super.Query(context.Background(),
		`SELECT 'rule:' || id, to_jsonb(r)::text FROM rules r UNION ALL SELECT 'version:' || id, to_jsonb(v)::text FROM rule_set_versions v`)
	if err != nil {
		t.Fatalf("snapshot the rule tables: %v", err)
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			t.Fatalf("scan a snapshot row: %v", err)
		}
		out[k] = v
	}
	return out
}

func inForceAt(t *testing.T, super *pgxpool.Pool, day string) string {
	t.Helper()
	var id string
	if err := super.QueryRow(context.Background(), `SELECT rule_set_version_for($1::date)::text`, day).Scan(&id); err != nil {
		t.Fatalf("rule_set_version_for(%s): %v", day, err)
	}
	return id
}

func TestStaffDraftsFunction_OpenCopiesTheVersionInForce(t *testing.T) {
	super, app := dbTestPools(t)
	removeDeskVersionsOnCleanup(t, super)
	actor := seedRulesStaffRow(t, super)

	inForceID, inForceNo := versionInForce(t, super)
	want := contentOf(t, super, inForceID)
	if len(want) == 0 {
		t.Fatal("the version in force holds no rules: the test cannot discriminate")
	}
	if n := unsealedCount(t, super); n != 0 {
		t.Fatalf("%d unsealed versions before the open, want 0", n)
	}
	var maxVersion int
	if err := super.QueryRow(context.Background(), `SELECT max(version) FROM rule_set_versions`).Scan(&maxVersion); err != nil {
		t.Fatalf("read max(version): %v", err)
	}

	got := mustDeskOpen(t, app, actor)
	if got.version != maxVersion+1 || got.from != inForceNo {
		t.Errorf("open returned version %d from v%d, want %d from v%d", got.version, got.from, maxVersion+1, inForceNo)
	}
	if got.id == inForceID {
		t.Fatalf("the draft is the version in force %s", inForceID)
	}
	var sealed bool
	var notes string
	var version int
	if err := super.QueryRow(context.Background(),
		`SELECT sealed, notes, version FROM rule_set_versions WHERE id = $1`, got.id).Scan(&sealed, &notes, &version); err != nil {
		t.Fatalf("read the draft row %s: %v", got.id, err)
	}
	if sealed || version != got.version || notes != fmt.Sprintf("Rules desk: from v%d", inForceNo) {
		t.Errorf("draft row = sealed %t, version %d, notes %q; want unsealed, %d, %q", sealed, version, notes, got.version, fmt.Sprintf("Rules desk: from v%d", inForceNo))
	}
	if n := unsealedCount(t, super); n != 1 {
		t.Errorf("%d unsealed versions after the open, want 1", n)
	}
	if have := contentOf(t, super, got.id); !reflect.DeepEqual(have, want) {
		t.Errorf("the draft's rules differ from v%d: %d rows, want %d\nhave %v\nwant %v", inForceNo, len(have), len(want), have, want)
	}
}

func TestStaffDraftsFunction_OpenCopiesADisabledRuleDisabled(t *testing.T) {
	super, app := dbTestPools(t)
	removeDeskVersionsOnCleanup(t, super)
	restoreRulesOnCleanup(t, super)
	actor := seedRulesStaffRow(t, super)

	inForceID, _ := versionInForce(t, super)
	if !ruleEnabledActive(t, super, deskDisabledKey) || !ruleEnabledActive(t, super, deskInForceKey) {
		t.Fatal("a fixture rule starts disabled: the test cannot discriminate")
	}
	if was := switchRuleViaFunction(t, app, actor, deskDisabledKey, false); !was {
		t.Fatal("the switch-off returned was_enabled = false, want true")
	}
	if ruleEnabledActive(t, super, deskDisabledKey) {
		t.Fatalf("%s is still enabled in %s after the switch", deskDisabledKey, inForceID)
	}

	got := mustDeskOpen(t, app, actor)
	var off, control bool
	if err := super.QueryRow(context.Background(),
		`SELECT (SELECT enabled FROM rules WHERE rule_set_version_id = $1 AND key = $2),
		        (SELECT enabled FROM rules WHERE rule_set_version_id = $1 AND key = $3)`,
		got.id, deskDisabledKey, deskInForceKey).Scan(&off, &control); err != nil {
		t.Fatalf("read the draft's copies: %v", err)
	}
	if off {
		t.Errorf("the draft's copy of %s is enabled, want disabled", deskDisabledKey)
	}
	if !control {
		t.Errorf("the draft's copy of %s is disabled, want enabled (only the switched rule drops)", deskInForceKey)
	}
}

func TestStaffDraftsFunction_ConcurrentOpensLeaveOneDraft(t *testing.T) {
	super, app := dbTestPools(t)
	removeDeskVersionsOnCleanup(t, super)
	actor := seedRulesStaffRow(t, super)
	if n := unsealedCount(t, super); n != 0 {
		t.Fatalf("%d unsealed versions before the opens, want 0", n)
	}

	type result struct {
		rows []deskOpened
		err  error
	}
	results := make([]result, 2)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := range results {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			results[i].rows, results[i].err = callDeskOpen(app, actor)
		}()
	}
	close(start)
	wg.Wait()

	wins, losses := 0, 0
	for i, r := range results {
		switch {
		case r.err == nil && len(r.rows) == 1:
			wins++
		case sqlStateOf(r.err) == "23505" && pgErrOf(r.err).ConstraintName == deskOneDraftName:
			losses++
		default:
			t.Errorf("open %d: rows %v, err %v; want one row or 23505 on %s", i, r.rows, r.err, deskOneDraftName)
		}
	}
	if wins != 1 || losses != 1 {
		t.Errorf("%d opens returned a row and %d raised 23505, want 1 and 1", wins, losses)
	}
	if n := unsealedCount(t, super); n != 1 {
		t.Errorf("%d unsealed versions after the race, want 1", n)
	}
}

func TestStaffDraftsFunction_PutAddsChangesAndNoOps(t *testing.T) {
	super, app := dbTestPools(t)
	removeDeskVersionsOnCleanup(t, super)
	actor := seedRulesStaffRow(t, super)

	draft := mustDeskOpen(t, app, actor)
	r := deskTestRule(deskNewKey, "first")
	if _, found := ruleMessage(t, super, draft.id, r.key); found {
		t.Fatalf("%s is already in the draft: the test cannot discriminate", r.key)
	}

	created := mustDeskPut(t, app, actor, r, true)
	if created.id != draft.id || created.version != draft.version || created.existed || !created.change {
		t.Errorf("create of a new key returned %+v, want draft %s v%d, existed false, changed true", created, draft.id, draft.version)
	}
	if !ruleMatches(t, super, draft.id, r) {
		t.Error("the created rule is absent or its columns differ from the call")
	}

	again := r
	again.message = "second"
	xmin := ruleXmin(t, super, draft.id, r.key)
	dup := mustDeskPut(t, app, actor, again, true)
	if !dup.existed || dup.change {
		t.Errorf("create of a key already in the draft returned %+v, want existed true, changed false", dup)
	}
	if msg, _ := ruleMessage(t, super, draft.id, r.key); msg != "first" {
		t.Errorf("create of an existing key changed the message to %q, want first", msg)
	}
	if x := ruleXmin(t, super, draft.id, r.key); x != xmin {
		t.Errorf("create of an existing key rewrote the row: xmin %s -> %s", xmin, x)
	}

	edit := mustDeskPut(t, app, actor, again, false)
	if edit.id != draft.id || !edit.existed || !edit.change {
		t.Errorf("edit of an existing key returned %+v, want existed true, changed true", edit)
	}
	if !ruleMatches(t, super, draft.id, again) {
		t.Error("the edited rule does not hold the new message")
	}

	xmin = ruleXmin(t, super, draft.id, r.key)
	same := mustDeskPut(t, app, actor, again, false)
	if !same.existed || same.change {
		t.Errorf("edit with identical content returned %+v, want existed true, changed false", same)
	}
	if x := ruleXmin(t, super, draft.id, r.key); x != xmin {
		t.Errorf("an identical edit rewrote the row: xmin %s -> %s", xmin, x)
	}

	absent := deskTestRule("desk-absent-rule", "none")
	miss := mustDeskPut(t, app, actor, absent, false)
	if miss.existed || miss.change {
		t.Errorf("edit of an absent key returned %+v, want existed false, changed false", miss)
	}
	if _, found := ruleMessage(t, super, draft.id, absent.key); found {
		t.Error("edit of an absent key inserted a row")
	}
}

func TestStaffDraftsFunction_NoDraftReturnsZeroRows(t *testing.T) {
	super, app := dbTestPools(t)
	removeDeskVersionsOnCleanup(t, super)
	actor := seedRulesStaffRow(t, super)
	if n := unsealedCount(t, super); n != 0 {
		t.Fatalf("%d unsealed versions, want 0: the test cannot discriminate", n)
	}
	before := deskSnapshot(t, super)
	if len(before) == 0 {
		t.Fatal("no rule rows to snapshot")
	}

	r := deskTestRule(deskNewKey, "none")
	for _, create := range []bool{true, false} {
		got, err := callDeskPut(app, actor, r, create)
		if err != nil {
			failDeskCall(t, "rule_draft_put_rule", err)
		}
		if len(got) != 0 {
			t.Errorf("put (create=%t) with no draft returned %v, want zero rows", create, got)
		}
	}
	rm, err := callDeskRemove(app, actor, deskInForceKey)
	if err != nil {
		failDeskCall(t, "rule_draft_remove_rule", err)
	}
	if len(rm) != 0 {
		t.Errorf("remove with no draft returned %v, want zero rows", rm)
	}
	pub, err := callDeskPublish(app, actor, deskFarDate)
	if err != nil {
		failDeskCall(t, "rule_draft_publish", err)
	}
	if len(pub) != 0 {
		t.Errorf("publish with no draft returned %v, want zero rows", pub)
	}
	if after := deskSnapshot(t, super); !reflect.DeepEqual(after, before) {
		t.Error("a call with no draft changed a rule or version row")
	}

	draft := mustDeskOpen(t, app, actor)
	if got := mustDeskPut(t, app, actor, r, true); got.id != draft.id {
		t.Errorf("control: put on an open draft returned %+v, want draft %s", got, draft.id)
	}
}

func TestStaffDraftsFunction_RemoveDeletesOnlyFromTheDraft(t *testing.T) {
	super, app := dbTestPools(t)
	removeDeskVersionsOnCleanup(t, super)
	actor := seedRulesStaffRow(t, super)

	inForceID, _ := versionInForce(t, super)
	draft := mustDeskOpen(t, app, actor)
	if _, found := ruleMessage(t, super, draft.id, deskInForceKey); !found {
		t.Fatalf("%s is not in the draft: the test cannot discriminate", deskInForceKey)
	}
	if _, found := ruleMessage(t, super, inForceID, deskInForceKey); !found {
		t.Fatalf("%s is not in the version in force: the test cannot discriminate", deskInForceKey)
	}

	got := mustDeskRemove(t, app, actor, deskInForceKey)
	if got.id != draft.id || got.version != draft.version || !got.removed {
		t.Errorf("remove returned %+v, want draft %s v%d, removed true", got, draft.id, draft.version)
	}
	if _, found := ruleMessage(t, super, draft.id, deskInForceKey); found {
		t.Error("the rule is still in the draft")
	}
	if _, found := ruleMessage(t, super, inForceID, deskInForceKey); !found {
		t.Error("the rule is gone from the version in force")
	}
	if again := mustDeskRemove(t, app, actor, deskInForceKey); again.removed {
		t.Errorf("remove of an absent key returned %+v, want removed false", again)
	}
}

func TestStaffDraftsFunction_PublishSealsAndDatesInOneStatement(t *testing.T) {
	super, app := dbTestPools(t)
	removeDeskVersionsOnCleanup(t, super)
	actor := seedRulesStaffRow(t, super)

	draft := mustDeskOpen(t, app, actor)
	rules := ruleCountOf(t, super, draft.id)
	if rules == 0 {
		t.Fatal("the draft holds no rules: the test cannot discriminate")
	}
	var started string
	if err := super.QueryRow(context.Background(), `SELECT clock_timestamp()::text`).Scan(&started); err != nil {
		t.Fatalf("read the database clock: %v", err)
	}

	got := mustDeskPublish(t, app, actor, deskFarDate)
	if got.id != draft.id || got.version != draft.version || got.count != rules {
		t.Errorf("publish returned %+v, want draft %s v%d with %d rules", got, draft.id, draft.version, rules)
	}
	var sealed, stamped bool
	var from string
	if err := super.QueryRow(context.Background(),
		`SELECT sealed, effective_from::text, published_at >= $2::timestamptz FROM rule_set_versions WHERE id = $1`,
		draft.id, started).Scan(&sealed, &from, &stamped); err != nil {
		t.Fatalf("read the published row: %v", err)
	}
	if !sealed || from != deskFarDate || !stamped {
		t.Errorf("published row = sealed %t, effective_from %s, published_at >= call start %t; want true, %s, true", sealed, from, stamped, deskFarDate)
	}
}

func TestStaffDraftsFunction_PublishRefusesAPastDateAndAnEmptyDraft(t *testing.T) {
	super, app := dbTestPools(t)
	removeDeskVersionsOnCleanup(t, super)
	actor := seedRulesStaffRow(t, super)

	draft := mustDeskOpen(t, app, actor)
	keys := ruleKeysOf(t, super, draft.id)
	if len(keys) == 0 {
		t.Fatal("the draft holds no rules: the test cannot discriminate")
	}
	requireStillDraft := func(what string) {
		t.Helper()
		var sealed bool
		var from *string
		if err := super.QueryRow(context.Background(),
			`SELECT sealed, effective_from::text FROM rule_set_versions WHERE id = $1`, draft.id).Scan(&sealed, &from); err != nil {
			t.Fatalf("read the draft after %s: %v", what, err)
		}
		if sealed || from != nil {
			t.Errorf("after %s the draft is sealed %t, effective_from %v; want unsealed and undated", what, sealed, from)
		}
	}

	_, err := callDeskPublish(app, actor, dbDay(t, super, -1))
	if sqlStateOf(err) != "22023" {
		t.Errorf("publish for yesterday: SQLSTATE %q, want 22023 (err %v)", sqlStateOf(err), err)
	}
	requireStillDraft("a past date")

	for _, k := range keys {
		if got := mustDeskRemove(t, app, actor, k); !got.removed {
			t.Fatalf("remove %s returned %+v, want removed true", k, got)
		}
	}
	if n := ruleCountOf(t, super, draft.id); n != 0 {
		t.Fatalf("the draft still holds %d rules after removing every key", n)
	}
	_, err = callDeskPublish(app, actor, deskFarDate)
	if sqlStateOf(err) != "23514" {
		t.Errorf("publish of an empty draft: SQLSTATE %q, want 23514 (err %v)", sqlStateOf(err), err)
	}
	requireStillDraft("an empty publish")

	mustDeskPut(t, app, actor, deskTestRule(deskNewKey, "boundary"), true)
	today := dbDay(t, super, 0)
	got := mustDeskPublish(t, app, actor, today)
	if got.id != draft.id || got.count != 1 {
		t.Errorf("publish for today returned %+v, want draft %s with 1 rule", got, draft.id)
	}
}

func TestStaffDraftsFunction_PublishedTodayIsInForce(t *testing.T) {
	super, app := dbTestPools(t)
	removeDeskVersionsOnCleanup(t, super)
	actor := seedRulesStaffRow(t, super)
	today := dbDay(t, super, 0)

	before := inForceAt(t, super, today)
	draft := mustDeskOpen(t, app, actor)
	if before == draft.id {
		t.Fatal("the draft is in force before it is published")
	}
	mustDeskPut(t, app, actor, deskTestRule(deskNewKey, "extra"), true)
	mustDeskPublish(t, app, actor, today)

	if got := inForceAt(t, super, today); got != draft.id {
		t.Errorf("rule_set_version_for(today) = %s, want the published draft %s", got, draft.id)
	}
	var rechecks int
	if err := super.QueryRow(context.Background(),
		`SELECT count(*) FROM rule_set_version_rechecks WHERE rule_set_version_id = $1`, draft.id).Scan(&rechecks); err != nil {
		t.Fatalf("count rechecks: %v", err)
	}
	if rechecks != 0 {
		t.Errorf("%d rule_set_version_rechecks rows for the published version, want 0", rechecks)
	}
}

func TestStaffDraftsFunction_PublishedLaterIsScheduled(t *testing.T) {
	super, app := dbTestPools(t)
	removeDeskVersionsOnCleanup(t, super)
	actor := seedRulesStaffRow(t, super)
	today := dbDay(t, super, 0)

	before := inForceAt(t, super, today)
	draft := mustDeskOpen(t, app, actor)
	mustDeskPublish(t, app, actor, deskFarDate)

	if got := inForceAt(t, super, today); got != before {
		t.Errorf("rule_set_version_for(today) = %s after a future publish, want %s unchanged", got, before)
	}
	if got := inForceAt(t, super, deskFarDate); got != draft.id {
		t.Errorf("rule_set_version_for(%s) = %s, want the published draft %s", deskFarDate, got, draft.id)
	}
}

func TestStaffDraftsFunction_PublishedVersionIsImmutable(t *testing.T) {
	super, app := dbTestPools(t)
	removeDeskVersionsOnCleanup(t, super)
	actor := seedRulesStaffRow(t, super)
	ctx := context.Background()

	draft := mustDeskOpen(t, app, actor)
	mustDeskPublish(t, app, actor, deskFarDate)
	if n := ruleCountOf(t, super, draft.id); n == 0 {
		t.Fatal("the published version holds no rules: the test cannot discriminate")
	}
	if n := unsealedCount(t, super); n != 0 {
		t.Fatalf("%d unsealed versions after the publish, want 0", n)
	}

	r := deskTestRule(deskNewKey, "late")
	for _, create := range []bool{true, false} {
		got, err := callDeskPut(app, actor, r, create)
		if err != nil || len(got) != 0 {
			t.Errorf("put (create=%t) after the publish returned %v, err %v, want zero rows", create, got, err)
		}
	}
	if got, err := callDeskRemove(app, actor, deskInForceKey); err != nil || len(got) != 0 {
		t.Errorf("remove after the publish returned %v, err %v, want zero rows", got, err)
	}
	if _, found := ruleMessage(t, super, draft.id, deskNewKey); found {
		t.Error("a put after the publish inserted a rule")
	}
	if _, found := ruleMessage(t, super, draft.id, deskInForceKey); !found {
		t.Error("a remove after the publish deleted a rule")
	}

	_, err := super.Exec(ctx, `UPDATE rules SET message = 'x' WHERE rule_set_version_id = $1 AND key = $2`, draft.id, deskInForceKey)
	if sqlStateOf(err) != "23001" {
		t.Errorf("owner UPDATE of a published rule: SQLSTATE %q, want 23001 (err %v)", sqlStateOf(err), err)
	}
	_, err = super.Exec(ctx, `UPDATE rule_set_versions SET effective_from = '3002-01-01' WHERE id = $1`, draft.id)
	if sqlStateOf(err) != "23001" {
		t.Errorf("owner UPDATE of effective_from: SQLSTATE %q, want 23001 (err %v)", sqlStateOf(err), err)
	}
}

func TestStaffDraftsFunction_OtherVersionsUntouched(t *testing.T) {
	super, app := dbTestPools(t)
	removeDeskVersionsOnCleanup(t, super)
	actor := seedRulesStaffRow(t, super)

	before := deskSnapshot(t, super)
	if len(before) == 0 {
		t.Fatal("no rule rows to snapshot")
	}
	draft := mustDeskOpen(t, app, actor)
	mustDeskPut(t, app, actor, deskTestRule(deskNewKey, "added"), true)
	mustDeskPut(t, app, actor, deskTestRule(deskNewKey, "edited"), false)
	if got := mustDeskRemove(t, app, actor, deskInForceKey); !got.removed {
		t.Fatalf("remove returned %+v, want removed true", got)
	}
	mustDeskPublish(t, app, actor, deskFarDate)

	after := deskSnapshot(t, super)
	var changed []string
	for k, v := range before {
		if after[k] != v {
			changed = append(changed, k)
		}
	}
	sort.Strings(changed)
	if len(changed) != 0 {
		t.Errorf("%d rows of other versions changed in a session on %s: %v", len(changed), draft.id, changed)
	}
}

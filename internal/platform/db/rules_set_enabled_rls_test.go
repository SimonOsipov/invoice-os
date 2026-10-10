// set_rule_enabled: the SECURITY DEFINER switch for the rule in force. Tests that flip a rule
// snapshot every rules.enabled and restore it in t.Cleanup before the first write.
package db_test

import (
	"context"
	"fmt"
	"io/fs"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/SimonOsipov/invoice-os/migrations"
)

const (
	setRuleEnabledGlob = "*_rules_set_enabled.sql"
	setRuleEnabledSQL  = `SELECT rule_set_version_id::text, version, was_enabled FROM public.set_rule_enabled($1::uuid, $2, $3)`
	setRuleEnabledSig  = `public.set_rule_enabled(uuid, text, boolean)`
	inForceSQL         = `SELECT public.rule_set_version_for((now() AT TIME ZONE 'UTC')::date)::text`
	killKey            = "no-duplicate-line-items"
)

type ruleSwitched struct {
	versionID string
	version   int
	was       bool
}

type rowsQuerier interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}

// callSetRuleEnabled returns the function's rows, or the call's error.
func callSetRuleEnabled(q rowsQuerier, actor, key, enabled any) ([]ruleSwitched, error) {
	rows, err := q.Query(context.Background(), setRuleEnabledSQL, actor, key, enabled)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ruleSwitched
	for rows.Next() {
		var r ruleSwitched
		if err := rows.Scan(&r.versionID, &r.version, &r.was); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// mustSwitch calls the function and fails the test when the call errors or does not return one row.
func mustSwitch(t *testing.T, q rowsQuerier, actor, key string, enabled bool) ruleSwitched {
	t.Helper()
	got, err := callSetRuleEnabled(q, actor, key, enabled)
	if err != nil {
		t.Fatalf("set_rule_enabled(%s, %t) as the app: %v (is the migration applied?)", key, enabled, err)
	}
	if len(got) != 1 {
		t.Fatalf("set_rule_enabled(%s, %t) returned %d rows, want 1", key, enabled, len(got))
	}
	return got[0]
}

func rulesEnabledSnapshot(t *testing.T, q rowsQuerier) map[string]bool {
	t.Helper()
	rows, err := q.Query(context.Background(), `SELECT id::text, enabled FROM public.rules`)
	if err != nil {
		t.Fatalf("read rules.enabled: %v", err)
	}
	defer rows.Close()
	got := map[string]bool{}
	for rows.Next() {
		var id string
		var on bool
		if err := rows.Scan(&id, &on); err != nil {
			t.Fatalf("scan rules row: %v", err)
		}
		got[id] = on
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate rules rows: %v", err)
	}
	return got
}

// restoreRulesEnabledOnCleanup snapshots rules.enabled and restores any drift as the superuser. Call it before the first write.
func restoreRulesEnabledOnCleanup(t *testing.T) map[string]bool {
	t.Helper()
	snap := rulesEnabledSnapshot(t, h.super)
	if len(snap) == 0 {
		t.Fatal("no rules rows to snapshot")
	}
	t.Cleanup(func() {
		for id, want := range snap {
			if _, err := h.super.Exec(context.Background(),
				`UPDATE public.rules SET enabled = $1 WHERE id = $2 AND enabled IS DISTINCT FROM $1`, want, id); err != nil {
				t.Errorf("cleanup: restore rules.id=%s enabled=%t: %v", id, want, err)
			}
		}
	})
	return snap
}

func versionInForce(t *testing.T, q interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}) string {
	t.Helper()
	var id string
	if err := q.QueryRow(context.Background(), inForceSQL).Scan(&id); err != nil {
		t.Fatalf("rule_set_version_for(today): %v", err)
	}
	return id
}

func versionNumberOf(t *testing.T, versionID string) int {
	t.Helper()
	var n int
	if err := h.super.QueryRow(context.Background(),
		`SELECT version FROM public.rule_set_versions WHERE id = $1`, versionID).Scan(&n); err != nil {
		t.Fatalf("read the version number of %s: %v", versionID, err)
	}
	return n
}

// ruleEnabledAt reads rules.enabled for key in one version as the superuser.
func ruleEnabledAt(t *testing.T, versionID, key string) bool {
	t.Helper()
	var on bool
	if err := h.super.QueryRow(context.Background(),
		`SELECT enabled FROM public.rules WHERE rule_set_version_id = $1 AND key = $2`, versionID, key).Scan(&on); err != nil {
		t.Fatalf("read rules.enabled for %s in %s: %v", key, versionID, err)
	}
	return on
}

// requireKeyEnabledInForce is the non-vacuous start: the key is enabled in the version in force.
func requireKeyEnabledInForce(t *testing.T, key string) string {
	t.Helper()
	ver := versionInForce(t, h.super)
	if !ruleEnabledAt(t, ver, key) {
		t.Fatalf("%s starts disabled in the version in force: the test cannot discriminate", key)
	}
	return ver
}

func requireRulesUnchanged(t *testing.T, want map[string]bool, what string) {
	t.Helper()
	got := rulesEnabledSnapshot(t, h.super)
	if !reflect.DeepEqual(got, want) {
		t.Errorf("rules.enabled changed after %s", what)
	}
}

// AC1.
func TestRLS_SetRuleEnabledFlipsTheRuleInForce(t *testing.T) {
	requireHarness(t)
	restoreRulesEnabledOnCleanup(t)
	actor := uuid.NewString()
	seedRulesStaff(t, actor)
	ver := requireKeyEnabledInForce(t, killKey)
	verNo := versionNumberOf(t, ver)

	off := mustSwitch(t, h.app, actor, killKey, false)
	if off.versionID != ver || off.version != verNo || !off.was {
		t.Errorf("disable returned %+v, want version id %s, number %d, was_enabled true", off, ver, verNo)
	}
	if ruleEnabledAt(t, ver, killKey) {
		t.Error("owner read after the disable: enabled = true, want false")
	}

	on := mustSwitch(t, h.app, actor, killKey, true)
	if on.versionID != ver || on.version != verNo || on.was {
		t.Errorf("restore returned %+v, want version id %s, number %d, was_enabled false", on, ver, verNo)
	}
	if !ruleEnabledAt(t, ver, killKey) {
		t.Error("owner read after the restore: enabled = false, want true")
	}
}

// AC3: a temp rule_set_versions made by invoice_app cannot redirect the flip.
func TestRLS_SetRuleEnabledIgnoresATempTableShadow(t *testing.T) {
	requireHarness(t)
	ctx := context.Background()
	restoreRulesEnabledOnCleanup(t)
	actor := uuid.NewString()
	seedRulesStaff(t, actor)
	ver := requireKeyEnabledInForce(t, killKey)

	var v1 string
	if err := h.super.QueryRow(ctx, `SELECT v.id::text FROM public.rule_set_versions v JOIN public.rules r
	                                 ON r.rule_set_version_id = v.id AND r.key = $1
	                                 WHERE v.version < $2 ORDER BY v.version LIMIT 1`, killKey, versionNumberOf(t, ver)).Scan(&v1); err != nil {
		t.Fatalf("the superseded v1 row of %s: %v", killKey, err)
	}
	if v1 == ver {
		t.Fatalf("v1 is the version in force: the shadow cannot discriminate")
	}
	if !ruleEnabledAt(t, v1, killKey) {
		t.Fatalf("v1 row of %s starts disabled: the test cannot discriminate", killKey)
	}

	tx, err := h.app.Begin(ctx)
	if err != nil {
		t.Fatalf("begin app tx: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	for _, s := range []string{
		fmt.Sprintf(`CREATE TEMP TABLE rule_set_versions AS SELECT * FROM public.rule_set_versions WHERE id = '%s'`, v1),
		`UPDATE rule_set_versions SET effective_from = DATE '2000-01-01'`,
		`GRANT ALL ON pg_temp.rule_set_versions TO invoice_migrator`,
	} {
		if _, err := tx.Exec(ctx, s); err != nil {
			t.Fatalf("shadow setup (%s): %v", s, err)
		}
	}
	mustSwitch(t, tx, actor, killKey, false)

	var inForceOn, v1On bool
	if err := tx.QueryRow(ctx, `SELECT
	    (SELECT enabled FROM public.rules WHERE rule_set_version_id = $1 AND key = $3),
	    (SELECT enabled FROM public.rules WHERE rule_set_version_id = $2 AND key = $3)`, ver, v1, killKey).Scan(&inForceOn, &v1On); err != nil {
		t.Fatalf("read both rows: %v", err)
	}
	if inForceOn {
		t.Error("the in-force row is still enabled: the flip missed the version in force")
	}
	if !v1On {
		t.Error("the superseded v1 row was disabled: a temp table redirected the flip")
	}

	var cfg []string
	if err := h.super.QueryRow(ctx, `SELECT proconfig FROM pg_proc WHERE oid = to_regprocedure($1)`, setRuleEnabledSig).Scan(&cfg); err != nil {
		t.Fatalf("read proconfig: %v", err)
	}
	if want := []string{"search_path=pg_catalog, public, pg_temp"}; !reflect.DeepEqual(cfg, want) {
		t.Errorf("proconfig = %v, want %v", cfg, want)
	}
}

// AC5: after a successful call, invoice_app still has no UPDATE on rules.
func TestRLS_SetRuleEnabledLeavesTheAppWithoutUpdate(t *testing.T) {
	requireHarness(t)
	ctx := context.Background()
	restoreRulesEnabledOnCleanup(t)
	actor := uuid.NewString()
	seedRulesStaff(t, actor)
	requireKeyEnabledInForce(t, killKey)

	mustSwitch(t, h.app, actor, killKey, false)
	mustSwitch(t, h.app, actor, killKey, true)

	var col, tbl bool
	if err := h.super.QueryRow(ctx, `SELECT
	    has_column_privilege('invoice_app', 'public.rules', 'enabled', 'UPDATE'),
	    has_table_privilege('invoice_app', 'public.rules', 'UPDATE')`).Scan(&col, &tbl); err != nil {
		t.Fatalf("read the app's privileges: %v", err)
	}
	if col || tbl {
		t.Errorf("invoice_app UPDATE on rules.enabled = %t, on rules = %t, want both false", col, tbl)
	}
	_, err := h.app.Exec(ctx, `UPDATE public.rules SET enabled = true`)
	if pgCode(err) != "42501" {
		t.Errorf("direct UPDATE rules SET enabled as invoice_app: SQLSTATE %q, want 42501: %v", pgCode(err), err)
	}
}

// AC6.
func TestRLS_SetRuleEnabledRefusesAnActorWithoutTheRulesRole(t *testing.T) {
	requireHarness(t)
	snap := restoreRulesEnabledOnCleanup(t)
	requireKeyEnabledInForce(t, killKey)

	plain := uuid.NewString()
	seedStaff(t, plain)
	requireRulesRoleFalse(t, plain)
	granted := uuid.NewString()
	seedRulesStaff(t, granted)

	for name, actor := range map[string]any{"no staff row": uuid.NewString(), "rules_role false": plain, "NULL actor": nil} {
		got, err := callSetRuleEnabled(h.app, actor, killKey, false)
		if pgCode(err) != "42501" {
			t.Errorf("%s: SQLSTATE %q, want 42501 (err %v, rows %v)", name, pgCode(err), err, got)
		}
		requireRulesUnchanged(t, snap, name)
	}

	if got := mustSwitch(t, h.app, granted, killKey, false); !got.was {
		t.Errorf("control: a rules-role actor got %+v, want was_enabled true", got)
	}
}

// AC6: the role is read on every call, so a revoke between two calls of one tx refuses the second.
func TestRLS_SetRuleEnabledRefusesAnActorRevokedMidSession(t *testing.T) {
	requireHarness(t)
	ctx := context.Background()
	restoreRulesEnabledOnCleanup(t)
	actor := uuid.NewString()
	seedRulesStaff(t, actor)
	ver := requireKeyEnabledInForce(t, killKey)

	tx, err := h.app.Begin(ctx)
	if err != nil {
		t.Fatalf("begin app tx: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	mustSwitch(t, tx, actor, killKey, false)
	if _, err := h.super.Exec(ctx, `UPDATE public.staff_members SET rules_role = false WHERE user_id = $1`, actor); err != nil {
		t.Fatalf("revoke the rules role: %v", err)
	}
	_, err = callSetRuleEnabled(tx, actor, killKey, true)
	if pgCode(err) != "42501" {
		t.Errorf("call after the revoke: SQLSTATE %q, want 42501: %v", pgCode(err), err)
	}
	_ = tx.Rollback(ctx)
	if !ruleEnabledAt(t, ver, killKey) {
		t.Error("the rolled-back tx left the rule disabled")
	}
}

// AC6.
func TestRLS_SetRuleEnabledExecuteGrants(t *testing.T) {
	requireHarness(t)
	ctx := context.Background()

	var exists bool
	if err := h.super.QueryRow(ctx, `SELECT to_regprocedure($1) IS NOT NULL`, setRuleEnabledSig).Scan(&exists); err != nil {
		t.Fatalf("to_regprocedure: %v", err)
	}
	if !exists {
		t.Fatalf("%s does not exist: the migration is not applied", setRuleEnabledSig)
	}
	var app, reader bool
	if err := h.super.QueryRow(ctx, `SELECT has_function_privilege('invoice_app', $1, 'EXECUTE'),
	                                        has_function_privilege('invoice_tenant_reader', $1, 'EXECUTE')`, setRuleEnabledSig).Scan(&app, &reader); err != nil {
		t.Fatalf("has_function_privilege: %v", err)
	}
	if !app || reader {
		t.Errorf("EXECUTE: invoice_app = %t, invoice_tenant_reader = %t, want true and false", app, reader)
	}

	grantees := collectStrings(t, `SELECT CASE WHEN a.grantee = 0 THEN '=PUBLIC' ELSE a.grantee::regrole::text END
	                                 FROM pg_proc p, aclexplode(p.proacl) a
	                                WHERE p.oid = to_regprocedure($1) AND a.privilege_type = 'EXECUTE' ORDER BY 1`, setRuleEnabledSig)
	if len(grantees) == 0 {
		t.Fatal("proacl lists no EXECUTE grantee, so a PUBLIC default would hide here")
	}
	if want := []string{"invoice_app", "invoice_migrator"}; !reflect.DeepEqual(grantees, want) {
		t.Errorf("EXECUTE grantees = %v, want %v (no PUBLIC)", grantees, want)
	}

	_, err := callSetRuleEnabled(h.reader, uuid.NewString(), killKey, true)
	if pgCode(err) != "42501" {
		t.Errorf("call on the reader pool: SQLSTATE %q, want 42501: %v", pgCode(err), err)
	}
}

// AC7.
func TestRLS_SetRuleEnabledUnknownKeyAndNoOp(t *testing.T) {
	requireHarness(t)
	snap := restoreRulesEnabledOnCleanup(t)
	actor := uuid.NewString()
	seedRulesStaff(t, actor)
	ver := requireKeyEnabledInForce(t, killKey)

	got, err := callSetRuleEnabled(h.app, actor, "no-such-rule", false)
	if err != nil {
		t.Fatalf("unknown key: %v (is the migration applied?)", err)
	}
	if len(got) != 0 {
		t.Errorf("unknown key returned %d rows, want 0", len(got))
	}
	requireRulesUnchanged(t, snap, "an unknown key")

	if got, err := callSetRuleEnabled(h.app, actor, nil, false); err != nil || len(got) != 0 {
		t.Errorf("NULL key returned %v, err %v, want zero rows", got, err)
	}
	requireRulesUnchanged(t, snap, "a NULL key")

	if got, err := callSetRuleEnabled(h.app, actor, killKey, nil); err == nil && len(got) != 0 {
		t.Errorf("NULL enabled returned %v with no error, want an error or zero rows", got)
	}
	requireRulesUnchanged(t, snap, "a NULL enabled")

	var xminBefore string
	if err := h.super.QueryRow(context.Background(),
		`SELECT xmin::text FROM public.rules WHERE rule_set_version_id = $1 AND key = $2`, ver, killKey).Scan(&xminBefore); err != nil {
		t.Fatalf("read xmin: %v", err)
	}
	same := mustSwitch(t, h.app, actor, killKey, true)
	if !same.was || same.versionID != ver {
		t.Errorf("enable of an enabled rule returned %+v, want was_enabled true in %s", same, ver)
	}
	requireRulesUnchanged(t, snap, "enabling an enabled rule")
	var xminAfter string
	if err := h.super.QueryRow(context.Background(),
		`SELECT xmin::text FROM public.rules WHERE rule_set_version_id = $1 AND key = $2`, ver, killKey).Scan(&xminAfter); err != nil {
		t.Fatalf("re-read xmin: %v", err)
	}
	if xminAfter != xminBefore {
		t.Errorf("a no-op request rewrote the row: xmin %s -> %s", xminBefore, xminAfter)
	}

	mustSwitch(t, h.app, actor, killKey, false)
	again := mustSwitch(t, h.app, actor, killKey, false)
	if again.was {
		t.Errorf("disable of a disabled rule returned was_enabled true, want false (the request)")
	}
	if ruleEnabledAt(t, ver, killKey) {
		t.Error("disable of a disabled rule left enabled = true")
	}
}

// AC7: a body that finds no version in force makes the function return nothing.
func TestRLS_SetRuleEnabledNoVersionInForceReturnsZeroRows(t *testing.T) {
	requireHarness(t)
	ctx := context.Background()
	snap := restoreRulesEnabledOnCleanup(t)
	requireKeyEnabledInForce(t, killKey)

	tx := revokeProbeTx(t)
	actor := uuid.NewString()
	if _, err := tx.Exec(ctx,
		`CREATE OR REPLACE FUNCTION public.rule_set_version_for(d date) RETURNS uuid LANGUAGE sql STABLE STRICT AS 'SELECT NULL::uuid'`); err != nil {
		t.Fatalf("replace rule_set_version_for: %v", err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO public.staff_members (user_id, rules_role) VALUES ($1, true)`, actor); err != nil {
		t.Fatalf("insert a rules-role staff row: %v", err)
	}
	if _, err := tx.Exec(ctx, `SET LOCAL ROLE invoice_app`); err != nil {
		t.Fatalf("SET LOCAL ROLE invoice_app: %v", err)
	}
	got, err := callSetRuleEnabled(tx, actor, killKey, false)
	if err != nil {
		t.Fatalf("set_rule_enabled with no version in force: %v (is the migration applied?)", err)
	}
	if len(got) != 0 {
		t.Errorf("returned %d rows, want 0", len(got))
	}
	if _, err := tx.Exec(ctx, `RESET ROLE`); err != nil {
		t.Fatalf("RESET ROLE: %v", err)
	}
	if after := rulesEnabledSnapshot(t, tx); !reflect.DeepEqual(after, snap) {
		t.Error("a rules row changed although no version is in force")
	}
}

// AC7: a second flip waits on the first one's row lock and then sees the new state.
func TestRLS_SetRuleEnabledSerialisesConcurrentFlips(t *testing.T) {
	requireHarness(t)
	ctx := context.Background()
	restoreRulesEnabledOnCleanup(t)
	actor := uuid.NewString()
	seedRulesStaff(t, actor)
	requireKeyEnabledInForce(t, killKey)

	txA, err := h.app.Begin(ctx)
	if err != nil {
		t.Fatalf("begin tx A: %v", err)
	}
	defer func() { _ = txA.Rollback(ctx) }()
	first := mustSwitch(t, txA, actor, killKey, false)

	type result struct {
		sw  []ruleSwitched
		err error
	}
	done := make(chan result, 1)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		sw, err := callSetRuleEnabled(h.app, actor, killKey, false)
		done <- result{sw, err}
	}()

	// Hold A open until B waits on A's lock (or B finishes without waiting: no lock taken).
	var second result
	got := false
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline) && !got; {
		select {
		case second = <-done:
			got = true
		case <-time.After(100 * time.Millisecond):
			if mustCount(t, h.super, `SELECT count(*) FROM pg_stat_activity
			                          WHERE wait_event_type = 'Lock' AND query LIKE '%set_rule_enabled%'`) > 0 {
				if err := txA.Commit(ctx); err != nil {
					t.Fatalf("commit tx A: %v", err)
				}
				second, got = <-done, true
			}
		}
	}
	if !got {
		t.Fatal("the second flip neither waited nor finished in 10s")
	}
	_ = txA.Commit(ctx)
	wg.Wait()
	if second.err != nil || len(second.sw) != 1 {
		t.Fatalf("second flip = %+v, err %v, want one row", second.sw, second.err)
	}
	if !first.was || second.sw[0].was {
		t.Errorf("was_enabled: first %t, second %t, want true and false (exactly one sees the old state)", first.was, second.sw[0].was)
	}
}

// AC8.
func TestRLS_SetRuleEnabledDownAndUp(t *testing.T) {
	requireHarness(t)
	ctx := context.Background()
	matches, err := fs.Glob(migrations.FS, setRuleEnabledGlob)
	if err != nil || len(matches) != 1 {
		t.Fatalf("glob %s in migrations.FS = %v (err %v), want exactly one file: the migration is not shipped", setRuleEnabledGlob, matches, err)
	}
	up := auditEntitySectionOf(t, matches[0], "Up")
	down := shippedDownStatements(t, setRuleEnabledGlob)
	if len(down) == 0 {
		t.Fatalf("Down of %s holds no statement", matches[0])
	}

	present := func(tx pgx.Tx, when string) bool {
		var ok bool
		if err := tx.QueryRow(ctx, `SELECT to_regprocedure($1) IS NOT NULL`, setRuleEnabledSig).Scan(&ok); err != nil {
			t.Fatalf("to_regprocedure %s: %v", when, err)
		}
		return ok
	}
	tx := revokeProbeTx(t)
	if _, err := tx.Exec(ctx, `SET LOCAL ROLE invoice_migrator`); err != nil {
		t.Fatalf("SET LOCAL ROLE invoice_migrator: %v", err)
	}
	if !present(tx, "before the Down") {
		t.Fatal("the function is absent before the Down: is the migration applied?")
	}
	for _, s := range down {
		if _, err := tx.Exec(ctx, s); err != nil {
			t.Fatalf("shipped Down statement %q: %v", s, err)
		}
	}
	if present(tx, "after the Down") {
		t.Error("the function is still present after the Down")
	}
	if _, err := tx.Exec(ctx, up); err != nil {
		t.Fatalf("shipped Up as the migrator: %v", err)
	}
	if !present(tx, "after the Up") {
		t.Fatal("the function is absent after the Up")
	}
	var app, reader bool
	if err := tx.QueryRow(ctx, `SELECT has_function_privilege('invoice_app', $1, 'EXECUTE'),
	                                   has_function_privilege('invoice_tenant_reader', $1, 'EXECUTE')`, setRuleEnabledSig).Scan(&app, &reader); err != nil {
		t.Fatalf("has_function_privilege: %v", err)
	}
	if !app || reader {
		t.Errorf("after the Up: EXECUTE invoice_app = %t, invoice_tenant_reader = %t, want true and false", app, reader)
	}
}

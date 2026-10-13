// rule_draft_*: the SECURITY DEFINER write path of the rules desk. Every test seeds its staff row in an
// owner transaction, switches to invoice_app inside it and rolls it back, so a killed run leaves no draft.
package db_test

import (
	"context"
	"errors"
	"io/fs"
	"reflect"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/SimonOsipov/invoice-os/migrations"
)

const (
	ruleDraftsGlob    = "*_rule_set_drafts.sql"
	ruleDraftIndex    = "rule_set_versions_one_draft"
	ruleDraftOpenSQL  = `SELECT rule_set_version_id::text, version, from_version FROM public.rule_draft_open($1::uuid)`
	ruleDraftSearch   = "search_path=pg_catalog, public, pg_temp"
	ruleDraftStateSQL = `SELECT (SELECT count(*) FROM public.rule_set_versions)::text || '/' || (SELECT count(*) FROM public.rules)::text
	    || '/' || coalesce((SELECT md5(string_agg(to_jsonb(v)::text, '' ORDER BY v.id)) FROM public.rule_set_versions v), '')
	    || '/' || coalesce((SELECT md5(string_agg(to_jsonb(r)::text, '' ORDER BY r.id)) FROM public.rules r), '')`
)

var ruleDraftSigs = []string{
	`public.rule_draft_open(uuid)`,
	`public.rule_draft_put_rule(uuid, text, text, text, jsonb, text, text, text, boolean, boolean)`,
	`public.rule_draft_remove_rule(uuid, text)`,
	`public.rule_draft_publish(uuid, date)`,
}

// ruleDraftCalls holds one call of each function with $1 as the actor.
var ruleDraftCalls = []struct{ name, sql string }{
	{"rule_draft_open", `SELECT 1 FROM public.rule_draft_open($1::uuid)`},
	{"rule_draft_put_rule", `SELECT 1 FROM public.rule_draft_put_rule($1::uuid, 'desk-probe', 'required', 'invoice.number', '{}'::jsonb, 'error', NULL, 'probe', true, true)`},
	{"rule_draft_remove_rule", `SELECT 1 FROM public.rule_draft_remove_rule($1::uuid, 'vat-standard-rate')`},
	{"rule_draft_publish", `SELECT 1 FROM public.rule_draft_publish($1::uuid, DATE '3001-01-01')`},
}

func ruleDraftPgErr(err error) *pgconn.PgError {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr
	}
	return nil
}

// ruleDraftTx is an owner transaction that always rolls back.
func ruleDraftTx(t *testing.T) pgx.Tx {
	t.Helper()
	return revokeProbeTx(t)
}

func ruleDraftSeedStaff(t *testing.T, tx pgx.Tx, rulesRole bool) string {
	t.Helper()
	actor := uuid.NewString()
	if _, err := tx.Exec(context.Background(),
		`INSERT INTO public.staff_members (user_id, rules_role) VALUES ($1, $2)`, actor, rulesRole); err != nil {
		t.Fatalf("insert a staff row (rules_role %t): %v", rulesRole, err)
	}
	return actor
}

func ruleDraftAsApp(t *testing.T, tx pgx.Tx) {
	t.Helper()
	if _, err := tx.Exec(context.Background(), `SET LOCAL ROLE invoice_app`); err != nil {
		t.Fatalf("SET LOCAL ROLE invoice_app: %v", err)
	}
}

func ruleDraftAsOwner(t *testing.T, tx pgx.Tx) {
	t.Helper()
	if _, err := tx.Exec(context.Background(), `RESET ROLE`); err != nil {
		t.Fatalf("RESET ROLE: %v", err)
	}
}

// ruleDraftTry runs one statement in its own savepoint and returns the rows it produced, or its error.
func ruleDraftTry(t *testing.T, tx pgx.Tx, sql string, args ...any) (int, error) {
	t.Helper()
	ctx := context.Background()
	sp, err := tx.Begin(ctx)
	if err != nil {
		t.Fatalf("savepoint: %v", err)
	}
	rows, err := sp.Query(ctx, sql, args...)
	n := 0
	if err == nil {
		for rows.Next() {
			n++
		}
		err = rows.Err()
		rows.Close()
	}
	if err != nil {
		_ = sp.Rollback(ctx)
		return 0, err
	}
	if err := sp.Commit(ctx); err != nil {
		t.Fatalf("release savepoint: %v", err)
	}
	return n, nil
}

type ruleDraftOpened struct {
	id      string
	version int
	from    int
}

// ruleDraftMustOpen opens a draft as the current role and fails the test when it cannot.
func ruleDraftMustOpen(t *testing.T, tx pgx.Tx, actor string) ruleDraftOpened {
	t.Helper()
	var o ruleDraftOpened
	if err := tx.QueryRow(context.Background(), ruleDraftOpenSQL, actor).Scan(&o.id, &o.version, &o.from); err != nil {
		if pgErr := ruleDraftPgErr(err); pgErr != nil && pgErr.Code == "42883" {
			t.Fatalf("rule_draft_open does not exist (SQLSTATE 42883): the migration is not applied: %v", err)
		}
		t.Fatalf("rule_draft_open: %v", err)
	}
	return o
}

// ruleDraftState fingerprints both rule tables as the owner, then returns to invoice_app.
func ruleDraftState(t *testing.T, tx pgx.Tx) string {
	t.Helper()
	ruleDraftAsOwner(t, tx)
	var s string
	if err := tx.QueryRow(context.Background(), ruleDraftStateSQL).Scan(&s); err != nil {
		t.Fatalf("fingerprint the rule tables: %v", err)
	}
	ruleDraftAsApp(t, tx)
	return s
}

func ruleDraftUnsealed(t *testing.T, q querier) int {
	t.Helper()
	return mustCount(t, q, `SELECT count(*) FROM public.rule_set_versions WHERE NOT sealed`)
}

func TestRLS_RuleDraftOneDraftIndex(t *testing.T) {
	requireHarness(t)

	if n := mustCount(t, h.super, `SELECT count(*) FROM pg_indexes WHERE schemaname = 'public' AND indexname = $1
	    AND indexdef LIKE 'CREATE UNIQUE INDEX%' AND indexdef LIKE '%WHERE (NOT sealed)'`, ruleDraftIndex); n != 1 {
		t.Errorf("unique partial index %s on rule_set_versions (WHERE NOT sealed) is absent", ruleDraftIndex)
	}
	if n := ruleDraftUnsealed(t, h.super); n != 0 {
		t.Fatalf("%d unsealed versions are committed, want 0: the test cannot discriminate", n)
	}

	tx := ruleDraftTx(t)
	actor := ruleDraftSeedStaff(t, tx, true)
	ruleDraftAsApp(t, tx)
	ruleDraftMustOpen(t, tx, actor)

	_, err := ruleDraftTry(t, tx, ruleDraftOpenSQL, actor)
	if pgErr := ruleDraftPgErr(err); pgErr == nil || pgErr.Code != "23505" || pgErr.ConstraintName != ruleDraftIndex {
		t.Errorf("second rule_draft_open: %v, want SQLSTATE 23505 on %s", err, ruleDraftIndex)
	}

	ruleDraftAsOwner(t, tx)
	_, err = ruleDraftTry(t, tx, `INSERT INTO public.rule_set_versions (version, sealed, notes)
	    VALUES ((SELECT max(version) + 1 FROM public.rule_set_versions), false, 'second draft probe')`)
	if pgErr := ruleDraftPgErr(err); pgErr == nil || pgErr.Code != "23505" || pgErr.ConstraintName != ruleDraftIndex {
		t.Errorf("owner INSERT of a second unsealed version: %v, want SQLSTATE 23505 on %s", err, ruleDraftIndex)
	}
	if n := ruleDraftUnsealed(t, tx); n != 1 {
		t.Errorf("%d unsealed versions after the refusals, want 1", n)
	}
}

func TestRLS_RuleDraftFunctionsRefuseAnActorWithoutTheRulesRole(t *testing.T) {
	requireHarness(t)
	tx := ruleDraftTx(t)
	granted := ruleDraftSeedStaff(t, tx, true)
	plain := ruleDraftSeedStaff(t, tx, false)
	if n := mustCount(t, tx, `SELECT count(*) FROM public.staff_members WHERE user_id = $1 AND NOT rules_role`, plain); n != 1 {
		t.Fatalf("the plain staff row is not rules_role false: %d rows", n)
	}
	ruleDraftAsApp(t, tx)
	actors := []struct {
		name string
		id   any
	}{{"no staff row", uuid.NewString()}, {"rules_role false", plain}, {"NULL actor", nil}}

	refuse := func(phase string) {
		t.Helper()
		before := ruleDraftState(t, tx)
		for _, a := range actors {
			for _, c := range ruleDraftCalls {
				n, err := ruleDraftTry(t, tx, c.sql, a.id)
				if pgErr := ruleDraftPgErr(err); pgErr == nil || pgErr.Code != "42501" {
					t.Errorf("%s, %s, %s: rows %d, err %v, want SQLSTATE 42501", phase, a.name, c.name, n, err)
				}
			}
		}
		if after := ruleDraftState(t, tx); after != before {
			t.Errorf("%s: a refused call changed a version or rule row", phase)
		}
	}

	refuse("no draft open")
	draft := ruleDraftMustOpen(t, tx, granted)
	if n := mustCount(t, tx, `SELECT count(*) FROM public.rules WHERE rule_set_version_id = $1`, draft.id); n == 0 {
		t.Fatal("the control draft holds no rules: the test cannot discriminate")
	}
	refuse("a draft open")
}

func TestRLS_RuleDraftFunctionsExecuteGrants(t *testing.T) {
	requireHarness(t)
	ctx := context.Background()

	for _, sig := range ruleDraftSigs {
		var exists bool
		if err := h.super.QueryRow(ctx, `SELECT to_regprocedure($1) IS NOT NULL`, sig).Scan(&exists); err != nil {
			t.Fatalf("to_regprocedure %s: %v", sig, err)
		}
		if !exists {
			t.Errorf("%s does not exist: the migration is not applied", sig)
			continue
		}
		var app, reader bool
		if err := h.super.QueryRow(ctx, `SELECT has_function_privilege('invoice_app', $1, 'EXECUTE'),
		                                        has_function_privilege('invoice_tenant_reader', $1, 'EXECUTE')`, sig).Scan(&app, &reader); err != nil {
			t.Fatalf("has_function_privilege %s: %v", sig, err)
		}
		if !app || reader {
			t.Errorf("%s EXECUTE: invoice_app = %t, invoice_tenant_reader = %t, want true and false", sig, app, reader)
		}
		grantees := collectStrings(t, `SELECT CASE WHEN a.grantee = 0 THEN '=PUBLIC' ELSE a.grantee::regrole::text END
		                                 FROM pg_proc p, aclexplode(p.proacl) a
		                                WHERE p.oid = to_regprocedure($1) AND a.privilege_type = 'EXECUTE' ORDER BY 1`, sig)
		if len(grantees) == 0 {
			t.Errorf("%s: proacl lists no EXECUTE grantee, so a PUBLIC default would hide here", sig)
		} else if want := []string{"invoice_app", "invoice_migrator"}; !reflect.DeepEqual(grantees, want) {
			t.Errorf("%s EXECUTE grantees = %v, want %v (no PUBLIC)", sig, grantees, want)
		}
	}

	// The functions are the only write path: no direct write grant, table or column level, on either table.
	for _, role := range []string{"invoice_app", "invoice_tenant_reader"} {
		for _, table := range []string{"public.rules", "public.rule_set_versions"} {
			for _, priv := range []string{"INSERT", "UPDATE", "DELETE", "TRUNCATE"} {
				var held bool
				if err := h.super.QueryRow(ctx, `SELECT has_table_privilege($1, $2, $3)
				    OR CASE WHEN $3 IN ('INSERT', 'UPDATE') THEN has_any_column_privilege($1, $2, $3) ELSE false END`, role, table, priv).Scan(&held); err != nil {
					t.Fatalf("privilege %s on %s for %s: %v", priv, table, role, err)
				}
				if held {
					t.Errorf("%s holds %s on %s, want no direct write grant", role, priv, table)
				}
			}
		}
	}
	if n := mustCount(t, h.super, `SELECT count(*) FROM pg_roles WHERE rolname IN ('invoice_app', 'invoice_tenant_reader')`); n != 2 {
		t.Fatalf("%d of the two audited roles exist: the grant check is vacuous", n)
	}

	for _, c := range ruleDraftCalls {
		rows, err := h.reader.Query(ctx, c.sql, uuid.NewString())
		if err == nil {
			rows.Close()
			err = rows.Err()
		}
		if pgErr := ruleDraftPgErr(err); pgErr == nil || pgErr.Code != "42501" {
			t.Errorf("%s on the reader pool: %v, want SQLSTATE 42501", c.name, err)
		}
	}
}

func TestRLS_RuleDraftOpenIgnoresATempTableShadow(t *testing.T) {
	requireHarness(t)
	ctx := context.Background()

	for _, sig := range ruleDraftSigs {
		var cfg []string
		if err := h.super.QueryRow(ctx, `SELECT proconfig FROM pg_proc WHERE oid = to_regprocedure($1)`, sig).Scan(&cfg); err != nil {
			t.Errorf("read proconfig of %s: %v (is the migration applied?)", sig, err)
			continue
		}
		if want := []string{ruleDraftSearch}; !reflect.DeepEqual(cfg, want) {
			t.Errorf("%s proconfig = %v, want %v", sig, cfg, want)
		}
	}

	inForce := versionInForce(t, h.super)
	inForceNo := versionNumberOf(t, inForce)
	minVersion := mustCount(t, h.super, `SELECT min(version) FROM public.rule_set_versions`)
	if inForceNo == minVersion {
		t.Fatal("the version in force is the oldest: the shadow cannot discriminate")
	}
	maxVersion := mustCount(t, h.super, `SELECT max(version) FROM public.rule_set_versions`)

	tx := ruleDraftTx(t)
	actor := ruleDraftSeedStaff(t, tx, true)
	ruleDraftAsApp(t, tx)
	for _, s := range []string{
		`CREATE TEMP TABLE rule_set_versions AS SELECT * FROM public.rule_set_versions WHERE version = (SELECT min(version) FROM public.rule_set_versions)`,
		`UPDATE rule_set_versions SET effective_from = DATE '2000-01-01'`,
		`GRANT ALL ON pg_temp.rule_set_versions TO invoice_migrator`,
	} {
		if _, err := tx.Exec(ctx, s); err != nil {
			t.Fatalf("shadow setup (%s): %v", s, err)
		}
	}

	got := ruleDraftMustOpen(t, tx, actor)
	if got.from != inForceNo {
		t.Errorf("from_version = %d, want %d (the version in force, not the shadow's oldest)", got.from, inForceNo)
	}
	if got.version != maxVersion+1 {
		t.Errorf("version = %d, want %d (the real max + 1, not the shadow's)", got.version, maxVersion+1)
	}
}

func TestRLS_RuleDraftsDownAndUp(t *testing.T) {
	requireHarness(t)
	ctx := context.Background()
	matches, err := fs.Glob(migrations.FS, ruleDraftsGlob)
	if err != nil || len(matches) != 1 {
		t.Fatalf("glob %s in migrations.FS = %v (err %v), want exactly one file: the migration is not shipped", ruleDraftsGlob, matches, err)
	}
	up := auditEntitySectionOf(t, matches[0], "Up")
	down := shippedDownStatements(t, ruleDraftsGlob)
	if len(down) == 0 {
		t.Fatalf("Down of %s holds no statement", matches[0])
	}

	// present reports each function's presence and then the index's.
	present := func(tx pgx.Tx, when string) (funcs []bool, index bool) {
		for _, sig := range ruleDraftSigs {
			var ok bool
			if err := tx.QueryRow(ctx, `SELECT to_regprocedure($1) IS NOT NULL`, sig).Scan(&ok); err != nil {
				t.Fatalf("to_regprocedure %s %s: %v", sig, when, err)
			}
			funcs = append(funcs, ok)
		}
		if err := tx.QueryRow(ctx, `SELECT to_regclass('public.' || $1) IS NOT NULL`, ruleDraftIndex).Scan(&index); err != nil {
			t.Fatalf("to_regclass %s: %v", when, err)
		}
		return funcs, index
	}
	all := func(v []bool, want bool) bool {
		for _, b := range v {
			if b != want {
				return false
			}
		}
		return true
	}

	tx := ruleDraftTx(t)
	if _, err := tx.Exec(ctx, `SET LOCAL ROLE invoice_migrator`); err != nil {
		t.Fatalf("SET LOCAL ROLE invoice_migrator: %v", err)
	}
	if f, i := present(tx, "before the Down"); !all(f, true) || !i {
		t.Fatalf("before the Down: functions present %v, index present %t, want all present: is the migration applied?", f, i)
	}
	for _, s := range down {
		if _, err := tx.Exec(ctx, s); err != nil {
			t.Fatalf("shipped Down statement %q: %v", s, err)
		}
	}
	if f, i := present(tx, "after the Down"); !all(f, false) || i {
		t.Errorf("after the Down: functions present %v, index present %t, want none", f, i)
	}
	if _, err := tx.Exec(ctx, up); err != nil {
		t.Fatalf("shipped Up as the migrator: %v", err)
	}
	if f, i := present(tx, "after the Up"); !all(f, true) || !i {
		t.Fatalf("after the Up: functions present %v, index present %t, want all present", f, i)
	}
	for _, sig := range ruleDraftSigs {
		var app, reader bool
		if err := tx.QueryRow(ctx, `SELECT has_function_privilege('invoice_app', $1, 'EXECUTE'),
		                                   has_function_privilege('invoice_tenant_reader', $1, 'EXECUTE')`, sig).Scan(&app, &reader); err != nil {
			t.Fatalf("has_function_privilege %s: %v", sig, err)
		}
		if !app || reader {
			t.Errorf("after the Up: %s EXECUTE invoice_app = %t, invoice_tenant_reader = %t, want true and false", sig, app, reader)
		}
	}
}

// AUDIT-13-01: account_event_handoffs, account_known_devices and
// public.account_event_memberships. Env-gated through requireHarness (make test-rls).
package db_test

import (
	"context"
	"errors"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

const (
	aeHandoffs  = "account_event_handoffs"
	aeDevices   = "account_known_devices"
	aeMemberSig = "public.account_event_memberships(uuid,text)"
)

var aeEvents = []string{
	"account.signed_in", "account.sign_in_failed", "account.sign_in_blocked",
	"account.signed_out", "account.password_reset_requested", "account.password_reset_completed",
}

// aeRequireTables and aeRequireFunction fail with the missing-migration reason before any case reads a row.
func aeRequireTables(t *testing.T) {
	t.Helper()
	requireHarness(t)
	for _, tbl := range []string{aeHandoffs, aeDevices} {
		var reg *string
		if err := h.super.QueryRow(context.Background(), `SELECT to_regclass($1)::text`, "public."+tbl).Scan(&reg); err != nil {
			t.Fatalf("read to_regclass %s: %v", tbl, err)
		}
		if reg == nil {
			t.Fatalf("table %s does not exist: the account_events migration is not applied", tbl)
		}
	}
}

func aeRequireFunction(t *testing.T) {
	t.Helper()
	requireHarness(t)
	var fn *string
	if err := h.super.QueryRow(context.Background(), `SELECT to_regprocedure($1)::text`, aeMemberSig).Scan(&fn); err != nil {
		t.Fatalf("read to_regprocedure %s: %v", aeMemberSig, err)
	}
	if fn == nil {
		t.Fatalf("function %s does not exist: the account_events migration is not applied", aeMemberSig)
	}
}

func aeAddr() string { return "ae-" + uuid.NewString()[:8] + "@corp.example.test" }

func aeTenant(t *testing.T) string {
	t.Helper()
	id := uuid.NewString()
	execSetup(t, h.super, `INSERT INTO tenants (id, name, kind) VALUES ($1, 'Account events test', 'firm')`, id)
	t.Cleanup(func() { dropTenantRows(t, h.super, id) })
	return id
}

type aeRow struct{ Tenant, User string }

// aeMemberships calls the function as invoice_app on a pooled connection with no tenant bound.
// user and email may be nil.
func aeMemberships(t *testing.T, q interface {
	Query(context.Context, string, ...any) (pgx.Rows, error)
}, user, email any) []aeRow {
	t.Helper()
	rows, err := q.Query(context.Background(),
		`SELECT tenant_id::text, user_id::text FROM public.account_event_memberships($1::uuid, $2::text)`, user, email)
	if err != nil {
		t.Fatalf("account_event_memberships(%v, %v): %v", user, email, err)
	}
	defer rows.Close()
	var out []aeRow
	for rows.Next() {
		var r aeRow
		if err := rows.Scan(&r.Tenant, &r.User); err != nil {
			t.Fatalf("scan account_event_memberships: %v", err)
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("account_event_memberships(%v, %v): %v", user, email, err)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Tenant < out[j].Tenant })
	return out
}

func aeWant(user string, tenants ...string) []aeRow {
	out := make([]aeRow, 0, len(tenants))
	for _, tn := range tenants {
		out = append(out, aeRow{Tenant: tn, User: user})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Tenant < out[j].Tenant })
	return out
}

// aeAsGateway runs fn as invoice_gateway in a transaction on the superuser pool, and commits only when commit is set.
func aeAsGateway(t *testing.T, commit bool, fn func(ctx context.Context, tx pgx.Tx) error) error {
	t.Helper()
	ctx := context.Background()
	tx, err := h.super.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `SET LOCAL ROLE invoice_gateway`); err != nil {
		t.Fatalf("SET LOCAL ROLE invoice_gateway: %v", err)
	}
	if err := fn(ctx, tx); err != nil {
		return err
	}
	if commit {
		return tx.Commit(ctx)
	}
	return nil
}

const aeInsert = `INSERT INTO account_event_handoffs (event, user_id, email, user_agent) VALUES ($1, $2, $3, $4)`

// aeVerbs lists the privileges role holds on table, table-level or column-level.
func aeVerbs(t *testing.T, role, table string) []string {
	t.Helper()
	var held []string
	for _, v := range []struct {
		verb   string
		column bool
	}{{"SELECT", true}, {"INSERT", true}, {"UPDATE", true}, {"REFERENCES", true}, {"DELETE", false}, {"TRUNCATE", false}, {"TRIGGER", false}} {
		fn := "has_table_privilege"
		if v.column {
			fn = "has_any_column_privilege"
		}
		var got bool
		if err := h.super.QueryRow(context.Background(), `SELECT `+fn+`($1, $2::regclass, $3)`, role, "public."+table, v.verb).Scan(&got); err != nil {
			t.Fatalf("%s(%s, %s, %s): %v", fn, role, table, v.verb, err)
		}
		if got {
			held = append(held, v.verb)
		}
	}
	sort.Strings(held)
	return held
}

func TestRLS_AccountEventTablesGrantExactlyTheseVerbs(t *testing.T) {
	aeRequireTables(t)
	for _, c := range []struct {
		table string
		want  []string
	}{
		{aeHandoffs, []string{"DELETE", "SELECT"}},
		{aeDevices, []string{"DELETE", "INSERT", "SELECT"}},
	} {
		if got := aeVerbs(t, "invoice_app", c.table); !reflect.DeepEqual(got, c.want) {
			t.Errorf("invoice_app privileges on %s = %v, want exactly %v", c.table, got, c.want)
		}
	}
	// whole ACL: no other role and not PUBLIC holds a verb on either table
	for table, want := range map[string][]string{
		aeHandoffs: {"invoice_app=DELETE", "invoice_app=SELECT", "invoice_gateway=INSERT"},
		aeDevices:  {"invoice_app=DELETE", "invoice_app=INSERT", "invoice_app=SELECT"},
	} {
		if got := aeNonOwnerACL(t, table); !reflect.DeepEqual(got, want) {
			t.Errorf("ACL of %s = %v, want exactly %v", table, got, want)
		}
	}
	for _, role := range []string{"invoice_tenant_reader", "supabase_auth_admin", "auth_hook_reader"} {
		for _, table := range []string{aeHandoffs, aeDevices} {
			if got := aeVerbs(t, role, table); len(got) != 0 {
				t.Errorf("%s privileges on %s = %v, want none", role, table, got)
			}
		}
	}
}

// aeNonOwnerACL lists "grantee=VERB" for every grant on table to a role other than its owner; PUBLIC shows as "PUBLIC".
func aeNonOwnerACL(t *testing.T, table string) []string {
	t.Helper()
	rows, err := h.super.Query(context.Background(), `
		SELECT CASE WHEN a.grantee = 0 THEN 'PUBLIC' ELSE pg_get_userbyid(a.grantee) END || '=' || a.privilege_type
		  FROM pg_class c, aclexplode(c.relacl) a
		 WHERE c.oid = to_regclass($1) AND a.grantee <> c.relowner
		 ORDER BY 1`, "public."+table)
	if err != nil {
		t.Fatalf("read the ACL of %s: %v", table, err)
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var g string
		if err := rows.Scan(&g); err != nil {
			t.Fatalf("scan ACL of %s: %v", table, err)
		}
		out = append(out, g)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("read the ACL of %s: %v", table, err)
	}
	return out
}

func TestRLS_AccountEventGatewayMayOnlyInsert(t *testing.T) {
	aeRequireTables(t)
	if got, want := aeVerbs(t, "invoice_gateway", aeHandoffs), []string{"INSERT"}; !reflect.DeepEqual(got, want) {
		t.Errorf("invoice_gateway privileges on %s = %v, want exactly %v", aeHandoffs, got, want)
	}
	if got := aeVerbs(t, "invoice_gateway", aeDevices); len(got) != 0 {
		t.Errorf("invoice_gateway privileges on %s = %v, want none", aeDevices, got)
	}

	marker := aeAddr()
	t.Cleanup(func() {
		_, _ = h.super.Exec(context.Background(), `DELETE FROM account_event_handoffs WHERE email = $1`, marker)
	})
	if err := aeAsGateway(t, true, func(ctx context.Context, tx pgx.Tx) error {
		_, err := tx.Exec(ctx, aeInsert, "account.signed_in", nil, marker, "ua")
		return err
	}); err != nil {
		t.Fatalf("INSERT into %s as invoice_gateway: %v", aeHandoffs, err)
	}
	if n := mustCount(t, h.super, `SELECT count(*) FROM account_event_handoffs WHERE email = $1`, marker); n != 1 {
		t.Fatalf("rows written by invoice_gateway = %d, want 1", n)
	}

	for _, c := range []struct{ name, sql, msg string }{
		{"SELECT handoffs", `SELECT id FROM account_event_handoffs`, "permission denied for table account_event_handoffs"},
		{"DELETE handoffs", `DELETE FROM account_event_handoffs WHERE email = $1`, "permission denied for table account_event_handoffs"},
		{"INSERT known devices", `INSERT INTO account_known_devices (user_id, browser, os) VALUES (gen_random_uuid(), 'Chrome', 'macOS')`, "permission denied for table account_known_devices"},
	} {
		err := aeAsGateway(t, false, func(ctx context.Context, tx pgx.Tx) error {
			if strings.Contains(c.sql, "$1") {
				_, err := tx.Exec(ctx, c.sql, marker)
				return err
			}
			_, err := tx.Exec(ctx, c.sql)
			return err
		})
		assertPgRefusal(t, "invoice_gateway "+c.name, err, "42501", c.msg)
	}
	if n := mustCount(t, h.super, `SELECT count(*) FROM account_event_handoffs WHERE email = $1`, marker); n != 1 {
		t.Errorf("rows after the refused DELETE = %d, want 1", n)
	}
}

func TestRLS_AccountEventOutboxRefusesBadRows(t *testing.T) {
	aeRequireTables(t)
	ok := func(name string, args ...any) {
		t.Run("accepts "+name, func(t *testing.T) {
			err := aeAsGateway(t, false, func(ctx context.Context, tx pgx.Tx) error {
				_, err := tx.Exec(ctx, aeInsert, args...)
				return err
			})
			if err != nil {
				t.Errorf("insert %v: %v, want it accepted", args, err)
			}
		})
	}
	for _, ev := range aeEvents {
		ok(ev, ev, nil, aeAddr(), "ua")
	}
	ok("user id and no email", "account.signed_out", uuid.NewString(), nil, "")

	for _, c := range []struct {
		name string
		args []any
	}{
		{"unknown event", []any{"account.hacked", nil, aeAddr(), "ua"}},
		{"neither user id nor email", []any{"account.signed_in", nil, nil, "ua"}},
		{"513-character user agent", []any{"account.signed_in", nil, aeAddr(), strings.Repeat("a", 513)}},
		{"513 multi-byte characters", []any{"account.signed_in", nil, aeAddr(), strings.Repeat("é", 513)}},
	} {
		t.Run("refuses "+c.name, func(t *testing.T) {
			err := aeAsGateway(t, false, func(ctx context.Context, tx pgx.Tx) error {
				_, err := tx.Exec(ctx, aeInsert, c.args...)
				return err
			})
			var pgErr *pgconn.PgError
			if !errors.As(err, &pgErr) {
				t.Fatalf("want SQLSTATE 23514 on %s, got %v", aeHandoffs, err)
			}
			if pgErr.Code != "23514" || pgErr.TableName != aeHandoffs {
				t.Errorf("SQLSTATE = %s on table %q (%s), want 23514 on %s", pgErr.Code, pgErr.TableName, pgErr.Message, aeHandoffs)
			}
		})
	}
}

func TestRLS_AccountEventOutboxAcceptsA512CharacterAgent(t *testing.T) {
	aeRequireTables(t)
	for _, c := range []struct{ name, ua string }{
		{"ASCII", strings.Repeat("a", 512)},
		{"multi-byte", strings.Repeat("é", 512)},
	} {
		t.Run(c.name, func(t *testing.T) {
			var got int
			err := aeAsGateway(t, false, func(ctx context.Context, tx pgx.Tx) error {
				if _, err := tx.Exec(ctx, aeInsert, "account.signed_in", nil, aeAddr(), c.ua); err != nil {
					return err
				}
				// the gateway cannot read the row back
				_, err := tx.Exec(ctx, `RESET ROLE`)
				if err != nil {
					return err
				}
				return tx.QueryRow(ctx, `SELECT max(char_length(user_agent)) FROM account_event_handoffs`).Scan(&got)
			})
			if err != nil {
				t.Fatalf("insert a 512-character user agent: %v", err)
			}
			if got != 512 {
				t.Errorf("stored user agent length = %d, want 512", got)
			}
		})
	}
}

func TestRLS_AccountEventMembershipsByUserID(t *testing.T) {
	aeRequireFunction(t)
	a, b, c := aeTenant(t), aeTenant(t), aeTenant(t)
	user, other := uuid.NewString(), uuid.NewString()
	seedMemberRow(t, a, user, "admin", "active", nil, aeAddr())
	seedMemberRow(t, b, user, "admin", "active", nil, aeAddr())
	seedMemberRow(t, c, user, "admin", "suspended", nil, aeAddr())
	seedMemberRow(t, a, other, "admin", "active", nil, aeAddr())

	got := aeMemberships(t, h.app, user, nil)
	if want := aeWant(user, a, b); !reflect.DeepEqual(got, want) {
		t.Errorf("rows for the user id = %v, want exactly %v", got, want)
	}
}

func TestRLS_AccountEventMembershipsByEmailIgnoresCaseAndSpace(t *testing.T) {
	aeRequireFunction(t)
	tag := uuid.NewString()[:8]
	a, b := aeTenant(t), aeTenant(t)
	ada, other := uuid.NewString(), uuid.NewString()
	seedMemberRow(t, a, ada, "admin", "active", nil, "Ada-"+tag+"@Example.com")
	seedMemberRow(t, b, other, "admin", "active", nil, "bob-"+tag+"@example.com")

	for _, in := range []string{"  ada-" + tag + "@example.COM ", "ada-" + tag + "@example.com"} {
		got := aeMemberships(t, h.app, nil, in)
		if want := aeWant(ada, a); !reflect.DeepEqual(got, want) {
			t.Errorf("rows for email %q = %v, want exactly %v", in, got, want)
		}
	}
	// the match is the whole address, not a prefix or a substring
	if got := aeMemberships(t, h.app, nil, "da-"+tag+"@example.com"); len(got) != 0 {
		t.Errorf("rows for a partial address = %v, want none", got)
	}
}

func TestRLS_AccountEventMembershipsUserIDWinsOverEmail(t *testing.T) {
	aeRequireFunction(t)
	a, b, c := aeTenant(t), aeTenant(t), aeTenant(t)
	x, y, z := uuid.NewString(), uuid.NewString(), uuid.NewString()
	xAddr := aeAddr()
	seedMemberRow(t, a, x, "admin", "active", nil, xAddr)
	seedMemberRow(t, a, y, "admin", "active", nil, aeAddr())
	seedMemberRow(t, b, y, "admin", "active", nil, aeAddr())
	seedMemberRow(t, c, z, "admin", "suspended", nil, aeAddr())

	got := aeMemberships(t, h.app, y, xAddr)
	if want := aeWant(y, a, b); !reflect.DeepEqual(got, want) {
		t.Errorf("rows for Y's id with X's email = %v, want only Y's %v", got, want)
	}
	// a user id with no active row is not rescued by the email
	if got := aeMemberships(t, h.app, z, xAddr); len(got) != 0 {
		t.Errorf("rows for an inactive user id with X's email = %v, want none", got)
	}
}

func TestRLS_AccountEventMembershipsSkipInactiveRows(t *testing.T) {
	aeRequireFunction(t)
	user, addr := uuid.NewString(), aeAddr()
	for _, status := range []string{"invited", "suspended", "removed"} {
		seedMemberRow(t, aeTenant(t), user, "admin", status, nil, addr)
	}
	if got := aeMemberships(t, h.app, user, nil); len(got) != 0 {
		t.Errorf("rows by user id = %v, want none", got)
	}
	if got := aeMemberships(t, h.app, nil, addr); len(got) != 0 {
		t.Errorf("rows by email = %v, want none", got)
	}

	// the same lookup finds the row once it is active
	active := aeTenant(t)
	seedMemberRow(t, active, user, "admin", "active", nil, addr)
	want := aeWant(user, active)
	if got := aeMemberships(t, h.app, user, nil); !reflect.DeepEqual(got, want) {
		t.Errorf("rows by user id after an active row = %v, want %v", got, want)
	}
	if got := aeMemberships(t, h.app, nil, addr); !reflect.DeepEqual(got, want) {
		t.Errorf("rows by email after an active row = %v, want %v", got, want)
	}
}

func TestRLS_AccountEventMembershipsUnknownKeyIsEmpty(t *testing.T) {
	aeRequireFunction(t)
	// active members whose email is NULL, empty or blank must not match an empty key
	seedMemberRow(t, aeTenant(t), uuid.NewString(), "admin", "active", nil, nil)
	seedMemberRow(t, aeTenant(t), uuid.NewString(), "admin", "active", nil, "")
	seedMemberRow(t, aeTenant(t), uuid.NewString(), "admin", "active", nil, "   ")
	seedMemberRow(t, aeTenant(t), uuid.NewString(), "admin", "active", nil, aeAddr())

	for _, c := range []struct {
		name        string
		user, email any
	}{
		{"unknown user id", uuid.NewString(), nil},
		{"unknown email", nil, aeAddr()},
		{"NULL and NULL", nil, nil},
		{"NULL and empty", nil, ""},
		{"NULL and blank", nil, "   "},
	} {
		t.Run(c.name, func(t *testing.T) {
			if got := aeMemberships(t, h.app, c.user, c.email); len(got) != 0 {
				t.Errorf("rows = %v, want none", got)
			}
		})
	}
}

func TestRLS_AccountEventMembershipsNeedNoTenant(t *testing.T) {
	aeRequireFunction(t)
	ctx := context.Background()
	tenant, user, addr := aeTenant(t), uuid.NewString(), aeAddr()
	seedMemberRow(t, tenant, user, "admin", "active", nil, addr)

	tx, err := h.app.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var bound *string
	if err := tx.QueryRow(ctx, `SELECT nullif(current_setting('app.current_tenant', true), '')`).Scan(&bound); err != nil {
		t.Fatalf("read app.current_tenant: %v", err)
	}
	if bound != nil {
		t.Fatalf("test setup: app.current_tenant = %q, want unset", *bound)
	}
	// RLS hides the row from a plain read, so only the definer read can return it
	if n := mustCount(t, tx, `SELECT count(*) FROM memberships WHERE email = $1`, addr); n != 0 {
		t.Fatalf("invoice_app sees %d memberships rows with no tenant, want 0", n)
	}
	if got, want := aeMemberships(t, tx, nil, addr), aeWant(user, tenant); !reflect.DeepEqual(got, want) {
		t.Errorf("rows by email with no tenant = %v, want %v", got, want)
	}
}

func TestRLS_AccountEventMembershipsShapeAndACL(t *testing.T) {
	aeRequireFunction(t)
	s, ok := readProc(t, aeMemberSig)
	if !ok {
		t.Fatalf("function %s does not exist", aeMemberSig)
	}
	if s.owner != "auth_hook_reader" {
		t.Errorf("owner = %q, want auth_hook_reader", s.owner)
	}
	if !s.secdef {
		t.Errorf("prosecdef = false, want SECURITY DEFINER")
	}
	if want := []string{`search_path=""`}; !reflect.DeepEqual(s.config, want) {
		t.Errorf("proconfig = %q, want exactly %q", s.config, want)
	}
	assertExecuteOnlyForApp(t, aeMemberSig, s, "auth_hook_reader")
	var gw bool
	if err := h.super.QueryRow(context.Background(), `SELECT has_function_privilege('invoice_gateway', to_regprocedure($1), 'EXECUTE')`, aeMemberSig).Scan(&gw); err != nil {
		t.Fatalf("has_function_privilege(invoice_gateway): %v", err)
	}
	if gw {
		t.Errorf("invoice_gateway holds EXECUTE on %s, want none", aeMemberSig)
	}
}

// aeHookReaderColumns maps each memberships column to whether auth_hook_reader may SELECT it.
func aeHookReaderColumns(t *testing.T) map[string]bool {
	t.Helper()
	rows, err := h.super.Query(context.Background(),
		`SELECT column_name, has_column_privilege('auth_hook_reader', 'public.memberships', column_name, 'SELECT')
		   FROM information_schema.columns WHERE table_schema = 'public' AND table_name = 'memberships'`)
	if err != nil {
		t.Fatalf("read memberships column grants: %v", err)
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var col string
		var ok bool
		if err := rows.Scan(&col, &ok); err != nil {
			t.Fatalf("scan column grant: %v", err)
		}
		out[col] = ok
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("read memberships column grants: %v", err)
	}
	return out
}

func aeObjectsPresent(t *testing.T) (tables [2]bool, fn bool) {
	t.Helper()
	for i, tbl := range []string{aeHandoffs, aeDevices} {
		var reg *string
		if err := h.super.QueryRow(context.Background(), `SELECT to_regclass($1)::text`, "public."+tbl).Scan(&reg); err != nil {
			t.Fatalf("read to_regclass %s: %v", tbl, err)
		}
		tables[i] = reg != nil
	}
	var reg *string
	if err := h.super.QueryRow(context.Background(), `SELECT to_regprocedure($1)::text`, aeMemberSig).Scan(&reg); err != nil {
		t.Fatalf("read to_regprocedure: %v", err)
	}
	return tables, reg != nil
}

func TestRLS_AccountEventMigrationDownAndUp(t *testing.T) {
	aeRequireTables(t)
	aeRequireFunction(t)
	before := aeHookReaderColumns(t)
	granted := 0
	for _, ok := range before {
		if ok {
			granted++
		}
	}
	for _, col := range []string{"user_id", "tenant_id", "status", "display_name", "email"} {
		if !before[col] {
			t.Fatalf("before the Down: auth_hook_reader cannot SELECT memberships.%s, want it granted", col)
		}
	}
	if granted == 0 {
		t.Fatalf("before the Down: auth_hook_reader holds no memberships column grant")
	}

	reapply := rollBackMigrations(t, staffMigrationProvider(t), migrationVersion(t, "*_account_events.sql"))
	if tables, fn := aeObjectsPresent(t); tables != [2]bool{false, false} || fn {
		t.Errorf("after the Down: tables present = %v, function present = %v, want none", tables, fn)
	}
	if after := aeHookReaderColumns(t); !reflect.DeepEqual(after, before) {
		t.Errorf("after the Down: auth_hook_reader memberships column grants = %v, want unchanged %v", after, before)
	}

	reapply()
	if tables, fn := aeObjectsPresent(t); tables != [2]bool{true, true} || !fn {
		t.Errorf("after the Up: tables present = %v, function present = %v, want all present", tables, fn)
	}
	if again := aeHookReaderColumns(t); !reflect.DeepEqual(again, before) {
		t.Errorf("after the Up: auth_hook_reader memberships column grants = %v, want unchanged %v", again, before)
	}
}

// A real row: the grants must work as invoice_app, not only read as privileges.
func TestRLS_AccountEventAppReadsDeletesHandoffsAndOwnsDevices(t *testing.T) {
	aeRequireTables(t)
	ctx := context.Background()
	marker := aeAddr()
	t.Cleanup(func() {
		_, _ = h.super.Exec(ctx, `DELETE FROM account_event_handoffs WHERE email = $1`, marker)
	})
	if err := aeAsGateway(t, true, func(ctx context.Context, tx pgx.Tx) error {
		_, err := tx.Exec(ctx, aeInsert, "account.signed_in", nil, marker, "ua")
		return err
	}); err != nil {
		t.Fatalf("INSERT as invoice_gateway: %v", err)
	}
	if n := mustCount(t, h.app, `SELECT count(*) FROM account_event_handoffs WHERE email = $1`, marker); n != 1 {
		t.Fatalf("invoice_app reads %d handoff rows, want 1", n)
	}
	for _, sql := range []string{
		`UPDATE account_event_handoffs SET ip = 'x' WHERE email = $1`,
		`INSERT INTO account_event_handoffs (event, email) VALUES ('account.signed_in', $1)`,
	} {
		_, err := h.app.Exec(ctx, sql, marker)
		assertPgRefusal(t, "invoice_app "+sql, err, "42501", "permission denied for table account_event_handoffs")
	}
	if tag, err := h.app.Exec(ctx, `DELETE FROM account_event_handoffs WHERE email = $1`, marker); err != nil || tag.RowsAffected() != 1 {
		t.Fatalf("invoice_app DELETE of a handoff: %v, rows %d, want 1", err, tag.RowsAffected())
	}

	user := uuid.NewString()
	t.Cleanup(func() { _, _ = h.super.Exec(ctx, `DELETE FROM account_known_devices WHERE user_id = $1`, user) })
	add := func(browser, os string) error {
		_, err := h.app.Exec(ctx, `INSERT INTO account_known_devices (user_id, browser, os) VALUES ($1, $2, $3)`, user, browser, os)
		return err
	}
	if err := add("Chrome", "macOS"); err != nil {
		t.Fatalf("invoice_app INSERT of a device: %v", err)
	}
	// the pair is the key: another OS or another browser for the same account is a new device
	for _, p := range [][2]string{{"Chrome", "Windows"}, {"Firefox", "macOS"}} {
		if err := add(p[0], p[1]); err != nil {
			t.Errorf("INSERT of the new pair %v: %v, want it accepted", p, err)
		}
	}
	var pgErr *pgconn.PgError
	if err := add("Chrome", "macOS"); !errors.As(err, &pgErr) || pgErr.Code != "23505" {
		t.Errorf("INSERT of a known pair = %v, want SQLSTATE 23505", err)
	}
	if n := mustCount(t, h.app, `SELECT count(*) FROM account_known_devices WHERE user_id = $1`, user); n != 3 {
		t.Errorf("invoice_app reads %d device rows, want 3", n)
	}
	_, err := h.app.Exec(ctx, `UPDATE account_known_devices SET os = 'x' WHERE user_id = $1`, user)
	assertPgRefusal(t, "invoice_app UPDATE of a device", err, "42501", "permission denied for table account_known_devices")
	for _, tbl := range []string{aeHandoffs, aeDevices} {
		_, err := h.reader.Exec(ctx, `SELECT 1 FROM `+tbl)
		assertPgRefusal(t, "invoice_tenant_reader SELECT "+tbl, err, "42501", "permission denied for table "+tbl)
	}
	if tag, err := h.app.Exec(ctx, `DELETE FROM account_known_devices WHERE user_id = $1`, user); err != nil || tag.RowsAffected() != 3 {
		t.Errorf("invoice_app DELETE of the devices: %v, rows %d, want 3", err, tag.RowsAffected())
	}
}

// The definer function must read public.memberships, not a table a caller's search_path puts first.
func TestRLS_AccountEventMembershipsIgnoreACallerSearchPath(t *testing.T) {
	aeRequireFunction(t)
	ctx := context.Background()
	tenant, user, addr := aeTenant(t), uuid.NewString(), aeAddr()
	seedMemberRow(t, tenant, user, "admin", "active", nil, addr)
	decoyTenant, decoyUser := uuid.NewString(), uuid.NewString()

	tx, err := h.super.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	for _, sql := range []string{
		`CREATE SCHEMA ae_decoy`,
		`CREATE TABLE ae_decoy.memberships (tenant_id uuid, user_id uuid, status text, email text)`,
		`INSERT INTO ae_decoy.memberships VALUES ('` + decoyTenant + `', '` + decoyUser + `', 'active', '` + addr + `')`,
		`GRANT USAGE ON SCHEMA ae_decoy TO PUBLIC`,
		`GRANT SELECT ON ae_decoy.memberships TO PUBLIC`,
		`SET LOCAL ROLE invoice_app`,
		`SET LOCAL search_path = ae_decoy, public`,
	} {
		if _, err := tx.Exec(ctx, sql); err != nil {
			t.Fatalf("test setup %q: %v", sql, err)
		}
	}
	// the decoy would win a plain read, so the function result proves the path was ignored
	if n := mustCount(t, tx, `SELECT count(*) FROM memberships WHERE email = $1`, addr); n != 1 {
		t.Fatalf("test setup: unqualified memberships reads %d decoy rows, want 1", n)
	}
	got := aeMemberships(t, tx, nil, addr)
	if want := aeWant(user, tenant); !reflect.DeepEqual(got, want) {
		t.Errorf("rows with a hijacked search_path = %v, want only the real %v", got, want)
	}
}

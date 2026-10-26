// workspace_deletion_links and workspace_deletion_cancel_mails: shape, tenant isolation, grants, purge.
// Env-gated through requireHarness (make test-rls). Tenants are seeded as the superuser and removed on cleanup.
package db_test

import (
	"context"
	"errors"
	"sort"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	db "github.com/SimonOsipov/invoice-os/internal/platform/db"
)

type dlColumn struct {
	typ  string
	dflt string // substring of column_default; "" means no default
}

type dlTable struct {
	name    string
	columns map[string]dlColumn
	// insert adds one valid row for tenant through q and returns its id.
	insert func(q querier, tenant string) (string, error)
	// frozen are the columns invoice_app holds no UPDATE grant on.
	frozen []string
}

const dlTimestamptz = "timestamp with time zone"

func dlHash() []byte {
	a, b := uuid.New(), uuid.New()
	return append(a[:], b[:]...)
}

var dlLinks = dlTable{
	name: "workspace_deletion_links",
	columns: map[string]dlColumn{
		"id":                {"uuid", "gen_random_uuid"},
		"tenant_id":         {"uuid", ""},
		"recipient_user_id": {"uuid", ""},
		"token_hash":        {"bytea", ""},
		"expires_at":        {dlTimestamptz, ""},
		"send_status":       {"text", "'sending'"},
		"created_at":        {dlTimestamptz, "now()"},
	},
	insert: func(q querier, tenant string) (string, error) {
		var id string
		err := q.QueryRow(context.Background(),
			`INSERT INTO public.workspace_deletion_links (tenant_id, recipient_user_id, token_hash, expires_at)
			 VALUES ($1, $2, $3, now() + interval '30 days') RETURNING id::text`,
			tenant, uuid.NewString(), dlHash()).Scan(&id)
		return id, err
	},
	frozen: []string{"token_hash", "expires_at", "recipient_user_id", "tenant_id"},
}

var dlMails = dlTable{
	name: "workspace_deletion_cancel_mails",
	columns: map[string]dlColumn{
		"id":                {"uuid", "gen_random_uuid"},
		"tenant_id":         {"uuid", ""},
		"recipient_user_id": {"uuid", ""},
		"cancelled_due_at":  {dlTimestamptz, ""},
		"send_status":       {"text", "'sending'"},
		"created_at":        {dlTimestamptz, "now()"},
	},
	insert: func(q querier, tenant string) (string, error) {
		var id string
		err := q.QueryRow(context.Background(),
			`INSERT INTO public.workspace_deletion_cancel_mails (tenant_id, recipient_user_id, cancelled_due_at)
			 VALUES ($1, $2, now()) RETURNING id::text`,
			tenant, uuid.NewString()).Scan(&id)
		return id, err
	},
	frozen: []string{"cancelled_due_at", "recipient_user_id", "tenant_id"},
}

// dlRequire fails when the migration is absent, so the red reason is the missing table.
func dlRequire(t *testing.T, tbl dlTable) {
	t.Helper()
	requireHarness(t)
	var reg *string
	if err := h.super.QueryRow(context.Background(), `SELECT to_regclass($1)::text`, "public."+tbl.name).Scan(&reg); err != nil {
		t.Fatalf("read to_regclass %s: %v", tbl.name, err)
	}
	if reg == nil {
		t.Fatalf("table public.%s not found: the workspace_deletion_mails migration is not applied", tbl.name)
	}
}

// dlTenant seeds a tenant; the cleanup delete cascades to its rows.
func dlTenant(t *testing.T) string {
	t.Helper()
	id := uuid.NewString()
	execSetup(t, h.super, `INSERT INTO tenants (id, name, kind) VALUES ($1, 'DEL-05 probe', 'firm')`, id)
	t.Cleanup(func() { _, _ = h.super.Exec(context.Background(), `DELETE FROM tenants WHERE id = $1`, id) })
	return id
}

// dlSeed inserts n rows for tenant as the superuser and returns their ids.
func dlSeed(t *testing.T, tbl dlTable, tenant string, n int) []string {
	t.Helper()
	var ids []string
	for i := 0; i < n; i++ {
		id, err := tbl.insert(h.super, tenant)
		if err != nil {
			t.Fatalf("test setup: seed %s: %v", tbl.name, err)
		}
		ids = append(ids, id)
	}
	return ids
}

func dlCount(t *testing.T, tbl dlTable, tenant string) int {
	t.Helper()
	return mustCount(t, h.super, `SELECT count(*) FROM public.`+tbl.name+` WHERE tenant_id = $1`, tenant)
}

// dlAsApp runs fn as invoice_app bound to tenant, in its own transaction.
func dlAsApp(tenant string, fn func(tx pgx.Tx) error) error {
	return db.WithinTenantTx(context.Background(), h.app, tenant, fn)
}

func dlAppExec(tenant, sql string, args ...any) (int64, error) {
	var n int64
	err := dlAsApp(tenant, func(tx pgx.Tx) error {
		tag, e := tx.Exec(context.Background(), sql, args...)
		n = tag.RowsAffected()
		return e
	})
	return n, err
}

// dlVisible counts the rows pool sees; bind "" leaves app.current_tenant unset, dlBindEmpty sets it to the empty string.
const dlBindEmpty = "\x00empty"

func dlVisible(t *testing.T, pool interface {
	Begin(context.Context) (pgx.Tx, error)
}, bind string, tbl dlTable) int {
	t.Helper()
	ctx := context.Background()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	switch bind {
	case "":
	case dlBindEmpty:
		execSetup(t, tx, `SELECT set_config('app.current_tenant', '', true)`)
	default:
		execSetup(t, tx, `SELECT set_config('app.current_tenant', $1, true)`, bind)
	}
	return mustCount(t, tx, `SELECT count(*) FROM public.`+tbl.name)
}

func dlPgErr(err error) *pgconn.PgError {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr
	}
	return nil
}

// dlWantCode asserts err is a pg error with the given SQLSTATE and, when part is set, a constraint name containing it.
func dlWantCode(t *testing.T, what string, err error, code, part string) {
	t.Helper()
	pgErr := dlPgErr(err)
	if pgErr == nil {
		t.Errorf("%s: got %v, want SQLSTATE %s", what, err, code)
		return
	}
	if pgErr.Code != code {
		t.Errorf("%s: got SQLSTATE %s %q, want %s", what, pgErr.Code, pgErr.Message, code)
		return
	}
	if part != "" && !strings.Contains(pgErr.ConstraintName, part) {
		t.Errorf("%s: constraint %q does not name %q", what, pgErr.ConstraintName, part)
	}
}

func dlCheckShape(t *testing.T, tbl dlTable) {
	t.Helper()
	dlRequire(t, tbl)
	ctx := context.Background()

	rows, err := h.super.Query(ctx,
		`SELECT column_name, data_type, is_nullable, coalesce(column_default, '') FROM information_schema.columns
		  WHERE table_schema = 'public' AND table_name = $1`, tbl.name)
	if err != nil {
		t.Fatalf("read columns: %v", err)
	}
	defer rows.Close()
	seen := map[string]bool{}
	for rows.Next() {
		var name, typ, nullable, dflt string
		if err := rows.Scan(&name, &typ, &nullable, &dflt); err != nil {
			t.Fatalf("scan column: %v", err)
		}
		seen[name] = true
		want, ok := tbl.columns[name]
		if !ok {
			t.Errorf("%s: unexpected column %s", tbl.name, name)
			continue
		}
		if typ != want.typ || nullable != "NO" {
			t.Errorf("%s.%s: type %q nullable %s, want %q NOT NULL", tbl.name, name, typ, nullable, want.typ)
		}
		if want.dflt == "" && dflt != "" || !strings.Contains(dflt, want.dflt) {
			t.Errorf("%s.%s: default %q, want one containing %q", tbl.name, name, dflt, want.dflt)
		}
	}
	rows.Close()
	if len(seen) != len(tbl.columns) {
		t.Errorf("%s: %d columns %v, want %d", tbl.name, len(seen), dlSortedKeys(seen), len(tbl.columns))
	}

	reg := "public." + tbl.name
	if n := mustCount(t, h.super,
		`SELECT count(*) FROM pg_constraint WHERE conrelid = $1::regclass AND contype = 'p'`, reg); n != 1 {
		t.Errorf("%s: primary keys = %d, want 1", tbl.name, n)
	}
	var del []string
	fk, err := h.super.Query(ctx,
		`SELECT confdeltype::text FROM pg_constraint
		  WHERE conrelid = $1::regclass AND contype = 'f' AND confrelid = 'public.tenants'::regclass`, reg)
	if err != nil {
		t.Fatalf("read foreign keys: %v", err)
	}
	for fk.Next() {
		var d string
		if err := fk.Scan(&d); err != nil {
			t.Fatalf("scan fk: %v", err)
		}
		del = append(del, d)
	}
	fk.Close()
	if len(del) != 1 || del[0] != "c" {
		t.Errorf("%s: foreign keys to tenants have confdeltype %v, want exactly [c]", tbl.name, del)
	}

	if _, ok := tbl.columns["token_hash"]; ok {
		if n := mustCount(t, h.super,
			`SELECT count(*) FROM pg_index i JOIN pg_attribute a ON a.attrelid = i.indrelid AND a.attnum = ANY(i.indkey)
			  WHERE i.indrelid = $1::regclass AND i.indisunique AND i.indnkeyatts = 1 AND a.attname = 'token_hash'`, reg); n != 1 {
			t.Errorf("%s: unique indexes on token_hash alone = %d, want 1", tbl.name, n)
		}
	}
}

func dlSortedKeys(m map[string]bool) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// dlCheckSendStatus pins the default and the CHECK on a table whose valid insert is tbl.insert.
func dlCheckSendStatus(t *testing.T, tbl dlTable) {
	t.Helper()
	a := dlTenant(t)
	id, err := tbl.insert(h.super, a)
	if err != nil {
		t.Fatalf("test setup: insert %s: %v", tbl.name, err)
	}
	var status string
	if err := h.super.QueryRow(context.Background(),
		`SELECT send_status FROM public.`+tbl.name+` WHERE id = $1`, id).Scan(&status); err != nil || status != "sending" {
		t.Errorf("%s: send_status of a row inserted without it = %q (%v), want sending", tbl.name, status, err)
	}
	for _, ok := range []string{"sent", "failed"} {
		if n, err := h.super.Exec(context.Background(),
			`UPDATE public.`+tbl.name+` SET send_status = $2 WHERE id = $1`, id, ok); err != nil || n.RowsAffected() != 1 {
			t.Errorf("%s: send_status %q: rows=%d err=%v, want accepted", tbl.name, ok, n.RowsAffected(), err)
		}
	}
	_, err = h.super.Exec(context.Background(),
		`UPDATE public.`+tbl.name+` SET send_status = 'queued' WHERE id = $1`, id)
	dlWantCode(t, tbl.name+" send_status = 'queued'", err, "23514", "send_status")
}

func dlCheckIsolation(t *testing.T, tbl dlTable) {
	t.Helper()
	dlRequire(t, tbl)
	ctx := context.Background()
	a, b := dlTenant(t), dlTenant(t)
	aIDs := dlSeed(t, tbl, a, 2)
	bIDs := dlSeed(t, tbl, b, 1)

	var enabled, forced bool
	if err := h.super.QueryRow(ctx,
		`SELECT relrowsecurity, relforcerowsecurity FROM pg_class WHERE oid = $1::regclass`, "public."+tbl.name).Scan(&enabled, &forced); err != nil {
		t.Fatalf("read relrowsecurity: %v", err)
	}
	if !enabled || !forced {
		t.Errorf("%s: relrowsecurity=%v relforcerowsecurity=%v, want ENABLE and FORCE", tbl.name, enabled, forced)
	}
	if n := mustCount(t, h.super,
		`SELECT count(*) FROM pg_policies WHERE schemaname = 'public' AND tablename = $1 AND policyname = 'tenant_isolation'`, tbl.name); n != 1 {
		t.Errorf("%s: tenant_isolation policies = %d, want 1", tbl.name, n)
	}

	// Bound to A: A's rows are visible, B's are not.
	if err := dlAsApp(a, func(tx pgx.Tx) error {
		if n := mustCount(t, tx, `SELECT count(*) FROM public.`+tbl.name); n != len(aIDs) {
			t.Errorf("%s: app bound to A sees %d rows, want %d", tbl.name, n, len(aIDs))
		}
		if n := mustCount(t, tx, `SELECT count(*) FROM public.`+tbl.name+` WHERE tenant_id = $1`, b); n != 0 {
			t.Errorf("%s: app bound to A sees %d of B's rows, want 0", tbl.name, n)
		}
		return nil
	}); err != nil {
		t.Fatalf("app bound to A: %v", err)
	}

	// Positive control: the app can insert for the bound tenant, so the refusal below is the policy.
	if err := dlAsApp(a, func(tx pgx.Tx) error { _, e := tbl.insert(tx, a); return e }); err != nil {
		t.Errorf("%s: app bound to A inserting for A: %v, want accepted", tbl.name, err)
	}
	err := dlAsApp(a, func(tx pgx.Tx) error { _, e := tbl.insert(tx, b); return e })
	dlWantCode(t, tbl.name+": app bound to A inserting for B", err, "42501", "")

	n, err := dlAppExec(a, `UPDATE public.`+tbl.name+` SET send_status = 'sent' WHERE id = $1`, bIDs[0])
	if err != nil || n != 0 {
		t.Errorf("%s: app bound to A updating B's row: rows=%d err=%v, want 0 rows", tbl.name, n, err)
	}
	var status string
	if err := h.super.QueryRow(ctx, `SELECT send_status FROM public.`+tbl.name+` WHERE id = $1`, bIDs[0]).Scan(&status); err != nil || status != "sending" {
		t.Errorf("%s: B's row send_status = %q (%v), want sending", tbl.name, status, err)
	}

	// No tenant: nothing is visible to the app or to the table owner.
	if got := dlVisible(t, h.app, "", tbl); got != 0 {
		t.Errorf("%s: app with no tenant sees %d rows, want 0", tbl.name, got)
	}
	if got := dlVisible(t, h.app, dlBindEmpty, tbl); got != 0 {
		t.Errorf("%s: app with an empty tenant sees %d rows, want 0", tbl.name, got)
	}
	if got := dlVisible(t, h.mig, "", tbl); got != 0 {
		t.Errorf("%s: invoice_migrator with no tenant sees %d rows, want 0 (FORCE RLS)", tbl.name, got)
	}
	if got := dlVisible(t, h.mig, a, tbl); got != len(aIDs)+1 {
		t.Errorf("%s: invoice_migrator bound to A sees %d rows, want %d", tbl.name, got, len(aIDs)+1)
	}
}

func dlCheckVerbs(t *testing.T, tbl dlTable) {
	t.Helper()
	dlRequire(t, tbl)
	ctx := context.Background()
	a := dlTenant(t)
	id := dlSeed(t, tbl, a, 1)[0]
	snapshot := func() string {
		var s string
		if err := h.super.QueryRow(ctx, `SELECT to_jsonb(x)::text FROM public.`+tbl.name+` x WHERE id = $1`, id).Scan(&s); err != nil {
			t.Fatalf("snapshot: %v", err)
		}
		return s
	}
	before := snapshot()

	n, err := dlAppExec(a, `UPDATE public.`+tbl.name+` SET send_status = 'sent' WHERE id = $1`, id)
	if err != nil || n != 1 {
		t.Fatalf("%s: app UPDATE send_status: rows=%d err=%v, want 1 row", tbl.name, n, err)
	}
	afterAllowed := snapshot()
	if afterAllowed == before || !strings.Contains(afterAllowed, `"send_status": "sent"`) {
		t.Fatalf("%s: send_status UPDATE left the row at %s", tbl.name, afterAllowed)
	}

	// col = col needs the UPDATE privilege and changes nothing, so a wrongly granted column reads as accepted.
	for _, col := range tbl.frozen {
		_, err := dlAppExec(a, `UPDATE public.`+tbl.name+` SET `+col+` = `+col+` WHERE id = $1`, id)
		dlWantCode(t, tbl.name+": app UPDATE "+col, err, "42501", "")
	}
	_, err = dlAppExec(a, `DELETE FROM public.`+tbl.name+` WHERE id = $1`, id)
	dlWantCode(t, tbl.name+": app DELETE", err, "42501", "")
	if got := snapshot(); got != afterAllowed {
		t.Errorf("%s: row changed by refused statements: %s, want %s", tbl.name, got, afterAllowed)
	}
}

func dlCheckPurge(t *testing.T, tbl dlTable) {
	t.Helper()
	dlRequire(t, tbl)
	a, b := dlTenant(t), dlTenant(t)
	dlSeed(t, tbl, a, 2)
	dlSeed(t, tbl, b, 1)
	if got := dlCount(t, tbl, a); got != 2 {
		t.Fatalf("test setup: A has %d rows, want 2", got)
	}

	if n := ptPurge(t, a); n != 1 {
		t.Errorf("purge A = %d, want 1", n)
	}
	if got := dlCount(t, tbl, a); got != 0 {
		t.Errorf("%s: rows left for A after purge = %d, want 0", tbl.name, got)
	}
	if got := dlCount(t, tbl, b); got != 1 {
		t.Errorf("%s: rows of B after purging A = %d, want 1", tbl.name, got)
	}
}

func TestRLS_DeletionLinksTableShape(t *testing.T) {
	dlCheckShape(t, dlLinks)
}

func TestRLS_DeletionLinksRefuseABadHashOrStatus(t *testing.T) {
	dlRequire(t, dlLinks)
	a := dlTenant(t)
	insert := func(hash []byte, status string) error {
		return dlAsApp(a, func(tx pgx.Tx) error {
			_, e := tx.Exec(context.Background(),
				`INSERT INTO public.workspace_deletion_links (tenant_id, recipient_user_id, token_hash, expires_at, send_status)
				 VALUES ($1, $2, $3, now() + interval '30 days', $4)`, a, uuid.NewString(), hash, status)
			return e
		})
	}
	h32 := dlHash()
	if err := insert(h32, "sending"); err != nil {
		t.Fatalf("control: a 32-byte hash with status sending: %v, want accepted", err)
	}
	dlWantCode(t, "31-byte hash", insert(h32[:31], "sending"), "23514", "token_hash")
	dlWantCode(t, "33-byte hash", insert(append(dlHash(), 1), "sending"), "23514", "token_hash")
	dlWantCode(t, "status queued", insert(dlHash(), "queued"), "23514", "send_status")
	dlWantCode(t, "the same hash twice", insert(h32, "sending"), "23505", "")

	dlCheckSendStatus(t, dlLinks)
}

func TestRLS_DeletionLinksAreTenantIsolated(t *testing.T) {
	dlCheckIsolation(t, dlLinks)
}

func TestRLS_AppHasOnlyTheGrantedVerbsOnDeletionLinks(t *testing.T) {
	dlCheckVerbs(t, dlLinks)
}

func TestRLS_PurgeTenantRemovesOnlyItsDeletionLinks(t *testing.T) {
	dlCheckPurge(t, dlLinks)
}

func TestRLS_DeletionCancelMailsTableShape(t *testing.T) {
	dlCheckShape(t, dlMails)
	dlCheckSendStatus(t, dlMails)
}

func TestRLS_DeletionCancelMailsAreTenantIsolated(t *testing.T) {
	dlCheckIsolation(t, dlMails)
}

func TestRLS_AppHasOnlyTheGrantedVerbsOnDeletionCancelMails(t *testing.T) {
	dlCheckVerbs(t, dlMails)
}

func TestRLS_PurgeTenantRemovesOnlyItsDeletionCancelMails(t *testing.T) {
	dlCheckPurge(t, dlMails)
}

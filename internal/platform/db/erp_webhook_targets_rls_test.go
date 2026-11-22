// Suite for public.erp_webhook_targets: the definer lookup of a vendor company key, the lookup role's
// column grant and policy, and the isolation of invoice_app around them.
package db_test

import (
	"context"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/SimonOsipov/invoice-os/internal/platform/db"
)

const webhookTargetsSig = "public.erp_webhook_targets(text, text)"

var webhookKeyColumns = []string{"external_key", "id", "status", "system", "tenant_id"}

type webhookTarget struct{ Tenant, Conn string }

// webhookTargets calls the function as invoice_app. guc nil leaves app.current_tenant unset.
func webhookTargets(t *testing.T, guc *string, system, key string) []webhookTarget {
	t.Helper()
	ctx := context.Background()
	read := func(q erpQuerier) ([]webhookTarget, error) {
		rows, err := q.Query(ctx, `SELECT tenant_id::text, connection_id::text FROM public.erp_webhook_targets($1, $2)`, system, key)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		var out []webhookTarget
		for rows.Next() {
			var w webhookTarget
			if err := rows.Scan(&w.Tenant, &w.Conn); err != nil {
				return nil, err
			}
			out = append(out, w)
		}
		return out, rows.Err()
	}
	var (
		got []webhookTarget
		err error
	)
	if guc == nil {
		got, err = read(h.app)
	} else {
		err = db.WithinTenantTx(ctx, h.app, *guc, func(tx pgx.Tx) error {
			var e error
			got, e = read(tx)
			return e
		})
	}
	if c := pgCode(err); c == "42883" {
		t.Fatalf("%s does not exist — the erp_webhook_targets migration is not applied yet: %v", webhookTargetsSig, err)
	}
	if err != nil {
		t.Fatalf("call %s(%q, %q): %v", webhookTargetsSig, system, key, err)
	}
	return got
}

// erpSeedConnID inserts one connection with an explicit id as the superuser.
func erpSeedConnID(t *testing.T, id, tenant, entity, system, key, grant, status string) {
	t.Helper()
	var g, k *string
	if grant != "" {
		g = &grant
	}
	if key != "" {
		k = &key
	}
	_, err := h.super.Exec(context.Background(),
		`INSERT INTO erp_connections (id, tenant_id, entity_id, system, external_key, status, grant_id)
		 VALUES ($1, $2, $3, $4, $5, $6, $7)`, id, tenant, entity, system, k, status, g)
	failIfErpUndefined(t, "seed erp_connections", err)
	if err != nil {
		t.Fatalf("seed erp_connections: %v", err)
	}
}

func webhookKey() string { return "fake-co-" + uuid.NewString() }

func TestRLS_ErpWebhookTargets_IsAnExactDefinerFunction(t *testing.T) {
	requireHarness(t)
	s, ok := readProc(t, webhookTargetsSig)
	if !ok {
		t.Fatalf("function %s does not exist — the erp_webhook_targets migration is not applied yet", webhookTargetsSig)
	}
	if s.owner != "auth_hook_reader" {
		t.Errorf("owner = %q, want auth_hook_reader", s.owner)
	}
	if !s.secdef {
		t.Errorf("prosecdef = false, want SECURITY DEFINER")
	}
	if s.volatile != "s" {
		t.Errorf("provolatile = %q, want s (STABLE)", s.volatile)
	}
	if !reflect.DeepEqual(s.config, []string{`search_path=""`}) {
		t.Errorf("proconfig = %q, want [search_path=\"\"]", s.config)
	}
	var result string
	if err := h.super.QueryRow(context.Background(),
		`SELECT pg_get_function_result(to_regprocedure($1))`, webhookTargetsSig).Scan(&result); err != nil {
		t.Fatalf("pg_get_function_result: %v", err)
	}
	if want := "TABLE(tenant_id uuid, connection_id uuid)"; result != want {
		t.Errorf("result = %q, want %q", result, want)
	}
}

func TestRLS_ErpWebhookTargets_MatchesOnlyTheExactSystemAndKey(t *testing.T) {
	requireHarness(t)
	a := erpTenant(t)
	grant := erpSeedGrant(t, a)
	key := webhookKey()
	fakeConn := erpSeedConn(t, a, erpEntity(t, a, "Webhook Exact Fake"), "fake", erpStr(key), erpStr(grant), "active")
	// Same key under another system: only the system clause tells the two apart.
	otherConn := erpSeedConn(t, a, erpEntity(t, a, "Webhook Exact Other"), "other", erpStr(key), erpStr(grant), "active")

	if got, want := webhookTargets(t, nil, "fake", key), []webhookTarget{{a, fakeConn}}; !slices.Equal(got, want) {
		t.Fatalf("(fake, key) = %v, want exactly %v", got, want)
	}
	if got, want := webhookTargets(t, nil, "other", key), []webhookTarget{{a, otherConn}}; !slices.Equal(got, want) {
		t.Fatalf("(other, key) = %v, want exactly %v", got, want)
	}
	for _, c := range []struct{ what, system, key string }{
		{"upper-case key", "fake", strings.ToUpper(key)},
		{"key prefix", "fake", key[:len(key)-1]},
		{"key with a trailing space", "fake", key + " "},
		{"LIKE wildcard %", "fake", "%"},
		{"LIKE wildcard _ in the last place", "fake", key[:len(key)-1] + "_"},
		{"empty key", "fake", ""},
		{"upper-case system", "FAKE", key},
		{"unknown system", "nosuch", key},
		{"empty system", "", key},
	} {
		if got := webhookTargets(t, nil, c.system, c.key); len(got) != 0 {
			t.Errorf("%s: (%q, %q) = %v, want no row", c.what, c.system, c.key, got)
		}
	}
}

func TestRLS_ErpWebhookTargets_ReturnsOnlyActiveConnections(t *testing.T) {
	requireHarness(t)
	a := erpTenant(t)
	grant := erpSeedGrant(t, a)
	key := webhookKey()
	statuses := []string{"pending", "reconsent", "disconnected"}
	ids := make([]string, len(statuses))
	for i, st := range statuses {
		ids[i] = erpSeedConn(t, a, erpEntity(t, a, "Webhook Status "+st), "fake", erpStr(key), erpStr(grant), st)
	}
	if got := webhookTargets(t, nil, "fake", key); len(got) != 0 {
		t.Fatalf("pending, reconsent and disconnected = %v, want no row", got)
	}
	// Flip the middle one: exactly it comes back, not all three nor the first.
	if _, err := h.super.Exec(context.Background(), `UPDATE erp_connections SET status = 'active' WHERE id = $1`, ids[1]); err != nil {
		t.Fatalf("activate connection: %v", err)
	}
	if got, want := webhookTargets(t, nil, "fake", key), []webhookTarget{{a, ids[1]}}; !slices.Equal(got, want) {
		t.Fatalf("one active of three = %v, want exactly %v", got, want)
	}
}

func TestRLS_ErpWebhookTargets_ReturnsEveryTenantsConnectionForOneKey(t *testing.T) {
	requireHarness(t)
	x, y := erpTenant(t), erpTenant(t)
	lo, hi := min(x, y), max(x, y)
	gLo, gHi := erpSeedGrant(t, lo), erpSeedGrant(t, hi)
	key := webhookKey()
	// Ids chosen so (tenant_id, id) disagrees with insertion order, with id alone and with tenant_id alone.
	tail := uuid.NewString()[1:]
	idF, idE, id0 := "f"+tail, "e"+tail, "0"+tail
	erpSeedConnID(t, idF, lo, erpEntity(t, lo, "Webhook Lo 1"), "fake", key, gLo, "active")
	erpSeedConnID(t, idE, lo, erpEntity(t, lo, "Webhook Lo 2"), "fake", key, gLo, "active")
	erpSeedConnID(t, id0, hi, erpEntity(t, hi, "Webhook Hi 1"), "fake", key, gHi, "active")
	want := []webhookTarget{{lo, idE}, {lo, idF}, {hi, id0}}

	nilT := uuid.Nil.String()
	for _, c := range []struct {
		what string
		guc  *string
	}{
		{"GUC unset", nil},
		{"GUC nil UUID", &nilT},
		{"GUC = lower tenant", &lo},
		{"GUC = higher tenant", &hi},
	} {
		if got := webhookTargets(t, c.guc, "fake", key); !slices.Equal(got, want) {
			t.Errorf("%s: rows = %v, want %v (every tenant, ordered by tenant_id, id)", c.what, got, want)
		}
	}
}

func TestRLS_ErpWebhookTargets_ExecuteIsInvoiceAppOnly(t *testing.T) {
	requireHarness(t)
	s, ok := readProc(t, webhookTargetsSig)
	if !ok {
		t.Fatalf("function %s does not exist — the erp_webhook_targets migration is not applied yet", webhookTargetsSig)
	}
	assertExecuteOnlyForApp(t, webhookTargetsSig, s, "auth_hook_reader")
	for _, c := range []struct {
		role string
		want bool
	}{{"invoice_app", true}, {"invoice_gateway", false}, {"invoice_tenant_reader", false}, {"staff_reader", false}} {
		var got bool
		if err := h.super.QueryRow(context.Background(),
			`SELECT has_function_privilege($1, to_regprocedure($2), 'EXECUTE')`, c.role, webhookTargetsSig).Scan(&got); err != nil {
			t.Fatalf("has_function_privilege(%s): %v", c.role, err)
		}
		if got != c.want {
			t.Errorf("%s EXECUTE on %s = %v, want %v", c.role, webhookTargetsSig, got, c.want)
		}
	}

	// A call as another role is refused with 42501, not 42883: the function exists.
	for _, role := range []string{"invoice_gateway", "invoice_tenant_reader", "staff_reader"} {
		ctx := context.Background()
		tx, err := h.super.Begin(ctx)
		if err != nil {
			t.Fatalf("begin: %v", err)
		}
		if _, err := tx.Exec(ctx, "SET LOCAL ROLE "+role); err != nil {
			_ = tx.Rollback(ctx)
			t.Fatalf("SET LOCAL ROLE %s: %v", role, err)
		}
		_, err = tx.Exec(ctx, `SELECT * FROM public.erp_webhook_targets('fake', 'x')`)
		_ = tx.Rollback(ctx)
		assertPgRefusal(t, role+" calls "+webhookTargetsSig, err, "42501", "erp_webhook_targets")
	}
}

func TestRLS_ErpWebhookTargets_LookupRoleReadsOnlyTheKeyColumns(t *testing.T) {
	requireHarness(t)
	ctx := context.Background()
	a := erpTenant(t)
	erpSeedConn(t, a, erpEntity(t, a, "Webhook Cols"), "fake", nil, nil, "pending")

	rows, err := h.super.Query(ctx, `
		SELECT attname FROM pg_attribute
		 WHERE attrelid = 'public.erp_connections'::regclass AND attnum > 0 AND NOT attisdropped
		   AND has_column_privilege('auth_hook_reader', 'public.erp_connections', attname, 'SELECT')
		 ORDER BY attname`)
	if err != nil {
		t.Fatalf("read column privileges: %v", err)
	}
	var readable []string
	for rows.Next() {
		var c string
		if err := rows.Scan(&c); err != nil {
			rows.Close()
			t.Fatalf("scan: %v", err)
		}
		readable = append(readable, c)
	}
	rows.Close()
	if !slices.Equal(readable, webhookKeyColumns) {
		t.Errorf("auth_hook_reader reads columns %v of erp_connections, want exactly %v", readable, webhookKeyColumns)
	}

	for _, priv := range []string{"SELECT", "INSERT", "UPDATE", "DELETE", "TRUNCATE", "REFERENCES", "TRIGGER"} {
		var has bool
		if err := h.super.QueryRow(ctx, `SELECT has_table_privilege('auth_hook_reader', 'public.erp_connections', $1)`, priv).Scan(&has); err != nil {
			t.Fatalf("has_table_privilege %s: %v", priv, err)
		}
		if has {
			t.Errorf("auth_hook_reader holds table-level %s on erp_connections, want none", priv)
		}
	}
	// Column-level writes: any column granted INSERT, UPDATE or REFERENCES.
	for _, priv := range []string{"INSERT", "UPDATE", "REFERENCES"} {
		var cols []string
		if err := h.super.QueryRow(ctx, `
			SELECT coalesce(array_agg(attname ORDER BY attname), '{}') FROM pg_attribute
			 WHERE attrelid = 'public.erp_connections'::regclass AND attnum > 0 AND NOT attisdropped
			   AND has_column_privilege('auth_hook_reader', 'public.erp_connections', attname, $1)`, priv).Scan(&cols); err != nil {
			t.Fatalf("column %s privileges: %v", priv, err)
		}
		if len(cols) != 0 {
			t.Errorf("auth_hook_reader holds column-level %s on %v, want none", priv, cols)
		}
	}

	// The migrator reaches the role through SET ROLE (the Down relies on it); the grant is what limits it.
	asLookupRole := func(sql string) (int, error) {
		tx, err := h.mig.Begin(ctx)
		if err != nil {
			return 0, err
		}
		defer func() { _ = tx.Rollback(ctx) }()
		if _, err := tx.Exec(ctx, `SET LOCAL ROLE auth_hook_reader`); err != nil {
			return 0, err
		}
		var n int
		err = tx.QueryRow(ctx, sql, a).Scan(&n)
		return n, err
	}
	n, err := asLookupRole(`SELECT count(*) FROM (SELECT id, tenant_id, system, external_key, status FROM erp_connections WHERE tenant_id = $1) q`)
	if err != nil {
		t.Fatalf("auth_hook_reader reads the five key columns: %v", err)
	}
	if n != 1 {
		t.Errorf("auth_hook_reader sees %d rows of the tenant's connections, want 1 (the lookup policy)", n)
	}
	for _, col := range []string{"cursor", "grant_id", "entity_id", "last_sync_at", "created_at", "updated_at"} {
		_, err := asLookupRole(`SELECT count(` + col + `) FROM erp_connections WHERE tenant_id = $1`)
		assertPgRefusal(t, "auth_hook_reader reads erp_connections."+col, err, "42501", "erp_connections")
	}
}

func TestRLS_ErpWebhookTargets_PolicyLeavesInvoiceAppIsolated(t *testing.T) {
	requireHarness(t)
	ctx := context.Background()
	a, b := erpTenant(t), erpTenant(t)
	ea1, ea2, eb := erpEntity(t, a, "Webhook Iso A1"), erpEntity(t, a, "Webhook Iso A2"), erpEntity(t, b, "Webhook Iso B")
	gA, gB := erpSeedGrant(t, a), erpSeedGrant(t, b)
	key := webhookKey()
	a1 := erpSeedConn(t, a, ea1, "fake", erpStr(key), erpStr(gA), "active")
	a2 := erpSeedConn(t, a, ea2, "fake", erpStr(key), erpStr(gA), "active")
	b1 := erpSeedConn(t, b, eb, "fake", erpStr(key), erpStr(gB), "active")

	if got, want := erpAppIDs(t, a, nil, "erp_connections"), sortedIDs(a1, a2); !slices.Equal(got, want) {
		t.Errorf("tenant A sees %v, want exactly its own %v", got, want)
	}
	if got, want := erpAppIDs(t, b, nil, "erp_connections"), []string{b1}; !slices.Equal(got, want) {
		t.Errorf("tenant B sees %v, want exactly its own %v", got, want)
	}
	if got, want := erpAppIDs(t, a, erpStr("{"+ea1+"}"), "erp_connections"), []string{a1}; !slices.Equal(got, want) {
		t.Errorf("tenant A limited to one client sees %v, want %v", got, want)
	}
	if got := erpAppIDs(t, uuid.Nil.String(), nil, "erp_connections"); len(got) != 0 {
		t.Errorf("nil-UUID tenant sees %v, want none", got)
	}
	var unset int
	if err := h.app.QueryRow(ctx, `SELECT count(*) FROM erp_connections WHERE id = ANY ($1::uuid[])`, []string{a1, a2, b1}).Scan(&unset); err != nil {
		t.Fatalf("read with the GUC unset: %v", err)
	}
	if unset != 0 {
		t.Errorf("GUC unset: invoice_app sees %d rows, want 0", unset)
	}

	rows, err := h.super.Query(ctx, `
		SELECT policyname, cmd, roles::text[], qual FROM pg_policies
		 WHERE schemaname = 'public' AND tablename = 'erp_connections' AND policyname = 'erp_connection_webhook_lookup'`)
	if err != nil {
		t.Fatalf("read pg_policies: %v", err)
	}
	defer rows.Close()
	type pol struct {
		name, cmd, qual string
		roles           []string
	}
	var got []pol
	for rows.Next() {
		var p pol
		if err := rows.Scan(&p.name, &p.cmd, &p.roles, &p.qual); err != nil {
			t.Fatalf("scan: %v", err)
		}
		got = append(got, p)
	}
	if want := (pol{"erp_connection_webhook_lookup", "SELECT", "true", []string{"auth_hook_reader"}}); len(got) != 1 || !reflect.DeepEqual(got[0], want) {
		t.Errorf("policy erp_connection_webhook_lookup = %+v, want exactly [%+v]", got, want)
	}
	var roles []string
	if err := h.super.QueryRow(ctx, `
		SELECT coalesce(array_agg(DISTINCT r ORDER BY r), '{}') FROM pg_policies, unnest(roles::text[]) r
		 WHERE schemaname = 'public' AND tablename = 'erp_connections' AND policyname <> 'erp_connection_webhook_lookup'`).Scan(&roles); err != nil {
		t.Fatalf("read the other policies' roles: %v", err)
	}
	if want := []string{"public"}; !slices.Equal(roles, want) {
		t.Errorf("roles of the other erp_connections policies = %v, want %v", roles, want)
	}
}

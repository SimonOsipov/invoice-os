// Storage suite for the client scope: app.entity_scope limits six tenant tables to a set of clients.
//
// Rows are seeded per test as the superuser, never in the shared harness.seed(). A skip is a failure
// under the CI gate. Unset or ” reads every client, '*' reads every client, '{ids}' reads those clients.
package db_test

import (
	"context"
	"fmt"
	"io/fs"
	"slices"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/SimonOsipov/invoice-os/internal/platform/db"
	"github.com/SimonOsipov/invoice-os/migrations"
)

const clientScopeMigrationGlob = "*_client_scope.sql"

// csClient is one client (business entity) of a seeded tenant with the rows that hang off it.
type csClient struct {
	entity, invoice, batch, mapping string
	lines, history                  []string
}

type csTenant struct {
	id      string
	clients []*csClient
}

// csTable names one scoped table and picks a client's row ids in it.
type csTable struct {
	name string
	ids  func(c *csClient) []string
}

var csTables = []csTable{
	{"business_entities", func(c *csClient) []string { return []string{c.entity} }},
	{"invoices", func(c *csClient) []string { return []string{c.invoice} }},
	{"line_items", func(c *csClient) []string { return c.lines }},
	{"invoice_status_history", func(c *csClient) []string { return c.history }},
	{"import_batches", func(c *csClient) []string { return []string{c.batch} }},
	{"import_mappings", func(c *csClient) []string { return []string{c.mapping} }},
}

// csSeed seeds a throwaway tenant: one client per shape entry {line items, history rows}, each with
// one invoice, one import batch and one import mapping. Sweeps its rows, children first.
func csSeed(t *testing.T, shapes ...[2]int) *csTenant {
	t.Helper()
	ctx := context.Background()
	ten := &csTenant{id: caThrowawayTenant(t)}
	t.Cleanup(func() {
		for _, tb := range []string{"invoice_status_history", "line_items", "import_mappings",
			"import_batches", "invoices", "business_entities"} {
			_, _ = h.super.Exec(ctx, `DELETE FROM `+tb+` WHERE tenant_id = $1`, ten.id)
		}
	})
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := h.super.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("seed: %v\n%s", err, sql)
		}
	}
	for i, shape := range shapes {
		c := &csClient{entity: uuid.NewString(), invoice: uuid.NewString(),
			batch: uuid.NewString(), mapping: uuid.NewString()}
		exec(`INSERT INTO business_entities (id, tenant_id, name) VALUES ($1, $2, $3)`,
			c.entity, ten.id, fmt.Sprintf("cs-client-%d", i))
		exec(`INSERT INTO invoices (id, tenant_id, entity_id, invoice_number) VALUES ($1, $2, $3, $4)`,
			c.invoice, ten.id, c.entity, "cs-"+uuid.NewString()[:8])
		for n := 1; n <= shape[0]; n++ {
			id := uuid.NewString()
			exec(`INSERT INTO line_items (id, tenant_id, invoice_id, line_no) VALUES ($1, $2, $3, $4)`,
				id, ten.id, c.invoice, n)
			c.lines = append(c.lines, id)
		}
		for n := 0; n < shape[1]; n++ {
			id := uuid.NewString()
			exec(`INSERT INTO invoice_status_history (id, tenant_id, invoice_id, from_status, to_status, actor)
				VALUES ($1, $2, $3, NULL, 'validated', 'system')`, id, ten.id, c.invoice)
			c.history = append(c.history, id)
		}
		exec(`INSERT INTO import_batches (id, tenant_id, entity_id) VALUES ($1, $2, $3)`, c.batch, ten.id, c.entity)
		exec(`INSERT INTO import_mappings (id, tenant_id, entity_id, column_signature, mapping)
			VALUES ($1, $2, $3, $4, $5::jsonb)`, c.mapping, ten.id, c.entity, docHash(), imMapping)
		ten.clients = append(ten.clients, c)
	}
	// Control: every zero and every "only A" below means nothing unless the rows exist.
	for _, tb := range csTables {
		want := 0
		for _, c := range ten.clients {
			want += len(tb.ids(c))
		}
		if want == 0 {
			t.Fatalf("%s: the seed shape gives no rows", tb.name)
		}
		if n := mustCount(t, h.super, `SELECT count(*) FROM `+tb.name+` WHERE tenant_id = $1`, ten.id); n != want {
			t.Fatalf("%s: superuser sees %d seeded rows, want %d", tb.name, n, want)
		}
	}
	return ten
}

func csDefaultShape(n int) [][2]int {
	s := make([][2]int, n)
	for i := range s {
		s[i] = [2]int{1, 1}
	}
	return s
}

func csSeedClients(t *testing.T, n int) *csTenant {
	t.Helper()
	return csSeed(t, csDefaultShape(n)...)
}

// csSet renders the scope literal for the given clients: {id,id}.
func csSet(clients ...*csClient) string {
	ids := make([]string, len(clients))
	for i, c := range clients {
		ids[i] = c.entity
	}
	return "{" + strings.Join(ids, ",") + "}"
}

func csWant(tb csTable, clients ...*csClient) []string {
	var want []string
	for _, c := range clients {
		want = append(want, tb.ids(c)...)
	}
	slices.Sort(want)
	return want
}

// csReadIDs returns the sorted ids tb shows to invoice_app under tenant, with scope set when non-nil.
func csReadIDs(t *testing.T, tenant string, scope *string, tb csTable) []string {
	t.Helper()
	var got []string
	csInTx(t, tenant, scope, func(tx pgx.Tx) error {
		ids, err := csQueryIDs(tx, tb.name)
		got = ids
		return err
	})
	return got
}

func csQueryIDs(tx pgx.Tx, table string) ([]string, error) {
	rows, err := tx.Query(context.Background(), `SELECT id::text FROM `+table)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	slices.Sort(ids)
	return ids, rows.Err()
}

// csTx opens the tenant transaction as invoice_app and sets the scope inside it, the way the seam will.
func csTx(tenant string, scope *string, fn func(tx pgx.Tx) error) error {
	ctx := context.Background()
	return db.WithinTenantTx(ctx, h.app, tenant, func(tx pgx.Tx) error {
		if scope != nil {
			if _, err := tx.Exec(ctx, `SELECT set_config('app.entity_scope', $1, true)`, *scope); err != nil {
				return err
			}
		}
		return fn(tx)
	})
}

func csInTx(t *testing.T, tenant string, scope *string, fn func(tx pgx.Tx) error) {
	t.Helper()
	if err := csTx(tenant, scope, fn); err != nil {
		t.Fatalf("read as invoice_app (scope %v): %v", csScopeStr(scope), err)
	}
}

func csScopeStr(s *string) string {
	if s == nil {
		return "<unset>"
	}
	return fmt.Sprintf("%q", *s)
}

func csScope(s string) *string { return &s }

// csExpectIDs asserts tb shows exactly want under the scope; want must be non-empty unless wantNone.
func csExpectIDs(t *testing.T, tenant string, scope *string, tb csTable, want []string) {
	t.Helper()
	got := csReadIDs(t, tenant, scope, tb)
	if !slices.Equal(got, want) {
		t.Errorf("%s under scope %s: ids = %v, want %v", tb.name, csScopeStr(scope), got, want)
	}
}

func TestRLS_ClientScopeLimitsEachTableToTheSet(t *testing.T) {
	requireHarness(t)
	ten := csSeedClients(t, 2)
	a, b := ten.clients[0], ten.clients[1]

	for _, tb := range csTables {
		t.Run(tb.name, func(t *testing.T) {
			want := csWant(tb, a)
			if len(want) == 0 {
				t.Fatalf("no expected rows for client A in %s", tb.name)
			}
			got := csReadIDs(t, ten.id, csScope(csSet(a)), tb)
			if !slices.Equal(got, want) {
				t.Errorf("%s under {A}: ids = %v, want exactly A's %v (B's are %v)", tb.name, got, want, csWant(tb, b))
			}
		})
	}
}

func TestRLS_ClientScopeEmptySetSeesNoClient(t *testing.T) {
	requireHarness(t)
	ten := csSeedClients(t, 2)

	for _, tb := range csTables {
		t.Run(tb.name, func(t *testing.T) {
			if got := csReadIDs(t, ten.id, csScope("{}"), tb); len(got) != 0 {
				t.Errorf("%s under {}: ids = %v, want none", tb.name, got)
			}
		})
	}
}

func TestRLS_ClientScopeStarSeesEveryClient(t *testing.T) {
	requireHarness(t)
	ten := csSeedClients(t, 2)

	for _, tb := range csTables {
		t.Run(tb.name, func(t *testing.T) {
			csExpectIDs(t, ten.id, csScope("*"), tb, csWant(tb, ten.clients...))
		})
	}
}

// A set of two discriminates "first id only" and "last id only".
func TestRLS_ClientScopeTwoOfThreeClients(t *testing.T) {
	requireHarness(t)
	ten := csSeedClients(t, 3)
	a, b, c := ten.clients[0], ten.clients[1], ten.clients[2]

	for _, tb := range csTables {
		t.Run(tb.name, func(t *testing.T) {
			want := csWant(tb, a, c)
			if len(want) < 2 {
				t.Fatalf("expected rows for A and C in %s = %v, want at least 2", tb.name, want)
			}
			got := csReadIDs(t, ten.id, csScope(csSet(a, c)), tb)
			if !slices.Equal(got, want) {
				t.Errorf("%s under {A,C}: ids = %v, want %v (B's are %v)", tb.name, got, want, csWant(tb, b))
			}
		})
	}
}

func TestRLS_ClientScopeUnsetReadsEveryClient(t *testing.T) {
	requireHarness(t)
	ten := csSeedClients(t, 2)

	for _, tb := range csTables {
		t.Run(tb.name, func(t *testing.T) {
			csExpectIDs(t, ten.id, nil, tb, csWant(tb, ten.clients...))
		})
	}
}

// A transaction-local set leaves ” (not NULL) behind on the connection; ” must read like unset.
func TestRLS_ClientScopeEmptyStringAfterALocalSetReadsEveryClient(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()
	ten := csSeedClients(t, 2)

	conn, err := pgx.Connect(ctx, h.appURL)
	if err != nil {
		t.Fatalf("connect as invoice_app: %v", err)
	}
	defer func() { _ = conn.Close(ctx) }()

	tx1, err := conn.Begin(ctx)
	if err != nil {
		t.Fatalf("begin tx 1: %v", err)
	}
	for _, kv := range [][2]string{{"app.current_tenant", ten.id}, {"app.entity_scope", csSet(ten.clients[0])}} {
		if _, err := tx1.Exec(ctx, `SELECT set_config($1, $2, true)`, kv[0], kv[1]); err != nil {
			t.Fatalf("tx 1 set_config %s: %v", kv[0], err)
		}
	}
	if err := tx1.Commit(ctx); err != nil {
		t.Fatalf("commit tx 1: %v", err)
	}

	tx2, err := conn.Begin(ctx)
	if err != nil {
		t.Fatalf("begin tx 2: %v", err)
	}
	defer func() { _ = tx2.Rollback(ctx) }()
	if _, err := tx2.Exec(ctx, `SELECT set_config('app.current_tenant', $1, true)`, ten.id); err != nil {
		t.Fatalf("tx 2 set tenant: %v", err)
	}
	var scope *string
	if err := tx2.QueryRow(ctx, `SELECT current_setting('app.entity_scope', true)`).Scan(&scope); err != nil {
		t.Fatalf("read app.entity_scope in tx 2: %v", err)
	}
	if scope == nil || *scope != "" {
		t.Fatalf("app.entity_scope in tx 2 = %v, want '' (the leftover of tx 1's local set)", scope)
	}
	for _, tb := range csTables {
		got, err := csQueryIDs(tx2, tb.name)
		if err != nil {
			t.Fatalf("%s: %v", tb.name, err)
		}
		if want := csWant(tb, ten.clients...); !slices.Equal(got, want) {
			t.Errorf("%s with scope '': ids = %v, want every client's %v", tb.name, got, want)
		}
	}
}

func TestRLS_ClientScopeChildRowsFollowTheirInvoice(t *testing.T) {
	requireHarness(t)
	ten := csSeed(t, [2]int{2, 1}, [2]int{3, 2})
	a, b := ten.clients[0], ten.clients[1]

	for _, tc := range []struct {
		table string
		want  int
		own   []string
		other []string
	}{
		{"line_items", 2, a.lines, b.lines},
		{"invoice_status_history", 1, a.history, b.history},
	} {
		t.Run(tc.table, func(t *testing.T) {
			var got []string
			var invoices []string
			csInTx(t, ten.id, csScope(csSet(a)), func(tx pgx.Tx) error {
				var err error
				if got, err = csQueryIDs(tx, tc.table); err != nil {
					return err
				}
				rows, err := tx.Query(context.Background(), `SELECT DISTINCT invoice_id::text FROM `+tc.table)
				if err != nil {
					return err
				}
				defer rows.Close()
				for rows.Next() {
					var inv string
					if err := rows.Scan(&inv); err != nil {
						return err
					}
					invoices = append(invoices, inv)
				}
				return rows.Err()
			})
			if len(got) != tc.want || !slices.Equal(got, sortedCopy(tc.own)) {
				t.Errorf("%s under {A}: ids = %v, want A's %d rows %v", tc.table, got, tc.want, tc.own)
			}
			for _, id := range got {
				if slices.Contains(tc.other, id) {
					t.Errorf("%s under {A}: B's invoice row %s is visible", tc.table, id)
				}
			}
			if len(invoices) != 1 || invoices[0] != a.invoice {
				t.Errorf("%s under {A}: invoice ids = %v, want only A's %s", tc.table, invoices, a.invoice)
			}
		})
	}
}

// csInsertCases builds one INSERT per table for the given tenant, client entity and invoice.
func csInsertCases(tenant, entity, invoice string) []struct {
	name, sql string
	args      []any
} {
	return []struct {
		name, sql string
		args      []any
	}{
		{"invoices", `INSERT INTO invoices (id, tenant_id, entity_id, invoice_number) VALUES ($1, $2, $3, $4)`,
			[]any{uuid.NewString(), tenant, entity, "cs-" + uuid.NewString()[:8]}},
		{"import_batches", `INSERT INTO import_batches (id, tenant_id, entity_id) VALUES ($1, $2, $3)`,
			[]any{uuid.NewString(), tenant, entity}},
		{"import_mappings", `INSERT INTO import_mappings (id, tenant_id, entity_id, column_signature, mapping)
			VALUES ($1, $2, $3, $4, $5::jsonb)`,
			[]any{uuid.NewString(), tenant, entity, docHash(), imMapping}},
		{"line_items", `INSERT INTO line_items (id, tenant_id, invoice_id, line_no) VALUES ($1, $2, $3, 99)`,
			[]any{uuid.NewString(), tenant, invoice}},
		{"invoice_status_history", `INSERT INTO invoice_status_history (id, tenant_id, invoice_id, from_status, to_status, actor)
			VALUES ($1, $2, $3, NULL, 'validated', 'system')`,
			[]any{uuid.NewString(), tenant, invoice}},
	}
}

func csAssertRLSRefusal(t *testing.T, what string, err error) {
	t.Helper()
	if !isRLSViolation(err) {
		t.Errorf("%s: err = %v, want SQLSTATE 42501", what, err)
		return
	}
	if msg := pgMessage(err); !strings.Contains(msg, "row-level security") {
		t.Errorf("%s: message = %q, want a row-level security violation", what, msg)
	}
}

func TestRLS_ClientScopeInsertOutsideTheSetRefused(t *testing.T) {
	requireHarness(t)
	ctx := context.Background()
	ten := csSeedClients(t, 2)
	a, b := ten.clients[0], ten.clients[1]

	cases := csInsertCases(ten.id, b.entity, b.invoice)
	if len(cases) != 5 {
		t.Fatalf("insert cases = %d, want 5", len(cases))
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := csTx(ten.id, csScope(csSet(a)), func(tx pgx.Tx) error {
				_, e := tx.Exec(ctx, tc.sql, tc.args...)
				return e
			})
			csAssertRLSRefusal(t, "INSERT into "+tc.name+" naming B under {A}", err)
			if n := mustCount(t, h.super, `SELECT count(*) FROM `+tc.name+` WHERE id = $1`, tc.args[0]); n != 0 {
				t.Errorf("%s: rows left by the refused INSERT = %d, want 0", tc.name, n)
			}
		})
	}
}

func TestRLS_ClientScopeMoveOutsideTheSetRefused(t *testing.T) {
	requireHarness(t)
	ctx := context.Background()
	ten := csSeedClients(t, 2)
	a, b := ten.clients[0], ten.clients[1]

	for _, tc := range []struct{ table, id string }{
		{"invoices", a.invoice}, {"import_batches", a.batch}, {"import_mappings", a.mapping},
	} {
		t.Run(tc.table, func(t *testing.T) {
			err := csTx(ten.id, csScope(csSet(a)), func(tx pgx.Tx) error {
				_, e := tx.Exec(ctx, `UPDATE `+tc.table+` SET entity_id = $1 WHERE id = $2`, b.entity, tc.id)
				return e
			})
			csAssertRLSRefusal(t, "UPDATE "+tc.table+" moving A's row to B under {A}", err)
			var entity string
			if e := h.super.QueryRow(ctx, `SELECT entity_id::text FROM `+tc.table+` WHERE id = $1`, tc.id).Scan(&entity); e != nil {
				t.Fatalf("read %s row as superuser: %v", tc.table, e)
			}
			if entity != a.entity {
				t.Errorf("%s row entity_id = %s after the refused UPDATE, want A's %s", tc.table, entity, a.entity)
			}
		})
	}
}

// Control for the refusals: the same five statement shapes naming A succeed under {A}.
func TestRLS_ClientScopeInsertInsideTheSetAllowed(t *testing.T) {
	requireHarness(t)
	ctx := context.Background()
	ten := csSeedClients(t, 2)
	a := ten.clients[0]

	for _, tc := range csInsertCases(ten.id, a.entity, a.invoice) {
		t.Run(tc.name, func(t *testing.T) {
			err := csTx(ten.id, csScope(csSet(a)), func(tx pgx.Tx) error {
				_, e := tx.Exec(ctx, tc.sql, tc.args...)
				return e
			})
			if err != nil {
				t.Fatalf("INSERT into %s naming A under {A}: %v", tc.name, err)
			}
			if n := mustCount(t, h.super, `SELECT count(*) FROM `+tc.name+` WHERE id = $1`, tc.args[0]); n != 1 {
				t.Errorf("%s: rows after the allowed INSERT = %d, want 1", tc.name, n)
			}
		})
	}
}

func TestRLS_ClientScopeKeepsTheTenantPredicate(t *testing.T) {
	requireHarness(t)
	one := csSeedClients(t, 2)
	two := csSeedClients(t, 2)

	for _, tb := range csTables {
		t.Run(tb.name, func(t *testing.T) {
			own := csWant(tb, one.clients...)
			if len(own) == 0 {
				t.Fatalf("no rows seeded for tenant one in %s", tb.name)
			}
			got := csReadIDs(t, one.id, csScope("*"), tb)
			if !slices.Equal(got, own) {
				t.Errorf("%s under * for tenant one: ids = %v, want only its own %v", tb.name, got, own)
			}
			foreign := csWant(tb, two.clients...)
			for _, id := range got {
				if slices.Contains(foreign, id) {
					t.Errorf("%s under *: tenant two's row %s is visible to tenant one", tb.name, id)
				}
			}
			if got := csReadIDs(t, one.id, csScope(csSet(two.clients[0])), tb); len(got) != 0 {
				t.Errorf("%s under tenant two's client set for tenant one: ids = %v, want none", tb.name, got)
			}
		})
	}
}

func TestRLS_ClientScopePoliciesDeclared(t *testing.T) {
	requireHarness(t)
	ctx := context.Background()

	type policy struct {
		permissive, roles, cmd string
		qual, withCheck        *string
	}
	read := func(table string) map[string][]policy {
		t.Helper()
		rows, err := h.super.Query(ctx,
			`SELECT policyname, permissive, roles::text, cmd, qual, with_check FROM pg_policies
			  WHERE schemaname = 'public' AND tablename = $1`, table)
		if err != nil {
			t.Fatalf("query pg_policies for %s: %v", table, err)
		}
		defer rows.Close()
		got := map[string][]policy{}
		for rows.Next() {
			var name string
			var p policy
			if err := rows.Scan(&name, &p.permissive, &p.roles, &p.cmd, &p.qual, &p.withCheck); err != nil {
				t.Fatalf("scan pg_policies row for %s: %v", table, err)
			}
			got[name] = append(got[name], p)
		}
		if err := rows.Err(); err != nil {
			t.Fatalf("read pg_policies for %s: %v", table, err)
		}
		return got
	}

	for _, tb := range csTables {
		t.Run(tb.name, func(t *testing.T) {
			ps := read(tb.name)["tenant_isolation"]
			if len(ps) != 1 {
				t.Fatalf("tenant_isolation policies on %s = %d, want exactly 1", tb.name, len(ps))
			}
			p := ps[0]
			if p.permissive != "PERMISSIVE" || p.roles != "{public}" {
				t.Errorf("%s tenant_isolation = %s for %s, want PERMISSIVE for {public}", tb.name, p.permissive, p.roles)
			}
			if p.withCheck != nil {
				t.Errorf("%s tenant_isolation has a separate WITH CHECK %q, want none (USING governs writes)", tb.name, *p.withCheck)
			}
			if p.qual == nil || !strings.Contains(*p.qual, "app.current_tenant") {
				t.Errorf("%s tenant_isolation qual = %v, want the tenant predicate kept", tb.name, p.qual)
			} else if !strings.Contains(*p.qual, "app.entity_scope") {
				t.Errorf("%s tenant_isolation qual lacks app.entity_scope: %s", tb.name, *p.qual)
			}
		})
	}

	for _, table := range []string{"invoices", "invoice_status_history"} {
		ps := read(table)["staff_read_lookup"]
		if len(ps) != 1 {
			t.Errorf("staff_read_lookup policies on %s = %d, want 1", table, len(ps))
			continue
		}
		if p := ps[0]; p.cmd != "SELECT" || p.roles != "{staff_reader}" || p.qual == nil || *p.qual != "true" {
			t.Errorf("staff_read_lookup on %s = cmd %s roles %s qual %v, want SELECT for {staff_reader} USING true",
				table, p.cmd, p.roles, p.qual)
		}
	}

	// Guards over-scoping: these three keep the tenant-only expression.
	for _, table := range []string{"membership_clients", "invitation_clients", "custom_rule_revisions"} {
		ps := read(table)["tenant_isolation"]
		if len(ps) != 1 || ps[0].qual == nil {
			t.Errorf("tenant_isolation on %s = %v, want exactly 1 with a qual", table, ps)
			continue
		}
		if q := *ps[0].qual; !strings.Contains(q, "app.current_tenant") || strings.Contains(q, "app.entity_scope") {
			t.Errorf("tenant_isolation qual on %s = %s, want the tenant-only expression", table, q)
		}
	}
}

// A hand-built value must error the statement; it must never widen access.
func TestRLS_ClientScopeMalformedValueErrors(t *testing.T) {
	requireHarness(t)
	ten := csSeedClients(t, 2)

	var rows []string
	err := csTx(ten.id, csScope("not-a-set"), func(tx pgx.Tx) error {
		ids, e := csQueryIDs(tx, "invoices")
		rows = ids
		return e
	})
	if c := pgCode(err); c != "22P02" {
		t.Errorf("SELECT on invoices under scope 'not-a-set': err = %v (code %q), want SQLSTATE 22P02", err, c)
	}
	if len(rows) != 0 {
		t.Errorf("SELECT on invoices under scope 'not-a-set' returned %v, want no rows", rows)
	}
}

func TestRLS_ClientScopeMigrationIsReversible(t *testing.T) {
	requireHarness(t)
	ctx := context.Background()

	matches, err := fs.Glob(migrations.FS, clientScopeMigrationGlob)
	if err != nil {
		t.Fatalf("glob %s: %v", clientScopeMigrationGlob, err)
	}
	if len(matches) != 1 {
		t.Fatalf("migrations matching %s = %v, want exactly 1", clientScopeMigrationGlob, matches)
	}
	migration := matches[0]

	tx := migratorTx(t, ctx)
	quals := func() map[string]string {
		t.Helper()
		out := map[string]string{}
		for _, table := range append([]string{"membership_clients"}, csTableNames(csTables)...) {
			var q *string
			if err := tx.QueryRow(ctx,
				`SELECT qual FROM pg_policies WHERE schemaname = 'public' AND tablename = $1 AND policyname = 'tenant_isolation'`,
				table).Scan(&q); err != nil || q == nil {
				t.Fatalf("read tenant_isolation qual of %s: %v (qual %v)", table, err, q)
			}
			out[table] = *q
		}
		return out
	}

	if _, err := tx.Exec(ctx, auditEntitySectionOf(t, migration, "Down")); err != nil {
		t.Fatalf("Down body of %s failed: %v", migration, err)
	}
	down := quals()
	tenantOnly := down["membership_clients"]
	if strings.Contains(tenantOnly, "app.entity_scope") || !strings.Contains(tenantOnly, "app.current_tenant") {
		t.Fatalf("membership_clients qual = %s, want the tenant-only text as the reference", tenantOnly)
	}
	for _, table := range csTableNames(csTables) {
		if down[table] != tenantOnly {
			t.Errorf("after Down, %s qual = %s\nwant the tenant-only text %s", table, down[table], tenantOnly)
		}
	}

	if _, err := tx.Exec(ctx, auditEntitySectionOf(t, migration, "Up")); err != nil {
		t.Fatalf("Up body of %s failed: %v", migration, err)
	}
	up := quals()
	for _, table := range csTableNames(csTables) {
		if !strings.Contains(up[table], "app.entity_scope") {
			t.Errorf("after Up, %s qual lacks app.entity_scope: %s", table, up[table])
		}
	}
	if up["membership_clients"] != tenantOnly {
		t.Errorf("membership_clients qual changed across the migration: %s", up["membership_clients"])
	}
}

func csTableNames(tbs []csTable) []string {
	names := make([]string, len(tbs))
	for i, tb := range tbs {
		names[i] = tb.name
	}
	return names
}

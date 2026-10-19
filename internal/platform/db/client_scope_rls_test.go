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

	scoped := quals()
	for _, table := range csTableNames(csTables) {
		if !strings.Contains(scoped[table], "app.entity_scope") {
			t.Fatalf("before Down, %s qual lacks app.entity_scope (migration not applied?): %s", table, scoped[table])
		}
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
		if up[table] != scoped[table] {
			t.Errorf("after Up, %s qual = %s\nwant the scoped text applied before Down: %s", table, up[table], scoped[table])
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

// csReadAll returns the sorted ids of every table in csTables for the scope, keyed by table name.
func csReadAll(t *testing.T, tenant string, scope *string) map[string][]string {
	t.Helper()
	got := map[string][]string{}
	csInTx(t, tenant, scope, func(tx pgx.Tx) error {
		for _, tb := range csTables {
			ids, err := csQueryIDs(tx, tb.name)
			if err != nil {
				return err
			}
			got[tb.name] = ids
		}
		return nil
	})
	return got
}

// Odd but valid array literals must scope exactly to the clients they name, never wider.
func TestRLS_ClientScopeOddSetLiteralsScopeExactly(t *testing.T) {
	requireHarness(t)
	ten := csSeedClients(t, 3)
	other := csSeedClients(t, 1)
	a, b, c := ten.clients[0], ten.clients[1], ten.clients[2]
	foreign := other.clients[0].entity

	for _, tc := range []struct {
		name  string
		scope string
		want  []*csClient
	}{
		{"duplicate ids", "{" + a.entity + "," + a.entity + "}", []*csClient{a}},
		{"duplicate ids in a pair", "{" + a.entity + "," + c.entity + "," + a.entity + "}", []*csClient{a, c}},
		{"a foreign tenant's client beside A", "{" + a.entity + "," + foreign + "}", []*csClient{a}},
		{"spaces around the ids", "{ " + a.entity + " , " + c.entity + " }", []*csClient{a, c}},
		{"quoted id", `{"` + a.entity + `"}`, []*csClient{a}},
		{"upper-case id", "{" + strings.ToUpper(a.entity) + "}", []*csClient{a}},
		{"id without hyphens", "{" + strings.ReplaceAll(b.entity, "-", "") + "}", []*csClient{b}},
		{"a NULL element beside A", "{" + a.entity + ",NULL}", []*csClient{a}},
		{"only a NULL element", "{NULL}", nil},
		{"only a foreign client", "{" + foreign + "}", nil},
		{"braces with a space", "{ }", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := csReadAll(t, ten.id, csScope(tc.scope))
			for _, tb := range csTables {
				want := csWant(tb, tc.want...)
				if tc.want != nil && len(want) == 0 {
					t.Fatalf("no expected rows in %s", tb.name)
				}
				if !slices.Equal(got[tb.name], want) {
					t.Errorf("%s under %q: ids = %v, want %v", tb.name, tc.scope, got[tb.name], want)
				}
			}
		})
	}
}

// A value that is neither ” nor '*' nor a uuid array must error; it must not read every client.
func TestRLS_ClientScopeMalformedValuesNeverWiden(t *testing.T) {
	requireHarness(t)
	ten := csSeedClients(t, 2)

	for _, scope := range []string{" ", " *", "* ", "**", "{*}", "{}x", "{" + ten.clients[0].entity + "}x", "{1111}", "A", "all", "true", "null"} {
		for _, table := range []string{"invoices", "business_entities", "line_items"} {
			t.Run(fmt.Sprintf("%q/%s", scope, table), func(t *testing.T) {
				var rows []string
				err := csTx(ten.id, csScope(scope), func(tx pgx.Tx) error {
					ids, e := csQueryIDs(tx, table)
					rows = ids
					return e
				})
				if c := pgCode(err); c != "22P02" {
					t.Errorf("SELECT on %s under %q: err = %v (code %q), want 22P02", table, scope, err, c)
				}
				if len(rows) != 0 {
					t.Errorf("SELECT on %s under %q returned %v, want no rows", table, scope, rows)
				}
			})
		}
	}
}

// Rows outside the set are invisible to UPDATE and DELETE: 0 rows affected, nothing changed.
func TestRLS_ClientScopeUpdateAndDeleteOutsideTheSetTouchNothing(t *testing.T) {
	requireHarness(t)
	ctx := context.Background()
	ten := csSeedClients(t, 2)
	a, b := ten.clients[0], ten.clients[1]

	affected := func(t *testing.T, sql string, args ...any) int64 {
		t.Helper()
		var n int64
		csInTx(t, ten.id, csScope(csSet(a)), func(tx pgx.Tx) error {
			tag, err := tx.Exec(ctx, sql, args...)
			n = tag.RowsAffected()
			return err
		})
		return n
	}

	for _, tc := range []struct{ name, sql, own, other, check string }{
		{"UPDATE invoices", `UPDATE invoices SET invoice_number = 'cs-touched' WHERE id = $1`, a.invoice, b.invoice,
			`SELECT count(*) FROM invoices WHERE invoice_number = 'cs-touched' AND id = $1`},
		{"UPDATE import_batches", `UPDATE import_batches SET filename = 'cs-touched' WHERE id = $1`, a.batch, b.batch,
			`SELECT count(*) FROM import_batches WHERE filename = 'cs-touched' AND id = $1`},
		{"UPDATE import_mappings", `UPDATE import_mappings SET saved_at = '2001-01-01Z' WHERE id = $1`, a.mapping, b.mapping,
			`SELECT count(*) FROM import_mappings WHERE saved_at = '2001-01-01Z' AND id = $1`},
		{"UPDATE business_entities", `UPDATE business_entities SET name = 'cs-touched' WHERE id = $1`, a.entity, b.entity,
			`SELECT count(*) FROM business_entities WHERE name = 'cs-touched' AND id = $1`},
		{"UPDATE line_items", `UPDATE line_items SET line_no = 98 WHERE id = $1`, a.lines[0], b.lines[0],
			`SELECT count(*) FROM line_items WHERE line_no = 98 AND id = $1`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if n := affected(t, tc.sql, tc.other); n != 0 {
				t.Errorf("%s on client B's row under {A}: %d rows affected, want 0", tc.name, n)
			}
			if n := mustCount(t, h.super, tc.check, tc.other); n != 0 {
				t.Errorf("%s: B's row carries the change (%d), want it untouched", tc.name, n)
			}
			if n := affected(t, tc.sql, tc.own); n != 1 {
				t.Errorf("%s on client A's row under {A}: %d rows affected, want 1 (control)", tc.name, n)
			}
			if n := mustCount(t, h.super, tc.check, tc.own); n != 1 {
				t.Errorf("%s: A's row check count = %d, want 1 (control)", tc.name, n)
			}
		})
	}

	t.Run("DELETE line_items", func(t *testing.T) {
		if n := affected(t, `DELETE FROM line_items WHERE id = $1`, b.lines[0]); n != 0 {
			t.Errorf("DELETE of B's line item under {A}: %d rows affected, want 0", n)
		}
		if n := mustCount(t, h.super, `SELECT count(*) FROM line_items WHERE id = $1`, b.lines[0]); n != 1 {
			t.Errorf("B's line item rows after the DELETE = %d, want 1", n)
		}
		if n := affected(t, `DELETE FROM line_items WHERE id = $1`, a.lines[0]); n != 1 {
			t.Errorf("DELETE of A's line item under {A}: %d rows affected, want 1 (control)", n)
		}
	})
	t.Run("DELETE business_entities", func(t *testing.T) {
		if n := affected(t, `DELETE FROM business_entities WHERE id = $1`, b.entity); n != 0 {
			t.Errorf("DELETE of client B under {A}: %d rows affected, want 0", n)
		}
		if n := mustCount(t, h.super, `SELECT count(*) FROM business_entities WHERE id = $1`, b.entity); n != 1 {
			t.Errorf("client B rows after the DELETE = %d, want 1", n)
		}
	})
}

// A child row cannot be re-pointed at an invoice outside the set.
func TestRLS_ClientScopeLineItemMovedToAnInvoiceOutsideTheSetRefused(t *testing.T) {
	requireHarness(t)
	ctx := context.Background()
	ten := csSeedClients(t, 2)
	a, b := ten.clients[0], ten.clients[1]

	err := csTx(ten.id, csScope(csSet(a)), func(tx pgx.Tx) error {
		_, e := tx.Exec(ctx, `UPDATE line_items SET invoice_id = $1 WHERE id = $2`, b.invoice, a.lines[0])
		return e
	})
	csAssertRLSRefusal(t, "UPDATE line_items moving A's line to B's invoice under {A}", err)
	var inv string
	if e := h.super.QueryRow(ctx, `SELECT invoice_id::text FROM line_items WHERE id = $1`, a.lines[0]).Scan(&inv); e != nil || inv != a.invoice {
		t.Errorf("line item invoice after the refused UPDATE = %q (err %v), want A's %s", inv, e, a.invoice)
	}
}

// business_entities is scoped by its own id: a set hides other clients and refuses a new one.
func TestRLS_ClientScopeBusinessEntitiesWrites(t *testing.T) {
	requireHarness(t)
	ctx := context.Background()
	ten := csSeedClients(t, 2)
	a := ten.clients[0]
	insert := func(scope *string) error {
		return csTx(ten.id, scope, func(tx pgx.Tx) error {
			_, e := tx.Exec(ctx, `INSERT INTO business_entities (id, tenant_id, name) VALUES ($1, $2, 'cs-new')`, uuid.NewString(), ten.id)
			return e
		})
	}

	csAssertRLSRefusal(t, "INSERT of a new business entity under {A}", insert(csScope(csSet(a))))
	csAssertRLSRefusal(t, "INSERT of a new business entity under {}", insert(csScope("{}")))
	for _, scope := range []*string{nil, csScope(""), csScope("*")} {
		if err := insert(scope); err != nil {
			t.Errorf("INSERT of a new business entity under scope %s: %v, want success", csScopeStr(scope), err)
		}
	}
}

// Unset, ” and '*' set no client limit on a write: a worker or an admin writes to any client.
func TestRLS_ClientScopeOpenScopesWriteToAnyClient(t *testing.T) {
	requireHarness(t)
	ctx := context.Background()

	for _, scope := range []*string{nil, csScope(""), csScope("*")} {
		t.Run(csScopeStr(scope), func(t *testing.T) {
			ten := csSeedClients(t, 2)
			b := ten.clients[1]
			cases := csInsertCases(ten.id, b.entity, b.invoice)
			if len(cases) != 5 {
				t.Fatalf("insert cases = %d, want 5", len(cases))
			}
			for _, tc := range cases {
				err := csTx(ten.id, scope, func(tx pgx.Tx) error {
					_, e := tx.Exec(ctx, tc.sql, tc.args...)
					return e
				})
				if err != nil {
					t.Errorf("INSERT into %s naming B under scope %s: %v, want success", tc.name, csScopeStr(scope), err)
				}
			}
		})
	}
}

// A set cannot reach another tenant, for a read or a write, even when it names that tenant's client.
func TestRLS_ClientScopeStarDoesNotOpenAnotherTenantForWrites(t *testing.T) {
	requireHarness(t)
	ctx := context.Background()
	one := csSeedClients(t, 1)
	two := csSeedClients(t, 1)
	victim := two.clients[0]

	for _, scope := range []string{"*", csSet(victim), ""} {
		t.Run(fmt.Sprintf("scope %q", scope), func(t *testing.T) {
			var affected int64
			csInTx(t, one.id, csScope(scope), func(tx pgx.Tx) error {
				tag, err := tx.Exec(ctx, `UPDATE invoices SET invoice_number = 'cs-hijack' WHERE id = $1`, victim.invoice)
				affected = tag.RowsAffected()
				return err
			})
			if affected != 0 {
				t.Errorf("UPDATE of tenant two's invoice as tenant one under %q: %d rows affected, want 0", scope, affected)
			}
			err := csTx(one.id, csScope(scope), func(tx pgx.Tx) error {
				_, e := tx.Exec(ctx, `INSERT INTO invoices (id, tenant_id, entity_id, invoice_number) VALUES ($1, $2, $3, 'cs-forged')`,
					uuid.NewString(), two.id, victim.entity)
				return e
			})
			csAssertRLSRefusal(t, "INSERT naming tenant two as tenant one under "+scope, err)
			if n := mustCount(t, h.super, `SELECT count(*) FROM invoices WHERE id = $1 AND invoice_number = 'cs-hijack'`, victim.invoice); n != 0 {
				t.Errorf("tenant two's invoice was changed through tenant one")
			}
		})
	}
}

// staff_reader reads invoices and history through staff_read_lookup; the client scope does not narrow it.
func TestRLS_ClientScopeLeavesStaffReaderUnaffected(t *testing.T) {
	requireHarness(t)
	ctx := context.Background()
	ten := csSeedClients(t, 2)

	for _, scope := range []string{"", "{}", csSet(ten.clients[0])} {
		t.Run(fmt.Sprintf("scope %q", scope), func(t *testing.T) {
			tx := migratorTx(t, ctx)
			if _, err := tx.Exec(ctx, `SELECT set_config('app.current_tenant', '', true), set_config('app.entity_scope', $1, true)`, scope); err != nil {
				t.Fatalf("set_config: %v", err)
			}
			if _, err := tx.Exec(ctx, `SET LOCAL ROLE staff_reader`); err != nil {
				t.Fatalf("SET LOCAL ROLE staff_reader: %v", err)
			}
			for _, tb := range []csTable{csTables[1], csTables[3]} {
				rows, err := tx.Query(ctx, `SELECT id::text FROM `+tb.name+` WHERE tenant_id = $1`, ten.id)
				if err != nil {
					t.Fatalf("staff_reader SELECT on %s: %v", tb.name, err)
				}
				got, err := pgx.CollectRows(rows, pgx.RowTo[string])
				if err != nil {
					t.Fatalf("staff_reader SELECT on %s: %v", tb.name, err)
				}
				slices.Sort(got)
				if want := csWant(tb, ten.clients...); len(want) == 0 || !slices.Equal(got, want) {
					t.Errorf("staff_reader on %s under scope %q: ids = %v, want every client's %v", tb.name, scope, got, want)
				}
			}
		})
	}
}

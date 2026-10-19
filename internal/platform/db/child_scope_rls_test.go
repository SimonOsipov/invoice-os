// Storage suite for the child-table scope: five tables follow their parent's client through app.entity_scope.
//
// Rows are seeded per test as the superuser. A skip is a failure under the CI gate.
package db_test

import (
	"context"
	"io/fs"
	"slices"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/SimonOsipov/invoice-os/internal/platform/db"
	"github.com/SimonOsipov/invoice-os/migrations"
)

const childScopeMigrationGlob = "*_child_scope.sql"

var (
	cpInvoiceChildren = []string{"submission_jobs", "approval_runs", "approval_decisions"}
	cpExtraction      = []string{"extraction_jobs", "extraction_page_images"}
	cpFiveTables      = append(slices.Clone(cpInvoiceChildren), cpExtraction...)
)

// cpRows are one client's child rows; the extraction maps are keyed by document: X, Y, Z.
type cpRows struct{ job, run, step, decision string }

type cpFix struct {
	ten      *csTenant
	version  string
	a, b     cpRows
	doc      map[string]string
	extJob   map[string]string
	extImage map[string]string
}

func cpExec(t *testing.T, sql string, args ...any) {
	t.Helper()
	if _, err := h.super.Exec(context.Background(), sql, args...); err != nil {
		t.Fatalf("seed: %v\n%s", err, sql)
	}
}

// cpSeed seeds a two-client tenant plus the child rows of the Description: X is cited by A's
// invoice, Y only by B's batch, Z by nothing.
func cpSeed(t *testing.T) *cpFix {
	t.Helper()
	ten := csSeedClients(t, 2)
	f := &cpFix{ten: ten, version: uuid.NewString(),
		doc: map[string]string{}, extJob: map[string]string{}, extImage: map[string]string{}}
	// Registered after csSeed, so it runs first: children go before the invoices they reference.
	t.Cleanup(func() {
		for _, tb := range []string{"approval_decisions", "approval_run_steps", "approval_runs", "submission_jobs",
			"extraction_page_images", "extraction_jobs"} {
			_, _ = h.super.Exec(context.Background(), `DELETE FROM `+tb+` WHERE tenant_id = $1`, ten.id)
		}
	})

	policy := uuid.NewString()
	cpExec(t, `INSERT INTO approval_policies (id, tenant_id, name) VALUES ($1, $2, 'cp-policy')`, policy, ten.id)
	cpExec(t, `INSERT INTO approval_policy_versions (id, tenant_id, policy_id, version) VALUES ($1, $2, $3, 1)`,
		f.version, ten.id, policy)

	for i, rows := range []*cpRows{&f.a, &f.b} {
		c := ten.clients[i]
		*rows = cpRows{job: uuid.NewString(), run: uuid.NewString(), step: uuid.NewString(), decision: uuid.NewString()}
		cpExec(t, `INSERT INTO submission_jobs (id, tenant_id, invoice_id, idempotency_key, adapter, adapter_version)
			VALUES ($1, $2, $3, $4, 'mock', 'v1')`, rows.job, ten.id, c.invoice, "cp-"+uuid.NewString())
		cpExec(t, `INSERT INTO approval_runs (id, tenant_id, invoice_id, policy_version_id, content_fingerprint)
			VALUES ($1, $2, $3, $4, $5)`, rows.run, ten.id, c.invoice, f.version, "fp-"+uuid.NewString())
		cpExec(t, `INSERT INTO approval_run_steps (id, tenant_id, run_id, ord, kind) VALUES ($1, $2, $3, 1, 'approval')`,
			rows.step, ten.id, rows.run)
		cpExec(t, `INSERT INTO approval_decisions (id, tenant_id, run_id, run_step_id, decision, actor)
			VALUES ($1, $2, $3, $4, 'approved', 'cp-actor')`, rows.decision, ten.id, rows.run, rows.step)
	}

	for _, key := range []string{"X", "Y", "Z"} {
		id := uuid.NewString()
		f.doc[key] = id
		cpExec(t, `INSERT INTO documents (id, tenant_id, storage_key, content_hash, size_bytes) VALUES ($1, $2, $3, $4, 1024)`,
			id, ten.id, "cp-"+uuid.NewString(), docHash())
		f.extJob[key], f.extImage[key] = cpExtractionRows(t, ten.id, id)
	}
	cpExec(t, `UPDATE invoices SET source_document_id = $1 WHERE id = $2`, f.doc["X"], ten.clients[0].invoice)
	cpExec(t, `UPDATE import_batches SET document_id = $1 WHERE id = $2`, f.doc["Y"], ten.clients[1].batch)

	// Control: every "only A" and every zero below means nothing unless these rows exist.
	for _, tb := range cpFiveTables {
		want := 2
		if slices.Contains(cpExtraction, tb) {
			want = 3
		}
		if n := mustCount(t, h.super, `SELECT count(*) FROM `+tb+` WHERE tenant_id = $1`, ten.id); n != want {
			t.Fatalf("%s: superuser sees %d seeded rows, want %d", tb, n, want)
		}
	}
	return f
}

func cpExtractionRows(t *testing.T, tenant, doc string) (job, image string) {
	t.Helper()
	job, image = uuid.NewString(), uuid.NewString()
	cpExec(t, `INSERT INTO extraction_jobs (id, tenant_id, document_id, extractor, extractor_version)
		VALUES ($1, $2, $3, $4, $5)`, job, tenant, doc, ejExtractor, ejExtractorVersion)
	cpExec(t, epiInsert, image, tenant, doc, 1, epiWidth, epiHeight, epiKey(tenant, 1))
	return job, image
}

// cpIDs returns the sorted ids of table's rows for the named owners: A and B for the invoice
// children, X, Y and Z (documents) for the extraction tables.
func cpIDs(f *cpFix, table string, owners ...string) []string {
	var out []string
	for _, o := range owners {
		rows := f.a
		if o == "B" {
			rows = f.b
		}
		switch table {
		case "submission_jobs":
			out = append(out, rows.job)
		case "approval_runs":
			out = append(out, rows.run)
		case "approval_decisions":
			out = append(out, rows.decision)
		case "extraction_jobs":
			out = append(out, f.extJob[o])
		case "extraction_page_images":
			out = append(out, f.extImage[o])
		}
	}
	slices.Sort(out)
	return out
}

func cpRead(t *testing.T, f *cpFix, scope *string, table string) []string {
	t.Helper()
	return csReadIDs(t, f.ten.id, scope, csTable{name: table})
}

// cpExpect asserts table shows exactly want under the scope; an empty want is checked against a non-empty control.
func cpExpect(t *testing.T, f *cpFix, scope *string, table string, want []string) {
	t.Helper()
	if got := cpRead(t, f, csScope("*"), table); len(got) == 0 {
		t.Fatalf("%s under *: no rows, the control is empty", table)
	}
	if got := cpRead(t, f, scope, table); !slices.Equal(got, want) && !(len(got) == 0 && len(want) == 0) {
		t.Errorf("%s under scope %s: ids = %v, want %v", table, csScopeStr(scope), got, want)
	}
}

func cpSet(c *csClient) *string { return csScope("{" + c.entity + "}") }

func TestRLS_ChildScopeInvoiceChildrenFollowTheSet(t *testing.T) {
	requireHarness(t)
	f := cpSeed(t)

	for _, table := range cpInvoiceChildren {
		t.Run(table, func(t *testing.T) {
			cpExpect(t, f, cpSet(f.ten.clients[0]), table, cpIDs(f, table, "A"))
		})
	}
}

func TestRLS_ChildScopeEmptySetSeesNoInvoiceChild(t *testing.T) {
	requireHarness(t)
	f := cpSeed(t)

	for _, table := range cpInvoiceChildren {
		t.Run(table, func(t *testing.T) {
			cpExpect(t, f, csScope("{}"), table, nil)
		})
	}
}

// Control: green before the migration.
func TestRLS_ChildScopeStarSeesEveryChild(t *testing.T) {
	requireHarness(t)
	f := cpSeed(t)

	for _, table := range cpFiveTables {
		t.Run(table, func(t *testing.T) {
			owners := []string{"A", "B"}
			if slices.Contains(cpExtraction, table) {
				owners = []string{"X", "Y", "Z"}
			}
			cpExpect(t, f, csScope("*"), table, cpIDs(f, table, owners...))
		})
	}
}

// A decision follows its run's invoice, not its tenant: it has no invoice_id of its own.
func TestRLS_ChildScopeDecisionFollowsItsRunNotItsTenant(t *testing.T) {
	requireHarness(t)
	f := cpSeed(t)
	a, b := f.ten.clients[0], f.ten.clients[1]

	ofRun := func(scope *string, run string) []string {
		var got []string
		csInTx(t, f.ten.id, scope, func(tx pgx.Tx) error {
			rows, err := tx.Query(context.Background(), `SELECT id::text FROM approval_decisions WHERE run_id = $1`, run)
			if err != nil {
				return err
			}
			got, err = pgx.CollectRows(rows, pgx.RowTo[string])
			return err
		})
		return got
	}
	if got := ofRun(cpSet(a), f.a.run); !slices.Equal(got, []string{f.a.decision}) {
		t.Errorf("decisions of A's run under {A} = %v, want [%s]", got, f.a.decision)
	}
	if got := ofRun(cpSet(a), f.b.run); len(got) != 0 {
		t.Errorf("decisions of B's run under {A} = %v, want none", got)
	}
	if got := ofRun(cpSet(b), f.b.run); !slices.Equal(got, []string{f.b.decision}) {
		t.Errorf("decisions of B's run under {B} = %v, want [%s]", got, f.b.decision)
	}
}

func TestRLS_ChildScopeExtractionFollowsTheDocumentRule(t *testing.T) {
	requireHarness(t)
	f := cpSeed(t)
	a := f.ten.clients[0]

	for _, table := range cpExtraction {
		t.Run(table, func(t *testing.T) {
			cpExpect(t, f, cpSet(a), table, cpIDs(f, table, "X", "Z"))
			cpExpect(t, f, csScope("{}"), table, cpIDs(f, table, "Z"))
		})
	}
}

func TestRLS_ChildScopeExtractionOfADocumentCitedByBothClients(t *testing.T) {
	requireHarness(t)
	f := cpSeed(t)
	a, b := f.ten.clients[0], f.ten.clients[1]

	w := uuid.NewString()
	cpExec(t, `INSERT INTO documents (id, tenant_id, storage_key, content_hash, size_bytes) VALUES ($1, $2, $3, $4, 1024)`,
		w, f.ten.id, "cp-"+uuid.NewString(), docHash())
	f.extJob["W"], f.extImage["W"] = cpExtractionRows(t, f.ten.id, w)
	cpExec(t, `INSERT INTO invoices (id, tenant_id, entity_id, invoice_number, source_document_id) VALUES ($1, $2, $3, $4, $5)`,
		uuid.NewString(), f.ten.id, a.entity, "cp-"+uuid.NewString()[:8], w)
	cpExec(t, `INSERT INTO import_batches (id, tenant_id, entity_id, document_id) VALUES ($1, $2, $3, $4)`,
		uuid.NewString(), f.ten.id, b.entity, w)

	for _, table := range cpExtraction {
		t.Run(table, func(t *testing.T) {
			for name, client := range map[string]*csClient{"A": a, "B": b} {
				if got := cpRead(t, f, cpSet(client), table); !slices.Contains(got, cpIDs(f, table, "W")[0]) {
					t.Errorf("%s under {%s}: ids = %v, want W's %v visible", table, name, got, cpIDs(f, table, "W"))
				}
			}
			if got := cpRead(t, f, cpSet(a), table); slices.Contains(got, cpIDs(f, table, "Y")[0]) {
				t.Errorf("%s under {A}: ids = %v, Y's row must stay hidden beside W", table, got)
			}
		})
	}
}

// document_link_state widens the scope to '*' for its lookup; it must put the caller's scope back.
func TestRLS_ChildScopeExtractionScopeIsRestoredAfterTheCitationCheck(t *testing.T) {
	requireHarness(t)
	f := cpSeed(t)
	a := f.ten.clients[0]
	scope := "{" + a.entity + "}"

	csInTx(t, f.ten.id, csScope(scope), func(tx pgx.Tx) error {
		ctx := context.Background()
		// Y hidden proves the citation check ran for this read.
		jobs, err := csQueryIDs(tx, "extraction_jobs")
		if err != nil {
			return err
		}
		if want := cpIDs(f, "extraction_jobs", "X", "Z"); !slices.Equal(jobs, want) {
			t.Errorf("extraction_jobs under {A} = %v, want %v", jobs, want)
		}
		var got string
		if err := tx.QueryRow(ctx, `SELECT current_setting('app.entity_scope')`).Scan(&got); err != nil {
			return err
		}
		if got != scope {
			t.Errorf("app.entity_scope after reading extraction_jobs = %q, want %q", got, scope)
		}
		invoices, err := csQueryIDs(tx, "invoices")
		if err != nil {
			return err
		}
		if want := []string{a.invoice}; !slices.Equal(invoices, want) {
			t.Errorf("invoices after reading extraction_jobs = %v, want only A's %v", invoices, want)
		}
		return nil
	})
}

// cpReadFreshConn reads the five tables on a connection that never set app.entity_scope; with
// localSetFirst an earlier committed transaction set it locally, which leaves an empty string behind.
func cpReadFreshConn(t *testing.T, f *cpFix, localSetFirst bool) {
	t.Helper()
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, h.appURL)
	if err != nil {
		t.Fatalf("connect as invoice_app: %v", err)
	}
	defer func() { _ = conn.Close(ctx) }()

	if localSetFirst {
		tx1, err := conn.Begin(ctx)
		if err != nil {
			t.Fatalf("begin tx 1: %v", err)
		}
		for _, kv := range [][2]string{{"app.current_tenant", f.ten.id}, {"app.entity_scope", "{" + f.ten.clients[0].entity + "}"}} {
			if _, err := tx1.Exec(ctx, `SELECT set_config($1, $2, true)`, kv[0], kv[1]); err != nil {
				t.Fatalf("tx 1 set_config %s: %v", kv[0], err)
			}
		}
		if err := tx1.Commit(ctx); err != nil {
			t.Fatalf("commit tx 1: %v", err)
		}
	}
	tx, err := conn.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `SELECT set_config('app.current_tenant', $1, true)`, f.ten.id); err != nil {
		t.Fatalf("set tenant: %v", err)
	}
	var scope *string
	if err := tx.QueryRow(ctx, `SELECT current_setting('app.entity_scope', true)`).Scan(&scope); err != nil {
		t.Fatalf("read app.entity_scope: %v", err)
	}
	if localSetFirst && (scope == nil || *scope != "") || !localSetFirst && scope != nil {
		t.Fatalf("app.entity_scope = %v (local set first: %v), want '' after a local set and NULL on a fresh connection", scope, localSetFirst)
	}
	for _, table := range cpFiveTables {
		got, err := csQueryIDs(tx, table)
		if err != nil {
			t.Fatalf("%s: %v", table, err)
		}
		owners := []string{"A", "B"}
		if slices.Contains(cpExtraction, table) {
			owners = []string{"X", "Y", "Z"}
		}
		if want := cpIDs(f, table, owners...); !slices.Equal(got, want) {
			t.Errorf("%s with scope %v: ids = %v, want every row %v", table, scope, got, want)
		}
	}
}

// Control: green before the migration.
func TestRLS_ChildScopeUnsetReadsEveryChild(t *testing.T) {
	requireHarness(t)
	cpReadFreshConn(t, cpSeed(t), false)
}

// Control: green before the migration.
func TestRLS_ChildScopeEmptyStringAfterALocalSetReadsEveryChild(t *testing.T) {
	requireHarness(t)
	cpReadFreshConn(t, cpSeed(t), true)
}

// cpMember seeds a membership; clients lists the membership_clients rows of a limited member.
func cpMember(t *testing.T, tenant, role string, allClients bool, clients ...string) string {
	t.Helper()
	user := uuid.NewString()
	cpExec(t, `INSERT INTO memberships (tenant_id, user_id, role, all_clients) VALUES ($1, $2, $3, $4)`,
		tenant, user, role, allClients)
	for _, c := range clients {
		cpExec(t, `INSERT INTO membership_clients (tenant_id, user_id, entity_id) VALUES ($1, $2, $3)`, tenant, user, c)
	}
	return user
}

// cpReadAllFive reads invoices and the five tables in one transaction opened by open.
func cpReadAllFive(t *testing.T, open func(fn func(pgx.Tx) error) error) map[string][]string {
	t.Helper()
	got := map[string][]string{}
	err := open(func(tx pgx.Tx) error {
		for _, table := range append([]string{"invoices"}, cpFiveTables...) {
			ids, err := csQueryIDs(tx, table)
			if err != nil {
				return err
			}
			got[table] = ids
		}
		return nil
	})
	if err != nil {
		t.Fatalf("read as invoice_app: %v", err)
	}
	return got
}

// The worker path: a transaction opened with WithinTenantTx never sets a scope, whoever the ctx names.
// Control: green before the migration.
func TestRLS_ChildScopeWorkerCoreReadsEveryChild(t *testing.T) {
	requireHarness(t)
	f := cpSeed(t)
	user := cpMember(t, f.ten.id, "reviewer", false)
	ctx := requestCtx(user, f.ten.id)

	// The same identity through the request seam is limited: it holds no client.
	limited := cpReadAllFive(t, func(fn func(pgx.Tx) error) error {
		return db.WithinRequestTenantTx(ctx, h.app, fn)
	})
	if len(limited["invoices"]) != 0 {
		t.Fatalf("unassigned member reads invoices %v through the request seam, want none", limited["invoices"])
	}
	worker := cpReadAllFive(t, func(fn func(pgx.Tx) error) error {
		return db.WithinTenantTx(ctx, h.app, f.ten.id, fn)
	})
	if len(worker["invoices"]) != 2 {
		t.Errorf("worker path reads invoices %v, want both clients'", worker["invoices"])
	}
	for _, table := range cpFiveTables {
		owners := []string{"A", "B"}
		if slices.Contains(cpExtraction, table) {
			owners = []string{"X", "Y", "Z"}
		}
		if want := cpIDs(f, table, owners...); !slices.Equal(worker[table], want) {
			t.Errorf("%s on the worker path: ids = %v, want every row %v", table, worker[table], want)
		}
	}
}

// The real seam, by caller kind: a limited member reads its client's rows, an unassigned member none,
// an "All clients" member and an admin every row.
func TestRLS_ChildScopeRequestSeamByCallerKind(t *testing.T) {
	requireHarness(t)
	f := cpSeed(t)
	a := f.ten.clients[0]

	for _, tc := range []struct {
		name string
		user string
		want func(table string) []string
	}{
		{"limited to A", cpMember(t, f.ten.id, "reviewer", false, a.entity), func(table string) []string {
			if slices.Contains(cpExtraction, table) {
				return cpIDs(f, table, "X", "Z")
			}
			return cpIDs(f, table, "A")
		}},
		{"unassigned", cpMember(t, f.ten.id, "reviewer", false), func(table string) []string {
			if slices.Contains(cpExtraction, table) {
				return cpIDs(f, table, "Z")
			}
			return nil
		}},
		{"All clients", cpMember(t, f.ten.id, "preparer", true), func(table string) []string { return cpEvery(f, table) }},
		{"admin", cpMember(t, f.ten.id, "admin", false), func(table string) []string { return cpEvery(f, table) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := requestCtx(tc.user, f.ten.id)
			got := cpReadAllFive(t, func(fn func(pgx.Tx) error) error {
				return db.WithinRequestTenantTx(ctx, h.app, fn)
			})
			for _, table := range cpFiveTables {
				if want := tc.want(table); !slices.Equal(got[table], want) && !(len(got[table]) == 0 && len(want) == 0) {
					t.Errorf("%s as %s: ids = %v, want %v", table, tc.name, got[table], want)
				}
			}
		})
	}
}

func cpEvery(f *cpFix, table string) []string {
	if slices.Contains(cpExtraction, table) {
		return cpIDs(f, table, "X", "Y", "Z")
	}
	return cpIDs(f, table, "A", "B")
}

type cpInsert struct {
	table, sql string
	args       []any
}

// cpInserts builds one INSERT per table naming the given client's invoice, run and step and the given document.
// The run is cancelled so it never collides with the open run of approval_runs_one_open.
func cpInserts(f *cpFix, rows cpRows, invoice, doc string) []cpInsert {
	tenant := f.ten.id
	return []cpInsert{
		{"submission_jobs", `INSERT INTO submission_jobs (id, tenant_id, invoice_id, idempotency_key, adapter, adapter_version)
			VALUES ($1, $2, $3, $4, 'mock', 'v1')`,
			[]any{uuid.NewString(), tenant, invoice, "cp-" + uuid.NewString()}},
		{"approval_runs", `INSERT INTO approval_runs (id, tenant_id, invoice_id, policy_version_id, state, content_fingerprint)
			VALUES ($1, $2, $3, $4, 'cancelled', $5)`,
			[]any{uuid.NewString(), tenant, invoice, f.version, "fp-" + uuid.NewString()}},
		{"approval_decisions", `INSERT INTO approval_decisions (id, tenant_id, run_id, run_step_id, decision, actor)
			VALUES ($1, $2, $3, $4, 'approved', 'cp-actor')`,
			[]any{uuid.NewString(), tenant, rows.run, rows.step}},
		{"extraction_jobs", `INSERT INTO extraction_jobs (id, tenant_id, document_id, extractor, extractor_version)
			VALUES ($1, $2, $3, 'mock', 'v1')`,
			[]any{uuid.NewString(), tenant, doc}},
	}
}

func TestRLS_ChildScopeInsertOutsideTheSetRefused(t *testing.T) {
	requireHarness(t)
	ctx := context.Background()
	f := cpSeed(t)
	a, b := f.ten.clients[0], f.ten.clients[1]

	cases := cpInserts(f, f.b, b.invoice, f.doc["Y"])
	if len(cases) != 4 {
		t.Fatalf("insert cases = %d, want 4", len(cases))
	}
	for _, tc := range cases {
		t.Run(tc.table, func(t *testing.T) {
			err := csTx(f.ten.id, cpSet(a), func(tx pgx.Tx) error {
				_, e := tx.Exec(ctx, tc.sql, tc.args...)
				return e
			})
			csAssertRLSRefusal(t, "INSERT into "+tc.table+" outside {A}", err)
			if n := mustCount(t, h.super, `SELECT count(*) FROM `+tc.table+` WHERE id = $1`, tc.args[0]); n != 0 {
				t.Errorf("%s: rows left by the refused INSERT = %d, want 0", tc.table, n)
			}
		})
	}
}

// Control: green before the migration.
func TestRLS_ChildScopeInsertInsideTheSetAllowed(t *testing.T) {
	requireHarness(t)
	ctx := context.Background()
	f := cpSeed(t)
	a := f.ten.clients[0]

	for _, tc := range cpInserts(f, f.a, a.invoice, f.doc["X"]) {
		t.Run(tc.table, func(t *testing.T) {
			err := csTx(f.ten.id, cpSet(a), func(tx pgx.Tx) error {
				_, e := tx.Exec(ctx, tc.sql, tc.args...)
				return e
			})
			if err != nil {
				t.Fatalf("INSERT into %s inside {A}: %v", tc.table, err)
			}
			if n := mustCount(t, h.super, `SELECT count(*) FROM `+tc.table+` WHERE id = $1`, tc.args[0]); n != 1 {
				t.Errorf("%s: rows after the allowed INSERT = %d, want 1", tc.table, n)
			}
		})
	}
}

// A policy WITH CHECK that kept only the scope term would pass this INSERT and hide it from reads.
func TestRLS_ChildScopeInsertForAnotherTenantRefused(t *testing.T) {
	requireHarness(t)
	ctx := context.Background()
	one, two := cpSeed(t), cpSeed(t)
	insert := func(tc cpInsert) error {
		return csTx(one.ten.id, csScope("*"), func(tx pgx.Tx) error {
			_, e := tx.Exec(ctx, tc.sql, tc.args...)
			return e
		})
	}

	own := cpInserts(one, one.a, one.ten.clients[0].invoice, one.doc["X"])
	foreign := cpInserts(two, two.b, two.ten.clients[1].invoice, two.doc["Y"])
	if len(own) != 4 || len(foreign) != 4 {
		t.Fatalf("insert cases = %d own, %d foreign, want 4 each", len(own), len(foreign))
	}
	for i, tc := range foreign {
		t.Run(tc.table, func(t *testing.T) {
			if err := insert(own[i]); err != nil {
				t.Fatalf("control: INSERT into %s for the caller's own tenant under *: %v", tc.table, err)
			}
			csAssertRLSRefusal(t, "INSERT into "+tc.table+" for another tenant under *", insert(tc))
			if n := mustCount(t, h.super, `SELECT count(*) FROM `+tc.table+` WHERE id = $1`, tc.args[0]); n != 0 {
				t.Errorf("%s: rows left by the refused INSERT = %d, want 0", tc.table, n)
			}
		})
	}
}

// Control: green before the migration.
func TestRLS_ChildScopeKeepsTheTenantPredicate(t *testing.T) {
	requireHarness(t)
	one, two := cpSeed(t), cpSeed(t)

	for _, table := range cpFiveTables {
		t.Run(table, func(t *testing.T) {
			got := cpRead(t, one, csScope("*"), table)
			if want := cpEvery(one, table); !slices.Equal(got, want) {
				t.Errorf("%s under * for tenant one: ids = %v, want only its own %v", table, got, want)
			}
			if foreign := cpEvery(two, table); len(foreign) == 0 || slices.ContainsFunc(got, func(id string) bool { return slices.Contains(foreign, id) }) {
				t.Errorf("%s under *: tenant two's rows %v are visible to tenant one (%v)", table, foreign, got)
			}
		})
	}
}

func TestRLS_ChildScopePoliciesDeclared(t *testing.T) {
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

	for _, table := range cpFiveTables {
		t.Run(table, func(t *testing.T) {
			ps := read(table)["tenant_isolation"]
			if len(ps) != 1 {
				t.Fatalf("tenant_isolation policies on %s = %d, want exactly 1", table, len(ps))
			}
			p := ps[0]
			if p.permissive != "PERMISSIVE" || p.roles != "{public}" {
				t.Errorf("%s tenant_isolation = %s for %s, want PERMISSIVE for {public}", table, p.permissive, p.roles)
			}
			if p.withCheck != nil {
				t.Errorf("%s tenant_isolation has a separate WITH CHECK %q, want none", table, *p.withCheck)
			}
			if p.qual == nil || !strings.Contains(*p.qual, "app.current_tenant") {
				t.Errorf("%s tenant_isolation qual = %v, want the tenant predicate kept", table, p.qual)
			} else if !strings.Contains(*p.qual, "app.entity_scope") {
				t.Errorf("%s tenant_isolation qual lacks app.entity_scope: %s", table, *p.qual)
			}
		})
	}

	for _, want := range []struct{ name, role string }{
		{"staff_read_lookup", "{staff_reader}"}, {"health_sample_count", "{auth_hook_reader}"},
	} {
		ps := read("submission_jobs")[want.name]
		if len(ps) != 1 {
			t.Errorf("%s policies on submission_jobs = %d, want 1", want.name, len(ps))
			continue
		}
		if p := ps[0]; p.cmd != "SELECT" || p.roles != want.role || p.qual == nil || *p.qual != "true" {
			qual := "<none>"
			if p.qual != nil {
				qual = *p.qual
			}
			t.Errorf("%s on submission_jobs = cmd %s roles %s qual %s, want SELECT for %s USING true",
				want.name, p.cmd, p.roles, qual, want.role)
		}
	}

	// Guards over-scoping: these seven keep the tenant-only expression.
	for _, table := range []string{"documents", "approval_run_steps", "app_exchange", "extraction_field_results",
		"extraction_field_corrections", "extraction_rule_breaks", "audit_log"} {
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

func TestRLS_ChildScopeMigrationIsReversible(t *testing.T) {
	requireHarness(t)
	ctx := context.Background()
	tx := migratorTx(t, ctx)

	quals := func() map[string]string {
		t.Helper()
		out := map[string]string{}
		for _, table := range append([]string{"membership_clients"}, cpFiveTables...) {
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

	// Everything but the qual: a Down that drops and recreates a policy changes one of these.
	shape := func() string {
		t.Helper()
		rows, err := tx.Query(ctx, `SELECT tablename, policyname, permissive, roles::text, cmd, coalesce(with_check, '<none>'),
				CASE WHEN policyname = 'tenant_isolation' THEN '' ELSE coalesce(qual, '') END
			FROM pg_policies WHERE schemaname = 'public' AND tablename = ANY($1) ORDER BY 1, 2`, cpFiveTables)
		if err != nil {
			t.Fatalf("read policy shapes: %v", err)
		}
		lines, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (string, error) {
			var parts [7]string
			err := r.Scan(&parts[0], &parts[1], &parts[2], &parts[3], &parts[4], &parts[5], &parts[6])
			return strings.Join(parts[:], "|"), err
		})
		if err != nil || len(lines) < len(cpFiveTables)+2 {
			t.Fatalf("policy shapes = %v (%v), want one tenant_isolation per table plus staff_read_lookup and health_sample_count", lines, err)
		}
		return strings.Join(lines, "\n")
	}

	// First, so the failure before the migration lands on the qual text and not on a file lookup.
	scoped := quals()
	shapeBefore := shape()
	for _, table := range cpFiveTables {
		if !strings.Contains(scoped[table], "app.entity_scope") {
			t.Fatalf("before Down, %s qual lacks app.entity_scope (migration not applied?): %s", table, scoped[table])
		}
	}

	matches, err := fs.Glob(migrations.FS, childScopeMigrationGlob)
	if err != nil {
		t.Fatalf("glob %s: %v", childScopeMigrationGlob, err)
	}
	if len(matches) != 1 {
		t.Fatalf("migrations matching %s = %v, want exactly 1", childScopeMigrationGlob, matches)
	}
	migration := matches[0]

	if _, err := tx.Exec(ctx, auditEntitySectionOf(t, migration, "Down")); err != nil {
		t.Fatalf("Down body of %s failed: %v", migration, err)
	}
	down := quals()
	tenantOnly := down["membership_clients"]
	if strings.Contains(tenantOnly, "app.entity_scope") || !strings.Contains(tenantOnly, "app.current_tenant") {
		t.Fatalf("membership_clients qual = %s, want the tenant-only text as the reference", tenantOnly)
	}
	for _, table := range cpFiveTables {
		if down[table] != tenantOnly {
			t.Errorf("after Down, %s qual = %s\nwant the tenant-only text %s", table, down[table], tenantOnly)
		}
	}
	if got := shape(); got != shapeBefore {
		t.Errorf("after Down, policy shapes differ from before:\n%s\nwant\n%s", got, shapeBefore)
	}

	if _, err := tx.Exec(ctx, auditEntitySectionOf(t, migration, "Up")); err != nil {
		t.Fatalf("Up body of %s failed: %v", migration, err)
	}
	up := quals()
	for _, table := range cpFiveTables {
		if up[table] != scoped[table] {
			t.Errorf("after Up, %s qual = %s\nwant the scoped text applied before Down: %s", table, up[table], scoped[table])
		}
	}
	if got := shape(); got != shapeBefore {
		t.Errorf("after Up, policy shapes differ from before:\n%s\nwant\n%s", got, shapeBefore)
	}
	if up["membership_clients"] != tenantOnly {
		t.Errorf("membership_clients qual changed across the migration: %s", up["membership_clients"])
	}
}

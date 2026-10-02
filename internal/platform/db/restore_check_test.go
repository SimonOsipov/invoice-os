package db_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"regexp"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

const restoreCheckSQLPath = "../../../db/restore-check.sql"

var rcTableNames = []string{"invoices", "line_items", "documents", "audit_log", "memberships"}

var rcHash = regexp.MustCompile(`^[0-9a-f]{32}$`)

// rcOut is the check's output: one []string per row, fields split on "|" and trimmed,
// plus the raw bytes for byte-identity comparisons.
type rcOut struct {
	rows [][]string
	raw  string
}

func (o rcOut) kind(k string) [][]string {
	var out [][]string
	for _, r := range o.rows {
		if len(r) > 0 && r[0] == k {
			out = append(out, r)
		}
	}
	return out
}

func (o rcOut) tables(t *testing.T) map[string][]string {
	t.Helper()
	got := map[string][]string{}
	for _, r := range o.kind("table") {
		if len(r) != 5 {
			t.Fatalf("table row has %d fields, want 5: %v", len(r), r)
		}
		got[r[1]] = r
	}
	if len(got) != len(rcTableNames) {
		t.Fatalf("output has %d table rows, want %d (%v)", len(got), len(rcTableNames), o.rows)
	}
	for _, n := range rcTableNames {
		if got[n] == nil {
			t.Fatalf("no table row for %s in %v", n, o.rows)
		}
	}
	return got
}

func parseRCOut(results []*pgconn.Result) rcOut {
	var o rcOut
	var raw []string
	for _, res := range results {
		for _, row := range res.Rows {
			cells := make([]string, len(row))
			for i, c := range row {
				cells[i] = string(c)
			}
			raw = append(raw, strings.Join(cells, "\t"))
			var fields []string
			for _, f := range strings.Split(strings.Join(cells, "|"), "|") {
				fields = append(fields, strings.TrimSpace(f))
			}
			o.rows = append(o.rows, fields)
		}
	}
	o.raw = strings.Join(raw, "\n")
	return o
}

func readRestoreCheckSQL(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile(restoreCheckSQLPath)
	if err != nil {
		t.Fatalf("read %s: %v", restoreCheckSQLPath, err)
	}
	return string(b)
}

// rcRunOn runs the file on conn after applying session settings (name -> value).
func rcRunOn(ctx context.Context, t *testing.T, conn *pgx.Conn, settings map[string]string) (rcOut, error) {
	t.Helper()
	for k, v := range settings {
		if _, err := conn.Exec(ctx, `SELECT set_config($1, $2, false)`, k, v); err != nil {
			t.Fatalf("set_config(%s): %v", k, err)
		}
	}
	results, err := conn.PgConn().Exec(ctx, readRestoreCheckSQL(t)).ReadAll()
	if err != nil {
		return rcOut{}, err
	}
	return parseRCOut(results), nil
}

// rcRun runs the file on a fresh non-pooled connection, so no earlier set_config leaks in.
func rcRun(t *testing.T, dsn string, settings map[string]string) (rcOut, error) {
	t.Helper()
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer conn.Close(ctx)
	return rcRunOn(ctx, t, conn, settings)
}

func rcMustRun(t *testing.T, dsn string, settings map[string]string) rcOut {
	t.Helper()
	out, err := rcRun(t, dsn, settings)
	if err != nil {
		t.Fatalf("restore-check failed: %v", err)
	}
	return out
}

func rcInputs(tenant string, cutoff time.Time) map[string]string {
	return map[string]string{
		"restore_check.tenant": tenant,
		"restore_check.cutoff": cutoff.Format(time.RFC3339),
	}
}

func rcWith(base map[string]string, kv ...string) map[string]string {
	out := map[string]string{}
	for k, v := range base {
		out[k] = v
	}
	for i := 0; i+1 < len(kv); i += 2 {
		out[kv[i]] = kv[i+1]
	}
	return out
}

var rcInstantLayouts = []string{
	time.RFC3339Nano,
	"2006-01-02 15:04:05.999999999Z07:00",
	"2006-01-02 15:04:05.999999999-07",
}

// rcInstant parses a timestamp cell and requires a zero UTC offset.
func rcInstant(t *testing.T, s string) time.Time {
	t.Helper()
	for _, l := range rcInstantLayouts {
		if ts, err := time.Parse(l, s); err == nil {
			if _, off := ts.Zone(); off != 0 {
				t.Fatalf("timestamp %q is not UTC", s)
			}
			return ts
		}
	}
	t.Fatalf("timestamp %q is not ISO", s)
	return time.Time{}
}

func requireRCPgMessage(t *testing.T, err error, want string) {
	t.Helper()
	if err == nil {
		t.Fatalf("run succeeded, want an error containing %q", want)
	}
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		t.Fatalf("want a Postgres error containing %q, got %T: %v", want, err, err)
	}
	if !strings.Contains(pgErr.Message, want) {
		t.Fatalf("error %q does not contain %q", pgErr.Message, want)
	}
}

// --- fixture ----------------------------------------------------------------

type rcFixture struct {
	pool   *pgxpool.Pool
	dsn    string
	tenant string
	entity string
	name   string
	cutoff time.Time
}

// newRCFixture commits a fresh tenant and entity as the superuser.
func newRCFixture(t *testing.T) *rcFixture {
	t.Helper()
	dsn := requireSuperuserDSN(t)
	f := &rcFixture{
		pool:   bootstrapSuperuserPool(t, dsn),
		dsn:    dsn,
		tenant: uuid.NewString(),
		entity: uuid.NewString(),
		cutoff: time.Date(2026, 10, 2, 8, 0, 0, 0, time.UTC),
	}
	f.name = "restore-check-" + f.tenant[:8]
	f.exec(t, `INSERT INTO tenants (id, name) VALUES ($1, $2)`, f.tenant, f.name)
	f.exec(t, `INSERT INTO business_entities (id, tenant_id, name) VALUES ($1, $2, $3)`,
		f.entity, f.tenant, "entity-"+f.entity[:8])
	t.Cleanup(func() {
		// audit_log rows are not deleted: the table is append-only and has no FK.
		for _, sql := range []string{
			`DELETE FROM line_items WHERE tenant_id = $1`,
			`DELETE FROM invoices WHERE tenant_id = $1`,
			`DELETE FROM documents WHERE tenant_id = $1`,
			`DELETE FROM memberships WHERE tenant_id = $1`,
			`DELETE FROM business_entities WHERE tenant_id = $1`,
			`DELETE FROM tenants WHERE id = $1`,
		} {
			_, _ = f.pool.Exec(context.Background(), sql, f.tenant)
		}
	})
	return f
}

func (f *rcFixture) exec(t *testing.T, sql string, args ...any) {
	t.Helper()
	if _, err := f.pool.Exec(context.Background(), sql, args...); err != nil {
		t.Fatalf("seed (%s): %v", sql, err)
	}
}

func (f *rcFixture) invoice(t *testing.T, number, status string, at time.Time) string {
	t.Helper()
	id := uuid.NewString()
	f.exec(t, `INSERT INTO invoices (id, tenant_id, entity_id, invoice_number, status, created_at)
	           VALUES ($1, $2, $3, $4, $5, $6)`, id, f.tenant, f.entity, number, status, at)
	return id
}

func (f *rcFixture) lineItem(t *testing.T, invoiceID string, no int, at time.Time) string {
	t.Helper()
	id := uuid.NewString()
	f.exec(t, `INSERT INTO line_items (id, tenant_id, invoice_id, line_no, created_at)
	           VALUES ($1, $2, $3, $4, $5)`, id, f.tenant, invoiceID, no, at)
	return id
}

func (f *rcFixture) document(t *testing.T, at time.Time) string {
	t.Helper()
	id := uuid.NewString()
	f.exec(t, `INSERT INTO documents (id, tenant_id, storage_key, content_hash, size_bytes, created_at)
	           VALUES ($1, $2, $3, $4, 1, $5)`, id, f.tenant, "rc/"+id, strings.Repeat("a", 64), at)
	return id
}

func (f *rcFixture) audit(t *testing.T, event string, at time.Time) int64 {
	t.Helper()
	var id int64
	err := f.pool.QueryRow(context.Background(),
		`INSERT INTO audit_log (tenant_id, actor, event, payload, created_at)
		 VALUES ($1, 'restore-check-fixture', $2, '{}'::jsonb, $3) RETURNING id`,
		f.tenant, event, at).Scan(&id)
	if err != nil {
		t.Fatalf("seed audit row: %v", err)
	}
	return id
}

func (f *rcFixture) membership(t *testing.T, at time.Time) string {
	t.Helper()
	id := uuid.NewString()
	f.exec(t, `INSERT INTO memberships (id, tenant_id, user_id, role, created_at)
	           VALUES ($1, $2, $3, 'admin', $4)`, id, f.tenant, uuid.NewString(), at)
	return id
}

func (f *rcFixture) run(t *testing.T) rcOut {
	t.Helper()
	return rcMustRun(t, f.dsn, rcInputs(f.tenant, f.cutoff))
}

// rcFull is the AC-1 shape: 3 invoices (the third has no line items), 4 line items,
// 1 document, 2 audit rows, 1 membership, all before the cutoff.
type rcFull struct {
	invoices    []string
	lineItems   []string
	document    string
	auditIDs    []int64
	membership  string
	newestInv   time.Time
	newestLine  time.Time
	newestDoc   time.Time
	newestAudit time.Time
	newestMem   time.Time
}

func (f *rcFixture) seedFull(t *testing.T) rcFull {
	t.Helper()
	c := f.cutoff
	r := rcFull{
		newestInv:   c.Add(-1 * time.Hour),
		newestLine:  c.Add(-90 * time.Minute),
		newestDoc:   c.Add(-4 * time.Hour),
		newestAudit: c.Add(-45 * time.Minute),
		newestMem:   c.Add(-6 * time.Hour),
	}
	r.invoices = []string{
		f.invoice(t, "RC-1", "draft", c.Add(-3*time.Hour)),
		f.invoice(t, "RC-2", "validated", c.Add(-2*time.Hour)),
		f.invoice(t, "RC-3", "accepted", r.newestInv),
	}
	r.lineItems = []string{
		f.lineItem(t, r.invoices[0], 1, c.Add(-5*time.Hour)),
		f.lineItem(t, r.invoices[0], 2, c.Add(-4*time.Hour)),
		f.lineItem(t, r.invoices[1], 1, c.Add(-3*time.Hour)),
		f.lineItem(t, r.invoices[1], 2, r.newestLine),
	}
	r.document = f.document(t, r.newestDoc)
	r.auditIDs = []int64{
		f.audit(t, "invoice.created", c.Add(-2*time.Hour)),
		f.audit(t, "invoice.validated", r.newestAudit),
	}
	r.membership = f.membership(t, r.newestMem)
	return r
}

// --- AC-1 -------------------------------------------------------------------

func TestRestoreCheck_ReportsExactCountsMaxAndHash(t *testing.T) {
	f := newRCFixture(t)
	s := f.seedFull(t)
	out := f.run(t)

	tables := out.tables(t)
	want := map[string]struct {
		count string
		max   time.Time
	}{
		"invoices":    {"3", s.newestInv},
		"line_items":  {"4", s.newestLine},
		"documents":   {"1", s.newestDoc},
		"audit_log":   {"2", s.newestAudit},
		"memberships": {"1", s.newestMem},
	}
	for name, w := range want {
		row := tables[name]
		if row[2] != w.count {
			t.Errorf("%s count = %q, want %q", name, row[2], w.count)
		}
		if got := rcInstant(t, row[3]); !got.Equal(w.max) {
			t.Errorf("%s max(created_at) = %s, want %s", name, got, w.max)
		}
		if !rcHash.MatchString(row[4]) {
			t.Errorf("%s hash = %q, want 32 hex", name, row[4])
		}
	}

	tenantRows := out.kind("tenant")
	if len(tenantRows) != 1 || len(tenantRows[0]) < 3 || tenantRows[0][1] != f.tenant || tenantRows[0][2] != f.name {
		t.Errorf("tenant row = %v, want [tenant %s %s]", tenantRows, f.tenant, f.name)
	}

	var order []string
	for _, r := range out.rows {
		order = append(order, r[0])
	}
	wantOrder := "role,tenant,table,table,table,table,table,recent,recent,recent,goose,owner"
	if got := strings.Join(dedupeTail(order, "owner"), ","); got != wantOrder {
		t.Errorf("row order = %s, want %s", got, wantOrder)
	}
}

// dedupeTail collapses repeated trailing runs of kind so owner rows (count varies by
// schema) compare as one entry.
func dedupeTail(kinds []string, kind string) []string {
	var out []string
	for _, k := range kinds {
		if k == kind && len(out) > 0 && out[len(out)-1] == kind {
			continue
		}
		out = append(out, k)
	}
	return out
}

func TestRestoreCheck_EmptyTableForTenantPrintsDash(t *testing.T) {
	f := newRCFixture(t)
	f.invoice(t, "RC-1", "draft", f.cutoff.Add(-time.Hour))
	tables := f.run(t).tables(t)

	if got := tables["invoices"][2]; got != "1" {
		t.Fatalf("invoices count = %q, want 1", got)
	}
	if got := strings.Join(tables["documents"][2:], " | "); got != "0 | - | -" {
		t.Errorf("documents row tail = %q, want %q", got, "0 | - | -")
	}
}

// --- AC-2 / AC-3 ------------------------------------------------------------

func rcRowsDiffer(a, b map[string][]string) (changed []string) {
	for _, n := range rcTableNames {
		if strings.Join(a[n], "|") != strings.Join(b[n], "|") {
			changed = append(changed, n)
		}
	}
	return changed
}

func TestRestoreCheck_DetectsMissingOrChangedRows(t *testing.T) {
	cases := []struct {
		name  string
		table string
		apply func(t *testing.T, f *rcFixture, s rcFull)
	}{
		{"delete invoice", "invoices", func(t *testing.T, f *rcFixture, s rcFull) {
			f.exec(t, `DELETE FROM invoices WHERE id = $1`, s.invoices[2])
		}},
		{"delete line item", "line_items", func(t *testing.T, f *rcFixture, s rcFull) {
			f.exec(t, `DELETE FROM line_items WHERE id = $1`, s.lineItems[0])
		}},
		{"delete document", "documents", func(t *testing.T, f *rcFixture, s rcFull) {
			f.exec(t, `DELETE FROM documents WHERE id = $1`, s.document)
		}},
		{"delete membership", "memberships", func(t *testing.T, f *rcFixture, s rcFull) {
			f.exec(t, `DELETE FROM memberships WHERE id = $1`, s.membership)
		}},
		{"delete audit row under replica mode", "audit_log", func(t *testing.T, f *rcFixture, s rcFull) {
			ctx := context.Background()
			conn, err := pgx.Connect(ctx, f.dsn)
			if err != nil {
				t.Fatalf("connect: %v", err)
			}
			defer conn.Close(ctx)
			if _, err := conn.Exec(ctx, `SET session_replication_role = replica`); err != nil {
				t.Fatalf("replica mode: %v", err)
			}
			if _, err := conn.Exec(ctx, `DELETE FROM audit_log WHERE id = $1`, s.auditIDs[0]); err != nil {
				t.Fatalf("delete audit row: %v", err)
			}
		}},
		{"update invoice status", "invoices", func(t *testing.T, f *rcFixture, s rcFull) {
			f.exec(t, `UPDATE invoices SET status = 'rejected' WHERE id = $1`, s.invoices[0])
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newRCFixture(t)
			s := f.seedFull(t)
			before := f.run(t).tables(t)
			if before["invoices"][2] != "3" {
				t.Fatalf("baseline invoices count = %q, want 3", before["invoices"][2])
			}
			tc.apply(t, f, s)
			after := f.run(t).tables(t)

			changed := rcRowsDiffer(before, after)
			if len(changed) != 1 || changed[0] != tc.table {
				t.Errorf("changed table rows = %v, want only [%s]", changed, tc.table)
			}
		})
	}
}

func TestRestoreCheck_IgnoresRowsOutsideScope(t *testing.T) {
	t.Run("invoice at cutoff plus one second", func(t *testing.T) {
		f := newRCFixture(t)
		f.seedFull(t)
		before := f.run(t)
		if len(before.rows) == 0 {
			t.Fatal("baseline output is empty")
		}
		f.invoice(t, "RC-LATE", "draft", f.cutoff.Add(time.Second))
		if after := f.run(t); after.raw != before.raw {
			t.Errorf("output changed after a post-cutoff invoice:\n--- before\n%s\n--- after\n%s", before.raw, after.raw)
		}
	})
	t.Run("another tenant's invoice inserted then deleted", func(t *testing.T) {
		f := newRCFixture(t)
		f.seedFull(t)
		before := f.run(t)
		if len(before.rows) == 0 {
			t.Fatal("baseline output is empty")
		}
		o := newRCFixture(t)
		oid := o.invoice(t, "RC-OTHER", "draft", f.cutoff.Add(-time.Hour))
		if after := f.run(t); after.raw != before.raw {
			t.Errorf("output changed after another tenant's invoice:\n--- before\n%s\n--- after\n%s", before.raw, after.raw)
		}
		o.exec(t, `DELETE FROM invoices WHERE id = $1`, oid)
		if after := f.run(t); after.raw != before.raw {
			t.Errorf("output changed after another tenant's invoice was deleted:\n--- before\n%s\n--- after\n%s", before.raw, after.raw)
		}
	})
}

func TestRestoreCheck_IncludesRowAtCutoff(t *testing.T) {
	f := newRCFixture(t)
	f.invoice(t, "RC-OLD", "draft", f.cutoff.Add(-time.Hour))
	f.invoice(t, "RC-AT", "draft", f.cutoff)
	row := f.run(t).tables(t)["invoices"]

	if row[2] != "2" {
		t.Errorf("invoices count = %q, want 2 (the row at the cutoff counts)", row[2])
	}
	if got := rcInstant(t, row[3]); !got.Equal(f.cutoff) {
		t.Errorf("invoices max(created_at) = %s, want the cutoff %s", got, f.cutoff)
	}
}

// --- AC-4 -------------------------------------------------------------------

func TestRestoreCheck_RejectsBadInput(t *testing.T) {
	good := newRCFixture(t)
	good.invoice(t, "RC-1", "draft", good.cutoff.Add(-time.Hour))
	t.Run("valid inputs produce table rows", func(t *testing.T) {
		good.run(t).tables(t)
	})

	lateOnly := newRCFixture(t)
	lateOnly.invoice(t, "RC-LATE", "draft", lateOnly.cutoff.Add(time.Hour))

	valid := rcInputs(good.tenant, good.cutoff)
	cases := []struct {
		name     string
		settings map[string]string
		unset    bool
	}{
		{"unknown tenant", rcInputs(uuid.NewString(), good.cutoff), false},
		{"tenant whose only invoice is after the cutoff", rcInputs(lateOnly.tenant, lateOnly.cutoff), false},
		{"tenant unset", map[string]string{"restore_check.cutoff": valid["restore_check.cutoff"]}, true},
		{"cutoff unset", map[string]string{"restore_check.tenant": good.tenant}, true},
		{"tenant empty", rcWith(valid, "restore_check.tenant", ""), false},
		{"cutoff empty", rcWith(valid, "restore_check.cutoff", ""), false},
		{"cutoff without offset", rcWith(valid, "restore_check.cutoff", "2026-10-02 08:00:00"), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := rcRun(t, good.dsn, tc.settings)
			if tc.unset && err != nil {
				var pgErr *pgconn.PgError
				if errors.As(err, &pgErr) && pgErr.Code == "42704" {
					return
				}
			}
			requireRCPgMessage(t, err, "restore-check:")
		})
	}
}

// --- AC-5 -------------------------------------------------------------------

func TestRestoreCheck_FailsWithoutBypassRLS(t *testing.T) {
	appDSN := os.Getenv("DATABASE_URL")
	if appDSN == "" {
		t.Skip("DATABASE_URL not set; skipping the invoice_app run")
	}
	f := newRCFixture(t)
	f.invoice(t, "RC-1", "draft", f.cutoff.Add(-time.Hour))
	// The same inputs must pass as superuser, so the failure below is the role.
	if out := f.run(t); len(out.kind("role")) != 1 {
		t.Errorf("superuser run produced no role row: %v", out.rows)
	}

	_, err := rcRun(t, appDSN, rcInputs(f.tenant, f.cutoff))
	requireRCPgMessage(t, err, "restore-check:")
}

func TestRestoreCheck_RoleRowNamesTheCaller(t *testing.T) {
	f := newRCFixture(t)
	f.invoice(t, "RC-1", "draft", f.cutoff.Add(-time.Hour))
	cfg, err := pgx.ParseConfig(f.dsn)
	if err != nil {
		t.Fatalf("parse DSN: %v", err)
	}
	rows := f.run(t).kind("role")
	if len(rows) != 1 {
		t.Fatalf("role rows = %v, want exactly one", rows)
	}
	if got, want := strings.Join(rows[0][1:], " | "), cfg.User+" | t | t"; got != want {
		t.Errorf("role row tail = %q, want %q", got, want)
	}
}

// --- AC-6 -------------------------------------------------------------------

func TestRestoreCheck_RecentRows(t *testing.T) {
	cases := []struct {
		name   string
		before int
		after  int
	}{
		{"six before the cutoff and one after", 6, 1},
		{"three invoices", 3, 0},
	}
	statuses := []string{"draft", "validated", "queued", "submitted", "accepted", "rejected"}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newRCFixture(t)
			type inv struct {
				id, number, status string
				at                 time.Time
			}
			var seeded []inv // newest first
			for i := 0; i < tc.before; i++ {
				at := f.cutoff.Add(-time.Duration(i+1) * time.Hour)
				n, st := fmt.Sprintf("RC-%d", i), statuses[i%len(statuses)]
				seeded = append(seeded, inv{f.invoice(t, n, st, at), n, st, at})
			}
			if tc.after > 0 {
				f.invoice(t, "RC-LATE", "draft", f.cutoff.Add(time.Hour))
			}

			recent := f.run(t).kind("recent")
			want := min(5, tc.before)
			if len(recent) != want {
				t.Fatalf("recent rows = %d, want %d: %v", len(recent), want, recent)
			}
			for i, r := range recent {
				w := seeded[i]
				if len(r) != 5 || r[1] != w.id || r[2] != w.number || r[3] != w.status {
					t.Errorf("recent[%d] = %v, want id %s number %s status %s", i, r, w.id, w.number, w.status)
					continue
				}
				if got := rcInstant(t, r[4]); !got.Equal(w.at) {
					t.Errorf("recent[%d] created_at = %s, want %s", i, got, w.at)
				}
			}
			for _, r := range recent {
				if r[2] == "RC-LATE" {
					t.Errorf("post-cutoff invoice appears in recent rows: %v", r)
				}
			}
		})
	}
}

// --- AC-7 -------------------------------------------------------------------

func TestRestoreCheck_OutputIndependentOfSessionSettings(t *testing.T) {
	f := newRCFixture(t)
	f.seedFull(t)
	in := rcInputs(f.tenant, f.cutoff)

	runs := []struct {
		name string
		set  map[string]string
	}{
		{"UTC ISO", rcWith(in, "TimeZone", "UTC", "DateStyle", "ISO, YMD")},
		{"Lagos", rcWith(in, "TimeZone", "Africa/Lagos")},
		{"SQL DMY", rcWith(in, "DateStyle", "SQL, DMY")},
		{"Lagos and SQL DMY", rcWith(in, "TimeZone", "Africa/Lagos", "DateStyle", "SQL, DMY")},
		{"UTC ISO again", rcWith(in, "TimeZone", "UTC", "DateStyle", "ISO, YMD")},
	}
	var first string
	for i, r := range runs {
		out := rcMustRun(t, f.dsn, r.set)
		if len(out.rows) == 0 {
			t.Fatalf("%s: empty output", r.name)
		}
		if i == 0 {
			first = out.raw
			continue
		}
		if out.raw != first {
			t.Errorf("%s output differs from %s:\n--- first\n%s\n--- this\n%s", r.name, runs[0].name, first, out.raw)
		}
	}
}

// --- AC-8 -------------------------------------------------------------------

func TestRestoreCheck_LedgerAndOwnerRows(t *testing.T) {
	f := newRCFixture(t)
	f.invoice(t, "RC-1", "draft", f.cutoff.Add(-time.Hour))
	out := f.run(t)

	var count, maxVer int64
	if err := f.pool.QueryRow(context.Background(),
		`SELECT count(*), max(version_id) FROM goose_db_version WHERE is_applied`).Scan(&count, &maxVer); err != nil {
		t.Fatalf("read goose_db_version: %v", err)
	}
	goose := out.kind("goose")
	if len(goose) != 1 || len(goose[0]) != 4 {
		t.Fatalf("goose rows = %v, want one row of 4 fields", goose)
	}
	if goose[0][1] != fmt.Sprint(count) || goose[0][2] != fmt.Sprint(maxVer) {
		t.Errorf("goose row = %v, want count %d and max %d", goose[0], count, maxVer)
	}
	if !rcHash.MatchString(goose[0][3]) {
		t.Errorf("goose hash = %q, want 32 hex", goose[0][3])
	}

	rows, err := f.pool.Query(context.Background(),
		`SELECT n.nspname, pg_get_userbyid(c.relowner), count(*)
		   FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
		  WHERE n.nspname IN ('public', 'auth') GROUP BY 1, 2`)
	if err != nil {
		t.Fatalf("read pg_class: %v", err)
	}
	defer rows.Close()
	var want []string
	for rows.Next() {
		var schema, role string
		var n int64
		if err := rows.Scan(&schema, &role, &n); err != nil {
			t.Fatalf("scan pg_class: %v", err)
		}
		want = append(want, fmt.Sprintf("%s|%s|%d", schema, role, n))
	}
	if len(want) == 0 {
		t.Fatal("pg_class grouping is empty")
	}
	var got []string
	for _, r := range out.kind("owner") {
		if len(r) != 4 {
			t.Fatalf("owner row has %d fields, want 4: %v", len(r), r)
		}
		got = append(got, strings.Join(r[1:], "|"))
	}
	sort.Strings(want)
	sort.Strings(got)
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("owner rows = %v, want %v", got, want)
	}
}

// --- AC-9 -------------------------------------------------------------------

func TestRestoreCheck_LeavesConnectionReadOnly(t *testing.T) {
	ctx := context.Background()
	f := newRCFixture(t)
	f.invoice(t, "RC-1", "draft", f.cutoff.Add(-time.Hour))

	control, err := pgx.Connect(ctx, f.dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer control.Close(ctx)
	if _, err := control.Exec(ctx, `CREATE TEMP TABLE rc_control(i int)`); err != nil {
		t.Fatalf("control connection cannot write: %v", err)
	}

	conn, err := pgx.Connect(ctx, f.dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer conn.Close(ctx)
	if _, err := rcRunOn(ctx, t, conn, rcInputs(f.tenant, f.cutoff)); err != nil {
		t.Fatalf("restore-check failed: %v", err)
	}
	_, err = conn.Exec(ctx, `CREATE TEMP TABLE rc_after(i int)`)
	requireRCPgMessage(t, err, "read-only transaction")
}

// bundle_audit_db_test.go: the audit rows of both evidence-bundle downloads against a real Postgres.
// plant purges the rows a test writes; audit_log and staff_audit_log are append-only, so it runs in replica mode.
package archive

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
	"unsafe"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/SimonOsipov/invoice-os/internal/audit"
	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
	"github.com/SimonOsipov/invoice-os/internal/platform/db"
)

const (
	evExported = "evidence_bundle.exported"
	evFailed   = "evidence_bundle.export_failed"
	msgStarted = "archive: evidence bundle download started"
	msgNoAudit = "archive: evidence bundle failure not audited"
)

var (
	pkCentral = []byte("PK\x01\x02")
	pkEnd     = []byte("PK\x05\x06")
	routeKeys = []string{"member", "staff"}
)

type bundleAuditRow struct {
	actor, payload string
	entity         *string
	at             time.Time
}

type bundleStaffRow struct {
	actor, tenant, subject, payload string
	at                              time.Time
}

func auditRowsOf(t *testing.T, super *pgxpool.Pool, tenant, event string) []bundleAuditRow {
	t.Helper()
	rows, err := super.Query(context.Background(),
		`SELECT actor, payload::text, entity_id::text, created_at FROM audit_log WHERE tenant_id = $1 AND event = $2 ORDER BY id`, tenant, event)
	if err != nil {
		t.Fatalf("read audit_log %s: %v", event, err)
	}
	defer rows.Close()
	var out []bundleAuditRow
	for rows.Next() {
		var r bundleAuditRow
		if err := rows.Scan(&r.actor, &r.payload, &r.entity, &r.at); err != nil {
			t.Fatalf("scan audit_log: %v", err)
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("read audit_log: %v", err)
	}
	return out
}

func staffRowsOf(t *testing.T, super *pgxpool.Pool, tenant, event string) []bundleStaffRow {
	t.Helper()
	rows, err := super.Query(context.Background(),
		`SELECT actor::text, tenant_id::text, coalesce(subject_id::text, ''), payload::text, created_at
		   FROM staff_audit_log WHERE tenant_id = $1 AND event = $2 ORDER BY id`, tenant, event)
	if err != nil {
		t.Fatalf("read staff_audit_log %s: %v", event, err)
	}
	defer rows.Close()
	var out []bundleStaffRow
	for rows.Next() {
		var r bundleStaffRow
		if err := rows.Scan(&r.actor, &r.tenant, &r.subject, &r.payload, &r.at); err != nil {
			t.Fatalf("scan staff_audit_log: %v", err)
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("read staff_audit_log: %v", err)
	}
	return out
}

// bundleCounts is the number of rows of each bundle event the tenant holds.
type bundleCounts struct{ exported, failed, staffExported, staffFailed int }

func (r *staffRig) counts(t *testing.T, tenant string) bundleCounts {
	t.Helper()
	return bundleCounts{
		exported:      len(auditRowsOf(t, r.super, tenant, evExported)),
		failed:        len(auditRowsOf(t, r.super, tenant, evFailed)),
		staffExported: len(staffRowsOf(t, r.super, tenant, evExported)),
		staffFailed:   len(staffRowsOf(t, r.super, tenant, evFailed)),
	}
}

func purgeAudit(t *testing.T, super *pgxpool.Pool, query string, args ...any) {
	t.Helper()
	ctx := context.Background()
	tx, err := super.Begin(ctx)
	if err != nil {
		t.Errorf("purge: begin: %v", err)
		return
	}
	defer func() { _ = tx.Rollback(ctx) }()
	for _, q := range []string{`SET LOCAL session_replication_role = 'replica'`, query} {
		a := args
		if strings.HasPrefix(q, "SET") {
			a = nil
		}
		if _, err := tx.Exec(ctx, q, a...); err != nil {
			t.Errorf("purge %q: %v", q, err)
			return
		}
	}
	if err := tx.Commit(ctx); err != nil {
		t.Errorf("purge: commit: %v", err)
	}
}

// plant is plantBundleTenant plus a cleanup of the audit rows the tenant collects.
func (r *staffRig) plant(t *testing.T, label string, n int) bundleFx {
	t.Helper()
	fx := plantBundleTenant(t, r.super, label, "INV-AUD-"+strings.ToUpper(label), n)
	t.Cleanup(func() {
		purgeAudit(t, r.super, `DELETE FROM audit_log WHERE tenant_id = $1`, fx.tenant)
		purgeAudit(t, r.super, `DELETE FROM staff_audit_log WHERE tenant_id = $1`, fx.tenant)
	})
	return fx
}

// bundleRoute is one download route as one caller uses it.
type bundleRoute struct {
	name   string
	staff  bool
	ctx    context.Context
	actor  string // audit_log.actor of the row
	caller string // "actor" in the logs
	run    func(s *Store, ctx context.Context, req Request, w io.Writer, onStart func(string)) error
}

func (r *staffRig) route(name string, fx bundleFx) bundleRoute {
	if name == "staff" {
		return bundleRoute{name: name, staff: true, ctx: r.ctx, actor: audit.SupportActor, caller: r.staff,
			run: func(s *Store, ctx context.Context, req Request, w io.Writer, onStart func(string)) error {
				return s.StaffAssemble(ctx, fx.tenant, req, w, onStart)
			}}
	}
	return bundleRoute{name: name, ctx: auth.WithIdentity(context.Background(), auth.Identity{Subject: fx.admin, TenantID: fx.tenant}),
		actor: fx.admin, caller: fx.admin,
		run: func(s *Store, ctx context.Context, req Request, w io.Writer, onStart func(string)) error {
			return s.Assemble(ctx, req, w, onStart)
		}}
}

// expect is the counts the route leaves for exported and failed rows; the staff route doubles each into staff_audit_log.
func (rt bundleRoute) expect(exported, failed int) bundleCounts {
	c := bundleCounts{exported: exported, failed: failed}
	if rt.staff {
		c.staffExported, c.staffFailed = exported, failed
	}
	return c
}

func requireCounts(t *testing.T, got, want bundleCounts) {
	t.Helper()
	if got != want {
		t.Errorf("rows {audit exported, audit failed, staff exported, staff failed} = %+v, want %+v", got, want)
	}
}

// requirePayload asserts the five keys and the request's own values; it returns files and bytes.
func requirePayload(t *testing.T, raw string, fx bundleFx) (files, sent int64) {
	t.Helper()
	var p map[string]any
	if err := json.Unmarshal([]byte(raw), &p); err != nil {
		t.Fatalf("payload %q: %v", raw, err)
	}
	keys := make([]string, 0, len(p))
	for k := range p {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	if !slices.Equal(keys, []string{"bytes", "entity_id", "files", "from", "to"}) {
		t.Errorf("payload keys = %v, want exactly bytes, entity_id, files, from, to; payload %s", keys, raw)
	}
	if p["entity_id"] != fx.entity {
		t.Errorf("payload entity_id = %v, want %s", p["entity_id"], fx.entity)
	}
	if p["from"] != fx.req.From.UTC().Format(time.RFC3339) || p["to"] != fx.req.To.UTC().Format(time.RFC3339) {
		t.Errorf("payload period = %v..%v, want %s..%s", p["from"], p["to"], fx.req.From.UTC().Format(time.RFC3339), fx.req.To.UTC().Format(time.RFC3339))
	}
	f, _ := p["files"].(float64)
	b, _ := p["bytes"].(float64)
	return int64(f), int64(b)
}

// requireExported asserts an exported row's files and bytes against the ZIP the caller received:
// files = manifest entries, bytes = the sum of their sizes (the workspace export's rule).
func requireExported(t *testing.T, raw string, fx bundleFx, zipRaw []byte) {
	t.Helper()
	_, body := zipEntries(t, zipRaw)
	var doc manifestDoc
	if err := json.Unmarshal(body["manifest.json"], &doc); err != nil {
		t.Fatalf("unmarshal manifest: %v", err)
	}
	var sum int64
	for _, e := range doc.Entries {
		sum += e.Bytes
	}
	if sum <= 0 {
		t.Fatalf("control: the manifest entries sum to %d bytes, want > 0", sum)
	}
	files, sent := requirePayload(t, raw, fx)
	if files != int64(len(doc.Entries)) {
		t.Errorf("payload files = %d, want the manifest's %d entries; payload %s", files, len(doc.Entries), raw)
	}
	if sent != sum {
		t.Errorf("payload bytes = %d, want %d (the sum of the manifest entries' bytes); payload %s", sent, sum, raw)
	}
}

// logCapture holds the JSON log lines of the default logger.
type logCapture struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (c *logCapture) Write(p []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.buf.Write(p)
}

func (c *logCapture) text() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.buf.String()
}

func captureLogs(t *testing.T) *logCapture {
	t.Helper()
	c := &logCapture{}
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(c, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return c
}

// logRecords returns the records of text whose msg is msg.
func logRecords(t *testing.T, text, msg string) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, line := range strings.Split(text, "\n") {
		if line == "" {
			continue
		}
		var rec map[string]any
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			continue
		}
		if rec["msg"] == msg {
			out = append(out, rec)
		}
	}
	return out
}

// withAuditTx sets Store.auditTx by reflection so the package compiles before the field exists.
func withAuditTx(t *testing.T, s *Store, fn func(ctx context.Context, tenantID string, write func(pgx.Tx) error) error) {
	t.Helper()
	f := reflect.ValueOf(s).Elem().FieldByName("auditTx")
	if !f.IsValid() {
		t.Errorf("Store has no auditTx field (the audit write seam)")
		return
	}
	reflect.NewAt(f.Type(), unsafe.Pointer(f.UnsafeAddr())).Elem().Set(reflect.ValueOf(fn))
}

func (r *staffRig) storeCopy() *Store { return &Store{pool: r.app, maxInvoices: maxBundleInvoices} }

// failOnWrite fails the nth sink write and accepts the rest.
func failOnWrite(n int) *wsSink {
	return &wsSink{hook: func(i int, _ []byte) error {
		if i == n {
			return errors.New("client went away")
		}
		return nil
	}}
}

func TestRLS_StaffBundleWritesOneStaffRowAndOneSupportRow(t *testing.T) {
	r := newStaffRig(t)
	fx := r.plant(t, "aud-one", 2)

	var buf bytes.Buffer
	if err := r.store.StaffAssemble(r.ctx, fx.tenant, fx.req, &buf, nil); err != nil {
		t.Fatalf("StaffAssemble: %v", err)
	}

	staff := staffRowsOf(t, r.super, fx.tenant, evExported)
	support := auditRowsOf(t, r.super, fx.tenant, evExported)
	if len(staff) != 1 || len(support) != 1 {
		t.Fatalf("staff_audit_log rows = %d, audit_log rows = %d, want 1 and 1", len(staff), len(support))
	}
	s, a := staff[0], support[0]
	if s.actor != r.staff || s.tenant != fx.tenant || s.subject != fx.entity {
		t.Errorf("staff row actor/tenant/subject = %s/%s/%s, want %s/%s/%s", s.actor, s.tenant, s.subject, r.staff, fx.tenant, fx.entity)
	}
	requireExported(t, s.payload, fx, buf.Bytes())
	if a.actor != audit.SupportActor {
		t.Errorf("audit_log actor = %q, want %q", a.actor, audit.SupportActor)
	}
	if a.entity == nil || *a.entity != fx.entity {
		t.Errorf("audit_log entity_id = %v, want %s", a.entity, fx.entity)
	}
	var sp, ap map[string]any
	if json.Unmarshal([]byte(s.payload), &sp) != nil || json.Unmarshal([]byte(a.payload), &ap) != nil || !reflect.DeepEqual(sp, ap) {
		t.Errorf("payloads differ: staff %s, support %s", s.payload, a.payload)
	}
	if !s.at.Equal(a.at) {
		t.Errorf("created_at differs: staff %v, support %v", s.at, a.at)
	}
}

func TestRLS_StaffBundleAuditLandsOnlyInThePathTenant(t *testing.T) {
	r := newStaffRig(t)
	a := r.plant(t, "aud-path-a", 1)
	b := r.plant(t, "aud-path-b", 1)
	var house string
	if err := r.super.QueryRow(context.Background(), `SELECT tenant_id::text FROM public.house_company`).Scan(&house); err != nil {
		t.Fatalf("read the house tenant: %v", err)
	}
	total := func(tenant string) int {
		var n int
		if err := r.super.QueryRow(context.Background(), `SELECT count(*) FROM audit_log WHERE tenant_id = $1`, tenant).Scan(&n); err != nil {
			t.Fatalf("count audit_log: %v", err)
		}
		return n
	}
	bBefore, houseBefore := total(b.tenant), total(house)

	if err := r.store.StaffAssemble(r.ctx, a.tenant, a.req, io.Discard, nil); err != nil {
		t.Fatalf("StaffAssemble: %v", err)
	}

	if n := len(auditRowsOf(t, r.super, a.tenant, evExported)); n != 1 {
		t.Errorf("path tenant exported rows = %d, want 1", n)
	}
	if n := total(b.tenant); n != bBefore {
		t.Errorf("other tenant audit_log rows = %d, want %d", n, bBefore)
	}
	if n := total(house); n != houseBefore {
		t.Errorf("house tenant audit_log rows = %d, want %d", n, houseBefore)
	}
	var leaks int
	if err := r.super.QueryRow(context.Background(),
		`SELECT count(*) FROM audit_log WHERE actor = $1 OR payload::text LIKE '%' || $1 || '%'`, r.staff).Scan(&leaks); err != nil {
		t.Fatalf("count staff uuid leaks: %v", err)
	}
	if leaks != 0 {
		t.Errorf("%d audit_log row(s) hold the staff uuid, want 0", leaks)
	}
}

func TestRLS_MemberBundleWritesOneAuditRow(t *testing.T) {
	r := newStaffRig(t)
	fx := r.plant(t, "aud-member", 2)

	rt := r.route("member", fx)
	var buf bytes.Buffer
	if err := rt.run(r.store, rt.ctx, fx.req, &buf, nil); err != nil {
		t.Fatalf("Store.Assemble: %v", err)
	}

	rows := auditRowsOf(t, r.super, fx.tenant, evExported)
	if len(rows) != 1 {
		t.Fatalf("audit_log exported rows = %d, want 1", len(rows))
	}
	if rows[0].actor != fx.admin {
		t.Errorf("actor = %q, want the member %s", rows[0].actor, fx.admin)
	}
	if rows[0].entity == nil || *rows[0].entity != fx.entity {
		t.Errorf("entity_id = %v, want %s", rows[0].entity, fx.entity)
	}
	requireExported(t, rows[0].payload, fx, buf.Bytes())
	var staffRows int
	if err := r.super.QueryRow(context.Background(), `SELECT count(*) FROM staff_audit_log WHERE tenant_id = $1`, fx.tenant).Scan(&staffRows); err != nil {
		t.Fatalf("count staff_audit_log: %v", err)
	}
	if staffRows != 0 {
		t.Errorf("staff_audit_log rows for the tenant = %d, want 0", staffRows)
	}
}

func TestRLS_ALockedWorkspaceBundleIsAudited(t *testing.T) {
	r := newStaffRig(t)
	fx := r.plant(t, "aud-locked", 1)
	if _, err := r.super.Exec(context.Background(),
		`UPDATE tenants SET deletion_scheduled_at = now(), deletion_scheduled_by = $2 WHERE id = $1`, fx.tenant, fx.admin); err != nil {
		t.Fatalf("lock the workspace: %v", err)
	}
	rt := r.route("member", fx)

	if err := rt.run(r.store, rt.ctx, fx.req, io.Discard, nil); err != nil {
		t.Fatalf("Store.Assemble in a locked workspace: %v", err)
	}
	requireCounts(t, r.counts(t, fx.tenant), rt.expect(1, 0))
}

func TestRLS_ALockedWorkspaceStaffBundleIsAudited(t *testing.T) {
	r := newStaffRig(t)
	fx := r.plant(t, "aud-locked-staff", 1)
	if _, err := r.super.Exec(context.Background(),
		`UPDATE tenants SET deletion_scheduled_at = now(), deletion_scheduled_by = $2 WHERE id = $1`, fx.tenant, fx.admin); err != nil {
		t.Fatalf("lock the workspace: %v", err)
	}

	if err := r.store.StaffAssemble(r.ctx, fx.tenant, fx.req, io.Discard, nil); err != nil {
		t.Fatalf("StaffAssemble in a locked workspace: %v", err)
	}
	requireCounts(t, r.counts(t, fx.tenant), r.route("staff", fx).expect(1, 0))
}

func TestRLS_ABundleRefusedBeforeTheFirstByteWritesNoRow(t *testing.T) {
	r := newStaffRig(t)
	for _, name := range routeKeys {
		t.Run(name, func(t *testing.T) {
			fx := r.plant(t, "aud-refuse-"+name, 2)
			rt := r.route(name, fx)
			refusals := map[string]func(sink *wsSink) error{
				"unknown entity": func(sink *wsSink) error {
					req := fx.req
					req.EntityID = uuid.NewString()
					return rt.run(r.store, rt.ctx, req, sink, nil)
				},
				"over the cap": func(sink *wsSink) error {
					return rt.run(&Store{pool: r.app, maxInvoices: 1}, rt.ctx, fx.req, sink, nil)
				},
				"context cancelled in onStart": func(sink *wsSink) error {
					ctx, cancel := context.WithCancel(rt.ctx)
					defer cancel()
					return rt.run(r.store, ctx, fx.req, sink, func(string) { cancel() })
				},
			}
			for label, refuse := range refusals {
				sink := &wsSink{}
				if err := refuse(sink); err == nil {
					t.Errorf("%s: want an error, got nil", label)
				}
				if sink.n != 0 {
					t.Errorf("%s: the sink got %d write(s), want 0", label, sink.n)
				}
			}
			requireCounts(t, r.counts(t, fx.tenant), bundleCounts{})

			if err := rt.run(r.store, rt.ctx, fx.req, io.Discard, nil); err != nil {
				t.Fatalf("control download: %v", err)
			}
			requireCounts(t, r.counts(t, fx.tenant), rt.expect(1, 0))
		})
	}
}

func TestRLS_ABundleThatFailsPartWayIsRecordedAsFailed(t *testing.T) {
	r := newStaffRig(t)
	for _, name := range routeKeys {
		t.Run(name, func(t *testing.T) {
			fx := r.plant(t, "aud-part-"+name, 2)
			rt := r.route(name, fx)
			sink := failOnWrite(3)

			if err := rt.run(r.store, rt.ctx, fx.req, sink, nil); err == nil {
				t.Fatal("want the sink error, got nil")
			}

			if sink.n < 3 {
				t.Errorf("control: the sink got %d write(s), want the failing 3rd", sink.n)
			}
			requireCounts(t, r.counts(t, fx.tenant), rt.expect(0, 1))
			rows := auditRowsOf(t, r.super, fx.tenant, evFailed)
			if len(rows) != 1 {
				return
			}
			if rows[0].actor != rt.actor {
				t.Errorf("failed row actor = %q, want %q", rows[0].actor, rt.actor)
			}
			files, sent := requirePayload(t, rows[0].payload, fx)
			if files < 1 || sent <= 0 || sent != int64(sink.size()) {
				t.Errorf("failed row files = %d, bytes = %d, want files >= 1 and bytes = the %d accepted; payload %s", files, sent, sink.size(), rows[0].payload)
			}
			if rt.staff {
				if s := staffRowsOf(t, r.super, fx.tenant, evFailed); len(s) == 1 && (s[0].actor != r.staff || s[0].subject != fx.entity) {
					t.Errorf("staff failed row actor/subject = %s/%s, want %s/%s", s[0].actor, s[0].subject, r.staff, fx.entity)
				}
			}
		})
	}
}

func TestRLS_ABundleFailingAtTheFirstByteIsRecordedAsFailed(t *testing.T) {
	r := newStaffRig(t)
	fx := r.plant(t, "aud-first", 1)
	rt := r.route("member", fx)

	if err := rt.run(r.store, rt.ctx, fx.req, failOnWrite(1), nil); err == nil {
		t.Fatal("want the sink error, got nil")
	}

	requireCounts(t, r.counts(t, fx.tenant), rt.expect(0, 1))
	if rows := auditRowsOf(t, r.super, fx.tenant, evFailed); len(rows) == 1 {
		if _, sent := requirePayload(t, rows[0].payload, fx); sent != 0 {
			t.Errorf("failed row bytes = %d, want 0; payload %s", sent, rows[0].payload)
		}
	}
}

func TestRLS_TheBundleCentralDirectoryFollowsTheCommittedRow(t *testing.T) {
	r := newStaffRig(t)
	fx := r.plant(t, "aud-cd", 2)
	rt := r.route("member", fx)
	fired, rowsThen := false, -1
	sink := &wsSink{hook: func(_ int, p []byte) error {
		if bytes.Contains(p, pkCentral) && !fired {
			fired = true
			return r.super.QueryRow(context.Background(),
				`SELECT count(*) FROM audit_log WHERE tenant_id = $1 AND event = $2`, fx.tenant, evExported).Scan(&rowsThen)
		}
		return nil
	}}

	if err := rt.run(r.store, rt.ctx, fx.req, sink, nil); err != nil {
		t.Fatalf("Store.Assemble: %v", err)
	}

	if !fired {
		t.Fatal("control: no sink write held the central directory")
	}
	if rowsThen != 1 {
		t.Errorf("exported rows when the central directory was written = %d, want 1", rowsThen)
	}
}

func TestRLS_ABundleCloseFailureKeepsOneExportedRow(t *testing.T) {
	r := newStaffRig(t)
	fx := r.plant(t, "aud-close", 2)
	rt := r.route("member", fx)
	fired := false
	sink := &wsSink{hook: func(_ int, p []byte) error {
		if bytes.Contains(p, pkCentral) {
			fired = true
			return errors.New("connection reset at close")
		}
		return nil
	}}

	if err := rt.run(r.store, rt.ctx, fx.req, sink, nil); err == nil {
		t.Fatal("want the close error, got nil")
	}

	if !fired {
		t.Fatal("control: no sink write held the central directory")
	}
	requireCounts(t, r.counts(t, fx.tenant), rt.expect(1, 0))
}

func TestRLS_AFailedExportedWriteLeavesNoCentralDirectory(t *testing.T) {
	r := newStaffRig(t)
	for _, name := range routeKeys {
		t.Run(name, func(t *testing.T) {
			fx := r.plant(t, "aud-noexp-"+name, 2)
			rt := r.route(name, fx)
			s := r.storeCopy()
			calls := 0
			withAuditTx(t, s, func(ctx context.Context, tenantID string, write func(pgx.Tx) error) error {
				calls++
				if calls == 1 {
					return errors.New("audit down")
				}
				return db.WithinTenantTxOpts(ctx, s.pool, tenantID, pgx.TxOptions{}, write)
			})
			sink := &wsSink{}

			if err := rt.run(s, rt.ctx, fx.req, sink, nil); err == nil {
				t.Error("want an error when the exported write fails, got nil")
			}

			if bytes.Contains(sink.data(), pkEnd) {
				t.Error("the sink holds a ZIP end-of-central-directory record, want none")
			}
			if sink.size() == 0 {
				t.Error("control: the sink got no bytes")
			}
			requireCounts(t, r.counts(t, fx.tenant), rt.expect(0, 1))
		})
	}
}

func TestRLS_AFailedBundleAuditWriteKeepsTheOriginalError(t *testing.T) {
	r := newStaffRig(t)
	for _, name := range routeKeys {
		t.Run(name, func(t *testing.T) {
			fx := r.plant(t, "aud-down-"+name, 2)
			rt := r.route(name, fx)
			logs := captureLogs(t)
			s := r.storeCopy()
			withAuditTx(t, s, func(context.Context, string, func(pgx.Tx) error) error { return errors.New("audit down") })
			errSink := errors.New("client went away")
			sink := &wsSink{hook: func(i int, _ []byte) error {
				if i == 3 {
					return errSink
				}
				return nil
			}}

			err := rt.run(s, rt.ctx, fx.req, sink, nil)

			if !errors.Is(err, errSink) {
				t.Errorf("error = %v, want the sink error", err)
			}
			recs := logRecords(t, logs.text(), msgNoAudit)
			if len(recs) != 1 {
				t.Fatalf("%q records = %d, want 1; logs %s", msgNoAudit, len(recs), logs.text())
			}
			rec := recs[0]
			if rec["entity_id"] != fx.entity {
				t.Errorf("log entity_id = %v, want %s", rec["entity_id"], fx.entity)
			}
			if f, _ := rec["files"].(float64); f < 1 {
				t.Errorf("log files = %v, want >= 1", rec["files"])
			}
			if b, _ := rec["bytes"].(float64); b <= 0 || int(b) != sink.size() {
				t.Errorf("log bytes = %v, want the %d accepted (> 0)", rec["bytes"], sink.size())
			}
			for _, k := range []string{"from", "to", "err"} {
				if rec[k] == nil || rec[k] == "" {
					t.Errorf("log lacks %q: %v", k, rec)
				}
			}
			requireCounts(t, r.counts(t, fx.tenant), bundleCounts{})
		})
	}
}

func TestRLS_StaffBundleAuditFollowsTheCaller(t *testing.T) {
	r := newStaffRig(t)
	fx := r.plant(t, "aud-caller", 1)
	cases := []struct {
		name    string
		ctx     context.Context
		calls   int
		wantErr error
		delta   int
	}{
		{"a staff member", r.ctx, 1, nil, 1},
		{"a customer member posing as staff", staffCtx(t, fx.admin), 1, db.ErrNotStaff, 0},
		{"no staff identity", context.Background(), 1, db.ErrNotStaff, 0},
		{"the same staff member twice", r.ctx, 2, nil, 2},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			before := r.counts(t, fx.tenant)
			for range c.calls {
				err := r.store.StaffAssemble(c.ctx, fx.tenant, fx.req, io.Discard, nil)
				if !errors.Is(err, c.wantErr) || (c.wantErr == nil && err != nil) {
					t.Errorf("StaffAssemble error = %v, want %v", err, c.wantErr)
				}
			}
			want := before
			want.exported += c.delta
			want.staffExported += c.delta
			requireCounts(t, r.counts(t, fx.tenant), want)
		})
	}
}

func TestRLS_AnEmptyPeriodBundleCountsItsFiveCSVs(t *testing.T) {
	r := newStaffRig(t)
	fx := r.plant(t, "aud-empty", 0)
	var staffZip bytes.Buffer
	if err := r.store.StaffAssemble(r.ctx, fx.tenant, fx.req, &staffZip, nil); err != nil {
		t.Fatalf("StaffAssemble: %v", err)
	}
	memberZip := r.tenantBundle(t, fx)

	want := []string{"invoices.csv", "line_items.csv", "status_history.csv", "submissions.csv", "exchange.csv", "manifest.json"}
	for name, raw := range map[string][]byte{"staff": staffZip.Bytes(), "member": memberZip} {
		if names, _ := zipEntries(t, raw); !slices.Equal(names, want) {
			t.Errorf("%s ZIP entries = %v, want %v", name, names, want)
		}
	}
	rows := map[string]bundleAuditRow{}
	for _, a := range auditRowsOf(t, r.super, fx.tenant, evExported) {
		rows[a.actor] = a
	}
	for actor, raw := range map[string][]byte{audit.SupportActor: staffZip.Bytes(), fx.admin: memberZip} {
		row, ok := rows[actor]
		if !ok {
			t.Errorf("no exported row for actor %q", actor)
			continue
		}
		requireExported(t, row.payload, fx, raw)
		if files, _ := requirePayload(t, row.payload, fx); files != 5 {
			t.Errorf("actor %q: files = %d, want 5", actor, files)
		}
	}
}

// cancelWriter is the response writer of a client that goes away on its 3rd write.
type cancelWriter struct {
	http.ResponseWriter
	cancel func()
	n      int
}

func (w *cancelWriter) Write(p []byte) (int, error) {
	w.n++
	if w.n == 3 {
		w.cancel()
		return 0, errors.New("client went away")
	}
	return w.ResponseWriter.Write(p)
}

func TestRLS_AClientDisconnectMidBundleStillAuditsTheFailure(t *testing.T) {
	r := newStaffRig(t)
	for _, name := range routeKeys {
		t.Run(name, func(t *testing.T) {
			fx := r.plant(t, "aud-gone-"+name, 2)
			rt := r.route(name, fx)
			ctx, cancel := context.WithCancel(rt.ctx)
			defer cancel()
			q := url.Values{"entity_id": {fx.entity}, "from": {fx.req.From.Format(time.RFC3339)}, "to": {fx.req.To.Format(time.RFC3339)}}
			req := httptest.NewRequest(http.MethodGet, "/?"+q.Encode(), nil).WithContext(ctx)
			h := http.HandlerFunc(DownloadHandler(r.store.Assemble, nil))
			if rt.staff {
				h = StaffDownloadHandler(r.store.StaffAssemble, slog.Default())
				req.SetPathValue("id", fx.tenant)
			}
			w := &cancelWriter{ResponseWriter: httptest.NewRecorder(), cancel: cancel}

			h.ServeHTTP(w, req)

			if w.n < 3 {
				t.Fatalf("control: the client got %d write(s), want the failing 3rd", w.n)
			}
			requireCounts(t, r.counts(t, fx.tenant), rt.expect(0, 1))
			if rows := auditRowsOf(t, r.super, fx.tenant, evFailed); len(rows) == 1 {
				if _, sent := requirePayload(t, rows[0].payload, fx); sent <= 0 {
					t.Errorf("failed row bytes = %d, want > 0; payload %s", sent, rows[0].payload)
				}
			}
		})
	}
}

func TestRLS_ABundleLogsItsFactsBeforeTheFirstByte(t *testing.T) {
	r := newStaffRig(t)
	for _, name := range routeKeys {
		t.Run(name, func(t *testing.T) {
			fx := r.plant(t, "aud-log-"+name, 1)
			rt := r.route(name, fx)
			logs := captureLogs(t)
			atFirstByte := ""
			sink := &wsSink{hook: func(i int, _ []byte) error {
				if i == 1 {
					atFirstByte = logs.text()
				}
				return nil
			}}

			if err := rt.run(r.store, rt.ctx, fx.req, sink, nil); err != nil {
				t.Fatalf("download: %v", err)
			}

			recs := logRecords(t, atFirstByte, msgStarted)
			if len(recs) != 1 {
				t.Fatalf("%q records at the first byte = %d, want 1; logs %s", msgStarted, len(recs), atFirstByte)
			}
			want := map[string]string{
				"actor": rt.caller, "tenant_id": fx.tenant, "entity_id": fx.entity,
				"from": fx.req.From.UTC().Format(time.RFC3339), "to": fx.req.To.UTC().Format(time.RFC3339),
			}
			for k, v := range want {
				if recs[0][k] != v {
					t.Errorf("log %s = %v, want %s", k, recs[0][k], v)
				}
			}

			unknown := fx.req
			unknown.EntityID = uuid.NewString()
			refused := captureLogs(t)
			if err := rt.run(r.store, rt.ctx, unknown, &wsSink{}, nil); err == nil {
				t.Error("control: unknown entity returned nil")
			}
			if n := len(logRecords(t, refused.text(), msgStarted)); n != 0 {
				t.Errorf("a refusal before the first byte logged %d %q record(s), want 0", n, msgStarted)
			}
		})
	}
}

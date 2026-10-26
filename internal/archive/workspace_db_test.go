package archive

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
	"github.com/SimonOsipov/invoice-os/internal/platform/db"
)

// --- fakes ------------------------------------------------------------------

// fakeObjects is the bucket: key to bytes, with per-key open failures.
type fakeObjects struct {
	mu      sync.Mutex
	objects map[string][]byte
	failKey map[string]error
}

func newFakeObjects() *fakeObjects {
	return &fakeObjects{objects: map[string][]byte{}, failKey: map[string]error{}}
}

func (f *fakeObjects) put(key string, b []byte) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.objects[key] = b
}

func (f *fakeObjects) setFail(key string, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err == nil {
		delete(f.failKey, key)
		return
	}
	f.failKey[key] = err
}

func (f *fakeObjects) open(_ context.Context, key string) (io.ReadCloser, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.failKey[key]; err != nil {
		return nil, err
	}
	b, ok := f.objects[key]
	if !ok {
		return nil, fmt.Errorf("fake bucket: no object %q", key)
	}
	return io.NopCloser(bytes.NewReader(b)), nil
}

// wsSink is the response writer: it counts writes and lets a test hook the nth one.
type wsSink struct {
	mu   sync.Mutex
	buf  bytes.Buffer
	n    int
	hook func(n int, p []byte) error
}

func (s *wsSink) Write(p []byte) (int, error) {
	s.mu.Lock()
	s.n++
	n := s.n
	s.mu.Unlock()
	if s.hook != nil {
		if err := s.hook(n, p); err != nil {
			return 0, err
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.Write(p)
}

func (s *wsSink) size() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.Len()
}

func (s *wsSink) data() []byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]byte(nil), s.buf.Bytes()...)
}

// wsGate blocks a sink on its first write until released.
type wsGate struct {
	started, release chan struct{}
	once             sync.Once
}

func newGate(t *testing.T) *wsGate {
	g := &wsGate{started: make(chan struct{}), release: make(chan struct{})}
	t.Cleanup(g.let)
	return g
}

func (g *wsGate) let() { g.once.Do(func() { close(g.release) }) }

func (g *wsGate) sink() *wsSink {
	return &wsSink{hook: func(n int, _ []byte) error {
		if n == 1 {
			close(g.started)
			<-g.release
		}
		return nil
	}}
}

// wsRun is one export held open on its first write.
type wsRun struct {
	done chan error
	gate *wsGate
	sink *wsSink
}

func startBlockedExport(t *testing.T, s *WorkspaceStore, ctx context.Context) *wsRun {
	t.Helper()
	g := newGate(t)
	r := &wsRun{done: make(chan error, 1), gate: g, sink: g.sink()}
	go func() { r.done <- s.AssembleWorkspace(ctx, r.sink, noStart) }()
	select {
	case <-g.started:
	case err := <-r.done:
		t.Fatalf("the export ended before its first write (err = %v), so nothing is held open", err)
	case <-time.After(30 * time.Second):
		t.Fatal("the export never reached its first write")
	}
	return r
}

func (r *wsRun) finish(t *testing.T) error {
	t.Helper()
	r.gate.let()
	select {
	case err := <-r.done:
		return err
	case <-time.After(60 * time.Second):
		t.Fatal("the held export never ended after release")
		return nil
	}
}

func noStart(string) {}

func idCtx(subject, tenant string) context.Context {
	return auth.WithIdentity(context.Background(), auth.Identity{Subject: subject, TenantID: tenant})
}

// --- fixtures ---------------------------------------------------------------

type wsShape struct {
	alpha         bool
	alphaInvoices int
	exchange      bool
	beta          bool
	docs          int
	audit         int
}

var (
	shapeFull    = wsShape{alpha: true, alphaInvoices: 2, exchange: true, beta: true, docs: 2, audit: 5}
	shapeFailing = wsShape{alpha: true, alphaInvoices: 1, docs: 2}
	shapeOneDoc  = wsShape{alpha: true, docs: 1}
	shapeEmpty   = wsShape{}
)

type wsDoc struct {
	hash, key string
	body      []byte
	name      *string
}

type wsFx struct {
	tenant, admin, preparer, reviewer, suspended string
	label                              string
	alpha, beta                        string
	invoiceNumbers                     []string
	exchange                           string
	docs                               []wsDoc
}

type wsRig struct {
	super *pgxpool.Pool
	app   *pgxpool.Pool
	rec   *sqlRecorder
	objs  *fakeObjects
	// filename is what the last exportOK handed to onStart.
	filename string
}

func newWsRig(t *testing.T) *wsRig {
	t.Helper()
	super := dbSuperPool(t)
	app, rec := dbAppPoolTraced(t)
	return &wsRig{super: super, app: app, rec: rec, objs: newFakeObjects()}
}

func (r *wsRig) store() *WorkspaceStore { return NewWorkspaceStore(r.app, r.objs.open) }

func mustCreateMemberRow(t *testing.T, tx pgx.Tx, tenant, user, role, status string) {
	t.Helper()
	if _, err := tx.Exec(context.Background(),
		`INSERT INTO memberships (tenant_id, user_id, role, status) VALUES ($1, $2, $3, $4)`,
		tenant, user, role, status); err != nil {
		t.Fatalf("insert memberships fixture (%s, %s): %v", role, status, err)
	}
}

// plant commits one tenant of the given shape; its objects join the shared fake bucket.
func (r *wsRig) plant(t *testing.T, base string, shape wsShape) wsFx {
	t.Helper()
	fx := wsFx{
		label: base + "-" + uuid.NewString()[:8],
		admin: uuid.NewString(), preparer: uuid.NewString(), reviewer: uuid.NewString(), suspended: uuid.NewString(),
	}
	now := time.Now().UTC()
	fx.tenant = mustCommitFixture(t, r.super, func(tx pgx.Tx) string {
		tid := mustCreateTenant(t, tx, "Okafor Holdings "+fx.label)
		mustCreateMemberRow(t, tx, tid, fx.admin, "admin", "active")
		mustCreateMemberRow(t, tx, tid, fx.preparer, "preparer", "active")
		mustCreateMemberRow(t, tx, tid, fx.reviewer, "reviewer", "active")
		mustCreateMemberRow(t, tx, tid, fx.suspended, "admin", "suspended")
		if shape.alpha {
			fx.alpha = mustCreateEntity(t, tx, tid, "Alpha Co", "80000070-0001")
			for i := range shape.alphaInvoices {
				num := fmt.Sprintf("INV-%s-%d", fx.label, i)
				fx.invoiceNumbers = append(fx.invoiceNumbers, num)
				inv := mustCreateInvoice(t, tx, invoiceFixture{tenantID: tid, entityID: fx.alpha, invoiceNumber: num, createdAt: now.Add(-time.Duration(i) * time.Minute)})
				if i == 0 && shape.exchange {
					mustCreateLineItem(t, tx, lineItemFixture{tenantID: tid, invoiceID: inv, lineNo: 1})
					job := mustCreateSubmissionJob(t, tx, submissionJobFixture{tenantID: tid, invoiceID: inv})
					mustCreateHistoryRow(t, tx, historyFixture{tenantID: tid, invoiceID: inv, toStatus: "validated", actor: "system"})
					body := `{"label":"` + fx.label + `"}`
					fx.exchange = mustCreateExchange(t, tx, exchangeFixture{tenantID: tid, submissionJobID: job, invoiceID: inv, requestBody: &body, responseBody: &body})
				}
			}
		}
		if shape.beta {
			fx.beta = mustCreateEntity(t, tx, tid, "Beta Co", "")
		}
		for i := range shape.docs {
			body := []byte(fmt.Sprintf("%%PDF-fixture %s doc %d\x00\xff", fx.label, i))
			sum := sha256.Sum256(body)
			d := wsDoc{hash: hex.EncodeToString(sum[:]), body: body}
			d.key = "tenants/" + tid + "/" + d.hash
			if i == 0 {
				n := "Scan.PDF"
				d.name = &n
			}
			if _, err := tx.Exec(context.Background(),
				`INSERT INTO documents (tenant_id, storage_key, content_hash, size_bytes, filename, created_at) VALUES ($1, $2, $3, $4, $5, $6)`,
				tid, d.key, d.hash, len(body), d.name, now.Add(-time.Hour+time.Duration(i)*time.Second)); err != nil {
				t.Fatalf("insert documents fixture: %v", err)
			}
			r.objs.put(d.key, body)
			fx.docs = append(fx.docs, d)
		}
		for i := range shape.audit {
			if _, err := tx.Exec(context.Background(),
				`INSERT INTO audit_log (tenant_id, actor, event, payload, created_at) VALUES ($1, $2, 'workspace.provisioned', $3, $4)`,
				tid, fx.admin, fmt.Sprintf(`{"marker":%q,"n":%d}`, fx.label, i), now.Add(-time.Duration(i+1)*time.Minute)); err != nil {
				t.Fatalf("insert audit_log fixture: %v", err)
			}
		}
		return tid
	})
	t.Cleanup(func() {
		ctx := context.Background()
		tx, err := r.super.Begin(ctx)
		if err != nil {
			t.Errorf("audit cleanup begin: %v", err)
			return
		}
		defer func() { _ = tx.Rollback(ctx) }()
		// audit_log is append-only; replica mode skips the trigger for the fixture's own rows.
		for _, stmt := range []string{`SET LOCAL session_replication_role = 'replica'`, `DELETE FROM audit_log WHERE tenant_id = '` + fx.tenant + `'`} {
			if _, err := tx.Exec(ctx, stmt); err != nil {
				t.Errorf("audit cleanup %q: %v", stmt, err)
				return
			}
		}
		if err := tx.Commit(ctx); err != nil {
			t.Errorf("audit cleanup commit: %v", err)
		}
	})
	return fx
}

func (r *wsRig) lock(t *testing.T, fx wsFx) {
	t.Helper()
	if _, err := r.super.Exec(context.Background(),
		`UPDATE tenants SET deletion_scheduled_at = now(), deletion_scheduled_by = $2 WHERE id = $1`, fx.tenant, fx.admin); err != nil {
		t.Fatalf("lock the workspace: %v", err)
	}
}

func (r *wsRig) countEvent(t *testing.T, tenant, event string) int {
	t.Helper()
	var n int
	if err := r.super.QueryRow(context.Background(), `SELECT count(*) FROM audit_log WHERE tenant_id = $1 AND event = $2`, tenant, event).Scan(&n); err != nil {
		t.Fatalf("count %s rows: %v", event, err)
	}
	return n
}

func (r *wsRig) auditTotal(t *testing.T, tenant string) int {
	t.Helper()
	var n int
	if err := r.super.QueryRow(context.Background(), `SELECT count(*) FROM audit_log WHERE tenant_id = $1`, tenant).Scan(&n); err != nil {
		t.Fatalf("count audit rows: %v", err)
	}
	return n
}

// eventRow reads the single row of event and fails unless there is exactly one.
func (r *wsRig) eventRow(t *testing.T, tenant, event string) (actor string, payload map[string]any, raw string) {
	t.Helper()
	if n := r.countEvent(t, tenant, event); n != 1 {
		t.Fatalf("%s rows = %d, want exactly 1", event, n)
	}
	if err := r.super.QueryRow(context.Background(),
		`SELECT actor, payload::text FROM audit_log WHERE tenant_id = $1 AND event = $2`, tenant, event).Scan(&actor, &raw); err != nil {
		t.Fatalf("read %s row: %v", event, err)
	}
	if err := json.Unmarshal([]byte(raw), &payload); err != nil {
		t.Fatalf("payload %q: %v", raw, err)
	}
	return actor, payload, raw
}

func (r *wsRig) exportOK(t *testing.T, fx wsFx) []byte {
	t.Helper()
	sink := &wsSink{}
	if err := r.store().AssembleWorkspace(idCtx(fx.admin, fx.tenant), sink, func(name string) { r.filename = name }); err != nil {
		t.Fatalf("AssembleWorkspace as the admin: %v", err)
	}
	return sink.data()
}

type wsManifestDoc struct {
	Format    string `json:"format"`
	TenantID  string `json:"tenant_id"`
	Companies []struct {
		ID   string  `json:"id"`
		Name string  `json:"name"`
		TIN  *string `json:"tin"`
		Dir  string  `json:"dir"`
	} `json:"companies"`
	Counts struct {
		Companies int `json:"companies"`
		Invoices  int `json:"invoices"`
		BodyFiles int `json:"body_files"`
		Documents int `json:"documents"`
		AuditRows int `json:"audit_rows"`
	} `json:"counts"`
	Entries []manifestEntry `json:"entries"`
	Notes   []string        `json:"notes"`
}

func parseWsManifest(t *testing.T, body map[string][]byte) wsManifestDoc {
	t.Helper()
	raw, ok := body["manifest.json"]
	if !ok {
		t.Fatal("the export has no manifest.json")
	}
	var m wsManifestDoc
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("manifest.json: %v", err)
	}
	return m
}

func csvNames(dir string) []string {
	return []string{dir + "invoices.csv", dir + "line_items.csv", dir + "status_history.csv", dir + "submissions.csv", dir + "exchange.csv"}
}

// --- Core AC 1: the layout, the bundle, the manifest -------------------------

func TestRLS_WorkspaceExportHoldsEveryCompanyDocumentAndTheLog(t *testing.T) {
	rig := newWsRig(t)
	fx := rig.plant(t, "layout", shapeFull)

	names, body := zipEntries(t, rig.exportOK(t, fx))

	a, b := "companies/alpha-co/", "companies/beta-co/"
	want := []string{
		a + "invoices.csv", a + "line_items.csv", a + "status_history.csv", a + "submissions.csv",
		a + "bodies/" + fx.exchange + ".request", a + "bodies/" + fx.exchange + ".response", a + "exchange.csv",
	}
	want = append(want, csvNames(b)...)
	want = append(want, "documents/"+fx.docs[0].hash+".pdf", "documents/"+fx.docs[1].hash, "audit_log.csv", "manifest.json")
	if !slices.Equal(names, want) {
		t.Fatalf("entries =\n%q\nwant\n%q", names, want)
	}
	for _, n := range csvNames(b) {
		if rows := parseCSV(t, body[n]); len(rows) != 1 {
			t.Errorf("%s has %d rows, want 1 (header only)", n, len(rows))
		}
	}
	if rows := parseCSV(t, body[a+"invoices.csv"]); len(rows) != 3 {
		t.Errorf("alpha invoices.csv has %d rows, want 3 (header + 2 invoices)", len(rows))
	}
	for _, d := range fx.docs {
		ext := ""
		if d.name != nil {
			ext = ".pdf"
		}
		if got := body["documents/"+d.hash+ext]; !bytes.Equal(got, d.body) {
			t.Errorf("documents/%s%s = %q, want the object's exact bytes %q", d.hash, ext, got, d.body)
		}
	}
}

func TestRLS_WorkspaceCompanyDirEqualsTheAllTimeBundle(t *testing.T) {
	rig := newWsRig(t)
	fx := rig.plant(t, "bundle", shapeFull)

	_, body := zipEntries(t, rig.exportOK(t, fx))

	compared := 0
	for dir, entity := range map[string]string{"alpha-co": fx.alpha, "beta-co": fx.beta} {
		var buf bytes.Buffer
		req := Request{EntityID: entity, From: time.Date(1, 1, 1, 0, 0, 0, 0, time.UTC), To: time.Date(9999, 12, 31, 23, 59, 59, 0, time.UTC)}
		if err := NewStore(rig.app).Assemble(idCtx(fx.admin, fx.tenant), req, &buf, nil); err != nil {
			t.Fatalf("Store.Assemble all-time for %s: %v", dir, err)
		}
		bundleNames, bundle := zipEntries(t, buf.Bytes())
		for _, n := range bundleNames {
			if n == "manifest.json" {
				continue
			}
			got, ok := body["companies/"+dir+"/"+n]
			if !ok {
				t.Errorf("export has no companies/%s/%s, which the bundle holds", dir, n)
				continue
			}
			if !bytes.Equal(got, bundle[n]) {
				t.Errorf("companies/%s/%s differs from the bundle's %s", dir, n, n)
			}
			compared++
		}
	}
	if compared != 7+5 {
		t.Errorf("compared %d entries, want 12 (alpha: 5 CSVs + 2 bodies, beta: 5 CSVs)", compared)
	}
}

func TestRLS_WorkspaceManifestChecksEveryEntry(t *testing.T) {
	rig := newWsRig(t)
	fx := rig.plant(t, "manifest", shapeFull)
	auditBefore := rig.auditTotal(t, fx.tenant)

	names, body := zipEntries(t, rig.exportOK(t, fx))
	m := parseWsManifest(t, body)

	if want := "ASComply_workspace_Okafor-Holdings-" + fx.label + "_" + time.Now().UTC().Format("20060102") + ".zip"; rig.filename != want {
		t.Errorf("download filename = %q, want %q", rig.filename, want)
	}
	if m.Format != "ascomply-workspace-export/1" {
		t.Errorf("format = %q, want ascomply-workspace-export/1", m.Format)
	}
	if m.TenantID != fx.tenant {
		t.Errorf("tenant_id = %q, want %q", m.TenantID, fx.tenant)
	}
	listed := names[:len(names)-1]
	if len(m.Entries) != len(listed) {
		t.Fatalf("manifest lists %d entries, the ZIP holds %d before manifest.json", len(m.Entries), len(listed))
	}
	for i, e := range m.Entries {
		if e.Name == "manifest.json" {
			t.Errorf("entry %d lists manifest.json itself", i)
		}
		if e.Name != listed[i] {
			t.Errorf("entry %d = %q, want %q", i, e.Name, listed[i])
			continue
		}
		sum := sha256.Sum256(body[e.Name])
		if e.Bytes != int64(len(body[e.Name])) || e.SHA256 != hex.EncodeToString(sum[:]) {
			t.Errorf("entry %s: bytes %d sha %s, want %d %x", e.Name, e.Bytes, e.SHA256, len(body[e.Name]), sum)
		}
	}
	if len(m.Companies) != 2 || m.Companies[0].Dir != "alpha-co" || m.Companies[1].Dir != "beta-co" ||
		m.Companies[0].Name != "Alpha Co" || m.Companies[0].ID != fx.alpha || m.Companies[1].ID != fx.beta {
		t.Errorf("companies = %+v, want alpha-co then beta-co with their ids", m.Companies)
	}
	if m.Counts.Companies != 2 || m.Counts.Invoices != 2 || m.Counts.Documents != 2 || m.Counts.BodyFiles != 2 || m.Counts.AuditRows != auditBefore {
		t.Errorf("counts = %+v, want companies 2, invoices 2, documents 2, body_files 2, audit_rows %d", m.Counts, auditBefore)
	}
	if auditBefore < 5 {
		t.Errorf("fixture holds %d audit rows, want at least 5", auditBefore)
	}
	if rows := parseCSV(t, body["audit_log.csv"]); len(rows)-1 != auditBefore {
		t.Errorf("audit_log.csv holds %d data rows, want %d", len(rows)-1, auditBefore)
	}
	if len(m.Notes) == 0 {
		t.Error("manifest has no notes")
	}
}

// --- Core AC 1: who may export -------------------------------------------------

func TestRLS_WorkspaceExportIsAdminOnly(t *testing.T) {
	rig := newWsRig(t)
	fx := rig.plant(t, "admin-only", shapeFull)
	other := rig.plant(t, "admin-only-other", shapeOneDoc)

	cases := []struct {
		name string
		ctx  context.Context
		want error
	}{
		{"active preparer", idCtx(fx.preparer, fx.tenant), ErrExportNotPermitted},
		{"active reviewer", idCtx(fx.reviewer, fx.tenant), ErrExportNotPermitted},
		{"admin of another workspace", idCtx(other.admin, fx.tenant), db.ErrNotActiveMember},
		{"tenant id that is not a uuid", idCtx(fx.admin, "not-a-uuid"), db.ErrNoTenant},
		{"suspended admin", idCtx(fx.suspended, fx.tenant), db.ErrNotActiveMember},
		{"subject with no row", idCtx(uuid.NewString(), fx.tenant), db.ErrNotActiveMember},
		{"subject system", idCtx("system", fx.tenant), db.ErrNoTenant},
		{"no identity", context.Background(), db.ErrNoTenant},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sink, started := &wsSink{}, false
			err := rig.store().AssembleWorkspace(tc.ctx, sink, func(string) { started = true })
			if !errors.Is(err, tc.want) {
				t.Fatalf("AssembleWorkspace error = %v, want %v", err, tc.want)
			}
			if sink.size() != 0 || started {
				t.Errorf("refusal wrote %d bytes, onStart called = %v, want 0 and false", sink.size(), started)
			}
			for _, tenant := range []string{fx.tenant, other.tenant} {
				if n := rig.countEvent(t, tenant, "workspace.exported") + rig.countEvent(t, tenant, "workspace.export_failed"); n != 0 {
					t.Errorf("refusal wrote %d export audit rows into %s, want 0", n, tenant)
				}
			}
		})
	}

	rig.exportOK(t, fx)
	if n := rig.countEvent(t, fx.tenant, "workspace.exported"); n != 1 {
		t.Errorf("control: the admin's export wrote %d workspace.exported rows, want 1", n)
	}
}

func TestRLS_ALockedWorkspaceStillExports(t *testing.T) {
	rig := newWsRig(t)
	fx := rig.plant(t, "locked", shapeOneDoc)
	rig.lock(t, fx)

	names, _ := zipEntries(t, rig.exportOK(t, fx))
	if len(names) == 0 || names[len(names)-1] != "manifest.json" {
		t.Errorf("entries = %q, want a ZIP ending in manifest.json", names)
	}
	if n := rig.countEvent(t, fx.tenant, "workspace.exported"); n != 1 {
		t.Errorf("workspace.exported rows = %d, want 1", n)
	}
}

// --- Core AC 5: the audit rows ---------------------------------------------------

func TestRLS_AnExportWritesOneAuditRowAndNoDocumentRead(t *testing.T) {
	rig := newWsRig(t)
	fx := rig.plant(t, "audit-one", shapeFull)
	before := [3]int{rig.countEvent(t, fx.tenant, "workspace.exported"), rig.countEvent(t, fx.tenant, "workspace.export_failed"), rig.countEvent(t, fx.tenant, "document.read")}

	_, body := zipEntries(t, rig.exportOK(t, fx))
	m := parseWsManifest(t, body)

	after := [3]int{rig.countEvent(t, fx.tenant, "workspace.exported"), rig.countEvent(t, fx.tenant, "workspace.export_failed"), rig.countEvent(t, fx.tenant, "document.read")}
	if after[0]-before[0] != 1 || after[1] != before[1] || after[2] != before[2] {
		t.Fatalf("event counts before %v after %v, want exported +1, export_failed +0, document.read +0", before, after)
	}
	actor, payload, _ := rig.eventRow(t, fx.tenant, "workspace.exported")
	if actor != fx.admin {
		t.Errorf("actor = %q, want the admin %q", actor, fx.admin)
	}
	var sum int64
	for _, e := range m.Entries {
		sum += e.Bytes
	}
	if payload["files"] != float64(len(m.Entries)) || payload["bytes"] != float64(sum) || len(payload) != 2 {
		t.Errorf("payload = %v, want files %d and bytes %d only", payload, len(m.Entries), sum)
	}
}

func TestRLS_AFailedExportRollsBackExportedAndAuditsTheFailure(t *testing.T) {
	rig := newWsRig(t)
	fx := rig.plant(t, "fail", shapeFailing)
	errOpen := errors.New("bucket gone")
	rig.objs.setFail(fx.docs[1].key, errOpen)

	sink := &wsSink{}
	err := rig.store().AssembleWorkspace(idCtx(fx.admin, fx.tenant), sink, noStart)
	if !errors.Is(err, errOpen) {
		t.Fatalf("AssembleWorkspace error = %v, want the open failure", err)
	}
	if n := rig.countEvent(t, fx.tenant, "workspace.exported"); n != 0 {
		t.Errorf("workspace.exported rows = %d, want 0", n)
	}
	actor, payload, raw := rig.eventRow(t, fx.tenant, "workspace.export_failed")
	if actor != fx.admin {
		t.Errorf("actor = %q, want the admin %q", actor, fx.admin)
	}
	if len(payload) != 2 || payload["files"] != float64(5+1) || payload["bytes"] != float64(sink.size()) || sink.size() == 0 {
		t.Errorf("payload = %s, want files 6 (5 company CSVs + the first document) and bytes %d (> 0)", raw, sink.size())
	}
	for _, leak := range []string{"Alpha", "Scan", "PDF", "@", fx.docs[0].hash} {
		if strings.Contains(raw, leak) {
			t.Errorf("payload %s holds %q, want counts only", raw, leak)
		}
	}
}

func TestRLS_AFailedExportOfALockedWorkspaceIsAudited(t *testing.T) {
	rig := newWsRig(t)
	fx := rig.plant(t, "fail-locked", shapeOneDoc)
	rig.lock(t, fx)
	errOpen := errors.New("bucket gone")
	rig.objs.setFail(fx.docs[0].key, errOpen)

	err := rig.store().AssembleWorkspace(idCtx(fx.admin, fx.tenant), &wsSink{}, noStart)
	if !errors.Is(err, errOpen) {
		t.Fatalf("AssembleWorkspace error = %v, want the open failure", err)
	}
	rig.eventRow(t, fx.tenant, "workspace.export_failed")
	if n := rig.countEvent(t, fx.tenant, "workspace.exported"); n != 0 {
		t.Errorf("workspace.exported rows = %d, want 0", n)
	}
}

func TestRLS_AClientDisconnectStillAuditsTheFailure(t *testing.T) {
	rig := newWsRig(t)
	fx := rig.plant(t, "disconnect", shapeFull)
	ctx, cancel := context.WithCancel(idCtx(fx.admin, fx.tenant))
	defer cancel()
	sink := &wsSink{hook: func(n int, _ []byte) error {
		if n == 3 {
			cancel()
			return errors.New("client went away")
		}
		return nil
	}}

	if err := rig.store().AssembleWorkspace(ctx, sink, noStart); err == nil {
		t.Fatal("AssembleWorkspace returned nil after the client went away")
	}
	_, payload, raw := rig.eventRow(t, fx.tenant, "workspace.export_failed")
	if files, _ := payload["files"].(float64); files < 1 {
		t.Errorf("payload = %s, want files >= 1", raw)
	}
	if n := rig.countEvent(t, fx.tenant, "workspace.exported"); n != 0 {
		t.Errorf("workspace.exported rows = %d, want 0", n)
	}
}

func TestRLS_AFailedAuditWriteKeepsTheOriginalError(t *testing.T) {
	rig := newWsRig(t)
	fx := rig.plant(t, "audit-down", shapeOneDoc)
	errOpen := errors.New("bucket gone")
	rig.objs.setFail(fx.docs[0].key, errOpen)

	var logs bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })

	s := rig.store()
	calls, gotFiles, gotBytes := 0, 0, int64(0)
	s.auditFailed = func(_ context.Context, _ auth.Identity, files int, bytes int64) error {
		calls++
		gotFiles, gotBytes = files, bytes
		return errors.New("audit down")
	}
	err := s.AssembleWorkspace(idCtx(fx.admin, fx.tenant), &wsSink{}, noStart)

	if !errors.Is(err, errOpen) {
		t.Fatalf("AssembleWorkspace error = %v, want the original open failure", err)
	}
	if calls != 1 || gotFiles < 1 || gotBytes < 1 {
		t.Errorf("auditFailed calls %d files %d bytes %d, want one call with counts above zero", calls, gotFiles, gotBytes)
	}
	if !strings.Contains(logs.String(), "archive: workspace export failure not audited") {
		t.Errorf("log = %q, want archive: workspace export failure not audited", logs.String())
	}
	if n := rig.countEvent(t, fx.tenant, "workspace.exported") + rig.countEvent(t, fx.tenant, "workspace.export_failed"); n != 0 {
		t.Errorf("export audit rows = %d, want 0", n)
	}
}

func TestRLS_ACloseFailureAfterCommitKeepsOneExportedRow(t *testing.T) {
	rig := newWsRig(t)
	fx := rig.plant(t, "close-fails", shapeFull)
	hit := false
	sink := &wsSink{hook: func(_ int, p []byte) error {
		if bytes.Contains(p, []byte("PK\x01\x02")) {
			hit = true
			return errors.New("connection reset")
		}
		return nil
	}}

	if err := rig.store().AssembleWorkspace(idCtx(fx.admin, fx.tenant), sink, noStart); err == nil {
		t.Fatal("AssembleWorkspace returned nil though the central directory write failed")
	}
	if !hit {
		t.Fatal("no write held the central directory signature, so the failure was never injected")
	}
	if n := rig.countEvent(t, fx.tenant, "workspace.exported"); n != 1 {
		t.Errorf("workspace.exported rows = %d, want 1 (committed before Close)", n)
	}
	if n := rig.countEvent(t, fx.tenant, "workspace.export_failed"); n != 0 {
		t.Errorf("workspace.export_failed rows = %d, want 0", n)
	}
}

func TestRLS_ARefusalWritesNeitherEvent(t *testing.T) {
	rig := newWsRig(t)
	fx := rig.plant(t, "refusals", shapeFull)
	s1, s2 := rig.store(), rig.store()
	adminCtx := idCtx(fx.admin, fx.tenant)

	run := startBlockedExport(t, s1, adminCtx)
	exported, failed := rig.countEvent(t, fx.tenant, "workspace.exported"), rig.countEvent(t, fx.tenant, "workspace.export_failed")

	if err := s2.AssembleWorkspace(idCtx(fx.preparer, fx.tenant), &wsSink{}, noStart); !errors.Is(err, ErrExportNotPermitted) {
		t.Errorf("preparer error = %v, want ErrExportNotPermitted", err)
	}
	if err := s2.AssembleWorkspace(adminCtx, &wsSink{}, noStart); !errors.Is(err, ErrExportRunning) {
		t.Errorf("second export of the workspace error = %v, want ErrExportRunning", err)
	}
	if err := s1.AssembleWorkspace(adminCtx, &wsSink{}, noStart); !errors.Is(err, ErrExportBusy) {
		t.Errorf("second export on the instance error = %v, want ErrExportBusy", err)
	}
	if e, f := rig.countEvent(t, fx.tenant, "workspace.exported"), rig.countEvent(t, fx.tenant, "workspace.export_failed"); e != exported || f != failed {
		t.Errorf("after three refusals exported %d failed %d, want %d and %d", e, f, exported, failed)
	}

	if err := run.finish(t); err != nil {
		t.Fatalf("the held export failed: %v", err)
	}
	if e, f := rig.countEvent(t, fx.tenant, "workspace.exported"), rig.countEvent(t, fx.tenant, "workspace.export_failed"); e != exported+1 || f != 0 {
		t.Errorf("after the held export exported %d failed %d, want %d and 0", e, f, exported+1)
	}
}

// --- Concurrency: D18, D26 ---------------------------------------------------------

func TestRLS_ASecondExportOnTheInstanceIs503(t *testing.T) {
	rig := newWsRig(t)
	fx := rig.plant(t, "busy", shapeFull)
	s := rig.store()
	ctx := idCtx(fx.admin, fx.tenant)

	run := startBlockedExport(t, s, ctx)
	rig.rec.reset()
	second := &wsSink{}
	err := s.AssembleWorkspace(ctx, second, noStart)
	begins := rig.rec.mentioning("begin")
	if !errors.Is(err, ErrExportBusy) {
		t.Errorf("second export error = %v, want ErrExportBusy", err)
	}
	if len(begins) != 0 || second.size() != 0 {
		t.Errorf("the refused export issued %d BEGIN and wrote %d bytes, want 0 and 0", len(begins), second.size())
	}

	if err := run.finish(t); err != nil {
		t.Fatalf("the held export failed: %v", err)
	}
	rig.exportAfterRelease(t, s, fx)
}

func (r *wsRig) exportAfterRelease(t *testing.T, s *WorkspaceStore, fx wsFx) {
	t.Helper()
	if err := s.AssembleWorkspace(idCtx(fx.admin, fx.tenant), &wsSink{}, noStart); err != nil {
		t.Errorf("export after the release: %v", err)
	}
}

func TestRLS_ASecondExportOfTheWorkspaceIs409(t *testing.T) {
	rig := newWsRig(t)
	fx := rig.plant(t, "running", shapeFull)
	s1, s2 := rig.store(), rig.store()
	ctx := idCtx(fx.admin, fx.tenant)

	run := startBlockedExport(t, s1, ctx)
	second := &wsSink{}
	if err := s2.AssembleWorkspace(ctx, second, noStart); !errors.Is(err, ErrExportRunning) {
		t.Errorf("second store's export error = %v, want ErrExportRunning", err)
	}
	if second.size() != 0 {
		t.Errorf("the refused export wrote %d bytes, want 0", second.size())
	}

	if err := run.finish(t); err != nil {
		t.Fatalf("the held export failed: %v", err)
	}
	rig.exportAfterRelease(t, s2, fx)
}

func TestRLS_AnotherWorkspaceExportsWhileOneRuns(t *testing.T) {
	rig := newWsRig(t)
	a := rig.plant(t, "conc-a", shapeFull)
	b := rig.plant(t, "conc-b", shapeOneDoc)
	s1, s2 := rig.store(), rig.store()

	run := startBlockedExport(t, s1, idCtx(a.admin, a.tenant))
	sink := &wsSink{}
	if err := s2.AssembleWorkspace(idCtx(b.admin, b.tenant), sink, noStart); err != nil {
		t.Fatalf("B's export while A's runs: %v", err)
	}
	if names, _ := zipEntries(t, sink.data()); len(names) == 0 {
		t.Error("B's ZIP holds no entries")
	}
	if n := rig.countEvent(t, b.tenant, "workspace.exported"); n != 1 {
		t.Errorf("B's workspace.exported rows = %d, want 1", n)
	}
	if err := run.finish(t); err != nil {
		t.Errorf("A's export after release: %v", err)
	}
}

func TestRLS_TheWorkspaceClaimEndsWithItsTransaction(t *testing.T) {
	rig := newWsRig(t)
	fx := rig.plant(t, "claim", shapeOneDoc)
	s := rig.store()
	ctx := idCtx(fx.admin, fx.tenant)
	tag := "workspace-export:" + fx.tenant

	if got := exportTag(fx.tenant); got != tag {
		t.Errorf("exportTag = %q, want %q", got, tag)
	}
	sessions := func() int {
		var n int
		if err := rig.super.QueryRow(context.Background(), `SELECT count(*) FROM pg_stat_activity WHERE application_name = $1`, tag).Scan(&n); err != nil {
			t.Fatalf("read pg_stat_activity: %v", err)
		}
		return n
	}
	settles := func(label string) {
		t.Helper()
		deadline := time.Now().Add(3 * time.Second)
		for sessions() != 0 {
			if time.Now().After(deadline) {
				t.Fatalf("%s: %d session(s) still carry %q", label, sessions(), tag)
			}
			time.Sleep(50 * time.Millisecond)
		}
	}

	run := startBlockedExport(t, s, ctx)
	if n := sessions(); n != 1 {
		t.Errorf("sessions tagged %q during the export = %d, want exactly 1", tag, n)
	}
	if err := run.finish(t); err != nil {
		t.Fatalf("the held export failed: %v", err)
	}
	settles("after a finished export")

	errOpen := errors.New("bucket gone")
	rig.objs.setFail(fx.docs[0].key, errOpen)
	if err := s.AssembleWorkspace(ctx, &wsSink{}, noStart); !errors.Is(err, errOpen) {
		t.Fatalf("failing export error = %v, want the open failure", err)
	}
	settles("after a failed export")

	rig.objs.setFail(fx.docs[0].key, nil)
	if err := s.AssembleWorkspace(ctx, &wsSink{}, noStart); err != nil {
		t.Errorf("next export of the workspace: %v", err)
	}
}

// --- Core AC 1: shapes of workspace ---------------------------------------------------

func TestRLS_AnEmptyWorkspaceExportsTheLogAndTheManifest(t *testing.T) {
	rig := newWsRig(t)
	fx := rig.plant(t, "empty", shapeEmpty)

	names, body := zipEntries(t, rig.exportOK(t, fx))

	if !slices.Equal(names, []string{"audit_log.csv", "manifest.json"}) {
		t.Fatalf("entries = %q, want audit_log.csv then manifest.json", names)
	}
	if rows := parseCSV(t, body["audit_log.csv"]); len(rows) < 1 {
		t.Error("audit_log.csv has no header row")
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(body["manifest.json"], &raw); err != nil {
		t.Fatalf("manifest.json: %v", err)
	}
	if string(raw["companies"]) != "[]" {
		t.Errorf("companies = %s, want []", raw["companies"])
	}
	m := parseWsManifest(t, body)
	if m.Counts.Companies != 0 || m.Counts.Invoices != 0 || m.Counts.Documents != 0 {
		t.Errorf("counts = %+v, want companies, invoices and documents all 0", m.Counts)
	}
}

func TestRLS_WorkspaceExportHoldsOnlyTheCallersTenant(t *testing.T) {
	rig := newWsRig(t)
	a := rig.plant(t, "iso-a", shapeFull)
	b := rig.plant(t, "iso-b", shapeFull)

	names, body := zipEntries(t, rig.exportOK(t, a))

	var all []byte
	for _, n := range names {
		all = append(all, body[n]...)
		for _, d := range b.docs {
			if strings.Contains(n, d.hash) {
				t.Errorf("entry %s carries B's document hash", n)
			}
		}
	}
	if !bytes.Contains(all, []byte(a.label)) {
		t.Fatalf("control: the export holds none of A's own marker %q", a.label)
	}
	if bytes.Contains(all, []byte(b.label)) {
		t.Errorf("the export holds B's marker %q", b.label)
	}
	for _, d := range b.docs {
		if bytes.Contains(all, d.body) {
			t.Errorf("the export holds B's document bytes %q", d.body)
		}
	}
}

// --- Constraints: one snapshot, REPEATABLE READ read-write -------------------------------

func TestRLS_WorkspaceExportReadsOneSnapshot(t *testing.T) {
	rig := newWsRig(t)
	fx := rig.plant(t, "snapshot", shapeFull)
	late := "INV-LATE-" + fx.label
	committed := false
	sink := &wsSink{hook: func(n int, _ []byte) error {
		if n != 1 {
			return nil
		}
		ctx := context.Background()
		tx, err := rig.super.Begin(ctx)
		if err != nil {
			t.Errorf("concurrent commit: begin: %v", err)
			return nil
		}
		mustCreateInvoice(t, tx, invoiceFixture{tenantID: fx.tenant, entityID: fx.alpha, invoiceNumber: late})
		if _, err := tx.Exec(ctx, `INSERT INTO audit_log (tenant_id, actor, event, payload) VALUES ($1, 'system', 'snapshot.late', '{}')`, fx.tenant); err != nil {
			t.Errorf("concurrent commit: audit row: %v", err)
		}
		if err := tx.Commit(ctx); err != nil {
			t.Errorf("concurrent commit: commit: %v", err)
			return nil
		}
		committed = true
		return nil
	}}

	if err := rig.store().AssembleWorkspace(idCtx(fx.admin, fx.tenant), sink, noStart); err != nil {
		t.Fatalf("AssembleWorkspace: %v", err)
	}
	if !committed {
		t.Fatal("the concurrent commit never ran, so the assertions below prove nothing")
	}
	_, body := zipEntries(t, sink.data())
	if got := body["companies/alpha-co/invoices.csv"]; !strings.Contains(string(got), fx.invoiceNumbers[0]) || strings.Contains(string(got), late) {
		t.Errorf("invoices.csv holds the original invoice = %v and the late one = %v, want true and false", strings.Contains(string(got), fx.invoiceNumbers[0]), strings.Contains(string(got), late))
	}
	if got := body["audit_log.csv"]; !strings.Contains(string(got), "workspace.provisioned") || strings.Contains(string(got), "snapshot.late") {
		t.Errorf("audit_log.csv holds a seeded row = %v and the late row = %v, want true and false", strings.Contains(string(got), "workspace.provisioned"), strings.Contains(string(got), "snapshot.late"))
	}
}

func TestRLS_WorkspaceExportBeginsRepeatableReadReadWrite(t *testing.T) {
	rig := newWsRig(t)
	fx := rig.plant(t, "isolation", shapeOneDoc)

	rig.rec.reset()
	rig.exportOK(t, fx)

	begins := rig.rec.mentioning("begin")
	if len(begins) != 1 {
		t.Fatalf("export issued %d BEGIN statements %q, want 1", len(begins), begins)
	}
	if !strings.Contains(begins[0], "repeatable read") || strings.Contains(begins[0], "read only") {
		t.Errorf("BEGIN = %q, want REPEATABLE READ and not READ ONLY", begins[0])
	}
}

// --- Core AC 3, 4: the preflight ---------------------------------------------------------

func TestRLS_PreflightTellsWhetherTheExportCanStart(t *testing.T) {
	rig := newWsRig(t)
	a := rig.plant(t, "pre-a", shapeFull)
	b := rig.plant(t, "pre-b", shapeOneDoc)
	locked := rig.plant(t, "pre-locked", shapeOneDoc)
	rig.lock(t, locked)

	free := rig.store()
	auditBefore := rig.auditTotal(t, a.tenant) + rig.auditTotal(t, b.tenant) + rig.auditTotal(t, locked.tenant)
	for _, tc := range []struct {
		name string
		ctx  context.Context
		want error
	}{
		{"free admin", idCtx(a.admin, a.tenant), nil},
		{"admin of a locked workspace", idCtx(locked.admin, locked.tenant), nil},
		{"preparer", idCtx(a.preparer, a.tenant), ErrExportNotPermitted},
		{"reviewer", idCtx(a.reviewer, a.tenant), ErrExportNotPermitted},
		{"admin of another workspace", idCtx(b.admin, a.tenant), db.ErrNotActiveMember},
		{"suspended admin", idCtx(a.suspended, a.tenant), db.ErrNotActiveMember},
		{"subject with no row", idCtx(uuid.NewString(), a.tenant), db.ErrNotActiveMember},
		{"no identity", context.Background(), db.ErrNoTenant},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := free.PreflightWorkspace(tc.ctx)
			if tc.want == nil && err != nil || tc.want != nil && !errors.Is(err, tc.want) {
				t.Errorf("PreflightWorkspace error = %v, want %v", err, tc.want)
			}
		})
	}
	if len(free.slots) != 0 {
		t.Errorf("slots held after the preflights = %d, want 0", len(free.slots))
	}
	if after := rig.auditTotal(t, a.tenant) + rig.auditTotal(t, b.tenant) + rig.auditTotal(t, locked.tenant); after != auditBefore {
		t.Errorf("preflights moved the audit rows from %d to %d, want no change", auditBefore, after)
	}

	rig.rec.reset()
	if err := free.PreflightWorkspace(idCtx(a.admin, a.tenant)); err != nil {
		t.Fatalf("PreflightWorkspace: %v", err)
	}
	if begins := rig.rec.mentioning("begin"); len(begins) != 1 || !strings.Contains(begins[0], "read only") {
		t.Errorf("preflight BEGIN statements = %q, want exactly one READ ONLY", begins)
	}

	s := rig.store()
	run := startBlockedExport(t, s, idCtx(a.admin, a.tenant))
	if err := s.PreflightWorkspace(idCtx(a.admin, a.tenant)); !errors.Is(err, ErrExportRunning) {
		t.Errorf("A's preflight while A exports = %v, want ErrExportRunning", err)
	}
	if err := s.PreflightWorkspace(idCtx(b.admin, b.tenant)); err != nil {
		t.Errorf("B's preflight while A exports = %v, want nil", err)
	}
	if len(s.slots) != 1 {
		t.Errorf("slots held on the busy store = %d, want 1 (the export's; the preflight neither takes nor frees one)", len(s.slots))
	}
	if err := run.finish(t); err != nil {
		t.Errorf("the held export failed: %v", err)
	}
}

// --- adversarial: paging, directory names ------------------------------------------------

// plantDocuments adds n documents sharing one created_at, so only the id breaks ties across pages.
func (r *wsRig) plantDocuments(t *testing.T, fx wsFx, n int) {
	t.Helper()
	ctx := context.Background()
	tx, err := r.super.Begin(ctx)
	if err != nil {
		t.Fatalf("begin documents fixture: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	at := time.Now().UTC().Add(-time.Hour)
	for i := range n {
		body := []byte(fmt.Sprintf("paged %s %d", fx.label, i))
		sum := sha256.Sum256(body)
		hash := hex.EncodeToString(sum[:])
		key := "tenants/" + fx.tenant + "/" + hash
		if _, err := tx.Exec(ctx,
			`INSERT INTO documents (tenant_id, storage_key, content_hash, size_bytes, created_at) VALUES ($1, $2, $3, $4, $5)`,
			fx.tenant, key, hash, len(body), at); err != nil {
			t.Fatalf("insert paged document %d: %v", i, err)
		}
		r.objs.put(key, body)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit documents fixture: %v", err)
	}
}

func TestRLS_WorkspaceExportPagesEveryDocumentOnceInIDOrder(t *testing.T) {
	rig := newWsRig(t)
	fx := rig.plant(t, "paging", shapeEmpty)
	const n = 2*documentPageSize + 1
	rig.plantDocuments(t, fx, n)

	names, body := zipEntries(t, rig.exportOK(t, fx))

	rows, err := rig.super.Query(context.Background(), `SELECT content_hash FROM documents WHERE tenant_id = $1 ORDER BY id`, fx.tenant)
	if err != nil {
		t.Fatalf("read document order: %v", err)
	}
	want := []string{}
	for rows.Next() {
		var h string
		if err := rows.Scan(&h); err != nil {
			t.Fatalf("scan: %v", err)
		}
		want = append(want, "documents/"+h)
	}
	rows.Close()
	if len(want) != n {
		t.Fatalf("fixture holds %d documents, want %d", len(want), n)
	}
	got := names[:len(names)-2]
	if !slices.Equal(got, want) {
		t.Errorf("document entries: %d names (first mismatch aside), want the %d documents once each in id order", len(got), n)
	}
	if names[len(names)-2] != "audit_log.csv" || names[len(names)-1] != "manifest.json" {
		t.Errorf("the ZIP ends with %q, want audit_log.csv then manifest.json", names[len(names)-2:])
	}
	if m := parseWsManifest(t, body); m.Counts.Documents != n {
		t.Errorf("counts.documents = %d, want %d", m.Counts.Documents, n)
	}
}

func TestRLS_WorkspaceExportGivesAlikeSlugsDistinctDirectories(t *testing.T) {
	rig := newWsRig(t)
	fx := rig.plant(t, "slugs", shapeEmpty)
	var ids [4]string
	for i, name := range []string{"Okafor & Co", "Okafor  Co", "../../escape", "!!!"} {
		ids[i] = uuid.NewString()
		if _, err := rig.super.Exec(context.Background(),
			`INSERT INTO business_entities (id, tenant_id, name) VALUES ($1, $2, $3)`, ids[i], fx.tenant, name); err != nil {
			t.Fatalf("insert company %q: %v", name, err)
		}
	}

	names, body := zipEntries(t, rig.exportOK(t, fx))

	m := parseWsManifest(t, body)
	dirOf := map[string]string{}
	for _, c := range m.Companies {
		dirOf[c.ID] = c.Dir
	}
	// ORDER BY name is collation-dependent: either alike name may take the plain slug.
	okay := dirOf[ids[0]] == "okafor-co" && dirOf[ids[1]] == "okafor-co-"+ids[1] ||
		dirOf[ids[1]] == "okafor-co" && dirOf[ids[0]] == "okafor-co-"+ids[0]
	if len(m.Companies) != 4 || !okay {
		t.Errorf("directories = %v, want okafor-co for one alike name and okafor-co-<its id> for the other", dirOf)
	}
	if dirOf[ids[2]] != "escape" || dirOf[ids[3]] != ids[3] {
		t.Errorf("directories for ../../escape and !!! = %q and %q, want escape and the id %q", dirOf[ids[2]], dirOf[ids[3]], ids[3])
	}
	seen := map[string]bool{}
	for _, n := range names {
		if seen[n] {
			t.Errorf("entry %q appears twice", n)
		}
		seen[n] = true
		if strings.Contains(n, "..") {
			t.Errorf("entry %q escapes its directory", n)
		}
	}
	for _, c := range m.Companies {
		if !seen["companies/"+c.Dir+"/invoices.csv"] {
			t.Errorf("no companies/%s/invoices.csv in %q", c.Dir, names)
		}
	}
}

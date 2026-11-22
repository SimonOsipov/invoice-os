package erp

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"os"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"

	"github.com/SimonOsipov/invoice-os/internal/importer"
	"github.com/SimonOsipov/invoice-os/internal/invoice"
)

// The seeded fake-co-1-inv-1 maps to number FK1-0001, one line of 1 x 100000.00 at 7.5%:
// subtotal 100000.00, vat 7500.00, total 107500.00.
const (
	docOne  = "fake-co-1-inv-1"
	numOne  = "FK1-0001"
	keyOne  = "fake-co-1"
	custOne = "fake-co-1-cust-1"
)

var seedLine = fakeLine{"Consulting services", "1", "100000.00", "7.5"}

// stubValidator stands in for the validation gate: it stamps the invoices it is handed, as the
// gate's apply step does, so a test can tell a validated invoice from an unvalidated one.
type stubValidator struct {
	super *pgxpool.Pool

	mu         sync.Mutex
	calls      [][]string
	errs       []error // one per call, consumed in order; nil entries succeed
	failAlways error
	blocking   bool // stamp a blocking violation, so the invoice stays a draft
}

func (v *stubValidator) callCount() int {
	v.mu.Lock()
	defer v.mu.Unlock()
	return len(v.calls)
}

func (v *stubValidator) called() []string {
	v.mu.Lock()
	defer v.mu.Unlock()
	var out []string
	for _, c := range v.calls {
		out = append(out, c...)
	}
	return out
}

func (v *stubValidator) ValidateBatch(ctx context.Context, invs []invoice.Invoice) (invoice.BatchOutcome, error) {
	v.mu.Lock()
	ids := make([]string, len(invs))
	for i, inv := range invs {
		ids[i] = inv.ID
	}
	v.calls = append(v.calls, ids)
	var err error
	if len(v.errs) > 0 {
		err, v.errs = v.errs[0], v.errs[1:]
	} else if v.failAlways != nil {
		err = v.failAlways
	}
	blocking := v.blocking
	v.mu.Unlock()
	if err != nil {
		return invoice.BatchOutcome{}, err
	}

	var ruleSet string
	var version int
	if err := v.super.QueryRow(ctx, "SELECT id, version FROM rule_set_versions ORDER BY version DESC LIMIT 1").Scan(&ruleSet, &version); err != nil {
		return invoice.BatchOutcome{}, err
	}
	out := invoice.BatchOutcome{RuleSetVersion: version, ByID: map[string][]invoice.Violation{}}
	for _, inv := range invs {
		vs := []invoice.Violation{}
		status := "validated"
		if blocking {
			vs = append(vs, invoice.Violation{RuleKey: "stub-blocking", Severity: "error", Message: "stub"})
			status = "draft"
			out.WithViolations++
		} else {
			out.Clean++
		}
		raw, _ := json.Marshal(vs)
		if _, err := v.super.Exec(ctx,
			"UPDATE invoices SET rule_set_version_id = $2, violations = $3, status = $4 WHERE id = $1 AND status = 'draft'",
			inv.ID, ruleSet, raw, status); err != nil {
			return invoice.BatchOutcome{}, err
		}
		out.ByID[inv.ID] = vs
	}
	return out, nil
}

// createFn adapts a function to importer.DraftCreator.
type createFn func(context.Context, invoice.CreateInput) (invoice.Invoice, error)

func (f createFn) Create(ctx context.Context, in invoice.CreateInput) (invoice.Invoice, error) {
	return f(ctx, in)
}

// ingestEnv is one tenant with one client and one active fake connection.
type ingestEnv struct {
	t            *testing.T
	super, app   *pgxpool.Pool
	tenant       string
	entity, conn string
	key          string
	script       scriptConnector
	reg          Registry
	val          *stubValidator
	worker       *IngestWorker
	fetches      atomic.Int64
	onFetch      func(ctx context.Context, ref DocRef) (Document, error) // nil: the fake
	jobSeq       atomic.Int64
}

func newIngest(t *testing.T) *ingestEnv {
	t.Helper()
	super, app := storePools(t)
	return newIngestOn(t, super, app, keyOne)
}

// newIngestOn seeds a new tenant on the given pools.
func newIngestOn(t *testing.T, super, app *pgxpool.Pool, key string) *ingestEnv {
	t.Helper()
	e := &ingestEnv{t: t, super: super, app: app, key: key}
	e.tenant = seedTenant(t, super, "firm")
	e.entity = seedClient(t, super, e.tenant, "ingest client X")
	e.conn, _ = seedActiveConn(t, super, e.tenant, e.entity, key)

	base := testFake(t)
	e.script = scriptConnector{
		Connector: base,
		fetch: func(ctx context.Context, c Credentials, ref DocRef) (Document, error) {
			e.fetches.Add(1)
			if e.onFetch != nil {
				return e.onFetch(ctx, ref)
			}
			return base.Fetch(ctx, c, ref)
		},
	}
	reg, err := NewRegistry(e.script)
	if err != nil {
		t.Fatalf("registry: %v", err)
	}
	e.reg = reg
	e.val = &stubValidator{super: super}
	e.worker = e.workerWith(invoice.NewStore(app))
	return e
}

func (e *ingestEnv) workerWith(c importer.DraftCreator) *IngestWorker {
	return &IngestWorker{
		Pool: e.app, Registry: e.reg, Invoices: c, Gate: e.val,
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
}

// addClient seeds a second client of the same tenant.
func (e *ingestEnv) addClient(name string) string { return seedClient(e.t, e.super, e.tenant, name) }

// addConnection seeds another active fake connection of the tenant on entity.
func (e *ingestEnv) addConnection(entity, key string) string {
	id, _ := seedActiveConn(e.t, e.super, e.tenant, entity, key)
	return id
}

func (e *ingestEnv) args(docID, rev string, deleted bool) IngestArgs {
	return IngestArgs{TenantID: e.tenant, ConnectionID: e.conn, SourceDocID: docID, SourceRevision: rev, Deleted: deleted}
}

func (e *ingestEnv) workAs(ctx context.Context, w *IngestWorker, a IngestArgs, attempt int) error {
	e.t.Helper()
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	job := &river.Job[IngestArgs]{
		JobRow: &rivertype.JobRow{ID: e.jobSeq.Add(1), Attempt: attempt, MaxAttempts: 8},
		Args:   a,
	}
	return w.Work(ctx, job)
}

func (e *ingestEnv) work(docID, rev string) error {
	e.t.Helper()
	return e.workAs(context.Background(), e.worker, e.args(docID, rev, false), 1)
}

func (e *ingestEnv) mustWork(docID, rev string) {
	e.t.Helper()
	if err := e.work(docID, rev); err != nil {
		e.t.Fatalf("Work(%s, rev %s): %v", docID, rev, err)
	}
}

func (e *ingestEnv) mustWorkDeleted(docID, rev string) {
	e.t.Helper()
	if err := e.workAs(context.Background(), e.worker, e.args(docID, rev, true), 1); err != nil {
		e.t.Fatalf("Work(%s, rev %s, deleted): %v", docID, rev, err)
	}
}

// snoozeOf reports the delay when err is a River snooze.
func snoozeOf(err error) (time.Duration, bool) {
	var s *rivertype.JobSnoozeError
	if errors.As(err, &s) {
		return s.Duration, true
	}
	return 0, false
}

// --- documents -----------------------------------------------------------------------

func fakeDocument(docID, number, customerID string, lines ...fakeLine) Document {
	body, err := json.Marshal(map[string]any{
		"number": number, "issue_date": "2026-01-05", "currency": "NGN", "customer_id": customerID, "lines": lines,
	})
	if err != nil {
		panic(err)
	}
	return Document{Ref: DocRef{SourceDocID: docID, SourceRevision: fakeRevision}, ContentType: jsonType, Body: body}
}

// serve makes Fetch answer doc for every document.
func (e *ingestEnv) serve(doc Document) {
	e.onFetch = func(context.Context, DocRef) (Document, error) { return doc, nil }
}

// mappedFrom is what the fixed mapping makes of doc, through the fake's own party.
func (e *ingestEnv) mappedFrom(doc Document) invoice.CreateInput {
	e.t.Helper()
	var body struct {
		CustomerID string `json:"customer_id"`
	}
	if err := json.Unmarshal(doc.Body, &body); err != nil {
		e.t.Fatalf("decode body: %v", err)
	}
	var party *Party
	if body.CustomerID != "" {
		p, err := testFake(e.t).FetchParty(context.Background(), Credentials{}, e.key, body.CustomerID)
		if err == nil {
			party = &p
		}
	}
	in, err := mapFake(doc, party)
	if err != nil {
		e.t.Fatalf("map: %v", err)
	}
	return in
}

// mapped is the mapped content of a seeded document as the fake serves it.
func (e *ingestEnv) mapped(docID string) invoice.CreateInput {
	e.t.Helper()
	doc, err := testFake(e.t).Fetch(context.Background(), Credentials{}, DocRef{SourceDocID: docID, SourceRevision: fakeRevision, Scope: e.key})
	if err != nil {
		e.t.Fatalf("fetch %s: %v", docID, err)
	}
	return e.mappedFrom(doc)
}

// --- rows ----------------------------------------------------------------------------

type docRow struct {
	ID, State, Revision, Entity string
	Failure, InvoiceID, Hash    *string
	Claimed                     *string
	HashVersion                 *int16
	Differs                     bool
	Changed, Deleted            *time.Time
	UpdatedAt                   time.Time
}

const docCols = "id::text, state, source_revision, entity_id::text, failure, invoice_id::text, content_hash, claimed_number, hash_version, differs_from_erp, changed_after_clearance_at, deleted_in_erp_at, updated_at"

func (e *ingestEnv) docOn(conn, docID string) (docRow, bool) {
	e.t.Helper()
	var d docRow
	err := e.super.QueryRow(context.Background(),
		"SELECT "+docCols+" FROM erp_source_documents WHERE connection_id = $1 AND source_doc_id = $2", conn, docID,
	).Scan(&d.ID, &d.State, &d.Revision, &d.Entity, &d.Failure, &d.InvoiceID, &d.Hash, &d.Claimed, &d.HashVersion, &d.Differs, &d.Changed, &d.Deleted, &d.UpdatedAt)
	if err != nil {
		if strings.Contains(err.Error(), "no rows") {
			return docRow{}, false
		}
		e.t.Fatalf("read document %s: %v", docID, err)
	}
	return d, true
}

func (e *ingestEnv) doc(docID string) docRow {
	e.t.Helper()
	d, ok := e.docOn(e.conn, docID)
	if !ok {
		e.t.Fatalf("no erp_source_documents row for %s", docID)
	}
	return d
}

func (e *ingestEnv) noDoc(docID string) {
	e.t.Helper()
	if d, ok := e.docOn(e.conn, docID); ok {
		e.t.Errorf("document %s has a row in state %q, want none", docID, d.State)
	}
}

// docJSON is the whole row, for "unchanged" checks.
func (e *ingestEnv) docJSON(docID string) string {
	e.t.Helper()
	var s string
	mustScan(e.t, e.super,
		"SELECT coalesce((SELECT to_jsonb(d)::text FROM erp_source_documents d WHERE connection_id = $1 AND source_doc_id = $2), 'none')",
		[]any{e.conn, docID}, &s)
	return s
}

func str0(p *string) string {
	if p == nil {
		return "<NULL>"
	}
	return *p
}

func (e *ingestEnv) wantState(docID, state string) docRow {
	e.t.Helper()
	d := e.doc(docID)
	if d.State != state {
		e.t.Fatalf("document %s state = %q (failure %s), want %q", docID, d.State, str0(d.Failure), state)
	}
	return d
}

func (e *ingestEnv) invoices() int {
	return countOf(e.t, e.super, "SELECT count(*) FROM invoices WHERE tenant_id = $1", e.tenant)
}

func (e *ingestEnv) invoicesIn(entity string) int {
	return countOf(e.t, e.super, "SELECT count(*) FROM invoices WHERE tenant_id = $1 AND entity_id = $2", e.tenant, entity)
}

func (e *ingestEnv) invoiceByNumber(entity, number string) (id, status string, conn *string) {
	e.t.Helper()
	mustScan(e.t, e.super,
		"SELECT id::text, status, erp_connection_id::text FROM invoices WHERE tenant_id = $1 AND entity_id = $2 AND invoice_number = $3",
		[]any{e.tenant, entity, number}, &id, &status, &conn)
	return id, status, conn
}

func (e *ingestEnv) invoiceStatus(id string) string {
	var s string
	mustScan(e.t, e.super, "SELECT status FROM invoices WHERE id = $1", []any{id}, &s)
	return s
}

func (e *ingestEnv) ruleSetOf(id string) *string {
	var s *string
	mustScan(e.t, e.super, "SELECT rule_set_version_id::text FROM invoices WHERE id = $1", []any{id}, &s)
	return s
}

// snapshot is the invoice row, its lines and history, and every submission job of the tenant.
func (e *ingestEnv) snapshot(invoiceID string) string {
	e.t.Helper()
	var s string
	mustScan(e.t, e.super, `SELECT jsonb_build_object(
		'invoice', (SELECT to_jsonb(i) FROM invoices i WHERE i.id = $1),
		'lines', (SELECT coalesce(jsonb_agg(to_jsonb(l) ORDER BY l.line_no), '[]') FROM line_items l WHERE l.invoice_id = $1),
		'history', (SELECT coalesce(jsonb_agg(to_jsonb(h) ORDER BY h.changed_at, h.id), '[]') FROM invoice_status_history h WHERE h.invoice_id = $1),
		'jobs', (SELECT coalesce(jsonb_agg(to_jsonb(s) ORDER BY s.id), '[]') FROM submission_jobs s WHERE s.tenant_id = $2))::text`,
		[]any{invoiceID, e.tenant}, &s)
	if !strings.Contains(s, `"invoice_number"`) {
		e.t.Fatalf("snapshot of %s has no invoice: %s", invoiceID, s)
	}
	return s
}

// withoutValidation is the invoice row minus what the validation gate writes.
func (e *ingestEnv) withoutValidation(invoiceID string) string {
	var s string
	mustScan(e.t, e.super,
		"SELECT (to_jsonb(i) - 'status' - 'violations' - 'rule_set_version_id' - 'verdict_fingerprint')::text FROM invoices i WHERE id = $1",
		[]any{invoiceID}, &s)
	return s
}

type ingestAuditRow struct {
	Actor   string
	Entity  *string
	Payload map[string]any
}

func (e *ingestEnv) audits(event string) []ingestAuditRow {
	e.t.Helper()
	rows, err := e.super.Query(context.Background(),
		"SELECT actor, entity_id::text, payload FROM audit_log WHERE tenant_id = $1 AND event = $2 ORDER BY id", e.tenant, event)
	if err != nil {
		e.t.Fatalf("read audit_log: %v", err)
	}
	defer rows.Close()
	var out []ingestAuditRow
	for rows.Next() {
		var r ingestAuditRow
		var raw []byte
		if err := rows.Scan(&r.Actor, &r.Entity, &raw); err != nil {
			e.t.Fatalf("scan audit_log: %v", err)
		}
		if err := json.Unmarshal(raw, &r.Payload); err != nil {
			e.t.Fatalf("decode payload: %v", err)
		}
		out = append(out, r)
	}
	return out
}

func (e *ingestEnv) erpAudits() int {
	return countOf(e.t, e.super, "SELECT count(*) FROM audit_log WHERE tenant_id = $1 AND event LIKE 'erp.document.%'", e.tenant)
}

var (
	pulledKeys  = []string{"connection_id", "invoice_id", "invoice_number", "source_doc_id", "source_revision", "system"}
	dupKeys     = append(slices.Clone(pulledKeys), "differs", "linked")
	changedKeys = append(slices.Clone(pulledKeys), "deleted")
)

func init() {
	slices.Sort(dupKeys)
	slices.Sort(changedKeys)
}

// wantAudit asserts exactly one row of event, as actor system, workspace-level, with exactly keys.
func (e *ingestEnv) wantAudit(event string, keys []string) ingestAuditRow {
	e.t.Helper()
	rows := e.audits(event)
	if len(rows) != 1 {
		e.t.Fatalf("%s rows = %d, want 1", event, len(rows))
	}
	r := rows[0]
	if r.Actor != "system" {
		e.t.Errorf("%s actor = %q, want system", event, r.Actor)
	}
	if r.Entity != nil {
		e.t.Errorf("%s entity_id = %s, want NULL (workspace-level)", event, *r.Entity)
	}
	if got := keysOf(r.Payload); !slices.Equal(got, keys) {
		e.t.Errorf("%s payload keys = %v, want %v", event, got, keys)
	}
	if r.Payload["system"] != fakeSystem || r.Payload["connection_id"] != e.conn {
		e.t.Errorf("%s payload system/connection_id = %v/%v, want fake/%s", event, r.Payload["system"], r.Payload["connection_id"], e.conn)
	}
	return r
}

// --- planting (super) ------------------------------------------------------------------

type plantInv struct {
	number          string
	entity          string // default: the env's client
	conn            *string
	status          string // default draft
	subtotal, vat   string // default: the seeded document's
	total, lineDesc string
}

func (e *ingestEnv) plantInvoice(p plantInv) string {
	e.t.Helper()
	if p.entity == "" {
		p.entity = e.entity
	}
	if p.status == "" {
		p.status = "draft"
	}
	if p.subtotal == "" {
		p.subtotal, p.vat, p.total = "100000.00", "7500.00", "107500.00"
	}
	if p.lineDesc == "" {
		p.lineDesc = seedLine.Description
	}
	var id string
	mustScan(e.t, e.super,
		`INSERT INTO invoices (tenant_id, entity_id, invoice_number, status, subtotal, vat, total, erp_connection_id)
		 VALUES ($1, $2, $3, $4, $5::numeric, $6::numeric, $7::numeric, $8) RETURNING id::text`,
		[]any{e.tenant, p.entity, p.number, p.status, p.subtotal, p.vat, p.total, p.conn}, &id)
	mustExec(e.t, e.super,
		`INSERT INTO line_items (tenant_id, invoice_id, line_no, description, quantity, unit_price, line_total, line_tax, tax_percent)
		 VALUES ($1, $2, 1, $3, 1, 100000.00, $4::numeric, $5::numeric, 7.5)`,
		e.tenant, id, p.lineDesc, p.subtotal, p.vat)
	return id
}

func (e *ingestEnv) accept(invoiceID string) {
	e.t.Helper()
	mustExec(e.t, e.super, "UPDATE invoices SET status = 'accepted' WHERE id = $1", invoiceID)
	mustExec(e.t, e.super,
		`INSERT INTO submission_jobs (tenant_id, invoice_id, idempotency_key, adapter, adapter_version, state)
		 VALUES ($1, $2, $3, 'fake', '1', 'accepted')`, e.tenant, invoiceID, uuid.NewString())
}

type plantDoc struct {
	conn, docID, rev, state string // conn default: the env's; rev default "1"; state default pending
	invoiceID, failure      *string
	claimed                 *string
	hash                    *string
	hashVersion             *int
	differs                 bool
	changed, deleted        *time.Time
	updatedAgo              time.Duration
}

func (e *ingestEnv) plantDocument(p plantDoc) string {
	e.t.Helper()
	if p.conn == "" {
		p.conn = e.conn
	}
	if p.rev == "" {
		p.rev = "1"
	}
	if p.state == "" {
		p.state = "pending"
	}
	var id string
	mustScan(e.t, e.super,
		`INSERT INTO erp_source_documents (tenant_id, entity_id, connection_id, source_doc_id, source_revision, state, invoice_id, failure,
		   claimed_number, content_hash, hash_version, differs_from_erp, changed_after_clearance_at, deleted_in_erp_at, updated_at)
		 VALUES ($1, (SELECT entity_id FROM erp_connections WHERE id = $2), $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, now() - $14::bigint * interval '1 second')
		 RETURNING id::text`,
		[]any{e.tenant, p.conn, p.docID, p.rev, p.state, p.invoiceID, p.failure, p.claimed, p.hash, p.hashVersion, p.differs, p.changed, p.deleted, int64(p.updatedAgo.Seconds())}, &id)
	return id
}

func tp(t time.Time) *time.Time             { return &t }
func ip(i int) *int                         { return &i }
func contentHashPtr(in invoice.CreateInput) *string { h := ContentHash(in); return &h }

// takenDoc plants a drafted or linked row on invoiceID carrying the hash of what the fake serves.
func (e *ingestEnv) takenDoc(docID, state, invoiceID string) {
	e.t.Helper()
	e.plantDocument(plantDoc{docID: docID, state: state, invoiceID: &invoiceID, hash: contentHashPtr(e.mapped(docID)), hashVersion: ip(HashVersion)})
}

func poolWithOneConn(t *testing.T) *pgxpool.Pool {
	t.Helper()
	cfg, err := pgxpool.ParseConfig(os.Getenv("DATABASE_URL"))
	if err != nil {
		t.Fatalf("parse DATABASE_URL: %v", err)
	}
	cfg.MaxConns = 1
	p, err := pgxpool.NewWithConfig(context.Background(), cfg)
	if err != nil {
		t.Fatalf("one-connection pool: %v", err)
	}
	t.Cleanup(p.Close)
	return p
}

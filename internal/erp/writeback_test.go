package erp

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"
)

const (
	wbIRN = "IRN-123-FBMOCK01-20260105"
	wbCo2 = "fake-co-2"
)

// wbEnv is one tenant with a client, an active fake connection on fake-co-1 and a recording connector.
type wbEnv struct {
	t           *testing.T
	super, app  *pgxpool.Pool
	tenant      string
	entity      string
	conn        string
	fake        *Fake
	worker      *WriteBackWorker
	sc          *scriptConnector
	mu          sync.Mutex
	writes      []DocRef
	sent        []WriteBack
	fetches     atomic.Int64
	invoiceID   string
	jobSeq      atomic.Int64
	writeScript func(call int, ref DocRef) (DocRef, error)
	fetchRev    func(call int) string
}

func newWB(t *testing.T, caps *Capabilities) *wbEnv {
	t.Helper()
	super, app := storePools(t)
	e := &wbEnv{t: t, super: super, app: app, fake: testFake(t)}
	e.tenant = seedTenant(t, super, "firm")
	e.entity = seedClient(t, super, e.tenant, "wb client")
	e.conn, _ = seedActiveConn(t, super, e.tenant, e.entity, keyOne)
	e.sc = &scriptConnector{
		Connector: e.fake,
		caps:      caps,
		fetch: func(ctx context.Context, c Credentials, ref DocRef) (Document, error) {
			n := int(e.fetches.Add(1))
			d, err := e.fake.Fetch(ctx, c, ref)
			if err == nil && e.fetchRev != nil {
				d.Ref.SourceRevision = e.fetchRev(n)
			}
			return d, err
		},
	}
	e.sc.writeBack = func(ctx context.Context, c Credentials, ref DocRef, wb WriteBack) (DocRef, error) {
		e.mu.Lock()
		e.writes, e.sent = append(e.writes, ref), append(e.sent, wb)
		n := len(e.writes)
		e.mu.Unlock()
		if e.writeScript != nil {
			return e.writeScript(n, ref)
		}
		return e.fake.WriteBack(ctx, c, ref, wb)
	}
	reg, err := NewRegistry(e.sc)
	if err != nil {
		t.Fatal(err)
	}
	e.worker = &WriteBackWorker{Pool: app, Registry: reg, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	e.invoiceID = e.invoice("WB-1", "accepted", wbIRN)
	return e
}

func (e *wbEnv) invoice(number, status, irn string) string {
	e.t.Helper()
	var id string
	mustScan(e.t, e.super,
		"INSERT INTO invoices (tenant_id, entity_id, invoice_number, status, irn) VALUES ($1, $2, $3, $4, nullif($5, '')) RETURNING id::text",
		[]any{e.tenant, e.entity, number, status, irn}, &id)
	e.history(id, status)
	return id
}

func (e *wbEnv) history(invoiceID, to string) {
	e.t.Helper()
	mustExec(e.t, e.super, "INSERT INTO invoice_status_history (tenant_id, invoice_id, to_status, actor) VALUES ($1, $2, $3, 'system')", e.tenant, invoiceID, to)
}

// doc plants an erp_source_documents row; invoice "" leaves it untaken.
func (e *wbEnv) doc(conn, docID, state, invoiceID string, deleted bool) string {
	e.t.Helper()
	var id string
	mustScan(e.t, e.super,
		`INSERT INTO erp_source_documents (tenant_id, entity_id, connection_id, source_doc_id, source_revision, state, invoice_id, failure, deleted_in_erp_at)
		 VALUES ($1, (SELECT entity_id FROM erp_connections WHERE id = $2), $2, $3, '1', $4, nullif($5, '')::uuid,
		         CASE WHEN $4 = 'failed' THEN 'exhausted' END, CASE WHEN $6 THEN now() END) RETURNING id::text`,
		[]any{e.tenant, conn, docID, state, invoiceID, deleted}, &id)
	return id
}

func (e *wbEnv) args(version int) WriteBackArgs {
	return WriteBackArgs{
		TenantID: e.tenant, InvoiceID: e.invoiceID, InvoiceNumber: "WB-1", ResultVersion: version,
		Outcome: "accepted", IRN: wbIRN,
	}
}

func (e *wbEnv) run(a WriteBackArgs, attempt int) error {
	e.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	job := &river.Job[WriteBackArgs]{JobRow: &rivertype.JobRow{ID: e.jobSeq.Add(1), Attempt: attempt, MaxAttempts: 8}, Args: a}
	return e.worker.Work(ctx, job)
}

type wbRow struct {
	State, Reason, Entity, Written, New string
	Attempt                             int
	Job                                 int64
}

func (e *wbEnv) rows() []wbRow {
	e.t.Helper()
	rs, err := e.super.Query(context.Background(),
		`SELECT state, coalesce(reason, ''), entity_id::text, coalesce(written_revision, ''), coalesce(new_revision, ''), attempt, river_job_id
		 FROM erp_writebacks WHERE tenant_id = $1 ORDER BY created_at, id`, e.tenant)
	if err != nil {
		e.t.Fatal(err)
	}
	defer rs.Close()
	var out []wbRow
	for rs.Next() {
		var r wbRow
		if err := rs.Scan(&r.State, &r.Reason, &r.Entity, &r.Written, &r.New, &r.Attempt, &r.Job); err != nil {
			e.t.Fatal(err)
		}
		out = append(out, r)
	}
	return out
}

func (e *wbEnv) audits(event string) []map[string]any {
	e.t.Helper()
	rs, err := e.super.Query(context.Background(),
		"SELECT actor, payload FROM audit_log WHERE tenant_id = $1 AND event = $2 ORDER BY created_at", e.tenant, event)
	if err != nil {
		e.t.Fatal(err)
	}
	defer rs.Close()
	var out []map[string]any
	for rs.Next() {
		var actor string
		var p map[string]any
		if err := rs.Scan(&actor, &p); err != nil {
			e.t.Fatal(err)
		}
		p["_actor"] = actor
		out = append(out, p)
	}
	return out
}

func (e *wbEnv) invoiceState() (status, irn string) {
	e.t.Helper()
	mustScan(e.t, e.super, "SELECT status, coalesce(irn, '') FROM invoices WHERE id = $1", []any{e.invoiceID}, &status, &irn)
	return
}

func (e *wbEnv) writeCount() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return len(e.writes)
}

func (e *wbEnv) drafted() string { return e.doc(e.conn, docOne, "drafted", e.invoiceID, false) }

func (e *wbEnv) mustRun(a WriteBackArgs, attempt int) {
	e.t.Helper()
	if err := e.run(a, attempt); err != nil {
		e.t.Fatalf("Work: %v", err)
	}
}

func (e *wbEnv) wantRow(i int, state, reason string) wbRow {
	e.t.Helper()
	rs := e.rows()
	if i >= len(rs) {
		e.t.Fatalf("row %d missing; rows = %+v", i, rs)
	}
	if rs[i].State != state || rs[i].Reason != reason {
		e.t.Errorf("row %d = %s/%s, want %s/%s", i, rs[i].State, rs[i].Reason, state, reason)
	}
	return rs[i]
}

var fakeCaps = &Capabilities{Note: true, CustomFieldSlots: 2}

func TestWriteBack_AcceptedWritesTheIRNAndAudits(t *testing.T) {
	e := newWB(t, nil)
	e.drafted()
	var ent string
	mustScan(t, e.super, "SELECT entity_id::text FROM erp_source_documents WHERE tenant_id = $1", []any{e.tenant}, &ent)
	e.mustRun(e.args(1), 3)

	wb, ok := e.fake.LastWriteBack(keyOne, docOne)
	if !ok || wb.IRN != wbIRN || len(wb.Fields) != 1 || wb.Fields[0] != "accepted" || wb.Note != "NRS accepted. IRN: "+wbIRN {
		t.Fatalf("LastWriteBack = %+v, %v", wb, ok)
	}
	if e.writes[0].SourceDocID != docOne || e.writes[0].Scope != keyOne {
		t.Errorf("write ref = %+v", e.writes[0])
	}
	r := e.wantRow(0, "succeeded", "")
	if r.Entity != ent || r.Attempt != 3 || r.Job != 1 || r.Written != "1" || r.New != "1" {
		t.Errorf("row = %+v, want entity %s attempt 3 job 1 revisions 1/1", r, ent)
	}
	au := e.audits("erp.writeback.succeeded")
	if len(au) != 1 {
		t.Fatalf("succeeded audit rows = %d, want 1", len(au))
	}
	p := au[0]
	if p["_actor"] != "system" || p["invoice_number"] != "WB-1" || p["irn"] != wbIRN || p["result_version"] != float64(1) ||
		p["invoice_id"] != e.invoiceID || p["outcome"] != "accepted" || p["connection_id"] != e.conn || p["source_doc_id"] != docOne || p["system"] != "fake" {
		t.Errorf("audit payload = %+v", p)
	}
}

func TestWriteBack_RejectedWritesTheReason(t *testing.T) {
	e := newWB(t, nil)
	e.drafted()
	a := e.args(1)
	a.Outcome, a.IRN, a.Reason = "rejected", "", "R1: bad"
	e.mustRun(a, 1)
	wb, _ := e.fake.LastWriteBack(keyOne, docOne)
	if wb.IRN != "" || len(wb.Fields) != 1 || wb.Fields[0] != "rejected" || wb.Note != "NRS rejected: R1: bad" {
		t.Errorf("write = %+v", wb)
	}
	au := e.audits("erp.writeback.succeeded")
	if len(au) != 1 {
		t.Fatalf("audit rows = %d", len(au))
	}
	if _, has := au[0]["irn"]; has {
		t.Error("a rejected write's audit carries an irn")
	}
}

func TestWriteBack_PlanFollowsCapabilities(t *testing.T) {
	for slots := 0; slots <= 3; slots++ {
		for _, note := range []bool{false, true} {
			for _, att := range []bool{false, true} {
				for _, outcome := range []string{"accepted", "failed"} {
					caps := Capabilities{Note: note, Attachment: att, CustomFieldSlots: slots}
					a := WriteBackArgs{Outcome: outcome, IRN: "IRN-9", Reason: "why"}
					if outcome == "failed" {
						a.IRN = ""
					}
					got, ok := wbPlan(caps, a)
					var want WriteBack
					if outcome == "accepted" && slots >= 1 {
						want.IRN = "IRN-9"
					}
					if slots >= 2 {
						want.Fields = []string{outcome}
					}
					if note {
						want.Note = "NRS accepted. IRN: IRN-9"
						if outcome == "failed" {
							want.Note = "NRS submission failed: why"
						}
					}
					empty := want.IRN == "" && len(want.Fields) == 0 && want.Note == ""
					if ok == empty {
						t.Errorf("%+v %s: ok = %v, want %v", caps, outcome, ok, !empty)
					}
					if got.IRN != want.IRN || got.Note != want.Note || strings.Join(got.Fields, ",") != strings.Join(want.Fields, ",") {
						t.Errorf("%+v %s: plan = %+v, want %+v", caps, outcome, got, want)
					}
					if got.Attachment != nil {
						t.Errorf("%+v: an attachment was planned", caps)
					}
					if slots > 0 && len(got.Fields) > slots-1 {
						t.Errorf("%+v: %d fields in %d slots with the IRN", caps, len(got.Fields), slots)
					}
				}
			}
		}
	}
}

func TestWriteBack_PlanReachesTheConnector(t *testing.T) {
	e := newWB(t, &Capabilities{CustomFieldSlots: 1, Attachment: true})
	e.drafted()
	e.mustRun(e.args(1), 1)
	if len(e.sent) != 1 || e.sent[0].IRN != wbIRN || len(e.sent[0].Fields) != 0 || e.sent[0].Note != "" || e.sent[0].Attachment != nil {
		t.Errorf("sent = %+v", e.sent)
	}
}

func TestWriteBack_NothingWritableMakesNoVendorCall(t *testing.T) {
	e := newWB(t, &Capabilities{})
	e.drafted()
	e.mustRun(e.args(1), 1)
	if e.writeCount() != 0 {
		t.Error("the connector was called")
	}
	e.wantRow(0, "skipped", "nothing_writable")
	if len(e.audits("erp.writeback.succeeded"))+len(e.audits("erp.writeback.failed")) != 0 {
		t.Error("a skip wrote an audit row")
	}
}

func TestWriteBack_RevisionFetchedFirst(t *testing.T) {
	e := newWB(t, &Capabilities{Note: true, CustomFieldSlots: 2, RequiresRevisionOnWrite: true})
	e.drafted()
	e.fetchRev = func(int) string { return "7" }
	e.writeScript = func(_ int, ref DocRef) (DocRef, error) { ref.SourceRevision = "8"; return ref, nil }
	e.mustRun(e.args(1), 1)
	if e.writes[0].SourceRevision != "7" {
		t.Errorf("write revision = %q, want the fetched 7", e.writes[0].SourceRevision)
	}
	r := e.wantRow(0, "succeeded", "")
	if r.Written != "7" || r.New != "8" {
		t.Errorf("row revisions = %q/%q, want 7/8", r.Written, r.New)
	}
}

func TestWriteBack_OneConflictRefetchesAndRetries(t *testing.T) {
	e := newWB(t, &Capabilities{Note: true, RequiresRevisionOnWrite: true})
	e.drafted()
	e.fetchRev = func(n int) string { return []string{"", "7", "9"}[n] }
	e.writeScript = func(n int, ref DocRef) (DocRef, error) {
		if n == 1 {
			return DocRef{}, ErrRevisionConflict
		}
		return ref, nil
	}
	e.mustRun(e.args(1), 1)
	if e.fetches.Load() != 2 || e.writeCount() != 2 || e.writes[1].SourceRevision != "9" {
		t.Errorf("fetches %d writes %+v", e.fetches.Load(), e.writes)
	}
	if rs := e.rows(); len(rs) != 1 || rs[0].State != "succeeded" {
		t.Errorf("rows = %+v", rs)
	}
}

func TestWriteBack_SecondConflictFailsTheAttempt(t *testing.T) {
	e := newWB(t, &Capabilities{Note: true, RequiresRevisionOnWrite: true})
	e.drafted()
	e.writeScript = func(int, DocRef) (DocRef, error) { return DocRef{}, ErrRevisionConflict }
	if err := e.run(e.args(1), 1); !errors.Is(err, ErrRevisionConflict) {
		t.Fatalf("err = %v", err)
	}
	if e.fetches.Load() != 2 || e.writeCount() != 2 {
		t.Errorf("fetches %d writes %d, want 2 and 2", e.fetches.Load(), e.writeCount())
	}
	e.wantRow(0, "failed", "revision_conflict")
}

func TestWriteBack_NoRevisionCapabilityNeverFetches(t *testing.T) {
	e := newWB(t, &Capabilities{Note: true})
	e.drafted()
	e.writeScript = func(int, DocRef) (DocRef, error) { return DocRef{}, ErrRevisionConflict }
	if err := e.run(e.args(1), 1); !errors.Is(err, ErrRevisionConflict) {
		t.Fatalf("err = %v", err)
	}
	if e.fetches.Load() != 0 || e.writeCount() != 1 || e.writes[0].SourceRevision != "1" {
		t.Errorf("fetches %d writes %+v", e.fetches.Load(), e.writes)
	}
	e.wantRow(0, "failed", "revision_conflict")
}

func TestWriteBack_MidBudgetFailureRetriesWithoutFailedAudit(t *testing.T) {
	e := newWB(t, nil)
	e.drafted()
	boom := errors.New("boom")
	e.writeScript = func(int, DocRef) (DocRef, error) { return DocRef{}, boom }
	if err := e.run(e.args(1), 3); !errors.Is(err, boom) {
		t.Fatalf("err = %v", err)
	}
	e.wantRow(0, "failed", "vendor")
	if n := len(e.audits("erp.writeback.failed")); n != 0 {
		t.Errorf("failed audit rows = %d, want 0", n)
	}
}

func TestWriteBack_FinalAttemptAuditsOnceAndLeavesTheInvoice(t *testing.T) {
	e := newWB(t, nil)
	e.drafted()
	e.writeScript = func(int, DocRef) (DocRef, error) { return DocRef{}, ErrNotFound }
	if err := e.run(e.args(1), 8); !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v", err)
	}
	e.wantRow(0, "failed", "not_found")
	au := e.audits("erp.writeback.failed")
	if len(au) != 1 || au[0]["failure"] != "not_found" || au[0]["invoice_number"] != "WB-1" || au[0]["_actor"] != "system" {
		t.Fatalf("failed audits = %+v", au)
	}
	if s, irn := e.invoiceState(); s != "accepted" || irn != wbIRN {
		t.Errorf("invoice = %s/%s, want accepted/%s", s, irn, wbIRN)
	}
}

func TestWriteBack_RateLimitSnoozes(t *testing.T) {
	e := newWB(t, nil)
	e.drafted()
	e.writeScript = func(int, DocRef) (DocRef, error) { return DocRef{}, &RateLimitError{RetryAfter: 42 * time.Second} }
	d, ok := snoozeOf(e.run(e.args(1), 1))
	if !ok || d != 42*time.Second {
		t.Fatalf("snooze = %v, %v", d, ok)
	}
	e.wantRow(0, "failed", "rate_limited")
}

func TestWriteBack_RateLimitPastTheCapSpendsAnAttempt(t *testing.T) {
	e := newWB(t, nil)
	docID := e.drafted()
	for range 8 {
		mustExec(t, e.super,
			`INSERT INTO erp_writebacks (tenant_id, entity_id, connection_id, source_document_id, invoice_id, result_version, outcome, attempt, state, reason, river_job_id)
			 VALUES ($1, $2, $3, $4, $5, 1, 'accepted', 1, 'failed', 'rate_limited', 1)`, e.tenant, e.entity, e.conn, docID, e.invoiceID)
	}
	e.writeScript = func(int, DocRef) (DocRef, error) { return DocRef{}, &RateLimitError{RetryAfter: time.Second} }
	err := e.run(e.args(1), 3)
	if _, snoozed := snoozeOf(err); err == nil || snoozed {
		t.Fatalf("err = %v, want a plain error past the cap", err)
	}
	if n := len(e.rows()); n != 9 {
		t.Errorf("rows = %d, want 9", n)
	}
	if n := len(e.audits("erp.writeback.failed")); n != 0 {
		t.Errorf("failed audits = %d, want 0 before the last attempt", n)
	}
	if err := e.run(e.args(1), 8); err == nil {
		t.Fatal("the last attempt returned nil")
	}
	au := e.audits("erp.writeback.failed")
	if len(au) != 1 || au[0]["failure"] != "rate_limited" {
		t.Errorf("failed audits = %+v", au)
	}
}

func TestWriteBack_UnsupportedIsAFailure(t *testing.T) {
	e := newWB(t, nil)
	e.drafted()
	e.writeScript = func(int, DocRef) (DocRef, error) { return DocRef{}, ErrUnsupported }
	if err := e.run(e.args(1), 1); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("err = %v", err)
	}
	e.wantRow(0, "failed", "unsupported")
}

// secondConn plants a second client with an active fake-co-2 connection.
func (e *wbEnv) secondConn() string {
	c2 := seedClient(e.t, e.super, e.tenant, "wb client 2")
	id, _ := seedActiveConn(e.t, e.super, e.tenant, c2, wbCo2)
	return id
}

func TestWriteBack_EveryLinkedDocumentIsWritten(t *testing.T) {
	e := newWB(t, nil)
	e.drafted()
	c2 := e.secondConn()
	e.doc(c2, "fake-co-2-inv-1", "linked", e.invoiceID, false)
	e.mustRun(e.args(1), 1)
	for _, k := range [][2]string{{keyOne, docOne}, {wbCo2, "fake-co-2-inv-1"}} {
		if wb, ok := e.fake.LastWriteBack(k[0], k[1]); !ok || wb.IRN != wbIRN {
			t.Errorf("no write to %v", k)
		}
	}
	if n := len(e.rows()); n != 2 {
		t.Errorf("rows = %d, want 2", n)
	}
	if n := len(e.audits("erp.writeback.succeeded")); n != 2 {
		t.Errorf("audits = %d, want 2", n)
	}
}

func TestWriteBack_RetrySkipsWrittenDocuments(t *testing.T) {
	e := newWB(t, nil)
	e.drafted()
	c2 := e.secondConn()
	e.doc(c2, "fake-co-2-inv-1", "linked", e.invoiceID, false)
	failing := true
	e.writeScript = func(_ int, ref DocRef) (DocRef, error) {
		if ref.Scope == wbCo2 && failing {
			return DocRef{}, errors.New("down")
		}
		return ref, nil
	}
	if err := e.run(e.args(1), 1); err == nil {
		t.Fatal("first attempt returned nil")
	}
	failing = false
	before := e.writeCount()
	e.mustRun(e.args(1), 2)
	if got := e.writes[before:]; len(got) != 1 || got[0].Scope != wbCo2 {
		t.Errorf("retry wrote %+v, want only fake-co-2", got)
	}
}

func TestWriteBack_HeldOrFailedDocumentsAreNotWritten(t *testing.T) {
	e := newWB(t, nil)
	e.drafted()
	e.doc(e.conn, "fake-co-1-inv-2", "held", "", false)
	e.doc(e.conn, "fake-co-1-inv-3", "failed", "", false)
	e.mustRun(e.args(1), 1)
	if e.writeCount() != 1 || e.writes[0].SourceDocID != docOne {
		t.Errorf("writes = %+v", e.writes)
	}
}

func TestWriteBack_SupersededResultWritesNothing(t *testing.T) {
	e := newWB(t, nil)
	e.drafted()
	e.history(e.invoiceID, "rejected")
	e.mustRun(e.args(1), 1)
	if e.writeCount() != 0 {
		t.Error("a superseded result was written")
	}
	e.wantRow(0, "skipped", "superseded")
}

func TestWriteBack_DisconnectedConnectionIsSkipped(t *testing.T) {
	t.Run("disconnected", func(t *testing.T) {
		e := newWB(t, nil)
		e.drafted()
		mustExec(t, e.super, "UPDATE erp_connections SET status = 'disconnected', grant_id = NULL WHERE id = $1", e.conn)
		e.mustRun(e.args(1), 1)
		if e.writeCount() != 0 {
			t.Error("a disconnected connection was written")
		}
		e.wantRow(0, "skipped", "disconnected")
	})
	t.Run("unregistered", func(t *testing.T) {
		e := newWB(t, nil)
		e.drafted()
		e.worker.Registry = Registry{}
		e.mustRun(e.args(1), 1)
		e.wantRow(0, "skipped", "disconnected")
	})
}

func TestWriteBack_DeletedInERPDocumentIsSkipped(t *testing.T) {
	t.Run("beside a live document", func(t *testing.T) {
		e := newWB(t, nil)
		e.doc(e.conn, docOne, "drafted", e.invoiceID, true)
		c2 := e.secondConn()
		e.doc(c2, "fake-co-2-inv-1", "linked", e.invoiceID, false)
		e.mustRun(e.args(1), 1)
		if e.writeCount() != 1 || e.writes[0].Scope != wbCo2 {
			t.Errorf("writes = %+v", e.writes)
		}
		skipped := 0
		for _, r := range e.rows() {
			if r.State == "skipped" && r.Reason == "deleted_in_erp" {
				skipped++
			}
		}
		if skipped != 1 {
			t.Errorf("rows = %+v", e.rows())
		}
	})
	t.Run("deleted between attempts", func(t *testing.T) {
		e := newWB(t, nil)
		e.drafted()
		e.writeScript = func(int, DocRef) (DocRef, error) { return DocRef{}, errors.New("down") }
		if err := e.run(e.args(1), 1); err == nil {
			t.Fatal("attempt 1 returned nil")
		}
		mustExec(t, e.super, "UPDATE erp_source_documents SET deleted_in_erp_at = now() WHERE tenant_id = $1", e.tenant)
		e.mustRun(e.args(1), 2)
		if e.writeCount() != 1 {
			t.Errorf("writes = %d, want only attempt 1's", e.writeCount())
		}
		if rs := e.rows(); len(rs) != 2 || rs[1].State != "skipped" || rs[1].Reason != "deleted_in_erp" {
			t.Errorf("rows = %+v", rs)
		}
	})
}

func TestWriteBack_LockedWorkspaceSnoozes(t *testing.T) {
	e := newWB(t, nil)
	e.drafted()
	lockWorkspace(t, e.super, e.tenant, uuid.NewString())
	d, ok := snoozeOf(e.run(e.args(1), 1))
	if !ok || d != 15*time.Minute {
		t.Fatalf("snooze = %v, %v", d, ok)
	}
	if e.writeCount() != 0 || len(e.rows()) != 0 {
		t.Error("a locked workspace was written or recorded")
	}
}

func TestWriteBack_MissingInvoiceCompletes(t *testing.T) {
	e := newWB(t, nil)
	e.drafted()
	a := e.args(1)
	a.InvoiceID = uuid.NewString()
	e.mustRun(a, 1)
	if e.writeCount() != 0 || len(e.rows()) != 0 {
		t.Error("a missing invoice was written or recorded")
	}
}

func TestWriteBack_FinalAttemptAuditsEveryFailingDocument(t *testing.T) {
	e := newWB(t, nil)
	e.drafted()
	c2 := e.secondConn()
	e.doc(c2, "fake-co-2-inv-1", "linked", e.invoiceID, false)
	e.writeScript = func(int, DocRef) (DocRef, error) { return DocRef{}, errors.New("down") }
	if err := e.run(e.args(1), 8); err == nil {
		t.Fatal("the last attempt returned nil")
	}
	au := e.audits("erp.writeback.failed")
	got := map[any]bool{}
	for _, a := range au {
		got[a["connection_id"]] = true
	}
	if len(au) != 2 || !got[e.conn] || !got[c2] {
		t.Errorf("failed audits = %+v, want one per connection %s and %s", au, e.conn, c2)
	}
	if n := len(e.rows()); n != 2 {
		t.Errorf("rows = %d, want 2", n)
	}
}

func TestWriteBack_RateLimitNeverHidesAnotherDocumentsOutcome(t *testing.T) {
	e := newWB(t, nil)
	e.drafted()
	c2 := e.secondConn()
	e.doc(c2, "fake-co-2-inv-1", "linked", e.invoiceID, false)
	limited := true
	e.writeScript = func(_ int, ref DocRef) (DocRef, error) {
		switch {
		case ref.Scope == keyOne && limited:
			return DocRef{}, &RateLimitError{RetryAfter: time.Minute}
		case ref.Scope == wbCo2:
			return DocRef{}, errors.New("down")
		}
		return ref, nil
	}
	err := e.run(e.args(1), 1)
	if _, snoozed := snoozeOf(err); err == nil || snoozed {
		t.Fatalf("err = %v, want the other document's plain error, not a snooze", err)
	}
	// The other document now succeeds: only the rate-limited one is left, so the job snoozes.
	e.writeScript = func(_ int, ref DocRef) (DocRef, error) {
		if ref.Scope == keyOne {
			return DocRef{}, &RateLimitError{RetryAfter: time.Minute}
		}
		return ref, nil
	}
	before := e.writeCount()
	if d, ok := snoozeOf(e.run(e.args(1), 2)); !ok || d != time.Minute {
		t.Fatalf("second attempt snooze = %v, %v", d, ok)
	}
	if got := e.writes[before:]; len(got) != 2 {
		t.Errorf("second attempt wrote %d documents, want both still open", len(got))
	}
	limited = false
	e.writeScript = nil
	before = e.writeCount()
	e.mustRun(e.args(1), 3)
	if got := e.writes[before:]; len(got) != 1 || got[0].Scope != keyOne {
		t.Errorf("third attempt wrote %+v, want only fake-co-1", got)
	}
}

func TestWriteBack_AnotherTenantsArgsWriteNothing(t *testing.T) {
	e := newWB(t, nil)
	e.drafted()
	other := seedTenant(t, e.super, "firm")
	a := e.args(1)
	a.TenantID = other
	e.mustRun(a, 1)
	var n int
	mustScan(t, e.super, "SELECT count(*) FROM erp_writebacks WHERE invoice_id = $1", []any{e.invoiceID}, &n)
	if e.writeCount() != 0 || n != 0 {
		t.Errorf("a job bound to another tenant wrote %d times and recorded %d rows", e.writeCount(), n)
	}
}

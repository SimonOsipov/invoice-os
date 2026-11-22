package erp

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"
)

// workJob runs one job under a chosen River id: the id is what orders the jobs of one document.
func (e *ingestEnv) workJob(id int64, docID, rev string, deleted bool) error {
	e.t.Helper()
	return e.workJobAt(id, 1, e.args(docID, rev, deleted))
}

func (e *ingestEnv) workJobAt(id int64, attempt int, a IngestArgs) error {
	e.t.Helper()
	job := &river.Job[IngestArgs]{
		JobRow: &rivertype.JobRow{ID: id, Attempt: attempt, MaxAttempts: 8},
		Args:   a,
	}
	return e.worker.Work(context.Background(), job)
}

func (e *ingestEnv) lastJobOf(conn, docID string) *int64 {
	e.t.Helper()
	var id *int64
	mustScan(e.t, e.super, "SELECT last_job_id FROM erp_source_documents WHERE connection_id = $1 AND source_doc_id = $2",
		[]any{conn, docID}, &id)
	return id
}

func (e *ingestEnv) setLastJob(conn, docID string, id *int64) {
	e.t.Helper()
	mustExec(e.t, e.super, "UPDATE erp_source_documents SET last_job_id = $3 WHERE connection_id = $1 AND source_doc_id = $2",
		conn, docID, id)
}

func i64(v int64) *int64 { return &v }

func (e *ingestEnv) wantLastJob(conn, docID string, want int64) {
	e.t.Helper()
	if got := e.lastJobOf(conn, docID); got == nil || *got != want {
		e.t.Errorf("last_job_id of %s = %v, want %d", docID, got, want)
	}
}

// ingestAuditEvents are the three events an ingest job writes.
var ingestAuditEvents = []string{"erp.document.pulled", "erp.document.skipped_duplicate", "erp.document.changed_after_clearance"}

func (e *ingestEnv) auditCounts() [3]int {
	var n [3]int
	for i, ev := range ingestAuditEvents {
		n[i] = len(e.audits(ev))
	}
	return n
}

// revisionAddressed makes Fetch answer the pricier content at revision 2 and the seed content otherwise.
func (e *ingestEnv) revisionAddressed() {
	base := testFake(e.t)
	e.onFetch = func(ctx context.Context, ref DocRef) (Document, error) {
		if ref.SourceRevision == "2" {
			return pricier(), nil
		}
		return base.Fetch(ctx, Credentials{}, ref)
	}
}

// settled runs revision 1 (job 5) and then revision 2 (job 20) of docOne.
func (e *ingestEnv) settled() string {
	e.t.Helper()
	e.revisionAddressed()
	if err := e.workJob(5, docOne, "1", false); err != nil {
		e.t.Fatal(err)
	}
	inv := *e.wantState(docOne, "drafted").InvoiceID
	return inv
}

func TestIngest_NormalOrderAppliesTheNewerRevision(t *testing.T) {
	e := newIngest(t)
	e.settled()
	if err := e.workJob(10, docOne, "2", false); err != nil {
		t.Fatal(err)
	}
	if d := e.doc(docOne); d.Revision != "2" || !d.Differs {
		t.Errorf("revision/differs = %s/%v, want 2/true", d.Revision, d.Differs)
	}
}

func TestIngest_LateOlderRevisionJobLeavesADraftRowAlone(t *testing.T) {
	e := newIngest(t)
	e.settled()
	if err := e.workJob(20, docOne, "2", false); err != nil {
		t.Fatal(err)
	}
	before := e.docJSON(docOne)
	if err := e.workJob(10, docOne, "1", false); err != nil {
		t.Fatal(err)
	}
	if after := e.docJSON(docOne); after != before {
		t.Errorf("the row changed:\nbefore %s\nafter  %s", before, after)
	}
	if d := e.doc(docOne); !d.Differs {
		t.Error("differs_from_erp flipped back to false")
	}
}

func TestIngest_LateOlderRevisionJobAddsNoSecondChangedAudit(t *testing.T) {
	e := newIngest(t)
	inv := e.settled()
	e.accept(inv)
	if err := e.workJob(20, docOne, "2", false); err != nil {
		t.Fatal(err)
	}
	before := e.docJSON(docOne)
	if err := e.workJob(10, docOne, "1", false); err != nil {
		t.Fatal(err)
	}
	if after := e.docJSON(docOne); after != before {
		t.Errorf("the row changed:\nbefore %s\nafter  %s", before, after)
	}
	if n := len(e.audits("erp.document.changed_after_clearance")); n != 1 {
		t.Errorf("changed_after_clearance rows = %d, want 1", n)
	}
}

func TestIngest_LateOlderUnmappableJobKeepsTheRevision(t *testing.T) {
	e := newIngest(t)
	e.settled()
	if err := e.workJob(20, docOne, "2", false); err != nil {
		t.Fatal(err)
	}
	before := e.docJSON(docOne)
	base := e.onFetch
	e.onFetch = func(ctx context.Context, ref DocRef) (Document, error) {
		if ref.SourceRevision == "1" {
			return Document{Ref: ref, ContentType: jsonType, Body: []byte("{")}, nil
		}
		return base(ctx, ref)
	}
	if err := e.workJob(10, docOne, "1", false); err != nil {
		t.Fatal(err)
	}
	if after := e.docJSON(docOne); after != before {
		t.Errorf("the row changed:\nbefore %s\nafter  %s", before, after)
	}
}

func TestIngest_LateLiveRefDoesNotClearANewerDeletion(t *testing.T) {
	e := newIngest(t)
	e.settled()
	if err := e.workJob(20, docOne, "2", true); err != nil {
		t.Fatal(err)
	}
	if e.doc(docOne).Deleted == nil {
		t.Fatal("the deleted ref did not mark the document")
	}
	before := e.docJSON(docOne)
	if err := e.workJob(10, docOne, "1", false); err != nil {
		t.Fatal(err)
	}
	if after := e.docJSON(docOne); after != before {
		t.Errorf("the row changed:\nbefore %s\nafter  %s", before, after)
	}
}

func TestIngest_LateDeletedRefDoesNotMarkANewerLiveRevision(t *testing.T) {
	e := newIngest(t)
	e.settled()
	if err := e.workJob(20, docOne, "2", false); err != nil {
		t.Fatal(err)
	}
	before := e.docJSON(docOne)
	if err := e.workJob(10, docOne, "1", true); err != nil {
		t.Fatal(err)
	}
	if after := e.docJSON(docOne); after != before {
		t.Errorf("the row changed:\nbefore %s\nafter  %s", before, after)
	}
}

// A job of the revision the row holds, or one that no row-writer outran, goes on whatever its id is.
func TestIngest_AJobOfTheRowsOwnRevisionProceedsUnderAnyId(t *testing.T) {
	// The retry of the job that wrote the row, and an earlier enqueue of the same revision: neither is stale.
	for _, c := range []struct {
		name    string
		last    int64
		jobID   int64
		wantRow int64
	}{
		{"the same job retried after a crash", 7, 7, 7},
		{"an earlier duplicate of the same revision", 20, 15, 20},
	} {
		t.Run(c.name, func(t *testing.T) {
			e := newIngest(t)
			inv := e.plantInvoice(plantInv{number: numOne, conn: &e.conn})
			e.plantDocument(plantDoc{docID: docOne, rev: "2", state: "pending", claimed: sp(numOne), updatedAgo: 20 * time.Minute})
			e.setLastJob(e.conn, docOne, i64(c.last))
			if err := e.workJob(c.jobID, docOne, "2", false); err != nil {
				t.Fatal(err)
			}
			d := e.wantState(docOne, "drafted")
			if d.InvoiceID == nil || *d.InvoiceID != inv {
				t.Errorf("row invoice_id = %v, want the adopted %s", d.InvoiceID, inv)
			}
			e.wantLastJob(e.conn, docOne, c.wantRow)
			if n := len(e.audits("erp.document.pulled")); n != 1 {
				t.Errorf("pulled rows = %d, want 1", n)
			}
		})
	}
}

func TestIngest_EpochRelistOfTheSameRevisionStopsBeforeTheFetch(t *testing.T) {
	t.Run("a draft", func(t *testing.T) {
		e := newIngest(t)
		e.settled()
		before, fetched, audits := e.docJSON(docOne), e.fetches.Load(), e.auditCounts()
		if err := e.workJob(30, docOne, "1", false); err != nil {
			t.Fatal(err)
		}
		if after := e.docJSON(docOne); after != before {
			t.Errorf("the row changed:\nbefore %s\nafter  %s", before, after)
		}
		if got := e.fetches.Load(); got != fetched {
			t.Errorf("fetches = %d, want %d: a settled revision is not fetched again", got, fetched)
		}
		if got := e.auditCounts(); got != audits || got[0] != 1 {
			t.Errorf("audit counts = %v, want %v with one pulled", got, audits)
		}
	})
	t.Run("an accepted invoice already marked changed", func(t *testing.T) {
		e := newIngest(t)
		e.accept(e.settled())
		if err := e.workJob(20, docOne, "2", false); err != nil {
			t.Fatal(err)
		}
		before, fetched := e.docJSON(docOne), e.fetches.Load()
		if err := e.workJob(30, docOne, "2", false); err != nil {
			t.Fatal(err)
		}
		if after := e.docJSON(docOne); after != before {
			t.Errorf("the row changed:\nbefore %s\nafter  %s", before, after)
		}
		if got := e.fetches.Load(); got != fetched {
			t.Errorf("fetches = %d, want %d", got, fetched)
		}
		if n := len(e.audits("erp.document.changed_after_clearance")); n != 1 {
			t.Errorf("changed_after_clearance rows = %d, want 1", n)
		}
	})
}

func TestIngest_ARowWithNoRecordedJobAcceptsAnyJob(t *testing.T) {
	e := newIngest(t)
	e.settled()
	e.setLastJob(e.conn, docOne, nil)
	if err := e.workJob(1, docOne, "2", false); err != nil {
		t.Fatal(err)
	}
	if d := e.doc(docOne); d.Revision != "2" || !d.Differs {
		t.Errorf("revision/differs = %s/%v, want 2/true", d.Revision, d.Differs)
	}
	e.wantLastJob(e.conn, docOne, 1)
}

func TestIngest_AHigherIdJobOfAnOlderLookingRevisionApplies(t *testing.T) {
	e := newIngest(t)
	e.settled()
	if err := e.workJob(20, docOne, "2", false); err != nil {
		t.Fatal(err)
	}
	if err := e.workJob(30, docOne, "1", false); err != nil {
		t.Fatal(err)
	}
	if d := e.doc(docOne); d.Revision != "1" || d.Differs {
		t.Errorf("revision/differs = %s/%v, want 1/false: the later job decides, not the revision text", d.Revision, d.Differs)
	}
	e.wantLastJob(e.conn, docOne, 30)
}

// A stale job reads the row first and stops: no fetch, no write, no snooze, no event, on every row shape.
func TestIngest_StaleJobLeavesEveryRowShapeAlone(t *testing.T) {
	shapes := map[string]plantDoc{
		"a live claim of the newer revision": {docID: docOne, rev: "2", state: "pending", claimed: sp(numOne)},
		"a failure of the newer revision":    {docID: docOne, rev: "2", state: "failed", failure: sp("unmappable")},
		"a deletion of the newer revision":   {docID: docOne, rev: "2", state: "deleted", deleted: tp(time.Now())},
	}
	for name, shape := range shapes {
		for _, deleted := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s, ref deleted=%v", name, deleted), func(t *testing.T) {
				e := newIngest(t)
				e.plantDocument(shape)
				e.setLastJob(e.conn, docOne, i64(20))
				before := e.docJSON(docOne)
				if err := e.workJob(10, docOne, "1", deleted); err != nil {
					t.Fatalf("Work = %v, want nil: no snooze and no retry", err)
				}
				if after := e.docJSON(docOne); after != before {
					t.Errorf("the row changed:\nbefore %s\nafter  %s", before, after)
				}
				if n := e.fetches.Load(); n != 0 {
					t.Errorf("fetches = %d, want 0", n)
				}
				if got := e.auditCounts(); got != [3]int{} {
					t.Errorf("audit counts = %v, want none", got)
				}
				if n := e.invoices(); n != 0 {
					t.Errorf("invoices = %d, want 0", n)
				}
			})
		}
	}
}

// A job that passed the first read and was outrun before its next transaction still writes nothing.
func TestIngest_AJobOutrunMidWayWritesNothing(t *testing.T) {
	unmappable := Document{ContentType: jsonType, Body: []byte("{")}
	for _, c := range []struct {
		name    string
		attempt int
		answer  func() (Document, error)
		wantErr error
	}{
		{"claim", 1, func() (Document, error) { return pricier(), nil }, nil},
		{"fail", 1, func() (Document, error) { return unmappable, nil }, nil},
		{"terminal", 8, func() (Document, error) { return Document{}, errVendor }, errVendor},
	} {
		t.Run(c.name, func(t *testing.T) {
			e := newIngest(t)
			e.settled()
			mustExec(t, e.super, "UPDATE erp_connections SET cursor = '\\x0a0b' WHERE id = $1", e.conn)
			var snapshot string
			e.onFetch = func(ctx context.Context, ref DocRef) (Document, error) {
				mustExec(t, e.super, "UPDATE erp_source_documents SET source_revision = '3', last_job_id = 20 WHERE connection_id = $1 AND source_doc_id = $2",
					e.conn, docOne)
				snapshot = e.docJSON(docOne)
				d, err := c.answer()
				d.Ref = ref
				return d, err
			}
			audits := e.auditCounts()
			err := e.workJobAt(10, c.attempt, e.args(docOne, "2", false))
			if !errors.Is(err, c.wantErr) {
				t.Fatalf("Work err = %v, want %v", err, c.wantErr)
			}
			if snapshot == "" {
				t.Fatal("the job never fetched")
			}
			if after := e.docJSON(docOne); after != snapshot {
				t.Errorf("the row changed:\nbefore %s\nafter  %s", snapshot, after)
			}
			if got := e.auditCounts(); got != audits {
				t.Errorf("audit counts = %v, want %v", got, audits)
			}
			var cursor []byte
			var epoch int64
			mustScan(t, e.super, "SELECT cursor, ingest_epoch FROM erp_connections WHERE id = $1", []any{e.conn}, &cursor, &epoch)
			if cursor == nil || epoch != 0 {
				t.Errorf("cursor/ingest_epoch = %v/%d, want the cursor kept and epoch 0", cursor, epoch)
			}
		})
	}
	t.Run("terminal of a job nobody outran reopens the listing", func(t *testing.T) {
		e := newIngest(t)
		e.settled()
		mustExec(t, e.super, "UPDATE erp_connections SET cursor = '\\x0a0b' WHERE id = $1", e.conn)
		e.onFetch = func(context.Context, DocRef) (Document, error) { return Document{}, errVendor }
		if err := e.workJobAt(10, 8, e.args(docOne, "2", false)); !errors.Is(err, errVendor) {
			t.Fatalf("Work err = %v, want the vendor error", err)
		}
		var cursor []byte
		var epoch int64
		mustScan(t, e.super, "SELECT cursor, ingest_epoch FROM erp_connections WHERE id = $1", []any{e.conn}, &cursor, &epoch)
		if cursor != nil || epoch != 1 {
			t.Errorf("cursor/ingest_epoch = %v/%d, want NULL/1", cursor, epoch)
		}
	})
}

// The recorded job id gates only its own document: not another document, not the same document id on another connection.
func TestIngest_OneDocumentsLastJobNeverGatesAnother(t *testing.T) {
	e := newIngest(t)
	e.revisionAddressed()
	c2 := e.addConnection(e.entity, "fake-co-2")
	if err := e.workJob(5, docOne, "1", false); err != nil {
		t.Fatal(err)
	}
	e.plantDocument(plantDoc{docID: "other-doc", rev: "9", state: "failed", failure: sp("unmappable")})
	e.setLastJob(e.conn, "other-doc", i64(1000))
	e.plantDocument(plantDoc{conn: c2, docID: docOne, rev: "9", state: "failed", failure: sp("unmappable")})
	e.setLastJob(c2, docOne, i64(1000))

	if err := e.workJob(10, docOne, "2", false); err != nil {
		t.Fatal(err)
	}
	if d := e.doc(docOne); d.Revision != "2" || !d.Differs {
		t.Errorf("revision/differs = %s/%v, want 2/true: only this document's own last job gates it", d.Revision, d.Differs)
	}
	e.wantLastJob(e.conn, docOne, 10)

	// The control: the same job id against the other connection's own row is stale, so the guard does bite there.
	other := e.args(docOne, "2", false)
	other.ConnectionID = c2
	if err := e.workJobAt(10, 1, other); err != nil {
		t.Fatal(err)
	}
	if d, ok := e.docOn(c2, docOne); !ok || d.Revision != "9" || d.State != "failed" {
		t.Errorf("the other connection's row = %+v, want its rev 9 failed row untouched", d)
	}
	e.wantLastJob(e.conn, "other-doc", 1000)
}

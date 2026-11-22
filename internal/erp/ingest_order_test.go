package erp

import (
	"context"
	"testing"

	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"
)

// workJob runs one job under a chosen River id: the id is what orders the jobs of one document.
func (e *ingestEnv) workJob(id int64, docID, rev string, deleted bool) error {
	e.t.Helper()
	job := &river.Job[IngestArgs]{
		JobRow: &rivertype.JobRow{ID: id, Attempt: 1, MaxAttempts: 8},
		Args:   e.args(docID, rev, deleted),
	}
	return e.worker.Work(context.Background(), job)
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

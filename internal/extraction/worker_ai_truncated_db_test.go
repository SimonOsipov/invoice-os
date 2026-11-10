// SECAUD-05-04: extraction_jobs.ai_text_truncated, the flag a tenant text read records when both
// AI calls answered from cut text. Rides worker_db_test.go's harness (wkAI, wpWorker, wkImageWorker).
package extraction_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/SimonOsipov/invoice-os/internal/extraction"
)

// atOverBoundPage is 20 filler tokens, each under the per-token cut (maxPromptRunBytes) and
// together past the document bound (maxDoclingPromptBytes, 256 KB). None reads as a label.
func atOverBoundPage() extraction.Page {
	const rows = 20
	p := extraction.Page{Number: 1, WidthPt: 612, HeightPt: 792}
	for i := range rows {
		y := 0.05 + 0.9*float64(i)/rows
		p.Tokens = append(p.Tokens, extraction.Token{
			Text:   fmt.Sprintf("filler%02d%s", i, strings.Repeat("x", 15_000)),
			Region: extraction.Region{Page: 1, X0: 0.1, Y0: y, X1: 0.9, Y1: y + 0.01},
		})
	}
	return p
}

// atNormalPage is a few short lines, far under any bound.
func atNormalPage() extraction.Page { return wjPage() }

func atAI() *wkAI { return &wkAI{enabled: true, answer: map[string]any{"invoice_number": "20417"}} }

// atFlag reads extraction_jobs.ai_text_truncated as the superuser; a missing column fails here
// with the SQL error naming it.
func atFlag(t *testing.T, ctx context.Context, jobID string) bool {
	t.Helper()
	var got bool
	if err := stRequire(t).super.QueryRow(ctx,
		`SELECT ai_text_truncated FROM extraction_jobs WHERE id = $1`, jobID).Scan(&got); err != nil {
		t.Fatalf("read ai_text_truncated for job %s: %v", jobID, err)
	}
	return got
}

// atRun is one successful tenant text read of page with the given AI seam.
func atRun(t *testing.T, ctx context.Context, riverJobID int64, page extraction.Page, reader extraction.AIReader) (jobID string) {
	t.Helper()
	run := wkRunAI(t, ctx, riverJobID, []extraction.Page{page}, reader)
	return run.jobID
}

func TestExtractWorker_AnOverBoundTextSetsAITextTruncated(t *testing.T) {
	ctx := t.Context()
	stub := atAI()
	jobID := atRun(t, ctx, 940401, atOverBoundPage(), stub)
	if n := stub.count(); n != 2 {
		t.Fatalf("the AI seam saw %d call(s), want 2 (header and lines): the over-bound read never reached the AI", n)
	}
	if !atFlag(t, ctx, jobID) {
		t.Errorf("job %s read an over-bound text with the AI enabled and recorded ai_text_truncated false, want true", jobID)
	}
}

func TestExtractWorker_ANormalTextLeavesAITextTruncatedFalse(t *testing.T) {
	ctx := t.Context()
	stub := atAI()
	jobID := atRun(t, ctx, 940402, atNormalPage(), stub)
	if n := stub.count(); n != 2 {
		t.Fatalf("the AI seam saw %d call(s), want 2: the control never reached the AI", n)
	}
	if atFlag(t, ctx, jobID) {
		t.Errorf("job %s read a normal text and recorded ai_text_truncated true, want false", jobID)
	}
}

func TestExtractWorker_AnOverBoundTextWithTheAIOffLeavesItFalse(t *testing.T) {
	ctx := t.Context()
	jobID := atRun(t, ctx, 940403, atOverBoundPage(), nil) // literal nil: no AI reader at all
	if atFlag(t, ctx, jobID) {
		t.Errorf("job %s ran with the AI off and recorded ai_text_truncated true, want false", jobID)
	}
}

// The audit hook runs last in the result transaction; its error rolls the flag back with the
// rest of the write.
func TestExtractWorker_AFailedResultWriteLeavesAITextTruncatedFalse(t *testing.T) {
	ctx := t.Context()
	tenantID, documentID := wkFixture(t, ctx)
	stub := atAI()
	ew := wpWorker(t, wkOK(), wpCorpusOpener(t), &wpReader{pages: []extraction.Page{atOverBoundPage()}}, wpStoreRules(t).load, &wkAuditRecorder{})
	ew.AI = stub
	auditErr := errors.New("audit store down")
	ew.Audit = func(context.Context, pgx.Tx, extraction.ExtractionAudit) error { return auditErr }

	const riverJobID = int64(940404)
	err := ew.Work(ctx, extraction.NewExtractJobForTest(riverJobID, 1, 3, tenantID, documentID, uuid.NewString()))
	if !errors.Is(err, auditErr) {
		t.Fatalf("Work returned %v, want the audit error", err)
	}
	if n := stub.count(); n != 2 {
		t.Fatalf("the AI seam saw %d call(s), want 2: the read never reached the result write", n)
	}
	xid := wkExtractionJobID(t, ctx, tenantID, riverJobID)
	var state string
	if err := stRequire(t).super.QueryRow(ctx, `SELECT state FROM extraction_jobs WHERE id = $1`, xid).Scan(&state); err != nil {
		t.Fatalf("read job %s state: %v", xid, err)
	}
	if state == "succeeded" {
		t.Fatalf("job %s is succeeded although its result write failed", xid)
	}
	if atFlag(t, ctx, xid) {
		t.Errorf("job %s has ai_text_truncated true although its result transaction rolled back, want false", xid)
	}
}

func TestExtractWorker_AnOverBoundTextWhoseAICallFailsLeavesItFalse(t *testing.T) {
	ctx := t.Context()
	stub := &wkAI{enabled: true, err: errors.New("provider 500")}
	run := wkRunAI(t, ctx, 940405, []extraction.Page{atOverBoundPage()}, stub)
	if n := stub.count(); n < 1 {
		t.Fatalf("the AI seam saw %d call(s), want at least 1: the failing call was never made", n)
	}
	// The AI-unavailable row proves the read took the failure path, not the answered one.
	unavailable := false
	for _, r := range run.rows {
		unavailable = unavailable || r.name == "document_ai_reading"
	}
	if !unavailable {
		t.Fatalf("job %s wrote no document_ai_reading row, so the AI-failure path was not taken: %v", run.jobID, run.rows)
	}
	if atFlag(t, ctx, run.jobID) {
		t.Errorf("job %s whose AI call failed recorded ai_text_truncated true, want false (the AI read nothing)", run.jobID)
	}
}

// The image arm sends page images, not text: no text is cut, so the flag stays false.
func TestExtractWorker_TheImageArmLeavesAITextTruncatedFalse(t *testing.T) {
	ctx := t.Context()
	tenantID, documentID := wkFixture(t, ctx)
	raw := fxRead(t, fxScanned)
	bucket := &wkPageBucket{}
	stub := &wkAI{enabled: true, answer: map[string]any{"invoice_number": "INV-5520", "total": "1935.00"}}
	ew := wkImageWorker(t, raw, extraction.NewPDFiumReader(), bucket, wpStoreRules(t).load, stub, &wkAuditRecorder{})

	const riverJobID = int64(940406)
	if err := ew.Work(ctx, extraction.NewExtractJobForTest(riverJobID, 1, 3, tenantID, documentID, uuid.NewString())); err != nil {
		t.Fatalf("Work: %v", err)
	}
	xid := wkExtractionJobID(t, ctx, tenantID, riverJobID)
	stAssertJobState(t, ctx, xid, "succeeded")
	if n := stub.count(); n != 1 || len(stub.reqs[0].Pages) != 1 {
		t.Fatalf("the AI seam saw %d call(s) (requests %d), want one image call: the image arm was not taken", n, len(stub.reqs))
	}
	if atFlag(t, ctx, xid) {
		t.Errorf("job %s took the image arm and recorded ai_text_truncated true, want false", xid)
	}
}

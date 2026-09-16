// learn_typed_db_test.go: a typed correction on a PDF teaches the next document of that layout,
// read through the real ExtractWorker with PDFium as its text reader. Helpers use a tl* prefix.
package extraction_test

import (
	"context"
	"slices"
	"testing"

	"github.com/SimonOsipov/invoice-os/internal/extraction"
)

// tlRead runs the real worker over fixture as documentID and returns the key the rules seam was
// asked for, the rules it served, and the rows the job wrote.
func tlRead(t *testing.T, ctx context.Context, tenantID, documentID string, riverJobID int64, fixture string) (string, []extraction.AnchorRule, []wpRow, string) {
	t.Helper()
	rules := wpStoreRules(t)
	ew := wpWorker(t, wkOK(), &wkOpener{body: fxRead(t, fixture)}, extraction.NewPDFiumReader(), rules.load, &wkAuditRecorder{})
	jobID := clSettle(t, ctx, tenantID, documentID, riverJobID, ew)
	asked, served := rules.wpOnlyCall(t)
	return asked, served, wpResults(t, ctx, jobID), jobID
}

// tlTeach reads the first document and types its total through the route. The one learned event
// is the route's own word that the self-check passed.
func tlTeach(t *testing.T, ctx context.Context, number string, riverJobID int64) (clFixture, string) {
	t.Helper()
	f := clSeed(t, ctx, number)
	wkCleanupInfra(t, f.tenantID)
	_, _, rows, jobID := tlRead(t, ctx, f.tenantID, f.documentID, riverJobID, fxLearnedTypedTotal)
	wpAssertRankZero(t, rows, ctField, nil, stPtr(string(extraction.ReasonMissing)))
	if v := ctVerdict(t, rvCorpusPages(t, fxLearnedTypedTotal), ctField, ctTotal); v != extraction.TypedLearned {
		t.Errorf("LearnTypedRule(%s, %q) = %d, want TypedLearned", ctField, ctTotal, v)
	}
	if t.Failed() {
		t.FailNow()
	}

	_, pageOne := ctReader(t, fxLearnedTypedTotal)
	learned := &blLearnRecorder{}
	ctPost(t, f, jobID, ctField, ctTotal, pageOne, nil, learned.record)
	if n := len(learned.events()); n != 1 {
		t.Fatalf("the typed correction emitted %d anchor.learned event(s), want 1 -- the next document can read no rule", n)
	}
	return f, jobID
}

func TestRLS_ATypedCorrectionTeachesTheNextDocumentOfThatLayout(t *testing.T) {
	ctx := t.Context()
	f, job1 := tlTeach(t, ctx, "EXTR28-04-E2E", 928501)
	fp := clJobFingerprint(t, ctx, job1)

	asked, served, taught, _ := tlRead(t, ctx, f.tenantID, wkSecondDocument(t, ctx, f.tenantID), 928502, fxLearnedTypedTotalTwin)
	if asked != fp {
		t.Fatalf("the rules seam was asked for %q over the twin, want the first job's %q", asked, fp)
	}
	if len(served) != 1 {
		t.Fatalf("the rules seam served %d rule(s) over the twin, want 1", len(served))
	}
	wpAssertRankZero(t, taught, ctField, stPtr("9250000.00"), nil)

	bareID, bareDoc := wkFixture(t, ctx)
	bareAsked, bareServed, bare, _ := tlRead(t, ctx, bareID, bareDoc, 928503, fxLearnedTypedTotalTwin)
	if bareAsked != fp || len(bareServed) != 0 {
		t.Fatalf("the untaught tenant was asked for %q and served %d rule(s), want %q and 0", bareAsked, len(bareServed), fp)
	}
	wpAssertRankZero(t, bare, ctField, nil, stPtr(string(extraction.ReasonMissing)))
	wkAssertOnlyFieldDiffers(t, taught, bare, ctField)
}

func TestRLS_DeletingTheTypedRuleRedsTheNextDocument(t *testing.T) {
	ctx := t.Context()
	f, _ := tlTeach(t, ctx, "EXTR28-04-DELETE", 928511)

	_, served, taught, _ := tlRead(t, ctx, f.tenantID, wkSecondDocument(t, ctx, f.tenantID), 928512, fxLearnedTypedTotalTwin)
	if len(served) != 1 {
		t.Fatalf("the rules seam served %d rule(s) over the twin, want 1", len(served))
	}
	wpAssertRankZero(t, taught, ctField, stPtr("9250000.00"), nil)

	lcDeleteRule(t, ctx, served[0].ID)
	_, after, untaught, _ := tlRead(t, ctx, f.tenantID, wkSecondDocument(t, ctx, f.tenantID), 928513, fxLearnedTypedTotalTwin)
	if len(after) != 0 {
		t.Errorf("the rules seam served %d rule(s) after the delete, want 0", len(after))
	}
	wpAssertRankZero(t, untaught, ctField, nil, stPtr(string(extraction.ReasonMissing)))
}

// tlTeachChrome reads chrome_register.pdf through the real worker and types its buyer_name
// through the route -- the Chrome-print twin of tlTeach.
func tlTeachChrome(t *testing.T, ctx context.Context, number string, riverJobID int64) (clFixture, string) {
	t.Helper()
	f := clSeed(t, ctx, number)
	wkCleanupInfra(t, f.tenantID)
	_, _, rows, jobID := tlRead(t, ctx, f.tenantID, f.documentID, riverJobID, chrRegister)
	wpAssertRankZero(t, rows, chtField, stPtr(chtValue), stPtr(string(extraction.ReasonAmbiguous)))
	if v := ctVerdict(t, rvCorpusPages(t, chrRegister), chtField, chtValue); v != extraction.TypedLearned {
		t.Errorf("LearnTypedRule(%s, %q) = %d, want TypedLearned", chtField, chtValue, v)
	}
	if t.Failed() {
		t.FailNow()
	}

	_, pageOne := ctReader(t, chrRegister)
	learned := &blLearnRecorder{}
	ctPost(t, f, jobID, chtField, chtValue, pageOne, nil, learned.record)
	if n := len(learned.events()); n != 1 {
		t.Fatalf("the typed correction emitted %d anchor.learned event(s), want 1 -- the next document can read no rule", n)
	}
	return f, jobID
}

// AC-4: the twin's buyer_name VALUE is identical taught and untaught -- only the reason moves,
// ambiguous -> none -- so the oracle below is the reason, per the Implementation Notes.
func TestRLS_AChromePrintedTwinReadsTheLearnedBuyer(t *testing.T) {
	ctx := t.Context()
	f, job1 := tlTeachChrome(t, ctx, "EXTR36-05-TWIN", 928601)
	fp := clJobFingerprint(t, ctx, job1)

	asked, served, taught, _ := tlRead(t, ctx, f.tenantID, wkSecondDocument(t, ctx, f.tenantID), 928602, chrRegisterTwin)
	if asked != fp {
		t.Fatalf("the rules seam was asked for %q over the twin, want the first job's %q", asked, fp)
	}
	if len(served) != 1 {
		t.Fatalf("the rules seam served %d rule(s) over the twin, want 1", len(served))
	}
	wpAssertRankZero(t, taught, chtField, stPtr(chtValue), nil)

	bareID, bareDoc := wkFixture(t, ctx)
	bareAsked, bareServed, bare, _ := tlRead(t, ctx, bareID, bareDoc, 928603, chrRegisterTwin)
	if bareAsked != fp || len(bareServed) != 0 {
		t.Fatalf("the untaught tenant was asked for %q and served %d rule(s), want %q and 0", bareAsked, len(bareServed), fp)
	}
	wpAssertRankZero(t, bare, chtField, stPtr(chtValue), stPtr(string(extraction.ReasonAmbiguous)))

	// wkAssertOnlyFieldDiffers does not hold here (Implementation Notes' EXECUTOR RISK): the
	// taught run's self-check adds a third buyer_name candidate the untaught run never writes,
	// so the row sets differ by rank count, not only by the taught field's own value.
	if got, want := wpRankZeroNames(taught), wpRankZeroNames(bare); !slices.Equal(got, want) {
		t.Errorf("taught rank-0 fields = %v, untaught rank-0 fields = %v, want the same set", got, want)
	}
}

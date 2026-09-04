// corpus_wired_db_test.go: the golden corpus measured through ExtractWorker.Work rather than
// through Resolve + Reconcile in process. Same 44 pairs, same denominator; what it adds is the
// store round trip, the fingerprint hoist, the learned-rule lookup and the rank-0 encoding in
// extraction_field_results.
//
// Package extraction_test, so it shares store_db_test.go's TestMain, per-role pools and single
// skip site.
package extraction_test

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/SimonOsipov/invoice-os/internal/extraction"
)

// --- what the wired walk is measured against ---------------------------------------------

// cwReportMarker titles the wired report. The ci.yml step greps for it and
// TestRLS_WiredPathTheCIStepsRunFilterNamesARealTest asserts the step carries it, so a rename
// cannot leave the workflow grepping for nothing.
const cwReportMarker = "tier-1 decision rate through the wired worker path"

// cwReportRunFilter is the -run pattern that ci.yml step must carry: the one spec that renders
// the report.
const cwReportRunFilter = "TestRLS_WiredPathScoresTheCorpus"

// The doc section the number is recorded under, and the fraction it must state.
const cwDocSection = "## Wired-path decision rate"

// The ranking decoy, seeded as a LEARNED rule through the real rule store.
//
// The label is the in-process acDecoyLabel widened by one alternation branch, and the widening
// is load-bearing rather than cosmetic. decideField (reconcile.go:78-82) keeps an alternative
// only when it shares the head's Tier AND Distance, so a TierLearned decoy at Distance 0 leaves
// the TierGeneric 5375.00 with no row at all: measured, the narrow decoy writes exactly ONE
// total row and an any-rank scorer reads the same 42/44 a rank-0 scorer does. Matching both
// amount tokens puts both candidates at TierLearned Distance 0, they tie, and compareRegions
// hands rank 0 to the higher token (Sub-total, Y0 0.6861) and rank 1 to the real total
// (Y0 0.7315). That is what makes an any-rank scorer read 43/44 and fail.
const (
	cwDecoyLayout = "corpus_totals_block.pdf"
	cwDecoyField  = "total"
	cwDecoyRule   = `{"label":"^\\s*5,(000|375)\\.00\\s*$","relation":{"kind":"same_token","max_distance":0},"shape":"amount"}`
	cwDecoyRank0  = "5000.00" // the Sub-total amount, filed under total
	cwDecoyRank1  = "5375.00" // the layout's real total, still reached, now an alternative
)

// cwDecoyHits: the decoy takes exactly one pair off the wired rate.
const cwDecoyHits = tier1DecisionHits - 1

// cwGoldenSuffix turns a corpus layout into its committed Docling golden.
const cwGoldenSuffix = ".docling.json"

// cwLineItemsRow is the block row Reconcile emits on every document. It is NOT a HeaderFields
// member, so the walk below must skip it or a layout with a table would score as a fabrication.
const cwLineItemsRow = "line_items"

// --- harness -----------------------------------------------------------------------------

// cwRankMode selects which candidate_rank rows the walk may score. Two modes, not one: on this
// corpus the rank-0 rate and the any-rank rate coincide at 43/44, so the shipped number alone
// cannot tell a rank-reading scorer from a candidate-containment one. The decoy spec runs both.
type cwRankMode int

const (
	cwRankZeroOnly cwRankMode = iota
	cwAnyRank
)

func (m cwRankMode) String() string {
	if m == cwAnyRank {
		return "any candidate_rank"
	}
	return "candidate_rank 0"
}

// cwReaderKind selects the PageReader the wired run drives Work with.
type cwReaderKind int

const (
	cwDocling cwReaderKind = iota // a real DoclingReader replaying the layout's committed golden
	cwPDFium                      // the reader the in-process harness reads through
)

func (r cwReaderKind) String() string {
	if r == cwPDFium {
		return "PDFiumReader"
	}
	return "DoclingReader over the committed golden"
}

// cwSeeder writes learned rules for one layout's tenant before Work runs. nil is the baseline.
type cwSeeder func(t *testing.T, ctx context.Context, tenantID, layout, fingerprint string)

// cwRun is one layout's wired run.
type cwRun struct {
	layout      string
	fingerprint string
	learned     int     // rules the Rules seam SERVED for this layout's tenant
	rows        []wpRow // every extraction_field_results row for the job
	decided     int     // rank-0 HeaderFields rows carrying a value
}

// cwScore is one walk of corpusExpect over the wired rows.
type cwScore struct {
	hits, total int
	missed      []acPair
	got         map[acPair]string
	runs        []cwRun
}

func (s cwScore) rate() float64 { return float64(s.hits) / float64(s.total) }

func (s cwScore) run(t *testing.T, layout string) cwRun {
	t.Helper()
	for _, r := range s.runs {
		if r.layout == layout {
			return r
		}
	}
	t.Fatalf("no wired run for %s among %d run(s)", layout, len(s.runs))
	return cwRun{}
}

// cwGolden is a layout's committed Docling golden.
func cwGolden(layout string) string {
	return strings.TrimSuffix(layout, ".pdf") + cwGoldenSuffix
}

// cwScoreWired drives ExtractWorker.Work once per corpusExpect layout -- a fresh tenant and
// document each, the layout's real bytes through the document opener, reader as the caller
// chose, and (*Store).AnchorRulesFor as the Rules seam -- then scores the rows read back out of
// extraction_field_results against corpusExpect, scoped to HeaderFields and to mode.
//
// base is the first river_job_id; each layout takes the next one.
func cwScoreWired(t *testing.T, ctx context.Context, what string, base int64, reader cwReaderKind, mode cwRankMode, seed cwSeeder) cwScore {
	t.Helper()

	corpusRequireCommitted(t)
	if len(corpusExpect) == 0 {
		t.Fatal("corpusExpect is empty; the walk below would score 0/0 and every rate assertion would read NaN")
	}

	s := cwScore{got: map[acPair]string{}}
	for i, want := range corpusExpect {
		tenantID, documentID := wkFixture(t, ctx)

		// The seeder is keyed by fingerprint, and Work computes it over the PDFium tokens
		// whatever the text reader is (worker.go hoists it out of Pages.Ingest).
		fingerprint := extraction.Fingerprint(rvCorpusPages(t, want.file))
		if seed != nil {
			seed(t, ctx, tenantID, want.file, fingerprint)
		}

		var text extraction.PageReader = extraction.NewPDFiumReader()
		if reader == cwDocling {
			text = wpDoclingReader(t, dcReadNamedGolden(t, cwGolden(want.file)))
		}
		rules := wpStoreRules(t)
		ew := wpWorker(t, wkOK(), &wkOpener{body: fxRead(t, want.file)}, text, rules.load, &wkAuditRecorder{})

		riverJobID := base + int64(i)
		if err := ew.Work(ctx, extraction.NewExtractJobForTest(riverJobID, 1, 3, tenantID, documentID, uuid.NewString())); err != nil {
			t.Fatalf("%s: Work over %s: %v", what, want.file, err)
		}

		asked, served := rules.wpOnlyCall(t)
		run := cwRun{
			layout:      want.file,
			fingerprint: asked,
			learned:     len(served),
			rows:        wpResults(t, ctx, wkExtractionJobID(t, ctx, tenantID, riverJobID)),
		}

		byField := map[string][]wpRow{}
		for _, row := range run.rows {
			byField[row.name] = append(byField[row.name], row)
		}
		for _, field := range extraction.HeaderFields {
			// decided is rank-0-and-non-NULL: a field Reconcile left undecided writes a row
			// carrying a reason and no value, so row presence is not the same question.
			var zero *string
			var anyValue []string
			for _, row := range byField[field] {
				if row.value == nil {
					continue
				}
				anyValue = append(anyValue, *row.value)
				if row.rank == 0 {
					zero = row.value
				}
			}
			if zero != nil {
				run.decided++
			}

			values, named := want.fields[field]
			if !named {
				continue
			}
			if len(values) == 0 {
				t.Fatalf("%s expects %s with no value; the pair would count as a miss for the wrong reason", want.file, field)
			}
			pair := acPair{file: want.file, field: field}
			s.total++
			if zero != nil {
				s.got[pair] = *zero
			}

			hit := zero != nil && slices.Contains(values, *zero)
			if mode == cwAnyRank {
				hit = false
				for _, v := range anyValue {
					if slices.Contains(values, v) {
						hit = true
					}
				}
			}
			if hit {
				s.hits++
				continue
			}
			s.missed = append(s.missed, pair)
		}
		s.runs = append(s.runs, run)
	}
	return s
}

// cwRequireWalk fails when the walk did not cover the corpus. Every assertion below quantifies
// over runs or over the score, and an empty walk satisfies all of them -- here an empty score
// set would read as a perfect run rather than as a broken one.
func cwRequireWalk(t *testing.T, s cwScore, what string) {
	t.Helper()

	if len(corpusExpect) == 0 {
		t.Fatal("corpusExpect is empty; every walk below would score 0/0 and read NaN")
	}
	if len(s.runs) != len(corpusExpect) {
		t.Fatalf("%s produced %d wired run(s), want one per corpusExpect layout (%d)", what, len(s.runs), len(corpusExpect))
	}
	if s.total == 0 {
		t.Fatalf("%s scored no pair at all; every assertion over the score would hold vacuously", what)
	}
	for _, r := range s.runs {
		if len(r.rows) == 0 {
			t.Fatalf("%s: %s wrote no extraction_field_results row; the walk is scoring an empty table", what, r.layout)
		}
		// The analogue of rvFloor and of acScoreDecisions' per-layout floor: a degraded page
		// ingest or text read collapses the result set to the single document_text_layer row,
		// and the walk would report a total regression rather than a broken reader.
		if r.decided == 0 {
			t.Fatalf("%s: %s decided no field at all; that is a reader failure, not a ranking result", what, r.layout)
		}
	}
}

// cwMissedSet is the walk's missed pairs as a set.
func cwMissedSet(s cwScore) map[acPair]bool {
	out := make(map[acPair]bool, len(s.missed))
	for _, p := range s.missed {
		out[p] = true
	}
	return out
}

// cwGapSet is t1aGaps as a set.
func cwGapSet() map[acPair]bool {
	out := make(map[acPair]bool, len(t1aGaps))
	for _, g := range t1aGaps {
		out[acPair{file: g.file, field: g.field}] = true
	}
	return out
}

// cwRenderReport is the block ci.yml greps. The marker and both numbers, or the step passes on
// output that says nothing.
func cwRenderReport(s cwScore) string {
	var b strings.Builder

	fmt.Fprintf(&b, "%s\n", cwReportMarker)
	fmt.Fprintf(&b, "  wired decision rate (rank-0 rows in extraction_field_results): %d / %d = %s (pinned %d / %d = %s)\n",
		s.hits, s.total, strconv.FormatFloat(s.rate(), 'f', 4, 64),
		tier1DecisionHits, tier1DecisionPairs, strconv.FormatFloat(tier1DecisionRate, 'f', 4, 64))
	for _, r := range s.runs {
		fmt.Fprintf(&b, "    %-28s decided %d field(s), %d row(s), learned %d, fingerprint %s\n",
			r.layout, r.decided, len(r.rows), r.learned, r.fingerprint)
	}
	for _, p := range s.missed {
		fmt.Fprintf(&b, "  MISS %s / %s = %q, want one of %v\n", p.file, p.field, s.got[p], acExpectedValues(p))
	}
	return b.String()
}

// --- the specs ----------------------------------------------------------------------------

// AC-1, AC-11. Every corpus layout read end to end through Work, scored from the rows the job
// wrote rather than from a Reconcile return value. The scope is asserted in the same spec: the
// line_items block row is present on every layout and is NOT part of the denominator.
func TestRLS_WiredPathScoresTheCorpus(t *testing.T) {
	ctx := t.Context()
	t1Floor(t)
	corpusRequireCommitted(t)

	s := cwScoreWired(t, ctx, "the shipped Tier-1 set", 918100, cwDocling, cwRankZeroOnly, nil)
	cwRequireWalk(t, s, "the shipped Tier-1 set")
	t.Log("\n" + cwRenderReport(s))

	if s.hits != tier1DecisionHits {
		for _, p := range s.missed {
			t.Errorf("%s: %s decided %q through the wired path, want one of %v", p.file, p.field, s.got[p], acExpectedValues(p))
		}
		t.Errorf("the wired path decides %d/%d = %v, want %d/%d = %v", s.hits, s.total, s.rate(), tier1DecisionHits, tier1DecisionPairs, tier1DecisionRate)
	}
	if s.hits+len(s.missed) != s.total {
		t.Errorf("%d hit(s) plus %d miss(es) is not the %d pair(s) scored", s.hits, len(s.missed), s.total)
	}

	// The provenance half of AC-1: the rows come from the table, so the block row Reconcile
	// emits is in them. A walk reading a Reconcile return value could scope itself before the
	// block row ever existed.
	for _, r := range s.runs {
		names := map[string]bool{}
		for _, row := range r.rows {
			if row.rank == 0 {
				names[row.name] = true
			}
		}
		if !names[cwLineItemsRow] {
			t.Errorf("%s wrote no rank-0 %s row; these rows did not come from extraction_field_results", r.layout, cwLineItemsRow)
		}
		for _, field := range extraction.HeaderFields {
			if !names[field] {
				t.Errorf("%s wrote no rank-0 %s row; the walk is scoring a partial vocabulary", r.layout, field)
			}
		}
	}
}

// AC-4. The wired rate is the in-process decision rate, not merely close to it. A divergence
// means a stage the in-process harness skips changes the answer, and that must fail here rather
// than be absorbed into a second pinned number.
func TestRLS_WiredPathRateMatchesTheInProcessDecisionRate(t *testing.T) {
	ctx := t.Context()

	s := cwScoreWired(t, ctx, "the shipped Tier-1 set", 918200, cwDocling, cwRankZeroOnly, nil)
	cwRequireWalk(t, s, "the shipped Tier-1 set")

	if s.hits != tier1DecisionHits || s.total != tier1DecisionPairs {
		t.Errorf("the wired path decides %d/%d; the in-process harness decides %d/%d. The store round trip, the fingerprint hoist, the rule lookup and the rank-0 encoding must not move the number",
			s.hits, s.total, tier1DecisionHits, tier1DecisionPairs)
	}
	if s.rate() != tier1DecisionRate {
		t.Errorf("the wired rate is %v and the in-process rate %v; both are quotients of the same two integers, so a difference here is a different denominator", s.rate(), tier1DecisionRate)
	}
}

// AC-2, AC-3. The corpus runs through a REAL DoclingReader replaying the committed goldens --
// the reader the deployed worker uses when EXTRACTOR=docling -- and PDFium is the second run
// AC-4's equality claim is well-posed against. Compared by identity, not by rate: two readers
// that both score 43/44 while missing DIFFERENT pairs are not the same reading.
func TestRLS_WiredPathReadsTheSameThroughBothReaders(t *testing.T) {
	ctx := t.Context()

	docling := cwScoreWired(t, ctx, "the shipped set through Docling", 918300, cwDocling, cwRankZeroOnly, nil)
	cwRequireWalk(t, docling, "the shipped set through Docling")
	pdfium := cwScoreWired(t, ctx, "the shipped set through PDFium", 918400, cwPDFium, cwRankZeroOnly, nil)
	cwRequireWalk(t, pdfium, "the shipped set through PDFium")

	if docling.hits != tier1DecisionHits {
		t.Errorf("Docling decides %d/%d through the wired path, want %d/%d", docling.hits, docling.total, tier1DecisionHits, tier1DecisionPairs)
	}
	if pdfium.hits != tier1DecisionHits {
		t.Errorf("PDFium decides %d/%d through the wired path, want %d/%d", pdfium.hits, pdfium.total, tier1DecisionHits, tier1DecisionPairs)
	}
	if docling.total != pdfium.total {
		t.Fatalf("Docling scored %d pair(s) and PDFium %d; comparing the two says nothing", docling.total, pdfium.total)
	}

	dm, pm := cwMissedSet(docling), cwMissedSet(pdfium)
	for p := range dm {
		if !pm[p] {
			t.Errorf("Docling misses %s / %s and PDFium reaches it; the two readers do not read this corpus alike", p.file, p.field)
		}
	}
	for p := range pm {
		if !dm[p] {
			t.Errorf("PDFium misses %s / %s and Docling reaches it; the two readers do not read this corpus alike", p.file, p.field)
		}
	}
}

// AC-5. The missed set compared to t1aGaps by identity, in BOTH directions. 43 == 43 also holds
// for a wired path that misses a different pair -- an exemption silently inherited -- so the
// count cannot tell "the same one gap" from "a new gap plus a new hit". Mirrors
// TestTier1Accuracy_TheMissedPairsAreExactlyTheRecordedGaps.
//
// The gap is corpus_two_column.pdf / buyer_tin, a Tier-1 REACH limit. FingerprintVersion is not
// what gates it: that bump invalidates stored learned rules. The lexicon widening that WOULD
// close it needs one (anchorLexicon is a Fingerprint input, fingerprint.go:55-62), which is a
// claim about one candidate remedy and not about the gap.
func TestRLS_WiredPathMissesExactlyTheRecordedGaps(t *testing.T) {
	ctx := t.Context()

	s := cwScoreWired(t, ctx, "the shipped Tier-1 set", 918500, cwDocling, cwRankZeroOnly, nil)
	cwRequireWalk(t, s, "the shipped Tier-1 set")

	if len(t1aGaps) == 0 {
		t.Fatal("t1aGaps is empty; both comparisons below would hold over nothing")
	}
	if len(s.missed) == 0 {
		t.Fatalf("the wired walk missed no pair while t1aGaps records %d; the walk is not scoring the decision", len(t1aGaps))
	}

	missed, recorded := cwMissedSet(s), cwGapSet()
	for p := range missed {
		if !recorded[p] {
			t.Errorf("the wired path misses %s / %s, which t1aGaps does not record; the wired walk and the in-process one disagree about WHICH pair is unreachable", p.file, p.field)
		}
	}
	for p := range recorded {
		if !missed[p] {
			t.Errorf("t1aGaps records %s / %s, which the wired path reaches; drop it from t1aGaps and raise the pinned hits in the same commit", p.file, p.field)
		}
	}
	if s.hits != tier1DecisionPairs-len(t1aGaps) {
		t.Errorf("%d hit(s) over %d pair(s) with %d recorded gap(s); the pinned hits, the pinned pairs and the exemption list cannot all be right", s.hits, s.total, len(t1aGaps))
	}
}

// AC-7. The denominator is asserted, never assumed: a measure whose denominator can drift
// flatters itself by losing pairs.
func TestRLS_WiredPathDenominatorIsTheCorpusDenominator(t *testing.T) {
	ctx := t.Context()

	if pairs := acCountPairs(); pairs != tier1DecisionPairs {
		t.Fatalf("corpusExpect names %d (layout, field) pair(s), want %d; the wired walk would be taken over the wrong table", pairs, tier1DecisionPairs)
	}

	s := cwScoreWired(t, ctx, "the shipped Tier-1 set", 918600, cwDocling, cwRankZeroOnly, nil)
	cwRequireWalk(t, s, "the shipped Tier-1 set")

	if s.total != tier1DecisionPairs {
		t.Fatalf("the wired walk scored %d pair(s), want %d; the rate would be taken over the wrong denominator", s.total, tier1DecisionPairs)
	}
	// line_items is a row on every layout and a member of no denominator.
	if slices.Contains(extraction.HeaderFields, cwLineItemsRow) {
		t.Fatalf("%s is a HeaderFields member; the block row would be scored as a field and this denominator is wrong", cwLineItemsRow)
	}
}

// AC-10. Zero learned rules is an ASSERTED PRECONDITION of the baseline, not an assumption. A
// fresh tenant has none, but EXTR-14 writes rules keyed by (tenant, fingerprint) and a layout
// run in a tenant that has already learned one scores a different number. Without this the
// baseline and AC-9's control can silently converge.
//
// The asserted zero carries its positive control in the same spec: a seam that served nothing
// to everyone would satisfy the zero half alone.
func TestRLS_WiredPathBaselineServesNoLearnedRule(t *testing.T) {
	ctx := t.Context()

	base := cwScoreWired(t, ctx, "the shipped Tier-1 set", 918700, cwDocling, cwRankZeroOnly, nil)
	cwRequireWalk(t, base, "the shipped Tier-1 set")

	for _, r := range base.runs {
		if r.learned != 0 {
			t.Errorf("%s was served %d learned rule(s) on the baseline run; the wired rate would not be the shipped Tier-1 set's", r.layout, r.learned)
		}
	}

	seeded := cwScoreWired(t, ctx, "the shipped set plus one learned rule", 918800, cwDocling, cwRankZeroOnly, cwSeedDecoy)
	cwRequireWalk(t, seeded, "the shipped set plus one learned rule")
	if n := seeded.run(t, cwDecoyLayout).learned; n != 1 {
		t.Errorf("the seeded run served %s %d learned rule(s), want 1; the zero above would hold on a seam that served nothing to anyone", cwDecoyLayout, n)
	}
}

// cwSeedDecoy writes the ranking decoy for cwDecoyLayout's fingerprint only.
func cwSeedDecoy(t *testing.T, ctx context.Context, tenantID, layout, fingerprint string) {
	t.Helper()
	if layout != cwDecoyLayout {
		return
	}
	stSeedAnchorRule(t, ctx, tenantID, fingerprint, cwDecoyField, cwDecoyRule, extraction.RuleSchemaVersion)
}

// AC-8, AC-9. The metric must move when extraction gets worse, and it must move for a reason
// only a rank-reading measure can see. On this corpus the rank-0 rate and the any-rank rate
// BOTH read 43/44, so swapping the wired scorer from rank 0 to any rank survives every other
// spec in this file -- the EXTR-16 defect restated.
//
// The decoy is a LEARNED rule through the real rule store, so TierLearned < TierGeneric
// (resolve.go:17) puts it at rank 0 unconditionally; Tier1Rules stays untouched.
func TestRLS_WiredPathRankingDecoyMovesTheRate(t *testing.T) {
	ctx := t.Context()
	corpusRequireCommitted(t)

	// The decoy is keyed by fingerprint. Two layouts fingerprinting alike would poison both,
	// and the drop would be two pairs for a reason that is not the ranking.
	seen := map[string]string{}
	for _, name := range corpusLayouts {
		fp := extraction.Fingerprint(rvCorpusPages(t, name))
		if prev, ok := seen[fp]; ok {
			t.Fatalf("%s and %s share the fingerprint %s; the decoy below would reach both", prev, name, fp)
		}
		seen[fp] = name
	}
	if len(seen) != len(corpusLayouts) {
		t.Fatalf("%d distinct fingerprint(s) over %d layout(s)", len(seen), len(corpusLayouts))
	}

	base := cwScoreWired(t, ctx, "the shipped Tier-1 set", 918900, cwDocling, cwRankZeroOnly, nil)
	cwRequireWalk(t, base, "the shipped Tier-1 set")

	decoy := cwScoreWired(t, ctx, "the shipped set plus the learned ranking decoy", 919000, cwDocling, cwRankZeroOnly, cwSeedDecoy)
	cwRequireWalk(t, decoy, "the shipped set plus the learned ranking decoy")

	if decoy.total != base.total {
		t.Fatalf("the decoy run scored %d pair(s) and the baseline %d; comparing the two says nothing", decoy.total, base.total)
	}
	if decoy.hits != cwDecoyHits {
		t.Errorf("the decoy run decides %d/%d, want %d/%d; the decoy out-ranks exactly one reached value", decoy.hits, decoy.total, cwDecoyHits, tier1DecisionPairs)
	}
	if decoy.hits >= base.hits {
		t.Errorf("the decoy leaves the wired rate at %d/%d against the baseline %d/%d; the metric does not move when extraction gets worse", decoy.hits, decoy.total, base.hits, base.total)
	}

	// By identity, not by count: a drop of one somewhere else is a different fact.
	taken := acPair{file: cwDecoyLayout, field: cwDecoyField}
	if !cwMissedSet(decoy)[taken] {
		t.Errorf("%s / %s is still decided correctly under the decoy; the decoy never took the rank it exists to take", taken.file, taken.field)
	}
	if got := decoy.got[taken]; got != cwDecoyRank0 {
		t.Errorf("%s / %s decides %q under the decoy, want the decoy's own reading %q", taken.file, taken.field, got, cwDecoyRank0)
	}
	for p := range cwMissedSet(base) {
		if !cwMissedSet(decoy)[p] {
			t.Errorf("the baseline misses %s / %s and the decoy run reaches it; the decoy must only ADD a miss", p.file, p.field)
		}
	}

	// The row shape the drop is made of: the real total is still reached and still stored, one
	// rank down. Without this the drop is indistinguishable from a decoy that destroyed reach.
	run := decoy.run(t, cwDecoyLayout)
	var ranks []wpRow
	for _, row := range run.rows {
		if row.name == cwDecoyField {
			ranks = append(ranks, row)
		}
	}
	if len(ranks) < 2 {
		t.Fatalf("%s wrote %d %s row(s) under the decoy (%v), want the decoy at rank 0 and %s at rank 1; with one row the any-rank control below cannot discriminate",
			cwDecoyLayout, len(ranks), cwDecoyField, ranks, cwDecoyRank1)
	}
	wpAssertRankZero(t, run.rows, cwDecoyField, stPtr(cwDecoyRank0), stPtr("ambiguous"))
	found := false
	for _, row := range ranks {
		if row.rank == 1 && row.value != nil && *row.value == cwDecoyRank1 {
			found = true
		}
	}
	if !found {
		t.Errorf("%s / %s carries no rank-1 row holding %q (%v); Resolve still reaches the real total and the store must keep it", cwDecoyLayout, cwDecoyField, cwDecoyRank1, ranks)
	}

	// The discriminator. A wired scorer that accepted a match at ANY candidate_rank reads the
	// decoy run at 43/44 -- the shipped number -- and passes every other spec in this file.
	t.Run("any_rank_scorer_reads_a_different_number", func(t *testing.T) {
		anyRank := cwScoreWired(t, ctx, "the decoy run scored at any candidate_rank", 919100, cwDocling, cwAnyRank, cwSeedDecoy)
		cwRequireWalk(t, anyRank, "the decoy run scored at any candidate_rank")

		if anyRank.total != decoy.total {
			t.Fatalf("the any-rank walk scored %d pair(s) and the rank-0 walk %d; comparing them says nothing", anyRank.total, decoy.total)
		}
		if anyRank.hits <= decoy.hits {
			t.Errorf("scoring the decoy run at any candidate_rank reads %d/%d and at rank 0 reads %d/%d; the two are indistinguishable, so nothing in this file can fail when the wired scorer stops reading the decision",
				anyRank.hits, anyRank.total, decoy.hits, decoy.total)
		}
		if anyRank.hits != tier1DecisionHits {
			t.Errorf("the any-rank walk reads %d/%d, want the shipped %d/%d: the real total is at rank 1, so containment still finds it", anyRank.hits, anyRank.total, tier1DecisionHits, tier1DecisionPairs)
		}
	})
}

// --- the goldens the Docling run replays ---------------------------------------------------

// AC-3. All six corpus goldens are what the pinned sidecar really returned. A stub image answers
// /v1/read with docling_version "stub" and one "STUB" token and round-trips perfectly, so the
// version is asserted alongside the round trip rather than instead of it.
func TestCorpusGoldens_AreMachineGenerated(t *testing.T) {
	if len(corpusLayouts) == 0 {
		t.Fatal("corpusLayouts is empty; the loop below would assert nothing")
	}
	for _, layout := range corpusLayouts {
		name := cwGolden(layout)
		t.Run(name, func(t *testing.T) {
			golden := dcReadNamedGolden(t, name)

			dec := json.NewDecoder(bytes.NewReader(golden))
			dec.UseNumber()
			var doc any
			if err := dec.Decode(&doc); err != nil {
				t.Fatalf("decode %s: %v", name, err)
			}
			round, err := json.MarshalIndent(doc, "", "  ")
			if err != nil {
				t.Fatalf("re-serialise %s: %v", name, err)
			}
			round = append(round, '\n')
			if !bytes.Equal(round, golden) {
				t.Errorf("%s is not what `docling-canary.sh golden` writes (%d bytes re-serialised, %d committed); regenerate it from a freshly built image rather than editing it",
					name, len(round), len(golden))
			}

			obj, ok := doc.(map[string]any)
			if !ok {
				t.Fatalf("%s decodes to %T, want a JSON object", name, doc)
			}
			if v, _ := obj["docling_version"].(string); v == "" || v == "stub" {
				t.Errorf("%s reports docling_version %q; it was generated by a stub image, not the pinned sidecar", name, v)
			}
		})
	}
}

// AC-3, edge 5. Without both halves of every pdf+json pair in ci.yml's sidecar filter, a
// golden-only PR skips docling-canary and the roll-up counts a skipped job as a pass; without a
// `golden` invocation per layout the job runs and validates only the first one.
func TestCorpusGoldens_TheCanaryJobCoversEveryLayout(t *testing.T) {
	yaml := acRepoFile(t, ".github/workflows/ci.yml")

	// Control needle: a scan whose file moved reads as a clean workflow.
	if !strings.Contains(yaml, "docling-canary.sh") {
		t.Fatalf("ci.yml never names docling-canary.sh; this scan is reading the wrong file and every absence below is a false report")
	}
	for _, layout := range corpusLayouts {
		pdf := "internal/extraction/testdata/" + layout
		gold := "internal/extraction/testdata/" + cwGolden(layout)
		if !strings.Contains(yaml, pdf) {
			t.Errorf("ci.yml's sidecar path filter does not name %s; a PR touching it skips docling-canary and the roll-up reads the skip as a pass", pdf)
		}
		if !strings.Contains(yaml, gold) {
			t.Errorf("ci.yml's sidecar path filter does not name %s; a golden-only PR skips the job that validates it", gold)
		}
		if strings.Count(yaml, gold) < 2 {
			t.Errorf("ci.yml names %s once; it needs the filter entry AND a `docling-canary.sh golden` invocation, or the golden is never re-measured against the built image", gold)
		}
	}
}

// --- the report and the doc -----------------------------------------------------------------

// AC-13. rls-test-gate.sh pipes `go test -json` to a file (rls-test-gate.sh:8) and rlsgate
// deletes a passing test's buffered output (internal/tools/rlsgate/rlsgate.go:66), so the wired
// report never reaches CI output from the gated step that runs this package (ci.yml:641, the
// queue job -- the rls job runs no extraction step).
// Mirrors TestTier1Accuracy_CIPrintsTheReport.
func TestRLS_WiredPathCIPrintsTheReport(t *testing.T) {
	yaml := acRepoFile(t, ".github/workflows/ci.yml")

	// Control needle: a scan reading the wrong file finds no step either.
	if !strings.Contains(yaml, "rls-test-gate.sh -count=1 ./internal/extraction/...") {
		t.Fatalf("ci.yml never runs ./internal/extraction/... through rls-test-gate.sh; this scan is reading the wrong file and would report a missing step that is there")
	}

	step := cwCIStep(t, yaml)
	for _, want := range []struct{ needle, why string }{
		{"go test", "the step must actually run the suite"},
		{"./internal/extraction", "the step must name this package"},
		{"set -o pipefail", "ci.yml has no defaults.run.shell, so run: is bash -e and a `| tee` would mask a failed go test"},
		{cwReportMarker, "the step must grep for the wired report marker, or a broken -run filter prints nothing and still passes"},
	} {
		if !strings.Contains(step, want.needle) {
			t.Errorf("the wired report step does not carry %q: %s", want.needle, want.why)
		}
	}
	if strings.Contains(step, "rls-test-gate.sh") {
		t.Errorf("the wired report step runs through rls-test-gate.sh, which deletes a passing test's output; the report would never print")
	}
}

// cwCIStep is the ci.yml chunk starting at the step that prints the wired report. The chunk
// runs past the step when it is a job's last one; cwStepBody cuts it.
func cwCIStep(t *testing.T, yaml string) string {
	t.Helper()

	for _, chunk := range strings.Split(yaml, "\n      - ") {
		if strings.Contains(chunk, cwReportRunFilter) {
			return chunk
		}
	}
	t.Fatalf("no .github/workflows/ci.yml step runs `go test -run %s`; the gated step that runs this package discards a passing test's output, so without a step of its own the wired number never reaches CI output (AC-13)", cwReportRunFilter)
	return ""
}

// AC-13. A -run filter naming no test runs nothing and prints nothing, and the grep would then
// be the only thing that failed -- after a full workflow run. Mirrors
// TestTier1Accuracy_TheCIStepsRunFilterNamesARealTest.
func TestRLS_WiredPathTheCIStepsRunFilterNamesARealTest(t *testing.T) {
	step := cwCIStep(t, acRepoFile(t, ".github/workflows/ci.yml"))

	m := aaRunFilterRE.FindStringSubmatch(step)
	if m == nil {
		t.Fatalf("the wired report step carries no -run '<pattern>'; it would run the whole package and the report would be buried")
	}
	filter, err := regexp.Compile(m[1])
	if err != nil {
		t.Fatalf("the step's -run pattern %q does not compile: %v", m[1], err)
	}

	src := acRepoFile(t, "internal/extraction/corpus_wired_db_test.go")
	var declared, renders []string
	for _, chunk := range strings.Split(src, "\nfunc ") {
		d := aaTestFuncRE.FindStringSubmatch("\nfunc " + chunk)
		if d == nil {
			continue
		}
		declared = append(declared, d[1])
		if strings.Contains(chunk, "cwRenderReport(") {
			renders = append(renders, d[1])
		}
	}
	if len(declared) == 0 {
		t.Fatalf("no test declaration found in corpus_wired_db_test.go; this scan is reading the wrong file")
	}
	if len(renders) == 0 {
		t.Fatalf("no test in corpus_wired_db_test.go renders the wired report; the step would print nothing whatever its filter says")
	}

	matched := 0
	for _, name := range declared {
		if filter.MatchString(name) {
			matched++
		}
	}
	if matched == 0 {
		t.Errorf("the step's -run pattern %q matches none of the %d test(s) in corpus_wired_db_test.go; the step would run nothing and print nothing", m[1], len(declared))
	}
	for _, name := range renders {
		if !filter.MatchString(name) {
			t.Errorf("the step's -run pattern %q does not match %s, which is what renders the wired report; the grep would find no marker", m[1], name)
		}
	}
}

// AC-12. docs/extraction-corpus.md carries the wired number. This is a WEAK oracle and is
// recorded as one: it compares prose to a Go constant, so it fails when someone edits the doc,
// never when the wired measurement is wrong. The measurement's real reader is the ci.yml step
// TestRLS_WiredPathCIPrintsTheReport pins.
func TestCorpusDoc_RecordsTheWiredPathRate(t *testing.T) {
	section := acDocSectionText(t, acRepoFile(t, acDoc), cwDocSection)

	for _, want := range []string{
		fmt.Sprintf("%d of %d", tier1DecisionHits, tier1DecisionPairs),
		strconv.FormatFloat(tier1DecisionRate, 'f', 4, 64),
		fmt.Sprintf("%d of %d", cwDecoyHits, tier1DecisionPairs), // the decoy control
		"ExtractWorker.Work",
		"extraction_field_results",
		cwDecoyLayout,
	} {
		if !strings.Contains(section, want) {
			t.Errorf("%s's %q section does not carry %q", acDoc, cwDocSection, want)
		}
	}

	// The limit, stated rather than implied: what the wired number does NOT prove is what a
	// reader would otherwise assume it does.
	for _, phrase := range []string{"DoclingReader", "no honest oracle"} {
		if !strings.Contains(section, phrase) {
			t.Errorf("%s's %q section never mentions %q; the doc's own coverage limit is the part a reader cannot reconstruct", acDoc, cwDocSection, phrase)
		}
	}
}

// --- QA additions: what the sweep above found unguarded --------------------------------------

// cwGeometryTol bounds the disagreement between a golden's token boxes and pdfium's reading of
// the same PDF. Measured max over all six layouts: |dy0| 0.000576, |dy1| 0.005727,
// |dx0| 0.003569, |dx1| 0.003863.
const cwGeometryTol = 0.02

// AC-3. TestCorpusGoldens_AreMachineGenerated proves a golden round-trips and did not come from
// a stub image; neither notices a hand-edited coordinate. Shifting corpus_totals_block's
// Supplier TIN token from y0 0.14 to 0.84 passed the whole suite, because every anchor that
// reads it is same-token. A golden that no longer describes its PDF is a replay of nothing.
func TestCorpusGoldens_DescribeTheirPDF(t *testing.T) {
	if len(corpusLayouts) == 0 {
		t.Fatal("corpusLayouts is empty; the loop below would assert nothing")
	}
	for _, layout := range corpusLayouts {
		t.Run(layout, func(t *testing.T) {
			_, golden, _ := dcServeGolden(t, dcReadNamedGolden(t, cwGolden(layout)))
			pdfium := rvCorpusPages(t, layout)

			if len(golden) == 0 {
				t.Fatalf("%s replayed no page; every comparison below would hold vacuously", cwGolden(layout))
			}
			if len(golden) != len(pdfium) {
				t.Fatalf("%s describes %d page(s) and %s reads %d", cwGolden(layout), len(golden), layout, len(pdfium))
			}

			compared := 0
			for i := range golden {
				g, p := golden[i], pdfium[i]
				if g.WidthPt != p.WidthPt || g.HeightPt != p.HeightPt {
					t.Errorf("page %d is %vx%v pt in the golden and %vx%v pt in the PDF", i+1, g.WidthPt, g.HeightPt, p.WidthPt, p.HeightPt)
				}
				if len(g.Tokens) != len(p.Tokens) {
					t.Errorf("page %d carries %d golden token(s) and %d pdfium token(s); the golden was truncated or padded", i+1, len(g.Tokens), len(p.Tokens))
					continue
				}
				for j := range g.Tokens {
					gt, pt := g.Tokens[j], p.Tokens[j]
					compared++
					if strings.TrimSpace(gt.Text) != strings.TrimSpace(pt.Text) {
						t.Errorf("page %d token %d reads %q in the golden and %q in the PDF", i+1, j, gt.Text, pt.Text)
					}
					for _, d := range []struct {
						edge     string
						got, off float64
					}{
						{"x0", gt.Region.X0, gt.Region.X0 - pt.Region.X0},
						{"x1", gt.Region.X1, gt.Region.X1 - pt.Region.X1},
						{"y0", gt.Region.Y0, gt.Region.Y0 - pt.Region.Y0},
						{"y1", gt.Region.Y1, gt.Region.Y1 - pt.Region.Y1},
					} {
						if math.Abs(d.off) > cwGeometryTol {
							t.Errorf("page %d token %d (%q) has %s %v in the golden, %v off pdfium's; the golden no longer describes this PDF",
								i+1, j, gt.Text, d.edge, d.got, d.off)
						}
					}
				}
			}
			if compared == 0 {
				t.Fatalf("%s compared no token; the geometry assertions above held over nothing", cwGolden(layout))
			}
		})
	}
}

// AC-11. cwScoreWired scopes its walk to HeaderFields, and the scope is equivalent to walking
// every row name only while no non-HeaderFields row carries a value. That premise is what this
// pins: LineItems finds nothing on any corpus layout (every golden carries `tables: []`), so the
// block row is written missing-and-empty. A seventh layout with a table turns this red, which is
// where the scope stops being free.
func TestRLS_WiredPathTheBlockRowCannotEnterTheWalk(t *testing.T) {
	ctx := t.Context()
	corpusRequireCommitted(t)

	if slices.Contains(extraction.HeaderFields, cwLineItemsRow) {
		t.Fatalf("%s is a HeaderFields member; the walk's scope no longer excludes it", cwLineItemsRow)
	}
	for _, layout := range corpusLayouts {
		pages, _, _ := dcServeGolden(t, dcReadNamedGolden(t, cwGolden(layout)))
		if n := len(extraction.LineItems(pages)); n != 0 {
			t.Errorf("%s yields %d line item(s); the block row can now carry a value, so scoring every row name instead of HeaderFields would inflate the decided count", layout, n)
		}
	}
	// No committed golden carries a table, so the zeros above need their own positive control:
	// without one they would also hold if LineItems always returned nothing.
	if n := len(extraction.LineItems([]extraction.Page{{Number: 1, Tables: []extraction.Table{{
		Rows: 2, Cols: 3,
		Cells: []extraction.TableCell{
			liCell(0, 0, "Qty", nil), liCell(0, 1, "Price", nil), liCell(0, 2, "Total", nil),
			liCell(1, 0, "1", nil), liCell(1, 1, "5.00", nil), liCell(1, 2, "5.00", nil),
		},
	}}}})); n != 1 {
		t.Fatalf("LineItems found %d line(s) on a one-row table; the zeros above hold whatever the corpus contains", n)
	}

	s := cwScoreWired(t, ctx, "the shipped Tier-1 set", 919200, cwDocling, cwRankZeroOnly, nil)
	cwRequireWalk(t, s, "the shipped Tier-1 set")

	header := map[string]bool{}
	for _, f := range extraction.HeaderFields {
		header[f] = true
	}
	valued := 0
	for _, r := range s.runs {
		block := false
		for _, row := range r.rows {
			if row.rank != 0 {
				continue
			}
			if row.name == cwLineItemsRow {
				block = true
			}
			if row.value == nil {
				continue
			}
			valued++
			if !header[row.name] {
				t.Errorf("%s wrote a rank-0 %s row carrying %q; it is not a HeaderFields member, so an unscoped walk would count it decided", r.layout, row.name, *row.value)
			}
		}
		if !block {
			t.Errorf("%s wrote no rank-0 %s row; this spec is not looking at the rows Reconcile writes", r.layout, cwLineItemsRow)
		}
	}
	if valued == 0 {
		t.Fatal("no run carried a rank-0 value at all; the membership assertion above held over nothing")
	}
}

// cwStepBody cuts a cwCIStep chunk at the end of its step. cwCIStep splits on the step marker,
// so the LAST step of a job runs on into the next job's header and its steps -- every needle
// asserted against the raw chunk can be satisfied by an unrelated job.
func cwStepBody(chunk string) string {
	lines := strings.Split(chunk, "\n")
	for i, l := range lines[1:] {
		if strings.TrimSpace(l) == "" {
			continue
		}
		if len(l)-len(strings.TrimLeft(l, " ")) < 8 {
			return strings.Join(lines[:i+1], "\n")
		}
	}
	return chunk
}

// AC-13. cwCIStep finds the step by substring, so it cannot tell a step that RUNS from one that
// is gated off, mistyped, or whose grep was neutered. Measured survivors of the shipped specs:
// `if: false` on the step, `run:` renamed to `run2:`, and `grep ... accuracy.txt || true`. Each
// leaves the wired number with no reader while both AC-13 specs stay green.
func TestRLS_WiredPathTheReportStepActuallyRuns(t *testing.T) {
	yaml := acRepoFile(t, ".github/workflows/ci.yml")
	step := cwStepBody(cwCIStep(t, yaml))

	if !regexp.MustCompile(`(?m)^\s+run: \|`).MatchString(step) {
		t.Errorf("the wired report step has no `run: |` block; a mistyped key makes it a no-op step that still carries every string the other specs grep for")
	}
	if m := regexp.MustCompile(`(?m)^\s+if:.*$`).FindString(step); m != "" {
		t.Errorf("the wired report step is conditional (%q); a step gated off never prints the report, and the substring scans cannot see the difference", strings.TrimSpace(m))
	}
	if strings.Contains(step, "continue-on-error") {
		t.Errorf("the wired report step is continue-on-error; a failed grep would no longer fail the job")
	}

	// The grep must guard the report, and must read what the go test actually wrote.
	grep := regexp.MustCompile(`grep -q '` + regexp.QuoteMeta(cwReportMarker) + `' (\S+)\s*(\S*)`).FindStringSubmatch(step)
	if grep == nil {
		t.Fatalf("the wired report step has no `grep -q '%s' <file>`; without it the step passes on output that says nothing", cwReportMarker)
	}
	if grep[2] != "" {
		t.Errorf("the grep is followed by %q; `|| true` and friends turn the only reader-side assertion into a no-op", grep[2])
	}
	tee := regexp.MustCompile(`\| tee (\S+)`).FindStringSubmatch(step)
	if tee == nil {
		t.Fatalf("the wired report step does not tee the go test output; there is nothing for the grep to read")
	}
	if tee[1] != grep[1] {
		t.Errorf("the step tees into %s and greps %s; the grep would read another job's file, or none", tee[1], grep[1])
	}

	// Same job as the gated run, so it inherits this job's DATABASE_* env. Without them every
	// TestRLS_* self-skips, the report never renders and the grep fails for the wrong reason.
	gated := strings.Index(yaml, "rls-test-gate.sh -count=1 ./internal/extraction/...")
	report := strings.Index(yaml, step)
	if gated < 0 || report < 0 {
		t.Fatalf("could not locate both steps in ci.yml (gated %d, report %d); this scan is reading the wrong file", gated, report)
	}
	if between := regexp.MustCompile(`(?m)^  [A-Za-z][\w-]*:$`).FindString(yaml[gated:report]); between != "" {
		t.Errorf("a new job (%q) starts between the gated extraction run and the report step; the report step would not inherit the DATABASE_* env and every TestRLS_* would self-skip", strings.TrimSpace(between))
	}
}

// --- EXTR-18-02: the rich fixture's golden, read through the wired path -------------------

// rfGolden and rfFixture are the rich invoice's committed inputs. rfGolden is untracked until
// this subtask's executor commits it, and neither is wired into ci.yml yet -- see
// TestGoldens_EveryCommittedGoldenHasACanaryStep.
const (
	rfGolden  = "rich_invoice.docling.json"
	rfFixture = "rich_invoice.pdf"
)

// rfRiverJobID is a literal distinct from cwScoreWired's corpus-loop range (918200-918544+44).
const rfRiverJobID = 918900

// rfInvoiceNumber, rfTableRows, rfTableCols are fxBuildRichInvoice's own literals -- the
// header row plus Widget/Gadget/Delivery in a 4-column table (fixtures_test.go).
const (
	rfInvoiceNumber = "ASC-2026-0918"
	rfTableRows     = 4
	rfTableCols     = 4
)

// rfRun drives ExtractWorker.Work once over the rich fixture through a real DoclingReader
// replaying rfGolden, and returns every extraction_field_results row it wrote. Each call gets
// its own fresh tenant via wkFixture, so calling it from more than one test cannot collide on
// rfRiverJobID.
func rfRun(t *testing.T) []wpRow {
	t.Helper()
	ctx := t.Context()

	tenantID, documentID := wkFixture(t, ctx)
	text := wpDoclingReader(t, dcReadNamedGolden(t, rfGolden))
	rules := wpStoreRules(t)
	ew := wpWorker(t, wkOK(), &wkOpener{body: fxRead(t, rfFixture)}, text, rules.load, &wkAuditRecorder{})

	if err := ew.Work(ctx, extraction.NewExtractJobForTest(rfRiverJobID, 1, 3, tenantID, documentID, uuid.NewString())); err != nil {
		t.Fatalf("Work over %s: %v", rfFixture, err)
	}
	return wpResults(t, ctx, wkExtractionJobID(t, ctx, tenantID, rfRiverJobID))
}

// Story AC #3. Every reason the rich fixture's wired path can emit, by field name and value --
// never by count. Measured through a real docling:canary run (task-846's Implementation Plan),
// not predicted from the story text.
func TestRLS_RichFixtureResolvesEveryReasonTheWiredPathCanEmit(t *testing.T) {
	rows := rfRun(t)

	wpAssertRankZero(t, rows, "issue_date", stPtr("2026-03-12"), stPtr("ambiguous"))
	var issueDateRows int
	for _, r := range rows {
		if r.name == "issue_date" {
			issueDateRows++
		}
	}
	if issueDateRows < 2 {
		t.Errorf("issue_date carries %d row(s), want at least 2 (rank 0 plus >=1 alternative) for an %q verdict", issueDateRows, "ambiguous")
	}

	wpAssertRankZero(t, rows, "subtotal", stPtr("1500.00"), stPtr("inconsistent"))

	// line_items[2] is the Gadget row -- confirmed against the golden, not an off-by-one guess.
	wpAssertRankZero(t, rows, extraction.LineFieldName(2, extraction.LineRoleLineTotal), stPtr("900.00"), stPtr("inconsistent"))
}

// Story AC #3. At least two line_items[N] rows, each carrying a named description and
// line_total -- never a bare count.
func TestRLS_RichFixtureCarriesAtLeastTwoLineRows(t *testing.T) {
	rows := rfRun(t)

	indices := map[int]bool{}
	for _, r := range rows {
		if r.rank != 0 {
			continue
		}
		idx, _, ok := extraction.ParseLineFieldName(r.name)
		if !ok {
			continue
		}
		indices[idx] = true
	}
	if len(indices) < 2 {
		t.Fatalf("found %d distinct line_items[N] index(es), want at least 2: %v", len(indices), indices)
	}

	for idx := range indices {
		desc := wpRankZero(t, rows, extraction.LineFieldName(idx, extraction.LineRoleDescription))
		if desc.value == nil || *desc.value == "" {
			t.Errorf("line_items[%d].description carries no value", idx)
		}
		total := wpRankZero(t, rows, extraction.LineFieldName(idx, extraction.LineRoleLineTotal))
		if total.value == nil || *total.value == "" {
			t.Errorf("line_items[%d].line_total carries no value", idx)
		}
	}
}

// Story AC #4. The printed invoice number, decided with reason SQL NULL -- store.go:150-151
// binds a decided field's reason_code as NULL, never "".
func TestRLS_RichFixtureInvoiceNumberIsNotINV001(t *testing.T) {
	rows := rfRun(t)

	wpAssertRankZero(t, rows, "invoice_number", stPtr("ASC-2026-0918"), nil)
}

// Story AC #5. No document_text_layer verdict -- the rich fixture carries a real text layer.
// Paired with a positive control (subtotal IS present) so this cannot pass by writing zero rows.
func TestRLS_RichFixtureEmitsNoTextLayerVerdict(t *testing.T) {
	rows := rfRun(t)

	names := wpRankZeroNames(rows)
	if !slices.Contains(names, "subtotal") {
		t.Fatalf("no rank-0 subtotal row among %v; the positive control failed, so the absence check below proves nothing", names)
	}
	if slices.Contains(names, "document_text_layer") {
		t.Errorf("a rank-0 document_text_layer row is present among %v; the rich fixture has a text layer and should never take that branch", names)
	}
}

// EXTR-18-02 adversarial coverage: the golden is genuine, and stays honest about its PDF.

// TestDoclingGolden_RichInvoiceIsMachineGenerated mirrors TestCorpusGoldens_AreMachineGenerated
// for rich_invoice.docling.json, which that loop does not cover (rich_invoice is deliberately
// excluded from corpusLayouts).
func TestDoclingGolden_RichInvoiceIsMachineGenerated(t *testing.T) {
	golden := dcReadNamedGolden(t, rfGolden)

	dec := json.NewDecoder(bytes.NewReader(golden))
	dec.UseNumber()
	var doc any
	if err := dec.Decode(&doc); err != nil {
		t.Fatalf("decode %s: %v", rfGolden, err)
	}
	round, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		t.Fatalf("re-serialise %s: %v", rfGolden, err)
	}
	round = append(round, '\n')
	if !bytes.Equal(round, golden) {
		t.Errorf("%s is not what `docling-canary.sh golden` writes (%d bytes re-serialised, %d committed); regenerate it from a freshly built image rather than editing it",
			rfGolden, len(round), len(golden))
	}

	obj, ok := doc.(map[string]any)
	if !ok {
		t.Fatalf("%s decodes to %T, want a JSON object", rfGolden, doc)
	}
	if v, _ := obj["docling_version"].(string); v == "" || v == "stub" {
		t.Errorf("%s reports docling_version %q; it was generated by a stub image, not the pinned sidecar", rfGolden, v)
	}
}

// TestDoclingGolden_RichInvoiceMatchesItsPDFsPrintedNumber is the local drift guard: nothing
// else in this package ties the committed golden to the committed PDF, so a regenerated PDF
// with a stale golden (or the reverse) passes every other test here. This reads both sources
// independently -- the PDF's own content stream (the same path
// TestFixtures_RichInvoicePrintsItsOwnNumber uses) and the golden's tokens, through a real
// DoclingReader replay -- and fails if they disagree. It cannot reproduce what TableFormer
// would read off a changed PDF, so it pins only the table's row/col shape as a coarse floor;
// CI's `docling-canary.sh golden` step is the only oracle for finer drift (cell text, boxes).
func TestDoclingGolden_RichInvoiceMatchesItsPDFsPrintedNumber(t *testing.T) {
	raw := fxRead(t, fxRich)
	objs := fxObjects(raw)
	pages := fxPages(t, objs)
	if len(pages) < 1 {
		t.Fatalf("found %d page object(s) in %s, want at least 1", len(pages), fxRich)
	}
	body := fxContent(t, objs, pages[0])
	if !bytes.Contains(body, []byte(rfInvoiceNumber)) {
		t.Fatalf("%s's own content stream does not carry %q; this test's oracle is broken, not the golden", fxRich, rfInvoiceNumber)
	}

	gPages, gTokens, _ := dcServeGolden(t, dcReadNamedGolden(t, rfGolden))
	if len(gPages) != 1 {
		t.Fatalf("%s carries %d page(s), want exactly 1", rfGolden, len(gPages))
	}

	var found bool
	for _, p := range gTokens {
		for _, tok := range p.Tokens {
			if strings.Contains(tok.Text, rfInvoiceNumber) {
				found = true
			}
		}
	}
	if !found {
		t.Errorf("%s carries no token containing %q, the number %s's own content stream prints; the golden is stale relative to the PDF", rfGolden, rfInvoiceNumber, fxRich)
	}

	var tables int
	for _, p := range gPages {
		tables += len(p.Tables)
		for _, tb := range p.Tables {
			if tb.Rows != rfTableRows || tb.Cols != rfTableCols {
				t.Errorf("%s's table is %dx%d, want %dx%d (fxBuildRichInvoice's header row plus 3 data rows)", rfGolden, tb.Rows, tb.Cols, rfTableRows, rfTableCols)
			}
		}
	}
	if tables != 1 {
		t.Fatalf("%s carries %d table(s), want exactly 1", rfGolden, tables)
	}
}

// --- EXTR-18-03: the no-recoverable-text fixture, wired through the real reader -----------

// scanned_invoice.pdf is a 4x4 checkerboard with no real glyphs and cannot serve OCR testing
// (sidecar/docling/tests/test_fixtures.py:9-10, test_convert.py:5-6,
// testdata/gen_scanned_ocr_fixture.py:7-8). The specs below pin "no recoverable text", never
// "OCR ran and found nothing".
const (
	sfGolden     = "scanned_invoice.docling.json"
	sfRiverJobID = 919300 // distinct from every other literal in this file
	sfPagesJobID = sfRiverJobID + 1
)

// Story AC #4. TextChars == 0 takes worker.go's wholesale-replacement branch (:196-202), which
// writes ONE row and nothing else -- asserted as exactly one, not "at least one", because that
// branch's whole claim is singularity.
func TestRLS_ScannedFixtureSettlesTheTextLayerUnreadable(t *testing.T) {
	ctx := t.Context()
	tenantID, documentID := wkFixture(t, ctx)

	text := wpDoclingReader(t, dcReadNamedGolden(t, sfGolden))
	rules := wpStoreRules(t)
	ew := wpWorker(t, wkOK(), &wkOpener{body: fxRead(t, fxScanned)}, text, rules.load, &wkAuditRecorder{})

	if err := ew.Work(ctx, extraction.NewExtractJobForTest(sfRiverJobID, 1, 3, tenantID, documentID, uuid.NewString())); err != nil {
		t.Fatalf("Work: %v", err)
	}

	xid := wkExtractionJobID(t, ctx, tenantID, sfRiverJobID)
	stAssertJobState(t, ctx, xid, "succeeded")

	rows := wpResults(t, ctx, xid)
	if len(rows) != 1 {
		t.Fatalf("%s wrote %d row(s), want exactly 1: %v", fxScanned, len(rows), rows)
	}
	wpAssertRankZero(t, rows, "document_text_layer", nil, stPtr("unreadable"))
}

// Story AC #4's other half. The unreadable verdict is a text-layer reading, not a page-render
// failure, so the page sink must still record a PUT -- what distinguishes this from
// pages_not_rendered. wpWorker builds its own sink and never exposes it, so this drives
// wkWorkerPages directly with a sink the test can read back.
func TestRLS_ScannedFixturePagesStillRender(t *testing.T) {
	ctx := t.Context()
	tenantID, documentID := wkFixture(t, ctx)

	sink := &wkPageSink{}
	ew := wkWorkerPages(t, wkOK(), &wkOpener{body: fxRead(t, fxScanned)}, wkPDFiumPages(sink), &wkAuditRecorder{})
	ew.Text = wpDoclingReader(t, dcReadNamedGolden(t, sfGolden))
	ew.Rules = wpStoreRules(t).load

	if err := ew.Work(ctx, extraction.NewExtractJobForTest(sfPagesJobID, 1, 3, tenantID, documentID, uuid.NewString())); err != nil {
		t.Fatalf("Work: %v", err)
	}

	if _, _, calls := sink.snapshot(); calls < 1 {
		t.Errorf("the page sink recorded %d call(s) for %s, want at least 1 -- an unreadable text verdict must not mean the pages never rendered", calls, fxScanned)
	}
}

// --- EXTR-18-04: the DOCX fixture, wired through the real reader --------------------------

// invoice.docx has no page image PDFium (or any renderer) can produce, so the wired run swaps
// PageStore.Reader for a stub yielding one synthetic page and never reaches worker.go's
// pages_not_rendered gate -- TestExtractWorker_PagesNotRenderedGateIsUntouched
// (worker_internal_test.go) pins that this subtask leaves that gate untouched.
const (
	dxFixture    = "invoice.docx"
	dxGolden     = "invoice.docling.json"
	dxRiverJobID = 919400 // distinct from every other literal in this file
)

// Story AC #4. The DOCX fixture resolves invoice_number, issue_date and total by identity --
// measured through a real docling:canary run (task-848's Implementation Notes), never by count.
// reason_code is asserted nil: ReasonNone binds as SQL NULL, never "" (store.go:150-151).
func TestRLS_DocxFixtureResolvesNamedFields(t *testing.T) {
	ctx := t.Context()
	tenantID, documentID := wkFixture(t, ctx)

	pages := &extraction.PageStore{Reader: &wkStubReader{pages: 1}, Sink: (&wkPageSink{}).put}
	ew := wkWorkerPages(t, wkOK(), &wkOpener{body: fxRead(t, dxFixture)}, pages, &wkAuditRecorder{})
	ew.Text = wpDoclingReader(t, dcReadNamedGolden(t, dxGolden))
	ew.Rules = wpStoreRules(t).load

	if err := ew.Work(ctx, extraction.NewExtractJobForTest(dxRiverJobID, 1, 3, tenantID, documentID, uuid.NewString())); err != nil {
		t.Fatalf("Work over %s: %v", dxFixture, err)
	}

	xid := wkExtractionJobID(t, ctx, tenantID, dxRiverJobID)
	stAssertJobState(t, ctx, xid, "succeeded")

	rows := wpResults(t, ctx, xid)
	wpAssertRankZero(t, rows, "invoice_number", stPtr("ASC-2026-0919"), nil)
	wpAssertRankZero(t, rows, "issue_date", stPtr("2026-08-14"), nil)
	wpAssertRankZero(t, rows, "total", stPtr("4300.00"), nil)
}

// EXTR-18-04 adversarial coverage: the golden is genuine, and stays honestly DOCX-shaped.
// Mirrors TestDoclingGolden_RichInvoiceIsMachineGenerated /
// TestDoclingGolden_RichInvoiceMatchesItsPDFsPrintedNumber (EXTR-18-02) -- invoice.docx sits
// outside corpusLayouts, so TestCorpusGoldens_AreMachineGenerated does not cover it either.

// dxParagraphs is build_invoice_docx.py's PARAGRAPHS, transcribed. Nothing else in this package
// ties the committed golden to the committed .docx -- without this, a regenerated .docx with a
// stale golden (or the reverse) passes every other test here.
var dxParagraphs = []string{
	"Invoice No: ASC-2026-0919",
	"Issue Date: 14 Aug 2026",
	"Total: NGN 4,300.00",
}

// dxDocumentXML reads word/document.xml out of a .docx's own zip container. None of
// dxParagraphs needs XML escaping (no <, >, &, ', "), so a python-docx single-run paragraph's
// text survives as a literal byte substring -- the same coarse-floor idiom fxContent uses for a
// PDF's content stream.
func dxDocumentXML(t *testing.T, raw []byte) string {
	t.Helper()
	zr, err := zip.NewReader(bytes.NewReader(raw), int64(len(raw)))
	if err != nil {
		t.Fatalf("open %s as a zip: %v", dxFixture, err)
	}
	f, err := zr.Open("word/document.xml")
	if err != nil {
		t.Fatalf("%s carries no word/document.xml: %v", dxFixture, err)
	}
	defer f.Close()
	body, err := io.ReadAll(f)
	if err != nil {
		t.Fatalf("read word/document.xml: %v", err)
	}
	return string(body)
}

// TestDoclingGolden_InvoiceDocxIsMachineGenerated mirrors
// TestDoclingGolden_RichInvoiceIsMachineGenerated for invoice.docling.json.
func TestDoclingGolden_InvoiceDocxIsMachineGenerated(t *testing.T) {
	golden := dcReadNamedGolden(t, dxGolden)

	dec := json.NewDecoder(bytes.NewReader(golden))
	dec.UseNumber()
	var doc any
	if err := dec.Decode(&doc); err != nil {
		t.Fatalf("decode %s: %v", dxGolden, err)
	}
	round, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		t.Fatalf("re-serialise %s: %v", dxGolden, err)
	}
	round = append(round, '\n')
	if !bytes.Equal(round, golden) {
		t.Errorf("%s is not what `docling-canary.sh golden` writes (%d bytes re-serialised, %d committed); regenerate it from a freshly built image rather than editing it",
			dxGolden, len(round), len(golden))
	}

	obj, ok := doc.(map[string]any)
	if !ok {
		t.Fatalf("%s decodes to %T, want a JSON object", dxGolden, doc)
	}
	if v, _ := obj["docling_version"].(string); v == "" || v == "stub" {
		t.Errorf("%s reports docling_version %q; it was generated by a stub image, not the pinned sidecar", dxGolden, v)
	}
}

// TestDoclingGolden_InvoiceDocxMatchesItsFixturesParagraphs is the local drift guard, and also
// pins the golden as a genuine DOCX reading: docling-canary.sh sends `Content-Type:
// application/pdf` on every call regardless of the fixture's real type
// (dcServeGolden/docling_align_test.go does the same in this suite), so nothing else here
// distinguishes a DOCX golden from a PDF-shaped one committed under the wrong name.
func TestDoclingGolden_InvoiceDocxMatchesItsFixturesParagraphs(t *testing.T) {
	raw := fxRead(t, dxFixture)
	xmlBody := dxDocumentXML(t, raw)
	for _, p := range dxParagraphs {
		if !strings.Contains(xmlBody, p) {
			t.Fatalf("%s's own word/document.xml does not carry %q; this test's oracle is broken, not the golden", dxFixture, p)
		}
	}

	gPages, _, _ := dcServeGolden(t, dcReadNamedGolden(t, dxGolden))
	if len(gPages) != 1 {
		t.Fatalf("%s carries %d page(s), want exactly 1", dxGolden, len(gPages))
	}
	page := gPages[0]

	if len(page.Tables) != 0 {
		t.Errorf("%s carries %d table(s), want 0 -- a DOCX reading of three plain paragraphs should extract no table structure; a non-zero count is what a PDF-shaped golden committed under this name would look like", dxGolden, len(page.Tables))
	}

	if len(page.Tokens) != len(dxParagraphs) {
		t.Fatalf("%s carries %d token(s), want exactly %d -- docling's DOCX path emits one token per paragraph, no word-split", dxGolden, len(page.Tokens), len(dxParagraphs))
	}
	for _, p := range dxParagraphs {
		var found bool
		for _, tok := range page.Tokens {
			if tok.Text == p {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("%s carries no token equal to %q, one of invoice.docx's own paragraphs; the golden is stale relative to the fixture", dxGolden, p)
		}
	}
}

// corpus_wired_db_test.go: the golden corpus measured through ExtractWorker.Work rather than
// through Resolve + Reconcile in process. Same 44 pairs, same denominator; what it adds is the
// store round trip, the fingerprint hoist, the learned-rule lookup and the rank-0 encoding in
// extraction_field_results.
//
// Package extraction_test, so it shares store_db_test.go's TestMain, per-role pools and single
// skip site.
package extraction_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/SimonOsipov/invoice-os/internal/extraction"
)

// --- what the wired walk is measured against ---------------------------------------------

// cwReportMarker titles the wired report. The ci.yml step greps for it and
// TestRLS_WiredPath_TheCIStepsRunFilterNamesARealTest asserts the step carries it, so a rename
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
//
// NOT IMPLEMENTED: EXTR-17-04 Stage 2.5 authors the assertions, Stage 3 authors this walk.
// wpDoclingReader already replays arbitrary golden bytes and wpWorker already takes any reader;
// only wpCorpusOpener (worker_pipeline_db_test.go:122-125) is pinned to one fixture, so pass
// &wkOpener{body: fxRead(t, layout)} rather than changing it.
func cwScoreWired(t *testing.T, ctx context.Context, what string, base int64, reader cwReaderKind, mode cwRankMode, seed cwSeeder) cwScore {
	t.Helper()
	_ = ctx
	_ = base
	_ = reader
	_ = seed
	t.Fatalf("cwScoreWired is not implemented, so %q was never measured through %s: Work must run once per corpusExpect layout on its own tenant, and the rows read back from extraction_field_results scored against corpusExpect over HeaderFields at %s",
		what, reader, mode)
	return cwScore{}
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
// report never reaches CI output from the gated step that runs this package (ci.yml:606).
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

// cwCIStep is the ci.yml step that prints the wired report, cut on the workflow's own step
// indentation so the assertions above hold against ONE step rather than three unrelated ones.
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

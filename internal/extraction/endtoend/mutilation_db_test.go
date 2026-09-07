// mutilation_db_test.go: the two deliberate breaks that must move the end-to-end number, and
// the spec that fails if the number could be satisfied by recall alone.
//
// The CUT drops every invoice_number Tier-1 rule. Every layout then quarantines and the score
// falls to 0 while most values stay reachable, so one test holds both halves of the gap the
// number exists to show: "the rules still reach it" and "no invoice was written".
//
// The DECOY seeds a learned rule that out-ranks one layout's real total. The value is still
// written, one rank down, so only a rank-reading measure sees the loss -- the EXTR-16 defect
// restated at this altitude.
//
// The Tier-1 set is not injectable: worker.go:249 reads the package var directly, and
// ExtractWorker has no Tier1 field. So the cut installs a filtered struct COPY into
// extraction.Tier1Rules and restores it. Zero shipped bytes change, and
// TestEndToEnd_TheVariantsNeverMutateTheShippedRules is the guard that says so.
package endtoend

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/SimonOsipov/invoice-os/internal/extraction"
)

// The cut's three pinned integers. Three, not one: a lexicon change that removes a different
// number of rules must fail here, naming the cut, rather than mutilate a different field
// silently.
const (
	eeShippedRules = 34                            // extraction.Tier1Rules: 10 specs x 3 relations + bare_tin's 3 + 1 party-scoped TIN sweep
	eeCutRemoved   = 3                             // every invoice_number rule
	eeCutRules     = eeShippedRules - eeCutRemoved // 31
	eeCutField     = "invoice_number"
	// Over eeCorpusCells. An empty invoice number is refused twice -- documentCreateInput
	// (importer/document.go) and Store.Create (invoice/store.go:159) -- so this suite scores the
	// outcome, no invoices row, and cannot say which guard answered.
	eeCutScore = 0

	// eeCutReach is how many of the same eeCorpusCells cells a value is still REACHABLE for under
	// the cut -- Resolve's candidate list, no database. The gap between it and eeCutScore is what
	// separates this number from a recall measure.
	//
	// Not to be confused with the 37/44 the story cites: that is acMutilatedHits over
	// corpusExpect's 44 (layout, field) pairs across 10 vocabulary fields
	// (internal/extraction/accuracy_test.go:52). This suite scores eeCorpusCells cells over the 8 fields
	// the mapper writes. Different denominator, different table -- pinning 37 here would be a
	// false pin.
	eeCutReach = 44
)

// The reach decoy. The two-branch alternation is load-bearing, not cosmetic: decideField keeps
// an alternative only when it shares the head's Tier AND Distance, so a decoy matching only
// 5,000.00 leaves the TierGeneric 5375.00 with no row at all and an any-rank read sees the same
// loss a rank-0 read does. Matching both amounts ties them at TierLearned/Distance 0, and
// compareRegions hands rank 0 to the higher token (Sub-total) and rank 1 to the real total.
const (
	eeDecoyLayout = "corpus_totals_block.pdf" // expectByLayout[5]
	eeDecoyField  = "total"
	eeDecoyRule   = `{"label":"^\\s*5,(000|375)\\.00\\s*$","relation":{"kind":"same_token","max_distance":0},"shape":"amount"}`
	eeDecoyRank0  = "5000.00" // the Sub-total amount, filed under total
	eeDecoyRank1  = "5375.00" // the layout's real total, still reached, one rank down
	eeDecoyRows   = 2

	// eeDecoyBaseHits is what eeDecoyLayout scores with no decoy, under BOTH reads. The decoy
	// run must score exactly one less.
	eeDecoyBaseHits = 4
)

// --- the variants ------------------------------------------------------------------------

// eeRulesWithout is the pure half of the cut: a fresh struct-copy slice minus every rule naming
// field, plus the keys it dropped. Never a reslice of rules -- an aliased backing array turns a
// filter into an in-place edit of the shipped set.
func eeRulesWithout(rules []extraction.Tier1Rule, field string) (kept []extraction.Tier1Rule, removed []string) {
	kept = make([]extraction.Tier1Rule, 0, len(rules))
	for _, r := range rules {
		if r.Field == field {
			removed = append(removed, r.Key)
			continue
		}
		kept = append(kept, r)
	}
	return kept, removed
}

// eeCutRuleSet is the shipped set minus every eeCutField rule: a filtered struct copy, never a
// mutation. It asserts all three pinned integers BEFORE returning, so an ambiguous needle
// cannot patch a different site and still report a cut.
func eeCutRuleSet(t *testing.T) []extraction.Tier1Rule {
	t.Helper()
	shipped := extraction.Tier1Rules
	if len(shipped) != eeShippedRules {
		t.Fatalf("extraction.Tier1Rules holds %d rule(s), pinned at %d -- the cut below would be taken out of a set nobody measured", len(shipped), eeShippedRules)
	}

	cut, _ := eeRulesWithout(shipped, eeCutField)

	if removed := len(shipped) - len(cut); removed != eeCutRemoved {
		t.Fatalf("the cut removed %d rule(s) for %q, pinned at %d -- the lexicon changed and this cut is now mutilating something other than what it names", removed, eeCutField, eeCutRemoved)
	}
	if len(cut) != eeCutRules {
		t.Fatalf("the cut leaves %d rule(s), pinned at %d", len(cut), eeCutRules)
	}
	for _, r := range cut {
		if r.Field == eeCutField {
			t.Fatalf("the cut still carries %s for %q; the filter did not fire", r.Key, eeCutField)
		}
	}
	return cut
}

// eeWithRuleSet installs rules for the duration of fn and restores the snapshot afterwards.
// The restore is a defer, so neither a panic nor a Fatalf inside fn can leak a mutilated global
// into another test. The Cleanup is the belt to that brace.
func eeWithRuleSet(t *testing.T, rules []extraction.Tier1Rule, fn func()) {
	t.Helper()
	saved := slices.Clone(extraction.Tier1Rules)
	restore := func() { extraction.Tier1Rules = saved }
	t.Cleanup(restore)
	defer restore()

	extraction.Tier1Rules = rules
	fn()
}

// eeTokenPages reads one layout's page text through the production reader.
func eeTokenPages(t *testing.T, layout string) []extraction.TokenPage {
	t.Helper()
	var pages []extraction.TokenPage
	doc := extraction.Document{Bytes: eeFixtureBytes(t, layout), ContentType: eeContentType}
	if _, err := extraction.NewPDFiumReader().Read(t.Context(), doc, extraction.CollectTokens(&pages)); err != nil {
		t.Fatalf("read %s with the PDFium reader: %v", layout, err)
	}
	if len(pages) == 0 {
		t.Fatalf("%s yielded no token page at all; anything resolved from here is resolved from nothing", layout)
	}
	return pages
}

// eeReachHits counts, over the SAME expectByLayout x writtenFields cells the end-to-end score
// walks, whether an expected value appears anywhere in Resolve's candidate list. In process, no
// database, no import hop. An empty expectation is never a hit, exactly as the invoice read
// scores it, so the two numbers are taken over the same denominator.
func eeReachHits(t *testing.T, rules []extraction.Tier1Rule) (hits, total int) {
	t.Helper()
	if len(rules) == 0 {
		t.Fatal("eeReachHits was handed an empty rule set; every cell below would be a miss for a reason that is not the cut")
	}
	for _, want := range expectByLayout {
		reached := map[string][]string{}
		for _, c := range extraction.Resolve(eeTokenPages(t, want.file), extraction.RuleSet{Tier1: rules}) {
			reached[c.Field] = append(reached[c.Field], c.Value)
		}
		for _, field := range writtenFields {
			total++
			values := want.fields[field]
			if len(values) == 0 {
				continue
			}
			for _, v := range values {
				if slices.Contains(reached[field], v) {
					hits++
					break
				}
			}
		}
	}
	return hits, total
}

// --- the decoy ---------------------------------------------------------------------------

// eeFingerprintOf computes the fingerprint the worker will store, through the production
// pipeline: PageStore.Ingest with a no-op sink writes nothing, then Fingerprint over the token
// pages it returns.
func eeFingerprintOf(t *testing.T, ctx context.Context, layout string) string {
	t.Helper()
	ps := &extraction.PageStore{
		Reader: extraction.NewPDFiumReader(),
		Sink:   func(context.Context, string, []byte) error { return nil },
	}
	doc := extraction.Document{Bytes: eeFixtureBytes(t, layout), ContentType: eeContentType}
	_, tokenPages, _, err := ps.Ingest(ctx, uuid.NewString(), doc)
	if err != nil {
		t.Fatalf("ingest %s to compute its fingerprint: %v", layout, err)
	}
	if len(tokenPages) == 0 {
		t.Fatalf("%s ingested to zero token page(s); its fingerprint would key on nothing", layout)
	}
	return extraction.Fingerprint(tokenPages)
}

// eeSeedDecoy writes one learned rule for (tenant, fingerprint) with appendAnchorRuleTx's own
// column list (anchor_store.go:96-105). That writer is unexported and its ForTest wrapper lives
// in package extraction's test build, so it is unreachable from here; the READ side still goes
// through the production AnchorRulesFor. tenants.id cascades, so eeSeed's teardown reaches
// this row.
func eeSeedDecoy(t *testing.T, ctx context.Context, tenantID, fingerprint, body string) string {
	t.Helper()
	if body == "" {
		t.Fatal("eeSeedDecoy was handed an empty rule body; a run against it measures the baseline")
	}
	var id string
	if err := eeRequire(t).super.QueryRow(ctx,
		`INSERT INTO extraction_anchor_rules
		     (tenant_id, layout_fingerprint, field_name, rule, rule_schema_version)
		 VALUES ($1, $2, $3, $4, $5)
		 RETURNING id`,
		tenantID, fingerprint, eeDecoyField, body, extraction.RuleSchemaVersion).Scan(&id); err != nil {
		t.Fatalf("seed the rank-1 decoy for fingerprint %s: %v", fingerprint, err)
	}
	if id == "" {
		t.Fatalf("the decoy insert returned an empty id; nothing was written, so a run against it measures the baseline")
	}
	return id
}

// eeDecoyRun is one end-to-end run of eeDecoyLayout, with the decoy either seeded or not.
type eeDecoyRun struct {
	decoyID      string            // "" on a baseline run
	wantFP       string            // computed in process, before the run
	fingerprint  string            // read back off extraction_jobs
	invoice      map[string]string // eeWrittenRow; nil means quarantined
	rows         []eeRow           // every extraction_field_results row, all ranks
	served       int               // AnchorRulesFor's own count for this tenant + fingerprint
	servedFields []string          // and the fields it served, so a right-count wrong-field seed fails
}

// eeRunDecoyLayout drives ONE layout end to end. It exists because eeRunLayout gives no hook
// between eeSeed and eeExtract, which is exactly where the rule must be written. Its own
// subtest, like eeRunLayout, so eeExtract's Stop cleanup fires before the next run enqueues.
// body is the learned rule to seed; "" is a baseline run with none.
func eeRunDecoyLayout(t *testing.T, ctx context.Context, body string) eeDecoyRun {
	t.Helper()
	eeRequireFixtures(t, []string{eeDecoyLayout})

	name := "baseline"
	switch body {
	case "":
	case eeDecoyRule:
		name = "decoy"
	default:
		name = "inert-decoy"
	}
	var out eeDecoyRun
	ok := t.Run(name, func(t *testing.T) {
		h := eeRequire(t)
		w := eeSeed(t, ctx, eeDecoyLayout)
		out.wantFP = eeFingerprintOf(t, ctx, eeDecoyLayout)

		if body != "" {
			out.decoyID = eeSeedDecoy(t, ctx, w.tenantID, out.wantFP, body)
		}

		// The production reader, not the insert, is what says the rule is servable.
		served, err := (&extraction.Store{Pool: h.app}).AnchorRulesFor(ctx, w.tenantID, out.wantFP)
		if err != nil {
			t.Fatalf("AnchorRulesFor(%s, %s): %v", w.tenantID, out.wantFP, err)
		}
		out.served = len(served)
		for _, r := range served {
			out.servedFields = append(out.servedFields, r.Field)
		}

		jobID := eeExtract(t, ctx, w, eeDecoyLayout)
		eeImport(t, ctx, w)

		if err := h.super.QueryRow(ctx,
			`SELECT coalesce(layout_fingerprint, '') FROM extraction_jobs WHERE id = $1`,
			jobID).Scan(&out.fingerprint); err != nil {
			t.Fatalf("read the layout_fingerprint job %s stored: %v", jobID, err)
		}
		out.invoice = eeWrittenRow(t, ctx, w.documentID)
		out.rows = eeFieldResults(t, ctx, jobID)
	})
	if !ok {
		t.Fatalf("the %s run for %s failed; anything scored from here measures the harness", name, eeDecoyLayout)
	}
	return out
}

// eeDecoyFieldRows returns one run's extraction_field_results rows for a field, in rank order.
func eeDecoyFieldRows(r eeDecoyRun, field string) []eeRow {
	var out []eeRow
	for _, row := range r.rows {
		if row.name == field {
			out = append(out, row)
		}
	}
	slices.SortFunc(out, func(a, b eeRow) int { return a.rank - b.rank })
	return out
}

// The two reads of ONE run. Both apply the same "an empty expectation is never a hit" rule and
// both are taken over writtenFields, so the ONLY difference between them is rank-0-via-the-
// invoice-row against any-rank-via-extraction_field_results.
func eeInvoiceReadHits(r eeDecoyRun, expect map[string][]string) (hits int, saw map[string]string) {
	saw = make(map[string]string, len(writtenFields))
	for _, field := range writtenFields {
		actual := r.invoice[field] // "" when there is no invoices row at all
		saw[field] = actual
		if actual != "" && slices.Contains(expect[field], actual) {
			hits++
		}
	}
	return hits, saw
}

func eeAnyRankHits(r eeDecoyRun, expect map[string][]string) (hits int, saw map[string][]string) {
	saw = make(map[string][]string, len(writtenFields))
	for _, row := range r.rows {
		if row.value == nil || !slices.Contains(writtenFields, row.name) {
			continue
		}
		saw[row.name] = append(saw[row.name], *row.value)
	}
	for _, field := range writtenFields {
		if len(expect[field]) == 0 {
			continue
		}
		for _, v := range saw[field] {
			if slices.Contains(expect[field], v) {
				hits++
				break
			}
		}
	}
	return hits, saw
}

// eeDecoyExpect is eeDecoyLayout's row out of the scoring table, by identity.
func eeDecoyExpect(t *testing.T) map[string][]string {
	t.Helper()
	for _, want := range expectByLayout {
		if want.file == eeDecoyLayout {
			return want.fields
		}
	}
	t.Fatalf("%s carries no expectByLayout row; the decoy would be scored against nothing", eeDecoyLayout)
	return nil
}

// --- the specs ----------------------------------------------------------------------------

// AC-1, AC-2. The named deliberate break. Both halves of the sandwich run in ONE test: without
// the shipped half the zero below is indistinguishable from a broken harness, and without the
// reach half the zero is indistinguishable from "the rules stopped reaching anything".
func TestRLS_EndToEndAMutilatedRuleSetTakesTheScoreToZero(t *testing.T) {
	eeRequire(t)
	ctx := t.Context()

	// The needle first, before any run: three independent integers, asserted inside
	// eeCutRuleSet, so a lexicon change fails HERE naming the cut.
	snapshot := slices.Clone(extraction.Tier1Rules)
	cut := eeCutRuleSet(t)

	var cutScore eeScore
	eeWithRuleSet(t, cut, func() {
		cutScore = eeScoreCorpus(t, ctx)
	})
	cutReport := eeRenderReport(cutScore)
	t.Log("\nunder the cut:\n" + cutReport)

	// Floor: an empty walk scores zero for a reason that is not the cut.
	if cutScore.total != eeCorpusCells {
		t.Fatalf("the cut walk is taken over %d cell(s), want %d -- the zero below would be over the wrong denominator", cutScore.total, eeCorpusCells)
	}
	if cutScore.hits != eeCutScore {
		t.Errorf("dropping every %q rule scores %d / %d, want %d / %d:\n%s", eeCutField, cutScore.hits, cutScore.total, eeCutScore, eeCorpusCells, cutReport)
	}
	// By identity, not by rate: the zero is the quarantine branch answering. An empty invoice
	// number is a RowError in documentCreateInput, so no invoices row is written at all.
	if len(cutScore.quarantined) != eeLayoutCount {
		t.Errorf("the cut quarantined %d layout(s) (%v), want all %d -- a zero that is not the quarantine branch is a broken scorer", len(cutScore.quarantined), cutScore.quarantined, eeLayoutCount)
	}

	// The gap the number exists to show: the rules still REACH most of these values, and not
	// one of them reached an invoices row.
	reach, reachCells := eeReachHits(t, cut)
	if reachCells != eeCorpusCells {
		t.Fatalf("the reach walk covered %d cell(s), want %d -- it is not the same denominator as the score", reachCells, eeCorpusCells)
	}
	if reach == 0 {
		t.Fatal("the cut reaches 0 cell(s); the gap between reach and the end-to-end score is vacuous, so the zero above proves nothing about the score")
	}
	if reach != eeCutReach {
		t.Errorf("the cut reaches %d / %d value(s) while writing %d invoice(s); pinned at %d -- re-measure and update eeCutReach. The story's 37/44 is acMutilatedHits over corpusExpect's 44 pairs and is NOT this figure",
			reach, reachCells, len(expectByLayout)-len(cutScore.quarantined), eeCutReach)
	}

	// The must-stay-green half, same test, shipped set restored.
	if !slices.Equal(extraction.Tier1Rules, snapshot) {
		t.Fatalf("extraction.Tier1Rules did not come back to the shipped set after the cut; the walk below would measure the mutilation")
	}
	shippedScore := eeScoreCorpus(t, ctx)
	shippedReport := eeRenderReport(shippedScore)
	t.Log("\nunder the shipped set:\n" + shippedReport)

	if shippedScore.total != eeCorpusCells {
		t.Fatalf("the shipped walk is taken over %d cell(s), want %d", shippedScore.total, eeCorpusCells)
	}
	// By identity: the shipped set quarantines exactly the layout that prints no invoice
	// number, and nothing else. A count would let the cut's quarantines leak into this half.
	if !slices.Equal(shippedScore.quarantined, eeQuarantinedLayouts) {
		t.Errorf("the shipped set quarantined %v, want exactly %v; the harness itself is broken, so the zero above proves nothing:\n%s", shippedScore.quarantined, eeQuarantinedLayouts, shippedReport)
	}
	if shippedScore.hits != eeCorpusHits {
		t.Errorf("the shipped set scores %d / %d, pinned at %d / %d -- the sandwich's green half moved:\n%s", shippedScore.hits, shippedScore.total, eeCorpusHits, eeCorpusCells, shippedReport)
	}
	if shippedScore.hits <= cutScore.hits {
		t.Errorf("the shipped set scores %d and the cut %d; the number does not move when the rules are deliberately broken", shippedScore.hits, cutScore.hits)
	}

	if !slices.Equal(extraction.Tier1Rules, snapshot) {
		t.Errorf("extraction.Tier1Rules is not the set this test started with")
	}
}

// AC-3, AC-4. A rank-1 alternative that recall would count and the invoice never sees.
// Everything below the rule is asserted BEFORE any rate is read.
func TestRLS_EndToEndARankOneDecoyMovesTheScore(t *testing.T) {
	eeRequire(t)
	ctx := t.Context()
	expect := eeDecoyExpect(t)

	run := eeRunDecoyLayout(t, ctx, eeDecoyRule)

	// 1. The decoy exists as a row.
	if run.decoyID == "" {
		t.Fatal("the decoy run wrote no anchor rule; a decoy that wrote nothing must not pass as a moved number")
	}
	// 2. The PRODUCTION reader serves it -- inserted is not served.
	if run.served != 1 || !slices.Equal(run.servedFields, []string{eeDecoyField}) {
		t.Fatalf("AnchorRulesFor served %d rule(s) %v for the decoy fingerprint, want exactly 1 for %q", run.served, run.servedFields, eeDecoyField)
	}
	// 3. The seed is keyed to the fingerprint the job actually stored.
	if run.fingerprint != run.wantFP {
		t.Fatalf("the job stored layout_fingerprint %q and the decoy was seeded under %q; a mis-keyed seed degrades to no decoy at all", run.fingerprint, run.wantFP)
	}
	// 4. The two rows, by count AND by per-rank value.
	totals := eeDecoyFieldRows(run, eeDecoyField)
	if len(totals) != eeDecoyRows {
		t.Fatalf("the decoy run wrote %d %q row(s), want %d -- one row, or three, is not the rank-1 shape this spec measures", len(totals), eeDecoyField, eeDecoyRows)
	}
	for i, want := range []string{eeDecoyRank0, eeDecoyRank1} {
		if totals[i].rank != i {
			t.Errorf("%q row %d sits at candidate_rank %d, want %d", eeDecoyField, i, totals[i].rank, i)
			continue
		}
		if totals[i].value == nil || *totals[i].value != want {
			t.Errorf("%q at rank %d holds %v, want %q", eeDecoyField, i, totals[i].value, want)
		}
	}

	// 5. Only now the rates.
	if run.invoice == nil {
		t.Fatal("the decoy run quarantined; the decoy must move one cell, not remove the row")
	}
	if got := run.invoice[eeDecoyField]; got != eeDecoyRank0 {
		t.Errorf("the invoices row holds %s = %q, want the decoy's %q", eeDecoyField, got, eeDecoyRank0)
	}

	base := eeRunDecoyLayout(t, ctx, "")
	if base.decoyID != "" || base.served != 0 {
		t.Fatalf("the baseline run was served %d learned rule(s); it is not a baseline", base.served)
	}
	if base.invoice == nil {
		t.Fatal("the baseline run quarantined; the comparison below would measure the harness")
	}
	if got := base.invoice[eeDecoyField]; got != eeDecoyRank1 {
		t.Errorf("the baseline invoices row holds %s = %q, want the layout's real total %q", eeDecoyField, got, eeDecoyRank1)
	}

	baseHits, _ := eeInvoiceReadHits(base, expect)
	decoyHits, _ := eeInvoiceReadHits(run, expect)
	if baseHits != eeDecoyBaseHits {
		t.Errorf("%s scores %d / %d with no decoy, pinned at %d -- re-measure and update eeDecoyBaseHits", eeDecoyLayout, baseHits, len(writtenFields), eeDecoyBaseHits)
	}
	if decoyHits != eeDecoyBaseHits-1 {
		t.Errorf("the decoy run scores %d / %d, want %d -- the decoy takes exactly one cell", decoyHits, len(writtenFields), eeDecoyBaseHits-1)
	}
	if decoyHits >= baseHits {
		t.Errorf("the decoy leaves the score at %d against the baseline %d; the number does not move when a value is out-ranked", decoyHits, baseHits)
	}

	// By identity, not by count: the decoy's miss set is the baseline's plus exactly {total}.
	// subtotal must STILL hit, proving the decoy took the rank it exists to take and no other.
	_, baseSaw := eeInvoiceReadHits(base, expect)
	_, decoySaw := eeInvoiceReadHits(run, expect)
	if len(baseSaw) != len(writtenFields) || len(decoySaw) != len(writtenFields) {
		t.Fatalf("the two reads cover %d and %d field(s), want %d each", len(baseSaw), len(decoySaw), len(writtenFields))
	}
	var moved []string
	for _, field := range writtenFields {
		if baseSaw[field] != decoySaw[field] {
			moved = append(moved, field)
		}
	}
	if !slices.Equal(moved, []string{eeDecoyField}) {
		t.Errorf("the decoy moved %v, want exactly [%s] -- baseline %v against decoy %v", moved, eeDecoyField, baseSaw, decoySaw)
	}
	if got := run.invoice["subtotal"]; got != eeDecoyRank0 {
		t.Errorf("the decoy run holds subtotal = %q, want %q -- the decoy was supposed to take the total's rank, not the sub-total's value", got, eeDecoyRank0)
	}
}

// AC-3. The spec that fails if the number could be satisfied by recall alone. The same run,
// scored twice: once off the invoices row, once by "the value is present at some rank".
func TestRLS_EndToEndTheScoreIsNotARecallMeasure(t *testing.T) {
	eeRequire(t)
	ctx := t.Context()
	expect := eeDecoyExpect(t)

	// The same-run baseline control FIRST: two reads that are secretly the same function, and
	// a scorer that always adds one, both fail here before the decoy is ever seeded.
	base := eeRunDecoyLayout(t, ctx, "")
	if base.invoice == nil {
		t.Fatal("the baseline run quarantined; both reads below would be over nothing")
	}
	baseInvoice, _ := eeInvoiceReadHits(base, expect)
	baseAnyRank, baseAnySaw := eeAnyRankHits(base, expect)
	if len(baseAnySaw) == 0 {
		t.Fatal("the baseline run wrote no extraction_field_results row this score can read; the any-rank read is reading nothing")
	}
	if baseInvoice != baseAnyRank {
		t.Fatalf("with no decoy the invoice read scores %d and the any-rank read %d; the two disagree where nothing out-ranks anything, so the difference below is not the decoy's", baseInvoice, baseAnyRank)
	}
	if baseInvoice != eeDecoyBaseHits {
		t.Errorf("%s scores %d / %d under both reads with no decoy, pinned at %d -- re-measure and update eeDecoyBaseHits", eeDecoyLayout, baseInvoice, len(writtenFields), eeDecoyBaseHits)
	}

	run := eeRunDecoyLayout(t, ctx, eeDecoyRule)
	if run.decoyID == "" || run.served != 1 {
		t.Fatalf("the decoy run was served %d learned rule(s) (id %q), want 1; a run with no decoy cannot show the difference", run.served, run.decoyID)
	}
	if run.invoice == nil {
		t.Fatal("the decoy run quarantined; the invoice read below would be over nothing")
	}

	invoiceHits, invoiceSaw := eeInvoiceReadHits(run, expect)
	anyRankHits, anyRankSaw := eeAnyRankHits(run, expect)

	// Floors: a read that returns nothing satisfies any difference.
	if invoiceHits == 0 || anyRankHits == 0 {
		t.Fatalf("the invoice read scores %d and the any-rank read %d; a zero read satisfies any difference", invoiceHits, anyRankHits)
	}
	if len(invoiceSaw) != len(writtenFields) {
		t.Fatalf("the invoice read covers %d field(s), want %d", len(invoiceSaw), len(writtenFields))
	}
	if len(anyRankSaw) == 0 {
		t.Fatal("the any-rank read saw no field at all")
	}
	// The loss is located on the invoice side alone: reach is unchanged by the decoy, so the
	// difference below cannot come from BOTH reads falling and one falling further.
	if anyRankHits != baseAnyRank {
		t.Errorf("the any-rank read scores %d with the decoy and %d without; the decoy is supposed to move a RANK, not a value's presence", anyRankHits, baseAnyRank)
	}
	if invoiceHits != baseInvoice-1 {
		t.Errorf("the invoice read scores %d with the decoy and %d without, want exactly one less", invoiceHits, baseInvoice)
	}

	// The difference is LOCATED, not counted: the real total is present at some rank and the
	// invoice holds the decoy instead.
	if !slices.Contains(anyRankSaw[eeDecoyField], eeDecoyRank1) {
		t.Errorf("the any-rank read of %q saw %v, which does not carry %q -- the value the invoice never saw is not there either, so the two reads differ for another reason", eeDecoyField, anyRankSaw[eeDecoyField], eeDecoyRank1)
	}
	if invoiceSaw[eeDecoyField] != eeDecoyRank0 {
		t.Errorf("the invoice read of %q holds %q, want the decoy's %q", eeDecoyField, invoiceSaw[eeDecoyField], eeDecoyRank0)
	}
	var differ []string
	for _, field := range writtenFields {
		wantValues := expect[field]
		if len(wantValues) == 0 {
			continue
		}
		byInvoice := invoiceSaw[field] != "" && slices.Contains(wantValues, invoiceSaw[field])
		byAnyRank := false
		for _, v := range anyRankSaw[field] {
			if slices.Contains(wantValues, v) {
				byAnyRank = true
				break
			}
		}
		if byInvoice != byAnyRank {
			differ = append(differ, field)
		}
	}
	if !slices.Equal(differ, []string{eeDecoyField}) {
		t.Errorf("the two reads differ on %v, want exactly [%s]", differ, eeDecoyField)
	}

	// Counted last, after the identity.
	if anyRankHits != invoiceHits+1 {
		t.Errorf("the any-rank read scores %d and the invoice read %d over %d cell(s), want exactly one more -- a number a recall measure could satisfy would read the same both ways",
			anyRankHits, invoiceHits, len(writtenFields))
	}
}

// AC-5. Every variant is a struct copy: no shipped rule, dial, lexicon entry or fingerprint
// version is edited. No database, so this runs in ci.yml's bare `go` job too.
func TestEndToEnd_TheVariantsNeverMutateTheShippedRules(t *testing.T) {
	snapshot := slices.Clone(extraction.Tier1Rules)
	// The floor first: every comparison below is over this set.
	if len(snapshot) != eeShippedRules {
		t.Fatalf("extraction.Tier1Rules holds %d rule(s), pinned at %d -- the comparisons below would be over the wrong set", len(snapshot), eeShippedRules)
	}

	cut := eeCutRuleSet(t)

	// Tier1Rule is comparable down to the compiled matcher pointer, so slices.Equal catches an
	// in-place edit of any rule BODY, not merely a resized slice.
	if !slices.Equal(extraction.Tier1Rules, snapshot) {
		t.Fatalf("building the cut mutated extraction.Tier1Rules")
	}

	// The cut differs in exactly the intended way: the eeCutField entries removed, every other
	// element == its snapshot counterpart, in order.
	var wantCut []extraction.Tier1Rule
	var removed []string
	for _, r := range snapshot {
		if r.Field == eeCutField {
			removed = append(removed, r.Key)
			continue
		}
		wantCut = append(wantCut, r)
	}
	if len(removed) != eeCutRemoved {
		t.Errorf("the shipped set carries %d %q rule(s) (%v), pinned at %d", len(removed), eeCutField, removed, eeCutRemoved)
	}
	if !slices.Equal(cut, wantCut) {
		t.Errorf("the cut is not the shipped set minus its %d %q rule(s), in order", eeCutRemoved, eeCutField)
	}
	// Control: the cut is not merely equal to the shipped set. A no-op "variant" would satisfy
	// every equality above.
	if slices.Equal(cut, snapshot) {
		t.Fatalf("the cut equals the shipped set; it mutilates nothing, so the score it produces is the shipped score")
	}
	for _, r := range cut {
		if !slices.Contains(snapshot, r) {
			t.Errorf("the cut carries %s, which is not an element of the shipped set -- a variant may drop rules, never invent one", r.Key)
		}
	}

	// The decoy is not a rule-set variant at all: it is a row in extraction_anchor_rules, read
	// back through AnchorRulesFor. Nothing in its body names a Tier-1 key.
	if strings.Contains(eeDecoyRule, "t1.") {
		t.Errorf("the decoy rule body names a Tier-1 key: %s", eeDecoyRule)
	}
	if !slices.Equal(extraction.Tier1Rules, snapshot) {
		t.Errorf("extraction.Tier1Rules moved while the decoy constants were read")
	}

	// eeWithRuleSet round-trips: the var reads the variant INSIDE the closure and the snapshot
	// after it returns. Without the inside half, an installer that installs nothing passes.
	var inside bool
	eeWithRuleSet(t, cut, func() {
		inside = slices.Equal(extraction.Tier1Rules, cut)
	})
	if !inside {
		t.Errorf("eeWithRuleSet did not install the variant; a run under it would measure the shipped set")
	}
	if !slices.Equal(extraction.Tier1Rules, snapshot) {
		t.Errorf("eeWithRuleSet did not restore extraction.Tier1Rules; the mutilation leaks into every later test")
	}
}

// --- the adversarial half -------------------------------------------------------------------

// eeInertDecoyRule is served by AnchorRulesFor and matches no token on the layout, so it writes
// nothing. The control for "a decoy that wrote nothing must not pass as a moved number".
const eeInertDecoyRule = `{"label":"^\\s*NO SUCH TOKEN\\s*$","relation":{"kind":"same_token","max_distance":0},"shape":"amount"}`

// A filter that names a field no rule carries is a no-op, not a mutilation. eeCutRuleSet's
// pinned integer is what rejects it; this spec is the positive control that the integer is
// measuring a real removal and not an arithmetic identity.
func TestEndToEnd_AFilterThatRemovesNothingIsNotAMutilation(t *testing.T) {
	shipped := slices.Clone(extraction.Tier1Rules)
	if len(shipped) != eeShippedRules {
		t.Fatalf("extraction.Tier1Rules holds %d rule(s), pinned at %d", len(shipped), eeShippedRules)
	}

	inert, removed := eeRulesWithout(shipped, "no_such_field")
	if len(removed) != 0 {
		t.Errorf("filtering a field no rule carries dropped %v, want nothing", removed)
	}
	if !slices.Equal(inert, shipped) {
		t.Errorf("the no-op filter did not return the shipped set")
	}
	// The integer is what makes that state loud: a cut equal to the shipped set fails the pin.
	if len(shipped)-len(inert) == eeCutRemoved {
		t.Errorf("a no-op filter removes as many rules as the cut is pinned at (%d); the pin cannot tell them apart", eeCutRemoved)
	}

	// And the real cut removes exactly the keys it names, not merely the right count.
	cut, cutKeys := eeRulesWithout(shipped, eeCutField)
	if len(cutKeys) != eeCutRemoved {
		t.Fatalf("the cut dropped %v, want %d key(s)", cutKeys, eeCutRemoved)
	}
	for _, key := range cutKeys {
		if !strings.HasPrefix(key, "t1."+eeCutField+".") {
			t.Errorf("the cut dropped %s, which does not belong to %q", key, eeCutField)
		}
	}
	if slices.Equal(cut, shipped) || !slices.Equal(extraction.Tier1Rules, shipped) {
		t.Errorf("eeRulesWithout aliased or failed to filter the shipped set")
	}
}

// The restore is not luck of test ordering: a t.Fatal inside the closure is a runtime.Goexit,
// and a panic unwinds, and neither may leave a mutilated set installed for the next test.
func TestEndToEnd_TheRuleSetSwapSurvivesAGoexitAndAPanic(t *testing.T) {
	snapshot := slices.Clone(extraction.Tier1Rules)
	cut := eeCutRuleSet(t)

	// The panic arm, on this goroutine.
	func() {
		defer func() {
			if recover() == nil {
				t.Error("the panic arm never panicked; the restore below is asserted against nothing")
			}
		}()
		eeWithRuleSet(t, cut, func() { panic("the closure died mid-run") })
	}()
	if !slices.Equal(extraction.Tier1Rules, snapshot) {
		// Errorf, not Fatalf: the Goexit arm below is a separate claim and must still run.
		t.Errorf("a panic inside the closure leaked the mutilated set into every later test")
		extraction.Tier1Rules = slices.Clone(snapshot)
	}

	// The Goexit arm -- what t.Fatal does -- on its own goroutine, so this test survives it.
	var installed bool
	done := make(chan struct{})
	go func() {
		defer close(done)
		eeWithRuleSet(t, cut, func() {
			installed = slices.Equal(extraction.Tier1Rules, cut)
			runtime.Goexit()
		})
		t.Error("the Goexit arm returned normally; it never exercised the Fatal path")
	}()
	<-done
	if !installed {
		t.Fatal("the Goexit arm never saw the variant installed; the restore below is asserted against nothing")
	}
	if !slices.Equal(extraction.Tier1Rules, snapshot) {
		t.Errorf("a Fatal inside the closure leaked the mutilated set into every later test")
	}
}

// AC-4's negative half, as a run rather than an assertion: a decoy that is served and writes
// nothing must read exactly like the baseline under both reads.
func TestRLS_EndToEndADecoyThatWritesNothingIsNotAMovedNumber(t *testing.T) {
	eeRequire(t)
	ctx := t.Context()
	expect := eeDecoyExpect(t)

	run := eeRunDecoyLayout(t, ctx, eeInertDecoyRule)
	if run.decoyID == "" {
		t.Fatal("the inert decoy wrote no anchor rule; this run is a baseline by accident")
	}
	if run.served != 1 || !slices.Equal(run.servedFields, []string{eeDecoyField}) {
		t.Fatalf("AnchorRulesFor served %d rule(s) %v, want exactly 1 for %q -- an unserved rule proves nothing about a rule that matched nothing", run.served, run.servedFields, eeDecoyField)
	}
	if run.fingerprint != run.wantFP {
		t.Fatalf("the job stored layout_fingerprint %q, the seed used %q", run.fingerprint, run.wantFP)
	}
	if run.invoice == nil {
		t.Fatal("the inert-decoy run quarantined; the reads below would be over nothing")
	}

	totals := eeDecoyFieldRows(run, eeDecoyField)
	if len(totals) != 1 {
		t.Errorf("the inert decoy left %d %q row(s), want 1 -- it matched a token it was written not to match", len(totals), eeDecoyField)
	}
	if got := run.invoice[eeDecoyField]; got != eeDecoyRank1 {
		t.Errorf("the invoices row holds %s = %q, want the layout's real total %q", eeDecoyField, got, eeDecoyRank1)
	}

	invoiceHits, _ := eeInvoiceReadHits(run, expect)
	anyRankHits, anyRankSaw := eeAnyRankHits(run, expect)
	if len(anyRankSaw) == 0 {
		t.Fatal("the inert-decoy run wrote no field-result row the any-rank read can see")
	}
	if invoiceHits != eeDecoyBaseHits || anyRankHits != eeDecoyBaseHits {
		t.Errorf("the inert decoy scores %d by invoice and %d at any rank, want %d each -- a decoy that wrote nothing moved the number", invoiceHits, anyRankHits, eeDecoyBaseHits)
	}
}

// The rule-set swap is process-global, so a parallel test in this package would race it. Both
// needles are assembled from fragments so this file does not match its own scan.
func TestEndToEndPackage_NoTestAsksToRunInParallel(t *testing.T) {
	names, err := filepath.Glob("*_test.go")
	if err != nil {
		t.Fatalf("glob *_test.go: %v", err)
	}
	if len(names) < eeMinTestFiles {
		t.Fatalf("read %d test file(s), want at least %d -- this scan asserts an ABSENCE and zero files read reports clean", len(names), eeMinTestFiles)
	}

	parallelCall := regexp.MustCompile(`\b[A-Za-z_][A-Za-z0-9_]*\.Par` + `allel\(`)
	controlRE := regexp.MustCompile(`func eeWithRul` + `eSet\(`)

	var control int
	sites := map[string]int{}
	for _, name := range names {
		raw, err := os.ReadFile(name)
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		src := string(raw)
		control += len(controlRE.FindAllString(src, -1))
		if n := len(parallelCall.FindAllString(src, -1)); n > 0 {
			sites[name] = n
		}
	}
	// Control needle: a scan that stopped matching anything reads clean too.
	if control != 1 {
		t.Fatalf("the scan found eeWithRuleSet declared %d time(s), want 1 -- it is not reading this package", control)
	}
	for name, n := range sites {
		t.Errorf("%s makes %d parallel call(s); this package swaps the process-global extraction.Tier1Rules, so a parallel test reads whichever set won the race", name, n)
	}
}

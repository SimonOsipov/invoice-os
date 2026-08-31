// reconcile_corpus_test.go: EXTR-05-07. The story's only end-to-end oracle -- the six committed
// golden layouts through the real pipeline, PDFiumReader -> CollectTokens -> Resolve(Tier1Rules)
// -> Reconcile, with Lines and Entity zero-valued. Every other reconcile_*_test.go file runs
// hand-built candidate slices; this is the one place a real document's output is pinned.
//
// corpusPinned is measured, not idealised. Two readings are still wrong and are tagged
// KNOWN GAP where they sit: corpus_two_column.pdf's supplier_tin and buyer_tin, which need an
// anchorLexicon edit and so a FingerprintVersion bump to close.
package extraction_test

import (
	"reflect"
	"slices"
	"testing"

	"github.com/SimonOsipov/invoice-os/internal/extraction"
)

// corpusRun reads name through the real pipeline and reconciles it, exactly as EXTR-05-07 wires
// it end to end.
func corpusRun(t *testing.T, name string, rules extraction.RuleSet) []extraction.FieldResult {
	t.Helper()

	var pages []extraction.TokenPage
	if _, err := extraction.NewPDFiumReader().Read(t.Context(), ptDoc(t, name), extraction.CollectTokens(&pages)); err != nil {
		t.Fatalf("Read(%s): %v", name, err)
	}
	cands := extraction.Resolve(pages, rules)
	return extraction.Reconcile(extraction.Input{Candidates: cands})
}

// corpusRequireSix is the floor every test below needs: a missing fixture or a trimmed
// corpusLayouts must fail loudly rather than let the assertions below pass over fewer layouts.
func corpusRequireSix(t *testing.T) {
	t.Helper()
	corpusRequireCommitted(t)
	if len(corpusLayouts) != 6 {
		t.Fatalf("corpusLayouts names %d layout(s), want 6", len(corpusLayouts))
	}
}

func corpusValueEqual(got, want *string) bool {
	if got == nil || want == nil {
		return got == nil && want == nil
	}
	return *got == *want
}

// --- the full pin: what the pipeline reads today, field by field -----------------------------

// corpusFieldPin is one field's decided reading plus its alternatives. alts is nil or empty for
// every non-ambiguous field; decideField never leaves Alternatives nil, but the comparison below
// reads only the values, so the two are equivalent here.
type corpusFieldPin struct {
	name   string
	value  *string
	reason extraction.Reason
	alts   []string
}

// corpusPinned is the measured output of all six layouts, re-measured on this pipeline (not
// carried over from EXTR-04's own accuracy table, which never runs Reconcile). Each row is
// HeaderFields order then line_items -- rcExpectedNames' order -- so index i always names the
// same field as rcExpectedNames()[i].
var corpusPinned = []struct {
	file   string
	fields []corpusFieldPin
}{
	{
		file: "corpus_inline_labels.pdf",
		fields: []corpusFieldPin{
			{"invoice_number", rcStr("INV-1001"), extraction.ReasonNone, nil},
			{"issue_date", rcStr("2026-03-04"), extraction.ReasonNone, nil},
			{"supplier_tin", rcStr("99999999-0101"), extraction.ReasonNone, nil},
			{"supplier_name", rcStr("Adeyemi Trading Limited"), extraction.ReasonNone, nil},
			{"buyer_tin", rcStr("99999999-0102"), extraction.ReasonNone, nil},
			{"buyer_name", rcStr("Honeywell Group"), extraction.ReasonNone, nil},
			{"currency", rcStr("NGN"), extraction.ReasonNone, nil},
			{"subtotal", rcStr("1000.00"), extraction.ReasonNone, nil},
			{"vat", rcStr("75.00"), extraction.ReasonNone, nil},
			{"total", rcStr("1075.00"), extraction.ReasonNone, nil},
			{"line_items", nil, extraction.ReasonMissing, nil},
		},
	},
	{
		file: "corpus_split_labels.pdf",
		fields: []corpusFieldPin{
			{"invoice_number", rcStr("INV-1002"), extraction.ReasonNone, nil},
			{"issue_date", rcStr("2026-04-15"), extraction.ReasonNone, nil},
			{"supplier_tin", rcStr("99999999-0201"), extraction.ReasonNone, nil},
			{"supplier_name", rcStr("Adeyemi Trading Limited"), extraction.ReasonNone, nil},
			{"buyer_tin", rcStr("99999999-0202"), extraction.ReasonNone, nil},
			{"buyer_name", rcStr("Honeywell Group"), extraction.ReasonNone, nil},
			{"currency", rcStr("NGN"), extraction.ReasonNone, nil},
			{"subtotal", rcStr("2000.00"), extraction.ReasonNone, nil},
			{"vat", rcStr("150.00"), extraction.ReasonNone, nil},
			{"total", rcStr("2150.00"), extraction.ReasonNone, nil},
			{"line_items", nil, extraction.ReasonMissing, nil},
		},
	},
	{
		file: "corpus_stacked_labels.pdf",
		fields: []corpusFieldPin{
			{"invoice_number", rcStr("INV-1003"), extraction.ReasonNone, nil},
			{"issue_date", rcStr("2026-04-22"), extraction.ReasonNone, nil},
			{"supplier_tin", rcStr("99999999-0301"), extraction.ReasonNone, nil},
			{"supplier_name", rcStr("Adeyemi Trading Limited"), extraction.ReasonNone, nil},
			{"buyer_tin", rcStr("99999999-0302"), extraction.ReasonNone, nil},
			{"buyer_name", rcStr("Honeywell Group"), extraction.ReasonNone, nil},
			// This layout carries no currency/subtotal/vat token at all (docs/extraction-corpus.md);
			// missing here is the correct reading, not a defect.
			{"currency", nil, extraction.ReasonMissing, nil},
			{"subtotal", nil, extraction.ReasonMissing, nil},
			{"vat", nil, extraction.ReasonMissing, nil},
			{"total", rcStr("3225.00"), extraction.ReasonNone, nil},
			{"line_items", nil, extraction.ReasonMissing, nil},
		},
	},
	{
		file: "corpus_two_column.pdf",
		fields: []corpusFieldPin{
			{"invoice_number", rcStr("INV-1004"), extraction.ReasonNone, nil},
			{"issue_date", rcStr("2026-05-06"), extraction.ReasonNone, nil},
			// KNOWN GAP (t1aGaps) -- still wrong, owned elsewhere; EXTR-16 does not touch the
			// lexicon. The bare "TIN" label's optional party word also matches the buyer's TIN,
			// so a clean field reads ambiguous.
			{"supplier_tin", rcStr("99999999-0401"), extraction.ReasonAmbiguous, []string{"99999999-0402"}},
			{"supplier_name", rcStr("Adeyemi Trading Limited"), extraction.ReasonNone, nil},
			// KNOWN GAP (t1aGaps) -- still wrong, owned elsewhere; EXTR-16 does not touch the
			// lexicon. The buyer's own TIN (99999999-0402) is real but never reaches this field:
			// it is entirely absorbed as supplier_tin's alternative above.
			{"buyer_tin", nil, extraction.ReasonMissing, nil},
			{"buyer_name", rcStr("Honeywell Group"), extraction.ReasonNone, nil},
			{"currency", nil, extraction.ReasonMissing, nil},
			{"subtotal", nil, extraction.ReasonMissing, nil},
			{"vat", nil, extraction.ReasonMissing, nil},
			{"total", rcStr("6450.00"), extraction.ReasonNone, nil},
			{"line_items", nil, extraction.ReasonMissing, nil},
		},
	},
	{
		file: "corpus_ambiguous_date.pdf",
		fields: []corpusFieldPin{
			{"invoice_number", rcStr("INV-1005"), extraction.ReasonNone, nil},
			// Correct: the fixture's whole reason for existing (AC-2). 12/03/2026 has both
			// components <= 12 and no month name, so ShapeDate returns both normalisations.
			{"issue_date", rcStr("2026-03-12"), extraction.ReasonAmbiguous, []string{"2026-12-03"}},
			{"supplier_tin", rcStr("99999999-0501"), extraction.ReasonNone, nil},
			{"supplier_name", rcStr("Adeyemi Trading Limited"), extraction.ReasonNone, nil},
			// This layout carries no buyer block at all; missing is the correct reading.
			{"buyer_tin", nil, extraction.ReasonMissing, nil},
			{"buyer_name", nil, extraction.ReasonMissing, nil},
			{"currency", nil, extraction.ReasonMissing, nil},
			{"subtotal", nil, extraction.ReasonMissing, nil},
			{"vat", nil, extraction.ReasonMissing, nil},
			{"total", rcStr("4300.00"), extraction.ReasonNone, nil},
			{"line_items", nil, extraction.ReasonMissing, nil},
		},
	},
	{
		file: "corpus_totals_block.pdf",
		fields: []corpusFieldPin{
			{"invoice_number", rcStr("INV-1006"), extraction.ReasonNone, nil},
			// This layout carries no issue_date token at all; missing is the correct reading.
			{"issue_date", nil, extraction.ReasonMissing, nil},
			{"supplier_tin", rcStr("99999999-0601"), extraction.ReasonNone, nil},
			// This layout carries no party name at all; missing is the correct reading.
			{"supplier_name", nil, extraction.ReasonMissing, nil},
			{"buyer_tin", nil, extraction.ReasonMissing, nil},
			{"buyer_name", nil, extraction.ReasonMissing, nil},
			{"currency", nil, extraction.ReasonMissing, nil},
			{"subtotal", rcStr("5000.00"), extraction.ReasonNone, nil},
			{"vat", rcStr("375.00"), extraction.ReasonNone, nil},
			{"total", rcStr("5375.00"), extraction.ReasonNone, nil},
			{"line_items", nil, extraction.ReasonMissing, nil},
		},
	},
}

// AC-1. Structural totality (11 results, HeaderFields order then line_items) plus the full
// value/reason/alternative pin above, in one pass: a mismatch anywhere is a readable diff
// against a table a human can read.
func TestReconcileCorpus_EveryLayoutIsTotal(t *testing.T) {
	corpusRequireSix(t)
	if len(corpusPinned) != 6 {
		t.Fatalf("corpusPinned has %d layout(s), want 6", len(corpusPinned))
	}

	wantNames := rcExpectedNames()
	rules := extraction.RuleSet{Tier1: extraction.Tier1Rules}

	ran := 0
	for _, lay := range corpusPinned {
		t.Run(lay.file, func(t *testing.T) {
			if len(lay.fields) != len(wantNames) {
				t.Fatalf("corpusPinned[%s] has %d field pin(s), want %d", lay.file, len(lay.fields), len(wantNames))
			}

			out := corpusRun(t, lay.file, rules)
			if len(out) == 0 {
				t.Fatalf("%s produced no results at all", lay.file)
			}
			ran++

			if !rcNamesEqual(rcNames(out), wantNames) {
				t.Fatalf("%s field order = %v, want %v", lay.file, rcNames(out), wantNames)
			}

			for i, pin := range lay.fields {
				got := out[i]
				if !corpusValueEqual(got.Value, pin.value) {
					t.Errorf("%s.%s value = %v, want %v", lay.file, pin.name, got.Value, pin.value)
				}
				if got.Reason != pin.reason {
					t.Errorf("%s.%s reason = %q, want %q", lay.file, pin.name, got.Reason, pin.reason)
				}
				if !slices.Equal(valuesOf(got.Alternatives), pin.alts) {
					t.Errorf("%s.%s alternatives = %v, want %v", lay.file, pin.name, valuesOf(got.Alternatives), pin.alts)
				}
			}
		})
	}
	if ran != 6 {
		t.Fatalf("exercised %d of 6 layout(s)", ran)
	}
}

// AC-7. D-19's pdfium-path fact, pinned rather than assumed: PDFiumReader sets no Tables at
// all, so line_items has no candidate on any layout, however many fields the layout otherwise
// carries.
func TestReconcileCorpus_ThePdfiumPathHasNoLineItems(t *testing.T) {
	corpusRequireSix(t)
	rules := extraction.RuleSet{Tier1: extraction.Tier1Rules}

	ran := 0
	for _, name := range corpusLayouts {
		out := corpusRun(t, name, rules)
		if len(out) == 0 {
			t.Fatalf("%s produced no results", name)
		}
		ran++

		li, ok := rcFind(out, "line_items")
		if !ok {
			t.Fatalf("%s: no line_items result at all", name)
		}
		if li.Reason != extraction.ReasonMissing {
			t.Errorf("%s: line_items reason = %q, want %q", name, li.Reason, extraction.ReasonMissing)
		}
		if li.Value != nil {
			t.Errorf("%s: line_items value = %q, want nil", name, *li.Value)
		}
	}
	if ran != 6 {
		t.Fatalf("exercised %d of 6 layout(s)", ran)
	}
}

// AC-2. corpus_ambiguous_date.pdf is the epic's only genuine two-candidate layout: both
// normalisations of 12/03/2026 must survive, one as the decided value and one as the
// alternative -- which one lands where is not asserted, only that both are present somewhere.
func TestReconcileCorpus_AmbiguousDateKeepsBothReadings(t *testing.T) {
	corpusRequireCommitted(t)
	const file = "corpus_ambiguous_date.pdf"

	out := corpusRun(t, file, extraction.RuleSet{Tier1: extraction.Tier1Rules})
	if len(out) == 0 {
		t.Fatalf("%s produced no results", file)
	}

	issueDate, ok := rcFind(out, "issue_date")
	if !ok {
		t.Fatalf("%s: no issue_date result", file)
	}
	if issueDate.Reason != extraction.ReasonAmbiguous {
		t.Fatalf("%s: issue_date reason = %q, want %q", file, issueDate.Reason, extraction.ReasonAmbiguous)
	}
	if issueDate.Value == nil {
		t.Fatalf("%s: issue_date has no decided value", file)
	}

	readings := map[string]bool{*issueDate.Value: true}
	for _, v := range valuesOf(issueDate.Alternatives) {
		readings[v] = true
	}
	for _, want := range []string{"2026-03-12", "2026-12-03"} {
		if !readings[want] {
			t.Errorf("%s: issue_date readings %v do not include %q", file, readings, want)
		}
	}
}

// corpusMissingExpect is AC-3's own expectation table: the exact set of ReasonMissing fields
// per layout. line_items belongs to every row (AC-7); the rest follows which fields each
// layout's generator omits (docs/extraction-corpus.md) plus the one omission the pipeline
// itself introduces -- corpus_two_column.pdf's buyer_tin, the KNOWN GAP pinned above.
var corpusMissingExpect = map[string][]string{
	"corpus_inline_labels.pdf":  {"line_items"},
	"corpus_split_labels.pdf":   {"line_items"},
	"corpus_stacked_labels.pdf": {"currency", "subtotal", "vat", "line_items"},
	"corpus_two_column.pdf":     {"buyer_tin", "currency", "subtotal", "vat", "line_items"},
	"corpus_ambiguous_date.pdf": {"buyer_tin", "buyer_name", "currency", "subtotal", "vat", "line_items"},
	"corpus_totals_block.pdf":   {"issue_date", "supplier_name", "buyer_tin", "buyer_name", "currency", "line_items"},
}

// AC-3. Set equality, not containment -- a field this table omits is asserted DECIDED by its
// absence, not merely unchecked.
func TestReconcileCorpus_MissingFieldsAreExactlyTheOmittedOnes(t *testing.T) {
	corpusRequireSix(t)
	if len(corpusMissingExpect) != len(corpusLayouts) {
		t.Fatalf("corpusMissingExpect has %d entr(y/ies), want %d", len(corpusMissingExpect), len(corpusLayouts))
	}
	rules := extraction.RuleSet{Tier1: extraction.Tier1Rules}

	ran := 0
	for _, name := range corpusLayouts {
		t.Run(name, func(t *testing.T) {
			want, ok := corpusMissingExpect[name]
			if !ok {
				t.Fatalf("no corpusMissingExpect entry for %s", name)
			}
			if len(want) == 0 {
				t.Fatalf("corpusMissingExpect[%s] is empty; equality below would assert nothing", name)
			}
			if !slices.Contains(want, "line_items") {
				t.Fatalf("corpusMissingExpect[%s] does not name line_items, which the pdfium path never fills (AC-7)", name)
			}

			out := corpusRun(t, name, rules)
			if len(out) == 0 {
				t.Fatalf("%s produced no results", name)
			}
			ran++

			var got []string
			for _, r := range out {
				if r.Reason == extraction.ReasonMissing {
					got = append(got, r.Name)
				}
			}
			gotSorted, wantSorted := slices.Clone(got), slices.Clone(want)
			slices.Sort(gotSorted)
			slices.Sort(wantSorted)
			if !slices.Equal(gotSorted, wantSorted) {
				t.Errorf("%s missing fields = %v, want exactly %v", name, gotSorted, wantSorted)
			}
		})
	}
	if ran != 6 {
		t.Fatalf("exercised %d of 6 layout(s)", ran)
	}
}

// AC-6's sibling: corpusMissingExpect emptied out would make
// TestReconcileCorpus_MissingFieldsAreExactlyTheOmittedOnes pass over nothing.
func TestReconcileCorpus_ExpectationTableIsNotEmpty(t *testing.T) {
	if len(corpusMissingExpect) != 6 {
		t.Fatalf("corpusMissingExpect has %d entr(y/ies), want exactly 6", len(corpusMissingExpect))
	}
	for file, fields := range corpusMissingExpect {
		if len(fields) == 0 {
			t.Errorf("%s names no omitted field; an empty row asserts nothing", file)
		}
	}
}

// AC-4. reason_code's closed set, re-declared here because rcValidReasons' own tests never run
// a real read -- this is the one place the set is checked against the pipeline's actual output.
// The >= 2 floor guards against a vacuous pass: the corpus is known to produce at least "",
// "ambiguous" and "missing" today, so seeing only one reason means the read produced nothing.
func TestReconcileCorpus_EveryReasonIsDeclared(t *testing.T) {
	corpusRequireSix(t)
	rules := extraction.RuleSet{Tier1: extraction.Tier1Rules}

	seen := map[extraction.Reason]bool{}
	ran := 0
	for _, name := range corpusLayouts {
		out := corpusRun(t, name, rules)
		if len(out) == 0 {
			t.Fatalf("%s produced no results", name)
		}
		ran++
		for _, r := range out {
			if !rcIsValidReason(r.Reason) {
				t.Errorf("%s: %s carries reason %q, outside the closed set", name, r.Name, r.Reason)
			}
			seen[r.Reason] = true
		}
	}
	if ran != 6 {
		t.Fatalf("exercised %d of 6 layout(s)", ran)
	}
	if len(seen) < 2 {
		t.Fatalf("the whole corpus produced only %d distinct reason(s) (%v)", len(seen), seen)
	}
}

// AC-5. 50 repetitions with real reads, in one process, compared against a captured first
// result -- not self-consistency. Measured at ~3s; no cheaper substitute is written.
func TestReconcileCorpus_IsDeterministic(t *testing.T) {
	corpusRequireSix(t)
	rules := extraction.RuleSet{Tier1: extraction.Tier1Rules}

	first := make(map[string][]extraction.FieldResult, len(corpusLayouts))
	for _, name := range corpusLayouts {
		out := corpusRun(t, name, rules)
		if len(out) == 0 {
			t.Fatalf("%s produced no results on the first repetition", name)
		}
		first[name] = out
	}

	const reps = 50
	for i := 1; i < reps; i++ {
		for _, name := range corpusLayouts {
			got := corpusRun(t, name, rules)
			if !reflect.DeepEqual(got, first[name]) {
				t.Fatalf("%s: repetition %d differs from repetition 0\nfirst: %+v\ngot:   %+v", name, i, first[name], got)
			}
		}
	}
}

// AC-6, and the one test that stops the oracle above being decorative: without it, a pipeline
// that returned nothing at all would satisfy every other assertion in this file (every field
// missing, 11 results, every reason in the closed set). Cloning Tier1Rules and dropping the
// three "total" rules (same_token, right, below) must flip total, and only total, to
// ReasonMissing.
func TestReconcileCorpus_AMutilatedRuleSetChangesTheReasons(t *testing.T) {
	corpusRequireSix(t)
	full := extraction.RuleSet{Tier1: extraction.Tier1Rules}

	var mutilated []extraction.Tier1Rule
	for _, r := range extraction.Tier1Rules {
		if r.Field != "total" {
			mutilated = append(mutilated, r)
		}
	}
	if want := len(extraction.Tier1Rules) - 3; len(mutilated) != want {
		t.Fatalf("dropping the total rules left %d rule(s), want %d -- Tier1Rules no longer carries exactly three total rules", len(mutilated), want)
	}
	mut := extraction.RuleSet{Tier1: mutilated}

	for _, name := range corpusLayouts {
		t.Run(name, func(t *testing.T) {
			before := corpusRun(t, name, full)
			if len(before) == 0 {
				t.Fatalf("%s produced no results with the unmutilated rule set", name)
			}

			// Positive control: today every layout carries a decided total, never missing --
			// the fact this test flips.
			beforeTotal, ok := rcFind(before, "total")
			if !ok {
				t.Fatalf("%s: no total result in the unmutilated run", name)
			}
			if beforeTotal.Reason == extraction.ReasonMissing {
				t.Fatalf("%s: total is already missing before mutilation -- the positive control has nothing to flip", name)
			}

			after := corpusRun(t, name, mut)
			if len(after) != len(before) {
				t.Fatalf("%s: mutilation changed the result count from %d to %d", name, len(before), len(after))
			}

			afterTotal, ok := rcFind(after, "total")
			if !ok {
				t.Fatalf("%s: no total result in the mutilated run", name)
			}
			if afterTotal.Reason != extraction.ReasonMissing {
				t.Errorf("%s: total reason = %q after dropping its rules, want %q", name, afterTotal.Reason, extraction.ReasonMissing)
			}
			if afterTotal.Value != nil {
				t.Errorf("%s: total value = %q after dropping its rules, want nil", name, *afterTotal.Value)
			}

			for i := range before {
				if before[i].Name != after[i].Name {
					t.Fatalf("%s: result[%d] name changed from %q to %q", name, i, before[i].Name, after[i].Name)
				}
				if before[i].Name == "total" {
					continue
				}
				if before[i].Reason != after[i].Reason {
					t.Errorf("%s: %s reason changed from %q to %q after removing only the total rules", name, before[i].Name, before[i].Reason, after[i].Reason)
				}
			}
		})
	}
}

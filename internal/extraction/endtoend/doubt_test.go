// doubt_test.go: EXTR-22's doubt pass against the shipped corpus. Which cells read
// ReasonAmbiguous, which adjacent heads earn their ReasonNone, and that no in-scope value
// moves. No database.
//
// Every walk here sources each layout the way bdByLayout does -- pdfium for ten, the committed
// docling golden for the image-only one, which reads zero pdfium tokens and would otherwise
// contribute an empty answer that agrees with any expectation.
package endtoend

import (
	"slices"
	"strings"
	"testing"

	"github.com/SimonOsipov/invoice-os/internal/extraction"
)

// dtInScope is the doubt's scope list, in the order dtByLayout stores its values. Every field
// outside it stays under D-4.
var dtInScope = [3]string{"buyer_tin", "buyer_name", "vat"}

// dtBelowSuffix and dtRightSuffix name the two beside-the-label relations by rule id.
const (
	dtBelowSuffix = ".below"
	dtRightSuffix = ".right"
)

// dtCell is one reconciled cell: what it decided and what it offers as alternatives.
type dtCell struct {
	layout string
	field  string
	value  string
	alts   []string
}

// dtAmbiguous is EVERY cell the eleven layouts read as ReasonAmbiguous, in walk order. Four are
// EXTR-22's doubt, all buyer_name; corpus_ambiguous_date.pdf's is the pre-existing
// equal-standing tie between two readings of one printed date, which the doubt never touches.
var dtAmbiguous = []dtCell{
	{"corpus_stacked_labels.pdf", "buyer_name", "Honeywell Group", []string{"99999999-0302"}},
	{"corpus_two_column.pdf", "buyer_name", "Honeywell Group", []string{"TIN: 99999999-0402"}},
	{"corpus_ambiguous_date.pdf", "issue_date", "2026-03-12", []string{"2026-12-03"}},
	{"wild_two_party_bare_tin.pdf", "buyer_name", "Honeywell Group", []string{"TIN:"}},
	{"wild_scanned_no_number.pdf", "buyer_name", "7 AWOLOWO ROAD, IKOYI", []string{"TIN: 99999999-1202"}},
}

const (
	// dtDoubtTotal is how many of those rows EXTR-22 adds. Pinned as a number as well as by
	// name: a sixth doubtful cell must be argued for, not absorbed.
	dtDoubtTotal = 4

	// The one ambiguous cell that predates the doubt: two readings of one printed date.
	dtPreExistingLayout = "corpus_ambiguous_date.pdf"
	dtPreExistingField  = "issue_date"
)

// dtLayoutValues is one layout's decided value for each dtInScope field. "" is ReasonMissing.
// The doubt moves a reason and adds an alternative; it can move no value, and this is the
// walk that says so over every cell in its blast surface.
type dtLayoutValues struct {
	file   string
	values [3]string
}

var dtByLayout = []dtLayoutValues{
	{"corpus_inline_labels.pdf", [3]string{"99999999-0102", "Honeywell Group", "75.00"}},
	{"corpus_split_labels.pdf", [3]string{"99999999-0202", "Honeywell Group", "150.00"}},
	{"corpus_stacked_labels.pdf", [3]string{"99999999-0302", "Honeywell Group", ""}},
	{"corpus_two_column.pdf", [3]string{"99999999-0402", "Honeywell Group", ""}},
	{"corpus_ambiguous_date.pdf", [3]string{"", "", ""}},
	{"corpus_totals_block.pdf", [3]string{"", "", "375.00"}},
	{"wild_two_party_bare_tin.pdf", [3]string{"99999999-0802", "Honeywell Group", "90.00"}},
	{"wild_ruled_lines_totals.pdf", [3]string{"99999999-0902", "Honeywell Group", "600.00"}},
	{"wild_rc_due_naira.pdf", [3]string{"99999999-1002", "Honeywell Group", "187.50"}},
	{"wild_stacked_borderless.pdf", [3]string{"99999999-1102", "", ""}},
	{"wild_scanned_no_number.pdf", [3]string{"99999999-1202", "7 AWOLOWO ROAD, IKOYI", "135.00"}},
}

// dtRun is one layout through Resolve and Reconcile, with the token count that proves it was
// read at all.
func dtRun(t *testing.T, l bdLayout) ([]extraction.Candidate, []extraction.FieldResult, int) {
	t.Helper()
	pages, tokens := bdPages(t, l)
	if tokens == 0 {
		t.Fatalf("%s read 0 token(s); every reason it reports is the reason of an empty page", l.file)
	}
	cands := extraction.Resolve(pages, extraction.RuleSet{Tier1: extraction.Tier1Rules})
	return cands, extraction.Reconcile(extraction.Input{Candidates: cands}), tokens
}

// dtLayout finds one row of the walk table by name, so a spec naming a layout the table dropped
// fails loudly instead of walking nothing.
func dtLayout(t *testing.T, file string) bdLayout {
	t.Helper()
	for _, l := range bdByLayout {
		if l.file == file {
			return l
		}
	}
	t.Fatalf("bdByLayout does not name %s; this spec's whole subject is absent", file)
	return bdLayout{}
}

func dtFor(cands []extraction.Candidate, field string) []extraction.Candidate {
	var out []extraction.Candidate
	for _, c := range cands {
		if c.Field == field {
			out = append(out, c)
		}
	}
	return out
}

func dtDistinct(cands []extraction.Candidate) []string {
	var out []string
	for _, c := range cands {
		if !slices.Contains(out, c.Value) {
			out = append(out, c.Value)
		}
	}
	return out
}

func dtResult(t *testing.T, res []extraction.FieldResult, field string) extraction.FieldResult {
	t.Helper()
	for _, r := range res {
		if r.Name == field {
			return r
		}
	}
	t.Fatalf("Reconcile emitted no %s", field)
	return extraction.FieldResult{}
}

func dtValue(f extraction.FieldResult) string {
	if f.Value == nil {
		return ""
	}
	return *f.Value
}

func dtAltValues(alts []extraction.Field) []string {
	out := make([]string, 0, len(alts))
	for _, a := range alts {
		if a.Value == nil {
			out = append(out, "")
			continue
		}
		out = append(out, *a.Value)
	}
	return out
}

// AC-1, AC-2, AC-5. The complete ambiguous set over all eleven layouts, by name and by count,
// with the value each cell still decides. The count pin is what stops a sixth doubtful cell
// arriving unargued, and the floors are what stop the whole walk agreeing with itself.
func TestEndToEnd_TheDoubtfulCellsAreExactlyThePinnedFive(t *testing.T) {
	if len(dtByLayout) != len(bdByLayout) {
		t.Fatalf("dtByLayout names %d layout(s) against bdByLayout's %d; the two walks cover different corpora", len(dtByLayout), len(bdByLayout))
	}
	for i, l := range bdByLayout {
		if dtByLayout[i].file != l.file {
			t.Fatalf("dtByLayout[%d] is %s and bdByLayout[%d] is %s; the value table has drifted off the walk", i, dtByLayout[i].file, i, l.file)
		}
	}
	pre := 0
	for _, c := range dtAmbiguous {
		if c.layout == dtPreExistingLayout && c.field == dtPreExistingField {
			pre++
		}
	}
	if pre != 1 {
		t.Fatalf("the pinned set names the pre-existing %s/%s %d time(s), want 1", dtPreExistingLayout, dtPreExistingField, pre)
	}
	if got := len(dtAmbiguous) - pre; got != dtDoubtTotal {
		t.Fatalf("the pinned set holds %d doubtful cell(s) beside %s/%s, want %d", got, dtPreExistingLayout, dtPreExistingField, dtDoubtTotal)
	}
	for i, c := range dtAmbiguous {
		for _, d := range dtAmbiguous[i+1:] {
			if c.layout == d.layout && c.field == d.field {
				t.Fatalf("the pinned set names %s/%s twice; a repeat keeps the count while leaving a cell unwatched", c.layout, c.field)
			}
		}
	}

	walked, cells := 0, 0
	var got []dtCell
	for i, l := range bdByLayout {
		cands, res, _ := dtRun(t, l)
		walked++

		for _, r := range res {
			if r.Reason != extraction.ReasonAmbiguous {
				continue
			}
			// AC-2. Structurally impossible today -- one assignment site, behind the
			// len(deduped) < 2 gate -- and the review screen depends on it
			// (frontend/app/src/components/ExtractionFields.tsx renders chips, not an input).
			if len(r.Alternatives) == 0 {
				t.Errorf("%s reads %s ambiguous with no alternative; the reviewer is asked to choose between one thing", l.file, r.Name)
			}
			got = append(got, dtCell{l.file, r.Name, dtValue(r), dtAltValues(r.Alternatives)})
		}

		for fi, field := range dtInScope {
			cells++
			f := dtResult(t, res, field)
			if want := dtByLayout[i].values[fi]; dtValue(f) != want {
				t.Errorf("%s reads %s = %q, want %q; the doubt moves a reason and adds an alternative, never a value", l.file, field, dtValue(f), want)
			}
		}

		// All four doubtful heads are BELOW reads. A flag set for the rightward relation alone
		// -- the shape crossesALabel has -- would empty the doubt set and leave every
		// assertion above vacuously true.
		for _, want := range dtAmbiguous {
			if want.layout != l.file || want.field == dtPreExistingField {
				continue
			}
			below, rightward := 0, 0
			for _, c := range dtFor(cands, want.field) {
				if c.Adjacent && strings.HasSuffix(c.RuleID, dtBelowSuffix) {
					below++
				}
				if strings.HasSuffix(c.RuleID, dtRightSuffix) {
					rightward++
				}
			}
			if below < 2 {
				t.Errorf("%s/%s reaches %d adjacent below candidate(s), want at least 2 -- the doubted head and the alternative are both below reads", l.file, want.field, below)
			}
			if rightward != 0 {
				t.Errorf("%s/%s reaches %d rightward candidate(s); this cell is pinned as evidence that the doubt does not depend on the rightward relation", l.file, want.field, rightward)
			}
		}
	}

	if walked != len(bdByLayout) {
		t.Fatalf("walked %d of %d layout(s)", walked, len(bdByLayout))
	}
	if want := len(bdByLayout) * len(dtInScope); cells != want {
		t.Fatalf("read %d in-scope cell(s), want %d", cells, want)
	}

	if len(got) != len(dtAmbiguous) {
		t.Fatalf("the corpus reads %d ambiguous cell(s) %v, want the pinned %d %v", len(got), got, len(dtAmbiguous), dtAmbiguous)
	}
	for i, c := range got {
		want := dtAmbiguous[i]
		if c.layout != want.layout || c.field != want.field {
			t.Errorf("ambiguous cell %d is %s/%s, want %s/%s", i, c.layout, c.field, want.layout, want.field)
			continue
		}
		if c.value != want.value {
			t.Errorf("%s/%s decides %q, want %q", c.layout, c.field, c.value, want.value)
		}
		if !slices.Equal(c.alts, want.alts) {
			t.Errorf("%s/%s offers %q, want %q", c.layout, c.field, c.alts, want.alts)
		}
	}
}

// AC-4. wild_rc_due_naira.pdf's issue_date is the exact shape the doubt catches -- an adjacent
// generic head with a second distinct value -- and it stays decided only because issue_date is
// outside the scope list. Green before and after: the non-vacuity clause is what makes it an
// oracle rather than decoration.
func TestEndToEnd_TheRCLayoutsCompetingDatesStayDecided(t *testing.T) {
	const layout, field = "wild_rc_due_naira.pdf", "issue_date"
	cands, res, _ := dtRun(t, dtLayout(t, layout))

	dates := dtFor(cands, field)
	if len(dates) == 0 {
		t.Fatalf("%s reaches no %s candidate; there is no competition to be decided over", layout, field)
	}
	if want := []string{"2026-07-08", "2026-08-07"}; !slices.Equal(dtDistinct(dates), want) {
		t.Fatalf("%s reaches %q for %s, want %q -- without a second distinct value the zero below is earned by the count, not by the scope", layout, dtDistinct(dates), field, want)
	}
	for _, c := range dates {
		if !c.Adjacent || c.Tier != extraction.TierGeneric {
			t.Fatalf("%s reads %s = %q via %s at tier %d adjacent=%v, want an adjacent generic read -- an out-of-scope field that is not a doubt candidate proves nothing about the scope", layout, field, c.Value, c.RuleID, c.Tier, c.Adjacent)
		}
	}

	f := dtResult(t, res, field)
	if dtValue(f) != "2026-07-08" {
		t.Errorf("%s reads %s = %q, want %q", layout, field, dtValue(f), "2026-07-08")
	}
	if f.Reason != extraction.ReasonNone {
		t.Errorf("%s reads %s %q, want %q -- the doubt covers %v and nothing else", layout, field, f.Reason, extraction.ReasonNone, dtInScope)
	}
	if len(f.Alternatives) != 0 {
		t.Errorf("%s offers %q as alternatives for %s, want none", layout, dtAltValues(f.Alternatives), field)
	}
}

// AC-3. A head read from inside its own label token is corroborated by the label, so a second
// distinct value beside it is not a doubt. Both layouts reach "Currency: NGN" through an
// adjacent generic rule and stay decided anyway.
func TestEndToEnd_ASameTokenHeadIsNotDoubtedEvenWithACompetitor(t *testing.T) {
	const field, want = "buyer_name", "Honeywell Group"
	for _, layout := range []string{"corpus_inline_labels.pdf", "wild_ruled_lines_totals.pdf"} {
		t.Run(layout, func(t *testing.T) {
			cands, res, _ := dtRun(t, dtLayout(t, layout))

			names := dtFor(cands, field)
			if got := dtDistinct(names); len(got) < 2 {
				t.Fatalf("%s reaches %q for %s; with one distinct value the zero below is the count answering, not the same_token head", layout, got, field)
			}
			sameToken, adjacentOther := 0, 0
			for _, c := range names {
				switch {
				case !c.Adjacent && c.Value == want:
					sameToken++
				case c.Adjacent && c.Value != want:
					adjacentOther++
				}
			}
			if sameToken != 1 {
				t.Fatalf("%s reaches %d non-adjacent %s candidate(s) reading %q, want exactly 1 -- the head must be the same_token read", layout, sameToken, field, want)
			}
			if adjacentOther == 0 {
				t.Fatalf("%s reaches no adjacent %s candidate carrying another value; nothing here could have been offered as an alternative", layout, field)
			}

			f := dtResult(t, res, field)
			if dtValue(f) != want {
				t.Errorf("%s reads %s = %q, want %q", layout, field, dtValue(f), want)
			}
			if f.Reason != extraction.ReasonNone {
				t.Errorf("%s reads %s %q, want %q -- a same_token head is its own corroboration", layout, field, f.Reason, extraction.ReasonNone)
			}
			if len(f.Alternatives) != 0 {
				t.Errorf("%s offers %q as alternatives for %s, want none", layout, dtAltValues(f.Alternatives), field)
			}
		})
	}
}

// doubt_test.go: EXTR-22's doubt pass against the shipped corpus. Which cells read
// ReasonAmbiguous, which adjacent heads earn their ReasonNone, and that no in-scope value
// moves. No database.
//
// The fourth column is EXTR-23's: total joined the doubt scope; EXTR-29's found total is the one
// value it moves, on the two ruled fixtures below. Its candidate COUNTS live in total_test.go.
//
// Every walk here sources each layout the way bdByLayout does -- pdfium for thirteen, the committed
// docling golden for the image-only one, which reads zero pdfium tokens and would otherwise
// contribute an empty answer that agrees with any expectation.
package endtoend

import (
	"slices"
	"strings"
	"testing"

	"github.com/SimonOsipov/invoice-os/internal/extraction"
)

// dtColumnFields is the whole doubt scope, one dtByLayout column each, in that order. total's
// column is measured, never idealised: the ruled rows read the printed 8,600.00, arithmetic's
// found total, matching score_test.go's expectByLayout pin. The six corpus_* rows have a twin in
// another package -- corpusPinned, reconcile_corpus_test.go.
var dtColumnFields = [4]string{"buyer_tin", "buyer_name", "vat", "total"}

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

// dtAmbiguous is EVERY cell the fourteen layouts read as ReasonAmbiguous, in walk order: five
// buyer_name doubts (EXTR-22), one pre-existing date tie (corpus_ambiguous_date.pdf, untouched by
// the doubt), and two found totals (EXTR-29, arithmetic on the ruled fixtures).
var dtAmbiguous = []dtCell{
	{"corpus_stacked_labels.pdf", "buyer_name", "Honeywell Group", []string{"99999999-0302"}},
	{"corpus_two_column.pdf", "buyer_name", "Honeywell Group", []string{"TIN: 99999999-0402"}},
	{"corpus_ambiguous_date.pdf", "issue_date", "2026-03-12", []string{"2026-12-03"}},
	{"wild_two_party_bare_tin.pdf", "buyer_name", "Honeywell Group", []string{"TIN:"}},
	{"wild_ruled_lines_totals.pdf", "total", "8600.00", []string{"1000.00"}},
	{"wild_scanned_no_number.pdf", "buyer_name", "7 AWOLOWO ROAD, IKOYI", []string{"TIN: 99999999-1202"}},
	{"wild_two_party_bare_tin_asprinted.pdf", "buyer_name", "Honeywell Group", []string{"TIN:"}},
	{"wild_ruled_lines_totals_asprinted.pdf", "total", "8600.00", []string{"1000.00"}},
}

const (
	// dtDoubtTotal is how many of those rows EXTR-22 adds; a seventh cell is argued for, not absorbed.
	// The fifth is the sibling's own doubt on its twin's already-doubted geometry.
	dtDoubtTotal = 5

	// dtFoundTotal is EXTR-29's found total: one per ruled fixture, arithmetic's own doubt.
	dtFoundTotal = 2

	// The one ambiguous cell that predates the doubt: two readings of one printed date.
	dtPreExistingLayout = "corpus_ambiguous_date.pdf"
	dtPreExistingField  = "issue_date"
)

// dtLayoutValues is one layout's decided value for each dtColumnFields field. "" is ReasonMissing.
// The doubt moves no value, the found total moves only the total column, and the drop band moves
// the stacked pair's rows; this is the walk that says so over every cell in its blast surface.
type dtLayoutValues struct {
	file   string
	values [4]string
}

var dtByLayout = []dtLayoutValues{
	{"corpus_inline_labels.pdf", [4]string{"99999999-0102", "Honeywell Group", "75.00", "1075.00"}},
	{"corpus_split_labels.pdf", [4]string{"99999999-0202", "Honeywell Group", "150.00", "2150.00"}},
	{"corpus_stacked_labels.pdf", [4]string{"99999999-0302", "Honeywell Group", "", "3225.00"}},
	{"corpus_two_column.pdf", [4]string{"99999999-0402", "Honeywell Group", "", "6450.00"}},
	{"corpus_ambiguous_date.pdf", [4]string{"", "", "", "4300.00"}},
	{"corpus_totals_block.pdf", [4]string{"", "", "375.00", "5375.00"}},
	{"wild_two_party_bare_tin.pdf", [4]string{"99999999-0802", "Honeywell Group", "90.00", "1290.00"}},
	{"wild_ruled_lines_totals.pdf", [4]string{"99999999-0902", "Honeywell Group", "600.00", "8600.00"}},
	{"wild_rc_due_naira.pdf", [4]string{"99999999-1002", "Honeywell Group", "187.50", "2687.50"}},
	{"wild_stacked_borderless.pdf", [4]string{"99999999-1102", "Honeywell Group", "112.50", "1612.50"}},
	{"wild_scanned_no_number.pdf", [4]string{"99999999-1202", "7 AWOLOWO ROAD, IKOYI", "135.00", "1935.00"}},
	{"wild_two_party_bare_tin_asprinted.pdf", [4]string{"99999999-0802", "Honeywell Group", "90.00", "1290.00"}},
	{"wild_ruled_lines_totals_asprinted.pdf", [4]string{"99999999-0902", "Honeywell Group", "600.00", "8600.00"}},
	{"wild_stacked_borderless_asprinted.pdf", [4]string{"99999999-1102", "Honeywell Group", "112.50", "1612.50"}},
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
	return cands, extraction.Reconcile(extraction.Input{Candidates: cands, Pages: pages}), tokens
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

// AC-1, AC-2, AC-5. The complete ambiguous set over all fourteen layouts, by name and by count,
// with the value each cell still decides. The count pin is what stops a ninth doubtful cell
// arriving unargued, and the floors are what stop the whole walk agreeing with itself.
func TestEndToEnd_TheDoubtfulCellsAreExactlyThePinnedEight(t *testing.T) {
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
	if got := len(dtAmbiguous) - pre; got != dtDoubtTotal+dtFoundTotal {
		t.Fatalf("the pinned set holds %d doubtful cell(s) beside %s/%s, want %d", got, dtPreExistingLayout, dtPreExistingField, dtDoubtTotal+dtFoundTotal)
	}
	found := 0
	for _, c := range dtAmbiguous {
		if c.field == ttField {
			found++
		}
	}
	if found != dtFoundTotal {
		t.Fatalf("the pinned set holds %d found-total cell(s), want %d; the doubt total and the found total cannot absorb one another", found, dtFoundTotal)
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
			// AC-2. Two assignment sites can reach this today -- decideField behind the
			// len(deduped) < 2 gate, findTotal behind matches == 1 -- and the review screen
			// depends on it (frontend/app/src/components/ExtractionFields.tsx renders chips, not
			// an input).
			if len(r.Alternatives) == 0 {
				t.Errorf("%s reads %s ambiguous with no alternative; the reviewer is asked to choose between one thing", l.file, r.Name)
			}
			got = append(got, dtCell{l.file, r.Name, dtValue(r), dtAltValues(r.Alternatives)})
		}

		for fi, field := range dtColumnFields {
			cells++
			f := dtResult(t, res, field)
			if want := dtByLayout[i].values[fi]; dtValue(f) != want {
				t.Errorf("%s reads %s = %q, want %q; the doubt moves no value, and the found total moves only the total column", l.file, field, dtValue(f), want)
			}
		}

		// All five doubtful heads are BELOW reads. A flag set for the rightward relation alone
		// -- the shape crossesALabel has -- would empty the doubt set and leave every
		// assertion above vacuously true.
		for _, want := range dtAmbiguous {
			if want.layout != l.file {
				continue
			}
			if want.field == ttField {
				// The found total is arithmetic over the whole page, not a below/rightward
				// adjacent read: its only reachable candidate is the anchored rival in alts.
				totals := dtFor(cands, want.field)
				if len(totals) != 1 || len(want.alts) != 1 || totals[0].Value != want.alts[0] {
					t.Errorf("%s/%s reaches %d total candidate(s) %v, want exactly 1 matching alternative %v", l.file, want.field, len(totals), dtDistinct(totals), want.alts)
				}
			}
			if want.field == dtPreExistingField || want.field == ttField {
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
	if want := len(bdByLayout) * len(dtColumnFields); cells != want {
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

// AC-4, AC-4.12. issue_date's doubt-scope exemption -- an adjacent generic head with a second
// distinct value still decides, because issue_date sits outside doubtfulFields -- needs a
// witness with two distinct issue_date candidates. wild_rc_due_naira.pdf was that witness until
// EXTR-25-04's due_date entry refused one of its two competing reads, so this reproduces the
// shape synthetically instead: two labels reaching issue_date at unequal distances, neither of
// them "Due Date", so the new entry cannot touch this page. Green before AND after -- it is a
// replacement oracle, not a red test for the due_date feature itself.
func TestEndToEnd_TheRCLayoutsCompetingDatesStayDecidedSynthetically(t *testing.T) {
	pages := []extraction.TokenPage{{Number: 1, WidthPt: 612, HeightPt: 792, Tokens: []extraction.Token{
		{Text: "Issue Date", Region: extraction.Region{Page: 1, X0: 0.10, Y0: 0.10, X1: 0.20, Y1: 0.13}},
		{Text: "2026-07-08", Region: extraction.Region{Page: 1, X0: 0.24, Y0: 0.10, X1: 0.34, Y1: 0.13}},
		{Text: "Delivery Date", Region: extraction.Region{Page: 1, X0: 0.10, Y0: 0.20, X1: 0.23, Y1: 0.23}},
		{Text: "2026-08-07", Region: extraction.Region{Page: 1, X0: 0.30, Y0: 0.20, X1: 0.40, Y1: 0.23}},
	}}}

	cands := extraction.Resolve(pages, extraction.RuleSet{Tier1: extraction.Tier1Rules})
	dates := dtFor(cands, "issue_date")
	if len(dates) == 0 {
		t.Fatal("the synthetic page reaches no issue_date candidate; there is no competition to be decided over")
	}
	if want := []string{"2026-07-08", "2026-08-07"}; !slices.Equal(dtDistinct(dates), want) {
		t.Fatalf("issue_date reaches %q, want %q -- without a second distinct value the zero below is earned by the count, not by the scope", dtDistinct(dates), want)
	}
	for _, c := range dates {
		if !c.Adjacent || c.Tier != extraction.TierGeneric {
			t.Fatalf("issue_date reads %q via %s at tier %d adjacent=%v, want an adjacent generic read", c.Value, c.RuleID, c.Tier, c.Adjacent)
		}
	}
	if dates[0].Distance == dates[1].Distance {
		t.Fatalf("both reads sit at distance %v; the two must be unequal or decideField's equal-standing group ties them into a doubt", dates[0].Distance)
	}

	f := dtResult(t, extraction.Reconcile(extraction.Input{Candidates: cands}), "issue_date")
	if dtValue(f) != "2026-07-08" {
		t.Errorf("issue_date = %q, want %q -- the closer read must win outright", dtValue(f), "2026-07-08")
	}
	if f.Reason != extraction.ReasonNone {
		t.Errorf("issue_date reason = %q, want %q -- issue_date is outside doubtfulFields", f.Reason, extraction.ReasonNone)
	}
	if len(f.Alternatives) != 0 {
		t.Errorf("issue_date offers %q as alternatives, want none", dtAltValues(f.Alternatives))
	}
}

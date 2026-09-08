// boundary_test.go: the intervening-label boundary against the shipped corpus. No database.
//
// AC-4 asserts an ABSENCE -- no rightward candidate moves on any layout -- so it is green
// before AND after by construction. Three things make it evidence rather than decoration: it
// ships in the RED commit, before crossesALabel exists; every layout's contribution is pinned
// cell by cell rather than counted; and the same comparison helper is run over an arrangement
// that MUST report a difference.
package endtoend

import (
	"fmt"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/SimonOsipov/invoice-os/internal/extraction"
)

// bdRow is one rightward candidate. Two rows are the same candidate only when all five cells
// agree, so a moved distance or a re-pointed rule is a difference, not a match.
type bdRow struct {
	field    string
	value    string
	ruleID   string
	tier     extraction.Tier
	distance string // %.6f, so the comparison is exact and the message is readable
}

func (r bdRow) String() string {
	return fmt.Sprintf("%s=%q via %s tier=%d d=%s", r.field, r.value, r.ruleID, r.tier, r.distance)
}

func bdMake(field, value, ruleID string, distance float64) bdRow {
	return bdRow{field: field, value: value, ruleID: ruleID, tier: extraction.TierGeneric, distance: strconv.FormatFloat(distance, 'f', 6, 64)}
}

// bdRightwardSuffix is what marks a candidate the boundary could ever remove. The predicate is
// applied to RelRight alone, so nothing else is in its blast surface.
const bdRightwardSuffix = ".right"

// bdRightward is every rightward candidate in Resolve's own output order.
func bdRightward(cands []extraction.Candidate) []bdRow {
	out := []bdRow{}
	for _, c := range cands {
		if !strings.HasSuffix(c.RuleID, bdRightwardSuffix) {
			continue
		}
		out = append(out, bdRow{
			field: c.Field, value: c.Value, ruleID: c.RuleID, tier: c.Tier,
			distance: strconv.FormatFloat(c.Distance, 'f', 6, 64),
		})
	}
	return out
}

// bdDiff is the multiset symmetric difference: rows got holds and want does not, and the other
// way round. A changed cell shows up as one of each, never as a match.
func bdDiff(got, want []bdRow) (extra, missing []bdRow) {
	pool := slices.Clone(want)
	for _, g := range got {
		if i := slices.Index(pool, g); i >= 0 {
			pool = slices.Delete(pool, i, i+1)
			continue
		}
		extra = append(extra, g)
	}
	return extra, pool
}

func bdShow(rows []bdRow) string {
	out := make([]string, len(rows))
	for i, r := range rows {
		out[i] = r.String()
	}
	return "[" + strings.Join(out, ", ") + "]"
}

// bdLayout is one layout's whole rightward contribution.
type bdLayout struct {
	file string
	// golden sources the layout from its committed docling golden instead of pdfium. The
	// scanned arrangement is image-only: pdfium reads ZERO tokens off it, so a pdfium-sourced
	// walk compares an empty set to an empty set on that row and calls it agreement.
	golden bool
	rows   []bdRow
}

// bdRightwardTotal is the whole corpus's rightward candidate count. The blast surface, not a
// summary: the boundary can only ever remove a rightward candidate.
const bdRightwardTotal = 29

// bdSilentLayouts read no value rightward at all, so crossesALabel is never called on them.
// Named here because "the boundary moved nothing on all eleven layouts" has an effective
// denominator of six.
var bdSilentLayouts = []string{
	"corpus_inline_labels.pdf",
	"corpus_stacked_labels.pdf",
	"corpus_two_column.pdf",
	"corpus_ambiguous_date.pdf",
	"wild_stacked_borderless.pdf",
}

// bdByLayout is the pinned table, measured at f707543a with the shipped Tier-1 set.
var bdByLayout = []bdLayout{
	{file: "corpus_inline_labels.pdf"},
	{file: "corpus_split_labels.pdf", rows: []bdRow{
		bdMake("invoice_number", "INV-1002", "t1.invoice_number.right", 0.151673),
		bdMake("issue_date", "2026-04-15", "t1.issue_date.right", 0.135614),
		bdMake("supplier_tin", "99999999-0201", "t1.supplier_tin.right", 0.135183),
		bdMake("supplier_name", "Adeyemi Trading Limited", "t1.supplier_name.right", 0.170203),
		bdMake("buyer_tin", "99999999-0202", "t1.buyer_tin.right", 0.155889),
		bdMake("buyer_name", "Honeywell Group", "t1.buyer_name.right", 0.192144),
		bdMake("currency", "NGN", "t1.currency.right", 0.164007),
		bdMake("subtotal", "2000.00", "t1.subtotal.right", 0.165183),
		bdMake("vat", "150.00", "t1.vat.right", 0.205948),
		bdMake("total", "2150.00", "t1.total.right", 0.200065),
	}},
	{file: "corpus_stacked_labels.pdf"},
	{file: "corpus_two_column.pdf"},
	{file: "corpus_ambiguous_date.pdf"},
	{file: "corpus_totals_block.pdf", rows: []bdRow{
		bdMake("subtotal", "5000.00", "t1.subtotal.right", 0.119549),
		bdMake("vat", "375.00", "t1.vat.right", 0.158882),
		bdMake("total", "5375.00", "t1.total.right", 0.154431),
	}},
	{file: "wild_two_party_bare_tin.pdf", rows: []bdRow{
		bdMake("supplier_tin", "99999999-0801", "t1.tin.right", 0.109281),
		bdMake("buyer_tin", "99999999-0802", "t1.tin.right", 0.109281),
		bdMake("subtotal", "1200.00", "t1.subtotal.right", 0.166654),
		bdMake("vat", "90.00", "t1.vat.right", 0.204791),
		bdMake("total", "1290.00", "t1.total.right", 0.201536),
	}},
	{file: "wild_ruled_lines_totals.pdf", rows: []bdRow{
		bdMake("subtotal", "8000.00", "t1.subtotal.right", 0.119667),
		bdMake("vat", "600.00", "t1.vat.right", 0.158961),
		// EXTR-23's cell: the grand total is 8,600.00 and this is the third line amount.
		bdMake("total", wildRuledLastLineAmount, "t1.total.right", 0.047611),
	}},
	{file: "wild_rc_due_naira.pdf", rows: []bdRow{
		bdMake("issue_date", "2026-07-08", "t1.issue_date.right", 0.149399),
		bdMake("issue_date", "2026-08-07", "t1.issue_date.right", 0.160301),
		bdMake("subtotal", "2500.00", "t1.subtotal.right", 0.164673),
		bdMake("vat", "187.50", "t1.vat.right", 0.203967),
		bdMake("total", "2687.50", "t1.total.right", 0.199556),
	}},
	{file: "wild_stacked_borderless.pdf"},
	{file: "wild_scanned_no_number.pdf", golden: true, rows: []bdRow{
		bdMake("subtotal", "1800.00", "t1.subtotal.right", 0.100218),
		bdMake("vat", "135.00", "t1.vat.right", 0.139434),
		bdMake("total", "1935.00", "t1.total.right", 0.099673),
	}},
}

// bdGoldenTokenPages replays one layout's committed golden through the real DoclingReader and
// collects its token pages the way the worker does.
func bdGoldenTokenPages(t *testing.T, layout string) []extraction.TokenPage {
	t.Helper()
	var pages []extraction.TokenPage
	r := eeGoldenReader(t, wildGolden(layout))
	if _, err := r.Read(t.Context(), extraction.Document{ContentType: eeContentType}, extraction.CollectTokens(&pages)); err != nil {
		t.Fatalf("replay the golden for %s: %v", layout, err)
	}
	return pages
}

// bdPages is one layout's token pages from the reader its row names, with the token count that
// proves the row was read at all.
func bdPages(t *testing.T, l bdLayout) ([]extraction.TokenPage, int) {
	t.Helper()
	var pages []extraction.TokenPage
	if l.golden {
		pages = bdGoldenTokenPages(t, l.file)
	} else {
		pages = eeTokenPages(t, l.file)
	}
	tokens := 0
	for _, p := range pages {
		tokens += len(p.Tokens)
	}
	return pages, tokens
}

// AC-4. Every rightward candidate the corpus produces, cell by cell, so this commit and the one
// that adds the boundary can be compared. The floors are what stop it agreeing with itself: a
// layout that read nothing, or a walk that shrank, would otherwise report no difference.
func TestEndToEnd_TheRightwardCandidateSetIsUnmovedByTheBoundary(t *testing.T) {
	if len(bdByLayout) != len(expectByLayout) {
		t.Fatalf("bdByLayout names %d layout(s) against expectByLayout's %d; the walk covers a different corpus", len(bdByLayout), len(expectByLayout))
	}
	for i, l := range bdByLayout {
		if l.file != expectByLayout[i].file {
			t.Fatalf("bdByLayout[%d] is %s and expectByLayout[%d] is %s; the two tables have drifted apart", i, l.file, i, expectByLayout[i].file)
		}
	}

	pinned := 0
	for _, l := range bdByLayout {
		pinned += len(l.rows)
	}
	if pinned != bdRightwardTotal {
		t.Fatalf("the pinned table holds %d rightward candidate(s), want %d; a row cannot leave the walk silently", pinned, bdRightwardTotal)
	}

	walked := 0
	for _, l := range bdByLayout {
		pages, tokens := bdPages(t, l)
		if tokens == 0 {
			t.Fatalf("%s read 0 token(s); its rightward set is empty for a reason that is not the boundary, and comparing it to anything proves nothing", l.file)
		}
		walked++

		got := bdRightward(extraction.Resolve(pages, extraction.RuleSet{Tier1: extraction.Tier1Rules}))
		extra, missing := bdDiff(got, l.rows)
		for _, r := range extra {
			t.Errorf("%s produced %s, which the pinned table does not hold", l.file, r)
		}
		for _, r := range missing {
			t.Errorf("%s no longer produces %s", l.file, r)
		}
		if len(extra) == 0 && len(missing) == 0 && !slices.Equal(got, l.rows) {
			t.Errorf("%s produced the pinned rows in a different order: %s, want %s", l.file, bdShow(got), bdShow(l.rows))
		}

		silent := slices.Contains(bdSilentLayouts, l.file)
		if silent && len(got) != 0 {
			t.Errorf("%s is listed as reading nothing rightward and produced %s; the named zeros are what make the vacuity visible", l.file, bdShow(got))
		}
		if !silent && len(got) == 0 {
			t.Errorf("%s produced no rightward candidate and is not listed as one of the layouts that read none; the walk would agree with an empty expectation", l.file)
		}
	}
	if walked != len(bdByLayout) {
		t.Fatalf("walked %d of %d layout(s)", walked, len(bdByLayout))
	}

	// The named zeros, as a list: five of eleven layouts call crossesALabel not once, so every
	// "over all eleven layouts" claim about a rightward behaviour has a denominator of six.
	if len(slices.Compact(slices.Sorted(slices.Values(bdSilentLayouts)))) != len(bdSilentLayouts) {
		t.Fatalf("bdSilentLayouts names a layout twice: %v; a repeat keeps the count while leaving a layout unwatched", bdSilentLayouts)
	}
	for _, name := range bdSilentLayouts {
		if !slices.ContainsFunc(bdByLayout, func(l bdLayout) bool { return l.file == name }) {
			t.Errorf("bdSilentLayouts names %s, which the walk does not cover", name)
		}
	}
	if got, want := len(bdByLayout)-len(bdSilentLayouts), 6; got != want {
		t.Errorf("%d of %d layouts read something rightward, want %d", got, len(bdByLayout), want)
	}
}

// bdScannedTokenFloor is what the committed golden yields for the image-only layout. pdfium
// yields zero, so this reds the moment the row is re-sourced through it.
const bdScannedTokenFloor = 10

// AC-4. The scanned arrangement has no text layer. Sourced the way the reach walk sources every
// other layout it contributes nothing, and its agreement with an empty expectation is an
// agreement between two empty sets.
func TestEndToEnd_TheScannedLayoutIsWalkedThroughItsGolden(t *testing.T) {
	var row bdLayout
	for _, l := range bdByLayout {
		if l.file == wildScanned {
			row = l
		}
	}
	if !row.golden {
		t.Fatalf("%s is not marked golden-sourced in bdByLayout; pdfium reads no token off it and its rows would be an empty comparison", wildScanned)
	}

	pages, tokens := bdPages(t, row)
	if tokens < bdScannedTokenFloor {
		t.Fatalf("%s read %d token(s) through its golden, want at least %d; the walk is not reading the layout", wildScanned, tokens, bdScannedTokenFloor)
	}
	if got := len(bdRightward(extraction.Resolve(pages, extraction.RuleSet{Tier1: extraction.Tier1Rules}))); got != len(row.rows) {
		t.Errorf("%s produced %d rightward candidate(s) through its golden, want %d", wildScanned, got, len(row.rows))
	}

	// The contrast that makes the golden sourcing load-bearing rather than a preference.
	if tokens := wildTokenCount(wildPDFiumPages(t, wildScanned)); tokens != 0 {
		t.Errorf("pdfium now reads %d token(s) off %s; the golden detour exists because it read none, so re-derive the rows before keeping it", tokens, wildScanned)
	}
}

// AC-4's discriminator. The eleven-layout walk above reports no difference; run the SAME helper
// over an arrangement whose rightward set the boundary MUST shrink, and it has to report one.
// Without this, "no difference" holds equally against a comparison that can never report one.
func TestEndToEnd_TheBoundaryComparisonDetectsASingleRemovedCandidate(t *testing.T) {
	pages := []extraction.TokenPage{{Number: 1, WidthPt: 612, HeightPt: 792, Tokens: []extraction.Token{
		{Text: "VAT", Region: extraction.Region{Page: 1, X0: 0.10, Y0: 0.70, X1: 0.16, Y1: 0.72}},
		{Text: "Total", Region: extraction.Region{Page: 1, X0: 0.30, Y0: 0.70, X1: 0.38, Y1: 0.72}},
		{Text: "2,687.50", Region: extraction.Region{Page: 1, X0: 0.45, Y0: 0.70, X1: 0.58, Y1: 0.72}},
	}}}
	// The expectation is what the arrangement read BEFORE the boundary: the VAT read of the
	// amount past the Total label, and the Total label's own read. Exactly one of them must go.
	blocked := bdMake("vat", "2687.50", "t1.vat.right", 0.290000)
	before := []bdRow{blocked, bdMake("total", "2687.50", "t1.total.right", 0.070000)}

	got := bdRightward(extraction.Resolve(pages, extraction.RuleSet{Tier1: extraction.Tier1Rules}))
	if len(got) == 0 {
		t.Fatal("the arrangement produced no rightward candidate at all; a difference of one below would be a difference from nothing")
	}

	extra, missing := bdDiff(got, before)
	if len(extra) != 0 {
		t.Fatalf("the arrangement produced %s, which it did not before; the difference this spec measures is not the one the boundary makes", bdShow(extra))
	}
	if !slices.Equal(missing, []bdRow{blocked}) {
		t.Errorf("the comparison reports %s missing against the pre-boundary set, want exactly %s; the eleven-layout walk's \"no difference\" holds equally against a comparison that can never report one", bdShow(missing), blocked)
	}
}

// bdRuledAnchor and bdRuledValue are the pair EXTR-23 owns: the Total label and the third line
// amount it wrongly takes for the grand total.
const (
	bdRuledAnchor = "Total"
	bdRuledValue  = "1,000.00"
)

// AC-5. wild_ruled_lines_totals.pdf still reads its total off the line amount. EXTR-23 owns that
// defect and this story's scope forbids touching it, so the assertion is that the boundary
// cannot reach the cell in either direction -- three tokens DO sit in the corridor between the
// two, and every one of them is off the anchor's band.
func TestWildLayouts_TheRuledTableTotalStillTakesTheLineAmount(t *testing.T) {
	pages := eeTokenPages(t, wildRuled)

	totals := []extraction.Candidate{}
	for _, c := range extraction.Resolve(pages, extraction.RuleSet{Tier1: extraction.Tier1Rules}) {
		if c.Field == "total" {
			totals = append(totals, c)
		}
	}
	if len(totals) != 1 {
		t.Fatalf("%s resolves total to %d candidate(s), want exactly 1", wildRuled, len(totals))
	}
	if got := bdRightward(totals); !slices.Equal(got, []bdRow{bdMake("total", wildRuledLastLineAmount, "t1.total.right", 0.047611)}) {
		t.Errorf("%s resolves total to %s, want the line amount at 0.047611; EXTR-23 owns this cell and the boundary must move it neither way", wildRuled, bdShow(got))
	}

	var anchor, value extraction.Region
	var sawAnchor, sawValue bool
	for _, p := range pages {
		for _, tok := range p.Tokens {
			switch tok.Text {
			case bdRuledAnchor:
				anchor, sawAnchor = tok.Region, true
			case bdRuledValue:
				value, sawValue = tok.Region, true
			}
		}
	}
	if !sawAnchor || !sawValue {
		t.Fatalf("%s carries %q=%v and %q=%v; the corridor below would be measured between boxes that are not there", wildRuled, bdRuledAnchor, sawAnchor, bdRuledValue, sawValue)
	}

	corridor := 0
	for _, p := range pages {
		for _, tok := range p.Tokens {
			b := tok.Region
			if b.X0 < anchor.X1 || b.X0 >= value.X0 {
				continue
			}
			corridor++
			ov := min(anchor.Y1, b.Y1) - max(anchor.Y0, b.Y0)
			span := min(anchor.Y1-anchor.Y0, b.Y1-b.Y0)
			if ov > 0 && ov >= 0.5*span {
				t.Errorf("%q sits between the %s label and the amount AND shares its band; the boundary reaches EXTR-23's cell after all", tok.Text, bdRuledAnchor)
			}
		}
	}
	if corridor == 0 {
		t.Errorf("no token sits between the %s label and %q; the band test above ran over nothing, so this spec would pass on a layout where the corridor is simply empty", bdRuledAnchor, bdRuledValue)
	}
}

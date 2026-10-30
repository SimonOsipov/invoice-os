package invoice

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/SimonOsipov/invoice-os/internal/submission"
)

const ncOrigID = "11111111-1111-4111-8111-111111111111"

func ncNote(dt submission.DocumentType, total *string) Invoice {
	return Invoice{DocumentType: dt, OriginalInvoiceID: strPtr(ncOrigID), Total: total, Currency: strPtr("NGN")}
}

// ncFacts is an accepted commercial original INV-1 with an IRN.
func ncFacts(total, credited string) originalFacts {
	return originalFacts{
		Found: true, Number: "INV-1", Currency: strPtr("NGN"), DocumentType: submission.CommercialInvoice,
		Status: StatusAccepted, IRN: strPtr("IRN-1"), Total: strPtr(total), CreditedBefore: credited,
	}
}

func ncKeys(vs []Violation) []string {
	keys := make([]string, 0, len(vs))
	for _, v := range vs {
		keys = append(keys, v.RuleKey)
	}
	return keys
}

// ncOnly asserts vs is exactly one violation with the key, severity error and the path.
func ncOnly(t *testing.T, vs []Violation, key, path string) Violation {
	t.Helper()
	if len(vs) != 1 || vs[0].RuleKey != key {
		t.Fatalf("keys = %v, want exactly [%s]", ncKeys(vs), key)
	}
	if vs[0].Severity != "error" {
		t.Errorf("%s severity = %q, want error", key, vs[0].Severity)
	}
	if vs[0].Path != path {
		t.Errorf("%s path = %q, want %q", key, vs[0].Path, path)
	}
	return vs[0]
}

func TestNoteViolations_CommercialInvoiceGetsNone(t *testing.T) {
	inv := Invoice{DocumentType: submission.CommercialInvoice, Total: strPtr("5.00")}
	if got := noteViolations(inv, originalFacts{}); got != nil {
		t.Errorf("commercial invoice, zero facts: %v, want nil", ncKeys(got))
	}
	if got := noteViolations(inv, originalFacts{Found: true, Status: StatusDraft}); got != nil {
		t.Errorf("commercial invoice, odd facts: %v, want nil", ncKeys(got))
	}
	cases := []struct {
		dt            submission.DocumentType
		note, locking bool
	}{
		{submission.CommercialInvoice, false, false},
		{"", false, false},
		{submission.CreditNote, true, true},
		{submission.DebitNote, true, false},
	}
	for _, c := range cases {
		inv := Invoice{DocumentType: c.dt}
		if got := isNote(inv); got != c.note {
			t.Errorf("isNote(%q) = %v, want %v", c.dt, got, c.note)
		}
		if got := locksOriginal(inv); got != c.locking {
			t.Errorf("locksOriginal(%q) = %v, want %v", c.dt, got, c.locking)
		}
	}
}

func TestNoteViolations_MissingOriginalReportsAlone(t *testing.T) {
	for _, dt := range []submission.DocumentType{submission.CreditNote, submission.DebitNote} {
		v := ncOnly(t, noteViolations(ncNote(dt, strPtr("999999.00")), originalFacts{}), "note-original-missing", "")
		if !strings.Contains(v.Message, ncOrigID) {
			t.Errorf("%s message %q does not name the id", dt, v.Message)
		}
	}
}

func TestNoteViolations_NoOriginalIDReportsMissing(t *testing.T) {
	for _, dt := range []submission.DocumentType{submission.CreditNote, submission.DebitNote} {
		inv := Invoice{DocumentType: dt, Total: strPtr("5.00")}
		v := ncOnly(t, noteViolations(inv, originalFacts{}), "note-original-missing", "")
		if v.Message != "This note names no original invoice." {
			t.Errorf("%s message = %q", dt, v.Message)
		}
	}
}

func TestNoteViolations_EachOriginalDefectNamesItsKeyAndTheOriginal(t *testing.T) {
	type tc struct {
		name string
		edit func(*originalFacts)
		key  string
	}
	var cases []tc
	for _, st := range []Status{StatusDraft, StatusValidated, StatusQueued, StatusSubmitted, StatusRejected, StatusFailed} {
		cases = append(cases, tc{"status " + string(st), func(f *originalFacts) { f.Status = st }, "note-original-not-accepted"})
	}
	for _, dt := range []submission.DocumentType{submission.CreditNote, submission.DebitNote} {
		cases = append(cases, tc{"type " + string(dt), func(f *originalFacts) { f.DocumentType = dt }, "note-original-not-commercial"})
	}
	cases = append(cases,
		tc{"irn nil", func(f *originalFacts) { f.IRN = nil }, "note-original-no-irn"},
		tc{"irn blank", func(f *originalFacts) { f.IRN = strPtr("  ") }, "note-original-no-irn"},
		tc{"irn tab", func(f *originalFacts) { f.IRN = strPtr("\t") }, "note-original-no-irn"},
	)
	for _, dt := range []submission.DocumentType{submission.CreditNote, submission.DebitNote} {
		if got := noteViolations(ncNote(dt, strPtr("1.00")), ncFacts("100.00", "0.00")); len(got) != 0 {
			t.Fatalf("%s baseline fires %v, want none", dt, ncKeys(got))
		}
		for _, c := range cases {
			f := ncFacts("100.00", "0.00")
			c.edit(&f)
			v := ncOnly(t, noteViolations(ncNote(dt, strPtr("1.00")), f), c.key, "")
			if !strings.Contains(v.Message, "INV-1") {
				t.Errorf("%s %s: message %q does not name the original", dt, c.name, v.Message)
			}
		}
	}
}

func TestNoteViolations_OriginalDefectsReportTogether(t *testing.T) {
	f := ncFacts("100.00", "0.00")
	f.Status, f.IRN = StatusSubmitted, nil
	got := ncKeys(noteViolations(ncNote(submission.CreditNote, strPtr("1.00")), f))
	slices.Sort(got)
	if want := []string{"note-original-no-irn", "note-original-not-accepted"}; !slices.Equal(got, want) {
		t.Errorf("keys = %v, want %v", got, want)
	}
	f.DocumentType = submission.DebitNote
	got = ncKeys(noteViolations(ncNote(submission.DebitNote, strPtr("1.00")), f))
	slices.Sort(got)
	want := []string{"note-original-no-irn", "note-original-not-accepted", "note-original-not-commercial"}
	if !slices.Equal(got, want) {
		t.Errorf("keys = %v, want %v", got, want)
	}
}

func TestNoteViolations_OverCapStatesTheAmountStillCreditable(t *testing.T) {
	v := ncOnly(t, noteViolations(ncNote(submission.CreditNote, strPtr("60.00")), ncFacts("100.00", "60.00")),
		"credit-note-over-cap", "total")
	if !strings.Contains(v.Message, "40.00") || !strings.Contains(v.Message, "INV-1") {
		t.Errorf("message %q must state 40.00 and name INV-1", v.Message)
	}
	// credited 30.00 and total 80.00 tell original-credited (70.00) from original-total (20.00).
	v = ncOnly(t, noteViolations(ncNote(submission.CreditNote, strPtr("80.00")), ncFacts("100.00", "30.00")),
		"credit-note-over-cap", "total")
	if !strings.Contains(v.Message, "70.00 still creditable") || !strings.Contains(v.Message, "total 80.00") {
		t.Errorf("message %q must state 80.00 against 70.00 creditable", v.Message)
	}
}

func TestNoteViolations_CapBoundary(t *testing.T) {
	cases := []struct {
		credited, total string
		over            bool
	}{
		{"60.00", "40.00", false},
		{"60.00", "40.01", true},
		{"0.00", "100.00", false},
		{"0.00", "100.01", true},
	}
	for _, c := range cases {
		got := noteViolations(ncNote(submission.CreditNote, strPtr(c.total)), ncFacts("100.00", c.credited))
		if c.over {
			ncOnly(t, got, "credit-note-over-cap", "total")
		} else if len(got) != 0 {
			t.Errorf("credited %s total %s: %v, want none", c.credited, c.total, ncKeys(got))
		}
	}
}

func TestNoteViolations_CreditableNeverPrintsNegative(t *testing.T) {
	v := ncOnly(t, noteViolations(ncNote(submission.CreditNote, strPtr("1.00")), ncFacts("100.00", "150.00")),
		"credit-note-over-cap", "total")
	if !strings.Contains(v.Message, "0.00") || strings.Contains(v.Message, "-50.00") {
		t.Errorf("message %q must state 0.00 creditable, not -50.00", v.Message)
	}
}

func TestNoteViolations_ComparesInDecimalNotFloat(t *testing.T) {
	if got := noteViolations(ncNote(submission.CreditNote, strPtr("0.20")), ncFacts("0.30", "0.10")); len(got) != 0 {
		t.Errorf("0.10 + 0.20 vs 0.30: %v, want none", ncKeys(got))
	}
	ncOnly(t, noteViolations(ncNote(submission.CreditNote, strPtr("90071992547409.94")), ncFacts("90071992547409.93", "0.00")),
		"credit-note-over-cap", "total")
}

func TestNoteViolations_UnusableTotal(t *testing.T) {
	for _, total := range []*string{nil, strPtr("NaN"), strPtr("abc"), strPtr("-0.01")} {
		ncOnly(t, noteViolations(ncNote(submission.CreditNote, total), ncFacts("100.00", "0.00")),
			"credit-note-total-unusable", "total")
	}
	// A zero total passes the cap even when the original is fully credited.
	if got := noteViolations(ncNote(submission.CreditNote, strPtr("0.00")), ncFacts("100.00", "100.00")); len(got) != 0 {
		t.Errorf("zero total: %v, want none", ncKeys(got))
	}
}

func TestNoteViolations_UnreadableCap(t *testing.T) {
	cases := []struct {
		name string
		edit func(*originalFacts)
	}{
		{"original total nil", func(f *originalFacts) { f.Total = nil }},
		{"original total NaN", func(f *originalFacts) { f.Total = strPtr("NaN") }},
		{"credited NaN", func(f *originalFacts) { f.CreditedBefore = "NaN" }},
		{"credited empty", func(f *originalFacts) { f.CreditedBefore = "" }},
	}
	for _, c := range cases {
		f := ncFacts("100.00", "0.00")
		c.edit(&f)
		v := ncOnly(t, noteViolations(ncNote(submission.CreditNote, strPtr("1.00")), f), "credit-note-cap-unreadable", "")
		if !strings.Contains(v.Message, "INV-1") {
			t.Errorf("%s: message %q does not name the original", c.name, v.Message)
		}
	}
}

func TestNoteViolations_DebitNoteHasNoCap(t *testing.T) {
	debit := func(total *string) Invoice { return ncNote(submission.DebitNote, total) }
	for _, total := range []*string{strPtr("999.00"), nil, strPtr("NaN"), strPtr("-5.00")} {
		if got := noteViolations(debit(total), ncFacts("1.00", "500.00")); len(got) != 0 {
			t.Errorf("total %v: %v, want none", total, ncKeys(got))
		}
	}
	f := ncFacts("NaN", "NaN")
	f.Total = nil
	if got := noteViolations(debit(strPtr("999.00")), f); len(got) != 0 {
		t.Errorf("unreadable cap on a debit note: %v, want none", ncKeys(got))
	}
	f = ncFacts("1.00", "500.00")
	f.IRN = nil
	ncOnly(t, noteViolations(debit(strPtr("999.00")), f), "note-original-no-irn", "")
}

func TestNoteViolations_CurrencyMismatchRefusesWithoutComparingAmounts(t *testing.T) {
	cases := []struct {
		name       string
		note, orig *string
		total      string
		wantKey    string
		wantInMsg  []string
	}{
		{"USD note on NGN original, amount within", strPtr("USD"), strPtr("NGN"), "5000.00", ruleCreditNoteCurrency, []string{"USD", "NGN", "INV-1"}},
		{"NGN note above the cap", strPtr("NGN"), strPtr("NGN"), "150000.00", ruleCreditNoteOverCap, nil},
		{"note currency NULL", nil, strPtr("NGN"), "5.00", ruleCreditNoteCurrency, []string{"INV-1"}},
		{"original currency NULL", strPtr("NGN"), nil, "5.00", ruleCreditNoteCurrency, []string{"INV-1"}},
		{"note currency blank", strPtr("  "), strPtr("NGN"), "5.00", ruleCreditNoteCurrency, []string{"INV-1"}},
		{"original currency blank", strPtr("NGN"), strPtr(""), "5.00", ruleCreditNoteCurrency, []string{"INV-1"}},
		{"both blank", strPtr(""), strPtr(""), "5.00", ruleCreditNoteCurrency, []string{"INV-1"}},
		{"case differs", strPtr("ngn"), strPtr("NGN"), "5.00", ruleCreditNoteCurrency, []string{"ngn", "NGN"}},
	}
	for _, c := range cases {
		inv := ncNote(submission.CreditNote, strPtr(c.total))
		inv.Currency = c.note
		f := ncFacts("100000.00", "0.00")
		f.Currency = c.orig
		path := "currency"
		if c.wantKey == ruleCreditNoteOverCap {
			path = "total"
		}
		v := ncOnly(t, noteViolations(inv, f), c.wantKey, path)
		for _, s := range c.wantInMsg {
			if !strings.Contains(v.Message, s) {
				t.Errorf("%s: message %q does not name %q", c.name, v.Message, s)
			}
		}
	}
}

func TestNoteViolations_CurrencyMismatchKeepsCollectingOtherChecks(t *testing.T) {
	inv := ncNote(submission.CreditNote, strPtr("5.00"))
	inv.Currency = strPtr("USD")
	f := ncFacts("100.00", "0.00")
	f.IRN = nil
	got := ncKeys(noteViolations(inv, f))
	slices.Sort(got)
	if want := []string{ruleCreditNoteCurrency, ruleNoteOriginalNoIRN}; !slices.Equal(got, want) {
		t.Errorf("keys = %v, want %v", got, want)
	}
}

func TestNoteViolations_DebitNoteInAnotherCurrencyHasNoKey(t *testing.T) {
	for _, c := range []*string{strPtr("USD"), nil, strPtr("")} {
		inv := ncNote(submission.DebitNote, strPtr("5.00"))
		inv.Currency = c
		if got := noteViolations(inv, ncFacts("100.00", "0.00")); len(got) != 0 {
			t.Errorf("debit note currency %v: %v, want none", c, ncKeys(got))
		}
	}
}

func TestWithoutNoteChecks_DropsExactlyTheEightKeys(t *testing.T) {
	keys := []string{
		"note-original-missing", "note-original-not-commercial", "note-original-not-accepted",
		"note-original-no-irn", "credit-note-total-unusable", "credit-note-cap-unreadable", "credit-note-over-cap",
		"credit-note-currency-mismatch",
	}
	var in []Violation
	for _, k := range keys {
		in = append(in, Violation{RuleKey: k, Severity: "error", Message: k})
	}
	in = append(in, Violation{RuleKey: "total-required", Severity: "error", Message: "keep 1"},
		Violation{RuleKey: "custom-x", Severity: "warning", Message: "keep 2"})
	got := withoutNoteChecks(in)
	if want := []string{"total-required", "custom-x"}; !slices.Equal(ncKeys(got), want) {
		t.Fatalf("keys = %v, want %v", ncKeys(got), want)
	}
	if got[0].Message != "keep 1" || got[1].Severity != "warning" {
		t.Errorf("kept violations were altered: %+v", got)
	}
}

func TestReadOriginalFacts_NilOriginalIsNotFound(t *testing.T) {
	got, err := readOriginalFactsTx(context.Background(), nil, nil, "", false)
	if err != nil || got.Found {
		t.Errorf("got %+v, %v; want Found=false, nil error", got, err)
	}
}

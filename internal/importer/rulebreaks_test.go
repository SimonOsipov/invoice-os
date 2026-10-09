package importer

import (
	"fmt"
	"reflect"
	"sort"
	"testing"

	"github.com/SimonOsipov/invoice-os/internal/invoice"
	"github.com/SimonOsipov/invoice-os/internal/invoicefields"
)

// rbReading is a settled extraction with a valued rank-0 row for each name.
func rbReading(names ...string) SettledExtraction {
	ex := SettledExtraction{JobID: "job-1", Fields: []extractedField{}}
	for _, n := range names {
		v := "value of " + n
		ex.Fields = append(ex.Fields, extractedField{Name: n, Value: &v})
	}
	return ex
}

func rbViolation(path, key string) invoice.Violation {
	return invoice.Violation{RuleKey: key, Severity: "error", Message: "message of " + key, Path: path}
}

func TestRuleBreaks_MapsEachPathToItsField(t *testing.T) {
	cases := []struct{ path, field string }{
		{"issue_date", "issue_date"},
		{"currency", "currency"},
		{"subtotal", "subtotal"},
		{"vat", "vat"},
		{"total", "total"},
		{"buyer.tin", "buyer_tin"},
		{"buyer.name", "buyer_name"},
	}
	for _, tc := range cases {
		t.Run(tc.path, func(t *testing.T) {
			got := ruleBreaks(rbReading(tc.field), []invoice.Violation{rbViolation(tc.path, "rule-"+tc.field)})
			want := []RuleBreak{{Field: tc.field, RuleKey: "rule-" + tc.field, Message: "message of rule-" + tc.field}}
			if !reflect.DeepEqual(got, want) {
				t.Errorf("ruleBreaks(%q) = %+v, want %+v", tc.path, got, want)
			}
		})
	}
}

func TestRuleBreaks_SkipsLockedLineAndUnknownPaths(t *testing.T) {
	// every name is valued, so only the path can be the reason a violation is skipped
	ex := rbReading("invoice_number", "supplier_tin", "supplier_name", "line_items", "line_items[1].description",
		"nope", "buyer_tin")
	skipped := []string{"invoice_number", "supplier.tin", "supplier.name", "line_items", "", "nope", "buyer_tin"}
	for _, path := range skipped {
		t.Run("path="+path, func(t *testing.T) {
			if got := ruleBreaks(ex, []invoice.Violation{rbViolation(path, "rule-x")}); len(got) != 0 {
				t.Errorf("ruleBreaks(path %q) = %+v, want none", path, got)
			}
		})
	}

	// positive control: the same reading flags buyer.tin, so the skips above are the path's doing
	all := []invoice.Violation{rbViolation("buyer.tin", "buyer-tin-format")}
	for _, path := range skipped {
		all = append(all, rbViolation(path, "rule-x"))
	}
	got := ruleBreaks(ex, all)
	want := []RuleBreak{{Field: "buyer_tin", RuleKey: "buyer-tin-format", Message: "message of buyer-tin-format"}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("ruleBreaks(mixed) = %+v, want only %+v", got, want)
	}
}

func TestRuleBreaks_MapsOnlyUnlockedExtractHeaderFields(t *testing.T) {
	// non-vacuity: later stories add paths, so the pin is on the seven that exist today
	for _, p := range []string{"issue_date", "currency", "subtotal", "vat", "total", "buyer.tin", "buyer.name"} {
		if _, ok := ruleBreakFields[p]; !ok {
			t.Errorf("path map has no entry for %q (Design § Violation -> field mapping)", p)
		}
	}

	header := map[string]bool{}
	for _, k := range invoicefields.ExtractHeaderKeys() {
		header[k] = true
	}
	for path, field := range ruleBreakFields {
		if !header[field] {
			t.Errorf("path %q maps to %q, which is not in invoicefields.ExtractHeaderKeys()", path, field)
		}
		switch field {
		case "invoice_number", "supplier_tin", "supplier_name":
			t.Errorf("path %q maps to the locked field %q", path, field)
		}
	}
}

func TestRuleBreaks_SkipsAFieldWithNoValue(t *testing.T) {
	vs := []invoice.Violation{rbViolation("buyer.tin", "buyer-tin-format")}

	nilValue := SettledExtraction{JobID: "job-1", Fields: []extractedField{{Name: "buyer_tin", Value: nil}}}
	if got := ruleBreaks(nilValue, vs); len(got) != 0 {
		t.Errorf("nil rank-0 value: ruleBreaks = %+v, want none", got)
	}
	noRow := rbReading("buyer_name")
	if got := ruleBreaks(noRow, vs); len(got) != 0 {
		t.Errorf("no buyer_tin row: ruleBreaks = %+v, want none", got)
	}

	if got := ruleBreaks(rbReading("buyer_tin"), vs); len(got) != 1 {
		t.Errorf("valued buyer_tin: ruleBreaks = %+v, want one (control for the two skips above)", got)
	}
}

func TestRuleBreaks_CollapsesADuplicate(t *testing.T) {
	ex := rbReading("vat", "buyer_tin")
	key := func(b RuleBreak) string { return fmt.Sprintf("%s/%s", b.Field, b.RuleKey) }
	keys := func(bs []RuleBreak) []string {
		var out []string
		for _, b := range bs {
			out = append(out, key(b))
		}
		sort.Strings(out)
		return out
	}

	same := ruleBreaks(ex, []invoice.Violation{rbViolation("vat", "vat-standard-rate"), rbViolation("vat", "vat-standard-rate")})
	if want := []string{"vat/vat-standard-rate"}; !reflect.DeepEqual(keys(same), want) {
		t.Errorf("the same (path, key) twice = %v, want %v", keys(same), want)
	}

	twoKeys := ruleBreaks(ex, []invoice.Violation{rbViolation("vat", "vat-standard-rate"), rbViolation("vat", "vat-not-negative")})
	if want := []string{"vat/vat-not-negative", "vat/vat-standard-rate"}; !reflect.DeepEqual(keys(twoKeys), want) {
		t.Errorf("two keys on vat = %v, want %v", keys(twoKeys), want)
	}

	twoFields := ruleBreaks(ex, []invoice.Violation{rbViolation("vat", "shared-key"), rbViolation("buyer.tin", "shared-key")})
	if want := []string{"buyer_tin/shared-key", "vat/shared-key"}; !reflect.DeepEqual(keys(twoFields), want) {
		t.Errorf("one key on two fields = %v, want %v", keys(twoFields), want)
	}
}

func TestRuleBreaks_EmptyInputsAndEverySeverity(t *testing.T) {
	ex := rbReading("vat", "buyer_tin")
	one := []invoice.Violation{rbViolation("vat", "vat-standard-rate")}
	if got := ruleBreaks(ex, one); len(got) != 1 {
		t.Fatalf("control: ruleBreaks = %+v, want one", got)
	}
	if got := ruleBreaks(ex, nil); len(got) != 0 {
		t.Errorf("nil violations: ruleBreaks = %+v, want none", got)
	}
	if got := ruleBreaks(ex, []invoice.Violation{}); len(got) != 0 {
		t.Errorf("empty violations: ruleBreaks = %+v, want none", got)
	}
	if got := ruleBreaks(SettledExtraction{JobID: "job-1"}, one); len(got) != 0 {
		t.Errorf("reading with no fields: ruleBreaks = %+v, want none", got)
	}

	for _, sev := range []string{"error", "warning", "info", ""} {
		v := rbViolation("vat", "vat-"+sev)
		v.Severity = sev
		if got := ruleBreaks(ex, []invoice.Violation{v}); len(got) != 1 || got[0].RuleKey != "vat-"+sev {
			t.Errorf("severity %q: ruleBreaks = %+v, want the violation recorded (every severity flags)", sev, got)
		}
	}
}

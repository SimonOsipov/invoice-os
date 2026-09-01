// reader_merge_internal_test.go: the correction merge is a pure function over the decided
// readings and the latest correction per field, so it has an oracle without a database.
package extraction

import (
	"encoding/json"
	"fmt"
	"reflect"
	"slices"
	"testing"
)

func mgStr(s string) *string { return &s }

var (
	mgR0       = &ExtractionRegion{Page: 1, X0: 0.11, Y0: 0.12, X1: 0.13, Y1: 0.14}
	mgAlt1     = &ExtractionRegion{Page: 1, X0: 0.21, Y0: 0.22, X1: 0.33, Y1: 0.34}
	mgAlt2     = &ExtractionRegion{Page: 2, X0: 0.41, Y0: 0.52, X1: 0.63, Y1: 0.74}
	mgBox      = Region{Page: 3, X0: 0.15, Y0: 0.26, X1: 0.37, Y1: 0.48}
	mgPointed  = &ExtractionRegion{Page: 3, X0: 0.15, Y0: 0.26, X1: 0.37, Y1: 0.48}
	mgDecoyR0  = &ExtractionRegion{Page: 1, X0: 0.01, Y0: 0.02, X1: 0.03, Y1: 0.04}
	mgDecoyAlt = &ExtractionRegion{Page: 4, X0: 0.71, Y0: 0.72, X1: 0.83, Y1: 0.84}
)

// mgRead is the extractor's own answer for one ambiguous field, rebuilt per case so a merge
// that mutates its input in place cannot leak across cases.
func mgRead() []ExtractionFieldState {
	return []ExtractionFieldState{{
		Name:   "total",
		Value:  mgStr("READ-A"),
		Region: mgR0,
		Reason: "ambiguous",
		Alternatives: []ExtractionCandidate{
			{Value: mgStr("ALT-1"), Region: mgAlt1},
			{Value: mgStr("ALT-2"), Region: mgAlt2},
		},
	}}
}

func mgCorrection(method CorrectionMethod, value string, superseded *string) Correction {
	return Correction{FieldName: "total", Value: value, Method: method, Superseded: superseded, Actor: "operator"}
}

// Every arm of the precedence table, over crafted inputs: the DB cases prove the query feeds
// the merge, this proves the merge itself.
func TestExtractionMerge_ResolvesEachMethodWithoutADatabase(t *testing.T) {
	for _, c := range []struct {
		name        string
		corrections []Correction
		want        ExtractionFieldState
	}{
		{
			name:        "no correction leaves the reading exactly as it is",
			corrections: nil,
			want:        mgRead()[0],
		},
		{
			name:        "typed takes the value and leaves the region",
			corrections: []Correction{mgCorrection(MethodTyped, "HUMAN-B", mgStr("HUMAN-OLD"))},
			want: ExtractionFieldState{
				Name: "total", Value: mgStr("HUMAN-B"), Region: mgR0, Reason: "",
				Alternatives: []ExtractionCandidate{},
				Corrected:    &ExtractionCorrected{Method: "typed", Was: mgStr("HUMAN-OLD")},
			},
		},
		{
			name:        "chosen takes the matched alternative's region",
			corrections: []Correction{mgCorrection(MethodChosen, "ALT-2", nil)},
			want: ExtractionFieldState{
				Name: "total", Value: mgStr("ALT-2"), Region: mgAlt2, Reason: "",
				Alternatives: []ExtractionCandidate{},
				Corrected:    &ExtractionCorrected{Method: "chosen", Was: mgStr("READ-A")},
			},
		},
		{
			name:        "chosen matching no alternative keeps the reading's region",
			corrections: []Correction{mgCorrection(MethodChosen, "HUMAN-NOMATCH", nil)},
			want: ExtractionFieldState{
				Name: "total", Value: mgStr("HUMAN-NOMATCH"), Region: mgR0, Reason: "",
				Alternatives: []ExtractionCandidate{},
				Corrected:    &ExtractionCorrected{Method: "chosen", Was: mgStr("READ-A")},
			},
		},
		{
			name: "pointed takes its own stored box and its anchor label",
			corrections: []Correction{func() Correction {
				c := mgCorrection(MethodPointed, "HUMAN-P", nil)
				box := mgBox
				c.Region = &box
				c.AnchorLabel = "Total due"
				return c
			}()},
			want: ExtractionFieldState{
				Name: "total", Value: mgStr("HUMAN-P"), Region: mgPointed, Reason: "",
				Alternatives: []ExtractionCandidate{},
				Corrected:    &ExtractionCorrected{Method: "pointed", Was: mgStr("READ-A"), Where: mgStr("Total due")},
			},
		},
		{
			name:        "undone resets to the reading and ignores its own value",
			corrections: []Correction{mgCorrection(MethodUndone, "UNDO-Z", mgStr("HUMAN-B"))},
			want:        mgRead()[0],
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			got := mergeCorrections(mgRead(), c.corrections)
			if len(got) != 1 {
				t.Fatalf("the merge returned %d field(s), want 1", len(got))
			}
			if !reflect.DeepEqual(got[0], c.want) {
				t.Errorf("the merge returned\n  %s\nwant\n  %s", mgShow(got[0]), mgShow(c.want))
			}
		})
	}

	t.Run("a correction naming no read field is appended after the read ones", func(t *testing.T) {
		got := mergeCorrections(mgRead(), []Correction{
			{FieldName: "currency", Value: "NGN", Method: MethodTyped, Actor: "operator"},
		})
		if len(got) != 2 {
			t.Fatalf("the merge returned %d field(s), the first being\n  %s\nwant 2 — a correction with "+
				"no reading is a human's value that already reached the invoice", len(got), mgShow(got[0]))
		}
		want := ExtractionFieldState{
			Name: "currency", Value: mgStr("NGN"), Reason: "",
			Alternatives: []ExtractionCandidate{},
			Corrected:    &ExtractionCorrected{Method: "typed"},
		}
		if got[0].Name != "total" {
			t.Errorf("the merge put %q first, want the read field total", got[0].Name)
		}
		if !reflect.DeepEqual(got[1], want) {
			t.Errorf("the synthesized field is\n  %s\nwant\n  %s", mgShow(got[1]), mgShow(want))
		}
	})

	// The corrections read hands its rows back in field_name order, so a sort fed by it can
	// never be observed to do anything. Fed unsorted, it can: the merge owns the output order.
	t.Run("synthesized fields are name-ordered whatever order the corrections arrive in", func(t *testing.T) {
		got := mergeCorrections(mgRead(), []Correction{
			{FieldName: "supplier_name", Value: "S", Method: MethodTyped, Actor: "operator"},
			{FieldName: "buyer_name", Value: "B", Method: MethodTyped, Actor: "operator"},
			{FieldName: "currency", Value: "NGN", Method: MethodTyped, Actor: "operator"},
		})
		want := []string{"total", "buyer_name", "currency", "supplier_name"}
		if names := mgNames(got); !slices.Equal(names, want) {
			t.Errorf("the merge returned fields %v, want %v — the read field first, then the "+
				"synthesized ones in name order, not the order they arrived in", names, want)
		}
	})

	// Two alternatives may legitimately hold one string — that is a shape of ambiguous — and the
	// row records which was clicked nowhere, so the rule is first match. Pinned, not assumed.
	t.Run("chosen takes the FIRST alternative whose value matches", func(t *testing.T) {
		fields := mgRead()
		fields[0].Alternatives = []ExtractionCandidate{
			{Value: mgStr("ALT-2"), Region: mgAlt1},
			{Value: mgStr("ALT-2"), Region: mgAlt2},
		}
		got := mergeCorrections(fields, []Correction{mgCorrection(MethodChosen, "ALT-2", nil)})
		if got[0].Region != mgAlt1 {
			t.Errorf("the chosen total highlights %s, want the first matching alternative %+v",
				mgShow(got[0]), *mgAlt1)
		}
	})

	// A correction's value is matched against ITS OWN field's alternatives. mgDecoy holds the
	// same string at a different box, and is deliberately the first field.
	t.Run("chosen matches the corrected field's alternatives, not a neighbour's", func(t *testing.T) {
		fields := append(mgDecoy(), mgRead()...)
		got := mergeCorrections(fields, []Correction{mgCorrection(MethodChosen, "ALT-2", nil)})
		if len(got) != 2 || got[1].Name != "total" {
			t.Fatalf("the merge returned %d field(s) %v, want [invoice_number total]", len(got), mgNames(got))
		}
		if got[1].Region != mgAlt2 {
			t.Errorf("the chosen total highlights %s, want total's own second alternative %+v — "+
				"invoice_number spells the same value at a different box", mgShow(got[1]), *mgAlt2)
		}
		if !reflect.DeepEqual(got[0], mgDecoy()[0]) {
			t.Errorf("the untouched invoice_number came back\n  %s\nwant\n  %s — a correction on total "+
				"settles total", mgShow(got[0]), mgShow(mgDecoy()[0]))
		}
	})
}

// mgDecoy is a second ambiguous field whose own alternative spells total's chosen value.
func mgDecoy() []ExtractionFieldState {
	return []ExtractionFieldState{{
		Name:         "invoice_number",
		Value:        mgStr("READ-N"),
		Region:       mgDecoyR0,
		Reason:       "ambiguous",
		Alternatives: []ExtractionCandidate{{Value: mgStr("ALT-2"), Region: mgDecoyAlt}},
	}}
}

func mgNames(fields []ExtractionFieldState) []string {
	out := make([]string, 0, len(fields))
	for _, f := range fields {
		out = append(out, f.Name)
	}
	return out
}

// mgShow renders a field state as the wire bytes, so a failure names values, not addresses.
func mgShow(f ExtractionFieldState) string {
	b, err := json.Marshal(f)
	if err != nil {
		return fmt.Sprintf("%+v", f)
	}
	return string(b)
}

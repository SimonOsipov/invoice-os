// reader_merge_internal_test.go: the correction merge is a pure function over the decided
// readings and the latest correction per field, so it has an oracle without a database.
package extraction

import (
	"encoding/json"
	"fmt"
	"reflect"
	"testing"
)

func mgStr(s string) *string { return &s }

var (
	mgR0      = &ExtractionRegion{Page: 1, X0: 0.11, Y0: 0.12, X1: 0.13, Y1: 0.14}
	mgAlt1    = &ExtractionRegion{Page: 1, X0: 0.21, Y0: 0.22, X1: 0.33, Y1: 0.34}
	mgAlt2    = &ExtractionRegion{Page: 2, X0: 0.41, Y0: 0.52, X1: 0.63, Y1: 0.74}
	mgBox     = Region{Page: 3, X0: 0.15, Y0: 0.26, X1: 0.37, Y1: 0.48}
	mgPointed = &ExtractionRegion{Page: 3, X0: 0.15, Y0: 0.26, X1: 0.37, Y1: 0.48}
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
}

// mgShow renders a field state as the wire bytes, so a failure names values, not addresses.
func mgShow(f ExtractionFieldState) string {
	b, err := json.Marshal(f)
	if err != nil {
		return fmt.Sprintf("%+v", f)
	}
	return string(b)
}

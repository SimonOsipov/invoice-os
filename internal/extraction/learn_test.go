// learn_test.go: O-06, O-07, O-09 -- the layout_anchors codec's happy path and AnchorLabelText
// against the real corpus. External package: every spec reaches only exported symbols.
package extraction_test

import (
	"reflect"
	"testing"

	"github.com/SimonOsipov/invoice-os/internal/extraction"
)

// O-06: marshal -> unmarshal of an observation list is the identity, across bands 0/1/2, a
// zero-area box and an empty Text; a nil or empty list marshals to "[]", never "null" -- the
// layout_anchors column's CHECK takes an array.
func TestAnchorObservationsCodec_RoundTripsTheIdentity(t *testing.T) {
	table := []extraction.AnchorObservation{
		{Label: "invoice_no", Text: "Invoice No", Page: 1, Band: 0, X0: 0.1194, Y0: 0.1179, X1: 0.3034, Y1: 0.1291},
		{Label: "buyer_name", Text: "Buyer", Page: 1, Band: 2, X0: 0.6550, Y0: 0.1937, X1: 0.7048, Y1: 0.2078},
		{Label: "supplier_tin", Text: "", Page: 1, Band: 1, X0: 0.5, Y0: 0.5, X1: 0.5, Y1: 0.5}, // zero-area box, empty Text
	}

	raw, err := extraction.MarshalAnchorObservations(table)
	if err != nil {
		t.Fatalf("MarshalAnchorObservations(table) error = %v, want nil", err)
	}

	got, err := extraction.UnmarshalAnchorObservations(raw)
	if err != nil {
		t.Fatalf("UnmarshalAnchorObservations(marshalled table) error = %v, want nil", err)
	}
	if !reflect.DeepEqual(got, table) {
		t.Errorf("round-tripped observations = %+v, want %+v", got, table)
	}

	for _, empty := range [][]extraction.AnchorObservation{nil, {}} {
		raw, err := extraction.MarshalAnchorObservations(empty)
		if err != nil {
			t.Fatalf("MarshalAnchorObservations(%#v) error = %v, want nil", empty, err)
		}
		if string(raw) != "[]" {
			t.Errorf("MarshalAnchorObservations(%#v) = %q, want \"[]\": the column's CHECK takes an array, never null", empty, string(raw))
		}
	}
}

// O-07: an array element carrying an unknown key decodes with no error, the unknown key
// ignored, and every known field intact -- forward compatibility, the same discipline
// ParseRule already follows (TestParseRule_IgnoresUnknownKeys).
func TestAnchorObservationsCodec_IgnoresAnUnknownKey(t *testing.T) {
	raw := []byte(`[{"label":"total","text":"Total","page":1,"band":0,"x0":0,"y0":0,"x1":0.1,"y1":0.1,"confidence":0.9}]`)

	got, err := extraction.UnmarshalAnchorObservations(raw)
	if err != nil {
		t.Fatalf("UnmarshalAnchorObservations(unknown key) error = %v, want nil", err)
	}
	want := []extraction.AnchorObservation{{Label: "total", Text: "Total", Page: 1, Band: 0, X0: 0, Y0: 0, X1: 0.1, Y1: 0.1}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("UnmarshalAnchorObservations(unknown key) = %+v, want %+v -- the known fields must still decode, not merely error-free", got, want)
	}
}

// O-09: AnchorLabelText returns the lexicon pattern's own matched substring, measured against
// the real reader on two different corpus layouts.
func TestAnchorLabelText_MatchesTheMeasuredCorpusOracles(t *testing.T) {
	for _, c := range []struct {
		file  string
		label string
		want  string
	}{
		{"corpus_two_column.pdf", "buyer_name", "Buyer"},
		{"corpus_split_labels.pdf", "invoice_no", "Invoice No"},
	} {
		t.Run(c.file+"/"+c.label, func(t *testing.T) {
			pages := rvCorpusPages(t, c.file)
			obs := extraction.AnchorObservations(pages)

			var found extraction.AnchorObservation
			var srcTok extraction.Token
			haveFound := false
			for _, page := range pages {
				if page.Number != 1 {
					continue
				}
				for _, o := range obs {
					if o.Label != c.label {
						continue
					}
					for _, candidate := range page.Tokens {
						if candidate.Region.X0 == o.X0 && candidate.Region.Y0 == o.Y0 &&
							candidate.Region.X1 == o.X1 && candidate.Region.Y1 == o.Y1 {
							found, srcTok, haveFound = o, candidate, true
						}
					}
				}
			}
			if !haveFound {
				t.Fatalf("%s: no observation/token pair found for label %q", c.file, c.label)
			}

			if got := extraction.AnchorLabelText(found, srcTok); got != c.want {
				t.Errorf("AnchorLabelText(%s observation, its own token) = %q, want %q", c.label, got, c.want)
			}
		})
	}
}

// learn_internal_test.go: C-agree. Package extraction, not extraction_test: this spec drives
// the request path's own unexported normalisedBox directly against the layout_anchors codec's
// predicate, so the two cannot silently drift onto different rules.
package extraction

import (
	"fmt"
	"testing"
)

// C-agree: normalisedBox (handlers_correction.go) and UnmarshalAnchorObservations' box
// predicate must accept and refuse the same boxes. A box the wire refuses that a worker could
// still store -- or the reverse -- is exactly the drift this spec exists to catch.
func TestNormalisedBoxAgreesWithTheAnchorCodecPredicate(t *testing.T) {
	table := []struct {
		name           string
		page           int
		x0, y0, x1, y1 float64
		wantAccept     bool
	}{
		{"all boundaries valid", 1, 0, 0, 1, 1, true},
		{"zero-area box (x0 == x1)", 1, 0.5, 0.2, 0.5, 0.3, true},
		{"zero-area box (y0 == y1)", 1, 0.2, 0.5, 0.3, 0.5, true},
		{"x0 below 0", 1, -0.0001, 0.2, 0.3, 0.4, false},
		{"y0 below 0", 1, 0.1, -0.0001, 0.3, 0.4, false},
		{"x1 above 1", 1, 0.1, 0.2, 1.0001, 0.4, false},
		{"y1 above 1", 1, 0.1, 0.2, 0.3, 1.0001, false},
		{"x0 > x1", 1, 0.6, 0.2, 0.5, 0.4, false},
		{"y0 > y1", 1, 0.2, 0.6, 0.4, 0.5, false},
		{"page 0", 0, 0.1, 0.1, 0.2, 0.2, false},
		{"page 1 boundary", 1, 0.1, 0.1, 0.2, 0.2, true},
	}

	for _, c := range table {
		t.Run(c.name, func(t *testing.T) {
			wireAccept := normalisedBox(ExtractionRegion{Page: c.page, X0: c.x0, Y0: c.y0, X1: c.x1, Y1: c.y1})
			if wireAccept != c.wantAccept {
				t.Fatalf("normalisedBox(%+v) = %v, want %v -- fixture disagrees with its own label; the codec comparison below would be meaningless", c, wireAccept, c.wantAccept)
			}

			raw := []byte(fmt.Sprintf(`[{"label":"total","text":"Total","page":%d,"band":0,"x0":%v,"y0":%v,"x1":%v,"y1":%v}]`,
				c.page, c.x0, c.y0, c.x1, c.y1))
			_, err := UnmarshalAnchorObservations(raw)
			codecAccept := err == nil

			if codecAccept != wireAccept {
				t.Errorf("%s: normalisedBox accepts = %v, codec accepts = %v (err=%v), want agreement", c.name, wireAccept, codecAccept, err)
			}
		})
	}
}

// lineitems_parse_qa_test.go: QA (Mode B) coverage for ParseLineFieldName. The RED spec pins
// the round trip and five refusals; expandLineCorrection uses ok=false to decide which readings
// are cells, so a parse that says yes too often silently deletes header fields. External
// package: both halves of the name are exported.
//
// Helpers use an lpq* prefix.
package extraction_test

import (
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/SimonOsipov/invoice-os/internal/extraction"
)

// Every role at several indices, so a parse that hard-codes one role or one index fails here.
func TestParseLineFieldName_AcceptsEveryRoleAtEveryIndex(t *testing.T) {
	if len(extraction.LineRoles) != 4 {
		t.Fatalf("LineRoles has %d entries, want 4 -- the table below would not cover the set", len(extraction.LineRoles))
	}
	checked := 0
	for _, index := range []int{1, 2, 9, 10, 99, 999} {
		for _, role := range extraction.LineRoles {
			name := extraction.LineFieldName(index, role)
			gotIndex, gotRole, ok := extraction.ParseLineFieldName(name)
			if !ok {
				t.Errorf("ParseLineFieldName(%q) ok = false, want true", name)
				continue
			}
			if gotIndex != index || gotRole != role {
				t.Errorf("ParseLineFieldName(%q) = (%d, %q), want (%d, %q)", name, gotIndex, gotRole, index, role)
			}
			checked++
		}
	}
	if checked != 24 {
		t.Errorf("the table accepted %d name(s), want 24", checked)
	}
}

// The refusals, each with the reason it exists. The accept table above is this table's control:
// ok is not unconditionally false.
func TestParseLineFieldName_RefusesWhatLineFieldNameCannotProduce(t *testing.T) {
	for _, tc := range []struct{ name, why string }{
		{"", "the empty name"},
		{"total", "a header field"},
		{"line_items", "the block row -- the one name the correction ACTUALLY carries"},
		{"line_items[", "the prefix alone"},
		{"line_items[]", "no index and no role"},
		{"line_items[].description", "no index"},
		{"line_items[1]", "no role"},
		{"line_items[1].", "an empty role"},
		{"line_items[0].description", "index zero -- the names are 1-based"},
		{"line_items[-1].description", "a negative index"},
		{"line_items[01].description", "a leading zero Itoa never emits"},
		{"line_items[+1].description", "a sign Atoi admits and Itoa never emits"},
		{"line_items[ 1].description", "a space Atoi refuses"},
		{"line_items[1.5].description", "a fractional index"},
		{"line_items[abc].description", "a non-numeric index"},
		{"line_items[1]].description", "a doubled bracket"},
		{"line_items[1].descriptions", "a role that is not one of the four"},
		{"line_items[1].Description", "the right role in the wrong case"},
		{"line_items[1].total", "a header field's name in the role slot"},
		{"line_items[1].description.extra", "a suffix past the role"},
		{"line_items[1].description ", "a trailing space"},
		{" line_items[1].description", "a leading space -- the prefix must start the name"},
		{"xline_items[1].description", "the prefix not at the start"},
		{"LINE_ITEMS[1].description", "the prefix in the wrong case"},
	} {
		if index, role, ok := extraction.ParseLineFieldName(tc.name); ok {
			t.Errorf("ParseLineFieldName(%q) = (%d, %q, true), want false -- %s", tc.name, index, role, tc.why)
		}
	}
}

// An index Atoi cannot hold is refused rather than silently truncated: expandLineCorrection
// would otherwise drop a reading it could not re-emit.
func TestParseLineFieldName_RefusesAnIndexThatOverflows(t *testing.T) {
	huge := "line_items[" + strings.Repeat("9", 40) + "].description"
	if index, _, ok := extraction.ParseLineFieldName(huge); ok {
		t.Errorf("ParseLineFieldName(%q) ok = true (index %d), want false", huge, index)
	}
	// Control: the same shape at a representable index parses, so the refusal is the size.
	fits := "line_items[" + strconv.Itoa(1<<20) + "].description"
	if _, _, ok := extraction.ParseLineFieldName(fits); !ok {
		t.Errorf("ParseLineFieldName(%q) ok = false, want true", fits)
	}
}

// ParseLineFieldName's doc comment claims it mirrors the SPA's own regex. The SPA parses the
// same names off the same wire, so a change on either side that the other does not follow shows
// up as a grid row the server settled and the browser ignores. Pinned by reading the source.
func TestParseLineFieldName_MirrorsTheSPARegex(t *testing.T) {
	const path = "../../frontend/app/src/lib/lineItems.ts"
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	if len(b) == 0 {
		t.Fatalf("%s is empty; the scan would report a match either way", path)
	}
	src := string(b)

	// The index half: 1-based, no leading zero, no sign.
	const want = `/^line_items\[([1-9][0-9]*)\]\.(description|quantity|unit_price|line_total)$/`
	if !strings.Contains(src, want) {
		t.Errorf("%s no longer spells the line-field regex as\n  %s\n-- ParseLineFieldName "+
			"(internal/extraction/lineitems.go) claims to mirror it", path, want)
	}
	// Floor: every role Go knows is named in that file, so a role added on one side only fails.
	for _, role := range extraction.LineRoles {
		if !strings.Contains(src, role) {
			t.Errorf("%s does not mention the role %q that LineRoles carries", path, role)
		}
	}
}

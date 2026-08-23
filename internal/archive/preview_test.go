// preview_test.go: RED specs for AUDIT-05-09 (Mode A) -- the parts of the preview
// endpoint that need no database: the Preview JSON contract (D-49), the scope-constant
// source guard (D-47) and the non-positive-cap guard (D-51). preview.go does not exist
// yet, so this file (and therefore the whole package archive test binary) does not
// compile until Stage 3 adds Preview/previewOpts/preview -- the expected RED state,
// matching handlers_test.go/assemble_test.go's own Mode A header comments.
// package archive (white-box).
package archive

import (
	"context"
	"encoding/json"
	"sort"
	"strings"
	"testing"
)

// keysOf returns m's keys, sorted, for a readable failure message.
func keysOf(m map[string]json.RawMessage) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// assertExactJSONKeys unmarshals raw as an object and requires exactly want's keys,
// no more, no fewer -- catches a slice or map field added later (D-49).
func assertExactJSONKeys(t *testing.T, raw json.RawMessage, want []string) {
	t.Helper()
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("json.Unmarshal(%s): %v", raw, err)
	}
	if len(m) != len(want) {
		t.Errorf("keys = %v, want exactly %v", keysOf(m), want)
		return
	}
	for _, k := range want {
		if _, ok := m[k]; !ok {
			t.Errorf("missing key %q, got keys %v", k, keysOf(m))
		}
	}
}

// --- AC-6: nil TIN marshals as null, inherited from manifestEntity (D-49) ------------

func TestPreview_NullTinMarshalsAsNullNotEmptyString(t *testing.T) {
	p := Preview{Entity: manifestEntity{ID: validEntityID, Name: "No TIN Co", TIN: nil}}
	raw, err := json.Marshal(p)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	if !strings.Contains(string(raw), `"tin":null`) {
		t.Errorf("marshaled Preview = %s, want it to contain \"tin\":null", raw)
	}
	if strings.Contains(string(raw), `"tin":""`) {
		t.Errorf("marshaled Preview = %s, must not contain an empty-string tin", raw)
	}

	// Non-nil control: a real TIN must round-trip under the same field, not vanish.
	tin := "98765432-0001"
	raw2, err := json.Marshal(Preview{Entity: manifestEntity{ID: validEntityID, Name: "Has TIN Co", TIN: &tin}})
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	if !strings.Contains(string(raw2), `"tin":"98765432-0001"`) {
		t.Errorf("marshaled Preview = %s, want the real tin to round-trip", raw2)
	}
}

// TestPreview_JSONShapeIsExactlyTheContract pins the exact key set at all three levels
// (D-49) -- the mechanism that would catch a slice added to the response later.
func TestPreview_JSONShapeIsExactlyTheContract(t *testing.T) {
	tin := "98765432-0001"
	p := Preview{
		Entity:    manifestEntity{ID: validEntityID, Name: "Honeywell Group", TIN: &tin},
		Period:    manifestPeriod{From: validFrom, To: validTo, Bounds: "inclusive", Basis: "invoices.created_at"},
		Filename:  "ASComply_evidence_Honeywell-Group_20260101_20260331.zip",
		Counts:    manifestCounts{Invoices: 1, StatusTransitions: 2, Submissions: 3, ExchangeAttempts: 4, BodyFiles: 5},
		OverLimit: true,
	}
	raw, err := json.Marshal(p)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}

	var top map[string]json.RawMessage
	if err := json.Unmarshal(raw, &top); err != nil {
		t.Fatalf("json.Unmarshal top level: %v", err)
	}
	assertExactJSONKeys(t, raw, []string{"entity", "period", "filename", "counts", "over_limit"})

	entity, ok := top["entity"]
	if !ok {
		t.Fatal("no \"entity\" key -- cannot check its shape")
	}
	assertExactJSONKeys(t, entity, []string{"id", "name", "tin"})

	period, ok := top["period"]
	if !ok {
		t.Fatal("no \"period\" key -- cannot check its shape")
	}
	assertExactJSONKeys(t, period, []string{"from", "to", "bounds", "basis"})

	counts, ok := top["counts"]
	if !ok {
		t.Fatal("no \"counts\" key -- cannot check its shape")
	}
	assertExactJSONKeys(t, counts, []string{"invoices", "status_transitions", "submissions", "exchange_attempts", "body_files"})
}

// TestPreviewSQL_ScopeConstantsAppearExactlyOnceInSource (D-47): a second hand-written
// copy of a scope constant is exactly the drift AC-1 forbids. Counts the constant's
// VALUE (the SQL text a hand-written duplicate would reproduce), not its identifier --
// each constant's own name is also its consumers' identifier, so counting the name
// necessarily exceeds 1. Both 0 (the refactor hasn't landed) and 2 (a duplicate) fail.
// nonTestGoSource is assemble_test.go's helper.
func TestPreviewSQL_ScopeConstantsAppearExactlyOnceInSource(t *testing.T) {
	src := nonTestGoSource(t)
	if src == "" {
		t.Fatal("scanned zero bytes of non-test source -- every assertion below would pass vacuously")
	}
	if !strings.Contains(src, "tx.Query(") {
		t.Fatal("scanned source never mentions tx.Query( -- the scan is broken (control needle absent), so the assertions below prove nothing")
	}
	for _, tc := range []struct{ name, value string }{
		{"invoicesScope", invoicesScope},
		{"historyScope", historyScope},
		{"submissionsScope", submissionsScope},
		{"exchangeScope", exchangeScope},
	} {
		if n := strings.Count(src, tc.value); n != 1 {
			t.Errorf("%s's value appears %d times in non-test source, want exactly 1", tc.name, n)
		}
	}
}

// TestPreview_RejectsNonPositiveCap mirrors assemble's own guard (D-51): a nil tx means
// any statement before the guard would panic, so this proves the check runs first.
func TestPreview_RejectsNonPositiveCap(t *testing.T) {
	r := Request{EntityID: validEntityID, From: mustParseRFC3339(t, validFrom), To: mustParseRFC3339(t, validTo)}
	if _, err := preview(context.Background(), nil, r, previewOpts{}); err == nil {
		t.Fatal("preview(maxInvoices=0): want an error before touching a nil tx, got nil")
	}
}

package endtoend

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

type nkRow struct {
	File   string              `json:"file"`
	Kind   string              `json:"kind"`
	Fields map[string][]string `json:"fields"`
}

func nkLoad(t *testing.T) []nkRow {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(eeFxDir, "nightly_key.json"))
	if err != nil {
		t.Fatalf("read the nightly key: %v", err)
	}
	var rows []nkRow
	if err := json.Unmarshal(raw, &rows); err != nil {
		t.Fatalf("parse the nightly key: %v", err)
	}
	if len(rows) != 44 {
		t.Fatalf("the nightly key holds %d row(s), want 44", len(rows))
	}
	return rows
}

func TestNightlyKey_LayoutRowsEqualExpectByLayout(t *testing.T) {
	inKey := map[string]nkRow{}
	for _, r := range nkLoad(t) {
		if r.Kind == "layout" {
			inKey[r.File] = r
		}
	}
	if len(inKey) != len(expectByLayout) {
		t.Errorf("the key holds %d layout row(s), expectByLayout %d", len(inKey), len(expectByLayout))
	}
	for _, want := range expectByLayout {
		row, ok := inKey[want.file]
		if !ok {
			t.Errorf("%s is in expectByLayout and not in the key", want.file)
			continue
		}
		for _, field := range writtenFields {
			if !slices.Equal(row.Fields[field], want.fields[field]) {
				t.Errorf("%s / %s: key %v, expectByLayout %v", want.file, field, row.Fields[field], want.fields[field])
			}
		}
		delete(inKey, want.file)
	}
	for file := range inKey {
		t.Errorf("%s is a layout row in the key and not in expectByLayout", file)
	}
}

func TestNightlyKey_EveryRowHasTheWrittenFields(t *testing.T) {
	seen := map[string]bool{}
	for _, r := range nkLoad(t) {
		if seen[r.File] {
			t.Errorf("%s appears twice", r.File)
		}
		seen[r.File] = true
		if len(r.Fields) != len(writtenFields) {
			t.Errorf("%s holds %d field(s), want %d", r.File, len(r.Fields), len(writtenFields))
		}
		for _, field := range writtenFields {
			if _, ok := r.Fields[field]; !ok {
				t.Errorf("%s lacks field %q", r.File, field)
			}
		}
	}
}

// Every non-empty text and tin value must be reachable from the PDF's own text layer, as for
// the scored layouts. Scans carry no text layer and are out of this walk.
func TestNightlyCorpus_TextValuesAreReadable(t *testing.T) {
	checked := 0
	for _, r := range nkLoad(t) {
		if r.Kind != "text" && r.Kind != "tin" {
			continue
		}
		if len(eePageTokenReadings(t, r.File, "invoice_number")) == 0 {
			t.Fatalf("%s yields no invoice-number reading; the checks below would prove nothing", r.File)
		}
		for _, field := range writtenFields {
			readings := eePageTokenReadings(t, r.File, field)
			for _, v := range r.Fields[field] {
				checked++
				if !slices.Contains(readings, v) {
					t.Errorf("%s / %s expects %q, which no token normalises to under %s; readings were %v",
						r.File, field, v, eeShapeOf[field], readings)
				}
			}
		}
	}
	if checked == 0 {
		t.Fatal("no value was checked")
	}
}

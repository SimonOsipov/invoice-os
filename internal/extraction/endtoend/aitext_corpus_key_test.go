// aitext_corpus_key_test.go: AIR-01-02's corpus key exporter. Stdlib only (scan B fences
// internal/extraction's own deps, not this package's, but the story keeps this file to stdlib
// so it needs no new import to justify).
package endtoend

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// TestAIText_WriteCorpusKey exports the committed corpus table (expectByLayout, writtenFields)
// to AIMT_OUT/corpus_key.json, so the answer key never retypes it by hand (D-A07). The env
// gate returns with one log line when AIMT_OUT is unset, opening no file.
func TestAIText_WriteCorpusKey(t *testing.T) {
	out := os.Getenv("AIMT_OUT")
	if out == "" {
		t.Log("AIMT_OUT unset: no read, no call")
		return
	}

	rows := make(map[string]map[string][]string, len(expectByLayout))
	for _, row := range expectByLayout {
		fields := make(map[string][]string, len(writtenFields))
		for _, f := range writtenFields {
			v := row.fields[f]
			if v == nil {
				v = []string{}
			}
			fields[f] = v
		}
		rows[row.file] = fields
	}

	b, err := json.MarshalIndent(rows, "", "  ")
	if err != nil {
		t.Fatalf("marshal corpus key: %v", err)
	}
	if err := os.MkdirAll(out, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", out, err)
	}
	path := filepath.Join(out, "corpus_key.json")
	if err := os.WriteFile(path, b, 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	t.Logf("wrote corpus key: %d layout(s) x %d field(s)", len(rows), len(writtenFields))
}

package invoicefields

import (
	"bytes"
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"testing"
)

var update = flag.Bool("update", false, "rewrite frontend/app/src/lib/invoiceFields.gen.ts")

const regenerate = "go test ./internal/invoicefields -run TestInvoiceFields_TheSPAFileIsGeneratedFromTheList -update"

type tsField struct {
	Key       string  `json:"key"`
	Label     string  `json:"label"`
	Type      Type    `json:"type"`
	Line      bool    `json:"line"`
	ImportKey *string `json:"importKey"`
	Required  bool    `json:"required"`
	FormKey   *string `json:"formKey"`
	Edit      bool    `json:"edit"`
	Extract   bool    `json:"extract"`
}

func renderTS(t *testing.T) []byte {
	t.Helper()
	rows := make([]tsField, 0, len(All))
	for _, f := range All {
		row := tsField{Key: f.Key, Label: f.Label, Type: f.Type, Line: f.Line, Required: f.Required, Edit: f.Edit, Extract: f.Extract}
		if f.Import {
			k := f.ImportKey()
			row.ImportKey = &k
		}
		if f.FormKey != "" {
			k := f.FormKey
			row.FormKey = &k
		}
		rows = append(rows, row)
	}
	body, err := json.MarshalIndent(rows, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	var b bytes.Buffer
	b.WriteString("// Generated from internal/invoicefields by: " + regenerate + "\n")
	b.WriteString("export const INVOICE_FIELDS = ")
	b.Write(body)
	b.WriteString(" as const\n")
	keys, err := json.MarshalIndent(ImportKeys(), "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	b.WriteString("\n// The import keys in Map-step order.\nexport const IMPORT_KEYS = ")
	b.Write(keys)
	b.WriteString(" as const\n")
	return b.Bytes()
}

func TestInvoiceFields_TheSPAFileIsGeneratedFromTheList(t *testing.T) {
	path := filepath.Join("..", "..", "frontend", "app", "src", "lib", "invoiceFields.gen.ts")
	want := renderTS(t)
	if *update {
		if err := os.WriteFile(path, want, 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v\nregenerate: %s", path, err, regenerate)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("%s differs from the rendering of All.\nregenerate: %s", path, regenerate)
	}
}

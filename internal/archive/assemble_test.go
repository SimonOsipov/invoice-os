// assemble_test.go: RED specs for AUDIT-05-07 (Mode A) -- the parts of the assembler
// that need no database: the cap guards (D-34) and the no-CollectRows source scan
// (AC-6). package archive (white-box): NewStore/Store.maxInvoices are new, unexported
// fields this subtask introduces.
package archive

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestBundleCap_IsTenThousand pins the production cap as a LITERAL, not the constant
// itself -- a self-referencing default test proves nothing (D-34, see
// sweep_adversarial_test.go:240-270's mutation experiment).
func TestBundleCap_IsTenThousand(t *testing.T) {
	if maxBundleInvoices != 10000 {
		t.Errorf("maxBundleInvoices = %d, want 10000", maxBundleInvoices)
	}
}

// TestNewStore_UsesTheProductionCap: NewStore must wire the production cap --
// assemble treats a non-positive cap as a programming error, not a default, so a
// NewStore that forgot the wiring must not silently pass every other test (D-34).
func TestNewStore_UsesTheProductionCap(t *testing.T) {
	if got := NewStore(nil).maxInvoices; got != 10000 {
		t.Errorf("NewStore(nil).maxInvoices = %d, want 10000", got)
	}
}

// nonTestGoSource concatenates every non-test .go file in this package directory, so
// assemble.go/store.go are covered automatically once they exist alongside the files
// already here.
func nonTestGoSource(t *testing.T) string {
	t.Helper()
	matches, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatalf("glob *.go: %v", err)
	}
	var buf strings.Builder
	for _, m := range matches {
		if strings.HasSuffix(m, "_test.go") {
			continue
		}
		b, err := os.ReadFile(m)
		if err != nil {
			t.Fatalf("read %s: %v", m, err)
		}
		buf.Write(b)
	}
	return buf.String()
}

// TestAssembleSource_UsesNoCollectRows (AC-6) needs a control needle: pgx.CollectRows
// and CollectOneRow appear nowhere in this package today, so an empty or broken scan
// would pass silently. tx.Query( is a string every file in this package uses today.
func TestAssembleSource_UsesNoCollectRows(t *testing.T) {
	src := nonTestGoSource(t)
	if src == "" {
		t.Fatal("scanned zero bytes of non-test source -- every assertion below would pass vacuously")
	}
	if !strings.Contains(src, "tx.Query(") {
		t.Fatal("scanned source never mentions tx.Query( -- the scan is broken (control needle absent), so the assertions below prove nothing")
	}
	for _, needle := range []string{"pgx.CollectRows", "CollectOneRow"} {
		if strings.Contains(src, needle) {
			t.Errorf("non-test source contains %q -- AC-6 forbids it (bodies must stream, never be collected into memory)", needle)
		}
	}
}

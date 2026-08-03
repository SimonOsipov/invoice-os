package importer

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// AC-6. The filename coercion MOVED to internal/document; two copies of a
// security control drift apart. The banned identifier is assembled at runtime
// so this file cannot match its own scan (cmd/gateway/main_test.go idiom), and
// a control needle that must be found keeps a broken walk from passing
// vacuously.
func TestImporterHasNoLocalSanitizeFilename(t *testing.T) {
	// Matched case-INSENSITIVELY: the control left this package as the EXPORTED
	// document.SanitizeFilename, so a copy back could land under either spelling
	// and an exact lowercase needle would miss the exported one.
	banned := "func " + strings.ToLower("S") + "anitizeFilename("
	const control = "func NewStore("

	bannedIn, controlIn := scanPackageSources(t, banned, control)

	if len(controlIn) == 0 {
		t.Fatalf("the scan found %q in no file in internal/importer -- the walk is broken, so the banned-declaration "+
			"check below passed vacuously", control)
	}
	if len(bannedIn) != 0 {
		t.Errorf("%q (in any casing) is still declared in %v -- it moved to internal/document as the exported "+
			"SanitizeFilename and must not have a second copy here", banned, bannedIn)
	}
}

// scanPackageSources reports which .go files in this package contain each
// needle. banned is matched case-insensitively so it catches both the exported
// and the unexported spelling; control is matched exactly.
func scanPackageSources(t *testing.T, banned, control string) (bannedIn, controlIn []string) {
	t.Helper()

	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("read internal/importer: %v", err)
	}
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".go" {
			continue
		}
		b, err := os.ReadFile(e.Name())
		if err != nil {
			t.Fatalf("read %s: %v", e.Name(), err)
		}
		src := string(b)
		if strings.Contains(strings.ToLower(src), strings.ToLower(banned)) {
			bannedIn = append(bannedIn, e.Name())
		}
		if strings.Contains(src, control) {
			controlIn = append(controlIn, e.Name())
		}
	}
	return bannedIn, controlIn
}

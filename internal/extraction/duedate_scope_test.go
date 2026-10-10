// duedate_scope_test.go: EXTR-25-04 AC-4.8 -- due_date must reach no non-test source
// under ddScanRoots outside anchor.go.
package extraction_test

import (
	"go/scanner"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
)

const (
	ddNeedle        = "due_date"
	ddControlNeedle = "issue_date" // known present in non-test source; proves the scan finds hits at all
)

// ddNeedleRe matches the needle in any letter case and with or without the underscore.
var ddNeedleRe = regexp.MustCompile(`(?i)due_?date`)

var (
	ddBlockComment = regexp.MustCompile(`(?s)/\*.*?\*/`)
	ddSlashComment = regexp.MustCompile(`//[^\n]*`)
	ddDashComment  = regexp.MustCompile(`--[^\n]*`)
)

// ddCode returns the source with comments removed, so a comment that names the needle is not a hit.
func ddCode(path string, src []byte) string {
	ext := filepath.Ext(path)
	if ext == ".go" {
		fset := token.NewFileSet()
		var sc scanner.Scanner
		sc.Init(fset.AddFile(path, fset.Base(), len(src)), src, nil, 0)
		var b strings.Builder
		for {
			_, tok, lit := sc.Scan()
			if tok == token.EOF {
				return b.String()
			}
			b.WriteString(lit)
			b.WriteByte('\n')
		}
	}
	text := ddBlockComment.ReplaceAllString(string(src), "")
	if ext == ".sql" {
		return ddDashComment.ReplaceAllString(text, "")
	}
	return ddSlashComment.ReplaceAllString(text, "")
}

var ddScanExts = []string{".go", ".sql", ".ts", ".tsx"}

// ddScanRoots is where extraction routes a due date; the invoice has its own due_date column
// elsewhere. ddScanFloor is the eligible-file count measured there.
var ddScanRoots = []string{"internal/extraction", "internal/importer", "cmd/submission"}

const ddScanFloor = 51

func ddScanEligible(path string) bool {
	ext := filepath.Ext(path)
	if !slices.Contains(ddScanExts, ext) {
		return false
	}
	if ext == ".go" && strings.HasSuffix(path, "_test.go") {
		return false
	}
	return true
}

// AC-4.8, narrowed to ddScanRoots. An *ast.Ident walk (reachability_test.go's own mechanism)
// cannot see "due_date" as a STRING LITERAL, which is exactly this row's own mutation shape,
// so this reads raw bytes instead. Skips node_modules and docling goldens, which are data, not source.
func TestExtraction_NoDueDateFieldExists(t *testing.T) {
	root := rxRepoRoot(t)

	var scanned int
	var hits []string
	var sawControl bool

	visit := func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch filepath.Base(path) {
			case ".git", ".claude", "node_modules":
				return filepath.SkipDir
			}
			return nil
		}
		if strings.HasSuffix(path, ".docling.json") || !ddScanEligible(path) {
			return nil
		}

		b, rerr := os.ReadFile(path)
		if rerr != nil {
			return rerr
		}
		scanned++
		text := ddCode(path, b)
		if strings.Contains(text, ddControlNeedle) {
			sawControl = true
		}
		if ddNeedleRe.MatchString(text) {
			rel, _ := filepath.Rel(root, path)
			hits = append(hits, filepath.ToSlash(rel))
		}
		return nil
	}
	for _, r := range ddScanRoots {
		if err := filepath.WalkDir(filepath.Join(root, r), visit); err != nil {
			t.Fatalf("walk %s: %v", r, err)
		}
	}

	if scanned < ddScanFloor {
		t.Fatalf("scanned %d file(s), want at least %d; the walk is reading the wrong tree", scanned, ddScanFloor)
	}
	if !sawControl {
		t.Fatalf("the control needle %q was found in no scanned file; a scan that finds nothing is indistinguishable from a broken walk", ddControlNeedle)
	}

	slices.Sort(hits)
	want := []string{"internal/extraction/anchor.go"}
	if !slices.Equal(hits, want) {
		t.Errorf("%q appears in %v, want exactly %v -- nothing outside the lexicon entry may carry a due date", ddNeedle, hits, want)
	}
}

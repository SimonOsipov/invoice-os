package tenancy

import (
	"go/scanner"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// provisionCaller is the one non-test file allowed to name the DEFINER function (the application-side guard).
const provisionCaller = "internal/tenancy/store.go"

// TestProvisionWorkspaceCalledOnlyByTheStore scans every non-test .go file's
// tokens (comments dropped) for provision_workspace.
func TestProvisionWorkspaceCalledOnlyByTheStore(t *testing.T) {
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	var scanned int
	sites := map[string]int{} // repo-relative file -> tokens naming the function
	err = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			name := d.Name()
			if path != root && (strings.HasPrefix(name, ".") || name == "node_modules" || name == "testdata") {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		src, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		scanned++
		rel, _ := filepath.Rel(root, path)
		rel = filepath.ToSlash(rel)
		fset := token.NewFileSet()
		var s scanner.Scanner
		s.Init(fset.AddFile(rel, -1, len(src)), src, func(pos token.Position, msg string) {
			t.Errorf("scan %s: %s", pos, msg)
		}, 0) // mode 0: comments are skipped
		for {
			_, tok, lit := s.Scan()
			if tok == token.EOF {
				break
			}
			if strings.Contains(lit, "provision_workspace") {
				sites[rel]++
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", root, err)
	}

	// Two subtests so the subset half can be mutation-proven while the needle waits for store.go.
	t.Run("only the store names it", func(t *testing.T) {
		if scanned < 200 {
			t.Fatalf("scanned %d non-test .go files, want >= 200 (222 at AUTH-03-03)", scanned)
		}
		for file, n := range sites {
			if file != provisionCaller {
				t.Errorf("%s names provision_workspace %d time(s); only %s may", file, n, provisionCaller)
			}
		}
	})
	t.Run("control needle", func(t *testing.T) {
		if sites[provisionCaller] == 0 {
			t.Errorf("no provision_workspace token in %s", provisionCaller)
		}
	})
}

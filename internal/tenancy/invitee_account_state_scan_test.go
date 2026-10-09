package tenancy

import (
	"go/scanner"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// stateCaller is the one non-test file allowed to name the account-state function.
const stateCaller = "internal/tenancy/store.go"

// SQL is case-blind and ignores a space before the parenthesis; the scan matches both.
var stateCall = regexp.MustCompile(`(?i)invitee_account_state\s*\(`)

// TestInviteeAccountStateCalledOnlyByTheStore scans every non-test .go file's
// string tokens (comments dropped) for `invitee_account_state(`; the parenthesis keeps the policy name out.
func TestInviteeAccountStateCalledOnlyByTheStore(t *testing.T) {
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	var scanned int
	sites := map[string]int{} // repo-relative file -> calls named
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
			if n := len(stateCall.FindAllString(lit, -1)); n > 0 {
				sites[rel] += n
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", root, err)
	}

	t.Run("only the store names it", func(t *testing.T) {
		if scanned < 200 {
			t.Fatalf("scanned %d non-test .go files, want >= 200", scanned)
		}
		for file, n := range sites {
			if file != stateCaller {
				t.Errorf("%s names invitee_account_state( %d time(s); only %s may", file, n, stateCaller)
			}
		}
	})
	t.Run("control needle", func(t *testing.T) {
		if sites[stateCaller] != 1 {
			t.Errorf("%d invitee_account_state( calls in %s, want exactly 1", sites[stateCaller], stateCaller)
		}
	})
}

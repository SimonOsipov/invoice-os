package tenancy

import (
	"go/ast"
	"go/parser"
	"go/scanner"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
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
	t.Run("only PreviewInvitation and ListInvitations call accountState", func(t *testing.T) {
		files, err := filepath.Glob("*.go")
		if err != nil {
			t.Fatal(err)
		}
		var parsed int
		var sawStore bool
		calls := map[string]int{} // enclosing func -> calls of accountState
		for _, f := range files {
			if strings.HasSuffix(f, "_test.go") {
				continue
			}
			parsed++
			sawStore = sawStore || f == "store.go"
			file, err := parser.ParseFile(token.NewFileSet(), f, nil, 0) // mode 0: comments dropped
			if err != nil {
				t.Fatalf("parse %s: %v", f, err)
			}
			for _, decl := range file.Decls {
				fd, ok := decl.(*ast.FuncDecl)
				if !ok {
					continue
				}
				ast.Inspect(fd, func(n ast.Node) bool {
					if c, ok := n.(*ast.CallExpr); ok {
						if id, ok := c.Fun.(*ast.Ident); ok && id.Name == "accountState" {
							calls[f+":"+fd.Name.Name]++
						}
					}
					return true
				})
			}
		}
		if parsed == 0 || !sawStore {
			t.Fatalf("parsed %d non-test files, store.go seen %v; the glob misses the package", parsed, sawStore)
		}
		var total int
		var sites []string
		for site, n := range calls {
			total += n
			sites = append(sites, site)
			if fn := site[strings.LastIndex(site, ":")+1:]; fn != "PreviewInvitation" && fn != "ListInvitations" {
				t.Errorf("%s calls accountState %d time(s); only PreviewInvitation and ListInvitations may", site, n)
			}
		}
		sort.Strings(sites)
		if total != 2 || len(sites) != 2 {
			t.Errorf("accountState is called %d time(s) from %v, want exactly 2 (PreviewInvitation, ListInvitations)", total, sites)
		}
	})
}

package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
)

const platformImportPath = "github.com/SimonOsipov/invoice-os/internal/platform"

// platformAssignments returns the value of every assignment to platform.<name> in
// one file: a string literal unquoted, anything else as "<non-literal>". It parses
// without comments, so a commented-out assignment is not one.
func platformAssignments(filename string, src any, name string) ([]string, error) {
	f, err := parser.ParseFile(token.NewFileSet(), filename, src, 0)
	if err != nil {
		return nil, err
	}
	local := ""
	for _, im := range f.Imports {
		if p, _ := strconv.Unquote(im.Path.Value); p == platformImportPath {
			local = "platform"
			if im.Name != nil {
				local = im.Name.Name
			}
		}
	}
	if local == "" {
		return nil, nil
	}
	var vals []string
	ast.Inspect(f, func(n ast.Node) bool {
		as, ok := n.(*ast.AssignStmt)
		if !ok {
			return true
		}
		for i, lhs := range as.Lhs {
			sel, ok := lhs.(*ast.SelectorExpr)
			if !ok || sel.Sel.Name != name {
				continue
			}
			if id, ok := sel.X.(*ast.Ident); !ok || id.Name != local {
				continue
			}
			val := "<non-literal>"
			if len(as.Rhs) == len(as.Lhs) {
				if lit, ok := as.Rhs[i].(*ast.BasicLit); ok && lit.Kind == token.STRING {
					if s, err := strconv.Unquote(lit.Value); err == nil {
						val = s
					}
				}
			}
			vals = append(vals, val)
		}
		return true
	})
	return vals, nil
}

func TestMockIssuerStateIsPublishedByTheGatewayOnly(t *testing.T) {
	const fixture = `package main

import p "github.com/SimonOsipov/invoice-os/internal/platform"

func main() {
	// platform.MockIssuer = "off"
	p.MockIssuer = "on"
}
`
	got, err := platformAssignments("fixture.go", fixture, "MockIssuer")
	if err != nil {
		t.Fatalf("parse fixture: %v", err)
	}
	if !slices.Equal(got, []string{"on"}) {
		t.Fatalf("fixture assignments = %v, want [on]: the scanner cannot find an aliased assignment, or reads a comment as code", got)
	}

	all, err := filepath.Glob(filepath.Join("..", "*", "*.go"))
	if err != nil {
		t.Fatal(err)
	}
	var files []string
	for _, f := range all {
		if !strings.HasSuffix(f, "_test.go") {
			files = append(files, filepath.ToSlash(f))
		}
	}
	const gatewayMain = "../gateway/main.go"
	if len(files) < 9 || !slices.Contains(files, gatewayMain) {
		t.Fatalf("scanned %d cmd source file(s) %v, want every service's main.go including %s", len(files), files, gatewayMain)
	}

	// Control needle on a real file: the gateway already publishes DBReset.
	if reset, err := platformAssignments(gatewayMain, nil, "DBReset"); err != nil || len(reset) == 0 {
		t.Fatalf("no platform.DBReset assignment found in %s (err %v); the scan cannot see real assignments", gatewayMain, err)
	}

	byFile := map[string][]string{}
	for _, f := range files {
		vals, err := platformAssignments(f, nil, "MockIssuer")
		if err != nil {
			t.Fatalf("parse %s: %v", f, err)
		}
		if len(vals) > 0 {
			byFile[f] = vals
		}
	}
	for f, vals := range byFile {
		if f != gatewayMain {
			t.Errorf("%s assigns platform.MockIssuer %v; only cmd/gateway/main.go may", f, vals)
		}
	}
	for _, want := range []string{"absent", "off", "on"} {
		if !slices.Contains(byFile[gatewayMain], want) {
			t.Errorf("cmd/gateway/main.go never assigns platform.MockIssuer = %q (it assigns %v)", want, byFile[gatewayMain])
		}
	}
}

// yamlCode drops YAML and shell comments, so a commented-out step cannot satisfy a scan.
func yamlCode(src string) []string {
	lines := strings.Split(src, "\n")
	for i, line := range lines {
		var inS, inD bool
		for j := 0; j < len(line); j++ {
			c := line[j]
			if c == '\'' && !inD {
				inS = !inS
			} else if c == '"' && !inS {
				inD = !inD
			} else if c == '#' && !inS && !inD && (j == 0 || line[j-1] == ' ' || line[j-1] == '\t') {
				lines[i] = strings.TrimRight(line[:j], " \t")
				break
			}
		}
	}
	return lines
}

// jobBlock returns the lines of jobs.<name>, or nil.
func jobBlock(lines []string, name string) []string {
	var inJobs, in bool
	var block []string
	for _, line := range lines {
		if strings.HasPrefix(line, "jobs:") {
			inJobs = true
			continue
		}
		if !inJobs {
			continue
		}
		if strings.TrimSpace(line) != "" && !strings.HasPrefix(line, " ") {
			break
		}
		if strings.HasPrefix(line, "  ") && !strings.HasPrefix(line, "   ") && strings.TrimSpace(line) != "" {
			in = strings.TrimSpace(line) == name+":"
			continue
		}
		if in {
			block = append(block, line)
		}
	}
	return block
}

// runText returns every run: value in block, block scalars included, one command line per line.
func runText(block []string) string {
	var b strings.Builder
	scalarIndent := -1
	for _, line := range block {
		trimmed := strings.TrimSpace(line)
		indent := len(line) - len(strings.TrimLeft(line, " "))
		if scalarIndent >= 0 {
			if trimmed == "" || indent > scalarIndent {
				b.WriteString(trimmed + "\n")
				continue
			}
			scalarIndent = -1
		}
		key := strings.TrimPrefix(trimmed, "- ")
		if !strings.HasPrefix(key, "run:") {
			continue
		}
		val := strings.TrimSpace(strings.TrimPrefix(key, "run:"))
		if strings.HasPrefix(val, "|") || strings.HasPrefix(val, ">") {
			scalarIndent = indent
			if strings.HasPrefix(trimmed, "- ") {
				scalarIndent += 2
			}
			continue
		}
		b.WriteString(val + "\n")
	}
	return b.String()
}

var (
	taggedGatewayVet  = regexp.MustCompile(`(?m)\bgo vet -tags[ =]mockissuer \./cmd/gateway/?(\s|$)`)
	taggedGatewayTest = regexp.MustCompile(`(?m)\bgo test -tags[ =]mockissuer \./cmd/gateway/?(\s|$)`)
)

// goJobTaggedGatewayProblems reports each tagged command the ci.yml `go` job does not run.
func goJobTaggedGatewayProblems(ciYAML string) []string {
	block := jobBlock(yamlCode(ciYAML), "go")
	if len(block) == 0 {
		return []string{"no `go` job"}
	}
	run := runText(block)
	var problems []string
	if !taggedGatewayVet.MatchString(run) {
		problems = append(problems, "no `go vet -tags mockissuer ./cmd/gateway/` in the go job")
	}
	if !taggedGatewayTest.MatchString(run) {
		problems = append(problems, "no `go test -tags mockissuer ./cmd/gateway/` in the go job")
	}
	return problems
}

func TestGoJobVetsAndTestsTheMockIssuerBuild(t *testing.T) {
	const head = "on: push\njobs:\n  go:\n    runs-on: ubuntu-latest\n    steps:\n      - name: Test\n        run: go test ./...\n"
	const step = "      - name: Test the mock-issuer build (-tags mockissuer)\n        run: go vet -tags mockissuer ./cmd/gateway/ && go test -tags mockissuer ./cmd/gateway/\n"
	const blockStep = "      - name: Test the mock-issuer build (-tags mockissuer)\n        run: |\n          go vet -tags mockissuer ./cmd/gateway/\n          go test -tags mockissuer ./cmd/gateway/\n"
	const tail = "  docker-canary:\n    runs-on: ubuntu-latest\n    steps:\n      - run: echo canary\n"
	commented := "      # - name: Test the mock-issuer build (-tags mockissuer)\n      #   run: go vet -tags mockissuer ./cmd/gateway/ && go test -tags mockissuer ./cmd/gateway/\n"
	otherJob := tail + "      - run: go vet -tags mockissuer ./cmd/gateway/ && go test -tags mockissuer ./cmd/gateway/\n"

	for _, c := range []struct {
		name string
		yaml string
		want int
	}{
		{"with the step", head + step + tail, 0},
		{"with the step as a block scalar", head + blockStep + tail, 0},
		{"without the step", head + tail, 2},
		{"step commented out", head + commented + tail, 2},
		{"step in another job", head + otherJob, 2},
	} {
		if got := goJobTaggedGatewayProblems(c.yaml); len(got) != c.want {
			t.Errorf("fixture %q: %d problem(s) %v, want %d", c.name, len(got), got, c.want)
		}
	}

	raw, err := os.ReadFile(filepath.Join("..", "..", ".github", "workflows", "ci.yml"))
	if err != nil {
		t.Fatal(err)
	}
	// Control: the real go job's run text carries its existing untagged steps.
	run := runText(jobBlock(yamlCode(string(raw)), "go"))
	if !strings.Contains(run, "go vet ./...") || !strings.Contains(run, "go test ./...") {
		t.Fatalf("the ci.yml go job's run text lacks `go vet ./...` / `go test ./...`; the block scan is broken:\n%s", run)
	}
	for _, p := range goJobTaggedGatewayProblems(string(raw)) {
		t.Errorf(".github/workflows/ci.yml: %s", p)
	}
}

func TestNoCommentCitesTheRetiredMilestone(t *testing.T) {
	retired := "M8" + "-07"
	if !strings.Contains("// "+"M8-"+"07 still owes the verifier", retired) {
		t.Fatalf("needle %q does not match a planted citation", retired)
	}

	root := filepath.Join("..", "..")
	for _, c := range []struct{ path, anchor string }{
		{"internal/gateway/gateway.go", "func MockIssuerEnabled("},
		{"internal/gateway/gateway_test.go", "func TestMockIssuerEnabled("},
		{"internal/platform/auth/mockissuer.go", "type MockIssuer struct"},
		{"internal/submission/registry.go", "func IsProduction("},
	} {
		b, err := os.ReadFile(filepath.Join(root, c.path))
		if err != nil {
			t.Fatal(err)
		}
		src := string(b)
		if !strings.Contains(src, c.anchor) {
			t.Fatalf("%s lacks %q; this read is not the file that carried the citation", c.path, c.anchor)
		}
		if n := strings.Count(src, retired); n > 0 {
			t.Errorf("%s still cites the retired milestone %s (%d time(s))", c.path, retired, n)
		}
	}

	for _, c := range []struct{ path, decl string }{
		{"internal/gateway/gateway.go", "MockIssuerEnabled"},
		{"internal/platform/auth/mockissuer.go", "MockIssuer"},
	} {
		doc := declDoc(t, filepath.Join(root, c.path), c.decl)
		if doc == "" {
			t.Fatalf("%s: %s has no doc comment", c.path, c.decl)
		}
		if !strings.Contains(doc, "AUTH-01") {
			t.Errorf("%s: the %s doc comment does not name AUTH-01:\n%s", c.path, c.decl, doc)
		}
	}
}

// declDoc returns the doc comment of the top-level func or type called name.
func declDoc(t *testing.T, path, name string) string {
	t.Helper()
	f, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.ParseComments)
	if err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	for _, d := range f.Decls {
		switch d := d.(type) {
		case *ast.FuncDecl:
			if d.Recv == nil && d.Name.Name == name {
				return d.Doc.Text()
			}
		case *ast.GenDecl:
			for _, s := range d.Specs {
				if ts, ok := s.(*ast.TypeSpec); ok && ts.Name.Name == name {
					if ts.Doc != nil {
						return ts.Doc.Text()
					}
					return d.Doc.Text()
				}
			}
		}
	}
	t.Fatalf("%s declares no top-level %s", path, name)
	return ""
}

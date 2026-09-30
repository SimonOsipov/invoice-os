package platform_test

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/printer"
	"go/token"
	"go/types"
	"io/fs"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// The boot exits in cmd/ end the process, so no runtime test reaches them; these scans
// read the parsed source (comments are not in the AST, build tags are ignored).

const platformPath = "github.com/SimonOsipov/invoice-os/internal/platform"

var nineMains = []string{
	"dashboard", "gateway", "invoice", "notifications", "portfolio",
	"reconciliation", "submission", "tenancy", "validation",
}

// Boot exit sites per file; a file may gain sites but not lose one.
var minFatalSites = map[string]int{
	"cmd/dashboard/main.go":      4,
	"cmd/gateway/main.go":        9,
	"cmd/gateway/mockissuer.go":  1,
	"cmd/invoice/main.go":        9,
	"cmd/notifications/main.go":  2,
	"cmd/portfolio/main.go":      4,
	"cmd/reconciliation/main.go": 8,
	"cmd/submission/main.go":     14,
	"cmd/tenancy/main.go":        4,
	"cmd/validation/main.go":     4,
}

const totalFatalSites = 59

type scanned struct {
	rel  string
	fset *token.FileSet
	f    *ast.File
}

func parseCmdTree(t *testing.T) []scanned {
	t.Helper()
	var out []scanned
	err := filepath.WalkDir("../../cmd", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") {
			return nil
		}
		fset := token.NewFileSet()
		f, err := parser.ParseFile(fset, p, nil, 0)
		if err != nil {
			return err
		}
		out = append(out, scanned{rel: filepath.ToSlash(strings.TrimPrefix(filepath.ToSlash(p), "../../")), fset: fset, f: f})
		return nil
	})
	if err != nil {
		t.Fatalf("walk cmd: %v", err)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].rel < out[j].rel })
	return out
}

func parseSnippet(t *testing.T, src string) scanned {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "snippet.go", src, 0)
	if err != nil {
		t.Fatalf("parse snippet: %v", err)
	}
	return scanned{rel: "snippet.go", fset: fset, f: f}
}

// mains returns the cmd/<svc>/main.go files, failing when a known service is missing so a
// shrunken walk cannot pass by scanning less.
func mains(t *testing.T, files []scanned) []scanned {
	t.Helper()
	var out []scanned
	have := map[string]bool{}
	for _, s := range files {
		if strings.Count(s.rel, "/") == 2 && strings.HasSuffix(s.rel, "/main.go") {
			out = append(out, s)
			have[s.rel] = true
		}
	}
	for _, svc := range nineMains {
		if !have["cmd/"+svc+"/main.go"] {
			t.Errorf("cmd/%s/main.go was not scanned", svc)
		}
	}
	if len(out) < len(nineMains) {
		t.Fatalf("scanned %d cmd/*/main.go files, want at least %d", len(out), len(nineMains))
	}
	return out
}

func importName(f *ast.File, path string) string {
	for _, imp := range f.Imports {
		if p, err := strconv.Unquote(imp.Path.Value); err != nil || p != path {
			continue
		}
		if imp.Name != nil {
			return imp.Name.Name
		}
		return path[strings.LastIndex(path, "/")+1:]
	}
	return ""
}

func line(s scanned, n ast.Node) string {
	return s.rel + ":" + strconv.Itoa(s.fset.Position(n.Pos()).Line)
}

// exitCall is a boot exit: platform.Fatal, the log package's Fatal family, or the identifier `fatal`.
type exitCall struct {
	call   *ast.CallExpr
	name   string
	format ast.Expr // nil when the call has no format string
	values []ast.Expr
}

func exitCalls(f *ast.File) []exitCall {
	pf, lg := importName(f, platformPath), importName(f, "log")
	var out []exitCall
	ast.Inspect(f, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		a := call.Args
		switch fn := call.Fun.(type) {
		case *ast.Ident:
			if fn.Name == "fatal" && len(a) >= 2 {
				out = append(out, exitCall{call, "fatal", a[1], a[2:]})
			}
		case *ast.SelectorExpr:
			x, ok := fn.X.(*ast.Ident)
			if !ok {
				return true
			}
			switch {
			case pf != "" && x.Name == pf && fn.Sel.Name == "Fatal" && len(a) >= 2:
				out = append(out, exitCall{call, "platform.Fatal", a[1], a[2:]})
			case lg != "" && x.Name == lg && fn.Sel.Name == "Fatalf" && len(a) >= 1:
				out = append(out, exitCall{call, "log.Fatalf", a[0], a[1:]})
			case lg != "" && x.Name == lg && (fn.Sel.Name == "Fatal" || fn.Sel.Name == "Fatalln"):
				out = append(out, exitCall{call, "log." + fn.Sel.Name, nil, a})
			}
		}
		return true
	})
	return out
}

// bannedExits lists every way a cmd file can end the process other than platform.Fatal.
func bannedExits(s scanned) []string {
	lg, osn := importName(s.f, "log"), importName(s.f, "os")
	var out []string
	ast.Inspect(s.f, func(n ast.Node) bool {
		switch x := n.(type) {
		case *ast.FuncDecl:
			if x.Recv == nil && x.Name.Name == "fatal" {
				out = append(out, line(s, x)+": func fatal declared, want platform.Fatal")
			}
		case *ast.CallExpr:
			switch fn := x.Fun.(type) {
			case *ast.Ident:
				if fn.Name == "fatal" {
					out = append(out, line(s, x)+": fatal(...), want platform.Fatal")
				}
			case *ast.SelectorExpr:
				id, ok := fn.X.(*ast.Ident)
				if !ok {
					break
				}
				sel := fn.Sel.Name
				if lg != "" && id.Name == lg && (strings.HasPrefix(sel, "Fatal") || strings.HasPrefix(sel, "Panic")) {
					out = append(out, line(s, x)+": log."+sel+", want platform.Fatal")
				}
				if osn != "" && id.Name == osn && sel == "Exit" {
					out = append(out, line(s, x)+": os.Exit, want platform.Fatal")
				}
			}
		}
		return true
	})
	return out
}

func countNamed(s scanned, name string) int {
	n := 0
	for _, c := range exitCalls(s.f) {
		if c.name == name {
			n++
		}
	}
	return n
}

func TestCmdExitsOnlyThroughPlatformFatal(t *testing.T) {
	// Control: the detector must see every banned form before "none found" means anything.
	ctl := bannedExits(parseSnippet(t, `package main
import ("log"; "os")
func main() { log.Fatalf("x"); log.Fatal("x"); log.Fatalln("x"); log.Panicf("x"); os.Exit(1); fatal(nil, "x") }
func fatal(l any, f string) {}`))
	if len(ctl) != 7 {
		t.Fatalf("detector found %d of the 7 banned forms in its control source: %v", len(ctl), ctl)
	}
	if clean := bannedExits(parseSnippet(t, `package main
import ("log/slog"; "github.com/SimonOsipov/invoice-os/internal/platform")
func h(log *slog.Logger) { log.Warn("x"); platform.Fatal(log, "x") }`)); len(clean) != 0 {
		t.Fatalf("detector flagged clean source: %v", clean)
	}

	files := parseCmdTree(t)
	mains(t, files)
	byRel := map[string]scanned{}
	for _, s := range files {
		byRel[s.rel] = s
	}
	if _, ok := byRel["cmd/gateway/mockissuer.go"]; !ok {
		t.Fatal("cmd/gateway/mockissuer.go was not scanned: build-tagged files must be parsed too")
	}

	for _, s := range files {
		for _, msg := range bannedExits(s) {
			t.Error(msg)
		}
	}

	total := 0
	for rel, want := range minFatalSites {
		s, ok := byRel[rel]
		if !ok {
			t.Errorf("%s was not scanned", rel)
			continue
		}
		got := countNamed(s, "platform.Fatal")
		total += got
		if got < want {
			t.Errorf("%s calls platform.Fatal %d time(s), want at least %d", rel, got, want)
		}
	}
	for _, s := range files {
		if strings.HasSuffix(s.rel, "/main.go") && countNamed(s, "platform.Fatal") == 0 {
			t.Errorf("%s never calls platform.Fatal", s.rel)
		}
	}
	if total < totalFatalSites {
		t.Errorf("cmd calls platform.Fatal %d time(s) in total, want at least %d", total, totalFatalSites)
	}
}

// deferBootPanic reports what the first statement of func main is when it is not
// `defer platform.ReportBootPanic()`; "" means it is. recover only works when the deferred
// function is ReportBootPanic itself, so a wrapper closure does not count.
func deferBootPanic(s scanned) string {
	for _, d := range s.f.Decls {
		fd, ok := d.(*ast.FuncDecl)
		if !ok || fd.Recv != nil || fd.Name.Name != "main" || fd.Body == nil {
			continue
		}
		if len(fd.Body.List) == 0 {
			return "main is empty"
		}
		first := fd.Body.List[0]
		if ds, ok := first.(*ast.DeferStmt); ok {
			if sel, ok := ds.Call.Fun.(*ast.SelectorExpr); ok && len(ds.Call.Args) == 0 && sel.Sel.Name == "ReportBootPanic" {
				if x, ok := sel.X.(*ast.Ident); ok && importName(s.f, platformPath) == x.Name {
					return ""
				}
			}
		}
		var b strings.Builder
		_ = printer.Fprint(&b, s.fset, first)
		got, _, _ := strings.Cut(b.String(), "\n")
		return "first statement of main is `" + got + "`"
	}
	return "no func main"
}

func TestCmdMainsDeferReportBootPanic(t *testing.T) {
	const head = `package main
import "github.com/SimonOsipov/invoice-os/internal/platform"
func main() {
`
	for name, c := range map[string]struct {
		body string
		ok   bool
	}{
		"first":         {"defer platform.ReportBootPanic()\nx := 1\n_ = x", true},
		"second":        {"x := 1\ndefer platform.ReportBootPanic()\n_ = x", false},
		"in a closure":  {"defer func() { platform.ReportBootPanic() }()", false},
		"other package": {"defer other.ReportBootPanic()", false},
		"not deferred":  {"platform.ReportBootPanic()", false},
	} {
		if got := deferBootPanic(parseSnippet(t, head+c.body+"\n}\n")); (got == "") != c.ok {
			t.Fatalf("detector control %q: reported %q, want ok=%v", name, got, c.ok)
		}
	}

	for _, s := range mains(t, parseCmdTree(t)) {
		if msg := deferBootPanic(s); msg != "" {
			t.Errorf("%s: %s, want defer platform.ReportBootPanic()", s.rel, msg)
		}
	}
}

var (
	urlLikeName = regexp.MustCompile(`(?i)(dsn|database_url|_url|url)$`)
	urlLikeWord = regexp.MustCompile(`(?i)url|dsn`)
	errLikeName = regexp.MustCompile(`(?i)err$`)
)

// rawURLReads lists what a boot exit formats that reads a DSN or URL value. ScrubText has
// no rule for URL userinfo, so a raw value would reach the log and Sentry with its password.
func rawURLReads(f *ast.File, c exitCall) []string {
	osn := importName(f, "os")
	exprs := c.values
	if lit, ok := c.format.(*ast.BasicLit); c.format != nil && !ok || ok && lit.Kind != token.STRING {
		exprs = append([]ast.Expr{c.format}, exprs...)
	}
	var out []string
	for _, e := range exprs {
		ast.Inspect(e, func(n ast.Node) bool {
			switch x := n.(type) {
			case *ast.Ident:
				if urlLikeName.MatchString(x.Name) {
					out = append(out, "reads "+x.Name)
				}
			case *ast.CallExpr:
				var env bool
				switch fn := x.Fun.(type) {
				case *ast.Ident:
					env = fn.Name == "mustEnv"
				case *ast.SelectorExpr:
					id, ok := fn.X.(*ast.Ident)
					env = ok && osn != "" && id.Name == osn && (fn.Sel.Name == "Getenv" || fn.Sel.Name == "LookupEnv")
				}
				if env && len(x.Args) > 0 {
					if lit, ok := x.Args[0].(*ast.BasicLit); ok && urlLikeWord.MatchString(lit.Value) {
						out = append(out, "reads env "+lit.Value)
					}
				}
			}
			return true
		})
	}
	// A format that names a URL or DSN may only format an error: any other value is that URL.
	if lit, ok := c.format.(*ast.BasicLit); ok && urlLikeWord.MatchString(lit.Value) {
		for _, v := range c.values {
			if id, ok := v.(*ast.Ident); ok && errLikeName.MatchString(id.Name) {
				continue
			}
			if call, ok := v.(*ast.CallExpr); ok {
				if sel, ok := call.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == "Error" {
					continue
				}
			}
			out = append(out, "formats "+types.ExprString(v)+" under a format naming a URL or DSN")
		}
	}
	return out
}

func TestCmdFatalNeverFormatsARawDSNOrURL(t *testing.T) {
	const head = `package main
import ("os"; "log"; "github.com/SimonOsipov/invoice-os/internal/platform")
func main() {
`
	for src, want := range map[string]int{
		`platform.Fatal(l, "x %s", dsn)`:                            1,
		`platform.Fatal(l, "x %v", cfg.DatabaseURL)`:                1,
		`platform.Fatal(l, "x %s", os.Getenv("REDIS_URL"))`:         1,
		`platform.Fatal(l, "x %s", mustEnv("DATABASE_READER_URL"))`: 1,
		`fatal(log, "AUTH_SITE_URL=%q is bad", raw)`:                1,
		`log.Fatalf("x %s", appDSN)`:                                1,
		`platform.Fatal(l, "db pool: %v", err)`:                     0,
		`platform.Fatal(l, "AUTH_SITE_URL: %v", err)`:               0,
		`platform.Fatal(l, "%s is required", key)`:                  0,
		`log.Fatal("DATABASE_URL is required")`:                     0,
		`platform.Fatal(l, "x %s", os.Getenv("PORT"))`:              0,
	} {
		s := parseSnippet(t, head+src+"\n}\n")
		got := 0
		for _, c := range exitCalls(s.f) {
			got += len(rawURLReads(s.f, c))
		}
		if got != want {
			t.Fatalf("detector control %s: %d finding(s), want %d", src, got, want)
		}
	}

	files := parseCmdTree(t)
	mains(t, files)
	sites := 0
	for _, s := range files {
		for _, c := range exitCalls(s.f) {
			sites++
			for _, why := range rawURLReads(s.f, c) {
				t.Errorf("%s: %s %s; pass the error or a redacted value", line(s, c.call), c.name, why)
			}
		}
	}
	if sites < totalFatalSites {
		t.Errorf("scanned %d boot exit call(s), want at least %d", sites, totalFatalSites)
	}
}

// loggerViolations applies the boot-exit logger rule to each platform.Fatal call: main uses
// slog.Default() on the platform.New failure and app.Logger after it; a helper uses the
// logger it was given or slog.Default().
func loggerViolations(s scanned) []string {
	pf := importName(s.f, platformPath)
	var out []string
	for _, d := range s.f.Decls {
		fd, ok := d.(*ast.FuncDecl)
		if !ok || fd.Body == nil {
			continue
		}
		isMain := fd.Recv == nil && fd.Name.Name == "main"
		var newFailure *ast.BlockStmt
		if isMain {
			for i, st := range fd.Body.List {
				as, ok := st.(*ast.AssignStmt)
				if !ok || len(as.Rhs) != 1 || i+1 >= len(fd.Body.List) {
					continue
				}
				if call, ok := as.Rhs[0].(*ast.CallExpr); ok {
					if sel, ok := call.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == "New" {
						if x, ok := sel.X.(*ast.Ident); ok && pf != "" && x.Name == pf {
							if ifs, ok := fd.Body.List[i+1].(*ast.IfStmt); ok {
								newFailure = ifs.Body
							}
							break
						}
					}
				}
			}
		}
		ast.Inspect(fd.Body, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || sel.Sel.Name != "Fatal" || len(call.Args) == 0 {
				return true
			}
			if x, ok := sel.X.(*ast.Ident); !ok || pf == "" || x.Name != pf {
				return true
			}
			got := types.ExprString(call.Args[0])
			switch {
			case isMain && newFailure != nil && call.Pos() >= newFailure.Pos() && call.End() <= newFailure.End():
				if got != "slog.Default()" {
					out = append(out, line(s, call)+": platform.New failure logs on "+got+", want slog.Default()")
				}
			case isMain:
				if got != "app.Logger" {
					out = append(out, line(s, call)+": main logs on "+got+", want app.Logger")
				}
			default:
				if got != "slog.Default()" && got != "log" && got != "logger" {
					out = append(out, line(s, call)+": "+fd.Name.Name+" logs on "+got+", want its logger parameter or slog.Default()")
				}
			}
			return true
		})
	}
	return out
}

func TestCmdFatalLoggerFollowsTheRule(t *testing.T) {
	const src = `package main
import ("log/slog"; "github.com/SimonOsipov/invoice-os/internal/platform")
func main() {
	app, err := platform.New("x")
	if err != nil { platform.Fatal(%s, "x: %%v", err) }
	if err != nil { platform.Fatal(%s, "y: %%v", err) }
}
func helper(log *slog.Logger) { platform.Fatal(%s, "z") }
`
	for _, c := range []struct {
		a, b, h string
		want    int
	}{
		{"slog.Default()", "app.Logger", "log", 0},
		{"app.Logger", "app.Logger", "log", 1},
		{"slog.Default()", "slog.Default()", "log", 1},
		{"slog.Default()", "app.Logger", "nil", 1},
	} {
		s := parseSnippet(t, fmt.Sprintf(src, c.a, c.b, c.h))
		if got := loggerViolations(s); len(got) != c.want {
			t.Fatalf("detector control %+v: %d violation(s) %v, want %d", c, len(got), got, c.want)
		}
	}

	files := parseCmdTree(t)
	mains(t, files)
	total := 0
	for _, s := range files {
		total += countNamed(s, "platform.Fatal")
		for _, msg := range loggerViolations(s) {
			t.Error(msg)
		}
	}
	if total < totalFatalSites {
		t.Errorf("cmd calls platform.Fatal %d time(s), want at least %d", total, totalFatalSites)
	}
}

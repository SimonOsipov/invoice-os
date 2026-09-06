// audit_internal_test.go: the shape of the terminal-outcome audit seam. audit_test.go pins the
// same seam from outside the package, which is where subtask 02's adapter calls it from.
package extraction

import (
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
)

// The seam is a func value, so deps_test.go's fence keeps internal/audit out of this
// package. Pins the exact signature: a func type nothing references drifts silently.
var _ RecordExtractionAudit = func(context.Context, pgx.Tx, ExtractionAudit) error { return nil }

// ---------------------------------------------------------------------------
// Shared corpus: this package's non-test .go files
// ---------------------------------------------------------------------------

// auditPkgFiles parses this package's production source. A test binary's CWD is its own
// package directory, so "*.go" is the whole package. Floored: a walk that reads nothing
// reports every absence assertion below as clean.
func auditPkgFiles(t *testing.T) (*token.FileSet, []*ast.File, []string) {
	t.Helper()

	names, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatalf("glob *.go: %v", err)
	}
	fset := token.NewFileSet()
	var files []*ast.File
	var parsed []string
	for _, name := range names {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v -- a file the scan cannot read is a file it reports clean on", name, err)
		}
		files = append(files, f)
		parsed = append(parsed, name)
	}
	if len(files) < 8 {
		t.Fatalf("parsed %d non-test .go file(s) in internal/extraction, want at least 8 (12 measured); a clean report over a broken walk means nothing", len(files))
	}
	return fset, files, parsed
}

// ---------------------------------------------------------------------------
// T1-1 — the FailureKind vocabulary
// ---------------------------------------------------------------------------

// failureKindConsts reads every const declared with an explicit FailureKind type, keyed by
// identifier. reflect cannot enumerate a Go const block, so a fifth value is only findable
// in source. Inside one block a spec with neither type nor value inherits the previous
// spec's type, so the carry-over is tracked.
func failureKindConsts(t *testing.T) map[string]string {
	t.Helper()

	_, files, _ := auditPkgFiles(t)
	got := map[string]string{}
	for _, f := range files {
		for _, decl := range f.Decls {
			gd, ok := decl.(*ast.GenDecl)
			if !ok || gd.Tok != token.CONST {
				continue
			}
			var carried string
			for _, spec := range gd.Specs {
				vs, ok := spec.(*ast.ValueSpec)
				if !ok {
					continue
				}
				switch {
				case vs.Type != nil:
					carried = ""
					if id, ok := vs.Type.(*ast.Ident); ok {
						carried = id.Name
					}
				case len(vs.Values) > 0:
					// An untyped value restarts the block's carried type.
					carried = ""
				}
				if carried != "FailureKind" {
					continue
				}
				for i, name := range vs.Names {
					lit := ""
					if i < len(vs.Values) {
						if bl, ok := vs.Values[i].(*ast.BasicLit); ok && bl.Kind == token.STRING {
							s, err := strconv.Unquote(bl.Value)
							if err != nil {
								t.Fatalf("unquote %s = %s: %v", name.Name, bl.Value, err)
							}
							lit = s
						}
					}
					got[name.Name] = lit
				}
			}
		}
	}
	return got
}

// EXTR-17-01 AC-3: the text read is its own stage, so it is its own kind. Valid() is the only
// gate on the extraction failure_kind -- the sole failure_kind CHECK in migrations is on the
// invoices column, a different table with a disjoint vocabulary -- so a kind Valid() rejects is
// a payload the adapter refuses to write.
func TestFailureKind_TextNotReadIsValid(t *testing.T) {
	if got, want := FailureTextNotRead, FailureKind("text_not_read"); got != want {
		t.Errorf("FailureTextNotRead = %q, want %q", got, want)
	}
	if !FailureTextNotRead.Valid() {
		t.Errorf("FailureKind(%q).Valid() = false, want true; the adapter gates its failure branch on Valid(), so a kind it rejects is a terminal outcome that goes unrecorded", FailureTextNotRead)
	}

	// Not vacuous: Valid() still refuses what is not a kind. Without this a Valid() that
	// returned true for everything would pass the assertion above.
	for _, k := range []FailureKind{"", "text_not_readable", "TEXT_NOT_READ", "quokka"} {
		if k.Valid() {
			t.Errorf("FailureKind(%q).Valid() = true, want false", k)
		}
	}
}

// EXTR-19-03 AC-1. The values are spelled as literals, not as the consts: a boxless layout the
// reader could not store is its own stage, and this test has to be able to red before the const
// FailureLayoutNotWritten exists.
func TestFailureKind_VocabularyIsExactlySix(t *testing.T) {
	want := map[string]FailureKind{
		"FailureDocumentUnavailable": "document_unavailable",
		"FailurePagesNotRendered":    "pages_not_rendered",
		"FailurePageRowsNotWritten":  "page_rows_not_written",
		"FailureExtractFailed":       "extract_failed",
		"FailureTextNotRead":         "text_not_read",
		"FailureLayoutNotWritten":    "layout_not_written",
	}

	seen := map[FailureKind]string{}
	for name, k := range want {
		if !k.Valid() {
			t.Errorf("%s (%q).Valid() = false, want true", name, k)
		}
		if k == "" {
			t.Errorf("%s is the empty string; a blank kind is indistinguishable from an unset field", name)
		}
		if prior, dup := seen[k]; dup {
			t.Errorf("%s and %s both carry %q; the six kinds must be pairwise distinct", prior, name, k)
		}
		seen[k] = name
	}
	if len(seen) != 6 {
		t.Errorf("the six names resolve to %d distinct value(s), want 6", len(seen))
	}

	// "" is invalid too: a success carries no kind, and Valid() is what the adapter would
	// use to refuse a half-filled failure payload.
	for _, k := range []FailureKind{"", "quokka", "extraction_failed", "Document_Unavailable", "document_unavailable ", "layout_not_writen", "LAYOUT_NOT_WRITTEN"} {
		if k.Valid() {
			t.Errorf("FailureKind(%q).Valid() = true, want false", k)
		}
	}

	// Exactly six in source: reflect cannot see a const, so a seventh one added later has to
	// be a deliberate edit here.
	got := failureKindConsts(t)
	if len(got) != len(want) {
		names := make([]string, 0, len(got))
		for name := range got {
			names = append(names, name)
		}
		t.Errorf("internal/extraction declares %d FailureKind const(s) %v, want exactly the %d named here", len(got), names, len(want))
	}
	for name, k := range want {
		lit, ok := got[name]
		if !ok {
			t.Errorf("const %s is not declared with an explicit FailureKind type", name)
			continue
		}
		if FailureKind(lit) != k {
			t.Errorf("const %s = %q, want %q", name, lit, k)
		}
	}
	for name := range got {
		if _, ok := want[name]; !ok {
			t.Errorf("const %s is a FailureKind this test does not name; a seventh kind widens the audit vocabulary and needs a label", name)
		}
	}
}

// EXTR-15-01 FK-10. Two packages ship a FailureKind and both travel under the wire key
// failure_kind: this package's six on extraction_jobs, internal/submission's three on
// invoices. The collision is deliberate (D-15) and safe only while the vocabularies are
// disjoint -- a shared value would make a rendered label ambiguous.
//
// A source scan, not an import: deps_test.go's fence runs `go list -deps -test`, so importing
// internal/submission from a test file in this package would break it.
func TestFailureKind_DisjointFromSubmissionsVocabulary(t *testing.T) {
	mine := failureKindConsts(t)
	if len(mine) != 6 {
		t.Fatalf("internal/extraction declares %d FailureKind const(s) (%v), want exactly 6", len(mine), mine)
	}

	const submissionSrc = "../submission/failure.go"
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, submissionSrc, nil, 0)
	if err != nil {
		t.Fatalf("parse %s: %v -- a file the scan cannot read is a file it reports clean on", submissionSrc, err)
	}

	theirs := map[string]string{}
	for _, decl := range f.Decls {
		gd, ok := decl.(*ast.GenDecl)
		if !ok || gd.Tok != token.CONST {
			continue
		}
		var carried string
		for _, spec := range gd.Specs {
			vs, ok := spec.(*ast.ValueSpec)
			if !ok {
				continue
			}
			switch {
			case vs.Type != nil:
				carried = ""
				if id, ok := vs.Type.(*ast.Ident); ok {
					carried = id.Name
				}
			case len(vs.Values) > 0:
				carried = ""
			}
			if carried != "FailureKind" {
				continue
			}
			for i := range vs.Names {
				if i >= len(vs.Values) {
					continue
				}
				bl, ok := vs.Values[i].(*ast.BasicLit)
				if !ok || bl.Kind != token.STRING {
					continue
				}
				lit, err := strconv.Unquote(bl.Value)
				if err != nil {
					t.Fatalf("unquote %s = %s: %v", vs.Names[i].Name, bl.Value, err)
				}
				theirs[vs.Names[i].Name] = lit
			}
		}
	}
	if len(theirs) != 3 {
		t.Fatalf("%s declares %d FailureKind const(s) (%v), want exactly 3", submissionSrc, len(theirs), theirs)
	}

	byValue := map[string]string{}
	for name, lit := range mine {
		byValue[lit] = "internal/extraction." + name
	}
	for name, lit := range theirs {
		if prior, clash := byValue[lit]; clash {
			t.Errorf("internal/submission.%s and %s both carry %q; one failure_kind value would render two different sentences",
				name, prior, lit)
		}
	}
}

// ---------------------------------------------------------------------------
// T1-2 — no error text in the payload
// ---------------------------------------------------------------------------

// auditMessageFieldNames are the names an error string arrives under. Matched
// case-insensitively on the whole identifier.
var auditMessageFieldNames = []string{"Error", "Err", "LastError", "Message", "Detail", "Raw", "Body", "Wire"}

func TestExtractionAudit_CarriesNoErrorTextField(t *testing.T) {
	rt := reflect.TypeOf(ExtractionAudit{})
	if rt.Kind() != reflect.Struct {
		t.Fatalf("ExtractionAudit is a %s, want a struct", rt.Kind())
	}
	if rt.NumField() < 8 {
		t.Fatalf("ExtractionAudit has %d field(s), want at least 8 (9 specified); a walk over an emptied or renamed type reports clean", rt.NumField())
	}

	// Positive: the specified carriers, by name and type. Without this the absence
	// assertion below is satisfied by a struct of eight unrelated fields.
	kindType := reflect.TypeOf(FailureKind(""))
	wantFields := map[string]reflect.Type{
		"Succeeded":        reflect.TypeOf(false),
		"DocumentID":       reflect.TypeOf(""),
		"ExtractionJobID":  reflect.TypeOf(""),
		"Extractor":        reflect.TypeOf(""),
		"ExtractorVersion": reflect.TypeOf(""),
		"FieldCount":       reflect.TypeOf(0),
		"FlaggedCount":     reflect.TypeOf(0),
		"State":            reflect.TypeOf(""),
		"FailureKind":      kindType,
	}
	for name, typ := range wantFields {
		f, ok := rt.FieldByName(name)
		if !ok {
			t.Errorf("ExtractionAudit has no field %s", name)
			continue
		}
		if f.Type != typ {
			t.Errorf("ExtractionAudit.%s is %s, want %s", name, f.Type, typ)
		}
	}

	errType := reflect.TypeOf((*error)(nil)).Elem()
	banned := make(map[string]bool, len(auditMessageFieldNames))
	for _, n := range auditMessageFieldNames {
		banned[strings.ToLower(n)] = true
	}
	for i := 0; i < rt.NumField(); i++ {
		f := rt.Field(i)
		if f.Type.Implements(errType) || reflect.PointerTo(f.Type).Implements(errType) {
			t.Errorf("ExtractionAudit.%s is %s, which carries an error; the payload records a FailureKind, never error text", f.Name, f.Type)
		}
		if banned[strings.ToLower(f.Name)] {
			t.Errorf("ExtractionAudit.%s (%s) is named for a message; free text reaches audit_log through exactly this kind of field", f.Name, f.Type)
		}
	}
}

// ---------------------------------------------------------------------------
// T1-3 — no audit event name in this package
// ---------------------------------------------------------------------------

// auditEventNameLits returns every string literal in f carrying the prefix. One matcher,
// shared by the production scan and its control.
func auditEventNameLits(f *ast.File, prefix string) []string {
	var hits []string
	ast.Inspect(f, func(n ast.Node) bool {
		bl, ok := n.(*ast.BasicLit)
		if !ok || bl.Kind != token.STRING {
			return true
		}
		s, err := strconv.Unquote(bl.Value)
		if err != nil {
			return true
		}
		if strings.HasPrefix(s, prefix) {
			hits = append(hits, s)
		}
		return true
	})
	return hits
}

// A drift guard, not a red spec: zero hits is the measured state, so this passes as soon as
// audit.go lands and the package compiles. It reds the day an event name is declared here,
// which would put that call site outside internal/platform/db's repo-wide audit.Record
// partition.
func TestExtractionPackage_DeclaresNoEventName(t *testing.T) {
	fset, files, names := auditPkgFiles(t)

	// The prefix is anchored twice: derived from the parsed package name, then checked
	// against the domain spelled out. A control needle built from the prefix proves the
	// matcher works but never that the prefix is right — mutate one anchor and this bites,
	// which is what a single derivation did not do.
	prefix := files[0].Name.Name + "."
	if prefix != "extraction." {
		t.Fatalf("the event-name prefix resolves to %q, want %q; a wrong prefix makes the clean scan below meaningless", prefix, "extraction.")
	}

	// Control needle: the same matcher over a source that plants exactly one hit. Zero hits
	// below is evidence only once the matcher has been shown finding one.
	planted := prefix + "planted"
	src := "package p\n\nvar needle = " + strconv.Quote(planted) + "\n"
	ctrl, err := parser.ParseFile(fset, "audit_event_name_control.go", src, 0)
	if err != nil {
		t.Fatalf("parse the control needle: %v", err)
	}
	hits := auditEventNameLits(ctrl, prefix)
	if len(hits) != 1 || hits[0] != planted {
		t.Fatalf("the control needle matched %v in a source planting exactly one %q literal; the matcher is broken, so the clean scan below proves nothing", hits, planted)
	}

	for i, f := range files {
		if hits := auditEventNameLits(f, prefix); len(hits) > 0 {
			t.Errorf("%s declares audit event name(s) %v; the name belongs at the adapter in cmd/submission, where the repo-wide audit.Record scan can see it", names[i], hits)
		}
	}
}

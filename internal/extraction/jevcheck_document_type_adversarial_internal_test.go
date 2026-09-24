package extraction

import (
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/SimonOsipov/invoice-os/internal/platform/jev"
)

// NaN compares false both ways, so a `<` guard lets it through.
func TestDocumentType_ANaNConfidenceRecordsNothing(t *testing.T) {
	if got := documentTypeVerdict(dtAnswer("receipt", math.NaN())); got != "" {
		t.Errorf("receipt at NaN: verdict = %q, want none", got)
	}
	if got := documentTypeVerdict(dtAnswer("receipt", 0.9)); got != "receipt" {
		t.Errorf("receipt at 0.9: verdict = %q, want %q", got, "receipt")
	}
}

func TestDocumentType_AnOutOfRangeConfidenceRecordsNothing(t *testing.T) {
	for _, c := range []float64{-0.1, math.Inf(-1)} {
		if got := documentTypeVerdict(dtAnswer("receipt", c)); got != "" {
			t.Errorf("receipt at %v: verdict = %q, want none", c, got)
		}
	}
	// Control: the same name at the threshold records.
	if got := documentTypeVerdict(dtAnswer("receipt", 0.9)); got != "receipt" {
		t.Errorf("receipt at 0.9: verdict = %q, want %q", got, "receipt")
	}
}

func TestDocumentType_ANameDifferingOnlyInCaseOrSpaceRecordsNothing(t *testing.T) {
	for _, name := range []string{"Receipt", "RECEIPT", " receipt", "receipt ", "credit  note", "creditnote", "Tax Invoice"} {
		if got := documentTypeVerdict(dtAnswer(name, 1)); got != "" {
			t.Errorf("%q at 1: verdict = %q, want none", name, got)
		}
	}
	if got := documentTypeVerdict(dtAnswer("credit note", 1)); got != "credit note" {
		t.Errorf("credit note at 1: verdict = %q, want %q", got, "credit note")
	}
}

func TestDocumentType_AChoiceNameUnderAnotherTypeRecordsNothing(t *testing.T) {
	for _, typ := range []jev.QuestionType{jev.TypeNoul, jev.TypeScore, ""} {
		resp := jev.Response{Answers: map[string]jev.Answer{
			"document_type": {Type: typ, Choice: "receipt", Confidence: 1, Noul: 1},
		}}
		if got := documentTypeVerdict(resp); got != "" {
			t.Errorf("type %q naming receipt: verdict = %q, want none", typ, got)
		}
	}
	if got := documentTypeVerdict(dtAnswer("receipt", 1)); got != "receipt" {
		t.Errorf("choice naming receipt: verdict = %q, want %q", got, "receipt")
	}
}

func TestDocumentType_OnlyTheDocumentTypeIDIsRead(t *testing.T) {
	resp := jev.Response{Answers: map[string]jev.Answer{
		"vat":           {Type: jev.TypeChoice, Choice: "receipt", Confidence: 1},
		"Document_Type": {Type: jev.TypeChoice, Choice: "receipt", Confidence: 1},
	}}
	if got := documentTypeVerdict(resp); got != "" {
		t.Errorf("receipt under other ids: verdict = %q, want none", got)
	}
}

func TestDocumentType_TheOptionNamesAreDistinctAndHoldTheDefault(t *testing.T) {
	q := DocumentTypeQuestion()
	if len(q.Options) != 8 {
		t.Fatalf("%d options, want 8", len(q.Options))
	}
	seen := map[string]bool{}
	for _, o := range q.Options {
		if o.Name == "" || seen[o.Name] {
			t.Errorf("option name %q is empty or repeated", o.Name)
		}
		seen[o.Name] = true
	}
	if !seen[q.Default] {
		t.Errorf("Default %q names no option", q.Default)
	}
}

func TestDocumentType_EachQuestionIsAFreshCopy(t *testing.T) {
	q := DocumentTypeQuestion()
	if len(q.Options) < 2 {
		t.Fatalf("%d options, want 8", len(q.Options))
	}
	q.Options[1].Name = "tampered"
	q.Options[1].Description = "tampered"

	again := DocumentTypeQuestion()
	if again.Options[1].Name != "receipt" || strings.Contains(again.Options[1].Description, "tampered") {
		t.Errorf("a caller's edit reached the next question: option 1 = %+v", again.Options[1])
	}
	if got := documentTypeVerdict(dtAnswer("receipt", 1)); got != "receipt" {
		t.Errorf("after a caller's edit: verdict = %q, want %q", got, "receipt")
	}
}

// An Ask that edits the request it was handed must not change the next call's question.
type dtTamperingAsker struct{ vcAsker }

func (a *dtTamperingAsker) Ask(ctx context.Context, req jev.Request) (jev.Response, error) {
	if q, ok := req.Questions["document_type"]; ok && len(q.Options) > 0 {
		q.Options[0].Name = "tampered"
	}
	return a.vcAsker.Ask(ctx, req)
}

func TestDocumentType_AnAskerEditingTheRequestChangesNoLaterCall(t *testing.T) {
	a := &dtTamperingAsker{vcAsker{enabled: true, resp: dtAnswer("receipt", 1)}}
	checkDocument(t.Context(), a, vcPages(), nil)
	checkDocument(t.Context(), a, vcPages(), nil)
	if len(a.calls) != 2 {
		t.Fatalf("%d Ask calls, want 2", len(a.calls))
	}
	if got := DocumentTypeQuestion().Options[0].Name; got != "tax invoice" {
		t.Errorf("DocumentTypeQuestion().Options[0].Name = %q after an asker edit, want %q", got, "tax invoice")
	}
}

func TestDocumentType_TheSupplierPairAloneAsksOnlyTheDocumentType(t *testing.T) {
	a := &vcAsker{enabled: true, resp: dtAnswer("receipt", 1)}
	results := vcRows("supplier_tin", "supplier_name")
	before := cloneResults(results)

	out, verdict := checkDocument(t.Context(), a, vcPages(), results)

	if len(a.calls) != 1 {
		t.Fatalf("%d Ask calls, want 1", len(a.calls))
	}
	if got := vcIDs(a.calls[0]); !slices.Equal(got, []string{"document_type"}) {
		t.Errorf("question ids = %v, want [document_type]", got)
	}
	if a.calls[0].Purpose != jev.PurposeDocumentType {
		t.Errorf("Purpose = %q, want %q", a.calls[0].Purpose, jev.PurposeDocumentType)
	}
	if !reflect.DeepEqual(out, before) || verdict != "receipt" {
		t.Errorf("rows = %+v, verdict = %q; want the input and %q", out, verdict, "receipt")
	}
}

func TestDocumentType_AValueAnswerOfTheWrongTypeLeavesTheVerdict(t *testing.T) {
	resp := dtAnswer("statement", 1)
	resp.Answers["vat"] = jev.Answer{Type: jev.TypeChoice, Choice: "receipt", Confidence: 1}
	results := vcRows("vat")
	before := cloneResults(results)

	out, verdict := checkDocument(t.Context(), &vcAsker{enabled: true, resp: resp}, vcPages(), results)

	if !reflect.DeepEqual(out, before) {
		t.Errorf("rows = %+v, want the input", out)
	}
	if verdict != "statement" {
		t.Errorf("verdict = %q, want %q", verdict, "statement")
	}
}

func TestDocumentType_ATaxInvoiceAnswerLeavesTheValueCheck(t *testing.T) {
	for label, dt := range map[string]jev.Answer{
		"tax invoice": {Type: jev.TypeChoice, Choice: "tax invoice", Confidence: 1},
		"below 0.9":   {Type: jev.TypeChoice, Choice: "receipt", Confidence: 0.5},
		"unknown":     {Type: jev.TypeChoice, Choice: "other", Confidence: 1},
	} {
		resp := vcAnswers(0.1, "vat", "total")
		resp.Answers["document_type"] = dt

		out, verdict := checkDocument(t.Context(), &vcAsker{enabled: true, resp: resp}, vcPages(), vcRows("vat", "total"))

		if flaggedCount(out) != 2 || verdict != "" {
			t.Errorf("%s: %d flagged, verdict %q; want 2 and none", label, flaggedCount(out), verdict)
		}
	}
}

// dtFuncDecls parses every non-test .go file in this package; comments are not parsed.
func dtFuncDecls(t *testing.T) map[string]int {
	t.Helper()
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	decls := map[string]int{}
	for _, name := range files {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		for _, d := range f.Decls {
			if fd, ok := d.(*ast.FuncDecl); ok && fd.Recv == nil {
				decls[fd.Name.Name]++
			}
		}
	}
	return decls
}

func TestDocumentType_CheckValuesIsGone(t *testing.T) {
	decls := dtFuncDecls(t)
	if len(decls) < 100 {
		t.Fatalf("parsed %d package functions, want at least 100; the scan read the wrong directory", len(decls))
	}
	if decls["checkDocument"] != 1 || decls["valueCheckRequest"] != 1 {
		t.Fatalf("checkDocument declared %d times, valueCheckRequest %d; want 1 each", decls["checkDocument"], decls["valueCheckRequest"])
	}
	if n := decls["checkValues"]; n != 0 {
		t.Errorf("checkValues declared %d times, want 0", n)
	}
}

// dtJevcheckDecl returns jevcheck.go's top-level GenDecl declaring name, with its file set and source.
func dtJevcheckDecl(t *testing.T, name string) (*ast.GenDecl, *token.FileSet) {
	t.Helper()
	raw, err := os.ReadFile("jevcheck.go")
	if err != nil {
		t.Fatalf("read jevcheck.go: %v", err)
	}
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "jevcheck.go", raw, parser.ParseComments)
	if err != nil {
		t.Fatalf("parse jevcheck.go: %v", err)
	}
	for _, d := range f.Decls {
		gd, ok := d.(*ast.GenDecl)
		if !ok {
			continue
		}
		for _, s := range gd.Specs {
			if vs, ok := s.(*ast.ValueSpec); ok && slices.ContainsFunc(vs.Names, func(n *ast.Ident) bool { return n.Name == name }) {
				return gd, fset
			}
		}
	}
	t.Fatalf("jevcheck.go declares no %s", name)
	return nil, nil
}

func TestDocumentType_TheOptionsAreKeyedOnePerLine(t *testing.T) {
	gd, fset := dtJevcheckDecl(t, "documentTypeOptions")
	lit, ok := gd.Specs[0].(*ast.ValueSpec).Values[0].(*ast.CompositeLit)
	if !ok || len(lit.Elts) != 8 {
		t.Fatalf("documentTypeOptions is not an eight-element literal")
	}
	lines := map[int]bool{}
	for i, e := range lit.Elts {
		el, ok := e.(*ast.CompositeLit)
		if !ok || len(el.Elts) != 2 {
			t.Errorf("option %d is not a two-field literal", i)
			continue
		}
		var keys []string
		for _, kv := range el.Elts {
			if kv, ok := kv.(*ast.KeyValueExpr); ok {
				keys = append(keys, kv.Key.(*ast.Ident).Name)
			}
		}
		if !slices.Equal(keys, []string{"Name", "Description"}) {
			t.Errorf("option %d keys = %v, want [Name Description]", i, keys)
		}
		start, end := fset.Position(el.Pos()).Line, fset.Position(el.End()).Line
		if start != end || lines[start] {
			t.Errorf("option %d spans lines %d-%d or shares a line", i, start, end)
		}
		lines[start] = true
	}
}

func TestDocumentType_TheThresholdCarriesACeilingLine(t *testing.T) {
	gd, _ := dtJevcheckDecl(t, "documentTypeThreshold")
	if gd.Doc == nil || len(gd.Doc.List) == 0 {
		t.Fatal("documentTypeThreshold has no comment")
	}
	n := 0
	for _, c := range gd.Doc.List {
		if strings.HasPrefix(c.Text, "// ceiling: ") && len(c.Text) > len("// ceiling: ") {
			n++
		}
	}
	if n != 1 {
		t.Errorf("documentTypeThreshold's comment carries %d ceiling: lines, want 1", n)
	}
}

package invoice

import (
	"go/ast"
	"go/parser"
	"go/token"
	"reflect"
	"strconv"
	"strings"
	"testing"
)

// numberWords maps number-words to their value, one..twenty. Safe to keep
// broad because every equality check below is paragraph-scoped first
// (Stage-1 note: an unscoped scan would collide with invoice.go's unrelated
// "first two" / "five filters" / "on one query").
var numberWords = map[string]int{
	"one": 1, "two": 2, "three": 3, "four": 4, "five": 5,
	"six": 6, "seven": 7, "eight": 8, "nine": 9, "ten": 10,
	"eleven": 11, "twelve": 12, "thirteen": 13, "fourteen": 14, "fifteen": 15,
	"sixteen": 16, "seventeen": 17, "eighteen": 18, "nineteen": 19, "twenty": 20,
}

func wordForNumber(n int) (string, bool) {
	for w, v := range numberWords {
		if v == n {
			return w, true
		}
	}
	return "", false
}

// commentCountMatches reports whether word (e.g. "eleven") equals n. Shared
// between the real-file check and the synthetic mismatch test so both use
// the identical comparison logic.
func commentCountMatches(word string, n int) (bool, string) {
	want, ok := numberWords[strings.ToLower(word)]
	if !ok {
		return false, "\"" + word + "\" is not a recognised number-word"
	}
	if want != n {
		return false, "comment says \"" + word + "\" (" + strconv.Itoa(want) +
			") but ListFilter has " + strconv.Itoa(n) + " fields"
	}
	return true, ""
}

// paragraphs splits ast.CommentGroup.Text() on the blank lines it preserves
// between paragraphs.
func paragraphs(text string) []string {
	return strings.Split(strings.TrimSpace(text), "\n\n")
}

// findParagraph returns the paragraph containing anchor, so an equality
// check never scans unrelated paragraphs in the same doc comment.
func findParagraph(text, anchor string) (string, bool) {
	for _, p := range paragraphs(text) {
		if strings.Contains(p, anchor) {
			return p, true
		}
	}
	return "", false
}

// extractNumberWords returns every recognised number-word in text, in order.
func extractNumberWords(text string) []string {
	var found []string
	for _, w := range strings.FieldsFunc(text, func(r rune) bool {
		return !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z')
	}) {
		if _, ok := numberWords[strings.ToLower(w)]; ok {
			found = append(found, w)
		}
	}
	return found
}

func mustParseFile(t *testing.T, filename string) *ast.File {
	t.Helper()
	fset := token.NewFileSet()
	// Bare relative filename: go test's cwd is the package dir
	// (list_approve_gate_test.go's os.ReadFile("handlers.go") precedent).
	f, err := parser.ParseFile(fset, filename, nil, parser.ParseComments)
	if err != nil {
		t.Fatalf("parse %s: %v", filename, err)
	}
	return f
}

// listHandlerDoc returns ListHandler's doc comment (handlers.go), which
// carries the "ELEVEN params" count claim.
func listHandlerDoc(t *testing.T) string {
	t.Helper()
	f := mustParseFile(t, "handlers.go")
	var doc string
	ast.Inspect(f, func(n ast.Node) bool {
		fn, ok := n.(*ast.FuncDecl)
		if !ok || fn.Name.Name != "ListHandler" {
			return true
		}
		if fn.Doc != nil {
			doc = fn.Doc.Text()
		}
		return false
	})
	return doc
}

// listFilterDoc returns the struct-level doc comment directly above `type
// ListFilter struct {` (invoice.go:239-280).
func listFilterDoc(t *testing.T) string {
	t.Helper()
	f := mustParseFile(t, "invoice.go")
	var doc string
	ast.Inspect(f, func(n ast.Node) bool {
		gd, ok := n.(*ast.GenDecl)
		if !ok || gd.Tok != token.TYPE {
			return true
		}
		for _, spec := range gd.Specs {
			if ts, ok := spec.(*ast.TypeSpec); ok && ts.Name.Name == "ListFilter" && gd.Doc != nil {
				doc = gd.Doc.Text()
			}
		}
		return true
	})
	return doc
}

// keptAsIsFieldDoc returns the KeptAsIs field's own doc comment
// (invoice.go:294-301), which carries the System Design §7 toolbar citation
// -- isolated from listFilterDoc's struct-level block so a "Validated"
// match here can never be satisfied by an unrelated StatusValidated hit
// elsewhere in the file.
func keptAsIsFieldDoc(t *testing.T) string {
	t.Helper()
	f := mustParseFile(t, "invoice.go")
	var doc string
	ast.Inspect(f, func(n ast.Node) bool {
		ts, ok := n.(*ast.TypeSpec)
		if !ok || ts.Name.Name != "ListFilter" {
			return true
		}
		st, ok := ts.Type.(*ast.StructType)
		if !ok {
			return true
		}
		for _, field := range st.Fields.List {
			for _, name := range field.Names {
				if name.Name == "KeptAsIs" && field.Doc != nil {
					doc = field.Doc.Text()
				}
			}
		}
		return true
	})
	return doc
}

// TestListFilterDoc_CommentCountMatchesTheLiveFieldCount (AC-1, AC-2): ports
// A01-5's idiom (invoices.test.ts:692-726) to Go. handlers.go's count
// paragraph names the total TWICE ("ELEVEN params" and "all eleven") --
// every occurrence in the paragraph must agree with ListFilter's live field
// count, not just the first one found.
func TestListFilterDoc_CommentCountMatchesTheLiveFieldCount(t *testing.T) {
	live := reflect.TypeOf(ListFilter{}).NumField()

	doc := listHandlerDoc(t)
	if doc == "" {
		t.Fatal("ListHandler has no doc comment -- the scan is broken")
	}
	para, ok := findParagraph(doc, "params AND together today")
	if !ok {
		t.Fatal(`no paragraph anchored on "params AND together today" -- the count comment moved or was reworded`)
	}

	words := extractNumberWords(para)
	if len(words) < 2 {
		t.Fatalf("count paragraph has %d number-word(s), want >= 2 (both the leading count claim and the "+
			"\"mirrors all N\" restatement): %q", len(words), para)
	}
	for _, w := range words {
		if matched, msg := commentCountMatches(w, live); !matched {
			t.Errorf("%s", msg)
		}
	}
}

// TestListFilterDoc_ControlBothBlocksAreLocatedAndCarryANumberWord (AC-4):
// proves the scans above read real source, not empty/renamed text. Mirrors
// TestGateFile_NoTenantIdPredicate's idiom (gate_test.go:1128-1147).
func TestListFilterDoc_ControlBothBlocksAreLocatedAndCarryANumberWord(t *testing.T) {
	hDoc := listHandlerDoc(t)
	if hDoc == "" {
		t.Fatal("ListHandler's doc comment is empty -- located nothing")
	}
	hPara, ok := findParagraph(hDoc, "params AND together today")
	if !ok {
		t.Fatal(`handlers.go's count paragraph was not located`)
	}
	if len(extractNumberWords(hPara)) == 0 {
		t.Error("handlers.go's count paragraph carries no number-word -- the scan would pass vacuously")
	}

	iDoc := listFilterDoc(t)
	if iDoc == "" {
		t.Fatal("ListFilter's struct-level doc comment is empty -- located nothing")
	}
	if len(extractNumberWords(iDoc)) == 0 {
		t.Error("invoice.go's ListFilter doc block carries no number-word -- the scan would pass vacuously")
	}
}

// TestListFilterDoc_AnAddedFieldWithoutACommentEditIsCaught (AC-5, synthetic
// half): commentCountMatches held against live+1 without touching any real
// file -- proves the comparison itself is sensitive to drift. The manual
// mutate-revert of ListFilter itself is the other half, recorded in the PR
// body per the story's AC-5.
func TestListFilterDoc_AnAddedFieldWithoutACommentEditIsCaught(t *testing.T) {
	live := reflect.TypeOf(ListFilter{}).NumField()
	liveWord, ok := wordForNumber(live)
	if !ok {
		t.Fatalf("no number-word maps to %d -- extend numberWords", live)
	}
	synthetic := live + 1 // a field added, comment left untouched

	matched, msg := commentCountMatches(liveWord, synthetic)
	if matched {
		t.Fatalf("commentCountMatches(%q, %d) reported a match -- a field added without updating the "+
			"comment must be caught", liveWord, synthetic)
	}
	if !strings.Contains(msg, strconv.Itoa(live)) || !strings.Contains(msg, strconv.Itoa(synthetic)) {
		t.Errorf("mismatch message %q does not name both the comment's count (%d) and the live+1 count (%d)",
			msg, live, synthetic)
	}
}

// TestListFilterDoc_TheToolbarCitationNamesTheShippedPillSet (AC-6, D-15):
// invoice.go:299 cited System Design §7's retired "Ready to submit" pill;
// the shipped set is All/Needs a fix/Validated/Queued
// (reviewBatch.ts:879-885, REVIEW_PILL_LABELS).
func TestListFilterDoc_TheToolbarCitationNamesTheShippedPillSet(t *testing.T) {
	doc := keptAsIsFieldDoc(t)
	if !strings.Contains(doc, "Toolbar filters are server-side") {
		t.Fatal(`KeptAsIs's doc comment no longer cites System Design §7's "Toolbar filters are server-side" ` +
			`table -- located the wrong field`)
	}
	if !strings.Contains(doc, "Validated") {
		t.Error(`the §7 citation does not name "Validated", the shipped pill set`)
	}
	if strings.Contains(doc, "Ready to submit") {
		t.Error(`the §7 citation still names the retired "Ready to submit" pill`)
	}
}

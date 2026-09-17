package main

import (
	"fmt"
	"strings"
	"testing"
)

// bt stands in for a backtick, which a Go raw string cannot hold.
func bt(s string) string { return strings.ReplaceAll(s, "¦", "`") }

// at places snippet so its first line is line `first` of the returned file.
func at(first int, snippet string) string {
	return strings.Repeat("\n", first-1) + snippet
}

func showing(files map[string]string) func(string) (string, error) {
	return func(p string) (string, error) {
		src, ok := files[p]
		if !ok {
			return "", fmt.Errorf("no fixture for %s", p)
		}
		return src, nil
	}
}

func flaggedLines(t *testing.T, diff string, files map[string]string) []string {
	t.Helper()
	got, err := Scan(diff, showing(files))
	if err != nil {
		t.Fatal(err)
	}
	out := make([]string, 0, len(got))
	for _, f := range got {
		out = append(out, fmt.Sprintf("%s:%d", f.Path, f.Line))
	}
	return out
}

func assertFlagged(t *testing.T, got []string, want ...string) {
	t.Helper()
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Fatalf("flagged = %v, want %v", got, want)
	}
}

// Known positive, verbatim from PR #215 (trimmed from one large added hunk). The
// raw-string constant above the comment must not swallow it.
func TestScan_PR215GoTestCommentCitesALine(t *testing.T) {
	const path = "internal/extraction/learn_adversarial_test.go"
	src := at(1095, bt(`// lbxTotalBodyLower is lbxTotalBody's lower-case twin: learnedLabel QuoteMetas the matched text
// verbatim, so "Total" and "total" are different labels and different bodies.
const lbxTotalBodyLower = ¦{"label":"(?i)\\btotal\\b","relation":{"kind":"same_token","max_distance":0.00},"shape":"amount"}¦

// Every shipped fixture puts its label at byte 0, so none of them can tell a derivation that
// searches the token from one that only reads its head. Must-fail mutation: add
// ¦|| loc[0] != 0¦ to the loc guard at learn.go:138.
func TestLearnBoxlessRule_DerivesFromALabelInsideTheToken(t *testing.T) {
`))
	diff := bt(`--- a/internal/extraction/learn_adversarial_test.go
+++ b/internal/extraction/learn_adversarial_test.go
@@ -716,0 +1095,8 @@
+// lbxTotalBodyLower is lbxTotalBody's lower-case twin: learnedLabel QuoteMetas the matched text
+// verbatim, so "Total" and "total" are different labels and different bodies.
+const lbxTotalBodyLower = ¦{"label":"(?i)\\btotal\\b","relation":{"kind":"same_token","max_distance":0.00},"shape":"amount"}¦
+
+// Every shipped fixture puts its label at byte 0, so none of them can tell a derivation that
+// searches the token from one that only reads its head. Must-fail mutation: add
+// ¦|| loc[0] != 0¦ to the loc guard at learn.go:138.
+func TestLearnBoxlessRule_DerivesFromALabelInsideTheToken(t *testing.T) {
`)
	assertFlagged(t, flaggedLines(t, diff, map[string]string{path: src}), path+":1101")
}

// Known positive, verbatim from PR #214: a JSX block comment whose continuation
// lines carry no leading `*`, and whose text holds backticks and apostrophes.
func TestScan_PR214JSXBlockCommentContinuation(t *testing.T) {
	const path = "frontend/app/src/components/InvoiceDetail.tsx"
	src := at(1169, bt(`                          value={resolveReason}
                          onChange={(e) => setResolveReason(e.target.value)}
                          disabled={resolving}
                          className="pf-input"
                          style={{ flex: '1 1 220px', minWidth: 160, height: 32, fontSize: 12.5 }}
                        />
                        {/* Same two-layer disabled recipe as Submit -- ¦filter: 'none'¦ is
                            mandatory: this is ¦.v2-btn-primary¦, whose unguarded ¦:hover¦
                            (app-layer.css:213) sets ¦filter: brightness(1.22)¦. */}
                        <button
`))
	diff := bt(`--- a/frontend/app/src/components/InvoiceDetail.tsx
+++ b/frontend/app/src/components/InvoiceDetail.tsx
@@ -1264,3 +1175,3 @@ function LiveInvoiceDetail({ ctx, invoiceId }: { ctx: PlatformCtx; invoiceId: st
-                        {/* Same four-layer disabled recipe as Submit (:573-590) -- ¦filter:
-                            'none'¦ is mandatory: this is ¦.v2-btn-primary¦, whose unguarded
-                            ¦:hover¦ (app-layer.css:213) sets ¦filter: brightness(1.22)¦. */}
+                        {/* Same two-layer disabled recipe as Submit -- ¦filter: 'none'¦ is
+                            mandatory: this is ¦.v2-btn-primary¦, whose unguarded ¦:hover¦
+                            (app-layer.css:213) sets ¦filter: brightness(1.22)¦. */}
`)
	assertFlagged(t, flaggedLines(t, diff, map[string]string{path: src}), path+":1177")
}

// Verbatim from PR #222: the comment is flagged; the next assertion quotes the
// same shape inside a regex literal and a string, which is code.
func TestScan_PR222CommentFlaggedRegexAndStringNot(t *testing.T) {
	const path = "frontend/app/src/App.atomAudit.test.tsx"
	src := at(161, bt(`    for (const a of cited) {
      const { line } = resolveCitation(appSrc, a.citation as Citation, ¦${a.name}'s citation¦)
      expect(line, ¦${a.name}'s citation resolved to no line¦).toBeGreaterThan(0)
    }
  })

  // Prose drifts the same way structured fields did. Symbol anchors (App.tsx#switchClient)
  // survive an edit above them; ¦App.tsx:698¦ does not.
  it('guard_noAuditNoteCitesAnAppTsxLineNumber', () => {
    const audited = requireAudited()
    const offenders = audited.filter((a) => /App\.tsx:\d/.test(a.note)).map((a) => a.name)
    expect(offenders, ¦notes citing an App.tsx line number instead of App.tsx#Symbol: ${offenders.join(' | ')}¦).toEqual(
      [],
    )
    // Control needle: the assertion above must be able to match at all.
    expect(/App\.tsx:\d/.test('cleared at App.tsx:702'), 'the line-number needle matches nothing').toBe(true)
  })
`))
	var diff strings.Builder
	diff.WriteString("--- /dev/null\n+++ b/" + path + "\n@@ -0,0 +161,17 @@\n")
	for _, l := range strings.Split(strings.TrimPrefix(src, strings.Repeat("\n", 160)), "\n")[:17] {
		diff.WriteString("+" + l + "\n")
	}
	assertFlagged(t, flaggedLines(t, diff.String(), map[string]string{path: src}), path+":168")
}

// Verbatim from PR #240: a Go string holding a citation and a backtick is code,
// and must not open a raw string that swallows the comment added after it.
func TestScan_PR240CitationInsideAGoStringIsCode(t *testing.T) {
	const path = "internal/extraction/accuracy_test.go"
	src := at(773, bt(`			want: []string{"¦RowReach¦", "0.614732", "0.613281", "0.460405", "0.498444", "0.465497",
				"**Closed on both fixtures**", "R0 and R1 decide ¦invoice_number¦ and ¦issue_date¦"},
			// The far-right totals gap, the dial line it cited, and the letter-spaced gap the label view closed.
			unwant: []string{"tier1.go:43", "nor a far-right totals column exists", "stays missing on both", "¦issue_date¦ is a known gap by name"},
			// planted: see tier1.go:43
`))
	diff := bt(`--- a/internal/extraction/accuracy_test.go
+++ b/internal/extraction/accuracy_test.go
@@ -774,0 +776,2 @@
+			unwant: []string{"tier1.go:43", "nor a far-right totals column exists", "stays missing on both", "¦issue_date¦ is a known gap by name"},
+			// planted: see tier1.go:43
`)
	assertFlagged(t, flaggedLines(t, diff, map[string]string{path: src}), path+":777")
}

// A trailing comment after a string, verbatim from internal/submission/mock_adapter_test.go.
func TestScan_TrailingCommentAfterAString(t *testing.T) {
	const path = "internal/submission/mock_adapter_test.go"
	src := "const (\n\tmaTINAccept      = \"99999999-0001\" // mock_script.go:76\n\tmaURL = \"http://localhost:5432\" // see localhost:5432\n)\n"
	diff := "--- a/" + path + "\n+++ b/" + path + "\n@@ -0,0 +1,4 @@\n+const (\n+\tmaTINAccept      = \"99999999-0001\" // mock_script.go:76\n+\tmaURL = \"http://localhost:5432\" // see localhost:5432\n+)\n"
	assertFlagged(t, flaggedLines(t, diff, map[string]string{path: src}), path+":2")
}

func TestScan_OnlyAddedLinesAreJudged(t *testing.T) {
	const path = "internal/invoice/store.go"
	src := "// old: store.go:10\nfunc a() {}\n// new: store.go:20\n"
	diff := "--- a/" + path + "\n+++ b/" + path + "\n@@ -2,0 +3 @@\n+// new: store.go:20\n"
	assertFlagged(t, flaggedLines(t, diff, map[string]string{path: src}), path+":3")
}

func TestScan_DocsAndMarkdownAreOutOfScope(t *testing.T) {
	diff := "--- a/docs/ops.sh\n+++ b/docs/ops.sh\n@@ -0,0 +1 @@\n+# see store.go:1\n" +
		"--- a/README.md\n+++ b/README.md\n@@ -0,0 +1 @@\n+see store.go:1\n"
	// No fixture files: showing either one would fail the scan.
	assertFlagged(t, flaggedLines(t, diff, map[string]string{}))
}

func TestComments_PerLanguage(t *testing.T) {
	cases := []struct {
		path, src string
		want      []int // lines whose comment text cites a line
	}{
		{"ci.yml", "# see dev-env.yml:189-191\nrun: echo \"# not.sh:1\" ${#arr} # tail ci.yml:3\nurl: http://x#frag.go:1\n", []int{1, 2}},
		{"s.sh", "echo '# a.sh:1'\n# b.sh:2\nn=$#c.sh:3\n", []int{2}},
		{"m.sql", "-- store.go:1\nSELECT 'it''s -- x.go:2'\nFROM t; /* y.go:3 */\nINSERT INTO t VALUES ('multi\n-- still.go:5 string');\n", []int{1, 3}},
		{"a.py", "x = \"# a.py:1\"\n# b.py:2\ns = \"\"\"\n# c.py:4\n\"\"\"\n", []int{2}},
		{"a.css", ".x { content: '/* a.css:1 */'; }\n/* b.css:2\n   c.css:3 */\n", []int{2, 3}},
		{"g_test.go", bt("var d = ¦\n// store.go:2 inside a raw string\n¦ // store.go:3\n"), []int{3}},
		{"n.ts", bt("const s = ¦a ${f(¦b¦)} c¦ // n.ts:1\nconst r = /['\"¦]/ // n.ts:2\nconst q = x / y // n.ts:3\nconst fx = ¦\n// store.go:5 inside a template\n¦ // n.ts:6\n"), []int{1, 2, 3, 6}},
	}
	for _, c := range cases {
		cm := Comments(c.path, c.src)
		var got []int
		for n := 1; n <= strings.Count(c.src, "\n")+1; n++ {
			if citation.MatchString(cm[n]) {
				got = append(got, n)
			}
		}
		if fmt.Sprint(got) != fmt.Sprint(c.want) {
			t.Errorf("%s: cited lines = %v, want %v (comments %v)", c.path, got, c.want, cm)
		}
	}
}

func TestCitation(t *testing.T) {
	for _, s := range []string{"store.go:115", "dev-env.yml:189-191", "mock.go:188,193", "(../lineitems.go:127-144)",
		"App.routePopstate.test.tsx:118", "db/seed.dev.sql:42", "docs/routing.md:87-93"} {
		if !citation.MatchString(s) {
			t.Errorf("citation missed %q", s)
		}
	}
	for _, s := range []string{"localhost:5432", "http://127.0.0.1:8080/x", "postgres://u:p@db:5432/app",
		"App.tsx#switchClient", "store.go", "at 12:30", "host.docker.internal:5432", "store.gold:12"} {
		if citation.MatchString(s) {
			t.Errorf("citation matched %q", s)
		}
	}
}

// Real diff shape: a deletion-only hunk, a deleted file, a count-less header,
// and an added line whose own text starts with "++".
func TestAddedLines(t *testing.T) {
	diff := `diff --git a/a.ts b/a.ts
--- a/a.ts
+++ b/a.ts
@@ -3,2 +2,0 @@ fn
-gone
-gone
@@ -9 +8,2 @@ fn
-old
+++ counter
+x
--- a/b.go
+++ /dev/null
@@ -1 +0,0 @@
-package b
--- a/c.go
+++ b/c.go
@@ -4,0 +5 @@
+// c
`
	got := AddedLines(diff)
	want := map[string]map[int]string{"a.ts": {8: "++ counter", 9: "x"}, "c.go": {5: "// c"}}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("AddedLines = %v, want %v", got, want)
	}
}

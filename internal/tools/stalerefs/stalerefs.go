// Package main implements stalerefs, a `go run`-able merge gate for the one
// blast-radius seam no compiler covers.
//
// Go's compiler and the TypeScript project references already fail a build when
// a renamed symbol leaves a caller behind. What neither can see is a *string*
// that one file produces and another file quotes: a Playwright assertion on
// user-visible copy, a getByTestId, a route path in a doc, an env var name in a
// CI workflow. Those references break silently and surface late — in a deploy
// gate, or not at all.
//
// So the question this tool asks is narrower than "what did the diff touch":
//
//	no producer emits this string any more, yet a consumer still quotes it
//
// producer  source that emits the string     (frontend/, internal/, cmd/, db/)
// consumer  something no compiler links back (e2e/, docs/, .github/)
// ignored   a unit test beside its own source — type-checked, and it runs on
//
//	the same push, so it cannot rot unnoticed
//
// Measured against the 40 most recent merged PRs before it was written: it
// fires on 3 of 577 commits, and on 1 of 32 PR ranges. Both breaks named in the
// QA logs are among the three — DEMO-01's `AUDIT LOGGED` toast (which cost a
// re-triggered deploy gate) and APPR-15's deleted invite test ids.
//
// The scan is deliberately paranoid about its own blindness. Three separate
// bugs during development each made it report a clean zero on the very case it
// was built to catch, and every one of them looked like good news. The known
// positives in stalerefs_test.go are pinned as fixtures for that reason: a
// future edit that blinds the scan fails a test instead of going quiet.
package main

import (
	"regexp"
	"strings"
	"unicode"
)

// consumerDirs hold references no compiler resolves back to their producer.
var consumerDirs = []string{"e2e/", "docs/", ".github/"}

// producerExts are the file kinds that can *emit* a string a consumer asserts on.
var producerExts = []string{".go", ".ts", ".tsx", ".js", ".jsx", ".sql"}

var (
	// A co-located unit test is neither producer nor consumer: it is type-checked
	// against its source and runs on the same push. Counting one as a producer
	// hides real findings — a retired string parked in a forbidden-copy list
	// under frontend/ read as "something still emits this" and suppressed the
	// DEMO-01 known positive.
	testish = regexp.MustCompile(`(_test\.go|\.test\.(ts|tsx|js)|\.spec\.(ts|tsx|js))$`)

	// Shapes with no identity: CSS values, selectors, bare punctuation.
	cssish = regexp.MustCompile(`^[\d\s.,%#>:()+-]*$|\d+(px|rem|em|vh|vw|%|ms|s)\b|oklch|rgba?\(|^[.#][\w-]+$`)
	// Shapes that are code, not a name or a sentence.
	codeFrag = regexp.MustCompile(`[{}();=\[\]]|\|\||=>|::`)

	envVar = regexp.MustCompile(`^[A-Z][A-Z0-9_]{5,}$`)
	kebab  = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)+$`)

	// A consumer line that merely *records* the removal is not a stale reference.
	absence = regexp.MustCompile(`(?i)not\.toContain|not\.toBe|toHaveCount\(0\)|queryAllByText|RETIRED|forbidden`)
	comment = regexp.MustCompile(`^\s*(//|#|\*|/\*|--)`)
)

// Hit is one `git grep` result: a surviving occurrence of a literal.
type Hit struct {
	Path string
	Line int
	Text string
}

// Finding is a literal that no producer emits any more, still quoted by a consumer.
type Finding struct {
	Literal string
	GoneOn  []string // producer files the diff removed it from
	Stale   []Hit    // surviving consumer references
}

// Role classifies a path. The consumer check runs first on purpose: an
// e2e/*.spec.ts matches testish too, and it is the single most important
// consumer in the tree.
func Role(path string) string {
	for _, d := range consumerDirs {
		if strings.HasPrefix(path, d) {
			return "consumer"
		}
	}
	if testish.MatchString(path) {
		return "ignore"
	}
	return "producer"
}

func isProducerFile(path string) bool {
	if Role(path) != "producer" {
		return false
	}
	for _, e := range producerExts {
		if strings.HasSuffix(path, e) {
			return true
		}
	}
	return false
}

// Literals pulls quoted strings out of one line. Written by hand rather than by
// regex because Go's RE2 has no lookbehind, and an escaped quote inside a
// string must not end it.
func Literals(line string) []string {
	var out []string
	r := []rune(line)
	for i := 0; i < len(r); i++ {
		q := r[i]
		if q != '\'' && q != '"' && q != '`' {
			continue
		}
		if i > 0 && r[i-1] == '\\' {
			continue
		}
		for j := i + 1; j < len(r); j++ {
			if r[j] == '\\' {
				j++
				continue
			}
			if r[j] == q {
				if s := string(r[i+1 : j]); len(s) >= 6 && len(s) <= 120 {
					out = append(out, s)
				}
				i = j
				break
			}
		}
	}
	return out
}

// Distinctive keeps only strings with enough identity to be worth tracking:
// user-visible copy, a test id, a route, an env var name. Without this the scan
// drowns in '100%' and '16px 20px' — 487 findings across 16 PRs when measured.
func Distinctive(s string) bool {
	if strings.TrimSpace(s) != s || cssish.MatchString(s) || codeFrag.MatchString(s) {
		return false
	}
	if envVar.MatchString(s) || kebab.MatchString(s) {
		return true
	}
	if strings.HasPrefix(s, "/") && len(s) > 6 {
		return true
	}
	if strings.HasPrefix(s, "http") || strings.HasPrefix(s, "./") || strings.HasPrefix(s, "../") {
		return false
	}
	words := strings.Fields(s)
	if len(words) < 2 {
		return false
	}
	var letters int
	for _, c := range s {
		if unicode.IsLetter(c) || unicode.IsSpace(c) {
			letters++
		}
	}
	return float64(letters)/float64(len([]rune(s))) > 0.8
}

// ParseDiff splits a `git diff --unified=0` into the literals each producer file
// stopped emitting and the ones it started emitting.
//
// Removals are keyed off the `--- a/` side, additions off `+++ b/`. Keying both
// off `+++ b/` looks equivalent until a file is DELETED, where git prints
// `+++ /dev/null`: the previous file's path stayed in place and every removal in
// the deleted file was blamed on it. That misattribution invented 14 findings in
// a single PR, all of them from a deleted e2e fixture that should never have
// been read as a producer at all.
func ParseDiff(diff string) (removed, added map[string]map[string]bool) {
	removed, added = map[string]map[string]bool{}, map[string]map[string]bool{}
	var oldPath, newPath string
	note := func(m map[string]map[string]bool, lit, path string) {
		if m[lit] == nil {
			m[lit] = map[string]bool{}
		}
		m[lit][path] = true
	}
	for _, line := range strings.Split(diff, "\n") {
		switch {
		case strings.HasPrefix(line, "--- "):
			oldPath = ""
			if strings.HasPrefix(line, "--- a/") {
				oldPath = line[len("--- a/"):]
			}
		case strings.HasPrefix(line, "+++ "):
			newPath = ""
			if strings.HasPrefix(line, "+++ b/") {
				newPath = line[len("+++ b/"):]
			}
		case strings.HasPrefix(line, "-"):
			if isProducerFile(oldPath) {
				for _, s := range Literals(line[1:]) {
					if Distinctive(s) {
						note(removed, s, oldPath)
					}
				}
			}
		case strings.HasPrefix(line, "+"):
			if isProducerFile(newPath) {
				for _, s := range Literals(line[1:]) {
					note(added, s, newPath)
				}
			}
		}
	}
	return removed, added
}

// WholeMatch reports whether lit occurs in text as a complete token rather than
// inside a longer identifier.
//
// `git grep -F` matches substrings, so renaming the test id
// `detail-submit-confirm` scored a hit on the surviving sibling
// `detail-submit-confirm-prompt` — the scan read that as "a producer still emits
// it" and stayed silent on a live break. Fixture tests could not catch this:
// they supply the grep results, so the substring never happens. Only running the
// whole gate against a real rename exposed it.
func WholeMatch(text, lit string) bool {
	ident := func(r byte) bool {
		return r == '-' || r == '_' ||
			(r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9')
	}
	for i := 0; ; {
		j := strings.Index(text[i:], lit)
		if j < 0 {
			return false
		}
		start := i + j
		end := start + len(lit)
		beforeOK := start == 0 || !ident(text[start-1])
		afterOK := end == len(text) || !ident(text[end])
		if beforeOK && afterOK {
			return true
		}
		i = start + 1
	}
}

// Allow suppresses known-good hits.
type Allow struct {
	// Literals the app assembles rather than stores, matched exactly.
	Literals map[string]bool
	// Files that exist to ARCHIVE retired copy — a forbidden-string registry.
	// A hit there is the file doing its job, the opposite of a stale reference.
	Files []string
}

func (a Allow) archived(path string) bool {
	for _, p := range a.Files {
		if strings.HasPrefix(path, p) {
			return true
		}
	}
	return false
}

// Scan returns the stale references in a diff. grep must return every surviving
// occurrence of a literal in the tree under test.
func Scan(diff string, grep func(string) []Hit, allow Allow) []Finding {
	removed, added := ParseDiff(diff)

	var out []Finding
	for lit, files := range removed {
		if allow.Literals[lit] {
			continue
		}
		// Gone from at least one file that did not re-add it. Scoped per file:
		// a global "was it re-added anywhere" test drops the case where a
		// reworded string is simultaneously added to a forbidden-copy list —
		// which is exactly how DEMO-01 shipped its broken assertion.
		var gone []string
		for f := range files {
			if !added[lit][f] {
				gone = append(gone, f)
			}
		}
		if len(gone) == 0 {
			continue
		}

		var stale []Hit
		producerSurvives := false
		for _, h := range grep(lit) {
			// A hit inside a longer identifier is a different symbol.
			if !WholeMatch(h.Text, lit) {
				continue
			}
			switch Role(h.Path) {
			case "producer":
				producerSurvives = true
			case "consumer":
				// A comment, an assert-it-is-absent, or a forbidden-copy
				// registry records the removal; none of them depends on the
				// string still being produced.
				if !comment.MatchString(h.Text) && !absence.MatchString(h.Text) && !allow.archived(h.Path) {
					stale = append(stale, h)
				}
			}
		}
		// A surviving producer means the string MOVED — a mock promoted into the
		// seed, a component split in two. The consumer is still correct.
		if producerSurvives || len(stale) == 0 {
			continue
		}
		sortStrings(gone)
		out = append(out, Finding{Literal: lit, GoneOn: gone, Stale: stale})
	}
	sortFindings(out)
	return out
}

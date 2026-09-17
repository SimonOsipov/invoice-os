// Package main implements citegate, a merge gate that fails a PR adding a code
// comment that cites source by line number (a file name, a colon, digits).
// Such a citation goes stale silently when code above the line moves. Only
// lines the PR adds are judged; existing citations are grandfathered.
//
// Known positives are pinned in citegate_test.go so a change that blinds the
// detector fails a test instead of reporting a clean zero.
package main

import (
	"regexp"
	"strconv"
	"strings"
)

// citation matches a file name with a source or config extension, then a line
// number or range. Hosts and ports never match: they carry no such extension.
var citation = regexp.MustCompile(`[\w-]\.(?:go|tsx?|jsx?|mjs|cjs|sql|ya?ml|sh|py|css|json|toml|html|md):\d+`)

// syntax is the comment and string grammar of one language, just deep enough to
// tell a comment from a string literal that happens to contain comment markers.
type syntax struct {
	line      []string // line comment markers
	block     bool     // /* ... */
	hashSpace bool     // `#` opens a comment only at line start or after whitespace
	quotes    string   // string quotes that end at the line end
	multi     string   // string quotes that may span lines, with no escapes
	template  bool     // JS template literal; ${} is read as part of the string
	regex     bool     // JS regex literal
	triple    bool     // Python triple-quoted strings
}

var (
	goSyntax   = syntax{line: []string{"//"}, block: true, quotes: `"'`, multi: "`"}
	jsSyntax   = syntax{line: []string{"//"}, block: true, quotes: `"'`, template: true, regex: true}
	cssSyntax  = syntax{block: true, quotes: `"'`}
	sqlSyntax  = syntax{line: []string{"--"}, block: true, quotes: `"`, multi: `'`}
	hashSyntax = syntax{line: []string{"#"}, hashSpace: true, quotes: `"'`}
	pySyntax   = syntax{line: []string{"#"}, quotes: `"'`, triple: true}
)

var syntaxByExt = map[string]syntax{
	".go": goSyntax, ".ts": jsSyntax, ".tsx": jsSyntax, ".js": jsSyntax, ".mjs": jsSyntax,
	".css": cssSyntax, ".sql": sqlSyntax, ".yml": hashSyntax, ".yaml": hashSyntax,
	".sh": hashSyntax, ".py": pySyntax,
}

// syntaxFor returns the grammar for a path the gate judges. Test files are in
// scope; docs are not.
func syntaxFor(path string) (syntax, bool) {
	if strings.HasPrefix(path, "docs/") {
		return syntax{}, false
	}
	i := strings.LastIndexByte(path, '.')
	if i < 0 {
		return syntax{}, false
	}
	sx, ok := syntaxByExt[path[i:]]
	return sx, ok
}

type lexState int

const (
	inCode lexState = iota
	inLineComment
	inBlockComment
	inString
	inTemplate
	inRegex
	inTriple
)

// Comments returns the comment text on each 1-based line of src. It lexes the
// whole file because a line inside a block comment or a multi-line string
// cannot be classified on its own.
func Comments(path, src string) map[int]string {
	sx, ok := syntaxFor(path)
	if !ok {
		return nil
	}
	out := map[int]string{}
	var buf strings.Builder
	state := inCode
	line := 1
	var quote byte
	var multiline, escapes, regexClass bool
	prev := byte('\n') // last non-blank code byte, for the regex-vs-division guess

	for i := 0; i < len(src); i++ {
		c := src[i]
		if c == '\n' {
			if buf.Len() > 0 {
				out[line] += buf.String()
				buf.Reset()
			}
			line++
			switch state {
			case inLineComment, inRegex:
				state = inCode
			case inString:
				if !multiline {
					state = inCode
				}
			}
			if state == inCode {
				prev = '\n'
			}
			continue
		}

		switch state {
		case inLineComment:
			buf.WriteByte(c)

		case inBlockComment:
			if strings.HasPrefix(src[i:], "*/") {
				state = inCode
				i++
				continue
			}
			buf.WriteByte(c)

		case inString:
			if escapes && c == '\\' && i+1 < len(src) && src[i+1] != '\n' {
				i++
			} else if c == quote {
				state = inCode
				prev = 'a'
			}

		case inTriple:
			if c == '\\' && i+1 < len(src) && src[i+1] != '\n' {
				i++
			} else if strings.HasPrefix(src[i:], strings.Repeat(string(quote), 3)) {
				state = inCode
				prev = 'a'
				i += 2
			}

		case inTemplate:
			switch {
			case c == '\\' && i+1 < len(src) && src[i+1] != '\n':
				i++
			case c == '`':
				state = inCode
				prev = 'a'
			}

		case inRegex:
			switch {
			case c == '\\' && i+1 < len(src) && src[i+1] != '\n':
				i++
			case c == '[':
				regexClass = true
			case c == ']':
				regexClass = false
			case c == '/' && !regexClass:
				state = inCode
				prev = 'a'
			}

		case inCode:
			if sx.block && strings.HasPrefix(src[i:], "/*") {
				state = inBlockComment
				i++
				continue
			}
			if marker := lineMarker(sx, src, i); marker != "" {
				state = inLineComment
				i += len(marker) - 1
				continue
			}
			switch {
			case sx.triple && (c == '"' || c == '\'') && strings.HasPrefix(src[i:], strings.Repeat(string(c), 3)):
				state, quote = inTriple, c
				i += 2
			case strings.IndexByte(sx.quotes, c) >= 0:
				state, quote, multiline, escapes = inString, c, false, true
			case strings.IndexByte(sx.multi, c) >= 0:
				state, quote, multiline, escapes = inString, c, true, false
			case sx.template && c == '`':
				state = inTemplate
			case sx.regex && c == '/' && strings.IndexByte("\n(,=:[!&|?{;+-*%~^", prev) >= 0:
				state, regexClass = inRegex, false
			}
			if c != ' ' && c != '\t' && c != '\r' && state == inCode {
				prev = c
			}
		}
	}
	if buf.Len() > 0 {
		out[line] += buf.String()
	}
	return out
}

func lineMarker(sx syntax, src string, i int) string {
	for _, m := range sx.line {
		if !strings.HasPrefix(src[i:], m) {
			continue
		}
		// `$#` and `${#arr}` in shell are not comments.
		if m == "#" && sx.hashSpace && i > 0 && src[i-1] != ' ' && src[i-1] != '\t' && src[i-1] != '\n' {
			continue
		}
		return m
	}
	return ""
}

// AddedLines maps each file in a `git diff --unified=0` to the new-side line
// numbers the diff adds, with each line's text.
func AddedLines(diff string) map[string]map[int]string {
	out := map[string]map[int]string{}
	var path string
	var newLeft, n int
	for _, l := range strings.Split(diff, "\n") {
		// Counting the hunk's added lines keeps an added "++ x" line from reading as a file header.
		if newLeft > 0 {
			if strings.HasPrefix(l, "+") {
				if path != "" {
					out[path][n] = l[1:]
				}
				n++
				newLeft--
			}
			continue
		}
		switch {
		case strings.HasPrefix(l, "+++ "):
			path = ""
			if p, ok := strings.CutPrefix(l, "+++ b/"); ok {
				path = p
				if out[path] == nil {
					out[path] = map[int]string{}
				}
			}
		case strings.HasPrefix(l, "@@ "):
			n, newLeft = newRange(l)
		}
	}
	return out
}

// newRange reads `+c,d` from a hunk header; a missing count is 1.
func newRange(header string) (start, count int) {
	for _, f := range strings.Fields(header) {
		if len(f) < 2 || f[0] != '+' {
			continue
		}
		s, c, found := strings.Cut(f[1:], ",")
		start, _ = strconv.Atoi(s)
		count = 1
		if found {
			count, _ = strconv.Atoi(c)
		}
		return start, count
	}
	return 0, 0
}

// Finding is an added comment line that cites code by line number.
type Finding struct {
	Path string
	Line int
	Text string
}

// Scan judges the added lines of diff. show returns a file's full content at
// the head of the diff.
func Scan(diff string, show func(path string) (string, error)) ([]Finding, error) {
	added := AddedLines(diff)
	paths := make([]string, 0, len(added))
	for p := range added {
		paths = append(paths, p)
	}
	sortStrings(paths)

	var out []Finding
	for _, p := range paths {
		if _, ok := syntaxFor(p); !ok || len(added[p]) == 0 {
			continue
		}
		src, err := show(p)
		if err != nil {
			return nil, err
		}
		comments := Comments(p, src)
		lines := make([]int, 0, len(added[p]))
		for n := range added[p] {
			lines = append(lines, n)
		}
		sortInts(lines)
		for _, n := range lines {
			if citation.MatchString(comments[n]) {
				out = append(out, Finding{Path: p, Line: n, Text: added[p][n]})
			}
		}
	}
	return out, nil
}

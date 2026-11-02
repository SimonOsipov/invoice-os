package main

import (
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
)

// Trigger is one statement that deletes or renames schema. n is the number of
// trailing name parts a mark must match: 1 for a table, function or type, 2 for a column.
type Trigger struct {
	Statement string
	Object    string
	kind      string
	parts     []string
	fold      []string
	n         int
}

// ident is a name part as written and as Postgres folds it: quoted keeps case, bare goes lower.
type ident struct{ text, fold string }

func texts(ids []ident) []string {
	out := make([]string, len(ids))
	for i, id := range ids {
		out[i] = id.text
	}
	return out
}

type Mark struct {
	Object string
	Reason string
}

type Result struct {
	File     string
	Triggers []Trigger
	Marks    []Mark
}

// Scan reads the Up section of one migration file.
func Scan(file, sql string) (Result, error) {
	up, err := upSection(sql)
	if err != nil {
		return Result{}, fmt.Errorf("%s: %w", file, err)
	}
	segs, comments, err := lex(up)
	if err != nil {
		return Result{}, fmt.Errorf("%s: %w", file, err)
	}
	r := Result{File: file}
	for _, s := range segs {
		r.Triggers = append(r.Triggers, findTriggers(s)...)
	}
	for _, c := range comments {
		if m, ok := parseMark(c); ok {
			r.Marks = append(r.Marks, m)
		}
	}
	return r, nil
}

// gooseAnnotation returns the lower-cased "-- +goose <cmd>" command of a line, read as goose does.
func gooseAnnotation(line string) string {
	if !strings.HasPrefix(strings.TrimSpace(line), "--") || !strings.Contains(line, "+goose") {
		return ""
	}
	return strings.ToLower(strings.TrimSpace(strings.Replace(strings.ReplaceAll(line, "--", ""), "+goose", "", 1)))
}

func upSection(sql string) (string, error) {
	lines := strings.Split(sql, "\n")
	start := slices.IndexFunc(lines, func(l string) bool { return gooseAnnotation(l) == "up" })
	if start < 0 {
		return "", errors.New(`no "-- +goose Up" line`)
	}
	lines = lines[start+1:]
	if end := slices.IndexFunc(lines, func(l string) bool { return gooseAnnotation(l) == "down" }); end >= 0 {
		lines = lines[:end]
	}
	return strings.Join(lines, "\n"), nil
}

func isIdent(c byte) bool {
	return c == '_' || c == '$' || c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= 0x80
}

// dollarTag returns the opening tag ("$$" or "$name$") at the start of s, or "".
func dollarTag(s string) string {
	j := 1
	for j < len(s) && isIdent(s[j]) && s[j] != '$' && (j > 1 || s[j] < '0' || s[j] > '9') {
		j++
	}
	if j < len(s) && s[j] == '$' {
		return s[:j+1]
	}
	return ""
}

// quoteEnd returns the index of the quote that closes the span opened at src[i],
// or -1 when it is unterminated. A quote after E or e is an E-string: backslash escapes.
func quoteEnd(src string, i int) int {
	c := src[i]
	esc := c == '\'' && i > 0 && src[i-1]|0x20 == 'e' && (i < 2 || !isIdent(src[i-2]))
	for j := i + 1; j < len(src); j++ {
		if esc && src[j] == '\\' {
			j++
			continue
		}
		if src[j] == c {
			if j+1 < len(src) && src[j+1] == c {
				j++
				continue
			}
			return j
		}
	}
	return -1
}

// dollarEnd returns the index after the dollar-quoted span opened at src[i],
// or -1 when it is unterminated. The caller has checked dollarTag.
func dollarEnd(src string, i int) int {
	tag := dollarTag(src[i:])
	end := strings.Index(src[i+len(tag):], tag)
	if end < 0 {
		return -1
	}
	return i + len(tag) + end + len(tag)
}

// skipSpan returns the index after the comment, quote or dollar-quoted span opened at
// src[i] and what it is; what is "" when src[i] opens none. end is -1 when unterminated.
func skipSpan(src string, i int) (end int, what string) {
	switch c := src[i]; {
	case c == '-' && strings.HasPrefix(src[i:], "--"):
		if n := strings.IndexAny(src[i:], "\r\n"); n >= 0 {
			return i + n, "line comment"
		}
		return len(src), "line comment"
	case c == '/' && strings.HasPrefix(src[i:], "/*"):
		depth, j := 1, i+2
		for depth > 0 {
			switch {
			case strings.HasPrefix(src[j:], "/*"):
				depth++
				j += 2
			case strings.HasPrefix(src[j:], "*/"):
				depth--
				j += 2
			case j >= len(src):
				return -1, "block comment"
			default:
				j++
			}
		}
		return j, "block comment"
	case c == '\'' || c == '"':
		if j := quoteEnd(src, i); j >= 0 {
			return j + 1, "quote"
		}
		return -1, "quote"
	case c == '$' && (i == 0 || !isIdent(src[i-1])) && dollarTag(src[i:]) != "":
		return dollarEnd(src, i), "dollar quote"
	}
	return i, ""
}

// lex splits Up text into code segments (comments removed, quoted and dollar-quoted
// spans kept verbatim) and the text of its "--" comments.
func lex(src string) (segs, comments []string, err error) {
	var cur strings.Builder
	flush := func() {
		if strings.TrimSpace(cur.String()) != "" {
			segs = append(segs, cur.String())
		}
		cur.Reset()
	}
	for i := 0; i < len(src); {
		if end, what := skipSpan(src, i); what != "" {
			if end < 0 {
				return nil, nil, errors.New("Up section ends inside an unterminated " + what)
			}
			switch src[i] {
			case '-':
				comments = append(comments, src[i+2:end])
				cur.WriteByte(' ')
			case '/':
				cur.WriteByte(' ')
			default:
				cur.WriteString(src[i:end])
			}
			i = end
		} else if src[i] == ';' {
			flush()
			i++
		} else {
			cur.WriteByte(src[i])
			i++
		}
	}
	flush()
	return segs, comments, nil
}

// cursor reads keywords and names from SQL text.
type cursor struct {
	s string
	i int
}

// ws skips the whitespace Postgres accepts, and comments (a dollar-quoted body keeps its own).
func (c *cursor) ws() {
	for c.i < len(c.s) {
		if strings.IndexByte(" \t\r\n\f\v", c.s[c.i]) >= 0 {
			c.i++
		} else if end, what := skipSpan(c.s, c.i); strings.HasSuffix(what, "comment") {
			if c.i = end; end < 0 {
				c.i = len(c.s)
			}
		} else {
			return
		}
	}
}

// kw consumes the keywords in order, case-insensitively, or consumes nothing.
func (c *cursor) kw(words ...string) bool {
	save := c.i
	for _, w := range words {
		c.ws()
		j := c.i
		for j < len(c.s) && isIdent(c.s[j]) {
			j++
		}
		if !strings.EqualFold(c.s[c.i:j], w) {
			c.i = save
			return false
		}
		c.i = j
	}
	return true
}

func (c *cursor) peek() byte {
	c.ws()
	if c.i < len(c.s) {
		return c.s[c.i]
	}
	return 0
}

// part reads one identifier or "quoted identifier" (with "" escapes), quotes removed.
func (c *cursor) part() ident {
	if c.i < len(c.s) && c.s[c.i] == '"' {
		end := quoteEnd(c.s, c.i)
		if end < 0 {
			return ident{}
		}
		p := strings.ReplaceAll(c.s[c.i+1:end], `""`, `"`)
		c.i = end + 1
		return ident{p, p}
	}
	j := c.i
	for j < len(c.s) && isIdent(c.s[j]) {
		j++
	}
	p := c.s[c.i:j]
	c.i = j
	return ident{p, strings.ToLower(p)}
}

// name reads a dotted name; nil when the text holds none (for example %I).
func (c *cursor) name() []ident {
	c.ws()
	var parts []ident
	for {
		p := c.part()
		if p.text == "" {
			return nil
		}
		parts = append(parts, p)
		if c.peek() == '.' {
			c.i++
			c.ws()
			continue
		}
		return parts
	}
}

// args reads a balanced "(...)" and returns the text inside.
func (c *cursor) args() string {
	depth, start := 0, c.i+1
	for ; c.i < len(c.s); c.i++ {
		switch c.s[c.i] {
		case '(':
			depth++
		case ')':
			if depth--; depth == 0 {
				c.i++
				return c.s[start : c.i-1]
			}
		}
	}
	return c.s[start:]
}

var (
	triggerStart = regexp.MustCompile(`(?i)\b(?:DROP|ALTER)\b`)
	dropWord     = regexp.MustCompile(`(?i)\b(?:DROP|RENAME)\b`)
)

// dotted joins name parts with dots, quoting a part that holds a dot or a quote so
// the text reads back as the same parts in a mark.
func dotted(parts []string) string {
	q := make([]string, len(parts))
	for i, p := range parts {
		q[i] = p
		if strings.ContainsAny(p, `."`) {
			q[i] = `"` + strings.ReplaceAll(p, `"`, `""`) + `"`
		}
	}
	return strings.Join(q, ".")
}

func newTrigger(stmt, kind string, ids []ident, n int) Trigger {
	low, fold := make([]string, len(ids)), make([]string, len(ids))
	for i, id := range ids {
		low[i], fold[i] = strings.ToLower(id.text), id.fold
	}
	return Trigger{Statement: stmt, Object: dotted(texts(ids)), kind: kind, parts: low, fold: fold, n: n}
}

// key names the object a mark claims: its kind and its name as Postgres folds it.
func (t Trigger) key() string { return t.kind + " " + strings.Join(t.fold, ".") }

func findTriggers(seg string) []Trigger {
	var out []Trigger
	for _, m := range triggerStart.FindAllStringIndex(seg, -1) {
		c := &cursor{s: seg, i: m[1]}
		if strings.EqualFold(seg[m[0]:m[1]], "DROP") {
			kind := ""
			for _, k := range []string{"TABLE", "FUNCTION", "TYPE"} {
				if c.kw(k) {
					kind = k
					break
				}
			}
			if kind == "" {
				continue
			}
			c.kw("IF", "EXISTS")
			for {
				parts := c.name()
				if parts == nil {
					break
				}
				stmt := "DROP " + kind + " " + dotted(texts(parts))
				if kind == "FUNCTION" && c.peek() == '(' {
					stmt += "(" + strings.TrimSpace(c.args()) + ")"
				}
				out = append(out, newTrigger(stmt, strings.ToLower(kind), parts, 1))
				if c.peek() != ',' {
					break
				}
				c.i++
			}
			continue
		}
		if !c.kw("TABLE") {
			continue
		}
		c.kw("IF", "EXISTS")
		c.kw("ONLY")
		table := c.name()
		if table == nil {
			continue
		}
		if c.peek() == '*' {
			c.i++
		}
		for _, action := range splitActions(seg[c.i:]) {
			out = append(out, actionTrigger(table, action)...)
		}
	}
	return out
}

// splitActions cuts the text after an ALTER TABLE name at top-level commas,
// up to the first top-level ";" or the end of the segment.
func splitActions(s string) []string {
	var out []string
	depth, start := 0, 0
	for i := 0; i < len(s); i++ {
		if end, what := skipSpan(s, i); what != "" {
			if end < 0 {
				break
			}
			i = end - 1
			continue
		}
		switch s[i] {
		case '(':
			depth++
		case ')':
			if depth--; depth < 0 {
				return append(out, s[start:i])
			}
		case ',':
			if depth == 0 {
				out = append(out, s[start:i])
				start = i + 1
			}
		case ';':
			return append(out, s[start:i])
		}
	}
	return append(out, s[start:])
}

// blank replaces comments, quoted and dollar-quoted spans with spaces.
func blank(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); {
		if end, what := skipSpan(s, i); what != "" && end >= 0 {
			b.WriteByte(' ')
			i = end
		} else {
			b.WriteByte(s[i])
			i++
		}
	}
	return b.String()
}

func actionTrigger(table []ident, action string) []Trigger {
	c := &cursor{s: action}
	t := dotted(texts(table))
	col := func(name ident) []ident { return append(slices.Clone(table), name) }
	switch {
	case c.kw("DROP"):
		if c.kw("CONSTRAINT") {
			return nil
		}
		c.kw("COLUMN")
		c.kw("IF", "EXISTS")
		if name := c.name(); len(name) == 1 {
			return []Trigger{newTrigger("ALTER TABLE "+t+" DROP COLUMN "+name[0].text, "column", col(name[0]), 2)}
		}
	case c.kw("RENAME"):
		if c.kw("CONSTRAINT") {
			return nil
		}
		if c.kw("TO") {
			if u := c.name(); u != nil {
				return []Trigger{newTrigger("ALTER TABLE "+t+" RENAME TO "+strings.Join(texts(u), "."), "table", table, 1)}
			}
			return nil
		}
		c.kw("COLUMN")
		if a := c.name(); len(a) == 1 && c.kw("TO") {
			if b := c.name(); len(b) == 1 {
				return []Trigger{newTrigger("ALTER TABLE "+t+" RENAME COLUMN "+a[0].text+" TO "+b[0].text, "column", col(a[0]), 2)}
			}
		}
	case c.kw("ALTER"):
		return nil
	}
	// Fail closed: a DROP or RENAME word the parser cannot place still needs a mark on the table.
	if text := strings.Join(strings.Fields(blank(action)), " "); dropWord.MatchString(text) {
		return []Trigger{newTrigger("ALTER TABLE "+t+" "+text, "table", table, 1)}
	}
	return nil
}

func parseMark(comment string) (Mark, bool) {
	rest, ok := strings.CutPrefix(strings.TrimSpace(comment), "contract-step")
	if !ok || rest == "" || strings.TrimSpace(rest) == rest {
		return Mark{}, false
	}
	obj, reason, _ := strings.Cut(rest, ":")
	return Mark{Object: strings.TrimSpace(obj), Reason: strings.TrimSpace(reason)}, true
}

// splitName splits a mark object on dots outside quotes, lower-cased, quotes removed.
func splitName(s string) []string {
	var parts []string
	var b strings.Builder
	quoted := false
	for i := 0; i < len(s); i++ {
		switch {
		case s[i] == '"' && quoted && i+1 < len(s) && s[i+1] == '"':
			b.WriteByte('"')
			i++
		case s[i] == '"':
			quoted = !quoted
		case s[i] == '.' && !quoted:
			parts = append(parts, strings.ToLower(strings.TrimSpace(b.String())))
			b.Reset()
		default:
			b.WriteByte(s[i])
		}
	}
	return append(parts, strings.ToLower(strings.TrimSpace(b.String())))
}

func (m Mark) matches(t Trigger) bool {
	mp := splitName(m.Object)
	if len(mp) != t.n && len(mp) != t.n+1 {
		return false
	}
	for k := 1; k <= t.n; k++ {
		if mp[len(mp)-k] != t.parts[len(t.parts)-k] {
			return false
		}
	}
	if len(mp) == t.n+1 && len(t.parts) == t.n+1 {
		return mp[0] == t.parts[0]
	}
	return true
}

// Evaluate turns scan results into output lines and says whether the gate fails.
func Evaluate(results []Result) (lines []string, failed bool) {
	rs := slices.SortedStableFunc(slices.Values(results), func(a, b Result) int { return strings.Compare(a.File, b.File) })
	var passed []string
	for _, r := range rs {
		var unmarked, noReason, orphan []string
		used := make([]bool, len(r.Marks))
		released := map[string]bool{}
		for _, t := range r.Triggers {
			if released[t.key()] {
				continue
			}
			claimed := false
			for k, m := range r.Marks {
				if !used[k] && m.matches(t) {
					used[k], released[t.key()], claimed = true, true, true
					break
				}
			}
			if !claimed {
				unmarked = append(unmarked, fmt.Sprintf("%s: %s deletes or renames schema that a running revision may read. Write it in a later deploy, or add to the Up section: -- contract-step %s: <why no running revision reads it>", r.File, t.Statement, t.Object))
			}
		}
		for k, m := range r.Marks {
			obj := m.Object
			if obj == "" {
				obj = "(empty)"
			}
			if m.Reason == "" {
				noReason = append(noReason, fmt.Sprintf("%s: the mark for %s has no reason. Write why no running revision reads it.", r.File, obj))
			}
			if !used[k] {
				orphan = append(orphan, fmt.Sprintf("%s: the mark for %s names no statement in this Up section. Remove it or correct the name.", r.File, obj))
			} else if m.Reason != "" {
				passed = append(passed, fmt.Sprintf("%s: %s: %s", r.File, m.Object, m.Reason))
			}
		}
		lines = append(lines, unmarked...)
		lines = append(lines, noReason...)
		lines = append(lines, orphan...)
	}
	if len(lines) > 0 {
		return lines, true
	}
	if len(passed) > 0 {
		return append([]string{"Deliberate schema changes:"}, passed...), false
	}
	return []string{"No deliberate schema changes."}, false
}

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
	parts     []string
	n         int
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

func upSection(sql string) (string, error) {
	lines := strings.Split(sql, "\n")
	start := slices.IndexFunc(lines, func(l string) bool { return strings.HasPrefix(strings.TrimSpace(l), "-- +goose Up") })
	if start < 0 {
		return "", errors.New(`no "-- +goose Up" line`)
	}
	lines = lines[start+1:]
	if end := slices.IndexFunc(lines, func(l string) bool { return strings.HasPrefix(strings.TrimSpace(l), "-- +goose Down") }); end >= 0 {
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
		c := src[i]
		switch {
		case c == '-' && strings.HasPrefix(src[i:], "--"):
			n := strings.IndexByte(src[i:], '\n')
			if n < 0 {
				n = len(src) - i
			}
			comments = append(comments, src[i+2:i+n])
			cur.WriteByte(' ')
			i += n
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
					return nil, nil, errors.New("Up section ends inside an unterminated block comment")
				default:
					j++
				}
			}
			cur.WriteByte(' ')
			i = j
		case c == '\'' || c == '"':
			esc := c == '\'' && i > 0 && src[i-1]|0x20 == 'e' && (i < 2 || !isIdent(src[i-2]))
			j := i + 1
			for {
				if j >= len(src) {
					return nil, nil, errors.New("Up section ends inside an unterminated quote")
				}
				if esc && src[j] == '\\' {
					j += 2
					continue
				}
				if src[j] == c {
					if j+1 < len(src) && src[j+1] == c {
						j += 2
						continue
					}
					break
				}
				j++
			}
			cur.WriteString(src[i : j+1])
			i = j + 1
		case c == '$' && (i == 0 || !isIdent(src[i-1])) && dollarTag(src[i:]) != "":
			tag := dollarTag(src[i:])
			end := strings.Index(src[i+len(tag):], tag)
			if end < 0 {
				return nil, nil, errors.New("Up section ends inside an unterminated dollar quote")
			}
			j := i + len(tag) + end + len(tag)
			cur.WriteString(src[i:j])
			i = j
		case c == ';':
			flush()
			i++
		default:
			cur.WriteByte(c)
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

func (c *cursor) ws() {
	for c.i < len(c.s) && strings.IndexByte(" \t\r\n", c.s[c.i]) >= 0 {
		c.i++
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

// part reads one identifier or "quoted identifier", quotes removed.
func (c *cursor) part() string {
	if c.i < len(c.s) && c.s[c.i] == '"' {
		end := strings.IndexByte(c.s[c.i+1:], '"')
		if end < 0 {
			return ""
		}
		p := c.s[c.i+1 : c.i+1+end]
		c.i += end + 2
		return p
	}
	j := c.i
	for j < len(c.s) && isIdent(c.s[j]) {
		j++
	}
	p := c.s[c.i:j]
	c.i = j
	return p
}

// name reads a dotted name; nil when the text holds none (for example %I).
func (c *cursor) name() []string {
	c.ws()
	var parts []string
	for {
		p := c.part()
		if p == "" {
			return nil
		}
		parts = append(parts, p)
		if c.i < len(c.s) && c.s[c.i] == '.' {
			c.i++
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

var triggerStart = regexp.MustCompile(`(?i)\b(?:(DROP)\s+(TABLE|FUNCTION|TYPE)|(ALTER)\s+TABLE)\b`)

func newTrigger(stmt string, parts []string, n int) Trigger {
	low := make([]string, len(parts))
	for i, p := range parts {
		low[i] = strings.ToLower(p)
	}
	return Trigger{Statement: stmt, Object: strings.Join(parts, "."), parts: low, n: n}
}

func findTriggers(seg string) []Trigger {
	var out []Trigger
	for _, m := range triggerStart.FindAllStringSubmatchIndex(seg, -1) {
		c := &cursor{s: seg, i: m[1]}
		if m[2] >= 0 {
			kind := strings.ToUpper(seg[m[4]:m[5]])
			c.kw("IF", "EXISTS")
			for {
				parts := c.name()
				if parts == nil {
					break
				}
				stmt := "DROP " + kind + " " + strings.Join(parts, ".")
				if kind == "FUNCTION" && c.peek() == '(' {
					stmt += "(" + strings.TrimSpace(c.args()) + ")"
				}
				out = append(out, newTrigger(stmt, parts, 1))
				if c.peek() != ',' {
					break
				}
				c.i++
			}
			continue
		}
		c.kw("IF", "EXISTS")
		c.kw("ONLY")
		table := c.name()
		if table == nil {
			continue
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
scan:
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '\'', '"':
			end := strings.IndexByte(s[i+1:], s[i])
			if end < 0 {
				break scan
			}
			i += end + 1
		case '(':
			depth++
		case ')':
			if depth--; depth < 0 {
				out = append(out, s[start:i])
				return out
			}
		case ',':
			if depth == 0 {
				out = append(out, s[start:i])
				start = i + 1
			}
		case ';':
			out = append(out, s[start:i])
			return out
		}
	}
	return append(out, s[start:])
}

func actionTrigger(table []string, action string) []Trigger {
	c := &cursor{s: action}
	t := strings.Join(table, ".")
	col := func(name string) []string { return append(slices.Clone(table), name) }
	switch {
	case c.kw("DROP"):
		if c.kw("CONSTRAINT") {
			return nil
		}
		c.kw("COLUMN")
		c.kw("IF", "EXISTS")
		if name := c.name(); len(name) == 1 {
			return []Trigger{newTrigger("ALTER TABLE "+t+" DROP COLUMN "+name[0], col(name[0]), 2)}
		}
	case c.kw("RENAME"):
		if c.kw("CONSTRAINT") {
			return nil
		}
		if c.kw("TO") {
			if u := c.name(); u != nil {
				return []Trigger{newTrigger("ALTER TABLE "+t+" RENAME TO "+strings.Join(u, "."), table, 1)}
			}
			return nil
		}
		c.kw("COLUMN")
		if a := c.name(); len(a) == 1 && c.kw("TO") {
			if b := c.name(); len(b) == 1 {
				return []Trigger{newTrigger("ALTER TABLE "+t+" RENAME COLUMN "+a[0]+" TO "+b[0], col(a[0]), 2)}
			}
		}
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
		for _, t := range r.Triggers {
			hit := false
			for k, m := range r.Marks {
				if m.matches(t) {
					used[k], hit = true, true
				}
			}
			if !hit {
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

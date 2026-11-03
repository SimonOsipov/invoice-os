package main

import (
	"fmt"
	"path"
	"sort"
	"strings"
)

// MarksFile holds the deliberate-deletion marks. goose and go:embed ignore a non-.sql file.
const MarksFile = Dir + "/deleted.txt"

// Mark is one line of MarksFile: a deleted file's bare name and why the deletion is safe.
type Mark struct {
	Name, Reason string
}

// isMigration reports a migrations/<name>.sql direct child.
func isMigration(f string) bool { return path.Dir(f) == Dir && strings.HasSuffix(f, ".sql") }

// DeletedOnBase keeps the migrations/<name>.sql direct children of deleted that onBase holds.
func DeletedOnBase(deleted, onBase []string) []string {
	have := toSet(onBase)
	var out []string
	for _, f := range deleted {
		if isMigration(f) && have[f] {
			out = append(out, f)
		}
	}
	sort.Strings(out)
	return out
}

// NewMarks returns the marks in head that base does not hold, compared after ParseMarks trimming.
// Each base mark cancels one equal head mark; an edited line is a new mark.
func NewMarks(head, base []string) []Mark {
	have := map[Mark]int{}
	for _, m := range ParseMarks(base) {
		have[m]++
	}
	var out []Mark
	for _, m := range ParseMarks(head) {
		if have[m] > 0 {
			have[m]--
			continue
		}
		out = append(out, m)
	}
	return out
}

// ParseMarks reads one mark per non-blank line. The first colon splits name from reason.
func ParseMarks(lines []string) []Mark {
	var out []Mark
	for _, l := range lines {
		if strings.TrimSpace(l) == "" {
			continue
		}
		name, reason, _ := strings.Cut(l, ":")
		out = append(out, Mark{Name: strings.TrimSpace(name), Reason: strings.TrimSpace(reason)})
	}
	return out
}

// EvaluateDeleted matches marks to deleted files, one mark per file by bare name.
// released lists "<path>: <reason>" for each file a mark with a reason releases.
func EvaluateDeleted(deleted []string, marks []Mark) (failures, released []string) {
	used := make([]bool, len(marks))
	var unmarked, noReason []string
	for _, d := range deleted {
		name := path.Base(d)
		i := claim(marks, used, name)
		switch {
		case i < 0:
			unmarked = append(unmarked, fmt.Sprintf("::error file=%s::%s was deleted. It is on the base branch, so deleting it breaks the Down chain and disagrees with any database that applied it. Restore it, or add to %s: %s: <why the deletion is safe>", d, d, MarksFile, name))
		case marks[i].Reason == "":
			noReason = append(noReason, fmt.Sprintf("::error file=%s::%s: the mark for %s has no reason. Write why the deletion is safe.", MarksFile, MarksFile, name))
		default:
			released = append(released, d+": "+marks[i].Reason)
		}
	}
	var orphans []string
	for i, m := range marks {
		if !used[i] {
			orphans = append(orphans, fmt.Sprintf("::error file=%s::%s: the mark for %s names no deleted migration in this change. Remove it or correct the name.", MarksFile, MarksFile, m.Name))
		}
	}
	failures = append(append(unmarked, noReason...), orphans...)
	return failures, released
}

// claim marks and returns the first unused mark named name, or -1.
func claim(marks []Mark, used []bool, name string) int {
	for i, m := range marks {
		if !used[i] && m.Name == name {
			used[i] = true
			return i
		}
	}
	return -1
}

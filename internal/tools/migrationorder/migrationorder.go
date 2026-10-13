// Command migrationorder fails a change that adds a goose migration whose
// version sorts at or before the newest migration already on the base branch.
//
// goose refuses to apply a migration numbered below the database's current
// version. A branch cut before another branch's migration merged is the usual
// cause: its timestamp is older than main's newest.
//
// While -head is outside -main (default origin/main), a file already on -main is skipped: main
// has applied it, so a branch that merged main in did not add it. An exact rename of a file
// that -base holds and -main does not is checked against -main's newest only: production has
// not applied the old name. Exact means equal blob ids, not git's R100.
//
// Usage:
//
//	go run ./internal/tools/migrationorder -base <rev> [-head HEAD] [-main origin/main]
package main

import (
	"fmt"
	"path"
	"sort"
	"strconv"
	"strings"
)

// Dir is the one goose migrations directory; db/ holds bootstrap and seed SQL, not migrations.
const Dir = "migrations"

// SkipOnMain drops every added file whose path is already in onMain.
func SkipOnMain(added, onMain []string) []string {
	have := make(map[string]bool, len(onMain))
	for _, f := range onMain {
		have[f] = true
	}
	var kept []string
	for _, f := range added {
		if !have[f] {
			kept = append(kept, f)
		}
	}
	return kept
}

// Change is an added or renamed migration. Old is empty for an add.
// Exact marks a rename whose old and new blob ids are equal.
type Change struct {
	Old, New string
	Exact    bool
}

// Split sorts changes into files to check against base's newest and exact renames to
// check against main's newest. Only an exact rename of a path on base and not on main
// is exempt, and only when exempt is true.
func Split(changes []Change, onBase, onMain []string, exempt bool) (vsBase, vsMain []string) {
	base := toSet(onBase)
	main := toSet(onMain)
	var added []string
	for _, c := range changes {
		added = append(added, c.New)
	}
	kept := toSet(SkipOnMain(added, onMain))
	for _, c := range changes {
		switch {
		case !kept[c.New]:
		case exempt && c.Exact && c.Old != "" && base[c.Old] && !main[c.Old]:
			vsMain = append(vsMain, c.New)
		default:
			vsBase = append(vsBase, c.New)
		}
	}
	return vsBase, vsMain
}

func toSet(s []string) map[string]bool {
	m := make(map[string]bool, len(s))
	for _, f := range s {
		m[f] = true
	}
	return m
}

// Violation is an added migration that does not sort after base's newest.
type Violation struct {
	File       string
	Version    int64
	NewestFile string
	Newest     int64
}

// Version reads a migration's version the way goose does: the integer before the
// first underscore. Non-SQL files report ok=false.
func Version(name string) (v int64, ok bool, err error) {
	base := path.Base(name)
	if !strings.HasSuffix(base, ".sql") {
		return 0, false, nil
	}
	num, _, found := strings.Cut(base, "_")
	if !found {
		return 0, false, fmt.Errorf("%s: no version prefix before '_'", name)
	}
	v, err = strconv.ParseInt(num, 10, 64)
	if err != nil || v < 1 {
		return 0, false, fmt.Errorf("%s: version prefix %q is not a positive integer", name, num)
	}
	return v, true, nil
}

// Check returns every added migration whose version is <= the newest version on base.
func Check(added, onBase []string) ([]Violation, error) {
	var newest int64
	var newestFile string
	for _, f := range onBase {
		v, ok, err := Version(f)
		if err != nil {
			return nil, err
		}
		if ok && v > newest {
			newest, newestFile = v, f
		}
	}

	var out []Violation
	for _, f := range added {
		v, ok, err := Version(f)
		if err != nil {
			return nil, err
		}
		if ok && newestFile != "" && v <= newest {
			out = append(out, Violation{File: f, Version: v, NewestFile: newestFile, Newest: newest})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Version < out[j].Version })
	return out, nil
}

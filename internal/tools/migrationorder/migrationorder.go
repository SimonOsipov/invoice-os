// Command migrationorder fails a change that adds a goose migration whose
// version sorts at or before the newest migration already on the base branch.
//
// goose refuses to apply a migration numbered below the database's current
// version, and per-PR environments migrate a fresh database, so only the
// production database can see the break. A branch cut before another branch's
// migration merged is the usual cause: its timestamp is older than main's newest.
//
// Usage:
//
//	go run ./internal/tools/migrationorder -base <rev> [-head HEAD]
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

package main

import (
	"fmt"
	"os"
	"strconv"
)

// main is a thin CLI wrapper (M4-21-08): read the PR number from argv[1]
// and the candidate environment names (as returned by resolve-env's
// GraphQL query, M4-21-09) from the remaining args, call Match, and print
// the resolved name or exit non-zero with the error. All matching logic
// lives in Match (match.go) so all of it is covered by match_test.go;
// the workflow invokes this as `go run ./tools/prenv <pr-number>
// <name>...` (AC-4: resolve-env contains no matching logic of its own).
func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: prenv <pr-number> <name>...")
		os.Exit(2)
	}
	prNumber, err := strconv.Atoi(os.Args[1])
	if err != nil {
		fmt.Fprintf(os.Stderr, "prenv: invalid PR number %q: %v\n", os.Args[1], err)
		os.Exit(2)
	}
	name, err := Match(os.Args[2:], prNumber)
	if err != nil {
		fmt.Fprintf(os.Stderr, "prenv: %v\n", err)
		os.Exit(1)
	}
	fmt.Println(name)
}

// Package main implements prenv, a `go run`-able CLI that owns the
// Railway per-PR environment naming convention: deriving an environment
// name from a PR number, and the inverse parse back from a name to a PR
// number (M4-23-01, task-154).
//
// The naming convention used to be Railway's to choose -- Railway named a
// PR environment non-deterministically as either `pr-<N>` or
// `<repo>-pr-<N>`, so the earlier version of this tool (M4-21-08) polled
// for the environment list and matched a name out of it. Under M4-23's
// [create-not-resolve] decision the workflow creates the environment
// itself, so its name is a known input rather than an unknown to
// disambiguate: the matcher is gone and Name is now the single place in
// the repo that constructs an environment name.
//
// Name and ParsePR are carved out as pure functions -- no network, no
// environment reads, no globals -- so the anchoring hazard is testable
// head-on: a substring match would attribute environment `pr-15` to PR 1,
// and on the sweeper path that does not deploy to the wrong environment,
// it DELETES one. A live Railway run only ever sees whatever environments
// happen to exist and can never deliberately exercise that ambiguity.
//
// The CLI surface exists because the callers are GitHub Actions YAML,
// which cannot call a Go function: `prenv name <pr>` serves prepare-env
// and teardown, and `prenv parse <name>` serves the sweeper, which needs
// the environment-name -> PR-number hop before it can run `gh pr view`.
// Without it each caller would reimplement this parse in sed, which is
// exactly what this package exists to prevent.
package main

import (
	"fmt"
	"os"
	"strconv"
)

const usage = `usage:
  prenv name <pr-number>       print the Railway environment name for a PR
  prenv parse <env-name>       print the PR number encoded in an environment name`

// main dispatches the subcommands. Exit codes are a contract, not an
// afterthought: 2 means "you called me wrong" (unknown subcommand, wrong
// arity, unparseable or non-positive PR number) and 1 means "well-formed
// call, negative answer" (the name is not a PR environment name). The
// sweeper depends on that split to tell a bug from a non-PR environment
// it should simply skip.
func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, usage)
		os.Exit(2)
	}

	switch os.Args[1] {
	case "name":
		if len(os.Args) != 3 {
			fmt.Fprintln(os.Stderr, usage)
			os.Exit(2)
		}
		// argv is the trust boundary -- this is where a bad PR number is
		// rejected, deliberately not inside Name (see name.go).
		pr, err := strconv.Atoi(os.Args[2])
		if err != nil {
			fmt.Fprintf(os.Stderr, "prenv: invalid PR number %q: %v\n", os.Args[2], err)
			os.Exit(2)
		}
		if pr < 1 {
			fmt.Fprintf(os.Stderr, "prenv: invalid PR number %d: must be >= 1\n", pr)
			os.Exit(2)
		}
		fmt.Println(Name(pr))

	case "parse":
		if len(os.Args) != 3 {
			fmt.Fprintln(os.Stderr, usage)
			os.Exit(2)
		}
		pr, ok := ParsePR(os.Args[2])
		if !ok {
			fmt.Fprintf(os.Stderr, "prenv: %q is not a PR environment name\n", os.Args[2])
			os.Exit(1)
		}
		fmt.Println(pr)

	default:
		fmt.Fprintf(os.Stderr, "prenv: unknown subcommand %q\n%s\n", os.Args[1], usage)
		os.Exit(2)
	}
}

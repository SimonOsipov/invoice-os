package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

func main() {
	base := flag.String("base", "", "base branch commit the change will land on")
	head := flag.String("head", "HEAD", "commit holding the change")
	mainRef := flag.String("main", "origin/main", "ref whose migrations count as already applied")
	flag.Parse()

	if *base == "" {
		fmt.Fprintln(os.Stderr, "migrationorder: -base is required")
		os.Exit(2)
	}
	mergeBase, err := git("merge-base", *base, *head)
	if err != nil {
		fmt.Fprintf(os.Stderr, "migrationorder: merge-base %s %s: %v\n", *base, *head, err)
		os.Exit(2)
	}
	// -M finds renames; only equal blob ids are exempt, see Split. R100 ignores line order.
	diff, err := git("diff", "-M", "--raw", "--no-abbrev", "--diff-filter=AR", mergeBase, *head, "--", Dir+"/")
	if err != nil {
		fmt.Fprintf(os.Stderr, "migrationorder: git diff: %v\n", err)
		os.Exit(2)
	}
	changes, err := parseChanges(diff)
	if err != nil {
		fmt.Fprintf(os.Stderr, "migrationorder: %v\n", err)
		os.Exit(2)
	}
	// The newest version comes from base itself, not the merge base: the break is a
	// migration merged to base after this branch was cut.
	onBase, err := git("ls-tree", "--name-only", *base, "--", Dir+"/")
	if err != nil {
		fmt.Fprintf(os.Stderr, "migrationorder: git ls-tree %s: %v\n", *base, err)
		os.Exit(2)
	}

	onMain, err := git("ls-tree", "--name-only", *mainRef, "--", Dir+"/")
	if err != nil {
		fmt.Fprintf(os.Stderr, "migrationorder: git ls-tree %s: %v\n", *mainRef, err)
		os.Exit(2)
	}

	// Head already in main (push to main): nothing is "already on main" to skip, no rename is exempt.
	exempt := true
	// Exit 1 means not an ancestor; any other failure must not leave the skip on.
	var ee *exec.ExitError
	switch err := exec.Command("git", "merge-base", "--is-ancestor", *head, *mainRef).Run(); {
	case err == nil:
		onMain, exempt = "", false
	case errors.As(err, &ee) && ee.ExitCode() == 1:
	default:
		fmt.Fprintf(os.Stderr, "migrationorder: git merge-base --is-ancestor %s %s: %v\n", *head, *mainRef, err)
		os.Exit(2)
	}

	vsBase, vsMain := Split(changes, lines(onBase), lines(onMain), exempt)
	violations, err := Check(vsBase, lines(onBase))
	if err != nil {
		fmt.Fprintf(os.Stderr, "migrationorder: %v\n", err)
		os.Exit(2)
	}
	// Production has applied main's chain only, so main's newest bounds an exempt rename.
	more, err := Check(vsMain, lines(onMain))
	if err != nil {
		fmt.Fprintf(os.Stderr, "migrationorder: %v\n", err)
		os.Exit(2)
	}
	if len(violations)+len(more) == 0 {
		other := "every file"
		if len(vsMain) > 0 {
			other = "every other file"
		}
		fmt.Printf("migrationorder: %s added under %s/ sorts after base's newest migration\n", other, Dir)
		if len(vsMain) > 0 {
			fmt.Printf("migrationorder: %d byte-identical rename(s) of base-only files sort after %s's newest migration instead\n", len(vsMain), *mainRef)
		}
		return
	}
	for _, v := range violations {
		fmt.Printf("::error file=%s::%s sorts at or before %s, already on the base branch. goose refuses it on any database that has applied %s. Rename it to a timestamp later than %d.\n",
			v.File, v.File, v.NewestFile, v.NewestFile, v.Newest)
	}
	for _, v := range more {
		fmt.Printf("::error file=%s::%s is a rename of a base-only file, so it must sort after %s's newest migration, %s. goose refuses it on any database that has applied %s. Rename it to a timestamp later than %d.\n",
			v.File, v.File, *mainRef, v.NewestFile, v.NewestFile, v.Newest)
	}
	os.Exit(1)
}

func git(args ...string) (string, error) {
	out, err := exec.Command("git", args...).Output()
	return strings.TrimSpace(string(out)), err
}

// parseChanges reads `git diff --raw --no-abbrev` lines:
// ":m1 m2 oldblob newblob A\tpath" or ":m1 m2 oldblob newblob R<score>\told\tnew".
func parseChanges(out string) ([]Change, error) {
	var cs []Change
	for _, l := range lines(out) {
		f := strings.Split(l, "\t")
		meta := strings.Fields(f[0])
		if len(meta) != 5 {
			return nil, fmt.Errorf("unexpected git diff line %q", l)
		}
		switch status := meta[4]; {
		case status == "A" && len(f) == 2:
			cs = append(cs, Change{New: f[1]})
		case strings.HasPrefix(status, "R") && len(f) == 3:
			cs = append(cs, Change{Old: f[1], New: f[2], Exact: meta[2] == meta[3]})
		default:
			return nil, fmt.Errorf("unexpected git diff line %q", l)
		}
	}
	return cs, nil
}

func lines(s string) []string {
	if s == "" {
		return nil
	}
	return strings.Split(s, "\n")
}

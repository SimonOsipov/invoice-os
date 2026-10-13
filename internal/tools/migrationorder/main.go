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
	// --no-renames: a renumbered file must be checked under its new name.
	added, err := git("diff", "--no-renames", "--diff-filter=A", "--name-only", mergeBase, *head, "--", Dir+"/")
	if err != nil {
		fmt.Fprintf(os.Stderr, "migrationorder: git diff: %v\n", err)
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

	// Head already in main (push to main): nothing is "already on main" to skip.
	// Exit 1 means not an ancestor; any other failure must not leave the skip on.
	var ee *exec.ExitError
	switch err := exec.Command("git", "merge-base", "--is-ancestor", *head, *mainRef).Run(); {
	case err == nil:
		onMain = ""
	case errors.As(err, &ee) && ee.ExitCode() == 1:
	default:
		fmt.Fprintf(os.Stderr, "migrationorder: git merge-base --is-ancestor %s %s: %v\n", *head, *mainRef, err)
		os.Exit(2)
	}

	violations, err := Check(SkipOnMain(lines(added), lines(onMain)), lines(onBase))
	if err != nil {
		fmt.Fprintf(os.Stderr, "migrationorder: %v\n", err)
		os.Exit(2)
	}
	if len(violations) == 0 {
		fmt.Printf("migrationorder: every file added under %s/ sorts after base's newest migration\n", Dir)
		return
	}
	for _, v := range violations {
		fmt.Printf("::error file=%s::%s sorts at or before %s, already on the base branch. goose refuses it on any database that has applied %s. Rename it to a timestamp later than %d.\n",
			v.File, v.File, v.NewestFile, v.NewestFile, v.Newest)
	}
	os.Exit(1)
}

func git(args ...string) (string, error) {
	out, err := exec.Command("git", args...).Output()
	return strings.TrimSpace(string(out)), err
}

func lines(s string) []string {
	if s == "" {
		return nil
	}
	return strings.Split(s, "\n")
}

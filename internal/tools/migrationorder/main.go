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
	// -M finds renames; only equal blob ids are exempt, see Split. R100 ignores line order. -z: git C-quotes a non-ASCII path otherwise.
	diff, err := git("diff", "-M", "--raw", "--no-abbrev", "-z", "--diff-filter=ADR", mergeBase, *head, "--", Dir+"/")
	if err != nil {
		fmt.Fprintf(os.Stderr, "migrationorder: git diff: %v\n", err)
		os.Exit(2)
	}
	changes, deleted, err := parseChanges(diff)
	if err != nil {
		fmt.Fprintf(os.Stderr, "migrationorder: %v\n", err)
		os.Exit(2)
	}
	// The newest version comes from base itself, not the merge base: the break is a
	// migration merged to base after this branch was cut.
	onBase, err := git("ls-tree", "--name-only", "-z", *base, "--", Dir+"/")
	if err != nil {
		fmt.Fprintf(os.Stderr, "migrationorder: git ls-tree %s: %v\n", *base, err)
		os.Exit(2)
	}

	onMain, err := git("ls-tree", "--name-only", "-z", *mainRef, "--", Dir+"/")
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

	deleted = DeletedOnBase(append(deleted, Vacated(changes, nul(onBase), nul(onMain), exempt)...), nul(onBase))
	headMarks, err := fileAt(*head, MarksFile)
	if err != nil {
		fmt.Fprintf(os.Stderr, "migrationorder: %v\n", err)
		os.Exit(2)
	}
	baseMarks, err := fileAt(mergeBase, MarksFile)
	if err != nil {
		fmt.Fprintf(os.Stderr, "migrationorder: %v\n", err)
		os.Exit(2)
	}
	failures, released := EvaluateDeleted(deleted, NewMarks(lines(headMarks), lines(baseMarks)))

	vsBase, vsMain := Split(changes, nul(onBase), nul(onMain), exempt)
	violations, err := Check(vsBase, nul(onBase))
	if err != nil {
		fmt.Fprintf(os.Stderr, "migrationorder: %v\n", err)
		os.Exit(2)
	}
	// Production has applied main's chain only, so main's newest bounds an exempt rename.
	more, err := Check(vsMain, nul(onMain))
	if err != nil {
		fmt.Fprintf(os.Stderr, "migrationorder: %v\n", err)
		os.Exit(2)
	}
	if len(violations)+len(more)+len(failures) == 0 {
		other := "every file"
		if len(vsMain) > 0 {
			other = "every other file"
		}
		fmt.Printf("migrationorder: %s added under %s/ sorts after base's newest migration\n", other, Dir)
		if len(vsMain) > 0 {
			fmt.Printf("migrationorder: %d byte-identical rename(s) of base-only files sort after %s's newest migration instead\n", len(vsMain), *mainRef)
		}
		if len(released) > 0 {
			fmt.Println("Deliberate migration deletions:")
			for _, r := range released {
				fmt.Println(r)
			}
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
	for _, f := range failures {
		fmt.Println(f)
	}
	os.Exit(1)
}

func git(args ...string) (string, error) {
	out, err := exec.Command("git", args...).Output()
	return strings.TrimSpace(string(out)), err
}

// fileAt returns path's content at rev, or "" when rev has no such file.
func fileAt(rev, path string) (string, error) {
	if exec.Command("git", "cat-file", "-e", rev+":"+path).Run() != nil {
		return "", nil
	}
	out, err := git("show", rev+":"+path)
	if err != nil {
		return "", fmt.Errorf("git show %s:%s: %w", rev, path, err)
	}
	return out, nil
}

// parseChanges reads `git diff --raw --no-abbrev -z` records:
// ":m1 m2 oldblob newblob A" NUL path, "... D" NUL path or "... R<score>" NUL old NUL new.
// deleted holds the D paths.
func parseChanges(out string) (cs []Change, deleted []string, err error) {
	toks := nul(out)
	for i := 0; i < len(toks); {
		meta := strings.Fields(toks[i])
		if len(meta) != 5 || !strings.HasPrefix(meta[0], ":") {
			return nil, nil, fmt.Errorf("unexpected git diff record %q", toks[i])
		}
		status := meta[4]
		n := 1
		if strings.HasPrefix(status, "R") {
			n = 2
		}
		if i+n >= len(toks) {
			return nil, nil, fmt.Errorf("truncated git diff record %q", toks[i])
		}
		p := toks[i+1 : i+1+n]
		switch {
		case status == "A":
			cs = append(cs, Change{New: p[0]})
		case status == "D":
			deleted = append(deleted, p[0])
		case n == 2:
			cs = append(cs, Change{Old: p[0], New: p[1], Exact: meta[2] == meta[3]})
		default:
			return nil, nil, fmt.Errorf("unexpected git diff record %q", toks[i])
		}
		i += 1 + n
	}
	return cs, deleted, nil
}

// nul splits NUL-separated git output into its records.
func nul(s string) []string {
	var out []string
	for _, f := range strings.Split(s, "\x00") {
		if f != "" {
			out = append(out, f)
		}
	}
	return out
}

func lines(s string) []string {
	if s == "" {
		return nil
	}
	return strings.Split(s, "\n")
}

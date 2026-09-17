package main

import (
	"flag"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strings"
)

const hint = "cite a function or test name, not a line number"

func main() {
	base := flag.String("base", "", "commit to diff from (usually the PR base)")
	head := flag.String("head", "HEAD", "commit to diff to")
	flag.Parse()

	if *base == "" {
		fmt.Fprintln(os.Stderr, "citegate: -base is required")
		os.Exit(2)
	}
	mergeBase, err := git("merge-base", *base, *head)
	if err != nil {
		fmt.Fprintf(os.Stderr, "citegate: merge-base %s %s: %v\n", *base, *head, err)
		os.Exit(2)
	}
	mergeBase = strings.TrimSpace(mergeBase)
	headSHA, err := git("rev-parse", *head)
	if err != nil {
		fmt.Fprintf(os.Stderr, "citegate: rev-parse %s: %v\n", *head, err)
		os.Exit(2)
	}
	headSHA = strings.TrimSpace(headSHA)
	if mergeBase == headSHA {
		fmt.Println("citegate: base and head are the same commit — nothing to scan")
		return
	}

	diff, err := git("diff", "--unified=0", mergeBase+"..."+headSHA)
	if err != nil {
		fmt.Fprintf(os.Stderr, "citegate: git diff: %v\n", err)
		os.Exit(2)
	}
	findings, err := Scan(diff, func(path string) (string, error) {
		return git("show", headSHA+":"+path)
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "citegate: git show: %v\n", err)
		os.Exit(2)
	}
	if len(findings) == 0 {
		fmt.Printf("citegate: no line-number citations added (%s...%s)\n", short(mergeBase), short(headSHA))
		return
	}

	for _, f := range findings {
		fmt.Printf("::error file=%s,line=%d::comment cites a line number; %s\n", f.Path, f.Line, hint)
	}
	fmt.Fprintf(os.Stderr, "\ncitegate: %d added comment line(s) cite code by line number.\n\n", len(findings))
	for _, f := range findings {
		fmt.Fprintf(os.Stderr, "%s:%d: %s\n", f.Path, f.Line, strings.TrimSpace(f.Text))
	}
	fmt.Fprintf(os.Stderr, "\nFix: %s. Line numbers go stale when code above them moves.\n", hint)
	os.Exit(1)
}

func git(args ...string) (string, error) {
	out, err := exec.Command("git", args...).Output()
	return string(out), err
}

func short(rev string) string {
	if len(rev) > 8 {
		return rev[:8]
	}
	return rev
}

func sortStrings(s []string) { sort.Strings(s) }

func sortInts(s []int) { sort.Ints(s) }

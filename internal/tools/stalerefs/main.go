package main

import (
	"bufio"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strconv"
	"strings"
)

// allowPath holds literals a maintainer has ruled out, one per line, `#` for
// comments. The one false-positive class measured over 32 PRs is copy the app
// ASSEMBLES rather than stores — `Named in ${n} approval steps` never exists as
// a whole literal, so the scan cannot see that something still produces it.
const allowPath = ".github/stale-refs-allow.txt"

func main() {
	base := flag.String("base", "", "commit to diff from (usually the PR base)")
	head := flag.String("head", "HEAD", "commit to diff to")
	// Searching a historical tree instead of the checkout is what lets this be
	// replayed over past PRs, which is the only way to know its false-positive
	// rate before making it a merge gate.
	tree := flag.String("tree", "", "rev to search for survivors (default: the working tree)")
	flag.Parse()

	if *base == "" {
		fmt.Fprintln(os.Stderr, "stalerefs: -base is required")
		os.Exit(2)
	}
	mergeBase, err := git("merge-base", *base, *head)
	if err != nil {
		fmt.Fprintf(os.Stderr, "stalerefs: merge-base %s %s: %v\n", *base, *head, err)
		os.Exit(2)
	}
	mergeBase = strings.TrimSpace(mergeBase)
	if mergeBase == strings.TrimSpace(mustGit("rev-parse", *head)) {
		fmt.Println("stalerefs: base and head are the same commit — nothing to scan")
		return
	}

	diff, err := git("diff", "--unified=0", mergeBase+"..."+*head)
	if err != nil {
		fmt.Fprintf(os.Stderr, "stalerefs: git diff: %v\n", err)
		os.Exit(2)
	}

	findings := Scan(diff, grepIn(*tree), loadAllow())
	if len(findings) == 0 {
		fmt.Printf("stalerefs: no stale references (%s...%s)\n", short(mergeBase), short(*head))
		return
	}

	for _, f := range findings {
		for _, h := range f.Stale {
			// GitHub renders this as an inline annotation on the offending line.
			fmt.Printf("::error file=%s,line=%d::%q is no longer produced (removed from %s) but this line still references it\n",
				h.Path, h.Line, f.Literal, strings.Join(f.GoneOn, ", "))
		}
	}
	fmt.Fprintf(os.Stderr, "\nstalerefs: %d stale reference(s).\n\n", len(findings))
	for _, f := range findings {
		fmt.Fprintf(os.Stderr, "  %q\n      no producer left; removed from %s\n", f.Literal, strings.Join(f.GoneOn, ", "))
		for _, h := range f.Stale {
			fmt.Fprintf(os.Stderr, "      still referenced at %s:%d: %s\n", h.Path, h.Line, strings.TrimSpace(h.Text))
		}
	}
	fmt.Fprintf(os.Stderr, "\nUpdate the reference, or — if the app assembles this string rather than\nstoring it — add the literal to %s with a reason.\n", allowPath)
	os.Exit(1)
}

func git(args ...string) (string, error) {
	out, err := exec.Command("git", args...).Output()
	return string(out), err
}

func mustGit(args ...string) string {
	out, _ := git(args...)
	return out
}

func short(rev string) string {
	if len(rev) > 8 {
		return rev[:8]
	}
	return rev
}

// grepIn finds every surviving occurrence of a literal in rev, or in the working
// tree when rev is empty. -F so the literal is never read as a pattern, -I so
// binaries never match.
func grepIn(rev string) func(string) []Hit {
	return func(lit string) []Hit {
		args := []string{"grep", "-n", "-F", "-I", lit}
		if rev != "" {
			args = append(args, rev)
		} else {
			args = append(args, "--")
		}
		out, _ := exec.Command("git", args...).Output() // exit 1 == no matches
		return parseGrep(string(out), rev != "")
	}
}

// parseGrep splits `git grep` output. With a rev the format gains a leading
// `rev:` field — reading the wrong field here is how the comment filter came to
// be silently inert for two versions of this scan.
func parseGrep(out string, hasRev bool) []Hit {
	var hits []Hit
	for _, line := range strings.Split(out, "\n") {
		parts := strings.SplitN(line, ":", 3)
		if hasRev {
			parts = strings.SplitN(line, ":", 4)
			if len(parts) == 4 {
				parts = parts[1:]
			}
		}
		if len(parts) < 3 {
			continue
		}
		// The allowlist quotes retired strings by design; a hit there is the
		// point of the file, not a stale reference.
		if parts[0] == allowPath {
			continue
		}
		n, err := strconv.Atoi(parts[1])
		if err != nil {
			continue
		}
		hits = append(hits, Hit{Path: parts[0], Line: n, Text: parts[2]})
	}
	return hits
}

// loadAllow reads the suppression list. A bare line is a literal; a `file:`
// line is a path prefix whose hits never count, for files that exist to archive
// retired copy.
func loadAllow() Allow {
	allow := Allow{Literals: map[string]bool{}}
	f, err := os.Open(allowPath)
	if err != nil {
		return allow
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if p, ok := strings.CutPrefix(line, "file:"); ok {
			allow.Files = append(allow.Files, strings.TrimSpace(p))
			continue
		}
		allow.Literals[line] = true
	}
	return allow
}

func sortStrings(s []string) { sort.Strings(s) }

func sortFindings(f []Finding) {
	sort.Slice(f, func(i, j int) bool { return f[i].Literal < f[j].Literal })
}

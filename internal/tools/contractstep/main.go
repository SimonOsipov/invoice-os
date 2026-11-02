// Command contractstep fails a change that adds a goose migration which deletes or
// renames schema without a "-- contract-step <object>: <reason>" mark in its Up section.
//
// Usage: contractstep -base <commit> [-head <commit>]
//
// Exit 0 pass, 1 unmarked or bad marks, 2 the check could not run.
package main

import (
	"bytes"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
)

const dir = "migrations"

func main() {
	os.Exit(run(".", os.Args[1:], os.Stdout))
}

func run(repo string, args []string, w io.Writer) int {
	results, err := scanAdded(repo, args)
	if err != nil {
		fmt.Fprintf(w, "Could not check migrations: %v. The gate stays red until this check runs.\n", err)
		return 2
	}
	lines, failed := Evaluate(results)
	for _, l := range lines {
		fmt.Fprintln(w, l)
	}
	if failed {
		return 1
	}
	return 0
}

func scanAdded(repo string, args []string) ([]Result, error) {
	fs := flag.NewFlagSet("contractstep", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	base := fs.String("base", "", "base branch commit the change will land on")
	head := fs.String("head", "HEAD", "commit holding the change")
	if err := fs.Parse(args); err != nil {
		return nil, err
	}
	if strings.Trim(*base, "0") == "" {
		return nil, errors.New("no base commit")
	}
	mergeBase, err := git(repo, "merge-base", *base, *head)
	if err != nil {
		return nil, err
	}
	// --no-renames: a renamed migration gets a new version and counts as added.
	// -z: git C-quotes a non-ASCII path otherwise.
	out, err := git(repo, "diff", "--no-renames", "--name-only", "-z", "--diff-filter=A", strings.TrimSpace(mergeBase), *head, "--", dir+"/")
	if err != nil {
		return nil, err
	}
	var results []Result
	for _, path := range strings.Split(out, "\x00") {
		rest, ok := strings.CutPrefix(path, dir+"/")
		if !ok || strings.Contains(rest, "/") || !strings.HasSuffix(rest, ".sql") {
			continue
		}
		sql, err := git(repo, "show", *head+":"+path)
		if err != nil {
			return nil, err
		}
		r, err := Scan(path, sql)
		if err != nil {
			return nil, err
		}
		results = append(results, r)
	}
	return results, nil
}

func git(repo string, args ...string) (string, error) {
	cmd := exec.Command("git", append([]string{"-C", repo}, args...)...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("git %s: %v: %s", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return stdout.String(), nil
}

package main

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
)

const (
	canaryFile = "nul-canary.txt"
	canaryText = "breaklist-nul-canary-5e1d"

	// A whole-repo scan reads ~1.6k files; far fewer means the walk skipped a tree.
	repoFloor = 800
)

type config struct {
	root  string // repo root; holds the canary and is the default search path
	floor int    // minimum files a default (whole-repo) scan must read
}

func main() {
	os.Exit(run(os.Args[1:], config{root: ".", floor: repoFloor}, os.Stdout, os.Stderr))
}

// run returns 0 when the search ran, 1 on bad usage or regexp, 2 when a
// self-check fails and the result cannot be trusted.
func run(args []string, cfg config, stdout, stderr io.Writer) int {
	if len(args) < 1 {
		fmt.Fprintln(stderr, "usage: go run ./internal/tools/breaklist '<Go RE2 regexp>' [path ...]")
		return 1
	}
	re, err := regexp.Compile(args[0])
	if err != nil {
		fmt.Fprintf(stderr, "breaklist: bad regexp: %v\n", err)
		return 1
	}

	if err := checkCanary(cfg.root); err != nil {
		fmt.Fprintf(stderr, "breaklist: SELF-CHECK FAILED, result not trustworthy: %v\n", err)
		return 2
	}

	roots := args[1:]
	wholeRepo := len(roots) == 0
	if wholeRepo {
		roots = []string{cfg.root}
	}
	res, err := Search(re, roots)
	if err != nil {
		fmt.Fprintf(stderr, "breaklist: SELF-CHECK FAILED, walk aborted: %v\n", err)
		return 2
	}
	// An explicit path may legitimately hold few files, so the floor guards only the whole-repo scan.
	if wholeRepo && res.Scanned < cfg.floor {
		fmt.Fprintf(stderr, "breaklist: SELF-CHECK FAILED: scanned %d files, floor is %d; run from the repo root\n", res.Scanned, cfg.floor)
		return 2
	}

	w := bufio.NewWriter(stdout)
	for _, h := range res.Hits {
		fmt.Fprintf(w, "%s:%d: %s\n", h.Path, h.Line, h.Text)
	}
	fmt.Fprintf(w, "TOTAL %d hits in %d files (%d files scanned)\n", len(res.Hits), res.Files, res.Scanned)
	w.Flush()
	return 0
}

// checkCanary proves the walker reads a file grep would skip as binary.
func checkCanary(root string) error {
	dir := filepath.Join(root, canaryDir)
	data, err := os.ReadFile(filepath.Join(dir, canaryFile))
	if err != nil {
		return fmt.Errorf("canary fixture unreadable (run from the repo root): %w", err)
	}
	if bytes.IndexByte(data, 0) < 0 {
		return fmt.Errorf("canary fixture %s lost its NUL byte, so it proves nothing", canaryFile)
	}
	res, err := Search(regexp.MustCompile(regexp.QuoteMeta(canaryText)), []string{dir})
	if err != nil {
		return fmt.Errorf("canary walk: %w", err)
	}
	if len(res.Hits) == 0 {
		return fmt.Errorf("walker did not find %q in the NUL-byte fixture %s", canaryText, canaryFile)
	}
	return nil
}

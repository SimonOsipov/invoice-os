// Command nightlysummary reads the result files of a dispatched CI run and writes summary.json (schema 1).
package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

type sectionFlags []string

func (s *sectionFlags) String() string     { return strings.Join(*s, ",") }
func (s *sectionFlags) Set(v string) error { *s = append(*s, v); return nil }

// run returns 0 on a green verdict, 1 on red, 2 on a usage or input error (no output file then).
func run(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("nightlysummary", flag.ContinueOnError)
	fs.SetOutput(stderr)
	suitesPath := fs.String("suites", "", "suites file: key=Display name[=results] per line (required)")
	jobsPath := fs.String("jobs", "", "JSON array of the run's jobs (required)")
	resultsDir := fs.String("results", "", "directory holding results-<key>/ (required; may be absent)")
	outPath := fs.String("out", "", "summary.json to write (required)")
	runID := fs.Int64("run-id", 0, "id of the dispatched CI run")
	sha := fs.String("sha", "", "commit the run tested")
	startedFlag := fs.String("started", "", "run start as RFC3339 (default: now)")
	nowFlag := fs.String("now", "", "clock as RFC3339 (default: now)")
	workspace := fs.String("workspace", "", "runner workspace path stripped from vitest file names")
	var sections sectionFlags
	fs.Var(&sections, "section", "name=file.json section to copy in (repeatable)")
	fail := func(format string, a ...any) int {
		fmt.Fprintf(stderr, "nightlysummary: "+format+"\n", a...)
		return 2
	}
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *suitesPath == "" || *jobsPath == "" || *resultsDir == "" || *outPath == "" || fs.NArg() != 0 {
		return fail("usage: nightlysummary -suites <file> -jobs <file> -results <dir> -out <file> [-run-id N] [-sha S] [-started RFC3339] [-now RFC3339] [-workspace P] [-section name=file]...")
	}
	now := time.Now().UTC()
	if *nowFlag != "" {
		t, err := time.Parse(time.RFC3339, *nowFlag)
		if err != nil {
			return fail("-now: %v", err)
		}
		now = t.UTC()
	}
	started := now
	if *startedFlag != "" {
		t, err := time.Parse(time.RFC3339, *startedFlag)
		if err != nil {
			return fail("-started: %v", err)
		}
		started = t.UTC()
	}
	rawSuites, err := os.ReadFile(*suitesPath)
	if err != nil {
		return fail("%v", err)
	}
	suites, err := parseSuites(string(rawSuites))
	if err != nil {
		return fail("%s: %v", *suitesPath, err)
	}
	rawJobs, err := os.ReadFile(*jobsPath)
	if err != nil {
		return fail("%v", err)
	}
	jobs, err := parseJobs(rawJobs)
	if err != nil {
		return fail("%s: %v", *jobsPath, err)
	}
	secs, err := parseSectionFlags(sections)
	if err != nil {
		return fail("-section: %v", err)
	}

	sum := assemble(input{
		suites: suites, jobs: jobs, resultsDir: *resultsDir, workspace: *workspace, sections: secs,
		runID: *runID, sha: *sha, started: started, finished: now,
	})
	body, err := sum.marshal()
	if err != nil {
		return fail("%v", err)
	}
	if err := os.WriteFile(*outPath, append(body, '\n'), 0o644); err != nil {
		return fail("%v", err)
	}
	sum.report(stdout)
	if sum.verdict == "red" {
		return 1
	}
	return 0
}

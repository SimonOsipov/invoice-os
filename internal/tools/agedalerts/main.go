// Command agedalerts lists open Dependabot alerts older than a cut and writes an issue body.
package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"time"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("agedalerts", flag.ContinueOnError)
	fs.SetOutput(stderr)
	alertsPath := fs.String("alerts", "", "path to the Dependabot alerts JSON array (required)")
	bodyPath := fs.String("body", "", "path of the issue body file to write (required)")
	days := fs.Int("days", 7, "alerts open longer than this many days count")
	nowFlag := fs.String("now", "", "clock as RFC3339 (default: now)")
	fail := func(format string, a ...any) int {
		fmt.Fprintf(stderr, "agedalerts: "+format+"\n", a...)
		return 2
	}
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *alertsPath == "" || *bodyPath == "" || fs.NArg() != 0 {
		return fail("usage: agedalerts -alerts <file> -body <file> [-days N] [-now RFC3339]")
	}
	if *days < 1 {
		return fail("-days must be at least 1")
	}
	now := time.Now().UTC()
	if *nowFlag != "" {
		t, err := time.Parse(time.RFC3339, *nowFlag)
		if err != nil {
			return fail("-now: %v", err)
		}
		now = t.UTC()
	}
	raw, err := os.ReadFile(*alertsPath)
	if err != nil {
		return fail("%v", err)
	}
	alerts, err := Select(raw, now, time.Duration(*days)*24*time.Hour)
	if err != nil {
		return fail("%v", err)
	}
	body := ""
	if len(alerts) > 0 {
		body = Body(alerts, now, *days)
	}
	if err := os.WriteFile(*bodyPath, []byte(body), 0o644); err != nil {
		return fail("%v", err)
	}
	for _, a := range alerts {
		fmt.Fprintf(stdout, "aged: #%d %s %s %s %dd\n", a.Number, a.Package, a.GHSA, a.Severity, a.AgeDays)
	}
	fmt.Fprintf(stdout, "aged alerts: %d\n", len(alerts))
	return 0
}

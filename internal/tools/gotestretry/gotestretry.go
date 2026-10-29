package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
)

type runner func(args []string, out io.Writer) int

type event struct {
	Action      string
	Package     string
	Test        string
	Output      string
	FailedBuild string
}

type pkgState struct {
	failed      bool // package-level fail action
	buildFailed bool
	panicked    bool
	tests       map[string]string // test -> last run/pass/fail/skip action
}

func run(args []string, stdout, stderr io.Writer, runGo runner) int {
	usage := func() int {
		fmt.Fprintln(stderr, "usage: gotestretry -out <file> -- <go test args>")
		return 2
	}
	sep := -1
	for i, a := range args {
		if a == "--" {
			sep = i
			break
		}
	}
	if sep < 0 || sep == len(args)-1 {
		return usage()
	}
	fs := flag.NewFlagSet("gotestretry", flag.ContinueOnError)
	fs.SetOutput(stderr)
	outPath := fs.String("out", "", "file that receives the go test -json stream")
	if err := fs.Parse(args[:sep]); err != nil || *outPath == "" || fs.NArg() != 0 {
		return usage()
	}
	goArgs := args[sep+1:]

	f, err := os.Create(*outPath)
	if err != nil {
		fmt.Fprintf(stderr, "gotestretry: %v\n", err)
		return 2
	}
	defer f.Close()
	if runGo(append([]string{"test", "-json"}, goArgs...), f) == 0 {
		return 0
	}

	pkgs, err := readStream(*outPath)
	if err != nil {
		fmt.Fprintf(stderr, "gotestretry: %v\n", err)
		return 1
	}
	flags, runVal := carriedFlags(goArgs)
	names := make([]string, 0, len(pkgs))
	for name, p := range pkgs {
		if p.failed {
			names = append(names, name)
		}
	}
	sort.Strings(names)

	ok, reruns := true, 0
	for _, name := range names {
		p := pkgs[name]
		if p.buildFailed {
			ok = false
			continue
		}
		argv := []string{"test", "-json", "-count=1"}
		var label string
		if failed := failedTopLevel(p); p.panicked || unfinished(p) || len(failed) == 0 {
			argv = append(argv, flags...)
			if runVal != "" {
				argv = append(argv, "-run", runVal)
			}
			label = "whole package"
		} else {
			label = "^(" + strings.Join(failed, "|") + ")$"
			argv = append(argv, "-run", label)
			argv = append(argv, flags...)
		}
		argv = append(argv, name)
		fmt.Fprintf(stderr, "gotestretry: re-run %s %s\n", name, label)
		reruns++
		if rerun(*outPath, argv, runGo) != 0 {
			ok = false
		}
	}
	// No re-run means the failure was not in a package event; stay red.
	if ok && reruns > 0 {
		return 0
	}
	return 1
}

// rerun appends the stdout of one go run to path.
func rerun(path string, argv []string, runGo runner) int {
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return 1
	}
	defer f.Close()
	return runGo(argv, f)
}

func readStream(path string) (map[string]*pkgState, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	pkgs := map[string]*pkgState{}
	br := bufio.NewReader(f)
	for {
		line, err := br.ReadString('\n')
		var e event
		if len(line) > 0 && json.Unmarshal([]byte(line), &e) == nil && e.Package != "" {
			p := pkgs[e.Package]
			if p == nil {
				p = &pkgState{tests: map[string]string{}}
				pkgs[e.Package] = p
			}
			consume(p, e)
		}
		if err != nil {
			return pkgs, nil
		}
	}
}

func consume(p *pkgState, e event) {
	if strings.HasPrefix(e.Output, "panic:") {
		p.panicked = true
	}
	if e.Test == "" {
		if e.Action == "fail" {
			p.failed = true
			p.buildFailed = e.FailedBuild != ""
		}
		return
	}
	switch e.Action {
	case "run", "pass", "fail", "skip":
		p.tests[e.Test] = e.Action
	}
}

func failedTopLevel(p *pkgState) []string {
	var out []string
	for name, a := range p.tests {
		if a == "fail" && !strings.Contains(name, "/") {
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out
}

func unfinished(p *pkgState) bool {
	for _, a := range p.tests {
		if a == "run" {
			return true
		}
	}
	return false
}

// carriedFlags returns the -tags and -race tokens of the go test args and the -run value.
func carriedFlags(args []string) (flags []string, runVal string) {
	for i := 0; i < len(args); i++ {
		if !strings.HasPrefix(args[i], "-") {
			continue
		}
		name, val, inline := strings.Cut(strings.TrimLeft(args[i], "-"), "=")
		switch {
		case name == "race":
			flags = append(flags, args[i])
		case name == "tags" || name == "run":
			tok := []string{args[i]}
			if !inline && i+1 < len(args) {
				i++
				val = args[i]
				tok = append(tok, val)
			}
			if name == "run" {
				runVal = val
			} else {
				flags = append(flags, tok...)
			}
		}
	}
	return flags, runVal
}

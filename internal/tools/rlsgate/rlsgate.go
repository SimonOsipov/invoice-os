// Package main implements rlsgate, a `go run`-able summarizer for a
// `go test -json` event stream (M3-17-01): it renders a human-readable
// per-test PASS/SKIP/FAIL view on stdout and exits non-zero iff any
// test-level SKIP, any FAIL, or zero test-level results were observed.
//
// A "test-level result" is a JSON event whose Test field is non-empty and
// whose Action is one of pass/fail/skip; package-level events (no Test
// field, e.g. a bare "no test files" skip) are never counted. The verdict
// must be driven only by the structured Action/Test fields, never by
// grepping the human-readable Output text.
package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

// event mirrors the subset of the `go test -json` (test2json) event shape
// rlsgate consumes. encoding/json ignores unknown fields, so the full
// event set (Time, Elapsed, …) decodes leniently into this struct.
type event struct {
	Action  string
	Package string
	Test    string
	Output  string
}

// summarizer accumulates test-level results while streaming the event log
// and renders the per-test view to out.
type summarizer struct {
	out io.Writer

	// last holds each Package\tTest key's final pass/fail/skip action, so a
	// re-run result replaces the first run's.
	last     map[string]string
	skipKeys []skipKey

	// pending holds buffered Output events per test (keyed by
	// Package\tTest), replayed only if that test ultimately fails so a
	// human reading CI logs sees why without re-running.
	pending map[string][]string
}

type skipKey struct{ key, name string }

// consume decodes one input line and folds it into the running summary. A
// non-JSON / malformed line is silently ignored (never panics, never
// counted), which feeds the zero-ran verdict. Package-level events (empty
// Test) are never treated as test-level results.
func (s *summarizer) consume(line string) {
	var e event
	if err := json.Unmarshal([]byte(line), &e); err != nil {
		return
	}
	if e.Test == "" {
		return
	}
	key := e.Package + "\t" + e.Test
	switch e.Action {
	case "output":
		s.pending[key] = append(s.pending[key], e.Output)
	case "pass", "skip", "fail":
		prev := s.last[key]
		s.last[key] = e.Action
		switch {
		case e.Action == "pass" && prev == "fail":
			fmt.Fprintf(s.out, "FLAKY %s.%s\n", e.Package, e.Test)
		case e.Action == "pass":
			fmt.Fprintf(s.out, "PASS  %s.%s\n", e.Package, e.Test)
		case e.Action == "skip":
			if prev != "skip" {
				s.skipKeys = append(s.skipKeys, skipKey{key, e.Test})
			}
			fmt.Fprintf(s.out, "SKIP  %s.%s\n", e.Package, e.Test)
		default:
			fmt.Fprintf(s.out, "FAIL  %s.%s\n", e.Package, e.Test)
			for _, o := range s.pending[key] {
				io.WriteString(s.out, o)
			}
		}
		delete(s.pending, key)
	}
}

// evaluate reads a `go test -json` event stream from r, writes a
// human-readable PASS/SKIP/FAIL render plus a summary line to out, and
// returns the process exit code: 0 iff every test-level result was a pass
// (and at least one test-level result was seen); non-zero if any
// test-level result was a skip or fail, or if zero test-level results were
// seen at all (including empty or malformed input). A key counts once, by
// its last result; fail then pass prints FLAKY. Precedence: skip →
// zero-ran → fail. The skip branch names the skipped tests so CI surfaces
// which DB-backed suites lost their env.
func evaluate(r io.Reader, out io.Writer) (exitCode int) {
	s := &summarizer{out: out, pending: make(map[string][]string), last: make(map[string]string)}

	// bufio.Reader.ReadString avoids Scanner's token-size limit, so an
	// arbitrarily long Output line can never truncate or error the stream.
	br := bufio.NewReader(r)
	for {
		line, err := br.ReadString('\n')
		if len(line) > 0 {
			s.consume(line)
		}
		if err != nil {
			break
		}
	}

	var passed, skipped, failed int
	for _, a := range s.last {
		switch a {
		case "pass":
			passed++
		case "skip":
			skipped++
		case "fail":
			failed++
		}
	}
	var skipNames []string
	for _, k := range s.skipKeys {
		if s.last[k.key] == "skip" {
			skipNames = append(skipNames, k.name)
		}
	}
	total := passed + skipped + failed
	fmt.Fprintf(out, "rlsgate: %d passed, %d skipped, %d failed (%d test-level results)\n",
		passed, skipped, failed, total)

	switch {
	case skipped > 0:
		fmt.Fprintf(out, "::error::rlsgate: %d DB-backed test(s) SKIPPED (DB env broke): %s\n",
			skipped, strings.Join(skipNames, ", "))
		return 1
	case total == 0:
		fmt.Fprintln(out, "::error::rlsgate: zero test-level results — the suite ran no tests (silent whole-suite skip or unmatched selection)")
		return 1
	case failed > 0:
		return 1
	default:
		return 0
	}
}

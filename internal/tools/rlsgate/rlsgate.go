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
//
// STUB NOTICE (QA Mode A / RALPH RED stage): evaluate below is a
// compile-only stub. It exists solely so rlsgate_test.go compiles and
// fails on its assertions (wrong exit code / missing render output)
// rather than "undefined: evaluate". The real event-parsing and verdict
// logic is implemented by the executor in Stage 3 (M3-17-01).
package main

import "io"

// evaluate reads a `go test -json` event stream from r, writes a
// human-readable PASS/SKIP/FAIL render plus a summary line to out, and
// returns the process exit code: 0 if every test-level result was a pass
// (and at least one test-level result was seen), non-zero if any
// test-level result was a skip or fail, or if zero test-level results
// were seen at all (including empty or malformed input).
//
// TODO: implemented by executor (M3-17-01 Stage 3)
func evaluate(r io.Reader, out io.Writer) (exitCode int) {
	return 0
}

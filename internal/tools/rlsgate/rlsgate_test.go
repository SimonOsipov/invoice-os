// rlsgate_test.go is the M3-17-01 subtask (Test-first: yes): acceptance
// tests for evaluate (rlsgate.go), authored BEFORE the real event-parsing
// / verdict logic exists (RALPH Phase 3.5 / QA Mode A). evaluate is
// currently a compile-only stub that always returns 0 and writes nothing
// (see rlsgate.go's STUB NOTICE), so every test below that expects a
// non-zero exit code or rendered output is RED until M3-17-01's
// implementation lands.
//
// Fixtures are hand-authored `go test -json` event JSONL, built directly
// from the documented event shape (run -> zero+ output -> pass/fail/skip,
// each carrying Test; package-level start/pass/skip events carry no Test
// field and must never be counted as test-level results). No real `go
// test -json` invocation, DB, or network is used anywhere in this file --
// fixtures are deterministic, inline string literals only.
//
// Coverage (finalized Test Specs table, M3-17-01):
//  1. TestEvaluate_AllPass_PrintsAndExitsZero      -- 3 passes, exit 0, rendered PASS lines + summary.
//  2. TestEvaluate_OneSkip_FailsAndNames           -- 2 pass + 1 skip, exit != 0, skip named + ::error:: line.
//  3. TestEvaluate_ZeroTestEvents_Fails            -- only package-level events, exit != 0, zero-results message.
//  4. TestEvaluate_EmptyInput_Fails                -- empty stdin, exit != 0.
//  5. TestEvaluate_OneFail_Fails                   -- 1 fail, exit != 0, failure output echoed.
//  6. TestEvaluate_SubtestsCountedNotDoubleFailed  -- parent + 2 child passes, exit 0, no panic/double-fail.
//  7. TestEvaluate_MixedPassAndSkip_SkipDominates  -- many passes + 1 skip, exit != 0 (skip not masked).
//  8. TestEvaluate_MalformedLine_TreatedAsZeroRan  -- non-JSON garbage lines, exit != 0, no panic.
//
// Assertions are deliberately loose on exact wording/whitespace (the
// verdict/exit code is the primary oracle; rendered-text checks are
// case-tolerant substrings) so a correct implementation with slightly
// different phrasing still passes -- see containsFold/mentionsZeroResults
// below.
package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

// containsFold reports whether s contains substr, ignoring case. Used so
// assertions on rendered output ("PASS", "pass", "Pass") don't pin an
// exact casing the executor hasn't chosen yet.
func containsFold(s, substr string) bool {
	return strings.Contains(strings.ToLower(s), strings.ToLower(substr))
}

// mentionsZeroResults reports whether s plausibly communicates "zero
// test-level results were seen", tolerating a few reasonable phrasings
// (the word "zero", a literal "0" next to "test"/"result", or a
// parenthesized "(0 ...)" summary count) rather than pinning one exact
// message string.
func mentionsZeroResults(s string) bool {
	lower := strings.ToLower(s)
	return strings.Contains(lower, "zero") ||
		strings.Contains(lower, "0 test") ||
		strings.Contains(lower, "0 result") ||
		strings.Contains(lower, "(0 ")
}

// TestEvaluate_AllPass_PrintsAndExitsZero: AC1 render + all-pass verdict.
// Three independent test-level passes (plus realistic package start/pass
// framing with no Test field) must exit 0 and render a PASS line per test
// plus a summary mentioning the count.
func TestEvaluate_AllPass_PrintsAndExitsZero(t *testing.T) {
	fixture := `{"Time":"2026-01-01T00:00:00Z","Action":"start","Package":"github.com/SimonOsipov/invoice-os/internal/tenancy"}
{"Time":"2026-01-01T00:00:00Z","Action":"run","Package":"github.com/SimonOsipov/invoice-os/internal/tenancy","Test":"TestRLS_A"}
{"Time":"2026-01-01T00:00:00Z","Action":"output","Package":"github.com/SimonOsipov/invoice-os/internal/tenancy","Test":"TestRLS_A","Output":"=== RUN   TestRLS_A\n"}
{"Time":"2026-01-01T00:00:00Z","Action":"pass","Package":"github.com/SimonOsipov/invoice-os/internal/tenancy","Test":"TestRLS_A","Elapsed":0.01}
{"Time":"2026-01-01T00:00:00Z","Action":"run","Package":"github.com/SimonOsipov/invoice-os/internal/tenancy","Test":"TestRLS_B"}
{"Time":"2026-01-01T00:00:00Z","Action":"pass","Package":"github.com/SimonOsipov/invoice-os/internal/tenancy","Test":"TestRLS_B","Elapsed":0.02}
{"Time":"2026-01-01T00:00:00Z","Action":"run","Package":"github.com/SimonOsipov/invoice-os/internal/tenancy","Test":"TestRLS_C"}
{"Time":"2026-01-01T00:00:00Z","Action":"pass","Package":"github.com/SimonOsipov/invoice-os/internal/tenancy","Test":"TestRLS_C","Elapsed":0.03}
{"Time":"2026-01-01T00:00:00Z","Action":"pass","Package":"github.com/SimonOsipov/invoice-os/internal/tenancy","Elapsed":0.2}
`
	var buf bytes.Buffer
	code := evaluate(strings.NewReader(fixture), &buf)

	if code != 0 {
		t.Fatalf("evaluate() exit code = %d, want 0 for all-pass input; buf=%q", code, buf.String())
	}
	out := buf.String()
	for _, name := range []string{"TestRLS_A", "TestRLS_B", "TestRLS_C"} {
		if !strings.Contains(out, name) {
			t.Errorf("evaluate() output missing test name %q; buf=%q", name, out)
		}
	}
	if !containsFold(out, "PASS") {
		t.Errorf("evaluate() output missing a PASS render line; buf=%q", out)
	}
	if !containsFold(out, "3") || !containsFold(out, "pass") {
		t.Errorf("evaluate() output missing a summary mentioning 3 passed; buf=%q", out)
	}
}

// TestEvaluate_OneSkip_FailsAndNames: any test-level skip fails the gate
// and the skipped test must be named, including an ::error:: annotation
// line (GitHub Actions workflow-command format) per the contract.
func TestEvaluate_OneSkip_FailsAndNames(t *testing.T) {
	fixture := `{"Action":"run","Package":"pkgA","Test":"TestOne"}
{"Action":"pass","Package":"pkgA","Test":"TestOne","Elapsed":0.01}
{"Action":"run","Package":"pkgA","Test":"TestTwo"}
{"Action":"pass","Package":"pkgA","Test":"TestTwo","Elapsed":0.01}
{"Action":"run","Package":"pkgA","Test":"TestRLS_Foo"}
{"Action":"output","Package":"pkgA","Test":"TestRLS_Foo","Output":"    rls_test.go:42: skipping: DATABASE_URL not set\n"}
{"Action":"skip","Package":"pkgA","Test":"TestRLS_Foo","Elapsed":0.00}
{"Action":"pass","Package":"pkgA","Elapsed":0.05}
`
	var buf bytes.Buffer
	code := evaluate(strings.NewReader(fixture), &buf)

	if code == 0 {
		t.Fatalf("evaluate() exit code = 0, want non-zero when a test-level skip is present; buf=%q", buf.String())
	}
	out := buf.String()
	if !strings.Contains(out, "TestRLS_Foo") {
		t.Errorf("evaluate() output does not name the skipped test TestRLS_Foo; buf=%q", out)
	}
	if !strings.Contains(out, "::error::") {
		t.Errorf("evaluate() output missing an ::error:: annotation line for the skip; buf=%q", out)
	}
}

// TestEvaluate_ZeroTestEvents_Fails: package-level-only events (no Test
// field anywhere, e.g. a "no test files" package) must NOT be counted as
// test-level results, so this is the zero-results failure path, not a
// pass.
func TestEvaluate_ZeroTestEvents_Fails(t *testing.T) {
	fixture := `{"Action":"start","Package":"github.com/SimonOsipov/invoice-os/internal/empty"}
{"Action":"output","Package":"github.com/SimonOsipov/invoice-os/internal/empty","Output":"?   \tgithub.com/SimonOsipov/invoice-os/internal/empty\t[no test files]\n"}
{"Action":"skip","Package":"github.com/SimonOsipov/invoice-os/internal/empty","Elapsed":0}
`
	var buf bytes.Buffer
	code := evaluate(strings.NewReader(fixture), &buf)

	if code == 0 {
		t.Fatalf("evaluate() exit code = 0, want non-zero when zero test-level results are seen; buf=%q", buf.String())
	}
	if !mentionsZeroResults(buf.String()) {
		t.Errorf("evaluate() output does not mention zero test-level results; buf=%q", buf.String())
	}
}

// TestEvaluate_EmptyInput_Fails: empty stdin is the degenerate zero-results
// case explicitly called out by the contract ("incl. empty/malformed
// stdin").
func TestEvaluate_EmptyInput_Fails(t *testing.T) {
	var buf bytes.Buffer
	code := evaluate(strings.NewReader(""), &buf)

	if code == 0 {
		t.Fatalf("evaluate() exit code = 0, want non-zero for empty input; buf=%q", buf.String())
	}
}

// TestEvaluate_OneFail_Fails: any test-level fail fails the gate, and its
// failure Output must be echoed into the render so a human reading CI
// logs sees why without re-running.
func TestEvaluate_OneFail_Fails(t *testing.T) {
	fixture := `{"Action":"run","Package":"pkgA","Test":"TestBad"}
{"Action":"output","Package":"pkgA","Test":"TestBad","Output":"=== RUN   TestBad\n"}
{"Action":"output","Package":"pkgA","Test":"TestBad","Output":"    bad_test.go:10: expected 42, got 7\n"}
{"Action":"output","Package":"pkgA","Test":"TestBad","Output":"--- FAIL: TestBad (0.00s)\n"}
{"Action":"fail","Package":"pkgA","Test":"TestBad","Elapsed":0.00}
{"Action":"fail","Package":"pkgA","Elapsed":0.01}
`
	var buf bytes.Buffer
	code := evaluate(strings.NewReader(fixture), &buf)

	if code == 0 {
		t.Fatalf("evaluate() exit code = 0, want non-zero when a test-level fail is present; buf=%q", buf.String())
	}
	out := buf.String()
	if !strings.Contains(out, "TestBad") {
		t.Errorf("evaluate() output does not name the failed test TestBad; buf=%q", out)
	}
	if !strings.Contains(out, "expected 42, got 7") {
		t.Errorf("evaluate() output does not echo the failure output; buf=%q", out)
	}
}

// TestEvaluate_SubtestsCountedNotDoubleFailed: a parent test plus two
// passing subtests must not panic, must not be miscounted into a failure,
// and must still exit 0. The ran-count is asserted loosely (>=1 PASS
// render) since whether the summary counts parent+children as 1 or 3
// results is an implementation choice this RED stage does not pin down.
func TestEvaluate_SubtestsCountedNotDoubleFailed(t *testing.T) {
	fixture := `{"Action":"run","Package":"pkgA","Test":"TestParent"}
{"Action":"run","Package":"pkgA","Test":"TestParent/child1"}
{"Action":"pass","Package":"pkgA","Test":"TestParent/child1","Elapsed":0.01}
{"Action":"run","Package":"pkgA","Test":"TestParent/child2"}
{"Action":"pass","Package":"pkgA","Test":"TestParent/child2","Elapsed":0.01}
{"Action":"pass","Package":"pkgA","Test":"TestParent","Elapsed":0.02}
{"Action":"pass","Package":"pkgA","Elapsed":0.03}
`
	var buf bytes.Buffer
	code := evaluate(strings.NewReader(fixture), &buf)

	if code != 0 {
		t.Fatalf("evaluate() exit code = %d, want 0 when parent+subtests all pass; buf=%q", code, buf.String())
	}
	out := buf.String()
	passRenders := strings.Count(strings.ToUpper(out), "PASS")
	if passRenders < 1 {
		t.Errorf("evaluate() rendered %d PASS occurrences, want >= 1 for parent+subtest passes; buf=%q", passRenders, out)
	}
}

// TestEvaluate_MixedPassAndSkip_SkipDominates: a large number of passes
// must never mask a single skip -- the skip verdict must dominate
// regardless of how many other tests passed.
func TestEvaluate_MixedPassAndSkip_SkipDominates(t *testing.T) {
	var b strings.Builder
	for i := 1; i <= 25; i++ {
		name := "TestOK" + string(rune('A'+i%26))
		b.WriteString(`{"Action":"run","Package":"pkgA","Test":"`)
		b.WriteString(name)
		b.WriteString(`"}` + "\n")
		b.WriteString(`{"Action":"pass","Package":"pkgA","Test":"`)
		b.WriteString(name)
		b.WriteString(`","Elapsed":0.01}` + "\n")
	}
	b.WriteString(`{"Action":"run","Package":"pkgA","Test":"TestRLS_Dominated"}` + "\n")
	b.WriteString(`{"Action":"skip","Package":"pkgA","Test":"TestRLS_Dominated","Elapsed":0.00}` + "\n")
	b.WriteString(`{"Action":"pass","Package":"pkgA","Elapsed":0.5}` + "\n")

	var buf bytes.Buffer
	code := evaluate(strings.NewReader(b.String()), &buf)

	if code == 0 {
		t.Fatalf("evaluate() exit code = 0, want non-zero: 25 passes must not mask 1 skip; buf=%q", buf.String())
	}
	if !strings.Contains(buf.String(), "TestRLS_Dominated") {
		t.Errorf("evaluate() output does not name the dominating skip TestRLS_Dominated; buf=%q", buf.String())
	}
}

// TestEvaluate_MalformedLine_TreatedAsZeroRan: garbage/non-JSON lines on
// stdin must never panic the process (a crash would be far worse in CI
// than a clean non-zero exit) and, seeing no valid test-level events,
// must fail as a zero-results run.
func TestEvaluate_MalformedLine_TreatedAsZeroRan(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("evaluate() panicked on malformed input: %v", r)
		}
	}()

	fixture := "this is not a JSON line at all\n" +
		`{"Action": "pass", "Test": ` + "\n" + // truncated/invalid JSON
		"{totally not json}\n" +
		"\x00\x01\x02 binary garbage\n"

	var buf bytes.Buffer
	code := evaluate(strings.NewReader(fixture), &buf)

	if code == 0 {
		t.Fatalf("evaluate() exit code = 0, want non-zero for malformed input treated as zero test-level results; buf=%q", buf.String())
	}
}

// ---------------------------------------------------------------------
// QA Mode B adversarial / edge coverage (M3-17-01). These are ADDED after
// the implementation landed (green), on top of the RED acceptance tests
// above. Each predicts the correct behavior from the task-72 contract
// FIRST, then asserts it -- a couple were verified empirically against the
// real binary (`go run ./internal/tools/rlsgate < fixture.jsonl`) before
// being encoded here, see the QA report for that trace.
// ---------------------------------------------------------------------

// TestEvaluate_PackageLevelSkip_NotPhantomTestSkip: the crux false-positive
// risk. A package-level skip event (no Test field, e.g. a "no test files"
// package folded into the same stream as a real suite) must NOT be counted
// as a DB-backed test-level skip, must NOT be named, and must NOT flip the
// exit code -- only skip events carrying a non-empty Test field count,
// per the "test-level result" definition in rlsgate.go's package doc.
func TestEvaluate_PackageLevelSkip_NotPhantomTestSkip(t *testing.T) {
	fixture := `{"Action":"start","Package":"pkgA"}
{"Action":"run","Package":"pkgA","Test":"TestReal"}
{"Action":"pass","Package":"pkgA","Test":"TestReal","Elapsed":0.01}
{"Action":"pass","Package":"pkgA","Elapsed":0.02}
{"Action":"start","Package":"pkgEmpty"}
{"Action":"output","Package":"pkgEmpty","Output":"?   \tpkgEmpty\t[no test files]\n"}
{"Action":"skip","Package":"pkgEmpty","Elapsed":0}
`
	var buf bytes.Buffer
	code := evaluate(strings.NewReader(fixture), &buf)

	if code != 0 {
		t.Fatalf("evaluate() exit code = %d, want 0: a package-level skip (no Test field) must not fail the gate; buf=%q", code, buf.String())
	}
	out := buf.String()
	if strings.Contains(out, "::error::") {
		t.Errorf("evaluate() emitted an ::error:: line for a package-level (non-test) skip; buf=%q", out)
	}
	if !strings.Contains(out, "0 skipped") {
		t.Errorf("evaluate() summary does not report 0 skipped for a package-level-only skip; buf=%q", out)
	}
}

// TestEvaluate_PauseCont_Ignored: parallel tests emit pause/cont around the
// run<->pass window (t.Parallel()). Neither action is pass/fail/skip, so
// neither may be counted, rendered as a PASS/SKIP/FAIL line, or otherwise
// affect the verdict.
func TestEvaluate_PauseCont_Ignored(t *testing.T) {
	fixture := `{"Action":"run","Package":"pkgA","Test":"TestPar1"}
{"Action":"run","Package":"pkgA","Test":"TestPar2"}
{"Action":"pause","Package":"pkgA","Test":"TestPar1"}
{"Action":"cont","Package":"pkgA","Test":"TestPar1"}
{"Action":"pass","Package":"pkgA","Test":"TestPar1","Elapsed":0.01}
{"Action":"pause","Package":"pkgA","Test":"TestPar2"}
{"Action":"cont","Package":"pkgA","Test":"TestPar2"}
{"Action":"pass","Package":"pkgA","Test":"TestPar2","Elapsed":0.01}
{"Action":"pass","Package":"pkgA","Elapsed":0.02}
`
	var buf bytes.Buffer
	code := evaluate(strings.NewReader(fixture), &buf)

	if code != 0 {
		t.Fatalf("evaluate() exit code = %d, want 0 for two parallel passes with pause/cont framing; buf=%q", code, buf.String())
	}
	out := buf.String()
	if got := strings.Count(out, "PASS"); got != 2 {
		t.Errorf("evaluate() rendered %d PASS lines, want exactly 2 (pause/cont must not be double-counted or rendered); buf=%q", got, out)
	}
	if strings.Count(out, "SKIP") != 0 || strings.Count(out, "FAIL") != 0 {
		t.Errorf("evaluate() rendered a SKIP/FAIL line for a pause/cont-only stream; buf=%q", out)
	}
	if !strings.Contains(out, "2 passed") {
		t.Errorf("evaluate() summary does not report 2 passed; buf=%q", out)
	}
}

// TestEvaluate_InterleavedOutputBeforeVerdict: the realistic run -> output
// -> output -> pass shape (captured stdout/stderr lines arriving before the
// final verdict event) must render cleanly and exit 0.
func TestEvaluate_InterleavedOutputBeforeVerdict(t *testing.T) {
	fixture := `{"Action":"run","Package":"pkgA","Test":"TestChatty"}
{"Action":"output","Package":"pkgA","Test":"TestChatty","Output":"=== RUN   TestChatty\n"}
{"Action":"output","Package":"pkgA","Test":"TestChatty","Output":"    chatty_test.go:5: setting up fixture\n"}
{"Action":"pass","Package":"pkgA","Test":"TestChatty","Elapsed":0.01}
{"Action":"pass","Package":"pkgA","Elapsed":0.01}
`
	var buf bytes.Buffer
	code := evaluate(strings.NewReader(fixture), &buf)

	if code != 0 {
		t.Fatalf("evaluate() exit code = %d, want 0 for a chatty passing test; buf=%q", code, buf.String())
	}
	out := buf.String()
	if !strings.Contains(out, "TestChatty") || !containsFold(out, "PASS") {
		t.Errorf("evaluate() did not render TestChatty as PASS; buf=%q", out)
	}
	if !strings.Contains(out, "1 passed") {
		t.Errorf("evaluate() summary does not report 1 passed; buf=%q", out)
	}
}

// TestEvaluate_VeryLongOutputLine_NoTruncationOrPanic: a single Output
// event carrying an >70KB string (e.g. a giant stack dump or verbose
// assertion diff) must not truncate, error, or panic the scanner. rlsgate
// uses bufio.Reader.ReadString rather than bufio.Scanner specifically to
// avoid Scanner's ~64KB default token-size limit -- this proves it.
func TestEvaluate_VeryLongOutputLine_NoTruncationOrPanic(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("evaluate() panicked on a very long Output line: %v", r)
		}
	}()

	bigOutput := strings.Repeat("x", 80_000)
	outputEvent, err := json.Marshal(event{Action: "output", Package: "pkgA", Test: "TestBig", Output: bigOutput})
	if err != nil {
		t.Fatalf("failed to build fixture: %v", err)
	}
	fixture := `{"Action":"run","Package":"pkgA","Test":"TestBig"}` + "\n" +
		string(outputEvent) + "\n" +
		`{"Action":"pass","Package":"pkgA","Test":"TestBig","Elapsed":0.5}` + "\n"

	var buf bytes.Buffer
	code := evaluate(strings.NewReader(fixture), &buf)

	if code != 0 {
		t.Fatalf("evaluate() exit code = %d, want 0 for a pass carrying a >70KB Output line; buf has %d bytes", code, buf.Len())
	}
	if !strings.Contains(buf.String(), "1 passed") {
		t.Errorf("evaluate() summary does not report 1 passed after a very long Output line; buf head=%q", buf.String()[:min(200, buf.Len())])
	}
}

// TestEvaluate_OnlyRunAndOutput_NoResolution_ZeroRan: a Test that starts
// (run) and emits output but never reaches pass/fail/skip (e.g. the test
// binary panicked or the process was killed mid-test, truncating the JSON
// stream) must not be counted as a test-level result -- it must fail as a
// zero-ran run, the same as no tests at all.
func TestEvaluate_OnlyRunAndOutput_NoResolution_ZeroRan(t *testing.T) {
	fixture := `{"Action":"start","Package":"pkgA"}
{"Action":"run","Package":"pkgA","Test":"TestHang"}
{"Action":"output","Package":"pkgA","Test":"TestHang","Output":"=== RUN   TestHang\n"}
{"Action":"output","Package":"pkgA","Test":"TestHang","Output":"panic: runtime error: nil pointer dereference\n"}
`
	var buf bytes.Buffer
	code := evaluate(strings.NewReader(fixture), &buf)

	if code == 0 {
		t.Fatalf("evaluate() exit code = 0, want non-zero: run+output with no pass/fail/skip resolution must be zero-ran; buf=%q", buf.String())
	}
	if !mentionsZeroResults(buf.String()) {
		t.Errorf("evaluate() output does not mention zero test-level results for an unresolved test; buf=%q", buf.String())
	}
}

// TestEvaluate_BlankLinesAndLeadingWhitespace_Tolerated: blank lines and
// leading whitespace before a JSON object (both plausible artifacts of
// piping/buffering `go test -json` through a shell) must not be treated as
// malformed-fatal -- they're skipped like any other unparsable line, and a
// valid stream around them still evaluates normally.
func TestEvaluate_BlankLinesAndLeadingWhitespace_Tolerated(t *testing.T) {
	fixture := "{\"Action\":\"run\",\"Package\":\"pkgA\",\"Test\":\"TestBlank1\"}\n" +
		"\n" + // blank line
		"{\"Action\":\"pass\",\"Package\":\"pkgA\",\"Test\":\"TestBlank1\",\"Elapsed\":0.01}\n" +
		"   {\"Action\":\"run\",\"Package\":\"pkgA\",\"Test\":\"TestBlank2\"}\n" + // leading whitespace
		"{\"Action\":\"pass\",\"Package\":\"pkgA\",\"Test\":\"TestBlank2\",\"Elapsed\":0.01}\n" +
		"\n" + // trailing blank line before EOF
		"{\"Action\":\"pass\",\"Package\":\"pkgA\",\"Elapsed\":0.02}\n"

	var buf bytes.Buffer
	code := evaluate(strings.NewReader(fixture), &buf)

	if code != 0 {
		t.Fatalf("evaluate() exit code = %d, want 0: blank lines/leading whitespace must not break an otherwise-valid all-pass stream; buf=%q", code, buf.String())
	}
	out := buf.String()
	for _, name := range []string{"TestBlank1", "TestBlank2"} {
		if !strings.Contains(out, name) {
			t.Errorf("evaluate() output missing test name %q around blank lines; buf=%q", name, out)
		}
	}
	if !strings.Contains(out, "2 passed") {
		t.Errorf("evaluate() summary does not report 2 passed; buf=%q", out)
	}
}

// TestEvaluate_FailAndSkipCoOccur_BothSurfaced: when a stream has both a
// genuine fail and a skip, the gate must still exit non-zero (precedence
// must never silently drop either signal) and the skip's ::error:: naming
// must still fire -- confirms the skip>0 -> zero-ran -> fail>0 precedence
// order doesn't mask the skip when a fail is also present.
func TestEvaluate_FailAndSkipCoOccur_BothSurfaced(t *testing.T) {
	fixture := `{"Action":"run","Package":"pkgA","Test":"TestBad"}
{"Action":"output","Package":"pkgA","Test":"TestBad","Output":"    bad_test.go:10: expected 42, got 7\n"}
{"Action":"fail","Package":"pkgA","Test":"TestBad","Elapsed":0.00}
{"Action":"run","Package":"pkgA","Test":"TestRLS_Skipped"}
{"Action":"skip","Package":"pkgA","Test":"TestRLS_Skipped","Elapsed":0.00}
{"Action":"fail","Package":"pkgA","Elapsed":0.01}
`
	var buf bytes.Buffer
	code := evaluate(strings.NewReader(fixture), &buf)

	if code == 0 {
		t.Fatalf("evaluate() exit code = 0, want non-zero when fail and skip co-occur; buf=%q", buf.String())
	}
	out := buf.String()
	if !strings.Contains(out, "::error::") || !strings.Contains(out, "TestRLS_Skipped") {
		t.Errorf("evaluate() skip ::error:: naming did not fire when a fail also co-occurred; buf=%q", out)
	}
	if !strings.Contains(out, "TestBad") {
		t.Errorf("evaluate() did not surface the failed test TestBad when a skip also co-occurred; buf=%q", out)
	}
}

// TestEvaluate_DuplicateTestNameAcrossPackages_BothCountedAndDistinguishable:
// the same Test name skipping in two different Packages must count as 2
// skips (not deduplicated to 1) and must remain distinguishable in the
// rendered output via the package-qualified per-event SKIP lines, so a
// human reading the full CI log can tell which two suites actually lost
// their env even though the two skips share a bare test name.
func TestEvaluate_DuplicateTestNameAcrossPackages_BothCountedAndDistinguishable(t *testing.T) {
	fixture := `{"Action":"run","Package":"pkgA","Test":"TestRLS_Dup"}
{"Action":"skip","Package":"pkgA","Test":"TestRLS_Dup","Elapsed":0.00}
{"Action":"run","Package":"pkgB","Test":"TestRLS_Dup"}
{"Action":"skip","Package":"pkgB","Test":"TestRLS_Dup","Elapsed":0.00}
{"Action":"pass","Package":"pkgA","Elapsed":0.01}
{"Action":"pass","Package":"pkgB","Elapsed":0.01}
`
	var buf bytes.Buffer
	code := evaluate(strings.NewReader(fixture), &buf)

	if code == 0 {
		t.Fatalf("evaluate() exit code = 0, want non-zero when duplicate-named tests skip across packages; buf=%q", buf.String())
	}
	out := buf.String()
	if !strings.Contains(out, "2 skipped") {
		t.Errorf("evaluate() summary does not report 2 skipped for duplicate-named skips in different packages (deduplication bug?); buf=%q", out)
	}
	// Per-event render lines are package-qualified ("%s.%s"), so both
	// occurrences must be individually distinguishable in the full output
	// even though they share a bare Test name.
	if !strings.Contains(out, "pkgA.TestRLS_Dup") || !strings.Contains(out, "pkgB.TestRLS_Dup") {
		t.Errorf("evaluate() output does not distinguish the two duplicate-named skips by package; buf=%q", out)
	}
	if strings.Count(out, "TestRLS_Dup") < 2 {
		t.Errorf("evaluate() output does not name TestRLS_Dup for both skip occurrences; buf=%q", out)
	}
}

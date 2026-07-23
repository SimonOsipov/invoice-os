// main_test.go: M5-02-04 RED spec (Mode A) for the submission.Select boot-refusal wiring.
// cmd/submission/ had no test files before this one (main() itself isn't unit-testable --
// it calls log.Fatalf and connects a real DB pool). Mirrors cmd/gateway/main_test.go's
// source-scan idiom exactly: os.ReadFile a relative sibling path, strings.Index an anchor,
// t.Fatal by name if the anchor isn't found (so a future rename can't make this test
// silently vacuous), then assert inside a fixed window following the anchor.
//
// This subtask does NOT wire submission.Select into main.go -- that is the executor's job.
// This test is therefore RED against the stub tree: the "submission.Select(" anchor is not
// yet present in main.go, so it fails via the named t.Fatal below, not the log.Fatal
// assertion further down. That is the expected and correct RED for this stage.
package main

import (
	"os"
	"strings"
	"testing"
)

// TestSubmissionMainFatalsOnAdapterSelectError: AC-6 / AC-7 (Core AC-6's binary-level
// half). Static source scan proving the submission.Select( call site's error path
// terminates the process via log.Fatalf/log.Fatal, before the next top-level statement.
func TestSubmissionMainFatalsOnAdapterSelectError(t *testing.T) {
	b, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatalf("read cmd/submission/main.go: %v", err)
	}
	src := string(b)

	idx := strings.Index(src, "submission.Select(")
	if idx == -1 {
		t.Fatal(`cmd/submission/main.go does not contain a "submission.Select(" call site -- this test's anchor moved (or the wiring hasn't landed yet)`)
	}
	end := idx + 500
	if end > len(src) {
		end = len(src)
	}
	window := src[idx:end]

	if !strings.Contains(window, "log.Fatalf") && !strings.Contains(window, "log.Fatal(") {
		t.Errorf("no log.Fatalf/log.Fatal found within 500 bytes after the submission.Select( call site -- the adapter-selection error path must terminate the process (Core AC-6):\n%s", window)
	}
}

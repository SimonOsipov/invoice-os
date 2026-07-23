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

// TestSubmissionMain_FatalOnAdapterConfigError: M5-03-05 AC-7. Static source scan proving that
// main() reads the mock's config from the environment BEFORE it builds the registry, and that
// the config error path terminates the process.
//
// DELIBERATELY NOT a copy of the 500-byte window above. With this subtask's wiring
// `submission.Select(` sits roughly 120 bytes after `submission.MockConfigFromEnv(`, so a fixed
// 500-byte window anchored at the config call would SWALLOW the Select error path's own
// log.Fatalf (main.go's existing wiring) and pass with a full 100% green even if the config
// error were ignored entirely. The window here is bounded by the NEXT anchor instead, and the
// ordering between the two anchors is asserted rather than assumed -- which is the real
// requirement anyway: a config read that happened AFTER the registry was built could not have
// configured it.
//
// HOW MUCH THIS PROVES, honestly: a source scan shows that a token appears inside a byte range,
// not that the branch is reachable. The behavioural version needs an `adapterFromEnv` seam
// extracted out of main(), which would relocate the shipped M5-02 Select wiring and break the
// anchor of TestSubmissionMainFatalsOnAdapterSelectError above -- a deliberately-shipped
// assertion this story has no mandate to weaken. Recorded as an M5-04 follow-up; M5-04 consumes
// `adapter` and wants a testable seam regardless. The compensating control is that
// MockConfigFromEnv's three branches ARE genuinely unit-tested
// (internal/submission/mock_adapter_test.go's TestMockConfigFromEnv), which shrinks what this
// scan leaves unproven to "main calls it and fatals" -- two lines, verifiable by eye. It is also
// COMMENT-BLIND: strings.Index matches raw bytes, so a comment quoting either anchor would
// satisfy it. Same limitation as the Select scan above; the reason main.go's own TODO avoids
// writing the call-site form.
func TestSubmissionMain_FatalOnAdapterConfigError(t *testing.T) {
	b, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatalf("read cmd/submission/main.go: %v", err)
	}
	src := string(b)

	cfgIdx := strings.Index(src, "submission.MockConfigFromEnv(")
	if cfgIdx == -1 {
		t.Fatal(`cmd/submission/main.go does not contain a "submission.MockConfigFromEnv(" call site -- ` +
			`the mock's latency knob is read nowhere, so APP_ADAPTER_MOCK_LATENCY is inert (or this ` +
			`test's anchor moved)`)
	}
	selIdx := strings.Index(src, "submission.Select(")
	if selIdx == -1 {
		t.Fatal(`cmd/submission/main.go does not contain a "submission.Select(" call site -- this test's ` +
			`closing anchor moved`)
	}
	if selIdx <= cfgIdx {
		t.Fatalf("submission.Select( appears at byte %d, BEFORE submission.MockConfigFromEnv( at byte "+
			"%d -- the adapter config must be read before the registry is built, or the registry cannot "+
			"have been built from it", selIdx, cfgIdx)
	}

	window := src[cfgIdx:selIdx]

	if !strings.Contains(window, "if err != nil") {
		t.Errorf("no `if err != nil` between the submission.MockConfigFromEnv( call site and the "+
			"submission.Select( one -- MockConfigFromEnv's error is being discarded, so a malformed "+
			"APP_ADAPTER_MOCK_LATENCY would boot silently:\n%s", window)
	}
	if !strings.Contains(window, "log.Fatalf") && !strings.Contains(window, "log.Fatal(") {
		t.Errorf("no log.Fatalf/log.Fatal between the submission.MockConfigFromEnv( call site and the "+
			"submission.Select( one -- the adapter-config error path must terminate the process, exactly "+
			"as the Select path does:\n%s", window)
	}
	if !strings.Contains(window, "NewDefaultRegistry(") {
		t.Errorf("no NewDefaultRegistry( between the submission.MockConfigFromEnv( call site and the "+
			"submission.Select( one -- the config that was just read must be what the registry is built "+
			"from:\n%s", window)
	}
}

// TestSubmissionMain_NoNonProductionAdapterFallback: M5-04-08 AC-2. The two tests above
// prove a log.Fatalf sits somewhere inside a fixed window after submission.Select( -- true
// of both the OLD conditional fatal (IsProduction(...) || appAdapter != "") and the NEW
// unconditional one, so neither is sufficient on its own to prove the conditional fallback
// branch was actually deleted, not merely not-shown-by-the-window. This test asserts that
// positively:
//
//  1. the exact log.Printf string the non-production fallback used to emit
//     ("continuing with no adapter configured") is gone from the file entirely -- its
//     presence anywhere, even in a comment, would mean the fallback (or a vestige of it)
//     survived;
//  2. IsProduction( does not appear in the window between submission.Select( and the next
//     statement -- deliberately scoped to that window, not the whole file: registry.go and
//     other call sites may legitimately reference IsProduction elsewhere.
func TestSubmissionMain_NoNonProductionAdapterFallback(t *testing.T) {
	b, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatalf("read cmd/submission/main.go: %v", err)
	}
	src := string(b)

	if strings.Contains(src, "continuing with no adapter configured") {
		t.Error(`cmd/submission/main.go still contains "continuing with no adapter configured" -- ` +
			"the non-production fallback branch (or a vestige of it) was not fully removed; a failed " +
			"adapter Select must be fatal in EVERY environment")
	}

	idx := strings.Index(src, "submission.Select(")
	if idx == -1 {
		t.Fatal(`cmd/submission/main.go does not contain a "submission.Select(" call site -- this test's anchor moved`)
	}
	end := idx + 500
	if end > len(src) {
		end = len(src)
	}
	window := src[idx:end]

	if strings.Contains(window, "IsProduction(") {
		t.Errorf("found IsProduction( within 500 bytes after the submission.Select( call site -- the "+
			"adapter-selection error path must be an unconditional log.Fatalf, not gated on "+
			"IsProduction(...) || appAdapter != \"\":\n%s", window)
	}
}

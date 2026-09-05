// The SPA labels the batch door's awaiting_approval token itself, so its bytes can drift
// from awaitingApprovalReason (handlers.go) with nothing to catch it. TypeScript cannot
// import a Go const, so the pair is pinned by this static scan and, because CI's go path
// filter excludes frontend/**, again in the SPA suite (invoices.test.ts).
package invoice

import (
	"os"
	"regexp"
	"sort"
	"testing"
	"unicode/utf8"
)

const spaInvoicesTSPath = "../../frontend/app/src/lib/invoices.ts"

// The value floor. Both sides are full sentences; a truncated or placeholder label would
// pass a bare non-empty check.
const skipLabelMinRunes = 40

var (
	// \b, not a bare substring: `const SKIP_REASON_LABELS_V2` would satisfy a substring anchor
	// while the map this scan claims to read no longer exists.
	skipMapAnchorRE = regexp.MustCompile(`\bconst SKIP_REASON_LABELS\b`)
	skipMapEndRE    = regexp.MustCompile(`(?m)^}$`)
	skipLabelRowRE  = regexp.MustCompile(`(?m)^\s*(not_validated|duplicate_request|awaiting_approval):\s*'([^']*)',$`)
)

// readSPASkipLabels extracts SKIP_REASON_LABELS from the SPA source. Every failure here is
// fatal: a scan that found nothing must not hand an empty string to an equality assertion.
func readSPASkipLabels(t *testing.T) map[string]string {
	t.Helper()

	b, err := os.ReadFile(spaInvoicesTSPath)
	if err != nil {
		t.Fatalf("read %s: %v", spaInvoicesTSPath, err)
	}
	if len(b) == 0 {
		t.Fatalf("%s is empty; the scan would report a match either way", spaInvoicesTSPath)
	}
	src := string(b)

	anchors := skipMapAnchorRE.FindAllStringIndex(src, -1)
	if len(anchors) != 1 {
		t.Fatalf("%s has %d occurrences of the anchor `const SKIP_REASON_LABELS`, want exactly 1 -- the parity scan lost its anchor", spaInvoicesTSPath, len(anchors))
	}

	// Scope the rows to the map body. File-wide, a decoy `awaiting_approval: '...'` line
	// elsewhere in invoices.ts would be pinned instead of the one that renders.
	body := src[anchors[0][1]:]
	end := skipMapEndRE.FindStringIndex(body)
	if end == nil {
		t.Fatalf("%s: no closing `}` after the SKIP_REASON_LABELS anchor; the map body cannot be bounded", spaInvoicesTSPath)
	}
	body = body[:end[0]]

	labels := make(map[string]string)
	for _, m := range skipLabelRowRE.FindAllStringSubmatch(body, -1) {
		if prev, dup := labels[m[1]]; dup {
			t.Fatalf("%s maps %q twice (%q then %q); the scan cannot say which one renders", spaInvoicesTSPath, m[1], prev, m[2])
		}
		labels[m[1]] = m[2]
	}
	if len(labels) == 0 {
		t.Fatalf("%s: the row pattern matched nothing under a present anchor; the extraction is broken, not the copy", spaInvoicesTSPath)
	}
	return labels
}

// The one assertion this file exists for: the SPA speaks the server's sentence verbatim.
func TestAwaitingApprovalReason_MatchesTheSPASkipLabel(t *testing.T) {
	labels := readSPASkipLabels(t)

	got, ok := labels["awaiting_approval"]
	if !ok {
		t.Fatalf("%s no longer maps awaiting_approval; extracted %d rows: %v", spaInvoicesTSPath, len(labels), labels)
	}
	if got != awaitingApprovalReason {
		t.Errorf("SKIP_REASON_LABELS.awaiting_approval = %q,\nwant awaitingApprovalReason = %q (internal/invoice/handlers.go)", got, awaitingApprovalReason)
	}
}

// The control on the scan above. An equality test between two extracted strings passes for
// free when both come back empty, so the map must be proved parsed before its verdict counts.
func TestAwaitingApprovalReason_ParityScanIsNonVacuous(t *testing.T) {
	labels := readSPASkipLabels(t)

	want := []string{"awaiting_approval", "duplicate_request", "not_validated"}
	keys := make([]string, 0, len(labels))
	for k, v := range labels {
		keys = append(keys, k)
		if v == "" {
			t.Errorf("SKIP_REASON_LABELS.%s extracted as empty", k)
		}
		t.Logf("extracted %-18s = %q bytes=%d runes=%d", k, v, len(v), utf8.RuneCountInString(v))
	}
	sort.Strings(keys)
	if len(keys) != len(want) {
		t.Fatalf("extracted %d SKIP_REASON_LABELS rows %v, want %d %v -- the map did not parse", len(keys), keys, len(want), want)
	}
	for i, k := range keys {
		if k != want[i] {
			t.Errorf("extracted key %d = %q, want %q", i, k, want[i])
		}
	}

	// The floor applies to the value under test only: not_validated is 33 runes today, so a
	// per-row floor would red on copy this subtask does not own.
	if n := utf8.RuneCountInString(labels["awaiting_approval"]); n < skipLabelMinRunes {
		t.Errorf("SKIP_REASON_LABELS.awaiting_approval is %d runes, want at least %d", n, skipLabelMinRunes)
	}
	if n := utf8.RuneCountInString(awaitingApprovalReason); n < skipLabelMinRunes {
		t.Errorf("awaitingApprovalReason is %d runes, want at least %d", n, skipLabelMinRunes)
	}
	t.Logf("go awaitingApprovalReason = %q bytes=%d runes=%d", awaitingApprovalReason, len(awaitingApprovalReason), utf8.RuneCountInString(awaitingApprovalReason))
}

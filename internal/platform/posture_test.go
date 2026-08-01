package platform

import (
	"os"
	"strings"
	"testing"
)

// TestPosture: the task's own Test Specs table (rows 1-8) plus the rest of
// AC-2's near-miss family and AC-1's fail-closed properties. Mirrors
// TestBootstrapEnabledAllowlist's shape (internal/platform/db/bootstrap_test.go).
func TestPosture(t *testing.T) {
	cases := []struct {
		name string
		env  string
		want PostureKind
	}{
		// --- architect's Test Specs table, verbatim ---
		{"local posture", "", PostureLocal},
		{"preview, bare", "pr-7", PosturePreview},
		{"preview, repo-qualified", "invoice-os-pr-42", PosturePreview},
		{"hosted", "production", PostureHosted},
		{"hosted, arbitrary name", "staging", PostureHosted},
		{"near-miss uppercase", "PR-7", PostureHosted},
		{"near-miss trailing space", "pr-7 ", PostureHosted},
		{"near-miss non-numeric", "pr-abc", PostureHosted},

		// --- AC-2's remaining near-miss cases, not already in the table above.
		// Pins drift against prEnvironmentPattern's twin in internal/platform/db. ---
		{"pr- with no number", "pr-", PostureHosted},
		{"pr- with trailing garbage after the number", "pr-7x", PostureHosted},

		// --- totality / fail-closed: only "" reaches the permissive branch ---
		{"whitespace-only is non-empty, stays Hosted", " ", PostureHosted},

		// --- QA adversarial: digit-shape near misses the spec table didn't cover ---
		{"pr-0, single zero digit, still Preview", "pr-0", PosturePreview},
		{"pr-007, leading zeros, still Preview", "pr-007", PosturePreview},
		{"pr- with a digit run past int64 range, still Preview (never parsed as a number)", "pr-" + strings.Repeat("9", 40), PosturePreview},
		{"extremely long non-matching name terminates and stays Hosted", strings.Repeat("x", 10000), PostureHosted},

		// --- QA adversarial: Go's $ behaves like \z (no PCRE trailing-newline exception),
		// so an embedded/trailing newline is a non-match, not a bypass ---
		{"trailing newline after the digits, fail-closed", "pr-7\n", PostureHosted},

		// --- QA adversarial: Unicode whitespace (NBSP) is not ASCII space but still
		// breaks the anchored match, same as the ASCII near-miss rows above ---
		{"trailing non-breaking space, fail-closed", "pr-7 ", PostureHosted},
		{"leading non-breaking space, fail-closed", " pr-7", PostureHosted},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Posture(tc.env); got != tc.want {
				t.Errorf("Posture(%q) = %v, want %v", tc.env, got, tc.want)
			}
		})
	}
}

// TestPRNamePatternPinnedAgainstDBTwin (C2): prNamePattern duplicates db.prEnvironmentPattern
// because that one is unexported and its package must not be touched here. The near-miss rows
// above only pin each package's pattern against itself -- either package's regexp could change
// and its own tests would stay green. Source-scan db/bootstrap.go directly (same idiom as
// cmd/gateway/main_test.go / cmd/submission/main_test.go:115-153) so a future edit to one
// literal without the other fails here instead of drifting silently.
func TestPRNamePatternPinnedAgainstDBTwin(t *testing.T) {
	b, err := os.ReadFile("db/bootstrap.go")
	if err != nil {
		t.Fatalf("read internal/platform/db/bootstrap.go: %v", err)
	}
	src := string(b)

	anchor := "prEnvironmentPattern = regexp.MustCompile(`"
	idx := strings.Index(src, anchor)
	if idx == -1 {
		t.Fatal("internal/platform/db/bootstrap.go: prEnvironmentPattern literal not found -- this test's anchor moved")
	}
	start := idx + len(anchor)
	end := strings.Index(src[start:], "`")
	if end == -1 {
		t.Fatal("internal/platform/db/bootstrap.go: unterminated prEnvironmentPattern literal")
	}
	dbPattern := src[start : start+end]

	if dbPattern != prNamePattern.String() {
		t.Errorf("prNamePattern (%q) has drifted from db.prEnvironmentPattern (%q) -- keep the two literals character-identical",
			prNamePattern.String(), dbPattern)
	}
}

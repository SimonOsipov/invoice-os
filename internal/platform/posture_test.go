package platform

import "testing"

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
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Posture(tc.env); got != tc.want {
				t.Errorf("Posture(%q) = %v, want %v", tc.env, got, tc.want)
			}
		})
	}
}

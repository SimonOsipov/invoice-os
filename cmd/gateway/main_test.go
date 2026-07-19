// main_test.go: Test-first (RED) suite for M4-22-09/task-168's role-password
// rename fallback shim, authored BEFORE resolveRolePassword's real logic
// exists (Test-first: yes). cmd/gateway/ had zero test files before this one
// (main() itself is not unit-testable -- it calls log.Fatalf and opens a real
// listener), so this file is new. It intentionally does NOT re-author
// TestBootstrapRejectsEmptyPasswords (internal/platform/db/bootstrap_test.go:967),
// which already proves the empty-password-is-fatal property this package's
// own resolveRolePassword deliberately does not duplicate.
//
// TestGatewayMainStillPassesRawEnvironment (Test Spec row 2, task-168) is
// deliberately NOT re-authored here either: it would duplicate the existing
// TestGatewayMainPassesRawEnvironmentToProvisioningGuard
// (internal/platform/db/provision_test.go:138), which AC #6 names directly as
// the regression guard this subtask must not disturb -- re-running it
// unmodified, as the Test Spec itself says, is the point, not writing a
// second copy under a new name.
package main

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// TestGatewayMainPrefersUnprefixedPasswordVars: Test Spec #1. Static
// source-scan of cmd/gateway/main.go's RolePasswords literal -- the same
// no-database, read-the-source-text technique
// TestGatewayMainPassesRawEnvironmentToProvisioningGuard
// (internal/platform/db/provision_test.go) already established for pinning a
// call site no Go test can invoke directly. Deprecated var names are built by
// concatenating a lowercase prefix + strings.ToUpper, kept on its own line, on
// purpose: this file is itself repo-wide-grepped by
// TestRepoHasNoStrayInvoicePrefixedVars below (AC #4), and a literal
// uppercase deprecated-prefix + "*PASSWORD" substring sharing one source line
// here would trip that same grep against this test file.
func TestGatewayMainPrefersUnprefixedPasswordVars(t *testing.T) {
	b, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatalf("read cmd/gateway/main.go: %v", err)
	}
	src := string(b)

	idx := strings.Index(src, "Passwords: db.RolePasswords{")
	if idx == -1 {
		t.Fatal(`cmd/gateway/main.go no longer builds Passwords via a "Passwords: db.RolePasswords{" literal -- this test's anchor moved`)
	}
	end := idx + 500
	if end > len(src) {
		end = len(src)
	}
	window := src[idx:end]

	deprecatedPrefix := strings.ToUpper("invoice_")

	for _, tc := range []struct {
		field     string
		newName   string
		oldSuffix string
	}{
		{"Migrator", `"MIGRATOR_PASSWORD"`, "MIGRATOR_PASSWORD"},
		{"App", `"APP_PASSWORD"`, "APP_PASSWORD"},
		{"Reader", `"READER_PASSWORD"`, "TENANT_READER_PASSWORD"},
	} {
		t.Run(tc.field, func(t *testing.T) {
			oldName := `"` + deprecatedPrefix + tc.oldSuffix + `"`

			newIdx := strings.Index(window, tc.newName)
			oldIdx := strings.Index(window, oldName)

			if newIdx == -1 {
				t.Errorf("%s: preferred var %s is not read anywhere in the RolePasswords literal:\n%s", tc.field, tc.newName, window)
			}
			if oldIdx == -1 {
				t.Errorf("%s: deprecated fallback var %s is not present in the RolePasswords literal:\n%s", tc.field, oldName, window)
			}
			if newIdx != -1 && oldIdx != -1 && newIdx > oldIdx {
				t.Errorf("%s: preferred var %s must be read before deprecated fallback %s (new name wins) -- window:\n%s", tc.field, tc.newName, oldName, window)
			}

			// The deprecated name must never be the sole, unconditional
			// os.Getenv(...) argument feeding the field directly -- that would
			// mean the field is populated straight from the deprecated
			// variable with no resolution/fallback logic at all.
			bareOldRead := tc.field + ": os.Getenv(" + oldName + ")"
			if strings.Contains(window, bareOldRead) {
				t.Errorf("%s is populated by a bare os.Getenv(%s) with no resolution/fallback to the preferred name -- window:\n%s", tc.field, oldName, window)
			}
		})
	}
}

// TestGatewayMainWiresEachRoleToItsOwnVarPair: adversarial coverage added at
// QA (Stage 4, task-168). TestGatewayMainPrefersUnprefixedPasswordVars above
// proves each var-name PAIR appears somewhere in the RolePasswords literal
// window in the right relative order -- but it scans the WHOLE window per
// pair, never a single field's own line, so it cannot tell one field's
// resolveRolePassword call from another's. MUTATION-VERIFIED 2026-07-19 (QA):
// swapping the Migrator/App fields' arguments -- so Migrator silently reads
// App's whole var pair and App reads Migrator's (deliberately not spelling
// out the deprecated-prefixed names here -- see the self-grep note below) --
// left TestGatewayMainPrefersUnprefixedPasswordVars fully green (every
// var-name pair is still present, still in order,
// somewhere in the shared window). That swap would silently misconfigure the
// database roles' passwords on every boot -- exactly the "wrong fallback
// bricks the fleet" risk this subtask exists to prevent. This test closes the
// gap by requiring the EXACT literal call, field name included, for each of
// the three roles.
func TestGatewayMainWiresEachRoleToItsOwnVarPair(t *testing.T) {
	b, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatalf("read cmd/gateway/main.go: %v", err)
	}
	src := string(b)

	// Built by concatenation, not as a literal, for the same self-grep reason
	// documented on TestGatewayMainPrefersUnprefixedPasswordVars above.
	deprecatedPrefix := strings.ToUpper("invoice_")

	for _, tc := range []struct {
		field     string
		newName   string
		oldSuffix string
	}{
		{"Migrator", "MIGRATOR_PASSWORD", "MIGRATOR_PASSWORD"},
		{"App", "APP_PASSWORD", "APP_PASSWORD"},
		{"Reader", "READER_PASSWORD", "TENANT_READER_PASSWORD"},
	} {
		t.Run(tc.field, func(t *testing.T) {
			oldName := deprecatedPrefix + tc.oldSuffix
			// \s+ (not a literal single space) because gofmt column-aligns
			// these three struct-literal lines, padding the shorter field
			// names ("App:", "Reader:") with extra spaces to match
			// "Migrator:"'s width.
			pattern := regexp.QuoteMeta(tc.field+":") + `\s*` + regexp.QuoteMeta(`resolveRolePassword("`+tc.newName+`", "`+oldName+`", app.Logger),`)
			re := regexp.MustCompile(pattern)
			if !re.MatchString(src) {
				t.Errorf("cmd/gateway/main.go does not contain the exact wiring %q -- the %s field must resolve from its own (%s, %s) var pair, not a swapped or mismatched one", pattern, tc.field, tc.newName, oldName)
			}
		})
	}
}

// TestRepoHasNoStrayInvoicePrefixedVars: Test Spec #3 / AC #4. Walks every
// git-tracked file (git ls-files, so node_modules/.git/backlog/etc. are
// excluded the same way the AC's own `--exclude-dir` grep excludes them) and
// enforces the bounded blast radius: every deprecated-prefix "*PASSWORD" hit
// must live in cmd/gateway/main.go and nowhere else; every deprecated-prefix
// "*DATABASE_URL" hit must be zero, repo-wide, no exceptions (those two DSN
// vars are Railway-console/docs-only -- no Go code reads them).
//
// Deliberately RED right now for a reason unrelated to any Go code: per
// task-168's Stage 1+2 Correction A, docs/migrations.md:80,429,432,433,436,437
// still reference the deprecated DSN var names. Renaming those is the
// executor's docs-half of this subtask (out of QA Mode-A's scope), so this
// test fails on real, expected hits until Stage 3 lands.
func TestRepoHasNoStrayInvoicePrefixedVars(t *testing.T) {
	rootOut, err := exec.Command("git", "rev-parse", "--show-toplevel").Output()
	if err != nil {
		t.Fatalf("git rev-parse --show-toplevel: %v", err)
	}
	root := strings.TrimSpace(string(rootOut))

	filesOut, err := exec.Command("git", "-C", root, "ls-files").Output()
	if err != nil {
		t.Fatalf("git -C %s ls-files: %v", root, err)
	}
	files := strings.Split(strings.TrimSpace(string(filesOut)), "\n")

	// See TestGatewayMainPrefersUnprefixedPasswordVars's doc comment above:
	// built the same way, for the same self-grep reason.
	deprecatedPrefix := strings.ToUpper("invoice_")
	passwordPattern := regexp.MustCompile(deprecatedPrefix + `.*PASSWORD`)
	dsnPattern := regexp.MustCompile(deprecatedPrefix + `.*DATABASE_URL`)

	const wantPasswordFile = "cmd/gateway/main.go"
	var passwordHitsElsewhere []string
	var dsnHits []string

	for _, rel := range files {
		if rel == "" {
			continue
		}
		content, err := os.ReadFile(filepath.Join(root, rel))
		if err != nil {
			// Broken symlink, submodule gitlink, or similar: irrelevant to a
			// plain-text content grep, so skip rather than fail the suite.
			continue
		}
		text := string(content)
		if passwordPattern.MatchString(text) && rel != wantPasswordFile {
			passwordHitsElsewhere = append(passwordHitsElsewhere, rel)
		}
		if dsnPattern.MatchString(text) {
			dsnHits = append(dsnHits, rel)
		}
	}

	if len(passwordHitsElsewhere) > 0 {
		t.Errorf("deprecated *PASSWORD var referenced outside %s (want none): %v", wantPasswordFile, passwordHitsElsewhere)
	}
	if len(dsnHits) > 0 {
		t.Errorf("deprecated *DATABASE_URL var referenced, want zero hits repo-wide: %v", dsnHits)
	}
}

// TestRolePasswordResolutionPrecedence: Test Spec #4 (table-driven, 4 cases).
// Exercises resolveRolePassword directly with synthetic, made-up env var
// names rather than the real MIGRATOR_PASSWORD/etc. triples on purpose:
// resolveRolePassword's logic is generic over whatever two names it is given
// (Correction B's extracted-helper design), so a fixture pair fully proves
// the resolution/precedence/warning behavior without coupling this test to
// literal production var names -- and, incidentally, sidesteps the same
// self-grep concern the two tests above route around by construction (a
// fixture name containing neither "PASSWORD" nor the deprecated prefix cannot
// ever trip AC #4's repo-wide grep). TestGatewayMainPrefersUnprefixedPasswordVars
// above is what proves main.go actually calls this function with the three
// REAL name pairs, in the right order; this test proves the function's logic
// is correct for any pair.
//
// Judgement call (flagged as such by the Stage 2.5 brief): case 3 (both set)
// still logs a warning, even though the deprecated value is never used --
// an unused-but-present deprecated variable is still a stale Railway var an
// operator should clean up, and AC #3 does not forbid warning outside the
// strict fallback-fired case. See resolveRolePassword's doc comment in
// main.go for the corresponding real-fix note.
func TestRolePasswordResolutionPrecedence(t *testing.T) {
	const (
		newVar = "GATEWAY_TEST_ROLE_PW_NEW"
		oldVar = "GATEWAY_TEST_ROLE_PW_OLD"
	)

	cases := []struct {
		name      string
		newVal    string
		oldVal    string
		wantValue string
		wantWarn  bool
	}{
		{
			name:      "new set, old unset",
			newVal:    "new-secret",
			oldVal:    "",
			wantValue: "new-secret",
			wantWarn:  false,
		},
		{
			name:      "new unset, old set",
			newVal:    "",
			oldVal:    "old-secret",
			wantValue: "old-secret",
			wantWarn:  true,
		},
		{
			name:      "both set: new wins, old ignored but still flagged for cleanup",
			newVal:    "new-secret",
			oldVal:    "old-secret",
			wantValue: "new-secret",
			wantWarn:  true,
		},
		{
			name:      "neither set: empty, no warning (fail-fast is validateRolePasswords' job, not this function's)",
			newVal:    "",
			oldVal:    "",
			wantValue: "",
			wantWarn:  false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(newVar, tc.newVal)
			t.Setenv(oldVar, tc.oldVal)

			var buf bytes.Buffer
			logger := slog.New(slog.NewJSONHandler(&buf, nil))

			got := resolveRolePassword(newVar, oldVar, logger)
			if got != tc.wantValue {
				t.Errorf("resolveRolePassword(%q, %q, ...) = %q, want %q", newVar, oldVar, got, tc.wantValue)
			}

			logged := buf.String()
			if !tc.wantWarn {
				if logged != "" {
					t.Errorf("expected no warning logged, got: %s", logged)
				}
				return
			}

			if logged == "" {
				t.Fatalf("expected a deprecation warning to be logged naming %s and %s, got none", oldVar, newVar)
			}
			var entry map[string]any
			if err := json.Unmarshal(buf.Bytes(), &entry); err != nil {
				t.Fatalf("log line is not valid JSON: %v\nraw: %s", err, logged)
			}
			if level, _ := entry["level"].(string); level != "WARN" {
				t.Errorf("log level = %q, want WARN", level)
			}
			msg, _ := entry["msg"].(string)
			if !strings.Contains(msg, oldVar) {
				t.Errorf("warning message %q does not name the deprecated variable %s", msg, oldVar)
			}
			if !strings.Contains(msg, newVar) {
				t.Errorf("warning message %q does not name the replacement variable %s", msg, newVar)
			}
			if !strings.Contains(strings.ToLower(msg), "deprecated") {
				t.Errorf("warning message %q does not say the variable is deprecated, so an operator reading Railway logs would not know it needs cleanup", msg)
			}
		})
	}
}

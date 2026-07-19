// main_test.go: role-password rename fallback shim tests (M4-22-09/task-168).
// cmd/gateway/ had no test files before this one (main() itself isn't
// unit-testable -- it calls log.Fatalf and opens a real listener).
// Deliberately does NOT re-author TestBootstrapRejectsEmptyPasswords
// (internal/platform/db/bootstrap_test.go) or
// TestGatewayMainPassesRawEnvironmentToProvisioningGuard
// (internal/platform/db/provision_test.go, AC #6's named regression guard)
// -- both already cover what their names say.
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
// source-scan of main.go's RolePasswords literal. Deprecated var names are
// built via ToUpper(prefix) + suffix, never as a literal substring -- this
// file is itself grepped by TestRepoHasNoStrayInvoicePrefixedVars below
// (AC #4), and a literal deprecated "*PASSWORD" string here would trip it.
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

// TestGatewayMainWiresEachRoleToItsOwnVarPair: adversarial coverage.
// TestGatewayMainPrefersUnprefixedPasswordVars above only proves each
// var-name pair appears somewhere in the RolePasswords window, in order --
// it can't tell one field's resolveRolePassword call from another's.
// Mutation-verified: swapping Migrator/App's arguments left that test green
// while silently misconfiguring both roles' passwords on every boot. This
// test requires the exact literal call, field name included, per role.
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
// git-tracked file (git ls-files) and enforces the bounded blast radius:
// every deprecated-prefix "*PASSWORD" hit must live in cmd/gateway/main.go
// and nowhere else (docs/migrations.md excepted, see below); every
// deprecated-prefix "*DATABASE_URL" hit must be zero, same exception --
// those two DSN vars are Railway-console/docs-only, no Go code reads them.
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
	// docs/migrations.md names the deprecated vars truthfully because Railway
	// still holds them (escalation E3 pending) -- not a stray code reference.
	const docsExceptionFile = "docs/migrations.md"
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
		if passwordPattern.MatchString(text) && rel != wantPasswordFile && rel != docsExceptionFile {
			passwordHitsElsewhere = append(passwordHitsElsewhere, rel)
		}
		if dsnPattern.MatchString(text) && rel != docsExceptionFile {
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

// TestRolePasswordResolutionPrecedence: Test Spec #4 (table-driven).
// Exercises resolveRolePassword with synthetic env var names, not the real
// MIGRATOR_PASSWORD/etc. triples: its logic is generic over whatever two
// names it's given, so a fixture pair proves resolution/precedence/warning
// behavior without coupling to production names (and sidesteps the same
// self-grep concern above, since fixture names contain neither "PASSWORD"
// nor the deprecated prefix). TestGatewayMainPrefersUnprefixedPasswordVars
// proves main.go calls this with the three real pairs, in order.
//
// Case 3 (both set) still warns even though the deprecated value goes
// unused -- an operator should still know to clean up the stale var.
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

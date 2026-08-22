// request_test.go: RED specs for AUDIT-05-01 (Mode A) — parseRequest, bundleFilename and
// the package's import fence. package archive (white-box): both symbols are unexported and
// this subtask ships no export_test.go shim. Precedent: internal/approval/*_test.go.
package archive

import (
	"go/build"
	"net/url"
	"os/exec"
	"strings"
	"testing"
	"time"
)

const (
	validEntityID = "a1b2c3d4-a1b2-c3d4-a1b2-c3d4a1b2c3d4"
	validFrom     = "2026-01-01T00:00:00Z"
	validTo       = "2026-03-31T23:59:59Z"
)

func mustParseRFC3339(t *testing.T, s string) time.Time {
	t.Helper()
	when, err := time.Parse(time.RFC3339, s)
	if err != nil {
		t.Fatalf("test setup: time.Parse(%q): %v", s, err)
	}
	return when
}

func TestParseRequest_MissingEntityIDIsRefused(t *testing.T) {
	query := url.Values{"from": {validFrom}, "to": {validTo}}
	_, msg := parseRequest(query)
	if want := "entity_id is required"; msg != want {
		t.Errorf("parseRequest(no entity_id) message = %q, want %q", msg, want)
	}
}

func TestParseRequest_MalformedEntityIDIsRefused(t *testing.T) {
	query := url.Values{"entity_id": {"not-a-uuid"}, "from": {validFrom}, "to": {validTo}}
	_, msg := parseRequest(query)
	if want := "entity_id must be a well-formed uuid"; msg != want {
		t.Errorf("parseRequest(malformed entity_id) message = %q, want %q", msg, want)
	}
}

// AC-2 divergence from internal/audit on purpose: entity_id="" is refused like absent,
// not treated as "no filter" (internal/audit/handlers.go:70-72 documents the opposite).
func TestParseRequest_EmptyIsNotAbsent(t *testing.T) {
	query := url.Values{"entity_id": {""}, "from": {validFrom}, "to": {validTo}}
	_, msg := parseRequest(query)
	if want := "entity_id is required"; msg != want {
		t.Errorf("parseRequest(entity_id=\"\") message = %q, want %q (empty must refuse like absent)", msg, want)
	}
}

func TestParseRequest_NonRFC3339FromIsRefused(t *testing.T) {
	query := url.Values{"entity_id": {validEntityID}, "from": {"2026-01-01"}, "to": {validTo}}
	_, msg := parseRequest(query)
	if want := "from must be an RFC3339 timestamp"; msg != want {
		t.Errorf("parseRequest(malformed from) message = %q, want %q", msg, want)
	}
}

func TestParseRequest_MissingFromIsRefused(t *testing.T) {
	query := url.Values{"entity_id": {validEntityID}, "to": {validTo}}
	_, msg := parseRequest(query)
	if want := "from is required"; msg != want {
		t.Errorf("parseRequest(no from) message = %q, want %q", msg, want)
	}
}

func TestParseRequest_MissingToIsRefused(t *testing.T) {
	query := url.Values{"entity_id": {validEntityID}, "from": {validFrom}}
	_, msg := parseRequest(query)
	if want := "to is required"; msg != want {
		t.Errorf("parseRequest(no to) message = %q, want %q", msg, want)
	}
}

func TestParseRequest_NonRFC3339ToIsRefused(t *testing.T) {
	query := url.Values{"entity_id": {validEntityID}, "from": {validFrom}, "to": {"2026-03-31"}}
	_, msg := parseRequest(query)
	if want := "to must be an RFC3339 timestamp"; msg != want {
		t.Errorf("parseRequest(malformed to) message = %q, want %q", msg, want)
	}
}

func TestParseRequest_FromAfterToIsRefused(t *testing.T) {
	query := url.Values{
		"entity_id": {validEntityID},
		"from":      {"2026-04-01T00:00:00Z"},
		"to":        {"2026-01-01T00:00:00Z"},
	}
	_, msg := parseRequest(query)
	if want := "from must not be after to"; msg != want {
		t.Errorf("parseRequest(from after to) message = %q, want %q", msg, want)
	}
}

// Bounds are inclusive (D-4): from == to is accepted, not refused.
func TestParseRequest_EqualFromAndToIsAccepted(t *testing.T) {
	query := url.Values{"entity_id": {validEntityID}, "from": {validFrom}, "to": {validFrom}}
	r, msg := parseRequest(query)
	if msg != "" {
		t.Fatalf("parseRequest(from==to) message = %q, want accepted (empty)", msg)
	}
	wantWhen := mustParseRFC3339(t, validFrom)
	if r.EntityID != validEntityID || !r.From.Equal(wantWhen) || !r.To.Equal(wantWhen) {
		t.Errorf("parseRequest(from==to) = %+v, want EntityID=%q From=To=%v", r, validEntityID, wantWhen)
	}
}

func TestBundleFilename_MatchesAudit08Pattern(t *testing.T) {
	r := Request{EntityID: validEntityID, From: mustParseRFC3339(t, validFrom), To: mustParseRFC3339(t, validTo)}
	got := bundleFilename("Honeywell Group", r)
	want := "ASComply_evidence_Honeywell-Group_20260101_20260331.zip"
	if got != want {
		t.Errorf("bundleFilename(%q) = %q, want %q", "Honeywell Group", got, want)
	}
}

func TestBundleFilename_CollapsesPunctuationRuns(t *testing.T) {
	r := Request{EntityID: validEntityID, From: mustParseRFC3339(t, validFrom), To: mustParseRFC3339(t, validTo)}
	got := bundleFilename("Okafor & Partners (Lagos)", r)
	want := "ASComply_evidence_Okafor-Partners-Lagos_20260101_20260331.zip"
	if got != want {
		t.Errorf("bundleFilename(%q) = %q, want %q (single '-' per run, no trailing '-')", "Okafor & Partners (Lagos)", got, want)
	}
}

func TestBundleFilename_NonAlphanumericNameFallsBackToUUID(t *testing.T) {
	r := Request{EntityID: validEntityID, From: mustParseRFC3339(t, validFrom), To: mustParseRFC3339(t, validTo)}
	got := bundleFilename("———", r)
	want := "ASComply_evidence_" + validEntityID + "_20260101_20260331.zip"
	if got != want {
		t.Errorf("bundleFilename(non-alnum name) = %q, want %q (fallback to entity uuid)", got, want)
	}
}

func TestBundleFilename_TruncatesLongNameTo48Bytes(t *testing.T) {
	name := strings.Repeat("A", 120)
	r := Request{EntityID: validEntityID, From: mustParseRFC3339(t, validFrom), To: mustParseRFC3339(t, validTo)}
	got := bundleFilename(name, r)
	want := "ASComply_evidence_" + strings.Repeat("A", maxSlugBytes) + "_20260101_20260331.zip"
	if got != want {
		t.Errorf("bundleFilename(120-char name) = %q, want %q (slug truncated to %d bytes)", got, want, maxSlugBytes)
	}
}

// TestArchivePackage_ImportsOnlyStdlibAndUUID guards AC-1: internal/archive must never
// gain a dependency on another internal package (the D-1 cycle risk with internal/audit /
// internal/submission) or any third-party module beyond github.com/google/uuid. Modeled on
// internal/actor/actor_test.go:227 (TestActorPackage_ImportsOnlyStdlib).
func TestArchivePackage_ImportsOnlyStdlibAndUUID(t *testing.T) {
	const selfPath = "github.com/SimonOsipov/invoice-os/internal/archive"
	allowed := map[string]bool{"github.com/google/uuid": true}
	isStdlibOrAllowed := func(imp string) bool {
		if allowed[imp] || imp == selfPath {
			return true
		}
		first, _, _ := strings.Cut(imp, "/")
		return !strings.Contains(first, ".")
	}

	pkg, err := build.ImportDir(".", 0)
	if err != nil {
		t.Fatalf("build.ImportDir(.) failed: %v", err)
	}
	// Direct, non-test imports only. Empty would mean every allowlist check below passes
	// vacuously, which is exactly the failure mode this test exists to prevent.
	if len(pkg.Imports) == 0 {
		t.Fatal("internal/archive imports nothing at all -- the allowlist assertions would pass vacuously")
	}
	for _, imp := range pkg.Imports {
		if !isStdlibOrAllowed(imp) {
			t.Errorf("internal/archive imports %q, want stdlib or github.com/google/uuid only", imp)
		}
	}

	// Transitive check: nothing direct could reach a forbidden package today, but a future
	// edit could. "." not "./internal/archive": the test's CWD is this package's directory.
	out, err := exec.CommandContext(t.Context(), "go", "list", "-deps", ".").Output()
	if err != nil {
		t.Fatalf("go list -deps .: %v", err)
	}
	deps := strings.Split(strings.TrimSpace(string(out)), "\n")
	if len(deps) == 0 || (len(deps) == 1 && deps[0] == "") {
		t.Fatal("go list -deps returned nothing; the allowlist assertions above would pass vacuously")
	}
	for _, dep := range deps {
		dep = strings.TrimSpace(dep)
		if dep == "" {
			continue
		}
		if !isStdlibOrAllowed(dep) {
			t.Errorf("internal/archive transitively depends on %q, want stdlib or github.com/google/uuid only", dep)
		}
	}
}

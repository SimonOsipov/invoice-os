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
	"unicode/utf8"
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

// AC-3 adversarial: from/to must compare by instant, not by clock digits or offset
// string. +01:00 (WAT, Africa/Lagos's fixed offset -- Lagos observes no DST) is one
// hour ahead of Z, so "01:00+01:00" and "00:00Z" name the same instant.
func TestParseRequest_NonUTCOffsetsCompareByInstant(t *testing.T) {
	query := url.Values{
		"entity_id": {validEntityID},
		"from":      {"2026-01-01T01:00:00+01:00"},
		"to":        {"2026-01-01T00:00:00Z"},
	}
	r, msg := parseRequest(query)
	if msg != "" {
		t.Fatalf("parseRequest(equal instants, different offsets) message = %q, want accepted (equal, inclusive)", msg)
	}
	if !r.From.Equal(r.To) {
		t.Errorf("r.From = %v, r.To = %v, want equal instants", r.From, r.To)
	}
}

// A clock reading with a larger hour digit can still name an EARLIER instant once its
// offset is applied. If the code compared strings or local clock fields instead of
// instants, this would be wrongly refused as "from after to".
func TestParseRequest_LaterClockDigitsButEarlierInstantIsAccepted(t *testing.T) {
	query := url.Values{
		"entity_id": {validEntityID},
		"from":      {"2026-01-01T00:59:00+01:00"}, // = 2025-12-31T23:59:00Z
		"to":        {"2026-01-01T00:00:00Z"},
	}
	_, msg := parseRequest(query)
	if msg != "" {
		t.Errorf("parseRequest(from clock-later but instant-earlier) message = %q, want accepted", msg)
	}
}

// AC-4 adversarial: bundleFilename dates the file by the UTC calendar day, which can
// differ from the offset's local calendar day near midnight.
func TestBundleFilename_UsesUTCDateNotLocalOffset(t *testing.T) {
	from := mustParseRFC3339(t, "2026-01-01T00:30:00+01:00") // = 2025-12-31T23:30:00Z
	r := Request{EntityID: validEntityID, From: from, To: mustParseRFC3339(t, validTo)}
	got := bundleFilename("Honeywell Group", r)
	want := "ASComply_evidence_Honeywell-Group_20251231_20260331.zip"
	if got != want {
		t.Errorf("bundleFilename(local-midnight-crossing from) = %q, want %q (UTC date, not local)", got, want)
	}
}

// AC-2 adversarial: uuid.Parse is permissive of non-canonical spellings, and
// Request.EntityID deliberately keeps the caller's raw string (see request.go) rather
// than uuid.Parse's normalized form. Confirms that deliberate choice holds for every
// form uuid.Parse accepts, so a later DB-layer subtask inherits the exact string, not a
// silently-normalized one.
func TestParseRequest_NonCanonicalUUIDFormsAcceptedRaw(t *testing.T) {
	cases := []string{
		"A1B2C3D4-A1B2-C3D4-A1B2-C3D4A1B2C3D4",          // uppercase
		"{a1b2c3d4-a1b2-c3d4-a1b2-c3d4a1b2c3d4}",        // braced
		"urn:uuid:a1b2c3d4-a1b2-c3d4-a1b2-c3d4a1b2c3d4", // urn prefix
		"a1b2c3d4a1b2c3d4a1b2c3d4a1b2c3d4",              // no hyphens
	}
	if len(cases) == 0 {
		t.Fatal("test setup: no cases")
	}
	for _, raw := range cases {
		query := url.Values{"entity_id": {raw}, "from": {validFrom}, "to": {validTo}}
		r, msg := parseRequest(query)
		if msg != "" {
			t.Errorf("parseRequest(entity_id=%q) message = %q, want accepted", raw, msg)
			continue
		}
		if r.EntityID != raw {
			t.Errorf("parseRequest(entity_id=%q).EntityID = %q, want unchanged raw string", raw, r.EntityID)
		}
	}
}

// AC-2 adversarial: url.Values.Get silently takes the first value of a repeated
// parameter. Documents that a duplicate entity_id resolves to the FIRST value, not the
// last or an error.
func TestParseRequest_DuplicateEntityIDTakesFirstValue(t *testing.T) {
	second := "b2c3d4a1-b2c3-d4a1-b2c3-d4a1b2c3d4a1"
	query := url.Values{"entity_id": {validEntityID, second}, "from": {validFrom}, "to": {validTo}}
	r, msg := parseRequest(query)
	if msg != "" {
		t.Fatalf("parseRequest(duplicate entity_id) message = %q, want accepted", msg)
	}
	if r.EntityID != validEntityID {
		t.Errorf("parseRequest(duplicate entity_id).EntityID = %q, want first value %q", r.EntityID, validEntityID)
	}
}

// AC-4 adversarial: pure ASCII punctuation (the regex's actual declared class), as
// opposed to the existing em-dash case which is non-ASCII.
func TestBundleFilename_ASCIIPunctuationOnlyFallsBackToUUID(t *testing.T) {
	r := Request{EntityID: validEntityID, From: mustParseRFC3339(t, validFrom), To: mustParseRFC3339(t, validTo)}
	got := bundleFilename("!!!@@@###", r)
	want := "ASComply_evidence_" + validEntityID + "_20260101_20260331.zip"
	if got != want {
		t.Errorf("bundleFilename(ASCII punctuation only) = %q, want %q (fallback to entity uuid)", got, want)
	}
}

// AC-4 boundary: a slug exactly 48 bytes must NOT be truncated (the check is
// len(slug) > maxSlugBytes, not >=).
func TestBundleFilename_ExactlyMaxSlugBytesIsNotTruncated(t *testing.T) {
	name := strings.Repeat("B", maxSlugBytes)
	r := Request{EntityID: validEntityID, From: mustParseRFC3339(t, validFrom), To: mustParseRFC3339(t, validTo)}
	got := bundleFilename(name, r)
	want := "ASComply_evidence_" + name + "_20260101_20260331.zip"
	if got != want {
		t.Errorf("bundleFilename(exactly %d-byte name) = %q, want %q (untruncated)", maxSlugBytes, got, want)
	}
}

// AC-4 adversarial: multi-byte runes (CJK, combining marks) must never survive into the
// slug -- the regex replaces every non-ASCII-alnum rune with '-' before truncation, so
// byte-slicing at maxSlugBytes can never land mid-rune. This guards that invariant: if a
// future edit widened the regex to admit Unicode letters, this test would catch the
// resulting invalid UTF-8 from a mid-rune byte cut.
func TestBundleFilename_MultiByteNameTruncatesWithoutCorruptingUTF8(t *testing.T) {
	name := "北京公司" + strings.Repeat("C", 60) + "é̀̂" // CJK + combining marks around the cut
	r := Request{EntityID: validEntityID, From: mustParseRFC3339(t, validFrom), To: mustParseRFC3339(t, validTo)}
	got := bundleFilename(name, r)
	if !utf8.ValidString(got) {
		t.Fatalf("bundleFilename(multi-byte name) = %q, produced invalid UTF-8", got)
	}
	wantPrefix := "ASComply_evidence_" + strings.Repeat("C", maxSlugBytes) + "_"
	if !strings.HasPrefix(got, wantPrefix) {
		t.Errorf("bundleFilename(multi-byte name) = %q, want prefix %q (multi-byte runes stripped, then truncated to %d bytes)", got, wantPrefix, maxSlugBytes)
	}
}

// AC-2 adversarial: an absurdly long malformed entity_id must be refused cleanly, not
// panic or hang uuid.Parse.
func TestParseRequest_ExtremelyLongEntityIDIsRefused(t *testing.T) {
	query := url.Values{"entity_id": {strings.Repeat("a", 100000)}, "from": {validFrom}, "to": {validTo}}
	_, msg := parseRequest(query)
	if want := "entity_id must be a well-formed uuid"; msg != want {
		t.Errorf("parseRequest(100000-char entity_id) message = %q, want %q", msg, want)
	}
}

// AC-4 adversarial: an absurdly long entity name must still truncate to maxSlugBytes,
// not panic or produce an unbounded filename.
func TestBundleFilename_ExtremelyLongNameIsCappedAtMaxSlugBytes(t *testing.T) {
	name := strings.Repeat("D", 1_000_000)
	r := Request{EntityID: validEntityID, From: mustParseRFC3339(t, validFrom), To: mustParseRFC3339(t, validTo)}
	got := bundleFilename(name, r)
	want := "ASComply_evidence_" + strings.Repeat("D", maxSlugBytes) + "_20260101_20260331.zip"
	if got != want {
		t.Errorf("bundleFilename(1,000,000-char name) length = %d, want capped result %q", len(got), want)
	}
}

// actor_test.go: AUDIT-02-01 (task-606) RED specs (QA Mode A) for internal/actor
// -- transcribed from the Test Specs table before actor.go has a body.
//
// package actor_test (external), per [test-package-follows-the-symbol]: Name,
// Kind and Label are the whole surface.
//
// No testify, no t.Skip, no DB, no network, no clock.
package actor_test

import (
	"go/build"
	"os/exec"
	"strings"
	"testing"

	"github.com/SimonOsipov/invoice-os/internal/actor"
)

func ptr[T any](v T) *T { return &v }

const uuid = "a1b2c3d4-a1b2-c3d4-a1b2-c3d4a1b2c3d4"

func TestActorName_DisplayNameWins(t *testing.T) {
	got := actor.Name(ptr("Folake Adesina"), ptr("f@x.ng"), uuid)
	want := actor.Label{Text: "Folake Adesina", Kind: actor.KindPerson}
	if got != want {
		t.Errorf("Name(display, email, uuid) = %+v, want %+v", got, want)
	}
}

func TestActorName_FallsBackToEmail(t *testing.T) {
	got := actor.Name(nil, ptr("f@x.ng"), uuid)
	want := actor.Label{Text: "f@x.ng", Kind: actor.KindPerson}
	if got != want {
		t.Errorf("Name(nil, email, uuid) = %+v, want %+v", got, want)
	}
}

func TestActorName_FallsBackToSubjectVerbatim(t *testing.T) {
	got := actor.Name(nil, nil, uuid)
	want := actor.Label{Text: uuid, Kind: actor.KindRaw}
	if got != want {
		t.Errorf("Name(nil, nil, uuid) = %+v, want %+v", got, want)
	}
	if got.Text != uuid {
		t.Errorf("Text = %q, want byte-identical %q", got.Text, uuid)
	}
}

// D-31, user's B2 answer: a non-nil "" is absent and falls through at every rung.
func TestActorName_EmptyStringFallsThrough(t *testing.T) {
	got1 := actor.Name(ptr(""), ptr("f@x.ng"), uuid)
	want1 := actor.Label{Text: "f@x.ng", Kind: actor.KindPerson}
	if got1 != want1 {
		t.Errorf("Name(\"\", email, uuid) = %+v, want %+v", got1, want1)
	}

	got2 := actor.Name(ptr(""), ptr(""), uuid)
	want2 := actor.Label{Text: uuid, Kind: actor.KindRaw}
	if got2 != want2 {
		t.Errorf("Name(\"\", \"\", uuid) = %+v, want %+v", got2, want2)
	}
}

// internal/approval's holderName stops on a non-nil "" (read_model.go:421,
// TestHolderName_EmptyStringDisplayNameDoesNotFallThrough) -- D-31 chose the
// opposite for this ladder. This test pins the divergence so a future reader
// does not "fix" one to match the other.
func TestActorName_DivergesFromHolderNameDeliberately(t *testing.T) {
	got := actor.Name(ptr(""), ptr("f@x.ng"), "user-2")
	holderNameEquivalent := "" // holderName(ptr(""), ...) stops here, returns ""
	if got.Text == holderNameEquivalent {
		t.Errorf("Name(\"\", email, subject).Text = %q, must diverge from holderName's %q (D-31)", got.Text, holderNameEquivalent)
	}
	want := "f@x.ng"
	if got.Text != want {
		t.Errorf("Name(\"\", email, subject).Text = %q, want %q", got.Text, want)
	}
}

// AC-3 scopes the non-empty guarantee to non-empty subject: subject == "" with
// nil/nil legitimately yields Text == "". So this table holds subject non-empty.
func TestActorName_NeverReturnsEmptyText(t *testing.T) {
	tests := []struct {
		name        string
		displayName *string
		email       *string
	}{
		{"both nil", nil, nil},
		{"both empty", ptr(""), ptr("")},
		{"display nil, email set", nil, ptr("f@x.ng")},
		{"display set, email nil", ptr("Folake"), nil},
		{"display empty, email set", ptr(""), ptr("f@x.ng")},
		{"display set, email empty", ptr("Folake"), ptr("")},
	}
	if len(tests) == 0 {
		t.Fatal("test table is empty -- would vacuously pass")
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := actor.Name(tt.displayName, tt.email, uuid)
			if got.Text == "" {
				t.Errorf("Name(%v, %v, uuid).Text = \"\", want non-empty", tt.displayName, tt.email)
			}
		})
	}
}

func TestActorName_SubjectIsNotParsed(t *testing.T) {
	got := actor.Name(nil, nil, "backfill-source-rows")
	want := actor.Label{Text: "backfill-source-rows", Kind: actor.KindRaw}
	if got != want {
		t.Errorf("Name(nil, nil, non-uuid) = %+v, want %+v -- Name must not validate shape (that's Resolve's job)", got, want)
	}
}

// KindSystem is Resolve's to assign (AUDIT-02-02), never Name's -- pin both
// directions so nothing special-cases the literal "system" inside Name.
func TestActorName_DoesNotSpecialCaseSystem(t *testing.T) {
	got := actor.Name(nil, nil, "system")
	want := actor.Label{Text: "system", Kind: actor.KindRaw}
	if got != want {
		t.Errorf("Name(nil, nil, \"system\") = %+v, want %+v (rung 3 is raw, not system)", got, want)
	}

	got2 := actor.Name(ptr("System"), nil, uuid)
	want2 := actor.Label{Text: "System", Kind: actor.KindPerson}
	if got2 != want2 {
		t.Errorf("Name(\"System\", nil, uuid) = %+v, want %+v", got2, want2)
	}
}

// docs/audit-log-read-contract.md §6: the resolver's grammar accepts braces
// and non-canonical hyphens. Name must not lowercase or strip either, or it
// diverges from what the resolver (and uuid_in) accept.
func TestActorName_SubjectIsNotNormalised(t *testing.T) {
	subject := "{A1B2C3D4-A1B2-C3D4-A1B2-C3D4A1B2C3D4}"
	got := actor.Name(nil, nil, subject)
	want := actor.Label{Text: subject, Kind: actor.KindRaw}
	if got != want {
		t.Errorf("Name(nil, nil, braced-upper) = %+v, want %+v (braces/case preserved)", got, want)
	}
}

// audit_log.actor is text(1..255); Name must pass Unicode and the max length
// through unmangled and untruncated. Mirrors internal/approval's
// TestResolveHolder_UnicodeAndVeryLongNameInPlusNFormatting.
func TestActorName_PassesUnicodeAndMaxLengthThrough(t *testing.T) {
	unicodeName := "Adéọlá Bàbáyẹmi"
	got := actor.Name(ptr(unicodeName), nil, uuid)
	want := actor.Label{Text: unicodeName, Kind: actor.KindPerson}
	if got != want {
		t.Errorf("Name(unicode, nil, uuid) = %+v, want %+v", got, want)
	}

	longSubject := strings.Repeat("a", 255)
	got2 := actor.Name(nil, nil, longSubject)
	want2 := actor.Label{Text: longSubject, Kind: actor.KindRaw}
	if got2 != want2 {
		t.Errorf("Name(nil, nil, 255-char subject) len = %d, want %d untruncated", len(got2.Text), len(want2.Text))
	}
}

// Kind's zero value ("") must never collide with a defined constant, or a
// zero-value Label{} would be mistakable for a resolved one.
func TestActorName_KindZeroValueIsDistinguishable(t *testing.T) {
	zero := actor.Kind("")
	for _, k := range []actor.Kind{actor.KindSystem, actor.KindPerson, actor.KindRaw} {
		if zero == k {
			t.Errorf("zero-value Kind collides with %q", k)
		}
	}
}

// AC-3 fences its guarantee to non-empty subject: Name(nil, nil, "") is the
// one input where Label.Text == "" is legitimate. Pin it so the fence stays
// exercised, not just claimed.
func TestActorName_EmptySubjectYieldsBlankTextAsAC3Permits(t *testing.T) {
	got := actor.Name(nil, nil, "")
	want := actor.Label{Text: "", Kind: actor.KindRaw}
	if got != want {
		t.Errorf("Name(nil, nil, \"\") = %+v, want %+v", got, want)
	}
}

// FINDING (report only, do not fix here): a whitespace-only display_name is
// NOT treated as absent -- it renders as KindPerson with a blank-looking Text.
// D-31 settled "" as absent; whitespace was not asked. This pins today's
// behaviour so a change is deliberate, not silent.
func TestActorName_WhitespaceOnlyDisplayNameIsNotTreatedAsAbsent(t *testing.T) {
	got := actor.Name(ptr(" "), ptr("f@x.ng"), uuid)
	want := actor.Label{Text: " ", Kind: actor.KindPerson}
	if got != want {
		t.Errorf("Name(\" \", email, uuid) = %+v, want %+v (current behaviour, see QA finding)", got, want)
	}
}

// Name claims byte-for-byte passthrough; prove it holds for RTL text, an
// embedded newline and combining diacritics, not just plain ASCII/Latin.
func TestActorName_PassesRTLAndControlCharsThrough(t *testing.T) {
	rtl := "محمد" // Arabic "Muhammad"
	got := actor.Name(ptr(rtl), nil, uuid)
	want := actor.Label{Text: rtl, Kind: actor.KindPerson}
	if got != want {
		t.Errorf("Name(rtl, nil, uuid) = %+v, want %+v", got, want)
	}

	withNewline := "Folake\nAdesina"
	got2 := actor.Name(ptr(withNewline), nil, uuid)
	want2 := actor.Label{Text: withNewline, Kind: actor.KindPerson}
	if got2 != want2 {
		t.Errorf("Name(newline, nil, uuid) = %+v, want %+v", got2, want2)
	}

	combining := "é̀̂" // e + three combining marks
	got3 := actor.Name(nil, nil, combining)
	want3 := actor.Label{Text: combining, Kind: actor.KindRaw}
	if got3 != want3 {
		t.Errorf("Name(nil, nil, combining) = %+v, want %+v", got3, want3)
	}
}

// AC-4 requires this package importable by internal/audit, internal/invoice,
// internal/approval and internal/tenancy without a cycle. resolve.go takes the
// caller's pgx.Tx, so github.com/jackc/pgx/v5 is admitted by name -- it is a leaf
// that imports none of those four. The allowlist is exactly one entry; widening it
// is a story-level call, not a convenience.
func TestActorPackage_ImportsOnlyStdlib(t *testing.T) {
	allowed := map[string]bool{"github.com/jackc/pgx/v5": true}

	pkg, err := build.ImportDir(".", 0)
	if err != nil {
		t.Fatalf("build.ImportDir(.) failed: %v", err)
	}
	// build.Package.Imports covers the non-test files only, which is the graph
	// AC-4 is about. Empty means resolve.go stopped importing pgx and every
	// assertion below would pass vacuously.
	if len(pkg.Imports) == 0 {
		t.Fatal("internal/actor imports nothing at all -- resolve.go must import pgx; the allowlist assertions would pass vacuously")
	}
	for _, imp := range pkg.Imports {
		if allowed[imp] {
			continue
		}
		// Stdlib import paths carry no dot in their first segment.
		if first, _, _ := strings.Cut(imp, "/"); !strings.Contains(first, ".") {
			continue
		}
		t.Errorf("internal/actor imports %q, want stdlib or github.com/jackc/pgx/v5 only", imp)
	}

	// The property the allowlist exists to protect, checked TRANSITIVELY rather
	// than on direct imports: pgx could not reach these, but a future edit could.
	// "." not "./internal/actor": the test's CWD is this package's directory.
	out, err := exec.CommandContext(t.Context(), "go", "list", "-deps", ".").Output()
	if err != nil {
		t.Fatalf("go list -deps .: %v", err)
	}
	deps := strings.Split(string(out), "\n")
	if len(deps) < 2 {
		t.Fatalf("go list -deps returned %d lines; the cycle assertions below would pass vacuously", len(deps))
	}
	for _, forbidden := range []string{"audit", "invoice", "approval", "tenancy"} {
		path := "github.com/SimonOsipov/invoice-os/internal/" + forbidden
		for _, dep := range deps {
			if strings.TrimSpace(dep) == path {
				t.Errorf("internal/actor depends on %s -- all four of its consumers would cycle", path)
			}
		}
	}
}

// actor_test.go: AUDIT-02-01 (task-606) RED specs (QA Mode A) for internal/actor
// -- transcribed from the Test Specs table before actor.go has a body.
//
// package actor_test (external), per [test-package-follows-the-symbol]: Name,
// Kind and Label are the whole surface.
//
// No testify, no t.Skip, no DB, no network, no clock.
package actor_test

import (
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

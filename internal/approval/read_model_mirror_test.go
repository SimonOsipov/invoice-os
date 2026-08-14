package approval

// FK-11 style (test "FK-11: FAILURE_KINDS matches the Go vocabulary" in
// frontend/app/src/lib/invoices.test.ts): expected strings are hand-transcribed
// from roles.ts, never read back from the Go port under test.

import (
	"fmt"
	"strings"
	"testing"
)

func TestReadModel_ResolveStringsMirrorRolesTs(t *testing.T) {
	// Independently transcribed from roles.ts:119-124 and roles.ts:63.
	const missingText = "Role no longer exists"
	const noneText = "Nobody assigned"
	const plusNShape = "%s +%d"
	const deletedRoleText = "Deleted role"

	if got := resolveHolder(false, nil); got.Text != missingText || !got.Warn {
		t.Errorf("missing: got %+v, want {%q true}", got, missingText)
	}
	if got := resolveHolder(true, nil); got.Text != noneText || !got.Warn {
		t.Errorf("none: got %+v, want {%q true}", got, noneText)
	}

	holders := []holderInput{
		{Name: "Musa Danjuma", Status: "active", AccessRole: "reviewer"},
		{Name: "Halima Yusuf", Status: "suspended", AccessRole: "reviewer"},
	}
	want := fmt.Sprintf(plusNShape, "Musa Danjuma", 1)
	if got := resolveHolder(true, holders); got.Text != want {
		t.Errorf("+N: got %q, want %q", got.Text, want)
	}

	if got := roleTitle(false, ""); got != deletedRoleText {
		t.Errorf("deleted role: got %q, want %q", got, deletedRoleText)
	}
}

func TestReadModel_InspectorResolveStringsMirrorRolesTs(t *testing.T) {
	// Independently transcribed from roles.ts:127-133 -- not read from inspectorResolveHolder.
	cases := []struct {
		name       string
		roleExists bool
		holders    []holderInput
		text       string
		warn       bool
	}{
		{"missing", false, nil, "Role no longer exists", true},
		{"none", true, nil, "Nobody holds this role — this step will block", true},
		{"blocked", true,
			[]holderInput{{Name: "Halima Yusuf", Status: "suspended", AccessRole: "reviewer"}},
			"Currently: Halima Yusuf — this step will block", true},
		{"ok", true,
			[]holderInput{{Name: "Musa Danjuma", Status: "active", AccessRole: "reviewer"}},
			"Currently: Musa Danjuma", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := inspectorResolveHolder(c.roleExists, c.holders)
			if got.Text != c.text {
				t.Errorf("text: got %q, want %q", got.Text, c.text)
			}
			if got.Warn != c.warn {
				t.Errorf("warn: got %v, want %v", got.Warn, c.warn)
			}
			if strings.Contains(got.Text, "+") {
				t.Errorf("inspector wording must never contain +N: %q", got.Text)
			}
		})
	}
}

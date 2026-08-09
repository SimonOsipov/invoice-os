package approval

// The seam's four pure surfaces: the slug port (AC-3, AC-5), the sentinel→status
// mapping (AC-7) and the wire shape of Role (AC-2). No DB, no HTTP, no skips (AC-8).

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"testing"

	"github.com/SimonOsipov/invoice-os/internal/platform/db"
)

// The tables below spell taken-sets as slices; newRoleKey wants the lookup map.
func takenSet(keys ...string) map[string]bool {
	set := make(map[string]bool, len(keys))
	for _, k := range keys {
		set[k] = true
	}
	return set
}

// TestWorkflowRole_KeyMatchesShippedSlugAlgorithm pins the port against every
// pair frontend/app/src/lib/roles.test.ts already asserts, plus the branches that
// suite never reaches.
func TestWorkflowRole_KeyMatchesShippedSlugAlgorithm(t *testing.T) {
	cases := []struct {
		name  string
		taken []string
		title string
		want  string
	}{
		// --- the pairs roles.test.ts pins, transcribed (443, 448, 461, 466, 468, 472, 477) ---
		{"slugifies the title", nil, "Engagement Partner", "engagement-partner"},
		{"suffixes on collision", []string{"tax-reviewer"}, "Tax Reviewer", "tax-reviewer-2"},
		{"a punctuation-only title falls back to the literal role", nil, "###", "role"},
		{"the fallback composes with the ordinary suffix", []string{"role"}, "###", "role-2"},
		{"the fallback composes with the second suffix", []string{"role", "role-2"}, "###", "role-3"},
		{"an emoji-only title hits the same fallback", nil, "🎉🎉🎉", "role"},
		{"non-latin letters are dropped and the ascii kept", nil, "Ω Reviewer", "reviewer"},
		// roles.test.ts:839 asserts only `!= "fin_mgr"`; the exact value is strictly stronger.
		{"a renamed title derives a key that is not the stored one", []string{"fin_mgr"}, "Chief Engagement Officer", "chief-engagement-officer"},

		// --- added here: roles.test.ts exercises no collapse-and-trim title ---
		{"runs of spaces collapse to one hyphen and the ends are trimmed", nil, "  Spaced  Out  ", "spaced-out"},
		{"an inner separator collapses like any other run", nil, "A/B Testing", "a-b-testing"},
		// --- added here: roles.test.ts never frees the base while its -2 is taken ---
		{"a free base wins even when its -2 is taken", []string{"reviewer-2"}, "Reviewer", "reviewer"},

		// --- QA adversarial: unicode past the two cases roles.test.ts pins. Every want
		// below was cross-checked against the shipped newRoleKey, not derived by reading it. ---
		{"an accented letter is dropped, never folded to its ascii base", nil, "Caf\u00e9 Manager", "caf-manager"},
		// The same glyphs as the row above, decomposed: now the combining mark is the only
		// non-slug rune, so the base letter survives and the two forms disagree.
		{"a decomposed accent keeps its base letter, so NFD and NFC yield different keys", nil, "Cafe\u0301 Manager", "cafe-manager"},
		// Unlike the emoji row these are letters, and every all-non-ascii title lands on
		// the single fallback — so two unrelated titles collide there.
		{"a title of only non-ascii letters falls back like punctuation", nil, "承認者", "role"},
		{"ß is dropped rather than expanded to ss", nil, "Gr\u00f6\u00dfe Reviewer", "gr-e-reviewer"},
		// The one JS divergence: JS full case-mapping gives "i"+U+0307 and slugs to i-x.
		// Pinned at the Go value so adopting x/text case-mapping fails here loudly.
		{"U+0130 lowercases without a combining mark in Go", nil, "\u0130X", "ix"},

		// --- QA adversarial: the fallback's own boundaries ---
		// Distinct from "###": the separator is already in the input, so trimming before
		// replacing — or treating "-" as slug-safe — would never reach the fallback.
		{"a hyphen-only title reaches the fallback too", nil, "---", "role"},
		{"an empty title returns the fallback rather than panicking; the store rejects it first", nil, "", "role"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := newRoleKey(takenSet(tc.taken...), tc.title); got != tc.want {
				t.Errorf("newRoleKey(%v, %q) = %q, want %q", tc.taken, tc.title, got, tc.want)
			}
		})
	}
}

// TestWorkflowRole_KeyNeverCollidesWithASeededKey mirrors roles.test.ts:451-456
// over the six SEED_FIRM_ROLES pairs (roles.ts:46-58), hardcoded: the seed is the
// SPA's and this table is the only copy this package carries.
func TestWorkflowRole_KeyNeverCollidesWithASeededKey(t *testing.T) {
	seeded := []struct{ key, title string }{
		{"preparer", "Invoice Preparer"},
		{"fin_mgr", "Engagement Manager"},
		{"fin_dir", "Senior Manager"},
		{"compliance", "Tax Reviewer"},
		{"cfo", "Engagement Partner"},
		{"quality_reviewer", "Quality Reviewer"},
	}
	if len(seeded) == 0 {
		t.Fatal("empty seed table would make every case below a vacuous pass")
	}
	keys := make([]string, 0, len(seeded))
	for _, s := range seeded {
		keys = append(keys, s.key)
	}
	set := takenSet(keys...)

	for _, s := range seeded {
		t.Run(s.key, func(t *testing.T) {
			first := newRoleKey(set, s.title)
			if set[first] {
				t.Errorf("newRoleKey(seed, %q) = %q, which is already a seeded key", s.title, first)
			}
			// No seeded title slugifies to a seeded key, so the assertion above alone survives
			// an implementation that ignores `taken` entirely. Minting the same title twice is
			// what forces the collision the seed itself never supplies.
			again := takenSet(keys...)
			again[first] = true
			if second := newRoleKey(again, s.title); second == first || again[second] {
				t.Errorf("newRoleKey(seed+%q, %q) = %q, want a key clear of both", first, s.title, second)
			}
		})
	}
}

// TestWorkflowRole_RederivingOnSaveWouldMoveTheKey covers the half of
// roles.test.ts:833-841 a pure function can carry: re-deriving from an UNCHANGED
// title still moves the key, because the stored key is itself in the taken-set. That
// a rename never calls the port at all is UpdateRole's property, proven in subtask 04.
func TestWorkflowRole_RederivingOnSaveWouldMoveTheKey(t *testing.T) {
	const stored, title = "engagement-partner", "Engagement Partner"
	if got := newRoleKey(takenSet(stored), title); got == stored {
		t.Errorf("newRoleKey({%q}, %q) = %q — a re-derive on save must be observable, never identity", stored, title, got)
	}
}

// TestWorkflowRole_NilTakenSetBehavesAsEmpty: the store's key query can return zero
// rows and leave `taken` nil. Reading a nil map is legal Go; pinned so a defensive
// rewrite cannot start panicking on a tenant's first role.
func TestWorkflowRole_NilTakenSetBehavesAsEmpty(t *testing.T) {
	var taken map[string]bool
	if got := newRoleKey(taken, "Engagement Partner"); got != "engagement-partner" {
		t.Errorf("newRoleKey(nil, \"Engagement Partner\") = %q, want %q", got, "engagement-partner")
	}
}

// TestWorkflowRole_DeletedKeyIsNeverReused proves what the port can prove: a key in
// the taken-set is never re-minted, whether it is live or soft-deleted. That the set
// arrives carrying soft-deleted keys is the store's SELECT (no deleted_at filter),
// proven in subtask 03.
func TestWorkflowRole_DeletedKeyIsNeverReused(t *testing.T) {
	cases := []struct {
		name  string
		taken []string
		title string
		want  string
	}{
		{"a soft-deleted key still blocks its own slug", []string{"engagement-partner"}, "Engagement Partner", "engagement-partner-2"},
		{"a soft-deleted suffix is walked past, not filled", []string{"engagement-partner", "engagement-partner-2", "engagement-partner-3"}, "Engagement Partner", "engagement-partner-4"},
		// The walk returns the FIRST free suffix, so a set missing a middle key is filled
		// rather than appended past. A highest-taken+1 port passes every other row here.
		{"a gap in the taken-set is filled, not appended past", []string{"engagement-partner", "engagement-partner-3"}, "Engagement Partner", "engagement-partner-2"},
		{"the fallback key is blocked the same way", []string{"role"}, "🎉", "role-2"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			set := takenSet(tc.taken...)
			got := newRoleKey(set, tc.title)
			if got != tc.want {
				t.Errorf("newRoleKey(%v, %q) = %q, want %q", tc.taken, tc.title, got, tc.want)
			}
			if set[got] {
				t.Errorf("newRoleKey(%v, %q) re-minted %q, a key already taken", tc.taken, tc.title, got)
			}
		})
	}
}

// TestWorkflowRole_RoleMarshalsFourKeys asserts on RAW BYTES on purpose: json.Unmarshal
// turns both `[]` and `null` into the same nil slice, so a decoded assertion passes
// against exactly the bug under test (a Go []T without omitempty rendering null).
func TestWorkflowRole_RoleMarshalsFourKeys(t *testing.T) {
	cases := []struct {
		name        string
		members     []string
		wantMembers string
	}{
		{"the contract: an empty members marshals to an empty array", []string{}, `"members":[]`},
		// The M4-16 defect, and the reason every producer normalises nil to []string{} inline.
		{"the hazard: a nil members marshals to null", nil, `"members":null`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			raw, err := json.Marshal(Role{Key: "k", Title: "t", Desc: "", Members: tc.members})
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			wire := string(raw)

			if !strings.Contains(wire, tc.wantMembers) {
				t.Errorf("wire = %s, want it to contain %s", wire, tc.wantMembers)
			}
			// The key's own presence, separately: an omitempty regression drops it entirely,
			// which satisfies a bare "does not contain null" assertion.
			if !strings.Contains(wire, `"members"`) {
				t.Errorf("wire = %s, dropped the members key entirely — members carries no omitempty", wire)
			}
			if !strings.Contains(wire, `"desc":""`) {
				t.Errorf("wire = %s, want an empty desc present rather than omitted", wire)
			}

			var keys map[string]json.RawMessage
			if err := json.Unmarshal(raw, &keys); err != nil {
				t.Fatalf("decode %s: %v", wire, err)
			}
			got := make([]string, 0, len(keys))
			for k := range keys {
				got = append(got, k)
			}
			sort.Strings(got)
			if want := "desc,key,members,title"; strings.Join(got, ",") != want {
				t.Errorf("key set = %s, want %s", strings.Join(got, ","), want)
			}
			// Named separately: `description` is the column name and the likely slip.
			for _, forbidden := range []string{"description", "id"} {
				if _, ok := keys[forbidden]; ok {
					t.Errorf("wire = %s, must not carry %q", wire, forbidden)
				}
			}
		})
	}
}

// TestWorkflowRole_StatusForErrTable: each sentinel maps to its status and its
// hand-written message, wrapped or bare, and no message leaks the "approval: "
// prefix the SPA would render as the reason.
func TestWorkflowRole_StatusForErrTable(t *testing.T) {
	cases := []struct {
		err     error
		want    int
		wantMsg string
	}{
		{db.ErrNoTenant, http.StatusUnauthorized, "unauthorized"},
		{ErrValidation, http.StatusBadRequest, "invalid request"},
		{ErrNotPermitted, http.StatusForbidden, "only an admin can change workflow roles"},
		{ErrNotFound, http.StatusNotFound, "workflow role not found"},
		{ErrConflict, http.StatusConflict, "that role was just created — try again"},
		{errors.New("boom"), http.StatusInternalServerError, "internal server error"},
	}
	for _, tc := range cases {
		for _, wrapped := range []bool{false, true} {
			err, name := tc.err, tc.err.Error()
			if wrapped {
				err, name = fmt.Errorf("store: %w", tc.err), name+" (wrapped)"
			}
			t.Run(name, func(t *testing.T) {
				status, msg := statusForErr(err)
				if status != tc.want {
					t.Errorf("status = %d, want %d", status, tc.want)
				}
				if msg != tc.wantMsg {
					t.Errorf("msg = %q, want %q", msg, tc.wantMsg)
				}
				if strings.Contains(msg, "approval: ") {
					t.Errorf("msg %q leaks the sentinel prefix to the SPA", msg)
				}
			})
		}
	}
}

package approval

// resolveHolder/inspectorResolveHolder oracle: frontend/app/src/lib/roles.ts:104-133.
// holderName oracle: members.ts:546. roleTitle oracle: roles.ts:63. Pure tests --
// never call dbTestPools, so they cannot skip.

import (
	"encoding/json"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"
)

func TestRunReadModel_MarshalsEmptyCollectionsAsArrays(t *testing.T) {
	run := Run{RunID: "11111111-1111-1111-1111-111111111111", State: "open", OpenedAt: time.Now()}
	b, err := json.Marshal(run)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	body := string(b)
	if !strings.Contains(body, `"steps":[]`) {
		t.Errorf("want steps:[], got %s", body)
	}
	if !strings.Contains(body, `"decisions":[]`) {
		t.Errorf("want decisions:[], got %s", body)
	}
	if strings.Contains(body, `"steps":null`) || strings.Contains(body, `"decisions":null`) {
		t.Errorf("nil collections must never marshal as null: %s", body)
	}
}

func TestRunReadModel_StepKeySetIsFixed(t *testing.T) {
	kinds := []string{"approval", "notify", "autoapprove"}
	var keySets [][]string
	for _, kind := range kinds {
		b, err := json.Marshal(RunStep{Kind: kind})
		if err != nil {
			t.Fatalf("Marshal(%s): %v", kind, err)
		}
		var m map[string]json.RawMessage
		if err := json.Unmarshal(b, &m); err != nil {
			t.Fatalf("Unmarshal(%s): %v", kind, err)
		}
		keys := make([]string, 0, len(m))
		for k := range m {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		if len(keys) != 13 {
			t.Errorf("kind=%s: want 13 keys, got %d: %v", kind, len(keys), keys)
		}
		keySets = append(keySets, keys)
	}
	for i := 1; i < len(keySets); i++ {
		if !reflect.DeepEqual(keySets[0], keySets[i]) {
			t.Errorf("kind=%s key set differs from kind=%s: %v vs %v", kinds[i], kinds[0], keySets[i], keySets[0])
		}
	}
}

func TestResolveHolder_MissingRole(t *testing.T) {
	got := resolveHolder(false, nil)
	want := Resolved{Text: "Role no longer exists", Warn: true}
	if got != want {
		t.Errorf("resolveHolder(false, nil) = %+v, want %+v", got, want)
	}
}

func TestResolveHolder_NoHolders(t *testing.T) {
	got := resolveHolder(true, nil)
	want := Resolved{Text: "Nobody assigned", Warn: true}
	if got != want {
		t.Errorf("resolveHolder(true, nil) = %+v, want %+v", got, want)
	}
}

func TestResolveHolder_BlockedNamesFirstHolderNotFirstActive(t *testing.T) {
	holders := []holderInput{
		{Name: "Halima Yusuf", Status: "suspended", AccessRole: "reviewer"},
		{Name: "Folake Adesina", Status: "active", AccessRole: "preparer"},
	}
	got := resolveHolder(true, holders)
	want := Resolved{Text: "Halima Yusuf +1", Warn: true}
	if got != want {
		t.Errorf("resolveHolder(true, %+v) = %+v, want %+v", holders, got, want)
	}
}

func TestResolveHolder_OkCountsSuspendedInTheExtra(t *testing.T) {
	holders := []holderInput{
		{Name: "Musa Danjuma", Status: "active", AccessRole: "reviewer"},
		{Name: "Halima Yusuf", Status: "suspended", AccessRole: "reviewer"},
	}
	got := resolveHolder(true, holders)
	want := Resolved{Text: "Musa Danjuma +1", Warn: false}
	if got != want {
		t.Errorf("resolveHolder(true, %+v) = %+v, want %+v", holders, got, want)
	}
}

func TestResolveHolder_SingleHolderHasNoPlusN(t *testing.T) {
	holders := []holderInput{{Name: "Musa Danjuma", Status: "active", AccessRole: "reviewer"}}
	got := resolveHolder(true, holders)
	want := Resolved{Text: "Musa Danjuma", Warn: false}
	if got != want {
		t.Errorf("resolveHolder(true, %+v) = %+v, want %+v", holders, got, want)
	}
}

func TestResolveHolder_PreparerHolderIsNotEligible(t *testing.T) {
	// Q2: staffing a preparer into a role is legal; it just can never satisfy the step.
	holders := []holderInput{{Name: "Folake Adesina", Status: "active", AccessRole: "preparer"}}
	got := resolveHolder(true, holders)
	want := Resolved{Text: "Folake Adesina", Warn: true}
	if got != want {
		t.Errorf("resolveHolder(true, %+v) = %+v, want %+v", holders, got, want)
	}
}

func TestInspectorResolveHolder_FourArms(t *testing.T) {
	cases := []struct {
		name       string
		roleExists bool
		holders    []holderInput
		want       Resolved
	}{
		{"missing", false, nil, Resolved{Text: "Role no longer exists", Warn: true}},
		{"none", true, nil, Resolved{Text: "Nobody holds this role — this step will block", Warn: true}},
		{"blocked", true,
			[]holderInput{{Name: "Halima Yusuf", Status: "suspended", AccessRole: "reviewer"}},
			Resolved{Text: "Currently: Halima Yusuf — this step will block", Warn: true}},
		{"ok", true,
			[]holderInput{{Name: "Musa Danjuma", Status: "active", AccessRole: "reviewer"}},
			Resolved{Text: "Currently: Musa Danjuma", Warn: false}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := inspectorResolveHolder(c.roleExists, c.holders)
			if got != c.want {
				t.Errorf("inspectorResolveHolder(%v, %+v) = %+v, want %+v", c.roleExists, c.holders, got, c.want)
			}
		})
	}
}

func TestInspectorResolveHolder_OmitsThePlusNSuffix(t *testing.T) {
	holders := []holderInput{
		{Name: "Musa Danjuma", Status: "active", AccessRole: "reviewer"},
		{Name: "Halima Yusuf", Status: "suspended", AccessRole: "reviewer"},
	}
	gotInspector := inspectorResolveHolder(true, holders)
	wantInspector := Resolved{Text: "Currently: Musa Danjuma", Warn: false}
	if gotInspector != wantInspector {
		t.Errorf("inspectorResolveHolder(true, %+v) = %+v, want %+v", holders, gotInspector, wantInspector)
	}

	gotCard := resolveHolder(true, holders)
	wantCard := Resolved{Text: "Musa Danjuma +1", Warn: false}
	if gotCard != wantCard {
		t.Errorf("resolveHolder(true, %+v) = %+v, want %+v (card DOES carry +N)", holders, gotCard, wantCard)
	}
}

func TestHolderName_FallsBackDisplayNameThenEmailThenSubject(t *testing.T) {
	cases := []struct {
		name        string
		displayName *string
		email       *string
		userID      string
		want        string
	}{
		{"display name set", ptr("Musa Danjuma"), ptr("musa@example.com"), "user-1", "Musa Danjuma"},
		{"display name null, email present", nil, ptr("halima@example.com"), "user-2", "halima@example.com"},
		{"both null", nil, nil, "user-3", "user-3"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := holderName(c.displayName, c.email, c.userID)
			if got != c.want {
				t.Errorf("holderName(%v, %v, %q) = %q, want %q", c.displayName, c.email, c.userID, got, c.want)
			}
		})
	}
}

func TestRoleTitle_DeletedRoleFallback(t *testing.T) {
	if got, want := roleTitle(false, ""), "Deleted role"; got != want {
		t.Errorf("roleTitle(false, \"\") = %q, want %q", got, want)
	}
	// Positive pair: a live role's title passes through unchanged.
	if got, want := roleTitle(true, "Reviewer"), "Reviewer"; got != want {
		t.Errorf("roleTitle(true, \"Reviewer\") = %q, want %q", got, want)
	}
}

// TestIsApprover_MatchesInvoiceStoreAndRolesTs pins this package's copy against the other
// two (internal/invoice/store.go:1412, roles.ts:405-407) so the three cannot drift silently.
// Literals transcribed independently, not read back from isApprover itself.
func TestIsApprover_MatchesInvoiceStoreAndRolesTs(t *testing.T) {
	cases := []struct {
		accessRole string
		want       bool
	}{
		{"admin", true},
		{"reviewer", true},
		{"preparer", false},
		{"", false},
		{"owner", false},
	}
	for _, c := range cases {
		if got := isApprover(c.accessRole); got != c.want {
			t.Errorf("isApprover(%q) = %v, want %v", c.accessRole, got, c.want)
		}
	}
}

// QA mutation pass: every existing "ok"-arm fixture happens to have active[0] == holders[0],
// so swapping active[0] for holders[0] in resolution() went undetected. This fixture puts the
// eligible holder second.
func TestResolution_OkPrimaryIsFirstActiveNotFirstHolder(t *testing.T) {
	holders := []holderInput{
		{Name: "Halima Yusuf", Status: "suspended", AccessRole: "reviewer"},
		{Name: "Musa Danjuma", Status: "active", AccessRole: "reviewer"},
	}
	got := resolveHolder(true, holders)
	want := Resolved{Text: "Musa Danjuma +1", Warn: false}
	if got != want {
		t.Errorf("resolveHolder(true, %+v) = %+v, want %+v", holders, got, want)
	}

	gotInspector := inspectorResolveHolder(true, holders)
	wantInspector := Resolved{Text: "Currently: Musa Danjuma", Warn: false}
	if gotInspector != wantInspector {
		t.Errorf("inspectorResolveHolder(true, %+v) = %+v, want %+v", holders, gotInspector, wantInspector)
	}
}

// QA mutation pass: TestRunReadModel_StepKeySetIsFixed only observes JSON output, so
// omitempty on a field the fixture always sets non-empty (e.g. Kind) went undetected.
// Assert directly against the struct tags -- AC-1's "no omitempty on any field".
func TestWireStructs_NoFieldCarriesOmitempty(t *testing.T) {
	for _, v := range []any{Resolved{}, RunStep{}, RunDecision{}, Run{}} {
		typ := reflect.TypeOf(v)
		for i := 0; i < typ.NumField(); i++ {
			tag := typ.Field(i).Tag.Get("json")
			if strings.Contains(tag, "omitempty") {
				t.Errorf("%s.%s: json tag %q carries omitempty, want none", typ.Name(), typ.Field(i).Name, tag)
			}
		}
	}
}

// TestRunReadModel_StepKeySetIsFixed only checks the key COUNT is stable across kinds; this
// pins the actual 13 names against §2.3 so a renamed (not just added/omitted) key is caught.
func TestRunStep_KeyNamesMatchSpec(t *testing.T) {
	b, err := json.Marshal(RunStep{Kind: "approval"})
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	want := []string{"ord", "kind", "state", "workflow_role_key", "workflow_role_title", "holder",
		"sla_hours", "due_at", "overdue", "satisfied_at", "satisfied_by", "notify_target", "notify_channel"}
	for _, k := range want {
		if _, ok := m[k]; !ok {
			t.Errorf("missing key %q in %v", k, m)
		}
	}
	if len(m) != len(want) {
		t.Errorf("got %d keys, want %d: %v", len(m), len(want), m)
	}
}

func TestRunDecision_KeyNamesMatchSpec(t *testing.T) {
	b, err := json.Marshal(RunDecision{})
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	want := []string{"run_step_id", "ord", "decision", "actor", "decided_at", "reason"}
	for _, k := range want {
		if _, ok := m[k]; !ok {
			t.Errorf("missing key %q in %v", k, m)
		}
	}
	if len(m) != len(want) {
		t.Errorf("got %d keys, want %d: %v", len(m), len(want), m)
	}
}

func TestRun_KeyNamesMatchSpec(t *testing.T) {
	b, err := json.Marshal(Run{})
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	want := []string{"run_id", "state", "opened_at", "closed_at", "closed_by", "steps", "decisions"}
	for _, k := range want {
		if _, ok := m[k]; !ok {
			t.Errorf("missing key %q in %v", k, m)
		}
	}
	if len(m) != len(want) {
		t.Errorf("got %d keys, want %d: %v", len(m), len(want), m)
	}
}

// A role can have holders yet still be unsatisfiable -- every one of them suspended, not just
// outranked by access role. Distinguishes "no eligible holder" from "no holder at all".
func TestResolveHolder_AllHoldersSuspended(t *testing.T) {
	holders := []holderInput{
		{Name: "Halima Yusuf", Status: "suspended", AccessRole: "reviewer"},
		{Name: "Musa Danjuma", Status: "suspended", AccessRole: "admin"},
	}
	got := resolveHolder(true, holders)
	want := Resolved{Text: "Halima Yusuf +1", Warn: true}
	if got != want {
		t.Errorf("resolveHolder(true, %+v) = %+v, want %+v", holders, got, want)
	}
}

// roles.ts/members.ts use `??` (nullish coalescing): only null/undefined falls through, an
// empty string does not. The Go port must agree -- a non-nil pointer to "" stops the ladder.
func TestHolderName_EmptyStringDisplayNameDoesNotFallThrough(t *testing.T) {
	got := holderName(ptr(""), ptr("halima@example.com"), "user-2")
	want := ""
	if got != want {
		t.Errorf("holderName(ptr(\"\"), ...) = %q, want %q (empty string must not fall through to email)", got, want)
	}
}

// Unicode and a long name must survive the "%s +%d" formatting unmangled.
func TestResolveHolder_UnicodeAndVeryLongNameInPlusNFormatting(t *testing.T) {
	longName := strings.Repeat("Adéọlá ", 40) + "Bàbáyẹmi"
	holders := []holderInput{
		{Name: longName, Status: "active", AccessRole: "reviewer"},
		{Name: "Ha", Status: "suspended", AccessRole: "reviewer"},
	}
	got := resolveHolder(true, holders)
	want := Resolved{Text: longName + " +1", Warn: false}
	if got != want {
		t.Errorf("resolveHolder with long unicode name: got %+v, want %+v", got, want)
	}
}

// extra counts every OTHER holder even when all of them (not just the primary) are eligible --
// existing fixtures only ever exercise extra=0 or extra=1.
func TestResolution_ExtraCountsAllOtherHoldersEvenWhenEveryoneEligible(t *testing.T) {
	holders := []holderInput{
		{Name: "Musa Danjuma", Status: "active", AccessRole: "reviewer"},
		{Name: "Halima Yusuf", Status: "active", AccessRole: "admin"},
		{Name: "Folake Adesina", Status: "active", AccessRole: "reviewer"},
	}
	got := resolveHolder(true, holders)
	want := Resolved{Text: "Musa Danjuma +2", Warn: false}
	if got != want {
		t.Errorf("resolveHolder(true, %+v) = %+v, want %+v", holders, got, want)
	}
}

// A non-nil but empty holders slice must behave identically to nil -- resolution() must not
// branch on nil-ness rather than length.
func TestResolveHolder_EmptyNonNilHoldersSliceSameAsNil(t *testing.T) {
	got := resolveHolder(true, []holderInput{})
	want := Resolved{Text: "Nobody assigned", Warn: true}
	if got != want {
		t.Errorf("resolveHolder(true, []holderInput{}) = %+v, want %+v", got, want)
	}
}

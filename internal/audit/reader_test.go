// Test-first (RED) suite for AUDIT-04-01: the pure-Go reader types and the keyset
// cursor codec. No test here touches a database — TestMain (audit_test.go) runs
// unconditionally, and none of these cases calls requireFixture.
package audit_test

import (
	"encoding/base64"
	"encoding/json"
	"math"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/SimonOsipov/invoice-os/internal/audit"
)

// --- AC #1, #2: the cursor codec -----------------------------------------------------

// AC #1: encode then decode round-trips both fields, including sub-microsecond
// precision and the full int64 range. Comparing time.Time via .Format(RFC3339Nano)
// rather than == or reflect.DeepEqual, because Parse returns a different Location
// (and no monotonic reading) even when the instant is identical.
func TestAuditCursor_RoundTripsNanosAndFullInt64Range(t *testing.T) {
	cases := []struct {
		name      string
		createdAt time.Time
		id        int64
	}{
		{
			name:      "nanosecond precision",
			createdAt: time.Date(2026, 8, 22, 9, 14, 3, 221041987, time.UTC),
			id:        42,
		},
		{
			name:      "MaxInt64",
			createdAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
			id:        math.MaxInt64,
		},
		{
			name:      "MinInt64",
			createdAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
			id:        math.MinInt64,
		},
	}
	if len(cases) == 0 {
		t.Fatal("test table is empty")
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			enc := audit.EncodeCursor(tc.createdAt, tc.id)
			got, err := audit.DecodeCursor(enc)
			if err != nil {
				t.Fatalf("DecodeCursor(%q) returned error %v, want nil", enc, err)
			}
			if got.ID != tc.id {
				t.Errorf("id = %d, want %d", got.ID, tc.id)
			}
			wantTS := tc.createdAt.Format(time.RFC3339Nano)
			gotTS := got.CreatedAt.Format(time.RFC3339Nano)
			if gotTS != wantTS {
				t.Errorf("created_at = %s, want %s", gotTS, wantTS)
			}
		})
	}
}

// readerMalformedCursors is the shared table for AC #2's two tests: every case must
// make DecodeCursor return a non-nil error, and none may return a zero Cursor with a
// nil error (the silent-first-page failure). Reused rather than duplicated so both
// tests exercise exactly the same seven shapes.
var readerMalformedCursors = []struct {
	name  string
	input string
}{
	{"empty string", ""},
	{"invalid base64", "!!!not-valid-base64$$$"},
	{"zero separators", base64.RawURLEncoding.EncodeToString([]byte("no-pipe-in-this-body"))},
	{"two separators", base64.RawURLEncoding.EncodeToString([]byte("2026-08-22T09:14:03.221041Z|123|456"))},
	{"unparseable timestamp", base64.RawURLEncoding.EncodeToString([]byte("not-a-timestamp|123"))},
	{"non-numeric id", base64.RawURLEncoding.EncodeToString([]byte("2026-08-22T09:14:03.221041Z|abc"))},
	{"id overflows int64", base64.RawURLEncoding.EncodeToString([]byte("2026-08-22T09:14:03.221041Z|99999999999999999999"))},
}

// AC #2: every malformed shape is rejected with a non-nil error.
func TestAuditCursor_DecodeRejectsEveryMalformedShape(t *testing.T) {
	if len(readerMalformedCursors) != 7 {
		t.Fatalf("malformed-cursor table holds %d cases, want 7", len(readerMalformedCursors))
	}

	for _, tc := range readerMalformedCursors {
		t.Run(tc.name, func(t *testing.T) {
			_, err := audit.DecodeCursor(tc.input)
			if err == nil {
				t.Errorf("DecodeCursor(%q) returned nil error, want non-nil", tc.input)
			}
		})
	}
}

// AC #2: no malformed shape may return a zero-value Cursor together with a nil error
// — that combination reads as a valid "first page" cursor instead of the parse
// failure it actually is.
func TestAuditCursor_DecodeNeverReturnsAZeroCursorWithNilError(t *testing.T) {
	if len(readerMalformedCursors) != 7 {
		t.Fatalf("malformed-cursor table holds %d cases, want 7", len(readerMalformedCursors))
	}

	for _, tc := range readerMalformedCursors {
		t.Run(tc.name, func(t *testing.T) {
			got, err := audit.DecodeCursor(tc.input)
			if err == nil && got == (audit.Cursor{}) {
				t.Errorf("DecodeCursor(%q) = (zero Cursor, nil) — the silent-first-page failure", tc.input)
			}
		})
	}
}

// --- AC #3: CompanyFilter's three states ----------------------------------------------

// AC #3: each constructor produces a distinct, inspectable state; Named carries the id
// it was given, Workspace carries none. There is no fourth constructor and no way to
// combine Named and Workspace in one value.
func TestAuditCompanyFilter_HasExactlyThreeStates(t *testing.T) {
	const namedID = "16903c18-de65-4b22-a5e8-3ab476b3bead"
	cases := []struct {
		name     string
		filter   audit.CompanyFilter
		wantMode audit.CompanyMode
		wantID   string
	}{
		{"all companies", audit.AllCompanies(), audit.ModeAllCompanies, ""},
		{"named company", audit.NamedCompany(namedID), audit.ModeNamedCompany, namedID},
		{"workspace only", audit.WorkspaceOnly(), audit.ModeWorkspaceOnly, ""},
	}
	if len(cases) == 0 {
		t.Fatal("test table is empty")
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.filter.Mode(); got != tc.wantMode {
				t.Errorf("Mode() = %v, want %v", got, tc.wantMode)
			}
			if got := tc.filter.ID(); got != tc.wantID {
				t.Errorf("ID() = %q, want %q", got, tc.wantID)
			}
		})
	}
}

// --- AC #4: ScopeOf and CompanyScope ---------------------------------------------------

// readerFirmWideEvents is this test's own copy of the twelve firm-wide names (System
// Design §2, verbatim — not the contract doc's incomplete prose enumeration, D-23).
// Deliberately a second literal, distinct from reader.go's unexported firmWideEvents:
// the point of this test is to pin ScopeOf's behavior against the spec, not against
// its own implementation.
var readerFirmWideEvents = []string{
	"approval_policy.created", "approval_policy.updated",
	"approval_policy.published", "approval_policy.deleted",
	"workflow_role.created", "workflow_role.updated",
	"workflow_role.deleted", "workflow_role.staffed",
	"membership.suspended", "membership.reactivated",
	"validation.rule.enabled", "validation.rule.disabled",
}

// readerDocumentEvents are the three document.* names the SQL resolver also leaves
// NULL, but which ScopeOf must classify unattributed, not workspace (D-28).
var readerDocumentEvents = []string{"document.created", "document.reused", "document.read"}

// AC #4: a set entity_id always wins to company; each of the twelve firm-wide names
// with a null entity_id is workspace; each document.* name and an invoice-scoped name
// with a null entity_id is unattributed.
func TestAuditScopeOf_ClassifiesAllThreeStates(t *testing.T) {
	entity := uuid.NewString()

	type tc struct {
		name     string
		event    string
		entityID *string
		want     audit.CompanyScope
	}
	var cases []tc
	cases = append(cases, tc{"a set entity_id", "invoice.approval_approved", &entity, audit.ScopeCompany})
	for _, e := range readerFirmWideEvents {
		cases = append(cases, tc{"firm-wide: " + e, e, nil, audit.ScopeWorkspace})
	}
	for _, e := range readerDocumentEvents {
		cases = append(cases, tc{"document: " + e, e, nil, audit.ScopeUnattributed})
	}
	cases = append(cases, tc{"invoice-scoped, unresolved", "invoice.created", nil, audit.ScopeUnattributed})

	if len(cases) == 0 {
		t.Fatal("test table is empty")
	}

	// population floor: the table must actually exercise all twelve firm-wide names,
	// or a shrinking table would pass by covering fewer than the spec requires.
	covered := map[string]bool{}
	for _, c := range cases {
		covered[c.event] = true
	}
	missing := 0
	for _, e := range readerFirmWideEvents {
		if !covered[e] {
			missing++
		}
	}
	if missing != 0 {
		t.Fatalf("table is missing %d of %d firm-wide events", missing, len(readerFirmWideEvents))
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := audit.ScopeOf(c.event, c.entityID); got != c.want {
				t.Errorf("ScopeOf(%q, entityID set=%v) = %q, want %q", c.event, c.entityID != nil, got, c.want)
			}
		})
	}
}

// AC #4: an event in neither the firm-wide set nor a known invoice-scoped/document
// list falls back to unattributed, never workspace — the fail-safe direction (D-28):
// an event nobody classified must read "we do not know", not "this was firm-wide".
func TestAuditScopeOf_UnknownEventFallsBackToUnattributedNotWorkspace(t *testing.T) {
	got := audit.ScopeOf("some.entirely.unclassified.event", nil)
	if got != audit.ScopeUnattributed {
		t.Errorf("ScopeOf(unknown event, nil) = %q, want %q", got, audit.ScopeUnattributed)
	}
}

// AC #4 drift guard: reuses audit_trigger_test.go's own triggerRuleDPayloads (already
// DB-pinned at 17 rows by TestAudit_InsertTriggerLeavesWorkspaceEventsNull) instead of
// a fifth hand-written copy of the list. If rule-D ever grows an 18th event that is
// neither firm-wide nor already listed below, ScopeOf's fallback makes this fail rather
// than silently pass, forcing a human to classify it (D-28's fail-safe intent).
//
// For the five unattributed names this is a DRIFT GUARD, not evidence of their scope:
// rule 3 answers identically for extraction.succeeded and for an event nobody ever named
// (TestAuditScopeOf_UnknownEventFallsBackToUnattributedNotWorkspace pins that fallback).
func TestAuditScopeOf_MatchesTheSeventeenEventsTheTriggerLeavesNull(t *testing.T) {
	ruleD := triggerRuleDPayloads(uuid.NewString())
	if len(ruleD) != 17 {
		t.Fatalf("rule-D payload map holds %d events, want 17", len(ruleD))
	}

	// The rule-D names ScopeOf must call unattributed rather than workspace.
	unattributedEvents := map[string]bool{
		"document.created":     true,
		"document.reused":      true,
		"document.read":        true,
		"extraction.succeeded": true,
		"extraction.failed":    true,
	}
	found := 0
	for event := range ruleD {
		want := audit.ScopeWorkspace
		if unattributedEvents[event] {
			want = audit.ScopeUnattributed
			found++
		}
		if got := audit.ScopeOf(event, nil); got != want {
			t.Errorf("ScopeOf(%q, nil) = %q, want %q", event, got, want)
		}
	}
	// Control needle: without this, a rule-D that shrank to zero unattributed events
	// would make the loop above pass by finding nothing to disagree with.
	if found != 5 {
		t.Fatalf("control needle: found %d of 5 unattributed events in rule-D, want 5", found)
	}
}

// AC #4: cross-checks the four DB-pinned rule sets (10+7+4+17=38) for pairwise
// disjointness and total. audit_trigger_test.go pins each set's size alone but never
// cross-checks them — an invariant CompanyScope's three-way split now depends on. Needs
// no audit package call: it guards the fixture data itself. Disjointness is a real
// structural property, not a restatement of ScopeOf's fallback.
func TestAuditScopeOf_RuleSetsAreDisjointAndSumToThirtyEight(t *testing.T) {
	all := map[string]string{}
	named := []struct {
		name   string
		events []string
	}{
		{"A", triggerRuleAEvents},
		{"B", triggerRuleBEvents},
		{"C", triggerRuleCEvents},
	}
	for _, s := range named {
		for _, e := range s.events {
			if prior, dup := all[e]; dup {
				t.Fatalf("%q is in both rule %s and rule %s", e, prior, s.name)
			}
			all[e] = s.name
		}
	}
	for e := range triggerRuleDPayloads(uuid.NewString()) {
		if prior, dup := all[e]; dup {
			t.Fatalf("%q is in both rule %s and rule D", e, prior)
		}
		all[e] = "D"
	}
	if len(all) != 38 {
		t.Fatalf("rule sets total %d events, want 38", len(all))
	}
}

// AC #4: ScopeOf answers workspace for exactly the twelve firm-wide names and for no
// other event in the shipped 38-name vocabulary. TestAuditScopeOf_ClassifiesAllThreeStates
// checks the twelve one at a time; this is the closed half, and it is the one assertion
// here that a fallback cannot satisfy — reaching ScopeWorkspace needs a real entry in
// reader.go's firmWideEvents. It fails if either extraction event is ever filed as
// firm-wide instead of unattributed.
func TestAuditScopeOf_WorkspaceAnswerIsExactlyTheTwelveFirmWideNames(t *testing.T) {
	vocabulary := map[string]bool{}
	for _, set := range [][]string{triggerRuleAEvents, triggerRuleBEvents, triggerRuleCEvents} {
		for _, e := range set {
			vocabulary[e] = true
		}
	}
	for e := range triggerRuleDPayloads(uuid.NewString()) {
		vocabulary[e] = true
	}
	// Population floor: a shrunk vocabulary would make the set equality below a claim
	// about whatever happened to survive.
	if len(vocabulary) != 38 {
		t.Fatalf("the four rule sets name %d events, want 38", len(vocabulary))
	}

	want := map[string]bool{}
	for _, e := range readerFirmWideEvents {
		want[e] = true
	}
	if len(want) != 12 {
		t.Fatalf("readerFirmWideEvents holds %d distinct names, want 12", len(want))
	}

	got := map[string]bool{}
	for e := range vocabulary {
		if audit.ScopeOf(e, nil) == audit.ScopeWorkspace {
			got[e] = true
		}
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("ScopeOf answers workspace for %v, want exactly the twelve firm-wide names %v", got, want)
	}
}

// --- AC #5: no omitempty on a Response/Facets slice field ------------------------------

// AC #5: reflects over Response and Facets; no slice-typed field may carry omitempty
// (D-9: the store, not the handler, coerces nil to make(…, 0, n), and omitempty would
// hide a coercion bug instead of letting the field marshal visibly null). This does not
// touch Facet.Kind, a scalar field that deliberately does carry omitempty.
func TestAuditResponse_SliceFieldsHaveNoOmitempty(t *testing.T) {
	types := []any{audit.Response{}, audit.Facets{}}
	scanned := 0
	for _, v := range types {
		rt := reflect.TypeOf(v)
		for i := 0; i < rt.NumField(); i++ {
			f := rt.Field(i)
			if f.Type.Kind() != reflect.Slice {
				continue
			}
			scanned++
			tag := f.Tag.Get("json")
			if strings.Contains(tag, "omitempty") {
				t.Errorf("%s.%s carries omitempty on a slice field (tag %q)", rt.Name(), f.Name, tag)
			}
		}
	}
	if scanned == 0 {
		t.Fatal("scanned no slice fields on Response/Facets — the test found nothing to assert over")
	}
}

// --- QA (Stage 4) adversarial coverage --------------------------------------------------

// readerPtr returns a pointer to v — avoids taking the address of a literal.
func readerPtr[T any](v T) *T { return &v }

// AC #1: extends the round-trip table with cases the original three didn't cover — a
// far-future and far-past timestamp, the zero time, a non-UTC location and a negative
// id. A non-UTC location round-trips the instant correctly but not the named zone (RFC
// 3339 only carries a numeric offset), which is fine: ordering only needs the instant.
func TestAuditCursor_RoundTripsFarBoundaryTimesAndNonUTCLocation(t *testing.T) {
	cases := []struct {
		name      string
		createdAt time.Time
		id        int64
	}{
		{"far future", time.Date(9999, 12, 31, 23, 59, 59, 999999999, time.UTC), 1},
		{"far past, year 1", time.Date(1, 1, 1, 0, 0, 0, 0, time.UTC), 2},
		{"zero time", time.Time{}, 0},
		{"non-UTC fixed zone", time.Date(2026, 8, 22, 9, 14, 3, 221041987, time.FixedZone("EST", -5*3600)), 3},
		{"negative id", time.Date(2026, 8, 22, 9, 14, 3, 0, time.UTC), -12345},
	}
	if len(cases) == 0 {
		t.Fatal("test table is empty")
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			enc := audit.EncodeCursor(tc.createdAt, tc.id)
			got, err := audit.DecodeCursor(enc)
			if err != nil {
				t.Fatalf("DecodeCursor(%q) returned error %v, want nil", enc, err)
			}
			if got.ID != tc.id {
				t.Errorf("id = %d, want %d", got.ID, tc.id)
			}
			wantTS := tc.createdAt.Format(time.RFC3339Nano)
			gotTS := got.CreatedAt.Format(time.RFC3339Nano)
			if gotTS != wantTS {
				t.Errorf("created_at = %s, want %s", gotTS, wantTS)
			}
		})
	}
}

// AC #4: exact-match only. A name that is a superstring of a firm-wide name, or one that
// differs only in casing, must classify unattributed like any other unknown event — the
// control needle (the real firm-wide name) proves the lookup itself still matches.
func TestAuditScopeOf_PrefixCasingAndEmptyEventDoNotMatchFirmWide(t *testing.T) {
	cases := []struct {
		name  string
		event string
		want  audit.CompanyScope
	}{
		{"empty event string", "", audit.ScopeUnattributed},
		{"superstring of a firm-wide name", "approval_policy.createdX", audit.ScopeUnattributed},
		{"different casing of a firm-wide name", "Approval_Policy.Created", audit.ScopeUnattributed},
		{"control needle: the real firm-wide name matches", "approval_policy.created", audit.ScopeWorkspace},
	}
	if len(cases) == 0 {
		t.Fatal("test table is empty")
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := audit.ScopeOf(c.event, nil); got != c.want {
				t.Errorf("ScopeOf(%q, nil) = %q, want %q", c.event, got, c.want)
			}
		})
	}
}

// AC #4 boundary: ScopeOf only checks entityID's nilness, not its value — a pointer to
// an empty string counts as "set" and returns company, the same as a real id would. This
// pins current behavior, not a guarantee: Event.EntityID is always populated from a
// nullable uuid DB column by the store subtask (never a pointer-to-empty-string), so this
// can't fire from real data, but ScopeOf itself does not defend against it.
func TestAuditScopeOf_PointerToEmptyStringEntityIDCountsAsSet(t *testing.T) {
	got := audit.ScopeOf("workflow_role.created", readerPtr(""))
	if got != audit.ScopeCompany {
		t.Errorf(`ScopeOf(firm-wide event, ptr to "") = %q, want %q`, got, audit.ScopeCompany)
	}
}

// AC #3: json.Unmarshal cannot reach CompanyFilter's unexported fields — a second
// construction path beyond a struct literal, which the compiler already refuses at
// build time from outside the package. Pins the closed-construction claim; if mode/id
// are ever exported, this test stops guarding and should be revisited.
func TestAuditCompanyFilter_JSONUnmarshalCannotSetUnexportedFields(t *testing.T) {
	var f audit.CompanyFilter
	if err := json.Unmarshal([]byte(`{"mode":1,"id":"evil-injected-id"}`), &f); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if f.Mode() != audit.ModeAllCompanies || f.ID() != "" {
		t.Errorf("json.Unmarshal reached unexported fields: mode=%v id=%q", f.Mode(), f.ID())
	}
}

// AC #5 companion: the reflection-based tag test proves no omitempty on Kind's siblings;
// this proves the actual wire shape. Kind must vanish for a facet that doesn't set it
// (event/company facets) and appear for one that does (the actor facet).
func TestAuditFacet_KindOmittedUnlessSet(t *testing.T) {
	eventFacet, err := json.Marshal(audit.Facet{Count: 5})
	if err != nil {
		t.Fatalf("marshal event facet: %v", err)
	}
	if strings.Contains(string(eventFacet), `"kind"`) {
		t.Errorf("event/company facet marshaled a kind field: %s", eventFacet)
	}

	actorFacet, err := json.Marshal(audit.Facet{Kind: "person", Count: 3})
	if err != nil {
		t.Fatalf("marshal actor facet: %v", err)
	}
	if !strings.Contains(string(actorFacet), `"kind":"person"`) {
		t.Errorf("actor facet did not marshal kind: %s", actorFacet)
	}
}

// AC #5 boundary, D-9: no omitempty (already pinned) does not by itself make a nil Go
// slice marshal as []. A bare zero-value Response still marshals nil slices as null —
// the store's make(…, 0, n) coercion, not this type layer, is what guarantees [] on the
// wire. Pins that boundary so the type layer alone is never mistaken for satisfying D-9.
func TestAuditResponse_NilSliceStillMarshalsNullWithoutStoreCoercion(t *testing.T) {
	b, err := json.Marshal(audit.Response{})
	if err != nil {
		t.Fatalf("marshal zero Response: %v", err)
	}
	if !strings.Contains(string(b), `"events":null`) {
		t.Errorf("expected a bare zero Response to marshal events as null (store not involved), got %s", b)
	}

	populated := audit.Response{
		Events: []audit.Event{},
		Facets: audit.Facets{Event: []audit.Facet{}, Actor: []audit.Facet{}, Company: []audit.Facet{}},
	}
	b2, err := json.Marshal(populated)
	if err != nil {
		t.Fatalf("marshal populated Response: %v", err)
	}
	if !strings.Contains(string(b2), `"events":[]`) {
		t.Errorf("expected an explicitly-empty slice to marshal as [], got %s", b2)
	}
}

// --- AUDIT-11-09 AC #6: the fence adds no wire field ---------------------------------------

// TestAuditFilter_NumberArmAddsNoWireField pins Filter and Event against the shape they
// have on main. AUDIT-11-09's fence lives entirely inside resolveSearchTargets, which is
// unexported; a Filter.InvoiceNumber or an Event.InvoiceNumber would be a wire change and
// would need both mirrors moved with it. The mirror half is already guarded by
// frontend/app/src/lib/wireMirrors.test.ts — this is the Go half.
func TestAuditFilter_NumberArmAddsNoWireField(t *testing.T) {
	for _, tc := range []struct {
		value any
		want  []string
	}{
		{audit.Filter{}, []string{
			"Limit", "Cursor", "From", "To", "Events", "Actors", "ActorKind", "Company", "Q", "InvoiceID",
		}},
		{audit.Event{}, []string{
			"ID", "CreatedAt", "Event", "Actor", "ActorName", "ActorKind", "EntityID", "CompanyName",
			"CompanyScope", "Payload",
		}},
	} {
		rt := reflect.TypeOf(tc.value)
		got := map[string]bool{}
		for i := 0; i < rt.NumField(); i++ {
			if f := rt.Field(i); f.IsExported() {
				got[f.Name] = true
			}
		}
		if len(tc.want) == 0 {
			t.Fatalf("%s: the expected field set is empty, so this case asserts nothing", rt.Name())
		}
		for _, w := range tc.want {
			if !got[w] {
				t.Errorf("%s lost the exported field %s", rt.Name(), w)
			}
			delete(got, w)
		}
		for extra := range got {
			t.Errorf("%s gained the exported field %s; the number fence must add no wire field, "+
				"and a new one needs frontend/app/src/lib/audit.ts and e2e/api/client.ts moved with it",
				rt.Name(), extra)
		}
	}
}

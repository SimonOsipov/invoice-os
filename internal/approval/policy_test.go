package approval

// The policy seam's pure surfaces: the wire shape, the two tree transforms, the two
// validators, the scope normalizer and the second status mapper. No DB, no HTTP,
// no skips — this file must run on a bare `go test ./...`.
//
// Written before internal/approval/policy.go has bodies, so every case here starts
// RED on an assertion or on "not implemented".

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"net/http"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/SimonOsipov/invoice-os/internal/platform/db"
)

// --- fixtures ---------------------------------------------------------------

// scopeAllInvoices is spelled here rather than read from the implementation: a test
// that asserts against the constant it is checking asserts nothing.
const scopeAllInvoices = "All invoices"

// removedScopes are the five RETIRED scope strings the server refuses. APPR-10 deleted
// them from the editor, so this is a permanent refusal table with no live TS twin. The
// B2G entry's separator is U+00B7 MIDDLE DOT, pinned by TestPolicy_NormalizeScopeTable.
var removedScopes = []string{
	"Foreign-currency invoices",
	"Document type · B2G",
	"Capex & fixed assets",
	"Consumer invoices (B2C)",
	"Credit notes & adjustments",
}

// stepWireKeys is Step's whole JSON key set, sorted.
var stepWireKeys = []string{
	"cond_amount", "cond_op", "else", "id", "kind",
	"notify_channel", "notify_target", "sla_hours", "then", "workflow_role_key",
}

// policyWireKeys is Policy's whole JSON key set, sorted.
var policyWireKeys = []string{
	"id", "name", "scope", "sealed", "status", "steps", "version", "versions",
}

// stepKinds is the set approval_policy_steps_kind_check accepts.
var stepKinds = []string{"approval", "condition", "notify", "autoapprove"}

func approvalIn(key string) stepInput {
	return stepInput{Kind: "approval", WorkflowRoleKey: ptr(key)}
}

func notifyIn(target, channel string) stepInput {
	return stepInput{Kind: "notify", NotifyTarget: ptr(target), NotifyChannel: ptr(channel)}
}

func condIn(op, amount string, then, els []stepInput) stepInput {
	return stepInput{Kind: "condition", CondOp: ptr(op), CondAmount: ptr(amount), Then: then, Else: els}
}

// legalStepOfKind is the minimal step validateTree accepts for kind — the base every
// every-kind case mutates one field of, so a refusal cannot come from the base.
func legalStepOfKind(kind string) stepInput {
	s := stepInput{Kind: kind}
	switch kind {
	case "condition":
		s.CondOp, s.CondAmount = ptr(">"), ptr("1.00")
	case "notify":
		s.NotifyTarget, s.NotifyChannel = ptr("preparer"), ptr("email")
	case "approval":
		s.WorkflowRoleKey = ptr("tax-reviewer")
	}
	return s
}

// polRowsByRole indexes the flat rows by workflow_role_key, so a case can name a
// step without depending on emission order.
func polRowsByRole(t *testing.T, rows []stepRow) map[string]stepRow {
	t.Helper()
	byRole := make(map[string]stepRow, len(rows))
	for _, r := range rows {
		if r.WorkflowRoleKey == nil {
			continue
		}
		if _, dup := byRole[*r.WorkflowRoleKey]; dup {
			t.Fatalf("role key %q appears on two rows — the fixture cannot address them", *r.WorkflowRoleKey)
		}
		byRole[*r.WorkflowRoleKey] = r
	}
	return byRole
}

func polRowOfKind(t *testing.T, rows []stepRow, kind string) stepRow {
	t.Helper()
	var found []stepRow
	for _, r := range rows {
		if r.Kind == kind {
			found = append(found, r)
		}
	}
	if len(found) != 1 {
		t.Fatalf("want exactly one %q row, got %d", kind, len(found))
	}
	return found[0]
}

func polStr(p *string) string {
	if p == nil {
		return "<nil>"
	}
	return *p
}

// polWalk visits every step in a nested tree.
func polWalk(steps []Step, fn func(Step)) {
	for _, s := range steps {
		fn(s)
		polWalk(s.Then, fn)
		polWalk(s.Else, fn)
	}
}

// polZeroIDs blanks server-minted ids so a round-trip can be compared structurally.
func polZeroIDs(steps []Step) {
	for i := range steps {
		steps[i].ID = ""
		polZeroIDs(steps[i].Then)
		polZeroIDs(steps[i].Else)
	}
}

// --- AC-1: the wire shape ---------------------------------------------------

func TestPolicy_StepMarshalsTenKeys(t *testing.T) {
	for _, kind := range stepKinds {
		t.Run(kind, func(t *testing.T) {
			// Nil lanes: the zero value is what a leaf kind carries.
			raw, err := json.Marshal(Step{ID: uuid.NewString(), Kind: kind})
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			if got := keySet(t, raw); !reflect.DeepEqual(got, stepWireKeys) {
				t.Errorf("key set = %v, want %v", got, stepWireKeys)
			}
			// Raw bytes, not a decoded value: a decoded nil and a decoded [] both read empty.
			for _, lane := range []string{"then", "else"} {
				if got := rawField(t, raw, lane); got != "[]" {
					t.Errorf("%q = %s, want [] (null forces the SPA to guard every lane)", lane, got)
				}
			}
			for _, col := range []string{"parent_step_id", "ord", "version_id"} {
				if bytes.Contains(raw, []byte(`"`+col+`"`)) {
					t.Errorf("%s leaks the storage column %q", raw, col)
				}
			}
		})
	}
}

func TestPolicy_PolicyMarshalsStableKeys(t *testing.T) {
	// Nil Steps/Versions: what a freshly created, stepless policy carries.
	raw, err := json.Marshal(Policy{
		ID: uuid.NewString(), Name: "Sign-off", Scope: scopeAllInvoices, Status: "draft", Version: 1,
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if got := keySet(t, raw); !reflect.DeepEqual(got, policyWireKeys) {
		t.Errorf("key set = %v, want %v", got, policyWireKeys)
	}
	for _, field := range []string{"steps", "versions"} {
		if got := rawField(t, raw, field); got != "[]" {
			t.Errorf("%q = %s, want []", field, got)
		}
	}
}

// --- AC-2: flatten ----------------------------------------------------------

func TestPolicy_FlattenMintsServerIds(t *testing.T) {
	// Decoded from real JSON carrying an id stepInput does not declare.
	const body = `[
	  {"id":"wn1000","kind":"approval","workflow_role_key":"engagement-partner"},
	  {"id":"wn1001","kind":"condition","cond_op":">","cond_amount":"250000.00",
	   "then":[{"id":"wn1002","kind":"notify","notify_target":"preparer","notify_channel":"email"}],
	   "else":[]}
	]`
	var tree []stepInput
	if err := json.Unmarshal([]byte(body), &tree); err != nil {
		t.Fatalf("decode fixture: %v", err)
	}
	clientIDs := takenSet("wn1000", "wn1001", "wn1002")

	rows := flattenSteps(tree)
	if len(rows) != 3 {
		t.Fatalf("rows = %d, want 3", len(rows))
	}

	minted := make(map[string]bool, len(rows))
	for _, r := range rows {
		if clientIDs[r.ID] {
			t.Errorf("client-supplied id %q was persisted", r.ID)
		}
		if _, err := uuid.Parse(r.ID); err != nil {
			t.Errorf("row id %q is not a uuid: %v", r.ID, err)
		}
		if minted[r.ID] {
			t.Errorf("id %q minted twice in one call", r.ID)
		}
		minted[r.ID] = true
	}

	// Ids churn per call by design — nothing reads a step id back.
	again := flattenSteps(tree)
	for _, r := range again {
		if minted[r.ID] {
			t.Errorf("second call reused id %q", r.ID)
		}
	}
}

func TestPolicy_FlattenDerivesParentBranchOrd(t *testing.T) {
	tree := []stepInput{
		approvalIn("root-first"),
		condIn(">", "250000.00",
			[]stepInput{approvalIn("then-a"), approvalIn("then-b")},
			[]stepInput{approvalIn("else-c")}),
	}

	rows := flattenSteps(tree)
	if len(rows) != 5 {
		t.Fatalf("rows = %d, want 5", len(rows))
	}
	cond := polRowOfKind(t, rows, "condition")
	byRole := polRowsByRole(t, rows)

	if cond.ParentStepID != nil || cond.Branch != nil {
		t.Errorf("condition root: parent = %s, branch = %s, want NULL/NULL", polStr(cond.ParentStepID), polStr(cond.Branch))
	}
	if cond.Ord != 1 {
		t.Errorf("condition root ord = %d, want 1", cond.Ord)
	}

	cases := []struct {
		role   string
		parent *string
		branch *string
		ord    int
	}{
		{"root-first", nil, nil, 0},
		{"then-a", ptr(cond.ID), ptr("then"), 0},
		{"then-b", ptr(cond.ID), ptr("then"), 1},
		{"else-c", ptr(cond.ID), ptr("else"), 0},
	}
	for _, tc := range cases {
		t.Run(tc.role, func(t *testing.T) {
			row, ok := byRole[tc.role]
			if !ok {
				t.Fatalf("no row for role %q", tc.role)
			}
			if polStr(row.ParentStepID) != polStr(tc.parent) {
				t.Errorf("parent_step_id = %s, want %s", polStr(row.ParentStepID), polStr(tc.parent))
			}
			if polStr(row.Branch) != polStr(tc.branch) {
				t.Errorf("branch = %s, want %s", polStr(row.Branch), polStr(tc.branch))
			}
			if row.Ord != tc.ord {
				t.Errorf("ord = %d, want %d (0-based and dense per lane, or the slot unique index rejects the write)", row.Ord, tc.ord)
			}
		})
	}
}

// --- AC-3: round trip -------------------------------------------------------

func TestPolicy_NestFlattenRoundTrip(t *testing.T) {
	tree := []stepInput{
		{Kind: "approval", WorkflowRoleKey: ptr("engagement-partner"), SLAHours: ptr(24)},
		condIn(">=", "250000.00",
			[]stepInput{{Kind: "approval", WorkflowRoleKey: ptr("tax-reviewer"), SLAHours: ptr(48)}},
			[]stepInput{notifyIn("preparer", "email"), {Kind: "autoapprove"}}),
	}
	want := []Step{
		{Kind: "approval", WorkflowRoleKey: ptr("engagement-partner"), SLAHours: ptr(24), Then: []Step{}, Else: []Step{}},
		{
			Kind: "condition", CondOp: ptr(">="), CondAmount: ptr("250000.00"),
			Then: []Step{{Kind: "approval", WorkflowRoleKey: ptr("tax-reviewer"), SLAHours: ptr(48), Then: []Step{}, Else: []Step{}}},
			Else: []Step{
				{Kind: "notify", NotifyTarget: ptr("preparer"), NotifyChannel: ptr("email"), Then: []Step{}, Else: []Step{}},
				{Kind: "autoapprove", Then: []Step{}, Else: []Step{}},
			},
		},
	}

	rows := flattenSteps(tree)
	got := nestSteps(rows)

	// Ids are checked before they are blanked, so zeroing cannot hide a dropped id.
	count := 0
	polWalk(got, func(s Step) {
		count++
		if _, err := uuid.Parse(s.ID); err != nil {
			t.Errorf("nested step %q carries no uuid: %v", s.Kind, err)
		}
	})
	if count != 5 {
		t.Errorf("nested tree holds %d steps, want 5", count)
	}

	polZeroIDs(got)
	if !reflect.DeepEqual(got, want) {
		t.Errorf("round trip diverged\n got: %+v\nwant: %+v", got, want)
	}
}

// --- AC-4: validateTree -----------------------------------------------------

func TestPolicy_ValidateTreeTable(t *testing.T) {
	legal := []stepInput{
		{Kind: "approval", WorkflowRoleKey: ptr("engagement-partner"), SLAHours: ptr(24)},
		condIn(">", "250000.00", []stepInput{approvalIn("tax-reviewer")}, []stepInput{{Kind: "autoapprove"}}),
		notifyIn("preparer", "email"),
	}

	cases := []struct {
		name    string
		tree    []stepInput
		wantErr bool
	}{
		{"an unknown kind", []stepInput{{Kind: "delegate"}}, true},
		{"an empty kind", []stepInput{{Kind: ""}}, true},
		{"a condition inside a then lane", []stepInput{
			condIn(">", "100.00", []stepInput{condIn(">", "50.00", nil, nil)}, nil),
		}, true},
		{"a condition inside an else lane", []stepInput{
			condIn(">", "100.00", nil, []stepInput{condIn(">", "50.00", nil, nil)}),
		}, true},
		{"a cond_op outside the four", []stepInput{condIn("=", "100.00", nil, nil)}, true},
		// A condition with no operator cannot evaluate; the column is nullable, so only
		// this gate refuses it.
		{"a condition with no cond_op", []stepInput{{Kind: "condition", CondAmount: ptr("100.00")}}, true},
		{"a condition with no cond_amount", []stepInput{{Kind: "condition", CondOp: ptr(">")}}, true},
		{"a cond_amount that is not a decimal", []stepInput{condIn(">", "lots", nil, nil)}, true},
		{"an empty cond_amount", []stepInput{condIn(">", "", nil, nil)}, true},
		// cond_amount needs no NUL check of its own: decimal.NewFromString refuses one,
		// and validateStepFields runs it whatever the kind.
		{"a NUL in cond_amount", []stepInput{condIn(">", "100.00\x00", nil, nil)}, true},
		{"a NUL in a nested lane's role key", []stepInput{
			condIn(">", "100.00", []stepInput{approvalIn("tax\x00reviewer")}, nil),
		}, true},
		{"a negative sla_hours", []stepInput{
			{Kind: "approval", WorkflowRoleKey: ptr("engagement-partner"), SLAHours: ptr(-1)},
		}, true},
		{"a notify with an empty target", []stepInput{notifyIn("", "email")}, true},
		{"a notify with an empty channel", []stepInput{notifyIn("preparer", "")}, true},
		{"a notify with no target at all", []stepInput{{Kind: "notify", NotifyChannel: ptr("email")}}, true},
		{"an approval carrying a then lane", []stepInput{
			{Kind: "approval", WorkflowRoleKey: ptr("engagement-partner"), Then: []stepInput{notifyIn("preparer", "email")}},
		}, true},
		{"an autoapprove carrying an else lane", []stepInput{
			{Kind: "autoapprove", Else: []stepInput{notifyIn("preparer", "email")}},
		}, true},

		{"a legal tree", legal, false},
		{"an empty tree", []stepInput{}, false},
		{"a nil tree", nil, false},
		// The dangling-role gate is publish's door and nowhere else.
		{"an approval with an empty role key", []stepInput{approvalIn("")}, false},
		{"an approval with no role key at all", []stepInput{{Kind: "approval"}}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validateTree(tc.tree)
			if tc.wantErr && !errors.Is(err, ErrValidation) {
				t.Errorf("err = %v, want ErrValidation", err)
			}
			if !tc.wantErr && err != nil {
				t.Errorf("err = %v, want nil", err)
			}
		})
	}
}

// TestPolicy_ValidateTreeCondAmountBounds: cond_amount is numeric(14,2). An
// out-of-range or over-scaled value raises 22003, which carries no constraint name
// and so cannot be mapped to a 400 downstream.
func TestPolicy_ValidateTreeCondAmountBounds(t *testing.T) {
	cases := []struct {
		amount  string
		wantErr bool
	}{
		{"999999999999999.99", true}, // overflows precision 14
		{"1000000000000.00", true},   // exactly 1e12
		{"-1000000000000", true},     // out of range the other way
		{"100.005", true},            // scale 3 — the column would silently round to 100.01
		{"999999999999.99", false},   // the largest legal value
		{"-999999999999.99", false},  // in range, negative
		{"0", false},
		{"250000000", false},
	}
	for _, tc := range cases {
		t.Run(tc.amount, func(t *testing.T) {
			err := validateTree([]stepInput{condIn(">", tc.amount, nil, nil)})
			if tc.wantErr && !errors.Is(err, ErrValidation) {
				t.Errorf("err = %v, want ErrValidation", err)
			}
			if !tc.wantErr && err != nil {
				t.Errorf("err = %v, want nil", err)
			}
		})
	}
}

// TestPolicy_ValidateTreeSlaHoursBounds: sla_hours is int4 and stepInput.SLAHours is
// a Go int, so an over-range value decodes cleanly and only 22003 stops it.
func TestPolicy_ValidateTreeSlaHoursBounds(t *testing.T) {
	cases := []struct {
		name    string
		sla     *int
		wantErr bool
	}{
		{"negative", ptr(-1), true},
		{"beyond int4", ptr(3000000000), true},
		{"the int4 ceiling", ptr(2147483647), false},
		{"zero", ptr(0), false},
		{"absent means no deadline", nil, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tree := []stepInput{{Kind: "approval", WorkflowRoleKey: ptr("engagement-partner"), SLAHours: tc.sla}}
			err := validateTree(tree)
			if tc.wantErr && !errors.Is(err, ErrValidation) {
				t.Errorf("err = %v, want ErrValidation", err)
			}
			if !tc.wantErr && err != nil {
				t.Errorf("err = %v, want nil", err)
			}
		})
	}
}

// --- AC-6: normalizeScope ---------------------------------------------------

func TestPolicy_NormalizeScopeTable(t *testing.T) {
	// Byte-level pin: retyped with an ASCII dot or hyphen, the rejection row below
	// would exercise a string the SPA never sends and assert nothing.
	t.Run("the B2G scope carries U+00B7", func(t *testing.T) {
		const want = "Document type · B2G"
		if removedScopes[1] != want {
			t.Fatalf("removedScopes[1] = %q (% x), want %q (% x)", removedScopes[1], removedScopes[1], want, want)
		}
		if !bytes.Contains([]byte(removedScopes[1]), []byte{0xc2, 0xb7}) {
			t.Fatalf("removedScopes[1] = % x, want it to contain the U+00B7 bytes c2 b7", removedScopes[1])
		}
	})

	// Every one of these returns nil, and the property below is what stands between
	// an empty scope and a 23514: the return is the STORED form, never the argument.
	for _, in := range []string{"", "   ", "\t\n ", scopeAllInvoices, "  All invoices  "} {
		t.Run("accepts "+strconv.Quote(in), func(t *testing.T) {
			got, err := normalizeScope(in)
			if err != nil {
				t.Fatalf("err = %v, want nil", err)
			}
			if got != scopeAllInvoices {
				t.Errorf("normalizeScope(%q) = %q, want %q — a normalizer that echoes its argument sends %q to a column whose CHECK refuses it",
					in, got, scopeAllInvoices, in)
			}
		})
	}

	for _, in := range removedScopes {
		t.Run("rejects "+in, func(t *testing.T) {
			got, err := normalizeScope(in)
			if !errors.Is(err, ErrValidation) {
				t.Errorf("err = %v, want ErrValidation", err)
			}
			if got != "" {
				t.Errorf("value = %q, want the empty string alongside the error", got)
			}
		})
	}
}

// --- AC-5: validateForPublish -----------------------------------------------

func TestPolicy_ValidateForPublishTable(t *testing.T) {
	live := takenSet("engagement-partner", "tax-reviewer")
	approvalOf := func(key *string) Step { return Step{Kind: "approval", WorkflowRoleKey: key} }
	condOf := func(then, els []Step) Step {
		return Step{Kind: "condition", CondOp: ptr(">"), CondAmount: ptr("250000.00"), Then: then, Else: els}
	}

	cases := []struct {
		name string
		tree []Step
		want error
	}{
		{"an approval with an empty role key", []Step{approvalOf(ptr(""))}, ErrPolicyStepRole},
		{"an approval with no role key at all", []Step{approvalOf(nil)}, ErrPolicyStepRole},
		{"an approval naming a role that is gone", []Step{approvalOf(ptr("ghost-role"))}, ErrPolicyStepRole},
		{"a dead role nested in a lane", []Step{condOf([]Step{approvalOf(ptr("ghost-role"))}, []Step{})}, ErrPolicyStepRole},
		{"a condition with two empty lanes", []Step{condOf([]Step{}, []Step{})}, ErrPolicyEmptyBranches},
		{"a condition with two nil lanes", []Step{condOf(nil, nil)}, ErrPolicyEmptyBranches},

		{"an empty tree", nil, nil},
		{"an empty but non-nil tree", []Step{}, nil},
		{"a condition with one lane populated", []Step{condOf([]Step{}, []Step{approvalOf(ptr("tax-reviewer"))})}, nil},
		{"an approval naming a live role", []Step{approvalOf(ptr("engagement-partner"))}, nil},
		{"a notify step, which names no role", []Step{{Kind: "notify", NotifyTarget: ptr("preparer"), NotifyChannel: ptr("email")}}, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validateForPublish(tc.tree, live)
			if tc.want == nil {
				if err != nil {
					t.Errorf("err = %v, want nil", err)
				}
				return
			}
			if !errors.Is(err, tc.want) {
				t.Errorf("err = %v, want %v", err, tc.want)
			}
		})
	}
}

// --- AC-7: policyStatusForErr -----------------------------------------------

// TestPolicy_StatusForErrTable: a second mapper, so statusForErr's workflow-role
// wording stays untouched. Every message is hand-written, so the "approval: "
// sentinel prefix never reaches the SPA.
func TestPolicy_StatusForErrTable(t *testing.T) {
	cases := []struct {
		err     error
		want    int
		wantMsg string
	}{
		{db.ErrNoTenant, http.StatusUnauthorized, "unauthorized"},
		{ErrValidation, http.StatusBadRequest, "invalid request"},
		{ErrNotPermitted, http.StatusForbidden, "only an admin can change approval policies"},
		{ErrPolicyNotFound, http.StatusNotFound, "approval policy not found"},
		{ErrPolicyStepRole, http.StatusConflict, "an approval step names a workflow role that no longer exists"},
		{ErrPolicyEmptyBranches, http.StatusConflict, "a condition must have at least one step in one of its two lanes"},
		{ErrPolicyNothingToPublish, http.StatusConflict, "this policy has no unpublished changes"},
		// AC-3 (task-484): the publish sweep's cap refusal.
		{ErrSweepCapExceeded, http.StatusConflict, "validated backlog exceeds the publish sweep cap — see docs/approvals.md"},
		// The concurrent-publish loser: 23505 on approval_policy_versions_one_active maps
		// here. Policy wording, not statusForErr's role-domain string — the two mappers
		// share the sentinel and nothing else.
		{ErrConflict, http.StatusConflict, "another version was published first — reload the policy and try again"},
		{errors.New("boom"), http.StatusInternalServerError, "internal server error"},
	}
	for _, tc := range cases {
		for _, wrapped := range []bool{false, true} {
			err, name := tc.err, tc.err.Error()
			if wrapped {
				err, name = fmt.Errorf("store: %w", tc.err), name+" (wrapped)"
			}
			t.Run(name, func(t *testing.T) {
				status, msg := policyStatusForErr(err)
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

// --- QA: adversarial coverage ------------------------------------------------

// embedHazardDemo is the deliberate counter-example. Step and Policy carry
// value-receiver MarshalJSON methods, so embedding one anonymously promotes that
// method onto the outer struct and every sibling field vanishes from the wire.
// TestPolicy_NoAnonymousWireEmbedding scans for this shape and allows only this
// one; TestPolicy_EmbeddingDropsSiblingFields is what it costs.
type embedHazardDemo struct {
	Policy
	Extra string `json:"extra"`
}

// namedFieldIsSafe is the shape a response struct must use instead.
type namedFieldIsSafe struct {
	Policy Policy `json:"policy"`
	Extra  string `json:"extra"`
}

func TestPolicy_EmbeddingDropsSiblingFields(t *testing.T) {
	raw, err := json.Marshal(embedHazardDemo{Policy: Policy{ID: "p1"}, Extra: "dropped"})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if got := keySet(t, raw); !reflect.DeepEqual(got, policyWireKeys) {
		t.Fatalf("embedded key set = %v, want Policy's own %v — anonymous embedding no longer hijacks "+
			"the outer struct, so re-check TestPolicy_NoAnonymousWireEmbedding before relying on it", got, policyWireKeys)
	}
	if bytes.Contains(raw, []byte(`"extra"`)) {
		t.Errorf("%s carries extra — the hazard this file guards against is gone", raw)
	}

	// The prescribed alternative keeps both.
	raw, err = json.Marshal(namedFieldIsSafe{Policy: Policy{ID: "p1"}, Extra: "kept"})
	if err != nil {
		t.Fatalf("marshal named field: %v", err)
	}
	if got := keySet(t, raw); !reflect.DeepEqual(got, []string{"extra", "policy"}) {
		t.Errorf("named-field key set = %v, want [extra policy]", got)
	}
	if rawField(t, raw, "policy") == "null" {
		t.Error("the named Policy field marshalled as null")
	}
}

// TestPolicy_NoAnonymousWireEmbedding fails if any type in this package embeds Step
// or Policy anonymously. Subtask 07 builds the response structs; a promoted
// MarshalJSON drops sibling fields silently, with no compile error and no test
// failure anywhere else.
func TestPolicy_NoAnonymousWireEmbedding(t *testing.T) {
	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, ".", nil, 0)
	if err != nil {
		t.Fatalf("parse the package source: %v", err)
	}
	banned := map[string]bool{"Step": true, "Policy": true}

	var declared, embeds []string
	for _, pkg := range pkgs {
		for path, file := range pkg.Files {
			ast.Inspect(file, func(n ast.Node) bool {
				spec, ok := n.(*ast.TypeSpec)
				if !ok {
					return true
				}
				declared = append(declared, spec.Name.Name)
				st, ok := spec.Type.(*ast.StructType)
				if !ok || st.Fields == nil {
					return true
				}
				for _, f := range st.Fields.List {
					if len(f.Names) != 0 { // a named field is safe
						continue
					}
					if name := embeddedTypeName(f.Type); banned[name] {
						embeds = append(embeds, spec.Name.Name)
						if spec.Name.Name != "embedHazardDemo" {
							t.Errorf("%s: %s embeds %s anonymously — the promoted MarshalJSON hijacks %s "+
								"and every sibling field disappears from the wire; use a named field",
								filepath.Base(path), spec.Name.Name, name, spec.Name.Name)
						}
					}
				}
				return true
			})
		}
	}

	// Non-vacuity: the scan must have read policy.go, and must have caught the
	// counter-example. Either miss would make every assertion above silent.
	for _, want := range []string{"Step", "Policy", "embedHazardDemo"} {
		if !slices.Contains(declared, want) {
			t.Fatalf("the source scan never saw type %s (%d types found) — it is not reading this package", want, len(declared))
		}
	}
	if !slices.Contains(embeds, "embedHazardDemo") {
		t.Fatal("the scan did not flag embedHazardDemo, which exists precisely to be flagged — it cannot detect an embed")
	}
}

func embeddedTypeName(expr ast.Expr) string {
	if star, ok := expr.(*ast.StarExpr); ok {
		expr = star.X
	}
	if id, ok := expr.(*ast.Ident); ok {
		return id.Name
	}
	return ""
}

// TestPolicy_LanesAreNeverNullAtDepthOrThroughAPointer: the [] substitution lives on
// a value receiver, so it must survive every path a handler can reach a Step by —
// a lane child, a *Policy, and a []Step nested inside other containers.
func TestPolicy_LanesAreNeverNullAtDepthOrThroughAPointer(t *testing.T) {
	// Lane children carry nil lanes: exactly what nestSteps' leaves would if the
	// substitution were not re-entrant.
	tree := []Step{{
		Kind: "condition", CondOp: ptr(">"), CondAmount: ptr("250000.00"),
		Then: []Step{{Kind: "approval", WorkflowRoleKey: ptr("tax-reviewer")}},
		Else: []Step{{Kind: "notify", NotifyTarget: ptr("preparer"), NotifyChannel: ptr("email")}},
	}}

	cases := []struct {
		name  string
		value any
		steps int
	}{
		{"a *Policy, as a handler holds one", &Policy{ID: "p1", Steps: tree}, 3},
		{"a Policy value", Policy{ID: "p1", Steps: tree}, 3},
		{"a bare []Step", tree, 3},
		{"[]Step inside a map inside a slice", []map[string][]Step{{"steps": tree}}, 3},
		{"a lane child on its own", tree[0].Then[0], 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			raw, err := json.Marshal(tc.value)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			for _, lane := range []string{"then", "else"} {
				if bytes.Contains(raw, []byte(`"`+lane+`":null`)) {
					t.Errorf("%s carries %q:null", raw, lane)
				}
				if got := bytes.Count(raw, []byte(`"`+lane+`":`)); got != tc.steps {
					t.Errorf("%s has %d %q keys, want one per step (%d)", raw, got, lane, tc.steps)
				}
			}
		})
	}
}

// TestPolicy_NestStepsEmptyIsNeverNull: an empty version's steps must reach the SPA
// as [], and nestSteps is the only producer of that slice.
func TestPolicy_NestStepsEmptyIsNeverNull(t *testing.T) {
	for _, rows := range [][]stepRow{nil, {}} {
		got := nestSteps(rows)
		if got == nil {
			t.Errorf("nestSteps(%v) = nil, want an empty non-nil slice", rows)
		}
		raw, err := json.Marshal(Policy{ID: "p1", Steps: got})
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		if f := rawField(t, raw, "steps"); f != "[]" {
			t.Errorf("steps = %s, want []", f)
		}
	}
}

// TestPolicy_NestStepsOrdersByOrdNotRowOrder: the store's SELECT order is not part
// of this contract, so nesting must sort by ord. Fed rows in reverse.
func TestPolicy_NestStepsOrdersByOrdNotRowOrder(t *testing.T) {
	rows := flattenSteps([]stepInput{
		approvalIn("root-0"),
		condIn(">", "250000.00",
			[]stepInput{approvalIn("then-0"), approvalIn("then-1"), approvalIn("then-2")},
			nil),
		approvalIn("root-2"),
	})
	slices.Reverse(rows)

	got := nestSteps(rows)
	var rootKeys, thenKeys []string
	for _, s := range got {
		rootKeys = append(rootKeys, polStr(s.WorkflowRoleKey))
		for _, c := range s.Then {
			thenKeys = append(thenKeys, polStr(c.WorkflowRoleKey))
		}
	}
	if want := []string{"root-0", "<nil>", "root-2"}; !reflect.DeepEqual(rootKeys, want) {
		t.Errorf("root lane = %v, want %v", rootKeys, want)
	}
	if want := []string{"then-0", "then-1", "then-2"}; !reflect.DeepEqual(thenKeys, want) {
		t.Errorf("then lane = %v, want %v", thenKeys, want)
	}
}

// TestPolicy_NestFlattenRoundTripIsStable: the pipeline must be a pure function of
// its input modulo the minted ids, or a PUT that changes nothing would still churn
// the stored tree.
func TestPolicy_NestFlattenRoundTripIsStable(t *testing.T) {
	tree := []stepInput{
		approvalIn("engagement-partner"),
		condIn("<=", "250000.00",
			[]stepInput{approvalIn("tax-reviewer"), {Kind: "autoapprove"}},
			[]stepInput{notifyIn("preparer", "email")}),
	}
	first := nestSteps(flattenSteps(tree))
	second := nestSteps(flattenSteps(tree))

	var firstIDs, secondIDs []string
	polWalk(first, func(s Step) { firstIDs = append(firstIDs, s.ID) })
	polWalk(second, func(s Step) { secondIDs = append(secondIDs, s.ID) })
	if len(firstIDs) != 5 || len(secondIDs) != 5 {
		t.Fatalf("step counts = %d and %d, want 5 each", len(firstIDs), len(secondIDs))
	}
	for i := range firstIDs {
		if firstIDs[i] == secondIDs[i] {
			t.Errorf("step %d reused id %q across calls — ids are minted per call", i, firstIDs[i])
		}
	}

	polZeroIDs(first)
	polZeroIDs(second)
	if !reflect.DeepEqual(first, second) {
		t.Errorf("two runs over the same input diverged\n first: %+v\nsecond: %+v", first, second)
	}
}

// TestPolicy_FlattenOrdStaysDenseAfterARemoval: approval_policy_steps_slot_uq is
// UNIQUE NULLS NOT DISTINCT (version_id, parent_step_id, branch, ord), so a lane that
// kept a gap after an edit would still write — and a lane that kept the REMOVED
// element's ord would collide. ord is re-derived from position, never carried.
func TestPolicy_FlattenOrdStaysDenseAfterARemoval(t *testing.T) {
	full := condIn(">", "250000.00",
		[]stepInput{approvalIn("then-a"), approvalIn("then-b"), approvalIn("then-c")}, nil)
	trimmed := condIn(">", "250000.00",
		[]stepInput{approvalIn("then-a"), approvalIn("then-c")}, nil)

	rows := flattenSteps([]stepInput{trimmed})
	byRole := polRowsByRole(t, rows)
	for role, wantOrd := range map[string]int{"then-a": 0, "then-c": 1} {
		if got := byRole[role].Ord; got != wantOrd {
			t.Errorf("%s ord = %d, want %d — the removed middle element must not leave a gap", role, got, wantOrd)
		}
	}

	// Every (parent, branch, ord) slot is unique across the whole tree.
	for _, tree := range [][]stepInput{{full}, {trimmed}} {
		seen := map[string]bool{}
		for _, r := range flattenSteps(tree) {
			slot := fmt.Sprintf("%s|%s|%d", polStr(r.ParentStepID), polStr(r.Branch), r.Ord)
			if seen[slot] {
				t.Errorf("slot %s emitted twice — approval_policy_steps_slot_uq would reject the write", slot)
			}
			seen[slot] = true
		}
	}
}

// TestPolicy_ValidateTreeDepthCap: approval_policy_steps_depth_cap allows a condition
// only at the root, and no other kind may carry a lane, so two is the deepest legal
// tree. Both ways of writing a third level are refused.
func TestPolicy_ValidateTreeDepthCap(t *testing.T) {
	deepest := []stepInput{condIn(">", "250000.00",
		[]stepInput{approvalIn("tax-reviewer")},
		[]stepInput{notifyIn("preparer", "email")})}
	if err := validateTree(deepest); err != nil {
		t.Errorf("the deepest legal tree (root condition + lane leaves) was refused: %v", err)
	}

	cases := []struct {
		name string
		tree []stepInput
	}{
		{"a third level via a nested condition", []stepInput{condIn(">", "250000.00",
			[]stepInput{condIn(">", "1.00", []stepInput{approvalIn("tax-reviewer")}, nil)}, nil)}},
		{"a third level via a lane on a nested approval", []stepInput{condIn(">", "250000.00",
			[]stepInput{{Kind: "approval", WorkflowRoleKey: ptr("tax-reviewer"),
				Then: []stepInput{notifyIn("preparer", "email")}}}, nil)}},
		{"a third level via a lane on a nested notify", []stepInput{condIn(">", "250000.00", nil,
			[]stepInput{{Kind: "notify", NotifyTarget: ptr("preparer"), NotifyChannel: ptr("email"),
				Else: []stepInput{{Kind: "autoapprove"}}}})}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := validateTree(tc.tree); !errors.Is(err, ErrValidation) {
				t.Errorf("err = %v, want ErrValidation", err)
			}
		})
	}
}

// TestPolicy_ValidateTreeBoundsApplyToEveryKind: sla_hours and cond_amount are written
// whatever the kind carries them, so a bound that only fired on the owning kind would
// still let 22003 reach the store as a 500.
func TestPolicy_ValidateTreeBoundsApplyToEveryKind(t *testing.T) {
	for _, kind := range stepKinds {
		base := stepInput{Kind: kind}
		switch kind {
		case "condition":
			base.CondOp, base.CondAmount = ptr(">"), ptr("1.00")
		case "notify":
			base.NotifyTarget, base.NotifyChannel = ptr("preparer"), ptr("email")
		case "approval":
			base.WorkflowRoleKey = ptr("tax-reviewer")
		}

		t.Run(kind+" with an sla_hours past int4", func(t *testing.T) {
			s := base
			s.SLAHours = ptr(2147483648)
			if err := validateTree([]stepInput{s}); !errors.Is(err, ErrValidation) {
				t.Errorf("err = %v, want ErrValidation", err)
			}
		})
		t.Run(kind+" with an sla_hours below int4", func(t *testing.T) {
			s := base
			s.SLAHours = ptr(-2147483649)
			if err := validateTree([]stepInput{s}); !errors.Is(err, ErrValidation) {
				t.Errorf("err = %v, want ErrValidation", err)
			}
		})
		t.Run(kind+" with an over-range cond_amount", func(t *testing.T) {
			s := base
			s.CondAmount = ptr("1000000000000.00")
			if err := validateTree([]stepInput{s}); !errors.Is(err, ErrValidation) {
				t.Errorf("err = %v, want ErrValidation", err)
			}
		})
	}
}

// TestPolicy_ValidateTreeRefusesANULInEveryTextFieldAndKind: a NUL in a text parameter
// raises 22021, which carries no constraint name, so policyStatusForErr answers 500 on
// input a client chose. Every string column is written whatever the kind carries it, so
// a refusal that only fired on the owning kind would leave the hole open — the
// TestPolicy_ValidateTreeBoundsApplyToEveryKind shape.
func TestPolicy_ValidateTreeRefusesANULInEveryTextFieldAndKind(t *testing.T) {
	fields := []struct {
		field string
		set   func(*stepInput)
	}{
		{"workflow_role_key", func(s *stepInput) { s.WorkflowRoleKey = ptr("tax\x00reviewer") }},
		{"cond_op", func(s *stepInput) { s.CondOp = ptr(">\x00") }},
		{"notify_target", func(s *stepInput) { s.NotifyTarget = ptr("prep\x00arer") }},
		{"notify_channel", func(s *stepInput) { s.NotifyChannel = ptr("em\x00ail") }},
	}
	for _, kind := range stepKinds {
		base := legalStepOfKind(kind)
		// The base is legal, or the rows below would pass on the wrong refusal.
		t.Run(kind+" without a NUL", func(t *testing.T) {
			if err := validateTree([]stepInput{base}); err != nil {
				t.Fatalf("err = %v, want nil", err)
			}
		})
		for _, tc := range fields {
			t.Run(kind+" with a NUL in "+tc.field, func(t *testing.T) {
				s := base
				tc.set(&s)
				if err := validateTree([]stepInput{s}); !errors.Is(err, ErrValidation) {
					t.Errorf("err = %v, want ErrValidation", err)
				}
			})
		}
	}
}

// TestPolicy_ValidateTreeRefusesAForeignCondOpOnEveryKind: cond_op is written whatever the
// kind carries it, and the column CHECK accepts only the four operators — a foreign one
// raises 23514 on approval_policy_steps_cond_op_check, which carries no sentinel, so
// policyStatusForErr answers 500 on input a client chose. The vocabulary therefore sits
// outside the kind switch; only the condition's must-have-one rule stays inside it.
//
// The legal-cond_op control per kind is the other half: a non-condition step may carry an
// operator, the column is nullable and unconstrained by kind, and refusing that would be a
// behaviour change rather than a fix.
func TestPolicy_ValidateTreeRefusesAForeignCondOpOnEveryKind(t *testing.T) {
	for _, kind := range stepKinds {
		base := legalStepOfKind(kind)
		for _, op := range []string{">", ">=", "<", "<="} {
			t.Run(kind+" with the legal cond_op "+op, func(t *testing.T) {
				s := base
				s.CondOp = ptr(op)
				if err := validateTree([]stepInput{s}); err != nil {
					t.Errorf("err = %v, want nil — the CHECK accepts this operator on any kind", err)
				}
			})
		}
		for _, op := range []string{"BOOM", "=", "<>", "!=", "=>", ">>", " >", "> ", ""} {
			t.Run(kind+" with the foreign cond_op "+op, func(t *testing.T) {
				s := base
				s.CondOp = ptr(op)
				if err := validateTree([]stepInput{s}); !errors.Is(err, ErrValidation) {
					t.Errorf("err = %v, want ErrValidation", err)
				}
			})
		}
	}
}

// TestPolicy_ValidateCondAmountGrammarIsNarrowerThanPostgres: the safe direction is
// the only one that matters — the validator may refuse text numeric would take, but
// never the reverse, or 22003/22P02 reaches the store with no constraint name and
// answers 500. NaN and Infinity are legal numeric input and must not get through:
// stored, they would make every > comparison false.
func TestPolicy_ValidateCondAmountGrammarIsNarrowerThanPostgres(t *testing.T) {
	cases := []struct {
		amount  string
		wantErr bool
	}{
		{"+250000.00", false},      // numeric takes a leading sign
		{"2.5e3", false},           // exponent notation resolves to 2500
		{"-0.00", false},           // signed zero
		{"250000", false},          // no fractional part at all
		{"NaN", true},              // numeric accepts it; a NaN threshold silently never fires
		{"Infinity", true},         // ditto (PG14+)
		{"-Infinity", true},        //
		{" 250000.00", true},       // numeric trims, this validator does not
		{"250000.00 ", true},       //
		{"250,000.00", true},       // group separators
		{"₦250000.00", true},       // a currency mark from a paste
		{"", true},                 //
		{"2.5e-3", true},           // scale 4 once resolved — numeric would round it to 0.00
		{"999999999999.999", true}, // scale 3 at the range boundary
	}
	for _, tc := range cases {
		t.Run(strconv.Quote(tc.amount), func(t *testing.T) {
			err := validateTree([]stepInput{condIn(">", tc.amount, nil, nil)})
			if tc.wantErr && !errors.Is(err, ErrValidation) {
				t.Errorf("err = %v, want ErrValidation", err)
			}
			if !tc.wantErr && err != nil {
				t.Errorf("err = %v, want nil", err)
			}
		})
	}
}

// condAmountCostBudget is the load-bearing half of the two tests below. A wall-clock
// deadline only reports that this machine was fast enough; the allocation ceiling holds
// on any machine, because reaching a magnitude comparison with one of these exponents
// costs hundreds of MB whatever the hardware — measured on the code before the exponent
// bound: 330MB for "1e100000000", 79MB for "0e100000000".
const condAmountCostBudget = 8 << 20

// costOfValidating returns the bytes validateTree allocated, or false if it had not
// answered within a second.
func costOfValidating(t *testing.T, amount string) (error, uint64, bool) {
	t.Helper()
	type result struct {
		err   error
		alloc uint64
	}
	done := make(chan result, 1)
	go func() {
		var before, after runtime.MemStats
		runtime.ReadMemStats(&before)
		err := validateTree([]stepInput{condIn(">", amount, nil, nil)})
		runtime.ReadMemStats(&after)
		done <- result{err, after.TotalAlloc - before.TotalAlloc}
	}()
	select {
	case r := <-done:
		if r.alloc > condAmountCostBudget {
			t.Errorf("cond_amount %q allocated %dMB — its exponent reached a magnitude comparison", amount, r.alloc>>20)
		}
		return r.err, r.alloc, true
	case <-time.After(time.Second):
		return nil, 0, false
	}
}

// TestPolicy_ValidateCondAmountAnswersAHugeExponentFast: "1e100000000" is eleven bytes
// inside maxPolicyBodyBytes and cannot be a legal numeric(14,2), but a magnitude
// comparison rescales it into a 10^8-digit integer before it can say so — 23.8s and
// 330MB for one call, on the request goroutine, pre-tx. "1e2147483647" is the reachable
// ceiling and did not answer at all inside a minute. The exponent has to be refused
// before any magnitude comparison.
func TestPolicy_ValidateCondAmountAnswersAHugeExponentFast(t *testing.T) {
	// "1e10000000" answered in 604ms before the bound — inside the deadline on this
	// machine and outside it on a slower one, so the allocation ceiling is what catches
	// it either way.
	for _, amount := range []string{"1e10000000", "1e100000000", "1e2147483647"} {
		t.Run(amount, func(t *testing.T) {
			err, _, answered := costOfValidating(t, amount)
			if !answered {
				t.Fatalf("validateTree with cond_amount %q had not answered after 1s — a value this "+
					"short burns CPU and heap in the request goroutine", amount)
			}
			if !errors.Is(err, ErrValidation) {
				t.Errorf("err = %v, want ErrValidation", err)
			}
		})
	}
}

// TestPolicy_ValidateCondAmountRejectsAZeroTheColumnCannotHold: a zero coefficient skips
// the exponent bound entirely, so the validator accepts every zero at every exponent.
// numeric does not: measured on :5433, '0e100000000'::numeric(14,2) is 0.00 but
// '0e2000000000' and '0e2147483647' both raise "value overflows numeric format" —
// SQLSTATE 22003, no constraint name, so policyStatusForErr answers 500 where this
// design promises 400. That is the defect class the cond_amount bound exists to close.
// Rejecting the whole band is the safe direction and costs nothing real: the accepted
// grammar is already a strict subset of numeric's.
func TestPolicy_ValidateCondAmountRejectsAZeroTheColumnCannotHold(t *testing.T) {
	// "0e13" is the first rejected zero: zero follows the same exponent rule as every
	// other value, one past the "0e12" its sibling test pins as legal.
	for _, amount := range []string{"0e13", "0e2147483647", "-0e2147483647", "0.00e2147483647", "0e1000000000"} {
		t.Run(amount, func(t *testing.T) {
			err, _, answered := costOfValidating(t, amount)
			if !answered {
				t.Fatalf("validateTree with cond_amount %q had not answered after 1s", amount)
			}
			if !errors.Is(err, ErrValidation) {
				t.Errorf("err = %v, want ErrValidation — numeric(14,2) refuses this value", err)
			}
		})
	}
}

// TestPolicy_ValidateCondAmountKeepsZeroLegal: the fix for the test above must not cost
// the zero every real caller sends.
func TestPolicy_ValidateCondAmountKeepsZeroLegal(t *testing.T) {
	for _, amount := range []string{"0", "0.00", "-0.00", "0e0", "0.0", "0e12"} {
		t.Run(amount, func(t *testing.T) {
			if err := validateTree([]stepInput{condIn(">", amount, nil, nil)}); err != nil {
				t.Errorf("err = %v, want nil", err)
			}
		})
	}
}

// TestPolicy_NormalizeNameUnicode: the column is unbounded text, so the only rule is
// the trim — and what counts as trimmable is unicode.IsSpace, not ASCII.
func TestPolicy_NormalizeNameUnicode(t *testing.T) {
	cases := []struct {
		name    string
		in      string
		want    string
		wantErr bool
	}{
		{"a plain name", "Sign-off", "Sign-off", false},
		{"outer ascii space", "  Sign-off  ", "Sign-off", false},
		{"a non-breaking space is trimmed", " Sign-off ", "Sign-off", false},
		{"an ideographic space is trimmed", "　Sign-off", "Sign-off", false},
		{"inner runs survive byte-exact", "Ekwuo · B2G  approvals", "Ekwuo · B2G  approvals", false},
		{"an emoji name is a name", "🚀", "🚀", false},
		{"a zero-width space is not whitespace", "​", "​", false},
		{"empty", "", "", true},
		{"ascii whitespace only", " \t\n ", "", true},
		// A NUL is not whitespace, so the trim leaves it and text refuses it: 22021.
		{"an embedded NUL", "Sign\x00off", "", true},
		{"a NUL alone", "\x00", "", true},
		{"a trailing NUL", "Sign-off\x00", "", true},
		{"non-breaking space only", "  ", "", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := normalizeName(tc.in)
			if tc.wantErr {
				if !errors.Is(err, ErrValidation) {
					t.Errorf("err = %v, want ErrValidation", err)
				}
				if got != "" {
					t.Errorf("value = %q, want the empty string alongside the error", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("err = %v, want nil", err)
			}
			if got != tc.want {
				t.Errorf("normalizeName(%q) = %q (% x), want %q (% x)", tc.in, got, got, tc.want, tc.want)
			}
		})
	}
}

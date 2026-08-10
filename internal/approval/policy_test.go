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
	"net/http"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/SimonOsipov/invoice-os/internal/platform/db"
)

// --- fixtures ---------------------------------------------------------------

// scopeAllInvoices is spelled here rather than read from the implementation: a test
// that asserts against the constant it is checking asserts nothing.
const scopeAllInvoices = "All invoices"

// removedScopes are the five WF_SCOPE_OPTIONS entries the server refuses, copied
// byte-for-byte from frontend/app/src/lib/workflows.ts:107-114. The B2G entry's
// separator is U+00B7 MIDDLE DOT, pinned by TestPolicy_NormalizeScopeTable.
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

	rows, ids := flattenSteps(tree)
	if len(rows) != 3 {
		t.Fatalf("rows = %d, want 3", len(rows))
	}
	if len(ids) != len(rows) {
		t.Errorf("ids = %d, want one per row (%d)", len(ids), len(rows))
	}
	for _, id := range ids {
		if _, err := uuid.Parse(id); err != nil {
			t.Errorf("returned id %q is not a uuid: %v", id, err)
		}
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
	again, _ := flattenSteps(tree)
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

	rows, _ := flattenSteps(tree)
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

	rows, _ := flattenSteps(tree)
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

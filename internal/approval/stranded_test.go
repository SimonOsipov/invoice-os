package approval

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/SimonOsipov/invoice-os/internal/platform/db"
)

func strandedFor(t *testing.T, app *pgxpool.Pool, tenantID string) []StrandedRun {
	t.Helper()
	var rows []StrandedRun
	err := db.WithinTenantTxOpts(context.Background(), app, tenantID, pgx.TxOptions{AccessMode: pgx.ReadOnly}, func(tx pgx.Tx) error {
		var err error
		rows, err = StrandedRunsTx(context.Background(), tx)
		return err
	})
	if err != nil {
		t.Fatalf("StrandedRunsTx: %v", err)
	}
	return rows
}

func byInvoice(rows []StrandedRun) map[string]StrandedRun {
	m := make(map[string]StrandedRun, len(rows))
	for _, r := range rows {
		m[r.InvoiceID] = r
	}
	return m
}

// staffWith staffs roleID with a fresh member of the given membership role and status.
func staffWith(t *testing.T, f runFixture, roleID, role, status string) string {
	t.Helper()
	user := uuid.NewString()
	seedMembership(t, f.super, f.tenantID, user, role, status)
	staffWorkflowRole(t, f.super, f.tenantID, roleID, user, 0)
	return user
}

func staffedByActiveReviewer(t *testing.T, f runFixture, key, title string) string {
	t.Helper()
	id := seedWorkflowRole(t, f.super, f.tenantID, key, title)
	staffWith(t, f, id, "reviewer", "active")
	return id
}

func deadRole(t *testing.T, f runFixture, key, title string) {
	t.Helper()
	id := staffedByActiveReviewer(t, f, key, title)
	softDeleteWorkflowRole(t, f.super, id)
}

func TestStranded_ListsAnOpenRunWhoseRoleIsDeleted(t *testing.T) {
	f := newRunFixture(t, "Stranded deleted")
	deadRole(t, f, "tax", "Tax Reviewer")
	inv, _ := seedOpenRun(t, f.super, f.tenantID, f.entityID, "INV-1", f.versionID, pendingOn("tax"))

	rows := strandedFor(t, f.app, f.tenantID)
	if len(rows) != 1 {
		t.Fatalf("rows = %+v, want one", rows)
	}
	r := rows[0]
	if r.InvoiceID != inv || r.Reason != StrandedDeleted || r.RoleKey != "tax" || r.RoleTitle != "Tax Reviewer" {
		t.Errorf("row = %+v", r)
	}
}

func TestStranded_ListsAnOpenRunWhoseRoleHasNoHolder(t *testing.T) {
	f := newRunFixture(t, "Stranded no holder")
	seedWorkflowRole(t, f.super, f.tenantID, "empty", "Empty")
	staffWith(t, f, seedWorkflowRole(t, f.super, f.tenantID, "susp", "Suspended"), "reviewer", "suspended")
	staffWith(t, f, seedWorkflowRole(t, f.super, f.tenantID, "prep", "Preparer"), "preparer", "active")
	want := map[string]string{}
	for i, k := range []string{"empty", "susp", "prep"} {
		inv, _ := seedOpenRun(t, f.super, f.tenantID, f.entityID, "INV-"+k+string(rune('0'+i)), f.versionID, pendingOn(k))
		want[inv] = k
	}

	rows := strandedFor(t, f.app, f.tenantID)
	if len(rows) != 3 {
		t.Fatalf("rows = %+v, want three", rows)
	}
	got := byInvoice(rows)
	for inv, k := range want {
		if got[inv].Reason != StrandedNoHolder || got[inv].RoleKey != k {
			t.Errorf("role %s: row = %+v, want no holder", k, got[inv])
		}
	}
}

func TestStranded_SkipsStaffedClosedAndNonValidatedRuns(t *testing.T) {
	f := newRunFixture(t, "Stranded skips")
	staffedByActiveReviewer(t, f, "held", "Held")
	deadRole(t, f, "dead", "Dead")
	seedOpenRun(t, f.super, f.tenantID, f.entityID, "INV-held", f.versionID, pendingOn("held"))
	for _, state := range []string{"approved", "rejected", "cancelled"} {
		_, run := seedOpenRun(t, f.super, f.tenantID, f.entityID, "INV-"+state, f.versionID, pendingOn("dead"))
		if _, err := f.super.Exec(context.Background(), `UPDATE approval_runs SET state = $2 WHERE id = $1`, run, state); err != nil {
			t.Fatalf("close run: %v", err)
		}
	}
	for _, status := range []string{"draft", "queued"} {
		inv, _ := seedOpenRun(t, f.super, f.tenantID, f.entityID, "INV-"+status, f.versionID, pendingOn("dead"))
		setInvoiceStatus(t, f.super, inv, status)
	}
	control, _ := seedOpenRun(t, f.super, f.tenantID, f.entityID, "INV-control", f.versionID, pendingOn("dead"))

	rows := strandedFor(t, f.app, f.tenantID)
	if len(rows) != 1 || rows[0].InvoiceID != control {
		t.Fatalf("rows = %+v, want only the control %s", rows, control)
	}
}

func TestStranded_SkipsRunsWithNoPendingApprovalStep(t *testing.T) {
	f := newRunFixture(t, "Stranded no pending")
	deadRole(t, f, "dead", "Dead")
	seedOpenRun(t, f.super, f.tenantID, f.entityID, "INV-sat", f.versionID,
		runStepSeed{Ord: 0, Kind: "approval", State: "satisfied", Key: ptr("dead")})
	seedOpenRun(t, f.super, f.tenantID, f.entityID, "INV-notify", f.versionID,
		runStepSeed{Ord: 0, Kind: "notify", State: "pending", Key: ptr("dead")})
	control, _ := seedOpenRun(t, f.super, f.tenantID, f.entityID, "INV-control", f.versionID, pendingOn("dead"))

	rows := strandedFor(t, f.app, f.tenantID)
	if len(rows) != 1 || rows[0].InvoiceID != control {
		t.Fatalf("rows = %+v, want only the control %s", rows, control)
	}
}

func TestStranded_ListsAnyPendingApprovalStepAndFlagsTheCurrentOne(t *testing.T) {
	f := newRunFixture(t, "Stranded any step")
	staffedByActiveReviewer(t, f, "held", "Held")
	deadRole(t, f, "dead-a", "Dead A")
	deadRole(t, f, "dead-b", "Dead B")
	seedWorkflowRole(t, f.super, f.tenantID, "empty", "Empty")
	step := func(ord int, kind, state, key string) runStepSeed {
		return runStepSeed{Ord: ord, Kind: kind, State: state, Key: ptr(key)}
	}
	a, _ := seedOpenRun(t, f.super, f.tenantID, f.entityID, "INV-A", f.versionID,
		step(0, "approval", "pending", "held"), step(1, "approval", "pending", "dead-a"))
	b, _ := seedOpenRun(t, f.super, f.tenantID, f.entityID, "INV-B", f.versionID,
		step(0, "approval", "satisfied", "held"), step(1, "approval", "pending", "dead-a"), step(2, "approval", "pending", "held"))
	c, _ := seedOpenRun(t, f.super, f.tenantID, f.entityID, "INV-C", f.versionID,
		step(0, "approval", "pending", "dead-b"), step(1, "approval", "pending", "empty"))
	d, _ := seedOpenRun(t, f.super, f.tenantID, f.entityID, "INV-D", f.versionID,
		step(0, "notify", "pending", "dead-b"), step(1, "approval", "pending", "dead-a"))

	rows := strandedFor(t, f.app, f.tenantID)
	if len(rows) != 4 {
		t.Fatalf("rows = %+v, want one per run", rows)
	}
	got := byInvoice(rows)
	for _, w := range []struct{ inv, key, blocks string }{
		{a, "dead-a", BlocksLater}, {b, "dead-a", BlocksNow}, {c, "dead-b", BlocksNow}, {d, "dead-a", BlocksNow},
	} {
		if got[w.inv].RoleKey != w.key || got[w.inv].Blocks != w.blocks {
			t.Errorf("run %s: row = %+v, want key %s blocks %s", w.inv, got[w.inv], w.key, w.blocks)
		}
	}
}

func TestStranded_NullAndUnknownKeysAreListedAsDeleted(t *testing.T) {
	f := newRunFixture(t, "Stranded null key")
	nullInv, _ := seedOpenRun(t, f.super, f.tenantID, f.entityID, "INV-null", f.versionID,
		runStepSeed{Ord: 0, Kind: "approval", State: "pending", Key: nil})
	unkInv, _ := seedOpenRun(t, f.super, f.tenantID, f.entityID, "INV-unk", f.versionID, pendingOn("no-such-role"))

	rows := strandedFor(t, f.app, f.tenantID)
	if len(rows) != 2 {
		t.Fatalf("rows = %+v, want two", rows)
	}
	got := byInvoice(rows)
	if r := got[nullInv]; r.Reason != StrandedDeleted || r.RoleKey != "" || r.RoleTitle != "" {
		t.Errorf("null key row = %+v", r)
	}
	if r := got[unkInv]; r.Reason != StrandedDeleted || r.RoleKey != "no-such-role" || r.RoleTitle != "" {
		t.Errorf("unknown key row = %+v", r)
	}
}

func TestStranded_RowCarriesWorkspaceInvoiceRoleReasonArmedAtAndBlocks(t *testing.T) {
	f := newRunFixture(t, "Stranded Co")
	seedWorkflowRole(t, f.super, f.tenantID, "ap-lead", "AP Lead")
	inv, run := seedOpenRun(t, f.super, f.tenantID, f.entityID, "INV-S-1", f.versionID, pendingOn("ap-lead"))
	var opened time.Time
	if err := f.super.QueryRow(context.Background(), `SELECT opened_at FROM approval_runs WHERE id = $1`, run).Scan(&opened); err != nil {
		t.Fatalf("read opened_at: %v", err)
	}

	rows := strandedFor(t, f.app, f.tenantID)
	if len(rows) != 1 {
		t.Fatalf("rows = %+v, want one", rows)
	}
	want := StrandedRun{
		WorkspaceID: f.tenantID, Workspace: "Stranded Co", InvoiceID: inv, InvoiceNumber: "INV-S-1",
		RoleKey: "ap-lead", RoleTitle: "AP Lead", Reason: StrandedNoHolder, Blocks: BlocksNow, ArmedAt: opened,
	}
	if !rows[0].ArmedAt.Equal(opened) {
		t.Errorf("ArmedAt = %v, want %v", rows[0].ArmedAt, opened)
	}
	rows[0].ArmedAt = opened
	if rows[0] != want {
		t.Errorf("row = %+v, want %+v", rows[0], want)
	}
}

// memberIn staffs roleID with a new member of the given spec.
func memberIn(t *testing.T, f runFixture, roleID string, s memberSpec) string {
	t.Helper()
	s.roleID = roleID
	return seedMemberOf(t, f.super, f.tenantID, s)
}

func limitedTo(ents ...string) memberSpec {
	return memberSpec{role: "reviewer", limited: true, entityIDs: ents}
}

func TestStranded_AgreesWithUnsignableReasons(t *testing.T) {
	f := newRunFixture(t, "Stranded agrees")
	entB := seedBusinessEntity(t, f.super, f.tenantID, "Stranded agrees B")
	var keys []string
	add := func(key string, specs ...memberSpec) string {
		id := seedWorkflowRole(t, f.super, f.tenantID, key, key)
		for _, sp := range specs {
			memberIn(t, f, id, sp)
		}
		keys = append(keys, key)
		return id
	}
	add("admin", memberSpec{role: "admin"})
	add("reviewer", memberSpec{role: "reviewer"})
	add("suspended", memberSpec{role: "reviewer", status: "suspended"})
	add("preparer", memberSpec{role: "preparer"})
	add("empty")
	softDeleteWorkflowRole(t, f.super, add("deleted", memberSpec{role: "reviewer"}))
	add("reach", limitedTo(entB))
	creatorID := add("creator")
	creator := memberIn(t, f, creatorID, memberSpec{role: "reviewer"})
	add("deleg") // empty: its step names an active reviewer outside every role
	add("deleg-off")
	delegate := memberIn(t, f, "", memberSpec{role: "reviewer"})

	invByKey := map[string]string{}
	var probes []SignerProbe
	for _, k := range keys {
		inv, run := seedOpenRun(t, f.super, f.tenantID, f.entityID, "INV-"+k, f.versionID, pendingOn(k))
		invByKey[k] = inv
		p := SignerProbe{InvoiceID: inv, RoleKey: ptr(k)}
		if k == "deleg" {
			setStepDelegation(t, f.super, oneApprovalStep(t, f.super, run, 0), true, &delegate)
			p.AllowDelegation, p.DelegateUserID = true, &delegate
		}
		probes = append(probes, p)
	}
	seedHistory(t, f.super, f.tenantID, invByKey["creator"], nil, creator)

	reasons := readReasons(t, f.app, f.tenantID, probes)
	listed := byInvoice(strandedFor(t, f.app, f.tenantID))
	seen := map[string]bool{}
	for i, k := range keys {
		seen[reasons[i]] = true
		row, isListed := listed[invByKey[k]]
		if isListed != (reasons[i] != "") || row.Reason != reasons[i] {
			t.Errorf("role %s: listed = %v with %q, unsignableReasonsTx = %q", k, isListed, row.Reason, reasons[i])
		}
	}
	idx := func(k string) int { return slices.Index(keys, k) }
	if reasons[idx("deleg")] != "" {
		t.Errorf("role deleg: unsignableReasonsTx = %q, want \"\" (an eligible delegate decides)", reasons[idx("deleg")])
	}
	if _, isListed := listed[invByKey["deleg"]]; isListed {
		t.Errorf("role deleg: listed as stranded, but an eligible delegate can decide it")
	}
	if r := listed[invByKey["deleg-off"]]; r.Reason != StrandedNoHolder || reasons[idx("deleg-off")] != StrandedNoHolder {
		t.Errorf("role deleg-off: listed %+v, reason %q, want no holder from both", r, reasons[idx("deleg-off")])
	}
	for _, want := range []string{"", StrandedDeleted, StrandedNoHolder, StrandedOutOfReach, StrandedCreatorOnly} {
		if !seen[want] {
			t.Errorf("the fixture yields no %q: reasons = %q", want, reasons)
		}
	}
}

func TestStranded_ListsACreatorOnlyRun(t *testing.T) {
	f := newRunFixture(t, "Stranded creator only")
	roleID := seedWorkflowRole(t, f.super, f.tenantID, "c-role", "C Role")
	c := memberIn(t, f, roleID, memberSpec{role: "reviewer"})
	inv, _ := seedOpenRun(t, f.super, f.tenantID, f.entityID, "INV-1", f.versionID, pendingOn("c-role"))
	seedHistory(t, f.super, f.tenantID, inv, nil, c)

	rows := strandedFor(t, f.app, f.tenantID)
	if len(rows) != 1 || rows[0].InvoiceID != inv || rows[0].Reason != StrandedCreatorOnly || rows[0].Blocks != BlocksNow {
		t.Fatalf("rows = %+v, want one creator only row blocking now", rows)
	}
}

func TestStranded_ListsAnOutOfReachRun(t *testing.T) {
	f := newRunFixture(t, "Stranded out of reach")
	entB := seedBusinessEntity(t, f.super, f.tenantID, "Stranded out of reach B")
	memberIn(t, f, seedWorkflowRole(t, f.super, f.tenantID, "r-role", "R Role"), limitedTo(entB))
	inv, _ := seedOpenRun(t, f.super, f.tenantID, f.entityID, "INV-1", f.versionID, pendingOn("r-role"))

	rows := strandedFor(t, f.app, f.tenantID)
	if len(rows) != 1 || rows[0].InvoiceID != inv || rows[0].Reason != StrandedOutOfReach {
		t.Fatalf("rows = %+v, want one out of client reach row", rows)
	}
}

func TestStranded_SkipsARunOneMemberCanApprove(t *testing.T) {
	f := newRunFixture(t, "Stranded one member")
	entB := seedBusinessEntity(t, f.super, f.tenantID, "Stranded one member B")
	mixed := seedWorkflowRole(t, f.super, f.tenantID, "mixed", "Mixed")
	memberIn(t, f, mixed, limitedTo(entB))
	memberIn(t, f, mixed, limitedTo(f.entityID))
	seedOpenRun(t, f.super, f.tenantID, f.entityID, "INV-mixed", f.versionID, pendingOn("mixed"))

	cr := seedWorkflowRole(t, f.super, f.tenantID, "cr", "CR")
	c := memberIn(t, f, cr, memberSpec{role: "reviewer"})
	memberIn(t, f, cr, memberSpec{role: "reviewer"})
	inv, _ := seedOpenRun(t, f.super, f.tenantID, f.entityID, "INV-cr", f.versionID, pendingOn("cr"))
	seedHistory(t, f.super, f.tenantID, inv, nil, c)

	if rows := strandedFor(t, f.app, f.tenantID); len(rows) != 0 {
		t.Fatalf("rows = %+v, want none", rows)
	}
}

func TestStranded_PicksTheFirstStepNobodyCanApprove(t *testing.T) {
	f := newRunFixture(t, "Stranded first step")
	entB := seedBusinessEntity(t, f.super, f.tenantID, "Stranded first step B")
	staffedByActiveReviewer(t, f, "ok", "Ok")
	memberIn(t, f, seedWorkflowRole(t, f.super, f.tenantID, "far", "Far"), limitedTo(entB))
	deadRole(t, f, "dead", "Dead")
	step := func(ord int, key string) runStepSeed {
		return runStepSeed{Ord: ord, Kind: "approval", State: "pending", Key: ptr(key)}
	}
	inv, _ := seedOpenRun(t, f.super, f.tenantID, f.entityID, "INV-1", f.versionID, step(0, "ok"), step(1, "far"), step(2, "dead"))

	rows := strandedFor(t, f.app, f.tenantID)
	if len(rows) != 1 || rows[0].InvoiceID != inv || rows[0].RoleKey != "far" || rows[0].Reason != StrandedOutOfReach || rows[0].Blocks != BlocksLater {
		t.Fatalf("rows = %+v, want one row on step far, out of client reach, later", rows)
	}
}

func TestStranded_ThreeRunsTwoTenantsKeepOrder(t *testing.T) {
	a := newRunFixture(t, "Stranded order A")
	b := newRunFixture(t, "Stranded order B")
	entB := seedBusinessEntity(t, a.super, a.tenantID, "Stranded order A2")
	cr := seedWorkflowRole(t, a.super, a.tenantID, "cr", "CR")
	c := memberIn(t, a, cr, memberSpec{role: "reviewer"})
	staffedByActiveReviewer(t, a, "ok", "Ok")
	memberIn(t, a, seedWorkflowRole(t, a.super, a.tenantID, "far", "Far"), limitedTo(entB))
	seedWorkflowRole(t, b.super, b.tenantID, "empty", "Empty")

	r1, run1 := seedOpenRun(t, a.super, a.tenantID, a.entityID, "A-1", a.versionID, pendingOn("cr"))
	seedHistory(t, a.super, a.tenantID, r1, nil, c)
	_, run2 := seedOpenRun(t, a.super, a.tenantID, a.entityID, "A-2", a.versionID, pendingOn("ok"))
	r3, run3 := seedOpenRun(t, a.super, a.tenantID, a.entityID, "A-3", a.versionID, pendingOn("far"))
	bInv, _ := seedOpenRun(t, b.super, b.tenantID, b.entityID, "B-1", b.versionID, pendingOn("empty"))
	for i, run := range []string{run3, run2, run1} { // opened 30, 20, 10 minutes ago
		if _, err := a.super.Exec(context.Background(),
			`UPDATE approval_runs SET opened_at = now() - make_interval(mins => $2) WHERE id = $1`, run, 30-i*10); err != nil {
			t.Fatalf("set opened_at: %v", err)
		}
	}
	rows, err := ListStranded(context.Background(), a.app, []string{a.tenantID, b.tenantID})
	if err != nil {
		t.Fatalf("ListStranded: %v", err)
	}
	if len(rows) != 3 || rows[0].InvoiceID != r3 || rows[1].InvoiceID != r1 || rows[2].InvoiceID != bInv {
		t.Fatalf("rows = %+v, want A's [r3, r1] by opened_at then B's run", rows)
	}
	if rows[0].Reason != StrandedOutOfReach || rows[1].Reason != StrandedCreatorOnly || rows[2].WorkspaceID != b.tenantID {
		t.Errorf("rows = %+v", rows)
	}
}

func TestStranded_InHouseReviewerWithoutClientsIsNotListed(t *testing.T) {
	f := newRunFixture(t, "Stranded in-house")
	memberIn(t, f, seedWorkflowRole(t, f.super, f.tenantID, "r", "R"), memberSpec{role: "reviewer", limited: true})
	inv, _ := seedOpenRun(t, f.super, f.tenantID, f.entityID, "INV-1", f.versionID, pendingOn("r"))
	setKind := func(kind string) {
		if _, err := f.super.Exec(context.Background(), `UPDATE tenants SET kind = $2 WHERE id = $1`, f.tenantID, kind); err != nil {
			t.Fatalf("set tenant kind: %v", err)
		}
	}

	setKind("in_house")
	if rows := strandedFor(t, f.app, f.tenantID); len(rows) != 0 {
		t.Fatalf("in-house rows = %+v, want none", rows)
	}
	setKind("firm")
	rows := strandedFor(t, f.app, f.tenantID)
	if len(rows) != 1 || rows[0].InvoiceID != inv || rows[0].Reason != StrandedOutOfReach {
		t.Fatalf("firm rows = %+v, want one out of client reach", rows)
	}
}

func TestStranded_SignerReadErrorIsReturned(t *testing.T) {
	f := newRunFixture(t, "Stranded signer error")
	seedWorkflowRole(t, f.super, f.tenantID, "x", "X")
	seedOpenRun(t, f.super, f.tenantID, f.entityID, "INV-1", f.versionID, pendingOn("x"))

	var rows []StrandedRun
	err := db.WithinTenantTxOpts(context.Background(), failingAppPool(t, signMarker), f.tenantID, pgx.TxOptions{AccessMode: pgx.ReadOnly}, func(tx pgx.Tx) error {
		var err error
		rows, err = StrandedRunsTx(context.Background(), tx)
		return err
	})
	if err == nil || rows != nil {
		t.Fatalf("rows = %+v, err = %v, want an error and no rows", rows, err)
	}
}

func TestStranded_AnotherTenantsRunsAreNotListed(t *testing.T) {
	a := newRunFixture(t, "Stranded tenant A")
	b := newRunFixture(t, "Stranded tenant B")
	staffedByActiveReviewer(t, a, "lead", "Lead")
	seedWorkflowRole(t, a.super, a.tenantID, "x", "X")
	seedOpenRun(t, a.super, a.tenantID, a.entityID, "A-ctl", a.versionID, pendingOn("lead"))
	seedOpenRun(t, a.super, a.tenantID, a.entityID, "A-1", a.versionID, pendingOn("x"))
	seedOpenRun(t, a.super, a.tenantID, a.entityID, "A-2", a.versionID, pendingOn("x"))
	seedWorkflowRole(t, b.super, b.tenantID, "lead", "Lead")
	for _, n := range []string{"B-1", "B-2", "B-3"} {
		seedOpenRun(t, b.super, b.tenantID, b.entityID, n, b.versionID, pendingOn("lead"))
	}

	for _, c := range []struct {
		f    runFixture
		want int
	}{{a, 2}, {b, 3}} {
		rows := strandedFor(t, c.f.app, c.f.tenantID)
		if len(rows) != c.want {
			t.Errorf("tenant %s: %d rows, want %d", c.f.tenantID, len(rows), c.want)
		}
		for _, r := range rows {
			if r.WorkspaceID != c.f.tenantID {
				t.Errorf("tenant %s listed a row of %s", c.f.tenantID, r.WorkspaceID)
			}
		}
	}
}

func TestStranded_AnotherTenantsRoleDoesNotStaffThisRun(t *testing.T) {
	a := newRunFixture(t, "Stranded role A")
	b := newRunFixture(t, "Stranded role B")
	deadRole(t, a, "lead", "Lead")
	staffedByActiveReviewer(t, b, "lead", "Lead")
	inv, _ := seedOpenRun(t, a.super, a.tenantID, a.entityID, "A-1", a.versionID, pendingOn("lead"))

	rows := strandedFor(t, a.app, a.tenantID)
	if len(rows) != 1 || rows[0].InvoiceID != inv || rows[0].Reason != StrandedDeleted {
		t.Fatalf("rows = %+v, want A's run as deleted", rows)
	}

	// RLS hides B's roles from A's transaction, so the join's tenant pin is only visible without RLS:
	// A's held role must not be judged by B's empty role of the same key.
	staffedByActiveReviewer(t, a, "held", "Held")
	seedWorkflowRole(t, b.super, b.tenantID, "held", "Held")
	heldInv, _ := seedOpenRun(t, a.super, a.tenantID, a.entityID, "A-held", a.versionID, pendingOn("held"))
	tx, err := a.super.BeginTx(context.Background(), pgx.TxOptions{AccessMode: pgx.ReadOnly})
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	all, err := StrandedRunsTx(context.Background(), tx)
	if err != nil {
		t.Fatalf("StrandedRunsTx as superuser: %v", err)
	}
	got := byInvoice(all)
	if _, listed := got[heldInv]; listed {
		t.Errorf("A's run on its staffed role was listed because B's role of the same key is empty")
	}
	if _, listed := got[inv]; !listed {
		t.Errorf("A's run on its deleted role is missing")
	}
}

func TestStrandedRunsTx_RefusesAReadWriteTx(t *testing.T) {
	f := newRunFixture(t, "Stranded rw")
	seedWorkflowRole(t, f.super, f.tenantID, "x", "X")
	seedOpenRun(t, f.super, f.tenantID, f.entityID, "INV-1", f.versionID, pendingOn("x"))

	var rows []StrandedRun
	err := db.WithinTenantTx(context.Background(), f.app, f.tenantID, func(tx pgx.Tx) error {
		var err error
		rows, err = StrandedRunsTx(context.Background(), tx)
		return err
	})
	if !errors.Is(err, ErrNotReadOnly) || rows != nil {
		t.Fatalf("read-write tx: rows = %+v, err = %v, want ErrNotReadOnly and no rows", rows, err)
	}
	if got := strandedFor(t, f.app, f.tenantID); len(got) != 1 {
		t.Errorf("read-only tx: %d rows, want 1", len(got))
	}
}

func TestListStranded_AWriteInsideTheToolsTxFails(t *testing.T) {
	f := newRunFixture(t, "Stranded write probe")
	seedWorkflowRole(t, f.super, f.tenantID, "x", "Original")
	probe := func(tx pgx.Tx) error {
		_, err := tx.Exec(context.Background(), `UPDATE workflow_roles SET title = 'x' WHERE tenant_id = $1`, f.tenantID)
		return err
	}

	rows, err := listStranded(context.Background(), f.app, []string{f.tenantID}, probe)
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "25006" {
		t.Fatalf("err = %v, want SQLSTATE 25006", err)
	}
	if rows != nil {
		t.Errorf("rows = %+v, want none on error", rows)
	}
	var title string
	if err := f.super.QueryRow(context.Background(), `SELECT title FROM workflow_roles WHERE tenant_id = $1`, f.tenantID).Scan(&title); err != nil {
		t.Fatalf("read title: %v", err)
	}
	if title != "Original" {
		t.Errorf("title = %q, want unchanged Original", title)
	}
}

func TestListStranded_ReadsEveryGivenTenantInOrder(t *testing.T) {
	a := newRunFixture(t, "Stranded list A")
	b := newRunFixture(t, "Stranded list B")
	staffedByActiveReviewer(t, a, "lead", "Lead")
	seedWorkflowRole(t, a.super, a.tenantID, "x", "X")
	seedOpenRun(t, a.super, a.tenantID, a.entityID, "A-ctl", a.versionID, pendingOn("lead"))
	seedOpenRun(t, a.super, a.tenantID, a.entityID, "A-1", a.versionID, pendingOn("x"))
	seedOpenRun(t, a.super, a.tenantID, a.entityID, "A-2", a.versionID, pendingOn("x"))
	seedWorkflowRole(t, b.super, b.tenantID, "x", "X")
	for _, n := range []string{"B-1", "B-2", "B-3"} {
		seedOpenRun(t, b.super, b.tenantID, b.entityID, n, b.versionID, pendingOn("x"))
	}

	rows, err := ListStranded(context.Background(), a.app, []string{b.tenantID, a.tenantID})
	if err != nil {
		t.Fatalf("ListStranded: %v", err)
	}
	if len(rows) != 5 {
		t.Fatalf("rows = %+v, want 5", rows)
	}
	seen := map[string]bool{}
	for i, r := range rows {
		want := b.tenantID
		if i >= 3 {
			want = a.tenantID
		}
		if r.WorkspaceID != want || seen[r.InvoiceID] {
			t.Errorf("row %d = %+v, want workspace %s and a new invoice", i, r, want)
		}
		seen[r.InvoiceID] = true
	}
}

func TestListStranded_NoTenantsIsEmpty(t *testing.T) {
	_, app := dbTestPools(t)
	rows, err := ListStranded(context.Background(), app, nil)
	if err != nil || len(rows) != 0 {
		t.Fatalf("rows = %+v, err = %v, want none", rows, err)
	}
}

func TestListStranded_NamesTheTenantThatFails(t *testing.T) {
	f := newRunFixture(t, "Stranded names failure")
	seedWorkflowRole(t, f.super, f.tenantID, "x", "X")
	seedOpenRun(t, f.super, f.tenantID, f.entityID, "INV-1", f.versionID, pendingOn("x"))

	rows, err := ListStranded(context.Background(), f.app, []string{f.tenantID, "not-a-uuid"})
	if err == nil || !strings.Contains(err.Error(), "workspace not-a-uuid") || !errors.Is(err, db.ErrNoTenant) {
		t.Fatalf("err = %v, want one naming workspace not-a-uuid wrapping ErrNoTenant", err)
	}
	if rows != nil {
		t.Errorf("rows = %+v, want nil", rows)
	}
}

func TestStranded_ADeletedRoleWithNoMembersIsDeletedNotNoHolder(t *testing.T) {
	f := newRunFixture(t, "Stranded deleted empty")
	softDeleteWorkflowRole(t, f.super, seedWorkflowRole(t, f.super, f.tenantID, "gone", "Gone"))
	inv, _ := seedOpenRun(t, f.super, f.tenantID, f.entityID, "INV-1", f.versionID, pendingOn("gone"))

	rows := strandedFor(t, f.app, f.tenantID)
	if len(rows) != 1 || rows[0].InvoiceID != inv || rows[0].Reason != StrandedDeleted {
		t.Fatalf("rows = %+v, want one run, reason deleted", rows)
	}
}

func TestListStranded_ReadsATenantScheduledForDeletion(t *testing.T) {
	f := newRunFixture(t, "Stranded scheduled")
	seedWorkflowRole(t, f.super, f.tenantID, "x", "X")
	inv, _ := seedOpenRun(t, f.super, f.tenantID, f.entityID, "INV-1", f.versionID, pendingOn("x"))
	if _, err := f.super.Exec(context.Background(), `UPDATE tenants SET deletion_scheduled_at = now(), deletion_scheduled_by = $2 WHERE id = $1`, f.tenantID, uuid.NewString()); err != nil {
		t.Fatalf("schedule deletion: %v", err)
	}

	rows, err := ListStranded(context.Background(), f.app, []string{f.tenantID})
	if err != nil || len(rows) != 1 || rows[0].InvoiceID != inv {
		t.Fatalf("rows = %+v, err = %v, want the scheduled tenant's one run", rows, err)
	}
}

// A delegate who could approve the step counts as someone who can decide it (DELEG-01 hand-off).
func TestStranded_AnEligibleDelegateCountsAsADecider(t *testing.T) {
	for _, c := range []struct {
		name     string
		delegate func(t *testing.T, f runFixture) *string
		want     string // "" = not listed
	}{
		{"named", func(t *testing.T, f runFixture) *string {
			d := uuid.NewString()
			seedMembership(t, f.super, f.tenantID, d, "reviewer", "active")
			return &d
		}, ""},
		{"anyone", func(*testing.T, runFixture) *string { return nil }, ""},
		{"named ineligible", func(t *testing.T, f runFixture) *string {
			d := uuid.NewString()
			seedMembership(t, f.super, f.tenantID, d, "reviewer", "suspended")
			return &d
		}, StrandedNoHolder},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := newRunFixture(t, "APSC-14-07 stranded "+c.name)
			staffWith(t, f, seedWorkflowRole(t, f.super, f.tenantID, "susp", "Suspended"), "reviewer", "suspended")
			inv, runID := seedOpenRun(t, f.super, f.tenantID, f.entityID, "INV-"+c.name, f.versionID, pendingOn("susp"))
			setStepDelegation(t, f.super, oneApprovalStep(t, f.super, runID, 0), true, c.delegate(t, f))

			rows := strandedFor(t, f.app, f.tenantID)
			if c.want == "" {
				if len(rows) != 0 {
					t.Fatalf("rows = %+v, want none: an eligible delegate decides the step", rows)
				}
				return
			}
			if len(rows) != 1 || rows[0].InvoiceID != inv || rows[0].Reason != c.want {
				t.Fatalf("rows = %+v, want the run listed once with reason %q", rows, c.want)
			}
		})
	}
}

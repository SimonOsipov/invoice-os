package invoice

// AUDIT-02-03 (task-608) Mode A: RED specs for actor resolution on
// Store.History. Store.History must populate StatusChange.ActorName/.ActorKind
// from ONE batched actor.Resolve call inside its existing
// db.WithinRequestTenantTx, leaving Actor byte-identical to the stored value.
//
// Written before the implementation exists. Reuses store_test.go's
// dbTestPools/seedTenant/seedEntity/mustCount and transition_gate_test.go's
// tracedAppPool/sqlRecorder.
//
// Run: `DEV_DB_PORT=5433 make test-invoice`.

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
	"github.com/SimonOsipov/invoice-os/internal/platform/db"
	"github.com/SimonOsipov/invoice-os/internal/submission"
)

// The three wire values StatusChange.ActorKind may carry. Literals, not
// actor.Kind constants: this is the JSON contract both mirrors copy, so a
// rename of the Go constant must not silently move it.
const (
	kindPerson = "person"
	kindSystem = "system"
	kindRaw    = "raw"
)

// --- fixtures --------------------------------------------------------------

// seedMembershipNamed is seedMembership plus a display_name -- the Q6 ladder's
// top rung, which neither existing helper here can set.
func seedMembershipNamed(t *testing.T, super *pgxpool.Pool, tenantID, userID, displayName string) {
	t.Helper()
	var id string
	if err := super.QueryRow(context.Background(),
		`INSERT INTO memberships (tenant_id, user_id, role, display_name)
		 VALUES ($1, $2, 'admin', $3) RETURNING id`,
		tenantID, userID, displayName,
	).Scan(&id); err != nil {
		t.Fatalf("seed memberships(display_name=%q): %v", displayName, err)
	}
	t.Cleanup(func() {
		_, _ = super.Exec(context.Background(), `DELETE FROM memberships WHERE id = $1`, id)
	})
}

// seedHistoryRow writes one invoice_status_history row as the superuser -- the
// only way to fixture an actor no Store writer can produce here: a stranger's
// uuid, another tenant's member, or the importer's free text.
func seedHistoryRow(t *testing.T, super *pgxpool.Pool, tenantID, invoiceID string, from *Status, to Status, actorValue string, changedAt time.Time) {
	t.Helper()
	var fromVal any
	if from != nil {
		fromVal = string(*from)
	}
	if _, err := super.Exec(context.Background(),
		`INSERT INTO invoice_status_history (tenant_id, invoice_id, from_status, to_status, actor, changed_at)
		 VALUES ($1, $2, $3, $4, $5, $6)`,
		tenantID, invoiceID, fromVal, string(to), actorValue, changedAt,
	); err != nil {
		t.Fatalf("seed invoice_status_history(actor=%q): %v", actorValue, err)
	}
}

func statusPtr(s Status) *Status { return &s }

// rowFor returns the one row actored by want, failing when the count is not
// exactly one -- so no assertion below indexes a slice a fixture change emptied.
func rowFor(t *testing.T, got []StatusChange, want string) StatusChange {
	t.Helper()
	var found []StatusChange
	for _, sc := range got {
		if sc.Actor == want {
			found = append(found, sc)
		}
	}
	if len(found) != 1 {
		t.Fatalf("History returned %d rows actored by %q, want exactly 1; all rows: %+v", len(found), want, got)
	}
	return found[0]
}

// namedInvoice is the shared fixture: a tenant whose caller IS a named member,
// one entity, and one invoice created through the real Store so the genesis row
// is subject-actored. D-12: all 133 seeded history rows are system-actored, so a
// subject-actored row has to be minted, never borrowed from the seed.
type namedInvoice struct {
	tenantID string
	subject  string
	name     string
	invID    string
	ctx      context.Context
	store    *Store
}

func seedNamedInvoice(t *testing.T, super, app *pgxpool.Pool, label, displayName string) namedInvoice {
	t.Helper()
	tenantID := seedTenant(t, super, label+" tenant")
	entityID := seedEntity(t, super, tenantID, label+" entity")
	subject := uuid.NewString()
	seedMembershipNamed(t, super, tenantID, subject, displayName)

	store := NewStore(app)
	c := auth.WithIdentity(context.Background(), auth.Identity{Subject: subject, Role: "authenticated", TenantID: tenantID})
	inv, err := store.Create(c, CreateInput{EntityID: entityID, InvoiceNumber: label})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	return namedInvoice{tenantID: tenantID, subject: subject, name: displayName, invID: inv.ID, ctx: c, store: store}
}

// --- AC-1 / AC-9: a subject-actored row renders a person ---------------------

// TestHistory_ResolvesActorNameOnEveryRow: every row of a history whose actor is
// a member of the caller's tenant carries that member's display_name and the
// person kind.
func TestHistory_ResolvesActorNameOnEveryRow(t *testing.T) {
	super, app := dbTestPools(t)
	fx := seedNamedInvoice(t, super, app, "AUDIT-02-03-NAME", "Chinedu Okafor")

	if _, err := fx.store.Transition(fx.ctx, fx.invID, StatusValidated); err != nil {
		t.Fatalf("Transition(draft->validated): %v", err)
	}

	got, err := fx.store.History(fx.ctx, fx.invID)
	if err != nil {
		t.Fatalf("History: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("History returned %d rows, want 2 (genesis + one transition)", len(got))
	}
	for i, sc := range got {
		if sc.ActorName != fx.name {
			t.Errorf("got[%d].ActorName = %q, want %q (AC-1: the membership display_name, not the subject)", i, sc.ActorName, fx.name)
		}
		if sc.ActorKind != kindPerson {
			t.Errorf("got[%d].ActorKind = %q, want %q", i, sc.ActorKind, kindPerson)
		}
	}
}

// TestHistory_ActorColumnIsUnchanged is the regression fence for the story's Out
// of Scope clause: resolution ADDS two fields, it never rewrites actor.
func TestHistory_ActorColumnIsUnchanged(t *testing.T) {
	super, app := dbTestPools(t)
	fx := seedNamedInvoice(t, super, app, "AUDIT-02-03-VERBATIM", "Chinedu Okafor")

	if _, err := fx.store.Transition(fx.ctx, fx.invID, StatusValidated); err != nil {
		t.Fatalf("Transition(draft->validated): %v", err)
	}

	got, err := fx.store.History(fx.ctx, fx.invID)
	if err != nil {
		t.Fatalf("History: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("History returned %d rows, want 2", len(got))
	}
	for i, sc := range got {
		if sc.Actor != fx.subject {
			t.Errorf("got[%d].Actor = %q, want the stored subject %q byte-for-byte", i, sc.Actor, fx.subject)
		}
	}
	// Without this the fence is vacuous: an implementation that resolved nothing
	// would leave actor untouched and pass the loop above.
	if got[0].ActorName != fx.name {
		t.Errorf("got[0].ActorName = %q, want %q -- the fence above only means something once resolution actually produced a name distinct from the subject", got[0].ActorName, fx.name)
	}
}

// --- AC-4: system rows are the system, not a person -------------------------

// TestHistory_SystemRowsResolveToSystem drives the real system writer
// (MarkFailedTx, the submission worker's queued->failed edge) so the fixture is
// a row production actually stores, and asserts the person rows in the SAME
// response still resolve -- the live positive control.
func TestHistory_SystemRowsResolveToSystem(t *testing.T) {
	super, app := dbTestPools(t)
	fx := seedNamedInvoice(t, super, app, "AUDIT-02-03-SYS", "Folake Adesina")

	for _, target := range []Status{StatusValidated, StatusQueued} {
		if _, err := fx.store.Transition(fx.ctx, fx.invID, target); err != nil {
			t.Fatalf("Transition -> %q: %v", target, err)
		}
	}
	if err := db.WithinTenantTx(context.Background(), app, fx.tenantID, func(tx pgx.Tx) error {
		_, err := fx.store.MarkFailedTx(context.Background(), tx, fx.invID, fx.tenantID, submission.FailurePayloadNotBuilt)
		return err
	}); err != nil {
		t.Fatalf("MarkFailedTx (the system-actored writer): %v", err)
	}

	got, err := fx.store.History(fx.ctx, fx.invID)
	if err != nil {
		t.Fatalf("History: %v", err)
	}
	if len(got) != 4 {
		t.Fatalf("History returned %d rows, want 4 (genesis + 2 transitions + the system failure row)", len(got))
	}

	sys := rowFor(t, got, "system")
	if sys.ActorName != "System" {
		t.Errorf("the system row's ActorName = %q, want %q (AC-4: never a person)", sys.ActorName, "System")
	}
	if sys.ActorKind != kindSystem {
		t.Errorf("the system row's ActorKind = %q, want %q", sys.ActorKind, kindSystem)
	}

	// Positive control in the same call: a Resolve that returned nothing at all
	// would also leave the system row unnamed, so the assertions above alone
	// cannot tell "classified as system" from "resolved nothing".
	subjectRows := 0
	for _, sc := range got {
		if sc.Actor != fx.subject {
			continue
		}
		subjectRows++
		if sc.ActorName != fx.name || sc.ActorKind != kindPerson {
			t.Errorf("a subject row in the same response = {name:%q kind:%q}, want {%q,%q}", sc.ActorName, sc.ActorKind, fx.name, kindPerson)
		}
	}
	if subjectRows != 3 {
		t.Fatalf("the response carried %d subject-actored rows, want 3 -- the control above would otherwise assert nothing", subjectRows)
	}
}

// --- AC-9: free text and unknown uuids render verbatim ----------------------

// TestHistory_FreeTextActorRendersVerbatim: the two free-text actors real
// writers store (internal/importer/backfill.go, internal/invoice/actor.go's
// RevalidateActor) come back exactly as stored, classified raw.
func TestHistory_FreeTextActorRendersVerbatim(t *testing.T) {
	super, app := dbTestPools(t)
	fx := seedNamedInvoice(t, super, app, "AUDIT-02-03-FREETEXT", "Chinedu Okafor")

	base := time.Now().UTC()
	seedHistoryRow(t, super, fx.tenantID, fx.invID, statusPtr(StatusDraft), StatusValidated, "backfill-source-rows", base.Add(1*time.Second))
	seedHistoryRow(t, super, fx.tenantID, fx.invID, statusPtr(StatusValidated), StatusDraft, "revalidate-rule-set", base.Add(2*time.Second))

	got, err := fx.store.History(fx.ctx, fx.invID)
	if err != nil {
		t.Fatalf("History: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("History returned %d rows, want 3 (genesis + two free-text rows)", len(got))
	}
	for _, free := range []string{"backfill-source-rows", "revalidate-rule-set"} {
		row := rowFor(t, got, free)
		if row.ActorName != free {
			t.Errorf("free-text actor %q rendered ActorName = %q, want the stored string verbatim", free, row.ActorName)
		}
		if row.ActorKind != kindRaw {
			t.Errorf("free-text actor %q rendered ActorKind = %q, want %q", free, row.ActorKind, kindRaw)
		}
	}

	// Positive control: the same call did resolve the one subject it could.
	own := rowFor(t, got, fx.subject)
	if own.ActorName != fx.name || own.ActorKind != kindPerson {
		t.Errorf("the genesis row in the same response = {name:%q kind:%q}, want {%q,%q}", own.ActorName, own.ActorKind, fx.name, kindPerson)
	}
}

// TestHistory_UnresolvableUUIDRendersVerbatim: a uuid subject no membership can
// name must come back as itself. Never a fabricated name, never blank.
func TestHistory_UnresolvableUUIDRendersVerbatim(t *testing.T) {
	super, app := dbTestPools(t)
	fx := seedNamedInvoice(t, super, app, "AUDIT-02-03-STRANGER", "Chinedu Okafor")

	stranger := uuid.NewString()
	if n := mustCount(t, super, `SELECT count(*) FROM memberships WHERE user_id = $1`, stranger); n != 0 {
		t.Fatalf("setup: the stranger subject has %d memberships rows, want 0", n)
	}
	seedHistoryRow(t, super, fx.tenantID, fx.invID, statusPtr(StatusDraft), StatusValidated, stranger, time.Now().UTC().Add(time.Second))

	got, err := fx.store.History(fx.ctx, fx.invID)
	if err != nil {
		t.Fatalf("History: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("History returned %d rows, want 2", len(got))
	}

	row := rowFor(t, got, stranger)
	if row.ActorName != stranger {
		t.Errorf("an unresolvable subject rendered ActorName = %q, want the subject verbatim %q", row.ActorName, stranger)
	}
	if row.ActorKind != kindRaw {
		t.Errorf("an unresolvable subject rendered ActorKind = %q, want %q", row.ActorKind, kindRaw)
	}

	// Positive control: resolution ran and worked for the subject it could name.
	own := rowFor(t, got, fx.subject)
	if own.ActorName != fx.name || own.ActorKind != kindPerson {
		t.Errorf("the genesis row in the same response = {name:%q kind:%q}, want {%q,%q}", own.ActorName, own.ActorKind, fx.name, kindPerson)
	}
}

// TestHistory_ZeroResolvableActorsStillNamesEveryRow (AC #7): a history where
// nothing resolves is still a 200 with a non-empty name on every row.
func TestHistory_ZeroResolvableActorsStillNamesEveryRow(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := seedTenant(t, super, "AUDIT-02-03-NONAMES tenant")
	entityID := seedEntity(t, super, tenantID, "AUDIT-02-03-NONAMES entity")
	subject := memberSubject

	store := NewStore(app)
	c := auth.WithIdentity(ctx, auth.Identity{Subject: subject, Role: "authenticated", TenantID: tenantID})
	inv, err := store.Create(c, CreateInput{EntityID: entityID, InvoiceNumber: "AUDIT-02-03-NONAMES"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	seedHistoryRow(t, super, tenantID, inv.ID, statusPtr(StatusDraft), StatusValidated, "backfill-source-rows", time.Now().UTC().Add(time.Second))

	// memberSubject now has a row (seedTenant), but no display_name/email, so
	// it still can't resolve to a person -- that's what this test is about.
	if n := mustCount(t, super, `SELECT count(*) FROM memberships WHERE tenant_id = $1 AND (display_name IS NOT NULL OR email IS NOT NULL)`, tenantID); n != 0 {
		t.Fatalf("setup: tenant has %d memberships row(s) that could resolve to a name, want 0 -- this test is about the nothing-resolves path", n)
	}

	got, err := store.History(c, inv.ID)
	if err != nil {
		t.Fatalf("History with zero resolvable actors: err = %v, want nil (AC #7: still a 200)", err)
	}
	if len(got) != 2 {
		t.Fatalf("History returned %d rows, want 2", len(got))
	}
	for i, sc := range got {
		if sc.ActorName == "" {
			t.Errorf("got[%d].ActorName is empty; AC #7 wants the stored actor %q verbatim", i, sc.Actor)
		}
		if sc.ActorKind != kindRaw {
			t.Errorf("got[%d].ActorKind = %q, want %q", i, sc.ActorKind, kindRaw)
		}
	}
}

// --- AC-3 / AC-7: the query bound -------------------------------------------

// TestHistory_IssuesOneResolveQueryForManyRows is the whole point of batching:
// over a 41-row history spanning 9 distinct actors, resolution costs ONE extra
// statement, on History's own transaction.
//
// The mutation this must catch: moving the actor.Resolve call inside the
// rows.Next() loop. That is invisible to every other test here -- the labels
// would all still be correct -- and turns one memberships statement into one per
// uuid-actored row.
func TestHistory_IssuesOneResolveQueryForManyRows(t *testing.T) {
	super, _ := dbTestPools(t) // the skip gate; the Store below runs on the traced pool
	traced, rec := tracedAppPool(t)
	ctx := context.Background()

	tenantID := seedTenant(t, super, "AUDIT-02-03-BATCH tenant")
	entityID := seedEntity(t, super, tenantID, "AUDIT-02-03-BATCH entity")
	caller := uuid.NewString()
	seedMembershipNamed(t, super, tenantID, caller, "Chinedu Okafor")

	store := NewStore(traced)
	c := auth.WithIdentity(ctx, auth.Identity{Subject: caller, Role: "authenticated", TenantID: tenantID})
	inv, err := store.Create(c, CreateInput{EntityID: entityID, InvoiceNumber: "AUDIT-02-03-BATCH"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	// Eight more actors: 5 named members, 2 strangers, and system. 40 rows spread
	// over them, so a per-row resolve is not merely slower -- it is countable.
	var actors []string
	for i := 0; i < 5; i++ {
		s := uuid.NewString()
		seedMembershipNamed(t, super, tenantID, s, fmt.Sprintf("Member %d", i))
		actors = append(actors, s)
	}
	actors = append(actors, uuid.NewString(), uuid.NewString(), "system")

	base := time.Now().UTC()
	for i := 0; i < 40; i++ {
		seedHistoryRow(t, super, tenantID, inv.ID, statusPtr(StatusDraft), StatusValidated,
			actors[i%len(actors)], base.Add(time.Duration(i+1)*time.Second))
	}

	rec.reset()
	got, err := store.History(c, inv.ID)
	if err != nil {
		t.Fatalf("History: %v", err)
	}
	if len(got) != 41 {
		t.Fatalf("History returned %d rows, want 41 (genesis + 40 seeded)", len(got))
	}

	distinct := map[string]bool{}
	for _, sc := range got {
		distinct[sc.Actor] = true
	}
	if len(distinct) != 9 {
		t.Fatalf("the fixture carries %d distinct actors, want 9 -- a one-statement claim over one actor proves nothing", len(distinct))
	}

	// Two memberships statements, counted apart so neither hides the other: the
	// store's own Resolve, and the request seam's batched gate
	// (db.WithinRequestTenantTxOpts). Folding them into one number would let a
	// per-row Resolve hide behind the gate's constant statement.
	if n := len(rec.mentioning("memberships")); n != 1 {
		t.Errorf("History over %d rows / %d distinct actors issued %d memberships statement(s), want exactly 1 (AC #3); all: %q",
			len(got), len(distinct), n, rec.mentioning("memberships"))
	}
	if n := len(rec.seamMentioning("FROM memberships")); n != 1 {
		t.Errorf("the seam gate issued %d memberships statement(s), want exactly 1 -- the count above is scoped, not blind", n)
	}
	if n := len(rec.mentioning("invoice_status_history")); n != 1 {
		t.Errorf("History issued %d invoice_status_history statement(s), want exactly 1 -- resolution must not re-read the rows", n)
	}
	if n := rec.tenantTxCount(); n != 1 {
		t.Errorf("History opened %d tenant transactions, want exactly 1 -- Resolve must run on History's OWN tx, not a second one", n)
	}

	// Non-vacuous: one statement is only the right answer if every row was
	// labelled. Reported on the first offender, not all 41 -- the failure is the
	// same defect repeated.
	for i, sc := range got {
		if sc.ActorName == "" {
			t.Fatalf("got[%d].ActorName is empty (actor %q) -- one query must still label every row", i, sc.Actor)
		}
		switch sc.ActorKind {
		case kindPerson, kindSystem, kindRaw:
		default:
			t.Fatalf("got[%d].ActorKind = %q, want one of %q/%q/%q", i, sc.ActorKind, kindPerson, kindSystem, kindRaw)
		}
	}
}

// --- cross-tenant ------------------------------------------------------------

// TestHistory_CrossTenantActorNameNeverRenders: tenant A's history must never
// render a name only tenant B can see. memberships' tenant_isolation policy is
// the only thing standing between the two, and Resolve sets no predicate of its
// own -- so this is the test that proves History runs it under the right GUC.
//
// The absence is paired with a live positive control in the SAME call: tenant
// A's own member resolves. Without it a Resolve that resolved nothing would pass.
func TestHistory_CrossTenantActorNameNeverRenders(t *testing.T) {
	super, app := dbTestPools(t)

	fx := seedNamedInvoice(t, super, app, "AUDIT-02-03-XTENANT", "Chinedu Okafor")
	tenantB := seedTenant(t, super, "AUDIT-02-03-XTENANT tenant B")
	subjectB := uuid.NewString()
	const nameB = "Ngozi Balogun"
	seedMembershipNamed(t, super, tenantB, subjectB, nameB)

	// Tenant B genuinely CAN name subjectB, so the refusal below is a refusal and
	// not an empty table.
	if n := mustCount(t, super,
		`SELECT count(*) FROM memberships WHERE tenant_id = $1 AND user_id = $2 AND display_name = $3`,
		tenantB, subjectB, nameB); n != 1 {
		t.Fatalf("setup: tenant B's named membership = %d rows, want 1 -- the assertion below would be vacuous", n)
	}

	seedHistoryRow(t, super, fx.tenantID, fx.invID, statusPtr(StatusDraft), StatusValidated, subjectB, time.Now().UTC().Add(time.Second))

	got, err := fx.store.History(fx.ctx, fx.invID)
	if err != nil {
		t.Fatalf("History (as tenant A): %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("History returned %d rows, want 2", len(got))
	}

	foreign := rowFor(t, got, subjectB)
	if foreign.ActorName == nameB {
		t.Errorf("History as tenant A rendered tenant B's display_name %q for subject %s -- memberships RLS must scope Resolve", nameB, subjectB)
	}
	if foreign.ActorName != subjectB {
		t.Errorf("the foreign subject's ActorName = %q, want the subject verbatim %q", foreign.ActorName, subjectB)
	}
	if foreign.ActorKind != kindRaw {
		t.Errorf("the foreign subject's ActorKind = %q, want %q", foreign.ActorKind, kindRaw)
	}

	own := rowFor(t, got, fx.subject)
	if own.ActorName != fx.name || own.ActorKind != kindPerson {
		t.Errorf("the SAME call resolved tenant A's own member to {name:%q kind:%q}, want {%q,%q} -- without this control the absence above passes against a Resolve that resolves nothing", own.ActorName, own.ActorKind, fx.name, kindPerson)
	}
}

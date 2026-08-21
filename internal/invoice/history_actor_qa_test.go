package invoice

// AUDIT-02-03 (task-608) Mode B: adversarial coverage over Store.History's actor
// resolution -- the shapes, boundaries and no-op paths the AC-derived specs in
// history_actor_test.go do not reach. Reuses that file's fixtures
// (seedNamedInvoice / seedHistoryRow / rowFor / kind*) and store_test.go's pools.
//
// Run: `DEV_DB_PORT=5433 make test-invoice`.

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
)

// seedMembershipNamedWithStatus is seedMembershipNamed plus an explicit status.
// seedMembershipWithStatus (resolved_outside_test.go) cannot set display_name and
// seedMembershipNamed cannot set status; D-9 needs both at once.
func seedMembershipNamedWithStatus(t *testing.T, super *pgxpool.Pool, tenantID, userID, displayName, status string) {
	t.Helper()
	var id string
	if err := super.QueryRow(context.Background(),
		`INSERT INTO memberships (tenant_id, user_id, role, display_name, status)
		 VALUES ($1, $2, 'admin', $3, $4) RETURNING id`,
		tenantID, userID, displayName, status,
	).Scan(&id); err != nil {
		t.Fatalf("seed memberships(display_name=%q status=%q): %v", displayName, status, err)
	}
	t.Cleanup(func() {
		_, _ = super.Exec(context.Background(), `DELETE FROM memberships WHERE id = $1`, id)
	})
}

// TestHistory_MixedActorShapesResolveInOneCall: all four stored shapes in ONE
// response -- a named member, "system", importer free text, and a uuid nothing
// can name. Each carries its own label, and the whole mix still costs one
// memberships statement. Per-shape tests each prove one shape in isolation; only
// this one proves a batch does not smear one shape's label over another.
func TestHistory_MixedActorShapesResolveInOneCall(t *testing.T) {
	super, _ := dbTestPools(t) // the skip gate; the Store below runs on the traced pool
	traced, rec := tracedAppPool(t)
	ctx := context.Background()

	tenantID := seedTenant(t, super, "AUDIT-02-03-MIXED tenant")
	entityID := seedEntity(t, super, tenantID, "AUDIT-02-03-MIXED entity")
	caller := uuid.NewString()
	const callerName = "Chinedu Okafor"
	seedMembershipNamed(t, super, tenantID, caller, callerName)

	store := NewStore(traced)
	c := auth.WithIdentity(ctx, auth.Identity{Subject: caller, Role: "authenticated", TenantID: tenantID})
	inv, err := store.Create(c, CreateInput{EntityID: entityID, InvoiceNumber: "AUDIT-02-03-MIXED"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	stranger := uuid.NewString()
	if n := mustCount(t, super, `SELECT count(*) FROM memberships WHERE user_id = $1`, stranger); n != 0 {
		t.Fatalf("setup: the stranger subject has %d memberships rows, want 0", n)
	}
	base := time.Now().UTC()
	seedHistoryRow(t, super, tenantID, inv.ID, statusPtr(StatusDraft), StatusValidated, "system", base.Add(1*time.Second))
	seedHistoryRow(t, super, tenantID, inv.ID, statusPtr(StatusValidated), StatusDraft, "backfill-source-rows", base.Add(2*time.Second))
	seedHistoryRow(t, super, tenantID, inv.ID, statusPtr(StatusDraft), StatusValidated, stranger, base.Add(3*time.Second))

	rec.reset()
	got, err := store.History(c, inv.ID)
	if err != nil {
		t.Fatalf("History: %v", err)
	}
	if len(got) != 4 {
		t.Fatalf("History returned %d rows, want 4 (genesis + system + free text + stranger)", len(got))
	}

	want := map[string]Label{
		caller:                 {Text: callerName, Kind: kindPerson},
		"system":               {Text: "System", Kind: kindSystem},
		"backfill-source-rows": {Text: "backfill-source-rows", Kind: kindRaw},
		stranger:               {Text: stranger, Kind: kindRaw},
	}
	for storedActor, w := range want {
		row := rowFor(t, got, storedActor)
		if row.ActorName != w.Text {
			t.Errorf("actor %q rendered ActorName = %q, want %q", storedActor, row.ActorName, w.Text)
		}
		if row.ActorKind != w.Kind {
			t.Errorf("actor %q rendered ActorKind = %q, want %q", storedActor, row.ActorKind, w.Kind)
		}
		if row.Actor != storedActor {
			t.Errorf("actor %q came back as %q, want it byte-for-byte", storedActor, row.Actor)
		}
	}

	kinds := map[string]bool{}
	for _, sc := range got {
		kinds[sc.ActorKind] = true
	}
	if len(kinds) != 3 {
		t.Fatalf("the response carried %d distinct ActorKinds (%v), want all 3 -- the mix is the point of this test", len(kinds), kinds)
	}
	if n := len(rec.mentioning("memberships")); n != 1 {
		t.Errorf("a four-shape history issued %d memberships statement(s), want exactly 1", n)
	}
}

// Label is the wire pair this file asserts over. Deliberately NOT actor.Label:
// ActorKind is a string on the wire, and a rename of actor.Kind's constants must
// not silently move what these tests demand.
type Label struct {
	Text string
	Kind string
}

// TestHistory_SuspendedMemberStillResolves (D-9): resolution reads no status
// column. A suspended member's past actions keep their name -- an audit trail
// that anonymises people when their access is revoked is a broken audit trail.
func TestHistory_SuspendedMemberStillResolves(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := seedTenant(t, super, "AUDIT-02-03-SUSPENDED tenant")
	entityID := seedEntity(t, super, tenantID, "AUDIT-02-03-SUSPENDED entity")
	subject := uuid.NewString()
	const name = "Halima Yusuf"
	seedMembershipNamedWithStatus(t, super, tenantID, subject, name, "suspended")

	if n := mustCount(t, super,
		`SELECT count(*) FROM memberships WHERE tenant_id = $1 AND user_id = $2 AND status = 'suspended'`,
		tenantID, subject); n != 1 {
		t.Fatalf("setup: the suspended membership = %d rows, want 1 -- the assertion below would prove nothing", n)
	}

	store := NewStore(app)
	c := auth.WithIdentity(ctx, auth.Identity{Subject: subject, Role: "authenticated", TenantID: tenantID})
	inv, err := store.Create(c, CreateInput{EntityID: entityID, InvoiceNumber: "AUDIT-02-03-SUSPENDED"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	got, err := store.History(c, inv.ID)
	if err != nil {
		t.Fatalf("History: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("History returned %d rows, want 1 (the genesis row)", len(got))
	}
	if got[0].ActorName != name {
		t.Errorf("a suspended member's row rendered ActorName = %q, want %q (D-9: Resolve reads no status column)", got[0].ActorName, name)
	}
	if got[0].ActorKind != kindPerson {
		t.Errorf("a suspended member's row rendered ActorKind = %q, want %q", got[0].ActorKind, kindPerson)
	}
}

// TestHistory_SingleRowResolvesThroughTheBatchPath: the lower boundary of the
// batch. One row, one distinct actor -- still exactly one memberships statement,
// still labelled. A batch path that only fires above some row count would pass
// every many-row test here and 404 the commonest case in production: a
// freshly-created invoice.
func TestHistory_SingleRowResolvesThroughTheBatchPath(t *testing.T) {
	super, _ := dbTestPools(t)
	traced, rec := tracedAppPool(t)
	ctx := context.Background()

	tenantID := seedTenant(t, super, "AUDIT-02-03-ONEROW tenant")
	entityID := seedEntity(t, super, tenantID, "AUDIT-02-03-ONEROW entity")
	caller := uuid.NewString()
	const callerName = "Folake Adesina"
	seedMembershipNamed(t, super, tenantID, caller, callerName)

	store := NewStore(traced)
	c := auth.WithIdentity(ctx, auth.Identity{Subject: caller, Role: "authenticated", TenantID: tenantID})
	inv, err := store.Create(c, CreateInput{EntityID: entityID, InvoiceNumber: "AUDIT-02-03-ONEROW"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	rec.reset()
	got, err := store.History(c, inv.ID)
	if err != nil {
		t.Fatalf("History: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("History returned %d rows, want exactly 1 -- this test is the one-row boundary", len(got))
	}
	if got[0].ActorName != callerName || got[0].ActorKind != kindPerson {
		t.Errorf("the single row = {name:%q kind:%q}, want {%q,%q}", got[0].ActorName, got[0].ActorKind, callerName, kindPerson)
	}
	if n := len(rec.mentioning("memberships")); n != 1 {
		t.Errorf("a one-row history issued %d memberships statement(s), want exactly 1", n)
	}
}

// TestHistory_ErrorPathsIssueNoResolveQuery: neither error exit resolves
// anything. Both return before the fan-out, so a Resolve moved above the
// len(result)==0 / rows.Err() checks would bind an empty uuid[] on every 404 and
// -- for the malformed id -- try to query an already-aborted transaction.
func TestHistory_ErrorPathsIssueNoResolveQuery(t *testing.T) {
	super, _ := dbTestPools(t)
	traced, rec := tracedAppPool(t)
	ctx := context.Background()

	tenantID := seedTenant(t, super, "AUDIT-02-03-NOROWS tenant")
	caller := uuid.NewString()
	seedMembershipNamed(t, super, tenantID, caller, "Chinedu Okafor")
	store := NewStore(traced)
	c := auth.WithIdentity(ctx, auth.Identity{Subject: caller, Role: "authenticated", TenantID: tenantID})

	for _, tc := range []struct {
		name    string
		id      string
		wantErr error
	}{
		{"an id no row carries", uuid.NewString(), ErrNotFound},
		{"a malformed id (22P02)", "not-a-uuid", ErrValidation},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec.reset()
			got, err := store.History(c, tc.id)
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("History(%q) err = %v, want %v", tc.id, err, tc.wantErr)
			}
			if got != nil {
				t.Errorf("History(%q) returned %d rows alongside the error, want nil", tc.id, len(got))
			}
			// Non-vacuous: the call did reach Postgres, so "zero memberships
			// statements" is a refusal to resolve, not a call that never ran.
			if n := len(rec.mentioning("invoice_status_history")); n != 1 {
				t.Fatalf("History(%q) issued %d invoice_status_history statement(s), want 1 -- the assertion below would otherwise pass on a no-op", tc.id, n)
			}
			if stmts := rec.mentioning("memberships"); len(stmts) != 0 {
				t.Errorf("the %s path issued %d memberships statement(s), want 0: %q", tc.name, len(stmts), stmts)
			}
		})
	}
}

// TestHistory_WireShapeCarriesBothKeysNeverNull marshals what the handler
// marshals (handlers.go:1235 writeJSON's the []StatusChange verbatim): six keys
// per row, actor_name always a non-empty JSON string, actor_kind always one of
// exactly three values. The Go-side fence under e2e/api/contract-invoice.spec.ts's
// deployed assertion -- that one cannot run on a PR without the fleet.
func TestHistory_WireShapeCarriesBothKeysNeverNull(t *testing.T) {
	super, app := dbTestPools(t)
	fx := seedNamedInvoice(t, super, app, "AUDIT-02-03-WIRE", "Chinedu Okafor")

	base := time.Now().UTC()
	seedHistoryRow(t, super, fx.tenantID, fx.invID, statusPtr(StatusDraft), StatusValidated, "system", base.Add(1*time.Second))
	seedHistoryRow(t, super, fx.tenantID, fx.invID, statusPtr(StatusValidated), StatusDraft, "revalidate-rule-set", base.Add(2*time.Second))

	got, err := fx.store.History(fx.ctx, fx.invID)
	if err != nil {
		t.Fatalf("History: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("History returned %d rows, want 3", len(got))
	}

	blob, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("marshal []StatusChange: %v", err)
	}
	var rows []map[string]json.RawMessage
	if err := json.Unmarshal(blob, &rows); err != nil {
		t.Fatalf("the body is not a bare JSON array of objects: %v (body %s)", err, blob)
	}
	if len(rows) != 3 {
		t.Fatalf("the marshalled body carried %d rows, want 3", len(rows))
	}

	wantKeys := []string{"from_status", "to_status", "actor", "actor_name", "actor_kind", "changed_at"}
	for i, row := range rows {
		if len(row) != len(wantKeys) {
			t.Errorf("row %d carries %d keys (%v), want exactly %v", i, len(row), row, wantKeys)
		}
		for _, k := range wantKeys {
			if _, ok := row[k]; !ok {
				t.Errorf("row %d has no %q key", i, k)
			}
		}
		var name string
		if err := json.Unmarshal(row["actor_name"], &name); err != nil {
			t.Errorf("row %d actor_name is not a JSON string (raw %s): %v", i, row["actor_name"], err)
		} else if name == "" {
			t.Errorf("row %d actor_name is the empty string; every row must carry a display", i)
		}
		var kind string
		if err := json.Unmarshal(row["actor_kind"], &kind); err != nil {
			t.Errorf("row %d actor_kind is not a JSON string (raw %s): %v", i, row["actor_kind"], err)
			continue
		}
		switch kind {
		case kindPerson, kindSystem, kindRaw:
		default:
			t.Errorf("row %d actor_kind = %q, want one of %q/%q/%q", i, kind, kindPerson, kindSystem, kindRaw)
		}
	}

	// Non-vacuous: the fixture really does span more than one kind, so the
	// three-value set above is a constraint and not a single value repeated.
	kinds := map[string]bool{}
	for _, sc := range got {
		kinds[sc.ActorKind] = true
	}
	if len(kinds) < 3 {
		t.Fatalf("the marshalled fixture spans %d kinds (%v), want all 3", len(kinds), kinds)
	}
}

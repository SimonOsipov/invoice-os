// RED, DB-backed tests for the resolve/unresolve-outside write path -- written
// BEFORE Store.ResolveOutside/UnresolveOutside/CallerRole/isApprover are real
// (RED against not-implemented stubs in store.go: every assertion below fails on
// VALUE, not on a compile/collection error, until the real bodies land). Reuses
// the dbTestPools/seedTenant/seedEntity/seedInvoiceAtStatus/mustKeptAsIsTriple/
// auditCount/auditActor/mustCount harness from store_test.go/
// transition_adversarial_test.go/kept_as_is_test.go (same package) -- nothing
// here is redefined.
//
// Run: `make test-rls` does NOT cover this package -- use:
//
//	DATABASE_URL="postgres://invoice_app:app@localhost:5435/invoice_os?sslmode=disable" \
//	DATABASE_SUPERUSER_URL="postgres://postgres:postgres@localhost:5435/invoice_os?sslmode=disable" \
//	go test -count=1 -p 1 ./internal/invoice/...
package invoice

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/SimonOsipov/invoice-os/internal/audit"
	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
	"github.com/SimonOsipov/invoice-os/internal/platform/db"
)

// seedMembership inserts one memberships row for tenantID/userID/role as the
// superuser and registers its cleanup -- new plumbing: this package has never
// seeded a memberships row before (KeepAsIs/UnkeepAsIs carry no role gate), and
// the platform/db and tenancy packages' own seedMembership helpers live in
// _test.go files, which are not importable across packages.
func seedMembership(t *testing.T, super *pgxpool.Pool, tenantID, userID, role string) string {
	t.Helper()
	ctx := context.Background()
	var id string
	if err := super.QueryRow(ctx,
		`INSERT INTO memberships (tenant_id, user_id, role) VALUES ($1, $2, $3) RETURNING id`,
		tenantID, userID, role,
	).Scan(&id); err != nil {
		t.Fatalf("seed memberships: %v", err)
	}
	t.Cleanup(func() {
		_, _ = super.Exec(context.Background(), `DELETE FROM memberships WHERE id = $1`, id)
	})
	return id
}

// seedResolvedFailed seeds a `failed` invoice with the kept_as_is triple already
// set, via a direct superuser UPDATE -- Store.ResolveOutside is not implemented
// yet at this stage, so fixtures needing "already resolved" as GIVEN cannot route
// through it without conflating the fixture with the method under test.
func seedResolvedFailed(t *testing.T, super *pgxpool.Pool, tenantID, entityID, number, by, reason string) string {
	t.Helper()
	id := seedInvoiceAtStatus(t, super, tenantID, entityID, number, StatusFailed)
	if _, err := super.Exec(context.Background(),
		`UPDATE invoices SET kept_as_is_at = now(), kept_as_is_by = $1, kept_as_is_reason = $2 WHERE id = $3`,
		by, reason, id,
	); err != nil {
		t.Fatalf("seed resolved-outside triple: %v", err)
	}
	return id
}

// seedSubmissionJob inserts one submission_jobs row as the superuser, for T2-7's
// "never touches submission state" proof. Column values beyond the FK/CHECK
// minimum are arbitrary -- the test only needs a row to prove untouched.
func seedSubmissionJob(t *testing.T, super *pgxpool.Pool, tenantID, invoiceID, idempotencyKey string) string {
	t.Helper()
	ctx := context.Background()
	var id string
	if err := super.QueryRow(ctx,
		`INSERT INTO submission_jobs (tenant_id, invoice_id, idempotency_key, adapter, adapter_version, state)
		 VALUES ($1, $2, $3, 'mock', 'v1', 'failed') RETURNING id`,
		tenantID, invoiceID, idempotencyKey,
	).Scan(&id); err != nil {
		t.Fatalf("seed submission_jobs: %v", err)
	}
	t.Cleanup(func() {
		_, _ = super.Exec(context.Background(), `DELETE FROM submission_jobs WHERE id = $1`, id)
	})
	return id
}

// --- T2-1..T2-9: ResolveOutside ---------------------------------------------

// T2-1: ResolveOutside stamps all three columns on a failed invoice, `by` the
// caller's identity subject.
func TestResolveOutside_StampsTripleOnFailed(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := seedTenant(t, super, "T2-1 tenant")
	entityID := seedEntity(t, super, tenantID, "T2-1 entity")
	invID := seedInvoiceAtStatus(t, super, tenantID, entityID, "T2-1", StatusFailed)

	subject := uuid.NewString()
	seedMembership(t, super, tenantID, subject, "admin")
	c := auth.WithIdentity(ctx, auth.Identity{Subject: subject, Role: "authenticated", TenantID: tenantID})

	store := NewStore(app)
	const reason = "filed manually"
	got, err := store.ResolveOutside(c, invID, reason)
	if err != nil {
		t.Fatalf("ResolveOutside: %v", err)
	}
	if got.KeptAsIsAt == nil || got.KeptAsIsBy == nil || got.KeptAsIsReason == nil {
		t.Fatalf("ResolveOutside return = (at=%v by=%v reason=%v), want all three non-nil", got.KeptAsIsAt, got.KeptAsIsBy, got.KeptAsIsReason)
	}
	if *got.KeptAsIsBy != subject {
		t.Errorf("KeptAsIsBy = %q, want caller subject %q", *got.KeptAsIsBy, subject)
	}
	if *got.KeptAsIsReason != reason {
		t.Errorf("KeptAsIsReason = %q, want %q", *got.KeptAsIsReason, reason)
	}

	at, by, gotReason := mustKeptAsIsTriple(t, super, invID)
	if at == nil || by == nil || gotReason == nil {
		t.Fatalf("stored triple = (%v,%v,%v), want all non-nil", at, by, gotReason)
	}
	if *by != subject || *gotReason != reason {
		t.Errorf("stored triple (by=%q,reason=%q), want (by=%q,reason=%q)", *by, *gotReason, subject, reason)
	}
}

// T2-2: kept_as_is_by is always the identity subject, never a value smuggled
// through the reason string.
func TestResolveOutside_ActorIsIdentityNotBody(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := seedTenant(t, super, "T2-2 tenant")
	entityID := seedEntity(t, super, tenantID, "T2-2 entity")
	invID := seedInvoiceAtStatus(t, super, tenantID, entityID, "T2-2", StatusFailed)

	subject := uuid.NewString()
	seedMembership(t, super, tenantID, subject, "admin")
	c := auth.WithIdentity(ctx, auth.Identity{Subject: subject, Role: "authenticated", TenantID: tenantID})

	smuggled := uuid.NewString()
	store := NewStore(app)
	got, err := store.ResolveOutside(c, invID, "actor is "+smuggled)
	if err != nil {
		t.Fatalf("ResolveOutside: %v", err)
	}
	if got.KeptAsIsBy == nil || *got.KeptAsIsBy != subject {
		t.Fatalf("KeptAsIsBy = %v, want the identity subject %q (never a uuid smuggled through reason)", got.KeptAsIsBy, subject)
	}
}

// T2-3: a `preparer` caller is refused with ErrNotPermitted; nothing written.
func TestResolveOutside_PreparerIsNotPermitted(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := seedTenant(t, super, "T2-3 tenant")
	entityID := seedEntity(t, super, tenantID, "T2-3 entity")
	invID := seedInvoiceAtStatus(t, super, tenantID, entityID, "T2-3", StatusFailed)

	subject := uuid.NewString()
	seedMembership(t, super, tenantID, subject, "preparer")
	c := auth.WithIdentity(ctx, auth.Identity{Subject: subject, Role: "authenticated", TenantID: tenantID})

	beforeAudit := auditCount(t, app, tenantID, "invoice.resolved_outside")
	store := NewStore(app)
	if _, err := store.ResolveOutside(c, invID, "x"); !errors.Is(err, ErrNotPermitted) {
		t.Fatalf("ResolveOutside (preparer) err = %v, want ErrNotPermitted", err)
	}

	at, by, reason := mustKeptAsIsTriple(t, super, invID)
	if at != nil || by != nil || reason != nil {
		t.Errorf("triple after refused preparer ResolveOutside = (%v,%v,%v), want all NULL", at, by, reason)
	}
	if n := auditCount(t, app, tenantID, "invoice.resolved_outside"); n != beforeAudit {
		t.Errorf("invoice.resolved_outside audit rows = %d, want unchanged %d", n, beforeAudit)
	}
}

// T2-4: a caller with no memberships row is refused with ErrNotPermitted, same
// as an explicit non-approver role.
func TestResolveOutside_NoMembershipIsNotPermitted(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := seedTenant(t, super, "T2-4 tenant")
	entityID := seedEntity(t, super, tenantID, "T2-4 entity")
	invID := seedInvoiceAtStatus(t, super, tenantID, entityID, "T2-4", StatusFailed)

	subject := uuid.NewString() // no seedMembership call: no row at all
	c := auth.WithIdentity(ctx, auth.Identity{Subject: subject, Role: "authenticated", TenantID: tenantID})

	store := NewStore(app)
	if _, err := store.ResolveOutside(c, invID, "x"); !errors.Is(err, ErrNotPermitted) {
		t.Fatalf("ResolveOutside (no membership) err = %v, want ErrNotPermitted", err)
	}

	at, by, reason := mustKeptAsIsTriple(t, super, invID)
	if at != nil || by != nil || reason != nil {
		t.Errorf("triple after refused no-membership ResolveOutside = (%v,%v,%v), want all NULL", at, by, reason)
	}
}

// T2-5: both `reviewer` and `admin` are permitted.
func TestResolveOutside_ReviewerAndAdminArePermitted(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := seedTenant(t, super, "T2-5 tenant")
	entityID := seedEntity(t, super, tenantID, "T2-5 entity")
	store := NewStore(app)

	for _, role := range []string{"reviewer", "admin"} {
		t.Run(role, func(t *testing.T) {
			invID := seedInvoiceAtStatus(t, super, tenantID, entityID, "T2-5-"+role, StatusFailed)
			subject := uuid.NewString()
			seedMembership(t, super, tenantID, subject, role)
			c := auth.WithIdentity(ctx, auth.Identity{Subject: subject, Role: "authenticated", TenantID: tenantID})

			got, err := store.ResolveOutside(c, invID, "resolved by "+role)
			if err != nil {
				t.Fatalf("ResolveOutside (%s): %v", role, err)
			}
			if got.KeptAsIsAt == nil {
				t.Errorf("ResolveOutside (%s): KeptAsIsAt = nil, want stamped", role)
			}
		})
	}
}

// T2-6: every non-`failed` status is refused with ErrNotResolvable; nothing
// written.
func TestResolveOutside_NonFailedIsNotResolvable(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := seedTenant(t, super, "T2-6 tenant")
	entityID := seedEntity(t, super, tenantID, "T2-6 entity")
	subject := uuid.NewString()
	seedMembership(t, super, tenantID, subject, "admin")
	c := auth.WithIdentity(ctx, auth.Identity{Subject: subject, Role: "authenticated", TenantID: tenantID})
	store := NewStore(app)

	nonFailed := []Status{StatusDraft, StatusValidated, StatusQueued, StatusSubmitted, StatusAccepted, StatusRejected}
	for _, status := range nonFailed {
		t.Run(string(status), func(t *testing.T) {
			invID := seedInvoiceAtStatus(t, super, tenantID, entityID, "T2-6-"+string(status), status)
			if _, err := store.ResolveOutside(c, invID, "x"); !errors.Is(err, ErrNotResolvable) {
				t.Fatalf("ResolveOutside (status=%s) err = %v, want ErrNotResolvable", status, err)
			}
			at, by, reason := mustKeptAsIsTriple(t, super, invID)
			if at != nil || by != nil || reason != nil {
				t.Errorf("triple after refused ResolveOutside (status=%s) = (%v,%v,%v), want all NULL", status, at, by, reason)
			}
		})
	}
}

// T2-7: ResolveOutside never touches status, failure_kind, or submission_jobs,
// and never files an invoice.transitioned audit row.
func TestResolveOutside_NeverChangesStatusOrSubmission(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := seedTenant(t, super, "T2-7 tenant")
	entityID := seedEntity(t, super, tenantID, "T2-7 entity")
	invID := seedInvoiceAtStatus(t, super, tenantID, entityID, "T2-7", StatusFailed)
	if _, err := super.Exec(ctx,
		`UPDATE invoices SET failure_kind = 'never_acknowledged' WHERE id = $1`, invID,
	); err != nil {
		t.Fatalf("seed failure_kind: %v", err)
	}
	jobID := seedSubmissionJob(t, super, tenantID, invID, "T2-7-idem")

	subject := uuid.NewString()
	seedMembership(t, super, tenantID, subject, "admin")
	c := auth.WithIdentity(ctx, auth.Identity{Subject: subject, Role: "authenticated", TenantID: tenantID})

	var beforeState, beforeUpdatedAt string
	if err := super.QueryRow(ctx,
		`SELECT state, updated_at::text FROM submission_jobs WHERE id = $1`, jobID,
	).Scan(&beforeState, &beforeUpdatedAt); err != nil {
		t.Fatalf("read submission_jobs before: %v", err)
	}
	beforeTransitioned := auditCount(t, app, tenantID, "invoice.transitioned")

	store := NewStore(app)
	if _, err := store.ResolveOutside(c, invID, "job stays put"); err != nil {
		t.Fatalf("ResolveOutside: %v", err)
	}

	var status, afterState, afterUpdatedAt string
	var failureKind *string
	if err := super.QueryRow(ctx,
		`SELECT status, failure_kind FROM invoices WHERE id = $1`, invID,
	).Scan(&status, &failureKind); err != nil {
		t.Fatalf("read invoice after: %v", err)
	}
	if status != string(StatusFailed) {
		t.Errorf("status = %q, want unchanged %q", status, StatusFailed)
	}
	if failureKind == nil || *failureKind != "never_acknowledged" {
		t.Errorf("failure_kind = %v, want unchanged %q", failureKind, "never_acknowledged")
	}

	if err := super.QueryRow(ctx,
		`SELECT state, updated_at::text FROM submission_jobs WHERE id = $1`, jobID,
	).Scan(&afterState, &afterUpdatedAt); err != nil {
		t.Fatalf("read submission_jobs after: %v", err)
	}
	if afterState != beforeState || afterUpdatedAt != beforeUpdatedAt {
		t.Errorf("submission_jobs row changed: before=(%s,%s) after=(%s,%s), want byte-identical", beforeState, beforeUpdatedAt, afterState, afterUpdatedAt)
	}

	if n := auditCount(t, app, tenantID, "invoice.transitioned"); n != beforeTransitioned {
		t.Errorf("invoice.transitioned audit rows = %d, want unchanged %d", n, beforeTransitioned)
	}
}

// T2-8: ResolveOutside files exactly one invoice.resolved_outside audit row,
// payload {id, reason}, actor the caller subject.
func TestResolveOutside_AuditsResolvedOutside(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := seedTenant(t, super, "T2-8 tenant")
	entityID := seedEntity(t, super, tenantID, "T2-8 entity")
	invID := seedInvoiceAtStatus(t, super, tenantID, entityID, "T2-8", StatusFailed)

	subject := uuid.NewString()
	seedMembership(t, super, tenantID, subject, "admin")
	c := auth.WithIdentity(ctx, auth.Identity{Subject: subject, Role: "authenticated", TenantID: tenantID})

	const event = "invoice.resolved_outside"
	const reason = "filed manually per email"
	before := auditCount(t, app, tenantID, event)

	store := NewStore(app)
	if _, err := store.ResolveOutside(c, invID, reason); err != nil {
		t.Fatalf("ResolveOutside: %v", err)
	}

	if after := auditCount(t, app, tenantID, event); after != before+1 {
		t.Fatalf("audit_log rows for %s = %d, want %d (exactly one new row)", event, after, before+1)
	}
	if actor := auditActor(t, app, tenantID, event); actor != subject {
		t.Errorf("audit actor = %q, want %q", actor, subject)
	}

	var payloadJSON string
	if err := db.WithinTenantTx(ctx, app, tenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx,
			`SELECT payload::text FROM audit_log WHERE event = $1 ORDER BY created_at DESC LIMIT 1`, event,
		).Scan(&payloadJSON)
	}); err != nil {
		t.Fatalf("read back audit payload: %v", err)
	}
	var payload struct {
		ID     string `json:"id"`
		Reason string `json:"reason"`
	}
	if err := json.Unmarshal([]byte(payloadJSON), &payload); err != nil {
		t.Fatalf("unmarshal audit payload %q: %v", payloadJSON, err)
	}
	if payload.ID != invID {
		t.Errorf("audit payload id = %q, want %q", payload.ID, invID)
	}
	if payload.Reason != reason {
		t.Errorf("audit payload reason = %q, want %q", payload.Reason, reason)
	}
}

// T2-9: re-resolving an already-resolved invoice is legal and overwrites
// at/by/reason; a second audit row is filed.
func TestResolveOutside_ReResolveOverwrites(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := seedTenant(t, super, "T2-9 tenant")
	entityID := seedEntity(t, super, tenantID, "T2-9 entity")
	firstSubject := uuid.NewString()
	invID := seedResolvedFailed(t, super, tenantID, entityID, "T2-9", firstSubject, "first reason")

	subject := uuid.NewString()
	seedMembership(t, super, tenantID, subject, "admin")
	c := auth.WithIdentity(ctx, auth.Identity{Subject: subject, Role: "authenticated", TenantID: tenantID})

	const event = "invoice.resolved_outside"
	before := auditCount(t, app, tenantID, event)

	store := NewStore(app)
	const newReason = "second reason, mind changed"
	got, err := store.ResolveOutside(c, invID, newReason)
	if err != nil {
		t.Fatalf("ResolveOutside (re-resolve): %v", err)
	}
	if got.KeptAsIsBy == nil || *got.KeptAsIsBy != subject {
		t.Errorf("KeptAsIsBy = %v, want the NEW caller subject %q (overwritten)", got.KeptAsIsBy, subject)
	}
	if got.KeptAsIsReason == nil || *got.KeptAsIsReason != newReason {
		t.Errorf("KeptAsIsReason = %v, want %q (overwritten)", got.KeptAsIsReason, newReason)
	}

	if after := auditCount(t, app, tenantID, event); after != before+1 {
		t.Errorf("audit_log rows for %s = %d, want %d (+1, a second row filed)", event, after, before+1)
	}
}

// --- T2-10..T2-12: UnresolveOutside -----------------------------------------

// T2-10: UnresolveOutside clears all three columns and files exactly one
// invoice.unresolved_outside audit row.
func TestUnresolveOutside_ClearsAndAudits(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := seedTenant(t, super, "T2-10 tenant")
	entityID := seedEntity(t, super, tenantID, "T2-10 entity")
	invID := seedResolvedFailed(t, super, tenantID, entityID, "T2-10", uuid.NewString(), "was resolved")

	subject := uuid.NewString()
	seedMembership(t, super, tenantID, subject, "admin")
	c := auth.WithIdentity(ctx, auth.Identity{Subject: subject, Role: "authenticated", TenantID: tenantID})

	const event = "invoice.unresolved_outside"
	before := auditCount(t, app, tenantID, event)

	store := NewStore(app)
	got, err := store.UnresolveOutside(c, invID)
	if err != nil {
		t.Fatalf("UnresolveOutside: %v", err)
	}
	if got.KeptAsIsAt != nil || got.KeptAsIsBy != nil || got.KeptAsIsReason != nil {
		t.Errorf("UnresolveOutside return triple = (%v,%v,%v), want all nil", got.KeptAsIsAt, got.KeptAsIsBy, got.KeptAsIsReason)
	}

	at, by, reason := mustKeptAsIsTriple(t, super, invID)
	if at != nil || by != nil || reason != nil {
		t.Errorf("stored triple after UnresolveOutside = (%v,%v,%v), want all NULL", at, by, reason)
	}
	if after := auditCount(t, app, tenantID, event); after != before+1 {
		t.Errorf("audit_log rows for %s = %d, want %d (+1)", event, after, before+1)
	}
}

// T2-6b: every non-failed status is refused with ErrNotResolvable for
// UnresolveOutside too -- AC-3 says "both methods", but T2-6 above only drives
// ResolveOutside. This also pins guard ORDER: UnresolveOutside's own
// `Status != StatusFailed` check runs BEFORE its `KeptAsIsAt == nil` no-op
// check, so an un-marked NON-failed invoice (e.g. queued) is ErrNotResolvable,
// never the silent no-op a naive reading of "idempotent" might expect.
func TestUnresolveOutside_NonFailedIsNotResolvable(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := seedTenant(t, super, "T2-6b tenant")
	entityID := seedEntity(t, super, tenantID, "T2-6b entity")
	subject := uuid.NewString()
	seedMembership(t, super, tenantID, subject, "admin")
	c := auth.WithIdentity(ctx, auth.Identity{Subject: subject, Role: "authenticated", TenantID: tenantID})
	store := NewStore(app)

	nonFailed := []Status{StatusDraft, StatusValidated, StatusQueued, StatusSubmitted, StatusAccepted, StatusRejected}
	for _, status := range nonFailed {
		t.Run(string(status), func(t *testing.T) {
			invID := seedInvoiceAtStatus(t, super, tenantID, entityID, "T2-6b-"+string(status), status)
			if _, err := store.UnresolveOutside(c, invID); !errors.Is(err, ErrNotResolvable) {
				t.Fatalf("UnresolveOutside (status=%s) err = %v, want ErrNotResolvable", status, err)
			}
		})
	}
}

// T2-11: UnresolveOutside on an unresolved failed invoice is an idempotent
// no-op -- no write, no audit row.
func TestUnresolveOutside_UnresolvedIsNoop(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := seedTenant(t, super, "T2-11 tenant")
	entityID := seedEntity(t, super, tenantID, "T2-11 entity")
	invID := seedInvoiceAtStatus(t, super, tenantID, entityID, "T2-11", StatusFailed)

	subject := uuid.NewString()
	seedMembership(t, super, tenantID, subject, "admin")
	c := auth.WithIdentity(ctx, auth.Identity{Subject: subject, Role: "authenticated", TenantID: tenantID})

	const event = "invoice.unresolved_outside"
	before := auditCount(t, app, tenantID, event)

	store := NewStore(app)
	got, err := store.UnresolveOutside(c, invID)
	if err != nil {
		t.Fatalf("UnresolveOutside (already unresolved): %v", err)
	}
	if got.ID != invID {
		t.Errorf("UnresolveOutside returned ID = %q, want %q", got.ID, invID)
	}
	if got.Status != StatusFailed {
		t.Errorf("UnresolveOutside returned status = %q, want unchanged %q", got.Status, StatusFailed)
	}
	if got.KeptAsIsAt != nil || got.KeptAsIsBy != nil || got.KeptAsIsReason != nil {
		t.Errorf("UnresolveOutside (no-op) return triple = (%v,%v,%v), want all nil", got.KeptAsIsAt, got.KeptAsIsBy, got.KeptAsIsReason)
	}

	if after := auditCount(t, app, tenantID, event); after != before {
		t.Errorf("audit_log rows for %s = %d, want unchanged %d (a no-op must not audit)", event, after, before)
	}
}

// T2-10b: UnresolveOutside never touches status, failure_kind, or
// submission_jobs, and never files an invoice.transitioned audit row -- AC-6
// says "neither", but T2-7 above only drives ResolveOutside.
func TestUnresolveOutside_NeverChangesStatusOrSubmission(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := seedTenant(t, super, "T2-10b tenant")
	entityID := seedEntity(t, super, tenantID, "T2-10b entity")
	invID := seedResolvedFailed(t, super, tenantID, entityID, "T2-10b", uuid.NewString(), "was resolved")
	if _, err := super.Exec(ctx,
		`UPDATE invoices SET failure_kind = 'never_acknowledged' WHERE id = $1`, invID,
	); err != nil {
		t.Fatalf("seed failure_kind: %v", err)
	}
	jobID := seedSubmissionJob(t, super, tenantID, invID, "T2-10b-idem")

	subject := uuid.NewString()
	seedMembership(t, super, tenantID, subject, "admin")
	c := auth.WithIdentity(ctx, auth.Identity{Subject: subject, Role: "authenticated", TenantID: tenantID})

	var beforeState, beforeUpdatedAt string
	if err := super.QueryRow(ctx,
		`SELECT state, updated_at::text FROM submission_jobs WHERE id = $1`, jobID,
	).Scan(&beforeState, &beforeUpdatedAt); err != nil {
		t.Fatalf("read submission_jobs before: %v", err)
	}
	beforeTransitioned := auditCount(t, app, tenantID, "invoice.transitioned")

	store := NewStore(app)
	if _, err := store.UnresolveOutside(c, invID); err != nil {
		t.Fatalf("UnresolveOutside: %v", err)
	}

	var status, afterState, afterUpdatedAt string
	var failureKind *string
	if err := super.QueryRow(ctx,
		`SELECT status, failure_kind FROM invoices WHERE id = $1`, invID,
	).Scan(&status, &failureKind); err != nil {
		t.Fatalf("read invoice after: %v", err)
	}
	if status != string(StatusFailed) {
		t.Errorf("status = %q, want unchanged %q", status, StatusFailed)
	}
	if failureKind == nil || *failureKind != "never_acknowledged" {
		t.Errorf("failure_kind = %v, want unchanged %q", failureKind, "never_acknowledged")
	}

	if err := super.QueryRow(ctx,
		`SELECT state, updated_at::text FROM submission_jobs WHERE id = $1`, jobID,
	).Scan(&afterState, &afterUpdatedAt); err != nil {
		t.Fatalf("read submission_jobs after: %v", err)
	}
	if afterState != beforeState || afterUpdatedAt != beforeUpdatedAt {
		t.Errorf("submission_jobs row changed: before=(%s,%s) after=(%s,%s), want byte-identical", beforeState, beforeUpdatedAt, afterState, afterUpdatedAt)
	}

	if n := auditCount(t, app, tenantID, "invoice.transitioned"); n != beforeTransitioned {
		t.Errorf("invoice.transitioned audit rows = %d, want unchanged %d", n, beforeTransitioned)
	}
}

// T2-12: a `preparer` caller cannot UnresolveOutside; the mark stays set.
func TestUnresolveOutside_PreparerIsNotPermitted(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := seedTenant(t, super, "T2-12 tenant")
	entityID := seedEntity(t, super, tenantID, "T2-12 entity")
	invID := seedResolvedFailed(t, super, tenantID, entityID, "T2-12", uuid.NewString(), "stays resolved")

	subject := uuid.NewString()
	seedMembership(t, super, tenantID, subject, "preparer")
	c := auth.WithIdentity(ctx, auth.Identity{Subject: subject, Role: "authenticated", TenantID: tenantID})

	beforeAt, beforeBy, beforeReason := mustKeptAsIsTriple(t, super, invID)

	store := NewStore(app)
	if _, err := store.UnresolveOutside(c, invID); !errors.Is(err, ErrNotPermitted) {
		t.Fatalf("UnresolveOutside (preparer) err = %v, want ErrNotPermitted", err)
	}

	afterAt, afterBy, afterReason := mustKeptAsIsTriple(t, super, invID)
	if afterAt == nil || afterBy == nil || afterReason == nil {
		t.Fatalf("triple after refused preparer UnresolveOutside = (%v,%v,%v), want it to STAY resolved (all non-NULL)", afterAt, afterBy, afterReason)
	}
	if *afterAt != *beforeAt || *afterBy != *beforeBy || *afterReason != *beforeReason {
		t.Errorf("triple changed across a refused UnresolveOutside: before=(%s,%s,%s) after=(%s,%s,%s), want byte-identical",
			*beforeAt, *beforeBy, *beforeReason, *afterAt, *afterBy, *afterReason)
	}
}

// T2-12b: a caller with no memberships row cannot UnresolveOutside either --
// AC-2 says "preparer OR no-membership" for BOTH methods; T2-4 covers
// no-membership for ResolveOutside, T2-12 covers preparer for UnresolveOutside,
// but nothing above pins UnresolveOutside's own no-membership leg.
func TestUnresolveOutside_NoMembershipIsNotPermitted(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := seedTenant(t, super, "T2-12b tenant")
	entityID := seedEntity(t, super, tenantID, "T2-12b entity")
	invID := seedResolvedFailed(t, super, tenantID, entityID, "T2-12b", uuid.NewString(), "stays resolved")

	subject := uuid.NewString() // no seedMembership call: no row at all
	c := auth.WithIdentity(ctx, auth.Identity{Subject: subject, Role: "authenticated", TenantID: tenantID})

	beforeAt, beforeBy, beforeReason := mustKeptAsIsTriple(t, super, invID)

	store := NewStore(app)
	if _, err := store.UnresolveOutside(c, invID); !errors.Is(err, ErrNotPermitted) {
		t.Fatalf("UnresolveOutside (no membership) err = %v, want ErrNotPermitted", err)
	}

	afterAt, afterBy, afterReason := mustKeptAsIsTriple(t, super, invID)
	if afterAt == nil || afterBy == nil || afterReason == nil {
		t.Fatalf("triple after refused no-membership UnresolveOutside = (%v,%v,%v), want it to STAY resolved", afterAt, afterBy, afterReason)
	}
	if *afterAt != *beforeAt || *afterBy != *beforeBy || *afterReason != *beforeReason {
		t.Errorf("triple changed across a refused UnresolveOutside: before=(%s,%s,%s) after=(%s,%s,%s), want byte-identical",
			*beforeAt, *beforeBy, *beforeReason, *afterAt, *afterBy, *afterReason)
	}
}

// --- T2-13..T2-15: interaction with pre-existing keep-as-is code -----------

// T2-13: UnkeepAsIs must leave a resolved-failed mark alone -- it is
// ResolveOutside/UnresolveOutside's mark to clear, not UnkeepAsIs's. Real RED:
// UnkeepAsIs is unstubbed, so today it clears ANY non-nil kept_as_is_at
// regardless of status, which is exactly the bug this story's fence closes.
func TestUnkeepAsIs_LeavesResolvedFailedMarkAlone(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := seedTenant(t, super, "T2-13 tenant")
	entityID := seedEntity(t, super, tenantID, "T2-13 entity")
	subject := uuid.NewString()
	invID := seedResolvedFailed(t, super, tenantID, entityID, "T2-13", subject, "resolved outside")
	c := auth.WithIdentity(ctx, auth.Identity{Subject: subject, Role: "authenticated", TenantID: tenantID})

	const event = "invoice.unkept_as_is"
	before := auditCount(t, app, tenantID, event)

	store := NewStore(app)
	got, err := store.UnkeepAsIs(c, invID)
	if err != nil {
		t.Fatalf("UnkeepAsIs: %v", err)
	}
	if got.KeptAsIsAt == nil || got.KeptAsIsBy == nil || got.KeptAsIsReason == nil {
		t.Fatalf("UnkeepAsIs on a resolved-failed invoice cleared the mark = (%v,%v,%v), want it to STAY set (only UnresolveOutside may clear a failed mark)", got.KeptAsIsAt, got.KeptAsIsBy, got.KeptAsIsReason)
	}

	at, by, reason := mustKeptAsIsTriple(t, super, invID)
	if at == nil || by == nil || reason == nil {
		t.Errorf("stored triple after UnkeepAsIs = (%v,%v,%v), want it to STAY set", at, by, reason)
	}
	if n := auditCount(t, app, tenantID, event); n != before {
		t.Errorf("invoice.unkept_as_is audit rows = %d, want unchanged %d (UnkeepAsIs must not touch a failed row's mark)", n, before)
	}
}

// T2-14: UnkeepAsIs's own pre-existing behaviour is unchanged -- a kept
// blocked draft is cleared and audited; an un-kept validated invoice is an
// unchanged no-op.
func TestUnkeepAsIs_DraftBehaviourUnchanged(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := seedTenant(t, super, "T2-14 tenant")
	entityID := seedEntity(t, super, tenantID, "T2-14 entity")
	subject := uuid.NewString()
	c := auth.WithIdentity(ctx, auth.Identity{Subject: subject, Role: "authenticated", TenantID: tenantID})
	store := NewStore(app)

	t.Run("kept blocked draft is cleared and audited", func(t *testing.T) {
		invID := seedDraftWithBlockingViolation(t, super, tenantID, entityID, "T2-14-draft")
		if _, err := store.KeepAsIs(c, invID, "will be un-kept"); err != nil {
			t.Fatalf("setup KeepAsIs: %v", err)
		}
		before := auditCount(t, app, tenantID, "invoice.unkept_as_is")

		got, err := store.UnkeepAsIs(c, invID)
		if err != nil {
			t.Fatalf("UnkeepAsIs: %v", err)
		}
		if got.KeptAsIsAt != nil {
			t.Errorf("UnkeepAsIs (draft) return KeptAsIsAt = %v, want nil (cleared)", got.KeptAsIsAt)
		}
		if n := auditCount(t, app, tenantID, "invoice.unkept_as_is"); n != before+1 {
			t.Errorf("invoice.unkept_as_is audit rows = %d, want %d (+1)", n, before+1)
		}
	})

	t.Run("un-kept validated invoice is a no-op", func(t *testing.T) {
		invID := seedInvoiceAtStatus(t, super, tenantID, entityID, "T2-14-validated", StatusValidated)
		before := auditCount(t, app, tenantID, "invoice.unkept_as_is")

		got, err := store.UnkeepAsIs(c, invID)
		if err != nil {
			t.Fatalf("UnkeepAsIs: %v", err)
		}
		if got.KeptAsIsAt != nil {
			t.Errorf("UnkeepAsIs (validated, never kept) return KeptAsIsAt = %v, want nil", got.KeptAsIsAt)
		}
		if n := auditCount(t, app, tenantID, "invoice.unkept_as_is"); n != before {
			t.Errorf("invoice.unkept_as_is audit rows = %d, want unchanged %d (a no-op must not audit)", n, before)
		}
	})
}

// T2-15: KeepAsIs refuses a failed invoice -- the two concepts (draft
// keep-as-is, failed resolve-outside) cannot cross.
func TestKeepAsIs_RefusesFailedInvoice(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := seedTenant(t, super, "T2-15 tenant")
	entityID := seedEntity(t, super, tenantID, "T2-15 entity")
	invID := seedInvoiceAtStatus(t, super, tenantID, entityID, "T2-15", StatusFailed)

	subject := uuid.NewString()
	c := auth.WithIdentity(ctx, auth.Identity{Subject: subject, Role: "authenticated", TenantID: tenantID})

	store := NewStore(app)
	if _, err := store.KeepAsIs(c, invID, "x"); !errors.Is(err, ErrNotKeepable) {
		t.Fatalf("KeepAsIs (failed invoice) err = %v, want ErrNotKeepable", err)
	}

	at, by, reason := mustKeptAsIsTriple(t, super, invID)
	if at != nil || by != nil || reason != nil {
		t.Errorf("triple after refused KeepAsIs on a failed invoice = (%v,%v,%v), want all NULL", at, by, reason)
	}
}

// --- T2-16..T2-17: matches every other store method -------------------------

// T2-16: a cross-tenant id yields ErrNotFound; the other tenant's row is
// untouched -- RLS does the isolation work, proven via the app-role pool, never
// a superuser bypass.
func TestRLS_ResolveOutsideCrossTenantIs404(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantA := seedTenant(t, super, "T2-16 tenant A")
	tenantB := seedTenant(t, super, "T2-16 tenant B")
	entityA := seedEntity(t, super, tenantA, "T2-16 A entity")
	invA := seedInvoiceAtStatus(t, super, tenantA, entityA, "T2-16-A", StatusFailed)

	subjectB := uuid.NewString()
	seedMembership(t, super, tenantB, subjectB, "admin")
	cB := auth.WithIdentity(ctx, auth.Identity{Subject: subjectB, Role: "authenticated", TenantID: tenantB})

	store := NewStore(app)
	if _, err := store.ResolveOutside(cB, invA, "cross-tenant attempt"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("ResolveOutside (tenant B on tenant A's invoice) err = %v, want ErrNotFound", err)
	}

	at, by, reason := mustKeptAsIsTriple(t, super, invA)
	if at != nil || by != nil || reason != nil {
		t.Errorf("tenant A's invoice triple after refused cross-tenant ResolveOutside = (%v,%v,%v), want all NULL", at, by, reason)
	}
}

// T2-17: a malformed id yields ErrValidation.
func TestResolveOutside_MalformedIDIsValidation(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := seedTenant(t, super, "T2-17 tenant")
	subject := uuid.NewString()
	seedMembership(t, super, tenantID, subject, "admin")
	c := auth.WithIdentity(ctx, auth.Identity{Subject: subject, Role: "authenticated", TenantID: tenantID})

	store := NewStore(app)
	if _, err := store.ResolveOutside(c, "not-a-uuid", "x"); !errors.Is(err, ErrValidation) {
		t.Fatalf("ResolveOutside (malformed id) err = %v, want ErrValidation", err)
	}
}

// T2-16b: a cross-tenant id yields ErrNotFound for UnresolveOutside too -- AC-8
// says "cross-tenant id -> ErrNotFound" without naming just one method, but
// T2-16 above only drives ResolveOutside.
func TestRLS_UnresolveOutsideCrossTenantIs404(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantA := seedTenant(t, super, "T2-16b tenant A")
	tenantB := seedTenant(t, super, "T2-16b tenant B")
	entityA := seedEntity(t, super, tenantA, "T2-16b A entity")
	invA := seedResolvedFailed(t, super, tenantA, entityA, "T2-16b-A", uuid.NewString(), "resolved by tenant A")

	subjectB := uuid.NewString()
	seedMembership(t, super, tenantB, subjectB, "admin")
	cB := auth.WithIdentity(ctx, auth.Identity{Subject: subjectB, Role: "authenticated", TenantID: tenantB})

	beforeAt, beforeBy, beforeReason := mustKeptAsIsTriple(t, super, invA)

	store := NewStore(app)
	if _, err := store.UnresolveOutside(cB, invA); !errors.Is(err, ErrNotFound) {
		t.Fatalf("UnresolveOutside (tenant B on tenant A's invoice) err = %v, want ErrNotFound", err)
	}

	afterAt, afterBy, afterReason := mustKeptAsIsTriple(t, super, invA)
	if afterAt == nil || afterBy == nil || afterReason == nil {
		t.Fatalf("tenant A's invoice triple after refused cross-tenant UnresolveOutside = (%v,%v,%v), want it to STAY resolved", afterAt, afterBy, afterReason)
	}
	if *afterAt != *beforeAt || *afterBy != *beforeBy || *afterReason != *beforeReason {
		t.Errorf("tenant A's triple changed across a refused cross-tenant UnresolveOutside: before=(%s,%s,%s) after=(%s,%s,%s), want byte-identical",
			*beforeAt, *beforeBy, *beforeReason, *afterAt, *afterBy, *afterReason)
	}
}

// T2-17b: a malformed id yields ErrValidation for UnresolveOutside too.
func TestUnresolveOutside_MalformedIDIsValidation(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := seedTenant(t, super, "T2-17b tenant")
	subject := uuid.NewString()
	seedMembership(t, super, tenantID, subject, "admin")
	c := auth.WithIdentity(ctx, auth.Identity{Subject: subject, Role: "authenticated", TenantID: tenantID})

	store := NewStore(app)
	if _, err := store.UnresolveOutside(c, "not-a-uuid"); !errors.Is(err, ErrValidation) {
		t.Fatalf("UnresolveOutside (malformed id) err = %v, want ErrValidation", err)
	}
}

// --- T2-18: isApprover, pure unit test --------------------------------------

// T2-18: isApprover admits only admin/reviewer -- a pure unit test, deliberately
// NOT routed through dbTestPools (it would skip unconditionally without a local
// DB), matching TestStatusForErr_NotFixableIs409's own un-gated style.
func TestIsApprover_AdminAndReviewerOnly(t *testing.T) {
	cases := []struct {
		role string
		want bool
	}{
		{"admin", true},
		{"reviewer", true},
		{"preparer", false},
		{"", false},
		{"authenticated", false},
	}
	for _, c := range cases {
		if got := isApprover(c.role); got != c.want {
			t.Errorf("isApprover(%q) = %v, want %v", c.role, got, c.want)
		}
	}
}

// --- QA adversarial coverage --------------------------------------------------

// TestCallerRole_TenantScopedNotUserScoped: one user carries a DIFFERENT
// memberships row (and role) in two tenants. CallerRole must see only the row
// for the RLS-active tenant, not "the user's role somewhere" -- this pins the
// tenant-scoping tenancy.Store.Me already relies on for the identical query
// shape, which nothing above exercised for CallerRole specifically (every T2
// test above uses one tenant per case).
func TestCallerRole_TenantScopedNotUserScoped(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantA := seedTenant(t, super, "T2-ROLE-SCOPE tenant A")
	tenantB := seedTenant(t, super, "T2-ROLE-SCOPE tenant B")
	subject := uuid.NewString()
	seedMembership(t, super, tenantA, subject, "admin")
	seedMembership(t, super, tenantB, subject, "preparer")

	store := NewStore(app)
	cA := auth.WithIdentity(ctx, auth.Identity{Subject: subject, Role: "authenticated", TenantID: tenantA})
	cB := auth.WithIdentity(ctx, auth.Identity{Subject: subject, Role: "authenticated", TenantID: tenantB})

	if role, err := store.CallerRole(cA); err != nil || role != "admin" {
		t.Errorf("CallerRole (tenant A) = (%q, %v), want (\"admin\", nil)", role, err)
	}
	if role, err := store.CallerRole(cB); err != nil || role != "preparer" {
		t.Errorf("CallerRole (tenant B) = (%q, %v), want (\"preparer\", nil)", role, err)
	}
}

// TestResolveOutside_PreparerRefusalIndistinguishableFromUnknownID: the
// permission check (CallerRole) takes no id parameter and runs before any row
// is ever looked at, so a non-approver must get ErrNotPermitted identically
// whether id names a real failed invoice or nothing at all -- no
// 403-vs-404/409 existence oracle.
func TestResolveOutside_PreparerRefusalIndistinguishableFromUnknownID(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := seedTenant(t, super, "T2-GUARD-ORDER tenant")
	entityID := seedEntity(t, super, tenantID, "T2-GUARD-ORDER entity")
	realID := seedInvoiceAtStatus(t, super, tenantID, entityID, "T2-GUARD-ORDER", StatusFailed)

	subject := uuid.NewString()
	seedMembership(t, super, tenantID, subject, "preparer")
	c := auth.WithIdentity(ctx, auth.Identity{Subject: subject, Role: "authenticated", TenantID: tenantID})

	store := NewStore(app)
	_, errReal := store.ResolveOutside(c, realID, "x")
	_, errUnknown := store.ResolveOutside(c, uuid.NewString(), "x")
	if !errors.Is(errReal, ErrNotPermitted) || !errors.Is(errUnknown, ErrNotPermitted) {
		t.Fatalf("errs = (%v, %v), want both ErrNotPermitted", errReal, errUnknown)
	}
}

// TestResolveOutside_AuditFailureRollsBackColumnWrite (AC-5): proves "a failing
// audit rolls the mark back" for ResolveOutside. This does NOT call
// Store.ResolveOutside directly: the 256-char-actor technique
// TestKeptAsIs_AuditInSameTx/GATE-13 use to trip audit_log's char_length(actor)
// CHECK cannot reach ResolveOutside's audit write at all here, because
// CallerRole's own memberships lookup binds the SAME Subject against a
// uuid-typed column FIRST -- a malformed Subject 22P02s there and never reaches
// the write tx (verified: CallerRole with a 256-char Subject returns a raw
// pgconn 22P02, not ErrNotPermitted). So this reconstructs ResolveOutside's
// write-tx body -- the exact lock/status-check/UPDATE/audit.Record sequence,
// store.go's ResolveOutside -- directly on the app pool, with a bad actor bound
// only to the audit call, and proves the UPDATE rolls back with it.
func TestResolveOutside_AuditFailureRollsBackColumnWrite(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := seedTenant(t, super, "T2-AUDIT-TX tenant")
	entityID := seedEntity(t, super, tenantID, "T2-AUDIT-TX entity")
	invID := seedInvoiceAtStatus(t, super, tenantID, entityID, "T2-AUDIT-TX", StatusFailed)

	badActor := strings.Repeat("a", 256)
	err := db.WithinTenantTx(ctx, app, tenantID, func(tx pgx.Tx) error {
		var locked Invoice
		if err := scanInvoice(tx.QueryRow(ctx,
			`SELECT `+invoiceColumns+` FROM invoices WHERE id = $1 FOR UPDATE`, invID,
		), &locked); err != nil {
			return err
		}
		if locked.Status != StatusFailed {
			return ErrNotResolvable
		}
		if err := scanInvoice(tx.QueryRow(ctx,
			`UPDATE invoices SET kept_as_is_at = $1, kept_as_is_by = $2, kept_as_is_reason = $3
			 WHERE id = $4 RETURNING `+invoiceColumns,
			time.Now().UTC(), badActor, "should never land", invID,
		), &locked); err != nil {
			return err
		}
		return audit.Record(ctx, tx, badActor, "invoice.resolved_outside", map[string]any{"id": invID, "reason": "should never land"})
	})
	if err == nil {
		t.Fatal("write-tx with a 256-char audit actor succeeded, want an audit_log actor CHECK violation (SQLSTATE 23514)")
	}
	if code := pgCode(err); code != "23514" {
		t.Fatalf("pgCode = %q, want 23514 (check_violation): %v", code, err)
	}

	at, by, reason := mustKeptAsIsTriple(t, super, invID)
	if at != nil || by != nil || reason != nil {
		t.Errorf("triple after a rolled-back write = (%v,%v,%v), want all NULL (the column write rolled back WITH the failed audit write)", at, by, reason)
	}
}

// TestUnresolveOutside_AuditFailureRollsBackColumnWrite (AC-5): UnresolveOutside's
// clear-direction sibling of the test above, same rationale (CallerRole gates
// on the same uuid-typed Subject first, so the bad-actor fault has to be
// injected directly on the write-tx body, not through the real method).
func TestUnresolveOutside_AuditFailureRollsBackColumnWrite(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := seedTenant(t, super, "T2-UNRESOLVE-AUDIT-TX tenant")
	entityID := seedEntity(t, super, tenantID, "T2-UNRESOLVE-AUDIT-TX entity")
	invID := seedResolvedFailed(t, super, tenantID, entityID, "T2-UNRESOLVE-AUDIT-TX", uuid.NewString(), "was resolved")

	badActor := strings.Repeat("a", 256)
	err := db.WithinTenantTx(ctx, app, tenantID, func(tx pgx.Tx) error {
		var locked Invoice
		if err := scanInvoice(tx.QueryRow(ctx,
			`SELECT `+invoiceColumns+` FROM invoices WHERE id = $1 FOR UPDATE`, invID,
		), &locked); err != nil {
			return err
		}
		if locked.Status != StatusFailed {
			return ErrNotResolvable
		}
		if locked.KeptAsIsAt == nil {
			return errors.New("setup: fixture was not resolved, this test would be vacuous")
		}
		if err := scanInvoice(tx.QueryRow(ctx,
			`UPDATE invoices SET kept_as_is_at = NULL, kept_as_is_by = NULL, kept_as_is_reason = NULL
			 WHERE id = $1 RETURNING `+invoiceColumns, invID,
		), &locked); err != nil {
			return err
		}
		return audit.Record(ctx, tx, badActor, "invoice.unresolved_outside", map[string]any{"id": invID})
	})
	if err == nil {
		t.Fatal("write-tx with a 256-char audit actor succeeded, want an audit_log actor CHECK violation (SQLSTATE 23514)")
	}
	if code := pgCode(err); code != "23514" {
		t.Fatalf("pgCode = %q, want 23514 (check_violation): %v", code, err)
	}

	at, by, reason := mustKeptAsIsTriple(t, super, invID)
	if at == nil || by == nil || reason == nil {
		t.Errorf("triple after a rolled-back clear = (%v,%v,%v), want it to STAY resolved (the UPDATE rolled back WITH the failed audit write)", at, by, reason)
	}
}

// TestUnkeepAsIs_NoopAcrossEveryNonDraftStatusWithNoMark (AC-7): UnkeepAsIs's
// no-op guard (`locked.KeptAsIsAt == nil || locked.Status != StatusDraft`)
// short-circuits on the nil check, so it is structurally status-independent --
// but T2-14 only exercises draft and validated. This drives every remaining
// status (queued/submitted/accepted/rejected/failed), all pre-story reachable
// with no mark (the old invoices_kept_as_is_draft_only CHECK only ever barred
// a NON-null mark outside draft; NULL was always legal everywhere), proving
// the no-op is genuinely uniform across the whole surface, not just the two
// statuses already under test.
func TestUnkeepAsIs_NoopAcrossEveryNonDraftStatusWithNoMark(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := seedTenant(t, super, "T2-UNKEEP-SURFACE tenant")
	entityID := seedEntity(t, super, tenantID, "T2-UNKEEP-SURFACE entity")
	subject := uuid.NewString()
	c := auth.WithIdentity(ctx, auth.Identity{Subject: subject, Role: "authenticated", TenantID: tenantID})
	store := NewStore(app)

	for _, status := range []Status{StatusQueued, StatusSubmitted, StatusAccepted, StatusRejected, StatusFailed} {
		t.Run(string(status), func(t *testing.T) {
			invID := seedInvoiceAtStatus(t, super, tenantID, entityID, "T2-UNKEEP-SURFACE-"+string(status), status)
			before := auditCount(t, app, tenantID, "invoice.unkept_as_is")

			got, err := store.UnkeepAsIs(c, invID)
			if err != nil {
				t.Fatalf("UnkeepAsIs (%s): %v", status, err)
			}
			if got.KeptAsIsAt != nil {
				t.Errorf("UnkeepAsIs (%s) return KeptAsIsAt = %v, want nil", status, got.KeptAsIsAt)
			}
			if got.Status != status {
				t.Errorf("UnkeepAsIs (%s) return status = %q, want unchanged", status, got.Status)
			}
			if n := auditCount(t, app, tenantID, "invoice.unkept_as_is"); n != before {
				t.Errorf("invoice.unkept_as_is audit rows (%s) = %d, want unchanged %d (a no-op must not audit)", status, n, before)
			}
		})
	}
}

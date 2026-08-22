// Test-first (RED) suite for audit_log's BEFORE INSERT entity_id trigger.
//
// Every case writes as invoice_app under a tenant GUC (db.WithinTenantTx). The resolver
// is SECURITY INVOKER, so its isolation is inherited from the writer's RLS context — a
// migrator or superuser insert would resolve a foreign invoice and pass the cross-tenant
// case that should fail.
//
// requireInsertTrigger runs first in every case: without the trigger, entity_id is NULL
// on every row and the NULL assertions would pass on an unbuilt feature.
//
// audit_log rows can never be deleted, so each case mints a fresh tenant uuid and leaves
// its audit rows behind; only the tenant's own domain rows are cleaned up.
package audit_test

import (
	"context"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/SimonOsipov/invoice-os/internal/audit"
	"github.com/SimonOsipov/invoice-os/internal/platform/db"
)

// The 21 attributable events, by rule, plus the 15 workspace-level ones.
var (
	triggerRuleAEvents = []string{ // bare `id`, looked up through invoices
		"invoice.created",
		"invoice.updated",
		"invoice.transitioned",
		"invoice.validated",
		"invoice.kept_as_is",
		"invoice.unkept_as_is",
		"invoice.resolved_outside",
		"invoice.unresolved_outside",
		"invoice.approval_armed",
		"invoice.approval_cancelled",
	}
	triggerRuleBEvents = []string{ // `invoice_id` spelling, same lookup
		"invoice.approval_approved",
		"invoice.approval_rejected",
		"submission.accepted",
		"submission.rejected",
		"submission.failed",
		"reconciliation.drift_detected",
		"reconciliation.auto_fixed",
	}
	triggerRuleCEvents = []string{ // the entity id is already in the payload
		"portfolio.entity.created",
		"portfolio.entity.updated",
		"portfolio.entity.onboarded",
		"portfolio.entity.offboarded",
	}
)

// triggerRuleDPayloads pairs each workspace-level event with the payload shape its call
// site writes. document.* deliberately gets a REAL invoice id here: attribution is
// event-scoped, so a key-scoped resolver would wrongly attribute it.
func triggerRuleDPayloads(invoiceID string) map[string]map[string]any {
	policyID, userID := uuid.NewString(), uuid.NewString()
	return map[string]map[string]any{
		"approval_policy.created":   {"policy_id": policyID},
		"approval_policy.updated":   {"policy_id": policyID},
		"approval_policy.published": {"policy_id": policyID},
		"approval_policy.deleted":   {"policy_id": policyID},
		"workflow_role.created":     {"key": "approver"},
		"workflow_role.updated":     {"key": "approver"},
		"workflow_role.deleted":     {"key": "approver"},
		"workflow_role.staffed":     {"key": "approver"},
		"document.created":          {"id": invoiceID},
		"document.read":             {"id": invoiceID},
		"document.reused":           {"id": invoiceID},
		"membership.suspended":      {"user_id": userID},
		"membership.reactivated":    {"user_id": userID},
		"validation.rule.enabled":   {"key": "buyer-tin-present"},
		"validation.rule.disabled":  {"key": "buyer-tin-present"},
	}
}

// --- acceptance cases ----------------------------------------------------------------

// AC-13, AC-14: every invoice-scoped event resolves to exactly the invoice's entity, in
// both payload spellings.
func TestAudit_InsertTriggerResolvesInvoiceScopedEvents(t *testing.T) {
	f := requireFixture(t)
	requireInsertTrigger(t, f)
	fx := seedTriggerFixture(t, f)

	if len(triggerRuleAEvents) != 10 || len(triggerRuleBEvents) != 7 {
		t.Fatalf("rule A/B hold %d/%d events, want 10/7", len(triggerRuleAEvents), len(triggerRuleBEvents))
	}
	for _, event := range triggerRuleAEvents {
		recordAudit(t, f, fx.tenant, event, map[string]any{"id": fx.invoice})
	}
	for _, event := range triggerRuleBEvents {
		recordAudit(t, f, fx.tenant, event, map[string]any{"invoice_id": fx.invoice})
	}

	got := triggerEntityIDs(t, f, fx.tenant)
	for _, event := range append(append([]string{}, triggerRuleAEvents...), triggerRuleBEvents...) {
		assertTriggerEntity(t, got, event, fx.entity)
	}
}

// The transmission failure is attributed from its very first row. A NULL entity_id is a
// positive firm-wide claim (docs/audit-log-read-contract.md §3), so an unresolved row of
// this event would be false, not merely thin.
func TestAudit_InsertTriggerResolvesSubmissionFailed(t *testing.T) {
	f := requireFixture(t)
	requireInsertTrigger(t, f)
	fx := seedTriggerFixture(t, f)

	recordAudit(t, f, fx.tenant, "submission.failed", map[string]any{"invoice_id": fx.invoice})
	recordAudit(t, f, fx.tenant, "submission.accepted", map[string]any{"invoice_id": fx.invoice})

	got := triggerEntityIDs(t, f, fx.tenant)
	assertTriggerEntity(t, got, "submission.failed", fx.entity)
	// Its already-attributed sibling is the positive control: it rules out a broken
	// fixture reading as a missing rule.
	assertTriggerEntity(t, got, "submission.accepted", fx.entity)
}

// AC-13: portfolio.entity.* carries the entity id itself, so it resolves with no join.
func TestAudit_InsertTriggerResolvesPortfolioEventsFromTheirOwnPayload(t *testing.T) {
	f := requireFixture(t)
	requireInsertTrigger(t, f)
	fx := seedTriggerFixture(t, f)

	if len(triggerRuleCEvents) != 4 {
		t.Fatalf("rule C holds %d events, want 4", len(triggerRuleCEvents))
	}
	if n := invoicesWithID(t, f, fx.tenant, fx.entity); n != 0 {
		t.Fatalf("invoices with id = the entity id = %d, want 0 — an invoices lookup could "+
			"otherwise produce the same answer as the direct payload read", n)
	}
	for _, event := range triggerRuleCEvents {
		recordAudit(t, f, fx.tenant, event, map[string]any{"id": fx.entity})
	}

	got := triggerEntityIDs(t, f, fx.tenant)
	for _, event := range triggerRuleCEvents {
		assertTriggerEntity(t, got, event, fx.entity)
	}
}

// AC-13: the 15 workspace-level events stay NULL even when their payload holds a real
// invoice id. The rule-A row in the same test is the positive control.
func TestAudit_InsertTriggerLeavesWorkspaceEventsNull(t *testing.T) {
	f := requireFixture(t)
	requireInsertTrigger(t, f)
	fx := seedTriggerFixture(t, f)

	ruleD := triggerRuleDPayloads(fx.invoice)
	if len(ruleD) != 15 {
		t.Fatalf("rule-D payload map holds %d events, want 15", len(ruleD))
	}
	for event, payload := range ruleD {
		recordAudit(t, f, fx.tenant, event, payload)
	}
	recordAudit(t, f, fx.tenant, "invoice.created", map[string]any{"id": fx.invoice})

	got := triggerEntityIDs(t, f, fx.tenant)
	for event := range ruleD {
		assertTriggerEntityNull(t, got, event)
	}
	assertTriggerEntity(t, got, "invoice.created", fx.entity)
}

// AC-13: the real call order writes the invoice and its audit row in ONE transaction, so
// a STABLE resolver must see the uncommitted sibling row.
func TestAudit_InsertTriggerResolvesAnInvoiceCreatedInTheSameTransaction(t *testing.T) {
	f := requireFixture(t)
	requireInsertTrigger(t, f)
	fx := seedTriggerFixture(t, f)
	ctx := context.Background()

	invoice := uuid.NewString()
	if err := db.WithinTenantTx(ctx, f.app, fx.tenant, func(tx pgx.Tx) error {
		if _, e := tx.Exec(ctx,
			`INSERT INTO invoices (id, tenant_id, entity_id, invoice_number) VALUES ($1, $2, $3, $4)`,
			invoice, fx.tenant, fx.entity, "INV-"+invoice[:8]); e != nil {
			return e
		}
		return audit.Record(ctx, tx, "actor", "invoice.created", map[string]any{"id": invoice})
	}); err != nil {
		t.Fatalf("write invoice + audit row in one tx: %v", err)
	}

	assertTriggerEntity(t, triggerEntityIDs(t, f, fx.tenant), "invoice.created", fx.entity)
}

// AC-14: the resolver runs under the writer's RLS, so another tenant's invoice is
// invisible to it. The writer's own invoice resolving in the same test is what rules out
// a resolver that always returns NULL.
func TestAudit_InsertTriggerCannotResolveAnotherTenantsInvoice(t *testing.T) {
	f := requireFixture(t)
	requireInsertTrigger(t, f)
	a := seedTriggerFixture(t, f)
	b := seedTriggerFixture(t, f)

	recordAudit(t, f, a.tenant, "invoice.updated", map[string]any{"id": b.invoice})
	recordAudit(t, f, a.tenant, "invoice.created", map[string]any{"id": a.invoice})

	got := triggerEntityIDs(t, f, a.tenant)
	assertTriggerEntityNull(t, got, "invoice.updated")
	assertTriggerEntity(t, got, "invoice.created", a.entity)
}

// AC-15: a non-uuid payload id yields NULL and never aborts the caller. The companion
// idempotency_keys row proves the surrounding transaction still committed — real callers
// build the id as text (internal/invoice/source_document_read_test.go:51).
func TestAudit_InsertTriggerToleratesAMalformedPayloadID(t *testing.T) {
	f := requireFixture(t)
	requireInsertTrigger(t, f)
	fx := seedTriggerFixture(t, f)
	ctx := context.Background()

	key := uuid.NewString()
	if err := db.WithinTenantTx(ctx, f.app, fx.tenant, func(tx pgx.Tx) error {
		if e := audit.Record(ctx, tx, "actor", "invoice.created", map[string]any{"id": "not-a-uuid"}); e != nil {
			return e
		}
		_, e := tx.Exec(ctx, `INSERT INTO idempotency_keys (tenant_id, key) VALUES ($1, $2)`, fx.tenant, key)
		return e
	}); err != nil {
		t.Fatalf("malformed payload id aborted the caller's transaction: %v", err)
	}

	if n := keyCount(t, f.app, fx.tenant, key); n != 1 {
		t.Errorf("idempotency_keys rows after commit = %d, want 1 — the transaction did not commit", n)
	}
	if n := auditCount(t, f.app, fx.tenant, "invoice.created"); n != 1 {
		t.Errorf("audit rows after commit = %d, want 1", n)
	}
	assertTriggerEntityNull(t, triggerEntityIDs(t, f, fx.tenant), "invoice.created")
}

// AC-1, AC-15: uuid_in accepts more spellings than canonical form, and six live routes
// echo the raw URL path segment into the payload without parsing it
// (internal/invoice/handlers.go). A hyphenless or brace-wrapped id names the same invoice,
// so it must resolve to the same entity -- NULL here reads as workspace-level and misfiles
// a client action as firm-wide. The refused spellings are exactly the ones uuid_in itself
// refuses; they must yield NULL without aborting the caller, which the companion
// idempotency_keys row proves.
func TestAudit_InsertTriggerResolvesEverySpellingUUIDInAccepts(t *testing.T) {
	f := requireFixture(t)
	requireInsertTrigger(t, f)
	fx := seedTriggerFixture(t, f)
	ctx := context.Background()

	bare := strings.ReplaceAll(fx.invoice, "-", "")
	if len(bare) != 32 || bare == fx.invoice {
		t.Fatalf("hyphenless id %q is %d chars, want a 32-char restyling of %s", bare, len(bare), fx.invoice)
	}

	resolves := map[string]string{
		"invoice.created":          fx.invoice,             // canonical, the positive control
		"invoice.kept_as_is":       bare,                   // hyphenless
		"invoice.unkept_as_is":     "{" + fx.invoice + "}", // brace-wrapped
		"invoice.resolved_outside": "{" + bare + "}",
		"invoice.validated":        strings.ToUpper(fx.invoice),
	}
	refuses := map[string]string{
		"invoice.transitioned":       bare[:3] + "-" + bare[3:], // hyphen off the 4-hex boundary
		"invoice.unresolved_outside": "{" + fx.invoice,          // unclosed brace
		"invoice.updated":            "not-a-uuid",
	}

	key := uuid.NewString()
	if err := db.WithinTenantTx(ctx, f.app, fx.tenant, func(tx pgx.Tx) error {
		for _, batch := range []map[string]string{resolves, refuses} {
			for event, id := range batch {
				if e := audit.Record(ctx, tx, "spelling-fixture", event, map[string]any{"id": id}); e != nil {
					return e
				}
			}
		}
		_, e := tx.Exec(ctx, `INSERT INTO idempotency_keys (tenant_id, key) VALUES ($1, $2)`, fx.tenant, key)
		return e
	}); err != nil {
		t.Fatalf("a spelling aborted the caller's transaction: %v", err)
	}

	if n := keyCount(t, f.app, fx.tenant, key); n != 1 {
		t.Errorf("idempotency_keys rows after commit = %d, want 1 — the transaction did not commit", n)
	}

	got := triggerEntityIDs(t, f, fx.tenant)
	for event := range resolves {
		assertTriggerEntity(t, got, event, fx.entity)
	}
	for event := range refuses {
		assertTriggerEntityNull(t, got, event)
	}
}

// --- helpers -------------------------------------------------------------------------

// requireInsertTrigger fails when the write-time trigger is absent. Every NULL assertion
// in this file would otherwise hold on an unbuilt feature.
func requireInsertTrigger(t *testing.T, f *fixture) {
	t.Helper()
	var n int
	if err := f.app.QueryRow(context.Background(),
		`SELECT count(*) FROM pg_trigger
		   WHERE tgrelid = 'audit_log'::regclass AND tgname = 'audit_log_entity_on_insert' AND NOT tgisinternal`,
	).Scan(&n); err != nil {
		t.Fatalf("look up audit_log_entity_on_insert: %v", err)
	}
	if n != 1 {
		t.Fatalf("triggers named audit_log_entity_on_insert on audit_log = %d, want 1", n)
	}
}

type triggerFixture struct {
	tenant  string
	entity  string
	invoice string
}

// seedTriggerFixture commits a throwaway tenant with one entity and one invoice. The
// tenant row goes in as the migrator (invoice_app holds SELECT only on tenants); the rest
// as the app role under that tenant's context. Ids are generated here, never read from
// db/seed.dev.sql, whose rows default theirs to gen_random_uuid().
func seedTriggerFixture(t *testing.T, f *fixture) triggerFixture {
	t.Helper()
	ctx := context.Background()
	fx := triggerFixture{tenant: uuid.NewString(), entity: uuid.NewString(), invoice: uuid.NewString()}

	if err := db.WithinTenantTx(ctx, f.mig, fx.tenant, func(tx pgx.Tx) error {
		_, e := tx.Exec(ctx, `INSERT INTO tenants (id, name) VALUES ($1, $2)`, fx.tenant, "audit-trigger-"+fx.tenant[:8])
		return e
	}); err != nil {
		t.Fatalf("seed tenant: %v", err)
	}
	if err := db.WithinTenantTx(ctx, f.app, fx.tenant, func(tx pgx.Tx) error {
		if _, e := tx.Exec(ctx,
			`INSERT INTO business_entities (id, tenant_id, name) VALUES ($1, $2, $3)`,
			fx.entity, fx.tenant, "entity-"+fx.entity[:8]); e != nil {
			return e
		}
		_, e := tx.Exec(ctx,
			`INSERT INTO invoices (id, tenant_id, entity_id, invoice_number) VALUES ($1, $2, $3, $4)`,
			fx.invoice, fx.tenant, fx.entity, "INV-"+fx.invoice[:8])
		return e
	}); err != nil {
		t.Fatalf("seed entity + invoice: %v", err)
	}

	t.Cleanup(func() {
		// Reverse FK order, as the owner; audit_log rows have no FK here and stay.
		_ = db.WithinTenantTx(context.Background(), f.mig, fx.tenant, func(tx pgx.Tx) error {
			for _, sql := range []string{
				`DELETE FROM invoices WHERE tenant_id = $1`,
				`DELETE FROM business_entities WHERE tenant_id = $1`,
				`DELETE FROM tenants WHERE id = $1`,
			} {
				if _, e := tx.Exec(context.Background(), sql, fx.tenant); e != nil {
					return e
				}
			}
			return nil
		})
	})
	return fx
}

// recordAudit writes one audit row through the production entry point, as invoice_app.
func recordAudit(t *testing.T, f *fixture, tenant, event string, payload map[string]any) {
	t.Helper()
	ctx := context.Background()
	if err := db.WithinTenantTx(ctx, f.app, tenant, func(tx pgx.Tx) error {
		return audit.Record(ctx, tx, "trigger-fixture", event, payload)
	}); err != nil {
		t.Fatalf("record %s: %v", event, err)
	}
}

// triggerEntityIDs maps event -> entity_id for one tenant's audit rows, read as the app
// role under that tenant's context.
func triggerEntityIDs(t *testing.T, f *fixture, tenant string) map[string]*string {
	t.Helper()
	ctx := context.Background()
	out := map[string]*string{}
	if err := db.WithinTenantTx(ctx, f.app, tenant, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT event, entity_id FROM audit_log WHERE tenant_id = $1`, tenant)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var event string
			var entity *string
			if err := rows.Scan(&event, &entity); err != nil {
				return err
			}
			out[event] = entity
		}
		return rows.Err()
	}); err != nil {
		t.Fatalf("read back entity_id for tenant %s: %v", tenant, err)
	}
	if len(out) == 0 {
		t.Fatalf("no audit rows visible for tenant %s — the fixture never landed, so every "+
			"assertion below would be vacuous", tenant)
	}
	return out
}

func invoicesWithID(t *testing.T, f *fixture, tenant, id string) int {
	t.Helper()
	ctx := context.Background()
	var n int
	if err := db.WithinTenantTx(ctx, f.app, tenant, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT count(*) FROM invoices WHERE id = $1`, id).Scan(&n)
	}); err != nil {
		t.Fatalf("count invoices with id %s: %v", id, err)
	}
	return n
}

func assertTriggerEntity(t *testing.T, got map[string]*string, event, want string) {
	t.Helper()
	v, ok := got[event]
	if !ok {
		t.Errorf("event %s: no audit row read back (have %s)", event, strings.Join(eventNames(got), ", "))
		return
	}
	if v == nil {
		t.Errorf("event %s: entity_id IS NULL, want %s", event, want)
		return
	}
	if *v != want {
		t.Errorf("event %s: entity_id = %s, want %s", event, *v, want)
	}
}

func assertTriggerEntityNull(t *testing.T, got map[string]*string, event string) {
	t.Helper()
	v, ok := got[event]
	if !ok {
		t.Errorf("event %s: no audit row read back (have %s)", event, strings.Join(eventNames(got), ", "))
		return
	}
	if v != nil {
		t.Errorf("event %s: entity_id = %s, want NULL", event, *v)
	}
}

func eventNames(got map[string]*string) []string {
	out := make([]string, 0, len(got))
	for k := range got {
		out = append(out, k)
	}
	return out
}

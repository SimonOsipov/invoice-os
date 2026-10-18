// audit_log_entity_for attributes invoice_register.exported to the client in its payload
// `entity_id` (a direct branch). The resolver runs at BEFORE INSERT and audit_log is
// append-only, so a row written without the branch stays unattributed forever.
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

const registerExportEvent = "invoice_register.exported"

func registerExportPayload(entity any, label string) map[string]any {
	return map[string]any{"entity_id": entity, "from": "2026-01-01", "to": "2026-01-31", "row_count": 2, "label": label}
}

// registerExportEntities maps payload label -> entity_id for the tenant's export rows.
func registerExportEntities(t *testing.T, f *fixture, tenant string) map[string]*string {
	t.Helper()
	ctx := context.Background()
	out := map[string]*string{}
	if err := db.WithinTenantTx(ctx, f.app, tenant, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx,
			`SELECT payload->>'label', entity_id::text FROM audit_log WHERE tenant_id = $1 AND event = $2`,
			tenant, registerExportEvent)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var label string
			var entity *string
			if err := rows.Scan(&label, &entity); err != nil {
				return err
			}
			out[label] = entity
		}
		return rows.Err()
	}); err != nil {
		t.Fatalf("read back %s rows for tenant %s: %v", registerExportEvent, tenant, err)
	}
	return out
}

func assertRegisterExportEntity(t *testing.T, got map[string]*string, label string, want *string) {
	t.Helper()
	v, ok := got[label]
	switch {
	case !ok:
		t.Errorf("%s row %q: not read back", registerExportEvent, label)
	case want == nil && v != nil:
		t.Errorf("%s row %q: entity_id = %s, want NULL", registerExportEvent, label, *v)
	case want != nil && v == nil:
		t.Errorf("%s row %q: entity_id IS NULL, want %s", registerExportEvent, label, *want)
	case want != nil && *v != *want:
		t.Errorf("%s row %q: entity_id = %s, want %s", registerExportEvent, label, *v, *want)
	}
}

func seedSecondEntity(t *testing.T, f *fixture, tenant string) string {
	t.Helper()
	ctx := context.Background()
	id := uuid.NewString()
	if err := db.WithinTenantTx(ctx, f.app, tenant, func(tx pgx.Tx) error {
		_, e := tx.Exec(ctx, `INSERT INTO business_entities (id, tenant_id, name) VALUES ($1, $2, $3)`,
			id, tenant, "entity-"+id[:8])
		return e
	}); err != nil {
		t.Fatalf("seed second entity: %v", err)
	}
	return id
}

func TestAudit_InsertTriggerResolvesRegisterExportToItsClient(t *testing.T) {
	f := requireFixture(t)
	requireInsertTrigger(t, f)
	fx := seedTriggerFixture(t, f)

	// Control: a shipped event resolves through this fixture, so a NULL below is a missing branch.
	recordAudit(t, f, fx.tenant, "invoice.created", map[string]any{"id": fx.invoice})
	recordAudit(t, f, fx.tenant, registerExportEvent, registerExportPayload(fx.entity, "export"))

	assertTriggerEntity(t, triggerEntityIDs(t, f, fx.tenant), "invoice.created", fx.entity)
	assertTriggerEntity(t, triggerEntityIDs(t, f, fx.tenant), registerExportEvent, fx.entity)
}

func TestAudit_RegisterExportOfTwoClientsResolvesEachToItsOwn(t *testing.T) {
	f := requireFixture(t)
	requireInsertTrigger(t, f)
	fx := seedTriggerFixture(t, f)
	second := seedSecondEntity(t, f, fx.tenant)

	recordAudit(t, f, fx.tenant, registerExportEvent, registerExportPayload(fx.entity, "first"))
	recordAudit(t, f, fx.tenant, registerExportEvent, registerExportPayload(second, "second"))

	got := registerExportEntities(t, f, fx.tenant)
	if len(got) != 2 {
		t.Fatalf("read back %d export rows, want 2", len(got))
	}
	assertRegisterExportEntity(t, got, "first", &fx.entity)
	assertRegisterExportEntity(t, got, "second", &second)
}

// A legal spelling resolves through the shared UUID grammar gate.
func TestAudit_RegisterExportEntityIDSpellingsFollowTheSharedGrammar(t *testing.T) {
	f := requireFixture(t)
	requireInsertTrigger(t, f)
	fx := seedTriggerFixture(t, f)

	spellings := map[string]string{
		"canonical": fx.entity,
		"upper":     strings.ToUpper(fx.entity),
		"braced":    "{" + fx.entity + "}",
		"nohyphen":  strings.ReplaceAll(fx.entity, "-", ""),
	}
	for label, spelling := range spellings {
		recordAudit(t, f, fx.tenant, registerExportEvent, registerExportPayload(spelling, label))
	}

	got := registerExportEntities(t, f, fx.tenant)
	if len(got) != len(spellings) {
		t.Fatalf("read back %d export rows, want %d", len(got), len(spellings))
	}
	for label := range spellings {
		assertRegisterExportEntity(t, got, label, &fx.entity)
	}
}

// A missing or malformed entity_id is NULL and must not abort the caller's transaction.
func TestAudit_RegisterExportWithoutAnEntityResolvesToNull(t *testing.T) {
	f := requireFixture(t)
	requireInsertTrigger(t, f)
	fx := seedTriggerFixture(t, f)
	ctx := context.Background()

	key := uuid.NewString()
	if err := db.WithinTenantTx(ctx, f.app, fx.tenant, func(tx pgx.Tx) error {
		for _, payload := range map[string]map[string]any{
			"control": registerExportPayload(fx.entity, "control"),
			"absent":  {"label": "absent", "from": "2026-01-01", "to": "2026-01-31", "row_count": 0},
			"garbage": registerExportPayload("not-a-uuid", "garbage"),
			"null":    registerExportPayload(nil, "null"),
		} {
			if e := audit.Record(ctx, tx, "spelling-fixture", registerExportEvent, payload); e != nil {
				return e
			}
		}
		_, e := tx.Exec(ctx, `INSERT INTO idempotency_keys (tenant_id, key) VALUES ($1, $2)`, fx.tenant, key)
		return e
	}); err != nil {
		t.Fatalf("a malformed or missing entity_id aborted the caller's transaction: %v", err)
	}
	if n := keyCount(t, f.app, fx.tenant, key); n != 1 {
		t.Errorf("idempotency_keys rows after commit = %d, want 1: the transaction did not commit", n)
	}

	got := registerExportEntities(t, f, fx.tenant)
	// Control: the same event with an entity resolves, so the NULLs are not a dead branch.
	assertRegisterExportEntity(t, got, "control", &fx.entity)
	for _, label := range []string{"absent", "garbage", "null"} {
		assertRegisterExportEntity(t, got, label, nil)
	}
}

// The event stays out of firmWideEvents: no client means unattributed, never workspace.
func TestAuditScopeOf_RegisterExportIsNotFirmWide(t *testing.T) {
	id := uuid.NewString()
	if got := audit.ScopeOf(registerExportEvent, &id); got != audit.ScopeCompany {
		t.Errorf("ScopeOf(%s, &id) = %q, want %q", registerExportEvent, got, audit.ScopeCompany)
	}
	if got := audit.ScopeOf(registerExportEvent, nil); got != audit.ScopeUnattributed {
		t.Errorf("ScopeOf(%s, nil) = %q, want %q", registerExportEvent, got, audit.ScopeUnattributed)
	}
}

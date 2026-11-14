// audit_log_entity_for attributes evidence_bundle.exported and .export_failed to the company in
// payload `entity_id`. audit_log is append-only, so a row written without the arm stays unattributed.
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

var evidenceBundleEvents = []string{"evidence_bundle.exported", "evidence_bundle.export_failed"}

func evidenceBundlePayload(entity any, label string) map[string]any {
	return map[string]any{"entity_id": entity, "from": "2026-01-01T00:00:00Z", "to": "2026-01-31T23:59:59Z", "files": 2, "bytes": 10, "label": label}
}

// evidenceBundleEntities maps "<event>|<label>" -> entity_id for the tenant's bundle rows.
func evidenceBundleEntities(t *testing.T, f *fixture, tenant string) map[string]*string {
	t.Helper()
	ctx := context.Background()
	out := map[string]*string{}
	if err := db.WithinTenantTx(ctx, f.app, tenant, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx,
			`SELECT event, payload->>'label', entity_id::text FROM audit_log WHERE tenant_id = $1 AND event = ANY($2)`,
			tenant, evidenceBundleEvents)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var event, label string
			var entity *string
			if err := rows.Scan(&event, &label, &entity); err != nil {
				return err
			}
			out[event+"|"+label] = entity
		}
		return rows.Err()
	}); err != nil {
		t.Fatalf("read back evidence_bundle rows for tenant %s: %v", tenant, err)
	}
	return out
}

func assertEvidenceBundleEntity(t *testing.T, got map[string]*string, event, label string, want *string) {
	t.Helper()
	v, ok := got[event+"|"+label]
	switch {
	case !ok:
		t.Errorf("%s row %q: not read back", event, label)
	case want == nil && v != nil:
		t.Errorf("%s row %q: entity_id = %s, want NULL", event, label, *v)
	case want != nil && v == nil:
		t.Errorf("%s row %q: entity_id IS NULL, want %s", event, label, *want)
	case want != nil && *v != *want:
		t.Errorf("%s row %q: entity_id = %s, want %s", event, label, *v, *want)
	}
}

func TestAudit_InsertTriggerResolvesEvidenceBundleEventsToTheirEntity(t *testing.T) {
	f := requireFixture(t)
	requireInsertTrigger(t, f)
	fx := seedTriggerFixture(t, f)

	// Control: the register arm shares the payload key, so a NULL below is a missing bundle arm.
	recordAudit(t, f, fx.tenant, registerExportEvent, registerExportPayload(fx.entity, "control"))
	for _, event := range evidenceBundleEvents {
		recordAudit(t, f, fx.tenant, event, evidenceBundlePayload(fx.entity, "plain"))
	}

	assertTriggerEntity(t, triggerEntityIDs(t, f, fx.tenant), registerExportEvent, fx.entity)
	got := evidenceBundleEntities(t, f, fx.tenant)
	if len(got) != len(evidenceBundleEvents) {
		t.Fatalf("read back %d bundle rows, want %d", len(got), len(evidenceBundleEvents))
	}
	for _, event := range evidenceBundleEvents {
		assertEvidenceBundleEntity(t, got, event, "plain", &fx.entity)
	}
}

func TestAudit_EvidenceBundleEntityIDSpellingsFollowTheSharedGrammar(t *testing.T) {
	f := requireFixture(t)
	requireInsertTrigger(t, f)
	fx := seedTriggerFixture(t, f)

	spellings := map[string]string{
		"upper":    strings.ToUpper(fx.entity),
		"braced":   "{" + fx.entity + "}",
		"nohyphen": strings.ReplaceAll(fx.entity, "-", ""),
	}
	for _, event := range evidenceBundleEvents {
		for label, spelling := range spellings {
			recordAudit(t, f, fx.tenant, event, evidenceBundlePayload(spelling, label))
		}
	}

	got := evidenceBundleEntities(t, f, fx.tenant)
	if want := len(spellings) * len(evidenceBundleEvents); len(got) != want {
		t.Fatalf("read back %d bundle rows, want %d", len(got), want)
	}
	for _, event := range evidenceBundleEvents {
		for label := range spellings {
			assertEvidenceBundleEntity(t, got, event, label, &fx.entity)
		}
	}
}

// A missing or malformed entity_id is NULL and must not abort the caller's transaction.
func TestAudit_EvidenceBundleWithoutAnEntityResolvesToNull(t *testing.T) {
	f := requireFixture(t)
	requireInsertTrigger(t, f)
	fx := seedTriggerFixture(t, f)
	ctx := context.Background()

	key := uuid.NewString()
	if err := db.WithinTenantTx(ctx, f.app, fx.tenant, func(tx pgx.Tx) error {
		for _, event := range evidenceBundleEvents {
			for _, payload := range []map[string]any{
				evidenceBundlePayload(fx.entity, "control"),
				{"label": "absent", "files": 0, "bytes": 0},
				evidenceBundlePayload("not-a-uuid", "garbage"),
			} {
				if e := audit.Record(ctx, tx, "bundle-fixture", event, payload); e != nil {
					return e
				}
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

	got := evidenceBundleEntities(t, f, fx.tenant)
	for _, event := range evidenceBundleEvents {
		// Control: the same event with an entity resolves, so the NULLs are not a dead arm.
		assertEvidenceBundleEntity(t, got, event, "control", &fx.entity)
		assertEvidenceBundleEntity(t, got, event, "absent", nil)
		assertEvidenceBundleEntity(t, got, event, "garbage", nil)
	}
}

// Not in firmWideEvents: no company means unattributed, never workspace.
func TestAuditScopeOf_EvidenceBundleIsNotFirmWide(t *testing.T) {
	id := uuid.NewString()
	for _, event := range evidenceBundleEvents {
		if got := audit.ScopeOf(event, &id); got != audit.ScopeCompany {
			t.Errorf("ScopeOf(%s, &id) = %q, want %q", event, got, audit.ScopeCompany)
		}
		if got := audit.ScopeOf(event, nil); got != audit.ScopeUnattributed {
			t.Errorf("ScopeOf(%s, nil) = %q, want %q", event, got, audit.ScopeUnattributed)
		}
	}
}

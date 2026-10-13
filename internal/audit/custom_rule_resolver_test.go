// ENGI-14-01: audit_log_entity_for attributes custom_rule.created|updated|deleted to the
// entity in the payload (a direct branch, like portfolio.entity.*). Every case writes through
// audit.Record as invoice_app under a tenant GUC; the resolver runs at BEFORE INSERT and
// audit_log is append-only, so a row written without the branch is unattributed forever.
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

var crEvents = []string{"custom_rule.created", "custom_rule.updated", "custom_rule.deleted"}

func TestAudit_InsertTriggerResolvesCustomRuleEventsToTheirEntity(t *testing.T) {
	f := requireFixture(t)
	requireInsertTrigger(t, f)
	fx := seedTriggerFixture(t, f)

	// Control: a shipped event resolves through this fixture, so a NULL below is a missing branch.
	recordAudit(t, f, fx.tenant, "invoice.created", map[string]any{"id": fx.invoice})
	for _, event := range crEvents {
		recordAudit(t, f, fx.tenant, event,
			map[string]any{"rule_id": uuid.NewString(), "revision": 1, "entity_id": fx.entity})
	}

	got := triggerEntityIDs(t, f, fx.tenant)
	assertTriggerEntity(t, got, "invoice.created", fx.entity)
	for _, event := range crEvents {
		assertTriggerEntity(t, got, event, fx.entity)
	}
}

func TestAudit_FirmWideCustomRuleEventResolvesToNull(t *testing.T) {
	f := requireFixture(t)
	requireInsertTrigger(t, f)
	fx := seedTriggerFixture(t, f)

	recordAudit(t, f, fx.tenant, "custom_rule.created", map[string]any{"rule_id": uuid.NewString(), "entity_id": nil})
	recordAudit(t, f, fx.tenant, "custom_rule.updated", map[string]any{"rule_id": uuid.NewString()})
	// Control: the same event family with an entity resolves, so the NULLs are not a dead branch.
	recordAudit(t, f, fx.tenant, "custom_rule.deleted", map[string]any{"rule_id": uuid.NewString(), "entity_id": fx.entity})

	got := triggerEntityIDs(t, f, fx.tenant)
	assertTriggerEntity(t, got, "custom_rule.deleted", fx.entity)
	assertTriggerEntityNull(t, got, "custom_rule.created")
	assertTriggerEntityNull(t, got, "custom_rule.updated")
}

// The branch is keyed on the event name: an entity_id key on another event stays unattributed.
func TestAudit_CustomRuleBranchLeavesOtherEventsAlone(t *testing.T) {
	f := requireFixture(t)
	requireInsertTrigger(t, f)
	fx := seedTriggerFixture(t, f)

	// Control needle: the new branch is live, or the "as before" assertions below are vacuous.
	recordAudit(t, f, fx.tenant, "custom_rule.created", map[string]any{"rule_id": uuid.NewString(), "entity_id": fx.entity})
	recordAudit(t, f, fx.tenant, "invoice.created", map[string]any{"id": fx.invoice})
	recordAudit(t, f, fx.tenant, "portfolio.entity.created", map[string]any{"id": fx.entity})
	recordAudit(t, f, fx.tenant, "approval_policy.created",
		map[string]any{"policy_id": uuid.NewString(), "entity_id": fx.entity})

	got := triggerEntityIDs(t, f, fx.tenant)
	assertTriggerEntity(t, got, "custom_rule.created", fx.entity)
	assertTriggerEntity(t, got, "invoice.created", fx.entity)
	assertTriggerEntity(t, got, "portfolio.entity.created", fx.entity)
	assertTriggerEntityNull(t, got, "approval_policy.created")
}

// The direct branch shares the resolver's UUID grammar gate: a legal spelling resolves, and
// a malformed entity_id yields NULL without aborting the rule save that wrote the row.
func TestAudit_CustomRuleEntityIDSpellingsFollowTheSharedGrammar(t *testing.T) {
	f := requireFixture(t)
	requireInsertTrigger(t, f)
	fx := seedTriggerFixture(t, f)
	ctx := context.Background()

	key := uuid.NewString()
	if err := db.WithinTenantTx(ctx, f.app, fx.tenant, func(tx pgx.Tx) error {
		for event, id := range map[string]string{
			"custom_rule.created": strings.ToUpper(fx.entity),
			"custom_rule.updated": strings.ReplaceAll(fx.entity, "-", ""),
			"custom_rule.deleted": "not-a-uuid",
		} {
			if e := audit.Record(ctx, tx, "spelling-fixture", event, map[string]any{"entity_id": id}); e != nil {
				return e
			}
		}
		_, e := tx.Exec(ctx, `INSERT INTO idempotency_keys (tenant_id, key) VALUES ($1, $2)`, fx.tenant, key)
		return e
	}); err != nil {
		t.Fatalf("a malformed entity_id aborted the caller's transaction: %v", err)
	}
	if n := keyCount(t, f.app, fx.tenant, key); n != 1 {
		t.Errorf("idempotency_keys rows after commit = %d, want 1: the transaction did not commit", n)
	}

	got := triggerEntityIDs(t, f, fx.tenant)
	assertTriggerEntity(t, got, "custom_rule.created", fx.entity)
	assertTriggerEntity(t, got, "custom_rule.updated", fx.entity)
	assertTriggerEntityNull(t, got, "custom_rule.deleted")
}

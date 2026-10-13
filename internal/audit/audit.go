// Package audit is the 08 Audit context: an in-process module (explicitly NOT a
// network service — locked call 1, 2026-07-03) that every ASComply service calls
// to leave an immutable trail. Record writes one audit_log row
// inside the CALLER'S transaction, so an audit row commits or rolls back atomically with
// the domain change it records — there is no second store to get out of sync with (the
// same in-tx-outbox idea as internal/platform/queue.EnqueueTx). AUDIT-04 added the read
// side alongside it: Query, the facets and the HTTP handler.
//
// audit_log is tenant-scoped under FORCE RLS and append-only (SELECT/INSERT grants only,
// plus an owner-proof trigger); see migrations/20260708062657_audit_log.sql. tenant_id is
// NOT a parameter here: the row defaults it from the app.current_tenant GUC the
// tenant-scoped seam already set on tx, and the table's RLS WITH CHECK refuses any row whose
// tenant diverges — so Record cannot write to the wrong tenant, and a call outside a
// tenant-scoped tx fails closed (NULL tenant_id fails the WITH CHECK, 42501, no row written).
//
// One exception, superuser-only: db.PurgeDemoTenants deletes the seeded demo tenants'
// rows on every gated gateway boot.
package audit

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// Record appends one audit event to audit_log on tx — the caller's tenant-scoped
// transaction (db.WithinTenantTx or db.WithinRequestTenantTx), so the audit row shares
// that transaction's fate. It
// takes only pgx.Tx, so RECORD depends on nothing in internal/platform/db (no import
// cycle). That is a claim about Record, not about the package: store.go imports it for
// WithinRequestTenantTx and handlers.go for ErrNoTenant. The tenant is carried implicitly
// by tx's app.current_tenant GUC and filled by the audit_log.tenant_id DEFAULT.
//
// actor is who performed the action (the HTTP path passes the verified auth.Identity
// Subject; a worker or system path passes a stable label like "system"), kept an explicit
// argument — like db.WithinTenantTx's explicit tenant — so both paths use the one helper.
// event is a stable dotted key (e.g. "portfolio.entity.created"). payload is any
// JSON-serializable value describing the event; a nil payload is stored as the empty
// object {} rather than JSON null.
func Record(ctx context.Context, tx pgx.Tx, actor, event string, payload any) error {
	body := []byte("{}")
	if payload != nil {
		b, err := json.Marshal(payload)
		if err != nil {
			return fmt.Errorf("audit: marshal payload: %w", err)
		}
		body = b
	}
	// id, tenant_id (from the GUC), and created_at are all filled by column defaults, and
	// entity_id by the audit_log_entity_on_insert trigger, which reads `id`/`invoice_id`
	// out of payload for 21 of the event names; the RLS WITH CHECK ties the row to the tx's
	// tenant. payload is passed as a string so pgx sends it to the jsonb column as raw JSON.
	if _, err := tx.Exec(ctx,
		`INSERT INTO audit_log (actor, event, payload) VALUES ($1, $2, $3)`,
		actor, event, string(body)); err != nil {
		return fmt.Errorf("audit: record event %q: %w", event, err)
	}
	return nil
}

// SupportActor is the audit_log.actor of every staff row a customer sees.
const SupportActor = "ASComply support"

// ErrStaffTenantMismatch is returned when a staff event's tenant does not match the transaction's.
var ErrStaffTenantMismatch = errors.New("audit: staff event tenant does not match the transaction's tenant")

// recordTenant is a var so a test can force the customer write to fail.
var recordTenant = Record

// StaffEvent is one staff change. A zero UUID field is stored as NULL; a zero TenantID is a global change.
type StaffEvent struct {
	Actor            uuid.UUID // the staff member; written to staff_audit_log only
	Event            string
	RuleSetVersionID uuid.UUID // rule events only
	TenantID         uuid.UUID // must equal the transaction's tenant; zero when it has none
	SubjectID        uuid.UUID // the tenant-side row the change touched
	Payload          any
}

// RecordStaff appends one staff action to staff_audit_log on tx. A global change (zero TenantID)
// needs a tx with no tenant; a tenant change needs that tenant's tx and also writes the customer's audit_log row.
// A TenantID that differs from the tx's tenant, in either direction, returns ErrStaffTenantMismatch.
func RecordStaff(ctx context.Context, tx pgx.Tx, ev StaffEvent) error {
	body := []byte("{}")
	if ev.Payload != nil {
		b, err := json.Marshal(ev.Payload)
		if err != nil {
			return fmt.Errorf("audit: marshal payload: %w", err)
		}
		body = b
	}
	nullable := func(id uuid.UUID) any {
		if id == uuid.Nil {
			return nil
		}
		return id
	}
	tag, err := tx.Exec(ctx,
		`INSERT INTO public.staff_audit_log (actor, event, rule_set_version_id, tenant_id, subject_id, payload)
SELECT $1::uuid, $2::text, $3::uuid, $4::uuid, $5::uuid, $6::jsonb
 WHERE nullif(current_setting('app.current_tenant', true), '')::uuid IS NOT DISTINCT FROM $4::uuid`,
		ev.Actor, ev.Event, nullable(ev.RuleSetVersionID), nullable(ev.TenantID), nullable(ev.SubjectID), string(body))
	if err != nil {
		return fmt.Errorf("audit: record staff event %q: %w", ev.Event, err)
	}
	if tag.RowsAffected() == 0 {
		return ErrStaffTenantMismatch
	}
	if ev.TenantID != uuid.Nil {
		return recordTenant(ctx, tx, SupportActor, ev.Event, ev.Payload)
	}
	return nil
}

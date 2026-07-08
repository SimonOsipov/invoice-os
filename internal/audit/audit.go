// Package audit is the 08 Audit context: an in-process module (explicitly NOT a
// network service — locked call 1, 2026-07-03) that every FiscalBridge service calls
// to leave an immutable trail. Its single entry point is Record, which writes one
// audit_log row inside the CALLER'S transaction, so an audit row commits or rolls back
// atomically with the domain change it records — there is no second store to get out of
// sync with (the same in-tx-outbox idea as internal/platform/queue.EnqueueTx).
//
// audit_log is tenant-scoped under FORCE RLS and append-only (SELECT/INSERT grants only,
// plus an owner-proof trigger); see migrations/20260708062657_audit_log.sql. tenant_id is
// NOT a parameter here: the row defaults it from the app.current_tenant GUC that
// db.WithinTenantTx already set on tx, and the table's RLS WITH CHECK refuses any row whose
// tenant diverges — so Record cannot write to the wrong tenant, and a call outside a
// tenant-scoped tx fails closed (NULL tenant_id fails the WITH CHECK, 42501, no row written).
package audit

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// Record appends one audit event to audit_log on tx — the caller's tenant-scoped
// transaction (db.WithinTenantTx), so the audit row shares that transaction's fate. It
// takes only pgx.Tx, keeping this package free of any dependency on internal/platform/db
// (no import cycle); the tenant is carried implicitly by tx's app.current_tenant GUC and
// filled by the audit_log.tenant_id DEFAULT.
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
	// id, tenant_id (from the GUC), and created_at are all filled by column defaults; the
	// RLS WITH CHECK ties the row to the tx's tenant. payload is passed as a string so pgx
	// sends it to the jsonb column as raw JSON.
	if _, err := tx.Exec(ctx,
		`INSERT INTO audit_log (actor, event, payload) VALUES ($1, $2, $3)`,
		actor, event, string(body)); err != nil {
		return fmt.Errorf("audit: record event %q: %w", event, err)
	}
	return nil
}

-- M2-10: the 08 Audit context's append-only audit_log table.
--
-- audit_log is the shared, immutable trail every domain mutation writes to via
-- audit.Record(ctx, tx, actor, event, payload) — in the CALLER'S transaction, so an
-- audit row commits or rolls back atomically with the change it records (there is no
-- second store to get out of sync with). Its first consumers are M3-03 (portfolio
-- mutations), M3-04 (rule kill-switch toggle), and M4-02 (invoice state transitions).
--
-- It is the twin of idempotency_keys (M2-08): tenant-scoped, FORCE RLS, append-only. It
-- copies the `tenants` FORCE-RLS template (docs/migrations.md §4) — ENABLE + FORCE RLS +
-- a tenant_isolation policy whose USING doubles as the INSERT WITH CHECK — and, being a
-- permanent/append-only ledger, gets SELECT + INSERT grants only, never UPDATE/DELETE
-- (docs/migrations.md §3, which names audit_log as the canonical append-only example).
--
-- tenant_id DEFAULTs FROM THE GUC: audit.Record is always called inside a
-- db.WithinTenantTx, which has already run `SET LOCAL app.current_tenant`. Defaulting the
-- column from that same GUC means Record needs no tenant argument, a row can never be
-- written for the wrong tenant (the policy's USING doubles as WITH CHECK, so a divergent
-- EXPLICIT tenant_id raises 42501), and a call with NO tenant context defaults tenant_id to
-- NULL, which the same WITH CHECK rejects (42501) — fail-closed, exactly like the read-side
-- "unset GUC → zero rows". The column NOT NULL is a secondary backstop only: a NULL tenant_id
-- can never satisfy the policy, so the RLS check is always what's observed first.
--
-- IMMUTABILITY — grants + an owner-proof trigger, and the honest ceiling:
--   * The SELECT/INSERT-only grant makes the table append-only for invoice_app (an UPDATE
--     or DELETE fails with 42501 before RLS is even consulted).
--   * Grants cannot restrain the table OWNER (invoice_migrator has implicit full DML on
--     objects it owns). The audit_log_append_only() trigger closes that hole: it raises on
--     any UPDATE/DELETE (row-level) or TRUNCATE (statement-level), so even the owner cannot
--     mutate a row through ordinary SQL. `session_replication_role='replica'` (the usual
--     "skip triggers" escape) is SUPERUSER-only and invoice_migrator is NOSUPERUSER, so the
--     owner cannot bypass the trigger that way either.
--   * Residual (stated plainly, not hidden): the owner still OWNS the table, so it can
--     DROP/DISABLE the trigger first — but that is a loud, auditable DDL step, not a silent
--     UPDATE — and a true SUPERUSER bypasses all of this. This is tamper-EVIDENCE, not
--     cryptographic immutability. The M2 exit criterion ("immutable at the DB-grant level")
--     is met by the grant; the trigger is defense-in-depth above it.
--
-- No FK to tenants: matches idempotency_keys — the tenant_id is a bare uuid, keeping the
-- hot audit write path free of a cross-table lock. bigserial id gives the log a natural
-- chronological ordering key (created_at alone is not unique under concurrency); the app
-- needs USAGE on its sequence to INSERT, granted explicitly like river_job_id_seq (§3).

-- +goose Up
CREATE TABLE audit_log (
    id         bigserial   PRIMARY KEY,
    -- Defaults from the tenant context db.WithinTenantTx set on this tx (see header). The
    -- isolation policy below is the WITH CHECK that keeps this honest against explicit inserts.
    tenant_id  uuid        NOT NULL DEFAULT nullif(current_setting('app.current_tenant', true), '')::uuid,
    actor      text        NOT NULL,
    event      text        NOT NULL,
    payload    jsonb       NOT NULL DEFAULT '{}',
    created_at timestamptz NOT NULL DEFAULT now(),
    -- Length checks mirror River's kind/queue conventions: a blank actor/event is a caller
    -- bug, and an unbounded value has no place in an audit key. Rejected at the schema edge.
    CONSTRAINT audit_actor_length CHECK (char_length(actor) > 0 AND char_length(actor) <= 255),
    CONSTRAINT audit_event_length CHECK (char_length(event) > 0 AND char_length(event) < 128)
);

-- Enable AND force: force is what subjects the table owner (the migrator) to the policy
-- below — enable alone would let the owner bypass it (docs/migrations.md §1).
ALTER TABLE audit_log ENABLE ROW LEVEL SECURITY;
ALTER TABLE audit_log FORCE  ROW LEVEL SECURITY;

-- Isolation policy — no TO clause, so it applies to EVERY role. A connection sees (and can
-- insert) an audit row only when app.current_tenant equals its tenant_id; an unset GUC →
-- NULL → no rows. This USING doubles as the INSERT WITH CHECK, so an explicit cross-tenant
-- tenant_id is refused (42501). Identical shape to tenants / idempotency_keys.
CREATE POLICY tenant_isolation ON audit_log
    USING (tenant_id = nullif(current_setting('app.current_tenant', true), '')::uuid);

-- Append-only grants (docs/migrations.md §3): SELECT + INSERT, never UPDATE/DELETE. USAGE on
-- the bigserial sequence is the one sequence the app advances (INSERT), like river_job_id_seq.
GRANT SELECT, INSERT ON audit_log TO invoice_app;
GRANT USAGE ON SEQUENCE audit_log_id_seq TO invoice_app;

-- Owner-proof append-only trigger (see header). One function serves both the row-level
-- UPDATE/DELETE trigger and the statement-level TRUNCATE trigger; it always raises, so it
-- never returns a row. restrict_violation (SQLSTATE 23001) is the code the suite asserts.
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION audit_log_append_only()
    RETURNS trigger
    LANGUAGE plpgsql
    AS $$
BEGIN
    RAISE EXCEPTION 'audit_log is append-only: % is not permitted', TG_OP
        USING ERRCODE = 'restrict_violation';
END;
$$;
-- +goose StatementEnd

CREATE TRIGGER audit_log_no_update_delete
    BEFORE UPDATE OR DELETE ON audit_log
    FOR EACH ROW EXECUTE FUNCTION audit_log_append_only();

CREATE TRIGGER audit_log_no_truncate
    BEFORE TRUNCATE ON audit_log
    FOR EACH STATEMENT EXECUTE FUNCTION audit_log_append_only();

-- +goose Down
-- Dropping the table removes its policy, grants, and triggers with it; the function is a
-- separate object, dropped explicitly. Reset→up round-trips clean (the CI reversibility
-- gate, docs/migrations.md §6).
DROP TABLE audit_log;
DROP FUNCTION audit_log_append_only;

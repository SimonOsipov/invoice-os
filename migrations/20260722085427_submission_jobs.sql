-- M5-01-03: submission_jobs — the mutable per-submission-cycle job record. One row per
-- submission CYCLE, updated in place as it progresses, so the SPA can read a live status
-- without replaying history. A resubmission after a rejection starts a NEW row; the old
-- row is never reset (Core AC-1), which is why the uniqueness guard below is
-- (tenant_id, idempotency_key) and deliberately NOT (tenant_id, invoice_id).
--
-- Born tenant-scoped with the verbatim M2-06 FORCE-RLS `tenant_isolation` template
-- (docs/migrations.md §4) and least-privilege grants (§3), like tenants / audit_log /
-- idempotency_keys / invoices / line_items. The table is OWNED by invoice_migrator —
-- goose runs as the migrator — which is exactly what FORCE ROW LEVEL SECURITY makes
-- load-bearing: without it the owner would bypass the policy, and the owner-path test
-- (TestRLS_SubmissionJobsOwnerInsertRefusedUnderForce) is the only case that notices.
--
-- No `tenant_enumerate` policy: nothing enumerates jobs across tenants (M5-06's
-- reconciliation loops one tenant at a time under WithinTenantTx).
--
-- COMPOSITE FK to invoices — (tenant_id, invoice_id) REFERENCES invoices (tenant_id, id),
-- never a bare invoice_id -> invoices(id). Postgres runs referential-integrity checks with
-- RLS BYPASSED, so a single-column FK would silently accept a cross-tenant invoice: the
-- row's own tenant_id passes the policy's WITH CHECK and nothing else looks. Its target,
-- invoices_tenant_id_id_uq, is the constraint M5-01-02 added for exactly this.
-- ON DELETE RESTRICT matches the invoices -> business_entities disposition (D9): a
-- submitted invoice is a durable fiscal record and must not be destroyed out from under
-- the evidence of its submission.
--
-- UNIQUE (tenant_id, id, invoice_id) is three columns ON PURPOSE and must not be
-- "simplified" to two. It is the FK target for M5-01-04's app_exchange, whose 3-column
-- composite FK then buys three guarantees from one constraint: same-tenant job existence,
-- agreement between the evidence row's denormalised invoice_id and the job's, and
-- (transitively, via this table's own FK) same-tenant invoice existence. It also makes a
-- job's invoice_id un-repointable once evidence rows exist.
--
-- idempotency_key's CHECK bound mirrors the M2-08 ledger's own, char_length > 0 AND <= 255
-- (20260707193000_river_and_idempotency.sql:394). Without the upper bound a job could hold
-- a key that idempotency_keys' CHECK rejects at enqueue time (M5-04) — a failure surfacing
-- far from its cause.
--
-- `state` is the job's OWN vocabulary — queued, submitting, pending, accepted, rejected,
-- failed — in a column named `state`, never `status`. It shares four names with
-- invoices.status but is a different column on a different table and nothing joins them;
-- the distinct column name IS the mitigation. `submitting` (in flight) is not the invoice's
-- `submitted`, and `pending` (the authority said "poll later") has no invoice-side twin.
--
-- river_job_id is a SOFT link with no FK: River prunes its own river_job rows on its own
-- schedule, and invoice_app holds full DML on that table, so a real FK would either
-- cascade-destroy our records or block River's pruning. It is NULL at creation and
-- stitched in after enqueue (M5-04).
--
-- updated_at is TRIGGER-maintained, not writer-set. This is the first app-owned table in
-- this repo with an updated_at (River's vendored tables maintain theirs writer-side via
-- DEFAULT CURRENT_TIMESTAMP and are not ours to imitate). A writer-maintained column is
-- unenforceable — a mutable row whose "last changed" can silently lie defeats the point of
-- a record the SPA reads a LIVE status from. The function is table-specific, not a shared
-- utility, and this migration's Down drops it explicitly.
--
-- No index on next_poll_at: the pending re-poll sweep that would need one does not exist
-- until M5-04/M5-06, and this repo defers indexes to the consumer stories that drive them
-- (internal/invoice/store.go:618-620). No standalone tenant_id index either — both indexes
-- below already lead with tenant_id (D12, 20260714103137_invoices.sql:79-80).

-- +goose Up
CREATE TABLE submission_jobs (
    id              uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id       uuid        NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    invoice_id      uuid        NOT NULL,
    idempotency_key text        NOT NULL CHECK (char_length(idempotency_key) > 0
                                            AND char_length(idempotency_key) <= 255),
    adapter         text        NOT NULL CHECK (char_length(adapter) > 0),
    adapter_version text        NOT NULL CHECK (char_length(adapter_version) > 0),
    state           text        NOT NULL DEFAULT 'queued'
                                CHECK (state IN ('queued', 'submitting', 'pending',
                                                 'accepted', 'rejected', 'failed')),
    attempts        int         NOT NULL DEFAULT 0 CHECK (attempts >= 0),
    next_poll_at    timestamptz,
    last_error      text,
    river_job_id    bigint,
    created_at      timestamptz NOT NULL DEFAULT now(),
    updated_at      timestamptz NOT NULL DEFAULT now(),
    -- FK target for app_exchange's 3-column FK (M5-01-04) — see header.
    CONSTRAINT submission_jobs_tenant_id_invoice_uq UNIQUE (tenant_id, id, invoice_id),
    CONSTRAINT submission_jobs_tenant_invoice_fk
        FOREIGN KEY (tenant_id, invoice_id) REFERENCES invoices (tenant_id, id) ON DELETE RESTRICT
);

-- The idempotency guard (per tenant, not per invoice — resubmission needs a second row).
CREATE UNIQUE INDEX submission_jobs_tenant_idem_uq     ON submission_jobs (tenant_id, idempotency_key);

-- Per-invoice job lookup (the SPA's live status read) + services the RESTRICT-delete check
-- from invoices. Not redundant with the UNIQUE above: (tenant_id, id, invoice_id) does not
-- have (tenant_id, invoice_id) as a prefix.
CREATE        INDEX submission_jobs_tenant_invoice_idx ON submission_jobs (tenant_id, invoice_id);

-- updated_at, maintained by the DB rather than trusted to every writer (see header).
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION submission_jobs_touch_updated_at()
    RETURNS trigger
    LANGUAGE plpgsql
    AS $$
BEGIN
    NEW.updated_at := now();
    RETURN NEW;
END;
$$;
-- +goose StatementEnd

CREATE TRIGGER submission_jobs_set_updated_at
    BEFORE UPDATE ON submission_jobs
    FOR EACH ROW EXECUTE FUNCTION submission_jobs_touch_updated_at();

-- Enable AND force: force is what subjects the table owner (the migrator) to the policy
-- below — enable alone would let the owner bypass it (docs/migrations.md §1).
ALTER TABLE submission_jobs ENABLE ROW LEVEL SECURITY;
ALTER TABLE submission_jobs FORCE  ROW LEVEL SECURITY;

-- Isolation policy — no TO clause, so it applies to EVERY role. A connection sees (and can
-- insert/update) a job only when app.current_tenant equals its tenant_id; an unset GUC →
-- NULL → no rows. This USING doubles as the INSERT/UPDATE WITH CHECK, so both a
-- cross-tenant INSERT and a same-row reassignment to another tenant are refused (42501).
CREATE POLICY tenant_isolation ON submission_jobs
    USING (tenant_id = nullif(current_setting('app.current_tenant', true), '')::uuid);

-- Least-privilege grants (docs/migrations.md §3). No DELETE: the row is mutable state but a
-- permanent record of the attempt, never deleted by the app. Nothing to
-- invoice_tenant_reader, whose only grant repo-wide is tenants/SELECT.
GRANT SELECT, INSERT, UPDATE ON submission_jobs TO invoice_app;

-- +goose Down
-- Dropping the table removes its policy, indexes, constraints, grants and trigger with it;
-- the function is a separate object, dropped explicitly. Reset→up round-trips clean (the
-- CI reversibility gate, docs/migrations.md §6).
DROP TABLE submission_jobs;
DROP FUNCTION submission_jobs_touch_updated_at;

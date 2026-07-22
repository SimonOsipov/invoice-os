-- M5-01-04: app_exchange — the tenant-owned, APPEND-ONLY evidence log of every submission
-- attempt against the APP. One row per attempt, request and response preserved verbatim,
-- INCLUDING attempts that never reached the wire (the rate-limit gate, the already-cleared
-- check, a transform failure). It is a record of ATTEMPTS, not of responses (Core AC-2).
--
-- Born tenant-scoped with the verbatim M2-06 FORCE-RLS `tenant_isolation` template
-- (docs/migrations.md §4) and least-privilege grants (§3), like tenants / audit_log /
-- idempotency_keys / invoices / line_items / invoice_status_history / submission_jobs.
--
-- No `tenant_enumerate` policy: nothing consumes this table across tenants.
--
-- APPEND-ONLY BY GRANT: invoice_app holds SELECT + INSERT and nothing else — no UPDATE, no
-- DELETE. That is the invoice_status_history precedent verbatim
-- (20260714111246_invoice_status_history.sql:16-20, M4-01 disposition D10: grant-enforced,
-- "the idempotency_keys precedent... Deliberately NO owner-proof trigger — that extra
-- hardening belongs to audit_log"), and it is the closest shipped analogue to this table: a
-- tenant-owned, FK-attached, invoice-child evidence log with exactly this grant. The
-- audit_log owner-proof-trigger precedent is deliberately NOT followed: retention on this
-- table is deliberately unanswered for now, so cleanup must remain possible as a considered
-- operational act, which an owner-proof trigger would forbid even to the migrator.
--
-- The HONEST CEILING of grant-only enforcement, stated because a header that implies more
-- than it enforces is worse than none: it does NOT restrain the table OWNER. invoice_migrator
-- holds implicit full DML here, and FORCE ROW LEVEL SECURITY binds it to the tenant
-- PREDICATE, not to whether it may UPDATE or DELETE at all. Operationally the residual is
-- small: with no app.current_tenant set, a migrator `DELETE FROM app_exchange` matches ZERO
-- rows (predicate → NULL → fail-closed), so destroying evidence takes a deliberate,
-- tenant-scoped act rather than an accident.
--
-- THREE-COLUMN FK, not two independent ones:
--   (tenant_id, submission_job_id, invoice_id) → submission_jobs (tenant_id, id, invoice_id)
-- Its target is submission_jobs_tenant_id_invoice_uq, added by M5-01-03 for exactly this. One
-- constraint buys three guarantees: same-tenant job existence; agreement between this row's
-- denormalised invoice_id and the job's; and (transitively, via submission_jobs' own composite
-- FK) same-tenant invoice existence. A job-only plus an invoice-only FK would each be
-- individually satisfied while the two invoice_ids drifted apart. Postgres runs referential
-- integrity with RLS BYPASSED, so a bare submission_job_id → submission_jobs(id) FK would
-- silently accept another tenant's job.
--
-- It is also directional armour, in two distinct ways with two distinct SQLSTATEs:
--   * DELETE of a job that has evidence → 23001 restrict_violation, from the EXPLICIT
--     ON DELETE RESTRICT below (an explicit RESTRICT is checked immediately; an implicit
--     NO ACTION would defer to end-of-statement and raise 23503 instead — the weaker
--     behaviour, which is why the RESTRICT here is spelled out).
--   * UPDATE re-pointing a job's invoice_id once evidence exists → 23503
--     foreign_key_violation, from THIS constraint's implicit ON UPDATE NO ACTION
--     (confupdtype = 'a'). Not from the job's own FK or unique constraint, which that UPDATE
--     leaves satisfied.
--
-- NO `content_type` column. Content-Type is already captured verbatim per side inside
-- request_headers / response_headers (`request_headers ->> 'Content-Type'`); a single scalar
-- column could only ever describe one of the two sides.
--
-- TWO INDEPENDENT boolean flags, never merged into one:
--   * truncated        = a body was size-capped. Incomplete because clipped for size — exactly
--                        Core AC-3's "visible flag when a body was truncated", nothing more.
--   * encoding_coerced = a body's wire bytes could not be stored losslessly as Postgres `text`
--                        (a NUL dropped, invalid UTF-8 replaced). Complete, but altered.
-- A body can be either, both, or neither.
--
-- NO CHECK ties `outcome` to response-column nullability, and NO range CHECK on http_status.
-- A timeout or dropped connection sends the request and never gets a response — the most
-- common real submission failure — and must be recordable, not rejected at write time. The
-- status code is the APP's output, not ours: store-invalid-faithfully.
--
-- NO octet_length CHECK on either body. The 256 KiB cap is enforced in Go by SafeBody
-- (M5-01-05); a schema-level cap would hard-reject an over-size evidence write exactly when
-- something has already gone wrong.
--
-- `adapter` / `adapter_version` mirror submission_jobs' columns and carry only char_length > 0
-- — deliberately no format CHECK.
--
-- No StatementBegin/End and no function: unlike submission_jobs there is nothing mutable here
-- to maintain, so Down is a single DROP TABLE.

-- +goose Up
CREATE TABLE app_exchange (
    id                uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id         uuid        NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    submission_job_id uuid        NOT NULL,
    invoice_id        uuid        NOT NULL,
    operation         text        NOT NULL CHECK (operation IN ('submit','poll')),
    outcome           text        NOT NULL CHECK (outcome IN ('sent','blocked_rate_limit',
                                                  'skipped_already_cleared','transform_failed')),
    attempt           int         NOT NULL CHECK (attempt >= 1),  -- submission_jobs.attempts at write time
    request_body      text,
    request_headers   jsonb       NOT NULL DEFAULT '{}',
    response_body     text,
    response_headers  jsonb       NOT NULL DEFAULT '{}',
    http_status       int,                                        -- no range CHECK: store-invalid-faithfully
    latency_ms        int         CHECK (latency_ms IS NULL OR latency_ms >= 0),
    -- truncated = a body was size-capped. Exactly the PM's [truncation is visible]
    -- meaning: incomplete because clipped for size.
    truncated         boolean     NOT NULL DEFAULT false,
    -- encoding_coerced = a body's wire bytes could not be stored losslessly as
    -- Postgres `text` (NUL dropped / invalid UTF-8 replaced). Complete, but altered.
    -- Independent of truncated: a body can be either, both, or neither.
    encoding_coerced  boolean     NOT NULL DEFAULT false,
    adapter           text        NOT NULL CHECK (char_length(adapter) > 0),
    adapter_version   text        NOT NULL CHECK (char_length(adapter_version) > 0),
    occurred_at       timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT app_exchange_job_fk
        FOREIGN KEY (tenant_id, submission_job_id, invoice_id)
        REFERENCES submission_jobs (tenant_id, id, invoice_id) ON DELETE RESTRICT
);

-- Applied at table birth: SET COMPRESSION only affects rows written after it. Verified in this
-- worktree that postgres:18 (the CI image and the Railway-provisioned major) is built
-- --with-lz4. If a future image ever rejected it, dropping these two lines costs performance,
-- not correctness.
ALTER TABLE app_exchange ALTER COLUMN request_body  SET COMPRESSION lz4;
ALTER TABLE app_exchange ALTER COLUMN response_body SET COMPRESSION lz4;

-- Per-invoice evidence reads (SPA invoice state, archive export) + the tenants ON DELETE
-- CASCADE. No standalone tenant_id index: both indexes below lead with tenant_id.
CREATE INDEX app_exchange_tenant_invoice_idx ON app_exchange (tenant_id, invoice_id);

-- Per-job drill-down (ops console) + services the ON DELETE RESTRICT check on the ONE FK that
-- leaves this table, app_exchange_job_fk.
CREATE INDEX app_exchange_tenant_job_idx     ON app_exchange (tenant_id, submission_job_id);

-- Enable AND force: force is what subjects the table owner (the migrator) to the policy below
-- — enable alone would let the owner bypass it (docs/migrations.md §1).
ALTER TABLE app_exchange ENABLE ROW LEVEL SECURITY;
ALTER TABLE app_exchange FORCE  ROW LEVEL SECURITY;

-- Isolation policy — no TO clause, so it applies to EVERY role. A connection sees (and can
-- insert) an app_exchange row only when app.current_tenant equals its tenant_id; an unset GUC →
-- NULL → no rows. This USING doubles as the INSERT WITH CHECK, so a cross-tenant INSERT is
-- refused (42501).
CREATE POLICY tenant_isolation ON app_exchange
    USING (tenant_id = nullif(current_setting('app.current_tenant', true), '')::uuid);

-- Least-privilege grants (docs/migrations.md §3). APPEND-ONLY: SELECT + INSERT only, no UPDATE
-- and no DELETE (see header). Nothing to invoice_tenant_reader, whose only grant repo-wide is
-- tenants/SELECT.
GRANT SELECT, INSERT ON app_exchange TO invoice_app;   -- append-only by grant

-- +goose Down
-- Dropping the table removes its policy, indexes, constraints and grants with it, so reset→up
-- round-trips clean (the CI reversibility gate, docs/migrations.md §6). Nothing else to drop:
-- unlike submission_jobs this table owns no function.
DROP TABLE app_exchange;

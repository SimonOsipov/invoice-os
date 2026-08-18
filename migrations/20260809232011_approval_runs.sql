-- The three approval run-ledger tables (approval_runs, approval_run_steps,
-- approval_decisions) — the mutable execution trail for one invoice's approval and the
-- permanent record of who decided what.
--
-- Retention is PERMANENT, matching audit_log's posture: no TTL column, no archival
-- table, no purge job, no deletion endpoint. The mechanism is the grant matrix below —
-- invoice_app holds SELECT, INSERT only on approval_decisions (no UPDATE, no DELETE) —
-- not a trigger (see [decisions-are-grant-only-append-only]). The Nigerian FIRS/NRS
-- statutory retention requirement is UNCONFIRMED; tracked as an open legal question,
-- not invented here.
--
-- The four seeded demo tenants are the one exception: db.PurgeDemoTenants deletes their
-- runs, steps and decisions on every gated gateway boot, production included
-- (docs/demo-reset.md).
--
-- approval_runs -> invoices and approval_runs -> approval_policy_versions are both ON
-- DELETE RESTRICT: a durable fiscal record, and the policy version that governed it,
-- must not be destroyed out from under the evidence of its approval. run_steps -> runs
-- and decisions -> runs/run_steps are CASCADE, required not stylistic: tenants ->
-- approval_runs is itself CASCADE, so a RESTRICT one level down would make tenant
-- deletion impossible.
--
-- approval_runs_one_open (tenant_id, invoice_id) WHERE state = 'open' caps a single
-- invoice at one open run at a time.
--
-- RLS is the verbatim M2-06 tenant_isolation template (ENABLE + FORCE, no TO clause, no
-- tenant_enumerate) — nothing in this epic reads approvals cross-tenant.
--
-- These three tables still need adding to internal/platform/db's resetTables, in the
-- SAME TRUNCATE statement as invoices (a later Go-side change, not this file): Postgres
-- refuses to truncate a table referenced by an FK from a table outside the statement,
-- and that check is not bypassed by session_replication_role='replica'.
--
-- No function bodies in this migration; goose's StatementBegin/StatementEnd markers are
-- not needed here (business_entities precedent, 20260709155011_business_entities.sql:25).

-- +goose Up
CREATE TABLE approval_runs (
    id                  uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id           uuid        NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    invoice_id          uuid        NOT NULL,
    policy_version_id   uuid        NOT NULL,
    state               text        NOT NULL DEFAULT 'open'
                                    CHECK (state IN ('open','approved','rejected','cancelled')),
    content_fingerprint text        NOT NULL,
    opened_at           timestamptz NOT NULL DEFAULT now(),
    closed_at           timestamptz,
    closed_by           text,
    created_at          timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT approval_runs_tenant_id_id_uq UNIQUE (tenant_id, id),
    CONSTRAINT approval_runs_tenant_invoice_fk
        FOREIGN KEY (tenant_id, invoice_id)
        REFERENCES invoices (tenant_id, id) ON DELETE RESTRICT,
    CONSTRAINT approval_runs_tenant_version_fk
        FOREIGN KEY (tenant_id, policy_version_id)
        REFERENCES approval_policy_versions (tenant_id, id) ON DELETE RESTRICT
);

CREATE UNIQUE INDEX approval_runs_one_open
    ON approval_runs (tenant_id, invoice_id) WHERE state = 'open';

CREATE TABLE approval_run_steps (
    id                uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id         uuid        NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    run_id            uuid        NOT NULL,
    ord               int         NOT NULL,
    kind              text        NOT NULL
                                  CHECK (kind IN ('approval','condition','notify','autoapprove')),
    workflow_role_key text,
    sla_hours         int,
    due_at            timestamptz,
    state             text        NOT NULL DEFAULT 'pending'
                                  CHECK (state IN ('pending','satisfied','skipped','rejected')),
    satisfied_at      timestamptz,
    satisfied_by      text,
    created_at        timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT approval_run_steps_tenant_id_id_uq UNIQUE (tenant_id, id),
    CONSTRAINT approval_run_steps_tenant_run_fk
        FOREIGN KEY (tenant_id, run_id)
        REFERENCES approval_runs (tenant_id, id) ON DELETE CASCADE
);

CREATE TABLE approval_decisions (
    id          uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id   uuid        NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    run_id      uuid        NOT NULL,
    run_step_id uuid        NOT NULL,
    decision    text        NOT NULL CHECK (decision IN ('approved','rejected')),
    actor       text        NOT NULL CHECK (char_length(actor) > 0),
    reason      text,
    decided_at  timestamptz NOT NULL DEFAULT now(),
    created_at  timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT approval_decisions_tenant_id_id_uq UNIQUE (tenant_id, id),
    CONSTRAINT approval_decisions_tenant_run_fk
        FOREIGN KEY (tenant_id, run_id)
        REFERENCES approval_runs (tenant_id, id) ON DELETE CASCADE,
    CONSTRAINT approval_decisions_tenant_run_step_fk
        FOREIGN KEY (tenant_id, run_step_id)
        REFERENCES approval_run_steps (tenant_id, id) ON DELETE CASCADE
);

ALTER TABLE approval_runs      ENABLE ROW LEVEL SECURITY;
ALTER TABLE approval_runs      FORCE  ROW LEVEL SECURITY;
ALTER TABLE approval_run_steps ENABLE ROW LEVEL SECURITY;
ALTER TABLE approval_run_steps FORCE  ROW LEVEL SECURITY;
ALTER TABLE approval_decisions ENABLE ROW LEVEL SECURITY;
ALTER TABLE approval_decisions FORCE  ROW LEVEL SECURITY;

CREATE POLICY tenant_isolation ON approval_runs
    USING (tenant_id = nullif(current_setting('app.current_tenant', true), '')::uuid);
CREATE POLICY tenant_isolation ON approval_run_steps
    USING (tenant_id = nullif(current_setting('app.current_tenant', true), '')::uuid);
CREATE POLICY tenant_isolation ON approval_decisions
    USING (tenant_id = nullif(current_setting('app.current_tenant', true), '')::uuid);

GRANT SELECT, INSERT, UPDATE ON approval_runs      TO invoice_app;
GRANT SELECT, INSERT, UPDATE ON approval_run_steps TO invoice_app;
GRANT SELECT, INSERT         ON approval_decisions TO invoice_app;
-- invoice_tenant_reader gets nothing on any of the three — nothing enumerates approvals
-- cross-tenant. No TRUNCATE, no REFERENCES, no sequence grants anywhere (every PK is a
-- defaulted uuid). approval_decisions withholds UPDATE and DELETE from invoice_app —
-- this grant IS the AC-9 retention mechanism.

-- +goose Down
DROP TABLE approval_decisions;
DROP TABLE approval_run_steps;
DROP TABLE approval_runs;

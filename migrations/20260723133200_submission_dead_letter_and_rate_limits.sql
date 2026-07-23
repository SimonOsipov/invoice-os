-- M5-04-01: two independent, unrelated changes that both lay schema foundation for the
-- rest of M5-04 (worker + batch submit). Bundled in one migration because both are
-- one-shot DDL with no data to backfill and no shared object between them.
--
-- (1) Widen submission_jobs.state's CHECK from six values to SEVEN, adding
--     `dead_lettered` — Decision [dead-letter-state]: a DEDICATED state rather than
--     reusing `failed`, because M7-04 lists/re-drives jobs BY STATE and M8-08 alerts on
--     DLQ size; a job discarded by River itself (retries exhausted, River gives up) would
--     otherwise be indistinguishable from a real terminal failure the adapter reported.
--     Those two have different operational responses (re-drive a River-abandoned job vs.
--     investigate why the authority rejected it), and telling them apart is the whole
--     reason the state exists as its own value.
--
--     DROP-and-re-ADD under the IDENTICAL constraint name `submission_jobs_state_check`
--     — not a second CHECK alongside it — for the same reason as
--     20260722114935_app_exchange_connection_failed.sql: the name is what PostgreSQL
--     auto-assigned to the inline CHECK in the birth migration
--     (20260722085427_submission_jobs.sql), confirmed against pg_constraint on the live
--     DB, not assumed from the naming convention. Re-adding under the same name keeps
--     exactly one authoritative CHECK on the column rather than two that would need to be
--     intersected by a reader of `\d submission_jobs`.
--
--     THE DOWN IS NARROWING and will fail with SQLSTATE 23514 check_violation if any row
--     already holds `dead_lettered`. That is correct and expected for a value-REMOVING
--     reversal — a CHECK that a live row violates must not be creatable — exactly the
--     same disposition the connection_failed migration documents, not a defect here
--     either. The CI reversibility gate (reset → up from an EMPTY database,
--     docs/migrations.md §6) is unaffected: a fresh database has no such rows, so the
--     narrowing always succeeds there.
--
--     `app_exchange.outcome` needs NO change in this migration — its CHECK already
--     covers all five outcomes M5-04 produces (sent, blocked_rate_limit,
--     skipped_already_cleared, transform_failed, connection_failed; confirmed against
--     pg_constraint). Noted explicitly so a future reader does not "helpfully" widen it
--     again looking for a dead-letter-shaped outcome that was never meant to live there —
--     `dead_lettered` is a JOB state, not an exchange-evidence outcome.
--
-- (2) Create submission_rate_limits, the per-tenant rate-limit ceiling table
--     M5-04-04's in-memory RateLimiter reads from (falling back to an env-var default
--     when no row exists for a tenant) — Decisions [rate-limit] /
--     [rate-limit-table-is-read-only-in-M5-04]: `invoice_app` gets SELECT only, no writer
--     ships in M5-04 (an operator or a test's superuser pool seeds it until M7-04 ships a
--     cockpit). Born tenant-scoped with the verbatim M2-06 FORCE-RLS `tenant_isolation`
--     template (docs/migrations.md §4) and least-privilege grants (§3), like tenants /
--     audit_log / idempotency_keys / submission_jobs.
--
--     Enable AND force: force is what subjects the table owner (the migrator) to the
--     policy below — enable alone would let the owner bypass it, exactly as
--     submission_jobs' own header explains (docs/migrations.md §1).
--
--     `tenant_id` IS the primary key, not a separate id column with a composite unique
--     constraint alongside it — this table holds at most one ceiling row per tenant, so
--     there is no second identity to disambiguate (unlike submission_jobs, where a
--     tenant may have many jobs and (tenant_id, id, invoice_id) is a distinct-purpose FK
--     target). Closest shipped shape is `tenants` itself (id is both PK and the isolation
--     column).
--
--     No `updated_at`/trigger: the table is read-only for invoice_app in M5-04 (no writer
--     exists to mutate a row after it is seeded), so there is nothing yet whose "last
--     changed" would need trigger-maintained tracking — unlike submission_jobs, which IS
--     mutated in place by the app and needs the guarantee a writer-set column cannot give.
--
--     No index beyond the PK: tenant_id IS the PK, so the primary key's own index already
--     serves every lookup this table needs (the limiter reads exactly one row by
--     tenant_id). Unlike submission_jobs/app_exchange, which lead composite indexes with
--     tenant_id because those tables hold MANY rows per tenant, there is no second access
--     pattern here to serve.
--
-- +goose Up
ALTER TABLE submission_jobs DROP CONSTRAINT submission_jobs_state_check;
ALTER TABLE submission_jobs ADD  CONSTRAINT submission_jobs_state_check
    CHECK (state IN ('queued', 'submitting', 'pending', 'accepted', 'rejected', 'failed',
                      'dead_lettered'));

CREATE TABLE submission_rate_limits (
    tenant_id      uuid        PRIMARY KEY REFERENCES tenants(id) ON DELETE CASCADE,
    max_per_minute int         NOT NULL CHECK (max_per_minute > 0),
    created_at     timestamptz NOT NULL DEFAULT now()
);

-- Enable AND force: force is what subjects the table owner (the migrator) to the policy
-- below — enable alone would let the owner bypass it (docs/migrations.md §1).
ALTER TABLE submission_rate_limits ENABLE ROW LEVEL SECURITY;
ALTER TABLE submission_rate_limits FORCE  ROW LEVEL SECURITY;

-- Isolation policy — no TO clause, so it applies to EVERY role, including the table
-- owner under FORCE. A connection sees a row only when app.current_tenant equals its
-- tenant_id; an unset GUC → NULL → no rows.
CREATE POLICY tenant_isolation ON submission_rate_limits
    USING (tenant_id = nullif(current_setting('app.current_tenant', true), '')::uuid);

-- Least-privilege grant (docs/migrations.md §3): SELECT only. No INSERT/UPDATE/DELETE —
-- the table is read-only for invoice_app in M5-04 (Decision
-- [rate-limit-table-is-read-only-in-M5-04]).
GRANT SELECT ON submission_rate_limits TO invoice_app;

-- +goose Down
-- Dropping the table removes its policy, index, constraints and grant with it.
DROP TABLE submission_rate_limits;

-- Narrowing: fails 23514 if any row holds 'dead_lettered' (see header).
ALTER TABLE submission_jobs DROP CONSTRAINT submission_jobs_state_check;
ALTER TABLE submission_jobs ADD  CONSTRAINT submission_jobs_state_check
    CHECK (state IN ('queued', 'submitting', 'pending', 'accepted', 'rejected', 'failed'));

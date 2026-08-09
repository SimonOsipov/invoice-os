// reset.go — PR-environment-only destructive reset (persona-handoff-fix,
// Decision [pr-only-reset]). Truncates the tenant-DATA tables a live PR
// environment accumulates because dev-env.yml's fork model (M4-23-03) gives each
// PR a COPY of the persistent environment's live Postgres volume, not an empty
// one — every E2E run since M3 that ever created a business_entities/invoices
// row left it there permanently. Measured directly against pr-110: the
// workspace switcher offered 90 companies (69 test residue against 21 curated
// active ones) and the tenant held ~3,090 invoices.
//
// THIS DOES NOT REVERSE M4-22-07 ("dropped reset-seed/E2E", dev-env.yml's own
// header). M4-22-07 removed reset-seed from the workflow_dispatch/push path,
// where the target is the PERSISTENT environment (renamed development ->
// production, 2026-07-27) — resetting THAT would destroy live data with no
// fork underneath it to fall back to; see db/reset.dev.sql's history (deleted
// by M4-22-07 — it TRUNCATEd `tenants` unconditionally against whatever the
// workflow happened to be targeting). Reset here is reachable ONLY for a
// Railway PR-environment name (resettableEnvironment below) and is invoked
// from the exact same boot-time seam Seed already runs from (Provision,
// provision.go) — it is a narrowing of what M4-22-07 shut off, not a
// reopening of it.
package db

import (
	"context"
	"fmt"
)

// resettableEnvironment reports whether environment is one the destructive
// PR-env reset (Reset, below) is ever permitted against: a Railway
// PR-environment name (prEnvironmentPattern, bootstrap.go) — and NOTHING ELSE.
//
// This is DELIBERATELY NARROWER than provisionableEnvironment (bootstrap.go),
// which additionally treats "development" as allowed. Two independent reasons,
// not one:
//
//  1. Safety margin, per the brief: `development` was renamed `production` on
//     2026-07-27 (Decision [dev-env-status]); a stale or newly-created
//     environment still carrying the literal name "development" must never be
//     wiped just because it would satisfy the (looser) seed gate.
//  2. It would be actively WRONG even ignoring (1): `provisionableEnvironment`'s
//     "development" branch is what makes Bootstrap/Seed fire inside a REAL PR
//     fork today, because ENVIRONMENT is inherited VERBATIM from the fork's
//     source and resolves to the literal string "development" inside every
//     PR environment (docs/deploy-model.md "ENVIRONMENT is decorative in a
//     fork"; scripts/ci/railway-env.sh's record_environment_variable measures
//     this and deliberately sets nothing, Decision [env-name-is-convention]).
//     If Reset's gate accepted that same "development" branch, it would fire
//     on every PR fork under the SAME string that also matches the persistent
//     environment's own pre-rename name — the one string this function must
//     reject unconditionally. Excluding it here is not merely a narrower
//     allowlist, it is the only choice that still distinguishes a fork from
//     the persistent environment at all.
//
// Consequence of (2): resettableEnvironment must NEVER be fed
// os.Getenv("ENVIRONMENT") the way BootstrapEnabled is — see
// ResetEnabled's doc comment and cmd/gateway/main.go's call site for the
// actually-varying signal (RAILWAY_ENVIRONMENT_NAME) this is fed instead.
func resettableEnvironment(environment string) bool {
	return prEnvironmentPattern.MatchString(environment)
}

// ResetEnabled reports whether the destructive PR-env reset (Reset) is
// permitted: flag == "true" AND environment matches a Railway PR-environment
// name shape, and NOTHING ELSE (resettableEnvironment above). Mirrors
// BootstrapEnabled's shape (bootstrap.go) — ALLOWLIST, not blocklist, same
// rationale — but is the narrower of the two allowlists: every environment
// Reset permits is also one BootstrapEnabled permits (so within one Provision
// call, a Reset is always immediately followed by a Seed — see provision.go),
// never the reverse ("development" satisfies BootstrapEnabled, never
// ResetEnabled).
//
// callers MUST pass os.Getenv("RAILWAY_ENVIRONMENT_NAME") — the Railway-injected
// system variable that reflects the CURRENT environment's real name (docs/
// add-a-service.md: "Railway injects RAILWAY_* variables automatically...
// never set these manually") — NOT os.Getenv("ENVIRONMENT") /
// app.Config.Environment. Unlike ENVIRONMENT (an ordinary app variable that
// forks along with everything else and is therefore identical inside a PR
// environment and its persistent source), RAILWAY_ENVIRONMENT_NAME is exactly
// "pr-<N>" inside a fork and exactly whatever the persistent environment is
// actually named ("production", post-rename) on that environment — it is the
// only variable available at gateway boot that can tell the two apart. See
// resettableEnvironment's doc comment for the full reasoning and
// cmd/gateway/main.go's ProvisionConfig construction for the call site this
// pins.
//
// flag is a SEPARATE env var from BootstrapFlag/GATEWAY_DB_BOOTSTRAP
// (GATEWAY_DB_RESET, ProvisionConfig.ResetFlag) — a deliberate second opt-in
// for a strictly more dangerous operation than seeding, and it also keeps
// every existing Provision-based test (which sets BootstrapFlag but never
// ResetFlag) from silently starting to truncate tables the day this shipped:
// ResetFlag's zero value is "", so ResetEnabled is false wherever a caller
// never mentions it, with no test needing to change.
func ResetEnabled(environment, flag string) bool {
	return flag == "true" && resettableEnvironment(environment)
}

// resetTables is every tenant-DATA table Reset truncates together, in ONE
// TRUNCATE statement — not because ordering matters (TRUNCATE has no per-row
// cascade to order) but because Postgres refuses to truncate a table that is
// still REFERENCED BY a foreign key from a table outside the statement.
// Verified empirically against this exact schema (make dev-db): truncating
// business_entities alone — even under the session_replication_role
// override Reset sets below — fails with "cannot truncate a table referenced
// in a foreign key constraint ... Table \"import_batches\" references
// \"business_entities\"". Every table below either has no FK partner in this
// list or has its FK partner ALSO in this list, so the statement succeeds
// with no CASCADE keyword — CASCADE would additionally sweep in whatever else
// comes to reference one of these tables in the future, with no compile-time
// signal here that it happened.
//
// Included, with reasons:
//
//	invoices                 the tenant's fiscal record; entity_id is ON DELETE
//	                          RESTRICT and rule_set_version_id is unconstrained
//	                          NO ACTION (migrations/20260714103137_invoices.sql).
//	                          NO ACTION only blocks deleting the REFERENCED row
//	                          (rule_set_versions) -- truncating invoices itself,
//	                          the referencing side, is unaffected by it, and
//	                          rule_set_versions is never touched here anyway.
//	line_items                ON DELETE CASCADE off invoices.id; included
//	invoice_status_history     explicitly rather than left to an actual cascade,
//	                          so both empty even if a future migration weakens
//	                          that FK.
//	business_entities         the 90-vs-21 pollution PR-110 measured directly.
//	                          invoices.entity_id (RESTRICT) and
//	                          import_batches.entity_id (CASCADE) both reference
//	                          it, so both must truncate in the SAME statement.
//	import_batches            references business_entities; invoices.
//	                          import_batch_id references IT (ON DELETE SET
//	                          NULL, so invoices alone wouldn't strictly need
//	                          it) -- included for the same
//	                          don't-rely-on-a-specific-ON-DELETE-clause reason
//	                          as line_items/invoice_status_history above.
//	submission_jobs           composite FK (tenant_id, invoice_id) -> invoices,
//	                          ON DELETE RESTRICT. Must truncate alongside
//	                          invoices.
//	app_exchange              composite FK -> submission_jobs, ON DELETE
//	                          RESTRICT. Must truncate alongside submission_jobs.
//	idempotency_keys          tenant-scoped dedupe ledger (M2-08); no FK, but
//	                          genuinely per-run data (job enqueue keys) that
//	                          must not survive into the next PR run's own
//	                          attempts.
//	submission_rate_limits    per-tenant rate-limit ceiling (M5-04-01); no FK
//	                          dependents. invoice_app has SELECT only — no
//	                          write path exists from any live HTTP surface
//	                          (no cockpit until M7-04), so a real PR fork
//	                          cannot accumulate residue here the way it does
//	                          in business_entities/invoices. Included anyway:
//	                          seed.dev.sql has no UPSERT for this table (unlike
//	                          tenants/memberships/rules below), so there is no
//	                          convergence mechanism if a row EVER got written
//	                          by hand against development (e.g. ad hoc manual
//	                          testing) — every later fork would inherit it
//	                          forever otherwise. PK'd to tenant_id (at most one
//	                          row per tenant), so this is cheap and safe to
//	                          include even though the realistic blast radius
//	                          is small.
//	audit_log                 tenant-scoped append-only trail. Its own
//	                          audit_log_append_only() trigger (migrations/
//	                          20260708062657_audit_log.sql) BLOCKS ordinary
//	                          TRUNCATE unconditionally, even for the table
//	                          OWNER -- verified empirically ("audit_log is
//	                          append-only: TRUNCATE is not permitted"). See
//	                          the session_replication_role handling in Reset
//	                          below, which is precisely why this needs a TRUE
//	                          superuser connection and not merely an
//	                          RLS-bypassing role. internal/audit's own
//	                          TestAudit_NoTruncate pins that the table OWNER
//	                          (invoice_migrator) stays blocked -- unaffected by
//	                          this, since Reset never connects as that role.
//	river_job, river_leader,  the async job-queue infrastructure (M2-08,
//	river_queue,              vendored River schema). Cross-tenant, no
//	river_notification        tenant_id, no RLS -- included because a stale or
//	                          completed job from a prior PR run has no
//	                          business outliving the fork, and a leftover
//	                          river_leader row would otherwise contest
//	                          leadership against the fresh fork's own worker.
//	documents                 source-document pointers (DOC-01). Added late:
//	                          the table postdates this list, and its absence
//	                          was a live bug, not a judgement call. A surviving
//	                          row whose audit_log trail was truncated beside it
//	                          loses its uploader forever -- the previewer reads
//	                          "Uploaded by" from the document.created audit row
//	                          alone, so it rendered "Not recorded" for a
//	                          document that HAD a recorded uploader. Worse, the
//	                          (tenant_id, content_hash) dedupe then resolves a
//	                          re-upload of identical bytes to the surviving row
//	                          and logs document.reused, so the attribution can
//	                          never be re-established. Truncated in the same
//	                          statement as its RESTRICT dependents
//	                          (invoices.source_document_id,
//	                          import_batches.document_id), which is what makes
//	                          it legal. Object-storage bytes are NOT deleted:
//	                          they are content-hash keyed and simply re-PUT on
//	                          the next upload of the same file.
//
// Deliberately EXCLUDED, with reasons (do not add on a whim):
//
//	tenants, memberships,     never test residue: no code path in this repo
//	invitations               creates a tenant/membership/invitation at
//	                          runtime (no CreateTenant handler, no INSERT INTO
//	                          tenants outside migrations and db/seed.dev.sql).
//	                          seed.dev.sql UPSERTs (ON CONFLICT DO UPDATE / DO
//	                          NOTHING) the 4 fixture tenants + 4 memberships
//	                          regardless of what Reset does, so leaving them
//	                          untouched and letting Seed reconcile them is
//	                          simpler and avoids having to also enumerate
//	                          every OTHER table that FKs to tenants in this
//	                          same statement.
//	rule_set_versions, rules  migration-owned and SEALED (M4-17
//	                          rules_content_lock / M4-18
//	                          active-implies-sealed): these rows are
//	                          schema-adjacent data a migration created, not
//	                          E2E residue, and rules also carries its own
//	                          BEFORE TRUNCATE trigger (migrations/
//	                          20260717120000_rule_immutability_lock.sql) that
//	                          would need the same bypass as audit_log for no
//	                          benefit -- seed.dev.sql already re-enables any
//	                          rule a prior demo kill-switched via `UPDATE rules
//	                          SET enabled = true ...`, never a drop/recreate.
//	workflow_roles,           per-tenant configuration created only by a tenant
//	workflow_role_members     admin's own CRUD. Nothing seeds them, so truncating
//	                          would unstaff every seat with nothing to restore it.
const resetTables = `TRUNCATE
	invoices, line_items, invoice_status_history, business_entities, import_batches,
	submission_jobs, app_exchange, idempotency_keys, submission_rate_limits, audit_log,
	documents,
	river_job, river_leader, river_queue, river_notification
RESTART IDENTITY`

// Reset destructively empties every table in resetTables, in a single
// transaction, then returns. It is the PR-environment-only counterpart to
// Seed (bootstrap.go): Provision (provision.go) runs it AFTER MigrateUp and
// BEFORE Seed, and ONLY when ResetEnabled(cfg.RailwayEnvironmentName,
// cfg.ResetFlag) is true — see that function's doc comment for the exact
// (narrower-than-Seed, differently-sourced) gate.
//
// Requires a TRUE Postgres superuser connection, for two independent reasons,
// not one: (1) every RLS-bearing table above is FORCE ROW LEVEL SECURITY, so
// a non-bypassing role would need app.current_tenant set per tenant to even
// see the rows, and TRUNCATE has no per-row WHERE clause to scope one tenant
// at a time regardless; (2) audit_log's own audit_log_append_only() trigger
// unconditionally raises on BEFORE TRUNCATE, even for the table's OWNER
// (invoice_migrator) — verified empirically against this exact schema — and
// only session_replication_role bypasses it, a GUC that is itself
// superuser-only (PGC_SUSET). SET LOCAL confines that override to THIS
// transaction only — never process- or session-durable — and it does not
// touch any RLS policy: only trigger firing (both the audit_log guard and
// ordinary referential-integrity enforcement) is suppressed, and only for the
// life of this one transaction. Verified empirically (make dev-db) that the
// multi-table naming requirement in resetTables' doc comment above holds
// independently of this override — Postgres's "cannot truncate a table
// referenced in a foreign key constraint" check is not trigger-based and is
// NOT bypassed by session_replication_role.
//
// Idempotent: TRUNCATE on an already-empty table is a no-op, so re-running
// Reset (e.g. a redeploy of the same PR) is always safe.
func Reset(ctx context.Context, superuserDSN string) error {
	conn, err := connectSuperuser(ctx, superuserDSN)
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close(ctx) }()

	tx, err := conn.Begin(ctx)
	if err != nil {
		return fmt.Errorf("db: reset: begin tx: %w", err)
	}
	// Rolled back on any early return; a no-op after a successful Commit
	// (Rollback on a committed tx returns pgx.ErrTxClosed, discarded here
	// exactly as db.WithinTenantTx already does).
	defer func() { _ = tx.Rollback(ctx) }()

	// SUSET GUC, superuser-only, scoped to THIS transaction by LOCAL — see the
	// audit_log paragraph above for why it is required at all.
	if _, err := tx.Exec(ctx, `SET LOCAL session_replication_role = 'replica'`); err != nil {
		return fmt.Errorf("db: reset: set session_replication_role: %w", err)
	}

	if _, err := tx.Exec(ctx, resetTables); err != nil {
		return fmt.Errorf("db: reset: truncate: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("db: reset: commit: %w", err)
	}
	return nil
}

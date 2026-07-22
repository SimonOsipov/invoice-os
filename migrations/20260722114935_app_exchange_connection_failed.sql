-- M5-01-04 (scope addition): widen app_exchange.outcome to a FIFTH value, `connection_failed`.
--
-- A PRE-CONNECTION failure — DNS resolution failure, TLS handshake failure, connection refused
-- — fits none of the original four values. Folding it into `sent` stretches that value to mean
-- "attempted" and destroys the evidence log's ability to distinguish "we reached the APP and
-- heard nothing back" from "we never reached the APP at all". Those two have different
-- operational responses (poll for a status the APP may already hold vs. check the network path
-- or the endpoint), and telling them apart is the whole reason the log exists. The resulting
-- boundary, stated once so it need not be re-derived at the call site:
--
--   * sent              — bytes LEFT OUR PROCESS toward the APP. Includes a timeout or a
--                         dropped connection AFTER transmission.
--   * connection_failed — we never got far enough to transmit; NO request reached the wire.
--
-- This is cheap NOW precisely because NO WRITER EXISTS YET. M5-04 is the only thing that will
-- ever set `outcome`, and it has not been built — so there is no data to backfill, no code path
-- to re-classify, and no deployed row that could hold a value this migration would have to
-- reconcile. The same change after M5-04 ships would be a data migration, not a DDL one-liner.
--
-- A NEW MIGRATION, NOT AN EDIT OF 20260722093218_app_exchange.sql. Verified, not stylistic: the
-- `pr-<n>` ephemeral Railway environment's Postgres PERSISTS across deploys — its volume is
-- created once at environment birth and never recreated (scripts/ci/railway-env.sh:1288-1296
-- returns early when a volume instance already exists), and the environment is destroyed only
-- at PR close. `goose_db_version` records (id, version_id, is_applied, tstamp) with NO checksum
-- or content-hash column, and 20260722093218 is already recorded applied there. Editing that
-- file in place would therefore be SILENTLY SKIPPED at the next gateway boot, leaving the
-- deployed environment on the stale four-valued CHECK while CI stayed green — the `rls` job
-- runs against a fresh service container where an edited file applies from scratch, so it
-- could never see the drift. A new migration is the only correct option.
--
-- DROP-and-re-ADD rather than a new constraint alongside: `app_exchange_outcome_check` is the
-- name PostgreSQL auto-assigned to the inline unnamed CHECK in the birth migration (confirmed
-- against pg_constraint on the live DB, not assumed from the naming convention). Re-adding
-- under the same name keeps exactly one outcome CHECK on the table, so a reader of
-- `\d app_exchange` sees one authoritative value set rather than two that must be intersected.
--
-- THE DOWN IS NARROWING and will fail with SQLSTATE 23514 check_violation if ANY row already
-- holds `connection_failed`. That is correct and expected for a value-REMOVING reversal — a
-- CHECK that a live row violates must not be creatable — not a defect, but the next reader
-- should not be surprised by it. The CI reversibility gate (reset → up from an EMPTY database,
-- docs/migrations.md §6) is unaffected: a fresh database has no such rows, so the narrowing
-- always succeeds there.
--
-- Deliberately NOT added: any CHECK tying `outcome` to response-column nullability. The
-- accompanying test asserts request_body IS NULL for a connection_failed row, but that
-- DESCRIBES the scenario; it is not a schema constraint. Such a CHECK would contradict the
-- birth migration's stated invariant ("NO CHECK ties `outcome` to response-column
-- nullability") and would satisfy the test for the wrong reason.

-- +goose Up
ALTER TABLE app_exchange DROP CONSTRAINT app_exchange_outcome_check;
ALTER TABLE app_exchange ADD  CONSTRAINT app_exchange_outcome_check
    CHECK (outcome IN ('sent','blocked_rate_limit','skipped_already_cleared',
                       'transform_failed','connection_failed'));

-- +goose Down
-- Narrowing: fails 23514 if any row holds 'connection_failed' (see header).
ALTER TABLE app_exchange DROP CONSTRAINT app_exchange_outcome_check;
ALTER TABLE app_exchange ADD  CONSTRAINT app_exchange_outcome_check
    CHECK (outcome IN ('sent','blocked_rate_limit','skipped_already_cleared',
                       'transform_failed'));

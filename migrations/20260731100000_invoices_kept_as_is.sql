-- +goose Up
-- INVCR-01-15 (D6, task-291): `Keep as-is` -- auditable triage, NOT a state-machine
-- change. legalTransitions (store.go:900-906), the invoices.status CHECK
-- (20260714103137_invoices.sql:47-49) and the invoice_status_history CHECKs are
-- byte-untouched by this migration -- D10 is dropped (no new edges, no widened CHECK,
-- 20260717193449_active_seal_invariant.sql not touched).
--
-- Three nullable columns, not one jsonb blob ([keep-as-is-three-columns]): D6 says
-- "records who / when / why" -- three typed facts, matching this codebase's
-- typed-column convention rather than an unqueryable blob.
ALTER TABLE invoices
  ADD COLUMN kept_as_is_at     timestamptz,
  ADD COLUMN kept_as_is_by     text,
  ADD COLUMN kept_as_is_reason text;

-- All three present or all three absent: a suppressed error must be attributable
-- (who/when/why), so a partial write is refused rather than silently accepted.
ALTER TABLE invoices ADD CONSTRAINT invoices_kept_as_is_complete CHECK (
  (kept_as_is_at IS NULL AND kept_as_is_by IS NULL AND kept_as_is_reason IS NULL)
  OR (kept_as_is_at IS NOT NULL AND kept_as_is_by IS NOT NULL AND kept_as_is_reason IS NOT NULL));

-- D6's "it stays a draft and can never be sent", enforced by the DB rather than by
-- discipline. Because draft has exactly ONE outgoing edge (draft->validated, reachable
-- only through Store.ApplyValidation), this CHECK also FORCES ApplyValidation to null
-- the three columns in the very same UPDATE that promotes -- a forgotten clear is a
-- 23514 (loud CI red), never a silently stale badge.
ALTER TABLE invoices ADD CONSTRAINT invoices_kept_as_is_draft_only CHECK (
  kept_as_is_at IS NULL OR status = 'draft');

-- No GRANT needed: invoices is already `GRANT SELECT, INSERT, UPDATE ... TO
-- invoice_app` (20260714103137_invoices.sql:95), a table-level grant that already
-- covers these new columns.

-- +goose Down
-- Both constraints are dropped before their columns (Postgres would refuse the column
-- drop otherwise); order also matches [keep-as-is-three-columns]'s "new constraints on
-- new columns, dropped cleanly" framing.
ALTER TABLE invoices DROP CONSTRAINT invoices_kept_as_is_draft_only;
ALTER TABLE invoices DROP CONSTRAINT invoices_kept_as_is_complete;
ALTER TABLE invoices
  DROP COLUMN kept_as_is_at,
  DROP COLUMN kept_as_is_by,
  DROP COLUMN kept_as_is_reason;

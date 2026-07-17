-- +goose Up
-- M4-18: close the gap M4-17 left open. M4-17's Guard A only locks the rules of a
-- SEALED parent -- nothing stops an UNSEALED version from being made active, and an
-- active-but-unsealed version is exactly the mutable production rule-set M4-17 set out
-- to forbid. This adds a DB-level `active ⟹ sealed` invariant so a migration that
-- activates an unsealed version (or inserts a new active-unsealed one) fails loudly in
-- CI, instead of silently shipping a mutable active rule-set. Migrator-only hardening
-- (invoice_app has no INSERT / UPDATE(is_active) grant on rule_set_versions) -- no
-- runtime exposure, same as M4-17's guards.
--
-- `NOT is_active OR sealed` is exactly `active ⟹ sealed` (both columns boolean NOT
-- NULL, no three-valued-logic edge): is_active=false always passes; is_active=true
-- requires sealed=true.
--
-- No backfill: at HEAD, v1 (is_active=false, sealed=true) and v2 (is_active=true,
-- sealed=true) already satisfy the constraint. Plain ADD CONSTRAINT (not NOT VALID +
-- VALIDATE) is the right choice -- a 2-row global reference table makes the validating
-- scan trivial.
--
-- Reversibility ordering: this is the NEWEST migration, so on `goose reset` (downs,
-- newest->oldest) its Down runs FIRST -- dropping this CHECK before M4-17's, rule_set_v2's,
-- or line_rules' Downs run, so none of their is_active/DELETE statements ever meet it.
ALTER TABLE rule_set_versions
    ADD CONSTRAINT rule_set_versions_active_is_sealed
    CHECK (NOT is_active OR sealed);

-- +goose Down
ALTER TABLE rule_set_versions
    DROP CONSTRAINT rule_set_versions_active_is_sealed;

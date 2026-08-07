-- +goose Up
-- Publish rule-set v4: v3's 19 rules SELECT-copied verbatim, plus buyer-tin-required.
--
-- Statement order is forced and must not be rearranged:
--   * rule_set_versions_one_active is a plain (non-deferrable) partial unique index, so
--     v3 must be deactivated in its OWN statement before v4 claims the slot, else 23505.
--   * rule_set_versions_active_is_sealed is a per-row CHECK, so v4 is sealed and
--     activated in ONE statement, else 23514 on the instant it is active-but-unsealed.
--   * rules_content_lock permits child INSERTs only under an unsealed parent, so the
--     rules land before the seal.

INSERT INTO rule_set_versions (version, is_active, sealed, notes)
VALUES (4, false, false, 'MBS global rule-set v4 (BUG-05: v3''s 19 rules + buyer-tin-required)');

-- SELECT-copy, never a hand-retyped literal, so v4 cannot silently diverge from v3's
-- real content. enabled is forced true: a kill-switch flip is a runtime decision taken
-- against v3, not part of what a freshly published version ships as.
INSERT INTO rules
    (rule_set_version_id, key, type, target, params, severity, "when", message, scope, enabled)
SELECT v4.id, r.key, r.type, r.target, r.params, r.severity, r."when", r.message, r.scope, true
FROM rules r
JOIN rule_set_versions v3 ON v3.id = r.rule_set_version_id AND v3.version = 3
CROSS JOIN rule_set_versions v4
WHERE v4.version = 4;

-- The 20th rule, mirroring supplier-tin-required's shape. params={} leaves AllowBlank
-- false, so a whitespace-only TIN counts as missing (requiredEval, evaluators.go).
INSERT INTO rules
    (rule_set_version_id, key, type, target, params, severity, "when", message, scope, enabled)
SELECT v4.id, 'buyer-tin-required', 'required', 'buyer.tin', '{}'::jsonb, 'error', NULL,
       'Buyer TIN is required.', 'document', true
FROM rule_set_versions v4
WHERE v4.version = 4;

UPDATE rule_set_versions SET is_active = false WHERE version = 3;

UPDATE rule_set_versions SET sealed = true, is_active = true WHERE version = 4;

-- +goose Down
-- v4 is sealed by the time this runs, and both owner-proof guards are still installed
-- (goose runs Downs newest-first, and this migration is newer than the lock). DISABLE
-- them for the duration rather than DROP+CREATE, which would duplicate the guard bodies.
-- The active⟹sealed CHECK is a real CHECK, not a trigger, so DISABLE TRIGGER does not
-- touch it -- it holds trivially below, since is_active=false satisfies it.
--
-- [v2-down-is-dev-irreversible], carried over: invoices.rule_set_version_id has no
-- ON DELETE clause, so once any invoice stamps v4 the DELETE raises 23503. CI's
-- reversibility gate runs against a fresh, invoice-less Postgres.

ALTER TABLE rules              DISABLE TRIGGER rules_content_lock;
ALTER TABLE rule_set_versions  DISABLE TRIGGER rule_set_versions_seal_guard;

UPDATE rule_set_versions SET is_active = false, sealed = false WHERE version = 4;

UPDATE rule_set_versions SET is_active = true WHERE version = 3;

-- v4's rules cascade via rules.rule_set_version_id ON DELETE CASCADE.
DELETE FROM rule_set_versions WHERE version = 4;

ALTER TABLE rule_set_versions  ENABLE TRIGGER rule_set_versions_seal_guard;
ALTER TABLE rules              ENABLE TRIGGER rules_content_lock;

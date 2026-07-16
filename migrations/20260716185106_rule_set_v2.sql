-- +goose Up
-- M4-04-01: restore v1's immutability by REVERTING the content mutation
-- 20260715120000_line_rules.sql made to it, and re-issuing those two rules as
-- a NEW, published version 2 instead.
--
-- WHY: M3-04's guarantee is that a published rule-set version is immutable --
-- an invoice stamped "v1" must mean one, knowable rule-set forever. line_rules
-- broke that by INSERTing two rules into the already-published, already-active
-- v1, so pre- and post-line_rules "v1" invoices are indistinguishable despite
-- carrying different rule sets. M4-04 is the FIRST writer of the rule-set
-- version stamp (invoices.rule_set_version_id), so this is the last moment the
-- mutation is free to reverse: once an invoice references a version, that
-- version's content is pinned permanently. Hence this is M4-04's first subtask.
--
-- End state: v1 = its original 17 M3-05 base rules, is_active=false (pristine
-- and frozen, exactly as M3-05 published it). v2 = those 17 + the 2 line-item
-- rules = 19, is_active=true.
--
-- STATEMENT ORDER IS FORCED BY THE SCHEMA ([v2-flip-order]):
-- rule_set_versions_one_active is a partial unique index on ((is_active))
-- WHERE is_active (20260711051711_rule_set_versions.sql:15), permitting exactly
-- one active row. It is a plain, non-deferrable btree index, so it is enforced
-- per-statement rather than at COMMIT: inserting v2 active BEFORE clearing v1
-- raises 23505 on deploy. Deactivate first, then insert.
--
-- Runs as the migrator role, which OWNS both tables. invoice_app holds only
-- SELECT on rule_set_versions and SELECT + UPDATE(enabled) on rules, so no
-- app-role INSERT grant is needed or wanted here (the kill-switch remains the
-- app's only sanctioned rules mutation).

-- 1. Un-mutate v1: remove the two rules line_rules wrongly added to it. The
--    rules.type CHECK's 'line_sum' widening (line_rules' own schema change)
--    STAYS -- v2 needs it, and widening a CHECK is a schema change, not a
--    mutation of v1's content.
DELETE FROM rules
 WHERE key IN ('line-cost-non-negative', 'line-items-sum-subtotal')
   AND rule_set_version_id IN (SELECT id FROM rule_set_versions WHERE version = 1);

-- 2. Clear the single active slot before v2 claims it ([v2-flip-order]).
UPDATE rule_set_versions SET is_active = false WHERE version = 1;

-- 3. Publish v2 as the new active version.
INSERT INTO rule_set_versions (version, is_active, notes)
VALUES (2, true, 'MBS global rule-set v2 (M4-04-01: v1''s 17 base rules + the 2 line-item rules)');

-- 4. Copy v1's 17 base rules into v2 verbatim ([v2-copy-not-redeclare]): a copy
--    guarantees v2 is a byte-for-byte superset of v1, where re-declaring 17
--    rows of SQL would invite a typo that silently changes a rule's meaning.
--    enabled is forced to true rather than inherited ([v2-ships-as-authored]):
--    a kill-switch flip is a RUNTIME decision taken against v1, while a newly
--    published version ships as authored -- inheriting would make this
--    migration's outcome depend on runtime state, so a fresh CI DB and a
--    toggled dev DB would diverge (and the reversibility round-trip with them).
INSERT INTO rules
    (rule_set_version_id, key, type, target, params, severity, "when", message, scope, enabled)
SELECT v2.id, r.key, r.type, r.target, r.params, r.severity, r."when", r.message, r.scope, true
FROM rules r
JOIN rule_set_versions v1 ON v1.id = r.rule_set_version_id AND v1.version = 1
CROSS JOIN rule_set_versions v2
WHERE v2.version = 2;

-- 5. Re-issue the two line-item rules under v2 -- params/type/message verbatim
--    from 20260715120000_line_rules.sql, so they behave identically to today.
INSERT INTO rules
    (rule_set_version_id, key, type, target, params, severity, "when", message, scope, enabled)
SELECT v.id, r.key, r.type, r.target, r.params::jsonb, r.severity, NULL, r.message, 'document', true
FROM rule_set_versions v
CROSS JOIN (VALUES
    ('line-cost-non-negative', 'cel', '',
       '{"expr":"!has(invoice.line_items) || invoice.line_items.all(x, !has(x.unit_price) || type(x.unit_price) != double || x.unit_price >= 0.0)"}',
       'error', 'Line item cost must be zero or positive.'),
    ('line-items-sum-subtotal', 'line_sum', '',
       '{"items":"line_items","amount":"unit_price","quantity":"quantity","expected":"subtotal","tolerance":0.005}',
       'error', 'Line item amounts must sum to the invoice subtotal.')
) AS r(key, type, target, params, severity, message)
WHERE v.version = 2;

-- +goose Down
-- Restores the exact pre-migration state: v1 active carrying all 19 rules, v2
-- absent. Symmetric to the Up, and constrained by the same single-active index
-- -- v2's row must go before v1 can reclaim the active slot.
--
-- ORDERING NOTE (the CI reversibility gate, docs/migrations.md section 6):
-- under `goose reset` downs run newest->oldest, so THIS Down runs BEFORE
-- 20260715120000_line_rules.sql's. That ordering is load-bearing: ours
-- re-inserts a 'line_sum' row while line_rules' widened rules_type_check is
-- still in place, and line_rules' own Down then deletes those rows BEFORE
-- narrowing the CHECK back. Reversing the two would fail the narrowed CHECK.
--
-- [v2-down-is-dev-irreversible]: invoices.rule_set_version_id ->
-- rule_set_versions(id) has NO ON DELETE (NO ACTION), so once any invoice
-- stamps v2 this DELETE raises 23503. That is correct and harmless: the shared
-- dev DB is forward-only/additive by policy (docs/migrations.md section 7) and
-- CI's reversibility gate runs on a fresh, invoice-less Postgres (section 6).

-- 1. Drop v2 (its 19 rules cascade via rules.rule_set_version_id ON DELETE
--    CASCADE), which also clears the active slot.
DELETE FROM rule_set_versions WHERE version = 2;

-- 2. v1 reclaims the active slot.
UPDATE rule_set_versions SET is_active = true WHERE version = 1;

-- 3. Put the two line-item rules back under v1 -- restoring the exact (mutated)
--    pre-migration state line_rules left behind, so its own Down finds them
--    where it expects.
INSERT INTO rules
    (rule_set_version_id, key, type, target, params, severity, "when", message, scope, enabled)
SELECT v.id, r.key, r.type, r.target, r.params::jsonb, r.severity, NULL, r.message, 'document', true
FROM rule_set_versions v
CROSS JOIN (VALUES
    ('line-cost-non-negative', 'cel', '',
       '{"expr":"!has(invoice.line_items) || invoice.line_items.all(x, !has(x.unit_price) || type(x.unit_price) != double || x.unit_price >= 0.0)"}',
       'error', 'Line item cost must be zero or positive.'),
    ('line-items-sum-subtotal', 'line_sum', '',
       '{"items":"line_items","amount":"unit_price","quantity":"quantity","expected":"subtotal","tolerance":0.005}',
       'error', 'Line item amounts must sum to the invoice subtotal.')
) AS r(key, type, target, params, severity, message)
WHERE v.version = 1;

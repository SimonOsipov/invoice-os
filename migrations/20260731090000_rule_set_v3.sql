-- +goose Up
-- INVCR-01-13 (D8): publish rule-set v3 -- v2's 19 rules SELECT-copied verbatim, with
-- `target` overridden on exactly the 4 keys that ship with a blank target today, so
-- every rule declares its own field and the inline fix editor (§10.4) needs NO
-- client-side rule-key map:
--   vat-standard-rate       -> vat
--   line-items-sum-subtotal -> subtotal
--   line-cost-non-negative  -> line_items
--   no-duplicate-line-items -> line_items
--
-- EVALUATION-NEUTRAL BY CONSTRUCTION: taxMathEval.Eval, lineSumEval.Eval, and
-- celEvaluator.Eval (internal/validation/evaluators_math.go, cel.go) never read
-- r.Target -- only the shared violation(r) helper (evaluators.go) copies r.Target into
-- Violation.Path. So the only observable effect of this migration is that a fired
-- violation for one of the 4 keys now carries a non-empty Path where it previously
-- carried "" (omitted on the wire, `json:"path,omitempty"`); no rule's severity,
-- message, params, or enabled changes, and the SET of rules that fire for any given
-- payload cannot change -- proven by internal/validation/rule_set_v3_test.go's
-- TestV3PathIsTheOnlyDelta (golden corpus, v2 vs v3) and
-- internal/validation/evaluators_math_test.go's
-- TestTaxMathTargetDoesNotChangeEvaluation.
--
-- v2 is NOT mutated (M4-17's Guard A would reject it anyway -- v2 is sealed): this is a
-- purely additive publish, the exact shape 20260716185106_rule_set_v2.sql established --
-- insert unsealed/inactive, insert its rules, deactivate the outgoing active version,
-- then seal+activate the new one in one statement.
--
-- STATEMENT ORDER IS FORCED TWICE OVER, now that both M4-17 and M4-18 are live
-- ([v2-flip-order], generalized):
--   1. rule_set_versions_one_active (the partial unique index,
--      20260711051711_rule_set_versions.sql:15) is a plain, non-deferrable index --
--      enforced PER STATEMENT. v2 must be deactivated in its own statement BEFORE v3
--      claims the slot, or the INSERT/UPDATE that tries to make v3 active raises 23505.
--   2. rule_set_versions_active_is_sealed (20260717193449_active_seal_invariant.sql,
--      `NOT is_active OR sealed`) is a per-row CHECK. Activating v3 before sealing it
--      (two separate statements) would 23514 on the activate step, since that row would
--      be active=true, sealed=false for the instant between them. Hence the seal and the
--      activate happen in ONE UPDATE, exactly mirroring the M4-18 story's own
--      seal-then-activate publish flow (schema_test.go's sealAndActivate).
--
-- Runs as invoice_migrator (owns both tables, per db/bootstrap.sql / docs/migrations.md
-- §1); v2's own `INSERT INTO rules ... SELECT ... FROM rules r JOIN rule_set_versions`
-- pattern is reused verbatim ([v2-copy-not-redeclare]) so this can never silently diverge
-- from v2's real content via a hand-retyped literal -- the CASE expression below touches
-- ONLY `target`; every other selected column is v2's own row, untouched.

-- 1. Publish v3 as a draft: unsealed, inactive. Guard A (rules_content_lock,
--    20260717120000_rule_immutability_lock.sql) permits INSERT under an unsealed parent.
INSERT INTO rule_set_versions (version, is_active, sealed, notes)
VALUES (3, false, false, 'MBS global rule-set v3 (INVCR-01-13, D8: v2''s 19 rules, target filled on 4 keys)');

-- 2. Copy v2's 19 rules into v3 verbatim ([v2-copy-not-redeclare]), overriding `target`
--    on exactly the 4 D8 keys -- every other column (type, params, severity, "when",
--    message, scope) copied byte-for-byte from v2's row. enabled is forced to true
--    rather than inherited ([v2-ships-as-authored], same rationale as v2's own publish:
--    a kill-switch flip is a runtime decision taken against v2, not part of what a
--    freshly published version ships as -- inheriting would make this migration's
--    outcome depend on runtime state).
INSERT INTO rules
    (rule_set_version_id, key, type, target, params, severity, "when", message, scope, enabled)
SELECT v3.id, r.key, r.type,
       CASE r.key
           WHEN 'vat-standard-rate'       THEN 'vat'
           WHEN 'line-items-sum-subtotal' THEN 'subtotal'
           WHEN 'line-cost-non-negative'  THEN 'line_items'
           WHEN 'no-duplicate-line-items' THEN 'line_items'
           ELSE r.target
       END,
       r.params, r.severity, r."when", r.message, r.scope, true
FROM rules r
JOIN rule_set_versions v2 ON v2.id = r.rule_set_version_id AND v2.version = 2
CROSS JOIN rule_set_versions v3
WHERE v3.version = 3;

-- 3. Clear the single active slot before v3 claims it ([v2-flip-order]).
UPDATE rule_set_versions SET is_active = false WHERE version = 2;

-- 4. Seal AND activate v3 in ONE statement -- see the active⟹sealed CHECK note above.
UPDATE rule_set_versions SET sealed = true, is_active = true WHERE version = 3;

-- +goose Down
-- v3 is SEALED the instant the Up above completes, and a sealed version can be neither
-- unsealed nor deleted (rule_set_versions_seal_guard / rules_content_lock,
-- 20260717120000_rule_immutability_lock.sql, Guards A/C) -- both guards are still
-- INSTALLED when this Down runs, because under `goose reset` Downs run newest-first and
-- this migration is newer than the lock migration. So this Down must DISABLE (never
-- DROP+CREATE -- that would duplicate the guard bodies in a second place, exactly the
-- drift the lock exists to prevent) both owner-proof triggers for the duration of the
-- unseal + deactivate + reactivate + delete sequence, then re-ENABLE them.
-- DISABLE/ENABLE TRIGGER is an owner privilege, and migrations run as invoice_migrator,
-- which owns both tables ([v3-down-disables-guards]).
--
-- [v2-down-is-dev-irreversible], carried over verbatim for v3: invoices.rule_set_version_id
-- -> rule_set_versions(id) has NO ON DELETE clause (NO ACTION), so once any invoice
-- stamps v3 the DELETE below raises 23503. That is correct and harmless: the shared dev
-- Postgres is forward-only/additive by policy (docs/migrations.md §7) and CI's
-- reversibility gate (§6) runs the reset->up round-trip against a fresh, invoice-less
-- Postgres, where this Down always succeeds cleanly.

ALTER TABLE rules              DISABLE TRIGGER rules_content_lock;
ALTER TABLE rule_set_versions  DISABLE TRIGGER rule_set_versions_seal_guard;

-- Unseal + deactivate v3 in one statement -- both guards are disabled, so ordering
-- between these two columns doesn't matter here; the active_is_sealed CHECK (a real
-- CHECK constraint, NOT a trigger -- DISABLE TRIGGER has no effect on it) still holds
-- trivially, since is_active=false always satisfies `NOT is_active OR sealed`.
UPDATE rule_set_versions SET is_active = false, sealed = false WHERE version = 3;

-- Reactivate v2 (still sealed=true, untouched by the Up). v3 is now the only inactive
-- row that was active, so this is the sole remaining active=true row -- the one-active
-- partial unique index (also not trigger-based, but satisfied by construction here)
-- never sees two active rows at once.
UPDATE rule_set_versions SET is_active = true WHERE version = 2;

-- Remove v3 (its rules cascade via rules.rule_set_version_id ON DELETE CASCADE, which
-- would otherwise re-trigger Guard A's DELETE branch -- already disabled above).
DELETE FROM rule_set_versions WHERE version = 3;

ALTER TABLE rule_set_versions  ENABLE TRIGGER rule_set_versions_seal_guard;
ALTER TABLE rules              ENABLE TRIGGER rules_content_lock;

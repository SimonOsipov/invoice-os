-- +goose Up
-- Seed two line-item rules into the active MBS v1 rule-set: a per-line
-- non-negative cost check (cel) and a lines-reconcile-to-subtotal check
-- (line_sum, a new aggregate evaluator type). The second requires widening
-- the rules.type CHECK to admit 'line_sum' before the INSERT.
ALTER TABLE rules DROP CONSTRAINT rules_type_check;
ALTER TABLE rules ADD CONSTRAINT rules_type_check CHECK (type IN
    ('required','format/regex','enum','range','tax_math',
     'cross_field','conditional','date','cel','line_sum'));

INSERT INTO rules
    (rule_set_version_id, key, type, target, params, severity, "when", message, scope, enabled)
SELECT v.id, r.key, r.type, r.target, r.params::jsonb, r.severity, NULL, r.message, 'document', true
FROM rule_set_versions v
CROSS JOIN (VALUES
    -- Every line item's unit_price (the per-unit "cost") must be >= 0. The
    -- `type(x.unit_price) != double` guard skips a non-numeric cost so it
    -- can't fault the CEL comparison -- that's the payload-shape engine's
    -- concern, not this rule's.
    ('line-cost-non-negative', 'cel', '',
       '{"expr":"!has(invoice.line_items) || invoice.line_items.all(x, !has(x.unit_price) || type(x.unit_price) != double || x.unit_price >= 0.0)"}',
       'error', 'Line item cost must be zero or positive.'),
    -- Sum(quantity * unit_price) across the line items must reconcile to the
    -- pre-VAT subtotal, within a kobo tolerance (subtotal + VAT = total).
    ('line-items-sum-subtotal', 'line_sum', '',
       '{"items":"line_items","amount":"unit_price","quantity":"quantity","expected":"subtotal","tolerance":0.005}',
       'error', 'Line item amounts must sum to the invoice subtotal.')
) AS r(key, type, target, params, severity, message)
WHERE v.version = 1;

-- +goose Down
DELETE FROM rules
 WHERE key IN ('line-cost-non-negative', 'line-items-sum-subtotal')
   AND rule_set_version_id IN (SELECT id FROM rule_set_versions WHERE version = 1);
ALTER TABLE rules DROP CONSTRAINT rules_type_check;
ALTER TABLE rules ADD CONSTRAINT rules_type_check CHECK (type IN
    ('required','format/regex','enum','range','tax_math',
     'cross_field','conditional','date','cel'));

-- +goose Up
-- M3-05: seed the first published, active global MBS rule-set (version 1).
-- Flips /v1/validate from 503 "no active rule-set" to live evaluation.
-- Content-only: no schema/grant/engine change (M3-04 shipped those).
INSERT INTO rule_set_versions (version, is_active, notes)
VALUES (1, true, 'MBS global rule-set v1 (M3-05 seed)');

INSERT INTO rules
    (rule_set_version_id, key, type, target, params, severity, "when", message, scope, enabled)
SELECT v.id, r.key, r.type, r.target, r.params::jsonb, r.severity, NULL, r.message, 'document', true
FROM rule_set_versions v
CROSS JOIN (VALUES
    -- (key, type, target, params-json, severity, message)  -- see the story's pinned rule table
    ('supplier-tin-required',   'required',     'supplier.tin', '{}',                                                        'error', 'Supplier TIN is required.'),
    ('supplier-tin-format',     'format/regex', 'supplier.tin', '{"pattern":"^[0-9]{8}-[0-9]{4}$"}',                         'error', 'Supplier TIN must be in the format NNNNNNNN-NNNN (8 digits, hyphen, 4 digits).'),
    ('buyer-tin-format',        'format/regex', 'buyer.tin',    '{"pattern":"^[0-9]{8}-[0-9]{4}$"}',                         'error', 'Buyer TIN, when present, must be in the format NNNNNNNN-NNNN.'),
    ('supplier-name-required',  'required',     'supplier.name','{}',                                                        'error', 'Supplier name is required.'),
    ('invoice-number-required', 'required',     'invoice_number','{}',                                                       'error', 'Invoice number is required.'),
    ('issue-date-required',     'required',     'issue_date',   '{}',                                                        'error', 'Invoice issue date is required.'),
    ('currency-required',       'required',     'currency',     '{}',                                                        'error', 'Currency is required.'),
    ('currency-allowed',        'enum',         'currency',     '{"values":["NGN"]}',                                        'error', 'Currency must be NGN.'),
    ('subtotal-required',       'required',     'subtotal',     '{}',                                                        'error', 'Subtotal is required.'),
    ('subtotal-non-negative',   'range',        'subtotal',     '{"min":0}',                                                 'error', 'Subtotal must be zero or positive.'),
    ('vat-required',            'required',     'vat',          '{}',                                                        'error', 'VAT amount is required.'),
    ('vat-non-negative',        'range',        'vat',          '{"min":0}',                                                 'error', 'VAT amount must be zero or positive.'),
    ('total-required',          'required',     'total',        '{}',                                                        'error', 'Total is required.'),
    ('total-non-negative',      'range',        'total',        '{"min":0}',                                                 'error', 'Total must be zero or positive.'),
    ('line-items-required',     'required',     'line_items',   '{}',                                                        'error', 'Invoice must include line items.'),
    ('vat-standard-rate',       'tax_math',     '',             '{"base":"subtotal","rate":0.075,"expected":"vat","tolerance":0.005}', 'error', 'VAT must equal 7.5% of the subtotal.'),
    ('no-duplicate-line-items', 'cel',          '',             '{"expr":"!has(invoice.line_items) || invoice.line_items.all(x, !has(x.id) || invoice.line_items.filter(y, has(y.id) && y.id == x.id).size() <= 1)"}', 'error', 'Invoice contains duplicate line items (a line id appears more than once).')
) AS r(key, type, target, params, severity, message)
WHERE v.version = 1;

-- +goose Down
DELETE FROM rule_set_versions WHERE version = 1;  -- rules cascade (ON DELETE CASCADE) -> restores 503

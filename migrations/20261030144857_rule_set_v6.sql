-- +goose Up
-- Publish rule-set v6 (v5 + note rules), dated 2027-01-01. v1-v5 untouched.
INSERT INTO rule_set_versions (version, sealed, notes)
VALUES (6, false, 'MBS global rule-set v6 (CRDDBTNOTE-03: credit and debit note fields)');

INSERT INTO rules
    (rule_set_version_id, key, type, target, params, severity, "when", message, scope, enabled)
SELECT v6.id, r.key, r.type, r.target, r.params, r.severity, r."when", r.message, r.scope, true
FROM rules r
JOIN rule_set_versions v5 ON v5.id = r.rule_set_version_id AND v5.version = 5
CROSS JOIN rule_set_versions v6
WHERE v6.version = 6;

-- billing-reference-*: NRS BillingReference; card number unconfirmed
INSERT INTO rules
    (rule_set_version_id, key, type, target, params, severity, "when", message, scope, enabled)
SELECT v6.id, n.key, 'required', n.target, '{}'::jsonb, 'error',
       $w$has(invoice.document_type) && invoice.document_type in ['credit_note', 'debit_note']$w$,
       n.message, 'document', true
FROM rule_set_versions v6
CROSS JOIN (VALUES
    ('billing-reference-irn-required', 'billing_reference.irn', 'A credit or debit note needs the IRN of its original invoice.'),
    ('billing-reference-issue-date-required', 'billing_reference.issue_date', 'A credit or debit note needs the issue date of its original invoice.')
) AS n(key, target, message)
WHERE v6.version = 6;

UPDATE rule_set_versions SET sealed = true, effective_from = '2027-01-01' WHERE version = 6;

-- +goose Down
ALTER TABLE rules              DISABLE TRIGGER rules_content_lock;
ALTER TABLE rule_set_versions  DISABLE TRIGGER rule_set_versions_seal_guard;
DELETE FROM rule_set_versions WHERE version = 6;
ALTER TABLE rule_set_versions  ENABLE TRIGGER rule_set_versions_seal_guard;
ALTER TABLE rules              ENABLE TRIGGER rules_content_lock;

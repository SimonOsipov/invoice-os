-- The document-type CHECK admits `debit note`.

-- +goose Up
ALTER TABLE extraction_jobs DROP CONSTRAINT extraction_jobs_document_type_check;
ALTER TABLE extraction_jobs ADD CONSTRAINT extraction_jobs_document_type_check
    CHECK (document_type IS NULL OR document_type IN (
        'receipt', 'proforma', 'quotation', 'credit note', 'debit note',
        'delivery note', 'statement', 'purchase order'));

-- +goose Down
-- NOT VALID: FORCE RLS hides existing debit note rows from the migrator, so they cannot be cleared.
ALTER TABLE extraction_jobs DROP CONSTRAINT extraction_jobs_document_type_check;
ALTER TABLE extraction_jobs ADD CONSTRAINT extraction_jobs_document_type_check
    CHECK (document_type IS NULL OR document_type IN (
        'receipt', 'proforma', 'quotation', 'credit note',
        'delivery note', 'statement', 'purchase order')) NOT VALID;

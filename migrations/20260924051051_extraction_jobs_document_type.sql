-- The document type the worker read a non-invoice as, when it was confident. NULL when the
-- check did not run, was skipped, or read a tax invoice. Grant and policy already cover it.

-- +goose Up
ALTER TABLE extraction_jobs ADD COLUMN document_type text
    CHECK (document_type IS NULL OR document_type IN (
        'receipt', 'proforma', 'quotation', 'credit note',
        'delivery note', 'statement', 'purchase order'));

-- +goose Down
ALTER TABLE extraction_jobs DROP COLUMN document_type;

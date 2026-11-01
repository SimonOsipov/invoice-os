-- Every invoice carries its NRS document type; a note names the invoice it adjusts.

-- +goose Up
ALTER TABLE invoices
    ADD COLUMN document_type text NOT NULL DEFAULT 'commercial_invoice'
        CONSTRAINT invoices_document_type_check
        CHECK (document_type IN ('commercial_invoice', 'credit_note', 'debit_note')),
    ADD COLUMN original_invoice_id uuid;
ALTER TABLE invoices
    ADD CONSTRAINT invoices_original_matches_type
        CHECK ((document_type = 'commercial_invoice') = (original_invoice_id IS NULL)),
    -- Composite so the original sits in the same tenant: a plain FK check skips RLS.
    ADD CONSTRAINT invoices_original_invoice_fk
        FOREIGN KEY (tenant_id, original_invoice_id) REFERENCES invoices (tenant_id, id);
CREATE INDEX invoices_original_invoice_idx ON invoices (tenant_id, original_invoice_id)
    WHERE original_invoice_id IS NOT NULL;

-- +goose Down
DROP INDEX invoices_original_invoice_idx;
ALTER TABLE invoices
    DROP CONSTRAINT invoices_original_invoice_fk,
    DROP CONSTRAINT invoices_original_matches_type,
    DROP COLUMN original_invoice_id,
    DROP COLUMN document_type;

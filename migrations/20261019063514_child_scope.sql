-- +goose Up
-- Child rows follow their parent's client; extraction rows follow document.ReadableByScope.
ALTER POLICY tenant_isolation ON submission_jobs USING (
    tenant_id = nullif(current_setting('app.current_tenant', true), '')::uuid
    AND (coalesce(current_setting('app.entity_scope', true), '') IN ('', '*')
         OR EXISTS (SELECT 1 FROM invoices i WHERE i.id = submission_jobs.invoice_id)));

ALTER POLICY tenant_isolation ON approval_runs USING (
    tenant_id = nullif(current_setting('app.current_tenant', true), '')::uuid
    AND (coalesce(current_setting('app.entity_scope', true), '') IN ('', '*')
         OR EXISTS (SELECT 1 FROM invoices i WHERE i.id = approval_runs.invoice_id)));

ALTER POLICY tenant_isolation ON approval_decisions USING (
    tenant_id = nullif(current_setting('app.current_tenant', true), '')::uuid
    AND (coalesce(current_setting('app.entity_scope', true), '') IN ('', '*')
         OR EXISTS (SELECT 1 FROM approval_runs r WHERE r.id = approval_decisions.run_id)));

ALTER POLICY tenant_isolation ON extraction_jobs USING (
    tenant_id = nullif(current_setting('app.current_tenant', true), '')::uuid
    AND (coalesce(current_setting('app.entity_scope', true), '') IN ('', '*')
         OR EXISTS (SELECT 1 FROM invoices i WHERE i.source_document_id = extraction_jobs.document_id)
         OR EXISTS (SELECT 1 FROM import_batches b WHERE b.document_id = extraction_jobs.document_id)
         OR NOT (SELECT has_link FROM public.document_link_state(extraction_jobs.document_id))));

ALTER POLICY tenant_isolation ON extraction_page_images USING (
    tenant_id = nullif(current_setting('app.current_tenant', true), '')::uuid
    AND (coalesce(current_setting('app.entity_scope', true), '') IN ('', '*')
         OR EXISTS (SELECT 1 FROM invoices i WHERE i.source_document_id = extraction_page_images.document_id)
         OR EXISTS (SELECT 1 FROM import_batches b WHERE b.document_id = extraction_page_images.document_id)
         OR NOT (SELECT has_link FROM public.document_link_state(extraction_page_images.document_id))));

-- +goose Down
ALTER POLICY tenant_isolation ON submission_jobs
    USING (tenant_id = nullif(current_setting('app.current_tenant', true), '')::uuid);

ALTER POLICY tenant_isolation ON approval_runs
    USING (tenant_id = nullif(current_setting('app.current_tenant', true), '')::uuid);

ALTER POLICY tenant_isolation ON approval_decisions
    USING (tenant_id = nullif(current_setting('app.current_tenant', true), '')::uuid);

ALTER POLICY tenant_isolation ON extraction_jobs
    USING (tenant_id = nullif(current_setting('app.current_tenant', true), '')::uuid);

ALTER POLICY tenant_isolation ON extraction_page_images
    USING (tenant_id = nullif(current_setting('app.current_tenant', true), '')::uuid);

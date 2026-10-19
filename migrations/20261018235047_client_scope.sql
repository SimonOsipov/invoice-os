-- +goose Up
-- app.entity_scope: unset or '' = no client limit (workers); '*' = every client; '{ids}' = those clients.
ALTER POLICY tenant_isolation ON business_entities USING (
    tenant_id = nullif(current_setting('app.current_tenant', true), '')::uuid
    AND (coalesce(current_setting('app.entity_scope', true), '') IN ('', '*')
         OR id = ANY (nullif(nullif(current_setting('app.entity_scope', true), ''), '*')::uuid[])));

ALTER POLICY tenant_isolation ON invoices USING (
    tenant_id = nullif(current_setting('app.current_tenant', true), '')::uuid
    AND (coalesce(current_setting('app.entity_scope', true), '') IN ('', '*')
         OR entity_id = ANY (nullif(nullif(current_setting('app.entity_scope', true), ''), '*')::uuid[])));

ALTER POLICY tenant_isolation ON import_batches USING (
    tenant_id = nullif(current_setting('app.current_tenant', true), '')::uuid
    AND (coalesce(current_setting('app.entity_scope', true), '') IN ('', '*')
         OR entity_id = ANY (nullif(nullif(current_setting('app.entity_scope', true), ''), '*')::uuid[])));

ALTER POLICY tenant_isolation ON import_mappings USING (
    tenant_id = nullif(current_setting('app.current_tenant', true), '')::uuid
    AND (coalesce(current_setting('app.entity_scope', true), '') IN ('', '*')
         OR entity_id = ANY (nullif(nullif(current_setting('app.entity_scope', true), ''), '*')::uuid[])));

ALTER POLICY tenant_isolation ON line_items USING (
    tenant_id = nullif(current_setting('app.current_tenant', true), '')::uuid
    AND (coalesce(current_setting('app.entity_scope', true), '') IN ('', '*')
         OR EXISTS (SELECT 1 FROM invoices i WHERE i.id = line_items.invoice_id)));

ALTER POLICY tenant_isolation ON invoice_status_history USING (
    tenant_id = nullif(current_setting('app.current_tenant', true), '')::uuid
    AND (coalesce(current_setting('app.entity_scope', true), '') IN ('', '*')
         OR EXISTS (SELECT 1 FROM invoices i WHERE i.id = invoice_status_history.invoice_id)));

-- +goose Down
ALTER POLICY tenant_isolation ON business_entities
    USING (tenant_id = nullif(current_setting('app.current_tenant', true), '')::uuid);

ALTER POLICY tenant_isolation ON invoices
    USING (tenant_id = nullif(current_setting('app.current_tenant', true), '')::uuid);

ALTER POLICY tenant_isolation ON import_batches
    USING (tenant_id = nullif(current_setting('app.current_tenant', true), '')::uuid);

ALTER POLICY tenant_isolation ON import_mappings
    USING (tenant_id = nullif(current_setting('app.current_tenant', true), '')::uuid);

ALTER POLICY tenant_isolation ON line_items
    USING (tenant_id = nullif(current_setting('app.current_tenant', true), '')::uuid);

ALTER POLICY tenant_isolation ON invoice_status_history
    USING (tenant_id = nullif(current_setting('app.current_tenant', true), '')::uuid);

-- +goose Up
-- A limited member reaches a buyer linked to their clients, or one they added.
SET LOCAL lock_timeout = '15s';

ALTER TABLE customers
    ADD COLUMN created_by uuid DEFAULT nullif(current_setting('app.current_user_id', true), '')::uuid,
    ADD COLUMN client_ids uuid[] NOT NULL DEFAULT '{}';

-- Link each record to every client whose invoice carries its TIN; per tenant under FORCE RLS.
-- ceiling: holds ACCESS EXCLUSIVE on customers until commit; out-of-band backfill above 500000 rows.
-- +goose StatementBegin
DO $backfill$
DECLARE
    v_tenant uuid;
    v_rows   bigint;
BEGIN
    ALTER TABLE customers NO FORCE ROW LEVEL SECURITY;
    CREATE TEMP TABLE customer_link_tenants ON COMMIT DROP AS
        SELECT DISTINCT tenant_id FROM customers;
    SELECT count(*) INTO v_rows FROM customers;
    IF v_rows > 500000 THEN
        RAISE EXCEPTION 'customers has % rows, above the 500000 in-migration ceiling; backfill client_ids out of band, then redeploy', v_rows
            USING ERRCODE = 'program_limit_exceeded';
    END IF;
    ALTER TABLE customers FORCE ROW LEVEL SECURITY;

    FOR v_tenant IN SELECT tenant_id FROM customer_link_tenants LOOP
        PERFORM set_config('app.current_tenant', v_tenant::text, true);
        UPDATE customers c SET client_ids = s.ids
          FROM (SELECT btrim(buyer_tin) AS tin, array_agg(DISTINCT entity_id ORDER BY entity_id) AS ids
                  FROM invoices GROUP BY 1) s
         WHERE c.tin = s.tin;
    END LOOP;
    PERFORM set_config('app.current_tenant', '', true);
END
$backfill$;
-- +goose StatementEnd

ALTER POLICY tenant_isolation ON customers USING (
    tenant_id = nullif(current_setting('app.current_tenant', true), '')::uuid
    AND (coalesce(current_setting('app.entity_scope', true), '') IN ('', '*')
         OR created_by = nullif(current_setting('app.current_user_id', true), '')::uuid
         OR client_ids && nullif(nullif(current_setting('app.entity_scope', true), ''), '*')::uuid[]));

-- +goose Down
ALTER POLICY tenant_isolation ON customers
    USING (tenant_id = nullif(current_setting('app.current_tenant', true), '')::uuid);
ALTER TABLE customers DROP COLUMN client_ids, DROP COLUMN created_by;

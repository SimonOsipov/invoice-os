-- +goose Up
-- A document binds to the client of its first citation; the uploader keeps read.
SET LOCAL lock_timeout = '15s';

ALTER TABLE documents
    ADD COLUMN entity_id   uuid,
    ADD COLUMN uploaded_by uuid DEFAULT nullif(current_setting('app.current_user_id', true), '')::uuid;
CREATE INDEX documents_entity_id_idx ON documents (entity_id) WHERE entity_id IS NOT NULL;

-- Backfill per tenant, as 20260820150810_audit_log_entity_id_and_read_indexes.sql does.
-- ceiling: holds ACCESS EXCLUSIVE on documents until commit; out-of-band backfill above 500000 rows.
-- +goose StatementBegin
DO $backfill$
DECLARE
    v_tenant uuid;
    v_rows   bigint;
BEGIN
    ALTER TABLE documents NO FORCE ROW LEVEL SECURITY;
    CREATE TEMP TABLE document_backfill_tenants ON COMMIT DROP AS
        SELECT DISTINCT tenant_id FROM documents;
    SELECT count(*) INTO v_rows FROM documents;
    IF v_rows > 500000 THEN
        RAISE EXCEPTION 'documents has % rows, above the 500000 in-migration ceiling; backfill entity_id and uploaded_by out of band, then redeploy', v_rows
            USING ERRCODE = 'program_limit_exceeded';
    END IF;
    ALTER TABLE documents FORCE ROW LEVEL SECURITY;

    FOR v_tenant IN SELECT tenant_id FROM document_backfill_tenants LOOP
        PERFORM set_config('app.current_tenant', v_tenant::text, true);

        UPDATE documents d SET entity_id = c.entity_id
          FROM (SELECT DISTINCT ON (doc) doc, entity_id
                  FROM (SELECT document_id AS doc, entity_id, created_at, 0 AS kind, id
                          FROM import_batches WHERE document_id IS NOT NULL
                        UNION ALL
                        SELECT source_document_id, entity_id, created_at, 1, id
                          FROM invoices WHERE source_document_id IS NOT NULL) x
                 ORDER BY doc, created_at, kind, id) c
         WHERE d.id = c.doc;

        UPDATE documents d SET uploaded_by = a.actor::uuid
          FROM (SELECT DISTINCT ON (payload->>'id') payload->>'id' AS doc, actor
                  FROM audit_log
                 WHERE event = 'document.created'
                   AND actor ~* '^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$'
                 ORDER BY payload->>'id', created_at, id) a
         WHERE a.doc = d.id::text;
    END LOOP;
    PERFORM set_config('app.current_tenant', '', true);
END
$backfill$;
-- +goose StatementEnd

GRANT UPDATE (entity_id) ON documents TO invoice_app;

-- +goose StatementBegin
CREATE FUNCTION documents_binding_guard() RETURNS trigger LANGUAGE plpgsql AS $fn$
BEGIN
    IF OLD.entity_id IS NOT NULL AND NEW.entity_id IS DISTINCT FROM OLD.entity_id THEN
        RAISE EXCEPTION 'document is bound to a client' USING ERRCODE = 'restrict_violation';
    END IF;
    RETURN NEW;
END
$fn$;
-- +goose StatementEnd
CREATE TRIGGER documents_binding_guard BEFORE UPDATE ON documents
    FOR EACH ROW EXECUTE FUNCTION documents_binding_guard();

-- The UPDATE then the SELECT are two statements: a loser blocked on the winner's row
-- lock re-reads the winner's client in the second one. Needs READ COMMITTED.
-- +goose StatementBegin
CREATE FUNCTION bind_document_client() RETURNS trigger LANGUAGE plpgsql AS $fn$
DECLARE
    v_doc   uuid := (to_jsonb(NEW) ->> TG_ARGV[0])::uuid;
    v_bound uuid;
BEGIN
    IF v_doc IS NULL THEN
        RETURN NEW;
    END IF;
    UPDATE documents SET entity_id = NEW.entity_id
     WHERE id = v_doc AND entity_id IS NULL
       AND (coalesce(current_setting('app.entity_scope', true), '') IN ('', '*')
            OR uploaded_by IS NULL
            OR uploaded_by = nullif(current_setting('app.current_user_id', true), '')::uuid);
    SELECT entity_id INTO v_bound FROM documents WHERE id = v_doc;
    IF NOT FOUND THEN
        RETURN NEW;
    END IF;
    IF v_bound IS DISTINCT FROM NEW.entity_id THEN
        RAISE EXCEPTION 'document cannot be cited for this client'
            USING ERRCODE = 'foreign_key_violation', CONSTRAINT = 'documents_bound_client';
    END IF;
    RETURN NEW;
END
$fn$;
-- +goose StatementEnd
CREATE TRIGGER invoices_bind_document
    BEFORE INSERT OR UPDATE OF entity_id, source_document_id ON invoices
    FOR EACH ROW EXECUTE FUNCTION bind_document_client('source_document_id');
CREATE TRIGGER import_batches_bind_document
    BEFORE INSERT OR UPDATE OF entity_id, document_id ON import_batches
    FOR EACH ROW EXECUTE FUNCTION bind_document_client('document_id');

-- +goose StatementBegin
CREATE FUNCTION public.document_readable(doc uuid) RETURNS boolean
    LANGUAGE sql STABLE AS $fn$
    SELECT coalesce(current_setting('app.entity_scope', true), '') IN ('', '*')
        OR EXISTS (SELECT 1 FROM public.documents d
                    WHERE d.id = doc
                      AND (d.entity_id IS NULL
                           OR d.uploaded_by = nullif(current_setting('app.current_user_id', true), '')::uuid
                           OR d.entity_id = ANY (nullif(nullif(current_setting('app.entity_scope', true), ''), '*')::uuid[])))
        OR EXISTS (SELECT 1 FROM public.invoices i WHERE i.source_document_id = doc)
        OR EXISTS (SELECT 1 FROM public.import_batches b WHERE b.document_id = doc)
$fn$;
-- +goose StatementEnd
REVOKE ALL ON FUNCTION public.document_readable(uuid) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION public.document_readable(uuid) TO invoice_app;

ALTER POLICY tenant_isolation ON extraction_jobs USING (
    tenant_id = nullif(current_setting('app.current_tenant', true), '')::uuid
    AND public.document_readable(extraction_jobs.document_id));

ALTER POLICY tenant_isolation ON extraction_page_images USING (
    tenant_id = nullif(current_setting('app.current_tenant', true), '')::uuid
    AND public.document_readable(extraction_page_images.document_id));

-- +goose Down
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

DROP TRIGGER import_batches_bind_document ON import_batches;
DROP TRIGGER invoices_bind_document ON invoices;
DROP TRIGGER documents_binding_guard ON documents;
DROP FUNCTION public.document_readable(uuid);
DROP FUNCTION bind_document_client();
DROP FUNCTION documents_binding_guard();
REVOKE UPDATE (entity_id) ON documents FROM invoice_app;
DROP INDEX documents_entity_id_idx;
ALTER TABLE documents DROP COLUMN uploaded_by, DROP COLUMN entity_id;

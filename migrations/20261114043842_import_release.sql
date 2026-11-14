-- +goose Up
-- A failed or undone batch and a discarded invoice are dead citations: they release the document binding.
SET LOCAL lock_timeout = '15s';

ALTER TABLE invoices
    DROP CONSTRAINT invoices_status_check,
    ADD CONSTRAINT invoices_status_check CHECK (status IN
        ('draft', 'validated', 'queued', 'submitted', 'accepted', 'rejected', 'failed', 'discarded'));
ALTER TABLE invoice_status_history
    DROP CONSTRAINT invoice_status_history_from_status_check,
    ADD CONSTRAINT invoice_status_history_from_status_check CHECK (from_status IS NULL OR from_status IN
        ('draft', 'validated', 'queued', 'submitted', 'accepted', 'rejected', 'failed', 'discarded'));
ALTER TABLE invoice_status_history
    DROP CONSTRAINT invoice_status_history_to_status_check,
    ADD CONSTRAINT invoice_status_history_to_status_check CHECK (to_status IN
        ('draft', 'validated', 'queued', 'submitted', 'accepted', 'rejected', 'failed', 'discarded'));
ALTER TABLE import_batches
    DROP CONSTRAINT import_batches_status_check,
    ADD CONSTRAINT import_batches_status_check CHECK (status IN
        ('pending', 'processing', 'completed', 'failed', 'undone'));

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION public.document_link_state(doc uuid, OUT has_invoice boolean, OUT has_link boolean)
    LANGUAGE plpgsql VOLATILE AS $$
DECLARE
    prior text := coalesce(current_setting('app.entity_scope', true), '');
BEGIN
    PERFORM set_config('app.entity_scope', '*', true);
    SELECT EXISTS (SELECT 1 FROM public.invoices WHERE source_document_id = doc AND status <> 'discarded') INTO has_invoice;
    has_link := has_invoice OR EXISTS (SELECT 1 FROM public.import_batches
                                        WHERE document_id = doc AND status NOT IN ('failed', 'undone'));
    PERFORM set_config('app.entity_scope', prior, true);
END
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION public.document_readable(doc uuid) RETURNS boolean
    LANGUAGE sql STABLE AS $fn$
    SELECT coalesce(current_setting('app.entity_scope', true), '') IN ('', '*')
        OR EXISTS (SELECT 1 FROM public.documents d
                    WHERE d.id = doc
                      AND (d.entity_id IS NULL
                           OR d.entity_id = ANY (nullif(nullif(current_setting('app.entity_scope', true), ''), '*')::uuid[])))
        OR EXISTS (SELECT 1 FROM public.document_uploaders u
                    WHERE u.document_id = doc
                      AND u.user_id = nullif(current_setting('app.current_user_id', true), '')::uuid)
        OR EXISTS (SELECT 1 FROM public.invoices i WHERE i.source_document_id = doc AND i.status <> 'discarded')
        OR EXISTS (SELECT 1 FROM public.import_batches b WHERE b.document_id = doc AND b.status NOT IN ('failed', 'undone'))
$fn$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION documents_binding_guard() RETURNS trigger LANGUAGE plpgsql AS $fn$
BEGIN
    IF OLD.entity_id IS NOT NULL AND NEW.entity_id IS DISTINCT FROM OLD.entity_id
       AND (NEW.entity_id IS NOT NULL OR (SELECT has_link FROM public.document_link_state(OLD.id))) THEN
        RAISE EXCEPTION 'document is bound to a client' USING ERRCODE = 'restrict_violation';
    END IF;
    RETURN NEW;
END
$fn$;
-- +goose StatementEnd

-- Lock first, as bind_document_client does, so a concurrent citation decides after this one.
-- +goose StatementBegin
CREATE FUNCTION release_document_binding() RETURNS trigger LANGUAGE plpgsql AS $fn$
BEGIN
    PERFORM 1 FROM documents WHERE id = NEW.document_id FOR NO KEY UPDATE;
    IF NOT (SELECT has_link FROM public.document_link_state(NEW.document_id)) THEN
        UPDATE documents SET entity_id = NULL WHERE id = NEW.document_id AND entity_id IS NOT NULL;
    END IF;
    RETURN NULL;
END
$fn$;
-- +goose StatementEnd
CREATE TRIGGER import_batches_release_document
    AFTER UPDATE OF status ON import_batches
    FOR EACH ROW
    WHEN (NEW.status IN ('failed', 'undone') AND OLD.status IS DISTINCT FROM NEW.status AND NEW.document_id IS NOT NULL)
    EXECUTE FUNCTION release_document_binding();

-- Rebind every bound document to its earliest live citation's client, or NULL.
-- ceiling: holds ACCESS EXCLUSIVE on documents until commit; out-of-band backfill above 500000 rows.
-- +goose StatementBegin
DO $backfill$
DECLARE
    v_tenant uuid;
    v_rows   bigint;
BEGIN
    ALTER TABLE documents NO FORCE ROW LEVEL SECURITY;
    CREATE TEMP TABLE document_release_tenants ON COMMIT DROP AS
        SELECT DISTINCT tenant_id FROM documents WHERE entity_id IS NOT NULL;
    SELECT count(*) INTO v_rows FROM documents;
    IF v_rows > 500000 THEN
        RAISE EXCEPTION 'documents has % rows, above the 500000 in-migration ceiling; rebind documents.entity_id out of band, then redeploy', v_rows
            USING ERRCODE = 'program_limit_exceeded';
    END IF;
    ALTER TABLE documents FORCE ROW LEVEL SECURITY;
    ALTER TABLE documents DISABLE TRIGGER documents_binding_guard;

    FOR v_tenant IN SELECT tenant_id FROM document_release_tenants LOOP
        PERFORM set_config('app.current_tenant', v_tenant::text, true);

        UPDATE documents d SET entity_id = c.entity_id
          FROM (SELECT dd.id, l.entity_id
                  FROM documents dd
                  LEFT JOIN (SELECT DISTINCT ON (doc) doc, entity_id
                               FROM (SELECT document_id AS doc, entity_id, created_at, 0 AS kind, id
                                       FROM import_batches
                                      WHERE document_id IS NOT NULL AND status NOT IN ('failed', 'undone')
                                     UNION ALL
                                     SELECT source_document_id, entity_id, created_at, 1, id
                                       FROM invoices
                                      WHERE source_document_id IS NOT NULL AND status <> 'discarded') x
                              ORDER BY doc, created_at, kind, id) l ON l.doc = dd.id
                 WHERE dd.entity_id IS NOT NULL) c
         WHERE d.id = c.id AND d.entity_id IS DISTINCT FROM c.entity_id;
    END LOOP;
    PERFORM set_config('app.current_tenant', '', true);
    ALTER TABLE documents ENABLE TRIGGER documents_binding_guard;
END
$backfill$;
-- +goose StatementEnd

-- +goose Down
-- ceiling: fails 23514 once a discarded invoice or an undone batch exists; does not undo the backfill.
DROP TRIGGER import_batches_release_document ON import_batches;
DROP FUNCTION release_document_binding();

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION documents_binding_guard() RETURNS trigger LANGUAGE plpgsql AS $fn$
BEGIN
    IF OLD.entity_id IS NOT NULL AND NEW.entity_id IS DISTINCT FROM OLD.entity_id THEN
        RAISE EXCEPTION 'document is bound to a client' USING ERRCODE = 'restrict_violation';
    END IF;
    RETURN NEW;
END
$fn$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION public.document_readable(doc uuid) RETURNS boolean
    LANGUAGE sql STABLE AS $fn$
    SELECT coalesce(current_setting('app.entity_scope', true), '') IN ('', '*')
        OR EXISTS (SELECT 1 FROM public.documents d
                    WHERE d.id = doc
                      AND (d.entity_id IS NULL
                           OR d.entity_id = ANY (nullif(nullif(current_setting('app.entity_scope', true), ''), '*')::uuid[])))
        OR EXISTS (SELECT 1 FROM public.document_uploaders u
                    WHERE u.document_id = doc
                      AND u.user_id = nullif(current_setting('app.current_user_id', true), '')::uuid)
        OR EXISTS (SELECT 1 FROM public.invoices i WHERE i.source_document_id = doc)
        OR EXISTS (SELECT 1 FROM public.import_batches b WHERE b.document_id = doc)
$fn$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION public.document_link_state(doc uuid, OUT has_invoice boolean, OUT has_link boolean)
    LANGUAGE plpgsql VOLATILE AS $$
DECLARE
    prior text := coalesce(current_setting('app.entity_scope', true), '');
BEGIN
    PERFORM set_config('app.entity_scope', '*', true);
    SELECT EXISTS (SELECT 1 FROM public.invoices WHERE source_document_id = doc) INTO has_invoice;
    has_link := has_invoice OR EXISTS (SELECT 1 FROM public.import_batches WHERE document_id = doc);
    PERFORM set_config('app.entity_scope', prior, true);
END
$$;
-- +goose StatementEnd

ALTER TABLE import_batches
    DROP CONSTRAINT import_batches_status_check,
    ADD CONSTRAINT import_batches_status_check CHECK (status IN
        ('pending', 'processing', 'completed', 'failed'));
ALTER TABLE invoice_status_history
    DROP CONSTRAINT invoice_status_history_to_status_check,
    ADD CONSTRAINT invoice_status_history_to_status_check CHECK (to_status IN
        ('draft', 'validated', 'queued', 'submitted', 'accepted', 'rejected', 'failed'));
ALTER TABLE invoice_status_history
    DROP CONSTRAINT invoice_status_history_from_status_check,
    ADD CONSTRAINT invoice_status_history_from_status_check CHECK (from_status IS NULL OR from_status IN
        ('draft', 'validated', 'queued', 'submitted', 'accepted', 'rejected', 'failed'));
ALTER TABLE invoices
    DROP CONSTRAINT invoices_status_check,
    ADD CONSTRAINT invoices_status_check CHECK (status IN
        ('draft', 'validated', 'queued', 'submitted', 'accepted', 'rejected', 'failed'));

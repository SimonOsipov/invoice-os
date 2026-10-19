-- +goose Up
-- The scope widens for one query and is restored: the tenant policy still applies, so a
-- limited caller learns whether a document has a link to any client of its tenant, never which.
-- +goose StatementBegin
CREATE FUNCTION public.document_link_state(doc uuid, OUT has_invoice boolean, OUT has_link boolean)
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
REVOKE ALL ON FUNCTION public.document_link_state(uuid) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION public.document_link_state(uuid) TO invoice_app;

-- +goose Down
DROP FUNCTION public.document_link_state(uuid);

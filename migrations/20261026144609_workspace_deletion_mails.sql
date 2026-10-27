-- +goose Up
CREATE TABLE public.workspace_deletion_links (
    id                uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id         uuid        NOT NULL REFERENCES public.tenants(id) ON DELETE CASCADE,
    recipient_user_id uuid        NOT NULL,
    token_hash        bytea       NOT NULL UNIQUE CHECK (octet_length(token_hash) = 32),
    -- The link is live while this equals tenants.deletion_due_at.
    expires_at        timestamptz NOT NULL,
    send_status       text        NOT NULL DEFAULT 'sending' CHECK (send_status IN ('sending', 'sent', 'failed')),
    created_at        timestamptz NOT NULL DEFAULT now()
);
ALTER TABLE public.workspace_deletion_links ENABLE ROW LEVEL SECURITY;
ALTER TABLE public.workspace_deletion_links FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON public.workspace_deletion_links
    USING (tenant_id = nullif(current_setting('app.current_tenant', true), '')::uuid);
GRANT SELECT, INSERT ON public.workspace_deletion_links TO invoice_app;
GRANT UPDATE (send_status) ON public.workspace_deletion_links TO invoice_app;

CREATE TABLE public.workspace_deletion_cancel_mails (
    id                uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id         uuid        NOT NULL REFERENCES public.tenants(id) ON DELETE CASCADE,
    recipient_user_id uuid        NOT NULL,
    cancelled_due_at  timestamptz NOT NULL,
    send_status       text        NOT NULL DEFAULT 'sending' CHECK (send_status IN ('sending', 'sent', 'failed')),
    created_at        timestamptz NOT NULL DEFAULT now()
);
ALTER TABLE public.workspace_deletion_cancel_mails ENABLE ROW LEVEL SECURITY;
ALTER TABLE public.workspace_deletion_cancel_mails FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON public.workspace_deletion_cancel_mails
    USING (tenant_id = nullif(current_setting('app.current_tenant', true), '')::uuid);
GRANT SELECT, INSERT ON public.workspace_deletion_cancel_mails TO invoice_app;
GRANT UPDATE (send_status) ON public.workspace_deletion_cancel_mails TO invoice_app;

-- +goose Down
DROP TABLE public.workspace_deletion_cancel_mails;
DROP TABLE public.workspace_deletion_links;

-- +goose Up
-- Erasure state is not tenant data: no tenant_id, no RLS. Guarantees are grants and CHECKs.
CREATE TABLE public.person_erasures (
    user_id           uuid PRIMARY KEY,
    email             text CHECK (email = lower(btrim(email)) AND octet_length(email) BETWEEN 3 AND 254),
    requested_at      timestamptz NOT NULL DEFAULT now(),
    local_erased_at   timestamptz,
    account_erased_at timestamptz,
    hubspot_erased_at timestamptz,
    resend_erased_at  timestamptz,
    vendor_mode       text CHECK (vendor_mode IN ('real', 'fake')),
    generation        bigint NOT NULL DEFAULT 1 CHECK (generation >= 1),
    superseded_at     timestamptz,
    finished_at       timestamptz,
    CHECK (superseded_at IS NULL OR finished_at IS NOT NULL),
    CHECK ((finished_at IS NULL) = (email IS NOT NULL))
);
GRANT SELECT, INSERT, UPDATE ON public.person_erasures TO invoice_app;
GRANT DELETE ON public.contacts, public.blog_subscribers TO invoice_app;

-- +goose Down
REVOKE DELETE ON public.contacts, public.blog_subscribers FROM invoice_app;
DROP TABLE public.person_erasures;

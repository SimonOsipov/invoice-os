-- Newsletter subscriptions are not tenant data: no tenant_id, no RLS. Guarantees are grants and CHECKs.
-- +goose Up
CREATE TABLE public.blog_subscription_requests (
    email        text        PRIMARY KEY CHECK (email = lower(btrim(email)) AND octet_length(email) BETWEEN 3 AND 254),
    category_ids text[]      NOT NULL CHECK (cardinality(category_ids) BETWEEN 1 AND 50),
    frequency    text        NOT NULL CHECK (frequency IN ('weekly', 'every_post', 'monthly')),
    token_hash   bytea       NOT NULL UNIQUE CHECK (octet_length(token_hash) = 32),
    expires_at   timestamptz NOT NULL,
    created_at   timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE public.blog_subscribers (
    email               text        PRIMARY KEY CHECK (email = lower(btrim(email)) AND octet_length(email) BETWEEN 3 AND 254),
    category_ids        text[]      NOT NULL CHECK (cardinality(category_ids) BETWEEN 1 AND 50),
    frequency           text        NOT NULL CHECK (frequency IN ('weekly', 'every_post', 'monthly')),
    confirmed_at        timestamptz NOT NULL,
    version             bigint      NOT NULL DEFAULT 1,
    resend_delivered_at timestamptz,
    delivery_mode       text        CHECK (delivery_mode IN ('real', 'fake')),
    created_at          timestamptz NOT NULL DEFAULT now(),
    updated_at          timestamptz NOT NULL DEFAULT now()
);

GRANT SELECT, INSERT, UPDATE, DELETE ON public.blog_subscription_requests TO invoice_app;
GRANT SELECT, INSERT, UPDATE ON public.blog_subscribers TO invoice_app;

-- +goose Down
DROP TABLE public.blog_subscribers;
DROP TABLE public.blog_subscription_requests;

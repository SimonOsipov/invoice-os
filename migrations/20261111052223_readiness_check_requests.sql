-- +goose Up
-- One row per confirm link until the owner presses it. Global: no tenant, no RLS. Never the file, never the client IP.
CREATE TABLE public.readiness_check_requests (
    email           text        NOT NULL CHECK (email = lower(btrim(email)) AND octet_length(email) BETWEEN 3 AND 254),
    token_hash      bytea       PRIMARY KEY CHECK (octet_length(token_hash) = 32),
    file_type       text        NOT NULL CHECK (file_type IN ('csv', 'xlsx', 'pdf', 'docx')),
    row_count       integer     NOT NULL CHECK (row_count >= 0),
    failed_rule_ids text[]      NOT NULL DEFAULT '{}',
    mail            jsonb       NOT NULL CHECK (jsonb_typeof(mail) = 'object'),
    expires_at      timestamptz NOT NULL,
    created_at      timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX readiness_check_requests_expires_at ON public.readiness_check_requests (expires_at);
CREATE INDEX readiness_check_requests_email ON public.readiness_check_requests (email);
GRANT SELECT, INSERT, UPDATE, DELETE ON public.readiness_check_requests TO invoice_app;

-- +goose Down
DROP TABLE public.readiness_check_requests;

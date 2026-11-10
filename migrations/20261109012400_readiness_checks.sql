-- +goose Up
-- One row per completed public check. Global: no tenant, no RLS. Never the file, never the client IP.
CREATE TABLE public.readiness_checks (
    id              bigint      GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    email           text        NOT NULL CHECK (email = lower(btrim(email)) AND octet_length(email) BETWEEN 3 AND 254),
    file_type       text        NOT NULL CHECK (file_type IN ('csv', 'xlsx', 'pdf', 'docx')),
    row_count       integer     NOT NULL CHECK (row_count >= 0),
    failed_rule_ids text[]      NOT NULL DEFAULT '{}',
    created_at      timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX readiness_checks_email ON public.readiness_checks (email);
GRANT SELECT, INSERT, DELETE ON public.readiness_checks TO invoice_app;

-- +goose Down
DROP TABLE public.readiness_checks;

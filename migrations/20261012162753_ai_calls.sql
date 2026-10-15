-- One row per AI call that sent a request. No content column (TestRLS_AICallsColumnsAreExactlyTheRecordFields).
-- ceiling: rows are kept forever; add a retention rule when one is decided or the table passes ~10M rows

-- +goose Up
CREATE TABLE ai_calls (
    id            uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id     uuid        NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    client        text        NOT NULL CHECK (client IN ('ai', 'jev')),
    purpose       text        NOT NULL CHECK (char_length(purpose) BETWEEN 1 AND 64),
    model         text        NOT NULL CHECK (char_length(model) <= 128),
    input_tokens  bigint      NOT NULL CHECK (input_tokens >= 0),
    output_tokens bigint      NOT NULL CHECK (output_tokens >= 0),
    cost          numeric     NOT NULL CHECK (cost >= 0),
    latency_ms    bigint      NOT NULL CHECK (latency_ms >= 0),
    attempts      int         NOT NULL CHECK (attempts >= 1),
    outcome       text        NOT NULL CHECK (char_length(outcome) BETWEEN 1 AND 64),
    created_at    timestamptz NOT NULL DEFAULT now()
);

ALTER TABLE ai_calls ENABLE ROW LEVEL SECURITY;
ALTER TABLE ai_calls FORCE  ROW LEVEL SECURITY;

CREATE POLICY tenant_isolation ON ai_calls
    USING (tenant_id = nullif(current_setting('app.current_tenant', true), '')::uuid);

GRANT SELECT, INSERT ON ai_calls TO invoice_app;

-- +goose Down
DROP TABLE ai_calls;

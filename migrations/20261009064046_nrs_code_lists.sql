-- +goose Up
-- Global NRS code lists, synced by the validation service (ENGI-03). No tenant, no RLS.
CREATE TABLE nrs_codes (
    list    text  NOT NULL CHECK (list <> ''),
    code    text  NOT NULL CHECK (code <> ''),
    -- every published entry for this code, in NRS order; NRS repeats some codes
    entries jsonb NOT NULL CHECK (jsonb_typeof(entries) = 'array' AND entries <> '[]'::jsonb),
    PRIMARY KEY (list, code)
);

CREATE TABLE nrs_code_list_syncs (
    id          uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    list        text        NOT NULL CHECK (list <> ''),
    synced_at   timestamptz NOT NULL DEFAULT now(),
    entry_count integer     NOT NULL CHECK (entry_count > 0),
    added       text[]      NOT NULL,
    removed     text[]      NOT NULL,
    changed     text[]      NOT NULL
);

GRANT SELECT, INSERT, DELETE, UPDATE (entries) ON nrs_codes TO invoice_app;
GRANT SELECT, INSERT ON nrs_code_list_syncs TO invoice_app;

-- +goose Down
DROP TABLE nrs_code_list_syncs;
DROP TABLE nrs_codes;

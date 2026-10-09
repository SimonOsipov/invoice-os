-- +goose Up
ALTER TABLE rule_set_versions ADD COLUMN effective_from date;
ALTER TABLE rule_set_versions ADD CONSTRAINT rule_set_versions_dated_is_sealed
    CHECK (effective_from IS NULL OR sealed);
UPDATE rule_set_versions SET effective_from = '2026-08-06' WHERE version = 4;

-- Guard C plus: a sealed version's start date is fixed once set.
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION rule_set_versions_seal_guard()
    RETURNS trigger
    LANGUAGE plpgsql
    AS $$
BEGIN
    IF TG_OP = 'DELETE' THEN
        IF OLD.sealed THEN
            RAISE EXCEPTION 'a sealed rule-set version cannot be deleted (version=%)', OLD.version
                USING ERRCODE = 'restrict_violation';
        END IF;
        RETURN OLD;
    END IF;

    -- UPDATE
    IF OLD.sealed AND NOT NEW.sealed THEN
        RAISE EXCEPTION 'a sealed rule-set version cannot be unsealed (version=%)', OLD.version
            USING ERRCODE = 'restrict_violation';
    END IF;
    IF OLD.sealed AND OLD.effective_from IS NOT NULL
       AND NEW.effective_from IS DISTINCT FROM OLD.effective_from THEN
        RAISE EXCEPTION 'the start date of a sealed rule-set version cannot change (version=%)', OLD.version
            USING ERRCODE = 'restrict_violation';
    END IF;
    RETURN NEW;
END;
$$;
-- +goose StatementEnd

-- Latest start <= d wins, tie to the higher version; a date before every start gets the earliest.
-- +goose StatementBegin
CREATE FUNCTION rule_set_version_for(d date) RETURNS uuid
    LANGUAGE sql STABLE STRICT AS $$
    SELECT id FROM rule_set_versions
     WHERE effective_from IS NOT NULL
     ORDER BY effective_from <= d DESC,
              CASE WHEN effective_from <= d THEN effective_from END DESC,
              effective_from, version DESC
     LIMIT 1
$$;
-- +goose StatementEnd

CREATE TABLE rule_set_version_rechecks (
    rule_set_version_id uuid PRIMARY KEY REFERENCES rule_set_versions(id) ON DELETE CASCADE,
    rechecked_at        timestamptz NOT NULL DEFAULT now()
);
GRANT SELECT, INSERT ON rule_set_version_rechecks TO invoice_app;
INSERT INTO rule_set_version_rechecks (rule_set_version_id)
    SELECT id FROM rule_set_versions WHERE effective_from IS NOT NULL;

-- +goose Down
DROP TABLE rule_set_version_rechecks;
DROP FUNCTION rule_set_version_for(date);

-- Guard C back to its M4-17 body.
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION rule_set_versions_seal_guard()
    RETURNS trigger
    LANGUAGE plpgsql
    AS $$
BEGIN
    IF TG_OP = 'DELETE' THEN
        IF OLD.sealed THEN
            RAISE EXCEPTION 'a sealed rule-set version cannot be deleted (version=%)', OLD.version
                USING ERRCODE = 'restrict_violation';
        END IF;
        RETURN OLD;
    END IF;

    -- UPDATE
    IF OLD.sealed AND NOT NEW.sealed THEN
        RAISE EXCEPTION 'a sealed rule-set version cannot be unsealed (version=%)', OLD.version
            USING ERRCODE = 'restrict_violation';
    END IF;
    RETURN NEW;
END;
$$;
-- +goose StatementEnd

ALTER TABLE rule_set_versions DROP CONSTRAINT rule_set_versions_dated_is_sealed;
ALTER TABLE rule_set_versions DROP COLUMN effective_from;

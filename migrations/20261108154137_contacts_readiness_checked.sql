-- +goose Up
ALTER TABLE public.contacts ADD COLUMN readiness_checked_at timestamptz;

-- Drop by definition: the generated name (contacts_check1) is not a contract.
-- +goose StatementBegin
DO $$
DECLARE
    expected constant text := 'CHECK (((registered_at IS NOT NULL) OR (demo_requested_at IS NOT NULL)))';
    found text[];
BEGIN
    SELECT array_agg(conname::text) INTO found
      FROM pg_constraint
     WHERE conrelid = 'public.contacts'::regclass
       AND contype = 'c'
       AND pg_get_constraintdef(oid) = expected;
    IF coalesce(cardinality(found), 0) <> 1 THEN
        RAISE EXCEPTION 'contacts: expected exactly one CHECK defined as %, found %', expected, coalesce(cardinality(found), 0);
    END IF;
    EXECUTE format('ALTER TABLE public.contacts DROP CONSTRAINT %I', found[1]);
END
$$;
-- +goose StatementEnd

ALTER TABLE public.contacts ADD CONSTRAINT contacts_has_a_source
    CHECK (registered_at IS NOT NULL OR demo_requested_at IS NOT NULL OR readiness_checked_at IS NOT NULL);

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION public.contacts_facts_only_grow() RETURNS trigger
    LANGUAGE plpgsql AS $$
BEGIN
    IF (OLD.registered_at IS NOT NULL AND NEW.registered_at IS DISTINCT FROM OLD.registered_at)
       OR (OLD.demo_requested_at IS NOT NULL AND NEW.demo_requested_at IS DISTINCT FROM OLD.demo_requested_at)
       OR (OLD.marketing_consent_text IS NOT NULL AND NEW.marketing_consent_text IS DISTINCT FROM OLD.marketing_consent_text)
       OR (OLD.marketing_consented_at IS NOT NULL AND NEW.marketing_consented_at IS DISTINCT FROM OLD.marketing_consented_at)
       OR (OLD.readiness_checked_at IS NOT NULL AND NEW.readiness_checked_at IS DISTINCT FROM OLD.readiness_checked_at)
    THEN
        RAISE EXCEPTION 'contacts: a recorded fact cannot change' USING ERRCODE = 'restrict_violation';
    END IF;
    RETURN NEW;
END;
$$;
-- +goose StatementEnd

-- +goose Down
-- The old schema cannot hold a row with no registration and no demo request.
DELETE FROM public.contacts WHERE registered_at IS NULL AND demo_requested_at IS NULL;

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION public.contacts_facts_only_grow() RETURNS trigger
    LANGUAGE plpgsql AS $$
BEGIN
    IF (OLD.registered_at IS NOT NULL AND NEW.registered_at IS DISTINCT FROM OLD.registered_at)
       OR (OLD.demo_requested_at IS NOT NULL AND NEW.demo_requested_at IS DISTINCT FROM OLD.demo_requested_at)
       OR (OLD.marketing_consent_text IS NOT NULL AND NEW.marketing_consent_text IS DISTINCT FROM OLD.marketing_consent_text)
       OR (OLD.marketing_consented_at IS NOT NULL AND NEW.marketing_consented_at IS DISTINCT FROM OLD.marketing_consented_at)
    THEN
        RAISE EXCEPTION 'contacts: a recorded fact cannot change' USING ERRCODE = 'restrict_violation';
    END IF;
    RETURN NEW;
END;
$$;
-- +goose StatementEnd

ALTER TABLE public.contacts DROP CONSTRAINT contacts_has_a_source;
ALTER TABLE public.contacts ADD CONSTRAINT contacts_check1
    CHECK (registered_at IS NOT NULL OR demo_requested_at IS NOT NULL);
ALTER TABLE public.contacts DROP COLUMN readiness_checked_at;

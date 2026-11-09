-- +goose Up
ALTER TABLE public.contacts
    ADD COLUMN readiness_consent_text text,
    ADD COLUMN readiness_consented_at timestamptz;
ALTER TABLE public.contacts ADD CONSTRAINT contacts_readiness_consent_pair
    CHECK ((readiness_consent_text IS NULL) = (readiness_consented_at IS NULL));

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION public.contacts_facts_only_grow() RETURNS trigger
    LANGUAGE plpgsql AS $$
BEGIN
    IF (OLD.registered_at IS NOT NULL AND NEW.registered_at IS DISTINCT FROM OLD.registered_at)
       OR (OLD.demo_requested_at IS NOT NULL AND NEW.demo_requested_at IS DISTINCT FROM OLD.demo_requested_at)
       OR (OLD.marketing_consent_text IS NOT NULL AND NEW.marketing_consent_text IS DISTINCT FROM OLD.marketing_consent_text)
       OR (OLD.marketing_consented_at IS NOT NULL AND NEW.marketing_consented_at IS DISTINCT FROM OLD.marketing_consented_at)
       OR (OLD.readiness_checked_at IS NOT NULL AND NEW.readiness_checked_at IS DISTINCT FROM OLD.readiness_checked_at)
       OR (OLD.readiness_consent_text IS NOT NULL AND NEW.readiness_consent_text IS DISTINCT FROM OLD.readiness_consent_text)
       OR (OLD.readiness_consented_at IS NOT NULL AND NEW.readiness_consented_at IS DISTINCT FROM OLD.readiness_consented_at)
    THEN
        RAISE EXCEPTION 'contacts: a recorded fact cannot change' USING ERRCODE = 'restrict_violation';
    END IF;
    RETURN NEW;
END;
$$;
-- +goose StatementEnd

-- +goose Down
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

ALTER TABLE public.contacts DROP CONSTRAINT contacts_readiness_consent_pair;
ALTER TABLE public.contacts DROP COLUMN readiness_consented_at, DROP COLUMN readiness_consent_text;

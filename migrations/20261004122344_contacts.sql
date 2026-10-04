-- Contacts are not tenant data: no tenant_id, no RLS. Guarantees are grants, CHECKs and the trigger.

-- +goose Up
CREATE TABLE public.contacts (
    email                  text PRIMARY KEY CHECK (email = lower(btrim(email)) AND octet_length(email) BETWEEN 3 AND 254),
    user_id                uuid,
    first_name             text,
    last_name              text,
    company                text,
    registered_at          timestamptz,
    demo_requested_at      timestamptz,
    marketing_consent_text text,
    marketing_consented_at timestamptz,
    version                bigint NOT NULL DEFAULT 1,
    hubspot_delivered_at   timestamptz,
    resend_delivered_at    timestamptz,
    resend_opt_in_sent_at  timestamptz,
    delivery_mode          text CHECK (delivery_mode IN ('real', 'fake')),
    created_at             timestamptz NOT NULL DEFAULT now(),
    updated_at             timestamptz NOT NULL DEFAULT now(),
    CHECK ((marketing_consent_text IS NULL) = (marketing_consented_at IS NULL)),
    CHECK (registered_at IS NOT NULL OR demo_requested_at IS NOT NULL)
);

GRANT SELECT, INSERT, UPDATE ON public.contacts TO invoice_app;

-- A set fact never changes; applies to the owner too.
-- +goose StatementBegin
CREATE FUNCTION public.contacts_facts_only_grow() RETURNS trigger
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

CREATE TRIGGER contacts_facts_only_grow
    BEFORE UPDATE ON public.contacts
    FOR EACH ROW EXECUTE FUNCTION public.contacts_facts_only_grow();

-- +goose Down
DROP TABLE public.contacts;
DROP FUNCTION public.contacts_facts_only_grow();

-- +goose Up
-- Global like contacts: an address carries no tenant, so no RLS.
CREATE TABLE public.mail_unsubscribes (
    email           text PRIMARY KEY CHECK (email = lower(btrim(email)) AND octet_length(email) BETWEEN 3 AND 254),
    token           uuid NOT NULL UNIQUE DEFAULT gen_random_uuid(),
    unsubscribed_at timestamptz,
    created_at      timestamptz NOT NULL DEFAULT now()
);
GRANT SELECT, DELETE ON public.mail_unsubscribes TO invoice_app;
GRANT UPDATE (unsubscribed_at) ON public.mail_unsubscribes TO invoice_app;

-- +goose StatementBegin
CREATE FUNCTION public.mail_unsubscribes_guard() RETURNS trigger
    LANGUAGE plpgsql SET search_path = '' AS $$
BEGIN
    IF NEW.token IS DISTINCT FROM OLD.token THEN
        RAISE EXCEPTION 'mail_unsubscribes.token never changes' USING ERRCODE = 'restrict_violation';
    END IF;
    IF OLD.unsubscribed_at IS NOT NULL AND NEW.unsubscribed_at IS DISTINCT FROM OLD.unsubscribed_at THEN
        RAISE EXCEPTION 'mail_unsubscribes.unsubscribed_at never changes once set' USING ERRCODE = 'restrict_violation';
    END IF;
    RETURN NEW;
END
$$;
-- +goose StatementEnd
CREATE TRIGGER mail_unsubscribes_guard
    BEFORE UPDATE ON public.mail_unsubscribes
    FOR EACH ROW EXECUTE FUNCTION public.mail_unsubscribes_guard();

-- One writer of tokens: get or create, normalised like contacts.email.
-- +goose StatementBegin
CREATE FUNCTION public.mail_unsubscribe_token(p_email text) RETURNS uuid
    LANGUAGE plpgsql VOLATILE SECURITY DEFINER SET search_path = '' AS $$
DECLARE
    v_email text := pg_catalog.lower(pg_catalog.btrim(p_email));
    v_token uuid;
BEGIN
    -- DO NOTHING writes no tuple, so a read of a known address takes no row lock. The loop covers a delete between the two statements.
    LOOP
        INSERT INTO public.mail_unsubscribes (email) VALUES (v_email)
        ON CONFLICT (email) DO NOTHING
        RETURNING token INTO v_token;
        EXIT WHEN v_token IS NOT NULL;
        SELECT token INTO v_token FROM public.mail_unsubscribes WHERE email = v_email;
        EXIT WHEN v_token IS NOT NULL;
    END LOOP;
    RETURN v_token;
END
$$;
-- +goose StatementEnd
REVOKE EXECUTE ON FUNCTION public.mail_unsubscribe_token(text) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION public.mail_unsubscribe_token(text) TO invoice_app;
-- The role is created by db/bootstrap.sql; a database bootstrapped before it has none.
-- ceiling: invoice_gateway can create a token row for any address past register's 400 checks; Cloud Armor throttles register.
-- +goose StatementBegin
DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'invoice_gateway') THEN
    GRANT EXECUTE ON FUNCTION public.mail_unsubscribe_token(text) TO invoice_gateway;
  END IF;
END
$$;
-- +goose StatementEnd

-- +goose Down
DROP FUNCTION public.mail_unsubscribe_token(text);
DROP TABLE public.mail_unsubscribes;
DROP FUNCTION public.mail_unsubscribes_guard();

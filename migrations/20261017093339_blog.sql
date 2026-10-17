-- Global blog content: no tenant, no RLS. Staff routes write it with invoice_app grants.
-- +goose Up
CREATE TABLE public.blog_categories (
    id       text     PRIMARY KEY CHECK (id ~ '^[a-z0-9-]{1,80}$'),
    name     text     NOT NULL CHECK (btrim(name) <> ''),
    headline text     NOT NULL,
    intro    text     NOT NULL DEFAULT '',
    icon     text     NOT NULL CHECK (icon IN ('package', 'landmark', 'building-2', 'megaphone', 'handshake', 'code-xml', 'globe', 'sparkles',
                                               'shield-check', 'file-check', 'chart-column', 'workflow', 'plug', 'archive', 'key-round', 'clock')),
    tone     smallint NOT NULL CHECK (tone BETWEEN 0 AND 5),
    position integer  NOT NULL,
    -- DEFERRABLE: a move swaps two positions in one UPDATE.
    CONSTRAINT blog_categories_position_key UNIQUE (position) DEFERRABLE
);
CREATE UNIQUE INDEX blog_categories_name_key ON public.blog_categories (lower(name));

CREATE TABLE public.blog_posts (
    id           text        PRIMARY KEY CHECK (id ~ '^[a-z0-9-]{1,80}$'),
    category_id  text        REFERENCES public.blog_categories (id),
    publish_date date,
    title        text        NOT NULL CHECK (btrim(title) <> ''),
    summary      text        NOT NULL DEFAULT '',
    author_id    uuid,
    author_name  text,
    status       text        NOT NULL DEFAULT 'draft' CHECK (status IN ('draft', 'published')),
    featured     boolean     NOT NULL DEFAULT false,
    body         jsonb       NOT NULL DEFAULT '[]' CHECK (jsonb_typeof(body) = 'array'),
    created_at   timestamptz NOT NULL DEFAULT now(),
    updated_at   timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT blog_posts_author_pair CHECK ((author_id IS NULL) = (author_name IS NULL)),
    CONSTRAINT blog_posts_published_complete CHECK (status = 'draft' OR (
        btrim(summary) <> '' AND category_id IS NOT NULL AND publish_date IS NOT NULL
        AND author_id IS NOT NULL AND jsonb_array_length(body) > 0))
);
CREATE UNIQUE INDEX blog_posts_one_featured ON public.blog_posts (featured) WHERE featured;
CREATE INDEX blog_posts_category_idx ON public.blog_posts (category_id);

CREATE TABLE public.blog_post_covers (
    post_id      text        PRIMARY KEY REFERENCES public.blog_posts (id) ON DELETE CASCADE,
    content_type text        NOT NULL CHECK (content_type IN ('image/jpeg', 'image/png')),
    bytes        bytea       NOT NULL CHECK (octet_length(bytes) BETWEEN 1 AND 2097152),
    updated_at   timestamptz NOT NULL DEFAULT now()
);

GRANT SELECT, INSERT, UPDATE, DELETE ON public.blog_categories, public.blog_posts, public.blog_post_covers TO invoice_app;

-- Created as the owner, so the owner's REVOKE and GRANT stand.
SET LOCAL ROLE staff_reader;
-- +goose StatementBegin
CREATE FUNCTION public.staff_read_blog_authors(p_actor uuid)
RETURNS TABLE (user_id uuid, display_name text, email text)
    LANGUAGE plpgsql STABLE SECURITY DEFINER SET search_path = '' AS $$
BEGIN
    IF NOT public.is_house_member(p_actor) THEN
        RAISE EXCEPTION 'staff_read_blog_authors: actor is not staff' USING ERRCODE = 'insufficient_privilege';
    END IF;
    -- ceiling: one is_house_member call per membership row; join house_company above ~10k memberships
    -- A blank name is NULL; email is for the console picker only.
    RETURN QUERY SELECT a.user_id, a.display_name, a.email FROM (
        SELECT DISTINCT ON (m.user_id) m.user_id, nullif(btrim(m.display_name), '') AS display_name, m.email
          FROM public.memberships m
         WHERE m.status = 'active' AND public.is_house_member(m.user_id)
         ORDER BY m.user_id, m.created_at) a
    ORDER BY a.display_name NULLS LAST, a.email, a.user_id;
END $$;
-- +goose StatementEnd
REVOKE EXECUTE ON FUNCTION public.staff_read_blog_authors(uuid) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION public.staff_read_blog_authors(uuid) TO invoice_app;
RESET ROLE;

-- +goose Down
SET LOCAL ROLE staff_reader;
DROP FUNCTION public.staff_read_blog_authors(uuid);
RESET ROLE;
DROP TABLE public.blog_post_covers;
DROP TABLE public.blog_posts;
DROP TABLE public.blog_categories;

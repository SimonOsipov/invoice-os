-- Stub (D20): the table only. The grant, policy and hook body land with the real migration.

-- +goose Up
CREATE TABLE public.staff_members (
    user_id    uuid PRIMARY KEY,
    created_at timestamptz NOT NULL DEFAULT now()
);

-- +goose Down
DROP TABLE public.staff_members;

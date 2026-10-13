-- staff_audit_log takes any staff change. tenant_id names the tenant a change touched; no FK, the log outlives a tenant.
-- +goose Up
ALTER TABLE public.staff_audit_log
    ALTER COLUMN rule_set_version_id DROP NOT NULL,
    ADD COLUMN tenant_id  uuid CONSTRAINT staff_audit_tenant_set  CHECK (tenant_id  <> '00000000-0000-0000-0000-000000000000'),
    ADD COLUMN subject_id uuid CONSTRAINT staff_audit_subject_set CHECK (subject_id <> '00000000-0000-0000-0000-000000000000');

-- +goose Down
-- rule_set_version_id stays nullable: the append-only trigger keeps any row without one.
ALTER TABLE public.staff_audit_log DROP COLUMN subject_id, DROP COLUMN tenant_id;

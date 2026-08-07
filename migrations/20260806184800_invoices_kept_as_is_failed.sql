-- +goose Up
-- Renamed, not re-predicated under the old name: `_draft_only` would be an
-- actively false name once `failed` is also allowed.
ALTER TABLE invoices DROP CONSTRAINT invoices_kept_as_is_draft_only;
ALTER TABLE invoices ADD  CONSTRAINT invoices_kept_as_is_status
    CHECK (kept_as_is_at IS NULL OR status IN ('draft', 'failed'));

-- +goose Down
ALTER TABLE invoices DROP CONSTRAINT invoices_kept_as_is_status;
ALTER TABLE invoices ADD  CONSTRAINT invoices_kept_as_is_draft_only
    CHECK (kept_as_is_at IS NULL OR status = 'draft');

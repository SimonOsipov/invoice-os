-- Soft delete is the ONLY available shape for removing an approval policy: invoice_app holds
-- no DELETE grant on approval_policies; the cascade into approval_policy_versions trips
-- approval_policy_versions_seal_guard (23001) on any sealed row; and approval_runs ->
-- approval_policy_versions is ON DELETE RESTRICT. A published policy is structurally
-- undeletable, so deleted_at is the removal path.
--
-- approval_policy_versions_one_draft caps a policy at one unsealed version: the draft PUT
-- resolves its target by NOT sealed, so two drafts would make that resolution ambiguous.

-- +goose Up
ALTER TABLE approval_policies ADD COLUMN deleted_at timestamptz;

CREATE UNIQUE INDEX approval_policy_versions_one_draft
    ON approval_policy_versions (tenant_id, policy_id) WHERE NOT sealed;

-- +goose Down
DROP INDEX approval_policy_versions_one_draft;
ALTER TABLE approval_policies DROP COLUMN deleted_at;

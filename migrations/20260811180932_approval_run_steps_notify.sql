-- Materialisation flattens branches, so a run step's ord does not map back to the policy
-- step's ord and there is no policy_step_id back-link: a notify step's target and channel
-- cannot be recovered by a join and must be copied onto the run step.

-- +goose Up
ALTER TABLE approval_run_steps
    ADD COLUMN notify_target  text,
    ADD COLUMN notify_channel text;

-- +goose Down
ALTER TABLE approval_run_steps
    DROP COLUMN notify_target,
    DROP COLUMN notify_channel;

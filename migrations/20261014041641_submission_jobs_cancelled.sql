-- Adds the terminal job state cancelled, set only by a staff cancel.
-- +goose Up
ALTER TABLE submission_jobs DROP CONSTRAINT submission_jobs_state_check;
ALTER TABLE submission_jobs ADD  CONSTRAINT submission_jobs_state_check
    CHECK (state IN ('queued', 'submitting', 'pending', 'accepted', 'rejected', 'failed',
                     'dead_lettered', 'cancelled'));

-- +goose Down
-- Narrowing: fails 23514 if any row holds 'cancelled'.
ALTER TABLE submission_jobs DROP CONSTRAINT submission_jobs_state_check;
ALTER TABLE submission_jobs ADD  CONSTRAINT submission_jobs_state_check
    CHECK (state IN ('queued', 'submitting', 'pending', 'accepted', 'rejected', 'failed',
                     'dead_lettered'));

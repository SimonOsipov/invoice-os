-- +goose Up
-- 0 is the decided reading (single/no candidate); 1..N are surviving alternatives
-- Reconcile keeps. No GRANT reissue: the table-level grant already covers new columns.
ALTER TABLE extraction_field_results
    ADD COLUMN candidate_rank int NOT NULL DEFAULT 0 CHECK (candidate_rank >= 0);

-- +goose Down
ALTER TABLE extraction_field_results DROP COLUMN candidate_rank;

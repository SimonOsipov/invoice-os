// M5-06-01 (task-243): the `next_poll_at` partial index migration. Both cases read
// pg_indexes — a system catalog with no RLS — so neither needs a tenant fixture.
//
// RED NOW: the migration does not exist yet (confirmed against this worktree's dev DB:
// `make migrate-status` tops out at 20260723133200_submission_dead_letter_and_rate_limits.sql),
// so pg_indexes has zero rows named submission_jobs_tenant_next_poll_idx and both cases
// fail on t.Fatalf, never on a compile or connection error.
//
// TestRLS_SubmissionJobsNextPollAtIndexReversible is SIMPLIFIED for the RED stage, per the
// QA task brief: a full goose Down/Up round trip in-test needs the migration's own
// timestamped version number to target, which does not exist until the executor creates
// the file. It asserts the same "present, exactly once, right shape" property as its
// sibling rather than driving goose itself — the executor's own reset->up->down->up round
// trip (task-243 AC-1) and the CI `migrations` job's reversibility gate are what actually
// prove Down works cleanly.
//
// Spec-to-test map (M5-06 story, [M5-06-01] Test Specs table):
//
//	AC-1 TestRLS_SubmissionJobsNextPollAtIndexExists
//	AC-1 TestRLS_SubmissionJobsNextPollAtIndexReversible
package reconciliation

import (
	"context"
	"strings"
	"testing"
)

func TestRLS_SubmissionJobsNextPollAtIndexExists(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	var indexdef string
	err := h.mig.QueryRow(ctx,
		`SELECT indexdef FROM pg_indexes
		   WHERE tablename = 'submission_jobs' AND indexname = 'submission_jobs_tenant_next_poll_idx'`,
	).Scan(&indexdef)
	if err != nil {
		t.Fatalf("submission_jobs_tenant_next_poll_idx: want it to exist (Constraint "+
			"[next-poll-index]), got: %v — the M5-06-01 migration has not been applied yet", err)
	}

	if !strings.Contains(indexdef, "(tenant_id, next_poll_at)") {
		t.Errorf("indexdef = %q, want it to contain %q", indexdef, "(tenant_id, next_poll_at)")
	}
	if !strings.Contains(indexdef, "WHERE (state = 'pending'::text)") {
		t.Errorf("indexdef = %q, want it to contain %q (the partial-index predicate, "+
			"[next-poll-index-is-partial])", indexdef, "WHERE (state = 'pending'::text)")
	}
}

func TestRLS_SubmissionJobsNextPollAtIndexReversible(t *testing.T) {
	h := requireHarness(t)

	n := mustCount(t, h.mig,
		`SELECT count(*) FROM pg_indexes
		   WHERE tablename = 'submission_jobs' AND indexname = 'submission_jobs_tenant_next_poll_idx'`)
	if n != 1 {
		t.Fatalf("submission_jobs_tenant_next_poll_idx rows in pg_indexes = %d, want exactly 1 "+
			"(present after Up, and only once — a duplicate Up is not idempotent) — the M5-06-01 "+
			"migration has not been applied yet", n)
	}
}

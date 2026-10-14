// submission_jobs_state_check admits `cancelled` (migration A) and its Down narrows it back.
package db_test

import (
	"context"
	"errors"
	"io/fs"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/SimonOsipov/invoice-os/internal/platform/db"
	"github.com/SimonOsipov/invoice-os/migrations"
)

const jobStateCheck = "submission_jobs_state_check"

var jobStateCheckAdd = regexp.MustCompile(`ADD\s+CONSTRAINT\s+submission_jobs_state_check\s+CHECK\s*\(\s*state\s+IN\s*\(([^)]*)\)\s*\)`)

// upSection returns the text between the `+goose Up` and `+goose Down` markers.
func upSection(raw string) string {
	up := raw
	if i := strings.Index(up, "-- +goose Up"); i >= 0 {
		up = up[i:]
	}
	if i := strings.Index(up, "-- +goose Down"); i >= 0 {
		up = up[:i]
	}
	return up
}

// jobStateChecks lists every migration that re-adds the constraint, as (version, states) in file order.
func jobStateChecks(t *testing.T) (versions []int64, states [][]string) {
	t.Helper()
	files, err := fs.Glob(migrations.FS, "*.sql")
	if err != nil {
		t.Fatalf("glob migrations.FS: %v", err)
	}
	for _, f := range files {
		raw, err := fs.ReadFile(migrations.FS, f)
		if err != nil {
			t.Fatalf("read %s: %v", f, err)
		}
		m := jobStateCheckAdd.FindStringSubmatch(upSection(string(raw)))
		if m == nil {
			continue
		}
		v, err := strconv.ParseInt(strings.SplitN(f, "_", 2)[0], 10, 64)
		if err != nil {
			t.Fatalf("parse goose version out of %q: %v", f, err)
		}
		var list []string
		for _, s := range strings.Split(m[1], ",") {
			list = append(list, strings.Trim(strings.TrimSpace(s), "'"))
		}
		versions = append(versions, v)
		states = append(states, list)
	}
	return versions, states
}

// submissionJobsCancelledMigrationVersion is the one migration whose Up re-adds the CHECK with `cancelled`.
func submissionJobsCancelledMigrationVersion(t *testing.T) int64 {
	t.Helper()
	versions, states := jobStateChecks(t)
	var found []int64
	for i, list := range states {
		for _, s := range list {
			if s == "cancelled" {
				found = append(found, versions[i])
				break
			}
		}
	}
	if len(found) != 1 {
		t.Fatalf("migrations that re-add %s with 'cancelled' = %v, want exactly one (migration A is missing)", jobStateCheck, found)
	}
	return found[0]
}

// priorJobStates is the CHECK of the newest migration before A that names the constraint.
func priorJobStates(t *testing.T) []string {
	t.Helper()
	a := submissionJobsCancelledMigrationVersion(t)
	versions, states := jobStateChecks(t)
	var best int64
	var prior []string
	for i, v := range versions {
		if v < a && v > best {
			best, prior = v, states[i]
		}
	}
	if len(prior) == 0 {
		t.Fatalf("no migration before %d re-adds %s", a, jobStateCheck)
	}
	hasDead := false
	for _, s := range prior {
		if s == "cancelled" {
			t.Fatalf("prior CHECK %v already holds 'cancelled'", prior)
		}
		hasDead = hasDead || s == "dead_lettered"
	}
	if !hasDead {
		t.Fatalf("prior CHECK %v lacks dead_lettered: wrong migration parsed", prior)
	}
	return prior
}

// seedQueuedJob commits a tenant-A job at the default state `queued`.
func seedQueuedJob(t *testing.T, label string) string {
	t.Helper()
	entity, cleanupEntity := seedBusinessEntity(t, h.tenantA, label+" Corp")
	t.Cleanup(cleanupEntity)
	invoice, cleanupInvoice := seedInvoice(t, h.tenantA, entity, label)
	t.Cleanup(cleanupInvoice)
	job, cleanupJob := seedSubmissionJob(t, h.tenantA, invoice, sjKey(label))
	t.Cleanup(cleanupJob)
	return job
}

// setJobState updates the job as invoice_app in its tenant tx; a no-row update is an error, not a pass.
func setJobState(job, state string) error {
	ctx := context.Background()
	return db.WithinTenantTx(ctx, h.app, h.tenantA, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `UPDATE submission_jobs SET state = $1 WHERE id = $2`, state, job)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return errors.New("update touched " + strconv.FormatInt(tag.RowsAffected(), 10) + " rows, want 1")
		}
		return nil
	})
}

func readJobState(t *testing.T, job string) string {
	t.Helper()
	var s string
	if err := h.super.QueryRow(context.Background(), `SELECT state FROM submission_jobs WHERE id = $1`, job).Scan(&s); err != nil {
		t.Fatalf("read job %s state: %v", job, err)
	}
	return s
}

func requireCheckViolation(t *testing.T, what string, err error) {
	t.Helper()
	if err == nil {
		t.Errorf("%s: succeeded, want 23514 on %s", what, jobStateCheck)
		return
	}
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "23514" || pgErr.ConstraintName != jobStateCheck {
		t.Errorf("%s: %v, want 23514 naming %s", what, err, jobStateCheck)
	}
}

func TestRLS_SubmissionJobsCheckAcceptsCancelled(t *testing.T) {
	requireHarness(t)
	job := seedQueuedJob(t, "SUPP-06-01-accept")

	if err := setJobState(job, "cancelled"); err != nil {
		t.Fatalf("UPDATE state = 'cancelled' as invoice_app: %v", err)
	}
	if got := readJobState(t, job); got != "cancelled" {
		t.Errorf("job state after the update = %q, want cancelled", got)
	}
}

func TestRLS_SubmissionJobsCheckKeepsEveryPriorState(t *testing.T) {
	requireHarness(t)
	prior := priorJobStates(t)
	job := seedQueuedJob(t, "SUPP-06-01-prior")

	for _, state := range prior {
		if err := setJobState(job, state); err != nil {
			t.Errorf("UPDATE state = %q: %v, want accepted (a state of the prior CHECK)", state, err)
			continue
		}
		if got := readJobState(t, job); got != state {
			t.Errorf("job state after UPDATE to %q = %q", state, got)
		}
	}
}

func TestRLS_SubmissionJobsCheckRefusesAnUnknownState(t *testing.T) {
	requireHarness(t)
	job := seedQueuedJob(t, "SUPP-06-01-unknown")

	// Positive half: the new state is admitted, so a refuse-everything CHECK cannot pass.
	if err := setJobState(job, "cancelled"); err != nil {
		t.Errorf("UPDATE state = 'cancelled': %v, want accepted", err)
	}
	for _, state := range []string{"canceled", ""} {
		requireCheckViolation(t, "UPDATE state = "+strconv.Quote(state), setJobState(job, state))
	}
}

func TestRLS_SubmissionJobsCancelledDownAndUp(t *testing.T) {
	requireHarness(t)
	version := submissionJobsCancelledMigrationVersion(t)
	prior := priorJobStates(t)
	job := seedQueuedJob(t, "SUPP-06-01-down")

	reapply := rollBackMigrations(t, staffMigrationProvider(t), version)
	requireCheckViolation(t, "after the Down: UPDATE state = 'cancelled'", setJobState(job, "cancelled"))
	for _, state := range prior {
		if err := setJobState(job, state); err != nil {
			t.Errorf("after the Down: UPDATE state = %q: %v, want accepted", state, err)
		}
	}

	reapply()
	if err := setJobState(job, "cancelled"); err != nil {
		t.Errorf("after the Up: UPDATE state = 'cancelled': %v, want accepted", err)
	}
}

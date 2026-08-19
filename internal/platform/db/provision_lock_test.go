// Adversarial coverage for Provision's tail: the advisory lock held across
// reset/purge/seed, and the DemoPurgeOutcome a boot leaves behind when it does
// not complete.
package db_test

import (
	"context"
	"testing"
	"testing/fstest"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	db "github.com/SimonOsipov/invoice-os/internal/platform/db"
)

// brokenSeedFS satisfies cfg.SeedFS but makes Seed fail server-side, so
// Provision returns an error AFTER the tail lock has been taken.
func brokenSeedFS() fstest.MapFS {
	return fstest.MapFS{"seed.dev.sql": &fstest.MapFile{
		Data: []byte(`DO $$ BEGIN RAISE EXCEPTION 'deliberate seed failure'; END $$;`),
	}}
}

// settledBackendCount counts server processes on this database once the count
// has stopped falling: a closed connection's backend exits asynchronously.
func settledBackendCount(t *testing.T, pool *pgxpool.Pool) int {
	t.Helper()
	const q = `SELECT count(*) FROM pg_stat_activity WHERE datname = current_database()`
	last := mustCount(t, pool, q)
	for i := 0; i < 20; i++ {
		time.Sleep(100 * time.Millisecond)
		n := mustCount(t, pool, q)
		if n >= last {
			return n
		}
		last = n
	}
	return last
}

// TestProvisionReleasesTailLockWhenSeedFails: the tail lock must not survive a
// failing boot. A leaked advisory lock on a dedicated connection blocks every
// later boot of every replica.
func TestProvisionReleasesTailLockWhenSeedFails(t *testing.T) {
	superDSN, migDSN := requireProvisionDSNs(t)
	ctx := context.Background()
	pool := bootstrapSuperuserPool(t, superDSN)
	restoreCuratedDemoState(t, superDSN)
	seedBaseline(t, superDSN)

	cfg := productionShapedProvisionConfig(superDSN, migDSN)
	cfg.SeedFS = brokenSeedFS()

	if err := db.Provision(ctx, cfg); err == nil {
		t.Fatal("Provision with a deliberately broken seed returned nil, want an error")
	}
	// The purge precedes Seed, so this proves the lock was actually taken and
	// the assertions below are not about a lock that never existed.
	if got := db.DemoPurgeOutcome; got != db.DemoPurgeRan {
		t.Fatalf("db.DemoPurgeOutcome = %q, want %q — the boot never reached the purge, so the tail lock was never held", got, db.DemoPurgeRan)
	}

	if advisoryKeyGranted(t, pool, db.BootstrapAdvisoryLockKey) {
		t.Errorf("pg_locks still shows advisory key %d granted after Provision failed at Seed — the tail lock leaked", db.BootstrapAdvisoryLockKey)
	}
	acquireAdvisoryLockRoundTrip(t, pool, db.BootstrapAdvisoryLockKey)

	// The guarantee that matters: the next boot is not blocked by the failed one.
	if err := db.Provision(ctx, productionShapedProvisionConfig(superDSN, migDSN)); err != nil {
		t.Fatalf("the boot after a failed one: %v — a leaked tail lock would hang here instead", err)
	}
}

// TestProvisionTailLockConnectionIsClosed: lockProvisionTail opens a fifth
// superuser connection. Repeated boots must not accumulate them.
func TestProvisionTailLockConnectionIsClosed(t *testing.T) {
	superDSN, migDSN := requireProvisionDSNs(t)
	ctx := context.Background()
	pool := bootstrapSuperuserPool(t, superDSN)
	restoreCuratedDemoState(t, superDSN)

	cfg := productionShapedProvisionConfig(superDSN, migDSN)
	if err := db.Provision(ctx, cfg); err != nil {
		t.Fatalf("warm-up Provision: %v", err)
	}
	before := settledBackendCount(t, pool)

	const boots = 3
	for i := 0; i < boots; i++ {
		if err := db.Provision(ctx, cfg); err != nil {
			t.Fatalf("Provision %d: %v", i, err)
		}
	}

	if after := settledBackendCount(t, pool); after > before {
		t.Errorf("backends on this database went from %d to %d across %d boots — Provision leaks a connection per boot", before, after, boots)
	}
}

// TestProvisionTailLockRespectsContextDeadline: an unrelated session holding
// BootstrapAdvisoryLockKey (a crashed replica, an operator's psql) must not make
// the tail block past the caller's deadline, and the connection opened to wait
// on it must be closed when the wait gives up.
func TestProvisionTailLockRespectsContextDeadline(t *testing.T) {
	superDSN := requireSuperuserDSN(t)
	pool := bootstrapSuperuserPool(t, superDSN)

	holder, err := pgx.Connect(context.Background(), superDSN)
	if err != nil {
		t.Fatalf("connect lock-holder session: %v", err)
	}
	defer holder.Close(context.Background())
	if _, err := holder.Exec(context.Background(),
		`SELECT pg_advisory_lock($1)`, db.BootstrapAdvisoryLockKey); err != nil {
		t.Fatalf("holder acquire advisory lock: %v", err)
	}
	defer func() {
		if _, err := holder.Exec(context.Background(),
			`SELECT pg_advisory_unlock($1)`, db.BootstrapAdvisoryLockKey); err != nil {
			t.Errorf("release holder's advisory lock: %v", err)
		}
	}()

	before := settledBackendCount(t, pool)

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	start := time.Now()
	unlock, err := db.LockProvisionTailForTest(ctx, superDSN)
	elapsed := time.Since(start)
	if err == nil {
		unlock()
		t.Fatal("lockProvisionTail succeeded while an unrelated session held the key throughout, want a deadline error")
	}
	if elapsed > 8*time.Second {
		t.Fatalf("lockProvisionTail took %s to give up under a 3s deadline — ctx is not plumbed to the pg_advisory_lock call", elapsed)
	}
	if after := settledBackendCount(t, pool); after > before {
		t.Errorf("backends went from %d to %d after a failed lock acquisition — the waiting connection was not closed", before, after)
	}
}

// TestProvisionFailedPurgeLeavesUnseededDemoRowsIntact: the whole-transaction
// rollback, measured on a row Seed cannot put back. Every demo row the existing
// coverage counts is seed-derived, so there a purge that committed part of its
// work before failing is indistinguishable from one that rolled back.
func TestProvisionFailedPurgeLeavesUnseededDemoRowsIntact(t *testing.T) {
	superDSN, migDSN := requireProvisionDSNs(t)
	ctx := context.Background()
	pool := bootstrapSuperuserPool(t, superDSN)
	restoreCuratedDemoState(t, superDSN)
	seedBaseline(t, superDSN)

	witness := plantDemoResidue(t, pool, "rollback-witness")
	if n := countEntityTIN(t, pool, demoTenantID, witness); n != 1 {
		t.Fatalf("witness row count before Provision = %d, want 1", n)
	}

	// invitations is purged after business_entities and never written by Seed.
	holdAccessExclusive(t, superDSN, "invitations")

	if err := db.Provision(ctx, productionShapedProvisionConfig(superDSN, migDSN)); err != nil {
		t.Fatalf("Provision: %v — a purge failure must not abort the boot", err)
	}
	if got := db.DemoPurgeOutcome; got != db.DemoPurgeErrored {
		t.Fatalf("db.DemoPurgeOutcome = %q, want %q — the purge did not fail, so the rollback is untested", got, db.DemoPurgeErrored)
	}
	if n := countEntityTIN(t, pool, demoTenantID, witness); n != 1 {
		t.Errorf("unseeded demo row (tin=%s) count after a FAILED purge = %d, want 1 — the purge committed part of its work", witness, n)
	}
}

// TestProvisionResetAndPurgeErrorInOneBoot: both destructive steps fire in one
// boot and the purge is the one that fails. Reset and Seed must still have run.
func TestProvisionResetAndPurgeErrorInOneBoot(t *testing.T) {
	superDSN, migDSN := requireProvisionDSNs(t)
	ctx := context.Background()
	pool := bootstrapSuperuserPool(t, superDSN)
	restoreCuratedDemoState(t, superDSN)
	seedBaseline(t, superDSN)

	// Only Reset's global TRUNCATE can remove a row on this tenant.
	probeTenant := newNonDemoProbeTenant(t, pool, "reset-and-purge-error")
	probeTIN := "33333333-" + uuid.NewString()[:4]
	if _, err := pool.Exec(ctx,
		`INSERT INTO business_entities (tenant_id, name, tin) VALUES ($1, $2, $3)`,
		probeTenant, "reset witness", probeTIN,
	); err != nil {
		t.Fatalf("insert reset witness (precondition): %v", err)
	}

	holdAccessExclusive(t, superDSN, "invitations")

	cfg := productionShapedProvisionConfig(superDSN, migDSN)
	cfg.RailwayEnvironmentName = "pr-111"
	cfg.ResetFlag = "true"
	if !cfg.ResetWillRun() {
		t.Fatal("test setup: ResetWillRun() is false, so Reset would not fire and this case proves nothing")
	}
	if err := db.Provision(ctx, cfg); err != nil {
		t.Fatalf("Provision (reset on, purge failing): %v", err)
	}

	if got := db.DemoPurgeOutcome; got != db.DemoPurgeErrored {
		t.Errorf("db.DemoPurgeOutcome = %q, want %q", got, db.DemoPurgeErrored)
	}
	if n := countEntityTIN(t, pool, probeTenant, probeTIN); n != 0 {
		t.Errorf("reset witness count after Provision = %d, want 0 — a failing purge must not stop Reset from having run", n)
	}
	if entities := fetchDemoBusinessEntities(t, pool, demoTenantID); len(entities) != 10 {
		t.Errorf("curated business_entities = %d, want 10 — Seed must still run after a reset and a failed purge", len(entities))
	}
}

// TestProvisionPurgeOutcomeIsRanWhenItDeletesNothing: the field reports the
// purge's success, not its row count. Demo tenants with no rows still read "true".
func TestProvisionPurgeOutcomeIsRanWhenItDeletesNothing(t *testing.T) {
	superDSN, migDSN := requireProvisionDSNs(t)
	ctx := context.Background()
	restoreCuratedDemoState(t, superDSN)
	seedBaseline(t, superDSN)

	if err := db.Reset(ctx, superDSN); err != nil {
		t.Fatalf("Reset: %v", err)
	}
	if _, err := db.PurgeDemoTenants(ctx, superDSN); err != nil {
		t.Fatalf("first purge: %v", err)
	}
	empty, err := db.PurgeDemoTenants(ctx, superDSN)
	if err != nil {
		t.Fatalf("second purge: %v", err)
	}
	if empty.Rows != 0 || len(empty.ByTable) != 0 {
		t.Fatalf("the demo tenants still own %d row(s) across %d table(s) — this case needs an empty starting state", empty.Rows, len(empty.ByTable))
	}

	cfg := productionShapedProvisionConfig(superDSN, migDSN)
	cfg.RailwayEnvironmentName = "pr-112"
	cfg.ResetFlag = "true" // Reset keeps the tables empty up to the purge.
	if err := db.Provision(ctx, cfg); err != nil {
		t.Fatalf("Provision over an empty demo set: %v", err)
	}
	if got := db.DemoPurgeOutcome; got != db.DemoPurgeRan {
		t.Errorf("db.DemoPurgeOutcome = %q after a purge that deleted nothing, want %q", got, db.DemoPurgeRan)
	}
}

// TestProvisionPurgeOutcomeAfterAFailedBoot pins what the field reads when the
// boot did not complete: "false" if the failure preceded the purge, "true" if it
// followed a successful one.
func TestProvisionPurgeOutcomeAfterAFailedBoot(t *testing.T) {
	superDSN, migDSN := requireProvisionDSNs(t)
	ctx := context.Background()
	restoreCuratedDemoState(t, superDSN)
	seedBaseline(t, superDSN)

	t.Run("failure before the purge", func(t *testing.T) {
		cfg := productionShapedProvisionConfig(superDSN, migDSN)
		cfg.MigrationDSN = migrationPoisonDSN
		if err := db.Provision(ctx, cfg); err == nil {
			t.Fatal("Provision with a poisoned migration DSN returned nil, want an error")
		}
		if got := db.DemoPurgeOutcome; got != db.DemoPurgeSkipped {
			t.Errorf("db.DemoPurgeOutcome = %q after a boot that failed before the purge, want %q", got, db.DemoPurgeSkipped)
		}
	})

	t.Run("failure after the purge", func(t *testing.T) {
		cfg := productionShapedProvisionConfig(superDSN, migDSN)
		cfg.SeedFS = brokenSeedFS()
		if err := db.Provision(ctx, cfg); err == nil {
			t.Fatal("Provision with a broken seed returned nil, want an error")
		}
		if got := db.DemoPurgeOutcome; got != db.DemoPurgeRan {
			t.Errorf("db.DemoPurgeOutcome = %q after a boot that failed after a successful purge, want %q", got, db.DemoPurgeRan)
		}
	})
}

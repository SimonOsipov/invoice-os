package erp

import (
	"context"
	"encoding/json"
	"slices"
	"sort"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/SimonOsipov/invoice-os/internal/platform/db"
)

// seedRiverJob inserts a job row as the superuser; a finished state gets its finalized_at.
func seedRiverJob(t *testing.T, super *pgxpool.Pool, kind, state string, args map[string]string) {
	t.Helper()
	raw, err := json.Marshal(args)
	if err != nil {
		t.Fatal(err)
	}
	mustExec(t, super, `INSERT INTO river_job (state, max_attempts, finalized_at, args, kind)
		VALUES ($1::text::river_job_state, 1, CASE WHEN $1::text IN ('completed', 'cancelled', 'discarded') THEN now() END, $2::jsonb, $3)`, state, string(raw), kind)
}

func refsOf(t *testing.T, app *pgxpool.Pool, tenant string) []string {
	t.Helper()
	var refs []string
	err := db.WithinTenantTx(context.Background(), app, tenant, func(tx pgx.Tx) error {
		var err error
		refs, err = TenantSecretRefs(context.Background(), tx, tenant)
		return err
	})
	if err != nil {
		t.Fatalf("TenantSecretRefs(%s): %v", tenant, err)
	}
	return refs
}

func TestTenantSecretRefs_OnlyTheBoundTenant(t *testing.T) {
	super, app := storePools(t)
	a, b, empty := seedTenant(t, super, "firm"), seedTenant(t, super, "firm"), seedTenant(t, super, "firm")
	t.Cleanup(func() {
		_, _ = super.Exec(context.Background(), "DELETE FROM river_job WHERE args->>'tenant_id' = ANY($1::text[])", []string{a, b})
	})
	name := func() string { return SecretName("", uuid.NewString()) }
	aGrant, aReleased, aOther, bGrant, bJob := name(), name(), name(), name(), name()

	seedGrant(t, super, a, aGrant)
	seedGrant(t, super, a, "") // NULL secret_ref
	seedGrant(t, super, b, bGrant)
	del := func(tenant, ref string) map[string]string {
		return map[string]string{"tenant_id": tenant, "secret_ref": ref, "connection_id": uuid.NewString(), "system": "fake"}
	}
	seedRiverJob(t, super, "erp_secret_delete", "available", del(a, aGrant))    // repeats a grant's ref
	seedRiverJob(t, super, "erp_secret_delete", "completed", del(a, aReleased)) // any state: a finished job's secret answers NOT_FOUND
	seedRiverJob(t, super, "erp_secret_delete", "available", del(b, bJob))
	seedRiverJob(t, super, "erp_token_refresh", "available", del(a, aOther)) // another kind

	got := refsOf(t, app, a)
	want := []string{aGrant, aReleased}
	sort.Strings(want)
	if len(got) == 0 || !slices.Equal(got, want) {
		t.Errorf("refs of A = %v, want exactly %v (sorted, distinct, no other tenant, no other kind, no NULL)", got, want)
	}
	gotB := refsOf(t, app, b)
	wantB := []string{bGrant, bJob}
	sort.Strings(wantB)
	if len(gotB) == 0 || !slices.Equal(gotB, wantB) {
		t.Errorf("refs of B = %v, want exactly %v", gotB, wantB)
	}
	if got := refsOf(t, app, empty); len(got) != 0 {
		t.Errorf("refs of a tenant with no grant or job = %v, want none", got)
	}
}

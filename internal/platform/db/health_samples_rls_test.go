package db_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"math/rand/v2"
	"net/url"
	"os"
	"reflect"
	"slices"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/SimonOsipov/invoice-os/migrations"
)

const (
	healthSamplesTable = "public.health_samples"
	samplerCountSig    = "public.submission_job_state_counts()"
	staffCountSig      = "public.staff_read_submission_job_counts(uuid)"
	staffSamplesSig    = "public.staff_read_health_samples(uuid, timestamp with time zone)"
)

var healthFunctionSigs = []string{samplerCountSig, staffCountSig, staffSamplesSig}

const staffRiverCountsSig = "public.staff_read_river_job_counts(uuid)"

// healthFunctionSigsWithRiver adds the per-kind River count; the older tests keep the three they were written for.
var healthFunctionSigsWithRiver = append(slices.Clone(healthFunctionSigs), staffRiverCountsSig)

// jobStates are the submission_jobs states a count key may name.
var jobStates = map[string]bool{
	"queued": true, "submitting": true, "pending": true, "accepted": true,
	"rejected": true, "failed": true, "dead_lettered": true, "cancelled": true,
}

func requireHealthSamples(t *testing.T) {
	t.Helper()
	var reg *string
	if err := h.super.QueryRow(context.Background(), `SELECT to_regclass($1)::text`, healthSamplesTable).Scan(&reg); err != nil {
		t.Fatalf("read to_regclass: %v", err)
	}
	if reg == nil {
		t.Fatalf("table %s not found: the health_samples migration is not applied", healthSamplesTable)
	}
}

func requireHealthFunctions(t *testing.T) {
	t.Helper()
	for _, sig := range healthFunctionSigs {
		var reg *string
		if err := h.super.QueryRow(context.Background(), `SELECT to_regprocedure($1)::text`, sig).Scan(&reg); err != nil {
			t.Fatalf("read to_regprocedure %s: %v", sig, err)
		}
		if reg == nil {
			t.Fatalf("function %s not found: the health_samples migration is not applied", sig)
		}
	}
}

// healthSlots returns n consecutive 5-minute slots far from now, unique per call, and deletes them on cleanup.
func healthSlots(t *testing.T, n int) []time.Time {
	t.Helper()
	base := time.Date(2001, 1, 1, 0, 0, 0, 0, time.UTC).Add(time.Duration(rand.IntN(200000)) * 5 * time.Minute)
	slots := make([]time.Time, n)
	for i := range slots {
		slots[i] = base.Add(time.Duration(i) * 5 * time.Minute)
	}
	t.Cleanup(func() {
		_, _ = h.super.Exec(context.Background(), `DELETE FROM public.health_samples WHERE taken_at = ANY($1)`, slots)
	})
	return slots
}

const kindCountsColumn = `SELECT data_type, is_nullable, column_default FROM information_schema.columns
	WHERE table_schema = 'public' AND table_name = 'health_samples' AND column_name = 'kind_counts'`

// requireKindCountsColumn fails the test when the per-kind migration is not applied.
func requireKindCountsColumn(t *testing.T) {
	t.Helper()
	var n int
	if err := h.super.QueryRow(context.Background(),
		`SELECT count(*) FROM information_schema.columns WHERE table_schema = 'public' AND table_name = 'health_samples' AND column_name = 'kind_counts'`).Scan(&n); err != nil {
		t.Fatalf("read health_samples columns: %v", err)
	}
	if n != 1 {
		t.Fatal("column public.health_samples.kind_counts not found: the per-kind health_samples migration is not applied")
	}
}

func requireRiverCountsFunction(t *testing.T) {
	t.Helper()
	var reg *string
	if err := h.super.QueryRow(context.Background(), `SELECT to_regprocedure($1)::text`, staffRiverCountsSig).Scan(&reg); err != nil {
		t.Fatalf("read to_regprocedure %s: %v", staffRiverCountsSig, err)
	}
	if reg == nil {
		t.Fatalf("function %s not found: the per-kind health_samples migration is not applied", staffRiverCountsSig)
	}
}

func healthInsertSampleKinds(t *testing.T, slot time.Time, kinds string) {
	t.Helper()
	if _, err := h.super.Exec(context.Background(),
		`INSERT INTO public.health_samples (taken_at, fleet_status, services_down, job_counts, kind_counts) VALUES ($1, 'ok', 0, '{}'::jsonb, $2::jsonb)`,
		slot, kinds); err != nil {
		t.Fatalf("seed sample %s with kind_counts: %v", slot.Format(time.RFC3339), err)
	}
}

func healthInsertSample(t *testing.T, slot time.Time, status string, down any, counts string) {
	t.Helper()
	if _, err := h.super.Exec(context.Background(),
		`INSERT INTO public.health_samples (taken_at, fleet_status, services_down, job_counts) VALUES ($1, $2, $3, $4::jsonb)`,
		slot, status, down, counts); err != nil {
		t.Fatalf("seed sample %s: %v", slot.Format(time.RFC3339), err)
	}
}

// healthRepeatableReadTx is a superuser REPEATABLE READ tx that always rolls back.
func healthRepeatableReadTx(t *testing.T) pgx.Tx {
	t.Helper()
	tx, err := h.super.BeginTx(context.Background(), pgx.TxOptions{IsoLevel: pgx.RepeatableRead})
	if err != nil {
		t.Fatalf("begin repeatable read: %v", err)
	}
	t.Cleanup(func() {
		if e := tx.Rollback(context.Background()); e != nil && !errors.Is(e, pgx.ErrTxClosed) {
			t.Errorf("rollback: %v", e)
		}
	})
	return tx
}

// healthSeedJobTx inserts an entity, an invoice and one job in the given state for tenant, inside tx.
func healthSeedJobTx(t *testing.T, tx pgx.Tx, tenant, state string) {
	t.Helper()
	entity, invoice := uuid.NewString(), uuid.NewString()
	txExec(t, tx, `INSERT INTO business_entities (id, tenant_id, name) VALUES ($1, $2, $3)`, entity, tenant, "entity-"+entity[:8])
	txExec(t, tx, `INSERT INTO invoices (id, tenant_id, entity_id, invoice_number, status, created_at) VALUES ($1, $2, $3, $4, 'draft', now())`,
		invoice, tenant, entity, "HS-"+invoice[:8])
	txExec(t, tx, `INSERT INTO submission_jobs (tenant_id, invoice_id, idempotency_key, adapter, adapter_version, state) VALUES ($1, $2, $3, 'mock', 'v1', $4)`,
		tenant, invoice, uuid.NewString(), state)
}

func healthDecodeCounts(t *testing.T, what string, raw []byte) map[string]int64 {
	t.Helper()
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var obj map[string]any
	if err := dec.Decode(&obj); err != nil || obj == nil {
		t.Fatalf("%s: result %q is not one JSON object: %v", what, raw, err)
	}
	out := map[string]int64{}
	for k, v := range obj {
		n, ok := v.(json.Number)
		if !ok {
			t.Fatalf("%s: key %q holds %T %v, want an integer", what, k, v, v)
		}
		i, err := n.Int64()
		if err != nil {
			t.Fatalf("%s: key %q holds %s, want an integer: %v", what, k, n, err)
		}
		out[k] = i
	}
	return out
}

func healthCallCounts(t *testing.T, tx pgx.Tx, what, sql string, args ...any) map[string]int64 {
	t.Helper()
	var raw []byte
	if err := tx.QueryRow(context.Background(), sql, args...).Scan(&raw); err != nil {
		t.Fatalf("%s: %v", what, err)
	}
	return healthDecodeCounts(t, what, raw)
}

func healthGroupByState(t *testing.T, tx pgx.Tx) map[string]int64 {
	t.Helper()
	rows, err := tx.Query(context.Background(), `SELECT state, count(*) FROM submission_jobs GROUP BY state`)
	if err != nil {
		t.Fatalf("superuser GROUP BY state: %v", err)
	}
	defer rows.Close()
	out := map[string]int64{}
	for rows.Next() {
		var s string
		var n int64
		if err := rows.Scan(&s, &n); err != nil {
			t.Fatalf("scan: %v", err)
		}
		out[s] = n
	}
	return out
}

func TestRLS_HealthSamplesKeepOneRowPerSlot(t *testing.T) {
	requireHarness(t)
	requireHealthSamples(t)
	ctx := context.Background()
	slot := healthSlots(t, 1)[0]
	const insert = `INSERT INTO public.health_samples (taken_at, fleet_status, services_down, job_counts) VALUES ($1, $2, $3, '{}'::jsonb) ON CONFLICT (taken_at) DO NOTHING`

	first, err := h.app.Exec(ctx, insert, slot, "ok", 0)
	if err != nil {
		t.Fatalf("invoice_app first insert: %v", err)
	}
	if first.RowsAffected() != 1 {
		t.Errorf("first insert affected %d rows, want 1", first.RowsAffected())
	}
	second, err := h.app.Exec(ctx, insert, slot, "degraded", 2)
	if err != nil {
		t.Fatalf("invoice_app second insert: %v", err)
	}
	if second.RowsAffected() != 0 {
		t.Errorf("second insert for the same slot affected %d rows, want 0", second.RowsAffected())
	}

	var n, down int
	var status string
	if err := h.super.QueryRow(ctx,
		`SELECT count(*), min(fleet_status), min(services_down) FROM public.health_samples WHERE taken_at = $1`, slot).Scan(&n, &status, &down); err != nil {
		t.Fatalf("read the slot: %v", err)
	}
	if n != 1 || status != "ok" || down != 0 {
		t.Errorf("slot holds %d rows (%s, %d down), want one row (ok, 0)", n, status, down)
	}
}

func TestRLS_HealthSamplesRefuseAnInconsistentRow(t *testing.T) {
	requireHarness(t)
	requireHealthSamples(t)
	tx := revokeProbeTx(t)
	slots := healthSlots(t, 6)
	const insert = `INSERT INTO public.health_samples (taken_at, fleet_status, services_down, job_counts) VALUES ($1, $2, $3, '{}'::jsonb)`

	txExec(t, tx, insert, slots[0], "unknown", nil)
	txExec(t, tx, insert, slots[1], "degraded", 1)
	if n := mustCount(t, tx, `SELECT count(*) FROM public.health_samples WHERE taken_at = ANY($1)`, slots[:2]); n != 2 {
		t.Fatalf("control: %d of the 2 consistent rows stored, want 2", n)
	}

	for _, c := range []struct {
		what, constraint, status string
		down                     any
		slot                     time.Time
	}{
		{"status outside the set", "health_samples_fleet_status_check", "up", 0, slots[2]},
		{"unknown with a count", "health_samples_down_known", "unknown", 0, slots[3]},
		{"known with no count", "health_samples_down_known", "ok", nil, slots[4]},
		{"negative count", "health_samples_services_down_check", "ok", -1, slots[5]},
	} {
		refusedBy(t, tx, c.what, "23514", c.constraint, insert, c.slot, c.status, c.down)
	}
}

func TestRLS_SubmissionJobStateCountsCountEveryTenant(t *testing.T) {
	requireHarness(t)
	requireHealthFunctions(t)
	tx := healthRepeatableReadTx(t)

	healthSeedJobTx(t, tx, h.tenantA, "queued")
	healthSeedJobTx(t, tx, h.tenantB, "dead_lettered")
	healthSeedJobTx(t, tx, h.tenantB, "cancelled")
	txExec(t, tx, `SELECT set_config('app.current_tenant', '', true)`)

	ruleDraftAsApp(t, tx)
	got := healthCallCounts(t, tx, "invoice_app with no tenant set calls submission_job_state_counts()", `SELECT public.submission_job_state_counts()`)
	ruleDraftAsOwner(t, tx)
	want := healthGroupByState(t, tx)

	if got["queued"] < 1 || got["dead_lettered"] < 1 || got["cancelled"] < 1 {
		t.Errorf("counts = %v, want queued, dead_lettered and cancelled each >= 1 (tenant A's and tenant B's seeded jobs)", got)
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("submission_job_state_counts() = %v, superuser GROUP BY state in the same snapshot = %v, want equal", got, want)
	}
}

func TestRLS_SubmissionJobStateCountsCarryNoRowData(t *testing.T) {
	requireHarness(t)
	requireHealthFunctions(t)
	tx := healthRepeatableReadTx(t)
	healthSeedJobTx(t, tx, h.tenantA, "queued")
	txExec(t, tx, `SELECT set_config('app.current_tenant', '', true)`)

	ruleDraftAsApp(t, tx)
	got := healthCallCounts(t, tx, "submission_job_state_counts()", `SELECT public.submission_job_state_counts()`)
	if len(got) == 0 {
		t.Fatal("counts are empty with a seeded job, want at least the queued key")
	}
	for k, v := range got {
		if _, err := uuid.Parse(k); err == nil {
			t.Errorf("key %q is a UUID, want a state name", k)
		}
		if !jobStates[k] {
			t.Errorf("key %q is not a submission_jobs state", k)
		}
		if v < 1 {
			t.Errorf("key %q holds %d, want a count >= 1 (an absent state has no key)", k, v)
		}
	}
	if got["queued"] < 1 {
		t.Errorf("counts = %v, want queued >= 1", got)
	}
}

func TestRLS_StaffReadFunctionsIncludeTheHealthReads(t *testing.T) {
	requireHarness(t)
	fns := requireStaffReadFunctions(t)
	byName := map[string]staffReadFn{}
	for _, fn := range fns {
		byName[fn.name] = fn
	}
	for name, argTypes := range map[string][]string{
		"staff_read_submission_job_counts": {"uuid"},
		"staff_read_health_samples":        {"uuid", "timestamp with time zone"},
		"staff_read_river_job_counts":      {"uuid"},
	} {
		fn, ok := byName[name]
		if !ok {
			t.Errorf("public.%s is not in the staff_read_ catalog, want it", name)
			continue
		}
		if !reflect.DeepEqual(fn.argTypes, argTypes) {
			t.Errorf("%s arguments = %v, want %v", name, fn.argTypes, argTypes)
		}
	}
}

func TestRLS_StaffReadSubmissionJobCountsMatchesTheSamplerCount(t *testing.T) {
	requireHarness(t)
	requireHealthFunctions(t)
	house := adoptHouseTenant(t)
	tx := healthRepeatableReadTx(t)
	member := seedMembershipTx(t, tx, house, "reviewer", "active")
	healthSeedJobTx(t, tx, h.tenantA, "queued")
	healthSeedJobTx(t, tx, h.tenantB, "dead_lettered")
	healthSeedJobTx(t, tx, h.tenantB, "cancelled")

	ruleDraftAsApp(t, tx)
	sampler := healthCallCounts(t, tx, "submission_job_state_counts()", `SELECT public.submission_job_state_counts()`)
	staff := healthCallCounts(t, tx, "staff_read_submission_job_counts(house member)", `SELECT public.staff_read_submission_job_counts($1::uuid)`, member)

	if sampler["queued"] < 1 || sampler["dead_lettered"] < 1 || sampler["cancelled"] < 1 {
		t.Errorf("sampler counts = %v, want queued, dead_lettered and cancelled each >= 1", sampler)
	}
	if !reflect.DeepEqual(staff, sampler) {
		t.Errorf("staff_read_submission_job_counts = %v, submission_job_state_counts() = %v, want equal", staff, sampler)
	}
}

type healthSampleRow struct {
	takenAt  time.Time
	status   string
	down     *int
	jobCount string
}

func healthReadSamples(t *testing.T, actor, since any) []healthSampleRow {
	t.Helper()
	rows, err := h.app.Query(context.Background(),
		`SELECT taken_at, fleet_status, services_down, job_counts::text FROM public.staff_read_health_samples($1::uuid, $2::timestamptz)`, actor, since)
	if err != nil {
		t.Fatalf("staff_read_health_samples: %v", err)
	}
	out, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (healthSampleRow, error) {
		var s healthSampleRow
		return s, r.Scan(&s.takenAt, &s.status, &s.down, &s.jobCount)
	})
	if err != nil {
		t.Fatalf("collect staff_read_health_samples: %v", err)
	}
	return out
}

func TestRLS_StaffReadHealthSamplesReturnsTheWindowOldestFirst(t *testing.T) {
	requireHarness(t)
	requireHealthSamples(t)
	requireHealthFunctions(t)
	member := seedHouseMember(t, "reviewer", "active")
	s := healthSlots(t, 3)
	// Inserted out of order: the read, not insertion order, sorts them.
	healthInsertSample(t, s[2], "unknown", nil, `{}`)
	healthInsertSample(t, s[0], "ok", 0, `{"queued": 1}`)
	healthInsertSample(t, s[1], "degraded", 2, `{"queued": 3}`)

	got := healthReadSamples(t, member, s[1])
	var own []healthSampleRow
	for i, r := range got {
		if i > 0 && r.takenAt.Before(got[i-1].takenAt) {
			t.Errorf("row %d (%s) is older than row %d (%s), want ascending taken_at", i, r.takenAt, i-1, got[i-1].takenAt)
		}
		if r.takenAt.Equal(s[0]) {
			t.Errorf("slot %s is before p_since and is returned", s[0])
		}
		for _, slot := range s {
			if r.takenAt.Equal(slot) {
				own = append(own, r)
			}
		}
	}
	if len(own) != 2 || !own[0].takenAt.Equal(s[1]) || !own[1].takenAt.Equal(s[2]) {
		t.Fatalf("the test's slots in the result = %+v, want [%s, %s] in that order", own, s[1], s[2])
	}
	if own[0].status != "degraded" || own[0].down == nil || *own[0].down != 2 || !strings.Contains(own[0].jobCount, `"queued": 3`) {
		t.Errorf("slot %s = %+v, want (degraded, 2, {queued: 3})", s[1], own[0])
	}
	if own[1].status != "unknown" || own[1].down != nil {
		t.Errorf("slot %s = %+v, want (unknown, NULL)", s[2], own[1])
	}

	if rows := healthReadSamples(t, member, nil); len(rows) != 0 {
		t.Errorf("p_since NULL returned %d rows, want 0", len(rows))
	}
}

// healthColumnGrants maps column to its sorted privileges for grantee on table. grantee "PUBLIC" reads ACL id 0.
func healthColumnGrants(t *testing.T, grantee, table string) []string {
	t.Helper()
	return collectStrings(t,
		`SELECT a.attname || '/' || x.privilege_type
		   FROM pg_attribute a, aclexplode(a.attacl) x
		  WHERE a.attrelid = to_regclass($2) AND a.attnum > 0 AND NOT a.attisdropped
		    AND x.grantee = CASE WHEN $1 = 'PUBLIC' THEN 0::oid ELSE (SELECT oid FROM pg_roles WHERE rolname = $1) END
		  ORDER BY 1`, grantee, table)
}

func healthTableGrants(t *testing.T, grantee, table string) []string {
	t.Helper()
	return collectStrings(t,
		`SELECT x.privilege_type FROM pg_class c, aclexplode(c.relacl) x
		  WHERE c.oid = to_regclass($2)
		    AND x.grantee = CASE WHEN $1 = 'PUBLIC' THEN 0::oid ELSE (SELECT oid FROM pg_roles WHERE rolname = $1) END
		  ORDER BY 1`, grantee, table)
}

func healthPolicies(t *testing.T, role, table string) []string {
	t.Helper()
	return collectStrings(t,
		`SELECT policyname || '/' || cmd || '/' || coalesce(qual, '') || '/' || roles::text FROM pg_policies
		  WHERE schemaname || '.' || tablename = $2 AND $1 = ANY (roles) ORDER BY 1`, role, table)
}

// The staff_read_submission_jobs migration's grants; the health_samples migration adds none.
var staffReaderJobGrants = []string{"attempts/SELECT", "created_at/SELECT", "id/SELECT", "invoice_id/SELECT", "last_error/SELECT", "poll_ref/SELECT", "state/SELECT", "tenant_id/SELECT", "updated_at/SELECT"}

func TestRLS_HealthSamplesPrivileges(t *testing.T) {
	requireHarness(t)
	ctx := context.Background()
	requireHealthSamples(t)
	requireHealthFunctions(t)
	const jobs = "public.submission_jobs"

	check := func(what string, got, want []string) {
		t.Helper()
		if len(got) == 0 {
			got = nil
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("%s = %v, want %v", what, got, want)
		}
	}

	check("invoice_app table privileges on health_samples", healthTableGrants(t, "invoice_app", healthSamplesTable), []string{"DELETE", "INSERT"})
	check("invoice_app column privileges on health_samples", healthColumnGrants(t, "invoice_app", healthSamplesTable), []string{"taken_at/SELECT"})
	check("PUBLIC table privileges on health_samples", healthTableGrants(t, "PUBLIC", healthSamplesTable), nil)
	check("PUBLIC column privileges on health_samples", healthColumnGrants(t, "PUBLIC", healthSamplesTable), nil)
	check("PUBLIC table privileges on submission_jobs", healthTableGrants(t, "PUBLIC", jobs), nil)
	check("PUBLIC column privileges on submission_jobs", healthColumnGrants(t, "PUBLIC", jobs), nil)

	check("auth_hook_reader table privileges on submission_jobs", healthTableGrants(t, "auth_hook_reader", jobs), nil)
	check("auth_hook_reader column privileges on submission_jobs", healthColumnGrants(t, "auth_hook_reader", jobs), []string{"state/SELECT"})
	check("auth_hook_reader policies on submission_jobs", healthPolicies(t, "auth_hook_reader", jobs),
		[]string{"health_sample_count/SELECT/true/{auth_hook_reader}"})
	check("staff_reader table privileges on submission_jobs", healthTableGrants(t, "staff_reader", jobs), nil)
	check("staff_reader column privileges on submission_jobs", healthColumnGrants(t, "staff_reader", jobs), staffReaderJobGrants)
	check("staff_reader policies on submission_jobs", healthPolicies(t, "staff_reader", jobs),
		[]string{"staff_read_lookup/SELECT/true/{staff_reader}"})
	check("staff_reader table privileges on health_samples", healthTableGrants(t, "staff_reader", healthSamplesTable), nil)
	check("staff_reader column privileges on health_samples", healthColumnGrants(t, "staff_reader", healthSamplesTable),
		[]string{"fleet_status/SELECT", "job_counts/SELECT", "kind_counts/SELECT", "services_down/SELECT", "taken_at/SELECT"})
	check("staff_reader policies on health_samples", healthPolicies(t, "staff_reader", healthSamplesTable), nil)

	check("staff_reader table privileges on river_job", healthTableGrants(t, "staff_reader", "public.river_job"), nil)
	check("staff_reader column privileges on river_job", healthColumnGrants(t, "staff_reader", "public.river_job"), []string{"kind/SELECT", "state/SELECT"})
	check("staff_reader policies on river_job", healthPolicies(t, "staff_reader", "public.river_job"), nil)

	var rls bool
	if err := h.super.QueryRow(ctx, `SELECT relrowsecurity FROM pg_class WHERE oid = $1::regclass`, healthSamplesTable).Scan(&rls); err != nil {
		t.Fatalf("read relrowsecurity: %v", err)
	}
	if rls {
		t.Error("health_samples has row-level security, want none (a global table like staff_audit_log)")
	}

	requireRiverCountsFunction(t)
	for _, sig := range healthFunctionSigsWithRiver {
		var app, public bool
		if err := h.super.QueryRow(ctx,
			`SELECT has_function_privilege('invoice_app', $1::regprocedure, 'EXECUTE'),
			        p.proacl IS NULL OR EXISTS (SELECT 1 FROM aclexplode(p.proacl) a WHERE a.grantee = 0)
			   FROM pg_proc p WHERE p.oid = $1::regprocedure`, sig).Scan(&app, &public); err != nil {
			t.Fatalf("read EXECUTE on %s: %v", sig, err)
		}
		if !app {
			t.Errorf("%s: invoice_app lacks EXECUTE, want it", sig)
		}
		if public {
			t.Errorf("%s: PUBLIC holds EXECUTE, want it revoked", sig)
		}
	}

	var owner string
	var config []string
	var secdef bool
	if err := h.super.QueryRow(ctx,
		`SELECT pg_get_userbyid(proowner), prosecdef, coalesce(proconfig, '{}') FROM pg_proc WHERE oid = $1::regprocedure`, samplerCountSig).Scan(&owner, &secdef, &config); err != nil {
		t.Fatalf("read the owner of %s: %v", samplerCountSig, err)
	}
	if owner != "auth_hook_reader" || !secdef || !reflect.DeepEqual(config, []string{`search_path=""`}) {
		t.Errorf("%s owner = %q, security definer = %t, config = %q, want auth_hook_reader, true, [search_path=\"\"]", samplerCountSig, owner, secdef, config)
	}

	// Runtime half of the grants: the catalog says no column read, the engine must agree.
	for _, q := range []string{
		`SELECT fleet_status FROM public.health_samples LIMIT 1`,
		`SELECT services_down FROM public.health_samples LIMIT 1`,
		`SELECT job_counts FROM public.health_samples LIMIT 1`,
		`SELECT kind_counts FROM public.health_samples LIMIT 1`,
		`SELECT * FROM public.health_samples LIMIT 1`,
	} {
		_, err := h.app.Exec(ctx, q)
		var pgErr *pgconn.PgError
		if !errors.As(err, &pgErr) || pgErr.Code != "42501" {
			t.Errorf("invoice_app %q: err = %v, want SQLSTATE 42501", q, err)
		}
	}
	if _, err := h.app.Exec(ctx, `SELECT taken_at FROM public.health_samples LIMIT 1`); err != nil {
		t.Errorf("control: invoice_app SELECT taken_at: %v", err)
	}
}

type healthFootprint struct {
	table               bool
	functions           int
	jobPolicies         []string
	staffJobGrants      int
	hookJobGrants       int
	appHealthInsertable bool
}

func readHealthFootprint(t *testing.T) healthFootprint {
	t.Helper()
	var f healthFootprint
	if err := h.super.QueryRow(context.Background(), `
		SELECT to_regclass($1) IS NOT NULL,
		       (SELECT count(*) FROM unnest($2::text[]) s WHERE to_regprocedure(s) IS NOT NULL),
		       coalesce(has_table_privilege('invoice_app', to_regclass($1), 'INSERT'), false)`,
		healthSamplesTable, healthFunctionSigs).Scan(&f.table, &f.functions, &f.appHealthInsertable); err != nil {
		t.Fatalf("read the health_samples footprint: %v", err)
	}
	f.jobPolicies = collectStrings(t,
		`SELECT policyname FROM pg_policies WHERE schemaname = 'public' AND tablename = 'submission_jobs' ORDER BY 1`)
	for role, dst := range map[string]*int{"staff_reader": &f.staffJobGrants, "auth_hook_reader": &f.hookJobGrants} {
		*dst = len(healthColumnGrants(t, role, "public.submission_jobs")) + len(healthTableGrants(t, role, "public.submission_jobs"))
	}
	return f
}

func TestRLS_HealthSamplesDownAndUp(t *testing.T) {
	requireHarness(t)
	version := migrationVersion(t, "*_health_samples.sql")
	kindsVersion := migrationVersion(t, "*_health_samples_job_kinds.sql")
	member := seedHouseMember(t, "reviewer", "active")

	before := readHealthFootprint(t)
	if !before.table || before.functions != 3 || before.staffJobGrants != len(staffReaderJobGrants) || before.hookJobGrants != 1 || !before.appHealthInsertable ||
		!containsAll(before.jobPolicies, "health_sample_count", "staff_read_lookup", "tenant_isolation") {
		t.Fatalf("before the Down: footprint = %+v, want the table, 3 functions, both policies beside tenant_isolation, the staff_reader column grants, one auth_hook_reader grant, invoice_app INSERT", before)
	}

	// The per-kind migration's Down runs first: it leaves the four-column function this Down drops.
	reapply := rollBackMigrations(t, staffMigrationProvider(t), kindsVersion, version)
	after := readHealthFootprint(t)
	if after.table || after.functions != 0 || after.staffJobGrants != len(staffReaderJobGrants) || after.hookJobGrants != 0 || after.appHealthInsertable ||
		!reflect.DeepEqual(after.jobPolicies, []string{"staff_read_lookup", "tenant_isolation"}) {
		t.Errorf("after the Down: footprint = %+v, want no table, no function, no auth_hook_reader grant, the staff_reader grants and staff_read_lookup left, submission_jobs policies = [staff_read_lookup tenant_isolation]", after)
	}
	if got := collectStaffTenantRows(t, h.app, member); len(got) == 0 {
		t.Error("after the Down: staff_read_tenants returned no rows to an active house member")
	}

	reapply()
	if again := readHealthFootprint(t); !reflect.DeepEqual(again, before) {
		t.Errorf("after the Up: footprint = %+v, want %+v", again, before)
	}
	var raw []byte
	if err := h.app.QueryRow(context.Background(), `SELECT public.staff_read_submission_job_counts($1::uuid)`, member).Scan(&raw); err != nil {
		t.Fatalf("after the Up: staff_read_submission_job_counts: %v", err)
	}
	healthDecodeCounts(t, "after the Up: staff_read_submission_job_counts", raw)
}

// A tenant GUC must not scope the sampler's count, and must still scope a direct read of submission_jobs.
func TestRLS_SubmissionJobStateCountsIgnoreTheTenantSettingWhileDirectReadsStayScoped(t *testing.T) {
	requireHarness(t)
	requireHealthFunctions(t)
	tx := healthRepeatableReadTx(t)
	healthSeedJobTx(t, tx, h.tenantA, "queued")
	healthSeedJobTx(t, tx, h.tenantB, "dead_lettered")
	total := mustCount(t, tx, `SELECT count(*) FROM submission_jobs`)
	ownA := mustCount(t, tx, `SELECT count(*) FROM submission_jobs WHERE tenant_id = $1`, h.tenantA)
	if ownA < 1 || total <= ownA {
		t.Fatalf("control: tenant A holds %d of %d jobs, want some and fewer than all", ownA, total)
	}
	want := healthGroupByState(t, tx)

	txExec(t, tx, `SELECT set_config('app.current_tenant', $1, true)`, h.tenantA)
	ruleDraftAsApp(t, tx)
	got := healthCallCounts(t, tx, "submission_job_state_counts() with tenant A set", `SELECT public.submission_job_state_counts()`)
	if !reflect.DeepEqual(got, want) {
		t.Errorf("with tenant A set, submission_job_state_counts() = %v, want every tenant's counts %v", got, want)
	}
	if n := mustCount(t, tx, `SELECT count(*) FROM submission_jobs`); n != ownA {
		t.Errorf("invoice_app with tenant A set reads %d submission_jobs rows, want only tenant A's %d", n, ownA)
	}
}

func TestRLS_SubmissionJobStateCountsOfNoJobsIsAnEmptyObject(t *testing.T) {
	requireHarness(t)
	requireHealthFunctions(t)
	tx := healthRepeatableReadTx(t)
	healthSeedJobTx(t, tx, h.tenantA, "queued")
	if n := mustCount(t, tx, `SELECT count(*) FROM submission_jobs`); n == 0 {
		t.Fatal("control: no job to delete")
	}
	txExec(t, tx, `DELETE FROM app_exchange`)
	txExec(t, tx, `DELETE FROM submission_jobs`)
	txExec(t, tx, `SELECT set_config('app.current_tenant', '', true)`)

	ruleDraftAsApp(t, tx)
	var raw []byte
	if err := tx.QueryRow(context.Background(), `SELECT public.submission_job_state_counts()::text`).Scan(&raw); err != nil {
		t.Fatalf("submission_job_state_counts() over no jobs: %v", err)
	}
	if got := strings.ReplaceAll(string(raw), " ", ""); got != "{}" {
		t.Errorf("submission_job_state_counts() over no jobs = %s, want {}", raw)
	}
}

func TestRLS_HealthSamplesTwoWritersKeepOneRow(t *testing.T) {
	requireHarness(t)
	requireHealthSamples(t)
	ctx := context.Background()
	slots := healthSlots(t, 12)
	const writers = 8
	const insert = `INSERT INTO public.health_samples (taken_at, fleet_status, services_down, job_counts) VALUES ($1, 'ok', $2, '{}'::jsonb) ON CONFLICT (taken_at) DO NOTHING`

	for _, slot := range slots {
		var wg sync.WaitGroup
		start := make(chan struct{})
		affected := make([]int64, writers)
		errs := make([]error, writers)
		for w := 0; w < writers; w++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				tag, err := h.app.Exec(ctx, insert, slot, w)
				affected[w], errs[w] = tag.RowsAffected(), err
			}()
		}
		close(start)
		wg.Wait()
		var sum int64
		for w := range affected {
			if errs[w] != nil {
				t.Fatalf("writer %d: %v", w, errs[w])
			}
			sum += affected[w]
		}
		if sum != 1 {
			t.Errorf("slot %s: %d writers stored %d rows between them, want 1", slot.Format(time.RFC3339), writers, sum)
		}
		if n := mustCount(t, h.super, `SELECT count(*) FROM public.health_samples WHERE taken_at = $1`, slot); n != 1 {
			t.Errorf("slot %s holds %d rows, want 1", slot.Format(time.RFC3339), n)
		}
	}

	// A writer without ON CONFLICT is refused by the key itself.
	_, err := h.app.Exec(ctx, `INSERT INTO public.health_samples (taken_at, fleet_status, services_down, job_counts) VALUES ($1, 'ok', 0, '{}'::jsonb)`, slots[0])
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "23505" {
		t.Errorf("plain duplicate insert: err = %v, want SQLSTATE 23505", err)
	}
}

func TestRLS_HealthSamplesRefuseANullColumn(t *testing.T) {
	requireHarness(t)
	requireHealthSamples(t)
	tx := revokeProbeTx(t)
	slots := healthSlots(t, 2)
	for _, c := range []struct{ column, sql string }{
		{"fleet_status", `INSERT INTO public.health_samples (taken_at, fleet_status, services_down, job_counts) VALUES ($1, NULL, NULL, '{}'::jsonb)`},
		{"job_counts", `INSERT INTO public.health_samples (taken_at, fleet_status, services_down, job_counts) VALUES ($1, 'ok', 0, NULL)`},
	} {
		slot := slots[0]
		if c.column == "job_counts" {
			slot = slots[1]
		}
		if _, err := tx.Exec(context.Background(), `SAVEPOINT refusal`); err != nil {
			t.Fatalf("savepoint: %v", err)
		}
		_, err := tx.Exec(context.Background(), c.sql, slot)
		if _, rerr := tx.Exec(context.Background(), `ROLLBACK TO SAVEPOINT refusal`); rerr != nil {
			t.Fatalf("rollback to savepoint: %v", rerr)
		}
		var pgErr *pgconn.PgError
		if !errors.As(err, &pgErr) || pgErr.Code != "23502" || pgErr.ColumnName != c.column {
			t.Errorf("NULL %s: err = %v, want SQLSTATE 23502 on column %s", c.column, err, c.column)
		}
	}
}

// Retention needs DELETE with a taken_at predicate; nothing else may read or rewrite a sample.
func TestRLS_InvoiceAppCanTrimHealthSamplesButNotReadOrRewriteThem(t *testing.T) {
	requireHarness(t)
	requireHealthSamples(t)
	ctx := context.Background()
	s := healthSlots(t, 3)
	for _, slot := range s {
		healthInsertSample(t, slot, "ok", 0, `{}`)
	}

	for _, q := range []struct{ what, sql string }{
		{"UPDATE", `UPDATE public.health_samples SET services_down = 9 WHERE taken_at = $1`},
		{"UPDATE of the key", `UPDATE public.health_samples SET taken_at = taken_at + interval '1 minute' WHERE taken_at = $1`},
		{"DELETE by a value column", `DELETE FROM public.health_samples WHERE taken_at = $1 AND fleet_status = 'ok'`},
		{"INSERT RETURNING a value column", `INSERT INTO public.health_samples (taken_at, fleet_status, services_down, job_counts) VALUES ($1::timestamptz + interval '1 second', 'ok', 0, '{}'::jsonb) RETURNING fleet_status`},
		{"TRUNCATE", `TRUNCATE public.health_samples`},
	} {
		var err error
		if strings.HasPrefix(q.sql, "TRUNCATE") {
			_, err = h.app.Exec(ctx, q.sql)
		} else {
			_, err = h.app.Exec(ctx, q.sql, s[0])
		}
		var pgErr *pgconn.PgError
		if !errors.As(err, &pgErr) || pgErr.Code != "42501" {
			t.Errorf("invoice_app %s: err = %v, want SQLSTATE 42501", q.what, err)
		}
	}
	if n := mustCount(t, h.super, `SELECT count(*) FROM public.health_samples WHERE taken_at = ANY($1)`, s); n != 3 {
		t.Fatalf("after the refused statements %d of 3 test samples remain, want 3", n)
	}

	tag, err := h.app.Exec(ctx, `DELETE FROM public.health_samples WHERE taken_at >= $1 AND taken_at < $2`, s[0], s[2])
	if err != nil {
		t.Fatalf("invoice_app retention DELETE: %v", err)
	}
	if tag.RowsAffected() != 2 {
		t.Errorf("retention DELETE affected %d rows, want 2 (the two older test slots)", tag.RowsAffected())
	}
	if n := mustCount(t, h.super, `SELECT count(*) FROM public.health_samples WHERE taken_at = $1`, s[2]); n != 1 {
		t.Errorf("the newest test slot was deleted too: %d rows remain, want 1", n)
	}
}

// Only the owner and invoice_app may execute the four functions: no other role through a stray grant.
func TestRLS_HealthFunctionsAreExecutableByTheOwnerAndInvoiceAppOnly(t *testing.T) {
	requireHarness(t)
	requireHealthFunctions(t)
	requireRiverCountsFunction(t)
	for _, sig := range healthFunctionSigsWithRiver {
		got := collectStrings(t,
			`SELECT coalesce(r.rolname, 'PUBLIC') FROM pg_proc p, aclexplode(coalesce(p.proacl, acldefault('f', p.proowner))) a
			   LEFT JOIN pg_roles r ON r.oid = a.grantee
			  WHERE p.oid = $1::regprocedure AND a.privilege_type = 'EXECUTE' ORDER BY 1`, sig)
		var owner string
		if err := h.super.QueryRow(context.Background(), `SELECT pg_get_userbyid(proowner) FROM pg_proc WHERE oid = $1::regprocedure`, sig).Scan(&owner); err != nil {
			t.Fatalf("read the owner of %s: %v", sig, err)
		}
		want := []string{"invoice_app", owner}
		sort.Strings(want)
		if !reflect.DeepEqual(got, want) {
			t.Errorf("%s: EXECUTE grantees = %v, want %v", sig, got, want)
		}
	}
}

func TestRLS_StaffReadHealthSamplesRefusesANonStaffActorEvenWithASample(t *testing.T) {
	requireHarness(t)
	requireHealthSamples(t)
	requireHealthFunctions(t)
	house := adoptHouseTenant(t)
	tx := revokeProbeTx(t)
	slot := healthSlots(t, 1)[0]
	txExec(t, tx, `INSERT INTO public.health_samples (taken_at, fleet_status, services_down, job_counts) VALUES ($1, 'ok', 0, '{}'::jsonb)`, slot)
	active := seedMembershipTx(t, tx, house, "reviewer", "active")
	actors := []struct {
		name string
		id   any
	}{
		{"customer_active_member", seedMembershipTx(t, tx, h.tenantA, "admin", "active")},
		{"suspended_house_member", seedMembershipTx(t, tx, house, "reviewer", "suspended")},
		{"invited_house_member", seedMembershipTx(t, tx, house, "reviewer", "invited")},
		{"stranger", uuid.NewString()},
		{"null_actor", nil},
	}
	ruleDraftAsApp(t, tx)

	const call = `SELECT * FROM public.staff_read_health_samples($1::uuid, '2000-01-01T00:00Z'::timestamptz)`
	n, err := ruleDraftTry(t, tx, call, active)
	if err != nil || n == 0 {
		t.Fatalf("control: an active house member read %d rows, err = %v, want the seeded sample", n, err)
	}
	for _, a := range actors {
		n, err := ruleDraftTry(t, tx, call, a.id)
		if n != 0 {
			t.Errorf("%s: refused call returned %d rows", a.name, n)
		}
		requireNotStaffRefusal(t, "staff_read_health_samples as "+a.name, err, "staff_read_health_samples")
	}
	for _, a := range actors {
		_, err := ruleDraftTry(t, tx, `SELECT public.staff_read_submission_job_counts($1::uuid)`, a.id)
		requireNotStaffRefusal(t, "staff_read_submission_job_counts as "+a.name, err, "staff_read_submission_job_counts")
	}
}

func containsAll(have []string, want ...string) bool {
	set := map[string]bool{}
	for _, s := range have {
		set[s] = true
	}
	for _, w := range want {
		if !set[w] {
			return false
		}
	}
	return true
}

func TestRLS_HealthSamplesKindCountsIsANullableJSONColumn(t *testing.T) {
	requireHarness(t)
	requireHealthSamples(t)
	requireKindCountsColumn(t)
	ctx := context.Background()

	var dataType, nullable string
	var def *string
	if err := h.super.QueryRow(ctx, kindCountsColumn).Scan(&dataType, &nullable, &def); err != nil {
		t.Fatalf("read kind_counts column: %v", err)
	}
	if dataType != "jsonb" || nullable != "YES" || def != nil {
		t.Errorf("kind_counts = (%s, nullable %s, default %v), want (jsonb, YES, no default)", dataType, nullable, def)
	}

	slots := healthSlots(t, 2)
	const without = `INSERT INTO public.health_samples (taken_at, fleet_status, services_down, job_counts) VALUES ($1, 'ok', 0, '{}'::jsonb)`
	const with = `INSERT INTO public.health_samples (taken_at, fleet_status, services_down, job_counts, kind_counts) VALUES ($1, 'ok', 0, '{}'::jsonb, '{"k":{"available":1}}'::jsonb)`
	if _, err := h.app.Exec(ctx, without, slots[0]); err != nil {
		t.Fatalf("invoice_app insert without kind_counts: %v", err)
	}
	if _, err := h.app.Exec(ctx, with, slots[1]); err != nil {
		t.Fatalf("invoice_app insert with kind_counts: %v", err)
	}
	var first, second *string
	if err := h.super.QueryRow(ctx, `SELECT kind_counts::text FROM public.health_samples WHERE taken_at = $1`, slots[0]).Scan(&first); err != nil {
		t.Fatalf("read the first row: %v", err)
	}
	if err := h.super.QueryRow(ctx, `SELECT kind_counts::text FROM public.health_samples WHERE taken_at = $1`, slots[1]).Scan(&second); err != nil {
		t.Fatalf("read the second row: %v", err)
	}
	if first != nil {
		t.Errorf("row inserted without kind_counts reads %s, want NULL", *first)
	}
	if second == nil || strings.ReplaceAll(*second, " ", "") != `{"k":{"available":1}}` {
		t.Errorf("row inserted with kind_counts reads %v, want {\"k\":{\"available\":1}}", second)
	}
}

// healthKindRows seeds two samples in fresh slots, the first with kind_counts NULL, and returns the slots.
func healthKindRows(t *testing.T) []time.Time {
	t.Helper()
	requireHealthSamples(t)
	requireKindCountsColumn(t)
	s := healthSlots(t, 2)
	healthInsertSample(t, s[0], "ok", 0, `{}`)
	healthInsertSampleKinds(t, s[1], `{"k":{"available":2}}`)
	return s
}

func TestRLS_StaffReadHealthSamplesReturnsKindCounts(t *testing.T) {
	requireHarness(t)
	requireHealthFunctions(t)
	member := seedHouseMember(t, "reviewer", "active")
	s := healthKindRows(t)

	rows, err := h.app.Query(context.Background(), `SELECT * FROM public.staff_read_health_samples($1::uuid, $2::timestamptz)`, member, s[0])
	if err != nil {
		t.Fatalf("staff_read_health_samples: %v", err)
	}
	var cols []string
	for _, f := range rows.FieldDescriptions() {
		cols = append(cols, f.Name)
	}
	var own [][]any
	for rows.Next() {
		vals, err := rows.Values()
		if err != nil {
			t.Fatalf("read a row: %v", err)
		}
		if at, ok := vals[0].(time.Time); ok && (at.Equal(s[0]) || at.Equal(s[1])) {
			own = append(own, vals)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("collect rows: %v", err)
	}

	if want := []string{"taken_at", "fleet_status", "services_down", "job_counts", "kind_counts"}; !reflect.DeepEqual(cols, want) {
		t.Fatalf("columns = %v, want %v", cols, want)
	}
	if len(own) != 2 || !own[0][0].(time.Time).Equal(s[0]) || !own[1][0].(time.Time).Equal(s[1]) {
		t.Fatalf("the test's slots in the result = %v, want [%s, %s] in that order", own, s[0], s[1])
	}
	if own[0][4] != nil {
		t.Errorf("old row kind_counts = %v, want NULL", own[0][4])
	}
	if want := map[string]any{"k": map[string]any{"available": float64(2)}}; !reflect.DeepEqual(own[1][4], want) {
		t.Errorf("new row kind_counts = %v, want %v", own[1][4], want)
	}
}

// The statement is the one internal/systemhealth/store.go issues today: a running revision must keep working.
func TestRLS_StaffReadHealthSamplesKeepsTheFourColumnRead(t *testing.T) {
	requireHarness(t)
	requireHealthFunctions(t)
	member := seedHouseMember(t, "reviewer", "active")
	s := healthKindRows(t)

	rows, err := h.app.Query(context.Background(), `SELECT taken_at, fleet_status, services_down, job_counts
			FROM public.staff_read_health_samples($1, $2)`, member, s[0])
	if err != nil {
		t.Fatalf("four-column read: %v", err)
	}
	got, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (time.Time, error) {
		var at time.Time
		var status string
		var down *int
		var jobs []byte
		return at, r.Scan(&at, &status, &down, &jobs)
	})
	if err != nil {
		t.Fatalf("collect the four-column read: %v", err)
	}
	var seen int
	for _, at := range got {
		if at.Equal(s[0]) || at.Equal(s[1]) {
			seen++
		}
	}
	if seen != 2 {
		t.Errorf("the four-column read returned %d of the 2 seeded rows, want 2", seen)
	}
}

// riverCountsForActor calls staff_read_river_job_counts as invoice_app inside tx.
func riverCountsForActor(t *testing.T, tx pgx.Tx, actor any) (map[string]map[string]int64, error) {
	t.Helper()
	var raw []byte
	sp, err := tx.Begin(context.Background())
	if err != nil {
		t.Fatalf("savepoint: %v", err)
	}
	if err := sp.QueryRow(context.Background(), `SELECT public.staff_read_river_job_counts($1::uuid)`, actor).Scan(&raw); err != nil {
		_ = sp.Rollback(context.Background())
		return nil, err
	}
	if err := sp.Commit(context.Background()); err != nil {
		t.Fatalf("release savepoint: %v", err)
	}
	return decodeRiverCounts(t, raw), nil
}

func decodeRiverCounts(t *testing.T, raw []byte) map[string]map[string]int64 {
	t.Helper()
	if raw == nil {
		t.Fatal("staff_read_river_job_counts returned NULL, want a JSON object")
	}
	var out map[string]map[string]int64
	if err := json.Unmarshal(raw, &out); err != nil || out == nil {
		t.Fatalf("result %q is not an object of {state: count} objects: %v", raw, err)
	}
	return out
}

// healthSeedRiverJob inserts n River rows of kind in state on a per-run queue, in tx.
func healthSeedRiverJob(t *testing.T, tx pgx.Tx, kind, queue, state string, n int) {
	t.Helper()
	for range n {
		txExec(t, tx, `INSERT INTO public.river_job (kind, queue, state, max_attempts, args, finalized_at)
			VALUES ($1, $2, $3::text::river_job_state, 25, '{}', CASE WHEN $3::text IN ('cancelled', 'completed', 'discarded') THEN now() END)`,
			kind, queue, state)
	}
}

func TestRLS_StaffReadRiverJobCountsCountsByKindAndState(t *testing.T) {
	requireHarness(t)
	requireRiverCountsFunction(t)
	house := adoptHouseTenant(t)
	tx := healthRepeatableReadTx(t)
	member := seedMembershipTx(t, tx, house, "reviewer", "active")
	// A per-run kind on a per-run queue: no River client fetches it and the count for it is exact.
	suffix := uuid.NewString()[:8]
	kind, queue := "h08_"+suffix, "h08q_"+suffix
	for state, n := range map[string]int{"available": 2, "retryable": 1, "discarded": 1} {
		healthSeedRiverJob(t, tx, kind, queue, state, n)
	}

	rows, err := tx.Query(context.Background(), `SELECT kind, state::text, count(*) FROM public.river_job GROUP BY 1, 2`)
	if err != nil {
		t.Fatalf("superuser GROUP BY kind, state: %v", err)
	}
	want := map[string]map[string]int64{}
	for rows.Next() {
		var k, s string
		var n int64
		if err := rows.Scan(&k, &s, &n); err != nil {
			t.Fatalf("scan: %v", err)
		}
		if want[k] == nil {
			want[k] = map[string]int64{}
		}
		want[k][s] = n
	}
	rows.Close()

	ruleDraftAsApp(t, tx)
	got, err := riverCountsForActor(t, tx, member)
	if err != nil {
		t.Fatalf("staff_read_river_job_counts as a house member: %v", err)
	}
	if wantKind := map[string]int64{"available": 2, "retryable": 1, "discarded": 1}; !reflect.DeepEqual(got[kind], wantKind) {
		t.Errorf("counts for kind %s = %v, want exactly %v", kind, got[kind], wantKind)
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("staff_read_river_job_counts = %v, superuser GROUP BY kind, state in the same snapshot = %v, want equal", got, want)
	}
}

// jobKindsScratchDB migrates a scratch database from migrations.FS, seeds an active house member there,
// and returns a superuser and an invoice_app connection to it. Each run applies the migration SQL afresh.
func jobKindsScratchDB(t *testing.T) (super, app *pgx.Conn, member string) {
	t.Helper()
	requireHarness(t)
	superDSN, _ := migratedScratchDB(t, migrations.FS)
	ctx := context.Background()
	u, err := url.Parse(superDSN)
	if err != nil {
		t.Fatalf("parse the scratch dsn: %v", err)
	}
	scratch := strings.TrimPrefix(u.Path, "/")

	super, err = pgx.Connect(ctx, superDSN)
	if err != nil {
		t.Fatalf("connect superuser to %s: %v", scratch, err)
	}
	t.Cleanup(func() { _ = super.Close(ctx) })
	tenant, member := uuid.NewString(), uuid.NewString()
	for _, q := range []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO tenants (id, name) VALUES ($1, 'house (scratch)')`, []any{tenant}},
		{`INSERT INTO public.house_company (tenant_id) VALUES ($1)`, []any{tenant}},
		{`INSERT INTO memberships (id, tenant_id, user_id, role, status) VALUES ($1, $2, $3, 'reviewer', 'active')`, []any{uuid.NewString(), tenant, member}},
	} {
		if _, err := super.Exec(ctx, q.sql, q.args...); err != nil {
			t.Fatalf("seed the scratch database: %v", err)
		}
	}
	app, err = pgx.Connect(ctx, dsnForDB(t, os.Getenv("DATABASE_URL"), scratch))
	if err != nil {
		t.Fatalf("connect invoice_app to %s: %v", scratch, err)
	}
	t.Cleanup(func() { _ = app.Close(ctx) })
	return super, app, member
}

func TestRLS_RiverJobCountsOfAnEmptyQueue(t *testing.T) {
	super, app, member := jobKindsScratchDB(t)
	ctx := context.Background()
	var jobs int
	if err := super.QueryRow(ctx, `SELECT count(*) FROM public.river_job`).Scan(&jobs); err != nil || jobs != 0 {
		t.Fatalf("control: scratch river_job holds %d rows (err %v), want 0", jobs, err)
	}

	t.Run("staff read is an empty object", func(t *testing.T) {
		var got *string
		if err := app.QueryRow(ctx, `SELECT public.staff_read_river_job_counts($1::uuid)::text`, member).Scan(&got); err != nil {
			t.Fatalf("staff_read_river_job_counts over an empty river_job: %v", err)
		}
		if got == nil {
			t.Fatal("staff_read_river_job_counts over an empty river_job = NULL, want {}")
		}
		if s := strings.ReplaceAll(*got, " ", ""); s != "{}" {
			t.Errorf("staff_read_river_job_counts over an empty river_job = %s, want {}", *got)
		}
	})
}

func TestRLS_StaffReadRiverJobCountsRefusesANonStaffActor(t *testing.T) {
	requireHarness(t)
	requireRiverCountsFunction(t)
	house := adoptHouseTenant(t)
	tx := revokeProbeTx(t)
	active := seedMembershipTx(t, tx, house, "reviewer", "active")
	actors := []struct {
		name string
		id   any
	}{
		{"stranger", uuid.NewString()},
		{"suspended_house_member", seedMembershipTx(t, tx, house, "reviewer", "suspended")},
		{"customer_active_member", seedMembershipTx(t, tx, h.tenantA, "admin", "active")},
		{"invited_house_member", seedMembershipTx(t, tx, house, "reviewer", "invited")},
		{"null_actor", nil},
	}
	ruleDraftAsApp(t, tx)

	if _, err := riverCountsForActor(t, tx, active); err != nil {
		t.Fatalf("control: an active house member is refused: %v", err)
	}
	for _, a := range actors {
		got, err := riverCountsForActor(t, tx, a.id)
		if got != nil {
			t.Errorf("%s: refused call returned %v", a.name, got)
		}
		requireNotStaffRefusal(t, "staff_read_river_job_counts as "+a.name, err, "staff_read_river_job_counts")
	}
}

// jobKindsFootprint is what the per-kind migration adds.
type jobKindsFootprint struct {
	column          bool
	riverFn         bool
	sampleColumns   int
	riverGrants     []string
	kindCountsGrant bool
}

func readJobKindsFootprint(t *testing.T) jobKindsFootprint {
	t.Helper()
	var f jobKindsFootprint
	if err := h.super.QueryRow(context.Background(), `
		SELECT EXISTS (SELECT 1 FROM information_schema.columns WHERE table_schema = 'public' AND table_name = 'health_samples' AND column_name = 'kind_counts'),
		       to_regprocedure($1) IS NOT NULL,
		       coalesce((SELECT count(*) FROM pg_proc p, unnest(p.proargmodes) m WHERE p.oid = to_regprocedure($2) AND m = 't'), 0)`,
		staffRiverCountsSig, staffSamplesSig).Scan(&f.column, &f.riverFn, &f.sampleColumns); err != nil {
		t.Fatalf("read the per-kind footprint: %v", err)
	}
	f.riverGrants = healthColumnGrants(t, "staff_reader", "public.river_job")
	if len(f.riverGrants) == 0 {
		f.riverGrants = nil
	}
	f.kindCountsGrant = slices.Contains(healthColumnGrants(t, "staff_reader", healthSamplesTable), "kind_counts/SELECT")
	return f
}

func TestRLS_HealthSamplesJobKindsDownAndUp(t *testing.T) {
	requireHarness(t)
	kindsVersion := migrationVersion(t, "*_health_samples_job_kinds.sql")
	member := seedHouseMember(t, "reviewer", "active")

	wantUp := jobKindsFootprint{column: true, riverFn: true, sampleColumns: 5, riverGrants: []string{"kind/SELECT", "state/SELECT"}, kindCountsGrant: true}
	before := readJobKindsFootprint(t)
	if !reflect.DeepEqual(before, wantUp) {
		t.Fatalf("before the Down: footprint = %+v, want %+v", before, wantUp)
	}

	const fourColumns = `SELECT taken_at, fleet_status, services_down, job_counts FROM public.staff_read_health_samples($1::uuid, '2000-01-01T00:00Z'::timestamptz)`
	reapply := rollBackMigrations(t, staffMigrationProvider(t), kindsVersion)
	after := readJobKindsFootprint(t)
	if wantDown := (jobKindsFootprint{sampleColumns: 4}); !reflect.DeepEqual(after, wantDown) {
		t.Errorf("after the Down: footprint = %+v, want %+v", after, wantDown)
	}
	rows, err := h.app.Query(context.Background(), fourColumns, member)
	if err != nil {
		t.Fatalf("after the Down: four-column read as a house member: %v", err)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		t.Errorf("after the Down: four-column read as a house member: %v", err)
	}

	reapply()
	if again := readJobKindsFootprint(t); !reflect.DeepEqual(again, before) {
		t.Errorf("after the Up: footprint = %+v, want %+v", again, before)
	}
}

func scratchStrings(t *testing.T, c *pgx.Conn, sql string, args ...any) []string {
	t.Helper()
	rows, err := c.Query(context.Background(), sql, args...)
	if err != nil {
		t.Fatalf("read %q: %v", sql, err)
	}
	got, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		t.Fatalf("collect %q: %v", sql, err)
	}
	return got
}

// The migration SQL runs on a fresh database here, so a break in its Up is visible to every subtest.
func TestRLS_JobKindsMigrationOnAFreshDatabase(t *testing.T) {
	super, app, member := jobKindsScratchDB(t)
	ctx := context.Background()
	const samplesFn, riverFn = "public.staff_read_health_samples(uuid, timestamptz)", "public.staff_read_river_job_counts(uuid)"

	t.Run("staff_reader reads kind and state of river_job and nothing else", func(t *testing.T) {
		columnGrants := func(table string) []string {
			return scratchStrings(t, super, `SELECT a.attname || '/' || x.privilege_type FROM pg_attribute a, aclexplode(a.attacl) x
				 WHERE a.attrelid = $1::regclass AND a.attnum > 0 AND NOT a.attisdropped
				   AND x.grantee = (SELECT oid FROM pg_roles WHERE rolname = 'staff_reader') ORDER BY 1`, table)
		}
		if got, want := columnGrants("public.river_job"), []string{"kind/SELECT", "state/SELECT"}; !reflect.DeepEqual(got, want) {
			t.Errorf("staff_reader column grants on river_job = %v, want exactly %v", got, want)
		}
		if got, want := columnGrants("public.health_samples"), []string{"fleet_status/SELECT", "job_counts/SELECT", "kind_counts/SELECT", "services_down/SELECT", "taken_at/SELECT"}; !reflect.DeepEqual(got, want) {
			t.Errorf("staff_reader column grants on health_samples = %v, want exactly %v", got, want)
		}
		for _, table := range []string{"public.river_job", "public.health_samples"} {
			if got := scratchStrings(t, super, `SELECT x.privilege_type FROM pg_class c, aclexplode(c.relacl) x
				 WHERE c.oid = $1::regclass AND x.grantee = (SELECT oid FROM pg_roles WHERE rolname = 'staff_reader')`, table); len(got) != 0 {
				t.Errorf("staff_reader table-level grants on %s = %v, want none", table, got)
			}
		}
		var args, selectKind bool
		if err := super.QueryRow(ctx, `SELECT has_column_privilege('staff_reader', 'public.river_job', 'args', 'SELECT'),
			has_column_privilege('staff_reader', 'public.river_job', 'kind', 'SELECT')`).Scan(&args, &selectKind); err != nil {
			t.Fatalf("has_column_privilege: %v", err)
		}
		if args || !selectKind {
			t.Errorf("staff_reader SELECT on river_job.args = %v, on river_job.kind = %v, want false and true", args, selectKind)
		}
	})

	t.Run("both functions are definer-owned with an empty search_path and run for invoice_app only", func(t *testing.T) {
		for _, sig := range []string{samplesFn, riverFn} {
			var owner string
			var definer bool
			var config []string
			if err := super.QueryRow(ctx, `SELECT pg_get_userbyid(proowner), prosecdef, coalesce(proconfig, '{}') FROM pg_proc WHERE oid = $1::regprocedure`, sig).Scan(&owner, &definer, &config); err != nil {
				t.Fatalf("read %s: %v", sig, err)
			}
			if owner != "staff_reader" || !definer || !reflect.DeepEqual(config, []string{`search_path=""`}) {
				t.Errorf("%s = owner %s, definer %v, config %v, want staff_reader, true, [search_path=\"\"]", sig, owner, definer, config)
			}
			got := scratchStrings(t, super, `SELECT coalesce(r.rolname, 'PUBLIC') FROM pg_proc p, aclexplode(coalesce(p.proacl, acldefault('f', p.proowner))) a
				   LEFT JOIN pg_roles r ON r.oid = a.grantee WHERE p.oid = $1::regprocedure AND a.privilege_type = 'EXECUTE' ORDER BY 1`, sig)
			if want := []string{"invoice_app", "staff_reader"}; !reflect.DeepEqual(got, want) {
				t.Errorf("%s executable by %v, want exactly %v", sig, got, want)
			}
		}
	})

	t.Run("kind_counts is jsonb and invoice_app inserts it but cannot read it", func(t *testing.T) {
		var typ, nullable string
		var def *string
		if err := super.QueryRow(ctx, kindCountsColumn).Scan(&typ, &nullable, &def); err != nil {
			t.Fatalf("read kind_counts column: %v", err)
		}
		if typ != "jsonb" || nullable != "YES" || def != nil {
			t.Errorf("kind_counts = (%s, nullable %s, default %v), want (jsonb, YES, no default)", typ, nullable, def)
		}
		got := scratchStrings(t, super, `SELECT privilege_type FROM information_schema.role_table_grants WHERE grantee = 'invoice_app' AND table_name = 'health_samples' ORDER BY 1`)
		if want := []string{"DELETE", "INSERT"}; !reflect.DeepEqual(got, want) {
			t.Errorf("invoice_app table privileges on health_samples = %v, want %v", got, want)
		}
		cols := scratchStrings(t, super, `SELECT a.attname || '/' || x.privilege_type FROM pg_attribute a, aclexplode(a.attacl) x
			 WHERE a.attrelid = 'public.health_samples'::regclass AND a.attnum > 0 AND NOT a.attisdropped
			   AND x.grantee = (SELECT oid FROM pg_roles WHERE rolname = 'invoice_app') ORDER BY 1`)
		if want := []string{"taken_at/SELECT"}; !reflect.DeepEqual(cols, want) {
			t.Errorf("invoice_app column privileges on health_samples = %v, want %v", cols, want)
		}
		if _, err := app.Exec(ctx, `SELECT kind_counts FROM public.health_samples`); err == nil {
			t.Error("invoice_app read kind_counts directly, want permission denied")
		}
	})

	t.Run("staff_read_health_samples returns five columns oldest first and keeps the four-column read", func(t *testing.T) {
		old := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
		for _, q := range []string{
			`INSERT INTO public.health_samples (taken_at, fleet_status, services_down, job_counts, kind_counts) VALUES ('2026-01-01T00:05Z', 'ok', 0, '{}', '{"k":{"available":2}}')`,
			`INSERT INTO public.health_samples (taken_at, fleet_status, services_down, job_counts) VALUES ('2026-01-01T00:00Z', 'ok', 0, '{}')`,
		} {
			if _, err := super.Exec(ctx, q); err != nil {
				t.Fatalf("seed a sample: %v", err)
			}
		}
		var n int
		if err := app.QueryRow(ctx, `SELECT count(*) FROM (SELECT taken_at, fleet_status, services_down, job_counts FROM public.staff_read_health_samples($1::uuid, $2::timestamptz)) q`, member, old).Scan(&n); err != nil || n != 2 {
			t.Errorf("four-column read returned %d rows (err %v), want 2", n, err)
		}
		rows, err := app.Query(ctx, `SELECT * FROM public.staff_read_health_samples($1::uuid, $2::timestamptz)`, member, old)
		if err != nil {
			t.Fatalf("staff_read_health_samples: %v", err)
		}
		var cols []string
		for _, f := range rows.FieldDescriptions() {
			cols = append(cols, f.Name)
		}
		var kinds []*string
		var ats []time.Time
		for rows.Next() {
			var at time.Time
			var status string
			var down *int
			var jobs []byte
			var k *string
			if err := rows.Scan(&at, &status, &down, &jobs, &k); err != nil {
				t.Fatalf("scan a row: %v", err)
			}
			ats, kinds = append(ats, at), append(kinds, k)
		}
		if err := rows.Err(); err != nil {
			t.Fatalf("collect rows: %v", err)
		}
		if want := []string{"taken_at", "fleet_status", "services_down", "job_counts", "kind_counts"}; !reflect.DeepEqual(cols, want) {
			t.Fatalf("columns = %v, want %v", cols, want)
		}
		if len(ats) != 2 || !ats[0].Equal(old) || !ats[1].Equal(old.Add(5*time.Minute)) {
			t.Fatalf("slots = %v, want the 00:00 row then the 00:05 row", ats)
		}
		if kinds[0] != nil || kinds[1] == nil || strings.ReplaceAll(*kinds[1], " ", "") != `{"k":{"available":2}}` {
			t.Errorf("kind_counts = %v, want NULL then {\"k\":{\"available\":2}}", kinds)
		}

		_, err = app.Exec(ctx, `SELECT * FROM public.staff_read_health_samples($1::uuid, $2::timestamptz)`, uuid.NewString(), old)
		requireNotStaffRefusal(t, "staff_read_health_samples as a stranger", err, "staff_read_health_samples")
	})

	t.Run("staff_read_river_job_counts groups every kind and state and refuses a non-staff actor", func(t *testing.T) {
		for _, j := range []struct {
			kind, state string
			n           int
		}{{"alpha", "available", 2}, {"alpha", "retryable", 1}, {"alpha", "discarded", 1}, {"beta", "available", 5}, {"beta", "completed", 3}} {
			for range j.n {
				if _, err := super.Exec(ctx, `INSERT INTO public.river_job (kind, queue, state, max_attempts, args, finalized_at)
					VALUES ($1, 'scratchq', $2::text::river_job_state, 25, '{}', CASE WHEN $2::text IN ('cancelled', 'completed', 'discarded') THEN now() END)`, j.kind, j.state); err != nil {
					t.Fatalf("seed river_job %s/%s: %v", j.kind, j.state, err)
				}
			}
		}
		var raw []byte
		if err := app.QueryRow(ctx, `SELECT public.staff_read_river_job_counts($1::uuid)`, member).Scan(&raw); err != nil {
			t.Fatalf("staff_read_river_job_counts: %v", err)
		}
		want := map[string]map[string]int64{
			"alpha": {"available": 2, "retryable": 1, "discarded": 1},
			"beta":  {"available": 5, "completed": 3},
		}
		if got := decodeRiverCounts(t, raw); !reflect.DeepEqual(got, want) {
			t.Errorf("counts = %v, want %v", got, want)
		}

		var suspended string
		if err := super.QueryRow(ctx, `INSERT INTO memberships (id, tenant_id, user_id, role, status)
			SELECT $1, tenant_id, $2, 'reviewer', 'suspended' FROM public.house_company LIMIT 1 RETURNING user_id`, uuid.NewString(), uuid.NewString()).Scan(&suspended); err != nil {
			t.Fatalf("seed a suspended house member: %v", err)
		}
		for name, actor := range map[string]any{"stranger": uuid.NewString(), "suspended house member": suspended, "null actor": nil} {
			err := app.QueryRow(ctx, `SELECT public.staff_read_river_job_counts($1::uuid)`, actor).Scan(&raw)
			requireNotStaffRefusal(t, "staff_read_river_job_counts as a "+name, err, "staff_read_river_job_counts")
		}
	})
}

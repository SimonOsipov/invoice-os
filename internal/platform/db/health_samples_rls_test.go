package db_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"math/rand/v2"
	"reflect"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

const (
	healthSamplesTable = "public.health_samples"
	samplerCountSig    = "public.submission_job_state_counts()"
	staffCountSig      = "public.staff_read_submission_job_counts(uuid)"
	staffSamplesSig    = "public.staff_read_health_samples(uuid, timestamp with time zone)"
)

var healthFunctionSigs = []string{samplerCountSig, staffCountSig, staffSamplesSig}

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
	txExec(t, tx, `SELECT set_config('app.current_tenant', '', true)`)

	ruleDraftAsApp(t, tx)
	got := healthCallCounts(t, tx, "invoice_app with no tenant set calls submission_job_state_counts()", `SELECT public.submission_job_state_counts()`)
	ruleDraftAsOwner(t, tx)
	want := healthGroupByState(t, tx)

	if got["queued"] < 1 || got["dead_lettered"] < 1 {
		t.Errorf("counts = %v, want queued and dead_lettered each >= 1 (tenant A's and tenant B's seeded jobs)", got)
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

	ruleDraftAsApp(t, tx)
	sampler := healthCallCounts(t, tx, "submission_job_state_counts()", `SELECT public.submission_job_state_counts()`)
	staff := healthCallCounts(t, tx, "staff_read_submission_job_counts(house member)", `SELECT public.staff_read_submission_job_counts($1::uuid)`, member)

	if sampler["queued"] < 1 || sampler["dead_lettered"] < 1 {
		t.Errorf("sampler counts = %v, want queued and dead_lettered each >= 1", sampler)
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
		[]string{"fleet_status/SELECT", "job_counts/SELECT", "services_down/SELECT", "taken_at/SELECT"})
	check("staff_reader policies on health_samples", healthPolicies(t, "staff_reader", healthSamplesTable), nil)

	var rls bool
	if err := h.super.QueryRow(ctx, `SELECT relrowsecurity FROM pg_class WHERE oid = $1::regclass`, healthSamplesTable).Scan(&rls); err != nil {
		t.Fatalf("read relrowsecurity: %v", err)
	}
	if rls {
		t.Error("health_samples has row-level security, want none (a global table like staff_audit_log)")
	}

	for _, sig := range healthFunctionSigs {
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
	member := seedHouseMember(t, "reviewer", "active")

	before := readHealthFootprint(t)
	if !before.table || before.functions != 3 || before.staffJobGrants != len(staffReaderJobGrants) || before.hookJobGrants != 1 || !before.appHealthInsertable ||
		!containsAll(before.jobPolicies, "health_sample_count", "staff_read_lookup", "tenant_isolation") {
		t.Fatalf("before the Down: footprint = %+v, want the table, 3 functions, both policies beside tenant_isolation, the staff_reader column grants, one auth_hook_reader grant, invoice_app INSERT", before)
	}

	reapply := rollBackMigrations(t, staffMigrationProvider(t), version)
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

// Only the owner and invoice_app may execute the three functions: no other role through a stray grant.
func TestRLS_HealthFunctionsAreExecutableByTheOwnerAndInvoiceAppOnly(t *testing.T) {
	requireHarness(t)
	requireHealthFunctions(t)
	for _, sig := range healthFunctionSigs {
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

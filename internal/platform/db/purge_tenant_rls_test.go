// public.purge_tenant(uuid): ownership, refusals, completeness, lock cost and guard state.
// Env-gated through requireHarness (make test-rls). No t.Parallel: the lock test holds a
// table lock and the rest share the house tenant.
package db_test

import (
	"context"
	"errors"
	"reflect"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	db "github.com/SimonOsipov/invoice-os/internal/platform/db"
)

const purgeSig = "public.purge_tenant(uuid)"

var purgeGuardTriggers = []string{
	"audit_log_no_update_delete",
	"approval_policy_versions_seal_guard",
	"approval_policy_steps_content_lock",
}

// ptTenant is a tenant seeded with a sealed version, two steps, an audit row and an idempotency key.
type ptTenant struct {
	id      string
	version string
	steps   []string
}

// ptNewTenant inserts a tenant and removes it, and everything under it, on cleanup.
func ptNewTenant(t *testing.T) string {
	t.Helper()
	id := uuid.NewString()
	execSetup(t, h.super, `INSERT INTO tenants (id, name, kind) VALUES ($1, 'Purge test', 'firm')`, id)
	t.Cleanup(func() {
		dropTenantRows(t, h.super, id)
		_, _ = h.super.Exec(context.Background(),
			`DELETE FROM river_job WHERE args @> jsonb_build_object('tenant_id', $1::text)`, id)
	})
	return id
}

// ptSealedPolicy adds a policy whose version gets steps before it is sealed: the content
// lock refuses a step INSERT under a sealed version.
func ptSealedPolicy(t *testing.T, tenantID string, nSteps int) (version string, steps []string) {
	t.Helper()
	policy, version := uuid.NewString(), uuid.NewString()
	execSetup(t, h.super, `INSERT INTO approval_policies (id, tenant_id, name) VALUES ($1,$2,'Purge test policy')`, policy, tenantID)
	execSetup(t, h.super, `INSERT INTO approval_policy_versions (id, tenant_id, policy_id, version) VALUES ($1,$2,$3,1)`, version, tenantID, policy)
	for i := 1; i <= nSteps; i++ {
		id := uuid.NewString()
		execSetup(t, h.super,
			`INSERT INTO approval_policy_steps (id, tenant_id, version_id, ord, kind) VALUES ($1,$2,$3,$4,'approval')`,
			id, tenantID, version, i)
		steps = append(steps, id)
	}
	execSetup(t, h.super, `UPDATE approval_policy_versions SET sealed = true WHERE id = $1`, version)
	return version, steps
}

func ptSeedSealedTenant(t *testing.T) ptTenant {
	t.Helper()
	id := ptNewTenant(t)
	version, steps := ptSealedPolicy(t, id, 2)
	execSetup(t, h.super, `INSERT INTO audit_log (tenant_id, actor, event) VALUES ($1,'purge-test','purge.test')`, id)
	execSetup(t, h.super, `INSERT INTO idempotency_keys (tenant_id, key) VALUES ($1,$2)`, id, "purge-test-"+uuid.NewString())
	return ptTenant{id: id, version: version, steps: steps}
}

func ptInsertRiverJob(t *testing.T, kind, args string) int64 {
	t.Helper()
	var id int64
	if err := h.super.QueryRow(context.Background(),
		`INSERT INTO river_job (kind, max_attempts, args) VALUES ($1, 25, $2::jsonb) RETURNING id`, kind, args).Scan(&id); err != nil {
		t.Fatalf("test setup: insert river_job %s: %v", kind, err)
	}
	t.Cleanup(func() { _, _ = h.super.Exec(context.Background(), `DELETE FROM river_job WHERE id = $1`, id) })
	return id
}

func ptRiverCount(t *testing.T, tenantID string) int {
	t.Helper()
	return mustCount(t, h.super, `SELECT count(*) FROM river_job WHERE args @> jsonb_build_object('tenant_id', $1::text)`, tenantID)
}

func ptRows(t *testing.T, table, tenantID string) int {
	t.Helper()
	col := "tenant_id"
	if table == "tenants" {
		col = "id"
	}
	return mustCount(t, h.super, `SELECT count(*) FROM `+table+` WHERE `+col+`::text = $1`, tenantID)
}

// ptCall runs purge_tenant(arg) as invoice_app. bind "" opens a tx with no tenant bound.
func ptCall(pool *pgxpool.Pool, bind string, arg any) (int, error) {
	ctx := context.Background()
	var n int
	q := func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT public.purge_tenant($1::uuid)`, arg).Scan(&n)
	}
	if bind != "" {
		return n, db.WithinTenantTx(ctx, pool, bind, q)
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := q(tx); err != nil {
		return 0, err
	}
	return n, tx.Commit(ctx)
}

func ptPurge(t *testing.T, id string) int {
	t.Helper()
	n, err := ptCall(h.app, id, id)
	if err != nil {
		t.Fatalf("purge_tenant(%s) as invoice_app: %v", id, err)
	}
	return n
}

func ptAssertGuardsArmed(t *testing.T) {
	t.Helper()
	rows, err := h.super.Query(context.Background(),
		`SELECT tgname, tgenabled::text FROM pg_trigger WHERE tgname = ANY($1) AND NOT tgisinternal`, purgeGuardTriggers)
	if err != nil {
		t.Fatalf("read pg_trigger: %v", err)
	}
	defer rows.Close()
	seen := map[string]bool{}
	for rows.Next() {
		var name, enabled string
		if err := rows.Scan(&name, &enabled); err != nil {
			t.Fatalf("scan pg_trigger: %v", err)
		}
		seen[name] = true
		if enabled != "O" {
			t.Errorf("trigger %s tgenabled = %q, want O", name, enabled)
		}
	}
	for _, n := range purgeGuardTriggers {
		if !seen[n] {
			t.Errorf("trigger %s not found in pg_trigger", n)
		}
	}
}

// ptHouseTenant returns the house tenant, creating a house row when the table has none.
func ptHouseTenant(t *testing.T) string {
	t.Helper()
	var id string
	err := h.super.QueryRow(context.Background(), `SELECT tenant_id::text FROM house_company`).Scan(&id)
	if err == nil {
		return id
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("read house_company: %v", err)
	}
	id = ptNewTenant(t)
	execSetup(t, h.super, `INSERT INTO house_company (tenant_id) VALUES ($1)`, id)
	t.Cleanup(func() {
		_, _ = h.super.Exec(context.Background(), `DELETE FROM house_company WHERE tenant_id = $1`, id)
	})
	return id
}

func ptLiveTenantTables(t *testing.T) []string {
	t.Helper()
	rows, err := h.super.Query(context.Background(), `
		SELECT c.relname FROM pg_attribute a
		  JOIN pg_class c ON c.oid = a.attrelid
		  JOIN pg_namespace n ON n.oid = c.relnamespace
		 WHERE a.attname = 'tenant_id' AND a.attnum > 0 AND NOT a.attisdropped
		   AND c.relkind = 'r' AND n.nspname = 'public' AND c.relname <> $1
		 ORDER BY 1`, rlsFixtureTable)
	if err != nil {
		t.Fatalf("read tenant_id tables: %v", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			t.Fatalf("scan relname: %v", err)
		}
		out = append(out, s)
	}
	if len(out) == 0 {
		t.Fatal("pg_attribute reported 0 tenant_id tables — the completeness oracle read nothing")
	}
	return out
}

// ptPlantEverything plants a row in every live tenant_id table but house_company and returns
// the table names. It fails by name when the live set and the planted set differ.
func ptPlantEverything(t *testing.T, id string) []string {
	t.Helper()
	planted := plantWitnessRows(t, h.super, id)
	set := map[string]bool{}
	for tbl := range planted {
		set[tbl] = true
	}
	for _, parent := range []string{"memberships", "approval_policies", "approval_policy_versions"} {
		set[parent] = true // plantWitnessRows builds these as parents
	}
	ptSealedPolicy(t, id, 1)
	set["approval_policy_steps"] = true
	execSetup(t, h.super,
		`INSERT INTO ai_calls (tenant_id, client, purpose, model, input_tokens, output_tokens, cost, latency_ms, attempts, outcome)
		 VALUES ($1,'ai','purge-test','m',1,1,0,1,1,'ok')`, id)
	set["ai_calls"] = true
	execSetup(t, h.super,
		`INSERT INTO staff_audit_log (actor, event, rule_set_version_id, tenant_id) VALUES ($1,'purge.test',$2,$3)`,
		uuid.NewString(), uuid.NewString(), id)
	set["staff_audit_log"] = true
	ptInsertRiverJob(t, "extraction_extract", `{"tenant_id":"`+id+`"}`)

	var live []string
	for _, tbl := range ptLiveTenantTables(t) {
		if tbl != "house_company" {
			live = append(live, tbl)
		}
	}
	var got []string
	for tbl := range set {
		got = append(got, tbl)
	}
	sort.Strings(got)
	if !reflect.DeepEqual(live, got) {
		t.Fatalf("planted tables differ from live tenant_id tables (minus house_company) — plant a row in the new table\nlive:    %v\nplanted: %v", live, got)
	}
	for _, tbl := range got {
		if ptRows(t, tbl, id) == 0 {
			t.Fatalf("test setup: no row planted in %s", tbl)
		}
	}
	return got
}

func TestRLS_PurgeTenantIsOwnedAndGrantedNarrowly(t *testing.T) {
	requireHarness(t)

	s, ok := readProc(t, purgeSig)
	if !ok {
		t.Fatalf("function %s does not exist", purgeSig)
	}
	if s.owner != "invoice_migrator" {
		t.Errorf("owner = %q, want invoice_migrator", s.owner)
	}
	if !s.secdef {
		t.Errorf("prosecdef = false, want SECURITY DEFINER")
	}
	got := append([]string{}, s.config...)
	want := []string{`search_path=""`, "lock_timeout=5s"}
	sort.Strings(got)
	sort.Strings(want)
	if !reflect.DeepEqual(got, want) {
		t.Errorf("proconfig = %q, want exactly %q", s.config, want)
	}
	assertExecuteOnlyForApp(t, purgeSig, s, "invoice_migrator")

	var gateway bool
	if err := h.super.QueryRow(context.Background(),
		`SELECT has_function_privilege('invoice_gateway', to_regprocedure($1), 'EXECUTE')`, purgeSig).Scan(&gateway); err != nil {
		t.Fatalf("has_function_privilege(invoice_gateway): %v", err)
	}
	if gateway {
		t.Errorf("invoice_gateway holds EXECUTE on %s, want none", purgeSig)
	}
}

func TestRLS_PurgeTenantRefusesATenantOtherThanTheBoundOne(t *testing.T) {
	requireHarness(t)
	a, b := ptSeedSealedTenant(t), ptSeedSealedTenant(t)

	for _, c := range []struct {
		name string
		bind string
		arg  any
	}{
		{"guc unset", "", a.id},
		{"guc is another tenant", b.id, a.id},
		{"null argument", a.id, nil},
	} {
		t.Run(c.name, func(t *testing.T) {
			_, err := ptCall(h.app, c.bind, c.arg)
			assertPgRefusal(t, "purge_tenant", err, "42501", "does not match app.current_tenant")
			if n := ptRows(t, "tenants", a.id); n != 1 {
				t.Errorf("tenants rows for A = %d, want 1", n)
			}
			if n := ptRows(t, "audit_log", a.id); n != 1 {
				t.Errorf("audit_log rows for A = %d, want 1", n)
			}
			if n := ptRows(t, "tenants", b.id); n != 1 {
				t.Errorf("tenants rows for B = %d, want 1", n)
			}
		})
	}
}

func TestRLS_PurgeTenantRefusesTheHouseTenant(t *testing.T) {
	requireHarness(t)
	house := ptHouseTenant(t)
	event := "purge.test." + uuid.NewString()[:8]
	execSetup(t, h.super, `INSERT INTO audit_log (tenant_id, actor, event) VALUES ($1,'purge-test',$2)`, house, event)
	t.Cleanup(func() {
		tx, err := h.super.Begin(context.Background())
		if err != nil {
			return
		}
		defer func() { _ = tx.Rollback(context.Background()) }()
		_, _ = tx.Exec(context.Background(), `SET LOCAL session_replication_role = 'replica'`)
		_, _ = tx.Exec(context.Background(), `DELETE FROM audit_log WHERE tenant_id = $1 AND event = $2`, house, event)
		_ = tx.Commit(context.Background())
	})

	_, err := ptCall(h.app, house, house)
	assertPgRefusal(t, "purge_tenant(house)", err, "42501", "house tenant is never purged")

	if n := ptRows(t, "tenants", house); n != 1 {
		t.Errorf("house tenants rows = %d, want 1", n)
	}
	if n := ptRows(t, "house_company", house); n != 1 {
		t.Errorf("house_company rows = %d, want 1", n)
	}
	if n := mustCount(t, h.super, `SELECT count(*) FROM audit_log WHERE tenant_id = $1 AND event = $2`, house, event); n != 1 {
		t.Errorf("house audit rows = %d, want 1", n)
	}
}

func TestRLS_PurgeTenantRemovesASealedTenantAndReturnsOne(t *testing.T) {
	requireHarness(t)
	a := ptSeedSealedTenant(t)
	tables := []string{"tenants", "approval_policies", "approval_policy_versions", "approval_policy_steps", "audit_log", "idempotency_keys"}
	for _, tbl := range tables {
		if ptRows(t, tbl, a.id) == 0 {
			t.Fatalf("test setup: no %s row for A", tbl)
		}
	}

	n, err := ptCall(h.app, a.id, a.id)
	if err != nil {
		t.Fatalf("purge_tenant as invoice_app: %v", err)
	}
	if n != 1 {
		t.Errorf("purge_tenant = %d, want 1", n)
	}
	for _, tbl := range tables {
		if got := ptRows(t, tbl, a.id); got != 0 {
			t.Errorf("%s rows left for A = %d, want 0", tbl, got)
		}
	}
}

func TestRLS_PurgeTenantOfAnUnknownTenantReturnsZero(t *testing.T) {
	requireHarness(t)
	id := uuid.NewString()
	n, err := ptCall(h.app, id, id)
	if err != nil {
		t.Fatalf("purge_tenant(unknown) as invoice_app: %v", err)
	}
	if n != 0 {
		t.Errorf("purge_tenant(unknown) = %d, want 0", n)
	}
}

func TestRLS_PurgeTenantRerunRemovesStragglers(t *testing.T) {
	requireHarness(t)
	a := ptSeedSealedTenant(t)
	if n := ptPurge(t, a.id); n != 1 {
		t.Fatalf("first purge = %d, want 1", n)
	}

	execSetup(t, h.super, `INSERT INTO audit_log (tenant_id, actor, event) VALUES ($1,'purge-test','purge.straggler')`, a.id)
	execSetup(t, h.super, `INSERT INTO idempotency_keys (tenant_id, key) VALUES ($1,'purge-straggler')`, a.id)
	ptInsertRiverJob(t, "extraction_extract", `{"tenant_id":"`+a.id+`"}`)
	if ptRows(t, "audit_log", a.id) != 1 || ptRows(t, "idempotency_keys", a.id) != 1 || ptRiverCount(t, a.id) != 1 {
		t.Fatal("test setup: stragglers not planted")
	}

	if n := ptPurge(t, a.id); n != 0 {
		t.Errorf("second purge = %d, want 0", n)
	}
	if n := ptRows(t, "audit_log", a.id); n != 0 {
		t.Errorf("straggler audit_log rows = %d, want 0", n)
	}
	if n := ptRows(t, "idempotency_keys", a.id); n != 0 {
		t.Errorf("straggler idempotency_keys rows = %d, want 0", n)
	}
	if n := ptRiverCount(t, a.id); n != 0 {
		t.Errorf("straggler river_job rows = %d, want 0", n)
	}
}

func TestRLS_PurgeTenantLeavesAnotherTenantUntouched(t *testing.T) {
	requireHarness(t)
	a, b := ptNewTenant(t), ptNewTenant(t)
	ptPlantEverything(t, a)
	planted := ptPlantEverything(t, b)

	snap := func() map[string]int {
		m := map[string]int{"tenants": ptRows(t, "tenants", b), "river_job": ptRiverCount(t, b)}
		for _, tbl := range planted {
			m[tbl] = ptRows(t, tbl, b)
		}
		return m
	}
	before := snap()
	if before["river_job"] == 0 || before["tenants"] != 1 {
		t.Fatalf("test setup: B snapshot = %v", before)
	}

	if n := ptPurge(t, a); n != 1 {
		t.Errorf("purge A = %d, want 1", n)
	}
	if after := snap(); !reflect.DeepEqual(before, after) {
		t.Errorf("B rows changed by A's purge\nbefore: %v\nafter:  %v", before, after)
	}
}

func TestRLS_PurgeTenantFailsFastBehindAnOpenAuditWriter(t *testing.T) {
	requireHarness(t)
	a, b := ptSeedSealedTenant(t), ptSeedSealedTenant(t)
	ctx := context.Background()

	inserted, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	stop := func() { once.Do(func() { close(release) }) }
	t.Cleanup(stop)
	done := make(chan error, 1)
	go func() {
		done <- db.WithinTenantTx(ctx, h.app, b.id, func(tx pgx.Tx) error {
			if _, err := tx.Exec(ctx, `INSERT INTO audit_log (actor, event) VALUES ('purge-test','purge.writer')`); err != nil {
				return err
			}
			close(inserted)
			<-release
			return nil
		})
	}()
	select {
	case <-inserted:
	case err := <-done:
		t.Fatalf("test setup: audit writer ended early: %v", err)
	case <-time.After(10 * time.Second):
		t.Fatal("test setup: audit writer did not insert in 10s")
	}

	start := time.Now()
	_, err := ptCall(h.app, a.id, a.id)
	elapsed := time.Since(start)
	assertPgRefusal(t, "purge_tenant behind an open audit writer", err, "55P03", "")
	if elapsed < 4*time.Second || elapsed > 8*time.Second {
		t.Errorf("purge failed after %v, want 4s..8s (lock_timeout 5s)", elapsed)
	}
	if n := ptRows(t, "tenants", a.id); n != 1 {
		t.Errorf("tenants rows for A after the timeout = %d, want 1", n)
	}
	ptAssertGuardsArmed(t)

	stop()
	if err := <-done; err != nil {
		t.Errorf("audit writer commit: %v", err)
	}
}

func TestRLS_PurgeTenantRolledBackLeavesTheTenantAndItsGuards(t *testing.T) {
	requireHarness(t)
	a := ptSeedSealedTenant(t)
	errRollback := errors.New("caller rolls back")

	var removed int
	err := db.WithinTenantTx(context.Background(), h.app, a.id, func(tx pgx.Tx) error {
		if err := tx.QueryRow(context.Background(), `SELECT public.purge_tenant($1::uuid)`, a.id).Scan(&removed); err != nil {
			return err
		}
		return errRollback
	})
	if !errors.Is(err, errRollback) {
		t.Fatalf("purge_tenant inside the tx: %v, want the caller's rollback error", err)
	}
	if removed != 1 {
		t.Errorf("purge_tenant inside the tx = %d, want 1", removed)
	}
	for _, tbl := range []string{"tenants", "approval_policy_versions", "approval_policy_steps", "audit_log"} {
		if n := ptRows(t, tbl, a.id); n == 0 {
			t.Errorf("%s rows for A after rollback = 0, want them back", tbl)
		}
	}
	ptAssertGuardsArmed(t)
}

func TestRLS_PurgeTenantLeavesEveryGuardArmedAfterCommit(t *testing.T) {
	requireHarness(t)
	a, b := ptSeedSealedTenant(t), ptSeedSealedTenant(t)
	if n := ptPurge(t, a.id); n != 1 {
		t.Fatalf("purge A = %d, want 1", n)
	}
	ptAssertGuardsArmed(t)

	step := b.steps[0]
	for _, c := range []struct {
		who, what string
		pool      *pgxpool.Pool
		sql       string
		arg       string
		code, msg string
	}{
		{"invoice_migrator", "DELETE approval_policy_versions", h.mig, `DELETE FROM approval_policy_versions WHERE id = $1`, b.version, "23001", "cannot be deleted"},
		{"invoice_migrator", "DELETE approval_policy_steps", h.mig, `DELETE FROM approval_policy_steps WHERE id = $1`, step, "23001", "immutable"},
		{"invoice_migrator", "DELETE audit_log", h.mig, `DELETE FROM audit_log WHERE tenant_id::text = $1`, b.id, "23001", "append-only"},
		{"invoice_app", "UPDATE approval_policy_versions SET sealed = false", h.app, `UPDATE approval_policy_versions SET sealed = false WHERE id = $1`, b.version, "23001", "cannot be unsealed"},
		{"invoice_app", "DELETE approval_policy_steps", h.app, `DELETE FROM approval_policy_steps WHERE id = $1`, step, "23001", "immutable"},
		{"invoice_app", "DELETE audit_log", h.app, `DELETE FROM audit_log WHERE tenant_id::text = $1`, b.id, "42501", "permission denied"},
		{"invoice_app", "DELETE approval_policy_versions", h.app, `DELETE FROM approval_policy_versions WHERE id = $1`, b.version, "42501", "permission denied"},
	} {
		t.Run(c.who+" "+c.what, func(t *testing.T) {
			err := db.WithinTenantTx(context.Background(), c.pool, b.id, func(tx pgx.Tx) error {
				_, err := tx.Exec(context.Background(), c.sql, c.arg)
				return err
			})
			assertPgRefusal(t, c.what, err, c.code, c.msg)
		})
	}
	for _, tbl := range []string{"tenants", "approval_policy_versions", "approval_policy_steps", "audit_log"} {
		if n := ptRows(t, tbl, b.id); n == 0 {
			t.Errorf("%s rows for B = 0, want them to survive", tbl)
		}
	}
	if n := mustCount(t, h.super, `SELECT count(*) FROM approval_policy_versions WHERE id = $1 AND sealed`, b.version); n != 1 {
		t.Errorf("B's version sealed rows = %d, want 1", n)
	}
}

func TestRLS_PurgeTenantRemovesItsRiverJobsOnly(t *testing.T) {
	requireHarness(t)
	a, b := ptNewTenant(t), ptNewTenant(t)
	mine := []int64{
		ptInsertRiverJob(t, "extraction_extract", `{"tenant_id":"`+a+`"}`),
		ptInsertRiverJob(t, "submission_submit", `{"tenant_id":"`+a+`"}`),
		ptInsertRiverJob(t, "submission_poll", `{"tenant_id":"`+a+`"}`),
	}
	others := []int64{
		ptInsertRiverJob(t, "submission_submit", `{"tenant_id":"`+b+`"}`),
		ptInsertRiverJob(t, "contact_deliver", `{"marker":"`+uuid.NewString()+`"}`),
	}

	if n := ptPurge(t, a); n != 1 {
		t.Errorf("purge A = %d, want 1", n)
	}
	count := func(ids []int64) int {
		return mustCount(t, h.super, `SELECT count(*) FROM river_job WHERE id = ANY($1)`, ids)
	}
	if n := count(mine); n != 0 {
		t.Errorf("A's river jobs left = %d, want 0", n)
	}
	if n := count(others); n != len(others) {
		t.Errorf("other river jobs left = %d, want %d", n, len(others))
	}
}

func TestRLS_PurgeTenantLeavesNoRowInAnyTenantTable(t *testing.T) {
	requireHarness(t)
	a := ptNewTenant(t)
	planted := ptPlantEverything(t, a)

	if n := ptPurge(t, a); n != 1 {
		t.Errorf("purge A = %d, want 1", n)
	}
	checked := 0
	for _, tbl := range planted {
		if tbl == "staff_audit_log" {
			continue
		}
		checked++
		if n := ptRows(t, tbl, a); n != 0 {
			t.Errorf("%s rows left for A = %d, want 0", tbl, n)
		}
	}
	if checked == 0 {
		t.Fatal("checked 0 tables — the removal assertion is vacuous")
	}
	if n := ptRows(t, "staff_audit_log", a); n != 1 {
		t.Errorf("staff_audit_log rows for A = %d, want 1 (kept by design)", n)
	}
	if n := ptRows(t, "tenants", a); n != 0 {
		t.Errorf("tenants rows for A = %d, want 0", n)
	}
}

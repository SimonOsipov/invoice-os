// M5-01-01 (task-211): a REGRESSION PIN over the `idempotency_keys` ledger shipped by
// M2-08 (migrations/20260707193000_river_and_idempotency.sql:382-406). Unlike its
// siblings in this package, this file was written AFTER the table it covers and is GREEN
// at HEAD by construction — its job is not to drive an implementation but to make M5-01's
// Core AC-5 ("the M2-08 ledger is left exactly as it is — not recreated, not re-granted,
// not weakened") a mechanism instead of a promise. M5-01 adds `submission_jobs` and
// `app_exchange` alongside this ledger and deliberately does NOT touch it; these specs
// turn any later re-grant, policy edit or PK change on the table into a red CI `rls` job.
//
// The DDL pinned here, verbatim from the M2-08 migration:
//
//	CREATE TABLE idempotency_keys (
//	    tenant_id  uuid        NOT NULL,
//	    key        text        NOT NULL,
//	    created_at timestamptz NOT NULL DEFAULT now(),
//	    CONSTRAINT idempotency_key_length CHECK (char_length(key) > 0 AND char_length(key) <= 255),
//	    PRIMARY KEY (tenant_id, key)
//	);
//	ALTER TABLE idempotency_keys ENABLE ROW LEVEL SECURITY;
//	ALTER TABLE idempotency_keys FORCE  ROW LEVEL SECURITY;
//	CREATE POLICY tenant_isolation ON idempotency_keys
//	    USING (tenant_id = nullif(current_setting('app.current_tenant', true), '')::uuid);
//	GRANT SELECT, INSERT ON idempotency_keys TO invoice_app;
//
// The ledger is append-only/permanent (the audit_log posture, docs/migrations.md §3):
// invoice_app holds SELECT + INSERT and nothing else, so every UPDATE/DELETE — own-row or
// cross-tenant — is refused at the GRANT layer (SQLSTATE 42501 insufficient_privilege)
// before the RLS policy's USING clause is ever evaluated. That is the same shape
// invoice_status_history_rls_test.go proves for its table, whose own header calls this
// ledger "the idempotency_keys precedent, D10"; this file closes the loop by proving the
// precedent itself. invoice_tenant_reader — the one cross-tenant enumeration identity —
// has no grant on the table at all.
//
// Four things about this table differ from every M4-01 table and shape the tests below:
//
//   - NO foreign key on tenant_id (no REFERENCES tenants(id), no ON DELETE CASCADE). Rows
//     are NOT reaped when their tenant is deleted, so every case here deletes its own
//     seeded rows explicitly rather than leaning on a cascade.
//   - pg_policies.with_check is NULL, not a copy of qual. The migration writes only USING;
//     Postgres applies it implicitly as the INSERT WITH CHECK but never materialises it in
//     the catalog. IK-04 therefore asserts with_check IS NULL — asserting equality with
//     qual would fail against a correct schema.
//   - pg_policies.qual is Postgres's deparsed, NORMALISED form of the policy source (added
//     parens, explicit ::text casts, upper-cased NULLIF), not the migration text. IK-04
//     compares against the normalised string; a literal compare against the migration
//     source would fail.
//   - PG18 records NOT NULL constraints in pg_constraint with contype='n'
//     (idempotency_keys_tenant_id_not_null and friends are live rows). IK-05/IK-08 filter
//     on contype explicitly; a bare count over pg_constraint would be wrong.
//
// Spec-to-test map (Test Specs table, task-211):
//
//	IK-01 TestRLS_IdempotencyKeysGrantMatrixIsSelectInsertOnly
//	IK-02 TestRLS_IdempotencyKeysAppUpdateRefused
//	IK-03 TestRLS_IdempotencyKeysAppDeleteRefused
//	IK-04 TestRLS_IdempotencyKeysForceRLSAndSinglePolicy
//	IK-05 TestRLS_IdempotencyKeysPrimaryKeyIsTenantAndKey
//	IK-06 TestRLS_IdempotencyKeysCrossTenantInsertRefused
//	IK-07 TestRLS_IdempotencyKeysReaderSelectRefused
//	IK-08 TestRLS_IdempotencyKeysKeyLengthCheck (beyond the plan's 7: the (0, 255] bound
//	      M5-01-03's submission_jobs.idempotency_key column is required to match)
//
// Named with the TestRLS_ prefix so the CI `rls` job's `-run TestRLS
// ./internal/platform/db/...` (.github/workflows/ci.yml:282) and `make test-rls` both pick
// these up with no workflow edit. Every case calls requireHarness(t), which SKIPS when the
// per-role DATABASE_* URLs are unset so a bare `go test ./...` stays green with no DB —
// note that under the CI gate (scripts/ci/rls-test-gate.sh) a SKIP is itself a failure, so
// no case here may add a t.Skip of its own.
//
// Run: `make test-rls`, or directly with the same four DSNs, e.g.:
//
//	DATABASE_URL="postgres://invoice_app:app@localhost:5432/invoice_os?sslmode=disable" \
//	DATABASE_MIGRATION_URL="postgres://invoice_migrator:migrator@localhost:5432/invoice_os?sslmode=disable" \
//	DATABASE_SUPERUSER_URL="postgres://postgres:postgres@localhost:5432/invoice_os?sslmode=disable" \
//	DATABASE_READER_URL="postgres://invoice_tenant_reader:reader@localhost:5432/invoice_os?sslmode=disable" \
//	go test -count=1 -run TestRLS_IdempotencyKeys ./internal/platform/db/...
//
// (A worktree running the compose DB on an alternate host port must substitute it in all
// four DSNs — e.g. `DEV_DB_PORT=5433 make test-rls`, since Makefile:32 defaults to 5432.)
package db_test

import (
	"context"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/SimonOsipov/invoice-os/internal/platform/db"
)

// policyQualNormalised is Postgres's deparsed form of the M2-08 policy's USING clause, as
// read back from pg_policies.qual. It is NOT the migration source text: PG adds the outer
// parens, the ::text casts on both current_setting arguments and on the empty-string
// literal, and upper-cases NULLIF. Pinned exactly (both the compose DB and every CI service container are
// postgres:18) so a silently rewritten predicate — e.g. one that drops the nullif and
// starts throwing 22P02 instead of failing closed — is caught, not just a renamed policy.
const policyQualNormalised = `(tenant_id = (NULLIF(current_setting('app.current_tenant'::text, true), ''::text))::uuid)`

// seedIdempotencyKey inserts one idempotency_keys row as the superuser (BYPASSRLS, so
// seeding needs neither tenant context nor an INSERT grant) and returns a cleanup func.
// The row MUST be cleaned up explicitly: unlike every M4-01 table, idempotency_keys has no
// FK on tenant_id, so the harness's teardown of tenants A/B does not reap it.
func seedIdempotencyKey(t *testing.T, tenantID, key string) (cleanup func()) {
	t.Helper()
	if _, err := h.super.Exec(context.Background(),
		`INSERT INTO idempotency_keys (tenant_id, key) VALUES ($1, $2)`, tenantID, key,
	); err != nil {
		t.Fatalf("seed idempotency_keys (tenant %s, key %q): %v", tenantID, key, err)
	}
	return func() {
		_, _ = h.super.Exec(context.Background(),
			`DELETE FROM idempotency_keys WHERE tenant_id = $1 AND key = $2`, tenantID, key)
	}
}

// IK-01: the grant matrix is exactly SELECT + INSERT for invoice_app, and nothing at all
// for invoice_tenant_reader. This is the catalog half of the append-only guarantee; IK-02/
// IK-03/IK-07 are the behavioural half (an error at runtime), and both halves are needed —
// a privilege granted but never exercised by a test would otherwise sit unnoticed.
//
// Asked as the SUPERUSER on purpose. information_schema.role_table_grants shows only rows
// visible to the current role (as invoice_app it lists invoice_app's own grants and no
// others), so the "reader holds nothing" claim cannot be proven from the app pool through
// that view; has_table_privilege asked as the superuser answers for any role.
func TestRLS_IdempotencyKeysGrantMatrixIsSelectInsertOnly(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	for _, c := range []struct {
		role string
		priv string
		want bool
	}{
		{"invoice_app", "SELECT", true},
		{"invoice_app", "INSERT", true},
		{"invoice_app", "UPDATE", false},
		{"invoice_app", "DELETE", false},
		{"invoice_app", "TRUNCATE", false},
		{"invoice_app", "REFERENCES", false},
		// The reader is the one cross-tenant enumeration identity — it must not hold
		// even SELECT on the dedupe ledger (behavioural proof in IK-07).
		{"invoice_tenant_reader", "SELECT", false},
		{"invoice_tenant_reader", "INSERT", false},
		{"invoice_tenant_reader", "UPDATE", false},
		{"invoice_tenant_reader", "DELETE", false},
		{"invoice_tenant_reader", "TRUNCATE", false},
	} {
		var got bool
		if err := h.super.QueryRow(ctx,
			`SELECT has_table_privilege($1, 'public.idempotency_keys', $2)`, c.role, c.priv,
		).Scan(&got); err != nil {
			t.Fatalf("has_table_privilege(%q, idempotency_keys, %q): %v", c.role, c.priv, err)
		}
		if got != c.want {
			t.Errorf("has_table_privilege(%q, idempotency_keys, %q) = %v, want %v — the M2-08 grant "+
				"is exactly `GRANT SELECT, INSERT ON idempotency_keys TO invoice_app` and nothing to "+
				"invoice_tenant_reader (M5-01 Core AC-5: the ledger must not be re-granted)",
				c.role, c.priv, got, c.want)
		}
	}
}

// IK-02: append-only. invoice_app has no UPDATE grant, so an UPDATE of its OWN, visible,
// same-tenant row is refused at the GRANT layer (42501) before RLS is evaluated. The error
// alone is not proof — the row is re-read as the superuser afterwards to confirm it really
// did not change (a re-grant that let the UPDATE through would flip both halves).
func TestRLS_IdempotencyKeysAppUpdateRefused(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	key := "IK-02-" + uuid.NewString()
	cleanup := seedIdempotencyKey(t, h.tenantA, key)
	defer cleanup()

	var before string
	if err := h.super.QueryRow(ctx,
		`SELECT created_at::text FROM idempotency_keys WHERE tenant_id = $1 AND key = $2`, h.tenantA, key,
	).Scan(&before); err != nil {
		t.Fatalf("read created_at before refused UPDATE: %v", err)
	}

	err := db.WithinTenantTx(ctx, h.app, h.tenantA, func(tx pgx.Tx) error {
		_, e := tx.Exec(ctx,
			`UPDATE idempotency_keys SET created_at = now() WHERE tenant_id = $1 AND key = $2`, h.tenantA, key)
		return e
	})
	if err == nil {
		t.Fatal("app-role UPDATE of its own idempotency_keys row succeeded, want permission denied " +
			"(SQLSTATE 42501) — invoice_app must hold SELECT + INSERT only")
	}
	if code := pgCode(err); code != "42501" {
		t.Fatalf("app-role UPDATE on idempotency_keys: SQLSTATE = %q, want 42501 (insufficient_privilege): %v", code, err)
	}

	var after string
	if err := h.super.QueryRow(ctx,
		`SELECT created_at::text FROM idempotency_keys WHERE tenant_id = $1 AND key = $2`, h.tenantA, key,
	).Scan(&after); err != nil {
		t.Fatalf("read created_at after refused UPDATE: %v", err)
	}
	if after != before {
		t.Errorf("created_at after refused UPDATE = %q, want unchanged %q", after, before)
	}
}

// IK-03: append-only. invoice_app has no DELETE grant either — a same-tenant DELETE of a
// row it can otherwise see is refused (42501), and the row must survive. Deleting a ledger
// entry would silently re-arm a duplicate submission, so this is the guarantee that makes
// the dedupe ledger authoritative rather than advisory.
func TestRLS_IdempotencyKeysAppDeleteRefused(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	key := "IK-03-" + uuid.NewString()
	cleanup := seedIdempotencyKey(t, h.tenantA, key)
	defer cleanup()

	err := db.WithinTenantTx(ctx, h.app, h.tenantA, func(tx pgx.Tx) error {
		_, e := tx.Exec(ctx, `DELETE FROM idempotency_keys WHERE tenant_id = $1 AND key = $2`, h.tenantA, key)
		return e
	})
	if err == nil {
		t.Fatal("app-role DELETE on idempotency_keys succeeded, want permission denied (SQLSTATE 42501) — " +
			"a deletable dedupe ledger would re-arm duplicate submissions")
	}
	if code := pgCode(err); code != "42501" {
		t.Fatalf("app-role DELETE on idempotency_keys: SQLSTATE = %q, want 42501 (insufficient_privilege): %v", code, err)
	}

	if n := mustCount(t, h.super,
		`SELECT count(*) FROM idempotency_keys WHERE tenant_id = $1 AND key = $2`, h.tenantA, key,
	); n != 1 {
		t.Errorf("row count after refused DELETE = %d, want 1 (row must survive)", n)
	}
}

// IK-04: the RLS posture is ENABLE + FORCE (so even the owning invoice_migrator is bound by
// the policy), and exactly ONE policy exists on the table — named tenant_isolation, cmd
// ALL, applying to {public} (the migration writes no TO clause), with the M2-08 predicate.
//
// Every catalog query here filters on schemaname='public' AND tablename='idempotency_keys':
// the name `tenant_isolation` is reused by most tenant-owned tables in this schema, so an
// unfiltered pg_policies query would assert nothing about this table.
func TestRLS_IdempotencyKeysForceRLSAndSinglePolicy(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	var rowSecurity, forceRowSecurity bool
	if err := h.super.QueryRow(ctx,
		`SELECT c.relrowsecurity, c.relforcerowsecurity
		   FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
		  WHERE n.nspname = 'public' AND c.relname = 'idempotency_keys'`,
	).Scan(&rowSecurity, &forceRowSecurity); err != nil {
		t.Fatalf("query pg_class RLS flags for idempotency_keys: %v", err)
	}
	if !rowSecurity {
		t.Error("pg_class.relrowsecurity = false — RLS is not ENABLEd on idempotency_keys")
	}
	if !forceRowSecurity {
		t.Error("pg_class.relforcerowsecurity = false — FORCE RLS is off, so the owning invoice_migrator " +
			"would bypass tenant_isolation entirely")
	}

	if n := mustCount(t, h.super,
		`SELECT count(*) FROM pg_policies WHERE schemaname = 'public' AND tablename = 'idempotency_keys'`,
	); n != 1 {
		t.Fatalf("policies on idempotency_keys = %d, want exactly 1 (tenant_isolation) — an added or "+
			"removed policy changes the isolation guarantee", n)
	}

	var (
		name, cmd string
		roles     []string
		qual      *string
		withCheck *string
	)
	if err := h.super.QueryRow(ctx,
		`SELECT policyname, cmd, roles::text[], qual, with_check
		   FROM pg_policies WHERE schemaname = 'public' AND tablename = 'idempotency_keys'`,
	).Scan(&name, &cmd, &roles, &qual, &withCheck); err != nil {
		t.Fatalf("query pg_policies for idempotency_keys: %v", err)
	}
	if name != "tenant_isolation" {
		t.Errorf("policyname = %q, want %q", name, "tenant_isolation")
	}
	if cmd != "ALL" {
		t.Errorf("policy cmd = %q, want %q (the USING clause must cover SELECT and INSERT alike)", cmd, "ALL")
	}
	if len(roles) != 1 || roles[0] != "public" {
		t.Errorf("policy roles = %v, want [public] — the M2-08 policy has no TO clause, so narrowing it "+
			"to a named role would leave other roles unfiltered", roles)
	}
	if qual == nil {
		t.Fatal("pg_policies.qual is NULL — the tenant_isolation policy has no USING clause")
	}
	if *qual != policyQualNormalised {
		t.Errorf("pg_policies.qual =\n\t%s\nwant (Postgres's normalised deparse of the M2-08 USING clause):\n\t%s",
			*qual, policyQualNormalised)
	}
	// Deliberately asserting NULL: the migration writes only USING, and Postgres applies
	// it implicitly as the INSERT WITH CHECK without materialising it in the catalog.
	// A non-NULL value here means someone added an explicit WITH CHECK — which could
	// legitimately differ from qual and must be reviewed, not silently accepted.
	if withCheck != nil {
		t.Errorf("pg_policies.with_check = %q, want NULL — M2-08 writes no explicit WITH CHECK "+
			"(Postgres reuses USING implicitly); an explicit one may diverge from qual", *withCheck)
	}
}

// IK-05: the PRIMARY KEY is exactly (tenant_id, key), in that column order — the PK IS the
// dedupe constraint, so its shape is the deduplication semantics. A single-column PK on
// `key` would collide across tenants; the reverse order would change the btree's prefix and
// with it every tenant-scoped lookup's plan.
//
// Filters contype='p' explicitly: PG18 also stores NOT NULL constraints in pg_constraint
// (contype='n'), so this table carries five constraint rows, not one.
func TestRLS_IdempotencyKeysPrimaryKeyIsTenantAndKey(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	var pkName string
	if err := h.super.QueryRow(ctx,
		`SELECT conname FROM pg_constraint
		  WHERE conrelid = 'public.idempotency_keys'::regclass AND contype = 'p'`,
	).Scan(&pkName); err != nil {
		t.Fatalf("query the primary-key constraint on idempotency_keys (contype='p'): %v", err)
	}
	if pkName != "idempotency_keys_pkey" {
		t.Errorf("primary-key constraint name = %q, want %q", pkName, "idempotency_keys_pkey")
	}

	rows, err := h.super.Query(ctx,
		`SELECT a.attname
		   FROM pg_constraint c
		   CROSS JOIN LATERAL unnest(c.conkey) WITH ORDINALITY AS k(attnum, ord)
		   JOIN pg_attribute a ON a.attrelid = c.conrelid AND a.attnum = k.attnum
		  WHERE c.conrelid = 'public.idempotency_keys'::regclass AND c.contype = 'p'
		  ORDER BY k.ord`)
	if err != nil {
		t.Fatalf("query primary-key columns: %v", err)
	}
	defer rows.Close()

	var cols []string
	for rows.Next() {
		var col string
		if e := rows.Scan(&col); e != nil {
			t.Fatalf("scan primary-key column: %v", e)
		}
		cols = append(cols, col)
	}
	if e := rows.Err(); e != nil {
		t.Fatalf("iterate primary-key columns: %v", e)
	}

	want := []string{"tenant_id", "key"}
	if len(cols) != len(want) {
		t.Fatalf("primary-key columns = %v, want %v (the PK IS the dedupe constraint)", cols, want)
	}
	for i := range want {
		if cols[i] != want[i] {
			t.Errorf("primary-key column %d = %q, want %q (order is load-bearing: tenant_id must be "+
				"the btree prefix)", i+1, cols[i], want[i])
		}
	}
}

// IK-06: tenant isolation on the write path. A tx scoped to tenant A may insert its own
// row (the positive half — without it, a table that refused every INSERT would pass the
// negative half vacuously), but an insert naming tenant B is refused by the policy's
// implicit WITH CHECK, SQLSTATE 42501. The refusal is mutation-verified: no B-named row
// exists afterwards when read as the superuser (which sees past RLS entirely).
func TestRLS_IdempotencyKeysCrossTenantInsertRefused(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	ownKey := "IK-06-own-" + uuid.NewString()
	defer func() {
		_, _ = h.super.Exec(context.Background(),
			`DELETE FROM idempotency_keys WHERE tenant_id = $1 AND key = $2`, h.tenantA, ownKey)
	}()

	// Positive half: A's own INSERT commits and is visible back to A under RLS.
	if err := db.WithinTenantTx(ctx, h.app, h.tenantA, func(tx pgx.Tx) error {
		_, e := tx.Exec(ctx, `INSERT INTO idempotency_keys (tenant_id, key) VALUES ($1, $2)`, h.tenantA, ownKey)
		return e
	}); err != nil {
		t.Fatalf("own-tenant INSERT into idempotency_keys: want success, got: %v", err)
	}
	if err := db.WithinTenantTx(ctx, h.app, h.tenantA, func(tx pgx.Tx) error {
		if n := mustCount(t, tx, `SELECT count(*) FROM idempotency_keys WHERE key = $1`, ownKey); n != 1 {
			t.Errorf("own row visible to A = %d, want 1", n)
		}
		return nil
	}); err != nil {
		t.Fatalf("WithinTenantTx (read back own row): %v", err)
	}

	// Negative half: the same tx context may not write a row named for tenant B.
	crossKey := "IK-06-cross-" + uuid.NewString()
	err := db.WithinTenantTx(ctx, h.app, h.tenantA, func(tx pgx.Tx) error {
		_, e := tx.Exec(ctx, `INSERT INTO idempotency_keys (tenant_id, key) VALUES ($1, $2)`, h.tenantB, crossKey)
		return e
	})
	assertRLSViolation(t, err)

	if n := mustCount(t, h.super,
		`SELECT count(*) FROM idempotency_keys WHERE tenant_id = $1 AND key = $2`, h.tenantB, crossKey,
	); n != 0 {
		t.Errorf("tenant-B rows after refused cross-tenant INSERT = %d, want 0", n)
	}
}

// IK-07: invoice_tenant_reader holds no grant on idempotency_keys at all, so a bare SELECT
// as that role fails at the GRANT layer (42501) before RLS is evaluated. This is the
// behavioural counterpart to IK-01's catalog check: information_schema views hide grants
// belonging to other roles, so only the live refusal proves the ledger was never exposed to
// the cross-tenant enumeration identity.
func TestRLS_IdempotencyKeysReaderSelectRefused(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	var n int
	err := h.reader.QueryRow(ctx, `SELECT count(*) FROM idempotency_keys`).Scan(&n)
	if err == nil {
		t.Fatal("invoice_tenant_reader SELECT on idempotency_keys succeeded, want permission denied " +
			"(SQLSTATE 42501) — the reader must hold no grant on the dedupe ledger")
	}
	if code := pgCode(err); code != "42501" {
		t.Fatalf("invoice_tenant_reader SELECT on idempotency_keys: SQLSTATE = %q, want 42501 "+
			"(insufficient_privilege): %v", code, err)
	}
}

// IK-08 (beyond the plan's 7 specs): the idempotency_key_length CHECK bounds `key` to
// (0, 255] characters — a blank key would dedupe unrelated jobs and an unbounded one could
// overflow the PK btree. Pinned here because M5-01-03's submission_jobs.idempotency_key is
// required to carry the SAME bound; if this ledger's bound ever moves, that column's must
// move with it, and a drifted pair would let a job be accepted whose ledger row cannot be
// written. Each attempt runs in its own tx — a rejected statement poisons the surrounding
// one — and asserts both rejection boundaries plus the accepted maximum.
func TestRLS_IdempotencyKeysKeyLengthCheck(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	// Both rejection boundaries: empty, and one character past the maximum.
	for _, c := range []struct {
		name string
		key  string
	}{
		{"empty key", ""},
		{"256-character key", strings.Repeat("k", 256)},
	} {
		err := db.WithinTenantTx(ctx, h.app, h.tenantA, func(tx pgx.Tx) error {
			_, e := tx.Exec(ctx, `INSERT INTO idempotency_keys (tenant_id, key) VALUES ($1, $2)`, h.tenantA, c.key)
			return e
		})
		if err == nil {
			// Not reachable on a correct schema, but if the CHECK were dropped the row would
			// commit — remove it so the failure does not leak into later cases.
			_, _ = h.super.Exec(context.Background(),
				`DELETE FROM idempotency_keys WHERE tenant_id = $1 AND key = $2`, h.tenantA, c.key)
			t.Errorf("INSERT with %s succeeded, want CHECK violation (SQLSTATE 23514) from "+
				"idempotency_key_length", c.name)
			continue
		}
		if code := pgCode(err); code != "23514" {
			t.Errorf("INSERT with %s: SQLSTATE = %q, want 23514 (check_violation): %v", c.name, code, err)
		}
	}

	// The accepted maximum: exactly 255 characters round-trips, proving the bound is
	// (0, 255] and not something narrower.
	maxKey := strings.Repeat("m", 255)
	defer func() {
		_, _ = h.super.Exec(context.Background(),
			`DELETE FROM idempotency_keys WHERE tenant_id = $1 AND key = $2`, h.tenantA, maxKey)
	}()
	if err := db.WithinTenantTx(ctx, h.app, h.tenantA, func(tx pgx.Tx) error {
		_, e := tx.Exec(ctx, `INSERT INTO idempotency_keys (tenant_id, key) VALUES ($1, $2)`, h.tenantA, maxKey)
		return e
	}); err != nil {
		t.Fatalf("INSERT with a 255-character key: want success (the CHECK's inclusive upper bound), got: %v", err)
	}

	// And the CHECK is a real, validated constraint on this table, not just an app-level
	// convention that happened to reject the two probes above.
	var def string
	if err := h.super.QueryRow(ctx,
		`SELECT pg_get_constraintdef(oid) FROM pg_constraint
		  WHERE conrelid = 'public.idempotency_keys'::regclass
		    AND contype = 'c' AND conname = 'idempotency_key_length'`,
	).Scan(&def); err != nil {
		t.Fatalf("query the idempotency_key_length CHECK in pg_constraint (contype='c'): %v", err)
	}
	for _, want := range []string{"char_length(key) > 0", "char_length(key) <= 255"} {
		if !strings.Contains(def, want) {
			t.Errorf("idempotency_key_length definition %q does not contain %q", def, want)
		}
	}
}

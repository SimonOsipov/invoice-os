// RLS, grant and constraint suite for `import_mappings`. failIfUndefinedImportMappings turns
// a not-yet-migrated table into a self-explaining 42P01 message — the shape every case here
// degrades to before the migration lands.
//
// Rows are seeded per test, never in harness.seed(): a missing table must fail only these
// cases, not the whole package. Each rejected statement gets its own db.WithinTenantTx or
// standalone superuser call, because a failed statement poisons the surrounding transaction.
package db_test

import (
	"context"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/SimonOsipov/invoice-os/internal/platform/db"
)

// imInsert names every column, so a test that means to violate one CHECK cannot silently
// trip a NOT NULL instead.
const imInsert = `INSERT INTO import_mappings (id, tenant_id, entity_id, column_signature, mapping)
	VALUES ($1, $2, $3, $4, $5::jsonb)`

// imUpsert is the save path's upsert, run here verbatim.
const imUpsert = `INSERT INTO import_mappings (tenant_id, entity_id, column_signature, mapping)
	VALUES ($1, $2, $3, $4::jsonb)
	ON CONFLICT (tenant_id, entity_id, column_signature)
	DO UPDATE SET mapping = EXCLUDED.mapping, saved_at = now()
	RETURNING id, saved_at`

// imMapping is a body the mapping CHECK admits: an object carrying invoice_number.
const imMapping = `{"invoice_number":"Invoice No"}`

// failIfUndefinedImportMappings turns the pre-migration failure mode into a self-explaining
// message instead of a raw driver error. Returns true when it fired.
func failIfUndefinedImportMappings(t *testing.T, what string, err error) bool {
	t.Helper()
	if pgCode(err) == "42P01" {
		t.Fatalf("%s: undefined_table (42P01) — the import_mappings migration is not applied yet: %v", what, err)
		return true
	}
	return false
}

// seedImportMapping inserts one row as the superuser (BYPASSRLS, so seeding needs neither
// tenant context nor an INSERT grant) and returns its id plus a cleanup func.
func seedImportMapping(t *testing.T, tenantID, entityID, signature, mappingJSON string) (id string, cleanup func()) {
	t.Helper()
	ctx := context.Background()
	id = uuid.NewString()
	if _, err := h.super.Exec(ctx, imInsert, id, tenantID, entityID, signature, mappingJSON); err != nil {
		if failIfUndefinedImportMappings(t, "seed import_mappings", err) {
			return "", nil
		}
		t.Fatalf("seed import_mappings: %v", err)
	}
	return id, func() {
		_, _ = h.super.Exec(context.Background(), `DELETE FROM import_mappings WHERE id = $1`, id)
	}
}

// imAsApp runs one statement as invoice_app under tenantID and returns its error.
func imAsApp(t *testing.T, tenantID, sql string, args ...any) error {
	t.Helper()
	ctx := context.Background()
	return db.WithinTenantTx(ctx, h.app, tenantID, func(tx pgx.Tx) error {
		_, e := tx.Exec(ctx, sql, args...)
		return e
	})
}

// imAssertPGCode names the constraint too: a CHECK renamed or widened trips a different one,
// and the SQLSTATE alone cannot tell those apart.
func imAssertPGCode(t *testing.T, err error, wantCode, wantConstraint, what string) {
	t.Helper()
	if failIfUndefinedImportMappings(t, what, err) {
		return
	}
	if got := pgCode(err); got != wantCode {
		t.Fatalf("%s returned SQLSTATE %q (%v), want %q on %s", what, got, err, wantCode, wantConstraint)
	}
	if wantConstraint == "" {
		return
	}
	if got := pgConstraint(err); got != wantConstraint {
		t.Errorf("%s tripped constraint %q, want %q", what, got, wantConstraint)
	}
}

// imRowCount counts rows by id as the superuser, which sees past the policy.
func imRowCount(t *testing.T, id string) int {
	t.Helper()
	return mustCount(t, h.super, `SELECT count(*) FROM import_mappings WHERE id = $1`, id)
}

// IM-RLS-01: two tenants can hold the identical signature; each sees only its own row and
// mapping, and an unfiltered count agrees.
func TestRLS_ImportMappingsTwoTenantsSameSignatureSeeOnlyTheirOwn(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	entityA, cleanupEntityA := seedBusinessEntity(t, h.tenantA, "IM Shared-Signature A Co")
	defer cleanupEntityA()
	entityB, cleanupEntityB := seedBusinessEntity(t, h.tenantB, "IM Shared-Signature B Co")
	defer cleanupEntityB()

	sig := docHash()
	_, cleanupA := seedImportMapping(t, h.tenantA, entityA, sig, `{"invoice_number":"A's column"}`)
	defer cleanupA()
	_, cleanupB := seedImportMapping(t, h.tenantB, entityB, sig, `{"invoice_number":"B's column"}`)
	defer cleanupB()

	for _, c := range []struct {
		tenant, wantMapping string
	}{
		{h.tenantA, "A's column"},
		{h.tenantB, "B's column"},
	} {
		err := db.WithinTenantTx(ctx, h.app, c.tenant, func(tx pgx.Tx) error {
			if n := mustCount(t, tx, `SELECT count(*) FROM import_mappings WHERE column_signature = $1`, sig); n != 1 {
				t.Errorf("tenant %s sees %d rows for the shared signature, want 1", c.tenant, n)
			}
			var got string
			if e := tx.QueryRow(ctx,
				`SELECT mapping->>'invoice_number' FROM import_mappings WHERE column_signature = $1`, sig,
			).Scan(&got); e != nil {
				return e
			}
			if got != c.wantMapping {
				t.Errorf("tenant %s mapping = %q, want %q", c.tenant, got, c.wantMapping)
			}
			if n := mustCount(t, tx, `SELECT count(*) FROM import_mappings`); n != 1 {
				t.Errorf("an unfiltered count under tenant %s = %d, want 1", c.tenant, n)
			}
			return nil
		})
		if failIfUndefinedImportMappings(t, "tenant-scoped read of the shared signature", err) {
			return
		}
		if err != nil {
			t.Fatalf("tenant %s read: %v", c.tenant, err)
		}
	}
}

// IM-RLS-02: a tx scoped to A cannot land a row it labels as B's, even with a valid B entity.
func TestRLS_ImportMappingsCrossTenantInsertRefused(t *testing.T) {
	h := requireHarness(t)

	entityB, cleanupEntityB := seedBusinessEntity(t, h.tenantB, "IM Cross-Tenant Insert Co")
	defer cleanupEntityB()

	id := uuid.NewString()
	defer func() {
		_, _ = h.super.Exec(context.Background(), `DELETE FROM import_mappings WHERE id = $1`, id)
	}()

	err := imAsApp(t, h.tenantA, imInsert, id, h.tenantB, entityB, docHash(), imMapping)
	imAssertPGCode(t, err, "42501", "", "an insert of tenant B's row scoped to tenant A")

	if n := imRowCount(t, id); n != 0 {
		t.Errorf("rows after the refused cross-tenant insert = %d, want 0", n)
	}
}

// IM-RLS-03: relabelling A's own row into B is refused, and the row keeps its tenant.
func TestRLS_ImportMappingsOwnRowReassignmentRefused(t *testing.T) {
	h := requireHarness(t)

	entityA, cleanupEntityA := seedBusinessEntity(t, h.tenantA, "IM Reassignment Co")
	defer cleanupEntityA()
	rowA, cleanupRow := seedImportMapping(t, h.tenantA, entityA, docHash(), imMapping)
	defer cleanupRow()

	err := imAsApp(t, h.tenantA, `UPDATE import_mappings SET tenant_id = $1 WHERE id = $2`, h.tenantB, rowA)
	imAssertPGCode(t, err, "42501", "", "reassigning a row's tenant_id to another tenant")

	var owner string
	if e := h.super.QueryRow(context.Background(),
		`SELECT tenant_id::text FROM import_mappings WHERE id = $1`, rowA).Scan(&owner); e != nil {
		t.Fatalf("read tenant_id for %s: %v", rowA, e)
	}
	if owner != h.tenantA {
		t.Errorf("tenant_id after the refused reassignment = %s, want the seeded %s", owner, h.tenantA)
	}
}

// IM-RLS-04: FORCE subjects the migrator too — scoped to A, it cannot insert B's row even
// though the row would otherwise be a valid (tenant, entity) pair for B.
func TestRLS_ImportMappingsOwnerInsertRefusedUnderForce(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	entityB, cleanupEntityB := seedBusinessEntity(t, h.tenantB, "IM Owner Force Co")
	defer cleanupEntityB()

	id := uuid.NewString()
	defer func() {
		_, _ = h.super.Exec(context.Background(), `DELETE FROM import_mappings WHERE id = $1`, id)
	}()

	err := db.WithinTenantTx(ctx, h.mig, h.tenantA, func(tx pgx.Tx) error {
		_, e := tx.Exec(ctx, imInsert, id, h.tenantB, entityB, docHash(), imMapping)
		return e
	})
	imAssertPGCode(t, err, "42501", "", "the migrator inserting tenant B's row while scoped to tenant A")

	if n := imRowCount(t, id); n != 0 {
		t.Errorf("rows after the refused owner insert = %d, want 0", n)
	}
}

// IM-RLS-05: an unset app.current_tenant yields NULL, which matches no row.
func TestRLS_ImportMappingsMissingContextFailsClosed(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	entityA, cleanupEntityA := seedBusinessEntity(t, h.tenantA, "IM No-Context A Co")
	defer cleanupEntityA()
	entityB, cleanupEntityB := seedBusinessEntity(t, h.tenantB, "IM No-Context B Co")
	defer cleanupEntityB()
	rowA, cleanupA := seedImportMapping(t, h.tenantA, entityA, docHash(), imMapping)
	defer cleanupA()
	rowB, cleanupB := seedImportMapping(t, h.tenantB, entityB, docHash(), imMapping)
	defer cleanupB()

	// Control: the zero below means nothing unless the rows are there to hide.
	if n := mustCount(t, h.super, `SELECT count(*) FROM import_mappings WHERE id = ANY($1)`, []string{rowA, rowB}); n != 2 {
		t.Fatalf("superuser sees %d of the two seeded rows, want 2", n)
	}

	tx, err := h.app.Begin(ctx)
	if err != nil {
		t.Fatalf("begin without a tenant GUC: %v", err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()

	var n int
	err = tx.QueryRow(ctx, `SELECT count(*) FROM import_mappings`).Scan(&n)
	if failIfUndefinedImportMappings(t, "count with no tenant set", err) {
		return
	}
	if err != nil {
		t.Fatalf("count with no tenant set: %v", err)
	}
	if n != 0 {
		t.Errorf("rows visible with app.current_tenant unset = %d, want 0", n)
	}
}

// IM-RLS-06: RLS admits the row (tenant_id matches), so the composite FK is what refuses an
// entity that belongs to another tenant.
func TestRLS_ImportMappingsCompositeFKRejectsAnotherTenantsEntity(t *testing.T) {
	h := requireHarness(t)

	entityB, cleanupEntityB := seedBusinessEntity(t, h.tenantB, "IM Cross-Entity Co")
	defer cleanupEntityB()

	id := uuid.NewString()
	defer func() {
		_, _ = h.super.Exec(context.Background(), `DELETE FROM import_mappings WHERE id = $1`, id)
	}()

	err := imAsApp(t, h.tenantA, imInsert, id, h.tenantA, entityB, docHash(), imMapping)
	imAssertPGCode(t, err, "23503", "import_mappings_tenant_entity_fk",
		"an insert naming tenant A with tenant B's entity_id")

	if n := imRowCount(t, id); n != 0 {
		t.Errorf("rows after the refused cross-tenant entity insert = %d, want 0", n)
	}
}

// IM-RLS-07: no DELETE grant — only the entity's cascade may remove a row.
func TestRLS_ImportMappingsDeleteRefused(t *testing.T) {
	h := requireHarness(t)

	entityA, cleanupEntityA := seedBusinessEntity(t, h.tenantA, "IM Delete Co")
	defer cleanupEntityA()
	rowA, cleanupRow := seedImportMapping(t, h.tenantA, entityA, docHash(), imMapping)
	defer cleanupRow()

	err := imAsApp(t, h.tenantA, `DELETE FROM import_mappings WHERE id = $1`, rowA)
	imAssertPGCode(t, err, "42501", "", "invoice_app deleting its own tenant's row")

	if n := imRowCount(t, rowA); n != 1 {
		t.Errorf("rows after the refused delete = %d, want 1", n)
	}
}

// IM-RLS-08: no reader grant — the reader enumerates tenants and has no use for mappings.
func TestRLS_ImportMappingsReaderHasNoGrant(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	var n int
	err := h.reader.QueryRow(ctx, `SELECT count(*) FROM import_mappings`).Scan(&n)
	if failIfUndefinedImportMappings(t, "reader SELECT", err) {
		return
	}
	if err == nil {
		t.Fatalf("invoice_tenant_reader SELECT on import_mappings returned %d rows, want permission denied (42501)", n)
	}
	if code := pgCode(err); code != "42501" {
		t.Fatalf("invoice_tenant_reader SELECT on import_mappings: SQLSTATE = %q, want 42501: %v", code, err)
	}
}

// IM-RLS-09: the upsert replaces A's row in place — same id, new mapping, saved_at not
// earlier than before — and never touches B's row that shares the signature.
func TestRLS_ImportMappingsUpsertReplacesInPlace(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	entityA, cleanupEntityA := seedBusinessEntity(t, h.tenantA, "IM Upsert A Co")
	defer cleanupEntityA()
	entityB, cleanupEntityB := seedBusinessEntity(t, h.tenantB, "IM Upsert B Co")
	defer cleanupEntityB()

	sig := docHash()
	rowA, cleanupA := seedImportMapping(t, h.tenantA, entityA, sig, `{"invoice_number":"Old A"}`)
	defer cleanupA()
	rowB, cleanupB := seedImportMapping(t, h.tenantB, entityB, sig, `{"invoice_number":"Old B"}`)
	defer cleanupB()

	var beforeSaved time.Time
	if err := h.super.QueryRow(ctx, `SELECT saved_at FROM import_mappings WHERE id = $1`, rowA).Scan(&beforeSaved); err != nil {
		t.Fatalf("read saved_at before the upsert: %v", err)
	}
	var beforeB string
	if err := h.super.QueryRow(ctx,
		`SELECT to_jsonb(import_mappings)::text FROM import_mappings WHERE id = $1`, rowB).Scan(&beforeB); err != nil {
		t.Fatalf("snapshot tenant B's row before the upsert: %v", err)
	}

	var gotID string
	var gotSaved time.Time
	err := db.WithinTenantTx(ctx, h.app, h.tenantA, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, imUpsert, h.tenantA, entityA, sig, `{"invoice_number":"New A"}`).Scan(&gotID, &gotSaved)
	})
	if failIfUndefinedImportMappings(t, "the upsert as invoice_app", err) {
		return
	}
	if err != nil {
		t.Fatalf("upsert as invoice_app: %v", err)
	}
	if gotID != rowA {
		t.Errorf("upsert returned id %s, want the existing row's id %s — a second row was inserted instead of replaced",
			gotID, rowA)
	}
	if gotSaved.Before(beforeSaved) {
		t.Errorf("saved_at after the upsert = %v, want not earlier than %v", gotSaved, beforeSaved)
	}

	var gotMapping string
	if err := h.super.QueryRow(ctx,
		`SELECT mapping->>'invoice_number' FROM import_mappings WHERE id = $1`, rowA).Scan(&gotMapping); err != nil {
		t.Fatalf("read back tenant A's mapping: %v", err)
	}
	if gotMapping != "New A" {
		t.Errorf("tenant A's mapping after the upsert = %q, want %q", gotMapping, "New A")
	}
	if n := imRowCount(t, rowA); n != 1 {
		t.Errorf("rows for tenant A's id after the upsert = %d, want 1 — replaced in place, not duplicated", n)
	}

	var afterB string
	if err := h.super.QueryRow(ctx,
		`SELECT to_jsonb(import_mappings)::text FROM import_mappings WHERE id = $1`, rowB).Scan(&afterB); err != nil {
		t.Fatalf("snapshot tenant B's row after the upsert: %v", err)
	}
	if afterB != beforeB {
		t.Errorf("tenant B's row changed by tenant A's upsert: before %s, after %s", beforeB, afterB)
	}
}

// IM-RLS-10: every discriminating malformed row is refused by name, and a well-formed
// control row still lands — proving the refusals are the CHECKs and not a broken statement.
func TestRLS_ImportMappingsChecksRefuseMalformedRows(t *testing.T) {
	h := requireHarness(t)

	entityA, cleanupEntityA := seedBusinessEntity(t, h.tenantA, "IM Checks Co")
	defer cleanupEntityA()

	var probes []string
	defer func() {
		_, _ = h.super.Exec(context.Background(), `DELETE FROM import_mappings WHERE id = ANY($1)`, probes)
	}()

	for _, c := range []struct {
		what       string
		signature  string
		mapping    string
		constraint string
	}{
		{"a non-hex signature", "abc", imMapping, "import_mappings_column_signature_check"},
		{"a 65-char signature", strings.Repeat("a", 65), imMapping, "import_mappings_column_signature_check"},
		{"an uppercase-hex signature", strings.Repeat("A", 64), imMapping, "import_mappings_column_signature_check"},
		{"a mapping whose key names are array elements", strings.Repeat("a", 64),
			`["invoice_number"]`, "import_mappings_mapping_check"},
		// ? also matches a scalar string, so only the typeof conjunct refuses; an array-only
		// exclusion would admit it.
		{"a scalar-string mapping", strings.Repeat("a", 64), `"invoice_number"`, "import_mappings_mapping_check"},
		{"a mapping missing invoice_number", strings.Repeat("a", 64), `{"total":"T"}`, "import_mappings_mapping_check"},
	} {
		id := uuid.NewString()
		probes = append(probes, id)
		err := imAsApp(t, h.tenantA, imInsert, id, h.tenantA, entityA, c.signature, c.mapping)
		imAssertPGCode(t, err, "23514", c.constraint, "a row with "+c.what)
		if n := imRowCount(t, id); n != 0 {
			t.Errorf("rows after the refused row (%s) = %d, want 0", c.what, n)
		}
	}

	okID := uuid.NewString()
	probes = append(probes, okID)
	err := imAsApp(t, h.tenantA, imInsert, okID, h.tenantA, entityA, strings.Repeat("a", 64), `{"invoice_number":""}`)
	if failIfUndefinedImportMappings(t, "a well-formed control row", err) {
		return
	}
	if err != nil {
		t.Fatalf("a well-formed control row: want success, got: %v", err)
	}
	if n := imRowCount(t, okID); n != 1 {
		t.Errorf("rows after the control insert = %d, want 1", n)
	}
}

// IM-RLS-11: a saved mapping has no meaning once its entity is gone, so the FK cascades.
func TestRLS_ImportMappingsEntityDeleteCascades(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	entityA, cleanupEntityA := seedBusinessEntity(t, h.tenantA, "IM Cascade Co")
	defer cleanupEntityA()
	rowA, cleanupRow := seedImportMapping(t, h.tenantA, entityA, docHash(), imMapping)
	defer cleanupRow()

	if _, err := h.super.Exec(ctx, `DELETE FROM business_entities WHERE id = $1`, entityA); err != nil {
		t.Fatalf("delete the entity: %v — the import_mappings FK must cascade, not restrict", err)
	}

	if n := imRowCount(t, rowA); n != 0 {
		t.Errorf("rows for the deleted entity = %d, want 0", n)
	}
}

// IM-RLS-12: the signature is unique per entity, not per tenant — a second entity of the
// same tenant may reuse the same signature.
func TestRLS_ImportMappingsUniqueSignaturePerEntity(t *testing.T) {
	h := requireHarness(t)

	entityA, cleanupEntityA := seedBusinessEntity(t, h.tenantA, "IM Unique Co")
	defer cleanupEntityA()
	entityA2, cleanupEntityA2 := seedBusinessEntity(t, h.tenantA, "IM Unique Co Second Entity")
	defer cleanupEntityA2()

	sig := docHash()
	rowA, cleanupRow := seedImportMapping(t, h.tenantA, entityA, sig, imMapping)
	defer cleanupRow()

	dupID := uuid.NewString()
	defer func() {
		_, _ = h.super.Exec(context.Background(), `DELETE FROM import_mappings WHERE id = $1`, dupID)
	}()
	dupErr := imAsApp(t, h.tenantA, imInsert, dupID, h.tenantA, entityA, sig, imMapping)
	imAssertPGCode(t, dupErr, "23505", "import_mappings_tenant_entity_signature_uq", "a repeated (tenant, entity, signature)")
	if n := imRowCount(t, dupID); n != 0 {
		t.Errorf("rows after the refused duplicate = %d, want 0", n)
	}

	otherID := uuid.NewString()
	defer func() {
		_, _ = h.super.Exec(context.Background(), `DELETE FROM import_mappings WHERE id = $1`, otherID)
	}()
	err := imAsApp(t, h.tenantA, imInsert, otherID, h.tenantA, entityA2, sig, imMapping)
	if failIfUndefinedImportMappings(t, "the same signature under a second entity", err) {
		return
	}
	if err != nil {
		t.Fatalf("the same signature under a second entity of tenant A: want success, got: %v", err)
	}
	if n := imRowCount(t, otherID); n != 1 {
		t.Errorf("rows after the second-entity insert = %d, want 1", n)
	}

	if n := imRowCount(t, rowA); n != 1 {
		t.Errorf("the original row = %d, want 1 — untouched by the refused duplicate", n)
	}
}

// IM-RLS-13: with no existing row, the upsert's INSERT branch lands one row with the id and
// saved_at defaults.
func TestRLS_ImportMappingsAppInsertsItsOwnRowWithDefaults(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	entityA, cleanupEntityA := seedBusinessEntity(t, h.tenantA, "IM Defaults Co")
	defer cleanupEntityA()

	sig := docHash()
	var gotID string
	var gotSaved time.Time
	err := db.WithinTenantTx(ctx, h.app, h.tenantA, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, imUpsert, h.tenantA, entityA, sig, imMapping).Scan(&gotID, &gotSaved)
	})
	if failIfUndefinedImportMappings(t, "the app's own-tenant upsert", err) {
		return
	}
	if err != nil {
		t.Fatalf("invoice_app's own-tenant upsert: want success, got: %v", err)
	}
	defer func() {
		_, _ = h.super.Exec(context.Background(), `DELETE FROM import_mappings WHERE id = $1`, gotID)
	}()

	if gotID == "" {
		t.Error("upsert returned an empty id, want a generated uuid")
	}
	if gotSaved.IsZero() {
		t.Error("upsert returned a zero saved_at, want the now() default")
	}
	if n := imRowCount(t, gotID); n != 1 {
		t.Errorf("rows after the insert = %d, want 1", n)
	}
}

// IM-RLS-14: the catalog half of the isolation posture. ENABLE alone would let the owner
// bypass the policy, and a TO clause on tenant_isolation would leave unnamed roles unbound.
func TestRLS_ImportMappingsForceRLSAndPolicyDeclared(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	var enabled, forced bool
	err := h.super.QueryRow(ctx,
		`SELECT relrowsecurity, relforcerowsecurity
		   FROM pg_class WHERE oid = 'public.import_mappings'::regclass`,
	).Scan(&enabled, &forced)
	if failIfUndefinedImportMappings(t, "read pg_class for import_mappings", err) {
		return
	}
	if err != nil {
		t.Fatalf("read pg_class for import_mappings: %v", err)
	}
	if !enabled || !forced {
		t.Errorf("import_mappings relrowsecurity/relforcerowsecurity = %v/%v, want true/true "+
			"(ENABLE alone would let the migrator/owner bypass the policy)", enabled, forced)
	}

	type policy struct {
		roles []string
		cmd   string
		qual  string
	}
	got := map[string]policy{}
	rows, err := h.super.Query(ctx,
		`SELECT policyname, roles::text[], cmd, coalesce(qual, '')
		   FROM pg_policies WHERE schemaname = 'public' AND tablename = 'import_mappings'`)
	if err != nil {
		t.Fatalf("query pg_policies for import_mappings: %v", err)
	}
	defer rows.Close()
	for rows.Next() {
		var name string
		var p policy
		if e := rows.Scan(&name, &p.roles, &p.cmd, &p.qual); e != nil {
			t.Fatalf("scan pg_policies row: %v", e)
		}
		got[name] = p
	}
	if e := rows.Err(); e != nil {
		t.Fatalf("iterate pg_policies rows: %v", e)
	}

	if len(got) != 1 {
		t.Fatalf("policies on import_mappings = %d (%v), want exactly 1 (tenant_isolation)", len(got), got)
	}
	p, ok := got["tenant_isolation"]
	if !ok {
		t.Fatalf("no tenant_isolation policy on import_mappings; got %v", got)
	}
	if p.cmd != "ALL" {
		t.Errorf("tenant_isolation cmd = %q, want ALL — a per-command policy leaves the other commands unbound", p.cmd)
	}
	if !reflect.DeepEqual(p.roles, []string{"public"}) {
		t.Errorf("tenant_isolation roles = %v, want [public] — a TO clause would leave unnamed roles unbound", p.roles)
	}
	if p.qual == "" {
		t.Error("tenant_isolation carries an empty USING; it would admit every row")
	}
}

// IM-RLS-15: invoice_app holds exactly SELECT, INSERT, UPDATE; invoice_tenant_reader holds
// nothing.
func TestRLS_ImportMappingsGrantMatrix(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	for _, c := range []struct {
		role string
		priv string
		want bool
	}{
		{"invoice_app", "SELECT", true},
		{"invoice_app", "INSERT", true},
		{"invoice_app", "UPDATE", true},
		{"invoice_app", "DELETE", false},
		{"invoice_app", "TRUNCATE", false},
		{"invoice_app", "REFERENCES", false},
		{"invoice_tenant_reader", "SELECT", false},
		{"invoice_tenant_reader", "INSERT", false},
		{"invoice_tenant_reader", "UPDATE", false},
		{"invoice_tenant_reader", "DELETE", false},
		{"invoice_tenant_reader", "TRUNCATE", false},
		{"invoice_tenant_reader", "REFERENCES", false},
	} {
		var got bool
		err := h.super.QueryRow(ctx,
			`SELECT has_table_privilege($1, 'public.import_mappings', $2)`, c.role, c.priv,
		).Scan(&got)
		if failIfUndefinedImportMappings(t, "has_table_privilege("+c.role+", "+c.priv+")", err) {
			return
		}
		if err != nil {
			t.Fatalf("has_table_privilege(%q, import_mappings, %q): %v", c.role, c.priv, err)
		}
		if got != c.want {
			t.Errorf("has_table_privilege(%q, import_mappings, %q) = %v, want %v — the grant is exactly "+
				"SELECT, INSERT, UPDATE to invoice_app and nothing to invoice_tenant_reader",
				c.role, c.priv, got, c.want)
		}
	}
}

// NOT NULL is the only guard on these columns: a CHECK admits NULL, a composite FK skips a
// NULL member, and UNIQUE treats NULLs as distinct.
func TestRLS_ImportMappingsNullColumnsRefused(t *testing.T) {
	h := requireHarness(t)

	entityA, cleanupEntityA := seedBusinessEntity(t, h.tenantA, "IM Null Columns Co")
	defer cleanupEntityA()

	const insertAll = `INSERT INTO import_mappings (id, tenant_id, entity_id, column_signature, mapping, saved_at)
		VALUES ($1, $2, $3, $4, $5::jsonb, $6)`

	var probes []string
	defer func() {
		_, _ = h.super.Exec(context.Background(), `DELETE FROM import_mappings WHERE id = ANY($1)`, probes)
	}()

	now := time.Now()
	for _, c := range []struct {
		column                              string
		entity, signature, mapping, savedAt any
	}{
		{"entity_id", nil, docHash(), imMapping, now},
		{"column_signature", entityA, nil, imMapping, now},
		{"mapping", entityA, docHash(), nil, now},
		{"saved_at", entityA, docHash(), imMapping, nil},
	} {
		id := uuid.NewString()
		probes = append(probes, id)
		err := imAsApp(t, h.tenantA, insertAll, id, h.tenantA, c.entity, c.signature, c.mapping, c.savedAt)
		imAssertPGCode(t, err, "23502", "", "a row with a NULL "+c.column)
		if got := earColumnName(err); got != c.column {
			t.Errorf("a NULL %s: the not-null violation names column %q", c.column, got)
		}
		if n := imRowCount(t, id); n != 0 {
			t.Errorf("rows after the refused NULL %s = %d, want 0", c.column, n)
		}
	}

	okID := uuid.NewString()
	probes = append(probes, okID)
	if err := imAsApp(t, h.tenantA, insertAll, okID, h.tenantA, entityA, docHash(), imMapping, now); err != nil {
		t.Fatalf("a well-formed control row through the same statement: want success, got: %v", err)
	}
	if n := imRowCount(t, okID); n != 1 {
		t.Errorf("rows after the control insert = %d, want 1", n)
	}
}

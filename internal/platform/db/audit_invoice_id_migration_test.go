// Test-first (RED) suite for AUDIT-04-11: the audit_log.invoice_id GENERATED ALWAYS ...
// STORED column and its tenant-leading index. Unlike AUDIT-01's entity_id (a join through
// invoices, filled by a backfill + BEFORE INSERT trigger), invoice_id is row-local — the
// normalised, grammar-guarded payload value cast straight to uuid — so there is no
// backfill bracket and no trigger to reason about; ADD COLUMN ... STORED alone recomputes
// every existing row during the rewrite.
//
// Every DB-backed case leads with requireInvoiceIDColumn so a run against the
// pre-migration schema fails on an attributable "column does not exist yet" message
// instead of a raw pgx 42703 surfacing from a later query.
package db_test

import (
	"context"
	"errors"
	"io/fs"
	"reflect"
	"regexp"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/SimonOsipov/invoice-os/internal/audit"
	"github.com/SimonOsipov/invoice-os/migrations"
)

const (
	auditInvoiceIDMigrationGlob = "*_audit_log_invoice_id_column_and_index.sql"
	auditInvoiceIDColumn        = "invoice_id"
	auditInvoiceIDIndex         = "audit_log_tenant_invoice_created_idx"
)

// --- migration file location -------------------------------------------------------

func auditInvoiceIDMigrationName(t *testing.T) string {
	t.Helper()
	matches, err := fs.Glob(migrations.FS, auditInvoiceIDMigrationGlob)
	if err != nil {
		t.Fatalf("glob %s in migrations.FS: %v", auditInvoiceIDMigrationGlob, err)
	}
	if len(matches) != 1 {
		t.Fatalf("migrations.FS holds %d files matching %s (%v), want exactly 1",
			len(matches), auditInvoiceIDMigrationGlob, matches)
	}
	return matches[0]
}

// auditInvoiceIDSection reuses auditEntitySectionOf (audit_entity_backfill_migration_test.go),
// which is generic over the migration name.
func auditInvoiceIDSection(t *testing.T, section string) string {
	t.Helper()
	return auditEntitySectionOf(t, auditInvoiceIDMigrationName(t), section)
}

func auditInvoiceIDFileBody(t *testing.T) string {
	t.Helper()
	name := auditInvoiceIDMigrationName(t)
	b, err := fs.ReadFile(migrations.FS, name)
	if err != nil {
		t.Fatalf("read %s from migrations.FS: %v", name, err)
	}
	return string(b)
}

// requireInvoiceIDColumn is the leading assertion of every DB-backed case: a schema still
// at the pre-migration state fails here, attributably, rather than inside a later query.
func requireInvoiceIDColumn(t *testing.T, ctx context.Context) {
	t.Helper()
	var present bool
	if err := h.super.QueryRow(ctx,
		`SELECT count(*) = 1 FROM information_schema.columns
		   WHERE table_schema = 'public' AND table_name = 'audit_log' AND column_name = $1`,
		auditInvoiceIDColumn).Scan(&present); err != nil {
		t.Fatalf("check audit_log.%s presence: %v", auditInvoiceIDColumn, err)
	}
	if !present {
		t.Fatalf("column %s does not exist yet", auditInvoiceIDColumn)
	}
}

// --- writing and reading back --------------------------------------------------------

// recordInvoiceIDRow writes one row through audit.Record itself (the real production
// entry point, invoice_app role) rather than a bespoke INSERT, so a passing case also
// proves the generated column survives Record's unmodified write path (AC-7).
func recordInvoiceIDRow(t *testing.T, ctx context.Context, tenant, event string, payload map[string]any) {
	t.Helper()
	tx, err := h.app.Begin(ctx)
	if err != nil {
		t.Fatalf("begin app tx: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }() // no-op once committed
	if _, err := tx.Exec(ctx, `SELECT set_config('app.current_tenant', $1, true)`, tenant); err != nil {
		t.Fatalf("set tenant context: %v", err)
	}
	if err := audit.Record(ctx, tx, "invoice-id-fixture", event, payload); err != nil {
		t.Fatalf("audit.Record(%s): %v", event, err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit audit.Record(%s): %v", event, err)
	}
}

// auditInvoiceIDsByEvent reads event -> invoice_id back for one tenant via the superuser
// pool (BYPASSRLS), so no tenant GUC dance is needed on the read side.
func auditInvoiceIDsByEvent(t *testing.T, ctx context.Context, tenant string) map[string]*string {
	t.Helper()
	rows, err := h.super.Query(ctx, `SELECT event, invoice_id FROM audit_log WHERE tenant_id = $1`, tenant)
	if err != nil {
		t.Fatalf("read back audit rows for %s: %v", tenant, err)
	}
	defer rows.Close()
	out := map[string]*string{}
	for rows.Next() {
		var event string
		var invoiceID *string
		if err := rows.Scan(&event, &invoiceID); err != nil {
			t.Fatalf("scan audit row: %v", err)
		}
		out[event] = invoiceID
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate audit rows: %v", err)
	}
	if len(out) == 0 {
		t.Fatalf("no audit rows visible for tenant %s — the fixture never landed, so every "+
			"assertion below would be vacuous", tenant)
	}
	return out
}

func assertAuditInvoiceID(t *testing.T, got map[string]*string, event, want string) {
	t.Helper()
	v, ok := got[event]
	if !ok {
		t.Errorf("event %s: no audit row read back", event)
		return
	}
	if v == nil {
		t.Errorf("event %s: invoice_id IS NULL, want %s", event, want)
		return
	}
	if *v != want {
		t.Errorf("event %s: invoice_id = %s, want %s", event, *v, want)
	}
}

func assertAuditInvoiceIDNull(t *testing.T, got map[string]*string, event string) {
	t.Helper()
	v, ok := got[event]
	if !ok {
		t.Errorf("event %s: no audit row read back", event)
		return
	}
	if v != nil {
		t.Errorf("event %s: invoice_id = %s, want NULL", event, *v)
	}
}

// --- AC-2: both payload spellings, and only invoice-scoped events ------------------

// Row 1: every one of the 17 invoice-scoped events, both spellings, via audit.Record.
func TestRLS_AuditInvoiceIDColumnMatchesBothPayloadSpellings(t *testing.T) {
	requireHarness(t)
	ctx := context.Background()
	requireInvoiceIDColumn(t, ctx)
	requireEventList(t, "auditRuleAEvents", auditRuleAEvents, 10)
	requireEventList(t, "auditRuleBEvents", auditRuleBEvents, 7)

	tenant := uuid.NewString()
	want := map[string]string{}
	for _, event := range auditRuleAEvents {
		id := uuid.NewString()
		recordInvoiceIDRow(t, ctx, tenant, event, map[string]any{"id": id})
		want[event] = id
	}
	for _, event := range auditRuleBEvents {
		id := uuid.NewString()
		recordInvoiceIDRow(t, ctx, tenant, event, map[string]any{"invoice_id": id})
		want[event] = id
	}
	if len(want) != 17 {
		t.Fatalf("wrote %d distinct events, want 17 (10 bare-id + 7 invoice_id-spelled)", len(want))
	}

	got := auditInvoiceIDsByEvent(t, ctx, tenant)
	if len(got) != 17 {
		t.Fatalf("read back %d rows, want 17 — the population floor for this fixture", len(got))
	}
	for event, id := range want {
		assertAuditInvoiceID(t, got, event, id)
	}
}

// Row 2: document.created is the adversarial needle — its payload carries a REAL invoice
// id under `id`, exactly like invoice.created's, so a resolver dispatching on which key is
// PRESENT rather than on the event NAME would wrongly resolve it. invoice.created in the
// same fixture is the positive control the two workspace-level NULLs need.
func TestRLS_AuditInvoiceIDIsNullForNonInvoiceEvents(t *testing.T) {
	requireHarness(t)
	ctx := context.Background()
	requireInvoiceIDColumn(t, ctx)

	tenant := uuid.NewString()
	needleInvoiceID := uuid.NewString()
	seedAuditEntityRow(t, tenant, "document.created", auditPayloadJSON("id", needleInvoiceID))
	seedAuditEntityRow(t, tenant, "portfolio.entity.updated", auditPayloadJSON("id", uuid.NewString()))
	seedAuditEntityRow(t, tenant, "approval_policy.published", auditPayloadJSON("policy_id", uuid.NewString()))
	controlID := uuid.NewString()
	seedAuditEntityRow(t, tenant, "invoice.created", auditPayloadJSON("id", controlID))

	got := auditInvoiceIDsByEvent(t, ctx, tenant)
	if len(got) != 4 {
		t.Fatalf("read back %d rows, want 4 — the fixture never landed", len(got))
	}
	for _, event := range []string{"document.created", "portfolio.entity.updated", "approval_policy.published"} {
		assertAuditInvoiceIDNull(t, got, event)
	}
	assertAuditInvoiceID(t, got, "invoice.created", controlID)
}

// Row 3: every spelling §6 admits normalises to the same canonical id, and the four
// live reject shapes (malformed, unclosed brace, hyphen off the 4-hex boundary, key
// absent entirely) all yield NULL rather than aborting the insert with 22P02.
func TestRLS_AuditInvoiceIDAcceptsEverySpellingUUIDInAccepts(t *testing.T) {
	requireHarness(t)
	ctx := context.Background()
	requireInvoiceIDColumn(t, ctx)
	requireEventList(t, "auditRuleAEvents", auditRuleAEvents, 10)

	tenant := uuid.NewString()
	base := uuid.NewString()
	bare := strings.ReplaceAll(base, "-", "")
	if len(bare) != 32 || bare == base {
		t.Fatalf("hyphenless id %q is %d chars, want a 32-char restyling of %s", bare, len(bare), base)
	}
	// Hyphen after every 4 hex digits (8 groups), not the canonical 8-4-4-4-12 —
	// still admitted by contract §6's grammar.
	nonCanonical := bare[0:4] + "-" + bare[4:8] + "-" + bare[8:12] + "-" + bare[12:16] + "-" +
		bare[16:20] + "-" + bare[20:24] + "-" + bare[24:28] + "-" + bare[28:32]

	resolves := map[string]string{
		"invoice.created":          base,             // canonical, the positive control
		"invoice.kept_as_is":       bare,             // hyphenless
		"invoice.unkept_as_is":     "{" + base + "}", // brace-wrapped canonical
		"invoice.resolved_outside": "{" + bare + "}", // brace-wrapped hyphenless
		"invoice.validated":        strings.ToUpper(base),
		"invoice.approval_armed":   nonCanonical,
	}
	refuses := map[string]string{
		"invoice.transitioned":       bare[:3] + "-" + bare[3:], // hyphen off the 4-hex boundary
		"invoice.unresolved_outside": "{" + base,                // unclosed brace
		"invoice.updated":            "not-a-uuid",
	}
	const absentKeyEvent = "invoice.approval_cancelled"
	if got, want := len(resolves)+len(refuses)+1, len(auditRuleAEvents); got != want {
		t.Fatalf("spelling table covers %d events (incl. the absent-key case), want %d (auditRuleAEvents)", got, want)
	}

	for event, spelling := range resolves {
		seedAuditEntityRow(t, tenant, event, auditPayloadJSON("id", spelling))
	}
	for event, spelling := range refuses {
		seedAuditEntityRow(t, tenant, event, auditPayloadJSON("id", spelling))
	}
	seedAuditEntityRow(t, tenant, absentKeyEvent, `{"note": "no id key here"}`)

	got := auditInvoiceIDsByEvent(t, ctx, tenant)
	if want := len(resolves) + len(refuses) + 1; len(got) != want {
		t.Fatalf("read back %d rows, want %d — the fixture never landed", len(got), want)
	}
	for event := range resolves {
		assertAuditInvoiceID(t, got, event, base)
	}
	for event := range refuses {
		assertAuditInvoiceIDNull(t, got, event)
	}
	assertAuditInvoiceIDNull(t, got, absentKeyEvent)
}

// Row 13 (new): an event carrying BOTH `id` and `invoice_id` keys, with DIFFERENT
// uuids, resolves to the key its dispatch branch names -- not whichever key happens to
// be present. The existing fixtures never populate both keys on the same row.
func TestRLS_AuditInvoiceIDDispatchUsesTheBranchNamedKeyNotWhicheverIsPresent(t *testing.T) {
	requireHarness(t)
	ctx := context.Background()
	requireInvoiceIDColumn(t, ctx)

	tenant := uuid.NewString()
	ruleAWant, ruleAOther := uuid.NewString(), uuid.NewString()
	ruleBWant, ruleBOther := uuid.NewString(), uuid.NewString()
	recordInvoiceIDRow(t, ctx, tenant, "invoice.created",
		map[string]any{"id": ruleAWant, "invoice_id": ruleAOther})
	recordInvoiceIDRow(t, ctx, tenant, "submission.accepted",
		map[string]any{"id": ruleBOther, "invoice_id": ruleBWant})

	got := auditInvoiceIDsByEvent(t, ctx, tenant)
	if len(got) != 2 {
		t.Fatalf("read back %d rows, want 2 — the fixture never landed", len(got))
	}
	assertAuditInvoiceID(t, got, "invoice.created", ruleAWant)
	assertAuditInvoiceID(t, got, "submission.accepted", ruleBWant)
}

// Row 14 (new): reject shapes beyond TestRLS_AuditInvoiceIDAcceptsEverySpellingUUIDInAccepts's
// three (malformed, unclosed brace, off-boundary hyphen) -- a nested JSON value, an
// unmatched-close-only brace, a doubled close brace, degenerate brace pairs, a brace in
// the interior, an empty string, a JSON null, and off-by-one lengths. None may raise
// 22P02 on the ALTER's table-wide rewrite; all must resolve NULL.
func TestRLS_AuditInvoiceIDRejectsAdversarialBraceAndLengthShapes(t *testing.T) {
	requireHarness(t)
	ctx := context.Background()
	requireInvoiceIDColumn(t, ctx)

	tenant := uuid.NewString()
	base := uuid.NewString()
	bare := strings.ReplaceAll(base, "-", "")
	rejects := map[string]any{
		"invoice.transitioned":       map[string]any{"inner": base}, // nested object under the key
		"invoice.unresolved_outside": "",                            // empty string
		"invoice.updated":            nil,                           // JSON null
		"invoice.validated":          bare + "xxx",                  // 35 chars
		"invoice.kept_as_is":         bare + "xxxxx",                // 37 chars
		"invoice.unkept_as_is":       "{" + bare + "}extra}",        // doubled close brace ('{abc}def}' shape)
		"invoice.resolved_outside":   "{}",                          // brace pair, no content
		"invoice.approval_armed":     "{",                           // open-brace-only
		"invoice.approval_cancelled": "}",                           // close-brace-only
	}
	const controlEvent = "invoice.created"
	if got, want := len(rejects)+1, len(auditRuleAEvents); got != want {
		t.Fatalf("reject-shape table covers %d events (incl. the control), want %d (auditRuleAEvents)", got, want)
	}
	for event, payload := range rejects {
		recordInvoiceIDRow(t, ctx, tenant, event, map[string]any{"id": payload})
	}
	controlID := uuid.NewString()
	recordInvoiceIDRow(t, ctx, tenant, controlEvent, map[string]any{"id": controlID})

	got := auditInvoiceIDsByEvent(t, ctx, tenant)
	if want := len(rejects) + 1; len(got) != want {
		t.Fatalf("read back %d rows, want %d — the fixture never landed", len(got), want)
	}
	for event := range rejects {
		assertAuditInvoiceIDNull(t, got, event)
	}
	assertAuditInvoiceID(t, got, controlEvent, controlID)
}

// --- AC-4: the row-count ceiling guard, executed, not read --------------------------

// auditInvoiceIDLoweredGuard returns the Up body with the size guard's threshold rewritten
// to 1, so it trips against the live (populated) table. The file on disk is never touched.
func auditInvoiceIDLoweredGuard(t *testing.T) string {
	t.Helper()
	body := auditInvoiceIDSection(t, "Up")
	re := regexp.MustCompile(`(?i)IF\s+[A-Za-z_][A-Za-z0-9_]*\s*>\s*([0-9_]+)\s+THEN`)
	m := re.FindAllStringSubmatchIndex(body, -1)
	if len(m) != 1 {
		t.Fatalf("the Up body holds %d `IF <var> > <threshold> THEN` size guards, want exactly 1 — "+
			"without one the migration cannot abort above the threshold", len(m))
	}
	return body[:m[0][2]] + "1" + body[m[0][3]:]
}

// Row 4: lowering the threshold to 1 and EXECUTING the Up must abort with 54000
// program_limit_exceeded. A static regex read of the guard's text would pass on an
// RLS-blinded guard that counts 0 rows and never fires — the exact defect this row
// exists to catch (see D-31 and the migrator's NOBYPASSRLS/FORCE RLS ownership).
func TestRLS_AuditInvoiceIDMigrationSizeGuardAbortsAndRollsBack(t *testing.T) {
	requireHarness(t)
	ctx := context.Background()
	requireInvoiceIDColumn(t, ctx)

	before := auditLogCatalogState(t, ctx, h.super)
	tx := migratorTx(t, ctx)
	if _, err := tx.Exec(ctx, auditInvoiceIDSection(t, "Down")); err != nil {
		t.Fatalf("Down body failed (is the migration applied? run `make migrate-up`): %v", err)
	}
	_, err := tx.Exec(ctx, auditInvoiceIDLoweredGuard(t))
	assertAuditGuardTripped(t, err)
	_ = tx.Rollback(ctx)

	after := auditLogCatalogState(t, ctx, h.super)
	if !reflect.DeepEqual(before, after) {
		t.Errorf("audit_log catalog state after the aborted migration = %+v, want the pre-migration %+v", after, before)
	}
}

// --- AC-5: no extension ------------------------------------------------------------

// Row 5: file-only. invoice_migrator has no CREATE on the database (D-32); pg_trgm and
// any other extension are unreachable from this migration by construction.
func TestRLS_AuditInvoiceIDMigrationCreatesNoExtension(t *testing.T) {
	body := auditEntityStripComments(auditInvoiceIDFileBody(t))
	if strings.TrimSpace(body) == "" {
		t.Fatalf("the comment-stripped migration body is empty — the absence assertion below would pass vacuously")
	}
	if strings.Contains(strings.ToUpper(body), "CREATE EXTENSION") {
		t.Errorf("the migration contains CREATE EXTENSION; invoice_migrator has no CREATE on the database (D-32)")
	}
}

// --- AC-6: reversible ----------------------------------------------------------------

func auditInvoiceIDColumnPresentIn(t *testing.T, ctx context.Context, tx pgx.Tx) bool {
	t.Helper()
	var present bool
	if err := tx.QueryRow(ctx,
		`SELECT count(*) = 1 FROM information_schema.columns
		   WHERE table_schema = 'public' AND table_name = 'audit_log' AND column_name = $1`,
		auditInvoiceIDColumn).Scan(&present); err != nil {
		t.Fatalf("check %s presence in tx: %v", auditInvoiceIDColumn, err)
	}
	return present
}

func auditInvoiceIDIndexPresentIn(t *testing.T, ctx context.Context, tx pgx.Tx) bool {
	t.Helper()
	var present bool
	if err := tx.QueryRow(ctx,
		`SELECT count(*) = 1 FROM pg_indexes WHERE schemaname = 'public' AND tablename = 'audit_log' AND indexname = $1`,
		auditInvoiceIDIndex).Scan(&present); err != nil {
		t.Fatalf("check %s presence in tx: %v", auditInvoiceIDIndex, err)
	}
	return present
}

// Row 6: Down then Up on one migrator connection, rolled back — the column and index are
// absent after Down and present again after Up.
func TestRLS_AuditInvoiceIDMigrationIsReversible(t *testing.T) {
	requireHarness(t)
	ctx := context.Background()
	requireInvoiceIDColumn(t, ctx)

	tx := migratorTx(t, ctx)
	if _, err := tx.Exec(ctx, auditInvoiceIDSection(t, "Down")); err != nil {
		t.Fatalf("Down body failed (is the migration applied? run `make migrate-up`): %v", err)
	}
	if auditInvoiceIDColumnPresentIn(t, ctx, tx) {
		t.Errorf("audit_log still has %s after Down", auditInvoiceIDColumn)
	}
	if auditInvoiceIDIndexPresentIn(t, ctx, tx) {
		t.Errorf("%s still exists after Down", auditInvoiceIDIndex)
	}

	if _, err := tx.Exec(ctx, auditInvoiceIDSection(t, "Up")); err != nil {
		t.Fatalf("Up body failed: %v", err)
	}
	if !auditInvoiceIDColumnPresentIn(t, ctx, tx) {
		t.Errorf("audit_log lacks %s after Up", auditInvoiceIDColumn)
	}
	if !auditInvoiceIDIndexPresentIn(t, ctx, tx) {
		t.Errorf("%s is missing after Up", auditInvoiceIDIndex)
	}
}

// --- AC-2: STORED generated, and cannot be forged by an explicit INSERT ------------

// Row 10 (new): the property that justifies GENERATED ALWAYS ... STORED over a
// trigger-filled column — attgenerated = 's', and an INSERT naming invoice_id explicitly
// is refused before it can ever be forged.
func TestRLS_AuditInvoiceIDIsGeneratedAndCannotBeForged(t *testing.T) {
	requireHarness(t)
	ctx := context.Background()
	requireInvoiceIDColumn(t, ctx)

	var attgenerated string
	if err := h.super.QueryRow(ctx,
		`SELECT attgenerated FROM pg_attribute WHERE attrelid = 'audit_log'::regclass AND attname = $1`,
		auditInvoiceIDColumn).Scan(&attgenerated); err != nil {
		t.Fatalf("read pg_attribute.attgenerated for %s: %v", auditInvoiceIDColumn, err)
	}
	if attgenerated != "s" {
		t.Fatalf("audit_log.%s attgenerated = %q, want %q (STORED generated) — a plain or "+
			"trigger-filled column could be forged by an explicit INSERT", auditInvoiceIDColumn, attgenerated, "s")
	}

	_, err := h.super.Exec(ctx,
		`INSERT INTO audit_log (tenant_id, actor, event, payload, invoice_id) VALUES ($1, $2, $3, $4::jsonb, $5)`,
		uuid.NewString(), "forge-actor", "invoice.created", "{}", uuid.NewString())
	if err == nil {
		t.Fatalf("INSERT naming invoice_id explicitly succeeded, want it refused — a generated " +
			"column that accepts a forged value is not a generated column")
	}
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		t.Fatalf("want a Postgres error from the forged INSERT, got %v", err)
	}
	if pgErr.Code != "428C9" {
		t.Fatalf("forged INSERT raised SQLSTATE %s (%s), want 428C9 (generated column)", pgErr.Code, pgErr.Message)
	}
}

// --- AC-3: index column order, not merely presence ---------------------------------

// Row 11 (new) / AC-3: the index's column order and sort directions. Row 6
// (TestRLS_AuditInvoiceIDMigrationIsReversible) checks the index NAME only -- a QA
// mutation confirmed a wrongly-ordered index (invoice_id trailing instead of second)
// still passes that test. This row closes the gap.
func TestRLS_AuditInvoiceIDIndexColumnOrderMatchesTheSiblings(t *testing.T) {
	requireHarness(t)
	ctx := context.Background()
	requireInvoiceIDColumn(t, ctx)

	var def string
	if err := h.super.QueryRow(ctx, `SELECT pg_get_indexdef($1::regclass)`, auditInvoiceIDIndex).Scan(&def); err != nil {
		t.Fatalf("read indexdef for %s: %v", auditInvoiceIDIndex, err)
	}
	const want = "(tenant_id, invoice_id, created_at DESC, id DESC)"
	if !strings.HasSuffix(def, want) {
		t.Errorf("indexdef = %q, want it to end with %q -- tenant-leading, invoice_id second, "+
			"created_at DESC/id DESC trailing so a keyset page needs no Sort", def, want)
	}
}

// --- cross-tenant leak guard: invoice_id is not a new leak vector -------------------

// invoiceIDCountForTenant counts audit_log rows visible to invoice_app for one
// invoice_id, scoped to one tenant's RLS context.
func invoiceIDCountForTenant(t *testing.T, ctx context.Context, tenant, invoiceID string) int {
	t.Helper()
	tx, err := h.app.Begin(ctx)
	if err != nil {
		t.Fatalf("begin app tx: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }() // no-op once committed
	if _, err := tx.Exec(ctx, `SELECT set_config('app.current_tenant', $1, true)`, tenant); err != nil {
		t.Fatalf("set tenant context: %v", err)
	}
	return mustCount(t, tx, `SELECT count(*) FROM audit_log WHERE invoice_id = $1`, invoiceID)
}

// Row 12 (new): a row is invisible to another tenant even when queried by its exact
// invoice_id -- RLS scopes by tenant_id regardless of which column the WHERE names.
func TestRLS_AuditInvoiceIDCrossTenantRowIsInvisibleByInvoiceID(t *testing.T) {
	requireHarness(t)
	ctx := context.Background()
	requireInvoiceIDColumn(t, ctx)

	tenantA, tenantB := uuid.NewString(), uuid.NewString()
	invoiceID := uuid.NewString()
	recordInvoiceIDRow(t, ctx, tenantA, "invoice.created", map[string]any{"id": invoiceID})

	if n := invoiceIDCountForTenant(t, ctx, tenantB, invoiceID); n != 0 {
		t.Errorf("tenant B sees %d rows for tenant A's invoice_id, want 0", n)
	}
	// Non-vacuous: tenant A sees its own row by the same invoice_id.
	if n := invoiceIDCountForTenant(t, ctx, tenantA, invoiceID); n != 1 {
		t.Errorf("tenant A sees %d of its own rows by invoice_id, want 1", n)
	}
}

// Test-first (RED) suite for EXTR-08-06: the migration that adds a fourth ELSIF to
// audit_log_entity_for so extraction.field_corrected is attributed through the invoice it
// corrects. The rule must exist before the first row: the resolver runs at BEFORE INSERT
// and audit_log is append-only, so a row written before the rule is unscoped forever.
//
// Two idioms, as in audit_schema_test.go. The file cases read migrations.FS only and run
// unconditionally; the DB cases write through audit.Record as invoice_app under a tenant
// GUC, so the trigger and the SECURITY INVOKER resolver run exactly as production runs them.
package audit_test

import (
	"context"
	"io/fs"
	"strconv"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/SimonOsipov/invoice-os/internal/audit"
	"github.com/SimonOsipov/invoice-os/internal/platform/db"
	"github.com/SimonOsipov/invoice-os/migrations"
)

// The file cases anchor on the slug, not on a timestamp (goose stamps local time) and not
// on a content grep (three migrations define this resolver).
const extractionResolverSuffix = "_audit_log_entity_for_extraction.sql"

// newestMigrationBeforeExtractionResolver is the newest migration on this branch before
// this story. A reverse-order landing froze production deploys for nine days.
const newestMigrationBeforeExtractionResolver = 20260828114600

// auditThreeResolverMigration defines the body this story's Down must restore.
const auditThreeResolverMigration = "20260821135423_audit_log_entity_for_submission_failed.sql"

// generatedInvoiceIDMigration is the one migration allowed to declare audit_log.invoice_id.
const generatedInvoiceIDMigration = "20260822080722_audit_log_invoice_id_column_and_index.sql"

// --- file cases (no DB) ---------------------------------------------------------------

// T6-1: exactly one new migration file, sorting after the newest prior one. Falsifiable at
// 0 (never copied in / renamed) and at 2 — a second `make migrate-create` leaves a stray
// file behind, and this catches it at the count instead of on merge.
func TestExtraction_SingleResolverMigrationForThisStory(t *testing.T) {
	name := requireExtractionResolverMigration(t)

	if len(name) < 14 {
		t.Fatalf("migration name %q is shorter than a 14-digit goose timestamp", name)
	}
	stamp, err := strconv.ParseInt(name[:14], 10, 64)
	if err != nil {
		t.Fatalf("leading 14 chars of %q are not a goose timestamp: %v", name, err)
	}
	if stamp <= newestMigrationBeforeExtractionResolver {
		t.Errorf("migration timestamp = %d, want strictly greater than %d — a migration that "+
			"sorts before an already-applied one never runs on a live database",
			stamp, newestMigrationBeforeExtractionResolver)
	}
}

// T6-2: the whole migration runs in one transaction. The positive needle stops an empty or
// unreadable body from passing these absence assertions vacuously.
func TestExtraction_ResolverMigrationRunsInOneTransaction(t *testing.T) {
	name := requireExtractionResolverMigration(t)
	body := extractionReadMigration(t, name)

	if !strings.Contains(body, scopedGooseUp) {
		t.Fatalf("%s does not contain %q (%d bytes read) — the body is not a goose migration, "+
			"so the assertions below would pass vacuously", name, scopedGooseUp, len(body))
	}
	if strings.Contains(body, "NO TRANSACTION") {
		t.Errorf("%s declares NO TRANSACTION, want the whole migration in one transaction", name)
	}
	if strings.Contains(strings.ToUpper(body), "CONCURRENTLY") {
		t.Errorf("%s uses CONCURRENTLY, which cannot run inside goose's transaction", name)
	}
}

// T6-7 (file half): the Down is a CREATE OR REPLACE whose body is byte-identical to
// AUDIT-03's Up. A DROP is wrong — audit_log_set_entity calls the resolver on every insert,
// and AUDIT-01's own Down is what drops it.
func TestExtraction_ResolverMigrationDownRestoresTheAuditThreeBody(t *testing.T) {
	name := requireExtractionResolverMigration(t)
	down := extractionGooseSection(t, name, "Down")

	if strings.Contains(strings.ToUpper(down), "DROP FUNCTION") {
		t.Errorf("%s's Down drops audit_log_entity_for, want a CREATE OR REPLACE of AUDIT-03's body", name)
	}

	want := extractionResolverFnBody(t, auditThreeResolverMigration, "Up",
		extractionGooseSection(t, auditThreeResolverMigration, "Up"))
	got := extractionResolverFnBody(t, name, "Down", down)
	if got != want {
		t.Errorf("%s's Down body is %d bytes, AUDIT-03's Up body is %d bytes, and they differ — "+
			"the Down must restore the shipped body byte for byte", name, len(got), len(want))
	}

	// Control: without this, a Down that merely copied this story's own Up would satisfy
	// the comparison above if the extractor were reading the wrong section.
	up := extractionResolverFnBody(t, name, "Up", extractionGooseSection(t, name, "Up"))
	if up == want {
		t.Errorf("%s's Up body is byte-identical to AUDIT-03's — the migration replaces nothing", name)
	}
	if !strings.Contains(up, "extraction.field_corrected") {
		t.Errorf("%s's Up body never names extraction.field_corrected", name)
	}
	if strings.Contains(want, "extraction.field_corrected") {
		t.Errorf("AUDIT-03's body names extraction.field_corrected — the restore target is not the shipped body")
	}
}

// T6-6 (file half): the STORED generated column audit_log.invoice_id is declared once, by
// AUDIT-04's migration, and this story does not redeclare it. A generated expression is
// frozen in pg_attrdef; rewriting it would recompute new rows while stored rows keep the
// old value — a silent, permanent divergence on an append-only table.
func TestExtraction_GeneratedInvoiceIDColumnIsNotRewritten(t *testing.T) {
	names, err := fs.Glob(migrations.FS, "*.sql")
	if err != nil {
		t.Fatalf("glob migrations.FS: %v", err)
	}
	if len(names) == 0 {
		t.Fatalf("migrations.FS contains no *.sql files — the embed is broken, so the scan below reads nothing")
	}

	var declarers []string
	for _, name := range names {
		raw := extractionReadMigration(t, name)
		if strings.Contains(raw, "ADD COLUMN invoice_id uuid GENERATED ALWAYS AS") {
			declarers = append(declarers, name)
		}
	}
	// Control needle: the scan must find the one declaration that exists, or its
	// "nobody else declares it" conclusion is drawn from having read nothing.
	if len(declarers) != 1 || declarers[0] != generatedInvoiceIDMigration {
		t.Fatalf("migrations declaring audit_log.invoice_id = %v, want exactly [%s]",
			declarers, generatedInvoiceIDMigration)
	}

	story := extractionReadMigration(t, requireExtractionResolverMigration(t))
	for _, needle := range []string{"ALTER TABLE audit_log", "GENERATED ALWAYS"} {
		if strings.Contains(story, needle) {
			t.Errorf("the resolver migration contains %q — it may only CREATE OR REPLACE the "+
				"resolver function, never touch the table or its generated column", needle)
		}
	}
}

// T6-5, the fence. extraction.field_corrected is attributable but not yet emitted and
// carries no frontend label, so it is NOT in the shipped rule-A/B/C/D vocabulary; EXTR-14
// adds it when it emits it. EXTR-08-04 adds extraction.succeeded and .failed to rule D and
// must not add this one by reflex — doing so would assert the opposite of what the
// migration ships and would break the disjoint-and-sum arithmetic.
func TestExtraction_FieldCorrectedIsNotInTheWorkspaceVocabulary(t *testing.T) {
	const event = "extraction.field_corrected"
	ruleD := triggerRuleDPayloads(uuid.NewString())

	// Population floors: a shrunk fixture would make every absence check below vacuous.
	// Rule D's floor is a floor, not a pin — EXTR-08-04 legitimately grows it to 17.
	if len(triggerRuleAEvents) != 10 || len(triggerRuleBEvents) != 7 || len(triggerRuleCEvents) != 4 {
		t.Fatalf("rule A/B/C hold %d/%d/%d events, want 10/7/4",
			len(triggerRuleAEvents), len(triggerRuleBEvents), len(triggerRuleCEvents))
	}
	if len(ruleD) < 15 {
		t.Fatalf("rule-D payload map holds %d events, want at least 15", len(ruleD))
	}
	if len(readerFirmWideEvents) != 12 || len(readerDocumentEvents) != 3 {
		t.Fatalf("ScopeOf fixtures hold %d firm-wide and %d document events, want 12 and 3",
			len(readerFirmWideEvents), len(readerDocumentEvents))
	}

	// Control needles: each list must be able to report a hit, or "not found" means
	// "nothing was searched".
	for _, c := range []struct {
		name    string
		list    []string
		present string
	}{
		{"triggerRuleAEvents", triggerRuleAEvents, "invoice.created"},
		{"triggerRuleBEvents", triggerRuleBEvents, "submission.failed"},
		{"triggerRuleCEvents", triggerRuleCEvents, "portfolio.entity.created"},
		{"readerFirmWideEvents", readerFirmWideEvents, "workflow_role.created"},
		{"readerDocumentEvents", readerDocumentEvents, "document.read"},
	} {
		if !extractionContains(c.list, c.present) {
			t.Fatalf("control needle: %s does not hold %s, so its membership check reads nothing",
				c.name, c.present)
		}
		if extractionContains(c.list, event) {
			t.Errorf("%s holds %s — it is attributable through its invoice, not workspace-level", c.name, event)
		}
	}
	if _, ok := ruleD["document.created"]; !ok {
		t.Fatalf("control needle: triggerRuleDPayloads does not hold document.created")
	}
	if _, ok := ruleD[event]; ok {
		t.Errorf("triggerRuleDPayloads holds %s — rule D is what always resolves NULL", event)
	}

	// The behaviour the migration makes reachable: a resolved row reads company, and an
	// unresolved one reads unattributed, never workspace (the fail-safe direction).
	entity := uuid.NewString()
	if got := audit.ScopeOf(event, &entity); got != audit.ScopeCompany {
		t.Errorf("ScopeOf(%q, entityID set) = %q, want %q", event, got, audit.ScopeCompany)
	}
	if got := audit.ScopeOf(event, nil); got != audit.ScopeUnattributed {
		t.Errorf("ScopeOf(%q, nil) = %q, want %q", event, got, audit.ScopeUnattributed)
	}
}

// --- DB cases -------------------------------------------------------------------------

// T6-3: the write-time trigger attributes extraction.field_corrected to the invoice's
// entity. The positive assertion comes first so a resolver that always returned NULL
// cannot pass, and submission.accepted carrying the same payload is the fixture control.
func TestAudit_InsertTriggerResolvesExtractionFieldCorrected(t *testing.T) {
	f := requireFixture(t)
	requireInsertTrigger(t, f)
	fx := seedTriggerFixture(t, f)

	recordAudit(t, f, fx.tenant, "extraction.field_corrected",
		map[string]any{"arm": "resolves", "invoice_id": fx.invoice})
	recordAudit(t, f, fx.tenant, "submission.accepted",
		map[string]any{"arm": "control", "invoice_id": fx.invoice})

	rows := extractionAuditRows(t, f, fx.tenant)
	extractionAssertEntity(t, rows, "resolves", fx.entity)
	extractionAssertEntity(t, rows, "control", fx.entity)

	// T6-6 (live half): the STORED generated column dispatches on its own two event
	// lists, which this story does not touch. submission.accepted is in rule B and fills
	// invoice_id; extraction.field_corrected is in neither list and must stay NULL.
	if got := rows["control"].invoiceID; got == nil || *got != fx.invoice {
		t.Fatalf("control needle: submission.accepted's generated invoice_id = %v, want %s — "+
			"the column is not populating, so the NULL assertion below is vacuous",
			extractionShow(got), fx.invoice)
	}
	if got := rows["resolves"].invoiceID; got != nil {
		t.Errorf("extraction.field_corrected's generated invoice_id = %s, want NULL — rules A "+
			"and B did not move, so the column must not have been rewritten", *got)
	}
}

// T6-4: every unattributable shape resolves NULL and raises nothing. An error here would
// abort the BEFORE INSERT trigger and fail the write the row records, so all five arms go
// out in ONE transaction with a companion idempotency_keys row that proves it committed.
// The extraction.succeeded arm carries an IDENTICAL payload to the positive arm: it is
// what proves dispatch is event-scoped rather than key-scoped.
func TestAudit_InsertTriggerLeavesUnattributableExtractionCorrectionsNull(t *testing.T) {
	f := requireFixture(t)
	requireInsertTrigger(t, f)
	fx := seedTriggerFixture(t, f)
	ctx := context.Background()

	arms := []struct {
		arm     string
		event   string
		payload map[string]any
	}{
		{"resolves", "extraction.field_corrected", map[string]any{"invoice_id": fx.invoice}},
		{"absent-invoice", "extraction.field_corrected", map[string]any{"invoice_id": uuid.NewString()}},
		{"malformed", "extraction.field_corrected", map[string]any{"invoice_id": "not-a-uuid"}},
		{"no-key", "extraction.field_corrected", map[string]any{"field": "buyer_tin"}},
		{"wrong-event", "extraction.succeeded", map[string]any{"invoice_id": fx.invoice}},
	}

	key := uuid.NewString()
	if err := db.WithinTenantTx(ctx, f.app, fx.tenant, func(tx pgx.Tx) error {
		for _, a := range arms {
			payload := map[string]any{"arm": a.arm}
			for k, v := range a.payload {
				payload[k] = v
			}
			if e := audit.Record(ctx, tx, "extraction-fixture", a.event, payload); e != nil {
				return e
			}
		}
		_, e := tx.Exec(ctx, `INSERT INTO idempotency_keys (tenant_id, key) VALUES ($1, $2)`, fx.tenant, key)
		return e
	}); err != nil {
		t.Fatalf("an unattributable payload aborted the caller's transaction: %v", err)
	}
	if n := keyCount(t, f.app, fx.tenant, key); n != 1 {
		t.Errorf("idempotency_keys rows after commit = %d, want 1 — the transaction did not commit", n)
	}

	rows := extractionAuditRows(t, f, fx.tenant)
	if len(rows) != len(arms) {
		t.Fatalf("read back %d rows, want %d — the arms did not all land", len(rows), len(arms))
	}
	// Control needle first: without a resolving arm in the same test, every NULL below
	// would also hold on a database where the rule was never added.
	extractionAssertEntity(t, rows, "resolves", fx.entity)
	for _, arm := range []string{"absent-invoice", "malformed", "no-key", "wrong-event"} {
		extractionAssertEntityNull(t, rows, arm)
	}
}

// --- helpers ----------------------------------------------------------------------------

// requireExtractionResolverMigration returns this story's single migration, failing loudly
// at any count other than one. Callers may index the name only because this counted first.
func requireExtractionResolverMigration(t *testing.T) string {
	t.Helper()
	all, err := fs.Glob(migrations.FS, "*.sql")
	if err != nil {
		t.Fatalf("glob migrations.FS: %v", err)
	}
	if len(all) == 0 {
		t.Fatalf("migrations.FS contains no *.sql files — the embed is broken")
	}
	var matches []string
	for _, name := range all {
		if strings.HasSuffix(name, extractionResolverSuffix) {
			matches = append(matches, name)
		}
	}
	if len(matches) != 1 {
		t.Fatalf("migrations matching *%s = %d %v, want exactly 1 (scanned %d files)",
			extractionResolverSuffix, len(matches), matches, len(all))
	}
	return matches[0]
}

func extractionReadMigration(t *testing.T, name string) string {
	t.Helper()
	raw, err := fs.ReadFile(migrations.FS, name)
	if err != nil {
		t.Fatalf("read %s from migrations.FS: %v", name, err)
	}
	return string(raw)
}

// extractionGooseSection returns one goose section of a named migration, verbatim.
func extractionGooseSection(t *testing.T, name, section string) string {
	t.Helper()
	raw := extractionReadMigration(t, name)
	up, ok := scopedGooseUpOf(raw)
	if !ok {
		t.Fatalf("%s: want both %q and %q, in that order", name, scopedGooseUp, scopedGooseDown)
	}
	if section == "Up" {
		return up
	}
	return raw[strings.Index(raw, scopedGooseDown)+len(scopedGooseDown):]
}

// extractionResolverFnBody returns the CREATE OR REPLACE ... $fn$; block of a goose
// section, verbatim — comments outside it are excluded, so the two migrations' prose
// headers cannot make identical bodies compare unequal.
func extractionResolverFnBody(t *testing.T, name, section, sql string) string {
	t.Helper()
	const head = "CREATE OR REPLACE FUNCTION audit_log_entity_for"
	const tail = "\n$fn$;"
	start := strings.Index(sql, head)
	if start < 0 {
		t.Fatalf("%s's %s section holds no %q", name, section, head)
	}
	end := strings.Index(sql[start:], tail)
	if end < 0 {
		t.Fatalf("%s's %s section: the $fn$ body is never closed", name, section)
	}
	return sql[start : start+end+len(tail)]
}

func extractionContains(list []string, want string) bool {
	for _, v := range list {
		if v == want {
			return true
		}
	}
	return false
}

type extractionRow struct {
	event     string
	entityID  *string
	invoiceID *string
}

// extractionAuditRows maps the payload's `arm` label -> row for one tenant, read as the app
// role under that tenant's context. The arms share an event name, so the label is what
// tells them apart; triggerEntityIDs keys by event and cannot.
func extractionAuditRows(t *testing.T, f *fixture, tenant string) map[string]extractionRow {
	t.Helper()
	ctx := context.Background()
	out := map[string]extractionRow{}
	if err := db.WithinTenantTx(ctx, f.app, tenant, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx,
			`SELECT payload->>'arm', event, entity_id, invoice_id FROM audit_log WHERE tenant_id = $1`, tenant)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var arm *string
			var r extractionRow
			if err := rows.Scan(&arm, &r.event, &r.entityID, &r.invoiceID); err != nil {
				return err
			}
			if arm == nil {
				continue // an unlabelled row from another case in this suite
			}
			out[*arm] = r
		}
		return rows.Err()
	}); err != nil {
		t.Fatalf("read back audit rows for tenant %s: %v", tenant, err)
	}
	if len(out) == 0 {
		t.Fatalf("no labelled audit rows visible for tenant %s — the fixture never landed, so "+
			"every assertion below would be vacuous", tenant)
	}
	return out
}

func extractionAssertEntity(t *testing.T, rows map[string]extractionRow, arm, want string) {
	t.Helper()
	r, ok := rows[arm]
	if !ok {
		t.Errorf("arm %s: no audit row read back", arm)
		return
	}
	if r.entityID == nil {
		t.Errorf("arm %s (%s): entity_id IS NULL, want %s", arm, r.event, want)
		return
	}
	if *r.entityID != want {
		t.Errorf("arm %s (%s): entity_id = %s, want %s", arm, r.event, *r.entityID, want)
	}
}

func extractionAssertEntityNull(t *testing.T, rows map[string]extractionRow, arm string) {
	t.Helper()
	r, ok := rows[arm]
	if !ok {
		t.Errorf("arm %s: no audit row read back", arm)
		return
	}
	if r.entityID != nil {
		t.Errorf("arm %s (%s): entity_id = %s, want NULL", arm, r.event, *r.entityID)
	}
}

func extractionShow(v *string) string {
	if v == nil {
		return "NULL"
	}
	return *v
}

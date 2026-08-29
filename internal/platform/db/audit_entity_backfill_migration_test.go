// Test-first (RED) suite for the audit_log entity_id backfill: the shared resolver, the
// per-tenant bracket, the size guard, and where the write-time trigger sits in the body.
//
// A backfill DB case executes that migration's own Down body then Up body — read out of
// migrations.FS — on one invoice_migrator connection with NO app.current_tenant pre-set,
// inside a single rolled-back transaction. TestRLS_AuditResolverReplacementIsReversible is
// the exception: the REPLACEMENT migration's Up then Down, under a tenant it sets itself.
// Bodies go out as argument-less Execs, which pgx sends over the simple protocol, so
// multi-statement bodies and $fn$ quoting survive and goose's directives stay ordinary
// comments. demoRepairStatements cannot be reused: it fails loudly on a function body,
// and the backfill migration carries three.
//
// The Down drops entity_id, so a fixture row the write-time trigger already attributed
// starts the Up as NULL and the backfill is the only thing that can fill it. Running as
// the migrator (NOBYPASSRLS, FORCE RLS) with no tenant pre-set is the point: a superuser
// body would pass every cross-tenant case vacuously, and a body missing its own
// set_config calls matches zero rows while goose still records MIGRATE OK.
//
// audit_log rows can never be deleted (42501 for the app, 23001 for the owner), so
// fixtures mint a fresh tenant uuid per test and leave their rows behind.
package db_test

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"reflect"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/SimonOsipov/invoice-os/migrations"
)

const auditEntityMigrationGlob = "*_audit_log_entity_id_and_read_indexes.sql"

// The 36 audit events the entity trigger classifies, split by attribution rule. Rules
// A-C are the 21 attributable ones; rule D is workspace-level and always resolves NULL.
// The vocabulary is wider than this set: extraction.* is in no rule and resolves NULL.
var (
	auditRuleAEvents = []string{ // bare `id`, looked up through invoices
		"invoice.created",
		"invoice.updated",
		"invoice.transitioned",
		"invoice.validated",
		"invoice.kept_as_is",
		"invoice.unkept_as_is",
		"invoice.resolved_outside",
		"invoice.unresolved_outside",
		"invoice.approval_armed",
		"invoice.approval_cancelled",
	}
	auditRuleBEvents = []string{ // `invoice_id` spelling, same lookup
		"invoice.approval_approved",
		"invoice.approval_rejected",
		"submission.accepted",
		"submission.rejected",
		"submission.failed",
		"reconciliation.drift_detected",
		"reconciliation.auto_fixed",
	}
	auditRuleCEvents = []string{ // the entity id is already in the payload
		"portfolio.entity.created",
		"portfolio.entity.updated",
		"portfolio.entity.onboarded",
		"portfolio.entity.offboarded",
	}
)

// auditRuleDPayloads pairs each workspace-level event with the payload shape its call
// site actually writes. Six carry a NON-uuid text `key`, which raises 22P02 the moment
// anything casts it.
//
// The three document.* events split the two ways a key-scoped resolver goes wrong:
// document.created gets a real INVOICE id (a resolver dispatching on `id` being present
// would join invoices and attribute it), the other two get a real documents id (a resolver
// returning the payload id directly would write a document uuid into entity_id).
func auditRuleDPayloads(documentID, invoiceID string) map[string]string {
	policyID, userID := uuid.NewString(), uuid.NewString()
	return map[string]string{
		"approval_policy.created":   auditPayloadJSON("policy_id", policyID),
		"approval_policy.updated":   auditPayloadJSON("policy_id", policyID),
		"approval_policy.published": auditPayloadJSON("policy_id", policyID),
		"approval_policy.deleted":   auditPayloadJSON("policy_id", policyID),
		"workflow_role.created":     auditPayloadJSON("key", "approver"),
		"workflow_role.updated":     auditPayloadJSON("key", "approver"),
		"workflow_role.deleted":     auditPayloadJSON("key", "approver"),
		"workflow_role.staffed":     auditPayloadJSON("key", "approver"),
		"document.created":          auditPayloadJSON("id", invoiceID),
		"document.read":             auditPayloadJSON("id", documentID),
		"document.reused":           auditPayloadJSON("id", documentID),
		"membership.suspended":      auditPayloadJSON("user_id", userID),
		"membership.reactivated":    auditPayloadJSON("user_id", userID),
		"validation.rule.enabled":   auditPayloadJSON("key", "buyer-tin-present"),
		"validation.rule.disabled":  auditPayloadJSON("key", "buyer-tin-present"),
	}
}

// --- migration body ---------------------------------------------------------------

func auditEntityMigrationName(t *testing.T) string {
	t.Helper()
	matches, err := fs.Glob(migrations.FS, auditEntityMigrationGlob)
	if err != nil {
		t.Fatalf("glob %s in migrations.FS: %v", auditEntityMigrationGlob, err)
	}
	if len(matches) != 1 {
		t.Fatalf("migrations.FS holds %d files matching %s (%v), want exactly 1",
			len(matches), auditEntityMigrationGlob, matches)
	}
	return matches[0]
}

// auditEntityUpOf returns a file's goose Up section, ok=false when the markers are absent
// or out of order. Callers that require the section fatal themselves.
func auditEntityUpOf(raw string) (string, bool) {
	up := strings.Index(raw, gooseUp)
	down := strings.Index(raw, gooseDown)
	if up < 0 || down < 0 || down < up {
		return "", false
	}
	return raw[up+len(gooseUp) : down], true
}

// auditEntitySectionOf returns one goose section of a named migration in migrations.FS.
func auditEntitySectionOf(t *testing.T, name, section string) string {
	t.Helper()
	b, err := fs.ReadFile(migrations.FS, name)
	if err != nil {
		t.Fatalf("read %s from migrations.FS: %v", name, err)
	}
	raw := string(b)
	up, ok := auditEntityUpOf(raw)
	if !ok {
		t.Fatalf("%s: want both %q and %q, in that order", name, gooseUp, gooseDown)
	}
	if section == "Up" {
		return up
	}
	return raw[strings.Index(raw, gooseDown)+len(gooseDown):]
}

// Every migration, so the resolver's CURRENT definition is found by CONTENT, not by a
// pinned filename. auditEntityMigrationName stays pinned; it answers a different question.
const auditResolverMigrationGlob = "*.sql"

// The parameter list terminates the identifier -- CREATE FUNCTION always has one -- so a
// renamed audit_log_entity_forZZ no longer counts as a definer and the zero-definers fatal
// below can fire. Fenced by TestRLS_AuditResolverDefinerIsTheLatestMigration.
var auditResolverDefRE = regexp.MustCompile(
	`(?is)CREATE\s+(OR\s+REPLACE\s+)?FUNCTION\s+([a-z_][a-z0-9_$]*\s*\.\s*)?audit_log_entity_for\s*\(`)

// auditResolverDefinerName returns the LAST migration whose Up section defines
// audit_log_entity_for. Filenames lead with the goose timestamp, so lexical order is apply
// order and the last definer is the definition live in the database. More than one definer
// is expected after this story; zero is not.
func auditResolverDefinerName(t *testing.T) string {
	t.Helper()
	matches, err := fs.Glob(migrations.FS, auditResolverMigrationGlob)
	if err != nil {
		t.Fatalf("glob %s in migrations.FS: %v", auditResolverMigrationGlob, err)
	}
	sort.Strings(matches)
	var definers []string
	for _, name := range matches {
		b, err := fs.ReadFile(migrations.FS, name)
		if err != nil {
			t.Fatalf("read %s from migrations.FS: %v", name, err)
		}
		up, ok := auditEntityUpOf(string(b))
		if !ok {
			continue // no goose markers: not a migration body we can read
		}
		if auditResolverDefRE.MatchString(auditEntityStripComments(up)) {
			definers = append(definers, name)
		}
	}
	if len(definers) == 0 {
		t.Fatalf("no migration in migrations.FS defines audit_log_entity_for")
	}
	return definers[len(definers)-1]
}

// auditEntitySection returns one goose section of the story migration, verbatim.
func auditEntitySection(t *testing.T, section string) string {
	t.Helper()
	name := auditEntityMigrationName(t)
	b, err := fs.ReadFile(migrations.FS, name)
	if err != nil {
		t.Fatalf("read %s from migrations.FS: %v", name, err)
	}
	raw := string(b)

	up := strings.Index(raw, gooseUp)
	down := strings.Index(raw, gooseDown)
	if up < 0 || down < 0 || down < up {
		t.Fatalf("%s: want both %q and %q, in that order (up=%d down=%d)", name, gooseUp, gooseDown, up, down)
	}
	switch section {
	case "Up":
		body, _ := auditEntityUpOf(raw)
		return body
	case "Down":
		return raw[down+len(gooseDown):]
	default:
		t.Fatalf("unknown goose section %q", section)
		return ""
	}
}

var auditDollarTagRE = regexp.MustCompile(`\$[A-Za-z_][A-Za-z0-9_]*\$|\$\$`)

// auditEntityStripComments drops `--` comments that sit outside a dollar-quoted body or a
// single-quoted literal, so prose in the header can neither satisfy nor trip a text
// assertion.
func auditEntityStripComments(sql string) string {
	var b strings.Builder
	for i := 0; i < len(sql); {
		switch {
		case sql[i] == '-' && i+1 < len(sql) && sql[i+1] == '-':
			for i < len(sql) && sql[i] != '\n' {
				i++
			}
		case sql[i] == '\'':
			b.WriteByte(sql[i])
			i++
			for i < len(sql) {
				if sql[i] == '\'' {
					if i+1 < len(sql) && sql[i+1] == '\'' {
						b.WriteString("''")
						i += 2
						continue
					}
					b.WriteByte(sql[i])
					i++
					break
				}
				b.WriteByte(sql[i])
				i++
			}
		case sql[i] == '$':
			loc := auditDollarTagRE.FindStringIndex(sql[i:])
			if loc == nil || loc[0] != 0 {
				b.WriteByte(sql[i])
				i++
				continue
			}
			tag := sql[i : i+loc[1]]
			end := strings.Index(sql[i+len(tag):], tag)
			if end < 0 {
				b.WriteString(sql[i:])
				i = len(sql)
				continue
			}
			stop := i + len(tag) + end + len(tag)
			b.WriteString(sql[i:stop])
			i = stop
		default:
			b.WriteByte(sql[i])
			i++
		}
	}
	return b.String()
}

// auditEntityFunctionSpan returns [start, end) of the named function's dollar-quoted
// body, delimiters included.
func auditEntityFunctionSpan(t *testing.T, body, fn string) (int, int) {
	t.Helper()
	i := strings.Index(body, "FUNCTION "+fn)
	if i < 0 {
		t.Fatalf("the Up body defines no FUNCTION %s", fn)
	}
	loc := auditDollarTagRE.FindStringIndex(body[i:])
	if loc == nil {
		t.Fatalf("FUNCTION %s has no dollar-quoted body", fn)
	}
	start := i + loc[0]
	tag := body[start : i+loc[1]]
	end := strings.Index(body[start+len(tag):], tag)
	if end < 0 {
		t.Fatalf("FUNCTION %s: dollar quote %s is never closed", fn, tag)
	}
	return start, start + len(tag) + end + len(tag)
}

// --- executing the body -----------------------------------------------------------

// auditEntityExecBodies runs the Down body then upBody on tx. Argument-less Execs go out
// over pgx's simple protocol, which accepts a whole multi-statement body.
func auditEntityExecBodies(t *testing.T, ctx context.Context, tx pgx.Tx, upBody string) {
	t.Helper()
	if _, err := tx.Exec(ctx, auditEntitySection(t, "Down")); err != nil {
		t.Fatalf("Down body failed (is the migration applied? run `make migrate-up`): %v", err)
	}
	if _, err := tx.Exec(ctx, upBody); err != nil {
		t.Fatalf("Up body failed: %v", err)
	}
}

// auditEntityApplyUp opens a migrator transaction with no tenant context and replays
// Down-then-Up inside it. The transaction is rolled back on cleanup.
func auditEntityApplyUp(t *testing.T, ctx context.Context) pgx.Tx {
	t.Helper()
	tx := migratorTx(t, ctx)
	auditEntityExecBodies(t, ctx, tx, auditEntitySection(t, "Up"))
	return tx
}

// auditEntityCountAll counts rows across every tenant by lifting FORCE RLS for the owner
// inside the caller's transaction, then restoring it. Rolled back with the transaction.
func auditEntityCountAll(t *testing.T, ctx context.Context, tx pgx.Tx, where string) int {
	t.Helper()
	if _, err := tx.Exec(ctx, `ALTER TABLE audit_log NO FORCE ROW LEVEL SECURITY`); err != nil {
		t.Fatalf("lift FORCE RLS for the all-tenant count: %v", err)
	}
	var n int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM audit_log WHERE `+where).Scan(&n); err != nil {
		t.Fatalf("count audit_log WHERE %s: %v", where, err)
	}
	if _, err := tx.Exec(ctx, `ALTER TABLE audit_log FORCE ROW LEVEL SECURITY`); err != nil {
		t.Fatalf("restore FORCE RLS after the all-tenant count: %v", err)
	}
	return n
}

// auditEntityIDsByEvent maps event -> entity_id for one tenant's rows, read inside the
// migration's own transaction under that tenant's RLS context.
func auditEntityIDsByEvent(t *testing.T, ctx context.Context, tx pgx.Tx, tenant string) map[string]*string {
	t.Helper()
	if _, err := tx.Exec(ctx, `SELECT set_config('app.current_tenant', $1, true)`, tenant); err != nil {
		t.Fatalf("set tenant context for read-back: %v", err)
	}
	rows, err := tx.Query(ctx, `SELECT event, entity_id FROM audit_log WHERE tenant_id = $1`, tenant)
	if err != nil {
		t.Fatalf("read back audit rows for %s: %v", tenant, err)
	}
	defer rows.Close()

	out := map[string]*string{}
	for rows.Next() {
		var event string
		var entity *string
		if err := rows.Scan(&event, &entity); err != nil {
			t.Fatalf("scan audit row: %v", err)
		}
		out[event] = entity
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

func assertAuditEntity(t *testing.T, got map[string]*string, event, want string) {
	t.Helper()
	v, ok := got[event]
	if !ok {
		t.Errorf("event %s: no audit row read back", event)
		return
	}
	if v == nil {
		t.Errorf("event %s: entity_id IS NULL, want %s", event, want)
		return
	}
	if *v != want {
		t.Errorf("event %s: entity_id = %s, want %s", event, *v, want)
	}
}

func assertAuditEntityNull(t *testing.T, got map[string]*string, event string) {
	t.Helper()
	v, ok := got[event]
	if !ok {
		t.Errorf("event %s: no audit row read back", event)
		return
	}
	if v != nil {
		t.Errorf("event %s: entity_id = %s, want NULL", event, *v)
	}
}

// --- fixtures ---------------------------------------------------------------------

type auditEntityFixture struct {
	tenant   string
	entity   string
	invoice  string
	document string
}

// seedAuditEntityFixture commits a throwaway tenant with one entity, one invoice on it
// and one document, as the superuser (BYPASSRLS, so no tenant context is needed). Ids are
// generated here, never read from db/seed.dev.sql — the seeded rows default theirs to
// gen_random_uuid() and differ per database.
func seedAuditEntityFixture(t *testing.T) auditEntityFixture {
	t.Helper()
	ctx := context.Background()
	f := auditEntityFixture{
		tenant:   uuid.NewString(),
		entity:   uuid.NewString(),
		invoice:  uuid.NewString(),
		document: uuid.NewString(),
	}
	exec := func(sql string, args ...any) {
		if _, err := h.super.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("seed fixture (%s): %v", sql, err)
		}
	}
	exec(`INSERT INTO tenants (id, name) VALUES ($1, $2)`, f.tenant, "audit-entity-"+f.tenant[:8])
	exec(`INSERT INTO business_entities (id, tenant_id, name) VALUES ($1, $2, $3)`,
		f.entity, f.tenant, "entity-"+f.entity[:8])
	exec(`INSERT INTO documents (id, tenant_id, storage_key, content_hash, size_bytes)
	      VALUES ($1, $2, $3, $4, 1)`,
		f.document, f.tenant, "audit-entity/"+f.document, strings.Repeat("a", 64))
	exec(`INSERT INTO invoices (id, tenant_id, entity_id, invoice_number) VALUES ($1, $2, $3, $4)`,
		f.invoice, f.tenant, f.entity, "INV-"+f.invoice[:8])

	t.Cleanup(func() {
		// Reverse FK order; audit_log rows have no FK here and cannot be deleted at all.
		for _, sql := range []string{
			`DELETE FROM invoices WHERE tenant_id = $1`,
			`DELETE FROM documents WHERE tenant_id = $1`,
			`DELETE FROM business_entities WHERE tenant_id = $1`,
			`DELETE FROM tenants WHERE id = $1`,
		} {
			_, _ = h.super.Exec(context.Background(), sql, f.tenant)
		}
	})
	return f
}

// seedAuditEntityRow commits one audit_log row as the superuser. Whatever the write-time
// trigger puts in entity_id is irrelevant: the Down body drops the column first.
func seedAuditEntityRow(t *testing.T, tenant, event, payload string) {
	t.Helper()
	if _, err := h.super.Exec(context.Background(),
		`INSERT INTO audit_log (tenant_id, actor, event, payload) VALUES ($1, 'backfill-fixture', $2, $3::jsonb)`,
		tenant, event, payload); err != nil {
		t.Fatalf("seed audit row %s: %v", event, err)
	}
}

func auditPayloadJSON(key, value string) string {
	return fmt.Sprintf(`{%q: %q}`, key, value)
}

func requireEventList(t *testing.T, name string, events []string, want int) {
	t.Helper()
	if len(events) != want {
		t.Fatalf("%s holds %d event names, want %d", name, len(events), want)
	}
}

// --- AC-1..AC-8: the backfill's attribution ---------------------------------------

// AC-1: the 10 bare-`id` invoice events resolve through invoices.entity_id.
func TestRLS_AuditBackfillResolvesBareIDInvoiceEvents(t *testing.T) {
	requireHarness(t)
	ctx := context.Background()
	requireEventList(t, "auditRuleAEvents", auditRuleAEvents, 10)

	f := seedAuditEntityFixture(t)
	for _, event := range auditRuleAEvents {
		seedAuditEntityRow(t, f.tenant, event, auditPayloadJSON("id", f.invoice))
	}

	tx := auditEntityApplyUp(t, ctx)
	got := auditEntityIDsByEvent(t, ctx, tx, f.tenant)
	for _, event := range auditRuleAEvents {
		assertAuditEntity(t, got, event, f.entity)
	}
}

// AC-2: the `invoice_id`-spelled events the one-time backfill can attribute resolve the
// same way. Both reconciliation events are asserted by name — the basic story lists only
// four of them.
func TestRLS_AuditBackfillResolvesInvoiceIDSpelledEvents(t *testing.T) {
	requireHarness(t)
	ctx := context.Background()
	requireEventList(t, "auditRuleBEvents", auditRuleBEvents, 7)
	// This test replays the one-time backfill, which ran before submission.failed joined
	// rule B and so can never attribute a stored row of it. The write-time trigger does;
	// TestAudit_InsertTriggerResolvesSubmissionFailed is where that is asserted.
	backfilled := slices.DeleteFunc(slices.Clone(auditRuleBEvents),
		func(e string) bool { return e == "submission.failed" })
	requireEventList(t, "auditRuleBEvents minus submission.failed", backfilled, 6)

	f := seedAuditEntityFixture(t)
	for _, event := range backfilled {
		seedAuditEntityRow(t, f.tenant, event, auditPayloadJSON("invoice_id", f.invoice))
	}

	tx := auditEntityApplyUp(t, ctx)
	got := auditEntityIDsByEvent(t, ctx, tx, f.tenant)
	for _, event := range backfilled {
		assertAuditEntity(t, got, event, f.entity)
	}
	assertAuditEntity(t, got, "reconciliation.drift_detected", f.entity)
	assertAuditEntity(t, got, "reconciliation.auto_fixed", f.entity)
}

// AC-3: the 4 portfolio.entity.* events carry the entity id themselves, so they resolve
// with no join to invoices.
func TestRLS_AuditBackfillResolvesPortfolioEventsFromTheirOwnPayload(t *testing.T) {
	requireHarness(t)
	ctx := context.Background()
	requireEventList(t, "auditRuleCEvents", auditRuleCEvents, 4)

	f := seedAuditEntityFixture(t)
	var stray int
	if err := h.super.QueryRow(ctx, `SELECT count(*) FROM invoices WHERE id = $1`, f.entity).Scan(&stray); err != nil {
		t.Fatalf("check for an invoice sharing the entity id: %v", err)
	}
	if stray != 0 {
		t.Fatalf("invoices with id = the entity id = %d, want 0 — an invoices lookup could "+
			"otherwise produce the same answer as the direct payload read", stray)
	}
	for _, event := range auditRuleCEvents {
		seedAuditEntityRow(t, f.tenant, event, auditPayloadJSON("id", f.entity))
	}

	tx := auditEntityApplyUp(t, ctx)
	got := auditEntityIDsByEvent(t, ctx, tx, f.tenant)
	for _, event := range auditRuleCEvents {
		assertAuditEntity(t, got, event, f.entity)
	}
}

// AC-4: the 15 workspace-level events stay NULL, including document.created (a bare `id`
// that is a documents id, not an invoice) and the six whose payload carries a non-uuid
// text key. The rule-A row in the same test is the positive control: without it, a
// resolver that always returned NULL would pass.
func TestRLS_AuditBackfillLeavesWorkspaceEventsNull(t *testing.T) {
	requireHarness(t)
	ctx := context.Background()

	f := seedAuditEntityFixture(t)
	ruleD := auditRuleDPayloads(f.document, f.invoice)
	if len(ruleD) != 15 {
		t.Fatalf("rule-D payload map holds %d events, want 15", len(ruleD))
	}
	for event, payload := range ruleD {
		seedAuditEntityRow(t, f.tenant, event, payload)
	}
	seedAuditEntityRow(t, f.tenant, "invoice.created", auditPayloadJSON("id", f.invoice))

	// A body that cast the non-uuid `key` would raise 22P02 here, inside auditEntityApplyUp.
	tx := auditEntityApplyUp(t, ctx)
	got := auditEntityIDsByEvent(t, ctx, tx, f.tenant)
	for event := range ruleD {
		assertAuditEntityNull(t, got, event)
	}
	assertAuditEntity(t, got, "invoice.created", f.entity)
}

// AC-1: the historical rows this one-shot sweeps carry whatever spelling their call site
// wrote, and uuid_in accepts more than canonical form. Same acceptance line as the
// write-time trigger, proved on the path that cannot be re-run.
func TestRLS_AuditBackfillResolvesEverySpellingUUIDInAccepts(t *testing.T) {
	requireHarness(t)
	ctx := context.Background()

	f := seedAuditEntityFixture(t)
	bare := strings.ReplaceAll(f.invoice, "-", "")
	if len(bare) != 32 || bare == f.invoice {
		t.Fatalf("hyphenless id %q is %d chars, want a 32-char restyling of %s", bare, len(bare), f.invoice)
	}

	resolves := map[string]string{
		"invoice.created":          f.invoice,             // canonical, the positive control
		"invoice.kept_as_is":       bare,                  // hyphenless
		"invoice.unkept_as_is":     "{" + f.invoice + "}", // brace-wrapped
		"invoice.resolved_outside": "{" + bare + "}",
		"invoice.validated":        strings.ToUpper(f.invoice),
	}
	refuses := map[string]string{
		"invoice.transitioned":       bare[:3] + "-" + bare[3:], // hyphen off the 4-hex boundary
		"invoice.unresolved_outside": "{" + f.invoice,           // unclosed brace
		"invoice.updated":            "not-a-uuid",
	}
	for _, batch := range []map[string]string{resolves, refuses} {
		for event, id := range batch {
			seedAuditEntityRow(t, f.tenant, event, auditPayloadJSON("id", id))
		}
	}

	// A body that cast a refused spelling would raise 22P02 here, inside auditEntityApplyUp.
	tx := auditEntityApplyUp(t, ctx)
	got := auditEntityIDsByEvent(t, ctx, tx, f.tenant)
	for event := range resolves {
		assertAuditEntity(t, got, event, f.entity)
	}
	for event := range refuses {
		assertAuditEntityNull(t, got, event)
	}
}

// AC-5: the backfill mutates rows although nothing sets app.current_tenant before it. A
// body without its own per-tenant set_config reports UPDATE 0 and succeeds silently.
func TestRLS_AuditBackfillMutatesRowsWithoutPreSetTenantContext(t *testing.T) {
	requireHarness(t)
	ctx := context.Background()

	f := seedAuditEntityFixture(t)
	for _, event := range auditRuleAEvents {
		seedAuditEntityRow(t, f.tenant, event, auditPayloadJSON("id", f.invoice))
	}

	// migratorTx deliberately sets no tenant GUC.
	tx := migratorTx(t, ctx)
	auditEntityExecBodies(t, ctx, tx, auditEntitySection(t, "Up"))

	if n := auditEntityCountAll(t, ctx, tx, "entity_id IS NOT NULL"); n == 0 {
		t.Errorf("rows with a non-NULL entity_id after the body = 0, want > 0 — the backfill " +
			"matched nothing, which is what a missing per-tenant set_config looks like")
	}
	got := auditEntityIDsByEvent(t, ctx, tx, f.tenant)
	assertAuditEntity(t, got, "invoice.created", f.entity)
}

// AC-6: every tenant with audit rows is covered, not only the first one the loop reaches.
func TestRLS_AuditBackfillCoversEveryTenant(t *testing.T) {
	requireHarness(t)
	ctx := context.Background()

	first := seedAuditEntityFixture(t)
	second := seedAuditEntityFixture(t)
	seedAuditEntityRow(t, first.tenant, "invoice.created", auditPayloadJSON("id", first.invoice))
	seedAuditEntityRow(t, second.tenant, "invoice.created", auditPayloadJSON("id", second.invoice))

	tx := auditEntityApplyUp(t, ctx)
	assertAuditEntity(t, auditEntityIDsByEvent(t, ctx, tx, first.tenant), "invoice.created", first.entity)
	assertAuditEntity(t, auditEntityIDsByEvent(t, ctx, tx, second.tenant), "invoice.created", second.entity)
}

// AC-6: a tenant whose audit rows are entirely workspace-level matches the backfill's key
// prefilter zero times. The loop must carry on past it, so the two tenants that DO resolve
// are the positive control on the same predicate. AC-6's own test cannot see this: both of
// its tenants resolve, so a body that aborted on an empty UPDATE would still pass it.
func TestRLS_AuditBackfillSurvivesATenantWithNothingResolvable(t *testing.T) {
	requireHarness(t)
	ctx := context.Background()

	first := seedAuditEntityFixture(t)
	barren := seedAuditEntityFixture(t)
	last := seedAuditEntityFixture(t)

	seedAuditEntityRow(t, first.tenant, "invoice.created", auditPayloadJSON("id", first.invoice))
	seedAuditEntityRow(t, last.tenant, "invoice.created", auditPayloadJSON("id", last.invoice))
	// None of these carries an `id` or `invoice_id` key, so the prefilter matches no row
	// of this tenant at all.
	barrenEvents := map[string]string{
		"workflow_role.created":   auditPayloadJSON("key", "approver"),
		"membership.suspended":    auditPayloadJSON("user_id", uuid.NewString()),
		"approval_policy.created": auditPayloadJSON("policy_id", uuid.NewString()),
	}
	for event, payload := range barrenEvents {
		seedAuditEntityRow(t, barren.tenant, event, payload)
	}

	tx := auditEntityApplyUp(t, ctx)

	assertAuditEntity(t, auditEntityIDsByEvent(t, ctx, tx, first.tenant), "invoice.created", first.entity)
	assertAuditEntity(t, auditEntityIDsByEvent(t, ctx, tx, last.tenant), "invoice.created", last.entity)

	got := auditEntityIDsByEvent(t, ctx, tx, barren.tenant)
	if len(got) != len(barrenEvents) {
		t.Fatalf("read back %d rows for the barren tenant, want %d — the fixture never landed", len(got), len(barrenEvents))
	}
	for event := range barrenEvents {
		assertAuditEntityNull(t, got, event)
	}
}

// AC-7: the backfill only updates. Both counts are taken in the same transaction and the
// same role, and compared to each other — the table carries permanent test debris, so a
// constant would be wrong. The paired mutation assertion is what stops a body that
// touches nothing at all from satisfying the equality.
func TestRLS_AuditBackfillPreservesRowCount(t *testing.T) {
	requireHarness(t)
	ctx := context.Background()

	f := seedAuditEntityFixture(t)
	seedAuditEntityRow(t, f.tenant, "invoice.created", auditPayloadJSON("id", f.invoice))

	tx := migratorTx(t, ctx)
	before := auditEntityCountAll(t, ctx, tx, "true")
	if before == 0 {
		t.Fatalf("audit_log rows before the body = 0 — an empty table makes the equality below vacuous")
	}
	auditEntityExecBodies(t, ctx, tx, auditEntitySection(t, "Up"))
	after := auditEntityCountAll(t, ctx, tx, "true")

	if after != before {
		t.Errorf("audit_log rows = %d after the body, %d before — the backfill must only UPDATE", after, before)
	}
	if n := auditEntityCountAll(t, ctx, tx, "entity_id IS NOT NULL"); n == 0 {
		t.Errorf("rows with a non-NULL entity_id after the body = 0, want > 0 — a body that " +
			"touches nothing preserves the row count trivially")
	}
	assertAuditEntity(t, auditEntityIDsByEvent(t, ctx, tx, f.tenant), "invoice.created", f.entity)
}

// AC-8: RLS, not a WHERE clause, is the cross-tenant guard. Tenant A's row naming tenant
// B's invoice stays NULL; A's own row resolving to exactly its entity in the same test is
// the positive control.
func TestRLS_AuditBackfillNeverCrossesTenants(t *testing.T) {
	requireHarness(t)
	ctx := context.Background()

	a := seedAuditEntityFixture(t)
	b := seedAuditEntityFixture(t)
	seedAuditEntityRow(t, a.tenant, "invoice.updated", auditPayloadJSON("id", b.invoice))
	seedAuditEntityRow(t, a.tenant, "invoice.created", auditPayloadJSON("id", a.invoice))

	tx := auditEntityApplyUp(t, ctx)
	got := auditEntityIDsByEvent(t, ctx, tx, a.tenant)
	assertAuditEntityNull(t, got, "invoice.updated")
	assertAuditEntity(t, got, "invoice.created", a.entity)
}

// --- AC-9..AC-12, AC-16: the body's own text ---------------------------------------

// AC-9: the migration bounds how long it WAITS for its lock and sets no per-statement
// timeout. The positive needle comes first so the absence assertion cannot pass on an
// empty body, and comments are stripped so the header's prose cannot decide either.
func TestRLS_AuditBackfillDeclaresLockTimeoutAndNoStatementTimeout(t *testing.T) {
	body := auditEntityStripComments(auditEntitySection(t, "Up"))

	if !strings.Contains(body, "SET LOCAL lock_timeout") {
		t.Fatalf("the comment-stripped Up body does not contain %q (%d bytes) — the absence "+
			"assertion below would pass vacuously", "SET LOCAL lock_timeout", len(body))
	}
	if strings.Contains(body, "statement_timeout") {
		t.Errorf("the Up body sets statement_timeout; the backfill must bound only the lock wait")
	}
}

// AC-10: above the threshold the guard aborts the whole migration, and the abort leaves
// the table exactly as it found it — no new trigger, the append-only trigger enabled,
// FORCE RLS back on.
func TestRLS_AuditBackfillSizeGuardAbortsAndRollsBack(t *testing.T) {
	requireHarness(t)
	ctx := context.Background()

	f := seedAuditEntityFixture(t)
	seedAuditEntityRow(t, f.tenant, "invoice.created", auditPayloadJSON("id", f.invoice))

	before := auditLogCatalogState(t, ctx, h.super)
	tx := migratorTx(t, ctx)
	if _, err := tx.Exec(ctx, auditEntitySection(t, "Down")); err != nil {
		t.Fatalf("Down body failed (is the migration applied? run `make migrate-up`): %v", err)
	}
	_, err := tx.Exec(ctx, auditEntityLoweredGuard(t))
	assertAuditGuardTripped(t, err)
	_ = tx.Rollback(ctx)

	after := auditLogCatalogState(t, ctx, h.super)
	if !reflect.DeepEqual(before, after) {
		t.Errorf("audit_log catalog state after the aborted migration = %+v, want the pre-migration %+v", after, before)
	}
	if state := after.triggers["audit_log_no_update_delete"]; state != "O" {
		t.Errorf("audit_log_no_update_delete tgenabled = %q after the abort, want %q — the "+
			"suspend/restore bracket leaked", state, "O")
	}
	if !after.forceRLS {
		t.Errorf("audit_log relforcerowsecurity = false after the abort, want true")
	}
}

// AC-11: tripping the guard fails the whole fleet deploy, so the message must tell the
// operator both how big the table is and what to do about it.
func TestRLS_AuditBackfillGuardMessageNamesCountAndRemedy(t *testing.T) {
	requireHarness(t)
	ctx := context.Background()

	f := seedAuditEntityFixture(t)
	seedAuditEntityRow(t, f.tenant, "invoice.created", auditPayloadJSON("id", f.invoice))

	var rows int
	if err := h.super.QueryRow(ctx, `SELECT count(*) FROM audit_log`).Scan(&rows); err != nil {
		t.Fatalf("count audit_log as superuser: %v", err)
	}

	tx := migratorTx(t, ctx)
	if _, err := tx.Exec(ctx, auditEntitySection(t, "Down")); err != nil {
		t.Fatalf("Down body failed (is the migration applied? run `make migrate-up`): %v", err)
	}
	_, err := tx.Exec(ctx, auditEntityLoweredGuard(t))
	assertAuditGuardTripped(t, err)

	var pgErr *pgconn.PgError
	_ = errors.As(err, &pgErr)
	msg := strings.ToLower(pgErr.Message)
	if !strings.Contains(msg, strconv.Itoa(rows)) {
		t.Errorf("guard message %q does not name the row count %d", pgErr.Message, rows)
	}
	for _, want := range []string{"backfill", "out of band"} {
		if !strings.Contains(msg, want) {
			t.Errorf("guard message %q does not contain %q — it must name the remedy", pgErr.Message, want)
		}
	}
}

// AC-12: inside the migration that currently defines the resolver, every attributable event
// name occurs exactly once and inside the resolver's own dollar-quoted body. Superseded
// copies — earlier migrations, and this definer's own Down — are out of the scan by design:
// only the last definer is live. The wiring half is
// TestRLS_AuditResolverWiringStillReadsTheBackfillMigration.
func TestRLS_AuditResolverIsDefinedOnceAndCalledByBoth(t *testing.T) {
	body := auditEntityStripComments(auditEntitySectionOf(t, auditResolverDefinerName(t), "Up"))

	defs := auditResolverDefRE.FindAllString(body, -1)
	if len(defs) != 1 {
		t.Fatalf("the Up body defines audit_log_entity_for %d times, want exactly 1", len(defs))
	}

	start, end := auditEntityFunctionSpan(t, body, "audit_log_entity_for")
	attributable := append(append(append([]string{}, auditRuleAEvents...), auditRuleBEvents...), auditRuleCEvents...)
	if len(attributable) != 21 {
		t.Fatalf("attributable event names = %d, want 21", len(attributable))
	}
	for _, event := range attributable {
		found := 0
		for i := strings.Index(body, event); i >= 0; {
			found++
			if i < start || i >= end {
				t.Errorf("event %s appears at offset %d, outside the resolver body [%d,%d) — the "+
					"attribution rules must exist exactly once", event, i, start, end)
			}
			next := strings.Index(body[i+len(event):], event)
			if next < 0 {
				break
			}
			i = i + len(event) + next
		}
		if found == 0 {
			t.Errorf("event %s appears nowhere in the Up body — the resolver does not attribute it", event)
		}
	}

}

// AC-12: the two callers stay wired to the shared resolver. Both live in the backfill
// migration, which auditEntitySection reads through its pinned glob — the "exactly 1"
// fatal in auditEntityMigrationName is what this test keeps exercising.
func TestRLS_AuditResolverWiringStillReadsTheBackfillMigration(t *testing.T) {
	body := auditEntityStripComments(auditEntitySection(t, "Up"))

	setStart, setEnd := auditEntityFunctionSpan(t, body, "audit_log_set_entity")
	if !strings.Contains(body[setStart:setEnd], "audit_log_entity_for(") {
		t.Errorf("audit_log_set_entity does not call audit_log_entity_for — the trigger has its own rules")
	}

	upd := regexp.MustCompile(`(?is)UPDATE\s+audit_log\s+SET\s+entity_id\s*=([^;]*);`).FindStringSubmatch(body)
	if upd == nil {
		t.Fatalf("the Up body has no `UPDATE audit_log SET entity_id = ...` backfill statement")
	}
	if !strings.Contains(upd[1], "audit_log_entity_for(") {
		t.Errorf("the backfill UPDATE assigns %q, want it to call audit_log_entity_for", strings.TrimSpace(upd[1]))
	}
}

// AC-12: the definition the inventory scan reads is the one goose applies last. Filenames
// lead with the goose timestamp, so a replacement that sorted before the backfill would
// never reach the database.
func TestRLS_AuditResolverDefinerIsTheLatestMigration(t *testing.T) {
	definer, backfill := auditResolverDefinerName(t), auditEntityMigrationName(t)
	if definer == backfill {
		t.Fatalf("audit_log_entity_for is still defined only by %s — no migration replaces it", backfill)
	}
	if definer < backfill {
		t.Errorf("the resolver definer %s sorts before %s, so goose applies it first and the "+
			"replacement never takes effect", definer, backfill)
	}
}

// AC-2: the LATEST replacement is reversible. Its Down restores the previously shipped
// body instead of dropping the function, which the write-time trigger calls on every
// insert. Executed as the migrator in a rolled-back transaction.
//
// The target is resolved by content, so this test follows whichever migration currently
// defines the resolver last. That is EXTR-08-06's, which adds extraction.field_corrected
// and whose Down restores AUDIT-03's body -- where submission.failed IS attributed. So the
// event this test watches flip is the newest one, and submission.failed and
// submission.accepted are the two controls: they stay attributed through both bodies, and
// a dropped or wrong-body Down cannot fake that.
func TestRLS_AuditResolverReplacementIsReversible(t *testing.T) {
	requireHarness(t)
	ctx := context.Background()

	definer, backfill := auditResolverDefinerName(t), auditEntityMigrationName(t)
	if definer == backfill {
		t.Fatalf("no replacement migration exists: audit_log_entity_for is still defined by %s", backfill)
	}

	f := seedAuditEntityFixture(t)
	tx := migratorTx(t, ctx)
	if _, err := tx.Exec(ctx, `SELECT set_config('app.current_tenant', $1, true)`, f.tenant); err != nil {
		t.Fatalf("set tenant context: %v", err)
	}
	payload := auditPayloadJSON("invoice_id", f.invoice)
	resolve := func(event, stage string) *string {
		t.Helper()
		var got *string
		if err := tx.QueryRow(ctx,
			`SELECT audit_log_entity_for($1, $2::jsonb)::text`, event, payload).Scan(&got); err != nil {
			t.Fatalf("resolve %s after the %s body: %v", event, stage, err)
		}
		return got
	}
	wantEntity := func(event, stage string) {
		t.Helper()
		switch got := resolve(event, stage); {
		case got == nil:
			t.Errorf("after the %s body, %s resolves to NULL, want %s", stage, event, f.entity)
		case *got != f.entity:
			t.Errorf("after the %s body, %s resolves to %s, want %s", stage, event, *got, f.entity)
		}
	}

	if _, err := tx.Exec(ctx, auditEntitySectionOf(t, definer, "Up")); err != nil {
		t.Fatalf("Up body of %s failed: %v", definer, err)
	}
	wantEntity("extraction.field_corrected", "Up")
	wantEntity("submission.failed", "Up")

	if _, err := tx.Exec(ctx, auditEntitySectionOf(t, definer, "Down")); err != nil {
		t.Fatalf("Down body of %s failed: %v", definer, err)
	}
	if got := resolve("extraction.field_corrected", "Down"); got != nil {
		t.Errorf("after the Down body, extraction.field_corrected resolves to %s, want NULL", *got)
	}
	// The controls the Down cannot fake: a dropped resolver would error here, and a Down
	// that reverted the wrong line would return NULL for these too.
	wantEntity("submission.failed", "Down")
	wantEntity("submission.accepted", "Down")
}

// T6-7: exactly one audit_log_entity_for overload exists at every stage. AUDIT-01's Down is
// an unqualified `DROP FUNCTION audit_log_entity_for`, which raises 42725 against two
// overloads -- so a replacement that changed the signature would make the whole audit_log
// family irreversible, and nothing else says so.
func TestRLS_AuditResolverIsExactlyOneOverloadAtEveryStage(t *testing.T) {
	requireHarness(t)
	ctx := context.Background()

	definer := auditResolverDefinerName(t)
	tx := migratorTx(t, ctx)
	overloads := func(stage string) int {
		t.Helper()
		var n int
		if err := tx.QueryRow(ctx,
			`SELECT count(*) FROM pg_proc WHERE proname = 'audit_log_entity_for'`).Scan(&n); err != nil {
			t.Fatalf("count audit_log_entity_for overloads at the %s stage: %v", stage, err)
		}
		return n
	}

	// Control needle: the applied schema must already hold exactly one, or a later zero
	// would read as "still one overload" rather than "the function is gone".
	if n := overloads("baseline"); n != 1 {
		t.Fatalf("audit_log_entity_for overloads before any replay = %d, want 1 -- is the "+
			"database migrated? (`make migrate-up`)", n)
	}
	for _, section := range []string{"Up", "Down"} {
		if _, err := tx.Exec(ctx, auditEntitySectionOf(t, definer, section)); err != nil {
			t.Fatalf("%s body of %s failed: %v", section, definer, err)
		}
		if n := overloads(section); n != 1 {
			t.Errorf("audit_log_entity_for overloads after %s's %s body = %d, want 1", definer, section, n)
		}
	}
}

// AC-16: the trigger is created after the backfill has run and after the bracket closed,
// so the suspend/restore window spans no unrelated trigger DDL.
func TestRLS_AuditTriggerIsCreatedAfterTheBackfill(t *testing.T) {
	body := auditEntityStripComments(auditEntitySection(t, "Up"))

	trig := strings.Index(body, "CREATE TRIGGER audit_log_entity_on_insert")
	if trig < 0 {
		t.Fatalf("the Up body never creates the trigger audit_log_entity_on_insert")
	}
	upd := regexp.MustCompile(`(?is)UPDATE\s+audit_log\s+SET\s+entity_id\s*=`).FindStringIndex(body)
	if upd == nil {
		t.Fatalf("the Up body has no backfill UPDATE to order the trigger against")
	}
	enable := regexp.MustCompile(`(?i)\bENABLE\s+TRIGGER\s+audit_log_no_update_delete`).FindStringIndex(body)
	if enable == nil {
		t.Fatalf("the Up body never re-enables audit_log_no_update_delete")
	}

	if trig <= upd[0] {
		t.Errorf("CREATE TRIGGER is at offset %d, the backfill UPDATE at %d — the trigger must come after", trig, upd[0])
	}
	if trig <= enable[0] {
		t.Errorf("CREATE TRIGGER is at offset %d, ENABLE TRIGGER at %d — the trigger must be created "+
			"outside the suspend/restore bracket", trig, enable[0])
	}
}

// AC-16: the bracket suspends the append-only trigger and nothing else, and every trigger
// on the table is enabled once the body finishes.
func TestRLS_AuditBracketLeavesTheInsertTriggerEnabled(t *testing.T) {
	requireHarness(t)
	ctx := context.Background()

	body := auditEntityStripComments(auditEntitySection(t, "Up"))
	for _, c := range []struct {
		re   *regexp.Regexp
		want int
		what string
	}{
		{regexp.MustCompile(`(?i)\bDISABLE\s+TRIGGER\s+audit_log_no_update_delete`), 1, "DISABLE of the append-only trigger"},
		{regexp.MustCompile(`(?i)\bENABLE\s+TRIGGER\s+audit_log_no_update_delete`), 1, "ENABLE of the append-only trigger"},
		{regexp.MustCompile(`(?i)\bDISABLE\s+TRIGGER\s+audit_log_entity_on_insert`), 0, "DISABLE of the insert trigger"},
		{regexp.MustCompile(`(?i)\bENABLE\s+TRIGGER\s+audit_log_entity_on_insert`), 0, "ENABLE of the insert trigger"},
	} {
		if n := len(c.re.FindAllString(body, -1)); n != c.want {
			t.Errorf("%s appears %d times in the Up body, want %d", c.what, n, c.want)
		}
	}

	tx := auditEntityApplyUp(t, ctx)
	got := auditLogTriggersIn(t, ctx, tx)
	for _, name := range []string{"audit_log_no_truncate", "audit_log_no_update_delete", "audit_log_entity_on_insert"} {
		state, ok := got[name]
		if !ok {
			t.Errorf("trigger %s is absent after the body (have %v)", name, got)
			continue
		}
		if state != "O" {
			t.Errorf("trigger %s tgenabled = %q after the body, want %q", name, state, "O")
		}
	}
}

// AC-16: a BEFORE INSERT trigger cannot fire on the backfill's UPDATEs. The precondition
// matters — without the trigger the sentinel survives trivially.
func TestRLS_AuditInsertTriggerDoesNotFireOnUpdate(t *testing.T) {
	requireHarness(t)
	ctx := context.Background()

	f := seedAuditEntityFixture(t)
	seedAuditEntityRow(t, f.tenant, "document.read", auditPayloadJSON("id", f.document))

	tx := auditEntityApplyUp(t, ctx)
	if _, ok := auditLogTriggersIn(t, ctx, tx)["audit_log_entity_on_insert"]; !ok {
		t.Fatalf("audit_log_entity_on_insert does not exist after the Up body — the sentinel " +
			"below would survive trivially")
	}

	sentinel := uuid.NewString()
	if _, err := tx.Exec(ctx, `ALTER TABLE audit_log DISABLE TRIGGER audit_log_no_update_delete`); err != nil {
		t.Fatalf("suspend the append-only trigger: %v", err)
	}
	if _, err := tx.Exec(ctx, `SELECT set_config('app.current_tenant', $1, true)`, f.tenant); err != nil {
		t.Fatalf("set tenant context for the UPDATE: %v", err)
	}
	tag, err := tx.Exec(ctx, `UPDATE audit_log SET entity_id = $1 WHERE tenant_id = $2 AND event = 'document.read'`,
		sentinel, f.tenant)
	if err != nil {
		t.Fatalf("write the sentinel: %v", err)
	}
	if tag.RowsAffected() != 1 {
		t.Fatalf("sentinel UPDATE touched %d rows, want 1", tag.RowsAffected())
	}

	got := auditEntityIDsByEvent(t, ctx, tx, f.tenant)
	assertAuditEntity(t, got, "document.read", sentinel)
}

// --- catalog helpers ---------------------------------------------------------------

type auditCatalogState struct {
	hasEntityID bool
	indexes     []string
	triggers    map[string]string
	forceRLS    bool
}

func auditLogCatalogState(t *testing.T, ctx context.Context, pool *pgxpool.Pool) auditCatalogState {
	t.Helper()
	st := auditCatalogState{triggers: map[string]string{}}

	if err := pool.QueryRow(ctx,
		`SELECT count(*) = 1 FROM information_schema.columns
		   WHERE table_schema = 'public' AND table_name = 'audit_log' AND column_name = 'entity_id'`,
	).Scan(&st.hasEntityID); err != nil {
		t.Fatalf("read audit_log.entity_id presence: %v", err)
	}
	if err := pool.QueryRow(ctx,
		`SELECT relforcerowsecurity FROM pg_class WHERE oid = 'audit_log'::regclass`,
	).Scan(&st.forceRLS); err != nil {
		t.Fatalf("read audit_log relforcerowsecurity: %v", err)
	}

	rows, err := pool.Query(ctx,
		`SELECT indexname FROM pg_indexes WHERE schemaname = 'public' AND tablename = 'audit_log'`)
	if err != nil {
		t.Fatalf("list audit_log indexes: %v", err)
	}
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			t.Fatalf("scan index name: %v", err)
		}
		st.indexes = append(st.indexes, n)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate audit_log indexes: %v", err)
	}
	if len(st.indexes) == 0 {
		t.Fatalf("audit_log has no indexes — the catalog query is broken, so the comparison is vacuous")
	}
	sort.Strings(st.indexes)

	trows, err := pool.Query(ctx,
		`SELECT tgname, tgenabled FROM pg_trigger WHERE tgrelid = 'audit_log'::regclass AND NOT tgisinternal`)
	if err != nil {
		t.Fatalf("list audit_log triggers: %v", err)
	}
	defer trows.Close()
	for trows.Next() {
		var n, state string
		if err := trows.Scan(&n, &state); err != nil {
			t.Fatalf("scan trigger row: %v", err)
		}
		st.triggers[n] = state
	}
	if err := trows.Err(); err != nil {
		t.Fatalf("iterate audit_log triggers: %v", err)
	}
	if len(st.triggers) == 0 {
		t.Fatalf("audit_log has no user triggers — the catalog query is broken")
	}
	return st
}

// auditLogTriggersIn reads tgname -> tgenabled inside an open transaction, so it sees the
// migration body's uncommitted trigger DDL.
func auditLogTriggersIn(t *testing.T, ctx context.Context, tx pgx.Tx) map[string]string {
	t.Helper()
	rows, err := tx.Query(ctx,
		`SELECT tgname, tgenabled FROM pg_trigger WHERE tgrelid = 'audit_log'::regclass AND NOT tgisinternal`)
	if err != nil {
		t.Fatalf("list audit_log triggers in tx: %v", err)
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var n, state string
		if err := rows.Scan(&n, &state); err != nil {
			t.Fatalf("scan trigger row: %v", err)
		}
		out[n] = state
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate audit_log triggers in tx: %v", err)
	}
	if len(out) == 0 {
		t.Fatalf("audit_log has no user triggers inside the transaction — the query is broken")
	}
	return out
}

// auditEntityLoweredGuard returns the Up body with the size guard's threshold rewritten to
// 1, so the guard trips against any populated table. The file on disk is never touched.
func auditEntityLoweredGuard(t *testing.T) string {
	t.Helper()
	body := auditEntitySection(t, "Up")
	re := regexp.MustCompile(`(?i)IF\s+[A-Za-z_][A-Za-z0-9_]*\s*>\s*([0-9_]+)\s+THEN`)
	m := re.FindAllStringSubmatchIndex(body, -1)
	if len(m) != 1 {
		t.Fatalf("the Up body holds %d `IF <var> > <threshold> THEN` size guards, want exactly 1 — "+
			"without one the migration cannot abort above the threshold", len(m))
	}
	return body[:m[0][2]] + "1" + body[m[0][3]:]
}

// assertAuditGuardTripped asserts the guard aborted the migration with
// program_limit_exceeded (54000) rather than any other failure.
func assertAuditGuardTripped(t *testing.T, err error) {
	t.Helper()
	if err == nil {
		t.Fatalf("the Up body succeeded with the guard threshold lowered to 1, want it to abort")
	}
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		t.Fatalf("want a Postgres error from the size guard, got %v", err)
	}
	if pgErr.Code != "54000" {
		t.Fatalf("size guard raised SQLSTATE %s (%s), want 54000 program_limit_exceeded", pgErr.Code, pgErr.Message)
	}
}

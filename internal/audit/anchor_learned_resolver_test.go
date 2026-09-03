// Migration B: the FOURTH audit_log_entity_for definition, which adds
// extraction.anchor.learned to the extraction arm. The rule must exist before the first row --
// the resolver runs at BEFORE INSERT and audit_log is append-only, so a row written before the
// rule is unscoped forever.
//
// File cases read migrations.FS only; DB cases write through audit.Record as invoice_app under
// a tenant GUC. Helpers use an al* prefix.
package audit_test

import (
	"context"
	"encoding/json"
	"io/fs"
	"strconv"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/SimonOsipov/invoice-os/internal/platform/db"
	"github.com/SimonOsipov/invoice-os/migrations"
)

const (
	// The slug this story's migration carries. Deliberately not the
	// _audit_log_entity_for_extraction.sql suffix, which requireExtractionResolverMigration
	// fatals on at a count other than one.
	alResolverSuffix = "_audit_log_entity_for_anchor_learned.sql"

	// The definition Migration B replaces, and the body its Down must restore byte for byte.
	alRestoreTarget = "20260829195203_audit_log_entity_for_extraction.sql"

	// The newest migration on this branch before this subtask. A reverse-order landing froze
	// production deploys for nine days, and a PR environment gets a virgin database, so the
	// deploy gate is structurally incapable of catching one.
	alNewestBeforeThisStory = 20260902235137

	alEvent      = "extraction.anchor.learned"
	alPriorEvent = "extraction.field_corrected"
)

// --- file cases (no DB) ---------------------------------------------------------------

// Exactly one migration for this story, sorting after Migration A. Falsifiable at 0 (never
// created) and at 2 (a second `make migrate-create` leaving a stray file behind).
func TestExtraction_SingleAnchorLearnedResolverMigration(t *testing.T) {
	name := alRequireMigration(t)

	stamp, err := strconv.ParseInt(name[:14], 10, 64)
	if err != nil {
		t.Fatalf("leading 14 chars of %q are not a goose timestamp: %v", name, err)
	}
	if stamp <= alNewestBeforeThisStory {
		t.Errorf("migration timestamp = %d, want strictly greater than %d -- a migration that "+
			"sorts before an already-applied one never runs on a live database",
			stamp, alNewestBeforeThisStory)
	}
	// The restore target must already be applied when this one runs, or the Down would put
	// back a body the database never held.
	target, err := strconv.ParseInt(alRestoreTarget[:14], 10, 64)
	if err != nil {
		t.Fatalf("leading 14 chars of %q are not a goose timestamp: %v", alRestoreTarget, err)
	}
	if stamp <= target {
		t.Errorf("migration timestamp = %d, want strictly greater than %s's %d", stamp, alRestoreTarget, target)
	}
}

// A-04b: the Down is a CREATE OR REPLACE whose function body is byte-identical to the body
// currently live in the database. This is the only oracle for AC-4's "exactly": a Down that
// differs from the shipped body only in a comment still passes the behavioural replay.
func TestExtraction_AnchorLearnedMigrationDownRestoresTheExtractionBody(t *testing.T) {
	name := alRequireMigration(t)
	down := extractionGooseSection(t, name, "Down")

	if strings.Contains(strings.ToUpper(down), "DROP FUNCTION") {
		t.Errorf("%s's Down drops audit_log_entity_for, want a CREATE OR REPLACE of %s's body -- "+
			"audit_log_set_entity calls it on every insert", name, alRestoreTarget)
	}

	want := extractionResolverFnBody(t, alRestoreTarget, "Up",
		extractionGooseSection(t, alRestoreTarget, "Up"))
	got := extractionResolverFnBody(t, name, "Down", down)
	if got != want {
		t.Errorf("%s's Down body is %d bytes, %s's Up body is %d bytes, and they differ -- the "+
			"Down must restore the shipped body byte for byte", name, len(got), alRestoreTarget, len(want))
	}

	// Controls: without these, a Down that merely copied this migration's own Up would pass the
	// comparison above if the extractor were reading the wrong section, and a migration that
	// replaces nothing would pass every assertion in this file.
	up := extractionResolverFnBody(t, name, "Up", extractionGooseSection(t, name, "Up"))
	if up == want {
		t.Errorf("%s's Up body is byte-identical to %s's -- the migration replaces nothing", name, alRestoreTarget)
	}
	if !strings.Contains(up, alEvent) {
		t.Errorf("%s's Up body never names %s", name, alEvent)
	}
	if strings.Contains(want, alEvent) {
		t.Errorf("%s's body already names %s -- the restore target is not the shipped body", alRestoreTarget, alEvent)
	}
	// The prior event survives BOTH bodies: it is what tells a correct Down from one that
	// reverted the whole extraction arm.
	for _, pair := range []struct{ label, body string }{{"Up", up}, {"Down", got}} {
		if !strings.Contains(pair.body, alPriorEvent) {
			t.Errorf("%s's %s body drops %s -- the extraction arm is grown, never replaced", name, pair.label, alPriorEvent)
		}
	}
}

// The event goes on its own ELSIF, never into rule A or rule B: both are set-equal-pinned to
// the generated invoice_id column by TestAudit_GeneratedInvoiceIDListsMatchTheLiveResolver, and
// rule B would additionally put the event under the invoice_number obligation AC-3 forbids.
func TestExtraction_AnchorLearnedIsNotAddedToRuleAOrRuleB(t *testing.T) {
	name := alRequireMigration(t)
	up := extractionResolverFnBody(t, name, "Up", extractionGooseSection(t, name, "Up"))

	arms := alEventArms(t, name, up)
	if len(arms) < 4 {
		t.Fatalf("%s's Up body holds %d `p_event IN (...)` arm(s), want at least 4 -- the parse "+
			"read nothing, so the membership checks below are vacuous", name, len(arms))
	}
	// Control needles first: each of the first two arms must report a hit of its own.
	if !strings.Contains(arms[0], "invoice.created") {
		t.Fatalf("control needle: arm 0 does not name invoice.created, so it is not rule A")
	}
	if !strings.Contains(arms[1], "submission.failed") {
		t.Fatalf("control needle: arm 1 does not name submission.failed, so it is not rule B")
	}
	for i, arm := range arms[:2] {
		if strings.Contains(arm, alEvent) {
			t.Errorf("%s adds %s to arm %d, want a separate ELSIF -- arms 0 and 1 are set-equal-pinned "+
				"to the generated invoice_id column", name, alEvent, i)
		}
	}
	if !strings.Contains(strings.Join(arms[2:], "\n"), alEvent) {
		t.Errorf("%s never names %s outside rules A and B", name, alEvent)
	}
}

// --- DB cases -------------------------------------------------------------------------

// A-04: the spelling pin. The resolver keys this event on payload->>'invoice_id'; the bare `id`
// spelling resolves NULL, which the entity_id column spells "workspace-level".
func TestAudit_ResolverKeysAnchorLearnedOnInvoiceId(t *testing.T) {
	f := requireFixture(t)
	requireInsertTrigger(t, f)
	fx := seedTriggerFixture(t, f)
	ctx := context.Background()

	resolve := func(event string, payload map[string]any) *string {
		t.Helper()
		raw, err := json.Marshal(payload)
		if err != nil {
			t.Fatalf("marshal payload %v: %v", payload, err)
		}
		var got *string
		if err := db.WithinTenantTx(ctx, f.app, fx.tenant, func(tx pgx.Tx) error {
			return tx.QueryRow(ctx,
				`SELECT audit_log_entity_for($1, $2::jsonb)::text`, event, string(raw)).Scan(&got)
		}); err != nil {
			t.Fatalf("resolve %s with %v: %v", event, payload, err)
		}
		return got
	}

	// Control needle: the already-shipped event must resolve through the same fixture, or a
	// NULL below is a fixture whose invoice is unreachable rather than a missing rule.
	if got := resolve(alPriorEvent, map[string]any{"invoice_id": fx.invoice}); got == nil || *got != fx.entity {
		t.Fatalf("control needle: %s with invoice_id resolves %s, want %s -- the fixture invoice is "+
			"unreachable, so every assertion below is vacuous", alPriorEvent, extractionShow(got), fx.entity)
	}

	if got := resolve(alEvent, map[string]any{"invoice_id": fx.invoice}); got == nil || *got != fx.entity {
		t.Errorf("audit_log_entity_for(%q, {\"invoice_id\": ...}) = %s, want %s -- an unresolved row "+
			"claims a client's action was firm-wide, and audit_log is append-only",
			alEvent, extractionShow(got), fx.entity)
	}
	if got := resolve(alEvent, map[string]any{"id": fx.invoice}); got != nil {
		t.Errorf("audit_log_entity_for(%q, {\"id\": ...}) = %s, want NULL -- the payload spelling is "+
			"invoice_id, and a resolver that read both would hide a writer using the wrong one", alEvent, *got)
	}
	if got := resolve(alEvent, map[string]any{"invoice_id": uuid.NewString()}); got != nil {
		t.Errorf("audit_log_entity_for(%q, {\"invoice_id\": <absent invoice>}) = %s, want NULL", alEvent, *got)
	}
}

// A-05 and A-06: a row written through the REAL audit.Record lands with the invoice's entity,
// and its generated invoice_id column stays NULL -- rules A and B did not move, so the column
// must not have been rewritten. Recorded, not fixed.
func TestAudit_InsertTriggerResolvesExtractionAnchorLearned(t *testing.T) {
	f := requireFixture(t)
	requireInsertTrigger(t, f)
	fx := seedTriggerFixture(t, f)

	recordAudit(t, f, fx.tenant, alEvent,
		map[string]any{"arm": "learned", "invoice_id": fx.invoice})
	recordAudit(t, f, fx.tenant, "submission.accepted",
		map[string]any{"arm": "control", "invoice_id": fx.invoice})

	rows := extractionAuditRows(t, f, fx.tenant)
	extractionAssertEntity(t, rows, "control", fx.entity)
	extractionAssertEntity(t, rows, "learned", fx.entity)

	if got := rows["control"].invoiceID; got == nil || *got != fx.invoice {
		t.Fatalf("control needle: submission.accepted's generated invoice_id = %v, want %s -- the "+
			"column is not populating, so the NULL assertion below is vacuous",
			extractionShow(got), fx.invoice)
	}
	if got := rows["learned"].invoiceID; got != nil {
		t.Errorf("%s's generated invoice_id = %s, want NULL -- rules A and B did not move, so the "+
			"column must not have been rewritten", alEvent, *got)
	}
}

// --- helpers ----------------------------------------------------------------------------

// alRequireMigration returns this story's single migration, failing loudly at any count other
// than one. Callers may index the name only because this counted first.
func alRequireMigration(t *testing.T) string {
	t.Helper()
	all, err := fs.Glob(migrations.FS, "*.sql")
	if err != nil {
		t.Fatalf("glob migrations.FS: %v", err)
	}
	if len(all) == 0 {
		t.Fatalf("migrations.FS contains no *.sql files -- the embed is broken")
	}
	var matches []string
	for _, name := range all {
		if strings.HasSuffix(name, alResolverSuffix) {
			matches = append(matches, name)
		}
	}
	if len(matches) != 1 {
		t.Fatalf("migrations matching *%s = %d %v, want exactly 1 (scanned %d files)",
			alResolverSuffix, len(matches), matches, len(all))
	}
	if len(matches[0]) < 14 {
		t.Fatalf("migration name %q is shorter than a 14-digit goose timestamp", matches[0])
	}
	return matches[0]
}

// alEventArms returns each `p_event IN (...)` list of a resolver body, in source order.
func alEventArms(t *testing.T, name, body string) []string {
	t.Helper()
	const head = "p_event IN ("
	var out []string
	rest := body
	for {
		i := strings.Index(rest, head)
		if i < 0 {
			return out
		}
		rest = rest[i+len(head):]
		j := strings.Index(rest, ")")
		if j < 0 {
			t.Fatalf("%s: a `%s` list is never closed", name, head)
		}
		out = append(out, rest[:j])
		rest = rest[j:]
	}
}

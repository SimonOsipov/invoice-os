// Rule-set v6 (v5 plus the credit and debit note rules), dated 2027-01-01. These tests read the
// migrated dev DB (`make migrate-up`), not the migration file; only the Down test reads the file.
package validation

import (
	"context"
	"errors"
	"io/fs"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/SimonOsipov/invoice-os/migrations"
	"github.com/jackc/pgx/v5/pgconn"
)

const (
	v6Start         = "2027-01-01"
	v6MigrationGlob = "*_rule_set_v6.sql"
	v6NoteGuard     = `has(invoice.document_type) && invoice.document_type in ['credit_note', 'debit_note']`
	v6IRNKey        = "billing-reference-irn-required"
	v6DateKey       = "billing-reference-issue-date-required"
)

// requireV6 fails, on this assertion, unless v6 is migrated with rules, and returns its id.
func requireV6(t *testing.T, q queryRower) string {
	t.Helper()
	var id string
	var n int
	if err := q.QueryRow(context.Background(),
		`SELECT coalesce(max(v.id::text), ''), count(r.id)
		   FROM rule_set_versions v LEFT JOIN rules r ON r.rule_set_version_id = v.id
		  WHERE v.version = 6`).Scan(&id, &n); err != nil {
		t.Fatalf("read v6: %v", err)
	}
	if id == "" || n == 0 {
		t.Fatalf("v6 is not migrated: id %q with %d rules, want a v6 with rules (run make migrate-up)", id, n)
	}
	return id
}

func v6RuleCount(t *testing.T, q queryRower, version int, enabledOnly bool) int {
	t.Helper()
	var n int
	if err := q.QueryRow(context.Background(),
		`SELECT count(r.id) FROM rule_set_versions v JOIN rules r ON r.rule_set_version_id = v.id
		  WHERE v.version = $1 AND (r.enabled OR NOT $2)`, version, enabledOnly).Scan(&n); err != nil {
		t.Fatalf("count the rules of v%d: %v", version, err)
	}
	return n
}

func v6MigrationSQL(t *testing.T) string {
	t.Helper()
	matches, err := fs.Glob(migrations.FS, v6MigrationGlob)
	if err != nil || len(matches) != 1 {
		t.Fatalf("want exactly one migrations/%s, got %v (err %v)", v6MigrationGlob, matches, err)
	}
	raw, err := fs.ReadFile(migrations.FS, matches[0])
	if err != nil {
		t.Fatalf("read %s: %v", matches[0], err)
	}
	return string(raw)
}

func TestV6_IsSealedAndDated(t *testing.T) {
	super, _ := dbTestPools(t)
	requireV6(t, super)

	var sealed bool
	var from, notes *string
	if err := super.QueryRow(context.Background(),
		`SELECT sealed, effective_from::text, notes FROM rule_set_versions WHERE version = 6`).Scan(&sealed, &from, &notes); err != nil {
		t.Fatalf("read v6: %v", err)
	}
	if !sealed {
		t.Error("v6.sealed = false, want true")
	}
	if from == nil || *from != v6Start {
		t.Errorf("v6.effective_from = %v, want %s", from, v6Start)
	}
	if notes == nil || !strings.HasPrefix(*notes, "MBS global rule-set v6") {
		t.Errorf("v6.notes = %v, want prefix %q", notes, "MBS global rule-set v6")
	}
	if n := v6RuleCount(t, super, 6, false); n != 69 {
		t.Errorf("v6 holds %d rules, want 69", n)
	}
	if n := v6RuleCount(t, super, 6, true); n != 69 {
		t.Errorf("v6 holds %d enabled rules, want 69", n)
	}
}

func TestV6_CarriesEveryV5RuleVerbatim(t *testing.T) {
	super, _ := dbTestPools(t)
	requireV6(t, super)
	v5 := ruleRowsByKey(t, super, 5)
	v6 := ruleRowsByKey(t, super, 6)
	if len(v5) != 67 {
		t.Fatalf("v5 holds %d rules, want 67 (the comparison below would assert nothing)", len(v5))
	}
	for key, want := range v5 {
		got, ok := v6[key]
		if !ok {
			t.Errorf("%s missing from v6", key)
			continue
		}
		want.Enabled = true
		if !reflect.DeepEqual(got, want) {
			t.Errorf("v6 %s = %+v, want v5's row with enabled=true: %+v", key, got, want)
		}
	}
	var added []string
	for key := range v6 {
		if _, ok := v5[key]; !ok {
			added = append(added, key)
		}
	}
	slices.Sort(added)
	if want := []string{v6IRNKey, v6DateKey}; !slices.Equal(added, want) {
		t.Errorf("keys v6 adds to v5 = %v, want exactly %v", added, want)
	}
}

func TestV6_VersionForBoundary(t *testing.T) {
	super, _ := dbTestPools(t)
	for date, want := range map[string]int{"2026-12-31": 4, v6Start: 6, "2027-06-30": 6} {
		if got := versionOnDate(t, super, date); got != want {
			t.Errorf("rule_set_version_for(%s) = v%d, want v%d", date, got, want)
		}
	}
}

func TestV6_NoteRulesGuardOnDocumentType(t *testing.T) {
	super, _ := dbTestPools(t)
	requireV6(t, super)
	rows := ruleRowsByKey(t, super, 6)
	for key, target := range map[string]string{v6IRNKey: "billing_reference.irn", v6DateKey: "billing_reference.issue_date"} {
		r, ok := rows[key]
		if !ok {
			t.Errorf("v6 is missing %s", key)
			continue
		}
		if r.Type != "required" || r.Severity != "error" || r.Target != target || r.Scope != "document" || r.Params != "{}" || !r.Enabled {
			t.Errorf("v6 %s = %+v, want required/error/%s/document, params {}, enabled", key, r, target)
		}
		if r.When == nil || *r.When != v6NoteGuard {
			t.Errorf("v6 %s when = %v, want %q", key, r.When, v6NoteGuard)
		}
	}
}

func TestV6_NoteRulesFireOnlyOnANoteMissingTheField(t *testing.T) {
	super, _ := dbTestPools(t)
	seedV5Lists(t, super)
	rs := v5LoadOn(t, edBegin(t, context.Background(), super), v6Start, 6)

	build := func(docType string, ref map[string]any) Payload {
		p := v5B2BPayload()
		inv := invoiceOf(p)
		if docType != "" {
			inv["document_type"] = docType
		}
		if ref != nil {
			inv["billing_reference"] = ref
		}
		return p
	}
	sameKeys := func(what string, p Payload, keys ...string) Result {
		t.Helper()
		res := evalV5(t, rs, p)
		got, want := violationKeys(res), slices.Clone(keys)
		slices.Sort(got)
		slices.Sort(want)
		if !slices.Equal(got, want) {
			t.Errorf("%s: violations = %s, want exactly %v", what, showViolations(res), want)
		}
		return res
	}

	ref := func(irn, date string) map[string]any {
		m := map[string]any{}
		if irn != "" {
			m["irn"] = irn
		}
		if date != "" {
			m["issue_date"] = date
		}
		return m
	}
	const irn, date = "INV0001-ABCD1234-20261215", "2026-12-15"

	for _, dt := range []string{"credit_note", "debit_note"} {
		res := sameKeys(dt+" with no reference", build(dt, nil), v6IRNKey, v6DateKey)
		if len(res.Violations) == 2 {
			if v := violationOf(t, res, v6IRNKey); v.Path != "billing_reference.irn" {
				t.Errorf("%s Path = %q, want billing_reference.irn", v6IRNKey, v.Path)
			}
			if v := violationOf(t, res, v6DateKey); v.Path != "billing_reference.issue_date" {
				t.Errorf("%s Path = %q, want billing_reference.issue_date", v6DateKey, v.Path)
			}
		}
		sameKeys(dt+" with an empty reference", build(dt, ref("", "")), v6IRNKey, v6DateKey)
		sameKeys(dt+" with the IRN only", build(dt, ref(irn, "")), v6DateKey)
		sameKeys(dt+" with the date only", build(dt, ref("", date)), v6IRNKey)
		sameKeys(dt+" with both", build(dt, ref(irn, date)))
	}
	sameKeys("commercial_invoice with no reference", build("commercial_invoice", nil))
	sameKeys("no document_type, no reference", build("", nil))
	sameKeys("no document_type, empty reference", build("", ref("", "")))
}

func TestV6_LeavesV4AndV5RowsAlone(t *testing.T) {
	super, _ := dbTestPools(t)
	requireV6(t, super)
	if n := v6RuleCount(t, super, 4, false); n != 20 {
		t.Errorf("v4 holds %d rules, want 20", n)
	}
	if n := v6RuleCount(t, super, 5, false); n != 67 {
		t.Errorf("v5 holds %d rules, want 67", n)
	}
	for version, from := range map[int]string{4: "2026-08-06", 5: v5Start} {
		var sealed bool
		var got *string
		if err := super.QueryRow(context.Background(),
			`SELECT sealed, effective_from::text FROM rule_set_versions WHERE version = $1`, version).Scan(&sealed, &got); err != nil {
			t.Fatalf("read v%d: %v", version, err)
		}
		if !sealed || got == nil || *got != from {
			t.Errorf("v%d sealed=%t effective_from=%v, want sealed from %s", version, sealed, got, from)
		}
	}
}

func TestV6_SealedVersionRefusesARule(t *testing.T) {
	super, _ := dbTestPools(t)
	ctx := context.Background()
	v6ID := requireV6(t, super)

	tx := edBegin(t, ctx, super)
	sp, err := tx.Begin(ctx)
	if err != nil {
		t.Fatalf("savepoint: %v", err)
	}
	_, err = sp.Exec(ctx,
		`INSERT INTO rules (rule_set_version_id, key, type, target, params, severity, message, scope, enabled)
		 VALUES ($1, 'v6-probe', 'required', 'supplier.tin', '{}', 'error', 'probe', 'document', true)`, v6ID)
	_ = sp.Rollback(ctx)
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "23001" || !strings.Contains(pgErr.Message, "immutable") {
		t.Errorf("insert into sealed v6 = %v, want restrict_violation (23001) from rules_content_lock", err)
	}
}

func TestV6_DownRemovesV6(t *testing.T) {
	super, _ := dbTestPools(t)
	ctx := context.Background()
	requireV6(t, super)

	raw := v6MigrationSQL(t)
	down := gooseDownStatements(t, raw)
	if len(down) == 0 {
		t.Fatal("the v6 migration has no Down statements")
	}
	tx := edBegin(t, ctx, super)
	v6ID := versionIDByVersion(t, ctx, tx, 6)
	v5ID := versionIDByVersion(t, ctx, tx, 5)
	for _, stmt := range down {
		if _, err := tx.Exec(ctx, stmt); err != nil {
			t.Fatalf("Down statement failed: %v\n%s", err, stmt)
		}
	}

	var versions, orphans int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM rule_set_versions WHERE version = 6`).Scan(&versions); err != nil {
		t.Fatal(err)
	}
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM rules WHERE rule_set_version_id = $1`, v6ID).Scan(&orphans); err != nil {
		t.Fatal(err)
	}
	if versions != 0 || orphans != 0 {
		t.Errorf("after the Down: %d v6 rows, %d v6 rules, want 0 and 0", versions, orphans)
	}
	if n := v6RuleCount(t, tx, 5, false); n != 67 {
		t.Errorf("v5 after the Down holds %d rules, want 67", n)
	}
	edWant(t, edVersionFor(t, ctx, tx, v6Start), v5ID, "rule_set_version_for("+v6Start+") after the Down")
	for _, tc := range []struct{ table, trigger string }{{"rules", "rules_content_lock"}, {"rule_set_versions", "rule_set_versions_seal_guard"}} {
		var enabled string
		if err := tx.QueryRow(ctx,
			`SELECT tgenabled FROM pg_trigger WHERE tgname = $1 AND tgrelid = $2::regclass`, tc.trigger, tc.table).Scan(&enabled); err != nil {
			t.Fatalf("read tgenabled of %s on %s: %v", tc.trigger, tc.table, err)
		}
		if enabled != "O" {
			t.Errorf("%s on %s tgenabled = %q after the Down, want \"O\" (re-enabled)", tc.trigger, tc.table, enabled)
		}
	}

	up := raw[strings.Index(raw, "-- +goose Up")+len("-- +goose Up") : strings.Index(raw, "-- +goose Down")]
	if _, err := tx.Exec(ctx, up); err != nil {
		t.Fatalf("apply the v6 migration's Up after its Down: %v", err)
	}
	if n := v6RuleCount(t, tx, 6, false); n != 69 {
		t.Errorf("v6 after Down then Up holds %d rules, want 69", n)
	}
	edWant(t, edVersionFor(t, ctx, tx, v6Start), versionIDByVersion(t, ctx, tx, 6), "rule_set_version_for("+v6Start+") after Down then Up")
}

func TestV6_HasNoRecheckMarker(t *testing.T) {
	super, _ := dbTestPools(t)
	requireV6(t, super)
	count := func(version int) int {
		var n int
		if err := super.QueryRow(context.Background(),
			`SELECT count(*) FROM rule_set_version_rechecks m JOIN rule_set_versions v ON v.id = m.rule_set_version_id
			  WHERE v.version = $1`, version).Scan(&n); err != nil {
			t.Fatalf("count rechecks of v%d: %v", version, err)
		}
		return n
	}
	if n := count(4); n != 1 {
		t.Fatalf("v4 recheck markers = %d, want 1 (the probe below would assert nothing)", n)
	}
	if n := count(6); n != 0 {
		t.Errorf("v6 recheck markers = %d, want 0 (the re-check writes it on %s)", n, v6Start)
	}
}

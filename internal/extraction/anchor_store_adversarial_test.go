// anchor_store_adversarial_test.go: the edge, negative and concurrency cases the acceptance
// specs leave open. Same package as anchor_store_db_test.go, so stRequire stays the only skip site.
package extraction_test

import (
	"context"
	"errors"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/SimonOsipov/invoice-os/internal/extraction"
	"github.com/SimonOsipov/invoice-os/internal/platform/db"
)

// stAnchorRuleUnknownKind passes the table's jsonb CHECK -- the CHECK asserts the three keys
// are present and reads no value -- and fails ParseRule's relation-kind switch.
const stAnchorRuleUnknownKind = `{"label":"invoice","relation":{"kind":"diagonal","max_distance":0},"shape":"invoice_number"}`

// stAssertEmptyNotNil is the never-nil claim stated as nil-ness, not as length: len(nil) is 0
// too, so a length check alone passes on the bug it should catch.
func stAssertEmptyNotNil(t *testing.T, out []extraction.AnchorRule, what string) {
	t.Helper()
	if reflect.ValueOf(out).IsNil() {
		t.Errorf("%s returned a nil slice; nil marshals to null, not to an empty array", what)
	}
	if len(out) != 0 {
		t.Errorf("%s returned %d row(s), want none", what, len(out))
	}
}

// stAnchorRuleCreatedAt reads a row's created_at as the superuser, so a test can prove the
// scan order it depends on rather than assume it.
func stAnchorRuleCreatedAt(t *testing.T, ctx context.Context, id string) time.Time {
	t.Helper()
	var at time.Time
	if err := stRequire(t).super.QueryRow(ctx,
		`SELECT created_at FROM extraction_anchor_rules WHERE id = $1`, id).Scan(&at); err != nil {
		t.Fatalf("read created_at for %s: %v", id, err)
	}
	return at
}

func TestAnchorRulesFor_RefusesAnUnusableTenantWithAnEmptyNotNilSlice(t *testing.T) {
	ctx := t.Context()
	s := stStore(t)

	// db.WithinTenantTx parses the tenant before it opens a transaction, so on each of these
	// the closure never runs and the exported wrapper's own initialiser is the only thing
	// between a caller and a JSON null.
	for _, tenantID := range []string{"", " ", "not-a-uuid", uuid.NewString() + "x"} {
		out, err := s.AnchorRulesFor(ctx, tenantID, "layout-any")
		if !errors.Is(err, db.ErrNoTenant) {
			t.Errorf("AnchorRulesFor(tenant %q) returned error %v, want db.ErrNoTenant", tenantID, err)
		}
		stAssertEmptyNotNil(t, out, "AnchorRulesFor(tenant "+strconv.Quote(tenantID)+")")
	}
}

func TestAnchorRulesFor_AnEmptyFingerprintMatchesNothing(t *testing.T) {
	ctx := t.Context()
	s := stStore(t)
	tenantID, _ := stTenant(t, ctx)

	// The column's CHECK forbids an empty layout_fingerprint, so no row can carry one. The
	// read must still be an ordinary empty result, not an error.
	seeded := stSeedAnchorRule(t, ctx, tenantID, "layout-real", "invoice_number", stAnchorRuleValid, extraction.RuleSchemaVersion)

	// Positive control: the seeded row IS readable under its own fingerprint, so the empty
	// result below is the fingerprint filtering and not a tenant or seeding failure.
	if got, err := s.AnchorRulesFor(ctx, tenantID, "layout-real"); err != nil {
		t.Fatalf("AnchorRulesFor(layout-real): %v", err)
	} else if len(got) != 1 || got[0].ID != seeded {
		t.Fatalf("AnchorRulesFor(layout-real) returned %d row(s), want the seeded %s", len(got), seeded)
	}

	out, err := s.AnchorRulesFor(ctx, tenantID, "")
	if err != nil {
		t.Fatalf("AnchorRulesFor with an empty fingerprint: %v, want an empty result and no error", err)
	}
	stAssertEmptyNotNil(t, out, "AnchorRulesFor with an empty fingerprint")
}

func TestAnchorRulesFor_ErrorsOnAnUnknownRelationKind(t *testing.T) {
	ctx := t.Context()
	s := stStore(t)
	tenantID, _ := stTenant(t, ctx)

	badID := stSeedAnchorRule(t, ctx, tenantID, "layout-bad-kind", "invoice_number", stAnchorRuleUnknownKind, extraction.RuleSchemaVersion)

	out, err := s.AnchorRulesFor(ctx, tenantID, "layout-bad-kind")
	if err == nil {
		t.Fatal("AnchorRulesFor accepted a rule whose relation kind ParseRule does not know")
	}
	if !strings.Contains(err.Error(), badID) {
		t.Errorf("AnchorRulesFor error %q does not name the offending row id %s", err.Error(), badID)
	}
	if !strings.Contains(err.Error(), "diagonal") {
		t.Errorf("AnchorRulesFor error %q does not name the rejected kind", err.Error())
	}
	stAssertEmptyNotNil(t, out, "AnchorRulesFor over an unknown relation kind")
}

func TestAnchorRulesFor_AParseFailureDiscardsTheRowsAlreadyRead(t *testing.T) {
	ctx := t.Context()
	s := stStore(t)
	tenantID, _ := stTenant(t, ctx)

	// The bad row is seeded FIRST and so is the OLDER of the two; under ORDER BY created_at
	// DESC the good row is scanned before it and is already in the accumulator when the parse
	// fails. A fingerprint holding only the bad row cannot catch a partial return.
	badID := stSeedAnchorRule(t, ctx, tenantID, "layout-mixed", "invoice_number", stAnchorRuleUnparseable, extraction.RuleSchemaVersion)
	goodID := stSeedAnchorRule(t, ctx, tenantID, "layout-mixed", "total_amount", stAnchorRuleValid, extraction.RuleSchemaVersion)
	if !stAnchorRuleCreatedAt(t, ctx, goodID).After(stAnchorRuleCreatedAt(t, ctx, badID)) {
		t.Fatalf("the good row %s is not newer than the bad row %s, so it is not scanned first and this test proves nothing", goodID, badID)
	}

	out, err := s.AnchorRulesFor(ctx, tenantID, "layout-mixed")
	if err == nil {
		t.Fatal("AnchorRulesFor accepted an unparseable rule and reported no error")
	}
	if !strings.Contains(err.Error(), badID) {
		t.Errorf("AnchorRulesFor error %q does not name the offending row id %s", err.Error(), badID)
	}
	stAssertEmptyNotNil(t, out, "AnchorRulesFor over a fingerprint whose newest row parses and whose oldest does not")
}

func TestAnchorRulesFor_ReturnsEveryRuleForABusyFingerprint(t *testing.T) {
	ctx := t.Context()
	s := stStore(t)
	tenantID, _ := stTenant(t, ctx)
	h := stRequire(t)

	const busy = 50
	ids := make([]string, busy)
	for i := range ids {
		ids[i] = uuid.NewString()
	}
	// One statement, so now() ties every row and seq alone is the whole order. INSERT ...
	// SELECT unnest(array) assigns nextval in array order, so seq DESC reads them back reversed.
	if _, err := h.super.Exec(ctx,
		`INSERT INTO extraction_anchor_rules (id, tenant_id, layout_fingerprint, field_name, rule, rule_schema_version)
		 SELECT u, $2, $3, $4, $5, $6 FROM unnest($1::uuid[]) AS u`,
		ids, tenantID, "layout-busy", "invoice_number", stAnchorRuleValid, extraction.RuleSchemaVersion); err != nil {
		t.Fatalf("seed %d rules: %v", busy, err)
	}

	out, err := s.AnchorRulesFor(ctx, tenantID, "layout-busy")
	if err != nil {
		t.Fatalf("AnchorRulesFor: %v", err)
	}
	if len(out) != busy {
		t.Fatalf("AnchorRulesFor returned %d rule(s), want all %d -- a truncated read is the failure this test exists for", len(out), busy)
	}

	want := slices.Clone(ids)
	slices.Reverse(want)
	for i, w := range want {
		if out[i].ID != w {
			t.Fatalf("position %d is %s, want %s; %d rows tied on created_at must come back in seq DESC order", i, out[i].ID, w, busy)
		}
		if out[i].Rule.Shape != extraction.ShapeInvoiceNumber {
			t.Fatalf("position %d decoded shape %q, want %q", i, out[i].Rule.Shape, extraction.ShapeInvoiceNumber)
		}
	}
}

func TestAnchorRulesFor_ARuleIdIsUniqueAcrossEveryTenant(t *testing.T) {
	ctx := t.Context()
	s := stStore(t)
	tenantA, _ := stTenant(t, ctx)
	tenantB, _ := stTenant(t, ctx)
	h := stRequire(t)

	idA := stSeedAnchorRule(t, ctx, tenantA, "layout-dup", "invoice_number", stAnchorRuleValid, extraction.RuleSchemaVersion)

	// id is the PRIMARY KEY, so the same id under another tenant is refused outright -- the
	// (tenant_id, id) unique constraint is the composite-FK target, not a second id namespace.
	_, err := h.super.Exec(ctx,
		`INSERT INTO extraction_anchor_rules (id, tenant_id, layout_fingerprint, field_name, rule, rule_schema_version)
		 VALUES ($1, $2, $3, $4, $5, $6)`,
		idA, tenantB, "layout-dup", "invoice_number", stAnchorRuleValid, extraction.RuleSchemaVersion)
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "23505" {
		t.Fatalf("re-using rule id %s under tenant B returned %v, want SQLSTATE 23505", idA, err)
	}

	// Tenant A's row is untouched by the refused write, and tenant B still has nothing.
	out, err := s.AnchorRulesFor(ctx, tenantA, "layout-dup")
	if err != nil {
		t.Fatalf("AnchorRulesFor(tenantA): %v", err)
	}
	if len(out) != 1 || out[0].ID != idA {
		t.Errorf("AnchorRulesFor(tenantA) returned %d row(s), want only %s", len(out), idA)
	}
	outB, err := s.AnchorRulesFor(ctx, tenantB, "layout-dup")
	if err != nil {
		t.Fatalf("AnchorRulesFor(tenantB): %v", err)
	}
	stAssertEmptyNotNil(t, outB, "AnchorRulesFor(tenantB) after the refused duplicate")
}

func TestAnchorRulesFor_ConcurrentReadsDoNotCrossTalk(t *testing.T) {
	ctx := t.Context()
	s := stStore(t)
	tenantA, _ := stTenant(t, ctx)
	tenantB, _ := stTenant(t, ctx)

	// One fingerprint string, two tenants: app.current_tenant is set per transaction on a
	// POOLED connection, so a leak between two concurrent readers would show up here.
	const fp = "layout-concurrent"
	idA := stSeedAnchorRule(t, ctx, tenantA, fp, "invoice_number", stAnchorRuleValid, extraction.RuleSchemaVersion)
	idB := stSeedAnchorRule(t, ctx, tenantB, fp, "total_amount", stAnchorRuleValid, extraction.RuleSchemaVersion)

	type result struct {
		tenant string
		ids    []string
		err    error
	}
	const rounds = 40
	results := make([]result, 2*rounds)

	var wg sync.WaitGroup
	for i := range rounds {
		for j, tenantID := range []string{tenantA, tenantB} {
			wg.Add(1)
			go func(slot int, tenantID string) {
				defer wg.Done()
				out, err := s.AnchorRulesFor(ctx, tenantID, fp)
				ids := make([]string, len(out))
				for k, r := range out {
					ids[k] = r.ID
				}
				results[slot] = result{tenant: tenantID, ids: ids, err: err}
			}(2*i+j, tenantID)
		}
	}
	wg.Wait()

	want := map[string]string{tenantA: idA, tenantB: idB}
	if len(results) == 0 {
		t.Fatal("no concurrent reads ran, so the loop below quantifies over nothing")
	}
	for slot, r := range results {
		if r.err != nil {
			t.Errorf("read %d for tenant %s: %v", slot, r.tenant, r.err)
			continue
		}
		if len(r.ids) != 1 || r.ids[0] != want[r.tenant] {
			t.Errorf("read %d for tenant %s returned %v, want exactly [%s]", slot, r.tenant, r.ids, want[r.tenant])
		}
	}
}

func TestAnchorRulesFor_PropagatesACancelledContext(t *testing.T) {
	ctx := t.Context()
	s := stStore(t)
	tenantID, _ := stTenant(t, ctx)

	// Seed a row first: against an empty fingerprint a swallowed cancellation and a genuine
	// empty result are the same value, and the test would pass on the bug.
	stSeedAnchorRule(t, ctx, tenantID, "layout-cancel", "invoice_number", stAnchorRuleValid, extraction.RuleSchemaVersion)
	if got, err := s.AnchorRulesFor(ctx, tenantID, "layout-cancel"); err != nil || len(got) != 1 {
		t.Fatalf("control read returned %d row(s) and error %v, want 1 and nil", len(got), err)
	}

	cancelled, cancel := context.WithCancel(ctx)
	cancel()

	out, err := s.AnchorRulesFor(cancelled, tenantID, "layout-cancel")
	if !errors.Is(err, context.Canceled) {
		t.Errorf("AnchorRulesFor on a cancelled context returned error %v, want context.Canceled", err)
	}
	stAssertEmptyNotNil(t, out, "AnchorRulesFor on a cancelled context")
}

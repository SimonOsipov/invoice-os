// anchor_store_db_test.go: the acceptance specs for AnchorRulesFor. Package extraction_test,
// so it shares store_db_test.go's TestMain, per-role pools and single skip site -- stRequire
// stays this package's only t.Skip.
package extraction_test

import (
	"context"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/SimonOsipov/invoice-os/internal/extraction"
	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
)

// stAnchorRuleValid is the documented rule shape (anchor.go), decodable by ParseRule.
const stAnchorRuleValid = `{"label":"invoice","relation":{"kind":"same_token","max_distance":0},"shape":"invoice_number"}`

// stAnchorRuleUnparseable passes the table's jsonb CHECK (label/relation/shape all present)
// but its label is an unclosed regexp group, which ParseRule rejects.
const stAnchorRuleUnparseable = `{"label":"(unclosed","relation":{"kind":"same_token","max_distance":0},"shape":"invoice_number"}`

// stTiedRules is how many rows share one created_at in the ordering spec.
const stTiedRules = 8

// stSeedAnchorRule inserts one row as superuser: invoice_app holds SELECT only on this table.
func stSeedAnchorRule(t *testing.T, ctx context.Context, tenantID, fingerprint, field, ruleJSON string, version int) (id string) {
	t.Helper()
	id = uuid.NewString()
	if _, err := stRequire(t).super.Exec(ctx,
		`INSERT INTO extraction_anchor_rules (id, tenant_id, layout_fingerprint, field_name, rule, rule_schema_version)
		 VALUES ($1, $2, $3, $4, $5, $6)`,
		id, tenantID, fingerprint, field, ruleJSON, version); err != nil {
		t.Fatalf("seed anchor rule: %v", err)
	}
	return id
}

func TestAnchorRulesFor_ReturnsOnlyTheMatchingFingerprint(t *testing.T) {
	ctx := t.Context()
	s := stStore(t)
	tenantID, _ := stTenant(t, ctx)

	x1 := stSeedAnchorRule(t, ctx, tenantID, "layout-x", "invoice_number", stAnchorRuleValid, extraction.RuleSchemaVersion)
	x2 := stSeedAnchorRule(t, ctx, tenantID, "layout-x", "total_amount", stAnchorRuleValid, extraction.RuleSchemaVersion)
	stSeedAnchorRule(t, ctx, tenantID, "layout-y", "invoice_number", stAnchorRuleValid, extraction.RuleSchemaVersion)

	out, err := s.AnchorRulesFor(ctx, tenantID, "layout-x")
	if err != nil {
		t.Fatalf("AnchorRulesFor: %v", err)
	}
	if len(out) != 2 {
		t.Fatalf("AnchorRulesFor returned %d row(s), want the 2 seeded on layout-x", len(out))
	}
	got := map[string]bool{out[0].ID: true, out[1].ID: true}
	if !got[x1] || !got[x2] {
		t.Errorf("AnchorRulesFor returned ids {%s, %s}, want exactly {%s, %s}", out[0].ID, out[1].ID, x1, x2)
	}
}

func TestAnchorRulesFor_NeverReturnsNil(t *testing.T) {
	ctx := t.Context()
	s := stStore(t)
	tenantID, _ := stTenant(t, ctx)

	out, err := s.AnchorRulesFor(ctx, tenantID, "layout-unseeded")
	if err != nil {
		t.Fatalf("AnchorRulesFor: %v", err)
	}
	if out == nil {
		t.Error("AnchorRulesFor returned a nil slice for a fingerprint with no rows; nil marshals to null, not to an empty array")
	}
	if len(out) != 0 {
		t.Errorf("AnchorRulesFor returned %d row(s) for an unseeded fingerprint", len(out))
	}

	// Mirrors TestExtractionStore_ReadReturnsEmptySliceNotNil: db.WithinTenantTx refuses a
	// non-UUID tenant before the query runs, so the tx helper's own initialiser never fires.
	out, err = s.AnchorRulesFor(ctx, "not-a-uuid", "layout-unseeded")
	if err == nil {
		t.Fatal("AnchorRulesFor accepted a non-UUID tenant and reported no error")
	}
	if out == nil {
		t.Errorf("AnchorRulesFor returned a nil slice alongside its error %v, want an empty slice", err)
	}
	if len(out) != 0 {
		t.Errorf("AnchorRulesFor returned %d row(s) alongside its error", len(out))
	}
}

func TestAnchorRulesFor_OrdersNewestFirstWithATotalTiebreak(t *testing.T) {
	ctx := t.Context()
	s := stStore(t)
	tenantID, _ := stTenant(t, ctx)
	h := stRequire(t)

	older := stSeedAnchorRule(t, ctx, tenantID, "layout-tie", "invoice_number", stAnchorRuleValid, extraction.RuleSchemaVersion)

	// One INSERT, stTiedRules tuples: now() is transaction-scoped, so every row shares one
	// created_at and only the id tiebreak can order them. Eight, not two: with a tied PAIR an
	// ORDER BY that dropped the id agrees with the sorted want by chance half the time, which
	// is no oracle at all -- eight random ids agree once in 8! runs.
	tied := make([]string, stTiedRules)
	for i := range tied {
		tied[i] = uuid.NewString()
	}
	if _, err := h.super.Exec(ctx,
		`INSERT INTO extraction_anchor_rules (id, tenant_id, layout_fingerprint, field_name, rule, rule_schema_version)
		 SELECT u, $2, $3, $4, $5, $6 FROM unnest($1::uuid[]) AS u`,
		tied, tenantID, "layout-tie", "total_amount", stAnchorRuleValid, extraction.RuleSchemaVersion); err != nil {
		t.Fatalf("seed the tied rows: %v", err)
	}
	want := append(slices.Sorted(slices.Values(tied)), older)

	for attempt := 1; attempt <= 2; attempt++ {
		out, err := s.AnchorRulesFor(ctx, tenantID, "layout-tie")
		if err != nil {
			t.Fatalf("AnchorRulesFor attempt %d: %v", attempt, err)
		}
		if len(out) != len(want) {
			t.Fatalf("AnchorRulesFor attempt %d returned %d row(s), want %d", attempt, len(out), len(want))
		}
		for i, w := range want {
			if out[i].ID != w {
				t.Errorf("attempt %d position %d is %s, want %s", attempt, i, out[i].ID, w)
			}
		}
	}
}

func TestAnchorRulesFor_DecodesTheRuleBody(t *testing.T) {
	ctx := t.Context()
	s := stStore(t)
	tenantID, _ := stTenant(t, ctx)

	stSeedAnchorRule(t, ctx, tenantID, "layout-decode", "invoice_number", stAnchorRuleValid, extraction.RuleSchemaVersion)

	out, err := s.AnchorRulesFor(ctx, tenantID, "layout-decode")
	if err != nil {
		t.Fatalf("AnchorRulesFor: %v", err)
	}
	if len(out) != 1 {
		t.Fatalf("AnchorRulesFor returned %d row(s), want 1", len(out))
	}

	// Rule.re is unexported with no accessor; TestAnchorRulesFor_ErrorsOnAnUnparseableRule is
	// what proves ParseRule actually ran.
	got := out[0].Rule
	if got.Label != "invoice" {
		t.Errorf("Rule.Label is %q, want invoice", got.Label)
	}
	if got.Relation.Kind != extraction.RelSameToken {
		t.Errorf("Rule.Relation.Kind is %q, want %q", got.Relation.Kind, extraction.RelSameToken)
	}
	if got.Relation.MaxDistance != 0 {
		t.Errorf("Rule.Relation.MaxDistance is %v, want 0", got.Relation.MaxDistance)
	}
	if got.Shape != extraction.ShapeInvoiceNumber {
		t.Errorf("Rule.Shape is %q, want %q", got.Shape, extraction.ShapeInvoiceNumber)
	}
}

func TestAnchorRulesFor_ErrorsOnAnUnparseableRule(t *testing.T) {
	ctx := t.Context()
	s := stStore(t)
	tenantID, _ := stTenant(t, ctx)

	badID := stSeedAnchorRule(t, ctx, tenantID, "layout-bad-rule", "invoice_number", stAnchorRuleUnparseable, extraction.RuleSchemaVersion)

	out, err := s.AnchorRulesFor(ctx, tenantID, "layout-bad-rule")
	if err == nil {
		t.Fatal("AnchorRulesFor accepted an unparseable rule and reported no error")
	}
	if !strings.Contains(err.Error(), badID) {
		t.Errorf("AnchorRulesFor error %q does not name the offending row id %s", err.Error(), badID)
	}
	if len(out) != 0 {
		t.Errorf("AnchorRulesFor returned %d row(s) alongside its error; a broken row must fail the whole read, not a partial slice", len(out))
	}
}

func TestAnchorRulesFor_ErrorsOnAnUnknownSchemaVersion(t *testing.T) {
	ctx := t.Context()
	s := stStore(t)
	tenantID, _ := stTenant(t, ctx)

	badVersion := extraction.RuleSchemaVersion + 1
	stSeedAnchorRule(t, ctx, tenantID, "layout-bad-version", "invoice_number", stAnchorRuleValid, badVersion)

	out, err := s.AnchorRulesFor(ctx, tenantID, "layout-bad-version")
	if err == nil {
		t.Fatal("AnchorRulesFor accepted a row with an unknown rule_schema_version and reported no error")
	}
	if !strings.Contains(err.Error(), strconv.Itoa(badVersion)) {
		t.Errorf("AnchorRulesFor error %q does not name the offending version %d", err.Error(), badVersion)
	}
	if len(out) != 0 {
		t.Errorf("AnchorRulesFor returned %d row(s) alongside its error", len(out))
	}
}

func TestAnchorRulesFor_CannotReadAnotherTenantsRules(t *testing.T) {
	ctx := t.Context()
	s := stStore(t)
	tenantA, _ := stTenant(t, ctx)
	tenantB, _ := stTenant(t, ctx)

	idA := stSeedAnchorRule(t, ctx, tenantA, "layout-shared", "invoice_number", stAnchorRuleValid, extraction.RuleSchemaVersion)
	stSeedAnchorRule(t, ctx, tenantB, "layout-shared", "invoice_number", stAnchorRuleValid, extraction.RuleSchemaVersion)

	out, err := s.AnchorRulesFor(ctx, tenantA, "layout-shared")
	if err != nil {
		t.Fatalf("AnchorRulesFor: %v", err)
	}
	if len(out) != 1 || out[0].ID != idA {
		t.Errorf("AnchorRulesFor(tenantA, ...) returned %+v, want only tenant A's row %s", out, idA)
	}
}

func TestAnchorRulesFor_UsesTenantTxNotRequestTx(t *testing.T) {
	ctx := t.Context()
	s := stStore(t)
	tenantA, _ := stTenant(t, ctx)
	tenantB, _ := stTenant(t, ctx)

	idA := stSeedAnchorRule(t, ctx, tenantA, "layout-identity", "invoice_number", stAnchorRuleValid, extraction.RuleSchemaVersion)

	// The worker has no request identity: a context carrying tenant B must not steer the read
	// away from the tenantID parameter, mirroring TestRLS_ExtractWorkerBuildsIdentityFromArgsNotContext.
	ctxB := auth.WithIdentity(ctx, auth.Identity{
		Subject: uuid.NewString(), Role: "authenticated", TenantID: tenantB,
	})

	out, err := s.AnchorRulesFor(ctxB, tenantA, "layout-identity")
	if err != nil {
		t.Fatalf("AnchorRulesFor: %v", err)
	}
	if len(out) != 1 || out[0].ID != idA {
		t.Errorf("AnchorRulesFor(ctxB, tenantA, ...) returned %+v, want tenant A's row %s; the parameter must win over ctx's identity", out, idA)
	}
}

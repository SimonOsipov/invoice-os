// anchor_store_db_test.go: the acceptance specs for AnchorRulesFor. Package extraction_test,
// so it shares store_db_test.go's TestMain, per-role pools and single skip site -- stRequire
// stays this package's only t.Skip.
package extraction_test

import (
	"context"
	"errors"
	"fmt"
	"go/ast"
	"go/token"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/SimonOsipov/invoice-os/internal/extraction"
	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
	"github.com/SimonOsipov/invoice-os/internal/platform/db"
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

// R-1 (rewritten for seq DESC): eight rows in one INSERT share created_at exactly, so only
// seq separates them. INSERT ... SELECT unnest(array) assigns nextval in array order, so under
// seq DESC they read back reversed. Eight, not two: a tied PAIR agrees with a wrong order half
// the time.
func TestAnchorRulesFor_OrdersNewestFirstBySeq(t *testing.T) {
	ctx := t.Context()
	s := stStore(t)
	tenantID, _ := stTenant(t, ctx)
	h := stRequire(t)

	older := stSeedAnchorRule(t, ctx, tenantID, "layout-tie", "invoice_number", stAnchorRuleValid, extraction.RuleSchemaVersion)

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

	// The tie is the premise, not a hope: without it seq and created_at could agree by accident
	// and the order below would prove nothing about which column answered.
	first := stAnchorRuleCreatedAt(t, ctx, tied[0])
	for _, id := range tied[1:] {
		if got := stAnchorRuleCreatedAt(t, ctx, id); !got.Equal(first) {
			t.Fatalf("row %s carries created_at %v, want %v -- the %d rows did not tie, so this test cannot tell seq from the clock",
				id, got, first, stTiedRules)
		}
	}

	want := make([]string, 0, stTiedRules+1)
	for i := len(tied) - 1; i >= 0; i-- {
		want = append(want, tied[i])
	}
	want = append(want, older)

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
				t.Errorf("attempt %d position %d is %s, want %s -- seq DESC reverses the insert order", attempt, i, out[i].ID, w)
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

// stAnchorRuleTotal is a rule the corpus actually matches, so a candidate reaching Resolve is
// evidence and not an empty result.
const stAnchorRuleTotal = `{"label":"(?i)\\btotal\\b","relation":{"kind":"same_token","max_distance":0},"shape":"amount"}`

// stCorpusFile is the layout G-11/G-12 fingerprint and resolve. Inline labels, so the shipped
// set reaches every field on it.
const stCorpusFile = "corpus_inline_labels.pdf"

// G-11
func TestResolve_LearnedRulesForAnotherFingerprintAreNeverPassedIn(t *testing.T) {
	ctx := t.Context()
	s := stStore(t)
	tenantID, _ := stTenant(t, ctx)

	pages := rvCorpusPages(t, stCorpusFile)
	fingerprint := extraction.Fingerprint(pages)
	stSeedAnchorRule(t, ctx, tenantID, fingerprint+"-other", "total", stAnchorRuleTotal, extraction.RuleSchemaVersion)

	learned, err := s.AnchorRulesFor(ctx, tenantID, fingerprint)
	if err != nil {
		t.Fatalf("AnchorRulesFor: %v", err)
	}
	if len(learned) != 0 {
		t.Fatalf("AnchorRulesFor returned %d rule(s) for this document's own fingerprint; the seeded rule belongs to another layout", len(learned))
	}

	got := extraction.Resolve(pages, extraction.RuleSet{Learned: learned, Tier1: extraction.Tier1Rules})
	// Not optional: "every candidate is generic" is what a Resolve returning nothing produces.
	rvFloor(t, got, "the shipped set with no learned rule for this fingerprint")
	for _, c := range got {
		if c.Tier != extraction.TierGeneric {
			t.Errorf("candidate %s=%q from rule %q carries %v; no stored rule for this fingerprint reached Resolve", c.Field, c.Value, c.RuleID, c.Tier)
		}
	}
}

// G-12
func TestResolve_LearnedRuleFromTheStoreReachesResolution(t *testing.T) {
	ctx := t.Context()
	s := stStore(t)
	tenantID, _ := stTenant(t, ctx)

	pages := rvCorpusPages(t, stCorpusFile)
	fingerprint := extraction.Fingerprint(pages)
	id := stSeedAnchorRule(t, ctx, tenantID, fingerprint, "total", stAnchorRuleTotal, extraction.RuleSchemaVersion)

	learned, err := s.AnchorRulesFor(ctx, tenantID, fingerprint)
	if err != nil {
		t.Fatalf("AnchorRulesFor: %v", err)
	}
	if len(learned) != 1 {
		t.Fatalf("AnchorRulesFor returned %d rule(s) for the seeded fingerprint, want 1", len(learned))
	}

	got := rvFor(extraction.Resolve(pages, extraction.RuleSet{Learned: learned, Tier1: extraction.Tier1Rules}), "total")
	rvFloor(t, got, "a stored rule for this document's own fingerprint")
	seeded := false
	for _, c := range got {
		if c.Tier == extraction.TierLearned && c.RuleID == id {
			seeded = true
		}
	}
	if !seeded {
		t.Errorf("no total candidate carries TierLearned with the seeded id %s: %+v", id, got)
	}

	// Paired control: the same page with nothing learned. Without it a Resolve that stamps
	// every candidate TierLearned passes the assertion above.
	ctl := rvFor(extraction.Resolve(pages, extraction.RuleSet{Tier1: extraction.Tier1Rules}), "total")
	rvControl(t, ctl, "the same page with Learned nil")
	for _, c := range ctl {
		if c.Tier != extraction.TierGeneric {
			t.Errorf("with Learned nil, total candidate %q from rule %q carries %v", c.Value, c.RuleID, c.Tier)
		}
	}
}

// --- EXTR-14-05: the writer, and a recency order that is not the clock ------------------

const (
	arStoreSource = "anchor_store.go"
	arIndex       = "extraction_anchor_rules_tenant_fingerprint_seq_idx"
)

// arLearn derives one rule the way EXTR-14-06 will. LearnRule is LearnedRule's only
// constructor, so a body ParseRule would reject is unrepresentable at the writer's call site.
func arLearn(t *testing.T, field, anchorText string) extraction.LearnedRule {
	t.Helper()
	anchor := rvAnchor("total", anchorText, 0.10, 0.10, 0.20, 0.13)
	region := extraction.Region{Page: 1, X0: 0.25, Y0: 0.10, X1: 0.35, Y1: 0.13} // gap 0.05, a right relation
	lr, ok := extraction.LearnRule(field, region, []extraction.AnchorObservation{anchor})
	if !ok {
		t.Fatalf("LearnRule(%s, %q) refused; the fixture derives no rule and every assertion over it is vacuous", field, anchorText)
	}
	return lr
}

// arAppendAll writes every rule through appendAnchorRuleTx inside ONE tenant transaction, so
// created_at (now(), transaction-scoped) ties across all of them and only seq separates them.
func arAppendAll(t *testing.T, ctx context.Context, tenantID, fingerprint string, lrs ...extraction.LearnedRule) []string {
	t.Helper()
	ids := make([]string, 0, len(lrs))
	if err := db.WithinTenantTx(ctx, stRequire(t).app, tenantID, func(tx pgx.Tx) error {
		for _, lr := range lrs {
			id, err := extraction.AppendAnchorRuleForTest(ctx, tx, tenantID, fingerprint, lr)
			if err != nil {
				return err
			}
			ids = append(ids, id)
		}
		return nil
	}); err != nil {
		t.Fatalf("append %d rule(s) for %s: %v", len(lrs), fingerprint, err)
	}
	return ids
}

// arLayoutAs opens the session as sessionTenant and passes paramTenant to jobLayoutTx. The two
// differ in exactly one arm of S-08, where only RLS -- never the WHERE clause -- can hide the row.
func arLayoutAs(t *testing.T, ctx context.Context, sessionTenant, paramTenant, jobID string) (extraction.JobLayout, bool, error) {
	t.Helper()
	var (
		out extraction.JobLayout
		ok  bool
	)
	err := db.WithinTenantTx(ctx, stRequire(t).app, sessionTenant, func(tx pgx.Tx) error {
		var e error
		out, ok, e = extraction.JobLayoutForTest(ctx, tx, paramTenant, jobID)
		return e
	})
	return out, ok, err
}

func arLayout(t *testing.T, ctx context.Context, tenantID, jobID string) (extraction.JobLayout, bool, error) {
	t.Helper()
	return arLayoutAs(t, ctx, tenantID, tenantID, jobID)
}

// arSetLayout writes a job's two layout columns as superuser. anchors is bound as a string so
// pgx sends raw JSON to jsonb; a nil anchors leaves the column NULL, which is S-07b's fixture.
func arSetLayout(t *testing.T, ctx context.Context, jobID, fingerprint string, anchors []byte) {
	t.Helper()
	var arg any
	if anchors != nil {
		arg = string(anchors)
	}
	ct, err := stRequire(t).super.Exec(ctx,
		`UPDATE extraction_jobs SET layout_fingerprint = $2, layout_anchors = $3 WHERE id = $1`,
		jobID, fingerprint, arg)
	if err != nil {
		t.Fatalf("set the layout on job %s: %v", jobID, err)
	}
	if ct.RowsAffected() != 1 {
		t.Fatalf("setting the layout on job %s touched %d row(s), want 1", jobID, ct.RowsAffected())
	}
}

// arIDs projects the read's slice order, which is the only place recency lives -- AnchorRule
// carries no Seq field.
func arIDs(out []extraction.AnchorRule) []string {
	ids := make([]string, len(out))
	for i, r := range out {
		ids[i] = r.ID
	}
	return ids
}

// S-01 / AC-1
func TestAppendAnchorRule_ReadsBackUnderItsOwnFingerprintAndNoOther(t *testing.T) {
	ctx := t.Context()
	s := stStore(t)
	tenantID, _ := stTenant(t, ctx)

	const fp = "v1:ar-s01"
	id := arAppendAll(t, ctx, tenantID, fp, arLearn(t, "total", "Total"))[0]
	if id == "" {
		t.Fatal("appendAnchorRuleTx returned an empty id; EXTR-14-07's audit payload carries it")
	}

	out, err := s.AnchorRulesFor(ctx, tenantID, fp)
	if err != nil {
		t.Fatalf("AnchorRulesFor(%s): %v", fp, err)
	}
	if len(out) != 1 || out[0].ID != id {
		t.Fatalf("AnchorRulesFor(%s) returned %v, want exactly [%s]", fp, arIDs(out), id)
	}
	if out[0].Field != "total" {
		t.Errorf("the written rule reads back with field %q, want total", out[0].Field)
	}

	// The negative half, against the same fixture: one character of fingerprint difference.
	other, err := s.AnchorRulesFor(ctx, tenantID, fp+"x")
	if err != nil {
		t.Fatalf("AnchorRulesFor(%sx): %v", fp, err)
	}
	if len(other) != 0 {
		t.Errorf("AnchorRulesFor(%sx) returned %v, want none -- a rule belongs to one fingerprint", fp, arIDs(other))
	}
}

// S-02 / AC-2. created_at defaults to now() and ties inside one transaction, so a tie alone
// cannot red an ORDER BY that reinstates it. Here the two orders DISAGREE: created_at is seeded
// backwards as superuser, so created_at DESC yields the exact reverse of seq DESC.
func TestAnchorRulesFor_OrdersBySeqWhenCreatedAtDisagrees(t *testing.T) {
	ctx := t.Context()
	s := stStore(t)
	tenantID, _ := stTenant(t, ctx)
	h := stRequire(t)

	const fp = "v1:ar-s02"
	ids := arAppendAll(t, ctx, tenantID, fp,
		arLearn(t, "total", "Total"),
		arLearn(t, "subtotal", "Sub-total"),
		arLearn(t, "vat", "VAT"),
	)

	// Backwards: the FIRST-written row (lowest seq) gets the LATEST created_at.
	base := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	for i, id := range ids {
		at := base.Add(time.Duration(len(ids)-i) * time.Hour)
		if _, err := h.super.Exec(ctx,
			`UPDATE extraction_anchor_rules SET created_at = $2 WHERE id = $1`, id, at); err != nil {
			t.Fatalf("seed created_at on %s: %v", id, err)
		}
	}

	// What the superseded ORDER BY created_at DESC, id would return. Asserting it DIFFERS is
	// what makes the read below evidence about seq rather than a coincidence.
	byClock := []string{}
	rows, err := h.super.Query(ctx,
		`SELECT id::text FROM extraction_anchor_rules
		  WHERE tenant_id = $1 AND layout_fingerprint = $2
		  ORDER BY created_at DESC, id`, tenantID, fp)
	if err != nil {
		t.Fatalf("read the clock order: %v", err)
	}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			t.Fatalf("scan the clock order: %v", err)
		}
		byClock = append(byClock, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		t.Fatalf("read the clock order: %v", err)
	}

	want := []string{ids[2], ids[1], ids[0]}
	if slices.Equal(byClock, want) {
		t.Fatalf("the clock order %v equals the seq order %v, so the fixture cannot discriminate", byClock, want)
	}

	out, err := s.AnchorRulesFor(ctx, tenantID, fp)
	if err != nil {
		t.Fatalf("AnchorRulesFor: %v", err)
	}
	if got := arIDs(out); !slices.Equal(got, want) {
		t.Errorf("AnchorRulesFor returned %v, want %v (seq DESC); the clock order is %v", got, want, byClock)
	}
}

// S-03 / AC-2: the order is a guarantee, not one plan's accident.
func TestAnchorRulesFor_TheSeqOrderIsStableAcrossRepeatedReads(t *testing.T) {
	ctx := t.Context()
	s := stStore(t)
	tenantID, _ := stTenant(t, ctx)

	const fp = "v1:ar-s03"
	ids := arAppendAll(t, ctx, tenantID, fp,
		arLearn(t, "total", "Total"),
		arLearn(t, "subtotal", "Sub-total"),
		arLearn(t, "vat", "VAT"),
		arLearn(t, "currency", "Currency"),
		arLearn(t, "buyer_name", "Buyer"),
	)
	want := make([]string, 0, len(ids))
	for i := len(ids) - 1; i >= 0; i-- {
		want = append(want, ids[i])
	}

	const reads = 10
	for i := 1; i <= reads; i++ {
		out, err := s.AnchorRulesFor(ctx, tenantID, fp)
		if err != nil {
			t.Fatalf("AnchorRulesFor read %d: %v", i, err)
		}
		if got := arIDs(out); !slices.Equal(got, want) {
			t.Fatalf("read %d returned %v, want %v", i, got, want)
		}
	}
}

// S-09 / AC-1: twenty rules the way LearnRule makes them, every one read back and decoded.
func TestAppendAnchorRule_ReadsBackAWholeLearnedCorpus(t *testing.T) {
	ctx := t.Context()
	s := stStore(t)
	tenantID, _ := stTenant(t, ctx)

	const fp = "v1:ar-s09"
	var lrs []extraction.LearnedRule
	for _, text := range []string{"Total", "Amount Due"} {
		for _, field := range extraction.HeaderFields {
			lrs = append(lrs, arLearn(t, field, text))
		}
	}
	if len(lrs) != 20 {
		t.Fatalf("the corpus holds %d rule(s), want 20", len(lrs))
	}
	ids := arAppendAll(t, ctx, tenantID, fp, lrs...)

	out, err := s.AnchorRulesFor(ctx, tenantID, fp)
	if err != nil {
		t.Fatalf("AnchorRulesFor over the learned corpus: %v", err)
	}
	if len(out) != len(lrs) {
		t.Fatalf("AnchorRulesFor returned %d rule(s), want all %d", len(out), len(lrs))
	}
	seen := map[string]bool{}
	for _, r := range out {
		seen[r.ID] = true
		if r.Rule.Label == "" {
			t.Errorf("rule %s decoded an empty label; ParseRule ran over a body the writer mangled", r.ID)
		}
	}
	for i, id := range ids {
		if !seen[id] {
			t.Errorf("rule %d (%s) was written but never read back", i, id)
		}
	}
}

// arAnchorRulesSQL is anchorRulesForTx's own SELECT, read from the source. A copy typed here
// would drift from the query the plan below is asserted about.
func arAnchorRulesSQL(t *testing.T) string {
	t.Helper()
	f, _ := mxParse(t, arStoreSource)

	var sql string
	for _, d := range f.Decls {
		fn, ok := d.(*ast.FuncDecl)
		if !ok || fn.Name.Name != "anchorRulesForTx" {
			continue
		}
		ast.Inspect(fn, func(n ast.Node) bool {
			bl, ok := n.(*ast.BasicLit)
			if ok && bl.Kind == token.STRING && strings.Contains(bl.Value, "FROM extraction_anchor_rules") {
				sql = strings.Trim(bl.Value, "`")
			}
			return true
		})
	}
	if sql == "" {
		t.Fatalf("%s: anchorRulesForTx issues no SELECT over extraction_anchor_rules, so this test has lost its subject", arStoreSource)
	}
	return sql
}

// S-10 / AC-2. Both GUCs are load-bearing: with only enable_seqscan = off the planner picks a
// Bitmap Index Scan, which collects tuples in heap order and so carries a Sort whatever the
// index looks like -- the "no Sort" claim would then be unassertable. The Index Scan is asserted
// FIRST, or "no Sort" also passes on a plan that reached neither node.
func TestAnchorRulesFor_OrdersFromTheIndexWithoutASort(t *testing.T) {
	ctx := t.Context()
	tenantID, _ := stTenant(t, ctx)
	h := stRequire(t)

	// Thirty rows over three fingerprints, then ANALYZE: on three rows and stale statistics the
	// planner reaches for extraction_anchor_rules_tenant_id_id_uq and sorts, whatever the GUCs
	// say, and the assertion below would be about the fixture rather than about the index.
	const fp = "v1:ar-s10"
	arAppendAll(t, ctx, tenantID, fp,
		arLearn(t, "total", "Total"),
		arLearn(t, "subtotal", "Sub-total"),
		arLearn(t, "vat", "VAT"),
	)
	for i := range 27 {
		stSeedAnchorRule(t, ctx, tenantID, fmt.Sprintf("%s-%d", fp, i%3), "total_amount",
			stAnchorRuleValid, extraction.RuleSchemaVersion)
	}
	if _, err := h.super.Exec(ctx, `ANALYZE extraction_anchor_rules`); err != nil {
		t.Fatalf("analyze extraction_anchor_rules: %v", err)
	}

	var plan strings.Builder
	if err := db.WithinTenantTx(ctx, h.app, tenantID, func(tx pgx.Tx) error {
		for _, guc := range []string{`SET LOCAL enable_seqscan = off`, `SET LOCAL enable_bitmapscan = off`} {
			if _, err := tx.Exec(ctx, guc); err != nil {
				return err
			}
		}
		rows, err := tx.Query(ctx, "EXPLAIN (COSTS OFF) "+arAnchorRulesSQL(t), tenantID, fp)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var line string
			if err := rows.Scan(&line); err != nil {
				return err
			}
			plan.WriteString(line + "\n")
		}
		return rows.Err()
	}); err != nil {
		t.Fatalf("explain the anchor-rule read: %v", err)
	}

	if !strings.Contains(plan.String(), "Index Scan using "+arIndex) {
		t.Fatalf("the plan does not scan %s, so the assertion below examines nothing:\n%s", arIndex, plan.String())
	}
	if strings.Contains(plan.String(), "Sort") {
		t.Errorf("the plan sorts, so %s no longer answers ORDER BY seq DESC on its own:\n%s", arIndex, plan.String())
	}
}

// S-12 / AC-2. seq is a bigserial, not GENERATED ALWAYS, so a caller-supplied seq is accepted by
// the database. AnchorRulesFor's ORDER BY carries no tiebreak, and this is what licenses that:
// only a writer that never names seq leaves the sequence a total order.
func TestExtractionAnchorStore_TheInsertNeverNamesSeq(t *testing.T) {
	f, fset := mxParse(t, arStoreSource)

	var checked int
	ast.Inspect(f, func(n ast.Node) bool {
		bl, ok := n.(*ast.BasicLit)
		if !ok || bl.Kind != token.STRING || !strings.Contains(bl.Value, "INSERT INTO extraction_anchor_rules") {
			return true
		}
		checked++
		cols, _, _ := strings.Cut(bl.Value, "VALUES")
		if strings.Contains(cols, "seq") {
			t.Errorf("%s: the INSERT names seq in its column list; a hand-supplied seq breaks the total order AnchorRulesFor reads",
				fset.Position(bl.Pos()))
		}
		return true
	})
	if checked == 0 {
		t.Fatalf("%s issues no INSERT INTO extraction_anchor_rules, so this scan examined nothing", arStoreSource)
	}
}

// S-07 / AC-4. csJob seeds a job with both layout columns NULL -- a job written before
// Migration A, and this fixture for free. The paired arm is not optional: without it a
// jobLayoutTx that always answers false passes the first half.
func TestJobLayout_AJobWithNoLayoutReadsAsAbsent(t *testing.T) {
	ctx := t.Context()
	tenantID, jobID := csJob(t, ctx)

	got, ok, err := arLayout(t, ctx, tenantID, jobID)
	if err != nil {
		t.Fatalf("jobLayoutTx over a job with NULL layout columns returned error %v, want none", err)
	}
	if ok {
		t.Errorf("jobLayoutTx reported ok = true for a job with NULL layout columns: %+v", got)
	}

	// Paired arm, same job: once the columns are set the same call finds it.
	anchors, err := extraction.MarshalAnchorObservations([]extraction.AnchorObservation{
		{Label: "total", Text: "Total", Page: 1, Band: 0, X0: 0.1, Y0: 0.1, X1: 0.2, Y1: 0.13},
	})
	if err != nil {
		t.Fatalf("marshal the anchors: %v", err)
	}
	const fp = "v1:ar-s07"
	arSetLayout(t, ctx, jobID, fp, anchors)

	got, ok, err = arLayout(t, ctx, tenantID, jobID)
	if err != nil {
		t.Fatalf("jobLayoutTx over the same job with its layout set: %v", err)
	}
	if !ok {
		t.Fatalf("jobLayoutTx reported ok = false for a job whose layout columns are set")
	}
	if got.Fingerprint != fp {
		t.Errorf("jobLayoutTx returned fingerprint %q, want %q", got.Fingerprint, fp)
	}
	if len(got.Anchors) != 1 || got.Anchors[0].Label != "total" {
		t.Errorf("jobLayoutTx returned anchors %+v, want the one seeded observation", got.Anchors)
	}
}

// S-07b / AC-4. Migration A made the two columns independently nullable, so a set fingerprint
// beside a NULL layout_anchors is representable. UnmarshalAnchorObservations(nil) errors
// ("want a JSON array"), so without a guard such a row becomes an error rather than a layout.
func TestJobLayout_AFingerprintWithNullAnchorsIsAnEmptyListNotAnError(t *testing.T) {
	ctx := t.Context()
	tenantID, jobID := csJob(t, ctx)

	const fp = "v1:ar-s07b"
	arSetLayout(t, ctx, jobID, fp, nil)

	got, ok, err := arLayout(t, ctx, tenantID, jobID)
	if err != nil {
		t.Fatalf("jobLayoutTx over a set fingerprint with NULL layout_anchors returned error %v, want none", err)
	}
	if !ok {
		t.Fatalf("jobLayoutTx reported ok = false for a job carrying a fingerprint")
	}
	if got.Fingerprint != fp {
		t.Errorf("jobLayoutTx returned fingerprint %q, want %q", got.Fingerprint, fp)
	}
	if got.Anchors == nil {
		t.Errorf("jobLayoutTx returned a nil Anchors slice; the type's contract is an empty list")
	}
	if len(got.Anchors) != 0 {
		t.Errorf("jobLayoutTx returned %d anchor(s) for a NULL column", len(got.Anchors))
	}
}

// S-08 / AC-5. Tenant A's job is seeded with its layout columns SET: with them NULL the read
// would answer absent whether RLS hid the row or the row simply had no layout, and would prove
// nothing about isolation.
func TestJobLayout_AnotherTenantsJobReadsAsAbsentNotAsAnError(t *testing.T) {
	ctx := t.Context()
	tenantA, jobA := csJob(t, ctx)
	tenantB, _ := csJob(t, ctx)

	anchors, err := extraction.MarshalAnchorObservations([]extraction.AnchorObservation{})
	if err != nil {
		t.Fatalf("marshal the anchors: %v", err)
	}
	const fp = "v1:ar-s08"
	arSetLayout(t, ctx, jobA, fp, anchors)

	// Positive arm first: the row exists and is readable by its owner, so "absent" below is
	// the policy and not a missing fixture.
	got, ok, err := arLayout(t, ctx, tenantA, jobA)
	if err != nil {
		t.Fatalf("jobLayoutTx as the owning tenant: %v", err)
	}
	if !ok || got.Fingerprint != fp {
		t.Fatalf("jobLayoutTx as the owning tenant returned ok = %v, fingerprint %q, want true and %q", ok, got.Fingerprint, fp)
	}

	// The production shape: tenant B asks for its own tenant's job by A's id.
	got, ok, err = arLayout(t, ctx, tenantB, jobA)
	if err != nil {
		t.Errorf("jobLayoutTx as tenant B returned error %v, want none -- another tenant's job is absent, not an error", err)
	}
	if ok {
		t.Errorf("jobLayoutTx as tenant B reported ok = true for tenant A's job: %+v", got)
	}

	// The discriminator: tenant B's session, tenant A's id in the WHERE. The predicate matches
	// the row, so only RLS can remove it -- with the policy off this arm is the one that reds.
	got, ok, err = arLayoutAs(t, ctx, tenantB, tenantA, jobA)
	if err != nil {
		t.Errorf("jobLayoutTx in tenant B's session naming tenant A returned error %v, want none", err)
	}
	if ok {
		t.Errorf("jobLayoutTx in tenant B's session read tenant A's job: %+v", got)
	}
}

// S-08b / AC-5. Only pgx.ErrNoRows becomes ok = false. A malformed jobID is SQLSTATE 22P02 and
// must stay an error; a blanket err != nil -> ok = false would swallow it, and a dead
// connection with it.
func TestJobLayout_AMalformedJobIdIsAnErrorNotAnAbsence(t *testing.T) {
	ctx := t.Context()
	tenantID, jobID := csJob(t, ctx)

	// Control: a well-formed id in the same session is read without error.
	arSetLayout(t, ctx, jobID, "v1:ar-s08b", nil)
	if _, ok, err := arLayout(t, ctx, tenantID, jobID); err != nil || !ok {
		t.Fatalf("the control read returned ok = %v, err = %v, want true and nil", ok, err)
	}

	got, ok, err := arLayout(t, ctx, tenantID, "not-a-uuid")
	if err == nil {
		t.Fatalf("jobLayoutTx accepted a malformed job id and reported no error (ok = %v, %+v)", ok, got)
	}
	if errors.Is(err, pgx.ErrNoRows) {
		t.Errorf("jobLayoutTx surfaced a malformed job id as pgx.ErrNoRows: %v", err)
	}
	if ok {
		t.Errorf("jobLayoutTx reported ok = true alongside its error %v", err)
	}
}

// S-11: the anchorless fingerprint. Every document with no page-1 lexicon hit hashes to
// sha256(""), so one tenant's anchorless documents share a bucket. The collision is real and is
// asserted, not denied; containment comes from Resolve, which matches the rule's label against
// raw token text and never consults anchors.
func TestAnchorRulesFor_TheAnchorlessFingerprintIsSharedButResolvesToNothing(t *testing.T) {
	ctx := t.Context()
	s := stStore(t)
	tenantID, _ := stTenant(t, ctx)

	pagesA := rvPage(rvTok("Widgets", 0.10, 0.10, 0.30, 0.13), rvTok("12.00", 0.40, 0.10, 0.50, 0.13))
	pagesB := rvPage(rvTok("Sprockets", 0.10, 0.10, 0.30, 0.13), rvTok("88.40", 0.40, 0.10, 0.50, 0.13))

	// Arm 1: the collision, named. Both documents are genuinely anchorless first, or the
	// equality below would be about something else.
	for what, pages := range map[string][]extraction.TokenPage{"A": pagesA, "B": pagesB} {
		if obs := extraction.AnchorObservations(pages); len(obs) != 0 {
			t.Fatalf("document %s carries %d anchor observation(s) (%+v); it is not the anchorless case", what, len(obs), obs)
		}
	}
	fpA, fpB := extraction.Fingerprint(pagesA), extraction.Fingerprint(pagesB)
	if fpA != fpB {
		t.Fatalf("two unrelated anchorless documents fingerprint to %q and %q; the shared bucket this design rests on is gone", fpA, fpB)
	}

	// Arm 2: the read does NOT isolate. A rule learned on document A is handed to document B.
	id := arAppendAll(t, ctx, tenantID, fpA, arLearn(t, "total", "Buyer"))[0]
	learned, err := s.AnchorRulesFor(ctx, tenantID, fpB)
	if err != nil {
		t.Fatalf("AnchorRulesFor(anchorless fingerprint): %v", err)
	}
	if len(learned) != 1 || learned[0].ID != id {
		t.Fatalf("AnchorRulesFor for document B returned %v, want [%s] -- the shared bucket is the point, not a bug", arIDs(learned), id)
	}

	// Arm 2 continued: Resolve yields nothing from it against document B.
	for _, c := range extraction.Resolve(pagesB, extraction.RuleSet{Learned: learned}) {
		if c.RuleID == id {
			t.Errorf("document B produced candidate %s=%q from the rule learned on document A", c.Field, c.Value)
		}
	}

	// Arm 3, the control: without it arm 2 passes on a Resolve that returns nothing for any
	// input. The same rule against a page carrying the label DOES fire.
	// Two tokens, at the geometry arLearn derived: LearnRule inverts relatedTokens, so the
	// label token and the value token stand in the same right relation at the same gap.
	control := rvPage(rvTok("Buyer", 0.10, 0.10, 0.20, 0.13), rvTok("99.00", 0.25, 0.10, 0.35, 0.13))
	ctl := rvFor(extraction.Resolve(control, extraction.RuleSet{Learned: learned}), "total")
	rvControl(t, ctl, "the same learned rule against a page whose token carries its label")
	fired := false
	for _, c := range ctl {
		if c.Tier == extraction.TierLearned && c.RuleID == id {
			fired = true
		}
	}
	if !fired {
		t.Errorf("no total candidate carries TierLearned with rule %s: %+v", id, ctl)
	}
}

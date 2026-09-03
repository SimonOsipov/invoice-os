// learn_corpus_db_test.go: EXTR-14-09. The learning chain end to end over real PDF bytes --
// a document is read, a reviewer points at a box, the server derives a rule, the rule is stored
// under that layout's fingerprint, and a SECOND job over the same layout resolves a field
// Tier-1 cannot reach at all.
//
// What this file does NOT prove: nothing in production calls Resolve, so the chain stops one
// rung short of a deployed document read. That rung is EXTR-17's Core AC 1. No deployed or
// browser oracle belongs here (D-4, P-23).
//
// Shares store_db_test.go's TestMain, per-role pools and SINGLE skip site (stRequire), so it
// adds no second skip site -- scripts/ci/rls-test-gate.sh fails the step on any skip and
// ci.yml runs this package with no -run filter. It also reuses handlers_correction_db_test.go's
// cx* harness and handlers_correction_learn_db_test.go's cl* helpers rather than re-typing them.
// E-01 is the one spec here that touches no database; it is the "before" the rest read against.
//
// Helpers use an lc* prefix.
package extraction_test

import (
	"context"
	"net/http"
	"testing"

	"github.com/SimonOsipov/invoice-os/internal/extraction"
)

const (
	// The layout the whole chain rides is fxLearnedTwoParty, deliberately outside corpusPrefix
	// -- see fxBuildLearnedTwoParty and docs/extraction-corpus.md, "## Learned rules". The two
	// corpus layouts below carry the breadth arm and the documented regression.
	lcSplit  = "corpus_split_labels.pdf"
	lcTwoCol = "corpus_two_column.pdf"
	lcField  = "buyer_tin"

	// The two bare TIN tokens on learned_two_party.pdf. Measured: Tier-1 alone reaches
	// NEITHER as a buyer_tin, and the buyer's is what a reviewer points at.
	lcBuyerTIN    = "99999999-0702"
	lcSupplierTIN = "99999999-0701"

	// The bodies LearnRule derives from those two boxes, verbatim. Pinned here rather than
	// recomputed, so a change in the derivation reds rather than moves with it.
	lcBuyerRuleBody    = `{"label":"(?i)\\bBuyer\\b","relation":{"kind":"below","max_distance":0.03},"shape":"tin"}`
	lcSupplierRuleBody = `{"label":"(?i)\\bSupplier\\b","relation":{"kind":"below","max_distance":0.03},"shape":"tin"}`

	// Measured gaps: "Buyer" clears the buyer TIN by 0.026525, "Supplier" the supplier TIN by
	// 0.026631. Both round UP to the 0.03 dial the bodies above carry.
	lcBuyerDistance    = 0.02652524697660197
	lcSupplierDistance = 0.026631364918718453
)

// Tier-1's own read of learned_two_party.pdf, measured off the emitted bytes. E-01 asserts the
// whole row set, so an unread page cannot pass as "buyer_tin is missing".
var lcTier1Decided = []struct {
	field, value string
	reason       extraction.Reason
}{
	{"invoice_number", "INV-1007", extraction.ReasonNone},
	{"issue_date", "2026-04-22", extraction.ReasonNone},
	{"supplier_tin", "99999999-0701", extraction.ReasonAmbiguous},
	{"supplier_name", "Adeyemi Trading Limited", extraction.ReasonNone},
	{"buyer_name", "Honeywell Group", extraction.ReasonNone},
	{"total", "3225.00", extraction.ReasonNone},
}

// lcTier1Candidates is what Resolve returns over the whole page under Tier1Rules alone. The
// floor under E-01's zero: nine is not zero, so "no buyer_tin" is a gap in the rule set and not
// a page the reader failed on.
const lcTier1Candidates = 9

// --- harness ----------------------------------------------------------------

// lcLayout stamps a NAMED fixture's layout onto an existing job as the superuser, in the bytes
// the worker writes. clLayout hard-codes corpus_two_column.pdf and cannot serve here.
func lcLayout(t *testing.T, ctx context.Context, jobID, fixture string) (string, []extraction.TokenPage) {
	t.Helper()
	pages := rvCorpusPages(t, fixture)
	raw, err := extraction.MarshalAnchorObservations(extraction.AnchorObservations(pages))
	if err != nil {
		t.Fatalf("marshal the %s anchors: %v", fixture, err)
	}
	fp := extraction.Fingerprint(pages)
	clLayoutRaw(t, ctx, jobID, fp, raw)
	return fp, pages
}

// lcRead is one document read: the tenant's stored rules for a fingerprint, the candidates they
// and Tier-1 produce for one field, and the reconciled answer.
type lcRead struct {
	rules   []extraction.AnchorRule
	cands   []extraction.Candidate
	decided extraction.FieldResult
	all     int
}

// lcResolve is THE document-N read path, called by every arm that claims to run it. E-04
// inverts E-03 by calling this same helper: a control that runs a different query proves
// nothing about the assertion it claims to invert.
func lcResolve(t *testing.T, ctx context.Context, tenantID, fingerprint, field string, pages []extraction.TokenPage) lcRead {
	t.Helper()

	learned, err := stStore(t).AnchorRulesFor(ctx, tenantID, fingerprint)
	if err != nil {
		t.Fatalf("AnchorRulesFor(tenant %s, %s): %v", tenantID, fingerprint, err)
	}
	all := extraction.Resolve(pages, extraction.RuleSet{Learned: learned, Tier1: extraction.Tier1Rules})
	return lcRead{
		rules:   learned,
		cands:   rvFor(all, field),
		decided: lcDecided(t, extraction.Reconcile(extraction.Input{Candidates: all}), field),
		all:     len(all),
	}
}

// lcDecided is one field's FieldResult. Reconcile is total over HeaderFields, so an absent name
// is a defect and not an empty answer.
func lcDecided(t *testing.T, results []extraction.FieldResult, field string) extraction.FieldResult {
	t.Helper()
	for _, fr := range results {
		if fr.Name == field {
			return fr
		}
	}
	t.Fatalf("Reconcile returned no row for %q; it is total over HeaderFields", field)
	return extraction.FieldResult{}
}

func lcValue(fr extraction.FieldResult) string {
	if fr.Value == nil {
		return "<nil>"
	}
	return *fr.Value
}

func lcAltValues(fr extraction.FieldResult) []string {
	out := []string{}
	for _, a := range fr.Alternatives {
		if a.Value == nil {
			out = append(out, "<nil>")
			continue
		}
		out = append(out, *a.Value)
	}
	return out
}

// lcRuleBodyIs compares a stored row against a body by JSONB equality, not by string equality:
// the column is jsonb and re-serialises with its own key order and spacing.
func lcRuleBodyIs(t *testing.T, ctx context.Context, ruleID, wantBody string) {
	t.Helper()
	var same bool
	if err := stRequire(t).super.QueryRow(ctx,
		`SELECT rule = $1::jsonb FROM extraction_anchor_rules WHERE id = $2::uuid`,
		wantBody, ruleID).Scan(&same); err != nil {
		t.Fatalf("compare the stored rule %s against %s: %v", ruleID, wantBody, err)
	}
	if !same {
		var got string
		if err := stRequire(t).super.QueryRow(ctx,
			`SELECT rule::text FROM extraction_anchor_rules WHERE id = $1::uuid`, ruleID).Scan(&got); err != nil {
			t.Fatalf("read the stored rule %s: %v", ruleID, err)
		}
		t.Errorf("stored rule %s = %s, want %s -- the derivation moved", ruleID, got, wantBody)
	}
}

// lcDeleteRule removes one rule row as the SUPERUSER: invoice_app holds INSERT and SELECT and
// no DELETE (the table is append-only by GRANT, D-7). A 0-row delete would make E-04 vacuous,
// so the command tag is asserted.
func lcDeleteRule(t *testing.T, ctx context.Context, ruleID string) {
	t.Helper()
	tag, err := stRequire(t).super.Exec(ctx,
		`DELETE FROM extraction_anchor_rules WHERE id = $1::uuid`, ruleID)
	if err != nil {
		t.Fatalf("delete anchor rule %s: %v", ruleID, err)
	}
	if n := tag.RowsAffected(); n != 1 {
		t.Fatalf("deleting anchor rule %s removed %d row(s), want exactly 1 -- the re-read below would be vacuous", ruleID, n)
	}
}

// lcPost drives one pointed correction at a token's own box and requires a 201.
func lcPost(t *testing.T, f clFixture, arm, field, value string, region extraction.Region) {
	t.Helper()
	w := cxServe(t, f.reqCtx, f.jobID, field, clPointedBody(value, region, ""),
		cxApplier(false, nil), cxAuditor(nil))
	if w.Code != http.StatusCreated {
		t.Fatalf("%s: status = %d, want %d (body=%q)", arm, w.Code, http.StatusCreated, w.Body.String())
	}
}

// --- E-01 / AC #1: Tier-1 alone reaches nothing -------------------------------------------

// The "before". No database: this is the shipped rule set over the committed bytes, and the
// paired positive control is in the same read -- six other fields DO decide, so the zero below
// is a gap in Tier-1 and not a page nothing was read from.
func TestLearnedTwoParty_Tier1AloneReachesNoBuyerTIN(t *testing.T) {
	pages := rvCorpusPages(t, fxLearnedTwoParty)

	all := extraction.Resolve(pages, extraction.RuleSet{Tier1: extraction.Tier1Rules})
	rvFloor(t, all, "the shipped Tier-1 set over "+fxLearnedTwoParty)
	if len(all) != lcTier1Candidates {
		t.Errorf("Tier-1 alone produced %d candidate(s) over %s, want %d -- the zero asserted below only means something against a page the reader DID read",
			len(all), fxLearnedTwoParty, lcTier1Candidates)
	}
	if got := rvFor(all, lcField); len(got) != 0 {
		t.Errorf("Tier-1 alone produced %d %s candidate(s) %v, want 0 -- the buyer sweep is banded to page 1's BOTTOM half and both TINs sit in the top",
			len(got), lcField, rvValues(got))
	}

	results := extraction.Reconcile(extraction.Input{Candidates: all})
	buyer := lcDecided(t, results, lcField)
	if buyer.Value != nil || buyer.Reason != extraction.ReasonMissing {
		t.Errorf("Tier-1 alone decided %s = %s reason %q, want <nil> and %q",
			lcField, lcValue(buyer), buyer.Reason, extraction.ReasonMissing)
	}

	// The positive control, on the SAME read: every other measured field still lands.
	for _, want := range lcTier1Decided {
		got := lcDecided(t, results, want.field)
		if lcValue(got) != want.value || got.Reason != want.reason {
			t.Errorf("Tier-1 alone decided %s = %s reason %q, want %q reason %q -- without these the missing buyer_tin above is indistinguishable from an unread page",
				want.field, lcValue(got), got.Reason, want.value, want.reason)
		}
	}
}

// --- E-02 / AC #2: one pointed correction, one rule, keyed to the job's fingerprint --------

func TestRLS_APointedCorrectionOnTheTwoPartyLayoutWritesOneRule(t *testing.T) {
	ctx := t.Context()
	f := clSeed(t, ctx, "EXTR14-09-E02")
	fp, pages := lcLayout(t, ctx, f.jobID, fxLearnedTwoParty)

	// The derivation itself, verbatim and independent of the database.
	obs := extraction.AnchorObservations(pages)
	if len(obs) != 5 {
		t.Fatalf("%s carries %d anchor observation(s), want 5 -- the fixture drifted and the derivation below is measured against the wrong page", fxLearnedTwoParty, len(obs))
	}
	region := clTokenRegion(t, pages, lcBuyerTIN)
	lr, ok := extraction.LearnRule(lcField, region, obs)
	if !ok {
		t.Fatalf("LearnRule refused the buyer TIN box %+v; nothing below can be stored", region)
	}
	if string(lr.Body) != lcBuyerRuleBody {
		t.Errorf("LearnRule derived %s, want %s", lr.Body, lcBuyerRuleBody)
	}
	if lr.Anchor.Label != "buyer_name" || lr.Anchor.Text != "Buyer" {
		t.Errorf("LearnRule anchored on %s/%q, want buyer_name/%q -- Supplier is below at gap 0.140267, past the 0.06 dial",
			lr.Anchor.Label, lr.Anchor.Text, "Buyer")
	}

	lcPost(t, f, "the pointed correction", lcField, lcBuyerTIN, region)

	rules := clRules(t, ctx, f.tenantID)
	if len(rules) != 1 {
		t.Fatalf("a pointed correction on a layout-bearing job left %d anchor rule(s), want exactly 1", len(rules))
	}
	r := rules[0]
	if stored := clJobFingerprint(t, ctx, f.jobID); r.fingerprint != stored || stored != fp {
		t.Errorf("the rule is keyed to %q and the job stores %q; both must equal Fingerprint(pages) %q -- a rule under another key can never be read back for this layout",
			r.fingerprint, stored, fp)
	}
	if r.field != lcField {
		t.Errorf("the rule names field %q, want %q", r.field, lcField)
	}
	if r.version != extraction.RuleSchemaVersion {
		t.Errorf("the rule carries schema version %d, want %d -- AnchorRulesFor errors on any other", r.version, extraction.RuleSchemaVersion)
	}
	lcRuleBodyIs(t, ctx, r.id, lcBuyerRuleBody)

	// The control on the SAME job: a typed correction teaches nothing, so the 1 above is the
	// pointed gesture and not "any correction writes a rule".
	w := cxServe(t, f.reqCtx, f.jobID, lcField, corBody(lcBuyerTIN, "typed", ""),
		cxApplier(false, nil), cxAuditor(nil))
	if w.Code != http.StatusCreated {
		t.Fatalf("control: the typed correction answered %d (body=%q), want 201", w.Code, w.Body.String())
	}
	if n := len(clRules(t, ctx, f.tenantID)); n != 1 {
		t.Errorf("control: %d anchor rule(s) after a typed correction, want the same 1 -- only a POINTED correction teaches", n)
	}
}

// --- E-03 / AC #3: the second document of the same layout reads better --------------------

// The story this subtask exists for. Job 2 carries no correction of its own; every buyer_tin
// candidate it gets comes from what job 1's reviewer taught.
func TestRLS_TheSecondDocumentOfTheSameLayoutResolvesTheLearnedBuyerTIN(t *testing.T) {
	ctx := t.Context()
	f := clSeed(t, ctx, "EXTR14-09-E03")
	fp1, pages := lcLayout(t, ctx, f.jobID, fxLearnedTwoParty)

	lcPost(t, f, "job 1", lcField, lcBuyerTIN, clTokenRegion(t, pages, lcBuyerTIN))
	rules := clRules(t, ctx, f.tenantID)
	if len(rules) != 1 {
		t.Fatalf("job 1 left %d anchor rule(s), want exactly 1 -- job 2 below would read against nothing", len(rules))
	}

	// Job 2: the same tenant, the same bytes, a second read.
	job2 := cxJobIn(t, ctx, f.tenantID, f.documentID)
	fp2, pages2 := lcLayout(t, ctx, job2, fxLearnedTwoParty)
	if fp2 != fp1 {
		t.Fatalf("job 2's fingerprint %q differs from job 1's %q over the same bytes; the rule could never be loaded for it", fp2, fp1)
	}
	if n := cxCorrectionRows(t, ctx, job2); n != 0 {
		t.Fatalf("job 2 carries %d correction row(s), want 0 -- a document with its own correction proves nothing about what carried over", n)
	}

	got := lcResolve(t, ctx, f.tenantID, fp2, lcField, pages2)
	if len(got.rules) != 1 || got.rules[0].ID != rules[0].id {
		t.Fatalf("AnchorRulesFor returned %d rule(s) for job 2's fingerprint, want the 1 job 1 wrote (%s)", len(got.rules), rules[0].id)
	}
	if len(got.cands) != 1 {
		t.Fatalf("job 2 resolved %d %s candidate(s) %v, want exactly 1 -- the rule matches one label and ShapeTIN rejects the party name below it",
			len(got.cands), lcField, rvValues(got.cands))
	}
	c := got.cands[0]
	if c.Value != lcBuyerTIN {
		t.Errorf("job 2 resolved %s = %q, want %q -- %q is the SUPPLIER's TIN and is not the expected answer",
			lcField, c.Value, lcBuyerTIN, lcSupplierTIN)
	}
	if c.Tier != extraction.TierLearned {
		t.Errorf("the candidate is at tier %v, want TierLearned -- a generic candidate would mean Tier-1 reached it after all", c.Tier)
	}
	if c.RuleID != rules[0].id {
		t.Errorf("the candidate came from rule %q, want the stored %q", c.RuleID, rules[0].id)
	}
	if c.Distance != lcBuyerDistance {
		t.Errorf("the candidate sits at distance %v, want the measured gap %v between the Buyer label and the TIN below it", c.Distance, lcBuyerDistance)
	}

	if lcValue(got.decided) != lcBuyerTIN || got.decided.Reason != extraction.ReasonNone {
		t.Errorf("job 2 decided %s = %s reason %q, want %q and %q",
			lcField, lcValue(got.decided), got.decided.Reason, lcBuyerTIN, extraction.ReasonNone)
	}
	if alts := lcAltValues(got.decided); len(alts) != 0 {
		t.Errorf("job 2 kept %d alternative(s) %v for %s, want none -- one rule matching one label is one answer", len(alts), alts, lcField)
	}

	// No collateral damage: the learned rule is additive, and Tier-1's own six readings stand.
	if got.all != lcTier1Candidates+1 {
		t.Errorf("job 2 produced %d candidate(s) overall, want %d -- the learned rule adds ONE candidate and displaces none", got.all, lcTier1Candidates+1)
	}
}

// --- E-04 / AC #6: the negative control ---------------------------------------------------

// Deleting the rule row reds E-03's assertion, through the SAME lcResolve helper. Without the
// non-zero read first and the 1-row delete tag, a suite where nothing was ever written would
// satisfy the zeros below identically.
func TestRLS_DeletingTheLearnedRuleRedsTheSecondDocumentAssertion(t *testing.T) {
	ctx := t.Context()
	f := clSeed(t, ctx, "EXTR14-09-E04")
	fp, pages := lcLayout(t, ctx, f.jobID, fxLearnedTwoParty)
	lcPost(t, f, "job 1", lcField, lcBuyerTIN, clTokenRegion(t, pages, lcBuyerTIN))

	job2 := cxJobIn(t, ctx, f.tenantID, f.documentID)
	_, pages2 := lcLayout(t, ctx, job2, fxLearnedTwoParty)

	before := lcResolve(t, ctx, f.tenantID, fp, lcField, pages2)
	if len(before.rules) != 1 || len(before.cands) != 1 || lcValue(before.decided) != lcBuyerTIN {
		t.Fatalf("before the delete: %d rule(s), %d candidate(s), decided %s -- want 1, 1 and %q, or the zeros below hold against a suite that never wrote anything",
			len(before.rules), len(before.cands), lcValue(before.decided), lcBuyerTIN)
	}

	lcDeleteRule(t, ctx, before.rules[0].ID)

	after := lcResolve(t, ctx, f.tenantID, fp, lcField, pages2)
	if len(after.rules) != 0 {
		t.Errorf("AnchorRulesFor returned %d rule(s) after the row was deleted, want 0", len(after.rules))
	}
	if len(after.cands) != 0 {
		t.Errorf("the identical read produced %d %s candidate(s) %v after the rule was deleted, want 0 -- E-03 would then be reading something other than the stored row",
			len(after.cands), lcField, rvValues(after.cands))
	}
	if after.decided.Value != nil || after.decided.Reason != extraction.ReasonMissing {
		t.Errorf("the identical read decided %s = %s reason %q after the delete, want <nil> and %q",
			lcField, lcValue(after.decided), after.decided.Reason, extraction.ReasonMissing)
	}
}

// --- E-05 / AC #4: the rule does not leave its tenant --------------------------------------

// Tenant B gets its own job over the IDENTICAL bytes, so the two fingerprints are equal and the
// isolation cannot be a fingerprint accident. Both sides are queried by tenant id, never by
// "the only row in the table", and tenant A is the paired positive control in this same test.
func TestRLS_TheLearnedRuleDoesNotLeaveItsTenant(t *testing.T) {
	ctx := t.Context()
	a := clSeed(t, ctx, "EXTR14-09-E05-A")
	b := clSeed(t, ctx, "EXTR14-09-E05-B")
	if a.tenantID == b.tenantID {
		t.Fatalf("both fixtures seeded tenant %s; there is no second tenant to isolate from", a.tenantID)
	}

	fpA, pages := lcLayout(t, ctx, a.jobID, fxLearnedTwoParty)
	fpB, pagesB := lcLayout(t, ctx, b.jobID, fxLearnedTwoParty)
	if fpA != fpB {
		t.Fatalf("the two tenants' jobs fingerprint the same bytes as %q and %q; a zero for tenant B would then be a fingerprint accident and not isolation", fpA, fpB)
	}

	lcPost(t, a, "tenant A", lcField, lcBuyerTIN, clTokenRegion(t, pages, lcBuyerTIN))

	// Tenant A: the positive control.
	gotA := lcResolve(t, ctx, a.tenantID, fpA, lcField, pages)
	if len(gotA.rules) != 1 {
		t.Fatalf("tenant A holds %d rule(s) for %q, want 1 -- tenant B's zero proves nothing without it", len(gotA.rules), fpA)
	}
	if len(gotA.cands) != 1 || gotA.cands[0].Value != lcBuyerTIN {
		t.Fatalf("tenant A resolved %d %s candidate(s) %v, want exactly 1 valued %q", len(gotA.cands), lcField, rvValues(gotA.cands), lcBuyerTIN)
	}

	// Tenant B: the same fingerprint, the same bytes, nothing learned.
	gotB := lcResolve(t, ctx, b.tenantID, fpB, lcField, pagesB)
	if len(gotB.rules) != 0 {
		t.Errorf("tenant B holds %d rule(s) for the same fingerprint, want 0 -- a learned rule is one tenant's, and RLS is what keeps it there", len(gotB.rules))
	}
	if len(gotB.cands) != 0 {
		t.Errorf("tenant B resolved %d %s candidate(s) %v, want 0", len(gotB.cands), lcField, rvValues(gotB.cands))
	}
	if gotB.decided.Value != nil || gotB.decided.Reason != extraction.ReasonMissing {
		t.Errorf("tenant B decided %s = %s reason %q, want <nil> and %q -- its document reads exactly as it did before tenant A pointed at anything",
			lcField, lcValue(gotB.decided), gotB.decided.Reason, extraction.ReasonMissing)
	}

	// Counted by tenant id as the superuser, so RLS cannot be what makes the zero above.
	if n := len(clRules(t, ctx, b.tenantID)); n != 0 {
		t.Errorf("tenant B owns %d anchor rule row(s) by a superuser count, want 0", n)
	}
	if n := len(clRules(t, ctx, a.tenantID)); n != 1 {
		t.Errorf("tenant A owns %d anchor rule row(s) by a superuser count, want 1", n)
	}
}

// --- E-06 + E-07 / AC #5: a second pointed correction supersedes the first ------------------

// The first place a LIVE newer rule displaces a LIVE older one over real PDF bytes: both rules
// produce a candidate here, and the two candidates carry DIFFERENT values. C-13 cannot make
// that distinction -- on corpus_two_column.pdf its second rule is barren, so "the newer rule
// won" and "the newer rule produced nothing" look the same. V-04 and V-08 are synthetic pages.
//
// The reversal control is what tells "R2 won" from "R2 happened to be the one that fired".
func TestRLS_ASecondPointedCorrectionSupersedesTheFirstOnTheThirdDocument(t *testing.T) {
	ctx := t.Context()
	f := clSeed(t, ctx, "EXTR14-09-E06")
	fp, pages := lcLayout(t, ctx, f.jobID, fxLearnedTwoParty)

	lcPost(t, f, "R1", lcField, lcBuyerTIN, clTokenRegion(t, pages, lcBuyerTIN))
	first := clRules(t, ctx, f.tenantID)
	if len(first) != 1 {
		t.Fatalf("R1: %d anchor rule(s), want 1 -- every arm below reads against this one", len(first))
	}
	r1 := first[0]
	lcRuleBodyIs(t, ctx, r1.id, lcBuyerRuleBody)

	// R2 points buyer_tin at the SUPPLIER's bare TIN, so it anchors on a different label and
	// resolves a different value.
	lcPost(t, f, "R2", lcField, lcSupplierTIN, clTokenRegion(t, pages, lcSupplierTIN))
	rules := clRules(t, ctx, f.tenantID)

	// E-07: convergence is by ordering, never by deletion. The table is append-only by GRANT.
	if len(rules) != 2 {
		t.Fatalf("after the second pointed correction the tenant holds %d rule(s), want 2 -- supersession appends, it never deletes", len(rules))
	}
	if rules[0].id == r1.id {
		t.Fatalf("AnchorRulesFor order puts R1 (%s) first; newest-first is what makes a later correction supersede an earlier one", r1.id)
	}
	r2 := rules[0]
	lcRuleBodyIs(t, ctx, r2.id, lcSupplierRuleBody)
	if rules[1].id != r1.id {
		t.Fatalf("the older row read back as %s, want R1 %s", rules[1].id, r1.id)
	}

	// Document 3: the same bytes, no correction of its own.
	job3 := cxJobIn(t, ctx, f.tenantID, f.documentID)
	fp3, pages3 := lcLayout(t, ctx, job3, fxLearnedTwoParty)
	if fp3 != fp {
		t.Fatalf("document 3 fingerprints as %q, want %q", fp3, fp)
	}

	got := lcResolve(t, ctx, f.tenantID, fp3, lcField, pages3)
	if len(got.rules) != 2 || got.rules[0].ID != r2.id {
		t.Fatalf("AnchorRulesFor returned %d rule(s) with %s first, want 2 with R2 %s first", len(got.rules), rvFirstRuleID(got.rules), r2.id)
	}
	if len(got.cands) != 1 {
		t.Fatalf("document 3 resolved %d %s candidate(s) %v, want exactly 1 -- the first rule to produce anything claims the field",
			len(got.cands), lcField, rvValues(got.cands))
	}
	if got.cands[0].RuleID != r2.id {
		t.Errorf("the candidate came from rule %q, want the NEWER R2 %q", got.cands[0].RuleID, r2.id)
	}
	if lcValue(got.decided) != lcSupplierTIN || got.decided.Reason != extraction.ReasonNone {
		t.Errorf("document 3 decided %s = %s reason %q, want %q and %q -- R2 points at the supplier block, and that is what a second pointed correction MEANS here",
			lcField, lcValue(got.decided), got.decided.Reason, lcSupplierTIN, extraction.ReasonNone)
	}
	if alts := lcAltValues(got.decided); len(alts) != 0 {
		t.Errorf("document 3 kept %d alternative(s) %v, want none", len(alts), alts)
	}
	if got.cands[0].Distance != lcSupplierDistance {
		t.Errorf("the candidate sits at distance %v, want the measured Supplier gap %v", got.cands[0].Distance, lcSupplierDistance)
	}

	// The reversal control: R1 ahead of R2 over the SAME page decides the other value. Without
	// it, "R2 won" cannot be told from "R2 was the only rule that fired".
	reversed := extraction.Resolve(pages3, extraction.RuleSet{
		Learned: []extraction.AnchorRule{got.rules[1], got.rules[0]},
		Tier1:   extraction.Tier1Rules,
	})
	rev := lcDecided(t, extraction.Reconcile(extraction.Input{Candidates: reversed}), lcField)
	if lcValue(rev) != lcBuyerTIN {
		t.Errorf("with R1 ahead of R2 the same page decides %s = %s, want %q -- if BOTH orders answered the same, ordering would not be what supersedes",
			lcField, lcValue(rev), lcBuyerTIN)
	}
	if revCands := rvFor(reversed, lcField); len(revCands) != 1 || revCands[0].RuleID != r1.id {
		t.Errorf("with R1 ahead of R2 the candidate came from %v, want exactly one from R1 %s", rvValues(revCands), r1.id)
	}
}

func rvFirstRuleID(rules []extraction.AnchorRule) string {
	if len(rules) == 0 {
		return "<none>"
	}
	return rules[0].ID
}

// --- E-08: breadth, a second layout and a second relation kind -----------------------------

// Not an AC arm: Tier-1 already gets issue_date right on corpus_split_labels.pdf. It is here so
// the chain is not proved on one fixture and one relation kind. Measured: right, 0.14.
func TestRLS_APointedIssueDateCorrectionTeachesARightRuleOnASecondLayout(t *testing.T) {
	ctx := t.Context()
	f := clSeed(t, ctx, "EXTR14-09-E08")
	fp, pages := lcLayout(t, ctx, f.jobID, lcSplit)

	const (
		field     = "issue_date"
		token     = "15/04/2026"
		value     = "2026-04-15"
		body      = `{"label":"(?i)\\bInvoice Date\\b","relation":{"kind":"right","max_distance":0.14},"shape":"date"}`
		distance  = 0.13561438267527065
		anchorTxt = "Invoice Date"
	)

	lcPost(t, f, "job 1", field, value, clTokenRegion(t, pages, token))
	rules := clRules(t, ctx, f.tenantID)
	if len(rules) != 1 {
		t.Fatalf("%d anchor rule(s) after a pointed issue_date correction, want 1", len(rules))
	}
	if rules[0].field != field {
		t.Errorf("the rule names field %q, want %q", rules[0].field, field)
	}
	lcRuleBodyIs(t, ctx, rules[0].id, body)

	job2 := cxJobIn(t, ctx, f.tenantID, f.documentID)
	fp2, pages2 := lcLayout(t, ctx, job2, lcSplit)
	if fp2 != fp {
		t.Fatalf("job 2 fingerprints as %q, want %q", fp2, fp)
	}

	learned, err := stStore(t).AnchorRulesFor(ctx, f.tenantID, fp2)
	if err != nil {
		t.Fatalf("AnchorRulesFor: %v", err)
	}
	// Learned alone here: with Tier-1 behind it the same value arrives twice and the candidate
	// under test could not be told from the generic one.
	got := rvFor(extraction.Resolve(pages2, extraction.RuleSet{Learned: learned}), field)
	if len(got) != 1 {
		t.Fatalf("the learned rule resolved %d %s candidate(s) %v on job 2, want exactly 1", len(got), field, rvValues(got))
	}
	if got[0].Value != value {
		t.Errorf("the learned rule resolved %s = %q, want %q", field, got[0].Value, value)
	}
	if got[0].Tier != extraction.TierLearned {
		t.Errorf("the candidate is at tier %v, want TierLearned", got[0].Tier)
	}
	if got[0].Distance != distance {
		t.Errorf("the candidate sits at distance %v, want the measured %q gap %v", got[0].Distance, anchorTxt, distance)
	}
}

// --- E-09: a rule is never even loaded under another layout's fingerprint -------------------

// The PRIMARY oracle is the store, not the resolver. Applying the rule to another layout also
// yields nothing today, but only because corpus_split_labels.pdf happens to carry no TIN-shaped
// token within 0.03 below a "Buyer" label -- a content coincidence that would stay green if the
// fingerprint gate were removed. It is asserted below as a SECONDARY arm only.
func TestRLS_ALearnedRuleIsNeverLoadedUnderAnotherLayoutsFingerprint(t *testing.T) {
	ctx := t.Context()
	f := clSeed(t, ctx, "EXTR14-09-E09")
	fp, pages := lcLayout(t, ctx, f.jobID, fxLearnedTwoParty)

	fpSplit := extraction.Fingerprint(rvCorpusPages(t, lcSplit))
	fpTwoCol := extraction.Fingerprint(rvCorpusPages(t, lcTwoCol))
	for _, pair := range []struct{ a, b, an, bn string }{
		{fp, fpSplit, fxLearnedTwoParty, lcSplit},
		{fp, fpTwoCol, fxLearnedTwoParty, lcTwoCol},
		{fpSplit, fpTwoCol, lcSplit, lcTwoCol},
	} {
		if pair.a == pair.b {
			t.Fatalf("%s and %s fingerprint identically as %q; the isolation below would be asserted over one key", pair.an, pair.bn, pair.a)
		}
	}

	lcPost(t, f, "job 1", lcField, lcBuyerTIN, clTokenRegion(t, pages, lcBuyerTIN))

	// Positive control: under its OWN fingerprint the rule is there.
	own, err := stStore(t).AnchorRulesFor(ctx, f.tenantID, fp)
	if err != nil {
		t.Fatalf("AnchorRulesFor(own fingerprint): %v", err)
	}
	if len(own) != 1 {
		t.Fatalf("the tenant holds %d rule(s) under its own fingerprint, want 1 -- the zeros below hold equally against a tenant that learned nothing", len(own))
	}

	// The primary oracle: under another layout's key the rule is never read at all.
	for _, other := range []struct{ name, fingerprint string }{
		{lcSplit, fpSplit},
		{lcTwoCol, fpTwoCol},
	} {
		loaded, err := stStore(t).AnchorRulesFor(ctx, f.tenantID, other.fingerprint)
		if err != nil {
			t.Fatalf("AnchorRulesFor(%s): %v", other.name, err)
		}
		if len(loaded) != 0 {
			t.Errorf("%d rule(s) loaded under %s's fingerprint %q, want 0 -- a rule keyed to one layout must not be READ for another",
				len(loaded), other.name, other.fingerprint)
		}
	}

	// Secondary: even if it were loaded, it resolves nothing there. Content, not mechanism.
	splitPages := rvCorpusPages(t, lcSplit)
	cross := rvFor(extraction.Resolve(splitPages, extraction.RuleSet{Learned: own}), lcField)
	if len(cross) != 0 {
		t.Errorf("the learned rule produced %d %s candidate(s) %v on %s, want 0", len(cross), lcField, rvValues(cross), lcSplit)
	}
}

// --- E-12: the two-column regression, asserted rather than avoided --------------------------

// This is a regression this feature CAUSES. It is recorded in D-18 and in
// docs/extraction-corpus.md ("## Learned rules", "When a learned rule misfires"), and it is
// asserted here rather than left as a comment. corpus_two_column.pdf goes from one honest
// "missing" to a confidently decided WRONG value with one alternative, and the wrong value is
// the SUPPLIER's TIN. Both candidates come from the SAME rule, so newest-rule-wins cannot
// rescue it.
//
// A failure here means the documented regression CHANGED -- not that buyer_tin is wrong.
func TestRLS_TheTwoColumnLayoutGetsWorseBeforeItGetsBetter(t *testing.T) {
	ctx := t.Context()
	f := clSeed(t, ctx, "EXTR14-09-E12")
	fp, pages := lcLayout(t, ctx, f.jobID, lcTwoCol)

	const (
		buyerToken   = "TIN: 99999999-0402"
		supplierTok  = "TIN: 99999999-0401"
		buyerValue   = "99999999-0402"
		supplierVal  = "99999999-0401"
		tinRuleBody  = `{"label":"(?i)\\bTIN\\b","relation":{"kind":"same_token","max_distance":0.00},"shape":"tin"}`
		sharedY0     = 0.23407067192925346
		twoColTotal  = "6450.00"
		twoColNumber = "INV-1004"
	)

	// Arm 1, before. The floor: the same read decides total and invoice_number, so an unread
	// page cannot pass as "buyer_tin is missing".
	t1 := extraction.Resolve(pages, extraction.RuleSet{Tier1: extraction.Tier1Rules})
	rvFloor(t, t1, "the shipped Tier-1 set over "+lcTwoCol)
	if got := rvFor(t1, lcField); len(got) != 0 {
		t.Fatalf("before: Tier-1 produced %d %s candidate(s) %v on %s, want 0 -- the regression below is a change FROM missing",
			len(got), lcField, rvValues(got), lcTwoCol)
	}
	beforeAll := extraction.Reconcile(extraction.Input{Candidates: t1})
	beforeBuyer := lcDecided(t, beforeAll, lcField)
	if beforeBuyer.Value != nil || beforeBuyer.Reason != extraction.ReasonMissing {
		t.Fatalf("before: %s = %s reason %q, want <nil> and %q", lcField, lcValue(beforeBuyer), beforeBuyer.Reason, extraction.ReasonMissing)
	}
	for _, want := range []struct{ field, value string }{{"total", twoColTotal}, {"invoice_number", twoColNumber}} {
		if got := lcDecided(t, beforeAll, want.field); lcValue(got) != want.value {
			t.Fatalf("before: %s = %s, want %q -- the missing buyer_tin above is otherwise indistinguishable from an unread page",
				want.field, lcValue(got), want.value)
		}
	}

	// Arm 2: the gesture a reviewer actually makes -- pointing at the BUYER's own token.
	lcPost(t, f, "the pointed correction", lcField, buyerValue, clTokenRegion(t, pages, buyerToken))
	rules := clRules(t, ctx, f.tenantID)
	if len(rules) != 1 {
		t.Fatalf("%d anchor rule(s), want 1", len(rules))
	}
	lcRuleBodyIs(t, ctx, rules[0].id, tinRuleBody)

	// Arm 3, after. TWO candidates from ONE rule, and the one that wins is the supplier's.
	got := lcResolve(t, ctx, f.tenantID, fp, lcField, pages)
	if len(got.cands) != 2 {
		t.Fatalf("after: %d %s candidate(s) %v, want exactly 2 -- the derived label matches both party blocks",
			len(got.cands), lcField, rvValues(got.cands))
	}
	for _, c := range got.cands {
		if c.RuleID != rules[0].id {
			t.Errorf("after: a candidate came from rule %q, want the ONE stored rule %q -- two rules would mean newest-rule-wins could resolve this",
				c.RuleID, rules[0].id)
		}
		if c.Tier != extraction.TierLearned || c.Distance != 0 {
			t.Errorf("after: a candidate is at tier %v distance %v, want TierLearned at 0 (same_token has no gap)", c.Tier, c.Distance)
		}
	}
	if lcValue(got.decided) != supplierVal {
		t.Errorf("after: %s decided as %s, want the SUPPLIER's %q -- this layout is documented as getting worse, and a different winner means the documented regression changed",
			lcField, lcValue(got.decided), supplierVal)
	}
	if got.decided.Reason != extraction.ReasonAmbiguous {
		t.Errorf("after: %s reason %q, want %q", lcField, got.decided.Reason, extraction.ReasonAmbiguous)
	}
	alts := lcAltValues(got.decided)
	if len(alts) != 1 || alts[0] != buyerValue {
		t.Errorf("after: alternatives %v, want exactly [%s]", alts, buyerValue)
	}

	// Arm 4: the mechanism, pinned. Y0 is bit-identical across the two tokens, so compareRegions
	// falls through to X0 -- and 0.1179 < 0.6539 hands the field to the supplier. A change to
	// that comparator reds HERE and not somewhere unrelated.
	sup := rvTokenByText(t, pages, supplierTok).Region
	buy := rvTokenByText(t, pages, buyerToken).Region
	if sup.Y0 != buy.Y0 {
		t.Errorf("the two TIN tokens sit at Y0 %v and %v; they are on one baseline and the tie X0 breaks only exists because these are EQUAL", sup.Y0, buy.Y0)
	}
	if sup.Y0 != sharedY0 {
		t.Errorf("the shared baseline reads Y0 %v, want the measured %v", sup.Y0, sharedY0)
	}
	if !(sup.X0 < buy.X0) {
		t.Errorf("the supplier token starts at X0 %v and the buyer's at %v; the supplier must sort FIRST, which is why the wrong value wins", sup.X0, buy.X0)
	}
}

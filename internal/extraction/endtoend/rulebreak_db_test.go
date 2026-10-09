package endtoend

import (
	"context"
	"testing"

	"github.com/SimonOsipov/invoice-os/internal/extraction"
	"github.com/SimonOsipov/invoice-os/internal/invoice"
	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
)

// eeRuleGate returns the canned violations for every invoice it is asked about, stamped with a
// real rule set version (extraction_rule_breaks references one).
type eeRuleGate struct {
	eeGate
	vs        []invoice.Violation
	versionID string
}

func (g eeRuleGate) Evaluate(_ context.Context, items []invoice.EvalItem) (invoice.EvalResult, error) {
	by := map[string][]invoice.Violation{}
	for _, it := range items {
		by[it.Ref] = g.vs
	}
	return invoice.EvalResult{ByRef: by, RuleSetVersionID: g.versionID}, nil
}

// Worker read, real ImportDocument and real Reader.Detail meet: the importer's rule-break write
// shows up on the review read, and only on the field the violation names.
func TestRLS_EndToEndARuleBreakReachesTheReview(t *testing.T) {
	h := eeRequire(t)
	ctx := t.Context()
	w := eeSeed(t, ctx, eeLayout)
	jobID := eeExtract(t, ctx, w, eeLayout)

	// importer.ruleBreakFields, inverted: header field -> violation path.
	pathOf := map[string]string{
		"issue_date": "issue_date", "currency": "currency", "subtotal": "subtotal", "vat": "vat",
		"total": "total", "buyer_tin": "buyer.tin", "buyer_name": "buyer.name",
	}
	stored := map[string]string{}
	picked, pickedPath := "", ""
	for _, r := range eeFieldResults(t, ctx, jobID) {
		if r.rank != 0 {
			continue
		}
		reason := ""
		if r.reason != nil {
			reason = *r.reason
		}
		stored[r.name] = reason
		if p, ok := pathOf[r.name]; ok && picked == "" && r.value != nil && reason == "" {
			picked, pickedPath = r.name, p
		}
	}
	if picked == "" {
		t.Fatalf("no mapped header field was read with a value and no reason; stored reasons: %v", stored)
	}
	if len(stored) < 2 {
		t.Fatalf("worker stored %d field(s), want others besides %s to compare", len(stored), picked)
	}

	var versionID string
	if err := h.super.QueryRow(ctx, `SELECT id FROM rule_set_versions ORDER BY version DESC LIMIT 1`).Scan(&versionID); err != nil {
		t.Fatalf("read a rule set version: %v", err)
	}

	const key, msg = "ee_canned_rule", "canned rule message"
	eeImport(t, ctx, w, eeRuleGate{vs: []invoice.Violation{
		{RuleKey: key, Severity: "error", Message: msg, Path: pickedPath},
	}, versionID: versionID})

	rctx := auth.WithIdentity(ctx, auth.Identity{Subject: w.subject, Role: "authenticated", TenantID: w.tenantID})
	detail, err := (&extraction.Reader{Pool: h.app}).Detail(rctx, jobID)
	if err != nil {
		t.Fatalf("Detail(%s): %v", jobID, err)
	}

	seen := false
	others := 0
	for _, f := range detail.Fields {
		if f.Name == picked {
			seen = true
			if f.Reason != "rule_break" {
				t.Errorf("%s reason = %q, want rule_break", f.Name, f.Reason)
			}
			if len(f.Rules) != 1 || f.Rules[0].Key != key || f.Rules[0].Message != msg {
				t.Errorf("%s rules = %+v, want [{%s %s}]", f.Name, f.Rules, key, msg)
			}
			continue
		}
		others++
		if want, ok := stored[f.Name]; ok && f.Reason != want {
			t.Errorf("%s reason = %q, want worker's %q", f.Name, f.Reason, want)
		}
		if len(f.Rules) != 0 {
			t.Errorf("%s rules = %+v, want none", f.Name, f.Rules)
		}
	}
	if !seen {
		t.Fatalf("Detail has no %s field", picked)
	}
	if others == 0 {
		t.Fatal("Detail has no field besides the flagged one; the other-fields assertion would be vacuous")
	}
}

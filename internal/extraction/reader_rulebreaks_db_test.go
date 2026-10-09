// reader_rulebreaks_db_test.go: Reader.Detail over stored extraction_rule_breaks rows.
package extraction_test

import (
	"context"
	"testing"
	"time"

	"github.com/SimonOsipov/invoice-os/internal/extraction"
)

func rbSeedBreak(t *testing.T, ctx context.Context, tenantID, jobID, field, key, msg string) {
	t.Helper()
	if _, err := stRequire(t).super.Exec(ctx,
		`INSERT INTO extraction_rule_breaks
		     (tenant_id, extraction_job_id, field_name, rule_set_version_id, rule_key, message)
		 VALUES ($1, $2, $3, (SELECT id FROM rule_set_versions WHERE is_active), $4, $5)`,
		tenantID, jobID, field, key, msg,
	); err != nil {
		t.Fatalf("seed rule break %s on %s: %v", key, field, err)
	}
}

func TestRLS_ExtractionDetailOverlaysStoredRuleBreaks(t *testing.T) {
	ctx := t.Context()
	r := &extraction.Reader{Pool: rdTracedPool(t, &rdQueryTracer{})}
	ctxA, tenantA, docA := rdTenant(t, ctx, "active")
	now := time.Now().UTC().Truncate(time.Microsecond)
	job := rdSeedJob(t, ctx, tenantA, docA, "succeeded", now, nil)
	rvdSeedField(t, ctx, tenantA, job, "buyer_tin", rvdStr("123"), nil, 0, nil, now)
	rvdSeedField(t, ctx, tenantA, job, "invoice_number", rvdStr("A-1"), nil, 0, nil, now)
	rbSeedBreak(t, ctx, tenantA, job, "buyer_tin", "buyer-tin-format", "The buyer TIN is malformed.")

	got, err := r.Detail(ctxA, job)
	if err != nil {
		t.Fatalf("Detail: %v", err)
	}
	f := rvcField(t, got, "buyer_tin")
	if f.Reason != "rule_break" || len(f.Rules) != 1 || f.Rules[0].Key != "buyer-tin-format" ||
		f.Rules[0].Message != "The buyer TIN is malformed." {
		t.Errorf("buyer_tin = reason %q rules %v, want rule_break with buyer-tin-format", f.Reason, f.Rules)
	}
	if o := rvcField(t, got, "invoice_number"); o.Reason != "" || o.Rules == nil || len(o.Rules) != 0 {
		t.Errorf("invoice_number = reason %q rules %v, want \"\" and []", o.Reason, o.Rules)
	}
}

func TestRLS_ExtractionDetailACorrectionClearsTheRuleBreak(t *testing.T) {
	ctx := t.Context()
	r := &extraction.Reader{Pool: rdTracedPool(t, &rdQueryTracer{})}
	ctxA, tenantA, docA := rdTenant(t, ctx, "active")
	now := time.Now().UTC().Truncate(time.Microsecond)
	job := rdSeedJob(t, ctx, tenantA, docA, "succeeded", now, nil)
	rvdSeedField(t, ctx, tenantA, job, "buyer_tin", rvdStr("123"), nil, 0, nil, now)
	rbSeedBreak(t, ctx, tenantA, job, "buyer_tin", "buyer-tin-format", "malformed")
	rvcSeedCorrection(t, ctx, tenantA, job, "buyer_tin", "12345678-0001", "typed", nil, nil)

	got, err := r.Detail(ctxA, job)
	if err != nil {
		t.Fatalf("Detail: %v", err)
	}
	if f := rvcField(t, got, "buyer_tin"); f.Reason != "" || f.Rules == nil || len(f.Rules) != 0 {
		t.Errorf("after a typed correction: reason %q rules %v, want \"\" and []", f.Reason, f.Rules)
	}

	rvcSeedCorrection(t, ctx, tenantA, job, "buyer_tin", "123", "undone", nil, nil)
	got, err = r.Detail(ctxA, job)
	if err != nil {
		t.Fatalf("Detail after undo: %v", err)
	}
	if f := rvcField(t, got, "buyer_tin"); f.Reason != "rule_break" || len(f.Rules) != 1 {
		t.Errorf("after an undo: reason %q rules %v, want rule_break and one rule", f.Reason, f.Rules)
	}
}

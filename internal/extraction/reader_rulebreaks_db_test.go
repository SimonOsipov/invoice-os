// reader_rulebreaks_db_test.go: Reader.Detail over stored extraction_rule_breaks rows.
package extraction_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/SimonOsipov/invoice-os/internal/extraction"
)

func rbSeedBreak(t *testing.T, ctx context.Context, tenantID, jobID, field, key, msg string) {
	t.Helper()
	if _, err := stRequire(t).super.Exec(ctx,
		`INSERT INTO extraction_rule_breaks
		     (tenant_id, extraction_job_id, field_name, rule_set_version_id, rule_key, message)
		 VALUES ($1, $2, $3, (SELECT id FROM rule_set_versions WHERE id = rule_set_version_for((now() AT TIME ZONE 'UTC')::date)), $4, $5)`,
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

func rbSeedBreakAt(t *testing.T, ctx context.Context, tenantID, jobID, field, key string, at time.Time) {
	t.Helper()
	if _, err := stRequire(t).super.Exec(ctx,
		`INSERT INTO extraction_rule_breaks
		     (tenant_id, extraction_job_id, field_name, rule_set_version_id, rule_key, message, created_at)
		 VALUES ($1, $2, $3, (SELECT id FROM rule_set_versions WHERE id = rule_set_version_for((now() AT TIME ZONE 'UTC')::date)), $4, $5, $6)`,
		tenantID, jobID, field, key, "msg "+key, at,
	); err != nil {
		t.Fatalf("seed rule break %s on %s: %v", key, field, err)
	}
}

func TestRLS_ExtractionDetailRuleBreakEdges(t *testing.T) {
	ctx := t.Context()
	r := &extraction.Reader{Pool: rdTracedPool(t, &rdQueryTracer{})}
	ctxA, tenantA, docA := rdTenant(t, ctx, "active")
	now := time.Now().UTC().Truncate(time.Microsecond)
	job := rdSeedJob(t, ctx, tenantA, docA, "succeeded", now, nil)
	rvdSeedField(t, ctx, tenantA, job, "vat", rvdStr("50.00"), nil, 0, nil, now)
	rvdSeedField(t, ctx, tenantA, job, "buyer_tin", rvdStr("X"), nil, 0, rvdStr("ambiguous"), now)
	rvdSeedField(t, ctx, tenantA, job, "total", nil, nil, 0, nil, now)
	rvdSeedField(t, ctx, tenantA, job, "orphan", rvdStr("alt"), nil, 1, nil, now)
	// Tied created_at: rule_key breaks the tie. Earlier created_at wins over a smaller key.
	rbSeedBreakAt(t, ctx, tenantA, job, "vat", "z-first", now.Add(-time.Second))
	rbSeedBreakAt(t, ctx, tenantA, job, "vat", "b-tied", now)
	rbSeedBreakAt(t, ctx, tenantA, job, "vat", "a-tied", now)
	rbSeedBreak(t, ctx, tenantA, job, "buyer_tin", "buyer-tin-format", "bad")
	rbSeedBreak(t, ctx, tenantA, job, "total", "total-positive", "bad")
	rbSeedBreak(t, ctx, tenantA, job, "orphan", "orphan-rule", "bad")
	rbSeedBreak(t, ctx, tenantA, job, "ghost", "ghost-rule", "bad")

	got, err := r.Detail(ctxA, job)
	if err != nil {
		t.Fatalf("Detail: %v", err)
	}
	if len(got.Fields) != 3 {
		t.Fatalf("got %d field(s) %v, want vat, buyer_tin, total only", len(got.Fields), got.Fields)
	}
	vat := rvcField(t, got, "vat")
	keys := []string{}
	for _, b := range vat.Rules {
		keys = append(keys, b.Key)
	}
	if vat.Reason != "rule_break" || len(keys) != 3 || keys[0] != "z-first" || keys[1] != "a-tied" || keys[2] != "b-tied" {
		t.Errorf("vat = reason %q rules %v, want rule_break and z-first, a-tied, b-tied", vat.Reason, keys)
	}
	if f := rvcField(t, got, "buyer_tin"); f.Reason != "ambiguous" || f.Rules == nil || len(f.Rules) != 0 {
		t.Errorf("buyer_tin = reason %q rules %v, want ambiguous and []", f.Reason, f.Rules)
	}
	if f := rvcField(t, got, "total"); f.Reason != "" || f.Rules == nil || len(f.Rules) != 0 {
		t.Errorf("total (nil value) = reason %q rules %v, want \"\" and []", f.Reason, f.Rules)
	}
	b, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), `"rules":null`) || strings.Count(string(b), `"rules":[]`) != 2 {
		t.Errorf("wire = %s, want two empty rules arrays and no null", b)
	}
}

func TestRLS_ExtractionDetailZeroBreakJobCarriesEmptyRules(t *testing.T) {
	ctx := t.Context()
	r := &extraction.Reader{Pool: rdTracedPool(t, &rdQueryTracer{})}
	ctxA, tenantA, docA := rdTenant(t, ctx, "active")
	now := time.Now().UTC().Truncate(time.Microsecond)
	job := rdSeedJob(t, ctx, tenantA, docA, "succeeded", now, nil)
	rvdSeedField(t, ctx, tenantA, job, "vat", rvdStr("50.00"), nil, 0, nil, now)
	got, err := r.Detail(ctxA, job)
	if err != nil {
		t.Fatalf("Detail: %v", err)
	}
	b, _ := json.Marshal(got.Fields)
	if len(got.Fields) != 1 || !strings.Contains(string(b), `"rules":[]`) {
		t.Errorf("fields = %s, want one field with \"rules\":[]", b)
	}
}

func TestRLS_ExtractionDetailNeverShowsAnotherTenantsRuleBreaks(t *testing.T) {
	ctx := t.Context()
	r := &extraction.Reader{Pool: rdTracedPool(t, &rdQueryTracer{})}
	ctxA, tenantA, docA := rdTenant(t, ctx, "active")
	ctxB, tenantB, docB := rdTenant(t, ctx, "active")
	now := time.Now().UTC().Truncate(time.Microsecond)
	jobA := rdSeedJob(t, ctx, tenantA, docA, "succeeded", now, nil)
	jobB := rdSeedJob(t, ctx, tenantB, docB, "succeeded", now, nil)
	rvdSeedField(t, ctx, tenantA, jobA, "vat", rvdStr("50.00"), nil, 0, nil, now)
	rvdSeedField(t, ctx, tenantB, jobB, "vat", rvdStr("50.00"), nil, 0, nil, now)
	rbSeedBreak(t, ctx, tenantA, jobA, "vat", "a-only", "tenant A's break")

	if a, err := r.Detail(ctxA, jobA); err != nil || len(rvcField(t, a, "vat").Rules) != 1 {
		t.Fatalf("control: tenant A must see its own break, got %+v err %v", a.Fields, err)
	}
	if _, err := r.Detail(ctxB, jobA); !errors.Is(err, extraction.ErrNotFound) {
		t.Errorf("tenant B reading tenant A's job: err = %v, want ErrNotFound", err)
	}
	gotB, err := r.Detail(ctxB, jobB)
	if err != nil {
		t.Fatalf("Detail B: %v", err)
	}
	if f := rvcField(t, gotB, "vat"); f.Reason != "" || len(f.Rules) != 0 {
		t.Errorf("tenant B's own job shows reason %q rules %v, want none of A's breaks", f.Reason, f.Rules)
	}
}

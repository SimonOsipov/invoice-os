package main

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/SimonOsipov/invoice-os/internal/customer"
)

const (
	tenantA = "11111111-1111-1111-1111-111111111111"
	tenantB = "22222222-2222-2222-2222-222222222222"
)

func TestTenantFlag_SetRejectsNonUUID(t *testing.T) {
	var f tenantFlag
	if err := f.Set("not-a-uuid"); err == nil {
		t.Fatal("Set(non-uuid) = nil error, want a parse failure")
	}
	if len(f) != 0 {
		t.Errorf("tenantFlag = %v, want untouched", f)
	}
}

func TestTenantFlag_SetAppendsValidUUIDsRepeatably(t *testing.T) {
	var f tenantFlag
	for _, v := range []string{tenantA, tenantB} {
		if err := f.Set(v); err != nil {
			t.Fatalf("Set(%q): %v", v, err)
		}
	}
	if len(f) != 2 || f[0] != tenantA || f[1] != tenantB {
		t.Fatalf("tenantFlag = %v, want [a b]", f)
	}
	if got, want := f.String(), tenantA+","+tenantB; got != want {
		t.Errorf("String() = %q, want %q", got, want)
	}
}

func TestRun_MissingDatabaseURLFailsBeforeAnyConnection(t *testing.T) {
	t.Setenv("DATABASE_URL", "")
	err := run(context.Background(), []string{tenantA}, true)
	if err == nil || !strings.Contains(err.Error(), "DATABASE_URL") {
		t.Fatalf("run() err = %v, want a DATABASE_URL error", err)
	}
}

func TestRun_NoTenantFailsBeforeAnyConnection(t *testing.T) {
	t.Setenv("DATABASE_URL", "")
	err := run(context.Background(), nil, true)
	if err == nil || !strings.Contains(err.Error(), "--tenant") || strings.Contains(err.Error(), "DATABASE_URL") {
		t.Fatalf("run() err = %v, want the --tenant error", err)
	}
}

func okBackfill(customer.BackfillResult) func(context.Context, string, bool) (customer.BackfillResult, error) {
	return func(context.Context, string, bool) (customer.BackfillResult, error) {
		return customer.BackfillResult{}, nil
	}
}

func TestRunTenants_ModeLineNamesTheMode(t *testing.T) {
	for _, tc := range []struct {
		dryRun bool
		want   string
	}{
		{true, "backfill-customers: DRY RUN (nothing will be written), 1 tenant(s)"},
		{false, "backfill-customers: WRITING, 1 tenant(s)"},
	} {
		var out, errOut bytes.Buffer
		if err := runTenants(context.Background(), &out, &errOut, []string{tenantA}, tc.dryRun, okBackfill(customer.BackfillResult{})); err != nil {
			t.Fatalf("runTenants: %v", err)
		}
		if !strings.HasPrefix(out.String(), tc.want) {
			t.Errorf("dryRun=%v out = %q, want prefix %q", tc.dryRun, out.String(), tc.want)
		}
	}
}

func TestRunTenants_ContinuesPastAFailedTenant(t *testing.T) {
	var seen []string
	fake := func(_ context.Context, id string, _ bool) (customer.BackfillResult, error) {
		seen = append(seen, id)
		if id == tenantA {
			return customer.BackfillResult{}, errors.New("boom")
		}
		return customer.BackfillResult{InvoicesRead: 3}, nil
	}
	var out, errOut bytes.Buffer
	err := runTenants(context.Background(), &out, &errOut, []string{tenantA, tenantB}, true, fake)
	if err == nil {
		t.Fatal("runTenants = nil, want an error after a failed tenant")
	}
	if !strings.Contains(errOut.String(), "tenant "+tenantA) || !strings.Contains(errOut.String(), "boom") {
		t.Errorf("errOut = %q, want tenant a and boom", errOut.String())
	}
	if !strings.Contains(out.String(), "tenant "+tenantB) || !strings.Contains(out.String(), "invoices read:       3") {
		t.Errorf("out = %q, want the report of b", out.String())
	}
	if len(seen) != 2 || seen[0] != tenantA || seen[1] != tenantB {
		t.Errorf("seen = %v, want [a b]", seen)
	}
}

func TestRunTenants_AllTenantsSucceedReturnsNil(t *testing.T) {
	var out, errOut bytes.Buffer
	if err := runTenants(context.Background(), &out, &errOut, []string{tenantA, tenantB}, true, okBackfill(customer.BackfillResult{})); err != nil {
		t.Fatalf("runTenants = %v, want nil", err)
	}
	if errOut.Len() != 0 {
		t.Errorf("errOut = %q, want empty", errOut.String())
	}
	if strings.Count(out.String(), "\ntenant ") != 2 {
		t.Errorf("out = %q, want two tenant reports", out.String())
	}
}

func TestReport_PrintsEveryCount(t *testing.T) {
	var buf bytes.Buffer
	report(&buf, tenantA, false, customer.BackfillResult{InvoicesRead: 31, TINs: 13, Created: 13})
	got := buf.String()
	for _, want := range []string{
		"tenant " + tenantA, "invoices read:       31", "distinct TINs:       13",
		"records created:     13", "records unchanged:   0",
		"invoices with no buyer TIN: 0", "invoices skipped (TIN not fileable): 0",
		"TINs with more than one name: 0",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("report missing %q in:\n%s", want, got)
		}
	}
}

func richResult() customer.BackfillResult {
	return customer.BackfillResult{
		InvoicesRead: 5, TINs: 2, Created: 1, Existing: 1,
		Skipped: []customer.SkippedInvoice{{InvoiceNumber: "DEMO-2026-6003", TIN: "12345678"}},
		Collisions: []customer.Collision{
			{TIN: "tin1", Names: []string{"A", "B"}, Chosen: "B"},
			{TIN: "tin2", Names: []string{"C", "D"}, Chosen: "Typed", Existing: true},
		},
	}
}

func TestReport_ListsSkippedInvoicesAndCollisions(t *testing.T) {
	var buf bytes.Buffer
	report(&buf, tenantA, false, richResult())
	got := buf.String()
	for _, want := range []string{`DEMO-2026-6003  "12345678"`, `"A" | "B" -> "B"`, `-> kept "Typed"`} {
		if !strings.Contains(got, want) {
			t.Errorf("report missing %q in:\n%s", want, got)
		}
	}
}

func TestReport_DryRunSaysToCreate(t *testing.T) {
	var buf bytes.Buffer
	report(&buf, tenantA, true, richResult())
	got := buf.String()
	if !strings.Contains(got, "records to create:") || strings.Contains(got, "records created:") {
		t.Errorf("dry-run report wording wrong:\n%s", got)
	}
}

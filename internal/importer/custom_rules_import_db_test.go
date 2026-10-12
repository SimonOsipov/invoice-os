package importer

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/google/uuid"

	"github.com/SimonOsipov/invoice-os/internal/customrule"
	"github.com/SimonOsipov/invoice-os/internal/invoice"
	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
)

// TestImport_DB_CustomRulesJudgeImportedInvoices: a client rule failing every row reaches the
// dry-run report and the stored verdicts of a real import; the invoices stay draft.
func TestImport_DB_CustomRulesJudgeImportedInvoices(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()
	tenantID := seedTenant(t, super, "ENGI-14-05 import tenant")
	entityID := seedEntityWithTIN(t, super, tenantID, "ENGI-14-05 import entity", "12345678-0001")
	adminID := uuid.NewString()
	if _, err := super.Exec(ctx, `INSERT INTO memberships (tenant_id, user_id, role) VALUES ($1, $2, 'admin')`, tenantID, adminID); err != nil {
		t.Fatalf("seed admin: %v", err)
	}
	admin := auth.WithIdentity(ctx, auth.Identity{Subject: adminID, Role: "authenticated", TenantID: tenantID})
	rule, err := customrule.NewStore(app, func(context.Context, customrule.CheckInput) error { return nil }).Create(admin,
		customrule.RuleInput{EntityID: &entityID, Type: "required", Target: "buyer.email", Params: json.RawMessage(`{}`),
			Severity: "error", Message: "buyer email needed", Enabled: true})
	if err != nil {
		t.Fatalf("save rule: %v", err)
	}
	key := customrule.Key(rule.ID)

	svc := newTestServiceWithGate(app, invoice.NewGate(invoice.NewStore(app),
		invoice.NewValidator(startInProcess04ForImporter(t, app).URL, impvS2SToken, nil)))
	c := auth.WithIdentity(ctx, auth.Identity{Subject: memberSubject, Role: "authenticated", TenantID: tenantID})
	rows := impvCleanFileFixture()[:3] // IMPV-CLEAN-1 (2 rows) and IMPV-CLEAN-2

	dry, err := svc.Import(c, entityID, "", "", 1, stdMapping, stdHeader, rows, true, "")
	if err != nil {
		t.Fatalf("dry-run Import: %v", err)
	}
	if len(dry.InvoiceViolations) != 2 {
		t.Fatalf("dry-run reports %d invoices with violations, want 2", len(dry.InvoiceViolations))
	}
	for _, iv := range dry.InvoiceViolations {
		if !impvHasViolation(iv.Violations, key) {
			t.Errorf("dry-run %s violations = %+v, want %s", iv.InvoiceNumber, iv.Violations, key)
		}
	}

	if _, err := svc.Import(c, entityID, "", "", 1, stdMapping, stdHeader, rows, false, ""); err != nil {
		t.Fatalf("real Import: %v", err)
	}
	for _, num := range []string{"IMPV-CLEAN-1", "IMPV-CLEAN-2"} {
		status, violations, _ := readInvoiceVerdict(t, super, invoiceIDByNumber(t, super, entityID, num))
		var vs []invoice.Violation
		if err := json.Unmarshal(violations, &vs); err != nil {
			t.Fatalf("unmarshal %s: %v", violations, err)
		}
		if status != "draft" || !impvHasViolation(vs, key) {
			t.Errorf("%s = %s with %+v, want draft carrying %s", num, status, vs, key)
		}
	}
}

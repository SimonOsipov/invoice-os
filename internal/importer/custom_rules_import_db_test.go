package importer

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

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

// savingProxy forwards to a real 04 after saving a fresh rule on every call, so each evaluation is overtaken.
func savingProxy(t *testing.T, inner *httptest.Server, save func()) *httptest.Server {
	t.Helper()
	target, err := url.Parse(inner.URL)
	if err != nil {
		t.Fatal(err)
	}
	proxy := httputil.NewSingleHostReverseProxy(target)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		save()
		proxy.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)
	return srv
}

type twiceChangedFixture struct {
	super, app *pgxpool.Pool
	tenantID   string
	entityID   string
	c          context.Context
	svc        *Service
}

func newTwiceChangedFixture(t *testing.T, label string) twiceChangedFixture {
	t.Helper()
	super, app := dbTestPools(t)
	ctx := context.Background()
	tenantID := seedTenant(t, super, label)
	entityID := seedEntityWithTIN(t, super, tenantID, label+" entity", "12345678-0001")
	adminID := uuid.NewString()
	if _, err := super.Exec(ctx, `INSERT INTO memberships (tenant_id, user_id, role) VALUES ($1, $2, 'admin')`, tenantID, adminID); err != nil {
		t.Fatalf("seed admin: %v", err)
	}
	admin := auth.WithIdentity(ctx, auth.Identity{Subject: adminID, Role: "authenticated", TenantID: tenantID})
	rules := customrule.NewStore(app, func(context.Context, customrule.CheckInput) error { return nil })
	save := func() {
		if _, err := rules.Create(admin, customrule.RuleInput{EntityID: &entityID, Type: "required", Target: "buyer.email",
			Params: json.RawMessage(`{}`), Severity: "error", Message: "saved mid-import", Enabled: true}); err != nil {
			t.Errorf("save rule: %v", err)
		}
	}
	srv := savingProxy(t, startInProcess04ForImporter(t, app), save)
	return twiceChangedFixture{super: super, app: app, tenantID: tenantID, entityID: entityID,
		c:   auth.WithIdentity(ctx, auth.Identity{Subject: memberSubject, Role: "authenticated", TenantID: tenantID}),
		svc: newTestServiceWithGate(app, invoice.NewGate(invoice.NewStore(app), invoice.NewValidator(srv.URL, impvS2SToken, nil)))}
}

// Rules saved during both evaluations leave the invoices drafts; the import reports them as errors, not a 500.
func TestImport_DB_TwiceChangedInvoicesAreReportedNotValidated(t *testing.T) {
	f := newTwiceChangedFixture(t, "ENGI-14-05 twice import")
	rows := impvCleanFileFixture()[:3]

	res, err := f.svc.Import(f.c, f.entityID, "", "", 1, stdMapping, stdHeader, rows, false, "")
	if err != nil {
		t.Fatalf("Import: %v, want no error", err)
	}
	if res.Status != "completed" || res.InvoicesClean != 0 || res.InvoicesWithViolations != 0 {
		t.Errorf("status/clean/violations = %s/%d/%d, want completed/0/0", res.Status, res.InvoicesClean, res.InvoicesWithViolations)
	}
	var named int
	for _, e := range res.Errors {
		if e.Message == unvalidatedMessage && e.InvoiceID != "" && e.RuleKey == "" {
			named++
			if len(e.Rows) == 0 {
				t.Errorf("unvalidated error names no rows: %+v", e)
			}
		}
	}
	if named != res.ReadyInvoices || named != 2 {
		t.Errorf("unvalidated errors = %d, ready invoices = %d, want 2 each: %+v", named, res.ReadyInvoices, res.Errors)
	}
	for _, num := range []string{"IMPV-CLEAN-1", "IMPV-CLEAN-2"} {
		status, _, version := readInvoiceVerdict(t, f.super, invoiceIDByNumber(t, f.super, f.entityID, num))
		if status != "draft" || version != nil {
			t.Errorf("%s = %s stamped %v, want an unstamped draft", num, status, version)
		}
	}
}

// The document path files the draft and records no rule breaks under an empty stamp id.
func TestSupplyInvoiceNumber_DB_TwiceChangedInvoiceStaysAnUnstampedDraft(t *testing.T) {
	f := newTwiceChangedFixture(t, "ENGI-14-05 twice document")
	doc := docSeedDocument(t, f.super, f.tenantID)
	docSeedExtraction(t, f.super, f.tenantID, doc, advGateReading("18.75"))
	var logs bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })

	got, err := f.svc.SupplyInvoiceNumber(f.c, f.entityID, doc, "ENGI-14-TWICE")
	if err != nil {
		t.Fatalf("SupplyInvoiceNumber: %v, want no error", err)
	}
	if got.Status != invoice.StatusDraft || got.RuleSetVersionID != nil {
		t.Errorf("status = %q, rule_set_version_id = %v, want an unstamped draft", got.Status, got.RuleSetVersionID)
	}
	var n int
	if err := f.super.QueryRow(context.Background(), `SELECT count(*) FROM extraction_rule_breaks WHERE tenant_id = $1`, f.tenantID).Scan(&n); err != nil {
		t.Fatalf("count rule breaks: %v", err)
	}
	if n != 0 {
		t.Errorf("rule break rows = %d, want 0", n)
	}
	// An empty-stamp RecordRuleBreaks is a silent no-op, so the guard shows only in the log.
	if out := logs.String(); !strings.Contains(out, "left unvalidated, rules changed twice") || strings.Contains(out, "record rule breaks") {
		t.Errorf("log = %q, want the left-unvalidated warning and no rule-break attempt", out)
	}
}

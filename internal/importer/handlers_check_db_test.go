package importer

import (
	"context"
	"net/http"
	"testing"

	"github.com/SimonOsipov/invoice-os/internal/document"
	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
)

func TestCheckMappingHandler_ACrossTenantDocumentIs404(t *testing.T) {
	super, app := dbTestPools(t)
	docSvc := document.NewService(document.NewStore(app), newMemObjects())

	tenantA := seedTenant(t, super, "check-mapping cross-tenant A")
	tenantB := seedTenant(t, super, "check-mapping cross-tenant B")
	docB := storeDocumentAs(t, docSvc, tenantB, "data.csv", "text/csv",
		csvBody(t, []string{"Invoice No"}, [][]string{{"INV-1"}}))

	opens := 0
	open := func(ctx context.Context, id, rangeHeader string) (document.Document, document.Object, error) {
		opens++
		return docSvc.Open(ctx, id, rangeHeader)
	}
	body := checkReqBody(t, docB.ID, chkOnePlacement)
	stub := &mcStub{enabled: true, resp: mcNouls(map[string]float64{"invoice_number": 1})}

	// Control: the owner reads it, so A's 404 is a refusal, not a fixture that stored nothing.
	idB := auth.Identity{Subject: memberSubject, Role: "authenticated", TenantID: tenantB}
	recB, rawB := doCheckRequest(t, open, stub, nil, &idB, body)
	if recB.Code != http.StatusOK || len(stub.calls) != 1 {
		t.Fatalf("owner: status %d, Ask %d; want 200, 1 (body=%s)", recB.Code, len(stub.calls), rawB)
	}

	idA := auth.Identity{Subject: memberSubject, Role: "authenticated", TenantID: tenantA}
	rec, raw := doCheckRequest(t, open, stub, nil, &idA, body)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("cross-tenant: status = %d, want 404 (body=%s)", rec.Code, raw)
	}
	if got := chkErrorText(t, raw); got != "not found" {
		t.Errorf("error = %q, want %q", got, "not found")
	}
	if len(stub.calls) != 1 {
		t.Errorf("Ask calls = %d, want still 1 -- a 404 must never reach Jev", len(stub.calls))
	}

	opens = 0
	off := &mcStub{enabled: false}
	rec, raw = doCheckRequest(t, open, off, nil, &idA, body)
	if rec.Code != http.StatusOK || string(raw) != chkEmpty {
		t.Errorf("off, cross-tenant: status %d body %q; want 200 %q", rec.Code, raw, chkEmpty)
	}
	if opens != 0 || len(off.calls) != 0 {
		t.Errorf("off: opens %d, Ask %d; want 0, 0", opens, len(off.calls))
	}
}

// The store's read has no tenant filter, so the 404 above is RLS's: a pool that bypasses RLS reads B's document.
func TestCheckMappingHandler_TheCrossTenantRefusalIsRLS(t *testing.T) {
	super, app := dbTestPools(t)
	tenantA := seedTenant(t, super, "check-mapping rls A")
	tenantB := seedTenant(t, super, "check-mapping rls B")
	objects := newMemObjects()
	docB := storeDocumentAs(t, document.NewService(document.NewStore(app), objects), tenantB, "data.csv", "text/csv",
		csvBody(t, []string{"Invoice No"}, [][]string{{"INV-1"}}))
	body := checkReqBody(t, docB.ID, chkOnePlacement)
	idA := auth.Identity{Subject: memberSubject, Role: "authenticated", TenantID: tenantA}

	for _, tc := range []struct {
		name     string
		svc      *document.Service
		wantCode int
		wantAsks int
	}{
		{"invoice_app under RLS", document.NewService(document.NewStore(app), objects), http.StatusNotFound, 0},
		{"control: superuser bypasses RLS", document.NewService(document.NewStore(super), objects), http.StatusOK, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stub := &mcStub{enabled: true, resp: mcNouls(map[string]float64{"invoice_number": 1})}
			rec, raw := doCheckRequest(t, tc.svc.Open, stub, nil, &idA, body)
			if rec.Code != tc.wantCode || len(stub.calls) != tc.wantAsks {
				t.Errorf("status %d, Ask %d; want %d, %d (body=%s)", rec.Code, len(stub.calls), tc.wantCode, tc.wantAsks, raw)
			}
		})
	}
}

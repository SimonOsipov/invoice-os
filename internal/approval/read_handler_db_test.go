package approval

// DB-backed proof that GET /v1/invoices/{id}/approval, wired to the real
// Store.ApprovalRun, answers byte-identically for a validated invoice with no run and
// for an invoice id that plain doesn't exist. decision_handlers_test.go's byte-identity
// test stubs the reader (tautological w.r.t. the database); read_model_db_test.go calls
// the store directly, never through HTTP. Neither covers the wired read path.
//
// Run locally with `DEV_DB_PORT=5433 make test-approvals`; in CI the rls job's gate step
// fails the build on any skip.

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"

	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
)

func TestRunHandler_ValidatedInvoiceWithNoRunOverRealHTTPLeaksNoExistence(t *testing.T) {
	super, app := dbTestPools(t)
	tenantID := policyTenant(t, super, "APPR-14 http-no-run")
	entityID := seedBusinessEntity(t, super, tenantID, "No Run HTTP Corp")
	invoiceID := seedInvoice(t, super, tenantID, entityID, "http-no-run-invoice-1")
	setInvoiceStatus(t, super, invoiceID, "validated")

	subject := uuid.NewString()
	seedMembership(t, super, tenantID, subject, "preparer", "active")

	store := NewStore(app, stubFingerprinter, nil)
	runReadFn := func(ctx context.Context, invoiceID string) (Run, error) { return store.ApprovalRun(ctx, invoiceID) }
	mux := runMux(runReadFn, nil)

	get := func(id string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("GET", "/v1/invoices/"+id+"/approval", nil)
		r = r.WithContext(auth.WithIdentity(r.Context(), auth.Identity{Subject: subject, Role: "authenticated", TenantID: tenantID}))
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, r)
		return rec
	}

	noRunRec := get(invoiceID)
	unknownRec := get(uuid.NewString())

	if noRunRec.Code != http.StatusNotFound {
		t.Fatalf("validated invoice with no run: status = %d, want 404: %s", noRunRec.Code, noRunRec.Body.String())
	}
	if unknownRec.Code != http.StatusNotFound {
		t.Fatalf("unknown invoice id: status = %d, want 404: %s", unknownRec.Code, unknownRec.Body.String())
	}
	if !bytes.Equal(noRunRec.Body.Bytes(), unknownRec.Body.Bytes()) {
		t.Errorf("no-run body = %s, want byte-identical to unknown-id's %s -- the wire must not leak that the invoice exists",
			noRunRec.Body.String(), unknownRec.Body.String())
	}
}

// M5-09-01 (task-250), QA Mode B: adversarial coverage for GetHandler's QR
// render path, added alongside handlers_test.go's own QR PNG acceptance
// tests (AC-3/4/5/6, Stages 2-3) rather than inside that file, to keep this
// add-on's diff separate from the story's RED->GREEN test file. Attacks two
// things the Test Specs table didn't name: whether the render-failure log
// line leaks the qr_payload (which encodes a supplier TIN and an invoice
// amount, internal/submission/mock_script.go:183-189), and the
// QRPayload-non-nil-but-empty-string edge the store can legally hold.
package invoice

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
)

// TestGetHandler_RenderFailureLogDoesNotLeakQRPayloadOrTINOrAmount attacks
// the log call at handlers.go's GetHandler --
// log.ErrorContext(r.Context(), "invoice: render qr", slog.Any("err", err))
// -- directly. A real qr_payload is base64url(JSON{irn,csid,tin,amt,cur})
// (mock_script.go:183-189, :241-247): a SUPPLIER TIN and an invoice AMOUNT.
// If either ever reached this log line, tenant financial identifiers would
// ship straight into logs (and from there, Sentry). qrcode.Render's two
// error paths -- its own blank-payload guard, and rsc.io/qr's over-capacity
// "text too long to encode as QR" -- never echo their input, so this should
// already hold; this test PINS that fact so a future edit to either error
// path (or to the log call itself, e.g. someone "helpfully" adding
// slog.String("payload", *inv.QRPayload) for debuggability) cannot regress
// it silently.
func TestGetHandler_RenderFailureLogDoesNotLeakQRPayloadOrTINOrAmount(t *testing.T) {
	const wantTIN = "87654321-0009"
	const wantAmt = "9999999.99"
	const wantIRN = "INV-SECRET-2026"
	// A realistic-shaped JSON body carrying a distinctive TIN/amount/IRN,
	// padded well past the 2331-char byte-mode capacity ceiling so Render
	// errors (Stage 1 validation addenda #1, task-250).
	payload := `{"irn":"` + wantIRN + `","csid":"deadbeef","tin":"` + wantTIN + `","amt":"` + wantAmt + `","cur":"NGN"}` +
		strings.Repeat("a", 4000)

	id := auth.Identity{Subject: "user-1", Role: "authenticated", TenantID: uuid.NewString()}
	invoiceID := uuid.NewString()
	want := Invoice{ID: invoiceID, Status: StatusAccepted, QRPayload: &payload}
	get := func(ctx context.Context, gotID string) (Invoice, error) {
		return want, nil
	}

	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, nil))

	r := httptest.NewRequest("GET", "/v1/invoices/"+invoiceID, nil)
	r.SetPathValue("id", invoiceID)
	r = r.WithContext(auth.WithIdentity(r.Context(), id))
	rec := httptest.NewRecorder()
	GetHandler(get, adminRoleStub, clearApprovalStub, logger).ServeHTTP(rec, r)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
	}

	logged := buf.String()
	if logged == "" {
		t.Fatal("expected the render failure to be logged, but the log buffer is empty")
	}
	// Negative assertions: none of the sensitive payload content.
	if strings.Contains(logged, wantTIN) {
		t.Errorf("log record contains the supplier TIN %q -- qr_payload (or a value derived from it) is "+
			"leaking into logs shipped to Sentry: %s", wantTIN, logged)
	}
	if strings.Contains(logged, wantAmt) {
		t.Errorf("log record contains the invoice amount %q -- qr_payload is leaking into logs: %s", wantAmt, logged)
	}
	if strings.Contains(logged, wantIRN) {
		t.Errorf("log record contains the payload's IRN field %q: %s", wantIRN, logged)
	}
	if strings.Contains(logged, payload) {
		t.Errorf("log record reproduces the full qr_payload verbatim: %s", logged)
	}
	// Positive assertion: the log record must still identify WHAT failed.
	if !strings.Contains(logged, "render qr") {
		t.Errorf("log record does not identify a QR render failure at all: %s", logged)
	}
}

// TestGetHandler_EmptyStringQRPayloadStillReturns200WithNullAndLogged: the
// store's qr_payload column is nullable text, not a NOT-NULL-with-CHECK --
// so a non-nil *string pointing at "" is a state the store can legally hold
// (e.g. a future migration path, a manual data fix, or a submission script
// bug that writes an empty string instead of leaving the column NULL).
// GetHandler's nil-check (`if inv.QRPayload != nil`) does NOT catch this --
// only qrcode.Render's own blank-payload guard does, one layer down. This
// confirms the same AC-5 contract (200, qr_png_base64: null, logged) holds
// for this edge, not just the "payload present but oversized" case the
// existing TestGetHandler_UnrenderableQRPayloadStillReturns200/
// TestGetHandler_UnrenderableQRPayloadIsLogged already cover.
func TestGetHandler_EmptyStringQRPayloadStillReturns200WithNullAndLogged(t *testing.T) {
	id := auth.Identity{Subject: "user-1", Role: "authenticated", TenantID: uuid.NewString()}
	invoiceID := uuid.NewString()
	empty := ""
	want := Invoice{ID: invoiceID, Status: StatusAccepted, QRPayload: &empty}
	get := func(ctx context.Context, gotID string) (Invoice, error) {
		return want, nil
	}

	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, nil))

	r := httptest.NewRequest("GET", "/v1/invoices/"+invoiceID, nil)
	r.SetPathValue("id", invoiceID)
	r = r.WithContext(auth.WithIdentity(r.Context(), id))
	rec := httptest.NewRecorder()
	GetHandler(get, adminRoleStub, clearApprovalStub, logger).ServeHTTP(rec, r)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 even when qr_payload is a non-nil empty string (body=%s)",
			rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, `"qr_png_base64":null`) {
		t.Errorf("body = %s, want the literal \"qr_png_base64\":null when qr_payload is a non-nil empty string",
			body)
	}
	if buf.Len() == 0 {
		t.Error("expected the render failure (blank payload) to be logged via the injected *slog.Logger, " +
			"but the log buffer is empty")
	}
}

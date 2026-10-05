package gateway

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/SimonOsipov/invoice-os/internal/accountmail"
)

// emailsMux mounts the handlers on the patterns cmd/gateway registers.
func emailsMux(t *testing.T) *http.ServeMux {
	t.Helper()
	tpl, err := MailTemplate("confirmation")
	if err != nil {
		t.Fatalf("MailTemplate(confirmation): %v", err)
	}
	mux := http.NewServeMux()
	mux.Handle("GET /emails/confirmation.html", tpl)
	mux.Handle("GET /emails/mark.png", MailLogo())
	return mux
}

func serveMail(mux http.Handler, method, path string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(method, path, nil))
	return rec
}

func TestMailTemplate_ServesTheComposedTemplate(t *testing.T) {
	want, err := accountmail.Template("confirmation")
	if err != nil {
		t.Fatalf("accountmail.Template: %v", err)
	}
	if len(want) == 0 {
		t.Fatal("accountmail.Template(confirmation) is empty; the comparison below would pass vacuously")
	}

	rec := serveMail(emailsMux(t), http.MethodGet, "/emails/confirmation.html")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if got := rec.Header().Get("Content-Type"); got != "text/html; charset=utf-8" {
		t.Errorf("Content-Type = %q, want %q", got, "text/html; charset=utf-8")
	}
	if !bytes.Equal(rec.Body.Bytes(), want) {
		t.Errorf("body = %d bytes, want accountmail.Template(confirmation) (%d bytes)", rec.Body.Len(), len(want))
	}
}

func TestMailTemplate_UnknownMailIsAnError(t *testing.T) {
	h, err := MailTemplate("nope")
	if err == nil {
		t.Error("MailTemplate(nope) err = nil, want an error")
	}
	if h != nil {
		t.Error("MailTemplate(nope) returned a handler, want nil")
	}
}

func TestMailLogo_ServesTheDesignTokensMark(t *testing.T) {
	want, err := os.ReadFile("../../packages/design-tokens/v2/assets/mark.png")
	if err != nil {
		t.Fatalf("read mark.png: %v", err)
	}
	if len(want) == 0 {
		t.Fatal("mark.png on disk is empty; the comparison below would pass vacuously")
	}

	rec := serveMail(emailsMux(t), http.MethodGet, "/emails/mark.png")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if got := rec.Header().Get("Content-Type"); got != "image/png" {
		t.Errorf("Content-Type = %q, want image/png", got)
	}
	if !bytes.Equal(rec.Body.Bytes(), want) {
		t.Errorf("body = %d bytes, want the %d bytes of mark.png", rec.Body.Len(), len(want))
	}
}

func TestMailLogo_ServesHead(t *testing.T) {
	rec := serveMail(emailsMux(t), http.MethodHead, "/emails/mark.png")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if got := rec.Header().Get("Content-Type"); got != "image/png" {
		t.Errorf("Content-Type = %q, want image/png", got)
	}
	if rec.Body.Len() != 0 {
		t.Errorf("HEAD body = %d bytes, want 0", rec.Body.Len())
	}
}

package gateway

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/SimonOsipov/invoice-os/internal/accountmail"
)

var pngSignature = []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'}

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
	if !bytes.HasPrefix(rec.Body.Bytes(), pngSignature) {
		t.Errorf("body starts % x, want the PNG signature", rec.Body.Bytes()[:min(8, rec.Body.Len())])
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

func TestMailTemplate_ServesHead(t *testing.T) {
	want, err := accountmail.Template("confirmation")
	if err != nil || len(want) == 0 {
		t.Fatalf("accountmail.Template(confirmation) = %d bytes, %v", len(want), err)
	}
	rec := serveMail(emailsMux(t), http.MethodHead, "/emails/confirmation.html")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if got := rec.Header().Get("Content-Type"); got != "text/html; charset=utf-8" {
		t.Errorf("Content-Type = %q, want text/html; charset=utf-8", got)
	}
	if got := rec.Header().Get("Content-Length"); got != strconv.Itoa(len(want)) {
		t.Errorf("Content-Length = %q, want %d", got, len(want))
	}
	if rec.Body.Len() != 0 {
		t.Errorf("HEAD body = %d bytes, want 0", rec.Body.Len())
	}
}

// Only GET and HEAD reach the handlers; the patterns are pinned by TestAccountMailRoutesRegisteredUnconditionally.
func TestMailRoutes_OnlyGetAndHead(t *testing.T) {
	mux := emailsMux(t)
	paths := []string{"/emails/confirmation.html", "/emails/mark.png"}
	for _, path := range paths {
		for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete, http.MethodOptions} {
			rec := serveMail(mux, method, path)
			if rec.Code != http.StatusMethodNotAllowed {
				t.Errorf("%s %s = %d, want 405", method, path, rec.Code)
			}
			if allow := rec.Header().Get("Allow"); !strings.Contains(allow, "GET") {
				t.Errorf("%s %s Allow = %q, want it to name GET", method, path, allow)
			}
			if got := rec.Header().Get("Content-Type"); strings.HasPrefix(got, "text/html") || got == "image/png" {
				t.Errorf("%s %s answered with the asset's content type %q", method, path, got)
			}
		}
	}
}

func TestMailRoutes_UnknownPathIsA404NotAPage(t *testing.T) {
	mux := emailsMux(t)
	paths := []string{
		"/emails/nonexistent.html", "/emails/", "/emails", "/emails/confirmation", "/emails/confirmation.html/",
		"/emails/CONFIRMATION.html", "/emails/mark.PNG", "/emails/mark.png/x", "/emails/../emails/nope.html", "/emails/layout.html",
	}
	for _, path := range paths {
		rec := serveMail(mux, http.MethodGet, path)
		if rec.Code == http.StatusOK {
			t.Errorf("GET %s = 200, want a refusal; the mail routes answer exact paths only", path)
		}
		if rec.Code == http.StatusNotFound {
			if got := rec.Header().Get("Content-Type"); strings.HasPrefix(got, "text/html") || got == "image/png" {
				t.Errorf("GET %s 404 has content type %q, want a non-page body", path, got)
			}
		}
		if strings.Contains(rec.Body.String(), `define "layout"`) || bytes.HasPrefix(rec.Body.Bytes(), pngSignature) {
			t.Errorf("GET %s leaked an asset body on status %d", path, rec.Code)
		}
	}
}

// GoTrue and mail clients are not browsers; the routes grant no cross-origin read.
func TestMailRoutes_CarryNoCORSHeaders(t *testing.T) {
	mux := emailsMux(t)
	for _, path := range []string{"/emails/confirmation.html", "/emails/mark.png"} {
		for _, method := range []string{http.MethodGet, http.MethodHead, http.MethodOptions} {
			req := httptest.NewRequest(method, path, nil)
			req.Header.Set("Origin", "https://app.example")
			req.Header.Set("Access-Control-Request-Method", "GET")
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, req)
			if method != http.MethodOptions && rec.Header().Get("Content-Type") == "" {
				t.Fatalf("%s %s has no Content-Type; the route did not answer, so the CORS assertion below is vacuous", method, path)
			}
			for name := range rec.Header() {
				if strings.HasPrefix(strings.ToLower(name), "access-control-") {
					t.Errorf("%s %s sets %s", method, path, name)
				}
			}
		}
	}
}

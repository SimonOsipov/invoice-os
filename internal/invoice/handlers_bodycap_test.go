package invoice

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
	"github.com/SimonOsipov/invoice-os/internal/platform/jsonbody"
)

const bodyCapEnvelope = `{"error":"request body exceeds the size limit"}`

// countingReader counts the bytes the handler pulls from the body.
type countingReader struct {
	r io.Reader
	n int64
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n += int64(n)
	return n, err
}

type bodyCapCase struct {
	name  string
	limit int64  // jsonbody.DefaultLimit or LineItemsLimit
	valid string // minimal body that reaches the store dependency
	// build returns the handler and a counter of store-dependency calls.
	build func() (http.Handler, *int)
}

func bodyCapCases() []bodyCapCase {
	inv := func() (Invoice, error) { return Invoice{}, nil }
	return []bodyCapCase{
		{"CreateHandler", jsonbody.LineItemsLimit, `{"entity_id":"e","invoice_number":"n"}`, func() (http.Handler, *int) {
			calls := new(int)
			return CreateHandler(func(context.Context, CreateInput) (Invoice, error) { *calls++; return inv() }, nil), calls
		}},
		{"TransitionHandler", jsonbody.DefaultLimit, `{"target":"draft"}`, func() (http.Handler, *int) {
			calls := new(int)
			return TransitionHandler(func(context.Context, string, Status) (Invoice, error) { *calls++; return inv() }, adminRoleStub, nil), calls
		}},
		{"EditHandler", jsonbody.LineItemsLimit, `{"currency":"NGN"}`, func() (http.Handler, *int) {
			calls := new(int)
			return EditHandler(func(context.Context, string, EditInput) (Invoice, error) { *calls++; return inv() }, nil), calls
		}},
		{"KeepAsIsHandler", jsonbody.DefaultLimit, `{"reason":"ok"}`, func() (http.Handler, *int) {
			calls := new(int)
			return KeepAsIsHandler(func(context.Context, string, string) (Invoice, error) { *calls++; return inv() }, nil), calls
		}},
		{"ResolveOutsideHandler", jsonbody.DefaultLimit, `{"reason":"ok"}`, func() (http.Handler, *int) {
			calls := new(int)
			return ResolveOutsideHandler(func(context.Context, string, string) (Invoice, error) { *calls++; return inv() }, nil), calls
		}},
		{"IssueNoteHandler", jsonbody.DefaultLimit, `{"document_type":"credit_note"}`, func() (http.Handler, *int) {
			calls := new(int)
			return IssueNoteHandler(func(context.Context, string, NoteInput) (Invoice, error) { *calls++; return inv() }, nil), calls
		}},
	}
}

// serveBodyCap serves body through h and returns the recorder and bytes read.
func serveBodyCap(h http.Handler, body io.Reader, withIdentity bool) (*httptest.ResponseRecorder, *countingReader) {
	cr := &countingReader{r: body}
	req := httptest.NewRequest(http.MethodPost, "/v1/invoices/"+uuid.NewString(), cr)
	req.SetPathValue("id", uuid.NewString())
	if withIdentity {
		req = req.WithContext(auth.WithIdentity(req.Context(), auth.Identity{Subject: "user-1", Role: "authenticated", TenantID: uuid.NewString()}))
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec, cr
}

func assertBodyCap413(t *testing.T, rec *httptest.ResponseRecorder, calls int) {
	t.Helper()
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 413 (body=%s)", rec.Code, rec.Body.String())
	}
	if got := strings.TrimSpace(rec.Body.String()); got != bodyCapEnvelope {
		t.Errorf("body = %s, want %s", got, bodyCapEnvelope)
	}
	if calls != 0 {
		t.Errorf("store dependency called %d times, want 0", calls)
	}
}

// padded returns valid followed by spaces up to exactly size bytes.
func padded(valid string, size int64) string {
	return valid + strings.Repeat(" ", int(size)-len(valid))
}

func TestInvoiceHandlers_OversizedBodyIs413(t *testing.T) {
	for _, c := range bodyCapCases() {
		t.Run(c.name, func(t *testing.T) {
			h, calls := c.build()
			body := `{"x":"` + strings.Repeat("a", int(c.limit)+64<<10)
			rec, cr := serveBodyCap(h, strings.NewReader(body), true)
			assertBodyCap413(t, rec, *calls)
			if cr.n > c.limit+1 {
				t.Errorf("read %d bytes, want <= %d", cr.n, c.limit+1)
			}
		})
	}
}

func TestInvoiceHandlers_DeclaredContentLengthOverTheLimitIs413(t *testing.T) {
	for _, c := range bodyCapCases() {
		t.Run(c.name, func(t *testing.T) {
			h, calls := c.build()
			body := `{"x":"` + strings.Repeat("a", int(c.limit)+64<<10)
			req := httptest.NewRequest(http.MethodPost, "/v1/invoices/x", strings.NewReader(body))
			req.SetPathValue("id", uuid.NewString())
			req = req.WithContext(auth.WithIdentity(req.Context(), auth.Identity{Subject: "user-1", Role: "authenticated", TenantID: uuid.NewString()}))
			if req.ContentLength != int64(len(body)) {
				t.Fatalf("ContentLength = %d, want %d", req.ContentLength, len(body))
			}
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			assertBodyCap413(t, rec, *calls)
		})
	}
}

func TestInvoiceHandlers_OneByteOverTheLimitIs413(t *testing.T) {
	for _, c := range bodyCapCases() {
		t.Run(c.name, func(t *testing.T) {
			h, calls := c.build()
			rec, _ := serveBodyCap(h, strings.NewReader(padded(c.valid, c.limit+1)), true)
			assertBodyCap413(t, rec, *calls)
		})
	}
}

func TestInvoiceHandlers_BodyAtTheLimitReachesTheStore(t *testing.T) {
	for _, c := range bodyCapCases() {
		t.Run(c.name, func(t *testing.T) {
			h, calls := c.build()
			rec, _ := serveBodyCap(h, strings.NewReader(padded(c.valid, c.limit)), true)
			if rec.Code == http.StatusRequestEntityTooLarge || rec.Code == http.StatusBadRequest {
				t.Fatalf("status = %d, want not 413/400 (body=%s)", rec.Code, rec.Body.String())
			}
			if *calls != 1 {
				t.Errorf("store dependency called %d times, want 1", *calls)
			}
		})
	}
}

func TestInvoiceHandlers_NoIdentityIs401BeforeTheBody(t *testing.T) {
	for _, c := range bodyCapCases() {
		t.Run(c.name, func(t *testing.T) {
			h, calls := c.build()
			body := `{"x":"` + strings.Repeat("a", int(c.limit)+64<<10)
			rec, cr := serveBodyCap(h, strings.NewReader(body), false)
			if rec.Code != http.StatusUnauthorized {
				t.Fatalf("status = %d, want 401 (body=%s)", rec.Code, rec.Body.String())
			}
			if cr.n != 0 {
				t.Errorf("read %d bytes, want 0", cr.n)
			}
			if *calls != 0 {
				t.Errorf("store dependency called %d times, want 0", *calls)
			}
		})
	}
}

func TestTransitionHandler_RoleRefusalPrecedesTheBodyCap(t *testing.T) {
	calls := 0
	h := TransitionHandler(
		func(context.Context, string, Status) (Invoice, error) { calls++; return Invoice{}, nil },
		func(context.Context) (string, error) { return "viewer", nil },
		nil,
	)
	body := `{"x":"` + strings.Repeat("a", int(jsonbody.DefaultLimit)+64<<10)
	rec, cr := serveBodyCap(h, strings.NewReader(body), true)
	want, _, _ := roleRefusal("viewer", nil)
	if rec.Code != want || rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want %d (body=%s)", rec.Code, http.StatusForbidden, rec.Body.String())
	}
	if cr.n != 0 {
		t.Errorf("read %d bytes, want 0", cr.n)
	}
	if calls != 0 {
		t.Errorf("transition called %d times, want 0", calls)
	}
}

func TestInvoiceHandlers_MalformedUnderTheCapIs400(t *testing.T) {
	for _, c := range bodyCapCases() {
		t.Run(c.name, func(t *testing.T) {
			h, calls := c.build()
			rec, _ := serveBodyCap(h, strings.NewReader(`{"x":`), true)
			if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), `"invalid request body"`) {
				t.Fatalf("got %d %s, want 400 invalid request body", rec.Code, rec.Body.String())
			}
			if *calls != 0 {
				t.Errorf("store dependency called %d times, want 0", *calls)
			}
		})
	}
}

func TestInvoiceHandlers_EmptyBodyIs400(t *testing.T) {
	for _, c := range bodyCapCases() {
		t.Run(c.name, func(t *testing.T) {
			h, calls := c.build()
			rec, _ := serveBodyCap(h, strings.NewReader(""), true)
			if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), `"invalid request body"`) {
				t.Fatalf("got %d %s, want 400 invalid request body", rec.Code, rec.Body.String())
			}
			if *calls != 0 {
				t.Errorf("store dependency called %d times, want 0", *calls)
			}
		})
	}
}

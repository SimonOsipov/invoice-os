package portfolio

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
	valid string // minimal body that reaches the store dependency
	// build returns the handler and a counter of store-dependency calls.
	build func() (http.Handler, *int)
}

func bodyCapCases() []bodyCapCase {
	return []bodyCapCase{
		{"CreateHandler", `{"name":"Acme"}`, func() (http.Handler, *int) {
			calls := new(int)
			return CreateHandler(func(context.Context, CreateInput) (Entity, error) { *calls++; return Entity{}, nil }, nil), calls
		}},
		{"UpdateHandler", `{"name":"Acme"}`, func() (http.Handler, *int) {
			calls := new(int)
			return UpdateHandler(func(context.Context, string, UpdateInput) (Entity, error) { *calls++; return Entity{}, nil }, nil), calls
		}},
	}
}

func serveBodyCap(h http.Handler, body io.Reader, withIdentity bool) (*httptest.ResponseRecorder, *countingReader) {
	cr := &countingReader{r: body}
	req := httptest.NewRequest(http.MethodPost, "/v1/entities", cr)
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

func TestPortfolioHandlers_OversizedBodyIs413(t *testing.T) {
	for _, c := range bodyCapCases() {
		t.Run(c.name, func(t *testing.T) {
			h, calls := c.build()
			body := `{"x":"` + strings.Repeat("a", int(jsonbody.DefaultLimit)+64<<10)
			rec, cr := serveBodyCap(h, strings.NewReader(body), true)
			assertBodyCap413(t, rec, *calls)
			if cr.n > jsonbody.DefaultLimit+1 {
				t.Errorf("read %d bytes, want <= %d", cr.n, jsonbody.DefaultLimit+1)
			}
		})
	}
}

func TestPortfolioHandlers_OneByteOverTheLimitIs413(t *testing.T) {
	for _, c := range bodyCapCases() {
		t.Run(c.name, func(t *testing.T) {
			h, calls := c.build()
			rec, _ := serveBodyCap(h, strings.NewReader(padded(c.valid, jsonbody.DefaultLimit+1)), true)
			assertBodyCap413(t, rec, *calls)
		})
	}
}

func TestPortfolioHandlers_BodyAtTheLimitReachesTheStore(t *testing.T) {
	for _, c := range bodyCapCases() {
		t.Run(c.name, func(t *testing.T) {
			h, calls := c.build()
			rec, _ := serveBodyCap(h, strings.NewReader(padded(c.valid, jsonbody.DefaultLimit)), true)
			if rec.Code == http.StatusRequestEntityTooLarge || rec.Code == http.StatusBadRequest {
				t.Fatalf("status = %d, want not 413/400 (body=%s)", rec.Code, rec.Body.String())
			}
			if *calls != 1 {
				t.Errorf("store dependency called %d times, want 1", *calls)
			}
		})
	}
}

func TestPortfolioHandlers_NoIdentityIs401BeforeTheBody(t *testing.T) {
	for _, c := range bodyCapCases() {
		t.Run(c.name, func(t *testing.T) {
			h, calls := c.build()
			body := `{"x":"` + strings.Repeat("a", int(jsonbody.DefaultLimit)+64<<10)
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

func TestPortfolioHandlers_MalformedUnderTheCapIs400(t *testing.T) {
	for _, c := range bodyCapCases() {
		t.Run(c.name, func(t *testing.T) {
			h, calls := c.build()
			rec, _ := serveBodyCap(h, strings.NewReader(`{"x":`), true)
			assertBodyCap400(t, rec, *calls)
		})
	}
}

func TestPortfolioHandlers_EmptyBodyIs400(t *testing.T) {
	for _, c := range bodyCapCases() {
		t.Run(c.name, func(t *testing.T) {
			h, calls := c.build()
			rec, _ := serveBodyCap(h, strings.NewReader(""), true)
			assertBodyCap400(t, rec, *calls)
		})
	}
}

func assertBodyCap400(t *testing.T, rec *httptest.ResponseRecorder, calls int) {
	t.Helper()
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (body=%s)", rec.Code, rec.Body.String())
	}
	if got := strings.TrimSpace(rec.Body.String()); got != `{"error":"invalid request body"}` {
		t.Errorf("body = %s, want invalid request body", got)
	}
	if calls != 0 {
		t.Errorf("store dependency called %d times, want 0", calls)
	}
}

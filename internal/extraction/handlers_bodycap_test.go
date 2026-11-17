// handlers_bodycap_test.go: the body cap on the correction and line-items handlers, driven with
// httptest and injected seams -- no database. The pool is nil on purpose: a field outside
// lockedFields never reads it before the decode, so a nil dereference would panic the test.
//
// Helpers use a bcap* prefix.
package extraction_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/SimonOsipov/invoice-os/internal/extraction"
	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
	"github.com/SimonOsipov/invoice-os/internal/platform/jsonbody"
)

const (
	bcapEnvelope = `{"error":"request body exceeds the size limit"}`
	bcapInvalid  = `{"error":"invalid request body"}`
)

type bcapCountingReader struct {
	r io.Reader
	n int64
}

func (c *bcapCountingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n += int64(n)
	return n, err
}

type bcapCase struct {
	name  string
	limit int64
	valid string
	build func() (http.Handler, *int)
}

func bcapCases() []bcapCase {
	return []bcapCase{
		{"CorrectionHandler", jsonbody.DefaultLimit, `{"value":"Acme","method":"typed"}`, func() (http.Handler, *int) {
			calls := new(int)
			apply := func(context.Context, pgx.Tx, string, string, *string, extraction.CorrectionMethod) (string, error) {
				*calls++
				return "", nil
			}
			record := func(context.Context, pgx.Tx, string, extraction.FieldCorrection) error { *calls++; return nil }
			return extraction.CorrectionHandler(nil, apply, record, nil, nil, nil), calls
		}},
		{"LineItemsHandler", jsonbody.LineItemsLimit, `{"lines":[]}`, func() (http.Handler, *int) {
			calls := new(int)
			apply := func(context.Context, pgx.Tx, string, []extraction.LineItemInput) (string, error) {
				*calls++
				return "", nil
			}
			record := func(context.Context, pgx.Tx, string, extraction.FieldCorrection) error { *calls++; return nil }
			return extraction.LineItemsHandler(nil, apply, record, nil), calls
		}},
	}
}

func bcapServe(h http.Handler, rawID string, body io.Reader, withIdentity bool) (*httptest.ResponseRecorder, *bcapCountingReader) {
	cr := &bcapCountingReader{r: body}
	req := httptest.NewRequest(http.MethodPost, "/", cr)
	req.SetPathValue("id", rawID)
	req.SetPathValue("name", "buyer_name")
	if withIdentity {
		req = req.WithContext(auth.WithIdentity(req.Context(), auth.Identity{Subject: "user-1", Role: "authenticated", TenantID: uuid.NewString()}))
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec, cr
}

func bcapAssert(t *testing.T, rec *httptest.ResponseRecorder, wantCode int, wantBody string, calls int) {
	t.Helper()
	if rec.Code != wantCode {
		t.Fatalf("status = %d, want %d (body=%s)", rec.Code, wantCode, rec.Body.String())
	}
	if got := strings.TrimSpace(rec.Body.String()); got != wantBody {
		t.Errorf("body = %s, want %s", got, wantBody)
	}
	if calls != 0 {
		t.Errorf("dependency called %d times, want 0", calls)
	}
}

func TestExtractionHandlers_OversizedBodyIs413(t *testing.T) {
	for _, c := range bcapCases() {
		t.Run(c.name, func(t *testing.T) {
			h, calls := c.build()
			body := `{"x":"` + strings.Repeat("a", int(c.limit)+64<<10)
			rec, cr := bcapServe(h, uuid.NewString(), strings.NewReader(body), true)
			bcapAssert(t, rec, http.StatusRequestEntityTooLarge, bcapEnvelope, *calls)
			if cr.n > c.limit+1 {
				t.Errorf("read %d bytes, want <= %d", cr.n, c.limit+1)
			}
		})
	}
}

func TestExtractionHandlers_OneByteOverTheLimitIs413(t *testing.T) {
	for _, c := range bcapCases() {
		t.Run(c.name, func(t *testing.T) {
			h, calls := c.build()
			body := c.valid + strings.Repeat(" ", int(c.limit)+1-len(c.valid))
			rec, _ := bcapServe(h, uuid.NewString(), strings.NewReader(body), true)
			bcapAssert(t, rec, http.StatusRequestEntityTooLarge, bcapEnvelope, *calls)
		})
	}
}

func TestCorrectionHandler_NilPoolIsNeverReadBeforeTheCap(t *testing.T) {
	h, calls := bcapCases()[0].build()
	body := `{"x":"` + strings.Repeat("a", int(jsonbody.DefaultLimit)+64<<10)
	rec, _ := bcapServe(h, uuid.NewString(), strings.NewReader(body), true)
	bcapAssert(t, rec, http.StatusRequestEntityTooLarge, bcapEnvelope, *calls)
}

func TestLineItemsHandler_ThousandLinesUnderTheCapStillHitsTheCountClause(t *testing.T) {
	h, calls := bcapCases()[1].build()
	body := `{"lines":[` + strings.TrimSuffix(strings.Repeat(`{"description":"x"},`, 1000), ",") + `]}`
	rec, _ := bcapServe(h, uuid.NewString(), strings.NewReader(body), true)
	bcapAssert(t, rec, http.StatusBadRequest, `{"error":"lines must not exceed 999"}`, *calls)
}

func TestExtractionHandlers_IdentityAndJobIdPrecedeTheBody(t *testing.T) {
	for _, c := range bcapCases() {
		t.Run(c.name, func(t *testing.T) {
			big := `{"x":"` + strings.Repeat("a", int(c.limit)+64<<10)

			h, calls := c.build()
			rec, cr := bcapServe(h, uuid.NewString(), strings.NewReader(big), false)
			bcapAssert(t, rec, http.StatusUnauthorized, `{"error":"unauthorized"}`, *calls)
			if cr.n != 0 {
				t.Errorf("no identity: read %d bytes, want 0", cr.n)
			}

			h, calls = c.build()
			rec, cr = bcapServe(h, "nope", strings.NewReader(big), true)
			bcapAssert(t, rec, http.StatusBadRequest, `{"error":"id must be a well-formed uuid"}`, *calls)
			if cr.n != 0 {
				t.Errorf("malformed id: read %d bytes, want 0", cr.n)
			}
		})
	}
}

func TestExtractionHandlers_MalformedUnderTheCapIs400(t *testing.T) {
	for _, c := range bcapCases() {
		t.Run(c.name, func(t *testing.T) {
			h, calls := c.build()
			rec, _ := bcapServe(h, uuid.NewString(), strings.NewReader(`{"x":`), true)
			bcapAssert(t, rec, http.StatusBadRequest, bcapInvalid, *calls)
		})
	}
}

func TestExtractionHandlers_EmptyBodyIs400(t *testing.T) {
	for _, c := range bcapCases() {
		t.Run(c.name, func(t *testing.T) {
			h, calls := c.build()
			rec, _ := bcapServe(h, uuid.NewString(), strings.NewReader(""), true)
			bcapAssert(t, rec, http.StatusBadRequest, bcapInvalid, *calls)
		})
	}
}

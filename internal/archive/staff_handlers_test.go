// staff_handlers_test.go: the staff bundle handlers on fakes. The path tenant reaches the store untouched;
// parameters are validated before the store; the store's errors map to one status each.
package archive

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"testing"

	"github.com/SimonOsipov/invoice-os/internal/platform/db"
)

func decodeJSON(t *testing.T, raw []byte, into any) {
	t.Helper()
	if err := json.Unmarshal(raw, into); err != nil {
		t.Fatalf("decode %q: %v", raw, err)
	}
}

const staffPathTenant = "22222222-2222-2222-2222-222222222222"

func staffRequest(t *testing.T, query url.Values) *http.Request {
	t.Helper()
	r := httptest.NewRequest(http.MethodGet, "/v1/staff/tenants/"+staffPathTenant+"/evidence-bundle?"+query.Encode(), nil)
	r.SetPathValue("id", staffPathTenant)
	return r
}

// staffStoreErrors is the shared error table of the download and the preview.
var staffStoreErrors = []struct {
	name     string
	err      error
	status   int
	wantBody string
}{
	{"tenant not found", ErrTenantNotFound, http.StatusNotFound, `{"error":"tenant not found"}`},
	{"entity not found", ErrEntityNotFound, http.StatusNotFound, `{"error":"not found"}`},
	{"not staff", db.ErrNotStaff, http.StatusForbidden, `{"error":"forbidden"}`},
	{"over the cap", &TooManyInvoicesError{Count: 3, Limit: 2}, http.StatusBadRequest, `{"error":"3 invoices exceeds the bundle limit of 2"}`},
	{"anything else", errors.New("boom"), http.StatusInternalServerError, `{"error":"internal server error"}`},
}

func TestStaffDownloadHandler_ParametersThenStore(t *testing.T) {
	t.Run("a bad parameter is a 400 and the store is not called", func(t *testing.T) {
		bad := map[string]struct {
			query url.Values
			want  string
		}{
			"entity_id missing": {url.Values{"from": {validFrom}, "to": {validTo}}, "entity_id is required"},
			"from after to":     {url.Values{"entity_id": {validEntityID}, "from": {validTo}, "to": {validFrom}}, "from must not be after to"},
		}
		for name, c := range bad {
			t.Run(name, func(t *testing.T) {
				calls := 0
				h := StaffDownloadHandler(func(context.Context, string, Request, io.Writer, func(string)) error {
					calls++
					return nil
				}, slog.Default())
				rec := httptest.NewRecorder()
				h.ServeHTTP(rec, staffRequest(t, c.query))
				if rec.Code != http.StatusBadRequest || strings.TrimSpace(rec.Body.String()) != `{"error":"`+c.want+`"}` {
					t.Errorf("answer = %d %q, want 400 %q", rec.Code, rec.Body.String(), c.want)
				}
				if calls != 0 {
					t.Errorf("store called %d time(s), want 0", calls)
				}
			})
		}
	})

	for _, c := range staffStoreErrors {
		t.Run(c.name, func(t *testing.T) {
			h := StaffDownloadHandler(func(context.Context, string, Request, io.Writer, func(string)) error { return c.err }, slog.Default())
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, staffRequest(t, validQuery()))
			if rec.Code != c.status || strings.TrimSpace(rec.Body.String()) != c.wantBody {
				t.Errorf("answer = %d %q, want %d %s", rec.Code, rec.Body.String(), c.status, c.wantBody)
			}
		})
	}

	t.Run("success passes the path tenant and the parsed request, and streams a zip", func(t *testing.T) {
		var gotTenant string
		var gotReq Request
		h := StaffDownloadHandler(func(_ context.Context, tenantID string, r Request, w io.Writer, onStart func(string)) error {
			gotTenant, gotReq = tenantID, r
			onStart("bundle.zip")
			_, err := w.Write([]byte("PK"))
			return err
		}, slog.Default())
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, staffRequest(t, validQuery()))
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200 (body %q)", rec.Code, rec.Body.String())
		}
		if got := rec.Header().Get("Content-Type"); got != "application/zip" {
			t.Errorf("Content-Type = %q, want application/zip", got)
		}
		if got := rec.Header().Get("Content-Disposition"); got != "attachment; filename=bundle.zip" {
			t.Errorf("Content-Disposition = %q, want attachment; filename=bundle.zip", got)
		}
		if rec.Body.String() != "PK" {
			t.Errorf("body = %q, want the streamed bytes", rec.Body.String())
		}
		if gotTenant != staffPathTenant {
			t.Errorf("store tenant = %q, want the path tenant %q", gotTenant, staffPathTenant)
		}
		if gotReq.EntityID != validEntityID {
			t.Errorf("store request entity = %q, want %q", gotReq.EntityID, validEntityID)
		}
	})
}

func TestStaffPreviewHandler_StatusMapping(t *testing.T) {
	t.Run("a bad parameter is a 400 and the store is not called", func(t *testing.T) {
		calls := 0
		h := StaffPreviewHandler(func(context.Context, string, Request) (Preview, error) { calls++; return Preview{}, nil }, slog.Default())
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, staffRequest(t, url.Values{"from": {validFrom}, "to": {validTo}}))
		if rec.Code != http.StatusBadRequest || strings.TrimSpace(rec.Body.String()) != `{"error":"entity_id is required"}` {
			t.Errorf("answer = %d %q, want 400 entity_id is required", rec.Code, rec.Body.String())
		}
		if calls != 0 {
			t.Errorf("store called %d time(s), want 0", calls)
		}
	})

	for _, c := range staffStoreErrors {
		t.Run(c.name, func(t *testing.T) {
			h := StaffPreviewHandler(func(context.Context, string, Request) (Preview, error) { return Preview{}, c.err }, slog.Default())
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, staffRequest(t, validQuery()))
			if rec.Code != c.status || strings.TrimSpace(rec.Body.String()) != c.wantBody {
				t.Errorf("answer = %d %q, want %d %s", rec.Code, rec.Body.String(), c.status, c.wantBody)
			}
		})
	}

	t.Run("success is the Preview as JSON for the path tenant", func(t *testing.T) {
		tin := "80000050-0001"
		want := Preview{
			Entity:   manifestEntity{ID: validEntityID, Name: "Staff Co", TIN: &tin},
			Period:   manifestPeriod{From: validFrom, To: validTo, Bounds: "inclusive", Basis: "invoices.created_at"},
			Filename: "ASComply_evidence_Staff-Co_20260101_20260331.zip",
			Counts:   manifestCounts{Invoices: 2},
		}
		var gotTenant string
		h := StaffPreviewHandler(func(_ context.Context, tenantID string, _ Request) (Preview, error) {
			gotTenant = tenantID
			return want, nil
		}, slog.Default())
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, staffRequest(t, validQuery()))
		if rec.Code != http.StatusOK || rec.Header().Get("Content-Type") != "application/json" {
			t.Fatalf("answer = %d %q, want 200 application/json (body %q)", rec.Code, rec.Header().Get("Content-Type"), rec.Body.String())
		}
		var got Preview
		decodeJSON(t, rec.Body.Bytes(), &got)
		if !reflect.DeepEqual(got, want) {
			t.Errorf("body = %+v, want %+v", got, want)
		}
		if gotTenant != staffPathTenant {
			t.Errorf("store tenant = %q, want %q", gotTenant, staffPathTenant)
		}
	})
}

func TestStaffEntitiesHandler_StatusMapping(t *testing.T) {
	list := func(entities []StaffEntity, err error) staffEntitiesFn {
		return func(context.Context, string) ([]StaffEntity, error) { return entities, err }
	}
	serve := func(h http.HandlerFunc) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodGet, "/v1/staff/tenants/"+staffPathTenant+"/entities", nil)
		r.SetPathValue("id", staffPathTenant)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, r)
		return rec
	}

	t.Run("an empty tenant is an empty list, not null", func(t *testing.T) {
		rec := serve(StaffEntitiesHandler(list(nil, nil), slog.Default()))
		if rec.Code != http.StatusOK || strings.TrimSpace(rec.Body.String()) != `{"entities":[]}` {
			t.Errorf("answer = %d %q, want 200 {\"entities\":[]}", rec.Code, rec.Body.String())
		}
	})

	t.Run("a company keeps its null TIN and its field names", func(t *testing.T) {
		tin := "80000051-0001"
		rec := serve(StaffEntitiesHandler(list([]StaffEntity{{ID: "a", Name: "Alpha"}, {ID: "b", Name: "Beta", TIN: &tin}}, nil), slog.Default()))
		want := `{"entities":[{"id":"a","name":"Alpha","tin":null},{"id":"b","name":"Beta","tin":"80000051-0001"}]}`
		if rec.Code != http.StatusOK || strings.TrimSpace(rec.Body.String()) != want {
			t.Errorf("answer = %d %q, want 200 %s", rec.Code, rec.Body.String(), want)
		}
	})

	t.Run("the path tenant reaches the store", func(t *testing.T) {
		var got string
		rec := serve(StaffEntitiesHandler(func(_ context.Context, id string) ([]StaffEntity, error) { got = id; return nil, nil }, slog.Default()))
		if rec.Code != http.StatusOK || got != staffPathTenant {
			t.Errorf("answer = %d, store tenant = %q, want 200 and %q", rec.Code, got, staffPathTenant)
		}
	})

	for _, c := range []struct {
		name   string
		err    error
		status int
		body   string
	}{
		{"tenant not found", ErrTenantNotFound, http.StatusNotFound, `{"error":"tenant not found"}`},
		{"not staff", db.ErrNotStaff, http.StatusForbidden, `{"error":"forbidden"}`},
		{"anything else", errors.New("boom"), http.StatusInternalServerError, `{"error":"internal server error"}`},
	} {
		t.Run(c.name, func(t *testing.T) {
			rec := serve(StaffEntitiesHandler(list(nil, c.err), slog.Default()))
			if rec.Code != c.status || strings.TrimSpace(rec.Body.String()) != c.body {
				t.Errorf("answer = %d %q, want %d %s", rec.Code, rec.Body.String(), c.status, c.body)
			}
		})
	}
}

func TestStaffDownloadHandler_ErrorAfterTheFirstByteAppendsNoJSON(t *testing.T) {
	h := StaffDownloadHandler(func(_ context.Context, _ string, _ Request, w io.Writer, onStart func(string)) error {
		onStart("bundle.zip")
		if _, err := w.Write([]byte("PK-partial")); err != nil {
			return err
		}
		return errors.New("db went away mid-stream")
	}, slog.Default())
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, staffRequest(t, validQuery()))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (the 200 is on the wire at the first byte)", rec.Code)
	}
	if !strings.HasPrefix(rec.Body.String(), "PK-partial") {
		t.Errorf("body = %q, want the streamed bytes first", rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), `{"error"`) {
		t.Errorf("body %q holds a JSON error after the first byte", rec.Body.String())
	}
}

// A malformed escape in the query is a 400 before the store, not a dropped parameter.
func TestStaffHandlers_MalformedQueryStringIsA400(t *testing.T) {
	calls := 0
	handlers := map[string]http.HandlerFunc{
		"download": StaffDownloadHandler(func(context.Context, string, Request, io.Writer, func(string)) error { calls++; return nil }, slog.Default()),
		"preview":  StaffPreviewHandler(func(context.Context, string, Request) (Preview, error) { calls++; return Preview{}, nil }, slog.Default()),
	}
	for name, h := range handlers {
		t.Run(name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet, "/v1/staff/tenants/"+staffPathTenant+"/evidence-bundle?"+validQuery().Encode()+"&junk=%zz", nil)
			r.SetPathValue("id", staffPathTenant)
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, r)
			if rec.Code != http.StatusBadRequest || strings.TrimSpace(rec.Body.String()) != `{"error":"invalid query string"}` {
				t.Errorf("answer = %d %q, want 400 invalid query string", rec.Code, rec.Body.String())
			}
		})
	}
	if calls != 0 {
		t.Errorf("store called %d time(s), want 0", calls)
	}
}

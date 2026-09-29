package invoice

import (
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/SimonOsipov/invoice-os/internal/platform"
	"github.com/SimonOsipov/invoice-os/internal/platform/sentrytest"
)

// mountCheck registers a route that relays a validation failure as invoice's handlers do: log ERROR, answer 502.
func mountCheck(app *platform.App, pattern, validationURL string) {
	app.Mux.HandleFunc(pattern, func(w http.ResponseWriter, r *http.Request) {
		items := []ValidateItem{{Ref: "r1", Invoice: map[string]any{"invoice_number": "INV-1"}}}
		if _, err := NewValidator(validationURL, "tok", nil).Validate(r.Context(), items); err != nil {
			app.Logger.ErrorContext(r.Context(), "check: validate", slog.Any("err", err))
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		w.WriteHeader(http.StatusOK)
	})
}

func closedValidationURL() string {
	srv := httptest.NewServer(http.NotFoundHandler())
	srv.Close()
	return srv.URL
}

func postCheck(t *testing.T, h http.Handler, path string) {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, path, nil))
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("POST %s status = %d, want 502", path, rec.Code)
	}
}

func TestValidatorSentry_RelayedValidationFailureOpensNothing(t *testing.T) {
	cases := []struct {
		name      string
		status    int // 0: validation is unreachable
		counted   bool
		wantCause string
	}{
		{name: "validation 500", status: http.StatusInternalServerError},
		{name: "validation 503", status: http.StatusServiceUnavailable},
		{name: "validation 504", status: http.StatusGatewayTimeout},
		// A 200 that fails the totality check is invoice's own failure.
		{name: "validation 200 without results", status: http.StatusOK, counted: true, wantCause: "covers 0 refs"},
		{name: "validation 400", status: http.StatusBadRequest, counted: true, wantCause: "returned status 400"},
		{name: "validation unreachable", counted: true, wantCause: "connection refused"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			app, rec, want := sentrytest.Boot(t, "invoice")
			target := closedValidationURL()
			if tc.status != 0 {
				fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					w.WriteHeader(tc.status)
					_, _ = w.Write([]byte(`{"error":"fake"}`))
				}))
				t.Cleanup(fake.Close)
				target = fake.URL
			}
			mountCheck(app, "POST /v1/check", target)
			mountCheck(app, "POST /v1/check-control", closedValidationURL())
			h := app.Handler()

			postCheck(t, h, "/v1/check")
			if tc.counted {
				e := rec.One(t, want)
				if len(e.Exception) == 0 || !strings.Contains(e.Exception[len(e.Exception)-1].Value, tc.wantCause) {
					t.Errorf("exception = %+v, want the logged validation error naming %q", e.Exception, tc.wantCause)
				}
				return
			}
			rec.None(t)

			// Positive control: invoice's own 502 on the same recorder is counted.
			postCheck(t, h, "/v1/check-control")
			rec.One(t, want)
		})
	}
}

package platform_test

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/getsentry/sentry-go"

	"github.com/SimonOsipov/invoice-os/internal/platform"
	"github.com/SimonOsipov/invoice-os/internal/platform/sentrytest"
)

const (
	thingPattern = "GET /v1/things/{id}"
	thingUUID    = "3f2b8c1e-6d4a-4b7e-9a1c-2e5f7d9b0a64"
	thingPath    = "/v1/things/" + thingUUID
)

func serve(h http.Handler, method, target string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(method, target, nil))
	return rec
}

func exceptionValues(e *sentry.Event) []string {
	var out []string
	for _, ex := range e.Exception {
		out = append(out, ex.Value)
	}
	return out
}

// assertServerErrorShape checks what every 5xx event carries, logged cause or not.
func assertServerErrorShape(t *testing.T, e *sentry.Event, status string) {
	t.Helper()
	if e.Level != sentry.LevelError {
		t.Errorf("level = %q, want error", e.Level)
	}
	if want := []string{"http-5xx", thingPattern, status}; !slices.Equal(e.Fingerprint, want) {
		t.Errorf("fingerprint = %q, want %q", e.Fingerprint, want)
	}
	if got := e.Tags["http.route"]; got != thingPattern {
		t.Errorf("tag http.route = %q, want %q", got, thingPattern)
	}
	if got := e.Tags["http.status_code"]; got != status {
		t.Errorf("tag http.status_code = %q, want %q", got, status)
	}
	if e.Request == nil {
		t.Fatal("event has no request")
	}
	if e.Request.Method != http.MethodGet {
		t.Errorf("request.method = %q, want GET", e.Request.Method)
	}
	if u, err := url.Parse(e.Request.URL); err != nil || u.Path != thingPath {
		t.Errorf("request.url = %q, want path %q", e.Request.URL, thingPath)
	}
	for _, f := range e.Fingerprint {
		if strings.Contains(f, thingUUID) {
			t.Errorf("fingerprint %q holds the request's UUID", e.Fingerprint)
		}
	}
	for k, v := range e.Tags {
		if strings.Contains(v, thingUUID) {
			t.Errorf("tag %s = %q holds the request's UUID", k, v)
		}
	}
}

func TestServerError_LoggedCauseOpensOneIssue(t *testing.T) {
	app, rec, want := sentrytest.Boot(t, "svc")
	app.Mux.HandleFunc(thingPattern, func(w http.ResponseWriter, r *http.Request) {
		app.Logger.ErrorContext(r.Context(), "thing: load", slog.Any("err", errors.New("db: connection refused")))
		w.WriteHeader(http.StatusInternalServerError)
	})
	serve(app.Handler(), http.MethodGet, thingPath)

	e := rec.One(t, want)
	if v := exceptionValues(e); len(v) == 0 || !strings.Contains(v[len(v)-1], "db: connection refused") {
		t.Errorf("exception values = %q, want the logged cause", v)
	}
	assertServerErrorShape(t, e, "500")
}

func TestServerError_UnloggedStatusOpensOneIssue(t *testing.T) {
	app, rec, want := sentrytest.Boot(t, "svc")
	app.Mux.HandleFunc(thingPattern, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	})
	serve(app.Handler(), http.MethodGet, thingPath)

	e := rec.One(t, want)
	if len(e.Exception) != 0 {
		t.Errorf("exception values = %q, want a message event", exceptionValues(e))
	}
	if !strings.Contains(e.Message, "503") || !strings.Contains(e.Message, thingPattern) {
		t.Errorf("message = %q, want the status and %q", e.Message, thingPattern)
	}
	assertServerErrorShape(t, e, "503")
}

func TestServerError_LastErrorRecordWins(t *testing.T) {
	app, rec, want := sentrytest.Boot(t, "svc")
	app.Mux.HandleFunc(thingPattern, func(w http.ResponseWriter, r *http.Request) {
		app.Logger.ErrorContext(r.Context(), "thing: load", slog.Any("err", errors.New("first")))
		app.Logger.ErrorContext(r.Context(), "thing: load", slog.Any("err", errors.New("second")))
		w.WriteHeader(http.StatusInternalServerError)
	})
	serve(app.Handler(), http.MethodGet, thingPath)

	e := rec.One(t, want)
	if v := exceptionValues(e); !slices.Equal(v, []string{"second"}) {
		t.Errorf("exception values = %q, want [second]", v)
	}
}

func TestServerError_ContextlessLogGivesMessageOnly(t *testing.T) {
	app, rec, want := sentrytest.Boot(t, "svc")
	app.Mux.HandleFunc(thingPattern, func(w http.ResponseWriter, _ *http.Request) {
		app.Logger.Error("thing: load", slog.Any("err", errors.New("db: connection refused")))
		w.WriteHeader(http.StatusInternalServerError)
	})
	serve(app.Handler(), http.MethodGet, thingPath)

	e := rec.One(t, want)
	if len(e.Exception) != 0 {
		t.Errorf("exception values = %q, want none: the log carried no request context", exceptionValues(e))
	}
	if !strings.Contains(e.Message, "500") {
		t.Errorf("message = %q, want the status", e.Message)
	}
}

func TestServerError_WarnRecordGivesMessageOnly(t *testing.T) {
	app, rec, want := sentrytest.Boot(t, "svc")
	app.Mux.HandleFunc(thingPattern, func(w http.ResponseWriter, r *http.Request) {
		app.Logger.WarnContext(r.Context(), "thing: load", slog.Any("err", errors.New("db: connection refused")))
		w.WriteHeader(http.StatusInternalServerError)
	})
	serve(app.Handler(), http.MethodGet, thingPath)

	e := rec.One(t, want)
	if len(e.Exception) != 0 {
		t.Errorf("exception values = %q, want none: only an ERROR record names the cause", exceptionValues(e))
	}
	if !strings.Contains(e.Message, "500") || !strings.Contains(e.Message, thingPattern) {
		t.Errorf("message = %q, want the status and %q", e.Message, thingPattern)
	}
	assertServerErrorShape(t, e, "500")
}

func statusRoute(app *platform.App, logErr bool) {
	app.Mux.HandleFunc("GET /v1/status/{code}", func(w http.ResponseWriter, r *http.Request) {
		code, _ := strconv.Atoi(r.PathValue("code"))
		if logErr {
			app.Logger.ErrorContext(r.Context(), "status: failed", slog.Any("err", errors.New("status: logged cause")))
		}
		w.WriteHeader(code)
	})
}

func TestClientError_OpensNothing(t *testing.T) {
	app, rec, want := sentrytest.Boot(t, "svc")
	statusRoute(app, false)
	h := app.Handler()

	for _, code := range []int{400, 401, 403, 404, 409, 422} {
		if got := serve(h, http.MethodGet, "/v1/status/"+strconv.Itoa(code)).Code; got != code {
			t.Fatalf("status = %d, want %d", got, code)
		}
		rec.None(t)
	}

	serve(h, http.MethodGet, "/v1/status/500")
	rec.One(t, want)
}

func TestServerError_ErrorLogOnSuccessOpensNothing(t *testing.T) {
	app, rec, want := sentrytest.Boot(t, "svc")
	statusRoute(app, true)
	h := app.Handler()

	for _, code := range []int{200, 422} {
		serve(h, http.MethodGet, "/v1/status/"+strconv.Itoa(code))
		rec.None(t)
	}

	serve(h, http.MethodGet, "/v1/status/500")
	rec.One(t, want)
}

func TestServerError_ReportedElsewhereOpensNothing(t *testing.T) {
	app, rec, want := sentrytest.Boot(t, "svc")
	app.Mux.HandleFunc("GET /v1/relay/{marked}", func(w http.ResponseWriter, r *http.Request) {
		if r.PathValue("marked") == "yes" {
			platform.ReportedElsewhere(r.Context())
		}
		w.WriteHeader(http.StatusBadGateway)
	})
	h := app.Handler()

	serve(h, http.MethodGet, "/v1/relay/yes")
	rec.None(t)

	serve(h, http.MethodGet, "/v1/relay/no")
	rec.One(t, want)
}

func TestServerError_ClientGoneOpensNothing(t *testing.T) {
	app, rec, want := sentrytest.Boot(t, "svc")
	entered := make(chan struct{})
	app.Mux.HandleFunc("GET /v1/slow", func(w http.ResponseWriter, r *http.Request) {
		close(entered)
		<-r.Context().Done()
		app.Logger.ErrorContext(r.Context(), "slow: wait", slog.Any("err", r.Context().Err()))
		w.WriteHeader(http.StatusInternalServerError)
	})
	app.Mux.HandleFunc("GET /v1/fail", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})
	h := app.Handler()
	// Signal after the whole chain returns, so a capture made after the mux is already recorded.
	finished := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h.ServeHTTP(w, r)
		if r.URL.Path == "/v1/slow" {
			close(finished)
		}
	}))
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+"/v1/slow", nil)
	if err != nil {
		t.Fatal(err)
	}
	errCh := make(chan error, 1)
	go func() {
		resp, err := srv.Client().Do(req)
		if err == nil {
			resp.Body.Close()
		}
		errCh <- err
	}()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("handler never started")
	}
	cancel()
	if err := <-errCh; err == nil {
		t.Error("client request succeeded, want it cancelled")
	}
	select {
	case <-finished:
	case <-time.After(5 * time.Second):
		t.Fatal("handler chain did not return after the client cancelled")
	}
	rec.None(t)

	serve(h, http.MethodGet, "/v1/fail")
	rec.One(t, want)
}

func TestServerError_HandlerDeadlineOpensOneIssue(t *testing.T) {
	t.Run("handler_deadline", func(t *testing.T) {
		app, rec, want := sentrytest.Boot(t, "svc")
		app.Mux.HandleFunc(thingPattern, func(w http.ResponseWriter, r *http.Request) {
			ctx, cancel := context.WithTimeout(r.Context(), time.Millisecond)
			defer cancel()
			<-ctx.Done()
			app.Logger.ErrorContext(ctx, "thing: load", slog.Any("err", ctx.Err()))
			w.WriteHeader(http.StatusGatewayTimeout)
		})
		serve(app.Handler(), http.MethodGet, thingPath)

		e := rec.One(t, want)
		if v := exceptionValues(e); len(v) == 0 || !strings.Contains(v[len(v)-1], "context deadline exceeded") {
			t.Errorf("exception values = %q, want context deadline exceeded", v)
		}
	})

	// The request's own context expires, as under http.TimeoutHandler: only Canceled means the client left.
	t.Run("request_deadline", func(t *testing.T) {
		app, rec, want := sentrytest.Boot(t, "svc")
		app.Mux.HandleFunc(thingPattern, func(w http.ResponseWriter, r *http.Request) {
			<-r.Context().Done()
			app.Logger.ErrorContext(r.Context(), "thing: load", slog.Any("err", r.Context().Err()))
			w.WriteHeader(http.StatusGatewayTimeout)
		})
		ctx, cancel := context.WithTimeout(context.Background(), time.Millisecond)
		defer cancel()
		app.Handler().ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, thingPath, nil).WithContext(ctx))

		e := rec.One(t, want)
		if v := exceptionValues(e); len(v) == 0 || !strings.Contains(v[len(v)-1], "context deadline exceeded") {
			t.Errorf("exception values = %q, want context deadline exceeded", v)
		}
	})
}

func TestReadyz_FailingCheckOpensOneIssue(t *testing.T) {
	app, rec, want := sentrytest.Boot(t, "svc")
	app.Ready("db", func(context.Context) error { return errors.New("db: ping failed") })
	h := app.Handler()

	if got := serve(h, http.MethodGet, "/readyz").Code; got != http.StatusServiceUnavailable {
		t.Fatalf("/readyz status = %d, want 503", got)
	}
	if got := serve(h, http.MethodGet, "/healthz").Code; got != http.StatusOK {
		t.Fatalf("/healthz status = %d, want 200", got)
	}

	e := rec.One(t, want)
	if !strings.Contains(e.Message, "503") || !strings.Contains(e.Message, "/readyz") {
		t.Errorf("message = %q, want 503 and /readyz", e.Message)
	}
}

func TestPanic_OpensOneIssueWithRequest(t *testing.T) {
	app, rec, want := sentrytest.Boot(t, "svc")
	app.Mux.HandleFunc(thingPattern, func(http.ResponseWriter, *http.Request) { panic("boom") })
	req := httptest.NewRequest(http.MethodGet, thingPath, nil)
	req.Header.Set("X-Request-Id", "req-1")
	resp := httptest.NewRecorder()
	app.Handler().ServeHTTP(resp, req)

	if resp.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", resp.Code)
	}
	var body map[string]string
	if err := json.Unmarshal(resp.Body.Bytes(), &body); err != nil || body["error"] == "" {
		t.Errorf("body = %q, want a JSON error body", resp.Body.String())
	}

	e := rec.One(t, want)
	if e.Level != sentry.LevelFatal {
		t.Errorf("level = %q, want fatal", e.Level)
	}
	if !strings.Contains(e.Message, "boom") {
		t.Errorf("message = %q, want the panic value", e.Message)
	}
	if got := e.Tags["request_id"]; got != "req-1" {
		t.Errorf("tag request_id = %q, want req-1", got)
	}
	if e.Request == nil {
		t.Fatal("panic event has no request")
	}
	if e.Request.Method != http.MethodGet {
		t.Errorf("request.method = %q, want GET", e.Request.Method)
	}
	if u, err := url.Parse(e.Request.URL); err != nil || u.Path != thingPath {
		t.Errorf("request.url = %q, want path %q", e.Request.URL, thingPath)
	}
	if got := e.Request.Headers["X-Request-Id"]; got != "req-1" {
		t.Errorf("request header X-Request-Id = %q, want req-1 (headers %v)", got, e.Request.Headers)
	}
}

func TestPanic_AfterServerErrorOpensOneIssue(t *testing.T) {
	app, rec, want := sentrytest.Boot(t, "svc")
	app.Mux.HandleFunc(thingPattern, func(w http.ResponseWriter, r *http.Request) {
		app.Logger.ErrorContext(r.Context(), "thing: load", slog.Any("err", errors.New("db: connection refused")))
		w.WriteHeader(http.StatusServiceUnavailable)
		panic("boom")
	})
	serve(app.Handler(), http.MethodGet, thingPath)

	e := rec.One(t, want)
	if e.Level != sentry.LevelFatal || !strings.Contains(e.Message, "boom") {
		t.Errorf("event level %q message %q, want the fatal panic event", e.Level, e.Message)
	}
	if len(e.Fingerprint) != 0 {
		t.Errorf("fingerprint = %q, want the panic's default grouping", e.Fingerprint)
	}
}

func TestPanic_AbortHandlerOpensNothing(t *testing.T) {
	app, rec, want := sentrytest.Boot(t, "svc")
	app.Mux.HandleFunc("GET /v1/abort", func(http.ResponseWriter, *http.Request) { panic(http.ErrAbortHandler) })
	app.Mux.HandleFunc("GET /v1/boom", func(http.ResponseWriter, *http.Request) { panic("boom") })
	h := app.Handler()

	got := func() (v any) {
		defer func() { v = recover() }()
		serve(h, http.MethodGet, "/v1/abort")
		return nil
	}()
	if got != http.ErrAbortHandler {
		t.Errorf("recovered %v, want http.ErrAbortHandler re-panicked", got)
	}
	rec.None(t)

	srv := httptest.NewServer(h)
	defer srv.Close()
	if resp, err := srv.Client().Get(srv.URL + "/v1/abort"); err == nil {
		resp.Body.Close()
		t.Errorf("client got status %d, want the connection aborted", resp.StatusCode)
	}
	rec.None(t)

	serve(h, http.MethodGet, "/v1/boom")
	rec.One(t, want)
}

func TestBotProbe_OpensNothing(t *testing.T) {
	app, rec, want := sentrytest.Boot(t, "svc")
	app.Mux.HandleFunc("GET /v1/fail", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})
	h := app.Handler()

	for _, path := range []string{"/wp-login.php", "/.env", "//wordpress/"} {
		code := serve(h, http.MethodGet, path).Code
		if code != http.StatusNotFound && (code < 300 || code > 399) {
			t.Errorf("GET %s status = %d, want 404 or a redirect", path, code)
		}
		rec.None(t)
	}

	serve(h, http.MethodGet, "/v1/fail")
	rec.One(t, want)
}

func TestCapturePanic_ReportsOnce(t *testing.T) {
	_, rec, want := sentrytest.Boot(t, "svc")
	ctx := platform.WithTenantID(platform.WithRequestID(context.Background(), "r1"), "t1")
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer func() {
			if v := recover(); v != nil {
				platform.CapturePanic(ctx, v)
			}
		}()
		panic("bg")
	}()
	<-done

	e := rec.One(t, want)
	if got := e.Tags["request_id"]; got != "r1" {
		t.Errorf("tag request_id = %q, want r1", got)
	}
	if got := e.Tags["tenant_id"]; got != "t1" {
		t.Errorf("tag tenant_id = %q, want t1", got)
	}
	if e.Request != nil {
		t.Errorf("request = %+v, want none off the request path", e.Request)
	}
}

func TestCapture_SentryOffIsNoop(t *testing.T) {
	t.Setenv("SENTRY_DSN", "")
	sentry.CurrentHub().BindClient(nil)
	prev := slog.Default()
	t.Cleanup(func() { slog.SetDefault(prev) })
	app, err := platform.New("svc")
	if err != nil {
		t.Fatal(err)
	}
	app.Mux.HandleFunc("GET /v1/fail", func(w http.ResponseWriter, r *http.Request) {
		app.Logger.ErrorContext(r.Context(), "fail", slog.Any("err", errors.New("fail: cause")))
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte("fail-body"))
	})
	app.Mux.HandleFunc("GET /v1/boom", func(http.ResponseWriter, *http.Request) { panic("boom") })
	h := app.Handler()

	for _, path := range []string{"/v1/fail", "/v1/boom"} {
		if got := serve(h, http.MethodGet, path).Code; got != http.StatusInternalServerError {
			t.Errorf("GET %s status = %d, want 500", path, got)
		}
	}
	// A panic inside the capture would be recovered outside it and append a JSON error body.
	if got := serve(h, http.MethodGet, "/v1/fail").Body.String(); got != "fail-body" {
		t.Errorf("GET /v1/fail body = %q, want the handler's body untouched", got)
	}
	platform.CapturePanic(context.Background(), "bare")
	platform.ReportedElsewhere(context.Background())
}

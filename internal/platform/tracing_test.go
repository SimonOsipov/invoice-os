package platform_test

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/getsentry/sentry-go"
	"github.com/google/uuid"

	"github.com/SimonOsipov/invoice-os/internal/platform"
	"github.com/SimonOsipov/invoice-os/internal/platform/sentrytest"
)

const (
	txTraceID = "0123456789abcdef0123456789abcdef"
	txSpanID  = "fedcba9876543210"
)

// txServe serves one request and returns the response.
func txServe(app *platform.App, req *http.Request) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	app.Handler().ServeHTTP(w, req)
	return w
}

// txWire round-trips an event through JSON, the shape Sentry receives.
func txWire(t *testing.T, e *sentry.Event) map[string]any {
	t.Helper()
	raw, err := json.Marshal(e)
	if err != nil {
		t.Fatalf("marshal event: %v", err)
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("decode event: %v", err)
	}
	return out
}

// txTrace returns the event's contexts.trace object.
func txTrace(t *testing.T, e *sentry.Event) map[string]any {
	t.Helper()
	ctxs, _ := txWire(t, e)["contexts"].(map[string]any)
	tr, _ := ctxs["trace"].(map[string]any)
	if tr == nil {
		t.Fatalf("transaction %q has no contexts.trace", e.Transaction)
	}
	return tr
}

func txData(t *testing.T, e *sentry.Event) map[string]any {
	t.Helper()
	data, _ := txTrace(t, e)["data"].(map[string]any)
	return data
}

// txNames lists the recorded transaction names.
func txNames(rec *sentrytest.Recorder) []string {
	var out []string
	for _, e := range rec.Transactions() {
		out = append(out, e.Transaction)
	}
	return out
}

// oneTransaction fails unless exactly one transaction was recorded.
func oneTransaction(t *testing.T, rec *sentrytest.Recorder) *sentry.Event {
	t.Helper()
	got := rec.Transactions()
	if len(got) != 1 {
		t.Fatalf("recorded %d transactions, want 1: %v", len(got), txNames(rec))
	}
	return got[0]
}

func txAssertShape(t *testing.T, e *sentry.Event, name string, source sentry.TransactionSource) {
	t.Helper()
	if e.Transaction != name {
		t.Errorf("transaction name = %q, want %q", e.Transaction, name)
	}
	if e.TransactionInfo == nil || e.TransactionInfo.Source != source {
		t.Errorf("transaction source = %+v, want %q", e.TransactionInfo, source)
	}
	if op := txTrace(t, e)["op"]; op != "http.server" {
		t.Errorf("transaction op = %v, want http.server", op)
	}
}

func TestTracing_TransactionNamedByRoutePattern(t *testing.T) {
	app, rec, _ := sentrytest.Boot(t, "invoice")
	app.Mux.HandleFunc("GET /v1/things/{id}", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })

	txServe(app, httptest.NewRequest(http.MethodGet, "/v1/things/"+sentrytest.MarkerTIN+"?q="+sentrytest.MarkerTIN+"#"+sentrytest.MarkerIRN, nil))

	e := oneTransaction(t, rec)
	const want = "GET /v1/things/{id}"
	txAssertShape(t, e, want, sentry.SourceRoute)
	dsc := e.GetDynamicSamplingContext()
	if dsc["transaction"] != want {
		t.Errorf("DSC transaction = %q, want %q", dsc["transaction"], want)
	}
	if dsc["sample_rate"] != "1" || dsc["sampled"] != "true" {
		t.Errorf("DSC sample_rate=%q sampled=%q, want 1 and true", dsc["sample_rate"], dsc["sampled"])
	}
	sentrytest.AssertNoLeak(t, rec.Transactions())
}

func TestTracing_UnknownMethodIsOther(t *testing.T) {
	app, rec, _ := sentrytest.Boot(t, "invoice")
	ok := func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }
	app.Mux.HandleFunc("/api/", ok)
	app.Mux.HandleFunc("/v1/things/{id}/", ok)

	txServe(app, httptest.NewRequest("PROPFIND", "/api/x", nil))
	txServe(app, httptest.NewRequest("CONNECT", "/api/", nil))
	// The mux answers a slash redirect for CONNECT with the raw path, marker included.
	txServe(app, httptest.NewRequest("CONNECT", "/v1/things/"+sentrytest.MarkerTIN, nil))
	txServe(app, httptest.NewRequest(sentrytest.MarkerCred, "/api/x", nil))

	got := rec.Transactions()
	if len(got) != 4 {
		t.Fatalf("recorded %d transactions, want 4: %v", len(got), txNames(rec))
	}
	txAssertShape(t, got[0], "OTHER /api/", sentry.SourceRoute)
	if m := txData(t, got[0])["http.request.method"]; m != "OTHER" {
		t.Errorf("http.request.method = %v, want OTHER", m)
	}
	txAssertShape(t, got[1], "CONNECT unmatched", sentry.SourceCustom)
	txAssertShape(t, got[2], "CONNECT unmatched", sentry.SourceCustom)
	txAssertShape(t, got[3], "OTHER /api/", sentry.SourceRoute)
	for _, name := range txNames(rec) {
		if strings.Contains(name, "/api/x") {
			t.Errorf("transaction name %q holds the request path", name)
		}
	}
	sentrytest.AssertNoLeak(t, got)
}

func TestTracing_ClosedMethodSetIsNamedByItsMethod(t *testing.T) {
	for _, method := range []string{
		http.MethodGet, http.MethodHead, http.MethodPost, http.MethodPut,
		http.MethodPatch, http.MethodDelete, http.MethodTrace,
	} {
		t.Run(method, func(t *testing.T) {
			app, rec, _ := sentrytest.Boot(t, "invoice")
			app.Mux.HandleFunc("/api/", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })

			txServe(app, httptest.NewRequest(method, "/api/x", nil))

			e := oneTransaction(t, rec)
			txAssertShape(t, e, method+" /api/", sentry.SourceRoute)
			if m := txData(t, e)["http.request.method"]; m != method {
				t.Errorf("http.request.method = %v, want %s", m, method)
			}
		})
	}
}

func TestTracing_HeadServedByAGetRouteKeepsThePattern(t *testing.T) {
	app, rec, _ := sentrytest.Boot(t, "invoice")
	app.Mux.HandleFunc("GET /v1/x", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })

	txServe(app, httptest.NewRequest(http.MethodHead, "/v1/x", nil))

	e := oneTransaction(t, rec)
	txAssertShape(t, e, "GET /v1/x", sentry.SourceRoute)
	if m := txData(t, e)["http.request.method"]; m != "HEAD" {
		t.Errorf("http.request.method = %v, want HEAD", m)
	}
}

func TestTracing_RedirectsAreNamedByTheirRegisteredPattern(t *testing.T) {
	app, rec, _ := sentrytest.Boot(t, "invoice")
	app.Mux.HandleFunc("GET /v1/things/{id}/", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })

	// Trailing-slash redirect, then path-clean redirect.
	for _, target := range []string{
		"/v1/things/" + sentrytest.MarkerTIN,
		"/v1/things//" + sentrytest.MarkerIRN + "/",
	} {
		if w := txServe(app, httptest.NewRequest(http.MethodGet, target, nil)); w.Code < 300 || w.Code > 399 {
			t.Fatalf("GET %s = %d, want a redirect", target, w.Code)
		}
	}

	got := rec.Transactions()
	if len(got) != 2 {
		t.Fatalf("recorded %d transactions, want 2: %v", len(got), txNames(rec))
	}
	for _, e := range got {
		txAssertShape(t, e, "GET /v1/things/{id}/", sentry.SourceRoute)
	}
	sentrytest.AssertNoLeak(t, got)
}

func TestTracing_UnmatchedRequestIsNamedUnmatched(t *testing.T) {
	app, rec, _ := sentrytest.Boot(t, "invoice")
	app.Mux.HandleFunc("GET /v1/only", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })

	if w := txServe(app, httptest.NewRequest(http.MethodGet, "/wp-login.php?x="+sentrytest.MarkerTIN, nil)); w.Code != http.StatusNotFound {
		t.Fatalf("GET /wp-login.php = %d, want 404", w.Code)
	}
	if w := txServe(app, httptest.NewRequest(http.MethodPost, "/v1/only", nil)); w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST /v1/only = %d, want 405", w.Code)
	}

	got := rec.Transactions()
	if len(got) != 2 {
		t.Fatalf("recorded %d transactions, want 2 (a 404 is kept): %v", len(got), txNames(rec))
	}
	txAssertShape(t, got[0], "GET unmatched", sentry.SourceCustom)
	if code := txData(t, got[0])["http.response.status_code"]; code != float64(http.StatusNotFound) {
		t.Errorf("http.response.status_code = %v, want 404", code)
	}
	if st := txTrace(t, got[0])["status"]; st != "not_found" {
		t.Errorf("404 transaction status = %v, want not_found", st)
	}
	txAssertShape(t, got[1], "POST unmatched", sentry.SourceCustom)
	sentrytest.AssertNoLeak(t, got)
}

func TestTracing_PanickingHandlerKeepsPatternAndOpensOneIssue(t *testing.T) {
	app, rec, want := sentrytest.Boot(t, "invoice")
	app.Mux.HandleFunc("GET /v1/boom/{id}", func(http.ResponseWriter, *http.Request) { panic("boom") })

	if w := txServe(app, httptest.NewRequest(http.MethodGet, "/v1/boom/1", nil)); w.Code != http.StatusInternalServerError {
		t.Fatalf("panicking route = %d, want 500", w.Code)
	}

	e := oneTransaction(t, rec)
	txAssertShape(t, e, "GET /v1/boom/{id}", sentry.SourceRoute)
	if st := txTrace(t, e)["status"]; st != "internal_error" {
		t.Errorf("transaction status = %v, want internal_error", st)
	}
	rec.One(t, want)
}

func TestTracing_AbortHandlerOpensNoIssueAndSendsNoTransaction(t *testing.T) {
	app, rec, _ := sentrytest.Boot(t, "invoice")
	app.Mux.HandleFunc("GET /v1/abort", func(http.ResponseWriter, *http.Request) { panic(http.ErrAbortHandler) })
	app.Mux.HandleFunc("GET /v1/ok", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })

	var recovered any
	func() {
		defer func() { recovered = recover() }()
		txServe(app, httptest.NewRequest(http.MethodGet, "/v1/abort", nil))
	}()
	if recovered != http.ErrAbortHandler {
		t.Fatalf("recovered %v, want http.ErrAbortHandler to unwind to the server", recovered)
	}
	txServe(app, httptest.NewRequest(http.MethodGet, "/v1/ok", nil))

	e := oneTransaction(t, rec)
	if e.Transaction != "GET /v1/ok" {
		t.Errorf("transaction = %q, want GET /v1/ok", e.Transaction)
	}
	rec.None(t)
}

func TestTracing_ProbesAndPreflightsAreNotTransactions(t *testing.T) {
	app, rec, _ := sentrytest.Boot(t, "invoice")
	ok := func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }
	app.Mux.HandleFunc("GET /healthz/fleet", ok)
	app.Mux.HandleFunc("OPTIONS /auth/sign-in", ok)
	app.Mux.HandleFunc("GET /v1/things/{id}", ok)
	app.Mux.HandleFunc("GET /healthzx", ok)

	for _, r := range []struct{ method, target string }{
		{http.MethodGet, "/healthz"},
		{http.MethodGet, "/readyz"},
		{http.MethodGet, "/healthz/fleet"},
		{http.MethodOptions, "/auth/sign-in"},
		{http.MethodOptions, "/v1/things/1"},
		{http.MethodHead, "/healthz"},
		{http.MethodGet, "/healthz/"},
		{http.MethodGet, "/readyz?x=1"},
	} {
		txServe(app, httptest.NewRequest(r.method, r.target, nil))
	}
	txServe(app, httptest.NewRequest(http.MethodGet, "/v1/things/1", nil))
	txServe(app, httptest.NewRequest(http.MethodGet, "/healthzx", nil))
	txServe(app, httptest.NewRequest(http.MethodGet, "/healthzz", nil))

	names := txNames(rec)
	want := []string{"GET /v1/things/{id}", "GET /healthzx", "GET unmatched"}
	if strings.Join(names, "|") != strings.Join(want, "|") {
		t.Fatalf("transactions = %v, want %v", names, want)
	}
}

func TestTracing_ContinuesInboundTraceIgnoresInboundBaggage(t *testing.T) {
	app, rec, _ := sentrytest.Boot(t, "invoice")
	var outbound string
	var spanSeen bool
	app.Mux.HandleFunc("GET /v1/x", func(w http.ResponseWriter, r *http.Request) {
		if span := sentry.SpanFromContext(r.Context()); span != nil {
			spanSeen, outbound = true, span.ToBaggage()
		}
		w.WriteHeader(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodGet, "/v1/x", nil)
	req.Header.Set("sentry-trace", txTraceID+"-"+txSpanID+"-1")
	// Percent-encoded, so the baggage parser accepts it and a read would freeze the DSC from it.
	req.Header.Set("baggage", "sentry-trace_id="+txTraceID+",sentry-transaction=GET%20%2Fv1%2Fx%3Fq%3D"+sentrytest.MarkerTIN+
		",sentry-release="+sentrytest.MarkerIRN+",sentry-environment="+sentrytest.MarkerCred)
	txServe(app, req)

	e := oneTransaction(t, rec)
	tr := txTrace(t, e)
	if tr["trace_id"] != txTraceID {
		t.Errorf("trace_id = %v, want the inbound %s", tr["trace_id"], txTraceID)
	}
	if tr["parent_span_id"] != txSpanID {
		t.Errorf("parent_span_id = %v, want the inbound %s", tr["parent_span_id"], txSpanID)
	}
	dsc := e.GetDynamicSamplingContext()
	if dsc["transaction"] != "GET /v1/x" {
		t.Errorf("DSC transaction = %q, want GET /v1/x", dsc["transaction"])
	}
	if dsc["environment"] != "production" {
		t.Errorf("DSC environment = %q, want production", dsc["environment"])
	}
	// The baggage the handler would forward is the transaction's own DSC.
	if !spanSeen {
		t.Fatal("handler context carries no span")
	}
	for _, leaked := range []string{"sentry-transaction=GET%20%2Fv1%2Fx%3Fq", sentrytest.MarkerTIN, sentrytest.MarkerIRN, sentrytest.MarkerCred} {
		if strings.Contains(outbound, leaked) {
			t.Errorf("span baggage %q carries inbound %q", outbound, leaked)
		}
	}
	sentrytest.AssertNoLeak(t, rec.Transactions())
}

func TestTracing_InboundSampledFlagIsIgnored(t *testing.T) {
	app, rec, _ := sentrytest.Boot(t, "invoice")
	app.Mux.HandleFunc("GET /v1/x", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })

	req := httptest.NewRequest(http.MethodGet, "/v1/x", nil)
	req.Header.Set("sentry-trace", txTraceID+"-"+txSpanID+"-0")
	txServe(app, req)

	e := oneTransaction(t, rec)
	if id := txTrace(t, e)["trace_id"]; id != txTraceID {
		t.Errorf("trace_id = %v, want the inbound %s", id, txTraceID)
	}
}

func TestTracing_MalformedSentryTraceStartsANewTrace(t *testing.T) {
	app, rec, _ := sentrytest.Boot(t, "invoice")
	app.Mux.HandleFunc("GET /v1/x", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })

	for _, header := range []string{
		"garbage",
		strings.Repeat("ab", 4096),
		txTraceID + "-" + txSpanID + "-2",
		txTraceID + "-" + txSpanID + "-1-junk",
		txTraceID + "-" + txSpanID + "-1" + strings.Repeat("0", 8192),
	} {
		req := httptest.NewRequest(http.MethodGet, "/v1/x", nil)
		req.Header.Set("sentry-trace", header)
		txServe(app, req)
	}

	got := rec.Transactions()
	if len(got) != 5 {
		t.Fatalf("recorded %d transactions, want 5: %v", len(got), txNames(rec))
	}
	for i, e := range got {
		id, _ := txTrace(t, e)["trace_id"].(string)
		if len(id) != 32 || id == txTraceID {
			t.Errorf("transaction %d trace_id = %q, want a fresh 32-hex id", i, id)
		}
	}
}

func TestTracing_TransactionShapeAndNoRequestData(t *testing.T) {
	app, rec, _ := sentrytest.Boot(t, "invoice")
	app.Mux.HandleFunc("POST /v1/things/{id}", func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusOK)
	})

	// MarkerCred has no value pattern for scrubbing to mask, so it alone proves the span never held the query, body or Referer.
	body := `{"buyer_tin":"` + sentrytest.MarkerTIN + `","total":"` + sentrytest.MarkerAmt + `","irn":"` + sentrytest.MarkerIRN + `","note":"` + sentrytest.MarkerCred + `-body"}`
	req := httptest.NewRequest(http.MethodPost, "/v1/things/"+uuid.NewString()+"?q="+sentrytest.MarkerTIN+"&k="+sentrytest.MarkerCred+"-query#"+sentrytest.MarkerIRN, strings.NewReader(body))
	tenant := uuid.NewString()
	for k, v := range map[string]string{
		"X-Tenant-ID":     tenant,
		"X-Request-Id":    "req-shape-1",
		"X-S2S-Token":     sentrytest.MarkerCred + "-s2s",
		"X-Gateway-Token": sentrytest.MarkerCred + "-gw",
		"Authorization":   "Bearer " + sentrytest.MarkerCred + "-authz",
		"Cookie":          "s=" + sentrytest.MarkerCred + "-cookie",
		"Referer":         "https://app.example/things?q=" + sentrytest.MarkerTIN + "&k=" + sentrytest.MarkerCred + "-ref",
		"Content-Type":    "application/json",
	} {
		req.Header.Set(k, v)
	}
	txServe(app, req)

	e := oneTransaction(t, rec)
	if st := txTrace(t, e)["status"]; st != "ok" {
		t.Errorf("transaction status = %v, want ok", st)
	}
	data := txData(t, e)
	if data["http.request.method"] != "POST" {
		t.Errorf("http.request.method = %v, want POST", data["http.request.method"])
	}
	if data["http.response.status_code"] != float64(http.StatusOK) {
		t.Errorf("http.response.status_code = %v, want 200", data["http.response.status_code"])
	}
	if e.Tags["request_id"] != "req-shape-1" || e.Tags["tenant_id"] != tenant {
		t.Errorf("tags = %v, want request_id req-shape-1 and tenant_id %s", e.Tags, tenant)
	}
	if e.Request != nil {
		t.Errorf("transaction carries request data: %+v", e.Request)
	}
	sentrytest.AssertNoLeak(t, rec.Transactions())
}

func TestTracing_ResponseStatusCodeMatchesTheWire(t *testing.T) {
	for _, tc := range []struct {
		name    string
		handler http.HandlerFunc
		status  string
		panics  bool
	}{
		{"handler writes nothing", func(http.ResponseWriter, *http.Request) {}, "ok", false},
		{"body without a header call", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("x")) }, "ok", false},
		{"5xx without a panic", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusServiceUnavailable) }, "unavailable", false},
		{"panic after the header is written", func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusCreated)
			panic("late")
		}, "internal_error", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			app, rec, _ := sentrytest.Boot(t, "invoice")
			app.Mux.HandleFunc("GET /v1/x", tc.handler)

			w := txServe(app, httptest.NewRequest(http.MethodGet, "/v1/x", nil))

			e := oneTransaction(t, rec)
			if got := txData(t, e)["http.response.status_code"]; got != float64(w.Code) {
				t.Errorf("http.response.status_code = %v, client saw %d", got, w.Code)
			}
			if st := txTrace(t, e)["status"]; st != tc.status {
				t.Errorf("transaction status = %v, want %s", st, tc.status)
			}
			if tc.panics {
				if w.Code != http.StatusCreated {
					t.Errorf("client saw %d, want the 201 already written", w.Code)
				}
				if n := len(rec.Events()); n != 1 {
					t.Errorf("recorded %d issues, want 1", n)
				}
			}
		})
	}
}

func TestTracing_NoEmptyTags(t *testing.T) {
	app, rec, _ := sentrytest.Boot(t, "invoice")
	app.Mux.HandleFunc("GET /v1/x", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })

	txServe(app, httptest.NewRequest(http.MethodGet, "/v1/x", nil))

	e := oneTransaction(t, rec)
	if e.Tags["request_id"] == "" {
		t.Errorf("tags = %v, want a request_id", e.Tags)
	}
	for k, v := range e.Tags {
		if v == "" {
			t.Errorf("tag %q is empty", k)
		}
	}
	if _, ok := e.Tags["tenant_id"]; ok {
		t.Errorf("tags = %v, want no tenant_id without a tenant", e.Tags)
	}
}

func TestTracing_LogsCarryTheTransactionTraceID(t *testing.T) {
	app, rec, _ := sentrytest.Boot(t, "invoice")
	var inHandler *sentry.Span
	app.Mux.HandleFunc("GET /v1/x", func(w http.ResponseWriter, r *http.Request) {
		inHandler = sentry.SpanFromContext(r.Context())
		app.Logger.InfoContext(r.Context(), "inside")
		w.WriteHeader(http.StatusOK)
	})

	txServe(app, httptest.NewRequest(http.MethodGet, "/v1/x", nil))

	e := oneTransaction(t, rec)
	want, _ := txTrace(t, e)["trace_id"].(string)
	if want == "" {
		t.Fatal("transaction has no trace_id")
	}
	wantSpan, _ := txTrace(t, e)["span_id"].(string)
	l := oneLogWithBody(t, rec, "inside")
	if got := l.TraceID.String(); got != want {
		t.Errorf("log trace id = %s, want the transaction's %s", got, want)
	}
	if got := l.SpanID.String(); wantSpan == "" || got != wantSpan {
		t.Errorf("log span id = %s, want the transaction's %q", got, wantSpan)
	}
	// The log resolves its ids from the hub scope too, so only the handler's own context proves the span was passed on.
	if inHandler == nil || inHandler.SpanID.String() != wantSpan || inHandler.TraceID.String() != want {
		t.Errorf("handler context span = %+v, want the transaction's span %s in trace %s", inHandler, wantSpan, want)
	}
}

func TestTracing_PassThroughWithoutAClient(t *testing.T) {
	sentry.CurrentHub().BindClient(nil)
	t.Setenv("SENTRY_DSN", "")
	prevLogger := slog.Default()
	t.Cleanup(func() {
		sentry.CurrentHub().BindClient(nil)
		slog.SetDefault(prevLogger)
	})
	app, err := platform.New("invoice")
	if err != nil {
		t.Fatalf("platform.New: %v", err)
	}
	var hubInHandler *sentry.Hub
	var spanInHandler *sentry.Span
	app.Mux.HandleFunc("GET /v1/x", func(w http.ResponseWriter, r *http.Request) {
		hubInHandler = sentry.GetHubFromContext(r.Context())
		spanInHandler = sentry.SpanFromContext(r.Context())
		w.WriteHeader(http.StatusCreated)
	})

	w := txServe(app, httptest.NewRequest(http.MethodGet, "/v1/x", nil))

	if w.Code != http.StatusCreated {
		t.Errorf("status = %d, want 201", w.Code)
	}
	if hubInHandler != nil {
		t.Error("handler context carries a hub although no client is bound")
	}
	if spanInHandler != nil {
		t.Error("handler context carries a span although no client is bound")
	}
}

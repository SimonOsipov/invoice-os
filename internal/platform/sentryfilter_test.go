package platform

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/getsentry/sentry-go"
	sentryhttp "github.com/getsentry/sentry-go/http"
	sentryhttpclient "github.com/getsentry/sentry-go/httpclient"
	"github.com/google/uuid"
)

// Markers are URL- and JSON-safe, so encoding cannot disguise a leak. Keep the
// literals here: stack-frame context lines near a capture site must not hold them.
const (
	markerTIN  = "87654321-0009"
	markerAmt  = "9999999.99"
	markerIRN  = "INV-SECRET-2026"
	markerCred = "cred-SECRET-4411"
)

var leakMarkers = []string{markerTIN, markerAmt, markerIRN, markerCred}

const filterTestDSN = "https://public@example.com/1"

// filteredClient binds the client initSentry builds, re-pointed at a recording
// transport, so a test fails if initSentry stops installing the hooks.
func filteredClient(t *testing.T, tracing bool) *mockTransport {
	t.Helper()
	sentry.CurrentHub().BindClient(nil)
	if err := initSentry(Config{Service: "svc", Environment: "test", SentryEnvironment: "test", SentryDSN: filterTestDSN}); err != nil {
		t.Fatalf("initSentry: %v", err)
	}
	installed := sentry.CurrentHub().Client()
	if installed == nil {
		t.Fatal("initSentry bound no client")
	}
	opts := installed.Options()
	installed.Close()

	mt := &mockTransport{}
	opts.Transport = mt
	if tracing {
		opts.EnableTracing = true
		opts.TracesSampleRate = 1
	} else {
		opts.EnableTracing = false
		opts.TracesSampleRate = 0
	}
	client, err := sentry.NewClient(opts)
	if err != nil {
		t.Fatalf("new client from initSentry's options: %v", err)
	}
	sentry.CurrentHub().BindClient(client)
	t.Cleanup(func() { sentry.CurrentHub().BindClient(nil) })
	return mt
}

// leakPaths lists the JSON paths under v whose key or string value holds marker.
func leakPaths(v any, path, marker string) []string {
	var out []string
	switch x := v.(type) {
	case map[string]any:
		for k, e := range x {
			p := path + "." + k
			if strings.Contains(k, marker) {
				out = append(out, p+" (key)")
			}
			out = append(out, leakPaths(e, p, marker)...)
		}
	case []any:
		for i, e := range x {
			out = append(out, leakPaths(e, path+"["+strconv.Itoa(i)+"]", marker)...)
		}
	case string:
		if strings.Contains(x, marker) {
			out = append(out, path)
		}
	}
	return out
}

// assertNoLeak fails on any marker in any recorded event's wire JSON or its
// envelope trace header (the dynamic sampling context).
func assertNoLeak(t *testing.T, events []*sentry.Event) {
	t.Helper()
	if len(events) == 0 {
		t.Fatal("no event recorded; nothing to check for leaks")
	}
	for i, e := range events {
		kind := e.Type
		if kind == "" {
			kind = "error"
		}
		raw, err := json.Marshal(e)
		if err != nil {
			t.Fatalf("marshal event %d (%s): %v", i, kind, err)
		}
		var decoded any
		if err := json.Unmarshal(raw, &decoded); err != nil {
			t.Fatalf("decode event %d (%s): %v", i, kind, err)
		}
		dsc, err := json.Marshal(e.GetDynamicSamplingContext())
		if err != nil {
			t.Fatalf("marshal DSC of event %d (%s): %v", i, kind, err)
		}
		for _, m := range leakMarkers {
			if strings.Contains(string(raw), m) {
				t.Errorf("%s event %d carries marker %q at %v", kind, i, m, leakPaths(decoded, "event", m))
			}
			if strings.Contains(string(dsc), m) {
				t.Errorf("%s event %d envelope trace header (DSC) carries marker %q: %s", kind, i, m, dsc)
			}
		}
	}
}

func eventsOfType(mt *mockTransport, typ string) []*sentry.Event {
	var out []*sentry.Event
	for _, e := range mt.captured() {
		if e.Type == typ {
			out = append(out, e)
		}
	}
	return out
}

// oneEvent returns the single recorded event of typ ("" is an error event).
func oneEvent(t *testing.T, mt *mockTransport, typ string) *sentry.Event {
	t.Helper()
	got := eventsOfType(mt, typ)
	if len(got) != 1 {
		t.Fatalf("recorded %d events of type %q, want 1 (all: %d)", len(got), typ, len(mt.captured()))
	}
	return got[0]
}

func exceptionValue(t *testing.T, e *sentry.Event) string {
	t.Helper()
	if len(e.Exception) == 0 {
		t.Fatal("error event has no exception")
	}
	return e.Exception[len(e.Exception)-1].Value
}

// serveThroughSentry runs h behind the real sentryhttp middleware.
func serveThroughSentry(r *http.Request, h http.HandlerFunc) {
	sentryhttp.New(sentryhttp.Options{}).Handle(h).ServeHTTP(httptest.NewRecorder(), r)
}

// readAndCapture reads the body first: SetRequest tees it only as it is read.
func readAndCapture(msg string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.ReadAll(r.Body)
		sentry.GetHubFromContext(r.Context()).CaptureException(errors.New(msg))
	}
}

func readAndReturn(w http.ResponseWriter, r *http.Request) {
	_, _ = io.ReadAll(r.Body)
	w.WriteHeader(http.StatusOK)
}

func queryRequest() *http.Request {
	return httptest.NewRequest(http.MethodGet, "/v1/invoices?q="+markerTIN+"&token="+markerCred, nil)
}

func TestSentryFilter_NoQueryStringLeaves(t *testing.T) {
	t.Run("error_event", func(t *testing.T) {
		mt := filteredClient(t, false)
		serveThroughSentry(queryRequest(), readAndCapture("invoice search failed"))

		ev := oneEvent(t, mt, "")
		if got := exceptionValue(t, ev); got != "invoice search failed" {
			t.Errorf("exception value = %q, want the anchor to survive", got)
		}
		if ev.Request == nil {
			t.Fatal("error event has no request; the handler's scope request was not applied")
		}
		if !strings.HasSuffix(ev.Request.URL, "/v1/invoices") {
			t.Errorf("request.url = %q, want it to end /v1/invoices", ev.Request.URL)
		}
		if ev.Request.QueryString != "" {
			t.Errorf("request.query_string = %q, want empty", ev.Request.QueryString)
		}
		assertNoLeak(t, mt.captured())
	})

	t.Run("transaction", func(t *testing.T) {
		mt := filteredClient(t, true)
		serveThroughSentry(queryRequest(), readAndReturn)

		tx := oneEvent(t, mt, "transaction")
		if tx.Transaction != "GET /v1/invoices" {
			t.Errorf("transaction = %q, want GET /v1/invoices", tx.Transaction)
		}
		if tx.Request != nil && tx.Request.QueryString != "" {
			t.Errorf("transaction request.query_string = %q, want empty", tx.Request.QueryString)
		}
		assertNoLeak(t, mt.captured())
	})

	t.Run("outbound_span", func(t *testing.T) {
		mt := filteredClient(t, true)
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {}))
		defer srv.Close()
		client := &http.Client{Transport: sentryhttpclient.NewSentryRoundTripper(nil)}

		serveThroughSentry(httptest.NewRequest(http.MethodGet, "/v1/invoices", nil), func(w http.ResponseWriter, r *http.Request) {
			req, err := http.NewRequestWithContext(r.Context(), http.MethodGet, srv.URL+"/v1/lookup?q="+markerTIN+"#"+markerIRN, nil)
			if err != nil {
				t.Errorf("build outbound request: %v", err)
				return
			}
			resp, err := client.Do(req)
			if err != nil {
				t.Errorf("outbound call: %v", err)
				return
			}
			_ = resp.Body.Close()
		})

		tx := oneEvent(t, mt, "transaction")
		var outbound []*sentry.Span
		for _, s := range tx.Spans {
			if s.Op == "http.client" {
				outbound = append(outbound, s)
			}
		}
		if len(outbound) != 1 {
			t.Fatalf("transaction has %d http.client spans, want 1", len(outbound))
		}
		span := outbound[0]
		for _, k := range []string{"http.query", "http.fragment"} {
			if v, ok := span.Data[k]; ok {
				t.Errorf("outbound span data holds %s = %v, want the key absent", k, v)
			}
		}
		if !strings.Contains(span.Description, "/v1/lookup") {
			t.Errorf("outbound span description = %q, want it to still name /v1/lookup", span.Description)
		}
		assertNoLeak(t, mt.captured())
	})

	t.Run("event_url", func(t *testing.T) {
		mt := filteredClient(t, false)
		sentry.CurrentHub().Clone().CaptureEvent(&sentry.Event{
			Level:   sentry.LevelError,
			Message: "event url anchor",
			Request: &sentry.Request{Method: http.MethodGet, URL: "https://h/v1/invoices?q=" + markerTIN},
		})

		ev := oneEvent(t, mt, "")
		if ev.Message != "event url anchor" {
			t.Errorf("message = %q, want the anchor", ev.Message)
		}
		if ev.Request == nil {
			t.Fatal("event lost its request")
		}
		if ev.Request.URL != "https://h/v1/invoices" {
			t.Errorf("request.url = %q, want https://h/v1/invoices", ev.Request.URL)
		}
		assertNoLeak(t, mt.captured())
	})

	// The slog bridge sets Transaction from a record's "transaction" attribute.
	t.Run("event_transaction_field", func(t *testing.T) {
		mt := filteredClient(t, false)
		sentry.CurrentHub().Clone().CaptureEvent(&sentry.Event{
			Level:       sentry.LevelError,
			Message:     "transaction field anchor",
			Transaction: "GET /v1/invoices?q=" + markerTIN,
		})

		ev := oneEvent(t, mt, "")
		if ev.Transaction != "GET /v1/invoices" {
			t.Errorf("transaction = %q, want GET /v1/invoices", ev.Transaction)
		}
		assertNoLeak(t, mt.captured())
	})

	t.Run("breadcrumb", func(t *testing.T) {
		mt := filteredClient(t, false)
		hub := sentry.CurrentHub().Clone()
		hub.AddBreadcrumb(&sentry.Breadcrumb{
			Category: "http",
			Message:  "GET /v1/invoices?q=" + markerTIN,
			Data: map[string]any{
				"url":           "https://h/p?q=" + markerTIN,
				"http.query":    "q=" + markerTIN,
				"http.fragment": markerIRN,
			},
		}, nil)
		hub.CaptureException(errors.New("breadcrumb anchor"))

		ev := oneEvent(t, mt, "")
		if len(ev.Breadcrumbs) != 1 {
			t.Fatalf("event has %d breadcrumbs, want 1", len(ev.Breadcrumbs))
		}
		bc := ev.Breadcrumbs[0]
		if bc.Message != "GET /v1/invoices" {
			t.Errorf("breadcrumb message = %q, want GET /v1/invoices", bc.Message)
		}
		if got := bc.Data["url"]; got != "https://h/p" {
			t.Errorf("breadcrumb data url = %v, want https://h/p", got)
		}
		for _, k := range []string{"http.query", "http.fragment"} {
			if v, ok := bc.Data[k]; ok {
				t.Errorf("breadcrumb data holds %s = %v, want the key absent", k, v)
			}
		}
		assertNoLeak(t, mt.captured())
	})

	t.Run("tag_value", func(t *testing.T) {
		mt := filteredClient(t, false)
		hub := sentry.CurrentHub().Clone()
		hub.Scope().SetTag("path", "/v1/invoices?q="+markerTIN)
		hub.CaptureException(errors.New("tag anchor"))

		ev := oneEvent(t, mt, "")
		if got := ev.Tags["path"]; got != "/v1/invoices" {
			t.Errorf("tag path = %q, want /v1/invoices", got)
		}
		assertNoLeak(t, mt.captured())
	})

	t.Run("nil_request", func(t *testing.T) {
		mt := filteredClient(t, false)
		hub := sentry.CurrentHub().Clone()
		hub.Scope().SetTag("job", "reconcile")
		hub.CaptureException(errors.New("no request anchor"))

		ev := oneEvent(t, mt, "")
		if got := exceptionValue(t, ev); got != "no request anchor" {
			t.Errorf("exception value = %q, want it unchanged", got)
		}
		if got := ev.Tags["job"]; got != "reconcile" {
			t.Errorf("tag job = %q, want it unchanged", got)
		}
		if ev.Request != nil {
			t.Errorf("request = %+v, want it to stay nil", ev.Request)
		}
		assertNoLeak(t, mt.captured())
	})

	t.Run("transaction_no_request", func(t *testing.T) {
		mt := filteredClient(t, true)
		ctx := sentry.SetHubOnContext(context.Background(), sentry.CurrentHub().Clone())
		sentry.StartTransaction(ctx, "reconcile.run").Finish()

		tx := oneEvent(t, mt, "transaction")
		if tx.Transaction != "reconcile.run" {
			t.Errorf("transaction = %q, want reconcile.run", tx.Transaction)
		}
		if tx.Request != nil {
			t.Errorf("request = %+v, want it to stay nil", tx.Request)
		}
		assertNoLeak(t, mt.captured())
	})

	t.Run("breadcrumb_nil_data", func(t *testing.T) {
		mt := filteredClient(t, false)
		hub := sentry.CurrentHub().Clone()
		hub.AddBreadcrumb(&sentry.Breadcrumb{Category: "nav", Message: "GET /p?q=" + markerTIN}, nil)
		hub.CaptureException(errors.New("nil data anchor"))

		ev := oneEvent(t, mt, "")
		if len(ev.Breadcrumbs) != 1 {
			t.Fatalf("event has %d breadcrumbs, want 1", len(ev.Breadcrumbs))
		}
		if got := ev.Breadcrumbs[0].Message; got != "GET /p" {
			t.Errorf("breadcrumb message = %q, want GET /p", got)
		}
		assertNoLeak(t, mt.captured())
	})

	t.Run("trace_data_not_a_map", func(t *testing.T) {
		mt := filteredClient(t, false)
		hub := sentry.CurrentHub().Clone()
		// A scope processor runs before BeforeSend, so the hook sees this value.
		hub.Scope().AddEventProcessor(func(e *sentry.Event, _ *sentry.EventHint) *sentry.Event {
			e.Contexts["trace"]["data"] = "opaque"
			return e
		})
		hub.CaptureException(errors.New("opaque data anchor"))

		ev := oneEvent(t, mt, "")
		trace, ok := ev.Contexts["trace"]
		if !ok {
			t.Fatal("event has no trace context")
		}
		if got := trace["data"]; got != "opaque" {
			t.Errorf("trace data = %v, want the string opaque untouched", got)
		}
		assertNoLeak(t, mt.captured())
	})

	// Go decodes %3F and %23 into r.URL.Path, which feeds request.url and the transaction name.
	t.Run("encoded_query_in_path", func(t *testing.T) {
		mt := filteredClient(t, true)
		r := httptest.NewRequest(http.MethodGet, "/v1/invoices%3Fq="+markerTIN+"%23"+markerIRN, nil)
		serveThroughSentry(r, readAndCapture("encoded path anchor"))

		ev := oneEvent(t, mt, "")
		if ev.Request == nil {
			t.Fatal("error event has no request")
		}
		if !strings.HasSuffix(ev.Request.URL, "/v1/invoices") {
			t.Errorf("request.url = %q, want it to end /v1/invoices", ev.Request.URL)
		}
		if tx := oneEvent(t, mt, "transaction"); tx.Transaction != "GET /v1/invoices" {
			t.Errorf("transaction = %q, want GET /v1/invoices", tx.Transaction)
		}
		assertNoLeak(t, mt.captured())
	})

	t.Run("trace_description_and_data", func(t *testing.T) {
		mt := filteredClient(t, true)
		ctx := sentry.SetHubOnContext(context.Background(), sentry.CurrentHub().Clone())
		tx := sentry.StartTransaction(ctx, "reconcile.run", sentry.WithDescription("GET /v1/invoices?q="+markerTIN))
		tx.SetData("url", "https://h/p?q="+markerTIN)
		tx.SetData("http.query", "q="+markerTIN)
		tx.SetData("http.fragment", markerIRN)
		sentry.GetHubFromContext(tx.Context()).CaptureException(errors.New("trace anchor"))
		tx.Finish()

		for _, typ := range []string{"", "transaction"} {
			ev := oneEvent(t, mt, typ)
			trace := ev.Contexts["trace"]
			if got := trace["description"]; got != "GET /v1/invoices" {
				t.Errorf("%q event trace description = %v, want GET /v1/invoices", typ, got)
			}
			data, ok := trace["data"].(map[string]interface{})
			if !ok || len(data) == 0 {
				t.Fatalf("%q event trace data = %#v, want a non-empty map", typ, trace["data"])
			}
			if got := data["url"]; got != "https://h/p" {
				t.Errorf("%q event trace data url = %v, want https://h/p", typ, got)
			}
			for _, k := range []string{"http.query", "http.fragment"} {
				if v, ok := data[k]; ok {
					t.Errorf("%q event trace data holds %s = %v, want the key absent", typ, k, v)
				}
			}
		}
		assertNoLeak(t, mt.captured())
	})

	t.Run("request_and_transaction_both_carry_a_query", func(t *testing.T) {
		mt := filteredClient(t, false)
		sentry.CurrentHub().Clone().CaptureEvent(&sentry.Event{
			Level:       sentry.LevelError,
			Message:     "both fields anchor",
			Transaction: "GET /v1/invoices?q=" + markerTIN + "#" + markerIRN,
			Request: &sentry.Request{
				Method:      http.MethodGet,
				URL:         "https://h/v1/invoices?q=" + markerTIN,
				QueryString: "q=" + markerTIN,
			},
		})

		ev := oneEvent(t, mt, "")
		if ev.Transaction != "GET /v1/invoices" {
			t.Errorf("transaction = %q, want GET /v1/invoices", ev.Transaction)
		}
		if ev.Request == nil {
			t.Fatal("event lost its request")
		}
		if ev.Request.URL != "https://h/v1/invoices" || ev.Request.QueryString != "" {
			t.Errorf("request url/query_string = %q / %q, want https://h/v1/invoices / empty", ev.Request.URL, ev.Request.QueryString)
		}
		assertNoLeak(t, mt.captured())
	})
}

func TestStripQuery(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		{"https://h/p?q=1", "https://h/p"},
		{"GET /p?", "GET /p"},
		{"/p#", "/p"},
		{"/p?a=1?b=2", "/p"},
		{"/p?a=1 then /q#f", "/p then /q"},
		{`"a"?b`, `"a"`},
		{"row #5", "row "}, // accepted cost: prose loses the token after # or ?
		{"a?x\tb", "a\tb"},
		{"/p?q=Ünï\nnext", "/p\nnext"},
		{"", ""},
		{"no query", "no query"},
	} {
		if got := stripQuery(c.in); got != c.want {
			t.Errorf("stripQuery(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func bodyRequest() *http.Request {
	body := `{"buyer_tin":"` + markerTIN + `","total":"` + markerAmt + `","irn":"` + markerIRN + `"}`
	r := httptest.NewRequest(http.MethodPost, "/v1/invoices", strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	return r
}

func TestSentryFilter_NoRequestBodyLeaves(t *testing.T) {
	t.Run("error_event", func(t *testing.T) {
		mt := filteredClient(t, false)
		serveThroughSentry(bodyRequest(), readAndCapture("invoice create failed"))

		ev := oneEvent(t, mt, "")
		if ev.Request == nil {
			t.Fatal("error event has no request; the handler's scope request was not applied")
		}
		if ev.Request.Method != http.MethodPost {
			t.Errorf("request.method = %q, want POST", ev.Request.Method)
		}
		if ev.Request.Data != "" {
			t.Errorf("request.data = %q, want empty", ev.Request.Data)
		}
		assertNoLeak(t, mt.captured())
	})

	t.Run("transaction", func(t *testing.T) {
		mt := filteredClient(t, true)
		serveThroughSentry(bodyRequest(), readAndReturn)

		tx := oneEvent(t, mt, "transaction")
		if tx.Transaction != "POST /v1/invoices" {
			t.Errorf("transaction = %q, want POST /v1/invoices", tx.Transaction)
		}
		if tx.Request != nil && tx.Request.Data != "" {
			t.Errorf("transaction request.data = %q, want empty", tx.Request.Data)
		}
		assertNoLeak(t, mt.captured())
	})

	t.Run("cookies_and_env", func(t *testing.T) {
		mt := filteredClient(t, false)
		sentry.CurrentHub().Clone().CaptureEvent(&sentry.Event{
			Level:   sentry.LevelError,
			Message: "cookies anchor",
			Request: &sentry.Request{
				Method:  http.MethodGet,
				URL:     "https://h/v1/session",
				Cookies: "s=" + markerCred,
				Env:     map[string]string{"REMOTE_ADDR": markerCred},
			},
		})

		ev := oneEvent(t, mt, "")
		if ev.Request == nil {
			t.Fatal("event lost its request")
		}
		if ev.Request.Method != http.MethodGet {
			t.Errorf("request.method = %q, want GET to survive", ev.Request.Method)
		}
		if ev.Request.Cookies != "" {
			t.Errorf("request.cookies = %q, want empty", ev.Request.Cookies)
		}
		if len(ev.Request.Env) != 0 {
			t.Errorf("request.env = %v, want empty", ev.Request.Env)
		}
		assertNoLeak(t, mt.captured())
	})
}

func headerRequest() *http.Request {
	r := httptest.NewRequest(http.MethodGet, "/v1/invoices", nil)
	for k, v := range map[string]string{
		"X-S2S-Token":      markerCred + "-s2s",
		"X-Gateway-Token":  markerCred + "-gw",
		"X-User-Id":        markerCred + "-user",
		"X-User-Role":      markerCred + "-role",
		"X-Tenant-Id":      markerCred + "-tenant",
		"Referer":          "https://app.example/invoices?q=" + markerTIN,
		"Authorization":    "Bearer " + markerCred + "-authz",
		"Cookie":           "s=" + markerCred + "-cookie",
		"X-Future-Adapter": markerCred + "-future",
		"Content-Type":     "application/json",
		"X-Request-Id":     "req-anchor-1",
	} {
		r.Header.Set(k, v)
	}
	return r
}

func assertHeaderKeys(t *testing.T, req *sentry.Request, want ...string) {
	t.Helper()
	if req == nil {
		t.Fatal("event has no request")
	}
	var got []string
	for k := range req.Headers {
		got = append(got, k)
	}
	sort.Strings(got)
	sort.Strings(want)
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("request.headers keys = %v, want exactly %v", got, want)
	}
}

func TestSentryFilter_HeadersPassAnAllowlist(t *testing.T) {
	t.Run("error_event", func(t *testing.T) {
		mt := filteredClient(t, false)
		serveThroughSentry(headerRequest(), readAndCapture("header anchor"))

		ev := oneEvent(t, mt, "")
		assertHeaderKeys(t, ev.Request, "Content-Type", "Host", "X-Request-Id")
		assertNoLeak(t, mt.captured())
	})

	t.Run("transaction", func(t *testing.T) {
		mt := filteredClient(t, true)
		serveThroughSentry(headerRequest(), readAndReturn)

		tx := oneEvent(t, mt, "transaction")
		assertHeaderKeys(t, tx.Request, "Content-Type", "Host", "X-Request-Id")
		assertNoLeak(t, mt.captured())
	})

	t.Run("lowercase_names", func(t *testing.T) {
		mt := filteredClient(t, false)
		sentry.CurrentHub().Clone().CaptureEvent(&sentry.Event{
			Level:   sentry.LevelError,
			Message: "lowercase anchor",
			Request: &sentry.Request{
				Method:  http.MethodGet,
				URL:     "https://h/v1/x",
				Headers: map[string]string{"x-s2s-token": markerCred, "content-type": "application/json"},
			},
		})

		ev := oneEvent(t, mt, "")
		if ev.Request == nil || len(ev.Request.Headers) == 0 {
			t.Fatalf("request headers are empty (%+v), want content-type to survive", ev.Request)
		}
		contentType := false
		for k, v := range ev.Request.Headers {
			if strings.EqualFold(k, "x-s2s-token") {
				t.Errorf("header %q survived, want it absent", k)
			}
			if strings.EqualFold(k, "content-type") && v == "application/json" {
				contentType = true
			}
		}
		if !contentType {
			t.Errorf("request.headers = %v, want the content type to survive", ev.Request.Headers)
		}
		assertNoLeak(t, mt.captured())
	})

	t.Run("every_listed_name_in_any_case", func(t *testing.T) {
		mt := filteredClient(t, false)
		sentry.CurrentHub().Clone().CaptureEvent(&sentry.Event{
			Level:   sentry.LevelError,
			Message: "mixed case anchor",
			Request: &sentry.Request{
				Method: http.MethodGet,
				URL:    "https://h/v1/x",
				Headers: map[string]string{
					"ACCEPT":         "application/json",
					"content-LENGTH": "42",
					"Content-type":   "text/csv",
					"host":           "invoice.internal",
					"user-agent":     "ops-probe/1",
					"X-REQUEST-ID":   "req-anchor-2",
					"x-S2S-TOKEN":    markerCred,
					"X-Buyer-Tin":    markerTIN,
				},
			},
		})

		ev := oneEvent(t, mt, "")
		if ev.Request == nil {
			t.Fatal("event lost its request")
		}
		want := map[string]string{
			"Accept":         "application/json",
			"Content-Length": "42",
			"Content-Type":   "text/csv",
			"Host":           "invoice.internal",
			"User-Agent":     "ops-probe/1",
			"X-Request-Id":   "req-anchor-2",
		}
		if !reflect.DeepEqual(ev.Request.Headers, want) {
			t.Errorf("request.headers = %v, want exactly %v", ev.Request.Headers, want)
		}
		assertNoLeak(t, mt.captured())
	})
}

func TestSentryFilter_KeepsIdentityAsIs(t *testing.T) {
	mt := filteredClient(t, false)
	hub := sentry.CurrentHub()
	hub.PushScope()
	defer hub.PopScope()
	hub.Scope().SetUser(sentry.User{ID: markerTIN, Email: "a@b.c"})

	requestID := `req "x"?y=1`
	tenantID := uuid.NewString()
	CaptureError(WithTenantID(WithRequestID(context.Background(), requestID), tenantID), errors.New("identity anchor"))

	ev := oneEvent(t, mt, "")
	if got := ev.Tags["request_id"]; got != requestID {
		t.Errorf("request_id tag = %q, want %q byte-identical", got, requestID)
	}
	if got := ev.Tags["tenant_id"]; got != tenantID {
		t.Errorf("tenant_id tag = %q, want %q byte-identical", got, tenantID)
	}
	if ev.ServerName != "svc" {
		t.Errorf("server_name = %q, want the service svc", ev.ServerName)
	}
	if !ev.User.IsEmpty() {
		t.Errorf("user = %+v, want empty", ev.User)
	}
	assertNoLeak(t, mt.captured())

	clientTenant := `tnt "y"?z=2`
	CaptureError(WithTenantID(context.Background(), clientTenant), errors.New("tenant anchor"))
	evs := eventsOfType(mt, "")
	if len(evs) != 2 {
		t.Fatalf("recorded %d error events, want 2", len(evs))
	}
	if got := evs[1].Tags["tenant_id"]; got != clientTenant {
		t.Errorf("tenant_id tag = %q, want %q byte-identical", got, clientTenant)
	}
}

func TestSentryFilter_TransactionCarriesNoUser(t *testing.T) {
	mt := filteredClient(t, true)
	hub := sentry.CurrentHub()
	hub.PushScope()
	defer hub.PopScope()
	hub.Scope().SetUser(sentry.User{ID: markerTIN, Email: "a@b.c"})

	serveThroughSentry(httptest.NewRequest(http.MethodGet, "/v1/invoices", nil), readAndReturn)

	tx := oneEvent(t, mt, "transaction")
	if tx.Transaction != "GET /v1/invoices" {
		t.Errorf("transaction = %q, want GET /v1/invoices", tx.Transaction)
	}
	if !tx.User.IsEmpty() {
		t.Errorf("transaction user = %+v, want empty", tx.User)
	}
	assertNoLeak(t, mt.captured())
}

func TestSentryFilter_ErrorTextLosesQuotedValues(t *testing.T) {
	t.Run("chained_exception", func(t *testing.T) {
		mt := filteredClient(t, false)
		inner := fmt.Errorf("issue_date %q", markerIRN)
		sentry.CurrentHub().Clone().CaptureException(fmt.Errorf("archive: compact json %q: %w", markerTIN, inner))

		ev := oneEvent(t, mt, "")
		if len(ev.Exception) < 2 {
			t.Fatalf("event has %d exception values, want the whole chain", len(ev.Exception))
		}
		for i, ex := range ev.Exception {
			if !strings.Contains(ex.Value, "issue_date") {
				t.Errorf("exception[%d] value = %q, want it to still hold issue_date", i, ex.Value)
			}
		}
		if got := exceptionValue(t, ev); !strings.Contains(got, "archive: compact json") {
			t.Errorf("outer exception value = %q, want it to still hold archive: compact json", got)
		}
		assertNoLeak(t, mt.captured())
	})

	t.Run("upstream_reason", func(t *testing.T) {
		mt := filteredClient(t, false)
		inner := fmt.Errorf("docling: /v1/read returned 500: failed on %s", markerTIN)
		sentry.CurrentHub().Clone().CaptureException(fmt.Errorf("extract: %w", inner))

		ev := oneEvent(t, mt, "")
		if len(ev.Exception) < 2 {
			t.Fatalf("event has %d exception values, want the whole chain", len(ev.Exception))
		}
		for i, ex := range ev.Exception {
			if !strings.Contains(ex.Value, "returned 500: ") {
				t.Errorf("exception[%d] value = %q, want it to still hold returned 500: ", i, ex.Value)
			}
		}
		assertNoLeak(t, mt.captured())
	})

	t.Run("message_event", func(t *testing.T) {
		mt := filteredClient(t, false)
		sentry.CurrentHub().Clone().CaptureMessage(fmt.Sprintf("total %q rejected", markerAmt))

		ev := oneEvent(t, mt, "")
		if !strings.Contains(ev.Message, "rejected") {
			t.Errorf("message = %q, want it to still hold rejected", ev.Message)
		}
		assertNoLeak(t, mt.captured())
	})

	t.Run("tag_value", func(t *testing.T) {
		mt := filteredClient(t, false)
		hub := sentry.CurrentHub().Clone()
		hub.Scope().SetTag("err", `decode "`+markerIRN+`"`)
		hub.CaptureException(errors.New("quoted tag anchor"))

		ev := oneEvent(t, mt, "")
		if got := ev.Tags["err"]; got != `decode "[redacted]"` {
			t.Errorf("tag err = %q, want %q", got, `decode "[redacted]"`)
		}
		assertNoLeak(t, mt.captured())
	})

	t.Run("breadcrumb_message", func(t *testing.T) {
		mt := filteredClient(t, false)
		hub := sentry.CurrentHub().Clone()
		hub.AddBreadcrumb(&sentry.Breadcrumb{Category: "import", Message: `import failed: header "` + markerTIN + `"`}, nil)
		hub.CaptureException(errors.New("quoted breadcrumb anchor"))

		ev := oneEvent(t, mt, "")
		if len(ev.Breadcrumbs) != 1 {
			t.Fatalf("event has %d breadcrumbs, want 1", len(ev.Breadcrumbs))
		}
		if got := ev.Breadcrumbs[0].Message; !strings.Contains(got, "import failed: header") {
			t.Errorf("breadcrumb message = %q, want it to still hold import failed: header", got)
		}
		assertNoLeak(t, mt.captured())
	})

	t.Run("breadcrumb_data", func(t *testing.T) {
		mt := filteredClient(t, false)
		hub := sentry.CurrentHub().Clone()
		hub.AddBreadcrumb(&sentry.Breadcrumb{
			Category: "import",
			Message:  "import failed",
			Data:     map[string]any{"err": `header "` + markerTIN + `"`},
		}, nil)
		hub.CaptureException(errors.New("quoted breadcrumb data anchor"))

		ev := oneEvent(t, mt, "")
		if len(ev.Breadcrumbs) != 1 {
			t.Fatalf("event has %d breadcrumbs, want 1", len(ev.Breadcrumbs))
		}
		if got, _ := ev.Breadcrumbs[0].Data["err"].(string); !strings.Contains(got, "header") {
			t.Errorf("breadcrumb data err = %q, want it to still hold header", got)
		}
		assertNoLeak(t, mt.captured())
	})

	t.Run("transaction_span_data", func(t *testing.T) {
		mt := filteredClient(t, true)
		ctx := sentry.SetHubOnContext(context.Background(), sentry.CurrentHub().Clone())
		tx := sentry.StartTransaction(ctx, "import.run")
		span := tx.StartChild("db.lookup", sentry.WithDescription(`lookup "`+markerTIN+`"`))
		span.SetData("err", `decode "`+markerIRN+`"`)
		span.Finish()
		tx.Finish()

		got := oneEvent(t, mt, "transaction")
		var lookups []*sentry.Span
		for _, s := range got.Spans {
			if s.Op == "db.lookup" {
				lookups = append(lookups, s)
			}
		}
		if len(lookups) != 1 {
			t.Fatalf("transaction has %d db.lookup spans, want 1", len(lookups))
		}
		if !strings.Contains(lookups[0].Description, "lookup") {
			t.Errorf("span description = %q, want it to still hold lookup", lookups[0].Description)
		}
		if got, _ := lookups[0].Data["err"].(string); !strings.Contains(got, "decode") {
			t.Errorf("span data err = %q, want it to still hold decode", got)
		}
		assertNoLeak(t, mt.captured())
	})

	t.Run("trace_description_and_data", func(t *testing.T) {
		mt := filteredClient(t, true)
		ctx := sentry.SetHubOnContext(context.Background(), sentry.CurrentHub().Clone())
		tx := sentry.StartTransaction(ctx, "import.run", sentry.WithDescription(`lookup "`+markerTIN+`"`))
		tx.SetData("err", `decode "`+markerIRN+`"`)
		sentry.GetHubFromContext(tx.Context()).CaptureException(errors.New("quoted trace anchor"))
		tx.Finish()

		for _, typ := range []string{"", "transaction"} {
			trace := oneEvent(t, mt, typ).Contexts["trace"]
			if got, _ := trace["description"].(string); !strings.Contains(got, "lookup") {
				t.Errorf("%q event trace description = %q, want it to still hold lookup", typ, got)
			}
			data, ok := trace["data"].(map[string]interface{})
			if !ok || len(data) == 0 {
				t.Fatalf("%q event trace data = %#v, want a non-empty map", typ, trace["data"])
			}
			if got, _ := data["err"].(string); !strings.Contains(got, "decode") {
				t.Errorf("%q event trace data err = %q, want it to still hold decode", typ, got)
			}
		}
		assertNoLeak(t, mt.captured())
	})

	t.Run("inner_only_quote", func(t *testing.T) {
		mt := filteredClient(t, false)
		inner := fmt.Errorf("issue_date %q is not in YYYY-MM-DD format", markerAmt)
		sentry.CurrentHub().Clone().CaptureException(fmt.Errorf("import row 7: %w", inner))

		ev := oneEvent(t, mt, "")
		if len(ev.Exception) < 2 {
			t.Fatalf("event has %d exception values, want the whole chain", len(ev.Exception))
		}
		for i, ex := range ev.Exception {
			if !strings.Contains(ex.Value, "is not in YYYY-MM-DD format") {
				t.Errorf("exception[%d] value = %q, want it to still hold is not in YYYY-MM-DD format", i, ex.Value)
			}
		}
		if got := exceptionValue(t, ev); !strings.Contains(got, "import row 7") {
			t.Errorf("outer exception value = %q, want it to still hold import row 7", got)
		}
		assertNoLeak(t, mt.captured())
	})

	// errors.Join reaches Sentry as an exception group: the joined value plus one per branch.
	t.Run("joined_errors", func(t *testing.T) {
		mt := filteredClient(t, false)
		sentry.CurrentHub().Clone().CaptureException(errors.Join(
			fmt.Errorf("mapping key %q", markerTIN),
			fmt.Errorf("issue_date %q", markerIRN),
		))

		ev := oneEvent(t, mt, "")
		if len(ev.Exception) < 3 {
			t.Fatalf("event has %d exception values, want the group and both branches", len(ev.Exception))
		}
		var keys, dates int
		for _, ex := range ev.Exception {
			if strings.Contains(ex.Value, "mapping key") {
				keys++
			}
			if strings.Contains(ex.Value, "issue_date") {
				dates++
			}
		}
		if keys < 2 || dates < 2 {
			t.Errorf("exception values %+v lost an anchor: mapping key in %d, issue_date in %d, want 2 each", ev.Exception, keys, dates)
		}
		assertNoLeak(t, mt.captured())
	})
}

// Each row fails if the composition order changes; each output must also be a fixed point.
func TestScrubText(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		{fmt.Sprintf("key %q header %q", "Total?", markerTIN), `key "[redacted]" header "[redacted]"`},
		{`import "returned 500: x" failed: docling returned 422: ` + markerIRN, `import "[redacted]" failed: docling returned 422: [redacted]`},
		{"GET /v1/invoices?q=" + markerTIN + ` decode "` + markerIRN + `"`, `GET /v1/invoices decode "[redacted]"`},
		{`docling: /v1/read returned 500: "` + markerTIN, "docling: /v1/read returned 500: [redacted]"},
		{"nothing to redact", "nothing to redact"},
		{"", ""},
	} {
		got := ScrubText(c.in)
		if got != c.want {
			t.Errorf("ScrubText(%q) = %q, want %q", c.in, got, c.want)
		}
		if again := ScrubText(got); again != got {
			t.Errorf("ScrubText is not idempotent on %q: second pass gives %q", got, again)
		}
	}
}

func TestRedactQuoted(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		{`a "` + markerTIN + `" b "` + markerIRN + `" c`, `a "[redacted]" b "[redacted]" c`},
		{fmt.Sprintf("x %q y", `a"`+markerTIN), `x "[redacted]" y`},
		{fmt.Sprintf("x %q y", markerTIN+`\`), `x "[redacted]" y`},
		{`bad "` + markerTIN + ` and more`, `bad "[redacted]`},
		{`field ""`, `field "[redacted]"`},
		{"", ""},
		{"docling: /v1/read returned 500", "docling: /v1/read returned 500"},
		{`"` + markerTIN + `""` + markerIRN + `"`, `"[redacted]""[redacted]"`},
		{`x "` + markerTIN + `\\" y "` + markerIRN + `"`, `x "[redacted]" y "[redacted]"`},
		{`bad "` + markerTIN + `\"`, `bad "[redacted]`},
		{`bad "` + markerTIN + `\`, `bad "[redacted]`},
		{`trailing "`, `trailing "[redacted]`},
		{fmt.Sprintf("Ọ̀yọ́ café %q — 日本", "Adébáyọ̀ "+markerTIN), `Ọ̀yọ́ café "[redacted]" — 日本`},
		{`invalid character '"' after object key: value "` + markerTIN + `"`, `invalid character '"' after object key: value "[redacted]"`},
		{`invalid character '\"' in literal: value "` + markerTIN + `"`, `invalid character '\"' in literal: value "[redacted]"`},
		{`bad input x"y: value "` + markerTIN + `"`, `bad input x"[redacted]`},
		{`a '"' b`, `a '"' b`},
		{`'"'`, `'"'`},
		{`value "` + markerTIN + `" rune '"'`, `value "[redacted]" rune '"'`},
		{`value "` + markerTIN + `" rune '\"'`, `value "[redacted]" rune '\"'`},
		{`cut '"`, `cut '"[redacted]`},
		{`rune '\\' then "` + markerTIN + `"`, `rune '\\' then "[redacted]"`},
		{`rune '\\'"` + markerTIN + `"`, `rune '\\'"[redacted]"`},
		{`a\"b "` + markerTIN + `"`, `a\"[redacted]`},
		{`x "a'"' "` + markerTIN + `"`, `x "[redacted]"' "[redacted]"`},
		{`bad size 5" for "` + markerTIN + `" in row 6"`, `bad size 5"[redacted]"` + markerTIN + `"[redacted]"`}, // accepted residual: stray quotes misalign the pairing
	} {
		if got := redactQuoted(c.in); got != c.want {
			t.Errorf("redactQuoted(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestRedactUpstreamReason(t *testing.T) {
	prefix := "docling: /v1/read returned 422: "
	var long strings.Builder
	for long.Len() < 4<<10 {
		long.WriteString("page " + markerTIN + "\n\"total\" " + markerAmt + "\n")
	}
	for _, c := range []struct{ in, want string }{
		{"docling: /v1/read returned 500: " + markerTIN, "docling: /v1/read returned 500: [redacted]"},
		{prefix + long.String(), prefix + "[redacted]"},
		{"validation service returned status 502", "validation service returned status 502"},
		{"returned 5: x", "returned 5: x"},
		{"returned 4220: x", "returned 4220: x"},
		{prefix + markerIRN + " retry returned 500: " + markerTIN, prefix + "[redacted]"},
	} {
		if got := redactUpstreamReason(c.in); got != c.want {
			t.Errorf("redactUpstreamReason(%.80q...) = %.80q..., want %q", c.in, got, c.want)
		}
	}
}

func TestSentryFilter_QuoteParity(t *testing.T) {
	t.Run("json_rune_literal_before_a_quoted_value", func(t *testing.T) {
		var v any
		jsonErr := json.Unmarshal([]byte(`{"a""b"}`), &v)
		if jsonErr == nil || !strings.Contains(jsonErr.Error(), `'"'`) {
			t.Fatalf("json error = %v, want one naming the character '\"'", jsonErr)
		}
		mt := filteredClient(t, false)
		sentry.CurrentHub().Clone().CaptureException(fmt.Errorf("%v: value %q", jsonErr, markerTIN))

		got := exceptionValue(t, oneEvent(t, mt, ""))
		if want := jsonErr.Error() + `: value "[redacted]"`; got != want {
			t.Errorf("exception value = %q, want %q", got, want)
		}
		assertNoLeak(t, mt.captured())
	})

	t.Run("stray_quote_operand_before_a_quoted_value", func(t *testing.T) {
		mt := filteredClient(t, false)
		stray := errors.New(`header x"y unparsed`)
		sentry.CurrentHub().Clone().CaptureException(fmt.Errorf("%v: value %q", stray, markerTIN))

		if got := exceptionValue(t, oneEvent(t, mt, "")); got != `header x"[redacted]` {
			t.Errorf("exception value = %q, want header x\"[redacted]", got)
		}
		assertNoLeak(t, mt.captured())
	})

	t.Run("even_string_keeps_unquoted_text", func(t *testing.T) {
		mt := filteredClient(t, false)
		sentry.CurrentHub().Clone().CaptureException(fmt.Errorf("import row 7: key %q date %q unparsed", markerIRN, markerTIN))

		want := `import row 7: key "[redacted]" date "[redacted]" unparsed`
		if got := exceptionValue(t, oneEvent(t, mt, "")); got != want {
			t.Errorf("exception value = %q, want %q", got, want)
		}
		assertNoLeak(t, mt.captured())
	})
}

func TestSentryFilter_SpanTagsAndName(t *testing.T) {
	mt := filteredClient(t, true)
	ctx := sentry.SetHubOnContext(context.Background(), sentry.CurrentHub().Clone())
	tx := sentry.StartTransaction(ctx, "reconcile.run")
	child := tx.StartChild("http.client")
	child.Name = "GET /v1/invoices?q=" + markerTIN
	child.SetTag("arg", fmt.Sprintf("buyer %q", markerTIN))
	child.Finish()
	tx.Finish()

	ev := oneEvent(t, mt, "transaction")
	if len(ev.Spans) != 1 {
		t.Fatalf("transaction has %d spans, want 1", len(ev.Spans))
	}
	s := ev.Spans[0]
	if s.Name != "GET /v1/invoices" {
		t.Errorf("span name = %q, want GET /v1/invoices", s.Name)
	}
	if got := s.Tags["arg"]; got != `buyer "[redacted]"` {
		t.Errorf("span tag arg = %q, want buyer \"[redacted]\"", got)
	}
	if got, want := child.Tags["arg"], fmt.Sprintf("buyer %q", markerTIN); got != want {
		t.Errorf("caller's span tag arg = %q, want %q: the filter must not edit the shared span", got, want)
	}
	assertNoLeak(t, mt.captured())
}

func TestSentryFilter_NestedBreadcrumbData(t *testing.T) {
	mt := filteredClient(t, false)
	hub := sentry.CurrentHub().Clone()
	hub.AddBreadcrumb(&sentry.Breadcrumb{
		Category: "db",
		Data: map[string]interface{}{
			"args":    []string{`x "` + markerTIN + `"`},
			"request": map[string]any{"url": "/x?tin=" + markerTIN},
			"rows":    []interface{}{map[string]interface{}{"irn": `k "` + markerIRN + `"`}},
			"headers": map[string]string{"x-note": `n "` + markerCred + `"`},
			"deep": map[string]interface{}{"l2": []interface{}{
				map[string]interface{}{"l4": []interface{}{`d "` + markerAmt + `"`}},
			}},
			"none":  nil,
			"count": 3,
		},
	}, nil)
	hub.CaptureException(errors.New("nested data anchor"))

	ev := oneEvent(t, mt, "")
	if len(ev.Breadcrumbs) != 1 {
		t.Fatalf("event has %d breadcrumbs, want 1", len(ev.Breadcrumbs))
	}
	d := ev.Breadcrumbs[0].Data
	if args, ok := d["args"].([]string); !ok || len(args) != 1 || args[0] != `x "[redacted]"` {
		t.Errorf("breadcrumb args = %#v, want [x \"[redacted]\"]", d["args"])
	}
	if req, ok := d["request"].(map[string]interface{}); !ok || req["url"] != "/x" {
		t.Errorf("breadcrumb request = %#v, want url /x", d["request"])
	}
	if rows, ok := d["rows"].([]interface{}); !ok || len(rows) != 1 {
		t.Errorf("breadcrumb rows = %#v, want one row", d["rows"])
	} else if row, _ := rows[0].(map[string]interface{}); row["irn"] != `k "[redacted]"` {
		t.Errorf("breadcrumb rows[0] = %#v, want irn k \"[redacted]\"", rows[0])
	}
	if h, ok := d["headers"].(map[string]string); !ok || h["x-note"] != `n "[redacted]"` {
		t.Errorf("breadcrumb headers = %#v, want x-note n \"[redacted]\"", d["headers"])
	}
	deep, _ := d["deep"].(map[string]interface{})
	l2, _ := deep["l2"].([]interface{})
	if len(l2) != 1 {
		t.Fatalf("breadcrumb deep = %#v, want l2 with one entry", d["deep"])
	}
	l3, _ := l2[0].(map[string]interface{})
	if l4, _ := l3["l4"].([]interface{}); len(l4) != 1 || l4[0] != `d "[redacted]"` {
		t.Errorf("breadcrumb deep.l2[0].l4 = %#v, want [d \"[redacted]\"]", l3["l4"])
	}
	if v, ok := d["none"]; !ok || v != nil {
		t.Errorf("breadcrumb none = %#v (present %v), want a nil value kept", v, ok)
	}
	if d["count"] != 3 {
		t.Errorf("breadcrumb count = %#v, want 3", d["count"])
	}
	assertNoLeak(t, mt.captured())
}

// Types outside scrubValue's switch take the JSON round-trip.
func TestSentryFilter_OtherDataTypesAreScrubbed(t *testing.T) {
	type tinT string
	type blobT []byte
	type deepT map[string]interface{}
	// Deeper than encoding/json decodes, so the round trip fails.
	deep := deepT{}
	cur := map[string]interface{}(deep)
	for i := 0; i < 10001; i++ {
		next := map[string]interface{}{}
		cur["d"] = next
		cur = next
	}
	cur["v"] = `d "` + markerTIN + `"`
	mt := filteredClient(t, false)
	hub := sentry.CurrentHub().Clone()
	hub.Scope().SetContext("probe", sentry.Context{"header": http.Header{"X-Note": {`n "` + markerCred + `"`}}})
	hub.AddBreadcrumb(&sentry.Breadcrumb{
		Category: "db",
		Data: map[string]interface{}{
			"header": http.Header{"Accept": {"text/html"}, "X-Note": {`n "` + markerCred + `"`}},
			"rows":   []map[string]interface{}{{"irn": `k "` + markerIRN + `"`, "line": 1}},
			"notes":  map[string][]string{"n": {`d "` + markerAmt + `"`, "ok"}},
			"raw":    []byte(`b "` + markerTIN + `"`),
			"json":   json.RawMessage(`"` + markerTIN + `"`),
			"blob":   blobT(`n "` + markerIRN + `"`),
			"list":   []interface{}{[]byte(`l "` + markerIRN + `"`)},
			"tin":    tinT(`t "` + markerTIN + `"`),
			"seq":    []int64{9007199254740993},
			"deep":   deep,
		},
	}, nil)
	hub.CaptureException(errors.New("other types anchor"))

	ev := oneEvent(t, mt, "")
	if len(ev.Breadcrumbs) != 1 {
		t.Fatalf("event has %d breadcrumbs, want 1", len(ev.Breadcrumbs))
	}
	got, err := json.Marshal(ev.Breadcrumbs[0].Data)
	if err != nil {
		t.Fatalf("marshal breadcrumb data: %v", err)
	}
	want := `{"blob":"n \"[redacted]\"",` +
		`"deep":"[redacted]",` +
		`"header":{"Accept":["text/html"],"X-Note":["n \"[redacted]\""]},` +
		`"json":"\"[redacted]\"",` +
		`"list":["l \"[redacted]\""],` +
		`"notes":{"n":["d \"[redacted]\"","ok"]},` +
		`"raw":"b \"[redacted]\"",` +
		`"rows":[{"irn":"k \"[redacted]\"","line":1}],` +
		`"seq":[9007199254740993],` +
		`"tin":"t \"[redacted]\""}`
	if string(got) != want {
		t.Errorf("breadcrumb data = %s, want %s", got, want)
	}
	if h, _ := ev.Contexts["probe"]["header"].(map[string]interface{}); fmt.Sprint(h["X-Note"]) != `[n "[redacted]"]` {
		t.Errorf("context header = %#v, want X-Note [n \"[redacted]\"]", ev.Contexts["probe"]["header"])
	}
	if got := scrubValue(make(chan int)); got != "[redacted]" {
		t.Errorf("scrubValue(chan) = %#v, want [redacted]", got)
	}
	assertNoLeak(t, mt.captured())
}

func TestSentryFilter_MapKeysAreScrubbed(t *testing.T) {
	mt := filteredClient(t, false)
	hub := sentry.CurrentHub().Clone()
	hub.Scope().SetContext("probe", sentry.Context{`k "` + markerIRN + `"`: "v"})
	hub.AddBreadcrumb(&sentry.Breadcrumb{
		Category: "db",
		Data: map[string]interface{}{
			`k "` + markerTIN + `"`: "top",
			"q?tin=" + markerTIN:    "query",
			"f#" + markerAmt:        "fragment",
			"http.query?x=1":        "q=" + markerTIN,
			"http.fragment#x":       markerIRN,
			"nested": map[string]interface{}{
				`k "` + markerIRN + `"`: "v",
				"q?tin=" + markerIRN:    "query",
				// Only a top-level http.query key is dropped.
				"http.query": "?tin=" + markerTIN,
			},
			"strings": map[string]string{`k "` + markerCred + `"`: "v"},
			"ints":    map[string]int{`k "` + markerAmt + `"`: 1},
		},
	}, nil)
	hub.CaptureException(errors.New("map keys anchor"))

	ev := oneEvent(t, mt, "")
	if len(ev.Breadcrumbs) != 1 {
		t.Fatalf("event has %d breadcrumbs, want 1", len(ev.Breadcrumbs))
	}
	d := ev.Breadcrumbs[0].Data
	for k, want := range map[string]string{`k "[redacted]"`: "top", "q": "query", "f": "fragment"} {
		if got := d[k]; got != want {
			t.Errorf("breadcrumb data[%q] = %#v, want %q", k, got, want)
		}
	}
	for _, k := range []string{"http.query", "http.fragment"} {
		if v, ok := d[k]; ok {
			t.Errorf("breadcrumb data holds %s = %#v, want the key absent", k, v)
		}
	}
	nested, _ := d["nested"].(map[string]interface{})
	if len(nested) != 3 {
		t.Fatalf("breadcrumb nested = %#v, want 3 keys", d["nested"])
	}
	for k, want := range map[string]string{`k "[redacted]"`: "v", "q": "query", "http.query": ""} {
		if got := nested[k]; got != want {
			t.Errorf("breadcrumb nested[%q] = %#v, want %q", k, got, want)
		}
	}
	if got := fmt.Sprint(d["strings"]); got != `map[k "[redacted]":v]` {
		t.Errorf("breadcrumb strings = %s, want map[k \"[redacted]\":v]", got)
	}
	if got := fmt.Sprint(d["ints"]); got != `map[k "[redacted]":1]` {
		t.Errorf("breadcrumb ints = %s, want map[k \"[redacted]\":1]", got)
	}
	if got := fmt.Sprint(ev.Contexts["probe"]); got != `map[k "[redacted]":v]` {
		t.Errorf("context probe = %s, want map[k \"[redacted]\":v]", got)
	}
	assertNoLeak(t, mt.captured())
}

func TestSentryFilter_ScopeContextsAreScrubbed(t *testing.T) {
	mt := filteredClient(t, false)
	hub := sentry.CurrentHub().Clone()
	hub.Scope().SetContext("invoice", sentry.Context{
		"irn":        `x "` + markerIRN + `"`,
		"list":       map[string]interface{}{"url": "/x?tin=" + markerTIN},
		"http.query": "tin=" + markerTIN,
	})
	hub.CaptureException(errors.New("context anchor"))

	ev := oneEvent(t, mt, "")
	if got := ev.Contexts["invoice"]["irn"]; got != `x "[redacted]"` {
		t.Errorf("invoice context irn = %v, want x \"[redacted]\"", got)
	}
	if v, ok := ev.Contexts["invoice"]["http.query"]; ok {
		t.Errorf("invoice context holds http.query = %v, want the key absent", v)
	}
	rt, ok := ev.Contexts["runtime"]
	if !ok || rt["name"] != "go" {
		t.Errorf("runtime context = %#v, want it to arrive with name go", rt)
	}
	assertNoLeak(t, mt.captured())
}

// Run with -race: a caller may write to a child span after it finished.
func TestSentryFilter_SpanWrittenAfterFinish(t *testing.T) {
	mt := filteredClient(t, true)
	ctx := sentry.SetHubOnContext(context.Background(), sentry.CurrentHub().Clone())
	tx := sentry.StartTransaction(ctx, "reconcile.run")
	child := tx.StartChild("db.query")
	child.SetData("q", fmt.Sprintf("buyer %q", markerTIN))
	child.Finish()

	done, started := make(chan struct{}), make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; ; i++ {
			child.SetData("late", fmt.Sprintf("n%d %q", i, markerIRN))
			if i == 0 {
				close(started)
			}
			select {
			case <-done:
				return
			default:
			}
		}
	}()
	<-started
	tx.Finish()
	close(done)
	wg.Wait()

	ev := oneEvent(t, mt, "transaction")
	if len(ev.Spans) != 1 || ev.Spans[0] == child {
		t.Fatalf("transaction spans = %v, want one scrubbed copy of the child", ev.Spans)
	}
	if got := ev.Spans[0].Data["q"]; got != `buyer "[redacted]"` {
		t.Errorf("span data q = %v, want buyer \"[redacted]\"", got)
	}
	assertNoLeak(t, mt.captured())
}

// spanJSON decodes s as Span.MarshalJSON writes it; numbers stay json.Number.
func spanJSON(t *testing.T, s *sentry.Span) map[string]any {
	t.Helper()
	raw, err := json.Marshal(s)
	if err != nil {
		t.Fatalf("marshal span: %v", err)
	}
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.UseNumber()
	var out map[string]any
	if err := dec.Decode(&out); err != nil {
		t.Fatalf("decode span: %v", err)
	}
	return out
}

func TestSentryFilter_SpanCopy(t *testing.T) {
	// Values here scrub to themselves, so the copy must match the caller's span byte for byte.
	for _, tc := range []struct {
		name     string
		tagsData bool
		keys     []string
	}{
		{"keeps_wire_fields", true, []string{"trace_id", "span_id", "parent_span_id", "name", "op", "description", "status", "start_timestamp", "timestamp", "origin", "tags", "data"}},
		{"no_tags_or_data", false, []string{"trace_id", "span_id", "parent_span_id", "op", "start_timestamp", "timestamp", "origin"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mt := filteredClient(t, true)
			ctx := sentry.SetHubOnContext(context.Background(), sentry.CurrentHub().Clone())
			tx := sentry.StartTransaction(ctx, "reconcile.run")
			child := tx.StartChild("db.query")
			if tc.tagsData {
				child.Name = "db select"
				child.Description = "select rows"
				child.Status = sentry.SpanStatusDeadlineExceeded
				child.SetTag("table", "invoices")
				child.SetData("rows", 3)
			}
			child.Finish()
			tx.Finish()

			ev := oneEvent(t, mt, "transaction")
			if len(ev.Spans) != 1 {
				t.Fatalf("transaction has %d spans, want 1", len(ev.Spans))
			}
			want, got := spanJSON(t, child), spanJSON(t, ev.Spans[0])
			for _, k := range tc.keys {
				if _, ok := want[k]; !ok {
					t.Fatalf("caller's span JSON has no %s: %v", k, want)
				}
			}
			if !tc.tagsData {
				if _, ok := got["tags"]; ok {
					t.Errorf("span copy JSON has tags %v, want none", got["tags"])
				}
				if _, ok := got["data"]; ok {
					t.Errorf("span copy JSON has data %v, want none", got["data"])
				}
			}
			if !reflect.DeepEqual(got, want) {
				t.Errorf("span copy JSON = %v, want the caller's span %v", got, want)
			}
			assertNoLeak(t, mt.captured())
		})
	}

	t.Run("large_int_keeps_precision", func(t *testing.T) {
		mt := filteredClient(t, true)
		ctx := sentry.SetHubOnContext(context.Background(), sentry.CurrentHub().Clone())
		tx := sentry.StartTransaction(ctx, "reconcile.run")
		child := tx.StartChild("db.query")
		child.SetData("id", int64(9007199254740993))
		child.Finish()
		tx.Finish()

		ev := oneEvent(t, mt, "transaction")
		if len(ev.Spans) != 1 {
			t.Fatalf("transaction has %d spans, want 1", len(ev.Spans))
		}
		data, _ := spanJSON(t, ev.Spans[0])["data"].(map[string]any)
		if got := fmt.Sprint(data["id"]); got != "9007199254740993" {
			t.Errorf("span data id = %s, want 9007199254740993", got)
		}
	})

	// encoding/json writes any depth but reads at most 10000 levels, so this span cannot round-trip.
	t.Run("undecodable_data_drops_tags_and_data", func(t *testing.T) {
		mt := filteredClient(t, true)
		ctx := sentry.SetHubOnContext(context.Background(), sentry.CurrentHub().Clone())
		tx := sentry.StartTransaction(ctx, "reconcile.run")
		child := tx.StartChild("db.query")
		deep := map[string]interface{}{}
		for i, cur := 0, deep; i < 10001; i++ {
			next := map[string]interface{}{}
			cur["d"] = next
			cur = next
		}
		child.SetTag("table", "invoices")
		child.SetData("deep", deep)
		child.Finish()
		tx.Finish()

		ev := oneEvent(t, mt, "transaction")
		if len(ev.Spans) != 1 {
			t.Fatalf("transaction has %d spans, want 1", len(ev.Spans))
		}
		s := ev.Spans[0]
		if s.Op != "db.query" || s.SpanID != child.SpanID {
			t.Errorf("span copy op %q id %v, want db.query %v", s.Op, s.SpanID, child.SpanID)
		}
		if len(s.Tags) != 0 || len(s.Data) != 0 {
			t.Errorf("span copy tags %v, %d data keys, want neither", s.Tags, len(s.Data))
		}
	})
}

func TestSentryFilter_KeySitesAreScrubbed(t *testing.T) {
	t.Run("event_tag_key", func(t *testing.T) {
		mt := filteredClient(t, false)
		hub := sentry.CurrentHub().Clone()
		hub.Scope().SetTag(`k "`+markerTIN+`"`, "v")
		hub.CaptureException(errors.New("tag key anchor"))

		ev := oneEvent(t, mt, "")
		if got := ev.Tags[`k "[redacted]"`]; got != "v" {
			t.Errorf("tags = %v, want k \"[redacted]\" = v", ev.Tags)
		}
		assertNoLeak(t, mt.captured())
	})

	// Only the exact id keys skip the value scrub; a key that scrubs to one does not.
	t.Run("id_tag_lookalike_key", func(t *testing.T) {
		mt := filteredClient(t, false)
		hub := sentry.CurrentHub().Clone()
		hub.Scope().SetTag("request_id?x=1", `v "`+markerTIN+`"`)
		hub.Scope().SetTag("tenant_id#x", `v "`+markerIRN+`"`)
		hub.CaptureException(errors.New("id lookalike anchor"))

		ev := oneEvent(t, mt, "")
		if len(ev.Tags) == 0 {
			t.Fatal("event has no tags")
		}
		scrubbed := 0
		for _, v := range ev.Tags {
			if v == `v "[redacted]"` {
				scrubbed++
			}
		}
		if scrubbed != 2 {
			t.Errorf("tags = %v, want both lookalike values v \"[redacted]\"", ev.Tags)
		}
		assertNoLeak(t, mt.captured())
	})

	t.Run("id_tag_beats_lookalike_key", func(t *testing.T) {
		mt := filteredClient(t, false)
		hub := sentry.CurrentHub().Clone()
		const realID, realTenant = `req "1"`, `tnt "1"`
		hub.Scope().SetTag("request_id", realID)
		hub.Scope().SetTag("request_id#x", "lookalike")
		hub.Scope().SetTag("tenant_id", realTenant)
		hub.Scope().SetTag("tenant_id#x", "lookalike")
		// Map order is random per capture, so 30 captures expose an overwrite.
		for i := 0; i < 30; i++ {
			hub.CaptureException(errors.New("id collision anchor"))
		}
		evs := mt.captured()
		if len(evs) != 30 {
			t.Fatalf("captured %d events, want 30", len(evs))
		}
		for i, ev := range evs {
			if got := ev.Tags["request_id"]; got != realID {
				t.Fatalf("capture %d: request_id = %q, want %q", i, got, realID)
			}
			if got := ev.Tags["tenant_id"]; got != realTenant {
				t.Fatalf("capture %d: tenant_id = %q, want %q", i, got, realTenant)
			}
		}
	})

	t.Run("context_name", func(t *testing.T) {
		mt := filteredClient(t, false)
		hub := sentry.CurrentHub().Clone()
		hub.Scope().SetContext(`c "`+markerIRN+`"`, sentry.Context{"a": "b"})
		hub.CaptureException(errors.New("context name anchor"))

		ev := oneEvent(t, mt, "")
		if got := fmt.Sprint(ev.Contexts[`c "[redacted]"`]); got != "map[a:b]" {
			t.Errorf("context c \"[redacted]\" = %s, want map[a:b]", got)
		}
		assertNoLeak(t, mt.captured())
	})

	t.Run("span_tag_key", func(t *testing.T) {
		mt := filteredClient(t, true)
		ctx := sentry.SetHubOnContext(context.Background(), sentry.CurrentHub().Clone())
		tx := sentry.StartTransaction(ctx, "reconcile.run")
		child := tx.StartChild("db.query")
		child.SetTag(`k "`+markerCred+`"`, "v")
		child.Finish()
		tx.Finish()

		ev := oneEvent(t, mt, "transaction")
		if len(ev.Spans) != 1 {
			t.Fatalf("transaction has %d spans, want 1", len(ev.Spans))
		}
		if got := ev.Spans[0].Tags[`k "[redacted]"`]; got != "v" {
			t.Errorf("span tags = %v, want k \"[redacted]\" = v", ev.Spans[0].Tags)
		}
		assertNoLeak(t, mt.captured())
	})
}

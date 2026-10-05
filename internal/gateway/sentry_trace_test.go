package gateway

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/getsentry/sentry-go"

	"github.com/SimonOsipov/invoice-os/internal/platform/sentrytest"
)

const (
	inboundTrace = "0123456789abcdef0123456789abcdef"
	inboundSpan  = "fedcba9876543210"
)

// stubURL starts a header-recording upstream.
func stubURL(t *testing.T) (*sentrytest.HeaderStub, *url.URL) {
	t.Helper()
	stub := sentrytest.NewHeaderStub(t, nil)
	u, err := url.Parse(stub.URL)
	if err != nil {
		t.Fatal(err)
	}
	return stub, u
}

// proxyWith serves one authenticated API request carrying the given extra headers.
func proxyWith(h http.Handler, tok, path string, headers map[string]string) int {
	req := request(http.MethodGet, path, tok)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec.Code
}

// proxyHeader is proxyWith for headers that carry several values.
func proxyHeader(h http.Handler, tok, path string, headers http.Header) int {
	req := request(http.MethodGet, path, tok)
	for k, vs := range headers {
		for _, v := range vs {
			req.Header.Add(k, v)
		}
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec.Code
}

// oneGatewayTx returns the only recorded transaction and its only http.client span.
func oneGatewayTx(t *testing.T, rec *sentrytest.Recorder) (*sentry.Event, *sentry.Span) {
	t.Helper()
	txs := rec.Transactions()
	if len(txs) != 1 {
		t.Fatalf("sent %d transactions, want 1", len(txs))
	}
	spans := sentrytest.ClientSpans(txs[0])
	if len(spans) != 1 {
		t.Fatalf("gateway transaction has %d http.client spans, want 1", len(spans))
	}
	return txs[0], spans[0]
}

func TestGatewayTrace_UpstreamIsAChildOfTheGateway(t *testing.T) {
	t.Run("no inbound trace", func(t *testing.T) {
		app, rec, _ := sentrytest.Boot(t, "gateway")
		up, upURL := stubURL(t)
		h, tok := mountAPI(t, app, map[string]*url.URL{"tenancy": upURL})

		if code := proxyWith(h, tok, "/api/tenancy/v1/me", nil); code != http.StatusOK {
			t.Fatalf("status = %d, want 200", code)
		}

		traceID, parentID := up.Only(t, 1).Trace(t)
		tx, span := oneGatewayTx(t, rec)
		if want := sentrytest.TraceID(t, tx); traceID != want {
			t.Errorf("upstream trace id = %s, want the gateway transaction's %s", traceID, want)
		}
		if want := span.SpanID.String(); parentID != want {
			t.Errorf("upstream parent = %s, want the gateway's http.client span %s", parentID, want)
		}
	})

	t.Run("inbound trace and baggage", func(t *testing.T) {
		app, rec, _ := sentrytest.Boot(t, "gateway")
		up, upURL := stubURL(t)
		h, tok := mountAPI(t, app, map[string]*url.URL{"tenancy": upURL})
		markers := []string{sentrytest.MarkerTIN, sentrytest.MarkerIRN, sentrytest.MarkerCred, sentrytest.MarkerAmt}

		code := proxyWith(h, tok, "/api/tenancy/v1/me", map[string]string{
			"Sentry-Trace": inboundTrace + "-" + inboundSpan + "-1",
			"Baggage": "sentry-transaction=" + markers[0] + ",sentry-release=" + markers[1] +
				",sentry-environment=" + markers[2] + ",vendor=" + markers[3],
		})
		if code != http.StatusOK {
			t.Fatalf("status = %d, want 200", code)
		}

		call := up.Only(t, 1)
		traceID, parentID := call.Trace(t)
		_, span := oneGatewayTx(t, rec)
		if traceID != inboundTrace {
			t.Errorf("upstream trace id = %s, want the inbound %s", traceID, inboundTrace)
		}
		if want := span.SpanID.String(); parentID != want {
			t.Errorf("upstream parent = %s, want the gateway's http.client span %s (not the inbound %s)", parentID, want, inboundSpan)
		}
		baggage := strings.Join(call.Header.Values("baggage"), ",")
		for _, m := range markers {
			if strings.Contains(baggage, m) {
				t.Errorf("upstream baggage %q carries inbound marker %q", baggage, m)
			}
		}
	})

	t.Run("several inbound sentry-trace headers", func(t *testing.T) {
		app, rec, _ := sentrytest.Boot(t, "gateway")
		up, upURL := stubURL(t)
		h, tok := mountAPI(t, app, map[string]*url.URL{"tenancy": upURL})

		code := proxyHeader(h, tok, "/api/tenancy/v1/me", http.Header{
			"Sentry-Trace": {inboundTrace + "-" + inboundSpan + "-1", inboundTrace + "-" + inboundSpan + "-0"},
			"Baggage":      {"sentry-transaction=" + sentrytest.MarkerTIN, "vendor=" + sentrytest.MarkerCred},
		})
		if code != http.StatusOK {
			t.Fatalf("status = %d, want 200", code)
		}

		call := up.Only(t, 1)
		_, parentID := call.Trace(t)
		_, span := oneGatewayTx(t, rec)
		if want := span.SpanID.String(); parentID != want {
			t.Errorf("upstream parent = %s, want the gateway's http.client span %s", parentID, want)
		}
		for _, v := range call.Header.Values("baggage") {
			if strings.Contains(v, sentrytest.MarkerTIN) || strings.Contains(v, sentrytest.MarkerCred) {
				t.Errorf("upstream baggage %q carries an inbound marker", v)
			}
		}
	})

	t.Run("inbound baggage alone", func(t *testing.T) {
		app, rec, _ := sentrytest.Boot(t, "gateway")
		up, upURL := stubURL(t)
		h, tok := mountAPI(t, app, map[string]*url.URL{"tenancy": upURL})

		code := proxyWith(h, tok, "/api/tenancy/v1/me", map[string]string{"Baggage": "sentry-transaction=" + sentrytest.MarkerTIN})
		if code != http.StatusOK {
			t.Fatalf("status = %d, want 200", code)
		}

		call := up.Only(t, 1)
		traceID, _ := call.Trace(t)
		tx, _ := oneGatewayTx(t, rec)
		if want := sentrytest.TraceID(t, tx); traceID != want {
			t.Errorf("upstream trace id = %s, want the gateway transaction's %s", traceID, want)
		}
		for _, v := range call.Header.Values("baggage") {
			if strings.Contains(v, sentrytest.MarkerTIN) {
				t.Errorf("upstream baggage %q carries the inbound marker", v)
			}
		}
	})

	t.Run("upstream status and headers come back through the traced transport", func(t *testing.T) {
		app, rec, _ := sentrytest.Boot(t, "gateway")
		up := sentrytest.NewHeaderStub(t, func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("X-Upstream", "kept")
			w.WriteHeader(http.StatusCreated)
		})
		upURL, err := url.Parse(up.URL)
		if err != nil {
			t.Fatal(err)
		}
		h, tok := mountAPI(t, app, map[string]*url.URL{"tenancy": upURL})

		req := request(http.MethodGet, "/api/tenancy/v1/me", tok)
		out := httptest.NewRecorder()
		h.ServeHTTP(out, req)

		up.Only(t, 1)
		if out.Code != http.StatusCreated || out.Header().Get("X-Upstream") != "kept" {
			t.Errorf("response = %d %q, want 201 with the upstream header", out.Code, out.Header().Get("X-Upstream"))
		}
		if _, span := oneGatewayTx(t, rec); span.Status != sentry.SpanStatusOK {
			t.Errorf("http.client span status = %v, want ok for a 201", span.Status)
		}
	})
}

func TestGatewayTrace_FleetIsUntracedAndProbesCarryNoHeader(t *testing.T) {
	app, rec, _ := sentrytest.Boot(t, "gateway")
	alpha, alphaURL := stubURL(t)
	beta, betaURL := stubURL(t)
	api, apiURL := stubURL(t)
	h, tok := mountAPI(t, app, map[string]*url.URL{"tenancy": apiURL})
	h = mountFleet(app, map[string]*url.URL{"alpha": alphaURL, "beta": betaURL})

	if code, _ := getFleet(t, h, "/healthz/fleet"); code != http.StatusOK {
		t.Fatalf("fleet status = %d, want 200", code)
	}

	for name, probe := range map[string]*sentrytest.HeaderStub{"alpha": alpha, "beta": beta} {
		calls := probe.Calls()
		if len(calls) != 2 || calls[0].Path != "/healthz" || calls[1].Path != "/readyz" {
			t.Errorf("%s probed at %+v, want /healthz then /readyz", name, calls)
		}
		for _, call := range calls {
			call.AssertUntraced(t)
		}
	}
	if got := rec.Transactions(); len(got) != 0 {
		t.Fatalf("/healthz/fleet sent %d transactions, want none", len(got))
	}

	// A probe stays untraced even when the fleet request itself carries a span. The span is never
	// finished, so it sends no transaction.
	tx := sentry.StartTransaction(t.Context(), "fleet")
	spanReq := httptest.NewRequest(http.MethodGet, "/healthz/fleet", nil).WithContext(tx.Context())
	rr := httptest.NewRecorder()
	FleetHealthHandler(map[string]*url.URL{"alpha": alphaURL}, nil, app.Logger).ServeHTTP(rr, spanReq)
	if rr.Code != http.StatusOK {
		t.Fatalf("fleet under a span: status = %d, want 200", rr.Code)
	}
	if calls := alpha.Calls(); len(calls) != 4 {
		t.Fatalf("alpha received %d requests, want 4", len(calls))
	} else {
		calls[2].AssertUntraced(t)
		calls[3].AssertUntraced(t)
	}

	// Positive control on the same recorder: an API call is the one transaction.
	if code := proxyWith(h, tok, "/api/tenancy/v1/me", nil); code != http.StatusOK {
		t.Fatalf("api status = %d, want 200", code)
	}
	api.Only(t, 1)
	txs := rec.Transactions()
	if len(txs) != 1 || !strings.Contains(txs[0].Transaction, routePrefix) {
		t.Fatalf("transactions = %d, want the one API call under %q", len(txs), routePrefix)
	}
}

func TestGatewayTrace_OutboundQueryDoesNotLeak(t *testing.T) {
	app, rec, _ := sentrytest.Boot(t, "gateway")
	up, upURL := stubURL(t)
	h, tok := mountAPI(t, app, map[string]*url.URL{"tenancy": upURL})

	if code := proxyWith(h, tok, "/api/tenancy/v1/x?q="+sentrytest.MarkerTIN, nil); code != http.StatusOK {
		t.Fatalf("status = %d, want 200", code)
	}

	if got := up.Only(t, 1).Query; got != "q="+sentrytest.MarkerTIN {
		t.Fatalf("upstream query = %q, want the marker forwarded", got)
	}
	oneGatewayTx(t, rec)
	sentrytest.AssertNoLeak(t, rec.Transactions())
}

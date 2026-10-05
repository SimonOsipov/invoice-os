package gateway

import (
	"context"
	"encoding/json"
	"io"
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
	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
	"github.com/SimonOsipov/invoice-os/internal/platform/sentrytest"
)

// mountAPI mounts the proxy as cmd/gateway/main.go does and returns the platform chain.
func mountAPI(t *testing.T, app *platform.App, upstreams map[string]*url.URL) (http.Handler, string) {
	t.Helper()
	tg := setupGateway(t)
	app.Mux.Handle(routePrefix, Handler(Options{Verifier: tg.verifier, Sessions: liveSessions(t), Upstreams: upstreams, Logger: app.Logger, GatewayToken: testGatewayToken}))
	return app.Handler(), tg.validToken(t)
}

func serveAPI(h http.Handler, path, bearer string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, request(http.MethodGet, path, bearer))
	return rec
}

// statusUpstream answers the status named by the last path segment.
func statusUpstream(t *testing.T) *url.URL {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		code, err := strconv.Atoi(r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:])
		if err != nil {
			code = http.StatusOK
		}
		w.WriteHeader(code)
	}))
	t.Cleanup(srv.Close)
	u, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatalf("parse upstream url: %v", err)
	}
	return u
}

func TestGatewaySentry_UnreachableUpstreamOpensOneIssue(t *testing.T) {
	app, rec, want := sentrytest.Boot(t, "gateway")
	h, tok := mountAPI(t, app, map[string]*url.URL{"invoice": closedURL(t)})

	if got := serveAPI(h, "/api/invoice/v1/invoices", tok).Code; got != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502", got)
	}
	e := rec.One(t, want)
	if len(e.Exception) == 0 || !strings.Contains(e.Exception[len(e.Exception)-1].Value, "connection refused") {
		t.Errorf("exception = %+v, want the proxy's dial error", e.Exception)
	}
	if got := e.Tags["http.route"]; got != routePrefix {
		t.Errorf("tag http.route = %q, want %q", got, routePrefix)
	}
	if wantFP := []string{"http-5xx", routePrefix, "502"}; !slices.Equal(e.Fingerprint, wantFP) {
		t.Errorf("fingerprint = %q, want %q", e.Fingerprint, wantFP)
	}
}

func TestGatewaySentry_RelayedServerErrorOpensNothing(t *testing.T) {
	app, rec, want := sentrytest.Boot(t, "gateway")
	h, tok := mountAPI(t, app, map[string]*url.URL{"invoice": statusUpstream(t), "dead": closedURL(t)})

	for _, code := range []int{http.StatusInternalServerError, http.StatusServiceUnavailable} {
		if got := serveAPI(h, "/api/invoice/v1/"+strconv.Itoa(code), tok).Code; got != code {
			t.Errorf("status = %d, want the upstream's %d relayed", got, code)
		}
	}
	rec.None(t)

	// Positive control: the gateway's own 502 on the same recorder is counted.
	serveAPI(h, "/api/dead/x", tok)
	rec.One(t, want)
}

func TestGatewaySentry_ClientErrorsOpenNothing(t *testing.T) {
	app, rec, want := sentrytest.Boot(t, "gateway")
	h, tok := mountAPI(t, app, map[string]*url.URL{"invoice": statusUpstream(t), "dead": closedURL(t)})

	if got := serveAPI(h, "/api/invoice/v1/invoices", "").Code; got != http.StatusUnauthorized {
		t.Errorf("no token: status = %d, want 401", got)
	}
	if got := serveAPI(h, "/api/nope/x", tok).Code; got != http.StatusNotFound {
		t.Errorf("unknown service: status = %d, want 404", got)
	}
	rec.None(t)

	serveAPI(h, "/api/dead/x", tok)
	rec.One(t, want)
}

func TestGatewaySentry_ClientDisconnectOpensNothing(t *testing.T) {
	app, rec, want := sentrytest.Boot(t, "gateway")
	entered := make(chan struct{})
	blocking := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		close(entered)
		<-r.Context().Done()
	}))
	t.Cleanup(blocking.Close)
	blockingURL, err := url.Parse(blocking.URL)
	if err != nil {
		t.Fatal(err)
	}
	h, tok := mountAPI(t, app, map[string]*url.URL{"invoice": blockingURL, "dead": closedURL(t)})
	gw := httptest.NewServer(h)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, gw.URL+"/api/invoice/v1/invoices", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+tok)
	errCh := make(chan error, 1)
	go func() {
		resp, err := gw.Client().Do(req)
		if err == nil {
			resp.Body.Close()
		}
		errCh <- err
	}()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("upstream never received the request")
	}
	cancel()
	if err := <-errCh; err == nil {
		t.Error("client request succeeded, want it cancelled")
	}
	// Close waits for the in-flight gateway handler, so its capture has happened.
	gw.Close()
	rec.None(t)

	serveAPI(h, "/api/dead/x", tok)
	rec.One(t, want)
}

// serveThroughGateway sends one GET through a real gateway server and closes it,
// which waits for the handler chain, so every capture has happened on return.
func serveThroughGateway(t *testing.T, h http.Handler, path, bearer string) (int, error) {
	t.Helper()
	gw := httptest.NewServer(h)
	defer gw.Close()
	req, err := http.NewRequest(http.MethodGet, gw.URL+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+bearer)
	resp, err := gw.Client().Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	_, err = io.ReadAll(resp.Body)
	return resp.StatusCode, err
}

func serverURL(t *testing.T, h http.Handler) *url.URL {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	u, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	return u
}

// The proxy has already marked the request when the body copy fails; it then aborts with http.ErrAbortHandler.
func TestGatewaySentry_UpstreamDiesMidBodyOpensNothing(t *testing.T) {
	app, rec, want := sentrytest.Boot(t, "gateway")
	dying := serverURL(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Length", "100")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("partial"))
		w.(http.Flusher).Flush()
		panic(http.ErrAbortHandler)
	}))
	h, tok := mountAPI(t, app, map[string]*url.URL{"invoice": dying, "dead": closedURL(t)})

	if code, err := serveThroughGateway(t, h, "/api/invoice/v1/invoices", tok); err == nil {
		t.Fatalf("status = %d with a whole body, want the response cut off", code)
	}
	rec.None(t)

	serveAPI(h, "/api/dead/x", tok)
	rec.One(t, want)
}

// An upstream that sends a 1xx and then drops the connection leaves the gateway's own 502.
func TestGatewaySentry_UpstreamDiesAfter1xxOpensOneIssue(t *testing.T) {
	app, rec, want := sentrytest.Boot(t, "gateway")
	hinting := serverURL(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		conn, buf, err := http.NewResponseController(w).Hijack()
		if err != nil {
			t.Errorf("hijack: %v", err)
			return
		}
		_, _ = buf.WriteString("HTTP/1.1 103 Early Hints\r\nLink: </app.css>; rel=preload\r\n\r\n")
		_ = buf.Flush()
		_ = conn.Close()
	}))
	h, tok := mountAPI(t, app, map[string]*url.URL{"invoice": hinting})

	code, err := serveThroughGateway(t, h, "/api/invoice/v1/invoices", tok)
	if err != nil || code != http.StatusBadGateway {
		t.Fatalf("status = %d, err = %v; want the gateway's 502", code, err)
	}
	e := rec.One(t, want)
	if wantFP := []string{"http-5xx", routePrefix, "502"}; !slices.Equal(e.Fingerprint, wantFP) {
		t.Errorf("fingerprint = %q, want %q", e.Fingerprint, wantFP)
	}
}

// mountFleet mounts the roll-up as cmd/gateway/main.go does, plus a control
// roll-up whose extra nil-URL upstream makes a probe panic.
func mountFleet(app *platform.App, upstreams map[string]*url.URL) http.Handler {
	app.Mux.HandleFunc("GET /healthz/fleet", FleetHealthHandler(upstreams, nil, app.Logger))
	withBoom := map[string]*url.URL{"boom": nil}
	for name, u := range upstreams {
		withBoom[name] = u
	}
	app.Mux.HandleFunc("GET /healthz/fleet-control", FleetHealthHandler(withBoom, nil, app.Logger))
	return app.Handler()
}

func getFleet(t *testing.T, h http.Handler, path string) (int, map[string]ServiceHealth) {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	var body FleetHealth
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode fleet body %q: %v", rec.Body.String(), err)
	}
	return rec.Code, statusByName(body)
}

func assertProbePanicked(t *testing.T, code int, byName map[string]ServiceHealth) {
	t.Helper()
	if code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503", code)
	}
	if got := byName["boom"]; got.Status != statusDown || got.Error != "probe panicked" {
		t.Errorf("boom = %+v, want down / probe panicked", got)
	}
	if got := byName["alpha"]; got.Status != statusUp {
		t.Errorf("alpha = %+v, want up", got)
	}
}

func assertPanicEvent(t *testing.T, e *sentry.Event) {
	t.Helper()
	if e.Level != sentry.LevelFatal {
		t.Errorf("level = %q, want fatal", e.Level)
	}
	if len(e.Exception) == 0 || !strings.Contains(e.Exception[len(e.Exception)-1].Value, "nil pointer dereference") {
		t.Errorf("exception = %+v, want the probe's nil-pointer panic", e.Exception)
	}
}

func TestFleetSentry_ProbePanicIsRecoveredAndReported(t *testing.T) {
	app, rec, want := sentrytest.Boot(t, "gateway")
	app.Mux.HandleFunc("GET /healthz/fleet", FleetHealthHandler(map[string]*url.URL{
		"alpha": healthzUpstream(t, false),
		"boom":  nil,
	}, nil, app.Logger))
	h := app.Handler()

	code, byName := getFleet(t, h, "/healthz/fleet")
	assertProbePanicked(t, code, byName)
	assertPanicEvent(t, rec.One(t, want))

	code, byName = getFleet(t, h, "/healthz/fleet")
	assertProbePanicked(t, code, byName)
	events := rec.Events()
	if len(events) != 2 {
		t.Fatalf("recorded %d events after two requests, want 2", len(events))
	}
	for _, e := range events {
		sentrytest.AssertLabels(t, e, want)
		assertPanicEvent(t, e)
	}
}

func TestFleetSentry_ProbePanicWithSentryOff(t *testing.T) {
	t.Setenv("SENTRY_DSN", "")
	sentry.CurrentHub().BindClient(nil)
	prev := slog.Default()
	t.Cleanup(func() { slog.SetDefault(prev) })
	app, err := platform.New("gateway")
	if err != nil {
		t.Fatal(err)
	}
	app.Mux.HandleFunc("GET /healthz/fleet", FleetHealthHandler(map[string]*url.URL{
		"alpha": healthzUpstream(t, false),
		"boom":  nil,
	}, nil, app.Logger))
	h := app.Handler()

	for range 2 {
		code, byName := getFleet(t, h, "/healthz/fleet")
		assertProbePanicked(t, code, byName)
	}
}

func TestFleetSentry_DegradedRollupOpensNothing(t *testing.T) {
	app, rec, want := sentrytest.Boot(t, "gateway")
	sick := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(sick.Close)
	sickURL, err := url.Parse(sick.URL)
	if err != nil {
		t.Fatal(err)
	}
	h := mountFleet(app, map[string]*url.URL{"alpha": healthzUpstream(t, false), "sick": sickURL})

	code, byName := getFleet(t, h, "/healthz/fleet")
	if code != http.StatusServiceUnavailable || byName["sick"].Status != statusDown {
		t.Fatalf("status = %d, sick = %+v; want 503 with sick down", code, byName["sick"])
	}
	rec.None(t)

	getFleet(t, h, "/healthz/fleet-control")
	rec.One(t, want)
}

func TestFleetSentry_HealthyRollupOpensNothing(t *testing.T) {
	app, rec, want := sentrytest.Boot(t, "gateway")
	h := mountFleet(app, map[string]*url.URL{"alpha": healthzUpstream(t, false), "beta": healthzUpstream(t, false)})

	if code, _ := getFleet(t, h, "/healthz/fleet"); code != http.StatusOK {
		t.Fatalf("status = %d, want 200", code)
	}
	rec.None(t)

	getFleet(t, h, "/healthz/fleet-control")
	rec.One(t, want)
}

func TestGatewayLogs_UpstreamDoesNotOverwriteService(t *testing.T) {
	oneLog := func(t *testing.T, rec *sentrytest.Recorder, body string) sentry.Log {
		t.Helper()
		var found []sentry.Log
		for _, l := range rec.Logs() {
			if l.Body == body {
				found = append(found, l)
			}
		}
		if len(found) != 1 {
			t.Fatalf("recorded %d %q logs, want 1", len(found), body)
		}
		return found[0]
	}
	assertAttrs := func(t *testing.T, l sentry.Log) {
		t.Helper()
		if got := l.Attributes["service"].AsString(); got != "gateway" {
			t.Errorf("service = %q, want gateway (the upstream must not replace it)", got)
		}
		if got := l.Attributes["upstream"].AsString(); got != "invoice" {
			t.Errorf("upstream = %q, want invoice", got)
		}
	}

	t.Run("unreachable", func(t *testing.T) {
		app, rec, _ := sentrytest.Boot(t, "gateway")
		h, tok := mountAPI(t, app, map[string]*url.URL{"invoice": closedURL(t)})

		if got := serveAPI(h, "/api/invoice/v1/invoices", tok).Code; got != http.StatusBadGateway {
			t.Fatalf("status = %d, want 502", got)
		}
		assertAttrs(t, oneLog(t, rec, "gateway upstream unreachable"))
	})

	t.Run("authz denied", func(t *testing.T) {
		app, rec, _ := sentrytest.Boot(t, "gateway")
		tg := setupGateway(t)
		app.Mux.Handle(routePrefix, Handler(Options{Verifier: tg.verifier, Sessions: liveSessions(t), Upstreams: map[string]*url.URL{"invoice": closedURL(t)}, Logger: app.Logger, GatewayToken: testGatewayToken}))
		tok := tg.mint(t, auth.MintOptions{Subject: testSubject, Role: testRole})

		if got := serveAPI(app.Handler(), "/api/invoice/v1/invoices", tok).Code; got != http.StatusForbidden {
			t.Fatalf("status = %d, want 403", got)
		}
		assertAttrs(t, oneLog(t, rec, "gateway authz denied"))
	})
}

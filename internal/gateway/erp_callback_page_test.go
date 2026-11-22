package gateway

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
)

const (
	ecGWToken = "gw-secret"
	ecPath    = "/internal/erp/callback"
	ecCSP     = "default-src 'none'; style-src 'unsafe-inline'; form-action 'self'; base-uri 'none'; frame-ancestors 'none'"
)

// ecState is a well-formed state: the tenant uuid, a dot and 43 base64url characters.
func ecState() string {
	return uuid.NewString() + "." + strings.Repeat("Ab3-_", 8) + "Cd4"
}

type ecCall struct {
	method, path, contentType, gateway string
	body                               []byte
}

// ecWorker stands in for the worker's callback route; reply decides each answer.
type ecWorker struct {
	mu    sync.Mutex
	calls []ecCall
	reply func(w http.ResponseWriter, r *http.Request)
}

func (f *ecWorker) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	raw, _ := io.ReadAll(r.Body)
	f.mu.Lock()
	f.calls = append(f.calls, ecCall{r.Method, r.URL.Path, r.Header.Get("Content-Type"), r.Header.Get("X-Gateway-Token"), raw})
	f.mu.Unlock()
	f.reply(w, r)
}

func (f *ecWorker) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

func (f *ecWorker) only(t *testing.T) ecCall {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.calls) != 1 {
		t.Fatalf("%d worker calls, want 1", len(f.calls))
	}
	return f.calls[0]
}

func ecOutcome(outcome string) func(http.ResponseWriter, *http.Request) {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"outcome": outcome})
	}
}

type ecEnv struct {
	h    http.Handler
	w    *ecWorker
	logs *safeBuf
}

// ecStart builds the page handler over a stub worker. A nil client gets the stub server's own.
func ecStart(t *testing.T, reply func(http.ResponseWriter, *http.Request), client *http.Client) *ecEnv {
	t.Helper()
	w := &ecWorker{reply: reply}
	srv := httptest.NewServer(w)
	t.Cleanup(srv.Close)
	base, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	if client == nil {
		client = srv.Client()
	}
	e := &ecEnv{w: w, logs: &safeBuf{}}
	log := slog.New(slog.NewTextHandler(e.logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	e.h = ERPCallbackPageHandler(base, client, ecGWToken, log)
	return e
}

func (e *ecEnv) get(query url.Values) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, "/erp/callback?"+query.Encode(), nil)
	rec := httptest.NewRecorder()
	e.h.ServeHTTP(rec, req)
	return rec
}

// ecOutcomeOf returns the page's one data-outcome.
func ecOutcomeOf(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	doc := vpParse(t, rec.Body.String())
	mains := vpFind(doc, vpTag("main"))
	if len(mains) != 1 {
		t.Fatalf("page has %d <main> elements, want 1:\n%s", len(mains), rec.Body.String())
	}
	v, ok := vpAttr(mains[0], "data-outcome")
	if !ok {
		t.Fatalf("<main> has no data-outcome:\n%s", rec.Body.String())
	}
	return v
}

func (e *ecEnv) wantNoEcho(t *testing.T, rec *httptest.ResponseRecorder, secrets ...string) {
	t.Helper()
	for _, s := range secrets {
		if s == "" {
			continue
		}
		if strings.Contains(rec.Body.String(), s) {
			t.Errorf("the page echoes %q", s)
		}
		if strings.Contains(e.logs.String(), s) {
			t.Errorf("a log line carries %q:\n%s", s, e.logs.String())
		}
	}
	if strings.Contains(e.logs.String(), ecGWToken) {
		t.Errorf("a log line carries the gateway token:\n%s", e.logs.String())
	}
}

func TestERPCallbackPage_RefusesShapeWithoutCallingTheWorker(t *testing.T) {
	e := ecStart(t, ecOutcome("connected"), nil)
	good := ecState()
	cases := []struct {
		name  string
		query url.Values
	}{
		{"a state of the wrong shape", url.Values{"state": {"x"}}},
		{"no state", url.Values{"code": {"fake-co-1"}}},
		{"a state with an upper-case tenant", url.Values{"state": {strings.ToUpper(good)}}},
		{"a state one character too long", url.Values{"state": {good + "A"}}},
		{"a state with an injected newline", url.Values{"state": {good + "\nX-Evil: 1"}}},
		{"a code of 2049 bytes", url.Values{"state": {good}, "code": {strings.Repeat("c", 2049)}}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			before := e.w.count()
			rec := e.get(c.query)
			if rec.Code != http.StatusBadRequest {
				t.Errorf("status = %d, want 400", rec.Code)
			}
			if got := ecOutcomeOf(t, rec); got != "invalid" {
				t.Errorf("data-outcome = %q, want invalid", got)
			}
			if n := e.w.count() - before; n != 0 {
				t.Errorf("the worker was called %d times, want 0", n)
			}
		})
	}

	// The control: the boundary code of 2048 bytes passes and reaches the worker.
	rec := e.get(url.Values{"state": {good}, "code": {strings.Repeat("c", 2048)}})
	if rec.Code != http.StatusOK || ecOutcomeOf(t, rec) != "connected" || e.w.count() != 1 {
		t.Errorf("a 2048-byte code = %d %q with %d worker calls, want 200 connected with 1", rec.Code, ecOutcomeOf(t, rec), e.w.count())
	}
}

func TestERPCallbackPage_ForwardsAndRenders(t *testing.T) {
	cases := []struct {
		outcome string
		status  int
	}{
		{"connected", http.StatusOK},
		{"invalid", http.StatusBadRequest},
		{"denied", http.StatusBadRequest},
		{"conflict", http.StatusBadRequest},
		{"ended", http.StatusBadRequest},
		{"failed", http.StatusBadGateway},
	}
	for _, c := range cases {
		t.Run(c.outcome, func(t *testing.T) {
			e := ecStart(t, ecOutcome(c.outcome), nil)
			state, code, vendorErr := ecState(), "code-"+uuid.NewString(), "err-"+uuid.NewString()
			rec := e.get(url.Values{"state": {state}, "code": {code}, "error": {vendorErr}})

			if rec.Code != c.status {
				t.Errorf("status = %d, want %d", rec.Code, c.status)
			}
			if got := ecOutcomeOf(t, rec); got != c.outcome {
				t.Errorf("data-outcome = %q, want %q", got, c.outcome)
			}
			call := e.w.only(t)
			if call.method != http.MethodPost || call.path != ecPath {
				t.Errorf("worker call = %s %s, want POST %s", call.method, call.path, ecPath)
			}
			if call.gateway != ecGWToken {
				t.Errorf("X-Gateway-Token = %q, want the gateway token", call.gateway)
			}
			if !strings.HasPrefix(call.contentType, "application/json") {
				t.Errorf("Content-Type = %q, want application/json", call.contentType)
			}
			var body map[string]string
			if err := json.Unmarshal(call.body, &body); err != nil {
				t.Fatalf("worker body %q: %v", call.body, err)
			}
			if len(body) != 3 || body["state"] != state || body["code"] != code || body["error"] != vendorErr {
				t.Errorf("worker body = %v, want exactly state, code and error as received", body)
			}

			h := rec.Header()
			for k, want := range map[string]string{
				"Cache-Control":           "no-store",
				"Referrer-Policy":         "no-referrer",
				"Content-Security-Policy": ecCSP,
			} {
				if got := h.Get(k); got != want {
					t.Errorf("%s = %q, want %q", k, got, want)
				}
			}
			if ct := h.Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
				t.Errorf("Content-Type = %q, want text/html", ct)
			}
			e.wantNoEcho(t, rec, state, code, vendorErr)
		})
	}

	t.Run("a vendor error alone is forwarded with an empty code", func(t *testing.T) {
		e := ecStart(t, ecOutcome("denied"), nil)
		state := ecState()
		rec := e.get(url.Values{"state": {state}, "error": {"access_denied"}})
		if ecOutcomeOf(t, rec) != "denied" {
			t.Errorf("data-outcome = %q, want denied", ecOutcomeOf(t, rec))
		}
		var body map[string]string
		if err := json.Unmarshal(e.w.only(t).body, &body); err != nil || len(body) != 3 || body["code"] != "" || body["error"] != "access_denied" {
			t.Errorf("worker body = %v (%v), want the three fields with an empty code", body, err)
		}
	})
}

func TestERPCallbackPage_WorkerErrorIsFailedAndTimeoutIsUnknown(t *testing.T) {
	hijackAndClose := func(w http.ResponseWriter, _ *http.Request) {
		conn, _, err := w.(http.Hijacker).Hijack()
		if err == nil {
			_ = conn.Close()
		}
	}
	sleepPastTheClient := func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(5 * time.Second):
		}
	}
	cases := []struct {
		name    string
		reply   func(http.ResponseWriter, *http.Request)
		client  *http.Client
		outcome string
	}{
		{"a worker 500", func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = io.WriteString(w, "{\"error\":\"internal server error\"}")
		}, nil, "failed"},
		{"a worker that outlasts the client timeout", sleepPastTheClient, &http.Client{Timeout: 150 * time.Millisecond}, "unknown"},
		{"a worker that closes the connection", hijackAndClose, nil, "unknown"},
		{"an outcome the page has no text for", ecOutcome("bogus"), nil, "unknown"},
		{"a worker refusal that is not a 500", func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusForbidden)
			ecOutcome("connected")(w, r) // a body that reads as success must not count on a refusal
		}, nil, "unknown"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e := ecStart(t, c.reply, c.client)
			state, code, vendorErr := ecState(), "code-"+uuid.NewString(), "err-"+uuid.NewString()
			rec := e.get(url.Values{"state": {state}, "code": {code}, "error": {vendorErr}})

			if rec.Code != http.StatusBadGateway {
				t.Errorf("status = %d, want 502", rec.Code)
			}
			if got := ecOutcomeOf(t, rec); got != c.outcome {
				t.Errorf("data-outcome = %q, want %q", got, c.outcome)
			}
			e.wantNoEcho(t, rec, state, code, vendorErr)
		})
	}

	// The control: a worker that answers in time is not unknown.
	e := ecStart(t, ecOutcome("connected"), &http.Client{Timeout: 5 * time.Second})
	if rec := e.get(url.Values{"state": {ecState()}, "code": {"fake-co-1"}}); rec.Code != http.StatusOK || ecOutcomeOf(t, rec) != "connected" {
		t.Errorf("a prompt worker = %d %q, want 200 connected", rec.Code, ecOutcomeOf(t, rec))
	}
}

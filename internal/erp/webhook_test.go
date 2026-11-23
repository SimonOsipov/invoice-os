package erp

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"

	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
	"github.com/SimonOsipov/invoice-os/internal/platform/queue"
)

// --- fixtures ---------------------------------------------------------------------------

// hookConnector wraps the fake: it counts Capabilities and VerifyWebhook, and a strict one fails
// the test on any other vendor call.
type hookConnector struct {
	Connector
	t        *testing.T
	caps     *Capabilities                             // nil: the fake's
	onVerify func(http.Header, []byte) ([]Hint, error) // nil: the fake verifies
	strict   bool
	verifies atomic.Int64
}

func (h *hookConnector) Capabilities() Capabilities {
	if h.caps != nil {
		return *h.caps
	}
	return h.Connector.Capabilities()
}

func (h *hookConnector) VerifyWebhook(ctx context.Context, hdr http.Header, body []byte) ([]Hint, error) {
	h.verifies.Add(1)
	if h.onVerify != nil {
		return h.onVerify(hdr, body)
	}
	return h.Connector.VerifyWebhook(ctx, hdr, body)
}

func (h *hookConnector) forbid(method string) {
	h.t.Helper()
	if h.strict {
		h.t.Errorf("Receive called %s; it may call only Capabilities and VerifyWebhook", method)
	}
}

func (h *hookConnector) Authorize(ctx context.Context, r AuthorizeRequest) (AuthorizeResult, error) {
	h.forbid("Authorize")
	return h.Connector.Authorize(ctx, r)
}

func (h *hookConnector) Exchange(ctx context.Context, r ExchangeRequest) (ExchangeResult, error) {
	h.forbid("Exchange")
	return h.Connector.Exchange(ctx, r)
}

func (h *hookConnector) Refresh(ctx context.Context, c Credentials) (GrantMaterial, error) {
	h.forbid("Refresh")
	return h.Connector.Refresh(ctx, c)
}

func (h *hookConnector) Validate(ctx context.Context, c Credentials, scope string) error {
	h.forbid("Validate")
	return h.Connector.Validate(ctx, c, scope)
}

func (h *hookConnector) ListChanged(ctx context.Context, c Credentials, scope string, cursor []byte) (Changes, error) {
	h.forbid("ListChanged")
	return h.Connector.ListChanged(ctx, c, scope, cursor)
}

func (h *hookConnector) Fetch(ctx context.Context, c Credentials, ref DocRef) (Document, error) {
	h.forbid("Fetch")
	return h.Connector.Fetch(ctx, c, ref)
}

func (h *hookConnector) FetchParty(ctx context.Context, c Credentials, scope, partyID string) (Party, error) {
	h.forbid("FetchParty")
	return h.Connector.FetchParty(ctx, c, scope, partyID)
}

func (h *hookConnector) WriteBack(ctx context.Context, c Credentials, ref DocRef, wb WriteBack) (DocRef, error) {
	h.forbid("WriteBack")
	return h.Connector.WriteBack(ctx, c, ref, wb)
}

func (h *hookConnector) EnsureSubscriptions(ctx context.Context, c Credentials, scope string) error {
	h.forbid("EnsureSubscriptions")
	return h.Connector.EnsureSubscriptions(ctx, c, scope)
}

func newHookConnector(t *testing.T) *hookConnector {
	t.Helper()
	return &hookConnector{Connector: testFake(t), t: t}
}

// noDBHooks has no pool and no River client: a database read or an enqueue panics.
func noDBHooks(t *testing.T, hc *hookConnector) *Webhooks {
	t.Helper()
	reg, err := NewRegistry(hc)
	if err != nil {
		t.Fatalf("registry: %v", err)
	}
	return &Webhooks{Registry: reg}
}

// receiveNoPanic turns a nil-pool dereference into a test failure that names the cause.
func receiveNoPanic(t *testing.T, w *Webhooks, system string, h http.Header, body []byte) (n int, err error) {
	t.Helper()
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("Receive panicked (%v): it touched the database or the queue before the call could succeed", r)
		}
	}()
	return w.Receive(context.Background(), system, h, body)
}

// signedEvents is a body of events and the header the fake accepts for it.
func signedEvents(events ...Hint) (http.Header, []byte) {
	type event struct {
		Company string `json:"company"`
		Invoice string `json:"invoice"`
	}
	evs := make([]event, 0, len(events))
	for _, e := range events {
		evs = append(evs, event{e.Scope, e.SourceDocID})
	}
	b, _ := json.Marshal(map[string]any{"events": evs})
	return signedHeader(b), b
}

func hookKey(label string) string { return "wh-" + label + "-" + uuid.NewString()[:8] }

type hookEnv struct {
	t          *testing.T
	super, app *pgxpool.Pool
	conn       *hookConnector
	hooks      *Webhooks
}

func newHookEnv(t *testing.T) *hookEnv {
	t.Helper()
	super, app := storePools(t)
	q, err := queue.New(app, queue.Config{})
	if err != nil {
		t.Fatalf("queue: %v", err)
	}
	return newHookEnvOn(t, super, app, q.River())
}

func newHookEnvOn(t *testing.T, super, app *pgxpool.Pool, rv *river.Client[pgx.Tx]) *hookEnv {
	t.Helper()
	e := &hookEnv{t: t, super: super, app: app, conn: newHookConnector(t)}
	reg, err := NewRegistry(e.conn)
	if err != nil {
		t.Fatalf("registry: %v", err)
	}
	e.hooks = &Webhooks{Pool: app, River: rv, Registry: reg}
	return e
}

// firm seeds a tenant with one client and drops the pull jobs the tenant collected.
func (e *hookEnv) firm(name string) (tenant, entity string) {
	e.t.Helper()
	tenant = seedTenant(e.t, e.super, "firm")
	entity = seedClient(e.t, e.super, tenant, name)
	e.t.Cleanup(func() {
		_, _ = e.super.Exec(context.Background(), "DELETE FROM river_job WHERE args->>'tenant_id' = $1", tenant)
	})
	return tenant, entity
}

func (e *hookEnv) active(tenant, entity, key string) string {
	e.t.Helper()
	id, _ := seedActiveConn(e.t, e.super, tenant, entity, key)
	return id
}

func (e *hookEnv) receive(h http.Header, body []byte) (int, error) {
	e.t.Helper()
	return receiveNoPanic(e.t, e.hooks, "fake", h, body)
}

// mustReceive is a Receive that must succeed.
func (e *hookEnv) mustReceive(h http.Header, body []byte) int {
	e.t.Helper()
	n, err := e.receive(h, body)
	if err != nil {
		e.t.Fatalf("Receive: %v", err)
	}
	return n
}

// jobs lists the jobs of kind that name one of conns as "tenant/connection", sorted.
func (e *hookEnv) jobs(kind string, conns ...string) []string {
	e.t.Helper()
	rows, err := e.super.Query(context.Background(),
		"SELECT args FROM river_job WHERE kind = $1 AND args->>'connection_id' = ANY($2::text[])", kind, conns)
	if err != nil {
		e.t.Fatalf("read river_job: %v", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var raw []byte
		var a PullArgs
		if err := rows.Scan(&raw); err != nil {
			e.t.Fatalf("scan: %v", err)
		}
		if err := json.Unmarshal(raw, &a); err != nil {
			e.t.Fatalf("decode: %v", err)
		}
		out = append(out, a.TenantID+"/"+a.ConnectionID)
	}
	slices.Sort(out)
	return out
}

func (e *hookEnv) pulls(conns ...string) []string { return e.jobs("erp.pull", conns...) }

func (e *hookEnv) pullsOfTenant(tenant string) int {
	var n int
	mustScan(e.t, e.super, "SELECT count(*) FROM river_job WHERE kind = 'erp.pull' AND args->>'tenant_id' = $1", []any{tenant}, &n)
	return n
}

// --- Receive: the registry and capability gate (AC-1) ----------------------------------

func TestReceive_UnknownOrWebhooklessSystemIs404WithoutVerify(t *testing.T) {
	body := []byte(`{"events":[]}`)
	cases := []struct {
		name   string
		system string
		caps   *Capabilities
	}{
		{"system not in the registry", "quickbooks", nil},
		{"connector reports no Webhooks", "fake", &Capabilities{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			hc := newHookConnector(t)
			hc.strict, hc.caps = true, tc.caps
			_, err := receiveNoPanic(t, noDBHooks(t, hc), tc.system, signedHeader(body), body)
			if !errors.Is(err, ErrUnknownWebhookSystem) {
				t.Errorf("err = %v, want ErrUnknownWebhookSystem", err)
			}
			if n := hc.verifies.Load(); n != 0 {
				t.Errorf("VerifyWebhook calls = %d, want 0", n)
			}
		})
	}
	t.Run("control: a Webhooks system reaches the verification", func(t *testing.T) {
		hc := newHookConnector(t)
		_, err := receiveNoPanic(t, noDBHooks(t, hc), "fake", http.Header{}, body)
		if !errors.Is(err, ErrWebhookUnverified) || hc.verifies.Load() != 1 {
			t.Errorf("err = %v, VerifyWebhook calls = %d, want ErrWebhookUnverified after 1 call", err, hc.verifies.Load())
		}
	})
}

// --- Receive: verify before any database read (AC-2) -----------------------------------

func TestReceive_UnverifiedWebhookReadsNoDatabase(t *testing.T) {
	_, body := signedEvents(Hint{keyOne, "fake-co-1-inv-2"})
	_, other := signedEvents(Hint{keyOne, "another-body"})
	cases := map[string]http.Header{
		"unsigned":                  {},
		"wrong signature":           {fakeSignatureHeader: {"sha256=" + strings.Repeat("0", 64)}},
		"signature of another body": signedHeader(other),
	}
	for name, h := range cases {
		t.Run(name, func(t *testing.T) {
			hc := newHookConnector(t)
			n, err := receiveNoPanic(t, noDBHooks(t, hc), "fake", h, body)
			if !errors.Is(err, ErrWebhookUnverified) || n != 0 {
				t.Errorf("Receive = %d, %v, want 0 and ErrWebhookUnverified", n, err)
			}
			if hc.verifies.Load() != 1 {
				t.Errorf("VerifyWebhook calls = %d, want 1: the refusal must come from the connector", hc.verifies.Load())
			}
		})
	}
}

func TestReceive_UnverifiedWebhookEnqueuesNothing(t *testing.T) {
	e := newHookEnv(t)
	ten, ent := e.firm("webhook client")
	key := hookKey("unverified")
	conn := e.active(ten, ent, key)
	h, body := signedEvents(Hint{key, key + "-inv-2"})

	bad := http.Header{fakeSignatureHeader: {"sha256=" + strings.Repeat("0", 64)}}
	if _, err := e.receive(bad, body); !errors.Is(err, ErrWebhookUnverified) {
		t.Fatalf("wrong signature: err = %v, want ErrWebhookUnverified", err)
	}
	if got := e.pulls(conn); len(got) != 0 {
		t.Errorf("erp.pull jobs after an unverified webhook = %v, want none", got)
	}
	// The control: the same body, correctly signed, does enqueue, so the silence above was the verification.
	if n := e.mustReceive(h, body); n != 1 {
		t.Errorf("signed control enqueued %d, want 1", n)
	}
	wantPulls(t, e.pulls(conn), ten+"/"+conn)
}

// --- Receive: tenant binding and fan-out (AC-3, AC-4, AC-5, AC-6) -----------------------

func TestReceive_VerifiedHintEnqueuesOnePullInTheTargetTenant(t *testing.T) {
	e := newHookEnv(t)
	ten, ent := e.firm("webhook client")
	key := hookKey("one")
	conn := e.active(ten, ent, key)
	h, body := signedEvents(Hint{key, key + "-inv-2"})

	n, err := e.receive(h, body)
	if n != 1 || err != nil {
		t.Fatalf("Receive = %d, %v, want 1, nil", n, err)
	}
	wantPulls(t, e.pulls(conn), ten+"/"+conn)
	if got := e.jobs("erp.ingest", conn); len(got) != 0 {
		t.Errorf("erp.ingest jobs = %v, want none: a webhook only hints, the pull does the work", got)
	}
}

func TestReceive_TenantComesFromTheLookupNotTheContext(t *testing.T) {
	e := newHookEnv(t)
	tenA, entA := e.firm("webhook client A")
	tenB, entB := e.firm("webhook client B")
	key := hookKey("ctx")
	conn := e.active(tenA, entA, key)
	e.active(tenB, entB, hookKey("ctx-other")) // B is a live tenant with a connection of another key
	h, body := signedEvents(Hint{key, key + "-inv-2"})

	ctxB := auth.WithIdentity(context.Background(), auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: tenB})
	n, err := e.hooks.Receive(ctxB, "fake", h, body)
	if n != 1 || err != nil {
		t.Fatalf("Receive = %d, %v, want 1, nil", n, err)
	}
	wantPulls(t, e.pulls(conn), tenA+"/"+conn)
	if got := e.pullsOfTenant(tenB); got != 0 {
		t.Errorf("tenant B (the context's) has %d erp.pull jobs, want 0", got)
	}
}

func TestReceive_OneKeyOnSeveralTenantsEnqueuesEach(t *testing.T) {
	e := newHookEnv(t)
	tenA, entA1 := e.firm("webhook client A1")
	entA2 := seedClient(t, e.super, tenA, "webhook client A2")
	tenB, entB := e.firm("webhook client B")
	key := hookKey("shared")
	a1, a2, b := e.active(tenA, entA1, key), e.active(tenA, entA2, key), e.active(tenB, entB, key)
	// A third firm holds the same key on a pending connection: it is not a target.
	tenC, entC := e.firm("webhook client C")
	c := seedConnRow(t, e.super, connRow{tenant: tenC, entity: entC, key: key, status: "pending"})
	h, body := signedEvents(Hint{key, key + "-inv-2"})

	n, err := e.receive(h, body)
	if n != 3 || err != nil {
		t.Fatalf("Receive = %d, %v, want 3, nil", n, err)
	}
	wantPulls(t, e.pulls(a1, a2, b, c), tenA+"/"+a1, tenA+"/"+a2, tenB+"/"+b)
}

func TestReceive_UnknownOrInactiveTargetsEnqueueNothing(t *testing.T) {
	cases := []struct {
		name   string
		status string // "": no connection has the key
		want   int
	}{
		{"active (control)", "active", 1},
		{"unknown key", "", 0},
		{"pending", "pending", 0},
		{"reconsent", "reconsent", 0},
		{"disconnected", "disconnected", 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newHookEnv(t)
			ten, ent := e.firm("webhook client")
			key := hookKey("status")
			var conn string
			switch tc.status {
			case "active":
				conn = e.active(ten, ent, key)
			case "":
				conn = uuid.NewString()
			default:
				conn = seedConnRow(t, e.super, connRow{tenant: ten, entity: ent, key: key, status: tc.status})
			}
			h, body := signedEvents(Hint{key, key + "-inv-2"})

			n, err := e.receive(h, body)
			if n != tc.want || err != nil {
				t.Fatalf("Receive = %d, %v, want %d, nil", n, err, tc.want)
			}
			if got := e.pulls(conn); len(got) != tc.want {
				t.Errorf("erp.pull jobs = %v, want %d", got, tc.want)
			}
		})
	}
}

func TestReceive_SkipsALockedWorkspace(t *testing.T) {
	e := newHookEnv(t)
	tenA, entA := e.firm("webhook client A")
	tenB, entB := e.firm("webhook client B")
	key := hookKey("locked")
	a, b := e.active(tenA, entA, key), e.active(tenB, entB, key)
	lockWorkspace(t, e.super, tenA, uuid.NewString())
	h, body := signedEvents(Hint{key, key + "-inv-2"})

	n, err := e.receive(h, body)
	if n != 1 || err != nil {
		t.Fatalf("Receive = %d, %v, want 1, nil", n, err)
	}
	wantPulls(t, e.pulls(a, b), tenB+"/"+b)
}

// --- Receive: duplicates, empty scopes and the cap (AC-7) -------------------------------

func TestReceive_RepeatedWebhooksKeepOnePullInFlight(t *testing.T) {
	inFlight := func(e *hookEnv, conn string) []string {
		rows, err := e.super.Query(context.Background(),
			"SELECT state::text FROM river_job WHERE kind = 'erp.pull' AND args->>'connection_id' = $1", conn)
		if err != nil {
			t.Fatalf("read river_job: %v", err)
		}
		defer rows.Close()
		var states []string
		for rows.Next() {
			var s string
			if err := rows.Scan(&s); err != nil {
				t.Fatalf("scan: %v", err)
			}
			states = append(states, s)
		}
		return states
	}
	final := []string{"completed", "cancelled", "discarded"}
	check := func(t *testing.T, e *hookEnv, conn string) {
		t.Helper()
		states := inFlight(e, conn)
		if len(states) != 1 || slices.Contains(final, states[0]) {
			t.Errorf("erp.pull rows for the connection have states %v, want exactly one in a non-final state", states)
		}
	}

	t.Run("the same signed body three times", func(t *testing.T) {
		e := newHookEnv(t)
		ten, ent := e.firm("webhook client")
		key := hookKey("repeat")
		conn := e.active(ten, ent, key)
		h, body := signedEvents(Hint{key, key + "-inv-2"})
		for i := range 3 {
			if n := e.mustReceive(h, body); n != 1 {
				t.Errorf("Receive #%d = %d, want 1 (enqueued or already in flight)", i+1, n)
			}
		}
		check(t, e, conn)
	})
	t.Run("one key five times plus an empty company", func(t *testing.T) {
		e := newHookEnv(t)
		ten, ent := e.firm("webhook client")
		key := hookKey("dup")
		conn := e.active(ten, ent, key)
		h, body := signedEvents(Hint{key, "a"}, Hint{key, "b"}, Hint{"", "c"}, Hint{key, "d"}, Hint{key, "e"}, Hint{key, "f"})
		if n := e.mustReceive(h, body); n != 1 {
			t.Errorf("Receive = %d, want 1", n)
		}
		check(t, e, conn)
	})
}

func TestReceive_LooksUpAtMostFiftyScopes(t *testing.T) {
	e := newHookEnv(t)
	ten, ent := e.firm("webhook client")
	prefix := hookKey("cap")
	var conns []string
	// Hint order: an empty scope, then every scope twice, so the cap counts distinct non-empty scopes.
	hints := []Hint{{"", "x"}}
	for i := range 60 {
		key := fmt.Sprintf("%s-%02d", prefix, i)
		conns = append(conns, e.active(ten, ent, key))
		hints = append(hints, Hint{key, "a"}, Hint{key, "b"})
	}
	e.conn.onVerify = func(http.Header, []byte) ([]Hint, error) { return hints, nil }

	n, err := e.receive(http.Header{}, []byte("{}"))
	if n != 50 || err != nil {
		t.Fatalf("Receive = %d, %v, want 50, nil", n, err)
	}
	var want []string
	for _, c := range conns[:50] {
		want = append(want, ten+"/"+c)
	}
	wantPulls(t, e.pulls(conns...), want...)
}

func TestReceive_SkipsWhenTheSweepAlreadyQueuedThePull(t *testing.T) {
	s := newSweepEnv(t)
	e := newHookEnvOn(t, s.super, s.sweeper.Pool, s.sweeper.River)
	ten, ent := e.firm("webhook client")
	key := hookKey("sweep")
	conn := e.active(ten, ent, key)
	s.sweep()
	wantPulls(t, e.pulls(conn), ten+"/"+conn)

	h, body := signedEvents(Hint{key, key + "-inv-2"})
	if n := e.mustReceive(h, body); n != 1 {
		t.Errorf("Receive = %d, want 1 (found in flight)", n)
	}
	wantPulls(t, e.pulls(conn), ten+"/"+conn)
}

// --- Receive: no vendor call but the verification (AC-8) --------------------------------

func TestReceive_CallsNoVendorMethodButVerify(t *testing.T) {
	e := newHookEnv(t)
	e.conn.strict = true
	ten, ent := e.firm("webhook client")
	key := hookKey("strict")
	conn := e.active(ten, ent, key)
	h, body := signedEvents(Hint{key, key + "-inv-2"})

	if n := e.mustReceive(h, body); n != 1 {
		t.Fatalf("Receive = %d, want 1: the strict connector must see a full verified receive", n)
	}
	wantPulls(t, e.pulls(conn), ten+"/"+conn)
	if e.conn.verifies.Load() != 1 {
		t.Errorf("VerifyWebhook calls = %d, want 1", e.conn.verifies.Load())
	}
}

// --- Test needles (AC-10) ---------------------------------------------------------------

const (
	needleBody = "NEEDLE-body-31ac"
	needleSig  = "NEEDLE-sig-77e0"
	needleAddr = "203.0.113.77"
	needleKey  = "needle-key-9d2e"
)

// --- WebhookHandler (AC-9, AC-10) -------------------------------------------------------

type recvCall struct {
	system string
	header http.Header
	body   []byte
}

// recorder is a receive stub that records its calls and answers n, err.
type recorder struct {
	calls []recvCall
	n     int
	err   error
}

func (r *recorder) receive(_ context.Context, system string, h http.Header, body []byte) (int, error) {
	r.calls = append(r.calls, recvCall{system, h, slices.Clone(body)})
	return r.n, r.err
}

func hookEnvelope(t *testing.T, h http.Header, body []byte) []byte {
	t.Helper()
	b, err := json.Marshal(struct {
		Headers http.Header `json:"headers"`
		Body    []byte      `json:"body"`
	}{h, body})
	if err != nil {
		t.Fatalf("marshal envelope: %v", err)
	}
	return b
}

func serveHook(rec *recorder, log *slog.Logger, system string, raw []byte, extra http.Header) *httptest.ResponseRecorder {
	m := http.NewServeMux()
	m.Handle("POST /internal/erp/webhooks/{system}", WebhookHandler(rec.receive, log))
	r := httptest.NewRequest(http.MethodPost, "/internal/erp/webhooks/"+system, bytes.NewReader(raw))
	for k, v := range extra {
		r.Header[k] = v
	}
	w := httptest.NewRecorder()
	m.ServeHTTP(w, r)
	return w
}

var discardLog = slog.New(slog.NewTextHandler(io.Discard, nil))

func TestWebhookHandler_StatusMapping(t *testing.T) {
	valid := hookEnvelope(t, http.Header{}, []byte("{}"))
	cases := []struct {
		name   string
		raw    []byte
		n      int
		err    error
		status int
		called bool
	}{
		{"body not JSON", []byte("not json"), 0, nil, http.StatusBadRequest, false},
		{"body not base64", []byte(`{"headers":{},"body":"!!not base64!!"}`), 0, nil, http.StatusBadRequest, false},
		{"envelope over 128 KiB", []byte(`{"headers":{},"body":"` + strings.Repeat("A", webhookEnvelopeLimit) + `"}`), 0, nil, http.StatusRequestEntityTooLarge, false},
		{"unknown system", valid, 0, fmt.Errorf("receive: %w", ErrUnknownWebhookSystem), http.StatusNotFound, true},
		{"unverified", valid, 0, fmt.Errorf("receive: %w", ErrWebhookUnverified), http.StatusUnauthorized, true},
		{"other error", valid, 0, errors.New("lookup failed"), http.StatusInternalServerError, true},
		{"nothing enqueued", valid, 0, nil, http.StatusOK, true},
		{"two enqueued", valid, 2, nil, http.StatusOK, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := &recorder{n: tc.n, err: tc.err}
			w := serveHook(rec, discardLog, "fake", tc.raw, nil)
			if w.Code != tc.status {
				t.Errorf("status = %d, want %d", w.Code, tc.status)
			}
			if got := len(rec.calls) > 0; got != tc.called {
				t.Errorf("receive called = %v, want %v", got, tc.called)
			}
			if tc.status == http.StatusOK {
				if got := strings.TrimSpace(w.Body.String()); got != `{"outcome":"accepted"}` {
					t.Errorf("body = %s, want {\"outcome\":\"accepted\"}", got)
				}
			}
		})
	}
}

// The gateway forwards at most 64 KiB of body and 16 KiB of JSON-encoded headers.
func TestWebhookHandler_AcceptsTheLargestForwardedEnvelope(t *testing.T) {
	const name = "X-Fake-Signature"
	overhead := len(mustJSON(t, http.Header{name: {""}}))
	room := 16384 - overhead
	// "<" encodes to 6 bytes, so the filler makes the encoded headers exactly 16,384 bytes.
	hdr := http.Header{name: {strings.Repeat("<", room/6) + strings.Repeat("a", room%6)}}
	if got := len(mustJSON(t, hdr)); got != 16384 {
		t.Fatalf("fixture: encoded headers = %d bytes, want 16384", got)
	}
	body := make([]byte, 65536)
	for i := range body {
		body[i] = byte(i * 7)
	}
	raw := hookEnvelope(t, hdr, body)
	if len(raw) != 103790 {
		t.Fatalf("fixture: envelope = %d bytes, want 103790", len(raw))
	}

	rec := &recorder{n: 1}
	w := serveHook(rec, discardLog, "fake", raw, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 for the largest envelope the gateway forwards", w.Code)
	}
	if len(rec.calls) != 1 || !bytes.Equal(rec.calls[0].body, body) {
		t.Errorf("receive calls = %d, want 1 with the 65536 body bytes intact", len(rec.calls))
	}
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return b
}

func TestWebhookHandler_PassesSystemHeadersAndExactBody(t *testing.T) {
	body := []byte{0x00, 0xff, 0xfe, '{', 0x80, '\n', 0x00}
	sig := "sha256=abc123"
	raw := hookEnvelope(t, http.Header{"X-Fake-Signature": {sig}, "X-Other": {"1", "2"}}, body)

	rec := &recorder{n: 1}
	w := serveHook(rec, discardLog, "acme", raw, nil)
	if w.Code != http.StatusOK || len(rec.calls) != 1 {
		t.Fatalf("status = %d, receive calls = %d, want 200 and 1", w.Code, len(rec.calls))
	}
	got := rec.calls[0]
	if got.system != "acme" {
		t.Errorf("system = %q, want the path's acme", got.system)
	}
	if got.header.Get("X-Fake-Signature") != sig || !slices.Equal(got.header.Values("X-Other"), []string{"1", "2"}) {
		t.Errorf("headers = %v, want the envelope's", got.header)
	}
	if !bytes.Equal(got.body, body) {
		t.Errorf("body = %x, want %x byte for byte", got.body, body)
	}
}

func TestWebhookHandler_LogsNoBodyHeaderKeyOrAddress(t *testing.T) {
	var logs bytes.Buffer
	log := slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	events := `{"events":[{"company":"` + needleKey + `","invoice":"` + needleBody + `"}]}`
	hdrs := http.Header{"X-Fake-Signature": {needleSig}, "X-Forwarded-For": {needleAddr}}
	good := hookEnvelope(t, hdrs, []byte(events))
	forwarded := http.Header{"X-Forwarded-For": {needleAddr}, "X-Fake-Signature": {needleSig}}
	cases := []struct {
		name   string
		raw    []byte
		n      int
		err    error
		status int
	}{
		{"200", good, 1, nil, http.StatusOK},
		{"401", good, 0, fmt.Errorf("%w: key %s from %s", ErrWebhookUnverified, needleKey, needleAddr), http.StatusUnauthorized},
		{"404", good, 0, fmt.Errorf("%w: %s", ErrUnknownWebhookSystem, needleBody), http.StatusNotFound},
		{"500", good, 0, fmt.Errorf("lookup %s: %s %s", needleKey, needleSig, needleAddr), http.StatusInternalServerError},
		{"400", []byte("{" + events + needleBody), 0, nil, http.StatusBadRequest},
		{"413", []byte(`{"headers":{},"body":"` + strings.Repeat("A", webhookEnvelopeLimit) + needleBody + `"}`), 0, nil, http.StatusRequestEntityTooLarge},
	}
	for _, tc := range cases {
		rec := &recorder{n: tc.n, err: tc.err}
		if w := serveHook(rec, log, "fake", tc.raw, forwarded); w.Code != tc.status {
			t.Fatalf("%s path: status = %d, want %d", tc.name, w.Code, tc.status)
		}
	}
	if !strings.Contains(logs.String(), "erp: webhook received") {
		t.Fatalf("the 200 path logged nothing to check: %q", logs.String())
	}
	for _, needle := range []string{needleBody, needleSig, needleAddr, needleKey, base64.StdEncoding.EncodeToString([]byte(events))} {
		if strings.Contains(logs.String(), needle) {
			t.Errorf("the handler logged %q:\n%s", needle, logs.String())
		}
	}
}

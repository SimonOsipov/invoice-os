// M4-07-03 (task-157): QA adversarial coverage ON TOP OF DASH-30..35
// (handlers_test.go), written during the Mode B (post-implementation) verify
// pass. The 6 shipped specs prove the happy path, empty-array serialization,
// the three auth/error branches, and nil-logger tolerance; this file closes
// gaps they don't touch: that method filtering is NOT the handler's job (it's
// enforced by the "GET /v1/rollup" mux pattern string, wired in M4-07-04's
// cmd/dashboard/main.go — not yet present in this codebase), that the
// context.Context passed to rollup is the real request context (not a
// disconnected/background one, so client cancellation propagates), that
// errors.Is (not ==) is what maps a wrapped db.ErrNoTenant to 401, a
// large/realistic body round-trips exactly with the documented wire field
// names, that Bucket's anonymous embedding promotes counts/needs_attention to
// the Client row's top level (not nested under "bucket"/"Bucket"), and that
// omitempty is absent from Counts (all 7 zero keys always present). Reuses
// doRollup/rollupBody from handlers_test.go (same package).
package dashboard

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
	"github.com/SimonOsipov/invoice-os/internal/platform/db"
)

// TestRollupHandler_MethodAgnostic: RollupHandler is a plain http.HandlerFunc
// with no r.Method check of its own -- every one of DASH-30..35's requests
// uses http.MethodGet, but nothing in handlers.go inspects r.Method. Proven
// here by driving the SAME handler instance with POST/PUT/DELETE and
// observing it still runs rollup and answers 200 exactly as GET would.
// Method filtering is NOT this handler's responsibility: it is enforced
// entirely by the Go 1.22 enhanced-ServeMux pattern string "GET /v1/rollup"
// that M4-07-04 wires in cmd/dashboard/main.go (Stage 1 architecture note
// #5) -- a bare `http.HandlerFunc` passed directly to httptest, as this test
// (and every other test in this file/handlers_test.go) does, never sees that
// mux and so never 405s. This test documents that boundary rather than
// inventing a 405 assertion the handler was never specified to produce.
func TestRollupHandler_MethodAgnostic(t *testing.T) {
	id := auth.Identity{Subject: "user-1", Role: "authenticated", TenantID: uuid.NewString()}
	want := Rollup{Clients: []Client{}, TopViolations: []RuleCount{}}

	for _, method := range []string{http.MethodGet, http.MethodPost, http.MethodPut, http.MethodDelete} {
		t.Run(method, func(t *testing.T) {
			calls := 0
			rollup := func(ctx context.Context) (Rollup, error) {
				calls++
				return want, nil
			}
			r := httptest.NewRequest(method, "/v1/rollup", nil)
			r = r.WithContext(auth.WithIdentity(r.Context(), id))
			rec := httptest.NewRecorder()
			RollupHandler(rollup, nil).ServeHTTP(rec, r)

			if rec.Code != http.StatusOK {
				t.Errorf("method %s: status = %d, want 200 (handler itself does not filter by method; body=%s)", method, rec.Code, rec.Body.String())
			}
			if calls != 1 {
				t.Errorf("method %s: rollup called %d times, want 1", method, calls)
			}
		})
	}
}

// ctxKey is a private type for the test-only context value below, avoiding
// collision with any production context key.
type ctxKey string

// TestRollupHandler_PropagatesRequestContext (context propagation): the ctx
// passed to rollup must be derived from r.Context() -- not context.Background()
// or some other disconnected context -- so that a cancelled client request
// (or any value threaded onto the request context upstream, e.g. by
// middleware) is visible inside rollup. Proven two ways: (1) a value set on
// the request's context before ServeHTTP is readable inside rollup, and (2)
// cancelling the request's context before ServeHTTP causes rollup to observe
// ctx.Err() != nil.
func TestRollupHandler_PropagatesRequestContext(t *testing.T) {
	id := auth.Identity{Subject: "user-1", Role: "authenticated", TenantID: uuid.NewString()}

	t.Run("value visible", func(t *testing.T) {
		var sawValue any
		rollup := func(ctx context.Context) (Rollup, error) {
			sawValue = ctx.Value(ctxKey("probe"))
			return Rollup{Clients: []Client{}, TopViolations: []RuleCount{}}, nil
		}
		r := httptest.NewRequest(http.MethodGet, "/v1/rollup", nil)
		r = r.WithContext(context.WithValue(auth.WithIdentity(r.Context(), id), ctxKey("probe"), "marker"))
		rec := httptest.NewRecorder()
		RollupHandler(rollup, nil).ServeHTTP(rec, r)

		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
		}
		if sawValue != "marker" {
			t.Errorf("ctx value inside rollup = %v, want %q -- handler is not passing r.Context() through", sawValue, "marker")
		}
	})

	t.Run("cancellation visible", func(t *testing.T) {
		var sawErr error
		rollup := func(ctx context.Context) (Rollup, error) {
			sawErr = ctx.Err()
			return Rollup{Clients: []Client{}, TopViolations: []RuleCount{}}, nil
		}
		r := httptest.NewRequest(http.MethodGet, "/v1/rollup", nil)
		ctx, cancel := context.WithCancel(auth.WithIdentity(r.Context(), id))
		r = r.WithContext(ctx)
		cancel()
		rec := httptest.NewRecorder()
		RollupHandler(rollup, nil).ServeHTTP(rec, r)

		if sawErr == nil {
			t.Error("ctx.Err() inside rollup = nil, want context.Canceled -- handler is not passing the request's (cancellable) context through")
		}
	})
}

// TestRollupHandler_WrappedErrNoTenantStillMaps401 (statusForErr uses
// errors.Is, not ==): a store error that WRAPS db.ErrNoTenant via %w, rather
// than returning the sentinel bare, must still map to 401. A `==` comparison
// would fail this and fall through to 500 -- exactly the regression
// errors.Is guards against.
func TestRollupHandler_WrappedErrNoTenantStillMaps401(t *testing.T) {
	id := auth.Identity{Subject: "user-1", Role: "authenticated", TenantID: uuid.NewString()}
	wrapped := fmt.Errorf("store: rollup: %w", db.ErrNoTenant)
	rollup := func(ctx context.Context) (Rollup, error) {
		return Rollup{}, wrapped
	}
	rec, resp := doRollup(t, rollup, &id)

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401 for a wrapped db.ErrNoTenant (body=%s)", rec.Code, rec.Body.String())
	}
	if resp.Error != "unauthorized" {
		t.Errorf("decoded error = %q, want %q", resp.Error, "unauthorized")
	}
	raw := rec.Body.String()
	if got := strings.TrimSpace(raw); got != `{"error":"unauthorized"}` {
		t.Errorf(`body = %s, want exactly {"error":"unauthorized"}`, got)
	}
}

// TestRollupHandler_LargeBodyRoundTripsWireContract: a Rollup with many
// clients and many top_violations must round-trip exactly through the
// handler with the documented wire field names (entity_id, entity_name,
// counts, needs_attention, rule_key, invoices, totals, clients,
// top_violations) -- pins the wire contract M4-10 and the E2E spec depend on.
// Checked two ways: (1) reflect.DeepEqual on the decoded struct, matching
// DASH-30's approach at a larger fanout, and (2) a raw-string scan for every
// field name key, so a future rename that DeepEqual alone wouldn't catch
// (e.g. swapping json tags between two same-typed fields) still fails loudly.
func TestRollupHandler_LargeBodyRoundTripsWireContract(t *testing.T) {
	id := auth.Identity{Subject: "user-1", Role: "authenticated", TenantID: uuid.NewString()}

	const nClients = 25
	const nRules = 10
	clients := make([]Client, nClients)
	for i := 0; i < nClients; i++ {
		clients[i] = Client{
			EntityID:   uuid.NewString(),
			EntityName: fmt.Sprintf("Client %02d Ltd", i),
			Bucket: Bucket{
				Counts: Counts{
					Draft: i, Validated: i + 1, Queued: i + 2, Submitted: i + 3,
					Accepted: i + 4, Rejected: i % 3, Failed: i % 2,
				},
				NeedsAttention: i % 5,
			},
		}
	}
	rules := make([]RuleCount, nRules)
	for i := 0; i < nRules; i++ {
		rules[i] = RuleCount{RuleKey: fmt.Sprintf("rule-key-%02d", i), Invoices: (nRules - i) * 3}
	}
	want := Rollup{
		Totals: Bucket{
			Counts:         Counts{Draft: 100, Validated: 200, Queued: 0, Submitted: 0, Accepted: 0, Rejected: 0, Failed: 0},
			NeedsAttention: 42,
		},
		Clients:       clients,
		TopViolations: rules,
	}
	rollup := func(ctx context.Context) (Rollup, error) { return want, nil }

	r := httptest.NewRequest(http.MethodGet, "/v1/rollup", nil)
	r = r.WithContext(auth.WithIdentity(r.Context(), id))
	rec := httptest.NewRecorder()
	RollupHandler(rollup, nil).ServeHTTP(rec, r)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
	}

	var got Rollup
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode response: %v (body=%s)", err, rec.Body.String())
	}
	if len(got.Clients) != nClients {
		t.Fatalf("decoded %d clients, want %d", len(got.Clients), nClients)
	}
	if len(got.TopViolations) != nRules {
		t.Fatalf("decoded %d top_violations, want %d", len(got.TopViolations), nRules)
	}
	for i := range clients {
		if got.Clients[i] != clients[i] {
			t.Errorf("client[%d] = %+v, want %+v", i, got.Clients[i], clients[i])
		}
	}
	for i := range rules {
		if got.TopViolations[i] != rules[i] {
			t.Errorf("top_violations[%d] = %+v, want %+v", i, got.TopViolations[i], rules[i])
		}
	}

	raw := rec.Body.String()
	for _, key := range []string{
		`"totals"`, `"clients"`, `"top_violations"`, `"entity_id"`,
		`"entity_name"`, `"counts"`, `"needs_attention"`, `"rule_key"`, `"invoices"`,
	} {
		if !strings.Contains(raw, key) {
			t.Errorf("response body missing wire key %s", key)
		}
	}
}

// TestRollupHandler_BucketFieldsPromotedToClientTopLevel (anonymous
// embedding): Client embeds Bucket anonymously so encoding/json promotes
// counts + needs_attention to the client row's TOP level, alongside
// entity_id/entity_name -- never nested under a "Bucket" or "bucket" key.
// This is exactly the kind of thing anonymous struct embedding gets wrong
// silently if a future edit accidentally names the field (Bucket Bucket
// with a `bucket` json tag), so this test decodes into a raw map and
// asserts on the top-level key set directly, rather than trusting the typed
// Client struct (which would happily decode either shape).
func TestRollupHandler_BucketFieldsPromotedToClientTopLevel(t *testing.T) {
	id := auth.Identity{Subject: "user-1", Role: "authenticated", TenantID: uuid.NewString()}
	entityID := uuid.NewString()
	rollup := func(ctx context.Context) (Rollup, error) {
		return Rollup{
			Clients: []Client{
				{
					EntityID:   entityID,
					EntityName: "Promoted Fields Ltd",
					Bucket: Bucket{
						Counts:         Counts{Draft: 3},
						NeedsAttention: 1,
					},
				},
			},
			TopViolations: []RuleCount{},
		}, nil
	}
	rec, _ := doRollup(t, rollup, &id)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
	}

	var decoded struct {
		Clients []map[string]json.RawMessage `json:"clients"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &decoded); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(decoded.Clients) != 1 {
		t.Fatalf("decoded %d clients, want 1", len(decoded.Clients))
	}
	row := decoded.Clients[0]

	for _, key := range []string{"entity_id", "entity_name", "counts", "needs_attention"} {
		if _, ok := row[key]; !ok {
			t.Errorf("client row missing top-level key %q (raw keys: %v) -- Bucket embedding must promote counts/needs_attention, not nest them", key, keysOf(row))
		}
	}
	for _, forbidden := range []string{"bucket", "Bucket"} {
		if _, ok := row[forbidden]; ok {
			t.Errorf("client row has a nested %q key -- Bucket must be embedded anonymously, promoting its fields, not nested under its type name", forbidden)
		}
	}
}

func keysOf(m map[string]json.RawMessage) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	return keys
}

// TestRollupHandler_CountsNeverOmitsZeroKeys: no field in Counts carries
// `omitempty`, so a client whose bucket has every state at zero must still
// serialize all 7 keys explicitly, not drop them. DASH-30 already checks
// this for a MIXED (some-nonzero) bucket; this test closes the gap for the
// ALL-zero case, which is the one omitempty would actually behave
// differently on.
func TestRollupHandler_CountsNeverOmitsZeroKeys(t *testing.T) {
	id := auth.Identity{Subject: "user-1", Role: "authenticated", TenantID: uuid.NewString()}
	rollup := func(ctx context.Context) (Rollup, error) {
		return Rollup{
			Clients: []Client{{
				EntityID:   uuid.NewString(),
				EntityName: "All Zero Ltd",
				Bucket:     Bucket{Counts: Counts{}, NeedsAttention: 0},
			}},
			TopViolations: []RuleCount{},
		}, nil
	}
	rec, _ := doRollup(t, rollup, &id)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
	}
	raw := rec.Body.String()
	for _, key := range []string{
		`"draft":0`, `"validated":0`, `"queued":0`, `"submitted":0`,
		`"accepted":0`, `"rejected":0`, `"failed":0`, `"needs_attention":0`,
	} {
		if !strings.Contains(raw, key) {
			t.Errorf("all-zero client row missing %s (omitempty must not be present on Counts/Bucket fields): %s", key, raw)
		}
	}
}

// AUDIT-04-07: the §2 parameter rules and the response envelope. No DB — the store is a
// fake that RECORDS the Filter it was handed, which is the only way to assert that a
// parameter reached the store as the right value rather than merely that the request
// returned 200.
//
// Helpers use an hnd* prefix; plan/trigger/scoped/reader/page/filt/fsql/act/fct/fctsql/inv
// are taken.
package audit_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/SimonOsipov/invoice-os/internal/audit"
	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
	"github.com/SimonOsipov/invoice-os/internal/platform/db"
)

// --- harness ------------------------------------------------------------------------------

// hndSpy is the injected store. calls counts invocations so a case can assert the store was
// never reached — which is what "a 400 raised BEFORE the store is called" actually means.
type hndSpy struct {
	got   audit.Filter
	calls int
	resp  audit.Response
	err   error
}

func (s *hndSpy) list(_ context.Context, f audit.Filter) (audit.Response, error) {
	s.got = f
	s.calls++
	return s.resp, s.err
}

// hndTenant is a fixed identity; these cases never touch a database, so the value only has
// to be present.
var hndTenant = auth.Identity{TenantID: "11111111-1111-1111-1111-111111111111"}

// hndGet issues an authenticated GET with the given raw query string.
func hndGet(t *testing.T, spy *hndSpy, rawQuery string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(http.MethodGet, "/v1/audit-log?"+rawQuery, nil)
	r = r.WithContext(auth.WithIdentity(r.Context(), hndTenant))
	w := httptest.NewRecorder()
	audit.ListHandler(spy.list, nil)(w, r)
	return w
}

// hndBody decodes a response body into a generic map.
func hndBody(t *testing.T, w *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var out map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode body %q: %v", w.Body.String(), err)
	}
	return out
}

// hndRejects asserts a 400 carrying an {"error": ...} body, raised without reaching the
// store.
func hndRejects(t *testing.T, rawQuery string) {
	t.Helper()
	spy := &hndSpy{}
	w := hndGet(t, spy, rawQuery)
	if w.Code != http.StatusBadRequest {
		t.Errorf("%s -> %d, want 400", rawQuery, w.Code)
	}
	if _, ok := hndBody(t, w)["error"]; !ok {
		t.Errorf("%s -> body %s, want an {\"error\": ...} envelope", rawQuery, w.Body.String())
	}
	if spy.calls != 0 {
		t.Errorf("%s reached the store %d times; a malformed value must be refused before the "+
			"store runs, or a wrong page ships with a plausible total", rawQuery, spy.calls)
	}
}

// hndAccepts asserts a 200 that reached the store exactly once, and returns the Filter the
// handler built.
func hndAccepts(t *testing.T, rawQuery string) audit.Filter {
	t.Helper()
	spy := &hndSpy{}
	w := hndGet(t, spy, rawQuery)
	if w.Code != http.StatusOK {
		t.Fatalf("%s -> %d (%s), want 200", rawQuery, w.Code, w.Body.String())
	}
	if spy.calls != 1 {
		t.Fatalf("%s reached the store %d times, want exactly 1", rawQuery, spy.calls)
	}
	return spy.got
}

// --- AC #1: the envelope --------------------------------------------------------------------

// TestAuditListHandler_EnvelopeHasExactlyTheDesignedKeys is AC #1. The store's response is
// rendered whole: extra keys would be a wire change nothing else pins, and a missing one
// breaks a client that reads it.
func TestAuditListHandler_EnvelopeHasExactlyTheDesignedKeys(t *testing.T) {
	entity := uuid.NewString()
	company := "Lagos Freight Ltd"
	cursor := "MTc1NjE5"
	spy := &hndSpy{resp: audit.Response{
		Events: []audit.Event{{
			ID: "918342", CreatedAt: time.Now().UTC(), Event: "invoice.created",
			Actor: hndTenant.TenantID, ActorName: "Adaeze Okonkwo", ActorKind: "person",
			EntityID: &entity, CompanyName: &company, CompanyScope: audit.ScopeCompany,
			Payload: json.RawMessage(`{"id":"x"}`),
		}},
		Page:   audit.PageInfo{Limit: 25, HasMore: true, NextCursor: &cursor},
		Total:  53,
		Facets: audit.Facets{Event: []audit.Facet{}, Actor: []audit.Facet{}, Company: []audit.Facet{}},
	}}

	w := hndGet(t, spy, "")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d (%s), want 200", w.Code, w.Body.String())
	}

	body := hndBody(t, w)
	want := []string{"events", "page", "total", "log_is_empty", "facets"}
	if len(body) == 0 {
		t.Fatalf("the response body has no keys at all")
	}
	if len(body) != len(want) {
		t.Errorf("the envelope has %d top-level keys (%v), want exactly %d: %v",
			len(body), hndKeys(body), len(want), want)
	}
	for _, k := range want {
		if _, ok := body[k]; !ok {
			t.Errorf("the envelope is missing %q; keys are %v", k, hndKeys(body))
		}
	}

	// The row must carry the store's values, not a re-derived shape.
	events, _ := body["events"].([]any)
	if len(events) != 1 {
		t.Fatalf("events = %v, want the one row the store returned", body["events"])
	}
	row, _ := events[0].(map[string]any)
	if row["company_scope"] != string(audit.ScopeCompany) {
		t.Errorf("company_scope = %v, want %q — every row carries it (AC #1)",
			row["company_scope"], audit.ScopeCompany)
	}
	if row["id"] != "918342" {
		t.Errorf("id = %v (%T), want the string \"918342\" — a bigint above 2^53 loses precision "+
			"as a JSON number", row["id"], row["id"])
	}
	if got := body["total"]; got != float64(53) {
		t.Errorf("total = %v, want 53 — the handler must render the store's total", got)
	}
}

// hndKeys lists a decoded body's top-level keys for failure messages.
func hndKeys(body map[string]any) []string {
	out := make([]string, 0, len(body))
	for k := range body {
		out = append(out, k)
	}
	return out
}

// --- AC #2: identity first ------------------------------------------------------------------

// TestAuditListHandler_UnauthenticatedIs401BeforeParsing is AC #2. The limit is malformed
// too, so a handler that parsed first would answer 400 and leak that the parameter was
// read at all.
func TestAuditListHandler_UnauthenticatedIs401BeforeParsing(t *testing.T) {
	spy := &hndSpy{}
	r := httptest.NewRequest(http.MethodGet, "/v1/audit-log?limit=abc", nil) // no identity
	w := httptest.NewRecorder()
	audit.ListHandler(spy.list, nil)(w, r)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("status = %d (%s), want 401 — identity is checked before any parameter",
			w.Code, w.Body.String())
	}
	if spy.calls != 0 {
		t.Errorf("the store ran %d times on an unauthenticated request, want 0", spy.calls)
	}
}

// --- AC #3, #5, #6: the 400 table -----------------------------------------------------------

// TestAuditListHandler_MalformedParamsAre400 is AC #3, and carries AC #5's bogus-company
// and AC #6's actor+actor_kind rows. Every §2 parameter appears at least once.
func TestAuditListHandler_MalformedParamsAre400(t *testing.T) {
	overCap := strings.TrimSuffix(strings.Repeat("event=x&", maxFilterValuesForTest+1), "&")
	overCapActors := strings.TrimSuffix(strings.Repeat("actor=x&", maxFilterValuesForTest+1), "&")

	cases := []struct {
		name  string
		query string
	}{
		{"limit not an integer", "limit=abc"},
		{"limit below 1", "limit=0"},
		{"limit negative", "limit=-3"},
		{"malformed cursor", "cursor=!!!not-base64!!!"},
		{"unparseable from", "from=yesterday"},
		{"unparseable to", "to=2026-13-45"},
		{"over-cap event", overCap},
		{"over-cap actor", overCapActors},
		{"unknown actor_kind", "actor_kind=robot"},
		{"actor with actor_kind", "actor=" + uuid.NewString() + "&actor_kind=people"},
		{"bogus company", "company=not-a-uuid-or-workspace"},
		{"over-length q", "q=" + strings.Repeat("z", maxFilterTextLenForTest+1)},
		{"non-uuid invoice_id", "invoice_id=not-a-uuid"},
	}
	if len(cases) < 12 {
		t.Fatalf("the malformed table has %d rows, want at least the 12 §2 names", len(cases))
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) { hndRejects(t, c.query) })
	}
}

// maxFilterTextLenForTest and maxFilterValuesForTest mirror the handler's unexported caps.
// A test package cannot read them, and hardcoding the numbers here is what makes a silent
// change to either cap visible.
const (
	maxFilterTextLenForTest = 200
	maxFilterValuesForTest  = 50
)

// TestAuditListHandler_AtTheCapIsAccepted fences the boundary the table above only probes
// from outside: an off-by-one in either cap would reject a legal request, and no case in
// TestAuditListHandler_MalformedParamsAre400 could see it.
func TestAuditListHandler_AtTheCapIsAccepted(t *testing.T) {
	atCap := strings.TrimSuffix(strings.Repeat("event=x&", maxFilterValuesForTest), "&")
	if got := hndAccepts(t, atCap); len(got.Events) != maxFilterValuesForTest {
		t.Errorf("exactly %d event values produced %d in the Filter, want all of them",
			maxFilterValuesForTest, len(got.Events))
	}
	if got := hndAccepts(t, "q="+strings.Repeat("z", maxFilterTextLenForTest)); len(got.Q) != maxFilterTextLenForTest {
		t.Errorf("a q of exactly %d chars produced %d in the Filter", maxFilterTextLenForTest, len(got.Q))
	}
}

// TestAuditListHandler_EmptyParamValueAppliesNoFilter is AC #3's other half, carried
// verbatim from internal/invoice/handlers.go:572-582: empty is ABSENT, not a 400. The
// Filter is compared field by field against the no-parameter one, because a 200 alone
// would not show that a filter was applied.
func TestAuditListHandler_EmptyParamValueAppliesNoFilter(t *testing.T) {
	bare := hndAccepts(t, "")
	empty := hndAccepts(t, "event=&actor=&q=&company=&from=&to=&invoice_id=&actor_kind=&cursor=&limit=")

	if len(empty.Events) != 0 || len(empty.Actors) != 0 {
		t.Errorf("empty repeated values produced Events=%v Actors=%v, want both empty",
			empty.Events, empty.Actors)
	}
	if empty.Q != "" || empty.InvoiceID != "" || empty.ActorKind != "" {
		t.Errorf("empty scalars produced Q=%q InvoiceID=%q ActorKind=%q, want all empty",
			empty.Q, empty.InvoiceID, empty.ActorKind)
	}
	if !empty.From.IsZero() || !empty.To.IsZero() {
		t.Errorf("empty dates produced From=%v To=%v, want both zero", empty.From, empty.To)
	}
	if empty.Cursor != nil {
		t.Errorf("an empty cursor produced %v, want nil", empty.Cursor)
	}
	if empty.Company.Mode() != bare.Company.Mode() {
		t.Errorf("company= produced mode %v but no company param produced %v; empty must mean "+
			"absent", empty.Company.Mode(), bare.Company.Mode())
	}
	// limit= empty must fall back to the default, not to zero.
	if empty.Limit != bare.Limit {
		t.Errorf("limit= produced %d but no limit param produced %d; empty is absent, so both "+
			"must be the default", empty.Limit, bare.Limit)
	}
}

// --- AC #4: the limit rules -------------------------------------------------------------------

// TestAuditListHandler_LimitDefaultsClampsAndRejects is AC #4. The clamp is asserted on the
// Filter the store received, not on the echoed page.limit, so a handler that clamped only
// the response would fail.
func TestAuditListHandler_LimitDefaultsClampsAndRejects(t *testing.T) {
	if got := hndAccepts(t, "").Limit; got != defaultLimitForTest {
		t.Errorf("no limit produced %d, want the default %d", got, defaultLimitForTest)
	}
	if got := hndAccepts(t, "limit=500").Limit; got != maxLimitForTest {
		t.Errorf("limit=500 produced %d, want it clamped to %d", got, maxLimitForTest)
	}
	if got := hndAccepts(t, "limit=100").Limit; got != maxLimitForTest {
		t.Errorf("limit=100 produced %d, want %d — the cap itself is legal", got, maxLimitForTest)
	}
	if got := hndAccepts(t, "limit=7").Limit; got != 7 {
		t.Errorf("limit=7 produced %d, want 7 passed through", got)
	}
	// The two refusals live in the AC #3 table; asserted here too so this case reads as
	// the whole rule.
	hndRejects(t, "limit=0")
	hndRejects(t, "limit=abc")
}

const (
	defaultLimitForTest = 25
	maxLimitForTest     = 100
)

// --- AC #5: company is three-valued ------------------------------------------------------------

// TestAuditListHandler_CompanyParamIsThreeValued is AC #5. Absent, a uuid and the literal
// `workspace` are three different reads; anything else is a 400 rather than a silent
// fallback to all-companies, which would show a firm-wide log to someone who asked for one
// company.
func TestAuditListHandler_CompanyParamIsThreeValued(t *testing.T) {
	entity := uuid.NewString()

	if got := hndAccepts(t, "").Company; got.Mode() != audit.ModeAllCompanies {
		t.Errorf("no company param produced mode %v, want ModeAllCompanies", got.Mode())
	}
	got := hndAccepts(t, "company="+entity).Company
	if got.Mode() != audit.ModeNamedCompany {
		t.Errorf("company=<uuid> produced mode %v, want ModeNamedCompany", got.Mode())
	}
	if got.ID() != entity {
		t.Errorf("company=<uuid> produced id %q, want %q", got.ID(), entity)
	}
	if got := hndAccepts(t, "company=workspace").Company; got.Mode() != audit.ModeWorkspaceOnly {
		t.Errorf("company=workspace produced mode %v, want ModeWorkspaceOnly", got.Mode())
	}
	for _, bogus := range []string{"not-a-uuid-or-workspace", "Workspace", "WORKSPACE", "all"} {
		hndRejects(t, "company="+bogus)
	}
}

// --- the rest of §2 reaching the store ----------------------------------------------------------

// TestAuditListHandler_EveryParameterReachesTheStore is the claim no per-rule case above
// makes: a parameter can be parsed, validated and then dropped on the floor, and every
// assertion still passes. This pins each one to the Filter field it feeds.
func TestAuditListHandler_EveryParameterReachesTheStore(t *testing.T) {
	entity, actor, invoice := uuid.NewString(), uuid.NewString(), uuid.NewString()
	from := "2026-01-02T03:04:05Z"
	to := "2026-02-03T04:05:06Z"

	got := hndAccepts(t, strings.Join([]string{
		"limit=10",
		"from=" + from,
		"to=" + to,
		"event=invoice.created",
		"event=invoice.updated",
		"actor=" + actor,
		"company=" + entity,
		"q=acme",
		"invoice_id=" + invoice,
	}, "&"))

	if got.Limit != 10 {
		t.Errorf("Limit = %d, want 10", got.Limit)
	}
	if want, _ := time.Parse(time.RFC3339, from); !got.From.Equal(want) {
		t.Errorf("From = %v, want %v", got.From, want)
	}
	if want, _ := time.Parse(time.RFC3339, to); !got.To.Equal(want) {
		t.Errorf("To = %v, want %v", got.To, want)
	}
	if len(got.Events) != 2 || got.Events[0] != "invoice.created" || got.Events[1] != "invoice.updated" {
		t.Errorf("Events = %v, want both repeated values in order", got.Events)
	}
	if len(got.Actors) != 1 || got.Actors[0] != actor {
		t.Errorf("Actors = %v, want [%s]", got.Actors, actor)
	}
	if got.Company.ID() != entity {
		t.Errorf("Company.ID() = %q, want %q", got.Company.ID(), entity)
	}
	if got.Q != "acme" {
		t.Errorf("Q = %q, want \"acme\"", got.Q)
	}
	if got.InvoiceID != invoice {
		t.Errorf("InvoiceID = %q, want %q", got.InvoiceID, invoice)
	}
}

// TestAuditListHandler_ActorKindReachesTheStoreAndActorIsRefusedAlongsideIt is AC #6 plus
// the half it hides: the two are mutually exclusive because they answer the same question
// two ways, so each must work alone.
func TestAuditListHandler_ActorKindReachesTheStoreAndActorIsRefusedAlongsideIt(t *testing.T) {
	for _, kind := range []string{"people", "system"} {
		if got := hndAccepts(t, "actor_kind="+kind).ActorKind; got != kind {
			t.Errorf("actor_kind=%s produced ActorKind %q, want %q", kind, got, kind)
		}
	}
	actor := uuid.NewString()
	if got := hndAccepts(t, "actor="+actor).Actors; len(got) != 1 || got[0] != actor {
		t.Errorf("actor alone produced Actors %v, want [%s]", got, actor)
	}
	hndRejects(t, "actor="+actor+"&actor_kind=people")
	hndRejects(t, "actor="+actor+"&actor_kind=system")
}

// TestAuditListHandler_CursorRoundTripsThroughTheHandler pins the cursor as an opaque
// value: the handler decodes it only far enough to refuse a malformed one, and the
// position it carries must reach the store intact.
func TestAuditListHandler_CursorRoundTripsThroughTheHandler(t *testing.T) {
	when := time.Now().UTC().Truncate(time.Microsecond)
	raw := audit.EncodeCursor(when, 4242)

	got := hndAccepts(t, "cursor="+raw)
	if got.Cursor == nil {
		t.Fatalf("cursor=%s produced a nil Filter.Cursor", raw)
	}
	if got.Cursor.ID != 4242 {
		t.Errorf("cursor id = %d, want 4242", got.Cursor.ID)
	}
	if !got.Cursor.CreatedAt.Equal(when) {
		t.Errorf("cursor created_at = %v, want %v", got.Cursor.CreatedAt, when)
	}
}

// --- store failures ---------------------------------------------------------------------------

// TestAuditListHandler_StoreErrorsMapThroughStatusForErr keeps the fail-closed rule: a
// request whose tenant could not be resolved is a 401, and anything else is a 500 with a
// generic body — never an internal on the wire.
func TestAuditListHandler_StoreErrorsMapThroughStatusForErr(t *testing.T) {
	secret := "pq: relation \"audit_log\" does not exist"
	for _, tc := range []struct {
		name string
		err  error
		want int
	}{
		{"no tenant", db.ErrNoTenant, http.StatusUnauthorized},
		{"anything else", errors.New(secret), http.StatusInternalServerError},
	} {
		t.Run(tc.name, func(t *testing.T) {
			spy := &hndSpy{err: tc.err}
			w := hndGet(t, spy, "")
			if w.Code != tc.want {
				t.Errorf("status = %d (%s), want %d", w.Code, w.Body.String(), tc.want)
			}
			if strings.Contains(w.Body.String(), secret) {
				t.Errorf("the response body leaks the store error: %s", w.Body.String())
			}
		})
	}
}

// --- AC #9: CORS ------------------------------------------------------------------------------

// TestAuditRoute_GETIsAlreadyInCorsAllowMethods is AC #9, a verify-not-edit. A NEW http
// method needs a corsAllowMethods edit that no Go or e2e test can see; GET needs none, and
// this case is what makes that claim checkable rather than remembered.
func TestAuditRoute_GETIsAlreadyInCorsAllowMethods(t *testing.T) {
	methods := hndCorsAllowMethods(t)
	if strings.TrimSpace(methods) == "" {
		t.Fatalf("corsAllowMethods read as empty; the extraction is broken and the check below " +
			"would pass vacuously")
	}
	if !strings.Contains(methods, "GET") {
		t.Errorf("corsAllowMethods = %q, want it to already contain GET", methods)
	}
}

// hndCorsAllowMethodsRE extracts the constant's quoted value from the gateway source.
// internal/gateway keeps it unexported, and importing that package here would add a
// dependency the reader does not otherwise have, so the file is read instead.
var hndCorsAllowMethodsRE = regexp.MustCompile(`corsAllowMethods\s*=\s*"([^"]*)"`)

func hndCorsAllowMethods(t *testing.T) string {
	t.Helper()
	const path = "../gateway/cors.go"
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	m := hndCorsAllowMethodsRE.FindSubmatch(raw)
	if m == nil {
		t.Fatalf("no corsAllowMethods constant in %s; the extraction lost its anchor", path)
	}
	return string(m[1])
}

package invoice

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/SimonOsipov/invoice-os/internal/platform/ai"
	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
	"github.com/SimonOsipov/invoice-os/internal/platform/db"
)

const explainTestMessage = "Line cost must not be negative."

// explainRecorder is a recording ExplainAI.
type explainRecorder struct {
	mu      sync.Mutex
	enabled bool
	answer  map[string]any
	err     error
	calls   []ai.Request
	events  *[]string
}

func (f *explainRecorder) Enabled() bool { return f.enabled }

func (f *explainRecorder) Call(_ context.Context, req ai.Request) (map[string]any, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, req)
	if f.events != nil {
		*f.events = append(*f.events, "call")
	}
	return f.answer, f.err
}

func (f *explainRecorder) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

func newExplainRecorder() *explainRecorder {
	return &explainRecorder{enabled: true, answer: map[string]any{
		"explanation": "The unit price on line 2 is negative.", "fix_field": nil, "fix_value": nil,
	}}
}

// explainInvoice is a stored invoice with the given verdict and one description per line.
func explainInvoice(t *testing.T, vs []Violation, descriptions ...string) Invoice {
	t.Helper()
	raw, err := json.Marshal(vs)
	if err != nil {
		t.Fatal(err)
	}
	inv := Invoice{ID: "inv-1", InvoiceNumber: "EXP-1", Violations: raw}
	for i, d := range descriptions {
		inv.LineItems = append(inv.LineItems, LineItem{ID: fmt.Sprintf("l%d", i+1), LineNo: i + 1, Description: strPtr(d), UnitPrice: strPtr("-5.00")})
	}
	return inv
}

func explainGet(inv Invoice) func(context.Context, string) (Invoice, error) {
	return func(context.Context, string) (Invoice, error) { return inv, nil }
}

func lineViolation(path string) Violation {
	return Violation{RuleKey: "line-cost-non-negative", Severity: "error", Message: explainTestMessage, Path: path}
}

func explainFakeClient(t *testing.T, logger *slog.Logger) *ai.Client {
	t.Helper()
	t.Setenv(ai.EnvFake, "true")
	t.Setenv(ai.EnvKey, "")
	c, err := ai.FromEnv(logger)
	if err != nil {
		t.Fatalf("ai.FromEnv: %v", err)
	}
	return c
}

func explainMarker(scope string, answer string) string {
	return "AIFAKE-" + scope + "ANSWER-" + base64.RawURLEncoding.EncodeToString([]byte(answer))
}

func TestExplain_PathMustMatchToo(t *testing.T) {
	inv := explainInvoice(t, []Violation{lineViolation("line_items[2]")}, "a", "b", "c")
	for name, key := range map[string]ViolationKey{
		"other path":     {RuleKey: "line-cost-non-negative", Path: "line_items[3]"},
		"empty path":     {RuleKey: "line-cost-non-negative"},
		"other rule key": {RuleKey: "currency-allowed", Path: "line_items[2]"},
	} {
		t.Run(name, func(t *testing.T) {
			rec := newExplainRecorder()
			_, err := NewExplainer(explainGet(inv), rec).Explain(context.Background(), "inv-1", key)
			if !errors.Is(err, ErrViolationGone) {
				t.Errorf("err = %v, want ErrViolationGone", err)
			}
			if n := rec.callCount(); n != 0 {
				t.Errorf("AI calls = %d, want 0", n)
			}
		})
	}

	rec := newExplainRecorder()
	got, err := NewExplainer(explainGet(inv), rec).Explain(context.Background(), "inv-1", ViolationKey{RuleKey: "line-cost-non-negative", Path: "line_items[2]"})
	if err != nil || got.Status != "ok" {
		t.Fatalf("exact key: got (%+v, %v), want status ok", got, err)
	}
	if n := rec.callCount(); n != 1 {
		t.Errorf("exact key: AI calls = %d, want 1", n)
	}
}

func TestExplain_DuplicateKeyUsesFirst(t *testing.T) {
	first := Violation{RuleKey: "r", Severity: "error", Message: "first", Path: "vat"}
	second := Violation{RuleKey: "r", Severity: "error", Message: "second", Path: "vat"}
	rec := newExplainRecorder()
	_, err := NewExplainer(explainGet(explainInvoice(t, []Violation{first, second}, "a")), rec).
		Explain(context.Background(), "inv-1", ViolationKey{RuleKey: "r", Path: "vat"})
	if err != nil {
		t.Fatalf("Explain: %v", err)
	}
	if rec.callCount() != 1 {
		t.Fatalf("AI calls = %d, want 1", rec.callCount())
	}
	text := rec.calls[0].Text
	if !strings.Contains(text, `"message":"first"`) || strings.Contains(text, "second") {
		t.Errorf("text must carry the first violation only:\n%s", text)
	}
}

func TestExplain_OffClientAnswersUnavailableWithNoCall(t *testing.T) {
	inv := explainInvoice(t, []Violation{lineViolation("line_items[2]")}, "a", "b")
	key := ViolationKey{RuleKey: "line-cost-non-negative", Path: "line_items[2]"}
	off := newExplainRecorder()
	off.enabled = false
	for name, client := range map[string]ExplainAI{"disabled client": off, "nil client": nil} {
		t.Run(name, func(t *testing.T) {
			got, err := NewExplainer(explainGet(inv), client).Explain(context.Background(), "inv-1", key)
			if err != nil || got.Status != "unavailable" {
				t.Errorf("got (%+v, %v), want unavailable and nil error", got, err)
			}
		})
	}
	if n := off.callCount(); n != 0 {
		t.Errorf("disabled client calls = %d, want 0", n)
	}

	// main passes ai.FromEnv's *Client: non-nil and off with no key, so Enabled is what gates the call.
	t.Setenv(ai.EnvFake, "")
	t.Setenv(ai.EnvKey, "")
	var buf bytes.Buffer
	real, err := ai.FromEnv(slog.New(slog.NewJSONHandler(&buf, nil)))
	if err != nil || real == nil || real.Enabled() {
		t.Fatalf("ai.FromEnv with no key: client %v, err %v; want a non-nil off client", real, err)
	}
	got, err := NewExplainer(explainGet(inv), real).Explain(context.Background(), "inv-1", key)
	if err != nil || got.Status != "unavailable" || got.Explanation != nil || got.Fix != nil {
		t.Errorf("real off client: got (%+v, %v), want unavailable with null explanation and fix", got, err)
	}
	if strings.Contains(buf.String(), "ai call") {
		t.Errorf("real off client logged an ai call:\n%s", buf.String())
	}

	on := newExplainRecorder()
	if got, _ := NewExplainer(explainGet(inv), on).Explain(context.Background(), "inv-1", key); got.Status != "ok" || on.callCount() != 1 {
		t.Errorf("enabled client: status %q, calls %d, want ok and 1", got.Status, on.callCount())
	}
}

func TestExplain_FailedCallAnswersUnavailable(t *testing.T) {
	inv := explainInvoice(t, []Violation{lineViolation("line_items[2]")}, "a", "b")
	key := ViolationKey{RuleKey: "line-cost-non-negative", Path: "line_items[2]"}
	for name, callErr := range map[string]error{
		"unavailable": fmt.Errorf("%w (last: 503)", ai.ErrUnavailable),
		"refusal":     errors.New("ai: refused: schema"),
		"canceled":    fmt.Errorf("ai: %w", context.Canceled),
	} {
		t.Run(name, func(t *testing.T) {
			rec := newExplainRecorder()
			rec.answer, rec.err = nil, callErr
			got, err := NewExplainer(explainGet(inv), rec).Explain(context.Background(), "inv-1", key)
			if err != nil || got.Status != "unavailable" {
				t.Errorf("got (%+v, %v), want unavailable and nil error", got, err)
			}
			if rec.callCount() != 1 {
				t.Errorf("AI calls = %d, want 1", rec.callCount())
			}
		})
	}
}

func TestExplain_FakeBlankAnswerIsUnavailable(t *testing.T) {
	inv := explainInvoice(t, []Violation{lineViolation("line_items[2]")}, "plain", "plain")
	got, err := NewExplainer(explainGet(inv), explainFakeClient(t, nil)).
		Explain(context.Background(), "inv-1", ViolationKey{RuleKey: "line-cost-non-negative", Path: "line_items[2]"})
	if err != nil || got.Status != "unavailable" || got.Explanation != nil || got.Fix != nil {
		t.Errorf("got (%+v, %v), want unavailable with null explanation and fix", got, err)
	}
}

func TestExplain_GetReturnsBeforeTheCall(t *testing.T) {
	var events []string
	rec := newExplainRecorder()
	rec.events = &events
	inv := explainInvoice(t, []Violation{lineViolation("line_items[2]")}, "a", "b")
	get := func(context.Context, string) (Invoice, error) {
		events = append(events, "get-start")
		time.Sleep(20 * time.Millisecond)
		events = append(events, "get-end")
		return inv, nil
	}
	if _, err := NewExplainer(get, rec).Explain(context.Background(), "inv-1", ViolationKey{RuleKey: "line-cost-non-negative", Path: "line_items[2]"}); err != nil {
		t.Fatalf("Explain: %v", err)
	}
	if want := []string{"get-start", "get-end", "call"}; !slices.Equal(events, want) {
		t.Errorf("events = %v, want %v", events, want)
	}
}

func TestExplain_ScopedMarkerSteersUnscopedDoesNot(t *testing.T) {
	answer := `{"explanation":"Line 2 has a negative unit price.","fix_field":null,"fix_value":null}`
	key := ViolationKey{RuleKey: "line-cost-non-negative", Path: "line_items[2]"}
	for _, tc := range []struct {
		name, description, wantStatus string
	}{
		{"unscoped answer marker", explainMarker("", answer), "unavailable"},
		{"scoped answer marker", explainMarker("EXPLAIN-", answer), "ok"},
		{"scoped unavailable marker", "AIFAKE-EXPLAIN-UNAVAILABLE", "unavailable"},
		{"no marker", "plain", "unavailable"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			inv := explainInvoice(t, []Violation{lineViolation(key.Path)}, "a", tc.description)
			got, err := NewExplainer(explainGet(inv), explainFakeClient(t, nil)).Explain(context.Background(), "inv-1", key)
			if err != nil || got.Status != tc.wantStatus {
				t.Fatalf("got (%+v, %v), want status %q", got, err, tc.wantStatus)
			}
			if tc.wantStatus == "ok" && (got.Explanation == nil || *got.Explanation != "Line 2 has a negative unit price.") {
				t.Errorf("explanation = %v, want the marker's", got.Explanation)
			}
		})
	}
}

// doExplain drives POST /v1/invoices/{id}/explain.
func doExplain(t *testing.T, explain func(context.Context, string, ViolationKey) (ExplainResult, error), log *slog.Logger, id *auth.Identity, invoiceID, rawBody string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(http.MethodPost, "/v1/invoices/"+invoiceID+"/explain", strings.NewReader(rawBody))
	r.SetPathValue("id", invoiceID)
	if id != nil {
		r = r.WithContext(auth.WithIdentity(r.Context(), *id))
	}
	rec := httptest.NewRecorder()
	ExplainHandler(explain, log).ServeHTTP(rec, r)
	return rec
}

func explainErrorOf(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	var body struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode %q: %v", rec.Body.String(), err)
	}
	return body.Error
}

func TestExplainHandler_RequestValidation(t *testing.T) {
	caller := &auth.Identity{Subject: memberSubject, Role: "authenticated", TenantID: "11111111-1111-1111-1111-111111111111"}
	var gotID string
	var gotKey ViolationKey
	calls := 0
	ok := func(_ context.Context, id string, key ViolationKey) (ExplainResult, error) {
		calls++
		gotID, gotKey = id, key
		return ExplainResult{Status: "unavailable"}, nil
	}
	body := func(key, path string) string {
		b, _ := json.Marshal(map[string]string{"rule_key": key, "path": path})
		return string(b)
	}
	for _, tc := range []struct {
		name     string
		identity *auth.Identity
		body     string
		explain  func(context.Context, string, ViolationKey) (ExplainResult, error)
		want     int
		wantErr  string
	}{
		{"no identity", nil, body("r", "p"), ok, 401, "unauthorized"},
		{"bad json", caller, "{", ok, 400, "invalid request body"},
		{"missing rule_key", caller, "{}", ok, 400, "rule_key is required"},
		{"key over 200 bytes", caller, body(strings.Repeat("k", 201), "p"), ok, 400, ""},
		{"path over 200 bytes", caller, body("r", strings.Repeat("p", 201)), ok, 400, ""},
		{"body over 4 KiB", caller, body("r", strings.Repeat("p", 5<<10)), ok, 413, ""},
		{"service validation error", caller, body("r", "p"), func(context.Context, string, ViolationKey) (ExplainResult, error) {
			calls++
			return ExplainResult{}, fmt.Errorf("%w: malformed id", ErrValidation)
		}, 400, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls = 0
			rec := doExplain(t, tc.explain, nil, tc.identity, "inv-1", tc.body)
			if rec.Code != tc.want {
				t.Fatalf("status = %d, want %d (body=%s)", rec.Code, tc.want, rec.Body.String())
			}
			if tc.wantErr != "" && explainErrorOf(t, rec) != tc.wantErr {
				t.Errorf("error = %q, want %q", explainErrorOf(t, rec), tc.wantErr)
			}
			wantCalls := 0
			if tc.name == "service validation error" {
				wantCalls = 1
			}
			if tc.explain != nil && calls != wantCalls {
				t.Errorf("explain calls = %d, want %d", calls, wantCalls)
			}
		})
	}

	for _, tc := range []struct{ name, key, path string }{
		{"key and path at 200 bytes", strings.Repeat("k", 200), strings.Repeat("p", 200)},
		{"empty path", "r", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls = 0
			rec := doExplain(t, ok, nil, caller, "inv-9", body(tc.key, tc.path))
			if rec.Code != http.StatusOK || calls != 1 {
				t.Fatalf("status = %d, calls = %d, want 200 and 1 (body=%s)", rec.Code, calls, rec.Body.String())
			}
			if gotID != "inv-9" || gotKey != (ViolationKey{RuleKey: tc.key, Path: tc.path}) {
				t.Errorf("explain got (%q, %+v), want the path id and the body key", gotID, gotKey)
			}
		})
	}
}

func TestExplainHandler_NoTenantIs401(t *testing.T) {
	caller := &auth.Identity{Subject: memberSubject, Role: "authenticated"}
	rec := doExplain(t, func(context.Context, string, ViolationKey) (ExplainResult, error) {
		return ExplainResult{}, db.ErrNoTenant
	}, nil, caller, "inv-1", `{"rule_key":"r"}`)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401 (body=%s)", rec.Code, rec.Body.String())
	}
}

func TestExplainHandler_ErrorMapping(t *testing.T) {
	caller := &auth.Identity{Subject: memberSubject, Role: "authenticated", TenantID: "11111111-1111-1111-1111-111111111111"}
	for _, tc := range []struct {
		name    string
		err     error
		want    int
		wantMsg string
		logged  bool
	}{
		{"not found", ErrNotFound, 404, "not found", false},
		{"not an active member", fmt.Errorf("invoice: get: %w", db.ErrNotActiveMember), 403, db.NotActiveMemberMessage, false},
		{"violation gone", ErrViolationGone, 409, "the violation is not on this invoice's last validation", false},
		{"unexpected", errors.New("boom"), 500, "internal server error", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var buf bytes.Buffer
			rec := doExplain(t, func(context.Context, string, ViolationKey) (ExplainResult, error) {
				return ExplainResult{}, tc.err
			}, slog.New(slog.NewJSONHandler(&buf, nil)), caller, "inv-1", `{"rule_key":"r"}`)
			if rec.Code != tc.want || explainErrorOf(t, rec) != tc.wantMsg {
				t.Errorf("got %d %q, want %d %q", rec.Code, explainErrorOf(t, rec), tc.want, tc.wantMsg)
			}
			if logged := buf.Len() > 0; logged != tc.logged {
				t.Errorf("logged = %v, want %v (%s)", logged, tc.logged, buf.String())
			}
		})
	}
}

func TestExplainHandler_UnavailableIs200(t *testing.T) {
	caller := &auth.Identity{Subject: memberSubject, Role: "authenticated", TenantID: "11111111-1111-1111-1111-111111111111"}
	rec := doExplain(t, func(context.Context, string, ViolationKey) (ExplainResult, error) {
		return explainUnavailable(), nil
	}, nil, caller, "inv-1", `{"rule_key":"r"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if got, want := strings.TrimSpace(rec.Body.String()), `{"status":"unavailable","explanation":null,"fix":null}`; got != want {
		t.Errorf("body = %s, want %s", got, want)
	}
}

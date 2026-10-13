// GET /v1/rules handler against a fake read: wire keys and the refusal map. No database.
package validation

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
	"github.com/SimonOsipov/invoice-os/internal/platform/db"
)

const goldenFixtureJSON = `{
  "today": "2026-10-12",
  "versions": [
    {"rule_set_version_id": "5a5a5a5a-0000-4000-8000-000000000005", "version": 5, "state": "scheduled", "effective_from": "2027-01-01", "rule_count": 67},
    {"rule_set_version_id": "4a4a4a4a-0000-4000-8000-000000000004", "version": 4, "state": "in_force", "effective_from": "2026-08-06", "rule_count": 2}
  ],
  "rule_set_version_id": "4a4a4a4a-0000-4000-8000-000000000004",
  "version": 4,
  "effective_from": "2026-08-06",
  "rules": [
    {"key": "buyer-tin-format", "type": "format/regex", "target": "buyer.tin", "params": {"pattern": "^[0-9]{8}-[0-9]{4}$"}, "severity": "error", "when": null, "message": "Buyer TIN is malformed"},
    {"key": "vat-standard-rate", "type": "math/rate", "target": "invoice.vat", "params": {"rate": 0.075}, "severity": "error", "when": "has(invoice.vat)", "message": "VAT is not 7.5%"}
  ]
}`

func goldenFixture(t *testing.T) GoldenRules {
	t.Helper()
	var g GoldenRules
	if err := json.Unmarshal([]byte(goldenFixtureJSON), &g); err != nil {
		t.Fatalf("fixture: %v", err)
	}
	return g
}

func goldenMemberCtx() context.Context {
	return auth.WithIdentity(context.Background(), auth.Identity{
		Subject: uuid.NewString(), Role: "authenticated", TenantID: uuid.NewString(),
	})
}

func doGoldenRules(h http.HandlerFunc, ctx context.Context) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/rules", nil).WithContext(ctx))
	return rec
}

func objectKeys(t *testing.T, raw json.RawMessage) []string {
	t.Helper()
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("decode object %s: %v", raw, err)
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func assertGoldenKeys(t *testing.T, what string, raw json.RawMessage, want ...string) {
	t.Helper()
	sort.Strings(want)
	if got := objectKeys(t, raw); fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("%s keys = %v, want exactly %v", what, got, want)
	}
}

func TestGoldenRulesHandler_WireKeys(t *testing.T) {
	want := goldenFixture(t)
	rec := doGoldenRules(GoldenRulesHandler(func(context.Context) (GoldenRules, error) { return want, nil }, nil), goldenMemberCtx())
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%q)", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}

	var top map[string]json.RawMessage
	if err := json.Unmarshal(rec.Body.Bytes(), &top); err != nil {
		t.Fatalf("decode %q: %v", rec.Body.String(), err)
	}
	assertGoldenKeys(t, "top-level", rec.Body.Bytes(), "today", "versions", "rule_set_version_id", "version", "effective_from", "rules")

	var versions, rules []json.RawMessage
	if err := json.Unmarshal(top["versions"], &versions); err != nil || len(versions) != 2 {
		t.Fatalf("versions = %s (%v), want 2 entries", top["versions"], err)
	}
	for i, v := range versions {
		assertGoldenKeys(t, fmt.Sprintf("versions[%d]", i), v, "rule_set_version_id", "version", "state", "effective_from", "rule_count")
	}
	if err := json.Unmarshal(top["rules"], &rules); err != nil || len(rules) != 2 {
		t.Fatalf("rules = %s (%v), want 2 entries", top["rules"], err)
	}
	for i, r := range rules {
		assertGoldenKeys(t, fmt.Sprintf("rules[%d]", i), r, "key", "type", "target", "params", "severity", "when", "message")
	}

	var r0, r1 map[string]json.RawMessage
	_ = json.Unmarshal(rules[0], &r0)
	_ = json.Unmarshal(rules[1], &r1)
	if string(r0["when"]) != "null" {
		t.Errorf("rules[0].when = %s, want null present", r0["when"])
	}
	if string(r1["when"]) != `"has(invoice.vat)"` {
		t.Errorf("rules[1].when = %s, want the string", r1["when"])
	}
	if !strings.Contains(string(r0["params"]), `"pattern"`) || string(r0["params"])[0] != '{' {
		t.Errorf("rules[0].params = %s, want the stored object", r0["params"])
	}
}

func TestGoldenRulesHandler_Refusals(t *testing.T) {
	sentinel := errors.New("pg: connection reset, secret-detail")
	for _, tc := range []struct {
		name     string
		ctx      context.Context
		err      error
		status   int
		msg      string
		wantLogs bool
	}{
		{"no identity", context.Background(), nil, http.StatusUnauthorized, "unauthorized", false},
		{"read says no tenant", goldenMemberCtx(), db.ErrNoTenant, http.StatusUnauthorized, "unauthorized", false},
		{"read says not an active member", goldenMemberCtx(), db.ErrNotActiveMember, http.StatusForbidden, db.NotActiveMemberMessage, false},
		{"wrapped not an active member", goldenMemberCtx(), fmt.Errorf("validation: golden rules: %w", db.ErrNotActiveMember), http.StatusForbidden, db.NotActiveMemberMessage, false},
		{"no version in force", goldenMemberCtx(), ErrNoActiveRuleSet, http.StatusServiceUnavailable, "no active rule-set", false},
		{"other error", goldenMemberCtx(), sentinel, http.StatusInternalServerError, "internal server error", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var logs bytes.Buffer
			log := slog.New(slog.NewTextHandler(&logs, nil))
			read := func(context.Context) (GoldenRules, error) {
				if tc.err != nil {
					return GoldenRules{}, tc.err
				}
				return goldenFixture(t), nil
			}
			rec := doGoldenRules(GoldenRulesHandler(read, log), tc.ctx)
			if rec.Code != tc.status {
				t.Fatalf("status = %d, want %d (body=%q)", rec.Code, tc.status, rec.Body.String())
			}
			var body map[string]string
			if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
				t.Fatalf("decode %q: %v", rec.Body.String(), err)
			}
			if len(body) != 1 || body["error"] != tc.msg {
				t.Errorf("body = %v, want exactly {error: %q}", body, tc.msg)
			}
			if tc.wantLogs {
				if !strings.Contains(logs.String(), "secret-detail") {
					t.Errorf("the 500 was not logged with its cause: %q", logs.String())
				}
				if strings.Contains(rec.Body.String(), "secret-detail") {
					t.Errorf("the 500 body leaks the cause: %q", rec.Body.String())
				}
			}
		})
	}
}

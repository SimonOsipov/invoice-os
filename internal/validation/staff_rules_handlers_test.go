// StaffListRulesHandler and StaffSwitchRuleHandler with fake funcs: wire shapes, body checks, error mapping.
package validation

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/SimonOsipov/invoice-os/internal/platform/db"
)

type switchCall struct {
	n       int
	key     string
	enabled bool
	reason  string
}

// patchRule serves a PATCH through a handler bound to a fake that returns (res, err).
func patchRule(t *testing.T, body string, res SwitchResult, err error) (*httptest.ResponseRecorder, *switchCall) {
	t.Helper()
	call := &switchCall{}
	h := StaffSwitchRuleHandler(func(_ context.Context, key string, enabled bool, reason string) (SwitchResult, error) {
		*call = switchCall{n: call.n + 1, key: key, enabled: enabled, reason: reason}
		return res, err
	}, nil)
	mux := http.NewServeMux()
	mux.Handle("PATCH /v1/staff/rules/{key}", h)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPatch, "/v1/staff/rules/vat-standard-rate", strings.NewReader(body)))
	return rec, call
}

func reasonBody(reason string) string {
	b, _ := json.Marshal(map[string]any{"enabled": false, "reason": reason})
	return string(b)
}

func TestStaffRulesHandlers_ListMapsNoRuleSetTo503(t *testing.T) {
	h := StaffListRulesHandler(func(context.Context) (InForceRules, error) { return InForceRules{}, ErrNoActiveRuleSet }, nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/staff/rules", nil))
	if rec.Code != 503 || strings.TrimSpace(rec.Body.String()) != `{"error":"no rule set in force"}` {
		t.Fatalf("got %d %q, want 503 no rule set in force", rec.Code, rec.Body.String())
	}
}

func TestStaffRulesHandlers_ListWireShape(t *testing.T) {
	id := uuid.New()
	h := StaffListRulesHandler(func(context.Context) (InForceRules, error) {
		return InForceRules{RuleSetVersionID: id, Version: 4, Rules: []StaffRule{{Key: "k", Type: "t", Target: "x", Severity: "error", Scope: "document", Message: "m", Enabled: true}}}, nil
	}, nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/staff/rules", nil))
	want := fmt.Sprintf(`{"rule_set_version_id":%q,"version":4,"rules":[{"key":"k","type":"t","target":"x","severity":"error","scope":"document","message":"m","enabled":true}]}`, id)
	if rec.Code != 200 || strings.TrimSpace(rec.Body.String()) != want {
		t.Fatalf("got %d %q, want 200 %q", rec.Code, rec.Body.String(), want)
	}
}

func TestStaffRulesHandlers_SwitchWireShape(t *testing.T) {
	id := uuid.New()
	rec, call := patchRule(t, `{"enabled":false,"reason":"x"}`, SwitchResult{Key: "vat-standard-rate", Enabled: false, RuleSetVersion: 4, RuleSetVersionID: id}, nil)
	if rec.Code != 200 {
		t.Fatalf("status = %d, want 200 (%s)", rec.Code, rec.Body.String())
	}
	var got map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got) != 4 || got["key"] != "vat-standard-rate" || got["enabled"] != false || got["rule_set_version"] != float64(4) || got["rule_set_version_id"] != id.String() {
		t.Errorf("body = %v, want exactly key, enabled, rule_set_version, rule_set_version_id", got)
	}
	if call.key != "vat-standard-rate" || call.enabled || call.reason != "x" {
		t.Errorf("switch call = %+v", call)
	}
	_, call = patchRule(t, `{"enabled":true,"reason":"y"}`, SwitchResult{Enabled: true}, nil)
	if !call.enabled {
		t.Errorf("enabled:true reached the switch as %+v", call)
	}
}

func TestStaffRulesHandlers_RejectsBadBodies(t *testing.T) {
	for name, body := range map[string]string{
		"empty body":     ``,
		"empty object":   `{}`,
		"enabled absent": `{"reason":"r"}`,
		"null body":      `null`,
		"reason absent":  `{"enabled":false}`,
		"reason empty":   reasonBody(""),
		"reason blank":   reasonBody("   "),
		"501 runes":      reasonBody(strings.Repeat("a", 501)),
		"extra field":    `{"enabled":false,"reason":"r","key":"x"}`,
		"truncated":      `{"enabled":fal`,
		"two documents":  `{"enabled":false,"reason":"r"}{}`,
		"enabled string": `{"enabled":"no","reason":"r"}`,
	} {
		t.Run(name, func(t *testing.T) {
			rec, call := patchRule(t, body, SwitchResult{}, nil)
			if rec.Code != 400 {
				t.Errorf("status = %d, want 400 (%s)", rec.Code, rec.Body.String())
			}
			if call.n != 0 {
				t.Errorf("switch called %d times on a 400", call.n)
			}
		})
	}
	rec, call := patchRule(t, reasonBody(strings.Repeat("a", 500)), SwitchResult{}, nil)
	if rec.Code != 200 || call.n != 1 {
		t.Errorf("500 runes: status = %d, calls = %d, want 200 and 1", rec.Code, call.n)
	}
}

func TestStaffRulesHandlers_ReasonIsTrimmedAndCountedInRunes(t *testing.T) {
	for _, tc := range []struct {
		name   string
		reason string
		want   int
		passed string
	}{
		{"trimmed", "  r  ", 200, "r"},
		{"500 euro signs (3 bytes)", strings.Repeat("€", 500), 200, strings.Repeat("€", 500)},
		{"500 emoji (4 bytes)", strings.Repeat("😀", 500), 200, strings.Repeat("😀", 500)},
		{"501 emoji", strings.Repeat("😀", 501), 400, ""},
		{"500 spaces around r", strings.Repeat(" ", 250) + "r" + strings.Repeat(" ", 250), 200, "r"},
		{"501 chars after trim", "  " + strings.Repeat("a", 501) + "  ", 400, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec, call := patchRule(t, reasonBody(tc.reason), SwitchResult{}, nil)
			if rec.Code != tc.want {
				t.Fatalf("status = %d, want %d", rec.Code, tc.want)
			}
			if tc.want == 200 && call.reason != tc.passed {
				t.Errorf("reason reaching the switch = %q, want %q", call.reason, tc.passed)
			}
		})
	}
}

func TestStaffRulesHandlers_OversizeBodyIs413(t *testing.T) {
	body := `{"enabled":false,"reason":"` + strings.Repeat("a", 9<<10) + `"}`
	rec, call := patchRule(t, body, SwitchResult{}, nil)
	if rec.Code != 413 || call.n != 0 {
		t.Fatalf("status = %d, calls = %d, want 413 and 0", rec.Code, call.n)
	}
	// An oversize tail after a valid document is 413 too.
	rec, call = patchRule(t, `{"enabled":false,"reason":"r"}`+strings.Repeat(" ", 9<<10)+`{}`, SwitchResult{}, nil)
	if rec.Code != 413 || call.n != 0 {
		t.Fatalf("oversize tail: status = %d, calls = %d, want 413 and 0", rec.Code, call.n)
	}
}

func TestStaffRulesHandlers_ErrorMapping(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		body string
		want int
		msg  string
	}{
		{"not in force", ErrRuleNotInForce, `{"enabled":false,"reason":"r"}`, 404, `{"error":"no such rule in the version in force"}`},
		{"already disabled", ErrRuleAlreadyInState, `{"enabled":false,"reason":"r"}`, 409, `{"error":"rule is already disabled"}`},
		{"already enabled", fmt.Errorf("wrap: %w", ErrRuleAlreadyInState), `{"enabled":true,"reason":"r"}`, 409, `{"error":"rule is already enabled"}`},
		{"not staff", db.ErrNotStaff, `{"enabled":false,"reason":"r"}`, 403, `{"error":"forbidden"}`},
		{"other", errors.New("boom"), `{"enabled":false,"reason":"r"}`, 500, `{"error":"internal error"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec, _ := patchRule(t, tc.body, SwitchResult{}, tc.err)
			if rec.Code != tc.want || strings.TrimSpace(rec.Body.String()) != tc.msg {
				t.Errorf("got %d %q, want %d %q", rec.Code, rec.Body.String(), tc.want, tc.msg)
			}
		})
	}
	// No version in force is a 503 on the list and a 404 on the switch.
	list := httptest.NewRecorder()
	StaffListRulesHandler(func(context.Context) (InForceRules, error) { return InForceRules{}, ErrNoActiveRuleSet }, nil).
		ServeHTTP(list, httptest.NewRequest(http.MethodGet, "/v1/staff/rules", nil))
	if list.Code != 503 {
		t.Errorf("list with no version in force = %d, want 503", list.Code)
	}
}

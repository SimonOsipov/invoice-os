// The draft handlers against injected fakes, and validateDraftRule's table. No DB.
package validation

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/SimonOsipov/invoice-os/internal/platform/db"
)

const goodRuleJSON = `"type":"required","target":"invoice.currency","severity":"error","message":"m","enabled":true`

func draftReq(method, target, body string) (*httptest.ResponseRecorder, *http.Request) {
	return httptest.NewRecorder(), httptest.NewRequest(method, target, strings.NewReader(body))
}

func bodyKeys(t *testing.T, rec *httptest.ResponseRecorder) []string {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &m); err != nil {
		t.Fatalf("body %q: %v", rec.Body.String(), err)
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func errText(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	var m map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &m); err != nil {
		t.Fatalf("body %q: %v", rec.Body.String(), err)
	}
	return m["error"]
}

func TestStaffDraftsHandlers_OpenMapping(t *testing.T) {
	id := uuid.New()
	for _, c := range []struct {
		name   string
		err    error
		status int
		msg    string
	}{
		{"ok", nil, 201, ""},
		{"exists", ErrDraftExists, 409, "a draft already exists"},
		{"wrapped exists", fmt.Errorf("x: %w", ErrDraftExists), 409, "a draft already exists"},
		{"none in force", ErrNoActiveRuleSet, 503, "no rule set in force"},
		{"not staff", db.ErrNotStaff, 403, "forbidden"},
		{"other", errors.New("boom"), 500, "internal error"},
	} {
		t.Run(c.name, func(t *testing.T) {
			h := StaffOpenDraftHandler(func(context.Context) (DraftOpened, error) {
				return DraftOpened{RuleSetVersionID: id, Version: 5, FromVersion: 4}, c.err
			}, nil)
			rec, req := draftReq("POST", "/v1/staff/rule-versions/draft", "")
			h(rec, req)
			if rec.Code != c.status {
				t.Fatalf("status = %d (%s), want %d", rec.Code, rec.Body.String(), c.status)
			}
			if c.err == nil {
				if got, want := bodyKeys(t, rec), []string{"from_version", "rule_set_version_id", "version"}; !reflect.DeepEqual(got, want) {
					t.Errorf("body keys = %v, want %v", got, want)
				}
			} else if got := errText(t, rec); got != c.msg {
				t.Errorf("error = %q, want %q", got, c.msg)
			}
		})
	}
}

func TestStaffDraftsHandlers_AddEditMapping(t *testing.T) {
	var adds, edits int
	add := StaffAddDraftRuleHandler(func(_ context.Context, key string, _ validated) (DraftRuleResult, error) {
		adds++
		return DraftRuleResult{Key: key}, ErrRuleInDraft
	}, nil)
	edit := StaffEditDraftRuleHandler(func(_ context.Context, key string, _ validated) (DraftRuleEdited, error) {
		edits++
		return DraftRuleEdited{}, ErrRuleNotInDraft
	}, nil)

	rec, req := draftReq("POST", "/x", `{"key":"k",`+goodRuleJSON+`}`)
	add(rec, req)
	if rec.Code != 409 || errText(t, rec) != "rule already in the draft" {
		t.Errorf("add existing = %d %q, want 409 rule already in the draft", rec.Code, rec.Body.String())
	}
	rec, req = draftReq("PUT", "/x", `{`+goodRuleJSON+`}`)
	req.SetPathValue("key", "k")
	edit(rec, req)
	if rec.Code != 404 || errText(t, rec) != "no such rule in the draft" {
		t.Errorf("edit absent = %d %q, want 404 no such rule in the draft", rec.Code, rec.Body.String())
	}
	if adds != 1 || edits != 1 {
		t.Fatalf("fake calls add %d edit %d, want 1 and 1", adds, edits)
	}

	rec, req = draftReq("POST", "/x", `{`+goodRuleJSON+`}`)
	add(rec, req)
	if rec.Code != 400 {
		t.Errorf("POST without key = %d, want 400", rec.Code)
	}
	rec, req = draftReq("PUT", "/x", `{"key":"k",`+goodRuleJSON+`}`)
	req.SetPathValue("key", "k")
	edit(rec, req)
	if rec.Code != 400 {
		t.Errorf("PUT with a key field = %d, want 400", rec.Code)
	}
	if adds != 1 || edits != 1 {
		t.Errorf("a 400 reached the fake: add %d edit %d, want 1 and 1", adds, edits)
	}
}

func TestStaffDraftsHandlers_AddAndEditSuccessShapes(t *testing.T) {
	id := uuid.New()
	add := StaffAddDraftRuleHandler(func(_ context.Context, key string, _ validated) (DraftRuleResult, error) {
		return DraftRuleResult{Key: key, RuleSetVersion: 5, RuleSetVersionID: id}, nil
	}, nil)
	edit := StaffEditDraftRuleHandler(func(_ context.Context, key string, _ validated) (DraftRuleEdited, error) {
		return DraftRuleEdited{DraftRuleResult{Key: key, RuleSetVersion: 5, RuleSetVersionID: id}, true}, nil
	}, nil)
	remove := StaffRemoveDraftRuleHandler(func(_ context.Context, key string) (DraftRuleResult, error) {
		return DraftRuleResult{Key: key, RuleSetVersion: 5, RuleSetVersionID: id}, nil
	}, nil)
	base := []string{"key", "rule_set_version", "rule_set_version_id"}

	rec, req := draftReq("POST", "/x", `{"key":"k",`+goodRuleJSON+`}`)
	add(rec, req)
	if rec.Code != 201 || !reflect.DeepEqual(bodyKeys(t, rec), base) {
		t.Errorf("add = %d keys %v, want 201 %v", rec.Code, bodyKeys(t, rec), base)
	}
	rec, req = draftReq("PUT", "/x", `{`+goodRuleJSON+`}`)
	req.SetPathValue("key", "k")
	edit(rec, req)
	if want := append([]string{"changed"}, base...); rec.Code != 200 || !reflect.DeepEqual(bodyKeys(t, rec), want) {
		t.Errorf("edit = %d keys %v, want 200 %v", rec.Code, bodyKeys(t, rec), want)
	}
	rec, req = draftReq("DELETE", "/x", "")
	req.SetPathValue("key", "k")
	remove(rec, req)
	if rec.Code != 200 || !reflect.DeepEqual(bodyKeys(t, rec), base) {
		t.Errorf("remove = %d keys %v, want 200 %v", rec.Code, bodyKeys(t, rec), base)
	}
}

func TestStaffDraftsHandlers_NoDraftIs404(t *testing.T) {
	noDraft := fmt.Errorf("x: %w", ErrNoDraft)
	add := StaffAddDraftRuleHandler(func(context.Context, string, validated) (DraftRuleResult, error) { return DraftRuleResult{}, noDraft }, nil)
	edit := StaffEditDraftRuleHandler(func(context.Context, string, validated) (DraftRuleEdited, error) { return DraftRuleEdited{}, noDraft }, nil)
	remove := StaffRemoveDraftRuleHandler(func(context.Context, string) (DraftRuleResult, error) { return DraftRuleResult{}, noDraft }, nil)
	publish := StaffPublishDraftHandler(func(context.Context, time.Time) (DraftPublished, error) { return DraftPublished{}, noDraft }, nil)
	for name, call := range map[string]http.HandlerFunc{
		"add": add, "edit": edit, "remove": remove, "publish": publish,
	} {
		body := `{` + goodRuleJSON + `}`
		if name == "add" {
			body = `{"key":"k",` + goodRuleJSON + `}`
		}
		if name == "publish" {
			body = `{"effective_from":"3001-01-01"}`
		}
		rec, req := draftReq("POST", "/x", body)
		req.SetPathValue("key", "k")
		call(rec, req)
		if rec.Code != 404 || errText(t, rec) != "no draft" {
			t.Errorf("%s = %d %q, want 404 no draft", name, rec.Code, rec.Body.String())
		}
	}
}

func TestStaffDraftsHandlers_PutRejectsBadBodies(t *testing.T) {
	var calls int
	edit := StaffEditDraftRuleHandler(func(context.Context, string, validated) (DraftRuleEdited, error) {
		calls++
		return DraftRuleEdited{}, nil
	}, nil)
	pad := `{` + goodRuleJSON + `,"params":{"p":"` + strings.Repeat("a", 33<<10) + `"}}`
	for _, c := range []struct {
		name, body string
		status     int
	}{
		{"unknown field", `{` + goodRuleJSON + `,"colour":"red"}`, 400},
		{"truncated", `{"type":"required",`, 400},
		{"trailing data", `{` + goodRuleJSON + `}{}`, 400},
		{"missing type", `{"severity":"error","message":"m","enabled":true}`, 400},
		{"33 KiB", pad, 413},
	} {
		t.Run(c.name, func(t *testing.T) {
			rec, req := draftReq("PUT", "/x", c.body)
			req.SetPathValue("key", "k")
			edit(rec, req)
			if rec.Code != c.status {
				t.Errorf("status = %d (%.80s), want %d", rec.Code, rec.Body.String(), c.status)
			}
		})
	}
	if calls != 0 {
		t.Errorf("fake called %d times, want 0", calls)
	}
}

func TestStaffDraftsHandlers_PublishBody(t *testing.T) {
	var got []time.Time
	h := StaffPublishDraftHandler(func(_ context.Context, from time.Time) (DraftPublished, error) {
		got = append(got, from)
		return DraftPublished{Version: 5, EffectiveFrom: from.Format("2006-01-02"), RuleCount: 3}, nil
	}, nil)
	yesterday := time.Now().UTC().AddDate(0, 0, -1).Format("2006-01-02")
	for name, body := range map[string]string{
		"empty object": `{}`, "month 13": `{"effective_from":"2026-13-01"}`,
		"year zero": `{"effective_from":"0000-01-01"}`, "yesterday": `{"effective_from":"` + yesterday + `"}`,
		"unknown field": `{"effective_from":"3001-01-01","x":1}`,
	} {
		rec, req := draftReq("POST", "/x", body)
		h(rec, req)
		if rec.Code != 400 {
			t.Errorf("%s = %d (%s), want 400", name, rec.Code, rec.Body.String())
		}
	}
	if len(got) != 0 {
		t.Fatalf("fake called %d times on bad bodies", len(got))
	}
	tomorrow := time.Now().UTC().AddDate(0, 0, 1).Format("2006-01-02")
	rec, req := draftReq("POST", "/x", `{"effective_from":"`+tomorrow+`"}`)
	h(rec, req)
	if rec.Code != 200 || len(got) != 1 || got[0].Format("2006-01-02") != tomorrow {
		t.Fatalf("tomorrow = %d, calls %v, want 200 with %s", rec.Code, got, tomorrow)
	}
	if want := []string{"effective_from", "rule_count", "rule_set_version_id", "version"}; !reflect.DeepEqual(bodyKeys(t, rec), want) {
		t.Errorf("body keys = %v, want %v", bodyKeys(t, rec), want)
	}
	today := time.Now().UTC().Format("2006-01-02")
	rec, req = draftReq("POST", "/x", `{"effective_from":"`+today+`"}`)
	h(rec, req)
	if rec.Code != 200 {
		t.Errorf("today = %d, want 200", rec.Code)
	}
	rec, req = draftReq("POST", "/x", `{"effective_from":"3001-01-01","pad":"`+strings.Repeat("a", 9<<10)+`"}`)
	h(rec, req)
	if rec.Code != 413 {
		t.Errorf("9 KiB = %d, want 413", rec.Code)
	}
}

func TestStaffDraftsHandlers_PublishErrorMapping(t *testing.T) {
	for _, c := range []struct {
		err    error
		status int
		msg    string
	}{
		{fmt.Errorf("%w: the draft has no rules", ErrDraftInvalid), 409, "the draft has no rules"},
		{fmt.Errorf("%w: the draft does not evaluate: boom", ErrDraftInvalid), 409, "the draft does not evaluate: boom"},
		{ErrStartDateInPast, 400, "start date must be today or later"},
		{db.ErrNotStaff, 403, "forbidden"},
		{errors.New("boom"), 500, "internal error"},
	} {
		h := StaffPublishDraftHandler(func(context.Context, time.Time) (DraftPublished, error) { return DraftPublished{}, c.err }, nil)
		rec, req := draftReq("POST", "/x", `{"effective_from":"3001-01-01"}`)
		h(rec, req)
		if rec.Code != c.status || errText(t, rec) != c.msg {
			t.Errorf("%v = %d %q, want %d %q", c.err, rec.Code, errText(t, rec), c.status, c.msg)
		}
	}
}

func TestValidateDraftRule(t *testing.T) {
	yes := true
	str := func(s string) *string { return &s }
	good := func() draftRuleRequest {
		return draftRuleRequest{Type: "required", Target: "invoice.currency", Severity: "error", Message: "m", Enabled: &yes}
	}
	with := func(f func(r *draftRuleRequest)) draftRuleRequest {
		r := good()
		f(&r)
		return r
	}
	k64 := strings.Repeat("a", 64)
	for _, c := range []struct {
		name string
		key  string
		in   draftRuleRequest
		ok   bool
	}{
		{"good", "ok-key", good(), true},
		{"64-char key", k64, good(), true},
		{"65-char key", k64 + "a", good(), false},
		{"underscore key", "Bad_Key", good(), false},
		{"double dash key", "a--b", good(), false},
		{"leading dash key", "-a", good(), false},
		{"empty key", "", good(), false},
		{"unknown type", "k", with(func(r *draftRuleRequest) { r.Type = "nope" }), false},
		{"fatal severity", "k", with(func(r *draftRuleRequest) { r.Severity = "fatal" }), false},
		{"params array", "k", with(func(r *draftRuleRequest) { r.Params = json.RawMessage(`[]`) }), false},
		{"params string", "k", with(func(r *draftRuleRequest) { r.Params = json.RawMessage(`"x"`) }), false},
		{"params 16 KiB", "k", with(func(r *draftRuleRequest) {
			r.Params = json.RawMessage(`{"p":"` + strings.Repeat("a", 16<<10-8) + `"}`)
		}), true},
		{"params 16 KiB + 1", "k", with(func(r *draftRuleRequest) {
			r.Params = json.RawMessage(`{"p":"` + strings.Repeat("a", 16<<10-7) + `"}`)
		}), false},
		{"params null", "k", with(func(r *draftRuleRequest) { r.Params = json.RawMessage(`null`) }), true},
		{"when blank", "k", with(func(r *draftRuleRequest) { r.When = str("") }), false},
		{"when no compile", "k", with(func(r *draftRuleRequest) { r.When = str("invoice.") }), false},
		{"when ok", "k", with(func(r *draftRuleRequest) { r.When = str("invoice.vat > 0") }), true},
		{"cel without expr", "k", with(func(r *draftRuleRequest) { r.Type = "cel" }), false},
		{"cel expr no compile", "k", with(func(r *draftRuleRequest) { r.Type = "cel"; r.Params = json.RawMessage(`{"expr":"1 +"}`) }), false},
		{"cel expr non-string", "k", with(func(r *draftRuleRequest) { r.Type = "cel"; r.Params = json.RawMessage(`{"expr":1}`) }), false},
		{"cel ok", "k", with(func(r *draftRuleRequest) { r.Type = "cel"; r.Params = json.RawMessage(`{"expr":"invoice.vat > 0"}`) }), true},
		{"format bad pattern", "k", with(func(r *draftRuleRequest) { r.Type = "format/regex"; r.Params = json.RawMessage(`{"pattern":"("}`) }), false},
		{"format no pattern", "k", with(func(r *draftRuleRequest) { r.Type = "format/regex" }), false},
		{"format ok", "k", with(func(r *draftRuleRequest) { r.Type = "format/regex"; r.Params = json.RawMessage(`{"pattern":"^a$"}`) }), true},
		{"blank message", "k", with(func(r *draftRuleRequest) { r.Message = "  " }), false},
		{"501-rune message", "k", with(func(r *draftRuleRequest) { r.Message = strings.Repeat("😀", 501) }), false},
		{"500-rune message", "k", with(func(r *draftRuleRequest) { r.Message = strings.Repeat("😀", 500) }), true},
		{"target 201", "k", with(func(r *draftRuleRequest) { r.Target = strings.Repeat("a", 201) }), false},
		{"target 200", "k", with(func(r *draftRuleRequest) { r.Target = strings.Repeat("a", 200) }), true},
		{"enabled absent", "k", with(func(r *draftRuleRequest) { r.Enabled = nil }), false},
	} {
		t.Run(c.name, func(t *testing.T) {
			_, err := validateDraftRule(c.key, c.in)
			if (err == nil) != c.ok {
				t.Errorf("err = %v, want ok=%v", err, c.ok)
			}
		})
	}
}

func TestValidateDraftRule_NormalisesBody(t *testing.T) {
	yes := true
	v, err := validateDraftRule("k", draftRuleRequest{Type: "required", Severity: "info", Message: "  hi  ", Enabled: &yes})
	if err != nil {
		t.Fatal(err)
	}
	if v.message != "hi" || v.params != "{}" || v.when != nil || v.target != "" {
		t.Errorf("got %+v, want trimmed message, params {}, no when, empty target", v)
	}
}

// A valid body padded with trailing spaces to exactly the limit passes; one byte more is 413.
func TestStaffDraftsHandlers_BodyLimitsAreExact(t *testing.T) {
	var calls int
	add := StaffAddDraftRuleHandler(func(context.Context, string, validated) (DraftRuleResult, error) {
		calls++
		return DraftRuleResult{}, nil
	}, nil)
	edit := StaffEditDraftRuleHandler(func(context.Context, string, validated) (DraftRuleEdited, error) {
		calls++
		return DraftRuleEdited{}, nil
	}, nil)
	publish := StaffPublishDraftHandler(func(context.Context, time.Time) (DraftPublished, error) { calls++; return DraftPublished{}, nil }, nil)
	padTo := func(body string, n int) string { return body + strings.Repeat(" ", n-len(body)) }
	for _, c := range []struct {
		name  string
		h     http.HandlerFunc
		body  string
		limit int
	}{
		{"add", add, `{"key":"k",` + goodRuleJSON + `}`, 32 << 10},
		{"edit", edit, `{` + goodRuleJSON + `}`, 32 << 10},
		{"publish", publish, `{"effective_from":"3001-01-01"}`, 8 << 10},
	} {
		before := calls
		rec, req := draftReq("POST", "/x", padTo(c.body, c.limit))
		req.SetPathValue("key", "k")
		c.h(rec, req)
		if rec.Code/100 != 2 || calls != before+1 {
			t.Errorf("%s at the limit = %d (%.80s), want 2xx and one call", c.name, rec.Code, rec.Body.String())
		}
		rec, req = draftReq("POST", "/x", padTo(c.body, c.limit+1))
		req.SetPathValue("key", "k")
		c.h(rec, req)
		if rec.Code != 413 || calls != before+1 {
			t.Errorf("%s one byte over = %d, want 413 and no further call", c.name, rec.Code)
		}
	}
}

func TestStaffDraftsHandlers_TestBodies(t *testing.T) {
	var calls int
	var got map[string]any
	h := StaffTestDraftHandler(func(_ context.Context, inv map[string]any) (DraftTestResult, error) {
		calls++
		got = inv
		return DraftTestResult{}, nil
	}, nil)
	pad := func(n int) string { b := `{"invoice":{}}`; return b + strings.Repeat(" ", n-len(b)) }
	for _, c := range []struct {
		name, body string
		status     int
	}{
		{"missing invoice", `{}`, 400},
		{"null", `{"invoice":null}`, 400},
		{"array", `{"invoice":[]}`, 400},
		{"string", `{"invoice":"x"}`, 400},
		{"unknown field", `{"invoice":{},"x":1}`, 400},
		{"1 MiB + 1", pad(1<<20 + 1), 413},
	} {
		t.Run(c.name, func(t *testing.T) {
			rec, req := draftReq("POST", "/x", c.body)
			h(rec, req)
			if rec.Code != c.status {
				t.Errorf("status = %d (%.80s), want %d", rec.Code, rec.Body.String(), c.status)
			}
			if c.status == 400 && c.name != "unknown field" && errText(t, rec) != "invoice must be an object" {
				t.Errorf("error = %q, want invoice must be an object", errText(t, rec))
			}
		})
	}
	if calls != 0 {
		t.Fatalf("fake called %d times on a refused body, want 0", calls)
	}
	rec, req := draftReq("POST", "/x", pad(1<<20))
	h(rec, req)
	if rec.Code != 200 || calls != 1 || got == nil {
		t.Errorf("1 MiB body = %d, calls %d, want 200 and one call", rec.Code, calls)
	}

	for _, c := range []struct {
		err    error
		status int
		msg    string
	}{
		{ErrNoDraft, 404, "no draft"},
		{fmt.Errorf("%w: the draft has no rules", ErrDraftInvalid), 409, "the draft has no rules"},
		{ErrNoActiveRuleSet, 503, "no rule set in force"},
		{db.ErrNotStaff, 403, "forbidden"},
	} {
		h := StaffTestDraftHandler(func(context.Context, map[string]any) (DraftTestResult, error) { return DraftTestResult{}, c.err }, nil)
		rec, req := draftReq("POST", "/x", `{"invoice":{}}`)
		h(rec, req)
		if rec.Code != c.status || errText(t, rec) != c.msg {
			t.Errorf("%v maps to %d %q, want %d %q", c.err, rec.Code, errText(t, rec), c.status, c.msg)
		}
	}
}
